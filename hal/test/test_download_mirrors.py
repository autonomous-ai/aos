"""Model, voice and engine downloads try a CDN copy first, quickly, then
upstream; the internet check accepts any of its probes."""

import hashlib
import importlib.util
import io
import time
from pathlib import Path

from hal.drivers.voice.speaker_recognizer.audio_processors import model_store as stoi_store
from hal.drivers.voice.tts import piper_catalog

_spec = importlib.util.spec_from_file_location(
    "piper_download_mirrors", Path(__file__).parents[1] / "routes/piper_download.py",
)
piper_dl = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(piper_dl)


def test_stoi_tries_cdn_copy_quickly_then_primary(tmp_path, monkeypatch):
    monkeypatch.setattr(stoi_store, "_CDN_BASE", "https://primary.test/base")
    monkeypatch.setattr(stoi_store, "_MIRROR_BASE", "https://mirror.test/models/")
    tried = []

    def fake_download(url, dest, timeout=stoi_store._DOWNLOAD_TIMEOUT_S, sha256=""):
        tried.append((url, timeout, sha256))
        if url.startswith("https://mirror.test"):
            raise RuntimeError(f"Failed to download {url}: HTTP Error 404")
        dest.write_bytes(b"model")

    monkeypatch.setattr(stoi_store, "_download_url", fake_download)
    path = tmp_path / "squimm_stoi.onnx"
    assert stoi_store.ensure_stoi_model(str(path)) == str(path)
    assert tried == [
        ("https://mirror.test/models/onnx_models/squimm_stoi.onnx", stoi_store._QUICK_TIMEOUT_S,
         stoi_store._OBJECT_SHA256["squimm_stoi.onnx"]),
        ("https://primary.test/base/onnx_models/squimm_stoi.onnx", stoi_store._DOWNLOAD_TIMEOUT_S, ""),
    ]


def test_stoi_mirror_copy_must_match_the_pin(tmp_path, monkeypatch):
    class Resp:
        def __init__(self, body):
            self.body = body

        def __enter__(self):
            import io
            return io.BytesIO(self.body)

        def __exit__(self, *a):
            return False

    monkeypatch.setattr(stoi_store.urllib.request, "urlopen", lambda url, timeout: Resp(b"wrong"))
    dest = tmp_path / "squimm_stoi.onnx"
    try:
        stoi_store._download_url("https://mirror.test/x", dest, 5.0, "0" * 64)
    except RuntimeError as exc:
        assert "sha256 mismatch" in str(exc)
    else:
        raise AssertionError("a copy with the wrong hash must not be kept")
    assert not dest.exists()


def test_stoi_mirror_can_be_disabled(monkeypatch):
    monkeypatch.setattr(stoi_store, "_CDN_BASE", "https://primary.test")
    monkeypatch.setattr(stoi_store, "_MIRROR_BASE", "")
    assert stoi_store._remotes_for(Path("squimm_stoi.onnx")) == [
        ("https://primary.test/onnx_models/squimm_stoi.onnx", False),
    ]
    assert stoi_store._remotes_for(Path("unknown.onnx")) == []


def test_piper_mirror_paths(monkeypatch):
    monkeypatch.setattr(piper_catalog, "MIRROR_BASE", "https://mirror.test/piper")
    assert piper_catalog.binary_mirror_url() == (
        "https://mirror.test/piper/2023.11.14-2/piper_linux_aarch64.tar.gz"
    )
    onnx, sidecar = piper_catalog.voice_urls(piper_catalog.DEFAULT_VOICE)
    m_onnx, m_sidecar = piper_catalog.voice_mirror_urls(piper_catalog.DEFAULT_VOICE)
    assert onnx.startswith(piper_catalog.VOICES_BASE)
    assert m_onnx.endswith("/voices/en/en_US/ljspeech/medium/en_US-ljspeech-medium.onnx")
    assert m_sidecar == m_onnx + ".json" and sidecar == onnx + ".json"
    assert piper_catalog.voice_mirror_urls("not-a-voice") is None
    monkeypatch.setattr(piper_catalog, "MIRROR_BASE", "")
    assert piper_catalog.binary_mirror_url() is None
    assert piper_catalog.voice_mirror_urls(piper_catalog.DEFAULT_VOICE) is None


