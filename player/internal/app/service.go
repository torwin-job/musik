// Package app coordinates application lifecycle concerns that span domain packages.
package app

import (
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/torwin-job/musik/player/internal/config"
	"github.com/torwin-job/musik/player/internal/db"
	"github.com/torwin-job/musik/player/internal/index"
	"github.com/torwin-job/musik/player/internal/playback"
	"github.com/torwin-job/musik/player/internal/taste"
)

var reloadKinds = map[string]bool{
	"embed": true, "full_rescan": true, "clusters": true,
	"daily": true, "album_tips": true, "mix_pack": true, "train_ranker": true,
}

type Service struct {
	Cfg   config.Config
	Store *db.Store
	Idx   *index.Index
	Taste *taste.Profile
	Play  *playback.Engine
	HTTP  *http.Client
	// Catalog is the process-wide embedding catalog. Profile indexes attach
	// to it; they do not each load their own copy.
	Catalog *index.CatalogSource
	// Plugins discovers local executables and runs their import tasks.
	Plugins *PluginHost
}

func New(cfg config.Config, store *db.Store, idx *index.Index, tp *taste.Profile, play *playback.Engine, client *http.Client) *Service {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	service := &Service{Cfg: cfg, Store: store, Idx: idx, Taste: tp, Play: play, HTTP: client, Catalog: index.NewCatalogSource(store)}
	service.Plugins = newPluginHost(cfg, store)
	return service
}

func (s *Service) Reload() error {
	cat, err := s.Catalog.Refresh()
	if err != nil {
		return err
	}
	if s.Play != nil {
		if err := ReloadProfile(s.Store, s.Idx, s.Taste, s.Play, cat); err != nil {
			return err
		}
		log.Printf("reloaded index n=%d dim=%d maturity=%s", s.Idx.Size(), s.Idx.Dim(),
			s.Taste.Maturity(s.Cfg.ProfileFormingAt, s.Cfg.ProfileReadyAt))
	} else {
		log.Printf("reloaded catalog n=%d dim=%d", cat.Size(), cat.Dim())
	}
	s.publishEntityVectors(cat)
	return nil
}

// ReloadProfile attaches cat to one profile's index and refreshes that
// profile's taste vector, transitions, and ranker. The shared catalog and
// installation entity vectors stay with Service.Reload.
func ReloadProfile(store *db.Store, idx *index.Index, tp *taste.Profile, play *playback.Engine, cat *index.Catalog) error {
	stats, err := store.LoadTrackStats()
	if err != nil {
		return err
	}
	idx.Attach(cat, stats)
	pos, neg, _ := store.ListenSignalCounts()
	if blob, err := store.LatestProfile("global"); err == nil && len(blob) > 0 {
		v := index.BytesToFloat32(blob)
		if len(v) == idx.Dim() {
			tp.SetWithMeta(v, pos, neg, "online_ema")
		}
	} else if !tp.Ready() {
		tp.SetWithMeta(idx.Centroid(), pos, neg, "centroid")
	} else {
		tp.SetCounts(pos, neg)
	}
	if play != nil {
		play.ReloadTransitions()
		play.ReloadRanker()
	}
	return nil
}

func (s *Service) publishEntityVectors(cat *index.Catalog) {
	if s.Store == nil || s.Store.ReadOnly || cat == nil {
		return
	}
	idx := index.New(s.Cfg)
	idx.Attach(cat, nil)
	rows := idx.EntityVectorRows(db.EntityModelVersion)
	store := s.Store
	go func() {
		if err := store.ReplaceEntityVectors(db.EntityModelVersion, rows); err != nil {
			log.Printf("entity vectors refresh: %v", err)
		}
	}()
}

func (s *Service) WorkerHealthy() bool {
	res, err := s.HTTP.Get(strings.TrimRight(s.Cfg.WorkerURL, "/") + "/jobs")
	if err != nil {
		return false
	}
	defer res.Body.Close()
	return res.StatusCode < 500
}

