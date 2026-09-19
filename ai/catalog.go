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
type catalogShape struct {
	Meta struct {
		SchemaVersion          int    `json:"schemaVersion"`
		GeneratedAt            string `json:"generatedAt"`
		UpstreamManifestSHA256 string `json:"upstreamManifestSHA256"`
	} `json:"_meta"`
	Providers map[string]map[string]json.RawMessage `json:"providers"`
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
			for api, entries := range apis {
				var byID map[string]json.RawMessage
				if err := jsonUnmarshalStrict(entries, &byID); err != nil {
					catalogErr = fmt.Errorf("ai: decoding catalog for provider %q api %q: %w", providerID, api, err)
					return
				}
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
func decodeCatalogModel(api Api, data []byte) (*Model, error) {
	var model Model
	if err := jsonUnmarshalStrict(data, &model); err != nil {
		return nil, err
	}
	var probe struct {
		Compat json.RawMessage `json:"compat"`
	}
	_ = jsonUnmarshalStrict(data, &probe)
	compat, err := DecodeModelCompat(api, probe.Compat)
	if err != nil {
		return nil, err
	}
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
