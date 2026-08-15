package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
)

func (s *Server) handleArtists(w http.ResponseWriter, _ *http.Request) {
	type row struct {
		Artist        string `json:"artist"`
		Tracks        int    `json:"tracks"`
		CoverTrackID  int64  `json:"cover_track_id,omitempty"`
		Artwork       string `json:"artwork,omitempty"`
	}
	by := map[string]*row{}
	n := s.Idx.Size()
	for i := 0; i < n; i++ {
		m := s.Idx.MetaAt(i)
		name := strings.TrimSpace(m.Artist)
		if name == "" {
			name = "Unknown"
		}
		key := strings.ToLower(name)
		r := by[key]
		if r == nil {
			r = &row{Artist: name}
			by[key] = r
			r.CoverTrackID = m.ID
			if m.ArtworkPath != "" {
				r.Artwork = "/api/artwork/" + strconv.FormatInt(m.ID, 10)
			}
		}
		r.Tracks++
	}
	out := make([]row, 0, len(by))
	for _, r := range by {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tracks != out[j].Tracks {
			return out[i].Tracks > out[j].Tracks
		}
		return out[i].Artist < out[j].Artist
	})
	writeJSON(w, map[string]any{"artists": out, "count": len(out)})
}

func (s *Server) handleAlbums(w http.ResponseWriter, _ *http.Request) {
	type row struct {
		Artist       string `json:"artist"`
		Album        string `json:"album"`
		Tracks       int    `json:"tracks"`
		CoverTrackID int64  `json:"cover_track_id,omitempty"`
		Artwork      string `json:"artwork,omitempty"`
	}
	by := map[string]*row{}
	n := s.Idx.Size()
	for i := 0; i < n; i++ {
		m := s.Idx.MetaAt(i)
		album := strings.TrimSpace(m.Album)
		if album == "" {
			continue
		}
		artist := strings.TrimSpace(m.Artist)
		key := strings.ToLower(artist) + "\x00" + strings.ToLower(album)
		r := by[key]
		if r == nil {
			r = &row{Artist: artist, Album: album}
			by[key] = r
			r.CoverTrackID = m.ID
			if m.ArtworkPath != "" {
				r.Artwork = "/api/artwork/" + strconv.FormatInt(m.ID, 10)
			}
		}
		r.Tracks++
	}
	out := make([]row, 0, len(by))
	for _, r := range by {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tracks != out[j].Tracks {
			return out[i].Tracks > out[j].Tracks
		}
		return out[i].Album < out[j].Album
	})
	writeJSON(w, map[string]any{"albums": out, "count": len(out)})
}

func (s *Server) handleTrack(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad_id", "bad id")
		return
	}
	tj := s.trackJSON(id)
	if tj == nil {
		writeErr(w, 404, "not_found", "track not found")
		return
	}
	writeJSON(w, tj)
}
