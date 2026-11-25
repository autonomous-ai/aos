#!/usr/bin/env python3
"""Build HAL's offline wheelhouse for one device platform from hal/uv.lock.

The archive lets `software-update hal` install HAL's locked dependencies with
`uv pip install --offline --no-index --find-links`, without reaching the
Python package index or GitHub. It holds:

  requirements.txt   one `name==version` per package the device installs
  wheels/*.whl       exactly those wheels, checked against the lock's sha256
  LOCK_SHA256        sha256 of the uv.lock it was built from
  PLATFORM           e.g. linux-aarch64-aec

Packages are the ones `uv export` lists for the device's extras, filtered by
their markers for the target (Linux, CPython 3.12, the given machine). Registry
wheels are picked by tag for that platform (manylinux up to --glibc); direct
wheel URLs are downloaded as locked; git sources are built into a wheel; and
sdist-only packages (which the device compiles today) are compiled in a
linux/<machine> Python 3.12 container (Docker, glibc 2.31 by default), so the
device never compiles them.

Run with `packaging` available, for example:
  uv run --no-project --with packaging python3 scripts/release/build_hal_wheelhouse.py \
      --extra aec --out dist/hal-wheelhouse-linux-aarch64-aec.tar.gz
`--dry-run` resolves and prints the plan without downloading anything.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
import tomllib
import urllib.request

from packaging.markers import Marker
from packaging.requirements import Requirement
from packaging.tags import Tag, compatible_tags, cpython_tags
from packaging.utils import canonicalize_name, parse_wheel_filename

ROOT = Path(__file__).resolve().parents[2]


def target_environment(machine: str) -> dict[str, str]:
    return {
        "implementation_name": "cpython",
        "implementation_version": "3.12.0",
        "os_name": "posix",
        "platform_machine": machine,
        "platform_python_implementation": "CPython",
        "platform_release": "",
        "platform_system": "Linux",
        "platform_version": "",
        "python_full_version": "3.12.0",
        "python_version": "3.12",
        "sys_platform": "linux",
        "extra": "",
    }


def supported_tags(machine: str, glibc: tuple[int, int]) -> list[Tag]:
    platforms = [f"manylinux_{glibc[0]}_{minor}_{machine}" for minor in range(glibc[1], 16, -1)]
    platforms += [f"manylinux2014_{machine}", f"linux_{machine}"]
    tags = list(cpython_tags((3, 12), abis=["cp312", "abi3", "none"], platforms=platforms))
    tags += list(compatible_tags((3, 12), interpreter="cp312", platforms=platforms))
    return tags


def export_requirements(hal_dir: Path, extras: list[str]) -> list[str]:
    command = ["uv", "export", "--project", str(hal_dir), "--frozen", "--no-hashes",
               "--no-emit-project", "--no-header", "--no-annotate"]
    for extra in extras:
        command += ["--extra", extra]
    output = subprocess.run(command, check=True, capture_output=True, text=True).stdout
    return [line.strip() for line in output.splitlines()
            if line.strip() and not line.lstrip().startswith(("#", "-"))]


def device_extras(extra: str) -> list[str]:
    # Mirrors software-update: hardware + the device extra, plus pipecat except on Reachy.
    extras = ["hardware", extra]
    if extra != "reachy":
        extras.append("pipecat")
    return extras


def plan(hal_dir: Path, extra: str, machine: str, glibc: tuple[int, int]) -> tuple[list[dict], str]:
    lock_bytes = (hal_dir / "uv.lock").read_bytes()
    lock = tomllib.loads(lock_bytes.decode())
    packages = {}
    for package in lock.get("package", []):
        packages.setdefault(canonicalize_name(package["name"]), []).append(package)
    environment = target_environment(machine)
    ranking = {tag: index for index, tag in enumerate(supported_tags(machine, glibc))}

    items, problems = [], []
    for line in export_requirements(hal_dir, device_extras(extra)):
        requirement = Requirement(line)
        if requirement.marker and not requirement.marker.evaluate(environment):
            continue
        name = canonicalize_name(requirement.name)
        version = None
        if requirement.specifier and len(requirement.specifier) == 1:
            version = next(iter(requirement.specifier)).version
        candidates = packages.get(name, [])
        if requirement.url:
            locked = [p for p in candidates if "git" in p.get("source", {}) or "url" in p.get("source", {})]
        else:
            locked = [p for p in candidates if p.get("version") == version]
        if len(locked) != 1:
            problems.append(f"{line}: {len(locked)} matching lock entries")
            continue
        package = locked[0]
        source = package.get("source", {})
        item = {"name": package["name"], "version": package["version"]}
        if "git" in source:
            item.update(kind="git", url=source["git"])
        else:
            best = None
            for wheel in package.get("wheels", []):
                filename = wheel["url"].rsplit("/", 1)[-1]
                rank = min((ranking[tag] for tag in parse_wheel_filename(filename)[3] if tag in ranking),
                           default=None)
                if rank is not None and (best is None or rank < best[0]):
                    best = (rank, wheel, filename)
            if best is None:
                sdist = package.get("sdist")
                if not sdist:
                    problems.append(f"{package['name']}=={package['version']}: no wheel or sdist for linux"
                                    f" {machine} cp312 (glibc <= {glibc[0]}.{glibc[1]})")
                    continue
                item.update(kind="sdist", url=sdist["url"], filename=sdist["url"].rsplit("/", 1)[-1],
                            sha256=sdist.get("hash", "").removeprefix("sha256:"), size=sdist.get("size", 0))
                items.append(item)
                continue
            _, wheel, filename = best
            item.update(kind="wheel", url=wheel["url"], filename=filename,
                        sha256=wheel.get("hash", "").removeprefix("sha256:"), size=wheel.get("size", 0))
        items.append(item)
    if not any(canonicalize_name(i["name"]) == "setuptools" for i in items):
        problems.append("setuptools is not in the set; the editable HAL install needs it")
    if problems:
        raise SystemExit("cannot build the wheelhouse:\n  " + "\n  ".join(problems))
    return items, hashlib.sha256(lock_bytes).hexdigest()


def download(url: str, destination: Path, digest: str) -> None:
    with urllib.request.urlopen(url, timeout=300) as response, destination.open("wb") as out:
        shutil.copyfileobj(response, out)
    if digest and hashlib.sha256(destination.read_bytes()).hexdigest() != digest:
        raise SystemExit(f"sha256 mismatch for {url}")


def build_git_wheel(url: str, wheels: Path, work: Path) -> str:
    repo, _, rest = url.partition("?rev=")
    revision = rest.split("#", 1)[0] or url.rsplit("#", 1)[-1]
    checkout = work / "git-src"
    subprocess.run(["git", "clone", "--quiet", repo, str(checkout)], check=True)
    subprocess.run(["git", "-C", str(checkout), "checkout", "--quiet", revision], check=True)
    before = set(wheels.iterdir())
    subprocess.run(["uv", "build", "--wheel", "--out-dir", str(wheels), str(checkout)], check=True)
    built = [p for p in set(wheels.iterdir()) - before if p.suffix == ".whl"]
    if len(built) != 1 or not built[0].name.endswith("-none-any.whl"):
        raise SystemExit(f"{repo}: expected one pure-Python wheel, got {[p.name for p in built]}")
    shutil.rmtree(checkout)
    return built[0].name


# System headers the sdists need; installed in the build container only.
BUILD_PACKAGES = "build-essential portaudio19-dev swig"


def build_sdists(sdists: Path, wheels: Path, machine: str, image: str) -> None:
    platform = {"aarch64": "linux/arm64", "x86_64": "linux/amd64"}[machine]
    script = (f"apt-get update -qq && apt-get install -y -qq {BUILD_PACKAGES} >/dev/null && "
              "pip wheel --quiet --no-deps -w /out /in/*")
    subprocess.run(["docker", "run", "--rm", "--platform", platform,
                    "-v", f"{sdists}:/in:ro", "-v", f"{wheels}:/out", image, "bash", "-c", script], check=True)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--hal-dir", type=Path, default=ROOT / "hal")
    parser.add_argument("--extra", default="aec", help="device extra: aec (default) or reachy")
    parser.add_argument("--machine", default="aarch64")
    parser.add_argument("--glibc", default=os.environ.get("HAL_WHEELHOUSE_GLIBC", "2.35"),
                        help="newest glibc the target devices run (default 2.35)")
    parser.add_argument("--out", type=Path, help="output .tar.gz")
    parser.add_argument("--builder-image", default=os.environ.get("HAL_WHEELHOUSE_IMAGE", "python:3.12-bullseye"),
                        help="container image that compiles sdist-only packages")
    parser.add_argument("--dry-run", action="store_true", help="print the plan; download nothing")
    args = parser.parse_args()
    glibc = tuple(int(part) for part in args.glibc.split("."))
    items, lock_sha = plan(args.hal_dir, args.extra, args.machine, glibc)
    platform = f"linux-{args.machine}-{args.extra}"

    if args.dry_run:
        size = sum(int(i.get("size") or 0) for i in items)
        for item in items:
            print(f"{item['kind']:5} {item['name']}=={item['version']}  {item.get('filename', item['url'])}")
        built = [i["name"] for i in items if i["kind"] == "sdist"]
        if built:
            print(f"compiled in {args.builder_image} (linux/{args.machine}): {', '.join(built)}")
        print(json.dumps({"platform": platform, "lock_sha256": lock_sha, "packages": len(items),
                          "registry_wheel_bytes": size}))
        return 0
    if not args.out:
        parser.error("--out is required unless --dry-run")

    with tempfile.TemporaryDirectory() as temporary:
        work = Path(temporary)
        stage = work / "stage"
        wheels = stage / "wheels"
        wheels.mkdir(parents=True)
        sdists = work / "sdists"
        sdists.mkdir()
        lines = []
        for item in items:
            if item["kind"] == "git":
                build_git_wheel(item["url"], wheels, work)
            elif item["kind"] == "sdist":
                download(item["url"], sdists / item["filename"], item["sha256"])
            else:
                download(item["url"], wheels / item["filename"], item["sha256"])
            lines.append(f"{item['name']}=={item['version']}")
        if any(sdists.iterdir()):
            build_sdists(sdists, wheels, args.machine, args.builder_image)
        (stage / "requirements.txt").write_text("\n".join(lines) + "\n")
        (stage / "LOCK_SHA256").write_text(lock_sha + "\n")
        (stage / "PLATFORM").write_text(platform + "\n")
        args.out.parent.mkdir(parents=True, exist_ok=True)
        with tarfile.open(args.out, "w:gz") as tar:
            for path in sorted(stage.rglob("*")):
                tar.add(path, arcname=str(path.relative_to(stage)), recursive=False)
    digest = hashlib.sha256(args.out.read_bytes()).hexdigest()
    print(json.dumps({"platform": platform, "lock_sha256": lock_sha, "sha256": digest,
                      "path": str(args.out), "packages": len(items)}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
