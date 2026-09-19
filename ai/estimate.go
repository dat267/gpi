package ai

import (
	"math"
)

// Port of utils/estimate.ts.

const (
	charsPerToken       = 4
	estimatedImageChars = 4800
)

// ContextUsageEstimate is the context size estimate.
type ContextUsageEstimate struct {
	// Tokens is the estimated total context tokens.
	Tokens int
	// UsageTokens is from the most recent applicable assistant usage block.
	UsageTokens int
	// TrailingTokens is estimated tokens after that usage block.
	TrailingTokens int
	// LastUsageIndex is the applicable message index, or -1 when none exists.
	LastUsageIndex int
}

// CalculateContextTokens of a usage block.
func CalculateContextTokens(usage Usage) int {
	if usage.TotalTokens != 0 {
		return int(usage.TotalTokens)
	}
	return int(usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite)
}

func safeJSONStringify(v any) string {
	enc, err := MarshalJSON(v)
	if err != nil {
		return "[unserializable]"
	}
	return string(enc)
}

// EstimateTextTokens of a text.
func EstimateTextTokens(text string) int {
	return ceilDiv(JSLength(text), charsPerToken)
}

func ceilDiv(a, b int) int {
	return (a + b - 1) / b
}

// estimateTextAndImageContentChars of user/tool-result content.
func estimateTextAndImageContentChars(content StringOrBlocks) int {
	if content.Blocks == nil {
		return JSLength(content.Text)
	}
	chars := 0
	for _, block := range content.Blocks {
		switch b := block.(type) {
		case TextContent:
			chars += JSLength(b.Text)
		case ImageContent:
			chars += estimatedImageChars
		}
	}
	return chars
}

// EstimateTextAndImageContentTokens of user/tool-result content.
func EstimateTextAndImageContentTokens(content StringOrBlocks) int {
	return ceilDiv(estimateTextAndImageContentChars(content), charsPerToken)
}

// EstimateMessageTokens of one transcript message.
func EstimateMessageTokens(message Message) int {
	switch m := message.(type) {
	case *SystemMessage:
		tokens := EstimateTextTokens(GetSystemMessageText(m))
		tokens += estimateToolsTokens(m.ToolsAdded)
		tokens += estimateToolsTokens(m.ToolsRemoved)
		return tokens
	case *UserMessage:
		return EstimateTextAndImageContentTokens(m.Content)
	case *ToolResultMessage:
		return EstimateTextAndImageContentTokens(StringOrBlocks{Blocks: userToContentList(m.Content)})
	case *AssistantMessage:
		chars := 0
		for _, block := range m.Content {
			switch b := block.(type) {
			case TextContent:
				chars += JSLength(b.Text)
			case ThinkingContent:
				chars += JSLength(b.Thinking)
			case ToolCall:
				chars += JSLength(b.Name) + len(safeJSONStringify(b.Arguments))
			}
		}
		return ceilDiv(chars, charsPerToken)
	default:
		return 0
	}
}

func userToContentList(content UserContentList) ContentList {
	out := make(ContentList, 0, len(content))
	for _, c := range content {
		out = append(out, c)
	}
	return out
}

// estimateToolsTokens of tool declarations or references (upstream
// JSON.stringifies either shape).
func estimateToolsTokens(tools any) int {
	switch v := tools.(type) {
	case []Tool:
		if len(v) == 0 {
			return 0
		}
		return EstimateTextTokens(safeJSONStringify(v))
	case []ToolReference:
		if len(v) == 0 {
			return 0
		}
		return EstimateTextTokens(safeJSONStringify(v))
	default:
		return 0
	}
}

// getLastAssistantUsageInfo finds the most recent applicable assistant usage:
// a newer prefix message after the response (e.g. a compaction summary)
// invalidates its usage.
func getLastAssistantUsageInfo(messages []Message) (Usage, int) {
	latestPrefixTimestamp := math.Inf(-1)
	var usage Usage
	index := -1
	for i, message := range messages {
		if assistant, ok := message.(*AssistantMessage); ok {
			usageAppliesToPrefix := float64(assistant.Timestamp) >= latestPrefixTimestamp
			if usageAppliesToPrefix &&
				assistant.StopReason != StopAborted &&
				assistant.StopReason != StopError &&
				CalculateContextTokens(assistant.Usage) > 0 {
				usage = assistant.Usage
				index = i
			}
		}
		ts := messageTimestamp(message)
		if float64(ts) > latestPrefixTimestamp {
			latestPrefixTimestamp = float64(ts)
		}
	}
	return usage, index
}

func messageTimestamp(m Message) int64 {
	switch v := m.(type) {
	case *SystemMessage:
		return v.Timestamp
	case *UserMessage:
		return v.Timestamp
	case *AssistantMessage:
		return v.Timestamp
	case *ToolResultMessage:
		return v.Timestamp
	default:
		return 0
	}
}

// EstimateContextTokens estimates the total context tokens of a transcript.
func EstimateContextTokens(messages []Message) ContextUsageEstimate {
	usage, index := getLastAssistantUsageInfo(messages)
	if index >= 0 {
		usageTokens := CalculateContextTokens(usage)
		trailing := 0
		for i := index + 1; i < len(messages); i++ {
			trailing += EstimateMessageTokens(messages[i])
		}
		return ContextUsageEstimate{
			Tokens: usageTokens + trailing, UsageTokens: usageTokens,
			TrailingTokens: trailing, LastUsageIndex: index,
		}
	}
	tokens := 0
	for _, message := range messages {
		tokens += EstimateMessageTokens(message)
	}
	return ContextUsageEstimate{Tokens: tokens, UsageTokens: 0, TrailingTokens: tokens, LastUsageIndex: -1}
}
