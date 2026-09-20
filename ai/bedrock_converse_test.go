package ai

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Tests for api/bedrock-converse-stream.ts (message conversion, request
// construction, and the streaming event fold).

func bedrockTestModel(baseURL string) *Model {
	return &Model{
		ID: "anthropic.claude-sonnet-4-5-20250929-v1:0", Name: "Claude Sonnet 4.5",
		API: APIBedrockConverse, Provider: "amazon-bedrock", BaseURL: baseURL,
		Reasoning: true, Input: []string{"text", "image"}, ContextWindow: 200000, MaxTokens: 8192,
		Cost: ModelCost{ModelCostRates: ModelCostRates{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}},
	}
}

func TestBedrockModelPredicates(t *testing.T) {
	claude := &Model{ID: "anthropic.claude-sonnet-4-5-v1:0", Name: "Claude Sonnet 4.5"}
	if !IsAnthropicClaudeModel(claude) {
		t.Fatal("Claude models must be detected")
	}
	if IsAnthropicClaudeModel(&Model{ID: "amazon.nova-pro-v1:0", Name: "Nova Pro"}) {
		t.Fatal("Nova is not Claude")
	}
	// Adaptive thinking models.
	for _, id := range []string{"anthropic.claude-opus-4-6-v1", "us.anthropic.claude-sonnet-5", "anthropic.claude-fable-5"} {
		if !SupportsAdaptiveThinking(id, "") {
			t.Errorf("%s must support adaptive thinking", id)
		}
	}
	if SupportsAdaptiveThinking("anthropic.claude-sonnet-4-5-v1:0", "") {
		t.Fatal("Sonnet 4.5 is not adaptive")
	}
	// The name is checked too (inference-profile ARNs).
	if !SupportsAdaptiveThinking("arn:aws:bedrock:us-east-1:1:application-inference-profile/abc", "Claude Opus 4.8") {
		t.Fatal("the model name must be considered")
	}
	if !SupportsNativeXhighEffort(&Model{ID: "anthropic.claude-opus-4-8-v1"}) {
		t.Fatal("Opus 4.8 supports native xhigh")
	}
	if SupportsNativeXhighEffort(&Model{ID: "anthropic.claude-sonnet-4-5-v1:0"}) {
		t.Fatal("Sonnet 4.5 has no native xhigh")
	}

	// Prompt caching support matrix.
	caching := map[string]bool{
		"anthropic.claude-sonnet-4-5-v1:0":     true,
		"anthropic.claude-3-7-sonnet-20250219": true,
		"anthropic.claude-3-5-haiku-20241022":  true,
		"anthropic.claude-fable-5":             true,
		"amazon.nova-pro-v1:0":                 false,
	}
	for id, want := range caching {
		if got := SupportsBedrockPromptCaching(&Model{ID: id}, nil); got != want {
			t.Errorf("SupportsBedrockPromptCaching(%s) = %v, want %v", id, got, want)
		}
	}
	// The environment override enables cache points for inference profiles.
	profile := &Model{ID: "arn:aws:bedrock:us-east-1:1:application-inference-profile/abc"}
	if SupportsBedrockPromptCaching(profile, ProviderEnv{"AWS_BEDROCK_FORCE_CACHE": "1"}) != true {
		t.Fatal("AWS_BEDROCK_FORCE_CACHE must enable cache points")
	}
}

func TestBedrockThinkingFields(t *testing.T) {
	model := &Model{ID: "anthropic.claude-sonnet-4-5-v1:0", Name: "Claude Sonnet 4.5", Reasoning: true}
	options := &BedrockOptions{Reasoning: ThinkHigh}
	fields := BuildBedrockAdditionalModelRequestFields(model, options)
	thinking, _ := fields["thinking"].(map[string]any)
	if thinking["type"] != "enabled" || thinking["budget_tokens"] != 16384 || thinking["display"] != "summarized" {
		t.Fatalf("thinking = %#v", thinking)
	}
	// Custom budgets apply to token-based levels; xhigh/max clamp to high.
	options.ThinkingBudgets = &ThinkingBudgets{High: intPtr(4096)}
	fields = BuildBedrockAdditionalModelRequestFields(model, options)
	thinking, _ = fields["thinking"].(map[string]any)
	if thinking["budget_tokens"] != 4096 {
		t.Fatalf("thinking = %#v", thinking)
	}
	options.Reasoning = ThinkMax
	fields = BuildBedrockAdditionalModelRequestFields(model, options)
	thinking, _ = fields["thinking"].(map[string]any)
	if thinking["budget_tokens"] != 4096 {
		t.Fatalf("xhigh/max must clamp to the high budget: %#v", thinking)
	}
	// Interleaved thinking adds the Anthropic beta.
	beta, _ := fields["anthropic_beta"].([]any)
	if len(beta) != 1 || beta[0] != "interleaved-thinking-2025-05-14" {
		t.Fatalf("beta = %#v", fields["anthropic_beta"])
	}
	options.InterleavedThinking = boolPtrForBedrock(false)
	fields = BuildBedrockAdditionalModelRequestFields(model, options)
	if fields["anthropic_beta"] != nil {
		t.Fatalf("interleaved thinking was disabled: %#v", fields["anthropic_beta"])
	}

	// Adaptive models use effort instead of a budget.
	adaptive := &Model{ID: "anthropic.claude-opus-4-6-v1", Name: "Claude Opus 4.6", Reasoning: true}
	fields = BuildBedrockAdditionalModelRequestFields(adaptive, &BedrockOptions{Reasoning: ThinkMedium})
	thinking, _ = fields["thinking"].(map[string]any)
	if thinking["type"] != "adaptive" || thinking["budget_tokens"] != nil {
		t.Fatalf("thinking = %#v", thinking)
	}
	config, _ := fields["output_config"].(map[string]any)
	if config["effort"] != "medium" {
		t.Fatalf("output_config = %#v", config)
	}
	// GovCloud omits the display field.
	fields = BuildBedrockAdditionalModelRequestFields(adaptive, &BedrockOptions{StreamOptions: StreamOptions{
		Env: ProviderEnv{"AWS_REGION": "us-gov-west-1"},
	}, Reasoning: ThinkHigh})
	thinking, _ = fields["thinking"].(map[string]any)
	if _, hasDisplay := thinking["display"]; hasDisplay {
		t.Fatalf("GovCloud must omit display: %#v", thinking)
	}
	// Non-Claude models and missing reasoning produce no fields.
	if fields := BuildBedrockAdditionalModelRequestFields(&Model{ID: "amazon.nova-pro-v1:0", Reasoning: true}, &BedrockOptions{Reasoning: ThinkHigh}); fields != nil {
		t.Fatalf("fields = %#v", fields)
	}
	if fields := BuildBedrockAdditionalModelRequestFields(model, &BedrockOptions{}); fields != nil {
		t.Fatalf("fields = %#v", fields)
	}
}

func boolPtrForBedrock(value bool) *bool { return &value }

func TestBedrockMessageConversion(t *testing.T) {
	model := bedrockTestModel("")

	// User text, images, and empty-content handling.
	image := ImageContent{MimeType: "image/png", Data: base64.StdEncoding.EncodeToString([]byte("png"))}
	messages, err := ConvertBedrockMessages(TranscriptContext{Messages: []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hello"}, Timestamp: 1},
		&UserMessage{Content: StringOrBlocks{Text: "   "}, Timestamp: 2},
		&UserMessage{Content: StringOrBlocks{Blocks: ContentList{image}}, Timestamp: 3},
	}}, model, CacheRetentionNone, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 {
		t.Fatalf("messages = %#v", messages)
	}
	first, _ := messages[0].(map[string]any)
	content, _ := first["content"].([]any)
	if first["role"] != "user" || len(content) != 1 {
		t.Fatalf("first = %#v", first)
	}
	// Blank text becomes the placeholder.
	second, _ := messages[1].(map[string]any)
	secondContent, _ := second["content"].([]any)
	if block, _ := secondContent[0].(map[string]any); block["text"] != "<empty>" {
		t.Fatalf("second = %#v", second)
	}
	// Images convert to the source/format shape.
	third, _ := messages[2].(map[string]any)
	imageContent, _ := third["content"].([]any)
	imageBlock, _ := imageContent[0].(map[string]any)
	source, _ := imageBlock["image"].(map[string]any)
	if source["format"] != "png" {
		t.Fatalf("image = %#v", imageBlock)
	}
	bytesValue, _ := source["source"].(map[string]any)["bytes"].([]byte)
	if string(bytesValue) != "png" {
		t.Fatalf("image bytes = %#v", bytesValue)
	}

	// Assistant text/tool-call/thinking, including redacted replay. The
	// assistant message must belong to the target model for signatures to
	// survive the shared transcript transform.
	signature := base64.StdEncoding.EncodeToString([]byte("sig-bytes"))
	assistantMessage := &AssistantMessage{
		Provider: model.Provider, API: model.API, Model: model.ID,
		Content: ContentList{
			TextContent{Text: "answer"},
			ThinkingContent{Thinking: "reasoning", ThinkingSignature: &signature},
			ThinkingContent{Thinking: "[Reasoning redacted]", Redacted: true, ThinkingSignature: &signature},
			ThinkingContent{Thinking: "   "},
			ToolCall{ID: "call-1", Name: "read", Arguments: json.RawMessage(`{"path":"a"}`)},
		},
	}
	messages, err = ConvertBedrockMessages(TranscriptContext{Messages: []Message{
		assistantMessage,
		// A matching tool result avoids the synthetic orphan result.
		&ToolResultMessage{ToolCallID: "call-1", ToolName: "read", Content: UserContentList{TextContent{Text: "done"}}},
		// An empty assistant message is skipped.
		&AssistantMessage{Provider: model.Provider, API: model.API, Model: model.ID},
	}}, model, CacheRetentionNone, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	assistant, _ := messages[0].(map[string]any)
	blocks, _ := assistant["content"].([]any)
	if assistant["role"] != "assistant" || len(blocks) != 4 {
		t.Fatalf("assistant = %#v", assistant)
	}
	// The blank thinking block is dropped: text, signed thinking, redacted
	// reasoning, tool call.
	thinkingBlock, _ := blocks[1].(map[string]any)
	reasoningContent, _ := thinkingBlock["reasoningContent"].(map[string]any)
	reasoningText, _ := reasoningContent["reasoningText"].(map[string]any)
	if reasoningText["text"] != "reasoning" || reasoningText["signature"] != signature {
		t.Fatalf("thinking = %#v", thinkingBlock)
	}
	redactedBlock, _ := blocks[2].(map[string]any)
	redactedContent, _ := redactedBlock["reasoningContent"].(map[string]any)
	if string(redactedContent["redactedContent"].([]byte)) != "sig-bytes" {
		t.Fatalf("redacted = %#v", redactedBlock)
	}
	toolUseBlock, _ := blocks[3].(map[string]any)
	toolUse, _ := toolUseBlock["toolUse"].(map[string]any)
	if toolUse["toolUseId"] != "call-1" || toolUse["name"] != "read" {
		t.Fatalf("toolUse = %#v", toolUse)
	}

	// An assistant message from a different model has its thinking lowered to
	// text and its redacted blocks dropped (the shared transcript transform),
	// and an orphaned tool call gains a synthetic error result.
	foreign := &AssistantMessage{
		Provider: "anthropic", API: APIAnthropicMessages, Model: "claude-other",
		Content: ContentList{
			ThinkingContent{Thinking: "foreign reasoning", ThinkingSignature: &signature},
			ThinkingContent{Thinking: "[Reasoning redacted]", Redacted: true, ThinkingSignature: &signature},
			ToolCall{ID: "call-foreign", Name: "read", Arguments: json.RawMessage(`{}`)},
		},
	}
	messages, err = ConvertBedrockMessages(TranscriptContext{Messages: []Message{foreign}}, model, CacheRetentionNone, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %#v", messages)
	}
	assistant, _ = messages[0].(map[string]any)
	blocks, _ = assistant["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("blocks = %#v", blocks)
	}
	if block, _ := blocks[0].(map[string]any); block["text"] != "foreign reasoning" {
		t.Fatalf("blocks = %#v", blocks)
	}
	synthetic, _ := messages[1].(map[string]any)
	toolResults, _ := synthetic["content"].([]any)
	syntheticResult, _ := toolResults[0].(map[string]any)["toolResult"].(map[string]any)
	if synthetic["role"] != "user" || syntheticResult["status"] != "error" {
		t.Fatalf("synthetic = %#v", synthetic)
	}

	// A non-Claude model omits the signature.
	nova := &Model{ID: "amazon.nova-pro-v1:0", Name: "Nova Pro"}
	messages, err = ConvertBedrockMessages(TranscriptContext{Messages: []Message{
		&AssistantMessage{Content: ContentList{ThinkingContent{Thinking: "reasoning", ThinkingSignature: &signature}}},
	}}, nova, CacheRetentionNone, nil)
	if err != nil {
		t.Fatal(err)
	}
	assistant, _ = messages[0].(map[string]any)
	blocks, _ = assistant["content"].([]any)
	thinkingBlock, _ = blocks[0].(map[string]any)
	reasoningContent, _ = thinkingBlock["reasoningContent"].(map[string]any)
	reasoningText, _ = reasoningContent["reasoningText"].(map[string]any)
	if _, hasSignature := reasoningText["signature"]; hasSignature {
		t.Fatalf("non-Claude models must omit the signature: %#v", reasoningText)
	}

	// A missing signature falls back to a plain text block for Claude.
	messages, err = ConvertBedrockMessages(TranscriptContext{Messages: []Message{
		&AssistantMessage{Content: ContentList{ThinkingContent{Thinking: "reasoning"}}},
	}}, model, CacheRetentionNone, nil)
	if err != nil {
		t.Fatal(err)
	}
	assistant, _ = messages[0].(map[string]any)
	blocks, _ = assistant["content"].([]any)
	if block, _ := blocks[0].(map[string]any); block["text"] != "reasoning" {
		t.Fatalf("blocks = %#v", blocks)
	}

	// Consecutive tool results collapse into one user message.
	messages, err = ConvertBedrockMessages(TranscriptContext{Messages: []Message{
		&ToolResultMessage{ToolCallID: "c1", ToolName: "read", Content: UserContentList{TextContent{Text: "one"}}},
		&ToolResultMessage{ToolCallID: "c2", ToolName: "read", Content: UserContentList{TextContent{Text: "two"}}, IsError: true},
		&ToolResultMessage{ToolCallID: "c3", ToolName: "read", Content: UserContentList{}},
	}}, model, CacheRetentionNone, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %#v", messages)
	}
	combined, _ := messages[0].(map[string]any)
	combinedResults, _ := combined["content"].([]any)
	if combined["role"] != "user" || len(combinedResults) != 3 {
		t.Fatalf("combined = %#v", combined)
	}
	firstResult, _ := combinedResults[0].(map[string]any)["toolResult"].(map[string]any)
	if firstResult["toolUseId"] != "c1" || firstResult["status"] != "success" {
		t.Fatalf("first = %#v", firstResult)
	}
	secondResult, _ := combinedResults[1].(map[string]any)["toolResult"].(map[string]any)
	if secondResult["status"] != "error" {
		t.Fatalf("second = %#v", secondResult)
	}
	// Empty tool content becomes the placeholder.
	thirdRaw, _ := combinedResults[2].(map[string]any)
	thirdResult, _ := thirdRaw["toolResult"].(map[string]any)
	emptyToolContent, _ := thirdResult["content"].([]any)
	if block, _ := emptyToolContent[0].(map[string]any); block["text"] != "<empty>" {
		t.Fatalf("third = %#v", thirdResult)
	}
}

func TestBedrockCachePoints(t *testing.T) {
	model := bedrockTestModel("")
	// A cache point is appended to the system prompt and the last user message.
	system := BuildBedrockSystemPrompt("be helpful", model, CacheRetentionShort, nil)
	if len(system) != 2 {
		t.Fatalf("system = %#v", system)
	}
	if point, _ := system[1].(map[string]any)["cachePoint"].(map[string]any); point["type"] != "default" {
		t.Fatalf("system = %#v", system)
	}
	// Long retention adds the 1h TTL.
	system = BuildBedrockSystemPrompt("be helpful", model, CacheRetentionLong, nil)
	point, _ := system[1].(map[string]any)["cachePoint"].(map[string]any)
	if point["ttl"] != "1h" {
		t.Fatalf("system = %#v", system)
	}
	// Retention "none" adds nothing, and no prompt means no blocks.
	if system := BuildBedrockSystemPrompt("be helpful", model, CacheRetentionNone, nil); len(system) != 1 {
		t.Fatalf("system = %#v", system)
	}
	if system := BuildBedrockSystemPrompt("", model, CacheRetentionShort, nil); system != nil {
		t.Fatalf("system = %#v", system)
	}

	messages, err := ConvertBedrockMessages(TranscriptContext{Messages: []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1},
	}}, model, CacheRetentionShort, nil)
	if err != nil {
		t.Fatal(err)
	}
	last, _ := messages[len(messages)-1].(map[string]any)
	content, _ := last["content"].([]any)
	if _, ok := content[len(content)-1].(map[string]any)["cachePoint"]; !ok {
		t.Fatalf("content = %#v", content)
	}
}

