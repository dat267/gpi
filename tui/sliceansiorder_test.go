package tui

import "testing"

// TestSliceKeepsAnsiOrderAcrossTheBoundary pins the order of the style carried
// into a slice and a code that applies at the slice's first column.
//
// A slice that begins exactly where a styled span ends carries the span's colour
// in from before the range and meets its reset at the range's first column. The
// pair has to stay in that order. Writing the in-range code as it is met and
// flushing the carried prefix only when the first text is written swaps them, so
// the reset lands ahead of the colour it cancels and the sliced text renders in
// the style that had just ended — a double-clicked word inside inline code left
// the rest of the line yellow.
func TestSliceKeepsAnsiOrderAcrossTheBoundary(t *testing.T) {
	line := "\x1b[33mabcdef\x1b[39m tail"
	got := SliceByColumn(line, 6, 5, true)
	want := "\x1b[33m\x1b[39m tail"
	if got != want {
		t.Fatalf("slice kept the wrong order:\n got %q\nwant %q", got, want)
	}
}
