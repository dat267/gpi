package ai

import (
	"encoding/json"
	"testing"
	"time"
)

// Catalog tests. Ground truth: upstream's generated data (see catalog.go
// header for the regeneration procedure).

func TestCatalogLoadsEveryModel(t *testing.T) {
	if err := CatalogLoadError(); err != nil {
		t.Fatalf("catalog decode: %v", err)
	}
	n := BuiltinModelCount()
	// The catalog drifts as upstream regenerates; assert the known scale.
	if n < 1000 {
		t.Fatalf("builtin models = %d; want >= 1000", n)
	}
	providers := GetBuiltinProviders()
	if len(providers) < 30 {
		t.Fatalf("builtin providers = %d; want >= 30", len(providers))
	}
	for _, want := range []string{"anthropic", "openai", "google", "openrouter", "amazon-bedrock", "zai"} {
		found := false
		for _, p := range providers {
			if p == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("provider %q missing from catalog", want)
		}
	}
}

func TestCatalogAnthropicOpusSpotCheck(t *testing.T) {
	m := GetBuiltinModel("anthropic", "claude-opus-4-5")
	if m == nil {
		t.Fatal("anthropic/claude-opus-4-5 missing")
	}
	if m.API != APIAnthropicMessages || m.Provider != "anthropic" {
		t.Fatalf("api/provider = %s/%s", m.API, m.Provider)
	}
	if m.BaseURL != "https://api.anthropic.com" {
		t.Fatalf("baseUrl = %s", m.BaseURL)
	}
	if !m.Reasoning || m.ContextWindow != 200000 || m.MaxTokens != 64000 {
		t.Fatalf("model = %+v", m)
	}
	if m.Cost.Input != 5 || m.Cost.Output != 25 || m.Cost.CacheRead != 0.5 || m.Cost.CacheWrite != 6.25 {
		t.Fatalf("cost = %+v", m.Cost)
	}
	if len(m.Input) != 2 || m.Input[0] != "text" || m.Input[1] != "image" {
		t.Fatalf("input = %v", m.Input)
	}
	if m.Compat == nil || m.Compat.AnthropicMessages == nil || m.Compat.AnthropicMessages.SupportsStrictTools == nil || !*m.Compat.AnthropicMessages.SupportsStrictTools {
		t.Fatalf("compat = %+v", m.Compat)
	}
}

func TestCatalogCompatArmMatchesAPI(t *testing.T) {
	// Every model with a compat object must decode into exactly the arm
	// matching its api, and every arm must decode losslessly.
	loadCatalog()
	if catalogErr != nil {
		t.Fatal(catalogErr)
	}
	for providerID, apis := range catalog.Providers {
		for api, entries := range apis {
			var byID map[string]json.RawMessage
			if err := jsonUnmarshalStrict(entries, &byID); err != nil {
				t.Fatal(err)
			}
			for modelID := range byID {
				m := GetBuiltinModel(providerID, modelID)
				if m == nil {
					t.Fatalf("model %s/%s failed to load", providerID, modelID)
				}
				if m.API != api {
					t.Fatalf("%s/%s api = %s; want %s", providerID, modelID, m.API, api)
				}
				if m.Compat == nil {
					continue
				}
				arms := 0
				if m.Compat.OpenAICompletions != nil {
					arms++
				}
				if m.Compat.OpenAIResponses != nil {
					arms++
				}
				if m.Compat.AnthropicMessages != nil {
					arms++
				}
				if m.Compat.Bedrock != nil {
					arms++
				}
				if m.Compat.MistralConversations != nil {
					arms++
				}
				if arms != 1 {
					t.Fatalf("%s/%s: %d compat arms set", providerID, modelID, arms)
				}
				switch api {
				case APIAnthropicMessages:
					if m.Compat.AnthropicMessages == nil {
						t.Fatalf("%s/%s: anthropic compat missing", providerID, modelID)
					}
				case APIOpenAICompletions:
					if m.Compat.OpenAICompletions == nil {
						t.Fatalf("%s/%s: completions compat missing", providerID, modelID)
					}
				case APIOpenAIResponses, APIAzureOpenAIResponses, APIOpenAICodexResponses:
					if m.Compat.OpenAIResponses == nil {
						t.Fatalf("%s/%s: responses compat missing", providerID, modelID)
					}
				case APIMistralConversations:
					if m.Compat.MistralConversations == nil {
						t.Fatalf("%s/%s: mistral compat missing", providerID, modelID)
					}
				case APIBedrockConverse:
					if m.Compat.Bedrock == nil {
						t.Fatalf("%s/%s: bedrock compat missing", providerID, modelID)
					}
				}
			}
		}
	}
}

