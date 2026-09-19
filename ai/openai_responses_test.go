package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// openai-responses tests keyed to upstream (openai-responses.ts +
// -shared.ts): compat resolution, params shape, message conversion, and the
// SSE event loop.

func testResponsesModel() *Model {
	return &Model{
		ID: "gpt-5", Name: "GPT-5", API: APIOpenAIResponses, Provider: "openai",
		BaseURL: "https://api.openai.com/v1", Reasoning: true, Input: []string{"text", "image"},
		Cost:          ModelCost{ModelCostRates: ModelCostRates{Input: 1.25, Output: 10, CacheRead: 0.125}},
		ContextWindow: 400000, MaxTokens: 128000,
	}
}

func TestResponsesCompatAndCacheFields(t *testing.T) {
	compat := GetOpenAIResponsesCompat(testResponsesModel())
	if !compat.SupportsDeveloperRole || !compat.SupportsLongCacheRetention || !compat.SupportsMaxOutputTokens {
		t.Fatalf("compat = %+v", compat)
	}
	// Long retention without explicit cache mode → 24h field.
	if retention := GetPromptCacheRetention(compat, CacheRetentionLong); retention != "24h" {
		t.Fatalf("retention = %q", retention)
	}
	if options := GetPromptCacheOptions(compat, CacheRetentionLong); options != nil {
		t.Fatalf("options = %+v; want nil without explicit mode", options)
	}

	// Explicit prompt-cache mode: none → {mode: explicit}; long → ttl 30m;
	// and the 24h retention field is suppressed.
	model := testResponsesModel()
	model.Compat = &ModelCompat{OpenAIResponses: &OpenAIResponsesCompat{
		SupportsExplicitPromptCacheMode: boolPtrT(true),
	}}
	compat = GetOpenAIResponsesCompat(model)
	if retention := GetPromptCacheRetention(compat, CacheRetentionLong); retention != "" {
		t.Fatalf("retention = %q; explicit mode suppresses it", retention)
	}
	if options := GetPromptCacheOptions(compat, CacheRetentionNone); options == nil || options.Mode != "explicit" {
		t.Fatalf("none options = %+v", options)
	}
	if options := GetPromptCacheOptions(compat, CacheRetentionLong); options == nil || options.TTL != "30m" {
		t.Fatalf("long options = %+v", options)
	}

	// OpenRouter session-affinity format detection.
	openRouter := testResponsesModel()
	openRouter.Provider = "openrouter"
	if format := detectOpenAIResponsesSessionAffinity(openRouter); format != SessionAffinityOpenRouter {
		t.Fatalf("affinity = %s", format)
	}
}

func TestBuildResponsesParams(t *testing.T) {
	model := testResponsesModel()
	tool := Tool{Name: "read_file", Description: "Reads", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)}
	ctx := NormalizeContext(Context{
		SystemPrompt: strPtrT("sys"),
		Messages:     []Message{&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1}},
		Tools:        []Tool{tool},
	})
	maxTokens := 8 // below the API minimum of 16
	params, err := BuildOpenAIResponsesParams(model, ctx, &OpenAIResponsesOptions{
		StreamOptions: StreamOptions{
			SessionID: "sess", MaxTokens: &maxTokens,
			CacheRetention: CacheRetentionLong,
		},
		ReasoningEffort: ThinkHigh,
		ServiceTier:     "flex",
	}, nil, nil)
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
	// max_output_tokens clamps UP to the API minimum (issue #6265).
	if string(probe["max_output_tokens"]) != "16" {
		t.Fatalf("max_output_tokens = %s; want 16 (clamped up)", probe["max_output_tokens"])
	}
	if string(probe["store"]) != "false" {
		t.Fatalf("store = %s", probe["store"])
	}
	if string(probe["prompt_cache_key"]) != `"sess"` {
		t.Fatalf("prompt_cache_key = %s", probe["prompt_cache_key"])
	}
	if string(probe["prompt_cache_retention"]) != `"24h"` {
		t.Fatalf("prompt_cache_retention = %s", probe["prompt_cache_retention"])
	}
	if string(probe["service_tier"]) != `"flex"` {
		t.Fatalf("service_tier = %s", probe["service_tier"])
	}
	// Reasoning effort + summary + encrypted-content include.
	if !strings.Contains(string(probe["reasoning"]), `"effort":"high"`) ||
		!strings.Contains(string(probe["reasoning"]), `"summary":"auto"`) {
		t.Fatalf("reasoning = %s", probe["reasoning"])
	}
	if string(probe["include"]) != `["reasoning.encrypted_content"]` {
		t.Fatalf("include = %s", probe["include"])
	}
	var tools []OpenAIResponsesTool
	if err := json.Unmarshal(probe["tools"], &tools); err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 || tools[0].Type != "function" || tools[0].Name != "read_file" {
		t.Fatalf("tools = %+v", tools)
	}
}

