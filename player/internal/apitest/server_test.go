package apitest

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/torwin-job/musik/player/internal/api"
	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/media"
	"github.com/torwin-job/musik/player/internal/taste"
	"github.com/torwin-job/musik/player/internal/testdb"
)

func openTestServer(t *testing.T) *api.Server {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "db", "musik-api-test.db")
	if err := testdb.Create(path); err != nil {
		t.Fatal(err)
	}
	store, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	cfg := config.Config{
		Root: dir, DBPath: path, QueueSize: 6, AuthDisabled: true,
		ProfileFormingAt: 3, ProfileReadyAt: 8, ExploreRatio: 0.15,
		DiscoverExploreRatio: 0.35, WorkerURL: "http://127.0.0.1:1",
		WorkerAutostart: false,
	}
	idx := index.New(cfg)
	rows := make([]db.TrackRow, 0, 3)
	for _, id := range []int64{11, 22, 33} {
		rows = append(rows, db.TrackRow{
			ID: id, Path: filepath.Join(dir, "missing.flac"),
			Title: "Track", Artist: "Artist", Album: "Album", Duration: 180,
			Embedding: index.Float32Bytes([]float32{1, float32(id)}), Dim: 2,
		})
		if _, err := store.DB.Exec(
			`INSERT INTO tracks(id, path, title, artist, album, duration) VALUES (?,?,?,?,?,?)`,
			id, filepath.Join(dir, "missing.flac"), "Track", "Artist", "Album", 180,
		); err != nil {
			t.Fatal(err)
		}
	}
	if err := idx.Load(rows); err != nil {
		t.Fatal(err)
	}
	server := api.New(cfg, store, idx, taste.New(), nil)
	server.Play.Warm = nil
	return server
}

func serve(server *api.Server, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	return rec
}

func jsonReq(method, path, body string) *http.Request {
	var rdr *bytes.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func flush(server *api.Server) {
	if server.Play != nil && server.Play.Flush != nil {
		server.Play.Flush()
	}
}

func loadIndex(t *testing.T, server *api.Server, rows []db.TrackRow) {
	t.Helper()
	idx := index.New(server.Cfg)
	if err := idx.Load(rows); err != nil {
		t.Fatal(err)
	}
	server.Idx = idx
	server.Media = media.New(server.Cfg, idx)
	if server.Play != nil {
		server.Play.Idx = idx
		server.Play.Warm = server.Media.Warm
	}
	if server.Builder != nil {
		server.Builder.Idx = idx
	}
}
