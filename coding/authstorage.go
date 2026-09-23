package coding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dat267/pier/ai"
)

// Port of core/auth-storage.ts: credential storage backed by auth.json.
// Provider auth orchestration belongs to the model runtime.
//
// D32: upstream's AuthStorageBackend is generic over the lock result and uses
// proper-lockfile (a lock *directory* with staleness detection and an
// onCompromised callback). The Go backend uses an explicit result/next pair and
// a lock directory with the same staleness window; "compromised" is detected by
// observing that the held lock directory disappeared.

// LockResult is the outcome of a locked read-modify-write. Next is written back
// when non-nil (upstream's `next` field).
type LockResult struct {
	Result any
	Next   *string
}

// AuthStorageBackend is the storage primitive AuthStorage builds on.
type AuthStorageBackend interface {
	// WithLock runs fn while holding the lock, synchronously.
	WithLock(fn func(current *string) (LockResult, error)) (any, error)
	// WithLockAsync runs fn while holding the lock; ctx cancels waiting.
	WithLockAsync(ctx context.Context, fn func(current *string) (LockResult, error)) (any, error)
}

// authFileWriteMode applies only on creation so administrator-managed modes and
// ACLs remain intact.
const authFileWriteMode = 0o600

// authStorageData is the auth.json content: provider id to credential.
type authStorageData map[string]ai.Credential

// FileAuthStorageBackend locks and rewrites an auth.json file.
type FileAuthStorageBackend struct {
	authPath string
}

// NewFileAuthStorageBackend builds a file-backed auth storage backend.
func NewFileAuthStorageBackend(authPath string) *FileAuthStorageBackend {
	return &FileAuthStorageBackend{authPath: NormalizePath(authPath, PathInputOptions{})}
}

func (b *FileAuthStorageBackend) ensureParentDir() error {
	dir := filepath.Dir(b.authPath)
	if PathExists(dir) {
		return nil
	}
	return os.MkdirAll(dir, 0o700)
}

func (b *FileAuthStorageBackend) ensureFileExists() error {
	if PathExists(b.authPath) {
		return nil
	}
	return writeFileMode(b.authPath, "{}", authFileWriteMode)
}

// authLockDir is the directory proper-lockfile creates next to the target.
func authLockDir(path string) string { return path + ".lock" }

// acquireAuthLockSync retries for a short window, mirroring
// acquireLockSyncWithRetry (10 attempts, 20ms apart).
func (b *FileAuthStorageBackend) acquireAuthLockSync() (func(), error) {
	const maxAttempts = 10
	const delayMs = 20
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		release, err := acquireAuthLockDir(b.authPath)
		if err == nil {
			return release, nil
		}
		lastErr = err
		if !errors.Is(err, errAuthLocked) || attempt == maxAttempts {
			return nil, err
		}
		time.Sleep(delayMs * time.Millisecond)
	}
	return nil, lastErr
}

var errAuthLocked = errors.New("auth storage is locked")

// errAuthLockCompromised reports a lock that was removed while held.
var errAuthLockCompromised = errors.New("Auth storage lock was compromised")

// acquireAuthLockDir creates the lock directory, treating a stale directory as
// free.
func acquireAuthLockDir(path string) (func(), error) {
	lockPath := authLockDir(path)
	info, err := os.Stat(lockPath)
	if err == nil {
		if time.Since(info.ModTime()) > authLockStaleMs*time.Millisecond {
			// Stale: the holder died. Remove and retry.
			if removeErr := os.RemoveAll(lockPath); removeErr != nil {
				return nil, removeErr
			}
		} else {
			return nil, fmt.Errorf("%w: %s", errAuthLocked, lockPath)
		}
	}
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("%w: %s", errAuthLocked, lockPath)
		}
		return nil, err
	}
	// Keep the mtime fresh so a long-held lock is not treated as stale.
	stop := make(chan struct{})
	var stopOnce sync.Once
	go func() {
		ticker := time.NewTicker(authLockStaleMs / 3 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				now := time.Now()
				_ = os.Chtimes(lockPath, now, now)
			}
		}
	}()
	released := false
	var releaseMu sync.Mutex
	return func() {
		releaseMu.Lock()
		defer releaseMu.Unlock()
		if released {
			return
		}
		released = true
		stopOnce.Do(func() { close(stop) })
		_ = os.RemoveAll(lockPath)
	}, nil
}

const authLockStaleMs = 30_000

