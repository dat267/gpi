package ai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Tests for images-models.ts and api/openrouter-images.ts.

func TestImageCatalog(t *testing.T) {
	if err := ImageCatalogError(); err != nil {
		t.Fatalf("catalog error: %v", err)
	}
	providers := GetBuiltinImageProviders()
	if len(providers) != 1 || providers[0] != "openrouter" {
		t.Fatalf("providers = %#v", providers)
	}
	models := GetBuiltinImageModels("openrouter")
	if len(models) == 0 {
		t.Fatal("no image models")
	}
	model := models[0]
	if model.API != KnownImagesApiOpenRouter || model.Provider != "openrouter" ||
		model.BaseURL == "" || len(model.Input) == 0 || len(model.Output) == 0 {
		t.Fatalf("model = %+v", model)
	}
	if found := GetBuiltinImageModel("openrouter", model.ID); found == nil || found.ID != model.ID {
		t.Fatalf("lookup = %+v", found)
	}
	if GetBuiltinImageModel("openrouter", "missing") != nil {
		t.Fatal("unknown models must be nil")
	}
	if models := GetBuiltinImageModels("unknown"); models != nil {
		t.Fatalf("models = %+v", models)
	}
}

func TestImagesModelsRegistry(t *testing.T) {
	registry := CreateImagesModels(nil)
	static := CreateImagesProvider(ImageProviderOptions{
		ID: "static", Models: []*ImagesModel{{ID: "m1", Provider: "static", API: KnownImagesApiOpenRouter}},
		Auth: ProviderAuth{APIKey: EnvApiKeyAuth("Static key", []string{"STATIC_KEY"})},
		API:  OpenRouterImages{},
	})
	registry.SetProvider(static)
	if got := registry.GetProvider("static"); got == nil || got.Name != "static" {
		t.Fatalf("provider = %+v", got)
	}
	if len(registry.GetProviders()) != 1 {
		t.Fatalf("providers = %+v", registry.GetProviders())
	}
	if models := registry.GetModels("static"); len(models) != 1 {
		t.Fatalf("models = %+v", models)
	}
	if model := registry.GetModel("static", "m1"); model == nil {
		t.Fatal("model lookup failed")
	}
	if models := registry.GetModels("unknown"); len(models) != 0 {
		t.Fatalf("models = %+v", models)
	}
	// A provider whose getModels is empty contributes nothing.
	if models := registry.GetModels(""); len(models) != 1 {
		t.Fatalf("models = %+v", models)
	}
	registry.DeleteProvider("static")
	if registry.GetProvider("static") != nil {
		t.Fatal("delete failed")
	}
	registry.SetProvider(static)
	registry.ClearProviders()
	if len(registry.GetProviders()) != 0 {
		t.Fatal("clear failed")
	}
}

