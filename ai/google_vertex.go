package ai

import (
	"bytes"
	ctxpkg "context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Port of api/google-vertex.ts and the Google Application Default Credentials
// resolution its client performs.

// GoogleVertexAPIVersion is the Vertex API version the adapter targets.
const GoogleVertexAPIVersion = "v1"

// GCPVertexCredentialsMarker marks a credential-store value that stands for
// "use Application Default Credentials".
const GCPVertexCredentialsMarker = "gcp-vertex-credentials"

// GoogleVertexOptions are the Vertex stream options.
type GoogleVertexOptions struct {
	GoogleOptions
	// Project overrides the resolved Google Cloud project.
	Project string
	// Location overrides the resolved Vertex location.
	Location string
}

// StreamGoogleVertex streams a Vertex AI generateContent call
// (upstream stream).
func StreamGoogleVertex(model *Model, context TranscriptContext, options *GoogleVertexOptions) *AssistantMessageEventStream {
	googleOptions := &GoogleOptions{}
	project := ""
	location := ""
	if options != nil {
		googleOptions = &options.GoogleOptions
		project = options.Project
		location = options.Location
	}
	ctx := bgCtx()
	if googleOptions.Ctx != nil {
		ctx = googleOptions.Ctx
	}
	if googleOptions.Env == nil {
		googleOptions.Env = providerEnvMap()
	}
	if googleOptions.APIKey == "" {
		googleOptions.APIKey = GetProviderEnvValueOr("GOOGLE_CLOUD_API_KEY", googleOptions.Env)
	}
	if project == "" {
		project = GetProviderEnvValueOr("GOOGLE_CLOUD_PROJECT", googleOptions.Env)
		if project == "" {
			project = GetProviderEnvValueOr("GCLOUD_PROJECT", googleOptions.Env)
		}
	}
	if location == "" {
		location = GetProviderEnvValueOr("GOOGLE_CLOUD_LOCATION", googleOptions.Env)
	}
	if googleOptions.Headers == nil {
		googleOptions.Headers = ProviderHeaders{}
	}

	config := &googleStreamConfig{
		api: APIGoogleVertex,
		preflight: func(model *Model, options *GoogleOptions) error {
			if ResolveVertexAPIKey(options.APIKey) != "" {
				return nil
			}
			if project == "" {
				return fmt.Errorf("Vertex AI requires a project ID. Set GOOGLE_CLOUD_PROJECT/GCLOUD_PROJECT or pass project in options.")
			}
			if location == "" {
				return fmt.Errorf("Vertex AI requires a location. Set GOOGLE_CLOUD_LOCATION or pass location in options.")
			}
			_, err := vertexAccessToken(ctx, options.Env)
			return err
		},
		request: func(requestCtx ctxpkg.Context, model *Model, params *GoogleGenerateContentParams, options *GoogleOptions) (*http.Response, error) {
			requestURL, err := VertexRequestURL(model, project, location)
			if err != nil {
				return nil, err
			}
			token := ""
			if ResolveVertexAPIKey(options.APIKey) == "" {
				token, err = vertexAccessToken(requestCtx, options.Env)
				if err != nil {
					return nil, err
				}
			}
			body, err := MarshalJSON(params)
			if err != nil {
				return nil, err
			}
			return retryVertexRequest(requestCtx, model, requestURL, body, options, token)
		},
		noFinishReasonMessage: "Google Vertex stream ended without a finish reason",
	}
	return streamGoogle(model, context, googleOptions, config)
}

// ResolveVertexAPIKey returns the usable Vertex API key, or "" when the
// configured value is absent, the ADC marker, or a placeholder
// (upstream resolveApiKey).
func ResolveVertexAPIKey(apiKey string) string {
	trimmed := strings.TrimSpace(apiKey)
	if trimmed == "" || trimmed == GCPVertexCredentialsMarker || isPlaceholderAPIKey(trimmed) {
		return ""
	}
	return trimmed
}

var placeholderAPIKeyPattern = regexp.MustCompile(`^<[^>]+>$`)

func isPlaceholderAPIKey(apiKey string) bool {
	return placeholderAPIKeyPattern.MatchString(apiKey)
}

// VertexRequestURL builds the streaming endpoint for a model
// (upstream buildHttpOptions plus the SDK's URL layout).
func VertexRequestURL(model *Model, project, location string) (string, error) {
	base := strings.TrimSpace(model.BaseURL)
	custom := ""
	if base != "" && !strings.Contains(base, "{location}") {
		custom = base
	}
	version := GoogleVertexAPIVersion
	if custom != "" && baseURLIncludesAPIVersion(custom) {
		version = ""
	}
	path := fmt.Sprintf("projects/%s/locations/%s/publishers/google/models/%s:streamGenerateContent?alt=sse",
		project, location, model.ID)
	if custom != "" {
		segments := []string{strings.TrimSuffix(custom, "/")}
		if version != "" {
			segments = append(segments, version)
		}
		segments = append(segments, path)
		return strings.Join(segments, "/"), nil
	}
	if base != "" {
		// A generated Vertex base URL is a {location} template.
		host := strings.ReplaceAll(base, "{location}", location)
		if version != "" {
			return strings.TrimSuffix(host, "/") + "/" + version + "/" + path, nil
		}
		return strings.TrimSuffix(host, "/") + "/" + path, nil
	}
	if version != "" {
		return "https://aiplatform.googleapis.com/" + version + "/" + path, nil
	}
	return "https://aiplatform.googleapis.com/" + path, nil
}

// baseURLIncludesAPIVersion reports whether a base URL already carries an API
// version segment.
func baseURLIncludesAPIVersion(baseURL string) bool {
	if parsed, err := url.Parse(baseURL); err == nil {
		for _, part := range strings.Split(parsed.Path, "/") {
			if apiVersionPattern.MatchString(part) {
				return true
			}
		}
		return false
	}
	return baseURLAPIVersionPattern.MatchString(baseURL)
}

// baseURLAPIVersionPattern matches an API version path segment (v1, v1beta1)
// anywhere in a base URL.
var baseURLAPIVersionPattern = regexp.MustCompile(`(?:^|/)v\d+(?:beta\d*)?(?:/|$)`)

var apiVersionPattern = regexp.MustCompile(`^v\d+(?:beta\d*)?$`)

// retryVertexRequest issues the Vertex call with the shared provider retry
// policy.
func retryVertexRequest(
	ctx ctxpkg.Context,
	model *Model,
	requestURL string,
	body []byte,
	options *GoogleOptions,
	accessToken string,
) (*http.Response, error) {
	return RetryProviderRequest(ctx, func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", GetPiUserAgent())
		if accessToken != "" {
			req.Header.Set("Authorization", "Bearer "+accessToken)
			if project := GetProviderEnvValueOr("GOOGLE_CLOUD_PROJECT", options.Env); project != "" {
				req.Header.Set("x-goog-user-project", project)
			}
		} else {
			req.Header.Set("x-goog-api-key", options.APIKey)
		}
		for name, value := range model.Headers {
			req.Header.Set(name, value)
		}
		for name, value := range options.Headers {
			if value == nil {
				req.Header.Del(name)
				continue
			}
			req.Header.Set(name, *value)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 400 {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			return nil, &ProviderError{
				Status: resp.StatusCode, Headers: resp.Header,
				Message: fmt.Sprintf("%d %s: %s", resp.StatusCode, http.StatusText(resp.StatusCode), string(raw)),
				Body:    string(raw),
			}
		}
		return resp, nil
	}, &ProviderRetryOptions{MaxRetries: options.MaxRetries, MaxRetryDelayMS: options.MaxRetryDelayMs})
}

// StreamGoogleVertexSimple maps reasoning levels onto the Vertex thinking
// controls (upstream streamSimple).
func StreamGoogleVertexSimple(model *Model, context TranscriptContext, options *SimpleStreamOptions) *AssistantMessageEventStream {
	if options == nil {
		options = &SimpleStreamOptions{}
	}
	stream := NewAssistantMessageEventStream()
	go func() {
		vertexOptions := &GoogleVertexOptions{GoogleOptions: GoogleOptions{
			StreamOptions: options.StreamOptions, ToolChoice: deref(options.ToolChoice),
		}}
		if options.Reasoning == "" {
			vertexOptions.Thinking = &GoogleThinkingOption{Enabled: false}
			forwardStream(stream, StreamGoogleVertex(model, context, vertexOptions))
			return
		}
		clamped := ClampThinkingLevel(model, options.Reasoning)
		if clamped == ThinkOff {
			vertexOptions.Thinking = &GoogleThinkingOption{Enabled: false}
			forwardStream(stream, StreamGoogleVertex(model, context, vertexOptions))
			return
		}
		resolved, err := ResolveGoogleThinkingLevel(model, clamped)
		if err != nil {
			message := err.Error()
			msg := &AssistantMessage{
				API: model.API, Provider: model.Provider, Model: model.ID,
				Usage: Usage{Cost: UsageCost{}}, StopReason: StopError,
				ErrorMessage: &message, Timestamp: time.Now().UnixMilli(),
			}
			stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopError, Error: msg})
			stream.End(&msg)
			return
		}
		if UsesGoogleThinkingLevel(model) {
			vertexOptions.Thinking = &GoogleThinkingOption{Enabled: true, Level: ToGoogleThinkingLevel(resolved)}
		} else {
			budget := GetGoogleBudget(model, resolved, options.ThinkingBudgets)
			vertexOptions.Thinking = &GoogleThinkingOption{Enabled: true, BudgetTokens: &budget}
		}
		forwardStream(stream, StreamGoogleVertex(model, context, vertexOptions))
	}()
	return stream
}

