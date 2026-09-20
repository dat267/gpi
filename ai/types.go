// Package ai is a faithful Go port of @earendil-works/pi-ai's core: the unified
// multi-provider LLM types, the AssistantMessageEvent protocol, and the
// channel-based EventStream.
//
// Ground truth: pi/packages/ai/src/types.ts at the pinned upstream commit.
// JSON field names match upstream byte-for-byte so transcripts and wire
// payloads stay interoperable with pi.
package ai

import (
	"context"
	"encoding/json"
)

// KnownApi enumerates the APIs with concrete implementations upstream.
type KnownApi = string

const (
	APIOpenAICompletions    KnownApi = "openai-completions"
	APIMistralConversations KnownApi = "mistral-conversations"
	APIOpenAIResponses      KnownApi = "openai-responses"
	APIAzureOpenAIResponses KnownApi = "azure-openai-responses"
	APIOpenAICodexResponses KnownApi = "openai-codex-responses"
	APIAnthropicMessages    KnownApi = "anthropic-messages"
	APIBedrockConverse      KnownApi = "bedrock-converse-stream"
	APIGoogleGenerativeAI   KnownApi = "google-generative-ai"
	APIGoogleVertex         KnownApi = "google-vertex"
	APIPiMessages           KnownApi = "pi-messages"
)

// Api is a known or custom API identifier.
type Api = string

// ProviderId is a known or custom provider identifier.
type ProviderId = string

// ToolChoice is the provider-neutral tool selection for simple requests.
type ToolChoice = string

const (
	ToolChoiceAuto ToolChoice = "auto"
	ToolChoiceNone ToolChoice = "none"
)

// ThinkingLevel is a pi thinking level.
type ThinkingLevel = string

const (
	ThinkMinimal ThinkingLevel = "minimal"
	ThinkLow     ThinkingLevel = "low"
	ThinkMedium  ThinkingLevel = "medium"
	ThinkHigh    ThinkingLevel = "high"
	ThinkXHigh   ThinkingLevel = "xhigh"
	ThinkMax     ThinkingLevel = "max"
)

// ModelThinkingLevel is a thinking level or "off".
type ModelThinkingLevel = string

const ThinkOff ModelThinkingLevel = "off"

// ThinkingLevelMap maps pi thinking levels to provider/model-specific values.
// Missing keys use provider defaults; nil marks a level as unsupported.
type ThinkingLevelMap map[ModelThinkingLevel]*string

// CacheRetention is a prompt cache retention preference.
type CacheRetention = string

const (
	CacheRetentionNone  CacheRetention = "none"
	CacheRetentionShort CacheRetention = "short"
	CacheRetentionLong  CacheRetention = "long"
)

// Transport is the preferred transport for providers that support multiple.
type Transport = string

const (
	TransportSSE             Transport = "sse"
	TransportWebSocket       Transport = "websocket"
	TransportWebSocketCached Transport = "websocket-cached"
	TransportAuto            Transport = "auto"
)

// ProviderEnv is provider-scoped environment overrides. Values take
// precedence over the process environment.
type ProviderEnv map[string]string

// ProviderHeaders maps header names to values. A nil value suppresses a
// provider/API default header with the same name.
type ProviderHeaders map[string]*string

// SessionAffinityFormat selects the session-affinity header format.
type SessionAffinityFormat = string

const (
	SessionAffinityOpenAI       SessionAffinityFormat = "openai"
	SessionAffinityOpenAINoSess SessionAffinityFormat = "openai-nosession"
	SessionAffinityOpenRouter   SessionAffinityFormat = "openrouter"
)

// ProviderResponse is the raw HTTP response surface exposed to onResponse.
type ProviderResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
}

// ThinkingTokenBudgetField names the top-level request field used to cap
// reasoning tokens on OpenAI-compatible servers.
type ThinkingTokenBudgetField = string

const (
	ThinkingTokenBudgetVLLM     ThinkingTokenBudgetField = "thinking_token_budget"
	ThinkingTokenBudgetQwen     ThinkingTokenBudgetField = "thinking_budget"
	ThinkingTokenBudgetLLamaCpp ThinkingTokenBudgetField = "thinking_budget_tokens"
)

