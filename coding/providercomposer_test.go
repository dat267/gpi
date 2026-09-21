package coding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
)

// Round 99 tests: the model composer (models.json layers) and the api-provider
// registry.

func TestAPIRegistryBuiltins(t *testing.T) {
	apis := []ai.Api{
		ai.APIAnthropicMessages, ai.APIOpenAICompletions, ai.APIOpenAIResponses,
		ai.APIOpenAICodexResponses, ai.APIAzureOpenAIResponses, ai.APIGoogleGenerativeAI,
		ai.APIGoogleVertex, ai.APIMistralConversations, ai.APIBedrockConverse, ai.APIPiMessages,
	}
	for _, api := range apis {
		if ai.GetAPIProvider(api) == nil {
			t.Fatalf("missing built-in implementation for %s", api)
		}
	}
	if ai.GetAPIProvider("not-an-api") != nil {
		t.Fatal("unknown api must have no provider")
	}

	// A mismatched api produces an error stream naming both apis.
	model := &ai.Model{ID: "m", API: ai.APIOpenAICompletions, Provider: "p"}
	stream := ai.GetAPIProvider(ai.APIAnthropicMessages).Stream(model, ai.TranscriptContext{}, nil)
	message, _ := stream.Result(nil)
	if message.StopReason != ai.StopError || message.ErrorMessage == nil ||
		*message.ErrorMessage != "Mismatched api: openai-completions expected anthropic-messages" {
		t.Fatalf("message = %+v", message)
	}

	// Registration and unregistration by source id, without clobbering the
	// built-ins.
	sourceID := "test-source"
	ai.RegisterAPIProvider(ai.APIAnthropicMessages, funcStreams{}, sourceID)
	if ai.GetAPIProvider(ai.APIAnthropicMessages) == nil {
		t.Fatal("registration failed")
	}
	ai.UnregisterAPIProviders(sourceID)
	ai.ResetAPIProviders()
	if ai.GetAPIProvider(ai.APIAnthropicMessages) == nil {
		t.Fatal("built-in lost after reset")
	}
}

func loadConfig(t *testing.T, content string) *ModelConfig {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	config := LoadModelConfig(path)
	if err := config.GetError(); err != "" {
		t.Fatalf("config error = %s", err)
	}
	return config
}

func anthropicBaseModel() *ai.Model {
	return &ai.Model{
		ID: "builtin-model", Name: "Builtin", API: ai.APIAnthropicMessages, Provider: "anthropic",
		BaseURL: "https://api.anthropic.com", Input: []string{"text"},
		Cost:          ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}},
		ContextWindow: 200000, MaxTokens: 8192,
	}
}

func anthropicBaseProvider() *ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID: "anthropic", Name: "Anthropic", BaseURL: "https://api.anthropic.com",
		Auth:   ai.ProviderAuth{APIKey: &ai.ApiKeyAuth{Name: "Anthropic API key"}},
		Models: []*ai.Model{anthropicBaseModel()},
		Single: funcStreams{},
	})
}

func TestApplyModelsJSONCustomModels(t *testing.T) {
	config := &ModelsJSONProvider{
		BaseURL: "https://custom.example.com/v1",
		API:     ai.APIOpenAICompletions,
		Models: []ModelsJSONModel{
			{ID: "custom-model", Name: "Custom"},
			{ID: "builtin-model", Name: "Replaced", ContextWindow: floatPtr(1000)},
		},
	}
	models, err := applyModelsJSON("custom", []*ai.Model{anthropicBaseModel()}, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}
	// The base model keeps its position but takes the provider baseUrl.
	base := models[0]
	if base.ID != "builtin-model" || base.Name != "Replaced" || base.ContextWindow != 1000 {
		t.Fatalf("base = %+v", base)
	}
	if base.BaseURL != "https://custom.example.com/v1" {
		t.Fatalf("baseUrl = %s", base.BaseURL)
	}
	// A custom model inherits defaults from the matching/default base model.
	custom := models[1]
	if custom.ID != "custom-model" || custom.Name != "Custom" || custom.API != ai.APIOpenAICompletions {
		t.Fatalf("custom = %+v", custom)
	}
	if custom.Provider != "custom" || custom.BaseURL != "https://custom.example.com/v1" {
		t.Fatalf("custom = %+v", custom)
	}
	// Defaults: reasoning false, input text, zero cost, 128k/16384.
	if custom.Reasoning || len(custom.Input) != 1 || custom.Input[0] != "text" {
		t.Fatalf("custom = %+v", custom)
	}
	if custom.ContextWindow != 128000 || custom.MaxTokens != 16384 {
		t.Fatalf("custom = %+v", custom)
	}
	// The name falls back to the id.
	config = &ModelsJSONProvider{API: ai.APIOpenAICompletions, BaseURL: "https://x"}
	models, err = applyModelsJSON("p", nil, config)
	if err != nil {
		t.Fatal(err)
	}
	config.Models = []ModelsJSONModel{{ID: "no-name"}}
	models, err = applyModelsJSON("p", nil, config)
	if err != nil || models[0].Name != "no-name" {
		t.Fatalf("models = %+v err = %v", models, err)
	}
}

