from __future__ import annotations

import re
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from mutagen import File as MutagenFile

from musik.scanner.collaborators import split_collaborators


@dataclass
class TrackTags:
    title: str | None = None
    artist: str | None = None
    album: str | None = None
    year: int | None = None
    track_number: int | None = None
    genres: list[str] = field(default_factory=list)
    duration: float | None = None
    bitrate: int | None = None
    sample_rate: int | None = None
    channels: int | None = None
    artwork_bytes: bytes | None = None
    # Album tag carried a remaster marker ("... (2001 Remastered)").
    is_remaster: bool = False
    # Collaborator segments parsed once here and stored in the database.
    artist_segments: list[str] = field(default_factory=list)


# Remaster markers seen in real album tags:
#   "Lovedrive (2001 Remastered)", "Slippery When Wet (Digitally Remastered)",
#   "Album - Remastered", "Album [Remastered 2011]", "Альбом (ремастер 2024)".
# Years in parentheses that are not remaster markers ("... (1998)") survive.
_REMASTER_RE = re.compile(
    r"\s*[(\[][^\)\]]*(?:remaster|ремаст)[^\)\]]*[)\]]"
    r"|\s*[-–—]\s*(?:remaster|ремаст)\w*"
    r"|\s+(?:remastered|ремастер)\s*$",
    re.IGNORECASE,
)


def detect_remaster(album: str | None) -> bool:
    """True when an album tag carries a remaster marker.

    The album name itself is stored unchanged. Album grouping ignores the year,
    so stripping "… (2001 Remastered)" would collapse an original and its
    remaster into one album with two copies of every track; keeping the full
    name (plus the ``is_remaster`` flag for dedup) keeps them apart.
    """
    return bool(album and _REMASTER_RE.search(album))


def _first(val: Any) -> str | None:
    if val is None:
        return None
    if isinstance(val, list):
        return str(val[0]) if val else None
    return str(val)


def _parse_year(raw: str | None) -> int | None:
    if not raw:
        return None
    m = re.search(r"(19|20)\d{2}", raw)
    return int(m.group(0)) if m else None


def _parse_track_no(raw: str | None) -> int | None:
    if not raw:
        return None
    m = re.match(r"(\d+)", str(raw))
    return int(m.group(1)) if m else None


def _fallback_from_filename(path: Path) -> tuple[str | None, str | None]:
    stem = path.stem
    # "01 - Title" or "Artist - Title"
    m = re.match(r"^\d+[\s.\-_]+(.+)$", stem)
    if m:
        return None, m.group(1).strip()
    if " - " in stem:
        a, t = stem.split(" - ", 1)
        return a.strip(), t.strip()
    return None, stem


def read_tags(path: Path) -> TrackTags:
    tags = TrackTags()
    try:
        audio = MutagenFile(path, easy=True)
    except Exception:
        audio = None

    if audio is not None:
        info = getattr(audio, "info", None)
        if info is not None:
            tags.duration = float(getattr(info, "length", 0) or 0) or None
            br = getattr(info, "bitrate", None)
            tags.bitrate = int(br) if br else None
            tags.sample_rate = int(getattr(info, "sample_rate", 0) or 0) or None
            tags.channels = int(getattr(info, "channels", 0) or 0) or None

        if audio.tags:
            tags.title = _first(audio.tags.get("title"))
            tags.artist = _first(audio.tags.get("artist"))
            tags.album = _first(audio.tags.get("album"))
            tags.is_remaster = detect_remaster(tags.album)
            tags.year = _parse_year(_first(audio.tags.get("date")) or _first(audio.tags.get("year")))
            tags.track_number = _parse_track_no(_first(audio.tags.get("tracknumber")))
            genre_raw = audio.tags.get("genre")
            if genre_raw:
                if isinstance(genre_raw, list):
                    tags.genres = [str(g).strip() for g in genre_raw if str(g).strip()]
                else:
                    tags.genres = [g.strip() for g in str(genre_raw).split(";") if g.strip()]

    # Artwork from non-easy mutagen
    try:
        raw = MutagenFile(path)
        if raw is not None:
            pictures = []
            if hasattr(raw, "pictures"):
                pictures = list(raw.pictures or [])
            elif raw.tags:
                for key in list(raw.tags.keys()):
                    if str(key).startswith("APIC") or str(key).startswith("covr"):
                        val = raw.tags[key]
                        if isinstance(val, list):
                            pictures.extend(val)
                        else:
                            pictures.append(val)
            for pic in pictures:
                data = getattr(pic, "data", None)
                if data:
                    tags.artwork_bytes = bytes(data)
                    break
    except Exception:
        pass

    if not tags.title or not tags.artist:
        fa, ft = _fallback_from_filename(path)
        tags.artist = tags.artist or fa
        tags.title = tags.title or ft

    if not tags.artist and path.parent.name:
        # Album folder often under Artist/
        parent = path.parent.name
        if not re.match(r"^\d{4}", parent):
            tags.artist = tags.artist or parent

    tags.artist_segments = split_collaborators(tags.artist)
    return tags
