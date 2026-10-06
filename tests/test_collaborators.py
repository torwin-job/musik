from __future__ import annotations

from musik.scanner.collaborators import split_collaborators
from musik.scanner.tags import detect_remaster


def test_split_explicit_separators():
    assert split_collaborators("Thomas/БИ-2/Сплин") == ["Thomas", "БИ-2", "Сплин"]
    assert split_collaborators("Jay-Z / Linkin Park") == ["Jay-Z", "Linkin Park"]
    assert split_collaborators("Linkin Park; Steve Aoki") == ["Linkin Park", "Steve Aoki"]
    assert split_collaborators("Linkin Park feat. Pusha T") == ["Linkin Park", "Pusha T"]
    assert split_collaborators("Linkin Park ft. Pusha T") == ["Linkin Park", "Pusha T"]


def test_split_keeps_band_names_whole():
    # "&" and the Russian "и" are not separators — they live inside band names.
    assert split_collaborators("Simon & Garfunkel") == ["Simon & Garfunkel"]
    assert split_collaborators("Король и Шут") == ["Король и Шут"]
    assert split_collaborators("Earth, Wind & Fire") == ["Earth, Wind & Fire"]
    assert split_collaborators("Scorpions") == ["Scorpions"]
    assert split_collaborators("") == []
    assert split_collaborators(None) == []


def test_split_dedupes_segments():
    assert split_collaborators("A / A / B") == ["A", "B"]


def test_detect_remaster():
    assert detect_remaster("Lovedrive (2001 Remastered)")
    assert detect_remaster("Альбом (ремастер 2024)")
    assert detect_remaster("Album - Remastered")
    assert not detect_remaster("Nevermind (1991)")
    assert not detect_remaster("Meteora")
    assert not detect_remaster(None)