func TestApplyModelsJSONErrors(t *testing.T) {
	// An empty config entry is rejected.
	if _, err := applyModelsJSON("p", nil, &ModelsJSONProvider{}); err == nil ||
		err.Error() != `Provider p: must specify "baseUrl", "headers", "compat", "modelOverrides", or "models".` {
		t.Fatalf("err = %v", err)
	}
	// oauth requires a baseUrl.
	if _, err := applyModelsJSON("p", nil, &ModelsJSONProvider{OAuth: "radius"}); err == nil ||
		err.Error() != `Provider p: "baseUrl" is required when "oauth" is set.` {
		t.Fatalf("err = %v", err)
	}
	// A custom model needs an api.
	if _, err := applyModelsJSON("p", nil, &ModelsJSONProvider{Models: []ModelsJSONModel{{ID: "m"}}, BaseURL: "https://x"}); err == nil ||
		err.Error() != `Provider p, model m: no "api" specified. Set at provider or model level.` {
		t.Fatalf("err = %v", err)
	}
	// A custom model needs a baseUrl.
	if _, err := applyModelsJSON("p", nil, &ModelsJSONProvider{Models: []ModelsJSONModel{{ID: "m"}}, API: ai.APIOpenAICompletions}); err == nil ||
		err.Error() != `Provider p: "baseUrl" is required when defining custom models.` {
		t.Fatalf("err = %v", err)
	}
	// Positive contextWindow/maxTokens only.
	if _, err := applyModelsJSON("p", nil, &ModelsJSONProvider{API: ai.APIOpenAICompletions, BaseURL: "https://x",
		Models: []ModelsJSONModel{{ID: "m", ContextWindow: floatPtr(0)}}}); err == nil ||
		err.Error() != "Provider p, model m: invalid contextWindow" {
		t.Fatalf("err = %v", err)
	}
	if _, err := applyModelsJSON("p", nil, &ModelsJSONProvider{API: ai.APIOpenAICompletions, BaseURL: "https://x",
		Models: []ModelsJSONModel{{ID: "m", MaxTokens: floatPtr(-1)}}}); err == nil ||
		err.Error() != "Provider p, model m: invalid maxTokens" {
		t.Fatalf("err = %v", err)
	}
	// A radius oauth provider keeps the base urls.
	models, err := applyModelsJSON("radius", []*ai.Model{anthropicBaseModel()}, &ModelsJSONProvider{OAuth: "radius", BaseURL: "https://keep"})
	if err != nil || models[0].BaseURL != "https://api.anthropic.com" {
		t.Fatalf("models = %+v err = %v", models, err)
	}
}

