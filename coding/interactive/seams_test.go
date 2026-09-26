package interactive

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// A seam audit found func-typed wiring fields that no production code assigned.
// Most were deliberate (test seams, host hooks, D41 vestiges) and are documented
// where they live, one (ConfigureHTTPIdleTimeout) was a dispatcher seam with
// nothing safe to install (D40), and one was a real gap, pinned here.

// The global debug key is the renderer's: upstream matches shift+ctrl+d in
// tui.ts and interactive-mode.ts points ui.onDebug at handleDebugCommand, so the
// key and /debug do the same work. Neither hook was assigned here.
func TestDebugKeyRunsTheDebugCommand(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDir) // the debug log path follows the env
	app, cleanup := newTestApp(t)
	defer cleanup()

	screen, ok := tuiConcrete(app.ui).(*tui.MainScreen)
	if !ok {
		t.Fatalf("renderer = %T", tuiConcrete(app.ui))
	}
	if screen.MatchesDebugKey == nil || screen.OnDebug == nil {
		t.Fatal("the renderer's debug key is not wired")
	}
	if screen.MatchesDebugKey("x") {
		t.Error("MatchesDebugKey accepted a plain character")
	}

	// The callback is the same work /debug does: a debug log in the agent dir.
	screen.OnDebug()
	if _, err := os.Stat(filepath.Join(agentDir, coding.AppName+"-debug.log")); err != nil {
		t.Fatalf("the debug key wrote no debug log: %v", err)
	}
}
