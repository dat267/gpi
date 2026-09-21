package providers

import (
	"strings"

	"github.com/dat267/pier/ai"
)

// Port of providers/cloudflare-auth.ts, providers/cloudflare-stream.ts, and the
// endpoint constants in api/cloudflare.ts.

// Cloudflare endpoint constants.
const (
	// CloudflareWorkersAIBaseURL is the Workers AI direct endpoint.
	CloudflareWorkersAIBaseURL = "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1"
	// CloudflareAIGatewayCompatBaseURL is the AI Gateway Unified API.
	CloudflareAIGatewayCompatBaseURL = "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/compat"
	// CloudflareAIGatewayOpenAIBaseURL is the OpenAI passthrough route.
	CloudflareAIGatewayOpenAIBaseURL = "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai"
	// CloudflareAIGatewayAnthropicBaseURL is the Anthropic passthrough route.
	CloudflareAIGatewayAnthropicBaseURL = "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic"
)

// Cloudflare env names.
const (
	CloudflareAPIKeyEnv    = "CLOUDFLARE_API_KEY"
	CloudflareAccountIDEnv = "CLOUDFLARE_ACCOUNT_ID"
	CloudflareGatewayIDEnv = "CLOUDFLARE_GATEWAY_ID"
)

// CloudflareAuthKind names the two Cloudflare providers.
type CloudflareAuthKind = string

const (
	CloudflareAuthWorkersAI CloudflareAuthKind = "workers-ai"
	CloudflareAuthAIGateway CloudflareAuthKind = "ai-gateway"
)

// cloudflareResolveValue merges one field: the credential value wins, then the
// ambient environment (upstream resolveValue's per-field merge, so a key-only
// credential still picks up the account id from the environment).
func cloudflareResolveValue(name string, ctx ai.AuthContext, credential *ai.ApiKeyCredential) (string, bool) {
	if credential != nil {
		if name == CloudflareAPIKeyEnv {
			if credential.Key != "" {
				return credential.Key, true
			}
		} else if credential.Env != nil {
			if value, ok := credential.Env[name]; ok && value != "" {
				return value, true
			}
		}
	}
	if ctx == nil {
		return "", false
	}
	return ctx.Env(name)
}

// ResolveCloudflareEnv resolves the API key plus the account/gateway ids.
func ResolveCloudflareEnv(
	kind CloudflareAuthKind,
	ctx ai.AuthContext,
	credential *ai.ApiKeyCredential,
) (*ai.AuthResult, bool) {
	apiKey, hasKey := cloudflareResolveValue(CloudflareAPIKeyEnv, ctx, credential)
	accountID, hasAccount := cloudflareResolveValue(CloudflareAccountIDEnv, ctx, credential)
	gatewayID := ""
	hasGateway := true
	if kind == CloudflareAuthAIGateway {
		gatewayID, hasGateway = cloudflareResolveValue(CloudflareGatewayIDEnv, ctx, credential)
	}
	if !hasKey || !hasAccount || !hasGateway {
		return nil, false
	}

	env := ai.ProviderEnv{CloudflareAccountIDEnv: accountID}
	if gatewayID != "" {
		env[CloudflareGatewayIDEnv] = gatewayID
	}
	source := CloudflareAPIKeyEnv
	if credential != nil {
		source = "stored credential"
	}
	auth := ai.ModelAuth{}
	if kind == CloudflareAuthAIGateway {
		// The gateway authenticates with cf-aig-authorization; the SDK-style
		// placeholders are suppressed so a request header never overrides the
		// gateway's stored provider keys.
		bearer := "Bearer " + apiKey
		auth.Headers = ai.ProviderHeaders{
			"cf-aig-authorization": &bearer,
			"Authorization":        nil,
			"x-api-key":            nil,
		}
	} else {
		auth.APIKey = apiKey
	}
	return &ai.AuthResult{Auth: auth, Env: env, Source: source}, true
}

// CloudflareWorkersAIAuth is the Workers AI auth (upstream
// cloudflareWorkersAIAuth).
func CloudflareWorkersAIAuth() *ai.ApiKeyAuth {
	return &ai.ApiKeyAuth{
		Name: "Cloudflare API key",
		Login: func(interaction *ai.AuthInteraction) (*ai.ApiKeyCredential, error) {
			key, err := interaction.Prompt(ai.AuthPrompt{Type: ai.AuthPromptSecret, Message: "Enter Cloudflare API key"})
			if err != nil {
				return nil, err
			}
			accountID, err := interaction.Prompt(ai.AuthPrompt{Type: ai.AuthPromptText, Message: "Enter Cloudflare account ID"})
			if err != nil {
				return nil, err
			}
			return &ai.ApiKeyCredential{Key: key, Env: ai.ProviderEnv{CloudflareAccountIDEnv: accountID}}, nil
		},
		Resolve: func(input ai.AuthResolveInput) (*ai.AuthResult, error) {
			if err := ctxErrOf(input.Ctx2); err != nil {
				return nil, err
			}
			result, ok := ResolveCloudflareEnv(CloudflareAuthWorkersAI, input.Ctx, input.Credential)
			if !ok {
				return nil, nil
			}
			return result, nil
		},
	}
}

