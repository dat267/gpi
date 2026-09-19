package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Port of auth/oauth/radius.ts: the Radius gateway OAuth flow (browser and
// device-code sign-in against a configured gateway).

const (
	RadiusCallbackPort  = 1456
	RadiusCallbackPath  = "/oauth/callback"
	radiusCallbackHost  = "127.0.0.1"
	radiusTokenSkewMS   = 60_000
	RadiusLoginBrowser  = "browser"
	RadiusLoginDevice   = "device-code"
	RadiusOAuthClientID = "pi-gateway"
	RadiusOAuthScope    = "gateway offline_access"
	radiusDeviceGrant   = "urn:ietf:params:oauth:grant-type:device_code"
)

// RadiusRedirectURI is the OAuth redirect URI.
const RadiusRedirectURI = "http://" + radiusCallbackHost + ":1456" + RadiusCallbackPath

// RadiusOAuthOptions configure the Radius OAuth flow.
type RadiusOAuthOptions struct {
	Name    string
	Gateway string
}

// RadiusOAuthDiscovery is the gateway's discovery document.
type RadiusOAuthDiscovery struct {
	AuthorizationEndpoint string
}

// radiusOAuthResponseError carries the parsed OAuth error of a failed response.
type radiusOAuthResponseError struct {
	Status       int
	OAuthError   string
	ErrorMessage string
}

func (e *radiusOAuthResponseError) Error() string { return e.ErrorMessage }

// LoadRadiusOAuthDiscovery fetches the gateway's OAuth discovery document.
func LoadRadiusOAuthDiscovery(ctx context.Context, gateway string) (*RadiusOAuthDiscovery, error) {
	requestURL := strings.TrimRight(gateway, "/") + "/v1/oauth"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("accept", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode >= 400 {
		return nil, fmt.Errorf("Could not load Radius OAuth config from %s: %d %s", gateway, response.StatusCode, string(raw))
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("Invalid Radius OAuth config from %s", gateway)
	}
	endpoint, ok := parsed["authorizationEndpoint"].(string)
	if !ok {
		return nil, fmt.Errorf("Invalid Radius OAuth config from %s", gateway)
	}
	return &RadiusOAuthDiscovery{AuthorizationEndpoint: endpoint}, nil
}

// readRadiusOAuthError parses a failed response into a coded error.
func readRadiusOAuthError(response *http.Response, message string) error {
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	oauthError := ""
	description := ""
	if text := strings.TrimSpace(string(raw)); text != "" {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(text), &parsed); err == nil {
			oauthError, _ = parsed["error"].(string)
			description, _ = parsed["error_description"].(string)
		} else {
			description = text
		}
	}
	detail := ""
	switch {
	case oauthError != "" && description != "":
		detail = oauthError + ": " + description
	case oauthError != "":
		detail = oauthError
	case description != "":
		detail = description
	default:
		detail = fmt.Sprintf("%d", response.StatusCode)
	}
	return &radiusOAuthResponseError{
		Status:       response.StatusCode,
		OAuthError:   oauthError,
		ErrorMessage: message + ": " + detail,
	}
}

// RequestRadiusOAuthToken posts a token request to the gateway.
func RequestRadiusOAuthToken(ctx context.Context, gateway string, fields map[string]string) (*OAuthCredential, error) {
	form := url.Values{}
	for key, value := range fields {
		form.Set(key, value)
	}
	requestURL := strings.TrimRight(gateway, "/") + "/v1/oauth/token"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("accept", "application/json")
	request.Header.Set("content-type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return nil, fmt.Errorf("Login cancelled")
		}
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return nil, readRadiusOAuthError(response, "Radius OAuth token request failed")
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	var payload struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	extra := map[string]json.RawMessage{}
	if payload.Scope != "" {
		extra["scope"] = jsonRawString(payload.Scope)
	}
	return &OAuthCredential{OAuthCredentials: OAuthCredentials{
		Access:  payload.AccessToken,
		Refresh: payload.RefreshToken,
		Expires: time.Now().UnixMilli() + payload.ExpiresIn*1000 - radiusTokenSkewMS,
		Extra:   extra,
	}}, nil
}

// RadiusCallbackServer is the Radius loopback callback server. When the fixed
// port is unavailable the wait reports no code so the caller errors.
type RadiusCallbackServer struct {
	server      *http.Server
	unavailable bool

	mu      sync.Mutex
	settled bool
	done    chan struct{}
	code    string
}

// StartRadiusCallbackServer serves the fixed-port OAuth callback.
func StartRadiusCallbackServer(expectedState string, ctx context.Context) *RadiusCallbackServer {
	callback := &RadiusCallbackServer{done: make(chan struct{})}
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", radiusCallbackHost, RadiusCallbackPort))
	if err != nil {
		callback.unavailable = true
		close(callback.done)
		return callback
	}
	if ctx != nil {
		context.AfterFunc(ctx, func() { callback.finish("") })
	}
	mux := http.NewServeMux()
	mux.HandleFunc(RadiusCallbackPath, func(writer http.ResponseWriter, request *http.Request) {
		sendPage := func(status int, html string) {
			writer.Header().Set("content-type", "text/html; charset=utf-8")
			writer.WriteHeader(status)
			_, _ = writer.Write([]byte(html))
		}
		query := request.URL.Query()
		if request.URL.Path != RadiusCallbackPath {
			sendPage(http.StatusNotFound, OAuthErrorHTML("Callback route not found.", ""))
			return
		}
		if query.Get("state") != expectedState {
			sendPage(http.StatusBadRequest, OAuthErrorHTML("OAuth state mismatch.", ""))
			return
		}
		if oauthError := query.Get("error"); oauthError != "" {
			description := query.Get("error_description")
			if description == "" {
				description = oauthError
			}
			sendPage(http.StatusBadRequest, OAuthErrorHTML(description, ""))
			callback.finish("")
			return
		}
		code := query.Get("code")
		if code == "" {
			sendPage(http.StatusBadRequest, OAuthErrorHTML("Missing authorization code.", ""))
			return
		}
		sendPage(http.StatusOK, OAuthSuccessHTML("Signed in to Radius. You may now close this page."))
		callback.finish(code)
	})
	callback.server = &http.Server{Handler: mux}
	go func() { _ = callback.server.Serve(listener) }()
	return callback
}

func (s *RadiusCallbackServer) finish(code string) {
	s.mu.Lock()
	if s.settled {
		s.mu.Unlock()
		return
	}
	s.settled = true
	s.code = code
	s.mu.Unlock()
	close(s.done)
}

// WaitForCode waits for the callback; ok is false when unavailable or
// cancelled.
func (s *RadiusCallbackServer) WaitForCode() (string, bool) {
	if s.unavailable {
		return "", false
	}
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.code == "" {
		return "", false
	}
	return s.code, true
}

// Close releases the server.
func (s *RadiusCallbackServer) Close() {
	s.finish("")
	if s.server != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.server.Shutdown(shutdownCtx)
	}
}