// WithLock runs fn under the file lock.
func (b *FileAuthStorageBackend) WithLock(fn func(current *string) (LockResult, error)) (any, error) {
	if err := b.ensureParentDir(); err != nil {
		return nil, err
	}

	release, err := b.acquireAuthLockSync()
	if err != nil {
		return nil, err
	}
	defer release()

	// The file is created inside the lock: creating it before locking lets a
	// concurrent writer's content be truncated by the empty placeholder.
	if err := b.ensureFileExists(); err != nil {
		return nil, err
	}

	var current *string
	if PathExists(b.authPath) {
		raw, err := os.ReadFile(b.authPath)
		if err != nil {
			return nil, err
		}
		text := string(raw)
		current = &text
	}
	result, err := fn(current)
	if err != nil {
		return nil, err
	}
	if result.Next != nil {
		if err := writeFileMode(b.authPath, *result.Next, authFileWriteMode); err != nil {
			return nil, err
		}
	}
	return result.Result, nil
}

// WithLockAsync runs fn under the file lock with upstream's stale window and
// exponential backoff; ctx aborts waiting.
func (b *FileAuthStorageBackend) WithLockAsync(ctx context.Context, fn func(current *string) (LockResult, error)) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := b.ensureParentDir(); err != nil {
		return nil, err
	}

	release, err := b.acquireAuthLockAsync(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = release() }()

	if err := b.ensureFileExists(); err != nil {
		return nil, err
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var current *string
	if PathExists(b.authPath) {
		raw, err := os.ReadFile(b.authPath)
		if err != nil {
			return nil, err
		}
		text := string(raw)
		current = &text
	}
	result, err := fn(current)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if result.Next != nil {
		if err := writeFileMode(b.authPath, *result.Next, authFileWriteMode); err != nil {
			return nil, err
		}
	}
	return result.Result, nil
}

