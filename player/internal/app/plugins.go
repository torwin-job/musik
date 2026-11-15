package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/plugins"
)

const maxLibraryChars = 4096

var (
	ErrPluginNotFound          = errors.New("plugin not found")
	ErrPluginURLInvalid        = errors.New("url is invalid")
	ErrPluginLibrary           = errors.New("library is unavailable")
	ErrPluginJobNotFound       = errors.New("plugin job not found")
	ErrPluginJobNotCancellable = errors.New("plugin job is not running")
	ErrPluginQueryInvalid      = errors.New("query is invalid")
	ErrPluginLimitInvalid      = errors.New("limit is invalid")
)

// PluginInfo is one executable the admin API may offer.
type PluginInfo struct {
	ID           string   `json:"id"`
	Name         string   `json:"name,omitempty"`
	Version      string   `json:"version,omitempty"`
	Description  string   `json:"description,omitempty"`
	Capabilities []string `json:"capabilities"`
}

// PluginRejection explains a manifest left inactive. Source is a local path
// for the administrator who installed it.
type PluginRejection struct {
	ID     string `json:"id,omitempty"`
	Source string `json:"source,omitempty"`
	Reason string `json:"reason"`
}

// PluginCatalog is the discovery result shown to an administrator.
type PluginCatalog struct {
	Plugins  []PluginInfo      `json:"plugins"`
	Rejected []PluginRejection `json:"rejected"`
	Count    int               `json:"count"`
}

type pluginRun struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// PluginHost runs library.import_url tasks and keeps their rows in plugin_jobs.
// The import URL is stored for the running task and is not written to logs.
// After a successful import, catalogRescan queues the standard full_rescan.
type PluginHost struct {
	library string
	runner  *plugins.Runner
	store   *db.Store

	mu            sync.Mutex
	registry      *plugins.Registry
	runs          map[int64]*pluginRun
	dropped       map[int64]struct{}
	catalogRescan func() (int64, error)
}

func newPluginHost(cfg config.Config, store *db.Store) *PluginHost {
	host := &PluginHost{
		library: cfg.Library,
		runner: plugins.NewRunner(plugins.Limits{
			Concurrency:   cfg.PluginConcurrency,
			Timeout:       cfg.PluginTimeout,
			SearchTimeout: cfg.PluginSearchTimeout,
		}),
		store:   store,
		runs:    map[int64]*pluginRun{},
		dropped: map[int64]struct{}{},
	}
	if store != nil && store.DB != nil && !store.ReadOnly {
		// A store without a pool is a route-table fixture. A read-only replica
		// cannot mark abandoned tasks, and it does not run them either.
		if err := store.InterruptPluginJobs(); err != nil {
			log.Printf("plugin jobs recover: %v", err)
		}
	}
	registry, err := host.runner.Discover(context.Background(), cfg.PluginDirs)
	if err != nil {
		log.Printf("plugin discovery: %v", err)
		registry = &plugins.Registry{}
	}
	host.registry = registry
	if len(cfg.PluginDirs) > 0 {
		log.Printf("plugins active=%d rejected=%d", len(registry.List()), len(registry.Rejections()))
	}
	return host
}

// Catalog returns the plugins discovery accepted and the manifests it left inactive.
func (h *PluginHost) Catalog() PluginCatalog {
	if h == nil {
		return PluginCatalog{Plugins: []PluginInfo{}, Rejected: []PluginRejection{}}
	}
	h.mu.Lock()
	registry := h.registry
	h.mu.Unlock()
	pluginsOut := registry.List()
	out := PluginCatalog{
		Plugins:  make([]PluginInfo, 0, len(pluginsOut)),
		Rejected: make([]PluginRejection, 0),
	}
	for _, plugin := range pluginsOut {
		caps := append([]string(nil), plugin.Capabilities...)
		out.Plugins = append(out.Plugins, PluginInfo{
			ID: plugin.ID, Name: plugin.Name, Version: plugin.Version,
			Description: plugin.Description, Capabilities: caps,
		})
	}
	for _, rejected := range registry.Rejections() {
		out.Rejected = append(out.Rejected, PluginRejection{
			ID: rejected.ID, Source: rejected.Source, Reason: rejected.Reason,
		})
	}
	out.Count = len(out.Plugins)
	return out
}

// SetCatalogRescan queues the library full_rescan after a successful import.
// The returned id is stored on the plugin job before its status becomes done.
func (h *PluginHost) SetCatalogRescan(enqueue func() (int64, error)) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.catalogRescan = enqueue
	h.mu.Unlock()
}