// LoginRadiusWithBrowser runs the browser sign-in (upstream loginWithBrowser).
func LoginRadiusWithBrowser(interaction *AuthInteraction, gateway, authorizationEndpoint string) (*OAuthCredential, error) {
	if interaction == nil {
		return nil, fmt.Errorf("auth interaction is required")
	}
	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	state := randomUUIDString()
	parsed, err := url.Parse(authorizationEndpoint)
	if err != nil {
		return nil, err
	}
	query := parsed.Query()
	query.Set("response_type", "code")
	query.Set("client_id", RadiusOAuthClientID)
	query.Set("redirect_uri", RadiusRedirectURI)
	query.Set("scope", RadiusOAuthScope)
	query.Set("code_challenge", pkce.Challenge)
	query.Set("code_challenge_method", "S256")
	query.Set("handoff", "url")
	query.Set("state", state)
	parsed.RawQuery = query.Encode()

	ctx := interaction.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	callbackServer := StartRadiusCallbackServer(state, ctx)
	if interaction.Notify != nil {
		interaction.Notify(AuthEvent{
			Type:    AuthEventProgress,
			Message: "Listening for OAuth callback on " + RadiusRedirectURI,
		})
		interaction.Notify(AuthEvent{
			Type:         AuthEventAuthURL,
			URL:          parsed.String(),
			Instructions: "Continue in your browser.",
		})
	}
	defer callbackServer.Close()

	code, ok := callbackServer.WaitForCode()
	if !ok {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("Login cancelled")
		}
		return nil, fmt.Errorf("OAuth callback did not complete.")
	}
	return RequestRadiusOAuthToken(ctx, gateway, map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     RadiusOAuthClientID,
		"redirect_uri":  RadiusRedirectURI,
		"code":          code,
		"code_verifier": pkce.Verifier,
	})
}

// RadiusDeviceAuthorization is one device authorization response.
type RadiusDeviceAuthorization struct {
	DeviceCode      string
	UserCode        string
	VerificationURI string
	ExpiresIn       int
	IntervalSeconds *int
}

// RequestRadiusDeviceAuthorization starts the device authorization.
func RequestRadiusDeviceAuthorization(ctx context.Context, gateway string) (*RadiusDeviceAuthorization, error) {
	form := url.Values{}
	form.Set("client_id", RadiusOAuthClientID)
	form.Set("scope", RadiusOAuthScope)
	requestURL := strings.TrimRight(gateway, "/") + "/v1/oauth/device"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	request.Header.Set("accept", "application/json")
	request.Header.Set("content-type", "application/x-www-form-urlencoded")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return nil, fmt.Errorf("Login cancelled")
		}
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return nil, readRadiusOAuthError(response, "Radius OAuth device authorization failed")
	}
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	deviceCode, _ := payload["device_code"].(string)
	userCode, _ := payload["user_code"].(string)
	verificationURI, _ := payload["verification_uri"].(string)
	expiresIn, hasExpiry := payload["expires_in"].(float64)
	if deviceCode == "" || userCode == "" || verificationURI == "" || !hasExpiry || expiresIn == 0 {
		return nil, fmt.Errorf("Radius OAuth device authorization response is missing required fields")
	}
	device := &RadiusDeviceAuthorization{
		DeviceCode: deviceCode, UserCode: userCode,
		VerificationURI: verificationURI, ExpiresIn: int(expiresIn),
	}
	if interval, ok := payload["interval"].(float64); ok {
		seconds := int(interval)
		device.IntervalSeconds = &seconds
	}
	return device, nil
}

