package ai

import (
	ctxpkg "context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Tests for api/openai-codex-responses.ts (the SSE transport path).

func codexModel(baseURL string) *Model {
	return &Model{
		ID: "gpt-5.5", Name: "GPT-5.5", API: APIOpenAICodexResponses, Provider: "openai-codex",
		BaseURL: baseURL, Reasoning: true, Input: []string{"text", "image"},
		ContextWindow: 400000, MaxTokens: 128000,
		Cost: ModelCost{ModelCostRates: ModelCostRates{Input: 1.25, Output: 10, CacheRead: 0.125}},
	}
}

func codexToken(t *testing.T, accountID string) string {
	t.Helper()
	payload := map[string]any{
		OpenAICodexJWTPath: map[string]any{"chatgpt_account_id": accountID},
	}
	encoded, _ := json.Marshal(payload)
	return "header." + base64.RawURLEncoding.EncodeToString(encoded) + ".signature"
}

func TestExtractCodexAccountIDForRequests(t *testing.T) {
	token := codexToken(t, "acct-1")
	accountID, err := ExtractCodexAccountID(token)
	if err != nil || accountID != "acct-1" {
		t.Fatalf("account = %q err = %v", accountID, err)
	}
	if _, err := ExtractCodexAccountID("not-a-jwt"); err == nil ||
		err.Error() != "Failed to extract accountId from token" {
		t.Fatalf("err = %v", err)
	}
}

func TestCodexURLs(t *testing.T) {
	cases := map[string]string{
		"":                                      "https://chatgpt.com/backend-api/codex/responses",
		"https://chatgpt.com/backend-api":       "https://chatgpt.com/backend-api/codex/responses",
		"https://chatgpt.com/backend-api/":      "https://chatgpt.com/backend-api/codex/responses",
		"https://chatgpt.com/backend-api/codex": "https://chatgpt.com/backend-api/codex/responses",
		"https://chatgpt.com/backend-api/codex/responses": "https://chatgpt.com/backend-api/codex/responses",
		"https://proxy.example.com":                       "https://proxy.example.com/codex/responses",
	}
	for input, want := range cases {
		if got := ResolveCodexURL(input); got != want {
			t.Errorf("ResolveCodexURL(%q) = %q, want %q", input, got, want)
		}
	}
	if got := ResolveCodexWebSocketURL("https://chatgpt.com/backend-api"); got != "wss://chatgpt.com/backend-api/codex/responses" {
		t.Fatalf("websocket url = %s", got)
	}
	if got := ResolveCodexWebSocketURL("http://localhost:8080"); got != "ws://localhost:8080/codex/responses" {
		t.Fatalf("websocket url = %s", got)
	}
}

func TestCodexHeaders(t *testing.T) {
	headers := BuildCodexSSEHeaders(map[string]string{"x-model": "1"}, ProviderHeaders{"x-extra": strPtr("2")}, "acct-1", "token-1", "session-1")
	expectations := map[string]string{
		"Authorization":       "Bearer token-1",
		"chatgpt-account-id":  "acct-1",
		"originator":          "pi",
		"OpenAI-Beta":         "responses=experimental",
		"accept":              "text/event-stream",
		"content-type":        "application/json",
		"session-id":          "session-1",
		"x-client-request-id": "session-1",
		"x-model":             "1",
		"x-extra":             "2",
	}
	for name, want := range expectations {
		if got := headers.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if headers.Get("User-Agent") == "" {
		t.Fatal("user agent missing")
	}
	// A nil override deletes a caller-visible header, but the SSE headers set
	// after the overrides (accept/content-type, auth, originator) always win.
	headers = BuildCodexSSEHeaders(map[string]string{"x-extra": "1"}, ProviderHeaders{"x-extra": nil}, "acct", "token", "")
	if headers.Get("x-extra") != "" {
		t.Fatalf("x-extra = %q", headers.Get("x-extra"))
	}
	if headers.Get("accept") != "text/event-stream" || headers.Get("content-type") != "application/json" {
		t.Fatalf("headers = %v", headers)
	}
	if headers.Get("session-id") != "" {
		t.Fatal("no session id must add no session headers")
	}
}

func TestCodexRetryClassification(t *testing.T) {
	// Terminal usage limits never retry.
	for _, text := range []string{
		`{"error":{"code":"GoUsageLimitError"}}`, "Monthly usage limit reached", "insufficient_quota", "out of budget",
	} {
		if IsCodexRetryableError(http.StatusTooManyRequests, text) {
			t.Errorf("%q must not be retryable", text)
		}
	}
	// Transient statuses retry.
	for _, status := range []int{429, 500, 502, 503, 504} {
		if !IsCodexRetryableError(status, "") {
			t.Errorf("status %d must be retryable", status)
		}
	}
	if IsCodexRetryableError(400, "bad request") {
		t.Fatal("400 must not be retryable")
	}
	if !IsCodexRetryableError(400, "connection refused") {
		t.Fatal("transport text must be retryable")
	}

	// Retry-after headers, including the millisecond variant and dates.
	headers := http.Header{}
	headers.Set("retry-after-ms", "1500")
	if delay, ok := GetCodexRetryAfterDelayMS(headers); !ok || delay != 1500*time.Millisecond {
		t.Fatalf("delay = %v ok = %v", delay, ok)
	}
	headers = http.Header{}
	headers.Set("retry-after", "2")
	if delay, ok := GetCodexRetryAfterDelayMS(headers); !ok || delay != 2*time.Second {
		t.Fatalf("delay = %v", delay)
	}
	headers = http.Header{}
	headers.Set("retry-after", time.Now().Add(5*time.Second).UTC().Format(http.TimeFormat))
	if delay, ok := GetCodexRetryAfterDelayMS(headers); !ok || delay < 3*time.Second || delay > 6*time.Second {
		t.Fatalf("delay = %v", delay)
	}
	if _, ok := GetCodexRetryAfterDelayMS(http.Header{}); ok {
		t.Fatal("no header means no delay")
	}

	// The delay is bounded by maxRetryDelayMs.
	maxDelay := 1000
	if _, err := ValidateCodexRetryDelay(2*time.Second, &OpenAICodexResponsesOptions{
		StreamOptions: StreamOptions{MaxRetryDelayMs: &maxDelay},
	}); err == nil || !strings.Contains(err.Error(), "Server requested 2s retry delay (max: 1s)") {
		t.Fatalf("err = %v", err)
	}
	if _, err := ValidateCodexRetryDelay(2*time.Second, nil); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestParseCodexErrorResponse(t *testing.T) {
	// A usage-limit error becomes the friendly message.
	raw := `{"error":{"code":"usage_limit_reached","type":"usage","message":"limit hit","plan_type":"PLUS","resets_at":` +
		jsonNumber(time.Now().Unix()+600) + `}}`
	info := ParseCodexErrorResponse(http.StatusTooManyRequests, "Too Many Requests", raw)
	if info.FriendlyMessage == "" || !strings.Contains(info.FriendlyMessage, "usage limit (plus plan)") ||
		!strings.Contains(info.FriendlyMessage, "Try again in ~") {
		t.Fatalf("info = %+v", info)
	}
	// The error message wins for the plain message.
	if info.Message != "limit hit" {
		t.Fatalf("message = %q", info.Message)
	}
	// A non-usage error keeps the raw body.
	info = ParseCodexErrorResponse(http.StatusBadRequest, "Bad Request", `{"error":{"code":"invalid","message":"nope"}}`)
	if info.Message != "nope" || info.FriendlyMessage != "" {
		t.Fatalf("info = %+v", info)
	}
	// An unparsable body falls back to the text.
	info = ParseCodexErrorResponse(http.StatusBadGateway, "Bad Gateway", "upstream exploded")
	if !strings.Contains(info.Message, "upstream exploded") {
		t.Fatalf("info = %+v", info)
	}
}

func jsonNumber(value int64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func TestCodexServiceTierPricing(t *testing.T) {
	if got := ResolveCodexServiceTier(strPtr("default"), "flex"); got != "flex" {
		t.Fatalf("tier = %s", got)
	}
	if got := ResolveCodexServiceTier(strPtr("default"), "auto"); got != "default" {
		t.Fatalf("tier = %s", got)
	}
	if got := ResolveCodexServiceTier(strPtr("priority"), "flex"); got != "priority" {
		t.Fatalf("tier = %s", got)
	}
	if got := ResolveCodexServiceTier(nil, "flex"); got != "flex" {
		t.Fatalf("tier = %s", got)
	}
	if got := CodexServiceTierMultiplier(&Model{ID: "gpt-5.5"}, "priority"); got != 2.5 {
		t.Fatalf("multiplier = %v", got)
	}
	if got := CodexServiceTierMultiplier(&Model{ID: "other"}, "priority"); got != 2 {
		t.Fatalf("multiplier = %v", got)
	}
	if got := CodexServiceTierMultiplier(nil, "flex"); got != 0.5 {
		t.Fatalf("multiplier = %v", got)
	}
	if got := CodexServiceTierMultiplier(nil, ""); got != 1 {
		t.Fatalf("multiplier = %v", got)
	}
}

func TestBuildCodexRequestBody(t *testing.T) {
	model := codexModel("")
	context := TranscriptContext{Messages: []Message{
		&SystemMessage{Content: StringOrBlocks{Text: "be brief"}},
		&UserMessage{Content: StringOrBlocks{Text: "hello"}, Timestamp: 1},
	}}
	body, err := BuildCodexRequestBody(model, context, &OpenAICodexResponsesOptions{
		ReasoningEffort: ThinkHigh, ReasoningSummary: "concise",
	}, "session-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if body["model"] != "gpt-5.5" || body["store"] != false || body["stream"] != true {
		t.Fatalf("body = %#v", body)
	}
	// The system message becomes instructions rather than an input message.
	if body["instructions"] != "be brief" {
		t.Fatalf("instructions = %#v", body["instructions"])
	}
	rawInput, _ := body["input"].(json.RawMessage)
	var messages []map[string]any
	if err := json.Unmarshal(rawInput, &messages); err != nil {
		t.Fatalf("input = %#v err = %v", body["input"], err)
	}
	if len(messages) != 1 || messages[0]["role"] != "user" {
		t.Fatalf("input = %#v", messages)
	}
	if text, _ := body["text"].(map[string]any); text["verbosity"] != "low" {
		t.Fatalf("text = %#v", body["text"])
	}
	include, _ := body["include"].([]any)
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v", body["include"])
	}
	if body["prompt_cache_key"] != "session-1" || body["tool_choice"] != "auto" || body["parallel_tool_calls"] != true {
		t.Fatalf("body = %#v", body)
	}
	reasoning, _ := body["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" || reasoning["summary"] != "concise" {
		t.Fatalf("reasoning = %#v", reasoning)
	}

	// Without a system message the default instructions apply.
	body, err = BuildCodexRequestBody(model, TranscriptContext{Messages: []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1},
	}}, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if body["instructions"] != "You are a helpful assistant." {
		t.Fatalf("instructions = %#v", body["instructions"])
	}
	if body["prompt_cache_key"] != nil {
		t.Fatalf("body = %#v", body["prompt_cache_key"])
	}
	// The default reasoning effort comes from the model's off mapping.
	reasoning, _ = body["reasoning"].(map[string]any)
	if reasoning["effort"] != "none" {
		t.Fatalf("reasoning = %#v", reasoning)
	}
}

func TestStreamCodexResponsesSSE(t *testing.T) {
	var captured *http.Request
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		captured = request
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &body)
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		events := []string{
			`{"type":"response.created"}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"m1","role":"assistant"}}`,
			`{"type":"response.output_text.delta","output_index":0,"delta":"Hel"}`,
			`{"type":"response.output_text.delta","output_index":0,"delta":"lo"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"m1","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Hello"}]}}`,
			`{"type":"response.done","response":{"id":"resp-1","status":"completed","end_turn":true,"output":[],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`,
		}
		for _, event := range events {
			_, _ = writer.Write([]byte("data: " + event + "\n\n"))
		}
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	model := codexModel(server.URL)
	stream := StreamOpenAICodexResponses(model, TranscriptContext{Messages: []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hello"}, Timestamp: 1},
	}}, &OpenAICodexResponsesOptions{
		StreamOptions: StreamOptions{APIKey: codexToken(t, "acct-1"), SessionID: "session-1"},
		Transport:     "sse",
		ServiceTier:   "flex",
	})
	message, err := stream.Result(ctxpkg.Background())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if message.StopReason != StopStop {
		t.Fatalf("message = %+v", message)
	}
	if len(message.Content) != 1 {
		t.Fatalf("content = %+v", message.Content)
	}
	if text, ok := message.Content[0].(TextContent); !ok || text.Text != "Hello" {
		t.Fatalf("content = %+v", message.Content[0])
	}
	if message.EndTurn == nil || !*message.EndTurn {
		t.Fatalf("endTurn = %v", message.EndTurn)
	}
	if message.Usage.Input != 10 || message.Usage.Output != 5 || message.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", message.Usage)
	}
	// The flex tier halves the cost.
	if message.Usage.Cost.Input != 1.25*10/1e6*0.5 {
		t.Fatalf("cost = %+v", message.Usage.Cost)
	}
	// Headers and URL are the codex ones.
	if captured.Header.Get("chatgpt-account-id") != "acct-1" ||
		captured.Header.Get("OpenAI-Beta") != "responses=experimental" {
		t.Fatalf("headers = %v", captured.Header)
	}
	if !strings.HasSuffix(captured.URL.Path, "/codex/responses") {
		t.Fatalf("path = %s", captured.URL.Path)
	}
	if body["service_tier"] != "flex" {
		t.Fatalf("body = %#v", body["service_tier"])
	}
}

