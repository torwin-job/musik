"""Persist lyrics in SQLite."""

from __future__ import annotations

from typing import Any

from musik.db.schema import connect, utcnow


def get_lyrics(track_id: int) -> dict[str, Any] | None:
    with connect() as conn:
        row = conn.execute(
            """
            SELECT track_id, plain_lyrics, synced_lyrics, source, source_id,
                   instrumental, status, error, updated_at
            FROM lyrics WHERE track_id = ?
            """,
            (track_id,),
        ).fetchone()
    return dict(row) if row else None


def upsert_lyrics(
    track_id: int,
    *,
    plain_lyrics: str = "",
    synced_lyrics: str = "",
    source: str = "lrclib",
    source_id: str = "",
    instrumental: bool = False,
    status: str = "ready",
    error: str | None = None,
) -> None:
    now = utcnow()
    with connect() as conn:
        conn.execute(
            """
            INSERT INTO lyrics (
                track_id, plain_lyrics, synced_lyrics, source, source_id,
                instrumental, status, error, updated_at
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
            ON CONFLICT(track_id) DO UPDATE SET
                plain_lyrics = excluded.plain_lyrics,
                synced_lyrics = excluded.synced_lyrics,
                source = excluded.source,
                source_id = excluded.source_id,
                instrumental = excluded.instrumental,
                status = excluded.status,
                error = excluded.error,
                updated_at = excluded.updated_at
            """,
            (
                track_id,
                plain_lyrics,
                synced_lyrics,
                source,
                source_id,
                1 if instrumental else 0,
                status,
                error,
                now,
            ),
        )


def list_tracks_needing_lyrics(*, limit: int | None = None, force: bool = False) -> list[dict[str, Any]]:
    sql = """
        SELECT t.id, t.artist, t.title, t.album, t.duration
        FROM tracks t
        LEFT JOIN lyrics l ON l.track_id = t.id
        WHERE t.is_active = 1 AND COALESCE(t.is_duplicate_of, 0) = 0
    """
    if not force:
        sql += " AND (l.track_id IS NULL OR l.status IN ('pending', 'failed', 'missing'))"
    sql += " ORDER BY t.id"
    if limit is not None:
        sql += f" LIMIT {int(limit)}"
    with connect() as conn:
        rows = conn.execute(sql).fetchall()
    return [dict(r) for r in rows]
