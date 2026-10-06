package settings

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// RestartCommand returns the command that restarts worker and player with the
// values saved in .env, or nil when this host has no known launcher.
//
// The launcher reads .env itself, so the child must not inherit this
// process's MUSIK_* variables: "real environment wins over .env" would bring
// the old values back. MUSIK_ROOT and MUSIK_SUPERVISOR are kept.
func RestartCommand(root string) *exec.Cmd {
	var cmd *exec.Cmd
	switch {
	case os.Getenv("MUSIK_SUPERVISOR") == "systemd":
		// systemd re-reads EnvironmentFile=.env; --no-block queues the job, so
		// stopping this process does not cancel it.
		cmd = exec.Command("systemctl", "--user", "--no-block", "restart",
			"musik-worker.service", "musik-player.service")
	case runtime.GOOS == "windows":
		script := filepath.Join(root, "scripts", "start-musik.ps1")
		if _, err := os.Stat(script); err != nil {
			return nil
		}
		cmd = exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass",
			"-File", script, "-Restart")
	default:
		script := filepath.Join(root, "scripts", "musik.sh")
		if _, err := os.Stat(script); err != nil {
			return nil
		}
		cmd = exec.Command("sh", script, "restart")
	}
	cmd.Dir = root
	cmd.Env = cleanEnv(os.Environ())
	detach(cmd)
	return cmd
}

// OpenRestartLog is where the launcher's output goes (data/restart.log), so
// a restart that did not happen leaves a reason behind.
func OpenRestartLog(root string) *os.File {
	dir := filepath.Join(root, "data")
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.OpenFile(filepath.Join(dir, "restart.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	return f
}

func cleanEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key := strings.ToUpper(strings.SplitN(kv, "=", 2)[0])
		if strings.HasPrefix(key, "MUSIK_") && key != "MUSIK_ROOT" && key != "MUSIK_SUPERVISOR" {
			continue
		}
		out = append(out, kv)
	}
	return out
}
