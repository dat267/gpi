package interactive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// TestInteractiveModeHelpers covers the helper layer of interactive-mode.ts.
func TestInteractiveModeHelpers(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	if got := QuoteIfNeeded("simple-1.2_x"); got != "simple-1.2_x" {
		t.Fatalf("quote simple = %q", got)
	}
	if got := QuoteIfNeeded("/tmp/a b"); got != "'/tmp/a b'" {
		t.Fatalf("quote space = %q", got)
	}
	if got := QuoteIfNeeded("it's"); got != `'it'\''s'` {
		t.Fatalf("quote apostrophe = %q", got)
	}
	if got := QuoteIfNeeded(""); got != "''" {
		t.Fatalf("quote empty = %q", got)
	}

	if !IsAnthropicSubscriptionAuthKey("sk-ant-oat01-xyz") {
		t.Fatal("subscription key not detected")
	}
	if IsAnthropicSubscriptionAuthKey("sk-ant-api03") || IsAnthropicSubscriptionAuthKey("") {
		t.Fatal("api key misdetected")
	}
	if !IsUnknownModel(&ai.Model{Provider: "unknown", ID: "unknown", API: "unknown"}) {
		t.Fatal("unknown model not detected")
	}
	if IsUnknownModel(&ai.Model{Provider: "unknown", ID: "unknown"}) || IsUnknownModel(nil) {
		t.Fatal("unknown model misdetected")
	}
	if !IsDeadTerminalError("EIO") || !IsDeadTerminalError("EPIPE") || !IsDeadTerminalError("ENOTCONN") {
		t.Fatal("dead terminal codes")
	}
	if IsDeadTerminalError("ECONNREFUSED") || IsDeadTerminalError("") {
		t.Fatal("unexpected dead terminal code")
	}

	if !HasDefaultModelProvider("anthropic") {
		t.Fatal("anthropic should have a default model")
	}
	if HasDefaultModelProvider("nope") {
		t.Fatal("unexpected default model provider")
	}
	if got := LlamaCppPostLoginGuidance("Logged in", 0); got != "Logged in. No llama.cpp models are loaded. Use /llama to load a model, then /model to select it." {
		t.Fatalf("guidance empty = %q", got)
	}
	if got := LlamaCppPostLoginGuidance("Logged in", 3); got != "Logged in. Use /model to select a loaded llama.cpp model, or /llama to manage models." {
		t.Fatalf("guidance loaded = %q", got)
	}

	options := GetLoginProviderCompletionOptions([]AuthSelectorProvider{
		{ID: "openai", Name: "OpenAI", AuthType: "oauth"},
		{ID: "openai", Name: "OpenAI", AuthType: "api_key"},
		{ID: "anthropic", Name: "Anthropic", AuthType: "api_key"},
		{ID: "openai", Name: "OpenAI", AuthType: "oauth"},
	})
	if len(options) != 2 {
		t.Fatalf("options = %+v", options)
	}
	// Sorted by name; auth types ordered oauth before api_key.
	if options[0].ID != "anthropic" || options[1].ID != "openai" {
		t.Fatalf("order = %+v", options)
	}
	if len(options[1].AuthTypes) != 2 || options[1].AuthTypes[0] != "oauth" || options[1].AuthTypes[1] != "api_key" {
		t.Fatalf("auth types = %+v", options[1].AuthTypes)
	}
	if got := GetLoginProviderSearchText(options[1]); got != "openai OpenAI oauth subscription api_key API key" {
		t.Fatalf("search text = %q", got)
	}
	if got := FormatLoginProviderCompletionDescription(options[1]); got != "OpenAI · subscription/API key" {
		t.Fatalf("description = %q", got)
	}
	same := LoginProviderCompletionOption{ID: "openai", Name: "openai", AuthTypes: []string{"oauth"}}
	if got := FormatLoginProviderCompletionDescription(same); got != "subscription" {
		t.Fatalf("same-name description = %q", got)
	}

	items := []string{"alpha", "beta", "gamma"}
	mapped := CreateFuzzyAutocompleteItems(items, "ga", func(item string) string { return item },
		func(item string) tui.AutocompleteItem { return tui.AutocompleteItem{Value: item} })
	if len(mapped) != 1 || mapped[0].Value != "gamma" {
		t.Fatalf("autocomplete items = %+v", mapped)
	}
	if got := CreateFuzzyAutocompleteItems(items, "zz", func(item string) string { return item },
		func(item string) tui.AutocompleteItem { return tui.AutocompleteItem{Value: item} }); got != nil {
		t.Fatalf("no-match items = %+v", got)
	}
}

