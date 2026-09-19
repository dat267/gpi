package ai

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Port of api/anthropic-messages.ts: wire types, request construction
// (buildParams), and helpers. Streaming/SSE lands with the HTTP client port.

// AnthropicEffort is an adaptive-thinking effort level.
type AnthropicEffort = string

const (
	AnthropicEffortLow    AnthropicEffort = "low"
	AnthropicEffortMedium AnthropicEffort = "medium"
	AnthropicEffortHigh   AnthropicEffort = "high"
	AnthropicEffortXHigh  AnthropicEffort = "xhigh"
	AnthropicEffortMax    AnthropicEffort = "max"
)

// AnthropicThinkingDisplay controls how thinking content is returned.
type AnthropicThinkingDisplay = string

const (
	ThinkingDisplaySummarized AnthropicThinkingDisplay = "summarized"
	ThinkingDisplayOmitted    AnthropicThinkingDisplay = "omitted"
)

// AnthropicOptions extends StreamOptions for the anthropic-messages API.
type AnthropicOptions struct {
	StreamOptions
	// ThinkingEnabled enables extended thinking.
	ThinkingEnabled *bool
	// ThinkingBudgetTokens is the token budget for extended thinking
	// (older models only). Default: 1024 when ThinkingEnabled and unset.
	ThinkingBudgetTokens *int
	// Effort is the adaptive-thinking effort level.
	Effort AnthropicEffort
	// ThinkingDisplay: "summarized" (default) or "omitted".
	ThinkingDisplay AnthropicThinkingDisplay
	// InterleavedThinking requests the interleaved thinking beta for
	// non-adaptive models. Default: true.
	InterleavedThinking *bool
	// ToolChoice: "auto" | "any" | "none" | {"type":"tool","name":...}.
	ToolChoice json.RawMessage
	// Client is a pre-built client hook (unused in the Go port until the
	// HTTP layer lands; reserved for SDK-compatible injection).
}

// AnthropicCacheControl is Anthropic's cache_control marker.
type AnthropicCacheControl struct {
	Type string  `json:"type"` // "ephemeral"
	TTL  *string `json:"ttl,omitempty"`
}

// ResolveCacheRetention defaults to "short" and honors PI_CACHE_RETENTION
// for backward compatibility (port of resolveCacheRetention).
func ResolveCacheRetention(cacheRetention CacheRetention, env ProviderEnv) CacheRetention {
	if cacheRetention != "" {
		return cacheRetention
	}
	if v, ok := GetProviderEnvValue("PI_CACHE_RETENTION", env); ok && v == "long" {
		return CacheRetentionLong
	}
	return CacheRetentionShort
}

// GetCacheControl resolves the retention preference and cache_control marker.
func GetCacheControl(model *Model, cacheRetention CacheRetention, env ProviderEnv) (retention CacheRetention, cacheControl *AnthropicCacheControl) {
	retention = ResolveCacheRetention(cacheRetention, env)
	if retention == CacheRetentionNone {
		return retention, nil
	}
	if retention == CacheRetentionLong && GetAnthropicCompat(model).SupportsLongCacheRetention {
		ttl := "1h"
		return retention, &AnthropicCacheControl{Type: "ephemeral", TTL: &ttl}
	}
	return retention, &AnthropicCacheControl{Type: "ephemeral"}
}

// Stealth mode: mimic Claude Code's tool naming exactly.

const claudeCodeVersion = "2.1.251"

var claudeCodeTools = []string{
	"Read", "Write", "Edit", "Bash", "Grep", "Glob", "AskUserQuestion",
	"EnterPlanMode", "ExitPlanMode", "KillShell", "NotebookEdit", "Skill",
	"Task", "TaskOutput", "TodoWrite", "WebFetch", "WebSearch",
}

var ccToolLookup = func() map[string]string {
	m := map[string]string{}
	for _, t := range claudeCodeTools {
		m[strings.ToLower(t)] = t
	}
	return m
}()

// toClaudeCodeName converts a tool name to CC canonical casing when it
// matches case-insensitively.
func toClaudeCodeName(name string) string {
	if canonical, ok := ccToolLookup[strings.ToLower(name)]; ok {
		return canonical
	}
	return name
}

func fromClaudeCodeName(name string, tools []Tool) string {
	if len(tools) > 0 {
		lower := strings.ToLower(name)
		for _, tool := range tools {
			if strings.ToLower(tool.Name) == lower {
				return tool.Name
			}
		}
	}
	return name
}

// Sanitize surrogates re-exported for the conversion helpers.

// AnthropicCompat is the resolved compatibility surface
// (port of getAnthropicCompat).
type AnthropicCompat struct {
	SupportsEagerToolInputStreaming bool
	SupportsLongCacheRetention      bool
	SendSessionAffinityHeaders      bool
	SessionAffinityFormat           string
	SupportsCacheControlOnTools     bool
	SupportsTemperature             bool
	AllowEmptySignature             bool
	SupportsStrictTools             bool
	SupportsMidConvoSystemMessages  bool
	SupportsMidConvoToolChanges     bool
}

