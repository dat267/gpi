package ai

import (
	"strings"
	"testing"
)

// Round 109 tests: context-overflow detection (port of utils/overflow.ts).

func overflowError(message *AssistantMessage, text string) *AssistantMessage {
	copied := *message
	copied.StopReason = StopError
	copied.ErrorMessage = &text
	return &copied
}

func overflowAssistant(provider string, stopReason string) *AssistantMessage {
	return &AssistantMessage{
		API: APIAnthropicMessages, Provider: provider, Model: "m", StopReason: stopReason,
	}
}

func TestIsContextOverflowPatterns(t *testing.T) {
	overflowMessages := []string{
		"prompt is too long: 213462 tokens > 200000 maximum",
		`413 {"error":{"type":"request_too_large","message":"Request exceeds the maximum size"}}`,
		"Input is too long for requested model",
		"Your input exceeds the context window of this model",
		"Requested token count exceeds the model's maximum context length of 131072 tokens",
		"Input length (265330) exceeds model's maximum context length (262144).",
		"The input token count (1196265) exceeds the maximum number of tokens allowed (1048575)",
		"This model's maximum prompt length is 131072 but the request contains 537812 tokens",
		"Please reduce the length of the messages or completion",
		"This endpoint's maximum context length is 131072 tokens.",
		"Input length 265330 exceeds the maximum allowed input length of 262144 tokens.",
		"The input (131073 tokens) is longer than the model's context length (131072 tokens).",
		"prompt token count of 265330 exceeds the limit of 262144",
		"the request exceeds the available context size, try increasing it",
		"tokens to keep from the initial prompt is greater than the context length",
		"invalid params, context window exceeds limit",
		"Your request exceeded model token limit: 200000 (requested: 213462)",
		"Prompt has 213462 tokens, but the configured context size is 200000 tokens",
		"model_context_window_exceeded",
		"prompt too long; exceeded max context length by 100 tokens",
		"Range of input length should be [1, 200000]",
		"context_length_exceeded",
		"too many tokens",
		"token limit exceeded",
	}
	for _, text := range overflowMessages {
		message := overflowError(overflowAssistant("anthropic", StopError), text)
		if !IsContextOverflow(message, 0) {
			t.Errorf("expected overflow for %q", text)
		}
	}

	// Cerebras' bodyless status text only counts for the cerebras provider.
	bodyless := "400 status code (no body)"
	if !IsContextOverflow(overflowError(overflowAssistant("cerebras", StopError), bodyless), 0) {
		t.Error("cerebras bodyless overflow must be detected")
	}
	if IsContextOverflow(overflowError(overflowAssistant("anthropic", StopError), bodyless), 0) {
		t.Error("bodyless overflow must not apply to other providers")
	}
}

func TestIsContextOverflowNonOverflow(t *testing.T) {
	// Rate limiting and server errors win over the generic overflow patterns.
	nonOverflow := []string{
		"Throttling error: Too many tokens, please wait before trying again.",
		"Service unavailable: token limit exceeded",
		"Rate limit exceeded",
		"Too many requests",
	}
	for _, text := range nonOverflow {
		message := overflowError(overflowAssistant("amazon-bedrock", StopError), text)
		if IsContextOverflow(message, 0) {
			t.Errorf("expected non-overflow for %q", text)
		}
	}
	// Unrelated error text is not an overflow.
	if IsContextOverflow(overflowError(overflowAssistant("anthropic", StopError), "invalid api key"), 0) {
		t.Error("unrelated errors must not be overflow")
	}
	if IsContextOverflow(nil, 0) {
		t.Error("nil is not overflow")
	}
	if IsContextOverflow(overflowError(overflowAssistant("anthropic", StopError), ""), 0) {
		t.Error("empty messages are not overflow")
	}
}

func TestIsContextOverflowSilentAndLengthStop(t *testing.T) {
	// Silent overflow: a success whose input exceeds the window.
	silent := &AssistantMessage{
		API: APIAnthropicMessages, Provider: "zai", Model: "m", StopReason: StopStop,
		Usage: Usage{Input: 200000, CacheRead: 5000, TotalTokens: 205000},
	}
	if !IsContextOverflow(silent, 200000) {
		t.Error("silent overflow must be detected")
	}
	// Without a window the silent case cannot be detected.
	if IsContextOverflow(silent, 0) {
		t.Error("silent overflow needs the context window")
	}
	// Within the window it is not an overflow.
	within := &AssistantMessage{
		API: APIAnthropicMessages, Provider: "zai", Model: "m", StopReason: StopStop,
		Usage: Usage{Input: 100000, TotalTokens: 100000},
	}
	if IsContextOverflow(within, 200000) {
		t.Error("usage within the window is not overflow")
	}

	// Length-stop overflow: zero output with the input filling the window.
	truncated := &AssistantMessage{
		API: APIAnthropicMessages, Provider: "mimo", Model: "m", StopReason: StopLength,
		Usage: Usage{Input: 199000, CacheRead: 1000, Output: 0, TotalTokens: 200000},
	}
	if !IsContextOverflow(truncated, 200000) {
		t.Error("length-stop overflow must be detected")
	}
	// Zero output but plenty of room is not an overflow.
	roomy := &AssistantMessage{
		API: APIAnthropicMessages, Provider: "mimo", Model: "m", StopReason: StopLength,
		Usage: Usage{Input: 1000, Output: 0, TotalTokens: 1000},
	}
	if IsContextOverflow(roomy, 200000) {
		t.Error("length stops with room are not overflow")
	}
	// A length stop with output produced is not case 3.
	withOutput := &AssistantMessage{
		API: APIAnthropicMessages, Provider: "mimo", Model: "m", StopReason: StopLength,
		Usage: Usage{Input: 199000, Output: 50, TotalTokens: 199050},
	}
	if IsContextOverflow(withOutput, 200000) {
		t.Error("length stops with output are handled by recoverable-length logic")
	}
}

func TestIsRecoverableLength(t *testing.T) {
	lengthStop := overflowAssistant("anthropic", StopLength)
	lengthStop.Usage.Output = 100
	if !IsRecoverableLength(lengthStop, 4096) {
		t.Error("output below the desired limit is recoverable")
	}
	if IsRecoverableLength(lengthStop, 100) {
		t.Error("output at the limit is not recoverable")
	}
	if IsRecoverableLength(lengthStop, 0) {
		t.Error("without a desired limit there is nothing to recover")
	}
	normal := overflowAssistant("anthropic", StopStop)
	if IsRecoverableLength(normal, 4096) {
		t.Error("non-length stops are not recoverable")
	}
	if IsRecoverableLength(nil, 4096) {
		t.Error("nil is not recoverable")
	}
}

func TestGetOverflowPatterns(t *testing.T) {
	patterns := GetOverflowPatterns()
	if len(patterns) == 0 {
		t.Fatal("patterns must not be empty")
	}
	// The returned slice is a copy.
	patterns[0] = nil
	if GetOverflowPatterns()[0] == nil {
		t.Fatal("patterns must be copied")
	}
	found := false
	for _, pattern := range GetOverflowPatterns() {
		if strings.Contains(pattern.String(), "too many tokens") {
			found = true
		}
	}
	if !found {
		t.Fatal("generic fallback pattern missing")
	}
}
