package coding

import (
	"context"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
)

// Tests ported from packages/coding-agent/test/model-resolver.test.ts.

func testModel(id, name string, provider ai.ProviderId, reasoning bool) *ai.Model {
	return &ai.Model{
		ID:            id,
		Name:          name,
		API:           "anthropic-messages",
		Provider:      provider,
		BaseURL:       "https://example.invalid",
		Reasoning:     reasoning,
		Input:         []string{"text", "image"},
		ContextWindow: 128000,
		MaxTokens:     8192,
	}
}

func mockModels() []*ai.Model {
	return []*ai.Model{
		testModel("claude-sonnet-4-5", "Claude Sonnet 4.5", "anthropic", true),
		testModel("gpt-4o", "GPT-4o", "openai", false),
	}
}

func mockOpenRouterModels() []*ai.Model {
	return []*ai.Model{
		testModel("qwen/qwen3-coder:exacto", "Qwen3 Coder Exacto", "openrouter", true),
		testModel("openai/gpt-4o:extended", "GPT-4o Extended", "openrouter", false),
	}
}

func allMockModels() []*ai.Model {
	return append(mockModels(), mockOpenRouterModels()...)
}

// fakeRuntime is the per-test model runtime (upstream builds partial registry
// objects per test).
type fakeRuntime struct {
	models              []*ai.Model
	available           []*ai.Model
	configuredProviders map[string]bool
	getModelFn          func(provider, modelID string) *ai.Model
}

func (r *fakeRuntime) GetModels(providerID string) []*ai.Model {
	if providerID == "" {
		return r.models
	}
	var out []*ai.Model
	for _, model := range r.models {
		if model.Provider == providerID {
			out = append(out, model)
		}
	}
	return out
}

func (r *fakeRuntime) GetModel(providerID, modelID string) *ai.Model {
	if r.getModelFn != nil {
		return r.getModelFn(providerID, modelID)
	}
	for _, model := range r.models {
		if model.Provider == providerID && model.ID == modelID {
			return model
		}
	}
	return nil
}

func (r *fakeRuntime) GetAvailable(providerID string, ctx context.Context) ([]*ai.Model, error) {
	if r.available != nil {
		return r.available, nil
	}
	return r.models, nil
}

func (r *fakeRuntime) GetAvailableSnapshot() []*ai.Model {
	if r.available != nil {
		return r.available
	}
	return r.models
}

func (r *fakeRuntime) HasConfiguredAuth(providerID string) bool {
	return r.configuredProviders[providerID]
}

