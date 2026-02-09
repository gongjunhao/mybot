//go:build windows

package codex

import (
	"os/exec"
)

func setSysProcAttr(cmd *exec.Cmd) {
	// Windows doesn't use Setpgid
}

func killProcessGroup(pid int) error {
	// On Windows, just return nil and let the caller use Process.Signal
	return nil
}
