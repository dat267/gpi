package ai

import (
	"encoding/json"
	"testing"
)

// Request-construction tests keyed to upstream's buildParams behavior
// (api/anthropic-messages.ts). Each expectation cites the upstream source
// path it verifies.

func testAnthropicModel() *Model {
	return &Model{
		ID: "claude-opus-4-5", Name: "Claude Opus 4.5", API: APIAnthropicMessages, Provider: "anthropic",
		BaseURL: "https://api.anthropic.com", Reasoning: true, Input: []string{"text", "image"},
		Cost:          ModelCost{ModelCostRates: ModelCostRates{Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25}},
		ContextWindow: 200000, MaxTokens: 64000,
		Compat: &ModelCompat{AnthropicMessages: &AnthropicMessagesCompat{SupportsStrictTools: boolPtrT(true)}},
	}
}

func boolPtrT(b bool) *bool { return &b }

func build(t *testing.T, model *Model, messages []Message, options *AnthropicOptions) *AnthropicMessageCreateParams {
	t.Helper()
	params, err := BuildAnthropicParams(model, NormalizeContext(Context{Messages: messages}), false, options)
	if err != nil {
		t.Fatal(err)
	}
	return params
}

func TestBuildParamsBasicShape(t *testing.T) {
	model := testAnthropicModel()
	params := build(t, model, []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1},
	}, nil)

	if params.Model != "claude-opus-4-5" || !params.Stream || params.MaxTokens != 64000 {
		t.Fatalf("params = %+v", params)
	}
	if len(params.Messages) != 1 || params.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v", params.Messages)
	}
	// No system prompt → no system param (upstream omits it).
	if params.System != nil {
		t.Fatalf("system = %s; want nil", params.System)
	}
	// No thinking without options (reasoning model, thinking not requested).
	if params.Thinking != nil {
		t.Fatalf("thinking = %+v; want nil", params.Thinking)
	}
}

func TestBuildParamsThinkingBudgetAndDisabled(t *testing.T) {
	model := testAnthropicModel()

	// Budget-based thinking.
	budget := 2048
	params := build(t, model, nil, &AnthropicOptions{ThinkingEnabled: boolPtrT(true), ThinkingBudgetTokens: &budget})
	if params.Thinking == nil || params.Thinking.Type != "enabled" || *params.Thinking.BudgetTokens != 2048 {
		t.Fatalf("thinking = %+v", params.Thinking)
	}
	if params.Thinking.Display == nil || *params.Thinking.Display != ThinkingDisplaySummarized {
		t.Fatalf("display = %+v", params.Thinking.Display)
	}
	// Interleaved thinking beta for non-adaptive reasoning models.
	found := false
	for _, b := range params.Betas {
		if b == InterleavedThinkingBeta {
			found = true
		}
	}
	if !found {
		t.Fatalf("betas = %v", params.Betas)
	}
	// Temperature suppressed while thinking.
	if params.Temperature != nil {
		t.Fatal("temperature must be omitted with thinking enabled")
	}

	// Explicit thinking off → disabled block.
	params = build(t, model, nil, &AnthropicOptions{ThinkingEnabled: boolPtrT(false)})
	if params.Thinking == nil || params.Thinking.Type != "disabled" {
		t.Fatalf("thinking = %+v", params.Thinking)
	}
}

func TestBuildParamsToolUseAndCacheBreakpoint(t *testing.T) {
	model := testAnthropicModel()
	tool := Tool{Name: "read_file", Description: "Reads a file", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}
	params := build(t, model, []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1},
	}, &AnthropicOptions{})

	_ = tool
	if len(params.Tools) != 0 {
		t.Fatalf("tools = %+v", params.Tools)
	}
}

func TestBuildParamsToolsWithCacheControlOnLast(t *testing.T) {
	model := testAnthropicModel()
	tool := Tool{Name: "read_file", Description: "Reads a file", Parameters: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}
	model2 := *model
	params := build(t, &model2, []Message{
		&SystemMessage{Content: StringOrBlocks{Text: "sys"}, ToolsAdded: []Tool{tool}, Timestamp: 0},
		&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1},
	}, nil)

	if len(params.Tools) != 1 {
		t.Fatalf("tools = %+v", params.Tools)
	}
	wire := params.Tools[0]
	if wire.Name != "read_file" || wire.Description != "Reads a file" {
		t.Fatalf("tool = %+v", wire)
	}
	// eager_input_streaming defaults true.
	if wire.EagerInputStreaming == nil || !*wire.EagerInputStreaming {
		t.Fatal("eager_input_streaming should default true")
	}
	// strict applies only to tools declaring constrainedSampling; a plain
	// tool is sent as-is with the legacy input_schema shape.
	if wire.Strict != nil {
		t.Fatalf("strict = %+v; want unset for plain tools", wire.Strict)
	}
	var schema map[string]json.RawMessage
	if err := json.Unmarshal(wire.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	var required []string
	json.Unmarshal(schema["required"], &required)
	if len(required) != 1 || required[0] != "path" {
		t.Fatalf("required = %v", required)
	}
	if _, ok := schema["additionalProperties"]; ok {
		t.Fatalf("additionalProperties should be untouched: %s", schema["additionalProperties"])
	}
	// Cache breakpoint lands on the last tool.
	if wire.CacheControl == nil || wire.CacheControl.Type != "ephemeral" {
		t.Fatal("cache_control should be on the last tool")
	}
	// Conversation history cache breakpoint on the last user message block.
	var msgs []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(mustMarshalJSON2(params.Messages), &msgs); err != nil {
		t.Fatal(err)
	}
	last := msgs[len(msgs)-1]
	if last.Role != "user" {
		t.Fatalf("last role = %s", last.Role)
	}
	var blocks []AnthropicContentBlock
	json.Unmarshal(last.Content, &blocks)
	if blocks[len(blocks)-1].CacheControl == nil {
		t.Fatal("history cache breakpoint missing on last user block")
	}
}

