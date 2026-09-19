package ai

import (
	"context"
	"encoding/json"
	"testing"
)

// Port of the auth-resolution and models-runtime subset of
// packages/ai/test/providers.test.ts, plus calculateCost/thinking-level
// units from models.ts. Builtin-catalog cases (models.dev data) await the
// catalog port.

type fakeAuthContext struct {
	env   map[string]string
	files []string
}

func (f *fakeAuthContext) Env(name string) (string, bool) {
	v, ok := f.env[name]
	return v, ok
}

func (f *fakeAuthContext) FileExists(path string) bool {
	for _, p := range f.files {
		if p == path {
			return true
		}
	}
	return false
}

func TestEnvApiKeyAuthPrefersStoredKeyAndFallsThroughEnvVarsInOrder(t *testing.T) {
	// Upstream: "prefers the stored credential key and falls back through
	// env vars in order".
	auth := EnvApiKeyAuth("Test", []string{"TEST_A", "TEST_B"})
	ctx := &fakeAuthContext{env: map[string]string{"TEST_A": "", "TEST_B": "b-value"}}

	// No credential: falls to the first set env var.
	result, err := auth.Resolve(AuthResolveInput{Ctx: ctx, Ctx2: bgCtx()})
	if err != nil || result == nil || result.Auth.APIKey != "b-value" || result.Source != "TEST_B" {
		t.Fatalf("ambient resolve = %+v, %v", result, err)
	}

	// Stored credential key wins.
	result, err = auth.Resolve(AuthResolveInput{
		Ctx:        ctx,
		Credential: &ApiKeyCredential{Key: "stored", Env: ProviderEnv{"PROVIDER_ID": "123"}},
		Ctx2:       bgCtx(),
	})
	if err != nil || result == nil || result.Auth.APIKey != "stored" || result.Source != "stored credential" {
		t.Fatalf("stored resolve = %+v, %v", result, err)
	}
	if result.Env["PROVIDER_ID"] != "123" {
		t.Fatalf("env = %v", result.Env)
	}

	// Unconfigured: undefined (no env var set at all).
	emptyCtx := &fakeAuthContext{env: map[string]string{}}
	resolution, err := ResolveProviderAuth("p", ProviderAuth{APIKey: auth}, NewInMemoryCredentialStore(), emptyCtx, nil)
	if err != nil || resolution != nil {
		t.Fatalf("unconfigured = %+v, %v", resolution, err)
	}
}

func TestResolveProviderAuthStoredCredentialOwnsProvider(t *testing.T) {
	// Upstream: "A stored credential owns the provider: ambient/env is
	// consulted only when nothing is stored."
	auth := EnvApiKeyAuth("Test", []string{"TEST_A"})
	ctx := &fakeAuthContext{env: map[string]string{"TEST_A": "ambient"}}
	store := NewInMemoryCredentialStore()
	if _, err := store.Modify("p", func(*Credential) (*Credential, error) {
		return &Credential{Type: CredentialAPIKey, APIKey: &ApiKeyCredential{Key: "stored"}}, nil
	}, bgCtx()); err != nil {
		t.Fatal(err)
	}
	resolution, err := ResolveProviderAuth("p", ProviderAuth{APIKey: auth}, store, ctx, nil)
	if err != nil || resolution.Auth.APIKey != "stored" {
		t.Fatalf("resolution = %+v, %v", resolution, err)
	}

	// API-key override beats the stored credential.
	resolution, err = ResolveProviderAuth("p", ProviderAuth{APIKey: auth}, store, ctx,
		&AuthResolutionOverrides{APIKey: "override"})
	if err != nil || resolution.Auth.APIKey != "override" {
		t.Fatalf("override = %+v, %v", resolution, err)
	}

	// Env overrides overlay the auth context.
	resolution, err = ResolveProviderAuth("p", ProviderAuth{APIKey: auth}, NewInMemoryCredentialStore(), ctx,
		&AuthResolutionOverrides{Env: ProviderEnv{"TEST_A": "overridden"}})
	if err != nil || resolution.Auth.APIKey != "overridden" {
		t.Fatalf("env override = %+v, %v", resolution, err)
	}
}

