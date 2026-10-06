//go:build !windows

package settings

import (
	"os/exec"
	"syscall"
)

// detach lets the restart script outlive this process, which it stops.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