func TestImagesModelsRefresh(t *testing.T) {
	// A static provider refreshes as a no-op.
	registry := CreateImagesModels(nil)
	registry.SetProvider(CreateImagesProvider(ImageProviderOptions{
		ID: "static", Models: []*ImagesModel{}, API: OpenRouterImages{},
	}))
	if err := registry.Refresh("static"); err != nil {
		t.Fatal(err)
	}

	// A dynamic provider stores the refreshed list; concurrent calls share one
	// in-flight fetch.
	calls := 0
	dynamic := CreateImagesProvider(ImageProviderOptions{
		ID: "dynamic",
		RefreshModels: func() ([]*ImagesModel, error) {
			calls++
			return []*ImagesModel{{ID: "fresh", Provider: "dynamic", API: KnownImagesApiOpenRouter}}, nil
		},
		API: OpenRouterImages{},
	})
	if len(dynamic.GetModels()) != 0 {
		t.Fatal("a dynamic provider starts empty")
	}
	if err := dynamic.RefreshModels(); err != nil {
		t.Fatal(err)
	}
	if models := dynamic.GetModels(); len(models) != 1 || models[0].ID != "fresh" {
		t.Fatalf("models = %+v", models)
	}

	// A refresh failure keeps the previous list and surfaces as model_source.
	failing := CreateImagesProvider(ImageProviderOptions{
		ID: "failing",
		RefreshModels: func() ([]*ImagesModel, error) {
			return nil, errors.New("network down")
		},
		API: OpenRouterImages{},
	})
	failingRegistry := CreateImagesModels(nil)
	failingRegistry.SetProvider(failing)
	err := failingRegistry.Refresh("failing")
	var modelsErr *ModelsError
	if err == nil || !asModelsError(err, &modelsErr) || modelsErr.Code != "model_source" {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(modelsErr.Error(), "Model refresh failed for failing") {
		t.Fatalf("err = %v", modelsErr)
	}
	// The all-providers refresh never fails.
	if err := failingRegistry.Refresh(""); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestImagesModelsGenerateImagesAuth(t *testing.T) {
	var captured *http.Request
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		captured = request
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &payload)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"resp-1","choices":[{"message":{"content":"here you go","images":[{"image_url":"data:image/png;base64,QUJD"},{"image_url":{"url":"https://example.com/remote.png"}}]}}],"usage":{"prompt_tokens":100,"completion_tokens":200,"prompt_tokens_details":{"cached_tokens":30,"cache_write_tokens":10}}}`))
	}))
	defer server.Close()

	model := &ImagesModel{
		ID: "flux", Name: "FLUX", API: KnownImagesApiOpenRouter, Provider: "openrouter",
		BaseURL: server.URL, Input: []string{"text", "image"}, Output: []string{"image", "text"},
		Cost: ModelCost{Input: 1000000, Output: 2000000, CacheRead: 500000, CacheWrite: 2000000},
	}
	registry := CreateImagesModels(&CreateModelsOptions{Credentials: staticCredentialStore("openrouter", "stored-key")})
	registry.SetProvider(CreateImagesProvider(ImageProviderOptions{
		ID: "openrouter", Models: []*ImagesModel{model},
		Auth: ProviderAuth{APIKey: EnvApiKeyAuth("OpenRouter API key", []string{"OPENROUTER_API_KEY"})},
		API:  OpenRouterImages{},
	}))

	result := registry.GenerateImages(model, ImagesContext{Input: []ImagesInputContent{
		TextContent{Text: "a cat"},
		ImageContent{MimeType: "image/png", Data: "QUJD"},
	}}, nil)
	if result.StopReason != ImagesStopStop {
		t.Fatalf("result = %+v", result)
	}
	// Text plus the base64 data URL image; the remote URL is skipped.
	if len(result.Output) != 2 {
		t.Fatalf("output = %+v", result.Output)
	}
	if text, ok := result.Output[0].(TextContent); !ok || text.Text != "here you go" {
		t.Fatalf("output = %+v", result.Output[0])
	}
	if image, ok := result.Output[1].(ImageContent); !ok || image.MimeType != "image/png" || image.Data != "QUJD" {
		t.Fatalf("output = %+v", result.Output[1])
	}
	if result.ResponseID == nil || *result.ResponseID != "resp-1" {
		t.Fatalf("result = %+v", result)
	}
	// Usage separates cache write from cache read, and cost uses the model
	// pricing (per million tokens).
	if result.Usage == nil {
		t.Fatal("usage missing")
	}
	if result.Usage.Input != 70 || result.Usage.CacheRead != 20 || result.Usage.CacheWrite != 10 ||
		result.Usage.Output != 200 || result.Usage.TotalTokens != 300 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if result.Usage.Cost.Input != 70 || result.Usage.Cost.Output != 400 || result.Usage.Cost.CacheRead != 10 ||
		result.Usage.Cost.CacheWrite != 20 {
		t.Fatalf("cost = %+v", result.Usage.Cost)
	}
	if result.Usage.Cost.Total != result.Usage.Cost.Input+result.Usage.Cost.Output+result.Usage.Cost.CacheRead+result.Usage.Cost.CacheWrite {
		t.Fatalf("cost total = %+v", result.Usage.Cost)
	}

	// The stored credential's key is used and the request carries the protocol.
	if captured.Header.Get("Authorization") != "Bearer stored-key" {
		t.Fatalf("authorization = %q", captured.Header.Get("Authorization"))
	}
	if !strings.HasSuffix(captured.URL.Path, "/chat/completions") {
		t.Fatalf("path = %s", captured.URL.Path)
	}
	modalities, _ := payload["modalities"].([]any)
	if len(modalities) != 2 || modalities[0] != "image" || modalities[1] != "text" {
		t.Fatalf("modalities = %#v", payload["modalities"])
	}
	messages, _ := payload["messages"].([]any)
	if len(messages) != 1 {
		t.Fatalf("messages = %#v", payload["messages"])
	}
	message, _ := messages[0].(map[string]any)
	content, _ := message["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content = %#v", content)
	}
	if part, ok := content[1].(map[string]any); !ok || part["type"] != "image_url" {
		t.Fatalf("content = %#v", content[1])
	}
	if payload["stream"] != false || payload["model"] != "flux" {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestImagesModelsGenerateImagesErrors(t *testing.T) {
	model := &ImagesModel{ID: "flux", API: KnownImagesApiOpenRouter, Provider: "openrouter",
		BaseURL: "https://example.invalid", Output: []string{"image"}}

	// An unknown provider is reported as an error result, not a panic.
	registry := CreateImagesModels(nil)
	result := registry.GenerateImages(model, ImagesContext{}, nil)
	if result.StopReason != ImagesStopError || result.ErrorMessage == nil ||
		*result.ErrorMessage != "Unknown provider: openrouter" {
		t.Fatalf("result = %+v", result)
	}

	// A missing API key is reported by the API implementation.
	registry.SetProvider(CreateImagesProvider(ImageProviderOptions{ID: "openrouter", API: OpenRouterImages{}}))
	result = registry.GenerateImages(model, ImagesContext{}, nil)
	if result.StopReason != ImagesStopError || result.ErrorMessage == nil ||
		!strings.Contains(*result.ErrorMessage, "No API key for provider: openrouter") {
		t.Fatalf("result = %+v", result)
	}

	// An HTTP failure is reported with the provider error message.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer server.Close()
	failing := *model
	failing.BaseURL = server.URL
	result = registry.GenerateImages(&failing, ImagesContext{}, &ImagesOptions{StreamOptions: StreamOptions{APIKey: "key"}})
	if result.StopReason != ImagesStopError || result.ErrorMessage == nil ||
		!strings.Contains(*result.ErrorMessage, "401") {
		t.Fatalf("result = %+v", result)
	}

	// An aborted request is reported as aborted.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result = registry.GenerateImages(&failing, ImagesContext{}, &ImagesOptions{
		StreamOptions: StreamOptions{APIKey: "key", Ctx: ctx},
	})
	if result.StopReason != ImagesStopAborted {
		t.Fatalf("result = %+v", result)
	}
}

func TestOpenRouterImagesProviderFactory(t *testing.T) {
	provider := OpenRouterImagesProvider()
	if provider.ID != "openrouter" || provider.Name != "OpenRouter" {
		t.Fatalf("provider = %+v", provider)
	}
	if provider.Auth.APIKey == nil || provider.Auth.OAuth == nil {
		t.Fatalf("auth = %+v", provider.Auth)
	}
	if len(provider.GetModels()) == 0 {
		t.Fatal("no catalog models")
	}
	registry := BuiltinImagesModels(CreateImagesModels(nil))
	if len(registry.GetProviders()) != 1 || registry.GetProvider("openrouter") == nil {
		t.Fatalf("providers = %+v", registry.GetProviders())
	}
}

// staticCredentialStore wraps the in-memory store with one stored credential.
func staticCredentialStore(providerID, key string) CredentialStore {
	store := NewInMemoryCredentialStore()
	_, _ = store.Modify(providerID, func(*Credential) (*Credential, error) {
		return &Credential{Type: CredentialAPIKey, APIKey: &ApiKeyCredential{Key: key}}, nil
	}, context.Background())
	return store
}
