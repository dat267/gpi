package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Port of auth/oauth/github-copilot.ts: the GitHub Copilot device flow, the
// Copilot token exchange, and the account model policy handling.

// GitHubCopilotClientID is the pi OAuth client id (upstream decodes it from
// base64).
const GitHubCopilotClientID = "Iv1.b507a08c87ecfe98"

// CopilotAPIVersion is the GitHub API version the flow requests.
const CopilotAPIVersion = "2026-06-01"

// GitHubCopilotHeaders are the Copilot client headers.
var GitHubCopilotHeaders = map[string]string{
	"User-Agent":             "GitHubCopilotChat/0.35.0",
	"Editor-Version":         "vscode/1.107.0",
	"Editor-Plugin-Version":  "copilot-chat/0.35.0",
	"Copilot-Integration-Id": "vscode-chat",
}

// COPILOTEnterpriseKey / COPILOTModelsKey are the credential extra fields.
const (
	CopilotEnterpriseURLKey  = "enterpriseUrl"
	CopilotAvailableModelKey = "availableModelIds"
)

// NormalizeGitHubDomain turns a URL or bare domain into a hostname.
func NormalizeGitHubDomain(input string) (string, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", false
	}
	candidate := trimmed
	if !strings.Contains(trimmed, "://") {
		candidate = "https://" + trimmed
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Hostname() == "" {
		return "", false
	}
	return parsed.Hostname(), true
}

// GitHubCopilotURLs are the per-domain endpoints.
type GitHubCopilotURLs struct {
	DeviceCodeURL   string
	AccessTokenURL  string
	CopilotTokenURL string
}

// githubCopilotURLsBuilder and githubCopilotBaseURLBuilder are overridable so
// tests can point the flow at a local server (upstream mocks fetch).
var (
	githubCopilotURLsBuilder    = GitHubCopilotURLsFor
	githubCopilotBaseURLBuilder = GetGitHubCopilotBaseURL
)

// GitHubCopilotURLsFor builds the endpoints for a GitHub domain.
func GitHubCopilotURLsFor(domain string) GitHubCopilotURLs {
	return GitHubCopilotURLs{
		DeviceCodeURL:   "https://" + domain + "/login/device/code",
		AccessTokenURL:  "https://" + domain + "/login/oauth/access_token",
		CopilotTokenURL: "https://api." + domain + "/copilot_internal/v2/token",
	}
}

// GetBaseURLFromCopilotToken extracts the proxy endpoint from a Copilot token
// (`proxy-ep=proxy.individual.githubcopilot.com;` -> https://api.individual...).
func GetBaseURLFromCopilotToken(token string) (string, bool) {
	index := strings.Index(token, "proxy-ep=")
	if index == -1 {
		return "", false
	}
	rest := token[index+len("proxy-ep="):]
	end := strings.Index(rest, ";")
	if end == -1 {
		end = len(rest)
	}
	proxyHost := rest[:end]
	if proxyHost == "" {
		return "", false
	}
	// proxy.xxx -> api.xxx
	apiHost := proxyHost
	if strings.HasPrefix(apiHost, "proxy.") {
		apiHost = "api." + apiHost[len("proxy."):]
	}
	return "https://" + apiHost, true
}

// GetGitHubCopilotBaseURL resolves the API base URL for a token and optional
// enterprise domain.
func GetGitHubCopilotBaseURL(token, enterpriseDomain string) string {
	if token != "" {
		if fromToken, ok := GetBaseURLFromCopilotToken(token); ok {
			return fromToken
		}
	}
	if enterpriseDomain != "" {
		return "https://copilot-api." + enterpriseDomain
	}
	return "https://api.individual.githubcopilot.com"
}

// CopilotModelCatalog is the parsed account model list.
type CopilotModelCatalog struct {
	AvailableModelIDs []string
	PolicyModelIDs    []string
}

// builtinCopilotModelIDs is the set of catalog model ids the policy fallback
// checks (upstream GITHUB_COPILOT_MODELS).
var builtinCopilotModelIDs = func() map[string]bool {
	ids := map[string]bool{}
	for _, model := range GetBuiltinModels("github-copilot") {
		ids[model.ID] = true
	}
	return ids
}()

