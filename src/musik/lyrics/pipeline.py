"""Batch lyrics fetch for the library."""

from __future__ import annotations

import logging
from dataclasses import dataclass, field
from typing import Any, Callable

from rich.console import Console
from rich.progress import Progress

from musik.lyrics.lrclib import fetch_lyrics, throttle
from musik.lyrics.store import list_tracks_needing_lyrics, upsert_lyrics

logger = logging.getLogger(__name__)
console = Console()


@dataclass
class LyricsResult:
    total: int = 0
    found: int = 0
    missing: int = 0
    failed: int = 0
    errors: list[str] = field(default_factory=list)


def fetch_library_lyrics(
    *,
    limit: int | None = None,
    force: bool = False,
    delay_sec: float = 0.35,
    on_progress: Callable[[dict[str, Any]], None] | None = None,
) -> LyricsResult:
    tracks = list_tracks_needing_lyrics(limit=limit, force=force)
    result = LyricsResult(total=len(tracks))
    if not tracks:
        console.print("[yellow]Нечего качать — тексты уже есть или библиотека пуста.[/yellow]")
        return result

    with Progress(console=console) as progress:
        task = progress.add_task("Lyrics", total=len(tracks))
        for i, t in enumerate(tracks, start=1):
            tid = int(t["id"])
            try:
                hit = fetch_lyrics(
                    artist=t.get("artist") or "",
                    title=t.get("title") or "",
                    album=t.get("album") or "",
                    duration_sec=t.get("duration"),
                )
                if hit is None:
                    upsert_lyrics(tid, status="missing", source="lrclib", error="not found")
                    result.missing += 1
                else:
                    upsert_lyrics(
                        tid,
                        plain_lyrics=hit.get("plain_lyrics") or "",
                        synced_lyrics=hit.get("synced_lyrics") or "",
                        source=hit.get("source") or "lrclib",
                        source_id=hit.get("source_id") or "",
                        instrumental=bool(hit.get("instrumental")),
                        status="ready",
                    )
                    result.found += 1
            except Exception as e:
                logger.exception("lyrics failed track_id=%s", tid)
                upsert_lyrics(tid, status="failed", error=str(e)[:500])
                result.failed += 1
                result.errors.append(f"{tid}: {e}")

            if on_progress is not None:
                on_progress(
                    {
                        "phase": "lyrics",
                        "done": i,
                        "total": len(tracks),
                        "pct": round(100.0 * i / len(tracks), 1),
                        "message": (
                            f"lyrics {i}/{len(tracks)} · "
                            f"found={result.found} missing={result.missing} fail={result.failed}"
                        ),
                    }
                )
            progress.update(
                task,
                advance=1,
                description=f"Lyrics · found={result.found} miss={result.missing}",
            )
            throttle(delay_sec)

    return result
