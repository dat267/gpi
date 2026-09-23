package interactive

import (
	"strings"
	"testing"

	"github.com/dat267/pier/tui"
)

// transcriptTexts collects the chat's text components, which is where a status
// line lands (upstream showStatus appends a dim line to the chat container).
func transcriptTexts(app *App) []string {
	texts := make([]string, 0, len(app.Chat.Children))
	for _, child := range app.Chat.Children {
		if text, ok := child.(*tui.Text); ok {
			texts = append(texts, text.Text())
		}
	}
	return texts
}

func transcriptHasStatus(app *App, needle string) bool {
	for _, text := range transcriptTexts(app) {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

// The queue controller reports through injected functions. They were never
// assigned, so every status it raised was dropped: the toggle still flipped the
// setting and re-rendered, but nothing told the user, which reads as the key
// doing nothing at all.
func TestQueueStatusReportersAreWired(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	if app.Queue.ShowStatus == nil {
		t.Error("the queue has no status reporter, so its statuses are silent")
	}
	if app.Queue.ShowError == nil {
		t.Error("the queue has no error reporter, so its failures are silent")
	}
	if app.Queue.ShowWarning == nil {
		t.Error("the queue has no warning reporter")
	}
}

// ctrl+t reports the new state the way upstream does ("Thinking blocks: …"),
// and consecutive statuses replace each other rather than stacking.
func TestThinkingToggleEmitsTheStatus(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	wiring := newKeyWiring(app)
	if app.Settings.GetHideThinkingBlock() {
		t.Fatal("the test app should start with thinking blocks visible")
	}

	wiring.OnThinkingToggle()
	if !transcriptHasStatus(app, "Thinking blocks: hidden") {
		t.Errorf("no hidden status; texts = %q", transcriptTexts(app))
	}

	wiring.OnThinkingToggle()
	if !transcriptHasStatus(app, "Thinking blocks: visible") {
		t.Errorf("no visible status; texts = %q", transcriptTexts(app))
	}
	if count := len(transcriptTexts(app)); count != 1 {
		t.Errorf("statuses stacked instead of replacing: %d text components", count)
	}
}
