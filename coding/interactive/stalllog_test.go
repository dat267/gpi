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

// TestStallLogThresholdDefaultsOn pins the default threshold: the logger is on
// at 100ms when PIER_STALL_MS is unset or garbage, so a freeze is captured even
// when the session was not started with the variable — the first two freezes
// after the markdown fix struck sessions with the logger off. An explicit
// PIER_STALL_MS=0 still disables it, and a set value overrides the default.
func TestStallLogThresholdDefaultsOn(t *testing.T) {
	cases := []struct {
		env  string
		set  bool
		want time.Duration
	}{
		{env: "", set: false, want: 100 * time.Millisecond},
		{env: "0", set: true, want: 0},
		{env: "50", set: true, want: 50 * time.Millisecond},
		{env: "garbage", set: true, want: 100 * time.Millisecond},
		{env: "-3", set: true, want: 100 * time.Millisecond},
	}
	for _, tc := range cases {
		if tc.set {
			t.Setenv("PIER_STALL_MS", tc.env)
		} else {
			t.Setenv("PIER_STALL_MS", "")
			os.Unsetenv("PIER_STALL_MS")
		}
		if got := stallLogThreshold(); got != tc.want {
			t.Errorf("PIER_STALL_MS=%q (set=%v): got %v, want %v", tc.env, tc.set, got, tc.want)
		}
	}
}