func TestParseModelPattern(t *testing.T) {
	models := allMockModels()

	// Exact match.
	result := ParseModelPattern("claude-sonnet-4-5", models, nil)
	if result.Model == nil || result.Model.ID != "claude-sonnet-4-5" || result.HasThinking || result.Warning != "" {
		t.Fatalf("exact match = %+v", result)
	}
	// Partial match.
	result = ParseModelPattern("sonnet", models, nil)
	if result.Model == nil || result.Model.ID != "claude-sonnet-4-5" || result.HasThinking || result.Warning != "" {
		t.Fatalf("partial match = %+v", result)
	}
	// No match.
	result = ParseModelPattern("nonexistent", models, nil)
	if result.Model != nil || result.HasThinking || result.Warning != "" {
		t.Fatalf("no match = %+v", result)
	}

	// Valid thinking levels.
	result = ParseModelPattern("sonnet:high", models, nil)
	if result.Model == nil || result.Model.ID != "claude-sonnet-4-5" ||
		!result.HasThinking || result.ThinkingLevel != ai.ThinkHigh || result.Warning != "" {
		t.Fatalf("sonnet:high = %+v", result)
	}
	result = ParseModelPattern("gpt-4o:medium", models, nil)
	if result.Model == nil || result.Model.ID != "gpt-4o" || result.ThinkingLevel != ai.ThinkMedium || result.Warning != "" {
		t.Fatalf("gpt-4o:medium = %+v", result)
	}
	for _, level := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
		result = ParseModelPattern("sonnet:"+level, models, nil)
		if result.Model == nil || result.Model.ID != "claude-sonnet-4-5" ||
			!result.HasThinking || string(result.ThinkingLevel) != level || result.Warning != "" {
			t.Fatalf("sonnet:%s = %+v", level, result)
		}
	}

	// Invalid thinking levels warn and keep the model.
	result = ParseModelPattern("sonnet:random", models, nil)
	if result.Model == nil || result.Model.ID != "claude-sonnet-4-5" || result.HasThinking {
		t.Fatalf("sonnet:random = %+v", result)
	}
	if !strings.Contains(result.Warning, "Invalid thinking level") || !strings.Contains(result.Warning, "random") {
		t.Fatalf("warning = %q", result.Warning)
	}
	result = ParseModelPattern("gpt-4o:invalid", models, nil)
	if result.Model == nil || result.Model.ID != "gpt-4o" || !strings.Contains(result.Warning, "Invalid thinking level") {
		t.Fatalf("gpt-4o:invalid = %+v", result)
	}

	// Colons inside model ids.
	result = ParseModelPattern("qwen/qwen3-coder:exacto", models, nil)
	if result.Model == nil || result.Model.ID != "qwen/qwen3-coder:exacto" || result.HasThinking || result.Warning != "" {
		t.Fatalf("qwen exacto = %+v", result)
	}
	result = ParseModelPattern("openrouter/qwen/qwen3-coder:exacto", models, nil)
	if result.Model == nil || result.Model.ID != "qwen/qwen3-coder:exacto" || result.Model.Provider != "openrouter" {
		t.Fatalf("provider-prefixed exacto = %+v", result)
	}
	result = ParseModelPattern("qwen/qwen3-coder:exacto:high", models, nil)
	if result.Model == nil || result.Model.ID != "qwen/qwen3-coder:exacto" || result.ThinkingLevel != ai.ThinkHigh {
		t.Fatalf("exacto:high = %+v", result)
	}
	result = ParseModelPattern("openrouter/qwen/qwen3-coder:exacto:high", models, nil)
	if result.Model == nil || result.Model.Provider != "openrouter" || result.ThinkingLevel != ai.ThinkHigh {
		t.Fatalf("provider exacto:high = %+v", result)
	}
	result = ParseModelPattern("openai/gpt-4o:extended", models, nil)
	if result.Model == nil || result.Model.ID != "openai/gpt-4o:extended" || result.HasThinking || result.Warning != "" {
		t.Fatalf("extended = %+v", result)
	}

	// Invalid suffixes after colons in model ids.
	result = ParseModelPattern("qwen/qwen3-coder:exacto:random", models, nil)
	if result.Model == nil || result.Model.ID != "qwen/qwen3-coder:exacto" || result.HasThinking ||
		!strings.Contains(result.Warning, "Invalid thinking level") || !strings.Contains(result.Warning, "random") {
		t.Fatalf("exacto:random = %+v", result)
	}
	result = ParseModelPattern("qwen/qwen3-coder:exacto:high:random", models, nil)
	if result.Model == nil || result.Model.ID != "qwen/qwen3-coder:exacto" || result.HasThinking ||
		!strings.Contains(result.Warning, "random") {
		t.Fatalf("exacto:high:random = %+v", result)
	}

	// Edge cases: an empty pattern matches through partial matching, and an
	// empty suffix is an invalid thinking level.
	result = ParseModelPattern("", models, nil)
	if result.Model == nil || result.HasThinking {
		t.Fatalf("empty pattern = %+v", result)
	}
	result = ParseModelPattern("sonnet:", models, nil)
	if result.Model == nil || result.Model.ID != "claude-sonnet-4-5" || !strings.Contains(result.Warning, "Invalid thinking level") {
		t.Fatalf("sonnet: = %+v", result)
	}

	// strict mode (CLI --model) rejects an invalid suffix instead of stripping it.
	strict := false
	result = ParseModelPattern("gpt-4o:invalid", models, &ParseModelPatternOptions{AllowInvalidThinkingLevelFallback: &strict})
	if result.Model != nil || result.Warning != "" {
		t.Fatalf("strict invalid suffix = %+v", result)
	}
}

