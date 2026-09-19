package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Port of providers/radius-config.ts and providers/radius.ts.

// DefaultRadiusGateway is the hosted Radius gateway.
const DefaultRadiusGateway = "https://radius.pi.dev"

// RadiusGatewayModel is one gateway catalog model.
type RadiusGatewayModel struct {
	ID               string
	Name             string
	Reasoning        bool
	ThinkingLevelMap map[string]*string
	Input            []string
	Cost             ModelCost
	ContextWindow    int64
	MaxTokens        int64
}

// RadiusGatewayConfig is the gateway's model catalogue.
type RadiusGatewayConfig struct {
	BaseURL string
	Models  []RadiusGatewayModel
}

// RadiusCredentialConfigKey is the credential extra field holding the cached
// gateway config (upstream RadiusOAuthCredential.gatewayConfig).
const RadiusCredentialConfigKey = "gatewayConfig"

// NormalizeRadiusGatewayURL normalizes a gateway URL.
func NormalizeRadiusGatewayURL(value string) string {
	withScheme := value
	if !strings.HasPrefix(strings.ToLower(value), "http://") && !strings.HasPrefix(strings.ToLower(value), "https://") {
		withScheme = "https://" + value
	}
	return strings.TrimRight(withScheme, "/")
}

// sanitizeRadiusGatewayConfig validates and copies a gateway config
// (upstream sanitizeRadiusGatewayConfig).
func sanitizeRadiusGatewayConfig(raw map[string]any) *RadiusGatewayConfig {
	if raw == nil {
		return nil
	}
	baseURL, ok := raw["baseUrl"].(string)
	if !ok {
		return nil
	}
	modelsRaw, ok := raw["models"].([]any)
	if !ok {
		return nil
	}
	config := &RadiusGatewayConfig{BaseURL: baseURL}
	for _, entry := range modelsRaw {
		record, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		model, ok := radiusGatewayModelFromMap(record)
		if !ok {
			continue
		}
		config.Models = append(config.Models, model)
	}
	return config
}

// radiusGatewayModelFromMap validates one catalog model entry.
func radiusGatewayModelFromMap(record map[string]any) (RadiusGatewayModel, bool) {
	id, hasID := record["id"].(string)
	name, hasName := record["name"].(string)
	reasoning, hasReasoning := record["reasoning"].(bool)
	input, hasInput := record["input"].([]any)
	contextWindow, hasContext := record["contextWindow"].(float64)
	maxTokens, hasMax := record["maxTokens"].(float64)
	costRecord, hasCost := record["cost"].(map[string]any)
	if !hasID || !hasName || !hasReasoning || !hasInput || !hasContext || !hasMax || !hasCost {
		return RadiusGatewayModel{}, false
	}
	model := RadiusGatewayModel{
		ID: id, Name: name, Reasoning: reasoning,
		Cost:          radiusCostFromMap(costRecord),
		ContextWindow: int64(contextWindow),
		MaxTokens:     int64(maxTokens),
	}
	for _, item := range input {
		if text, ok := item.(string); ok {
			model.Input = append(model.Input, text)
		}
	}
	if thinking, ok := record["thinkingLevelMap"].(map[string]any); ok {
		model.ThinkingLevelMap = map[string]*string{}
		for level, value := range thinking {
			if text, ok := value.(string); ok {
				copied := text
				model.ThinkingLevelMap[level] = &copied
			} else if value == nil {
				model.ThinkingLevelMap[level] = nil
			}
		}
	}
	return model, true
}

func radiusCostFromMap(record map[string]any) ModelCost {
	cost := ModelCost{}
	read := func(key string) float64 {
		if value, ok := record[key].(float64); ok {
			return value
		}
		return 0
	}
	cost.Input = read("input")
	cost.Output = read("output")
	cost.CacheRead = read("cacheRead")
	cost.CacheWrite = read("cacheWrite")
	return cost
}

// GetRadiusCredentialConfig reads the cached gateway config from a credential.
func GetRadiusCredentialConfig(credential *OAuthCredential) *RadiusGatewayConfig {
	if credential == nil || credential.Extra == nil {
		return nil
	}
	raw, ok := credential.Extra[RadiusCredentialConfigKey]
	if !ok {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil
	}
	return sanitizeRadiusGatewayConfig(parsed)
}

// GetRadiusModelsFromConfig turns a gateway config into provider models.
func GetRadiusModelsFromConfig(providerID string, config *RadiusGatewayConfig) []*Model {
	if config == nil {
		return nil
	}
	models := make([]*Model, 0, len(config.Models))
	for _, entry := range config.Models {
		models = append(models, &Model{
			ID:               entry.ID,
			Name:             entry.Name,
			API:              APIPiMessages,
			Provider:         providerID,
			BaseURL:          config.BaseURL,
			Reasoning:        entry.Reasoning,
			ThinkingLevelMap: entry.ThinkingLevelMap,
			Input:            entry.Input,
			Cost:             entry.Cost,
			ContextWindow:    entry.ContextWindow,
			MaxTokens:        entry.MaxTokens,
		})
	}
	return models
}

// GetRadiusModels returns the legacy cached catalogue from a credential.
func GetRadiusModels(providerID string, credential *OAuthCredential) []*Model {
	return GetRadiusModelsFromConfig(providerID, GetRadiusCredentialConfig(credential))
}

