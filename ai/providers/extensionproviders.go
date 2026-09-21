package providers

import "github.com/dat267/gpi/ai"

// Extension providers: Go ports of the user's pi provider extensions
// (~/.pi/agent/extensions/providers). They are registered alongside the
// built-in providers so the settings' default model resolves. Ground truth:
// the extension catalog sources named below.
//
// Port of hyper/catalog.ts + hyper/index.ts (Charm Hyper,
// https://hyper.charm.land/v1, HYPER_API_KEY, openai-completions) and
// commandcode/catalog.ts + commandcode/index.ts (Command Code,
// https://api.commandcode.ai/provider/v1, COMMAND_CODE_API_KEY, Claude models
// over anthropic-messages from the base URL minus /v1).

// ExtensionProviderIDs are the providers added beyond upstream's built-ins.
var ExtensionProviderIDs = []string{"hyper", "commandcode"}

func extBoolPtr(v bool) *bool { return &v }

func extStrPtr(v string) *string { return &v }

// hyperModels is the static Charm Hyper catalog (hyper/catalog.ts buildModels).
func hyperModels() []*ai.Model {
	return []*ai.Model{
		{
			ID: "deepseek-v4-flash", Name: "DeepSeek V4 Flash", API: ai.APIOpenAICompletions, Provider: "hyper",
			BaseURL:          "https://hyper.charm.land/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: nil, ai.ThinkMedium: nil, ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: extStrPtr("xhigh"), ai.ThinkMax: nil},
			Input:            []string{"text"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.2, Output: 0.4, CacheRead: 0.04, CacheWrite: 0.0}},
			ContextWindow:    1000000, MaxTokens: 384000,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "deepseek-v4-pro", Name: "DeepSeek V4 Pro", API: ai.APIOpenAICompletions, Provider: "hyper",
			BaseURL:          "https://hyper.charm.land/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: nil, ai.ThinkMedium: nil, ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: extStrPtr("xhigh"), ai.ThinkMax: nil},
			Input:            []string{"text"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 2.4, Output: 4.8, CacheRead: 0.2, CacheWrite: 0.0}},
			ContextWindow:    1000000, MaxTokens: 384000,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash", API: ai.APIOpenAICompletions, Provider: "hyper",
			BaseURL:          "https://hyper.charm.land/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: nil, ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: extStrPtr("max")},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.3, Output: 1.2, CacheRead: 0.03, CacheWrite: 0.0}},
			ContextWindow:    1000000, MaxTokens: 32768,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "glm-5.3", Name: "GLM-5.3", API: ai.APIOpenAICompletions, Provider: "hyper",
			BaseURL:          "https://hyper.charm.land/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: nil, ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: extStrPtr("max")},
			Input:            []string{"text"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 1.52432, Output: 4.79072, CacheRead: 0.283088, CacheWrite: 0.0}},
			ContextWindow:    1000000, MaxTokens: 128000,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "glm-5.3-flash", Name: "GLM-5.3 Flash", API: ai.APIOpenAICompletions, Provider: "hyper",
			BaseURL:          "https://hyper.charm.land/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: nil, ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: extStrPtr("max")},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.16332, Output: 0.5444, CacheRead: 0.031575, CacheWrite: 0.0}},
			ContextWindow:    1048576, MaxTokens: 131072,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "kimi-k3", Name: "Kimi K3", API: ai.APIOpenAICompletions, Provider: "hyper",
			BaseURL:          "https://hyper.charm.land/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: nil, ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: extStrPtr("max")},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 3.2664, Output: 16.332, CacheRead: 0.32664, CacheWrite: 0.0}},
			ContextWindow:    1048576, MaxTokens: 16000,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "minimax-m3", Name: "MiniMax M3", API: ai.APIOpenAICompletions, Provider: "hyper",
			BaseURL:          "https://hyper.charm.land/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: extStrPtr("medium"), ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: nil},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.32664, Output: 1.30656, CacheRead: 0.064239, CacheWrite: 0.0}},
			ContextWindow:    512000, MaxTokens: 512000,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "qwen3.7-max", Name: "Qwen3.7 Max", API: ai.APIOpenAICompletions, Provider: "hyper",
			BaseURL:          "https://hyper.charm.land/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: extStrPtr("medium"), ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: nil},
			Input:            []string{"text"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 2.5, Output: 7.5, CacheRead: 0.5, CacheWrite: 0.0}},
			ContextWindow:    1000000, MaxTokens: 64000,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "qwen3.7-plus", Name: "Qwen3.7 Plus", API: ai.APIOpenAICompletions, Provider: "hyper",
			BaseURL:          "https://hyper.charm.land/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: extStrPtr("medium"), ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: nil},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 1.2, Output: 4.8, CacheRead: 0.24, CacheWrite: 0.0}},
			ContextWindow:    1000000, MaxTokens: 64000,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "qwen3.8-flash", Name: "Qwen3.8 Flash", API: ai.APIOpenAICompletions, Provider: "hyper",
			BaseURL:          "https://hyper.charm.land/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: extStrPtr("medium"), ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: nil},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.15, Output: 0.47, CacheRead: 0.016, CacheWrite: 0.0}},
			ContextWindow:    1000000, MaxTokens: 128000,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "qwen3.8-max", Name: "Qwen3.8 Max", API: ai.APIOpenAICompletions, Provider: "hyper",
			BaseURL:          "https://hyper.charm.land/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: extStrPtr("medium"), ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: nil},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 2.0, Output: 6.0, CacheRead: 0.25, CacheWrite: 0.0}},
			ContextWindow:    1000000, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
	}
}

