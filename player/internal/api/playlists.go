package api

import (
	"net/http"
)

func (s *Server) handleDailyToday(w http.ResponseWriter, _ *http.Request) {
	pl, err := s.Store.LatestPlaylist("daily")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if pl == nil {
		writeJSON(w, map[string]any{"ok": false, "playlist": nil, "hint": "enqueue daily job via POST /api/jobs/daily"})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "playlist": pl})
}

func (s *Server) handleDailyPlay(w http.ResponseWriter, _ *http.Request) {
	pl, err := s.Store.LatestPlaylist("daily")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if pl == nil || len(pl.Tracks) == 0 {
		http.Error(w, "no daily playlist", 404)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]int64, 0, len(pl.Tracks))
	for _, t := range pl.Tracks {
		ids = append(ids, t.TrackID)
	}
	sess := s.startFixedLocked(ids, "daily", pl.Name, "daily", 0, 0)
	out := s.playResponse(sess)
	out["playlist"] = pl
	writeJSON(w, out)
}

func (s *Server) handlePlaylistLatest(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind == "" {
		http.Error(w, "kind required", 400)
		return
	}
	pl, err := s.Store.LatestPlaylist(kind)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if pl == nil {
		http.Error(w, "not found", 404)
		return
	}
	writeJSON(w, pl)
}
