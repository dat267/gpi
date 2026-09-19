package ai

import (
	ctxpkg "context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// Tests for api/mistral-conversations.ts.

func mistralModel(baseURL string) *Model {
	return &Model{
		ID: "mistral-large-latest", API: "mistral-conversations", Provider: "mistral",
		BaseURL: baseURL, ContextWindow: 128000, MaxTokens: 8192,
	}
}

func mistralUserContext() TranscriptContext {
	return TranscriptContext{Messages: []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hello"}, Timestamp: 1},
	}}
}

func TestMistralWirePayloadRenamesKeys(t *testing.T) {
	model := mistralModel("https://api.mistral.ai")
	maxTokens := 100
	temperature := 0.5
	payload := buildMistralChatPayload(model, mistralUserContext(), mistralUserContext().Messages, &MistralOptions{
		StreamOptions: StreamOptions{MaxTokens: &maxTokens, Temperature: &temperature},
		ToolChoice:    "auto",
		PromptMode:    "reasoning",
	})
	wire := mistralWirePayload(payload)
	if wire["max_tokens"] != float64(100) {
		t.Fatalf("max_tokens = %#v", wire["max_tokens"])
	}
	if _, hasCamel := wire["maxTokens"]; hasCamel {
		t.Fatalf("camelCase key survived: %#v", wire)
	}
	if wire["prompt_mode"] != "reasoning" {
		t.Fatalf("prompt_mode = %#v", wire["prompt_mode"])
	}
	if wire["tool_choice"] != "auto" {
		t.Fatalf("tool_choice = %#v", wire["tool_choice"])
	}
	if wire["stream"] != true || wire["model"] != "mistral-large-latest" {
		t.Fatalf("wire = %#v", wire)
	}
	messages, _ := wire["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %#v", wire["messages"])
	}
}

func TestMistralMessageConversion(t *testing.T) {
	// User text stays a string; images become data URLs when supported.
	messages := toMistralChatMessages([]Message{
		&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1},
	}, true)
	if len(messages) != 1 || messages[0].Role != "user" || messages[0].Content != "hi" {
		t.Fatalf("messages = %+v", messages)
	}

	image := aiImageContent{"abc", "image/png"}
	messages = toMistralChatMessages([]Message{
		&UserMessage{Content: StringOrBlocks{Blocks: ContentList{TextContent{Text: "look"}, image.TextContent()}}, Timestamp: 1},
	}, true)
	content, ok := messages[0].Content.([]mistralContentChunk)
	if !ok || len(content) != 2 || content[1].ImageURL != "data:image/png;base64,abc" {
		t.Fatalf("content = %#v", messages[0].Content)
	}
	// Without image support the image is dropped and noted.
	messages = toMistralChatMessages([]Message{
		&UserMessage{Content: StringOrBlocks{Blocks: ContentList{image.TextContent()}}, Timestamp: 1},
	}, false)
	if messages[0].Content != "(image omitted: model does not support images)" {
		t.Fatalf("content = %#v", messages[0].Content)
	}

	// Assistant content keeps text/thinking and tool calls.
	assistant := &AssistantMessage{Content: ContentList{
		TextContent{Text: "answer"},
		ThinkingContent{Thinking: "reasoning"},
		ToolCall{ID: "call1", Name: "read", Arguments: json.RawMessage(`{"path":"a"}`)},
	}}
	messages = toMistralChatMessages([]Message{assistant}, false)
	if len(messages) != 1 || len(messages[0].ToolCalls) != 1 {
		t.Fatalf("messages = %+v", messages)
	}
	if messages[0].ToolCalls[0].Function.Name != "read" ||
		messages[0].ToolCalls[0].Function.Arguments != `{"path":"a"}` {
		t.Fatalf("tool call = %+v", messages[0].ToolCalls[0])
	}
	if messages[0].Prefix == nil || *messages[0].Prefix {
		t.Fatalf("prefix = %v", messages[0].Prefix)
	}
	parts, _ := messages[0].Content.([]mistralContentChunk)
	if len(parts) != 2 || parts[1].Type != "thinking" {
		t.Fatalf("content = %#v", messages[0].Content)
	}

	// Tool results get the upstream text forms.
	toolResultText := func(text string, hasImages, supportsImages, isError bool) string {
		return buildMistralToolResultText(text, hasImages, supportsImages, isError)
	}
	cases := []struct {
		text           string
		hasImages      bool
		supportsImages bool
		isError        bool
		want           string
	}{
		{"out", false, false, false, "out"},
		{"out", false, false, true, "[tool error] out"},
		{"", true, true, false, "(see attached image)"},
		{"", true, true, true, "[tool error] (see attached image)"},
		{"", true, false, false, "(image omitted: model does not support images)"},
		{"", true, false, true, "[tool error] (image omitted: model does not support images)"},
		{"", false, false, false, "(no tool output)"},
		{"", false, false, true, "[tool error] (no tool output)"},
		{"out", true, false, false, "out\n[tool image omitted: model does not support images]"},
	}
	for _, testCase := range cases {
		if got := toolResultText(testCase.text, testCase.hasImages, testCase.supportsImages, testCase.isError); got != testCase.want {
			t.Errorf("toolResultText(%q, %v, %v, %v) = %q, want %q", testCase.text, testCase.hasImages,
				testCase.supportsImages, testCase.isError, got, testCase.want)
		}
	}
}