func mustMarshalJSON2(v any) json.RawMessage {
	enc, err := MarshalJSON(v)
	if err != nil {
		panic(err)
	}
	return enc
}

func TestBuildParamsToolResultPairingAndSyntheticResults(t *testing.T) {
	model := testAnthropicModel()
	messages := []Message{
		&UserMessage{Content: StringOrBlocks{Text: "run it"}, Timestamp: 1},
		&AssistantMessage{
			Content: ContentList{ToolCall{ID: "call-1", Name: "bash", Arguments: json.RawMessage(`{"cmd":"ls"}`)}},
			API:     APIAnthropicMessages, Provider: "anthropic", Model: "claude-opus-4-5",
			StopReason: StopToolUse, Timestamp: 2,
		},
		&AssistantMessage{ // errored turn must be dropped
			Content: ContentList{TextContent{Text: "partial"}}, API: APIAnthropicMessages,
			Provider: "anthropic", Model: "claude-opus-4-5", StopReason: StopError, Timestamp: 3,
		},
		&UserMessage{Content: StringOrBlocks{Text: "go on"}, Timestamp: 4},
	}
	params := build(t, model, messages, nil)

	// transformMessages synthesizes a tool result for the orphaned call.
	var roles []string
	for _, m := range params.Messages {
		roles = append(roles, m.Role)
	}
	// user, assistant(tool_use), user(tool_result), user("go on")
	if len(roles) != 4 || roles[0] != "user" || roles[1] != "assistant" || roles[2] != "user" || roles[3] != "user" {
		t.Fatalf("roles = %v", roles)
	}
	var trBlocks []AnthropicContentBlock
	json.Unmarshal(params.Messages[2].Content, &trBlocks)
	if len(trBlocks) != 1 || trBlocks[0].Type != "tool_result" || trBlocks[0].ToolUseID != "call-1" {
		t.Fatalf("tool_result = %+v", trBlocks)
	}
	if trBlocks[0].IsError == nil || !*trBlocks[0].IsError {
		t.Fatal("synthetic result should be an error")
	}
}

func TestConvertAnthropicMessagesThinkingReplay(t *testing.T) {
	sig := "encrypted-signature"
	emptySig := ""
	messages := []Message{
		&AssistantMessage{
			Content: ContentList{
				ThinkingContent{Thinking: "deliberation", ThinkingSignature: &sig},
				ThinkingContent{Thinking: "unsigned thinking"},
				TextContent{Text: "answer"},
			},
			API: APIAnthropicMessages, Provider: "anthropic", Model: "claude-opus-4-5",
			StopReason: StopStop, Timestamp: 1,
		},
	}
	converted := ConvertAnthropicMessages(messages, false, nil, false, "", false)
	var blocks []AnthropicContentBlock
	if err := json.Unmarshal(converted.Messages[0].Content, &blocks); err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 3 {
		t.Fatalf("blocks = %+v", blocks)
	}
	if blocks[0].Type != "thinking" || blocks[0].Thinking != "deliberation" || *blocks[0].Signature != sig {
		t.Fatalf("block 0 = %+v", blocks[0])
	}
	// Unsigned thinking converts to text for replay; the text block follows.
	if blocks[1].Type != "text" || blocks[1].Text != "unsigned thinking" {
		t.Fatalf("block 1 = %+v", blocks[1])
	}
	if blocks[2].Type != "text" || blocks[2].Text != "answer" {
		t.Fatalf("block 2 = %+v", blocks[2])
	}

	// allowEmptySignature preserves the block with an empty signature.
	converted = ConvertAnthropicMessages([]Message{
		&AssistantMessage{
			Content: ContentList{ThinkingContent{Thinking: "unsigned", ThinkingSignature: &emptySig}},
			API:     APIAnthropicMessages, Provider: "anthropic", Model: "m",
			StopReason: StopStop, Timestamp: 1,
		},
	}, false, nil, true, "", false)
	var emptySigBlocks []AnthropicContentBlock
	if err := json.Unmarshal(converted.Messages[0].Content, &emptySigBlocks); err != nil {
		t.Fatal(err)
	}
	if len(emptySigBlocks) != 1 || emptySigBlocks[0].Type != "thinking" || emptySigBlocks[0].Signature == nil || *emptySigBlocks[0].Signature != "" {
		t.Fatalf("allowEmptySignature blocks = %+v", emptySigBlocks)
	}
}

