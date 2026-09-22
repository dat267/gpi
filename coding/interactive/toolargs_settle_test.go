package interactive

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
)

// builtinRendererSession resolves the built-in tool renderers so the tests
// render the real call header instead of the JSON fallback.
type builtinRendererSession struct{}

func (builtinRendererSession) GetToolRenderers(toolName string) *ToolRenderers {
	return WithBuiltInRenderers(toolName, nil)
}
func (builtinRendererSession) GetRetryAttempt() int                         { return 0 }
func (builtinRendererSession) GetModelPriceSource() coding.ModelPriceSource { return nil }

// TestToolCallArgsSettleWhenComplete covers a streamed tool call whose
// arguments are still incomplete when the last partial update arrives (the
// read tool sends `path` last, so mid-stream the header shows a cut path).
// The authoritative arguments arrive with the final message and with the
// execution-start event; the header must settle on the full path instead of
// keeping the truncated one.
func TestToolCallArgsSettleWhenComplete(t *testing.T) {
	// The paths are absolute and the expectation goes through ShortenPath, so
	// the test does not depend on where the home directory is.
	const filePath = "/srv/work/pier/tui/component.go"
	const full = `{"limit":60,"offset":285,"path":"` + filePath + `"}`
	const partial = `{"limit":60,"offset":285,"path":"/srv/work/pier/tui/comp"}`
	wantFull := ShortenPath(filePath) + ":285-344"

	assistant := func(raw string) *ai.AssistantMessage {
		return &ai.AssistantMessage{
			Content:    ai.ContentList{ai.ToolCall{ID: "t1", Name: "read", Arguments: json.RawMessage(raw)}},
			StopReason: ai.StopToolUse,
		}
	}
	stream := func(t *testing.T) (*EventDispatcher, *TranscriptRenderer) {
		t.Helper()
		dispatcher, _, transcript, _ := newEventTestDispatcher(t)
		transcript.Session = builtinRendererSession{}
		dispatcher.HandleEvent(&coding.SessionEvent{
			Type: coding.SessionMessageStart, Agent: agentEvent("message_start", &ai.AssistantMessage{StopReason: ai.StopToolUse}),
		})
		dispatcher.HandleEvent(&coding.SessionEvent{
			Type: coding.SessionMessageUpdate, Agent: agentEvent("message_update", assistant(partial)),
		})
		return dispatcher, transcript
	}
	header := func(t *testing.T, transcript *TranscriptRenderer) string {
		t.Helper()
		return coding.StripAnsi(strings.Join(renderChat(t, transcript.Chat), "\n"))
	}

	t.Run("final message completes the arguments", func(t *testing.T) {
		dispatcher, transcript := stream(t)
		if got := header(t, transcript); strings.Contains(got, wantFull) {
			t.Fatalf("the partial update was expected to render a cut path, got %q", got)
		}
		dispatcher.HandleEvent(&coding.SessionEvent{
			Type: coding.SessionMessageEnd, Agent: agentEvent("message_end", assistant(full)),
		})
		if got := header(t, transcript); !strings.Contains(got, wantFull) {
			t.Fatalf("header after message_end = %q, want the full path", got)
		}
	})

	t.Run("execution start completes the arguments", func(t *testing.T) {
		dispatcher, transcript := stream(t)
		dispatcher.HandleEvent(&coding.SessionEvent{
			Type: coding.SessionToolExecutionStart,
			Agent: &agent.AgentEvent{
				Type: "tool_execution_start", ToolCallID: "t1", ToolName: "read", Args: json.RawMessage(full),
			},
		})
		if got := header(t, transcript); !strings.Contains(got, wantFull) {
			t.Fatalf("header after tool_execution_start = %q, want the full path", got)
		}
	})
}
