"""Release scripts stop when a pinned mirrored file is not on the CDN mirror (no upload)."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

RELEASE = Path(__file__).resolve().parents[1]
PINS = "MIRROR_PINS='\ncodex/1.0.0/a.tar.gz " + "a" * 64 + "\nnode/n.deb " + "b" * 64 + "\n'\n"


class MirrorCheckTests(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        (self.tmp / "pins").write_text(PINS)
        bin_dir = self.tmp / "bin"
        bin_dir.mkdir()
        # Fake curl: succeeds only for URLs listed in $SERVED; logs every URL.
        (bin_dir / "curl").write_text(
            '#!/bin/bash\nfor a in "$@"; do url="$a"; done\necho "$url" >> "$LOG"\n'
            'grep -qxF "$url" "$SERVED"\n')
        (bin_dir / "curl").chmod(0o755)
        self.env = {**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}", "LOG": str(self.tmp / "log"),
                    "SERVED": str(self.tmp / "served"), "MIRROR_PINS_FILE": str(self.tmp / "pins"),
                    "MIRROR_CHECK_BASE": "https://cdn.example/os/mirror"}

    def run_check(self, served, *keys):
        (self.tmp / "served").write_text("".join(f"https://cdn.example/os/mirror/{k}\n" for k in served))
        return subprocess.run(
            ["bash", "-c", 'source "$1/ota-config.sh"; source "$1/mirror-check.sh"; shift; mirror_require "$@"',
             "_", str(RELEASE), "HINT", *keys], env=self.env, capture_output=True, text=True)

    def checked(self):
        log = self.tmp / "log"
        return log.read_text().split() if log.exists() else []

    def test_pinned_and_served_passes(self):
        result = self.run_check(["codex/1.0.0/a.tar.gz"], "codex/1.0.0/a.tar.gz")
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_pinned_but_missing_fails_with_hint(self):
        result = self.run_check([], "codex/1.0.0/a.tar.gz", "node/n.deb")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("codex/1.0.0/a.tar.gz", result.stderr)
        self.assertIn("node/n.deb", result.stderr)
        self.assertIn("Mirror them first with: HINT", result.stderr)

    def test_unpinned_version_is_not_checked(self):
        result = self.run_check([], "codex/2.0.0/a.tar.gz")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.checked(), [])
        self.assertIn("no sha256 pin", result.stderr)

    def test_release_scripts_check_before_publishing(self):
        for name in ("upload-codex.sh", "upload-opencode.sh", "upload-picoclaw.sh", "upload-hermes.sh",
                     "upload-device.sh", "upload-setup.sh"):
            text = (RELEASE / name).read_text()
            with self.subTest(script=name):
                self.assertIn("mirror-check.sh", text)
                check = text.index("mirror_require")
                for publish in ("ota_metadata_publish", "gsutil", "ota_metadata_fetch"):
                    if publish in text:
                        self.assertLess(check, text.index(publish), publish)

    def test_software_update_pins_parse(self):
        env = {**self.env, "MIRROR_PINS_FILE": str(RELEASE.parent / "provision/software-update")}
        result = subprocess.run(
            ["bash", "-c", 'source "$1/ota-config.sh"; source "$1/mirror-check.sh"; '
             'mirror_repo_pin uv/0.12.23/uv-aarch64-unknown-linux-gnu.tar.gz', "_", str(RELEASE)],
            env=env, capture_output=True, text=True)
        self.assertRegex(result.stdout.strip(), r"^[0-9a-f]{64}$")


if __name__ == "__main__":
    unittest.main()
