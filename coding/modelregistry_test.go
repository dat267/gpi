package coding

import (
	ctxpkg "context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
)

// Round 102 tests: the ModelRegistry facade.

func registryFor(t *testing.T, providers ...*ai.Provider) *ModelRegistry {
	t.Helper()
	return NewModelRegistry(runtimeWithProviders(t, providers...))
}

func TestModelRegistryReads(t *testing.T) {
	registry := registryFor(t, stubProvider("alpha"), stubProvider("beta"))
	if len(registry.GetAll()) != 2 {
		t.Fatalf("all = %+v", registry.GetAll())
	}
	if len(registry.GetAvailable()) != 0 {
		t.Fatalf("available = %+v", registry.GetAvailable())
	}
	if registry.Find("alpha", "alpha-model") == nil || registry.Find("alpha", "missing") != nil {
		t.Fatal("Find")
	}
	model := registry.Find("alpha", "alpha-model")
	if registry.HasConfiguredAuth(model) {
		t.Fatal("auth must not be configured yet")
	}
	if registry.GetProviderDisplayName("alpha") != "alpha provider" ||
		registry.GetProviderDisplayName("missing") != "missing" {
		t.Fatalf("display name = %q", registry.GetProviderDisplayName("alpha"))
	}
	if registry.GetProvider("alpha") == nil || registry.GetProvider("missing") != nil {
		t.Fatal("GetProvider")
	}
	if registry.IsUsingOAuth(model) {
		t.Fatal("api key provider is not oauth")
	}
	if status := registry.GetProviderAuthStatus("alpha"); status.Configured {
		t.Fatalf("status = %+v", status)
	}

	// Making the provider configured flips the reads.
	if err := registry.Runtime().SetRuntimeAPIKey("alpha", "sk-alpha", ctxpkg.Background()); err != nil {
		t.Fatal(err)
	}
	if !registry.HasConfiguredAuth(model) {
		t.Fatal("configured auth expected")
	}
	if len(registry.GetAvailable()) != 1 {
		t.Fatalf("available = %+v", registry.GetAvailable())
	}
	status := registry.GetProviderAuthStatus("alpha")
	if !status.Configured || status.Source != "runtime" {
		t.Fatalf("status = %+v", status)
	}
	// GetApiKeyAndHeaders resolves the key.
	resolved := registry.GetAPIKeyAndHeaders(model)
	if !resolved.OK || resolved.APIKey != "sk-alpha" {
		t.Fatalf("resolved = %+v", resolved)
	}
	if key := registry.GetAPIKeyForProvider("alpha"); key != "sk-alpha" {
		t.Fatalf("key = %q", key)
	}
	if key := registry.GetAPIKeyForProvider("beta"); key != "" {
		t.Fatalf("key = %q", key)
	}
	if result, err := registry.GetProviderAuth("alpha"); err != nil || result == nil {
		t.Fatalf("result = %+v err = %v", result, err)
	}

	// GetAvailable returns a copy.
	available := registry.GetAvailable()
	available[0] = nil
	if registry.GetAvailable()[0] == nil {
		t.Fatal("GetAvailable must copy")
	}
}