// GetAnthropicCompat resolves the compatibility surface for a model, with
// URL-based OpenRouter detection.
func GetAnthropicCompat(model *Model) AnthropicCompat {
	isOpenRouter := model.Provider == "openrouter" || strings.Contains(model.BaseURL, "openrouter.ai")
	boolOr := func(v *bool, d bool) bool {
		if v != nil {
			return *v
		}
		return d
	}
	compat := AnthropicCompat{
		SupportsEagerToolInputStreaming: true,
		SupportsLongCacheRetention:      true,
		SendSessionAffinityHeaders:      isOpenRouter,
		SupportsCacheControlOnTools:     true,
		SupportsTemperature:             true,
	}
	if model.Compat != nil && model.Compat.AnthropicMessages != nil {
		c := model.Compat.AnthropicMessages
		compat.SupportsEagerToolInputStreaming = boolOr(c.SupportsEagerToolInputStreaming, compat.SupportsEagerToolInputStreaming)
		compat.SupportsLongCacheRetention = boolOr(c.SupportsLongCacheRetention, compat.SupportsLongCacheRetention)
		compat.SendSessionAffinityHeaders = boolOr(c.SendSessionAffinityHeaders, compat.SendSessionAffinityHeaders)
		if c.SessionAffinityFormat != nil {
			compat.SessionAffinityFormat = *c.SessionAffinityFormat
		} else if isOpenRouter {
			compat.SessionAffinityFormat = "openrouter"
		}
		compat.SupportsCacheControlOnTools = boolOr(c.SupportsCacheControlOnTools, compat.SupportsCacheControlOnTools)
		compat.SupportsTemperature = boolOr(c.SupportsTemperature, compat.SupportsTemperature)
		compat.AllowEmptySignature = boolOr(c.AllowEmptySignature, false)
		compat.SupportsStrictTools = boolOr(c.SupportsStrictTools, false)
		compat.SupportsMidConvoSystemMessages = boolOr(c.SupportsMidConvoSystemMessages, false)
		compat.SupportsMidConvoToolChanges = boolOr(c.SupportsMidConvoToolChanges, false)
	} else if isOpenRouter {
		compat.SessionAffinityFormat = "openrouter"
	}
	return compat
}

// Anthropic wire types.

