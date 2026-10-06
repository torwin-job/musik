from __future__ import annotations

import json
from typing import Any

import numpy as np

from musik.db.schema import connect, init_db, row_to_dict, utcnow


def ensure_db() -> None:
    init_db()


def upsert_track(data: dict[str, Any]) -> int:
    """Insert or update track by path. Returns track id."""
    now = utcnow()
    data = {**data, "is_remaster": 1 if data.get("is_remaster") else 0}
    segments = data.get("artist_segments")
    data["artist_segments"] = json.dumps(
        [str(s) for s in (segments or [])], ensure_ascii=False
    )
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
                    is_remaster=:is_remaster, artist_segments=:artist_segments,
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
                    is_remaster, artist_segments,
                    track_number, duration, bitrate, sample_rate, channels,
                    fingerprint, lufs, artwork_path, is_active, created_at, updated_at
                ) VALUES (
                    :path, :file_md5, :file_mtime, :file_size, :title, :artist, :album, :year,
                    :is_remaster, :artist_segments,
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


# First tie-breaker inside a duplicate group: container/format rank, lower wins.
# FLAC beats WAV/AIFF beats Opus/OGG beats M4A/AAC beats MP3 beats anything else.
FORMAT_RANK_SQL = """
    CASE
        WHEN lower(path) LIKE '%.flac' THEN 0
        WHEN lower(path) LIKE '%.wav'
          OR lower(path) LIKE '%.aiff'
          OR lower(path) LIKE '%.aif' THEN 1
        WHEN lower(path) LIKE '%.opus'
          OR lower(path) LIKE '%.ogg' THEN 2
        WHEN lower(path) LIKE '%.m4a'
          OR lower(path) LIKE '%.aac' THEN 3
        WHEN lower(path) LIKE '%.mp3' THEN 4
        ELSE 5
    END
"""

# Copies whose durations differ by more than this are different takes
# (live vs studio), even when artist, title and album agree.
DUPLICATE_DURATION_TOLERANCE_SEC = 3.0


def mark_duplicates() -> int:
    """Mark duplicates: same MD5, then fingerprint, then artist+title+album.

    The winner of a group is the finest copy: format rank first (FLAC >
    WAV/AIFF > Opus/OGG > M4A/AAC > MP3 > other), then highest bitrate,
    then largest file (stable: lowest id). Album, year and the remaster flag
    keep originals, remasters and other editions apart, and durations must
    agree within DUPLICATE_DURATION_TOLERANCE_SEC so live and studio takes
    that share one title never merge. Everyone else gets is_duplicate_of
    and disappears from the catalog until cleanup removes the file.
    """
    rank = FORMAT_RANK_SQL
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
            f"""
            SELECT id,
                   file_md5 AS grp,
                   COALESCE(bitrate, 0) AS bitrate,
                   COALESCE(file_size, 0) AS file_size
            FROM tracks
            WHERE is_active = 1
              AND is_duplicate_of IS NULL
              AND file_md5 IS NOT NULL AND file_md5 != ''
            ORDER BY file_md5, {rank},
                     bitrate DESC, file_size DESC, id ASC
            """
        )
        # 2) chromaprint (when present). Year and the remaster flag keep an
        # original and its remaster in different groups even when the
        # fingerprint survives mastering (Chromaprint is volume-robust).
        marked += _mark_groups(
            f"""
            SELECT id,
                   fingerprint || '|' || COALESCE(CAST(year AS TEXT), '')
                       || '|' || COALESCE(CAST(is_remaster AS TEXT), '0') AS grp,
                   COALESCE(bitrate, 0) AS bitrate,
                   COALESCE(file_size, 0) AS file_size
            FROM tracks
            WHERE is_active = 1
              AND is_duplicate_of IS NULL
              AND fingerprint IS NOT NULL AND fingerprint != ''
            ORDER BY fingerprint, COALESCE(CAST(year AS TEXT), ''),
                     COALESCE(CAST(is_remaster AS TEXT), '0'), {rank},
                     bitrate DESC, file_size DESC, id ASC
            """
        )
        # 3) same song metadata (different encodes / renames / editions).
        # Album, year and the remaster flag keep remasters and other editions
        # of the same song in separate groups (e.g. Scorpions 1979 original vs
        # 2001 remaster), and durations must agree within the tolerance so a
        # live take never absorbs the studio one with the same tags.
        rows = conn.execute(
            f"""
            SELECT id,
                   lower(trim(artist)) || '|' || lower(trim(title))
                       || '|' || lower(trim(COALESCE(album, '')))
                       || '|' || COALESCE(CAST(year AS TEXT), '')
                       || '|' || COALESCE(CAST(is_remaster AS TEXT), '0') AS grp,
                   duration
            FROM tracks
            WHERE is_active = 1
              AND is_duplicate_of IS NULL
              AND trim(COALESCE(artist, '')) != ''
              AND trim(COALESCE(title, '')) != ''
              AND duration IS NOT NULL AND duration > 0
            ORDER BY lower(trim(artist)), lower(trim(title)),
                     lower(trim(COALESCE(album, ''))),
                     COALESCE(CAST(year AS TEXT), ''),
                     COALESCE(CAST(is_remaster AS TEXT), '0'), {rank},
                     bitrate DESC, file_size DESC, id ASC
            """
        ).fetchall()
        kept: dict[str, list[tuple[int, float]]] = {}
        for row in rows:
            copies = kept.setdefault(row["grp"], [])
            best = next(
                (tid for tid, dur in copies
                 if abs(dur - row["duration"]) <= DUPLICATE_DURATION_TOLERANCE_SEC),
                None,
            )
            if best is None:
                copies.append((row["id"], row["duration"]))
            else:
                conn.execute(
                    "UPDATE tracks SET is_duplicate_of = ?, updated_at = ? WHERE id = ?",
                    (best, utcnow(), row["id"]),
                )
                marked += 1
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


