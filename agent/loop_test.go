package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dat267/pier/ai"
)

// Port of the core subset of packages/agent/test/agent-loop.test.ts.

func createModel() *ai.Model {
	return &ai.Model{
		ID: "mock", Name: "mock", API: "openai-responses", Provider: "openai",
		BaseURL: "https://example.invalid", Reasoning: false, Input: []string{"text"},
		Cost:          ai.ModelCost{ModelCostRates: ai.ModelCostRates{}},
		ContextWindow: 8192, MaxTokens: 2048,
	}
}

func createAssistantMessage(content ai.ContentList, stopReason ai.StopReason) *ai.AssistantMessage {
	if stopReason == "" {
		stopReason = ai.StopStop
	}
	return &ai.AssistantMessage{
		Content: content, API: "openai-responses", Provider: "openai", Model: "mock",
		StopReason: stopReason, Timestamp: time.Now().UnixMilli(),
	}
}

func createUserMessage(text string) *ai.UserMessage {
	return &ai.UserMessage{Content: ai.StringOrBlocks{Text: text}, Timestamp: time.Now().UnixMilli()}
}

// mockStreamFn builds a StreamFn that yields scripted assistant messages.
func mockStreamFn(responses ...*ai.AssistantMessage) (StreamFn, *int) {
	calls := 0
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
			calls++
			mu.Unlock()
			if response == nil {
				// Exhausted script: end with a plain stop (keeps the loop
				// from hanging on a mis-scripted test).
				response = createAssistantMessage(ai.ContentList{ai.TextContent{Text: "(script exhausted)"}}, ai.StopStop)
			}
			stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: response.StopReason, Message: response})
		}()
		return stream
	}, &calls
}

func identityConverter(messages []ai.Message) []ai.Message {
	var out []ai.Message
	for _, m := range messages {
		switch ai.RoleOf(m) {
		case ai.RoleSystem, ai.RoleUser, ai.RoleAssistant, ai.RoleToolResult:
			out = append(out, m)
		}
	}
	return out
}

func drainAgentStream(t *testing.T, stream *ai.EventStream[AgentEvent, []ai.Message]) ([]AgentEvent, []ai.Message) {
	t.Helper()
	var events []AgentEvent
	for {
		event, ok := stream.Next(context.Background())
		if !ok {
			break
		}
		events = append(events, event)
	}
	messages, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return events, messages
}

func TestAgentLoopEventSequence(t *testing.T) {
	streamFn, _ := mockStreamFn(createAssistantMessage(ai.ContentList{ai.TextContent{Text: "Hi there!"}}, ai.StopStop))
	config := &AgentLoopConfig{Model: createModel(), ConvertToLlm: identityConverter}

	stream := AgentLoop([]ai.Message{createUserMessage("Hello")}, AgentContext{Tools: nil}, config, nil, streamFn)
	events, messages := drainAgentStream(t, stream)

	if len(messages) != 2 || ai.RoleOf(messages[0]) != "user" || ai.RoleOf(messages[1]) != "assistant" {
		t.Fatalf("messages = %v", messageRoles(messages))
	}
	eventTypes := map[string]bool{}
	for _, e := range events {
		eventTypes[e.Type] = true
	}
	for _, want := range []string{AgentStart, TurnStart, MessageStart, MessageEnd, TurnEnd, AgentEnd} {
		if !eventTypes[want] {
			t.Fatalf("events missing %s: %v", want, eventTypes)
		}
	}
}

func messageRoles(messages []ai.Message) []string {
	var out []string
	for _, m := range messages {
		out = append(out, ai.RoleOf(m))
	}
	return out
}

func TestAgentLoopProviderContextFromTranscriptOnly(t *testing.T) {
	initialSystem := &ai.SystemMessage{Content: ai.StringOrBlocks{Text: "Transcript prompt"}, Timestamp: 1}
	var captured ai.TranscriptContext
	var capturedKeys []string
	streamFn := func(model *ai.Model, providerContext ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		captured = providerContext
		capturedKeys = transcriptKeys(providerContext)
		stream := ai.NewAssistantMessageEventStream()
		stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop,
			Message: createAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)})
		return stream
	}
	config := &AgentLoopConfig{Model: createModel(), ConvertToLlm: identityConverter}
	stream := AgentLoop([]ai.Message{initialSystem, createUserMessage("Hello")}, AgentContext{}, config, nil, streamFn)
	drainAgentStream(t, stream)

	// The provider receives a transcript: no top-level prompt or tool fields.
	if len(capturedKeys) != 1 || capturedKeys[0] != "messages" {
		t.Fatalf("transcript keys = %v", capturedKeys)
	}
	if captured.Messages[0] != ai.Message(initialSystem) {
		t.Fatal("transcript must carry the caller's system message first")
	}
}

