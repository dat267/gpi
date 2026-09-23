package ai

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Port of api/bedrock-converse-stream.ts: the Bedrock Converse streaming
// adapter, built on the AWS plumbing in aws_sigv4.go, aws_bedrock.go, and
// aws_eventstream.go.

// Placeholders upstream uses for empty content.
const (
	bedrockEmptyTextPlaceholder        = "<empty>"
	bedrockRedactedThinkingPlaceholder = "[Reasoning redacted]"
)

// bedrockBlock is one streaming content block with its scratch state
// (upstream's `Block` intersection).
type bedrockBlock struct {
	kind              string // "text" | "thinking" | "toolCall"
	contentIndex      int
	text              string
	thinking          string
	thinkingSignature string
	redacted          bool
	redactedChunks    [][]byte
	toolCall          ToolCall
	partialJSON       string
}

// content renders the block for the assistant message.
func (b *bedrockBlock) content() Content {
	switch b.kind {
	case "text":
		return TextContent{Text: b.text}
	case "thinking":
		signature := b.thinkingSignature
		var signaturePtr *string
		if signature != "" {
			signaturePtr = &signature
		}
		return ThinkingContent{Thinking: b.thinking, ThinkingSignature: signaturePtr, Redacted: b.redacted}
	default:
		call := b.toolCall
		return call
	}
}

// flushRedactedContent encodes buffered encrypted reasoning into the signature.
func (b *bedrockBlock) flushRedactedContent() {
	if b.kind != "thinking" || b.redactedChunks == nil {
		return
	}
	b.thinkingSignature = bedrockBytesToBase64(b.redactedChunks)
	b.redactedChunks = nil
}

// finalize clears the streaming scratch state.
func (b *bedrockBlock) finalize() {
	b.partialJSON = ""
	b.flushRedactedContent()
}

func bedrockBytesToBase64(chunks [][]byte) string {
	var joined []byte
	for _, chunk := range chunks {
		joined = append(joined, chunk...)
	}
	return base64.StdEncoding.EncodeToString(joined)
}

// syncBedrockContent mirrors the block slice into the message content.
func syncBedrockContent(output *AssistantMessage, blocks []*bedrockBlock) {
	content := make([]Content, 0, len(blocks))
	for _, block := range blocks {
		content = append(content, block.content())
	}
	output.Content = content
}

// GetModelMatchCandidates returns the lowercase match candidates for a model
// id/name (upstream getModelMatchCandidates).
func GetModelMatchCandidates(modelID, modelName string) []string {
	values := []string{modelID}
	if modelName != "" {
		values = append(values, modelName)
	}
	var candidates []string
	for _, value := range values {
		lower := strings.ToLower(value)
		candidates = append(candidates, lower, bedrockNormalizeCandidate(lower))
	}
	return candidates
}

var bedrockCandidateSeparators = regexp.MustCompile(`[\s_.:]+`)

func bedrockNormalizeCandidate(value string) string {
	return bedrockCandidateSeparators.ReplaceAllString(value, "-")
}

// SupportsAdaptiveThinking reports whether a Bedrock model supports adaptive
// thinking (Opus 4.6+, Sonnet 4.6, Claude 5).
func SupportsAdaptiveThinking(modelID, modelName string) bool {
	for _, candidate := range GetModelMatchCandidates(modelID, modelName) {
		for _, needle := range []string{"opus-4-6", "opus-4-7", "opus-4-8", "opus-5", "sonnet-4-6", "sonnet-5", "fable-5"} {
			if strings.Contains(candidate, needle) {
				return true
			}
		}
	}
	return false
}

// SupportsNativeXhighEffort reports native xhigh support.
func SupportsNativeXhighEffort(model *Model) bool {
	for _, candidate := range GetModelMatchCandidates(model.ID, model.Name) {
		for _, needle := range []string{"opus-4-7", "opus-4-8", "opus-5", "sonnet-5", "fable-5"} {
			if strings.Contains(candidate, needle) {
				return true
			}
		}
	}
	return false
}

// MapThinkingLevelToBedrockEffort maps a level to Bedrock's effort control.
func MapThinkingLevelToBedrockEffort(model *Model, level ThinkingLevel) string {
	if level == ThinkXHigh && SupportsNativeXhighEffort(model) {
		return "xhigh"
	}
	if mapped, ok := model.ThinkingLevelMap[level]; ok && mapped != nil && *mapped != "" {
		return *mapped
	}
	switch level {
	case ThinkMinimal, ThinkLow:
		return "low"
	case ThinkMedium:
		return "medium"
	default:
		return "high"
	}
}

// ResolveBedrockCacheRetention resolves cache retention, defaulting to short.
func ResolveBedrockCacheRetention(cacheRetention CacheRetention, env ProviderEnv) CacheRetention {
	if cacheRetention != "" {
		return cacheRetention
	}
	if GetProviderEnvValueOr("PI_CACHE_RETENTION", env) == "long" {
		return CacheRetentionLong
	}
	return CacheRetentionShort
}

// IsAnthropicClaudeModel reports whether a Bedrock model is an Anthropic Claude
// model (checking both id and name for inference-profile ARNs).
func IsAnthropicClaudeModel(model *Model) bool {
	id := strings.ToLower(model.ID)
	name := strings.ToLower(model.Name)
	for _, value := range []string{id, name} {
		if strings.Contains(value, "anthropic.claude") || strings.Contains(value, "anthropic/claude") ||
			strings.Contains(value, "claude") {
			return true
		}
	}
	return false
}

