package providers

import (
	"github.com/dat267/pier/ai"
)

// Port of providers/google.ts (Generative AI API).

const googleBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// googleStreams adapts the google-generative-ai implementation.
type googleStreams struct{}

func (googleStreams) Stream(model *ai.Model, context ai.TranscriptContext, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	opts := &ai.GoogleOptions{StreamOptions: derefStreamOptions(options)}
	return ai.StreamGoogleGenerativeAI(model, context, opts)
}

func (googleStreams) StreamSimple(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	return ai.StreamGoogleGenerativeAISimple(model, context, options)
}

// GoogleProvider builds the built-in Google provider (port of
// googleProvider). API keys resolve from GEMINI_API_KEY (or
// GOOGLE_GENERATIVE_AI_API_KEY via the env table).
func GoogleProvider() *ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      "google",
		Name:    "Google",
		BaseURL: googleBaseURL,
		Auth: ai.ProviderAuth{APIKey: ai.EnvApiKeyAuth("Google API key",
			[]string{"GEMINI_API_KEY", "GOOGLE_GENERATIVE_AI_API_KEY"})},
		Models: ai.GetBuiltinModels("google"),
		Single: googleStreams{},
	})
}
