package coding

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
)

// AgentSession tests keyed to upstream agent-session.ts semantics.

func mockSessionStreamFn(responses ...*ai.AssistantMessage) agent.StreamFn {
	var mu sync.Mutex
	return func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			mu.Lock()
			var response *ai.AssistantMessage
			if len(responses) > 0 {
				response = responses[0]
				responses = responses[1:]
			}
			mu.Unlock()
			if response == nil {
				response = createAssistantMessageT("(exhausted)")
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: response.StopReason, Message: response})
		}()
		return stream
	}
}

func TestAgentSessionPersistence(t *testing.T) {
	dir := t.TempDir()
	sessions := NewSessionManager(dir, &SessionManagerOptions{SessionDir: dir})
	session, err := NewAgentSession(&SessionConfig{
		Cwd: dir, Model: &ai.Model{ID: "mock", API: "openai-responses", Provider: "openai", ContextWindow: 200000},
		StreamFn: mockSessionStreamFn(createAssistantMessageT("reply")),
		Sessions: sessions,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.PromptText(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}

	entries := sessions.GetEntries()
	// user + assistant persisted from agent events.
	if len(entries) != 2 {
		t.Fatalf("entries = %d", len(entries))
	}
	role := func(raw json.RawMessage) string {
		var probe struct {
			Role string `json:"role"`
		}
		json.Unmarshal(raw, &probe)
		return probe.Role
	}
	if role(entries[0].Message) != "user" || role(entries[1].Message) != "assistant" {
		t.Fatalf("roles = %s/%s", role(entries[0].Message), role(entries[1].Message))
	}
}

func TestAgentSessionQueueTracking(t *testing.T) {
	dir := t.TempDir()
	session, err := NewAgentSession(&SessionConfig{
		Cwd: dir, Model: &ai.Model{ID: "mock", API: "openai-responses", Provider: "openai", ContextWindow: 200000},
		StreamFn: mockSessionStreamFn(createAssistantMessageT("r1"), createAssistantMessageT("r2")),
		Sessions: NewSessionManager(dir, &SessionManagerOptions{SessionDir: dir}),
	})
	if err != nil {
		t.Fatal(err)
	}

	var queueUpdates []string
	session.Subscribe(func(event *SessionEvent) {
		if event.Type == SessionQueueUpdate {
			queueUpdates = append(queueUpdates, strings.Join(event.Steering, "|"))
		}
	})

	session.Steer(createUserMessage("steered"))
	if len(queueUpdates) != 1 {
		t.Fatalf("queue updates = %v", queueUpdates)
	}
	// Run: the steering message is delivered and removed from the display
	// queue at message_start.
	if err := session.PromptText(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	if len(session.steeringMessages) != 0 {
		t.Fatalf("steering display queue = %v", session.steeringMessages)
	}
}

func TestAgentSessionStats(t *testing.T) {
	dir := t.TempDir()
	sessions := NewSessionManager(dir, &SessionManagerOptions{SessionDir: dir})
	session, _ := NewAgentSession(&SessionConfig{
		Cwd: dir, Model: &ai.Model{ID: "mock", API: "openai-responses", Provider: "openai", ContextWindow: 200000},
		StreamFn: mockSessionStreamFn(&ai.AssistantMessage{
			Content: ai.ContentList{
				ai.TextContent{Text: "reply"},
				ai.ToolCall{ID: "t1", Name: "bash", Arguments: json.RawMessage(`{}`)},
			},
			API: "openai-responses", Provider: "openai", Model: "mock",
			Usage:      ai.Usage{Input: 100, Output: 50, TotalTokens: 150, Cost: ai.UsageCost{Total: 0.5}},
			StopReason: ai.StopToolUse, Timestamp: 1,
		}, &ai.AssistantMessage{
			Content: ai.ContentList{ai.TextContent{Text: "done"}},
			API:     "openai-responses", Provider: "openai", Model: "mock",
			Usage:      ai.Usage{Input: 30, Output: 10, TotalTokens: 40, Cost: ai.UsageCost{Total: 0.1}},
			StopReason: ai.StopStop, Timestamp: 2,
		}),
		Sessions: sessions,
	})
	// Tool call round: bash result.
	session.Agent.SetTools([]agent.AgentTool{makeTool("bash")})
	if err := session.PromptText(context.Background(), "run"); err != nil {
		t.Fatal(err)
	}

	stats := session.GetSessionStats()
	// One prompt: user + assistant(toolUse) + toolResult + assistant(stop),
	// plus the persisted tool-declaration system message.
	if stats.UserMessages != 1 || stats.AssistantMessages != 2 || stats.ToolCalls != 1 || stats.ToolResults != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.TotalMessages != 5 {
		t.Fatalf("totalMessages = %d; want 5 (incl. tool-declaration system message)", stats.TotalMessages)
	}
	// Usage aggregated across the whole run.
	if stats.Tokens.Input != 130 || stats.Tokens.Output != 60 || stats.Tokens.Total != 190 {
		t.Fatalf("tokens = %+v", stats.Tokens)
	}
	if stats.Cost != 0.6 {
		t.Fatalf("cost = %v", stats.Cost)
	}
	if stats.ContextUsage == nil || stats.ContextUsage.ContextWindow != 200000 {
		t.Fatalf("contextUsage = %+v", stats.ContextUsage)
	}
}

func TestAgentSessionAutoRetryDecision(t *testing.T) {
	dir := t.TempDir()
	session, _ := NewAgentSession(&SessionConfig{
		Cwd: dir, Model: &ai.Model{ID: "mock", API: "openai-responses", Provider: "openai", ContextWindow: 200000},
		StreamFn: mockSessionStreamFn(),
		Sessions: NewSessionManager(dir, &SessionManagerOptions{SessionDir: dir}),
		Settings: SessionSettings{Retry: &ai.RetryPolicy{Enabled: true, MaxRetries: 2, BaseDelayMS: 10}},
	})

	// Transient error → willRetry.
	msg := "terminated"
	errored := &ai.AssistantMessage{StopReason: ai.StopError, ErrorMessage: &msg}
	event := &agent.AgentEvent{Type: agent.AgentEnd, Messages: []ai.Message{errored}}
	if !session.willRetryAfterAgentEnd(event) {
		t.Fatal("transient error should retry")
	}

	// Quota/billing exhaustion never retries.
	quota := "insufficient_quota"
	errored2 := &ai.AssistantMessage{StopReason: ai.StopError, ErrorMessage: &quota}
	if session.willRetryAfterAgentEnd(&agent.AgentEvent{Type: agent.AgentEnd, Messages: []ai.Message{errored2}}) {
		t.Fatal("quota errors must not retry")
	}

	// Retry budget exhausted.
	session.retryAttempt = 2
	if session.willRetryAfterAgentEnd(&agent.AgentEvent{Type: agent.AgentEnd, Messages: []ai.Message{errored}}) {
		t.Fatal("exhausted retries must stop")
	}
}

func TestParseSkillBlockExtraction(t *testing.T) {
	text := "<skill name=\"debugging\" location=\"/x/SKILL.md\">\nSkill instructions.\n</skill>\n\nNow debug it."
	parsed := ParseSkillBlock(text)
	if parsed == nil {
		t.Fatal("expected skill block")
	}
	if parsed.Name != "debugging" || parsed.Location != "/x/SKILL.md" || parsed.Content != "Skill instructions." || parsed.UserMessage != "Now debug it." {
		t.Fatalf("parsed = %+v", parsed)
	}
	if ParseSkillBlock("no block here") != nil {
		t.Fatal("non-skill text should not parse")
	}
}

func TestThinkingLevelDefaults(t *testing.T) {
	if DefaultThinkingLevel != ai.ThinkMedium {
		t.Fatal("default thinking level")
	}
	if len(ThinkingLevelOptions) != 7 || ThinkingLevelOptions[0] != ai.ThinkOff {
		t.Fatal("thinking level options")
	}
}

var _ = time.Now

func makeTool(name string) agent.AgentTool {
	return agent.AgentTool{
		Name: name, Description: name + " tool", Parameters: json.RawMessage(`{"type":"object"}`),
		Label: name,
		Execute: func(string, json.RawMessage, context.Context, func(agent.AgentToolResult)) (agent.AgentToolResult, error) {
			return agent.AgentToolResult{Content: []ai.Content{ai.TextContent{Text: name}}, Details: json.RawMessage(`{}`)}, nil
		},
	}
}