func truncateHTTPBody(body string) string {
	trimmed := strings.TrimSpace(body)
	runes := []rune(trimmed)
	if len(runes) > 512 {
		return string(runes[:512]) + "…"
	}
	return trimmed
}

// LoadRadiusGatewayConfig fetches the gateway model catalogue (upstream
// loadRadiusGatewayConfig). The gateway is overridable for tests.
func LoadRadiusGatewayConfig(ctx context.Context, gateway, apiKey string) (*RadiusGatewayConfig, error) {
	requestURL := strings.TrimRight(gateway, "/") + "/v1/config"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("accept", "application/json")
	if apiKey != "" {
		request.Header.Set("authorization", "Bearer "+apiKey)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode >= 400 {
		return nil, fmt.Errorf("Could not load Radius config from %s: %d: %s",
			gateway, response.StatusCode, truncateHTTPBody(string(raw)))
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("Invalid Radius config from %s", gateway)
	}
	config := sanitizeRadiusGatewayConfig(parsed)
	if config == nil {
		return nil, fmt.Errorf("Invalid Radius config from %s", gateway)
	}
	return config, nil
}

// RadiusProviderOptions configure the Radius provider.
type RadiusProviderOptions struct {
	ID      string
	Name    string
	Gateway string
	// Streams overrides the pi-messages API implementation (unported, so the
	// default provider dispatches to a missing-implementation error).
	Streams ProviderStreams
}

// RadiusProvider builds the Radius gateway provider with a persisted,
// dynamically refreshed catalogue (upstream radiusProvider).
func RadiusProvider(options RadiusProviderOptions) *Provider {
	id := options.ID
	if id == "" {
		id = "radius"
	}
	name := options.Name
	if name == "" {
		name = "Radius"
	}
	gateway := NormalizeRadiusGatewayURL(options.Gateway)
	if options.Gateway == "" {
		gateway = NormalizeRadiusGatewayURL(DefaultRadiusGateway)
	}

	// Radius is a dynamic provider: its catalogue is persisted and refreshed
	// rather than read from the generated catalog, so the provider is built
	// directly (upstream returns an object literal with getModels/refreshModels
	// closures over the gateway's cache).
	cache := &radiusModelCache{}
	return &Provider{
		ID:   id,
		Name: name,
		Auth: ProviderAuth{
			APIKey: EnvApiKeyAuth("Radius API key", []string{"RADIUS_API_KEY"}),
			OAuth:  CreateRadiusOAuth(RadiusOAuthOptions{Name: name, Gateway: gateway}),
		},
		getModels: cache.get,
		RefreshModels: func(refresh *RefreshModelsContext) error {
			return refreshRadiusModels(id, gateway, cache, refresh)
		},
		streamsFor: func(model *Model) ProviderStreams { return options.Streams },
	}
}

// radiusModelCache holds one provider's refreshed catalogue (upstream closes
// over a local `models` binding).
type radiusModelCache struct {
	mu     sync.Mutex
	models []*Model
}

func (c *radiusModelCache) get() []*Model {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.models
}

func (c *radiusModelCache) set(models []*Model) {
	c.mu.Lock()
	c.models = models
	c.mu.Unlock()
}

// refreshRadiusModels implements the provider's dynamic catalogue refresh.
func refreshRadiusModels(id, gateway string, cache *radiusModelCache, refresh *RefreshModelsContext) error {
	if refresh.Stored != nil {
		var restored []*Model
		for _, model := range refresh.Stored.Models {
			if model.Provider == id {
				restored = append(restored, model)
			}
		}
		if !refresh.Publish(ModelsPublication{Update: func() { cache.set(restored) }}) {
			return nil
		}
	}

	// Import catalogues cached by the pre-ModelsStore Radius implementation.
	if refresh.Stored == nil && refresh.Credential != nil && refresh.Credential.Type == CredentialOAuth {
		legacy := GetRadiusModels(id, refresh.Credential.OAuth)
		if len(legacy) > 0 {
			models := legacy
			checkedAt := time.Now().UnixMilli()
			if !refresh.Publish(ModelsPublication{
				Persist: &ModelsStoreEntry{Models: models, CheckedAt: &checkedAt},
				Update:  func() { cache.set(models) },
			}) {
				return nil
			}
		}
	}

	if !refresh.AllowNetwork {
		return nil
	}
	ctx := refresh.Ctx
	if ctx != nil && ctx.Err() != nil {
		return nil
	}
	apiKey := ""
	if refresh.Credential != nil {
		if refresh.Credential.Type == CredentialOAuth && refresh.Credential.OAuth != nil {
			apiKey = refresh.Credential.OAuth.Access
		} else if refresh.Credential.APIKey != nil {
			apiKey = refresh.Credential.APIKey.Key
		}
	}
	config, err := LoadRadiusGatewayConfig(ctx, gateway, apiKey)
	if err != nil {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		return nil
	}
	refreshed := GetRadiusModelsFromConfig(id, config)
	checkedAt := time.Now().UnixMilli()
	refresh.Publish(ModelsPublication{
		Persist: &ModelsStoreEntry{Models: refreshed, CheckedAt: &checkedAt},
		Update:  func() { cache.set(refreshed) },
	})
	return nil
}

// CreateRadiusOAuth is declared in the OAuth file.