// ImportURL records a library.import_url task and runs it. The returned row
// is the insert; the task continues after this method returns.
func (h *PluginHost) ImportURL(pluginID, rawURL string) (db.PluginJob, error) {
	plugin, ok := h.lookup(pluginID)
	if !ok || !canImport(plugin) {
		return db.PluginJob{}, ErrPluginNotFound
	}
	url := strings.TrimSpace(rawURL)
	if !plugins.ValidImportURL(url) {
		return db.PluginJob{}, ErrPluginURLInvalid
	}
	library, err := libraryRoot(h.library)
	if err != nil {
		return db.PluginJob{}, err
	}
	dest := "imports/" + plugin.ID
	payload, err := json.Marshal(struct {
		URL     string `json:"url"`
		DestDir string `json:"dest_dir"`
	}{URL: url, DestDir: dest})
	if err != nil {
		return db.PluginJob{}, err
	}
	job, err := h.store.CreatePluginJob(plugin.ID, plugins.CapabilityImportURL, string(payload))
	if err != nil {
		return db.PluginJob{}, err
	}
	go h.execute(job, plugin, url, library, dest)
	return job, nil
}

// Search runs library.search in the request and returns the items.
// The query is not stored and is not written to logs. Cancelling ctx stops the process.
func (h *PluginHost) Search(ctx context.Context, pluginID, rawQuery string, limit int) ([]plugins.SearchItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	plugin, ok := h.lookup(pluginID)
	if !ok || !canSearch(plugin) {
		return nil, ErrPluginNotFound
	}
	query := strings.TrimSpace(rawQuery)
	if !plugins.ValidSearchQuery(query) {
		return nil, ErrPluginQueryInvalid
	}
	if limit < plugins.MinSearchLimit || limit > plugins.MaxSearchLimit {
		return nil, ErrPluginLimitInvalid
	}
	id, err := newSearchID()
	if err != nil {
		return nil, err
	}
	return h.runner.Search(ctx, plugin, plugins.SearchJob{
		ID: id, Query: query, Limit: limit,
	})
}

func newSearchID() (string, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "s" + hex.EncodeToString(buf[:]), nil
}

func (h *PluginHost) lookup(id string) (plugins.Plugin, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.registry == nil {
		return plugins.Plugin{}, false
	}
	return h.registry.Get(id)
}

// GetPluginJob reads one stored task. The input URL stays on the row for the
// host and must be stripped by the HTTP layer.
func (h *PluginHost) GetPluginJob(id int64) (*db.PluginJob, error) {
	if h == nil || h.store == nil {
		return nil, ErrPluginJobNotFound
	}
	return h.store.GetPluginJob(id)
}

// ListPluginJobs reads recent tasks, optionally filtered by status.
func (h *PluginHost) ListPluginJobs(status string, limit int) ([]db.PluginJob, error) {
	if h == nil || h.store == nil {
		return []db.PluginJob{}, nil
	}
	return h.store.ListPluginJobs(status, limit)
}

// Cancel stops a pending or running task and waits until the row is terminal.
func (h *PluginHost) Cancel(ctx context.Context, id int64) (db.PluginJob, error) {
	if err := ctx.Err(); err != nil {
		return db.PluginJob{}, err
	}
	job, err := h.store.GetPluginJob(id)
	if err != nil {
		return db.PluginJob{}, err
	}
	if job == nil {
		return db.PluginJob{}, ErrPluginJobNotFound
	}
	if terminalPluginJob(job.Status) {
		return *job, ErrPluginJobNotCancellable
	}

	h.mu.Lock()
	run, running := h.runs[id]
	if !running {
		h.dropped[id] = struct{}{}
	}
	h.mu.Unlock()

	if !running {
		if _, err := h.store.FinishPluginJob(id, db.PluginJobCancelled, "", plugins.CodeCancelled, "plugin cancelled"); err != nil {
			return db.PluginJob{}, err
		}
		current, err := h.current(id)
		if err != nil {
			return db.PluginJob{}, err
		}
		if current.Status != db.PluginJobCancelled {
			return current, ErrPluginJobNotCancellable
		}
		return current, nil
	}

	run.cancel()
	select {
	case <-run.done:
	case <-ctx.Done():
		current, _ := h.current(id)
		return current, ctx.Err()
	}
	return h.current(id)
}

