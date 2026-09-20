package coding

import (
	"bytes"
	ctxpkg "context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dat267/gpi/ai"
)

// Round 95 unit tests: config-value resolution, provider attribution, runtime
// credentials, and startup timings.

func TestParseConfigValueTemplate(t *testing.T) {
	type part struct {
		env   bool
		value string
	}
	cases := []struct {
		name  string
		input string
		want  []part
	}{
		{"literal", "sk-abc", []part{{value: "sk-abc"}}},
		{"bare env", "$ANTHROPIC_API_KEY", []part{{env: true, value: "ANTHROPIC_API_KEY"}}},
		{"braced env", "${ANTHROPIC_API_KEY}", []part{{env: true, value: "ANTHROPIC_API_KEY"}}},
		{"prefix", "Bearer $TOKEN", []part{{value: "Bearer "}, {env: true, value: "TOKEN"}}},
		{"suffix", "${TOKEN}-suffix", []part{{env: true, value: "TOKEN"}, {value: "-suffix"}}},
		{"two envs", "$A$B", []part{{env: true, value: "A"}, {env: true, value: "B"}}},
		{"escaped dollar", "$$literal", []part{{value: "$literal"}}},
		{"escaped bang", "$!literal", []part{{value: "!literal"}}},
		{"trailing dollar", "value$", []part{{value: "value$"}}},
		{"invalid brace name", "${1BAD}", []part{{value: "${1BAD}"}}},
		{"unclosed brace", "${OPEN", []part{{value: "${OPEN"}}},
		{"dollar before space", "a $ b", []part{{value: "a $ b"}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			parts := parseConfigValueTemplate(testCase.input)
			if len(parts) != len(testCase.want) {
				t.Fatalf("parts = %+v want %+v", parts, testCase.want)
			}
			for index, expected := range testCase.want {
				if parts[index].isEnv != expected.env || parts[index].value != expected.value {
					t.Fatalf("part %d = %+v want %+v", index, parts[index], expected)
				}
			}
			// Adjacent literals are merged.
			for index := 1; index < len(parts); index++ {
				if !parts[index].isEnv && !parts[index-1].isEnv {
					t.Fatalf("unmerged literals: %+v", parts)
				}
			}
		})
	}
}

func TestResolveConfigValue(t *testing.T) {
	env := map[string]string{"TOKEN": "secret", "EMPTY": ""}
	t.Setenv("PROCESS_TOKEN", "from-process")

	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{"literal", "literal", true},
		{"$TOKEN", "secret", true},
		{"${TOKEN}", "secret", true},
		{"Bearer $TOKEN", "Bearer secret", true},
		{"$PROCESS_TOKEN", "from-process", true},
		{"$MISSING", "", false},
		{"prefix-$MISSING", "", false},
		{"$EMPTY", "", false},
		{"$$TOKEN", "$TOKEN", true},
		{"$!TOKEN", "!TOKEN", true},
	}
	for _, testCase := range cases {
		got, ok := ResolveConfigValue(testCase.input, env)
		if got != testCase.want || ok != testCase.ok {
			t.Errorf("ResolveConfigValue(%q) = %q, %v; want %q, %v", testCase.input, got, ok, testCase.want, testCase.ok)
		}
	}

	// An environment map wins over the process environment.
	t.Setenv("TOKEN", "from-process")
	if got, _ := ResolveConfigValue("$TOKEN", env); got != "secret" {
		t.Fatalf("value = %q", got)
	}

	// Env var name helpers.
	if name := GetConfigValueEnvVarName("$TOKEN"); name != "TOKEN" {
		t.Fatalf("name = %q", name)
	}
	if name := GetConfigValueEnvVarName("Bearer $TOKEN"); name != "" {
		t.Fatalf("name = %q", name)
	}
	if name := GetConfigValueEnvVarName("!command"); name != "" {
		t.Fatalf("name = %q", name)
	}
	names := GetConfigValueEnvVarNames("${A}-$B-$A")
	if strings.Join(names, ",") != "A,B" {
		t.Fatalf("names = %v", names)
	}
	missing := GetMissingConfigValueEnvVarNames("$A-$MISSING", map[string]string{"A": "1"})
	if strings.Join(missing, ",") != "MISSING" {
		t.Fatalf("missing = %v", missing)
	}
	if IsConfigValueConfigured("$A", map[string]string{"A": "1"}) != true {
		t.Fatal("configured")
	}
	if IsCommandConfigValue("$A") || !IsCommandConfigValue("!echo hi") {
		t.Fatal("command detection")
	}
}

