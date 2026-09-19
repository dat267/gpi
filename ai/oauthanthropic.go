package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Port of auth/oauth/anthropic.ts: the Claude Pro/Max OAuth flow.

// AnthropicOAuthClientID is the pi OAuth client id (upstream decodes it from
// base64 at module load).
const AnthropicOAuthClientID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"

const anthropicAuthorizeURL = "https://claude.ai/oauth/authorize"

// anthropicTokenURLValue is the token endpoint; tests point it at a local
// server (upstream keeps TOKEN_URL constant and mocks fetch).
var anthropicTokenURLValue = "https://platform.claude.com/v1/oauth/token"

func anthropicTokenURL() string { return anthropicTokenURLValue }

const (
	anthropicCallbackPath = "/callback"
	anthropicScopes       = "org:create_api_key user:profile user:inference user:sessions:claude_code " +
		"user:mcp_servers user:file_upload"
	// AnthropicCallbackPort is the fixed loopback port the redirect URI uses.
	AnthropicCallbackPort = 53692
)

// AnthropicRedirectURI is the OAuth redirect URI.
const AnthropicRedirectURI = "http://localhost:53692" + anthropicCallbackPath

// AnthropicCallbackHost returns the loopback host to bind; PI_OAUTH_CALLBACK_HOST
// overrides it (upstream CALLBACK_HOST).
func AnthropicCallbackHost() string {
	if host := os.Getenv("PI_OAUTH_CALLBACK_HOST"); host != "" {
		return host
	}
	return "127.0.0.1"
}

// OAuthAuthorizationInput is the parsed manual-paste input.
type OAuthAuthorizationInput struct {
	Code  string
	State string
}

// ParseAuthorizationInput parses a pasted redirect URL, `code#state` pair, a
// query string, or a bare code (upstream parseAuthorizationInput).
func ParseAuthorizationInput(input string) OAuthAuthorizationInput {
	value := strings.TrimSpace(input)
	if value == "" {
		return OAuthAuthorizationInput{}
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return OAuthAuthorizationInput{
			Code:  parsed.Query().Get("code"),
			State: parsed.Query().Get("state"),
		}
	}
	if index := strings.Index(value, "#"); index != -1 {
		return OAuthAuthorizationInput{Code: value[:index], State: value[index+1:]}
	}
	if strings.Contains(value, "code=") {
		if values, err := url.ParseQuery(value); err == nil {
			return OAuthAuthorizationInput{Code: values.Get("code"), State: values.Get("state")}
		}
	}
	return OAuthAuthorizationInput{Code: value}
}

// FormatOAuthErrorDetails renders an error with its code/errno/cause/stack
// (upstream formatErrorDetails).
func FormatOAuthErrorDetails(err error) string {
	if err == nil {
		return ""
	}
	details := []string{fmt.Sprintf("%T: %s", err, err.Error())}
	if providerErr, ok := err.(*ProviderError); ok {
		details = append(details, fmt.Sprintf("code=%d", providerErr.Status))
	}
	if unwrapped := errors.Unwrap(err); unwrapped != nil {
		details = append(details, "cause="+FormatOAuthErrorDetails(unwrapped))
	}
	return strings.Join(details, "; ")
}

// OAuthCallbackServer is the loopback callback server (upstream
// startCallbackServer).
type OAuthCallbackServer struct {
	listener    net.Listener
	server      *http.Server
	RedirectURI string

	mu       sync.Mutex
	settled  bool
	result   chan OAuthAuthorizationInput
	finished chan struct{}
}

