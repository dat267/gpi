package ai

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Tests for auth/oauth/{pkce,device-code,oauth-page,anthropic}.ts.

func TestGeneratePKCE(t *testing.T) {
	pair, err := GeneratePKCE()
	if err != nil {
		t.Fatal(err)
	}
	// 32 random bytes base64url without padding is 43 characters.
	if len(pair.Verifier) != 43 || strings.ContainsAny(pair.Verifier, "+/=") {
		t.Fatalf("verifier = %q", pair.Verifier)
	}
	sum := sha256.Sum256([]byte(pair.Verifier))
	want := base64.RawURLEncoding.EncodeToString(sum[:])
	if pair.Challenge != want {
		t.Fatalf("challenge = %q, want %q", pair.Challenge, want)
	}
	second, _ := GeneratePKCE()
	if second.Verifier == pair.Verifier {
		t.Fatal("verifiers must be random")
	}
}

func TestAbortableSleepAndDeviceFlow(t *testing.T) {
	// A cancelled context aborts the sleep with the cancel message.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := AbortableSleep(ctx, time.Second, DeviceCodeCancelMessage); err == nil ||
		err.Error() != DeviceCodeCancelMessage {
		t.Fatalf("err = %v", err)
	}
	// A completed sleep returns nil.
	if err := AbortableSleep(context.Background(), time.Millisecond, DeviceCodeCancelMessage); err != nil {
		t.Fatalf("err = %v", err)
	}

	// The poll loop completes when the poll reports complete.
	polls := 0
	pending := 0
	value, err := PollOAuthDeviceCodeFlow(OAuthDeviceCodePollOptions[string]{
		Ctx: context.Background(),
		Poll: func() (OAuthDeviceCodePollResult[string], error) {
			polls++
			if polls < 2 {
				pending++
				return OAuthDeviceCodePollResult[string]{Status: "pending"}, nil
			}
			return OAuthDeviceCodePollResult[string]{Status: "complete", Value: "token"}, nil
		},
	})
	if err != nil || value != "token" || pending != 1 {
		t.Fatalf("value = %q err = %v polls = %d", value, err, polls)
	}

	// A failed poll surfaces its message.
	if _, err := PollOAuthDeviceCodeFlow(OAuthDeviceCodePollOptions[string]{
		Ctx: context.Background(),
		Poll: func() (OAuthDeviceCodePollResult[string], error) {
			return OAuthDeviceCodePollResult[string]{Status: "failed", Message: "denied"}, nil
		},
	}); err == nil || err.Error() != "denied" {
		t.Fatalf("err = %v", err)
	}

	// Expiry reports the timeout message; with a slow_down it reports the
	// clock-drift variant.
	zero := 0
	if _, err := PollOAuthDeviceCodeFlow(OAuthDeviceCodePollOptions[string]{
		ExpiresInSeconds: &zero,
		Ctx:              context.Background(),
		Poll: func() (OAuthDeviceCodePollResult[string], error) {
			return OAuthDeviceCodePollResult[string]{Status: "pending"}, nil
		},
	}); err == nil || err.Error() != DeviceCodeTimeoutMessage {
		t.Fatalf("err = %v", err)
	}
	one := 1
	if _, err := PollOAuthDeviceCodeFlow(OAuthDeviceCodePollOptions[string]{
		ExpiresInSeconds: &one,
		Ctx:              context.Background(),
		Poll: func() (OAuthDeviceCodePollResult[string], error) {
			interval := 1
			return OAuthDeviceCodePollResult[string]{Status: "slow_down", IntervalSeconds: &interval}, nil
		},
	}); err == nil || err.Error() != DeviceCodeSlowDownMessage {
		t.Fatalf("err = %v", err)
	}

	// A cancelled context aborts the loop.
	ctxCancel, cancelLoop := context.WithCancel(context.Background())
	cancelLoop()
	if _, err := PollOAuthDeviceCodeFlow(OAuthDeviceCodePollOptions[string]{
		Ctx: ctxCancel,
		Poll: func() (OAuthDeviceCodePollResult[string], error) {
			return OAuthDeviceCodePollResult[string]{Status: "pending"}, nil
		},
	}); err == nil || err.Error() != DeviceCodeCancelMessage {
		t.Fatalf("err = %v", err)
	}
}

