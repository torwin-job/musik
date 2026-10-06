from __future__ import annotations

import sqlite3
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path


@dataclass(frozen=True)
class Migration:
    version: int
    name: str
    apply: Callable[[sqlite3.Connection], None]


def _columns(conn: sqlite3.Connection, table: str) -> set[str]:
    return {str(row[1]) for row in conn.execute(f"PRAGMA table_info({table})")}


def _add_column(conn: sqlite3.Connection, table: str, definition: str) -> None:
    name = definition.split(maxsplit=1)[0]
    if name not in _columns(conn, table):
        conn.execute(f"ALTER TABLE {table} ADD COLUMN {definition}")


def _baseline(conn: sqlite3.Connection) -> None:
    # Imported lazily to keep schema.py's public SCHEMA compatibility without
    # making migration discovery depend on import order.
    from musik.db.schema import SCHEMA

    conn.executescript(SCHEMA)
    _add_column(conn, "listening_history", "listened_sec REAL")
    _add_column(conn, "listening_history", "session_id TEXT")
    _add_column(conn, "listening_history", "reason TEXT")

    conn.execute(
        """CREATE TABLE IF NOT EXISTS schema_migrations (
               name TEXT PRIMARY KEY,
               applied_at TEXT NOT NULL
           )"""
    )
    marker = conn.execute(
        "SELECT 1 FROM schema_migrations WHERE name = ?",
        ("weekday_monday_zero_v1",),
    ).fetchone()
    if marker is None:
        conn.execute(
            """UPDATE listening_history
               SET weekday = (weekday + 6) % 7
               WHERE weekday BETWEEN 0 AND 6"""
        )
        conn.execute(
            """INSERT INTO schema_migrations(name, applied_at)
               VALUES ('weekday_monday_zero_v1', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))"""
        )


