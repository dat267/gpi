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
)

// Tests for auth/oauth/github-copilot.ts.

func TestNormalizeGitHubDomain(t *testing.T) {
	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{"github.com", "github.com", true},
		{"https://github.com", "github.com", true},
		{"company.ghe.com/", "company.ghe.com", true},
		{"https://company.ghe.com/path", "company.ghe.com", true},
		{"  ", "", false},
		{"not a domain", "", false},
	}
	for _, testCase := range cases {
		got, ok := NormalizeGitHubDomain(testCase.input)
		if got != testCase.want || ok != testCase.ok {
			t.Errorf("NormalizeGitHubDomain(%q) = %q/%v, want %q/%v", testCase.input, got, ok, testCase.want, testCase.ok)
		}
	}
}

func TestGitHubCopilotURLsAndBaseURL(t *testing.T) {
	urls := GitHubCopilotURLsFor("company.ghe.com")
	if urls.DeviceCodeURL != "https://company.ghe.com/login/device/code" ||
		urls.AccessTokenURL != "https://company.ghe.com/login/oauth/access_token" ||
		urls.CopilotTokenURL != "https://api.company.ghe.com/copilot_internal/v2/token" {
		t.Fatalf("urls = %+v", urls)
	}

	// The proxy endpoint comes from the token's proxy-ep claim.
	token := "tid=abc;exp=123;proxy-ep=proxy.individual.githubcopilot.com;other=1"
	if base, ok := GetBaseURLFromCopilotToken(token); !ok || base != "https://api.individual.githubcopilot.com" {
		t.Fatalf("base = %q ok = %v", base, ok)
	}
	if _, ok := GetBaseURLFromCopilotToken("tid=abc"); ok {
		t.Fatal("no proxy-ep must not resolve")
	}
	if got := GetGitHubCopilotBaseURL(token, ""); got != "https://api.individual.githubcopilot.com" {
		t.Fatalf("base = %s", got)
	}
	// Enterprise fallback and the default endpoint.
	if got := GetGitHubCopilotBaseURL("tid=abc", "company.ghe.com"); got != "https://copilot-api.company.ghe.com" {
		t.Fatalf("base = %s", got)
	}
	if got := GetGitHubCopilotBaseURL("", ""); got != "https://api.individual.githubcopilot.com" {
		t.Fatalf("base = %s", got)
	}
}

func TestParseGitHubCopilotModelCatalog(t *testing.T) {
	raw := map[string]any{"data": []any{
		map[string]any{"id": "gpt-5", "model_picker_enabled": true},
		map[string]any{"id": "claude-opus", "model_picker_enabled": true, "policy": map[string]any{"state": "disabled"}},
		map[string]any{"id": "no-tools", "model_picker_enabled": true,
			"capabilities": map[string]any{"supports": map[string]any{"tool_calls": false}}},
		map[string]any{"id": "unconfigured", "model_picker_enabled": true, "policy": map[string]any{"state": "unconfigured"}},
		map[string]any{"id": "unknown-unconfigured", "model_picker_enabled": true, "policy": map[string]any{"state": "unconfigured"}},
	}}
	catalog, err := ParseGitHubCopilotModelCatalog(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.AvailableModelIDs) != 3 || catalog.AvailableModelIDs[0] != "gpt-5" ||
		catalog.AvailableModelIDs[1] != "unconfigured" || catalog.AvailableModelIDs[2] != "unknown-unconfigured" {
		t.Fatalf("available = %#v", catalog.AvailableModelIDs)
	}
	// Only catalog-known ids are eligible for policy enabling, so none of the
	// fixture ids (which are not real catalog models) qualify.
	if len(catalog.PolicyModelIDs) != 0 {
		t.Fatalf("policy = %#v", catalog.PolicyModelIDs)
	}

	// The Individual-account fallback applies only when allowed and when the
	// picker list is empty. The policy list only considers real catalog ids.
	realID := ""
	if models := GetBuiltinModels("github-copilot"); len(models) > 0 {
		realID = models[0].ID
	}
	fallbackRaw := map[string]any{"data": []any{
		map[string]any{"id": "policy-enabled", "policy": map[string]any{"state": "enabled"}},
		map[string]any{"id": realID, "policy": map[string]any{"state": "unconfigured"}},
	}}
	catalog, err = ParseGitHubCopilotModelCatalog(fallbackRaw, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.AvailableModelIDs) != 1 || catalog.AvailableModelIDs[0] != "policy-enabled" {
		t.Fatalf("available = %#v", catalog.AvailableModelIDs)
	}
	if len(catalog.PolicyModelIDs) != 1 || catalog.PolicyModelIDs[0] != realID {
		t.Fatalf("policy = %#v, want %q", catalog.PolicyModelIDs, realID)
	}
	catalog, err = ParseGitHubCopilotModelCatalog(fallbackRaw, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.AvailableModelIDs) != 0 || len(catalog.PolicyModelIDs) != 0 {
		t.Fatalf("catalog = %+v", catalog)
	}

	// A malformed response is rejected.
	if _, err := ParseGitHubCopilotModelCatalog(map[string]any{}, false); err == nil ||
		err.Error() != "Invalid Copilot models response" {
		t.Fatalf("err = %v", err)
	}
}