func TestOAuthPageHTML(t *testing.T) {
	success := OAuthSuccessHTML("Done <now>")
	if !strings.Contains(success, "Authentication successful") || !strings.Contains(success, "Done &lt;now&gt;") {
		t.Fatalf("success page = %q", success)
	}
	if !strings.Contains(success, oauthLogoSVG) {
		t.Fatal("logo missing")
	}
	failure := OAuthErrorHTML("Failed & bad", "code=1")
	if !strings.Contains(failure, "Authentication failed") || !strings.Contains(failure, "Failed &amp; bad") ||
		!strings.Contains(failure, `class="details"`) {
		t.Fatalf("failure page = %q", failure)
	}
	// Details are omitted when absent.
	if strings.Contains(OAuthSuccessHTML("x"), `class="details"`) {
		t.Fatal("success page must not carry a details block")
	}
}

func TestParseAuthorizationInput(t *testing.T) {
	cases := []struct {
		input string
		code  string
		state string
	}{
		{"", "", ""},
		{"http://localhost:53692/callback?code=abc&state=verifier", "abc", "verifier"},
		{"abc#verifier", "abc", "verifier"},
		{"code=abc&state=verifier", "abc", "verifier"},
		{"raw-code", "raw-code", ""},
		{"  http://localhost:53692/callback?code=abc  ", "abc", ""},
	}
	for _, testCase := range cases {
		parsed := ParseAuthorizationInput(testCase.input)
		if parsed.Code != testCase.code || parsed.State != testCase.state {
			t.Errorf("ParseAuthorizationInput(%q) = %+v, want code=%q state=%q", testCase.input, parsed, testCase.code, testCase.state)
		}
	}
}

func TestAnthropicAuthorizeURL(t *testing.T) {
	authURL := AnthropicAuthorizeURL("challenge-value", "verifier-value")
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "https" || parsed.Host != "claude.ai" || parsed.Path != "/oauth/authorize" {
		t.Fatalf("url = %s", authURL)
	}
	query := parsed.Query()
	expectations := map[string]string{
		"code":                  "true",
		"client_id":             AnthropicOAuthClientID,
		"response_type":         "code",
		"redirect_uri":          AnthropicRedirectURI,
		"code_challenge":        "challenge-value",
		"code_challenge_method": "S256",
		"state":                 "verifier-value",
	}
	for key, want := range expectations {
		if got := query.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if !strings.Contains(query.Get("scope"), "user:inference") {
		t.Fatalf("scope = %q", query.Get("scope"))
	}
}

func TestOAuthCallbackServer(t *testing.T) {
	server, err := StartOAuthCallbackServer("expected-state")
	if err != nil {
		t.Skipf("loopback callback port unavailable: %v", err)
	}
	defer server.Close()
	if server.RedirectURI != AnthropicRedirectURI {
		t.Fatalf("redirect uri = %s", server.RedirectURI)
	}

	// A successful callback serves the success page and settles the wait.
	go func() {
		response, err := http.Get(AnthropicRedirectURI + "?code=abc&state=expected-state")
		if err == nil {
			response.Body.Close()
		}
	}()
	result, ok := server.WaitForCode(context.Background())
	if !ok || result.Code != "abc" || result.State != "expected-state" {
		t.Fatalf("result = %+v ok = %v", result, ok)
	}

	// A state mismatch serves the failure page and does not settle.
	server2, err := StartOAuthCallbackServer("expected-2")
	if err != nil {
		t.Skipf("loopback callback port unavailable: %v", err)
	}
	defer server2.Close()
	response, err := http.Get(AnthropicRedirectURI + "?code=abc&state=wrong")
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 4096)
	read, _ := response.Body.Read(body)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(body[:read]), "Authentication failed") {
		t.Fatalf("status = %d body = %q", response.StatusCode, body[:read])
	}
	server2.CancelWait()
	if _, ok := server2.WaitForCode(context.Background()); ok {
		t.Fatal("a cancelled wait must not report a code")
	}
}