FOUNDATION_SQL = """
CREATE TABLE IF NOT EXISTS recommendation_requests (
    request_id       TEXT PRIMARY KEY,
    session_id       TEXT NOT NULL,
    reason           TEXT NOT NULL,
    policy_version   TEXT NOT NULL,
    model_version    TEXT,
    candidate_count  INTEGER NOT NULL CHECK (candidate_count >= 0),
    latency_ms       REAL NOT NULL CHECK (latency_ms >= 0),
    created_at       TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_recommendation_requests_session_created
    ON recommendation_requests(session_id, created_at DESC);

CREATE TABLE IF NOT EXISTS taste_contexts (
    context_id              TEXT PRIMARY KEY,
    kind                    TEXT NOT NULL CHECK (kind IN ('mood', 'place', 'activity')),
    name                    TEXT NOT NULL,
    icon                    TEXT,
    influence               REAL NOT NULL DEFAULT 1 CHECK (influence >= 0),
    learning_enabled        INTEGER NOT NULL DEFAULT 1 CHECK (learning_enabled IN (0, 1)),
    activation_schema_version INTEGER,
    activation_json         TEXT,
    created_at              TEXT NOT NULL,
    updated_at              TEXT NOT NULL,
    archived_at             TEXT,
    CHECK (
        (activation_json IS NULL AND activation_schema_version IS NULL)
        OR
        (activation_json IS NOT NULL AND activation_schema_version IS NOT NULL
         AND activation_schema_version > 0 AND json_valid(activation_json))
    )
);

CREATE TABLE IF NOT EXISTS taste_context_states (
    context_id              TEXT PRIMARY KEY REFERENCES taste_contexts(context_id) ON DELETE CASCADE,
    positive_vector         BLOB,
    embedding_dim           INTEGER CHECK (embedding_dim > 0),
    negative_schema_version INTEGER,
    negative_prototypes_json TEXT,
    positive_samples        INTEGER NOT NULL DEFAULT 0 CHECK (positive_samples >= 0),
    negative_samples        INTEGER NOT NULL DEFAULT 0 CHECK (negative_samples >= 0),
    model_version           TEXT NOT NULL,
    updated_at              TEXT NOT NULL,
    CHECK (
        (positive_vector IS NULL AND embedding_dim IS NULL)
        OR (positive_vector IS NOT NULL AND embedding_dim IS NOT NULL)
    ),
    CHECK (
        (negative_prototypes_json IS NULL AND negative_schema_version IS NULL)
        OR
        (negative_prototypes_json IS NOT NULL AND negative_schema_version IS NOT NULL
         AND negative_schema_version > 0 AND json_valid(negative_prototypes_json))
    )
);

CREATE TABLE IF NOT EXISTS session_contexts (
    session_id  TEXT NOT NULL,
    context_id  TEXT NOT NULL REFERENCES taste_contexts(context_id),
    activated_at TEXT NOT NULL,
    deactivated_at TEXT,
    PRIMARY KEY (session_id, context_id, activated_at)
);

CREATE INDEX IF NOT EXISTS idx_session_contexts_active
    ON session_contexts(session_id, deactivated_at);

CREATE TABLE IF NOT EXISTS entity_vectors (
    entity_type      TEXT NOT NULL
        CHECK (entity_type IN ('artist', 'album', 'genre', 'custom_collection')),
    entity_key       TEXT NOT NULL,
    embedding        BLOB NOT NULL,
    embedding_dim    INTEGER NOT NULL CHECK (embedding_dim > 0),
    model_version    TEXT NOT NULL,
    track_count      INTEGER NOT NULL CHECK (track_count > 0),
    computed_at      TEXT NOT NULL,
    PRIMARY KEY (entity_type, entity_key, model_version)
);

CREATE TABLE IF NOT EXISTS radio_rules (
    rule_id       TEXT PRIMARY KEY,
    target_type   TEXT NOT NULL
        CHECK (target_type IN ('track', 'song', 'artist', 'album', 'genre', 'cluster')),
    action        TEXT NOT NULL CHECK (action IN ('block', 'downrank', 'cooldown')),
    scope         TEXT NOT NULL CHECK (scope IN ('session', 'context', 'global')),
    target_key    TEXT NOT NULL,
    strength      REAL NOT NULL CHECK (strength >= 0),
    session_id    TEXT,
    context_id    TEXT REFERENCES taste_contexts(context_id),
    expires_at    TEXT,
    created_at    TEXT NOT NULL,
    archived_at   TEXT,
    CHECK (
        (scope = 'session' AND session_id IS NOT NULL AND context_id IS NULL)
        OR (scope = 'context' AND context_id IS NOT NULL AND session_id IS NULL)
        OR (scope = 'global' AND session_id IS NULL AND context_id IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_radio_rules_active
    ON radio_rules(scope, action, expires_at, archived_at);

CREATE TABLE IF NOT EXISTS model_versions (
    model_version          TEXT PRIMARY KEY,
    model_type             TEXT NOT NULL,
    feature_schema_version INTEGER NOT NULL CHECK (feature_schema_version > 0),
    artifact_path          TEXT NOT NULL,
    artifact_hash          TEXT NOT NULL,
    status                 TEXT NOT NULL CHECK (status IN ('candidate', 'active', 'rejected', 'retired')),
    created_at             TEXT NOT NULL,
    activated_at           TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_model_versions_one_active_type
    ON model_versions(model_type) WHERE status = 'active';

CREATE TABLE IF NOT EXISTS training_runs (
    run_id                 TEXT PRIMARY KEY,
    model_version          TEXT REFERENCES model_versions(model_version),
    model_type             TEXT NOT NULL,
    feature_schema_version INTEGER NOT NULL CHECK (feature_schema_version > 0),
    train_from             TEXT NOT NULL,
    train_until            TEXT NOT NULL,
    positive_count         INTEGER NOT NULL CHECK (positive_count >= 0),
    negative_count         INTEGER NOT NULL CHECK (negative_count >= 0),
    metrics_schema_version INTEGER NOT NULL CHECK (metrics_schema_version > 0),
    metrics_json           TEXT NOT NULL CHECK (json_valid(metrics_json)),
    status                 TEXT NOT NULL CHECK (status IN ('running', 'completed', 'failed', 'rejected')),
    created_at             TEXT NOT NULL,
    completed_at           TEXT
);
"""


