package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

type playReq struct {
	SessionID    string  `json:"session_id"`
	TrackID      int64   `json:"track_id"`
	TrackIDs     []int64 `json:"track_ids"`
	Artist       string  `json:"artist"`
	Album        string  `json:"album"`
	StartIndex   *int    `json:"start_index"`
	StartTrackID int64   `json:"start_track_id"`
	Name         string  `json:"name"`
}

type jumpReq struct {
	SessionID string `json:"session_id"`
	Index     *int   `json:"index"`
	TrackID   int64  `json:"track_id"`
}

func isFixedMode(mode string) bool {
	switch mode {
	case "playlist", "later", "daily", "listen", "favorites":
		return true
	default:
		return false
	}
}

func (s *Server) sessionTracks(sess *PlaySession) []any {
	if sess == nil || len(sess.DailyIDs) == 0 {
		return nil
	}
	out := make([]any, 0, len(sess.DailyIDs))
	for i, id := range sess.DailyIDs {
		tj := s.trackJSON(id)
		m, ok := tj.(map[string]any)
		if !ok || m == nil {
			continue
		}
		m["position"] = i
		m["current"] = i == sess.DailyPos && sess.Current == id
		out = append(out, m)
	}
	return out
}

func (s *Server) resolvePlayIDs(req playReq) (ids []int64, name string, errMsg string, code int) {
	if len(req.TrackIDs) > 0 {
		for _, id := range req.TrackIDs {
			if _, ok := s.Idx.RowOf(id); ok {
				ids = append(ids, id)
			}
		}
		name = req.Name
		if name == "" {
			name = "Плейлист"
		}
		if len(ids) == 0 {
			return nil, "", "нет доступных треков", 404
		}
		return ids, name, "", 0
	}

	artist := strings.TrimSpace(req.Artist)
	album := strings.TrimSpace(req.Album)
	if artist != "" || album != "" {
		n := s.Idx.Size()
		for i := 0; i < n; i++ {
			m := s.Idx.MetaAt(i)
			if artist != "" && !strings.EqualFold(strings.TrimSpace(m.Artist), artist) {
				continue
			}
			if album != "" && !strings.EqualFold(strings.TrimSpace(m.Album), album) {
				continue
			}
			ids = append(ids, m.ID)
		}
		if len(ids) == 0 {
			return nil, "", "ничего не найдено", 404
		}
		switch {
		case artist != "" && album != "":
			name = artist + " — " + album
		case album != "":
			name = album
		default:
			name = artist
		}
		if req.Name != "" {
			name = req.Name
		}
		return ids, name, "", 0
	}

	if req.TrackID != 0 {
		if _, ok := s.Idx.RowOf(req.TrackID); !ok {
			return nil, "", "track not found", 404
		}
		name = req.Name
		if name == "" {
			name = "Трек"
		}
		return []int64{req.TrackID}, name, "", 0
	}
	return nil, "", "укажи track_id, track_ids, artist или album", 400
}

func (s *Server) startFixedLocked(ids []int64, mode, name, kind string, startIndex int, startTrackID int64) *PlaySession {
	pos := 0
	if startTrackID != 0 {
		for i, id := range ids {
			if id == startTrackID {
				pos = i
				break
			}
		}
	} else if startIndex >= 0 && startIndex < len(ids) {
		pos = startIndex
	}
	sess := s.newSession(mode)
	sess.DailyIDs = ids
	sess.DailyPos = pos
	sess.PlaylistName = name
	sess.PlaylistKind = kind
	sess.Current = ids[pos]
	s.excludeTrackLocked(sess, sess.Current)
	s.rebuildDailyQueueFor(sess)
	return sess
}

func (s *Server) playResponse(sess *PlaySession) map[string]any {
	return map[string]any{
		"session_id": sess.ID,
		"mode":       sess.Mode,
		"kind":       sess.PlaylistKind,
		"name":       sess.PlaylistName,
		"index":      sess.DailyPos,
		"count":      len(sess.DailyIDs),
		"current":    s.trackJSON(sess.Current),
		"queue":      sess.Queue,
		"tracks":     s.sessionTracks(sess),
		"fixed":      isFixedMode(sess.Mode),
	}
}

// POST /api/play — fixed listen list (track / album / artist / ids). Taste still learns from events.
func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	var req playReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	ids, name, errMsg, code := s.resolvePlayIDs(req)
	if code != 0 {
		http.Error(w, errMsg, code)
		return
	}
	startIdx := 0
	if req.StartIndex != nil {
		startIdx = *req.StartIndex
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.startFixedLocked(ids, "listen", name, "listen", startIdx, req.StartTrackID)
	writeJSON(w, s.playResponse(sess))
}

// POST /api/session/jump — jump within current fixed playlist.
func (s *Server) handleSessionJump(w http.ResponseWriter, r *http.Request) {
	var req jumpReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.requireSession(w, r, req.SessionID)
	if sess == nil {
		return
	}
	if len(sess.DailyIDs) == 0 {
		http.Error(w, "нечего переключать — это не плейлист", 400)
		return
	}
	pos := -1
	if req.Index != nil {
		pos = *req.Index
	} else if req.TrackID != 0 {
		for i, id := range sess.DailyIDs {
			if id == req.TrackID {
				pos = i
				break
			}
		}
	}
	if pos < 0 || pos >= len(sess.DailyIDs) {
		http.Error(w, "track not in playlist", 404)
		return
	}
	sess.DailyPos = pos
	sess.Current = sess.DailyIDs[pos]
	s.excludeTrackLocked(sess, sess.Current)
	s.rebuildDailyQueueFor(sess) // includes scheduleWarm
	out := s.playResponse(sess)
	out["ok"] = true
	writeJSON(w, out)
}
