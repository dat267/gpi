package tui

import (
	"testing"
	"time"
)

// TestHoveredScrollbarDoesNotHide pins upstream's hover rule for a transient
// (mode "auto") scrollbar.
//
// scroll-view.ts markScrollbarActivity() clears any pending hide timer and then
// returns early when the scrollbar is active, so the timer that hides a
// transient scrollbar is never scheduled while the pointer is on the scrollbar
// (or while it is being dragged):
//
//	this.transientScrollbarVisible = true;
//	if (this.scrollbarHideTimer) { clearTimeout(this.scrollbarHideTimer); this.scrollbarHideTimer = undefined; }
//	if (this.scrollbarActive) return;
//	this.scrollbarHideTimer = setTimeout(() => { this.transientScrollbarVisible = false; ... }, this.scrollbarHideDelayMs);
//
// The port armed its lazy deadline before that check, so the early return did
// nothing: the thumb appeared on hover and vanished a second later even though
// the cursor had not left it — and a stationary pointer emits no further
// mouse-move events to re-mark the activity, so it stayed hidden.
func TestHoveredScrollbarDoesNotHide(t *testing.T) {
	view := NewScrollView(probe("A", "B", "C", "D", "E", "F"), ScrollViewOptions{Scrollbar: ScrollbarAuto})
	RenderLayoutFrame(view, 6, 3, func() {})

	// The pointer moves onto the scrollbar.
	view.SetScrollbarActive(true)
	if !view.IsScrollbarVisible() {
		t.Fatal("hovering did not reveal the scrollbar")
	}

	// It stays there while the hide delay elapses. The loop drives the deadline
	// through the animation walk, so ask for a frame well past it.
	view.AnimationFrame(time.Now().Add(2 * time.Second))
	if !view.IsScrollbarVisible() {
		t.Fatal("a hovered scrollbar hid while the cursor was still on it")
	}

	// Leaving is what starts the hide again: the pointer moves off, and now the
	// delay runs to its end.
	view.SetScrollbarActive(false)
	if !view.IsScrollbarVisible() {
		t.Fatal("leaving the scrollbar hid it at once instead of starting the delay")
	}
	view.AnimationFrame(time.Now().Add(2 * time.Second))
	if view.IsScrollbarVisible() {
		t.Fatal("the scrollbar stayed visible after the pointer left")
	}
}
