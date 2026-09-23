package interactive

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

func newHelpersTestWiring(t *testing.T) (*TrustCrashWiring, *coding.SettingsManager, *coding.SessionManager) {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	settings := coding.NewInMemorySettingsManager(nil, coding.SettingsManagerCreateOptions{})
	manager := coding.NewSessionManager("/tmp/proj", &coding.SessionManagerOptions{Persist: boolPtr(false)})
	wiring := &TrustCrashWiring{
		Chat:        &tui.Container{},
		Settings:    settings,
		SessionInfo: manager,
		AppName:     "pi",
		AgentDir:    t.TempDir(),
		Display:     &DisplayOptions{},
	}
	return wiring, settings, manager
}

// TestTrustWarning covers the project-trust warning.
func TestTrustWarning(t *testing.T) {
	wiring, settings, manager := newHelpersTestWiring(t)
	// A trusted project renders nothing.
	wiring.RenderProjectTrustWarningIfNeeded()
	if len(wiring.Chat.Children) != 0 {
		t.Fatal("warning rendered for a trusted project")
	}

	// An untrusted project without trust-requiring resources renders nothing.
	untrusted := coding.NewInMemorySettingsManager(nil, coding.SettingsManagerCreateOptions{
		ProjectTrusted: boolPtr(false),
	})
	wiring.Settings = untrusted
	wiring.RenderProjectTrustWarningIfNeeded()
	if len(wiring.Chat.Children) != 0 {
		t.Fatal("warning rendered without trust-requiring resources")
	}
	_ = settings
	_ = manager
}

// TestCrashHelpers covers the crash recording and instructions.
func TestCrashHelpers(t *testing.T) {
	wiring, _, _ := newHelpersTestWiring(t)
	wiring.SessionFile = func() string { return "" }

	if instructions := wiring.CrashReportInstructions(); !strings.Contains(instructions, "Start") {
		t.Fatalf("instructions = %q", instructions)
	}
	wiring.SessionFile = func() string { return "/tmp/sess.jsonl" }
	if instructions := wiring.CrashReportInstructions(); !strings.Contains(instructions, "pi -r` to resume") {
		t.Fatalf("instructions = %q", instructions)
	}

	// A recorded crash writes a record.
	if !wiring.RecordCrash("fatal_error", errors.New("boom")) {
		t.Fatal("crash not recorded")
	}
	if !wiring.RecordCrash("uncaught_exception", errors.New("boom2")) {
		t.Fatal("crash not recorded")
	}

}

// TestFatalRuntimeError covers the fatal error path.
func TestFatalRuntimeError(t *testing.T) {
	wiring, _, _ := newHelpersTestWiring(t)
	errorsShown := []string{}
	wiring.ShowError = func(message string) { errorsShown = append(errorsShown, message) }
	stoppedWatcher := 0
	wiring.StopThemeWatcher = func() { stoppedWatcher++ }
	stopped := ""
	wiring.Stop = func(output string) { stopped = output }
	exitCode := -1
	wiring.Exit = func(code int) { exitCode = code }

	wiring.HandleFatalRuntimeError(context.Background(), "Failed to resume session", errors.New("boom"))
	if len(errorsShown) != 1 || errorsShown[0] != "Failed to resume session: boom" {
		t.Fatalf("errors = %v", errorsShown)
	}
	if stoppedWatcher != 1 || stopped != "transcript" || exitCode != 1 {
		t.Fatalf("watcher = %d, stop = %q, exit = %d", stoppedWatcher, stopped, exitCode)
	}
	lines := coding.StripAnsi(strings.Join(wiring.Chat.Render(100), "\n"))
	if !strings.Contains(lines, "Start pi to begin a new session.") {
		t.Fatalf("chat = %q", lines)
	}
}

// TestPromptForMissingCwd covers the missing-cwd prompt.
func TestPromptForMissingCwd(t *testing.T) {
	wiring, _, _ := newHelpersTestWiring(t)
	issue := coding.SessionCwdIssue{SessionCwd: "/gone", FallbackCwd: "/tmp/fallback"}

	// Without the dialog seam the prompt cancels.
	var cwd string
	var ok bool
	wiring.PromptForMissingSessionCwd(context.Background(), issue, func(selected string, selectedOK bool) {
		cwd, ok = selected, selectedOK
	})
	if ok {
		t.Fatal("prompt should cancel without the seam")
	}
	wiring.ShowExtensionConfirm = func(_ context.Context, _, _ string, onAnswer func(bool)) { onAnswer(true) }
	wiring.PromptForMissingSessionCwd(context.Background(), issue, func(selected string, selectedOK bool) {
		cwd, ok = selected, selectedOK
	})
	if !ok || cwd != "/tmp/fallback" {
		t.Fatalf("cwd = %q ok = %v", cwd, ok)
	}
	// A declined confirm cancels.
	wiring.ShowExtensionConfirm = func(_ context.Context, _, _ string, onAnswer func(bool)) { onAnswer(false) }
	wiring.PromptForMissingSessionCwd(context.Background(), issue, func(selected string, selectedOK bool) {
		cwd, ok = selected, selectedOK
	})
	if ok {
		t.Fatal("declined prompt returned a cwd")
	}
}