def _future_data_foundation(conn: sqlite3.Connection) -> None:
    conn.executescript(FOUNDATION_SQL)

    # Legacy rows remain valid and identifiable. New writers can populate the
    # stable event identity and references without rewriting old history.
    _add_column(conn, "listening_history", "event_id TEXT")
    _add_column(conn, "listening_history", "event_schema_version INTEGER NOT NULL DEFAULT 1")
    _add_column(conn, "listening_history", "request_id TEXT REFERENCES recommendation_requests(request_id)")
    _add_column(conn, "listening_history", "impression_id TEXT")
    _add_column(conn, "listening_history", "device_id TEXT")
    _add_column(conn, "listening_history", "client_id TEXT")
    _add_column(conn, "listening_history", "metadata_schema_version INTEGER")
    _add_column(conn, "listening_history", "metadata_json TEXT")
    conn.execute(
        """CREATE UNIQUE INDEX IF NOT EXISTS idx_listening_history_event_id
           ON listening_history(event_id) WHERE event_id IS NOT NULL"""
    )
    conn.execute(
        """CREATE INDEX IF NOT EXISTS idx_listening_history_request
           ON listening_history(request_id) WHERE request_id IS NOT NULL"""
    )
    conn.executescript(
        """
        CREATE TRIGGER IF NOT EXISTS trg_listening_history_metadata_insert
        BEFORE INSERT ON listening_history
        WHEN NOT (
            (NEW.metadata_json IS NULL AND NEW.metadata_schema_version IS NULL)
            OR
            (NEW.metadata_json IS NOT NULL AND NEW.metadata_schema_version IS NOT NULL
             AND NEW.metadata_schema_version > 0 AND json_valid(NEW.metadata_json))
        )
        BEGIN
            SELECT RAISE(ABORT, 'event metadata requires valid versioned JSON');
        END;

        CREATE TRIGGER IF NOT EXISTS trg_listening_history_append_only_update
        BEFORE UPDATE ON listening_history
        BEGIN
            SELECT RAISE(ABORT, 'listening_history is append-only');
        END;

        CREATE TRIGGER IF NOT EXISTS trg_listening_history_append_only_delete
        BEFORE DELETE ON listening_history
        BEGIN
            SELECT RAISE(ABORT, 'listening_history is append-only');
        END;
        """
    )

    conn.execute(
        """CREATE TABLE IF NOT EXISTS event_contexts (
               history_id INTEGER NOT NULL REFERENCES listening_history(id) ON DELETE CASCADE,
               context_id TEXT NOT NULL REFERENCES taste_contexts(context_id),
               PRIMARY KEY (history_id, context_id)
           )"""
    )
    conn.execute(
        """CREATE TABLE IF NOT EXISTS request_contexts (
               request_id TEXT NOT NULL REFERENCES recommendation_requests(request_id) ON DELETE CASCADE,
               context_id TEXT NOT NULL REFERENCES taste_contexts(context_id),
               PRIMARY KEY (request_id, context_id)
           )"""
    )