func TestApplyModelOverride(t *testing.T) {
	base := anthropicBaseModel()
	base.ThinkingLevelMap = ai.ThinkingLevelMap{ai.ThinkLow: strPtr("low")}
	base.SamplingParams = map[string]json.RawMessage{"temperature": json.RawMessage("0.1")}
	base.Compat = &ai.ModelCompat{AnthropicMessages: &ai.AnthropicMessagesCompat{}}

	override := ModelsJSONModelOverride{
		Name:             "Overridden",
		Reasoning:        boolPtr(true),
		ThinkingLevelMap: ai.ThinkingLevelMap{ai.ThinkHigh: strPtr("high"), ai.ThinkLow: nil},
		Input:            []string{"text", "image"},
		Cost:             &ModelsJSONCostOverride{Input: floatPtr(1), Tiers: []ModelsJSONCostTier{{InputTokensAbove: 100, ModelsJSONCostRates: ModelsJSONCostRates{Input: 2, Output: 3, CacheRead: 4, CacheWrite: 5}}}},
		ContextWindow:    floatPtr(500),
		MaxTokens:        floatPtr(100),
		SamplingParams:   map[string]any{"temperature": 0.7, "topP": 0.9},
		Compat:           &ModelsJSONCompat{SupportsTemperature: boolPtr(true)},
	}
	updated, err := applyModelOverride(base, override)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Overridden" || !updated.Reasoning {
		t.Fatalf("override = %+v", updated)
	}
	if level := updated.ThinkingLevelMap[ai.ThinkHigh]; level == nil || *level != "high" {
		t.Fatalf("thinkingLevelMap = %+v", updated.ThinkingLevelMap)
	}
	if updated.ThinkingLevelMap[ai.ThinkLow] != nil {
		t.Fatal("low must be cleared by the override")
	}
	if len(updated.Input) != 2 || updated.ContextWindow != 500 || updated.MaxTokens != 100 {
		t.Fatalf("override = %+v", updated)
	}
	// The cost merges field by field and keeps unspecified rates.
	if updated.Cost.Input != 1 || updated.Cost.Output != 15 || updated.Cost.CacheRead != 0.3 || updated.Cost.CacheWrite != 3.75 {
		t.Fatalf("cost = %+v", updated.Cost)
	}
	if len(updated.Cost.Tiers) != 1 || updated.Cost.Tiers[0].InputTokensAbove != 100 || updated.Cost.Tiers[0].Output != 3 {
		t.Fatalf("tiers = %+v", updated.Cost.Tiers)
	}
	// Sampling params merge, with the override winning.
	var temperature float64
	if err := json.Unmarshal(updated.SamplingParams["temperature"], &temperature); err != nil || temperature != 0.7 {
		t.Fatalf("temperature = %v err = %v", temperature, err)
	}
	if _, ok := updated.SamplingParams["topP"]; !ok {
		t.Fatalf("samplingParams = %+v", updated.SamplingParams)
	}
	// Compat merges into the typed variant.
	if updated.Compat == nil || updated.Compat.AnthropicMessages == nil ||
		updated.Compat.AnthropicMessages.SupportsTemperature == nil || !*updated.Compat.AnthropicMessages.SupportsTemperature {
		t.Fatalf("compat = %+v", updated.Compat)
	}
	// The original model is untouched.
	if base.Name != "Builtin" || base.Cost.Input != 3 {
		t.Fatalf("base mutated: %+v", base)
	}
}

func TestMergeCompatNested(t *testing.T) {
	base := &ai.ModelCompat{OpenAICompletions: &ai.OpenAICompletionsCompat{
		SupportsStore: boolPtr(true),
		ChatTemplateKwargs: map[string]json.RawMessage{
			"a": json.RawMessage("1"), "b": json.RawMessage("2"),
		},
		OpenRouterRouting: &ai.OpenRouterRouting{Zdr: boolPtr(true)},
	}}
	override := &ModelsJSONCompat{
		SupportsStore:      boolPtr(false),
		ChatTemplateKwargs: map[string]ModelsJSONChatTemplateKwarg{"b": {Scalar: float64(20)}, "c": {Scalar: "x"}},
		OpenRouterRouting:  &ModelsJSONOpenRouterRouting{Zdr: boolPtr(false), Order: []string{"a"}},
	}
	merged, err := mergeCompatJSON(ai.APIOpenAICompletions, base, override)
	if err != nil {
		t.Fatal(err)
	}
	compat := merged.OpenAICompletions
	if compat == nil || compat.SupportsStore == nil || *compat.SupportsStore {
		t.Fatalf("compat = %+v", compat)
	}
	// Nested records merge key by key: base keys survive.
	if len(compat.ChatTemplateKwargs) != 3 || string(compat.ChatTemplateKwargs["a"]) != "1" {
		t.Fatalf("kwargs = %+v", compat.ChatTemplateKwargs)
	}
	routing := compat.OpenRouterRouting
	if routing == nil || routing.Zdr == nil || *routing.Zdr {
		t.Fatalf("routing = %+v", routing)
	}
	if len(routing.Order) != 1 || routing.Order[0] != "a" {
		t.Fatalf("routing = %+v", routing)
	}

	// A nil override returns the base pointer unchanged.
	if got, err := mergeCompatJSON(ai.APIOpenAICompletions, base, nil); err != nil || got != base {
		t.Fatalf("got = %+v err = %v", got, err)
	}
	// A nil base still decodes the override for the model's api.
	only, err := mergeCompatJSON(ai.APIAnthropicMessages, nil, &ModelsJSONCompat{
		ForceAdaptiveThinking: boolPtr(true),
	})
	if err != nil || only == nil || only.AnthropicMessages == nil || only.AnthropicMessages.ForceAdaptiveThinking == nil {
		t.Fatalf("only = %+v err = %v", only, err)
	}
}