// acquireAuthLockAsync waits for the lock until the stale deadline, backing off
// exponentially with jitter.
func (b *FileAuthStorageBackend) acquireAuthLockAsync(ctx context.Context) (func() error, error) {
	const maxDelayMs = 2_000
	deadline := time.Now().Add(authLockStaleMs * time.Millisecond)
	retry := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		release, err := acquireAuthLockDir(b.authPath)
		if err == nil {
			return func() error { release(); return nil }, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining := time.Until(deadline)
		if !errors.Is(err, errAuthLocked) || remaining <= 0 {
			return nil, err
		}
		baseDelayMs := min(math.Pow(2, float64(retry))*10, float64(maxDelayMs)/2)
		retry++
		delayMs := min(baseDelayMs*(1+rand.Float64()), float64(remaining.Milliseconds()))
		if delayMs < 1 {
			delayMs = 1
		}
		timer := time.NewTimer(time.Duration(delayMs) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

// writeFileMode writes with the creation mode masked by the process umask,
// matching Node's mode option on creation.
func writeFileMode(path, content string, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// ReadOnlyAuthStorage reads auth.json and refuses mutations.
type ReadOnlyAuthStorage struct {
	authPath string
	mu       sync.Mutex
	data     *authStorageData
}

// NewReadOnlyAuthStorage builds a read-only auth.json reader.
func NewReadOnlyAuthStorage(authPath string) *ReadOnlyAuthStorage {
	return &ReadOnlyAuthStorage{authPath: NormalizePath(authPath, PathInputOptions{})}
}

func (s *ReadOnlyAuthStorage) load() (authStorageData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data != nil {
		return *s.data, nil
	}

	raw, err := os.ReadFile(s.authPath)
	if err != nil {
		if os.IsNotExist(err) {
			empty := authStorageData{}
			s.data = &empty
			return empty, nil
		}
		return nil, fmt.Errorf("Failed to read auth.json: %v", err)
	}

	var parsed any
	if err := json.Unmarshal([]byte(StripBom(string(raw))), &parsed); err != nil {
		return nil, fmt.Errorf("Failed to read auth.json: %v", err)
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return nil, errors.New("Invalid auth.json: expected an object")
	}

	data := authStorageData{}
	for providerID, rawCredential := range object {
		credential, ok := rawCredential.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Invalid auth.json credential for provider %q", providerID)
		}
		valid := false
		if credential["type"] == "api_key" {
			key, hasKey := credential["key"]
			validKey := !hasKey || key == nil || isStringValue(key)
			validEnv := true
			if envValue, hasEnv := credential["env"]; hasEnv && envValue != nil {
				envObject, isObject := envValue.(map[string]any)
				validEnv = isObject
				if validEnv {
					for _, entry := range envObject {
						if !isStringValue(entry) {
							validEnv = false
							break
						}
					}
				}
			}
			valid = validKey && validEnv
		} else if credential["type"] == "oauth" {
			_, accessOK := credential["access"].(string)
			_, refreshOK := credential["refresh"].(string)
			expires, expiresOK := credential["expires"].(float64)
			valid = accessOK && refreshOK && expiresOK && isFinite(expires)
		}
		if !valid {
			return nil, fmt.Errorf("Invalid auth.json credential for provider %q", providerID)
		}
		encoded, err := json.Marshal(credential)
		if err != nil {
			return nil, err
		}
		var aiCredential ai.Credential
		if err := json.Unmarshal(encoded, &aiCredential); err != nil {
			return nil, fmt.Errorf("Invalid auth.json credential for provider %q", providerID)
		}
		data[providerID] = aiCredential
	}
	s.data = &data
	return data, nil
}

func isStringValue(value any) bool {
	_, ok := value.(string)
	return ok
}

func isFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

// Read returns the credential, resolving a configured API key value.
func (s *ReadOnlyAuthStorage) Read(providerID string, ctx context.Context) (*ai.Credential, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := s.load()
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	credential, ok := data[providerID]
	if !ok {
		return nil, nil
	}
	if credential.Type != ai.CredentialAPIKey || credential.APIKey == nil || credential.APIKey.Key == "" ||
		IsCommandConfigValue(credential.APIKey.Key) {
		return cloneCredential(&credential), nil
	}
	if resolved, ok := ResolveConfigValue(credential.APIKey.Key, map[string]string(credential.APIKey.Env)); ok {
		updated := cloneCredential(&credential)
		updated.APIKey.Key = resolved
		return updated, nil
	}
	return cloneCredential(&credential), nil
}

// List returns credential metadata.
func (s *ReadOnlyAuthStorage) List(ctx context.Context) ([]ai.CredentialInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]ai.CredentialInfo, 0, len(data))
	for providerID, credential := range data {
		out = append(out, ai.CredentialInfo{ProviderID: providerID, Type: credential.Type})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// Modify always fails: the storage is read-only.
func (s *ReadOnlyAuthStorage) Modify(providerID string, fn func(*ai.Credential) (*ai.Credential, error), ctx context.Context) (*ai.Credential, error) {
	return nil, errors.New("Read-only credential storage cannot modify auth.json")
}

// Delete always fails: the storage is read-only.
func (s *ReadOnlyAuthStorage) Delete(providerID string, ctx context.Context) error {
	return errors.New("Read-only credential storage cannot modify auth.json")
}

// InMemoryAuthStorageBackend keeps the auth.json text in memory and serializes
// async writers.
type InMemoryAuthStorageBackend struct {
	mu         sync.Mutex
	value      *string
	asyncChain chan struct{}
}

// NewInMemoryAuthStorageBackend builds an in-memory backend.
func NewInMemoryAuthStorageBackend() *InMemoryAuthStorageBackend {
	ready := make(chan struct{})
	close(ready)
	return &InMemoryAuthStorageBackend{asyncChain: ready}
}

func (b *InMemoryAuthStorageBackend) WithLock(fn func(current *string) (LockResult, error)) (any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	result, err := fn(b.value)
	if err != nil {
		return nil, err
	}
	if result.Next != nil {
		b.value = result.Next
	}
	return result.Result, nil
}

// WithLockAsync serializes writers behind the previous operation.
func (b *InMemoryAuthStorageBackend) WithLockAsync(ctx context.Context, fn func(current *string) (LockResult, error)) (any, error) {
	b.mu.Lock()
	previous := b.asyncChain
	b.mu.Unlock()

	done := make(chan struct{})
	b.mu.Lock()
	b.asyncChain = done
	b.mu.Unlock()

	type outcome struct {
		value any
		err   error
	}
	finished := make(chan outcome, 1)
	go func() {
		defer close(done)
		select {
		case <-previous:
		case <-ctx.Done():
			finished <- outcome{err: ctx.Err()}
			return
		}
		if err := ctx.Err(); err != nil {
			finished <- outcome{err: err}
			return
		}
		b.mu.Lock()
		result, err := fn(b.value)
		if err == nil && result.Next != nil {
			b.value = result.Next
		}
		b.mu.Unlock()
		finished <- outcome{value: result.Result, err: err}
	}()

	select {
	case result := <-finished:
		return result.value, result.err
	case <-ctx.Done():
		// Stop waiting while the operation settles in the background.
		go func() { <-finished }()
		return nil, ctx.Err()
	}
}

// AuthStorage is credential storage backed by a JSON file (upstream
// AuthStorage).
type AuthStorage struct {
	storage     AuthStorageBackend
	authPath    string
	mu          sync.Mutex
	readData    authStorageData
	readOrder   []string
	revision    *string
	reloadState *authFileReload
}

// authFileReload is an in-flight reload shared by concurrent readers.
type authFileReload struct {
	cancel  context.CancelFunc
	done    chan struct{}
	result  authStorageData
	err     error
	readers int
}

var (
	sharedAuthStorageMu   sync.Mutex
	sharedAuthStoragePath string
	sharedAuthStorageData *authStorageData
	sharedAuthStorageRev  *string
)

var _ = sharedAuthStorageRev

// NewAuthStorage opens the auth.json at authPath (or the default under the
// agent dir).
func NewAuthStorage(authPath string) *AuthStorage {
	if authPath == "" {
		authPath = filepath.Join(GetAgentDir(), "auth.json")
	}
	normalized := NormalizePath(authPath, PathInputOptions{})
	return newAuthStorageFromBackend(NewFileAuthStorageBackend(normalized), &normalized)
}

// AuthStorageFromBackend builds storage over an explicit backend (no file
// revision tracking).
func AuthStorageFromBackend(storage AuthStorageBackend) *AuthStorage {
	return newAuthStorageFromBackend(storage, nil)
}

// AuthStorageInMemory builds storage over an in-memory backend seeded with
// data.
func AuthStorageInMemory(data authStorageData) *AuthStorage {
	storage := NewInMemoryAuthStorageBackend()
	encoded, _ := marshalAuthStorageData(data, nil)
	text := string(encoded)
	_, _ = storage.WithLock(func(current *string) (LockResult, error) {
		return LockResult{Next: &text}, nil
	})
	return AuthStorageFromBackend(storage)
}

func newAuthStorageFromBackend(storage AuthStorageBackend, authPath *string) *AuthStorage {
	instance := &AuthStorage{storage: storage, readData: authStorageData{}}
	if authPath == nil {
		instance.Reload()
		return instance
	}
	instance.authPath = *authPath

	// Upstream shares one read-state snapshot across stores for the same path.
	sharedAuthStorageMu.Lock()
	if sharedAuthStoragePath == *authPath && sharedAuthStorageData != nil {
		instance.readData = *sharedAuthStorageData
		instance.revision = sharedAuthStorageRev
		sharedAuthStorageMu.Unlock()
		if revision, ok := GetFileRevision(*authPath); ok && instance.revision != nil && revision == *instance.revision {
			// The snapshot is current; skip the reload.
			return instance
		}
		instance.Reload()
		return instance
	}
	if sharedAuthStoragePath == "" {
		sharedAuthStoragePath = *authPath
		sharedAuthStorageData = &instance.readData
		sharedAuthStorageRev = instance.revision
	}
	sharedAuthStorageMu.Unlock()
	instance.Reload()
	return instance
}

// parseAuthStorageData parses auth.json content into credentials plus the
// top-level key order (upstream preserves insertion order through parse and
// stringify).
func parseAuthStorageData(content *string) (authStorageData, []string) {
	data := authStorageData{}
	if content == nil || *content == "" {
		return data, nil
	}
	raw := []byte(StripBom(*content))
	// The key order comes from the raw JSON, because Go maps have none.
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return data, nil
	}
	var order []string
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return authStorageData{}, nil
		}
		name, ok := nameToken.(string)
		if !ok {
			return authStorageData{}, nil
		}
		var credentialRaw json.RawMessage
		if err := decoder.Decode(&credentialRaw); err != nil {
			return authStorageData{}, nil
		}
		var credential ai.Credential
		if err := json.Unmarshal(credentialRaw, &credential); err != nil {
			return authStorageData{}, nil
		}
		if credential.Type == "" {
			return authStorageData{}, nil
		}
		data[name] = credential
		order = append(order, name)
	}
	return data, order
}

