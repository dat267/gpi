//go:build unix

package server

import (
	"io/fs"
	"syscall"
)

// identityOf extracts the device/inode identity used to prove that a socket
// path still refers to the socket this listener created.
func identityOf(stats fs.FileInfo) *fileIdentity {
	sys, ok := stats.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	return &fileIdentity{dev: uint64(sys.Dev), ino: uint64(sys.Ino)}
}
