package coding

import (
	"os/exec"
	"strings"
	"sync"
)

// Port of the remaining portable pieces of core/footer-data-provider.ts (the
// git metadata discovery already lives in contextfiles.go) and
// utils/deprecation.ts. The footer's file watchers, extension statuses, and
// chalk coloring are TUI/extension mechanics and stay out of scope (D41).

// ResolveGitBranch asks git for the current branch. Empty on detached HEAD or
// when git is unavailable.
func ResolveGitBranch(repoDir string) string {
	command := exec.Command("git", "--no-optional-locks", "symbolic-ref", "--quiet", "--short", "HEAD")
	command.Dir = repoDir
	output, err := command.Output()
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(output))
	return branch
}

// Deprecation warns once per message (port of utils/deprecation.ts without the
// chalk coloring).

var (
	deprecationMu       sync.Mutex
	deprecationEmitted  = map[string]bool{}
	deprecationWarnings []string
)

// WarnDeprecation emits a deprecation warning once per message.
func WarnDeprecation(message string) {
	deprecationMu.Lock()
	defer deprecationMu.Unlock()
	if deprecationEmitted[message] {
		return
	}
	deprecationEmitted[message] = true
	deprecationWarnings = append(deprecationWarnings, message)
}

// TakeDeprecationWarnings returns and clears the emitted warnings (test
// wiring; upstream logs to stderr).
func TakeDeprecationWarnings() []string {
	deprecationMu.Lock()
	defer deprecationMu.Unlock()
	warnings := deprecationWarnings
	deprecationWarnings = nil
	deprecationEmitted = map[string]bool{}
	return warnings
}
