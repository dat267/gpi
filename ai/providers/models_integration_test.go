package providers

import (
	"context"
	"testing"
	"time"

	"github.com/dat267/pier/ai"
)

// Port of the Models-integration subset of
// packages/ai/test/providers.test.ts: "streams queued responses through a
// Models collection" and "merges provider-resolved env into stream options".

func TestFauxThroughModelsCollection(t *testing.T) {
	faux := FauxProvider(FauxOptions{})
	models := ai.CreateModels(nil)
	models.SetProvider(faux.Provider)
	faux.Core.SetResponses([]FauxResponseStep{{Message: FauxAssistantMessage("hello from faux", FauxMessageOptions{})}})

	model := models.GetModels(faux.Provider.ID)[0]
	result, err := models.CompleteSimple(model, ai.Context{
		Messages: []ai.Message{fauxUserMsg("hi", time.Now().UnixMilli())},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != ai.StopStop {
		t.Fatalf("stopReason = %s", result.StopReason)
	}
	if text := result.Content[0].(ai.TextContent).Text; text != "hello from faux" {
		t.Fatalf("text = %q", text)
	}
	if faux.Core.State().CallCount != 1 {
		t.Fatalf("callCount = %d", faux.Core.State().CallCount)
	}
}

func TestModelsMergesProviderResolvedEnvIntoStreamOptions(t *testing.T) {
	// Upstream: explicit request options win per-field; provider-resolved env
	// fills the rest.
	var capturedEnv ai.ProviderEnv
	var capturedAPIKey string

	capturing := &capturingStreams{capture: func(options *ai.StreamOptions) {
		capturedEnv = options.Env
		capturedAPIKey = options.APIKey
	}}

	envModel := &ai.Model{ID: "model-a", Name: "model-a", API: "api-a", Provider: "env-provider"}
	provider := ai.CreateProvider(ai.CreateProviderOptions{
		ID: "env-provider",
		Auth: ai.ProviderAuth{APIKey: &ai.ApiKeyAuth{Name: "Test",
			Resolve: func(ai.AuthResolveInput) (*ai.AuthResult, error) {
				return &ai.AuthResult{
					Auth: ai.ModelAuth{APIKey: "provider-key"},
					Env:  ai.ProviderEnv{"PROVIDER_ONLY": "provider", "SHARED": "provider"},
				}, nil
			}}},
		Models: []*ai.Model{envModel},
		Single: capturing,
	})
	models := ai.CreateModels(nil)
	models.SetProvider(provider)

	_, err := models.CompleteSimple(envModel, ai.Context{
		Messages: []ai.Message{fauxUserMsg("hi", 1)},
	}, &ai.ModelsSimpleStreamOptions{
		SimpleStreamOptions: ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{
			APIKey: "request-key",
			Env:    ai.ProviderEnv{"REQUEST_ONLY": "request", "SHARED": "request"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if capturedAPIKey != "request-key" {
		t.Fatalf("apiKey = %q; want request-key (explicit wins)", capturedAPIKey)
	}
	if capturedEnv["PROVIDER_ONLY"] != "provider" || capturedEnv["REQUEST_ONLY"] != "request" || capturedEnv["SHARED"] != "request" {
		t.Fatalf("env = %v", capturedEnv)
	}
}

func TestModelsStreamErrorWhenProviderUnconfigured(t *testing.T) {
	// Upstream applyAuth: unconfigured provider → ModelsError "auth".
	faux := FauxProvider(FauxOptions{Provider: "locked"})
	// Replace auth with an ambient-only resolver that finds nothing.
	faux.Provider.Auth = ai.ProviderAuth{APIKey: &ai.ApiKeyAuth{Name: "None",
		Resolve: func(ai.AuthResolveInput) (*ai.AuthResult, error) { return nil, nil }}}
	models := ai.CreateModels(nil)
	models.SetProvider(faux.Provider)

	model := models.GetModels(faux.Provider.ID)[0]
	stream := models.StreamSimple(model, ai.Context{Messages: []ai.Message{fauxUserMsg("hi", 1)}}, nil)
	result, err := stream.Result(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != ai.StopError || result.ErrorMessage == nil {
		t.Fatalf("result = %s / %v", result.StopReason, result.ErrorMessage)
	}
	if *result.ErrorMessage != "Provider is not configured: locked" {
		t.Fatalf("errorMessage = %q", *result.ErrorMessage)
	}
}

// capturingStreams records the options each call received.
type capturingStreams struct {
	capture func(options *ai.StreamOptions)
}

func (c *capturingStreams) Stream(model *ai.Model, context ai.TranscriptContext, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	if options != nil {
		c.capture(options)
	}
	stream := ai.NewAssistantMessageEventStream()
	msg := &ai.AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID,
		StopReason: ai.StopStop, Timestamp: 1}
	stream.Push(ai.AssistantMessageEvent{Type: ai.EventDone, Reason: ai.StopStop, Message: msg})
	stream.End(&msg)
	return stream
}

func (c *capturingStreams) StreamSimple(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	if options != nil {
		c.capture(&options.StreamOptions)
	}
	return c.Stream(model, context, &options.StreamOptions)
}
