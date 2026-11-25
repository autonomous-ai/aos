"""STOI model path + download-on-first-use from cloud storage."""

import hashlib
import logging
import os
import shutil
import urllib.request
from pathlib import Path

logger = logging.getLogger(__name__)

_CDN_BASE: str = os.environ.get(
    "HAL_SPEAKER_MODEL_CDN_BASE", "https://storage.googleapis.com/autonomous-models"
)
# CDN copy of the same objects (same paths below it), tried before _CDN_BASE
# with a short timeout, so a copy that is missing or slow costs seconds. A
# copy is kept only when it matches the object's pinned sha256. Empty
# disables it.
_MIRROR_BASE: str = os.environ.get("HAL_SPEAKER_MODEL_MIRROR_BASE", "https://cdn.autonomous.ai/models")
# Socket timeout for one download, so an unreachable host fails instead of hanging.
_DOWNLOAD_TIMEOUT_S: float = 30.0
# Connect and response-header timeout for the CDN copy tried first; its body
# reads then get _DOWNLOAD_TIMEOUT_S.
_QUICK_TIMEOUT_S: float = 5.0

_CDN_OBJECTS: dict[str, str] = {
    "squimm_stoi.onnx": "onnx_models/squimm_stoi.onnx",
}
# SHA-256 of each object as _CDN_BASE serves it; a mirror copy must match.
_OBJECT_SHA256: dict[str, str] = {
    "squimm_stoi.onnx": "b05fdf25472c89597f6e9a67bd154a8bb4a29590dd10f2104c52942fbf50ced1",
}


def _remotes_for(local_path: Path) -> list[tuple[str, bool]]:
    """(url, is_mirror) for a model in try order, resolved by its basename ([] if unknown)."""
    obj = _CDN_OBJECTS.get(local_path.name)
    if obj is None:
        return []
    urls: list[tuple[str, bool]] = []
    # A mirror copy is only asked for an object with a pinned hash.
    pinned = local_path.name in _OBJECT_SHA256
    for base, mirror in ((_MIRROR_BASE if pinned else "", True), (_CDN_BASE, False)):
        base = base.strip().rstrip("/")
        if base:
            url = f"{base}/{obj}"
            if all(url != u for u, _ in urls):
                urls.append((url, mirror))
    return urls


def _sha256_of(path: Path) -> str:
    digest = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _set_read_timeout(resp, timeout: float) -> None:
    """Give an open urllib response's socket a new timeout for the body reads.
    urlopen's own timeout covers connecting and the response headers."""
    raw = getattr(getattr(resp, "fp", None), "raw", None)
    sock = getattr(raw, "_sock", None)
    if sock is not None:
        sock.settimeout(timeout)


def _download_url(url: str, dest: Path, timeout: float = _DOWNLOAD_TIMEOUT_S,
                  sha256: str = "") -> None:
    """Atomic download from a direct URL. ``timeout`` bounds connecting and the
    response headers; the body reads use _DOWNLOAD_TIMEOUT_S.

    Downloads to a per-PID temp file then atomically renames into place, so a crash/kill
    mid-download never leaves a truncated file a later run would mistake for a complete
    cached model.
    """
    dest.parent.mkdir(parents=True, exist_ok=True)
    tmp: Path = dest.with_suffix(dest.suffix + f".part.{os.getpid()}")
    logger.info("[stoi] downloading %s -> %s", url, dest)
    try:
        with urllib.request.urlopen(url, timeout=timeout) as response, \
                open(tmp, "wb") as out_file:
            _set_read_timeout(response, _DOWNLOAD_TIMEOUT_S)
            shutil.copyfileobj(response, out_file)
        if sha256 and _sha256_of(tmp) != sha256:
            raise ValueError("sha256 mismatch")
        tmp.replace(dest)
        logger.info("[stoi] download complete: %s", dest)
    except Exception as exc:
        if tmp.exists():
            try:
                tmp.unlink()
            except OSError:
                pass
        raise RuntimeError(f"Failed to download {url}: {exc}") from exc


def ensure_stoi_model(model_path: str) -> str:
    """Ensure the STOI model exists at ``model_path``, downloading if needed."""
    path = Path(model_path)
    if path.exists():
        return str(path)
    remotes = _remotes_for(path)
    if not remotes:
        raise FileNotFoundError(
            f"STOI model not found: {path}. No download URL is known for "
            f"'{path.name}' — set HAL_SPEAKER_PROC_STOI_MODEL_PATH to a "
            "pre-provisioned file, or add the basename to _CDN_OBJECTS."
        )
    last: Exception | None = None
    for remote, mirror in remotes:
        try:
            if mirror:
                _download_url(remote, path, _QUICK_TIMEOUT_S, _OBJECT_SHA256[path.name])
            else:
                _download_url(remote, path)
            return str(path)
        except RuntimeError as exc:
            last = exc
            logger.warning("[stoi] %s", exc)
    raise last