LIFECYCLE_TASTE_SQL = """
CREATE TABLE IF NOT EXISTS track_stats (
    track_id         INTEGER PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
    plays            INTEGER NOT NULL DEFAULT 0 CHECK (plays >= 0),
    finishes         INTEGER NOT NULL DEFAULT 0 CHECK (finishes >= 0),
    partial          INTEGER NOT NULL DEFAULT 0 CHECK (partial >= 0),
    early_skips      INTEGER NOT NULL DEFAULT 0 CHECK (early_skips >= 0),
    likes            INTEGER NOT NULL DEFAULT 0 CHECK (likes >= 0),
    dislikes         INTEGER NOT NULL DEFAULT 0 CHECK (dislikes >= 0),
    last_played_at   TEXT,
    last_finished_at TEXT,
    updated_at       TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS taste_states (
    state_key               TEXT PRIMARY KEY,
    positive_vector         BLOB,
    embedding_dim           INTEGER,
    positive_samples        INTEGER NOT NULL DEFAULT 0 CHECK (positive_samples >= 0),
    negative_samples        INTEGER NOT NULL DEFAULT 0 CHECK (negative_samples >= 0),
    negative_schema_version INTEGER,
    negative_prototypes_json TEXT,
    model_version           TEXT NOT NULL DEFAULT 'clap-default',
    updated_at              TEXT NOT NULL,
    CHECK (
        (positive_vector IS NULL AND embedding_dim IS NULL)
        OR (positive_vector IS NOT NULL AND embedding_dim > 0)
    ),
    CHECK (
        (negative_prototypes_json IS NULL AND negative_schema_version IS NULL)
        OR (negative_prototypes_json IS NOT NULL AND negative_schema_version > 0
            AND json_valid(negative_prototypes_json))
    )
);

CREATE TABLE IF NOT EXISTS taste_centroids (
    idx               INTEGER NOT NULL,
    vector            BLOB NOT NULL,
    embedding_dim     INTEGER NOT NULL CHECK (embedding_dim > 0),
    mass              REAL NOT NULL CHECK (mass > 0),
    label             TEXT,
    sample_count      INTEGER NOT NULL CHECK (sample_count > 0),
    updated_at        TEXT NOT NULL,
    algorithm_version TEXT NOT NULL,
    PRIMARY KEY (idx, algorithm_version)
);
"""


def _recommendation_lifecycle_and_taste(conn: sqlite3.Connection) -> None:
    conn.executescript(LIFECYCLE_TASTE_SQL)

    # SQLite cannot add table-level constraints with ALTER TABLE, so stable
    # identities and lifecycle validation are enforced by partial indexes and
    # write-path conditional updates.
    for definition in (
        "impression_id TEXT",
        "request_id TEXT REFERENCES recommendation_requests(request_id)",
        "source TEXT",
        "features_schema_version INTEGER",
        "features_json TEXT",
        "queued_at TEXT",
        "played_at TEXT",
        "closed_at TEXT",
        "outcome TEXT NOT NULL DEFAULT 'pending'",
        "listened_ratio REAL",
        "legacy INTEGER NOT NULL DEFAULT 1",
    ):
        _add_column(conn, "recommendation_impressions", definition)

    # Existing queue rows cannot be reconstructed into honest plays. Give them
    # stable identities solely for auditability and keep them outside metrics.
    conn.execute(
        """UPDATE recommendation_impressions
           SET impression_id = COALESCE(impression_id, lower(hex(randomblob(16)))),
               source = COALESCE(source, 'legacy'),
               queued_at = COALESCE(queued_at, shown_at),
               outcome = CASE
                   WHEN played_at IS NULL AND closed_at IS NULL THEN 'legacy'
                   ELSE outcome
               END,
               closed_at = COALESCE(closed_at, shown_at),
               legacy = 1
           WHERE impression_id IS NULL OR queued_at IS NULL"""
    )
    conn.execute(
        """CREATE UNIQUE INDEX IF NOT EXISTS idx_impressions_identity
           ON recommendation_impressions(impression_id)"""
    )
    conn.execute(
        """CREATE INDEX IF NOT EXISTS idx_impressions_request
           ON recommendation_impressions(request_id, position)"""
    )
    conn.execute(
        """CREATE INDEX IF NOT EXISTS idx_impressions_pending_session
           ON recommendation_impressions(session_id, track_id, queued_at DESC)
           WHERE outcome = 'pending' AND closed_at IS NULL"""
    )
    conn.execute(
        """CREATE INDEX IF NOT EXISTS idx_impressions_metrics
           ON recommendation_impressions(legacy, source, queued_at, outcome)"""
    )
    conn.executescript(
        """
        CREATE TRIGGER IF NOT EXISTS trg_impressions_features_insert
        BEFORE INSERT ON recommendation_impressions
        WHEN NOT (
            (NEW.features_json IS NULL AND NEW.features_schema_version IS NULL)
            OR
            (NEW.features_json IS NOT NULL AND NEW.features_schema_version > 0
             AND json_valid(NEW.features_json))
        )
        BEGIN
            SELECT RAISE(ABORT, 'impression features require valid versioned JSON');
        END;

        CREATE TRIGGER IF NOT EXISTS trg_impressions_lifecycle_insert
        BEFORE INSERT ON recommendation_impressions
        WHEN NEW.impression_id IS NULL
          OR NEW.queued_at IS NULL
          OR NEW.source IS NULL
          OR NEW.outcome NOT IN (
              'pending', 'finished', 'partial', 'early_skip',
              'superseded', 'abandoned', 'legacy'
          )
          OR NEW.legacy NOT IN (0, 1)
        BEGIN
            SELECT RAISE(ABORT, 'invalid recommendation impression lifecycle');
        END;
        """
    )

    _add_column(conn, "play_sessions", "current_item_json TEXT")
    _add_column(conn, "play_sessions", "taste_state_schema_version INTEGER")
    _add_column(conn, "play_sessions", "taste_state_json TEXT")


