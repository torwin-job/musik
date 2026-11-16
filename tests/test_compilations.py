"""Album artist and compilation tags: read from files, stored, refreshed once."""
from __future__ import annotations

import os
import shutil
import subprocess
from pathlib import Path

import numpy as np
import pytest
import soundfile as sf
from mutagen.flac import FLAC
from mutagen.mp4 import MP4

from musik.config import get_settings
from musik.db import ensure_db
from musik.db.schema import connect
from musik.db.store import TAG_SCHEMA_VERSION, set_tag_schema_version, tag_schema_version
from musik.scanner import scan_library
from musik.scanner.tags import read_tags


def _flac(path: Path, **tags: str) -> Path:
    sr = 22050
    tone = (0.2 * np.sin(2 * np.pi * 440 * np.arange(sr) / sr)).astype(np.float32)
    sf.write(path, tone, sr, format="FLAC")
    audio = FLAC(path)
    for key, value in tags.items():
        audio[key] = value
    audio.save()
    return path


def test_flac_album_artist_and_compilation(tmp_path: Path) -> None:
    path = _flac(
        tmp_path / "be-junior.flac",
        title="Be Junior", artist="Foxmelody", album="Силовая тренировка & Бег",
        albumartist="Разные артисты", compilation="1",
    )
    tags = read_tags(path)
    assert tags.artist == "Foxmelody"
    assert tags.album_artist == "Разные артисты"
    assert tags.compilation is True


@pytest.mark.skipif(shutil.which("ffmpeg") is None, reason="ffmpeg is required to write an M4A file")
def test_m4a_compilation_flag_comes_from_cpil(tmp_path: Path) -> None:
    path = tmp_path / "track.m4a"
    subprocess.run(
        ["ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=duration=1",
         "-c:a", "aac", str(path)],
        check=True,
    )
    audio = MP4(path)
    audio["\xa9nam"], audio["\xa9ART"], audio["\xa9alb"] = "Run", "Jogger", "Mix"
    audio["cpil"] = True
    audio.save()
    tags = read_tags(path)
    assert tags.compilation is True
    assert tags.album_artist is None


@pytest.mark.skipif(shutil.which("ffmpeg") is None, reason="ffmpeg is required to write an M4A file")
def test_m4a_embedded_cover_is_read(tmp_path: Path) -> None:
    from mutagen.mp4 import MP4Cover

    path = tmp_path / "track.m4a"
    subprocess.run(
        ["ffmpeg", "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=duration=1",
         "-c:a", "aac", str(path)],
        check=True,
    )
    jpeg = b"\xff\xd8\xff\xe0 not a real jpeg \xff\xd9"
    audio = MP4(path)
    audio["covr"] = [MP4Cover(jpeg, imageformat=MP4Cover.FORMAT_JPEG)]
    audio.save()
    assert read_tags(path).artwork_bytes == jpeg


def test_scan_refreshes_tags_of_unchanged_files_once(
    tmp_path: Path, monkeypatch, test_database
) -> None:
    lib = tmp_path / "lib"
    lib.mkdir()
    monkeypatch.setenv("MUSIK_LIBRARY", str(lib))
    monkeypatch.setenv("MUSIK_DATA_DIR", str(tmp_path / "data"))
    monkeypatch.setenv("MUSIK_ARTWORK_CACHE", str(tmp_path / "data" / "cache" / "artwork"))
    test_database(tmp_path / "data" / "db" / "t.db")
    get_settings.cache_clear()
    ensure_db()
    path = _flac(
        lib / "01.flac", title="Be Junior", artist="Foxmelody", album="Mix",
        albumartist="Разные артисты", compilation="1",
    )
    scan_library(lib, extract_audio=False, workers=1)
    assert tag_schema_version() == TAG_SCHEMA_VERSION

    # Simulate a library scanned before the album credit was read: the file is
    # unchanged, so only the tag refresh can fill the new columns.
    with connect() as conn:
        conn.execute("UPDATE tracks SET album_artist = NULL, is_compilation = 0")
    set_tag_schema_version(1)
    mtime = os.stat(path).st_mtime
    result = scan_library(lib, extract_audio=False, workers=1)
    assert result.skipped_unchanged == 1 and result.upserted == 0
    assert os.stat(path).st_mtime == mtime
    with connect() as conn:
        row = conn.execute("SELECT album_artist, is_compilation FROM tracks").fetchone()
    assert (row["album_artist"], row["is_compilation"]) == ("Разные артисты", 1)
    assert tag_schema_version() == TAG_SCHEMA_VERSION
    get_settings.cache_clear()
