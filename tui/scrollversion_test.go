package tui

import "testing"

// TestScrollViewForwardsChildRenderVersion pins the repaint path through the
// fullscreen transcript. A ScrollView renders by handing back its child's lines,
// and a Container child rewrites only its changed suffix *in place* (same backing
// array, bumped version) when the change is not in its first child. Slice
// identity therefore cannot see the change, so the scroll view must forward the
// revision — otherwise the parent keeps its cached prefix and the changed line
// (the shell elapsed label, say) never repaints.
func TestScrollViewForwardsChildRenderVersion(t *testing.T) {
	first := NewText("header", 0, 0, nil)
	elapsed := NewText("Elapsed 1.0s", 0, 0, nil)
	inner := &Container{}
	inner.AddChild(first)
	inner.AddChild(elapsed)
	view := NewScrollView(inner, ScrollViewOptions{})
	outer := &Container{}
	outer.AddChild(view)

	outer.Render(20)
	before, ok := outer.RenderVersion()
	if !ok {
		t.Fatal("a Container must expose its revision")
	}

	// Change the *second* child: the inner container rewrites in place.
	elapsed.SetText("Elapsed 2.0s")
	outer.Render(20)
	after, _ := outer.RenderVersion()
	if after == before {
		t.Fatal("an in-place change inside the ScrollView was not detected, so the frame would not repaint")
	}
}

// TestScrollViewRenderVersionPassesThroughWhenUnversioned covers the fallback: a
// child that exposes no revision leaves the scroll view unversioned, so the
// parent keeps using slice identity (what a fresh padded copy relies on).
func TestScrollViewRenderVersionPassesThroughWhenUnversioned(t *testing.T) {
	view := NewScrollView(&staticComponent{lines: []string{"plain"}}, ScrollViewOptions{})
	if _, ok := view.RenderVersion(); ok {
		t.Fatal("an unversioned child must leave the scroll view unversioned")
	}
}
