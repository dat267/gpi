package coding

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/dat267/pier/ai"
)

// Port of core/remote-catalog-provider.ts: a persisted pi.dev catalog overlay
// on top of a static built-in provider (closing D37).

// DefaultCatalogBaseURL is the default catalog host.
const DefaultCatalogBaseURL = "https://pi.dev"

// RemoteCatalogAttemptTimeoutMS bounds each catalog request attempt.
const RemoteCatalogAttemptTimeoutMS int64 = 4_000

// RemoteCatalogRefreshIntervalMS is how long a stored catalog stays fresh.
const RemoteCatalogRefreshIntervalMS int64 = 4 * 60 * 60 * 1000

// mergeCatalogModels merges dynamic models over a baseline by model id.
func mergeCatalogModels(baseline, dynamic []*ai.Model) []*ai.Model {
	merged := append([]*ai.Model{}, baseline...)
	for _, model := range dynamic {
		index := -1
		for i, entry := range merged {
			if entry.ID == model.ID {
				index = i
				break
			}
		}
		if index >= 0 {
			merged[index] = model
		} else {
			merged = append(merged, model)
		}
	}
	return merged
}

// ParseRemoteCatalog parses a catalog payload: a bare array, an object with a
// models array, or an object whose values are models.
func ParseRemoteCatalog(providerID string, value any) ([]*ai.Model, error) {
	var entries []any
	switch typed := value.(type) {
	case []any:
		entries = typed
	case map[string]any:
		if models, ok := typed["models"].([]any); ok {
			entries = models
		} else {
			for _, item := range typed {
				entries = append(entries, item)
			}
		}
	default:
		return nil, fmt.Errorf("Invalid model catalog for provider %q", providerID)
	}
	var models []*ai.Model
	for _, entry := range entries {
		object, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		if _, hasID := object["id"]; !hasID {
			continue
		}
		encoded, err := json.Marshal(object)
		if err != nil {
			continue
		}
		var model ai.Model
		if err := json.Unmarshal(encoded, &model); err != nil {
			continue
		}
		model.Provider = ai.ProviderId(providerID)
		models = append(models, &model)
	}
	return models, nil
}

// remoteCatalogModels decides which stored models are usable: a stored overlay
// only applies when it is newer than the local generated catalog.
func remoteCatalogModels(entry *ai.ModelsStoreEntry, localGeneratedAt int64, hasLocalGeneratedAt bool) []*ai.Model {
	if entry == nil {
		return nil
	}
	if hasLocalGeneratedAt && (entry.LastModified == nil || *entry.LastModified <= localGeneratedAt) {
		return nil
	}
	return entry.Models
}

// RemoteCatalogFetcher performs the catalog HTTP request (the shared
// management-HTTP helper; swappable in tests).
type RemoteCatalogFetcher func(ctx context.Context, request *http.Request) (*http.Response, error)

