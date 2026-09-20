package coding

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/dat267/gpi/ai"
)

// Port of core/model-config.ts: an immutable, credential-blind models.json
// snapshot, plus utils/json.ts (stripJsonComments).
//
// D34: upstream validates with typebox and reports typebox's message texts.
// The Go port reproduces the same schema (fields, types, required properties,
// minLength, maxItems and literal unions) and upstream's error *paths*, but the
// message text is the Go port's own phrasing ("Expected string" and similar)
// rather than typebox's exact wording.

var (
	jsonStringOrCommentPattern = regexp.MustCompile(`"(?:\\.|[^"\\])*"|//[^\n]*`)
	jsonStringOrTrailingComma  = regexp.MustCompile(`"(?:\\.|[^"\\])*"|,(\s*[}\]])`)
)

// StripJSONComments removes `//` line comments and trailing commas from JSON,
// leaving string literals untouched (port of stripJsonComments).
func StripJSONComments(input string) string {
	withoutComments := jsonStringOrCommentPattern.ReplaceAllStringFunc(input, func(match string) string {
		if strings.HasPrefix(match, `"`) {
			return match
		}
		return ""
	})
	return jsonStringOrTrailingComma.ReplaceAllStringFunc(withoutComments, func(match string) string {
		if strings.HasPrefix(match, `"`) {
			return match
		}
		// match is ",<whitespace><closing brace>"; keep the tail after the comma.
		commaIndex := strings.Index(match, ",")
		if commaIndex < 0 {
			return match
		}
		return match[commaIndex+1:]
	})
}

// ModelsJSONCostRates are the per-million-token rates.
type ModelsJSONCostRates struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cacheRead"`
	CacheWrite float64 `json:"cacheWrite"`
}

// ModelsJSONCostTier is a volume pricing tier.
type ModelsJSONCostTier struct {
	InputTokensAbove float64 `json:"inputTokensAbove"`
	ModelsJSONCostRates
}

// ModelsJSONCost is a model's pricing, optionally tiered.
type ModelsJSONCost struct {
	ModelsJSONCostRates
	Tiers []ModelsJSONCostTier `json:"tiers,omitempty"`
}

// ModelsJSONCostOverride is a partial cost override.
type ModelsJSONCostOverride struct {
	Input      *float64             `json:"input,omitempty"`
	Output     *float64             `json:"output,omitempty"`
	CacheRead  *float64             `json:"cacheRead,omitempty"`
	CacheWrite *float64             `json:"cacheWrite,omitempty"`
	Tiers      []ModelsJSONCostTier `json:"tiers,omitempty"`
}

// ModelsJSONChatTemplateVariable is a `{$var, omitWhenOff}` chat template kwarg.
type ModelsJSONChatTemplateVariable struct {
	Var         string `json:"$var"`
	OmitWhenOff *bool  `json:"omitWhenOff,omitempty"`
}

// ModelsJSONChatTemplateKwarg is a scalar or a variable reference.
type ModelsJSONChatTemplateKwarg struct {
	// Scalar holds string, float64, bool or nil for the scalar form.
	Scalar   any
	Variable *ModelsJSONChatTemplateVariable
}

