package interactive

import (
	"testing"
	"time"

	"github.com/dat267/pier/tui"
)

// quietProbe is an animator that reports no frame needed, counting the walks it
// takes part in.
type quietProbe struct {
	tui.Component
	frames int
}

func (q *quietProbe) AnimationFrame(time.Time) (bool, time.Duration) {
	q.frames++
	return false, 0
}
func (q *quietProbe) Render(int) []string { return nil }
func (q *quietProbe) Invalidate()         {}

// TestArmAnimationArmsALiveAnimatorWithoutAnActiveTurn covers the state the
// running-turn fallback cannot reach: the walk's "nothing animates" result was
// cached before a tool component appeared, and no turn is active to force an
// unconditional re-walk. Upstream's per-call setInterval keeps ticking in every
// such state, so the loop must still arm — otherwise the shell elapsed label on
// screen has nothing that repaints it and freezes until some unrelated paint
// drops the cache.
func TestArmAnimationArmsALiveAnimatorWithoutAnActiveTurn(t *testing.T) {
	wiring, _ := newRunTestWiring(t)
	probe := &animationProbe{}
	wiring.Chat.AddChild(probe)

	// The fullscreen shape, so the walk reaches the chat.
	document := &tui.Container{}
	document.AddChild(wiring.Chat)
	transcript := tui.NewScrollView(document, tui.ScrollViewOptions{Follow: "end", Primary: true})
	root := tui.NewVStack(nil, tui.StackOptions{})
	root.AddChild(transcript)
	screen := tui.NewAltScreen(&fakeRendererTerminal{width: 80, height: 24}, false, t.TempDir(), tui.AltScreenOptions{})
	screen.SetLayoutRoot(root)
	wiring.UI = screen

	// A scan ran before the component appeared and found nothing; only a paint
	// drops that cache, and no turn is active to force the re-walk. The scan is
	// older than the boxed interval, which is the worst case a user can sit in:
	// the label is on screen and nothing has painted since.
	wiring.animationScanValid = true
	wiring.animationScanNeeds = false
	wiring.animationScanAt = time.Now().Add(-2 * time.Second)
	wiring.work.active = false

	timer := time.NewTimer(time.Hour)
	timer.Stop()
	var deadline time.Time
	ch := wiring.armAnimation(timer, &deadline)
	if ch == nil || deadline.IsZero() {
		t.Fatalf("a live animator was not armed with no turn active (probe frames=%d): its label would freeze", probe.frames)
	}
	if probe.frames == 0 {
		t.Fatal("the animation walk never ran, so the cached scan was trusted indefinitely")
	}
}

// TestArmAnimationTimeBoxesANothingAnimatesScan pins the other half of the fix:
// a cached "nothing animates" still short-circuits the walk while it is fresh, so
// an input event that paints nothing does not pay for the walk (D164) — it is only
// re-walked once the boxed interval has passed.
func TestArmAnimationTimeBoxesANothingAnimatesScan(t *testing.T) {
	wiring, _ := newRunTestWiring(t)
	probe := &quietProbe{}
	wiring.Chat.AddChild(probe)
	document := &tui.Container{}
	document.AddChild(wiring.Chat)
	transcript := tui.NewScrollView(document, tui.ScrollViewOptions{Follow: "end", Primary: true})
	root := tui.NewVStack(nil, tui.StackOptions{})
	root.AddChild(transcript)
	screen := tui.NewAltScreen(&fakeRendererTerminal{width: 80, height: 24}, false, t.TempDir(), tui.AltScreenOptions{})
	screen.SetLayoutRoot(root)
	wiring.UI = screen

	timer := time.NewTimer(time.Hour)
	timer.Stop()
	var deadline time.Time

	// First arm walks (nothing animates), the second reuses the fresh cache.
	wiring.armAnimation(timer, &deadline)
	if probe.frames != 1 {
		t.Fatalf("first arm walked %d times, want 1", probe.frames)
	}
	wiring.armAnimation(timer, &deadline)
	if probe.frames != 1 {
		t.Fatalf("a fresh \"nothing animates\" scan was re-walked (%d walks)", probe.frames)
	}

	// Past the boxed interval the scan is refreshed, so a component that started
	// animating cannot hide behind it.
	wiring.animationScanAt = time.Now().Add(-2 * time.Second)
	wiring.armAnimation(timer, &deadline)
	if probe.frames != 2 {
		t.Fatalf("a stale scan was not re-walked (%d walks)", probe.frames)
	}
}