// SupportsBedrockPromptCaching reports whether cache points are supported.
func SupportsBedrockPromptCaching(model *Model, env ProviderEnv) bool {
	candidates := GetModelMatchCandidates(model.ID, model.Name)
	hasClaudeRef := false
	for _, candidate := range candidates {
		if strings.Contains(candidate, "claude") {
			hasClaudeRef = true
			break
		}
	}
	if !hasClaudeRef {
		// Inference-profile ARNs carry no model name; allow an explicit override.
		return GetProviderEnvValueOr("AWS_BEDROCK_FORCE_CACHE", env) == "1"
	}
	for _, candidate := range candidates {
		// Claude 5.
		if strings.Contains(candidate, "fable-5") || strings.Contains(candidate, "opus-5") ||
			strings.Contains(candidate, "sonnet-5") {
			return true
		}
		// Claude 4.x.
		if strings.Contains(candidate, "-4-") {
			return true
		}
		if strings.Contains(candidate, "claude-3-7-sonnet") || strings.Contains(candidate, "claude-3-5-haiku") {
			return true
		}
	}
	return false
}

// SupportsBedrockThinkingSignature reports whether reasoning signatures are
// accepted (Anthropic Claude only).
func SupportsBedrockThinkingSignature(model *Model) bool { return IsAnthropicClaudeModel(model) }

// BuildBedrockSystemPrompt renders the system content blocks with an optional
// cache point (upstream buildSystemPrompt).
func BuildBedrockSystemPrompt(systemPrompt string, model *Model, cacheRetention CacheRetention, env ProviderEnv) []any {
	if systemPrompt == "" {
		return nil
	}
	blocks := []any{map[string]any{"text": SanitizeSurrogates(systemPrompt)}}
	if cacheRetention != CacheRetentionNone && SupportsBedrockPromptCaching(model, env) {
		cachePoint := map[string]any{"type": "default"}
		if cacheRetention == CacheRetentionLong {
			cachePoint["ttl"] = "1h"
		}
		blocks = append(blocks, map[string]any{"cachePoint": cachePoint})
	}
	return blocks
}

var bedrockToolCallIDPattern = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// NormalizeBedrockToolCallID sanitizes and truncates a tool-call id.
func NormalizeBedrockToolCallID(id string) string {
	sanitized := bedrockToolCallIDPattern.ReplaceAllString(id, "_")
	if len(sanitized) > 64 {
		sanitized = sanitized[:64]
	}
	return sanitized
}

// bedrockNonBlankTextBlock renders a text block, or nil when blank.
func bedrockNonBlankTextBlock(text string) map[string]any {
	sanitized := SanitizeSurrogates(text)
	if strings.TrimSpace(sanitized) == "" {
		return nil
	}
	return map[string]any{"text": sanitized}
}

func bedrockRequiredTextBlock(text string) map[string]any {
	if block := bedrockNonBlankTextBlock(text); block != nil {
		return block
	}
	return map[string]any{"text": bedrockEmptyTextPlaceholder}
}

// SanitizeBedrockDocument strips empty keys from a document value.
func SanitizeBedrockDocument(value any) any {
	switch typed := value.(type) {
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, SanitizeBedrockDocument(item))
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for key, nested := range typed {
			if key == "" {
				continue
			}
			out[key] = SanitizeBedrockDocument(nested)
		}
		return out
	default:
		return value
	}
}

// createBedrockImageBlock renders an image block.
func createBedrockImageBlock(mimeType, data string) (map[string]any, error) {
	var format string
	switch mimeType {
	case "image/jpeg", "image/jpg":
		format = "jpeg"
	case "image/png":
		format = "png"
	case "image/gif":
		format = "gif"
	case "image/webp":
		format = "webp"
	default:
		return nil, fmt.Errorf("Unknown image type: %s", mimeType)
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, err
	}
	return map[string]any{"source": map[string]any{"bytes": decoded}, "format": format}, nil
}

// ConvertBedrockToolResultContent renders tool-result content blocks.
func ConvertBedrockToolResultContent(content UserContentList) ([]any, error) {
	var result []any
	for _, block := range content {
		switch typed := block.(type) {
		case ImageContent:
			image, err := createBedrockImageBlock(typed.MimeType, typed.Data)
			if err != nil {
				return nil, err
			}
			result = append(result, map[string]any{"image": image})
		case TextContent:
			if textBlock := bedrockNonBlankTextBlock(typed.Text); textBlock != nil {
				result = append(result, textBlock)
			}
		}
	}
	if len(result) == 0 {
		result = append(result, map[string]any{"text": bedrockEmptyTextPlaceholder})
	}
	return result, nil
}

