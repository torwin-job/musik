package api

import (
	"encoding/json"
	"net/http"
)

func (s *Server) handleRadioStart(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SeedTrackID *int64 `json:"seed_track_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.newSession("radio")
	s.seedRadioExcludeLocked(sess)
	startID := s.pickStartTrackLocked(sess, req.SeedTrackID)
	sess.Current = startID
	sess.Prev = 0
	s.excludeTrackLocked(sess, startID)
	s.refreshQueueFor(sess, startID, true)
	// refreshQueueFor already scheduleWarm
	writeJSON(w, map[string]any{
		"session_id": sess.ID,
		"mode":       "radio",
		"maturity":   s.maturityLocked(),
		"current":    s.trackJSON(startID),
		"queue":      sess.Queue,
	})
}
