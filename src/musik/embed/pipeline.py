"""Embed pending tracks with CLAP; use disk cache keyed by MD5."""

from __future__ import annotations

import logging
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass
from pathlib import Path

from typing import Any, Callable

from rich.console import Console
from rich.progress import BarColumn, Progress, SpinnerColumn, TextColumn, TimeElapsedColumn, TimeRemainingColumn

from musik.config import get_settings
from musik.db import ensure_db
from musik.db.store import (
    list_tracks_needing_embedding,
    mark_feature_failed,
    save_embedding,
)
from musik.embed.cache import load_cached, save_cached
from musik.embed.clap import device_info, embed_file
from musik.embed.segments import SEGMENT_STRATEGY

logger = logging.getLogger(__name__)
console = Console()


@dataclass
class EmbedResult:
    total: int = 0
    from_cache: int = 0
    computed: int = 0
    failed: int = 0
    skipped_missing: int = 0


def _process_one(
    track: dict,
    *,
    model_id: str,
    segment_sec: float,
    force: bool,
) -> tuple[str, int, str | None]:
    """
    Returns (status, track_id, error).
    status: cache | computed | missing | fail
    """
    tid = int(track["id"])
    path = Path(track["path"])
    md5 = track.get("file_md5") or ""
    if not path.is_file():
        return "missing", tid, f"file missing: {path}"
    if not md5:
        return "fail", tid, "missing file_md5"

    try:
        vec = None if force else load_cached(md5, model_id)
        if vec is not None:
            save_embedding(tid, vec, model_id=model_id)
            return "cache", tid, None

        vec = embed_file(path, model_id=model_id, segment_sec=segment_sec)
        save_cached(md5, model_id, vec)
        save_embedding(tid, vec, model_id=model_id)
        return "computed", tid, None
    except Exception as exc:
        logger.exception("embed failed track_id=%s path=%s", tid, path)
        return "fail", tid, str(exc)


def embed_library(
    *,
    limit: int | None = None,
    force: bool = False,
    model_id: str | None = None,
    workers: int = 1,
    on_progress: Callable[[dict[str, Any]], None] | None = None,
) -> EmbedResult:
    """
    Compute CLAP embeddings for active non-duplicate tracks that are not ready.

    Model inference is heavy — default workers=1 (shared GPU/CPU model).
    Cache hits can be parallelized lightly; we still keep workers small.
    """
    settings = get_settings()
    ensure_db()
    model_id = model_id or settings.clap_model
    segment_sec = settings.embed_segment_sec

    device_name, has_cuda = device_info()
    if not has_cuda:
        console.print(
            "[bold yellow]⚠ GPU не найден — CLAP считается на CPU. "
            "На ~100 треках это могут быть десятки минут; на тысячах — часы. "
            "Кеш на диске позволит не пересчитывать повторно.[/bold yellow]"
        )
    else:
        console.print(f"[green]Device:[/green] {device_name}")

    console.print(
        f"[bold]CLAP[/bold] model={model_id} strategy={SEGMENT_STRATEGY} "
        f"windows=start/middle/end × {segment_sec:.0f}s"
    )

    tracks = list_tracks_needing_embedding(limit=limit, force=force)
    result = EmbedResult(total=len(tracks))
    if not tracks:
        console.print("[green]Нечего эмбеддить — все готовы или библиотека пуста.[/green]")
        return result

    # Warm model once in main thread before workers
    if any(True for t in tracks):
        from musik.embed.clap import _load_model

        _load_model(model_id)

    workers = max(1, workers)
    total = len(tracks)
    done = 0

    def _emit() -> None:
        if on_progress is None:
            return
        pct = (100.0 * done / total) if total else 100.0
        on_progress(
            {
                "phase": "embed",
                "done": done,
                "total": total,
                "pct": round(pct, 2),
                "from_cache": result.from_cache,
                "computed": result.computed,
                "failed": result.failed,
                "message": f"embed {done}/{total} ({pct:.1f}%) · new={result.computed} cache={result.from_cache}",
            }
        )

    _emit()
    with Progress(
        SpinnerColumn(),
        TextColumn("[progress.description]{task.description}"),
        BarColumn(),
        TextColumn("{task.completed}/{task.total}"),
        TimeElapsedColumn(),
        TimeRemainingColumn(),
        console=console,
    ) as progress:
        task = progress.add_task("Embedding", total=total)
        with ThreadPoolExecutor(max_workers=workers) as pool:
            futures = {
                pool.submit(
                    _process_one,
                    t,
                    model_id=model_id,
                    segment_sec=segment_sec,
                    force=force,
                ): t
                for t in tracks
            }
            for fut in as_completed(futures):
                status, tid, err = fut.result()
                done += 1
                progress.advance(task)
                progress.update(
                    task,
                    description=(
                        f"Embedding · new={result.computed} cache={result.from_cache} fail={result.failed}"
                    ),
                )
                if status == "cache":
                    result.from_cache += 1
                elif status == "computed":
                    result.computed += 1
                elif status == "missing":
                    result.skipped_missing += 1
                    mark_feature_failed(tid, err or "missing file")
                else:
                    result.failed += 1
                    mark_feature_failed(tid, err or "unknown error")
                # embed is slow — update every item
                _emit()

    return result
