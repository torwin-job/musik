package api

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"

	"github.com/torwin-job/musik/player/internal/db"
)

func (s *Server) handleDiscoverAlbums(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"tips": s.enrichTips("new_album")})
}

func (s *Server) handleDiscoverResurfaced(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"tips": s.enrichTips("resurfaced")})
}

func (s *Server) enrichTips(kind string) []map[string]any {
	tips, err := s.Store.ListDiscoverTips(kind, 20)
	if err != nil {
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(tips))
	for _, t := range tips {
		tracks := make([]any, 0, len(t.TrackIDs))
		for _, id := range t.TrackIDs {
			tracks = append(tracks, s.trackJSON(id))
		}
		out = append(out, map[string]any{
			"id": t.ID, "kind": t.Kind, "artist": t.Artist, "album": t.Album,
			"score": t.Score, "explanation": t.Explanation, "created_at": t.CreatedAt,
			"track_ids": t.TrackIDs, "tracks": tracks,
		})
	}
	return out
}

func (s *Server) handleLibraryRescan(w http.ResponseWriter, _ *http.Request) {
	s.ensureWorkerBeforeEnqueue()
	out, code, err := s.proxyWorker("POST", "/jobs", map[string]any{"kind": "full_rescan"})
	if err != nil {
		id, e2 := s.Store.EnqueueJob("full_rescan", "")
		if e2 != nil {
			http.Error(w, "worker unreachable and local enqueue failed: "+err.Error(), 503)
			return
		}
		writeJSON(w, map[string]any{
			"ok": true, "job_id": id, "status": "pending",
			"hint": "start `musik worker` to process jobs",
		})
		return
	}
	if code >= 400 {
		http.Error(w, "worker error", code)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleEnqueueJob(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind == "" {
		http.Error(w, "kind required", 400)
		return
	}
	s.ensureWorkerBeforeEnqueue()
	out, code, err := s.proxyWorker("POST", "/jobs", map[string]any{"kind": kind})
	if err != nil {
		id, e2 := s.Store.EnqueueJob(kind, "")
		if e2 != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "id": id, "status": "pending", "via": "local_db"})
		return
	}
	if code >= 400 {
		log.Printf("worker enqueue %s status %d", kind, code)
	}
	writeJSON(w, out)
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	jobs, err := s.Store.ListJobs(status, limit)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	if jobs == nil {
		jobs = []db.Job{}
	}
	out := make([]map[string]any, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, jobPublic(j))
	}
	writeJSON(w, map[string]any{"jobs": out, "count": len(out)})
}

func jobPublic(j db.Job) map[string]any {
	m := map[string]any{
		"id": j.ID, "kind": j.Kind, "status": j.Status,
		"error": j.Error, "created_at": j.CreatedAt, "updated_at": j.UpdatedAt,
	}
	if j.Result != "" {
		var parsed any
		if json.Unmarshal([]byte(j.Result), &parsed) == nil {
			m["result"] = parsed
			if pm, ok := parsed.(map[string]any); ok {
				if prog, ok := pm["progress"]; ok {
					m["progress"] = prog
				}
			}
		} else {
			m["result_json"] = j.Result
		}
	}
	return m
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad_id", "bad id")
		return
	}
	out, code, err := s.proxyWorker("GET", "/jobs/"+strconv.FormatInt(id, 10), nil)
	if err == nil && code < 400 && out != nil {
		// normalize progress to top-level for UI
		if res, ok := out["result"].(map[string]any); ok {
			if prog, ok := res["progress"]; ok {
				out["progress"] = prog
			}
		}
		writeJSON(w, out)
		return
	}
	j, err := s.Store.GetJob(id)
	if err != nil {
		writeErr(w, 500, "db", err.Error())
		return
	}
	if j == nil {
		writeErr(w, 404, "not_found", "not found")
		return
	}
	writeJSON(w, jobPublic(*j))
}

func (s *Server) handleWeeklyMetrics(w http.ResponseWriter, _ *http.Request) {
	m, err := s.Store.WeeklyMetrics()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, m)
}

func (s *Server) handleManifest(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	_, _ = w.Write([]byte(`{
  "name": "musik",
  "short_name": "musik",
  "start_url": "/",
  "display": "standalone",
  "background_color": "#141210",
  "theme_color": "#c45c26",
  "description": "Local smart music player"
}`))
}
