package providers

import (
	"encoding/json"
	"fmt"

	"github.com/dat267/gpi/ai"
)

// Port of the thin provider factories in packages/ai/src/providers/*.ts.

// OpenCodeSessionHeader is OpenCode's required per-conversation routing header.
const OpenCodeSessionHeader = "x-opencode-session"

// EnvAPIKeyProviderSpec describes one env-API-key provider factory.
type EnvAPIKeyProviderSpec struct {
	ID      string
	Name    string
	BaseURL string
	// AuthName and EnvVars configure envApiKeyAuth.
	AuthName string
	EnvVars  []string
	// Anthropic/Completions/Responses/Google/Mistral select the API
	// implementations that exist in the port.
	Anthropic   bool
	Completions bool
	Responses   bool
	Google      bool
	Mistral     bool
	Vertex      bool
	Azure       bool
	// OpenCode wraps every implementation with the session header.
	OpenCode bool
	// Auth overrides the default env API-key auth (Cloudflare, Vertex).
	Auth *ai.ApiKeyAuth
	// WrapStreams wraps every implementation (Cloudflare placeholder
	// resolution).
	WrapStreams func(ai.ProviderStreams) ai.ProviderStreams
	// OAuth overrides the provider's OAuth auth (when a flow is ported).
	OAuth *ai.OAuthAuth
}

// EnvAPIKeyProvider builds one provider from a spec.
func EnvAPIKeyProvider(spec EnvAPIKeyProviderSpec) *ai.Provider {
	streams := map[ai.Api]ai.ProviderStreams{}
	if spec.Anthropic {
		streams[ai.APIAnthropicMessages] = anthropicMessagesStreams{}
	}
	if spec.Completions {
		streams[ai.APIOpenAICompletions] = openaiCompletionsStreams{}
	}
	if spec.Responses {
		streams[ai.APIOpenAIResponses] = openaiResponsesStreams{}
	}
	if spec.Google {
		streams[ai.APIGoogleGenerativeAI] = googleGenerativeAIStreams{}
	}
	if spec.Mistral {
		streams[ai.APIMistralConversations] = mistralConversationsStreams{}
	}
	if spec.Vertex {
		streams[ai.APIGoogleVertex] = googleVertexStreams{}
	}
	if spec.Azure {
		streams[ai.APIAzureOpenAIResponses] = azureOpenAIResponsesStreams{}
	}
	if spec.OpenCode {
		for api, implementation := range streams {
			streams[api] = WithOpenCodeSessionHeader(implementation)
		}
	}
	if spec.WrapStreams != nil {
		for api, implementation := range streams {
			streams[api] = spec.WrapStreams(implementation)
		}
	}
	authName := spec.AuthName
	if authName == "" {
		authName = spec.Name + " API key"
	}
	auth := ai.ProviderAuth{APIKey: ai.EnvApiKeyAuth(authName, spec.EnvVars)}
	if spec.Auth != nil {
		auth = ai.ProviderAuth{APIKey: spec.Auth}
	}
	switch spec.ID {
	case "xai":
		auth.OAuth = ai.XaiOAuth()
	case "kimi-coding":
		auth.OAuth = ai.KimiCodingOAuth()
	case "openrouter":
		auth.OAuth = ai.OpenRouterOAuth()
	case "openai-codex":
		auth.OAuth = ai.OpenAICodexOAuth()
	}
	if spec.OAuth != nil {
		auth.OAuth = spec.OAuth
	}
	options := ai.CreateProviderOptions{
		ID:      spec.ID,
		Name:    spec.Name,
		BaseURL: spec.BaseURL,
		Auth:    auth,
		Models:  ai.GetBuiltinModels(spec.ID),
	}
	if len(streams) == 1 {
		for _, implementation := range streams {
			options.Single = implementation
		}
	} else {
		options.ByAPI = map[string]ai.ProviderStreams{}
		for api, implementation := range streams {
			options.ByAPI[api] = implementation
		}
	}
	return ai.CreateProvider(options)
}