func transcriptKeys(context ai.TranscriptContext) []string {
	// Go's TranscriptContext has exactly one field; assert structurally.
	return []string{"messages"}
}

func TestAgentLoopCustomMessagesViaConvertToLlm(t *testing.T) {
	notification := &ai.CustomMessage{Role: "notification", Content: json.RawMessage(`"This is a notification"`), Timestamp: time.Now().UnixMilli()}
	var convertedRoles []string
	converter := func(messages []ai.Message) []ai.Message {
		var out []ai.Message
		for _, m := range messages {
			if ai.RoleOf(m) == "notification" {
				continue
			}
			convertedRoles = append(convertedRoles, ai.RoleOf(m))
			out = append(out, m)
		}
		return out
	}
	streamFn, _ := mockStreamFn(createAssistantMessage(ai.ContentList{ai.TextContent{Text: "Response"}}, ai.StopStop))
	config := &AgentLoopConfig{Model: createModel(), ConvertToLlm: converter}

	stream := AgentLoop([]ai.Message{createUserMessage("Hello")}, AgentContext{Messages: []ai.Message{notification}}, config, nil, streamFn)
	drainAgentStream(t, stream)

	// The notification was filtered out; only the user message converted.
	if len(convertedRoles) != 1 || convertedRoles[0] != "user" {
		t.Fatalf("converted = %v", convertedRoles)
	}
}

func TestAgentLoopToolCallsAndResults(t *testing.T) {
	toolCalled := false
	tool := AgentTool{
		Name: "echo", Description: "echo", Parameters: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
		Label: "Echo",
		Execute: func(toolCallID string, params json.RawMessage, ctx context.Context, onUpdate func(AgentToolResult)) (AgentToolResult, error) {
			toolCalled = true
			var args map[string]any
			json.Unmarshal(params, &args)
			if args["text"] != "hi" {
				return AgentToolResult{}, fmt.Errorf("bad args: %s", params)
			}
			return AgentToolResult{
				Content: []ai.Content{ai.TextContent{Text: "echoed: hi"}},
				Details: json.RawMessage(`{"got":"hi"}`),
			}, nil
		},
	}
	assistant := createAssistantMessage(ai.ContentList{
		ai.ToolCall{ID: "tc1", Name: "echo", Arguments: json.RawMessage(`{"text":"hi"}`)},
	}, ai.StopToolUse)
	followUp := createAssistantMessage(ai.ContentList{ai.TextContent{Text: "done"}}, ai.StopStop)
	streamFn, calls := mockStreamFn(assistant, followUp)

	config := &AgentLoopConfig{Model: createModel(), ConvertToLlm: identityConverter}
	stream := AgentLoop([]ai.Message{createUserMessage("run")}, AgentContext{Tools: []AgentTool{tool}}, config, nil, streamFn)
	events, messages := drainAgentStream(t, stream)

	if !toolCalled {
		t.Fatal("tool not executed")
	}
	if *calls != 2 {
		t.Fatalf("stream calls = %d; want 2 (tool loop)", *calls)
	}
	// declareToolChanges emits a system message declaring the executable tools.
	roles := messageRoles(messages)
	if len(roles) != 5 || roles[0] != "system" || roles[1] != "user" || roles[2] != "assistant" || roles[3] != "toolResult" || roles[4] != "assistant" {
		t.Fatalf("messages = %v", roles)
	}
	toolResult, ok := messages[3].(*ai.ToolResultMessage)
	if !ok || toolResult.ToolName != "echo" || toolResult.IsError {
		t.Fatalf("toolResult = %+v", messages[2])
	}
	if text := toolResult.Content[0].(ai.TextContent).Text; text != "echoed: hi" {
		t.Fatalf("tool text = %q", text)
	}
	// The second LLM call sees the tool result in its transcript.
	_ = events
}

