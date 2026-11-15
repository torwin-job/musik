package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/torwin-job/musik/player/internal/app"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/plugins"
)

func (s *Server) handlePlugins(w http.ResponseWriter, _ *http.Request) {
	if s.App == nil || s.App.Plugins == nil {
		writeErr(w, http.StatusServiceUnavailable, "plugins_unavailable", "plugins are unavailable")
		return
	}
	writeJSON(w, s.App.Plugins.Catalog())
}

func (s *Server) handlePluginImport(w http.ResponseWriter, r *http.Request) {
	host, ok := s.pluginHost(w)
	if !ok {
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_url", "url is invalid")
		return
	}
	job, err := host.ImportURL(r.PathValue("id"), body.URL)
	if err != nil {
		writePluginErr(w, err)
		return
	}
	// The row still holds the import URL. The response does not.
	out := pluginJobPublic(job)
	out["ok"] = true
	out["job_id"] = job.ID
	writeJSON(w, out)
}

func (s *Server) handlePluginSearch(w http.ResponseWriter, r *http.Request) {
	host, ok := s.pluginHost(w)
	if !ok {
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	dec.UseNumber()
	var body map[string]any
	if err := dec.Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_query", "query is invalid")
		return
	}
	query, _ := body["query"].(string)
	limit := plugins.DefaultSearchLimit
	if raw, present := body["limit"]; present && raw != nil {
		number, ok := raw.(json.Number)
		if !ok {
			writeErr(w, http.StatusBadRequest, "invalid_limit", "limit is invalid")
			return
		}
		parsed, err := number.Int64()
		if err != nil || parsed < int64(plugins.MinSearchLimit) || parsed > int64(plugins.MaxSearchLimit) {
			writeErr(w, http.StatusBadRequest, "invalid_limit", "limit is invalid")
			return
		}
		limit = int(parsed)
	}
	items, err := host.Search(r.Context(), r.PathValue("id"), query, limit)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(r.Context().Err(), context.Canceled) {
			return
		}
		writePluginSearchErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		row := map[string]any{"title": item.Title, "url": item.URL}
		if item.Artist != "" {
			row["artist"] = item.Artist
		}
		if item.Duration != nil {
			row["duration"] = *item.Duration
		}
		out = append(out, row)
	}
	writeJSON(w, map[string]any{"items": out})
}

func (s *Server) handleListPluginJobs(w http.ResponseWriter, r *http.Request) {
	host, ok := s.pluginHost(w)
	if !ok {
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	jobs, err := host.ListPluginJobs(r.URL.Query().Get("status"), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db", "plugin jobs unavailable")
		return
	}
	if jobs == nil {
		jobs = []db.PluginJob{}
	}
	out := make([]map[string]any, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, pluginJobPublic(job))
	}
	writeJSON(w, map[string]any{"jobs": out, "count": len(out)})
}

func (s *Server) handleGetPluginJob(w http.ResponseWriter, r *http.Request) {
	host, ok := s.pluginHost(w)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeErr(w, http.StatusBadRequest, "bad_id", "bad id")
		return
	}
	job, err := host.GetPluginJob(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db", "plugin job unavailable")
		return
	}
	if job == nil {
		writeErr(w, http.StatusNotFound, "not_found", "not found")
		return
	}
	writeJSON(w, pluginJobPublic(*job))
}

func (s *Server) handleCancelPluginJob(w http.ResponseWriter, r *http.Request) {
	host, ok := s.pluginHost(w)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeErr(w, http.StatusBadRequest, "bad_id", "bad id")
		return
	}
	job, err := host.Cancel(r.Context(), id)
	if err != nil {
		writePluginErr(w, err)
		return
	}
	writeJSON(w, pluginJobPublic(job))
}

func (s *Server) pluginHost(w http.ResponseWriter) (*app.PluginHost, bool) {
	if s.App == nil || s.App.Plugins == nil {
		writeErr(w, http.StatusServiceUnavailable, "plugins_unavailable", "plugins are unavailable")
		return nil, false
	}
	return s.App.Plugins, true
}

func writePluginSearchErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, app.ErrPluginNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, app.ErrPluginQueryInvalid):
		writeErr(w, http.StatusBadRequest, "invalid_query", "query is invalid")
	case errors.Is(err, app.ErrPluginLimitInvalid):
		writeErr(w, http.StatusBadRequest, "invalid_limit", "limit is invalid")
	case errors.Is(err, context.DeadlineExceeded):
		writeErr(w, http.StatusGatewayTimeout, "plugin_timeout", "search timed out")
	default:
		writeErr(w, http.StatusInternalServerError, "plugin_search", "search failed")
	}
}

func writePluginErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, app.ErrPluginNotFound), errors.Is(err, app.ErrPluginJobNotFound):
		writeErr(w, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, app.ErrPluginURLInvalid):
		writeErr(w, http.StatusBadRequest, "invalid_url", "url is invalid")
	case errors.Is(err, app.ErrPluginLibrary):
		writeErr(w, http.StatusServiceUnavailable, "library_unavailable", "library is unavailable")
	case errors.Is(err, app.ErrPluginJobNotCancellable):
		writeErr(w, http.StatusConflict, "not_cancellable", "plugin job is not running")
	default:
		writeErr(w, http.StatusInternalServerError, "plugin_job", "plugin job failed")
	}
}

// pluginJobPublic is the admin view of a plugin task. input_json stays in the
// database because the running task needs the URL; this document never includes it.
func pluginJobPublic(job db.PluginJob) map[string]any {
	out := map[string]any{
		"id":                job.ID,
		"plugin_id":         job.PluginID,
		"action":            job.Action,
		"status":            job.Status,
		"error":             job.Error,
		"error_code":        job.ErrorCode,
		"progress":          nil,
		"result":            nil,
		"downstream_job_id": nil,
		"created_at":        job.CreatedAt,
		"updated_at":        job.UpdatedAt,
	}
	if job.DownstreamJobID != 0 {
		out["downstream_job_id"] = job.DownstreamJobID
	}
	if job.Progress != "" {
		var parsed any
		if json.Unmarshal([]byte(job.Progress), &parsed) == nil {
			out["progress"] = parsed
		}
	}
	if job.Result != "" {
		var parsed any
		if json.Unmarshal([]byte(job.Result), &parsed) == nil {
			out["result"] = parsed
		}
	}
	return out
}
