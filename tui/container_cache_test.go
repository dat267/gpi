package tui

import (
	"fmt"
	"strings"
	"testing"
)

func makeWarmContainer(lines int) *Container {
	var sb strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	container := &Container{}
	container.AddChild(NewText(strings.TrimSuffix(sb.String(), "\n"), 0, 0, nil))
	container.AddChild(NewText("second child", 0, 0, nil))
	container.Render(40)
	return container
}

// TestContainerRenderReusesCachedLines pins the frame cost: every container
// re-concatenated its children into a fresh slice on every paint, so a warm
// frame of a long transcript allocated ~650 KB and spent its time in the GC
// (typedslicecopy + scan). An unchanged container must return the slice it
// already has, and a change must produce new lines.
func TestContainerRenderReusesCachedLines(t *testing.T) {
	container := makeWarmContainer(200)
	first := container.Render(40)

	allocs := testing.AllocsPerRun(20, func() { _ = container.Render(40) })
	if allocs != 0 {
		t.Fatalf("warm render allocated %.0f times; want 0", allocs)
	}
	second := container.Render(40)
	if len(first) == 0 || len(second) != len(first) {
		t.Fatalf("render lengths %d vs %d", len(first), len(second))
	}
	if &first[0] != &second[0] {
		t.Fatal("warm render rebuilt the line slice instead of reusing it")
	}
}

// TestContainerRenderRebuildsOnChanges covers what the cache fingerprint must
// still catch: a child's content, its line count, the width and the child list.
func TestContainerRenderRebuildsOnChanges(t *testing.T) {
	child := NewText("one\ntwo", 0, 0, nil)
	container := &Container{}
	container.AddChild(child)

	hasLine := func(width int, fragment string) bool {
		for _, line := range container.Render(width) {
			if strings.HasPrefix(line, fragment) {
				return true
			}
		}
		return false
	}
	lineCount := func(width int) int { return len(container.Render(width)) }

	if !hasLine(40, "one") || !hasLine(40, "two") {
		t.Fatal("initial render is missing the child's lines")
	}
	child.SetText("one\ntwo\nthree")
	if got := lineCount(40); got != 3 {
		t.Fatalf("after SetText the render has %d lines, want 3", got)
	}
	container.AddChild(NewText("added", 0, 0, nil))
	if !hasLine(40, "added") {
		t.Fatal("added child missing from the render")
	}
	// A width change must re-wrap: the same child at a narrower width produces
	// more lines.
	long := NewText(strings.Repeat("word ", 30), 0, 0, nil)
	narrow := &Container{}
	narrow.AddChild(long)
	if first, second := len(narrow.Render(30)), len(narrow.Render(80)); first <= second {
		t.Fatalf("width change did not re-wrap: %d lines at 30 vs %d at 80", first, second)
	}
}

func BenchmarkContainerWarmRender(b *testing.B) {
	container := makeWarmContainer(200)
	container.Render(40)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = container.Render(40)
	}
}

// TestContainerRenderReusesPrefixOnTailChange pins the streaming fix: a change
// to the last child must rewrite only the suffix in place, not re-flatten (and
// re-allocate) the whole transcript. A streaming message changes only the last
// child, which is what made a long session stutter for ~100 ms a frame.
func TestContainerRenderReusesPrefixOnTailChange(t *testing.T) {
	container := &Container{}
	for i := 0; i < 100; i++ {
		container.AddChild(NewText(fmt.Sprintf("child %d", i), 0, 0, nil))
	}
	tail := NewText("tail", 0, 0, nil)
	container.AddChild(tail)
	first := container.Render(40)

	tail.SetText("tail\ngrown")
	second := container.Render(40)

	if len(second) != len(first)+1 {
		t.Fatalf("render has %d lines, want %d", len(second), len(first)+1)
	}
	if &first[0] != &second[0] {
		t.Fatal("tail change rebuilt the whole line slice instead of rewriting the suffix")
	}
	if second[len(second)-1] != "grown"+strings.Repeat(" ", 40-len("grown")) {
		t.Fatalf("last line = %q, want the grown tail", second[len(second)-1])
	}
}

// TestContainerRenderDetectsInPlaceChildChange is the version-contract guard:
// a child Container that rewrites its suffix in place returns the same backing
// array, so slice identity alone would leave the parent serving stale lines.
func TestContainerRenderDetectsInPlaceChildChange(t *testing.T) {
	inner := &Container{}
	inner.AddChild(NewText("stable", 0, 0, nil))
	tail := NewText("before", 0, 0, nil)
	inner.AddChild(tail)

	outer := &Container{}
	outer.AddChild(inner)
	outer.Render(40)

	tail.SetText("after!") // same length: identity is unchanged, content is not
	got := outer.Render(40)

	found := false
	for _, line := range got {
		if strings.TrimRight(line, " ") == "after!" {
			found = true
		}
	}
	if !found {
		t.Fatalf("outer returned stale lines after an in-place child change: %q", got)
	}
}
