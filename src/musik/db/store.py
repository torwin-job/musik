from __future__ import annotations

from typing import Any

import numpy as np

from musik.db.schema import connect, init_db, row_to_dict, utcnow


def ensure_db() -> None:
    init_db()


def upsert_track(data: dict[str, Any]) -> int:
    """Insert or update track by path. Returns track id."""
    now = utcnow()
    with connect() as conn:
        existing = conn.execute(
            "SELECT id FROM tracks WHERE path = ?", (data["path"],)
        ).fetchone()
        if existing:
            tid = int(existing["id"])
            conn.execute(
                """
                UPDATE tracks SET
                    file_md5=:file_md5, file_mtime=:file_mtime, file_size=:file_size,
                    title=:title, artist=:artist, album=:album, year=:year,
                    track_number=:track_number, duration=:duration, bitrate=:bitrate,
                    sample_rate=:sample_rate, channels=:channels,
                    fingerprint=:fingerprint, lufs=:lufs,
                    artwork_path=:artwork_path, is_active=1, updated_at=:updated_at
                WHERE id=:id
                """,
                {**data, "id": tid, "updated_at": now},
            )
        else:
            cur = conn.execute(
                """
                INSERT INTO tracks (
                    path, file_md5, file_mtime, file_size, title, artist, album, year,
                    track_number, duration, bitrate, sample_rate, channels,
                    fingerprint, lufs, artwork_path, is_active, created_at, updated_at
                ) VALUES (
                    :path, :file_md5, :file_mtime, :file_size, :title, :artist, :album, :year,
                    :track_number, :duration, :bitrate, :sample_rate, :channels,
                    :fingerprint, :lufs, :artwork_path, 1, :created_at, :updated_at
                )
                """,
                {**data, "created_at": now, "updated_at": now},
            )
            tid = int(cur.lastrowid)

        # Ensure features row
        feat = conn.execute(
            "SELECT track_id FROM features WHERE track_id = ?", (tid,)
        ).fetchone()
        if not feat:
            conn.execute(
                "INSERT INTO features (track_id, status) VALUES (?, 'pending')",
                (tid,),
            )
        return tid


def set_genres(track_id: int, genre_names: list[str]) -> None:
    with connect() as conn:
        conn.execute("DELETE FROM track_genres WHERE track_id = ?", (track_id,))
        for name in genre_names:
            name = name.strip()
            if not name:
                continue
            conn.execute("INSERT OR IGNORE INTO genres (name) VALUES (?)", (name,))
            gid = conn.execute("SELECT id FROM genres WHERE name = ?", (name,)).fetchone()["id"]
            conn.execute(
                "INSERT OR IGNORE INTO track_genres (track_id, genre_id) VALUES (?, ?)",
                (track_id, gid),
            )


def mark_missing_inactive(seen_paths: set[str]) -> int:
    with connect() as conn:
        rows = conn.execute("SELECT id, path FROM tracks WHERE is_active = 1").fetchall()
        n = 0
        for row in rows:
            if row["path"] not in seen_paths:
                conn.execute(
                    "UPDATE tracks SET is_active = 0, updated_at = ? WHERE id = ?",
                    (utcnow(), row["id"]),
                )
                n += 1
        return n


def update_fingerprint_and_lufs(
    track_id: int, *, fingerprint: str | None, lufs: float | None
) -> None:
    with connect() as conn:
        conn.execute(
            "UPDATE tracks SET fingerprint = COALESCE(?, fingerprint), lufs = COALESCE(?, lufs), updated_at = ? WHERE id = ?",
            (fingerprint, lufs, utcnow(), track_id),
        )
        if lufs is not None:
            conn.execute(
                "UPDATE features SET lufs = ? WHERE track_id = ?",
                (lufs, track_id),
            )


def update_audio_scalars(
    track_id: int,
    *,
    bpm: float | None = None,
    key_name: str | None = None,
    mode: str | None = None,
    lufs: float | None = None,
) -> None:
    with connect() as conn:
        conn.execute(
            """
            UPDATE features SET
                bpm = COALESCE(?, bpm),
                key_name = COALESCE(?, key_name),
                mode = COALESCE(?, mode),
                lufs = COALESCE(?, lufs)
            WHERE track_id = ?
            """,
            (bpm, key_name, mode, lufs, track_id),
        )
        if lufs is not None:
            conn.execute(
                "UPDATE tracks SET lufs = ?, updated_at = ? WHERE id = ?",
                (lufs, utcnow(), track_id),
            )


