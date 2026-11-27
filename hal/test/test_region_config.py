"""China region: Gemini Live is replaced by the normal pipeline and first-response waits loosen."""

import importlib.util
import json
import pathlib

import pytest

_CONFIG_PY = pathlib.Path(__file__).resolve().parent.parent / "config.py"
_ENV_KEYS = (
    "HAL_REALTIME_ENABLED", "HAL_REALTIME_PROVIDER",
    "HAL_REALTIME_RECV_QUEUE_TIMEOUT_S", "HAL_FAST_VOICE_SILENT_TIMEOUT_S",
)


def _load(monkeypatch, tmp_path, cfg, **env):
    """A private copy of hal.config read against a temp config.json (the shared module stays untouched)."""
    path = tmp_path / "config.json"
    path.write_text(json.dumps(cfg))
    for key in _ENV_KEYS:
        monkeypatch.delenv(key, raising=False)
    for key, value in env.items():
        monkeypatch.setenv(key, value)
    monkeypatch.setenv("OS_CONFIG_PATH", str(path))
    spec = importlib.util.spec_from_file_location("hal_config_region_copy", _CONFIG_PY)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def test_realtime_stays_on_outside_china(monkeypatch, tmp_path):
    for cfg in ({}, {"region": "US"}, {"region": ""}):
        cfg_mod = _load(monkeypatch, tmp_path, cfg)
        assert cfg_mod.REALTIME_ENABLED is True
        assert cfg_mod.REALTIME_RECV_QUEUE_TIMEOUT_S == 8.0
        assert cfg_mod.REALTIME_FAST_VOICE_SILENT_TIMEOUT_S == 5.0


def test_gemini_live_off_in_china(monkeypatch, tmp_path):
    cfg_mod = _load(monkeypatch, tmp_path, {"region": "CN"})
    assert cfg_mod.REALTIME_PROVIDER == "gemini"
    assert cfg_mod.REALTIME_ENABLED is False
    # An owner's explicit "enabled" does not bring Gemini Live back.
    assert _load(monkeypatch, tmp_path, {"region": "cn", "realtime": {"enabled": True}}).REALTIME_ENABLED is False


def test_other_realtime_provider_untouched_in_china(monkeypatch, tmp_path):
    cfg_mod = _load(monkeypatch, tmp_path, {"region": "CN", "realtime": {"provider": "openai"}})
    assert cfg_mod.REALTIME_ENABLED is True


def test_explicit_env_override_wins_in_china(monkeypatch, tmp_path):
    assert _load(monkeypatch, tmp_path, {"region": "CN"}, HAL_REALTIME_ENABLED="true").REALTIME_ENABLED is True


def test_first_response_waits_loosen_in_china(monkeypatch, tmp_path):
    cfg_mod = _load(monkeypatch, tmp_path, {"region": "CN"})
    assert cfg_mod.REALTIME_RECV_QUEUE_TIMEOUT_S == pytest.approx(8.0 * 5 / 3)
    # 5 s * 5/3 is above the 8 s ceiling for this setting.
    assert cfg_mod.REALTIME_FAST_VOICE_SILENT_TIMEOUT_S == cfg_mod.FAST_VOICE_SILENT_TIMEOUT_MAX_S
    # An explicit setting is used as given.
    assert _load(
        monkeypatch, tmp_path, {"region": "CN"}, HAL_REALTIME_RECV_QUEUE_TIMEOUT_S="4"
    ).REALTIME_RECV_QUEUE_TIMEOUT_S == 4.0
    # Cues stay immediate: the filler delay does not move.
    assert cfg_mod.REALTIME_FILLER_DELAY_S == 1.5