func TestCatalogGeneratedAt(t *testing.T) {
	ts := GetBuiltinModelDataGeneratedAt()
	if ts == nil {
		t.Fatal("generatedAt missing or unparsable")
	}
	if ts.IsZero() || ts.After(time.Now().Add(time.Hour)) {
		t.Fatalf("generatedAt implausible: %v", ts)
	}
}

func TestGetEnvApiKey(t *testing.T) {
	// Simple providers: single env var.
	if got := GetEnvApiKey("deepseek", ProviderEnv{"DEEPSEEK_API_KEY": "k"}); got != "k" {
		t.Fatalf("deepseek = %q", got)
	}
	if got := GetEnvApiKey("deepseek", ProviderEnv{}); got != "" {
		t.Fatalf("unset deepseek = %q", got)
	}

	// Anthropic: AUTH_TOKEN participates in discovery but is skipped for the
	// key; OAUTH_TOKEN and API_KEY resolve.
	if got := GetEnvApiKey("anthropic", ProviderEnv{AnthropicAuthTokenEnv: "tok"}); got != "" {
		t.Fatalf("anthropic auth token must not resolve as api key, got %q", got)
	}
	if got := GetEnvApiKey("anthropic", ProviderEnv{AnthropicAuthTokenEnv: "tok", AnthropicOAuthTokenEnv: "o", AnthropicAPIKeyEnv: "k"}); got != "o" {
		t.Fatalf("anthropic precedence = %q; want oauth token first", got)
	}
	if got := GetEnvApiKey("anthropic", ProviderEnv{AnthropicAPIKeyEnv: "k"}); got != "k" {
		t.Fatalf("anthropic api key = %q", got)
	}

	// Scoped env beats the process environment.
	t.Setenv("DEEPSEEK_API_KEY", "process")
	if got := GetEnvApiKey("deepseek", ProviderEnv{"DEEPSEEK_API_KEY": "scoped"}); got != "scoped" {
		t.Fatalf("scoped = %q", got)
	}
	if got := GetEnvApiKey("deepseek", ProviderEnv{}); got != "process" {
		t.Fatalf("process fallback = %q", got)
	}

	// google-vertex: ADC + project + location.
	if got := GetEnvApiKey("google-vertex", ProviderEnv{
		"GOOGLE_CLOUD_PROJECT":  "p",
		"GOOGLE_CLOUD_LOCATION": "l",
	}); got != "" {
		t.Fatalf("vertex without ADC = %q", got)
	}
	// (ADC file presence is environment-dependent; the ambient branch is
	// covered by upstream's provider tests and the coding agent's live tests.)

	// amazon-bedrock: standard IAM keys.
	if got := GetEnvApiKey("amazon-bedrock", ProviderEnv{
		"AWS_ACCESS_KEY_ID":     "id",
		"AWS_SECRET_ACCESS_KEY": "secret",
	}); got != "<authenticated>" {
		t.Fatalf("bedrock IAM = %q", got)
	}
	if got := GetEnvApiKey("amazon-bedrock", ProviderEnv{"AWS_PROFILE": "default"}); got != "<authenticated>" {
		t.Fatalf("bedrock profile = %q", got)
	}
	if got := GetEnvApiKey("amazon-bedrock", ProviderEnv{"AWS_ACCESS_KEY_ID": "id"}); got != "" {
		t.Fatalf("bedrock partial IAM = %q", got)
	}
}