func TestResolveModelScopeDiagnostics(t *testing.T) {
	result := ResolveModelScopeFromModels([]string{"sonnet:high", "gpt-4o:invalid", "missing"}, allMockModels())
	if len(result.ScopedModels) != 2 ||
		result.ScopedModels[0].Model.ID != "claude-sonnet-4-5" ||
		result.ScopedModels[1].Model.ID != "gpt-4o" {
		t.Fatalf("scoped = %+v", result.ScopedModels)
	}
	if !result.ScopedModels[0].HasThinking || result.ScopedModels[0].ThinkingLevel != ai.ThinkHigh {
		t.Fatalf("first scope = %+v", result.ScopedModels[0])
	}
	if result.ScopedModels[1].HasThinking {
		t.Fatalf("second scope = %+v", result.ScopedModels[1])
	}
	if len(result.Diagnostics) != 2 {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
	wantFirst := `Invalid thinking level "invalid" in pattern "gpt-4o:invalid". Using default instead.`
	if result.Diagnostics[0].Code != "invalid-thinking-level" || result.Diagnostics[0].Message != wantFirst ||
		result.Diagnostics[0].Pattern != "gpt-4o:invalid" || result.Diagnostics[0].Type != "warning" {
		t.Fatalf("diagnostic 0 = %+v", result.Diagnostics[0])
	}
	if result.Diagnostics[1].Code != "no-match" || result.Diagnostics[1].Message != `No models match pattern "missing"` {
		t.Fatalf("diagnostic 1 = %+v", result.Diagnostics[1])
	}

	// A glob pattern with no match reports no-match.
	globResult := ResolveModelScopeFromModels([]string{"nothing*"}, allMockModels())
	if len(globResult.ScopedModels) != 0 || len(globResult.Diagnostics) != 1 ||
		globResult.Diagnostics[0].Code != "no-match" {
		t.Fatalf("glob result = %+v", globResult)
	}

	// Glob patterns match provider/model and bare ids (nocase), with an
	// optional thinking suffix; duplicates collapse.
	globResult = ResolveModelScopeFromModels([]string{"anthropic/*:high", "anthropic/claude-sonnet-4-5", "sonnet"}, allMockModels())
	if len(globResult.ScopedModels) != 1 || globResult.ScopedModels[0].Model.ID != "claude-sonnet-4-5" {
		t.Fatalf("glob scoped = %+v", globResult.ScopedModels)
	}
	if globResult.ScopedModels[0].ThinkingLevel != ai.ThinkHigh {
		t.Fatalf("glob thinking = %+v", globResult.ScopedModels[0])
	}
	// The bare-id form matches without requiring a provider prefix.
	globResult = ResolveModelScopeFromModels([]string{"*sonnet*"}, allMockModels())
	if len(globResult.ScopedModels) != 1 || globResult.ScopedModels[0].Model.ID != "claude-sonnet-4-5" {
		t.Fatalf("bare glob = %+v", globResult.ScopedModels)
	}

	// A bracketed model id resolves as an exact reference before glob matching.
	bracketed := testModel("bracketed-model[1m]", "Bracketed Model", "custom", true)
	withBracketed := append(allMockModels(), bracketed)
	globResult = ResolveModelScopeFromModels([]string{"custom/bracketed-model[1m]"}, withBracketed)
	if len(globResult.ScopedModels) != 1 || globResult.ScopedModels[0].Model.ID != "bracketed-model[1m]" ||
		len(globResult.Diagnostics) != 0 {
		t.Fatalf("bracketed = %+v", globResult)
	}
	globResult = ResolveModelScopeFromModels([]string{"custom/bracketed-model[1m]:high"}, withBracketed)
	if len(globResult.ScopedModels) != 1 || globResult.ScopedModels[0].ThinkingLevel != ai.ThinkHigh ||
		len(globResult.Diagnostics) != 0 {
		t.Fatalf("bracketed thinking = %+v", globResult)
	}
}

func TestResolveCliModelCases(t *testing.T) {
	runtime := &fakeRuntime{models: allMockModels()}

	// --model provider/id without --provider.
	result := ResolveCliModel(ResolveCliModelOptions{CLIModel: "openai/gpt-4o", ModelRuntime: runtime})
	if result.Error != "" || result.Model == nil || result.Model.Provider != "openai" || result.Model.ID != "gpt-4o" {
		t.Fatalf("provider/id = %+v", result)
	}
	// Fuzzy patterns within an explicit provider.
	result = ResolveCliModel(ResolveCliModelOptions{CLIProvider: "openai", CLIModel: "4o", ModelRuntime: runtime})
	if result.Error != "" || result.Model == nil || result.Model.ID != "gpt-4o" {
		t.Fatalf("fuzzy = %+v", result)
	}
	// pattern:thinking.
	result = ResolveCliModel(ResolveCliModelOptions{CLIModel: "sonnet:high", ModelRuntime: runtime})
	if result.Error != "" || result.Model == nil || result.Model.ID != "claude-sonnet-4-5" ||
		result.ThinkingLevel != ai.ThinkHigh || !result.HasThinking {
		t.Fatalf("thinking = %+v", result)
	}
	// Exact model ids win over provider inference (OpenRouter-style ids).
	result = ResolveCliModel(ResolveCliModelOptions{CLIModel: "openai/gpt-4o:extended", ModelRuntime: runtime})
	if result.Error != "" || result.Model == nil || result.Model.Provider != "openrouter" ||
		result.Model.ID != "openai/gpt-4o:extended" {
		t.Fatalf("openrouter id = %+v", result)
	}
	// An invalid suffix stays part of the id for an explicit provider.
	result = ResolveCliModel(ResolveCliModelOptions{CLIProvider: "openai", CLIModel: "gpt-4o:extended", ModelRuntime: runtime})
	if result.Error != "" || result.Model == nil || result.Model.Provider != "openai" || result.Model.ID != "gpt-4o:extended" {
		t.Fatalf("raw suffix = %+v", result)
	}
	// Custom model ids for explicit providers are not double-prefixed.
	result = ResolveCliModel(ResolveCliModelOptions{CLIProvider: "openrouter", CLIModel: "openrouter/openai/ghost-model", ModelRuntime: runtime})
	if result.Error != "" || result.Model == nil || result.Model.Provider != "openrouter" ||
		result.Model.ID != "openai/ghost-model" {
		t.Fatalf("custom id = %+v", result)
	}
	// No models available.
	result = ResolveCliModel(ResolveCliModelOptions{CLIProvider: "openai", CLIModel: "gpt-4o", ModelRuntime: &fakeRuntime{}})
	if result.Model != nil || !strings.Contains(result.Error, "No models available") {
		t.Fatalf("no models = %+v", result)
	}
	// Unknown provider.
	result = ResolveCliModel(ResolveCliModelOptions{CLIProvider: "nope", CLIModel: "x", ModelRuntime: runtime})
	if result.Model != nil || !strings.Contains(result.Error, `Unknown provider "nope"`) {
		t.Fatalf("unknown provider = %+v", result)
	}
	// An unparsable pattern reports the display form.
	result = ResolveCliModel(ResolveCliModelOptions{CLIModel: "ghost", ModelRuntime: runtime})
	if result.Model != nil || !strings.Contains(result.Error, `Model "ghost" not found`) {
		t.Fatalf("not found = %+v", result)
	}

	// Provider-prefixed fuzzy pattern.
	result = ResolveCliModel(ResolveCliModelOptions{CLIModel: "openrouter/qwen", ModelRuntime: runtime})
	if result.Error != "" || result.Model == nil || result.Model.Provider != "openrouter" ||
		result.Model.ID != "qwen/qwen3-coder:exacto" {
		t.Fatalf("provider fuzzy = %+v", result)
	}

	// Ambiguous bare exact ids need a unique authenticated provider.
	azure := testModel("gpt-5.6-sol", "GPT 5.6 Sol", "azure-openai-responses", false)
	codex := testModel("gpt-5.6-sol", "GPT 5.6 Sol", "openai-codex", false)
	ambiguous := &fakeRuntime{models: []*ai.Model{azure, codex}, configuredProviders: map[string]bool{"openai-codex": true}}
	result = ResolveCliModel(ResolveCliModelOptions{CLIModel: "gpt-5.6-sol", ModelRuntime: ambiguous})
	if result.Error != "" || result.Model == nil || result.Model.Provider != "openai-codex" {
		t.Fatalf("unique auth = %+v", result)
	}
	ambiguousBoth := &fakeRuntime{models: []*ai.Model{azure, codex}}
	result = ResolveCliModel(ResolveCliModelOptions{CLIModel: "gpt-5.6-sol", ModelRuntime: ambiguousBoth})
	if result.Model != nil || !strings.Contains(result.Error, `Model "gpt-5.6-sol" is ambiguous across providers`) ||
		!strings.Contains(result.Error, "azure-openai-responses/gpt-5.6-sol") ||
		!strings.Contains(result.Error, "openai-codex/gpt-5.6-sol") ||
		!strings.Contains(result.Error, "Use --provider or provider/model") {
		t.Fatalf("ambiguous = %+v", result)
	}

	// provider/model split wins over a gateway model whose id matches.
	zai := testModel("glm-5", "GLM-5", "zai", true)
	gateway := testModel("zai/glm-5", "GLM-5", "vercel-ai-gateway", true)
	split := &fakeRuntime{models: append(allMockModels(), zai, gateway), configuredProviders: map[string]bool{"zai": true, "vercel-ai-gateway": true}}
	result = ResolveCliModel(ResolveCliModelOptions{CLIModel: "zai/glm-5", ModelRuntime: split})
	if result.Error != "" || result.Model == nil || result.Model.Provider != "zai" || result.Model.ID != "glm-5" {
		t.Fatalf("split preference = %+v", result)
	}

	// An authenticated exact raw id beats an unauthenticated inferred provider.
	commandcode := testModel("xiaomi/mimo-v2.5-pro", "Xiaomi MiMo via Commandcode", "commandcode", false)
	xiaomi := testModel("mimo-v2.5-pro", "Xiaomi MiMo", "xiaomi", false)
	rawID := &fakeRuntime{
		models:              append(allMockModels(), commandcode, xiaomi),
		configuredProviders: map[string]bool{"commandcode": true},
	}
	result = ResolveCliModel(ResolveCliModelOptions{CLIModel: "xiaomi/mimo-v2.5-pro", ModelRuntime: rawID})
	if result.Error != "" || result.Model == nil || result.Model.Provider != "commandcode" ||
		result.Model.ID != "xiaomi/mimo-v2.5-pro" {
		t.Fatalf("raw id preference = %+v", result)
	}
}

func TestResolveCliModelCustomFallback(t *testing.T) {
	neuralwatt := testModel("some-base-model", "Some Base Model", "neuralwatt", false)
	runtime := &fakeRuntime{models: append(allMockModels(), neuralwatt)}

	// The :thinking suffix must not leak into the custom model id.
	result := ResolveCliModel(ResolveCliModelOptions{CLIModel: "neuralwatt/zai-org/GLM-5.1-FP8:high", ModelRuntime: runtime})
	if result.Error != "" || result.Model == nil || result.Model.Provider != "neuralwatt" ||
		result.Model.ID != "zai-org/GLM-5.1-FP8" || !result.Model.Reasoning ||
		result.ThinkingLevel != ai.ThinkHigh || !result.HasThinking {
		t.Fatalf("fallback thinking = %+v", result)
	}
	if !strings.Contains(result.Warning, "Using custom model id") {
		t.Fatalf("warning = %q", result.Warning)
	}

	result = ResolveCliModel(ResolveCliModelOptions{CLIModel: "neuralwatt/zai-org/GLM-5.1-FP8", ModelRuntime: runtime})
	if result.Error != "" || result.Model == nil || result.Model.ID != "zai-org/GLM-5.1-FP8" || result.HasThinking {
		t.Fatalf("fallback plain = %+v", result)
	}

	for _, level := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
		result = ResolveCliModel(ResolveCliModelOptions{CLIModel: "neuralwatt/zai-org/GLM-5.1-FP8:" + level, ModelRuntime: runtime})
		if result.Error != "" || result.Model == nil || result.Model.ID != "zai-org/GLM-5.1-FP8" ||
			string(result.ThinkingLevel) != level {
			t.Fatalf("fallback %s = %+v", level, result)
		}
	}

	// An invalid suffix stays in the id.
	result = ResolveCliModel(ResolveCliModelOptions{CLIModel: "neuralwatt/zai-org/GLM-5.1-FP8:banana", ModelRuntime: runtime})
	if result.Error != "" || result.Model == nil || result.Model.ID != "zai-org/GLM-5.1-FP8:banana" || result.HasThinking {
		t.Fatalf("fallback invalid = %+v", result)
	}

	// An explicit provider strips the suffix the same way.
	result = ResolveCliModel(ResolveCliModelOptions{CLIProvider: "neuralwatt", CLIModel: "zai-org/GLM-5.1-FP8:high", ModelRuntime: runtime})
	if result.Error != "" || result.Model == nil || result.Model.ID != "zai-org/GLM-5.1-FP8" ||
		result.ThinkingLevel != ai.ThinkHigh {
		t.Fatalf("explicit provider fallback = %+v", result)
	}

	// With explicit --thinking the suffix stays part of the id.
	result = ResolveCliModel(ResolveCliModelOptions{
		CLIModel:    "neuralwatt/zai-org/GLM-5.1-FP8:high",
		CLIThinking: ai.ThinkMedium, HasCLIThinking: true,
		ModelRuntime: runtime,
	})
	if result.Error != "" || result.Model == nil || result.Model.ID != "zai-org/GLM-5.1-FP8:high" || result.HasThinking {
		t.Fatalf("explicit thinking = %+v", result)
	}
}

