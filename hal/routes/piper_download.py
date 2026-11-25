"""Piper download worker, run as its own transient unit so it survives `systemctl restart hal`.

Invoked as a script, never imported; must not import `hal`.
Usage: python3 piper_download.py <job-file> <spec-json>
"""

import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import sys
import tarfile
import tempfile
import time
import urllib.request

CHUNK = 256 * 1024

# Throttled to limit SD-card write wear; matches the admin page poll interval.
WRITE_INTERVAL_S = 2.0

_job_path = ""
_state: dict = {}
_last_write = 0.0


def _record(**over) -> dict:
    rec = dict(_state)
    rec.update(over)
    rec["pid"] = os.getpid()
    return rec


def _flush(force: bool = False, **over) -> None:
    """Write the job file atomically (complete record, never a patch), throttled unless forced."""
    global _last_write
    _state.update(over)
    now = time.time()
    if not force and now - _last_write < WRITE_INTERVAL_S:
        return
    _last_write = now
    tmp = _job_path + ".tmp"
    try:
        with open(tmp, "w", encoding="utf-8") as fh:
            json.dump(_record(), fh)
        os.replace(tmp, _job_path)
    except OSError:
        pass


# Socket timeout for an upstream URL, and for a mirror copy tried before it.
DOWNLOAD_TIMEOUT_S = 60
QUICK_TIMEOUT_S = 5


def _download(url: str, dest: str, w_from: int, w_to: int, track: bool,
              timeout: float = DOWNLOAD_TIMEOUT_S, sha256: str = "") -> None:
    """Fetch url to dest via a .part file, mapping progress onto a slice of the
    whole job. The file is kept only when its SHA-256 equals `sha256`."""
    if len(sha256) != 64:
        raise ValueError(f"no pinned sha256 for {url}")
    tmp = dest + ".part"
    digest = hashlib.sha256()
    with urllib.request.urlopen(url, timeout=timeout) as resp, open(tmp, "wb") as out:
        total = int(resp.headers.get("Content-Length") or 0)
        if track:
            _flush(force=True, bytes_total=total, bytes_done=0)
        got = 0
        while True:
            chunk = resp.read(CHUNK)
            if not chunk:
                break
            out.write(chunk)
            digest.update(chunk)
            got += len(chunk)
            over = {"bytes_done": got} if track else {}
            if total:
                over["percent"] = w_from + int((w_to - w_from) * got / total)
            _flush(**over)
    if digest.hexdigest() != sha256.lower():
        _discard([tmp])
        raise ValueError(f"sha256 mismatch for {url}")
    os.replace(tmp, dest)


def _download_any(urls, dest: str, w_from: int, w_to: int, track: bool,
                  sha256: str) -> None:
    """Try each (url, timeout) in order until one downloads a file matching
    sha256; raise the last error when none does."""
    last = None
    for url, timeout in urls:
        try:
            _download(url, dest, w_from, w_to, track, timeout, sha256)
            return
        except Exception as exc:  # noqa: BLE001 — any failure moves to the next URL
            last = exc
            _discard([dest + ".part"])
    raise last if last else RuntimeError("no download URL")


def _urls(spec: dict) -> list:
    """(url, timeout) in try order: the spec's mirror copies first, each with a
    short timeout, then its URL (a spec without `quick` has only the URL)."""
    quick = [(u, QUICK_TIMEOUT_S) for u in spec.get("quick") or [] if u]
    return [*quick, (spec["url"], DOWNLOAD_TIMEOUT_S)]


def _discard(paths) -> None:
    for path in paths:
        try:
            os.remove(path)
        except OSError:
            pass


def _run_voice(spec: dict) -> None:
    steps = spec["steps"]
    os.makedirs(os.path.dirname(steps[0]["dest"]), exist_ok=True)
    try:
        for step in steps:
            _download_any(_urls(step), step["dest"], step["from"], step["to"],
                          step.get("track", False), step.get("sha256", ""))
    except Exception:
        # A half-installed voice is worse than none: discard every step's files.
        _discard([p for s in steps for p in (s["dest"], s["dest"] + ".part")])
        raise


def _extract_engine(tar_path: str, destination: str) -> Path:
    """Extract release files without allowing archive paths to escape Piper."""
    root = Path(destination).resolve()
    source = root / "piper"

    def release_filter(member: tarfile.TarInfo, path: str):
        name = PurePosixPath(member.name)
        if (name.is_absolute() or ".." in name.parts
                or not name.parts or name.parts[0] != "piper"):
            raise ValueError(f"Unsafe Piper archive path: {member.name}")
        filtered = tarfile.data_filter(member, path)
        target = root / member.name
        if not target.resolve().is_relative_to(source):
            raise ValueError(f"Piper archive path escapes release: {member.name}")
        if member.issym() or member.islnk():
            link = Path(member.linkname)
            base = target.parent if member.issym() else root
            if link.is_absolute() or not (base / link).resolve().is_relative_to(source):
                raise ValueError(f"Unsafe Piper archive link: {member.name}")
        return filtered

    with tarfile.open(tar_path) as tf:
        members = tf.getmembers()
        # Check the complete manifest first, then recheck while extracting so
        # earlier symlinks cannot redirect later writes outside the release.
        for member in members:
            release_filter(member, destination)
        tf.extractall(destination, members=members, filter=release_filter)
    if not source.is_dir() or source.is_symlink():
        raise ValueError("Piper archive is missing its release directory")
    for entry in source.rglob("*"):
        if not entry.resolve().is_relative_to(source):
            raise ValueError(f"Piper archive link escapes release: {entry.name}")
        # copytree follows links. Reject directory links (including cycles),
        # while retaining shared-library file symlinks used by real releases.
        if entry.is_symlink() and not entry.is_file():
            raise ValueError(f"Invalid Piper archive file link: {entry.name}")
    return source


def _run_engine(spec: dict) -> None:
    piper_dir = spec["dir"]
    os.makedirs(piper_dir, exist_ok=True)
    with tempfile.TemporaryDirectory() as td:
        tar_path = os.path.join(td, "piper.tar.gz")
        _download_any(_urls(spec), tar_path, 0, 85, True, spec.get("sha256", ""))
        _flush(force=True, percent=88)
        src = _extract_engine(tar_path, os.path.join(td, "extracted"))
        for entry in os.listdir(src):
            s, d = os.path.join(src, entry), os.path.join(piper_dir, entry)
            if os.path.isdir(s):
                shutil.copytree(s, d, dirs_exist_ok=True)
            else:
                shutil.copy2(s, d)
    os.chmod(os.path.join(piper_dir, "piper"), 0o755)
    os.makedirs(spec["voices_dir"], exist_ok=True)


def main() -> int:
    global _job_path
    _job_path, spec_raw = sys.argv[1], sys.argv[2]
    spec = json.loads(spec_raw)
    _state.update(
        active=True, kind=spec["kind"], target=spec["target"], percent=0,
        bytes_done=0, bytes_total=0, error="", done=False, claimed_at=time.time(),
    )
    _flush(force=True)
    try:
        if spec["kind"] == "engine":
            _run_engine(spec)
        else:
            _run_voice(spec)
    except Exception as e:
        _flush(force=True, active=False, done=True, error=str(e))
        return 1
    _flush(force=True, active=False, done=True, percent=100, error="")
    return 0


if __name__ == "__main__":
    sys.exit(main())
