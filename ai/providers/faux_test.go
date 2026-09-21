package providers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/dat267/pier/ai"
)

// Port of packages/ai/test/faux-provider.test.ts. The "unregisters the
// provider" case needs the global API registry (not yet ported) and is
// deferred.
//
// Ground-truth note: upstream's "estimates prompt and output tokens from
// serialized context" test is STALE relative to source commit 9e05370b2
// ("Mid conversation system messages"), which changed faux's serialization
// (tools render as `tool+:` lines inside the leading system message, and
// there is no separate `tools:` paragraph). The source is authoritative;
// the corresponding Go test asserts the source behavior.

func fauxUserMsg(text string, ts int64) *ai.UserMessage {
	return &ai.UserMessage{Content: ai.StringOrBlocks{Text: text}, Timestamp: ts}
}

func fauxComplete(t *testing.T, core *FauxCore, model *ai.Model, ctx ai.Context, options *ai.SimpleStreamOptions) *ai.AssistantMessage {
	t.Helper()
	stream := core.Stream(model, ai.NormalizeContext(ctx), options)
	msg, err := stream.Result(context.Background())
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	return msg
}

func fauxCollectEvents(t *testing.T, stream *ai.AssistantMessageEventStream) []ai.AssistantMessageEvent {
	t.Helper()
	return stream.Events(context.Background())
}

func TestFauxEstimatesUsage(t *testing.T) {
	core := NewFauxCore(FauxOptions{})
	core.SetResponses([]FauxResponseStep{{Message: FauxAssistantMessage("hello world", FauxMessageOptions{})}})
	model := core.GetModel("")

	ctx := ai.Context{
		SystemPrompt: strPtr("Be concise."),
		Messages:     []ai.Message{fauxUserMsg("hi there", time.Now().UnixMilli())},
	}
	response := fauxComplete(t, core, model, ctx, nil)

	if len(response.Content) != 1 || response.Content[0].(ai.TextContent).Text != "hello world" {
		t.Fatalf("content = %v", response.Content)
	}
	if response.Usage.Input <= 0 || response.Usage.Output <= 0 {
		t.Fatalf("usage = %+v", response.Usage)
	}
	if response.Usage.TotalTokens != response.Usage.Input+response.Usage.Output {
		t.Fatalf("totalTokens = %d; want %d", response.Usage.TotalTokens, response.Usage.Input+response.Usage.Output)
	}
	if core.State().CallCount != 1 {
		t.Fatalf("callCount = %d; want 1", core.State().CallCount)
	}
}

func strPtr(s string) *string { return &s }

func TestFauxHelperBlocks(t *testing.T) {
	core := NewFauxCore(FauxOptions{})
	core.SetResponses([]FauxResponseStep{{Message: FauxAssistantMessage(
		ai.ContentList{FauxThinking("think"), FauxToolCall("echo", json.RawMessage(`{"text":"hi"}`), ""), FauxText("done")},
		FauxMessageOptions{StopReason: ai.StopToolUse},
	)}})

	response := fauxComplete(t, core, core.GetModel(""), ai.Context{
		Messages: []ai.Message{fauxUserMsg("hi", time.Now().UnixMilli())},
	}, nil)

	if response.StopReason != ai.StopToolUse {
		t.Fatalf("stopReason = %s", response.StopReason)
	}
	thinking, ok := response.Content[0].(ai.ThinkingContent)
	if !ok || thinking.Thinking != "think" {
		t.Fatalf("content[0] = %v", response.Content[0])
	}
	call, ok := response.Content[1].(ai.ToolCall)
	if !ok || call.Name != "echo" || string(call.Arguments) != `{"text":"hi"}` {
		t.Fatalf("content[1] = %v", response.Content[1])
	}
	text, ok := response.Content[2].(ai.TextContent)
	if !ok || text.Text != "done" {
		t.Fatalf("content[2] = %v", response.Content[2])
	}
}

