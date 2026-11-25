"""STOI model path + download-on-first-use from cloud storage."""

import logging
import os
import shutil
import urllib.request
from pathlib import Path

logger = logging.getLogger(__name__)

_CDN_BASE: str = os.environ.get(
    "HAL_SPEAKER_MODEL_CDN_BASE", "https://storage.googleapis.com/autonomous-models"
)
# Optional mirror of the same objects (same paths below it), tried after
# _CDN_BASE fails. Empty (the default) adds none.
_MIRROR_BASE: str = os.environ.get("HAL_SPEAKER_MODEL_MIRROR_BASE", "")
# Socket timeout for one download, so an unreachable host fails instead of hanging.
_DOWNLOAD_TIMEOUT_S: float = 30.0

_CDN_OBJECTS: dict[str, str] = {
    "squimm_stoi.onnx": "onnx_models/squimm_stoi.onnx",
}


def _remotes_for(local_path: Path) -> list[str]:
    """Download URLs for a model in try order, resolved by its basename ([] if unknown)."""
    obj = _CDN_OBJECTS.get(local_path.name)
    if obj is None:
        return []
    urls: list[str] = []
    for base in (_CDN_BASE, _MIRROR_BASE):
        base = base.strip().rstrip("/")
        if base:
            url = f"{base}/{obj}"
            if url not in urls:
                urls.append(url)
    return urls


def _download_url(url: str, dest: Path) -> None:
    """Atomic download from a direct URL.

    Downloads to a per-PID temp file then atomically renames into place, so a crash/kill
    mid-download never leaves a truncated file a later run would mistake for a complete
    cached model.
    """
    dest.parent.mkdir(parents=True, exist_ok=True)
    tmp: Path = dest.with_suffix(dest.suffix + f".part.{os.getpid()}")
    logger.info("[stoi] downloading %s -> %s", url, dest)
    try:
        with urllib.request.urlopen(url, timeout=_DOWNLOAD_TIMEOUT_S) as response, \
                open(tmp, "wb") as out_file:
            shutil.copyfileobj(response, out_file)
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
    for remote in remotes:
        try:
            _download_url(remote, path)
            return str(path)
        except RuntimeError as exc:
            last = exc
            logger.warning("[stoi] %s", exc)
    raise last
