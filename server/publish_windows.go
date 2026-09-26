//go:build windows

package server

import (
	"fmt"
	"io/fs"

	"golang.org/x/sys/windows"
)

// renameNoReplace renames oldPath to newPath, failing if newPath exists — the
// Windows form of the Linux renameat2(RENAME_NOREPLACE) fallback (D152).
//
// Without it the platform fell through to os.Rename, which replaces an occupied
// destination, so publishing a socket could clobber a file that appeared at the
// configured path between the bind and the publish. link(2) refuses
// ERROR_ALREADY_EXISTS on Windows the same way it does elsewhere, but the
// fallback exists precisely for the hosts where the link is refused, and on
// Windows that is the only path left.
//
// MoveFileEx without MOVEFILE_REPLACE_EXISTING fails with ERROR_ALREADY_EXISTS
// when the destination is occupied; MOVEFILE_COPY_ALLOWED keeps a cross-volume
// move working. The destination-exists errno is reported as fs.ErrExist so
// publishSocket's "never clobber" branch sees the same error as on Linux.
func renameNoReplace(oldPath, newPath string) error {
	from, err := windows.UTF16PtrFromString(oldPath)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(newPath)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_COPY_ALLOWED); err != nil {
		if err == windows.ERROR_ALREADY_EXISTS || err == windows.ERROR_FILE_EXISTS {
			return fmt.Errorf("rename %s -> %s: %w", oldPath, newPath, fs.ErrExist)
		}
		return err
	}
	return nil
}