// aiImageContent builds an ImageContent block.
type aiImageContent struct {
	data     string
	mimeType string
}

func (i aiImageContent) TextContent() ImageContent {
	return ImageContent{Data: i.data, MimeType: i.mimeType}
}

func TestMistralToolCallIDNormalization(t *testing.T) {
	normalize := newMistralToolCallIDNormalizer()
	// A 9-character alphanumeric id is preserved.
	if got := normalize("abc123XYZ"); got != "abc123XYZ" {
		t.Fatalf("id = %q", got)
	}
	// Longer ids are hashed to 9 alphanumeric characters and are stable.
	long := normalize("call_abcdefghijklmnop")
	if len(long) != mistralToolCallIDLength || !regexp.MustCompile(`^[a-zA-Z0-9]+$`).MatchString(long) {
		t.Fatalf("id = %q", long)
	}
	if again := normalize("call_abcdefghijklmnop"); again != long {
		t.Fatalf("unstable: %q vs %q", again, long)
	}
	// Distinct ids stay distinct.
	if other := normalize("call_zzzzzzzzzzzzzzz"); other == long {
		t.Fatalf("collision: %q", other)
	}
}

func TestMistralStreamEndToEnd(t *testing.T) {
	var captured *http.Request
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		captured = request
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &body)
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		events := []string{
			`{"id":"chunk-1","choices":[{"delta":{"content":"Hel"}}]}`,
			`{"choices":[{"delta":{"content":"lo"}}]}`,
			`{"choices":[{"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"think"}]}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"id":"abc123XYZ","index":0,"function":{"name":"read","arguments":"{\"pa"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"read","arguments":"th\":\"a\"}"}}]}}]}`,
			`{"choices":[{"finish_reason":"tool_calls","delta":{}}],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14,"prompt_tokens_details":{"cached_tokens":3}}}`,
		}
		for _, event := range events {
			_, _ = writer.Write([]byte("data: " + event + "\n\n"))
		}
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	model := mistralModel(server.URL)
	stream := StreamMistralConversations(model, mistralUserContext(), &MistralOptions{
		StreamOptions: StreamOptions{APIKey: "test-key", SessionID: "session-1"},
	})
	message, err := stream.Result(ctxpkg.Background())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if message.StopReason != StopToolUse {
		t.Fatalf("stopReason = %s", message.StopReason)
	}
	if message.ResponseID == nil || *message.ResponseID != "chunk-1" {
		t.Fatalf("responseId = %v", message.ResponseID)
	}
	// Text, thinking, and the tool call are assembled in order.
	if len(message.Content) != 3 {
		t.Fatalf("content = %+v", message.Content)
	}
	if text, ok := message.Content[0].(TextContent); !ok || text.Text != "Hello" {
		t.Fatalf("text = %+v", message.Content[0])
	}
	if thinking, ok := message.Content[1].(ThinkingContent); !ok || thinking.Thinking != "think" {
		t.Fatalf("thinking = %+v", message.Content[1])
	}
	call, ok := message.Content[2].(ToolCall)
	if !ok || call.ID != "abc123XYZ" || call.Name != "read" || string(call.Arguments) != `{"path":"a"}` {
		t.Fatalf("tool call = %+v", message.Content[2])
	}
	// Usage separates cached tokens from input.
	if message.Usage.Input != 7 || message.Usage.CacheRead != 3 || message.Usage.Output != 4 ||
		message.Usage.TotalTokens != 14 {
		t.Fatalf("usage = %+v", message.Usage)
	}

	// The request is authenticated and streams with the affinity header.
	if captured.Header.Get("authorization") != "Bearer test-key" {
		t.Fatalf("authorization = %q", captured.Header.Get("authorization"))
	}
	if captured.Header.Get("accept") != "text/event-stream" {
		t.Fatalf("accept = %q", captured.Header.Get("accept"))
	}
	if captured.Header.Get("x-affinity") != "session-1" {
		t.Fatalf("x-affinity = %q", captured.Header.Get("x-affinity"))
	}
	if !strings.HasSuffix(captured.URL.Path, "/v1/chat/completions") {
		t.Fatalf("path = %s", captured.URL.Path)
	}
	if body["prompt_cache_key"] != "session-1" {
		t.Fatalf("prompt_cache_key = %#v", body["prompt_cache_key"])
	}
}

