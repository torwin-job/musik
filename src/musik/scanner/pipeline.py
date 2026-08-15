from __future__ import annotations

import logging
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from pathlib import Path

from typing import Any, Callable

from rich.progress import Progress, SpinnerColumn, BarColumn, TextColumn, TimeElapsedColumn

from musik.config import get_settings
from musik.db import (
    ensure_db,
    mark_duplicates,
    mark_missing_inactive,
    set_genres,
    update_audio_scalars,
    update_fingerprint_and_lufs,
    upsert_track,
)
from musik.scanner.audio_params import (
    compute_bpm,
    compute_fingerprint,
    compute_key_mode,
    compute_lufs,
)
from musik.scanner.hashing import file_md5
from musik.scanner.tags import read_tags
from musik.scanner.walk import iter_audio_files

logger = logging.getLogger(__name__)


@dataclass
class ScanResult:
    scanned: int = 0
    upserted: int = 0
    skipped_unchanged: int = 0
    failed: int = 0
    inactivated: int = 0
    duplicates_marked: int = 0


def _save_artwork(track_md5: str, data: bytes | None) -> str | None:
    if not data:
        return None
    settings = get_settings()
    out = settings.artwork_cache / f"{track_md5}.jpg"
    if not out.exists():
        out.write_bytes(data)
    return str(out)


def _process_one(path: Path, *, extract_audio: bool) -> tuple[str, dict | None, str | None]:
    """Returns (status, payload_or_none, error). status: ok|skip|fail"""
    settings = get_settings()
    try:
        stat = path.stat()
        md5 = file_md5(path)
        # Skip if unchanged md5 already in DB — handled by caller via mtime/md5 check optionally
        tags = read_tags(path)
        artwork = _save_artwork(md5, tags.artwork_bytes)

        fingerprint = None
        lufs = None
        bpm = None
        key_name = None
        mode = None
        if extract_audio:
            fingerprint = compute_fingerprint(path)
            lufs = compute_lufs(path, max_seconds=settings.analysis_seconds)
            bpm = compute_bpm(path, max_seconds=settings.analysis_seconds)
            key_name, mode = compute_key_mode(path, max_seconds=min(45.0, settings.analysis_seconds))

        payload = {
            "path": str(path.resolve()),
            "file_md5": md5,
            "file_mtime": float(stat.st_mtime),
            "file_size": int(stat.st_size),
            "title": tags.title,
            "artist": tags.artist,
            "album": tags.album,
            "year": tags.year,
            "track_number": tags.track_number,
            "duration": tags.duration,
            "bitrate": tags.bitrate,
            "sample_rate": tags.sample_rate,
            "channels": tags.channels,
            "fingerprint": fingerprint,
            "lufs": lufs,
            "artwork_path": artwork,
            "genres": tags.genres,
            "bpm": bpm,
            "key_name": key_name,
            "mode": mode,
        }
        return "ok", payload, None
    except Exception as exc:
        logger.exception("Failed %s", path)
        return "fail", None, str(exc)


def scan_library(
    library: Path | None = None,
    *,
    extract_audio: bool = True,
    workers: int | None = None,
    limit: int | None = None,
    on_progress: Callable[[dict[str, Any]], None] | None = None,
) -> ScanResult:
    settings = get_settings()
    ensure_db()
    files = iter_audio_files(library, settings)
    if limit is not None:
        files = files[:limit]
    workers = workers or settings.workers
    result = ScanResult(scanned=len(files))
    seen: set[str] = set()
    total = len(files)
    done = 0

    def _emit() -> None:
        if on_progress is None:
            return
        pct = (100.0 * done / total) if total else 100.0
        on_progress(
            {
                "phase": "scan",
                "done": done,
                "total": total,
                "pct": round(pct, 2),
                "upserted": result.upserted,
                "failed": result.failed,
                "message": f"scan {done}/{total} ({pct:.1f}%)",
            }
        )

    _emit()
    with Progress(
        SpinnerColumn(),
        TextColumn("[progress.description]{task.description}"),
        BarColumn(),
        TextColumn("{task.completed}/{task.total}"),
        TimeElapsedColumn(),
    ) as progress:
        task = progress.add_task(f"Scanning 0/{total}", total=total or 1)
        with ThreadPoolExecutor(max_workers=max(1, workers)) as pool:
            futures = {
                pool.submit(_process_one, path, extract_audio=extract_audio): path
                for path in files
            }
            for fut in as_completed(futures):
                status, payload, err = fut.result()
                done += 1
                progress.advance(task)
                progress.update(
                    task,
                    description=f"Scanning · ok={result.upserted} fail={result.failed}",
                )
                if status != "ok" or payload is None:
                    result.failed += 1
                    if done % 25 == 0 or done == total:
                        _emit()
                    continue
                path_str = payload["path"]
                seen.add(path_str)
                genres = payload.pop("genres", [])
                bpm = payload.pop("bpm", None)
                key_name = payload.pop("key_name", None)
                mode = payload.pop("mode", None)
                tid = upsert_track(payload)
                set_genres(tid, genres)
                if extract_audio:
                    update_audio_scalars(
                        tid, bpm=bpm, key_name=key_name, mode=mode, lufs=payload.get("lufs")
                    )
                result.upserted += 1
                if done % 25 == 0 or done == total:
                    _emit()

    if on_progress is not None:
        on_progress(
            {
                "phase": "scan_finalize",
                "done": total,
                "total": total,
                "pct": 100.0,
                "message": "marking missing / duplicates",
            }
        )
    result.inactivated = mark_missing_inactive(seen)
    if extract_audio:
        result.duplicates_marked = mark_duplicates()
    _emit()
    return result
