package ai

import "fmt"

// Port of api/simple-options.ts.

const (
	contextSafetyTokens = 4096
	minMaxTokens        = 1

	// MinAnswerTokens are always left for the answer when a thinking budget
	// shares the response ceiling.
	MinAnswerTokens = 1024
)

// ClampMaxTokensToContext clamps a maxTokens value to what fits in the
// context window alongside the current transcript.
func ClampMaxTokensToContext(model *Model, context TranscriptContext, maxTokens int) int {
	if model.ContextWindow <= 0 {
		return max(minMaxTokens, maxTokens)
	}
	available := int(model.ContextWindow) - EstimateContextTokens(context.Messages).Tokens - contextSafetyTokens
	return min(maxTokens, max(minMaxTokens, available))
}

// DefaultThinkingBudgets are the per-level thinking token budgets.
var DefaultThinkingBudgets = ThinkingBudgets{
	Minimal: intPtr(1024),
	Low:     intPtr(2048),
	Medium:  intPtr(8192),
	High:    intPtr(16384),
}

func intPtr(n int) *int { return &n }

// ClampReasoning maps xhigh/max down to high for budget-based thinking.
func ClampReasoning(effort ThinkingLevel) ThinkingLevel {
	if effort == ThinkXHigh || effort == ThinkMax {
		return ThinkHigh
	}
	return effort
}

// ThinkingBudgetForLevel resolves the thinking budget for a level.
func ThinkingBudgetForLevel(reasoningLevel ThinkingLevel, customBudgets *ThinkingBudgets) int {
	budgetFor := func(level ThinkingLevel) *int {
		if customBudgets == nil {
			switch level {
			case ThinkMinimal:
				return DefaultThinkingBudgets.Minimal
			case ThinkLow:
				return DefaultThinkingBudgets.Low
			case ThinkMedium:
				return DefaultThinkingBudgets.Medium
			case ThinkHigh:
				return DefaultThinkingBudgets.High
			}
			return nil
		}
		switch level {
		case ThinkMinimal:
			if customBudgets.Minimal != nil {
				return customBudgets.Minimal
			}
			return DefaultThinkingBudgets.Minimal
		case ThinkLow:
			if customBudgets.Low != nil {
				return customBudgets.Low
			}
			return DefaultThinkingBudgets.Low
		case ThinkMedium:
			if customBudgets.Medium != nil {
				return customBudgets.Medium
			}
			return DefaultThinkingBudgets.Medium
		case ThinkHigh:
			if customBudgets.High != nil {
				return customBudgets.High
			}
			return DefaultThinkingBudgets.High
		}
		return nil
	}
	level := ClampReasoning(reasoningLevel)
	if b := budgetFor(level); b != nil {
		return *b
	}
	if b := budgetFor(ThinkHigh); b != nil {
		return *b
	}
	return 1024
}

// ClampThinkingBudgetToAnswerRoom caps a thinking budget so at least
// MinAnswerTokens remain under a shared response ceiling.
func ClampThinkingBudgetToAnswerRoom(thinkingBudget, ceiling int) int {
	return min(thinkingBudget, max(0, ceiling-MinAnswerTokens))
}

// AdjustMaxTokensForThinking fits a thinking budget under a maxTokens cap.
// A nil baseMaxTokens means no explicit caller cap: use the model cap and
// fit thinking inside it.
func AdjustMaxTokensForThinking(baseMaxTokens *int, modelMaxTokens int, reasoningLevel ThinkingLevel, customBudgets *ThinkingBudgets) (maxTokens, thinkingBudget int) {
	thinkingBudget = ThinkingBudgetForLevel(reasoningLevel, customBudgets)
	if baseMaxTokens == nil {
		maxTokens = modelMaxTokens
	} else {
		maxTokens = min(*baseMaxTokens+thinkingBudget, modelMaxTokens)
	}
	if maxTokens <= thinkingBudget {
		thinkingBudget = ClampThinkingBudgetToAnswerRoom(thinkingBudget, maxTokens)
	}
	return maxTokens, thinkingBudget
}

var _ = fmt.Sprintf