// commandCodeModels is the Command Code seed catalog minus the excluded ids
// (commandcode/catalog.ts buildModels + EXCLUDED_IDS).
func commandCodeModels() []*ai.Model {
	return []*ai.Model{
		{
			ID: "gpt-5.6-luna", Name: "GPT-5.6 Luna", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: extStrPtr("medium"), ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: extStrPtr("xhigh"), ai.ThinkMax: extStrPtr("max")},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.2, Output: 1.2, CacheRead: 0.02, CacheWrite: 0.25}},
			ContextWindow:    1050000, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "deepseek/deepseek-v4-pro", Name: "DeepSeek V4 Pro (latest)", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: nil, ai.ThinkMedium: nil, ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: extStrPtr("max")},
			Input:            []string{"text"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.66, Output: 1.98, CacheRead: 0.022, CacheWrite: 0.0}},
			ContextWindow:    1000000, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "deepseek/deepseek-v4-flash", Name: "DeepSeek V4 Flash (latest)", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: nil, ai.ThinkMedium: nil, ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: extStrPtr("max")},
			Input:            []string{"text"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.15, Output: 0.6, CacheRead: 0.003, CacheWrite: 0.0}},
			ContextWindow:    1000000, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "deepseek/deepseek-v4-flash-vision-exp", Name: "DeepSeek V4 Flash Vision (exp)", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: nil, ai.ThinkMedium: nil, ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: extStrPtr("max")},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.15, Output: 0.6, CacheRead: 0.003, CacheWrite: 0.0}},
			ContextWindow:    1000000, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "deepseek/deepseek-v4-flash-fast", Name: "DeepSeek V4 Flash Fast", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: nil, ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: extStrPtr("max")},
			Input:            []string{"text"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.28, Output: 0.56, CacheRead: 0.07, CacheWrite: 0.0}},
			ContextWindow:    1000000, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "deepseek/deepseek-v4.1-flash", Name: "DeepSeek V4.1 Flash", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: nil, ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: extStrPtr("max")},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.15, Output: 0.6, CacheRead: 0.003, CacheWrite: 0.0}},
			ContextWindow:    1000000, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "moonshotai/Kimi-K3", Name: "Kimi K3", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: nil, ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: extStrPtr("max")},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 3.0, Output: 15.0, CacheRead: 0.3, CacheWrite: 0.0}},
			ContextWindow:    1000000, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "z-ai/glm-5.3-flash", Name: "GLM-5.3 Flash", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: nil, ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: extStrPtr("max")},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.15, Output: 0.5, CacheRead: 0.03, CacheWrite: 0.0}},
			ContextWindow:    1048576, MaxTokens: 131072,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "zai-org/GLM-5.3", Name: "GLM-5.3", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: nil, ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: extStrPtr("max")},
			Input:            []string{"text"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 1.4, Output: 4.4, CacheRead: 0.26, CacheWrite: 0.0}},
			ContextWindow:    1000000, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "MiniMaxAI/MiniMax-M3", Name: "MiniMax M3", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: extStrPtr("medium"), ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: nil},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.3, Output: 1.2, CacheRead: 0.06, CacheWrite: 0.0}},
			ContextWindow:    1000000, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "xiaomi/mimo-v2.5-pro", Name: "MiMo V2.5 Pro", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: nil, ai.ThinkMedium: nil, ai.ThinkHigh: nil, ai.ThinkXHigh: nil, ai.ThinkMax: nil},
			Input:            []string{"text"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.435, Output: 0.87, CacheRead: 0.0036, CacheWrite: 0.0}},
			ContextWindow:    1000000, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "Qwen/Qwen3.8-Max", Name: "Qwen 3.8 Max", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: extStrPtr("medium"), ai.ThinkHigh: nil, ai.ThinkXHigh: extStrPtr("xhigh"), ai.ThinkMax: nil},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 2.0, Output: 6.0, CacheRead: 0.25, CacheWrite: 2.5}},
			ContextWindow:    1000000, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "Qwen/Qwen3.8-27B", Name: "Qwen 3.8 27B", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: extStrPtr("medium"), ai.ThinkHigh: nil, ai.ThinkXHigh: extStrPtr("xhigh"), ai.ThinkMax: nil},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.4, Output: 3.0, CacheRead: 0.04, CacheWrite: 0.0}},
			ContextWindow:    262144, MaxTokens: 32768,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "Qwen/Qwen3.8-Flash", Name: "Qwen 3.8 Flash", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: extStrPtr("medium"), ai.ThinkHigh: nil, ai.ThinkXHigh: extStrPtr("xhigh"), ai.ThinkMax: nil},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.16, Output: 0.47, CacheRead: 0.016, CacheWrite: 0.0}},
			ContextWindow:    1000000, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "meituan/LongCat-2.0:free", Name: "LongCat 2.0", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: nil, ai.ThinkMedium: nil, ai.ThinkHigh: nil, ai.ThinkXHigh: nil, ai.ThinkMax: nil},
			Input:            []string{"text"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.0, Output: 0.0, CacheRead: 0.0, CacheWrite: 0.0}},
			ContextWindow:    1048576, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "stepfun/Step-3.7-Flash", Name: "Step 3.7 Flash", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: nil, ai.ThinkMedium: nil, ai.ThinkHigh: nil, ai.ThinkXHigh: nil, ai.ThinkMax: nil},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.2, Output: 1.15, CacheRead: 0.04, CacheWrite: 0.0}},
			ContextWindow:    256000, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "tencent/hy4-preview", Name: "Tencent Hy4 Preview", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: extStrPtr("medium"), ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: nil},
			Input:            []string{"text"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.834, Output: 2.501, CacheRead: 0.042, CacheWrite: 0.0}},
			ContextWindow:    1048576, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "google/gemini-3.8-flash", Name: "Gemini 3.8 Flash", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: extStrPtr("medium"), ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: nil},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 1.5, Output: 7.5, CacheRead: 0.15, CacheWrite: 0.0}},
			ContextWindow:    1000000, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "google/gemini-3.7-flash", Name: "Gemini 3.7 Flash", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: extStrPtr("medium"), ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: nil, ai.ThinkMax: nil},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 1.5, Output: 7.5, CacheRead: 0.15, CacheWrite: 0.08334}},
			ContextWindow:    1048576, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "nvidia/nemotron-3-ultra-550b-a55b", Name: "Nemotron 3 Ultra", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: nil, ai.ThinkMedium: nil, ai.ThinkHigh: nil, ai.ThinkXHigh: nil, ai.ThinkMax: nil},
			Input:            []string{"text"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.6, Output: 2.4, CacheRead: 0.12, CacheWrite: 0.0}},
			ContextWindow:    1000000, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "poolside/laguna-s-2.1-free", Name: "Laguna S 2.1", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: nil, ai.ThinkMedium: nil, ai.ThinkHigh: nil, ai.ThinkXHigh: nil, ai.ThinkMax: nil},
			Input:            []string{"text"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.0, Output: 0.0, CacheRead: 0.0, CacheWrite: 0.0}},
			ContextWindow:    256000, MaxTokens: 32768,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "inclusionai/ling-3.0-flash-sante:free", Name: "Ling 3.0 Flash Sante", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:       "https://api.commandcode.ai/provider/v1",
			Reasoning:     false,
			Input:         []string{"text"},
			Cost:          ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.0, Output: 0.0, CacheRead: 0.0, CacheWrite: 0.0}},
			ContextWindow: 262144, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(false), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "meta/muse-spark-1.3", Name: "Muse Spark 1.3", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: extStrPtr("medium"), ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: extStrPtr("xhigh"), ai.ThinkMax: extStrPtr("max")},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 1.25, Output: 4.25, CacheRead: 0.15, CacheWrite: 0.0}},
			ContextWindow:    1048576, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "meta/muse-spark-1.3-contributor", Name: "Muse Spark 1.3 Contributor", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: extStrPtr("medium"), ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: extStrPtr("xhigh"), ai.ThinkMax: nil},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 0.1, Output: 0.2, CacheRead: 0.002, CacheWrite: 0.0}},
			ContextWindow:    1048576, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
		{
			ID: "xai/grok-4.6", Name: "Grok 4.6", API: ai.APIOpenAICompletions, Provider: "commandcode",
			BaseURL:          "https://api.commandcode.ai/provider/v1",
			Reasoning:        true,
			ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkOff: nil, ai.ThinkMinimal: nil, ai.ThinkLow: extStrPtr("low"), ai.ThinkMedium: extStrPtr("medium"), ai.ThinkHigh: extStrPtr("high"), ai.ThinkXHigh: extStrPtr("xhigh"), ai.ThinkMax: nil},
			Input:            []string{"text", "image"},
			Cost:             ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 2.0, Output: 6.0, CacheRead: 0.5, CacheWrite: 0.0}},
			ContextWindow:    500000, MaxTokens: 65536,
			Compat: &ai.ModelCompat{
				OpenAICompletions: &ai.OpenAICompletionsCompat{SupportsStore: extBoolPtr(false), SupportsDeveloperRole: extBoolPtr(false),
					SupportsReasoningEffort: extBoolPtr(true), MaxTokensField: extStrPtr("max_tokens"), ThinkingFormat: extStrPtr("deepseek")},
			},
		},
	}
}

