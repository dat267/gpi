package coding

import (
	ctxpkg "context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
)

// Round 114 tests: the remote catalog overlay, the bug-report zip archive, and
// the built-in slash commands.

func catalogProvider(t *testing.T) *ai.Provider {
	t.Helper()
	return ai.CreateProvider(ai.CreateProviderOptions{
		ID: "anthropic", Name: "Anthropic", BaseURL: "https://api.anthropic.com",
		Auth: ai.ProviderAuth{APIKey: &ai.ApiKeyAuth{Name: "key"}},
		Models: []*ai.Model{{
			ID: "builtin-model", Name: "Builtin", API: ai.APIAnthropicMessages, Provider: "anthropic",
			Input: []string{"text"}, ContextWindow: 200000, MaxTokens: 8192,
		}},
		Single: funcStreams{},
	})
}

func TestParseRemoteCatalog(t *testing.T) {
	// A bare array.
	models, err := ParseRemoteCatalog("p", []any{
		map[string]any{"id": "m1", "name": "One"},
		map[string]any{"name": "no id"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "m1" || models[0].Provider != "p" {
		t.Fatalf("models = %+v", models)
	}
	// An object with a models array.
	models, err = ParseRemoteCatalog("p", map[string]any{
		"models": []any{map[string]any{"id": "m2"}},
	})
	if err != nil || len(models) != 1 || models[0].ID != "m2" {
		t.Fatalf("models = %+v err = %v", models, err)
	}
	// An object whose values are models.
	models, err = ParseRemoteCatalog("p", map[string]any{
		"m3": map[string]any{"id": "m3"},
	})
	if err != nil || len(models) != 1 || models[0].ID != "m3" {
		t.Fatalf("models = %+v err = %v", models, err)
	}
	// Invalid payloads error.
	if _, err := ParseRemoteCatalog("p", "nope"); err == nil ||
		err.Error() != `Invalid model catalog for provider "p"` {
		t.Fatalf("err = %v", err)
	}
}

func TestRemoteCatalogOverlayAndRefresh(t *testing.T) {
	var requests []*http.Request
	etag := `"v1"`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, request)
		if request.Header.Get("if-none-match") == etag {
			writer.WriteHeader(http.StatusNotModified)
			return
		}
		writer.Header().Set("etag", etag)
		writer.Header().Set("last-modified", "Mon, 02 Jan 2006 15:04:05 GMT")
		_, _ = writer.Write([]byte(`[{"id":"remote-model","name":"Remote","api":"anthropic-messages","provider":"anthropic","reasoning":false,"input":["text"],"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0},"contextWindow":1000,"maxTokens":100}]`))
	}))
	defer server.Close()

	store := NewInMemoryCodingAgentModelsStore()
	base := catalogProvider(t)
	// No local generated-at: any stored overlay applies.
	wrapped := WithRemoteCatalog(base, server.URL, 0, false, nil)

	if wrapped.RefreshModels == nil {
		t.Fatal("refresh must be set")
	}
	// Refresh with an empty store: restores nothing, then fetches.
	ctx := ctxpkg.Background()
	if err := wrapped.RefreshModels(&ai.RefreshModelsContext{
		AllowNetwork: true, Ctx: ctx, Publish: func(p ai.ModelsPublication) bool {
			if p.Update != nil {
				p.Update()
			}
			if p.Persist != nil {
				_ = store.Write("anthropic", p.Persist, ctx)
			}
			return true
		},
	}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatalf("requests = %d", len(requests))
	}
	if requests[0].URL.Path != "/api/models/providers/anthropic" {
		t.Fatalf("url = %s", requests[0].URL)
	}
	if requests[0].Header.Get("if-none-match") != "" {
		t.Fatal("no validator on the first fetch")
	}
	// The remote model is merged over the baseline.
	models := wrapped.GetModels()
	found := false
	for _, model := range models {
		if model.ID == "remote-model" {
			found = true
		}
	}
	if !found {
		t.Fatalf("models = %+v", models)
	}
	// The persisted entry carries the etag and timestamps.
	stored, _ := store.Read("anthropic", ctx)
	if stored == nil || stored.Etag == nil || *stored.Etag != etag || stored.CheckedAt == nil {
		t.Fatalf("stored = %+v", stored)
	}

	// A refresh within the freshness window skips the network.
	requests = nil
	if err := wrapped.RefreshModels(&ai.RefreshModelsContext{
		AllowNetwork: true, Ctx: ctx, Stored: stored,
		Publish: func(p ai.ModelsPublication) bool { return true },
	}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 0 {
		t.Fatalf("requests = %d", len(requests))
	}

	// Force bypasses the window; the etag yields a 304 that only moves the
	// freshness timestamp.
	force := true
	if err := wrapped.RefreshModels(&ai.RefreshModelsContext{
		AllowNetwork: true, Ctx: ctx, Stored: stored, Force: &force,
		Publish: func(p ai.ModelsPublication) bool {
			if p.Persist != nil {
				_ = store.Write("anthropic", p.Persist, ctx)
			}
			return true
		},
	}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].Header.Get("if-none-match") != etag {
		t.Fatalf("requests = %d", len(requests))
	}
	revalidated, _ := store.Read("anthropic", ctx)
	// Same-millisecond fetches can produce equal timestamps.
	if revalidated.CheckedAt == nil || *revalidated.CheckedAt < *stored.CheckedAt {
		t.Fatalf("checkedAt must move: %+v", revalidated)
	}
	if len(revalidated.Models) != len(stored.Models) {
		t.Fatalf("304 must keep the stored models: %+v", revalidated)
	}
}