def test_piper_worker_tries_cdn_copy_quickly_then_upstream(tmp_path, monkeypatch):
    tried = []

    def fake(url, dest, w_from, w_to, track, timeout, sha256):
        tried.append((url, timeout))
        if url == "https://m/x":
            Path(dest + ".part").write_bytes(b"half")
            raise OSError("404")
        Path(dest).write_bytes(b"ok")

    monkeypatch.setattr(piper_dl, "_download", fake)
    dest = str(tmp_path / "v.onnx")
    piper_dl._download_any(piper_dl._urls({"url": "https://up/x", "quick": ["https://m/x"]}),
                           dest, 0, 100, False, "0" * 64)
    assert tried == [("https://m/x", piper_dl.QUICK_TIMEOUT_S), ("https://up/x", piper_dl.DOWNLOAD_TIMEOUT_S)]
    assert Path(dest).read_bytes() == b"ok"
    assert not Path(dest + ".part").exists()
    assert piper_dl._urls({"url": "https://up/x"}) == [("https://up/x", piper_dl.DOWNLOAD_TIMEOUT_S)]


class _Resp:
    def __init__(self, body):
        self._body = io.BytesIO(body)
        self.headers = {"Content-Length": str(len(body))}

    def __enter__(self):
        return self

    def __exit__(self, *a):
        return False

    def read(self, n):
        return self._body.read(n)


def _serve(monkeypatch, bodies):
    tried = []

    def urlopen(url, timeout):
        tried.append(url)
        return _Resp(bodies[url])

    monkeypatch.setattr(piper_dl.urllib.request, "urlopen", urlopen)
    monkeypatch.setattr(piper_dl, "_flush", lambda *a, **k: None)
    return tried


def test_piper_download_rejects_mismatch_and_tries_next_url(tmp_path, monkeypatch):
    good = b"the real voice"
    pin = hashlib.sha256(good).hexdigest()
    tried = _serve(monkeypatch, {"https://m/v.onnx": b"something else", "https://up/v.onnx": good})
    dest = str(tmp_path / "v.onnx")
    piper_dl._download_any(piper_dl._urls({"url": "https://up/v.onnx", "quick": ["https://m/v.onnx"]}),
                           dest, 0, 100, True, pin)
    assert tried == ["https://m/v.onnx", "https://up/v.onnx"]
    assert Path(dest).read_bytes() == good
    assert not Path(dest + ".part").exists()


def test_piper_download_fails_cleanly_when_no_url_matches(tmp_path, monkeypatch):
    _serve(monkeypatch, {"https://m/v.onnx": b"x", "https://up/v.onnx": b"y"})
    dest = str(tmp_path / "v.onnx")
    try:
        piper_dl._download_any(piper_dl._urls({"url": "https://up/v.onnx", "quick": ["https://m/v.onnx"]}),
                               dest, 0, 100, False, "0" * 64)
    except ValueError as exc:
        assert "sha256 mismatch" in str(exc)
    else:
        raise AssertionError("a file matching no pin must not install")
    assert not Path(dest).exists() and not Path(dest + ".part").exists()


def test_piper_download_needs_a_pin(tmp_path, monkeypatch):
    tried = _serve(monkeypatch, {"https://up/v.onnx": b"y"})
    dest = str(tmp_path / "v.onnx")
    try:
        piper_dl._download_any([("https://up/v.onnx", 60)], dest, 0, 100, False, "")
    except ValueError:
        pass
    else:
        raise AssertionError("a download without a pin must not install")
    assert tried == [] and not Path(dest).exists()


def test_piper_catalog_pins_every_file():
    assert len(piper_catalog.BINARY_SHA256) == 64
    for name in piper_catalog.CATALOG:
        onnx, sidecar = piper_catalog.voice_sha256(name)
        assert len(onnx) == 64 and len(sidecar) == 64, name
    assert piper_catalog.voice_sha256("not-a-voice") is None


def test_has_internet_accepts_any_probe(monkeypatch):
    from hal import app_state

    monkeypatch.setattr(app_state, "_cloud_probe_target", lambda: ("cloud.test", 443))
    monkeypatch.setattr(app_state, "_tcp_probe", lambda host, port: host == "8.8.8.8")
    monkeypatch.setattr(app_state, "_tls_probe", lambda host, port: False)
    assert app_state._has_internet() is True

    monkeypatch.setattr(app_state, "_tcp_probe", lambda host, port: host == "223.5.5.5")
    assert app_state._has_internet() is True

    monkeypatch.setattr(app_state, "_tcp_probe", lambda host, port: False)
    monkeypatch.setattr(app_state, "_tls_probe", lambda host, port: host == "cloud.test")
    assert app_state._has_internet() is True

    monkeypatch.setattr(app_state, "_tls_probe", lambda host, port: False)
    assert app_state._has_internet() is False

    monkeypatch.setattr(app_state, "_cloud_probe_target", lambda: None)
    monkeypatch.setattr(app_state, "_tls_probe", lambda host, port: True)
    assert app_state._has_internet() is False


