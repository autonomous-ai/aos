"""Every version the repo pins for a mirrored download has a sha256 pin."""
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
UPDATER = ROOT / "scripts/provision/software-update"


def default(path, name):
    match = re.search(rf'^{name}="\$\{{{name}:-([^}}]+)\}}"', path.read_text(), re.M)
    assert match, f"{name} not found in {path}"
    return match.group(1)


def pins(path):
    match = re.search(r"^MIRROR_PINS='\n(.*?)\n'", path.read_text(), re.M | re.S)
    assert match, f"MIRROR_PINS not found in {path}"
    table = {}
    for line in match.group(1).splitlines():
        key, digest = line.split()
        assert re.fullmatch(r"[0-9a-f]{64}", digest), line
        table[key] = digest
    return table


class MirrorPinsTest(unittest.TestCase):
    def setUp(self):
        self.pins = pins(UPDATER)

    def required(self):
        uv = default(UPDATER, "HAL_UV_PIN")
        codex = default(ROOT / "runtimes/codex/install.sh", "CODEX_VERSION").removeprefix("rust-v")
        pico = default(ROOT / "runtimes/picoclaw/install.sh", "PICO_VERSION")
        opencode = default(ROOT / "runtimes/opencode/install.sh", "OPENCODE_VERSION").removeprefix("v")
        return {
            "software-update": [
                f"uv/{uv}/uv-aarch64-unknown-linux-gnu.tar.gz",
                f"uv/{uv}/uv-x86_64-unknown-linux-gnu.tar.gz",
                f"node/{default(UPDATER, 'NODE_MIRROR_DEB')}",
                f"codex/{codex}/codex-aarch64-unknown-linux-musl.tar.gz",
                f"codex/{codex}/codex-x86_64-unknown-linux-musl.tar.gz",
                f"picoclaw/{pico}/picoclaw-linux-arm64",
                f"opencode/{opencode}/opencode-linux-arm64.tar.gz",
                f"opencode/{opencode}/opencode-linux-x64.tar.gz",
                "yq/v4.46.1/yq_linux_arm64",
                "yq/v4.46.1/yq_linux_amd64",
            ],
            "runtimes/codex/install.sh": [
                f"codex/{codex}/codex-aarch64-unknown-linux-musl.tar.gz",
                f"codex/{codex}/codex-x86_64-unknown-linux-musl.tar.gz",
            ],
            "runtimes/picoclaw/install.sh": [
                f"picoclaw/{pico}/picoclaw-linux-arm64",
                "yq/v4.46.1/yq_linux_arm64",
                "yq/v4.46.1/yq_linux_amd64",
            ],
            "runtimes/opencode/install.sh": [
                f"opencode/{opencode}/opencode-linux-arm64.tar.gz",
                f"opencode/{opencode}/opencode-linux-x64.tar.gz",
            ],
            "runtimes/hermes/install.sh": ["yq/v4.46.1/yq_linux_arm64", "yq/v4.46.1/yq_linux_amd64"],
        }

    def test_pinned_versions_have_digests(self):
        for key in self.required()["software-update"]:
            self.assertIn(key, self.pins, f"software-update MIRROR_PINS has no digest for {key}")

    def test_installers_match_software_update(self):
        for rel, keys in self.required().items():
            if rel == "software-update":
                continue
            local = pins(ROOT / rel)
            for key in keys:
                self.assertIn(key, local, f"{rel} has no digest for {key}")
                self.assertEqual(local[key], self.pins.get(key), f"{rel} and software-update disagree on {key}")

    def test_yq_version_matches_installers(self):
        for rel in ("runtimes/picoclaw/install.sh", "runtimes/hermes/install.sh"):
            versions = set(re.findall(r"yq/(v[0-9.]+)/", (ROOT / rel).read_text()))
            self.assertEqual(versions, {"v4.46.1"}, rel)


if __name__ == "__main__":
    unittest.main()
