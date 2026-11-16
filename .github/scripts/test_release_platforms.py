"""Release routing must produce a macOS artifact without silently omitting it."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("release-platforms.sh")
SECRETS = ("MACOS_DEVELOPER_ID_P12_BASE64", "MACOS_DEVELOPER_ID_P12_PASSWORD", "APP_STORE_CONNECT_API_KEY_ID", "APP_STORE_CONNECT_API_ISSUER_ID", "APP_STORE_CONNECT_API_KEY_P8_BASE64")

class ReleasePlatformsTest(unittest.TestCase):
    def run_check(self, configured):
        env = {k: v for k, v in os.environ.items() if k not in SECRETS and k not in ("BLUR_REQUIRE_TRUSTED_MACOS", "GITHUB_STEP_SUMMARY")}
        env.update(configured)
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "output"
            env["GITHUB_OUTPUT"] = str(output)
            result = subprocess.run(["bash", str(SCRIPT)], env=env, capture_output=True, text=True)
            return result, output.read_text() if output.exists() else ""

    def test_no_credentials_still_builds_development_app(self):
        result, output = self.run_check({})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(output, "macos_mode=development\n")

    def test_complete_credentials_select_notarized_app(self):
        result, output = self.run_check({key: "test-value" for key in SECRETS})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(output, "macos_mode=notarized\n")

    def test_trusted_requirement_never_falls_back(self):
        result, output = self.run_check({"BLUR_REQUIRE_TRUSTED_MACOS": "true"})
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(output, "")
        self.assertIn("MACOS_DEVELOPER_ID_P12_BASE64", result.stderr)

    def test_partial_credentials_fail_without_disclosing_values(self):
        for missing in SECRETS:
            with self.subTest(missing=missing):
                result, output = self.run_check({key: "private-sentinel" for key in SECRETS if key != missing})
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(output, "")
                self.assertIn(missing, result.stderr)
                self.assertNotIn("private-sentinel", result.stdout + result.stderr)

    def test_invalid_policy_is_rejected(self):
        result, _ = self.run_check({"BLUR_REQUIRE_TRUSTED_MACOS": "maybe"})
        self.assertNotEqual(result.returncode, 0)
