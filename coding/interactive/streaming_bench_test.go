package interactive

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// BenchmarkStreamingAssistantDelta measures one streaming update: the
// assistant message is re-rendered with a longer body plus one full frame. The
// message component and its Markdown keep their incremental caches, so the
// cost should track the appended tail, not the whole transcript.
func BenchmarkStreamingAssistantDelta(b *testing.B) {
	app, cleanup := newTestAppB(b)
	defer cleanup()

	screen, _ := app.initialUI.(*tui.AltScreen)
	screen.Start()
	screen.DisableAutoRender()

	// A warm transcript to render around.
	for i := 0; i < 200; i++ {
		assistant := &ai.AssistantMessage{
			API: ai.APIAnthropicMessages, Provider: "test", Model: "m",
			StopReason: ai.StopStop,
			Content: ai.ContentList{ai.TextContent{Text: fmt.Sprintf(
				"# reply %d\n\n```go\nfunc f%d() int { return %d }\n```\n\nsome paragraph with **bold** and `code` spans that wraps across the viewport width\n", i, i, i)}},
		}
		app.events.HandleEvent(&coding.SessionEvent{
			Type: coding.SessionMessageStart, Agent: agentEvent("message_start", assistant),
		})
		app.events.HandleEvent(&coding.SessionEvent{
			Type: coding.SessionMessageEnd, Agent: agentEvent("message_end", assistant),
		})
	}

	var paragraphs []string
	for i := 0; i < 400; i++ {
		paragraphs = append(paragraphs, fmt.Sprintf(
			"Paragraph %d with **bold**, `code` and a [link](https://example.com/%d) that wraps.\n\n", i, i))
	}
	streaming := &ai.AssistantMessage{API: ai.APIAnthropicMessages, Provider: "test", Model: "m", StopReason: ai.StopPending}
	app.events.HandleEvent(&coding.SessionEvent{
		Type: coding.SessionMessageStart, Agent: agentEvent("message_start", streaming),
	})

	// A long in-flight message body (what a streaming reply looks like mid-turn).
	var body strings.Builder
	for _, paragraph := range paragraphs {
		body.WriteString(paragraph)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		body.WriteString(paragraphs[i%len(paragraphs)])
		message := &ai.AssistantMessage{
			API: ai.APIAnthropicMessages, Provider: "test", Model: "m", StopReason: ai.StopPending,
			Content: ai.ContentList{ai.TextContent{Text: body.String()}},
		}
		app.events.HandleEvent(&coding.SessionEvent{
			Type: coding.SessionMessageUpdate, Agent: agentEvent("message_update", message),
		})
		app.ui.RenderNow(true)
	}
}
