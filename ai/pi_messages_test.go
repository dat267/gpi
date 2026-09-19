package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Tests for api/pi-messages.ts.

func piMessagesModel(baseURL string) *Model {
	return &Model{
		ID: "balanced", API: APIPiMessages, Provider: "radius",
		BaseURL: baseURL, ContextWindow: 200000, MaxTokens: 8192,
	}
}

func piMessagesContext() TranscriptContext {
	return TranscriptContext{Messages: []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hello"}, Timestamp: 1},
	}}
}

func TestParsePiMessagesEvent(t *testing.T) {
	event, err := parsePiMessagesEvent("event: message\ndata: {\"type\":\"text_delta\",\"contentIndex\":0,\"delta\":\"hi\"}")
	if err != nil || event == nil || event.Type != "text_delta" || event.Delta != "hi" {
		t.Fatalf("event = %+v err = %v", event, err)
	}
	// The [DONE] sentinel yields no event.
	if event, err := parsePiMessagesEvent("data: [DONE]"); err != nil || event != nil {
		t.Fatalf("event = %+v err = %v", event, err)
	}
	// A block without data yields no event.
	if event, err := parsePiMessagesEvent("event: ping"); err != nil || event != nil {
		t.Fatalf("event = %+v err = %v", event, err)
	}
	// Invalid JSON is an error.
	if _, err := parsePiMessagesEvent("data: {oops"); err == nil {
		t.Fatal("invalid JSON must error")
	}
}

func TestPiMessagesErrorBodyParsing(t *testing.T) {
	body := `{"error":{"message":"nope","code":"rate_limited","details":{"retry":1}}}`
	parsed := parsePiMessagesErrorBody(body)
	if parsed == nil {
		t.Fatal("error body must parse")
	}
	if _, ok := parsed["error"].(map[string]any); !ok {
		t.Fatalf("parsed = %#v", parsed)
	}
	// A body without an error object is not an error body.
	if parsePiMessagesErrorBody(`{"ok":true}`) != nil {
		t.Fatal("a body without error must not parse")
	}
	if parsePiMessagesErrorBody("not json") != nil {
		t.Fatal("invalid JSON must not parse")
	}
	if truncateDiagnosticString(strings.Repeat("x", 9000)) == strings.Repeat("x", 9000) {
		t.Fatal("long bodies must be truncated")
	}
}