func TestModelRegistryRequestAuthFallbacks(t *testing.T) {
	registry := registryFor(t, stubProvider("alpha"))
	model := registry.Find("alpha", "alpha-model")

	// Unconfigured and without authHeader: the compatibility config answers.
	resolved := registry.GetAPIKeyAndHeaders(model)
	if !resolved.OK || resolved.Error != "" {
		t.Fatalf("resolved = %+v", resolved)
	}
	// With baseUrl configured, the friendly error appears when authHeader is on
	// and no key can be resolved.
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	if err := os.WriteFile(path, []byte(`{"providers":{"alpha":{"baseUrl":"https://alpha.example.com","authHeader":true}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
		ModelsPath:      path,
		RefreshOnCreate: boolPtr(false),
		Credentials:     newMemoryCredentialStore(),
	})
	if err != nil {
		t.Fatal(err)
	}
	stub := stubProvider("alpha")
	runtime.defaults = map[string]*ai.Provider{"alpha": stub}
	runtime.builtins = map[string]*ai.Provider{"alpha": stub}
	runtime.rebuildProviders()
	composed := NewModelRegistry(runtime).Find("alpha", "alpha-model")
	resolved = NewModelRegistry(runtime).GetAPIKeyAndHeaders(composed)
	if resolved.OK || !strings.Contains(resolved.Error, "No API key found for") {
		t.Fatalf("resolved = %+v", resolved)
	}

	// A stored credential without a key surfaces the authHeader error through
	// the friendly message too.
	keyless := ai.CreateProvider(ai.CreateProviderOptions{
		ID: "alpha", Name: "alpha", BaseURL: "https://alpha.example.com",
		Auth: ai.ProviderAuth{APIKey: &ai.ApiKeyAuth{
			Name: "keyless",
			Resolve: func(input ai.AuthResolveInput) (*ai.AuthResult, error) {
				return &ai.AuthResult{Auth: ai.ModelAuth{APIKey: ""}, Source: "none"}, nil
			},
		}},
		Models: stubProvider("alpha").GetModels(),
		Single: funcStreams{},
	})
	runtime2 := runtimeWithProviders(t, keyless)
	runtime2.config = LoadModelConfig(path)
	if _, err := runtime2.GetAuthForModel(keyless.GetModels()[0], nil); err != nil {
		// The composed provider must exist for the auth path to run.
		t.Fatalf("auth: %v", err)
	}
	runtime2.recomposeProvider("alpha")
	resolved = NewModelRegistry(runtime2).GetAPIKeyAndHeaders(runtime2.GetModel("alpha", "alpha-model"))
	if resolved.OK || resolved.Error == "" {
		t.Fatalf("resolved = %+v", resolved)
	}
	if !strings.Contains(resolved.Error, "authHeader") && !strings.Contains(resolved.Error, "No API key found") {
		t.Fatalf("resolved = %+v", resolved)
	}
}

func TestModelRegistryStreaming(t *testing.T) {
	ctx := ctxpkg.Background()
	registry := registryFor(t, stubProvider("alpha"))
	model := registry.Find("alpha", "alpha-model")

	// Unconfigured streaming fails through the lazy stream.
	message, _ := registry.Stream(model, ai.Context{}, nil).Result(ctx)
	if message.StopReason != ai.StopError || message.ErrorMessage == nil ||
		!strings.Contains(*message.ErrorMessage, "Provider is not configured: alpha") {
		t.Fatalf("message = %+v", message)
	}
	// Complete resolves to the error message rather than returning an error
	// (upstream result() resolves the terminal message).
	completed, err := registry.Complete(model, ai.Context{}, nil)
	if err != nil || completed == nil || completed.StopReason != ai.StopError {
		t.Fatalf("completed = %+v err = %v", completed, err)
	}
	message, _ = registry.StreamSimple(model, ai.Context{}, nil).Result(ctx)
	if message.StopReason != ai.StopError {
		t.Fatalf("message = %+v", message)
	}
	completed, err = registry.CompleteSimple(model, ai.Context{}, nil)
	if err != nil || completed == nil || completed.StopReason != ai.StopError {
		t.Fatalf("completed = %+v err = %v", completed, err)
	}
}

func TestModelRegistryRefresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	if err := os.WriteFile(path, []byte(`{"providers":{"alpha":{"baseUrl":"https://first.example.com","apiKey":"k"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
		ModelsPath:      path,
		RefreshOnCreate: boolPtr(false),
		Credentials:     newMemoryCredentialStore(),
	})
	if err != nil {
		t.Fatal(err)
	}
	stub := stubProvider("alpha")
	runtime.defaults["alpha"] = stub
	runtime.builtins["alpha"] = stub
	runtime.recomposeProvider("alpha")
	registry := NewModelRegistry(runtime)
	if provider := registry.GetProvider("alpha"); provider == nil || provider.BaseURL != "https://first.example.com" {
		t.Fatalf("provider = %+v", provider)
	}

	// Rewriting models.json and refreshing picks up the change.
	if err := os.WriteFile(path, []byte(`{"providers":{"alpha":{"baseUrl":"https://second.example.com","apiKey":"k"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Refresh(ctxpkg.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if provider := registry.GetProvider("alpha"); provider == nil || provider.BaseURL != "https://second.example.com" {
		t.Fatalf("provider after refresh = %+v", provider)
	}
	if errorText := registry.GetError(); errorText != "" {
		t.Fatalf("error = %q", errorText)
	}
}
