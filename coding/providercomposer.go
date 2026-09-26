package coding

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dat267/pier/ai"
)

// Port of the models.json layer of core/provider-composer.ts: composing
// built-in providers with models.json custom models, overrides, configured
// auth, and request headers.
//
// D35: the extension layer of upstream's composer (ProviderConfigInput,
// ExtensionOAuthConfig, registerProvider / streamSimple overrides) is extension
// mechanics and is out of scope for the port, so every function here takes only
// the models.json config. The behaviour of the remaining layers (base provider
// plus models.json) is ported in full. D36 records that the ported compat union
// is the typed ai.ModelCompat, so merging decodes the merged flat object with
// the model's api (upstream merges flat objects that consumers interpret).

// AuthStatus is the auth configuration summary surfaced to the UI.
type AuthStatus struct {
	Configured bool
	// Source is one of "stored", "runtime", "environment", "fallback",
	// "models_json_key", "models_json_command".
	Source string
	Label  string
}

// ClearAPIKeyCache clears the configured-value command cache.
func ClearAPIKeyCache() { ClearConfigValueCache() }

// nestedCompatKeys are the compat fields merged key-by-key rather than
// replaced wholesale.
var nestedCompatKeys = []string{"openRouterRouting", "vercelGatewayRouting", "chatTemplateKwargs", "chatTemplateArgs"}

// mergeCompatJSON merges a typed base compat with a flat models.json override
// and decodes the result for the model's api.
func mergeCompatJSON(api ai.Api, base *ai.ModelCompat, override *ModelsJSONCompat) (*ai.ModelCompat, error) {
	if override == nil {
		return base, nil
	}
	baseRaw := []byte{}
	if base != nil {
		encoded, err := ai.MarshalJSON(base)
		if err != nil {
			return nil, err
		}
		baseRaw = encoded
	}
	overrideRaw, err := ai.MarshalJSON(override)
	if err != nil {
		return nil, err
	}
	merged, err := mergeCompatObjects(baseRaw, overrideRaw)
	if err != nil {
		return nil, err
	}
	return ai.DecodeModelCompat(api, merged)
}

// mergeCompatObjects merges two flat compat objects, merging the nested objects
// key by key.
func mergeCompatObjects(baseRaw, overrideRaw []byte) ([]byte, error) {
	base := decodeCompatObject(baseRaw)
	override := decodeCompatObject(overrideRaw)
	for key, value := range override {
		if containsString(nestedCompatKeys, key) {
			baseNested, baseIsObject := base[key].(map[string]any)
			overrideNested, overrideIsObject := value.(map[string]any)
			if baseIsObject || overrideIsObject {
				mergedNested := map[string]any{}
				for nestedKey, nestedValue := range baseNested {
					mergedNested[nestedKey] = nestedValue
				}
				for nestedKey, nestedValue := range overrideNested {
					mergedNested[nestedKey] = nestedValue
				}
				base[key] = mergedNested
				continue
			}
		}
		base[key] = value
	}
	return ai.MarshalJSON(base)
}

