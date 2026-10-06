package library

import (
	"strings"
	"testing"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
)

func testIndex(t *testing.T, rows []db.TrackRow) *index.Index {
	t.Helper()
	idx := index.New(config.Config{})
	if err := idx.Load(rows); err != nil {
		t.Fatal(err)
	}
	return idx
}

func vec(id int64) []byte {
	return index.Float32Bytes([]float32{1, float32(id)})
}

func artistTracks(groups []ArtistGroup, name string) int {
	for _, g := range groups {
		if strings.EqualFold(g.Artist, name) {
			return g.Tracks
		}
	}
	return 0
}

func TestMatchArtistAlbum(t *testing.T) {
	if !MatchArtistAlbum("Massive Attack", nil, "Mezzanine", " massive attack ", "") {
		t.Fatal("artist match should be case-insensitive")
	}
	if MatchArtistAlbum("Massive Attack", nil, "Protection", "Massive Attack", "mezzanine") {
		t.Fatal("album mismatch should fail")
	}
	if !MatchArtistAlbum("Portishead", nil, "Dummy", "", "") {
		t.Fatal("empty filters should match")
	}
	// A stored collaborator segment matches its performer.
	if !MatchArtistAlbum("Thomas/БИ-2/Сплин", []string{"Thomas", "БИ-2", "Сплин"}, "Fellini 2001 Tour", "Сплин", "") {
		t.Fatal("collaboration should match a stored segment")
	}
	// A band name is stored whole and must not match a separator part.
	if MatchArtistAlbum("Король и Шут", []string{"Король и Шут"}, "Ангел-Демон", "Король", "") {
		t.Fatal("band name should not match on a part")
	}
	if !MatchArtistAlbum("Король и Шут", []string{"Король и Шут"}, "Ангел-Демон", "Король и Шут", "") {
		t.Fatal("band name should match as a whole")
	}
}

func TestGroupArtistsAndAlbums(t *testing.T) {
	idx := testIndex(t, []db.TrackRow{
		{ID: 1, Title: "One", Artist: "Massive Attack", Album: "Mezzanine", Embedding: vec(1), Dim: 2, ArtworkPath: "/a.png"},
		{ID: 2, Title: "Two", Artist: "Massive Attack", Album: "Protection", Embedding: vec(2), Dim: 2},
		{ID: 3, Title: "Three", Artist: "Portishead", Album: "Dummy", Embedding: vec(3), Dim: 2},
		{ID: 4, Title: "Four", Artist: "", Album: "", Embedding: vec(4), Dim: 2},
		{ID: 5, Title: "Five", Artist: "Linkin Park", Album: "Meteora", Embedding: vec(5), Dim: 2},
		{ID: 6, Title: "Six", Artist: "Linkin Park & Jay-Z", ArtistSegments: []string{"Linkin Park & Jay-Z"}, Album: "Collision Course", Embedding: vec(6), Dim: 2},
		{ID: 7, Title: "Seven", Artist: "Thomas/БИ-2/Сплин", ArtistSegments: []string{"Thomas", "БИ-2", "Сплин"}, Album: "Fellini 2001 Tour", Embedding: vec(7), Dim: 2},
		{ID: 8, Title: "Eight", Artist: "Король и Шут", ArtistSegments: []string{"Король и Шут"}, Album: "Ангел-Демон", Embedding: vec(8), Dim: 2},
		{ID: 9, Title: "Nine", Artist: "БИ-2", Album: "Город золота", Embedding: vec(9), Dim: 2},
		{ID: 10, Title: "Ten", Artist: "Сплин", Album: "Гранатовый альбом", Embedding: vec(10), Dim: 2},
	})

	artists := GroupArtists(idx)
	// Massive Attack, Portishead, Unknown, Linkin Park, "Linkin Park & Jay-Z",
	// Thomas, БИ-2, Сплин and Король и Шут — every stored segment, and amp/band
	// names kept whole.
	want := []string{
		"Massive Attack", "Portishead", "Unknown", "Linkin Park", "Linkin Park & Jay-Z",
		"Thomas", "БИ-2", "Сплин", "Король и Шут",
	}
	if len(artists) != len(want) {
		t.Fatalf("artists=%d, want %d: %+v", len(artists), len(want), artists)
	}
	for _, name := range want {
		if artistTracks(artists, name) == 0 {
			t.Fatalf("missing artist group %q in %+v", name, artists)
		}
	}
	if n := artistTracks(artists, "Massive Attack"); n != 2 {
		t.Fatalf("Massive Attack tracks=%d, want 2", n)
	}
	if n := artistTracks(artists, "Linkin Park"); n != 1 {
		t.Fatalf("Linkin Park tracks=%d, want 1 (the & credit is not split)", n)
	}
	if artistTracks(artists, "Jay-Z") != 0 {
		t.Fatalf("Jay-Z should not appear: & is not a separator")
	}
	if n := artistTracks(artists, "БИ-2"); n != 2 || artistTracks(artists, "Сплин") != 2 {
		t.Fatalf("Би-2/Сплин groups wrong: %+v", artists)
	}
	if n := artistTracks(artists, "Король"); n != 0 {
		t.Fatalf("Король group=%d, want 0 (band name must stay whole)", n)
	}

	albums := GroupAlbums(idx)
	if len(albums) != 9 {
		t.Fatalf("albums=%d, want 9: %+v", len(albums), albums)
	}
	byAlbum := map[string]AlbumGroup{}
	for _, al := range albums {
		byAlbum[al.Album] = al
	}
	if g := byAlbum["Collision Course"]; g.Artist != "Linkin Park & Jay-Z" || g.Tracks != 1 {
		t.Fatalf("Collision Course grouped as %+v", g)
	}
	if g := byAlbum["Fellini 2001 Tour"]; g.Artist != "Thomas" || g.Tracks != 1 {
		t.Fatalf("Fellini 2001 Tour grouped as %+v", g)
	}
	if g := byAlbum["Ангел-Демон"]; g.Artist != "Король и Шут" {
		t.Fatalf("Ангел-Демон grouped as %+v", g)
	}
	covered := false
	for _, g := range artists {
		if g.HasArtwork && strings.EqualFold(g.Artist, "Massive Attack") {
			covered = true
		}
	}
	if !covered {
		t.Fatalf("Massive Attack group should carry the cover: %+v", artists)
	}
}

func TestGameSoundtrackAlbums(t *testing.T) {
	idx := testIndex(t, []db.TrackRow{
		{ID: 1, Title: "Theme", Artist: "Composer A", Album: "Space Game", Path: "/music/Steam OST/Space Game/01.mp3", Embedding: vec(1), Dim: 2},
		{ID: 2, Title: "Song", Artist: "Band B", Album: "Studio Album", Path: "/music/Band B/Studio Album/01.mp3", Embedding: vec(2), Dim: 2},
		{ID: 3, Title: "Battle", Artist: "Composer C", Album: "Fantasy Game", Path: "/music/gog ost/Fantasy Game/01.flac", Embedding: vec(3), Dim: 2},
	})
	games := map[string]bool{}
	for _, al := range GroupAlbums(idx) {
		games[al.Album] = al.Game
	}
	if !games["Space Game"] || !games["Fantasy Game"] || games["Studio Album"] {
		t.Fatalf("game flags = %v", games)
	}
	if IsGameSoundtrackPath("/music/Steam OSTs/x.mp3") {
		t.Fatal("only whole folder names count")
	}
}
