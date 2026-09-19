package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Tests for auth/oauth/xai.ts and auth/oauth/kimi-coding.ts.

func TestValidateXaiVerificationURI(t *testing.T) {
	if _, err := ValidateXaiVerificationURI("https://x.ai/device"); err != nil {
		t.Fatalf("err = %v", err)
	}
	for _, raw := range []string{"http://x.ai/device", "not a url", "javascript:alert(1)", ""} {
		if _, err := ValidateXaiVerificationURI(raw); err == nil ||
			err.Error() != "Untrusted verification URI in xAI OAuth response" {
			t.Errorf("ValidateXaiVerificationURI(%q) err = %v", raw, err)
		}
	}
}

func TestParseXaiDeviceCode(t *testing.T) {
	device, err := parseXaiDeviceCode(map[string]any{
		"device_code":               "device-1",
		"user_code":                 "ABCD-1234",
		"verification_uri":          "https://x.ai/device",
		"verification_uri_complete": "https://x.ai/device?user_code=ABCD-1234",
		"interval":                  float64(5),
		"expires_in":                float64(600),
	})
	if err != nil {
		t.Fatal(err)
	}
	if device.DeviceCode != "device-1" || device.UserCode != "ABCD-1234" || device.ExpiresInSeconds != 600 ||
		device.IntervalSeconds == nil || *device.IntervalSeconds != 5 {
		t.Fatalf("device = %+v", device)
	}
	if device.VerificationURIComplete != "https://x.ai/device?user_code=ABCD-1234" {
		t.Fatalf("complete = %s", device.VerificationURIComplete)
	}

	// A non-positive interval falls back to the poller default.
	device, err = parseXaiDeviceCode(map[string]any{
		"device_code": "d", "user_code": "u",
		"verification_uri": "https://x.ai/device", "interval": float64(0), "expires_in": float64(60),
	})
	if err != nil || device.IntervalSeconds != nil {
		t.Fatalf("device = %+v err = %v", device, err)
	}

	// Missing or invalid fields report the upstream message.
	for name, body := range map[string]map[string]any{
		"missing device code":    {"user_code": "u", "verification_uri": "https://x.ai/d", "expires_in": float64(1)},
		"untrusted verification": {"device_code": "d", "user_code": "u", "verification_uri": "http://x.ai/d", "expires_in": float64(1)},
		"non-positive expiry":    {"device_code": "d", "user_code": "u", "verification_uri": "https://x.ai/d", "expires_in": float64(0)},
		"untrusted complete uri": {"device_code": "d", "user_code": "u", "verification_uri": "https://x.ai/d", "expires_in": float64(1), "verification_uri_complete": "javascript:x"},
	} {
		if _, err := parseXaiDeviceCode(body); err == nil {
			t.Errorf("%s: expected an error", name)
		} else if !strings.HasPrefix(err.Error(), "Invalid xAI OAuth response field: ") &&
			err.Error() != "Untrusted verification URI in xAI OAuth response" {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestXaiCredentialsFromTokenResponse(t *testing.T) {
	credential, err := xaiCredentialsFromTokenResponse(map[string]any{
		"access_token": "access", "refresh_token": "refresh", "expires_in": float64(100),
	}, "")
	if err != nil || credential.Access != "access" || credential.Refresh != "refresh" {
		t.Fatalf("credential = %+v err = %v", credential, err)
	}
	// A refresh response without a rotated token keeps the previous one.
	credential, err = xaiCredentialsFromTokenResponse(map[string]any{"access_token": "access-2"}, "refresh-1")
	if err != nil || credential.Refresh != "refresh-1" {
		t.Fatalf("credential = %+v err = %v", credential, err)
	}
	// A missing expiry uses the default lifetime.
	if _, err := xaiCredentialsFromTokenResponse(map[string]any{"access_token": "a", "refresh_token": "r"}, ""); err != nil {
		t.Fatalf("err = %v", err)
	}
	// A missing refresh token with no previous value is an error.
	if _, err := xaiCredentialsFromTokenResponse(map[string]any{"access_token": "a"}, ""); err == nil {
		t.Fatal("expected an error")
	}
}

func TestXaiDeviceCodeFlow(t *testing.T) {
	var tokenCalls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/oauth2/device/code":
			body := readForm(t, request)
			if body.Get("client_id") != XAIClientID || body.Get("referrer") != "pi" {
				t.Errorf("device body = %v", body)
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"device_code":"device-1","user_code":"CODE","verification_uri":"https://x.ai/device","expires_in":600,"interval":1}`))
		case "/oauth2/token":
			tokenCalls++
			body := readForm(t, request)
			if body.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" {
				t.Errorf("grant = %s", body.Get("grant_type"))
			}
			writer.Header().Set("Content-Type", "application/json")
			if tokenCalls == 1 {
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = writer.Write([]byte(`{"error":"authorization_pending"}`))
				return
			}
			_, _ = writer.Write([]byte(`{"access_token":"xai-access","refresh_token":"xai-refresh","expires_in":3600}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	withXaiURLs(t, server.URL, func() {
		var notified []AuthEvent
		credential, err := LoginXai(&AuthInteraction{
			Ctx:    context.Background(),
			Notify: func(event AuthEvent) { notified = append(notified, event) },
		})
		if err != nil {
			t.Fatal(err)
		}
		if credential.Access != "xai-access" || credential.Refresh != "xai-refresh" {
			t.Fatalf("credential = %+v", credential)
		}
		if len(notified) != 1 || notified[0].Type != AuthEventDeviceCode || notified[0].UserCode != "CODE" ||
			notified[0].VerificationURI != "https://x.ai/device" || notified[0].ExpiresSeconds != 600 {
			t.Fatalf("notifications = %+v", notified)
		}
	})

	// Error responses map to the upstream messages.
	t.Setenv("noop", "1")
	failServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/oauth2/device/code":
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":"invalid_client","error_description":"bad client"}`))
		default:
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":"access_denied"}`))
		}
	}))
	defer failServer.Close()
	withXaiURLs(t, failServer.URL, func() {
		_, err := RequestXaiDeviceCode(context.Background())
		if err == nil || err.Error() != "xAI OAuth device authorization failed (HTTP 400): invalid_client: bad client" {
			t.Fatalf("err = %v", err)
		}
		_, err = PollXaiForTokens(context.Background(), &XaiDeviceCode{
			DeviceCode: "d", VerificationURI: "https://x.ai/d", ExpiresInSeconds: 60, IntervalSeconds: intPtr(1),
		})
		if err == nil || err.Error() != "xAI device authorization was denied" {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestXaiRefresh(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body := readForm(t, request)
		if body.Get("grant_type") != "refresh_token" || body.Get("refresh_token") != "refresh-1" {
			t.Errorf("body = %v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"new-access"}`))
	}))
	defer server.Close()
	withXaiURLs(t, server.URL, func() {
		credential, err := RefreshXaiToken(context.Background(), "refresh-1")
		if err != nil {
			t.Fatal(err)
		}
		if credential.Access != "new-access" || credential.Refresh != "refresh-1" {
			t.Fatalf("credential = %+v", credential)
		}
	})
}

func TestKimiDeviceCodeFlow(t *testing.T) {
	t.Setenv("KIMI_CODE_OAUTH_HOST", "")
	t.Setenv("KIMI_OAUTH_HOST", "")
	if host := KimiCodingOAuthHost(); host != KimiCodingDefaultOAuthHost {
		t.Fatalf("host = %s", host)
	}
	t.Setenv("KIMI_CODE_OAUTH_HOST", "https://override.example///")
	if host := KimiCodingOAuthHost(); host != "https://override.example" {
		t.Fatalf("host = %s", host)
	}
	t.Setenv("KIMI_CODE_OAUTH_HOST", "")
	t.Setenv("KIMI_OAUTH_HOST", "https://env2.example")
	if host := KimiCodingOAuthHost(); host != "https://env2.example" {
		t.Fatalf("host = %s", host)
	}
	t.Setenv("KIMI_OAUTH_HOST", "")

	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/api/oauth/device_authorization":
			body := readForm(t, request)
			if body.Get("client_id") != KimiCodingClientID {
				t.Errorf("body = %v", body)
			}
			_, _ = writer.Write([]byte(`{"device_code":"d1","user_code":"U1","verification_uri":"https://auth.kimi.com/device","verification_uri_complete":"https://auth.kimi.com/device?code=U1","interval":1,"expires_in":600}`))
		case "/api/oauth/token":
			polls++
			if polls == 1 {
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = writer.Write([]byte(`{"error":"slow_down","interval":1}`))
				return
			}
			_, _ = writer.Write([]byte(`{"access_token":"kimi-access","refresh_token":"kimi-refresh","expires_in":1800}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	// Point the flow at the test host through the env override.
	t.Setenv("KIMI_CODE_OAUTH_HOST", server.URL)
	var notified []AuthEvent
	credential, err := LoginKimiCoding(&AuthInteraction{
		Ctx:    context.Background(),
		Notify: func(event AuthEvent) { notified = append(notified, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if credential.Access != "kimi-access" || credential.Refresh != "kimi-refresh" {
		t.Fatalf("credential = %+v", credential)
	}
	if len(notified) != 1 || notified[0].Type != AuthEventDeviceCode ||
		notified[0].VerificationURI != "https://auth.kimi.com/device?code=U1" ||
		notified[0].IntervalSeconds != 1 || notified[0].ExpiresSeconds != 600 {
		t.Fatalf("notifications = %+v", notified)
	}
	if polls < 2 {
		t.Fatalf("polls = %d", polls)
	}
}

func TestKimiDeviceAuthorizationValidation(t *testing.T) {
	// A missing field or an untrusted verification URI is rejected.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"device_code":"d","user_code":"u","verification_uri":"javascript:x","verification_uri_complete":"javascript:y"}`))
	}))
	defer server.Close()
	if _, err := StartKimiDeviceAuthorization(context.Background(), server.URL); err == nil ||
		!strings.HasPrefix(err.Error(), "Invalid Kimi Code device authorization response: ") {
		t.Fatalf("err = %v", err)
	}

	// A failing status reports upstream's message.
	failServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte("boom"))
	}))
	defer failServer.Close()
	// The raw response text is appended, matching upstream's `: ${text}`.
	if _, err := StartKimiDeviceAuthorization(context.Background(), failServer.URL); err == nil ||
		err.Error() != "Kimi Code device authorization failed with status 502: boom" {
		t.Fatalf("err = %v", err)
	}
}

