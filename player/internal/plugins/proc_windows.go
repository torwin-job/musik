//go:build windows

package plugins

import (
	"os"
	"os/exec"
)

func configureProcess(cmd *exec.Cmd) {}

func terminateProcess(pid int) error {
	return killProcess(pid)
}

func killProcess(pid int) error {
	if pid <= 0 {
		return os.ErrInvalid
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}

func processAlive(pid int) bool {
	return false
}