func TestRemoteCatalog404AndError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	store := NewInMemoryCodingAgentModelsStore()
	ctx := ctxpkg.Background()
	// A stored overlay is cleared when the server no longer serves it.
	lastModified := int64(500)
	etag := `"old"`
	stored := &ai.ModelsStoreEntry{
		Models: []*ai.Model{{ID: "gone", Provider: "p"}}, CheckedAt: &lastModified,
		LastModified: &lastModified, Etag: &etag,
	}
	_ = store.Write("p", stored, ctx)

	base := ai.CreateProvider(ai.CreateProviderOptions{
		ID: "p", Name: "p", Single: funcStreams{},
		Models: []*ai.Model{{ID: "base", Provider: "p"}},
	})
	wrapped := WithRemoteCatalog(base, server.URL, 0, false, nil)
	if err := wrapped.RefreshModels(&ai.RefreshModelsContext{
		AllowNetwork: true, Ctx: ctx, Stored: stored,
		Publish: func(p ai.ModelsPublication) bool {
			if p.Persist != nil {
				_ = store.Write("p", p.Persist, ctx)
			}
			return true
		},
	}); err != nil {
		t.Fatal(err)
	}
	cleared, _ := store.Read("p", ctx)
	if cleared.Models != nil || cleared.Etag != nil || *cleared.LastModified != 0 {
		t.Fatalf("cleared = %+v", cleared)
	}

	// A transient failure keeps the cached body and validator, and errors.
	failing := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusInternalServerError)
	}))
	defer failing.Close()
	_ = store.Write("p", stored, ctx)
	wrapped = WithRemoteCatalog(base, failing.URL, 0, false, nil)
	err := wrapped.RefreshModels(&ai.RefreshModelsContext{
		AllowNetwork: true, Ctx: ctx, Stored: stored,
		Publish: func(p ai.ModelsPublication) bool {
			if p.Persist != nil {
				_ = store.Write("p", p.Persist, ctx)
			}
			return true
		},
	})
	if err == nil || !strings.Contains(err.Error(), "Model catalog request failed for p: 500") {
		t.Fatalf("err = %v", err)
	}
	kept, _ := store.Read("p", ctx)
	if kept.Etag == nil || *kept.Etag != etag || len(kept.Models) != 1 {
		t.Fatalf("kept = %+v", kept)
	}
	if *kept.CheckedAt == *stored.CheckedAt {
		t.Fatal("checkedAt must move on failures")
	}
}

func TestRemoteCatalogStaleOverlayIgnored(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`[{"id":"remote"}]`))
	}))
	defer server.Close()
	ctx := ctxpkg.Background()
	// A local generated-at newer than the stored overlay ignores the overlay.
	lastModified := int64(100)
	generatedAt := int64(200)
	stored := &ai.ModelsStoreEntry{Models: []*ai.Model{{ID: "stale", Provider: "p"}}, LastModified: &lastModified}
	base := ai.CreateProvider(ai.CreateProviderOptions{
		ID: "p", Name: "p", Single: funcStreams{},
		Models: []*ai.Model{{ID: "base", Provider: "p"}},
	})
	wrapped := WithRemoteCatalog(base, server.URL, generatedAt, true, nil)
	if err := wrapped.RefreshModels(&ai.RefreshModelsContext{
		AllowNetwork: false, Ctx: ctx, Stored: stored,
		Publish: func(p ai.ModelsPublication) bool {
			if p.Update != nil {
				p.Update()
			}
			return true
		},
	}); err != nil {
		t.Fatal(err)
	}
	for _, model := range wrapped.GetModels() {
		if model.ID == "stale" {
			t.Fatal("stale overlay must not apply")
		}
	}
}

func TestBuiltinSlashCommands(t *testing.T) {
	found := map[string]bool{}
	for _, command := range BuiltinSlashCommands {
		found[command.Name] = true
		if command.Description == "" {
			t.Errorf("%s must have a description", command.Name)
		}
	}
	for _, expected := range []string{"model", "compact", "login", "quit"} {
		if !found[expected] {
			t.Errorf("missing command %q", expected)
		}
	}
	if len(BuiltinSlashCommands) != 21 {
		t.Fatalf("commands = %d", len(BuiltinSlashCommands))
	}
	if !strings.Contains(BuiltinSlashCommands[len(BuiltinSlashCommands)-1].Description, AppName) {
		t.Fatal("quit must mention the app name")
	}
}