// UnmarshalJSON decodes either the scalar or the variable form.
func (k *ModelsJSONChatTemplateKwarg) UnmarshalJSON(data []byte) error {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err == nil {
		if _, isObject := probe["$var"]; isObject {
			var variable ModelsJSONChatTemplateVariable
			if err := json.Unmarshal(data, &variable); err != nil {
				return err
			}
			k.Variable = &variable
			return nil
		}
		// An object without $var is not a valid scalar either; keep it opaque.
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		k.Scalar = value
		return nil
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	k.Scalar = value
	return nil
}

// MarshalJSON re-encodes the kwarg.
func (k ModelsJSONChatTemplateKwarg) MarshalJSON() ([]byte, error) {
	if k.Variable != nil {
		return ai.MarshalJSON(k.Variable)
	}
	return ai.MarshalJSON(k.Scalar)
}

// ModelsJSONPercentileCutoffs is OpenRouter's throughput/latency percentile map.
type ModelsJSONPercentileCutoffs struct {
	P50 *float64 `json:"p50,omitempty"`
	P75 *float64 `json:"p75,omitempty"`
	P90 *float64 `json:"p90,omitempty"`
	P99 *float64 `json:"p99,omitempty"`
}

// ModelsJSONOpenRouterRouting is the OpenRouter routing policy.
type ModelsJSONOpenRouterRouting struct {
	AllowFallbacks         *bool    `json:"allow_fallbacks,omitempty"`
	RequireParameters      *bool    `json:"require_parameters,omitempty"`
	DataCollection         *string  `json:"data_collection,omitempty"`
	Zdr                    *bool    `json:"zdr,omitempty"`
	EnforceDistillableText *bool    `json:"enforce_distillable_text,omitempty"`
	Order                  []string `json:"order,omitempty"`
	Only                   []string `json:"only,omitempty"`
	Ignore                 []string `json:"ignore,omitempty"`
	Quantizations          []string `json:"quantizations,omitempty"`
	Sort                   any      `json:"sort,omitempty"`
	MaxPrice               any      `json:"max_price,omitempty"`
	PreferredMinThroughput any      `json:"preferred_min_throughput,omitempty"`
	PreferredMaxLatency    any      `json:"preferred_max_latency,omitempty"`
}

// ModelsJSONVercelGatewayRouting is the Vercel AI Gateway routing policy.
type ModelsJSONVercelGatewayRouting struct {
	Only  []string `json:"only,omitempty"`
	Order []string `json:"order,omitempty"`
}

// ModelsJSONFallbackModel is an Anthropic fallback model entry.
type ModelsJSONFallbackModel struct {
	Provider string         `json:"provider"`
	Model    string         `json:"model"`
	Cost     ModelsJSONCost `json:"cost"`
}

// ModelsJSONCompat is the union of the three provider compat schemas.
type ModelsJSONCompat struct {
	// OpenAI Completions.
	SupportsStore                               *bool                                  `json:"supportsStore,omitempty"`
	SupportsDeveloperRole                       *bool                                  `json:"supportsDeveloperRole,omitempty"`
	SupportsReasoningEffort                     *bool                                  `json:"supportsReasoningEffort,omitempty"`
	SupportsUsageInStreaming                    *bool                                  `json:"supportsUsageInStreaming,omitempty"`
	SupportsFinishReason                        *bool                                  `json:"supportsFinishReason,omitempty"`
	MaxTokensField                              *string                                `json:"maxTokensField,omitempty"`
	RequiresToolResultName                      *bool                                  `json:"requiresToolResultName,omitempty"`
	RequiresAssistantAfterToolResult            *bool                                  `json:"requiresAssistantAfterToolResult,omitempty"`
	RequiresThinkingAsText                      *bool                                  `json:"requiresThinkingAsText,omitempty"`
	RequiresReasoningContentOnAssistantMessages *bool                                  `json:"requiresReasoningContentOnAssistantMessages,omitempty"`
	ThinkingFormat                              *string                                `json:"thinkingFormat,omitempty"`
	ChatTemplateKwargs                          map[string]ModelsJSONChatTemplateKwarg `json:"chatTemplateKwargs,omitempty"`
	ChatTemplateArgs                            map[string]ModelsJSONChatTemplateKwarg `json:"chatTemplateArgs,omitempty"`
	CacheControlFormat                          *string                                `json:"cacheControlFormat,omitempty"`
	OpenRouterRouting                           *ModelsJSONOpenRouterRouting           `json:"openRouterRouting,omitempty"`
	VercelGatewayRouting                        *ModelsJSONVercelGatewayRouting        `json:"vercelGatewayRouting,omitempty"`
	SupportsOpenAIGrammarTools                  *bool                                  `json:"supportsOpenAIGrammarTools,omitempty"`
	SupportsStrictMode                          *bool                                  `json:"supportsStrictMode,omitempty"`
	SendSessionAffinityHeaders                  *bool                                  `json:"sendSessionAffinityHeaders,omitempty"`
	SessionAffinityFormat                       *string                                `json:"sessionAffinityFormat,omitempty"`
	SupportsLongCacheRetention                  *bool                                  `json:"supportsLongCacheRetention,omitempty"`
	VLLMPriority                                *float64                               `json:"vllmPriority,omitempty"`
	// OpenAI Responses.
	SupportsMaxOutputTokens *bool `json:"supportsMaxOutputTokens,omitempty"`
	// Anthropic Messages.
	SupportsEagerToolInputStreaming *bool                     `json:"supportsEagerToolInputStreaming,omitempty"`
	SupportsCacheControlOnTools     *bool                     `json:"supportsCacheControlOnTools,omitempty"`
	SupportsTemperature             *bool                     `json:"supportsTemperature,omitempty"`
	ForceAdaptiveThinking           *bool                     `json:"forceAdaptiveThinking,omitempty"`
	AllowEmptySignature             *bool                     `json:"allowEmptySignature,omitempty"`
	SupportsStrictTools             *bool                     `json:"supportsStrictTools,omitempty"`
	SupportsMidConvoEffort          *bool                     `json:"supportsMidConvoEffort,omitempty"`
	AllowedFallbackModels           []ModelsJSONFallbackModel `json:"allowedFallbackModels,omitempty"`
}

// ModelsJSONModel is one model definition in models.json.
type ModelsJSONModel struct {
	ID               string              `json:"id"`
	Name             string              `json:"name,omitempty"`
	API              string              `json:"api,omitempty"`
	BaseURL          string              `json:"baseUrl,omitempty"`
	Reasoning        *bool               `json:"reasoning,omitempty"`
	ThinkingLevelMap ai.ThinkingLevelMap `json:"thinkingLevelMap,omitempty"`
	Input            []string            `json:"input,omitempty"`
	Cost             *ModelsJSONCost     `json:"cost,omitempty"`
	ContextWindow    *float64            `json:"contextWindow,omitempty"`
	MaxTokens        *float64            `json:"maxTokens,omitempty"`
	SamplingParams   map[string]any      `json:"samplingParams,omitempty"`
	Headers          map[string]string   `json:"headers,omitempty"`
	Compat           *ModelsJSONCompat   `json:"compat,omitempty"`
}

// ModelsJSONModelOverride is a partial override of a built-in model.
type ModelsJSONModelOverride struct {
	Name             string                  `json:"name,omitempty"`
	Reasoning        *bool                   `json:"reasoning,omitempty"`
	ThinkingLevelMap ai.ThinkingLevelMap     `json:"thinkingLevelMap,omitempty"`
	Input            []string                `json:"input,omitempty"`
	Cost             *ModelsJSONCostOverride `json:"cost,omitempty"`
	ContextWindow    *float64                `json:"contextWindow,omitempty"`
	MaxTokens        *float64                `json:"maxTokens,omitempty"`
	SamplingParams   map[string]any          `json:"samplingParams,omitempty"`
	Headers          map[string]string       `json:"headers,omitempty"`
	Compat           *ModelsJSONCompat       `json:"compat,omitempty"`
}

// ModelsJSONProvider is one provider entry in models.json.
type ModelsJSONProvider struct {
	Name           string                             `json:"name,omitempty"`
	BaseURL        string                             `json:"baseUrl,omitempty"`
	APIKey         string                             `json:"apiKey,omitempty"`
	API            string                             `json:"api,omitempty"`
	OAuth          string                             `json:"oauth,omitempty"`
	Headers        map[string]string                  `json:"headers,omitempty"`
	Compat         *ModelsJSONCompat                  `json:"compat,omitempty"`
	AuthHeader     *bool                              `json:"authHeader,omitempty"`
	Models         []ModelsJSONModel                  `json:"models,omitempty"`
	ModelOverrides map[string]ModelsJSONModelOverride `json:"modelOverrides,omitempty"`
}

// ModelsJSON is the whole models.json document.
type ModelsJSON struct {
	Providers map[string]ModelsJSONProvider `json:"providers"`
}

// ModelConfig is one immutable load of models.json.
type ModelConfig struct {
	providers map[string]ModelsJSONProvider
	order     []string
	err       string
}

// LoadModelConfig loads models.json. An empty path yields an empty config; a
// missing file yields an empty config with no error.
func LoadModelConfig(modelsJSONPath string) *ModelConfig {
	if modelsJSONPath == "" {
		return &ModelConfig{providers: map[string]ModelsJSONProvider{}}
	}
	path := NormalizePath(modelsJSONPath, PathInputOptions{})
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &ModelConfig{providers: map[string]ModelsJSONProvider{}}
		}
		return &ModelConfig{
			providers: map[string]ModelsJSONProvider{},
			err:       fmt.Sprintf("Failed to load models.json: %v\n\nFile: %s", err, path),
		}
	}

	var parsed any
	if err := json.Unmarshal([]byte(StripJSONComments(StripBom(string(raw)))), &parsed); err != nil {
		return &ModelConfig{
			providers: map[string]ModelsJSONProvider{},
			err:       fmt.Sprintf("Failed to parse models.json: %v\n\nFile: %s", err, path),
		}
	}

	if validationErrors := validateModelsConfig(parsed); len(validationErrors) > 0 {
		lines := make([]string, 0, len(validationErrors))
		for _, validationError := range validationErrors {
			lines = append(lines, fmt.Sprintf("  - %s: %s", validationError.path, validationError.message))
		}
		message := strings.Join(lines, "\n")
		if message == "" {
			message = "Unknown schema error"
		}
		return &ModelConfig{
			providers: map[string]ModelsJSONProvider{},
			err:       fmt.Sprintf("Invalid models.json schema:\n%s\n\nFile: %s", message, path),
		}
	}

	// Decode the validated document into typed providers, preserving the file
	// order for GetProviderIDs.
	object := parsed.(map[string]any)
	providersObject, _ := object["providers"].(map[string]any)
	order := make([]string, 0, len(providersObject))
	for _, name := range topLevelJSONOrder(StripJSONComments(StripBom(string(raw))), "providers") {
		if _, ok := providersObject[name]; ok {
			order = append(order, name)
		}
	}
	if len(order) != len(providersObject) {
		// Fall back to a sorted order when the raw scan is incomplete.
		order = order[:0]
		for name := range providersObject {
			order = append(order, name)
		}
		sort.Strings(order)
	}

	providers := make(map[string]ModelsJSONProvider, len(providersObject))
	for providerID, rawProvider := range providersObject {
		encoded, err := ai.MarshalJSON(rawProvider)
		if err != nil {
			continue
		}
		var provider ModelsJSONProvider
		if err := json.Unmarshal(encoded, &provider); err != nil {
			continue
		}
		providers[providerID] = provider
	}
	return &ModelConfig{providers: providers, order: order}
}