func TestKimiTokenPollErrors(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   string
	}{
		{500, `{"error":"server_error"}`, `Kimi Code device token request failed with status 500: {"error":"server_error"}`},
		{400, `{"error":"expired_token"}`, "Kimi Code device authorization expired. Please restart login."},
		{400, `{"error":"access_denied"}`, "Kimi Code login was denied."},
		{400, `{"error":"weird","error_description":"because"}`, "Kimi Code device token request failed (status 400): weird: because"},
	}
	for _, testCase := range cases {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(testCase.status)
			_, _ = writer.Write([]byte(testCase.body))
		}))
		device := &KimiDeviceAuthorization{
			DeviceCode: "d", IntervalSeconds: 1, ExpiresInSeconds: 60,
			VerificationURI: "https://auth.kimi.com/device", VerificationURIComplete: "https://auth.kimi.com/device",
		}
		_, err := PollKimiForToken(context.Background(), server.URL, device)
		server.Close()
		if err == nil || err.Error() != testCase.want {
			t.Errorf("status %d body %s: err = %v, want %q", testCase.status, testCase.body, err, testCase.want)
		}
	}

	// A 2xx response without an access token reports the field error.
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"access_token":"a"}`))
	}))
	defer server.Close()
	device := &KimiDeviceAuthorization{DeviceCode: "d", IntervalSeconds: 1, ExpiresInSeconds: 60}
	if _, err := PollKimiForToken(context.Background(), server.URL, device); err == nil ||
		!strings.Contains(err.Error(), "Kimi Code token poll response missing fields") {
		t.Fatalf("err = %v", err)
	}
}

func TestKimiRefresh(t *testing.T) {
	t.Setenv("KIMI_CODE_OAUTH_HOST", "")
	t.Setenv("KIMI_OAUTH_HOST", "")

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		attempts++
		writer.Header().Set("Content-Type", "application/json")
		if attempts == 1 {
			// A retryable status is retried with backoff.
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"error":"unavailable"}`))
			return
		}
		_, _ = writer.Write([]byte(`{"access_token":"new","refresh_token":"rotated","expires_in":900}`))
	}))
	defer server.Close()

	token, err := RefreshKimiToken(context.Background(), server.URL, "refresh-1")
	if err != nil {
		t.Fatal(err)
	}
	if token.Access != "new" || token.Refresh != "rotated" || attempts != 2 {
		t.Fatalf("token = %+v attempts = %d", token, attempts)
	}

	// A 401 is terminal with upstream's message.
	authServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error_description":"expired"}`))
	}))
	defer authServer.Close()
	if _, err := RefreshKimiToken(context.Background(), authServer.URL, "r"); err == nil ||
		!strings.Contains(err.Error(), "Kimi Code token refresh unauthorized (status 401)") {
		t.Fatalf("err = %v", err)
	}

	// A non-retryable 400 reports the body.
	badServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":"invalid_request"}`))
	}))
	defer badServer.Close()
	if _, err := RefreshKimiToken(context.Background(), badServer.URL, "r"); err == nil ||
		!strings.Contains(err.Error(), "Kimi Code token refresh failed with status 400") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeviceFlowAuthDefinitions(t *testing.T) {
	xai := XaiOAuth()
	if xai.Name != "xAI (Grok/X subscription)" || !xai.IsSubscription ||
		xai.LoginLabel != "Sign in with SuperGrok or X Premium" {
		t.Fatalf("xai = %+v", xai)
	}
	modelAuth, err := xai.ToAuth(&OAuthCredential{OAuthCredentials: OAuthCredentials{Access: "access"}})
	if err != nil || modelAuth.APIKey != "access" {
		t.Fatalf("model auth = %+v err = %v", modelAuth, err)
	}

	kimi := KimiCodingOAuth()
	if kimi.Name != "Kimi Code (subscription)" || kimi.LoginLabel != "Sign in with Kimi Code" {
		t.Fatalf("kimi = %+v", kimi)
	}
	modelAuth, err = kimi.ToAuth(&OAuthCredential{OAuthCredentials: OAuthCredentials{Access: "access"}})
	if err != nil || modelAuth.Headers["Authorization"] == nil || *modelAuth.Headers["Authorization"] != "Bearer access" {
		t.Fatalf("model auth = %+v err = %v", modelAuth, err)
	}
	if _, err := kimi.ToAuth(nil); err == nil {
		t.Fatal("nil credentials must be rejected")
	}
}

func readForm(t *testing.T, request *http.Request) url.Values {
	t.Helper()
	raw := make([]byte, 0, 1024)
	chunk := make([]byte, 512)
	for {
		read, err := request.Body.Read(chunk)
		raw = append(raw, chunk[:read]...)
		if err != nil {
			break
		}
	}
	values, err := url.ParseQuery(string(raw))
	if err != nil {
		t.Fatalf("form = %q: %v", raw, err)
	}
	return values
}

// withXaiURLs points the xAI endpoints at a test server.
func withXaiURLs(t *testing.T, base string, run func()) {
	t.Helper()
	originalDevice, originalToken := xaiDeviceCodeURL, xaiTokenURLValue
	xaiDeviceCodeURL = base + "/oauth2/device/code"
	xaiTokenURLValue = base + "/oauth2/token"
	defer func() {
		xaiDeviceCodeURL = originalDevice
		xaiTokenURLValue = originalToken
	}()
	run()
}

var _ = json.Marshal