func TestStreamCodexResponsesErrors(t *testing.T) {
	model := codexModel("")

	// A missing API key fails early.
	stream := StreamOpenAICodexResponses(model, TranscriptContext{Messages: []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1},
	}}, nil)
	message, _ := stream.Result(ctxpkg.Background())
	if message.ErrorMessage == nil || !strings.Contains(*message.ErrorMessage, "No API key for provider: openai-codex") {
		t.Fatalf("message = %+v", message)
	}

	// An explicit websocket transport is rejected (D28).
	stream = StreamOpenAICodexResponses(model, TranscriptContext{Messages: []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1},
	}}, &OpenAICodexResponsesOptions{
		StreamOptions: StreamOptions{APIKey: codexToken(t, "acct")},
		Transport:     "websocket",
	})
	message, _ = stream.Result(ctxpkg.Background())
	if message.ErrorMessage == nil || !strings.Contains(*message.ErrorMessage, "WebSocket transport is not supported") {
		t.Fatalf("message = %+v", message)
	}

	// A codex error event surfaces as a failed response.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`data: {"type":"error","code":"invalid_request","message":"bad input"}` + "\n\n"))
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()
	stream = StreamOpenAICodexResponses(codexModel(server.URL), TranscriptContext{Messages: []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1},
	}}, &OpenAICodexResponsesOptions{StreamOptions: StreamOptions{APIKey: codexToken(t, "acct")}})
	message, _ = stream.Result(ctxpkg.Background())
	if message.StopReason != StopError || message.ErrorMessage == nil ||
		!strings.Contains(*message.ErrorMessage, "Codex error: bad input") {
		t.Fatalf("message = %+v", message)
	}

	// An HTTP failure with a usage-limit body produces the friendly message.
	limitServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"error":{"code":"usage_limit_reached","plan_type":"PLUS"}}`))
	}))
	defer limitServer.Close()
	stream = StreamOpenAICodexResponses(codexModel(limitServer.URL), TranscriptContext{Messages: []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1},
	}}, &OpenAICodexResponsesOptions{StreamOptions: StreamOptions{APIKey: codexToken(t, "acct")}})
	message, _ = stream.Result(ctxpkg.Background())
	if message.ErrorMessage == nil || !strings.Contains(*message.ErrorMessage, "You have hit your ChatGPT usage limit (plus plan)") {
		t.Fatalf("message = %+v", message)
	}

	// A stream without a terminal event is an error.
	truncated := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`data: {"type":"response.created"}` + "\n\n"))
	}))
	defer truncated.Close()
	stream = StreamOpenAICodexResponses(codexModel(truncated.URL), TranscriptContext{Messages: []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1},
	}}, &OpenAICodexResponsesOptions{StreamOptions: StreamOptions{APIKey: codexToken(t, "acct")}})
	message, _ = stream.Result(ctxpkg.Background())
	if message.StopReason != StopError || message.ErrorMessage == nil ||
		!strings.Contains(*message.ErrorMessage, "before a terminal response event") {
		t.Fatalf("message = %+v", message)
	}
}

func TestStreamCodexResponsesRetries(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts++
		if attempts == 1 {
			writer.Header().Set("retry-after-ms", "1")
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte("overloaded"))
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`data: {"type":"response.done","response":{"status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	retries := 1
	stream := StreamOpenAICodexResponses(codexModel(server.URL), TranscriptContext{Messages: []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1},
	}}, &OpenAICodexResponsesOptions{StreamOptions: StreamOptions{
		APIKey: codexToken(t, "acct"), MaxRetries: &retries,
	}})
	message, _ := stream.Result(ctxpkg.Background())
	if message.StopReason != StopStop || attempts != 2 {
		t.Fatalf("message = %+v attempts = %d", message, attempts)
	}
}

func TestCodexProviderFactory(t *testing.T) {
	provider := OpenAICodexProvider()
	if provider.ID != "openai-codex" || provider.Name != "OpenAI Codex" {
		t.Fatalf("provider = %+v", provider)
	}
	if provider.BaseURL != defaultCodexBaseURL {
		t.Fatalf("baseURL = %s", provider.BaseURL)
	}
	// Codex is OAuth-only upstream.
	if provider.Auth.OAuth == nil || provider.Auth.APIKey != nil {
		t.Fatalf("auth = %+v", provider.Auth)
	}
	if len(provider.GetModels()) == 0 {
		t.Fatal("no catalog models")
	}
}