PLAYLISTS_CONTEXTS_QUEUE_SQL = """
CREATE TABLE IF NOT EXISTS transition_stats (
    from_id         INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    to_id           INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    manual_count    INTEGER NOT NULL DEFAULT 0 CHECK (manual_count >= 0),
    radio_count     INTEGER NOT NULL DEFAULT 0 CHECK (radio_count >= 0),
    finished_count  INTEGER NOT NULL DEFAULT 0 CHECK (finished_count >= 0),
    partial_count   INTEGER NOT NULL DEFAULT 0 CHECK (partial_count >= 0),
    skip_count      INTEGER NOT NULL DEFAULT 0 CHECK (skip_count >= 0),
    decayed_weight  REAL NOT NULL DEFAULT 0,
    updated_at      TEXT NOT NULL,
    PRIMARY KEY (from_id, to_id)
);

CREATE TABLE IF NOT EXISTS custom_tags (
    tag_id      TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    archived_at TEXT,
    owner_scope TEXT NOT NULL DEFAULT 'local'
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_custom_tags_active_name
    ON custom_tags(name) WHERE archived_at IS NULL;

CREATE TABLE IF NOT EXISTS track_tags (
    tag_id     TEXT NOT NULL REFERENCES custom_tags(tag_id) ON DELETE CASCADE,
    track_id   INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,
    created_at TEXT NOT NULL,
    PRIMARY KEY (tag_id, track_id)
);

CREATE TABLE IF NOT EXISTS track_preferences (
    track_id   INTEGER PRIMARY KEY REFERENCES tracks(id) ON DELETE CASCADE,
    rating     TEXT NOT NULL DEFAULT 'neutral'
        CHECK (rating IN ('like', 'dislike', 'neutral')),
    favorite   INTEGER NOT NULL DEFAULT 0 CHECK (favorite IN (0, 1)),
    updated_at TEXT NOT NULL
);
"""