// providerEnvMap snapshots the process environment for provider env lookups.
func providerEnvMap() ProviderEnv {
	env := ProviderEnv{}
	for _, entry := range os.Environ() {
		if index := strings.Index(entry, "="); index > 0 {
			env[entry[:index]] = entry[index+1:]
		}
	}
	return env
}

// ─── Application Default Credentials ─────────────────────────────────────────

// vertexTokenCache caches minted ADC tokens per credential source.
var vertexTokenCache = struct {
	mu      sync.Mutex
	entries map[string]cachedVertexToken
}{entries: map[string]cachedVertexToken{}}

type cachedVertexToken struct {
	token   string
	expires time.Time
}

// vertexAccessToken resolves an ADC access token: an authorized-user
// credential file (GOOGLE_APPLICATION_CREDENTIALS or the gcloud ADC file)
// refreshed against its token endpoint, or the GCE metadata server.
//
// D27: upstream delegates to google-auth-library, which also signs
// service-account keys and speaks workload identity; the Go port implements the
// refresh-token and metadata flows and reports the unimplemented credential
// types explicitly.
func vertexAccessToken(ctx ctxpkg.Context, env ProviderEnv) (string, error) {
	path := GetProviderEnvValueOr("GOOGLE_APPLICATION_CREDENTIALS", env)
	if path == "" {
		path = gcloudADCPath(env)
	}
	if path != "" {
		credentials, err := readAuthorizedUserCredentials(path)
		if err != nil {
			return "", err
		}
		if credentials != nil {
			return refreshAuthorizedUserToken(ctx, path, credentials)
		}
	}
	return metadataAccessToken(ctx, env)
}

