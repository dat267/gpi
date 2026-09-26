package tui

import (
	"strings"
	"testing"
)

// TestLaidOutTranscriptRepaintsThroughTheStack covers the fullscreen shape: the
// transcript is a ScrollView inside a VStack, and a Stack renders through an
// embedded Container, which keeps the flattened lines and only rebuilds when a
// child reports a change. A change inside the scroll view must therefore reach
// that container — otherwise the frame is served the stale prefix and the change
// (the shell elapsed label, say) never paints.
func TestLaidOutTranscriptRepaintsThroughTheStack(t *testing.T) {
	header := NewText("header", 0, 0, nil)
	elapsed := NewText("Elapsed 1.0s", 0, 0, nil)
	document := &Container{}
	document.AddChild(header)
	document.AddChild(elapsed)
	transcript := NewScrollView(document, ScrollViewOptions{})
	dock := NewVStack(nil, StackOptions{})
	root := NewVStack([]Component{transcript, dock}, StackOptions{})

	// This is what the fullscreen renderer paints.
	first := strings.Join(RenderLayoutFrame(root, 40, 12, func() {}).Lines, "\n")
	if !strings.Contains(first, "Elapsed 1.0s") {
		t.Fatalf("the first frame lost the elapsed line:\n%s", first)
	}

	// The label ticks: the text changes, so the inner container rewrites its
	// suffix in place.
	elapsed.SetText("Elapsed 2.0s")
	second := strings.Join(RenderLayoutFrame(root, 40, 12, func() {}).Lines, "\n")
	if !strings.Contains(second, "Elapsed 2.0s") {
		t.Fatalf("the laid-out frame kept the stale line, so the tick never painted:\n%s", second)
	}
}
