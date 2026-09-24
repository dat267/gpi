package coding

import (
	ctxpkg "context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/dat267/pier/ai"
)

// Round 100 tests: the ModelRuntime composition/availability/credential
// synchronization and ai.Models login/logout.

type memoryCredentialStore struct {
	mu        sync.Mutex
	entries   map[string]*ai.Credential
	modifyErr error
	deleteErr error
	modifies  int
}

func newMemoryCredentialStore() *memoryCredentialStore {
	return &memoryCredentialStore{entries: map[string]*ai.Credential{}}
}

func (s *memoryCredentialStore) Read(providerID string, ctx ctxpkg.Context) (*ai.Credential, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries[providerID], nil
}

func (s *memoryCredentialStore) List(ctx ctxpkg.Context) ([]ai.CredentialInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ai.CredentialInfo
	for providerID, credential := range s.entries {
		out = append(out, ai.CredentialInfo{ProviderID: providerID, Type: credential.Type})
	}
	return out, nil
}

func (s *memoryCredentialStore) Modify(providerID string, fn func(*ai.Credential) (*ai.Credential, error), ctx ctxpkg.Context) (*ai.Credential, error) {
	if s.modifyErr != nil {
		return nil, s.modifyErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modifies++
	next, err := fn(s.entries[providerID])
	if err != nil {
		return nil, err
	}
	if next == nil {
		return s.entries[providerID], nil
	}
	s.entries[providerID] = next
	return next, nil
}

func (s *memoryCredentialStore) Delete(providerID string, ctx ctxpkg.Context) error {
	if s.deleteErr != nil {
		return s.deleteErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, providerID)
	return nil
}

func TestModelsLoginLogout(t *testing.T) {
	ctx := ctxpkg.Background()
	store := newMemoryCredentialStore()
	var prompted []string
	modelProvider := ai.CreateProvider(ai.CreateProviderOptions{
		ID: "p", Name: "P", Models: nil,
		Auth: ai.ProviderAuth{
			APIKey: &ai.ApiKeyAuth{
				Login: func(interaction *ai.AuthInteraction) (*ai.ApiKeyCredential, error) {
					key, err := interaction.Prompt(ai.AuthPrompt{Type: ai.AuthPromptSecret, Message: "Enter API key"})
					if err != nil {
						return nil, err
					}
					prompted = append(prompted, key)
					return &ai.ApiKeyCredential{Key: key, Env: ai.ProviderEnv{"REGION": "eu"}}, nil
				},
			},
			OAuth: &ai.OAuthAuth{
				Name: "P OAuth",
				Login: func(interaction *ai.AuthInteraction) (*ai.OAuthCredential, error) {
					return &ai.OAuthCredential{OAuthCredentials: ai.OAuthCredentials{
						Access: "access-1", Refresh: "refresh-1", Expires: 1,
					}}, nil
				},
			},
		},
	})
	models := ai.CreateModels(&ai.CreateModelsOptions{Credentials: store})
	models.SetProvider(modelProvider)

	credential, err := models.Login("p", ai.AuthTypeAPIKey, &ai.AuthInteraction{
		Ctx: ctx,
		Prompt: func(prompt ai.AuthPrompt) (string, error) {
			return "sk-entered", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if credential.APIKey == nil || credential.APIKey.Key != "sk-entered" || credential.APIKey.Env["REGION"] != "eu" {
		t.Fatalf("credential = %+v", credential)
	}
	if len(prompted) != 1 || prompted[0] != "sk-entered" {
		t.Fatalf("prompted = %v", prompted)
	}
	stored, _ := store.Read("p", ctx)
	if stored == nil || stored.APIKey.Key != "sk-entered" {
		t.Fatalf("stored = %+v", stored)
	}

	// OAuth login stores an OAuth credential.
	credential, err = models.Login("p", ai.AuthTypeOAuth, &ai.AuthInteraction{Ctx: ctx})
	if err != nil || credential.Type != ai.CredentialOAuth || credential.OAuth.Access != "access-1" {
		t.Fatalf("credential = %+v err = %v", credential, err)
	}
	stored, _ = store.Read("p", ctx)
	if stored == nil || stored.Type != ai.CredentialOAuth {
		t.Fatalf("stored = %+v", stored)
	}

	// An unknown provider, a method without login, and store failures error.
	if _, err := models.Login("nope", ai.AuthTypeAPIKey, &ai.AuthInteraction{Ctx: ctx}); err == nil ||
		err.Error() != "Unknown provider: nope" {
		t.Fatalf("err = %v", err)
	}
	noLogin := ai.CreateProvider(ai.CreateProviderOptions{
		ID: "nologin", Name: "No Login", Models: nil,
		Auth: ai.ProviderAuth{APIKey: &ai.ApiKeyAuth{Name: "env only"}},
	})
	models.SetProvider(noLogin)
	if _, err := models.Login("nologin", ai.AuthTypeAPIKey, &ai.AuthInteraction{Ctx: ctx}); err == nil ||
		err.Error() != "No Login does not support api_key login" {
		t.Fatalf("err = %v", err)
	}
	if _, err := models.Login("nologin", ai.AuthTypeOAuth, &ai.AuthInteraction{Ctx: ctx}); err == nil ||
		err.Error() != "No Login does not support oauth login" {
		t.Fatalf("err = %v", err)
	}

	store.modifyErr = errors.New("disk full")
	if _, err := models.Login("p", ai.AuthTypeAPIKey, &ai.AuthInteraction{
		Ctx:    ctx,
		Prompt: func(ai.AuthPrompt) (string, error) { return "k", nil },
	}); err == nil || !strings.Contains(err.Error(), "Credential store modify failed for p") {
		t.Fatalf("err = %v", err)
	}
	store.modifyErr = nil

	// A cancelled context aborts before the mutation.
	cancelled, cancel := ctxpkg.WithCancel(ctx)
	cancel()
	if _, err := models.Login("p", ai.AuthTypeAPIKey, &ai.AuthInteraction{Ctx: cancelled}); !errors.Is(err, ctxpkg.Canceled) {
		t.Fatalf("err = %v", err)
	}

	// Logout deletes; store failures wrap.
	if err := models.Logout("p", ctx); err != nil {
		t.Fatal(err)
	}
	if stored, _ := store.Read("p", ctx); stored != nil {
		t.Fatalf("stored = %+v", stored)
	}
	store.deleteErr = errors.New("locked")
	if err := models.Logout("p", ctx); err == nil || !strings.Contains(err.Error(), "Credential store delete failed for p") {
		t.Fatalf("err = %v", err)
	}
	store.deleteErr = nil
}

func runtimeWithProviders(t *testing.T, providers ...*ai.Provider) *ModelRuntime {
	t.Helper()
	runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
		DisableModelsJSON: true,
		RefreshOnCreate:   boolPtr(false),
		Credentials:       newMemoryCredentialStore(),
		ModelsStore:       NewInMemoryCodingAgentModelsStore(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) == 0 {
		return runtime
	}
	// Replace the whole provider set with the stubs so recomposition (for
	// example after a credential change) keeps them.
	runtime.defaults = map[string]*ai.Provider{}
	runtime.builtins = map[string]*ai.Provider{}
	for _, provider := range providers {
		runtime.defaults[provider.ID] = provider
		runtime.builtins[provider.ID] = provider
	}
	runtime.rebuildProviders()
	return runtime
}

func stubProvider(id string) *ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID: id, Name: id + " provider", BaseURL: "https://" + id + ".example.com",
		Auth: ai.ProviderAuth{APIKey: &ai.ApiKeyAuth{
			Name: id + " key",
			Resolve: func(input ai.AuthResolveInput) (*ai.AuthResult, error) {
				if input.Credential == nil || input.Credential.Key == "" {
					return nil, nil
				}
				return &ai.AuthResult{
					Auth:   ai.ModelAuth{APIKey: input.Credential.Key},
					Env:    ai.ProviderEnv(input.Credential.Env),
					Source: "stored credential",
				}, nil
			},
		}},
		Models: []*ai.Model{{
			ID: id + "-model", Name: id + " model", API: ai.APIOpenAICompletions, Provider: id,
			BaseURL: "https://" + id + ".example.com", Input: []string{"text"},
			ContextWindow: 1000, MaxTokens: 100,
		}},
		Single: funcStreams{},
	})
}

func TestModelRuntimeCompositionAndSnapshot(t *testing.T) {
	ctx := ctxpkg.Background()
	runtime := runtimeWithProviders(t, stubProvider("alpha"), stubProvider("beta"))

	if len(runtime.GetProviders()) != 2 {
		t.Fatalf("providers = %+v", runtime.GetProviders())
	}
	if runtime.GetModels("") == nil || len(runtime.GetModels("alpha")) != 1 {
		t.Fatalf("models = %+v", runtime.GetModels(""))
	}
	if runtime.GetModel("alpha", "alpha-model") == nil || runtime.GetModel("alpha", "missing") != nil {
		t.Fatal("GetModel")
	}
	if runtime.HasConfiguredAuth("alpha") {
		t.Fatal("no auth configured")
	}
	status := runtime.GetProviderAuthStatus("alpha")
	if status.Configured || status.Source != "" {
		t.Fatalf("status = %+v", status)
	}

	// A runtime key makes the provider available and the status reports it.
	if err := runtime.SetRuntimeAPIKey("alpha", "sk-alpha", ctx); err != nil {
		t.Fatal(err)
	}
	if !runtime.HasConfiguredAuth("alpha") || runtime.HasConfiguredAuth("beta") {
		t.Fatal("configured auth after runtime key")
	}
	status = runtime.GetProviderAuthStatus("alpha")
	if !status.Configured || status.Source != "runtime" {
		t.Fatalf("status = %+v", status)
	}
	if runtime.IsUsingOAuth("alpha") {
		t.Fatal("api key is not oauth")
	}
	available := runtime.GetAvailableSnapshot()
	if len(available) != 1 || available[0].Provider != "alpha" {
		t.Fatalf("available = %+v", available)
	}
	available, err := runtime.GetAvailable("", ctx)
	if err != nil || len(available) != 1 {
		t.Fatalf("available = %+v err = %v", available, err)
	}

	// Removing the key clears availability again.
	if err := runtime.RemoveRuntimeAPIKey("alpha", ctx); err != nil {
		t.Fatal(err)
	}
	if runtime.HasConfiguredAuth("alpha") {
		t.Fatal("configured auth after removal")
	}
	status = runtime.GetProviderAuthStatus("alpha")
	if status.Configured {
		t.Fatalf("status = %+v", status)
	}
}

func TestModelRuntimeCredentialSyncFailure(t *testing.T) {
	ctx := ctxpkg.Background()
	store := newMemoryCredentialStore()
	// A provider that reports a composition failure: models.json needs baseUrl
	// but the provider has none, so composition throws.
	dir := t.TempDir()
	modelsPath := filepath.Join(dir, "models.json")
	if err := os.WriteFile(modelsPath, []byte(`{"providers":{"alpha":{"models":[{"id":"m"}]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
		ModelsPath:      modelsPath,
		RefreshOnCreate: boolPtr(false),
		Credentials:     store,
	})
	if err != nil {
		t.Fatal(err)
	}
	// No base provider for "alpha": the models.json entry cannot resolve an
	// api, so composition fails.

	// Recomposition fails, so the credential change is committed but the
	// synchronization errors with the composition message.
	store.modifyErr = nil
	err = runtime.SetRuntimeAPIKey("alpha", "sk-1", ctx)
	var syncErr *CredentialSynchronizationError
	if !errors.As(err, &syncErr) {
		t.Fatalf("err = %v", err)
	}
	if syncErr.ProviderID != "alpha" || syncErr.Operation != CredentialOperationSetRuntimeAPIKey {
		t.Fatalf("syncErr = %+v", syncErr)
	}
	if !strings.Contains(syncErr.Error(), `no "api" specified`) {
		t.Fatalf("syncErr = %v", syncErr)
	}
	if runtime.GetError() == "" || !strings.Contains(runtime.GetError(), `Provider "alpha"`) {
		t.Fatalf("error = %q", runtime.GetError())
	}
}

func TestModelRuntimeStreamingRequiresAuth(t *testing.T) {
	ctx := ctxpkg.Background()
	runtime := runtimeWithProviders(t, stubProvider("alpha"))
	model := runtime.GetModel("alpha", "alpha-model")
	if model == nil {
		t.Fatal("model missing")
	}
	// No auth configured: streaming errors through the lazy stream.
	stream := runtime.Stream(model, ai.Context{}, &ai.ModelsStreamOptions{})
	message, _ := stream.Result(ctx)
	if message.StopReason != ai.StopError || message.ErrorMessage == nil ||
		!strings.Contains(*message.ErrorMessage, "Provider is not configured: alpha") {
		t.Fatalf("message = %+v", message)
	}
	// An unknown provider fails too.
	unknown := &ai.Model{ID: "x", API: ai.APIOpenAICompletions, Provider: "nope"}
	stream = runtime.StreamSimple(unknown, ai.Context{}, nil)
	message, _ = stream.Result(ctx)
	if message.ErrorMessage == nil || !strings.Contains(*message.ErrorMessage, "Unknown provider: nope") {
		t.Fatalf("message = %+v", message)
	}
}

func TestModelRuntimeGetAuthForModelHeaders(t *testing.T) {
	dir := t.TempDir()
	modelsPath := filepath.Join(dir, "models.json")
	if err := os.WriteFile(modelsPath, []byte(`{"providers":{"alpha":{
		"baseUrl":"https://beta.example.com",
		"headers":{"X-Provider":"p"},
		"models":[{"id":"alpha-model","headers":{"X-Model":"m"}}]
	}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
		ModelsPath:      modelsPath,
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

	model := runtime.GetModel("alpha", "alpha-model")
	if model == nil {
		t.Fatal("model missing")
	}
	// Provider headers are not part of the model header set; the model's own
	// headers are.
	resolution, err := runtime.GetAuthForModel(model, &ModelRuntimeAuthOverrides{APIKey: "sk-1"})
	if err != nil || resolution == nil {
		t.Fatalf("resolution = %+v err = %v", resolution, err)
	}
	if resolution.Auth.APIKey != "sk-1" || resolution.Auth.Headers["X-Model"] == nil ||
		*resolution.Auth.Headers["X-Model"] != "m" {
		t.Fatalf("resolution = %+v", resolution)
	}
	// Provider headers arrive through the composed auth path (upstream merges
	// them in composeApiKeyAuth), while model headers are merged here.
	if resolution.Auth.Headers["X-Provider"] == nil || *resolution.Auth.Headers["X-Provider"] != "p" {
		t.Fatalf("provider headers missing: %+v", resolution.Auth.Headers)
	}
}

func TestModelRuntimeBaseURLOverride(t *testing.T) {
	ctx := ctxpkg.Background()
	runtime := runtimeWithProviders(t, stubProvider("alpha"))
	base := ai.CreateProvider(ai.CreateProviderOptions{
		ID: "alpha", Name: "alpha", BaseURL: "https://orig.example.com",
		Auth: ai.ProviderAuth{APIKey: &ai.ApiKeyAuth{
			Name: "alpha key",
			Resolve: func(input ai.AuthResolveInput) (*ai.AuthResult, error) {
				return &ai.AuthResult{
					Auth:   ai.ModelAuth{APIKey: "sk", BaseURL: "https://override.example.com"},
					Source: "test",
				}, nil
			},
		}},
		Models: []*ai.Model{{
			ID: "alpha-model", API: ai.APIOpenAICompletions, Provider: "alpha",
			BaseURL: "https://orig.example.com", Input: []string{"text"},
			ContextWindow: 1, MaxTokens: 1,
		}},
		Single: funcStreams{},
	})
	runtime.defaults["alpha"] = base
	runtime.builtins["alpha"] = base
	runtime.recomposeProvider("alpha")
	model := runtime.GetModel("alpha", "alpha-model")

	prepared, err := runtime.prepareStreamRequest(model, &ai.ModelsStreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Model.BaseURL != "https://override.example.com" {
		t.Fatalf("baseUrl = %s", prepared.Model.BaseURL)
	}
	if prepared.Options.APIKey != "sk" {
		t.Fatalf("apiKey = %s", prepared.Options.APIKey)
	}
	// The original model is untouched.
	if model.BaseURL != "https://orig.example.com" {
		t.Fatalf("model mutated: %+v", model)
	}
	_ = ctx
}

func TestCreateModelRuntimeDefaults(t *testing.T) {
	// modelsPath null disables models.json and uses the in-memory store.
	runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
		DisableModelsJSON: true,
		RefreshOnCreate:   boolPtr(false),
		Credentials:       newMemoryCredentialStore(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.modelsPath != "" {
		t.Fatalf("modelsPath = %q", runtime.modelsPath)
	}
	if len(runtime.GetModels("")) == 0 {
		t.Fatal("built-in models expected")
	}
	if runtime.GetError() != "" {
		t.Fatalf("error = %q", runtime.GetError())
	}

	// A models.json error surfaces through getError.
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	if err := os.WriteFile(path, []byte("{ bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken, err := CreateModelRuntime(CreateModelRuntimeOptions{
		ModelsPath:      path,
		RefreshOnCreate: boolPtr(false),
		Credentials:     newMemoryCredentialStore(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(broken.GetError(), "Failed to parse models.json:") {
		t.Fatalf("error = %q", broken.GetError())
	}
}

func TestModelRuntimeRadiusGateway(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	if err := os.WriteFile(path, []byte(`{"providers":{"radius-custom":{"oauth":"radius","baseUrl":"https://gw.example.com/v1"}}}`), 0o600); err != nil {
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
	provider := runtime.GetProvider("radius-custom")
	if provider == nil || provider.Name != "radius-custom" {
		t.Fatalf("provider = %+v", provider)
	}
	if provider.Auth.OAuth == nil {
		t.Fatalf("radius provider must expose oauth: %+v", provider.Auth)
	}
	// The composed provider keeps the configured baseUrl; the gateway handed to
	// the Radius provider is the /v1-stripped form.
	if provider.BaseURL != "https://gw.example.com/v1" {
		t.Fatalf("baseUrl = %s", provider.BaseURL)
	}
}

// models.json is the agent dir's file, never the cwd's. Upstream resolves the
// agent dir once (`main.ts`) and its cwd-bound services carry it over unchanged,
// so switching sessions never changes which models.json is read. This pins the
// default path because the D160 writeup used to claim a per-project models.json,
// which does not exist upstream.
func TestModelRuntimeReadsModelsJSONFromTheAgentDir(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
	write := func(dir string) {
		t.Helper()
		content := `{"providers":{"custom":{"baseUrl":"https://custom.example.com/v1","api":"openai-completions",` +
			`"models":[{"id":"custom-model","name":"Custom Model"}]}}}`
		if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	no := false
	build := func() *ModelRuntime {
		t.Helper()
		runtime, err := CreateModelRuntime(CreateModelRuntimeOptions{
			RefreshOnCreate: &no, // no network, no catalog fetch
			Credentials:     newMemoryCredentialStore(),
		})
		if err != nil {
			t.Fatal(err)
		}
		return runtime
	}

	// With no explicit path, the agent dir's file is the one that loads.
	write(agentDir)
	runtime := build()
	if runtime.GetProvider("custom") == nil || len(runtime.GetModels("custom")) == 0 {
		t.Fatalf("agent dir models.json not loaded; providers=%v", runtime.providerIDs())
	}

	// A models.json in the working directory is not consulted: the path follows
	// the agent dir alone, as upstream's does.
	cwd := t.TempDir()
	write(cwd)
	t.Chdir(cwd)
	if err := os.Remove(filepath.Join(agentDir, "models.json")); err != nil {
		t.Fatal(err)
	}
	if runtime := build(); runtime.GetProvider("custom") != nil {
		t.Fatalf("the cwd's models.json was loaded; providers=%v", runtime.providerIDs())
	}
}
