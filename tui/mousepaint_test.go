package tui

import "testing"

// drainRenderTicks empties the coalesced request channel so a test can observe
// the requests caused by one input event.
func drainRenderTicks(screen *AltScreen) {
	for len(screen.RenderTicks()) > 0 {
		<-screen.RenderTicks()
	}
}

// TestAltScreenMouseMoveRequestsNoRender pins the input-paint contract behind
// D164. Under ?1003h the terminal reports every pointer movement, and a full
// frame is O(the whole transcript); the owner paints only when a render
// request is pending, so a move that changed nothing must leave the request
// channel empty. A wheel scrolls the viewport and must still ask for a frame.
func TestAltScreenMouseMoveRequestsNoRender(t *testing.T) {
	terminal := &recordingTerminal{width: 80, height: 20}
	screen := NewAltScreen(terminal, false, t.TempDir(), AltScreenOptions{})
	screen.EnableRenderTicks()
	screen.AddChild(&scriptedComponent{lines: []string{"hello", "world"}})
	screen.Start()
	screen.RenderNow(false)
	drainRenderTicks(screen)

	// SGR 35 = 32 (motion) + 3 (no button held): a bare pointer movement.
	screen.HandleTerminalInput("\x1b[<35;10;3M")
	if pending := len(screen.RenderTicks()); pending != 0 {
		t.Fatalf("a bare mouse move requested %d paints, want 0", pending)
	}

	screen.HandleTerminalInput("\x1b[<64;10;3M") // wheel up
	if pending := len(screen.RenderTicks()); pending != 1 {
		t.Fatalf("a wheel event requested %d paints, want 1", pending)
	}

	// A press is input too, and still paints (the widget under the pointer may
	// have changed).
	drainRenderTicks(screen)
	screen.HandleTerminalInput("\x1b[<0;10;3M")
	if pending := len(screen.RenderTicks()); pending != 1 {
		t.Fatalf("a mouse press requested %d paints, want 1", pending)
	}
}
