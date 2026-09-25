//go:build linux

package interactive

// The startup timing instrumentation (coding/timings.go, upstream
// core/timings.ts) is a complete port with tests and had no production caller,
// so PI_TIMING=1 printed nothing and the port's boot was unmeasurable — the one
// part of a stutter report the UI-loop stall log cannot see.

import (
	"testing"
	"time"
)

// Upstream marks its boot phases in main.ts and prints the table right before
// entering the run loop; the port does the same, with the labels that exist here.
func TestStartupTimingsArePrinted(t *testing.T) {
	t.Setenv("PI_TIMING", "1")
	session := startPier(t)
	if !session.waitForOutput("Startup Timings: main", 30*time.Second) {
		t.Fatalf("no startup timings:\n%s", session.output())
	}
	for _, label := range []string{
		"parseArgs",
		"initTheme",
		"createSessionManager",
		"createRuntime",
		"resolveProjectTrust",
		"resolveModelScope",
		"prepareInitialMessage",
		"createAgentSession",
		"newApp",
	} {
		if !session.waitForOutput(label, time.Second) {
			t.Errorf("timings are missing %q:\n%s", label, session.output())
		}
	}
}

// Opt-in, like upstream: no PI_TIMING, no table.
func TestStartupTimingsAreOptIn(t *testing.T) {
	session := startPier(t)
	if !session.waitForOutput("Press ctrl+o", 30*time.Second) {
		t.Fatalf("pier did not start:\n%s", session.output())
	}
	if session.waitForOutput("Startup Timings", time.Second) {
		t.Fatalf("timings printed without PI_TIMING:\n%s", session.output())
	}
}
