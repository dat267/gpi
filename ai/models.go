package ai

import "encoding/json"

// Port of the Model half of types.ts: ModelCost, Model, and the per-API
// compat interfaces.

// ModelCostRates is $/million-token pricing.
type ModelCostRates struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// ModelCostTier is a request-wide pricing tier. The highest matching input
// threshold applies to the full request.
type ModelCostTier struct {
	ModelCostRates
	// InputTokensAbove: use this tier for requests whose total input usage
	// exceeds this token count.
	InputTokensAbove int64 `json:"inputTokensAbove"`
}

// ModelCost is a model's pricing with optional tiers.
type ModelCost struct {
	ModelCostRates
	Tiers []ModelCostTier `json:"tiers,omitempty"`
}

// OpenAICompletionsCompat holds compatibility settings for OpenAI-compatible
// completions APIs. Use it to override URL-based auto-detection for custom
// providers.
type OpenAICompletionsCompat struct {
	SupportsStore                               *bool                      `json:"supportsStore,omitempty"`
	SupportsDeveloperRole                       *bool                      `json:"supportsDeveloperRole,omitempty"`
	SupportsReasoningEffort                     *bool                      `json:"supportsReasoningEffort,omitempty"`
	SupportsUsageInStreaming                    *bool                      `json:"supportsUsageInStreaming,omitempty"`
	SupportsFinishReason                        *bool                      `json:"supportsFinishReason,omitempty"`
	MaxTokensField                              *string                    `json:"maxTokensField,omitempty"` // "max_completion_tokens" | "max_tokens"
	RequiresToolResultName                      *bool                      `json:"requiresToolResultName,omitempty"`
	RequiresAssistantAfterToolResult            *bool                      `json:"requiresAssistantAfterToolResult,omitempty"`
	RequiresThinkingAsText                      *bool                      `json:"requiresThinkingAsText,omitempty"`
	RequiresReasoningContentOnAssistantMessages *bool                      `json:"requiresReasoningContentOnAssistantMessages,omitempty"`
	ThinkingFormat                              *string                    `json:"thinkingFormat,omitempty"`
	ChatTemplateKwargs                          map[string]json.RawMessage `json:"chatTemplateKwargs,omitempty"`
	ChatTemplateArgs                            map[string]json.RawMessage `json:"chatTemplateArgs,omitempty"`
	OpenRouterRouting                           *OpenRouterRouting         `json:"openRouterRouting,omitempty"`
	VercelGatewayRouting                        *VercelGatewayRouting      `json:"vercelGatewayRouting,omitempty"`
	ZaiToolStream                               *bool                      `json:"zaiToolStream,omitempty"`
	ThinkingTokenBudgetField                    *string                    `json:"thinkingTokenBudgetField,omitempty"`
	SupportsThinkingTokenBudget                 *bool                      `json:"supportsThinkingTokenBudget,omitempty"`
	SupportsOpenAIGrammarTools                  *bool                      `json:"supportsOpenAIGrammarTools,omitempty"`
	SupportsMidConvoSystemMessages              *bool                      `json:"supportsMidConvoSystemMessages,omitempty"`
	SupportsMidConvoToolAdditions               *bool                      `json:"supportsMidConvoToolAdditions,omitempty"`
	SupportsStrictMode                          *bool                      `json:"supportsStrictMode,omitempty"`
	CacheControlFormat                          *string                    `json:"cacheControlFormat,omitempty"` // "anthropic"
	SendSessionAffinityHeaders                  *bool                      `json:"sendSessionAffinityHeaders,omitempty"`
	SessionAffinityFormat                       *SessionAffinityFormat     `json:"sessionAffinityFormat,omitempty"`
	SupportsLongCacheRetention                  *bool                      `json:"supportsLongCacheRetention,omitempty"`
	VllmPriority                                *float64                   `json:"vllmPriority,omitempty"`
}

