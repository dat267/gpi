package interactive

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestMouseMotionBurstDoesNotRepaint pins the mouse-interaction fix (D164).
//
// Fullscreen mode enables ?1003h, so the terminal reports every pointer
// movement, and one frame is O(the whole transcript): painting once per input
// chunk meant the loop spent its entire budget on full repaints, so mouse
// movement — and everything queued behind it — was unusable on a large
// session. A motion burst must still be dispatched (no input is dropped) but
// must no longer cost a frame per event.
//
// The burst is closed with a keystroke and the assertion waits for that
// keystroke to reach the editor. The input channel is FIFO and the loop drains
// it in order, so the keystroke landing proves every preceding motion chunk
// was dispatched too — a silently dead input path cannot pass this test.
func TestMouseMotionBurstDoesNotRepaint(t *testing.T) {
	app, cleanup := newTestAppB(t)
	defer cleanup()
	stop := startLoopApp(t, app)
	defer stop()

	// Let the startup paints settle so the burst is what gets measured.
	waitForConditionWithin(t, func() bool { return app.UI.RenderCount() > 0 }, 6*time.Second)
	time.Sleep(50 * time.Millisecond)

	beforeText := editorText(app)
	before := app.UI.RenderCount()

	// SGR 35 = 32 (motion) + 3 (no button held): the report a bare pointer
	// movement produces.
	move := "\x1b[<35;50;8M"
	for i := 0; i < 60; i++ {
		app.PostTerminalInput(move)
	}
	app.PostTerminalInput("z")
	expected := beforeText + "z"
	waitForConditionWithin(t, func() bool {
		return strings.Contains(editorText(app), expected)
	}, 6*time.Second)

	// 60 moves + 1 keystroke: the keystroke's own frame, plus room for a hover
	// transition and a coalesced tick. One paint per chunk was ~61 frames.
	paints := app.UI.RenderCount() - before
	t.Logf("60 mouse moves + 1 keystroke: %d paints", paints)
	if paints > 6 {
		t.Fatalf("60 mouse moves + 1 keystroke caused %d paints, want at most 6", paints)
	}
}

// TestAnimationScanCacheIsDroppedByAPaint guards the other side of the D164
// animation-scan cache. The loop reuses its last walk between paints, so a
// paint has to invalidate it — otherwise a spinner that a paint starts (the
// paint that renders the indicator is what makes it animate) would arm nothing
// and freeze on its first frame. Deterministic: no loop, no timing.
func TestAnimationScanCacheIsDroppedByAPaint(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	app.Init(context.Background())
	wiring := newRunWiring(app)

	schedule := newLoopSchedule(runLoopHost{wiring}, wiring.renderUI, nil)
	defer schedule.close()

	// Idle: the walk finds no animator, and the loop caches that answer.
	if ch := schedule.arm(); ch != nil {
		t.Fatal("idle arm armed an animation timer")
	}

	app.UIState.ShowStatusIndicator(NewCompactionStatusIndicator(app.UI, "manual"))
	schedule.paintNow() // the paint that shows the indicator

	if ch := schedule.arm(); ch == nil {
		t.Fatal("a paint that started an animation did not re-arm the animation timer")
	}
}
