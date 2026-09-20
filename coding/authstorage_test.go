package coding

import (
	ctxpkg "context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dat267/gpi/ai"
)

// Round 96 tests: the auth.json credential storage (file/in-memory backends,
// read-only storage, reload coalescing) and getFileRevision.

func writeAuthFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAuthStorageFileBackendRoundTrip(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	storage := NewAuthStorage(authPath)
	ctx := ctxpkg.Background()

	// A missing file lists nothing.
	entries, err := storage.List(ctx)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries = %+v err = %v", entries, err)
	}
	if _, err := os.Stat(authPath); err != nil {
		t.Fatalf("auth.json must be created: %v", err)
	}

	// Modify writes the credential.
	credential, err := storage.Modify("anthropic", func(current *ai.Credential) (*ai.Credential, error) {
		if current != nil {
			t.Fatalf("current = %+v", current)
		}
		return &ai.Credential{Type: ai.CredentialAPIKey, APIKey: &ai.ApiKeyCredential{Key: "sk-1"}}, nil
	}, ctx)
	if err != nil || credential.APIKey.Key != "sk-1" {
		t.Fatalf("credential = %+v err = %v", credential, err)
	}
	raw, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"anthropic\": {\n    \"type\": \"api_key\",\n    \"key\": \"sk-1\"\n  }\n}"
	if string(raw) != want {
		t.Fatalf("file = %q want %q", raw, want)
	}
	info, err := os.Stat(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}

	// Read resolves the stored value; a second store sees it (shared snapshot).
	read, err := storage.Read("anthropic", ctx)
	if err != nil || read.APIKey.Key != "sk-1" {
		t.Fatalf("read = %+v err = %v", read, err)
	}
	other := NewAuthStorage(authPath)
	if read, err := other.Read("anthropic", ctx); err != nil || read.APIKey.Key != "sk-1" {
		t.Fatalf("read = %+v err = %v", read, err)
	}

	// A returned nil leaves the entry unchanged.
	if _, err := storage.Modify("anthropic", func(current *ai.Credential) (*ai.Credential, error) { return nil, nil }, ctx); err != nil {
		t.Fatal(err)
	}
	if read, _ := storage.Read("anthropic", ctx); read.APIKey.Key != "sk-1" {
		t.Fatalf("read = %+v", read)
	}

	// An error from the callback propagates and writes nothing.
	boom := errors.New("no write")
	if _, err := storage.Modify("anthropic", func(current *ai.Credential) (*ai.Credential, error) { return nil, boom }, ctx); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if read, _ := storage.Read("anthropic", ctx); read.APIKey.Key != "sk-1" {
		t.Fatalf("read = %+v", read)
	}

	// Delete removes the provider.
	if err := storage.Delete("anthropic", ctx); err != nil {
		t.Fatal(err)
	}
	if read, _ := storage.Read("anthropic", ctx); read != nil {
		t.Fatalf("read = %+v", read)
	}
	entries, err = storage.List(ctx)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries = %+v err = %v", entries, err)
	}
}

func TestAuthStorageConfiguredKeyResolution(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	writeAuthFile(t, authPath, `{"anthropic":{"type":"api_key","key":"$AUTH_TEST_KEY","env":{"AUTH_TEST_KEY":"from-env"}}}`)
	storage := NewAuthStorage(authPath)

	read, err := storage.Read("anthropic", ctxpkg.Background())
	if err != nil || read.APIKey.Key != "from-env" {
		t.Fatalf("read = %+v err = %v", read, err)
	}
	// List never resolves configured values.
	entries, err := storage.List(ctxpkg.Background())
	if err != nil || len(entries) != 1 || entries[0].ProviderID != "anthropic" || entries[0].Type != ai.CredentialAPIKey {
		t.Fatalf("entries = %+v err = %v", entries, err)
	}

	// The writable store resolves a command key by running it (the read-only
	// store is the one that returns it unresolved).
	writeAuthFile(t, authPath, `{"anthropic":{"type":"api_key","key":"!printf sk-cmd"}}`)
	command := NewAuthStorage(authPath)
	read, err = command.Read("anthropic", ctxpkg.Background())
	if err != nil || read.APIKey.Key != "sk-cmd" {
		t.Fatalf("read = %+v err = %v", read, err)
	}
	readOnly := NewReadOnlyAuthStorage(authPath)
	read, err = readOnly.Read("anthropic", ctxpkg.Background())
	if err != nil || read.APIKey.Key != "!printf sk-cmd" {
		t.Fatalf("read-only read = %+v err = %v", read, err)
	}
}

func TestAuthStorageExternalChange(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	writeAuthFile(t, authPath, `{"anthropic":{"type":"api_key","key":"sk-1"}}`)
	storage := NewAuthStorage(authPath)
	ctx := ctxpkg.Background()
	if read, _ := storage.Read("anthropic", ctx); read.APIKey.Key != "sk-1" {
		t.Fatalf("read = %+v", read)
	}

	// Change the file behind the store; the revision check picks it up.
	time.Sleep(5 * time.Millisecond)
	writeAuthFile(t, authPath, `{"anthropic":{"type":"api_key","key":"sk-2"},"openai":{"type":"api_key","key":"sk-3"}}`)
	if read, err := storage.Read("anthropic", ctx); err != nil || read.APIKey.Key != "sk-2" {
		t.Fatalf("read = %+v err = %v", read, err)
	}
	if read, err := storage.Read("openai", ctx); err != nil || read.APIKey.Key != "sk-3" {
		t.Fatalf("read = %+v err = %v", read, err)
	}
}

