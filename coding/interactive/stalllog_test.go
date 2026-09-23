package interactive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStallLogRecordsSlowPhases covers PIER_STALL_MS: a UI-loop phase above the
// threshold is recorded with the goroutine stacks, a fast one is not, and the
// log is disabled by default.
func TestStallLogRecordsSlowPhases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pier-stall.log")
	wiring := &RunWiring{StallLogPath: path, StallLogThreshold: time.Millisecond}

	func() {
		defer wiring.phase("render")()
		time.Sleep(5 * time.Millisecond)
	}()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("stall log not written: %v", err)
	}
	log := string(data)
	if !strings.Contains(log, `slow UI phase "render"`) {
		t.Fatalf("missing record:\n%s", log)
	}
	if !strings.Contains(log, "goroutine") {
		t.Fatalf("missing goroutine dump:\n%s", log)
	}

	// A fast phase adds nothing.
	before := len(data)
	wiring.phase("beat")()
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != before {
		t.Fatalf("fast phase was recorded:\n%s", string(after[before:]))
	}

	// Disabled by default (no path, no threshold).
	disabled := &RunWiring{StallLogPath: path}
	func() {
		defer disabled.phase("input")()
		time.Sleep(2 * time.Millisecond)
	}()
	final, _ := os.ReadFile(path)
	if len(final) != before {
		t.Fatal("a disabled stall log wrote a record")
	}
}
