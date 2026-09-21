package interactive

import (
	"context"
	"testing"

	"github.com/dat267/gpi/tui"
)

// TestStopModeSwitchesToRegularOnTranscriptExit covers the fullscreen exit
// replay: with fullscreenExitOutput="transcript" the teardown swaps the
// renderer to the regular main screen (its final frame stays in the terminal
// scrollback) before the terminal is restored.
func TestStopModeSwitchesToRegularOnTranscriptExit(t *testing.T) {
	app, cleanup := newTestAppB(t)
	defer cleanup()
	app.Init(context.Background())
	screen, ok := app.Lifecycle.CurrentUI().(*tui.AltScreen)
	if !ok {
		t.Fatalf("initial UI = %T", app.Lifecycle.CurrentUI())
	}
	screen.Start()
	screen.DisableAutoRender()

	app.StopMode("transcript")

	current, ok := app.Lifecycle.CurrentUI().(*tui.MainScreen)
	if !ok {
		t.Fatalf("after StopMode the renderer = %T, want MainScreen", app.Lifecycle.CurrentUI())
	}
	_ = current
	// And with "resume-hint" the alt screen is kept (only the screen state is
	// restored, no replay).
}

func TestStopModeKeepsAltScreenOnResumeHintExit(t *testing.T) {
	app, cleanup := newTestAppB(t)
	defer cleanup()
	app.Init(context.Background())
	screen, ok := app.Lifecycle.CurrentUI().(*tui.AltScreen)
	if !ok {
		t.Fatalf("initial UI = %T", app.Lifecycle.CurrentUI())
	}
	screen.Start()
	screen.DisableAutoRender()

	app.StopMode("resume-hint")

	if _, ok := app.Lifecycle.CurrentUI().(*tui.AltScreen); !ok {
		t.Fatalf("after StopMode the renderer = %T, want AltScreen", app.Lifecycle.CurrentUI())
	}
}