// marshalAuthStorageData renders auth.json like JSON.stringify(data, null, 2):
// two-space indentation, the observed key order first, then any new keys
// sorted.
func marshalAuthStorageData(data authStorageData, order []string) ([]byte, error) {
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
		credential := data[key]
		encoded, err := ai.MarshalJSON(&credential)
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

func (s *AuthStorage) updateReadState(data authStorageData, order []string, revision *string) {
	s.mu.Lock()
	s.readData = data
	s.readOrder = order
	s.revision = revision
	s.mu.Unlock()

	// Stores created for the same path share one snapshot (upstream's
	// sharedAuthFileReadState), so a later store can skip its initial reload.
	if s.authPath == "" {
		return
	}
	sharedAuthStorageMu.Lock()
	if sharedAuthStoragePath == "" {
		sharedAuthStoragePath = s.authPath
	}
	if sharedAuthStoragePath == s.authPath {
		sharedAuthStorageData = &data
		sharedAuthStorageRev = revision
	}
	sharedAuthStorageMu.Unlock()
}

// Reload refreshes credentials from storage; failures keep the last valid
// in-memory snapshot.
func (s *AuthStorage) Reload() {
	var content *string
	_, err := s.storage.WithLock(func(current *string) (LockResult, error) {
		content = current
		return LockResult{}, nil
	})
	if err != nil {
		return
	}
	var revision *string
	if s.authPath != "" {
		if value, ok := GetFileRevision(s.authPath); ok {
			revision = &value
		}
	}
	data, order := parseAuthStorageData(content)
	s.updateReadState(data, order, revision)
}

// Read returns the credential for a provider, resolving configured key values.
func (s *AuthStorage) Read(providerID string, ctx context.Context) (*ai.Credential, error) {
	data, err := s.readLatestData(ctx)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	credential, ok := data[providerID]
	if !ok {
		return nil, nil
	}
	if credential.Type != ai.CredentialAPIKey || credential.APIKey == nil || credential.APIKey.Key == "" {
		return cloneCredential(&credential), nil
	}
	resolved, ok := ResolveConfigValue(credential.APIKey.Key, map[string]string(credential.APIKey.Env))
	if !ok {
		return cloneCredential(&credential), nil
	}
	updated := cloneCredential(&credential)
	updated.APIKey.Key = resolved
	return updated, nil
}

// List returns credential metadata without resolving configured key values.
func (s *AuthStorage) List(ctx context.Context) ([]ai.CredentialInfo, error) {
	data, err := s.readLatestData(ctx)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]ai.CredentialInfo, 0, len(data))
	for providerID, credential := range data {
		out = append(out, ai.CredentialInfo{ProviderID: providerID, Type: credential.Type})
	}
	return out, nil
}

