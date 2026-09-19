package providers

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dat267/gpi/ai"
)

// Port of providers/anthropic.ts (auth resolution and provider factory).
// The api implementation wires in when the HTTP/SSE layer lands.

const (
	anthropicBaseURL = "https://api.anthropic.com"
)

// AnthropicAPIKeyEnv, OAuth token and auth-token env names (env-api-keys.ts).
const (
	AnthropicAuthTokenEnv  = ai.AnthropicAuthTokenEnv
	AnthropicOAuthTokenEnv = ai.AnthropicOAuthTokenEnv
	AnthropicAPIKeyEnv     = ai.AnthropicAPIKeyEnv
)

// AnthropicAPIKeyAuth ports anthropicApiKeyAuth: stored credential wins,
// then ANTHROPIC_AUTH_TOKEN (as a Bearer header), then OAUTH_TOKEN, then
// API_KEY.
func AnthropicAPIKeyAuth() *ai.ApiKeyAuth {
	return &ai.ApiKeyAuth{
		Name: "Anthropic API key",
		Login: func(interaction *ai.AuthInteraction) (*ai.ApiKeyCredential, error) {
			if err := ctxErrOf(interaction.Ctx); err != nil {
				return nil, err
			}
			key, err := interaction.Prompt(ai.AuthPrompt{Type: ai.AuthPromptSecret, Message: "Enter Anthropic API key"})
			if err != nil {
				return nil, err
			}
			if err := ctxErrOf(interaction.Ctx); err != nil {
				return nil, err
			}
			return &ai.ApiKeyCredential{Key: key}, nil
		},
		Resolve: func(input ai.AuthResolveInput) (*ai.AuthResult, error) {
			if err := ctxErrOf(input.Ctx2); err != nil {
				return nil, err
			}
			if input.Credential != nil && input.Credential.Key != "" {
				return &ai.AuthResult{
					Auth:   ai.ModelAuth{APIKey: input.Credential.Key},
					Env:    input.Credential.Env,
					Source: "stored credential",
				}, nil
			}

			if authToken, ok := input.Ctx.Env(AnthropicAuthTokenEnv); ok {
				if err := ctxErrOf(input.Ctx2); err != nil {
					return nil, err
				}
				return &ai.AuthResult{
					Auth:   ai.ModelAuth{Headers: ai.ProviderHeaders{"Authorization": anthropicStrPtr("Bearer " + authToken)}},
					Source: AnthropicAuthTokenEnv,
				}, nil
			}

			for _, envVar := range []string{AnthropicOAuthTokenEnv, AnthropicAPIKeyEnv} {
				apiKey, ok := input.Ctx.Env(envVar)
				if err := ctxErrOf(input.Ctx2); err != nil {
					return nil, err
				}
				if ok {
					return &ai.AuthResult{Auth: ai.ModelAuth{APIKey: apiKey}, Source: envVar}, nil
				}
			}
			return nil, nil
		},
	}
}

func anthropicStrPtr(s string) *string { return &s }

func ctxErrOf(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

// AnthropicProvider builds the built-in Anthropic provider with its catalog
// models (port of anthropicProvider). The api implementation argument
// completes the wiring; nil means the provider streams errors until the
// anthropic-messages implementation is registered.
func AnthropicProvider(streams ai.ProviderStreams) *ai.Provider {
	models := ai.GetBuiltinModels("anthropic")
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      "anthropic",
		Name:    "Anthropic",
		BaseURL: anthropicBaseURL,
		Auth: ai.ProviderAuth{
			APIKey: AnthropicAPIKeyAuth(),
			// OAuth (Claude Pro/Max) lands with the OAuth flow port.
		},
		Models: models,
		Single: streams,
	})
}

// FirstAnthropicModel returns the first catalog model for smoke wiring.
func FirstAnthropicModel() *ai.Model { return ai.GetBuiltinModels("anthropic")[0] }

var _ = json.Marshal
var _ = fmt.Sprintf
