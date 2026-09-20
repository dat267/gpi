//go:build unix

package coding

import (
	"os"
	"syscall"
)

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
		return stat.Ctim.Sec*1e9 + stat.Ctim.Nsec
	}
	return 0
}
