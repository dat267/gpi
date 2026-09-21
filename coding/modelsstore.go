package coding

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/dat267/pier/ai"
)

// Port of core/models-store.ts: locked JSON-backed storage for dynamically
// refreshed provider catalogs, plus the coding agent's in-memory store.

// InMemoryCodingAgentModelsStore is an in-memory model catalog store.
type InMemoryCodingAgentModelsStore struct {
	mu      sync.Mutex
	entries map[string]*ai.ModelsStoreEntry
}

// NewInMemoryCodingAgentModelsStore builds an empty in-memory store.
func NewInMemoryCodingAgentModelsStore() *InMemoryCodingAgentModelsStore {
	return &InMemoryCodingAgentModelsStore{entries: map[string]*ai.ModelsStoreEntry{}}
}

// Read returns a clone of the stored catalog.
func (s *InMemoryCodingAgentModelsStore) Read(providerID string, ctx context.Context) (*ai.ModelsStoreEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[providerID]
	if !ok {
		return nil, nil
	}
	return cloneModelsStoreEntry(entry), nil
}

// Write stores a clone of the catalog.
func (s *InMemoryCodingAgentModelsStore) Write(providerID string, entry *ai.ModelsStoreEntry, ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[providerID] = cloneModelsStoreEntry(entry)
	return nil
}

// Delete removes a provider's catalog.
func (s *InMemoryCodingAgentModelsStore) Delete(providerID string, ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, providerID)
	return nil
}

// storedModels is the models-store.json content: provider id to catalog.
type storedModels map[string]*ai.ModelsStoreEntry

// modelsFileReload is an in-flight catalog reload shared by concurrent readers.
type modelsFileReload struct {
	cancel  context.CancelFunc
	done    chan struct{}
	result  storedModels
	err     error
	readers int
}

// modelsFileReadState is the shared read snapshot for one path.
type modelsFileReadState struct {
	mu       sync.Mutex
	data     storedModels
	order    []string
	revision *string
	reload   *modelsFileReload
}

// FileModelsStore is locked JSON-backed storage for provider catalogs.
type FileModelsStore struct {
	storage   AuthStorageBackend
	path      string
	readState *modelsFileReadState
}

var (
	sharedModelsFileMu   sync.Mutex
	sharedModelsFilePath string
	sharedModelsFileData *modelsFileReadState
)

// NewFileModelsStore opens the catalog store at path (or the default under the
// agent dir).
func NewFileModelsStore(path string) *FileModelsStore {
	if path == "" {
		path = filepath.Join(GetAgentDir(), "models-store.json")
	}
	normalized := NormalizePath(path, PathInputOptions{})
	store := &FileModelsStore{
		path:    normalized,
		storage: NewFileAuthStorageBackend(normalized),
	}

	// The read state is shared between stores opened on the same path.
	sharedModelsFileMu.Lock()
	if sharedModelsFilePath == normalized && sharedModelsFileData != nil {
		store.readState = sharedModelsFileData
	} else {
		store.readState = &modelsFileReadState{data: storedModels{}}
		if sharedModelsFilePath == "" || sharedModelsFilePath != normalized {
			sharedModelsFilePath = normalized
			sharedModelsFileData = store.readState
		}
	}
	sharedModelsFileMu.Unlock()
	return store
}

// parseStoredModels parses models-store.json, preserving provider order.
func parseStoredModels(content *string) (storedModels, []string) {
	data := storedModels{}
	if content == nil || *content == "" {
		return data, nil
	}
	raw := []byte(StripBom(*content))
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return data, nil
	}
	var order []string
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return storedModels{}, nil
		}
		name, ok := nameToken.(string)
		if !ok {
			return storedModels{}, nil
		}
		var entryRaw json.RawMessage
		if err := decoder.Decode(&entryRaw); err != nil {
			return storedModels{}, nil
		}
		var entry ai.ModelsStoreEntry
		if err := json.Unmarshal(entryRaw, &entry); err != nil {
			return storedModels{}, nil
		}
		data[name] = &entry
		order = append(order, name)
	}
	return data, order
}

// marshalStoredModels renders models-store.json like JSON.stringify(data, null, 2).
func marshalStoredModels(data storedModels, order []string) ([]byte, error) {
	keys := make([]string, 0, len(data))
	seen := map[string]bool{}
	for _, key := range order {
		if _, ok := data[key]; ok && !seen[key] {
			keys = append(keys, key)
			seen[key] = true
		}
	}
	var remaining []string
	for key := range data {
		if !seen[key] {
			remaining = append(remaining, key)
		}
	}
	sort.Strings(remaining)
	keys = append(keys, remaining...)

	var builder strings.Builder
	builder.WriteString("{")
	for index, key := range keys {
		if index > 0 {
			builder.WriteString(",")
		}
		builder.WriteString("\n  ")
		name, err := ai.MarshalJSON(key)
		if err != nil {
			return nil, err
		}
		builder.Write(name)
		builder.WriteString(": ")
		encoded, err := ai.MarshalJSON(data[key])
		if err != nil {
			return nil, err
		}
		var indented bytes.Buffer
		if err := json.Indent(&indented, encoded, "  ", "  "); err != nil {
			return nil, err
		}
		builder.Write(indented.Bytes())
	}
	if len(keys) > 0 {
		builder.WriteString("\n")
	}
	builder.WriteString("}")
	return []byte(builder.String()), nil
}

