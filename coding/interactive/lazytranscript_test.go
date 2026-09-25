package interactive

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/internal/offloop"
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

	for transcript.MaterializeDeferred(80) {
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
	if transcript.MaterializeDeferred(80) {
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

	for transcript.MaterializeDeferred(80) {
	}
	lines := strings.Join(renderChat(t, chat), "\n")
	live := strings.Index(lines, "live-msg")
	early := strings.Index(lines, "lazy-msg-0000")
	late := strings.Index(lines, "lazy-msg-0799")
	if live == -1 || early == -1 || late == -1 || !(early < late && late < live) {
		t.Fatalf("order broken: early=%d late=%d live=%d", early, late, live)
	}
}

// TestLazyTranscriptPrerendersOffLoop drives materialization through the
// pre-render queue: the worker warms each chunk, the loop attaches only
// warmed chunks, and the transcript still ends up complete and in order.
func TestLazyTranscriptPrerendersOffLoop(t *testing.T) {
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
	queue := offloop.New()
	defer queue.Stop()
	transcript.PrerenderQueue = queue
	transcript.RenderInitialMessages()

	if len(chat.Children) >= count {
		t.Fatalf("large session attached eagerly: %d children", len(chat.Children))
	}

	deadline := time.Now().Add(10 * time.Second)
	for transcript.MaterializeDeferred(80) {
		if time.Now().After(deadline) {
			t.Fatalf("materialization did not finish: %d deferred left", len(transcript.deferredComponents))
		}
	}
	if len(transcript.deferredComponents) != 0 {
		t.Fatalf("deferred queue not drained: %d", len(transcript.deferredComponents))
	}
	if transcript.pre.busy || transcript.pre.ready {
		t.Fatalf("prerender state not settled: busy=%v ready=%v", transcript.pre.busy, transcript.pre.ready)
	}

	lines := strings.Join(renderChat(t, chat), "\n")
	first := strings.Index(lines, "lazy-msg-0000")
	last := strings.Index(lines, "lazy-msg-0799")
	if first == -1 || last == -1 || first > last {
		t.Fatalf("order broken: first=%d last=%d", first, last)
	}
}

// TestLazyTranscriptPrerenderRewarmsAfterWidthChange pins that a chunk warmed
// for one width is discarded rather than attached when the terminal width has
// moved (the render cache is keyed by width, so attaching it would just make
// the loop re-render the same components).
func TestLazyTranscriptPrerenderRewarmsAfterWidthChange(t *testing.T) {
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
	queue := offloop.New()
	defer queue.Stop()
	transcript.PrerenderQueue = queue
	transcript.RenderInitialMessages()

	// Kick a warm at one width, then drive the rest at another.
	transcript.MaterializeDeferred(80)
	deadline := time.Now().Add(10 * time.Second)
	for transcript.MaterializeDeferred(100) {
		if time.Now().After(deadline) {
			t.Fatal("materialization did not finish after a width change")
		}
	}
	if len(transcript.deferredComponents) != 0 {
		t.Fatalf("deferred queue not drained: %d", len(transcript.deferredComponents))
	}
	lines := strings.Join(renderChat(t, chat), "\n")
	if !strings.Contains(lines, "lazy-msg-0000") || !strings.Contains(lines, "lazy-msg-0799") {
		t.Fatal("transcript incomplete after a width change")
	}
}

// TestLazyTranscriptDefersWithoutPopulatingHistory pins the decoupling: a theme
// rebuild renders with populateHistory=false and used to bypass the lazy path,
// eagerly attaching the whole (large) session on the UI loop.
func TestLazyTranscriptDefersWithoutPopulatingHistory(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	dir := t.TempDir()
	manager := coding.NewSessionManager(dir, &coding.SessionManagerOptions{Persist: boolPtr(false)})
	for i := 0; i < 800; i++ {
		manager.AppendMessage(&ai.UserMessage{Content: ai.StringOrBlocks{Text: fmt.Sprintf("nohist-msg-%04d", i)}})
	}

	chat := &tui.Container{}
	transcript := NewTranscriptRenderer(chat, nil, nil, nil, manager)
	transcript.RenderSessionEntries(manager.BuildContextEntriesForLeaf(), false, false)

	if len(transcript.deferredComponents) == 0 {
		t.Fatal("large render with populateHistory=false did not defer")
	}
	if len(chat.Children) > lazyTranscriptWindowComponents {
		t.Fatalf("chat attached too many components: %d", len(chat.Children))
	}
	for transcript.MaterializeDeferred(80) {
	}
	lines := strings.Join(renderChat(t, chat), "\n")
	if !strings.Contains(lines, "nohist-msg-0000") || !strings.Contains(lines, "nohist-msg-0799") {
		t.Fatal("deferred render incomplete after materialization")
	}
}
