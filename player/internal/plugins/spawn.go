package plugins

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type running struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
}

var droppedEnv = map[string]struct{}{
	"MUSIK_PASSWORD":           {},
	"MUSIK_API_TOKEN":          {},
	"MUSIK_SESSION_SECRET":     {},
	"MUSIK_OIDC_CLIENT_SECRET": {},
	"MUSIK_DATABASE_URL":       {},
	"LD_PRELOAD":               {},
	"LD_AUDIT":                 {},
}

func startProcess(executable, arg, secret string, grace time.Duration) (*running, error) {
	cmd := exec.Command(executable, arg)
	cmd.Dir = filepath.Dir(executable)
	cmd.Env = childEnv(secret)
	cmd.WaitDelay = grace
	configureProcess(cmd)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &running{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

func childEnv(secret string) []string {
	src := os.Environ()
	out := make([]string, 0, len(src))
	for _, entry := range src {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, drop := droppedEnv[key]; drop {
			continue
		}
		if len(secret) >= 8 && strings.Contains(value, secret) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func readStderr(r io.Reader, max int, secret string) string {
	var buf bytes.Buffer
	tmp := make([]byte, 4096)
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			remain := max - buf.Len()
			if remain > 0 {
				chunk := tmp[:n]
				if len(chunk) > remain {
					chunk = chunk[:remain]
				}
				buf.Write(chunk)
			}
		}
		if err != nil {
			break
		}
	}
	return Redact(buf.String(), secret)
}

func supervise(ctx context.Context, cmd *exec.Cmd, grace time.Duration, reason func() error) (int, error) {
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	var kill <-chan time.Time
	var killTimer *time.Timer
	defer func() {
		if killTimer != nil {
			killTimer.Stop()
		}
	}()

	ctxDone := ctx.Done()
	var cause error
	for {
		select {
		case err := <-done:
			return exitStatus(err), cause
		case <-ctxDone:
			ctxDone = nil
			if cause == nil {
				if reason != nil {
					cause = reason()
				}
				if cause == nil {
					cause = ctx.Err()
				}
			}
			if cmd.Process != nil && cmd.Process.Pid > 0 {
				_ = terminateProcess(cmd.Process.Pid)
			}
			killTimer = time.NewTimer(grace)
			kill = killTimer.C
		case <-kill:
			if cmd.Process != nil && cmd.Process.Pid > 0 {
				_ = killProcess(cmd.Process.Pid)
			}
			kill = nil
		}
	}
}

func exitStatus(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
