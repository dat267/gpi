//go:build linux

package server

import "golang.org/x/sys/unix"

// renameNoReplace renames oldPath to newPath, failing if newPath exists. It is
// the fallback publishSocket uses where link(2) is refused (D152): it keeps both
// of link's properties — the operation is atomic, and an occupied destination is
// never replaced.
func renameNoReplace(oldPath, newPath string) error {
	return unix.Renameat2(unix.AT_FDCWD, oldPath, unix.AT_FDCWD, newPath, unix.RENAME_NOREPLACE)
}