func TestFauxMultipleModelsWithModelAwareFactories(t *testing.T) {
	core := NewFauxCore(FauxOptions{
		Models: []FauxModelDefinition{
			{ID: "faux-fast", Name: "Faux Fast"},
			{ID: "faux-thinker", Name: "Faux Thinker", Reasoning: true},
		},
	})
	factory := func(ctx ai.TranscriptContext, options *ai.SimpleStreamOptions, state *FauxProviderState, model *ai.Model) (*ai.AssistantMessage, error) {
		reasoning := "false"
		if model.Reasoning {
			reasoning = "true"
		}
		return FauxAssistantMessage(model.ID+":"+reasoning, FauxMessageOptions{}), nil
	}
	core.SetResponses([]FauxResponseStep{{Factory: factory}, {Factory: factory}})

	if core.Models()[0].ID != "faux-fast" || core.Models()[1].ID != "faux-thinker" {
		t.Fatalf("models = %v", core.Models())
	}
	if core.GetModel("") != core.Models()[0] {
		t.Fatal("GetModel() should return the first model")
	}
	if core.GetModel("faux-fast").Reasoning {
		t.Fatal("faux-fast should not reason")
	}
	if !core.GetModel("faux-thinker").Reasoning {
		t.Fatal("faux-thinker should reason")
	}

	fast := fauxComplete(t, core, core.GetModel("faux-fast"), ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}, nil)
	thinker := fauxComplete(t, core, core.GetModel("faux-thinker"), ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}, nil)
	if fast.Content[0].(ai.TextContent).Text != "faux-fast:false" {
		t.Fatalf("fast = %v", fast.Content[0])
	}
	if thinker.Content[0].(ai.TextContent).Text != "faux-thinker:true" {
		t.Fatalf("thinker = %v", thinker.Content[0])
	}
}

func TestFauxRewritesAttribution(t *testing.T) {
	core := NewFauxCore(FauxOptions{API: "faux:test", Provider: "faux-provider", Models: []FauxModelDefinition{{ID: "faux-model"}}})
	core.SetResponses([]FauxResponseStep{{Message: FauxAssistantMessage("hello", FauxMessageOptions{})}})

	response := fauxComplete(t, core, core.GetModel(""), ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}, nil)
	if response.API != "faux:test" || response.Provider != "faux-provider" || response.Model != "faux-model" {
		t.Fatalf("attribution = %s/%s/%s", response.API, response.Provider, response.Model)
	}
}

func TestFauxConsumesQueueInOrderAndErrorsWhenExhausted(t *testing.T) {
	core := NewFauxCore(FauxOptions{})
	core.SetResponses([]FauxResponseStep{
		{Message: FauxAssistantMessage("first", FauxMessageOptions{})},
		{Message: FauxAssistantMessage("second", FauxMessageOptions{})},
	})
	ctx := ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}
	model := core.GetModel("")

	first := fauxComplete(t, core, model, ctx, nil)
	second := fauxComplete(t, core, model, ctx, nil)
	exhausted := fauxComplete(t, core, model, ctx, nil)

	if first.Content[0].(ai.TextContent).Text != "first" {
		t.Fatalf("first = %v", first.Content[0])
	}
	if second.Content[0].(ai.TextContent).Text != "second" {
		t.Fatalf("second = %v", second.Content[0])
	}
	if exhausted.StopReason != ai.StopError || *exhausted.ErrorMessage != "No more faux responses queued" {
		t.Fatalf("exhausted = %s / %v", exhausted.StopReason, exhausted.ErrorMessage)
	}
	if core.GetPendingResponseCount() != 0 || core.State().CallCount != 3 {
		t.Fatalf("pending = %d, calls = %d", core.GetPendingResponseCount(), core.State().CallCount)
	}
}