def track_file_states() -> dict[str, dict[str, Any]]:
    """Return the lightweight file state needed by incremental scans."""
    with connect() as conn:
        rows = conn.execute(
            "SELECT path, file_mtime, file_size, is_active FROM tracks"
        ).fetchall()
        return {str(row["path"]): dict(row) for row in rows}


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


def list_tracks_needing_artwork(
    *, limit: int | None = None, force: bool = False
) -> list[dict[str, Any]]:
    """Active non-duplicate tracks with an album to look up.

    The caller checks the artwork file itself: a stored ``artwork_path`` may be
    empty, point at a container path, or reference a file that was deleted.
    Both cases ("empty path" or "file missing") are handled in Python, so the
    query never discards a row because it happens to carry a non-empty string.
    """
    sql = """
        SELECT t.id, t.path, t.file_md5, t.artist, t.album, t.artwork_path
        FROM tracks t
        WHERE t.is_active = 1 AND COALESCE(t.is_duplicate_of, 0) = 0
          AND trim(COALESCE(t.artist, '')) != ''
          AND trim(COALESCE(t.album, '')) != ''
          AND t.file_md5 IS NOT NULL AND t.file_md5 != ''
    """
    sql += " ORDER BY t.artist, t.album, t.track_number"
    if limit is not None:
        sql += f" LIMIT {int(limit)}"
    with connect() as conn:
        return [dict(r) for r in conn.execute(sql).fetchall()]


def recent_artwork_misses(since_iso: str) -> set[str]:
    """Album keys looked up without a match on or after since_iso."""
    with connect() as conn:
        rows = conn.execute(
            "SELECT album_key FROM artwork_lookups WHERE status = 'missing' AND checked_at >= ?",
            (since_iso,),
        ).fetchall()
    return {str(r[0]) for r in rows}


def record_artwork_lookup(album_key: str, found: bool) -> None:
    with connect() as conn:
        if found:
            conn.execute("DELETE FROM artwork_lookups WHERE album_key = ?", (album_key,))
        else:
            conn.execute(
                """
                INSERT INTO artwork_lookups(album_key, status, checked_at)
                VALUES (?, 'missing', ?)
                ON CONFLICT(album_key) DO UPDATE SET
                  status = excluded.status, checked_at = excluded.checked_at
                """,
                (album_key, utcnow()),
            )


def list_library_artists() -> list[str]:
    """Distinct artist names as the library shows them (collaborator segments)."""
    with connect() as conn:
        rows = conn.execute(
            """
            SELECT artist, artist_segments FROM tracks
            WHERE is_active = 1 AND COALESCE(is_duplicate_of, 0) = 0
            """
        ).fetchall()
    seen: dict[str, str] = {}
    for artist, segments_json in rows:
        try:
            segments = json.loads(segments_json) if segments_json else []
        except ValueError:
            segments = []
        names = [str(s) for s in segments if str(s).strip()] or [str(artist or "")]
        for name in names:
            name = " ".join(name.split())
            if name:
                seen.setdefault(name.lower(), name)
    return sorted(seen.values(), key=str.lower)


def artist_photo_states() -> dict[str, dict[str, Any]]:
    """name_key -> {status, path, checked_at} for every stored lookup."""
    with connect() as conn:
        rows = conn.execute(
            "SELECT name_key, status, path, checked_at FROM artist_photos"
        ).fetchall()
    return {
        str(r[0]): {"status": r[1], "path": r[2], "checked_at": r[3]} for r in rows
    }


def save_artist_photo(name_key: str, artist: str, path: str | None, source: str) -> None:
    status = "found" if path else "missing"
    with connect() as conn:
        conn.execute(
            """
            INSERT INTO artist_photos(name_key, artist, path, status, source, checked_at)
            VALUES (?, ?, ?, ?, ?, ?)
            ON CONFLICT(name_key) DO UPDATE SET
              artist = excluded.artist, path = excluded.path, status = excluded.status,
              source = excluded.source, checked_at = excluded.checked_at
            """,
            (name_key, artist, path, status, source, utcnow()),
        )


def save_artwork_path(track_id: int, path: str) -> None:
    with connect() as conn:
        conn.execute(
            "UPDATE tracks SET artwork_path = ?, updated_at = ? WHERE id = ?",
            (path, utcnow(), track_id),
        )