func TestDefaultModelPerProviderTracksCurrentModels(t *testing.T) {
	expectations := map[ai.ProviderId]string{
		"openai":                     "gpt-5.5",
		"openai-codex":               "gpt-5.5",
		"zai":                        "glm-5.3",
		"zai-coding-cn":              "glm-5.3",
		"minimax":                    "MiniMax-M2.7",
		"minimax-cn":                 "MiniMax-M2.7",
		"cerebras":                   "gpt-oss-120b",
		"ant-ling":                   "Ring-2.6-1T",
		"vercel-ai-gateway":          "zai/glm-5.1",
		"xai":                        "grok-4.6",
		"qwen-token-plan-individual": "qwen3.8-max",
		"meta":                       "muse-spark-1.3",
	}
	for provider, want := range expectations {
		if got := DefaultModelPerProvider[provider]; got != want {
			t.Fatalf("%s default = %s, want %s", provider, got, want)
		}
	}
	if len(DefaultProviderOrder) != len(DefaultModelPerProvider) {
		t.Fatalf("provider order has %d entries for %d defaults", len(DefaultProviderOrder), len(DefaultModelPerProvider))
	}
	for _, provider := range DefaultProviderOrder {
		if _, ok := DefaultModelPerProvider[provider]; !ok {
			t.Fatalf("provider order lists %s with no default", provider)
		}
	}
}