// ThinProviderSpecs lists every provider whose factory is a base URL plus env
// API-key auth (upstream's per-provider *.ts factories).
var ThinProviderSpecs = []EnvAPIKeyProviderSpec{
	{ID: "ant-ling", Name: "Ant Ling", BaseURL: "https://api.ant-ling.com/v1", EnvVars: []string{"ANT_LING_API_KEY"}, Completions: true},
	{ID: "baseten", Name: "Baseten", BaseURL: "https://inference.baseten.co/v1", EnvVars: []string{"BASETEN_API_KEY"}, Completions: true},
	{ID: "cerebras", Name: "Cerebras", BaseURL: "https://api.cerebras.ai/v1", EnvVars: []string{"CEREBRAS_API_KEY"}, Completions: true},
	{ID: "deepseek", Name: "DeepSeek", BaseURL: "https://api.deepseek.com", EnvVars: []string{"DEEPSEEK_API_KEY"}, Completions: true},
	{ID: "fireworks", Name: "Fireworks", BaseURL: "https://api.fireworks.ai/inference", EnvVars: []string{"FIREWORKS_API_KEY"}, Anthropic: true, Completions: true},
	{ID: "groq", Name: "Groq", BaseURL: "https://api.groq.com/openai/v1", EnvVars: []string{"GROQ_API_KEY"}, Completions: true},
	{ID: "huggingface", Name: "Hugging Face", BaseURL: "https://router.huggingface.co/v1", AuthName: "Hugging Face token", EnvVars: []string{"HF_TOKEN"}, Completions: true},
	{ID: "kimi-coding", Name: "Kimi For Coding", BaseURL: "https://api.kimi.com/coding", EnvVars: []string{"KIMI_API_KEY"}, Anthropic: true},
	{ID: "minimax", Name: "MiniMax", BaseURL: "https://api.minimax.io/anthropic", EnvVars: []string{"MINIMAX_API_KEY"}, Anthropic: true},
	{ID: "minimax-cn", Name: "MiniMax CN", BaseURL: "https://api.minimaxi.com/anthropic", AuthName: "MiniMax CN API key", EnvVars: []string{"MINIMAX_CN_API_KEY"}, Anthropic: true},
	{ID: "mistral", Name: "Mistral", BaseURL: "https://api.mistral.ai", EnvVars: []string{"MISTRAL_API_KEY"}, Mistral: true},
	{ID: "moonshotai", Name: "Moonshot AI", BaseURL: "https://api.moonshot.ai/v1", EnvVars: []string{"MOONSHOT_API_KEY"}, Completions: true},
	{ID: "moonshotai-cn", Name: "Moonshot AI CN", BaseURL: "https://api.moonshot.cn/v1", EnvVars: []string{"MOONSHOT_API_KEY"}, Completions: true},
	{ID: "nvidia", Name: "NVIDIA", BaseURL: "https://integrate.api.nvidia.com/v1", EnvVars: []string{"NVIDIA_API_KEY"}, Completions: true},
	{ID: "opencode", Name: "OpenCode Zen", EnvVars: []string{"OPENCODE_API_KEY"}, Anthropic: true, Google: true, Completions: true, Responses: true, OpenCode: true},
	{ID: "opencode-go", Name: "OpenCode Go", EnvVars: []string{"OPENCODE_API_KEY"}, Anthropic: true, Completions: true, Responses: true, OpenCode: true},
	{ID: "openrouter", Name: "OpenRouter", BaseURL: "https://openrouter.ai/api/v1", EnvVars: []string{"OPENROUTER_API_KEY"}, Anthropic: true, Completions: true},
	{ID: "qwen-token-plan", Name: "Qwen Token Plan", BaseURL: "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1", EnvVars: []string{"QWEN_TOKEN_PLAN_API_KEY"}, Completions: true},
	{ID: "qwen-token-plan-cn", Name: "Qwen Token Plan CN", BaseURL: "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", AuthName: "Qwen Token Plan CN API key", EnvVars: []string{"QWEN_TOKEN_PLAN_CN_API_KEY"}, Completions: true},
	{ID: "qwen-token-plan-individual", Name: "Qwen Token Plan Individual", BaseURL: "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1", AuthName: "Qwen Token Plan Individual API key", EnvVars: []string{"QWEN_TOKEN_PLAN_API_KEY"}, Completions: true},
	{ID: "together", Name: "Together", BaseURL: "https://api.together.ai/v1", EnvVars: []string{"TOGETHER_API_KEY"}, Completions: true},
	{ID: "vercel-ai-gateway", Name: "Vercel AI Gateway", BaseURL: "https://ai-gateway.vercel.sh", AuthName: "Vercel AI Gateway API key", EnvVars: []string{"AI_GATEWAY_API_KEY"}, Anthropic: true},
	{ID: "xai", Name: "xAI", BaseURL: "https://api.x.ai/v1", EnvVars: []string{"XAI_API_KEY"}, Responses: true},
	{ID: "xiaomi", Name: "Xiaomi", BaseURL: "https://api.xiaomimimo.com/v1", EnvVars: []string{"XIAOMI_API_KEY"}, Completions: true},
	{ID: "xiaomi-token-plan-ams", Name: "Xiaomi Token Plan AMS", BaseURL: "https://token-plan-ams.xiaomimimo.com/v1", AuthName: "Xiaomi Token Plan AMS API key", EnvVars: []string{"XIAOMI_TOKEN_PLAN_AMS_API_KEY"}, Completions: true},
	{ID: "xiaomi-token-plan-cn", Name: "Xiaomi Token Plan CN", BaseURL: "https://token-plan-cn.xiaomimimo.com/v1", AuthName: "Xiaomi Token Plan CN API key", EnvVars: []string{"XIAOMI_TOKEN_PLAN_CN_API_KEY"}, Completions: true},
	{ID: "xiaomi-token-plan-sgp", Name: "Xiaomi Token Plan SGP", BaseURL: "https://token-plan-sgp.xiaomimimo.com/v1", AuthName: "Xiaomi Token Plan SGP API key", EnvVars: []string{"XIAOMI_TOKEN_PLAN_SGP_API_KEY"}, Completions: true},
	{ID: "zai", Name: "Z.AI", BaseURL: "https://api.z.ai/api/coding/paas/v4", EnvVars: []string{"ZAI_API_KEY"}, Completions: true},
	{ID: "zai-coding-cn", Name: "Z.AI Coding CN", BaseURL: "https://open.bigmodel.cn/api/coding/paas/v4", AuthName: "Z.AI Coding CN API key", EnvVars: []string{"ZAI_CODING_CN_API_KEY"}, Completions: true},
}

