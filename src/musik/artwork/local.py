"""Album covers that sit next to the audio files.

Many rips carry the cover as a file (cover.jpg, Folder.jpg, f.jpg) or a scans
subfolder ("Scans", "обложки") instead of embedding it. Only names that mean
"cover" are taken from the album folder itself: a folder can also hold band
photos (Band.jpg) or disc labels (cd.jpg) that must not become the cover.
"""

from __future__ import annotations

import re
from pathlib import Path

IMAGE_EXTS = {".jpg", ".jpeg", ".png", ".webp"}
# Exact file stems, best first.
_PREFERRED = (
    "cover", "folder", "front", "frontcover", "cover front", "front cover",
    "albumart", "album", "обложка", "f", "00", "01",
)
_COVER_WORDS = ("cover", "front", "folder", "облож", "лицев")
_NOT_COVER_WORDS = ("back", "inlay", "inside", "booklet", "tray", "cd", "disc", "band", "зад")
# Subfolders that hold scans; any image there may be taken, cover-like first.
_SCAN_DIRS = {
    "cover", "covers", "scan", "scans", "artwork", "art", "images", "img",
    "обложка", "обложки", "скан", "сканы",
}
# A disc folder ("CD1", "Disc 2", "1. Main CD") takes the cover of its album folder.
_DISC_RE = re.compile(r"\b(cd|disc|disk|диск)\s*\d*\b|^\d+\.\s*(main|bonus)\b", re.IGNORECASE)


def _images(folder: Path) -> list[Path]:
    try:
        return sorted(
            (p for p in folder.iterdir() if p.is_file() and p.suffix.lower() in IMAGE_EXTS),
            key=lambda p: p.name.lower(),
        )
    except OSError:
        return []


def _rank(path: Path, *, in_scans: bool) -> int | None:
    """Lower is better; None = not a cover."""
    stem = path.stem.strip().lower()
    if stem in _PREFERRED:
        return _PREFERRED.index(stem)
    if any(w in stem for w in _COVER_WORDS) and not any(w in stem for w in _NOT_COVER_WORDS):
        return 20
    if in_scans and not any(w in stem for w in _NOT_COVER_WORDS):
        return 50  # first remaining scan by name
    return None


def _best_in(folder: Path, base: int) -> tuple[int, Path] | None:
    scored: list[tuple[int, Path]] = []
    for image in _images(folder):
        rank = _rank(image, in_scans=False)
        if rank is not None:
            scored.append((base + rank, image))
    try:
        subdirs = sorted((p for p in folder.iterdir() if p.is_dir()), key=lambda p: p.name.lower())
    except OSError:
        subdirs = []
    for sub in subdirs:
        if sub.name.strip().lower() not in _SCAN_DIRS:
            continue
        for image in _images(sub):
            rank = _rank(image, in_scans=True)
            if rank is not None:
                scored.append((base + 100 + rank, image))
    # min() keeps the first of equal scores, i.e. the first file by name.
    return min(scored, key=lambda s: s[0]) if scored else None


def find_folder_cover(track_path: str | Path) -> Path | None:
    """The cover image for a track's album folder, or None."""
    folder = Path(track_path).parent
    found = _best_in(folder, 0)
    if found is None and _DISC_RE.search(folder.name):
        found = _best_in(folder.parent, 1000)
    return found[1] if found else None
