from __future__ import annotations

import sqlite3

import pytest

from musik.db.migrations import LATEST_SCHEMA_VERSION, migrate_db
from musik.db.schema import SCHEMA


def _version(path) -> int:
    with sqlite3.connect(path) as conn:
        return int(conn.execute("PRAGMA user_version").fetchone()[0])


def test_fresh_migration_is_idempotent(tmp_path) -> None:
    path = tmp_path / "fresh.db"
    assert migrate_db(path) == LATEST_SCHEMA_VERSION
    with sqlite3.connect(path) as conn:
        before = conn.execute(
            "SELECT type, name, sql FROM sqlite_master ORDER BY type, name"
        ).fetchall()
    assert migrate_db(path) == LATEST_SCHEMA_VERSION
    with sqlite3.connect(path) as conn:
        after = conn.execute(
            "SELECT type, name, sql FROM sqlite_master ORDER BY type, name"
        ).fetchall()
    assert before == after
    assert _version(path) == LATEST_SCHEMA_VERSION
    with sqlite3.connect(path) as conn:
        columns = {row[1] for row in conn.execute("PRAGMA table_info(device_tokens)")}
        assert columns == {
            "id", "secret_hash", "user_id", "profile_id", "name", "created_at",
            "last_used_at", "expires_at", "revoked_at",
        }
        plugin_jobs = {row[1] for row in conn.execute("PRAGMA table_info(plugin_jobs)")}
        assert plugin_jobs == {
            "id", "plugin_id", "action", "input_json", "status", "progress_json",
            "result_json", "error", "error_code", "downstream_job_id",
            "created_at", "updated_at",
        }
        assert conn.execute(
            "SELECT value FROM installation_state WHERE key='active_admin_guard'"
        ).fetchone() == ("1",)


def test_legacy_database_reaches_same_schema_and_preserves_rows(tmp_path) -> None:
    old_path = tmp_path / "old.db"
    fresh_path = tmp_path / "fresh.db"
    with sqlite3.connect(old_path) as conn:
        conn.executescript(SCHEMA)
        conn.executescript(
            """
            INSERT INTO tracks(id, path, title, created_at, updated_at)
            VALUES (1, '/music/a.flac', 'A', 'now', 'now');
            INSERT INTO listening_history(track_id, ts, action, weekday)
            VALUES (1, 'now', 'finish', 0);
            """
        )

    migrate_db(old_path)
    migrate_db(fresh_path)
    with sqlite3.connect(old_path) as old, sqlite3.connect(fresh_path) as fresh:
        old_tables = {
            row[0] for row in old.execute("SELECT name FROM sqlite_master WHERE type='table'")
        }
        fresh_tables = {
            row[0] for row in fresh.execute("SELECT name FROM sqlite_master WHERE type='table'")
        }
        assert old_tables == fresh_tables
        assert old.execute("SELECT title FROM tracks WHERE id=1").fetchone()[0] == "A"
        # Legacy Python weekday 0 was Sunday; migration standardizes Monday=0.
        assert old.execute("SELECT weekday FROM listening_history").fetchone()[0] == 6
        assert _version(old_path) == _version(fresh_path) == LATEST_SCHEMA_VERSION


