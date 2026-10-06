package index

import (
	"reflect"
	"testing"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
)

func TestTrackArtistNames(t *testing.T) {
	cases := []struct {
		artist   string
		segments []string
		want     []string
	}{
		// Segments come from the scanner; the read path only trims them.
		{"Thomas/БИ-2/Сплин", []string{"Thomas", "БИ-2", "Сплин"}, []string{"Thomas", "БИ-2", "Сплин"}},
		{"Solo", []string{"Solo", "Solo"}, []string{"Solo"}},
		// No stored segments: the raw label is used untouched — "&"/"и" are
		// never treated as separators on the read path.
		{"Simon & Garfunkel", nil, []string{"Simon & Garfunkel"}},
		{"Король и Шут", nil, []string{"Король и Шут"}},
		{"", nil, []string{}},
	}
	for _, c := range cases {
		got := trackArtistNames(c.artist, c.segments)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("trackArtistNames(%q,%v)=%v, want %v", c.artist, c.segments, got, c.want)
		}
	}
}

func TestLoadIndexesCollaboratorSegments(t *testing.T) {
	idx := New(config.Config{})
	rows := []db.TrackRow{
		{ID: 1, Artist: "Linkin Park", Album: "Meteora", Embedding: Float32Bytes([]float32{1, 0}), Dim: 2},
		{ID: 2, Artist: "Thomas/БИ-2/Сплин", ArtistSegments: []string{"Thomas", "БИ-2", "Сплин"}, Album: "Fellini 2001 Tour", Embedding: Float32Bytes([]float32{0.8, 0.2}), Dim: 2},
		{ID: 3, Artist: "Король и Шут", ArtistSegments: []string{"Король и Шут"}, Album: "Ангел-Демон", Embedding: Float32Bytes([]float32{0, 1}), Dim: 2},
		{ID: 4, Artist: "БИ-2", Album: "Город золота", Embedding: Float32Bytes([]float32{0.5, 0.5}), Dim: 2},
		{ID: 5, Artist: "Сплин", Album: "Гранатовый альбом", Embedding: Float32Bytes([]float32{-0.5, 0.5}), Dim: 2},
	}
	if err := idx.Load(rows); err != nil {
		t.Fatal(err)
	}
	if got := idx.RowsForArtist("Сплин"); len(got) != 2 {
		t.Fatalf("Сплин rows=%v, want solo record plus the collaboration", got)
	}
	if got := idx.RowsForArtist("БИ-2"); len(got) != 2 {
		t.Fatalf("БИ-2 rows=%v, want solo record plus the collaboration", got)
	}
	if got := idx.RowsForArtist("Король"); len(got) != 0 {
		t.Fatalf("Король rows=%v, want none (band name must not split)", got)
	}
	if got := idx.RowsForArtist("Король и Шут"); len(got) != 1 {
		t.Fatalf("Король и Шут rows=%v, want the whole band", got)
	}
	if got := idx.RowsForAlbum("Сплин", "Fellini 2001 Tour"); len(got) != 1 {
		t.Fatalf("album on artist page=%v, want the collaboration", got)
	}
}
