//go:build windows

package util

import (
	"os/exec"
)

// SetProcessGroup sets the process group for the command on Windows (no-op)
func SetProcessGroup(cmd *exec.Cmd) {
	// Windows doesn't use Setpgid
}
