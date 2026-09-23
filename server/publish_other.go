//go:build !linux

package server

import "errors"

// renameNoReplace reports that the no-replace rename is unavailable on this
// platform, so publishSocket falls through to a plain rename (D152).
func renameNoReplace(oldPath, newPath string) error {
	return errors.ErrUnsupported
}
