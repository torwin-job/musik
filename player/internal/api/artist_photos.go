package api

import (
	"net/http"
	"strconv"
)

// handleArtistPhotos maps the normalized artist name (spaces collapsed, lower
// case — the same rule the Python fetcher and app.js use) to its photo URL.
func (s *Server) handleArtistPhotos(w http.ResponseWriter, _ *http.Request) {
	photos, err := s.Store.ListArtistPhotos()
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	out := make(map[string]string, len(photos))
	for _, p := range photos {
		out[p.NameKey] = "/api/artist-photos/" + strconv.FormatInt(p.ID, 10)
	}
	writeJSON(w, map[string]any{"photos": out, "count": len(out)})
}

func (s *Server) handleArtistPhoto(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, 400, "bad_id", "bad id")
		return
	}
	path, ok, err := s.Store.ArtistPhotoPath(id)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	if !ok {
		writeErr(w, 404, "not_found", "no photo")
		return
	}
	s.Media.ServeImage(w, r, "artist"+strconv.FormatInt(id, 10), path)
}
