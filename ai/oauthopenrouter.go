package ai

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

// Port of auth/oauth/openrouter.ts: the OpenRouter PKCE flow. OpenRouter
// exchanges an authorization code for a permanent, user-controlled API key
// rather than an expiring access/refresh token pair.

const (
	// OpenRouterAuthorizeEndpoint is the authorize endpoint.
	OpenRouterAuthorizeEndpoint = "https://openrouter.ai/auth"
	// OpenRouterTokenEndpoint is the key-exchange endpoint.
	OpenRouterTokenEndpoint = "https://openrouter.ai/api/v1/auth/keys"
	openRouterLoginTimeout  = 5 * time.Minute
	openRouterExchangeTTL   = 30 * time.Second
)

// openRouterTokenURLValue is overridable for tests (upstream keeps it constant
// and mocks fetch).
var openRouterTokenURLValue = OpenRouterTokenEndpoint

// OpenRouterTokenURLValue returns the key-exchange endpoint.
func OpenRouterTokenURLValue() string { return openRouterTokenURLValue }

// oauthCallbackHost returns the loopback host for OAuth callbacks
// (PI_OAUTH_CALLBACK_HOST overrides it).
func oauthCallbackHost() string {
	if host := os.Getenv("PI_OAUTH_CALLBACK_HOST"); host != "" {
		return host
	}
	return "127.0.0.1"
}

// ParseOpenRouterAuthorizationInput extracts a code from a pasted redirect URL,
// a query string, or a bare code (upstream parseAuthorizationInput).
func ParseOpenRouterAuthorizationInput(input string) (string, bool) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", false
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		if code := parsed.Query().Get("code"); code != "" {
			return code, true
		}
		return "", false
	}
	if strings.Contains(value, "code=") {
		if values, err := url.ParseQuery(value); err == nil {
			if code := values.Get("code"); code != "" {
				return code, true
			}
		}
		return "", false
	}
	return value, true
}

// openRouterErrorDetail extracts the most specific error text from a body.
func openRouterErrorDetail(body map[string]any) string {
	if text, ok := body["error_description"].(string); ok {
		return text
	}
	if text, ok := body["message"].(string); ok {
		return text
	}
	if text, ok := body["error"].(string); ok {
		return text
	}
	if record, ok := body["error"].(map[string]any); ok {
		if text, ok := record["message"].(string); ok {
			return text
		}
	}
	return ""
}

// ExchangeOpenRouterAuthorizationCode exchanges an authorization code for a
// permanent API key (upstream exchangeAuthorizationCode).
func ExchangeOpenRouterAuthorizationCode(ctx context.Context, code, verifier string) (*OAuthCredential, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, fmt.Errorf("Login cancelled")
	}
	requestCtx, cancel := context.WithTimeout(context.Background(), openRouterExchangeTTL)
	defer cancel()
	if ctx != nil {
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
	}

	body, err := json.Marshal(map[string]string{
		"code":                  code,
		"code_verifier":         verifier,
		"code_challenge_method": "S256",
	})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, openRouterTokenURLValue, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("accept", "application/json")
	request.Header.Set("content-type", "application/json")

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return nil, fmt.Errorf("Login cancelled")
		}
		if requestCtx.Err() != nil {
			return nil, fmt.Errorf("OpenRouter OAuth token exchange timed out")
		}
		return nil, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	ok := response.StatusCode < 400
	payload := map[string]any{}
	parsed := any(nil)
	if err := json.Unmarshal(raw, &parsed); err == nil {
		if record, isRecord := parsed.(map[string]any); isRecord {
			payload = record
		}
	} else if ok {
		return nil, fmt.Errorf("OpenRouter OAuth returned invalid JSON")
	}

	if !ok {
		detail := openRouterErrorDetail(payload)
		message := fmt.Sprintf("OpenRouter OAuth key exchange failed (HTTP %d)", response.StatusCode)
		if detail != "" {
			message += ": " + detail
		}
		return nil, fmt.Errorf("%s", message)
	}
	key, _ := payload["key"].(string)
	if key == "" {
		return nil, fmt.Errorf(`OpenRouter OAuth response carries no "key"`)
	}
	return &OAuthCredential{OAuthCredentials: OAuthCredentials{
		Access: key,
		// OpenRouter keys do not expire.
		Expires: 9007199254740991,
	}}, nil
}

