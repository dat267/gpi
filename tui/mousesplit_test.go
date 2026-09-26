package tui

import (
	"testing"
	"time"
)

// The input buffer force-flushes what it holds once the deadline passes: 50 ms
// for an incomplete sequence and 10 ms for a lone ESC (that shorter deadline is
// how the Escape key itself is delivered). A terminal that writes a mouse report
// in two pieces across that deadline — the ESC first, the rest later — therefore
// hands the report's tail over with no ESC, and upstream parses it as typed text:
// `[<65;59;42M` (wheel-down at 58,41) ends up in the prompt. These tests pin the
// split and that the port still treats both halves as one mouse report (D169).

// TestSplitMouseReportAcrossTheDeadline documents the mechanism and the fix.
// Joined inside the deadline the buffer produces the whole report. If the flush
// happens first, the lone ESC goes out as the Escape key and the rest arrives as
// a plain chunk — which, without the re-join, the buffer shreds into characters
// and the prompt receives the report as text.
func TestSplitMouseReportAcrossTheDeadline(t *testing.T) {
	joined := NewStdinBuffer(StdinBufferOptions{})
	var got []string
	joined.OnData = func(sequence string) { got = append(got, sequence) }
	joined.ProcessString("\x1b")
	joined.ProcessString("[<65;59;42M")
	if len(got) != 1 || got[0] != "\x1b[<65;59;42M" {
		t.Fatalf("joined within the deadline = %q, want the whole report", got)
	}

	// A tail with no head in front of it is emitted one rune at a time: that is
	// how `[<65;59;42M` reaches the prompt.
	shredded := NewStdinBuffer(StdinBufferOptions{})
	var runes []string
	shredded.OnData = func(sequence string) { runes = append(runes, sequence) }
	shredded.ProcessString("[<65;59;42M")
	if len(runes) != 11 {
		t.Fatalf("a bare tail produced %d sequences (%q), want one rune each", len(runes), runes)
	}

	// After the head was force-flushed, the tail is recognised and re-joined, so
	// the report is dispatched as the mouse event it is instead of being typed.
	split := NewStdinBuffer(StdinBufferOptions{})
	var tail []string
	split.OnData = func(sequence string) { tail = append(tail, sequence) }
	split.ProcessString("\x1b")
	if flushed := split.FlushExpired(time.Now().Add(time.Second)); len(flushed) != 1 || flushed[0] != "\x1b" {
		t.Fatalf("expired flush = %q, want the lone ESC", flushed)
	}
	split.ProcessString("[<65;59;42M")
	if len(tail) != 1 || tail[0] != "\x1b[<65;59;42M" {
		t.Fatalf("after the flush the buffer produced %q, want the re-joined report", tail)
	}
}

// TestOrphanedMouseReportIsStillDispatched is the fix: the tail is dispatched as
// the mouse event it is, so the wheel still scrolls instead of being typed.
func TestOrphanedMouseReportIsStillDispatched(t *testing.T) {
	orphaned, ok := parseSgrMouseEvent("[<65;59;42M")
	if !ok {
		t.Fatal("an orphaned mouse report is not recognised as a mouse event")
	}
	prefixed, ok := parseSgrMouseEvent("\x1b[<65;59;42M")
	if !ok {
		t.Fatal("a prefixed mouse report is not recognised")
	}
	if orphaned != prefixed {
		t.Fatalf("orphaned = %+v, prefixed = %+v", orphaned, prefixed)
	}
	if orphaned.Button != 65 || orphaned.X != 58 || orphaned.Y != 41 {
		t.Fatalf("event = %+v, want wheel-down at 58,41", orphaned)
	}

	terminal := &recordingTerminal{width: 80, height: 20}
	screen := NewAltScreen(terminal, false, t.TempDir(), AltScreenOptions{})
	screen.EnableRenderTicks()
	screen.AddChild(&scriptedComponent{lines: []string{"hello", "world"}})
	screen.Start()
	screen.RenderNow(false)
	drainRenderTicks(screen)

	screen.HandleTerminalInput("[<65;59;42M")
	if pending := len(screen.RenderTicks()); pending != 1 {
		t.Fatalf("the orphaned report requested %d paints, want 1 (a wheel scroll)", pending)
	}
}

// TestTruncatedMouseReportIsConsumed pins the other half of the split: a
// fragment — the head that lost its tail, or a tail that lost its ESC — is
// consumed rather than delivered to the prompt. isMouseSequence is the gate the
// input listener consults after parseSgrMouseEvent (altscreen.go), so this is the
// decision that keeps the fragment out of the editor.
func TestTruncatedMouseReportIsConsumed(t *testing.T) {
	terminal := &recordingTerminal{width: 80, height: 20}
	screen := NewAltScreen(terminal, false, t.TempDir(), AltScreenOptions{})

	for _, fragment := range []string{
		"\x1b[<65;59;42", // head, tail still in flight
		"\x1b[<65;59",    // head, cut mid-report
		"\x1b[<",         // head, only the marker
		"\x1b[<35;10;3m", // a complete report still is one
	} {
		if !screen.isMouseSequence(fragment) {
			t.Errorf("%q is typed into the prompt instead of being consumed", fragment)
		}
	}

	// Text that merely looks similar is not swallowed.
	for _, text := range []string{"[", "[65;59", "<65;59;42M", "hello[<65"} {
		if screen.isMouseSequence(text) {
			t.Errorf("%q was swallowed as a mouse fragment", text)
		}
	}
}
