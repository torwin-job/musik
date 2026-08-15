"""Watch MUSIK_LIBRARY and auto-run scan → embed → clusters/mixes → player reload."""

from __future__ import annotations

import logging
import threading
import time
from pathlib import Path
from typing import Callable

from watchdog.events import FileSystemEvent, FileSystemEventHandler
from watchdog.observers import Observer

from musik.config import get_settings

log = logging.getLogger(__name__)

# Incomplete downloads / temp junk
_IGNORE_SUFFIXES = (
    ".part",
    ".tmp",
    ".temp",
    ".crdownload",
    ".download",
    ". partial",
    ".bak",
    ".wkdownload",
)


def _is_audio_path(path: Path, extensions: tuple[str, ...]) -> bool:
    name = path.name
    if name.startswith(".") or name.endswith("~"):
        return False
    low = name.lower()
    if any(low.endswith(s) for s in _IGNORE_SUFFIXES):
        return False
    return path.suffix.lower() in extensions


class _DebouncedHandler(FileSystemEventHandler):
    def __init__(
        self,
        *,
        extensions: tuple[str, ...],
        debounce_sec: float,
        on_trigger: Callable[[], None],
    ) -> None:
        super().__init__()
        self.extensions = extensions
        self.debounce_sec = debounce_sec
        self.on_trigger = on_trigger
        self._lock = threading.Lock()
        self._timer: threading.Timer | None = None
        self._pending = 0

    def _maybe(self, path: str) -> None:
        p = Path(path)
        try:
            if p.exists() and p.is_dir():
                self._schedule()
                return
        except OSError:
            pass
        if _is_audio_path(p, self.extensions):
            self._schedule()

    def _schedule(self) -> None:
        with self._lock:
            self._pending += 1
            if self._timer is not None:
                self._timer.cancel()
            self._timer = threading.Timer(self.debounce_sec, self._fire)
            self._timer.daemon = True
            self._timer.start()
            log.info(
                "library change queued (pending=%s, run in %.0fs)",
                self._pending,
                self.debounce_sec,
            )

    def _fire(self) -> None:
        with self._lock:
            n = self._pending
            self._pending = 0
            self._timer = None
        log.info("library debounce fired after %s event(s) — starting pipeline", n)
        try:
            self.on_trigger()
        except Exception:
            log.exception("watch pipeline failed")

    def on_created(self, event: FileSystemEvent) -> None:
        if event.is_directory:
            self._schedule()
            return
        self._maybe(event.src_path)

    def on_modified(self, event: FileSystemEvent) -> None:
        if event.is_directory:
            return
        self._maybe(event.src_path)

    def on_moved(self, event: FileSystemEvent) -> None:
        if event.is_directory:
            self._schedule()
            return
        dest = getattr(event, "dest_path", None) or event.src_path
        self._maybe(dest)

    def on_deleted(self, event: FileSystemEvent) -> None:
        # deletions → mark inactive on next scan
        self._schedule()


def run_incremental_pipeline(
    *,
    tags_only: bool = False,
    do_clusters: bool = True,
    do_mixes: bool = True,
) -> dict:
    """scan → embed (pending only) → optional clusters/mixes → notify player."""
    from musik.discover.albums import rebuild_discover_tips
    from musik.embed import embed_library
    from musik.index import assign_clusters
    from musik.jobs.runner import _notify_player_reload
    from musik.scanner import scan_library

    settings = get_settings()
    out: dict = {}
    log.info("watch pipeline: scan %s", settings.library)
    scan = scan_library(settings.library, extract_audio=not tags_only)
    out["scan"] = {
        "scanned": scan.scanned,
        "upserted": scan.upserted,
        "failed": scan.failed,
        "inactivated": scan.inactivated,
    }
    log.info("watch pipeline: embed (pending only)")
    emb = embed_library(force=False)
    out["embed"] = {
        "total": emb.total,
        "computed": emb.computed,
        "from_cache": emb.from_cache,
        "failed": emb.failed,
    }
    if do_clusters:
        log.info("watch pipeline: clusters")
        out["clusters"] = assign_clusters()
    if do_mixes:
        log.info("watch pipeline: album tips + mix_pack")
        try:
            out["album_tips"] = rebuild_discover_tips()
        except Exception as exc:  # noqa: BLE001
            log.warning("album_tips: %s", exc)
        from musik.brain import generate_mix_pack

        out["mix_pack"] = generate_mix_pack()
    _notify_player_reload("full_rescan")
    log.info("watch pipeline done: %s", out)
    return out


def start_library_watch(
    *,
    library: Path | None = None,
    debounce_sec: float | None = None,
    tags_only: bool = False,
    do_clusters: bool = True,
    do_mixes: bool = True,
    blocking: bool = True,
) -> Observer:
    settings = get_settings()
    root = Path(library or settings.library).resolve()
    if not root.is_dir():
        raise FileNotFoundError(f"Library directory not found: {root}")
    delay = float(debounce_sec if debounce_sec is not None else settings.watch_debounce_sec)

    busy = threading.Lock()

    def _run() -> None:
        if not busy.acquire(blocking=False):
            log.warning("watch pipeline already running — will retry after debounce")
            # re-arm so we don't miss the batch
            handler._schedule()
            return
        try:
            run_incremental_pipeline(
                tags_only=tags_only, do_clusters=do_clusters, do_mixes=do_mixes
            )
        finally:
            busy.release()

    handler = _DebouncedHandler(
        extensions=settings.extensions,
        debounce_sec=delay,
        on_trigger=_run,
    )
    observer = Observer()
    observer.schedule(handler, str(root), recursive=True)
    observer.start()
    log.info(
        "watching %s (debounce=%.0fs, clusters=%s, mixes=%s)",
        root,
        delay,
        do_clusters,
        do_mixes,
    )
    if blocking:
        try:
            while True:
                time.sleep(1)
        except KeyboardInterrupt:
            log.info("watch stopped")
        finally:
            observer.stop()
            observer.join(timeout=5)
    return observer