// OpenRouterCallbackServer is the one-shot loopback callback server (upstream
// startCallbackServer).
type OpenRouterCallbackServer struct {
	CallbackURL string

	listener net.Listener
	server   *http.Server
	timeout  *time.Timer

	mu         sync.Mutex
	claimed    bool
	settled    bool
	credential *OAuthCredential
	waitErr    error
	done       chan struct{}
}

// StartOpenRouterCallbackServer starts the server on an ephemeral port.
func StartOpenRouterCallbackServer(callbackPath, verifier string, ctx context.Context) (*OpenRouterCallbackServer, error) {
	if ctx != nil && ctx.Err() != nil {
		return nil, fmt.Errorf("Login cancelled")
	}
	host := oauthCallbackHost()
	listener, err := net.Listen("tcp", host+":0")
	if err != nil {
		return nil, err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	callback := &OpenRouterCallbackServer{
		CallbackURL: fmt.Sprintf("http://%s:%d%s", host, port, callbackPath),
		listener:    listener,
		done:        make(chan struct{}),
	}

	finish := func(credential *OAuthCredential, waitErr error) {
		callback.mu.Lock()
		if callback.settled {
			callback.mu.Unlock()
			return
		}
		callback.settled = true
		callback.credential = credential
		callback.waitErr = waitErr
		callback.mu.Unlock()
		callback.stopListening()
		close(callback.done)
	}

	mux := http.NewServeMux()
	mux.HandleFunc(callbackPath, func(writer http.ResponseWriter, request *http.Request) {
		sendHTML := func(status int, html string) {
			writer.Header().Set("content-type", "text/html; charset=utf-8")
			writer.Header().Set("cache-control", "no-store")
			writer.WriteHeader(status)
			_, _ = writer.Write([]byte(html))
		}
		if request.Method != http.MethodGet {
			sendHTML(http.StatusNotFound, OAuthErrorHTML("OAuth callback route not found.", ""))
			return
		}
		callback.mu.Lock()
		alreadyUsed := callback.claimed || callback.settled
		callback.mu.Unlock()
		if alreadyUsed {
			sendHTML(http.StatusConflict, OAuthErrorHTML("This OAuth callback has already been used.", ""))
			return
		}
		query := request.URL.Query()
		if oauthError := query.Get("error"); oauthError != "" {
			description := query.Get("error_description")
			if description == "" {
				description = oauthError
			}
			sendHTML(http.StatusBadRequest, OAuthErrorHTML("OpenRouter authorization was denied.", description))
			finish(nil, fmt.Errorf("OpenRouter authorization failed: %s", description))
			return
		}
		code := query.Get("code")
		if code == "" {
			sendHTML(http.StatusBadRequest, OAuthErrorHTML("OpenRouter returned no authorization code.", ""))
			return
		}
		callback.mu.Lock()
		callback.claimed = true
		callback.mu.Unlock()

		credential, exchangeErr := ExchangeOpenRouterAuthorizationCode(ctx, code, verifier)
		if exchangeErr != nil {
			message := exchangeErr.Error()
			sendHTML(http.StatusBadGateway, OAuthErrorHTML("OpenRouter key exchange failed.", message))
			finish(nil, exchangeErr)
			return
		}
		sendHTML(http.StatusOK, OAuthSuccessHTML("Signed in to OpenRouter. You may now close this page."))
		finish(credential, nil)
	})
	callback.server = &http.Server{Handler: mux}
	go func() { _ = callback.server.Serve(listener) }()

	stop := func() {
		if ctx == nil {
			return
		}
		select {
		case <-ctx.Done():
			finish(nil, fmt.Errorf("Login cancelled"))
		default:
		}
	}
	callback.timeout = time.AfterFunc(openRouterLoginTimeout, func() {
		finish(nil, fmt.Errorf("OpenRouter OAuth login timed out"))
	})
	if ctx != nil {
		context.AfterFunc(ctx, stop)
	}
	return callback, nil
}

// CancelWait hands the login over to manual code entry unless a callback
// already claimed the exchange.
func (s *OpenRouterCallbackServer) CancelWait() {
	s.mu.Lock()
	claimed := s.claimed
	settled := s.settled
	s.mu.Unlock()
	if claimed || settled {
		return
	}
	s.mu.Lock()
	if s.settled {
		s.mu.Unlock()
		return
	}
	s.settled = true
	s.mu.Unlock()
	s.stopListening()
	close(s.done)
}

// WaitForCredential waits for the browser callback to complete the exchange, or
// for a cancellation to hand over to manual entry.
func (s *OpenRouterCallbackServer) WaitForCredential() (*OAuthCredential, error) {
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.waitErr != nil {
		return nil, s.waitErr
	}
	return s.credential, nil
}

func (s *OpenRouterCallbackServer) stopListening() {
	if s.timeout != nil {
		s.timeout.Stop()
	}
	if s.server != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.server.Shutdown(shutdownCtx)
	}
}