// topLevelJSONOrder returns the key order of the nested object stored under a
// top-level key (used to keep the file's provider order, which Go maps lose).
func topLevelJSONOrder(content, key string) []string {
	decoder := json.NewDecoder(strings.NewReader(content))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil
	}
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return nil
		}
		name, ok := nameToken.(string)
		if !ok {
			return nil
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil
		}
		if name != key {
			continue
		}
		nested := json.NewDecoder(strings.NewReader(string(value)))
		nestedToken, err := nested.Token()
		if err != nil || nestedToken != json.Delim('{') {
			return nil
		}
		var order []string
		for nested.More() {
			nestedName, err := nested.Token()
			if err != nil {
				return order
			}
			if name, ok := nestedName.(string); ok {
				order = append(order, name)
			}
			var nestedValue json.RawMessage
			if err := nested.Decode(&nestedValue); err != nil {
				return order
			}
		}
		return order
	}
	return nil
}

// GetProvider returns the provider config, or nil when unknown. The returned
// value is a copy: the config is immutable.
func (c *ModelConfig) GetProvider(providerID string) *ModelsJSONProvider {
	provider, ok := c.providers[providerID]
	if !ok {
		return nil
	}
	return &provider
}

// GetProviderIDs returns the provider ids in file order.
func (c *ModelConfig) GetProviderIDs() []string {
	return append([]string{}, c.order...)
}

