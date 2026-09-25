//go:build darwin || freebsd || netbsd

package coding

import (
	"os"
	"syscall"
)

// Darwin and the BSDs name the inode change time Ctimespec; the rest of the
// unix family (stat_unix.go) names it Ctim.

// statDeviceInode returns the device and inode numbers for a file.
func statDeviceInode(info os.FileInfo) (uint64, uint64) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Dev), uint64(stat.Ino)
	}
	return 0, 0
}

// statChangeTimeNano returns the inode change time in nanoseconds.
func statChangeTimeNano(info os.FileInfo) int64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Ctimespec.Sec*1e9 + stat.Ctimespec.Nsec
	}
	return 0
}