func TestFindInitialModelSelection(t *testing.T) {
	// Explicit provider + custom model id.
	runtime := &fakeRuntime{models: allMockModels()}
	result := FindInitialModel(FindInitialModelOptions{
		CLIProvider: "openrouter", CLIModel: "openrouter/openai/ghost-model", ModelRuntime: runtime,
	})
	if result.Model == nil || result.Model.Provider != "openrouter" || result.Model.ID != "openai/ghost-model" {
		t.Fatalf("explicit = %+v", result)
	}

	// The first scoped model wins with its thinking level.
	scopedRuntime := &fakeRuntime{}
	result = FindInitialModel(FindInitialModelOptions{
		ScopedModels: []ScopedModel{{Model: mockModels()[0], ThinkingLevel: ai.ThinkHigh, HasThinking: true}},
		ModelRuntime: scopedRuntime,
	})
	if result.Model == nil || result.Model.ID != "claude-sonnet-4-5" || result.ThinkingLevel != ai.ThinkHigh {
		t.Fatalf("scoped = %+v", result)
	}
	// Per-model thinking levels apply when the scope has none.
	result = FindInitialModel(FindInitialModelOptions{
		ScopedModels:        []ScopedModel{{Model: mockModels()[0]}},
		ModelThinkingLevels: map[string]ai.ThinkingLevel{"anthropic/claude-sonnet-4-5": ai.ThinkLow},
		ModelRuntime:        scopedRuntime,
	})
	if result.ThinkingLevel != ai.ThinkLow {
		t.Fatalf("per-model thinking = %+v", result)
	}
	// Continuing sessions skip the scoped list.
	result = FindInitialModel(FindInitialModelOptions{
		ScopedModels: []ScopedModel{{Model: mockModels()[0], ThinkingLevel: ai.ThinkHigh, HasThinking: true}},
		IsContinuing: true,
		ModelRuntime: &fakeRuntime{available: []*ai.Model{}},
	})
	if result.Model != nil {
		t.Fatalf("continuing = %+v", result)
	}

	// The saved default is used when its auth is configured.
	deepseek := testModel("deepseek-v4-flash", "DeepSeek V4 Flash", "deepseek", true)
	local := testModel("deepseek-v4-flash", "DeepSeek V4 Flash", "spark-two", true)
	defaultRuntime := &fakeRuntime{
		models:              []*ai.Model{deepseek},
		available:           []*ai.Model{local},
		configuredProviders: map[string]bool{"spark-two": true},
		getModelFn: func(provider, modelID string) *ai.Model {
			if provider == "deepseek" && modelID == "deepseek-v4-flash" {
				return deepseek
			}
			return nil
		},
	}
	result = FindInitialModel(FindInitialModelOptions{
		DefaultProvider: "deepseek", DefaultModelID: "deepseek-v4-flash", ModelRuntime: defaultRuntime,
	})
	if result.Model == nil || result.Model.Provider != "spark-two" {
		t.Fatalf("unauthenticated default = %+v", result)
	}

	// A known provider default wins over the first available model.
	aiGateway := testModel("anthropic/claude-opus-4-6", "Claude Opus 4.6", "vercel-ai-gateway", true)
	result = FindInitialModel(FindInitialModelOptions{
		ModelRuntime: &fakeRuntime{available: []*ai.Model{aiGateway}},
	})
	if result.Model == nil || result.Model.Provider != "vercel-ai-gateway" || result.Model.ID != "anthropic/claude-opus-4-6" {
		t.Fatalf("available default = %+v", result)
	}
	// No availability leaves the model unset with the default thinking level.
	result = FindInitialModel(FindInitialModelOptions{ModelRuntime: &fakeRuntime{available: []*ai.Model{}}})
	if result.Model != nil || result.ThinkingLevel != DefaultThinkingLevel {
		t.Fatalf("no models = %+v", result)
	}

	// An unresolvable explicit pair is reported instead of exiting the process.
	// (A custom id on a known provider resolves through the fallback path, so an
	// unknown provider is the error case.)
	result = FindInitialModel(FindInitialModelOptions{
		CLIProvider: "nope", CLIModel: "ghost",
		ModelRuntime: &fakeRuntime{models: allMockModels()},
	})
	if result.Error == "" || !strings.Contains(result.Error, `Unknown provider "nope"`) {
		t.Fatalf("cli error = %+v", result)
	}
}

