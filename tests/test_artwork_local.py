from __future__ import annotations

from pathlib import Path

from musik.artwork.local import find_folder_cover


def _touch(path: Path) -> Path:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(b"x")
    return path


def test_prefers_cover_named_file_over_band_photo(tmp_path):
    album = tmp_path / "Artist" / "2001 - Album"
    track = _touch(album / "01.flac")
    _touch(album / "Band.jpg")
    _touch(album / "cd.jpg")
    cover = _touch(album / "Cover.JPG")
    assert find_folder_cover(track) == cover


def test_short_f_and_front_names(tmp_path):
    a = tmp_path / "a"
    _touch(a / "f.jpg")
    assert find_folder_cover(_touch(a / "1.mp3")) == a / "f.jpg"
    b = tmp_path / "b"
    _touch(b / "Back.jpg")
    _touch(b / "Front.jpg")
    assert find_folder_cover(_touch(b / "1.mp3")) == b / "Front.jpg"


def test_scans_subfolder_used_when_no_cover_file(tmp_path):
    album = tmp_path / "1996"
    track = _touch(album / "01.flac")
    _touch(album / "обложки" / "2.JPG")
    first = _touch(album / "обложки" / "1.JPG")
    _touch(album / "обложки" / "back.jpg")
    assert find_folder_cover(track) == first
    covers = tmp_path / "x"
    _touch(covers / "Covers" / "scan911.jpg")
    front = _touch(covers / "Covers" / "front.jpg")
    assert find_folder_cover(_touch(covers / "1.flac")) == front


def test_disc_folder_takes_album_cover_but_artist_folder_does_not(tmp_path):
    album = tmp_path / "2017 - История Звука (3 CD)"
    cover = _touch(album / "folder.jpg")
    assert find_folder_cover(_touch(album / "CD 3" / "01.flac")) == cover
    assert find_folder_cover(_touch(album / "1. Main CD" / "01.flac")) == cover
    # A plain album folder never borrows a random picture from the artist folder.
    artist = tmp_path / "Nickelback"
    _touch(artist / "Ryan_Peake_Nickelback.jpg")
    assert find_folder_cover(_touch(artist / "1996" / "01.flac")) is None


def test_artist_folder_without_cover_is_none(tmp_path):
    folder = tmp_path / "Rammstein"
    _touch(folder / "Rammstein — Engel.flac")
    assert find_folder_cover(folder / "Rammstein — Engel.flac") is None
