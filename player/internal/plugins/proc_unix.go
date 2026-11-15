//go:build unix && !linux

package plugins

import (
	"os"
	"os/exec"
	"syscall"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateProcess(pid int) error {
	return signalGroup(pid, syscall.SIGTERM)
}

func killProcess(pid int) error {
	return signalGroup(pid, syscall.SIGKILL)
}

func signalGroup(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return os.ErrInvalid
	}
	return syscall.Kill(-pid, sig)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
