"""Persist generated playlists in SQLite."""

from __future__ import annotations

import json
from typing import Any

from musik.db.schema import connect, utcnow


def save_playlist(
    *,
    kind: str,
    name: str,
    entries: list[dict[str, Any]],
    meta: dict[str, Any] | None = None,
    retain: int = 14,
) -> int:
    """
    entries: [{track_id, explanation}, ...] in order.
    Returns playlist id.
    """
    now = utcnow()
    with connect() as conn:
        cur = conn.execute(
            "INSERT INTO playlists(kind, name, created_at, meta_json) VALUES (?,?,?,?)",
            (kind, name, now, json.dumps(meta or {}, ensure_ascii=False)),
        )
        pid = int(cur.lastrowid)
        for pos, e in enumerate(entries):
            conn.execute(
                """
                INSERT INTO playlist_tracks(playlist_id, position, track_id, explanation)
                VALUES (?,?,?,?)
                """,
                (pid, pos, int(e["track_id"]), e.get("explanation")),
            )
        if retain > 0:
            conn.execute(
                """
                DELETE FROM playlists
                WHERE kind = ? AND id NOT IN (
                    SELECT id FROM playlists
                    WHERE kind = ?
                    ORDER BY id DESC
                    LIMIT ?
                )
                """,
                (kind, kind, retain),
            )
        return pid


def list_playlists(limit: int = 30) -> list[dict[str, Any]]:
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT p.id, p.kind, p.name, p.created_at,
                   (SELECT COUNT(*) FROM playlist_tracks pt WHERE pt.playlist_id = p.id) AS n
            FROM playlists p
            ORDER BY p.id DESC
            LIMIT ?
            """,
            (limit,),
        ).fetchall()
        return [dict(r) for r in rows]


def latest_playlist(kind: str) -> dict[str, Any] | None:
    """Most recent saved playlist of the given kind (e.g. daily)."""
    with connect() as conn:
        row = conn.execute(
            """
            SELECT id FROM playlists
            WHERE kind = ?
            ORDER BY id DESC
            LIMIT 1
            """,
            (kind,),
        ).fetchone()
    if not row:
        return None
    return get_playlist(int(row["id"]))


def get_playlist(playlist_id: int) -> dict[str, Any] | None:
    with connect() as conn:
        row = conn.execute(
            "SELECT id, kind, name, created_at, meta_json FROM playlists WHERE id = ?",
            (playlist_id,),
        ).fetchone()
        if not row:
            return None
        pl = dict(row)
        tracks = conn.execute(
            """
            SELECT pt.position, pt.track_id, pt.explanation,
                   t.artist, t.title, t.path
            FROM playlist_tracks pt
            JOIN tracks t ON t.id = pt.track_id
            WHERE pt.playlist_id = ?
            ORDER BY pt.position
            """,
            (playlist_id,),
        ).fetchall()
        pl["tracks"] = [dict(t) for t in tracks]
        return pl
