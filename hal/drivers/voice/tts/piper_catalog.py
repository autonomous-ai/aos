"""Curated Piper voice catalogue — the list the device offers for download."""

import os

VOICES_BASE = "https://huggingface.co/rhasspy/piper-voices/resolve/main"

BINARY_URL = (
    "https://github.com/rhasspy/piper/releases/download/2023.11.14-2/"
    "piper_linux_aarch64.tar.gz"
)

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
        "license": "public domain",
        "requires_attribution": False,
        "size_mb": 64,
    },
    "en_US-kristin-medium": {
        "language": "English (US) — alt voice",
        "lang_code": "en",
        "path": "en/en_US/kristin/medium",
        "license": "public domain",
        "requires_attribution": False,
        "size_mb": 64,
    },
    "en_US-libritts_r-medium": {
        "language": "English (US) — multi-speaker",
        "lang_code": "en",
        "path": "en/en_US/libritts_r/medium",
        "license": "CC BY 4.0",
        "requires_attribution": True,
        "size_mb": 79,
    },
    "vi_VN-vais1000-medium": {
        "language": "Tiếng Việt",
        "lang_code": "vi",
        "path": "vi/vi_VN/vais1000/medium",
        "license": "CC BY 4.0",
        "requires_attribution": True,
        "size_mb": 63,
    },
    "es_ES-davefx-medium": {
        "language": "Español",
        "lang_code": "es",
        "path": "es/es_ES/davefx/medium",
        "license": "CC0",
        "requires_attribution": False,
        "size_mb": 63,
    },
    "de_DE-thorsten-medium": {
        "language": "Deutsch",
        "lang_code": "de",
        "path": "de/de_DE/thorsten/medium",
        "license": "CC0",
        "requires_attribution": False,
        "size_mb": 63,
    },
    "pt_BR-faber-medium": {
        "language": "Português (Brasil)",
        "lang_code": "pt",
        "path": "pt/pt_BR/faber/medium",
        "license": "CC0",
        "requires_attribution": False,
        "size_mb": 63,
    },
    "fr_FR-siwis-medium": {
        "language": "Français",
        "lang_code": "fr",
        "path": "fr/fr_FR/siwis/medium",
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
