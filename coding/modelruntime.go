package coding

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/dat267/gpi/ai"
	"github.com/dat267/gpi/ai/providers"
)

// Port of core/model-runtime.ts: the configured pi-ai Models collection used by
// the coding agent and SDK consumers.
//
// D37: upstream wraps every static built-in provider with the pi.dev remote
// catalog overlay (core/remote-catalog-provider.ts). That overlay is unported,
// so the runtime uses the built-in provider factories as-is. D35 also applies:
// extension provider registration is extension mechanics and omitted.

// ModelRuntimeSnapshot is the cached provider/model/auth view.
type ModelRuntimeSnapshot struct {
	All                 []*ai.Model
	Available           []*ai.Model
	ConfiguredProviders map[string]bool
	StoredProviders     map[string]bool
	Auth                map[string]*ai.AuthCheck
}

// CreateModelRuntimeOptions configure the runtime.
type CreateModelRuntimeOptions struct {
	// Credentials overrides the credential store. Defaults to the auth.json
	// storage at AuthPath.
	Credentials ai.CredentialStore
	AuthPath    string
	// ModelsPath is the models.json path; empty means the default under the
	// agent dir. DisableModelsJSON omits models.json entirely (upstream's
	// modelsPath: null).
	ModelsPath        string
	DisableModelsJSON bool
	ModelsStore       ai.ModelsStore
	ModelsStorePath   string
	// AllowModelNetwork lets create() refresh catalogs over the network.
	AllowModelNetwork bool
	// ModelRefreshTimeoutMS bounds the create-time network refresh.
	ModelRefreshTimeoutMS *int64
	CatalogBaseURL        string
	// Signal cancels initial cache restoration and availability checks.
	Signal context.Context
	// RefreshOnCreate false skips the initial refresh (default true).
	RefreshOnCreate *bool
}

// ModelRuntimeAuthOverrides are per-request auth overrides.
type ModelRuntimeAuthOverrides struct {
	APIKey string
	Env    map[string]string
	// MinOAuthValidityMS requires this much remaining OAuth validity; defaults
	// to five minutes.
	MinOAuthValidityMS *int64
}

// CredentialSynchronizationOperation describes a committed credential change.
type CredentialSynchronizationOperation = string

const (
	CredentialOperationLogin               CredentialSynchronizationOperation = "login"
	CredentialOperationLogout              CredentialSynchronizationOperation = "logout"
	CredentialOperationSetRuntimeAPIKey    CredentialSynchronizationOperation = "setRuntimeApiKey"
	CredentialOperationRemoveRuntimeAPIKey CredentialSynchronizationOperation = "removeRuntimeApiKey"
)

// CredentialSynchronizationError reports a committed credential change whose
// local model/auth snapshot could not be synchronized.
type CredentialSynchronizationError struct {
	ProviderID string
	Operation  CredentialSynchronizationOperation
	Credential *ai.Credential
	Cause      error
}

func (e *CredentialSynchronizationError) Error() string {
	return fmt.Sprintf("Credential %s committed for %s, but local synchronization failed: %v",
		e.Operation, e.ProviderID, e.Cause)
}

func (e *CredentialSynchronizationError) Unwrap() error { return e.Cause }

// ModelRuntime is the configured model collection.
type ModelRuntime struct {
	models     *ai.Models
	creds      *RuntimeCredentials
	defaults   map[string]*ai.Provider
	builtins   map[string]*ai.Provider
	modelsPath string
	network    bool
	config     *ModelConfig

	mu sync.Mutex
	// refreshMu serializes the state-mutating part of Refresh (D121).
	refreshMu          sync.Mutex
	snapshot           ModelRuntimeSnapshot
	availabilitySeq    int
	availabilityErrSeq int
	providerSeq        map[string]int
	availabilityError  string
	credentialOps      map[string]chan struct{}
	compositionErrors  map[string]string
}

