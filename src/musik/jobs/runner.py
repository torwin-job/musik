"""Process background jobs by kind."""

from __future__ import annotations

import logging
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

from musik.brain.generators import generate_daily
from musik.brain.mixes import generate_mix_pack
from musik.brain.train_ranker import train_ranker
from musik.config import get_settings
from musik.db.schema import connect
from musik.db.scoped_connection import profile_scope
from musik.discover.albums import rebuild_discover_tips
from musik.embed import embed_library
from musik.index import assign_clusters
from musik.jobs.queue import claim_next, fail_job, finish_job, update_job_progress
from musik.scanner import scan_library

log = logging.getLogger(__name__)

JOB_KINDS = frozenset(
    {
        "scan",
        "embed",
        "clusters",
        "daily",
        "album_tips",
        "full_rescan",
        "mix_pack",
        "train_ranker",
    }
)

# After these jobs the Go player should reload its in-memory matrix / tips.
_RELOAD_KINDS = frozenset(
    {"embed", "full_rescan", "clusters", "daily", "album_tips", "mix_pack", "train_ranker"}
)


def _progress_cb(job_id: int, *, start: float = 0, span: float = 100):
    def _cb(progress: dict[str, Any]) -> None:
        try:
            mapped = dict(progress)
            pct = progress.get("pct")
            if isinstance(pct, (int, float)):
                mapped["step_pct"] = round(float(pct), 2)
                mapped["pct"] = round(start + (float(pct) / 100.0) * span, 2)
            update_job_progress(job_id, mapped)
            log.info(
                "job #%s progress %s",
                job_id,
                progress.get("message") or progress.get("phase"),
            )
        except Exception as exc:  # noqa: BLE001
            log.debug("progress update failed: %s", exc)

    return _cb


def _notify_player_reload(kind: str) -> None:
    if kind not in _RELOAD_KINDS:
        return
    settings = get_settings()
    url = settings.player_reload_url.strip()
    if not url:
        return
    try:
        headers: dict[str, str] = {}
        if settings.api_token:
            headers["Authorization"] = f"Bearer {settings.api_token}"
        req = urllib.request.Request(url, method="POST", data=b"", headers=headers)
        with urllib.request.urlopen(req, timeout=10) as resp:
            log.info("notified player reload after %s → %s (%s)", kind, url, resp.status)
    except urllib.error.URLError as exc:
        log.warning("player reload notify failed (%s): %s", url, exc)
    except Exception as exc:  # noqa: BLE001
        log.warning("player reload notify failed: %s", exc)


def _run_scan(
    payload: dict[str, Any],
    *,
    job_id: int | None = None,
    progress_start: float = 0,
    progress_span: float = 100,
) -> dict[str, Any]:
    settings = get_settings()
    library = Path(payload["library"]) if payload.get("library") else settings.library
    result = scan_library(
        library,
        extract_audio=not payload.get("tags_only", False),
        workers=payload.get("workers"),
        limit=payload.get("limit"),
        on_progress=(
            _progress_cb(job_id, start=progress_start, span=progress_span)
            if job_id is not None
            else None
        ),
    )
    return {
        "scanned": result.scanned,
        "upserted": result.upserted,
        "skipped_unchanged": result.skipped_unchanged,
        "failed": result.failed,
        "inactivated": result.inactivated,
        "duplicates_marked": result.duplicates_marked,
    }


def _run_embed(
    payload: dict[str, Any],
    *,
    job_id: int | None = None,
    progress_start: float = 0,
    progress_span: float = 100,
) -> dict[str, Any]:
    settings = get_settings()
    result = embed_library(
        limit=payload.get("limit"),
        force=bool(payload.get("force", False)),
        workers=payload.get("workers", settings.embed_workers),
        on_progress=(
            _progress_cb(job_id, start=progress_start, span=progress_span)
            if job_id is not None
            else None
        ),
    )
    return {
        "total": result.total,
        "from_cache": result.from_cache,
        "computed": result.computed,
        "failed": result.failed,
        "skipped_missing": result.skipped_missing,
    }


def _run_clusters(payload: dict[str, Any]) -> dict[str, Any]:
    result = assign_clusters(k=int(payload.get("k", 8)))
    return {"k": result.k, "n": result.n, "inertia": result.inertia}


def _run_daily(payload: dict[str, Any]) -> dict[str, Any]:
    build = generate_daily(
        size=int(payload.get("size", 25)),
        explore_ratio=payload.get("explore_ratio"),
    )
    pid = build.persist()
    return {
        "playlist_id": pid,
        "name": build.name,
        "tracks": len(build.entries),
        "meta": build.meta,
    }


def _run_mix_pack(payload: dict[str, Any]) -> dict[str, Any]:
    from musik.brain.mixes import resolve_mix_contract

    # Resolve the shelf contract before any DB work so a missing mixes.json
    # fails instantly instead of running album_tips and then erroring.
    mixes = resolve_mix_contract(payload.get("mixes"))
    # refresh tips first so "Новинки" has data
    try:
        rebuild_discover_tips()
    except Exception as exc:  # noqa: BLE001
        log.warning("album_tips before mix_pack: %s", exc)
    return generate_mix_pack(
        daily_size=int(payload.get("daily_size", 25)),
        for_you_size=int(payload.get("for_you_size", 30)),
        weekday_size=int(payload.get("weekday_size", 25)),
        weekly_size=int(payload.get("weekly_size", 40)),
        new_size=int(payload.get("new_size", 20)),
        mixes=mixes,
    )