// ParseGitHubCopilotModelCatalog parses the Copilot models response
// (upstream parseGitHubCopilotModelCatalog).
func ParseGitHubCopilotModelCatalog(raw map[string]any, allowPolicyFallback bool) (*CopilotModelCatalog, error) {
	data, ok := raw["data"].([]any)
	if !ok {
		return nil, fmt.Errorf("Invalid Copilot models response")
	}
	type accountModel struct {
		id            string
		pickerEnabled bool
		policyState   string
	}
	var accountModels []accountModel
	for _, item := range data {
		record, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, ok := record["id"].(string)
		if !ok {
			continue
		}
		if capabilities, ok := record["capabilities"].(map[string]any); ok {
			if supports, ok := capabilities["supports"].(map[string]any); ok {
				if toolCalls, present := supports["tool_calls"].(bool); present && !toolCalls {
					continue
				}
			}
		}
		state := ""
		if policy, ok := record["policy"].(map[string]any); ok {
			state, _ = policy["state"].(string)
		}
		accountModels = append(accountModels, accountModel{
			id:            id,
			pickerEnabled: record["model_picker_enabled"] == true,
			policyState:   state,
		})
	}

	var pickerModelIDs []string
	for _, model := range accountModels {
		if model.pickerEnabled && model.policyState != "disabled" {
			pickerModelIDs = append(pickerModelIDs, model.id)
		}
	}
	usePolicyFallback := allowPolicyFallback && len(pickerModelIDs) == 0
	available := pickerModelIDs
	if len(pickerModelIDs) == 0 && allowPolicyFallback {
		available = nil
		for _, model := range accountModels {
			if model.policyState == "enabled" {
				available = append(available, model.id)
			}
		}
	}

	var policyModelIDs []string
	for _, model := range accountModels {
		if model.policyState != "unconfigured" {
			continue
		}
		if !builtinCopilotModelIDs[model.id] {
			continue
		}
		if model.pickerEnabled || usePolicyFallback {
			policyModelIDs = append(policyModelIDs, model.id)
		}
	}
	return &CopilotModelCatalog{AvailableModelIDs: available, PolicyModelIDs: policyModelIDs}, nil
}

// CopilotRetryPolicy bounds the rate-limit retry loop.
type CopilotRetryPolicy struct {
	MaxRetries   int
	MaxElapsedMS int64
}

// FetchWithCopilotRateLimitRetry retries 429 responses within a budget
// (upstream fetchWithRateLimitRetry).
func FetchWithCopilotRateLimitRetry(
	ctx context.Context,
	requestURL string,
	buildRequest func(ctx context.Context) (*http.Request, error),
	policy CopilotRetryPolicy,
) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	retryBudget := time.Duration(0)
	if policy.MaxRetries > 0 && policy.MaxElapsedMS > 0 {
		retryBudget = time.Duration(policy.MaxElapsedMS) * time.Millisecond
	}
	deadline := time.Time{}
	if retryBudget > 0 {
		deadline = time.Now().Add(retryBudget)
	}

	for retry := 0; ; retry++ {
		requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		request, err := buildRequest(requestCtx)
		if err != nil {
			cancel()
			return nil, err
		}
		response, err := http.DefaultClient.Do(request)
		cancel()
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusTooManyRequests || retry == policy.MaxRetries {
			return response, nil
		}

		delay := time.Duration(500*(1<<retry)) * time.Millisecond
		if retryAfter := response.Header.Get("retry-after"); retryAfter != "" {
			if seconds, err := strconv.ParseFloat(retryAfter, 64); err == nil {
				delay = time.Duration(seconds * float64(time.Second))
			} else if when, err := http.ParseTime(retryAfter); err == nil {
				delay = time.Until(when)
			} else {
				return response, nil
			}
		}
		if delay < 0 {
			delay = 0
		}
		if !deadline.IsZero() && delay >= time.Until(deadline) {
			return response, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
		response.Body.Close()
		if err := AbortableSleep(ctx, delay, "Login cancelled"); err != nil {
			return nil, err
		}
	}
}

// FetchGitHubCopilotModels lists the account's models (upstream
// fetchGitHubCopilotModels).
func FetchGitHubCopilotModels(ctx context.Context, copilotToken, enterpriseDomain string, policy CopilotRetryPolicy) (*CopilotModelCatalog, error) {
	baseURL := githubCopilotBaseURLBuilder(copilotToken, enterpriseDomain)
	// Some Individual accounts report false for every picker flag despite
	// explicit enabled policies, so the fallback is limited to that endpoint.
	allowPolicyFallback := baseURL == "https://api.individual.githubcopilot.com"
	response, err := FetchWithCopilotRateLimitRetry(ctx, baseURL+"/models", func(requestCtx context.Context) (*http.Request, error) {
		request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, baseURL+"/models", nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Authorization", "Bearer "+copilotToken)
		for name, value := range GitHubCopilotHeaders {
			request.Header.Set(name, value)
		}
		request.Header.Set("X-GitHub-Api-Version", CopilotAPIVersion)
		return request, nil
	}, policy)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode >= 400 {
		return nil, fmt.Errorf("%d %s: %s", response.StatusCode, http.StatusText(response.StatusCode), string(raw))
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("Invalid Copilot models response")
	}
	return ParseGitHubCopilotModelCatalog(payload, allowPolicyFallback)
}

