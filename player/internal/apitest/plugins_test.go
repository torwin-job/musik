package apitest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/torwin-job/musik/player/internal/api"
	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/playback"
	"github.com/torwin-job/musik/player/internal/taste"
	"github.com/torwin-job/musik/player/internal/testdb"
)

func TestPluginSearch(t *testing.T) {
	query := "musik-plugin-search-secret"
	pluginDir := t.TempDir()
	writePluginExecutable(t, pluginDir, "plugin", searchPluginScript())
	writePluginExecutable(t, pluginDir, "plain", plainPluginScript())
	if err := os.WriteFile(filepath.Join(pluginDir, "example.json"), []byte(
		`{"id":"example","executable":"plugin","protocol":"1","name":"Example","description":"Example catalog","capabilities":["library.import_url","library.search"]}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plain.json"), []byte(
		`{"id":"plain","executable":"plain","protocol":"1","capabilities":["library.import_url"]}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	server := openPluginServer(t, t.TempDir(), pluginDir)
	catalog := serve(server, jsonReq(http.MethodGet, "/api/plugins", ""))
	if catalog.Code != http.StatusOK || strings.Contains(catalog.Body.String(), query) {
		t.Fatalf("catalog status=%d body=%s", catalog.Code, catalog.Body.String())
	}
	var listed struct {
		Plugins []struct {
			ID           string   `json:"id"`
			Name         string   `json:"name"`
			Description  string   `json:"description"`
			Capabilities []string `json:"capabilities"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(catalog.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	var exampleFound bool
	for _, plugin := range listed.Plugins {
		if plugin.ID != "example" {
			continue
		}
		exampleFound = true
		if plugin.Name != "Example" || plugin.Description != "Example catalog" || !containsString(plugin.Capabilities, "library.search") || !containsString(plugin.Capabilities, "library.import_url") {
			t.Fatalf("plugin = %+v", plugin)
		}
	}
	if !exampleFound {
		t.Fatalf("plugins = %+v", listed.Plugins)
	}

	badQuery := serve(server, jsonReq(http.MethodPost, "/api/plugins/example/search", `{"query":"bad\nquery"}`))
	if badQuery.Code != http.StatusBadRequest {
		t.Fatalf("bad query status=%d body=%s", badQuery.Code, badQuery.Body.String())
	}
	badLimit := serve(server, jsonReq(http.MethodPost, "/api/plugins/example/search", `{"query":"example track","limit":21}`))
	if badLimit.Code != http.StatusBadRequest {
		t.Fatalf("bad limit status=%d body=%s", badLimit.Code, badLimit.Body.String())
	}
	missing := serve(server, jsonReq(http.MethodPost, "/api/plugins/plain/search", `{"query":"`+query+`"}`))
	if missing.Code != http.StatusNotFound || strings.Contains(missing.Body.String(), query) {
		t.Fatalf("plain status=%d body=%s", missing.Code, missing.Body.String())
	}
	if _, err := os.Stat(filepath.Join(pluginDir, "plain-ran")); err == nil {
		t.Fatal("import-only plugin was started for search")
	}
	if countPluginJobs(t, server) != 0 || countJobs(t, server) != 0 {
		t.Fatal("rejected search was stored")
	}

	found := serve(server, jsonReq(http.MethodPost, "/api/plugins/example/search", `{"query":"`+query+`"}`))
	if found.Code != http.StatusOK || strings.Contains(found.Body.String(), query) {
		t.Fatalf("search status=%d body=%s", found.Code, found.Body.String())
	}
	var body struct {
		Items []struct {
			Title    string `json:"title"`
			Artist   string `json:"artist"`
			URL      string `json:"url"`
			Duration int    `json:"duration"`
		} `json:"items"`
	}
	if err := json.Unmarshal(found.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Items) != 1 || body.Items[0].Title != "Example Track" || body.Items[0].Artist != "Example Artist" || body.Items[0].URL != "https://example.invalid/track" || body.Items[0].Duration != 248 {
		t.Fatalf("items = %+v", body.Items)
	}
	var recorded struct {
		Action string         `json:"action"`
		Input  map[string]any `json:"input"`
	}
	raw, err := os.ReadFile(filepath.Join(pluginDir, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &recorded); err != nil {
		t.Fatal(err)
	}
	if recorded.Action != "library.search" || recorded.Input["query"] != query || recorded.Input["limit"] != float64(8) {
		t.Fatalf("recorded = %+v", recorded)
	}
	argv, err := os.ReadFile(filepath.Join(pluginDir, "argv.json"))
	if err != nil {
		t.Fatal(err)
	}
	envText, err := os.ReadFile(filepath.Join(pluginDir, "env"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(argv), query) || strings.Contains(string(envText), query) {
		t.Fatal("query leaked into argv or the environment")
	}
	if countPluginJobs(t, server) != 0 || countJobs(t, server) != 0 {
		t.Fatal("search created a job")
	}
}

func TestPluginSearchCancel(t *testing.T) {
	query := "musik-plugin-search-secret"
	pluginDir := t.TempDir()
	writePluginExecutable(t, pluginDir, "plugin", searchWaitScript())
	if err := os.WriteFile(filepath.Join(pluginDir, "example.json"), []byte(
		`{"id":"example","executable":"plugin","protocol":"1","capabilities":["library.import_url","library.search"]}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	server := openPluginServer(t, t.TempDir(), pluginDir)
	ctx, cancel := context.WithCancel(context.Background())
	req := jsonReq(http.MethodPost, "/api/plugins/example/search", `{"query":"`+query+`"}`).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.Handler().ServeHTTP(rec, req)
		close(done)
	}()
	waitForFile(t, filepath.Join(pluginDir, "ready"))
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("search handler did not return after cancel")
	}
	waitForFile(t, filepath.Join(pluginDir, "stopped"))
	if strings.Contains(rec.Body.String(), query) {
		t.Fatalf("cancel response leaked the query: %s", rec.Body.String())
	}
	if countPluginJobs(t, server) != 0 {
		t.Fatal("cancelled search was stored")
	}
}

func TestPluginSearchTimeout(t *testing.T) {
	query := "musik-plugin-search-secret"
	pluginDir := t.TempDir()
	writePluginExecutable(t, pluginDir, "plugin", searchWaitScript())
	if err := os.WriteFile(filepath.Join(pluginDir, "example.json"), []byte(
		`{"id":"example","executable":"plugin","protocol":"1","capabilities":["library.import_url","library.search"]}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	server := openPluginServerTimeout(t, t.TempDir(), pluginDir, time.Second)
	started := time.Now()
	rec := serve(server, jsonReq(http.MethodPost, "/api/plugins/example/search", `{"query":"`+query+`"}`))
	if rec.Code != http.StatusGatewayTimeout || strings.Contains(rec.Body.String(), query) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if time.Since(started) > 4*time.Second {
		t.Fatalf("search timeout took %s", time.Since(started))
	}
	waitForFile(t, filepath.Join(pluginDir, "stopped"))
}

func searchPluginScript() string {
	return `#!/usr/bin/env python3
import json, os, sys, pathlib
ROOT = pathlib.Path(__file__).resolve().parent

def emit(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

(ROOT / "argv.json").write_text(json.dumps(sys.argv))
(ROOT / "env").write_text("\n".join("%s=%s" % item for item in os.environ.items()))
if sys.argv[1:] == ["describe"]:
    emit({
        "type": "describe", "protocol": "1", "id": "example",
        "name": "Example", "description": "Example catalog",
        "capabilities": ["library.import_url", "library.search"],
    })
    raise SystemExit(0)
if sys.argv[1:] != ["run"]:
    raise SystemExit(2)
req = json.loads(sys.stdin.readline())
(ROOT / "request.json").write_text(json.dumps(req))
if req.get("action") != "library.search":
    raise SystemExit(2)
emit({
    "type": "result", "protocol": "1", "id": req["id"],
    "result": {"items": [{
        "title": "Example Track", "artist": "Example Artist",
        "url": "https://example.invalid/track", "duration": 248,
    }]},
})
raise SystemExit(0)
`
}

func searchWaitScript() string {
	return `#!/usr/bin/env python3
import json, signal, sys, time, pathlib
ROOT = pathlib.Path(__file__).resolve().parent
REQUEST_ID = "0"

def emit(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

def on_term(signum, frame):
    (ROOT / "stopped").write_text("1")
    emit({"type": "error", "protocol": "1", "id": REQUEST_ID, "error": {"code": "cancelled", "message": "cancelled"}})
    raise SystemExit(1)

signal.signal(signal.SIGTERM, on_term)
if sys.argv[1:] == ["describe"]:
    emit({
        "type": "describe", "protocol": "1", "id": "example",
        "capabilities": ["library.import_url", "library.search"],
    })
    raise SystemExit(0)
if sys.argv[1:] != ["run"]:
    raise SystemExit(2)
req = json.loads(sys.stdin.readline())
REQUEST_ID = req["id"]
(ROOT / "ready").write_text("1")
time.sleep(30)
`

}

func plainPluginScript() string {
	return `#!/usr/bin/env python3
import json, sys, pathlib
ROOT = pathlib.Path(__file__).resolve().parent
if sys.argv[1:] == ["describe"]:
    sys.stdout.write(json.dumps({
        "type": "describe", "protocol": "1", "id": "plain",
        "capabilities": ["library.import_url"],
    }) + "\n")
    raise SystemExit(0)
(ROOT / "plain-ran").write_text("1")
raise SystemExit(0)
`
}

func TestPluginsEmptyWithoutDirectories(t *testing.T) {
	server := openTestServer(t)
	rec := serve(server, jsonReq(http.MethodGet, "/api/plugins", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Plugins []map[string]any `json:"plugins"`
		Count   int              `json:"count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Count != 0 || len(body.Plugins) != 0 {
		t.Fatalf("catalog = %+v", body)
	}
}

func TestPluginImportProgressAndCancel(t *testing.T) {
	secret := "https://example.invalid/musik-plugin-secret-token"
	library := t.TempDir()
	pluginDir := t.TempDir()
	writePluginExecutable(t, pluginDir, "plugin", importPluginScript(false))
	if err := os.WriteFile(filepath.Join(pluginDir, "example.json"), []byte(
		`{"id":"example","executable":"plugin","protocol":"1","capabilities":["library.import_url"]}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "bad.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}

	server := openPluginServer(t, library, pluginDir)
	catalog := serve(server, jsonReq(http.MethodGet, "/api/plugins", ""))
	if catalog.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", catalog.Code, catalog.Body.String())
	}
	var listed struct {
		Count   int `json:"count"`
		Plugins []struct {
			ID           string   `json:"id"`
			Capabilities []string `json:"capabilities"`
		} `json:"plugins"`
		Rejected []struct {
			Reason string `json:"reason"`
		} `json:"rejected"`
	}
	if err := json.Unmarshal(catalog.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Count != 1 || listed.Plugins[0].ID != "example" || !containsString(listed.Plugins[0].Capabilities, "library.import_url") {
		t.Fatalf("plugins = %+v", listed.Plugins)
	}
	if len(listed.Rejected) == 0 {
		t.Fatal("expected a rejected manifest")
	}
	if strings.Contains(catalog.Body.String(), "executable") {
		t.Fatalf("catalog leaked an executable path: %s", catalog.Body.String())
	}

	bad := serve(server, jsonReq(http.MethodPost, "/api/plugins/example/import", `{"url":"bad\nurl"}`))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad url status=%d body=%s", bad.Code, bad.Body.String())
	}
	missing := serve(server, jsonReq(http.MethodPost, "/api/plugins/missing/import", `{"url":"`+secret+`"}`))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing plugin status=%d body=%s", missing.Code, missing.Body.String())
	}
	if countPluginJobs(t, server) != 0 {
		t.Fatal("rejected imports were stored")
	}

	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(pluginDir, "go"), []byte("1"), 0o644)
	})
	created := serve(server, jsonReq(http.MethodPost, "/api/plugins/example/import", `{"url":"`+secret+`"}`))
	if created.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", created.Code, created.Body.String())
	}
	if strings.Contains(created.Body.String(), secret) {
		t.Fatalf("import response leaked the url: %s", created.Body.String())
	}
	var enqueued struct {
		ID     int64  `json:"id"`
		JobID  int64  `json:"job_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &enqueued); err != nil || enqueued.ID == 0 || enqueued.JobID != enqueued.ID {
		t.Fatalf("enqueued = %+v body=%s err=%v", enqueued, created.Body.String(), err)
	}
	var stored string
	if err := server.Store.DB.QueryRow(`SELECT input_json FROM plugin_jobs WHERE id = ?`, enqueued.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stored, secret) {
		t.Fatalf("stored input = %s", stored)
	}

	waitForFile(t, filepath.Join(pluginDir, "ready"))
	status := serve(server, jsonReq(http.MethodGet, fmt.Sprintf("/api/plugin-jobs/%d", enqueued.ID), ""))
	if status.Code != http.StatusOK || strings.Contains(status.Body.String(), secret) {
		t.Fatalf("status=%d body=%s", status.Code, status.Body.String())
	}
	var running map[string]any
	if err := json.Unmarshal(status.Body.Bytes(), &running); err != nil {
		t.Fatal(err)
	}
	progress, _ := running["progress"].(map[string]any)
	if progress["phase"] != "download" || progress["message"] != "[redacted]" {
		t.Fatalf("progress = %#v", running["progress"])
	}
	items, _ := progress["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %#v", progress["items"])
	}
	first, _ := items[0].(map[string]any)
	second, _ := items[1].(map[string]any)
	if first["title"] != "Первая" || first["status"] != "done" || second["title"] != "[redacted]" || second["status"] != "running" {
		t.Fatalf("items = %#v", items)
	}
	jobs := serve(server, jsonReq(http.MethodGet, "/api/plugin-jobs?status=running", ""))
	if jobs.Code != http.StatusOK || strings.Contains(jobs.Body.String(), secret) {
		t.Fatalf("list status=%d body=%s", jobs.Code, jobs.Body.String())
	}

	if err := os.WriteFile(filepath.Join(pluginDir, "go"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var done map[string]any
	for {
		rec := serve(server, jsonReq(http.MethodGet, fmt.Sprintf("/api/plugin-jobs/%d", enqueued.ID), ""))
		if err := json.Unmarshal(rec.Body.Bytes(), &done); err != nil {
			t.Fatal(err)
		}
		if done["status"] == "done" || done["status"] == "error" || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if done["status"] != "done" {
		t.Fatalf("job = %#v", done)
	}
	if strings.Contains(fmt.Sprint(done), secret) {
		t.Fatalf("done job leaked the url: %#v", done)
	}
	result, _ := done["result"].(map[string]any)
	paths, _ := result["paths"].([]any)
	if len(paths) != 1 || paths[0] != "imports/example/track.flac" {
		t.Fatalf("result = %#v", done["result"])
	}
	downstream := jsonInt(t, done["downstream_job_id"])
	assertFullRescan(t, server, downstream, secret)
	body, err := os.ReadFile(filepath.Join(library, "imports", "example", "track.flac"))
	if err != nil || string(body) != "audio" {
		t.Fatalf("imported file = %q, %v", body, err)
	}
	cancelDone := serve(server, jsonReq(http.MethodPost, fmt.Sprintf("/api/plugin-jobs/%d/cancel", enqueued.ID), ""))
	if cancelDone.Code != http.StatusConflict {
		t.Fatalf("cancel done status=%d body=%s", cancelDone.Code, cancelDone.Body.String())
	}
	missingJob := serve(server, jsonReq(http.MethodGet, "/api/plugin-jobs/999999", ""))
	if missingJob.Code != http.StatusNotFound {
		t.Fatalf("missing job status=%d", missingJob.Code)
	}
}

func TestPluginImportCancel(t *testing.T) {
	secret := "https://example.invalid/musik-plugin-secret-token"
	pluginDir := t.TempDir()
	writePluginExecutable(t, pluginDir, "plugin", importPluginScript(true))
	if err := os.WriteFile(filepath.Join(pluginDir, "example.json"), []byte(
		`{"id":"example","executable":"plugin","protocol":"1","capabilities":["library.import_url"]}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	server := openPluginServer(t, t.TempDir(), pluginDir)
	created := serve(server, jsonReq(http.MethodPost, "/api/plugins/example/import", `{"url":"`+secret+`"}`))
	if created.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", created.Code, created.Body.String())
	}
	var enqueued struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &enqueued); err != nil || enqueued.ID == 0 {
		t.Fatalf("body=%s err=%v", created.Body.String(), err)
	}
	waitForFile(t, filepath.Join(pluginDir, "ready"))
	cancelled := serve(server, jsonReq(http.MethodPost, fmt.Sprintf("/api/plugin-jobs/%d/cancel", enqueued.ID), ""))
	if cancelled.Code != http.StatusOK || strings.Contains(cancelled.Body.String(), secret) {
		t.Fatalf("cancel status=%d body=%s", cancelled.Code, cancelled.Body.String())
	}
	var job map[string]any
	if err := json.Unmarshal(cancelled.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job["status"] != "cancelled" || job["error_code"] != "cancelled" {
		t.Fatalf("job = %#v", job)
	}
	if job["downstream_job_id"] != nil {
		t.Fatalf("cancelled job queued a rescan: %#v", job["downstream_job_id"])
	}
	if countJobs(t, server) != 0 {
		t.Fatal("cancelled import queued a catalog job")
	}
}

func TestPluginImportFailureSkipsRescan(t *testing.T) {
	pluginDir := t.TempDir()
	writePluginExecutable(t, pluginDir, "plugin", `#!/usr/bin/env python3
import json, sys

def emit(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

if sys.argv[1:] == ["describe"]:
    emit({"type": "describe", "protocol": "1", "id": "example", "capabilities": ["library.import_url"]})
    raise SystemExit(0)
if sys.argv[1:] != ["run"]:
    raise SystemExit(2)
req = json.loads(sys.stdin.readline())
emit({
    "type": "error", "protocol": "1", "id": req["id"],
    "error": {"code": "unsupported_url", "message": "URL is not supported"},
})
raise SystemExit(1)
`)
	if err := os.WriteFile(filepath.Join(pluginDir, "example.json"), []byte(
		`{"id":"example","executable":"plugin","protocol":"1","capabilities":["library.import_url"]}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	server := openPluginServer(t, t.TempDir(), pluginDir)
	created := serve(server, jsonReq(http.MethodPost, "/api/plugins/example/import", `{"url":"https://example.invalid/track"}`))
	if created.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", created.Code, created.Body.String())
	}
	var enqueued struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &enqueued); err != nil || enqueued.ID == 0 {
		t.Fatalf("body=%s err=%v", created.Body.String(), err)
	}
	job := waitPluginJob(t, server, enqueued.ID)
	if job["status"] != "error" || job["error_code"] != "unsupported_url" || job["downstream_job_id"] != nil {
		t.Fatalf("job = %#v", job)
	}
	if countJobs(t, server) != 0 {
		t.Fatal("failed import queued a catalog job")
	}
}

func TestPluginImportQueuesWorkerRescan(t *testing.T) {
	secret := "https://example.invalid/musik-plugin-secret-token"
	library := t.TempDir()
	pluginDir := t.TempDir()
	writePluginExecutable(t, pluginDir, "plugin", importPluginScript(false))
	if err := os.WriteFile(filepath.Join(pluginDir, "example.json"), []byte(
		`{"id":"example","executable":"plugin","protocol":"1","capabilities":["library.import_url"]}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	server := openPluginServer(t, library, pluginDir)
	postedC := make(chan struct {
		Kind    string
		Payload json.RawMessage
	}, 1)
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/jobs" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var posted struct {
			Kind    string          `json:"kind"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&posted); err != nil {
			t.Errorf("decode job: %v", err)
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		postedC <- struct {
			Kind    string
			Payload json.RawMessage
		}{posted.Kind, posted.Payload}
		id, err := server.Store.EnqueueJob(posted.Kind, string(posted.Payload))
		if err != nil {
			t.Errorf("store job: %v", err)
			http.Error(w, "db", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "status": "pending"})
	}))
	t.Cleanup(worker.Close)
	server.Cfg.WorkerURL = worker.URL

	created := serve(server, jsonReq(http.MethodPost, "/api/plugins/example/import", `{"url":"`+secret+`"}`))
	if created.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", created.Code, created.Body.String())
	}
	var enqueued struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &enqueued); err != nil || enqueued.ID == 0 {
		t.Fatalf("body=%s err=%v", created.Body.String(), err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "go"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	job := waitPluginJob(t, server, enqueued.ID)
	if job["status"] != "done" {
		t.Fatalf("job = %#v", job)
	}
	downstream := jsonInt(t, job["downstream_job_id"])
	var posted struct {
		Kind    string
		Payload json.RawMessage
	}
	select {
	case posted = <-postedC:
	default:
		t.Fatal("worker did not receive the rescan")
	}
	if posted.Kind != "full_rescan" || strings.Contains(string(posted.Payload), secret) {
		t.Fatalf("posted kind=%s payload=%s", posted.Kind, posted.Payload)
	}
	assertMixContract(t, posted.Payload)
	assertFullRescan(t, server, downstream, secret)
	if countJobs(t, server) != 1 {
		t.Fatalf("worker jobs = %d", countJobs(t, server))
	}
}

func TestPluginImportRequiresLibrary(t *testing.T) {
	pluginDir := t.TempDir()
	writePluginExecutable(t, pluginDir, "plugin", importPluginScript(true))
	if err := os.WriteFile(filepath.Join(pluginDir, "example.json"), []byte(
		`{"id":"example","executable":"plugin","protocol":"1","capabilities":["library.import_url"]}`+"\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	server := openPluginServer(t, filepath.Join(t.TempDir(), "missing-library"), pluginDir)
	rec := serve(server, jsonReq(http.MethodPost, "/api/plugins/example/import", `{"url":"https://example.invalid/track"}`))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if countPluginJobs(t, server) != 0 {
		t.Fatal("import was stored without a library")
	}
}

func openPluginServer(t *testing.T, library, pluginDir string) *api.Server {
	t.Helper()
	return openPluginServerTimeout(t, library, pluginDir, 3*time.Second)
}

func openPluginServerTimeout(t *testing.T, library, pluginDir string, searchTimeout time.Duration) *api.Server {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "db", "musik-api-test.db")
	databaseURL, path := testdb.Target(t, path)
	store, err := db.OpenDatabase(databaseURL, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	cfg := config.Config{
		Root: dir, DBPath: path, QueueSize: 6, AuthDisabled: true,
		ProfileFormingAt: 3, ProfileReadyAt: 8, ExploreRatio: 0.15,
		DiscoverExploreRatio: 0.35, WorkerURL: "http://127.0.0.1:1",
		WorkerAutostart: false, Library: library, PluginDirs: []string{pluginDir},
		PluginTimeout: 10 * time.Second, PluginSearchTimeout: searchTimeout, PluginConcurrency: 2,
	}
	idx := index.New(cfg)
	server := api.New(cfg, store, idx, taste.New(), nil)
	if server.Play != nil {
		server.Play.Warm = nil
	}
	return server
}

func writePluginExecutable(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func importPluginScript(cancel bool) string {
	if cancel {
		return `#!/usr/bin/env python3
import json, signal, sys, time, pathlib
ROOT = pathlib.Path(__file__).resolve().parent
REQUEST_ID = "0"

def on_term(signum, frame):
    sys.stdout.write(json.dumps({
        "type": "error", "protocol": "1", "id": REQUEST_ID,
        "error": {"code": "cancelled", "message": "cancelled"},
    }) + "\n")
    sys.stdout.flush()
    raise SystemExit(1)

signal.signal(signal.SIGTERM, on_term)
if sys.argv[1:] == ["describe"]:
    sys.stdout.write(json.dumps({
        "type": "describe", "protocol": "1", "id": "example",
        "capabilities": ["library.import_url"],
    }) + "\n")
    raise SystemExit(0)
if sys.argv[1:] != ["run"]:
    raise SystemExit(2)
req = json.loads(sys.stdin.readline())
REQUEST_ID = req["id"]
(ROOT / "ready").write_text("1")
time.sleep(30)
`
	}
	return `#!/usr/bin/env python3
import json, sys, time, pathlib
ROOT = pathlib.Path(__file__).resolve().parent

def emit(obj):
    sys.stdout.write(json.dumps(obj) + "\n")
    sys.stdout.flush()

if sys.argv[1:] == ["describe"]:
    emit({
        "type": "describe", "protocol": "1", "id": "example",
        "name": "Example", "version": "1.2.3",
        "capabilities": ["library.import_url"],
    })
    raise SystemExit(0)
if sys.argv[1:] != ["run"]:
    raise SystemExit(2)
req = json.loads(sys.stdin.readline())
emit({
    "type": "progress", "protocol": "1", "id": req["id"],
    "progress": {
        "pct": 40, "phase": "download", "message": req["input"]["url"],
        "items": [
            {"title": "Первая", "status": "done", "pct": 100},
            {"title": req["input"]["url"], "status": "running", "pct": 20},
        ],
    },
})
(ROOT / "ready").write_text("1")
deadline = time.time() + 8
while not (ROOT / "go").exists():
    if time.time() > deadline:
        raise SystemExit(2)
    time.sleep(0.02)
lib = pathlib.Path(req["input"]["library"])
dest = req["input"]["dest_dir"]
final = lib.joinpath(*dest.split("/"), "track.flac")
final.parent.mkdir(parents=True, exist_ok=True)
part = final.with_name(final.name + ".part")
part.write_bytes(b"audio")
part.replace(final)
emit({
    "type": "result", "protocol": "1", "id": req["id"],
    "result": {"paths": [dest + "/track.flac"]},
})
raise SystemExit(0)
`
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("file %s did not appear", path)
}

func countPluginJobs(t *testing.T, server *api.Server) int {
	t.Helper()
	var n int
	if err := server.Store.DB.QueryRow(`SELECT count(*) FROM plugin_jobs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func waitPluginJob(t *testing.T, server *api.Server, id int64) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var job map[string]any
	for {
		rec := serve(server, jsonReq(http.MethodGet, fmt.Sprintf("/api/plugin-jobs/%d", id), ""))
		if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		status, _ := job["status"].(string)
		if status == "done" || status == "error" || status == "cancelled" || time.Now().After(deadline) {
			return job
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func countJobs(t *testing.T, server *api.Server) int {
	t.Helper()
	var n int
	if err := server.Store.DB.QueryRow(`SELECT count(*) FROM jobs`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func jsonInt(t *testing.T, v any) int64 {
	t.Helper()
	n, ok := v.(float64)
	if !ok || n < 1 || n != float64(int64(n)) {
		t.Fatalf("id = %#v", v)
	}
	return int64(n)
}

func assertFullRescan(t *testing.T, server *api.Server, id int64, secret string) {
	t.Helper()
	var kind, payload, status string
	if err := server.Store.DB.QueryRow(
		`SELECT kind, COALESCE(payload_json, ''), status FROM jobs WHERE id = ?`, id,
	).Scan(&kind, &payload, &status); err != nil {
		t.Fatal(err)
	}
	if kind != "full_rescan" || status != "pending" {
		t.Fatalf("catalog job %d kind=%s status=%s", id, kind, status)
	}
	if secret != "" && strings.Contains(payload, secret) {
		t.Fatalf("rescan payload leaked the import url: %s", payload)
	}
	assertMixContract(t, json.RawMessage(payload))
	if countJobs(t, server) != 1 {
		t.Fatalf("catalog jobs = %d", countJobs(t, server))
	}
}

func assertMixContract(t *testing.T, payload json.RawMessage) {
	t.Helper()
	var body struct {
		Mixes []playback.MixContract `json:"mixes"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatalf("payload %s: %v", payload, err)
	}
	want, _ := json.Marshal(playback.GeneratedMixContract())
	got, _ := json.Marshal(body.Mixes)
	if string(got) != string(want) {
		t.Fatalf("mixes = %s", got)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
