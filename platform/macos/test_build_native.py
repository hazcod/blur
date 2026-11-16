"""Synthetic cache tests: no compiler, signing, model or application runs."""
import importlib.util
import os
import shlex
from pathlib import Path
import subprocess
import tempfile
import threading
import time
import unittest

SCRIPT = Path(__file__).with_name("build_native.py")
spec = importlib.util.spec_from_file_location("blur_build_native", SCRIPT)
native = importlib.util.module_from_spec(spec)
spec.loader.exec_module(native)


class CacheTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        for name in ("FoundationModels/main.swift", "MeetingAudio/Core.swift", "MeetingAudio/Audio.swift", "ScreenText/ScreenText.swift"):
            path = self.root / "platform/macos" / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("// synthetic source " + name)
        self.calls = []
        self.lock = threading.Lock()
        self.toolchain = {"swiftc": "fixture-swiftc", "lipo": "fixture-lipo", "sdk": "/fixture/sdk", "identity": {"swift": "fixture-1", "sdk": "27.2"}}

    def invoke(self, command, **_):
        with self.lock:
            self.calls.append(command)
        if command[0] == "fixture-swiftc":
            output = Path(command[command.index("-o") + 1])
            data = b"".join(Path(p).read_bytes() for p in command if p.endswith(".swift"))
            output.write_bytes(data + command[command.index("-target") + 1].encode())
        elif "-verify_arch" in command:
            # Match lipo's option parsing: the filename plus multiple trailing
            # verify arches is not accepted. Verify each architecture separately.
            self.assertEqual(len(command), 4)
        elif "-create" in command:
            files = command[command.index("-create") + 1: command.index("-output")]
            Path(command[-1]).write_bytes(b"".join(Path(p).read_bytes() for p in files))
        return subprocess.CompletedProcess(command, 0)

    def builder(self, **kwargs):
        return native.NativeBuilder(self.root, self.root / "cache", self.toolchain, runner=self.invoke, **kwargs)

    def compiles(self):
        return sum(c[0] == "fixture-swiftc" for c in self.calls)

    def test_repeat_build_hits_both_architectures_without_touching_output(self):
        output = self.root / "output/helper"
        self.builder().build({"foundation": output}, "universal")
        self.assertEqual(self.compiles(), 2)
        before = output.stat().st_mtime_ns
        self.builder().build({"foundation": output}, "universal")
        self.assertEqual(self.compiles(), 2)
        self.assertEqual(output.stat().st_mtime_ns, before)

    def test_screen_text_build_has_its_own_sources_module_and_install_name(self):
        output = self.root / "output/libBlurScreenText.dylib"
        self.builder().build({"screen": output})
        commands = [call for call in self.calls if call[0] == "fixture-swiftc"]
        self.assertEqual(len(commands), 2)
        for command in commands:
            self.assertIn("BlurScreenText", command)
            self.assertIn("@rpath/libBlurScreenText.dylib", command)
            self.assertTrue(any("ScreenText/ScreenText.swift" in arg for arg in command))
            self.assertFalse(any("MeetingAudio/" in arg for arg in command))
        self.builder().build({"screen": output})
        self.assertEqual(self.compiles(), 2)

    def test_source_sdk_toolchain_flags_arch_and_new_file_invalidate(self):
        output = self.root / "output/meeting.dylib"
        builder = self.builder()
        builder.build({"meeting": output}, "arm64")
        self.assertEqual(self.compiles(), 1)
        source = self.root / "platform/macos/MeetingAudio/Core.swift"
        source.write_text(source.read_text() + "\n// changed")
        builder.build({"meeting": output}, "arm64")
        self.assertEqual(self.compiles(), 2)
        self.toolchain["identity"]["sdk"] = "28.0"
        builder.build({"meeting": output}, "arm64")
        self.assertEqual(self.compiles(), 3)
        self.toolchain["identity"]["swift"] = "fixture-2"
        builder.build({"meeting": output}, "arm64")
        self.assertEqual(self.compiles(), 4)
        self.builder(extra_flags=["-D", "SYNTHETIC"]).build({"meeting": output}, "arm64")
        self.assertEqual(self.compiles(), 5)
        builder.build({"meeting": output}, "x86_64")
        self.assertEqual(self.compiles(), 6)
        (source.parent / "Added.swift").write_text("// new production source")
        builder.build({"meeting": output}, "arm64")
        self.assertEqual(self.compiles(), 7)

    def test_component_change_does_not_recompile_other_component(self):
        outputs = {"foundation": self.root / "output/helper",
                   "meeting": self.root / "output/meeting.dylib",
                   "screen": self.root / "output/screen.dylib"}
        self.builder().build(outputs)
        self.assertEqual(self.compiles(), 6)
        self.calls.clear()
        source = self.root / "platform/macos/MeetingAudio/Core.swift"
        source.write_text(source.read_text() + "\n// changed meeting only")
        self.builder().build(outputs)
        commands = [call for call in self.calls if call[0] == "fixture-swiftc"]
        self.assertEqual(len(commands), 2)
        self.assertTrue(all("BlurMeetingAudio" in call for call in commands))

    def test_global_compiler_pool_is_bounded_across_all_components(self):
        active = 0
        maximum = 0
        def run(command, **kwargs):
            nonlocal active, maximum
            if command[0] != "fixture-swiftc":
                return self.invoke(command, **kwargs)
            with self.lock:
                active += 1
                maximum = max(maximum, active)
            try:
                time.sleep(0.02)
                return self.invoke(command, **kwargs)
            finally:
                with self.lock:
                    active -= 1
        outputs = {"foundation": self.root / "output/helper",
                   "meeting": self.root / "output/meeting.dylib",
                   "screen": self.root / "output/screen.dylib"}
        native.NativeBuilder(self.root, self.root / "cache", self.toolchain,
                             runner=run, jobs=2).build(outputs)
        self.assertEqual(self.compiles(), 6)
        self.assertEqual(maximum, 2)

    def test_corrupted_artifact_rebuilds_instead_of_signing_stale_cache(self):
        output = self.root / "output/helper"
        self.builder().build({"foundation": output}, "arm64")
        artifact = next((self.root / "cache").rglob("artifact"))
        artifact.write_bytes(b"corrupted")
        self.builder().build({"foundation": output}, "arm64")
        self.assertEqual(self.compiles(), 2)
        self.assertNotEqual(output.read_bytes(), b"corrupted")

    def test_failed_compile_preserves_previous_output_and_cannot_be_cache_hit(self):
        output = self.root / "output/helper"
        self.builder().build({"foundation": output}, "arm64")
        before = output.read_bytes()
        source = self.root / "platform/macos/FoundationModels/main.swift"
        source.write_text("// changed source")

        def fail(command, **_):
            raise subprocess.CalledProcessError(1, command)

        with self.assertRaises(subprocess.CalledProcessError):
            native.NativeBuilder(self.root, self.root / "cache", self.toolchain, runner=fail).build({"foundation": output}, "arm64")
        self.assertEqual(output.read_bytes(), before)
        self.builder().build({"foundation": output}, "arm64")
        self.assertEqual(self.compiles(), 2)

    def test_parallel_builds_share_lock_and_only_compile_once(self):
        errors = []

        def build(index):
            try:
                self.builder().build({"foundation": self.root / f"output/helper-{index}"}, "arm64")
            except Exception as error:
                errors.append(error)

        threads = [threading.Thread(target=build, args=(i,)) for i in range(4)]
        for thread in threads:
            thread.start()
        for thread in threads:
            thread.join()
        self.assertEqual(errors, [])
        self.assertEqual(self.compiles(), 1)

    def test_snapshot_compiles_the_hashed_source_even_if_checkout_changes(self):
        source = self.root / "platform/macos/FoundationModels/main.swift"
        original = source.read_bytes()

        def change(command, **kwargs):
            if command[0] == "fixture-swiftc":
                source.write_text("// changed during compilation")
            return self.invoke(command, **kwargs)

        output = self.root / "output/helper"
        native.NativeBuilder(self.root, self.root / "cache", self.toolchain, runner=change).build({"foundation": output}, "arm64")
        self.assertTrue(output.read_bytes().startswith(original))
        self.builder().build({"foundation": output}, "arm64")
        self.assertEqual(self.compiles(), 2)


