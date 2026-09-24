package coding

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// D161: the port sweeps its own temp output files instead of leaving them to the
// OS. These pin the three properties that matter: this port's files are the only
// ones touched, the oldest go first, and the file being written now survives.
func TestSweepTempOutputFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	make := func(name string, size int, age time.Duration) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, bytes.Repeat([]byte("x"), size), 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := now.Add(-age)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		return path
	}
	exists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}

	// Names run the opposite way to age, so a sweep that trusts directory order
	// (ReadDir is alphabetical) deletes the wrong file and fails below.
	oldest := make("pi-bash-zzz-oldest.log", 100, 3*time.Hour)
	older := make("pi-bash-mmm-middle.log", 100, 2*time.Hour)
	newer := make("pi-bash-aaa-newest.log", 100, time.Hour)
	fresh := make("pi-bash-bbb-fresh.log", 0, 0)
	// Not this port's scratch: a different tool's log, a file without the
	// suffix, and a directory that happens to match the name pattern.
	stranger := make("other-tool.log", 100, 4*time.Hour)
	noSuffix := make("pi-bash-not-a-log", 100, 4*time.Hour)
	matchingDir := filepath.Join(dir, "pi-bash-iam-a-dir.log")
	if err := os.Mkdir(matchingDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Under budget: nothing is touched.
	if removed, freed := sweepTempOutputFiles(dir, fresh, 300); removed != 0 || freed != 0 {
		t.Fatalf("under budget: removed=%d freed=%d, want 0/0", removed, freed)
	}
	for _, path := range []string{oldest, older, newer, fresh} {
		if !exists(path) {
			t.Fatalf("%s was removed while under budget", filepath.Base(path))
		}
	}

	// Budget 200 of 300 bytes: the oldest goes, the rest stay.
	removed, freed := sweepTempOutputFiles(dir, fresh, 200)
	if removed != 1 || freed != 100 {
		t.Fatalf("sweep = %d files / %d bytes, want 1/100", removed, freed)
	}
	if exists(oldest) {
		t.Error("the oldest file survived")
	}
	for _, path := range []string{older, newer, fresh} {
		if !exists(path) {
			t.Errorf("%s was removed but it was not the oldest", filepath.Base(path))
		}
	}

	// Budget 0: everything of this port's goes, except the file being written.
	removed, freed = sweepTempOutputFiles(dir, fresh, 0)
	if removed != 2 || freed != 200 {
		t.Fatalf("sweep to zero = %d files / %d bytes, want 2/200", removed, freed)
	}
	for _, path := range []string{older, newer} {
		if exists(path) {
			t.Errorf("%s survived a zero budget", filepath.Base(path))
		}
	}
	if !exists(fresh) {
		t.Error("the file being written was swept")
	}
	// Anything that is not this port's scratch is still there.
	if !exists(stranger) {
		t.Error("another tool's log was swept")
	}
	if !exists(noSuffix) {
		t.Error("a file without the .log suffix was swept")
	}
	if !exists(matchingDir) {
		t.Error("a directory matching the name pattern was swept")
	}
}
