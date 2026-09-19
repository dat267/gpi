package ai

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Port of env-api-keys.ts.

const (
	AnthropicAuthTokenEnv  = "ANTHROPIC_AUTH_TOKEN"
	AnthropicOAuthTokenEnv = "ANTHROPIC_OAUTH_TOKEN"
	AnthropicAPIKeyEnv     = "ANTHROPIC_API_KEY"
)

// apiKeyEnvVars maps provider id to its API-key environment variable(s).
// Port of getApiKeyEnvVars.
func apiKeyEnvVars(provider string) []string {
	switch provider {
	case "github-copilot":
		return []string{"COPILOT_GITHUB_TOKEN"}
	case "anthropic":
		// ANTHROPIC_AUTH_TOKEN participates in env discovery/status, but
		// GetEnvApiKey skips it because requests must pass it as
		// Authorization: Bearer.
		return []string{AnthropicAuthTokenEnv, AnthropicOAuthTokenEnv, AnthropicAPIKeyEnv}
	}
	envMap := map[string]string{
		"ant-ling":                   "ANT_LING_API_KEY",
		"qwen-token-plan":            "QWEN_TOKEN_PLAN_API_KEY",
		"qwen-token-plan-cn":         "QWEN_TOKEN_PLAN_CN_API_KEY",
		"qwen-token-plan-individual": "QWEN_TOKEN_PLAN_API_KEY",
		"openai":                     "OPENAI_API_KEY",
		"azure-openai-responses":     "AZURE_OPENAI_API_KEY",
		"nvidia":                     "NVIDIA_API_KEY",
		"deepseek":                   "DEEPSEEK_API_KEY",
		"google":                     "GEMINI_API_KEY",
		"google-vertex":              "GOOGLE_CLOUD_API_KEY",
		"groq":                       "GROQ_API_KEY",
		"cerebras":                   "CEREBRAS_API_KEY",
		"xai":                        "XAI_API_KEY",
		"radius":                     "RADIUS_API_KEY",
		"openrouter":                 "OPENROUTER_API_KEY",
		"vercel-ai-gateway":          "AI_GATEWAY_API_KEY",
		"zai":                        "ZAI_API_KEY",
		"zai-coding-cn":              "ZAI_CODING_CN_API_KEY",
		"mistral":                    "MISTRAL_API_KEY",
		"minimax":                    "MINIMAX_API_KEY",
		"minimax-cn":                 "MINIMAX_CN_API_KEY",
		"moonshotai":                 "MOONSHOT_API_KEY",
		"moonshotai-cn":              "MOONSHOT_API_KEY",
		"huggingface":                "HF_TOKEN",
		"fireworks":                  "FIREWORKS_API_KEY",
		"together":                   "TOGETHER_API_KEY",
		"baseten":                    "BASETEN_API_KEY",
		"opencode":                   "OPENCODE_API_KEY",
		"opencode-go":                "OPENCODE_API_KEY",
		"kimi-coding":                "KIMI_API_KEY",
		"cloudflare-workers-ai":      "CLOUDFLARE_API_KEY",
		"cloudflare-ai-gateway":      "CLOUDFLARE_API_KEY",
		"xiaomi":                     "XIAOMI_API_KEY",
		"xiaomi-token-plan-cn":       "XIAOMI_TOKEN_PLAN_CN_API_KEY",
		"xiaomi-token-plan-ams":      "XIAOMI_TOKEN_PLAN_AMS_API_KEY",
		"xiaomi-token-plan-sgp":      "XIAOMI_TOKEN_PLAN_SGP_API_KEY",
	}
	if envVar, ok := envMap[provider]; ok {
		return []string{envVar}
	}
	return nil
}

// FindEnvKeys finds configured environment variables that can provide an API
// key for a provider. This only reports actual API key variables. It
// intentionally excludes ambient credential sources such as AWS profiles,
// AWS IAM credentials, and Google Application Default Credentials.
func FindEnvKeys(provider string, env ProviderEnv) []string {
	envVars := apiKeyEnvVars(provider)
	if envVars == nil {
		return nil
	}
	var found []string
	for _, envVar := range envVars {
		if _, ok := GetProviderEnvValue(envVar, env); ok {
			found = append(found, envVar)
		}
	}
	return found
}

