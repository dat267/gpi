package server

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// publishSocket moves the freshly bound socket to its configured path. These
// cover the two properties the publish must keep on every platform: the socket
// ends up at the path, and an occupied path is never replaced. Android denies
// link(2) outright (D152), so on this host the renameat2 path is the one under
// test; on a link-capable host it is the link path. Both must satisfy these.

func TestPublishSocketMovesTheBoundSocket(t *testing.T) {
	dir := t.TempDir()
	owned := filepath.Join(dir, "bind-tmp")
	final := filepath.Join(dir, "pi.sock")
	if err := os.WriteFile(owned, []byte("bound"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := publishSocket(owned, final); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if _, err := os.Lstat(owned); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the temporary bind path is still present (err = %v)", err)
	}
	published, err := os.Lstat(final)
	if err != nil {
		t.Fatalf("nothing published at the configured path: %v", err)
	}
	// The cleanup path compares device/inode to recognise its own socket, so
	// the published path must be the very file that was bound.
	bound, err := os.Stat(final)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(published, bound) {
		t.Error("the published path is not the bound file")
	}
}

func TestPublishSocketRefusesToClobber(t *testing.T) {
	dir := t.TempDir()
	owned := filepath.Join(dir, "bind-tmp")
	final := filepath.Join(dir, "pi.sock")
	if err := os.WriteFile(owned, []byte("bound"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(final, []byte("someone else"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := publishSocket(owned, final)
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
}