// ConvertBedrockMessages converts a transcript into Converse messages
// (upstream convertMessages).
func ConvertBedrockMessages(context TranscriptContext, model *Model, cacheRetention CacheRetention, env ProviderEnv) ([]any, error) {
	messages := TransformMessages(WithoutInitialSystemMessage(context.Messages), model,
		func(id string, _ *Model, _ *AssistantMessage) string { return NormalizeBedrockToolCallID(id) })

	var result []any
	for index := 0; index < len(messages); index++ {
		message := messages[index]
		switch typed := message.(type) {
		case *UserMessage:
			var content []any
			if typed.Content.String() {
				content = append(content, bedrockRequiredTextBlock(typed.Content.Text))
			} else {
				for _, block := range typed.Content.Blocks {
					switch inner := block.(type) {
					case TextContent:
						if textBlock := bedrockNonBlankTextBlock(inner.Text); textBlock != nil {
							content = append(content, textBlock)
						}
					case ImageContent:
						image, err := createBedrockImageBlock(inner.MimeType, inner.Data)
						if err != nil {
							return nil, err
						}
						content = append(content, map[string]any{"image": image})
					}
				}
				if len(content) == 0 {
					content = append(content, map[string]any{"text": bedrockEmptyTextPlaceholder})
				}
			}
			result = append(result, map[string]any{"role": "user", "content": content})
		case *AssistantMessage:
			// Bedrock rejects empty content arrays.
			if len(typed.Content) == 0 {
				continue
			}
			var contentBlocks []any
			for _, block := range typed.Content {
				switch inner := block.(type) {
				case TextContent:
					if textBlock := bedrockNonBlankTextBlock(inner.Text); textBlock != nil {
						contentBlocks = append(contentBlocks, textBlock)
					}
				case ToolCall:
					contentBlocks = append(contentBlocks, map[string]any{
						"toolUse": map[string]any{
							"toolUseId": inner.ID,
							"name":      inner.Name,
							"input":     SanitizeBedrockDocument(rawMessageValue(inner.Arguments)),
						},
					})
				case ThinkingContent:
					// Encrypted reasoning replays as redactedContent.
					if inner.Redacted {
						if redacted := decodeBedrockRedactedContent(inner.ThinkingSignature); redacted != nil {
							contentBlocks = append(contentBlocks, map[string]any{
								"reasoningContent": map[string]any{"redactedContent": redacted},
							})
						}
						continue
					}
					thinking := SanitizeSurrogates(inner.Thinking)
					if strings.TrimSpace(thinking) == "" {
						continue
					}
					if SupportsBedrockThinkingSignature(model) {
						// A missing signature cannot be replayed; fall back to text.
						if inner.ThinkingSignature == nil || strings.TrimSpace(*inner.ThinkingSignature) == "" {
							contentBlocks = append(contentBlocks, map[string]any{"text": thinking})
						} else {
							contentBlocks = append(contentBlocks, map[string]any{
								"reasoningContent": map[string]any{
									"reasoningText": map[string]any{"text": thinking, "signature": *inner.ThinkingSignature},
								},
							})
						}
					} else {
						contentBlocks = append(contentBlocks, map[string]any{
							"reasoningContent": map[string]any{"reasoningText": map[string]any{"text": thinking}},
						})
					}
				}
			}
			if len(contentBlocks) == 0 {
				continue
			}
			result = append(result, map[string]any{"role": "assistant", "content": contentBlocks})
		case *ToolResultMessage:
			toolResults := []any{bedrockToolResultBlock(typed)}
			// Bedrock requires consecutive tool results in one user message.
			next := index + 1
			for next < len(messages) {
				following, ok := messages[next].(*ToolResultMessage)
				if !ok {
					break
				}
				toolResults = append(toolResults, bedrockToolResultBlock(following))
				next++
			}
			index = next - 1
			result = append(result, map[string]any{"role": "user", "content": toolResults})
		}
	}

	// A cache point on the last user message when caching is enabled.
	if cacheRetention != CacheRetentionNone && SupportsBedrockPromptCaching(model, env) && len(result) > 0 {
		last, ok := result[len(result)-1].(map[string]any)
		if ok && last["role"] == "user" {
			if content, ok := last["content"].([]any); ok {
				cachePoint := map[string]any{"type": "default"}
				if cacheRetention == CacheRetentionLong {
					cachePoint["ttl"] = "1h"
				}
				last["content"] = append(content, map[string]any{"cachePoint": cachePoint})
			}
		}
	}
	return result, nil
}

// bedrockToolResultBlock renders one tool result.
func bedrockToolResultBlock(message *ToolResultMessage) map[string]any {
	content, err := ConvertBedrockToolResultContent(message.Content)
	if err != nil {
		content = []any{map[string]any{"text": bedrockEmptyTextPlaceholder}}
	}
	status := "success"
	if message.IsError {
		status = "error"
	}
	return map[string]any{
		"toolResult": map[string]any{
			"toolUseId": message.ToolCallID,
			"content":   content,
			"status":    status,
		},
	}
}

// rawMessageValue decodes a raw JSON value into a generic value.
func rawMessageValue(raw json.RawMessage) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return map[string]any{}
	}
	return value
}

// decodeBedrockRedactedContent decodes a stored redacted payload; a signature
// that is not base64 is dropped (upstream decodeRedactedContent).
func decodeBedrockRedactedContent(signature *string) []byte {
	if signature == nil || *signature == "" {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(*signature)
	if err != nil {
		return nil
	}
	return decoded
}

// ConvertBedrockToolConfig renders the tool configuration
// (upstream convertToolConfig).
func ConvertBedrockToolConfig(tools []Tool, toolChoice string, toolChoiceName string, supportsStrictMode bool) map[string]any {
	if len(tools) == 0 || toolChoice == "none" {
		return nil
	}
	bedrockTools := make([]any, 0, len(tools))
	for _, tool := range tools {
		strict, unset, err := ResolveJSONSchemaStrictSampling(tool, supportsStrictMode)
		if err != nil {
			strict = false
		}
		spec := map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"inputSchema": map[string]any{"json": SanitizeBedrockDocument(rawMessageValue(GetJSONSchemaToolParameters(tool, strict)))},
		}
		if !unset && strict {
			spec["strict"] = true
		}
		bedrockTools = append(bedrockTools, map[string]any{"toolSpec": spec})
	}

	var choice map[string]any
	switch toolChoice {
	case "auto":
		choice = map[string]any{"auto": map[string]any{}}
	case "any":
		choice = map[string]any{"any": map[string]any{}}
	case "":
	default:
		if toolChoice == "tool" && toolChoiceName != "" {
			choice = map[string]any{"tool": map[string]any{"name": toolChoiceName}}
		}
	}
	config := map[string]any{"tools": bedrockTools}
	if choice != nil {
		config["toolChoice"] = choice
	}
	return config
}

