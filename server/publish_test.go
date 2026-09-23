package server

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// publishSocket moves the freshly bound socket to its configured path. Both of
// its mechanisms are exercised here on every host: linking where the platform
// allows it, and the rename fallback that Android needs (D152).
//
// The fallback is what makes this worth a table — Android denies link(2) to the
// app domain, and a test that only ever ran where linking works let a completely
// broken bind ship. A host that cannot link naturally exercises the fallback; a
// host that can is made to by passing a refusing link function.

// refusingLink stands in for a platform that denies link(2) (EPERM on Android)
// without also meaning "the path is occupied", so the fallback is taken.
func refusingLink(string, string) error { return syscall.EPERM }

func publishMechanisms() []struct {
	name string
	link func(oldname, newname string) error
} {
	return []struct {
		name string
		link func(oldname, newname string) error
	}{
		{"link where the platform allows it", os.Link},
		{"the fallback where link is refused", refusingLink},
	}
}

func TestPublishSocketMovesTheBoundSocket(t *testing.T) {
	for _, mechanism := range publishMechanisms() {
		t.Run(mechanism.name, func(t *testing.T) {
			dir := t.TempDir()
			owned := filepath.Join(dir, "bind-tmp")
			final := filepath.Join(dir, "pi.sock")
			if err := os.WriteFile(owned, []byte("bound"), 0o600); err != nil {
				t.Fatal(err)
			}
			bound, err := os.Stat(owned)
			if err != nil {
				t.Fatal(err)
			}

			if err := publishSocket(owned, final, mechanism.link); err != nil {
				t.Fatalf("publish: %v", err)
			}
			// The caller removes the temporary name afterwards; whether it
			// still exists here depends on the mechanism, which is why the
			// removal is not part of the publish.
			if err := removePath(owned); err != nil {
				t.Fatalf("remove temporary bind path: %v", err)
			}

			published, err := os.Stat(final)
			if err != nil {
				t.Fatalf("nothing published at the configured path: %v", err)
			}
			// The cleanup path compares device/inode to recognise its own
			// socket, so the published path must be the very file that was
			// bound — not a copy of it.
			if !os.SameFile(bound, published) {
				t.Error("the published path is not the bound file")
			}
			if _, err := os.Lstat(owned); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the temporary bind path survived the cleanup (err = %v)", err)
			}
		})
	}
}

func TestPublishSocketRefusesToClobber(t *testing.T) {
	for _, mechanism := range publishMechanisms() {
		t.Run(mechanism.name, func(t *testing.T) {
			dir := t.TempDir()
			owned := filepath.Join(dir, "bind-tmp")
			final := filepath.Join(dir, "pi.sock")
			if err := os.WriteFile(owned, []byte("bound"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(final, []byte("someone else"), 0o600); err != nil {
				t.Fatal(err)
			}

			err := publishSocket(owned, final, mechanism.link)
			if err == nil {
				t.Fatal("publish replaced an occupied path")
			}
			if !errors.Is(err, fs.ErrExist) {
				t.Errorf("err = %v, want a file-exists failure", err)
			}
			content, readErr := os.ReadFile(final)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(content) != "someone else" {
				t.Errorf("the occupied path was modified: %q", content)
			}
			if _, statErr := os.Lstat(owned); statErr != nil {
				t.Errorf("the bound socket was consumed by a refused publish: %v", statErr)
			}
		})
	}
}