// Close releases the server and timers without settling a wait.
func (s *OpenRouterCallbackServer) Close() {
	s.mu.Lock()
	if s.settled {
		s.mu.Unlock()
		return
	}
	s.settled = true
	s.mu.Unlock()
	s.stopListening()
	close(s.done)
}

// OpenRouterAuthorizeURL builds the authorize URL for a callback and challenge.
func OpenRouterAuthorizeURL(callbackURL, challenge string) string {
	params := url.Values{}
	params.Set("callback_url", callbackURL)
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	return OpenRouterAuthorizeURLBase() + "?" + params.Encode()
}

// OpenRouterAuthorizeURLBase is the authorize endpoint.
func OpenRouterAuthorizeURLBase() string { return OpenRouterAuthorizeEndpoint }

// LoginOpenRouter runs the OpenRouter PKCE login (upstream loginOpenRouter).
func LoginOpenRouter(interaction *AuthInteraction) (*OAuthCredential, error) {
	if interaction == nil {
		return nil, fmt.Errorf("auth interaction is required")
	}
	pkce, err := GeneratePKCE()
	if err != nil {
		return nil, err
	}
	callbackPath := "/oauth/callback/" + randomUUIDString()
	ctx := interaction.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	callback, err := StartOpenRouterCallbackServer(callbackPath, pkce.Verifier, ctx)
	if err != nil {
		return nil, err
	}
	defer callback.Close()

	type manualResult struct {
		input string
		err   error
	}
	manualCh := make(chan manualResult, 1)
	go func() {
		input, perr := interaction.Prompt(AuthPrompt{
			Type:        AuthPromptManualCode,
			Message:     "Complete sign-in in your browser, or paste the authorization code / redirect URL here:",
			Placeholder: callback.CallbackURL,
		})
		manualCh <- manualResult{input: input, err: perr}
		callback.CancelWait()
	}()

	if interaction.Notify != nil {
		interaction.Notify(AuthEvent{
			Type:    AuthEventProgress,
			Message: "Listening for OpenRouter OAuth callback on " + callback.CallbackURL,
		})
		interaction.Notify(AuthEvent{
			Type: AuthEventAuthURL,
			URL:  OpenRouterAuthorizeURL(callback.CallbackURL, pkce.Challenge),
			Instructions: "Complete sign-in in your browser. If the browser is on another machine, " +
				"paste the final redirect URL here.",
		})
	}

	credential, waitErr := callback.WaitForCredential()
	if waitErr != nil {
		// A manual prompt error wins over the callback outcome (upstream checks
		// manualError first).
		select {
		case manual := <-manualCh:
			if manual.err != nil {
				return nil, manual.err
			}
		default:
		}
		return nil, waitErr
	}
	if credential != nil {
		return credential, nil
	}
	manual := <-manualCh
	if manual.err != nil {
		return nil, manual.err
	}
	code, ok := ParseOpenRouterAuthorizationInput(manual.input)
	if !ok {
		return nil, fmt.Errorf("Missing authorization code")
	}
	if interaction.Notify != nil {
		interaction.Notify(AuthEvent{Type: AuthEventProgress, Message: "Exchanging authorization code for an API key..."})
	}
	return ExchangeOpenRouterAuthorizationCode(ctx, code, pkce.Verifier)
}

// OpenRouterOAuth is the OpenRouter OAuth auth definition (upstream
// openRouterOAuth).
func OpenRouterOAuth() *OAuthAuth {
	return &OAuthAuth{
		Name:       "OpenRouter OAuth",
		LoginLabel: "Sign in with OpenRouter",
		Login:      LoginOpenRouter,
		Refresh: func(credential *OAuthCredential, ctx context.Context) (*OAuthCredential, error) {
			// OpenRouter hands out a permanent key, so refresh is a no-op.
			return credential, nil
		},
		ToAuth: func(credential *OAuthCredential) (*ModelAuth, error) {
			if credential == nil {
				return nil, fmt.Errorf("credential is required")
			}
			return &ModelAuth{APIKey: credential.Access}, nil
		},
	}
}

// randomUUIDString generates a v4 UUID for the callback path.
func randomUUIDString() string {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	random[6] = (random[6] & 0x0f) | 0x40
	random[8] = (random[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(random[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}