// Modify runs a read-modify-write under the storage lock.
func (s *AuthStorage) Modify(providerID string, fn func(*ai.Credential) (*ai.Credential, error), ctx context.Context) (*ai.Credential, error) {
	s.mu.Lock()
	latestData := s.readData
	latestOrder := s.readOrder
	s.mu.Unlock()
	var revision *string

	result, err := s.storage.WithLockAsync(ctx, func(content *string) (LockResult, error) {
		currentData, currentOrder := parseAuthStorageData(content)
		next, err := fn(credentialPointer(currentData[providerID]))
		if err != nil {
			return LockResult{}, err
		}
		if next == nil {
			latestData = currentData
			latestOrder = currentOrder
			if s.authPath != "" {
				if value, ok := GetFileRevision(s.authPath); ok {
					revision = &value
				}
			}
			return LockResult{Result: credentialPointer(currentData[providerID])}, nil
		}
		merged := authStorageData{}
		for key, value := range currentData {
			merged[key] = value
		}
		merged[providerID] = *next
		latestData = merged
		latestOrder = appendProviderOrder(currentOrder, providerID)
		encoded, err := marshalAuthStorageData(merged, latestOrder)
		if err != nil {
			return LockResult{}, err
		}
		text := string(encoded)
		return LockResult{Result: next, Next: &text}, nil
	})
	if err != nil {
		return nil, err
	}
	s.updateReadState(latestData, latestOrder, revision)
	credential, _ := result.(*ai.Credential)
	return credential, nil
}

// appendProviderOrder appends a provider id to the observed order when new.
func appendProviderOrder(order []string, providerID string) []string {
	for _, key := range order {
		if key == providerID {
			return order
		}
	}
	return append(append([]string{}, order...), providerID)
}

// Delete removes a provider's credential.
func (s *AuthStorage) Delete(providerID string, ctx context.Context) error {
	s.mu.Lock()
	latestData := s.readData
	latestOrder := s.readOrder
	s.mu.Unlock()

	_, err := s.storage.WithLockAsync(ctx, func(content *string) (LockResult, error) {
		currentData, currentOrder := parseAuthStorageData(content)
		delete(currentData, providerID)
		latestData = currentData
		latestOrder = removeProviderOrder(currentOrder, providerID)
		encoded, err := marshalAuthStorageData(currentData, latestOrder)
		if err != nil {
			return LockResult{}, err
		}
		text := string(encoded)
		return LockResult{Next: &text}, nil
	})
	if err != nil {
		return err
	}
	s.updateReadState(latestData, latestOrder, nil)
	return nil
}

