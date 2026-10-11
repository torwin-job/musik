package db

import (
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

// Store is one profile's view of the configured database. ProfileID scopes
// every personal-data query; catalog data is shared.
type Store struct {
	DB        *DB
	ProfileID string
	Dialect   Dialect
	ReadOnly  bool
}

const SupportedSchemaVersion = 14
const SupportedSchemaRevision = "musik_14"

func mondayZeroWeekday(day time.Weekday) int { return (int(day) + 6) % 7 }

type TrackRow struct {
	ID     int64
	Path   string
	Title  string
	Artist string
	// ArtistSegments is the collaborator split parsed at scan time (row.artists).
	ArtistSegments []string
	// AlbumArtist is the album-level credit ("Various Artists" for a flagged
	// compilation); empty when the tags carry neither.
	AlbumArtist string
	Album       string
	Duration    float64
	FileMD5     string
	CreatedAt   string
	ArtworkPath string
	ClusterID   int
	Embedding   []byte
	Dim         int
	// EmbeddingStamp is features.computed_at: it changes whenever the
	// embedding is rewritten, so a reload can keep unchanged vectors.
	EmbeddingStamp string
	Shown          int
	SkipEarly      int
	Completed      int
	Year           int
	BPM            float64
	LUFS           float64
	HasBPM         bool
	HasLUFS        bool
	KeyName        string
	Plays          int
	Finishes       int
	EarlySkips     int
	LastPlayedAt   string
}

// OpenDatabase opens the configured backend and validates that `musik db
// migrate` has brought it to the supported schema; the player never migrates.
func OpenDatabase(databaseURL, sqlitePath string) (*Store, error) {
	dialect, sqlDB, err := openPool(databaseURL, sqlitePath)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(8)
	sqlDB.SetMaxIdleConns(4)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	s, err := newStore(dialect, sqlDB)
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := s.validateSchema(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return s, nil
}

func openPool(databaseURL, sqlitePath string) (Dialect, *sql.DB, error) {
	if databaseURL == "" {
		absolute, err := filepath.Abs(sqlitePath)
		if err != nil {
			return "", nil, err
		}
		// _txlock=immediate: write transactions take the write lock at BEGIN,
		// so read-then-write repository methods cannot interleave.
		dsn := sqliteFileURL(filepath.ToSlash(absolute)) +
			"?_pragma=foreign_keys(1)&_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)&_txlock=immediate"
		pool, err := sql.Open("sqlite", dsn)
		return SQLite, pool, err
	}
	u, err := url.Parse(databaseURL)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" || strings.Trim(u.Path, "/") == "" {
		return "", nil, fmt.Errorf("invalid PostgreSQL MUSIK_DATABASE_URL")
	}
	pool, err := sql.Open("pgx", databaseURL)
	return Postgres, pool, err
}

// sqliteFileURL turns an absolute slash path into a file: URI. A Windows path
// ("C:/data/musik.db") needs a leading slash, or "C:" becomes the URI authority
// and SQLite refuses it: file:///C:/data/musik.db, like file:///data/musik.db.
func sqliteFileURL(slashPath string) string {
	if !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	return (&url.URL{Scheme: "file", Path: slashPath}).String()
}

func newStore(dialect Dialect, pool *sql.DB) (*Store, error) {
	driver := "sqlite3" // sqlx bind type: '?' placeholders
	if dialect == Postgres {
		driver = "pgx"
	}
	return &Store{
		DB:      &DB{x: sqlx.NewDb(pool, driver), Dialect: dialect},
		Dialect: dialect,
	}, nil
}

func (s *Store) validateSchema() error {
	if s.Dialect == Postgres {
		if err := s.DB.QueryRow(`SELECT pg_is_in_recovery()`).Scan(&s.ReadOnly); err != nil {
			return fmt.Errorf("read PostgreSQL recovery state: %w", err)
		}
	} else {
		var version int
		if err := s.DB.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
			return fmt.Errorf("read SQLite schema version: %w", err)
		}
		if version != SupportedSchemaVersion {
			return fmt.Errorf("unsupported SQLite database schema version %d (player requires exactly %d); run `musik db migrate` before starting the player", version, SupportedSchemaVersion)
		}
		_, _ = s.DB.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	}
	var revision string
	if err := s.DB.QueryRow(`SELECT version_num FROM alembic_version`).Scan(&revision); err != nil {
		return fmt.Errorf("database migration history is missing; run `musik db migrate` first: %w", err)
	}
	if revision != SupportedSchemaRevision {
		return fmt.Errorf("unsupported database migration revision %q (player requires %q)", revision, SupportedSchemaRevision)
	}
	if err := s.DB.QueryRow(`SELECT value FROM installation_state WHERE key='legacy_profile_id'`).Scan(&s.ProfileID); err != nil {
		return fmt.Errorf("read installation profile: %w", err)
	}
	return nil
}

func (s *Store) Close() error { return s.DB.Close() }

// ForProfile returns a view of the same pool scoped to profileID.
func (s *Store) ForProfile(profileID string) *Store {
	scoped := *s
	scoped.ProfileID = profileID
	return &scoped
}
