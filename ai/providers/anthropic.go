package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dat267/pier/ai"
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

// anthropicStreams adapts the anthropic-messages implementation to
// ai.ProviderStreams.
type anthropicStreams struct{}

func (anthropicStreams) Stream(model *ai.Model, context ai.TranscriptContext, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	anthropicOptions := &ai.AnthropicOptions{StreamOptions: derefStreamOptions(options)}
	return ai.StreamAnthropic(model, context, anthropicOptions)
}

func (anthropicStreams) StreamSimple(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	return ai.StreamAnthropicSimple(model, context, options)
}

func (anthropicStreams) FetchDeferred(model *ai.Model, handle *ai.DeferredHandle, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	stream := ai.NewAssistantMessageEventStream()
	err := fmt.Errorf("Provider anthropic does not support deferred responses for %q", model.API)
	msgText := err.Error()
	msg := &ai.AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID,
		StopReason: ai.StopError, ErrorMessage: &msgText, Timestamp: time.Now().UnixMilli()}
	stream.Push(ai.AssistantMessageEvent{Type: ai.EventError, Reason: ai.StopError, Error: msg})
	stream.End(&msg)
	return stream
}

func derefStreamOptions(options *ai.StreamOptions) ai.StreamOptions {
	if options == nil {
		return ai.StreamOptions{}
	}
	return *options
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
			// OAuth (Claude Pro/Max): the flow lives in the ai package, so the
			// provider holds it directly (upstream uses lazyOAuth for bundlers).
			OAuth: ai.AnthropicOAuth(),
		},
		Models: models,
		Single: func() ai.ProviderStreams {
			if streams != nil {
				return streams
			}
			return anthropicStreams{}
		}(),
	})
}

// FirstAnthropicModel returns the first catalog model for smoke wiring.
func FirstAnthropicModel() *ai.Model { return ai.GetBuiltinModels("anthropic")[0] }

var _ = json.Marshal
var _ = fmt.Sprintf
