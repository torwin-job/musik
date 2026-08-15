package api

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
)

func TestLibraryFiltersByArtistAndAlbum(t *testing.T) {
	server := openTestServer(t)
	idx := index.New(server.Cfg)
	rows := []db.TrackRow{
		{ID: 1, Title: "One", Artist: "Massive Attack", Album: "Mezzanine", Embedding: index.Float32Bytes([]float32{1, 0}), Dim: 2},
		{ID: 2, Title: "Two", Artist: "Massive Attack", Album: "Protection", Embedding: index.Float32Bytes([]float32{0, 1}), Dim: 2},
		{ID: 3, Title: "Three", Artist: "Portishead", Album: "Dummy", Embedding: index.Float32Bytes([]float32{1, 1}), Dim: 2},
	}
	if err := idx.Load(rows); err != nil {
		t.Fatal(err)
	}
	server.Idx = idx

	tests := []struct {
		name  string
		query url.Values
		want  []int64
	}{
		{name: "all", want: []int64{1, 2, 3}},
		{name: "artist case insensitive", query: url.Values{"artist": {" massive attack "}}, want: []int64{1, 2}},
		{name: "artist and album", query: url.Values{"artist": {"Massive Attack"}, "album": {"mezzanine"}}, want: []int64{1}},
		{name: "unknown artist", query: url.Values{"artist": {"Unknown"}}, want: []int64{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/library?"+test.query.Encode(), nil)
			rec := httptest.NewRecorder()
			server.handleLibrary(rec, req)
			if rec.Code != 200 {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			var rows []struct {
				ID int64 `json:"id"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			got := make([]int64, 0, len(rows))
			for _, row := range rows {
				got = append(got, row.ID)
			}
			if len(got) != len(test.want) {
				t.Fatalf("ids = %v, want %v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("ids = %v, want %v", got, test.want)
				}
			}
		})
	}
}