// commandCodeExcludedIDs are the ids the extension never surfaces
// (UNAVAILABLE_IDS + DOMINATED_IDS).
var commandCodeExcludedIDs = map[string]bool{
	"claude-sonnet-5":                     true,
	"claude-sonnet-4-6":                   true,
	"claude-fable-5-1":                    true,
	"claude-fable-5":                      true,
	"claude-opus-5":                       true,
	"claude-opus-4-8":                     true,
	"claude-opus-4-7":                     true,
	"claude-haiku-4-5-20251001":           true,
	"gpt-5.6-terra":                       true,
	"gpt-5.5":                             true,
	"gpt-5.4":                             true,
	"gpt-5.3-codex":                       true,
	"gpt-5.4-mini":                        true,
	"google/gemini-3.6-flash":             true,
	"google/gemini-3.5-flash":             true,
	"google/gemini-3.5-flash-lite":        true,
	"google/gemini-3.1-flash-lite":        true,
	"sakana/fugu-ultra":                   true,
	"meta/muse-spark-1.1":                 true,
	"thinkingmachines/tinker-70b":         true,
	"zai-org/GLM-5.2-Fast":                true,
	"MiniMaxAI/MiniMax-M2.7":              true,
	"zai-org/GLM-5":                       true,
	"zai-org/GLM-5.1":                     true,
	"zai-org/GLM-5.2":                     true,
	"gpt-5.6-sol":                         true,
	"moonshotai/Kimi-K2.5":                true,
	"moonshotai/Kimi-K2.7-Code":           true,
	"moonshotai/Kimi-K2.7-Code-Highspeed": true,
	"xiaomi/mimo-v2.5":                    true,
	"thinkingmachines/inkling":            true,
	"thinkingmachines/inkling-small":      true,
	"xai/grok-4.5":                        true,
	"MiniMaxAI/MiniMax-M2.5":              true,
	"stepfun/Step-3.5-Flash":              true,
	"meta/muse-spark-1.2":                 true,
	"meta/muse-spark-1.2-contributor":     true,
	"Qwen/Qwen3.6-Max-Preview":            true,
	"Qwen/Qwen3.6-Plus":                   true,
	"Qwen/Qwen3.7-Max":                    true,
	"Qwen/Qwen3.7-Plus":                   true,
	"Qwen/Qwen3.7-Flash":                  true,
	"Qwen/Qwen3.8-Max-0902":               true,
	"moonshotai/Kimi-K2.6":                true,
	"tencent/hy3-paid":                    true,
}

