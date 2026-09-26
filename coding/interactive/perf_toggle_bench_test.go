package interactive

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// toggleBenchMessages is the transcript length for the thinking-toggle
// benchmarks: long enough that per-message work shows up, short enough to run.
var toggleBenchMessages = 200

func buildToggleTranscript(b *testing.B, app *App) {
	b.Helper()
	buildToggleTranscriptTo(b, app)
}

// buildToggleTranscriptTo is buildToggleTranscript with a testing.TB, so tests
// can reuse it too.
func buildToggleTranscriptTo(b testing.TB, app *App) {
	b.Helper()
	thinking := strings.Repeat(
		"Let me work through this carefully, weighing the alternatives and checking the edge cases as I go.\n", 40)
	reply := strings.Repeat(
		"Here is the answer, with **emphasis** and `code`:\n\n```go\nfmt.Println(\"hi\")\n```\n", 8)

	for i := 0; i < toggleBenchMessages; i++ {
		user := &ai.UserMessage{Content: ai.StringOrBlocks{Text: fmt.Sprintf("question %d", i)}}
		app.events.HandleEvent(&coding.SessionEvent{
			Type: coding.SessionMessageStart, Agent: agentEvent("message_start", user),
		})
		assistant := &ai.AssistantMessage{
			API: ai.APIAnthropicMessages, Provider: "test", Model: "m",
			Content: ai.ContentList{
				ai.ThinkingContent{Thinking: thinking},
				ai.TextContent{Text: reply},
			},
		}
		app.events.HandleEvent(&coding.SessionEvent{
			Type: coding.SessionMessageStart, Agent: agentEvent("message_start", assistant),
		})
		app.events.HandleEvent(&coding.SessionEvent{
			Type: coding.SessionMessageEnd, Agent: agentEvent("message_end", assistant),
		})
	}
}

// BenchmarkToggleThinkingFull measures ctrl+t end to end: flip the setting,
// rebuild every assistant message and lay the transcript out.
//
// The layout is asked for through app.Chat.Render rather than UI.RenderNow: the
// benchmark harness never starts the screen, so a frame is a no-op there and
// timing it measures only the rebuild (which is not where the cost is).
func BenchmarkToggleThinkingFull(b *testing.B) {
	app, cleanup := newTestAppB(b)
	defer cleanup()
	buildToggleTranscript(b, app)
	_ = app.chat.Render(80)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		app.queue.ToggleThinkingBlockVisibility(&app.display.HideThinkingBlock)
		_ = app.chat.Render(80)
	}
}

// BenchmarkToggleThinkingRebuild measures only the visibility rebuild across the
// transcript, without the settings write or the repaint.
func BenchmarkToggleThinkingRebuild(b *testing.B) {
	app, cleanup := newTestAppB(b)
	defer cleanup()
	screen, _ := app.initialUI.(*tui.AltScreen)
	screen.Start()
	screen.DisableAutoRender()
	buildToggleTranscript(b, app)
	app.ui.RenderNow(true)

	hide := false
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hide = !hide
		app.queue.UpdateThinkingBlockVisibility(hide)
	}
}

// BenchmarkToggleThinkingRepaint measures a repaint after the rebuild, which is
// what the toggle's visible cost is on top of the rebuild.
func BenchmarkToggleThinkingRepaint(b *testing.B) {
	app, cleanup := newTestAppB(b)
	defer cleanup()
	screen, _ := app.initialUI.(*tui.AltScreen)
	screen.Start()
	screen.DisableAutoRender()
	buildToggleTranscript(b, app)
	app.ui.RenderNow(true)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		app.ui.RenderNow(true)
	}
}

