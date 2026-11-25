"""Release scripts write CDN download URLs for the default OTA storage (no upload)."""

import os
from pathlib import Path
import subprocess
import unittest

RELEASE = Path(__file__).resolve().parents[1]


def public_url(path, **env):
    clean = {k: v for k, v in os.environ.items() if k not in ("GCS_BUCKET", "OTA_PUBLIC_BASE")}
    result = subprocess.run(
        ["bash", "-c", 'source "$1/ota-config.sh"; ota_public_url "$2"', "_", str(RELEASE), path],
        env={**clean, **env}, capture_output=True, text=True, check=True)
    return result.stdout.strip()


class OTAPublicURLTests(unittest.TestCase):
    def test_default_storage_publishes_cdn_urls(self):
        self.assertEqual(public_url("os/ota/hal/0.1.310.zip"),
                         "https://cdn.autonomous.ai/os/ota/hal/0.1.310.zip")

    def test_other_buckets_keep_storage_urls(self):
        self.assertEqual(public_url("os/ota/hal/1.zip", GCS_BUCKET="test-bucket"),
                         "https://storage.googleapis.com/test-bucket/os/ota/hal/1.zip")

    def test_explicit_base_wins(self):
        self.assertEqual(public_url("/os/x.zip", OTA_PUBLIC_BASE="https://mirror.example/"),
                         "https://mirror.example/os/x.zip")

    def test_device_upload_scripts_use_public_url(self):
        for name in ("upload-os-server.sh", "upload-bootstrap.sh", "upload-hal.sh", "upload-device.sh",
                     "upload-web.sh", "upload-service.sh", "upload-autonomous-chat.sh",
                     "upload-twitch-irc.sh", "upload-claude-desktop-buddy.sh"):
            text = (RELEASE / name).read_text()
            with self.subTest(script=name):
                self.assertIn("ota_public_url", text)
                self.assertNotIn("https://storage.googleapis.com/${GCS_BUCKET}", text)


if __name__ == "__main__":
    unittest.main()
