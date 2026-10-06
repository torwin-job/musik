from __future__ import annotations

import sqlite3
from pathlib import Path

import pytest

from musik.artwork import artists
from musik.config import get_settings
from musik.db.schema import init_db
from musik.db.store import upsert_track

JPEG = b"\xff\xd8\xff\xe0" + b"photo" * 10
PIC = "https://cdn-images.dzcdn.net/images/artist/abc/1000x1000-000000-80-0-0.jpg"


@pytest.fixture(autouse=True)
def _no_sleep(monkeypatch):
    monkeypatch.setattr(artists, "_sleep", lambda _s: None)


@pytest.fixture()
def env(tmp_path: Path, monkeypatch):
    library = tmp_path / "music"
    library.mkdir()
    db = tmp_path / "musik.db"
    monkeypatch.setenv("MUSIK_DB_PATH", str(db))
    monkeypatch.setenv("MUSIK_LIBRARY", str(library))
    monkeypatch.setenv("MUSIK_ARTWORK_CACHE", str(tmp_path / "artwork-cache"))
    get_settings.cache_clear()
    init_db()
    yield db, library
    get_settings.cache_clear()


def _track(library: Path, name: str, artist: str, segments: list[str]) -> int:
    path = library / name
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(b"audio")
    return upsert_track(
        {
            "path": str(path), "file_md5": name, "file_mtime": 0, "file_size": 5,
            "title": name, "artist": artist, "album": "A", "artist_segments": segments,
            "year": 2000, "is_remaster": 0, "track_number": 1, "duration": 100.0,
            "bitrate": 1000, "sample_rate": 44100, "channels": 2, "fingerprint": "",
            "lufs": None, "artwork_path": None,
        }
    )


def _deezer(items):
    return lambda url, **kwargs: {"data": items}


def test_exact_name_match_prefers_most_followed(monkeypatch):
    monkeypatch.setattr(artists, "_get_json", _deezer([
        {"name": "Би-2", "nb_fan": 1, "picture_xl": PIC.replace("abc", "fake")},
        {"name": "Shura Би-2", "nb_fan": 999999, "picture_xl": PIC},
        {"name": "би-2", "nb_fan": 262324, "picture_xl": PIC},
    ]))
    got: list[str] = []
    monkeypatch.setattr(artists, "_request", lambda url, **kwargs: (got.append(url), (JPEG, "image/jpeg"))[1])
    assert artists.fetch_artist_photo("Би-2") == JPEG
    assert got == [PIC]  # not the fan duplicate, not the partial "Shura Би-2"


def test_no_match_and_placeholder_are_rejected(monkeypatch):
    monkeypatch.setattr(artists, "_get_json", _deezer([
        {"name": "Сплин", "nb_fan": 5, "picture_xl": "https://cdn/images/artist//1000x1000-000000.jpg"},
        {"name": "Спринт", "nb_fan": 5, "picture_xl": PIC},
    ]))
    monkeypatch.setattr(artists, "_request", lambda *a, **k: pytest.fail("must not download"))
    assert artists.fetch_artist_photo("Сплин") is None


def test_quota_error_retries_then_stops(monkeypatch):
    calls: list[str] = []

    def quota(url, **kwargs):
        calls.append(url)
        return {"error": {"code": 4, "message": "Quota limit exceeded"}}

    monkeypatch.setattr(artists, "_get_json", quota)
    with pytest.raises(artists.RateLimited):
        artists.fetch_artist_photo("Сплин")
    assert len(calls) == artists._QUOTA_RETRIES + 1


def test_library_run_uses_segments_and_remembers_misses(env, monkeypatch):
    db, library = env
    _track(library, "a.flac", "Thomas / Сплин", ["Thomas", "Сплин"])
    _track(library, "b.flac", "Сплин", ["Сплин"])

    asked: list[str] = []

    def fake(name, **kwargs):
        asked.append(name)
        return JPEG if name == "Сплин" else None

    monkeypatch.setattr(artists, "fetch_artist_photo", fake)
    result = artists.fetch_library_artist_photos(delay_sec=0)
    assert sorted(asked) == ["Thomas", "Сплин"]  # each segment once
    assert (result.total, result.found, result.missing) == (2, 1, 1)

    conn = sqlite3.connect(db)
    rows = dict(conn.execute("SELECT name_key, status FROM artist_photos").fetchall())
    path = conn.execute("SELECT path FROM artist_photos WHERE name_key = 'сплин'").fetchone()[0]
    conn.close()
    assert rows == {"thomas": "missing", "сплин": "found"}
    assert Path(path).read_bytes() == JPEG

    # Second run: the found photo and the fresh miss are both skipped.
    asked.clear()
    result = artists.fetch_library_artist_photos(delay_sec=0)
    assert asked == [] and result.total == 0
    # --force looks everyone up again.
    artists.fetch_library_artist_photos(delay_sec=0, force=True)
    assert sorted(asked) == ["Thomas", "Сплин"]
