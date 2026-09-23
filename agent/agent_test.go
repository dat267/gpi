package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dat267/pier/ai"
)

// Port of the core subset of packages/agent/test/agent.test.ts.

func unusedStreamFn() StreamFn {
	return func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			msg := createAssistantMessage(ai.ContentList{ai.TextContent{Text: "unused"}}, ai.StopStop)
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: msg})
		}()
		return stream
	}
}

func makeTool(name string) AgentTool {
	return AgentTool{
		Name: name, Description: name + " tool", Parameters: json.RawMessage(`{"type":"object"}`),
		Label: name,
		Execute: func(string, json.RawMessage, context.Context, func(AgentToolResult)) (AgentToolResult, error) {
			return AgentToolResult{Content: []ai.Content{ai.TextContent{Text: name}}, Details: json.RawMessage(`{}`)}, nil
		},
	}
}

func TestAgentDefaultState(t *testing.T) {
	agent, err := NewAgent(&AgentOptions{StreamFn: unusedStreamFn()})
	if err != nil {
		t.Fatal(err)
	}
	state := agent.State()
	if state.Model == nil || state.ThinkingLevel != ai.ThinkOff || state.Tools == nil ||
		state.Messages != nil || state.IsStreaming || state.StreamingMessage != nil ||
		state.PendingToolCalls == nil || len(state.PendingToolCalls) != 0 || state.ErrorMessage != "" {
		t.Fatalf("default state = %+v", state)
	}
}

func TestAgentCustomInitialState(t *testing.T) {
	customModel := &ai.Model{ID: "gpt-4o-mini", Name: "gpt-4o-mini", API: "openai-responses", Provider: "openai"}
	agent, err := NewAgent(&AgentOptions{
		StreamFn: unusedStreamFn(),
		InitialState: &AgentInitialState{
			SystemPrompt: "You are a helpful assistant.", Model: customModel, ThinkingLevel: ai.ThinkLow,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	state := agent.State()
	if len(state.Messages) != 1 {
		t.Fatalf("messages = %v", messageRoles(state.Messages))
	}
	sys := state.Messages[0].(*ai.SystemMessage)
	if sys.Content.Text != "You are a helpful assistant." || sys.Timestamp != 0 {
		t.Fatalf("initial system = %+v", sys)
	}
	if state.Model != customModel || state.ThinkingLevel != ai.ThinkLow {
		t.Fatalf("model/thinking = %v/%v", state.Model, state.ThinkingLevel)
	}
}

func TestAgentInitialToolsDeclared(t *testing.T) {
	tool := makeTool("echo")
	agent, err := NewAgent(&AgentOptions{
		StreamFn:     unusedStreamFn(),
		InitialState: &AgentInitialState{SystemPrompt: "You are helpful.", Tools: []AgentTool{tool}},
	})
	if err != nil {
		t.Fatal(err)
	}
	initial := agent.State().Messages[0].(*ai.SystemMessage)
	if initial.Content.Text != "You are helpful." {
		t.Fatalf("content = %q", initial.Content.Text)
	}
	if len(initial.ToolsAdded) != 1 || initial.ToolsAdded[0].Name != "echo" {
		t.Fatalf("toolsAdded = %+v", initial.ToolsAdded)
	}
}

func TestAgentDeclaresToolLoadoutChanges(t *testing.T) {
	// Upstream: requests record the declared-tool delta per call.
	var mu sync.Mutex
	var requests [][]string
	streamFn := func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		mu.Lock()
		var entry []string
		for _, message := range context.Messages {
			if sys, ok := message.(*ai.SystemMessage); ok {
				added := "+"
				for _, tool := range sys.ToolsAdded {
					added += tool.Name + ","
				}
				removed := "-"
				for _, tool := range sys.ToolsRemoved {
					removed += tool.Name + ","
				}
				entry = append(entry, added, removed)
			}
		}
		requests = append(requests, entry)
		mu.Unlock()
		stream := ai.NewAssistantMessageEventStream()
		stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop,
			Message: createAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)})
		return stream
	}
	first, second := makeTool("first"), makeTool("second")
	agent, _ := NewAgent(&AgentOptions{
		InitialState: &AgentInitialState{SystemPrompt: "You are helpful.", Tools: []AgentTool{first}},
		StreamFn:     streamFn,
	})

	if err := agent.PromptText(context.Background(), "one"); err != nil {
		t.Fatal(err)
	}
	agent.SetTools([]AgentTool{second})
	if err := agent.PromptText(context.Background(), "two"); err != nil {
		t.Fatal(err)
	}
	if err := agent.PromptText(context.Background(), "three"); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 3 {
		t.Fatalf("requests = %v", requests)
	}
	// Upstream expectation: the first request carries the transcript's
	// initial declaration; the second announces the swap; the third no change.
	wantFirst := []string{"+first,", "-"}
	for i, v := range wantFirst {
		if requests[0][i] != v {
			t.Fatalf("requests[0] = %v; want %v", requests[0], wantFirst)
		}
	}
	var updates []*ai.SystemMessage
	for _, m := range agent.State().Messages {
		if sys, ok := m.(*ai.SystemMessage); ok && len(sys.ToolsRemoved) > 0 {
			updates = append(updates, sys)
		}
	}
	if len(updates) != 1 {
		t.Fatalf("updates = %d", len(updates))
	}
	if len(updates[0].ToolsAdded) != 1 || updates[0].ToolsAdded[0].Name != "second" ||
		len(updates[0].ToolsRemoved) != 1 || updates[0].ToolsRemoved[0].Name != "first" {
		t.Fatalf("update = %+v", updates[0])
	}
	// The initial declaration carries no executable fields.
	initial := agent.State().Messages[0].(*ai.SystemMessage)
	if len(initial.ToolsAdded) != 1 || initial.ToolsAdded[0].Description != "first tool" {
		t.Fatalf("initial = %+v", initial.ToolsAdded)
	}
}