// CloudflareAIGatewayAuth is the AI Gateway auth (upstream
// cloudflareAIGatewayAuth).
func CloudflareAIGatewayAuth() *ai.ApiKeyAuth {
	return &ai.ApiKeyAuth{
		Name: "Cloudflare API key",
		Login: func(interaction *ai.AuthInteraction) (*ai.ApiKeyCredential, error) {
			key, err := interaction.Prompt(ai.AuthPrompt{Type: ai.AuthPromptSecret, Message: "Enter Cloudflare API key"})
			if err != nil {
				return nil, err
			}
			accountID, err := interaction.Prompt(ai.AuthPrompt{Type: ai.AuthPromptText, Message: "Enter Cloudflare account ID"})
			if err != nil {
				return nil, err
			}
			gatewayID, err := interaction.Prompt(ai.AuthPrompt{Type: ai.AuthPromptText, Message: "Enter Cloudflare AI Gateway ID"})
			if err != nil {
				return nil, err
			}
			return &ai.ApiKeyCredential{Key: key, Env: ai.ProviderEnv{
				CloudflareAccountIDEnv: accountID, CloudflareGatewayIDEnv: gatewayID,
			}}, nil
		},
		Resolve: func(input ai.AuthResolveInput) (*ai.AuthResult, error) {
			if err := ctxErrOf(input.Ctx2); err != nil {
				return nil, err
			}
			result, ok := ResolveCloudflareEnv(CloudflareAuthAIGateway, input.Ctx, input.Credential)
			if !ok {
				return nil, nil
			}
			return result, nil
		},
	}
}

// ResolveCloudflareModel materializes the account/gateway placeholders in a
// model's base URL from the resolved provider env (upstream
// resolveCloudflareModel).
func ResolveCloudflareModel(model *ai.Model, env ai.ProviderEnv) *ai.Model {
	if len(env) == 0 || model == nil {
		return model
	}
	baseURL := model.BaseURL
	if value, ok := env[CloudflareAccountIDEnv]; ok && value != "" {
		baseURL = replaceAllLiteral(baseURL, "{"+CloudflareAccountIDEnv+"}", value)
	}
	if value, ok := env[CloudflareGatewayIDEnv]; ok && value != "" {
		baseURL = replaceAllLiteral(baseURL, "{"+CloudflareGatewayIDEnv+"}", value)
	}
	if baseURL == model.BaseURL {
		return model
	}
	cloned := *model
	cloned.BaseURL = baseURL
	return &cloned
}

func replaceAllLiteral(input, target, replacement string) string {
	if target == "" {
		return input
	}
	out := make([]byte, 0, len(input))
	for index := 0; index < len(input); {
		if strings.HasPrefix(input[index:], target) {
			out = append(out, replacement...)
			index += len(target)
			continue
		}
		out = append(out, input[index])
		index++
	}
	return string(out)
}

// CloudflareStreams wraps an API implementation so account/gateway placeholders
// materialize before dispatch (upstream cloudflareStreams).
func CloudflareStreams(streams ai.ProviderStreams) ai.ProviderStreams {
	return cloudflareStreams{inner: streams}
}

type cloudflareStreams struct {
	inner ai.ProviderStreams
}

func (s cloudflareStreams) Stream(model *ai.Model, context ai.TranscriptContext, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	var env ai.ProviderEnv
	if options != nil {
		env = options.Env
	}
	return s.inner.Stream(ResolveCloudflareModel(model, env), context, options)
}

func (s cloudflareStreams) StreamSimple(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	var env ai.ProviderEnv
	if options != nil {
		env = options.Env
	}
	return s.inner.StreamSimple(ResolveCloudflareModel(model, env), context, options)
}

// CloudflareWorkersAIProvider builds the Workers AI provider (upstream
// cloudflareWorkersAIProvider).
func CloudflareWorkersAIProvider() *ai.Provider {
	return EnvAPIKeyProvider(EnvAPIKeyProviderSpec{
		ID: "cloudflare-workers-ai", Name: "Cloudflare Workers AI",
		Auth: CloudflareWorkersAIAuth(), Completions: true,
		WrapStreams: CloudflareStreams,
	})
}

// CloudflareAIGatewayProvider builds the AI Gateway provider (upstream
// cloudflareAIGatewayProvider). The api map is pinned to all three APIs because
// the generated catalog's `workers-ai/*` entries appear and disappear over
// time.
func CloudflareAIGatewayProvider() *ai.Provider {
	return EnvAPIKeyProvider(EnvAPIKeyProviderSpec{
		ID: "cloudflare-ai-gateway", Name: "Cloudflare AI Gateway",
		Auth:      CloudflareAIGatewayAuth(),
		Anthropic: true, Completions: true, Responses: true,
		WrapStreams: CloudflareStreams,
	})
}