def _playlists_contexts_queue(conn: sqlite3.Connection) -> None:
    conn.executescript(PLAYLISTS_CONTEXTS_QUEUE_SQL)

    for definition in (
        "type TEXT NOT NULL DEFAULT 'generated'",
        "description TEXT",
        "updated_at TEXT",
        "cover_track_id INTEGER REFERENCES tracks(id)",
        "cover_artwork TEXT",
        "sort_mode TEXT NOT NULL DEFAULT 'manual'",
        "archived_at TEXT",
        "rule_schema_version INTEGER",
        "rule_json TEXT",
        "allow_duplicates INTEGER NOT NULL DEFAULT 0",
        "owner_scope TEXT NOT NULL DEFAULT 'local'",
    ):
        _add_column(conn, "playlists", definition)
    conn.execute(
        """UPDATE playlists
           SET type = CASE
                 WHEN type IN ('generated', 'manual', 'smart') THEN type
                 ELSE 'generated'
               END,
               updated_at = COALESCE(updated_at, created_at)"""
    )
    conn.executescript(
        """
        CREATE TRIGGER IF NOT EXISTS trg_playlists_rule_json
        BEFORE INSERT ON playlists
        WHEN NOT (
            (NEW.rule_json IS NULL AND NEW.rule_schema_version IS NULL)
            OR
            (NEW.rule_json IS NOT NULL AND NEW.rule_schema_version IS NOT NULL
             AND NEW.rule_schema_version > 0 AND json_valid(NEW.rule_json))
        )
        BEGIN
            SELECT RAISE(ABORT, 'smart playlist rule requires valid versioned JSON');
        END;
        """
    )

    cols = _columns(conn, "playlist_tracks")
    if "item_id" not in cols:
        conn.executescript(
            """
            CREATE TABLE playlist_tracks_v4 (
                item_id TEXT NOT NULL DEFAULT (lower(hex(randomblob(16)))),
                playlist_id INTEGER NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
                position INTEGER NOT NULL,
                track_id INTEGER REFERENCES tracks(id),
                unresolved_artist TEXT,
                unresolved_title TEXT,
                unresolved_path TEXT,
                added_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
                source TEXT NOT NULL DEFAULT 'manual',
                note TEXT,
                explanation TEXT,
                PRIMARY KEY (playlist_id, position)
            );
            INSERT INTO playlist_tracks_v4(
                item_id, playlist_id, position, track_id, added_at, source, explanation
            )
            SELECT lower(hex(randomblob(16))), pt.playlist_id, pt.position, pt.track_id,
                   COALESCE(p.created_at, strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
                   'rule', COALESCE(pt.explanation, '')
            FROM playlist_tracks pt
            JOIN playlists p ON p.id = pt.playlist_id;
            DROP TABLE playlist_tracks;
            ALTER TABLE playlist_tracks_v4 RENAME TO playlist_tracks;
            CREATE UNIQUE INDEX IF NOT EXISTS idx_playlist_tracks_item
                ON playlist_tracks(item_id);
            """
        )

    for definition in (
        "seeds_schema_version INTEGER",
        "seeds_json TEXT",
    ):
        _add_column(conn, "taste_contexts", definition)
    conn.executescript(
        """
        CREATE TRIGGER IF NOT EXISTS trg_taste_contexts_seeds_insert
        BEFORE INSERT ON taste_contexts
        WHEN NOT (
            (NEW.seeds_json IS NULL AND NEW.seeds_schema_version IS NULL)
            OR
            (NEW.seeds_json IS NOT NULL AND NEW.seeds_schema_version IS NOT NULL
             AND NEW.seeds_schema_version > 0 AND json_valid(NEW.seeds_json))
        )
        BEGIN
            SELECT RAISE(ABORT, 'context seeds require valid versioned JSON');
        END;
        """
    )
    _add_column(conn, "play_sessions", "active_contexts_json TEXT")
    _add_column(conn, "play_sessions", "transition_profile TEXT NOT NULL DEFAULT 'smooth'")