func TestAuthStorageInMemoryAndReadOnly(t *testing.T) {
	ctx := ctxpkg.Background()

	// The in-memory backend seeds data and refuses nothing.
	storage := AuthStorageInMemory(authStorageData{
		"openai": {Type: ai.CredentialAPIKey, APIKey: &ai.ApiKeyCredential{Key: "sk-in-memory"}},
	})
	if read, err := storage.Read("openai", ctx); err != nil || read.APIKey.Key != "sk-in-memory" {
		t.Fatalf("read = %+v err = %v", read, err)
	}
	if _, err := storage.Modify("openai", func(current *ai.Credential) (*ai.Credential, error) {
		return &ai.Credential{Type: ai.CredentialAPIKey, APIKey: &ai.ApiKeyCredential{Key: "sk-new"}}, nil
	}, ctx); err != nil {
		t.Fatal(err)
	}
	if read, _ := storage.Read("openai", ctx); read.APIKey.Key != "sk-new" {
		t.Fatalf("read = %+v", read)
	}

	// A backend without a file path reloads on every read.
	backend := NewInMemoryAuthStorageBackend()
	seeded := `{"openai":{"type":"api_key","key":"sk-1"}}`
	if _, err := backend.WithLock(func(current *string) (LockResult, error) {
		return LockResult{Next: &seeded}, nil
	}); err != nil {
		t.Fatal(err)
	}
	pathless := AuthStorageFromBackend(backend)
	if read, err := pathless.Read("openai", ctx); err != nil || read.APIKey.Key != "sk-1" {
		t.Fatalf("read = %+v err = %v", read, err)
	}
	updated := `{"openai":{"type":"api_key","key":"sk-2"}}`
	if _, err := backend.WithLock(func(current *string) (LockResult, error) {
		return LockResult{Next: &updated}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if read, err := pathless.Read("openai", ctx); err != nil || read.APIKey.Key != "sk-2" {
		t.Fatalf("read = %+v err = %v", read, err)
	}

	// The read-only storage parses and validates but never writes.
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	writeAuthFile(t, authPath, `{"anthropic":{"type":"api_key","key":"sk-readonly"},"openai":{"type":"oauth","access":"a","refresh":"r","expires":123}}`)
	readOnly := NewReadOnlyAuthStorage(authPath)
	read, err := readOnly.Read("anthropic", ctx)
	if err != nil || read.APIKey.Key != "sk-readonly" {
		t.Fatalf("read = %+v err = %v", read, err)
	}
	oauth, err := readOnly.Read("openai", ctx)
	if err != nil || oauth.Type != ai.CredentialOAuth || oauth.OAuth.Access != "a" {
		t.Fatalf("oauth = %+v err = %v", oauth, err)
	}
	entries, err := readOnly.List(ctx)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %+v err = %v", entries, err)
	}
	if _, err := readOnly.Modify("anthropic", func(current *ai.Credential) (*ai.Credential, error) { return nil, nil }, ctx); err == nil ||
		err.Error() != "Read-only credential storage cannot modify auth.json" {
		t.Fatalf("err = %v", err)
	}
	if err := readOnly.Delete("anthropic", ctx); err == nil ||
		err.Error() != "Read-only credential storage cannot modify auth.json" {
		t.Fatalf("err = %v", err)
	}
}

func TestReadOnlyAuthStorageValidation(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	ctx := ctxpkg.Background()

	// Invalid JSON.
	writeAuthFile(t, authPath, "not json")
	if _, err := NewReadOnlyAuthStorage(authPath).List(ctx); err == nil ||
		!strings.HasPrefix(err.Error(), "Failed to read auth.json:") {
		t.Fatalf("err = %v", err)
	}
	// Arrays are not objects.
	writeAuthFile(t, authPath, "[]")
	if _, err := NewReadOnlyAuthStorage(authPath).List(ctx); err == nil ||
		err.Error() != "Invalid auth.json: expected an object" {
		t.Fatalf("err = %v", err)
	}
	// A non-object credential.
	writeAuthFile(t, authPath, `{"anthropic":"nope"}`)
	if _, err := NewReadOnlyAuthStorage(authPath).List(ctx); err == nil ||
		err.Error() != `Invalid auth.json credential for provider "anthropic"` {
		t.Fatalf("err = %v", err)
	}
	// An api_key with a non-string key.
	writeAuthFile(t, authPath, `{"anthropic":{"type":"api_key","key":5}}`)
	if _, err := NewReadOnlyAuthStorage(authPath).List(ctx); err == nil {
		t.Fatal("numeric key must fail")
	}
	// An api_key with a non-string env value.
	writeAuthFile(t, authPath, `{"anthropic":{"type":"api_key","key":"k","env":{"A":1}}}`)
	if _, err := NewReadOnlyAuthStorage(authPath).List(ctx); err == nil {
		t.Fatal("numeric env must fail")
	}
	// An oauth credential missing expires.
	writeAuthFile(t, authPath, `{"openai":{"type":"oauth","access":"a","refresh":"r"}}`)
	if _, err := NewReadOnlyAuthStorage(authPath).List(ctx); err == nil {
		t.Fatal("missing expires must fail")
	}
	// An unknown type.
	writeAuthFile(t, authPath, `{"x":{"type":"other"}}`)
	if _, err := NewReadOnlyAuthStorage(authPath).List(ctx); err == nil {
		t.Fatal("unknown type must fail")
	}
	// A missing file is an empty store.
	if entries, err := NewReadOnlyAuthStorage(filepath.Join(dir, "missing.json")).List(ctx); err != nil || len(entries) != 0 {
		t.Fatalf("entries = %+v err = %v", entries, err)
	}
}

func TestReadStoredCredential(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	writeAuthFile(t, authPath, `{"anthropic":{"type":"api_key","key":"sk-stored"}}`)
	credential := ReadStoredCredential("anthropic", authPath)
	if credential == nil || credential.APIKey.Key != "sk-stored" {
		t.Fatalf("credential = %+v", credential)
	}
	if credential := ReadStoredCredential("missing", authPath); credential != nil {
		t.Fatalf("credential = %+v", credential)
	}
	if credential := ReadStoredCredential("anthropic", filepath.Join(dir, "nope.json")); credential != nil {
		t.Fatalf("credential = %+v", credential)
	}
}

func TestAuthStorageLockingAndCancellation(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	writeAuthFile(t, authPath, `{"anthropic":{"type":"api_key","key":"sk-1"}}`)
	storage := NewAuthStorage(authPath)

	// Concurrent modifies serialize; every distinct provider survives.
	var waitGroup sync.WaitGroup
	for index := 0; index < 8; index++ {
		waitGroup.Add(1)
		go func(index int) {
			defer waitGroup.Done()
			providerID := "provider-" + string(rune('a'+index))
			if _, err := storage.Modify(providerID, func(current *ai.Credential) (*ai.Credential, error) {
				return &ai.Credential{Type: ai.CredentialAPIKey, APIKey: &ai.ApiKeyCredential{Key: "sk-" + providerID}}, nil
			}, ctxpkg.Background()); err != nil {
				t.Errorf("modify: %v", err)
			}
		}(index)
	}
	waitGroup.Wait()

	raw, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	if len(onDisk) != 9 {
		t.Fatalf("on disk = %v", onDisk)
	}

	// A cancelled context fails fast.
	cancelled, cancel := ctxpkg.WithCancel(ctxpkg.Background())
	cancel()
	if _, err := storage.Read("anthropic", cancelled); !errors.Is(err, ctxpkg.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if err := storage.Delete("anthropic", cancelled); !errors.Is(err, ctxpkg.Canceled) {
		t.Fatalf("err = %v", err)
	}

	// A held lock makes the sync path wait rather than corrupt the file.
	release, err := acquireAuthLockDir(authPath)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		// The sync path retries for ~200ms then succeeds once released.
		time.Sleep(50 * time.Millisecond)
		release()
	}()
	if _, err := storage.Modify("late", func(current *ai.Credential) (*ai.Credential, error) {
		return &ai.Credential{Type: ai.CredentialAPIKey, APIKey: &ai.ApiKeyCredential{Key: "sk-late"}}, nil
	}, ctxpkg.Background()); err != nil {
		t.Fatalf("modify: %v", err)
	}
	<-done
	if read, err := storage.Read("late", ctxpkg.Background()); err != nil || read.APIKey.Key != "sk-late" {
		t.Fatalf("read = %+v err = %v", read, err)
	}
}

func TestAuthLockStaleness(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "auth.json")
	// A stale lock directory is reclaimed.
	lockPath := authLockDir(target)
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	release, err := acquireAuthLockDir(target)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	release()
	if PathExists(lockPath) {
		t.Fatal("lock must be released")
	}

	// A fresh lock is respected.
	release, err = acquireAuthLockDir(target)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := acquireAuthLockDir(target); !errors.Is(err, errAuthLocked) {
		t.Fatalf("err = %v", err)
	}
}

func TestGetFileRevision(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "file.txt")
	writeAuthFile(t, path, "one")
	first, ok := GetFileRevision(path)
	if !ok || first == "" {
		t.Fatalf("revision = %q ok = %v", first, ok)
	}
	// Rewriting the same content can still change the revision (mtime/ctime).
	time.Sleep(5 * time.Millisecond)
	writeAuthFile(t, path, "two")
	second, ok := GetFileRevision(path)
	if !ok || second == first {
		t.Fatalf("revision = %q", second)
	}
	if _, ok := GetFileRevision(filepath.Join(dir, "missing")); ok {
		t.Fatal("missing file must have no revision")
	}
}
