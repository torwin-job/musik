// Package testdb creates the explicit SQL fixture used by Go tests. Production
// code never creates or migrates the shared database.
package testdb

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const Schema = `
PRAGMA foreign_keys = ON;
CREATE TABLE tracks (
 id INTEGER PRIMARY KEY, path TEXT NOT NULL DEFAULT '', file_md5 TEXT, title TEXT,
 artist TEXT, album TEXT, year INTEGER, duration REAL, lufs REAL,
 bitrate INTEGER, sample_rate INTEGER, channels INTEGER,
 is_active INTEGER NOT NULL DEFAULT 1,
 is_duplicate_of INTEGER, artwork_path TEXT, created_at TEXT,
 is_remaster INTEGER NOT NULL DEFAULT 0, artist_segments TEXT
);
CREATE TABLE features (
 track_id INTEGER PRIMARY KEY, status TEXT, cluster_id INTEGER, embedding BLOB,
 embedding_dim INTEGER, bpm REAL, key_name TEXT, mode TEXT, lufs REAL
);
CREATE TABLE listening_history (
 id INTEGER PRIMARY KEY AUTOINCREMENT, track_id INTEGER NOT NULL, ts TEXT NOT NULL,
 source TEXT, action TEXT NOT NULL, daypart TEXT, weekday INTEGER, position_sec REAL,
 duration_sec REAL, listened_sec REAL, session_id TEXT, reason TEXT, event_id TEXT,
 event_schema_version INTEGER NOT NULL DEFAULT 1, request_id TEXT, impression_id TEXT,
 device_id TEXT, client_id TEXT, metadata_schema_version INTEGER, metadata_json TEXT
);
CREATE TABLE rec_stats (
 track_id INTEGER PRIMARY KEY, shown INTEGER NOT NULL DEFAULT 0,
 skipped_early INTEGER NOT NULL DEFAULT 0, completed INTEGER NOT NULL DEFAULT 0,
 updated_at TEXT NOT NULL
);
CREATE TABLE recommendation_impressions (
 id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL, track_id INTEGER NOT NULL,
 position INTEGER NOT NULL, score REAL NOT NULL DEFAULT 0, cosine_taste REAL NOT NULL DEFAULT 0,
 cosine_current REAL NOT NULL DEFAULT 0, explore INTEGER NOT NULL DEFAULT 0,
 new_boost INTEGER NOT NULL DEFAULT 0, maturity TEXT NOT NULL DEFAULT '',
 mode TEXT NOT NULL DEFAULT '', shown_at TEXT NOT NULL, impression_id TEXT NOT NULL UNIQUE,
 request_id TEXT, source TEXT NOT NULL, features_schema_version INTEGER, features_json TEXT,
 queued_at TEXT NOT NULL, played_at TEXT, closed_at TEXT,
 outcome TEXT NOT NULL DEFAULT 'pending', listened_ratio REAL,
 legacy INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE recommendation_requests (
 request_id TEXT PRIMARY KEY, session_id TEXT NOT NULL, reason TEXT NOT NULL,
 policy_version TEXT NOT NULL, model_version TEXT, candidate_count INTEGER NOT NULL,
 latency_ms REAL NOT NULL, created_at TEXT NOT NULL,
 policy_schema_version INTEGER, policy_json TEXT
);
CREATE TABLE track_stats (
 track_id INTEGER PRIMARY KEY, plays INTEGER NOT NULL DEFAULT 0,
 finishes INTEGER NOT NULL DEFAULT 0, partial INTEGER NOT NULL DEFAULT 0,
 early_skips INTEGER NOT NULL DEFAULT 0, likes INTEGER NOT NULL DEFAULT 0,
 dislikes INTEGER NOT NULL DEFAULT 0, last_played_at TEXT, last_finished_at TEXT,
 updated_at TEXT NOT NULL
);
CREATE TABLE transitions (
 from_id INTEGER NOT NULL, to_id INTEGER NOT NULL, weight REAL NOT NULL DEFAULT 1,
 updated_at TEXT NOT NULL, PRIMARY KEY (from_id, to_id)
);
CREATE TABLE playlists (
 id INTEGER PRIMARY KEY, kind TEXT NOT NULL, name TEXT NOT NULL, created_at TEXT NOT NULL,
 meta_json TEXT, type TEXT NOT NULL DEFAULT 'generated', description TEXT, updated_at TEXT,
 cover_track_id INTEGER, cover_artwork TEXT, sort_mode TEXT NOT NULL DEFAULT 'manual',
 archived_at TEXT, rule_schema_version INTEGER, rule_json TEXT,
 allow_duplicates INTEGER NOT NULL DEFAULT 0, owner_scope TEXT NOT NULL DEFAULT 'local'
);
CREATE TABLE playlist_tracks (
 item_id TEXT, playlist_id INTEGER NOT NULL, position INTEGER NOT NULL, track_id INTEGER,
 unresolved_artist TEXT, unresolved_title TEXT, unresolved_path TEXT,
 added_at TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT 'manual',
 note TEXT, explanation TEXT, PRIMARY KEY (playlist_id, position)
);
CREATE TABLE jobs (
 id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending',
 payload_json TEXT, result_json TEXT, error TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE discover_tips (
 id INTEGER PRIMARY KEY AUTOINCREMENT, kind TEXT NOT NULL, artist TEXT, album TEXT,
 score REAL NOT NULL DEFAULT 0, track_ids_json TEXT NOT NULL, explanation TEXT, created_at TEXT NOT NULL
);
CREATE TABLE listen_later (track_id INTEGER PRIMARY KEY, added_at TEXT NOT NULL, position INTEGER NOT NULL DEFAULT 0);
CREATE TABLE favorites (track_id INTEGER PRIMARY KEY, added_at TEXT NOT NULL, position INTEGER NOT NULL DEFAULT 0);
CREATE TABLE favorite_artists (artist TEXT PRIMARY KEY, added_at TEXT NOT NULL, position INTEGER NOT NULL DEFAULT 0);
CREATE TABLE favorite_albums (
 artist TEXT NOT NULL, album TEXT NOT NULL, added_at TEXT NOT NULL,
 position INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (artist, album)
);
CREATE TABLE radio_shares (
 token TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
 revoked_at TEXT, last_listen_at TEXT, listen_count INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE play_sessions (
 id TEXT PRIMARY KEY, mode TEXT NOT NULL DEFAULT '', current_id INTEGER NOT NULL DEFAULT 0,
 queue_json TEXT, exclude_json TEXT, rated_json TEXT, daily_ids_json TEXT,
 daily_pos INTEGER NOT NULL DEFAULT 0, playlist_name TEXT NOT NULL DEFAULT '',
 playlist_kind TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL, current_item_json TEXT,
 taste_state_schema_version INTEGER, taste_state_json TEXT,
 active_contexts_json TEXT, transition_profile TEXT NOT NULL DEFAULT 'smooth'
);
CREATE TABLE lyrics (
 track_id INTEGER PRIMARY KEY, plain_lyrics TEXT NOT NULL DEFAULT '',
 synced_lyrics TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT '',
 source_id TEXT NOT NULL DEFAULT '', instrumental INTEGER NOT NULL DEFAULT 0,
 status TEXT NOT NULL DEFAULT 'pending', error TEXT, updated_at TEXT NOT NULL
);
CREATE TABLE user_profile_snapshots (
 id INTEGER PRIMARY KEY AUTOINCREMENT, context TEXT NOT NULL, embedding BLOB NOT NULL,
 created_at TEXT NOT NULL
);
CREATE TABLE taste_states (
 state_key TEXT PRIMARY KEY, positive_vector BLOB, embedding_dim INTEGER,
 positive_samples INTEGER NOT NULL DEFAULT 0, negative_samples INTEGER NOT NULL DEFAULT 0,
 negative_schema_version INTEGER, negative_prototypes_json TEXT,
 model_version TEXT NOT NULL DEFAULT 'clap-default', updated_at TEXT NOT NULL
);
CREATE TABLE taste_centroids (
 idx INTEGER NOT NULL, vector BLOB NOT NULL, embedding_dim INTEGER NOT NULL,
 mass REAL NOT NULL, label TEXT, sample_count INTEGER NOT NULL, updated_at TEXT NOT NULL,
 algorithm_version TEXT NOT NULL, PRIMARY KEY (idx, algorithm_version)
);
CREATE TABLE schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL);
CREATE TABLE taste_contexts (
 context_id TEXT PRIMARY KEY, kind TEXT NOT NULL, name TEXT NOT NULL, icon TEXT,
 influence REAL NOT NULL DEFAULT 1, learning_enabled INTEGER NOT NULL DEFAULT 1,
 activation_schema_version INTEGER, activation_json TEXT,
 seeds_schema_version INTEGER, seeds_json TEXT,
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL, archived_at TEXT
);
CREATE TABLE taste_context_states (
 context_id TEXT PRIMARY KEY, positive_vector BLOB, embedding_dim INTEGER,
 negative_schema_version INTEGER, negative_prototypes_json TEXT,
 positive_samples INTEGER NOT NULL DEFAULT 0, negative_samples INTEGER NOT NULL DEFAULT 0,
 model_version TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE session_contexts (
 session_id TEXT NOT NULL, context_id TEXT NOT NULL, activated_at TEXT NOT NULL,
 deactivated_at TEXT, PRIMARY KEY (session_id, context_id, activated_at)
);
CREATE TABLE entity_vectors (
 entity_type TEXT NOT NULL, entity_key TEXT NOT NULL, embedding BLOB NOT NULL,
 embedding_dim INTEGER NOT NULL, model_version TEXT NOT NULL,
 track_count INTEGER NOT NULL, computed_at TEXT NOT NULL,
 PRIMARY KEY (entity_type, entity_key, model_version)
);
CREATE TABLE radio_rules (
 rule_id TEXT PRIMARY KEY, target_type TEXT NOT NULL, action TEXT NOT NULL,
 scope TEXT NOT NULL, target_key TEXT NOT NULL, strength REAL NOT NULL,
 session_id TEXT, context_id TEXT, expires_at TEXT, created_at TEXT NOT NULL, archived_at TEXT
);
CREATE TABLE transition_stats (
 from_id INTEGER NOT NULL, to_id INTEGER NOT NULL,
 manual_count INTEGER NOT NULL DEFAULT 0, radio_count INTEGER NOT NULL DEFAULT 0,
 finished_count INTEGER NOT NULL DEFAULT 0, partial_count INTEGER NOT NULL DEFAULT 0,
 skip_count INTEGER NOT NULL DEFAULT 0, decayed_weight REAL NOT NULL DEFAULT 0,
 updated_at TEXT NOT NULL, PRIMARY KEY (from_id, to_id)
);
CREATE TABLE custom_tags (
 tag_id TEXT PRIMARY KEY, name TEXT NOT NULL, created_at TEXT NOT NULL,
 archived_at TEXT, owner_scope TEXT NOT NULL DEFAULT 'local'
);
CREATE TABLE track_tags (
 tag_id TEXT NOT NULL, track_id INTEGER NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY (tag_id, track_id)
);
CREATE TABLE track_preferences (
 track_id INTEGER PRIMARY KEY, rating TEXT NOT NULL DEFAULT 'neutral',
 favorite INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL
);
CREATE TABLE request_contexts (
 request_id TEXT NOT NULL, context_id TEXT NOT NULL, PRIMARY KEY (request_id, context_id)
);
CREATE TABLE event_contexts (
 history_id INTEGER NOT NULL, context_id TEXT NOT NULL, PRIMARY KEY (history_id, context_id)
);
CREATE UNIQUE INDEX idx_listening_history_event_id ON listening_history(event_id) WHERE event_id IS NOT NULL;
CREATE TABLE model_versions (
 model_version TEXT PRIMARY KEY, model_type TEXT NOT NULL,
 feature_schema_version INTEGER NOT NULL, artifact_path TEXT NOT NULL,
 artifact_hash TEXT NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL, activated_at TEXT
);
CREATE TABLE training_runs (
 run_id TEXT PRIMARY KEY, model_version TEXT, model_type TEXT NOT NULL,
 feature_schema_version INTEGER NOT NULL, train_from TEXT NOT NULL, train_until TEXT NOT NULL,
 positive_count INTEGER NOT NULL, negative_count INTEGER NOT NULL,
 metrics_schema_version INTEGER NOT NULL, metrics_json TEXT NOT NULL,
 status TEXT NOT NULL, created_at TEXT NOT NULL, completed_at TEXT
);
CREATE TABLE explore_arms (
 arm_key TEXT PRIMARY KEY, arm_kind TEXT NOT NULL, alpha REAL NOT NULL, beta REAL NOT NULL,
 successes INTEGER NOT NULL DEFAULT 0, failures INTEGER NOT NULL DEFAULT 0,
 last_decay_at TEXT, updated_at TEXT NOT NULL, owner_scope TEXT NOT NULL DEFAULT 'local'
);
CREATE TABLE radio_prefs (
 owner_scope TEXT PRIMARY KEY DEFAULT 'local',
 explore_lo REAL NOT NULL DEFAULT 0.10, explore_hi REAL NOT NULL DEFAULT 0.40,
 updated_at TEXT NOT NULL
);
 PRAGMA user_version = 7;
`

func Create(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	conn, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.Exec(Schema); err != nil {
		return fmt.Errorf("create test database: %w", err)
	}
	return nil
}
