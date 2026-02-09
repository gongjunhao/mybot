//go:build unix

package util

import (
	"os/exec"
	"syscall"
)

// SetProcessGroup sets the process group for the command on Unix systems
func SetProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