// CreateModelRuntime builds the runtime.
func CreateModelRuntime(options CreateModelRuntimeOptions) (*ModelRuntime, error) {
	if options.Credentials == nil {
		if options.AuthPath == "" {
			options.AuthPath = filepath.Join(GetAgentDir(), "auth.json")
		}
		options.Credentials = NewAuthStorage(options.AuthPath)
	}
	credentials := NewRuntimeCredentials(options.Credentials)

	modelsPath := options.ModelsPath
	if modelsPath == "" && !options.DisableModelsJSON {
		modelsPath = filepath.Join(GetAgentDir(), "models.json")
	}
	if options.DisableModelsJSON {
		modelsPath = ""
	}
	config := LoadModelConfig(modelsPath)

	var modelsStore ai.ModelsStore
	if options.ModelsStore != nil {
		modelsStore = options.ModelsStore
	} else if modelsPath != "" {
		storePath := options.ModelsStorePath
		if storePath == "" {
			storePath = filepath.Join(filepath.Dir(modelsPath), "models-store.json")
		}
		modelsStore = NewFileModelsStore(storePath)
	} else {
		modelsStore = NewInMemoryCodingAgentModelsStore()
	}

	runtime := &ModelRuntime{
		creds:             credentials,
		config:            config,
		modelsPath:        modelsPath,
		network:           isOfflineDisabled(),
		defaults:          map[string]*ai.Provider{},
		builtins:          map[string]*ai.Provider{},
		providerSeq:       map[string]int{},
		credentialOps:     map[string]chan struct{}{},
		compositionErrors: map[string]string{},
		snapshot: ModelRuntimeSnapshot{
			ConfiguredProviders: map[string]bool{},
			StoredProviders:     map[string]bool{},
			Auth:                map[string]*ai.AuthCheck{},
		},
	}
	for _, provider := range providers.BuiltinProviders() {
		runtime.defaults[provider.ID] = provider
	}
	for id, provider := range runtime.defaults {
		runtime.builtins[id] = provider
	}
	runtime.models = ai.CreateModels(&ai.CreateModelsOptions{Credentials: credentials, ModelsStore: modelsStore})
	runtime.rebuildProviders()
	runtime.configureRadiusProviders()
	runtime.rebuildProviders()

	refreshFromNetwork := runtime.network && options.AllowModelNetwork
	ctx := options.Signal
	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	if refreshFromNetwork && options.ModelRefreshTimeoutMS != nil {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(*options.ModelRefreshTimeoutMS)*time.Millisecond)
	}
	refreshOnCreate := options.RefreshOnCreate == nil || *options.RefreshOnCreate
	if refreshOnCreate {
		_, _ = runtime.Refresh(ctx, &ModelsRefreshCallOptions{
			AllowNetwork: &refreshFromNetwork, AllProviders: true,
		})
	}
	if cancel != nil {
		cancel()
	}
	return runtime, nil
}

// isOfflineDisabled reports whether network access is allowed (PI_OFFLINE unset).
func isOfflineDisabled() bool {
	_, set := lookupEnv("PI_OFFLINE")
	return !set
}

func lookupEnv(name string) (string, bool) { return os.LookupEnv(name) }

// configureRadiusProviders replaces built-in providers with gateway-configured
// Radius providers from models.json.
func (r *ModelRuntime) configureRadiusProviders() {
	r.builtins = map[string]*ai.Provider{}
	for id, provider := range r.defaults {
		r.builtins[id] = provider
	}
	for _, providerID := range r.config.GetProviderIDs() {
		config := r.config.GetProvider(providerID)
		if config == nil || config.OAuth != "radius" || config.BaseURL == "" {
			continue
		}
		name := config.Name
		if name == "" {
			name = providerID
		}
		r.builtins[providerID] = ai.RadiusProvider(ai.RadiusProviderOptions{
			ID:      providerID,
			Name:    name,
			Gateway: radiusGatewayFromBaseURL(config.BaseURL),
		})
	}
}

var radiusVersionSuffix = regexp.MustCompile(`/v1/?$`)

// radiusGatewayFromBaseURL strips a trailing /v1 from a gateway base url.
func radiusGatewayFromBaseURL(baseURL string) string {
	return radiusVersionSuffix.ReplaceAllString(baseURL, "")
}