func TestResolveConfigValueCommand(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no shell available")
	}
	ClearConfigValueCache()
	value, ok := ResolveConfigValue("!printf 'sk-from-command'", nil)
	if !ok || value != "sk-from-command" {
		t.Fatalf("value = %q ok = %v", value, ok)
	}
	// The result is cached; a failing command caches its absence too.
	if value, _ := ResolveConfigValue("!printf 'sk-from-command'", nil); value != "sk-from-command" {
		t.Fatalf("cached value = %q", value)
	}
	if _, ok := ResolveConfigValue("!exit 3", nil); ok {
		t.Fatal("failing command must not resolve")
	}
	if _, ok := ResolveConfigValue("!exit 3", nil); ok {
		t.Fatal("cached failure must not resolve")
	}
	// The uncached variant re-runs.
	if value, ok := ResolveConfigValueUncached("!printf 'x'", nil); !ok || value != "x" {
		t.Fatalf("uncached value = %q ok = %v", value, ok)
	}
	// Unicode-safe environments still resolve commands.
	if value, ok := ResolveConfigValueUncached("!printf 'ok'", map[string]string{"IGNORED": "1"}); !ok || value != "ok" {
		t.Fatalf("value = %q", value)
	}
	ClearConfigValueCache()
}

func TestResolveConfigValueOrThrow(t *testing.T) {
	_, err := ResolveConfigValueOrThrow("!exit 4", "API key", nil)
	if err == nil || err.Error() != "Failed to resolve API key from shell command: exit 4" {
		t.Fatalf("err = %v", err)
	}
	_, err = ResolveConfigValueOrThrow("$MISSING_ONE", "API key", nil)
	if err == nil || err.Error() != "Failed to resolve API key from environment variable: MISSING_ONE" {
		t.Fatalf("err = %v", err)
	}
	_, err = ResolveConfigValueOrThrow("$ONE-$TWO", "Token", map[string]string{})
	if err == nil || err.Error() != "Failed to resolve Token from environment variables: ONE, TWO" {
		t.Fatalf("err = %v", err)
	}
	// A literal always resolves.
	if value, err := ResolveConfigValueOrThrow("literal", "API key", nil); err != nil || value != "literal" {
		t.Fatalf("value = %q err = %v", value, err)
	}
}

func TestResolveHeaders(t *testing.T) {
	env := map[string]string{"TOKEN": "secret"}
	headers := map[string]string{"authorization": "Bearer $TOKEN", "x-static": "v", "x-missing": "$MISSING"}
	resolved := ResolveHeaders(headers, env)
	if len(resolved) != 2 || resolved["authorization"] != "Bearer secret" || resolved["x-static"] != "v" {
		t.Fatalf("resolved = %v", resolved)
	}
	if ResolveHeaders(nil, env) != nil {
		t.Fatal("nil headers")
	}
	if ResolveHeaders(map[string]string{"x": "$MISSING"}, env) != nil {
		t.Fatal("all-missing headers must be nil")
	}

	resolved, err := ResolveHeadersOrThrow(headers, "Provider", env)
	if err == nil || !strings.Contains(err.Error(), `Failed to resolve Provider header "x-missing"`) {
		t.Fatalf("err = %v", err)
	}
	resolved, err = ResolveHeadersOrThrow(map[string]string{"a": "1"}, "Provider", env)
	if err != nil || resolved["a"] != "1" {
		t.Fatalf("resolved = %v err = %v", resolved, err)
	}
	if resolved, err := ResolveHeadersOrThrow(nil, "Provider", env); err != nil || resolved != nil {
		t.Fatalf("resolved = %v err = %v", resolved, err)
	}
}

