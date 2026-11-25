"""Model, voice and engine downloads fall back to a mirror; the internet check
accepts any of its probes."""

import importlib.util
from pathlib import Path

from hal.drivers.voice.speaker_recognizer.audio_processors import model_store as stoi_store
from hal.drivers.voice.tts import piper_catalog

_spec = importlib.util.spec_from_file_location(
    "piper_download_mirrors", Path(__file__).parents[1] / "routes/piper_download.py",
)
piper_dl = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(piper_dl)


def test_stoi_tries_primary_then_mirror(tmp_path, monkeypatch):
    monkeypatch.setattr(stoi_store, "_CDN_BASE", "https://primary.test/base")
    monkeypatch.setattr(stoi_store, "_MIRROR_BASE", "https://mirror.test/models/")
    tried = []

    def fake_download(url, dest):
        tried.append(url)
        if url.startswith("https://primary.test"):
            raise RuntimeError(f"Failed to download {url}: unreachable")
        dest.write_bytes(b"model")

    monkeypatch.setattr(stoi_store, "_download_url", fake_download)
    path = tmp_path / "squimm_stoi.onnx"
    assert stoi_store.ensure_stoi_model(str(path)) == str(path)
    assert tried == [
        "https://primary.test/base/onnx_models/squimm_stoi.onnx",
        "https://mirror.test/models/onnx_models/squimm_stoi.onnx",
    ]


def test_stoi_mirror_can_be_disabled(monkeypatch):
    monkeypatch.setattr(stoi_store, "_CDN_BASE", "https://primary.test")
    monkeypatch.setattr(stoi_store, "_MIRROR_BASE", "")
    assert stoi_store._remotes_for(Path("squimm_stoi.onnx")) == [
        "https://primary.test/onnx_models/squimm_stoi.onnx",
    ]
    assert stoi_store._remotes_for(Path("unknown.onnx")) == []


def test_piper_has_no_mirror_by_default(monkeypatch):
    monkeypatch.setattr(piper_catalog, "MIRROR_BASE", "")
    assert piper_catalog.binary_urls() == [piper_catalog.BINARY_URL]
    assert piper_catalog.voice_mirror_urls(piper_catalog.DEFAULT_VOICE) is None


def test_piper_upstream_stays_first(monkeypatch):
    monkeypatch.setattr(piper_catalog, "MIRROR_BASE", "https://mirror.test/piper")
    assert piper_catalog.binary_urls() == [
        piper_catalog.BINARY_URL, "https://mirror.test/piper/2023.11.14-2/piper_linux_aarch64.tar.gz",
    ]
    onnx, sidecar = piper_catalog.voice_urls(piper_catalog.DEFAULT_VOICE)
    m_onnx, m_sidecar = piper_catalog.voice_mirror_urls(piper_catalog.DEFAULT_VOICE)
    assert onnx.startswith(piper_catalog.VOICES_BASE)
    assert m_onnx.endswith("/en/en_US/ljspeech/medium/en_US-ljspeech-medium.onnx")
    assert m_sidecar == m_onnx + ".json" and sidecar == onnx + ".json"
    assert piper_catalog.voice_mirror_urls("not-a-voice") is None


def test_piper_worker_falls_back_to_mirror(tmp_path, monkeypatch):
    tried = []

    def fake(url, dest, w_from, w_to, track):
        tried.append(url)
        if url == "https://up/x":
            Path(dest + ".part").write_bytes(b"half")
            raise OSError("unreachable")
        Path(dest).write_bytes(b"ok")

    monkeypatch.setattr(piper_dl, "_download", fake)
    dest = str(tmp_path / "v.onnx")
    piper_dl._download_any(piper_dl._urls({"url": "https://up/x", "mirrors": ["https://m/x"]}),
                           dest, 0, 100, False)
    assert tried == ["https://up/x", "https://m/x"]
    assert Path(dest).read_bytes() == b"ok"
    assert not Path(dest + ".part").exists()
    assert piper_dl._urls({"url": "https://up/x"}) == ["https://up/x"]


def test_has_internet_accepts_any_probe(monkeypatch):
    from hal import app_state

    tried = []

    def tcp(host, port):
        tried.append(host)
        return host == "8.8.8.8"

    monkeypatch.setattr(app_state, "_cloud_probe_target", lambda: ("cloud.test", 443))
    monkeypatch.setattr(app_state, "_tcp_probe", tcp)
    monkeypatch.setattr(app_state, "_tls_probe", lambda host, port: False)
    assert app_state._has_internet() is True
    assert tried == ["8.8.8.8"], "the second resolver is only tried when the first fails"

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