// GitHubDeviceCodeResponse is one device authorization.
type GitHubDeviceCodeResponse struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	IntervalSeconds *int
	ExpiresIn       int
}

// StartGitHubDeviceFlow starts the GitHub device authorization.
func StartGitHubDeviceFlow(ctx context.Context, domain string) (*GitHubDeviceCodeResponse, error) {
	urls := githubCopilotURLsBuilder(domain)
	form := url.Values{}
	form.Set("client_id", GitHubCopilotClientID)
	form.Set("scope", "read:user")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, urls.DeviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", "GitHubCopilotChat/0.35.0")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode >= 400 {
		return nil, fmt.Errorf("%d %s: %s", response.StatusCode, http.StatusText(response.StatusCode), string(raw))
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("Invalid device code response")
	}
	deviceCode, _ := data["device_code"].(string)
	userCode, _ := data["user_code"].(string)
	verificationURI, _ := data["verification_uri"].(string)
	expiresIn, hasExpiry := data["expires_in"].(float64)
	intervalRaw, hasInterval := data["interval"]
	if deviceCode == "" || userCode == "" || verificationURI == "" || !hasExpiry {
		return nil, fmt.Errorf("Invalid device code response fields")
	}
	parsedURI, err := url.Parse(verificationURI)
	if err != nil || (parsedURI.Scheme != "https" && parsedURI.Scheme != "http") {
		return nil, fmt.Errorf("Untrusted verification_uri in device code response")
	}
	device := &GitHubDeviceCodeResponse{
		DeviceCode: deviceCode, UserCode: userCode, VerificationURI: parsedURI.String(),
		ExpiresIn: int(expiresIn),
	}
	if hasInterval {
		interval, ok := intervalRaw.(float64)
		if !ok {
			return nil, fmt.Errorf("Invalid device code response fields")
		}
		seconds := int(interval)
		device.IntervalSeconds = &seconds
	}
	return device, nil
}

// PollForGitHubAccessToken polls the GitHub access-token endpoint.
func PollForGitHubAccessToken(ctx context.Context, domain string, device *GitHubDeviceCodeResponse) (string, error) {
	urls := githubCopilotURLsBuilder(domain)
	expiresIn := device.ExpiresIn
	return PollOAuthDeviceCodeFlow(OAuthDeviceCodePollOptions[string]{
		IntervalSeconds:     device.IntervalSeconds,
		ExpiresInSeconds:    &expiresIn,
		WaitBeforeFirstPoll: true,
		Ctx:                 ctx,
		Poll: func() (OAuthDeviceCodePollResult[string], error) {
			form := url.Values{}
			form.Set("client_id", GitHubCopilotClientID)
			form.Set("device_code", device.DeviceCode)
			form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, urls.AccessTokenURL, strings.NewReader(form.Encode()))
			if err != nil {
				return OAuthDeviceCodePollResult[string]{Status: "failed", Message: err.Error()}, nil
			}
			request.Header.Set("Accept", "application/json")
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("User-Agent", "GitHubCopilotChat/0.35.0")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				return OAuthDeviceCodePollResult[string]{Status: "failed", Message: err.Error()}, nil
			}
			defer response.Body.Close()
			raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
			var payload map[string]any
			if err := json.Unmarshal(raw, &payload); err != nil {
				return OAuthDeviceCodePollResult[string]{Status: "failed", Message: "Invalid device token response"}, nil
			}
			if accessToken, ok := payload["access_token"].(string); ok && accessToken != "" {
				return OAuthDeviceCodePollResult[string]{Status: "complete", Value: accessToken}, nil
			}
			if errorCode, ok := payload["error"].(string); ok {
				description, _ := payload["error_description"].(string)
				switch errorCode {
				case "authorization_pending":
					return OAuthDeviceCodePollResult[string]{Status: "pending"}, nil
				case "slow_down":
					result := OAuthDeviceCodePollResult[string]{Status: "slow_down"}
					if interval, ok := payload["interval"].(float64); ok {
						seconds := int(interval)
						result.IntervalSeconds = &seconds
					}
					return result, nil
				default:
					suffix := ""
					if description != "" {
						suffix = ": " + description
					}
					return OAuthDeviceCodePollResult[string]{
						Status: "failed", Message: "Device flow failed: " + errorCode + suffix,
					}, nil
				}
			}
			return OAuthDeviceCodePollResult[string]{Status: "failed", Message: "Invalid device token response"}, nil
		},
	})
}

