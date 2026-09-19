package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Streaming tests replicating the Anthropic SSE wire format (the same shapes
// upstream's mock-server tests replay).

// sseServer serves a scripted SSE response for POST /v1/messages.
func sseServer(t *testing.T, events []string, capture func(r *http.Request, body []byte)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if capture != nil {
			body := make([]byte, r.ContentLength)
			_, _ = r.Body.Read(body)
			capture(r, body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		for _, event := range events {
			_, _ = w.Write([]byte(event))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}))
}

func TestStreamAnthropicTextFlow(t *testing.T) {
	model := testAnthropicModel()
	model.BaseURL = "placeholder"
	events := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-opus-4-5\",\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hel\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"lo\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	var capturedPath, capturedKey, capturedVersion, capturedBody string
	server := sseServer(t, events, func(r *http.Request, body []byte) {
		capturedPath = r.URL.String()
		capturedKey = r.Header.Get("X-Api-Key")
		capturedVersion = r.Header.Get("anthropic-version")
		capturedBody = string(body)
	})
	model.BaseURL = server.URL
	defer server.Close()

	stream := StreamAnthropic(model, NormalizeContext(Context{
		SystemPrompt: strPtr("sys"),
		Messages:     []Message{&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1}},
	}), &AnthropicOptions{StreamOptions: StreamOptions{APIKey: "test-key"}})

	var types []string
	var text string
	for {
		event, ok := stream.Next(context.Background())
		if !ok {
			break
		}
		types = append(types, event.Type)
		if event.Type == EventTextDelta {
			text += event.Delta
		}
	}
	want := []string{"start", "text_start", "text_delta", "text_delta", "text_end", "done"}
	if len(types) != len(want) {
		t.Fatalf("events = %v; want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("events = %v; want %v", types, want)
		}
	}
	if text != "Hello" {
		t.Fatalf("text = %q", text)
	}

	msg, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if msg.StopReason != StopStop || msg.ResponseID == nil || *msg.ResponseID != "msg_1" {
		t.Fatalf("message = %+v", msg)
	}
	if msg.Usage.Input != 10 || msg.Usage.Output != 5 || msg.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", msg.Usage)
	}
	// Cost computed from catalog rates: input 10 * $5/M, output 5 * $25/M.
	if msg.Usage.Cost.Input != (5.0/1000000.0)*10.0 || msg.Usage.Cost.Output != (25.0/1000000.0)*5.0 {
		t.Fatalf("cost = %+v", msg.Usage.Cost)
	}

	// Request wire shape: SDK-compatible URL, headers, body keys.
	if capturedPath != "/v1/messages?beta=true" {
		t.Fatalf("path = %s", capturedPath)
	}
	if capturedKey != "test-key" {
		t.Fatalf("x-api-key = %q", capturedKey)
	}
	if capturedVersion != "2023-06-01" {
		t.Fatalf("anthropic-version = %q", capturedVersion)
	}
	var bodyMap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(capturedBody), &bodyMap); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"model", "messages", "max_tokens", "stream", "system"} {
		if _, ok := bodyMap[key]; !ok {
			t.Fatalf("body missing %q: %s", key, capturedBody)
		}
	}
	if string(bodyMap["stream"]) != "true" {
		t.Fatalf("stream = %s", bodyMap["stream"])
	}
}

func TestStreamAnthropicToolUseWithPartialJSON(t *testing.T) {
	model := testAnthropicModel()
	events := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_2\",\"model\":\"claude-opus-4-5\",\"usage\":{\"input_tokens\":8,\"output_tokens\":1}}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"bash\",\"input\":{}}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"cmd\\\":\\\"ls\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\" -la\\\"}\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":9}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	server := sseServer(t, events, nil)
	model.BaseURL = server.URL
	defer server.Close()

	stream := StreamAnthropic(model, NormalizeContext(Context{
		Messages: []Message{&UserMessage{Content: StringOrBlocks{Text: "list"}, Timestamp: 1}},
	}), &AnthropicOptions{StreamOptions: StreamOptions{APIKey: "k"}})

	var deltas []string
	var finalCall *ToolCall
	for {
		event, ok := stream.Next(context.Background())
		if !ok {
			break
		}
		if event.Type == EventToolcallDelta {
			deltas = append(deltas, event.Delta)
		}
		if event.Type == EventToolcallEnd {
			finalCall = event.ToolCall
		}
	}
	if len(deltas) != 2 {
		t.Fatalf("deltas = %q", deltas)
	}
	msg, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if msg.StopReason != StopToolUse {
		t.Fatalf("stopReason = %s", msg.StopReason)
	}
	if finalCall == nil || finalCall.Name != "bash" || string(finalCall.Arguments) != `{"cmd":"ls -la"}` {
		t.Fatalf("toolCall = %+v", finalCall)
	}
	if rawStop := msg.RawStopReason; rawStop == nil || *rawStop != "tool_use" {
		t.Fatalf("rawStopReason = %v", msg.RawStopReason)
	}
}

