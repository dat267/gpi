package tui

import (
	"testing"
	"time"
)

// The input-latency observer closes the loop on a keystroke: the reader stamps
// the read (MarkInputRead), the loop tags the frame it produces, and the writer
// reports when that frame reaches the console. It exists to bisect a slow
// terminal into before-the-app (read), in-the-loop (dispatch+paint), and
// in-the-writer (console write).

func TestInputLatencyObserverReportsFrameFlush(t *testing.T) {
	terminal := NewProcessTerminal(nil, nil)
	terminal.writeFn = func(string) {}
	type sample struct{ readAt, paintAt, writtenAt time.Time }
	done := make(chan sample, 1)
	terminal.SetInputLatencyObserver(func(readAt, paintAt, writtenAt time.Time) {
		done <- sample{readAt, paintAt, writtenAt}
	})

	readAt := time.Now().Add(-5 * time.Millisecond)
	terminal.MarkInputRead(readAt)
	terminal.BeginFrame()
	terminal.Write("paint")
	terminal.EndFrame()

	select {
	case got := <-done:
		if !got.readAt.Equal(readAt) {
			t.Fatalf("readAt = %v, want %v", got.readAt, readAt)
		}
		if got.paintAt.Before(got.readAt) {
			t.Fatalf("paintAt %v before readAt %v", got.paintAt, got.readAt)
		}
		if got.writtenAt.Before(got.paintAt) {
			t.Fatalf("writtenAt %v before paintAt %v", got.writtenAt, got.paintAt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("observer not called for a tagged frame")
	}
}

func TestInputLatencyObserverIgnoresUntaggedFrames(t *testing.T) {
	terminal := NewProcessTerminal(nil, nil)
	terminal.writeFn = func(string) {}
	called := make(chan struct{}, 1)
	terminal.SetInputLatencyObserver(func(_, _, _ time.Time) { called <- struct{}{} })

	terminal.BeginFrame()
	terminal.Write("paint")
	terminal.EndFrame()
	terminal.flushWrites()

	select {
	case <-called:
		t.Fatal("observer called for an untagged frame")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestInputLatencyObserverDropsAStaleStamp(t *testing.T) {
	terminal := NewProcessTerminal(nil, nil)
	terminal.writeFn = func(string) {}
	called := make(chan struct{}, 1)
	terminal.SetInputLatencyObserver(func(_, _, _ time.Time) { called <- struct{}{} })

	// A stamp older than the max age is dropped rather than attributed to an
	// unrelated frame that arrives later.
	terminal.MarkInputRead(time.Now().Add(-time.Minute))
	terminal.BeginFrame()
	terminal.Write("paint")
	terminal.EndFrame()
	terminal.flushWrites()

	select {
	case <-called:
		t.Fatal("observer called for a stale stamp")
	case <-time.After(50 * time.Millisecond):
	}
}