// GetError returns the load/validation error, if any.
func (c *ModelConfig) GetError() string {
	return c.err
}

// validationError is one schema failure.
type validationError struct {
	path    string
	message string
}

// validateModelsConfig validates the parsed document against the schema and
// returns every failure with an upstream-style path.
func validateModelsConfig(parsed any) []validationError {
	root, ok := parsed.(map[string]any)
	if !ok {
		return []validationError{{path: "root", message: expectedMessage("object")}}
	}
	providers, ok := root["providers"]
	if !ok {
		return []validationError{{path: "providers", message: "Expected required property"}}
	}
	providersObject, ok := providers.(map[string]any)
	if !ok {
		return []validationError{{path: "providers", message: expectedMessage("object")}}
	}

	var errors []validationError
	names := make([]string, 0, len(providersObject))
	for name := range providersObject {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, providerID := range names {
		validateProvider(providersObject[providerID], "providers."+providerID, &errors)
	}
	return errors
}

func validateProvider(value any, path string, errors *[]validationError) {
	provider, ok := value.(map[string]any)
	if !ok {
		*errors = append(*errors, validationError{path: path, message: expectedMessage("object")})
		return
	}
	validateOptionalString(provider, "name", path, true, errors)
	validateOptionalString(provider, "baseUrl", path, true, errors)
	validateOptionalString(provider, "apiKey", path, true, errors)
	validateOptionalString(provider, "api", path, true, errors)
	validateLiteral(provider, "oauth", []string{"radius"}, path, errors)
	validateStringRecord(provider, "headers", path, errors)
	if compat, ok := provider["compat"]; ok {
		validateCompat(compat, path+".compat", errors)
	}
	validateOptionalBool(provider, "authHeader", path, errors)
	if models, ok := provider["models"]; ok {
		list, ok := models.([]any)
		if !ok {
			*errors = append(*errors, validationError{path: path + ".models", message: expectedMessage("array")})
		} else {
			for index, model := range list {
				validateModelDefinition(model, fmt.Sprintf("%s.models.%d", path, index), errors)
			}
		}
	}
	if overrides, ok := provider["modelOverrides"]; ok {
		overrideObject, ok := overrides.(map[string]any)
		if !ok {
			*errors = append(*errors, validationError{path: path + ".modelOverrides", message: expectedMessage("object")})
		} else {
			names := make([]string, 0, len(overrideObject))
			for name := range overrideObject {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				validateModelOverride(overrideObject[name], fmt.Sprintf("%s.modelOverrides.%s", path, name), errors)
			}
		}
	}
}

func validateModelDefinition(value any, path string, errors *[]validationError) {
	model, ok := value.(map[string]any)
	if !ok {
		*errors = append(*errors, validationError{path: path, message: expectedMessage("object")})
		return
	}
	if id, ok := model["id"]; !ok {
		*errors = append(*errors, validationError{path: path + ".id", message: "Expected required property"})
	} else if text, ok := id.(string); !ok {
		*errors = append(*errors, validationError{path: path + ".id", message: expectedMessage("string")})
	} else if text == "" {
		*errors = append(*errors, validationError{path: path + ".id", message: minLengthMessage(1)})
	}
	validateOptionalString(model, "name", path, true, errors)
	validateOptionalString(model, "api", path, true, errors)
	validateOptionalString(model, "baseUrl", path, true, errors)
	validateOptionalBool(model, "reasoning", path, errors)
	validateThinkingLevelMap(model, path, errors)
	validateInputList(model, path, errors)
	if cost, ok := model["cost"]; ok {
		validateCost(cost, path+".cost", true, errors)
	}
	validateOptionalNumber(model, "contextWindow", path, errors)
	validateOptionalNumber(model, "maxTokens", path, errors)
	validateUnknownRecord(model, "samplingParams", path, errors)
	validateStringRecord(model, "headers", path, errors)
	if compat, ok := model["compat"]; ok {
		validateCompat(compat, path+".compat", errors)
	}
}

func validateModelOverride(value any, path string, errors *[]validationError) {
	model, ok := value.(map[string]any)
	if !ok {
		*errors = append(*errors, validationError{path: path, message: expectedMessage("object")})
		return
	}
	validateOptionalString(model, "name", path, true, errors)
	validateOptionalBool(model, "reasoning", path, errors)
	validateThinkingLevelMap(model, path, errors)
	validateInputList(model, path, errors)
	if cost, ok := model["cost"]; ok {
		validateCost(cost, path+".cost", false, errors)
	}
	validateOptionalNumber(model, "contextWindow", path, errors)
	validateOptionalNumber(model, "maxTokens", path, errors)
	validateUnknownRecord(model, "samplingParams", path, errors)
	validateStringRecord(model, "headers", path, errors)
	if compat, ok := model["compat"]; ok {
		validateCompat(compat, path+".compat", errors)
	}
}

func validateThinkingLevelMap(record map[string]any, path string, errors *[]validationError) {
	value, ok := record["thinkingLevelMap"]
	if !ok {
		return
	}
	levelObject, ok := value.(map[string]any)
	if !ok {
		*errors = append(*errors, validationError{path: path + ".thinkingLevelMap", message: expectedMessage("object")})
		return
	}
	for _, level := range []string{"off", "minimal", "low", "medium", "high", "xhigh", "max"} {
		entry, ok := levelObject[level]
		if !ok {
			continue
		}
		if entry == nil {
			continue
		}
		if _, ok := entry.(string); !ok {
			*errors = append(*errors, validationError{
				path:    fmt.Sprintf("%s.thinkingLevelMap.%s", path, level),
				message: "Expected union value",
			})
		}
	}
}

func validateInputList(record map[string]any, path string, errors *[]validationError) {
	value, ok := record["input"]
	if !ok {
		return
	}
	list, ok := value.([]any)
	if !ok {
		*errors = append(*errors, validationError{path: path + ".input", message: expectedMessage("array")})
		return
	}
	for index, entry := range list {
		text, ok := entry.(string)
		if !ok || (text != "text" && text != "image") {
			*errors = append(*errors, validationError{
				path:    fmt.Sprintf("%s.input.%d", path, index),
				message: "Expected union value",
			})
		}
	}
}

func validateCost(value any, path string, ratesRequired bool, errors *[]validationError) {
	cost, ok := value.(map[string]any)
	if !ok {
		*errors = append(*errors, validationError{path: path, message: expectedMessage("object")})
		return
	}
	for _, field := range []string{"input", "output", "cacheRead", "cacheWrite"} {
		entry, ok := cost[field]
		if !ok {
			if ratesRequired {
				*errors = append(*errors, validationError{path: path + "." + field, message: "Expected required property"})
			}
			continue
		}
		if _, ok := entry.(float64); !ok {
			*errors = append(*errors, validationError{path: path + "." + field, message: expectedMessage("number")})
		}
	}
	if tiers, ok := cost["tiers"]; ok {
		list, ok := tiers.([]any)
		if !ok {
			*errors = append(*errors, validationError{path: path + ".tiers", message: expectedMessage("array")})
			return
		}
		for index, tier := range list {
			tierPath := fmt.Sprintf("%s.tiers.%d", path, index)
			tierObject, ok := tier.(map[string]any)
			if !ok {
				*errors = append(*errors, validationError{path: tierPath, message: expectedMessage("object")})
				continue
			}
			if _, ok := tierObject["inputTokensAbove"]; !ok {
				*errors = append(*errors, validationError{path: tierPath + ".inputTokensAbove", message: "Expected required property"})
			} else if _, ok := tierObject["inputTokensAbove"].(float64); !ok {
				*errors = append(*errors, validationError{path: tierPath + ".inputTokensAbove", message: expectedMessage("number")})
			}
			for _, field := range []string{"input", "output", "cacheRead", "cacheWrite"} {
				entry, ok := tierObject[field]
				if !ok {
					*errors = append(*errors, validationError{path: tierPath + "." + field, message: "Expected required property"})
					continue
				}
				if _, ok := entry.(float64); !ok {
					*errors = append(*errors, validationError{path: tierPath + "." + field, message: expectedMessage("number")})
				}
			}
		}
	}
}

// compatFieldKind describes how a compat field is validated.
type compatFieldKind int

const (
	compatBool compatFieldKind = iota
	compatNumber
	compatLiteral
	compatChatTemplateRecord
	compatOpenRouterRouting
	compatVercelGatewayRouting
	compatFallbackModels
)

type compatField struct {
	name     string
	kind     compatFieldKind
	literals []string
}

// compatFields is the union of the three provider compat schemas.
var compatFields = []compatField{
	{name: "supportsStore", kind: compatBool},
	{name: "supportsDeveloperRole", kind: compatBool},
	{name: "supportsReasoningEffort", kind: compatBool},
	{name: "supportsUsageInStreaming", kind: compatBool},
	{name: "supportsFinishReason", kind: compatBool},
	{name: "maxTokensField", kind: compatLiteral, literals: []string{"max_completion_tokens", "max_tokens"}},
	{name: "requiresToolResultName", kind: compatBool},
	{name: "requiresAssistantAfterToolResult", kind: compatBool},
	{name: "requiresThinkingAsText", kind: compatBool},
	{name: "requiresReasoningContentOnAssistantMessages", kind: compatBool},
	{name: "thinkingFormat", kind: compatLiteral, literals: []string{
		"openai", "openrouter", "together", "baseten", "deepseek", "zai", "qwen",
		"chat-template", "qwen-chat-template", "string-thinking", "ant-ling",
	}},
	{name: "chatTemplateKwargs", kind: compatChatTemplateRecord},
	{name: "chatTemplateArgs", kind: compatChatTemplateRecord},
	{name: "cacheControlFormat", kind: compatLiteral, literals: []string{"anthropic"}},
	{name: "openRouterRouting", kind: compatOpenRouterRouting},
	{name: "vercelGatewayRouting", kind: compatVercelGatewayRouting},
	{name: "supportsOpenAIGrammarTools", kind: compatBool},
	{name: "supportsStrictMode", kind: compatBool},
	{name: "sendSessionAffinityHeaders", kind: compatBool},
	{name: "sessionAffinityFormat", kind: compatLiteral, literals: []string{"openai", "openai-nosession", "openrouter"}},
	{name: "supportsLongCacheRetention", kind: compatBool},
	{name: "vllmPriority", kind: compatNumber},
	{name: "supportsMaxOutputTokens", kind: compatBool},
	{name: "supportsEagerToolInputStreaming", kind: compatBool},
	{name: "supportsCacheControlOnTools", kind: compatBool},
	{name: "supportsTemperature", kind: compatBool},
	{name: "forceAdaptiveThinking", kind: compatBool},
	{name: "allowEmptySignature", kind: compatBool},
	{name: "supportsStrictTools", kind: compatBool},
	{name: "supportsMidConvoEffort", kind: compatBool},
	{name: "allowedFallbackModels", kind: compatFallbackModels},
}

func validateCompat(value any, path string, errors *[]validationError) {
	compat, ok := value.(map[string]any)
	if !ok {
		*errors = append(*errors, validationError{path: path, message: "Expected union value"})
		return
	}
	for _, field := range compatFields {
		entry, ok := compat[field.name]
		if !ok {
			continue
		}
		fieldPath := path + "." + field.name
		switch field.kind {
		case compatBool:
			if _, ok := entry.(bool); !ok {
				*errors = append(*errors, validationError{path: fieldPath, message: expectedMessage("boolean")})
			}
		case compatNumber:
			if _, ok := entry.(float64); !ok {
				*errors = append(*errors, validationError{path: fieldPath, message: expectedMessage("number")})
			}
		case compatLiteral:
			text, ok := entry.(string)
			if !ok || !containsString(field.literals, text) {
				*errors = append(*errors, validationError{path: fieldPath, message: "Expected union value"})
			}
		case compatChatTemplateRecord:
			record, ok := entry.(map[string]any)
			if !ok {
				*errors = append(*errors, validationError{path: fieldPath, message: expectedMessage("object")})
				continue
			}
			names := make([]string, 0, len(record))
			for name := range record {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				validateChatTemplateKwarg(record[name], fieldPath+"."+name, errors)
			}
		case compatOpenRouterRouting:
			validateOpenRouterRouting(entry, fieldPath, errors)
		case compatVercelGatewayRouting:
			validateVercelGatewayRouting(entry, fieldPath, errors)
		case compatFallbackModels:
			list, ok := entry.([]any)
			if !ok {
				*errors = append(*errors, validationError{path: fieldPath, message: expectedMessage("array")})
				continue
			}
			if len(list) > 3 {
				*errors = append(*errors, validationError{path: fieldPath, message: maxItemsMessage(3)})
			}
			for index, item := range list {
				itemPath := fmt.Sprintf("%s.%d", fieldPath, index)
				object, ok := item.(map[string]any)
				if !ok {
					*errors = append(*errors, validationError{path: itemPath, message: expectedMessage("object")})
					continue
				}
				for _, required := range []string{"provider", "model"} {
					entry, ok := object[required]
					if !ok {
						*errors = append(*errors, validationError{path: itemPath + "." + required, message: "Expected required property"})
						continue
					}
					text, ok := entry.(string)
					if !ok {
						*errors = append(*errors, validationError{path: itemPath + "." + required, message: expectedMessage("string")})
					} else if text == "" {
						*errors = append(*errors, validationError{path: itemPath + "." + required, message: minLengthMessage(1)})
					}
				}
				cost, ok := object["cost"]
				if !ok {
					*errors = append(*errors, validationError{path: itemPath + ".cost", message: "Expected required property"})
				} else {
					validateCost(cost, itemPath+".cost", true, errors)
				}
			}
		}
	}
}

func validateChatTemplateKwarg(value any, path string, errors *[]validationError) {
	switch typed := value.(type) {
	case string, float64, bool:
		return
	case nil:
		return
	case map[string]any:
		varRaw, ok := typed["$var"]
		if !ok {
			*errors = append(*errors, validationError{path: path, message: "Expected union value"})
			return
		}
		text, ok := varRaw.(string)
		if !ok || (text != "thinking.enabled" && text != "thinking.effort") {
			*errors = append(*errors, validationError{path: path + ".$var", message: "Expected union value"})
		}
		if omit, ok := typed["omitWhenOff"]; ok {
			if _, ok := omit.(bool); !ok {
				*errors = append(*errors, validationError{path: path + ".omitWhenOff", message: expectedMessage("boolean")})
			}
		}
	default:
		*errors = append(*errors, validationError{path: path, message: "Expected union value"})
	}
}

func validateOpenRouterRouting(value any, path string, errors *[]validationError) {
	routing, ok := value.(map[string]any)
	if !ok {
		*errors = append(*errors, validationError{path: path, message: expectedMessage("object")})
		return
	}
	for _, name := range []string{"allow_fallbacks", "require_parameters", "zdr", "enforce_distillable_text"} {
		if entry, ok := routing[name]; ok {
			if _, ok := entry.(bool); !ok {
				*errors = append(*errors, validationError{path: path + "." + name, message: expectedMessage("boolean")})
			}
		}
	}
	if entry, ok := routing["data_collection"]; ok {
		text, ok := entry.(string)
		if !ok || (text != "deny" && text != "allow") {
			*errors = append(*errors, validationError{path: path + ".data_collection", message: "Expected union value"})
		}
	}
	for _, name := range []string{"order", "only", "ignore", "quantizations"} {
		if entry, ok := routing[name]; ok {
			list, ok := entry.([]any)
			if !ok {
				*errors = append(*errors, validationError{path: path + "." + name, message: expectedMessage("array")})
				continue
			}
			for index, item := range list {
				if _, ok := item.(string); !ok {
					*errors = append(*errors, validationError{
						path:    fmt.Sprintf("%s.%s.%d", path, name, index),
						message: expectedMessage("string"),
					})
				}
			}
		}
	}
	if entry, ok := routing["sort"]; ok {
		switch typed := entry.(type) {
		case string:
		case map[string]any:
			if by, ok := typed["by"]; ok {
				if _, ok := by.(string); !ok {
					*errors = append(*errors, validationError{path: path + ".sort.by", message: expectedMessage("string")})
				}
			}
			if partition, ok := typed["partition"]; ok && partition != nil {
				if _, ok := partition.(string); !ok {
					*errors = append(*errors, validationError{path: path + ".sort.partition", message: "Expected union value"})
				}
			}
		default:
			*errors = append(*errors, validationError{path: path + ".sort", message: "Expected union value"})
		}
	}
	if entry, ok := routing["max_price"]; ok {
		price, ok := entry.(map[string]any)
		if !ok {
			*errors = append(*errors, validationError{path: path + ".max_price", message: expectedMessage("object")})
		} else {
			for _, name := range []string{"prompt", "completion", "image", "audio", "request"} {
				if value, ok := price[name]; ok {
					if _, isNumber := value.(float64); isNumber {
						continue
					}
					if _, isString := value.(string); isString {
						continue
					}
					*errors = append(*errors, validationError{path: path + ".max_price." + name, message: "Expected union value"})
				}
			}
		}
	}
	for _, name := range []string{"preferred_min_throughput", "preferred_max_latency"} {
		entry, ok := routing[name]
		if !ok {
			continue
		}
		if _, isNumber := entry.(float64); isNumber {
			continue
		}
		cutoffs, ok := entry.(map[string]any)
		if !ok {
			*errors = append(*errors, validationError{path: path + "." + name, message: "Expected union value"})
			continue
		}
		for _, percentile := range []string{"p50", "p75", "p90", "p99"} {
			if value, ok := cutoffs[percentile]; ok {
				if _, ok := value.(float64); !ok {
					*errors = append(*errors, validationError{
						path:    fmt.Sprintf("%s.%s.%s", path, name, percentile),
						message: expectedMessage("number"),
					})
				}
			}
		}
	}
}

func validateVercelGatewayRouting(value any, path string, errors *[]validationError) {
	routing, ok := value.(map[string]any)
	if !ok {
		*errors = append(*errors, validationError{path: path, message: expectedMessage("object")})
		return
	}
	for _, name := range []string{"only", "order"} {
		entry, ok := routing[name]
		if !ok {
			continue
		}
		list, ok := entry.([]any)
		if !ok {
			*errors = append(*errors, validationError{path: path + "." + name, message: expectedMessage("array")})
			continue
		}
		for index, item := range list {
			if _, ok := item.(string); !ok {
				*errors = append(*errors, validationError{
					path:    fmt.Sprintf("%s.%s.%d", path, name, index),
					message: expectedMessage("string"),
				})
			}
		}
	}
}

func validateOptionalString(record map[string]any, name, path string, minLength bool, errors *[]validationError) {
	entry, ok := record[name]
	if !ok {
		return
	}
	text, ok := entry.(string)
	if !ok {
		*errors = append(*errors, validationError{path: path + "." + name, message: expectedMessage("string")})
		return
	}
	if minLength && text == "" {
		*errors = append(*errors, validationError{path: path + "." + name, message: minLengthMessage(1)})
	}
}

func validateOptionalBool(record map[string]any, name, path string, errors *[]validationError) {
	entry, ok := record[name]
	if !ok {
		return
	}
	if _, ok := entry.(bool); !ok {
		*errors = append(*errors, validationError{path: path + "." + name, message: expectedMessage("boolean")})
	}
}

func validateOptionalNumber(record map[string]any, name, path string, errors *[]validationError) {
	entry, ok := record[name]
	if !ok {
		return
	}
	if _, ok := entry.(float64); !ok {
		*errors = append(*errors, validationError{path: path + "." + name, message: expectedMessage("number")})
	}
}

func validateLiteral(record map[string]any, name string, literals []string, path string, errors *[]validationError) {
	entry, ok := record[name]
	if !ok {
		return
	}
	text, ok := entry.(string)
	if !ok || !containsString(literals, text) {
		*errors = append(*errors, validationError{path: path + "." + name, message: "Expected union value"})
	}
}

func validateStringRecord(record map[string]any, name, path string, errors *[]validationError) {
	entry, ok := record[name]
	if !ok {
		return
	}
	object, ok := entry.(map[string]any)
	if !ok {
		*errors = append(*errors, validationError{path: path + "." + name, message: expectedMessage("object")})
		return
	}
	names := make([]string, 0, len(object))
	for key := range object {
		names = append(names, key)
	}
	sort.Strings(names)
	for _, key := range names {
		if _, ok := object[key].(string); !ok {
			*errors = append(*errors, validationError{
				path:    fmt.Sprintf("%s.%s.%s", path, name, key),
				message: expectedMessage("string"),
			})
		}
	}
}

func validateUnknownRecord(record map[string]any, name, path string, errors *[]validationError) {
	entry, ok := record[name]
	if !ok {
		return
	}
	if _, ok := entry.(map[string]any); !ok {
		*errors = append(*errors, validationError{path: path + "." + name, message: expectedMessage("object")})
	}
}

// Paths are reported in upstream's formatValidationPath format (dots between
// segments, "root" for the document), built directly by the validator.

func expectedMessage(kind string) string {
	return "Expected " + kind
}

func minLengthMessage(length int) string {
	return fmt.Sprintf("Expected string length greater or equal to %d", length)
}

func maxItemsMessage(count int) string {
	return fmt.Sprintf("Expected array length less or equal to %d", count)
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
