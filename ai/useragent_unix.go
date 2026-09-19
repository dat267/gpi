//go:build linux

package ai

import (
	"runtime"
	"syscall"
)

func runtimeGOOS() string   { return runtime.GOOS }
func runtimeGOARCH() string { return runtime.GOARCH }

// kernelRelease reads the kernel release (Node's os.release() on Linux).
func kernelRelease() string {
	var uts syscall.Utsname
	if err := syscall.Uname(&uts); err != nil {
		return ""
	}
	out := make([]byte, 0, len(uts.Release))
	for _, b := range uts.Release {
		if b == 0 {
			break
		}
		out = append(out, byte(b))
	}
	return string(out)
}
