package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Tests for auth/oauth/openrouter.ts.

func TestParseOpenRouterAuthorizationInput(t *testing.T) {
	cases := []struct {
		input string
		code  string
		ok    bool
	}{
		{"", "", false},
		{"http://127.0.0.1:1234/oauth/callback/uuid?code=abc", "abc", true},
		{"code=abc", "abc", true},
		{"raw-code", "raw-code", true},
		{"http://127.0.0.1:1234/oauth/callback/uuid", "", false},
		{"  code=abc  ", "abc", true},
	}
	for _, testCase := range cases {
		code, ok := ParseOpenRouterAuthorizationInput(testCase.input)
		if code != testCase.code || ok != testCase.ok {
			t.Errorf("ParseOpenRouterAuthorizationInput(%q) = %q/%v, want %q/%v",
				testCase.input, code, ok, testCase.code, testCase.ok)
		}
	}
}

func TestOpenRouterErrorDetail(t *testing.T) {
	cases := []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"error_description": "desc"}, "desc"},
		{map[string]any{"message": "msg"}, "msg"},
		{map[string]any{"error": "code"}, "code"},
		{map[string]any{"error": map[string]any{"message": "nested"}}, "nested"},
		{map[string]any{"error": map[string]any{"code": 1}}, ""},
		{map[string]any{}, ""},
	}
	for _, testCase := range cases {
		if got := openRouterErrorDetail(testCase.body); got != testCase.want {
			t.Errorf("openRouterErrorDetail(%#v) = %q, want %q", testCase.body, got, testCase.want)
		}
	}
}