func decodeCompatObject(raw []byte) map[string]any {
	out := map[string]any{}
	if len(raw) == 0 || string(raw) == "null" {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

// applyModelOverride applies a models.json model override to a base model.
// mergeInputLimits merges a models.json inputLimits override over the model's
// limits the way upstream mergeInputLimits does: a shallow spread at the top
// level, a shallow spread of images, and a shallow spread of images.resize. A
// block that sets one key therefore keeps the rest of the model's limits.
//
// A zero value means absent: every field in the schema has a minimum of 1, so an
// explicit zero cannot clear a limit.
func mergeInputLimits(base, override *ai.ModelInputLimits) *ai.ModelInputLimits {
	if override == nil {
		return base
	}
	merged := ai.ModelInputLimits{}
	if base != nil {
		merged = *base
	}
	if override.MaxRequestBytes != 0 {
		merged.MaxRequestBytes = override.MaxRequestBytes
	}
	if override.Images != nil {
		images := ai.ModelImageLimits{}
		if merged.Images != nil {
			images = *merged.Images
		}
		if override.Images.MaxPerMessage != 0 {
			images.MaxPerMessage = override.Images.MaxPerMessage
		}
		if override.Images.MaxPerRequest != 0 {
			images.MaxPerRequest = override.Images.MaxPerRequest
		}
		if override.Images.Resize != nil {
			resize := ai.ModelImageResize{}
			if images.Resize != nil {
				resize = *images.Resize
			}
			if override.Images.Resize.MaxWidth != 0 {
				resize.MaxWidth = override.Images.Resize.MaxWidth
			}
			if override.Images.Resize.MaxHeight != 0 {
				resize.MaxHeight = override.Images.Resize.MaxHeight
			}
			if override.Images.Resize.MaxBytes != 0 {
				resize.MaxBytes = override.Images.Resize.MaxBytes
			}
			if override.Images.Resize.JPEGQuality != 0 {
				resize.JPEGQuality = override.Images.Resize.JPEGQuality
			}
			resizeCopy := resize
			images.Resize = &resizeCopy
		}
		imagesCopy := images
		merged.Images = &imagesCopy
	}
	return &merged
}

func applyModelOverride(model *ai.Model, override ModelsJSONModelOverride) (*ai.Model, error) {
	updated := *model
	if override.Name != "" {
		updated.Name = override.Name
	}
	if override.Reasoning != nil {
		updated.Reasoning = *override.Reasoning
	}
	if override.ThinkingLevelMap != nil {
		merged := ai.ThinkingLevelMap{}
		for level, value := range model.ThinkingLevelMap {
			merged[level] = value
		}
		for level, value := range override.ThinkingLevelMap {
			merged[level] = value
		}
		updated.ThinkingLevelMap = merged
	}
	if override.Input != nil {
		updated.Input = append([]string{}, override.Input...)
	}
	if override.InputLimits != nil {
		updated.InputLimits = mergeInputLimits(model.InputLimits, override.InputLimits)
	}
	if override.Cost != nil {
		cost := model.Cost
		if override.Cost.Input != nil {
			cost.Input = *override.Cost.Input
		}
		if override.Cost.Output != nil {
			cost.Output = *override.Cost.Output
		}
		if override.Cost.CacheRead != nil {
			cost.CacheRead = *override.Cost.CacheRead
		}
		if override.Cost.CacheWrite != nil {
			cost.CacheWrite = *override.Cost.CacheWrite
		}
		if override.Cost.Tiers != nil {
			cost.Tiers = costTiersFromJSON(override.Cost.Tiers)
		}
		updated.Cost = cost
	}
	if override.ContextWindow != nil {
		updated.ContextWindow = int64(*override.ContextWindow)
	}
	if override.MaxTokens != nil {
		updated.MaxTokens = int64(*override.MaxTokens)
	}
	if override.SamplingParams != nil {
		merged := map[string]json.RawMessage{}
		for key, value := range model.SamplingParams {
			merged[key] = value
		}
		for key, value := range override.SamplingParams {
			encoded, err := ai.MarshalJSON(value)
			if err != nil {
				continue
			}
			merged[key] = encoded
		}
		updated.SamplingParams = merged
	}
	compat, err := mergeCompatJSON(model.API, model.Compat, override.Compat)
	if err != nil {
		return nil, err
	}
	updated.Compat = compat
	return &updated, nil
}

func costTiersFromJSON(tiers []ModelsJSONCostTier) []ai.ModelCostTier {
	if len(tiers) == 0 {
		return nil
	}
	out := make([]ai.ModelCostTier, 0, len(tiers))
	for _, tier := range tiers {
		out = append(out, ai.ModelCostTier{
			InputTokensAbove: int64(tier.InputTokensAbove),
			ModelCostRates: ai.ModelCostRates{
				Input: tier.Input, Output: tier.Output,
				CacheRead: tier.CacheRead, CacheWrite: tier.CacheWrite,
			},
		})
	}
	return out
}

// modelFromJSON builds one custom model from a models.json definition.
func modelFromJSON(providerID string, definition ModelsJSONModel, providerConfig ModelsJSONProvider, defaults *ai.Model) (*ai.Model, error) {
	api := definition.API
	if api == "" {
		api = providerConfig.API
	}
	if api == "" && defaults != nil {
		api = defaults.API
	}
	if api == "" {
		return nil, fmt.Errorf(`Provider %s, model %s: no "api" specified. Set at provider or model level.`, providerID, definition.ID)
	}
	baseURL := definition.BaseURL
	if baseURL == "" {
		baseURL = providerConfig.BaseURL
	}
	if baseURL == "" && defaults != nil {
		baseURL = defaults.BaseURL
	}
	if baseURL == "" {
		return nil, fmt.Errorf(`Provider %s: "baseUrl" is required when defining custom models.`, providerID)
	}
	if definition.ContextWindow != nil && *definition.ContextWindow <= 0 {
		return nil, fmt.Errorf("Provider %s, model %s: invalid contextWindow", providerID, definition.ID)
	}
	if definition.MaxTokens != nil && *definition.MaxTokens <= 0 {
		return nil, fmt.Errorf("Provider %s, model %s: invalid maxTokens", providerID, definition.ID)
	}

	name := definition.Name
	if name == "" {
		name = definition.ID
	}
	reasoning := false
	if definition.Reasoning != nil {
		reasoning = *definition.Reasoning
	}
	input := definition.Input
	if input == nil {
		input = []string{"text"}
	}
	var cost ai.ModelCost
	if definition.Cost != nil {
		cost = ai.ModelCost{ModelCostRates: ai.ModelCostRates{
			Input: definition.Cost.Input, Output: definition.Cost.Output,
			CacheRead: definition.Cost.CacheRead, CacheWrite: definition.Cost.CacheWrite,
		}}
		cost.Tiers = costTiersFromJSON(definition.Cost.Tiers)
	}
	contextWindow := int64(128000)
	if definition.ContextWindow != nil {
		contextWindow = int64(*definition.ContextWindow)
	}
	maxTokens := int64(16384)
	if definition.MaxTokens != nil {
		maxTokens = int64(*definition.MaxTokens)
	}

	compat, err := mergeCompatJSON(api, compatFromConfig(providerConfig.Compat, api), definition.Compat)
	if err != nil {
		return nil, err
	}
	samplingParams := map[string]json.RawMessage{}
	for key, value := range definition.SamplingParams {
		encoded, err := ai.MarshalJSON(value)
		if err != nil {
			continue
		}
		samplingParams[key] = encoded
	}
	if len(samplingParams) == 0 {
		samplingParams = nil
	}

	return &ai.Model{
		ID:               definition.ID,
		Name:             name,
		API:              api,
		Provider:         providerID,
		BaseURL:          baseURL,
		Reasoning:        reasoning,
		ThinkingLevelMap: definition.ThinkingLevelMap,
		Input:            append([]string{}, input...),
		InputLimits:      definition.InputLimits,
		Cost:             cost,
		ContextWindow:    contextWindow,
		MaxTokens:        maxTokens,
		SamplingParams:   samplingParams,
		Compat:           compat,
	}, nil
}

// compatFromConfig decodes a provider-level compat for an api.
func compatFromConfig(compat *ModelsJSONCompat, api ai.Api) *ai.ModelCompat {
	if compat == nil {
		return nil
	}
	encoded, err := ai.MarshalJSON(compat)
	if err != nil {
		return nil
	}
	decoded, err := ai.DecodeModelCompat(api, encoded)
	if err != nil {
		return nil
	}
	return decoded
}

// findModelDefaults picks the baseline model used as the default source for a
// custom definition.
func findModelDefaults(models []*ai.Model, modelID, api string) *ai.Model {
	for _, model := range models {
		if model.ID == modelID {
			return model
		}
	}
	if api != "" {
		for _, model := range models {
			if model.API == api {
				return model
			}
		}
	}
	for _, model := range models {
		if model.API == ai.APIOpenAICompletions {
			return model
		}
	}
	if len(models) > 0 {
		return models[0]
	}
	return nil
}

// applyModelsJSON composes the base models with the models.json config.
func applyModelsJSON(providerID string, baseModels []*ai.Model, config *ModelsJSONProvider) ([]*ai.Model, error) {
	if config == nil {
		return append([]*ai.Model{}, baseModels...), nil
	}
	if config.OAuth != "" && config.BaseURL == "" {
		return nil, fmt.Errorf(`Provider %s: "baseUrl" is required when "oauth" is set.`, providerID)
	}
	hasOverrides := len(config.ModelOverrides) > 0
	if len(config.Models) == 0 && config.BaseURL == "" && len(config.Headers) == 0 && config.Compat == nil &&
		!hasOverrides && config.APIKey == "" && config.OAuth == "" && config.AuthHeader == nil {
		return nil, fmt.Errorf(`Provider %s: must specify "baseUrl", "headers", "compat", "modelOverrides", or "models".`, providerID)
	}

	models := make([]*ai.Model, 0, len(baseModels)+len(config.Models))
	for _, model := range baseModels {
		updated := *model
		if config.OAuth != "radius" && config.BaseURL != "" {
			updated.BaseURL = config.BaseURL
		}
		compat, err := mergeCompatJSON(model.API, model.Compat, config.Compat)
		if err != nil {
			return nil, err
		}
		updated.Compat = compat
		models = append(models, &updated)
	}
	for _, definition := range config.Models {
		existingIndex := -1
		for index, model := range models {
			if model.ID == definition.ID {
				existingIndex = index
				break
			}
		}
		defAPI := definition.API
		if defAPI == "" {
			defAPI = config.API
		}
		model, err := modelFromJSON(providerID, definition, *config, findModelDefaults(models, definition.ID, defAPI))
		if err != nil {
			return nil, err
		}
		if existingIndex >= 0 {
			models[existingIndex] = model
		} else {
			models = append(models, model)
		}
	}
	return models, nil
}

// configuredAPIKey is the models.json configured key.
func configuredAPIKey(config *ModelsJSONProvider) string {
	if config == nil {
		return ""
	}
	return config.APIKey
}

// configuredHeaders are the models.json provider headers.
func configuredHeaders(config *ModelsJSONProvider) map[string]string {
	if config == nil || len(config.Headers) == 0 {
		return nil
	}
	out := map[string]string{}
	for key, value := range config.Headers {
		out[key] = value
	}
	return out
}

// configContextEnv resolves the environment values a config value references.
func configContextEnv(values []string, ctx ai.AuthContext, explicit map[string]string) map[string]string {
	env := map[string]string{}
	for key, value := range explicit {
		env[key] = value
	}
	seen := map[string]bool{}
	for _, value := range values {
		for _, name := range GetConfigValueEnvVarNames(value) {
			if seen[name] {
				continue
			}
			seen[name] = true
			if _, ok := env[name]; ok {
				continue
			}
			if resolved, ok := ctx.Env(name); ok {
				env[name] = resolved
			}
		}
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

// composeAPIKeyAuth composes the api-key auth method for a provider from the
// base provider and models.json config.
func composeAPIKeyAuth(providerID string, base *ai.Provider, config *ModelsJSONProvider) (*ai.ApiKeyAuth, error) {
	var inherited *ai.ApiKeyAuth
	var baseOAuth *ai.OAuthAuth
	if base != nil {
		inherited = base.Auth.APIKey
		baseOAuth = base.Auth.OAuth
	}
	rawKey := configuredAPIKey(config)
	// OAuth-only providers get no fabricated API-key login method.
	if inherited == nil && rawKey == "" && baseOAuth != nil {
		return nil, nil
	}
	rawHeaders := configuredHeaders(config)
	authHeader := config != nil && config.AuthHeader != nil && *config.AuthHeader

	name := "API key"
	if inherited != nil && inherited.Name != "" {
		name = inherited.Name
	}
	auth := &ai.ApiKeyAuth{Name: name}
	if inherited != nil {
		auth.Login = inherited.Login
	}
	if auth.Login == nil {
		auth.Login = func(interaction *ai.AuthInteraction) (*ai.ApiKeyCredential, error) {
			key, err := interaction.Prompt(ai.AuthPrompt{Type: ai.AuthPromptSecret, Message: "Enter API key"})
			if err != nil {
				return nil, err
			}
			return &ai.ApiKeyCredential{Key: key}, nil
		}
	}

	auth.Check = func(input ai.AuthResolveInput) (*ai.AuthCheck, error) {
		if input.Credential != nil {
			if inherited != nil && inherited.Check != nil {
				return inherited.Check(input)
			}
			if input.Credential.Key != "" {
				return &ai.AuthCheck{Type: ai.AuthTypeAPIKey, Source: "stored credential"}, nil
			}
			if inherited != nil && inherited.Resolve != nil {
				resolved, err := inherited.Resolve(input)
				if err != nil || resolved == nil {
					return nil, err
				}
				return &ai.AuthCheck{Type: ai.AuthTypeAPIKey, Source: resolved.Source}, nil
			}
			return nil, nil
		}
		if rawKey != "" {
			if IsCommandConfigValue(rawKey) {
				return &ai.AuthCheck{Type: ai.AuthTypeAPIKey, Source: "configured API key"}, nil
			}
			for _, name := range GetConfigValueEnvVarNames(rawKey) {
				if _, ok := input.Ctx.Env(name); !ok {
					return nil, nil
				}
			}
			return &ai.AuthCheck{Type: ai.AuthTypeAPIKey, Source: "configured API key"}, nil
		}
		if inherited != nil && inherited.Check != nil {
			return inherited.Check(input)
		}
		if inherited != nil && inherited.Resolve != nil {
			resolved, err := inherited.Resolve(input)
			if err != nil || resolved == nil {
				return nil, err
			}
			return &ai.AuthCheck{Type: ai.AuthTypeAPIKey, Source: resolved.Source}, nil
		}
		return nil, nil
	}

	auth.Resolve = func(input ai.AuthResolveInput) (*ai.AuthResult, error) {
		var result *ai.AuthResult
		switch {
		case input.Credential != nil:
			if inherited != nil && inherited.Resolve != nil {
				resolved, err := inherited.Resolve(input)
				if err != nil {
					return nil, err
				}
				result = resolved
			} else if input.Credential.Key != "" {
				result = &ai.AuthResult{
					Auth:   ai.ModelAuth{APIKey: input.Credential.Key},
					Env:    ai.ProviderEnv(input.Credential.Env),
					Source: "stored credential",
				}
			}
		case rawKey != "":
			env := configContextEnv([]string{rawKey}, input.Ctx, nil)
			key, err := ResolveConfigValueOrThrow(rawKey, fmt.Sprintf(`API key for provider "%s"`, providerID), env)
			if err != nil {
				return nil, err
			}
			if inherited != nil && inherited.Resolve != nil {
				resolved, err := inherited.Resolve(ai.AuthResolveInput{
					Ctx: input.Ctx, Ctx2: input.Ctx2,
					Credential: &ai.ApiKeyCredential{Key: key},
				})
				if err != nil {
					return nil, err
				}
				result = resolved
			} else {
				result = &ai.AuthResult{Auth: ai.ModelAuth{APIKey: key}, Source: "configured API key"}
			}
		default:
			if inherited != nil && inherited.Resolve != nil {
				resolved, err := inherited.Resolve(input)
				if err != nil {
					return nil, err
				}
				result = resolved
			}
		}
		if result == nil {
			return nil, nil
		}
		explicitEnv := map[string]string{}
		if input.Credential != nil {
			for key, value := range input.Credential.Env {
				explicitEnv[key] = value
			}
		}
		for key, value := range result.Env {
			explicitEnv[key] = value
		}
		values := make([]string, 0, len(rawHeaders))
		for _, value := range rawHeaders {
			values = append(values, value)
		}
		headerEnv := configContextEnv(values, input.Ctx, explicitEnv)
		headers, err := ResolveHeadersOrThrow(rawHeaders, fmt.Sprintf(`provider "%s"`, providerID), headerEnv)
		if err != nil {
			return nil, err
		}
		withAuth, err := withConfiguredAuth(result.Auth, headers, authHeader)
		if err != nil {
			return nil, err
		}
		return &ai.AuthResult{Auth: withAuth, Env: result.Env, Source: result.Source}, nil
	}
	return auth, nil
}

// withConfiguredAuth merges configured headers into resolved request auth and
// applies the auth-header option.
func withConfiguredAuth(auth ai.ModelAuth, headers map[string]string, authHeader bool) (ai.ModelAuth, error) {
	var merged ai.ProviderHeaders
	if auth.Headers != nil || len(headers) > 0 {
		merged = ai.ProviderHeaders{}
		for key, value := range auth.Headers {
			merged[key] = value
		}
		for key, value := range headers {
			copied := value
			merged[key] = &copied
		}
	}
	if authHeader {
		if auth.APIKey == "" {
			return ai.ModelAuth{}, fmt.Errorf("authHeader requires a resolved API key")
		}
		if merged == nil {
			merged = ai.ProviderHeaders{}
		}
		authorization := "Bearer " + auth.APIKey
		merged["Authorization"] = &authorization
	}
	return ai.ModelAuth{APIKey: auth.APIKey, Headers: merged, BaseURL: auth.BaseURL}, nil
}

// composeOAuthAuth returns the provider's OAuth auth method with models.json
// headers and the auth-header option applied to derived request auth.
func composeOAuthAuth(providerID string, base *ai.Provider, config *ModelsJSONProvider) *ai.OAuthAuth {
	if base == nil || base.Auth.OAuth == nil {
		return nil
	}
	oauth := base.Auth.OAuth
	rawHeaders := configuredHeaders(config)
	authHeader := config != nil && config.AuthHeader != nil && *config.AuthHeader
	composed := *oauth
	inner := oauth.ToAuth
	composed.ToAuth = func(credential *ai.OAuthCredential) (*ai.ModelAuth, error) {
		auth := ai.ModelAuth{}
		if inner != nil {
			derived, err := inner(credential)
			if err != nil {
				return nil, err
			}
			if derived != nil {
				auth = *derived
			}
		}
		env := oauthCredentialEnv(credential)
		headers, err := ResolveHeadersOrThrow(rawHeaders, fmt.Sprintf(`provider "%s"`, providerID), env)
		if err != nil {
			return nil, err
		}
		result, err := withConfiguredAuth(auth, headers, authHeader)
		if err != nil {
			return nil, err
		}
		return &result, nil
	}
	return &composed
}

// rawModelHeaders gathers the configured headers for one model.
func rawModelHeaders(model *ai.Model, config *ModelsJSONProvider) map[string]string {
	if config == nil {
		return nil
	}
	headers := map[string]string{}
	if override, ok := config.ModelOverrides[model.ID]; ok {
		for key, value := range override.Headers {
			headers[key] = value
		}
	}
	for _, definition := range config.Models {
		if definition.ID == model.ID {
			for key, value := range definition.Headers {
				headers[key] = value
			}
		}
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

// ComposeModelProvider composes the built-in and models.json layers without
// reading credentials.
func ComposeModelProvider(providerID string, base *ai.Provider, modelConfig *ModelConfig) (*ai.Provider, error) {
	config := modelConfig.GetProvider(providerID)

	// Validate eagerly so registration reports structural errors immediately.
	if _, err := applyModelsJSON(providerID, baseModels(base), config); err != nil {
		return nil, err
	}
	getModels := func() []*ai.Model {
		models, err := applyModelsJSON(providerID, baseModels(base), config)
		if err != nil {
			return nil
		}
		out := make([]*ai.Model, 0, len(models))
		for _, model := range models {
			if config != nil {
				if override, ok := config.ModelOverrides[model.ID]; ok {
					applied, err := applyModelOverride(model, override)
					if err != nil {
						continue
					}
					out = append(out, applied)
					continue
				}
			}
			out = append(out, model)
		}
		return out
	}

	apiKey, err := composeAPIKeyAuth(providerID, base, config)
	if err != nil {
		return nil, err
	}
	oauth := composeOAuthAuth(providerID, base, config)
	if apiKey == nil && oauth == nil {
		return nil, fmt.Errorf("Provider %s: no authentication method configured.", providerID)
	}

	name := providerID
	switch {
	case config != nil && config.Name != "":
		name = config.Name
	case base != nil && base.Name != "":
		name = base.Name
	}
	baseURL := ""
	if base != nil {
		baseURL = base.BaseURL
	}
	if config != nil && config.BaseURL != "" {
		baseURL = config.BaseURL
	}

	supportsBaseAPI := func(model *ai.Model) bool {
		if base == nil {
			return false
		}
		for _, entry := range base.GetModels() {
			if entry.API == model.API {
				return true
			}
		}
		return false
	}
	streamWith := func(model *ai.Model, context ai.TranscriptContext, options *ai.StreamOptions, simple bool, simpleOptions *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
		if base != nil && supportsBaseAPI(model) {
			if simple {
				return base.StreamSimple(model, context, simpleOptions)
			}
			return base.Stream(model, context, options)
		}
		api := ai.GetAPIProvider(model.API)
		if api == nil {
			return ai.ErrorStreamForModel(model, ai.ErrCodeProvider, fmt.Sprintf("No API provider registered for api: %s", model.API))
		}
		if simple {
			return api.StreamSimple(model, context, simpleOptions)
		}
		return api.Stream(model, context, options)
	}

	var headers ai.ProviderHeaders
	if base != nil {
		headers = base.Headers
	}
	streams := funcStreams{
		stream: func(model *ai.Model, context ai.TranscriptContext, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
			return streamWith(model, context, options, false, nil)
		},
		streamSimple: func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
			return streamWith(model, context, nil, true, options)
		},
	}
	var implementation ai.ProviderStreams = streams
	if base != nil && base.HasDeferred() {
		implementation = funcStreamsWithDeferred{funcStreams: streams, base: base}
	}

	provider := ai.CreateProvider(ai.CreateProviderOptions{
		ID:           providerID,
		Name:         name,
		BaseURL:      baseURL,
		Headers:      headers,
		Auth:         ai.ProviderAuth{APIKey: apiKey, OAuth: oauth},
		ModelsGetter: getModels,
		Single:       implementation,
	})
	if base != nil {
		provider.RefreshModels = base.RefreshModels
		provider.FilterModels = base.FilterModels
	}
	return provider, nil
}

// funcStreams adapts two functions to ai.ProviderStreams.
type funcStreams struct {
	stream       func(model *ai.Model, context ai.TranscriptContext, options *ai.StreamOptions) *ai.AssistantMessageEventStream
	streamSimple func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream
}

func (f funcStreams) Stream(model *ai.Model, context ai.TranscriptContext, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	return f.stream(model, context, options)
}

func (f funcStreams) StreamSimple(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	return f.streamSimple(model, context, options)
}

// funcStreamsWithDeferred delegates deferred responses to the base provider so
// the composed provider keeps that capability.
type funcStreamsWithDeferred struct {
	funcStreams
	base *ai.Provider
}

func (f funcStreamsWithDeferred) FetchDeferred(model *ai.Model, handle *ai.DeferredHandle, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	return f.base.FetchDeferred(model, handle, options)
}

func (f funcStreamsWithDeferred) CancelDeferred(model *ai.Model, handle *ai.DeferredHandle, options *ai.StreamOptions) error {
	return f.base.CancelDeferred(model, handle, options)
}

func baseModels(base *ai.Provider) []*ai.Model {
	if base == nil {
		return nil
	}
	return base.GetModels()
}

// ResolveConfiguredModelHeaders resolves the configured headers for one model.
func ResolveConfiguredModelHeaders(model *ai.Model, config *ModelsJSONProvider, env map[string]string) (map[string]string, error) {
	return ResolveHeadersOrThrow(rawModelHeaders(model, config), fmt.Sprintf(`model "%s/%s"`, model.Provider, model.ID), env)
}

// CompatibilityRequestConfig is the compatibility-path request configuration.
type CompatibilityRequestConfig struct {
	Headers    ai.ProviderHeaders
	AuthHeader bool
}

// ResolveCompatibilityRequestConfig resolves the headers used when a request
// runs without resolved auth.
func ResolveCompatibilityRequestConfig(model *ai.Model, config *ModelsJSONProvider) (CompatibilityRequestConfig, error) {
	combined := map[string]string{}
	for key, value := range configuredHeaders(config) {
		combined[key] = value
	}
	for key, value := range rawModelHeaders(model, config) {
		combined[key] = value
	}
	configured, err := ResolveHeadersOrThrow(combined, fmt.Sprintf(`model "%s/%s"`, model.Provider, model.ID), nil)
	if err != nil {
		return CompatibilityRequestConfig{}, err
	}
	var headers ai.ProviderHeaders
	if model.Headers != nil || configured != nil {
		headers = ai.ProviderHeaders{}
		for key, value := range model.Headers {
			copied := value
			headers[key] = &copied
		}
		for key, value := range configured {
			copied := value
			headers[key] = &copied
		}
	}
	return CompatibilityRequestConfig{
		Headers:    headers,
		AuthHeader: config != nil && config.AuthHeader != nil && *config.AuthHeader,
	}, nil
}

// ConfiguredRequestAuthStatus reports how a provider's configured key is
// satisfied.
func ConfiguredRequestAuthStatus(config *ModelsJSONProvider) *AuthStatus {
	value := configuredAPIKey(config)
	if value == "" {
		return nil
	}
	if IsCommandConfigValue(value) {
		return &AuthStatus{Configured: true, Source: "models_json_command"}
	}
	names := GetConfigValueEnvVarNames(value)
	if len(names) > 0 {
		if IsConfigValueConfigured(value, nil) {
			return &AuthStatus{Configured: true, Source: "environment", Label: strings.Join(names, ", ")}
		}
		return &AuthStatus{Configured: false}
	}
	return &AuthStatus{Configured: true, Source: "models_json_key"}
}

// oauthCredentialEnv reads the credential-scoped environment values stored on
// an OAuth credential (upstream credential.env).
func oauthCredentialEnv(credential *ai.OAuthCredential) map[string]string {
	if credential == nil || len(credential.Extra) == 0 {
		return nil
	}
	raw, ok := credential.Extra["env"]
	if !ok || len(raw) == 0 {
		return nil
	}
	var env map[string]string
	if err := json.Unmarshal(raw, &env); err != nil || len(env) == 0 {
		return nil
	}
	return env
}