func TestStreamAnthropicThinkingAndRedacted(t *testing.T) {
	model := testAnthropicModel()
	events := []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"claude-opus-4-5\",\"usage\":{\"input_tokens\":5,\"output_tokens\":1}}}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"hmm\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"sig123\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n",
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"redacted_thinking\",\"data\":\"enc-payload\"}}\n\n",
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	server := sseServer(t, events, nil)
	model.BaseURL = server.URL
	defer server.Close()

	stream := StreamAnthropic(model, NormalizeContext(Context{
		Messages: []Message{&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1}},
	}), &AnthropicOptions{StreamOptions: StreamOptions{APIKey: "k"}})

	var events0 []string
	for {
		event, ok := stream.Next(context.Background())
		if !ok {
			break
		}
		events0 = append(events0, event.Type)
	}
	want := []string{"start", "thinking_start", "thinking_delta", "thinking_end", "thinking_start", "thinking_end", "done"}
	if len(events0) != len(want) {
		t.Fatalf("events = %v; want %v", events0, want)
	}
	msg, _ := stream.Result(context.Background())
	thinking := msg.Content[0].(ThinkingContent)
	if thinking.Thinking != "hmm" || thinking.ThinkingSignature == nil || *thinking.ThinkingSignature != "sig123" {
		t.Fatalf("thinking = %+v", thinking)
	}
	redacted := msg.Content[1].(ThinkingContent)
	if !redacted.Redacted || *redacted.ThinkingSignature != "enc-payload" || redacted.Thinking != "[Reasoning redacted]" {
		t.Fatalf("redacted = %+v", redacted)
	}
}

func TestStreamAnthropicErrorPaths(t *testing.T) {
	t.Run("sse error event", func(t *testing.T) {
		model := testAnthropicModel()
		server := sseServer(t, []string{
			"event: error\ndata: {\"type\":\"error\",\"error\":{\"message\":\"overloaded\"}}\n\n",
		}, nil)
		model.BaseURL = server.URL
		defer server.Close()
		stream := StreamAnthropic(model, NormalizeContext(Context{
			Messages: []Message{&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1}},
		}), &AnthropicOptions{StreamOptions: StreamOptions{APIKey: "k"}})
		msg, _ := stream.Result(context.Background())
		if msg.StopReason != StopError || msg.ErrorMessage == nil || !strings.Contains(*msg.ErrorMessage, "overloaded") {
			t.Fatalf("message = %+v", msg)
		}
	})

	t.Run("ended before message_stop", func(t *testing.T) {
		model := testAnthropicModel()
		server := sseServer(t, []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"claude-opus-4-5\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n",
		}, nil)
		model.BaseURL = server.URL
		defer server.Close()
		stream := StreamAnthropic(model, NormalizeContext(Context{
			Messages: []Message{&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1}},
		}), &AnthropicOptions{StreamOptions: StreamOptions{APIKey: "k"}})
		msg, _ := stream.Result(context.Background())
		if msg.StopReason != StopError || !strings.Contains(*msg.ErrorMessage, "ended before message_stop") {
			t.Fatalf("message = %+v", msg)
		}
	})

	t.Run("missing api key fails before request", func(t *testing.T) {
		model := testAnthropicModel()
		called := false
		server := sseServer(t, nil, func(*http.Request, []byte) { called = true })
		model.BaseURL = server.URL
		defer server.Close()
		stream := StreamAnthropic(model, NormalizeContext(Context{
			Messages: []Message{&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1}},
		}), nil)
		msg, _ := stream.Result(context.Background())
		if called {
			t.Fatal("no request should be made without auth")
		}
		if msg.StopReason != StopError || *msg.ErrorMessage != "No API key for provider: anthropic" {
			t.Fatalf("message = %+v", msg)
		}
	})

	t.Run("unhandled stop reason", func(t *testing.T) {
		model := testAnthropicModel()
		server := sseServer(t, []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"claude-opus-4-5\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"mystery\"}}\n\n",
		}, nil)
		model.BaseURL = server.URL
		defer server.Close()
		stream := StreamAnthropic(model, NormalizeContext(Context{
			Messages: []Message{&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1}},
		}), &AnthropicOptions{StreamOptions: StreamOptions{APIKey: "k"}})
		msg, _ := stream.Result(context.Background())
		if msg.StopReason != StopError || !strings.Contains(*msg.ErrorMessage, "Unhandled stop reason: mystery") {
			t.Fatalf("message = %+v", msg)
		}
	})

	t.Run("refusal with explanation", func(t *testing.T) {
		model := testAnthropicModel()
		server := sseServer(t, []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"claude-opus-4-5\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\",\"stop_details\":{\"explanation\":\"nope\"}}}\n\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		}, nil)
		model.BaseURL = server.URL
		defer server.Close()
		stream := StreamAnthropic(model, NormalizeContext(Context{
			Messages: []Message{&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1}},
		}), &AnthropicOptions{StreamOptions: StreamOptions{APIKey: "k"}})
		msg, _ := stream.Result(context.Background())
		if msg.StopReason != StopError || *msg.ErrorMessage != "nope" {
			t.Fatalf("message = %+v", msg)
		}
	})
}

