"""Music search fails fast in China instead of spawning yt-dlp."""

import json

import pytest
from fastapi import HTTPException

from hal import config
from hal.drivers.voice import music_service
from hal.drivers.voice.music_service import MusicService
from hal.routes import music as music_routes


def _set_region(monkeypatch, tmp_path, region):
    path = tmp_path / "config.json"
    body = {} if region is None else {"region": region}
    path.write_text(json.dumps(body))
    monkeypatch.setattr(config, "OS_CONFIG_PATH", str(path))


@pytest.mark.parametrize("region,expected", [("CN", True), ("cn", True), (" Cn ", True), ("US", False), ("", False), (None, False)])
def test_in_china_reads_config(monkeypatch, tmp_path, region, expected):
    _set_region(monkeypatch, tmp_path, region)
    assert config.in_china() is expected
    assert music_service.music_source_available() is (not expected)


def test_resolve_skips_ytdlp_in_china(monkeypatch, tmp_path):
    _set_region(monkeypatch, tmp_path, "CN")

    def _boom(*a, **kw):
        raise AssertionError("yt-dlp must not be spawned in China")

    monkeypatch.setattr(music_service.subprocess, "run", _boom)
    assert MusicService()._resolve_audio_url("some song") == (None, None)


def test_resolve_still_searches_outside_china(monkeypatch, tmp_path):
    _set_region(monkeypatch, tmp_path, None)
    calls = []

    class _Result:
        returncode = 0
        stdout = json.dumps({"title": "T", "webpage_url": "https://example.test/w"})
        stderr = ""

    monkeypatch.setattr(music_service.subprocess, "run", lambda *a, **kw: calls.append(a) or _Result())
    assert MusicService()._resolve_audio_url("some song") == ("https://example.test/w", "T")
    assert len(calls) == 1


class _FakeMusic:
    available = True

    def __init__(self):
        self.played = []

    def play(self, query, on_started=None, person=""):
        self.played.append(query)
        return True


def _prepare_route(monkeypatch):
    fake = _FakeMusic()
    monkeypatch.setattr(music_routes.state, "_speaker_muted", False, raising=False)
    monkeypatch.setattr(music_routes.state, "music_cancel_active", lambda: False)
    monkeypatch.setattr(music_routes, "audio_quiet_now", lambda policy: False)
    monkeypatch.setattr(music_routes.state, "music_service", fake)
    return fake


def test_route_fails_fast_in_china(monkeypatch, tmp_path):
    _set_region(monkeypatch, tmp_path, "CN")
    fake = _prepare_route(monkeypatch)
    with pytest.raises(HTTPException) as exc:
        music_routes.audio_play(music_routes.MusicPlayRequest(query="some song"))
    assert exc.value.status_code == 503
    assert exc.value.detail == music_service.MUSIC_UNAVAILABLE_MESSAGE
    assert fake.played == []
