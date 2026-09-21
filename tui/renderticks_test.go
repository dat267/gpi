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

// TestEnableRenderTicksDisablesTimer asserts loop mode stops arming the
// internal render timer (stage 2 deletes the timer goroutine on this path).
func TestEnableRenderTicksDisablesTimer(t *testing.T) {
	terminal := &fakePostTerminal{width: 40, height: 10}
	screen := NewMainScreen(terminal, false, "")
	screen.RequestRender(false)
	screen.mu.Lock()
	armed := screen.renderTimer != nil
	screen.mu.Unlock()
	if !armed {
		t.Fatal("timer mode should arm the render timer")
	}

	screen.EnableRenderTicks()
	screen.mu.Lock()
	screen.cancelRenderTimerLocked()
	screen.renderTimer = nil
	screen.mu.Unlock()

	screen.RequestRender(false)
	screen.mu.Lock()
	armedAfter := screen.renderTimer != nil
	screen.mu.Unlock()
	if armedAfter {
		t.Fatal("loop mode must not arm the render timer")
	}
	if got := len(screen.RenderTicks()); got != 1 {
		t.Fatalf("pending ticks = %d, want 1", got)
	}
}
