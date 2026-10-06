from __future__ import annotations

import importlib.util
import os
import sys
from pathlib import Path

import pytest

from musik.config import get_settings
from musik.db.schema import init_db
from musik.db.store import upsert_track

_MODULE_PATH = Path(__file__).resolve().parents[1] / "tools" / "cleanup_duplicates.py"
_spec = importlib.util.spec_from_file_location("cleanup_duplicates", _MODULE_PATH)
assert _spec and _spec.loader
cleanup = importlib.util.module_from_spec(_spec)
sys.modules["cleanup_duplicates"] = cleanup
_spec.loader.exec_module(cleanup)


@pytest.fixture()
def env(tmp_path: Path, monkeypatch):
    library = tmp_path / "music"
    library.mkdir()
    db = tmp_path / "musik.db"
    monkeypatch.setenv("MUSIK_DB_PATH", str(db))
    monkeypatch.setenv("MUSIK_LIBRARY", str(library))
    get_settings.cache_clear()
    init_db()
    yield db, library
    get_settings.cache_clear()


def _track(path: Path, *, md5: str, title: str, size: int) -> int:
    return upsert_track(
        {
            "path": str(path),
            "file_md5": md5,
            "file_mtime": 0,
            "file_size": size,
            "title": title,
            "artist": "Nickelback",
            "album": "Curb",
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


def _pair(library: Path, tmp_path: Path) -> tuple[Path, Path]:
    keeper = library / "album" / "01 - Pusher.flac"
    loser = library / "other release" / "01 - Pusher.mp3"
    keeper.parent.mkdir(parents=True)
    loser.parent.mkdir(parents=True)
    keeper.write_bytes(b"flac-bytes")
    loser.write_bytes(b"mp3-bytes")
    _track(keeper, md5="deadbeef", title="Pusher", size=10)
    _track(loser, md5="deadbeef", title="Pusher", size=6)
    return keeper, loser


def test_dry_run_keeps_files(env, capsys):
    db, library = env
    keeper, loser = _pair(library, Path(db).parent)
    assert cleanup.main(["--db", str(db)]) == 0
    out = capsys.readouterr().out
    assert "dry run" in out
    assert "duplicate files: 1" in out
    assert loser.exists() and keeper.exists()


def test_apply_removes_loser(env, capsys):
    db, library = env
    keeper, loser = _pair(library, Path(db).parent)
    assert cleanup.main(["--db", str(db), "--apply"]) == 0
    out = capsys.readouterr().out
    assert "1 file(s) removed" in out
    assert keeper.exists()
    assert not loser.exists()

    import sqlite3

    conn = sqlite3.connect(db)
    row = conn.execute(
        "SELECT is_active, is_duplicate_of FROM tracks WHERE path = ?",
        (str(loser),),
    ).fetchone()
    conn.close()
    assert row == (0, None)
    # the keeper stays untouched and active
    conn = sqlite3.connect(db)
    active = conn.execute(
        "SELECT COUNT(*) FROM tracks WHERE is_active = 1"
    ).fetchone()[0]
    conn.close()
    assert active == 1


def test_missing_keeper_is_never_deleted(env, capsys):
    db, library = env
    keeper, loser = _pair(library, Path(db).parent)
    keeper.unlink()
    assert cleanup.main(["--db", str(db), "--apply"]) == 0
    out = capsys.readouterr().out
    assert "keeper missing" in out
    assert "duplicate files: 0" in out
    assert loser.exists()


def test_no_database_exits_with_error(tmp_path, capsys):
    missing = tmp_path / "nope.db"
    assert cleanup.main(["--db", str(missing)]) == 2
    assert "run `musik scan` first" in capsys.readouterr().err
