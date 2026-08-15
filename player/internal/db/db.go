package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB *sql.DB
}

func mondayZeroWeekday(day time.Weekday) int {
	return (int(day) + 6) % 7
}

type TrackRow struct {
	ID          int64
	Path        string
	Title       string
	Artist      string
	Album       string
	Duration    float64
	FileMD5     string
	CreatedAt   string
	ArtworkPath string
	ClusterID   int
	Embedding   []byte
	Dim         int
	Shown       int
	SkipEarly   int
	Completed   int
}

func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{DB: db}
	if err := s.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	// Truncate WAL so it does not grow unbounded across restarts.
	_, _ = db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) ensureSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS rec_stats (
    track_id       INTEGER PRIMARY KEY,
    shown          INTEGER NOT NULL DEFAULT 0,
    skipped_early  INTEGER NOT NULL DEFAULT 0,
    completed      INTEGER NOT NULL DEFAULT 0,
    updated_at     TEXT NOT NULL
);`,
		`CREATE TABLE IF NOT EXISTS jobs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    payload_json TEXT,
    result_json TEXT,
    error TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);`,
		`CREATE TABLE IF NOT EXISTS discover_tips (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    kind TEXT NOT NULL,
    artist TEXT,
    album TEXT,
    score REAL NOT NULL DEFAULT 0,
    track_ids_json TEXT NOT NULL,
    explanation TEXT,
    created_at TEXT NOT NULL
);`,
		`CREATE TABLE IF NOT EXISTS listen_later (
    track_id INTEGER PRIMARY KEY,
    added_at TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0
);`,
		`CREATE TABLE IF NOT EXISTS favorites (
    track_id INTEGER PRIMARY KEY,
    added_at TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0
);`,
		`CREATE TABLE IF NOT EXISTS favorite_artists (
    artist TEXT PRIMARY KEY,
    added_at TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0
);`,
		`CREATE TABLE IF NOT EXISTS favorite_albums (
    artist TEXT NOT NULL,
    album TEXT NOT NULL,
    added_at TEXT NOT NULL,
    position INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (artist, album)
);`,
		`CREATE TABLE IF NOT EXISTS radio_shares (
    token TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    revoked_at TEXT,
    last_listen_at TEXT,
    listen_count INTEGER NOT NULL DEFAULT 0
);`,
		`CREATE TABLE IF NOT EXISTS play_sessions (
    id TEXT PRIMARY KEY,
    mode TEXT NOT NULL DEFAULT '',
    current_id INTEGER NOT NULL DEFAULT 0,
    queue_json TEXT,
    exclude_json TEXT,
    rated_json TEXT,
    daily_ids_json TEXT,
    daily_pos INTEGER NOT NULL DEFAULT 0,
    playlist_name TEXT NOT NULL DEFAULT '',
    playlist_kind TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL
);`,
		`CREATE TABLE IF NOT EXISTS lyrics (
    track_id INTEGER PRIMARY KEY,
    plain_lyrics TEXT NOT NULL DEFAULT '',
    synced_lyrics TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    source_id TEXT NOT NULL DEFAULT '',
    instrumental INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'pending',
    error TEXT,
    updated_at TEXT NOT NULL
);`,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
    name TEXT PRIMARY KEY,
    applied_at TEXT NOT NULL
);`,
		`CREATE INDEX IF NOT EXISTS idx_history_weekday_action
ON listening_history(weekday, action);`,
		`CREATE INDEX IF NOT EXISTS idx_playlists_kind_id
ON playlists(kind, id DESC);`,
	}
	for _, q := range stmts {
		if _, err := s.DB.Exec(q); err != nil {
			return err
		}
	}
	for _, col := range []string{
		"ALTER TABLE listening_history ADD COLUMN listened_sec REAL",
		"ALTER TABLE listening_history ADD COLUMN session_id TEXT",
		"ALTER TABLE listening_history ADD COLUMN reason TEXT",
	} {
		_, _ = s.DB.Exec(col)
	}
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	res, err := tx.Exec(
		`INSERT OR IGNORE INTO schema_migrations(name, applied_at) VALUES (?, ?)`,
		"weekday_monday_zero_v1", time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if changed, _ := res.RowsAffected(); changed > 0 {
		if _, err := tx.Exec(
			`UPDATE listening_history SET weekday = (weekday + 6) % 7
			 WHERE weekday BETWEEN 0 AND 6`,
		); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *Store) LoadReadyTracks() ([]TrackRow, error) {
	rows, err := s.DB.Query(`
SELECT t.id, t.path, COALESCE(t.title,''), COALESCE(t.artist,''), COALESCE(t.album,''),
       COALESCE(t.duration,0), COALESCE(t.file_md5,''), COALESCE(t.created_at,''),
       COALESCE(t.artwork_path,''), COALESCE(f.cluster_id, -1),
       f.embedding, COALESCE(f.embedding_dim,0),
       COALESCE(rs.shown,0), COALESCE(rs.skipped_early,0), COALESCE(rs.completed,0)
FROM tracks t
JOIN features f ON f.track_id = t.id
LEFT JOIN rec_stats rs ON rs.track_id = t.id
WHERE t.is_active = 1 AND t.is_duplicate_of IS NULL
  AND f.status = 'ready' AND f.embedding IS NOT NULL
ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrackRow
	for rows.Next() {
		var tr TrackRow
		if err := rows.Scan(&tr.ID, &tr.Path, &tr.Title, &tr.Artist, &tr.Album,
			&tr.Duration, &tr.FileMD5, &tr.CreatedAt, &tr.ArtworkPath, &tr.ClusterID,
			&tr.Embedding, &tr.Dim, &tr.Shown, &tr.SkipEarly, &tr.Completed); err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	return out, rows.Err()
}

// RecentTrackIDs returns distinct track ids heard in the last hours (most recent first).
func (s *Store) RecentTrackIDs(hours int, limit int) ([]int64, error) {
	if hours < 1 {
		hours = 24
	}
	if limit < 1 {
		limit = 40
	}
	rows, err := s.DB.Query(`
SELECT track_id FROM (
  SELECT track_id, MAX(ts) AS last_ts
  FROM listening_history
  WHERE ts >= datetime('now', ?)
  GROUP BY track_id
  ORDER BY last_ts DESC
  LIMIT ?
)`, fmt.Sprintf("-%d hours", hours), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) InsertListen(trackID int64, action, source, sessionID, reason string,
	position, duration, listened *float64) (int64, error) {
	now := time.Now().UTC()
	daypart := dayPart(now.Hour())
	res, err := s.DB.Exec(`
INSERT INTO listening_history(
  track_id, ts, source, action, daypart, weekday,
  position_sec, duration_sec, listened_sec, session_id, reason
) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		trackID, now.Format(time.RFC3339Nano), source, action, daypart,
		mondayZeroWeekday(now.Weekday()),
		position, duration, listened, nullStr(sessionID), nullStr(reason),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) BumpTransition(fromID, toID int64, weight float64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(`
INSERT INTO transitions(from_id, to_id, weight, updated_at) VALUES (?,?,?,?)
ON CONFLICT(from_id, to_id) DO UPDATE SET
  weight = weight + excluded.weight,
  updated_at = excluded.updated_at`, fromID, toID, weight, now)
	return err
}

func (s *Store) BumpRecStats(trackID int64, shown, skipEarly, completed int) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(`
INSERT INTO rec_stats(track_id, shown, skipped_early, completed, updated_at)
VALUES (?,?,?,?,?)
ON CONFLICT(track_id) DO UPDATE SET
  shown = shown + excluded.shown,
  skipped_early = skipped_early + excluded.skipped_early,
  completed = completed + excluded.completed,
  updated_at = excluded.updated_at`,
		trackID, shown, skipEarly, completed, now)
	return err
}

func (s *Store) SaveProfile(context string, emb []byte) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(
		`INSERT INTO user_profile_snapshots(context, embedding, created_at) VALUES (?,?,?)`,
		context, emb, now,
	)
	return err
}

