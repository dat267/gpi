package providers

import (
	"context"
	"strings"
	"testing"

	"github.com/dat267/gpi/ai"
)

// Tests for providers/cloudflare-auth.ts, providers/cloudflare-stream.ts, and
// api/cloudflare.ts.

// cloudflareEnv is an injected auth context.
type cloudflareEnv map[string]string

func (e cloudflareEnv) Env(name string) (string, bool) {
	value, ok := e[name]
	return value, ok && value != ""
}

func (e cloudflareEnv) FileExists(string) bool { return false }

func TestCloudflareWorkersAIAuthResolution(t *testing.T) {
	auth := CloudflareWorkersAIAuth()
	if auth.Name != "Cloudflare API key" {
		t.Fatalf("name = %q", auth.Name)
	}
	// Credential key + credential env account id.
	result, err := auth.Resolve(ai.AuthResolveInput{
		Ctx: cloudflareEnv{},
		Credential: &ai.ApiKeyCredential{
			Key: "cf-key", Env: ai.ProviderEnv{CloudflareAccountIDEnv: "account-1"},
		},
	})
	if err != nil || result == nil {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	if result.Auth.APIKey != "cf-key" || result.Env[CloudflareAccountIDEnv] != "account-1" {
		t.Fatalf("result = %+v", result)
	}
	if result.Source != "stored credential" {
		t.Fatalf("source = %q", result.Source)
	}

	// A key-only credential picks the account id up from the environment
	// (upstream's per-field merge).
	result, err = auth.Resolve(ai.AuthResolveInput{
		Ctx:        cloudflareEnv{CloudflareAccountIDEnv: "account-env", CloudflareGatewayIDEnv: "gateway-env"},
		Credential: &ai.ApiKeyCredential{Key: "cf-key"},
	})
	if err != nil || result == nil {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	if result.Env[CloudflareAccountIDEnv] != "account-env" {
		t.Fatalf("env = %#v", result.Env)
	}
	if _, hasGateway := result.Env[CloudflareGatewayIDEnv]; hasGateway {
		t.Fatalf("workers-ai must not resolve a gateway id: %#v", result.Env)
	}
	if result.Source != "stored credential" {
		t.Fatalf("source = %q", result.Source)
	}

	// Ambient-only resolution reports the env source.
	result, err = auth.Resolve(ai.AuthResolveInput{
		Ctx: cloudflareEnv{CloudflareAPIKeyEnv: "env-key", CloudflareAccountIDEnv: "account-env"},
	})
	if err != nil || result == nil || result.Source != CloudflareAPIKeyEnv || result.Auth.APIKey != "env-key" {
		t.Fatalf("result = %+v err = %v", result, err)
	}

	// Missing fields resolve to nothing.
	for _, env := range []cloudflareEnv{
		{},
		{CloudflareAPIKeyEnv: "key"},
		{CloudflareAccountIDEnv: "account"},
	} {
		result, err := auth.Resolve(ai.AuthResolveInput{Ctx: env})
		if err != nil || result != nil {
			t.Fatalf("env %#v: result = %+v err = %v", env, result, err)
		}
	}
}

func TestCloudflareAIGatewayAuthResolution(t *testing.T) {
	auth := CloudflareAIGatewayAuth()
	result, err := auth.Resolve(ai.AuthResolveInput{
		Ctx: cloudflareEnv{
			CloudflareAPIKeyEnv:    "gateway-key",
			CloudflareAccountIDEnv: "account-1",
			CloudflareGatewayIDEnv: "gateway-1",
		},
	})
	if err != nil || result == nil {
		t.Fatalf("result = %+v err = %v", result, err)
	}
	// The gateway uses cf-aig-authorization and suppresses the SDK placeholder
	// auth headers (nil deletes a default header).
	header := result.Auth.Headers["cf-aig-authorization"]
	if header == nil || *header != "Bearer gateway-key" {
		t.Fatalf("headers = %#v", result.Auth.Headers)
	}
	if _, hasAuth := result.Auth.Headers["Authorization"]; !hasAuth {
		t.Fatalf("Authorization must be suppressed explicitly: %#v", result.Auth.Headers)
	}
	if value, hasKey := result.Auth.Headers["x-api-key"]; !hasKey || value != nil {
		t.Fatalf("x-api-key must be nil: %#v", result.Auth.Headers)
	}
	if result.Auth.APIKey != "" {
		t.Fatalf("the gateway must not send a plain api key: %+v", result.Auth)
	}
	if result.Env[CloudflareAccountIDEnv] != "account-1" || result.Env[CloudflareGatewayIDEnv] != "gateway-1" {
		t.Fatalf("env = %#v", result.Env)
	}

	// Without a gateway id the gateway auth does not resolve.
	result, err = auth.Resolve(ai.AuthResolveInput{Ctx: cloudflareEnv{
		CloudflareAPIKeyEnv: "key", CloudflareAccountIDEnv: "account",
	}})
	if err != nil || result != nil {
		t.Fatalf("result = %+v err = %v", result, err)
	}
}

func TestResolveCloudflareModel(t *testing.T) {
	model := &ai.Model{
		ID:      "@cf/meta/llama",
		BaseURL: "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1",
	}
	// No env leaves the model untouched (same pointer).
	if resolved := ResolveCloudflareModel(model, nil); resolved != model {
		t.Fatal("no env must return the same model")
	}
	resolved := ResolveCloudflareModel(model, ai.ProviderEnv{CloudflareAccountIDEnv: "abc123"})
	if resolved == model {
		t.Fatal("a changed base URL must return a clone")
	}
	if resolved.BaseURL != "https://api.cloudflare.com/client/v4/accounts/abc123/ai/v1" {
		t.Fatalf("baseUrl = %s", resolved.BaseURL)
	}
	// The original is untouched.
	if model.BaseURL != "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1" {
		t.Fatalf("original mutated: %s", model.BaseURL)
	}
	// A missing placeholder keeps the unresolved marker, and an unchanged URL
	// returns the same model.
	unchanged := ResolveCloudflareModel(model, ai.ProviderEnv{CloudflareGatewayIDEnv: "gw"})
	if !strings.Contains(unchanged.BaseURL, "{CLOUDFLARE_ACCOUNT_ID}") {
		t.Fatalf("baseUrl = %s", unchanged.BaseURL)
	}

	// Both placeholders resolve for the gateway routes.
	gateway := &ai.Model{BaseURL: CloudflareAIGatewayAnthropicBaseURL}
	gatewayResolved := ResolveCloudflareModel(gateway, ai.ProviderEnv{
		CloudflareAccountIDEnv: "acct", CloudflareGatewayIDEnv: "gate",
	})
	if gatewayResolved.BaseURL != "https://gateway.ai.cloudflare.com/v1/acct/gate/anthropic" {
		t.Fatalf("baseUrl = %s", gatewayResolved.BaseURL)
	}
}

func TestCloudflareStreamsResolveModelBeforeDispatch(t *testing.T) {
	var seen *ai.Model
	recorder := &recordingStreams{}
	recorder.onStream = func(*ai.StreamOptions) {}
	wrapped := CloudflareStreams(modelRecordingStreams{onModel: func(model *ai.Model) { seen = model }})

	model := &ai.Model{
		ID:      "workers-model",
		BaseURL: "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1",
	}
	wrapped.Stream(model, ai.TranscriptContext{}, &ai.StreamOptions{
		Env: ai.ProviderEnv{CloudflareAccountIDEnv: "acct"},
	})
	if seen == nil || !strings.Contains(seen.BaseURL, "/accounts/acct/") {
		t.Fatalf("model = %+v", seen)
	}
	// The simple path resolves too.
	seen = nil
	wrapped.StreamSimple(model, ai.TranscriptContext{}, &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{Env: ai.ProviderEnv{CloudflareAccountIDEnv: "acct2"}},
	})
	if seen == nil || !strings.Contains(seen.BaseURL, "/accounts/acct2/") {
		t.Fatalf("model = %+v", seen)
	}
	_ = recorder
}

// modelRecordingStreams records the model each stream call receives.
type modelRecordingStreams struct {
	onModel func(model *ai.Model)
}

func (m modelRecordingStreams) Stream(model *ai.Model, context ai.TranscriptContext, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	if m.onModel != nil {
		m.onModel(model)
	}
	return ai.NewAssistantMessageEventStream()
}

func (m modelRecordingStreams) StreamSimple(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	if m.onModel != nil {
		m.onModel(model)
	}
	return ai.NewAssistantMessageEventStream()
}

func TestCloudflareProviders(t *testing.T) {
	workers := CloudflareWorkersAIProvider()
	if workers.ID != "cloudflare-workers-ai" || workers.Name != "Cloudflare Workers AI" {
		t.Fatalf("provider = %s/%s", workers.ID, workers.Name)
	}
	if workers.BaseURL != "" {
		t.Fatalf("baseUrl = %s (models carry the endpoint)", workers.BaseURL)
	}
	models := workers.GetModels()
	if len(models) == 0 {
		t.Fatal("no workers-ai catalog models")
	}
	for _, model := range models {
		if model.API != ai.APIOpenAICompletions {
			t.Fatalf("model %s api = %s", model.ID, model.API)
		}
		if !strings.Contains(model.BaseURL, "{CLOUDFLARE_ACCOUNT_ID}") {
			t.Fatalf("model base URL must keep its placeholder: %s", model.BaseURL)
		}
	}

	gateway := CloudflareAIGatewayProvider()
	if gateway.ID != "cloudflare-ai-gateway" || gateway.Name != "Cloudflare AI Gateway" {
		t.Fatalf("provider = %s/%s", gateway.ID, gateway.Name)
	}
	apis := map[string]bool{}
	for _, model := range gateway.GetModels() {
		apis[model.API] = true
		if !strings.Contains(model.BaseURL, "{CLOUDFLARE_ACCOUNT_ID}") ||
			!strings.Contains(model.BaseURL, "{CLOUDFLARE_GATEWAY_ID}") {
			t.Fatalf("model base URL must keep both placeholders: %s", model.BaseURL)
		}
	}
	// All three gateway dialects are pinned, matching upstream's comment about
	// the catalog's workers-ai entries coming and going.
	for _, api := range []string{ai.APIAnthropicMessages, ai.APIOpenAICompletions, ai.APIOpenAIResponses} {
		if !apis[api] {
			t.Fatalf("missing api %s in %v", api, apis)
		}
	}
}

func TestCloudflareLoginFlows(t *testing.T) {
	// The login prompts collect the account (and gateway) ids into the
	// credential env.
	interaction := &ai.AuthInteraction{
		Ctx: context.Background(),
		Prompt: func(prompt ai.AuthPrompt) (string, error) {
			return "value:" + prompt.Message, nil
		},
	}
	credential, err := CloudflareWorkersAIAuth().Login(interaction)
	if err != nil {
		t.Fatal(err)
	}
	if credential.Key != "value:Enter Cloudflare API key" ||
		credential.Env[CloudflareAccountIDEnv] != "value:Enter Cloudflare account ID" {
		t.Fatalf("credential = %+v", credential)
	}
	gatewayCredential, err := CloudflareAIGatewayAuth().Login(interaction)
	if err != nil {
		t.Fatal(err)
	}
	if gatewayCredential.Env[CloudflareGatewayIDEnv] != "value:Enter Cloudflare AI Gateway ID" {
		t.Fatalf("credential = %+v", gatewayCredential)
	}
}
