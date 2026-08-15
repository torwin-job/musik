package api

import (
	"encoding/json"
	"net/http"
	"time"
)

// Mix shelf definition (VK-style).
var mixShelf = []struct {
	Kind     string
	Title    string
	Subtitle string
}{
	{"for_you", "Для вас", "Персональный микс под вкус"},
	{"daily", "На сегодня", "Свежий микс на день"},
	{"favorites", "Избранное", "Треки с сердечком"},
	{"later", "Потом", "Отложенные треки"},
	{"new_releases", "Новинки", "Недавно в библиотеке"},
	{"weekly", "Недельный микс", "Разнообразие по кластерам"},
	{"weekday_mon", "Понедельник", "Микс дня недели"},
	{"weekday_tue", "Вторник", "Микс дня недели"},
	{"weekday_wed", "Среда", "Микс дня недели"},
	{"weekday_thu", "Четверг", "Микс дня недели"},
	{"weekday_fri", "Пятница", "Микс дня недели"},
	{"weekday_sat", "Суббота", "Микс дня недели"},
	{"weekday_sun", "Воскресенье", "Микс дня недели"},
}

func weekdayKind(day time.Weekday) string {
	// Go: Sunday=0 … Saturday=6; we use Mon-first keys
	switch day {
	case time.Monday:
		return "weekday_mon"
	case time.Tuesday:
		return "weekday_tue"
	case time.Wednesday:
		return "weekday_wed"
	case time.Thursday:
		return "weekday_thu"
	case time.Friday:
		return "weekday_fri"
	case time.Saturday:
		return "weekday_sat"
	default:
		return "weekday_sun"
	}
}

func todayWeekdayKind() string {
	return weekdayKind(time.Now().Weekday())
}

func (s *Server) handleMixes(w http.ResponseWriter, _ *http.Request) {
	today := todayWeekdayKind()
	out := make([]map[string]any, 0, len(mixShelf))
	for _, m := range mixShelf {
		card := map[string]any{
			"kind": m.Kind, "title": m.Title, "subtitle": m.Subtitle,
			"highlight": m.Kind == "for_you" || m.Kind == "daily" || m.Kind == today,
			"today":     m.Kind == today,
		}
		if m.Kind == "later" {
			tracks, _ := s.Store.LaterList()
			card["tracks"] = len(tracks)
			card["ready"] = len(tracks) > 0
			card["playlist_id"] = nil
			card["special"] = "later"
			if len(tracks) > 0 {
				card["cover_track_id"] = tracks[0].TrackID
			}
		} else if m.Kind == "favorites" {
			tracks, _ := s.Store.FavoritesList()
			card["tracks"] = len(tracks)
			card["ready"] = len(tracks) > 0
			card["playlist_id"] = nil
			card["special"] = "favorites"
			if len(tracks) > 0 {
				card["cover_track_id"] = tracks[0].TrackID
			}
		} else {
			id, name, n, err := s.Store.PlaylistMeta(m.Kind)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			card["playlist_id"] = nil
			card["name"] = name
			card["tracks"] = n
			card["ready"] = id != 0 && n > 0
			if id != 0 {
				card["playlist_id"] = id
				pl, _ := s.Store.LatestPlaylist(m.Kind)
				if pl != nil && len(pl.Tracks) > 0 {
					card["cover_track_id"] = pl.Tracks[0].TrackID
					card["generated_at"] = pl.CreatedAt
					if generatedAt, err := time.Parse(time.RFC3339Nano, pl.CreatedAt); err == nil {
						card["age_seconds"] = int64(time.Since(generatedAt).Seconds())
						card["stale"] = time.Since(generatedAt) > 36*time.Hour
					}
				}
			}
		}
		out = append(out, card)
	}
	writeJSON(w, map[string]any{
		"mixes":         out,
		"today_weekday": today,
		"hint":          "Полки пересобираются ночью; POST /api/jobs/mix_pack запускает обновление вручную",
	})
}

func (s *Server) handleMixPlay(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind == "" {
		http.Error(w, "kind required", 400)
		return
	}

	var req struct {
		StartIndex   *int  `json:"start_index"`
		StartTrackID int64 `json:"start_track_id"`
		TrackID      int64 `json:"track_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.StartTrackID == 0 && req.TrackID != 0 {
		req.StartTrackID = req.TrackID
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var ids []int64
	var name string
	mode := "playlist"
	generatedMix := false

	if kind == "later" {
		tracks, err := s.Store.LaterList()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if len(tracks) == 0 {
			http.Error(w, "Потом пусто — добавь треки кнопкой «Потом»", 404)
			return
		}
		for _, t := range tracks {
			ids = append(ids, t.TrackID)
		}
		name = "Потом"
		mode = "later"
	} else if kind == "favorites" {
		tracks, err := s.Store.FavoritesList()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if len(tracks) == 0 {
			http.Error(w, "Избранное пусто — жми ♥ на треке", 404)
			return
		}
		for _, t := range tracks {
			ids = append(ids, t.TrackID)
		}
		name = "Избранное"
		mode = "favorites"
	} else {
		generatedMix = true
		pl, err := s.Store.LatestPlaylist(kind)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if pl == nil || len(pl.Tracks) == 0 {
			http.Error(w, "плейлист не собран — нажми «Обновить миксы»", 404)
			return
		}
		for _, t := range pl.Tracks {
			ids = append(ids, t.TrackID)
		}
		name = pl.Name
	}

	startIdx := 0
	if req.StartIndex != nil {
		startIdx = *req.StartIndex
	}
	sess := s.startFixedLocked(ids, mode, name, kind, startIdx, req.StartTrackID)
	if generatedMix {
		for _, id := range ids {
			_ = s.Store.BumpRecStats(id, 1, 0, 0)
			s.Idx.BumpShownLocal(id)
		}
	}
	writeJSON(w, s.playResponse(sess))
}

func (s *Server) handleLaterList(w http.ResponseWriter, _ *http.Request) {
	tracks, err := s.Store.LaterList()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	enriched := make([]any, 0, len(tracks))
	for _, t := range tracks {
		enriched = append(enriched, map[string]any{
			"track_id": t.TrackID, "artist": t.Artist, "title": t.Title,
			"duration": t.Duration, "position": t.Position,
			"stream": s.trackJSON(t.TrackID),
		})
	}
	writeJSON(w, map[string]any{"tracks": enriched, "count": len(tracks)})
}

func (s *Server) handleLaterAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TrackID int64 `json:"track_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TrackID == 0 {
		http.Error(w, "track_id required", 400)
		return
	}
	if err := s.Store.LaterAdd(req.TrackID); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "count": s.Store.LaterCount()})
}

func (s *Server) handleLaterRemove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TrackID int64 `json:"track_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TrackID == 0 {
		http.Error(w, "track_id required", 400)
		return
	}
	if err := s.Store.LaterRemove(req.TrackID); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "count": s.Store.LaterCount()})
}
