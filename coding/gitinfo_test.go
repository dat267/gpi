package coding

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Round 117 tests: git metadata discovery, branch resolution, and deprecation
// warnings.

func TestResolveGitBranch(t *testing.T) {
	if _, err := execLookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	run := func(args ...string) {
		command := exec.Command("git", args...)
		command.Dir = repo
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	run("init", "-q", "-b", "main", ".")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")

	if branch := ResolveGitBranch(repo); branch != "main" {
		t.Fatalf("branch = %q", branch)
	}

	// Detached HEAD resolves to empty.
	run("checkout", "-q", "--detach", "HEAD")
	if branch := ResolveGitBranch(repo); branch != "" {
		t.Fatalf("branch = %q", branch)
	}

	// A non-repository directory resolves to empty.
	if branch := ResolveGitBranch(t.TempDir()); branch != "" {
		t.Fatalf("branch = %q", branch)
	}
}

func TestWarnDeprecation(t *testing.T) {
	WarnDeprecation("old thing")
	WarnDeprecation("old thing") // deduplicated
	WarnDeprecation("other thing")
	warnings := TakeDeprecationWarnings()
	if strings.Join(warnings, ",") != "old thing,other thing" {
		t.Fatalf("warnings = %v", warnings)
	}
	// After the take, the state is cleared and warnings emit again.
	WarnDeprecation("old thing")
	if warnings := TakeDeprecationWarnings(); len(warnings) != 1 || warnings[0] != "old thing" {
		t.Fatalf("warnings = %v", warnings)
	} else {
		TakeDeprecationWarnings()
	}
}

// execLookPath reports whether a command is on the PATH.
func execLookPath(name string) (string, error) { return exec.LookPath(name) }