func TestProviderAttributionHeaders(t *testing.T) {
	settings := NewSettingsManagerFromFiles(t.TempDir(), t.TempDir(), SettingsManagerCreateOptions{})
	openRouter := &ai.Model{Provider: "openrouter", BaseURL: "https://openrouter.ai/api/v1"}
	headers := GetDefaultAttributionHeaders(openRouter, settings)
	if headers["HTTP-Referer"] != "https://pi.dev" || headers["X-OpenRouter-Title"] != "pi" ||
		headers["X-OpenRouter-Categories"] != "cli-agent" {
		t.Fatalf("headers = %v", headers)
	}
	// A model whose base URL is OpenRouter qualifies even with another
	// provider id.
	viaURL := &ai.Model{Provider: "custom", BaseURL: "https://openrouter.ai/api/v1"}
	if GetDefaultAttributionHeaders(viaURL, settings)["X-OpenRouter-Title"] != "pi" {
		t.Fatal("hostname detection")
	}
	nvidia := &ai.Model{Provider: "nvidia", BaseURL: "https://integrate.api.nvidia.com/v1"}
	if GetDefaultAttributionHeaders(nvidia, settings)["X-BILLING-INVOKE-ORIGIN"] != "Pi" {
		t.Fatal("nvidia headers")
	}
	cloudflare := &ai.Model{Provider: "cloudflare-workers-ai", BaseURL: "https://api.cloudflare.com/x"}
	if GetDefaultAttributionHeaders(cloudflare, settings)["User-Agent"] != "pi-coding-agent" {
		t.Fatal("cloudflare headers")
	}
	if headers := GetDefaultAttributionHeaders(&ai.Model{Provider: "anthropic"}, settings); headers != nil {
		t.Fatalf("headers = %v", headers)
	}

	// Telemetry off (env wins over the setting) disables attribution.
	if GetDefaultAttributionHeaders(openRouter, settings) == nil {
		t.Fatal("expected headers")
	}
	if !IsInstallTelemetryEnabledWithEnv(settings, "1") || IsInstallTelemetryEnabledWithEnv(settings, "0") {
		t.Fatal("env flag")
	}
	if IsInstallTelemetryEnabledWithEnv(settings, "no") {
		t.Fatal("no is false")
	}
	t.Setenv("PI_TELEMETRY", "false")
	if IsInstallTelemetryEnabled(settings) {
		t.Fatal("PI_TELEMETRY=false must disable")
	}
	t.Setenv("PI_TELEMETRY", "")
	if !IsInstallTelemetryEnabled(settings) {
		t.Fatal("default is enabled")
	}

	// Session headers only apply to OpenCode providers/hosts.
	openCode := &ai.Model{Provider: "opencode", BaseURL: "https://opencode.ai/x"}
	session := GetSessionHeaders(openCode, "sess-1")
	if session["x-opencode-session"] != "sess-1" || session["x-opencode-client"] != "pi" {
		t.Fatalf("session = %v", session)
	}
	if GetSessionHeaders(openCode, "") != nil {
		t.Fatal("no session id")
	}
	if GetSessionHeaders(&ai.Model{Provider: "anthropic", BaseURL: "https://opencode.ai"}, "s") == nil {
		t.Fatal("host must qualify")
	}
	if GetSessionHeaders(&ai.Model{Provider: "anthropic", BaseURL: "https://api.anthropic.com"}, "s") != nil {
		t.Fatal("unrelated provider")
	}

	// The merge order is session, attribution, then caller sources.
	merged := MergeProviderAttributionHeaders(openCode, settings, "sess-2",
		map[string]string{"X-OpenRouter-Title": "override", "extra": "1"},
		map[string]string{"extra": "2"},
	)
	if merged["x-opencode-session"] != "sess-2" || merged["extra"] != "2" {
		t.Fatalf("merged = %v", merged)
	}
	if merged := MergeProviderAttributionHeaders(&ai.Model{Provider: "anthropic"}, settings, "", nil); merged != nil {
		t.Fatalf("merged = %v", merged)
	}
}

type memoryCredentials struct {
	entries  map[string]*ai.Credential
	listErr  error
	readErr  error
	modified []string
}

func (m *memoryCredentials) Read(providerID string, ctx ctxpkg.Context) (*ai.Credential, error) {
	if m.readErr != nil {
		return nil, m.readErr
	}
	return m.entries[providerID], nil
}

func (m *memoryCredentials) List(ctx ctxpkg.Context) ([]ai.CredentialInfo, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	var out []ai.CredentialInfo
	for providerID, credential := range m.entries {
		out = append(out, ai.CredentialInfo{ProviderID: providerID, Type: credential.Type})
	}
	return out, nil
}

func (m *memoryCredentials) Modify(providerID string, fn func(*ai.Credential) (*ai.Credential, error), ctx ctxpkg.Context) (*ai.Credential, error) {
	m.modified = append(m.modified, providerID)
	return fn(m.entries[providerID])
}

func (m *memoryCredentials) Delete(providerID string, ctx ctxpkg.Context) error {
	delete(m.entries, providerID)
	return nil
}

