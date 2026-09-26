package interactive

import (
	"context"
	"testing"
)

// TestDisplayOptionsSingleOwner pins the display-settings seam: output padding,
// thinking-block visibility, the hidden-thinking label and tool-output
// expansion had a copy on the transcript, the event dispatcher, the run wiring,
// the trust wiring and the UI state, synced by hand in app.go. Two copies were
// never assigned (TranscriptRenderer/EventDispatcher.ToolOutputExpanded) and
// RunWiring.OutputPad went stale on /reload. One DisplayOptions value is now
// shared by every holder and mutated in one place.
func TestDisplayOptionsSingleOwner(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	app.Init(context.Background())

	if app.display == nil {
		t.Fatal("app has no display options")
	}
	holders := map[string]*DisplayOptions{
		"transcript": app.transcript.Display,
		"events":     app.events.Display,
		"runner":     app.runner.Display,
		"trust":      app.trust.Display,
		"uiState":    app.uiState.Display,
	}
	for name, got := range holders {
		if got != app.display {
			t.Fatalf("%s does not share the app display options", name)
		}
	}

	// ctrl+o flips the single owner, so components created afterwards inherit
	// the expansion state (the dead copies kept them collapsed).
	app.key.OnToolsExpand()
	if !app.display.ToolOutputExpanded {
		t.Fatal("tool expansion did not reach the shared display options")
	}

	// /reload re-reads the padding into the single owner, including the run
	// wiring's error lines. OutputPad clamps to 0 or 1 (GetOutputPad).
	app.settings.SetOutputPad(0)
	app.applyReloadedSettings()
	if app.display.OutputPad != 0 {
		t.Fatalf("output pad = %d, want 0 after reload", app.display.OutputPad)
	}
}