// LoginRadiusWithDeviceCode runs the device-code sign-in.
func LoginRadiusWithDeviceCode(interaction *AuthInteraction, gateway string) (*OAuthCredential, error) {
	if interaction == nil {
		return nil, fmt.Errorf("auth interaction is required")
	}
	ctx := interaction.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	device, err := RequestRadiusDeviceAuthorization(ctx, gateway)
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
	expiresIn := device.ExpiresIn
	return PollOAuthDeviceCodeFlow(OAuthDeviceCodePollOptions[*OAuthCredential]{
		IntervalSeconds:  device.IntervalSeconds,
		ExpiresInSeconds: &expiresIn,
		Ctx:              ctx,
		Poll: func() (OAuthDeviceCodePollResult[*OAuthCredential], error) {
			credentials, err := RequestRadiusOAuthToken(ctx, gateway, map[string]string{
				"grant_type":  radiusDeviceGrant,
				"client_id":   RadiusOAuthClientID,
				"device_code": device.DeviceCode,
			})
			if err == nil {
				return OAuthDeviceCodePollResult[*OAuthCredential]{Status: "complete", Value: credentials}, nil
			}
			var responseErr *radiusOAuthResponseError
			if !asError(err, &responseErr) {
				return OAuthDeviceCodePollResult[*OAuthCredential]{Status: "failed", Message: err.Error()}, nil
			}
			switch responseErr.OAuthError {
			case "authorization_pending":
				return OAuthDeviceCodePollResult[*OAuthCredential]{Status: "pending"}, nil
			case "slow_down":
				return OAuthDeviceCodePollResult[*OAuthCredential]{Status: "slow_down"}, nil
			case "expired_token":
				return OAuthDeviceCodePollResult[*OAuthCredential]{
					Status: "failed", Message: "Device authorization expired.",
				}, nil
			case "access_denied":
				return OAuthDeviceCodePollResult[*OAuthCredential]{
					Status: "failed", Message: "Device authorization was denied.",
				}, nil
			default:
				return OAuthDeviceCodePollResult[*OAuthCredential]{Status: "failed", Message: err.Error()}, nil
			}
		},
	})
}

// asError is a small errors.As wrapper kept local to avoid an import cycle in
// this file's helper set.
func asError(err error, target **radiusOAuthResponseError) bool {
	for err != nil {
		if typed, ok := err.(*radiusOAuthResponseError); ok {
			*target = typed
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// CreateRadiusOAuth builds the Radius OAuth auth definition (upstream
// createRadiusOAuth).
func CreateRadiusOAuth(options RadiusOAuthOptions) *OAuthAuth {
	gateway := NormalizeRadiusGatewayURL(options.Gateway)
	return &OAuthAuth{
		Name:       options.Name,
		LoginLabel: "Sign in with " + options.Name,
		Login: func(interaction *AuthInteraction) (*OAuthCredential, error) {
			if interaction == nil {
				return nil, fmt.Errorf("auth interaction is required")
			}
			method, err := interaction.Prompt(AuthPrompt{
				Type:    AuthPromptSelect,
				Message: "Sign in to " + options.Name + ":",
				SelectOptions: []AuthSelectOption{
					{ID: RadiusLoginBrowser, Label: "Sign in with browser (recommended)"},
					{ID: RadiusLoginDevice, Label: "Sign in with device code (when signing in from another device)"},
				},
			})
			if err != nil {
				return nil, err
			}
			switch method {
			case RadiusLoginDevice:
				return LoginRadiusWithDeviceCode(interaction, gateway)
			case RadiusLoginBrowser, "":
				ctx := interaction.Ctx
				if ctx == nil {
					ctx = context.Background()
				}
				discovery, err := LoadRadiusOAuthDiscovery(ctx, gateway)
				if err != nil {
					return nil, err
				}
				return LoginRadiusWithBrowser(interaction, gateway, discovery.AuthorizationEndpoint)
			default:
				return nil, fmt.Errorf("Unknown %s sign-in method: %s", options.Name, method)
			}
		},
		Refresh: func(credential *OAuthCredential, ctx context.Context) (*OAuthCredential, error) {
			if credential == nil {
				return nil, fmt.Errorf("credential is required")
			}
			return RequestRadiusOAuthToken(ctx, gateway, map[string]string{
				"grant_type":    "refresh_token",
				"client_id":     RadiusOAuthClientID,
				"refresh_token": credential.Refresh,
			})
		},
		ToAuth: func(credential *OAuthCredential) (*ModelAuth, error) {
			if credential == nil {
				return nil, fmt.Errorf("credential is required")
			}
			return &ModelAuth{APIKey: credential.Access}, nil
		},
	}
}