func gcloudADCPath(env ProviderEnv) string {
	configDir := GetProviderEnvValueOr("CLOUDSDK_CONFIG", env)
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		configDir = filepath.Join(home, ".config", "gcloud")
	}
	path := filepath.Join(configDir, "application_default_credentials.json")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

type authorizedUserCredentials struct {
	Type         string `json:"type"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`
	TokenURI     string `json:"token_uri"`
}

// readAuthorizedUserCredentials reads a credential file, returning nil for a
// non authorized-user file (so the caller falls back to the metadata server).
func readAuthorizedUserCredentials(path string) (*authorizedUserCredentials, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read Google credentials %s: %w", path, err)
	}
	var credentials authorizedUserCredentials
	if err := json.Unmarshal(raw, &credentials); err != nil {
		return nil, fmt.Errorf("could not parse Google credentials %s: %w", path, err)
	}
	switch credentials.Type {
	case "authorized_user":
		return &credentials, nil
	case "service_account":
		return nil, fmt.Errorf("service-account key files are not supported by the Go port (D27); " +
			"use an authorized-user credential, the gcloud ADC file, or a Vertex API key")
	default:
		return nil, nil
	}
}

func refreshAuthorizedUserToken(ctx ctxpkg.Context, cacheKey string, credentials *authorizedUserCredentials) (string, error) {
	vertexTokenCache.mu.Lock()
	if cached, ok := vertexTokenCache.entries[cacheKey]; ok && cached.token != "" && time.Now().Before(cached.expires) {
		vertexTokenCache.mu.Unlock()
		return cached.token, nil
	}
	vertexTokenCache.mu.Unlock()

	tokenURI := credentials.TokenURI
	if tokenURI == "" {
		tokenURI = "https://oauth2.googleapis.com/token"
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", credentials.ClientID)
	form.Set("client_secret", credentials.ClientSecret)
	form.Set("refresh_token", credentials.RefreshToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return "", &ProviderError{
			Status: resp.StatusCode, Headers: resp.Header,
			Message: fmt.Sprintf("%d %s: %s", resp.StatusCode, http.StatusText(resp.StatusCode), string(raw)),
			Body:    string(raw),
		}
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", err
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("Google credential refresh returned no access token")
	}
	expiresIn := payload.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	// Refresh a minute early to avoid using an expired token.
	expires := time.Now().Add(time.Duration(expiresIn)*time.Second - time.Minute)
	vertexTokenCache.mu.Lock()
	vertexTokenCache.entries[cacheKey] = cachedVertexToken{token: payload.AccessToken, expires: expires}
	vertexTokenCache.mu.Unlock()
	return payload.AccessToken, nil
}

func metadataAccessToken(ctx ctxpkg.Context, env ProviderEnv) (string, error) {
	host := GetProviderEnvValueOr("GCE_METADATA_HOST", env)
	if host == "" {
		host = "169.254.169.254"
	}
	requestURL := "http://" + host + "/computeMetadata/v1/instance/service-accounts/default/token"
	cacheKey := "metadata:" + host
	vertexTokenCache.mu.Lock()
	if cached, ok := vertexTokenCache.entries[cacheKey]; ok && cached.token != "" && time.Now().Before(cached.expires) {
		vertexTokenCache.mu.Unlock()
		return cached.token, nil
	}
	vertexTokenCache.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("no Google Cloud credentials found: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("no Google Cloud credentials found: metadata server returned %d", resp.StatusCode)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("no Google Cloud credentials found")
	}
	expiresIn := payload.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 3600
	}
	expires := time.Now().Add(time.Duration(expiresIn)*time.Second - time.Minute)
	vertexTokenCache.mu.Lock()
	vertexTokenCache.entries[cacheKey] = cachedVertexToken{token: payload.AccessToken, expires: expires}
	vertexTokenCache.mu.Unlock()
	return payload.AccessToken, nil
}