func (s *Service) EnsureWorker() {
	if !s.Cfg.WorkerAutostart {
		return
	}
	if s.WorkerHealthy() {
		log.Printf("worker already up at %s", s.Cfg.WorkerURL)
		return
	}
	dataDir := filepath.Dir(filepath.Dir(s.Cfg.DBPath))
	projectRoot := filepath.Dir(dataDir)
	if root := os.Getenv("MUSIK_ROOT"); root != "" {
		projectRoot = root
	}
	python := findPython()
	if python == "" {
		venvPython := filepath.Join(projectRoot, ".venv", "bin", "python")
		if _, err := os.Stat(venvPython); err == nil {
			python = venvPython
		}
	}
	if python == "" {
		log.Printf("worker autostart: no python found; start `musik worker` manually")
		return
	}
	musikBin := filepath.Join(filepath.Dir(python), "musik")
	var cmd *exec.Cmd
	if _, err := os.Stat(musikBin); err == nil {
		cmd = exec.Command(musikBin, "worker")
	} else {
		cmd = exec.Command(python, "-m", "musik", "worker")
	}
	cmd.Dir = projectRoot
	cmd.Env = make([]string, 0, len(os.Environ())+10)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "MUSIK_DB_PATH=") || strings.HasPrefix(entry, "MUSIK_DATABASE_URL=") ||
			strings.HasPrefix(entry, "MUSIK_MULTI_USER=") || strings.HasPrefix(entry, "MUSIK_API_TOKEN=") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env,
		"MUSIK_ROOT="+projectRoot,
		"MUSIK_MULTI_USER="+strconv.FormatBool(s.Cfg.MultiUser),
		"MUSIK_LIBRARY="+s.Cfg.Library,
		"MUSIK_PLAYER_RELOAD_URL=http://127.0.0.1"+normalizeAddr(s.Cfg.Addr)+"/api/reload",
	)
	if s.Cfg.DatabaseURL != "" {
		cmd.Env = append(cmd.Env, "MUSIK_DATABASE_URL="+s.Cfg.DatabaseURL)
	} else {
		cmd.Env = append(cmd.Env, "MUSIK_DB_PATH="+s.Cfg.DBPath)
	}
	if s.Cfg.APIToken != "" {
		cmd.Env = append(cmd.Env, "MUSIK_API_TOKEN="+s.Cfg.APIToken)
	}
	logPath := filepath.Join(dataDir, "worker.log")
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		defer file.Close()
		cmd.Stdout = file
		cmd.Stderr = file
	}
	if err := cmd.Start(); err != nil {
		log.Printf("worker autostart failed: %v", err)
		return
	}
	log.Printf("worker autostarted pid=%d log=%s", cmd.Process.Pid, logPath)
	go func() { _ = cmd.Wait() }()
	for range 20 {
		time.Sleep(250 * time.Millisecond)
		if s.WorkerHealthy() {
			log.Printf("worker ready at %s", s.Cfg.WorkerURL)
			return
		}
	}
	log.Printf("worker autostart: still not healthy at %s (check %s)", s.Cfg.WorkerURL, logPath)
}

func (s *Service) WatchJobs(reload func() error) {
	if reload == nil {
		reload = s.Reload
	}
	after := time.Now().UTC().Format(time.RFC3339Nano)
	ticker := time.NewTicker(3 * time.Second)
	go func() {
		for range ticker.C {
			jobs, err := s.Store.ListDoneJobsAfter(after, 50)
			if err != nil || len(jobs) == 0 {
				continue
			}
			needReload := false
			for _, job := range jobs {
				after = job.UpdatedAt
				if reloadKinds[job.Kind] {
					needReload = true
					log.Printf("job #%d kind=%s done → reload index", job.ID, job.Kind)
				}
			}
			if needReload {
				if err := reload(); err != nil {
					log.Printf("auto-reload after job: %v", err)
				}
			}
		}
	}()
}

func normalizeAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return addr
	}
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return ":" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	return addr
}

func findPython() string {
	candidates := []string{os.Getenv("MUSIK_PYTHON")}
	if root := os.Getenv("MUSIK_ROOT"); root != "" {
		candidates = append(candidates, filepath.Join(root, ".venv", "bin", "python"))
	}
	cwd, _ := os.Getwd()
	candidates = append(candidates,
		filepath.Join(cwd, ".venv", "bin", "python"),
		filepath.Join(cwd, "..", ".venv", "bin", "python"),
		".venv/bin/python", "python3", "python",
	)
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if path, err := exec.LookPath(candidate); err == nil {
			return path
		}
		if _, err := os.Stat(candidate); err == nil {
			absolute, err := filepath.Abs(candidate)
			if err == nil {
				return absolute
			}
			return candidate
		}
	}
	return ""
}
