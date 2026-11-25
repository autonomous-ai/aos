"""Model, voice and engine downloads try a CDN copy first, quickly, then
upstream; the internet check accepts any of its probes."""

import importlib.util
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

    def fake(url, dest, w_from, w_to, track, timeout):
        tried.append((url, timeout))
        if url == "https://m/x":
            Path(dest + ".part").write_bytes(b"half")
            raise OSError("404")
        Path(dest).write_bytes(b"ok")

    monkeypatch.setattr(piper_dl, "_download", fake)
    dest = str(tmp_path / "v.onnx")
    piper_dl._download_any(piper_dl._urls({"url": "https://up/x", "quick": ["https://m/x"]}),
                           dest, 0, 100, False)
    assert tried == [("https://m/x", piper_dl.QUICK_TIMEOUT_S), ("https://up/x", piper_dl.DOWNLOAD_TIMEOUT_S)]
    assert Path(dest).read_bytes() == b"ok"
    assert not Path(dest + ".part").exists()
    assert piper_dl._urls({"url": "https://up/x"}) == [("https://up/x", piper_dl.DOWNLOAD_TIMEOUT_S)]


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