func TestStreamAnthropicHTTPRetry(t *testing.T) {
	model := testAnthropicModel()
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.Header().Set("retry-after-ms", "10")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"type":"error","error":{"message":"rate limited"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"claude-opus-4-5\",\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\n" +
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	model.BaseURL = server.URL
	defer server.Close()

	maxRetries := 5
	stream := StreamAnthropic(model, NormalizeContext(Context{
		Messages: []Message{&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1}},
	}), &AnthropicOptions{StreamOptions: StreamOptions{APIKey: "k", MaxRetries: &maxRetries}})
	msg, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if msg.StopReason != StopStop {
		t.Fatalf("stopReason = %s", msg.StopReason)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d; want 3", attempts)
	}
}

func TestStreamAnthropicRetryDelayCapFails(t *testing.T) {
	model := testAnthropicModel()
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.Header().Set("retry-after", "120")
		w.WriteHeader(429)
	}))
	model.BaseURL = server.URL
	defer server.Close()

	maxRetries := 3
	stream := StreamAnthropic(model, NormalizeContext(Context{
		Messages: []Message{&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1}},
	}), &AnthropicOptions{StreamOptions: StreamOptions{APIKey: "k", MaxRetries: &maxRetries}})
	msg, _ := stream.Result(context.Background())
	if msg.StopReason != StopError || !strings.Contains(*msg.ErrorMessage, "Server requested 120s retry delay") {
		t.Fatalf("message = %+v", msg)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d; want 1 (cap fails immediately)", attempts)
	}
}

func TestStreamAnthropicAbort(t *testing.T) {
	model := testAnthropicModel()
	block := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"claude-opus-4-5\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-block
	}))
	model.BaseURL = server.URL
	defer server.Close()
	defer close(block)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := StreamAnthropic(model, NormalizeContext(Context{
		Messages: []Message{&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1}},
	}), &AnthropicOptions{StreamOptions: StreamOptions{APIKey: "k", Ctx: ctx}})

	// Wait for start, then abort.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		event, ok := stream.Next(context.Background())
		if !ok {
			break
		}
		if event.Type == EventStart {
			cancel()
		}
	}
	msg, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if msg.StopReason != StopAborted {
		t.Fatalf("stopReason = %s; want aborted", msg.StopReason)
	}
	_ = msg.ErrorMessage
}

func TestStreamAnthropicSimpleMapping(t *testing.T) {
	// streamSimple: no reasoning → thinking disabled.
	model := testAnthropicModel()
	var captured map[string]json.RawMessage
	server := sseServer(t, []string{
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"model\":\"claude-opus-4-5\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n",
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n",
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}, func(r *http.Request, body []byte) {
		captured = map[string]json.RawMessage{}
		json.Unmarshal(body, &captured)
	})
	model.BaseURL = server.URL
	defer server.Close()

	stream := StreamAnthropicSimple(model, NormalizeContext(Context{
		Messages: []Message{&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1}},
	}), &SimpleStreamOptions{StreamOptions: StreamOptions{APIKey: "k"}})
	msg, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if msg.StopReason != StopStop {
		t.Fatalf("stopReason = %s", msg.StopReason)
	}
	if thinking, ok := captured["thinking"]; !ok || !strings.Contains(string(thinking), `"type":"disabled"`) {
		t.Fatalf("thinking = %s", captured["thinking"])
	}

	// Reasoning low → budget-based thinking with 2048 budget.
	stream = StreamAnthropicSimple(model, NormalizeContext(Context{
		Messages: []Message{&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1}},
	}), &SimpleStreamOptions{StreamOptions: StreamOptions{APIKey: "k"}, Reasoning: ThinkLow})
	if _, err := stream.Result(context.Background()); err != nil {
		t.Fatal(err)
	}
	if thinking, ok := captured["thinking"]; !ok || !strings.Contains(string(thinking), `"budget_tokens":2048`) {
		t.Fatalf("thinking = %s", captured["thinking"])
	}
	// max_tokens = model cap (64000).
	if string(captured["max_tokens"]) != "64000" {
		t.Fatalf("max_tokens = %s", captured["max_tokens"])
	}
}

func TestGetPiUserAgent(t *testing.T) {
	ua := GetPiUserAgent()
	if !strings.HasPrefix(ua, "pi (") || !strings.HasSuffix(ua, ")") {
		t.Fatalf("user agent = %q", ua)
	}
}
