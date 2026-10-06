from __future__ import annotations

from pathlib import Path

import pytest

from musik.config import get_settings
from musik.db.schema import init_db
from musik.db.store import upsert_track
from musik.artwork import online, pipeline


JPEG = b"\xff\xd8\xff\xe0" + b"jpeg-bytes" * 10


@pytest.fixture()
def env(tmp_path: Path, monkeypatch):
    library = tmp_path / "music"
    library.mkdir()
    db = tmp_path / "musik.db"
    cache = tmp_path / "artwork-cache"
    monkeypatch.setenv("MUSIK_DB_PATH", str(db))
    monkeypatch.setenv("MUSIK_LIBRARY", str(library))
    monkeypatch.setenv("MUSIK_ARTWORK_CACHE", str(cache))
    get_settings.cache_clear()
    init_db()
    yield db, library, cache
    get_settings.cache_clear()


def _track(library: Path, name: str, *, artist: str, album: str, md5: str) -> int:
    path = library / name
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(b"audio")
    return upsert_track(
        {
            "path": str(path),
            "file_md5": md5,
            "file_mtime": 0,
            "file_size": 5,
            "title": "Pusher",
            "artist": artist,
            "album": album,
            "year": 1996,
            "is_remaster": 0,
            "track_number": 1,
            "duration": 100.0,
            "bitrate": 1000,
            "sample_rate": 44100,
            "channels": 2,
            "fingerprint": "",
            "lufs": None,
            "artwork_path": None,
        }
    )


def test_large_artwork_url():
    thumb = "https://is1-ssl.mzstatic.com/image/thumb/aaa/100x100bb.jpg"
    assert online._large_artwork_url(thumb) == (
        "https://is1-ssl.mzstatic.com/image/thumb/aaa/1900x1900bb.jpg"
    )


def test_fetch_cover_picks_matching_artist(monkeypatch):
    seen: dict[str, str] = {}

    def fake_get_json(url, **kwargs):
        seen["search"] = url
        return {
            "results": [
                {"artistName": "Nickelback", "collectionName": "Curb",
                 "artworkUrl100": "https://img/100x100bb.jpg"},
                {"artistName": "Nickelback Extended", "collectionName": "Curb",
                 "artworkUrl100": "https://img/other.jpg"},
            ]
        }

    def fake_request(url, **kwargs):
        seen["img"] = url
        return JPEG, "image/jpeg"

    monkeypatch.setattr(online, "_get_json", fake_get_json)
    monkeypatch.setattr(online, "_request", fake_request)

    data = online.fetch_cover(artist="Nickelback", album="Curb")
    assert data == JPEG
    assert "term=Nickelback+Curb" in seen["search"]
    assert "entity=album" in seen["search"]
    # the 100px thumbnail was upscaled before download
    assert seen["img"].endswith("1900x1900bb.jpg")


def test_fetch_cover_refuses_foreign_artist(monkeypatch):
    monkeypatch.setattr(
        online,
        "_get_json",
        lambda url, **kwargs: {
            "results": [{"artistName": "Some Other Band", "collectionName": "Curb",
                         "artworkUrl100": "https://img/100x100bb.jpg"}]
        },
    )
    downloads: list[str] = []
    monkeypatch.setattr(
        online, "_request", lambda url, **kwargs: (downloads.append(url), (JPEG, "image/jpeg"))[1]
    )
    assert online.fetch_cover(artist="Nickelback", album="Curb") is None
    assert downloads == []  # nothing was downloaded for a foreign artist


def test_fetch_cover_requires_artist_and_album(monkeypatch):
    monkeypatch.setattr(
        online, "_get_json", lambda *_a, **_k: (_ for _ in ()).throw(AssertionError("must not query"))
    )
    assert online.fetch_cover(artist="", album="Curb") is None
    assert online.fetch_cover(artist="Nickelback", album="") is None


def test_fetch_cover_rejects_non_image(monkeypatch):
    monkeypatch.setattr(
        online,
        "_get_json",
        lambda url, **kwargs: {
            "results": [{"artistName": "Nickelback", "collectionName": "Curb",
                         "artworkUrl100": "https://img/100x100bb.jpg"}]
        },
    )
    monkeypatch.setattr(online, "_request", lambda url, **kwargs: (b"not an image", "text/html"))
    assert online.fetch_cover(artist="Nickelback", album="Curb") is None