func TestConvertResponsesMessages(t *testing.T) {
	model := testResponsesModel()
	messages := []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hello"}, Timestamp: 1},
		&AssistantMessage{
			Content: ContentList{
				ThinkingContent{Thinking: "hmm", ThinkingSignature: strPtrT(`{"type":"reasoning","id":"rs_1","summary":[]}`)},
				TextContent{Text: "answer"},
				ToolCall{ID: "call_1|fc_1", Name: "bash", Arguments: json.RawMessage(`{"cmd":"ls"}`)},
			},
			API: APIOpenAIResponses, Provider: "openai", Model: "gpt-5",
			StopReason: StopToolUse, Timestamp: 2,
		},
		&ToolResultMessage{
			ToolCallID: "call_1|fc_1", ToolName: "bash",
			Content: UserContentList{TextContent{Text: "out"}}, Timestamp: 3,
		},
	}
	input, err := ConvertResponsesMessages(model, TranscriptContext{Messages: messages}, nil)
	if err != nil {
		t.Fatal(err)
	}
	text := string(input)
	// Reasoning items replay verbatim as their raw signature JSON.
	if !strings.Contains(text, `{"type":"reasoning","id":"rs_1","summary":[]}`) {
		t.Fatalf("reasoning replay missing: %s", text)
	}
	// Assistant text becomes a message item with an output_text part.
	if !strings.Contains(text, `"type":"message"`) || !strings.Contains(text, `"type":"output_text"`) {
		t.Fatalf("message item missing: %s", text)
	}
	// Tool call keeps call_id|item_id for OpenAI-family providers.
	if !strings.Contains(text, `"type":"function_call"`) || !strings.Contains(text, `"call_id":"call_1"`) ||
		!strings.Contains(text, `"id":"fc_1"`) {
		t.Fatalf("function_call missing: %s", text)
	}
	// Tool result becomes function_call_output keyed by call_id only.
	if !strings.Contains(text, `"type":"function_call_output"`) || !strings.Contains(text, `"output":"out"`) {
		t.Fatalf("function_call_output missing: %s", text)
	}
}

