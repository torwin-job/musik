package library

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/torwin-job/musik/player/internal/index"
)

type ArtistGroup struct {
	Artist       string
	Tracks       int
	CoverTrackID int64
	HasArtwork   bool
}

type AlbumGroup struct {
	Artist       string
	Album        string
	Tracks       int
	CoverTrackID int64
	HasArtwork   bool
	// Game is true when the album's files live under a game-soundtrack folder
	// ("Steam OST", "GOG OST"): album titles like "Cyberpunk 2077" say nothing.
	Game bool
}

// gameSoundtrackDirs are library folders that hold game soundtracks (see the Steam/GOG importer).
var gameSoundtrackDirs = []string{"steam ost", "gog ost"}

// IsGameSoundtrackPath reports whether a track path lies in a game-soundtrack folder.
func IsGameSoundtrackPath(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		part = strings.ToLower(strings.TrimSpace(part))
		for _, dir := range gameSoundtrackDirs {
			if part == dir {
				return true
			}
		}
	}
	return false
}

func MatchArtistAlbum(gotArtist string, gotSegments []string, gotAlbum, wantArtist, wantAlbum string) bool {
	if wantArtist != "" {
		want := strings.TrimSpace(wantArtist)
		// The raw label matches first so queries that carry a full
		// collaboration string keep working, then each stored segment.
		matched := strings.EqualFold(strings.TrimSpace(gotArtist), want)
		if !matched {
			for _, segment := range gotSegments {
				if strings.EqualFold(strings.TrimSpace(segment), want) {
					matched = true
					break
				}
			}
		}
		if !matched {
			return false
		}
	}
	if wantAlbum != "" && !strings.EqualFold(strings.TrimSpace(gotAlbum), strings.TrimSpace(wantAlbum)) {
		return false
	}
	return true
}

func GroupArtists(idx *index.Index) []ArtistGroup {
	if idx == nil {
		return nil
	}
	by := map[string]*ArtistGroup{}
	n := idx.Size()
	for i := 0; i < n; i++ {
		m := idx.MetaAt(i)
		names := m.Artists
		if len(names) == 0 {
			names = []string{strings.TrimSpace(m.Artist)}
		}
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name == "" {
				addArtistGroup(by, "Unknown", m)
				continue
			}
			addArtistGroup(by, name, m)
		}
	}
	out := make([]ArtistGroup, 0, len(by))
	for _, g := range by {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tracks != out[j].Tracks {
			return out[i].Tracks > out[j].Tracks
		}
		return out[i].Artist < out[j].Artist
	})
	return out
}

func addArtistGroup(by map[string]*ArtistGroup, name string, m index.Meta) {
	key := strings.ToLower(name)
	g := by[key]
	if g == nil {
		g = &ArtistGroup{Artist: name, CoverTrackID: m.ID, HasArtwork: m.ArtworkPath != ""}
		by[key] = g
	}
	g.Tracks++
}

func GroupAlbums(idx *index.Index) []AlbumGroup {
	if idx == nil {
		return nil
	}
	by := map[string]*AlbumGroup{}
	n := idx.Size()
	for i := 0; i < n; i++ {
		m := idx.MetaAt(i)
		album := strings.TrimSpace(m.Album)
		if album == "" {
			continue
		}
		// Group by the primary collaborator segment so a record credited to
		// several artists stays a single album entry instead of one per artist.
		artist := strings.TrimSpace(m.Artist)
		if len(m.Artists) > 0 {
			artist = strings.TrimSpace(m.Artists[0])
		}
		key := strings.ToLower(artist) + "\x00" + strings.ToLower(album)
		g := by[key]
		if g == nil {
			g = &AlbumGroup{
				Artist: artist, Album: album,
				CoverTrackID: m.ID, HasArtwork: m.ArtworkPath != "",
			}
			by[key] = g
		}
		g.Tracks++
		if IsGameSoundtrackPath(m.Path) {
			g.Game = true
		}
	}
	out := make([]AlbumGroup, 0, len(by))
	for _, g := range by {
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tracks != out[j].Tracks {
			return out[i].Tracks > out[j].Tracks
		}
		return out[i].Album < out[j].Album
	})
	return out
}