// providerIDs lists every known provider id.
func (r *ModelRuntime) providerIDs() []string {
	seen := map[string]bool{}
	var out []string
	for id := range r.builtins {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, id := range r.config.GetProviderIDs() {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// recomposeProvider rebuilds one provider from the built-in and models.json
// layers.
func (r *ModelRuntime) recomposeProvider(providerID string) {
	base := r.builtins[providerID]
	config := r.config.GetProvider(providerID)
	if base == nil && config == nil {
		r.models.DeleteProvider(providerID)
		delete(r.compositionErrors, providerID)
		return
	}
	if base != nil && config == nil {
		// No overlays: use the builtin untouched so its auth/login/stream
		// behavior is exact.
		r.models.SetProvider(base)
		delete(r.compositionErrors, providerID)
		return
	}
	provider, err := ComposeModelProvider(providerID, base, r.config)
	if err != nil {
		r.compositionErrors[providerID] = err.Error()
		if base != nil {
			r.models.SetProvider(base)
		} else {
			r.models.DeleteProvider(providerID)
		}
		return
	}
	r.models.SetProvider(provider)
	delete(r.compositionErrors, providerID)
}

// rebuildProviders recomposes every provider.
func (r *ModelRuntime) rebuildProviders() {
	r.models.ClearProviders()
	r.mu.Lock()
	r.compositionErrors = map[string]string{}
	r.mu.Unlock()
	for _, providerID := range r.providerIDs() {
		r.recomposeProvider(providerID)
	}
	r.updateModelSnapshot()
}

// updateModelSnapshot refreshes the cached model view.
func (r *ModelRuntime) updateModelSnapshot() {
	all := r.models.GetModels("")
	r.mu.Lock()
	available := make([]*ai.Model, 0, len(all))
	for _, model := range all {
		if r.snapshot.ConfiguredProviders[model.Provider] {
			available = append(available, model)
		}
	}
	r.snapshot.All = all
	r.snapshot.Available = available
	r.mu.Unlock()
}

// runAvailabilityRefresh runs one full availability pass.
func (r *ModelRuntime) runAvailabilityRefresh(seq, errorSeq int, ctx context.Context) {
	allProviders := r.models.GetProviders()
	ids := make([]string, 0, len(allProviders))
	for _, provider := range allProviders {
		ids = append(ids, provider.ID)
	}

	type checkResult struct {
		id    string
		check *ai.AuthCheck
	}
	checks := make([]checkResult, len(ids))
	availableByProvider := make([][]*ai.Model, len(ids))
	var waitGroup sync.WaitGroup
	for index, id := range ids {
		waitGroup.Add(1)
		go func(index int, id string) {
			defer waitGroup.Done()
			available, err := r.models.GetAvailable(id, ctx)
			if err == nil {
				availableByProvider[index] = available
			}
			check, err := r.models.CheckAuth(id, ctx)
			if err == nil {
				checks[index] = checkResult{id: id, check: check}
			}
		}(index, id)
	}
	credentials, credErr := r.creds.List(ctx)
	waitGroup.Wait()
	if credErr != nil {
		credentials = nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if seq != r.availabilitySeq {
		return
	}
	configured := map[string]bool{}
	auth := map[string]*ai.AuthCheck{}
	for _, entry := range checks {
		if entry.check != nil {
			configured[entry.id] = true
			auth[entry.id] = entry.check
		}
	}
	stored := map[string]bool{}
	for _, entry := range credentials {
		stored[entry.ProviderID] = true
	}
	var available []*ai.Model
	for _, models := range availableByProvider {
		available = append(available, models...)
	}
	r.snapshot = ModelRuntimeSnapshot{
		All:                 r.models.GetModels(""),
		Available:           available,
		ConfiguredProviders: configured,
		StoredProviders:     stored,
		Auth:                auth,
	}
	if errorSeq == r.availabilityErrSeq {
		r.availabilityError = ""
	}
}

// queueAvailabilityRefresh starts a full availability pass.
func (r *ModelRuntime) queueAvailabilityRefresh(ctx context.Context) {
	r.mu.Lock()
	r.availabilitySeq++
	seq := r.availabilitySeq
	for id, providerSeq := range r.providerSeq {
		r.providerSeq[id] = providerSeq + 1
	}
	r.availabilityErrSeq++
	errorSeq := r.availabilityErrSeq
	r.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}
	r.runAvailabilityRefresh(seq, errorSeq, ctx)
}

// refreshProviderAvailability refreshes one provider's availability.
func (r *ModelRuntime) refreshProviderAvailability(providerID string, ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	r.availabilitySeq++
	providerSeq := r.providerSeq[providerID] + 1
	r.providerSeq[providerID] = providerSeq
	r.availabilityErrSeq++
	errorSeq := r.availabilityErrSeq
	r.mu.Unlock()

	available, availableErr := r.models.GetAvailable(providerID, ctx)
	auth, authErr := r.models.CheckAuth(providerID, ctx)
	credential, credErr := r.creds.Read(providerID, ctx)
	if availableErr != nil {
		r.recordAvailabilityError(providerID, providerSeq, errorSeq, availableErr, ctx)
		return availableErr
	}
	if authErr != nil {
		r.recordAvailabilityError(providerID, providerSeq, errorSeq, authErr, ctx)
		return authErr
	}
	if credErr != nil {
		r.recordAvailabilityError(providerID, providerSeq, errorSeq, credErr, ctx)
		return credErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providerSeq[providerID] != providerSeq {
		return nil
	}
	configured := map[string]bool{}
	for id := range r.snapshot.ConfiguredProviders {
		configured[id] = true
	}
	stored := map[string]bool{}
	for id := range r.snapshot.StoredProviders {
		stored[id] = true
	}
	authByProvider := map[string]*ai.AuthCheck{}
	for id, check := range r.snapshot.Auth {
		authByProvider[id] = check
	}
	if auth != nil {
		configured[providerID] = true
		authByProvider[providerID] = auth
	} else {
		delete(configured, providerID)
		delete(authByProvider, providerID)
	}
	if credential != nil {
		stored[providerID] = true
	} else {
		delete(stored, providerID)
	}
	all := r.models.GetModels("")
	availableByID := map[string]*ai.Model{}
	for _, model := range r.snapshot.Available {
		if model.Provider != providerID {
			availableByID[model.Provider+"\x00"+model.ID] = model
		}
	}
	for _, model := range available {
		availableByID[model.Provider+"\x00"+model.ID] = model
	}
	availableModels := make([]*ai.Model, 0, len(all))
	for _, model := range all {
		if kept, ok := availableByID[model.Provider+"\x00"+model.ID]; ok && kept != nil {
			availableModels = append(availableModels, kept)
		}
	}
	r.snapshot = ModelRuntimeSnapshot{
		All:                 all,
		Available:           availableModels,
		ConfiguredProviders: configured,
		StoredProviders:     stored,
		Auth:                authByProvider,
	}
	if errorSeq == r.availabilityErrSeq {
		r.availabilityError = ""
	}
	return nil
}

func (r *ModelRuntime) recordAvailabilityError(providerID string, providerSeq, errorSeq int, err error, ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providerSeq[providerID] == providerSeq && errorSeq == r.availabilityErrSeq && ctx.Err() == nil {
		r.availabilityError = err.Error()
	}
}

// GetProviders returns the composed providers.
func (r *ModelRuntime) GetProviders() []*ai.Provider { return r.models.GetProviders() }

// GetProvider returns one composed provider.
func (r *ModelRuntime) GetProvider(providerID string) *ai.Provider {
	return r.models.GetProvider(providerID)
}

// GetModels returns models for a provider (all when empty).
func (r *ModelRuntime) GetModels(providerID string) []*ai.Model {
	return r.models.GetModels(providerID)
}

// GetModel finds one model.
func (r *ModelRuntime) GetModel(providerID, modelID string) *ai.Model {
	return r.models.GetModel(providerID, modelID)
}

// CheckAuth reports a provider's auth check.
func (r *ModelRuntime) CheckAuth(providerID string, ctx context.Context) (*ai.AuthCheck, error) {
	return r.models.CheckAuth(providerID, ctx)
}

// GetAvailable returns the available models for a provider, or the cached
// snapshot for every provider.
func (r *ModelRuntime) GetAvailable(providerID string, ctx context.Context) ([]*ai.Model, error) {
	if providerID != "" {
		r.mu.Lock()
		r.availabilityErrSeq++
		errorSeq := r.availabilityErrSeq
		r.mu.Unlock()
		available, err := r.models.GetAvailable(providerID, ctx)
		r.mu.Lock()
		if err == nil {
			if errorSeq == r.availabilityErrSeq {
				r.availabilityError = ""
			}
		} else if errorSeq == r.availabilityErrSeq && (ctx == nil || ctx.Err() == nil) {
			r.availabilityError = err.Error()
		}
		r.mu.Unlock()
		return available, err
	}
	r.queueAvailabilityRefresh(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot.Available, nil
}

// GetAvailableSnapshot returns the cached available models.
func (r *ModelRuntime) GetAvailableSnapshot() []*ai.Model {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot.Available
}

// GetError reports config, composition, and availability errors.
func (r *ModelRuntime) GetError() string {
	errors := []string{}
	if configError := r.config.GetError(); configError != "" {
		errors = append(errors, configError)
	}
	r.mu.Lock()
	for providerID, err := range r.compositionErrors {
		errors = append(errors, fmt.Sprintf("Provider %q: %s", providerID, err))
	}
	if r.availabilityError != "" {
		errors = append(errors, "Availability refresh: "+r.availabilityError)
	}
	r.mu.Unlock()
	if len(errors) == 0 {
		return ""
	}
	return strings.Join(errors, "\n\n")
}

// GetCompatibilityRequestConfig is the compatibility fallback used when a
// provider's auth is unconfigured.
func (r *ModelRuntime) GetCompatibilityRequestConfig(model *ai.Model) (CompatibilityRequestConfig, error) {
	return ResolveCompatibilityRequestConfig(model, r.config.GetProvider(model.Provider))
}

// IsUsingOAuth reports whether the provider's resolved auth is OAuth.
func (r *ModelRuntime) IsUsingOAuth(providerID string) bool {
	r.mu.Lock()
	check := r.snapshot.Auth[providerID]
	r.mu.Unlock()
	return check != nil && check.Type == ai.AuthTypeOAuth
}

// IsUsingSubscription reports whether the provider uses a subscription-backed
// OAuth method.
func (r *ModelRuntime) IsUsingSubscription(providerID string) bool {
	if !r.IsUsingOAuth(providerID) {
		return false
	}
	provider := r.models.GetProvider(providerID)
	return provider != nil && provider.Auth.OAuth != nil && provider.Auth.OAuth.IsSubscription
}

// HasConfiguredAuth reports whether the provider has configured auth.
func (r *ModelRuntime) HasConfiguredAuth(providerID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot.ConfiguredProviders[providerID]
}

// GetAuth resolves provider auth. For a model, configured model headers are
// merged into the result.
func (r *ModelRuntime) GetAuth(providerID string, overrides *ModelRuntimeAuthOverrides) (*ai.AuthResult, error) {
	return r.models.GetAuth(providerID, authOverrides(overrides))
}

// GetAuthForModel resolves model auth with configured model headers.
func (r *ModelRuntime) GetAuthForModel(model *ai.Model, overrides *ModelRuntimeAuthOverrides) (*ai.AuthResult, error) {
	resolution, err := r.models.GetAuthForModel(model, authOverrides(overrides))
	if err != nil || resolution == nil {
		return resolution, err
	}
	env := map[string]string{}
	for key, value := range resolution.Env {
		env[key] = value
	}
	if overrides != nil {
		for key, value := range overrides.Env {
			env[key] = value
		}
	}
	configuredHeaders, err := ResolveConfiguredModelHeaders(model, r.config.GetProvider(model.Provider), env)
	if err != nil {
		return nil, err
	}
	return &ai.AuthResult{
		Auth: ai.ModelAuth{
			APIKey:  resolution.Auth.APIKey,
			BaseURL: resolution.Auth.BaseURL,
			Headers: ai.MergeHeaders(resolution.Auth.Headers, stringHeaders(configuredHeaders)),
		},
		Env:    resolution.Env,
		Source: resolution.Source,
	}, nil
}

func authOverrides(overrides *ModelRuntimeAuthOverrides) *ai.AuthResolutionOverrides {
	if overrides == nil {
		return nil
	}
	resolved := &ai.AuthResolutionOverrides{APIKey: overrides.APIKey, Env: overrides.Env}
	if overrides.MinOAuthValidityMS != nil {
		resolved.MinOAuthValidityMS = *overrides.MinOAuthValidityMS
	}
	return resolved
}

func stringHeaders(headers map[string]string) ai.ProviderHeaders {
	if len(headers) == 0 {
		return nil
	}
	out := ai.ProviderHeaders{}
	for key, value := range headers {
		copied := value
		out[key] = &copied
	}
	return out
}

// enqueueCredentialOperation serializes credential changes per provider.
func (r *ModelRuntime) enqueueCredentialOperation(providerID string, ctx context.Context, task func() error) error {
	r.mu.Lock()
	previous := r.credentialOps[providerID]
	started := make(chan struct{})
	done := make(chan struct{})
	r.credentialOps[providerID] = done
	r.mu.Unlock()

	result := make(chan error, 1)
	go func() {
		defer close(done)
		if previous != nil {
			select {
			case <-previous:
			case <-ctx.Done():
				result <- ctx.Err()
				return
			}
		}
		if err := ctx.Err(); err != nil {
			result <- err
			return
		}
		close(started)
		result <- task()
	}()

	select {
	case <-started:
		err := <-result
		r.clearCredentialOp(providerID, done)
		return err
	case err := <-result:
		r.clearCredentialOp(providerID, done)
		return err
	case <-ctx.Done():
		// Stop waiting while the operation settles in the background.
		go func() {
			<-result
			r.clearCredentialOp(providerID, done)
		}()
		return ctx.Err()
	}
}

func (r *ModelRuntime) clearCredentialOp(providerID string, done chan struct{}) {
	r.mu.Lock()
	if r.credentialOps[providerID] == done {
		delete(r.credentialOps, providerID)
	}
	r.mu.Unlock()
}

// synchronizeCredentialState recomposes the provider and refreshes its
// availability after a credential change.
func (r *ModelRuntime) synchronizeCredentialState(providerID string, operation CredentialSynchronizationOperation, credential *ai.Credential, ctx context.Context) error {
	fail := func(cause error) error {
		return &CredentialSynchronizationError{ProviderID: providerID, Operation: operation, Credential: credential, Cause: cause}
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	r.recomposeProvider(providerID)
	r.mu.Lock()
	compositionError := r.compositionErrors[providerID]
	r.mu.Unlock()
	if compositionError != "" {
		return fail(fmt.Errorf("%s", compositionError))
	}
	result := r.models.Refresh(&ai.ModelsRefreshOptions{Providers: []string{providerID}, Ctx: ctx})
	if result.Aborted {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
	}
	if err, ok := result.Errors[providerID]; ok && err != nil {
		return fail(err)
	}
	r.updateModelSnapshot()
	if err := r.refreshProviderAvailability(providerID, ctx); err != nil {
		return fail(err)
	}
	return nil
}

// SetRuntimeAPIKey stores a non-persistent runtime API key.
func (r *ModelRuntime) SetRuntimeAPIKey(providerID, apiKey string, ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return r.enqueueCredentialOperation(providerID, ctx, func() error {
		r.creds.SetRuntimeAPIKey(providerID, apiKey)
		return r.synchronizeCredentialState(providerID, CredentialOperationSetRuntimeAPIKey,
			&ai.Credential{Type: ai.CredentialAPIKey, APIKey: &ai.ApiKeyCredential{Key: apiKey}}, ctx)
	})
}

// RemoveRuntimeAPIKey drops a runtime API key.
func (r *ModelRuntime) RemoveRuntimeAPIKey(providerID string, ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return r.enqueueCredentialOperation(providerID, ctx, func() error {
		r.creds.RemoveRuntimeAPIKey(providerID)
		return r.synchronizeCredentialState(providerID, CredentialOperationRemoveRuntimeAPIKey, nil, ctx)
	})
}

// ListCredentials lists stored credential metadata plus runtime keys.
func (r *ModelRuntime) ListCredentials(ctx context.Context) ([]ai.CredentialInfo, error) {
	return r.creds.List(ctx)
}

// GetProviderAuthStatus reports how a provider's auth is satisfied.
func (r *ModelRuntime) GetProviderAuthStatus(providerID string) AuthStatus {
	if r.creds.HasRuntimeAPIKey(providerID) {
		return AuthStatus{Configured: true, Source: "runtime"}
	}
	r.mu.Lock()
	stored := r.snapshot.StoredProviders[providerID]
	check := r.snapshot.Auth[providerID]
	r.mu.Unlock()
	if stored {
		return AuthStatus{Configured: true, Source: "stored"}
	}
	if configured := ConfiguredRequestAuthStatus(r.config.GetProvider(providerID)); configured != nil {
		return *configured
	}
	if check != nil {
		return AuthStatus{Configured: true, Source: "environment", Label: check.Source}
	}
	return AuthStatus{Configured: false}
}

// PreparedRequest is a request with auth applied.
type PreparedRequest struct {
	Provider *ai.Provider
	Model    *ai.Model
	// Options carries the merged request options. Callers pass the concrete
	// options struct they received; the runtime merges auth into it.
	StreamOptions *ai.StreamOptions
	SimpleOptions *ai.SimpleStreamOptions
}

// PrepareRequest resolves auth for a model request (port of prepareRequest).
func (r *ModelRuntime) PrepareRequest(model *ai.Model, apiKey string, env map[string]string, ctx context.Context,
	headers ai.ProviderHeaders, transformHeaders func(ai.ProviderHeaders) ai.ProviderHeaders) (*PreparedRequest, error) {
	provider := r.models.GetProvider(model.Provider)
	if provider == nil {
		return nil, ai.NewModelsError(ai.ErrCodeProvider, fmt.Sprintf("Unknown provider: %s", model.Provider), nil)
	}
	resolution, err := r.GetAuthForModel(model, &ModelRuntimeAuthOverrides{APIKey: apiKey, Env: env})
	if err != nil {
		return nil, err
	}
	if resolution == nil {
		return nil, ai.NewModelsError(ai.ErrCodeAuth, fmt.Sprintf("Provider is not configured: %s", model.Provider), nil)
	}
	merged := ai.MergeHeaders(resolution.Auth.Headers, headers)
	if transformHeaders != nil {
		merged = transformHeaders(merged)
	}
	mergedEnv := map[string]string{}
	for key, value := range resolution.Env {
		mergedEnv[key] = value
	}
	for key, value := range env {
		mergedEnv[key] = value
	}
	requestModel := model
	if resolution.Auth.BaseURL != "" {
		copied := *model
		copied.BaseURL = resolution.Auth.BaseURL
		requestModel = &copied
	}
	resolvedAPIKey := apiKey
	if resolvedAPIKey == "" {
		resolvedAPIKey = resolution.Auth.APIKey
	}
	var finalEnv map[string]string
	if len(mergedEnv) > 0 {
		finalEnv = mergedEnv
	}
	return &PreparedRequest{
		Provider: provider,
		Model:    requestModel,
		StreamOptions: &ai.StreamOptions{
			APIKey: resolvedAPIKey, Headers: merged, Env: finalEnv,
		},
		SimpleOptions: &ai.SimpleStreamOptions{
			StreamOptions: ai.StreamOptions{APIKey: resolvedAPIKey, Headers: merged, Env: finalEnv},
		},
	}, nil
}

// Stream streams a model response with auth applied.
func (r *ModelRuntime) Stream(model *ai.Model, context ai.Context, options *ai.ModelsStreamOptions) *ai.AssistantMessageEventStream {
	transcript := ai.NormalizeContext(context)
	return ai.LazyStreamFunc(model, func() (*ai.AssistantMessageEventStream, error) {
		prepared, err := r.prepareStreamRequest(model, options)
		if err != nil {
			return nil, err
		}
		return prepared.Provider.Stream(prepared.Model, transcript, prepared.Options), nil
	})
}

// Complete streams and waits for the final message.
func (r *ModelRuntime) Complete(model *ai.Model, context ai.Context, options *ai.ModelsStreamOptions) (*ai.AssistantMessage, error) {
	return r.Stream(model, context, options).Result(nil)
}

// StreamSimple streams with reasoning/tool-selection options.
func (r *ModelRuntime) StreamSimple(model *ai.Model, context ai.Context, options *ai.ModelsSimpleStreamOptions) *ai.AssistantMessageEventStream {
	transcript := ai.NormalizeContext(context)
	return ai.LazyStreamFunc(model, func() (*ai.AssistantMessageEventStream, error) {
		prepared, err := r.prepareSimpleRequest(model, options)
		if err != nil {
			return nil, err
		}
		return prepared.Provider.StreamSimple(prepared.Model, transcript, prepared.Simple), nil
	})
}

// CompleteSimple streams and waits for the final message.
func (r *ModelRuntime) CompleteSimple(model *ai.Model, context ai.Context, options *ai.ModelsSimpleStreamOptions) (*ai.AssistantMessage, error) {
	return r.StreamSimple(model, context, options).Result(nil)
}

type preparedStream struct {
	Provider *ai.Provider
	Model    *ai.Model
	Options  *ai.StreamOptions
	Simple   *ai.SimpleStreamOptions
}

func (r *ModelRuntime) prepareStreamRequest(model *ai.Model, options *ai.ModelsStreamOptions) (*preparedStream, error) {
	var apiKey string
	var env map[string]string
	var headers ai.ProviderHeaders
	var transform func(ai.ProviderHeaders) ai.ProviderHeaders
	var requestOptions ai.StreamOptions
	if options != nil {
		requestOptions = options.StreamOptions
		apiKey = options.APIKey
		env = options.Env
		headers = options.Headers
		transform = options.TransformHeaders
	}
	provider := r.models.GetProvider(model.Provider)
	if provider == nil {
		return nil, ai.NewModelsError(ai.ErrCodeProvider, fmt.Sprintf("Unknown provider: %s", model.Provider), nil)
	}
	resolution, err := r.GetAuthForModel(model, &ModelRuntimeAuthOverrides{APIKey: apiKey, Env: env})
	if err != nil {
		return nil, err
	}
	if resolution == nil {
		return nil, ai.NewModelsError(ai.ErrCodeAuth, fmt.Sprintf("Provider is not configured: %s", model.Provider), nil)
	}
	merged := ai.MergeHeaders(resolution.Auth.Headers, headers)
	if transform != nil {
		merged = transform(merged)
	}
	mergedEnv := map[string]string{}
	for key, value := range resolution.Env {
		mergedEnv[key] = value
	}
	for key, value := range env {
		mergedEnv[key] = value
	}
	requestModel := model
	if resolution.Auth.BaseURL != "" {
		copied := *model
		copied.BaseURL = resolution.Auth.BaseURL
		requestModel = &copied
	}
	if requestOptions.APIKey == "" {
		requestOptions.APIKey = resolution.Auth.APIKey
	}
	requestOptions.Headers = merged
	if len(mergedEnv) > 0 {
		requestOptions.Env = mergedEnv
	}
	return &preparedStream{Provider: provider, Model: requestModel, Options: &requestOptions}, nil
}

func (r *ModelRuntime) prepareSimpleRequest(model *ai.Model, options *ai.ModelsSimpleStreamOptions) (*preparedStream, error) {
	streamOptions := &ai.ModelsStreamOptions{}
	if options != nil {
		streamOptions.StreamOptions = options.SimpleStreamOptions.StreamOptions
		streamOptions.TransformHeaders = options.TransformHeaders
	}
	prepared, err := r.prepareStreamRequest(model, streamOptions)
	if err != nil {
		return nil, err
	}
	simple := &ai.SimpleStreamOptions{StreamOptions: *prepared.Options}
	return &preparedStream{Provider: prepared.Provider, Model: prepared.Model, Options: prepared.Options, Simple: simple}, nil
}

// StreamDeferred fetches a deferred response.
func (r *ModelRuntime) StreamDeferred(model *ai.Model, handle *ai.DeferredHandle, options *ai.ModelsStreamOptions) *ai.AssistantMessageEventStream {
	return ai.LazyStreamFunc(model, func() (*ai.AssistantMessageEventStream, error) {
		prepared, err := r.prepareStreamRequest(model, options)
		if err != nil {
			return nil, err
		}
		if !prepared.Provider.HasDeferred() {
			return nil, ai.NewModelsError(ai.ErrCodeProvider, fmt.Sprintf("Provider %s does not support deferred responses", model.Provider), nil)
		}
		return prepared.Provider.FetchDeferred(prepared.Model, handle, prepared.Options), nil
	})
}

// FetchDeferred fetches and waits for a deferred response.
func (r *ModelRuntime) FetchDeferred(model *ai.Model, handle *ai.DeferredHandle, options *ai.ModelsStreamOptions) (*ai.AssistantMessage, error) {
	return r.StreamDeferred(model, handle, options).Result(nil)
}

// CancelDeferred cancels a deferred response.
func (r *ModelRuntime) CancelDeferred(model *ai.Model, handle *ai.DeferredHandle, options *ai.ModelsStreamOptions) error {
	prepared, err := r.prepareStreamRequest(model, options)
	if err != nil {
		return err
	}
	if !prepared.Provider.HasDeferred() {
		return ai.NewModelsError(ai.ErrCodeProvider, fmt.Sprintf("Provider %s does not support deferred responses", model.Provider), nil)
	}
	return prepared.Provider.CancelDeferred(prepared.Model, handle, prepared.Options)
}

// Login runs a provider login flow and synchronizes the local snapshot.
func (r *ModelRuntime) Login(providerID string, authType ai.AuthType, interaction *ai.AuthInteraction) (*ai.Credential, error) {
	ctx := context.Background()
	if interaction != nil && interaction.Ctx != nil {
		ctx = interaction.Ctx
	}
	var credential *ai.Credential
	err := r.enqueueCredentialOperation(providerID, ctx, func() error {
		var loginErr error
		credential, loginErr = r.models.Login(providerID, authType, interaction)
		if loginErr != nil {
			return loginErr
		}
		return r.synchronizeCredentialState(providerID, CredentialOperationLogin, credential, ctx)
	})
	if err != nil {
		return nil, err
	}
	return credential, nil
}

// Logout deletes a provider credential and synchronizes the local snapshot.
func (r *ModelRuntime) Logout(providerID string, ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return r.enqueueCredentialOperation(providerID, ctx, func() error {
		if err := r.models.Logout(providerID, ctx); err != nil {
			return err
		}
		return r.synchronizeCredentialState(providerID, CredentialOperationLogout, nil, ctx)
	})
}

// ModelsRefreshCallOptions are the runtime refresh options.
type ModelsRefreshCallOptions struct {
	Providers    []string
	AllowNetwork *bool
	Force        *bool
	// AllProviders marks the refresh as covering every provider.
	AllProviders bool
}

// Refresh reloads models.json and provider catalogs.
//
// D121: upstream's single-threaded event loop serializes refreshes; the Go
// port can receive concurrent refreshes (the interactive mode fires them from
// background goroutines), so the state-mutating part is serialized here.
func (r *ModelRuntime) Refresh(ctx context.Context, options *ModelsRefreshCallOptions) (ai.ModelsRefreshResult, error) {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	if options == nil {
		options = &ModelsRefreshCallOptions{}
	}
	config := LoadModelConfig(r.modelsPath)
	r.config = config
	r.configureRadiusProviders()
	if len(options.Providers) > 0 {
		for _, providerID := range uniqueStrings(options.Providers) {
			r.recomposeProvider(providerID)
		}
		r.updateModelSnapshot()
	} else {
		r.rebuildProviders()
	}
	allowNetwork := r.network
	if options.AllowNetwork != nil {
		allowNetwork = *options.AllowNetwork
	}
	result := r.models.Refresh(&ai.ModelsRefreshOptions{
		Providers:    options.Providers,
		AllowNetwork: &allowNetwork,
		Force:        options.Force,
		Ctx:          ctx,
	})
	errors := map[string]error{}
	for providerID, err := range result.Errors {
		errors[providerID] = err
	}
	r.updateModelSnapshot()
	if len(options.Providers) > 0 {
		for _, providerID := range uniqueStrings(options.Providers) {
			if err := r.refreshProviderAvailability(providerID, ctx); err != nil && ctx.Err() == nil {
				errors[providerID] = err
			}
		}
	} else {
		// Availability errors are recorded by the latest pass; refreshed
		// models remain usable.
		func() {
			defer func() { _ = recover() }()
			r.queueAvailabilityRefresh(ctx)
		}()
	}
	return ai.ModelsRefreshResult{Aborted: result.Aborted || ctx.Err() != nil, Errors: errors}, nil
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}
