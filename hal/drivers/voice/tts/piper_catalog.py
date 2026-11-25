"""Curated Piper voice catalogue — the list the device offers for download."""

import os

# Voices come from a fixed revision of the voices repo, so the files never
# change under their pinned digests. Moving to a newer revision means
# rechecking every sha256/json_sha256 below at that revision.
VOICES_REVISION = "5ec43b186ef915a195d3ddbcfd9916e76c503c3f"
VOICES_BASE = f"https://huggingface.co/rhasspy/piper-voices/resolve/{VOICES_REVISION}"

BINARY_URL = (
    "https://github.com/rhasspy/piper/releases/download/2023.11.14-2/"
    "piper_linux_aarch64.tar.gz"
)
# SHA-256 of the upstream engine archive. Every download, from upstream or the
# mirror, must match it (and each voice its catalogue sha256 / json_sha256).
BINARY_SHA256 = "fea0fd2d87c54dbc7078d0f878289f404bd4d6eea6e7444a77835d1537ab88eb"

# CDN copy of the same files, tried before the upstream URL with a short
# timeout (for networks where the upstream hosts are unreachable); a copy that
# is missing or slow costs seconds, then upstream is used. Layout below it:
# <version>/piper_linux_aarch64.tar.gz and voices/<path>/<name>.onnx(.json).
# Empty disables it.
MIRROR_BASE = os.environ.get(
    "HAL_PIPER_MIRROR_BASE", "https://cdn.autonomous.ai/models/piper",
).strip().rstrip("/")

CATALOG = {
    "en_US-ljspeech-medium": {
        "language": "English (US)",
        "lang_code": "en",
        "path": "en/en_US/ljspeech/medium",
        "sha256": "6f52a751e2349abe7a76735eb09dc1875298c77ea2342ffd2fef79ff81b87f22",
        "json_sha256": "141d612cc0a95ed7efc1ca936b845c2364967f2e9217c5dbfcf69fc4d6c65860",
        "license": "public domain",
        "requires_attribution": False,
        "size_mb": 64,
    },
    "en_US-kristin-medium": {
        "language": "English (US) — alt voice",
        "lang_code": "en",
        "path": "en/en_US/kristin/medium",
        "sha256": "5849957f929cbf720c258f8458692d6103fff2f0e3d3b19c8259474bb06a18d4",
        "json_sha256": "5681426d4aead22195de70531eeeeddb46493cfaffc5764b2ea3db73428b651c",
        "license": "public domain",
        "requires_attribution": False,
        "size_mb": 64,
    },
    "en_US-libritts_r-medium": {
        "language": "English (US) — multi-speaker",
        "lang_code": "en",
        "path": "en/en_US/libritts_r/medium",
        "sha256": "10bb85e071d616fcf4071f369f1799d0491492ab3c5d552ec19fb548fac13195",
        "json_sha256": "b471dc60d2d8335e819c393d196d6fbf792817f40051257b269878505bc9afb3",
        "license": "CC BY 4.0",
        "requires_attribution": True,
        "size_mb": 79,
    },
    "vi_VN-vais1000-medium": {
        "language": "Tiếng Việt",
        "lang_code": "vi",
        "path": "vi/vi_VN/vais1000/medium",
        "sha256": "0567643f67b8329a46b3f0b549befc58e1e7e21ef558677768068d23a41d8471",
        "json_sha256": "19240ab1eb13e5eb9c7dd00f9d1de5db08ff88d591c5008cbd24f1a25f51e13a",
        "license": "CC BY 4.0",
        "requires_attribution": True,
        "size_mb": 63,
    },
    "es_ES-davefx-medium": {
        "language": "Español",
        "lang_code": "es",
        "path": "es/es_ES/davefx/medium",
        "sha256": "d2c9224e122321aac3c33343957fff4d0f465226cb74c66b0f520ca3f8c0dd07",
        "json_sha256": "7d82d069eccca0f87963bd23c33166fe1ef6d24a6b231543931bfe692397f256",
        "license": "CC0",
        "requires_attribution": False,
        "size_mb": 63,
    },
    "de_DE-thorsten-medium": {
        "language": "Deutsch",
        "lang_code": "de",
        "path": "de/de_DE/thorsten/medium",
        "sha256": "fd270eb3d1ebf91d763f8a0f949b1b1bebdfefaea18c3aaf8577dd34536e064f",
        "json_sha256": "e5bb7410dcbbcc0be6f4544f6bc66612b315ab5cb3099ec44bb46d860bc7cca9",
        "license": "CC0",
        "requires_attribution": False,
        "size_mb": 63,
    },
    "pt_BR-faber-medium": {
        "language": "Português (Brasil)",
        "lang_code": "pt",
        "path": "pt/pt_BR/faber/medium",
        "sha256": "75e913eb463db0b6ac7ddf68d3e4c9ac19ace36dc3b710a81039b448352b8ffc",
        "json_sha256": "6b6801319e5a70aa4d75c63343515f55d1cd671ecd1ac511586bc16216812261",
        "license": "CC0",
        "requires_attribution": False,
        "size_mb": 63,
    },
    "fr_FR-siwis-medium": {
        "language": "Français",
        "lang_code": "fr",
        "path": "fr/fr_FR/siwis/medium",
        "sha256": "b1d946fd68d0347155fa59bce0a018924abbef3c1561181db45ca463d83963ef",
        "json_sha256": "3b3efdbcdcae930c40d8255527170ec5c87bf05e510b67f53d75fd028df40f43",
        "license": "CC BY 4.0",
        "requires_attribution": True,
        "size_mb": 63,
    },
}

# The voice a device installs when the operator turns Piper on without picking
# one. Public domain, so shipping it carries no obligation at all.
DEFAULT_VOICE = "en_US-ljspeech-medium"


def voice_urls(name: str):
    """(onnx_url, json_url) for a catalogue entry, or None if not listed."""
    meta = CATALOG.get(name)
    if not meta:
        return None
    base = f"{VOICES_BASE}/{meta['path']}/{name}"
    return f"{base}.onnx", f"{base}.onnx.json"


def binary_mirror_url():
    """The engine archive on the mirror, or None when the mirror is off."""
    if not MIRROR_BASE:
        return None
    return f"{MIRROR_BASE}/{BINARY_URL.split('/download/', 1)[1]}"


def voice_mirror_urls(name: str):
    """(onnx_url, json_url) on the mirror for a catalogue entry, or None."""
    meta = CATALOG.get(name)
    if not meta or not MIRROR_BASE:
        return None
    base = f"{MIRROR_BASE}/voices/{meta['path']}/{name}"
    return f"{base}.onnx", f"{base}.onnx.json"


def voice_sha256(name: str):
    """(onnx_sha256, json_sha256) pinned for a catalogue entry, or None."""
    meta = CATALOG.get(name)
    if not meta:
        return None
    return meta["sha256"], meta["json_sha256"]
