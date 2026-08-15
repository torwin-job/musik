package api

import (
	"net/http"
	"strconv"
)

func (s *Server) handleTrackLyrics(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "bad track id", 400)
		return
	}
	ly, ok, err := s.Store.GetLyrics(id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if !ok {
		writeJSON(w, map[string]any{
			"track_id":      id,
			"status":        "absent",
			"plain_lyrics":  "",
			"synced_lyrics": "",
		})
		return
	}
	writeJSON(w, ly)
}
