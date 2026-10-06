package api

import (
	"encoding/json"
	"math"
	"net/http"

	"github.com/torwin-job/musik/player/internal/db"
)

// handlePlaybackStateGet returns where the owner last listened, so a reload or
// another device can resume the same session, track and position.
func (s *Server) handlePlaybackStateGet(w http.ResponseWriter, _ *http.Request) {
	st, ok, err := s.Store.LoadPlaybackState()
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	if !ok || st.TrackID == 0 {
		writeJSON(w, map[string]any{"state": nil})
		return
	}
	writeJSON(w, map[string]any{"state": st, "track": s.trackJSON(st.TrackID)})
}

// handlePlaybackStatePut saves the state from the playing device. "claim":true
// (playback started there) takes ownership; plain heartbeats from a device
// that lost ownership are not applied and get "ok":false with the current
// state, so that device stops playing.
func (s *Server) handlePlaybackStatePut(w http.ResponseWriter, r *http.Request) {
	var req struct {
		db.PlaybackState
		Claim bool `json:"claim"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_json", "bad json")
		return
	}
	st := req.PlaybackState
	if st.TrackID <= 0 {
		writeErr(w, 400, "track_id_required", "track_id required")
		return
	}
	st.PositionSec = finiteNonNegative(st.PositionSec)
	st.ListenedSec = finiteNonNegative(st.ListenedSec)
	saved, applied, err := s.Store.SavePlaybackState(st, req.Claim)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": applied, "state": saved})
}

func finiteNonNegative(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0
	}
	return v
}
