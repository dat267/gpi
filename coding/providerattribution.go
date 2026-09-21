package coding

import (
	"context"
	"net/url"
	"os"
	"strings"

	"github.com/dat267/pier/ai"
)

// Port of core/provider-attribution.ts: default attribution headers and
// per-session OpenCode headers, merged with caller-supplied headers.

const (
	openRouterHost          = "openrouter.ai"
	nvidiaNimHost           = "integrate.api.nvidia.com"
	cloudflareAPIHost       = "api.cloudflare.com"
	cloudflareAIGatewayHost = "gateway.ai.cloudflare.com"
	openCodeHost            = "opencode.ai"
)

func matchesProviderHost(baseURL, expectedHost string) bool {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	return parsed.Hostname() == expectedHost
}

func isOpenRouterModel(model *ai.Model) bool {
	return model.Provider == "openrouter" || strings.Contains(model.BaseURL, openRouterHost)
}

func isNvidiaNimModel(model *ai.Model) bool {
	return model.Provider == "nvidia" || matchesProviderHost(model.BaseURL, nvidiaNimHost)
}

func isCloudflareModel(model *ai.Model) bool {
	return model.Provider == "cloudflare-workers-ai" ||
		model.Provider == "cloudflare-ai-gateway" ||
		matchesProviderHost(model.BaseURL, cloudflareAPIHost) ||
		matchesProviderHost(model.BaseURL, cloudflareAIGatewayHost)
}

// GetDefaultAttributionHeaders returns the install-telemetry attribution headers
// for a model, or nil when telemetry is disabled or the provider is unlisted.
func GetDefaultAttributionHeaders(model *ai.Model, settingsManager *SettingsManager) map[string]string {
	if !IsInstallTelemetryEnabled(settingsManager) {
		return nil
	}
	if isOpenRouterModel(model) {
		return map[string]string{
			"HTTP-Referer":            "https://pi.dev",
			"X-OpenRouter-Title":      "pi",
			"X-OpenRouter-Categories": "cli-agent",
		}
	}
	if isNvidiaNimModel(model) {
		return map[string]string{"X-BILLING-INVOKE-ORIGIN": "Pi"}
	}
	if isCloudflareModel(model) {
		return map[string]string{"User-Agent": "pi-coding-agent"}
	}
	return nil
}

// GetSessionHeaders returns the OpenCode session-affinity headers.
func GetSessionHeaders(model *ai.Model, sessionID string) map[string]string {
	if sessionID == "" {
		return nil
	}
	if model.Provider != "opencode" && model.Provider != "opencode-go" &&
		!matchesProviderHost(model.BaseURL, openCodeHost) {
		return nil
	}
	return map[string]string{"x-opencode-session": sessionID, "x-opencode-client": "pi"}
}

// MergeProviderAttributionHeaders merges session headers, default attribution
// headers, and caller header sources (later sources win).
func MergeProviderAttributionHeaders(model *ai.Model, settingsManager *SettingsManager, sessionID string, headerSources ...map[string]string) map[string]string {
	merged := map[string]string{}
	for key, value := range GetSessionHeaders(model, sessionID) {
		merged[key] = value
	}
	for key, value := range GetDefaultAttributionHeaders(model, settingsManager) {
		merged[key] = value
	}
	for _, headers := range headerSources {
		for key, value := range headers {
			merged[key] = value
		}
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

// IsInstallTelemetryEnabled reports whether install telemetry is on: the
// PI_TELEMETRY environment variable wins over the settings toggle.
func IsInstallTelemetryEnabled(settingsManager *SettingsManager) bool {
	return IsInstallTelemetryEnabledWithEnv(settingsManager, telemetryEnvValue())
}

// IsInstallTelemetryEnabledWithEnv is IsInstallTelemetryEnabled with an
// explicit PI_TELEMETRY value; empty means unset.
func IsInstallTelemetryEnabledWithEnv(settingsManager *SettingsManager, telemetryEnv string) bool {
	if telemetryEnv != "" {
		return IsTruthyEnvFlag(telemetryEnv)
	}
	if settingsManager == nil {
		return true
	}
	return settingsManager.GetEnableInstallTelemetry()
}

// RuntimeCredentials overlays non-persistent runtime API keys on a credential
// store (port of core/runtime-credentials.ts).
type RuntimeCredentials struct {
	store     ai.CredentialStore
	overrides map[string]string
}

// NewRuntimeCredentials wraps a credential store.
func NewRuntimeCredentials(store ai.CredentialStore) *RuntimeCredentials {
	return &RuntimeCredentials{store: store, overrides: map[string]string{}}
}

// SetRuntimeAPIKey records a runtime API key for a provider.
func (r *RuntimeCredentials) SetRuntimeAPIKey(providerID, apiKey string) {
	r.overrides[providerID] = apiKey
}

// RemoveRuntimeAPIKey drops a runtime API key for a provider.
func (r *RuntimeCredentials) RemoveRuntimeAPIKey(providerID string) {
	delete(r.overrides, providerID)
}

// HasRuntimeAPIKey reports whether a runtime API key is set.
func (r *RuntimeCredentials) HasRuntimeAPIKey(providerID string) bool {
	_, ok := r.overrides[providerID]
	return ok
}

// Read returns the runtime override, otherwise the stored credential.
func (r *RuntimeCredentials) Read(providerID string, ctx context.Context) (*ai.Credential, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if override, ok := r.overrides[providerID]; ok {
		return &ai.Credential{Type: ai.CredentialAPIKey, APIKey: &ai.ApiKeyCredential{Key: override}}, nil
	}
	return r.store.Read(providerID, ctx)
}

// List returns stored credential metadata plus the runtime overrides.
func (r *RuntimeCredentials) List(ctx context.Context) ([]ai.CredentialInfo, error) {
	entries, err := r.store.List(ctx)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	byProvider := map[string]ai.CredentialInfo{}
	var order []string
	for _, entry := range entries {
		if _, seen := byProvider[entry.ProviderID]; !seen {
			order = append(order, entry.ProviderID)
		}
		byProvider[entry.ProviderID] = entry
	}
	for providerID := range r.overrides {
		if _, seen := byProvider[providerID]; !seen {
			order = append(order, providerID)
		}
		byProvider[providerID] = ai.CredentialInfo{ProviderID: providerID, Type: ai.CredentialAPIKey}
	}
	out := make([]ai.CredentialInfo, 0, len(order))
	for _, providerID := range order {
		out = append(out, byProvider[providerID])
	}
	return out, nil
}

// Modify delegates to the wrapped store.
func (r *RuntimeCredentials) Modify(providerID string, fn func(current *ai.Credential) (*ai.Credential, error), ctx context.Context) (*ai.Credential, error) {
	return r.store.Modify(providerID, fn, ctx)
}

// Delete removes the stored credential and any runtime override.
func (r *RuntimeCredentials) Delete(providerID string, ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := r.store.Delete(providerID, ctx)
	if err != nil {
		return err
	}
	delete(r.overrides, providerID)
	return nil
}

// IsTruthyEnvFlag mirrors the upstream env flag check: "1", "true", or "yes"
// (case-insensitive).
func IsTruthyEnvFlag(value string) bool {
	if value == "" {
		return false
	}
	if value == "1" {
		return true
	}
	lower := strings.ToLower(value)
	return lower == "true" || lower == "yes"
}

// telemetryEnvValue reads PI_TELEMETRY.
func telemetryEnvValue() string {
	return os.Getenv("PI_TELEMETRY")
}