func TestFauxReplaceAndAppendQueues(t *testing.T) {
	core := NewFauxCore(FauxOptions{})
	ctx := ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}
	model := core.GetModel("")

	core.SetResponses([]FauxResponseStep{{Message: FauxAssistantMessage("first", FauxMessageOptions{})}})
	if got := fauxComplete(t, core, model, ctx, nil).Content[0].(ai.TextContent).Text; got != "first" {
		t.Fatalf("got %q", got)
	}
	if core.GetPendingResponseCount() != 0 {
		t.Fatal("queue should be empty")
	}

	core.SetResponses([]FauxResponseStep{{Message: FauxAssistantMessage("second", FauxMessageOptions{})}})
	if core.GetPendingResponseCount() != 1 {
		t.Fatal("queue should have 1")
	}
	core.AppendResponses([]FauxResponseStep{
		{Message: FauxAssistantMessage("third", FauxMessageOptions{})},
		{Message: FauxAssistantMessage("fourth", FauxMessageOptions{})},
	})
	if core.GetPendingResponseCount() != 3 {
		t.Fatal("queue should have 3")
	}
	for _, want := range []string{"second", "third", "fourth"} {
		if got := fauxComplete(t, core, model, ctx, nil).Content[0].(ai.TextContent).Text; got != want {
			t.Fatalf("got %q; want %q", got, want)
		}
	}
	if core.GetPendingResponseCount() != 0 {
		t.Fatal("queue should be empty")
	}
}

func TestFauxAsyncFactorySeesState(t *testing.T) {
	core := NewFauxCore(FauxOptions{})
	core.SetResponses([]FauxResponseStep{{Factory: func(ctx ai.TranscriptContext, options *ai.SimpleStreamOptions, state *FauxProviderState, model *ai.Model) (*ai.AssistantMessage, error) {
		return FauxAssistantMessage(fmtInt(len(ctx.Messages))+":"+fmtInt(state.CallCount), FauxMessageOptions{}), nil
	}}})
	response := fauxComplete(t, core, core.GetModel(""), ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}, nil)
	if response.Content[0].(ai.TextContent).Text != "1:1" {
		t.Fatalf("got %v", response.Content[0])
	}
}

func fmtInt(n int) string { return json.Number(itoa(n)).String() }

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func TestFauxFactoryThrowEmitsError(t *testing.T) {
	core := NewFauxCore(FauxOptions{})
	core.SetResponses([]FauxResponseStep{{Factory: func(ctx ai.TranscriptContext, options *ai.SimpleStreamOptions, state *FauxProviderState, model *ai.Model) (*ai.AssistantMessage, error) {
		return nil, &fauxTestError{"boom"}
	}}})
	events := fauxCollectEvents(t, core.Stream(core.GetModel(""), ai.NormalizeContext(ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}), nil))
	if len(events) != 1 {
		t.Fatalf("events = %d; want 1", len(events))
	}
	if events[0].Type != ai.EventError {
		t.Fatalf("event type = %s", events[0].Type)
	}
	if events[0].Error.StopReason != ai.StopError || *events[0].Error.ErrorMessage != "boom" {
		t.Fatalf("error = %s / %v", events[0].Error.StopReason, events[0].Error.ErrorMessage)
	}
}

type fauxTestError struct{ msg string }

func (e *fauxTestError) Error() string { return e.msg }

func TestFauxPendingStopReasonRejected(t *testing.T) {
	core := NewFauxCore(FauxOptions{})
	core.SetResponses([]FauxResponseStep{{Message: FauxAssistantMessage("partial", FauxMessageOptions{StopReason: ai.StopPending})}})
	events := fauxCollectEvents(t, core.Stream(core.GetModel(""), ai.NormalizeContext(ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}), nil))
	for _, e := range events {
		if e.Type == ai.EventDone {
			t.Fatal("should not emit done")
		}
	}
	terminal := events[len(events)-1]
	if terminal.Type != ai.EventError || *terminal.Error.ErrorMessage != "Faux response ended without a stop reason" {
		t.Fatalf("terminal = %s / %v", terminal.Type, terminal.Error.ErrorMessage)
	}
}

