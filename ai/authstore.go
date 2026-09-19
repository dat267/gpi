package ai

import (
	"context"
	"sync"
)

// Port of auth/credential-store.ts (InMemoryCredentialStore) and
// models-store.ts (InMemoryModelsStore).

// InMemoryCredentialStore is the default in-memory credential store. Apps
// inject persistent stores. Writes are serialized per provider id (upstream
// serializes through a promise chain; Go uses a per-id mutex with the same
// mutual-exclusion guarantee).
type InMemoryCredentialStore struct {
	mu    sync.Mutex
	creds map[string]*Credential
}

func NewInMemoryCredentialStore() *InMemoryCredentialStore {
	return &InMemoryCredentialStore{creds: map[string]*Credential{}}
}

func (s *InMemoryCredentialStore) Read(providerID string, ctx context.Context) (*Credential, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.creds[providerID]; ok {
		cloned := *c
		return &cloned, nil
	}
	return nil, nil
}

func (s *InMemoryCredentialStore) List(ctx context.Context) ([]CredentialInfo, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CredentialInfo, 0, len(s.creds))
	for id, c := range s.creds {
		out = append(out, CredentialInfo{ProviderID: id, Type: c.Type})
	}
	return out, nil
}

func (s *InMemoryCredentialStore) Modify(providerID string, fn func(current *Credential) (*Credential, error), ctx context.Context) (*Credential, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.creds[providerID]
	next, err := fn(current)
	if err != nil {
		return nil, err
	}
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	if next != nil {
		s.creds[providerID] = next
	}
	if next != nil {
		cloned := *next
		return &cloned, nil
	}
	if current != nil {
		cloned := *current
		return &cloned, nil
	}
	return nil, nil
}

func (s *InMemoryCredentialStore) Delete(providerID string, ctx context.Context) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.creds, providerID)
	return nil
}

// ModelsStoreEntry is one persisted model catalog keyed by provider ID.
type ModelsStoreEntry struct {
	Models []*Model `json:"models"`
	// LastModified is the Unix timestamp from the remote catalog's
	// Last-Modified header.
	LastModified *int64 `json:"lastModified,omitempty"`
	// CheckedAt is the Unix timestamp of the last completed remote check.
	CheckedAt *int64 `json:"checkedAt,omitempty"`
	// Etag is an opaque validator from the remote catalog's ETag header,
	// stored verbatim (quotes included) and echoed back as If-None-Match.
	Etag *string `json:"etag,omitempty"`
}

// ModelsStore is persistent model catalogs keyed by provider ID.
type ModelsStore interface {
	Read(providerID string, ctx context.Context) (*ModelsStoreEntry, error)
	Write(providerID string, entry *ModelsStoreEntry, ctx context.Context) error
	Delete(providerID string, ctx context.Context) error
}

// InMemoryModelsStore is the default in-memory models store.
type InMemoryModelsStore struct {
	mu      sync.Mutex
	entries map[string]*ModelsStoreEntry
}

func NewInMemoryModelsStore() *InMemoryModelsStore {
	return &InMemoryModelsStore{entries: map[string]*ModelsStoreEntry{}}
}

func (s *InMemoryModelsStore) Read(providerID string, ctx context.Context) (*ModelsStoreEntry, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[providerID]
	if !ok {
		return nil, nil
	}
	// structuredClone equivalent.
	enc, err := MarshalJSON(entry)
	if err != nil {
		return nil, err
	}
	var out ModelsStoreEntry
	if err := jsonUnmarshalStrict(enc, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (s *InMemoryModelsStore) Write(providerID string, entry *ModelsStoreEntry, ctx context.Context) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	enc, err := MarshalJSON(entry)
	if err != nil {
		return err
	}
	var cloned ModelsStoreEntry
	if err := jsonUnmarshalStrict(enc, &cloned); err != nil {
		return err
	}
	s.entries[providerID] = &cloned
	return nil
}

func (s *InMemoryModelsStore) Delete(providerID string, ctx context.Context) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, providerID)
	return nil
}
