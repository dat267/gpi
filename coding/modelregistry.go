package coding

import (
	"context"
	"errors"
	"fmt"

	"github.com/dat267/pier/ai"
)

// Port of core/model-registry.ts: the synchronous compatibility facade over the
// model runtime.
//
// D35 applies here too: the extension registerProvider/unregisterProvider
// surface is extension mechanics and omitted, so the facade covers the runtime
// reads, request auth resolution, and streaming.

// ResolvedRequestAuth is the request-auth outcome for a model.
type ResolvedRequestAuth struct {
	OK      bool
	APIKey  string
	Headers ai.ProviderHeaders
	BaseURL string
	Env     map[string]string
	// Error is set when OK is false.
	Error string
}

// ModelRegistry is the facade over a ModelRuntime.
type ModelRegistry struct {
	runtime *ModelRuntime
}

// NewModelRegistry wraps a runtime.
func NewModelRegistry(runtime *ModelRuntime) *ModelRegistry {
	return &ModelRegistry{runtime: runtime}
}

// Runtime exposes the wrapped runtime.
func (r *ModelRegistry) Runtime() *ModelRuntime { return r.runtime }

// Refresh reloads models.json and provider catalogs.
func (r *ModelRegistry) Refresh(ctx context.Context, options *ModelsRefreshCallOptions) (ai.ModelsRefreshResult, error) {
	return r.runtime.Refresh(ctx, options)
}

// GetError reports configuration and refresh errors.
func (r *ModelRegistry) GetError() string { return r.runtime.GetError() }

// GetAll lists every known model.
func (r *ModelRegistry) GetAll() []*ai.Model {
	models := r.runtime.GetModels("")
	return append([]*ai.Model{}, models...)
}

// GetAvailable lists the models with usable auth.
func (r *ModelRegistry) GetAvailable() []*ai.Model {
	available := r.runtime.GetAvailableSnapshot()
	return append([]*ai.Model{}, available...)
}

// Find looks one model up.
func (r *ModelRegistry) Find(provider, modelID string) *ai.Model {
	return r.runtime.GetModel(provider, modelID)
}

// HasConfiguredAuth reports whether the model's provider has usable auth.
func (r *ModelRegistry) HasConfiguredAuth(model *ai.Model) bool {
	return r.runtime.HasConfiguredAuth(model.Provider)
}

// GetAPIKeyAndHeaders resolves request auth, falling back to the compatibility
// configuration when the provider is unconfigured.
func (r *ModelRegistry) GetAPIKeyAndHeaders(model *ai.Model) ResolvedRequestAuth {
	resolution, err := r.runtime.GetAuthForModel(model, nil)
	if err != nil {
		message := err.Error()
		if cause := errors.Unwrap(err); cause != nil && cause.Error() != "" {
			message = cause.Error()
		}
		if message == "authHeader requires a resolved API key" {
			message = fmt.Sprintf("No API key found for %q", model.Provider)
		}
		return ResolvedRequestAuth{Error: message}
	}
	if resolution == nil {
		compatibility, compatErr := r.runtime.GetCompatibilityRequestConfig(model)
		if compatErr != nil {
			return ResolvedRequestAuth{Error: compatErr.Error()}
		}
		if compatibility.AuthHeader {
			return ResolvedRequestAuth{Error: fmt.Sprintf("No API key found for %q", model.Provider)}
		}
		return ResolvedRequestAuth{OK: true, Headers: compatibility.Headers}
	}
	env := map[string]string{}
	for key, value := range resolution.Env {
		env[key] = value
	}
	if len(env) == 0 {
		env = nil
	}
	return ResolvedRequestAuth{
		OK:      true,
		APIKey:  resolution.Auth.APIKey,
		Headers: resolution.Auth.Headers,
		BaseURL: resolution.Auth.BaseURL,
		Env:     env,
	}
}

// GetProviderAuthStatus reports how a provider's auth is satisfied.
func (r *ModelRegistry) GetProviderAuthStatus(provider string) AuthStatus {
	return r.runtime.GetProviderAuthStatus(provider)
}

// GetProvider returns the composed provider.
func (r *ModelRegistry) GetProvider(provider string) *ai.Provider {
	return r.runtime.GetProvider(provider)
}

// Stream streams through the configured provider with request-time auth.
func (r *ModelRegistry) Stream(model *ai.Model, context ai.Context, options *ai.ModelsStreamOptions) *ai.AssistantMessageEventStream {
	return r.runtime.Stream(model, context, options)
}

// StreamSimple streams with provider-neutral options.
func (r *ModelRegistry) StreamSimple(model *ai.Model, context ai.Context, options *ai.ModelsSimpleStreamOptions) *ai.AssistantMessageEventStream {
	return r.runtime.StreamSimple(model, context, options)
}

// Complete streams and waits for the final message.
func (r *ModelRegistry) Complete(model *ai.Model, context ai.Context, options *ai.ModelsStreamOptions) (*ai.AssistantMessage, error) {
	return r.runtime.Complete(model, context, options)
}

// CompleteSimple streams and waits with provider-neutral options.
func (r *ModelRegistry) CompleteSimple(model *ai.Model, context ai.Context, options *ai.ModelsSimpleStreamOptions) (*ai.AssistantMessage, error) {
	return r.runtime.CompleteSimple(model, context, options)
}

// GetProviderDisplayName returns the provider's display name or its id.
func (r *ModelRegistry) GetProviderDisplayName(provider string) string {
	if resolved := r.runtime.GetProvider(provider); resolved != nil && resolved.Name != "" {
		return resolved.Name
	}
	return provider
}

// GetProviderAuth resolves a provider's auth.
func (r *ModelRegistry) GetProviderAuth(provider string) (*ai.AuthResult, error) {
	return r.runtime.GetAuth(provider, nil)
}

// GetAPIKeyForProvider returns the resolved api key, or "" when unavailable.
func (r *ModelRegistry) GetAPIKeyForProvider(provider string) string {
	result, err := r.runtime.GetAuth(provider, nil)
	if err != nil || result == nil {
		return ""
	}
	return result.Auth.APIKey
}

// IsUsingOAuth reports whether the model's provider resolves to OAuth auth.
func (r *ModelRegistry) IsUsingOAuth(model *ai.Model) bool {
	return r.runtime.IsUsingOAuth(model.Provider)
}