func TestFauxEstimatesPromptAndOutputTokensFromSerializedContext(t *testing.T) {
	// Adapted to SOURCE behavior after upstream commit 9e05370b2 (see file
	// header): tools fold into the leading system message and render as
	// `tool+:` lines; there is no `tools:` paragraph. Upstream's test still
	// expects the pre-9e05370b2 serialization and is stale.
	core := NewFauxCore(FauxOptions{})
	core.SetResponses([]FauxResponseStep{{Message: FauxAssistantMessage("done", FauxMessageOptions{})}})

	tool := ai.Tool{
		Name:        "echo",
		Description: "Echo back text",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
	}
	toolJSON, err := ai.MarshalJSON(tool)
	if err != nil {
		t.Fatal(err)
	}
	ctx := ai.Context{
		SystemPrompt: strPtr("sys"),
		Messages: []ai.Message{
			&ai.UserMessage{
				Content: ai.StringOrBlocks{Blocks: ai.ContentList{
					ai.TextContent{Text: "hello"},
					ai.ImageContent{MimeType: "image/png", Data: "abcd"},
				}},
				Timestamp: 1,
			},
			FauxAssistantMessage("prior", FauxMessageOptions{Timestamp: 5}),
			&ai.ToolResultMessage{
				ToolCallID: "tool-1",
				ToolName:   "echo",
				Content:    ai.UserContentList{ai.TextContent{Text: "tool out"}},
				Timestamp:  2,
			},
		},
		Tools: []ai.Tool{tool},
	}

	response := fauxComplete(t, core, core.GetModel(""), ctx, nil)
	promptText := joinWithDoubleNewline([]string{
		"system:sys\ntool+:" + string(toolJSON),
		"user:hello\n[image:image/png:4]",
		"assistant:prior",
		"toolResult:echo\ntool out",
	})
	expectedPromptTokens := (len(promptText) + 3) / 4
	expectedOutputTokens := (len("done") + 3) / 4

	if response.Usage.Input != int64(expectedPromptTokens) {
		t.Fatalf("input = %d; want %d (prompt %q)", response.Usage.Input, expectedPromptTokens, promptText)
	}
	if response.Usage.Output != int64(expectedOutputTokens) {
		t.Fatalf("output = %d; want %d", response.Usage.Output, expectedOutputTokens)
	}
	if response.Usage.CacheRead != 0 || response.Usage.CacheWrite != 0 {
		t.Fatalf("cache = %d/%d; want 0/0", response.Usage.CacheRead, response.Usage.CacheWrite)
	}
	if response.Usage.TotalTokens != int64(expectedPromptTokens+expectedOutputTokens) {
		t.Fatalf("total = %d", response.Usage.TotalTokens)
	}
}

func TestFauxCacheIsolation(t *testing.T) {
	core := NewFauxCore(FauxOptions{})
	core.SetResponses([]FauxResponseStep{
		{Message: FauxAssistantMessage("first", FauxMessageOptions{})},
		{Message: FauxAssistantMessage("second", FauxMessageOptions{})},
		{Message: FauxAssistantMessage("third", FauxMessageOptions{})},
	})
	ctx := ai.Context{Messages: []ai.Message{fauxUserMsg("hello", 1)}}
	model := core.GetModel("")

	first := fauxComplete(t, core, model, ctx, &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{SessionID: "session-1", CacheRetention: ai.CacheRetentionShort}})
	if first.Usage.CacheWrite <= 0 {
		t.Fatalf("first cacheWrite = %d", first.Usage.CacheWrite)
	}
	ctx.Messages = append(ctx.Messages, first)
	ctx.Messages = append(ctx.Messages, fauxUserMsg("follow up", 2))

	second := fauxComplete(t, core, model, ctx, &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{SessionID: "session-2", CacheRetention: ai.CacheRetentionShort}})
	if second.Usage.CacheRead != 0 || second.Usage.CacheWrite <= 0 {
		t.Fatalf("second cache = %d/%d", second.Usage.CacheRead, second.Usage.CacheWrite)
	}

	third := fauxComplete(t, core, model, ctx, nil)
	if third.Usage.CacheRead != 0 || third.Usage.CacheWrite != 0 {
		t.Fatalf("third cache = %d/%d", third.Usage.CacheRead, third.Usage.CacheWrite)
	}
}