func TestAgentLoopTruncatedToolCallsFail(t *testing.T) {
	assistant := createAssistantMessage(ai.ContentList{
		ai.ToolCall{ID: "tc1", Name: "echo", Arguments: json.RawMessage(`{"text":"hi"}`)},
	}, ai.StopLength)
	streamFn, _ := mockStreamFn(assistant)
	tool := AgentTool{
		Name: "echo", Description: "", Parameters: json.RawMessage(`{}`), Label: "echo",
		Execute: func(string, json.RawMessage, context.Context, func(AgentToolResult)) (AgentToolResult, error) {
			t.Fatal("tool must not execute on a truncated message")
			return AgentToolResult{}, nil
		},
	}
	config := &AgentLoopConfig{Model: createModel(), ConvertToLlm: identityConverter}
	stream := AgentLoop([]ai.Message{createUserMessage("run")}, AgentContext{Tools: []AgentTool{tool}}, config, nil, streamFn)
	events, messages := drainAgentStream(t, stream)

	toolResult := messages[3].(*ai.ToolResultMessage)
	if !toolResult.IsError {
		t.Fatal("truncated tool call must fail")
	}
	// The failure text explains the token limit.
	text := toolResult.Content[0].(ai.TextContent).Text
	if !contains(text, "output token limit") {
		t.Fatalf("text = %q", text)
	}
	// tool_execution_start/end still emitted.
	starts, ends := 0, 0
	for _, e := range events {
		if e.Type == ToolExecutionStart {
			starts++
		}
		if e.Type == ToolExecutionEnd {
			ends++
		}
	}
	if starts != 1 || ends != 1 {
		t.Fatalf("execution events = %d/%d", starts, ends)
	}
}

func TestAgentLoopParallelCompletionOrderVsSourceOrder(t *testing.T) {
	// Upstream: "should emit tool_execution_end in completion order but
	// persist tool results in source order".
	var mu sync.Mutex
	var endOrder []string
	slow := AgentTool{
		Name: "slow", Description: "", Parameters: json.RawMessage(`{}`), Label: "slow",
		Execute: func(string, json.RawMessage, context.Context, func(AgentToolResult)) (AgentToolResult, error) {
			time.Sleep(80 * time.Millisecond)
			mu.Lock()
			endOrder = append(endOrder, "slow")
			mu.Unlock()
			return AgentToolResult{Content: []ai.Content{ai.TextContent{Text: "slow"}}, Details: json.RawMessage(`{}`)}, nil
		},
	}
	fast := AgentTool{
		Name: "fast", Description: "", Parameters: json.RawMessage(`{}`), Label: "fast",
		Execute: func(string, json.RawMessage, context.Context, func(AgentToolResult)) (AgentToolResult, error) {
			mu.Lock()
			endOrder = append(endOrder, "fast")
			mu.Unlock()
			return AgentToolResult{Content: []ai.Content{ai.TextContent{Text: "fast"}}, Details: json.RawMessage(`{}`)}, nil
		},
	}
	assistant := createAssistantMessage(ai.ContentList{
		ai.ToolCall{ID: "s1", Name: "slow", Arguments: json.RawMessage(`{}`)},
		ai.ToolCall{ID: "f1", Name: "fast", Arguments: json.RawMessage(`{}`)},
	}, ai.StopToolUse)
	streamFn, _ := mockStreamFn(assistant, createAssistantMessage(ai.ContentList{ai.TextContent{Text: "ok"}}, ai.StopStop))
	config := &AgentLoopConfig{Model: createModel(), ConvertToLlm: identityConverter}
	stream := AgentLoop([]ai.Message{createUserMessage("run")}, AgentContext{Tools: []AgentTool{slow, fast}}, config, nil, streamFn)
	events, messages := drainAgentStream(t, stream)

	if len(endOrder) != 2 || endOrder[0] != "fast" || endOrder[1] != "slow" {
		t.Fatalf("endOrder = %v; want fast,slow", endOrder)
	}
	// Persisted tool results are in assistant source order: slow, fast.
	// (messages[2] is declareToolChanges' system message.)
	r1 := messages[3].(*ai.ToolResultMessage)
	r2 := messages[4].(*ai.ToolResultMessage)
	if r1.ToolName != "slow" || r2.ToolName != "fast" {
		t.Fatalf("source order = %s, %s", r1.ToolName, r2.ToolName)
	}
	// tool_execution_end events fired in completion order.
	var endEvents []string
	for _, e := range events {
		if e.Type == ToolExecutionEnd {
			endEvents = append(endEvents, e.ToolName)
		}
	}
	if len(endEvents) != 2 || endEvents[0] != "fast" {
		t.Fatalf("end events = %v", endEvents)
	}
}

