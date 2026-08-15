from __future__ import annotations

import tempfile
from pathlib import Path

import numpy as np
import soundfile as sf

from musik.db import ensure_db, counts
from musik.scanner import scan_library
from musik.scanner.tags import read_tags
from musik.scanner.hashing import file_md5


def _make_wav(path: Path, seconds: float = 1.5, freq: float = 440.0) -> None:
    sr = 22050
    t = np.linspace(0, seconds, int(sr * seconds), endpoint=False)
    y = (0.2 * np.sin(2 * np.pi * freq * t)).astype(np.float32)
    sf.write(path, y, sr)


def test_md5_and_tags(tmp_path: Path):
    wav = tmp_path / "01 - Test Song.wav"
    _make_wav(wav)
    assert len(file_md5(wav)) == 32
    tags = read_tags(wav)
    assert tags.title == "Test Song"


def test_scan_library(tmp_path: Path, monkeypatch):
    lib = tmp_path / "lib"
    lib.mkdir()
    _make_wav(lib / "a.wav", freq=440)
    _make_wav(lib / "b.wav", freq=880)

    # Point settings at temp dirs
    monkeypatch.setenv("MUSIK_LIBRARY", str(lib))
    monkeypatch.setenv("MUSIK_DATA_DIR", str(tmp_path / "data"))
    monkeypatch.setenv("MUSIK_DB_PATH", str(tmp_path / "data" / "db" / "t.db"))
    monkeypatch.setenv("MUSIK_EMBEDDINGS_CACHE", str(tmp_path / "data" / "cache" / "embeddings"))
    monkeypatch.setenv("MUSIK_ARTWORK_CACHE", str(tmp_path / "data" / "cache" / "artwork"))

    from musik.config import get_settings

    get_settings.cache_clear()
    ensure_db()
    result = scan_library(lib, extract_audio=True, workers=2)
    assert result.upserted == 2
    assert result.failed == 0
    st = counts()
    assert st["tracks_total"] == 2
    get_settings.cache_clear()