// The toggle reuses the cached thinking markdown, so the round trip has to stay
// correct as well as cheap: collapsing and re-expanding must reproduce the
// original render exactly, and a message whose thinking really changed must show
// the new text rather than the cached one. A streaming transition and a padding
// change have to re-render too, because the cached lines bake both in.
func TestThinkingToggleRoundTripRender(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	thinking := "# heading\n\nA long thought with `code` and a list:\n\n- one\n- two\n"
	message := &ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "test", Model: "m",
		Content: ai.ContentList{
			ai.ThinkingContent{Thinking: thinking},
			ai.TextContent{Text: "the answer"},
		},
	}
	component := NewAssistantMessageComponent(message, false, nil, "", 1, nil)
	expanded := strings.Join(component.Render(80), "\n")
	if !strings.Contains(expanded, "heading") || !strings.Contains(expanded, "the answer") {
		t.Fatalf("expanded render is missing content:\n%s", expanded)
	}

	component.SetHideThinkingBlock(true)
	collapsed := strings.Join(component.Render(80), "\n")
	if strings.Contains(collapsed, "heading") {
		t.Errorf("collapsed render still shows the thinking text:\n%s", collapsed)
	}
	if !strings.Contains(collapsed, "the answer") {
		t.Errorf("collapsed render lost the text block:\n%s", collapsed)
	}

	component.SetHideThinkingBlock(false)
	if reExpanded := strings.Join(component.Render(80), "\n"); reExpanded != expanded {
		t.Errorf("re-expanding changed the render:\n got %s\nwant %s", reExpanded, expanded)
	}

	// A real change while hidden must not be served from the cache.
	updated := &ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "test", Model: "m",
		Content: ai.ContentList{
			ai.ThinkingContent{Thinking: thinking + "\nand a new closing thought"},
			ai.TextContent{Text: "the answer"},
		},
	}
	component.SetHideThinkingBlock(true)
	component.UpdateContent(updated, false)
	component.SetHideThinkingBlock(false)
	fresh := strings.Join(component.Render(80), "\n")
	if !strings.Contains(fresh, "and a new closing thought") {
		t.Errorf("the changed thinking text did not render:\n%s", fresh)
	}

	// Padding and streaming transitions are baked into the cached lines.
	component.SetOutputPad(3)
	if padded := strings.Join(component.Render(80), "\n"); padded == fresh {
		t.Error("a padding change did not re-render")
	}
	component.Invalidate()
	if invalidated := strings.Join(component.Render(80), "\n"); !strings.Contains(invalidated, "new closing thought") {
		t.Errorf("Invalidate dropped content:\n%s", invalidated)
	}
}

// The streaming transform renders differently for the same text, so the
// transition has to drop the reused caches — the text-equality checks cannot see
// it. A transformer that reports the streaming state makes the difference
// observable.
func TestStreamingTransitionRerenders(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	transformer := func(markdown string, context MarkdownTransformContext) (string, bool) {
		if context.IsStreaming {
			return markdown + "\nstreaming-marker", true
		}
		return markdown, true
	}
	message := &ai.AssistantMessage{
		API: ai.APIAnthropicMessages, Provider: "test", Model: "m",
		Content: ai.ContentList{ai.TextContent{Text: "hello"}},
	}
	component := NewAssistantMessageComponent(message, false, nil, "", 1, []MarkdownTransformer{transformer})
	component.UpdateContent(message, true)
	if got := strings.Join(component.Render(80), "\n"); !strings.Contains(got, "streaming-marker") {
		t.Fatalf("the streaming render is missing the marker:\n%s", got)
	}

	component.UpdateContent(message, false)
	got := strings.Join(component.Render(80), "\n")
	if strings.Contains(got, "streaming-marker") {
		t.Errorf("the finished render still shows the streaming output:\n%s", got)
	}
}

// BenchmarkToggleThinkingFirstExpand measures expanding a transcript that was
// built with thinking hidden: the collapsed render never lexed the thinking
// text, so the first expansion has to, and it is the layout that pays.
// Everything after that is the reuse path, so with -benchtime=Nx the first
// iteration is the cold expansion and the rest are warm.
func BenchmarkToggleThinkingFirstExpand(b *testing.B) {
	app, cleanup := newTestAppB(b)
	defer cleanup()
	app.display.HideThinkingBlock = true
	buildToggleTranscript(b, app)
	_ = app.chat.Render(80)
	app.display.HideThinkingBlock = false

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		app.queue.UpdateThinkingBlockVisibility(false)
		_ = app.chat.Render(80)
	}
}

// BenchmarkToggleThinkingDrain measures the whole sweep converged: the
// synchronous window plus every deferred chunk. The freeze is the synchronous
// part (the rest is spread over loop beats, ~8 messages per frame), so this is
// the total work rather than what any one frame pays.
func BenchmarkToggleThinkingDrain(b *testing.B) {
	app, cleanup := newTestAppB(b)
	defer cleanup()
	app.display.HideThinkingBlock = true
	buildToggleTranscript(b, app)
	_ = app.chat.Render(80)

	// Start on the expansion, which is the direction that has to render the
	// thinking text for the first time.
	hide := true
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hide = !hide
		app.queue.UpdateThinkingBlockVisibility(hide)
		for app.queue.MaterializeThinkingChunk() {
		}
		_ = app.chat.Render(80)
	}
}