// ThinkingBudgets holds token budgets for each thinking level
// (token-based providers only).
type ThinkingBudgets struct {
	Minimal *int `json:"minimal,omitempty"`
	Low     *int `json:"low,omitempty"`
	Medium  *int `json:"medium,omitempty"`
	High    *int `json:"high,omitempty"`
}

// DeferredRequest asks a capable provider to return a durable handle and
// continue the request asynchronously (upstream's `deferred` option).
type DeferredRequest struct {
	// Window is "15m", "1h", or "24h".
	Window string
}

// StreamOptions are the options all providers share.
type StreamOptions struct {
	// Ctx is upstream's AbortSignal: cancel it to abort the request.
	// Nil means context.Background().
	Ctx    context.Context
	APIKey string
	Env    ProviderEnv
	// Headers are merged with provider defaults; caller values override
	// default headers. A nil value suppresses a default header.
	Headers                   ProviderHeaders
	Temperature               *float64
	SamplingParams            map[string]json.RawMessage
	MaxTokens                 *int
	Transport                 Transport
	CacheRetention            CacheRetention
	SessionID                 string
	TimeoutMs                 *int
	MaxRetries                *int
	MaxRetryDelayMs           *int
	Metadata                  map[string]json.RawMessage
	WebsocketConnectTimeoutMs *int
	// TransformHeaders runs over the merged auth/request header set before the
	// request (upstream ModelsRequestTransforms.transformHeaders, which rides on
	// request options through every layer).
	TransformHeaders func(headers ProviderHeaders) ProviderHeaders
	// OnPayload inspects or replaces provider payloads before sending.
	// Return nil to keep the payload unchanged.
	OnPayload func(payload json.RawMessage, model *Model) json.RawMessage
	// OnResponse is invoked after an HTTP response is received.
	OnResponse func(response ProviderResponse, model *Model)
	// Deferred asks a capable provider to return a durable handle and
	// continue the request asynchronously.
	Deferred *DeferredRequest
}

// SimpleStreamOptions adds reasoning and tool selection to StreamOptions for
// streamSimple()/completeSimple().
type SimpleStreamOptions struct {
	StreamOptions
	// ToolChoice is provider-neutral tool selection. When nil, adapters use
	// provider-specific behavior.
	ToolChoice *ToolChoice
	Reasoning  ThinkingLevel
	// ThinkingBudgets are custom token budgets for thinking levels
	// (token-based providers only).
	ThinkingBudgets *ThinkingBudgets
}

// Usage is token usage and cost for one assistant message.
type Usage struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	// CacheWrite1h is the subset of CacheWrite written with 1h retention.
	// Only Anthropic reports this split.
	CacheWrite1h *int64 `json:"cacheWrite1h,omitempty"`
	// Reasoning is the reasoning/thinking token count when the provider
	// reports it. A subset of Output. Nil when the provider doesn't.
	Reasoning   *int64    `json:"reasoning,omitempty"`
	TotalTokens int64     `json:"totalTokens"`
	Cost        UsageCost `json:"cost"`
}

// UsageCost is the dollar cost breakdown of a Usage.
type UsageCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
	Total      float64 `json:"total"`
}

// StopReason explains why an assistant message ended.
type StopReason = string

const (
	StopPending  StopReason = "pending"
	StopStop     StopReason = "stop"
	StopLength   StopReason = "length"
	StopToolUse  StopReason = "toolUse"
	StopError    StopReason = "error"
	StopAborted  StopReason = "aborted"
	StopDeferred StopReason = "deferred"
)

// DeferredHandle identifies an in-flight deferred response.
type DeferredHandle struct {
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"`
	API      string `json:"api"`
	// ID is the provider token, such as a response id or batch id plus row id.
	ID          string `json:"id"`
	ExpiresAt   *int64 `json:"expiresAt,omitempty"`
	PollAfterMs *int64 `json:"pollAfterMs,omitempty"`
	// Data is provider conversion data required to reconstruct the final
	// assistant message.
	Data json.RawMessage `json:"data,omitempty"`
}