func TestConvertResponsesToolResultWithImages(t *testing.T) {
	model := testResponsesModel()
	output := convertResponsesToolResultOutput(model, UserContentList{
		TextContent{Text: "caption"},
		ImageContent{Data: "aGk=", MimeType: "image/png"},
	})
	list, ok := output.([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("output = %+v", output)
	}
	encoded, _ := MarshalJSON(list)
	if !strings.Contains(string(encoded), `"type":"input_text"`) ||
		!strings.Contains(string(encoded), `"image_url":"data:image/png;base64,aGk="`) {
		t.Fatalf("encoded = %s", encoded)
	}

	// Non-vision models get the placeholder text instead.
	noVision := testResponsesModel()
	noVision.Input = []string{"text"}
	if got := convertResponsesToolResultOutput(noVision, UserContentList{ImageContent{Data: "aGk=", MimeType: "image/png"}}); got != "(see attached image)" {
		t.Fatalf("non-vision output = %v", got)
	}
	if got := convertResponsesToolResultOutput(model, UserContentList{}); got != "(no tool output)" {
		t.Fatalf("empty output = %v", got)
	}
}

func TestProcessResponsesStreamTextAndReasoning(t *testing.T) {
	events := []string{
		`{"type":"response.created","response":{"id":"resp_1"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
		`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"thinking..."}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[{"text":"thinking..."}],"encrypted_content":"enc"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_1","role":"assistant"}}`,
		`{"type":"response.output_text.delta","output_index":1,"delta":"Hel"}`,
		`{"type":"response.output_text.delta","output_index":1,"delta":"lo"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"msg_1","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Hello"}]}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":100,"output_tokens":10,"total_tokens":110,"input_tokens_details":{"cached_tokens":40},"output_tokens_details":{"reasoning_tokens":5}}}}`,
		`[DONE]`,
	}
	var chunks []string
	for _, event := range events {
		chunks = append(chunks, "data: "+event+"\n\n")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, chunk := range chunks {
			_, _ = w.Write([]byte(chunk))
		}
	}))
	defer server.Close()

	model := testResponsesModel()
	model.BaseURL = server.URL
	stream := StreamOpenAIResponses(model, NormalizeContext(Context{
		Messages: []Message{&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1}},
	}), &OpenAIResponsesOptions{StreamOptions: StreamOptions{APIKey: "k"}})

	var types []string
	var text, thinking string
	for {
		event, ok := stream.Next(context.Background())
		if !ok {
			break
		}
		types = append(types, event.Type)
		if event.Type == EventTextDelta {
			text += event.Delta
		}
		if event.Type == EventThinkingDelta {
			thinking += event.Delta
		}
	}
	if text != "Hello" || thinking != "thinking..." {
		t.Fatalf("text=%q thinking=%q", text, thinking)
	}
	want := []string{"start", "thinking_start", "thinking_delta", "thinking_end", "text_start", "text_delta", "text_delta", "text_end", "done"}
	if len(types) != len(want) {
		t.Fatalf("events = %v; want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("events = %v; want %v", types, want)
		}
	}
	msg, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if msg.StopReason != StopStop || msg.ResponseID == nil || *msg.ResponseID != "resp_1" {
		t.Fatalf("message = %+v", msg)
	}
	// Usage: cached tokens split out of input_tokens.
	if msg.Usage.Input != 60 || msg.Usage.Output != 10 || msg.Usage.CacheRead != 40 || msg.Usage.TotalTokens != 110 {
		t.Fatalf("usage = %+v", msg.Usage)
	}
	if msg.Usage.Reasoning == nil || *msg.Usage.Reasoning != 5 {
		t.Fatalf("reasoning = %v", msg.Usage.Reasoning)
	}
	// The reasoning signature carries the encrypted content.
	thinkingBlock := msg.Content[0].(ThinkingContent)
	if thinkingBlock.ThinkingSignature == nil || !strings.Contains(*thinkingBlock.ThinkingSignature, "encrypted_content") {
		t.Fatalf("signature = %v", thinkingBlock.ThinkingSignature)
	}
	// Assistant text block carries a v1 text signature.
	textBlock := msg.Content[1].(TextContent)
	if textBlock.TextSignature == nil || !strings.Contains(*textBlock.TextSignature, `"v":1`) ||
		!strings.Contains(*textBlock.TextSignature, `"phase":"final_answer"`) {
		t.Fatalf("textSignature = %v", textBlock.TextSignature)
	}
}

