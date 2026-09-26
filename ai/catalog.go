package ai

import (
	"embed"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// The embedded builtin model catalog. Ground truth: upstream pi's own
// generator (packages/ai/scripts/generate-models.ts), converted by
// scripts/gen_catalog.py from the upstream clone at the pinned commit.
// Regenerate with:
//
//	cd <upstream>/packages/ai && node scripts/generate-models.ts --strict --data-only
//	python3 scripts/gen_catalog.py <upstream> > ai/models_catalog.json
//
//go:embed models_catalog.json
var catalogFS embed.FS

// catalogShape mirrors the embedded file's structure:
// {provider: {api: {modelId: model}}}.
//
// The model ids are captured as raw JSON in the same single decode that walks
// the provider and api keys. Keeping the api subtree as one json.RawMessage
// would parse those keys twice (once into the RawMessage, once into a map to
// iterate them), which on the 924 KB catalog is a second full key pass.
type catalogShape struct {
	Meta struct {
		SchemaVersion          int    `json:"schemaVersion"`
		GeneratedAt            string `json:"generatedAt"`
		UpstreamManifestSHA256 string `json:"upstreamManifestSHA256"`
	} `json:"_meta"`
	Providers map[string]map[string]map[string]json.RawMessage `json:"providers"`
}

var (
	catalogOnce   sync.Once
	catalog       catalogShape
	catalogModels map[string]map[string]*Model
	catalogErr    error
)

func loadCatalog() {
	catalogOnce.Do(func() {
		data, err := catalogFS.ReadFile("models_catalog.json")
		if err != nil {
			catalogErr = err
			return
		}
		if err := jsonUnmarshalStrict(data, &catalog); err != nil {
			catalogErr = fmt.Errorf("ai: decoding builtin catalog: %w", err)
			return
		}
		catalogModels = map[string]map[string]*Model{}
		for providerID, apis := range catalog.Providers {
			models := map[string]*Model{}
			for api, byID := range apis {
				for modelID, raw := range byID {
					model, err := decodeCatalogModel(api, raw)
					if err != nil {
						catalogErr = fmt.Errorf("ai: decoding catalog model %s/%s: %w", providerID, modelID, err)
						return
					}
					models[modelID] = model
				}
			}
			catalogModels[providerID] = models
		}
	})
}

// decodeCatalogModel decodes one generated model entry in a single pass, taking
// its compat arm at the same time: the api is known here, so the object decodes
// straight into that arm's type instead of being read out raw and parsed again
// by DecodeModelCompat. Letting Model.Compat's own decoder run would also guess
// the arm from the key names first, a result the authoritative arm overwrote.
//
// Worth 0.8 ms of a whole-catalog decode (p50 13.71 ms -> 12.73 ms, 15 runs of
// each on one machine), and one fewer parse per model — every real run decodes
// the catalog, because CreateModelRuntime builds all 39 providers.
func decodeCatalogModel(api Api, data []byte) (*Model, error) {
	switch api {
	case APIAnthropicMessages:
		return decodeCatalogEntry[AnthropicMessagesCompat](api, data, func(c *ModelCompat, arm *AnthropicMessagesCompat) { c.AnthropicMessages = arm })
	case APIOpenAICompletions:
		return decodeCatalogEntry[OpenAICompletionsCompat](api, data, func(c *ModelCompat, arm *OpenAICompletionsCompat) { c.OpenAICompletions = arm })
	case APIOpenAIResponses, APIAzureOpenAIResponses, APIOpenAICodexResponses:
		return decodeCatalogEntry[OpenAIResponsesCompat](api, data, func(c *ModelCompat, arm *OpenAIResponsesCompat) { c.OpenAIResponses = arm })
	case APIMistralConversations:
		return decodeCatalogEntry[MistralConversationsCompat](api, data, func(c *ModelCompat, arm *MistralConversationsCompat) { c.MistralConversations = arm })
	case APIBedrockConverse:
		return decodeCatalogEntry[BedrockCompat](api, data, func(c *ModelCompat, arm *BedrockCompat) { c.Bedrock = arm })
	}
	// No arm exists for this api, and DecodeModelCompat drops compat for it too,
	// so decode the entry alone. json.RawMessage absorbs whatever the compat
	// value is without error, which keeps the unknown-api case identical.
	return decodeCatalogEntry[json.RawMessage](api, data, func(*ModelCompat, *json.RawMessage) {})
}

// decodeCatalogEntry decodes one model, pulling compat into set in the same pass.
// The outer Compat field shadows Model.Compat (depth 0 over depth 1), so
// ModelCompat.UnmarshalJSON never runs.
func decodeCatalogEntry[C any](api Api, data []byte, set func(*ModelCompat, *C)) (*Model, error) {
	var payload struct {
		Model
		Compat *C `json:"compat"`
	}
	if err := jsonUnmarshalStrict(data, &payload); err != nil {
		return nil, err
	}
	model := payload.Model
	model.API = api
	if payload.Compat != nil {
		compat := &ModelCompat{}
		set(compat, payload.Compat)
		model.Compat = compat
	}
	return &model, nil
}

// GetBuiltinModel returns one generated builtin model.
func GetBuiltinModel(provider, modelID string) *Model {
	loadCatalog()
	if catalogErr != nil {
		return nil
	}
	return catalogModels[provider][modelID]
}

// GetBuiltinProviders returns the providers present in the generated catalog.
// (KnownProvider additionally includes purely dynamic providers, e.g.
// "radius", that have no static catalog entry.)
func GetBuiltinProviders() []string {
	loadCatalog()
	if catalogErr != nil {
		return nil
	}
	out := make([]string, 0, len(catalog.Providers))
	for provider := range catalog.Providers {
		out = append(out, provider)
	}
	sortStrings(out)
	return out
}

// GetBuiltinModels returns the generated models of one builtin provider.
func GetBuiltinModels(provider string) []*Model {
	loadCatalog()
	if catalogErr != nil {
		return nil
	}
	var out []*Model
	for _, model := range catalogModels[provider] {
		out = append(out, model)
	}
	return out
}

// BuiltinModelCount returns the total number of models in the embedded
// catalog (test/monitoring convenience).
func BuiltinModelCount() int {
	loadCatalog()
	if catalogErr != nil {
		return 0
	}
	n := 0
	for _, models := range catalogModels {
		n += len(models)
	}
	return n
}

// GetBuiltinModelDataGeneratedAt returns the generation timestamp shared by
// all builtin provider catalogs (port of getBuiltinModelDataGeneratedAt).
func GetBuiltinModelDataGeneratedAt() *time.Time {
	loadCatalog()
	if catalogErr != nil || catalog.Meta.GeneratedAt == "" {
		return nil
	}
	ts, err := time.Parse(time.RFC3339Nano, catalog.Meta.GeneratedAt)
	if err != nil {
		return nil
	}
	return &ts
}

// CatalogLoadError exposes decode failures of the embedded catalog.
func CatalogLoadError() error {
	loadCatalog()
	return catalogErr
}
