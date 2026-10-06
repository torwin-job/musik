package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB *sql.DB
}

const SupportedSchemaVersion = 9

func mondayZeroWeekday(day time.Weekday) int {
	return (int(day) + 6) % 7
}

type TrackRow struct {
	ID          int64
	Path        string
	Title       string
	Artist      string
	// ArtistSegments is the collaborator split parsed at scan time (row.artists).
	ArtistSegments []string
	Album       string
	Duration    float64
	FileMD5     string
	CreatedAt   string
	ArtworkPath  string
	ClusterID    int
	Embedding    []byte
	Dim          int
	Shown        int
	SkipEarly    int
	Completed    int
	Year         int
	BPM          float64
	LUFS         float64
	HasBPM       bool
	HasLUFS      bool
	KeyName      string
	Plays        int
	Finishes     int
	EarlySkips   int
	LastPlayedAt string
}

func Open(path string) (*Store, error) {
	// _txlock=immediate: transactions take the write lock on BEGIN. A deferred
	// transaction that reads first and then writes cannot be upgraded once another
	// writer has committed in WAL mode — SQLite fails it at once (SQLITE_BUSY /
	// BUSY_SNAPSHOT) instead of waiting for busy_timeout.
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)&_txlock=immediate", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// WAL allows concurrent readers. One connection serialized every catalog
	// request behind reloads and mix queries, so the home shelves stayed empty.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	s := &Store{DB: db}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("read schema version: %w", err)
	}
	if version != SupportedSchemaVersion {
		_ = db.Close()
		return nil, fmt.Errorf(
			"unsupported database schema version %d (player requires exactly %d); "+
				"run `musik db migrate` with the Python worker before starting the player",
			version, SupportedSchemaVersion,
		)
	}
	// Truncate WAL so it does not grow unbounded across restarts.
	_, _ = db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return s, nil
}

func (s *Store) Close() error { return s.DB.Close() }