def test_foundation_schema_has_normalized_references_and_versioned_json(tmp_path) -> None:
    path = tmp_path / "foundation.db"
    migrate_db(path)
    with sqlite3.connect(path) as conn:
        tables = {
            row[0] for row in conn.execute("SELECT name FROM sqlite_master WHERE type='table'")
        }
        cols = {row[1] for row in conn.execute("PRAGMA table_info(playlists)")}
        assert {"type", "rule_json", "archived_at", "updated_at"} <= cols
        item_cols = {row[1] for row in conn.execute("PRAGMA table_info(playlist_tracks)")}
        assert {"item_id", "source", "added_at"} <= item_cols
        assert "plugin_jobs" in tables
        assert {
            "recommendation_requests",
            "request_contexts",
            "event_contexts",
            "taste_contexts",
            "taste_context_states",
            "session_contexts",
            "entity_vectors",
            "radio_rules",
            "model_versions",
            "training_runs",
            "explore_arms",
            "radio_prefs",
            "transition_stats",
            "custom_tags",
            "track_tags",
            "track_preferences",
        } <= tables

        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                """INSERT INTO taste_contexts(
                       context_id, kind, name, activation_schema_version, activation_json,
                       created_at, updated_at
                   ) VALUES ('bad', 'mood', 'Bad', NULL, '{}', 'now', 'now')"""
            )

        conn.execute(
            """INSERT INTO tracks(path, created_at, updated_at)
               VALUES ('/music/event.flac', 'now', 'now')"""
        )
        track_id = conn.execute("SELECT last_insert_rowid()").fetchone()[0]
        with pytest.raises(sqlite3.IntegrityError, match="versioned JSON"):
            conn.execute(
                """INSERT INTO listening_history(
                       track_id, ts, action, metadata_json
                   ) VALUES (?, 'now', 'start', '{}')""",
                (track_id,),
            )
        conn.execute(
            """INSERT INTO listening_history(
                   track_id, ts, action, event_id, event_schema_version
               ) VALUES (?, 'now', 'start', 'event-1', 1)""",
            (track_id,),
        )
        with pytest.raises(sqlite3.IntegrityError, match="append-only"):
            conn.execute(
                "UPDATE listening_history SET action='finish' WHERE event_id='event-1'"
            )


def test_identity_schema_enforces_profile_and_oidc_ownership(tmp_path) -> None:
    path = tmp_path / "identity.db"
    migrate_db(path)
    with sqlite3.connect(path) as conn:
        conn.execute("PRAGMA foreign_keys = ON")
        conn.execute(
            "INSERT INTO users(id, status, display_name, created_at, updated_at) "
            "VALUES ('user-a', 'active', 'A', 'now', 'now')"
        )
        conn.execute(
            "INSERT INTO users(id, status, display_name, created_at, updated_at) "
            "VALUES ('user-b', 'active', 'B', 'now', 'now')"
        )
        conn.execute(
            "INSERT INTO profiles(id, owner_user_id, name, is_default, created_at, updated_at) "
            "VALUES ('profile-a', 'user-a', 'Main', 1, 'now', 'now')"
        )
        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                "INSERT INTO profiles(id, owner_user_id, name, is_default, created_at, updated_at) "
                "VALUES ('profile-a2', 'user-a', 'Other', 1, 'now', 'now')"
            )
        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                "INSERT INTO profiles(id, owner_user_id, name, is_default, created_at, updated_at) "
                "VALUES ('orphan', 'missing', 'Orphan', 0, 'now', 'now')"
            )
        conn.execute(
            "INSERT INTO external_identities(issuer, subject, user_id, created_at) "
            "VALUES ('https://issuer.test', 'subject-1', 'user-a', 'now')"
        )
        with pytest.raises(sqlite3.IntegrityError):
            conn.execute(
                "INSERT INTO external_identities(issuer, subject, user_id, created_at) "
                "VALUES ('https://issuer.test', 'subject-1', 'user-b', 'now')"
            )
        conn.execute(
            "INSERT INTO external_identities(issuer, subject, user_id, created_at) "
            "VALUES ('https://other.test', 'subject-1', 'user-b', 'now')"
        )


def test_profile_migration_scopes_unique_event_keys_per_profile(tmp_path) -> None:
    path = tmp_path / "profile-unique.db"
    migrate_db(path)
    with sqlite3.connect(path) as conn:
        conn.execute("PRAGMA foreign_keys = ON")
        legacy_profile = conn.execute(
            "SELECT value FROM installation_state WHERE key='legacy_profile_id'"
        ).fetchone()[0]
        conn.execute(
            "INSERT INTO users(id,status,display_name,created_at,updated_at) "
            "VALUES ('user-b','active','B','now','now')"
        )
        conn.execute(
            "INSERT INTO profiles(id,owner_user_id,name,is_default,created_at,updated_at) "
            "VALUES ('profile-b','user-b','Main',1,'now','now')"
        )
        conn.execute("INSERT INTO tracks(path,created_at,updated_at) VALUES ('/a.flac','now','now')")
        track_id = conn.execute("SELECT last_insert_rowid()").fetchone()[0]
        for profile in (legacy_profile, "profile-b"):
            conn.execute(
                """INSERT INTO listening_history(
                       profile_id,track_id,ts,action,event_id,event_schema_version
                   ) VALUES (?,?,'now','track_start','same-client-event',1)""",
                (profile, track_id),
            )
        assert conn.execute(
            "SELECT count(*) FROM listening_history WHERE event_id='same-client-event'"
        ).fetchone()[0] == 2


