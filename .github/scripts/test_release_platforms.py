"""Exercise release routing without real signing credentials."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("release-platforms.sh")
SECRETS = (
    "MACOS_DEVELOPER_ID_P12_BASE64",
    "MACOS_DEVELOPER_ID_P12_PASSWORD",
    "APP_STORE_CONNECT_API_KEY_ID",
    "APP_STORE_CONNECT_API_ISSUER_ID",
    "APP_STORE_CONNECT_API_KEY_P8_BASE64",
)


class ReleasePlatformsTest(unittest.TestCase):
    def run_check(self, configured):
        env = {key: value for key, value in os.environ.items() if key not in SECRETS}
        env.update(configured)
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "output"
            summary = Path(directory) / "summary"
            env.update(GITHUB_OUTPUT=str(output), GITHUB_STEP_SUMMARY=str(summary))
            result = subprocess.run(
                ["bash", str(SCRIPT)], env=env, capture_output=True, text=True
            )
            return (
                result,
                output.read_text() if output.exists() else "",
                summary.read_text() if summary.exists() else "",
            )

    def test_no_credentials_releases_portable_archives_only(self):
        result, output, summary = self.run_check({})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(output, "macos_enabled=false\n")
        self.assertIn("Windows and Linux", summary)
        self.assertIn("macOS", summary)

    def test_complete_credentials_enable_signed_macos_job(self):
        result, output, _ = self.run_check({key: "test-value" for key in SECRETS})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(output, "macos_enabled=true\n")

    def test_partial_configuration_fails_without_disclosing_values(self):
        for missing in SECRETS:
            with self.subTest(missing=missing):
                result, output, _ = self.run_check(
                    {key: "private-sentinel" for key in SECRETS if key != missing}
                )
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(output, "")
                self.assertIn(missing, result.stderr)
                self.assertNotIn("private-sentinel", result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