func (s *FileModelsStore) updateReadState(data storedModels, order []string, revision *string) {
	s.readState.mu.Lock()
	s.readState.data = data
	s.readState.order = order
	s.readState.revision = revision
	s.readState.mu.Unlock()
}

func (s *FileModelsStore) snapshot() (storedModels, []string) {
	s.readState.mu.Lock()
	defer s.readState.mu.Unlock()
	return s.readState.data, s.readState.order
}

func (s *FileModelsStore) reloadFromStorage(ctx context.Context) (storedModels, error) {
	result, err := s.storage.WithLockAsync(ctx, func(content *string) (LockResult, error) {
		data, order := parseStoredModels(content)
		var revision *string
		if value, ok := GetFileRevision(s.path); ok {
			revision = &value
		}
		s.updateReadState(data, order, revision)
		return LockResult{Result: data}, nil
	})
	if err != nil {
		return nil, err
	}
	data, _ := result.(storedModels)
	return data, nil
}

// readLatest returns the freshest catalog snapshot, coalescing concurrent
// reloads.
func (s *FileModelsStore) readLatest(ctx context.Context) (storedModels, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.readState.mu.Lock()
	revision := s.readState.revision
	s.readState.mu.Unlock()
	if current, ok := GetFileRevision(s.path); ok && revision != nil && current == *revision {
		data, _ := s.snapshot()
		return data, nil
	}

	s.readState.mu.Lock()
	if s.readState.reload == nil {
		reloadCtx, cancel := context.WithCancel(context.Background())
		reload := &modelsFileReload{cancel: cancel, done: make(chan struct{})}
		s.readState.reload = reload
		go func() {
			reload.result, reload.err = s.reloadFromStorage(reloadCtx)
			close(reload.done)
			s.readState.mu.Lock()
			if s.readState.reload == reload {
				s.readState.reload = nil
			}
			s.readState.mu.Unlock()
		}()
	}
	reload := s.readState.reload
	reload.readers++
	s.readState.mu.Unlock()

	select {
	case <-reload.done:
		s.releaseReload(reload)
		if reload.err != nil {
			data, _ := s.snapshot()
			return data, nil
		}
		return reload.result, nil
	case <-ctx.Done():
		s.releaseReload(reload)
		data, _ := s.snapshot()
		return data, ctx.Err()
	}
}

func (s *FileModelsStore) releaseReload(reload *modelsFileReload) {
	s.readState.mu.Lock()
	reload.readers--
	if reload.readers == 0 && s.readState.reload == reload {
		s.readState.reload = nil
		reload.cancel()
	}
	s.readState.mu.Unlock()
}

// Read returns a clone of a provider's stored catalog.
func (s *FileModelsStore) Read(providerID string, ctx context.Context) (*ai.ModelsStoreEntry, error) {
	data, err := s.readLatest(ctx)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entry, ok := data[providerID]
	if !ok || entry == nil {
		return nil, nil
	}
	return cloneModelsStoreEntry(entry), nil
}

// Write stores a provider's catalog.
func (s *FileModelsStore) Write(providerID string, entry *ai.ModelsStoreEntry, ctx context.Context) error {
	var latest storedModels
	var latestOrder []string
	_, err := s.storage.WithLockAsync(ctx, func(content *string) (LockResult, error) {
		current, order := parseStoredModels(content)
		current[providerID] = cloneModelsStoreEntry(entry)
		latest = current
		latestOrder = appendModelsOrder(order, providerID)
		encoded, err := marshalStoredModels(current, latestOrder)
		if err != nil {
			return LockResult{}, err
		}
		text := string(encoded)
		return LockResult{Next: &text}, nil
	})
	if err != nil {
		return err
	}
	if latest != nil {
		s.updateReadState(latest, latestOrder, nil)
	}
	return nil
}

// Delete removes a provider's catalog.
func (s *FileModelsStore) Delete(providerID string, ctx context.Context) error {
	var latest storedModels
	var latestOrder []string
	_, err := s.storage.WithLockAsync(ctx, func(content *string) (LockResult, error) {
		current, order := parseStoredModels(content)
		delete(current, providerID)
		latest = current
		latestOrder = removeModelsOrder(order, providerID)
		encoded, err := marshalStoredModels(current, latestOrder)
		if err != nil {
			return LockResult{}, err
		}
		text := string(encoded)
		return LockResult{Next: &text}, nil
	})
	if err != nil {
		return err
	}
	if latest != nil {
		s.updateReadState(latest, latestOrder, nil)
	}
	return nil
}

func appendModelsOrder(order []string, providerID string) []string {
	for _, key := range order {
		if key == providerID {
			return order
		}
	}
	return append(append([]string{}, order...), providerID)
}

func removeModelsOrder(order []string, providerID string) []string {
	out := make([]string, 0, len(order))
	for _, key := range order {
		if key != providerID {
			out = append(out, key)
		}
	}
	return out
}

// cloneModelsStoreEntry deep-copies a catalog entry (structuredClone).
func cloneModelsStoreEntry(entry *ai.ModelsStoreEntry) *ai.ModelsStoreEntry {
	if entry == nil {
		return nil
	}
	encoded, err := ai.MarshalJSON(entry)
	if err != nil {
		return nil
	}
	var cloned ai.ModelsStoreEntry
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return nil
	}
	return &cloned
}

var _ = os.ErrNotExist