// BuildBedrockAdditionalModelRequestFields renders the thinking fields
// (upstream buildAdditionalModelRequestFields).
func BuildBedrockAdditionalModelRequestFields(model *Model, options *BedrockOptions) map[string]any {
	if options == nil || options.Reasoning == "" || !model.Reasoning {
		return nil
	}
	if !IsAnthropicClaudeModel(model) {
		return nil
	}
	// GovCloud rejects Claude's thinking.display field.
	display := options.ThinkingDisplay
	if display == "" {
		display = "summarized"
	}
	if IsGovCloudBedrockTarget(model, options) {
		display = ""
	}

	var result map[string]any
	if SupportsAdaptiveThinking(model.ID, model.Name) {
		thinking := map[string]any{"type": "adaptive"}
		if display != "" {
			thinking["display"] = display
		}
		result = map[string]any{
			"thinking":      thinking,
			"output_config": map[string]any{"effort": MapThinkingLevelToBedrockEffort(model, options.Reasoning)},
		}
	} else {
		defaultBudgets := map[ThinkingLevel]int{
			ThinkMinimal: 1024, ThinkLow: 2048, ThinkMedium: 8192,
			ThinkHigh: 16384, ThinkXHigh: 16384, ThinkMax: 16384,
		}
		// Custom budgets only cover token-based levels through high.
		level := options.Reasoning
		if level == ThinkXHigh || level == ThinkMax {
			level = ThinkHigh
		}
		budget := defaultBudgets[options.Reasoning]
		if options.ThinkingBudgets != nil {
			switch level {
			case ThinkMinimal:
				if options.ThinkingBudgets.Minimal != nil {
					budget = *options.ThinkingBudgets.Minimal
				}
			case ThinkLow:
				if options.ThinkingBudgets.Low != nil {
					budget = *options.ThinkingBudgets.Low
				}
			case ThinkMedium:
				if options.ThinkingBudgets.Medium != nil {
					budget = *options.ThinkingBudgets.Medium
				}
			case ThinkHigh:
				if options.ThinkingBudgets.High != nil {
					budget = *options.ThinkingBudgets.High
				}
			}
		}
		thinking := map[string]any{"type": "enabled", "budget_tokens": budget}
		if display != "" {
			thinking["display"] = display
		}
		result = map[string]any{"thinking": thinking}
	}

	interleaved := true
	if options.InterleavedThinking != nil {
		interleaved = *options.InterleavedThinking
	}
	if !SupportsAdaptiveThinking(model.ID, model.Name) && interleaved {
		result["anthropic_beta"] = []any{"interleaved-thinking-2025-05-14"}
	}
	return result
}

// bedrockRequestURL builds the ConverseStream endpoint.
func bedrockRequestURL(model *Model, region string, useExplicitEndpoint bool) string {
	base := strings.TrimRight(model.BaseURL, "/")
	if !useExplicitEndpoint || base == "" {
		base = fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com", region)
	}
	return base + "/model/" + url.PathEscape(model.ID) + "/converse-stream"
}