func TestRestoreModelFromSession(t *testing.T) {
	saved := testModel("claude-sonnet-4-5", "Claude Sonnet 4.5", "anthropic", true)
	runtime := &fakeRuntime{models: []*ai.Model{saved}, configuredProviders: map[string]bool{"anthropic": true}}
	result := RestoreModelFromSession("anthropic", "claude-sonnet-4-5", nil, runtime)
	if result.Model == nil || result.FallbackMessage != "" {
		t.Fatalf("restored = %+v", result)
	}

	// A missing model falls back to the current model with a message.
	missing := &fakeRuntime{configuredProviders: map[string]bool{"anthropic": true}}
	current := testModel("gpt-4o", "GPT-4o", "openai", false)
	result = RestoreModelFromSession("anthropic", "gone", current, missing)
	if result.Model != current ||
		result.FallbackMessage != "Could not restore model anthropic/gone (model no longer exists). Using openai/gpt-4o." {
		t.Fatalf("fallback to current = %+v", result)
	}

	// Without a current model the first available model is used.
	result = RestoreModelFromSession("anthropic", "gone", nil, &fakeRuntime{available: []*ai.Model{current}})
	if result.Model != current ||
		result.FallbackMessage != "Could not restore model anthropic/gone (model no longer exists). Using openai/gpt-4o." {
		t.Fatalf("fallback to available = %+v", result)
	}

	// An unauthenticated restored model reports the auth reason.
	noAuth := &fakeRuntime{models: []*ai.Model{saved}}
	result = RestoreModelFromSession("anthropic", "claude-sonnet-4-5", current, noAuth)
	if result.Model != current || !strings.Contains(result.FallbackMessage, "(no auth configured)") {
		t.Fatalf("no auth = %+v", result)
	}

	// Nothing available at all.
	result = RestoreModelFromSession("anthropic", "gone", nil, &fakeRuntime{available: []*ai.Model{}})
	if result.Model != nil || result.FallbackMessage != "" {
		t.Fatalf("nothing = %+v", result)
	}
}

