package tui

import (
	"testing"
	"time"
)

// animationProbe is an animating component used to check the animation walk
// reaches it through wrappers.
type animationProbe struct{ calls int }

func (p *animationProbe) Render(width int) []string { return []string{"probe"} }
func (p *animationProbe) Invalidate()               {}
func (p *animationProbe) AnimationFrame(now time.Time) (bool, time.Duration) {
	p.calls++
	return true, time.Second
}

// TestNextAnimationDescendsThroughWrappers pins the tree walk: ScrollView keeps
// its content in a field (not the embedded Container's Children) and MouseRegion
// wraps a single child, so both must expose childComponents or an animator inside
// the transcript is never visited and its value freezes on screen.
func TestNextAnimationDescendsThroughWrappers(t *testing.T) {
	for _, tc := range []struct {
		name string
		wrap func(Component) Component
	}{
		{"MouseRegion", func(c Component) Component { return NewMouseRegion(c, nil) }},
		{"ScrollView", func(c Component) Component { return NewScrollView(c, ScrollViewOptions{}) }},
		{"ScrollView+MouseRegion", func(c Component) Component {
			return NewScrollView(NewMouseRegion(c, nil), ScrollViewOptions{})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &animationProbe{}
			renderer := NewRenderer(&fakeTerminal{width: 80, height: 24})
			renderer.AddChild(tc.wrap(probe))
			needs, delay := renderer.NextAnimation()
			if !needs || delay <= 0 {
				t.Fatalf("animator not found through %s: needs=%v delay=%v", tc.name, needs, delay)
			}
			if probe.calls == 0 {
				t.Fatal("AnimationFrame was never called")
			}
		})
	}
}

// TestNextAnimationFindsElapsedInsideFullscreenLayout builds the real fullscreen
// shape (layout root VStack -> transcript ScrollView -> document -> chat -> tool
// -> content Box -> result MouseRegion -> elapsed animator) and checks the walk
// reaches it on an AltScreen, whose mounted roots are the layout root.
func TestNextAnimationFindsElapsedInsideFullscreenLayout(t *testing.T) {
	elapsed := &animationProbe{}
	result := &Container{}
	result.AddChild(elapsed)
	region := NewMouseRegion(result, nil)
	box := NewBox(1, 1, nil)
	box.AddChild(region)
	tool := &Container{}
	tool.AddChild(box)
	chat := &Container{}
	chat.AddChild(tool)
	document := &Container{}
	document.AddChild(chat)
	transcript := NewScrollView(document, ScrollViewOptions{})
	dock := NewVStack(nil, StackOptions{})
	root := NewVStack([]Component{transcript, dock}, StackOptions{})

	screen := NewAltScreen(&fakeTerminal{width: 120, height: 40}, false, "", AltScreenOptions{})
	screen.SetLayoutRoot(root)

	needs, delay := screen.NextAnimation()
	if !needs || delay <= 0 {
		t.Fatalf("elapsed animator not reached in the fullscreen layout: needs=%v delay=%v", needs, delay)
	}
	if elapsed.calls == 0 {
		t.Fatal("AnimationFrame was never called")
	}
}