// WithOpenCodeSessionHeader adds OpenCode's routing header before dispatch
// (upstream withOpenCodeSessionHeader).
func WithOpenCodeSessionHeader(streams ai.ProviderStreams) ai.ProviderStreams {
	withSession := func(options *ai.StreamOptions) *ai.StreamOptions {
		if options == nil || options.SessionID == "" {
			return options
		}
		for name := range options.Headers {
			if equalFoldASCII(name, OpenCodeSessionHeader) {
				return options
			}
		}
		headers := ai.ProviderHeaders{}
		for name, value := range options.Headers {
			headers[name] = value
		}
		session := options.SessionID
		headers[OpenCodeSessionHeader] = &session
		cloned := *options
		cloned.Headers = headers
		return &cloned
	}
	return openCodeStreams{inner: streams, withSession: withSession}
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := 0; index < len(a); index++ {
		ca, cb := a[index], b[index]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// openCodeStreams wraps a provider's streams with the session header.
type openCodeStreams struct {
	inner       ai.ProviderStreams
	withSession func(*ai.StreamOptions) *ai.StreamOptions
}

func (s openCodeStreams) Stream(model *ai.Model, context ai.TranscriptContext, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	return s.inner.Stream(model, context, s.withSession(options))
}

func (s openCodeStreams) StreamSimple(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	if options == nil {
		return s.inner.StreamSimple(model, context, nil)
	}
	cloned := *options
	cloned.StreamOptions = derefCopy(s.withSession(&options.StreamOptions))
	return s.inner.StreamSimple(model, context, &cloned)
}

func derefCopy(options *ai.StreamOptions) ai.StreamOptions {
	if options == nil {
		return ai.StreamOptions{}
	}
	return *options
}

// MistralProvider builds the built-in Mistral provider.
func MistralProvider() *ai.Provider { return EnvAPIKeyProvider(thinByID("mistral")) }

// AzureOpenAIResponsesProvider builds the built-in Azure OpenAI provider.
func AzureOpenAIResponsesProvider() *ai.Provider {
	return EnvAPIKeyProvider(EnvAPIKeyProviderSpec{
		ID: "azure-openai-responses", Name: "Azure OpenAI", Azure: true,
		AuthName: "Azure OpenAI API key", EnvVars: []string{"AZURE_OPENAI_API_KEY"},
	})
}

// GoogleVertexProvider builds the built-in Vertex provider.
func GoogleVertexProvider() *ai.Provider {
	return EnvAPIKeyProvider(EnvAPIKeyProviderSpec{
		ID: "google-vertex", Name: "Google Vertex AI", Vertex: true,
		AuthName: "Google Cloud credentials",
		// Vertex accepts an explicit API key or ADC (see D27).
		EnvVars: []string{"GOOGLE_CLOUD_API_KEY"},
	})
}

// GitHubCopilotProvider builds the built-in GitHub Copilot provider
// (upstream githubCopilotProvider). OAuth model filtering by the credential's
// available model ids is ported; the OAuth login flow is not.
func GitHubCopilotProvider() *ai.Provider {
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID:      "github-copilot",
		Name:    "GitHub Copilot",
		BaseURL: "https://api.individual.githubcopilot.com",
		Auth:    ai.ProviderAuth{APIKey: ai.EnvApiKeyAuth("GitHub Copilot token", []string{"COPILOT_GITHUB_TOKEN"})},
		Models:  ai.GetBuiltinModels("github-copilot"),
		FilterModels: func(models []*ai.Model, credential *ai.Credential) []*ai.Model {
			if credential == nil || credential.Type != ai.CredentialOAuth || credential.OAuth == nil {
				return models
			}
			// The OAuth credential carries the account's available model ids in
			// its extra fields (upstream credential.availableModelIds).
			raw, ok := credential.OAuth.Extra["availableModelIds"]
			if !ok {
				return models
			}
			var ids []string
			if err := jsonUnmarshalLenient(raw, &ids); err != nil || len(ids) == 0 {
				return models
			}
			available := map[string]bool{}
			for _, id := range ids {
				available[id] = true
			}
			var filtered []*ai.Model
			for _, model := range models {
				if available[model.ID] {
					filtered = append(filtered, model)
				}
			}
			return filtered
		},
		ByAPI: map[string]ai.ProviderStreams{
			ai.APIAnthropicMessages: anthropicMessagesStreams{},
			ai.APIOpenAICompletions: openaiCompletionsStreams{},
			ai.APIOpenAIResponses:   openaiResponsesStreams{},
		},
	})
}

