//go:build unix

package coding

import (
	"os/exec"
	"syscall"
)

// configureDetachedCommand starts the command in its own process group so a
// kill can target the whole tree (upstream's `detached: true`, which it sets on
// every platform but Windows).
func configureDetachedCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessTreePlatform kills the process group with SIGKILL, falling back to
// the single process if the group kill fails (upstream killProcessTree, the
// non-win32 branch).
func killProcessTreePlatform(pid int) {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