def test_pipeline_saves_cover_and_updates_track(env, monkeypatch, capsys):
    db, library, cache = env
    tid = _track(library, "album/01.flac", artist="Nickelback", album="Curb", md5="abc123")
    monkeypatch.setattr(pipeline, "fetch_cover", lambda **kwargs: JPEG)

    result = pipeline.fetch_library_artwork(delay_sec=0)

    assert (result.total, result.found, result.missing) == (1, 1, 0)
    saved = Path(cache) / "abc123.jpg"
    assert saved.read_bytes() == JPEG

    import sqlite3

    conn = sqlite3.connect(db)
    row = conn.execute("SELECT artwork_path FROM tracks WHERE id = ?", (tid,)).fetchone()
    conn.close()
    assert row[0] == str(saved)


def test_pipeline_counts_missing_without_touching_track(env, monkeypatch):
    db, library, _cache = env
    tid = _track(library, "album/01.flac", artist="Nobody", album="Nowhere", md5="zzz")
    monkeypatch.setattr(pipeline, "fetch_cover", lambda **kwargs: None)

    result = pipeline.fetch_library_artwork(delay_sec=0)
    assert (result.total, result.found, result.missing) == (1, 0, 1)

    import sqlite3

    conn = sqlite3.connect(db)
    row = conn.execute("SELECT artwork_path FROM tracks WHERE id = ?", (tid,)).fetchone()
    conn.close()
    assert row[0] is None


def test_pipeline_skips_track_with_existing_cover_file(env, monkeypatch):
    db, library, cache = env
    tid = _track(library, "album/01.flac", artist="Nickelback", album="Curb", md5="have")
    (Path(cache)).mkdir(parents=True, exist_ok=True)
    existing = Path(cache) / "have.jpg"
    existing.write_bytes(JPEG)

    import sqlite3

    conn = sqlite3.connect(db)
    conn.execute("UPDATE tracks SET artwork_path = ? WHERE id = ?", (str(existing), tid))
    conn.commit()
    conn.close()

    calls: list[bool] = []
    monkeypatch.setattr(pipeline, "fetch_cover", lambda **kwargs: calls.append(True) or JPEG)

    result = pipeline.fetch_library_artwork(delay_sec=0)
    assert result.total == 0
    assert calls == []


def test_fetch_cover_accepts_edition_suffix(monkeypatch):
    monkeypatch.setattr(
        online,
        "_get_json",
        lambda url, **kwargs: {
            "results": [{"artistName": "Nickelback", "collectionName": "Curb (Remastered)",
                         "artworkUrl100": "https://img/100x100bb.jpg"}]
        },
    )
    monkeypatch.setattr(online, "_request", lambda url, **kwargs: (JPEG, "image/jpeg"))
    assert online.fetch_cover(artist="Nickelback", album="Curb") == JPEG


def test_fetch_cover_rejects_partial_artist(monkeypatch):
    # "Би-2" must not match "Би-2 & Сплин" — that is another release.
    monkeypatch.setattr(
        online,
        "_get_json",
        lambda url, **kwargs: {
            "results": [{"artistName": "Би-2 & Сплин", "collectionName": "Curb",
                         "artworkUrl100": "https://img/100x100bb.jpg"}]
        },
    )
    monkeypatch.setattr(online, "_request", lambda url, **kwargs: (JPEG, "image/jpeg"))
    assert online.fetch_cover(artist="Би-2", album="Curb") is None


def test_pipeline_refetches_when_artwork_file_missing(env, monkeypatch):
    # A non-empty artwork_path pointing at a deleted file must not be skipped.
    db, library, cache = env
    tid = _track(library, "album/01.flac", artist="Nickelback", album="Curb", md5="gone")

    import sqlite3

    conn = sqlite3.connect(db)
    conn.execute(
        "UPDATE tracks SET artwork_path = ? WHERE id = ?",
        (str(Path(cache) / "missing.jpg"), tid),
    )
    conn.commit()
    conn.close()

    monkeypatch.setattr(pipeline, "fetch_cover", lambda **kwargs: JPEG)
    result = pipeline.fetch_library_artwork(delay_sec=0)
    assert (result.total, result.found, result.missing) == (1, 1, 0)


def test_pipeline_queries_once_per_album(env, monkeypatch):
    db, library, cache = env
    _track(library, "album/01.flac", artist="Nickelback", album="Curb", md5="m1")
    _track(library, "album/02.flac", artist="Nickelback", album="Curb", md5="m2")

    calls: list[tuple[str, str]] = []

    def fake_fetch(**kwargs):
        calls.append((kwargs.get("artist"), kwargs.get("album")))
        return JPEG

    monkeypatch.setattr(pipeline, "fetch_cover", fake_fetch)
    result = pipeline.fetch_library_artwork(delay_sec=0)
    assert len(calls) == 1  # one request for the album, not one per track
    assert result.found == 2
    assert (Path(cache) / "m1.jpg").read_bytes() == JPEG
    assert (Path(cache) / "m2.jpg").read_bytes() == JPEG