// PruneProfiles keeps the newest keep snapshots for a context.
func (s *Store) PruneProfiles(context string, keep int) error {
	if keep < 1 {
		keep = 50
	}
	_, err := s.DB.Exec(`
DELETE FROM user_profile_snapshots
WHERE context = ? AND id NOT IN (
  SELECT id FROM user_profile_snapshots WHERE context = ?
  ORDER BY id DESC LIMIT ?
)`, context, context, keep)
	return err
}

func (s *Store) LatestProfile(context string) ([]byte, error) {
	var b []byte
	err := s.DB.QueryRow(`
SELECT embedding FROM user_profile_snapshots WHERE context = ?
ORDER BY id DESC LIMIT 1`, context).Scan(&b)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return b, err
}

// ListenSignalCounts counts positive/negative signals for maturity.
func (s *Store) ListenSignalCounts() (pos, neg int, err error) {
	err = s.DB.QueryRow(`
SELECT
  COALESCE(SUM(CASE
    WHEN action IN ('like','finish') THEN 1
    WHEN action = 'track_end' AND (reason IN ('completed','next') OR COALESCE(listened_sec,0) >= 0.8 * COALESCE(duration_sec,1)) THEN 1
    ELSE 0 END), 0),
  COALESCE(SUM(CASE
    WHEN action IN ('dislike','skip') THEN 1
    WHEN action = 'track_end' AND reason = 'skipped' AND COALESCE(listened_sec,0) < 0.3 * COALESCE(duration_sec,1) THEN 1
    ELSE 0 END), 0)
FROM listening_history`).Scan(&pos, &neg)
	return
}

