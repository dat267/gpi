package tui

import (
	"strings"
	"testing"
)

// TestScrollToEndIndicatorCentersInClipWidth pins the v0.87 upstream
// tui-alt-screen.ts compositeScrollToEndIndicator arithmetic: the label is
// truncated to the clip width, centered within the clip, and only then clamped
// against the scrollbar column. Pre-0.87 centered the label in the space left
// of the scrollbar instead, which shifted it left by one cell for an even
// remainder (Go previously matched the old behavior).
func TestScrollToEndIndicatorCentersInClipWidth(t *testing.T) {
	terminal := &recordingTerminal{width: 40, height: 5}
	label := strings.Repeat("x", 26)
	screen := NewAltScreen(terminal, false, t.TempDir(), AltScreenOptions{
		ScrollToEndIndicator: func() string { return label },
	})

	sv := NewScrollView(&scriptedComponent{}, ScrollViewOptions{
		Follow: "end", Primary: true, Scrollbar: ScrollbarAlways,
	})
	sv.currentViewportHeight = 5
	sv.contentHeight = 20
	// Scrolled away from the end: the constructor starts following the end.
	sv.followingEnd = false
	box := &LayoutBox{
		Rect:               LayoutRect{X: 0, Y: 0, Width: 40, Height: 5},
		Clip:               LayoutRect{X: 0, Y: 0, Width: 40, Height: 5},
		ScrollView:         sv,
		HasScrollContent:   true,
		ScrollContentLines: make([]string, 20),
	}
	frame := LayoutFrame{
		Root:              &LayoutBox{Children: []*LayoutBox{box}},
		PrimaryScrollView: sv,
	}

	lines := make([]string, 5)
	for i := range lines {
		lines[i] = strings.Repeat(" ", 40)
	}
	screen.compositeScrollToEndIndicator(lines, frame, 40)

	if screen.scrollToEndRect == nil {
		t.Fatal("indicator not composited")
	}
	// column = clip.X + (clip.Width-labelWidth)/2 = 0 + (40-26)/2 = 7.
	if screen.scrollToEndRect.Column != 7 || screen.scrollToEndRect.Width != 26 {
		t.Fatalf("indicator rect = %+v, want column 7 width 26", *screen.scrollToEndRect)
	}
}

// TestScrollToEndIndicatorClampsToScrollbar pins the clamping half of the v0.87
// logic: a label wider than the clip is centered from column 0 and truncated
// at the scrollbar column, not at the clip edge.
func TestScrollToEndIndicatorClampsToScrollbar(t *testing.T) {
	terminal := &recordingTerminal{width: 40, height: 5}
	label := strings.Repeat("x", 50)
	screen := NewAltScreen(terminal, false, t.TempDir(), AltScreenOptions{
		ScrollToEndIndicator: func() string { return label },
	})

	sv := NewScrollView(&scriptedComponent{}, ScrollViewOptions{
		Follow: "end", Primary: true, Scrollbar: ScrollbarAlways,
	})
	sv.currentViewportHeight = 5
	sv.contentHeight = 20
	// Scrolled away from the end: the constructor starts following the end.
	sv.followingEnd = false
	box := &LayoutBox{
		Rect:               LayoutRect{X: 0, Y: 0, Width: 40, Height: 5},
		Clip:               LayoutRect{X: 0, Y: 0, Width: 40, Height: 5},
		ScrollView:         sv,
		HasScrollContent:   true,
		ScrollContentLines: make([]string, 20),
	}
	frame := LayoutFrame{
		Root:              &LayoutBox{Children: []*LayoutBox{box}},
		PrimaryScrollView: sv,
	}

	lines := make([]string, 5)
	for i := range lines {
		lines[i] = strings.Repeat(" ", 40)
	}
	screen.compositeScrollToEndIndicator(lines, frame, 40)

	if screen.scrollToEndRect == nil {
		t.Fatal("indicator not composited")
	}
	// label truncates to clip.Width=40, column=0, rightEdge=scrollbar at 39,
	// so availableWidth=39 and the composited text is 39 cells wide.
	if screen.scrollToEndRect.Column != 0 || screen.scrollToEndRect.Width != 39 {
		t.Fatalf("indicator rect = %+v, want column 0 width 39", *screen.scrollToEndRect)
	}
}
