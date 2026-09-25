package interactive

import (
	"testing"
	"time"

	"github.com/dat267/pier/tui"
)

// elapsedTestComponent renders a line that changes with its value, mimicking the
// shell elapsed label.
type elapsedTestComponent struct{ value string }

func (c *elapsedTestComponent) Render(int) []string { return []string{"Elapsed " + c.value} }
func (c *elapsedTestComponent) Invalidate()         {}
func (c *elapsedTestComponent) AnimationFrame(time.Time) (bool, time.Duration) {
	return true, time.Second
}

// TestAltScreenWritesTheUpdatedElapsed pins that a repaint through the real
// fullscreen paint path (AltScreen.doRender -> RenderLayoutFrame -> scroll
// content) writes a changed elapsed line without any interaction. If this ever
// returns cached lines, the label freezes until the next click.
func TestAltScreenWritesTheUpdatedElapsed(t *testing.T) {
	terminal := &recordingTerminal{fakeRendererTerminal: &fakeRendererTerminal{width: 60, height: 10}}
	screen := tui.NewAltScreen(terminal, false, t.TempDir(), tui.AltScreenOptions{})
	t.Cleanup(func() { screen.Stop(tui.TuiStopOptions{}) })

	elapsed := &elapsedTestComponent{value: "1.0s"}
	chat := &tui.Container{}
	chat.AddChild(elapsed)
	document := &tui.Container{}
	document.AddChild(chat)
	transcript := tui.NewScrollView(document, tui.ScrollViewOptions{Follow: "end", Primary: true})
	root := tui.NewVStack(nil, tui.StackOptions{})
	root.AddChild(transcript)
	screen.SetLayoutRoot(root)

	screen.Start()
	screen.RenderNow(false)
	screen.RenderNow(false)
	if !terminal.hasWrite("1.0s") {
		t.Fatalf("initial elapsed not written; writes=%q", terminal.joinedWrites())
	}

	elapsed.value = "2.0s"
	screen.RenderNow(false)
	if !terminal.hasWrite("2.0s") {
		t.Fatalf("updated elapsed not written; writes=%q", terminal.joinedWrites())
	}
}