func TestExchangeOpenRouterAuthorizationCode(t *testing.T) {
	var captured map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		raw, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(raw, &captured)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"key":"sk-or-permanent"}`))
	}))
	defer server.Close()

	withOpenRouterTokenURL(t, server.URL, func() {
		credential, err := ExchangeOpenRouterAuthorizationCode(context.Background(), "code-1", "verifier-1")
		if err != nil {
			t.Fatal(err)
		}
		if credential.Access != "sk-or-permanent" || credential.Refresh != "" {
			t.Fatalf("credential = %+v", credential)
		}
		// OpenRouter keys do not expire.
		if credential.Expires != 9007199254740991 {
			t.Fatalf("expires = %d", credential.Expires)
		}
		if captured["code"] != "code-1" || captured["code_verifier"] != "verifier-1" ||
			captured["code_challenge_method"] != "S256" {
			t.Fatalf("body = %#v", captured)
		}
	})

	// Failure details come from the body.
	failServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error_description":"bad code"}`))
	}))
	defer failServer.Close()
	withOpenRouterTokenURL(t, failServer.URL, func() {
		_, err := ExchangeOpenRouterAuthorizationCode(context.Background(), "c", "v")
		if err == nil || err.Error() != "OpenRouter OAuth key exchange failed (HTTP 401): bad code" {
			t.Fatalf("err = %v", err)
		}
	})

	// A success without a key reports upstream's message.
	emptyServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"other":true}`))
	}))
	defer emptyServer.Close()
	withOpenRouterTokenURL(t, emptyServer.URL, func() {
		_, err := ExchangeOpenRouterAuthorizationCode(context.Background(), "c", "v")
		if err == nil || err.Error() != `OpenRouter OAuth response carries no "key"` {
			t.Fatalf("err = %v", err)
		}
	})

	// Invalid JSON on a success is an error.
	badJSON := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("not json"))
	}))
	defer badJSON.Close()
	withOpenRouterTokenURL(t, badJSON.URL, func() {
		_, err := ExchangeOpenRouterAuthorizationCode(context.Background(), "c", "v")
		if err == nil || err.Error() != "OpenRouter OAuth returned invalid JSON" {
			t.Fatalf("err = %v", err)
		}
	})

	// A cancelled context reports the cancellation.
	cancelled := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"key":"k"}`))
	}))
	defer cancelled.Close()
	withOpenRouterTokenURL(t, cancelled.URL, func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := ExchangeOpenRouterAuthorizationCode(ctx, "c", "v"); err == nil ||
			err.Error() != "Login cancelled" {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestOpenRouterCallbackServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"key":"sk-from-callback"}`))
	}))
	defer server.Close()

	withOpenRouterTokenURL(t, server.URL, func() {
		seed := t.TempDir()
		_ = seed
		callback, err := StartOpenRouterCallbackServer("/oauth/callback/test", "verifier-1", context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer callback.Close()
		if !strings.HasPrefix(callback.CallbackURL, "http://127.0.0.1:") {
			t.Fatalf("callback url = %s", callback.CallbackURL)
		}

		// A wrong method or path is refused.
		response, err := http.Get(strings.Replace(callback.CallbackURL, "/oauth/callback/test", "/other", 1))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("status = %d", response.StatusCode)
		}

		// The real callback performs the exchange and settles.
		response, err = http.Get(callback.CallbackURL + "?code=abc")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "Signed in to OpenRouter") {
			t.Fatalf("status = %d body = %q", response.StatusCode, body)
		}
		credential, err := callback.WaitForCredential()
		if err != nil || credential == nil || credential.Access != "sk-from-callback" {
			t.Fatalf("credential = %+v err = %v", credential, err)
		}

		// Once the exchange settled, the one-shot server stops listening (the
		// 409 path only covers a callback while its exchange is in flight), so a
		// repeat request either cannot connect or is refused as already used.
		response, err = http.Get(callback.CallbackURL + "?code=abc")
		if err == nil {
			response.Body.Close()
			if response.StatusCode != http.StatusConflict {
				t.Fatalf("status = %d", response.StatusCode)
			}
		}
	})
}

func TestOpenRouterCallbackServerManualHandover(t *testing.T) {
	// Cancelling the wait hands over to manual entry with no credential.
	callback, err := StartOpenRouterCallbackServer("/oauth/callback/manual", "verifier", context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer callback.Close()
	go callback.CancelWait()
	credential, err := callback.WaitForCredential()
	if err != nil || credential != nil {
		t.Fatalf("credential = %+v err = %v", credential, err)
	}
	// Cancelling again is a no-op.
	callback.CancelWait()
}

func TestOpenRouterCallbackServerErrors(t *testing.T) {
	// A denied authorization settles with an error and serves the failure page.
	callback, err := StartOpenRouterCallbackServer("/oauth/callback/denied", "verifier", context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer callback.Close()
	response, err := http.Get(callback.CallbackURL + "?error=access_denied&error_description=nope")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "authorization was denied") {
		t.Fatalf("status = %d body = %q", response.StatusCode, body)
	}
	_, err = callback.WaitForCredential()
	if err == nil || err.Error() != "OpenRouter authorization failed: nope" {
		t.Fatalf("err = %v", err)
	}

	// A missing code serves a failure page but does not settle.
	callback2, err := StartOpenRouterCallbackServer("/oauth/callback/nocode", "verifier", context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer callback2.Close()
	response, err = http.Get(callback2.CallbackURL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "returned no authorization code") {
		t.Fatalf("status = %d body = %q", response.StatusCode, body)
	}
	callback2.CancelWait()
	if credential, err := callback2.WaitForCredential(); credential != nil || err != nil {
		t.Fatalf("credential = %+v err = %v", credential, err)
	}
}

func TestOpenRouterAuthorizeURL(t *testing.T) {
	authURL := OpenRouterAuthorizeURL("http://127.0.0.1:5555/oauth/callback/x", "challenge-1")
	parsed, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "openrouter.ai" || parsed.Path != "/auth" {
		t.Fatalf("url = %s", authURL)
	}
	query := parsed.Query()
	if query.Get("callback_url") != "http://127.0.0.1:5555/oauth/callback/x" ||
		query.Get("code_challenge") != "challenge-1" ||
		query.Get("code_challenge_method") != "S256" {
		t.Fatalf("query = %v", query)
	}
}

func TestLoginOpenRouterManualPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"key":"sk-manual"}`))
	}))
	defer server.Close()

	withOpenRouterTokenURL(t, server.URL, func() {
		var notified []AuthEvent
		credential, err := LoginOpenRouter(&AuthInteraction{
			Ctx: context.Background(),
			Prompt: func(prompt AuthPrompt) (string, error) {
				if prompt.Type != AuthPromptManualCode {
					t.Errorf("prompt type = %s", prompt.Type)
				}
				return "code=manual-code", nil
			},
			Notify: func(event AuthEvent) { notified = append(notified, event) },
		})
		if err != nil {
			t.Fatal(err)
		}
		if credential.Access != "sk-manual" {
			t.Fatalf("credential = %+v", credential)
		}
		// A progress event announcing the callback URL, the auth URL, and the
		// exchange progress.
		if len(notified) != 3 || notified[0].Type != AuthEventProgress ||
			!strings.Contains(notified[0].Message, "Listening for OpenRouter OAuth callback") ||
			notified[1].Type != AuthEventAuthURL || notified[2].Type != AuthEventProgress {
			t.Fatalf("notifications = %+v", notified)
		}
	})

	// Without a code the flow reports upstream's message.
	withOpenRouterTokenURL(t, server.URL, func() {
		_, err := LoginOpenRouter(&AuthInteraction{
			Ctx:    context.Background(),
			Prompt: func(AuthPrompt) (string, error) { return "   ", nil },
		})
		if err == nil || err.Error() != "Missing authorization code" {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestOpenRouterOAuthDefinition(t *testing.T) {
	auth := OpenRouterOAuth()
	if auth.Name != "OpenRouter OAuth" || auth.LoginLabel != "Sign in with OpenRouter" || auth.IsSubscription {
		t.Fatalf("auth = %+v", auth)
	}
	credential := &OAuthCredential{OAuthCredentials: OAuthCredentials{Access: "sk-key"}}
	refreshed, err := auth.Refresh(credential, context.Background())
	if err != nil || refreshed != credential {
		t.Fatalf("refresh = %+v err = %v", refreshed, err)
	}
	modelAuth, err := auth.ToAuth(credential)
	if err != nil || modelAuth.APIKey != "sk-key" {
		t.Fatalf("model auth = %+v err = %v", modelAuth, err)
	}
	if _, err := auth.ToAuth(nil); err == nil {
		t.Fatal("nil credentials must be rejected")
	}
}

// withOpenRouterTokenURL points the key exchange at a test server.
func withOpenRouterTokenURL(t *testing.T, url string, run func()) {
	t.Helper()
	original := openRouterTokenURLValue
	openRouterTokenURLValue = url
	defer func() { openRouterTokenURLValue = original }()
	run()
}

var _ = time.Second
