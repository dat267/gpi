package interactive

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
)

// TestStreamingBashCommandTabRendersEarly covers the reported symptom: a tab in
// a streamed command only rendered once the call finished. The decode side is
// pinned in ai.TestParseStreamingEscapes; here the arguments arrive as the ai
// layer delivers them after that decode (a repaired document whose command
// holds a real tab), and the header must render it expanded and stay stable.
func TestStreamingBashCommandTabRendersEarly(t *testing.T) {
	dispatcher, _, transcript, _ := newEventTestDispatcher(t)
	transcript.Session = builtinRendererSession{}

	bashCall := func(arguments string) *ai.AssistantMessage {
		return &ai.AssistantMessage{
			API: ai.APIAnthropicMessages, Provider: "test", Model: "m", StopReason: ai.StopToolUse,
			Content: ai.ContentList{ai.ToolCall{ID: "t1", Name: "bash", Arguments: json.RawMessage(arguments)}},
		}
	}
	dispatcher.HandleEvent(&coding.SessionEvent{
		Type: coding.SessionMessageStart, Agent: agentEvent("message_start", &ai.AssistantMessage{StopReason: ai.StopToolUse}),
	})

	// Mid-stream: the ai layer has decoded the partial JSON (the command holds
	// a real tab) and re-encoded it.
	dispatcher.HandleEvent(&coding.SessionEvent{
		Type: coding.SessionMessageUpdate, Agent: agentEvent("message_update", bashCall(`{"command":"echo\tfoo"}`)),
	})
	streaming := coding.StripAnsi(strings.Join(renderChat(t, transcript.Chat), "\n"))
	if !strings.Contains(streaming, "echo   foo") {
		t.Fatalf("streaming header = %q, want the tab expanded to three spaces", streaming)
	}
	if strings.Contains(streaming, `echo\tfoo`) {
		t.Fatalf("streaming header kept the escape verbatim: %q", streaming)
	}

	// Completed: the header must not change.
	dispatcher.HandleEvent(&coding.SessionEvent{
		Type: coding.SessionMessageUpdate, Agent: agentEvent("message_update", bashCall(`{"command":"echo\tfoo"}`)),
	})
	completed := coding.StripAnsi(strings.Join(renderChat(t, transcript.Chat), "\n"))
	if !strings.Contains(completed, "echo   foo") {
		t.Fatalf("completed header = %q", completed)
	}
	if streaming != completed {
		t.Fatalf("header changed on completion:\n streaming %q\n completed %q", streaming, completed)
	}
}