func TestAgentResetRestoresBaseline(t *testing.T) {
	agent, _ := NewAgent(&AgentOptions{
		StreamFn:     unusedStreamFn(),
		InitialState: &AgentInitialState{SystemPrompt: "base", Tools: []AgentTool{makeTool("echo")}},
	})
	if err := agent.PromptText(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if err := agent.Reset(); err != nil {
		t.Fatal(err)
	}
	messages := agent.State().Messages
	if len(messages) != 1 || ai.RoleOf(messages[0]) != "system" {
		t.Fatalf("after reset = %v", messageRoles(messages))
	}
	sys := messages[0].(*ai.SystemMessage)
	if sys.Content.Text != "base" || len(sys.ToolsAdded) != 1 {
		t.Fatalf("baseline = %+v", sys)
	}
}

func TestAgentSubscribeAndLifecycle(t *testing.T) {
	streamFn, _ := mockStreamFn(createAssistantMessage(ai.ContentList{ai.TextContent{Text: "reply"}}, ai.StopStop))
	agent, _ := NewAgent(&AgentOptions{StreamFn: streamFn})

	var mu sync.Mutex
	var events []string
	cancel := agent.Subscribe(func(event AgentEvent, ctx context.Context) error {
		mu.Lock()
		events = append(events, event.Type)
		mu.Unlock()
		return nil
	})
	defer cancel()

	if err := agent.PromptText(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) == 0 || events[len(events)-1] != AgentEnd {
		t.Fatalf("events = %v", events)
	}
	// isStreaming false after settle.
	if agent.State().IsStreaming {
		t.Fatal("agent should be idle")
	}
	// Transcript grew with the assistant reply.
	roles := messageRoles(agent.State().Messages)
	if len(roles) != 2 || roles[1] != "assistant" {
		t.Fatalf("transcript = %v", roles)
	}
}

func TestAgentRunFailureEmitsLifecycle(t *testing.T) {
	agent, _ := NewAgent(&AgentOptions{
		StreamFn: func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
			panic("provider exploded")
		},
	})
	var events []AgentEvent
	agent.Subscribe(func(event AgentEvent, ctx context.Context) error {
		events = append(events, event)
		return nil
	})
	if err := agent.PromptText(context.Background(), "hello"); err != nil {
		t.Fatalf("run failure should be folded into events, not returned: %v", err)
	}
	var types []string
	for _, e := range events {
		types = append(types, e.Type)
	}
	// Upstream expectation: the loop's agent_start/turn_start precede the
	// synthesized failure lifecycle.
	want := []string{AgentStart, TurnStart, MessageStart, MessageEnd, MessageStart, MessageEnd, TurnEnd, AgentEnd}
	if len(types) != len(want) {
		t.Fatalf("events = %v; want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("events = %v; want %v", types, want)
		}
	}
	lastMessage := agent.State().Messages[len(agent.State().Messages)-1].(*ai.AssistantMessage)
	if lastMessage.StopReason != ai.StopError || *lastMessage.ErrorMessage != "provider exploded" {
		t.Fatalf("lastMessage = %+v", lastMessage)
	}
	if agent.State().ErrorMessage != "provider exploded" {
		t.Fatalf("state errorMessage = %q", agent.State().ErrorMessage)
	}
}

