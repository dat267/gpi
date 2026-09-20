package tui

import (
	"strings"
	"testing"
)

// Round 2 tests: truncation, column slicing, the ANSI tracker, wrapping, and
// background application.

func TestTruncateToWidth(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		width    int
		ellipsis string
		pad      bool
		want     string
	}{
		{"zero width", "abc", 0, "...", false, ""},
		{"empty text", "", 5, "...", false, ""},
		{"short ascii fits", "abc", 5, "...", false, "abc"},
		{"short ascii pads", "abc", 5, "...", true, "abc  "},
		{"ascii truncates with ellipsis", "abcdef", 5, "...", false, "ab\x1b[0m...\x1b[0m"},
		{"exact fit no ellipsis", "abcde", 5, "...", false, "abcde"},
		{"wide runes count double", "日本語", 5, "...", false, "日\x1b[0m...\x1b[0m"},
		{"wide runes fit", "日本語", 6, "...", false, "日本語"},
		{"wide runes cut mid-cluster pads", "日本語", 5, "...", true, "日\x1b[0m...\x1b[0m"},
		{"ansi styling preserved", "\x1b[31mabcdef\x1b[0m", 5, "...", false, "\x1b[31mab\x1b[0m...\x1b[0m"},
	}
	for _, testCase := range cases {
		if got := TruncateToWidth(testCase.text, testCase.width, testCase.ellipsis, testCase.pad); got != testCase.want {
			t.Errorf("%s: TruncateToWidth(%q, %d, %q, %v) = %q, want %q",
				testCase.name, testCase.text, testCase.width, testCase.ellipsis, testCase.pad, got, testCase.want)
		}
	}
	// Empty text returns empty even with padding (upstream returns early).
	if got := TruncateToWidth("", 3, "...", false); got != "" {
		t.Fatalf("empty = %q", got)
	}

	// An ellipsis wider than the target clips the ellipsis itself.
	if got := TruncateToWidth("abcdef", 2, "...", false); got != "\x1b[0m..\x1b[0m" {
		t.Fatalf("wide ellipsis = %q", got)
	}
	// Text that fits is returned as-is.
	if got := TruncateToWidth("ab", 3, "...", false); got != "ab" {
		t.Fatalf("fits = %q", got)
	}
}

func TestSliceWithWidth(t *testing.T) {
	line := "\x1b[31m日本語\x1b[0mabc"

	// Middle range across wide characters: the pending ANSI from before the
	// range prefixes the result.
	slice := SliceWithWidth(line, 1, 3, false)
	if slice.Text != "\x1b[31m本" || slice.Width != 2 {
		t.Fatalf("slice = %+v", slice)
	}

	// Strict keeps the wide char that fits within the range.
	slice = SliceWithWidth(line, 1, 3, true)
	if slice.Text != "\x1b[31m本" || slice.Width != 2 {
		t.Fatalf("strict slice = %+v", slice)
	}

	// ANSI codes in range are preserved.
	slice = SliceWithWidth(line, 0, 2, false)
	if !strings.Contains(slice.Text, "\x1b[31m") {
		t.Fatalf("slice lost ansi: %+v", slice)
	}

	// Zero length.
	if slice := SliceWithWidth(line, 0, 0, false); slice.Text != "" || slice.Width != 0 {
		t.Fatalf("zero slice = %+v", slice)
	}
}

type sliceCase struct {
	line     string
	startCol int
	length   int
	strict   bool
}

func TestSliceByColumnASCII(t *testing.T) {
	if got := SliceByColumn("hello", 1, 3, false); got != "ell" {
		t.Fatalf("got %q", got)
	}
	if got := SliceByColumn("hello", 10, 3, false); got != "" {
		t.Fatalf("out of range = %q", got)
	}
}

func TestANSITracker(t *testing.T) {
	tracker := &ansiCodeTracker{}
	tracker.process("\x1b[1m")        // bold
	tracker.process("\x1b[31m")       // red fg
	tracker.process("\x1b[48;5;240m") // 256-color bg
	if !tracker.hasActiveCodes() {
		t.Fatal("state must be active")
	}
	if got := tracker.activeCodes(); got != "\x1b[1;31;48;5;240m" {
		t.Fatalf("activeCodes = %q", got)
	}
	if got := tracker.activeBackgroundCode(); got != "\x1b[48;5;240m" {
		t.Fatalf("bg = %q", got)
	}

	// RGB colors.
	tracker2 := &ansiCodeTracker{}
	tracker2.process("\x1b[38;2;10;20;30m")
	if got := tracker2.activeCodes(); got != "\x1b[38;2;10;20;30m" {
		t.Fatalf("rgb = %q", got)
	}

	// Reset clears everything.
	tracker.process("\x1b[0m")
	if tracker.hasActiveCodes() {
		t.Fatalf("reset failed: %+v", tracker)
	}
	if got := tracker.activeCodes(); got != "" {
		t.Fatalf("activeCodes = %q", got)
	}

	// Attribute resets are specific. Upstream's tracker reads only the first
	// SGR of a combined sequence, so codes are processed individually.
	tracker.process("\x1b[1m")
	tracker.process("\x1b[31m")
	tracker.process("\x1b[48;5;240m")
	tracker.process("\x1b[4m")
	tracker.process("\x1b[24m") // underline off
	if got := tracker.activeCodes(); got != "\x1b[1;31;48;5;240m" {
		t.Fatalf("underline reset: %q", got)
	}

	// The line-end reset closes underline only.
	tracker3 := &ansiCodeTracker{}
	tracker3.process("\x1b[1m")
	tracker3.process("\x1b[4m")
	if got := tracker3.lineEndReset(); got != "\x1b[24m" {
		t.Fatalf("lineEndReset = %q", got)
	}

	// 39/49 restore defaults. Combined sequences only process the first SGR
	// (upstream's regex limitation), so codes are processed separately.
	tracker4 := &ansiCodeTracker{}
	tracker4.process("\x1b[31m")
	tracker4.process("\x1b[41m")
	tracker4.process("\x1b[39m")
	if got := tracker4.activeCodes(); got != "\x1b[41m" {
		t.Fatalf("fg default: %q", got)
	}
	tracker4.process("\x1b[49m")
	if tracker4.hasActiveCodes() {
		t.Fatalf("bg default failed")
	}
}

