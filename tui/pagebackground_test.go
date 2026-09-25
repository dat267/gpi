package tui

import (
	"strings"
	"testing"
)

// TestApplyPageBackgroundReplacesResetsAndPads pins the page-background
// contract: a default-background reset inside the line becomes the page
// background, so cells that do not set their own fill stay on the palette, and
// the line is padded to the viewport width on that background.
func TestApplyPageBackgroundReplacesResetsAndPads(t *testing.T) {
	bg := "\x1b[48;2;16;16;16m"
	line := "\x1b[38;2;1;2;3mhi\x1b[39m\x1b[49m"
	got := ApplyPageBackground(line, 5, bg)
	if !strings.HasPrefix(got, bg) {
		t.Fatalf("page background missing at the start: %q", got)
	}
	if strings.Count(got, "\x1b[49m") != 1 {
		t.Fatalf("interior default-background reset survived: %q", got)
	}
	if VisibleWidth(got) != 5 {
		t.Fatalf("width = %d, want 5 (%q)", VisibleWidth(got), got)
	}
}

// TestApplyPageBackgroundNoopWithoutToken keeps the upstream (unstyled) form
// when the theme has no page background.
func TestApplyPageBackgroundNoopWithoutToken(t *testing.T) {
	if got := ApplyPageBackground("hi", 4, ""); got != "hi  " {
		t.Fatalf("empty bg = %q, want padded plain", got)
	}
}

// TestAltScreenPageBackgroundOwnsTheSurface drives a real paint with a page
// background and asserts the whole frame sits on it (and that the low-bandwidth
// trim, which would strip the fill, is skipped).
func TestAltScreenPageBackgroundOwnsTheSurface(t *testing.T) {
	terminal := &fakeTerminal{width: 20, height: 3}
	screen := NewAltScreen(terminal, false, "", AltScreenOptions{})
	screen.PageBackground = func() string { return "\x1b[48;2;16;16;16m" }
	screen.AddChild(&staticComponent{lines: []string{"hello"}})
	screen.Start()
	screen.RenderNow(true)
	joined := terminal.joinedWrites()
	if !strings.Contains(joined, "\x1b[48;2;16;16;16m") {
		t.Fatalf("page background not painted: %q", joined)
	}
}
