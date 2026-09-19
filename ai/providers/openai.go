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