func TestMistralStreamErrors(t *testing.T) {
	// An HTTP failure is reported with the status and body.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"message":"invalid api key"}`))
	}))
	defer server.Close()

	model := mistralModel(server.URL)
	stream := StreamMistralConversations(model, mistralUserContext(), &MistralOptions{
		StreamOptions: StreamOptions{APIKey: "bad"},
	})
	message, err := stream.Result(ctxpkg.Background())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if message.StopReason != StopError || message.ErrorMessage == nil ||
		!strings.Contains(*message.ErrorMessage, "Mistral API error (401)") ||
		!strings.Contains(*message.ErrorMessage, "invalid api key") {
		t.Fatalf("message = %+v", message)
	}

	// A missing API key fails before the request.
	stream = StreamMistralConversations(model, mistralUserContext(), &MistralOptions{})
	message, err = stream.Result(ctxpkg.Background())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if message.ErrorMessage == nil || !strings.Contains(*message.ErrorMessage, "No API key for provider: mistral") {
		t.Fatalf("message = %+v", message)
	}

	// A malformed event is an error.
	badServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("data: {\"unexpected\":true}\n\n"))
	}))
	defer badServer.Close()
	stream = StreamMistralConversations(mistralModel(badServer.URL), mistralUserContext(), &MistralOptions{
		StreamOptions: StreamOptions{APIKey: "key"},
	})
	message, err = stream.Result(ctxpkg.Background())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if message.StopReason != StopError {
		t.Fatalf("message = %+v", message)
	}
}

func TestMistralErrorFormatting(t *testing.T) {
	long := strings.Repeat("x", maxMistralErrorBodyChars+50)
	formatted := FormatMistralError(&ProviderError{Status: 429, Body: long})
	if !strings.HasPrefix(formatted, "Mistral API error (429): ") ||
		!strings.Contains(formatted, "[truncated 50 chars]") {
		t.Fatalf("formatted = %q", formatted)
	}
	// Without a body the status and message are used.
	formatted = FormatMistralError(&ProviderError{Status: 500, Message: "boom"})
	if formatted != "Mistral API error (500): boom" {
		t.Fatalf("formatted = %q", formatted)
	}
	if got := FormatMistralError(io.EOF); got != "EOF" {
		t.Fatalf("formatted = %q", got)
	}
}

func TestMistralReasoningModes(t *testing.T) {
	if !usesMistralReasoningEffort(&Model{ID: "mistral-small-latest"}) {
		t.Fatal("mistral-small-latest uses reasoning effort")
	}
	if !usesMistralReasoningEffort(&Model{ID: "mistral-medium-2508"}) {
		t.Fatal("mistral-medium-* uses reasoning effort")
	}
	if !usesMistralReasoningEffort(&Model{ID: "zai-glm-5-2"}) {
		t.Fatal("zai-glm-5-2 uses reasoning effort")
	}
	if usesMistralReasoningEffort(&Model{ID: "mistral-large-latest"}) {
		t.Fatal("mistral-large uses prompt mode")
	}
	if !usesMistralPromptMode(&Model{ID: "mistral-large-latest", Reasoning: true}) {
		t.Fatal("reasoning models use prompt mode")
	}
	high := "high"
	model := &Model{ID: "mistral-large-latest", Reasoning: true, ThinkingLevelMap: ThinkingLevelMap{"high": &high}}
	if got := mapMistralReasoningEffort(model, ThinkHigh); got != "high" {
		t.Fatalf("effort = %q", got)
	}
	// The map falls back to high when the level is unmapped.
	if got := mapMistralReasoningEffort(&Model{}, ThinkLow); got != "high" {
		t.Fatalf("effort = %q", got)
	}
}

func TestMistralStopReasonMapping(t *testing.T) {
	cases := []struct {
		reason string
		want   StopReason
		error  string
	}{
		{"", StopStop, ""},
		{"stop", StopStop, ""},
		{"length", StopLength, ""},
		{"model_length", StopLength, ""},
		{"tool_calls", StopToolUse, ""},
		{"error", StopError, "Provider stopped with: error"},
		{"mystery", StopError, "Provider stopped with: mystery"},
	}
	for _, testCase := range cases {
		reason, message := mapMistralStopReason(testCase.reason)
		if reason != testCase.want || message != testCase.error {
			t.Errorf("mapMistralStopReason(%q) = %s/%q, want %s/%q", testCase.reason, reason, message, testCase.want, testCase.error)
		}
	}
}
