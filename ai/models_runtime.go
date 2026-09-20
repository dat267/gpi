package ai

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Port of packages/ai/src/models.ts and api/lazy.ts (lazyStream).

// ProviderStreams is the uniform stream contract of an API implementation
// module (types.ts ProviderStreams).
type ProviderStreams interface {
	Stream(model *Model, context TranscriptContext, options *StreamOptions) *AssistantMessageEventStream
	StreamSimple(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream
}

// DeferredStreamer is the optional deferred-response capability of an API
// implementation (upstream's optional fetchDeferred/cancelDeferred).
type DeferredStreamer interface {
	FetchDeferred(model *Model, handle *DeferredHandle, options *StreamOptions) *AssistantMessageEventStream
	CancelDeferred(model *Model, handle *DeferredHandle, options *StreamOptions) error
}

// RefreshModelsContext is the argument to a dynamic provider's refresh.
type RefreshModelsContext struct {
	Credential   *Credential
	Stored       *ModelsStoreEntry
	Publish      func(publication ModelsPublication) bool
	AllowNetwork bool
	// Force is only defined when AllowNetwork is set (upstream passes
	// `force: allowNetwork ? force : undefined`).
	Force *bool
	Ctx   context.Context
}

// ModelsPublication publishes refreshed models. Update applies the new list
// to in-memory state. Persist writes the entry to the models store; nil with
// DeleteStored=false means no write, DeleteStored=true deletes the stored
// entry (upstream's persist: value | null | undefined).
type ModelsPublication struct {
	Update       func()
	Persist      *ModelsStoreEntry
	DeleteStored bool
}

// Provider is a model source with auth semantics. Built with CreateProvider
// (upstream: built-in provider factories and models.json custom providers
// both go through it).
type Provider struct {
	ID      string
	Name    string
	BaseURL string
	Headers ProviderHeaders
	Auth    ProviderAuth

	getModels func() []*Model
	// RefreshModels is the dynamic-provider refresh hook (nil for static).
	RefreshModels func(context *RefreshModelsContext) error
	// FilterModels is an optional provider policy for credential-specific
	// model availability.
	FilterModels func(models []*Model, credential *Credential) []*Model

	// streamsFor dispatches on model.api (single implementation or per-API map).
	streamsFor func(model *Model) ProviderStreams
	// hasDeferred mirrors createProvider's conditional deferred exposure.
	hasDeferredFetch  bool
	hasDeferredCancel bool
}

// GetModels returns the current known models, sync.
func (p *Provider) GetModels() []*Model { return p.getModels() }

// streamsForModel resolves the API implementation for a model.
func (p *Provider) streamsForModel(model *Model) ProviderStreams { return p.streamsFor(model) }

// Stream streams a normalized transcript.
func (p *Provider) Stream(model *Model, context TranscriptContext, options *StreamOptions) *AssistantMessageEventStream {
	return dispatchStreams(p, model, func(s ProviderStreams) *AssistantMessageEventStream {
		return s.Stream(model, context, options)
	})
}

// StreamSimple streams with reasoning/tool-selection options.
func (p *Provider) StreamSimple(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream {
	return dispatchStreams(p, model, func(s ProviderStreams) *AssistantMessageEventStream {
		return s.StreamSimple(model, context, options)
	})
}

func dispatchStreams(p *Provider, model *Model, run func(ProviderStreams) *AssistantMessageEventStream) *AssistantMessageEventStream {
	streams := p.streamsFor(model)
	if streams == nil {
		return LazyStream(model, func() (eventSource, error) {
			return nil, NewModelsError(ErrCodeStream, fmt.Sprintf("Provider %s has no API implementation for %q", p.ID, model.API), nil)
		})
	}
	return run(streams)
}

// FetchDeferred fetches a deferred response when the API implementation
// supports it; otherwise the stream terminates with an error.
func (p *Provider) FetchDeferred(model *Model, handle *DeferredHandle, options *StreamOptions) *AssistantMessageEventStream {
	return LazyStream(model, func() (eventSource, error) {
		implementation := p.streamsForModel(model)
		d, ok := implementation.(DeferredStreamer)
		if implementation == nil || !ok {
			return nil, NewModelsError(ErrCodeProvider, fmt.Sprintf("Provider %s does not support deferred responses for %q", p.ID, model.API), nil)
		}
		return AsEventSource(d.FetchDeferred(model, handle, options)), nil
	})
}

// CancelDeferred cancels a deferred response when supported.
func (p *Provider) CancelDeferred(model *Model, handle *DeferredHandle, options *StreamOptions) error {
	implementation := p.streamsForModel(model)
	d, ok := implementation.(DeferredStreamer)
	if implementation == nil || !ok {
		return NewModelsError(ErrCodeProvider, fmt.Sprintf("Provider %s cannot cancel deferred responses for %q", p.ID, model.API), nil)
	}
	return d.CancelDeferred(model, handle, options)
}

// StreamFunction is the generic stream function contract (types.ts).
type StreamFunction func(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream

// CreateProviderOptions are the parts for CreateProvider.
type CreateProviderOptions struct {
	ID      string
	Name    string // default: ID
	BaseURL string
	Headers ProviderHeaders
	// Auth is required — every provider has auth semantics, even
	// ambient/keyless ones.
	Auth ProviderAuth
	// Models is the static baseline model list (empty for purely dynamic
	// providers).
	Models []*Model
	// ModelsGetter, when set, supplies the current model list on every read
	// (upstream's getModels closure). It replaces Models as the baseline; the
	// dynamic overlay is still merged on top.
	ModelsGetter func() []*Model
	// FetchModels fetches a dynamic model overlay. CreateProvider restores
	// and publishes it transactionally.
	FetchModels  func(context *RefreshModelsContext) ([]*Model, error)
	FilterModels func(models []*Model, credential *Credential) []*Model
	// Single is one API implementation for all models.
	Single ProviderStreams
	// ByAPI dispatches on model.api for mixed-API providers.
	ByAPI map[string]ProviderStreams
}

// CreateProvider builds a provider from parts. A single API streams all
// models; a per-API map dispatches on Model.API, and a model whose api has
// no entry produces a stream error.
func CreateProvider(input CreateProviderOptions) *Provider {
	var dynamicMu sync.Mutex
	var dynamicModels []*Model
	currentModels := func() []*Model {
		baseline := input.Models
		if input.ModelsGetter != nil {
			baseline = input.ModelsGetter()
		}
		dynamicMu.Lock()
		defer dynamicMu.Unlock()
		merged := append([]*Model{}, baseline...)
		for _, model := range dynamicModels {
			index := -1
			for i, entry := range merged {
				if entry.ID == model.ID {
					index = i
					break
				}
			}
			if index >= 0 {
				merged[index] = model
			} else {
				merged = append(merged, model)
			}
		}
		return merged
	}

	var streamsFor func(model *Model) ProviderStreams
	if input.Single != nil {
		single := input.Single
		streamsFor = func(*Model) ProviderStreams { return single }
	} else {
		byAPI := input.ByAPI
		streamsFor = func(model *Model) ProviderStreams { return byAPI[model.API] }
	}

	p := &Provider{
		ID:           input.ID,
		Name:         input.Name,
		BaseURL:      input.BaseURL,
		Headers:      input.Headers,
		Auth:         input.Auth,
		getModels:    currentModels,
		FilterModels: input.FilterModels,
		streamsFor:   streamsFor,
	}
	if p.Name == "" {
		p.Name = input.ID
	}

	if input.FetchModels != nil {
		fetch := input.FetchModels
		p.RefreshModels = func(context *RefreshModelsContext) error {
			dynamicMu.Lock()
			if context.Stored != nil {
				var restored []*Model
				for _, model := range context.Stored.Models {
					if model.Provider == input.ID {
						restored = append(restored, model)
					}
				}
				published := context.Publish(ModelsPublication{
					Update: func() { dynamicModels = restored },
				})
				dynamicMu.Unlock()
				if !published {
					return nil
				}
				dynamicMu.Lock()
			}
			dynamicMu.Unlock()
			if !context.AllowNetwork || ctxDone(context.Ctx) {
				return nil
			}
			refreshed, err := fetch(context)
			if err != nil {
				return err
			}
			if ctxDone(context.Ctx) {
				return nil
			}
			checkedAt := timeNowMS()
			context.Publish(ModelsPublication{
				Persist: &ModelsStoreEntry{Models: refreshed, CheckedAt: &checkedAt},
				Update:  func() { dynamicModels = refreshed },
			})
			return nil
		}
	}

	// Conditionally expose deferred capabilities (upstream checks whether
	// any implementation declares them).
	streamsList := input.ByAPIList()
	if input.Single != nil {
		streamsList = []ProviderStreams{input.Single}
	}
	for _, s := range streamsList {
		if _, ok := s.(DeferredStreamer); ok {
			p.hasDeferredFetch = true
			p.hasDeferredCancel = true
		}
	}
	return p
}

// HasDeferred reports whether deferred responses are supported by any of the
// provider's API implementations.
func (p *Provider) HasDeferred() bool { return p.hasDeferredFetch }

// ByAPIList is a test seam; the real map iteration happens in CreateProvider.
func (o CreateProviderOptions) ByAPIList() []ProviderStreams {
	var out []ProviderStreams
	keys := make([]string, 0, len(o.ByAPI))
	for k := range o.ByAPI {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if o.ByAPI[k] != nil {
			out = append(out, o.ByAPI[k])
		}
	}
	return out
}

func ctxDone(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

// eventSource is the minimal surface LazyStream forwards from.
type eventSource interface {
	// Forward pushes every remaining event into target and ends it.
	forwardInto(target *AssistantMessageEventStream)
}

// streamSource adapts an AssistantMessageEventStream to eventSource.
type streamSource struct{ inner *AssistantMessageEventStream }

func (s streamSource) forwardInto(target *AssistantMessageEventStream) {
	ctx := context.Background()
	for {
		event, ok := s.inner.Next(ctx)
		if !ok {
			break
		}
		target.Push(event)
	}
	result, err := s.inner.Result(ctx)
	if err != nil {
		target.End(nil)
		return
	}
	target.End(&result)
}

// LazyStream returns a stream synchronously while running async setup (auth
// resolution, lazy module loading) behind it. Setup failures terminate the
// stream with an error event (port of api/lazy.ts lazyStream).
func LazyStream(model *Model, setup func() (eventSource, error)) *AssistantMessageEventStream {
	outer := NewAssistantMessageEventStream()
	go func() {
		inner, err := setup()
		if err != nil {
			message := createSetupErrorMessage(model, err)
			outer.Push(AssistantMessageEvent{Type: EventError, Reason: StopError, Error: message})
			outer.End(&message)
			return
		}
		inner.forwardInto(outer)
	}()
	return outer
}

// AsEventSource adapts an AssistantMessageEventStream for LazyStream setups.
func AsEventSource(s *AssistantMessageEventStream) eventSource { return streamSource{inner: s} }

// LazyStreamFunc builds a stream whose source is created on first consumption
// (upstream lazyStream with an async factory). The factory returns a stream that
// is forwarded into the outer stream.
func LazyStreamFunc(model *Model, create func() (*AssistantMessageEventStream, error)) *AssistantMessageEventStream {
	return LazyStream(model, func() (eventSource, error) {
		inner, err := create()
		if err != nil {
			return nil, err
		}
		return AsEventSource(inner), nil
	})
}

// ErrorStreamForModel builds a lazy stream that terminates with a typed
// provider error (used when no implementation can serve a model).
func ErrorStreamForModel(model *Model, code string, message string) *AssistantMessageEventStream {
	return LazyStream(model, func() (eventSource, error) {
		return nil, NewModelsError(code, message, nil)
	})
}

func createSetupErrorMessage(model *Model, err error) *AssistantMessage {
	msg := err.Error()
	return &AssistantMessage{
		API:          model.API,
		Provider:     model.Provider,
		Model:        model.ID,
		Usage:        Usage{Cost: UsageCost{}},
		StopReason:   StopError,
		ErrorMessage: &msg,
		Timestamp:    timeNowMS(),
	}
}

func timeNowMS() int64 { return time.Now().UnixMilli() }

// bgCtx is context.Background (named to avoid clashing with ai.Context).
func bgCtx() context.Context { return context.Background() }
func nowUnixMilli() int64    { return time.Now().UnixMilli() }

// ModelsStreamOptions adds the Models-only request transforms to stream options.
type ModelsStreamOptions struct {
	StreamOptions
	// TransformHeaders runs last over the merged header set.
	TransformHeaders func(headers ProviderHeaders) ProviderHeaders
}

// ModelsSimpleStreamOptions is the SimpleStreamOptions variant.
type ModelsSimpleStreamOptions struct {
	SimpleStreamOptions
	TransformHeaders func(headers ProviderHeaders) ProviderHeaders
}

// Models is the runtime collection of providers plus auth application and
// stream convenience (upstream Models/MutableModels; mutability is inherent
// to the Go struct).
type Models struct {
	mu          sync.Mutex
	providers   map[string]*Provider
	credentials CredentialStore
	modelsStore ModelsStore
	authContext AuthContext
	// refresh bookkeeping (upstream refreshGenerations/refreshControllers)
	refreshGenerations map[string]int
	refreshCancels     map[string]context.CancelFunc
	// publication serialization per provider
	publicationMu map[string]*sync.Mutex
}

// CreateModels builds a Models collection.
func CreateModels(options *CreateModelsOptions) *Models {
	m := &Models{
		providers:          map[string]*Provider{},
		refreshGenerations: map[string]int{},
		refreshCancels:     map[string]context.CancelFunc{},
		publicationMu:      map[string]*sync.Mutex{},
	}
	if options != nil && options.Credentials != nil {
		m.credentials = options.Credentials
	} else {
		m.credentials = NewInMemoryCredentialStore()
	}
	if options != nil && options.ModelsStore != nil {
		m.modelsStore = options.ModelsStore
	} else {
		m.modelsStore = NewInMemoryModelsStore()
	}
	if options != nil && options.AuthContext != nil {
		m.authContext = options.AuthContext
	} else {
		m.authContext = DefaultAuthContext{}
	}
	return m
}

// CreateModelsOptions are CreateModels dependencies.
type CreateModelsOptions struct {
	Credentials CredentialStore
	ModelsStore ModelsStore
	AuthContext AuthContext
}

// SetProvider upserts by Provider.ID (ids are unique).
func (m *Models) SetProvider(provider *Provider) {
	m.mu.Lock()
	m.supersedeProviderRefreshLocked(provider.ID)
	m.providers[provider.ID] = provider
	m.mu.Unlock()
}

// DeleteProvider removes a provider.
func (m *Models) DeleteProvider(id string) {
	m.mu.Lock()
	m.supersedeProviderRefreshLocked(id)
	delete(m.providers, id)
	m.mu.Unlock()
}

// ClearProviders removes all providers.
func (m *Models) ClearProviders() {
	m.mu.Lock()
	ids := map[string]bool{}
	for id := range m.providers {
		ids[id] = true
	}
	for id := range m.refreshCancels {
		ids[id] = true
	}
	for id := range ids {
		m.supersedeProviderRefreshLocked(id)
	}
	m.providers = map[string]*Provider{}
	m.mu.Unlock()
}

// GetProviders returns all providers.
func (m *Models) GetProviders() []*Provider {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*Provider, 0, len(m.providers))
	for _, p := range m.providers {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// GetProvider returns one provider by id.
func (m *Models) GetProvider(id string) *Provider {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.providers[id]
}

// GetModels returns last-known models from one provider or all providers.
// Best-effort: a panicking provider yields no models.
func (m *Models) GetModels(providerID string) []*Model {
	m.mu.Lock()
	defer m.mu.Unlock()
	if providerID != "" {
		entry, ok := m.providers[providerID]
		if !ok {
			return nil
		}
		return safeModels(entry)
	}
	var models []*Model
	for _, entry := range m.providers {
		models = append(models, safeModels(entry)...)
	}
	return models
}

func safeModels(p *Provider) (out []*Model) {
	defer func() {
		if r := recover(); r != nil {
			out = nil
		}
	}()
	return p.GetModels()
}

// GetModel returns one model by provider and id.
func (m *Models) GetModel(providerID, id string) *Model {
	for _, model := range m.GetModels(providerID) {
		if model.ID == id {
			return model
		}
	}
	return nil
}

func (m *Models) supersedeProviderRefreshLocked(providerID string) int {
	generation := m.refreshGenerations[providerID] + 1
	m.refreshGenerations[providerID] = generation
	if cancel, ok := m.refreshCancels[providerID]; ok {
		delete(m.refreshCancels, providerID)
		cancel()
	}
	return generation
}

// ModelsRefreshOptions tune Refresh.
type ModelsRefreshOptions struct {
	Providers    []string
	AllowNetwork *bool
	Force        *bool
	Ctx          context.Context
}

// ModelsRefreshResult reports refresh outcome per provider.
type ModelsRefreshResult struct {
	Aborted bool
	Errors  map[string]error
}

// Refresh refreshes selected configured dynamic providers concurrently (all
// when Providers is omitted). Provider errors and cancellation are returned
// without failing the whole call; static, unknown, and unconfigured
// providers are skipped.
func (m *Models) Refresh(options *ModelsRefreshOptions) ModelsRefreshResult {
	allowNetwork := true
	if options != nil && options.AllowNetwork != nil {
		allowNetwork = *options.AllowNetwork
	}
	callerCtx := context.Background()
	if options != nil && options.Ctx != nil {
		callerCtx = options.Ctx
	}
	errors := map[string]error{}
	if ctxDone(callerCtx) {
		return ModelsRefreshResult{Aborted: true, Errors: errors}
	}
	var selected map[string]bool
	if options != nil && options.Providers != nil {
		selected = map[string]bool{}
		for _, id := range options.Providers {
			selected[id] = true
		}
	}

	m.mu.Lock()
	var refreshable []*Provider
	for _, p := range m.providers {
		if p.RefreshModels != nil && (selected == nil || selected[p.ID]) {
			refreshable = append(refreshable, p)
		}
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, provider := range refreshable {
		wg.Add(1)
		go func(provider *Provider) {
			defer wg.Done()
			m.mu.Lock()
			generation := m.supersedeProviderRefreshLocked(provider.ID)
			refreshCtx, cancel := context.WithCancel(callerCtx)
			m.refreshCancels[provider.ID] = cancel
			m.mu.Unlock()
			defer func() {
				m.mu.Lock()
				if m.refreshCancels[provider.ID] != nil {
					cancel2 := m.refreshCancels[provider.ID]
					_ = cancel2
					delete(m.refreshCancels, provider.ID)
				}
				m.mu.Unlock()
				cancel()
			}()

			operation := func() error {
				storedCredential, credErr := readCredential(m.credentials, provider.ID, refreshCtx)

				// Restore cached provider state before auth resolution or
				// network access.
				if err := m.runProviderRefreshPhase(provider, storedCredential, false, nil, generation, refreshCtx); err != nil {
					return err
				}
				if credErr != nil {
					return credErr
				}
				if !allowNetwork || ctxDone(refreshCtx) {
					return nil
				}

				credential, err := m.resolveRefreshCredential(provider, storedCredential, refreshCtx)
				if err != nil {
					return err
				}
				if credential == nil {
					return nil
				}
				return m.runProviderRefreshPhase(provider, credential, true, options.forceOrDefault(), generation, refreshCtx)
			}

			if err := operation(); err != nil && !ctxDone(refreshCtx) {
				m.mu.Lock()
				errors[provider.ID] = err
				m.mu.Unlock()
			}
		}(provider)
	}
	wg.Wait()

	return ModelsRefreshResult{Aborted: ctxDone(callerCtx), Errors: errors}
}

func (o *ModelsRefreshOptions) forceOrDefault() *bool {
	if o != nil {
		return o.Force
	}
	return nil
}

func (m *Models) pubMu(providerID string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	mu, ok := m.publicationMu[providerID]
	if !ok {
		mu = &sync.Mutex{}
		m.publicationMu[providerID] = mu
	}
	return mu
}

func (m *Models) publishProviderModels(providerID string, generation int, signal context.Context, publication ModelsPublication) bool {
	mu := m.pubMu(providerID)
	mu.Lock()
	defer mu.Unlock()
	m.mu.Lock()
	superseded := m.refreshGenerations[providerID] != generation
	m.mu.Unlock()
	if ctxDone(signal) || superseded {
		return false
	}

	if publication.DeleteStored {
		if err := m.modelsStore.Delete(providerID, signal); err != nil {
			return false
		}
	} else if publication.Persist != nil {
		if err := m.modelsStore.Write(providerID, publication.Persist, signal); err != nil {
			return false
		}
	}

	m.mu.Lock()
	superseded = m.refreshGenerations[providerID] != generation
	m.mu.Unlock()
	if ctxDone(signal) || superseded {
		return false
	}
	if publication.Update != nil {
		publication.Update()
	}
	return true
}

func (m *Models) runProviderRefreshPhase(provider *Provider, credential *Credential, allowNetwork bool, force *bool, generation int, signal context.Context) error {
	stored, err := m.modelsStore.Read(provider.ID, signal)
	if err != nil {
		return err
	}
	return provider.RefreshModels(&RefreshModelsContext{
		Credential: credential,
		Stored:     stored,
		Publish: func(publication ModelsPublication) bool {
			return m.publishProviderModels(provider.ID, generation, signal, publication)
		},
		AllowNetwork: allowNetwork,
		Force:        boolPtrWhen(allowNetwork, force),
		Ctx:          signal,
	})
}

func boolPtrWhen(cond bool, v *bool) *bool {
	if !cond {
		return nil
	}
	return v
}

func (m *Models) resolveRefreshCredential(provider *Provider, stored *Credential, signal context.Context) (*Credential, error) {
	if stored != nil && stored.Type == CredentialOAuth {
		oauth := provider.Auth.OAuth
		if oauth == nil {
			return nil, nil
		}
		if nowUnixMilli() < stored.OAuth.Expires {
			return stored, nil
		}
		if ctxDone(signal) {
			return nil, nil
		}
		post, err := m.credentials.Modify(provider.ID, func(current *Credential) (*Credential, error) {
			if current == nil || current.Type != CredentialOAuth || nowUnixMilli() < current.OAuth.Expires {
				return nil, nil
			}
			refreshed, err := oauth.Refresh(current.OAuth, signal)
			if err != nil {
				return nil, err
			}
			return &Credential{Type: CredentialOAuth, OAuth: refreshed}, nil
		}, signal)
		if err != nil {
			return nil, err
		}
		if post != nil && post.Type == CredentialOAuth {
			return post, nil
		}
		return nil, nil
	}

	apiKey := provider.Auth.APIKey
	if apiKey == nil {
		return nil, nil
	}
	var credential *ApiKeyCredential
	if stored != nil && stored.Type == CredentialAPIKey {
		credential = stored.APIKey
	}
	result, err := apiKey.Resolve(AuthResolveInput{Ctx: m.authContext, Credential: credential, Ctx2: signal})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, nil
	}
	return &Credential{Type: CredentialAPIKey, APIKey: &ApiKeyCredential{Key: result.Auth.APIKey, Env: result.Env}}, nil
}

func (m *Models) checkProviderAuth(provider *Provider, credential *Credential, signal context.Context) (*AuthCheck, error) {
	if credential != nil && credential.Type == CredentialOAuth {
		if provider.Auth.OAuth != nil {
			return &AuthCheck{Source: "OAuth", Type: AuthTypeOAuth}, nil
		}
		return nil, nil
	}
	apiKey := provider.Auth.APIKey
	if apiKey == nil {
		return nil, nil
	}
	if apiKey.Check != nil {
		var apiCredential *ApiKeyCredential
		if credential != nil && credential.Type == CredentialAPIKey {
			apiCredential = credential.APIKey
		}
		check, err := apiKey.Check(AuthResolveInput{Ctx: m.authContext, Credential: apiCredential, Ctx2: signal})
		if err != nil {
			return nil, NewModelsError(ErrCodeAuth, fmt.Sprintf("API key auth check failed for provider %s", provider.ID), err)
		}
		return check, nil
	}
	resolution, err := ResolveProviderAuth(provider.ID, provider.Auth, m.credentials, m.authContext, &AuthResolutionOverrides{Ctx: signal})
	if err != nil {
		return nil, err
	}
	if resolution != nil {
		return &AuthCheck{Source: resolution.Source, Type: AuthTypeAPIKey}, nil
	}
	return nil, nil
}

// CheckAuth checks whether a provider has complete auth configuration
// without refreshing OAuth.
func (m *Models) CheckAuth(providerID string, ctx context.Context) (*AuthCheck, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	m.mu.Lock()
	provider := m.providers[providerID]
	m.mu.Unlock()
	if provider == nil {
		return nil, nil
	}
	credential, err := readCredential(m.credentials, providerID, ctx)
	if err != nil {
		return nil, err
	}
	return m.checkProviderAuth(provider, credential, ctx)
}

// GetAvailable returns models whose providers have complete auth
// configuration.
func (m *Models) GetAvailable(providerID string, ctx context.Context) ([]*Model, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	var providers []*Provider
	if providerID != "" {
		m.mu.Lock()
		if p := m.providers[providerID]; p != nil {
			providers = []*Provider{p}
		}
		m.mu.Unlock()
	} else {
		providers = m.GetProviders()
	}
	var out []*Model
	for _, provider := range providers {
		credential, err := readCredential(m.credentials, provider.ID, ctx)
		if err != nil {
			return nil, err
		}
		auth, err := m.checkProviderAuth(provider, credential, ctx)
		if err != nil {
			return nil, err
		}
		if auth == nil {
			continue
		}
		models := provider.GetModels()
		if provider.FilterModels != nil {
			models = provider.FilterModels(models, credential)
		}
		out = append(out, models...)
	}
	return out, nil
}

// GetAuth resolves provider-scoped auth by provider id.
func (m *Models) GetAuth(providerID string, overrides *AuthResolutionOverrides) (*AuthResult, error) {
	m.mu.Lock()
	provider := m.providers[providerID]
	m.mu.Unlock()
	if provider == nil {
		return nil, nil
	}
	return ResolveProviderAuth(provider.ID, provider.Auth, m.credentials, m.authContext, overrides)
}

// GetAuthForModel resolves provider auth plus static model headers.
// Returns nil when the provider is unknown or unconfigured.
func (m *Models) GetAuthForModel(model *Model, overrides *AuthResolutionOverrides) (*AuthResult, error) {
	result, err := m.GetAuth(model.Provider, overrides)
	if err != nil || result == nil || len(model.Headers) == 0 {
		return result, err
	}
	result.Auth.Headers = MergeHeaders(result.Auth.Headers, headersOf(model.Headers))
	return result, nil
}

func headersOf(h map[string]string) ProviderHeaders {
	out := ProviderHeaders{}
	for k, v := range h {
		value := v
		out[k] = &value
	}
	return out
}

// MergeHeaders merges header maps case-insensitively; override values win
// and a nil override value deletes the base header (port of mergeHeaders).
func MergeHeaders(base, override ProviderHeaders) ProviderHeaders {
	if base == nil && override == nil {
		return nil
	}
	merged := ProviderHeaders{}
	for k, v := range base {
		merged[k] = v
	}
	for name, value := range override {
		lower := lowerName(name)
		for existing := range merged {
			if lowerName(existing) == lower {
				delete(merged, existing)
			}
		}
		merged[name] = value
	}
	return merged
}

func lowerName(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'A' && r <= 'Z' {
			out[i] = r + ('a' - 'A')
		}
	}
	return string(out)
}

func (m *Models) requireProvider(model *Model) (*Provider, error) {
	m.mu.Lock()
	provider := m.providers[model.Provider]
	m.mu.Unlock()
	if provider == nil {
		return nil, NewModelsError(ErrCodeProvider, fmt.Sprintf("Unknown provider: %s", model.Provider), nil)
	}
	return provider, nil
}

// applyAuth resolves auth and merges request options. Explicit request
// options win per-field; the Models-only transform runs last.
func (m *Models) applyAuth(model *Model, options *ModelsStreamOptions) (*Model, *ModelsStreamOptions, error) {
	if _, err := m.requireProvider(model); err != nil {
		return nil, nil, err
	}
	resolution, err := m.GetAuthForModel(model, &AuthResolutionOverrides{
		APIKey: options.APIKey,
		Env:    options.Env,
		Ctx:    options.Ctx,
	})
	if err != nil {
		return nil, nil, err
	}
	if resolution == nil {
		return nil, nil, NewModelsError(ErrCodeAuth, fmt.Sprintf("Provider is not configured: %s", model.Provider), nil)
	}
	auth := resolution.Auth

	apiKey := options.APIKey
	if apiKey == "" {
		apiKey = auth.APIKey
	}
	headers := MergeHeaders(auth.Headers, options.Headers)
	if options.TransformHeaders != nil {
		headers = options.TransformHeaders(headers)
	}
	var env ProviderEnv
	if resolution.Env != nil || options.Env != nil {
		env = ProviderEnv{}
		for k, v := range resolution.Env {
			env[k] = v
		}
		for k, v := range options.Env {
			env[k] = v
		}
	}
	requestModel := model
	if auth.BaseURL != "" {
		cloned := *model
		cloned.BaseURL = auth.BaseURL
		requestModel = &cloned
	}
	requestOptions := *options
	requestOptions.APIKey = apiKey
	requestOptions.Headers = headers
	requestOptions.Env = env
	requestOptions.TransformHeaders = nil
	return requestModel, &requestOptions, nil
}

// Stream normalizes the context, resolves auth, and delegates to the owning
// provider. Setup failures arrive as stream errors.
func (m *Models) Stream(model *Model, context Context, options *ModelsStreamOptions) *AssistantMessageEventStream {
	transcript := NormalizeContext(context)
	if options == nil {
		options = &ModelsStreamOptions{}
	}
	return LazyStream(model, func() (eventSource, error) {
		provider, err := m.requireProvider(model)
		if err != nil {
			return nil, err
		}
		requestModel, requestOptions, err := m.applyAuth(model, options)
		if err != nil {
			return nil, err
		}
		return AsEventSource(provider.Stream(requestModel, transcript, &requestOptions.StreamOptions)), nil
	})
}

// Complete streams and awaits the final message.
func (m *Models) Complete(model *Model, context Context, options *ModelsStreamOptions) (*AssistantMessage, error) {
	return m.Stream(model, context, options).Result(bgCtx())
}

// StreamSimple is the SimpleStreamOptions variant of Stream.
func (m *Models) StreamSimple(model *Model, context Context, options *ModelsSimpleStreamOptions) *AssistantMessageEventStream {
	transcript := NormalizeContext(context)
	if options == nil {
		options = &ModelsSimpleStreamOptions{}
	}
	return LazyStream(model, func() (eventSource, error) {
		provider, err := m.requireProvider(model)
		if err != nil {
			return nil, err
		}
		merged := &ModelsStreamOptions{StreamOptions: options.StreamOptions, TransformHeaders: options.TransformHeaders}
		requestModel, requestOptions, err := m.applyAuth(model, merged)
		if err != nil {
			return nil, err
		}
		simple := &SimpleStreamOptions{StreamOptions: requestOptions.StreamOptions, ToolChoice: options.ToolChoice, Reasoning: options.Reasoning, ThinkingBudgets: options.ThinkingBudgets}
		return AsEventSource(provider.StreamSimple(requestModel, transcript, simple)), nil
	})
}

// CompleteSimple streams simply and awaits the final message.
func (m *Models) CompleteSimple(model *Model, context Context, options *ModelsSimpleStreamOptions) (*AssistantMessage, error) {
	return m.StreamSimple(model, context, options).Result(bgCtx())
}

// StreamDeferred streams a deferred-response fetch.
func (m *Models) StreamDeferred(model *Model, handle *DeferredHandle, options *ModelsStreamOptions) *AssistantMessageEventStream {
	if options == nil {
		options = &ModelsStreamOptions{}
	}
	return LazyStream(model, func() (eventSource, error) {
		provider, err := m.requireProvider(model)
		if err != nil {
			return nil, err
		}
		requestModel, requestOptions, err := m.applyAuth(model, options)
		if err != nil {
			return nil, err
		}
		return AsEventSource(provider.FetchDeferred(requestModel, handle, &requestOptions.StreamOptions)), nil
	})
}

// FetchDeferred awaits a deferred-response fetch.
func (m *Models) FetchDeferred(model *Model, handle *DeferredHandle, options *ModelsStreamOptions) (*AssistantMessage, error) {
	return m.StreamDeferred(model, handle, options).Result(bgCtx())
}

// CancelDeferred cancels a deferred response.
func (m *Models) CancelDeferred(model *Model, handle *DeferredHandle, options *ModelsStreamOptions) error {
	provider, err := m.requireProvider(model)
	if err != nil {
		return err
	}
	_, requestOptions, err := m.applyAuth(model, options)
	if err != nil {
		return err
	}
	return provider.CancelDeferred(model, handle, &requestOptions.StreamOptions)
}

// HasAPI reports whether a dynamically looked-up model uses the given API
// (upstream type guard hasApi).
func HasAPI(model *Model, api Api) bool { return model.API == api }

// CalculateCost computes usage cost from model rates and mutates
// usage.Cost in place, returning it (upstream calculateCost).
func CalculateCost(model *Model, usage *Usage) UsageCost {
	inputTokens := usage.Input + usage.CacheRead + usage.CacheWrite
	rates := model.Cost.ModelCostRates
	matchedThreshold := int64(-1)
	for _, tier := range model.Cost.Tiers {
		if inputTokens > tier.InputTokensAbove && tier.InputTokensAbove > matchedThreshold {
			rates = tier.ModelCostRates
			matchedThreshold = tier.InputTokensAbove
		}
	}

	// Anthropic charges 2x base input for 1h cache writes.
	longWrite := int64(0)
	if usage.CacheWrite1h != nil {
		longWrite = *usage.CacheWrite1h
	}
	shortWrite := usage.CacheWrite - longWrite
	usage.Cost.Input = (rates.Input / 1000000) * float64(usage.Input)
	usage.Cost.Output = (rates.Output / 1000000) * float64(usage.Output)
	usage.Cost.CacheRead = (rates.CacheRead / 1000000) * float64(usage.CacheRead)
	usage.Cost.CacheWrite = (rates.CacheWrite*float64(shortWrite) + rates.Input*2*float64(longWrite)) / 1000000
	usage.Cost.Total = usage.Cost.Input + usage.Cost.Output + usage.Cost.CacheRead + usage.Cost.CacheWrite
	return usage.Cost
}

var extendedThinkingLevels = []ModelThinkingLevel{"off", "minimal", "low", "medium", "high", "xhigh", "max"}

// GetSupportedThinkingLevels returns the thinking levels a model supports.
func GetSupportedThinkingLevels(model *Model) []ModelThinkingLevel {
	if !model.Reasoning {
		return []ModelThinkingLevel{ThinkOff}
	}
	var out []ModelThinkingLevel
	for _, level := range extendedThinkingLevels {
		mapped, has := model.ThinkingLevelMap[level]
		if has && mapped == nil {
			continue // null marks unsupported
		}
		if level == ThinkXHigh || level == ThinkMax {
			if !has {
				continue
			}
		}
		out = append(out, level)
	}
	return out
}

// ClampThinkingLevel clamps a requested level to the model's support.
func ClampThinkingLevel(model *Model, level ModelThinkingLevel) ModelThinkingLevel {
	available := GetSupportedThinkingLevels(model)
	for _, a := range available {
		if a == level {
			return level
		}
	}
	requestedIndex := -1
	for i, l := range extendedThinkingLevels {
		if l == level {
			requestedIndex = i
			break
		}
	}
	if requestedIndex == -1 {
		if len(available) > 0 {
			return available[0]
		}
		return ThinkOff
	}
	for i := requestedIndex; i < len(extendedThinkingLevels); i++ {
		for _, a := range available {
			if a == extendedThinkingLevels[i] {
				return a
			}
		}
	}
	for i := requestedIndex - 1; i >= 0; i-- {
		for _, a := range available {
			if a == extendedThinkingLevels[i] {
				return a
			}
		}
	}
	if len(available) > 0 {
		return available[0]
	}
	return ThinkOff
}

// ModelsAreEqual checks whether two models are equal by comparing both
// their id and provider. Returns false if either is nil.
func ModelsAreEqual(a, b *Model) bool {
	if a == nil || b == nil {
		return false
	}
	return a.ID == b.ID && a.Provider == b.Provider
}

// ApplyProviderModelFilter applies a provider's credential-specific model policy
// (upstream provider.filterModels).
func ApplyProviderModelFilter(provider *Provider, models []*Model, credential *Credential) []*Model {
	if provider == nil || provider.FilterModels == nil {
		return models
	}
	return provider.FilterModels(models, credential)
}
