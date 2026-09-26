package coding

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
)

// TestModelSwitchMidTurnAppliesToTheNextRequest pins upstream's
// prepareNextTurnWithContext behavior (agent-session.ts): a model or
// thinking-level change made while a turn is running must reach the next
// assistant request in that same turn, not only the next prompt. The port's
// AgentLoopTurnUpdate carried only the context, so the loop's config.Model /
// config.Reasoning refresh (agent-loop.ts nextTurnSnapshot) was dead.
func TestModelSwitchMidTurnAppliesToTheNextRequest(t *testing.T) {
	first := &ai.Model{ID: "first", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 100000, MaxTokens: 8192, Reasoning: true}
	second := &ai.Model{ID: "second", API: ai.APIAnthropicMessages, Provider: "anthropic", ContextWindow: 100000, MaxTokens: 8192, Reasoning: true}

	var session *AgentSession
	var mu sync.Mutex
	var models []string
	var reasoning []string
	streamFn := func(model *ai.Model, transcript ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		mu.Lock()
		models = append(models, model.ID)
		reasoning = append(reasoning, options.Reasoning)
		call := len(models)
		mu.Unlock()

		stream := ai.NewAssistantMessageEventStream()
		go func() {
			if call == 1 {
				// The user switches both mid-turn, while the tool-calling
				// response is in flight.
				if err := session.SetModel(context.Background(), second, ModelMutationOptions{}); err != nil {
					msg := err.Error()
					failed := &ai.AssistantMessage{
						API: ai.APIAnthropicMessages, Provider: model.Provider, Model: model.ID,
						StopReason: ai.StopError, ErrorMessage: &msg,
					}
					stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopError, Error: failed})
					stream.End(&failed)
					return
				}
				session.SetThinkingLevel(ai.ThinkHigh, ModelMutationOptions{})
				stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopToolUse, Message: &ai.AssistantMessage{
					API: ai.APIAnthropicMessages, Provider: model.Provider, Model: model.ID,
					Content:    ai.ContentList{ai.ToolCall{ID: "t1", Name: "read", Arguments: json.RawMessage(`{}`)}},
					StopReason: ai.StopToolUse,
				}})
				return
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: &ai.AssistantMessage{
				API: ai.APIAnthropicMessages, Provider: model.Provider, Model: model.ID,
				Content: ai.ContentList{ai.TextContent{Text: "done"}}, StopReason: ai.StopStop,
			}})
		}()
		return stream
	}

	var err error
	session, err = NewAgentSession(&SessionConfig{
		Cwd: t.TempDir(), Model: first, StreamFn: streamFn,
		Tools: []agent.AgentTool{testTool("read")},
	})
	if err != nil {
		t.Fatal(err)
	}
	session.control = &AgentSessionControl{}

	if err := session.PromptText(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(models) != 2 {
		t.Fatalf("stream calls = %v", models)
	}
	if want := []string{"first", "second"}; strings.Join(models, ",") != strings.Join(want, ",") {
		t.Fatalf("models per request = %v; want %v", models, want)
	}
	if len(reasoning) != 2 || reasoning[1] != "high" {
		t.Fatalf("reasoning per request = %v; want second request \"high\"", reasoning)
	}
}
