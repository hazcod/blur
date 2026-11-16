#!/usr/bin/env python3
"""Cache native compilation inputs; signing is always a separate bundle step."""
import argparse
from concurrent.futures import ThreadPoolExecutor
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[2]


def digest(data):
    return hashlib.sha256(data).hexdigest()


def file_digest(path):
    with Path(path).open("rb") as stream:
        checksum = hashlib.sha256()
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            checksum.update(chunk)
        return checksum.hexdigest()


def toolchain():
    env = os.environ.copy()
    selected = env.get("DEVELOPER_DIR", "")
    if not selected:
        selected = subprocess.check_output(["/usr/bin/xcode-select", "-p"], text=True).strip()
    full = Path("/Applications/Xcode.app/Contents/Developer")
    if (not selected or Path(selected).name == "CommandLineTools") and full.is_dir():
        selected = str(full)
    if selected:
        env["DEVELOPER_DIR"] = selected

    def query(*args):
        return subprocess.check_output(args, env=env, text=True, stderr=subprocess.STDOUT).strip()

    swiftc = query("/usr/bin/xcrun", "--find", "swiftc")
    lipo = query("/usr/bin/xcrun", "--find", "lipo")
    linker = query("/usr/bin/xcrun", "--find", "ld")
    sdk = query("/usr/bin/xcrun", "--sdk", "macosx", "--show-sdk-path")
    executable_hashes = {}
    for path in (Path(swiftc), Path(swiftc).with_name("swift-frontend"), Path(linker)):
        if path.is_file():
            executable_hashes[str(path.resolve())] = file_digest(path)
    sdk_settings = {}
    for name in ("SDKSettings.json", "SDKSettings.plist", "SystemVersion.plist"):
        path = Path(sdk) / name
        if path.is_file():
            sdk_settings[name] = file_digest(path)
    relevant_env = {key: value for key, value in env.items() if key.startswith("SWIFT") or key in (
        "DEVELOPER_DIR", "TOOLCHAINS", "SDKROOT", "MACOSX_DEPLOYMENT_TARGET", "PATH", "CPATH",
        "LIBRARY_PATH", "C_INCLUDE_PATH", "CPLUS_INCLUDE_PATH", "OBJC_INCLUDE_PATH", "SOURCE_DATE_EPOCH")}
    identity = {
        "swift": query(swiftc, "--version"), "executables": executable_hashes,
        "sdk_path": str(Path(sdk).resolve()), "sdk_settings": sdk_settings,
        "sdk_version": query("/usr/bin/xcrun", "--sdk", "macosx", "--show-sdk-version"),
        "sdk_build": query("/usr/bin/xcrun", "--sdk", "macosx", "--show-sdk-build-version"),
        "developer": selected, "environment": relevant_env,
    }
    return {"swiftc": swiftc, "lipo": lipo, "sdk": sdk, "identity": identity, "env": env}