def test_upstream_schema_10_upgrade_preserves_catalog_and_personal_data(tmp_path):
    from musik.db.migrations import MIGRATIONS

    path = tmp_path / "upstream-10.db"
    with sqlite3.connect(path) as conn:
        for migration in MIGRATIONS:
            if migration.version > 10:
                break
            migration.apply(conn)
            conn.execute(f"PRAGMA user_version = {migration.version}")
            conn.commit()
        conn.execute(
            """INSERT INTO tracks(id,path,title,artist,artist_segments,album_artist,
               is_compilation,is_remaster,created_at,updated_at)
               VALUES(1,'/music/upstream.flac','Preserved','A / B','["A","B"]',
               'Various Artists',1,1,'now','now')"""
        )
        conn.execute("INSERT INTO listening_history(track_id,ts,action,weekday) VALUES(1,'now','finish',0)")
        conn.execute("INSERT INTO playback_state(owner_scope,track_id,position_sec,updated_at) VALUES('local',1,42,'now')")

    assert migrate_db(path) == LATEST_SCHEMA_VERSION
    assert migrate_db(path) == LATEST_SCHEMA_VERSION
    with sqlite3.connect(path) as conn:
        assert conn.execute("SELECT title,artist_segments,album_artist,is_compilation,is_remaster FROM tracks WHERE id=1").fetchone() == (
            'Preserved', '["A","B"]', 'Various Artists', 1, 1,
        )
        profile = conn.execute("SELECT value FROM installation_state WHERE key='legacy_profile_id'").fetchone()[0]
        assert conn.execute("SELECT profile_id,weekday FROM listening_history WHERE track_id=1").fetchone() == (profile, 0)
        assert conn.execute("SELECT profile_id,owner_scope,track_id,position_sec FROM playback_state").fetchone() == (profile, "local", 1, 42)
        assert conn.execute("SELECT version_num FROM alembic_version").fetchone()[0] == f"musik_{LATEST_SCHEMA_VERSION}"
        assert conn.execute("PRAGMA foreign_key_check").fetchall() == []


def test_interrupted_playback_ownership_rolls_back_and_retries(tmp_path):
    from musik.db.migrations import MIGRATIONS, _profile_ownership

    class InterruptedConnection(sqlite3.Connection):
        interrupt = True

        def check_interruption(self, sql):
            if self.interrupt and "CREATE TABLE playback_state_by_profile" in sql:
                self.interrupt = False
                raise RuntimeError("interrupted playback ownership")

        def execute(self, sql, *args, **kwargs):
            self.check_interruption(sql)
            return super().execute(sql, *args, **kwargs)

        def executescript(self, sql):
            self.check_interruption(sql)
            return super().executescript(sql)

    path = tmp_path / "interrupted.db"
    with sqlite3.connect(path, factory=InterruptedConnection) as conn:
        for migration in MIGRATIONS:
            if migration.version > 11:
                break
            migration.apply(conn)
            conn.execute(f"PRAGMA user_version = {migration.version}")
            conn.commit()
        conn.execute("INSERT INTO playback_state(owner_scope,position_sec,updated_at) VALUES('local',42,'now')")
        conn.commit()
        with pytest.raises(RuntimeError, match="interrupted playback ownership"):
            _profile_ownership(conn)
        assert conn.execute("PRAGMA user_version").fetchone()[0] == 11
        assert conn.execute("SELECT count(*) FROM users").fetchone()[0] == 0
        assert "profile_id" not in {r[1] for r in conn.execute("PRAGMA table_info(listening_history)")}
        assert conn.execute("SELECT position_sec FROM playback_state").fetchone() == (42,)
        _profile_ownership(conn)
        assert conn.execute("PRAGMA user_version").fetchone()[0] == 12
        assert conn.execute("SELECT profile_id,position_sec FROM playback_state").fetchone()[1] == 42
    assert migrate_db(path) == LATEST_SCHEMA_VERSION