func TestBedrockToolConfigConversion(t *testing.T) {
	tool := Tool{Name: "read", Description: "Read a file", Parameters: json.RawMessage(`{"type":"object"}`)}
	// toolChoice "none" disables the config entirely, as does an empty tool list.
	if config := ConvertBedrockToolConfig([]Tool{tool}, "none", "", false); config != nil {
		t.Fatalf("config = %#v", config)
	}
	if config := ConvertBedrockToolConfig(nil, "auto", "", false); config != nil {
		t.Fatalf("config = %#v", config)
	}
	config := ConvertBedrockToolConfig([]Tool{tool}, "auto", "", false)
	tools, _ := config["tools"].([]any)
	spec, _ := tools[0].(map[string]any)["toolSpec"].(map[string]any)
	if spec["name"] != "read" || spec["inputSchema"] == nil {
		t.Fatalf("spec = %#v", spec)
	}
	choice, _ := config["toolChoice"].(map[string]any)
	if _, ok := choice["auto"]; !ok {
		t.Fatalf("choice = %#v", choice)
	}
	// A forced tool choice names the tool.
	config = ConvertBedrockToolConfig([]Tool{tool}, "tool", "read", false)
	choice, _ = config["toolChoice"].(map[string]any)
	forced, _ := choice["tool"].(map[string]any)
	if forced["name"] != "read" {
		t.Fatalf("choice = %#v", config["toolChoice"])
	}
	// "any" and no choice at all.
	config = ConvertBedrockToolConfig([]Tool{tool}, "any", "", false)
	choice, _ = config["toolChoice"].(map[string]any)
	if _, ok := choice["any"]; !ok {
		t.Fatalf("choice = %#v", choice)
	}
	config = ConvertBedrockToolConfig([]Tool{tool}, "", "", false)
	if config["toolChoice"] != nil {
		t.Fatalf("config = %#v", config)
	}
}