func TestFauxPromptCachingPerSession(t *testing.T) {
	core := NewFauxCore(FauxOptions{})
	core.SetResponses([]FauxResponseStep{
		{Message: FauxAssistantMessage("first", FauxMessageOptions{})},
		{Message: FauxAssistantMessage("second", FauxMessageOptions{})},
	})
	ctx := ai.Context{
		SystemPrompt: strPtr("Be concise."),
		Messages:     []ai.Message{fauxUserMsg("hello", 1)},
	}
	model := core.GetModel("")

	first := fauxComplete(t, core, model, ctx, &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{SessionID: "session-1", CacheRetention: ai.CacheRetentionShort}})
	if first.Usage.CacheRead != 0 || first.Usage.CacheWrite <= 0 {
		t.Fatalf("first cache = %d/%d", first.Usage.CacheRead, first.Usage.CacheWrite)
	}
	ctx.Messages = append(ctx.Messages, first)
	ctx.Messages = append(ctx.Messages, fauxUserMsg("follow up", 2))

	second := fauxComplete(t, core, model, ctx, &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{SessionID: "session-1", CacheRetention: ai.CacheRetentionShort}})
	if second.Usage.CacheRead <= 0 {
		t.Fatalf("second cacheRead = %d; want > 0", second.Usage.CacheRead)
	}
}

func TestFauxNoCachingWhenRetentionNone(t *testing.T) {
	core := NewFauxCore(FauxOptions{})
	core.SetResponses([]FauxResponseStep{
		{Message: FauxAssistantMessage("first", FauxMessageOptions{})},
		{Message: FauxAssistantMessage("second", FauxMessageOptions{})},
	})
	ctx := ai.Context{Messages: []ai.Message{fauxUserMsg("hello", 1)}}
	model := core.GetModel("")

	fauxComplete(t, core, model, ctx, &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{SessionID: "session-1", CacheRetention: ai.CacheRetentionNone}})
	ctx.Messages = append(ctx.Messages, FauxAssistantMessage("first", FauxMessageOptions{}))
	ctx.Messages = append(ctx.Messages, fauxUserMsg("follow up", 2))
	second := fauxComplete(t, core, model, ctx, &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{SessionID: "session-1", CacheRetention: ai.CacheRetentionNone}})
	if second.Usage.CacheRead != 0 || second.Usage.CacheWrite != 0 {
		t.Fatalf("cache = %d/%d; want 0/0", second.Usage.CacheRead, second.Usage.CacheWrite)
	}
}

func TestFauxStreamsDeltas(t *testing.T) {
	core := NewFauxCore(FauxOptions{})
	core.SetResponses([]FauxResponseStep{{Message: FauxAssistantMessage(
		ai.ContentList{
			FauxThinking("thinking text"),
			FauxText("answer text"),
			FauxToolCall("echo", json.RawMessage(`{"text":"hi","count":12}`), "tool-1"),
		},
		FauxMessageOptions{StopReason: ai.StopToolUse},
	)}})

	var events []string
	var toolCallDeltas []string
	stream := core.Stream(core.GetModel(""), ai.NormalizeContext(ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}), nil)
	for _, event := range fauxCollectEvents(t, stream) {
		events = append(events, event.Type)
		if event.Type == ai.EventToolcallDelta {
			toolCallDeltas = append(toolCallDeltas, event.Delta)
		}
	}

	contains := func(list []string, want string) bool {
		for _, v := range list {
			if v == want {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"thinking_start", "thinking_delta", "text_start", "text_delta", "toolcall_start", "toolcall_delta", "toolcall_end"} {
		if !contains(events, want) {
			t.Fatalf("events missing %s: %v", want, events)
		}
	}
	if len(toolCallDeltas) < 2 {
		t.Fatalf("toolCallDeltas = %d; want > 1", len(toolCallDeltas))
	}
	var args map[string]any
	joined := ""
	for _, d := range toolCallDeltas {
		joined += d
	}
	if err := json.Unmarshal([]byte(joined), &args); err != nil {
		t.Fatalf("joined deltas %q: %v", joined, err)
	}
	if args["text"] != "hi" || args["count"] != float64(12) {
		t.Fatalf("args = %v", args)
	}
}

func TestFauxExactEventOrderForFixedSizeChunks(t *testing.T) {
	core := NewFauxCore(FauxOptions{TokenSizeMin: 1, TokenSizeMax: 1})
	core.SetResponses([]FauxResponseStep{{Message: FauxAssistantMessage(
		ai.ContentList{FauxThinking("go"), FauxText("ok"), FauxToolCall("echo", json.RawMessage(`{}`), "tool-1")},
		FauxMessageOptions{StopReason: ai.StopToolUse},
	)}})
	events := fauxCollectEvents(t, core.Stream(core.GetModel(""), ai.NormalizeContext(ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}), nil))

	if events[0].Type != "start" || events[0].Partial.StopReason != ai.StopPending {
		t.Fatalf("first event = %+v", events[0])
	}
	var types []string
	for _, e := range events {
		types = append(types, e.Type)
	}
	want := []string{
		"start", "thinking_start", "thinking_delta", "thinking_end",
		"text_start", "text_delta", "text_end",
		"toolcall_start", "toolcall_delta", "toolcall_end", "done",
	}
	if len(types) != len(want) {
		t.Fatalf("events = %v; want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("events = %v; want %v", types, want)
		}
	}
}