// RefreshGitHubCopilotAccessToken exchanges a GitHub token for a Copilot token
// (upstream refreshGitHubCopilotAccessToken).
func RefreshGitHubCopilotAccessToken(ctx context.Context, refreshToken, enterpriseDomain string) (*OAuthCredential, error) {
	domain := enterpriseDomain
	if domain == "" {
		domain = "github.com"
	}
	urls := githubCopilotURLsBuilder(domain)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, urls.CopilotTokenURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+refreshToken)
	for name, value := range GitHubCopilotHeaders {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode >= 400 {
		return nil, fmt.Errorf("%d %s: %s", response.StatusCode, http.StatusText(response.StatusCode), string(raw))
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("Invalid Copilot token response")
	}
	token, _ := payload["token"].(string)
	expiresAt, hasExpiry := payload["expires_at"].(float64)
	if token == "" || !hasExpiry {
		return nil, fmt.Errorf("Invalid Copilot token response fields")
	}
	extra := map[string]json.RawMessage{CopilotEnterpriseURLKey: jsonRawString(enterpriseDomain)}
	return &OAuthCredential{OAuthCredentials: OAuthCredentials{
		Refresh: refreshToken,
		Access:  token,
		Expires: int64(expiresAt)*1000 - 5*60*1000,
		Extra:   extra,
	}}, nil
}

// RefreshGitHubCopilotToken refreshes the Copilot token and its model list.
func RefreshGitHubCopilotToken(ctx context.Context, refreshToken, enterpriseDomain string) (*OAuthCredential, error) {
	credentials, err := RefreshGitHubCopilotAccessToken(ctx, refreshToken, enterpriseDomain)
	if err != nil {
		return nil, err
	}
	catalog, err := FetchGitHubCopilotModels(ctx, credentials.Access, enterpriseDomain, CopilotRetryPolicy{})
	if err != nil {
		return nil, err
	}
	if credentials.Extra == nil {
		credentials.Extra = map[string]json.RawMessage{}
	}
	credentials.Extra[CopilotAvailableModelKey] = jsonRawStringSlice(catalog.AvailableModelIDs)
	return credentials, nil
}

// EnableGitHubCopilotModel enables one model for the account (upstream
// enableGitHubCopilotModel).
func EnableGitHubCopilotModel(ctx context.Context, token, modelID, enterpriseDomain string) (bool, error) {
	baseURL := githubCopilotBaseURLBuilder(token, enterpriseDomain)
	response, err := FetchWithCopilotRateLimitRetry(ctx, baseURL+"/models/"+modelID+"/policy",
		func(requestCtx context.Context) (*http.Request, error) {
			request, err := http.NewRequestWithContext(requestCtx, http.MethodPost,
				baseURL+"/models/"+modelID+"/policy", strings.NewReader(`{"state":"enabled"}`))
			if err != nil {
				return nil, err
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+token)
			for name, value := range GitHubCopilotHeaders {
				request.Header.Set(name, value)
			}
			request.Header.Set("openai-intent", "chat-policy")
			request.Header.Set("x-interaction-type", "chat-policy")
			return request, nil
		}, CopilotRetryPolicy{MaxRetries: 2, MaxElapsedMS: 5000})
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return false, err
		}
		return false, nil
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusTooManyRequests {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		return false, fmt.Errorf("%d %s: %s", response.StatusCode, http.StatusText(response.StatusCode), string(raw))
	}
	return response.StatusCode < 400, nil
}

// EnableGitHubCopilotModels enables the requested models, stopping at the first
// failure (upstream enableGitHubCopilotModels).
func EnableGitHubCopilotModels(ctx context.Context, token string, modelIDs []string, enterpriseDomain string) ([]string, error) {
	var enabled []string
	for _, modelID := range modelIDs {
		ok, err := EnableGitHubCopilotModel(ctx, token, modelID, enterpriseDomain)
		if err != nil {
			if ctx != nil && ctx.Err() != nil {
				return enabled, err
			}
			break
		}
		if ok {
			enabled = append(enabled, modelID)
		}
	}
	return enabled, nil
}

