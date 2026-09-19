package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Port of auth/oauth/openai-codex.ts: the OpenAI Codex (ChatGPT) OAuth flow,
// with both the browser (loopback) and device-code login methods.

const (
	OpenAICodexClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	OpenAICodexAuthBaseURL  = "https://auth.openai.com"
	OpenAICodexRedirectURI  = "http://localhost:1455/auth/callback"
	OpenAICodexScope        = "openid profile email offline_access"
	OpenAICodexJWTPath      = "https://api.openai.com/auth"
	OpenAICodexDeviceTTLSec = 15 * 60
	// OpenAICodexCallbackPort is the fixed loopback port of the browser flow.
	OpenAICodexCallbackPort = 1455
	// Login method ids for the select prompt.
	OpenAICodexBrowserLogin    = "browser"
	OpenAICodexDeviceCodeLogin = "device_code"
)

// openAICodexDeviceRedirectURI is the redirect used with device-code logins.
const openAICodexDeviceRedirectURI = OpenAICodexAuthBaseURL + "/deviceauth/callback"

// The auth endpoints are variables so tests can point them at a local server
// (upstream keeps constants and mocks fetch).
var (
	openAICodexAuthorizeURL      = OpenAICodexAuthBaseURL + "/oauth/authorize"
	openAICodexTokenURL          = OpenAICodexAuthBaseURL + "/oauth/token"
	openAICodexDeviceUserCodeURL = OpenAICodexAuthBaseURL + "/api/accounts/deviceauth/usercode"
	openAICodexDeviceTokenURL    = OpenAICodexAuthBaseURL + "/api/accounts/deviceauth/token"
	openAICodexDeviceVerifyURL   = OpenAICodexAuthBaseURL + "/codex/device"
)

// OpenAICodexDeviceVerificationURI is the device verification page.
func OpenAICodexDeviceVerificationURI() string { return openAICodexDeviceVerifyURL }

// OpenAICodexTokenResponse is one token endpoint response.
type OpenAICodexTokenResponse struct {
	Access  string
	Refresh string
	Expires int64
}

// ParseOpenAICodexAuthorizationInput extracts the code/state from a pasted
// redirect URL, `code#state`, a query string, or a bare code.
func ParseOpenAICodexAuthorizationInput(input string) OAuthAuthorizationInput {
	return ParseAuthorizationInput(input)
}