func TestFauxStreamsMultipleToolCalls(t *testing.T) {
	core := NewFauxCore(FauxOptions{})
	core.SetResponses([]FauxResponseStep{{Message: FauxAssistantMessage(
		ai.ContentList{
			FauxToolCall("echo", json.RawMessage(`{"text":"one"}`), "tool-1"),
			FauxToolCall("echo", json.RawMessage(`{"text":"two"}`), "tool-2"),
		},
		FauxMessageOptions{StopReason: ai.StopToolUse},
	)}})
	events := fauxCollectEvents(t, core.Stream(core.GetModel(""), ai.NormalizeContext(ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}), nil))
	starts, ends := 0, 0
	for _, e := range events {
		if e.Type == ai.EventToolcallStart {
			starts++
		}
		if e.Type == ai.EventToolcallEnd {
			ends++
		}
	}
	if starts != 2 || ends != 2 {
		t.Fatalf("starts = %d, ends = %d; want 2/2", starts, ends)
	}
}

func TestFauxExplicitErrorAndAbortedAreTerminalErrors(t *testing.T) {
	for _, tc := range []struct {
		reason ai.StopReason
		msg    string
	}{
		{ai.StopError, "upstream failed"},
		{ai.StopAborted, "Request was aborted"},
	} {
		core := NewFauxCore(FauxOptions{TokenSizeMin: 2, TokenSizeMax: 2})
		msg := FauxAssistantMessage("partial", FauxMessageOptions{StopReason: tc.reason, ErrorMessage: tc.msg})
		core.SetResponses([]FauxResponseStep{{Message: msg}})
		events := fauxCollectEvents(t, core.Stream(core.GetModel(""), ai.NormalizeContext(ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}), nil))
		var types []string
		for _, e := range events {
			types = append(types, e.Type)
		}
		want := []string{"start", "text_start", "text_delta", "text_end", "error"}
		if len(types) != len(want) {
			t.Fatalf("%s: events = %v; want %v", tc.reason, types, want)
		}
		for i := range want {
			if types[i] != want[i] {
				t.Fatalf("%s: events = %v; want %v", tc.reason, types, want)
			}
		}
		terminal := events[len(events)-1]
		if terminal.Reason != tc.reason || terminal.Error.StopReason != tc.reason || *terminal.Error.ErrorMessage != tc.msg {
			t.Fatalf("%s: terminal = %+v", tc.reason, terminal)
		}
	}
}

func TestFauxAbortBeforeFirstChunk(t *testing.T) {
	core := NewFauxCore(FauxOptions{TokensPerSecond: 50, TokenSizeMin: 3, TokenSizeMax: 3})
	core.SetResponses([]FauxResponseStep{{Message: FauxAssistantMessage("abcdefghijklmnopqrstuvwxyz", FauxMessageOptions{})}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	events := fauxCollectEvents(t, core.Stream(core.GetModel(""),
		ai.NormalizeContext(ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}),
		&ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{Ctx: ctx}}))
	if len(events) != 1 {
		t.Fatalf("events = %d; want 1", len(events))
	}
	if events[0].Type != ai.EventError || events[0].Reason != ai.StopAborted || events[0].Error.StopReason != ai.StopAborted {
		t.Fatalf("event = %+v", events[0])
	}
}

