"""Batch cover-art fetch for the library."""

from __future__ import annotations

import logging
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from pathlib import Path
from typing import Any, Callable

from rich.console import Console
from rich.progress import Progress

from musik.artwork.local import find_folder_cover
from musik.artwork.online import RateLimited, fetch_cover, throttle
from musik.config import get_settings
from musik.db.store import (
    list_tracks_needing_artwork,
    recent_artwork_misses,
    record_artwork_lookup,
    save_artwork_path,
)

# An album without a match is not looked up again for this long, so the
# hourly run only asks iTunes about new albums.
MISS_RETRY_DAYS = 30


def album_key(artist: str, album: str) -> str:
    return f"{' '.join(artist.split()).lower()}|{' '.join(album.split()).lower()}"


def _miss_cutoff() -> str:
    return (datetime.now(timezone.utc) - timedelta(days=MISS_RETRY_DAYS)).isoformat()


logger = logging.getLogger(__name__)
console = Console()


@dataclass
class ArtworkResult:
    total: int = 0
    found: int = 0
    missing: int = 0
    failed: int = 0
    # Covers taken from image files in the album folder (no request needed).
    local: int = 0
    # Set when iTunes kept throttling: the run stopped and the remaining
    # tracks were not looked up (counted in failed, not in missing).
    rate_limited: bool = False
    errors: list[str] = field(default_factory=list)


def save_cover(file_md5: str, data: bytes, *, overwrite: bool = True) -> str:
    out = get_settings().artwork_cache / f"{file_md5}.jpg"
    if overwrite or not out.exists():
        out.write_bytes(data)
    return str(out)


def _use_folder_covers(tracks: list[dict[str, Any]], used: list[int]) -> list[dict[str, Any]]:
    """Save folder covers for tracks that have one; return the rest."""
    rest: list[dict[str, Any]] = []
    by_folder: dict[str, Path | None] = {}
    for t in tracks:
        path = str(t.get("path") or "")
        folder = str(Path(path).parent) if path else ""
        if folder not in by_folder:
            by_folder[folder] = find_folder_cover(path) if path else None
        cover = by_folder[folder]
        if cover is None:
            rest.append(t)
            continue
        save_artwork_path(int(t["id"]), str(cover))
        used.append(int(t["id"]))
    return rest


def fetch_library_artwork(
    *,
    limit: int | None = None,
    force: bool = False,
    delay_sec: float = 3.0,
    on_progress: Callable[[dict[str, Any]], None] | None = None,
) -> ArtworkResult:
    # Without force the limit applies after the "cover is missing" filter;
    # limiting the SQL first would pick tracks that already have covers.
    tracks = list_tracks_needing_artwork(limit=limit if force else None, force=force)
    local_ids: list[int] = []
    if not force:
        # A stored artwork_path may be empty, point at a container path, or
        # reference a deleted file. Re-fetch only when the cover is really gone.
        tracks = [
            t
            for t in tracks
            if not t.get("artwork_path") or not Path(str(t["artwork_path"])).is_file()
        ]
        # A cover file in the album folder beats any online lookup.
        tracks = _use_folder_covers(tracks, local_ids)
        recent = recent_artwork_misses(_miss_cutoff())
        tracks = [
            t
            for t in tracks
            if album_key(str(t.get("artist") or ""), str(t.get("album") or "")) not in recent
        ]
        if limit is not None:
            tracks = tracks[:limit]

    result = ArtworkResult(total=len(tracks), local=len(local_ids))
    if not tracks:
        console.print("[yellow]Нечего качать — обложки уже есть или библиотека пуста.[/yellow]")
        return result

    # One iTunes request per album, not per track. Key keeps the artist case
    # so the printed label stays readable; insertion order is preserved.
    groups: dict[tuple[str, str], list[dict[str, Any]]] = {}
    for t in tracks:
        key = (str(t.get("artist") or ""), str(t.get("album") or ""))
        groups.setdefault(key, []).append(t)

    done = 0

    def _progress(description: str) -> None:
        if on_progress is not None:
            pct = round(100.0 * done / len(tracks), 1) if tracks else 100.0
            on_progress(
                {
                    "phase": "artwork",
                    "done": done,
                    "total": len(tracks),
                    "pct": pct,
                    "message": description,
                }
            )

    with Progress(console=console) as progress:
        task = progress.add_task("Artwork", total=len(tracks))
        for (artist, album), group in groups.items():
            try:
                hit = fetch_cover(artist=artist, album=album)
            except RateLimited as e:
                logger.warning("artwork stopped: %s", e)
                result.rate_limited = True
                result.failed += len(tracks) - done
                result.errors.append(f"{artist} — {album}: {e}")
                break
            except Exception as e:  # noqa: BLE001
                logger.exception("artwork failed album=%s — %s", artist, album)
                result.failed += len(group)
                result.errors.append(f"{artist} — {album}: {e}")
                hit = None
            else:
                record_artwork_lookup(album_key(artist, album), found=hit is not None)
            if hit is None:
                result.missing += len(group)
            else:
                for t in group:
                    try:
                        path = save_cover(str(t["file_md5"]), hit, overwrite=force)
                        save_artwork_path(int(t["id"]), path)
                    except Exception as e:  # noqa: BLE001
                        logger.exception("artwork save failed track_id=%s", t.get("id"))
                        result.failed += 1
                        result.errors.append(f"{t.get('id')}: {e}")
                result.found += len(group)
            done += len(group)
            progress.update(
                task,
                advance=len(group),
                description=f"Artwork · found={result.found} miss={result.missing}",
            )
            _progress(
                f"artwork {done}/{len(tracks)} · album {artist} — {album} · "
                f"found={result.found} missing={result.missing} fail={result.failed}"
            )
            throttle(delay_sec)

    return result
