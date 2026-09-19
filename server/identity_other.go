//go:build !unix

package server

import "io/fs"

// identityOf is unavailable off Unix; path identity checks then fall back to
// the socket-mode check alone (Node reports dev/ino as equal values there).
func identityOf(stats fs.FileInfo) *fileIdentity {
	if stats.Mode()&fs.ModeSocket == 0 {
		return nil
	}
	return &fileIdentity{}
}