func TestMatchGlobSemantics(t *testing.T) {
	cases := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"*sonnet*", "claude-sonnet-4-5", true},
		{"*sonnet*", "anthropic/claude-sonnet-4-5", false}, // "*" does not cross "/"
		{"**sonnet**", "anthropic/claude-sonnet-4-5", true},
		{"anthropic/*", "anthropic/claude-sonnet-4-5", true},
		{"anthropic/*", "openai/gpt-4o", false},
		{"?pt-4o", "gpt-4o", true},
		{"?pt-4o", "gpt-4o-extra", false},
		{"gpt-4[ao]", "gpt-4o", true},
		{"gpt-4[!o]", "gpt-4o", false},
		{"{gpt-4o,sonnet}", "gpt-4o", true},
		{"{gpt-4o,sonnet}", "sonnet", true},
		{"{gpt-4o,sonnet}", "other", false},
		{"GPT-4O", "gpt-4o", true}, // nocase
	}
	for _, testCase := range cases {
		if got := MatchGlob(testCase.pattern, testCase.value, true); got != testCase.want {
			t.Errorf("MatchGlob(%q, %q) = %v, want %v", testCase.pattern, testCase.value, got, testCase.want)
		}
	}
}

func TestIsAliasAndThinkingLevels(t *testing.T) {
	if !IsAlias("claude-sonnet-4-5") || !IsAlias("claude-sonnet-4-5-latest") {
		t.Fatal("aliases must be detected")
	}
	if IsAlias("claude-sonnet-4-5-20250929") {
		t.Fatal("dated versions are not aliases")
	}
	if !IsAlias("claude-sonnet-4-5-2025092") {
		t.Fatal("a non-8-digit suffix is not a date")
	}
	for _, level := range ThinkingLevelOptions {
		if !IsValidThinkingLevel(string(level)) {
			t.Fatalf("level %s must be valid", level)
		}
	}
	if IsValidThinkingLevel("banana") {
		t.Fatal("unknown levels are invalid")
	}
	if DefaultThinkingLevel != ai.ThinkMedium {
		t.Fatalf("default thinking level = %s", DefaultThinkingLevel)
	}
}