// decodeJWT decodes a JWT payload using the WHATWG forgiving base64 alphabet
// (upstream atob), returning nil when the token is not a JWT.
func decodeJWT(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload := parts[1]
	// WHATWG forgiving-base64 accepts both alphabets and omits padding.
	normalized := strings.NewReplacer("-", "+", "_", "/").Replace(payload)
	if remainder := len(normalized) % 4; remainder != 0 {
		normalized += strings.Repeat("=", 4-remainder)
	}
	decoded, err := base64.StdEncoding.DecodeString(normalized)
	if err != nil {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(decoded, &parsed); err != nil {
		return nil
	}
	return parsed
}

// GetOpenAICodexAccountID extracts the ChatGPT account id from an access token
// (upstream getAccountId).
func GetOpenAICodexAccountID(accessToken string) string {
	payload := decodeJWT(accessToken)
	if payload == nil {
		return ""
	}
	auth, ok := payload[OpenAICodexJWTPath].(map[string]any)
	if !ok {
		return ""
	}
	accountID, _ := auth["chatgpt_account_id"].(string)
	if accountID == "" {
		return ""
	}
	return accountID
}

// ReadOpenAICodexTokenResponse decodes a token endpoint response.
func ReadOpenAICodexTokenResponse(response *http.Response, operation string) (*OpenAICodexTokenResponse, error) {
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode >= 400 {
		text := string(raw)
		if text == "" {
			text = response.Status
		}
		return nil, fmt.Errorf("OpenAI Codex token %s failed (%d): %s", operation, response.StatusCode, text)
	}
	var payload struct {
		AccessToken  string   `json:"access_token"`
		RefreshToken string   `json:"refresh_token"`
		ExpiresIn    *float64 `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.AccessToken == "" ||
		payload.RefreshToken == "" || payload.ExpiresIn == nil {
		var generic any
		_ = json.Unmarshal(raw, &generic)
		encoded, _ := json.Marshal(generic)
		return nil, fmt.Errorf("OpenAI Codex token %s response missing fields: %s", operation, encoded)
	}
	return &OpenAICodexTokenResponse{
		Access:  payload.AccessToken,
		Refresh: payload.RefreshToken,
		Expires: time.Now().UnixMilli() + int64(*payload.ExpiresIn)*1000,
	}, nil
}

func openAICodexPostForm(ctx context.Context, requestURL string, fields map[string]string) (*http.Response, error) {
	form := url.Values{}
	for key, value := range fields {
		form.Set(key, value)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return nil, fmt.Errorf("Login cancelled")
		}
		return nil, err
	}
	return response, nil
}

// ExchangeOpenAICodexAuthorizationCode exchanges a code for tokens.
func ExchangeOpenAICodexAuthorizationCode(ctx context.Context, code, verifier, redirectURI string) (*OpenAICodexTokenResponse, error) {
	response, err := openAICodexPostForm(ctx, openAICodexTokenURL, map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     OpenAICodexClientID,
		"code":          code,
		"code_verifier": verifier,
		"redirect_uri":  redirectURI,
	})
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	return ReadOpenAICodexTokenResponse(response, "exchange")
}

// RefreshOpenAICodexAccessToken refreshes the Codex access token.
func RefreshOpenAICodexAccessToken(ctx context.Context, refreshToken string) (*OpenAICodexTokenResponse, error) {
	response, err := openAICodexPostForm(ctx, openAICodexTokenURL, map[string]string{
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"client_id":     OpenAICodexClientID,
	})
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return nil, fmt.Errorf("OpenAI Codex token refresh error: Login cancelled")
		}
		return nil, fmt.Errorf("OpenAI Codex token refresh error: %s", err.Error())
	}
	defer response.Body.Close()
	return ReadOpenAICodexTokenResponse(response, "refresh")
}

// OpenAICodexDeviceAuthInfo is one device authorization.
type OpenAICodexDeviceAuthInfo struct {
	DeviceAuthID    string
	UserCode        string
	IntervalSeconds int
}

// StartOpenAICodexDeviceAuth starts the Codex device authorization.
func StartOpenAICodexDeviceAuth(ctx context.Context) (*OpenAICodexDeviceAuthInfo, error) {
	body := strings.NewReader(`{"client_id":"` + OpenAICodexClientID + `"}`)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, openAICodexDeviceUserCodeURL, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return nil, fmt.Errorf("Login cancelled")
		}
		return nil, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode >= 400 {
		if response.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("OpenAI Codex device code login is not enabled for this server. " +
				"Use browser login or verify the server URL.")
		}
		message := fmt.Sprintf("OpenAI Codex device code request failed with status %d", response.StatusCode)
		if len(raw) > 0 {
			message += ": " + string(raw)
		}
		return nil, fmt.Errorf("%s", message)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		var generic any
		_ = json.Unmarshal(raw, &generic)
		encoded, _ := json.Marshal(generic)
		return nil, fmt.Errorf("Invalid OpenAI Codex device code response: %s", encoded)
	}
	deviceAuthID, _ := payload["device_auth_id"].(string)
	userCode, _ := payload["user_code"].(string)
	// The interval may arrive as a string.
	var intervalSeconds float64
	hasInterval := false
	switch typed := payload["interval"].(type) {
	case float64:
		intervalSeconds, hasInterval = typed, true
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
			intervalSeconds, hasInterval = parsed, true
		}
	}
	if deviceAuthID == "" || userCode == "" || !hasInterval || intervalSeconds < 0 {
		encoded, _ := json.Marshal(payload)
		return nil, fmt.Errorf("Invalid OpenAI Codex device code response: %s", encoded)
	}
	return &OpenAICodexDeviceAuthInfo{
		DeviceAuthID: deviceAuthID, UserCode: userCode, IntervalSeconds: int(intervalSeconds),
	}, nil
}

// OpenAICodexDeviceTokenSuccess is the device authorization result.
type OpenAICodexDeviceTokenSuccess struct {
	AuthorizationCode string
	CodeVerifier      string
}

// PollOpenAICodexDeviceAuth polls the device token endpoint.
func PollOpenAICodexDeviceAuth(ctx context.Context, device *OpenAICodexDeviceAuthInfo) (*OpenAICodexDeviceTokenSuccess, error) {
	intervalSeconds := device.IntervalSeconds
	expiresIn := OpenAICodexDeviceTTLSec
	return PollOAuthDeviceCodeFlow(OAuthDeviceCodePollOptions[*OpenAICodexDeviceTokenSuccess]{
		IntervalSeconds:  &intervalSeconds,
		ExpiresInSeconds: &expiresIn,
		Ctx:              ctx,
		Poll: func() (OAuthDeviceCodePollResult[*OpenAICodexDeviceTokenSuccess], error) {
			requestBody, _ := json.Marshal(map[string]string{
				"device_auth_id": device.DeviceAuthID,
				"user_code":      device.UserCode,
			})
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, openAICodexDeviceTokenURL, strings.NewReader(string(requestBody)))
			if err != nil {
				return OAuthDeviceCodePollResult[*OpenAICodexDeviceTokenSuccess]{Status: "failed", Message: err.Error()}, nil
			}
			request.Header.Set("Content-Type", "application/json")
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				if ctx != nil && ctx.Err() != nil {
					return OAuthDeviceCodePollResult[*OpenAICodexDeviceTokenSuccess]{
						Status: "failed", Message: "Login cancelled",
					}, nil
				}
				return OAuthDeviceCodePollResult[*OpenAICodexDeviceTokenSuccess]{Status: "failed", Message: err.Error()}, nil
			}
			defer response.Body.Close()
			raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))

			if response.StatusCode < 400 {
				var payload struct {
					AuthorizationCode string `json:"authorization_code"`
					CodeVerifier      string `json:"code_verifier"`
				}
				if err := json.Unmarshal(raw, &payload); err != nil || payload.AuthorizationCode == "" || payload.CodeVerifier == "" {
					var generic any
					_ = json.Unmarshal(raw, &generic)
					encoded, _ := json.Marshal(generic)
					return OAuthDeviceCodePollResult[*OpenAICodexDeviceTokenSuccess]{
						Status:  "failed",
						Message: fmt.Sprintf("Invalid OpenAI Codex device auth token response: %s", encoded),
					}, nil
				}
				return OAuthDeviceCodePollResult[*OpenAICodexDeviceTokenSuccess]{
					Status: "complete",
					Value:  &OpenAICodexDeviceTokenSuccess{AuthorizationCode: payload.AuthorizationCode, CodeVerifier: payload.CodeVerifier},
				}, nil
			}
			if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound {
				return OAuthDeviceCodePollResult[*OpenAICodexDeviceTokenSuccess]{Status: "pending"}, nil
			}

			// The error code may be a string or nested under `error.code`.
			var errorCode string
			var parsedError map[string]any
			if err := json.Unmarshal(raw, &parsedError); err == nil {
				switch typed := parsedError["error"].(type) {
				case string:
					errorCode = typed
				case map[string]any:
					errorCode, _ = typed["code"].(string)
				}
			}
			switch errorCode {
			case "deviceauth_authorization_pending":
				return OAuthDeviceCodePollResult[*OpenAICodexDeviceTokenSuccess]{Status: "pending"}, nil
			case "slow_down":
				return OAuthDeviceCodePollResult[*OpenAICodexDeviceTokenSuccess]{Status: "slow_down"}, nil
			}
			message := fmt.Sprintf("OpenAI Codex device auth failed with status %d", response.StatusCode)
			if len(raw) > 0 {
				message += ": " + string(raw)
			}
			return OAuthDeviceCodePollResult[*OpenAICodexDeviceTokenSuccess]{Status: "failed", Message: message}, nil
		},
	})
}

// OpenAICodexAuthorizationFlow is the browser flow's PKCE state and URL.
type OpenAICodexAuthorizationFlow struct {
	Verifier string
	State    string
	URL      string
}

// CreateOpenAICodexAuthorizationFlow builds the authorize URL and state.
func CreateOpenAICodexAuthorizationFlow(originator string) (*OpenAICodexAuthorizationFlow, error) {
	if originator == "" {
		originator = "pi"
	}
	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	state, err := randomHexState()
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(openAICodexAuthorizeURL)
	if err != nil {
		return nil, err
	}
	query := parsed.Query()
	query.Set("response_type", "code")
	query.Set("client_id", OpenAICodexClientID)
	query.Set("redirect_uri", OpenAICodexRedirectURI)
	query.Set("scope", OpenAICodexScope)
	query.Set("code_challenge", pkce.Challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("state", state)
	query.Set("id_token_add_organizations", "true")
	query.Set("codex_cli_simplified_flow", "true")
	query.Set("originator", originator)
	parsed.RawQuery = query.Encode()
	return &OpenAICodexAuthorizationFlow{Verifier: pkce.Verifier, State: state, URL: parsed.String()}, nil
}

// OpenAICodexCallbackServer is the loopback callback server. When the fixed
// port is unavailable the server reports a null wait so the manual paste path
// takes over (upstream's listen-error handling).
type OpenAICodexCallbackServer struct {
	server      *http.Server
	listener    net.Listener
	unavailable bool

	mu      sync.Mutex
	settled bool
	result  chan string
	done    chan struct{}
}

// StartOpenAICodexCallbackServer serves /auth/callback on the fixed port.
func StartOpenAICodexCallbackServer(expectedState string) *OpenAICodexCallbackServer {
	callback := &OpenAICodexCallbackServer{
		result: make(chan string, 1),
		done:   make(chan struct{}),
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", oauthCallbackHost(), OpenAICodexCallbackPort))
	if err != nil {
		callback.unavailable = true
		close(callback.done)
		return callback
	}
	callback.listener = listener
	mux := http.NewServeMux()
	mux.HandleFunc("/auth/callback", func(writer http.ResponseWriter, request *http.Request) {
		writeHTML := func(status int, body string) {
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			writer.WriteHeader(status)
			_, _ = writer.Write([]byte(body))
		}
		if request.URL.Path != "/auth/callback" {
			writeHTML(http.StatusNotFound, OAuthErrorHTML("Callback route not found.", ""))
			return
		}
		if request.URL.Query().Get("state") != expectedState {
			writeHTML(http.StatusBadRequest, OAuthErrorHTML("State mismatch.", ""))
			return
		}
		code := request.URL.Query().Get("code")
		if code == "" {
			writeHTML(http.StatusBadRequest, OAuthErrorHTML("Missing authorization code.", ""))
			return
		}
		writeHTML(http.StatusOK, OAuthSuccessHTML("OpenAI authentication completed. You can close this window."))
		callback.settle(code)
	})
	callback.server = &http.Server{Handler: mux}
	go func() { _ = callback.server.Serve(listener) }()
	return callback
}

func (s *OpenAICodexCallbackServer) settle(code string) {
	s.mu.Lock()
	if s.settled {
		s.mu.Unlock()
		return
	}
	s.settled = true
	s.mu.Unlock()
	s.result <- code
	close(s.done)
}

// CancelWait hands the login over to manual code entry.
func (s *OpenAICodexCallbackServer) CancelWait() {
	s.mu.Lock()
	if s.settled {
		s.mu.Unlock()
		return
	}
	s.settled = true
	s.mu.Unlock()
	close(s.done)
}

// WaitForCode returns the callback code; ok is false when the wait was
// cancelled or the listener was unavailable.
func (s *OpenAICodexCallbackServer) WaitForCode() (string, bool) {
	if s.unavailable {
		return "", false
	}
	<-s.done
	select {
	case code := <-s.result:
		return code, true
	default:
		return "", false
	}
}

// Close shuts the server down.
func (s *OpenAICodexCallbackServer) Close() {
	if s.server != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.server.Shutdown(shutdownCtx)
	}
}

// OpenAICodexCredentialsFromToken builds a credential, requiring the ChatGPT
// account id claim (upstream credentialsFromToken).
func OpenAICodexCredentialsFromToken(token *OpenAICodexTokenResponse) (*OAuthCredential, error) {
	accountID := GetOpenAICodexAccountID(token.Access)
	if accountID == "" {
		return nil, fmt.Errorf("Failed to extract accountId from token")
	}
	return &OAuthCredential{OAuthCredentials: OAuthCredentials{
		Access: token.Access, Refresh: token.Refresh, Expires: token.Expires,
		Extra: map[string]json.RawMessage{"accountId": jsonRawString(accountID)},
	}}, nil
}

// LoginOpenAICodexDeviceCode runs the device-code login path.
func LoginOpenAICodexDeviceCode(interaction *AuthInteraction) (*OAuthCredential, error) {
	if interaction == nil {
		return nil, fmt.Errorf("auth interaction is required")
	}
	ctx := interaction.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	device, err := StartOpenAICodexDeviceAuth(ctx)
	if err != nil {
		return nil, err
	}
	if interaction.Notify != nil {
		interaction.Notify(AuthEvent{
			Type: AuthEventDeviceCode, UserCode: device.UserCode,
			VerificationURI: openAICodexDeviceVerifyURL,
			IntervalSeconds: device.IntervalSeconds, ExpiresSeconds: OpenAICodexDeviceTTLSec,
		})
	}
	result, err := PollOpenAICodexDeviceAuth(ctx, device)
	if err != nil {
		return nil, err
	}
	token, err := ExchangeOpenAICodexAuthorizationCode(ctx, result.AuthorizationCode, result.CodeVerifier, openAICodexDeviceRedirectURI)
	if err != nil {
		return nil, err
	}
	return OpenAICodexCredentialsFromToken(token)
}

// LoginOpenAICodex runs the browser login path with the manual paste race.
func LoginOpenAICodex(interaction *AuthInteraction) (*OAuthCredential, error) {
	if interaction == nil {
		return nil, fmt.Errorf("auth interaction is required")
	}
	flow, err := CreateOpenAICodexAuthorizationFlow("pi")
	if err != nil {
		return nil, err
	}
	ctx := interaction.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	server := StartOpenAICodexCallbackServer(flow.State)
	defer server.Close()

	type manualResult struct {
		input string
		err   error
	}
	manualCh := make(chan manualResult, 1)
	go func() {
		input, perr := interaction.Prompt(AuthPrompt{
			Type:        AuthPromptManualCode,
			Message:     "Complete login in your browser, or paste the authorization code / redirect URL here:",
			Placeholder: OpenAICodexRedirectURI,
		})
		manualCh <- manualResult{input: input, err: perr}
		server.CancelWait()
	}()

	if interaction.Notify != nil {
		interaction.Notify(AuthEvent{
			Type:         AuthEventAuthURL,
			URL:          flow.URL,
			Instructions: "A browser window should open. Complete login to finish.",
		})
	}

	code := ""
	callbackCode, ok := server.WaitForCode()
	if ok && callbackCode != "" {
		code = callbackCode
	} else {
		manual := <-manualCh
		if manual.err != nil {
			return nil, manual.err
		}
		parsed := ParseOpenAICodexAuthorizationInput(manual.input)
		if parsed.State != "" && parsed.State != flow.State {
			return nil, fmt.Errorf("State mismatch")
		}
		code = parsed.Code
	}
	if code == "" {
		return nil, fmt.Errorf("Missing authorization code")
	}
	token, err := ExchangeOpenAICodexAuthorizationCode(ctx, code, flow.Verifier, OpenAICodexRedirectURI)
	if err != nil {
		return nil, err
	}
	return OpenAICodexCredentialsFromToken(token)
}

// RefreshOpenAICodexToken refreshes a Codex credential.
func RefreshOpenAICodexToken(ctx context.Context, refreshToken string) (*OAuthCredential, error) {
	token, err := RefreshOpenAICodexAccessToken(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	return OpenAICodexCredentialsFromToken(token)
}

// OpenAICodexOAuth is the Codex OAuth auth definition (upstream
// openaiCodexOAuth).
func OpenAICodexOAuth() *OAuthAuth {
	return &OAuthAuth{
		Name:           "OpenAI (ChatGPT Plus/Pro)",
		IsSubscription: true,
		LoginLabel:     "Sign in with ChatGPT",
		Login: func(interaction *AuthInteraction) (*OAuthCredential, error) {
			if interaction == nil {
				return nil, fmt.Errorf("auth interaction is required")
			}
			method, err := interaction.Prompt(AuthPrompt{
				Type:    AuthPromptSelect,
				Message: "Select OpenAI Codex login method:",
				SelectOptions: []AuthSelectOption{
					{ID: OpenAICodexBrowserLogin, Label: "Browser login (default)"},
					{ID: OpenAICodexDeviceCodeLogin, Label: "Device code login (headless)"},
				},
			})
			if err != nil {
				return nil, err
			}
			switch method {
			case OpenAICodexDeviceCodeLogin:
				return LoginOpenAICodexDeviceCode(interaction)
			case OpenAICodexBrowserLogin, "":
				return LoginOpenAICodex(interaction)
			default:
				return nil, fmt.Errorf("Unknown OpenAI Codex login method: %s", method)
			}
		},
		Refresh: func(credential *OAuthCredential, ctx context.Context) (*OAuthCredential, error) {
			if credential == nil {
				return nil, fmt.Errorf("credential is required")
			}
			return RefreshOpenAICodexToken(ctx, credential.Refresh)
		},
		ToAuth: func(credential *OAuthCredential) (*ModelAuth, error) {
			if credential == nil {
				return nil, fmt.Errorf("credential is required")
			}
			return &ModelAuth{APIKey: credential.Access}, nil
		},
	}
}

// randomHexState returns 16 random bytes as hex (upstream createState).
func randomHexState() (string, error) {
	return randomHex(16)
}