func TestAgentQueuesAndModes(t *testing.T) {
	streamFn, calls := mockStreamFn(
		createAssistantMessage(ai.ContentList{ai.TextContent{Text: "1"}}, ai.StopStop),
		createAssistantMessage(ai.ContentList{ai.TextContent{Text: "2"}}, ai.StopStop),
	)
	agent, _ := NewAgent(&AgentOptions{StreamFn: streamFn})

	agent.Steer(createUserMessage("steer-me"))
	if !agent.HasQueuedMessages() {
		t.Fatal("queue should have items")
	}
	// Queued steering is not yet in the transcript.
	if len(agent.State().Messages) != 0 {
		t.Fatal("queued message must not leak into state")
	}
	// The loop's AT-START steering poll injects it before the first response,
	// so one prompt run + one steering message = one LLM call.
	if err := agent.PromptText(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	if agent.HasQueuedMessages() {
		t.Fatal("steering should have been drained")
	}
	if *calls != 1 {
		t.Fatalf("calls = %d; want 1", *calls)
	}

	// one-at-a-time: two queued steers drain one per poll.
	agent.Steer(createUserMessage("s1"))
	agent.Steer(createUserMessage("s2"))
	if agent.steeringQueue.getMode() != QueueModeOneAtATime {
		t.Fatal("default steering mode is one-at-a-time")
	}
	agent.SetSteeringMode(QueueModeAll)
	if agent.steeringQueue.getMode() != QueueModeAll {
		t.Fatal("mode setter failed")
	}
	agent.ClearAllQueues()
	if agent.HasQueuedMessages() {
		t.Fatal("queues should be cleared")
	}
}

func TestAgentContinueFromAssistantTail(t *testing.T) {
	// continue() with an assistant tail drains steering first.
	streamFn, calls := mockStreamFn(createAssistantMessage(ai.ContentList{ai.TextContent{Text: "r"}}, ai.StopStop))
	agent, _ := NewAgent(&AgentOptions{StreamFn: streamFn})

	// Seed transcript with an assistant tail via a prompt run.
	if err := agent.PromptText(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	agent.Steer(createUserMessage("queued"))
	if err := agent.Continue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if *calls != 2 {
		t.Fatalf("calls = %d; want 2", *calls)
	}
	roles := messageRoles(agent.State().Messages)
	// user(start), assistant, user(queued steer), assistant(reply).
	if len(roles) != 4 || roles[2] != "user" || roles[3] != "assistant" {
		t.Fatalf("transcript = %v", roles)
	}
	// A second continue with empty queues errors.
	if err := agent.Continue(context.Background()); err == nil ||
		err.Error() != "Cannot continue from message role: assistant" {
		t.Fatalf("continue error = %v", err)
	}
}

func TestAgentDoublePromptErrors(t *testing.T) {
	block := make(chan struct{})
	defer close(block)
	slowFn := func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		stream := ai.NewAssistantMessageEventStream()
		go func() {
			<-block
			msg := createAssistantMessage(ai.ContentList{ai.TextContent{Text: "slow"}}, ai.StopStop)
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: msg})
		}()
		return stream
	}
	agent, _ := NewAgent(&AgentOptions{StreamFn: slowFn})

	runErr := make(chan error, 1)
	go func() { runErr <- agent.PromptText(context.Background(), "first") }()
	time.Sleep(20 * time.Millisecond)

	if err := agent.PromptText(context.Background(), "second"); err == nil ||
		!strings.Contains(err.Error(), "already processing") {
		t.Fatalf("double prompt error = %v", err)
	}
	if err := agent.Reset(); err == nil {
		t.Fatal("reset during processing must error")
	}
	if err := agent.Continue(context.Background()); err == nil {
		t.Fatal("continue during processing must error")
	}
}

