package providers

import "testing"

// The Meta provider is registered like every other built-in (upstream
// metaProvider in builtinProviders()).
func TestMetaProviderIsRegistered(t *testing.T) {
	provider := MetaProvider()
	if provider.ID != "meta" || provider.Name != "Meta" {
		t.Fatalf("provider = %s/%s", provider.ID, provider.Name)
	}
	if provider.BaseURL != "https://api.meta.ai/v1" {
		t.Fatalf("baseUrl = %q", provider.BaseURL)
	}
	if provider.Auth.APIKey == nil {
		t.Fatal("meta has no api-key auth")
	}
	if provider.Auth.OAuth == nil || provider.Auth.OAuth.Name != "Meta (Muse subscription)" {
		t.Fatalf("oauth = %+v", provider.Auth.OAuth)
	}
	if len(provider.GetModels()) == 0 {
		t.Fatal("the meta provider has no models")
	}

	registered := false
	for _, id := range BuiltinProviderIDs {
		if id == "meta" {
			registered = true
		}
	}
	if !registered {
		t.Fatal("meta is missing from BuiltinProviderIDs")
	}
	found := false
	for _, builtin := range BuiltinProviders() {
		if builtin.ID == "meta" {
			found = true
		}
	}
	if !found {
		t.Fatal("BuiltinProviders does not construct meta")
	}
}
