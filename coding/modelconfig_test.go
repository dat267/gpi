package coding

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dat267/gpi/ai"
)

// Round 98 tests: models.json loading and validation (core/model-config.ts) and
// stripJsonComments (utils/json.ts).

func TestStripJSONComments(t *testing.T) {
	cases := map[string]string{
		`{"a":1}`:                          `{"a":1}`,
		"{\"a\":1} // trailing":            `{"a":1} `,
		"{\n// leading\n\"a\":1\n}":        "{\n\n\"a\":1\n}",
		`{"url":"https://x//y"}`:           `{"url":"https://x//y"}`,
		`{"a":"// not a comment"}`:         `{"a":"// not a comment"}`,
		`{"a":"quote \" // still string"}`: `{"a":"quote \" // still string"}`,
		`{"a":[1,2,],}`:                    `{"a":[1,2]}`,
		"{\"a\":1,\n}":                     "{\"a\":1\n}",
		`{"a":","}`:                        `{"a":","}`,
		`{"a":",}"}`:                       `{"a":",}"}`,
	}
	for input, want := range cases {
		if got := StripJSONComments(input); got != want {
			t.Errorf("StripJSONComments(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestModelConfigLoad(t *testing.T) {
	// No path yields an empty config without error.
	empty := LoadModelConfig("")
	if empty.GetError() != "" || len(empty.GetProviderIDs()) != 0 {
		t.Fatalf("config = %+v", empty)
	}
	if provider := empty.GetProvider("anthropic"); provider != nil {
		t.Fatalf("provider = %+v", provider)
	}

	// A missing file is empty, not an error.
	missing := LoadModelConfig(filepath.Join(t.TempDir(), "models.json"))
	if missing.GetError() != "" || len(missing.GetProviderIDs()) != 0 {
		t.Fatalf("config = %+v", missing)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	content := `{
		// providers can be added here
		"providers": {
			"custom": {
				"name": "Custom",
				"baseUrl": "https://custom.example.com/v1",
				"apiKey": "$CUSTOM_KEY",
				"api": "openai-completions",
				"headers": { "X-Custom": "1", },
				"compat": { "supportsStore": false, "thinkingFormat": "qwen", "maxTokensField": "max_tokens" },
				"authHeader": true,
				"models": [
					{
						"id": "custom-model",
						"name": "Custom Model",
						"reasoning": true,
						"thinkingLevelMap": { "high": "high", "off": null },
						"input": ["text", "image"],
						"cost": { "input": 1, "output": 2, "cacheRead": 0.1, "cacheWrite": 0.2, "tiers": [ { "inputTokensAbove": 1000, "input": 3, "output": 4, "cacheRead": 0.3, "cacheWrite": 0.4 } ] },
						"contextWindow": 1000,
						"maxTokens": 100,
						"samplingParams": { "temperature": 0.5, "nested": { "a": 1 } },
						"headers": { "X-Model": "2" }
					}
				],
				"modelOverrides": {
					"builtin": { "name": "Override", "cost": { "input": 9 }, "contextWindow": 42 }
				}
			},
			"radius": { "oauth": "radius" }
		}
	}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	config := LoadModelConfig(path)
	if err := config.GetError(); err != "" {
		t.Fatalf("error = %s", err)
	}
	ids := config.GetProviderIDs()
	if strings.Join(ids, ",") != "custom,radius" {
		t.Fatalf("ids = %v", ids)
	}
	provider := config.GetProvider("custom")
	if provider == nil || provider.Name != "Custom" || provider.BaseURL != "https://custom.example.com/v1" ||
		provider.APIKey != "$CUSTOM_KEY" || provider.API != "openai-completions" {
		t.Fatalf("provider = %+v", provider)
	}
	if provider.Headers["X-Custom"] != "1" || provider.AuthHeader == nil || !*provider.AuthHeader {
		t.Fatalf("provider = %+v", provider)
	}
	if provider.Compat == nil || provider.Compat.SupportsStore == nil || *provider.Compat.SupportsStore ||
		provider.Compat.ThinkingFormat == nil || *provider.Compat.ThinkingFormat != "qwen" {
		t.Fatalf("compat = %+v", provider.Compat)
	}
	if len(provider.Models) != 1 {
		t.Fatalf("models = %+v", provider.Models)
	}
	model := provider.Models[0]
	if model.ID != "custom-model" || model.Name != "Custom Model" || model.Reasoning == nil || !*model.Reasoning {
		t.Fatalf("model = %+v", model)
	}
	if len(model.Input) != 2 || model.ContextWindow == nil || *model.ContextWindow != 1000 {
		t.Fatalf("model = %+v", model)
	}
	if model.Cost == nil || model.Cost.Input != 1 || len(model.Cost.Tiers) != 1 || model.Cost.Tiers[0].InputTokensAbove != 1000 {
		t.Fatalf("cost = %+v", model.Cost)
	}
	if model.ThinkingLevelMap[ai.ThinkOff] != nil {
		t.Fatal("off must be null")
	}
	if level := model.ThinkingLevelMap[ai.ThinkHigh]; level == nil || *level != "high" {
		t.Fatalf("thinkingLevelMap = %+v", model.ThinkingLevelMap)
	}
	if model.SamplingParams["temperature"] != 0.5 {
		t.Fatalf("samplingParams = %+v", model.SamplingParams)
	}
	override := provider.ModelOverrides["builtin"]
	if override.Name != "Override" || override.Cost == nil || override.Cost.Input == nil || *override.Cost.Input != 9 ||
		override.ContextWindow == nil || *override.ContextWindow != 42 {
		t.Fatalf("override = %+v", override)
	}
	radius := config.GetProvider("radius")
	if radius == nil || radius.OAuth != "radius" {
		t.Fatalf("radius = %+v", radius)
	}

	// The returned provider is a copy: mutating it leaves the config intact.
	provider.Name = "Mutated"
	if again := config.GetProvider("custom"); again.Name != "Custom" {
		t.Fatalf("config was mutated: %+v", again)
	}
	// GetProviderIDs returns a copy.
	ids[0] = "mutated"
	if config.GetProviderIDs()[0] != "custom" {
		t.Fatal("ids were mutated")
	}
}

func TestModelConfigErrors(t *testing.T) {
	dir := t.TempDir()
	write := func(t *testing.T, content string) string {
		t.Helper()
		path := filepath.Join(dir, "models.json")
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	// Parse failures keep the upstream prefix and file line.
	config := LoadModelConfig(write(t, "{ not json"))
	if !strings.HasPrefix(config.GetError(), "Failed to parse models.json: ") ||
		!strings.Contains(config.GetError(), "File: ") {
		t.Fatalf("error = %q", config.GetError())
	}
	if len(config.GetProviderIDs()) != 0 {
		t.Fatalf("ids = %v", config.GetProviderIDs())
	}

	// A JSON array is not an object.
	config = LoadModelConfig(write(t, "[]"))
	if !strings.Contains(config.GetError(), "Invalid models.json schema:\n  - root: Expected object") {
		t.Fatalf("error = %q", config.GetError())
	}

	// Missing providers.
	config = LoadModelConfig(write(t, "{}"))
	if !strings.Contains(config.GetError(), "- providers: Expected required property") {
		t.Fatalf("error = %q", config.GetError())
	}

	// A model without an id, plus a wrong-typed field.
	config = LoadModelConfig(write(t, `{"providers":{"p":{"models":[{"name":"x"}]}}}`))
	if !strings.Contains(config.GetError(), "- providers.p.models.0.id: Expected required property") {
		t.Fatalf("error = %q", config.GetError())
	}
	config = LoadModelConfig(write(t, `{"providers":{"p":{"authHeader":"yes"}}}`))
	if !strings.Contains(config.GetError(), "- providers.p.authHeader: Expected boolean") {
		t.Fatalf("error = %q", config.GetError())
	}
	config = LoadModelConfig(write(t, `{"providers":{"p":{"models":[{"id":""}]}}}`))
	if !strings.Contains(config.GetError(), "- providers.p.models.0.id: Expected string length greater or equal to 1") {
		t.Fatalf("error = %q", config.GetError())
	}
	config = LoadModelConfig(write(t, `{"providers":{"p":{"models":[{"id":"m","input":["pdf"]}]}}}`))
	if !strings.Contains(config.GetError(), "- providers.p.models.0.input.0: Expected union value") {
		t.Fatalf("error = %q", config.GetError())
	}
	config = LoadModelConfig(write(t, `{"providers":{"p":{"models":[{"id":"m","cost":{"input":1}}]}}}`))
	if !strings.Contains(config.GetError(), "- providers.p.models.0.cost.output: Expected required property") {
		t.Fatalf("error = %q", config.GetError())
	}
	// Override costs are partial.
	config = LoadModelConfig(write(t, `{"providers":{"p":{"modelOverrides":{"m":{"cost":{"input":1}}}}}}`))
	if config.GetError() != "" {
		t.Fatalf("error = %q", config.GetError())
	}
	// Compat literal validation.
	config = LoadModelConfig(write(t, `{"providers":{"p":{"compat":{"thinkingFormat":"nope"}}}}`))
	if !strings.Contains(config.GetError(), "- providers.p.compat.thinkingFormat: Expected union value") {
		t.Fatalf("error = %q", config.GetError())
	}
	config = LoadModelConfig(write(t, `{"providers":{"p":{"compat":{"sessionAffinityFormat":"openrouter"}}}}`))
	if config.GetError() != "" {
		t.Fatalf("error = %q", config.GetError())
	}
	// Unknown compat keys are allowed (typebox does not reject them).
	config = LoadModelConfig(write(t, `{"providers":{"p":{"compat":{"futureOption":true}}}}`))
	if config.GetError() != "" {
		t.Fatalf("error = %q", config.GetError())
	}
	// Unknown top-level keys are allowed too.
	config = LoadModelConfig(write(t, `{"providers":{},"extra":1}`))
	if config.GetError() != "" || len(config.GetProviderIDs()) != 0 {
		t.Fatalf("error = %q", config.GetError())
	}
	// Chat template kwargs: variable form, scalar form, invalid variable.
	config = LoadModelConfig(write(t, `{"providers":{"p":{"compat":{"chatTemplateKwargs":{"a":{"$var":"thinking.enabled","omitWhenOff":true},"b":1,"c":"x","d":null}}}}}`))
	if config.GetError() != "" {
		t.Fatalf("error = %q", config.GetError())
	}
	kwarg := config.GetProvider("p").Compat.ChatTemplateKwargs["a"]
	if kwarg.Variable == nil || kwarg.Variable.Var != "thinking.enabled" || kwarg.Variable.OmitWhenOff == nil || !*kwarg.Variable.OmitWhenOff {
		t.Fatalf("kwarg = %+v", kwarg)
	}
	long := config.GetProvider("p").Compat
	if long.ChatTemplateKwargs["b"].Scalar != float64(1) || long.ChatTemplateKwargs["c"].Scalar != "x" || long.ChatTemplateKwargs["d"].Scalar != nil {
		t.Fatalf("kwargs = %+v", long.ChatTemplateKwargs)
	}
	config = LoadModelConfig(write(t, `{"providers":{"p":{"compat":{"chatTemplateKwargs":{"a":{"$var":"nope"}}}}}}`))
	if !strings.Contains(config.GetError(), `- providers.p.compat.chatTemplateKwargs.a.$var: Expected union value`) {
		t.Fatalf("error = %q", config.GetError())
	}
	// Fallback models: maxItems and required fields.
	fallback := func(entries string) string {
		return `{"providers":{"p":{"models":[{"id":"m","compat":{"allowedFallbackModels":[` + entries + `]}}]}}}`
	}
	entry := `{"provider":"a","model":"b","cost":{"input":1,"output":2,"cacheRead":3,"cacheWrite":4}}`
	config = LoadModelConfig(write(t, fallback(entry)))
	if config.GetError() != "" {
		t.Fatalf("error = %q", config.GetError())
	}
	config = LoadModelConfig(write(t, fallback(entry+","+entry+","+entry+","+entry)))
	if !strings.Contains(config.GetError(), "Expected array length less or equal to 3") {
		t.Fatalf("error = %q", config.GetError())
	}
	config = LoadModelConfig(write(t, fallback(`{"provider":"a"}`)))
	if !strings.Contains(config.GetError(), "allowedFallbackModels.0.model: Expected required property") ||
		!strings.Contains(config.GetError(), "allowedFallbackModels.0.cost: Expected required property") {
		t.Fatalf("error = %q", config.GetError())
	}
	// OpenRouter routing validation.
	config = LoadModelConfig(write(t, `{"providers":{"p":{"models":[{"id":"m","compat":{"openRouterRouting":{"data_collection":"deny","max_price":{"prompt":1,"completion":"1"},"preferred_min_throughput":{"p50":1},"sort":{"by":"price","partition":null},"only":["a"],"zdr":true}}}]}}}`))
	if config.GetError() != "" {
		t.Fatalf("error = %q", config.GetError())
	}
	config = LoadModelConfig(write(t, `{"providers":{"p":{"models":[{"id":"m","compat":{"openRouterRouting":{"zdr":"yes"}}}]}}}`))
	if !strings.Contains(config.GetError(), "compat.openRouterRouting.zdr: Expected boolean") {
		t.Fatalf("error = %q", config.GetError())
	}
	config = LoadModelConfig(write(t, `{"providers":{"p":{"models":[{"id":"m","compat":{"vercelGatewayRouting":{"only":["a",1]}}}]}}}`))
	if !strings.Contains(config.GetError(), "compat.vercelGatewayRouting.only.1: Expected string") {
		t.Fatalf("error = %q", config.GetError())
	}

	// Load errors (a directory instead of a file).
	config = LoadModelConfig(dir)
	if !strings.HasPrefix(config.GetError(), "Failed to load models.json: ") {
		t.Fatalf("error = %q", config.GetError())
	}
}

func TestModelConfigPathOrderAndComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	// Comments, trailing commas, and a BOM are all stripped before parsing.
	content := "\uFEFF{\n  // comment\n  \"providers\": {\n    \"z\": {},\n    \"a\": {},\n    \"m\": {},\n  },\n}\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	config := LoadModelConfig(path)
	if err := config.GetError(); err != "" {
		t.Fatalf("error = %s", err)
	}
	if ids := strings.Join(config.GetProviderIDs(), ","); ids != "z,a,m" {
		t.Fatalf("ids = %s", ids)
	}
	if provider := config.GetProvider("a"); provider == nil {
		t.Fatal("provider a missing")
	}
}