func TestResolveStoredOAuthRefreshesUnderLock(t *testing.T) {
	// Upstream: double-checked locking — tokens with less than five minutes
	// remaining lock, re-check, refresh once, persist before release.
	refreshes := 0
	oauth := &OAuthAuth{
		Name: "Test OAuth",
		Refresh: func(credential *OAuthCredential, ctx context.Context) (*OAuthCredential, error) {
			refreshes++
			return &OAuthCredential{OAuthCredentials{
				Refresh: "r2", Access: "a2",
				Expires: nowUnixMilli() + 3600_000,
			}}, nil
		},
		ToAuth: func(credential *OAuthCredential) (*ModelAuth, error) {
			return &ModelAuth{APIKey: credential.Access}, nil
		},
	}
	ctx := &fakeAuthContext{}
	store := NewInMemoryCredentialStore()
	if _, err := store.Modify("p", func(*Credential) (*Credential, error) {
		return &Credential{Type: CredentialOAuth, OAuth: &OAuthCredential{OAuthCredentials{
			Refresh: "r1", Access: "a1", Expires: nowUnixMilli() + 60_000, // expires soon
		}}}, nil
	}, bgCtx()); err != nil {
		t.Fatal(err)
	}

	resolution, err := ResolveProviderAuth("p", ProviderAuth{OAuth: oauth}, store, ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if refreshes != 1 {
		t.Fatalf("refreshes = %d; want 1", refreshes)
	}
	if resolution.Auth.APIKey != "a2" || resolution.Source != "OAuth" {
		t.Fatalf("resolution = %+v", resolution)
	}
	// The rotated credential was persisted.
	stored, _ := store.Read("p", bgCtx())
	if stored.OAuth.Access != "a2" {
		t.Fatalf("stored = %+v", stored.OAuth)
	}

	// A second resolution within validity does not refresh again.
	if _, err := ResolveProviderAuth("p", ProviderAuth{OAuth: oauth}, store, ctx, nil); err != nil {
		t.Fatal(err)
	}
	if refreshes != 1 {
		t.Fatalf("refreshes after reuse = %d; want 1", refreshes)
	}
}

func TestCalculateCostWithTiersAndLongCacheWrites(t *testing.T) {
	// Port of models.ts calculateCost: tier selection by total input usage
	// and Anthropic's 2x base input for 1h cache writes.
	model := &Model{
		Cost: ModelCost{ModelCostRates: ModelCostRates{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75},
			Tiers: []ModelCostTier{
				{ModelCostRates: ModelCostRates{Input: 2, Output: 10, CacheRead: 0.2, CacheWrite: 2.5}, InputTokensAbove: 100000},
			}},
	}

	usage := Usage{Input: 200, CacheRead: 50, CacheWrite: 20}
	cost := CalculateCost(model, &usage)
	// Compare with upstream's exact runtime float math (variables, not
	// constants — Go folds constant expressions with exact arithmetic while
	// JS rounds at runtime). cacheWrite is (rate*short + input*2*long) / 1e6.
	in, read, write := 3.0, 0.3, 3.75
	if cost.Input != in/1000000.0*200.0 || cost.CacheRead != read/1000000.0*50.0 {
		t.Fatalf("base cost = %+v", cost)
	}
	// longWrite = 0 in the base case.
	if cost.CacheWrite != (write*20.0+in*2.0*0.0)/1000000.0 {
		t.Fatalf("base cacheWrite cost = %v", cost.CacheWrite)
	}
	// Tier applies when total input usage (input + cacheRead + cacheWrite = 270) exceeds 100k.
	large := Usage{Input: 150000, CacheRead: 0, CacheWrite: 0}
	cost = CalculateCost(model, &large)
	tierIn := 2.0
	if cost.Input != tierIn/1000000.0*150000.0 {
		t.Fatalf("tiered input cost = %v; want %v", cost.Input, tierIn/1000000.0*150000.0)
	}

	oneHour := int64(10)
	longWrite := Usage{Input: 100, CacheWrite: 30, CacheWrite1h: &oneHour}
	cost = CalculateCost(model, &longWrite)
	// shortWrite = 20 at 3.75, longWrite = 10 at 2x base input (3*2).
	want := (write*20.0 + in*2.0*10.0) / 1000000.0
	if cost.CacheWrite != want {
		t.Fatalf("cacheWrite cost = %v; want %v", cost.CacheWrite, want)
	}
}

func TestGetSupportedAndClampThinkingLevels(t *testing.T) {
	// Port of models.ts getSupportedThinkingLevels/clampThinkingLevel.
	nonReasoning := &Model{Reasoning: false}
	if got := GetSupportedThinkingLevels(nonReasoning); len(got) != 1 || got[0] != ThinkOff {
		t.Fatalf("non-reasoning levels = %v", got)
	}

	// Reasoning without a map: all base levels, but xhigh/max require
	// explicit entries.
	plain := &Model{Reasoning: true}
	got := GetSupportedThinkingLevels(plain)
	want := []ModelThinkingLevel{"off", "minimal", "low", "medium", "high"}
	if len(got) != len(want) {
		t.Fatalf("levels = %v; want %v", got, want)
	}

	// null marks a level unsupported.
	limited := &Model{Reasoning: true, ThinkingLevelMap: ThinkingLevelMap{
		ThinkMinimal: nil,
		ThinkXHigh:   strPtr("xhigh"),
	}}
	got = GetSupportedThinkingLevels(limited)
	want = []ModelThinkingLevel{"off", "low", "medium", "high", "xhigh"}
	if len(got) != len(want) {
		t.Fatalf("levels = %v; want %v", got, want)
	}

	// Clamping: requested unsupported level walks to the nearest support.
	// Upstream walks UP through levels first when clamping.
	if got := ClampThinkingLevel(limited, ThinkMinimal); got != ThinkLow {
		t.Fatalf("clamp(minimal) = %s; want low", got)
	}
	// max is unmapped; the walk down lands on the mapped xhigh.
	if got := ClampThinkingLevel(limited, ThinkMax); got != ThinkXHigh {
		t.Fatalf("clamp(max) = %s; want xhigh", got)
	}
	if got := ClampThinkingLevel(nonReasoning, ThinkHigh); got != ThinkOff {
		t.Fatalf("clamp(high) on non-reasoning = %s; want off", got)
	}
}

func TestModelsAreEqual(t *testing.T) {
	a := &Model{ID: "m", Provider: "p"}
	if !ModelsAreEqual(a, &Model{ID: "m", Provider: "p"}) {
		t.Fatal("equal models")
	}
	if ModelsAreEqual(a, &Model{ID: "m", Provider: "q"}) {
		t.Fatal("different provider")
	}
	if ModelsAreEqual(nil, a) || ModelsAreEqual(a, nil) {
		t.Fatal("nil is never equal")
	}
}

func TestMergeHeadersCaseInsensitive(t *testing.T) {
	base := ProviderHeaders{"X-Api-Key": strPtr("a"), "Keep": strPtr("1")}
	override := ProviderHeaders{"x-api-key": strPtr("b")}
	merged := MergeHeaders(base, override)
	if len(merged) != 2 {
		t.Fatalf("merged = %v", merged)
	}
	if v := merged["x-api-key"]; v == nil || *v != "b" {
		t.Fatalf("x-api-key = %v", merged["x-api-key"])
	}
	if merged["X-Api-Key"] != nil {
		t.Fatal("case-variant key should be replaced")
	}
	if v := merged["Keep"]; v == nil || *v != "1" {
		t.Fatalf("Keep = %v", merged["Keep"])
	}
	// nil deletes.
	merged = MergeHeaders(base, ProviderHeaders{"keep": nil})
	if _, ok := merged["Keep"]; ok {
		t.Fatal("nil override should delete")
	}
}

func TestCredentialJSONRoundTrip(t *testing.T) {
	// The auth.json shape: type-tagged, one credential per provider.
	apiKey := &Credential{Type: CredentialAPIKey, APIKey: &ApiKeyCredential{Key: "k", Env: ProviderEnv{"X": "1"}}}
	enc, err := MarshalJSON(apiKey)
	if err != nil {
		t.Fatal(err)
	}
	var back Credential
	if err := json.Unmarshal(enc, &back); err != nil {
		t.Fatal(err)
	}
	if back.Type != CredentialAPIKey || back.APIKey.Key != "k" || back.APIKey.Env["X"] != "1" {
		t.Fatalf("api_key round trip = %+v (%s)", back, enc)
	}

	oauth := &Credential{Type: CredentialOAuth, OAuth: &OAuthCredential{OAuthCredentials{
		Refresh: "r", Access: "a", Expires: 123,
		Extra: map[string]json.RawMessage{"plan": json.RawMessage(`"pro"`)},
	}}}
	enc, err = MarshalJSON(oauth)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"oauth","refresh":"r","access":"a","expires":123,"plan":"pro"}`
	if string(enc) != want {
		t.Fatalf("oauth = %s; want %s", enc, want)
	}
	var back2 Credential
	if err := json.Unmarshal(enc, &back2); err != nil {
		t.Fatal(err)
	}
	if back2.OAuth.Refresh != "r" || string(back2.OAuth.Extra["plan"]) != `"pro"` {
		t.Fatalf("oauth round trip = %+v", back2.OAuth)
	}
}

func strPtr(s string) *string { return &s }