// AnthropicContentBlock is one content block param (text | image | thinking |
// redacted_thinking | tool_use | tool_result | tool_addition | tool_removal).
type AnthropicContentBlock struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// image
	Source *AnthropicImageSource `json:"source,omitempty"`
	// thinking
	Thinking  string  `json:"thinking,omitempty"`
	Signature *string `json:"signature,omitempty"`
	// redacted_thinking
	Data string `json:"data,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // string | ContentBlock[]
	IsError   *bool           `json:"is_error,omitempty"`
	// tool_addition / tool_removal
	Tool *AnthropicToolReference `json:"tool,omitempty"`
	// cache_control (injected on selected blocks)
	CacheControl *AnthropicCacheControl `json:"cache_control,omitempty"`
}

// AnthropicImageSource is a base64 image source.
type AnthropicImageSource struct {
	Type     string `json:"type"` // "base64"
	MimeType string `json:"media_type"`
	Data     string `json:"data"`
}

// AnthropicToolReference references a tool by name.
type AnthropicToolReference struct {
	Type string `json:"type"` // "tool_reference"
	Name string `json:"name"`
}

// AnthropicMessage is one wire message (role + content).
type AnthropicMessage struct {
	Role         string                 `json:"role"`
	Content      json.RawMessage        `json:"content"` // string | ContentBlock[]
	OutputConfig *AnthropicOutputConfig `json:"output_config,omitempty"`
}

// AnthropicOutputConfig carries effort binding for managed-effort models.
type AnthropicOutputConfig struct {
	Effort AnthropicEffort `json:"effort"`
}

// AnthropicTool is one wire tool definition.
type AnthropicTool struct {
	Name                string                 `json:"name"`
	Description         string                 `json:"description"`
	EagerInputStreaming *bool                  `json:"eager_input_streaming,omitempty"`
	Strict              *bool                  `json:"strict,omitempty"`
	InputSchema         json.RawMessage        `json:"input_schema"`
	DeferLoading        *bool                  `json:"defer_loading,omitempty"`
	CacheControl        *AnthropicCacheControl `json:"cache_control,omitempty"`
}

// AnthropicToolChoice is the tool_choice wire shape.
type AnthropicToolChoice struct {
	Type string `json:"type"` // "auto" | "any" | "none" | "tool"
	Name string `json:"name,omitempty"`
}

// AnthropicThinking is the thinking request configuration.
type AnthropicThinking struct {
	Type         string                         `json:"type"` // "adaptive" | "enabled" | "disabled"
	Display      *AnthropicThinkingDisplay      `json:"display,omitempty"`
	BudgetTokens *int                           `json:"budget_tokens,omitempty"`
	BlockBinding *AnthropicThinkingBlockBinding `json:"block_binding,omitempty"`
}

// AnthropicThinkingBlockBinding controls prefix mismatch behavior for
// managed-effort models.
type AnthropicThinkingBlockBinding struct {
	PrefixMismatchBehavior string `json:"prefix_mismatch_behavior"`
}

// AnthropicMessageCreateParams is the streaming request body.
type AnthropicMessageCreateParams struct {
	Model        string                   `json:"model"`
	Messages     []AnthropicMessage       `json:"messages"`
	MaxTokens    int64                    `json:"max_tokens"`
	Stream       bool                     `json:"stream"`
	Betas        []string                 `json:"betas,omitempty"`
	System       json.RawMessage          `json:"system,omitempty"` // string | [{type:"text",...}]
	Temperature  *float64                 `json:"temperature,omitempty"`
	Tools        []AnthropicTool          `json:"tools,omitempty"`
	Thinking     *AnthropicThinking       `json:"thinking,omitempty"`
	OutputConfig *AnthropicOutputConfig   `json:"output_config,omitempty"`
	Metadata     *AnthropicMetadata       `json:"metadata,omitempty"`
	ToolChoice   *AnthropicToolChoice     `json:"tool_choice,omitempty"`
	Fallbacks    []AnthropicFallbackModel `json:"fallbacks,omitempty"`
}

// AnthropicMetadata carries the abuse-tracking user_id.
type AnthropicMetadata struct {
	UserID string `json:"user_id,omitempty"`
}

// AnthropicFallbackModel is one server-side fallback target.
type AnthropicFallbackModel struct {
	Model string `json:"model"`
}

// Beta feature strings.
const (
	FineGrainedToolStreamingBeta    = "fine-grained-tool-streaming-2025-05-14"
	InterleavedThinkingBeta         = "interleaved-thinking-2025-05-14"
	ServerSideFallbackBeta          = "server-side-fallback-2026-07-01"
	MidConversationOutputConfigBeta = "mid-conversation-output-config-2026-07-01"
	ThinkingBindingControlsBeta     = "thinking-binding-controls-2026-08-01"
	MidConversationToolChangesBeta  = "mid-conversation-tool-changes-2026-07-01"
)

// deferredToolPlaceholder is declared whenever native tool changes are in
// use: Anthropic adds hidden prompt scaffolding as soon as any tool has
// defer_loading; declaring this placeholder from the first request keeps
// that scaffolding in the cached prefix.
var deferredToolPlaceholder = AnthropicTool{
	Name:         "__pi_deferred_placeholder__",
	Description:  "Reserved placeholder. Never available. Never call this.",
	InputSchema:  json.RawMessage(`{"type":"object","properties":{},"required":[]}`),
	DeferLoading: boolPtr(true),
}

func boolPtr(b bool) *bool { return &b }

func shouldUseServerSideFallbackBeta(model *Model) bool {
	return model.Compat != nil && model.Compat.AnthropicMessages != nil &&
		len(model.Compat.AnthropicMessages.AllowedFallbackModels) > 0
}

// ConvertAnthropicContentBlocks converts user/tool-result content to wire
// format. Text-only content becomes a concatenated string; images produce a
// block array (with a placeholder when images carry no text).
func ConvertAnthropicContentBlocks(content StringOrBlocks) json.RawMessage {
	if content.Blocks == nil {
		return mustMarshalJSON(SanitizeSurrogates(content.Text))
	}
	hasImages := false
	for _, block := range content.Blocks {
		if _, ok := block.(ImageContent); ok {
			hasImages = true
		}
	}
	if !hasImages {
		var texts []string
		for _, block := range content.Blocks {
			texts = append(texts, block.(TextContent).Text)
		}
		return mustMarshalJSON(SanitizeSurrogates(strings.Join(texts, "\n")))
	}
	var blocks []AnthropicContentBlock
	hasText := false
	for _, block := range content.Blocks {
		switch b := block.(type) {
		case TextContent:
			hasText = true
			blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: SanitizeSurrogates(b.Text)})
		case ImageContent:
			blocks = append(blocks, AnthropicContentBlock{
				Type:   "image",
				Source: &AnthropicImageSource{Type: "base64", MimeType: b.MimeType, Data: b.Data},
			})
		}
	}
	if !hasText {
		blocks = append([]AnthropicContentBlock{{Type: "text", Text: "(see attached image)"}}, blocks...)
	}
	return mustMarshalJSON(blocks)
}

func mustMarshalJSON(v any) json.RawMessage {
	enc, err := MarshalJSON(v)
	if err != nil {
		panic(err)
	}
	return enc
}

// NormalizeAnthropicToolCallID normalizes tool call IDs to Anthropic's
// required pattern and length.
func NormalizeAnthropicToolCallID(id string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	normalized := re.ReplaceAllString(id, "_")
	if len(normalized) > 64 {
		normalized = normalized[:64]
	}
	return normalized
}

func convertToolResult(msg *ToolResultMessage) AnthropicContentBlock {
	isError := msg.IsError
	return AnthropicContentBlock{
		Type:      "tool_result",
		ToolUseID: msg.ToolCallID,
		Content:   ConvertAnthropicContentBlocks(StringOrBlocks{Blocks: userToContentList(msg.Content)}),
		IsError:   &isError,
	}
}

// ConvertedAnthropicMessages is the conversion result.
type ConvertedAnthropicMessages struct {
	Messages []AnthropicMessage
	// AssistantLevels maps message index to a replayed provider effort level
	// (managed-effort models only).
	AssistantLevels map[int]AnthropicEffort
}

// ConvertAnthropicMessages converts transcript messages to wire messages.
// Later system messages are held back and emitted directly before the next
// assistant message (or at the end): Anthropic requires tool_result blocks
// to immediately follow their tool_use. Cache control lands on the last
// user/system block to cache conversation history.
func ConvertAnthropicMessages(
	transformedMessages []Message,
	isOAuthToken bool,
	cacheControl *AnthropicCacheControl,
	allowEmptySignature bool,
	managedProvider string,
	nativeToolChanges bool,
) ConvertedAnthropicMessages {
	var params []AnthropicMessage
	assistantLevels := map[int]AnthropicEffort{}
	var pendingSystemMessages []AnthropicMessage
	flushPending := func() {
		params = append(params, pendingSystemMessages...)
		pendingSystemMessages = nil
	}

	for i := 0; i < len(transformedMessages); i++ {
		msg := transformedMessages[i]
		switch m := msg.(type) {
		case *SystemMessage:
			text := RenderSystemMessageUpdate(m)
			var blocks []AnthropicContentBlock
			if len(text) > 0 {
				blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: SanitizeSurrogates(text)})
			}
			if nativeToolChanges {
				for _, tool := range m.ToolsRemoved {
					name := tool.Name
					if isOAuthToken {
						name = toClaudeCodeName(name)
					}
					blocks = append(blocks, AnthropicContentBlock{
						Type: "tool_removal",
						Tool: &AnthropicToolReference{Type: "tool_reference", Name: name},
					})
				}
				for _, tool := range m.ToolsAdded {
					name := tool.Name
					if isOAuthToken {
						name = toClaudeCodeName(name)
					}
					blocks = append(blocks, AnthropicContentBlock{
						Type: "tool_addition",
						Tool: &AnthropicToolReference{Type: "tool_reference", Name: name},
					})
				}
			}
			if len(blocks) > 0 {
				pendingSystemMessages = append(pendingSystemMessages, AnthropicMessage{
					Role:    "system",
					Content: mustMarshalJSON(blocks),
				})
			}
		case *UserMessage:
			if m.Content.Blocks == nil {
				if !JSTrimIsEmpty(m.Content.Text) {
					params = append(params, AnthropicMessage{
						Role:    "user",
						Content: mustMarshalJSON(SanitizeSurrogates(m.Content.Text)),
					})
				}
				continue
			}
			var blocks []AnthropicContentBlock
			for _, item := range m.Content.Blocks {
				switch b := item.(type) {
				case TextContent:
					blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: SanitizeSurrogates(b.Text)})
				case ImageContent:
					blocks = append(blocks, AnthropicContentBlock{
						Type:   "image",
						Source: &AnthropicImageSource{Type: "base64", MimeType: b.MimeType, Data: b.Data},
					})
				}
			}
			var filtered []AnthropicContentBlock
			for _, b := range blocks {
				if b.Type == "text" && JSTrimIsEmpty(b.Text) {
					continue
				}
				filtered = append(filtered, b)
			}
			if len(filtered) == 0 {
				continue
			}
			params = append(params, AnthropicMessage{Role: "user", Content: mustMarshalJSON(filtered)})
		case *AssistantMessage:
			flushPending()
			var blocks []AnthropicContentBlock
			for _, block := range m.Content {
				switch b := block.(type) {
				case TextContent:
					if JSTrimIsEmpty(b.Text) {
						continue
					}
					blocks = append(blocks, AnthropicContentBlock{Type: "text", Text: SanitizeSurrogates(b.Text)})
				case ThinkingContent:
					// Redacted thinking: pass the opaque payload back.
					if b.Redacted {
						blocks = append(blocks, AnthropicContentBlock{Type: "redacted_thinking", Data: deref(b.ThinkingSignature)})
						continue
					}
					thinkingSignature := deref(b.ThinkingSignature)
					hasThinkingSignature := thinkingSignature != "" && !JSTrimIsEmpty(thinkingSignature)
					if JSTrimIsEmpty(b.Thinking) && !hasThinkingSignature {
						continue
					}
					// Missing/empty signature (e.g. from aborted stream):
					// convert to plain text unless the model allows empty ones.
					if !hasThinkingSignature {
						if allowEmptySignature {
							sig := ""
							blocks = append(blocks, AnthropicContentBlock{
								Type: "thinking", Thinking: SanitizeSurrogates(b.Thinking), Signature: &sig,
							})
						} else {
							blocks = append(blocks, AnthropicContentBlock{
								Type: "text", Text: SanitizeSurrogates(b.Thinking),
							})
						}
					} else {
						blocks = append(blocks, AnthropicContentBlock{
							Type: "thinking", Thinking: SanitizeSurrogates(b.Thinking), Signature: &thinkingSignature,
						})
					}
				case ToolCall:
					name := b.Name
					if isOAuthToken {
						name = toClaudeCodeName(name)
					}
					args := b.Arguments
					if len(args) == 0 {
						args = json.RawMessage("{}")
					}
					blocks = append(blocks, AnthropicContentBlock{
						Type: "tool_use", ID: b.ID, Name: name, Input: args,
					})
				}
			}
			if len(blocks) == 0 {
				continue
			}
			messageIndex := len(params)
			params = append(params, AnthropicMessage{Role: "assistant", Content: mustMarshalJSON(blocks)})
			if managedProvider != "" && m.API == APIAnthropicMessages && m.Provider == managedProvider &&
				isAnthropicEffort(deref(m.ProviderThinkingLevel)) {
				assistantLevels[messageIndex] = *m.ProviderThinkingLevel
			}
		case *ToolResultMessage:
			// Collect consecutive toolResult messages into one user message
			// (needed for z.ai's Anthropic endpoint).
			var toolResults []AnthropicContentBlock
			j := i
			for j < len(transformedMessages) {
				tr, ok := transformedMessages[j].(*ToolResultMessage)
				if !ok {
					break
				}
				toolResults = append(toolResults, convertToolResult(tr))
				j++
			}
			i = j - 1
			params = append(params, AnthropicMessage{Role: "user", Content: mustMarshalJSON(toolResults)})
		}
	}

	flushPending()

	// Cache conversation history: cache_control on the last user or system
	// message block.
	if cacheControl != nil && len(params) > 0 {
		last := &params[len(params)-1]
		if last.Role == "user" || last.Role == "system" {
			var blocks []AnthropicContentBlock
			if err := jsonUnmarshalStrict(last.Content, &blocks); err == nil && len(blocks) > 0 {
				lastBlock := &blocks[len(blocks)-1]
				switch lastBlock.Type {
				case "text", "image", "tool_result", "tool_addition", "tool_removal":
					lastBlock.CacheControl = cacheControl
					last.Content = mustMarshalJSON(blocks)
				}
			} else {
				// String content: convert to a block array with cache_control.
				var text string
				if err := jsonUnmarshalStrict(last.Content, &text); err == nil {
					last.Content = mustMarshalJSON([]AnthropicContentBlock{{
						Type: "text", Text: text, CacheControl: cacheControl,
					}})
				}
			}
		}
	}

	return ConvertedAnthropicMessages{Messages: params, AssistantLevels: assistantLevels}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func isAnthropicEffort(value string) bool {
	switch value {
	case "low", "medium", "high", "xhigh", "max":
		return true
	}
	return false
}

// InsertThinkingLevelMessages injects per-turn effort bindings for
// managed-effort models.
func InsertThinkingLevelMessages(converted ConvertedAnthropicMessages, activeEffort AnthropicEffort) []AnthropicMessage {
	var messages []AnthropicMessage
	for index, message := range converted.Messages {
		if historicalEffort, ok := converted.AssistantLevels[index]; ok {
			messages = append(messages, AnthropicMessage{
				Role:         "system",
				Content:      json.RawMessage("[]"),
				OutputConfig: &AnthropicOutputConfig{Effort: historicalEffort},
			})
		}
		messages = append(messages, message)
	}
	messages = append(messages, AnthropicMessage{
		Role:         "system",
		Content:      json.RawMessage("[]"),
		OutputConfig: &AnthropicOutputConfig{Effort: activeEffort},
	})
	return messages
}

// ConvertAnthropicTools converts tool declarations to wire format. The last
// tool carries the cache_control breakpoint.
func ConvertAnthropicTools(
	tools []Tool,
	isOAuthToken bool,
	supportsEagerToolInputStreaming bool,
	supportsStrictTools bool,
	cacheControl *AnthropicCacheControl,
) ([]AnthropicTool, error) {
	var out []AnthropicTool
	for index, tool := range tools {
		strict, unset, err := ResolveJSONSchemaStrictSampling(tool, supportsStrictTools)
		if err != nil {
			return nil, err
		}
		isStrict := strict && !unset
		parameters := GetJSONSchemaToolParameters(tool, isStrict)
		// legacyInputSchema: {type, properties, required}.
		legacy, err := legacyInputSchema(parameters)
		if err != nil {
			return nil, err
		}
		var inputSchema json.RawMessage
		if isStrict {
			// Strict: full parameters overlaid with the legacy fields
			// (spread order: parameters first, legacy wins).
			merged, merr := overlaySchema(parameters, legacy)
			if merr != nil {
				return nil, merr
			}
			inputSchema = merged
		} else {
			inputSchema = legacy
		}

		name := tool.Name
		if isOAuthToken {
			name = toClaudeCodeName(name)
		}
		wire := AnthropicTool{
			Name:        name,
			Description: tool.Description,
			InputSchema: inputSchema,
		}
		if supportsEagerToolInputStreaming {
			wire.EagerInputStreaming = boolPtr(true)
		}
		if isStrict {
			wire.Strict = boolPtr(true)
		}
		if cacheControl != nil && index == len(tools)-1 {
			wire.CacheControl = cacheControl
		}
		out = append(out, wire)
	}
	return out, nil
}

// legacyInputSchema reduces a schema to {type: object, properties, required}.
func legacyInputSchema(parameters json.RawMessage) (json.RawMessage, error) {
	var schema struct {
		Properties json.RawMessage            `json:"properties"`
		Required   []string                   `json:"required"`
		Extra      map[string]json.RawMessage `json:"-"`
	}
	if err := jsonUnmarshalStrict(parameters, &schema); err != nil {
		return nil, err
	}
	props := schema.Properties
	if len(props) == 0 {
		props = json.RawMessage("{}")
	}
	required := schema.Required
	if required == nil {
		required = []string{}
	}
	return mustMarshalJSON(struct {
		Type       string   `json:"type"`
		Properties any      `json:"properties"`
		Required   []string `json:"required"`
	}{"object", json.RawMessage(props), required}), nil
}

// overlaySchema merges a strict parameters object with the legacy fields,
// legacy winning (upstream spread order).
func overlaySchema(parameters, legacy json.RawMessage) (json.RawMessage, error) {
	var p map[string]json.RawMessage
	if err := jsonUnmarshalStrict(parameters, &p); err != nil {
		return nil, err
	}
	var l map[string]json.RawMessage
	if err := jsonUnmarshalStrict(legacy, &l); err != nil {
		return nil, err
	}
	for k, v := range l {
		p[k] = v
	}
	return sortedObjectJSON(mustMarshalJSON(p)), nil
}

// GetBetaFeatures resolves the anthropic-beta list (port of getBetaFeatures).
func GetBetaFeatures(model *Model, context TranscriptContext, isOAuthToken, nativeToolChanges bool, options *AnthropicOptions) []string {
	if options == nil {
		options = &AnthropicOptions{}
	}
	// A configured anthropic-beta header overrides the defaults; null
	// suppresses all betas. Later header sources win (model.headers, then
	// options.headers).
	var configured *string
	configuredNull := false
	for _, headers := range []ProviderHeaders{headersOf(model.Headers), options.Headers} {
		for name, value := range headers {
			if strings.EqualFold(name, "anthropic-beta") {
				if value == nil {
					configured, configuredNull = nil, true
				} else {
					configured, configuredNull = value, false
				}
			}
		}
	}
	if configuredNull {
		return []string{}
	}
	if configured != nil {
		seen := map[string]bool{}
		var out []string
		for _, feature := range strings.Split(*configured, ",") {
			feature = strings.TrimSpace(feature)
			if feature == "" || seen[feature] {
				continue
			}
			seen[feature] = true
			out = append(out, feature)
		}
		return out
	}

	var features []string
	add := func(f string) {
		for _, existing := range features {
			if existing == f {
				return
			}
		}
		features = append(features, f)
	}
	if isOAuthToken {
		add("claude-code-20250219")
		add("oauth-2025-04-20")
	}
	if len(GetCurrentTools(context.Messages)) > 0 && !GetAnthropicCompat(model).SupportsEagerToolInputStreaming {
		add(FineGrainedToolStreamingBeta)
	}
	interleaved := options.InterleavedThinking == nil || *options.InterleavedThinking
	forcedAdaptive := model.Compat != nil && model.Compat.AnthropicMessages != nil &&
		model.Compat.AnthropicMessages.ForceAdaptiveThinking != nil && *model.Compat.AnthropicMessages.ForceAdaptiveThinking
	if model.Reasoning && options.ThinkingEnabled != nil && *options.ThinkingEnabled && interleaved && !forcedAdaptive {
		add(InterleavedThinkingBeta)
	}
	if shouldUseServerSideFallbackBeta(model) {
		add(ServerSideFallbackBeta)
	}
	if model.Compat != nil && model.Compat.AnthropicMessages != nil &&
		model.Compat.AnthropicMessages.SupportsMidConvoEffort != nil && *model.Compat.AnthropicMessages.SupportsMidConvoEffort {
		add(MidConversationOutputConfigBeta)
		add(ThinkingBindingControlsBeta)
	}
	if nativeToolChanges {
		add(MidConversationToolChangesBeta)
	}
	return features
}

func ptrString(s string) *string { return &s }

// BuildAnthropicParams builds the streaming request body
// (port of buildParams).
func BuildAnthropicParams(model *Model, context TranscriptContext, isOAuthToken bool, options *AnthropicOptions) (*AnthropicMessageCreateParams, error) {
	if options == nil {
		options = &AnthropicOptions{}
	}
	_, cacheControl := GetCacheControl(model, options.CacheRetention, options.Env)
	compat := GetAnthropicCompat(model)
	initialSystemMessage := GetInitialSystemMessage(context.Messages)
	initialSystemText := ""
	if initialSystemMessage != nil {
		initialSystemText = GetSystemMessageText(initialSystemMessage)
	}
	transformedMessages := TransformMessages(context.Messages, model, func(id string, model *Model, source *AssistantMessage) string {
		return NormalizeAnthropicToolCallID(id)
	})
	conversationMessages := transformedMessages
	if initialSystemMessage != nil {
		conversationMessages = transformedMessages[1:]
	}
	// Native tool changes reference tools by name, so a redefined name
	// cannot be expressed, and Anthropic rejects a tool list where every
	// tool is deferred, so there must be an initial active tool to anchor
	// the deferred ones.
	initialTools := []Tool{}
	if initialSystemMessage != nil {
		initialTools = initialSystemMessage.ToolsAdded
	}
	nativeToolChanges := compat.SupportsMidConvoSystemMessages && compat.SupportsMidConvoToolChanges &&
		len(initialTools) > 0 && !HasToolRedefinitions(context.Messages)

	managedProvider := ""
	if model.Compat != nil && model.Compat.AnthropicMessages != nil &&
		model.Compat.AnthropicMessages.SupportsMidConvoEffort != nil && *model.Compat.AnthropicMessages.SupportsMidConvoEffort {
		managedProvider = model.Provider
	}
	converted := ConvertAnthropicMessages(conversationMessages, isOAuthToken, cacheControl, compat.AllowEmptySignature, managedProvider, nativeToolChanges)

	activeEffort := options.Effort
	if activeEffort == "" {
		activeEffort = AnthropicEffortHigh
	}
	betaFeatures := GetBetaFeatures(model, context, isOAuthToken, nativeToolChanges, options)

	var messages []AnthropicMessage
	if managedProvider != "" {
		messages = InsertThinkingLevelMessages(converted, activeEffort)
	} else {
		messages = converted.Messages
	}

	maxTokens := int64(model.MaxTokens)
	if options.MaxTokens != nil {
		maxTokens = int64(*options.MaxTokens)
	}
	params := &AnthropicMessageCreateParams{
		Model:     model.ID,
		Messages:  messages,
		MaxTokens: maxTokens,
		Stream:    true,
	}
	if len(betaFeatures) > 0 {
		params.Betas = betaFeatures
	}

	// For OAuth tokens, we MUST include Claude Code identity.
	if isOAuthToken {
		system := []AnthropicContentBlock{{
			Type: "text", Text: "You are Claude Code, Anthropic's official CLI for Claude.",
			CacheControl: cacheControl,
		}}
		if initialSystemText != "" {
			system = append(system, AnthropicContentBlock{
				Type: "text", Text: SanitizeSurrogates(initialSystemText), CacheControl: cacheControl,
			})
		}
		params.System = mustMarshalJSON(system)
	} else if initialSystemText != "" {
		params.System = mustMarshalJSON([]AnthropicContentBlock{{
			Type: "text", Text: SanitizeSurrogates(initialSystemText), CacheControl: cacheControl,
		}})
	}

	// Temperature is incompatible with extended thinking and unsupported on
	// Claude Opus 4.7+.
	if options.Temperature != nil && (options.ThinkingEnabled == nil || !*options.ThinkingEnabled) &&
		managedProvider == "" && compat.SupportsTemperature {
		params.Temperature = options.Temperature
	}

	toolCacheControl := cacheControl
	if !compat.SupportsCacheControlOnTools {
		toolCacheControl = nil
	}
	if nativeToolChanges {
		// Initial tools stay active with the cache breakpoint on the last
		// one; every later declaration is deferred. The request-level list
		// only grows, keeping the cached prefix intact across tool changes.
		initialNames := map[string]bool{}
		for _, tool := range initialTools {
			initialNames[tool.Name] = true
		}
		var laterTools []Tool
		for _, tool := range GetDeclaredTools(context.Messages) {
			if !initialNames[tool.Name] {
				laterTools = append(laterTools, tool)
			}
		}
		initial, err := ConvertAnthropicTools(initialTools, isOAuthToken, compat.SupportsEagerToolInputStreaming, compat.SupportsStrictTools, toolCacheControl)
		if err != nil {
			return nil, err
		}
		params.Tools = append(params.Tools, initial...)
		params.Tools = append(params.Tools, deferredToolPlaceholder)
		later, err := ConvertAnthropicTools(laterTools, isOAuthToken, compat.SupportsEagerToolInputStreaming, compat.SupportsStrictTools, nil)
		if err != nil {
			return nil, err
		}
		for _, tool := range later {
			tool.DeferLoading = boolPtr(true)
			params.Tools = append(params.Tools, tool)
		}
	} else {
		tools := GetCurrentTools(context.Messages)
		if len(tools) > 0 {
			converted, err := ConvertAnthropicTools(tools, isOAuthToken, compat.SupportsEagerToolInputStreaming, compat.SupportsStrictTools, toolCacheControl)
			if err != nil {
				return nil, err
			}
			params.Tools = converted
		}
	}

	// Managed effort models always use adaptive thinking so prefix
	// mismatches can be dropped instead of surfacing as persistent 400s.
	forcedAdaptive := model.Compat != nil && model.Compat.AnthropicMessages != nil &&
		model.Compat.AnthropicMessages.ForceAdaptiveThinking != nil && *model.Compat.AnthropicMessages.ForceAdaptiveThinking
	if managedProvider != "" {
		display := options.ThinkingDisplay
		if display == "" {
			display = ThinkingDisplaySummarized
		}
		params.Thinking = &AnthropicThinking{
			Type:         "adaptive",
			Display:      &display,
			BlockBinding: &AnthropicThinkingBlockBinding{PrefixMismatchBehavior: "drop_block"},
		}
		params.OutputConfig = &AnthropicOutputConfig{Effort: AnthropicEffortHigh}
	} else if model.Reasoning {
		if options.ThinkingEnabled != nil && *options.ThinkingEnabled {
			display := options.ThinkingDisplay
			if display == "" {
				display = ThinkingDisplaySummarized
			}
			if forcedAdaptive {
				params.Thinking = &AnthropicThinking{Type: "adaptive", Display: &display}
				if options.Effort != "" {
					params.OutputConfig = &AnthropicOutputConfig{Effort: options.Effort}
				}
			} else {
				budget := 1024
				if options.ThinkingBudgetTokens != nil && *options.ThinkingBudgetTokens != 0 {
					budget = *options.ThinkingBudgetTokens
				}
				params.Thinking = &AnthropicThinking{Type: "enabled", BudgetTokens: &budget, Display: &display}
			}
		} else if options.ThinkingEnabled != nil && !*options.ThinkingEnabled &&
			!(model.ThinkingLevelMap[ThinkOff] != nil && *model.ThinkingLevelMap[ThinkOff] == "") {
			// thinkingLevelMap.off !== null (nil means absent → still send disabled)
			offVal, has := model.ThinkingLevelMap[ThinkOff]
			if !has || offVal != nil {
				params.Thinking = &AnthropicThinking{Type: "disabled"}
			}
		}
	}

	if userIDRaw, ok := options.Metadata["user_id"]; ok {
		var userID string
		if err := jsonUnmarshalStrict(userIDRaw, &userID); err == nil && userID != "" {
			params.Metadata = &AnthropicMetadata{UserID: userID}
		}
	}

	if options.ToolChoice != nil && len(options.ToolChoice) > 0 {
		var strChoice string
		if err := jsonUnmarshalStrict(options.ToolChoice, &strChoice); err == nil {
			params.ToolChoice = &AnthropicToolChoice{Type: strChoice}
		} else {
			var obj AnthropicToolChoice
			if err := jsonUnmarshalStrict(options.ToolChoice, &obj); err == nil {
				params.ToolChoice = &obj
			}
		}
	}

	var allowedFallbackModels []AnthropicAllowedFallbackModel
	if model.Compat != nil && model.Compat.AnthropicMessages != nil {
		allowedFallbackModels = model.Compat.AnthropicMessages.AllowedFallbackModels
	}
	if len(allowedFallbackModels) > 0 {
		for _, fallback := range allowedFallbackModels {
			params.Fallbacks = append(params.Fallbacks, AnthropicFallbackModel{Model: fallback.Model})
		}
	}

	return params, nil
}

// MapAnthropicStopReason maps an Anthropic stop reason to pi's
// (port of mapStopReason). An unknown reason is an error, matching upstream.
func MapAnthropicStopReason(reason string, refusalExplanation string) (stopReason StopReason, errorMessage string, err error) {
	switch reason {
	case "end_turn":
		return StopStop, "", nil
	case "max_tokens":
		return StopLength, "", nil
	case "tool_use":
		return StopToolUse, "", nil
	case "refusal":
		if refusalExplanation != "" {
			return StopError, refusalExplanation, nil
		}
		return StopError, "The model refused to complete the request", nil
	case "pause_turn": // Stop is good enough -> resubmit
		return StopStop, "", nil
	case "stop_sequence":
		return StopStop, "", nil
	case "sensitive":
		return StopError, "Provider stopped with: sensitive", nil
	default:
		return "", "", fmt.Errorf("Unhandled stop reason: %s", reason)
	}
}
