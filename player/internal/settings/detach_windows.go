//go:build windows

package settings

import (
	"os/exec"
	"syscall"
)

// detach lets the restart script outlive this process, which it stops.
// CREATE_NO_WINDOW, not DETACHED_PROCESS: PowerShell started without any
// console exited without running the script.
func detach(cmd *exec.Cmd) {
	const createNewProcessGroup = 0x00000200
	const createNoWindow = 0x08000000
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | createNoWindow,
		HideWindow:    true,
	}
}