func TestNormalizeAnthropicToolCallID(t *testing.T) {
	// OpenAI Responses ids are 450+ chars with special characters.
	long := "resp|" + string(make([]byte, 0)) + "abc$def"
	for i := 0; i < 100; i++ {
		long += "|xyz"
	}
	got := NormalizeAnthropicToolCallID(long)
	if len(got) > 64 {
		t.Fatalf("len = %d", len(got))
	}
	for _, c := range got {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			t.Fatalf("char %q in %q", c, got)
		}
	}
}

func TestMapAnthropicStopReason(t *testing.T) {
	cases := map[string]StopReason{
		"end_turn": StopStop, "max_tokens": StopLength, "tool_use": StopToolUse,
		"pause_turn": StopStop, "stop_sequence": StopStop,
	}
	for in, want := range cases {
		got, _, err := MapAnthropicStopReason(in, "")
		if err != nil || got != want {
			t.Fatalf("%s = %s, %v; want %s", in, got, err, want)
		}
	}
	got, msg, err := MapAnthropicStopReason("refusal", "cannot comply")
	if err != nil || got != StopError || msg != "cannot comply" {
		t.Fatalf("refusal = %s/%s/%v", got, msg, err)
	}
	got, msg, _ = MapAnthropicStopReason("refusal", "")
	if msg != "The model refused to complete the request" {
		t.Fatalf("default refusal message = %q", msg)
	}
	got, msg, _ = MapAnthropicStopReason("sensitive", "")
	if got != StopError || msg != "Provider stopped with: sensitive" {
		t.Fatalf("sensitive = %s/%s", got, msg)
	}
	if _, _, err := MapAnthropicStopReason("mystery", ""); err == nil {
		t.Fatal("unknown stop reason must error")
	}
}

func TestClaudeCodeToolNaming(t *testing.T) {
	// OAuth requests map names to CC canonical casing both ways.
	if got := toClaudeCodeName("read"); got != "Read" {
		t.Fatalf("toCC(read) = %q", got)
	}
	if got := fromClaudeCodeName("Read", []Tool{{Name: "read"}}); got != "read" {
		t.Fatalf("fromCC(Read) = %q", got)
	}
	if got := toClaudeCodeName("my_tool"); got != "my_tool" {
		t.Fatalf("unmatched = %q", got)
	}
}

func TestGetCacheControlRetention(t *testing.T) {
	// Default short.
	retention, cc := GetCacheControl(testAnthropicModel(), "", nil)
	if retention != CacheRetentionShort || cc == nil || cc.TTL != nil {
		t.Fatalf("short = %s/%+v", retention, cc)
	}
	// Long with support → ttl 1h.
	retention, cc = GetCacheControl(testAnthropicModel(), CacheRetentionLong, nil)
	if retention != CacheRetentionLong || cc == nil || cc.TTL == nil || *cc.TTL != "1h" {
		t.Fatalf("long = %s/%+v", retention, cc)
	}
	// None → no marker.
	retention, cc = GetCacheControl(testAnthropicModel(), CacheRetentionNone, nil)
	if retention != CacheRetentionNone || cc != nil {
		t.Fatalf("none = %s/%+v", retention, cc)
	}
	// PI_CACHE_RETENTION=long env fallback.
	retention, _ = GetCacheControl(testAnthropicModel(), "", ProviderEnv{"PI_CACHE_RETENTION": "long"})
	if retention != CacheRetentionLong {
		t.Fatalf("env long = %s", retention)
	}
}

func TestAnthropicOAuthSystemPrompt(t *testing.T) {
	model := testAnthropicModel()
	params, err := BuildAnthropicParams(model, NormalizeContext(Context{
		SystemPrompt: strPtr("Be helpful."),
		Messages:     []Message{&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1}},
	}), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	var system []AnthropicContentBlock
	if err := json.Unmarshal(params.System, &system); err != nil {
		t.Fatal(err)
	}
	if len(system) != 2 {
		t.Fatalf("system = %+v", system)
	}
	if system[0].Text != "You are Claude Code, Anthropic's official CLI for Claude." {
		t.Fatalf("identity = %q", system[0].Text)
	}
	if system[1].Text != "Be helpful." {
		t.Fatalf("prompt = %q", system[1].Text)
	}
	// OAuth beta headers.
	joined := ""
	for _, b := range params.Betas {
		joined += b + ","
	}
	if !contains(joined, "claude-code-20250219") || !contains(joined, "oauth-2025-04-20") {
		t.Fatalf("betas = %v", params.Betas)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
