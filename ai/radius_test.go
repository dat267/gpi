package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Tests for providers/radius-config.ts, providers/radius.ts, and
// auth/oauth/radius.ts.

func TestNormalizeRadiusGatewayURL(t *testing.T) {
	cases := map[string]string{
		"radius.pi.dev":          "https://radius.pi.dev",
		"https://radius.pi.dev":  "https://radius.pi.dev",
		"http://localhost:8080/": "http://localhost:8080",
		"HTTPS://example.com//":  "HTTPS://example.com",
	}
	for input, want := range cases {
		if got := NormalizeRadiusGatewayURL(input); got != want {
			t.Errorf("NormalizeRadiusGatewayURL(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRadiusGatewayConfigParsing(t *testing.T) {
	raw := map[string]any{
		"baseUrl": "https://gateway.example",
		"models": []any{
			map[string]any{
				"id": "balanced", "name": "Balanced", "reasoning": true,
				"input":            []any{"text", "image"},
				"cost":             map[string]any{"input": 0.5, "output": 1.5, "cacheRead": 0.05, "cacheWrite": 0.6},
				"contextWindow":    float64(200000),
				"maxTokens":        float64(8192),
				"thinkingLevelMap": map[string]any{"high": "high", "off": nil},
			},
			// Invalid entries are dropped.
			map[string]any{"id": "broken"},
			"not an object",
		},
	}
	config := sanitizeRadiusGatewayConfig(raw)
	if config == nil {
		t.Fatal("config must parse")
	}
	if config.BaseURL != "https://gateway.example" || len(config.Models) != 1 {
		t.Fatalf("config = %+v", config)
	}
	model := config.Models[0]
	if model.ID != "balanced" || !model.Reasoning || model.ContextWindow != 200000 || model.MaxTokens != 8192 {
		t.Fatalf("model = %+v", model)
	}
	if model.Cost.Input != 0.5 || model.Cost.Output != 1.5 {
		t.Fatalf("cost = %+v", model.Cost)
	}
	if level := model.ThinkingLevelMap["high"]; level == nil || *level != "high" {
		t.Fatalf("thinking map = %#v", model.ThinkingLevelMap)
	}
	if value, present := model.ThinkingLevelMap["off"]; !present || value != nil {
		t.Fatalf("thinking map = %#v", model.ThinkingLevelMap)
	}

	// Invalid configs are rejected.
	for _, invalid := range []map[string]any{
		{"models": []any{}},
		{"baseUrl": "https://x"},
		{"baseUrl": 1, "models": []any{}},
	} {
		if sanitizeRadiusGatewayConfig(invalid) != nil {
			t.Errorf("expected nil for %#v", invalid)
		}
	}
}

func TestGetRadiusModels(t *testing.T) {
	configJSON, _ := json.Marshal(map[string]any{
		"baseUrl": "https://gateway.example",
		"models": []any{map[string]any{
			"id": "balanced", "name": "Balanced", "reasoning": true, "input": []any{"text"},
			"cost": map[string]any{"input": 1, "output": 2}, "contextWindow": float64(1000), "maxTokens": float64(100),
		}},
	})
	credential := &OAuthCredential{OAuthCredentials: OAuthCredentials{
		Access: "token", Extra: map[string]json.RawMessage{RadiusCredentialConfigKey: configJSON},
	}}
	models := GetRadiusModels("radius", credential)
	if len(models) != 1 {
		t.Fatalf("models = %+v", models)
	}
	model := models[0]
	if model.ID != "balanced" || model.Provider != "radius" || model.API != APIPiMessages ||
		model.BaseURL != "https://gateway.example" || !model.Reasoning {
		t.Fatalf("model = %+v", model)
	}
	if got := GetRadiusModels("radius", nil); len(got) != 0 {
		t.Fatalf("models = %+v", got)
	}
	// A malformed cached config yields nothing.
	bad := &OAuthCredential{OAuthCredentials: OAuthCredentials{
		Extra: map[string]json.RawMessage{RadiusCredentialConfigKey: json.RawMessage(`"nope"`)},
	}}
	if got := GetRadiusModels("radius", bad); len(got) != 0 {
		t.Fatalf("models = %+v", got)
	}
}

func TestLoadRadiusGatewayConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/config" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		if request.Header.Get("authorization") != "Bearer api-key" {
			t.Errorf("authorization = %q", request.Header.Get("authorization"))
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"baseUrl":"https://gw","models":[{"id":"m","name":"M","reasoning":false,"input":["text"],"cost":{"input":1,"output":1},"contextWindow":1,"maxTokens":1}]}`))
	}))
	defer server.Close()

	config, err := LoadRadiusGatewayConfig(context.Background(), server.URL, "api-key")
	if err != nil {
		t.Fatal(err)
	}
	if config.BaseURL != "https://gw" || len(config.Models) != 1 {
		t.Fatalf("config = %+v", config)
	}

	// HTTP failures include the truncated body.
	failServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte(strings.Repeat("x", 600)))
	}))
	defer failServer.Close()
	_, err = LoadRadiusGatewayConfig(context.Background(), failServer.URL, "")
	if err == nil || !strings.Contains(err.Error(), "Could not load Radius config from") ||
		!strings.Contains(err.Error(), "…") {
		t.Fatalf("err = %v", err)
	}

	// Invalid JSON is reported.
	badServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("not json"))
	}))
	defer badServer.Close()
	if _, err := LoadRadiusGatewayConfig(context.Background(), badServer.URL, ""); err == nil ||
		!strings.Contains(err.Error(), "Invalid Radius config from") {
		t.Fatalf("err = %v", err)
	}
}

func TestRadiusProviderRefresh(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"baseUrl":"https://gw","models":[{"id":"refreshed","name":"Refreshed","reasoning":true,"input":["text"],"cost":{"input":1,"output":2},"contextWindow":1000,"maxTokens":100}]}`))
	}))
	defer server.Close()

	provider := RadiusProvider(RadiusProviderOptions{Gateway: server.URL})
	if provider.ID != "radius" || provider.Name != "Radius" {
		t.Fatalf("provider = %s/%s", provider.ID, provider.Name)
	}
	// Radius has no static catalog models, so the initial list is empty.
	if len(provider.GetModels()) != 0 {
		t.Fatalf("models = %+v", provider.GetModels())
	}
	// The OAuth auth is present alongside the API key auth.
	if provider.Auth.OAuth == nil || provider.Auth.OAuth.Name != "Radius" {
		t.Fatalf("auth = %+v", provider.Auth)
	}

	// Refreshing publishes the fetched catalogue through the update closure.
	err := provider.RefreshModels(&RefreshModelsContext{
		AllowNetwork: true,
		Ctx:          context.Background(),
		Publish:      func(publication ModelsPublication) bool { publication.Update(); return true },
	})
	if err != nil {
		t.Fatal(err)
	}
	models := provider.GetModels()
	if len(models) != 1 || models[0].ID != "refreshed" || models[0].API != APIPiMessages {
		ids := make([]string, 0, len(models))
		for _, model := range models {
			ids = append(ids, model.ID+"@"+model.BaseURL)
		}
		t.Fatalf("models = %#v", ids)
	}

	// A stored entry restores its models without a network refresh.
	restoreProvider := RadiusProvider(RadiusProviderOptions{Gateway: "https://gw"})
	storedModels := []*Model{{ID: "stored", Provider: "radius", API: APIPiMessages}}
	if err := restoreProvider.RefreshModels(&RefreshModelsContext{
		Stored:  &ModelsStoreEntry{Models: storedModels},
		Publish: func(publication ModelsPublication) bool { publication.Update(); return true },
	}); err != nil {
		t.Fatal(err)
	}
	if models := restoreProvider.GetModels(); len(models) != 1 || models[0].ID != "stored" {
		t.Fatalf("models = %+v", models)
	}

	// Without network access and without a stored entry nothing happens.
	emptyProvider := RadiusProvider(RadiusProviderOptions{Gateway: "https://gw"})
	if err := emptyProvider.RefreshModels(&RefreshModelsContext{
		Publish: func(ModelsPublication) bool { return true },
	}); err != nil {
		t.Fatal(err)
	}
	if models := emptyProvider.GetModels(); len(models) != 0 {
		t.Fatalf("models = %+v", models)
	}
}

func TestRadiusOAuthDiscoveryAndToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/oauth":
			_, _ = writer.Write([]byte(`{"authorizationEndpoint":"https://gw.example/oauth/authorize"}`))
		case "/v1/oauth/token":
			form := readForm(t, request)
			if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "refresh-1" {
				t.Errorf("form = %v", form)
			}
			_, _ = writer.Write([]byte(`{"access_token":"access-1","refresh_token":"refresh-2","expires_in":3600,"scope":"gateway"}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	discovery, err := LoadRadiusOAuthDiscovery(context.Background(), server.URL)
	if err != nil || discovery.AuthorizationEndpoint != "https://gw.example/oauth/authorize" {
		t.Fatalf("discovery = %+v err = %v", discovery, err)
	}

	credential, err := RequestRadiusOAuthToken(context.Background(), server.URL, map[string]string{
		"grant_type": "refresh_token", "refresh_token": "refresh-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if credential.Access != "access-1" || credential.Refresh != "refresh-2" {
		t.Fatalf("credential = %+v", credential)
	}
	// The expiry keeps a one-minute skew.
	expected := nowMillisApprox() + 3600*1000 - radiusTokenSkewMS
	if diff := credential.Expires - expected; diff > 5000 || diff < -5000 {
		t.Fatalf("expires off by %d", diff)
	}
	var scope string
	_ = json.Unmarshal(credential.Extra["scope"], &scope)
	if scope != "gateway" {
		t.Fatalf("scope = %q", scope)
	}

	// Invalid discovery is reported.
	badServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer badServer.Close()
	if _, err := LoadRadiusOAuthDiscovery(context.Background(), badServer.URL); err == nil ||
		!strings.Contains(err.Error(), "Invalid Radius OAuth config from") {
		t.Fatalf("err = %v", err)
	}
}

func TestRadiusOAuthErrorParsing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = writer.Write([]byte(`{"error":"invalid_grant","error_description":"expired"}`))
	}))
	defer server.Close()

	_, err := RequestRadiusOAuthToken(context.Background(), server.URL, map[string]string{"grant_type": "refresh_token"})
	if err == nil || err.Error() != "Radius OAuth token request failed: invalid_grant: expired" {
		t.Fatalf("err = %v", err)
	}
	var coded *radiusOAuthResponseError
	if !asError(err, &coded) || coded.Status != 400 || coded.OAuthError != "invalid_grant" {
		t.Fatalf("coded = %+v", coded)
	}

	// A non-JSON body becomes the description.
	plainServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = writer.Write([]byte("upstream down"))
	}))
	defer plainServer.Close()
	_, err = RequestRadiusOAuthToken(context.Background(), plainServer.URL, map[string]string{})
	if err == nil || err.Error() != "Radius OAuth token request failed: upstream down" {
		t.Fatalf("err = %v", err)
	}

	// A response without a body reports the status.
	emptyServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer emptyServer.Close()
	_, err = RequestRadiusOAuthToken(context.Background(), emptyServer.URL, map[string]string{})
	if err == nil || err.Error() != "Radius OAuth token request failed: 401" {
		t.Fatalf("err = %v", err)
	}
}

func TestRadiusDeviceCodeLogin(t *testing.T) {
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/oauth/device":
			form := readForm(t, request)
			if form.Get("client_id") != RadiusOAuthClientID || form.Get("scope") != RadiusOAuthScope {
				t.Errorf("form = %v", form)
			}
			_, _ = writer.Write([]byte(`{"device_code":"dev","user_code":"CODE","verification_uri":"https://gw.example/device","expires_in":600,"interval":1}`))
		case "/v1/oauth/token":
			polls++
			if polls == 1 {
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = writer.Write([]byte(`{"error":"authorization_pending"}`))
				return
			}
			_, _ = writer.Write([]byte(`{"access_token":"access","refresh_token":"refresh","expires_in":60}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	var notified []AuthEvent
	credential, err := LoginRadiusWithDeviceCode(&AuthInteraction{
		Ctx:    context.Background(),
		Notify: func(event AuthEvent) { notified = append(notified, event) },
	}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if credential.Access != "access" {
		t.Fatalf("credential = %+v", credential)
	}
	if len(notified) != 1 || notified[0].Type != AuthEventDeviceCode || notified[0].UserCode != "CODE" ||
		notified[0].VerificationURI != "https://gw.example/device" || notified[0].IntervalSeconds != 1 {
		t.Fatalf("notified = %+v", notified)
	}

	// Failure codes map to upstream's messages.
	for _, testCase := range []struct{ code, want string }{
		{"expired_token", "Device authorization expired."},
		{"access_denied", "Device authorization was denied."},
	} {
		failServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			if request.URL.Path == "/v1/oauth/device" {
				_, _ = writer.Write([]byte(`{"device_code":"d","user_code":"u","verification_uri":"https://gw/device","expires_in":60,"interval":1}`))
				return
			}
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = writer.Write([]byte(`{"error":"` + testCase.code + `"}`))
		}))
		_, err := LoginRadiusWithDeviceCode(&AuthInteraction{Ctx: context.Background()}, failServer.URL)
		failServer.Close()
		if err == nil || err.Error() != testCase.want {
			t.Errorf("%s: err = %v", testCase.code, err)
		}
	}

	// A device response missing fields is reported.
	missingServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"device_code":"d"}`))
	}))
	defer missingServer.Close()
	if _, err := RequestRadiusDeviceAuthorization(context.Background(), missingServer.URL); err == nil ||
		err.Error() != "Radius OAuth device authorization response is missing required fields" {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateRadiusOAuthDefinition(t *testing.T) {
	auth := CreateRadiusOAuth(RadiusOAuthOptions{Name: "Radius", Gateway: "radius.pi.dev/"})
	if auth.Name != "Radius" {
		t.Fatalf("auth = %+v", auth)
	}
	// An unknown sign-in method is reported.
	_, err := auth.Login(&AuthInteraction{
		Ctx:    context.Background(),
		Prompt: func(AuthPrompt) (string, error) { return "other", nil },
	})
	if err == nil || err.Error() != "Unknown Radius sign-in method: other" {
		t.Fatalf("err = %v", err)
	}
	modelAuth, err := auth.ToAuth(&OAuthCredential{OAuthCredentials: OAuthCredentials{Access: "access"}})
	if err != nil || modelAuth.APIKey != "access" {
		t.Fatalf("model auth = %+v err = %v", modelAuth, err)
	}
	if _, err := auth.ToAuth(nil); err == nil {
		t.Fatal("nil credentials must be rejected")
	}
	if _, err := auth.Refresh(nil, context.Background()); err == nil {
		t.Fatal("nil credentials must be rejected on refresh")
	}
}

func nowMillisApprox() int64 { return time.Now().UnixMilli() }