func TestStreamPiMessagesAssemblesEvents(t *testing.T) {
	var captured *http.Request
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		captured = request
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &payload)
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		events := []string{
			`{"type":"start"}`,
			`{"type":"text_start","contentIndex":0}`,
			`{"type":"text_delta","contentIndex":0,"delta":"Hel"}`,
			`{"type":"text_delta","contentIndex":0,"delta":"lo"}`,
			`{"type":"text_end","contentIndex":0,"content":"Hello","contentSignature":"sig-1"}`,
			`{"type":"thinking_start","contentIndex":1}`,
			`{"type":"thinking_delta","contentIndex":1,"delta":"think"}`,
			`{"type":"thinking_end","contentIndex":1,"content":"think","contentSignature":"tsig","redacted":true}`,
			`{"type":"toolcall_start","contentIndex":2,"id":"call-1","toolName":"read"}`,
			`{"type":"toolcall_delta","contentIndex":2,"delta":"{\"pa"}`,
			`{"type":"toolcall_delta","contentIndex":2,"delta":"th\":\"a\"}"}`,
			`{"type":"toolcall_end","contentIndex":2,"toolCall":{"type":"toolCall","id":"call-1","name":"read","arguments":{"path":"a"}}}`,
			`{"type":"done","reason":"toolUse","usage":{"input":10,"output":4,"cacheRead":1,"cacheWrite":0,"totalTokens":15,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"responseId":"resp-1","providerThinkingLevel":"high","rewrite":{"policyId":"p1","policyVersion":2,"changed":true,"tokenCountChange":-3,"messageCountChange":0,"systemPromptChanged":false}}`,
		}
		for _, event := range events {
			_, _ = writer.Write([]byte("data: " + event + "\n\n"))
		}
	}))
	defer server.Close()

	stream := StreamPiMessages(piMessagesModel(server.URL), piMessagesContext(), &PiMessagesOptions{
		StreamOptions: StreamOptions{APIKey: "radius-token", SessionID: "session-1"},
		Reasoning:     ThinkHigh,
		ToolChoice:    json.RawMessage(`"auto"`),
		Debug:         true,
	})
	message, err := stream.Result(context.Background())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if message.StopReason != StopToolUse || message.API != APIPiMessages {
		t.Fatalf("message = %+v", message)
	}
	if len(message.Content) != 3 {
		t.Fatalf("content = %+v", message.Content)
	}
	if text, ok := message.Content[0].(TextContent); !ok || text.Text != "Hello" ||
		text.TextSignature == nil || *text.TextSignature != "sig-1" {
		t.Fatalf("text = %+v", message.Content[0])
	}
	if thinking, ok := message.Content[1].(ThinkingContent); !ok || thinking.Thinking != "think" || !thinking.Redacted ||
		thinking.ThinkingSignature == nil || *thinking.ThinkingSignature != "tsig" {
		t.Fatalf("thinking = %+v", message.Content[1])
	}
	if call, ok := message.Content[2].(ToolCall); !ok || call.ID != "call-1" || call.Name != "read" ||
		string(call.Arguments) != `{"path":"a"}` {
		t.Fatalf("tool call = %+v", message.Content[2])
	}
	if message.Usage.Input != 10 || message.Usage.Output != 4 || message.Usage.CacheRead != 1 ||
		message.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", message.Usage)
	}
	if message.ResponseID == nil || *message.ResponseID != "resp-1" ||
		message.ProviderThinkingLevel == nil || *message.ProviderThinkingLevel != "high" {
		t.Fatalf("message = %+v", message)
	}
	// The rewrite impact is recorded as a diagnostic.
	if len(message.Diagnostics) != 1 || message.Diagnostics[0].Type != "pi_messages_rewrite" {
		t.Fatalf("diagnostics = %+v", message.Diagnostics)
	}

	// The request carries the protocol shape.
	if captured.Header.Get("authorization") != "Bearer radius-token" ||
		captured.Header.Get("accept") != "text/event-stream" {
		t.Fatalf("headers = %v", captured.Header)
	}
	if !strings.HasSuffix(captured.URL.Path, "/messages") || captured.URL.Query().Get("debug") != "1" {
		t.Fatalf("url = %s", captured.URL.String())
	}
	if payload["model"] != "balanced" {
		t.Fatalf("payload = %#v", payload)
	}
	options, ok := payload["options"].(map[string]any)
	if !ok || options["reasoning"] != "high" || options["sessionId"] != "session-1" || options["toolChoice"] != "auto" {
		t.Fatalf("options = %#v", payload["options"])
	}
	contextValue, ok := payload["context"].(map[string]any)
	if !ok {
		t.Fatalf("context = %#v", payload["context"])
	}
	messages, _ := contextValue["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("context = %#v", contextValue)
	}
}

func TestStreamPiMessagesErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"error":{"message":"slow down","code":"rate_limited"}}`))
	}))
	defer server.Close()

	stream := StreamPiMessages(piMessagesModel(server.URL), piMessagesContext(), &PiMessagesOptions{
		StreamOptions: StreamOptions{APIKey: "token"},
	})
	message, err := stream.Result(context.Background())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if message.StopReason != StopError || message.ErrorMessage == nil ||
		!strings.Contains(*message.ErrorMessage, "429") ||
		!strings.Contains(*message.ErrorMessage, "slow down") ||
		!strings.Contains(*message.ErrorMessage, "(rate_limited)") {
		t.Fatalf("message = %+v", message)
	}
	// The response failure diagnostic carries the structured details.
	if len(message.Diagnostics) != 1 || message.Diagnostics[0].Type != "pi_messages_response_failure" {
		t.Fatalf("diagnostics = %+v", message.Diagnostics)
	}
	details := string(message.Diagnostics[0].Details)
	for _, needle := range []string{`"provider":"radius"`, `"status":429`, `"code":"rate_limited"`, `"url"`} {
		if !strings.Contains(details, needle) {
			t.Fatalf("diagnostic details missing %s: %s", needle, details)
		}
	}

	// A non-JSON body is stored truncated in the diagnostic.
	plainServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte("upstream exploded"))
	}))
	defer plainServer.Close()
	stream = StreamPiMessages(piMessagesModel(plainServer.URL), piMessagesContext(), &PiMessagesOptions{
		StreamOptions: StreamOptions{APIKey: "token"},
	})
	message, _ = stream.Result(context.Background())
	if message.ErrorMessage == nil || !strings.Contains(*message.ErrorMessage, "upstream exploded") {
		t.Fatalf("message = %+v", message)
	}
	details = string(message.Diagnostics[0].Details)
	if !strings.Contains(details, `"body":"upstream exploded"`) {
		t.Fatalf("details = %s", details)
	}
}

func TestStreamPiMessagesFailures(t *testing.T) {
	// A missing API key fails before the request.
	stream := StreamPiMessages(piMessagesModel("https://example.invalid"), piMessagesContext(), &PiMessagesOptions{})
	message, _ := stream.Result(context.Background())
	if message.ErrorMessage == nil || !strings.Contains(*message.ErrorMessage, `No API key provided for provider "radius"`) {
		t.Fatalf("message = %+v", message)
	}

	// A stream without a terminal event is an error.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("data: {\"type\":\"text_delta\",\"contentIndex\":0,\"delta\":\"partial\"}\n\n"))
	}))
	defer server.Close()
	stream = StreamPiMessages(piMessagesModel(server.URL), piMessagesContext(), &PiMessagesOptions{
		StreamOptions: StreamOptions{APIKey: "token"},
	})
	message, _ = stream.Result(context.Background())
	if message.ErrorMessage == nil || !strings.Contains(*message.ErrorMessage, "stream ended without a terminal event") {
		t.Fatalf("message = %+v", message)
	}

	// An error event terminates the stream with its message.
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("data: {\"type\":\"error\",\"reason\":\"error\",\"usage\":{\"input\":1,\"output\":0,\"cacheRead\":0,\"cacheWrite\":0,\"totalTokens\":1,\"cost\":{\"input\":0,\"output\":0,\"cacheRead\":0,\"cacheWrite\":0,\"total\":0}},\"errorMessage\":\"policy blocked\"}\n\n"))
	}))
	defer server.Close()
	stream = StreamPiMessages(piMessagesModel(server.URL), piMessagesContext(), &PiMessagesOptions{
		StreamOptions: StreamOptions{APIKey: "token"},
	})
	message, _ = stream.Result(context.Background())
	if message.StopReason != StopError || message.ErrorMessage == nil || *message.ErrorMessage != "policy blocked" {
		t.Fatalf("message = %+v", message)
	}
}

func TestPiMessagesCacheRetention(t *testing.T) {
	// The legacy env opt-in maps to long retention.
	t.Setenv("PI_CACHE_RETENTION", "long")
	if got := resolvePiMessagesCacheRetention("", nil); got != CacheRetentionLong {
		t.Fatalf("retention = %q", got)
	}
	t.Setenv("PI_CACHE_RETENTION", "short")
	if got := resolvePiMessagesCacheRetention("", nil); got != "" {
		t.Fatalf("retention = %q", got)
	}
	// An explicit option wins.
	if got := resolvePiMessagesCacheRetention(CacheRetentionLong, nil); got != CacheRetentionLong {
		t.Fatalf("retention = %q", got)
	}
}

func TestStreamPiMessagesSimple(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &payload)
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("data: {\"type\":\"done\",\"reason\":\"stop\",\"usage\":{\"input\":1,\"output\":1,\"cacheRead\":0,\"cacheWrite\":0,\"totalTokens\":2,\"cost\":{\"input\":0,\"output\":0,\"cacheRead\":0,\"cacheWrite\":0,\"total\":0}}}\n\n"))
	}))
	defer server.Close()

	stream := StreamPiMessagesSimple(piMessagesModel(server.URL), piMessagesContext(), &SimpleStreamOptions{
		StreamOptions: StreamOptions{APIKey: "token"},
		Reasoning:     ThinkLow,
		ToolChoice:    toolChoicePtr(ToolChoiceAuto),
	})
	if _, err := stream.Result(context.Background()); err != nil {
		t.Fatal(err)
	}
	options, _ := payload["options"].(map[string]any)
	if options["reasoning"] != "low" || options["toolChoice"] != "auto" {
		t.Fatalf("options = %#v", options)
	}
}

func toolChoicePtr(value ToolChoice) *ToolChoice { return &value }
