package providers

import (
	"github.com/dat267/gpi/ai"
)

// Port of providers/openai.ts and the OpenAI-compatible passthrough factories.

const openaiBaseURL = "https://api.openai.com/v1"

// openaiCompletionsStreams adapts the openai-completions implementation.
type openaiCompletionsStreams struct{}

func (openaiCompletionsStreams) Stream(model *ai.Model, context ai.TranscriptContext, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	opts := &ai.OpenAICompletionsOptions{StreamOptions: derefStreamOptions(options)}
	return ai.StreamOpenAICompletions(model, context, opts)
}

func (openaiCompletionsStreams) StreamSimple(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	return ai.StreamOpenAICompletionsSimple(model, context, options)
}

// OpenAIProvider builds the built-in OpenAI provider (port of openaiProvider).
func OpenAIProvider() *ai.Provider {
	models := ai.GetBuiltinModels("openai")
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      "openai",
		Name:    "OpenAI",
		BaseURL: openaiBaseURL,
		Auth: ai.ProviderAuth{
			APIKey: ai.EnvApiKeyAuth("OpenAI API key", []string{"OPENAI_API_KEY"}),
			// OAuth lands with the Codex OAuth flow port.
		},
		Models: models,
		Single: openaiCompletionsStreams{},
	})
}

// openaiResponsesStreams adapts the openai-responses implementation.
type openaiResponsesStreams struct{}

func (openaiResponsesStreams) Stream(model *ai.Model, context ai.TranscriptContext, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	opts := &ai.OpenAIResponsesOptions{StreamOptions: derefStreamOptions(options)}
	return ai.StreamOpenAIResponses(model, context, opts)
}

func (openaiResponsesStreams) StreamSimple(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	return ai.StreamOpenAIResponsesSimple(model, context, options)
}

// OpenAIResponsesProvider builds a Responses-API provider for a given id and
// base URL (used for the openai provider's Responses-API models and for
// Codex/Azure-style deployments).
func OpenAIResponsesProvider(id, name, baseURL string, envVars []string) *ai.Provider {
	models := ai.GetBuiltinModels(id)
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      id,
		Name:    name,
		BaseURL: baseURL,
		Auth:    ai.ProviderAuth{APIKey: ai.EnvApiKeyAuth(name+" API key", envVars)},
		Models:  models,
		Single:  openaiResponsesStreams{},
	})
}