class NativeBuilder:
    def __init__(self, root, cache, selected_toolchain, runner=subprocess.run, extra_flags=None, jobs=2):
        self.root = Path(root).resolve()
        self.cache = Path(cache)
        self.tools = selected_toolchain
        self.runner = runner
        self.extra_flags = list(extra_flags or [])
        self.jobs = jobs

    def sources(self, component):
        folder = {"foundation": "FoundationModels", "meeting": "MeetingAudio", "screen": "ScreenText"}[component]
        files = sorted((self.root / "platform/macos" / folder).glob("*.swift"))
        if not files:
            raise ValueError("native sources are missing: " + component)
        return {str(path.relative_to(self.root)): path.read_bytes() for path in files}

    def flags(self, component, arch):
        flags = ["-swift-version", "6", "-warnings-as-errors", "-parse-as-library", "-O",
                 "-sdk", self.tools["sdk"], "-target", arch + "-apple-macos13.0"]
        if component == "foundation":
            flags += ["-module-name", "BlurFoundationModels", "-Xlinker", "-weak_framework", "-Xlinker", "FoundationModels"]
        else:
            module = {"meeting": "BlurMeetingAudio", "screen": "BlurScreenText"}[component]
            flags += ["-module-name", module, "-emit-library", "-Xlinker", "-fatal_warnings",
                      "-Xlinker", "-install_name", "-Xlinker", "@rpath/lib" + module + ".dylib"]
        return flags + self.extra_flags

    def compile(self, component, arch, sources):
        flags = self.flags(component, arch)
        inputs = {"format": 1, "component": component, "arch": arch, "flags": flags,
                  "source_root": str(self.root), "source_map": "<snapshot>=" + str(self.root),
                  "sources": {name: digest(data) for name, data in sources.items()},
                  "toolchain": self.tools["identity"], "builder": file_digest(__file__)}
        key = digest(json.dumps(inputs, sort_keys=True, separators=(",", ":")).encode())
        entry = self.cache / component / arch / key
        entry.mkdir(parents=True, exist_ok=True, mode=0o700)
        artifact, manifest = entry / "artifact", entry / "manifest.json"
        with (entry / "build.lock").open("a+b") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            try:
                saved = json.loads(manifest.read_text())
                if saved["inputs"] == inputs and artifact.is_file() and saved["sha256"] == file_digest(artifact):
                    print("native cache hit: " + component + "/" + arch, flush=True)
                    return artifact
            except (OSError, ValueError, KeyError):
                pass
            # Compile immutable copies of exactly the bytes used in the key.
            # Concurrent checkout edits cannot poison an earlier source key.
            with tempfile.TemporaryDirectory(prefix="compile-", dir=entry) as temporary:
                temporary = Path(temporary)
                snapshot = temporary / "sources"
                for name, data in sources.items():
                    path = snapshot / name
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.write_bytes(data)
                output = temporary / "artifact"
                command = [self.tools["swiftc"]] + flags + ["-file-prefix-map", str(snapshot) + "=" + str(self.root)]
                command += [str(snapshot / name) for name in sources] + ["-o", str(output)]
                print("native compile: " + component + "/" + arch, flush=True)
                self.runner(command, check=True, env=self.tools.get("env"))
                self.runner([self.tools["lipo"], str(output), "-verify_arch", arch], check=True, env=self.tools.get("env"))
                output.chmod(0o755)
                recorded = {"inputs": inputs, "sha256": file_digest(output)}
                temporary_manifest = temporary / "manifest.json"
                temporary_manifest.write_text(json.dumps(recorded, sort_keys=True))
                os.replace(output, artifact)
                os.replace(temporary_manifest, manifest)
            return artifact

    def build(self, outputs, architecture="universal"):
        arches = ("arm64", "x86_64") if architecture == "universal" else (architecture,)
        if any(arch not in ("arm64", "x86_64") for arch in arches):
            raise ValueError("unsupported native architecture")
        sources = {component: self.sources(component) for component in outputs}
        # This global pool also bounds all-component builds to two compilers.
        with ThreadPoolExecutor(max_workers=self.jobs) as pool:
            futures = {(component, arch): pool.submit(self.compile, component, arch, sources[component])
                       for component in outputs for arch in arches}
            artifacts = {key: future.result() for key, future in futures.items()}
        for component, output in outputs.items():
            output = Path(output)
            output.parent.mkdir(parents=True, exist_ok=True)
            with tempfile.TemporaryDirectory(prefix=".blur-native-", dir=output.parent) as temporary:
                assembled = Path(temporary) / "assembled"
                parts = [str(artifacts[component, arch]) for arch in arches]
                if len(parts) == 1:
                    shutil.copyfile(parts[0], assembled)
                else:
                    self.runner([self.tools["lipo"], "-create"] + parts + ["-output", str(assembled)], check=True, env=self.tools.get("env"))
                for arch in arches:
                    self.runner([self.tools["lipo"], str(assembled), "-verify_arch", arch], check=True, env=self.tools.get("env"))
                assembled.chmod(0o755)
                if not output.is_file() or file_digest(output) != file_digest(assembled):
                    os.replace(assembled, output)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--foundation-out", type=Path)
    parser.add_argument("--meeting-out", type=Path)
    parser.add_argument("--screen-out", type=Path)
    parser.add_argument("--arch", choices=("universal", "arm64", "x86_64"), default="universal")
    parser.add_argument("--cache-dir", type=Path, default=Path(os.environ.get("BLUR_NATIVE_CACHE_DIR", str(ROOT / "dist/macos-native-cache"))))
    parser.add_argument("--jobs", type=int, choices=range(1, 5), default=2)
    args = parser.parse_args()
    outputs = {}
    if args.foundation_out:
        outputs["foundation"] = args.foundation_out
    if args.meeting_out:
        if args.meeting_out.suffix != ".dylib":
            parser.error("--meeting-out must end in .dylib")
        outputs["meeting"] = args.meeting_out
    if args.screen_out:
        if args.screen_out.suffix != ".dylib":
            parser.error("--screen-out must end in .dylib")
        outputs["screen"] = args.screen_out
    if not outputs:
        parser.error("at least one native output is required")
    NativeBuilder(ROOT, args.cache_dir, toolchain(), jobs=args.jobs).build(outputs, args.arch)


if __name__ == "__main__":
    main()
