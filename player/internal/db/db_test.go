package db

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "musik-test.db")
	bootstrap, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	schema := []string{
		`CREATE TABLE tracks (
			id INTEGER PRIMARY KEY, path TEXT NOT NULL DEFAULT '',
			title TEXT, artist TEXT, album TEXT, duration REAL
		)`,
		`CREATE TABLE listening_history (
			id INTEGER PRIMARY KEY AUTOINCREMENT, track_id INTEGER NOT NULL,
			ts TEXT NOT NULL, source TEXT, action TEXT NOT NULL,
			daypart TEXT, weekday INTEGER, position_sec REAL, duration_sec REAL
		)`,
		`CREATE TABLE playlists (
			id INTEGER PRIMARY KEY, kind TEXT NOT NULL, name TEXT NOT NULL, created_at TEXT NOT NULL
		)`,
		`CREATE TABLE playlist_tracks (
			playlist_id INTEGER NOT NULL, position INTEGER NOT NULL,
			track_id INTEGER NOT NULL, explanation TEXT
		)`,
	}
	for _, statement := range schema {
		if _, err := bootstrap.Exec(statement); err != nil {
			_ = bootstrap.Close()
			t.Fatalf("create test schema: %v", err)
		}
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func TestMondayZeroWeekday(t *testing.T) {
	tests := []struct {
		day  time.Weekday
		want int
	}{
		{time.Monday, 0},
		{time.Tuesday, 1},
		{time.Saturday, 5},
		{time.Sunday, 6},
	}
	for _, tt := range tests {
		if got := mondayZeroWeekday(tt.day); got != tt.want {
			t.Fatalf("mondayZeroWeekday(%s) = %d, want %d", tt.day, got, tt.want)
		}
	}
}

func TestLatestPlaylistSelectsNewestAndPreservesPositions(t *testing.T) {
	store, _ := openTestStore(t)
	for id := int64(1); id <= 3; id++ {
		if _, err := store.DB.Exec(
			`INSERT INTO tracks(id, path, title, artist, duration) VALUES (?, ?, ?, ?, ?)`,
			id, fmt.Sprintf("/track-%d.flac", id), fmt.Sprintf("Track %d", id), "Artist", 180,
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.DB.Exec(`
		INSERT INTO playlists(id, kind, name, created_at) VALUES
			(10, 'daily', 'Old', '2026-08-14T00:00:00Z'),
			(11, 'daily', 'New', '2026-08-15T00:00:00Z'),
			(12, 'weekly', 'Other kind', '2026-08-16T00:00:00Z');
		INSERT INTO playlist_tracks(playlist_id, position, track_id, explanation) VALUES
			(10, 0, 1, 'old'),
			(11, 7, 2, 'second'),
			(11, 3, 3, 'first')`); err != nil {
		t.Fatal(err)
	}

	playlist, err := store.LatestPlaylist("daily")
	if err != nil {
		t.Fatal(err)
	}
	if playlist == nil || playlist.ID != 11 || playlist.Name != "New" {
		t.Fatalf("got playlist %#v, want newest daily playlist 11", playlist)
	}
	if len(playlist.Tracks) != 2 {
		t.Fatalf("got %d tracks, want 2", len(playlist.Tracks))
	}
	if got := []int{playlist.Tracks[0].Position, playlist.Tracks[1].Position}; got[0] != 3 || got[1] != 7 {
		t.Fatalf("positions = %v, want [3 7]", got)
	}
	if playlist.Tracks[0].TrackID != 3 || playlist.Tracks[1].TrackID != 2 {
		t.Fatalf("track order = [%d %d], want [3 2]", playlist.Tracks[0].TrackID, playlist.Tracks[1].TrackID)
	}
}

func TestConcurrentWorkerAndEventWrites(t *testing.T) {
	store, path := openTestStore(t)
	if _, err := store.DB.Exec(`INSERT INTO tracks(id, path, title) VALUES (1, '/track.flac', 'Track')`); err != nil {
		t.Fatal(err)
	}
	jobID, err := store.EnqueueJob("mix_pack", `{}`)
	if err != nil {
		t.Fatal(err)
	}

	worker, err := sql.Open("sqlite",
		"file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	worker.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = worker.Close() })

	const writes = 40
	errs := make(chan error, writes*3)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < writes; i++ {
			_, err := worker.Exec(
				`UPDATE jobs SET status = ?, updated_at = ? WHERE id = ?`,
				"running", time.Now().UTC().Format(time.RFC3339Nano), jobID,
			)
			if err != nil {
				errs <- fmt.Errorf("worker write %d: %w", i, err)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < writes; i++ {
			if _, err := store.InsertListen(1, "progress", "test", "session", "",
				nil, nil, nil); err != nil {
				errs <- fmt.Errorf("history write %d: %w", i, err)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < writes; i++ {
			if err := store.BumpRecStats(1, 1, 0, 0); err != nil {
				errs <- fmt.Errorf("rec stats write %d: %w", i, err)
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	var histories, shown int
	if err := store.DB.QueryRow(`SELECT COUNT(*) FROM listening_history`).Scan(&histories); err != nil {
		t.Fatal(err)
	}
	if err := store.DB.QueryRow(`SELECT shown FROM rec_stats WHERE track_id = 1`).Scan(&shown); err != nil {
		t.Fatal(err)
	}
	if histories != writes || shown != writes {
		t.Fatalf("history=%d shown=%d, want %d each", histories, shown, writes)
	}
}
