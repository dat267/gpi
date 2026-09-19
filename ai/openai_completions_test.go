package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testOpenAIModel() *Model {
	return &Model{
		ID: "gpt-5", Name: "GPT-5", API: APIOpenAICompletions, Provider: "openai",
		BaseURL: "https://api.openai.com/v1", Reasoning: true, Input: []string{"text", "image"},
		Cost:          ModelCost{ModelCostRates: ModelCostRates{Input: 1.25, Output: 10, CacheRead: 0.125, CacheWrite: 0}},
		ContextWindow: 400000, MaxTokens: 128000,
	}
}

func TestDetectOpenAICompletionsCompat(t *testing.T) {
	// OpenAI proper.
	compat := DetectOpenAICompletionsCompat(testOpenAIModel())
	if !compat.SupportsStore || !compat.SupportsDeveloperRole || !compat.SupportsReasoningEffort ||
		compat.MaxTokensField != "max_completion_tokens" || compat.ThinkingFormat != "openai" {
		t.Fatalf("openai compat = %+v", compat)
	}

	// DeepSeek: max_tokens field, deepseek thinking format, reasoning content required.
	ds := DetectOpenAICompletionsCompat(&Model{ID: "v3", Provider: "deepseek", BaseURL: "https://api.deepseek.com"})
	if ds.MaxTokensField != "max_tokens" || ds.ThinkingFormat != "deepseek" || !ds.RequiresReasoningContentOnAssistantMessages {
		t.Fatalf("deepseek compat = %+v", ds)
	}
	if ds.SupportsStore {
		t.Fatalf("deepseek non-standard = %+v", ds)
	}
	// DeepSeek keeps reasoning_effort support upstream (not in the exclusion list).
	if !ds.SupportsReasoningEffort {
		t.Fatalf("deepseek reasoning effort = %+v", ds)
	}

	// z.ai: zai format, no reasoning effort, no strict.
	zai := DetectOpenAICompletionsCompat(&Model{ID: "glm", Provider: "zai", BaseURL: "https://api.z.ai/api/paas/v4"})
	if zai.ThinkingFormat != "zai" || zai.SupportsReasoningEffort || !zai.SupportsUsageInStreaming {
		t.Fatalf("zai compat = %+v", zai)
	}
	// z.ai is not in upstream's strict-mode exclusion list.
	if !zai.SupportsStrictMode {
		t.Fatalf("zai strict = %+v", zai)
	}

	// Moonshot: strict disabled.
	ms := DetectOpenAICompletionsCompat(&Model{ID: "kimi", Provider: "moonshotai", BaseURL: "https://api.moonshot.ai/v1"})
	if ms.SupportsStrictMode || ms.MaxTokensField != "max_tokens" {
		t.Fatalf("moonshot compat = %+v", ms)
	}

	// OpenRouter anthropic models: anthropic cache-control format, developer role.
	or := DetectOpenAICompletionsCompat(&Model{ID: "anthropic/claude-opus-4-5", Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1"})
	if or.CacheControlFormat != "anthropic" || !or.SupportsDeveloperRole || or.SessionAffinityFormat != SessionAffinityOpenRouter {
		t.Fatalf("openrouter compat = %+v", or)
	}

	// Explicit model.compat overrides detection.
	model := testOpenAIModel()
	model.Compat = &ModelCompat{OpenAICompletions: &OpenAICompletionsCompat{
		SupportsStore: boolPtrT(false), MaxTokensField: strPtr("max_tokens"),
	}}
	resolved := GetOpenAICompletionsCompat(model)
	if resolved.SupportsStore || resolved.MaxTokensField != "max_tokens" {
		t.Fatalf("override compat = %+v", resolved)
	}
	if !resolved.SupportsDeveloperRole {
		t.Fatal("non-overridden fields keep detected values")
	}
}

func TestBuildOpenAICompletionsParamsShape(t *testing.T) {
	model := testOpenAIModel()
	tool := Tool{Name: "read_file", Description: "Reads", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}
	ctx := NormalizeContext(Context{
		SystemPrompt: strPtr("sys"),
		Messages:     []Message{&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1}},
		Tools:        []Tool{tool},
	})
	params, err := BuildOpenAICompletionsParams(model, ctx, &OpenAICompletionsOptions{
		StreamOptions:   StreamOptions{SessionID: "sess", MaxTokens: intPtrT(4000), Temperature: floatPtrT(0.3)},
		ReasoningEffort: ThinkLow,
	}, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := MarshalJSON(params)
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(enc, &probe); err != nil {
		t.Fatal(err)
	}
	// OpenAI endpoint + retention != none → prompt_cache_key from sessionId.
	if string(probe["prompt_cache_key"]) != `"sess"` {
		t.Fatalf("prompt_cache_key = %s", probe["prompt_cache_key"])
	}
	if string(probe["stream"]) != "true" {
		t.Fatal("stream must be true")
	}
	if _, ok := probe["stream_options"]; !ok {
		t.Fatal("stream_options.include_usage expected")
	}
	if string(probe["max_completion_tokens"]) != "4000" {
		t.Fatalf("max_completion_tokens = %s", probe["max_completion_tokens"])
	}
	if string(probe["temperature"]) != "0.3" {
		t.Fatalf("temperature = %s", probe["temperature"])
	}
	if string(probe["reasoning_effort"]) != `"low"` {
		t.Fatalf("reasoning_effort = %s", probe["reasoning_effort"])
	}
	var tools []OpenAITool
	if err := json.Unmarshal(probe["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Function == nil || tools[0].Function.Name != "read_file" {
		t.Fatalf("tools = %+v", tools)
	}
	// OpenAI supports strict; a plain tool carries the explicit strict: false.
	if tools[0].Function.Strict == nil || *tools[0].Function.Strict {
		t.Fatalf("strict = %+v; want explicit false for plain tools", tools[0].Function.Strict)
	}
	var messages []OpenAIMessage
	if err := json.Unmarshal(probe["messages"], &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Role != "developer" {
		t.Fatalf("messages[0] = %+v", messages[0])
	}
}

func intPtrT(n int) *int           { return &n }
func floatPtrT(f float64) *float64 { return &f }

func TestBuildOpenAICompletionsParamsSamplingExtrasOverride(t *testing.T) {
	model := testOpenAIModel()
	params, err := BuildOpenAICompletionsParams(model, NormalizeContext(Context{}), &OpenAICompletionsOptions{
		StreamOptions: StreamOptions{
			Temperature:    floatPtrT(0.5),
			SamplingParams: map[string]json.RawMessage{"temperature": json.RawMessage("0.1"), "top_k": json.RawMessage("40")},
		},
	}, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	enc, _ := MarshalJSON(params)
	var probe map[string]json.RawMessage
	json.Unmarshal(enc, &probe)
	if string(probe["temperature"]) != "0.1" {
		t.Fatalf("sampling override = %s", probe["temperature"])
	}
	if string(probe["top_k"]) != "40" {
		t.Fatalf("top_k = %s", probe["top_k"])
	}
}

func TestConvertOpenAICompletionsMessagesAssistantShapes(t *testing.T) {
	model := testOpenAIModel()
	compat := DetectOpenAICompletionsCompat(model)

	// Assistant with tool calls: content null + tool_calls with stringified args.
	messages := []Message{
		&UserMessage{Content: StringOrBlocks{Text: "run"}, Timestamp: 1},
		&AssistantMessage{
			Content: ContentList{ToolCall{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{"cmd":"ls"}`)}},
			API:     APIOpenAICompletions, Provider: "openai", Model: "gpt-5",
			StopReason: StopToolUse, Timestamp: 2,
		},
		&ToolResultMessage{
			ToolCallID: "c1", ToolName: "bash",
			Content: UserContentList{TextContent{Text: "out"}}, Timestamp: 3,
		},
	}
	params := ConvertOpenAICompletionsMessages(model, TranscriptContext{Messages: messages}, compat, nil)
	if len(params) != 3 {
		t.Fatalf("params = %+v", params)
	}
	// assistant: content null, tool_calls present.
	if string(params[1].Content) != "null" {
		t.Fatalf("assistant content = %s; want null", params[1].Content)
	}
	if len(params[1].ToolCalls) != 1 || params[1].ToolCalls[0].ID != "c1" ||
		params[1].ToolCalls[0].Function.Arguments != `{"cmd":"ls"}` {
		t.Fatalf("tool_calls = %+v", params[1].ToolCalls)
	}
	// tool result.
	if params[2].Role != "tool" || params[2].ToolCallID != "c1" {
		t.Fatalf("tool message = %+v", params[2])
	}
	var toolText string
	json.Unmarshal(params[2].Content, &toolText)
	if toolText != "out" {
		t.Fatalf("tool content = %q", toolText)
	}

	// Thinking blocks with reasoning_content signature replay.
	messages = []Message{
		&AssistantMessage{
			Content: ContentList{
				ThinkingContent{Thinking: "thought", ThinkingSignature: strPtr("reasoning_content")},
				TextContent{Text: "answer"},
			},
			API: APIOpenAICompletions, Provider: "openai", Model: "gpt-5",
			StopReason: StopStop, Timestamp: 1,
		},
	}
	params = ConvertOpenAICompletionsMessages(model, TranscriptContext{Messages: messages}, compat, nil)
	if len(params) != 1 || params[0].ReasoningContent == nil || *params[0].ReasoningContent != "thought" {
		t.Fatalf("reasoning replay = %+v", params[0])
	}
	var text string
	json.Unmarshal(params[0].Content, &text)
	if text != "answer" {
		t.Fatalf("content = %q", text)
	}

	// requiresThinkingAsText converts thinking to text parts.
	model2 := *model
	model2.Compat = &ModelCompat{OpenAICompletions: &OpenAICompletionsCompat{RequiresThinkingAsText: boolPtrT(true)}}
	compat2 := GetOpenAICompletionsCompat(&model2)
	params = ConvertOpenAICompletionsMessages(&model2, TranscriptContext{Messages: messages}, compat2, nil)
	var parts []OpenAIContentPart
	json.Unmarshal(params[0].Content, &parts)
	if len(parts) != 2 || parts[0].Text != "thought" || parts[1].Text != "answer" {
		t.Fatalf("thinking-as-text = %+v", parts)
	}
}

func TestStreamOpenAICompletionsTextAndToolFlow(t *testing.T) {
	var chunks []string
	chunkJSON := func(s string) string {
		return "data: " + s + "\n\n"
	}
	chunks = append(chunks,
		chunkJSON(`{"id":"chatcmpl-1","model":"gpt-5","choices":[{"index":0,"delta":{"role":"assistant","content":"He"},"finish_reason":null}]}`),
		chunkJSON(`{"id":"chatcmpl-1","model":"gpt-5","choices":[{"index":0,"delta":{"content":"llo"},"finish_reason":null}]}`),
		chunkJSON(`{"id":"chatcmpl-1","model":"gpt-5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"bash","arguments":""}}]},"finish_reason":null}]}`),
		chunkJSON(`{"id":"chatcmpl-1","model":"gpt-5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\":"}}]},"finish_reason":null}]}`),
		chunkJSON(`{"id":"chatcmpl-1","model":"gpt-5","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"ls\"}"}}]},"finish_reason":null}]}`),
		chunkJSON(`{"id":"chatcmpl-1","model":"gpt-5","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`),
		"data: [DONE]\n\n",
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			_, _ = w.Write([]byte(c))
		}
	}))
	defer server.Close()

	model := testOpenAIModel()
	model.BaseURL = server.URL
	stream := StreamOpenAICompletions(model, NormalizeContext(Context{
		Messages: []Message{&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1}},
	}), &OpenAICompletionsOptions{StreamOptions: StreamOptions{APIKey: "k"}})

	var types []string
	var text string
	var finalCall *ToolCall
	for {
		event, ok := stream.Next(context.Background())
		if !ok {
			break
		}
		types = append(types, event.Type)
		if event.Type == EventTextDelta {
			text += event.Delta
		}
		if event.Type == EventToolcallEnd {
			finalCall = event.ToolCall
		}
	}
	if text != "Hello" {
		t.Fatalf("text = %q", text)
	}
	if finalCall == nil || finalCall.ID != "call_1" || finalCall.Name != "bash" || string(finalCall.Arguments) != `{"cmd":"ls"}` {
		t.Fatalf("toolCall = %+v", finalCall)
	}
	msg, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if msg.StopReason != StopToolUse || msg.ResponseID == nil || *msg.ResponseID != "chatcmpl-1" {
		t.Fatalf("message stopReason=%s responseId=%v", msg.StopReason, msg.ResponseID)
	}
	if msg.Usage.Input != 10 || msg.Usage.Output != 5 || msg.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", msg.Usage)
	}
	// Ends with text_end and toolcall_end finishing blocks.
	if types[len(types)-1] != EventDone {
		t.Fatalf("last = %s", types[len(types)-1])
	}
}

func TestStreamOpenAICompletionsReasoningFieldsAndDetails(t *testing.T) {
	chunks := []string{
		"data: {\"id\":\"c\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"think\"},\"finish_reason\":null}]}",
		"data: {\"id\":\"c\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"reasoning_details\":[{\"type\":\"reasoning.text\",\"text\":\" more\"}]},\"finish_reason\":null}]}",
		"data: {\"id\":\"c\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}",
		"data: [DONE]",
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, c := range chunks {
			_, _ = w.Write([]byte(c + "\n\n"))
		}
	}))
	defer server.Close()

	model := testOpenAIModel()
	model.BaseURL = server.URL
	stream := StreamOpenAICompletions(model, NormalizeContext(Context{
		Messages: []Message{&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1}},
	}), &OpenAICompletionsOptions{StreamOptions: StreamOptions{APIKey: "k"}})
	msg, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// The reasoning_content delta created a thinking block; reasoning_details
	// appended to it and serialize into the thinking signature at end.
	var found bool
	for _, block := range msg.Content {
		if tc, ok := block.(ThinkingContent); ok {
			found = true
			if tc.Thinking != "think" {
				t.Fatalf("thinking = %q", tc.Thinking)
			}
			if tc.ThinkingSignature == nil || !strings.Contains(*tc.ThinkingSignature, "reasoning.text") {
				t.Fatalf("signature = %v", tc.ThinkingSignature)
			}
		}
	}
	if !found {
		t.Fatalf("no thinking block: %+v", msg.Content)
	}
}

func TestStreamOpenAICompletionsErrorsAndFinishFallback(t *testing.T) {
	t.Run("missing key", func(t *testing.T) {
		model := testOpenAIModel()
		stream := StreamOpenAICompletionsSimple(model, NormalizeContext(Context{}), nil)
		msg, _ := stream.Result(context.Background())
		if msg.StopReason != StopError || *msg.ErrorMessage != "No API key for provider: openai" {
			t.Fatalf("message = %+v", msg)
		}
	})

	t.Run("finish_reason content_filter", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("data: {\"id\":\"c\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\ndata: [DONE]\n\n"))
		}))
		defer server.Close()
		model := testOpenAIModel()
		model.BaseURL = server.URL
		stream := StreamOpenAICompletions(model, NormalizeContext(Context{}), &OpenAICompletionsOptions{StreamOptions: StreamOptions{APIKey: "k"}})
		msg, _ := stream.Result(context.Background())
		if msg.StopReason != StopError || *msg.ErrorMessage != "Provider finish_reason: content_filter" {
			t.Fatalf("message = %+v", msg)
		}
	})

	t.Run("no finish reason with supportsFinishReason errors", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("data: {\"id\":\"c\",\"model\":\"m\",\"choices\":[]}\n\n"))
		}))
		defer server.Close()
		model := testOpenAIModel()
		model.BaseURL = server.URL
		stream := StreamOpenAICompletions(model, NormalizeContext(Context{}), &OpenAICompletionsOptions{StreamOptions: StreamOptions{APIKey: "k"}})
		msg, _ := stream.Result(context.Background())
		if msg.StopReason != StopError || *msg.ErrorMessage != "Stream ended without finish_reason" {
			t.Fatalf("message = %+v", msg)
		}
	})

	t.Run("no finish reason without supportsFinishReason infers stop", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("data: {\"id\":\"c\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\n"))
		}))
		defer server.Close()
		model := testOpenAIModel()
		model.BaseURL = server.URL
		model.Compat = &ModelCompat{OpenAICompletions: &OpenAICompletionsCompat{SupportsFinishReason: boolPtrT(false)}}
		stream := StreamOpenAICompletions(model, NormalizeContext(Context{}), &OpenAICompletionsOptions{StreamOptions: StreamOptions{APIKey: "k"}})
		msg, _ := stream.Result(context.Background())
		if msg.StopReason != StopStop {
			t.Fatalf("stopReason = %s", msg.StopReason)
		}
	})
}

func TestShortHash(t *testing.T) {
	// Deterministic and short.
	a := ShortHash("hello world")
	b := ShortHash("hello world")
	if a != b || len(a) < 4 {
		t.Fatalf("hash = %q", a)
	}
	if ShortHash("x") == ShortHash("y") {
		t.Fatal("different inputs should hash differently")
	}
}