func (h *PluginHost) execute(job db.PluginJob, plugin plugins.Plugin, url, library, dest string) {
	ctx, cancel := context.WithCancel(context.Background())
	h.mu.Lock()
	if _, drop := h.dropped[job.ID]; drop {
		delete(h.dropped, job.ID)
		h.mu.Unlock()
		cancel()
		_, _ = h.store.FinishPluginJob(job.ID, db.PluginJobCancelled, "", plugins.CodeCancelled, "plugin cancelled")
		return
	}
	done := make(chan struct{})
	h.runs[job.ID] = &pluginRun{cancel: cancel, done: done}
	h.mu.Unlock()
	defer cancel()
	defer func() {
		h.mu.Lock()
		delete(h.runs, job.ID)
		h.mu.Unlock()
		close(done)
	}()

	ok, err := h.store.MarkPluginJobRunning(job.ID)
	if err != nil {
		log.Printf("plugin job #%d plugin=%s mark running: %v", job.ID, plugin.ID, err)
		return
	}
	if !ok {
		return
	}
	result, err := h.runner.ImportURL(ctx, plugin, plugins.ImportJob{
		ID:      strconv.FormatInt(job.ID, 10),
		URL:     url,
		Library: library,
		DestDir: dest,
		OnProgress: func(update plugins.Progress) {
			h.recordProgress(job.ID, url, update)
		},
	})
	if err != nil {
		h.finishFailure(job.ID, plugin.ID, url, err)
		return
	}
	payload, err := json.Marshal(map[string]any{"paths": result.Paths})
	if err != nil {
		h.finish(job.ID, plugin.ID, db.PluginJobError, "", plugins.CodeInternal, "plugin failed")
		return
	}
	// Paths are already inside the library. Queue the catalog job before the
	// plugin row becomes done, so a status read never sees success without
	// the rescan id when the enqueue succeeded.
	downstream, enqErr := h.queueCatalogRescan()
	if enqErr != nil {
		log.Printf("plugin job #%d plugin=%s rescan: %v", job.ID, plugin.ID, enqErr)
		h.finish(job.ID, plugin.ID, db.PluginJobDone, string(payload), "", "")
		return
	}
	ok, err = h.store.FinishPluginImport(job.ID, string(payload), downstream)
	if err != nil {
		log.Printf("plugin job #%d plugin=%s finish: %v", job.ID, plugin.ID, err)
		return
	}
	if !ok {
		log.Printf("plugin job #%d plugin=%s rescan #%d was not linked", job.ID, plugin.ID, downstream)
		return
	}
	log.Printf("plugin job #%d plugin=%s status=%s downstream=#%d", job.ID, plugin.ID, db.PluginJobDone, downstream)
}

func (h *PluginHost) queueCatalogRescan() (int64, error) {
	h.mu.Lock()
	enqueue := h.catalogRescan
	h.mu.Unlock()
	if enqueue == nil {
		return 0, errors.New("rescan enqueue unavailable")
	}
	id, err := enqueue()
	if err != nil {
		return 0, err
	}
	if id < 1 {
		return 0, errors.New("full_rescan was not queued")
	}
	return id, nil
}

func (h *PluginHost) recordProgress(id int64, url string, update plugins.Progress) {
	body := plugins.ProgressView(update, url)
	if len(body) == 0 {
		return
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return
	}
	if err := h.store.SetPluginJobProgress(id, string(payload)); err != nil {
		log.Printf("plugin job #%d progress: %v", id, err)
	}
}

func (h *PluginHost) finishFailure(id int64, pluginID, url string, cause error) {
	code := plugins.CodeInternal
	message := "plugin failed"
	var failure *plugins.Failure
	if errors.As(cause, &failure) {
		if failure.Code != "" {
			code = failure.Code
		}
		if failure.Message != "" {
			message = failure.Message
		}
	}
	message = plugins.Redact(message, url)
	status := db.PluginJobError
	if code == plugins.CodeCancelled || errors.Is(cause, context.Canceled) {
		status = db.PluginJobCancelled
		code = plugins.CodeCancelled
		if message == "" || message == "plugin failed" {
			message = "plugin cancelled"
		}
	}
	h.finish(id, pluginID, status, "", code, message)
}

func (h *PluginHost) finish(id int64, pluginID, status, result, code, message string) {
	ok, err := h.store.FinishPluginJob(id, status, result, code, message)
	if err != nil {
		log.Printf("plugin job #%d plugin=%s finish: %v", id, pluginID, err)
		return
	}
	if ok {
		log.Printf("plugin job #%d plugin=%s status=%s code=%s", id, pluginID, status, code)
	}
}

func (h *PluginHost) current(id int64) (db.PluginJob, error) {
	job, err := h.store.GetPluginJob(id)
	if err != nil {
		return db.PluginJob{}, err
	}
	if job == nil {
		return db.PluginJob{}, ErrPluginJobNotFound
	}
	return *job, nil
}

func canSearch(plugin plugins.Plugin) bool {
	for _, capability := range plugin.Capabilities {
		if capability == plugins.CapabilitySearch {
			return true
		}
	}
	return false
}

func canImport(plugin plugins.Plugin) bool {
	for _, capability := range plugin.Capabilities {
		if capability == plugins.CapabilityImportURL {
			return true
		}
	}
	return false
}

func terminalPluginJob(status string) bool {
	switch status {
	case db.PluginJobDone, db.PluginJobError, db.PluginJobCancelled:
		return true
	default:
		return false
	}
}

func libraryRoot(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", ErrPluginLibrary
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", ErrPluginLibrary
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", ErrPluginLibrary
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", ErrPluginLibrary
	}
	if !strings.HasPrefix(resolved, "/") || utf8.RuneCountInString(resolved) > maxLibraryChars {
		return "", ErrPluginLibrary
	}
	for _, r := range resolved {
		if r < 0x20 {
			return "", ErrPluginLibrary
		}
	}
	return resolved, nil
}
