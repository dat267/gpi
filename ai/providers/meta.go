package providers

import "github.com/dat267/pier/ai"

// MetaProvider builds the built-in Meta provider (upstream metaProvider): the
// Meta Model API (`META_API_KEY`) over the shared openai-responses adapter,
// with the Muse subscription OAuth as the alternative.
func MetaProvider() *ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      "meta",
		Name:    "Meta",
		BaseURL: "https://api.meta.ai/v1",
		Auth: ai.ProviderAuth{
			APIKey: ai.EnvApiKeyAuth("Meta Model API key", []string{"META_API_KEY"}),
			OAuth:  ai.MetaOAuth(),
		},
		Models: ai.GetBuiltinModels("meta"),
		Single: openaiResponsesStreams{},
	})
}
