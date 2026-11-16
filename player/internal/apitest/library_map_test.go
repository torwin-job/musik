package apitest

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestLibraryMapBeforeFirstBuild(t *testing.T) {
	server := openTestServer(t)
	rec := serve(server, jsonReq("GET", "/api/library/map", ""))
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Ready  bool            `json:"ready"`
		Total  int             `json:"total"`
		Points [][4]float64    `json:"points"`
		Labels []any           `json:"labels"`
		Genres json.RawMessage `json:"genres"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Ready || out.Total != 3 || len(out.Points) != 0 || len(out.Labels) != 0 || string(out.Genres) != "[]" {
		t.Fatalf("empty map = %s", rec.Body.String())
	}
}

func TestLibraryMapHidesOtherProfilesPrivateTracks(t *testing.T) {
	server := openTestServer(t)
	dir := t.TempDir()
	for id, path := range map[int64]string{
		44: filepath.Join(dir, "_private", "someone-else", "x.mp3"),
		55: filepath.Join(dir, "_private", server.Store.ProfileID, "y.mp3"),
	} {
		if _, err := server.Store.DB.Exec(
			`INSERT INTO tracks(id, path, title, created_at, updated_at) VALUES (?, ?, 'T', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			id, path,
		); err != nil {
			t.Fatal(err)
		}
	}
	doc := `{"version":1,"method":"umap","updated_at":"2026-10-27T00:00:00Z","genres":["Rock"],` +
		`"points":[[11,0.1,0.2,0,44,55,22],[22,0.3,0.4,-1,11,-1,-1],[44,0.5,0.5,0,11,22,55],[55,0.9,0.8,-1,44,11,22]],` +
		`"labels":[{"id":"chill","name":"Чилл","color":"#5fb3a8","x":0.2,"y":0.3,"track_ids":[44,11,55]},` +
		`{"id":"sad","name":"Грустное","color":"#5a9fd8","x":0.5,"y":0.5,"track_ids":[44]}]}`
	if _, err := server.Store.DB.Exec(`INSERT INTO scan_state(key, value) VALUES ('library_map', ?)`, doc); err != nil {
		t.Fatal(err)
	}

	for _, step := range [][4]any{
		{11, 22, 3, server.Store.ProfileID},
		{22, 55, 1, server.Store.ProfileID},
		{11, 44, 5, server.Store.ProfileID}, // hidden track
	} {
		if _, err := server.Store.DB.Exec(
			`INSERT INTO transition_stats(from_id, to_id, finished_count, updated_at, profile_id) VALUES (?, ?, ?, '2026-10-27T00:00:00Z', ?)`,
			step[0], step[1], step[2], step[3],
		); err != nil {
			t.Fatal(err)
		}
	}

	rec := serve(server, jsonReq("GET", "/api/library/map", ""))
	if rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Ready       bool         `json:"ready"`
		Method      string       `json:"method"`
		Total       int          `json:"total"`
		Points      [][]float64  `json:"points"`
		Transitions [][3]float64 `json:"transitions"`
		Labels      []struct {
			ID       string  `json:"id"`
			TrackIDs []int64 `json:"track_ids"`
		} `json:"labels"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Ready || out.Method != "umap" || out.Total != 4 {
		t.Fatalf("map header = %s", rec.Body.String())
	}
	var ids []float64
	for _, p := range out.Points {
		ids = append(ids, p[0])
	}
	if len(ids) != 3 || ids[0] != 11 || ids[1] != 22 || ids[2] != 55 {
		t.Fatalf("points = %v, want 11, 22 and own private 55", ids)
	}
	if got := out.Points[0][4:]; got[0] != -1 || got[1] != 55 || got[2] != 22 {
		t.Fatalf("neighbours of 11 = %v, want hidden 44 blanked", got)
	}
	want := [][3]float64{{11, 22, 3}, {22, 55, 1}}
	if len(out.Transitions) != 2 || out.Transitions[0] != want[0] || out.Transitions[1] != want[1] {
		t.Fatalf("transitions = %v, want own visible steps %v", out.Transitions, want)
	}
	// A label left with only hidden tracks disappears; others lose them.
	if len(out.Labels) != 1 || out.Labels[0].ID != "chill" {
		t.Fatalf("labels = %+v", out.Labels)
	}
	if got := out.Labels[0].TrackIDs; len(got) != 2 || got[0] != 11 || got[1] != 55 {
		t.Fatalf("chill tracks = %v", got)
	}
}