func TestAgentForwardsSessionID(t *testing.T) {
	var captured *ai.SimpleStreamOptions
	streamFn := func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		captured = options
		stream := ai.NewAssistantMessageEventStream()
		stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop,
			Message: createAssistantMessage(ai.ContentList{ai.TextContent{Text: "ok"}}, ai.StopStop)})
		return stream
	}
	agent, _ := NewAgent(&AgentOptions{StreamFn: streamFn, SessionID: "chat-42"})
	if err := agent.PromptText(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if captured == nil || captured.SessionID != "chat-42" {
		t.Fatalf("sessionId = %+v", captured)
	}
}

var _ = fmt.Sprintf

// The Agent stores the tool-call hooks in its options; they must reach the loop
// config, or every call is silently ungated (regression: they were dropped,
// which left BeforeToolCall inert on Agent).
func TestAgentBeforeToolCallReachesTheLoop(t *testing.T) {
	ran := 0
	tool := makeTool("echo")
	original := tool.Execute
	tool.Execute = func(id string, params json.RawMessage, ctx context.Context, onUpdate func(AgentToolResult)) (AgentToolResult, error) {
		ran++
		return original(id, params, ctx, onUpdate)
	}

	assistant := createAssistantMessage(ai.ContentList{
		ai.ToolCall{ID: "tc1", Name: "echo", Arguments: json.RawMessage(`{"text":"hi"}`)},
	}, ai.StopToolUse)
	streamFn, _ := mockStreamFn(assistant)

	a, err := NewAgent(&AgentOptions{
		InitialState: &AgentInitialState{SystemPrompt: "sys", Tools: []AgentTool{tool}, Model: createModel()},
		StreamFn:     streamFn,
		BeforeToolCall: func(call *BeforeToolCallContext, ctx context.Context) (*BeforeToolCallResult, error) {
			if call.ToolCall.Name == "echo" {
				return &BeforeToolCallResult{Block: true, Reason: "denied by the test"}, nil
			}
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.PromptText(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	if ran != 0 {
		t.Fatalf("the tool ran %d times, want the hook to block it", ran)
	}

	var blocked *ai.ToolResultMessage
	for _, message := range a.State().Messages {
		if result, ok := message.(*ai.ToolResultMessage); ok {
			blocked = result
		}
	}
	if blocked == nil {
		t.Fatal("no tool result recorded for the blocked call")
	}
	if !blocked.IsError {
		t.Error("a blocked call should record an error result")
	}
	if !strings.Contains(blocked.Content[0].(ai.TextContent).Text, "denied by the test") {
		t.Errorf("result = %+v, want the hook's reason", blocked.Content)
	}
}
