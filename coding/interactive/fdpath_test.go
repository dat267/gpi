package interactive

import (
	"os"
	"path/filepath"
	"testing"
)

// writeExecutable drops a runnable file so exec.LookPath can find it.
func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// fd is the preferred binary; fdfind is Debian's name for the same thing, and
// accepting it is the difference between @-file completion working and being
// silently unavailable there.
func TestResolveAutocompleteFdPath(t *testing.T) {
	t.Run("fdfind alone", func(t *testing.T) {
		dir := t.TempDir()
		writeExecutable(t, filepath.Join(dir, "fdfind"))
		t.Setenv("PATH", dir)
		if got := ResolveAutocompleteFdPath(); got != filepath.Join(dir, "fdfind") {
			t.Errorf("resolved %q, want the fdfind fallback", got)
		}
	})

	t.Run("fd wins", func(t *testing.T) {
		dir := t.TempDir()
		writeExecutable(t, filepath.Join(dir, "fd"))
		writeExecutable(t, filepath.Join(dir, "fdfind"))
		t.Setenv("PATH", dir)
		if got := ResolveAutocompleteFdPath(); got != filepath.Join(dir, "fd") {
			t.Errorf("resolved %q, want fd to take precedence", got)
		}
	})

	t.Run("neither", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if got := ResolveAutocompleteFdPath(); got != "" {
			t.Errorf("resolved %q, want none", got)
		}
	})
}
