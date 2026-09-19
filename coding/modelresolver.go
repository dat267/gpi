package coding

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/dat267/gpi/ai"
)

// Port of core/model-resolver.ts plus the thinking-level validation from
// core/defaults.ts and cli/args.ts.

// DefaultModelPerProvider maps each known provider to its default model id
// (upstream defaultModelPerProvider).
var DefaultModelPerProvider = map[ai.ProviderId]string{
	"amazon-bedrock":             "us.anthropic.claude-opus-4-6-v1",
	"ant-ling":                   "Ring-2.6-1T",
	"anthropic":                  "claude-opus-4-8",
	"openai":                     "gpt-5.5",
	"azure-openai-responses":     "gpt-5.4",
	"openai-codex":               "gpt-5.5",
	"radius":                     "balanced",
	"nvidia":                     "nvidia/nemotron-3-super-120b-a12b",
	"deepseek":                   "deepseek-v4-pro",
	"google":                     "gemini-3.1-pro-preview",
	"google-vertex":              "gemini-3.1-pro-preview",
	"github-copilot":             "gpt-5.4",
	"openrouter":                 "moonshotai/kimi-k2.6",
	"vercel-ai-gateway":          "zai/glm-5.1",
	"xai":                        "grok-4.6",
	"groq":                       "openai/gpt-oss-120b",
	"cerebras":                   "gpt-oss-120b",
	"zai":                        "glm-5.3",
	"zai-coding-cn":              "glm-5.3",
	"mistral":                    "devstral-medium-latest",
	"minimax":                    "MiniMax-M2.7",
	"minimax-cn":                 "MiniMax-M2.7",
	"moonshotai":                 "kimi-k2.6",
	"moonshotai-cn":              "kimi-k2.6",
	"huggingface":                "moonshotai/Kimi-K2.6",
	"fireworks":                  "accounts/fireworks/models/kimi-k2p6",
	"together":                   "moonshotai/Kimi-K2.6",
	"baseten":                    "zai-org/GLM-5.2",
	"opencode":                   "kimi-k2.6",
	"opencode-go":                "kimi-k2.6",
	"kimi-coding":                "kimi-for-coding",
	"cloudflare-workers-ai":      "@cf/moonshotai/kimi-k2.6",
	"cloudflare-ai-gateway":      "workers-ai/@cf/moonshotai/kimi-k2.6",
	"qwen-token-plan":            "qwen3.7-max",
	"qwen-token-plan-cn":         "qwen3.7-max",
	"qwen-token-plan-individual": "qwen3.8-max",
	"xiaomi":                     "mimo-v2.5-pro",
	"xiaomi-token-plan-cn":       "mimo-v2.5-pro",
	"xiaomi-token-plan-ams":      "mimo-v2.5-pro",
	"xiaomi-token-plan-sgp":      "mimo-v2.5-pro",
}

// IsValidThinkingLevel reports whether a string is a selectable thinking level.
func IsValidThinkingLevel(level string) bool {
	for _, candidate := range ThinkingLevelOptions {
		if string(candidate) == level {
			return true
		}
	}
	return false
}

// ScopedModel is one resolved model pattern.
type ScopedModel struct {
	Model *ai.Model
	// ThinkingLevel is set when the pattern carried one (e.g. "model:high").
	ThinkingLevel ai.ThinkingLevel
	HasThinking   bool
}

// IsAlias reports whether a model id looks like an alias (no date suffix).
func IsAlias(id string) bool {
	if strings.HasSuffix(id, "-latest") {
		return true
	}
	// A date suffix is -YYYYMMDD.
	if len(id) >= 9 && id[len(id)-9] == '-' {
		digits := id[len(id)-8:]
		allDigits := true
		for _, char := range digits {
			if char < '0' || char > '9' {
				allDigits = false
				break
			}
		}
		if allDigits {
			return false
		}
	}
	return true
}