func TestExchangeAndRefreshAnthropicToken(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := readAll(request)
		_ = json.Unmarshal(raw, &captured)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"access-1","refresh_token":"refresh-1","expires_in":3600}`))
	}))
	defer server.Close()

	withTokenURL(t, server.URL, func() {
		credential, err := ExchangeAnthropicAuthorizationCode(context.Background(), "code-1", "state-1", "verifier-1", AnthropicRedirectURI)
		if err != nil {
			t.Fatal(err)
		}
		if credential.Access != "access-1" || credential.Refresh != "refresh-1" {
			t.Fatalf("credential = %+v", credential)
		}
		// The expiry is five minutes inside the server-provided lifetime.
		expected := time.Now().UnixMilli() + 3600*1000 - 5*60*1000
		if diff := credential.Expires - expected; diff > 5000 || diff < -5000 {
			t.Fatalf("expires off by %d ms", diff)
		}
		if captured["grant_type"] != "authorization_code" || captured["code_verifier"] != "verifier-1" ||
			captured["client_id"] != AnthropicOAuthClientID || captured["redirect_uri"] != AnthropicRedirectURI {
			t.Fatalf("body = %#v", captured)
		}

		refreshed, err := RefreshAnthropicToken(context.Background(), "refresh-1")
		if err != nil || refreshed.Access != "access-1" {
			t.Fatalf("refreshed = %+v err = %v", refreshed, err)
		}
		if captured["grant_type"] != "refresh_token" || captured["refresh_token"] != "refresh-1" {
			t.Fatalf("body = %#v", captured)
		}
	})

	// HTTP failures carry upstream's message shape.
	errorServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer errorServer.Close()
	withTokenURL(t, errorServer.URL, func() {
		_, err := ExchangeAnthropicAuthorizationCode(context.Background(), "c", "s", "v", AnthropicRedirectURI)
		if err == nil || !strings.Contains(err.Error(), "Token exchange request failed.") ||
			!strings.Contains(err.Error(), "invalid_grant") {
			t.Fatalf("err = %v", err)
		}
		_, err = RefreshAnthropicToken(context.Background(), "r")
		if err == nil || !strings.Contains(err.Error(), "Anthropic token refresh request failed.") {
			t.Fatalf("err = %v", err)
		}
	})

	// Invalid JSON is reported.
	badJSON := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("not json"))
	}))
	defer badJSON.Close()
	withTokenURL(t, badJSON.URL, func() {
		_, err := ExchangeAnthropicAuthorizationCode(context.Background(), "c", "s", "v", AnthropicRedirectURI)
		if err == nil || !strings.Contains(err.Error(), "Token exchange returned invalid JSON.") {
			t.Fatalf("err = %v", err)
		}
		_, err = RefreshAnthropicToken(context.Background(), "r")
		if err == nil || !strings.Contains(err.Error(), "Anthropic token refresh returned invalid JSON.") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestLoginAnthropicManualCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"access_token":"manual-access","refresh_token":"manual-refresh","expires_in":60}`))
	}))
	defer server.Close()

	var notified []AuthEvent
	withTokenURL(t, server.URL, func() {
		credential, err := LoginAnthropic(&AuthInteraction{
			Ctx: context.Background(),
			Prompt: func(prompt AuthPrompt) (string, error) {
				if prompt.Type != AuthPromptManualCode {
					t.Errorf("prompt type = %s", prompt.Type)
				}
				// A bare code is accepted; the state defaults to the PKCE
				// verifier (upstream's behavior).
				return "manual-code", nil
			},
			Notify: func(event AuthEvent) { notified = append(notified, event) },
		})
		if err != nil {
			t.Fatal(err)
		}
		if credential.Access != "manual-access" {
			t.Fatalf("credential = %+v", credential)
		}
	})
	if len(notified) != 2 || notified[0].Type != AuthEventAuthURL ||
		!strings.Contains(notified[0].URL, "claude.ai/oauth/authorize") ||
		notified[1].Type != AuthEventProgress {
		t.Fatalf("notifications = %+v", notified)
	}
}

func TestAnthropicOAuthDefinition(t *testing.T) {
	auth := AnthropicOAuth()
	if auth.Name != "Anthropic (Claude Pro/Max)" || !auth.IsSubscription {
		t.Fatalf("auth = %+v", auth)
	}
	modelAuth, err := auth.ToAuth(&OAuthCredential{OAuthCredentials: OAuthCredentials{Access: "access-token"}})
	if err != nil || modelAuth.APIKey != "access-token" {
		t.Fatalf("model auth = %+v err = %v", modelAuth, err)
	}
	if _, err := auth.ToAuth(nil); err == nil {
		t.Fatal("nil credentials must be rejected")
	}
	if _, err := auth.Refresh(nil, context.Background()); err == nil {
		t.Fatal("nil credentials must be rejected on refresh")
	}
}

// withTokenURL points the Anthropic token endpoint at a test server.
func withTokenURL(t *testing.T, url string, run func()) {
	t.Helper()
	original := anthropicTokenURLValue
	anthropicTokenURLValue = url
	defer func() { anthropicTokenURLValue = original }()
	run()
}

func readAll(request *http.Request) ([]byte, error) {
	buffer := make([]byte, 0, 1024)
	chunk := make([]byte, 512)
	for {
		read, err := request.Body.Read(chunk)
		buffer = append(buffer, chunk[:read]...)
		if err != nil {
			return buffer, nil
		}
	}
}