type ArtistCount struct {
	Artist string `json:"artist"`
	Count  int    `json:"count"`
}

func (s *Store) TopArtists(limit int) ([]ArtistCount, error) {
	if limit < 1 {
		limit = 5
	}
	rows, err := s.DB.Query(`
SELECT COALESCE(t.artist,'(unknown)'), COUNT(*) AS c
FROM listening_history h
JOIN tracks t ON t.id = h.track_id
WHERE h.action IN ('like','finish','track_end')
  AND (h.reason IS NULL OR h.reason != 'skipped')
GROUP BY 1
ORDER BY c DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ArtistCount
	for rows.Next() {
		var a ArtistCount
		if err := rows.Scan(&a.Artist, &a.Count); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

type ClusterCount struct {
	ClusterID int `json:"cluster_id"`
	Count     int `json:"count"`
}

func (s *Store) TopClusters(limit int) ([]ClusterCount, error) {
	if limit < 1 {
		limit = 5
	}
	rows, err := s.DB.Query(`
SELECT COALESCE(f.cluster_id, -1), COUNT(*) AS c
FROM listening_history h
JOIN features f ON f.track_id = h.track_id
WHERE h.action IN ('like','finish','track_end')
  AND (h.reason IS NULL OR h.reason != 'skipped')
  AND f.cluster_id IS NOT NULL
GROUP BY 1
ORDER BY c DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClusterCount
	for rows.Next() {
		var c ClusterCount
		if err := rows.Scan(&c.ClusterID, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type PlaylistTrack struct {
	Position    int     `json:"position"`
	TrackID     int64   `json:"track_id"`
	Artist      string  `json:"artist"`
	Title       string  `json:"title"`
	Duration    float64 `json:"duration"`
	Explanation string  `json:"explanation"`
}

type Playlist struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	Name      string          `json:"name"`
	CreatedAt string          `json:"created_at"`
	Tracks    []PlaylistTrack `json:"tracks"`
}

func (s *Store) PlaylistMeta(kind string) (id int64, name string, n int, err error) {
	err = s.DB.QueryRow(`
SELECT p.id, p.name,
  (SELECT COUNT(*) FROM playlist_tracks pt WHERE pt.playlist_id = p.id)
FROM playlists p
WHERE p.kind = ?
ORDER BY p.id DESC LIMIT 1`, kind).Scan(&id, &name, &n)
	if err == sql.ErrNoRows {
		return 0, "", 0, nil
	}
	return
}

func (s *Store) LaterList() ([]PlaylistTrack, error) {
	rows, err := s.DB.Query(`
SELECT l.position, l.track_id, COALESCE(t.artist,''), COALESCE(t.title,''),
       COALESCE(t.duration,0), ''
FROM listen_later l
JOIN tracks t ON t.id = l.track_id
ORDER BY l.position ASC, l.added_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlaylistTrack
	for rows.Next() {
		var t PlaylistTrack
		if err := rows.Scan(&t.Position, &t.TrackID, &t.Artist, &t.Title, &t.Duration, &t.Explanation); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) LaterAdd(trackID int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var pos int
	_ = s.DB.QueryRow(`SELECT COALESCE(MAX(position),0) FROM listen_later`).Scan(&pos)
	_, err := s.DB.Exec(`
INSERT INTO listen_later(track_id, added_at, position) VALUES (?,?,?)
ON CONFLICT(track_id) DO UPDATE SET added_at=excluded.added_at`,
		trackID, now, pos+1)
	return err
}

func (s *Store) LaterRemove(trackID int64) error {
	_, err := s.DB.Exec(`DELETE FROM listen_later WHERE track_id = ?`, trackID)
	return err
}

func (s *Store) LaterCount() int {
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM listen_later`).Scan(&n)
	return n
}

func (s *Store) FavoritesList() ([]PlaylistTrack, error) {
	rows, err := s.DB.Query(`
SELECT f.position, f.track_id, COALESCE(t.artist,''), COALESCE(t.title,''),
       COALESCE(t.duration,0), ''
FROM favorites f
JOIN tracks t ON t.id = f.track_id
ORDER BY f.position ASC, f.added_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PlaylistTrack
	for rows.Next() {
		var t PlaylistTrack
		if err := rows.Scan(&t.Position, &t.TrackID, &t.Artist, &t.Title, &t.Duration, &t.Explanation); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) FavoritesAdd(trackID int64) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var pos int
	_ = s.DB.QueryRow(`SELECT COALESCE(MAX(position),0) FROM favorites`).Scan(&pos)
	_, err := s.DB.Exec(`
INSERT INTO favorites(track_id, added_at, position) VALUES (?,?,?)
ON CONFLICT(track_id) DO UPDATE SET added_at=excluded.added_at`,
		trackID, now, pos+1)
	return err
}

func (s *Store) FavoritesRemove(trackID int64) error {
	_, err := s.DB.Exec(`DELETE FROM favorites WHERE track_id = ?`, trackID)
	return err
}

func (s *Store) FavoritesCount() int {
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM favorites`).Scan(&n)
	return n
}

func (s *Store) FavoritesHas(trackID int64) bool {
	var n int
	_ = s.DB.QueryRow(`SELECT 1 FROM favorites WHERE track_id = ?`, trackID).Scan(&n)
	return n == 1
}

type FavArtist struct {
	Artist   string `json:"artist"`
	Position int    `json:"position"`
	AddedAt  string `json:"added_at"`
}

type FavAlbum struct {
	Artist   string `json:"artist"`
	Album    string `json:"album"`
	Position int    `json:"position"`
	AddedAt  string `json:"added_at"`
}

func (s *Store) FavArtistsList() ([]FavArtist, error) {
	rows, err := s.DB.Query(`SELECT artist, position, added_at FROM favorite_artists ORDER BY position ASC, added_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FavArtist
	for rows.Next() {
		var a FavArtist
		if err := rows.Scan(&a.Artist, &a.Position, &a.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) FavArtistAdd(artist string) error {
	artist = strings.TrimSpace(artist)
	if artist == "" {
		return fmt.Errorf("artist required")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var pos int
	_ = s.DB.QueryRow(`SELECT COALESCE(MAX(position),0) FROM favorite_artists`).Scan(&pos)
	_, err := s.DB.Exec(`
INSERT INTO favorite_artists(artist, added_at, position) VALUES (?,?,?)
ON CONFLICT(artist) DO UPDATE SET added_at=excluded.added_at`, artist, now, pos+1)
	return err
}

func (s *Store) FavArtistRemove(artist string) error {
	_, err := s.DB.Exec(`DELETE FROM favorite_artists WHERE artist = ?`, strings.TrimSpace(artist))
	return err
}

func (s *Store) FavArtistHas(artist string) bool {
	var n int
	_ = s.DB.QueryRow(`SELECT 1 FROM favorite_artists WHERE artist = ?`, strings.TrimSpace(artist)).Scan(&n)
	return n == 1
}

func (s *Store) FavArtistCount() int {
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM favorite_artists`).Scan(&n)
	return n
}

func (s *Store) FavAlbumsList() ([]FavAlbum, error) {
	rows, err := s.DB.Query(`SELECT artist, album, position, added_at FROM favorite_albums ORDER BY position ASC, added_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FavAlbum
	for rows.Next() {
		var a FavAlbum
		if err := rows.Scan(&a.Artist, &a.Album, &a.Position, &a.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) FavAlbumAdd(artist, album string) error {
	artist = strings.TrimSpace(artist)
	album = strings.TrimSpace(album)
	if album == "" {
		return fmt.Errorf("album required")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var pos int
	_ = s.DB.QueryRow(`SELECT COALESCE(MAX(position),0) FROM favorite_albums`).Scan(&pos)
	_, err := s.DB.Exec(`
INSERT INTO favorite_albums(artist, album, added_at, position) VALUES (?,?,?,?)
ON CONFLICT(artist, album) DO UPDATE SET added_at=excluded.added_at`,
		artist, album, now, pos+1)
	return err
}

func (s *Store) FavAlbumRemove(artist, album string) error {
	_, err := s.DB.Exec(`DELETE FROM favorite_albums WHERE artist = ? AND album = ?`,
		strings.TrimSpace(artist), strings.TrimSpace(album))
	return err
}

func (s *Store) FavAlbumHas(artist, album string) bool {
	var n int
	_ = s.DB.QueryRow(`SELECT 1 FROM favorite_albums WHERE artist = ? AND album = ?`,
		strings.TrimSpace(artist), strings.TrimSpace(album)).Scan(&n)
	return n == 1
}

func (s *Store) FavAlbumCount() int {
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM favorite_albums`).Scan(&n)
	return n
}

func (s *Store) LatestPlaylist(kind string) (*Playlist, error) {
	var pl Playlist
	err := s.DB.QueryRow(`
SELECT id, kind, name, created_at FROM playlists
WHERE kind = ? ORDER BY id DESC LIMIT 1`, kind).Scan(&pl.ID, &pl.Kind, &pl.Name, &pl.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.Query(`
SELECT pt.position, pt.track_id, COALESCE(t.artist,''), COALESCE(t.title,''),
       COALESCE(t.duration,0), COALESCE(pt.explanation,'')
FROM playlist_tracks pt
JOIN tracks t ON t.id = pt.track_id
WHERE pt.playlist_id = ?
ORDER BY pt.position`, pl.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var t PlaylistTrack
		if err := rows.Scan(&t.Position, &t.TrackID, &t.Artist, &t.Title, &t.Duration, &t.Explanation); err != nil {
			return nil, err
		}
		pl.Tracks = append(pl.Tracks, t)
	}
	return &pl, rows.Err()
}

type DiscoverTip struct {
	ID          int64   `json:"id"`
	Kind        string  `json:"kind"`
	Artist      string  `json:"artist"`
	Album       string  `json:"album"`
	Score       float64 `json:"score"`
	TrackIDs    []int64 `json:"track_ids"`
	Explanation string  `json:"explanation"`
	CreatedAt   string  `json:"created_at"`
}

func (s *Store) ListDiscoverTips(kind string, limit int) ([]DiscoverTip, error) {
	if limit < 1 {
		limit = 20
	}
	q := `
SELECT id, kind, COALESCE(artist,''), COALESCE(album,''), score,
       track_ids_json, COALESCE(explanation,''), created_at
FROM discover_tips`
	args := []any{}
	if kind != "" {
		q += ` WHERE kind = ?`
		args = append(args, kind)
	}
	q += ` ORDER BY score DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DiscoverTip
	for rows.Next() {
		var t DiscoverTip
		var idsJSON string
		if err := rows.Scan(&t.ID, &t.Kind, &t.Artist, &t.Album, &t.Score, &idsJSON, &t.Explanation, &t.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(idsJSON), &t.TrackIDs)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) EnqueueJob(kind, payloadJSON string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.DB.Exec(`
INSERT INTO jobs(kind, status, payload_json, created_at, updated_at)
VALUES (?,'pending',?,?,?)`, kind, nullStr(payloadJSON), now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

type Job struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Payload   string `json:"payload_json,omitempty"`
	Result    string `json:"result_json,omitempty"`
	Error     string `json:"error,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ListDoneJobsAfter returns jobs completed after the given RFC3339/Nano timestamp
// (exclusive). Used by the player to auto-reload after worker finishes.
func (s *Store) ListDoneJobsAfter(after string, limit int) ([]Job, error) {
	if limit < 1 {
		limit = 20
	}
	rows, err := s.DB.Query(`
SELECT id, kind, status, COALESCE(payload_json,''), COALESCE(result_json,''),
       COALESCE(error,''), created_at, updated_at
FROM jobs
WHERE status = 'done' AND updated_at > ?
ORDER BY updated_at ASC
LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.Kind, &j.Status, &j.Payload, &j.Result, &j.Error, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) ListJobs(status string, limit int) ([]Job, error) {
	if limit < 1 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if status != "" {
		rows, err = s.DB.Query(`
SELECT id, kind, status, COALESCE(payload_json,''), COALESCE(result_json,''),
       COALESCE(error,''), created_at, updated_at
FROM jobs WHERE status = ? ORDER BY id DESC LIMIT ?`, status, limit)
	} else {
		rows, err = s.DB.Query(`
SELECT id, kind, status, COALESCE(payload_json,''), COALESCE(result_json,''),
       COALESCE(error,''), created_at, updated_at
FROM jobs ORDER BY id DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.Kind, &j.Status, &j.Payload, &j.Result, &j.Error, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) GetJob(id int64) (*Job, error) {
	var j Job
	var payload, result, errStr sql.NullString
	err := s.DB.QueryRow(`
SELECT id, kind, status, payload_json, result_json, error, created_at, updated_at
FROM jobs WHERE id = ?`, id).Scan(&j.ID, &j.Kind, &j.Status, &payload, &result, &errStr, &j.CreatedAt, &j.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j.Payload = payload.String
	j.Result = result.String
	j.Error = errStr.String
	return &j, nil
}

type Metrics struct {
	Listens7d       int     `json:"listens_7d"`
	Skips7d         int     `json:"skips_7d"`
	SkipRate7d      float64 `json:"skip_rate_7d"`
	Completes7d     int     `json:"completes_7d"`
	ExploreShown    int     `json:"explore_shown_approx"`
	UniqueArtists7d int     `json:"unique_artists_7d"`
}

func (s *Store) WeeklyMetrics() (Metrics, error) {
	var m Metrics
	err := s.DB.QueryRow(`
SELECT
  COALESCE(SUM(CASE WHEN action IN ('track_end','finish','skip','like') THEN 1 ELSE 0 END),0),
  COALESCE(SUM(CASE WHEN action='skip' OR (action='track_end' AND reason='skipped') THEN 1 ELSE 0 END),0),
  COALESCE(SUM(CASE WHEN action='finish' OR (action='track_end' AND reason='completed') THEN 1 ELSE 0 END),0)
FROM listening_history
WHERE ts >= datetime('now', '-7 days')`).Scan(&m.Listens7d, &m.Skips7d, &m.Completes7d)
	if err != nil {
		return m, err
	}
	if m.Listens7d > 0 {
		m.SkipRate7d = float64(m.Skips7d) / float64(m.Listens7d)
	}
	_ = s.DB.QueryRow(`
SELECT COUNT(DISTINCT t.artist)
FROM listening_history h
JOIN tracks t ON t.id = h.track_id
WHERE h.ts >= datetime('now', '-7 days')`).Scan(&m.UniqueArtists7d)
	_ = s.DB.QueryRow(`SELECT COALESCE(SUM(shown),0) FROM rec_stats`).Scan(&m.ExploreShown)
	return m, nil
}

func dayPart(h int) string {
	switch {
	case h >= 5 && h < 12:
		return "morning"
	case h >= 12 && h < 17:
		return "afternoon"
	case h >= 17 && h < 23:
		return "evening"
	default:
		return "night"
	}
}

// DayPart is the exported name for API/queue blending.
func DayPart(h int) string { return dayPart(h) }

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

type RadioShare struct {
	Token        string  `json:"token"`
	Name         string  `json:"name"`
	CreatedAt    string  `json:"created_at"`
	RevokedAt    *string `json:"revoked_at,omitempty"`
	LastListenAt *string `json:"last_listen_at,omitempty"`
	ListenCount  int     `json:"listen_count"`
	Active       bool    `json:"active"`
}

func (s *Store) CreateRadioShare(token, name string) (RadioShare, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(`
INSERT INTO radio_shares(token, name, created_at, listen_count)
VALUES(?,?,?,0)`, token, name, now)
	if err != nil {
		return RadioShare{}, err
	}
	return RadioShare{Token: token, Name: name, CreatedAt: now, Active: true}, nil
}

func (s *Store) ListRadioShares(includeRevoked bool) ([]RadioShare, error) {
	q := `SELECT token, name, created_at, revoked_at, last_listen_at, listen_count
FROM radio_shares`
	if !includeRevoked {
		q += ` WHERE revoked_at IS NULL`
	}
	q += ` ORDER BY created_at DESC`
	rows, err := s.DB.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RadioShare
	for rows.Next() {
		var sh RadioShare
		var revoked, last sql.NullString
		if err := rows.Scan(&sh.Token, &sh.Name, &sh.CreatedAt, &revoked, &last, &sh.ListenCount); err != nil {
			return nil, err
		}
		if revoked.Valid {
			sh.RevokedAt = &revoked.String
		}
		if last.Valid {
			sh.LastListenAt = &last.String
		}
		sh.Active = !revoked.Valid
		out = append(out, sh)
	}
	return out, rows.Err()
}

func (s *Store) GetActiveRadioShare(token string) (RadioShare, bool, error) {
	var sh RadioShare
	var revoked, last sql.NullString
	err := s.DB.QueryRow(`
SELECT token, name, created_at, revoked_at, last_listen_at, listen_count
FROM radio_shares WHERE token = ?`, token).Scan(
		&sh.Token, &sh.Name, &sh.CreatedAt, &revoked, &last, &sh.ListenCount)
	if err == sql.ErrNoRows {
		return RadioShare{}, false, nil
	}
	if err != nil {
		return RadioShare{}, false, err
	}
	if revoked.Valid {
		sh.RevokedAt = &revoked.String
		return sh, false, nil
	}
	if last.Valid {
		sh.LastListenAt = &last.String
	}
	sh.Active = true
	return sh, true, nil
}

func (s *Store) RevokeRadioShare(token string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	res, err := s.DB.Exec(`UPDATE radio_shares SET revoked_at = ? WHERE token = ? AND revoked_at IS NULL`, now, token)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("not found")
	}
	return nil
}

func (s *Store) TouchRadioShareListen(token string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.Exec(`
UPDATE radio_shares SET listen_count = listen_count + 1, last_listen_at = ?
WHERE token = ? AND revoked_at IS NULL`, now, token)
	return err
}

// TransitionEdge is one directed listen transition.
type TransitionEdge struct {
	ToID   int64
	Weight float64
}

// LoadTransitionGraph loads from→to weights (min weight 1).
func (s *Store) LoadTransitionGraph() (map[int64]map[int64]float64, error) {
	rows, err := s.DB.Query(`
SELECT from_id, to_id, weight FROM transitions WHERE weight >= 1
ORDER BY weight DESC LIMIT 50000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]map[int64]float64{}
	for rows.Next() {
		var from, to int64
		var w float64
		if err := rows.Scan(&from, &to, &w); err != nil {
			return nil, err
		}
		m := out[from]
		if m == nil {
			m = map[int64]float64{}
			out[from] = m
		}
		m[to] = w
	}
	return out, rows.Err()
}

type PlaySessionRow struct {
	ID           string
	Mode         string
	CurrentID    int64
	QueueJSON    string
	ExcludeJSON  string
	RatedJSON    string
	DailyIDsJSON string
	DailyPos     int
	PlaylistName string
	PlaylistKind string
	UpdatedAt    string
}

func (s *Store) UpsertPlaySession(row PlaySessionRow) error {
	_, err := s.DB.Exec(`
INSERT INTO play_sessions(
  id, mode, current_id, queue_json, exclude_json, rated_json,
  daily_ids_json, daily_pos, playlist_name, playlist_kind, updated_at
) VALUES (?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  mode=excluded.mode,
  current_id=excluded.current_id,
  queue_json=excluded.queue_json,
  exclude_json=excluded.exclude_json,
  rated_json=excluded.rated_json,
  daily_ids_json=excluded.daily_ids_json,
  daily_pos=excluded.daily_pos,
  playlist_name=excluded.playlist_name,
  playlist_kind=excluded.playlist_kind,
  updated_at=excluded.updated_at`,
		row.ID, row.Mode, row.CurrentID, nullStr(row.QueueJSON), nullStr(row.ExcludeJSON),
		nullStr(row.RatedJSON), nullStr(row.DailyIDsJSON), row.DailyPos,
		row.PlaylistName, row.PlaylistKind, row.UpdatedAt)
	return err
}

func (s *Store) LoadPlaySession(id string) (PlaySessionRow, bool, error) {
	var row PlaySessionRow
	var q, ex, rated, daily sql.NullString
	err := s.DB.QueryRow(`
SELECT id, mode, current_id, queue_json, exclude_json, rated_json,
       daily_ids_json, daily_pos, playlist_name, playlist_kind, updated_at
FROM play_sessions WHERE id = ?`, id).Scan(
		&row.ID, &row.Mode, &row.CurrentID, &q, &ex, &rated, &daily,
		&row.DailyPos, &row.PlaylistName, &row.PlaylistKind, &row.UpdatedAt)
	if err == sql.ErrNoRows {
		return PlaySessionRow{}, false, nil
	}
	if err != nil {
		return PlaySessionRow{}, false, err
	}
	if q.Valid {
		row.QueueJSON = q.String
	}
	if ex.Valid {
		row.ExcludeJSON = ex.String
	}
	if rated.Valid {
		row.RatedJSON = rated.String
	}
	if daily.Valid {
		row.DailyIDsJSON = daily.String
	}
	return row, true, nil
}

func (s *Store) DeletePlaySession(id string) error {
	_, err := s.DB.Exec(`DELETE FROM play_sessions WHERE id = ?`, id)
	return err
}

func (s *Store) DeleteStalePlaySessions(olderThan time.Time) error {
	_, err := s.DB.Exec(`DELETE FROM play_sessions WHERE updated_at < ?`, olderThan.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) CountPlaySessions() (int, error) {
	var n int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM play_sessions`).Scan(&n)
	return n, err
}

func (s *Store) OldestPlaySessionIDs(limit int) ([]string, error) {
	rows, err := s.DB.Query(`SELECT id FROM play_sessions ORDER BY updated_at ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Lyrics is plain/synced text for a track (filled by Python `musik lyrics`).
type Lyrics struct {
	TrackID      int64  `json:"track_id"`
	PlainLyrics  string `json:"plain_lyrics"`
	SyncedLyrics string `json:"synced_lyrics"`
	Source       string `json:"source"`
	SourceID     string `json:"source_id"`
	Instrumental bool   `json:"instrumental"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
	UpdatedAt    string `json:"updated_at"`
}

func (s *Store) GetLyrics(trackID int64) (*Lyrics, bool, error) {
	row := s.DB.QueryRow(`
SELECT track_id, plain_lyrics, synced_lyrics, source, source_id,
       instrumental, status, COALESCE(error,''), updated_at
FROM lyrics WHERE track_id = ?`, trackID)
	var ly Lyrics
	var instr int
	err := row.Scan(
		&ly.TrackID, &ly.PlainLyrics, &ly.SyncedLyrics, &ly.Source, &ly.SourceID,
		&instr, &ly.Status, &ly.Error, &ly.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	ly.Instrumental = instr != 0
	return &ly, true, nil
}
