"""Exercise real bundle/archive contents using synthetic files, never a user app."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import plistlib
import shlex
import struct
import subprocess
import tempfile
import unittest
import zipfile

SCRIPT = Path(__file__).with_name("macos-release.py")

class MacOSReleaseTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        spec = importlib.util.spec_from_file_location("macos_release", SCRIPT)
        cls.module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(cls.module)

    def fixture(self, root):
        app = root / "Blur.app"
        for relative in ("Contents/MacOS/blur", "Contents/Helpers/blur-foundation-models", "Contents/Frameworks/libBlurMeetingAudio.dylib", "Contents/Frameworks/libBlurScreenText.dylib"):
            path = app / relative
            path.parent.mkdir(parents=True, exist_ok=True)
            # Universal Mach-O header with arm64 and x86_64 slices.
            path.write_bytes(struct.pack(">II", 0xCAFEBABE, 2) + struct.pack(">IIIII", 0x0100000C, 0, 4096, 4, 12) + struct.pack(">IIIII", 0x01000007, 3, 8192, 4, 12) + bytes(8192))
            path.chmod(0o755)
        resources = app / "Contents/Resources"
        resources.mkdir()
        (resources / "AppIcon.icns").write_bytes(b"icon fixture")
        (app / "Contents/Info.plist").write_bytes(plistlib.dumps({"CFBundleIdentifier": "com.hazcod.blur", "CFBundleExecutable": "blur", "CFBundlePackageType": "APPL", "CFBundleShortVersionString": "0.1.0", "CFBundleVersion": "1", "LSMinimumSystemVersion": "13.0", "CFBundleIconFile": "AppIcon.icns", "NSMicrophoneUsageDescription": "Explicit meeting microphone transcription.", "NSAudioCaptureUsageDescription": "Explicit meeting system audio transcription."}))
        return app

    def test_rejects_missing_screen_text_bridge(self):
        with tempfile.TemporaryDirectory() as directory:
            app = self.fixture(Path(directory))
            (app / "Contents/Frameworks/libBlurScreenText.dylib").unlink()
            with self.assertRaises(ValueError):
                self.prepare(app)

    def test_rejects_missing_meeting_bridge(self):
        with tempfile.TemporaryDirectory() as directory:
            app = self.fixture(Path(directory))
            (app / "Contents/Frameworks/libBlurMeetingAudio.dylib").unlink()
            with self.assertRaises(ValueError):
                self.prepare(app)

    def prepare(self, app, mode="development"):
        self.module.prepare(app, "0.1.0-alpha.3", "3", "a" * 40, mode)

    def test_prepare_sets_release_version_and_honest_development_metadata(self):
        with tempfile.TemporaryDirectory() as directory:
            app = self.fixture(Path(directory))
            self.prepare(app)
            info = plistlib.loads((app / "Contents/Info.plist").read_bytes())
            self.assertEqual(info["CFBundleShortVersionString"], "0.1.0")
            self.assertEqual(info["CFBundleVersion"], "3")
            self.assertEqual(info["BlurReleaseVersion"], "0.1.0-alpha.3")
            metadata = json.loads((app / "Contents/Resources/BlurRelease.json").read_text())
            self.assertEqual(metadata["signing"], "ad-hoc-development")
            self.assertFalse(metadata["developer_id_signed"])
            self.assertFalse(metadata["notarized"])
            self.assertFalse(metadata["sandbox"])
            self.assertEqual(metadata["architectures"], ["arm64", "x86_64"])
            self.assertIn("Gatekeeper", metadata["notice"])

    def test_rejects_missing_helper_wrong_identity_symlinks_and_thin_executable(self):
        for broken in ("helper", "identity", "symlink", "thin"):
            with self.subTest(broken=broken), tempfile.TemporaryDirectory() as directory:
                app = self.fixture(Path(directory))
                if broken == "helper":
                    (app / "Contents/Helpers/blur-foundation-models").unlink()
                elif broken == "identity":
                    path = app / "Contents/Info.plist"
                    info = plistlib.loads(path.read_bytes()); info["CFBundleIdentifier"] = "other.app"
                    path.write_bytes(plistlib.dumps(info))
                elif broken == "symlink":
                    os.symlink("/tmp", app / "Contents/Resources/link")
                else:
                    (app / "Contents/MacOS/blur").write_bytes(struct.pack("<II", 0xFEEDFACF, 0x0100000C))
                with self.assertRaises(ValueError):
                    self.prepare(app)

    def test_native_verification_accepts_unsandboxed_signatures_and_requires_trust(self):
        with tempfile.TemporaryDirectory() as directory:
            app = self.fixture(Path(directory)); self.prepare(app)
            calls = []
            def run(args):
                calls.append(args)
                if "--entitlements" in args:
                    return plistlib.dumps({"com.apple.security.device.audio-input": True}) if Path(args[-1]) == app else b""
                return b"Signature=adhoc\nflags=0x10000(runtime)\n"
            self.module.verify_native(app, "development", run)
            self.assertTrue(any("--deep" in call for call in calls))
            self.prepare(app, "notarized")
            with self.assertRaises(ValueError):
                self.module.verify_native(app, "notarized", run)
            with self.assertRaises(ValueError):
                self.module.verify_native(app, "development", lambda _: b"Signature=adhoc\n")

    def test_native_verification_rejects_sandbox_or_inherit_on_host_and_helper(self):
        for relative in ("", "Contents/Helpers/blur-foundation-models"):
            for entitlement in ("com.apple.security.app-sandbox", "com.apple.security.inherit"):
                for value in (True, False):
                    with self.subTest(relative=relative, entitlement=entitlement, value=value), tempfile.TemporaryDirectory() as directory:
                        app = self.fixture(Path(directory)); self.prepare(app)
                        rejected = app / relative
                        def run(args):
                            if "--entitlements" in args:
                                result = {"com.apple.security.device.audio-input": True} if Path(args[-1]) == app else {}
                                if Path(args[-1]) == rejected: result[entitlement] = value
                                return plistlib.dumps(result)
                            return b"Signature=adhoc\nflags=0x10000(runtime)\n"
                        with self.assertRaisesRegex(ValueError, "unsandboxed"):
                            self.module.verify_native(app, "development", run)

    @unittest.skipUnless(Path("/usr/bin/ditto").exists(), "macOS ditto required")
    def test_archive_rejects_sandboxed_or_missing_manifest_policy(self):
        for sandbox in (True, None):
            with self.subTest(sandbox=sandbox), tempfile.TemporaryDirectory() as directory:
                root = Path(directory); app = self.fixture(root); self.prepare(app)
                path = app / "Contents/Resources/BlurRelease.json"
                metadata = json.loads(path.read_text())
                if sandbox is None:
                    metadata.pop("sandbox", None)
                else:
                    metadata["sandbox"] = sandbox
                path.write_text(json.dumps(metadata))
                with self.assertRaisesRegex(ValueError, "unsandboxed"):
                    self.module.archive(app, root / "assets", "development", verify=lambda *_: None)

    def test_make_defaults_and_stable_build_omit_sandbox_entitlements(self):
        repository = SCRIPT.parents[2]
        for target in ("macos-app", "macos-app-stable"):
            with self.subTest(target=target), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                result = subprocess.run(["make", "--no-print-directory", "-n", target, "MACOS_CODESIGN_IDENTITY=fixture", "MACOS_APP=" + str(root / "Blur.app"), "MACOS_BINARY=" + str(root / "blur"), "BLUR_APP_GROUP_ID=", "BLUR_KEYCHAIN_ACCESS_GROUP=", "MACOS_SANDBOX_DEV_PATH="], cwd=repository, check=True, capture_output=True, text=True)
                commands = [shlex.split(line) for line in result.stdout.splitlines() if line.startswith("codesign ") and "--sign " in line]
                self.assertEqual(len(commands), 4)
                for command in commands:
                    if command[-1].endswith(".app"):
                        self.assertIn("platform/macos/Blur.runtime.entitlements", command)
                    else:
                        self.assertNotIn("--entitlements", command)

    def test_make_keeps_explicit_sandbox_compatibility_build(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            result = subprocess.run(["make", "--no-print-directory", "-n", "macos-app", "MACOS_SANDBOX=1", "MACOS_CODESIGN_IDENTITY=fixture", "MACOS_APP=" + str(root / "Blur.app"), "MACOS_BINARY=" + str(root / "blur"), "BLUR_APP_GROUP_ID=", "BLUR_KEYCHAIN_ACCESS_GROUP=", "MACOS_SANDBOX_DEV_PATH="], cwd=SCRIPT.parents[2], check=True, capture_output=True, text=True)
            commands = [shlex.split(line) for line in result.stdout.splitlines() if line.startswith("codesign ") and "--sign " in line]
            self.assertEqual(len(commands), 4)
            for command in commands:
                if command[-1].endswith(".dylib"):
                    self.assertNotIn("--entitlements", command)
                else:
                    self.assertIn("--entitlements", command)

    @unittest.skipUnless(Path("/usr/bin/ditto").exists(), "macOS ditto required")
    def test_archive_contains_real_app_helper_and_metadata_with_executable_permissions(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); app = self.fixture(root); self.prepare(app)
            archive = self.module.archive(app, root / "assets", "development", verify=lambda *_: None)
            self.assertEqual(archive.name, "Blur_0.1.0-alpha.3_macos_universal_development.zip")
            with zipfile.ZipFile(archive) as bundle:
                for name in ("Blur.app/Contents/MacOS/blur", "Blur.app/Contents/Helpers/blur-foundation-models", "Blur.app/Contents/Frameworks/libBlurMeetingAudio.dylib", "Blur.app/Contents/Frameworks/libBlurScreenText.dylib", "Blur.app/Contents/Resources/BlurRelease.json"):
                    self.assertIn(name, bundle.namelist())
                self.assertTrue(bundle.getinfo("Blur.app/Contents/MacOS/blur").external_attr >> 16 & 0o111)
            manifest = json.loads(archive.with_suffix(".json").read_text())
            self.assertFalse(manifest["sandbox"])
            self.assertEqual(manifest["archive_sha256"], hashlib.sha256(archive.read_bytes()).hexdigest())
            self.assertEqual(archive.with_suffix(".zip.sha256").read_text().split()[1], archive.name)

    @unittest.skipUnless(Path("/usr/bin/codesign").exists(), "macOS signing tools required")
    def test_real_ad_hoc_unsandboxed_signatures_survive_archive(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory); app = self.fixture(root)
            source = root / "fixture.c"
            source.write_text("int main(void) { return 0; }\n")
            slices = []
            for arch in ("arm64", "x86_64"):
                binary = root / arch
                subprocess.run(["xcrun", "clang", "-target", arch + "-apple-macos13.0", str(source), "-o", str(binary)], check=True, capture_output=True)
                slices.append(str(binary))
            for relative in self.module.BINARIES:
                subprocess.run(["lipo", "-create", *slices, "-output", str(app / relative)], check=True, capture_output=True)
            self.prepare(app)
            for path in (app / "Contents/Helpers/blur-foundation-models", app / "Contents/Frameworks/libBlurMeetingAudio.dylib", app / "Contents/Frameworks/libBlurScreenText.dylib", app):
                flags = ["--entitlements", str(SCRIPT.parents[2] / "platform/macos/Blur.runtime.entitlements")] if path == app else []
                subprocess.run(["codesign", "--force", "--sign", "-", "--options", "runtime", "--timestamp=none", *flags, str(path)], check=True, capture_output=True)
            archive = self.module.archive(app, root / "assets", "development")
            unpacked = root / "unpacked"
            subprocess.run(["ditto", "-x", "-k", str(archive), str(unpacked)], check=True, capture_output=True)
            self.module.verify_native(unpacked / "Blur.app", "development")

            # An existing local bundle cannot use the fresh-development ad-hoc
            # fallback, even when its previous signature was also ad-hoc.
            result = subprocess.run(["make", "--no-print-directory", "macos-app-preflight", "MACOS_APP=" + str(app), "MACOS_CODESIGN_IDENTITY=-"], cwd=SCRIPT.parents[2], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("existing app", result.stderr)

    def test_workflow_always_builds_macos_before_publishing(self):
        workflow = SCRIPT.parents[1] / "workflows/release.yml"
        text = workflow.read_text()
        self.assertNotIn("macos_enabled == 'true'", text)
        self.assertIn("bash .github/scripts/macos-release.sh", text)
        release_script = SCRIPT.with_suffix(".sh").read_text()
        self.assertIn("--identifier com.hazcod.blur.screen-text", release_script)
        self.assertIn("macos_mode", text)
        self.assertIn("actions/upload-artifact@", text)
