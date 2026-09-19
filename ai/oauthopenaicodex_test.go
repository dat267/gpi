package ai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Tests for auth/oauth/openai-codex.ts.

// codexAccessToken builds a JWT-shaped token with the ChatGPT account claim.
func codexAccessToken(t *testing.T, accountID string) string {
	t.Helper()
	payload := map[string]any{
		OpenAICodexJWTPath: map[string]any{"chatgpt_account_id": accountID},
	}
	encoded, _ := json.Marshal(payload)
	// WHATWG base64url without padding, like the real tokens.
	segment := base64.RawURLEncoding.EncodeToString(encoded)
	return "header." + segment + ".signature"
}

func TestGetOpenAICodexAccountID(t *testing.T) {
	token := codexAccessToken(t, "acct_123")
	if got := GetOpenAICodexAccountID(token); got != "acct_123" {
		t.Fatalf("account id = %q", got)
	}
	// A token without the claim, a malformed token, and a non-JWT all yield "".
	other := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x"}`))
	if got := GetOpenAICodexAccountID("h." + other + ".s"); got != "" {
		t.Fatalf("account id = %q", got)
	}
	if got := GetOpenAICodexAccountID("not-a-jwt"); got != "" {
		t.Fatalf("account id = %q", got)
	}
	if got := GetOpenAICodexAccountID("h.***.***.s"); got != "" {
		t.Fatalf("account id = %q", got)
	}
	// Standard base64 with padding is accepted too (WHATWG forgiving base64).
	standard := base64.StdEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"acct_pad"}}`))
	if got := GetOpenAICodexAccountID("h." + standard + ".s"); got != "acct_pad" {
		t.Fatalf("account id = %q", got)
	}
}

func TestOpenAICodexAuthorizationFlow(t *testing.T) {
	flow, err := CreateOpenAICodexAuthorizationFlow("")
	if err != nil {
		t.Fatal(err)
	}
	if len(flow.Verifier) != 43 || len(flow.State) != 32 {
		t.Fatalf("flow = %+v", flow)
	}
	parsed, err := url.Parse(flow.URL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != "auth.openai.com" || parsed.Path != "/oauth/authorize" {
		t.Fatalf("url = %s", flow.URL)
	}
	query := parsed.Query()
	expectations := map[string]string{
		"response_type":              "code",
		"client_id":                  OpenAICodexClientID,
		"redirect_uri":               OpenAICodexRedirectURI,
		"scope":                      OpenAICodexScope,
		"code_challenge_method":      "S256",
		"state":                      flow.State,
		"id_token_add_organizations": "true",
		"codex_cli_simplified_flow":  "true",
		"originator":                 "pi",
	}
	for key, want := range expectations {
		if got := query.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if query.Get("code_challenge") == "" {
		t.Fatal("code_challenge missing")
	}
}

func TestOpenAICodexTokenExchangeAndRefresh(t *testing.T) {
	var forms []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		forms = append(forms, readForm(t, request))
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"` + codexAccessToken(t, "acct_1") + `","refresh_token":"refresh-1","expires_in":3600}`))
	}))
	defer server.Close()

	withCodexURLs(t, server.URL, func() {
		token, err := ExchangeOpenAICodexAuthorizationCode(context.Background(), "code", "verifier", OpenAICodexRedirectURI)
		if err != nil {
			t.Fatal(err)
		}
		if token.Refresh != "refresh-1" || token.Access == "" {
			t.Fatalf("token = %+v", token)
		}
		expected := forms[0]
		if expected.Get("grant_type") != "authorization_code" || expected.Get("code_verifier") != "verifier" ||
			expected.Get("client_id") != OpenAICodexClientID || expected.Get("redirect_uri") != OpenAICodexRedirectURI {
			t.Fatalf("form = %v", expected)
		}

		credential, err := OpenAICodexCredentialsFromToken(token)
		if err != nil {
			t.Fatal(err)
		}
		var accountID string
		_ = json.Unmarshal(credential.Extra["accountId"], &accountID)
		if accountID != "acct_1" {
			t.Fatalf("account = %q", accountID)
		}

		if _, err := RefreshOpenAICodexToken(context.Background(), "refresh-1"); err != nil {
			t.Fatal(err)
		}
		if forms[1].Get("grant_type") != "refresh_token" || forms[1].Get("refresh_token") != "refresh-1" {
			t.Fatalf("form = %v", forms[1])
		}
	})

	// A token without the account claim cannot produce a credential.
	if _, err := OpenAICodexCredentialsFromToken(&OpenAICodexTokenResponse{Access: "not-a-jwt", Refresh: "r"}); err == nil ||
		err.Error() != "Failed to extract accountId from token" {
		t.Fatalf("err = %v", err)
	}

	// Failure responses report the upstream message.
	failServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer failServer.Close()
	withCodexURLs(t, failServer.URL, func() {
		_, err := ExchangeOpenAICodexAuthorizationCode(context.Background(), "c", "v", OpenAICodexRedirectURI)
		if err == nil || !strings.HasPrefix(err.Error(), "OpenAI Codex token exchange failed (401): ") {
			t.Fatalf("err = %v", err)
		}
	})

	// Missing fields are reported with the operation name.
	missingServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"a"}`))
	}))
	defer missingServer.Close()
	withCodexURLs(t, missingServer.URL, func() {
		_, err := RefreshOpenAICodexToken(context.Background(), "r")
		if err == nil || !strings.Contains(err.Error(), "OpenAI Codex token refresh response missing fields") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestOpenAICodexDeviceAuthFlow(t *testing.T) {
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			raw := readBody(t, request)
			if !strings.Contains(raw, OpenAICodexClientID) {
				t.Errorf("body = %s", raw)
			}
			_, _ = writer.Write([]byte(`{"device_auth_id":"device-1","user_code":"CODE","interval":"1"}`))
		case "/api/accounts/deviceauth/token":
			polls++
			if polls == 1 {
				writer.WriteHeader(http.StatusForbidden)
				return
			}
			if polls == 2 {
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = writer.Write([]byte(`{"error":{"code":"deviceauth_authorization_pending"}}`))
				return
			}
			_, _ = writer.Write([]byte(`{"authorization_code":"auth-code","code_verifier":"auth-verifier"}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	withCodexURLs(t, server.URL, func() {
		device, err := StartOpenAICodexDeviceAuth(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		// The interval arrives as a string and is parsed.
		if device.DeviceAuthID != "device-1" || device.UserCode != "CODE" || device.IntervalSeconds != 1 {
			t.Fatalf("device = %+v", device)
		}
		result, err := PollOpenAICodexDeviceAuth(context.Background(), device)
		if err != nil {
			t.Fatal(err)
		}
		if result.AuthorizationCode != "auth-code" || result.CodeVerifier != "auth-verifier" {
			t.Fatalf("result = %+v", result)
		}
		if polls != 3 {
			t.Fatalf("polls = %d", polls)
		}
	})

	// A 404 on the user-code request reports the not-enabled message.
	notFound := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer notFound.Close()
	withCodexURLs(t, notFound.URL, func() {
		_, err := StartOpenAICodexDeviceAuth(context.Background())
		if err == nil || !strings.HasPrefix(err.Error(), "OpenAI Codex device code login is not enabled") {
			t.Fatalf("err = %v", err)
		}
	})

	// An invalid response is rejected.
	invalid := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"device_auth_id":"d"}`))
	}))
	defer invalid.Close()
	withCodexURLs(t, invalid.URL, func() {
		_, err := StartOpenAICodexDeviceAuth(context.Background())
		if err == nil || !strings.HasPrefix(err.Error(), "Invalid OpenAI Codex device code response: ") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestOpenAICodexLoginMethods(t *testing.T) {
	var tokenResponses []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/oauth/token" {
			form := readForm(t, request)
			if form.Get("redirect_uri") == openAICodexDeviceRedirectURI {
				tokenResponses = append(tokenResponses, "device")
			} else {
				tokenResponses = append(tokenResponses, "browser")
			}
			_, _ = writer.Write([]byte(`{"access_token":"` + codexAccessToken(t, "acct_login") + `","refresh_token":"r","expires_in":60}`))
			return
		}
		switch request.URL.Path {
		case "/api/accounts/deviceauth/usercode":
			_, _ = writer.Write([]byte(`{"device_auth_id":"d","user_code":"U","interval":1}`))
		case "/api/accounts/deviceauth/token":
			_, _ = writer.Write([]byte(`{"authorization_code":"c","code_verifier":"v"}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	withCodexURLs(t, server.URL, func() {
		// The select prompt routes to each login method.
		var notified []AuthEvent
		credential, err := OpenAICodexOAuth().Login(&AuthInteraction{
			Ctx: context.Background(),
			Prompt: func(prompt AuthPrompt) (string, error) {
				if prompt.Type == AuthPromptSelect {
					return OpenAICodexDeviceCodeLogin, nil
				}
				return "code#state", nil
			},
			Notify: func(event AuthEvent) { notified = append(notified, event) },
		})
		if err != nil {
			t.Fatal(err)
		}
		if credential.Access == "" || len(notified) != 1 || notified[0].Type != AuthEventDeviceCode ||
			notified[0].VerificationURI != openAICodexDeviceVerifyURL || notified[0].ExpiresSeconds != OpenAICodexDeviceTTLSec {
			t.Fatalf("credential = %+v notified = %+v", credential, notified)
		}
		if len(tokenResponses) != 1 || tokenResponses[0] != "device" {
			t.Fatalf("token responses = %#v", tokenResponses)
		}

		// An unknown method is reported.
		_, err = OpenAICodexOAuth().Login(&AuthInteraction{
			Ctx:    context.Background(),
			Prompt: func(AuthPrompt) (string, error) { return "nope", nil },
		})
		if err == nil || err.Error() != "Unknown OpenAI Codex login method: nope" {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestOpenAICodexOAuthDefinition(t *testing.T) {
	auth := OpenAICodexOAuth()
	if auth.Name != "OpenAI (ChatGPT Plus/Pro)" || !auth.IsSubscription {
		t.Fatalf("auth = %+v", auth)
	}
	credential := &OAuthCredential{OAuthCredentials: OAuthCredentials{Access: "access-token", Refresh: "refresh-token"}}
	modelAuth, err := auth.ToAuth(credential)
	if err != nil || modelAuth.APIKey != "access-token" {
		t.Fatalf("model auth = %+v err = %v", modelAuth, err)
	}
	if _, err := auth.ToAuth(nil); err == nil {
		t.Fatal("nil credentials must be rejected")
	}
}

func readBody(t *testing.T, request *http.Request) string {
	t.Helper()
	buffer := make([]byte, 0, 512)
	chunk := make([]byte, 256)
	for {
		read, err := request.Body.Read(chunk)
		buffer = append(buffer, chunk[:read]...)
		if err != nil {
			return string(buffer)
		}
	}
}

// withCodexURLs points the Codex endpoints at a test server.
func withCodexURLs(t *testing.T, base string, run func()) {
	t.Helper()
	originals := []*string{
		&openAICodexAuthorizeURL, &openAICodexTokenURL, &openAICodexDeviceUserCodeURL,
		&openAICodexDeviceTokenURL, &openAICodexDeviceVerifyURL,
	}
	originalValues := make([]string, len(originals))
	for index, target := range originals {
		originalValues[index] = *target
	}
	openAICodexAuthorizeURL = base + "/oauth/authorize"
	openAICodexTokenURL = base + "/oauth/token"
	openAICodexDeviceUserCodeURL = base + "/api/accounts/deviceauth/usercode"
	openAICodexDeviceTokenURL = base + "/api/accounts/deviceauth/token"
	openAICodexDeviceVerifyURL = base + "/codex/device"
	defer func() {
		for index, target := range originals {
			*target = originalValues[index]
		}
	}()
	run()
}
