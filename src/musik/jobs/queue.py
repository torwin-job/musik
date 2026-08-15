"""SQLite-backed job queue."""

from __future__ import annotations

import json
from typing import Any

from musik.db.schema import connect, row_to_dict, utcnow


def _job_row(row: Any) -> dict[str, Any] | None:
    d = row_to_dict(row)
    if d is None:
        return None
    if d.get("payload_json"):
        try:
            d["payload"] = json.loads(d["payload_json"])
        except json.JSONDecodeError:
            d["payload"] = None
    else:
        d["payload"] = None
    if d.get("result_json"):
        try:
            d["result"] = json.loads(d["result_json"])
        except json.JSONDecodeError:
            d["result"] = None
    else:
        d["result"] = None
    return d


def enqueue_job(kind: str, payload: dict[str, Any] | None = None) -> dict[str, Any]:
    now = utcnow()
    payload_json = json.dumps(payload or {}, ensure_ascii=False)
    with connect() as conn:
        cur = conn.execute(
            """
            INSERT INTO jobs(kind, status, payload_json, created_at, updated_at)
            VALUES (?, 'pending', ?, ?, ?)
            """,
            (kind, payload_json, now, now),
        )
        job_id = int(cur.lastrowid)
        row = conn.execute("SELECT * FROM jobs WHERE id = ?", (job_id,)).fetchone()
    job = _job_row(row)
    assert job is not None
    return job


def enqueue_job_once(
    kind: str,
    payload: dict[str, Any],
) -> dict[str, Any] | None:
    """Atomically enqueue an exact kind/payload pair unless it already exists."""
    now = utcnow()
    payload_json = json.dumps(payload, ensure_ascii=False, sort_keys=True)
    with connect() as conn:
        existing = conn.execute(
            """
            SELECT * FROM jobs
            WHERE kind = ? AND payload_json = ?
            ORDER BY id DESC LIMIT 1
            """,
            (kind, payload_json),
        ).fetchone()
        if existing is not None:
            return None
        cur = conn.execute(
            """
            INSERT INTO jobs(kind, status, payload_json, created_at, updated_at)
            VALUES (?, 'pending', ?, ?, ?)
            """,
            (kind, payload_json, now, now),
        )
        row = conn.execute("SELECT * FROM jobs WHERE id = ?", (int(cur.lastrowid),)).fetchone()
    job = _job_row(row)
    assert job is not None
    return job


def claim_next() -> dict[str, Any] | None:
    now = utcnow()
    with connect() as conn:
        row = conn.execute(
            """
            UPDATE jobs
            SET status = 'running', updated_at = ?
            WHERE id = (
                SELECT id FROM jobs
                WHERE status = 'pending'
                ORDER BY id
                LIMIT 1
            )
            RETURNING *
            """,
            (now,),
        ).fetchone()
    return _job_row(row)


def finish_job(job_id: int, result: dict[str, Any] | None = None) -> None:
    now = utcnow()
    result_json = json.dumps(result or {}, ensure_ascii=False)
    with connect() as conn:
        conn.execute(
            """
            UPDATE jobs
            SET status = 'done', result_json = ?, error = NULL, updated_at = ?
            WHERE id = ?
            """,
            (result_json, now, job_id),
        )


def fail_job(job_id: int, error: str) -> None:
    now = utcnow()
    with connect() as conn:
        conn.execute(
            """
            UPDATE jobs
            SET status = 'failed', error = ?, updated_at = ?
            WHERE id = ?
            """,
            (error[:4000], now, job_id),
        )


def update_job_progress(job_id: int, progress: dict[str, Any]) -> None:
    """Write live progress into result_json while status=running (pollable via API)."""
    now = utcnow()
    body = {"progress": progress}
    result_json = json.dumps(body, ensure_ascii=False)
    with connect() as conn:
        conn.execute(
            """
            UPDATE jobs
            SET result_json = ?, updated_at = ?
            WHERE id = ? AND status = 'running'
            """,
            (result_json, now, job_id),
        )


def get_job(job_id: int) -> dict[str, Any] | None:
    with connect() as conn:
        row = conn.execute("SELECT * FROM jobs WHERE id = ?", (job_id,)).fetchone()
    return _job_row(row)


def list_recent(limit: int = 30) -> list[dict[str, Any]]:
    with connect() as conn:
        rows = conn.execute(
            "SELECT * FROM jobs ORDER BY id DESC LIMIT ?",
            (max(1, int(limit)),),
        ).fetchall()
    out: list[dict[str, Any]] = []
    for r in rows:
        job = _job_row(r)
        if job is not None:
            out.append(job)
    return out