// WithRemoteCatalog adds a persisted pi.dev catalog overlay to a static
// built-in provider. The overlay only applies when the stored catalog is newer
// than the locally generated one; refreshes revalidate with the stored etag and
// keep the cached body on transient failures.
func WithRemoteCatalog(provider *ai.Provider, catalogBaseURL string, localGeneratedAt int64, hasLocalGeneratedAt bool, fetch RemoteCatalogFetcher) *ai.Provider {
	if catalogBaseURL == "" {
		catalogBaseURL = DefaultCatalogBaseURL
	}
	if fetch == nil {
		attemptTimeout := RemoteCatalogAttemptTimeoutMS
		fetch = func(ctx context.Context, request *http.Request) (*http.Response, error) {
			return FetchWithRetry(ctx, request, http.DefaultClient, FetchRetryOptions{
				AttemptTimeoutMS: &attemptTimeout,
			})
		}
	}
	dynamicModels := []*ai.Model{}

	streams := funcStreams{
		stream: func(model *ai.Model, context ai.TranscriptContext, options *ai.StreamOptions) *ai.AssistantMessageEventStream {
			return provider.Stream(model, context, options)
		},
		streamSimple: func(model *ai.Model, context ai.TranscriptContext, options *ai.SimpleStreamOptions) *ai.AssistantMessageEventStream {
			return provider.StreamSimple(model, context, options)
		},
	}
	var implementation ai.ProviderStreams = streams
	if provider.HasDeferred() {
		implementation = funcStreamsWithDeferred{funcStreams: streams, base: provider}
	}

	wrapped := ai.CreateProvider(ai.CreateProviderOptions{
		ID:      provider.ID,
		Name:    provider.Name,
		BaseURL: provider.BaseURL,
		Headers: provider.Headers,
		Auth:    provider.Auth,
		// The baseline is the built-in catalog; the persisted pi.dev overlay
		// merges over it by model id.
		ModelsGetter: func() []*ai.Model {
			return mergeCatalogModels(provider.GetModels(), dynamicModels)
		},
		Single: implementation,
	})
	wrapped.FilterModels = provider.FilterModels

	wrapped.RefreshModels = func(refresh *ai.RefreshModelsContext) error {
		stored := refresh.Stored
		restored := remoteCatalogModels(stored, localGeneratedAt, hasLocalGeneratedAt)
		var kept []*ai.Model
		for _, model := range restored {
			if string(model.Provider) == provider.ID {
				kept = append(kept, model)
			}
		}
		published := refresh.Publish(ai.ModelsPublication{
			Update: func() { dynamicModels = kept },
		})
		if !published {
			return nil
		}
		if !refresh.AllowNetwork || refresh.Ctx.Err() != nil {
			return nil
		}
		force := refresh.Force != nil && *refresh.Force
		if !force && stored != nil && stored.CheckedAt != nil && stored.LastModified != nil &&
			time.Now().UnixMilli()-*stored.CheckedAt < RemoteCatalogRefreshIntervalMS {
			return nil
		}

		ctx := refresh.Ctx
		if ctx == nil {
			ctx = context.Background()
		}
		// Only revalidate when a cached body backs the validator, so a 304 can
		// never leave the overlay empty.
		validator := ""
		if stored != nil && len(stored.Models) > 0 && stored.Etag != nil {
			validator = *stored.Etag
		}
		endpoint := strings.TrimSuffix(catalogBaseURL, "/") + "/api/models/providers/" + url.PathEscape(provider.ID)
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		request.Header.Set("accept", "application/json")
		request.Header.Set("User-Agent", PiUserAgent(Version))
		if validator != "" {
			request.Header.Set("if-none-match", validator)
		}
		response, err := fetch(ctx, request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if ctx.Err() != nil {
			return nil
		}
		checkedAt := time.Now().UnixMilli()

		// Unchanged: the stored overlay is already live, so only the freshness
		// window moves.
		if response.StatusCode == http.StatusNotModified && stored != nil {
			refresh.Publish(ai.ModelsPublication{Persist: storedWithCheckedAt(stored, checkedAt)})
			return nil
		}
		if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusNotImplemented {
			refresh.Publish(ai.ModelsPublication{Persist: storedWithoutOverlay(stored, checkedAt)})
			return nil
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			// Transient failure: the cached body and its validator stay valid, so
			// keep the etag and let the next refresh revalidate instead of
			// downloading the catalog.
			refresh.Publish(ai.ModelsPublication{Persist: storedWithCheckedAt(stored, checkedAt)})
			return fmt.Errorf("Model catalog request failed for %s: %d", provider.ID, response.StatusCode)
		}
		raw, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
		if err != nil {
			return err
		}
		var payload any
		if err := json.Unmarshal(raw, &payload); err != nil {
			return err
		}
		refreshed, err := ParseRemoteCatalog(provider.ID, payload)
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return nil
		}
		lastModified := int64(0)
		if parsed, err := http.ParseTime(response.Header.Get("last-modified")); err == nil {
			lastModified = parsed.UnixMilli()
		}
		entry := &ai.ModelsStoreEntry{Models: refreshed, CheckedAt: &checkedAt, LastModified: &lastModified}
		if etag := response.Header.Get("etag"); etag != "" {
			entry.Etag = &etag
		}
		publishedModels := remoteCatalogModels(entry, localGeneratedAt, hasLocalGeneratedAt)
		refresh.Publish(ai.ModelsPublication{
			Persist: entry,
			Update:  func() { dynamicModels = publishedModels },
		})
		return nil
	}
	return wrapped
}

// storedWithCheckedAt copies a stored entry with a new freshness timestamp.
func storedWithCheckedAt(stored *ai.ModelsStoreEntry, checkedAt int64) *ai.ModelsStoreEntry {
	copied := ai.ModelsStoreEntry{}
	if stored != nil {
		copied = *stored
	}
	copied.CheckedAt = &checkedAt
	return &copied
}

// storedWithoutOverlay clears a catalog that the server no longer serves.
func storedWithoutOverlay(stored *ai.ModelsStoreEntry, checkedAt int64) *ai.ModelsStoreEntry {
	copied := *storedWithCheckedAt(stored, checkedAt)
	copied.Models = nil
	zero := int64(0)
	copied.LastModified = &zero
	copied.Etag = nil
	return &copied
}
