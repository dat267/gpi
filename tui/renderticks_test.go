package tui

import (
	"testing"
)

// TestRequestRenderCoalescesIntoTicks asserts the loop-mode contract: N render
// requests collapse into a single pending tick (capacity 1), so a burst of
// state changes cannot schedule N paints.
func TestRequestRenderCoalescesIntoTicks(t *testing.T) {
	terminal := &fakePostTerminal{width: 40, height: 10}
	screen := NewMainScreen(terminal, false, "")
	screen.EnableRenderTicks()

	for i := 0; i < 50; i++ {
		screen.RequestRender(false)
	}
	if got := len(screen.RenderTicks()); got != 1 {
		t.Fatalf("pending ticks = %d, want 1 (coalesced)", got)
	}
	// Consuming the tick and rendering once paints exactly once.
	<-screen.RenderTicks()
	before := screen.RenderCount()
	screen.RenderNow(false)
	if got := screen.RenderCount() - before; got != 1 {
		t.Fatalf("renders after one tick = %d, want 1", got)
	}
	// A request after the paint re-arms the tick.
	screen.RequestRender(false)
	if got := len(screen.RenderTicks()); got != 1 {
		t.Fatalf("pending ticks after a new request = %d, want 1", got)
	}
}

// TestRequestRenderNeverArmsATimer pins the D146 contract: there is no
// internal render timer — requests only coalesce onto the tick channel, and
// the owner paints.
func TestRequestRenderNeverArmsATimer(t *testing.T) {
	terminal := &fakePostTerminal{width: 40, height: 10}
	screen := NewMainScreen(terminal, false, "")

	// Without a tick channel the request is dropped (no owner to paint).
	screen.RequestRender(false)
	if got := screen.RenderCount(); got != 0 {
		t.Fatalf("renders without a tick channel = %d, want 0", got)
	}

	screen.EnableRenderTicks()
	screen.RequestRender(false)
	if got := len(screen.RenderTicks()); got != 1 {
		t.Fatalf("pending ticks = %d, want 1", got)
	}
	if screen.RenderCount() != 0 {
		t.Fatalf("renders before the owner paints = %d, want 0", screen.RenderCount())
	}
}
