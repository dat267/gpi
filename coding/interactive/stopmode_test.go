package interactive

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/dat267/pier/tui"
)

// TestStopModeSwitchesToRegularOnTranscriptExit covers the fullscreen exit
// replay: with fullscreenExitOutput="transcript" the teardown swaps the
// renderer to the regular main screen (its final frame stays in the terminal
// scrollback) before the terminal is restored.
func TestStopModeSwitchesToRegularOnTranscriptExit(t *testing.T) {
	app, cleanup := newTestAppB(t)
	defer cleanup()
	app.Init(context.Background())
	screen, ok := app.lifecycle.CurrentUI().(*tui.AltScreen)
	if !ok {
		t.Fatalf("initial UI = %T", app.lifecycle.CurrentUI())
	}
	screen.Start()
	screen.DisableAutoRender()

	app.StopMode("transcript")

	current, ok := app.lifecycle.CurrentUI().(*tui.MainScreen)
	if !ok {
		t.Fatalf("after StopMode the renderer = %T, want MainScreen", app.lifecycle.CurrentUI())
	}
	_ = current
	// And with "resume-hint" the alt screen is kept (only the screen state is
	// restored, no replay).
}

func TestStopModeKeepsAltScreenOnResumeHintExit(t *testing.T) {
	app, cleanup := newTestAppB(t)
	defer cleanup()
	app.Init(context.Background())
	screen, ok := app.lifecycle.CurrentUI().(*tui.AltScreen)
	if !ok {
		t.Fatalf("initial UI = %T", app.lifecycle.CurrentUI())
	}
	screen.Start()
	screen.DisableAutoRender()

	app.StopMode("resume-hint")

	if _, ok := app.lifecycle.CurrentUI().(*tui.AltScreen); !ok {
		t.Fatalf("after StopMode the renderer = %T, want AltScreen", app.lifecycle.CurrentUI())
	}
}

// TestStopModeStopsTheAppOffloopQueues pins that StopMode stops the queues the
// app registered (threaded through its offloop group), not just the settings
// and session queues it flushes explicitly — the theme queue used to be left
// running.
func TestStopModeStopsTheAppOffloopQueues(t *testing.T) {
	app, cleanup := newTestAppB(t)
	defer cleanup()
	app.Init(context.Background())
	queue := app.transcript.PrerenderQueue
	if queue == nil {
		t.Fatal("pre-render queue not wired")
	}

	app.StopMode("transcript")

	var ran atomic.Bool
	queue.Go(func() { ran.Store(true) })
	queue.Flush()
	if ran.Load() {
		t.Fatal("StopMode did not stop the app's off-loop queues")
	}
}