// AmazonBedrockProvider builds the built-in Bedrock provider.
func AmazonBedrockProvider() *ai.Provider { return ai.AmazonBedrockProvider() }

// OpenRouterImagesProvider builds the built-in OpenRouter image provider.
func OpenRouterImagesProvider() *ai.ImagesProvider { return ai.OpenRouterImagesProvider() }

// BuiltinImagesModels registers every built-in image provider.
func BuiltinImagesModels(models *ai.ImagesModels) *ai.ImagesModels {
	return ai.BuiltinImagesModels(models)
}

// RadiusProvider builds the dynamic Radius gateway provider (upstream
// radiusProvider). Its pi-messages API adapter is not ported, so the provider
// advertises auth and the refreshed catalogue while dispatching to a
// missing-implementation error.
func RadiusProvider(gateway string) *ai.Provider {
	return ai.RadiusProvider(ai.RadiusProviderOptions{Gateway: gateway, Streams: piMessagesStreams{}})
}

// BuiltinProviderIDs is the upstream builtinProviders() order.
var BuiltinProviderIDs = []string{
	"amazon-bedrock", "ant-ling", "anthropic", "azure-openai-responses", "baseten", "cerebras",
	"cloudflare-ai-gateway", "cloudflare-workers-ai", "deepseek", "fireworks", "github-copilot",
	"google", "google-vertex", "groq", "huggingface", "kimi-coding", "minimax", "minimax-cn",
	"mistral", "moonshotai", "moonshotai-cn", "nvidia", "openai", "openai-codex", "opencode",
	"opencode-go", "openrouter", "qwen-token-plan", "qwen-token-plan-cn",
	"qwen-token-plan-individual", "radius", "together", "vercel-ai-gateway", "xai", "xiaomi",
	"xiaomi-token-plan-ams", "xiaomi-token-plan-cn", "xiaomi-token-plan-sgp", "zai", "zai-coding-cn",
}

// UnportedBuiltinProviderIDs are built-in providers whose API adapters are not
// ported yet; BuiltinProviders skips them.
var UnportedBuiltinProviderIDs = []string{
	"openai-codex",
}

// BuiltinProviders returns every built-in provider whose adapter is ported, in
// upstream order and freshly constructed (upstream builtinProviders).
func BuiltinProviders() []*ai.Provider {
	unported := map[string]bool{}
	for _, id := range UnportedBuiltinProviderIDs {
		unported[id] = true
	}
	var providers []*ai.Provider
	for _, id := range BuiltinProviderIDs {
		if unported[id] {
			continue
		}
		if provider := builtinProvider(id); provider != nil {
			providers = append(providers, provider)
		}
	}
	return providers
}