// HyperProvider builds the Charm Hyper provider (hyper/index.ts
// registerCharmHyper).
func HyperProvider() *ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID: "hyper", Name: "Charm Hyper", BaseURL: "https://hyper.charm.land/v1",
		Auth:   ai.ProviderAuth{APIKey: ai.EnvApiKeyAuth("Hyper API key", []string{"HYPER_API_KEY"})},
		Models: hyperModels(),
		Single: openaiCompletionsStreams{},
	})
}

// CommandCodeProvider builds the Command Code provider (commandcode/index.ts
// registerCommandCode): Claude models over anthropic-messages from the base
// URL minus /v1, everything else over openai-completions.
func CommandCodeProvider() *ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID: "commandcode", Name: "Command Code", BaseURL: "https://api.commandcode.ai/provider/v1",
		Auth:   ai.ProviderAuth{APIKey: ai.EnvApiKeyAuth("Command Code API key", []string{"COMMAND_CODE_API_KEY"})},
		Models: commandCodeModels(),
		ByAPI: map[string]ai.ProviderStreams{
			string(ai.APIOpenAICompletions): openaiCompletionsStreams{},
			string(ai.APIAnthropicMessages): anthropicMessagesStreams{},
		},
	})
}

// ExtensionProviders builds every extension provider, in ExtensionProviderIDs
// order.
func ExtensionProviders() []*ai.Provider {
	return []*ai.Provider{HyperProvider(), CommandCodeProvider()}
}