// TestImplicitProjectTrust covers the implicit trust save.
func TestImplicitProjectTrust(t *testing.T) {
	wiring, _, manager := newHelpersTestWiring(t)
	// A different cwd does nothing.
	if wiring.MaybeSaveImplicitProjectTrustAfterReload("/other") {
		t.Fatal("saved for a different cwd")
	}
	// The current cwd is trusted and has no trust-requiring resources.
	if wiring.MaybeSaveImplicitProjectTrustAfterReload(manager.GetCwd()) {
		t.Fatal("saved without trust-requiring resources")
	}
}

// autocompleteTestSession implements AutocompleteSession.
type autocompleteTestSession struct {
	scoped    []coding.ScopedModel
	runtime   *coding.ModelRuntime
	levels    []ai.ThinkingLevel
	templates []coding.PromptTemplate
}

func (s *autocompleteTestSession) ScopedModels() []coding.ScopedModel { return s.scoped }
func (s *autocompleteTestSession) ModelRuntime() *coding.ModelRuntime { return s.runtime }
func (s *autocompleteTestSession) GetAvailableThinkingLevels() []ai.ThinkingLevel {
	return s.levels
}
func (s *autocompleteTestSession) PromptTemplates() []coding.PromptTemplate { return s.templates }

// TestAutocompleteAssembly covers the base provider assembly.
func TestAutocompleteAssembly(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	appKeybindings := NewAppKeybindingsManager(nil, "")
	previous := tui.GetKeybindings()
	tui.SetKeybindings(appKeybindings.KeybindingsManager)
	t.Cleanup(func() { tui.SetKeybindings(previous) })

	runtime, err := coding.CreateModelRuntime(coding.CreateModelRuntimeOptions{
		Credentials: ai.NewInMemoryCredentialStore(), DisableModelsJSON: true,
	})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	settings := coding.NewInMemorySettingsManager(nil, coding.SettingsManagerCreateOptions{})
	manager := coding.NewSessionManager("/tmp/proj", &coding.SessionManagerOptions{Persist: boolPtr(false)})
	session := &autocompleteTestSession{
		runtime: runtime,
		levels:  []ai.ThinkingLevel{"off", "medium", "high"},
		templates: []coding.PromptTemplate{
			{Name: "review", Description: "Review the diff", ArgumentHint: "<path>"},
		},
	}
	editor := NewCustomEditor(editorTestHost{}, tui.EditorTheme{}, appKeybindings, CustomEditorOptions{})
	wiring := &AutocompleteWiring{
		Session:       session,
		Settings:      settings,
		SessionInfo:   manager,
		DefaultEditor: editor,
		Editor:        editor,
		LoginProviders: func() []AuthSelectorProvider {
			return []AuthSelectorProvider{{ID: "anthropic", Name: "Anthropic", AuthType: "api_key"}}
		},
		Skills: func() []SkillCommand {
			return []SkillCommand{{Name: "debug", Description: "Debug things", FilePath: "/skills/debug"}}
		},
	}
	settings.SetEnableSkillCommands(true)

	provider := wiring.CreateBaseAutocompleteProvider()
	if provider == nil {
		t.Fatal("no provider")
	}
	combined, ok := provider.(*tui.CombinedAutocompleteProvider)
	if !ok {
		t.Fatalf("provider = %T", provider)
	}
	// The provider suggests built-in commands, templates and skills.
	suggestions := combined.GetSuggestions(context.Background(), []string{"/"}, 0, 1, false)
	names := map[string]bool{}
	if suggestions != nil {
		for _, item := range suggestions.Items {
			names[item.Value] = true
		}
	}
	for _, expected := range []string{"model", "thinking", "login", "review", "skill:debug"} {
		if !names[expected] {
			t.Errorf("missing command %q in %v", expected, names)
		}
	}
	// The skill command map is recorded.
	commands := wiring.SkillCommands()
	if commands["skill:debug"] != "/skills/debug" {
		t.Fatalf("skill commands = %v", commands)
	}

	// Setup installs the provider on both editors.
	wiring.SetupAutocompleteProvider()
	if wiring.Provider() == nil {
		t.Fatal("provider not installed")
	}
	if wiring.Provider() == nil {
		t.Fatal("editor provider not set")
	}

	// Disabling skill commands removes them.
	settings.SetEnableSkillCommands(false)
	disabled := wiring.CreateBaseAutocompleteProvider().(*tui.CombinedAutocompleteProvider)
	disabledSuggestions := disabled.GetSuggestions(context.Background(), []string{"/skill"}, 0, 6, false)
	if disabledSuggestions != nil {
		for _, item := range disabledSuggestions.Items {
			if strings.HasPrefix(item.Value, "skill:") {
				t.Fatalf("skill command present while disabled: %v", item)
			}
		}
	}
}

// TestPrefixAutocompleteDescription covers the source prefix.
func TestPrefixAutocompleteDescription(t *testing.T) {
	if got := PrefixAutocompleteDescription("desc", nil); got != "desc" {
		t.Fatalf("nil source = %q", got)
	}
	user := coding.SourceInfo{Scope: coding.SourceScopeUser}
	if got := PrefixAutocompleteDescription("desc", &user); got != "[user] desc" {
		t.Fatalf("user = %q", got)
	}
	project := coding.SourceInfo{Scope: coding.SourceScopeProject}
	if got := PrefixAutocompleteDescription("", &project); got != "project" {
		t.Fatalf("empty desc = %q", got)
	}
	if got := trimSlash("/model"); got != "model" {
		t.Fatalf("trimSlash = %q", got)
	}
}