func TestGitHubCopilotDeviceFlow(t *testing.T) {
	fastDeviceCodeFlows(t)
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/login/device/code":
			body := readForm(t, request)
			if body.Get("client_id") != GitHubCopilotClientID || body.Get("scope") != "read:user" {
				t.Errorf("body = %v", body)
			}
			_, _ = writer.Write([]byte(`{"device_code":"dev","user_code":"CODE-1","verification_uri":"https://github.com/login/device","interval":1,"expires_in":900}`))
		case "/login/oauth/access_token":
			polls++
			if polls == 1 {
				_, _ = writer.Write([]byte(`{"error":"authorization_pending"}`))
				return
			}
			_, _ = writer.Write([]byte(`{"access_token":"gho_token"}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	withGitHubDomain(t, server.URL, func(domain string) {
		device, err := StartGitHubDeviceFlow(context.Background(), domain)
		if err != nil {
			t.Fatal(err)
		}
		if device.DeviceCode != "dev" || device.UserCode != "CODE-1" || device.ExpiresIn != 900 ||
			device.IntervalSeconds == nil || *device.IntervalSeconds != 1 {
			t.Fatalf("device = %+v", device)
		}
		if device.VerificationURI != "https://github.com/login/device" {
			t.Fatalf("verification = %s", device.VerificationURI)
		}
		token, err := PollForGitHubAccessToken(context.Background(), domain, device)
		if err != nil || token != "gho_token" {
			t.Fatalf("token = %q err = %v", token, err)
		}
	})

	// An untrusted verification URI is rejected.
	badServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"device_code":"d","user_code":"u","verification_uri":"javascript:alert(1)","expires_in":1}`))
	}))
	defer badServer.Close()
	withGitHubDomain(t, badServer.URL, func(domain string) {
		if _, err := StartGitHubDeviceFlow(context.Background(), domain); err == nil ||
			err.Error() != "Untrusted verification_uri in device code response" {
			t.Fatalf("err = %v", err)
		}
	})

	// An error response from the token endpoint reports the upstream message.
	failServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/login/device/code" {
			_, _ = writer.Write([]byte(`{"device_code":"d","user_code":"u","verification_uri":"https://github.com/login/device","expires_in":60,"interval":1}`))
			return
		}
		_, _ = writer.Write([]byte(`{"error":"access_denied","error_description":"user said no"}`))
	}))
	defer failServer.Close()
	withGitHubDomain(t, failServer.URL, func(domain string) {
		device, err := StartGitHubDeviceFlow(context.Background(), domain)
		if err != nil {
			t.Fatal(err)
		}
		_, err = PollForGitHubAccessToken(context.Background(), domain, device)
		if err == nil || err.Error() != "Device flow failed: access_denied: user said no" {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestRefreshGitHubCopilotAccessToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer gho_token" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("Copilot-Integration-Id") != "vscode-chat" {
			t.Errorf("copilot headers missing: %v", request.Header)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"token":"tid=1;proxy-ep=proxy.individual.githubcopilot.com;","expires_at":1000000}`))
	}))
	defer server.Close()

	withGitHubDomain(t, server.URL, func(domain string) {
		credential, err := RefreshGitHubCopilotAccessToken(context.Background(), "gho_token", domain)
		if err != nil {
			t.Fatal(err)
		}
		if credential.Access != "tid=1;proxy-ep=proxy.individual.githubcopilot.com;" || credential.Refresh != "gho_token" {
			t.Fatalf("credential = %+v", credential)
		}
		// The expiry keeps a five-minute margin.
		expected := int64(1000000)*1000 - 5*60*1000
		if credential.Expires != expected {
			t.Fatalf("expires = %d, want %d", credential.Expires, expected)
		}
		// The enterprise domain is stored for later refreshes.
		var stored string
		_ = json.Unmarshal(credential.Extra[CopilotEnterpriseURLKey], &stored)
		if stored != domain {
			t.Fatalf("enterprise = %q", stored)
		}
	})

	// A malformed token response is rejected.
	badServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"token":"x"}`))
	}))
	defer badServer.Close()
	withGitHubDomain(t, badServer.URL, func(domain string) {
		if _, err := RefreshGitHubCopilotAccessToken(context.Background(), "t", domain); err == nil ||
			err.Error() != "Invalid Copilot token response fields" {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestEnableGitHubCopilotModels(t *testing.T) {
	fastDeviceCodeFlows(t)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Errorf("method = %s", request.Method)
		}
		if request.Header.Get("openai-intent") != "chat-policy" {
			t.Errorf("intent = %q", request.Header.Get("openai-intent"))
		}
		raw, _ := io.ReadAll(request.Body)
		if string(raw) != `{"state":"enabled"}` {
			t.Errorf("body = %s", raw)
		}
		switch {
		case strings.Contains(request.URL.Path, "/models/gpt-5/policy"):
			writer.WriteHeader(http.StatusOK)
		case strings.Contains(request.URL.Path, "/models/boom/policy"):
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = writer.Write([]byte("rate limited"))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	withGitHubDomain(t, server.URL, func(domain string) {
		token := "tid=1;proxy-ep=proxy.individual.githubcopilot.com;"
		ok, err := EnableGitHubCopilotModel(context.Background(), token, "gpt-5", domain)
		if err != nil || !ok {
			t.Fatalf("ok = %v err = %v", ok, err)
		}
		// A model that 404s is simply not enabled.
		ok, err = EnableGitHubCopilotModel(context.Background(), token, "ghost", domain)
		if err != nil || ok {
			t.Fatalf("ok = %v err = %v", ok, err)
		}
		// A rate-limit failure stops the batch; the ids enabled so far are
		// returned without an error (upstream swallows non-abort failures).
		enabled, err := EnableGitHubCopilotModels(context.Background(), token, []string{"gpt-5", "boom", "gpt-5"}, domain)
		if err != nil || len(enabled) != 1 || enabled[0] != "gpt-5" {
			t.Fatalf("enabled = %#v err = %v", enabled, err)
		}
	})
}

func TestGitHubCopilotOAuthDefinition(t *testing.T) {
	auth := GitHubCopilotOAuth()
	if auth.Name != "GitHub Copilot" || !auth.IsSubscription {
		t.Fatalf("auth = %+v", auth)
	}
	credential := &OAuthCredential{OAuthCredentials: OAuthCredentials{
		Access: "tid=1;proxy-ep=proxy.business.githubcopilot.com;",
		Extra: map[string]json.RawMessage{
			CopilotEnterpriseURLKey:  jsonRawString("company.ghe.com"),
			CopilotAvailableModelKey: jsonRawStringSlice([]string{"gpt-5"}),
		},
	}}
	modelAuth, err := auth.ToAuth(credential)
	if err != nil {
		t.Fatal(err)
	}
	// ToAuth derives the credential-specific proxy endpoint from the token.
	if modelAuth.APIKey != credential.Access || modelAuth.BaseURL != "https://api.business.githubcopilot.com" {
		t.Fatalf("model auth = %+v", modelAuth)
	}
	if domain := copilotEnterpriseDomain(credential); domain != "company.ghe.com" {
		t.Fatalf("domain = %s", domain)
	}
	// A credential without extras falls back to the public endpoint.
	modelAuth, err = auth.ToAuth(&OAuthCredential{OAuthCredentials: OAuthCredentials{Access: "tid=1"}})
	if err != nil || modelAuth.BaseURL != "https://api.individual.githubcopilot.com" {
		t.Fatalf("model auth = %+v err = %v", modelAuth, err)
	}
	if _, err := auth.ToAuth(nil); err == nil {
		t.Fatal("nil credentials must be rejected")
	}
}

// withGitHubDomain points the flow's endpoints at a test server by using the
// server's host and rewiring the URL builder.
func withGitHubDomain(t *testing.T, serverURL string, run func(domain string)) {
	t.Helper()
	parsed, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	// Point the three endpoints at the test server while keeping the relative
	// paths the flow expects.
	originalURLs, originalBase := githubCopilotURLsBuilder, githubCopilotBaseURLBuilder
	githubCopilotURLsBuilder = func(domain string) GitHubCopilotURLs {
		return GitHubCopilotURLs{
			DeviceCodeURL:   serverURL + "/login/device/code",
			AccessTokenURL:  serverURL + "/login/oauth/access_token",
			CopilotTokenURL: serverURL + "/copilot_internal/v2/token",
		}
	}
	githubCopilotBaseURLBuilder = func(token, enterpriseDomain string) string { return serverURL }
	defer func() {
		githubCopilotURLsBuilder = originalURLs
		githubCopilotBaseURLBuilder = originalBase
	}()
	run(parsed.Hostname())
}