def mark_duplicates() -> int:
    """Mark duplicates: same MD5, then fingerprint, then artist+title.

    Keeps the highest-bitrate (then largest) copy; others get is_duplicate_of.
    """
    with connect() as conn:
        # Clear previous duplicate flags among active tracks so re-runs are idempotent.
        conn.execute(
            """
            UPDATE tracks SET is_duplicate_of = NULL
            WHERE is_active = 1 AND is_duplicate_of IS NOT NULL
            """
        )
        marked = 0

        def _mark_groups(sql: str) -> int:
            rows = conn.execute(sql).fetchall()
            best: dict[str, int] = {}
            n = 0
            for row in rows:
                key = row["grp"]
                if not key:
                    continue
                if key not in best:
                    best[key] = row["id"]
                else:
                    conn.execute(
                        "UPDATE tracks SET is_duplicate_of = ?, updated_at = ? WHERE id = ?",
                        (best[key], utcnow(), row["id"]),
                    )
                    n += 1
            return n

        # 1) identical files
        marked += _mark_groups(
            """
            SELECT id,
                   file_md5 AS grp,
                   COALESCE(bitrate, 0) AS bitrate,
                   COALESCE(file_size, 0) AS file_size
            FROM tracks
            WHERE is_active = 1
              AND is_duplicate_of IS NULL
              AND file_md5 IS NOT NULL AND file_md5 != ''
            ORDER BY file_md5, bitrate DESC, file_size DESC, id ASC
            """
        )
        # 2) chromaprint (when present)
        marked += _mark_groups(
            """
            SELECT id,
                   fingerprint AS grp,
                   COALESCE(bitrate, 0) AS bitrate,
                   COALESCE(file_size, 0) AS file_size
            FROM tracks
            WHERE is_active = 1
              AND is_duplicate_of IS NULL
              AND fingerprint IS NOT NULL AND fingerprint != ''
            ORDER BY fingerprint, bitrate DESC, file_size DESC, id ASC
            """
        )
        # 3) same song metadata (different encodes / renames)
        marked += _mark_groups(
            """
            SELECT id,
                   lower(trim(artist)) || '|' || lower(trim(title)) AS grp,
                   COALESCE(bitrate, 0) AS bitrate,
                   COALESCE(file_size, 0) AS file_size
            FROM tracks
            WHERE is_active = 1
              AND is_duplicate_of IS NULL
              AND trim(COALESCE(artist, '')) != ''
              AND trim(COALESCE(title, '')) != ''
            ORDER BY lower(trim(artist)), lower(trim(title)),
                     bitrate DESC, file_size DESC, id ASC
            """
        )
        return marked


def counts() -> dict[str, int]:
    with connect() as conn:
        total = conn.execute("SELECT COUNT(*) FROM tracks").fetchone()[0]
        active = conn.execute(
            "SELECT COUNT(*) FROM tracks WHERE is_active = 1 AND is_duplicate_of IS NULL"
        ).fetchone()[0]
        pending = conn.execute(
            "SELECT COUNT(*) FROM features WHERE status IN ('pending', 'retry')"
        ).fetchone()[0]
        ready = conn.execute(
            "SELECT COUNT(*) FROM features WHERE status = 'ready'"
        ).fetchone()[0]
        failed = conn.execute(
            "SELECT COUNT(*) FROM features WHERE status = 'failed'"
        ).fetchone()[0]
        return {
            "tracks_total": int(total),
            "tracks_active": int(active),
            "features_pending": int(pending),
            "features_ready": int(ready),
            "features_failed": int(failed),
        }


def list_active_tracks(limit: int = 20) -> list[dict[str, Any]]:
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT id, title, artist, album, path, bitrate, lufs, fingerprint
            FROM tracks
            WHERE is_active = 1 AND is_duplicate_of IS NULL
            ORDER BY artist, album, track_number
            LIMIT ?
            """,
            (limit,),
        ).fetchall()
        return [dict(r) for r in rows]


def get_track_by_path(path: str) -> dict[str, Any] | None:
    with connect() as conn:
        return row_to_dict(
            conn.execute("SELECT * FROM tracks WHERE path = ?", (path,)).fetchone()
        )


def list_tracks_needing_embedding(
    *, limit: int | None = None, force: bool = False
) -> list[dict[str, Any]]:
    """Active non-duplicate tracks without a ready embedding (or all if force)."""
    sql = """
        SELECT t.id, t.path, t.file_md5, t.duration, t.title, t.artist, f.status
        FROM tracks t
        JOIN features f ON f.track_id = t.id
        WHERE t.is_active = 1 AND t.is_duplicate_of IS NULL
    """
    if not force:
        sql += " AND (f.status != 'ready' OR f.embedding IS NULL)"
    sql += " ORDER BY t.artist, t.album, t.track_number"
    if limit is not None:
        sql += f" LIMIT {int(limit)}"
    with connect() as conn:
        return [dict(r) for r in conn.execute(sql).fetchall()]


def save_embedding(track_id: int, embedding: np.ndarray, *, model_id: str | None = None) -> None:
    vec = np.asarray(embedding, dtype=np.float32).reshape(-1)
    blob = vec.tobytes()
    now = utcnow()
    with connect() as conn:
        conn.execute(
            """
            UPDATE features SET
                embedding = ?,
                embedding_dim = ?,
                status = 'ready',
                error = NULL,
                computed_at = ?
            WHERE track_id = ?
            """,
            (blob, int(vec.shape[0]), now, track_id),
        )
        if model_id:
            conn.execute(
                """
                INSERT INTO scan_state(key, value) VALUES('clap_model', ?)
                ON CONFLICT(key) DO UPDATE SET value = excluded.value
                """,
                (model_id,),
            )


def mark_feature_failed(track_id: int, error: str) -> None:
    with connect() as conn:
        conn.execute(
            """
            UPDATE features SET status = 'failed', error = ?, computed_at = ?
            WHERE track_id = ?
            """,
            (error[:2000], utcnow(), track_id),
        )


def get_embedding(track_id: int) -> np.ndarray | None:
    with connect() as conn:
        row = conn.execute(
            "SELECT embedding, embedding_dim FROM features WHERE track_id = ?",
            (track_id,),
        ).fetchone()
        if not row or row["embedding"] is None:
            return None
        dim = int(row["embedding_dim"] or 0)
        arr = np.frombuffer(row["embedding"], dtype=np.float32)
        if dim and arr.size != dim:
            return arr.astype(np.float32)
        return np.asarray(arr, dtype=np.float32)
