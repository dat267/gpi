//go:build !unix

package coding

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// configureDetachedCommand is a no-op off unix: upstream detaches the shell
// child only on non-Windows (`detached: process.platform !== "win32"`), so on
// Windows there is no process group to configure.
func configureDetachedCommand(cmd *exec.Cmd) {}

// killProcessTreePlatform kills the process and its children with taskkill,
// resolved under System32 so cleanup does not depend on PATH (upstream
// killProcessTree, the win32 branch). Errors are ignored — the process may
// already be gone — and a failed spawn cannot crash the caller.
func killProcessTreePlatform(pid int) {
	systemRoot := os.Getenv("SystemRoot")
	if systemRoot == "" {
		systemRoot = `C:\Windows`
	}
	taskkill := filepath.Join(systemRoot, "System32", "taskkill.exe")
	_ = exec.Command(taskkill, "/F", "/T", "/PID", strconv.Itoa(pid)).Run()
}