// OpenAIResponsesCompat holds compatibility settings for OpenAI Responses APIs.
type OpenAIResponsesCompat struct {
	SupportsDeveloperRole           *bool                  `json:"supportsDeveloperRole,omitempty"`
	SupportsMidConvoSystemMessages  *bool                  `json:"supportsMidConvoSystemMessages,omitempty"`
	SessionAffinityFormat           *SessionAffinityFormat `json:"sessionAffinityFormat,omitempty"`
	SupportsLongCacheRetention      *bool                  `json:"supportsLongCacheRetention,omitempty"`
	SupportsStrictMode              *bool                  `json:"supportsStrictMode,omitempty"`
	SupportsOpenAIGrammarTools      *bool                  `json:"supportsOpenAIGrammarTools,omitempty"`
	SupportsAdditionalTools         *bool                  `json:"supportsAdditionalTools,omitempty"`
	SupportsToolSearch              *bool                  `json:"supportsToolSearch,omitempty"`
	SupportsExplicitPromptCacheMode *bool                  `json:"supportsExplicitPromptCacheMode,omitempty"`
	SupportsMaxOutputTokens         *bool                  `json:"supportsMaxOutputTokens,omitempty"`
}

// AnthropicAllowedFallbackModel names a model Anthropic accepts in
// `fallbacks` for server-side refusal fallback, with local pricing metadata.
type AnthropicAllowedFallbackModel struct {
	Provider ProviderId `json:"provider"`
	Model    string     `json:"model"`
	Cost     ModelCost  `json:"cost"`
}

// AnthropicMessagesCompat holds compatibility settings for Anthropic
// Messages-compatible APIs.
type AnthropicMessagesCompat struct {
	SupportsEagerToolInputStreaming *bool                           `json:"supportsEagerToolInputStreaming,omitempty"`
	SupportsLongCacheRetention      *bool                           `json:"supportsLongCacheRetention,omitempty"`
	SendSessionAffinityHeaders      *bool                           `json:"sendSessionAffinityHeaders,omitempty"`
	SessionAffinityFormat           *string                         `json:"sessionAffinityFormat,omitempty"` // "openrouter"
	SupportsCacheControlOnTools     *bool                           `json:"supportsCacheControlOnTools,omitempty"`
	SupportsTemperature             *bool                           `json:"supportsTemperature,omitempty"`
	ForceAdaptiveThinking           *bool                           `json:"forceAdaptiveThinking,omitempty"`
	AllowEmptySignature             *bool                           `json:"allowEmptySignature,omitempty"`
	SupportsStrictTools             *bool                           `json:"supportsStrictTools,omitempty"`
	SupportsMidConvoEffort          *bool                           `json:"supportsMidConvoEffort,omitempty"`
	SupportsMidConvoSystemMessages  *bool                           `json:"supportsMidConvoSystemMessages,omitempty"`
	SupportsMidConvoToolChanges     *bool                           `json:"supportsMidConvoToolChanges,omitempty"`
	AllowedFallbackModels           []AnthropicAllowedFallbackModel `json:"allowedFallbackModels,omitempty"`
}

// BedrockCompat holds compatibility settings for Amazon Bedrock models.
type BedrockCompat struct {
	SupportsStrictMode *bool `json:"supportsStrictMode,omitempty"`
}

// MistralConversationsCompat holds compatibility settings for the Mistral
// chat API.
type MistralConversationsCompat struct {
	SupportsMidConvoSystemMessages *bool `json:"supportsMidConvoSystemMessages,omitempty"`
}

