package interactive

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Lazy transcript materialization: on a large session load only the last
// window of components attaches immediately (the visible bottom), and the
// rest is attached in chunks from the UI loop, so the first paint is instant
// instead of a multi-hundred-millisecond full-transcript render.

func TestLazyTranscriptDefersLargeSessions(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	dir := t.TempDir()
	manager := coding.NewSessionManager(dir, &coding.SessionManagerOptions{Persist: boolPtr(false)})
	const count = 800
	for i := 0; i < count; i++ {
		manager.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: fmt.Sprintf("lazy-msg-%04d", i)}})
	}

	chat := &tui.Container{}
	transcript := NewTranscriptRenderer(chat, nil, nil, nil, manager)
	transcript.RenderInitialMessages()

	if len(chat.Children) >= count {
		t.Fatalf("large session attached eagerly: %d children", len(chat.Children))
	}

	for transcript.MaterializeDeferred() {
	}
	if len(transcript.deferredComponents) != 0 {
		t.Fatalf("deferred queue not drained: %d", len(transcript.deferredComponents))
	}

	// Everything attached, in order.
	lines := strings.Join(renderChat(t, chat), "\n")
	first := strings.Index(lines, "lazy-msg-0000")
	last := strings.Index(lines, "lazy-msg-0799")
	if first == -1 || last == -1 || first > last {
		t.Fatalf("order broken: first=%d last=%d", first, last)
	}
	for _, probe := range []string{"lazy-msg-0100", "lazy-msg-0400", "lazy-msg-0700"} {
		if !strings.Contains(lines, probe) {
			t.Fatalf("missing %s after materialization", probe)
		}
	}
}

func TestLazyTranscriptSmallSessionsAttachEagerly(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	dir := t.TempDir()
	manager := coding.NewSessionManager(dir, &coding.SessionManagerOptions{Persist: boolPtr(false)})
	for i := 0; i < 30; i++ {
		manager.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: fmt.Sprintf("small-msg-%02d", i)}})
	}

	chat := &tui.Container{}
	transcript := NewTranscriptRenderer(chat, nil, nil, nil, manager)
	transcript.RenderInitialMessages()

	if transcript.deferredComponents != nil {
		t.Fatalf("small session deferred: %d components", len(transcript.deferredComponents))
	}
	lines := strings.Join(renderChat(t, chat), "\n")
	if !strings.Contains(lines, "small-msg-00") || !strings.Contains(lines, "small-msg-29") {
		t.Fatal("small session messages missing")
	}
	if transcript.MaterializeDeferred() {
		t.Fatal("materialize reported work for an eager session")
	}
}

// TestLazyTranscriptLiveAppendsStayOrdered pins the interleaving rule: live
// messages appended while the deferred queue drains must stay after the
// deferred (older) entries once materialization completes.
func TestLazyTranscriptLiveAppendsStayOrdered(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	dir := t.TempDir()
	manager := coding.NewSessionManager(dir, &coding.SessionManagerOptions{Persist: boolPtr(false)})
	for i := 0; i < 800; i++ {
		manager.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: fmt.Sprintf("lazy-msg-%04d", i)}})
	}

	chat := &tui.Container{}
	transcript := NewTranscriptRenderer(chat, nil, nil, nil, manager)
	transcript.RenderInitialMessages()

	// A live user message arrives before materialization finishes.
	transcript.AddMessageToChat(&ai.UserMessage{Content: ai.StringOrBlocks{Text: "live-msg"}}, false)

	for transcript.MaterializeDeferred() {
	}
	lines := strings.Join(renderChat(t, chat), "\n")
	live := strings.Index(lines, "live-msg")
	early := strings.Index(lines, "lazy-msg-0000")
	late := strings.Index(lines, "lazy-msg-0799")
	if live == -1 || early == -1 || late == -1 || !(early < late && late < live) {
		t.Fatalf("order broken: early=%d late=%d live=%d", early, late, live)
	}
}