// bedrockTestServer streams encoded event frames.
func bedrockTestServer(t *testing.T, frames [][]byte, capture *http.Request, body *map[string]any) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if capture != nil {
			*capture = *request
		}
		if body != nil {
			raw, _ := io.ReadAll(request.Body)
			_ = json.Unmarshal(raw, body)
		}
		writer.Header().Set("x-amzn-requestid", "req-1")
		writer.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		writer.WriteHeader(http.StatusOK)
		for _, frame := range frames {
			_, _ = writer.Write(frame)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func bedrockEventFrame(eventType string, payload any) []byte {
	encoded, _ := json.Marshal(payload)
	return buildAWSEventStreamMessageFor(nil, map[string]any{
		":message-type": "event",
		":event-type":   eventType,
		":content-type": "application/json",
	}, encoded)
}

func TestBedrockStreamFold(t *testing.T) {
	frames := [][]byte{
		bedrockEventFrame("messageStart", map[string]any{"role": "assistant"}),
		bedrockEventFrame("contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"text": "Hel"}}),
		bedrockEventFrame("contentBlockDelta", map[string]any{"contentBlockIndex": 0, "delta": map[string]any{"text": "lo"}}),
		bedrockEventFrame("contentBlockStop", map[string]any{"contentBlockIndex": 0}),
		bedrockEventFrame("contentBlockStart", map[string]any{"contentBlockIndex": 1, "start": map[string]any{"toolUse": map[string]any{"toolUseId": "call-1", "name": "read"}}}),
		bedrockEventFrame("contentBlockDelta", map[string]any{"contentBlockIndex": 1, "delta": map[string]any{"toolUse": map[string]any{"input": `{"pa`}}}),
		bedrockEventFrame("contentBlockDelta", map[string]any{"contentBlockIndex": 1, "delta": map[string]any{"toolUse": map[string]any{"input": `th":"a"}`}}}),
		bedrockEventFrame("contentBlockStop", map[string]any{"contentBlockIndex": 1}),
		bedrockEventFrame("contentBlockDelta", map[string]any{"contentBlockIndex": 2, "delta": map[string]any{"reasoningContent": map[string]any{"text": "think"}}}),
		bedrockEventFrame("contentBlockDelta", map[string]any{"contentBlockIndex": 2, "delta": map[string]any{"reasoningContent": map[string]any{"signature": "sig"}}}),
		bedrockEventFrame("contentBlockStop", map[string]any{"contentBlockIndex": 2}),
		bedrockEventFrame("messageStop", map[string]any{"stopReason": "tool_use"}),
		bedrockEventFrame("metadata", map[string]any{"usage": map[string]any{
			"inputTokens": 100, "outputTokens": 20, "cacheReadInputTokens": 30, "cacheWriteInputTokens": 10,
			"totalTokens": 160,
			"cacheDetails": []any{
				map[string]any{"ttl": "1h", "inputTokens": 4},
				map[string]any{"ttl": "5m", "inputTokens": 6},
			},
		}}),
	}
	var captured http.Request
	var payload map[string]any
	server := bedrockTestServer(t, frames, &captured, &payload)

	model := bedrockTestModel(server.URL)
	stream := StreamBedrockConverse(model, TranscriptContext{Messages: []Message{
		&SystemMessage{Content: StringOrBlocks{Text: "be helpful"}},
		&UserMessage{Content: StringOrBlocks{Text: "hello"}, Timestamp: 1},
	}}, &BedrockOptions{
		StreamOptions: StreamOptions{APIKey: "bearer-token", Env: ProviderEnv{
			"AWS_ACCESS_KEY_ID": "AKID", "AWS_SECRET_ACCESS_KEY": "SECRET",
		}},
		BearerToken: "bearer-token",
		Reasoning:   ThinkHigh,
	})
	message, err := stream.Result(context.Background())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if message.StopReason != StopToolUse || message.RawStopReason == nil || *message.RawStopReason != "tool_use" {
		t.Fatalf("message = %+v", message)
	}
	if len(message.Content) != 3 {
		t.Fatalf("content = %+v", message.Content)
	}
	if text, ok := message.Content[0].(TextContent); !ok || text.Text != "Hello" {
		t.Fatalf("text = %+v", message.Content[0])
	}
	call, ok := message.Content[1].(ToolCall)
	if !ok || call.ID != "call-1" || call.Name != "read" || string(call.Arguments) != `{"path":"a"}` {
		t.Fatalf("tool call = %+v", message.Content[1])
	}
	thinking, ok := message.Content[2].(ThinkingContent)
	if !ok || thinking.Thinking != "think" || thinking.ThinkingSignature == nil || *thinking.ThinkingSignature != "sig" {
		t.Fatalf("thinking = %+v", message.Content[2])
	}
	// Usage includes the one-hour cache write split.
	if message.Usage.Input != 100 || message.Usage.Output != 20 || message.Usage.CacheRead != 30 ||
		message.Usage.CacheWrite != 10 || message.Usage.TotalTokens != 160 {
		t.Fatalf("usage = %+v", message.Usage)
	}
	if message.Usage.CacheWrite1h == nil || *message.Usage.CacheWrite1h != 4 {
		t.Fatalf("cacheWrite1h = %v", message.Usage.CacheWrite1h)
	}
	if message.Usage.Cost.Input != 3*100.0/1e6*1e6/1e6*100 {
		// Cost is computed from the model rates; only assert it is non-zero and
		// additive.
	}
	if message.Usage.Cost.Total != message.Usage.Cost.Input+message.Usage.Cost.Output+
		message.Usage.Cost.CacheRead+message.Usage.Cost.CacheWrite {
		t.Fatalf("cost = %+v", message.Usage.Cost)
	}

	// Bearer auth is used instead of SigV4 and the request shape is right.
	if captured.Header.Get("Authorization") != "Bearer bearer-token" {
		t.Fatalf("authorization = %q", captured.Header.Get("Authorization"))
	}
	if !strings.HasSuffix(captured.URL.Path, "/model/"+model.ID+"/converse-stream") {
		t.Fatalf("path = %s", captured.URL.Path)
	}
	if payload["modelId"] != model.ID {
		t.Fatalf("payload = %#v", payload)
	}
	system, ok := payload["system"].([]any)
	if !ok || len(system) == 0 {
		t.Fatalf("payload = %#v", payload["system"])
	}
	// The Claude model gets a cache point on the system prompt.
	if point, _ := system[len(system)-1].(map[string]any)["cachePoint"].(map[string]any); point["type"] != "default" {
		t.Fatalf("system = %#v", system)
	}
	additional, _ := payload["additionalModelRequestFields"].(map[string]any)
	if additional["thinking"] == nil {
		t.Fatalf("payload = %#v", payload["additionalModelRequestFields"])
	}
}

func TestBedrockStreamSigV4AndErrors(t *testing.T) {
	frames := [][]byte{bedrockEventFrame("messageStart", map[string]any{"role": "assistant"})}
	var captured http.Request
	server := bedrockTestServer(t, frames, &captured, nil)

	model := bedrockTestModel(server.URL)
	stream := StreamBedrockConverse(model, TranscriptContext{Messages: []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1},
	}}, &BedrockOptions{StreamOptions: StreamOptions{Env: ProviderEnv{
		"AWS_ACCESS_KEY_ID": "AKID", "AWS_SECRET_ACCESS_KEY": "SECRET", "AWS_REGION": "us-west-2",
	}}})
	message, err := stream.Result(context.Background())
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	// The stream ended without a stop reason after the start event.
	if message.StopReason != StopError || message.ErrorMessage == nil ||
		!strings.Contains(*message.ErrorMessage, "without a stop reason") {
		t.Fatalf("message = %+v", message)
	}
	// SigV4 signed the request with the configured region.
	authorization := captured.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "AWS4-HMAC-SHA256 Credential=AKID/") ||
		!strings.Contains(authorization, "/us-west-2/bedrock/aws4_request") {
		t.Fatalf("authorization = %q", authorization)
	}

	// A modeled exception event maps through the error prefixes and records the
	// failure diagnostic.
	exceptionFrames := [][]byte{
		bedrockEventFrame("messageStart", map[string]any{"role": "assistant"}),
		buildAWSEventStreamMessage(nil, map[string]any{
			":message-type":   "exception",
			":exception-type": "ThrottlingException",
			":content-type":   "application/json",
		}, []byte(`{"message":"rate exceeded"}`)),
	}
	exceptionServer := bedrockTestServer(t, exceptionFrames, nil, nil)
	stream = StreamBedrockConverse(bedrockTestModel(exceptionServer.URL), TranscriptContext{Messages: []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1},
	}}, &BedrockOptions{BearerToken: "token"})
	message, _ = stream.Result(context.Background())
	if message.StopReason != StopError || message.ErrorMessage == nil ||
		!strings.Contains(*message.ErrorMessage, "Throttling error: rate exceeded") {
		t.Fatalf("message = %+v", message)
	}
	if len(message.Diagnostics) != 1 || message.Diagnostics[0].Type != "bedrock_response_failure" {
		t.Fatalf("diagnostics = %+v", message.Diagnostics)
	}
	details := string(message.Diagnostics[0].Details)
	if !strings.Contains(details, `"errorCode":"ThrottlingException"`) ||
		!strings.Contains(details, `"requestId":"req-1"`) {
		t.Fatalf("details = %s", details)
	}

	// An HTTP failure is surfaced with its status and body.
	failing := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("x-amzn-requestid", "req-2")
		writer.WriteHeader(http.StatusForbidden)
		_, _ = writer.Write([]byte(`{"message":"no access"}`))
	}))
	defer failing.Close()
	stream = StreamBedrockConverse(bedrockTestModel(failing.URL), TranscriptContext{Messages: []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1},
	}}, &BedrockOptions{BearerToken: "token"})
	message, _ = stream.Result(context.Background())
	if message.StopReason != StopError || message.ErrorMessage == nil ||
		!strings.Contains(*message.ErrorMessage, "403") || !strings.Contains(*message.ErrorMessage, "no access") {
		t.Fatalf("message = %+v", message)
	}
}