// OpenRouterRouting is OpenRouter provider routing preferences, sent as the
// `provider` field in the request body.
type OpenRouterRouting struct {
	AllowFallbacks         *bool                      `json:"allow_fallbacks,omitempty"`
	RequireParameters      *bool                      `json:"require_parameters,omitempty"`
	DataCollection         *string                    `json:"data_collection,omitempty"` // "deny" | "allow"
	Zdr                    *bool                      `json:"zdr,omitempty"`
	EnforceDistillableText *bool                      `json:"enforce_distillable_text,omitempty"`
	Order                  []string                   `json:"order,omitempty"`
	Only                   []string                   `json:"only,omitempty"`
	Ignore                 []string                   `json:"ignore,omitempty"`
	Quantizations          []string                   `json:"quantizations,omitempty"`
	Sort                   json.RawMessage            `json:"sort,omitempty"` // string | { by, partition }
	MaxPrice               map[string]json.RawMessage `json:"max_price,omitempty"`
	PreferredMinThroughput json.RawMessage            `json:"preferred_min_throughput,omitempty"`
	PreferredMaxLatency    json.RawMessage            `json:"preferred_max_latency,omitempty"`
}

// VercelGatewayRouting is Vercel AI Gateway routing preferences.
type VercelGatewayRouting struct {
	Only  []string `json:"only,omitempty"`
	Order []string `json:"order,omitempty"`
}

// ModelCompat is the union of per-API compat settings; exactly one arm is
// populated, keyed by the model's API.
type ModelCompat struct {
	OpenAICompletions    *OpenAICompletionsCompat
	OpenAIResponses      *OpenAIResponsesCompat
	AnthropicMessages    *AnthropicMessagesCompat
	Bedrock              *BedrockCompat
	MistralConversations *MistralConversationsCompat
}

func (c ModelCompat) MarshalJSON() ([]byte, error) {
	switch {
	case c.OpenAICompletions != nil:
		return json.Marshal(c.OpenAICompletions)
	case c.OpenAIResponses != nil:
		return json.Marshal(c.OpenAIResponses)
	case c.AnthropicMessages != nil:
		return json.Marshal(c.AnthropicMessages)
	case c.Bedrock != nil:
		return json.Marshal(c.Bedrock)
	case c.MistralConversations != nil:
		return json.Marshal(c.MistralConversations)
	default:
		return []byte("null"), nil
	}
}

func (c *ModelCompat) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	// Probe which compat family the JSON belongs to by its known keys.
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		return err
	}
	if _, ok := keys["supportsEagerToolInputStreaming"]; ok {
		c.AnthropicMessages = new(AnthropicMessagesCompat)
		return json.Unmarshal(data, c.AnthropicMessages)
	}
	if _, ok := keys["supportsStrictMode"]; ok {
		if _, ok := keys["supportsMidConvoSystemMessages"]; ok {
			// Ambiguous between completions/responses/mistral; decode all
			// three views into the completions arm by default, corrected by
			// the catalog loader which knows the API.
		}
		c.OpenAICompletions = new(OpenAICompletionsCompat)
		return json.Unmarshal(data, c.OpenAICompletions)
	}
	c.OpenAIResponses = new(OpenAIResponsesCompat)
	return json.Unmarshal(data, c.OpenAIResponses)
}

// Model is the unified model descriptor.
type Model struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	API      Api        `json:"api"`
	Provider ProviderId `json:"provider"`
	BaseURL  string     `json:"baseUrl"`
	// Reasoning reports whether the model supports thinking/reasoning.
	Reasoning        bool             `json:"reasoning"`
	ThinkingLevelMap ThinkingLevelMap `json:"thinkingLevelMap,omitempty"`
	Input            []string         `json:"input"` // "text" | "image"
	Cost             ModelCost        `json:"cost"`
	ContextWindow    int64            `json:"contextWindow"`
	MaxTokens        int64            `json:"maxTokens"`
	// SamplingParams are default sampling parameters for this model. See
	// StreamOptions.SamplingParams; per-request keys override these.
	SamplingParams map[string]json.RawMessage `json:"samplingParams,omitempty"`
	Headers        map[string]string          `json:"headers,omitempty"`
	// Compat holds compatibility overrides. If not set, auto-detected from
	// BaseURL.
	Compat *ModelCompat `json:"compat,omitempty"`
}
