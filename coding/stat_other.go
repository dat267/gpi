//go:build !unix

package coding

import "os"

// statDeviceInode returns zero device/inode numbers on platforms that do not
// expose them.
func statDeviceInode(info os.FileInfo) (uint64, uint64) {
	return 0, 0
}

// statChangeTimeNano falls back to the modification time.
func statChangeTimeNano(info os.FileInfo) int64 {
	return info.ModTime().UnixNano()
}