// StartOAuthCallbackServer binds the loopback port and serves the callback.
func StartOAuthCallbackServer(expectedState string) (*OAuthCallbackServer, error) {
	host := AnthropicCallbackHost()
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", host, AnthropicCallbackPort))
	if err != nil {
		return nil, err
	}
	callback := &OAuthCallbackServer{
		listener:    listener,
		RedirectURI: AnthropicRedirectURI,
		result:      make(chan OAuthAuthorizationInput, 1),
		finished:    make(chan struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc(anthropicCallbackPath, func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		writeHTML := func(status int, body string) {
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			writer.WriteHeader(status)
			_, _ = writer.Write([]byte(body))
		}
		if request.URL.Path != anthropicCallbackPath {
			writeHTML(http.StatusNotFound, OAuthErrorHTML("Callback route not found.", ""))
			return
		}
		if errParam := query.Get("error"); errParam != "" {
			writeHTML(http.StatusBadRequest, OAuthErrorHTML("Anthropic authentication did not complete.", "Error: "+errParam))
			return
		}
		code := query.Get("code")
		state := query.Get("state")
		if code == "" || state == "" {
			writeHTML(http.StatusBadRequest, OAuthErrorHTML("Missing code or state parameter.", ""))
			return
		}
		if state != expectedState {
			writeHTML(http.StatusBadRequest, OAuthErrorHTML("State mismatch.", ""))
			return
		}
		writeHTML(http.StatusOK, OAuthSuccessHTML("Anthropic authentication completed. You can close this window."))
		callback.settle(OAuthAuthorizationInput{Code: code, State: state})
	})
	callback.server = &http.Server{Handler: mux}
	go func() { _ = callback.server.Serve(listener) }()
	return callback, nil
}

func (s *OAuthCallbackServer) settle(value OAuthAuthorizationInput) {
	s.mu.Lock()
	if s.settled {
		s.mu.Unlock()
		return
	}
	s.settled = true
	s.mu.Unlock()
	s.result <- value
	close(s.finished)
}

// CancelWait unblocks WaitForCode without a code.
func (s *OAuthCallbackServer) CancelWait() {
	s.mu.Lock()
	if s.settled {
		s.mu.Unlock()
		return
	}
	s.settled = true
	s.mu.Unlock()
	close(s.finished)
}

// WaitForCode waits for the callback or a cancellation; ok is false when the
// wait was cancelled.
func (s *OAuthCallbackServer) WaitForCode(ctx context.Context) (OAuthAuthorizationInput, bool) {
	select {
	case value := <-s.result:
		return value, true
	case <-s.finished:
		select {
		case value := <-s.result:
			return value, true
		default:
			return OAuthAuthorizationInput{}, false
		}
	case <-ctx.Done():
		s.CancelWait()
		return OAuthAuthorizationInput{}, false
	}
}

// Close shuts the callback server down.
func (s *OAuthCallbackServer) Close() {
	if s.server != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.server.Shutdown(shutdownCtx)
	}
}

// postOAuthJSON posts a JSON body and returns the response text (upstream
// postJson).
func postOAuthJSON(ctx context.Context, requestURL string, body map[string]any) (string, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, requestURL, bytes.NewReader(encoded))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode >= 400 {
		return "", fmt.Errorf("HTTP request failed. status=%d; url=%s; body=%s", response.StatusCode, requestURL, string(raw))
	}
	return string(raw), nil
}

// ExchangeAnthropicAuthorizationCode exchanges a code for tokens
// (upstream exchangeAuthorizationCode).
func ExchangeAnthropicAuthorizationCode(ctx context.Context, code, state, verifier, redirectURI string) (*OAuthCredential, error) {
	responseBody, err := postOAuthJSON(ctx, anthropicTokenURL(), map[string]any{
		"grant_type":    "authorization_code",
		"client_id":     AnthropicOAuthClientID,
		"code":          code,
		"state":         state,
		"redirect_uri":  redirectURI,
		"code_verifier": verifier,
	})
	if err != nil {
		return nil, fmt.Errorf(
			"Token exchange request failed. url=%s; redirect_uri=%s; response_type=authorization_code; details=%s",
			anthropicTokenURL(), redirectURI, FormatOAuthErrorDetails(err))
	}
	var tokenData struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal([]byte(responseBody), &tokenData); err != nil {
		return nil, fmt.Errorf("Token exchange returned invalid JSON. url=%s; body=%s; details=%s",
			anthropicTokenURL(), responseBody, FormatOAuthErrorDetails(err))
	}
	return &OAuthCredential{OAuthCredentials: OAuthCredentials{
		Refresh: tokenData.RefreshToken,
		Access:  tokenData.AccessToken,
		Expires: time.Now().UnixMilli() + tokenData.ExpiresIn*1000 - 5*60*1000,
	}}, nil
}

// RefreshAnthropicToken refreshes an Anthropic OAuth credential (upstream
// refreshAnthropicToken).
func RefreshAnthropicToken(ctx context.Context, refreshToken string) (*OAuthCredential, error) {
	responseBody, err := postOAuthJSON(ctx, anthropicTokenURL(), map[string]any{
		"grant_type":    "refresh_token",
		"client_id":     AnthropicOAuthClientID,
		"refresh_token": refreshToken,
	})
	if err != nil {
		return nil, fmt.Errorf("Anthropic token refresh request failed. url=%s; details=%s",
			anthropicTokenURL(), FormatOAuthErrorDetails(err))
	}
	var data struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal([]byte(responseBody), &data); err != nil {
		return nil, fmt.Errorf("Anthropic token refresh returned invalid JSON. url=%s; body=%s; details=%s",
			anthropicTokenURL(), responseBody, FormatOAuthErrorDetails(err))
	}
	return &OAuthCredential{OAuthCredentials: OAuthCredentials{
		Refresh: data.RefreshToken,
		Access:  data.AccessToken,
		Expires: time.Now().UnixMilli() + data.ExpiresIn*1000 - 5*60*1000,
	}}, nil
}