func TestFauxAbortMidTextStream(t *testing.T) {
	core := NewFauxCore(FauxOptions{TokensPerSecond: 50, TokenSizeMin: 3, TokenSizeMax: 3})
	core.SetResponses([]FauxResponseStep{{Message: FauxAssistantMessage("abcdefghijklmnopqrstuvwxyz", FauxMessageOptions{})}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var types []string
	textDeltaCount := 0
	stream := core.Stream(core.GetModel(""),
		ai.NormalizeContext(ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}),
		&ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{Ctx: ctx}})
	for {
		event, ok := stream.Next(context.Background())
		if !ok {
			break
		}
		types = append(types, event.Type)
		if event.Type == ai.EventTextDelta {
			textDeltaCount++
			cancel()
		}
	}
	if textDeltaCount != 1 {
		t.Fatalf("textDeltaCount = %d; want 1 (events %v)", textDeltaCount, types)
	}
	if !containsString(types, "text_start") || !containsString(types, "text_delta") || !containsString(types, "error") {
		t.Fatalf("events = %v", types)
	}
	if containsString(types, "text_end") {
		t.Fatalf("events should not contain text_end: %v", types)
	}
}

func TestFauxAbortMidThinkingStream(t *testing.T) {
	core := NewFauxCore(FauxOptions{TokensPerSecond: 50, TokenSizeMin: 3, TokenSizeMax: 3})
	core.SetResponses([]FauxResponseStep{{Message: FauxAssistantMessage(
		ai.ContentList{FauxThinking("abcdefghijklmnopqrstuvwxyz")}, FauxMessageOptions{StopReason: ai.StopStop},
	)}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var types []string
	thinkingDeltaCount := 0
	stream := core.Stream(core.GetModel(""),
		ai.NormalizeContext(ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}),
		&ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{Ctx: ctx}})
	for {
		event, ok := stream.Next(context.Background())
		if !ok {
			break
		}
		types = append(types, event.Type)
		if event.Type == ai.EventThinkingDelta {
			thinkingDeltaCount++
			cancel()
		}
	}
	if thinkingDeltaCount != 1 {
		t.Fatalf("thinkingDeltaCount = %d (events %v)", thinkingDeltaCount, types)
	}
	if !containsString(types, "thinking_start") || !containsString(types, "thinking_delta") || !containsString(types, "error") {
		t.Fatalf("events = %v", types)
	}
	if containsString(types, "thinking_end") {
		t.Fatalf("events should not contain thinking_end: %v", types)
	}
}

func TestFauxAbortMidToolcallStream(t *testing.T) {
	core := NewFauxCore(FauxOptions{TokensPerSecond: 50, TokenSizeMin: 3, TokenSizeMax: 3})
	core.SetResponses([]FauxResponseStep{{Message: FauxAssistantMessage(
		ai.ContentList{FauxToolCall("echo", json.RawMessage(`{"text":"abcdefghijklmnopqrstuvwxyz","count":123456789}`), "tool-1")},
		FauxMessageOptions{StopReason: ai.StopToolUse},
	)}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var types []string
	toolCallDeltaCount := 0
	stream := core.Stream(core.GetModel(""),
		ai.NormalizeContext(ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}),
		&ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{Ctx: ctx}})
	for {
		event, ok := stream.Next(context.Background())
		if !ok {
			break
		}
		types = append(types, event.Type)
		if event.Type == ai.EventToolcallDelta {
			toolCallDeltaCount++
			cancel()
		}
	}
	if toolCallDeltaCount != 1 {
		t.Fatalf("toolCallDeltaCount = %d (events %v)", toolCallDeltaCount, types)
	}
	if !containsString(types, "toolcall_start") || !containsString(types, "toolcall_delta") || !containsString(types, "error") {
		t.Fatalf("events = %v", types)
	}
	if containsString(types, "toolcall_end") {
		t.Fatalf("events should not contain toolcall_end: %v", types)
	}
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