func builtinProvider(id string) *ai.Provider {
	switch id {
	case "radius":
		return RadiusProvider("")
	case "amazon-bedrock":
		return AmazonBedrockProvider()
	case "anthropic":
		return AnthropicProvider(anthropicMessagesStreams{})
	case "google":
		return GoogleProvider()
	case "google-vertex":
		return GoogleVertexProvider()
	case "openai":
		return OpenAIProvider()
	case "azure-openai-responses":
		return AzureOpenAIResponsesProvider()
	case "github-copilot":
		return GitHubCopilotProvider()
	case "cloudflare-ai-gateway":
		return CloudflareAIGatewayProvider()
	case "cloudflare-workers-ai":
		return CloudflareWorkersAIProvider()
	case "mistral":
		return MistralProvider()
	}
	spec := thinByID(id)
	if spec.ID == "" || spec.ID != id {
		return nil
	}
	return EnvAPIKeyProvider(spec)
}

// BuiltinModels builds a Models registry with every ported built-in provider
// registered (upstream builtinModels).
func BuiltinModels(models *ai.Models) *ai.Models {
	for _, provider := range BuiltinProviders() {
		models.SetProvider(provider)
	}
	return models
}

func thinByID(id string) EnvAPIKeyProviderSpec {
	for _, spec := range ThinProviderSpecs {
		if spec.ID == id {
			return spec
		}
	}
	return EnvAPIKeyProviderSpec{ID: id, Name: id}
}

// ─── API implementation adapters ─────────────────────────────────────────────

// anthropicMessagesStreams adapts the anthropic-messages implementation.
type anthropicMessagesStreams struct{}

func (anthropicMessagesStreams) Stream(model *ai.Model, context ai.TranscriptContext, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	return ai.StreamAnthropic(model, context, &ai.AnthropicOptions{StreamOptions: derefStreamOptions(options)})
}

func (anthropicMessagesStreams) StreamSimple(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	return ai.StreamAnthropicSimple(model, context, options)
}

// googleGenerativeAIStreams adapts the google-generative-ai implementation.
type googleGenerativeAIStreams struct{}

func (googleGenerativeAIStreams) Stream(model *ai.Model, context ai.TranscriptContext, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	return ai.StreamGoogleGenerativeAI(model, context, &ai.GoogleOptions{StreamOptions: derefStreamOptions(options)})
}

func (googleGenerativeAIStreams) StreamSimple(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	return ai.StreamGoogleGenerativeAISimple(model, context, options)
}

// mistralConversationsStreams adapts the mistral-conversations implementation.
type mistralConversationsStreams struct{}

func (mistralConversationsStreams) Stream(model *ai.Model, context ai.TranscriptContext, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	return ai.StreamMistralConversations(model, context, &ai.MistralOptions{StreamOptions: derefStreamOptions(options)})
}

func (mistralConversationsStreams) StreamSimple(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	return ai.StreamMistralConversationsSimple(model, context, options)
}

// googleVertexStreams adapts the google-vertex implementation.
type googleVertexStreams struct{}

func (googleVertexStreams) Stream(model *ai.Model, context ai.TranscriptContext, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	return ai.StreamGoogleVertex(model, context, &ai.GoogleVertexOptions{
		GoogleOptions: ai.GoogleOptions{StreamOptions: derefStreamOptions(options)},
	})
}

func (googleVertexStreams) StreamSimple(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	return ai.StreamGoogleVertexSimple(model, context, options)
}

// azureOpenAIResponsesStreams adapts the azure-openai-responses implementation.
type azureOpenAIResponsesStreams struct{}

func (azureOpenAIResponsesStreams) Stream(model *ai.Model, context ai.TranscriptContext, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	return ai.StreamAzureOpenAIResponses(model, context, &ai.AzureOpenAIResponsesOptions{
		OpenAIResponsesOptions: ai.OpenAIResponsesOptions{StreamOptions: derefStreamOptions(options)},
	})
}

func (azureOpenAIResponsesStreams) StreamSimple(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	return ai.StreamAzureOpenAIResponsesSimple(model, context, options)
}

// jsonUnmarshalLenient decodes a credential extra field.
func jsonUnmarshalLenient(raw []byte, target any) error {
	if len(raw) == 0 {
		return fmt.Errorf("empty value")
	}
	return json.Unmarshal(raw, target)
}

// piMessagesStreams adapts the pi-messages implementation.
type piMessagesStreams struct{}

func (piMessagesStreams) Stream(model *ai.Model, context ai.TranscriptContext, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
	return ai.StreamPiMessages(model, context, &ai.PiMessagesOptions{StreamOptions: derefStreamOptions(options)})
}

func (piMessagesStreams) StreamSimple(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
	return ai.StreamPiMessagesSimple(model, context, options)
}