func TestProcessResponsesStreamFunctionCallAndErrors(t *testing.T) {
	t.Run("function call", func(t *testing.T) {
		events := []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash"}}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"cmd\":"}`,
			`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"ls\"}"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":"{\"cmd\":\"ls\"}"}}`,
			`{"type":"response.completed","response":{"id":"resp","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
			`[DONE]`,
		}
		var chunks []string
		for _, event := range events {
			chunks = append(chunks, "data: "+event+"\n\n")
		}
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, chunk := range chunks {
				_, _ = w.Write([]byte(chunk))
			}
		}))
		defer server.Close()
		model := testResponsesModel()
		model.BaseURL = server.URL
		stream := StreamOpenAIResponses(model, NormalizeContext(Context{}),
			&OpenAIResponsesOptions{StreamOptions: StreamOptions{APIKey: "k"}})
		msg, err := stream.Result(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		// Tool calls upgrade the stop reason to toolUse.
		if msg.StopReason != StopToolUse {
			t.Fatalf("stopReason = %s", msg.StopReason)
		}
		call := msg.Content[0].(ToolCall)
		if call.ID != "call_1|fc_1" || call.Name != "bash" || string(call.Arguments) != `{"cmd":"ls"}` {
			t.Fatalf("call = %+v", call)
		}
	})

	t.Run("response.failed", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"server_error\",\"message\":\"boom\"}}}\n\n"))
		}))
		defer server.Close()
		model := testResponsesModel()
		model.BaseURL = server.URL
		stream := StreamOpenAIResponses(model, NormalizeContext(Context{}),
			&OpenAIResponsesOptions{StreamOptions: StreamOptions{APIKey: "k"}})
		msg, _ := stream.Result(context.Background())
		if msg.StopReason != StopError || msg.ErrorMessage == nil || !strings.Contains(*msg.ErrorMessage, "server_error: boom") {
			t.Fatalf("message = %+v", msg)
		}
	})

	t.Run("no terminal event", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\n"))
		}))
		defer server.Close()
		model := testResponsesModel()
		model.BaseURL = server.URL
		stream := StreamOpenAIResponses(model, NormalizeContext(Context{}),
			&OpenAIResponsesOptions{StreamOptions: StreamOptions{APIKey: "k"}})
		msg, _ := stream.Result(context.Background())
		if msg.StopReason != StopError || !strings.Contains(*msg.ErrorMessage, "before a terminal response event") {
			t.Fatalf("message = %+v", msg)
		}
	})

	t.Run("incomplete max_output_tokens is length", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("data: {\"type\":\"response.incomplete\",\"response\":{\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n"))
		}))
		defer server.Close()
		model := testResponsesModel()
		model.BaseURL = server.URL
		stream := StreamOpenAIResponses(model, NormalizeContext(Context{}),
			&OpenAIResponsesOptions{StreamOptions: StreamOptions{APIKey: "k"}})
		msg, _ := stream.Result(context.Background())
		if msg.StopReason != StopLength || msg.RawStopReason == nil || *msg.RawStopReason != "incomplete.max_output_tokens" {
			t.Fatalf("message = %+v", msg)
		}
	})
}

func TestServiceTierPricing(t *testing.T) {
	model := testResponsesModel()
	usage := Usage{Cost: UsageCost{Input: 2, Output: 4, CacheRead: 1, CacheWrite: 1, Total: 8}}
	applyServiceTierPricing(&usage, "flex", model)
	if usage.Cost.Input != 1 || usage.Cost.Output != 2 || usage.Cost.Total != 4 {
		t.Fatalf("flex pricing = %+v", usage.Cost)
	}
	usage = Usage{Cost: UsageCost{Input: 1, Output: 1, Total: 2}}
	applyServiceTierPricing(&usage, "priority", model)
	if usage.Cost.Input != 2 || usage.Cost.Total != 4 {
		t.Fatalf("priority pricing = %+v", usage.Cost)
	}
	if getServiceTierCostMultiplier(&Model{ID: "gpt-5.5"}, "priority") != 2.5 {
		t.Fatal("gpt-5.5 priority multiplier")
	}
}

func strPtrT(s string) *string { return &s }