class MakeTests(unittest.TestCase):
    def dry_run(self, target):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name)
        binary = root / "bin"
        binary.mkdir()
        # The only go command evaluated during make -n is signer discovery.
        # A synthetic executable keeps this test independent of Keychain/TCC.
        go = binary / "go"
        go.write_text('#!/bin/sh\necho resolution >> "$SIGNER_LOG"\nprintf fixture\n')
        go.chmod(0o755)
        env = os.environ.copy()
        env.update(PATH=str(binary) + os.pathsep + env["PATH"],
                   SIGNER_LOG=str(root / "signer.log"), HOME=str(root))
        env.pop("MACOS_CODESIGN_IDENTITY", None)
        result = subprocess.run(["make", "--no-print-directory", "-n", target,
                                 "MACOS_APP=" + str(root / "Blur.app"),
                                 "MACOS_BINARY=" + str(root / "blur"),
                                 "VERSION=fixture", "COMMIT=fixture", "BUILD_DATE=fixture"],
                                cwd=SCRIPT.parents[2], env=env, check=True,
                                capture_output=True, text=True)
        log = root / "signer.log"
        resolutions = log.read_text().splitlines() if log.exists() else []
        return result.stdout, resolutions

    def test_signer_is_resolved_once_across_recursive_build_stages(self):
        for target in ("macos-app", "macos-app-stable"):
            with self.subTest(target=target):
                output, resolutions = self.dry_run(target)
                self.assertEqual(resolutions, ["resolution"])
                signings = [shlex.split(line) for line in output.splitlines()
                            if line.startswith("codesign ") and "--sign " in line]
                self.assertEqual(len(signings), 4)
                self.assertTrue(all(command[command.index("--sign") + 1] == "fixture"
                                    for command in signings))
                self.assertTrue(all("runtime" in command for command in signings))
                self.assertLess(output.index("macos-app-identity-check"),
                                output.index('rm -rf "' + str(Path(signings[-1][-1]).with_name("Blur.app")) + '"'))
                self.assertIn('macos-app-compile MACOS_CODESIGN_IDENTITY="fixture" BUILD_DATE="fixture"', output)

    def test_other_targets_do_not_inspect_signing_identity(self):
        _, resolutions = self.dry_run("version")
        self.assertEqual(resolutions, [])


if __name__ == "__main__":
    unittest.main()
