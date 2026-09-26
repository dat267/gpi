package interactive

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dat267/pier/coding"
)

// Tests for the autocomplete wiring. The fd-backed file completion is dormant
// unless the wiring resolves the binary: a `FdPath` left empty made every
// `@`-mention completion return nothing, silently.

// The regression: the wiring must pass a resolved fd path through. Without it
// the provider's fd-backed paths short-circuit to nil.
func TestNewAutocompleteWiringResolvesFdPath(t *testing.T) {
	want := ResolveAutocompleteFdPath()
	wiring := newAutocompleteWiring(&App{})
	if wiring.FdPath != want {
		t.Fatalf("FdPath = %q, want %q — the fd-backed file completion is dead without it", wiring.FdPath, want)
	}
	if want == "" {
		t.Skip("fd is not installed; the fd-backed paths stay disabled")
	}
	if _, err := os.Stat(want); err != nil {
		t.Errorf("resolved fd path %q is not usable: %v", want, err)
	}
}

// The resolved path must be the one the provider actually receives.
func TestBaseAutocompleteProviderUsesTheResolvedFdPath(t *testing.T) {
	fdPath := ResolveAutocompleteFdPath()
	if fdPath == "" {
		t.Skip("fd is not installed")
	}

	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "visible.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, ".pi"), 0o755); err != nil {
		t.Fatal(err)
	}

	wiring := &AutocompleteWiring{
		Session:     &autocompleteTestSession{},
		SessionInfo: coding.NewSessionManager(cwd, nil),
		FdPath:      fdPath,
	}
	provider := wiring.CreateBaseAutocompleteProvider()

	suggestions := provider.GetSuggestions(context.Background(), []string{"@"}, 0, 1, true)
	if suggestions == nil || len(suggestions.Items) == 0 {
		t.Fatal("@-mention completion returned nothing with fd configured")
	}

	labels := map[string]bool{}
	for _, item := range suggestions.Items {
		labels[item.Label] = true
	}
	if !labels["visible.txt"] {
		t.Errorf("@ suggestions = %v, want the visible file", labels)
	}
	// Hidden entries are not filtered: fd runs with --hidden.
	if !labels[".pi/"] {
		t.Errorf("@ suggestions = %v, want the hidden directory too (fd runs with --hidden)", labels)
	}
}

// The in-process path completion (no fd involved) offers hidden entries after
// `~/` without the leading dot being typed.
func TestPathCompletionOffersHiddenEntries(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".pi"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	provider := (&AutocompleteWiring{
		Session:     &autocompleteTestSession{},
		SessionInfo: coding.NewSessionManager(t.TempDir(), nil),
	}).CreateBaseAutocompleteProvider()
	suggestions := provider.GetSuggestions(context.Background(), []string{"~/"}, 0, 2, true)
	if suggestions == nil {
		t.Fatal("~/ completion returned nothing")
	}

	labels := map[string]bool{}
	for _, item := range suggestions.Items {
		labels[item.Label] = true
	}
	if !labels[".pi/"] {
		t.Errorf("~/ suggestions = %v, want the hidden directory", labels)
	}
}

// End to end through the real app wiring: the app's own autocomplete provider
// must answer an @-mention query. This is the assertion that would have caught
// the wiring gap, since it never touches the provider constructor directly.
func TestAppAutocompleteCompletesAtMentions(t *testing.T) {
	if ResolveAutocompleteFdPath() == "" {
		t.Skip("fd is not installed")
	}
	app, cleanup := newTestApp(t)
	defer cleanup()

	if err := os.WriteFile(filepath.Join(app.options.Cwd, "wired.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(app.options.Cwd, ".hidden-dir"), 0o755); err != nil {
		t.Fatal(err)
	}

	// The boot path installs the provider.
	app.autocomplete.SetupAutocompleteProvider()
	provider := app.autocomplete.Provider()
	if provider == nil {
		t.Fatal("the app installed no autocomplete provider")
	}

	suggestions := provider.GetSuggestions(context.Background(), []string{"@"}, 0, 1, true)
	if suggestions == nil || len(suggestions.Items) == 0 {
		t.Fatal("the app's provider returned nothing for @ — fd is still not wired through")
	}
	labels := map[string]bool{}
	for _, item := range suggestions.Items {
		labels[item.Label] = true
	}
	if !labels["wired.txt"] {
		t.Errorf("@ suggestions = %v, want the file in the session cwd", labels)
	}
	if !labels[".hidden-dir/"] {
		t.Errorf("@ suggestions = %v, want the hidden directory (fd runs with --hidden)", labels)
	}
}