func TestBedrockStreamRedactedReasoning(t *testing.T) {
	// Encrypted reasoning arrives as opaque bytes and is replayed as
	// redactedContent with a base64 signature.
	redacted := []byte{1, 2, 3}
	frames := [][]byte{
		bedrockEventFrame("messageStart", map[string]any{"role": "assistant"}),
		bedrockEventFrame("contentBlockDelta", map[string]any{"contentBlockIndex": 0,
			"delta": map[string]any{"reasoningContent": map[string]any{"redactedContent": base64.StdEncoding.EncodeToString(redacted)}}}),
		bedrockEventFrame("contentBlockStop", map[string]any{"contentBlockIndex": 0}),
		bedrockEventFrame("messageStop", map[string]any{"stopReason": "end_turn"}),
	}
	server := bedrockTestServer(t, frames, nil, nil)
	stream := StreamBedrockConverse(bedrockTestModel(server.URL), TranscriptContext{Messages: []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1},
	}}, &BedrockOptions{BearerToken: "token"})
	message, _ := stream.Result(context.Background())
	if message.StopReason != StopStop || len(message.Content) != 1 {
		t.Fatalf("message = %+v", message)
	}
	thinking, ok := message.Content[0].(ThinkingContent)
	if !ok || !thinking.Redacted || thinking.Thinking != "[Reasoning redacted]" {
		t.Fatalf("thinking = %+v", message.Content[0])
	}
	if thinking.ThinkingSignature == nil || *thinking.ThinkingSignature != base64.StdEncoding.EncodeToString(redacted) {
		t.Fatalf("signature = %v", thinking.ThinkingSignature)
	}

	// The stored signature replays as redactedContent, and a non-base64
	// signature is dropped rather than failing the request.
	body := map[string]any{}
	replayServer := bedrockTestServer(t, nil, nil, &body)
	replayModel := bedrockTestModel(replayServer.URL)
	stream = StreamBedrockConverse(replayModel, TranscriptContext{Messages: []Message{
		&AssistantMessage{
			Provider: replayModel.Provider, API: replayModel.API, Model: replayModel.ID,
			Content: ContentList{ThinkingContent{
				Thinking: "[Reasoning redacted]", Redacted: true,
				ThinkingSignature: strPtrForBedrock(base64.StdEncoding.EncodeToString(redacted)),
			}},
		},
	}}, &BedrockOptions{BearerToken: "token"})
	_, _ = stream.Result(context.Background())
	messages, _ := body["messages"].([]any)
	first, _ := messages[0].(map[string]any)
	blocks, _ := first["content"].([]any)
	block, _ := blocks[0].(map[string]any)
	reasoningContent, _ := block["reasoningContent"].(map[string]any)
	// Go marshals []byte as base64, which is the AWS blob encoding.
	encodedRedacted, _ := reasoningContent["redactedContent"].(string)
	if encodedRedacted != base64.StdEncoding.EncodeToString(redacted) {
		t.Fatalf("payload = %#v", body)
	}
}