// FindExactModelReferenceMatch finds a model by bare id or canonical
// provider/model reference; ambiguous bare ids are rejected.
func FindExactModelReferenceMatch(modelReference string, availableModels []*ai.Model) *ai.Model {
	trimmed := strings.TrimSpace(modelReference)
	if trimmed == "" {
		return nil
	}
	normalized := strings.ToLower(trimmed)

	var canonicalMatches []*ai.Model
	for _, model := range availableModels {
		if strings.ToLower(model.Provider+"/"+model.ID) == normalized {
			canonicalMatches = append(canonicalMatches, model)
		}
	}
	if len(canonicalMatches) == 1 {
		return canonicalMatches[0]
	}
	if len(canonicalMatches) > 1 {
		return nil
	}

	if slashIndex := strings.Index(trimmed, "/"); slashIndex != -1 {
		provider := strings.TrimSpace(trimmed[:slashIndex])
		modelID := strings.TrimSpace(trimmed[slashIndex+1:])
		if provider != "" && modelID != "" {
			var providerMatches []*ai.Model
			for _, model := range availableModels {
				if strings.EqualFold(model.Provider, provider) && strings.EqualFold(model.ID, modelID) {
					providerMatches = append(providerMatches, model)
				}
			}
			if len(providerMatches) == 1 {
				return providerMatches[0]
			}
			if len(providerMatches) > 1 {
				return nil
			}
		}
	}

	var idMatches []*ai.Model
	for _, model := range availableModels {
		if strings.ToLower(model.ID) == normalized {
			idMatches = append(idMatches, model)
		}
	}
	if len(idMatches) == 1 {
		return idMatches[0]
	}
	return nil
}

