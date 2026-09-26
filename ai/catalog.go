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

// decodeCatalogModel decodes one generated model entry, decoding its compat
// arm explicitly from the known api (upstream keys compat by api at the type
// level).
//
// compat is captured as raw JSON in the same pass rather than left to
// Model.Compat's own decoder: *ModelCompat.UnmarshalJSON guesses the arm from
// the key names, and the api decides it here, so that guess was always thrown
// away — for a model with compat the object was parsed four times (the guess's
// keys map, the guess's arm, a probe just to re-read the raw compat, then the
// authoritative arm) and five parses of the entry overall. Capturing the raw
// value makes the whole entry one parse plus the one authoritative arm.
func decodeCatalogModel(api Api, data []byte) (*Model, error) {
	// The outer CompatRaw (depth 0) shadows Model.Compat (depth 1), so the
	// embedded decode never invokes ModelCompat.UnmarshalJSON.
	var payload struct {
		Model
		CompatRaw json.RawMessage `json:"compat"`
	}
	if err := jsonUnmarshalStrict(data, &payload); err != nil {
		return nil, err
	}
	compat, err := DecodeModelCompat(api, payload.CompatRaw)
	if err != nil {
		return nil, err
	}
	model := payload.Model
	model.API = api
	model.Compat = compat
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