def _ranker_and_explore(conn: sqlite3.Connection) -> None:
    conn.executescript(
        """
        CREATE TABLE IF NOT EXISTS explore_arms (
            arm_key       TEXT PRIMARY KEY,
            arm_kind      TEXT NOT NULL CHECK (arm_kind IN ('aggregate', 'source')),
            alpha         REAL NOT NULL CHECK (alpha > 0),
            beta          REAL NOT NULL CHECK (beta > 0),
            successes     INTEGER NOT NULL DEFAULT 0 CHECK (successes >= 0),
            failures      INTEGER NOT NULL DEFAULT 0 CHECK (failures >= 0),
            last_decay_at TEXT,
            updated_at    TEXT NOT NULL,
            owner_scope   TEXT NOT NULL DEFAULT 'local'
        );

        CREATE TABLE IF NOT EXISTS radio_prefs (
            owner_scope TEXT PRIMARY KEY DEFAULT 'local',
            explore_lo  REAL NOT NULL DEFAULT 0.10 CHECK (explore_lo >= 0 AND explore_lo < 1),
            explore_hi  REAL NOT NULL DEFAULT 0.40 CHECK (explore_hi > 0 AND explore_hi <= 1),
            updated_at  TEXT NOT NULL,
            CHECK (explore_lo < explore_hi)
        );
        """
    )
    _add_column(conn, "recommendation_requests", "policy_schema_version INTEGER")
    _add_column(conn, "recommendation_requests", "policy_json TEXT")
    conn.executescript(
        """
        CREATE TRIGGER IF NOT EXISTS trg_recommendation_requests_policy_insert
        BEFORE INSERT ON recommendation_requests
        WHEN NOT (
            (NEW.policy_json IS NULL AND NEW.policy_schema_version IS NULL)
            OR
            (NEW.policy_json IS NOT NULL AND NEW.policy_schema_version IS NOT NULL
             AND NEW.policy_schema_version > 0 AND json_valid(NEW.policy_json))
        )
        BEGIN
            SELECT RAISE(ABORT, 'request policy requires valid versioned JSON');
        END;
        """
    )


def _remaster_marker(conn: sqlite3.Connection) -> None:
    # The scanner keeps the full album tag (including "… (2001 Remastered)")
    # and records whether the marker was present. Dedup uses the flag together
    # with album and year to keep an original and its remaster apart.
    _add_column(conn, "tracks", "is_remaster INTEGER NOT NULL DEFAULT 0")


def _artist_segments(conn: sqlite3.Connection) -> None:
    # Collaborator credits ("Thomas / БИ-2 / Сплин") are parsed once during the
    # scan; the segments live here so every reader (API, UI) shows the same
    # split and no client has to re-implement the parsing rule.
    _add_column(conn, "tracks", "artist_segments TEXT")


MIGRATIONS = (
    Migration(1, "baseline", _baseline),
    Migration(2, "future_data_foundation", _future_data_foundation),
    Migration(3, "recommendation_lifecycle_and_taste", _recommendation_lifecycle_and_taste),
    Migration(4, "playlists_contexts_queue", _playlists_contexts_queue),
    Migration(5, "ranker_and_explore", _ranker_and_explore),
    Migration(6, "remaster_marker", _remaster_marker),
    Migration(7, "artist_segments", _artist_segments),
)
LATEST_SCHEMA_VERSION = MIGRATIONS[-1].version


def schema_version(conn: sqlite3.Connection) -> int:
    return int(conn.execute("PRAGMA user_version").fetchone()[0])


def migrate_connection(conn: sqlite3.Connection) -> int:
    current = schema_version(conn)
    if current > LATEST_SCHEMA_VERSION:
        raise RuntimeError(
            f"database schema version {current} is newer than supported "
            f"version {LATEST_SCHEMA_VERSION}"
        )
    for migration in MIGRATIONS:
        if migration.version <= current:
            continue
        migration.apply(conn)
        conn.execute(f"PRAGMA user_version = {migration.version}")
        conn.commit()
        current = migration.version
    return current


def migrate_db(db_path: Path) -> int:
    db_path.parent.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(db_path)
    try:
        conn.execute("PRAGMA foreign_keys = ON")
        conn.execute("PRAGMA journal_mode = WAL")
        conn.execute("PRAGMA busy_timeout = 5000")
        return migrate_connection(conn)
    except Exception:
        conn.rollback()
        raise
    finally:
        conn.close()