// tryMatchModel matches a pattern exactly, then by partial id/name match.
func tryMatchModel(modelPattern string, availableModels []*ai.Model) *ai.Model {
	if exact := FindExactModelReferenceMatch(modelPattern, availableModels); exact != nil {
		return exact
	}
	lowered := strings.ToLower(modelPattern)
	var matches []*ai.Model
	for _, model := range availableModels {
		if strings.Contains(strings.ToLower(model.ID), lowered) ||
			(model.Name != "" && strings.Contains(strings.ToLower(model.Name), lowered)) {
			matches = append(matches, model)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	var aliases, dated []*ai.Model
	for _, model := range matches {
		if IsAlias(model.ID) {
			aliases = append(aliases, model)
		} else {
			dated = append(dated, model)
		}
	}
	sortByIDDesc := func(models []*ai.Model) {
		sort.SliceStable(models, func(i, j int) bool { return models[i].ID > models[j].ID })
	}
	if len(aliases) > 0 {
		sortByIDDesc(aliases)
		return aliases[0]
	}
	sortByIDDesc(dated)
	return dated[0]
}

// ParsedModelResult is one parsed model pattern (upstream ParsedModelResult).
type ParsedModelResult struct {
	Model         *ai.Model
	ThinkingLevel ai.ThinkingLevel
	HasThinking   bool
	Warning       string
}

// ParseModelPatternOptions configure ParseModelPattern.
type ParseModelPatternOptions struct {
	// AllowInvalidThinkingLevelFallback is nil for the upstream default (true).
	AllowInvalidThinkingLevelFallback *bool
}

// ParseModelPattern extracts a model and an optional thinking level from a
// pattern, handling model ids that contain colons.
func ParseModelPattern(pattern string, availableModels []*ai.Model, options *ParseModelPatternOptions) ParsedModelResult {
	if exact := tryMatchModel(pattern, availableModels); exact != nil {
		return ParsedModelResult{Model: exact}
	}

	lastColonIndex := strings.LastIndex(pattern, ":")
	if lastColonIndex == -1 {
		return ParsedModelResult{}
	}
	prefix := pattern[:lastColonIndex]
	suffix := pattern[lastColonIndex+1:]

	if IsValidThinkingLevel(suffix) {
		result := ParseModelPattern(prefix, availableModels, options)
		if result.Model != nil {
			level := ai.ThinkingLevel(suffix)
			if result.Warning != "" {
				return ParsedModelResult{Model: result.Model, Warning: result.Warning}
			}
			return ParsedModelResult{Model: result.Model, ThinkingLevel: level, HasThinking: true}
		}
		return result
	}

	allowFallback := true
	if options != nil && options.AllowInvalidThinkingLevelFallback != nil {
		allowFallback = *options.AllowInvalidThinkingLevelFallback
	}
	if !allowFallback {
		return ParsedModelResult{}
	}
	result := ParseModelPattern(prefix, availableModels, options)
	if result.Model != nil {
		return ParsedModelResult{
			Model:   result.Model,
			Warning: fmt.Sprintf("Invalid thinking level %q in pattern %q. Using default instead.", suffix, pattern),
		}
	}
	return result
}

// ModelScopeDiagnostic is one scope warning.
type ModelScopeDiagnostic struct {
	Type    string // "warning"
	Code    string // "no-match" | "invalid-thinking-level"
	Message string
	Pattern string
}

// ResolveModelScopeResult is the scope resolution result.
type ResolveModelScopeResult struct {
	ScopedModels []ScopedModel
	Diagnostics  []ModelScopeDiagnostic
}

// ResolveModelScopeFromModels resolves model patterns over a model list.
func ResolveModelScopeFromModels(patterns []string, models []*ai.Model) ResolveModelScopeResult {
	availableModels := append([]*ai.Model{}, models...)
	var scopedModels []ScopedModel
	var diagnostics []ModelScopeDiagnostic

	has := func(model *ai.Model) bool {
		for _, scoped := range scopedModels {
			if ai.ModelsAreEqual(scoped.Model, model) {
				return true
			}
		}
		return false
	}

	for _, pattern := range patterns {
		if strings.ContainsAny(pattern, "*?[") {
			colonIndex := strings.LastIndex(pattern, ":")
			globPattern := pattern
			var thinkingLevel ai.ThinkingLevel
			hasThinking := false
			if colonIndex != -1 {
				suffix := pattern[colonIndex+1:]
				if IsValidThinkingLevel(suffix) {
					thinkingLevel = ai.ThinkingLevel(suffix)
					hasThinking = true
					globPattern = pattern[:colonIndex]
				}
			}

			if exact := FindExactModelReferenceMatch(globPattern, availableModels); exact != nil {
				if !has(exact) {
					scopedModels = append(scopedModels, ScopedModel{Model: exact, ThinkingLevel: thinkingLevel, HasThinking: hasThinking})
				}
				continue
			}

			var matchingModels []*ai.Model
			for _, model := range availableModels {
				fullID := model.Provider + "/" + model.ID
				if MatchGlob(globPattern, fullID, true) || MatchGlob(globPattern, model.ID, true) {
					matchingModels = append(matchingModels, model)
				}
			}
			if len(matchingModels) == 0 {
				diagnostics = append(diagnostics, ModelScopeDiagnostic{
					Type: "warning", Code: "no-match",
					Message: fmt.Sprintf("No models match pattern %q", pattern), Pattern: pattern,
				})
				continue
			}
			for _, model := range matchingModels {
				if !has(model) {
					scopedModels = append(scopedModels, ScopedModel{Model: model, ThinkingLevel: thinkingLevel, HasThinking: hasThinking})
				}
			}
			continue
		}

		parsed := ParseModelPattern(pattern, availableModels, nil)
		if parsed.Warning != "" {
			diagnostics = append(diagnostics, ModelScopeDiagnostic{
				Type: "warning", Code: "invalid-thinking-level", Message: parsed.Warning, Pattern: pattern,
			})
		}
		if parsed.Model == nil {
			diagnostics = append(diagnostics, ModelScopeDiagnostic{
				Type: "warning", Code: "no-match",
				Message: fmt.Sprintf("No models match pattern %q", pattern), Pattern: pattern,
			})
			continue
		}
		if !has(parsed.Model) {
			scopedModels = append(scopedModels, ScopedModel{
				Model: parsed.Model, ThinkingLevel: parsed.ThinkingLevel, HasThinking: parsed.HasThinking,
			})
		}
	}
	return ResolveModelScopeResult{ScopedModels: scopedModels, Diagnostics: diagnostics}
}

// ResolveCliModelResult is the CLI model resolution result.
type ResolveCliModelResult struct {
	Model         *ai.Model
	ThinkingLevel ai.ThinkingLevel
	HasThinking   bool
	Warning       string
	// Error is a CLI-facing error; when set the model is nil.
	Error string
}

// ResolveCliModelOptions configure ResolveCliModel.
type ResolveCliModelOptions struct {
	CLIProvider    string
	CLIModel       string
	CLIThinking    ai.ThinkingLevel
	HasCLIThinking bool
	ModelRuntime   ModelRuntime
}

// ResolveCliModel resolves a single model from CLI flags.
func ResolveCliModel(options ResolveCliModelOptions) ResolveCliModelResult {
	cliProvider := options.CLIProvider
	cliModel := options.CLIModel
	modelRuntime := options.ModelRuntime

	if cliModel == "" {
		return ResolveCliModelResult{}
	}

	availableModels := modelRuntime.GetModels("")
	if len(availableModels) == 0 {
		return ResolveCliModelResult{Error: "No models available. Check your installation or add models to models.json."}
	}

	providerMap := map[string]string{}
	for _, model := range availableModels {
		providerMap[strings.ToLower(model.Provider)] = model.Provider
	}

	provider := ""
	if cliProvider != "" {
		provider = providerMap[strings.ToLower(cliProvider)]
		if provider == "" {
			return ResolveCliModelResult{Error: fmt.Sprintf("Unknown provider %q. Use --list-models to see available providers/models.", cliProvider)}
		}
	}

	pattern := cliModel
	inferredProvider := false
	if provider == "" {
		if slashIndex := strings.Index(cliModel, "/"); slashIndex != -1 {
			maybeProvider := cliModel[:slashIndex]
			if canonical, ok := providerMap[strings.ToLower(maybeProvider)]; ok {
				provider = canonical
				pattern = cliModel[slashIndex+1:]
				inferredProvider = true
			}
		}
	}

	if provider == "" {
		lower := strings.ToLower(cliModel)
		var exactMatches []*ai.Model
		for _, model := range availableModels {
			if strings.ToLower(model.ID) == lower || strings.ToLower(model.Provider+"/"+model.ID) == lower {
				exactMatches = append(exactMatches, model)
			}
		}
		if len(exactMatches) == 1 {
			return ResolveCliModelResult{Model: exactMatches[0]}
		}
		if len(exactMatches) > 1 {
			var authenticated []*ai.Model
			for _, model := range exactMatches {
				if modelRuntime.HasConfiguredAuth(model.Provider) {
					authenticated = append(authenticated, model)
				}
			}
			if len(authenticated) == 1 {
				return ResolveCliModelResult{Model: authenticated[0]}
			}
			var described []string
			for _, model := range exactMatches {
				described = append(described, model.Provider+"/"+model.ID)
			}
			sort.Strings(described)
			authHint := "No matching provider is authenticated."
			if len(authenticated) > 0 {
				authHint = "More than one matching provider is authenticated."
			}
			return ResolveCliModelResult{Error: fmt.Sprintf(
				"Model %q is ambiguous across providers: %s. %s Use --provider or provider/model.",
				cliModel, strings.Join(described, ", "), authHint)}
		}
	}

	if cliProvider != "" && provider != "" {
		prefix := provider + "/"
		if strings.HasPrefix(strings.ToLower(cliModel), strings.ToLower(prefix)) {
			pattern = cliModel[len(prefix):]
		}
	}

	var candidates []*ai.Model
	if provider != "" {
		for _, model := range availableModels {
			if model.Provider == provider {
				candidates = append(candidates, model)
			}
		}
	} else {
		candidates = availableModels
	}
	strict := false
	parsed := ParseModelPattern(pattern, candidates, &ParseModelPatternOptions{
		AllowInvalidThinkingLevelFallback: &strict,
	})

	if parsed.Model != nil {
		if inferredProvider {
			var rawExactMatches []*ai.Model
			for _, model := range availableModels {
				if strings.ToLower(model.ID) == strings.ToLower(cliModel) && !ai.ModelsAreEqual(model, parsed.Model) {
					rawExactMatches = append(rawExactMatches, model)
				}
			}
			if len(rawExactMatches) > 0 && !modelRuntime.HasConfiguredAuth(parsed.Model.Provider) {
				var authenticatedRaw []*ai.Model
				for _, model := range rawExactMatches {
					if modelRuntime.HasConfiguredAuth(model.Provider) {
						authenticatedRaw = append(authenticatedRaw, model)
					}
				}
				if len(authenticatedRaw) == 1 {
					return ResolveCliModelResult{Model: authenticatedRaw[0]}
				}
			}
		}
		return ResolveCliModelResult{
			Model: parsed.Model, ThinkingLevel: parsed.ThinkingLevel, HasThinking: parsed.HasThinking, Warning: parsed.Warning,
		}
	}

	if inferredProvider {
		lower := strings.ToLower(cliModel)
		for _, model := range availableModels {
			if strings.ToLower(model.ID) == lower || strings.ToLower(model.Provider+"/"+model.ID) == lower {
				return ResolveCliModelResult{Model: model}
			}
		}
		fallback := ParseModelPattern(cliModel, availableModels, &ParseModelPatternOptions{
			AllowInvalidThinkingLevelFallback: &strict,
		})
		if fallback.Model != nil {
			return ResolveCliModelResult{
				Model: fallback.Model, ThinkingLevel: fallback.ThinkingLevel, HasThinking: fallback.HasThinking, Warning: fallback.Warning,
			}
		}
	}

	if provider != "" {
		fallbackPattern := pattern
		var fallbackThinking ai.ThinkingLevel
		hasFallbackThinking := false
		if !options.HasCLIThinking {
			if lastColon := strings.LastIndex(pattern, ":"); lastColon != -1 {
				suffix := pattern[lastColon+1:]
				if IsValidThinkingLevel(suffix) {
					fallbackPattern = pattern[:lastColon]
					fallbackThinking = ai.ThinkingLevel(suffix)
					hasFallbackThinking = true
				}
			}
		}
		fallbackModel := buildFallbackModel(provider, fallbackPattern, availableModels)
		if fallbackModel != nil {
			requestedThinking := options.CLIThinking
			if !options.HasCLIThinking {
				requestedThinking = fallbackThinking
			}
			model := fallbackModel
			if requestedThinking != "" && requestedThinking != ai.ThinkOff {
				cloned := *fallbackModel
				cloned.Reasoning = true
				model = &cloned
			}
			warning := fmt.Sprintf("Model %q not found for provider %q. Using custom model id.", fallbackPattern, provider)
			if parsed.Warning != "" {
				warning = parsed.Warning + " " + warning
			}
			return ResolveCliModelResult{
				Model: model, ThinkingLevel: fallbackThinking, HasThinking: hasFallbackThinking, Warning: warning,
			}
		}
	}

	display := cliModel
	if provider != "" {
		display = provider + "/" + pattern
	}
	return ResolveCliModelResult{
		Warning: parsed.Warning,
		Error:   fmt.Sprintf("Model %q not found. Use --list-models to see available models.", display),
	}
}

func buildFallbackModel(provider, modelID string, availableModels []*ai.Model) *ai.Model {
	var providerModels []*ai.Model
	for _, model := range availableModels {
		if model.Provider == provider {
			providerModels = append(providerModels, model)
		}
	}
	if len(providerModels) == 0 {
		return nil
	}
	base := providerModels[0]
	if defaultID, ok := DefaultModelPerProvider[ai.ProviderId(provider)]; ok {
		for _, model := range providerModels {
			if model.ID == defaultID {
				base = model
				break
			}
		}
	}
	cloned := *base
	cloned.ID = modelID
	cloned.Name = modelID
	return &cloned
}

// InitialModelResult is the initial model selection.
//
// D21: upstream prints a CLI error and exits the process when the explicit
// --provider/--model pair cannot be resolved; the Go port reports it in Error
// so the caller decides (library code must not exit).
type InitialModelResult struct {
	Model           *ai.Model
	ThinkingLevel   ai.ThinkingLevel
	FallbackMessage string
	Error           string
}

// FindInitialModelOptions configure FindInitialModel.
type FindInitialModelOptions struct {
	CLIProvider          string
	CLIModel             string
	ScopedModels         []ScopedModel
	IsContinuing         bool
	DefaultProvider      string
	DefaultModelID       string
	DefaultThinkingLevel ai.ThinkingLevel
	HasDefaultThinking   bool
	ModelThinkingLevels  map[string]ai.ThinkingLevel
	ModelRuntime         ModelRuntime
}

// FindInitialModel picks the initial model with upstream's priority order.
func FindInitialModel(options FindInitialModelOptions) InitialModelResult {
	var model *ai.Model
	thinkingLevel := DefaultThinkingLevel

	if options.CLIProvider != "" && options.CLIModel != "" {
		resolved := ResolveCliModel(ResolveCliModelOptions{
			CLIProvider:  options.CLIProvider,
			CLIModel:     options.CLIModel,
			ModelRuntime: options.ModelRuntime,
		})
		if resolved.Error != "" {
			return InitialModelResult{Error: resolved.Error, ThinkingLevel: DefaultThinkingLevel}
		}
		if resolved.Model != nil {
			return InitialModelResult{Model: resolved.Model, ThinkingLevel: DefaultThinkingLevel}
		}
	}

	if len(options.ScopedModels) > 0 && !options.IsContinuing {
		scoped := options.ScopedModels[0]
		perModel, hasPerModel := options.ModelThinkingLevels[scoped.Model.Provider+"/"+scoped.Model.ID]
		switch {
		case scoped.HasThinking:
			thinkingLevel = scoped.ThinkingLevel
		case hasPerModel:
			thinkingLevel = perModel
		case options.HasDefaultThinking:
			thinkingLevel = options.DefaultThinkingLevel
		default:
			thinkingLevel = DefaultThinkingLevel
		}
		return InitialModelResult{Model: scoped.Model, ThinkingLevel: thinkingLevel}
	}

	if options.DefaultProvider != "" && options.DefaultModelID != "" {
		found := options.ModelRuntime.GetModel(options.DefaultProvider, options.DefaultModelID)
		if found != nil && options.ModelRuntime.HasConfiguredAuth(found.Provider) {
			model = found
			if perModel, ok := options.ModelThinkingLevels[options.DefaultProvider+"/"+options.DefaultModelID]; ok {
				thinkingLevel = perModel
			} else if options.HasDefaultThinking {
				thinkingLevel = options.DefaultThinkingLevel
			}
			return InitialModelResult{Model: model, ThinkingLevel: thinkingLevel}
		}
	}

	availableModels := options.ModelRuntime.GetAvailableSnapshot()
	if len(availableModels) > 0 {
		for _, providerID := range DefaultProviderOrder {
			defaultID := DefaultModelPerProvider[providerID]
			for _, candidate := range availableModels {
				if candidate.Provider == providerID && candidate.ID == defaultID {
					return InitialModelResult{Model: candidate, ThinkingLevel: DefaultThinkingLevel}
				}
			}
		}
		return InitialModelResult{Model: availableModels[0], ThinkingLevel: DefaultThinkingLevel}
	}

	return InitialModelResult{ThinkingLevel: DefaultThinkingLevel}
}

// DefaultProviderOrder is the order providers are considered for defaults.
//
// D21: upstream iterates the object keys of defaultModelPerProvider (insertion
// order); Go maps have no order, so the declaration order is kept explicitly.
var DefaultProviderOrder = []ai.ProviderId{
	"amazon-bedrock", "ant-ling", "anthropic", "openai", "azure-openai-responses", "openai-codex",
	"radius", "nvidia", "deepseek", "google", "google-vertex", "github-copilot", "openrouter",
	"vercel-ai-gateway", "xai", "groq", "cerebras", "zai", "zai-coding-cn", "mistral", "minimax",
	"minimax-cn", "moonshotai", "moonshotai-cn", "huggingface", "fireworks", "together", "baseten",
	"opencode", "opencode-go", "kimi-coding", "cloudflare-workers-ai", "cloudflare-ai-gateway",
	"qwen-token-plan", "qwen-token-plan-cn", "qwen-token-plan-individual", "xiaomi",
	"xiaomi-token-plan-cn", "xiaomi-token-plan-ams", "xiaomi-token-plan-sgp",
}

// RestoreModelResult is the session model restore result.
type RestoreModelResult struct {
	Model           *ai.Model
	FallbackMessage string
}

// RestoreModelFromSession restores a session's model, falling back to available
// models (upstream restoreModelFromSession).
func RestoreModelFromSession(
	savedProvider, savedModelID string,
	currentModel *ai.Model,
	modelRuntime ModelRuntime,
) RestoreModelResult {
	restoredModel := modelRuntime.GetModel(savedProvider, savedModelID)
	hasAuth := restoredModel != nil && modelRuntime.HasConfiguredAuth(restoredModel.Provider)
	if restoredModel != nil && hasAuth {
		return RestoreModelResult{Model: restoredModel}
	}

	reason := "model no longer exists"
	if restoredModel != nil {
		reason = "no auth configured"
	}
	if currentModel != nil {
		return RestoreModelResult{
			Model: currentModel,
			FallbackMessage: fmt.Sprintf("Could not restore model %s/%s (%s). Using %s/%s.",
				savedProvider, savedModelID, reason, currentModel.Provider, currentModel.ID),
		}
	}

	availableModels := modelRuntime.GetAvailableSnapshot()
	if len(availableModels) > 0 {
		var fallbackModel *ai.Model
		for _, providerID := range DefaultProviderOrder {
			defaultID := DefaultModelPerProvider[providerID]
			for _, candidate := range availableModels {
				if candidate.Provider == providerID && candidate.ID == defaultID {
					fallbackModel = candidate
					break
				}
			}
			if fallbackModel != nil {
				break
			}
		}
		if fallbackModel == nil {
			fallbackModel = availableModels[0]
		}
		return RestoreModelResult{
			Model: fallbackModel,
			FallbackMessage: fmt.Sprintf("Could not restore model %s/%s (%s). Using %s/%s.",
				savedProvider, savedModelID, reason, fallbackModel.Provider, fallbackModel.ID),
		}
	}
	return RestoreModelResult{}
}

// ModelRuntime is the model-runtime surface the resolver needs (the Go subset
// of upstream ModelRuntime).
type ModelRuntime interface {
	GetModels(providerID string) []*ai.Model
	GetModel(providerID, modelID string) *ai.Model
	GetAvailable(providerID string, ctx context.Context) ([]*ai.Model, error)
	GetAvailableSnapshot() []*ai.Model
	HasConfiguredAuth(providerID string) bool
}

// ModelsRuntime adapts an ai.Models registry to the resolver's ModelRuntime.
//
// D21: upstream ModelRuntime keeps a pushed snapshot of available models and
// configured providers; this adapter queries the registry on demand and caches
// the most recent successful availability listing.
type ModelsRuntime struct {
	Models *ai.Models
	ctx    context.Context
	// cache is the last full availability listing.
	cache []*ai.Model
}

// NewModelsRuntime wraps an ai.Models registry.
func NewModelsRuntime(models *ai.Models) *ModelsRuntime {
	return &ModelsRuntime{Models: models, ctx: context.Background()}
}

// GetModels lists registered models, optionally scoped to a provider.
func (r *ModelsRuntime) GetModels(providerID string) []*ai.Model {
	return r.Models.GetModels(providerID)
}

// GetModel looks one model up.
func (r *ModelsRuntime) GetModel(providerID, modelID string) *ai.Model {
	return r.Models.GetModel(providerID, modelID)
}

// GetAvailable lists models with usable auth.
func (r *ModelsRuntime) GetAvailable(providerID string, ctx context.Context) ([]*ai.Model, error) {
	if ctx == nil {
		ctx = r.ctx
	}
	available, err := r.Models.GetAvailable(providerID, ctx)
	if err != nil {
		return nil, err
	}
	if providerID == "" {
		r.cache = available
	}
	return available, nil
}

// GetAvailableSnapshot is the last availability listing.
func (r *ModelsRuntime) GetAvailableSnapshot() []*ai.Model {
	if r.cache != nil {
		return r.cache
	}
	available, err := r.Models.GetAvailable("", r.ctx)
	if err != nil {
		return nil
	}
	r.cache = available
	return available
}

// HasConfiguredAuth reports whether a provider has usable credentials.
func (r *ModelsRuntime) HasConfiguredAuth(providerID string) bool {
	check, err := r.Models.CheckAuth(providerID, r.ctx)
	return err == nil && check != nil
}

// ResolveModelScopeWithDiagnostics resolves patterns against the runtime.
func ResolveModelScopeWithDiagnostics(patterns []string, runtime ModelRuntime, ctx context.Context) (ResolveModelScopeResult, error) {
	models, err := runtime.GetAvailable("", ctx)
	if err != nil {
		return ResolveModelScopeResult{}, err
	}
	return ResolveModelScopeFromModels(patterns, models), nil
}

// ResolveModelScope resolves patterns and returns the scoped models (warnings
// are returned as the result's diagnostics).
func ResolveModelScope(patterns []string, runtime ModelRuntime, ctx context.Context) ([]ScopedModel, error) {
	result, err := ResolveModelScopeWithDiagnostics(patterns, runtime, ctx)
	if err != nil {
		return nil, err
	}
	return result.ScopedModels, nil
}