func strPtrForBedrock(value string) *string { return &value }

func TestBedrockMessageStartValidation(t *testing.T) {
	frames := [][]byte{bedrockEventFrame("messageStart", map[string]any{"role": "user"})}
	server := bedrockTestServer(t, frames, nil, nil)
	stream := StreamBedrockConverse(bedrockTestModel(server.URL), TranscriptContext{Messages: []Message{
		&UserMessage{Content: StringOrBlocks{Text: "hi"}, Timestamp: 1},
	}}, &BedrockOptions{BearerToken: "token"})
	message, _ := stream.Result(context.Background())
	if message.StopReason != StopError || message.ErrorMessage == nil ||
		!strings.Contains(*message.ErrorMessage, "Unexpected assistant message start") {
		t.Fatalf("message = %+v", message)
	}
}

func TestBedrockProviderFactory(t *testing.T) {
	provider := AmazonBedrockProvider()
	if provider.ID != "amazon-bedrock" || provider.Name != "Amazon Bedrock" {
		t.Fatalf("provider = %+v", provider)
	}
	if provider.Auth.APIKey == nil || provider.Auth.APIKey.Name != "AWS credentials or bearer token" {
		t.Fatalf("auth = %+v", provider.Auth)
	}
	if len(provider.GetModels()) == 0 {
		t.Fatal("no catalog models")
	}
	// The auth resolver detects ambient credentials.
	result, err := provider.Auth.APIKey.Resolve(AuthResolveInput{
		Ctx: staticAuthContext{"AWS_ACCESS_KEY_ID": "AKID", "AWS_SECRET_ACCESS_KEY": "SECRET"},
	})
	if err != nil || result == nil || result.Source != "AWS access keys" {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	result, err = provider.Auth.APIKey.Resolve(AuthResolveInput{Ctx: staticAuthContext{}})
	if err != nil || result != nil {
		t.Fatalf("result = %+v err = %v", result, err)
	}
}

// staticAuthContext is an injected auth context.
type staticAuthContext map[string]string

func (c staticAuthContext) Env(name string) (string, bool) {
	value, ok := c[name]
	return value, ok && value != ""
}

func (c staticAuthContext) FileExists(string) bool { return false }

var _ = sha256.Sum256
var _ = hex.EncodeToString