// AnthropicAuthorizeURL builds the authorize URL for a PKCE challenge
// (upstream login's auth params).
func AnthropicAuthorizeURL(challenge, verifier string) string {
	params := url.Values{}
	params.Set("code", "true")
	params.Set("client_id", AnthropicOAuthClientID)
	params.Set("response_type", "code")
	params.Set("redirect_uri", AnthropicRedirectURI)
	params.Set("scope", anthropicScopes)
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("state", verifier)
	return anthropicAuthorizeURL + "?" + params.Encode()
}

// LoginAnthropic runs the interactive Anthropic OAuth flow (upstream
// loginAnthropic): it starts the loopback callback server, offers the manual
// paste path concurrently, and exchanges whichever arrives first.
func LoginAnthropic(interaction *AuthInteraction) (*OAuthCredential, error) {
	if interaction == nil {
		return nil, fmt.Errorf("auth interaction is required")
	}
	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	server, err := StartOAuthCallbackServer(pkce.Verifier)
	if err != nil {
		return nil, err
	}
	defer server.Close()

	ctx := interaction.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	manualCtx, cancelManual := context.WithCancel(ctx)
	defer cancelManual()

	type manualResult struct {
		input string
		err   error
	}
	manualCh := make(chan manualResult, 1)
	go func() {
		input, perr := interaction.Prompt(AuthPrompt{
			Type:        AuthPromptManualCode,
			Message:     "Complete login in your browser, or paste the authorization code / redirect URL here:",
			Placeholder: AnthropicRedirectURI,
		})
		manualCh <- manualResult{input: input, err: perr}
		manualCtxCancelFor(manualCtx, cancelManual)
	}()

	if interaction.Notify != nil {
		interaction.Notify(AuthEvent{
			Type: AuthEventAuthURL,
			URL:  AnthropicAuthorizeURL(pkce.Challenge, pkce.Verifier),
			Instructions: "Complete login in your browser. If the browser is on another machine, " +
				"paste the final redirect URL here.",
		})
	}

	var code, state string
	result, ok := server.WaitForCode(manualCtx)
	if ok && result.Code != "" {
		code = result.Code
		state = result.State
	} else {
		// The manual paste path: wait for the prompt to settle.
		manual := <-manualCh
		if manual.err != nil {
			return nil, manual.err
		}
		parsed := ParseAuthorizationInput(manual.input)
		if parsed.State != "" && parsed.State != pkce.Verifier {
			return nil, fmt.Errorf("OAuth state mismatch")
		}
		code = parsed.Code
		state = parsed.State
		if state == "" {
			state = pkce.Verifier
		}
	}
	if code == "" {
		return nil, fmt.Errorf("Missing authorization code")
	}
	if state == "" {
		return nil, fmt.Errorf("Missing OAuth state")
	}
	if interaction.Notify != nil {
		interaction.Notify(AuthEvent{Type: AuthEventProgress, Message: "Exchanging authorization code for tokens..."})
	}
	return ExchangeAnthropicAuthorizationCode(ctx, code, state, pkce.Verifier, AnthropicRedirectURI)
}

// manualCtxCancelFor cancels the manual wait once the prompt settled.
func manualCtxCancelFor(_ context.Context, cancel context.CancelFunc) { cancel() }

// AnthropicOAuth is the Anthropic OAuth auth definition (upstream
// anthropicOAuth).
func AnthropicOAuth() *OAuthAuth {
	return &OAuthAuth{
		Name:           "Anthropic (Claude Pro/Max)",
		IsSubscription: true,
		LoginLabel:     "Sign in with Claude Pro/Max",
		Login:          LoginAnthropic,
		Refresh: func(credential *OAuthCredential, ctx context.Context) (*OAuthCredential, error) {
			if credential == nil {
				return nil, fmt.Errorf("credential is required")
			}
			return RefreshAnthropicToken(ctx, credential.Refresh)
		},
		ToAuth: func(credential *OAuthCredential) (*ModelAuth, error) {
			if credential == nil {
				return nil, fmt.Errorf("credential is required")
			}
			return &ModelAuth{APIKey: credential.Access}, nil
		},
	}
}