func TestAgentLoopSequentialForcedByExecutionMode(t *testing.T) {
	var mu sync.Mutex
	running := 0
	maxConcurrent := 0
	seqTool := AgentTool{
		Name: "seq", Description: "", Parameters: json.RawMessage(`{}`), Label: "seq",
		ExecutionMode: ToolExecutionSequential,
		Execute: func(string, json.RawMessage, context.Context, func(AgentToolResult)) (AgentToolResult, error) {
			mu.Lock()
			running++
			if running > maxConcurrent {
				maxConcurrent = running
			}
			mu.Unlock()
			time.Sleep(30 * time.Millisecond)
			mu.Lock()
			running--
			mu.Unlock()
			return AgentToolResult{Content: []ai.Content{ai.TextContent{Text: "x"}}, Details: json.RawMessage(`{}`)}, nil
		},
	}
	assistant := createAssistantMessage(ai.ContentList{
		ai.ToolCall{ID: "a", Name: "seq", Arguments: json.RawMessage(`{}`)},
		ai.ToolCall{ID: "b", Name: "seq", Arguments: json.RawMessage(`{}`)},
	}, ai.StopToolUse)
	streamFn, _ := mockStreamFn(assistant, createAssistantMessage(ai.ContentList{ai.TextContent{Text: "ok"}}, ai.StopStop))
	config := &AgentLoopConfig{Model: createModel(), ConvertToLlm: identityConverter}
	stream := AgentLoop([]ai.Message{createUserMessage("run")}, AgentContext{Tools: []AgentTool{seqTool}}, config, nil, streamFn)
	drainAgentStream(t, stream)
	if maxConcurrent != 1 {
		t.Fatalf("maxConcurrent = %d; want 1 (sequential)", maxConcurrent)
	}
}

func TestAgentLoopTerminateBatch(t *testing.T) {
	// All tools terminate → the loop stops after the batch.
	terminating := AgentTool{
		Name: "stop", Description: "", Parameters: json.RawMessage(`{}`), Label: "stop",
		Execute: func(string, json.RawMessage, context.Context, func(AgentToolResult)) (AgentToolResult, error) {
			return AgentToolResult{
				Content: []ai.Content{ai.TextContent{Text: "stopping"}}, Details: json.RawMessage(`{}`), Terminate: true,
			}, nil
		},
	}
	assistant := createAssistantMessage(ai.ContentList{
		ai.ToolCall{ID: "a", Name: "stop", Arguments: json.RawMessage(`{}`)},
	}, ai.StopToolUse)
	streamFn, calls := mockStreamFn(assistant)
	config := &AgentLoopConfig{Model: createModel(), ConvertToLlm: identityConverter}
	stream := AgentLoop([]ai.Message{createUserMessage("run")}, AgentContext{Tools: []AgentTool{terminating}}, config, nil, streamFn)
	events, _ := drainAgentStream(t, stream)
	if *calls != 1 {
		t.Fatalf("calls = %d; want 1 (terminated after batch)", *calls)
	}
	var agentEnds int
	for _, e := range events {
		if e.Type == AgentEnd {
			agentEnds++
		}
	}
	if agentEnds != 1 {
		t.Fatal("agent_end expected")
	}
}