// TestExpandableTextAndGuards covers the small components.
func TestExpandableTextAndGuards(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	text := NewExpandableText(func() string { return "collapsed" }, func() string { return "expanded" }, false, 0, 0)
	firstLine := func() string {
		lines := text.Render(20)
		if len(lines) == 0 {
			return ""
		}
		return strings.TrimSpace(lines[0])
	}
	if got := firstLine(); got != "collapsed" {
		t.Fatalf("collapsed render = %q", got)
	}
	text.SetExpanded(true)
	if got := firstLine(); got != "expanded" {
		t.Fatalf("expanded render = %q", got)
	}
	text.SetExpanded(false)
	if got := firstLine(); got != "collapsed" {
		t.Fatalf("re-collapsed render = %q", got)
	}
	if _, ok := IsExpandable(text); !ok {
		t.Fatal("expandable not detected")
	}
	if _, ok := IsExpandable("nope"); ok {
		t.Fatal("unexpected expandable")
	}

	editor := NewCustomEditor(editorTestHost{}, tui.EditorTheme{}, NewAppKeybindingsManager(nil, ""), CustomEditorOptions{
		EmbedWorkingStatus: true,
	})
	statusEditor, ok := IsWorkingStatusEditor(editor)
	if !ok {
		t.Fatal("working status editor not detected")
	}
	if statusEditor != WorkingStatusEditor(editor) {
		t.Fatal("wrong editor returned")
	}
	plain := NewCustomEditor(editorTestHost{}, tui.EditorTheme{}, NewAppKeybindingsManager(nil, ""), CustomEditorOptions{})
	if _, ok := IsWorkingStatusEditor(plain); ok {
		t.Fatal("unexpected working status editor")
	}
}

// TestFormatResumeCommand covers the resume command assembly.
func TestFormatResumeCommand(t *testing.T) {
	dir := t.TempDir()
	sessionDir := filepath.Join(dir, "sessions")
	manager := coding.NewSessionManager("/tmp/proj", &coding.SessionManagerOptions{SessionDir: sessionDir})
	if !manager.IsPersisted() {
		t.Fatal("session should be persisted")
	}
	if command := FormatResumeCommand(manager, "", false); command != "" {
		t.Fatalf("non-tty command = %q", command)
	}
	// The session file is written lazily on the first append; create it so the
	// existsSync branch is exercised.
	if err := os.WriteFile(manager.GetSessionFile(), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write session file: %v", err)
	}
	command := FormatResumeCommand(manager, "pier", true)
	if command == "" {
		t.Fatal("missing command")
	}
	want := "pier" + " --session-dir " + QuoteIfNeeded(manager.GetSessionDir()) + " --session " + manager.GetSessionID()
	if command != want {
		t.Fatalf("command = %q, want %q", command, want)
	}

	// Non-persisted managers produce no command.
	notPersisted := coding.NewSessionManager("/tmp/proj", &coding.SessionManagerOptions{Persist: boolPtr(false)})
	if command := FormatResumeCommand(notPersisted, "", true); command != "" {
		t.Fatalf("non-persisted command = %q", command)
	}
}

func boolPtr(value bool) *bool { return &value }
