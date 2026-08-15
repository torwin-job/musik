package api

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var reloadKinds = map[string]bool{
	"embed": true, "full_rescan": true, "clusters": true,
	"daily": true, "album_tips": true, "mix_pack": true,
}

// EnsureWorker starts `musik worker` if WorkerAutostart and health check fails.
func (s *Server) EnsureWorker() {
	if !s.Cfg.WorkerAutostart {
		return
	}
	if s.workerHealthy() {
		log.Printf("worker already up at %s", s.Cfg.WorkerURL)
		return
	}
	dataDir := filepath.Dir(filepath.Dir(s.Cfg.DBPath)) // .../data
	projectRoot := filepath.Dir(dataDir)                // repo root
	if r := os.Getenv("MUSIK_ROOT"); r != "" {
		projectRoot = r
	}
	python := findPython()
	if python == "" {
		// last try: project venv
		venvPy := filepath.Join(projectRoot, ".venv", "bin", "python")
		if _, err := os.Stat(venvPy); err == nil {
			python = venvPy
		}
	}
	if python == "" {
		log.Printf("worker autostart: no python found; start `musik worker` manually")
		return
	}
	// Prefer installed console script next to venv python; fallback to -m musik
	musikBin := filepath.Join(filepath.Dir(python), "musik")
	var cmd *exec.Cmd
	if _, err := os.Stat(musikBin); err == nil {
		cmd = exec.Command(musikBin, "worker")
	} else {
		cmd = exec.Command(python, "-m", "musik", "worker")
	}
	cmd.Dir = projectRoot
	env := append(os.Environ(),
		"MUSIK_ROOT="+projectRoot,
		"MUSIK_DB_PATH="+s.Cfg.DBPath,
		"MUSIK_LIBRARY="+s.Cfg.Library,
		"MUSIK_PLAYER_RELOAD_URL=http://127.0.0.1"+normalizeAddr(s.Cfg.Addr)+"/api/reload",
	)
	if s.Cfg.APIToken != "" {
		env = append(env, "MUSIK_API_TOKEN="+s.Cfg.APIToken)
	}
	cmd.Env = env
	logPath := filepath.Join(dataDir, "worker.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err == nil {
		cmd.Stdout = f
		cmd.Stderr = f
	}
	if err := cmd.Start(); err != nil {
		log.Printf("worker autostart failed: %v", err)
		return
	}
	log.Printf("worker autostarted pid=%d log=%s", cmd.Process.Pid, logPath)
	go func() { _ = cmd.Wait() }()
	// wait briefly for listen
	for i := 0; i < 20; i++ {
		time.Sleep(250 * time.Millisecond)
		if s.workerHealthy() {
			log.Printf("worker ready at %s", s.Cfg.WorkerURL)
			return
		}
	}
	log.Printf("worker autostart: still not healthy at %s (check %s)", s.Cfg.WorkerURL, logPath)
}

func (s *Server) workerHealthy() bool {
	res, err := s.HTTP.Get(strings.TrimRight(s.Cfg.WorkerURL, "/") + "/jobs")
	if err != nil {
		return false
	}
	defer res.Body.Close()
	return res.StatusCode < 500
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
		".venv/bin/python",
		"python3",
		"python",
	)
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if path, err := exec.LookPath(c); err == nil {
			return path
		}
		if _, err := os.Stat(c); err == nil {
			abs, err := filepath.Abs(c)
			if err == nil {
				return abs
			}
			return c
		}
	}
	return ""
}

// WatchJobs polls SQLite for completed jobs and reloads the index when needed.
func (s *Server) WatchJobs() {
	after := time.Now().UTC().Format(time.RFC3339Nano)
	ticker := time.NewTicker(3 * time.Second)
	go func() {
		for range ticker.C {
			jobs, err := s.Store.ListDoneJobsAfter(after, 50)
			if err != nil || len(jobs) == 0 {
				continue
			}
			needReload := false
			for _, j := range jobs {
				after = j.UpdatedAt
				if reloadKinds[j.Kind] {
					needReload = true
					log.Printf("job #%d kind=%s done → reload index", j.ID, j.Kind)
				}
			}
			if needReload {
				if err := s.Reload(); err != nil {
					log.Printf("auto-reload after job: %v", err)
				}
			}
		}
	}()
}

// ensureWorkerBeforeEnqueue tries to bring worker up before proxying a job.
func (s *Server) ensureWorkerBeforeEnqueue() {
	if s.workerHealthy() {
		return
	}
	s.EnsureWorker()
}
