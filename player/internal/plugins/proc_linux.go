//go:build linux

package plugins

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
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
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	text := string(data)
	end := strings.LastIndex(text, ")")
	if end < 0 || end+2 >= len(text) {
		return true
	}
	state := text[end+2]
	return state != 'Z' && state != 'X'
}