// LoginGitHubCopilot runs the Copilot login (upstream loginGitHubCopilot).
func LoginGitHubCopilot(interaction *AuthInteraction) (*OAuthCredential, error) {
	if interaction == nil {
		return nil, fmt.Errorf("auth interaction is required")
	}
	ctx := interaction.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	input, err := interaction.Prompt(AuthPrompt{
		Type:        AuthPromptText,
		Message:     "GitHub Enterprise URL/domain (blank for github.com)",
		Placeholder: "company.ghe.com",
	})
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("Login cancelled")
	}
	trimmed := strings.TrimSpace(input)
	enterpriseDomain, valid := NormalizeGitHubDomain(input)
	if trimmed != "" && !valid {
		return nil, fmt.Errorf("Invalid GitHub Enterprise URL/domain")
	}
	domain := enterpriseDomain
	if domain == "" {
		domain = "github.com"
	}

	device, err := StartGitHubDeviceFlow(ctx, domain)
	if err != nil {
		return nil, err
	}
	if interaction.Notify != nil {
		event := AuthEvent{
			Type: AuthEventDeviceCode, UserCode: device.UserCode,
			VerificationURI: device.VerificationURI, ExpiresSeconds: device.ExpiresIn,
		}
		if device.IntervalSeconds != nil {
			event.IntervalSeconds = *device.IntervalSeconds
		}
		interaction.Notify(event)
	}

	githubAccessToken, err := PollForGitHubAccessToken(ctx, domain, device)
	if err != nil {
		return nil, err
	}
	credentials, err := RefreshGitHubCopilotAccessToken(ctx, githubAccessToken, enterpriseDomain)
	if err != nil {
		return nil, err
	}
	catalog, err := FetchGitHubCopilotModels(ctx, credentials.Access, enterpriseDomain,
		CopilotRetryPolicy{MaxRetries: 2, MaxElapsedMS: 5000})
	if err != nil {
		return nil, err
	}
	var enabledModelIDs []string
	if len(catalog.PolicyModelIDs) > 0 {
		if interaction.Notify != nil {
			interaction.Notify(AuthEvent{Type: AuthEventProgress, Message: "Enabling models..."})
		}
		enabledModelIDs, _ = EnableGitHubCopilotModels(ctx, credentials.Access, catalog.PolicyModelIDs, enterpriseDomain)
	}
	merged := append([]string{}, catalog.AvailableModelIDs...)
	for _, modelID := range enabledModelIDs {
		if !containsString(merged, modelID) {
			merged = append(merged, modelID)
		}
	}
	if credentials.Extra == nil {
		credentials.Extra = map[string]json.RawMessage{}
	}
	credentials.Extra[CopilotAvailableModelKey] = jsonRawStringSlice(merged)
	if enterpriseDomain != "" {
		credentials.Extra[CopilotEnterpriseURLKey] = jsonRawString(enterpriseDomain)
	}
	return credentials, nil
}

// GitHubCopilotOAuth is the Copilot OAuth auth definition (upstream
// githubCopilotOAuth).
func GitHubCopilotOAuth() *OAuthAuth {
	return &OAuthAuth{
		Name:           "GitHub Copilot",
		IsSubscription: true,
		LoginLabel:     "Sign in with GitHub Copilot",
		Login:          LoginGitHubCopilot,
		Refresh: func(credential *OAuthCredential, ctx context.Context) (*OAuthCredential, error) {
			if credential == nil {
				return nil, fmt.Errorf("credential is required")
			}
			return RefreshGitHubCopilotToken(ctx, credential.Refresh, copilotEnterpriseDomain(credential))
		},
		ToAuth: func(credential *OAuthCredential) (*ModelAuth, error) {
			if credential == nil {
				return nil, fmt.Errorf("credential is required")
			}
			return &ModelAuth{
				APIKey:  credential.Access,
				BaseURL: GetGitHubCopilotBaseURL(credential.Access, copilotEnterpriseDomain(credential)),
			}, nil
		},
	}
}

// copilotEnterpriseDomain reads the enterprise domain from a credential.
func copilotEnterpriseDomain(credential *OAuthCredential) string {
	if credential == nil || credential.Extra == nil {
		return ""
	}
	raw, ok := credential.Extra[CopilotEnterpriseURLKey]
	if !ok {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" {
		return ""
	}
	domain, ok := NormalizeGitHubDomain(value)
	if !ok {
		return ""
	}
	return domain
}

func jsonRawString(value string) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return encoded
}

func jsonRawStringSlice(values []string) json.RawMessage {
	encoded, err := json.Marshal(values)
	if err != nil {
		return json.RawMessage(`[]`)
	}
	return encoded
}

// decodeBase64Secret decodes an embedded base64 secret (upstream `decode`).
func decodeBase64Secret(encoded string) string {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return ""
	}
	return string(decoded)
}