// StreamBedrockConverse streams a Bedrock Converse request (upstream stream).
func StreamBedrockConverse(model *Model, context TranscriptContext, options *BedrockOptions) *AssistantMessageEventStream {
	stream := NewAssistantMessageEventStream()
	// Bedrock has no mid-conversation system messages: fold them into the prompt.
	normalizedContext := CollapseSystemMessages(context)

	go func() {
		ctx := bgCtx()
		if options != nil && options.Ctx != nil {
			ctx = options.Ctx
		}
		if options == nil {
			options = &BedrockOptions{}
		}
		output := &AssistantMessage{
			API: APIBedrockConverse, Provider: model.Provider, Model: model.ID,
			Usage: Usage{Cost: UsageCost{}}, StopReason: StopPending,
			Timestamp: time.Now().UnixMilli(),
		}
		var blocks []*bedrockBlock
		finalizeAll := func() {
			for _, block := range blocks {
				block.finalize()
			}
			syncBedrockContent(output, blocks)
		}
		fail := func(err error, requestID string, status int, headers http.Header) {
			finalizeAll()
			if ctxErr(ctx) != nil {
				output.StopReason = StopAborted
			} else {
				output.StopReason = StopError
			}
			message := FormatBedrockError(withBedrockStatus(err, status, headers))
			output.ErrorMessage = &message
			if output.StopReason == StopError {
				AppendBedrockFailureDiagnostic(output, withBedrockStatus(err, status, headers), requestID)
			}
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: output.StopReason, Error: output})
			stream.End(&output)
		}

		// Region/endpoint resolution.
		optionsProfile := options.Profile
		if optionsProfile == "" && options.Env != nil {
			optionsProfile = options.Env["AWS_PROFILE"]
		}
		if optionsProfile == "" {
			optionsProfile = GetProviderEnvValueOr("AWS_PROFILE", options.Env)
		}
		configuredRegion := GetConfiguredBedrockRegion(options)
		hasAmbientConfiguredProfile := GetProviderEnvValueOr("AWS_PROFILE", options.Env) != ""
		endpointRegion := GetStandardBedrockEndpointRegion(model.BaseURL)
		useExplicitEndpoint := ShouldUseExplicitBedrockEndpoint(model.BaseURL, configuredRegion, hasAmbientConfiguredProfile)
		if !useExplicitEndpoint && GetProviderEnvValueOr("AWS_PROFILE", options.Env) == "" && optionsProfile == "" &&
			configuredRegion == "" {
			useExplicitEndpoint = ShouldUseExplicitBedrockEndpoint(model.BaseURL, configuredRegion, false)
		}
		region := ResolveBedrockRegion(model, options, configuredRegion, endpointRegion, useExplicitEndpoint, hasAmbientConfiguredProfile)

		bearerToken, useBearerToken, skipAuth := ResolveBedrockBearerToken(options)
		var credentials AWSCredentials
		if skipAuth {
			credentials = AWSCredentials{AccessKeyID: "dummy-access-key", SecretAccessKey: "dummy-secret-key"}
		} else if configured, ok := GetConfiguredBedrockCredentials(options.Env); ok && optionsProfile == "" {
			credentials = configured
		} else if optionsProfile != "" {
			if profileCredentials, ok := ResolveAWSProfileCredentials(optionsProfile, options.Env); ok {
				credentials = profileCredentials
			}
		}

		// Request construction.
		cacheRetention := ResolveBedrockCacheRetention(options.CacheRetention, options.Env)
		var inferenceMaxTokens *int
		if options.MaxTokens != nil {
			inferenceMaxTokens = options.MaxTokens
		} else if IsAnthropicClaudeModel(model) {
			maxTokens := int(model.MaxTokens)
			inferenceMaxTokens = &maxTokens
		}
		initialSystemMessage := GetInitialSystemMessage(normalizedContext.Messages)
		initialSystemPrompt := ""
		if initialSystemMessage != nil {
			initialSystemPrompt = GetSystemMessageText(initialSystemMessage)
		}
		messages, err := ConvertBedrockMessages(normalizedContext, model, cacheRetention, options.Env)
		if err != nil {
			fail(err, "", 0, nil)
			return
		}
		supportsStrictMode := false
		if model.Compat != nil && model.Compat.Bedrock != nil && model.Compat.Bedrock.SupportsStrictMode != nil {
			supportsStrictMode = *model.Compat.Bedrock.SupportsStrictMode
		}
		inferenceConfig := map[string]any{}
		if inferenceMaxTokens != nil {
			inferenceConfig["maxTokens"] = *inferenceMaxTokens
		}
		if options.Temperature != nil {
			inferenceConfig["temperature"] = *options.Temperature
		}
		commandInput := map[string]any{
			"modelId":  model.ID,
			"messages": messages,
		}
		if system := BuildBedrockSystemPrompt(initialSystemPrompt, model, cacheRetention, options.Env); system != nil {
			commandInput["system"] = system
		}
		if len(inferenceConfig) > 0 {
			commandInput["inferenceConfig"] = inferenceConfig
		}
		toolChoice := options.ToolChoice
		if toolChoice == "tool" && options.ToolChoiceName != "" {
			toolChoice = "tool"
		}
		if toolConfig := ConvertBedrockToolConfig(GetCurrentTools(normalizedContext.Messages), toolChoice, options.ToolChoiceName, supportsStrictMode); toolConfig != nil {
			commandInput["toolConfig"] = toolConfig
		}
		if additional := BuildBedrockAdditionalModelRequestFields(model, options); additional != nil {
			commandInput["additionalModelRequestFields"] = additional
		}
		if options.RequestMetadata != nil {
			commandInput["requestMetadata"] = options.RequestMetadata
		}
		if options.OnPayload != nil {
			if next := options.OnPayload(mustMarshalJSON(commandInput), model); next != nil {
				var replaced map[string]any
				if perr := json.Unmarshal(next, &replaced); perr == nil {
					commandInput = replaced
				}
			}
		}
		body, err := MarshalJSON(commandInput)
		if err != nil {
			fail(err, "", 0, nil)
			return
		}

		requestURL := bedrockRequestURL(model, region, useExplicitEndpoint)
		customHeaders := map[string]string{}
		for name, value := range options.Headers {
			if value == nil {
				continue
			}
			if !IsReservedBedrockHeader(name) {
				customHeaders[name] = *value
			}
		}

		request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
		if err != nil {
			fail(err, "", 0, nil)
			return
		}
		request.Header.Set("content-type", "application/json")
		for name, value := range customHeaders {
			request.Header.Set(name, value)
		}
		if useBearerToken {
			request.Header.Set("Authorization", "Bearer "+bearerToken)
		} else if !skipAuth || !credentials.IsZero() {
			if _, err := SignAWSRequest(request, body, credentials, "bedrock", region, time.Now()); err != nil {
				fail(err, "", 0, nil)
				return
			}
		}

		response, err := http.DefaultClient.Do(request)
		if err != nil {
			fail(err, "", 0, nil)
			return
		}
		defer response.Body.Close()
		requestID := response.Header.Get("x-amzn-requestid")
		if options.OnResponse != nil {
			headers := map[string]string{}
			for name, values := range response.Header {
				headers[strings.ToLower(name)] = strings.Join(values, ", ")
			}
			options.OnResponse(ProviderResponse{Status: response.StatusCode, Headers: headers}, model)
		}
		if response.StatusCode >= 400 {
			raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			fail(&ProviderError{
				Status: response.StatusCode, Headers: response.Header,
				Message: fmt.Sprintf("%d %s: %s", response.StatusCode, http.StatusText(response.StatusCode), string(raw)),
				Body:    string(raw),
			}, requestID, response.StatusCode, response.Header)
			return
		}

		stream.Push(AssistantMessageEvent{Type: EventStart, Partial: output})
		iterErr := readBedrockConverseStream(response.Body, model, output, stream, &blocks)
		if iterErr != nil {
			fail(iterErr, requestID, response.StatusCode, response.Header)
			return
		}
		if ctxErr(ctx) != nil {
			fail(fmt.Errorf("Request was aborted"), requestID, response.StatusCode, response.Header)
			return
		}
		if output.StopReason == StopPending {
			fail(fmt.Errorf("Bedrock stream ended without a stop reason"), requestID, response.StatusCode, response.Header)
			return
		}
		if output.StopReason == StopError || output.StopReason == StopAborted {
			message := "An unknown error occurred"
			if output.ErrorMessage != nil {
				message = *output.ErrorMessage
			}
			fail(fmt.Errorf("%s", message), requestID, response.StatusCode, response.Header)
			return
		}
		// A stream can settle without stopping every block.
		finalizeAll()
		stream.Push(AssistantMessageEvent{Type: EventDone, Reason: output.StopReason, Message: output})
		stream.End(&output)
	}()
	return stream
}

// withBedrockStatus attaches HTTP metadata to an error for formatting and
// diagnostics.
func withBedrockStatus(err error, status int, headers http.Header) error {
	providerErr, ok := err.(*ProviderError)
	if !ok {
		if status == 0 {
			return err
		}
		return &ProviderError{
			Status: status, Headers: headers, Message: err.Error(),
			Code: bedrockExceptionCode(err),
		}
	}
	if providerErr.Code == "" {
		providerErr.Code = bedrockExceptionCode(err)
	}
	return providerErr
}