def _run_train_ranker(payload: dict[str, Any]) -> dict[str, Any]:
    return train_ranker(force=bool(payload.get("force", False)))


def _run_album_tips(payload: dict[str, Any]) -> dict[str, Any]:
    return rebuild_discover_tips(
        new_album_days=int(payload.get("new_album_days", 14)),
        limit_new=int(payload.get("limit_new", 10)),
        limit_old=int(payload.get("limit_old", 10)),
    )


def _run_full_rescan(payload: dict[str, Any], *, job_id: int | None = None) -> dict[str, Any]:
    out: dict[str, Any] = {}
    if job_id is not None:
        update_job_progress(job_id, {"phase": "full_rescan", "message": "scan…", "pct": 0})
    out["scan"] = _run_scan(
        payload, job_id=job_id, progress_start=0, progress_span=40
    )
    if job_id is not None:
        update_job_progress(job_id, {"phase": "full_rescan", "message": "embed…", "pct": 40})
    out["embed"] = _run_embed(
        payload, job_id=job_id, progress_start=40, progress_span=35
    )
    if job_id is not None:
        update_job_progress(job_id, {"phase": "full_rescan", "message": "clusters…", "pct": 75})
    out["clusters"] = _run_clusters(payload)
    if job_id is not None:
        update_job_progress(job_id, {"phase": "full_rescan", "message": "album_tips…", "pct": 85})
    if get_settings().multi_user:
        out["profiles"] = {}
        for profile_id in _active_profiles():
            with profile_scope(profile_id):
                out["profiles"][profile_id] = {"album_tips": _run_album_tips(payload), "mix_pack": _run_mix_pack(payload)}
        return out
    out["album_tips"] = _run_album_tips(payload)
    if job_id is not None:
        update_job_progress(job_id, {"phase": "full_rescan", "message": "mix_pack…", "pct": 92})
    out["mix_pack"] = _run_mix_pack(payload)
    return out


def _process_job(job: dict[str, Any]) -> dict[str, Any]:
    kind = job["kind"]
    payload = job.get("payload") or {}
    job_id = int(job["id"]) if job.get("id") is not None else None
    if kind == "scan":
        return _run_scan(payload, job_id=job_id)
    if kind == "embed":
        return _run_embed(payload, job_id=job_id)
    if kind == "clusters":
        return _run_clusters(payload)
    if kind == "daily":
        return _run_daily(payload)
    if kind == "mix_pack":
        return _run_mix_pack(payload)
    if kind == "album_tips":
        return _run_album_tips(payload)
    if kind == "full_rescan":
        return _run_full_rescan(payload, job_id=job_id)
    if kind == "train_ranker":
        return _run_train_ranker(payload)
    raise ValueError(f"unknown job kind: {kind}")


def _active_profiles() -> list[str]:
    with connect() as conn:
        return [row[0] for row in conn.execute(
            "SELECT p.id FROM profiles p JOIN users u ON u.id=p.owner_user_id WHERE p.deleted_at IS NULL AND u.status='active' ORDER BY p.id"
        )]


def process_job(job: dict[str, Any]) -> dict[str, Any]:
    payload = job.get("payload") or {}
    selected = payload.get("profile_id")
    if get_settings().multi_user:
        profiles = _active_profiles()
        if selected is not None and selected not in profiles:
            raise ValueError("job profile is not active")
        if selected is None and job["kind"] in {"daily", "mix_pack", "album_tips", "train_ranker"}:
            results = {}
            for profile_id in profiles:
                with profile_scope(profile_id):
                    results[profile_id] = _process_job(job)
            return {"profiles": results}
    if selected is None:
        with connect() as conn:
            selected = conn.profile_id
    with profile_scope(selected):
        return _process_job(job)


def run_one() -> dict[str, Any] | None:
    """Claim and process a single pending job. Returns job summary or None."""
    kinds = get_settings().worker_job_kinds
    if kinds is not None and (not kinds or not set(kinds).issubset(JOB_KINDS)):
        raise ValueError("worker_job_kinds must list known job kinds, or be unset for all")
    job = claim_next(kinds=kinds)
    if not job:
        return None
    job_id = int(job["id"])
    kind = job["kind"]
    log.info("running job #%s kind=%s", job_id, kind)
    try:
        if kind not in JOB_KINDS:
            raise ValueError(f"unknown job kind: {kind}")
        update_job_progress(job_id, {"phase": kind, "message": "starting", "pct": 0})
        result = process_job(job)
        finish_job(job_id, result)
        _notify_player_reload(kind)
        log.info("job #%s done", job_id)
        return {"id": job_id, "kind": kind, "status": "done", "result": result}
    except Exception as exc:
        log.exception("job #%s failed", job_id)
        fail_job(job_id, str(exc))
        return {"id": job_id, "kind": kind, "status": "failed", "error": str(exc)}


def run_pending(*, max_jobs: int | None = None) -> list[dict[str, Any]]:
    """Process pending jobs until queue is empty or max_jobs reached."""
    results: list[dict[str, Any]] = []
    while True:
        if max_jobs is not None and len(results) >= max_jobs:
            break
        summary = run_one()
        if summary is None:
            break
        results.append(summary)
    return results
