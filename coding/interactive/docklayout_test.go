package interactive

import (
	"context"
	"fmt"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

func TestDockPinnedAfterScroll(t *testing.T) {
	app, cleanup := newTestAppB(t)
	defer cleanup()
	app.Init(context.Background())
	screen, ok := app.lifecycle.CurrentUI().(*tui.AltScreen)
	if !ok {
		t.Fatalf("UI = %T", app.lifecycle.CurrentUI())
	}
	screen.Start()
	screen.DisableAutoRender()

	// populate
	for i := 0; i < 50; i++ {
		app.events.HandleEvent(&coding.SessionEvent{Type: coding.SessionMessageStart, Agent: agentEvent("message_start", &ai.UserMessage{Content: ai.StringOrBlocks{Text: fmt.Sprintf("message %d", i)}})})
		app.events.HandleEvent(&coding.SessionEvent{Type: coding.SessionMessageEnd, Agent: agentEvent("message_end", &ai.UserMessage{Content: ai.StringOrBlocks{Text: "x"}})})
	}
	app.ui.RenderNow(false)
	screen.ScrollBy(-10)
	app.ui.RenderNow(false)
	t.Logf("transcriptScrollView=%T layoutRoot=%T", app.transcriptScrollView, screen.LayoutRoot())

	// the layout is pure: recompute it with the current scroll state
	frame := tui.RenderLayoutFrame(screen.LayoutRoot(), 100, 30, func() {})
	box, ok := tui.GetScrollViewBox(frame, app.transcriptScrollView)
	if !ok {
		t.Fatal("no transcript scroll box")
	}
	t.Logf("transcript box: %+v", box.Rect)
	boxes := map[string]*tui.LayoutBox{}
	var walk func(b *tui.LayoutBox)
	walk = func(b *tui.LayoutBox) {
		for name, component := range map[string]tui.Component{
			"editor":  app.editorContainer,
			"footer":  app.footerContainer,
			"status":  app.statusContainer,
			"above":   app.widgetAbove,
			"below":   app.widgetBelow,
			"pending": app.pendingMessages,
		} {
			if b.Component == component {
				boxes[name] = b
			}
		}
		for _, child := range b.Children {
			walk(child)
		}
	}
	walk(frame.Root)
	for _, name := range []string{"pending", "status", "above", "editor", "below", "footer"} { //nolint
		if box, ok := boxes[name]; ok {
			t.Logf("%s box: %+v", name, box.Rect)
		} else {
			t.Errorf("%s box NOT FOUND in layout", name)
		}
	}
	// The dock boxes must sit in the rows below the transcript viewport.
	if boxes["editor"].Rect.Y != 26 || boxes["editor"].Rect.Height != 3 {
		t.Fatalf("editor rect = %+v, want Y=26 height=3", boxes["editor"].Rect)
	}
	if boxes["footer"].Rect.Y != 29 || boxes["footer"].Rect.Height != 1 {
		t.Fatalf("footer rect = %+v, want Y=29 height=1", boxes["footer"].Rect)
	}
	if boxes["above"].Rect.Y != 25 || boxes["above"].Rect.Height != 1 {
		t.Fatalf("widgets-above rect = %+v, want the blank spacer at Y=25", boxes["above"].Rect)
	}
}