// readBedrockConverseStream decodes and folds the event stream.
func readBedrockConverseStream(body io.Reader, model *Model, output *AssistantMessage, stream *AssistantMessageEventStream, blocks *[]*bedrockBlock) error {
	for {
		message, err := ReadAWSEventStreamMessage(body)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		messageType := awsHeaderValueString(message.Headers[":message-type"])
		eventType := awsHeaderValueString(message.Headers[":event-type"])
		exceptionType := awsHeaderValueString(message.Headers[":exception-type"])
		switch messageType {
		case "exception", "error":
			var payload struct {
				Message string `json:"message"`
			}
			_ = json.Unmarshal(message.Payload, &payload)
			messageText := payload.Message
			if messageText == "" {
				messageText = exceptionType
			}
			return &BedrockServiceError{Code: exceptionType, Message: messageText}
		}
		if err := applyBedrockConverseEvent(eventType, message.Payload, model, output, stream, blocks); err != nil {
			return err
		}
	}
}

func awsHeaderValueString(value any) string {
	text, _ := value.(string)
	return text
}

// applyBedrockConverseEvent folds one Converse stream event into the output.
func applyBedrockConverseEvent(eventType string, payload []byte, model *Model, output *AssistantMessage, stream *AssistantMessageEventStream, blocks *[]*bedrockBlock) error {
	switch eventType {
	case "messageStart":
		var event struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return err
		}
		if event.Role != "" && event.Role != "assistant" {
			return fmt.Errorf("Unexpected assistant message start but got user message start instead")
		}
		stream.Push(AssistantMessageEvent{Type: EventStart, Partial: output})
	case "contentBlockStart":
		var event struct {
			ContentBlockIndex int `json:"contentBlockIndex"`
			Start             *struct {
				ToolUse *struct {
					ToolUseID string `json:"toolUseId"`
					Name      string `json:"name"`
				} `json:"toolUse"`
			} `json:"start"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return err
		}
		if event.Start != nil && event.Start.ToolUse != nil {
			block := &bedrockBlock{
				kind: "toolCall", contentIndex: event.ContentBlockIndex,
				toolCall: ToolCall{
					ID: event.Start.ToolUse.ToolUseID, Name: event.Start.ToolUse.Name,
					Arguments: json.RawMessage(`{}`),
				},
			}
			*blocks = append(*blocks, block)
			syncBedrockContent(output, *blocks)
			stream.Push(AssistantMessageEvent{
				Type: EventToolcallStart, ContentIndex: len(*blocks) - 1, Partial: output,
			})
		}
	case "contentBlockDelta":
		var event struct {
			ContentBlockIndex int `json:"contentBlockIndex"`
			Delta             struct {
				Text    *string `json:"text"`
				ToolUse *struct {
					Input string `json:"input"`
				} `json:"toolUse"`
				ReasoningContent *struct {
					Text            *string `json:"text"`
					Signature       *string `json:"signature"`
					RedactedContent []byte  `json:"redactedContent"`
				} `json:"reasoningContent"`
			} `json:"delta"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return err
		}
		index := -1
		for position, block := range *blocks {
			if block.contentIndex == event.ContentBlockIndex {
				index = position
				break
			}
		}
		switch {
		case event.Delta.Text != nil:
			if index == -1 {
				// Bedrock sends no contentBlockStart for text blocks.
				*blocks = append(*blocks, &bedrockBlock{kind: "text", contentIndex: event.ContentBlockIndex})
				index = len(*blocks) - 1
				syncBedrockContent(output, *blocks)
				stream.Push(AssistantMessageEvent{Type: EventTextStart, ContentIndex: index, Partial: output})
			}
			block := (*blocks)[index]
			if block.kind == "text" {
				block.text += *event.Delta.Text
				syncBedrockContent(output, *blocks)
				stream.Push(AssistantMessageEvent{
					Type: EventTextDelta, ContentIndex: index, Delta: *event.Delta.Text, Partial: output,
				})
			}
		case event.Delta.ToolUse != nil && index != -1 && (*blocks)[index].kind == "toolCall":
			block := (*blocks)[index]
			block.partialJSON += event.Delta.ToolUse.Input
			block.toolCall.Arguments = parseStreamingArgs(json.RawMessage(block.partialJSON))
			syncBedrockContent(output, *blocks)
			stream.Push(AssistantMessageEvent{
				Type: EventToolcallDelta, ContentIndex: index, Delta: event.Delta.ToolUse.Input, Partial: output,
			})
		case event.Delta.ReasoningContent != nil:
			if index == -1 {
				*blocks = append(*blocks, &bedrockBlock{kind: "thinking", contentIndex: event.ContentBlockIndex})
				index = len(*blocks) - 1
				syncBedrockContent(output, *blocks)
				stream.Push(AssistantMessageEvent{Type: EventThinkingStart, ContentIndex: index, Partial: output})
			}
			block := (*blocks)[index]
			if block.kind == "thinking" {
				reasoning := event.Delta.ReasoningContent
				if reasoning.Text != nil && *reasoning.Text != "" {
					block.thinking += *reasoning.Text
					syncBedrockContent(output, *blocks)
					stream.Push(AssistantMessageEvent{
						Type: EventThinkingDelta, ContentIndex: index, Delta: *reasoning.Text, Partial: output,
					})
				}
				// The signature holds either an Anthropic signature or an opaque
				// redacted payload, never both.
				if reasoning.Signature != nil && !block.redacted {
					block.thinkingSignature += *reasoning.Signature
				}
				if len(reasoning.RedactedContent) > 0 {
					if !block.redacted {
						block.redacted = true
						block.thinkingSignature = ""
						block.thinking += bedrockRedactedThinkingPlaceholder
						syncBedrockContent(output, *blocks)
						stream.Push(AssistantMessageEvent{
							Type: EventThinkingDelta, ContentIndex: index,
							Delta: bedrockRedactedThinkingPlaceholder, Partial: output,
						})
					}
					block.redactedChunks = append(block.redactedChunks, reasoning.RedactedContent)
				}
			}
		}
	case "contentBlockStop":
		var event struct {
			ContentBlockIndex int `json:"contentBlockIndex"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return err
		}
		index := -1
		for position, block := range *blocks {
			if block.contentIndex == event.ContentBlockIndex {
				index = position
				break
			}
		}
		if index == -1 {
			return nil
		}
		block := (*blocks)[index]
		block.contentIndex = -1
		switch block.kind {
		case "text":
			stream.Push(AssistantMessageEvent{
				Type: EventTextEnd, ContentIndex: index, Content: block.text, Partial: output,
			})
		case "thinking":
			block.flushRedactedContent()
			syncBedrockContent(output, *blocks)
			stream.Push(AssistantMessageEvent{
				Type: EventThinkingEnd, ContentIndex: index, Content: block.thinking, Partial: output,
			})
		case "toolCall":
			block.toolCall.Arguments = parseStreamingArgs(json.RawMessage(block.partialJSON))
			block.partialJSON = ""
			syncBedrockContent(output, *blocks)
			call := block.toolCall
			stream.Push(AssistantMessageEvent{
				Type: EventToolcallEnd, ContentIndex: index, ToolCall: &call, Partial: output,
			})
		}
	case "messageStop":
		var event struct {
			StopReason string `json:"stopReason"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return err
		}
		output.RawStopReason = &event.StopReason
		stopReason, errorMessage := MapBedrockStopReason(event.StopReason)
		output.StopReason = stopReason
		if errorMessage != "" {
			output.ErrorMessage = &errorMessage
		}
	case "metadata":
		var event struct {
			Usage *struct {
				InputTokens           *int64 `json:"inputTokens"`
				OutputTokens          *int64 `json:"outputTokens"`
				CacheReadInputTokens  *int64 `json:"cacheReadInputTokens"`
				CacheWriteInputTokens *int64 `json:"cacheWriteInputTokens"`
				TotalTokens           *int64 `json:"totalTokens"`
				CacheDetails          []struct {
					TTL         string `json:"ttl"`
					InputTokens *int64 `json:"inputTokens"`
				} `json:"cacheDetails"`
			} `json:"usage"`
		}
		if err := json.Unmarshal(payload, &event); err != nil {
			return err
		}
		if event.Usage != nil {
			usage := event.Usage
			output.Usage.Input = derefInt64(usage.InputTokens)
			output.Usage.Output = derefInt64(usage.OutputTokens)
			output.Usage.CacheRead = derefInt64(usage.CacheReadInputTokens)
			output.Usage.CacheWrite = derefInt64(usage.CacheWriteInputTokens)
			var oneHour int64
			for _, detail := range usage.CacheDetails {
				if detail.TTL == "1h" {
					oneHour += derefInt64(detail.InputTokens)
				}
			}
			output.Usage.CacheWrite1h = &oneHour
			output.Usage.TotalTokens = derefInt64(usage.TotalTokens)
			if output.Usage.TotalTokens == 0 {
				output.Usage.TotalTokens = output.Usage.Input + output.Usage.Output
			}
			CalculateCost(model, &output.Usage)
		}
	}
	return nil
}