// removeProviderOrder drops a provider id from the observed order.
func removeProviderOrder(order []string, providerID string) []string {
	out := make([]string, 0, len(order))
	for _, key := range order {
		if key != providerID {
			out = append(out, key)
		}
	}
	return out
}

// readLatestData returns the freshest snapshot, coalescing concurrent reloads
// behind one shared in-flight read.
func (s *AuthStorage) readLatestData(ctx context.Context) (authStorageData, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.authPath == "" {
		reloaded, err := s.reloadFromStorageAsync(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			return s.snapshot(), nil
		}
		return reloaded, nil
	}

	// Fast path: the file revision is unchanged.
	s.mu.Lock()
	revision := s.revision
	s.mu.Unlock()
	if current, ok := GetFileRevision(s.authPath); ok && revision != nil && current == *revision {
		return s.snapshot(), nil
	}

	// Join or start the shared reload.
	s.mu.Lock()
	if s.reloadState == nil {
		reloadCtx, cancel := context.WithCancel(context.Background())
		reload := &authFileReload{cancel: cancel, done: make(chan struct{})}
		s.reloadState = reload
		go func() {
			reload.result, reload.err = s.reloadFromStorageAsync(reloadCtx)
			close(reload.done)
			s.mu.Lock()
			if s.reloadState == reload {
				s.reloadState = nil
			}
			s.mu.Unlock()
		}()
	}
	reload := s.reloadState
	reload.readers++
	s.mu.Unlock()

	select {
	case <-reload.done:
		s.releaseReload(reload)
		if reload.err != nil {
			return s.snapshot(), nil
		}
		return reload.result, nil
	case <-ctx.Done():
		s.releaseReload(reload)
		return s.snapshot(), ctx.Err()
	}
}

// releaseReload decrements the reader count and aborts the shared reload once
// nobody is waiting for it.
func (s *AuthStorage) releaseReload(reload *authFileReload) {
	s.mu.Lock()
	reload.readers--
	if reload.readers == 0 && s.reloadState == reload {
		s.reloadState = nil
		reload.cancel()
	}
	s.mu.Unlock()
}

func (s *AuthStorage) snapshot() authStorageData {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readData
}

func (s *AuthStorage) reloadFromStorageAsync(ctx context.Context) (authStorageData, error) {
	result, err := s.storage.WithLockAsync(ctx, func(content *string) (LockResult, error) {
		currentData, currentOrder := parseAuthStorageData(content)
		var revision *string
		if s.authPath != "" {
			if value, ok := GetFileRevision(s.authPath); ok {
				revision = &value
			}
		}
		s.updateReadState(currentData, currentOrder, revision)
		return LockResult{Result: currentData}, nil
	})
	if err != nil {
		return nil, err
	}
	data, _ := result.(authStorageData)
	return data, nil
}

// ReadStoredCredential performs a one-off synchronous read of a stored
// credential without resolving configured key values.
func ReadStoredCredential(providerID, authPath string) *ai.Credential {
	if authPath == "" {
		authPath = filepath.Join(GetAgentDir(), "auth.json")
	}
	raw, err := os.ReadFile(NormalizePath(authPath, PathInputOptions{}))
	if err != nil {
		return nil
	}
	data := authStorageData{}
	if err := json.Unmarshal([]byte(StripBom(string(raw))), &data); err != nil {
		return nil
	}
	credential, ok := data[providerID]
	if !ok {
		return nil
	}
	return &credential
}

// credentialPointer returns a pointer to a credential value, or nil when the
// credential is absent (upstream passes undefined).
func credentialPointer(credential ai.Credential) *ai.Credential {
	if credential.Type == "" {
		return nil
	}
	copied := credential
	return &copied
}

func cloneCredential(credential *ai.Credential) *ai.Credential {
	if credential == nil {
		return nil
	}
	copied := *credential
	if credential.APIKey != nil {
		apiKey := *credential.APIKey
		copied.APIKey = &apiKey
	}
	if credential.OAuth != nil {
		oauth := *credential.OAuth
		copied.OAuth = &oauth
	}
	return &copied
}

var _ = strings.TrimSpace