func TestRuntimeCredentials(t *testing.T) {
	ctx := ctxpkg.Background()
	store := &memoryCredentials{entries: map[string]*ai.Credential{
		"anthropic": {Type: ai.CredentialAPIKey, APIKey: &ai.ApiKeyCredential{Key: "stored"}},
	}}
	overlay := NewRuntimeCredentials(store)
	overlay.SetRuntimeAPIKey("openai", "runtime-key")
	if !overlay.HasRuntimeAPIKey("openai") || overlay.HasRuntimeAPIKey("anthropic") {
		t.Fatal("hasRuntimeApiKey")
	}

	// The override wins; otherwise the store answers.
	credential, err := overlay.Read("openai", ctx)
	if err != nil || credential.APIKey.Key != "runtime-key" {
		t.Fatalf("credential = %+v err = %v", credential, err)
	}
	credential, err = overlay.Read("anthropic", ctx)
	if err != nil || credential.APIKey.Key != "stored" {
		t.Fatalf("credential = %+v err = %v", credential, err)
	}
	if credential, err := overlay.Read("missing", ctx); err != nil || credential != nil {
		t.Fatalf("credential = %+v err = %v", credential, err)
	}

	// List merges stored metadata with the overrides.
	entries, err := overlay.List(ctx)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %+v err = %v", entries, err)
	}
	byProvider := map[string]ai.CredentialType{}
	for _, entry := range entries {
		byProvider[entry.ProviderID] = entry.Type
	}
	if byProvider["openai"] != ai.CredentialAPIKey || byProvider["anthropic"] != ai.CredentialAPIKey {
		t.Fatalf("entries = %+v", entries)
	}

	// Modify delegates; Delete drops the stored credential and the override.
	if _, err := overlay.Modify("anthropic", func(current *ai.Credential) (*ai.Credential, error) { return current, nil }, ctx); err != nil {
		t.Fatal(err)
	}
	if len(store.modified) != 1 {
		t.Fatalf("modified = %v", store.modified)
	}
	if err := overlay.Delete("openai", ctx); err != nil {
		t.Fatal(err)
	}
	if overlay.HasRuntimeAPIKey("openai") {
		t.Fatal("override must be dropped")
	}

	// A cancelled context fails before touching the store.
	cancelled, cancel := ctxpkg.WithCancel(ctx)
	cancel()
	if _, err := overlay.Read("openai", cancelled); !errors.Is(err, ctxpkg.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if err := overlay.Delete("openai", cancelled); !errors.Is(err, ctxpkg.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if _, err := overlay.List(cancelled); !errors.Is(err, ctxpkg.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestTimings(t *testing.T) {
	// Disabled by default: nothing is recorded.
	SetTimingsEnabled(nil)
	t.Setenv("PI_TIMING", "")
	ResetTimings(TimingMain)
	Time("startup", TimingMain)
	var buffer bytes.Buffer
	SetTimingsOutput(&buffer)
	defer SetTimingsOutput(os.Stderr)
	PrintTimings()
	if buffer.Len() != 0 {
		t.Fatalf("output = %q", buffer.String())
	}

	// Enabled: labels accumulate and print grouped.
	enabled := true
	SetTimingsEnabled(&enabled)
	clock := time.Unix(0, 0)
	originalNow := timingNow
	timingNow = func() time.Time { return clock }
	defer func() { timingNow = originalNow }()
	ResetTimings(TimingMain)

	Time("load-config", TimingMain)
	clock = clock.Add(120 * time.Millisecond)
	Time("load-models", TimingMain)
	clock = clock.Add(30 * time.Millisecond)

	buffer.Reset()
	PrintTimings()
	output := buffer.String()
	if !strings.Contains(output, "--- Startup Timings: main ---") ||
		!strings.Contains(output, "  load-config: 0ms") ||
		!strings.Contains(output, "  load-models: 120ms") {
		t.Fatalf("output = %q", output)
	}
	if !strings.Contains(output, "  TOTAL: 120ms") {
		t.Fatalf("output = %q", output)
	}
	if !strings.Contains(output, strings.Repeat("-", len("Startup Timings: main")+8)) {
		t.Fatalf("output = %q", output)
	}

	// An empty namespace prints nothing.
	ResetTimings(TimingExtensions)
	buffer.Reset()
	PrintTimings()
	if strings.Contains(buffer.String(), "extensions") {
		t.Fatalf("output = %q", buffer.String())
	}

	SetTimingsEnabled(nil)
}

func TestAreExperimentalFeaturesEnabled(t *testing.T) {
	t.Setenv("PI_EXPERIMENTAL", "1")
	if !AreExperimentalFeaturesEnabled() {
		t.Fatal("PI_EXPERIMENTAL=1 must enable")
	}
	t.Setenv("PI_EXPERIMENTAL", "true")
	if AreExperimentalFeaturesEnabled() {
		t.Fatal("only 1 enables")
	}
	t.Setenv("PI_EXPERIMENTAL", "")
	if AreExperimentalFeaturesEnabled() {
		t.Fatal("unset is disabled")
	}
}

func TestIsTruthyEnvFlag(t *testing.T) {
	for _, value := range []string{"1", "true", "TRUE", "yes", "Yes"} {
		if !IsTruthyEnvFlag(value) {
			t.Errorf("%q must be truthy", value)
		}
	}
	for _, value := range []string{"", "0", "false", "no", "on"} {
		if IsTruthyEnvFlag(value) {
			t.Errorf("%q must be falsy", value)
		}
	}
}