func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

// bedrockExceptionCode carries the AWS modeled exception name.
func bedrockExceptionCode(err error) string {
	if awsErr, ok := err.(*BedrockServiceError); ok {
		return awsErr.Code
	}
	return ""
}

// BedrockServiceError is one modeled Bedrock exception from a stream event.
type BedrockServiceError struct {
	Code    string
	Message string
}

func (e *BedrockServiceError) Error() string { return e.Message }

// StreamBedrockConverseSimple maps simple options onto Bedrock options
// (upstream streamSimple).
func StreamBedrockConverseSimple(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream {
	if options == nil {
		options = &SimpleStreamOptions{}
	}
	bedrockOptions := &BedrockOptions{StreamOptions: options.StreamOptions}
	if options.ToolChoice != nil {
		if *options.ToolChoice == ToolChoiceAuto {
			bedrockOptions.ToolChoice = "auto"
		} else if *options.ToolChoice == ToolChoiceNone {
			bedrockOptions.ToolChoice = "none"
		}
	}
	if options.Reasoning == "" {
		return StreamBedrockConverse(model, context, bedrockOptions)
	}
	bedrockOptions.Reasoning = options.Reasoning
	bedrockOptions.ThinkingBudgets = options.ThinkingBudgets

	if !IsAnthropicClaudeModel(model) {
		return StreamBedrockConverse(model, context, bedrockOptions)
	}
	if SupportsAdaptiveThinking(model.ID, model.Name) {
		return StreamBedrockConverse(model, context, bedrockOptions)
	}

	// Token-budget thinking: fit the budget under the model's output cap, then
	// under the context window.
	var baseMaxTokens *int
	adjustedMaxTokens, thinkingBudget := AdjustMaxTokensForThinking(
		baseMaxTokens, int(model.MaxTokens), options.Reasoning, options.ThinkingBudgets)
	maxTokens := ClampMaxTokensToContext(model, context, adjustedMaxTokens)
	bedrockOptions.MaxTokens = intPtr(maxTokens)

	budgets := ThinkingBudgets{}
	if options.ThinkingBudgets != nil {
		budgets = *options.ThinkingBudgets
	}
	clampedReasoning := ClampReasoning(options.Reasoning)
	room := max(0, maxTokens-1024)
	budget := thinkingBudget
	if budget > room {
		budget = room
	}
	switch clampedReasoning {
	case ThinkMinimal:
		budgets.Minimal = intPtr(budget)
	case ThinkLow:
		budgets.Low = intPtr(budget)
	case ThinkMedium:
		budgets.Medium = intPtr(budget)
	case ThinkHigh, ThinkXHigh, ThinkMax:
		budgets.High = intPtr(budget)
	}
	bedrockOptions.ThinkingBudgets = &budgets
	return StreamBedrockConverse(model, context, bedrockOptions)
}

// BedrockAPIKeyAuth is the Bedrock auth that accepts a bearer token, an AWS
// profile, or the ambient credential chain (upstream bedrockAuth).
func BedrockAPIKeyAuth() *ApiKeyAuth {
	return &ApiKeyAuth{
		Name: "AWS credentials or bearer token",
		Login: func(interaction *AuthInteraction) (*ApiKeyCredential, error) {
			method, err := interaction.Prompt(AuthPrompt{
				Type:    AuthPromptSelect,
				Message: "Select Amazon Bedrock authentication method:",
				SelectOptions: []AuthSelectOption{
					{ID: "bearer-token", Label: "Bearer token"},
					{ID: "aws-profile", Label: "AWS profile"},
					{ID: "credential-chain", Label: "Existing AWS credential chain"},
				},
			})
			if err != nil {
				return nil, err
			}
			switch method {
			case "bearer-token":
				key, perr := interaction.Prompt(AuthPrompt{Type: AuthPromptSecret, Message: "Enter Amazon Bedrock bearer token"})
				if perr != nil {
					return nil, perr
				}
				return &ApiKeyCredential{Key: key}, nil
			case "aws-profile":
				if interaction.Notify != nil {
					interaction.Notify(AuthEvent{
						Type:    AuthEventInfo,
						Message: "Amazon Bedrock supports AWS profiles, IAM credentials, and role-based credentials.",
						Links: []AuthInfoLink{{
							Label: "AWS credential provider chain",
							URL:   "https://docs.aws.amazon.com/sdkref/latest/guide/standardized-credentials.html",
						}},
					})
				}
				profile, perr := interaction.Prompt(AuthPrompt{Type: AuthPromptText, Message: "Enter AWS profile name"})
				if perr != nil {
					return nil, perr
				}
				return &ApiKeyCredential{Env: ProviderEnv{"AWS_PROFILE": profile}}, nil
			case "credential-chain":
				if interaction.Notify != nil {
					interaction.Notify(AuthEvent{
						Type:    AuthEventInfo,
						Message: "Amazon Bedrock supports AWS profiles, IAM credentials, and role-based credentials.",
						Links: []AuthInfoLink{{
							Label: "AWS credential provider chain",
							URL:   "https://docs.aws.amazon.com/sdkref/latest/guide/standardized-credentials.html",
						}},
					})
				}
				if _, perr := interaction.Prompt(AuthPrompt{
					Type:    AuthPromptText,
					Message: "Configure AWS credentials, then press Enter to continue",
				}); perr != nil {
					return nil, perr
				}
				return &ApiKeyCredential{}, nil
			default:
				return nil, fmt.Errorf("Unknown Amazon Bedrock auth method: %s", method)
			}
		},
		Resolve: func(input AuthResolveInput) (*AuthResult, error) {
			if err := ctxErr(input.Ctx2); err != nil {
				return nil, err
			}
			if input.Credential != nil && input.Credential.Key != "" {
				return &AuthResult{
					Auth: ModelAuth{APIKey: input.Credential.Key}, Env: input.Credential.Env,
					Source: "stored credential",
				}, nil
			}
			if _, ok := input.Ctx.Env("AWS_BEARER_TOKEN_BEDROCK"); ok {
				return &AuthResult{Source: "AWS_BEARER_TOKEN_BEDROCK"}, nil
			}
			storedProfile := ""
			if input.Credential != nil && input.Credential.Env != nil {
				storedProfile = input.Credential.Env["AWS_PROFILE"]
			}
			_, hasAmbientProfile := input.Ctx.Env("AWS_PROFILE")
			if storedProfile != "" || hasAmbientProfile {
				source := "AWS_PROFILE"
				if storedProfile != "" {
					source = "stored credential"
				}
				var env ProviderEnv
				if input.Credential != nil {
					env = input.Credential.Env
				}
				return &AuthResult{Env: env, Source: source}, nil
			}
			_, hasAccessKey := input.Ctx.Env("AWS_ACCESS_KEY_ID")
			_, hasSecret := input.Ctx.Env("AWS_SECRET_ACCESS_KEY")
			if hasAccessKey && hasSecret {
				return &AuthResult{Source: "AWS access keys"}, nil
			}
			if _, ok := input.Ctx.Env("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"); ok {
				return &AuthResult{Source: "ECS task role"}, nil
			}
			if _, ok := input.Ctx.Env("AWS_CONTAINER_CREDENTIALS_FULL_URI"); ok {
				return &AuthResult{Source: "ECS task role"}, nil
			}
			if _, ok := input.Ctx.Env("AWS_WEB_IDENTITY_TOKEN_FILE"); ok {
				return &AuthResult{Source: "web identity token"}, nil
			}
			return nil, nil
		},
	}
}

// BedrockStreams adapts the Bedrock Converse implementation.
type BedrockStreams struct{}

func (BedrockStreams) Stream(model *Model, context TranscriptContext, options *StreamOptions) *AssistantMessageEventStream {
	return StreamBedrockConverse(model, context, &BedrockOptions{StreamOptions: derefStreamOptionsForBedrock(options)})
}

func (BedrockStreams) StreamSimple(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream {
	return StreamBedrockConverseSimple(model, context, options)
}

func derefStreamOptionsForBedrock(options *StreamOptions) StreamOptions {
	if options == nil {
		return StreamOptions{}
	}
	return *options
}

// AmazonBedrockProvider builds the built-in Bedrock provider (upstream
// amazonBedrockProvider).
func AmazonBedrockProvider() *Provider {
	return CreateProvider(CreateProviderOptions{
		ID:     "amazon-bedrock",
		Name:   "Amazon Bedrock",
		Auth:   ProviderAuth{APIKey: BedrockAPIKeyAuth()},
		Models: GetBuiltinModels("amazon-bedrock"),
		Single: BedrockStreams{},
	})
}