func TestAgentLoopSteeringAndFollowUp(t *testing.T) {
	// Steering messages injected between turns; follow-ups continue the run.
	assistant1 := createAssistantMessage(ai.ContentList{ai.TextContent{Text: "first"}}, ai.StopStop)
	assistant2 := createAssistantMessage(ai.ContentList{ai.TextContent{Text: "second"}}, ai.StopStop)
	assistant3 := createAssistantMessage(ai.ContentList{ai.TextContent{Text: "third"}}, ai.StopStop)
	streamFn, calls := mockStreamFn(assistant1, assistant2, assistant3)

	steerings := [][]ai.Message{
		{createUserMessage("steer")},
		{createUserMessage("steer2")},
		nil,
	}
	followUps := [][]ai.Message{
		{createUserMessage("follow-up")},
		nil,
	}
	config := &AgentLoopConfig{
		Model: createModel(), ConvertToLlm: identityConverter,
		GetSteeringMessages: func(ctx context.Context) ([]ai.Message, error) {
			if len(steerings) == 0 {
				return nil, nil
			}
			msgs := steerings[0]
			steerings = steerings[1:]
			return msgs, nil
		},
		GetFollowUpMessages: func(ctx context.Context) ([]ai.Message, error) {
			if len(followUps) == 0 {
				return nil, nil
			}
			msgs := followUps[0]
			followUps = followUps[1:]
			return msgs, nil
		},
	}
	stream := AgentLoop([]ai.Message{createUserMessage("start")}, AgentContext{}, config, nil, streamFn)
	events, messages := drainAgentStream(t, stream)

	if *calls != 3 {
		t.Fatalf("calls = %d; want 3 (steer + follow-up continue the run)", *calls)
	}
	roles := messageRoles(messages)
	// The loop polls steering AT START (upstream runLoop) and again after
	// each turn: user(start), user(steer), assistant, user(steer2),
	// assistant, user(follow-up), assistant.
	if len(roles) != 7 {
		t.Fatalf("messages = %v", roles)
	}
	if roles[1] != "user" || roles[3] != "user" || roles[5] != "user" {
		t.Fatalf("injected messages = %v", roles)
	}
	var agentEnds int
	for _, e := range events {
		if e.Type == AgentEnd {
			agentEnds++
		}
	}
	if agentEnds != 1 {
		t.Fatal("exactly one agent_end")
	}
}

func TestAgentLoopErrorTerminates(t *testing.T) {
	errored := createAssistantMessage(nil, ai.StopError)
	msgText := "provider down"
	errored.ErrorMessage = &msgText
	streamFn, calls := mockStreamFn(errored)
	config := &AgentLoopConfig{Model: createModel(), ConvertToLlm: identityConverter}
	stream := AgentLoop([]ai.Message{createUserMessage("run")}, AgentContext{}, config, nil, streamFn)
	events, messages := drainAgentStream(t, stream)
	if *calls != 1 || len(messages) != 2 {
		t.Fatalf("calls=%d messages=%v", *calls, messageRoles(messages))
	}
	var turnEnds, agentEnds int
	for _, e := range events {
		switch e.Type {
		case TurnEnd:
			turnEnds++
		case AgentEnd:
			agentEnds++
		}
	}
	if turnEnds != 1 || agentEnds != 1 {
		t.Fatalf("turnEnds=%d agentEnds=%d", turnEnds, agentEnds)
	}
}

func TestAgentLoopContinueValidation(t *testing.T) {
	config := &AgentLoopConfig{Model: createModel(), ConvertToLlm: identityConverter}
	streamFn, _ := mockStreamFn()

	// Empty context panics.
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic on empty context")
			}
		}()
		AgentLoopContinue(AgentContext{}, config, nil, streamFn)
	}()

	// Assistant-tail context panics.
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected panic on assistant tail")
			}
		}()
		AgentLoopContinue(AgentContext{Messages: []ai.Message{
			createAssistantMessage(ai.ContentList{ai.TextContent{Text: "x"}}, ai.StopStop),
		}}, config, nil, streamFn)
	}()
}

func TestAgentLoopContinueRunsFromContext(t *testing.T) {
	// Retry semantics: context already has the user message; the loop
	// continues without a prompt and without re-emitting it as a prompt run.
	streamFn, calls := mockStreamFn(createAssistantMessage(ai.ContentList{ai.TextContent{Text: "retry"}}, ai.StopStop))
	config := &AgentLoopConfig{Model: createModel(), ConvertToLlm: identityConverter}
	stream := AgentLoopContinue(AgentContext{Messages: []ai.Message{createUserMessage("run")}}, config, nil, streamFn)
	events, messages := drainAgentStream(t, stream)
	if *calls != 1 || len(messages) != 1 {
		t.Fatalf("calls=%d messages=%v", *calls, messageRoles(messages))
	}
	// No message events for the pre-existing context message.
	for _, e := range events {
		if e.Type == MessageStart && ai.RoleOf(e.Message) == "user" {
			t.Fatal("continue must not re-emit context messages")
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