def test_cloud_probe_target_only_for_autonomous_hosts(monkeypatch):
    from hal import app_state
    import hal.config as hal_config

    for base, want in [
        ("https://api.example.autonomous.ai/v1", ("api.example.autonomous.ai", 443)),
        ("https://api.openai.com/v1", None),
        ("http://api.example.autonomous.ai/v1", None),
        ("", None),
    ]:
        monkeypatch.setattr(hal_config, "_os_cfg_get", lambda key, default="", b=base: b)
        assert app_state._cloud_probe_target() == want, base


class _StallServer:
    """HTTP server sending headers at once, then the body after `stall` s."""

    def __init__(self, body: bytes, stall: float):
        import http.server
        import threading

        class H(http.server.BaseHTTPRequestHandler):
            def do_GET(h):
                h.send_response(200)
                h.send_header("Content-Length", str(len(body)))
                h.end_headers()
                h.wfile.flush()
                time.sleep(stall)
                h.wfile.write(body)

            def log_message(h, *a):
                pass

        self.srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), H)
        self.url = f"http://127.0.0.1:{self.srv.server_address[1]}/f"
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()

    def close(self):
        self.srv.shutdown()
        self.srv.server_close()


def test_piper_quick_copy_survives_a_stall_longer_than_the_quick_timeout(tmp_path, monkeypatch):
    body = b"voice bytes"
    srv = _StallServer(body, stall=0.6)
    monkeypatch.setattr(piper_dl, "_flush", lambda *a, **k: None)
    try:
        dest = str(tmp_path / "v.onnx")
        piper_dl._download(srv.url, dest, 0, 100, True, 0.2, hashlib.sha256(body).hexdigest())
        assert Path(dest).read_bytes() == body
    finally:
        srv.close()


def test_stoi_quick_copy_survives_a_stall_longer_than_the_quick_timeout(tmp_path):
    body = b"stoi bytes"
    srv = _StallServer(body, stall=0.6)
    try:
        dest = tmp_path / "squimm_stoi.onnx"
        stoi_store._download_url(srv.url, dest, 0.2, hashlib.sha256(body).hexdigest())
        assert dest.read_bytes() == body
    finally:
        srv.close()


def _recording_probes(monkeypatch, tcp, tls=lambda host, port: False):
    from hal import app_state

    calls = []

    def tcp_probe(host, port):
        calls.append(host)
        return tcp(host, port)

    def tls_probe(host, port):
        calls.append(host)
        return tls(host, port)

    monkeypatch.setattr(app_state, "_cloud_probe_target", lambda: ("cloud.test", 443))
    monkeypatch.setattr(app_state, "_tcp_probe", tcp_probe)
    monkeypatch.setattr(app_state, "_tls_probe", tls_probe)
    monkeypatch.setattr(app_state, "_INTERNET_PROBE_HEDGE_S", 0.2)
    monkeypatch.setattr(app_state, "_INTERNET_PROBE_TIMEOUT_S", 1.0)
    return app_state, calls


def test_has_internet_asks_only_the_first_resolver_when_it_answers(monkeypatch):
    app_state, calls = _recording_probes(monkeypatch, lambda host, port: host == "8.8.8.8")
    assert app_state._has_internet() is True
    time.sleep(0.3)
    assert calls == ["8.8.8.8"]


def test_has_internet_starts_the_others_when_the_first_is_slow(monkeypatch):
    def tcp(host, port):
        if host == "8.8.8.8":
            time.sleep(0.8)
            return False
        return True

    app_state, calls = _recording_probes(monkeypatch, tcp)
    started = time.monotonic()
    assert app_state._has_internet() is True
    took = time.monotonic() - started
    assert 0.15 <= took < 0.6, took
    time.sleep(0.1)
    assert calls[0] == "8.8.8.8" and set(calls[1:]) == {"223.5.5.5", "cloud.test"}


def test_has_internet_starts_the_others_at_once_when_the_first_fails(monkeypatch):
    app_state, calls = _recording_probes(monkeypatch, lambda host, port: False,
                                         tls=lambda host, port: True)
    started = time.monotonic()
    assert app_state._has_internet() is True
    assert time.monotonic() - started < 0.15
    assert calls[0] == "8.8.8.8" and "cloud.test" in calls


def test_has_internet_gives_up_at_the_overall_bound(monkeypatch):
    def hang(host, port):
        time.sleep(3)
        return True

    app_state, _ = _recording_probes(monkeypatch, hang, tls=hang)
    started = time.monotonic()
    assert app_state._has_internet() is False
    assert time.monotonic() - started < 1.3


def test_piper_voices_come_from_a_fixed_revision():
    rev = piper_catalog.VOICES_REVISION
    assert len(rev) == 40 and all(c in "0123456789abcdef" for c in rev)
    for name in piper_catalog.CATALOG:
        for url in piper_catalog.voice_urls(name):
            assert f"/resolve/{rev}/" in url, url
