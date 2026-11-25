"""CDN-first downloads, resume and region-aware sources in software-update (no network)."""

import hashlib
import io
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import tarfile
import tempfile
import unittest

SCRIPT = Path(__file__).resolve().parents[1] / "software-update"
STORAGE = "https://storage.example/ota-bucket"
CDN = "https://cdn.example"


def function(name):
    match = re.search(rf"^{re.escape(name)}\(\) \{{\n.*?^\}}$", SCRIPT.read_text(), re.M | re.S)
    if match is None:
        raise AssertionError(f"Missing shell function: {name}")
    return match.group(0)


def functions(*names):
    return "\n".join(function(n) for n in names) + "\n"


# A fake curl: serves files from $SERVE/<host>/<path>, logs every call to $EVENTS,
# honours -o, -D and -C -, and can cut a transfer short once per URL ($CUT_ONCE).
FAKE_CURL = r'''
import os, pathlib, sys
args = sys.argv[1:]
url = args[-1]
with open(os.environ["EVENTS"], "a") as log:
    log.write("curl " + " ".join(args) + "\n")
host_path = url.split("://", 1)[1]
source = pathlib.Path(os.environ["SERVE"]) / host_path
out = pathlib.Path(args[args.index("-o") + 1])
headers = pathlib.Path(args[args.index("-D") + 1]) if "-D" in args else None
if not source.is_file():
    if headers: headers.write_text("HTTP/1.1 404 Not Found\r\n\r\n")
    sys.exit(22)
data = source.read_bytes()
offset = out.stat().st_size if ("-C" in args and out.exists()) else 0
cut_marker = pathlib.Path(os.environ["SERVE"]) / (".cut-" + host_path.replace("/", "_"))
chunk = data[offset:]
cut = os.environ.get("CUT_ONCE") == host_path and not cut_marker.exists()
if cut:
    cut_marker.write_text("1")
    chunk = chunk[: len(chunk) // 2]
if headers:
    status = "206 Partial Content" if offset else "200 OK"
    extra = f"Content-Range: bytes {offset}-{len(data) - 1}/{len(data)}\r\n" if offset else ""
    headers.write_text(f"HTTP/1.1 {status}\r\nContent-Length: {len(data) - offset}\r\n{extra}\r\n")
with out.open("ab" if offset else "wb") as f:
    f.write(chunk)
sys.exit(18 if cut else 0)
'''


class MirrorTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.serve = self.root / "serve"
        self.serve.mkdir()
        self.events = self.root / "events"
        self.curl = self.root / "curl.py"
        self.curl.write_text(FAKE_CURL)

    def publish(self, url, data):
        path = self.serve / url.split("://", 1)[1]
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)

    def bash(self, code, **env):
        self.events.unlink(missing_ok=True)
        prelude = (f'OTA_STORAGE_PREFIX="{STORAGE}"\nOTA_CDN_BASE="{CDN}"\n'
                   f'OS_MIRROR_BASE="{CDN}/os/mirror"\n'
                   'MIRROR_PINS="${TEST_PINS:-}"\n'
                   'curl() { python3 "$FAKE_CURL" "$@"; }\nprogress_report() { :; }\n'
                   'event() { printf "%s\\n" "$*" >> "$EVENTS"; }\n')
        result = subprocess.run(["/bin/bash", "-c", prelude + code], capture_output=True, text=True,
                                env={**os.environ, "FAKE_CURL": str(self.curl), "SERVE": str(self.serve),
                                     "EVENTS": str(self.events), "TMPDIR": str(self.root),
                                     "OS_CONFIG_JSON": str(self.root / "config.json"), **env})
        events = self.events.read_text().splitlines() if self.events.exists() else []
        return result, events

    def curled(self, events):
        return [line.split()[-1] for line in events if line.startswith("curl ")]

    def test_candidates_map_storage_and_cdn_both_ways(self):
        code = functions("ota_url_candidates") + (
            f'ota_url_candidates "{STORAGE}/os/ota/hal/1.zip"; echo --\n'
            f'ota_url_candidates "{CDN}/os/ota/metadata.json"; echo --\n'
            'ota_url_candidates "https://github.com/x/y"; echo --\n'
            f'ota_url_candidates "{STORAGE}x/os/a.zip"; echo --\n'
            'ota_url_candidates ""\n')
        result, _ = self.bash(code)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.split("--\n"), [
            f"{CDN}/os/ota/hal/1.zip\n{STORAGE}/os/ota/hal/1.zip\n",
            f"{CDN}/os/ota/metadata.json\n{STORAGE}/os/ota/metadata.json\n",
            "https://github.com/x/y\n",
            f"{STORAGE}x/os/a.zip\n",
            "",
        ])

    def download(self, url, digest, *fallbacks, **env):
        dest = self.root / "artifact"
        code = functions("progress_download_sample", "ota_url_candidates", "download_from", "download_verified")
        code += 'sleep() { :; }\n'
        code += f'download_verified "{url}" "{dest}" "{digest}" ' + " ".join(f'"{f}"' for f in fallbacks) + "\n"
        result, events = self.bash(code, **env)
        return result, events, dest

    def test_cdn_first_then_storage(self):
        data = b"hal-zip" * 100
        self.publish(f"{STORAGE}/os/ota/hal/1.zip", data)
        result, events, dest = self.download(f"{STORAGE}/os/ota/hal/1.zip", hashlib.sha256(data).hexdigest())
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.curled(events), [f"{CDN}/os/ota/hal/1.zip", f"{STORAGE}/os/ota/hal/1.zip"])
        self.assertEqual(dest.read_bytes(), data)

    def test_cdn_answer_skips_storage(self):
        data = b"zip"
        self.publish(f"{CDN}/os/ota/hal/1.zip", data)
        result, events, _ = self.download(f"{STORAGE}/os/ota/hal/1.zip", hashlib.sha256(data).hexdigest())
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.curled(events), [f"{CDN}/os/ota/hal/1.zip"])

    def test_checksum_mismatch_on_cdn_tries_storage(self):
        good = b"good"
        self.publish(f"{CDN}/os/ota/hal/1.zip", b"bad")
        self.publish(f"{STORAGE}/os/ota/hal/1.zip", good)
        result, _, dest = self.download(f"{STORAGE}/os/ota/hal/1.zip", hashlib.sha256(good).hexdigest())
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(dest.read_bytes(), good)

    def test_interrupted_download_resumes(self):
        data = bytes(range(256)) * 64
        self.publish(f"{CDN}/os/ota/hal/1.zip", data)
        result, events, dest = self.download(f"{STORAGE}/os/ota/hal/1.zip", hashlib.sha256(data).hexdigest(),
                                             CUT_ONCE="cdn.example/os/ota/hal/1.zip")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(dest.read_bytes(), data)
        calls = [line for line in events if line.startswith("curl ")]
        self.assertEqual(len(calls), 2, events)
        self.assertTrue(all("-C -" in call for call in calls))
        self.assertIn("resuming", result.stderr)

    def test_vendor_fallback_after_mirror(self):
        data = b"codex"
        self.publish("https://github.com/openai/codex/a.tar.gz", data)
        result, events, dest = self.download(f"{CDN}/os/mirror/codex/1/a.tar.gz", "",
                                             "https://github.com/openai/codex/a.tar.gz")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.curled(events), [f"{CDN}/os/mirror/codex/1/a.tar.gz",
                                               f"{STORAGE}/os/mirror/codex/1/a.tar.gz",
                                               "https://github.com/openai/codex/a.tar.gz"])
        self.assertEqual(dest.read_bytes(), data)

    def test_all_sources_failing_fails(self):
        result, _, _ = self.download(f"{STORAGE}/os/ota/hal/1.zip", "")
        self.assertNotEqual(result.returncode, 0)

    def test_region_reads_os_server_config_and_env(self):
        code = functions("device_region", "region_is_cn") + 'region_is_cn && echo cn || echo other\n'
        self.assertEqual(self.bash(code)[0].stdout, "other\n")
        (self.root / "config.json").write_text(json.dumps({"region": "cn"}))
        self.assertEqual(self.bash(code)[0].stdout, "cn\n")
        self.assertEqual(self.bash(code, OTA_REGION="US")[0].stdout, "other\n")

    def npm_run(self, region=None, default_fails=False, pinned=True, override="", pack_bytes=b"tarball"):
        if region:
            (self.root / "config.json").write_text(json.dumps({"region": region}))
        integrity = "sha512-" + __import__("base64").b64encode(hashlib.sha512(b"tarball").digest()).decode()
        meta = {"openclaw": {"version": "2026.9.3"}}
        if pinned:
            meta["openclaw"]["integrity"] = integrity
            meta["openclaw"]["plugins"] = {"@openclaw/discord": "sha512-discord"}
        (self.root / "meta.json").write_text(json.dumps(meta))
        (self.root / "pack").write_bytes(pack_bytes)
        code = functions("device_region", "region_is_cn", "openclaw_pinned", "openclaw_npm_registry",
                         "openclaw_npm", "sri_sha512", "install_openclaw_package", "install_openclaw_plugin",
                         "ensure_openclaw_node")
        code += (f'METADATA_PAYLOAD="{self.root}/meta.json"\nOTA_NPM_MIRROR=https://npm-mirror.example\n'
                 'npm() { event "npm env=${NPM_CONFIG_REGISTRY:-unset} $*";'
                 ' case " $* " in *" --registry "*) ;; *) [ "$DEFAULT_FAILS" = 1 ] && return 1 ;; esac;'
                 ' case "$*" in *dist.integrity*) printf "%s\\n" sha512-discord ;;'
                 ' *" pack "*) cp "$PACK" openclaw-2026.9.3.tgz; echo openclaw-2026.9.3.tgz ;;'
                 ' *engines.node*) printf "%s\\n" \'">=22"\' ;; esac; }\n'
                 'openclaw() { event "openclaw env=${NPM_CONFIG_REGISTRY:-unset} $*"; }\n'
                 'ensure_node_engine() { event "engine $*"; }\n'
                 'ensure_openclaw_node 2026.9.3 && install_openclaw_package 2026.9.3'
                 ' && install_openclaw_plugin @openclaw/discord 2026.9.3 || exit 1\n'
                 'event "after env=${NPM_CONFIG_REGISTRY:-unset}"\n')
        return self.bash(code, DEFAULT_FAILS="1" if default_fails else "0", PACK=str(self.root / "pack"),
                         OTA_NPM_REGISTRY=override)

    def test_npm_default_registry_outside_cn(self):
        result, events = self.npm_run()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(any("--registry" in e for e in events), events)
        self.assertIn("npm env=unset install -g openclaw@2026.9.3", events)

    def test_npm_no_cn_registry_after_default_fails_outside_cn(self):
        result, events = self.npm_run(default_fails=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any("npm-mirror" in e for e in events), events)

    def test_npm_cn_registry_scoped_to_each_command(self):
        result, events = self.npm_run(region="CN")
        self.assertEqual(result.returncode, 0, result.stderr)
        npm = [e for e in events if e.startswith("npm")]
        self.assertTrue(npm and all("--registry https://npm-mirror.example" in e for e in npm), events)
        self.assertTrue(all("env=unset" in e for e in npm), events)
        self.assertTrue(any(" pack openclaw@2026.9.3" in e for e in npm), events)
        self.assertTrue(any("install -g " in e and e.endswith("openclaw-2026.9.3.tgz") for e in npm), events)
        self.assertIn("openclaw env=https://npm-mirror.example plugins install @openclaw/discord@2026.9.3 --force",
                      events)
        self.assertEqual(events[-1], "after env=unset")

    def test_npm_cn_without_pinned_integrity_uses_default(self):
        result, events = self.npm_run(region="CN", pinned=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(any("--registry" in e for e in events), events)

    def test_npm_cn_tarball_mismatch_is_refused(self):
        result, events = self.npm_run(region="CN", pack_bytes=b"other bytes")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any("install -g" in e for e in events), events)

    def test_npm_explicit_registry_override(self):
        result, events = self.npm_run(override="https://other.example")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(any("--registry https://other.example" in e for e in events), events)

    def node_order(self, region=None):
        if region:
            (self.root / "config.json").write_text(json.dumps({"region": region}))
        code = functions("device_region", "region_is_cn", "ensure_node_engine")
        code += ('rm -f "$TMPDIR/upgraded"\n'
                 'node_satisfies_engine() { [ -f "$TMPDIR/upgraded" ] && return 0; return 1; }\n'
                 'install_node_from_vendor() { event vendor; [ "$VENDOR_OK" = 1 ] && touch "$TMPDIR/upgraded"; }\n'
                 'install_node_from_mirror() { event mirror; [ "$MIRROR_OK" = 1 ] && touch "$TMPDIR/upgraded"; }\n'
                 'ensure_node_engine ">=24" openclaw\n')
        return code

    def test_node_vendor_first_outside_cn(self):
        result, events = self.bash(self.node_order(), VENDOR_OK="0", MIRROR_OK="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(events, ["vendor", "mirror"])
        result, events = self.bash(self.node_order(), VENDOR_OK="1", MIRROR_OK="1")
        self.assertEqual(events[-1:], ["vendor"])

    def test_node_mirror_first_in_cn(self):
        result, events = self.bash(self.node_order("CN"), VENDOR_OK="1", MIRROR_OK="1")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(events, ["mirror"])

    def hal_modes(self, region=None, uv_ok=True, wheel_ok=True, mode=""):
        if region:
            (self.root / "config.json").write_text(json.dumps({"region": region}))
        code = functions("device_region", "region_is_cn", "hal_wheelhouse_mode", "hal_install_dependencies")
        code += ('hal_uv_sync() { event uv-sync; [ "$UV_OK" = 1 ]; }\n'
                 'hal_wheelhouse_install() { event wheelhouse; [ "$WHEEL_OK" = 1 ]; }\n'
                 'hal_install_dependencies\n')
        return self.bash(code, UV_OK="1" if uv_ok else "0", WHEEL_OK="1" if wheel_ok else "0",
                         HAL_WHEELHOUSE=mode)

    def test_hal_outside_cn_keeps_uv_sync_first(self):
        result, events = self.hal_modes()
        self.assertEqual((result.returncode, events), (0, ["uv-sync"]))
        result, events = self.hal_modes(uv_ok=False)
        self.assertEqual((result.returncode, events), (0, ["uv-sync", "wheelhouse"]))
        result, events = self.hal_modes(uv_ok=False, wheel_ok=False)
        self.assertNotEqual(result.returncode, 0)

    def test_hal_in_cn_uses_wheelhouse_first(self):
        result, events = self.hal_modes(region="CN")
        self.assertEqual((result.returncode, events), (0, ["wheelhouse"]))
        result, events = self.hal_modes(region="CN", wheel_ok=False)
        self.assertEqual((result.returncode, events), (0, ["wheelhouse", "uv-sync"]))

    def test_hal_wheelhouse_off(self):
        result, events = self.hal_modes(region="CN", uv_ok=False, mode="off")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(events, ["uv-sync"])

    def wheelhouse_archive(self):
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w:gz") as tar:
            for name, data in (("requirements.txt", b"six==1.16.0\n"), ("wheels/six.whl", b"w")):
                info = tarfile.TarInfo(name)
                info.size = len(data)
                tar.addfile(info, io.BytesIO(data))
        return buffer.getvalue()

    def wheelhouse_run(self, lock=b"lock", metadata_lock=None):
        stage = self.root / "stage"
        stage.mkdir(exist_ok=True)
        (stage / "uv.lock").write_bytes(lock)
        archive = self.wheelhouse_archive()
        url = f"{STORAGE}/os/ota/hal/wheelhouse/a.tar.gz"
        self.publish(url.replace(STORAGE, CDN), archive)
        key = f"linux-{platform.machine()}-aec"
        meta = {"hal": {"wheelhouse": {key: {
            "url": url, "sha256": hashlib.sha256(archive).hexdigest(),
            "lock_sha256": metadata_lock or hashlib.sha256(b"lock").hexdigest()}}}}
        (self.root / "meta.json").write_text(json.dumps(meta))
        code = functions("progress_download_sample", "ota_url_candidates", "download_from",
                         "download_verified", "hal_wheelhouse_install")
        code += (f'sleep() {{ :; }}\nMETADATA_PAYLOAD="{self.root}/meta.json"\nHAL_EXTRA=aec\n'
                 f'HAL_DIR="{self.root}/opt/hal"\nUV_BIN=uv\n'
                 'run_uv_with_activity() { event "$*"; [ "$2" != venv ] || mkdir -p .venv; }\n'
                 f'cd "{stage}"\nhal_wheelhouse_install && hal_wheelhouse_install\n')
        return self.bash(code)

    def test_wheelhouse_installs_offline_and_reuses_download(self):
        result, events = self.wheelhouse_run()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(len(self.curled(events)), 1, events)  # second run reuses the unpacked copy
        installs = [e for e in events if e.startswith("uv pip install")]
        self.assertEqual(len(installs), 4, events)
        for line in installs:
            self.assertIn("--offline --no-index --find-links", line)
        self.assertTrue(any(line.endswith("--no-deps -e .") for line in installs))
        cache = self.root / "opt" / ".hal-wheelhouse"
        self.assertEqual(len(list(cache.iterdir())), 1)

    def test_wheelhouse_for_another_lock_is_skipped(self):
        result, events = self.wheelhouse_run(metadata_lock="0" * 64)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.curled(events), [])
        self.assertIn("another uv.lock", result.stderr)

    def uv_setup(self, mirror_ok=True, vendor_ok=True, pinned=True):
        triple = {"arm64": "aarch64", "aarch64": "aarch64", "x86_64": "x86_64"}[platform.machine()]
        triple += "-unknown-linux-gnu"
        buffer = io.BytesIO()
        with tarfile.open(fileobj=buffer, mode="w:gz") as tar:
            for name in ("uv", "uvx"):
                data = b"#!/bin/sh\necho uv 0.12.23\n"
                info = tarfile.TarInfo(f"uv-{triple}/{name}")
                info.size = len(data)
                info.mode = 0o755
                tar.addfile(info, io.BytesIO(data))
        asset = f"uv-{triple}.tar.gz"
        archive = buffer.getvalue()
        self.publish(f"{CDN}/os/mirror/uv/0.12.23/{asset}", archive if mirror_ok else b"other bytes")
        self.publish(f"https://github.com/astral-sh/uv/releases/download/0.12.23/{asset}",
                     archive if vendor_ok else b"other bytes")
        pins = f"uv/0.12.23/{asset} {hashlib.sha256(archive).hexdigest()}" if pinned else ""
        home = self.root / "uvhome"
        code = functions("mirror_pin", "fetch_pinned", "install_uv_from_mirror") + \
            f'HAL_UV_PIN=0.12.23\nHAL_UV_HOME="{home}"\ninstall_uv_from_mirror\n'
        result, events = self.bash(code, TEST_PINS=pins)
        return result, events, home

    def test_uv_pinned_from_mirror(self):
        result, events, home = self.uv_setup()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((home / "uv").exists() and (home / "uvx").exists())
        self.assertEqual(len(self.curled(events)), 1)

    def test_uv_mirror_mismatch_uses_pinned_vendor_file(self):
        result, events, home = self.uv_setup(mirror_ok=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(self.curled(events)[-1].startswith("https://github.com/astral-sh/uv/"))
        self.assertTrue((home / "uv").exists())

    def test_uv_both_mismatch_installs_nothing(self):
        result, _, home = self.uv_setup(mirror_ok=False, vendor_ok=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((home / "uv").exists())

    def test_uv_without_pin_skips_mirror(self):
        result, events, home = self.uv_setup(pinned=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.curled(events), [])

    def test_mirror_pin_lookup(self):
        code = functions("mirror_pin") + 'mirror_pin codex/1/a.tar.gz; echo -; mirror_pin codex/2/a.tar.gz\n'
        result, _ = self.bash(code, TEST_PINS="\ncodex/1/a.tar.gz " + "a" * 64 + "\n")
        self.assertEqual(result.stdout, "a" * 64 + "\n-\n")

if __name__ == "__main__":
    unittest.main()