func derefHeader(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func TestComposeModelProvider(t *testing.T) {
	config := loadConfig(t, `{"providers":{"anthropic":{
		"name":"Anthropic Custom",
		"baseUrl":"https://proxy.example.com",
		"headers":{"X-Provider":"1"},
		"models":[{"id":"custom","name":"Custom","api":"anthropic-messages","reasoning":true}],
		"modelOverrides":{"builtin-model":{"name":"Overridden"}}
	}}}`)
	provider, err := ComposeModelProvider("anthropic", anthropicBaseProvider(), config)
	if err != nil {
		t.Fatal(err)
	}
	if provider.ID != "anthropic" || provider.Name != "Anthropic Custom" || provider.BaseURL != "https://proxy.example.com" {
		t.Fatalf("provider = %+v", provider)
	}
	models := provider.GetModels()
	if len(models) != 2 {
		t.Fatalf("models = %+v", models)
	}
	if models[0].ID != "builtin-model" || models[0].Name != "Overridden" {
		t.Fatalf("override not applied: %+v", models[0])
	}
	if models[1].ID != "custom" || !models[1].Reasoning || models[1].BaseURL != "https://proxy.example.com" {
		t.Fatalf("custom model = %+v", models[1])
	}
	// The composed provider streams through the base for known apis and
	// through the api registry otherwise.
	if provider.Auth.APIKey == nil || provider.Auth.APIKey.Name != "Anthropic API key" {
		t.Fatalf("auth = %+v", provider.Auth)
	}

	// A provider with no auth at all is rejected.
	config = loadConfig(t, `{"providers":{"custom":{"baseUrl":"https://x","apiKey":"k"}}}`)
	provider, err = ComposeModelProvider("custom", nil, config)
	if err != nil {
		t.Fatal(err)
	}
	if provider.Auth.APIKey == nil || provider.Name != "custom" {
		t.Fatalf("provider = %+v", provider)
	}
	// The configured key resolves through the auth method.
	result, err := provider.Auth.APIKey.Resolve(ai.AuthResolveInput{Ctx: ai.DefaultAuthContext{}})
	if err != nil || result == nil || result.Auth.APIKey != "k" || result.Source != "configured API key" {
		t.Fatalf("result = %+v err = %v", result, err)
	}

	// A provider with no base and no models.json entry still gets the default
	// interactive API-key method (upstream fabricates one when no OAuth arm
	// exists), so composition succeeds.
	bare, err := ComposeModelProvider("unknown", nil, loadConfig(t, `{"providers":{}}`))
	if err != nil || bare.Auth.APIKey == nil || bare.Auth.APIKey.Name != "API key" {
		t.Fatalf("provider = %+v err = %v", bare, err)
	}
	// Structural errors surface at composition time.
	if _, err := ComposeModelProvider("bad", nil, loadConfig(t, `{"providers":{"bad":{"models":[{"id":"m"}]}}}`)); err == nil ||
		!strings.Contains(err.Error(), `no "api" specified`) {
		t.Fatalf("err = %v", err)
	}
}

func TestComposeModelProviderAuthHeader(t *testing.T) {
	config := loadConfig(t, `{"providers":{"anthropic":{"baseUrl":"https://x","authHeader":true}}}`)
	provider, err := ComposeModelProvider("anthropic", anthropicBaseProvider(), config)
	if err != nil {
		t.Fatal(err)
	}
	result, err := provider.Auth.APIKey.Resolve(ai.AuthResolveInput{
		Ctx:        ai.DefaultAuthContext{},
		Credential: &ai.ApiKeyCredential{Key: "sk-1"},
	})
	if err != nil || result == nil {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	if result.Auth.Headers["Authorization"] == nil || *result.Auth.Headers["Authorization"] != "Bearer sk-1" {
		t.Fatalf("headers = %+v", result.Auth.Headers)
	}

	// With no base auth and no configured key there is nothing to resolve, so
	// resolution returns nothing rather than an error (upstream only throws
	// once a result exists).
	bare, err := ComposeModelProvider("anthropic", ai.CreateProvider(ai.CreateProviderOptions{
		ID: "anthropic", Models: nil, Auth: ai.ProviderAuth{},
	}), config)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if result, err := bare.Auth.APIKey.Resolve(ai.AuthResolveInput{Ctx: ai.DefaultAuthContext{}}); err != nil || result != nil {
		t.Fatalf("result = %+v err = %v", result, err)
	}

	// An authHeader with a result that has no api key fails.
	keyless := ai.CreateProvider(ai.CreateProviderOptions{
		ID: "anthropic", Models: nil,
		Auth: ai.ProviderAuth{APIKey: &ai.ApiKeyAuth{
			Name: "Keyless",
			Resolve: func(input ai.AuthResolveInput) (*ai.AuthResult, error) {
				return &ai.AuthResult{Auth: ai.ModelAuth{}, Source: "none"}, nil
			},
		}},
	})
	provider, err = ComposeModelProvider("anthropic", keyless, config)
	if err != nil {
		t.Fatalf("compose: %v", err)
	}
	if _, err := provider.Auth.APIKey.Resolve(ai.AuthResolveInput{Ctx: ai.DefaultAuthContext{}}); err == nil ||
		err.Error() != "authHeader requires a resolved API key" {
		t.Fatalf("err = %v", err)
	}
}

func TestConfiguredHeadersAndStatus(t *testing.T) {
	t.Setenv("CUSTOM_TOKEN", "resolved")
	config := &ModelsJSONProvider{
		APIKey:  "$CUSTOM_TOKEN",
		Headers: map[string]string{"X-Provider": "$CUSTOM_TOKEN"},
		Models: []ModelsJSONModel{{
			ID: "m", Headers: map[string]string{"X-Model": "static"},
		}},
		ModelOverrides: map[string]ModelsJSONModelOverride{
			"m": {Headers: map[string]string{"X-Override": "static"}},
		},
	}
	model := &ai.Model{ID: "m", Provider: "custom", Headers: map[string]string{"X-Base": "static"}}
	// Per-model headers are the model override plus the definition's headers;
	// provider-level headers are applied by the auth path instead.
	headers, err := ResolveConfiguredModelHeaders(model, config, nil)
	if err != nil {
		t.Fatal(err)
	}
	if headers["X-Model"] != "static" || headers["X-Override"] != "static" {
		t.Fatalf("headers = %+v", headers)
	}
	if _, ok := headers["X-Provider"]; ok {
		t.Fatalf("provider headers must not be in the model headers: %+v", headers)
	}

	requestConfig, err := ResolveCompatibilityRequestConfig(model, config)
	if err != nil {
		t.Fatal(err)
	}
	if derefHeader(requestConfig.Headers["X-Base"]) != "static" || derefHeader(requestConfig.Headers["X-Model"]) != "static" ||
		derefHeader(requestConfig.Headers["X-Provider"]) != "resolved" {
		t.Fatalf("headers = %+v", requestConfig.Headers)
	}

	status := ConfiguredRequestAuthStatus(config)
	if status == nil || !status.Configured || status.Source != "environment" || status.Label != "CUSTOM_TOKEN" {
		t.Fatalf("status = %+v", status)
	}
	if status := ConfiguredRequestAuthStatus(&ModelsJSONProvider{APIKey: "sk-literal"}); status == nil ||
		status.Source != "models_json_key" {
		t.Fatalf("status = %+v", status)
	}
	if status := ConfiguredRequestAuthStatus(&ModelsJSONProvider{APIKey: "!printf k"}); status == nil ||
		status.Source != "models_json_command" {
		t.Fatalf("status = %+v", status)
	}
	t.Setenv("CUSTOM_TOKEN", "")
	os.Unsetenv("CUSTOM_TOKEN")
	if status := ConfiguredRequestAuthStatus(config); status == nil || status.Configured {
		t.Fatalf("status = %+v", status)
	}
	if status := ConfiguredRequestAuthStatus(nil); status != nil {
		t.Fatalf("status = %+v", status)
	}
}

func floatPtr(value float64) *float64 { return &value }
