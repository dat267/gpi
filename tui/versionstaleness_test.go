package tui

import (
	"strings"
	"testing"
	"time"
)

// versionedLabel models a revision-carrying wrapper around clock-driven content:
// it reports the revision its cached lines were built at, Invalidate moves that
// revision the way a Container does, and it asks to be repainted once a second.
type versionedLabel struct {
	text    string
	version uint64
}

func (v *versionedLabel) Render(int) []string           { return []string{v.text} }
func (v *versionedLabel) Invalidate()                   { v.version++ }
func (v *versionedLabel) RenderVersion() (uint64, bool) { return v.version, true }
func (v *versionedLabel) AnimationFrame(time.Time) (bool, time.Duration) {
	return true, time.Second
}

// TestAnimationFrameInvalidatesWhatItAnimates reproduces the frozen elapsed
// label and pins the fix.
//
// Container.Render renders every child, then asks firstChangedChild what
// changed. For a child reporting a revision, an unchanged revision is taken as
// proof of an unchanged render — the freshly rendered lines are never compared —
// so it returns -1 and Render hands back the previous cacheLines, throwing the
// fresh render away. Clock-driven content is exactly that case: it changes with
// no Invalidate, so the frame the loop keeps painting stays stale until a click
// on the block invalidates the wrapper by hand.
//
// The tick is what must invalidate, as upstream's setInterval(context.invalidate)
// does. Asking for the next frame therefore has to move the revision, or nothing
// else will.
func TestAnimationFrameInvalidatesWhatItAnimates(t *testing.T) {
	label := &versionedLabel{text: "Elapsed 1.2s"}
	container := &Container{}
	container.AddChild(label)

	first := strings.Join(container.Render(40), "\n")
	if !strings.Contains(first, "1.2s") {
		t.Fatalf("first render = %q", first)
	}

	// Four seconds on, with nothing invalidated: the staleness the tick must cure.
	label.text = "Elapsed 4.5s"
	if second := strings.Join(container.Render(40), "\n"); strings.Contains(second, "4.5s") {
		t.Fatal("no staleness without an invalidate: the cache no longer skips a moved revision")
	}

	// One tick: the loop asks for the next frame.
	if needs, _ := nextAnimationFor([]Component{container}, time.Now()); !needs {
		t.Fatal("the label did not report an animation frame")
	}

	third := strings.Join(container.Render(40), "\n")
	if !strings.Contains(third, "4.5s") {
		t.Fatalf("the tick painted a stale frame: %q", third)
	}
}
