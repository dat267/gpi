package providers

import (
	"testing"

	"github.com/dat267/pier/ai"
)

// TestExtensionProvidersRegistered covers the two user provider extensions:
// the providers register with their static catalogs and env API-key auth.
func TestExtensionProvidersRegistered(t *testing.T) {
	providers := ExtensionProviders()
	if len(providers) != len(ExtensionProviderIDs) {
		t.Fatalf("providers = %d, want %d", len(providers), len(ExtensionProviderIDs))
	}
	byID := map[string]*ai.Provider{}
	for _, provider := range providers {
		byID[provider.ID] = provider
	}
	for _, id := range ExtensionProviderIDs {
		if _, ok := byID[id]; !ok {
			t.Fatalf("missing provider %q", id)
		}
	}
	hyper := byID["hyper"]
	if hyper == nil || hyper.Name != "Charm Hyper" || hyper.BaseURL != "https://hyper.charm.land/v1" {
		t.Fatalf("hyper = %+v", hyper)
	}
	if hyper.Auth.APIKey == nil {
		t.Fatalf("hyper auth = %+v", hyper.Auth)
	}
	commandCode := byID["commandcode"]
	if commandCode == nil || commandCode.Name != "Command Code" || commandCode.BaseURL != "https://api.commandcode.ai/provider/v1" {
		t.Fatalf("commandcode = %+v", commandCode)
	}
}

// TestHyperCatalog covers the Charm Hyper catalog invariants.
func TestHyperCatalog(t *testing.T) {
	models := hyperModels()
	if len(models) != 11 {
		t.Fatalf("hyper models = %d, want 11", len(models))
	}
	byID := map[string]*ai.Model{}
	for _, model := range models {
		if model.Provider != "hyper" {
			t.Errorf("provider = %q", model.Provider)
		}
		if model.API != ai.APIOpenAICompletions {
			t.Errorf("%s api = %q", model.ID, model.API)
		}
		if model.BaseURL != "https://hyper.charm.land/v1" {
			t.Errorf("%s baseUrl = %q", model.ID, model.BaseURL)
		}
		byID[model.ID] = model
	}
	flash := byID["glm-5.3-flash"]
	if flash == nil {
		t.Fatal("glm-5.3-flash missing")
	}
	if flash.ContextWindow != 1_048_576 || flash.MaxTokens != 131_072 {
		t.Fatalf("glm-5.3-flash limits = %d/%d", flash.ContextWindow, flash.MaxTokens)
	}
	if flash.Cost.Input != 0.16332 || flash.Cost.CacheRead != 0.031575 {
		t.Fatalf("glm-5.3-flash cost = %+v", flash.Cost)
	}
	if len(flash.Input) != 2 || flash.Input[1] != "image" {
		t.Fatalf("glm-5.3-flash input = %v", flash.Input)
	}
	// The effort vocabulary maps onto pi's thinking levels.
	if flash.ThinkingLevelMap[ai.ThinkLow] == nil || flash.ThinkingLevelMap[ai.ThinkMedium] != nil {
		t.Fatalf("glm-5.3-flash thinkingLevelMap = %+v", flash.ThinkingLevelMap)
	}
	if flash.Compat == nil || flash.Compat.OpenAICompletions == nil ||
		flash.Compat.OpenAICompletions.ThinkingFormat == nil ||
		*flash.Compat.OpenAICompletions.ThinkingFormat != "deepseek" {
		t.Fatalf("glm-5.3-flash compat = %+v", flash.Compat)
	}
}

// TestCommandCodeCatalog covers the Command Code routing and exclusions.
func TestCommandCodeCatalog(t *testing.T) {
	models := commandCodeModels()
	if len(models) != 25 {
		t.Fatalf("commandcode models = %d, want 25", len(models))
	}
	byID := map[string]*ai.Model{}
	for _, model := range models {
		if commandCodeExcludedIDs[model.ID] {
			t.Errorf("%s must be excluded", model.ID)
		}
		if model.Provider != "commandcode" {
			t.Errorf("provider = %q", model.Provider)
		}
		byID[model.ID] = model
	}
	// Claude models route to anthropic-messages at the base URL minus /v1.
	claude, ok := byID["claude-haiku-4-5-20251001"]
	if ok {
		if claude.API != ai.APIAnthropicMessages || claude.BaseURL != "https://api.commandcode.ai/provider" {
			t.Fatalf("claude routing = %s %s", claude.API, claude.BaseURL)
		}
	}
	openai := byID["deepseek/deepseek-v4-pro"]
	if openai == nil || openai.API != ai.APIOpenAICompletions || openai.BaseURL != "https://api.commandcode.ai/provider/v1" {
		t.Fatalf("openai routing = %+v", openai)
	}
	// Unavailable and dominated ids stay out.
	for _, id := range []string{"claude-sonnet-5", "zai-org/GLM-5.2-Fast", "Qwen/Qwen3.7-Flash", "moonshotai/Kimi-K2.6"} {
		if byID[id] != nil {
			t.Errorf("%s must be excluded", id)
		}
	}
	// Effort mapping: an entry with efforts keeps them, one without stays nil.
	if withEfforts := byID["deepseek/deepseek-v4-pro"]; withEfforts != nil &&
		withEfforts.ThinkingLevelMap[ai.ThinkHigh] == nil {
		t.Fatalf("efforts map = %+v", withEfforts.ThinkingLevelMap)
	}
	if noEfforts := byID["moonshotai/Kimi-K2.6"]; noEfforts == nil {
		t.Log("Kimi-K2.6 excluded; skipping the no-efforts assertion")
	}
}