func TestANSITrackerHyperlink(t *testing.T) {
	tracker := &ansiCodeTracker{}
	tracker.process("\x1b]8;;https://x\x1b\\")
	if !tracker.hasActiveCodes() {
		t.Fatal("hyperlink is active")
	}
	// The active codes re-open the hyperlink with its original terminator.
	if got := tracker.activeCodes(); got != "\x1b]8;;https://x\x1b\\" {
		t.Fatalf("activeCodes = %q", got)
	}
	// The line-end reset closes it.
	if got := tracker.lineEndReset(); got != "\x1b]8;;\x1b\\" {
		t.Fatalf("lineEndReset = %q", got)
	}
	// SGR reset does not affect the hyperlink.
	tracker.process("\x1b[0m")
	if tracker.activeHyperlink == nil {
		t.Fatal("sgr reset must not clear the hyperlink")
	}
	// A BEL-terminated link keeps BEL.
	bel := &ansiCodeTracker{}
	bel.process("\x1b]8;;https://y\x07")
	if got := bel.activeCodes(); got != "\x1b]8;;https://y\x07" {
		t.Fatalf("activeCodes = %q", got)
	}
	if got := bel.lineEndReset(); got != "\x1b]8;;\x07" {
		t.Fatalf("lineEndReset = %q", got)
	}
	// A close sequence is not tracked as an open; the link stays until cleared.
	bel.process("\x1b]8;;\x07")
	if bel.activeHyperlink == nil {
		t.Fatal("upstream keeps the link tracked across a close sequence")
	}
	bel.clear()
	if bel.activeHyperlink != nil {
		t.Fatal("clear must reset the hyperlink")
	}
}

func TestWrapTextWithAnsi(t *testing.T) {
	// Plain text wraps on spaces.
	lines := WrapTextWithAnsi("hello world foo", 5)
	if strings.Join(lines, "|") != "hello|world|foo" {
		t.Fatalf("lines = %q", lines)
	}

	// Text within the width returns one line.
	if lines := WrapTextWithAnsi("hello", 10); len(lines) != 1 || lines[0] != "hello" {
		t.Fatalf("lines = %q", lines)
	}

	// Newlines split with ANSI state carried over.
	lines = WrapTextWithAnsi("\x1b[31mred\nstill red\x1b[0m", 20)
	if len(lines) != 2 || !strings.HasPrefix(lines[1], "\x1b[31m") {
		t.Fatalf("lines = %q", lines)
	}

	// ANSI styling is re-applied after the break.
	lines = WrapTextWithAnsi("\x1b[31mred red red", 4)
	if len(lines) < 2 {
		t.Fatalf("lines = %q", lines)
	}
	if !strings.HasPrefix(lines[1], "\x1b[31m") {
		t.Fatalf("styled continuation = %q", lines[1])
	}

	// Long words break character by character.
	lines = WrapTextWithAnsi("abcdefgh", 3)
	if strings.Join(lines, "|") != "abc|def|gh" {
		t.Fatalf("lines = %q", lines)
	}

	// Empty text.
	if lines := WrapTextWithAnsi("", 5); len(lines) != 1 || lines[0] != "" {
		t.Fatalf("lines = %q", lines)
	}

	// Within the width the early return does not trim trailing whitespace.
	lines = WrapTextWithAnsi("a b ", 10)
	if len(lines) != 1 || lines[0] != "a b " {
		t.Fatalf("lines = %q", lines)
	}

	// CJK characters break anywhere (two runes per line at width 4).
	lines = WrapTextWithAnsi("日本語日本語", 4)
	if strings.Join(lines, "|") != "日本|語日|本語" {
		t.Fatalf("lines = %q", lines)
	}
}

func TestApplyBackgroundToLine(t *testing.T) {
	// The background applies to the content plus padding.
	line := ApplyBackgroundToLine("hi", 5, func(text string) string {
		return "\x1b[44m" + text + "\x1b[49m"
	})
	if line != "\x1b[44mhi   \x1b[49m" {
		t.Fatalf("line = %q", line)
	}
	// Wide characters count double for the padding.
	line = ApplyBackgroundToLine("日", 4, nil)
	if line != "日  " {
		t.Fatalf("line = %q", line)
	}
}