def test_home_postgres_v8_upgrade_preserves_owned_data(empty_postgres_database, tmp_path):
    """The deployed fork's Alembic v8 is distinct from upstream SQLite v8."""
    from sqlalchemy import create_engine, inspect, text
    from musik.db.migrations import migrate_database

    url = empty_postgres_database
    migrate_database(url, tmp_path / 'unused.db')
    engine = create_engine(url.replace('postgresql://', 'postgresql+psycopg://', 1))
    with engine.begin() as conn:
        conn.execute(text("INSERT INTO tracks(id,path,title,created_at,updated_at) VALUES (1,'/preserved.flac','Preserved','now','now')"))
        profile = conn.scalar(text("SELECT value FROM installation_state WHERE key='legacy_profile_id'"))
        user = conn.scalar(text("SELECT value FROM installation_state WHERE key='legacy_user_id'"))
        conn.execute(text("INSERT INTO listening_history(profile_id,track_id,ts,action) VALUES (:p,1,'now','finish')"), {'p': profile})
        conn.execute(text("INSERT INTO favorites(profile_id,track_id,added_at,position) VALUES (:p,1,'now',0)"), {'p': profile})
        conn.execute(text("INSERT INTO external_identities(issuer,subject,user_id,created_at) VALUES ('https://issuer.test','subject',:u,'now')"), {'u': user})
        conn.execute(text("INSERT INTO device_tokens(id,secret_hash,user_id,profile_id,name,created_at,expires_at) VALUES ('device','test-hash',:u,:p,'Device','now','2099')"), {'u': user, 'p': profile})
        for table in ('artist_photos', 'artwork_lookups', 'playback_state'):
            conn.execute(text(f'DROP TABLE {table}'))
        for column in ('is_remaster', 'artist_segments', 'album_artist', 'is_compilation'):
            conn.execute(text(f'ALTER TABLE tracks DROP COLUMN {column}'))
        conn.execute(text('ALTER TABLE external_identities DROP COLUMN password_hash'))
        conn.execute(text('DROP TABLE plugin_jobs'))
        conn.execute(text("UPDATE alembic_version SET version_num='musik_8'"))
    try:
        assert migrate_database(url, tmp_path / 'unused.db') == LATEST_SCHEMA_VERSION
        assert migrate_database(url, tmp_path / 'unused.db') == LATEST_SCHEMA_VERSION
        with engine.connect() as conn:
            assert conn.scalar(text('SELECT version_num FROM alembic_version')) == f'musik_{LATEST_SCHEMA_VERSION}'
            assert conn.execute(text('SELECT title,is_remaster,is_compilation FROM tracks')).one() == ('Preserved', 0, 0)
            assert conn.execute(text('SELECT profile_id,track_id FROM favorites')).one() == (profile, 1)
            assert conn.execute(text('SELECT profile_id,track_id FROM listening_history')).one() == (profile, 1)
            assert conn.execute(text('SELECT user_id,profile_id FROM device_tokens')).one() == (user, profile)
            assert conn.execute(text('SELECT user_id,password_hash FROM external_identities')).one() == (user, None)
            assert conn.scalar(text('SELECT count(*) FROM users')) == 1
            names = set(inspect(conn).get_table_names())
            assert {'artist_photos', 'artwork_lookups', 'playback_state', 'plugin_jobs'} <= names
            plugin_jobs = {column['name'] for column in inspect(conn).get_columns('plugin_jobs')}
            assert {
                'id', 'plugin_id', 'action', 'input_json', 'status', 'progress_json',
                'result_json', 'error', 'error_code', 'downstream_job_id',
                'created_at', 'updated_at',
            } <= plugin_jobs
    finally:
        engine.dispose()