// GetEnvApiKey gets an API key for a provider from known environment
// variables, e.g. OPENAI_API_KEY. Will not return API keys for providers
// that require OAuth tokens.
func GetEnvApiKey(provider string, env ProviderEnv) string {
	envKeys := FindEnvKeys(provider, env)
	if len(envKeys) > 0 {
		var apiKeyEnv string
		if provider == "anthropic" {
			for _, key := range envKeys {
				if key != AnthropicAuthTokenEnv {
					apiKeyEnv = key
					break
				}
			}
		} else {
			apiKeyEnv = envKeys[0]
		}
		if apiKeyEnv != "" {
			return GetProviderEnvValueOr(apiKeyEnv, env)
		}
	}

	// Vertex AI supports either an explicit API key or Application Default
	// Credentials. Auth is configured via `gcloud auth application-default login`.
	if provider == "google-vertex" {
		hasCredentials := hasVertexAdcCredentials(env)
		_, hasProject := GetProviderEnvValue("GOOGLE_CLOUD_PROJECT", env)
		if !hasProject {
			_, hasProject = GetProviderEnvValue("GCLOUD_PROJECT", env)
		}
		_, hasLocation := GetProviderEnvValue("GOOGLE_CLOUD_LOCATION", env)
		if hasCredentials && hasProject && hasLocation {
			return "<authenticated>"
		}
	}

	if provider == "amazon-bedrock" {
		// Amazon Bedrock supports multiple credential sources:
		// 1. AWS_PROFILE — named profile from ~/.aws/credentials
		// 2. AWS_ACCESS_KEY_ID + AWS_SECRET_ACCESS_KEY — standard IAM keys
		// 3. AWS_BEARER_TOKEN_BEDROCK — Bedrock bearer token
		// 4. AWS_CONTAINER_CREDENTIALS_RELATIVE_URI — ECS task roles
		// 5. AWS_CONTAINER_CREDENTIALS_FULL_URI — ECS task roles (full URI)
		// 6. AWS_WEB_IDENTITY_TOKEN_FILE — IRSA (IAM Roles for Service Accounts)
		if hasEnv("AWS_PROFILE", env) ||
			(hasEnv("AWS_ACCESS_KEY_ID", env) && hasEnv("AWS_SECRET_ACCESS_KEY", env)) ||
			hasEnv("AWS_BEARER_TOKEN_BEDROCK", env) ||
			hasEnv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", env) ||
			hasEnv("AWS_CONTAINER_CREDENTIALS_FULL_URI", env) ||
			hasEnv("AWS_WEB_IDENTITY_TOKEN_FILE", env) {
			return "<authenticated>"
		}
	}

	return ""
}

func hasEnv(name string, env ProviderEnv) bool {
	_, ok := GetProviderEnvValue(name, env)
	return ok
}

var (
	adcCacheMu    sync.Mutex
	adcCacheValue bool
	adcCacheSet   bool
)

// hasVertexAdcCredentials checks for Google Application Default Credentials:
// GOOGLE_APPLICATION_CREDENTIALS (explicit or env), else the default
// ~/.config/gcloud/application_default_credentials.json path.
func hasVertexAdcCredentials(env ProviderEnv) bool {
	if path, ok := GetProviderEnvValue("GOOGLE_APPLICATION_CREDENTIALS", env); ok {
		return fileExists(path)
	}
	adcCacheMu.Lock()
	defer adcCacheMu.Unlock()
	if adcCacheSet {
		return adcCacheValue
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	adcCacheValue = fileExists(filepath.Join(home, ".config", "gcloud", "application_default_credentials.json"))
	adcCacheSet = true
	return adcCacheValue
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

var _ = strings.TrimSpace
