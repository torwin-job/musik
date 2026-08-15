"""Album discovery tips: new releases and resurfaced catalog gems."""

from __future__ import annotations

import json
from collections import defaultdict
from datetime import datetime, timedelta, timezone
from typing import Any

import numpy as np

from musik.db.schema import connect, utcnow
from musik.index.brute import EmbeddingIndex, load_index
from musik.listen.profile import resolve_taste


def _parse_ts(ts: str) -> datetime:
    return datetime.fromisoformat(ts.replace("Z", "+00:00"))


def _taste_vector(index: EmbeddingIndex) -> np.ndarray:
    vec, _src = resolve_taste(index)
    return vec


def _cosine(a: np.ndarray, b: np.ndarray) -> float:
    return float(np.dot(a, b))


def _album_groups(index: EmbeddingIndex) -> dict[tuple[str, str], list[int]]:
    """Map (artist, album) -> row indices in index."""
    track_ids = [int(m["id"]) for m in index.meta]
    album_by_id: dict[int, tuple[str, str]] = {}
    if track_ids:
        placeholders = ",".join("?" * len(track_ids))
        with connect() as conn:
            rows = conn.execute(
                f"""
                SELECT id, artist, album FROM tracks
                WHERE id IN ({placeholders})
                """,
                track_ids,
            ).fetchall()
        for r in rows:
            album_by_id[int(r["id"])] = (
                (r["artist"] or "").strip(),
                (r["album"] or "").strip(),
            )

    groups: dict[tuple[str, str], list[int]] = defaultdict(list)
    for i, meta in enumerate(index.meta):
        tid = int(meta["id"])
        artist, album = album_by_id.get(tid, ("", ""))
        if not album:
            continue
        groups[(artist, album)].append(i)
    return groups


def _track_created_at() -> dict[int, str]:
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT id, created_at FROM tracks
            WHERE is_active = 1 AND is_duplicate_of IS NULL
            """
        ).fetchall()
    return {int(r["id"]): r["created_at"] for r in rows}


def _rec_completed() -> dict[int, int]:
    with connect() as conn:
        rows = conn.execute(
            "SELECT track_id, completed FROM rec_stats"
        ).fetchall()
    return {int(r["track_id"]): int(r["completed"]) for r in rows}


def _album_dates(
    row_indices: list[int], index: EmbeddingIndex, created: dict[int, str]
) -> tuple[datetime | None, datetime | None]:
    dates: list[datetime] = []
    for i in row_indices:
        tid = int(index.meta[i]["id"])
        ts = created.get(tid)
        if ts:
            dates.append(_parse_ts(ts))
    if not dates:
        return None, None
    return min(dates), max(dates)


def _album_mean_embedding(index: EmbeddingIndex, row_indices: list[int]) -> np.ndarray:
    return index.centroid(row_indices)


def _top_track_ids(
    index: EmbeddingIndex, row_indices: list[int], taste: np.ndarray, *, limit: int = 3
) -> list[int]:
    sims = [(i, _cosine(index.matrix[i], taste)) for i in row_indices]
    sims.sort(key=lambda x: -x[1])
    return [int(index.meta[i]["id"]) for i, _ in sims[:limit]]


def _save_tips(kind: str, tips: list[dict[str, Any]]) -> int:
    now = utcnow()
    with connect() as conn:
        conn.execute("DELETE FROM discover_tips WHERE kind = ?", (kind,))
        for tip in tips:
            conn.execute(
                """
                INSERT INTO discover_tips(
                    kind, artist, album, score, track_ids_json, explanation, created_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?)
                """,
                (
                    kind,
                    tip.get("artist"),
                    tip.get("album"),
                    float(tip["score"]),
                    json.dumps(tip["track_ids"], ensure_ascii=False),
                    tip.get("explanation"),
                    now,
                ),
            )
    return len(tips)


def rebuild_discover_tips(
    *,
    new_album_days: int = 14,
    limit_new: int = 10,
    limit_old: int = 10,
) -> dict[str, Any]:
    """
    Recompute discover_tips for new_album and resurfaced kinds.
    Clears previous tips of each kind before insert.
    """
    index = load_index()
    if index.size == 0:
        with connect() as conn:
            conn.execute("DELETE FROM discover_tips")
        return {"new_album": 0, "resurfaced": 0, "reason": "empty index"}

    taste = _taste_vector(index)
    created = _track_created_at()
    completed = _rec_completed()
    groups = _album_groups(index)
    cutoff = datetime.now(timezone.utc) - timedelta(days=new_album_days)

    # --- new_album ---
    new_candidates: list[dict[str, Any]] = []
    for (artist, album), rows in groups.items():
        oldest, newest = _album_dates(rows, index, created)
        if newest is None or newest < cutoff:
            continue
        mean_emb = _album_mean_embedding(index, rows)
        score = _cosine(mean_emb, taste)
        track_ids = _top_track_ids(index, rows, taste)
        new_candidates.append(
            {
                "artist": artist or None,
                "album": album,
                "score": score,
                "track_ids": track_ids,
                "explanation": f"Новый альбом · cosine {score:.2f} к вкусу",
            }
        )
    new_candidates.sort(key=lambda x: -x["score"])
    new_tips = new_candidates[:limit_new]
    n_new = _save_tips("new_album", new_tips)

    # --- resurfaced ---
    # Prefer under-listened albums with high taste match. If the whole library
    # was scanned recently (created_at fresh), still surface "forgotten" albums
    # that are not already featured as new_album tips.
    new_keys = {(t.get("artist") or "", t["album"]) for t in new_tips}
    old_candidates: list[dict[str, Any]] = []
    for (artist, album), rows in groups.items():
        if (artist, album) in new_keys:
            continue
        oldest, newest = _album_dates(rows, index, created)
        track_ids_in_album = [int(index.meta[i]["id"]) for i in rows]
        total_completed = sum(completed.get(tid, 0) for tid in track_ids_in_album)
        if total_completed > 2:
            continue
        mean_emb = _album_mean_embedding(index, rows)
        score = _cosine(mean_emb, taste)
        # slight bonus for truly older scan dates when available
        age_days = 0.0
        if oldest:
            age_days = max(
                0.0, (datetime.now(timezone.utc) - oldest).total_seconds() / 86400.0
            )
        is_fresh_scan = newest is not None and newest >= cutoff
        rank = score * (1.0 - 0.08 * total_completed) + min(age_days, 365) * 0.0005
        if is_fresh_scan:
            rank *= 0.95  # still allow when whole lib is "new" in DB
        track_ids = _top_track_ids(index, rows, taste)
        old_candidates.append(
            {
                "artist": artist or None,
                "album": album,
                "score": score,
                "track_ids": track_ids,
                "explanation": (
                    f"Из старого каталога · cosine {score:.2f} к вкусу"
                    f" · listens {total_completed}"
                ),
                "_rank": rank,
            }
        )
    old_candidates.sort(key=lambda x: -x["_rank"])
    resurfaced_tips = [
        {k: v for k, v in tip.items() if not k.startswith("_")} for tip in old_candidates[:limit_old]
    ]
    n_old = _save_tips("resurfaced", resurfaced_tips)

    return {
        "new_album": n_new,
        "resurfaced": n_old,
        "new_album_days": new_album_days,
    }
