package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/playback"
)

// workerJob is one queue insert, either from the worker or the local fallback.
type workerJob struct {
	ID    int64
	Code  int
	Local bool
	Body  map[string]any
}

func fullRescanPayload() map[string]any {
	return map[string]any{"mixes": playback.GeneratedMixContract()}
}

func (s *Server) handleLibraryRescan(w http.ResponseWriter, _ *http.Request) {
	queued, err := s.enqueueWorkerJob("full_rescan", fullRescanPayload())
	if err != nil {
		writeErr(w, 503, "enqueue_failed", "worker unreachable and local enqueue failed: "+err.Error())
		return
	}
	if queued.Local {
		writeJSON(w, map[string]any{
			"ok": true, "id": queued.ID, "job_id": queued.ID, "status": "pending",
			"hint": "start `musik worker` to process jobs",
		})
		return
	}
	if queued.Code >= 400 {
		writeErr(w, queued.Code, "worker_error", "worker error")
		return
	}
	writeJSON(w, queued.Body)
}

func (s *Server) handleEnqueueJob(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if kind == "" {
		writeErr(w, 400, "kind_required", "kind required")
		return
	}
	if p := caller(r); p != nil && p.DeviceToken && kind != "mix_pack" {
		writeErr(w, http.StatusForbidden, "device_token_scope", "device tokens can only refresh personal mixes")
		return
	}
	// Admin routes run on the installation server. The mix still belongs to
	// the caller's profile; single-user has no principal and uses the install id.
	profileID := s.Store.ProfileID
	if p := caller(r); p != nil && p.Profile.ID != "" {
		profileID = p.Profile.ID
	}
	payload := map[string]any{"profile_id": profileID}
	if kind == "mix_pack" || kind == "full_rescan" {
		payload["mixes"] = playback.GeneratedMixContract()
	}
	queued, err := s.enqueueWorkerJob(kind, payload)
	if err != nil {
		writeErr(w, 503, "enqueue_failed", err.Error())
		return
	}
	if queued.Local {
		writeJSON(w, map[string]any{
			"ok": true, "id": queued.ID, "job_id": queued.ID, "status": "pending", "via": "local_db",
		})
		return
	}
	if queued.Code >= 400 {
		writeErr(w, queued.Code, "worker_error", "worker error")
		return
	}
	writeJSON(w, queued.Body)
}

// enqueueFullRescan queues the same full_rescan a library rescan starts.
// The worker scans, embeds, and asks the player to reload when that job finishes.
func (s *Server) enqueueFullRescan() (int64, error) {
	queued, err := s.enqueueWorkerJob("full_rescan", fullRescanPayload())
	if err != nil {
		return 0, err
	}
	if queued.ID < 1 || (!queued.Local && queued.Code >= 400) {
		return 0, fmt.Errorf("full_rescan was not queued")
	}
	return queued.ID, nil
}

// enqueueWorkerJob posts kind to the worker. When the worker cannot be
// reached, the same payload is inserted locally. An HTTP error from a
// reachable worker is returned as Code and does not insert a second row.
func (s *Server) enqueueWorkerJob(kind string, payload map[string]any) (workerJob, error) {
	s.ensureWorkerBeforeEnqueue()
	body, err := json.Marshal(payload)
	if err != nil {
		return workerJob{}, err
	}
	out, code, err := s.proxyWorker("POST", "/jobs", map[string]any{"kind": kind, "payload": payload})
	if err != nil {
		if s.Store == nil {
			return workerJob{}, err
		}
		id, e2 := s.Store.EnqueueJob(kind, string(body))
		if e2 != nil {
			return workerJob{}, err
		}
		return workerJob{ID: id, Local: true}, nil
	}
	if code >= 400 {
		return workerJob{Code: code, Body: out}, nil
	}
	id, _ := jsonInt64(out["id"])
	return workerJob{ID: id, Code: code, Body: out}, nil
}

func jsonInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		if n < 1 || n != float64(int64(n)) {
			return 0, false
		}
		return int64(n), true
	case int64:
		return n, n > 0
	case int:
		return int64(n), n > 0
	case json.Number:
		i, err := n.Int64()
		return i, err == nil && i > 0
	default:
		return 0, false
	}
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

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, 400, "bad_id", "bad id")
		return
	}
	if p := caller(r); p != nil && p.DeviceToken {
		job, err := s.Store.GetJob(id)
		if err != nil {
			writeErr(w, 500, "db", "job unavailable")
			return
		}
		var payload struct {
			ProfileID string `json:"profile_id"`
		}
		if job == nil || job.Kind != "mix_pack" || json.Unmarshal([]byte(job.Payload), &payload) != nil || payload.ProfileID != p.Profile.ID {
			writeErr(w, 404, "not_found", "not found")
			return
		}
		writeJSON(w, jobPublic(*job))
		return
	}
	out, code, err := s.proxyWorker("GET", "/jobs/"+strconv.FormatInt(id, 10), nil)
	if err == nil && code < 400 && out != nil {
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

func (s *Server) proxyWorker(method, path string, body any) (map[string]any, int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, s.Cfg.WorkerURL+path, rdr)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := s.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return out, res.StatusCode, nil
}
