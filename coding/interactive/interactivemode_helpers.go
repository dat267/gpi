package interactive

import (
	"context"
	"os/exec"
	"strings"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of the remaining in-scope helpers of
// src/modes/interactive/interactive-mode.ts (renderProjectTrustWarningIfNeeded,
// promptForMissingSessionCwd, handleFatalRuntimeError, recordCrash,
// crashReportInstructions, suggestBugReport,
// maybeSaveImplicitProjectTrustAfterReload, createBaseAutocompleteProvider,
// setupAutocompleteProvider).
//
// Divergences: the process-level effects and the resource-loader/extension
// collaborators are injected seams (D131, D41).

// TrustCrashWiring carries the trust/crash helpers.
type TrustCrashWiring struct {
	Chat        *tui.Container
	UI          tui.TUI
	Settings    *coding.SettingsManager
	SessionInfo *coding.SessionManager
	AppName     string
	Display     *DisplayOptions
	AgentDir    string

	// SessionFile is the current session file ("" = in-memory).
	SessionFile func() string
	// ShowError reports an error.
	ShowError func(message string)
	// RequestRender requests a render.
	RequestRender func()
	// StopThemeWatcher stops the theme file watcher.
	StopThemeWatcher func()
	// Stop stops the mode (the fullscreen exit output).
	Stop func(fullscreenExitOutput string)
	// Exit terminates the process.
	Exit func(code int)
	// ShowExtensionConfirm asks a yes/no question; the answer arrives through the
	// callback on the UI loop.
	ShowExtensionConfirm func(ctx context.Context, title string, message string, onAnswer func(confirmed bool))

	// bugReportHintShown dedupes the /bug hint.
}

func (w *TrustCrashWiring) requestRender() {
	if w.RequestRender != nil {
		w.RequestRender()
	} else if w.UI != nil {
		w.UI.RequestRender(false)
	}
}

// RenderProjectTrustWarningIfNeeded warns when the project is untrusted.
func (w *TrustCrashWiring) RenderProjectTrustWarningIfNeeded() {
	if w.Settings == nil || w.SessionInfo == nil {
		return
	}
	if w.Settings.IsProjectTrusted() || !coding.HasTrustRequiringProjectResources(w.SessionInfo.GetCwd()) {
		return
	}
	theme := ActiveTheme()
	if w.Chat == nil {
		return
	}
	if len(w.Chat.Children) > 0 {
		w.Chat.AddChild(tui.NewSpacer(1))
	}
	// Upstream's wording also mentions installing project packages and running
	// project extensions; this port does neither (D41), so the warning names only
	// what is actually gated.
	w.Chat.AddChild(tui.NewText(theme.Fg("warning",
		"This project is not trusted. Project "+coding.ConfigDirName+
			" resources are ignored. Use /trust to save a trust decision, then restart pi."), 1, 0, nil))
}

// PromptForMissingSessionCwd asks for a fallback cwd.
func (w *TrustCrashWiring) PromptForMissingSessionCwd(ctx context.Context, issue coding.SessionCwdIssue, onCwd func(cwd string, ok bool)) {
	if w.ShowExtensionConfirm == nil {
		if onCwd != nil {
			onCwd("", false)
		}
		return
	}
	w.ShowExtensionConfirm(ctx, "Session cwd not found", coding.FormatMissingSessionCwdPrompt(issue), func(confirmed bool) {
		if onCwd == nil {
			return
		}
		if !confirmed {
			onCwd("", false)
			return
		}
		onCwd(issue.FallbackCwd, true)
	})
}

// HandleFatalRuntimeError reports a fatal error, records the crash and exits.
func (w *TrustCrashWiring) HandleFatalRuntimeError(ctx context.Context, prefix string, err error) {
	message := ""
	if err != nil {
		message = err.Error()
	}
	if w.ShowError != nil {
		w.ShowError(prefix + ": " + message)
	}
	if w.RecordCrash("fatal_error", err) && w.Chat != nil {
		theme := ActiveTheme()
		w.Chat.AddChild(tui.NewText(theme.Fg("muted", w.CrashReportInstructions()), w.Display.OutputPad, 0, nil))
	}
	if w.StopThemeWatcher != nil {
		w.StopThemeWatcher()
	}
	if w.Stop != nil {
		w.Stop("transcript")
	}
	if w.Exit != nil {
		w.Exit(1)
	}
}

// RecordCrash persists a crash, reporting whether anything was written.
func (w *TrustCrashWiring) RecordCrash(kind string, err error) bool {
	defer func() { _ = recover() }()
	sessionFile := ""
	if w.SessionFile != nil {
		sessionFile = w.SessionFile()
	}
	cwd := ""
	if w.SessionInfo != nil {
		cwd = w.SessionInfo.GetCwd()
	}
	return coding.RecordCrash(coding.CrashInput{
		Kind: kind, Error: err, SessionFile: sessionFile, Cwd: cwd,
	}, coding.GetCrashLogPath(w.AgentDir)) != nil
}

// CrashReportInstructions returns the post-crash resume hint.
func (w *TrustCrashWiring) CrashReportInstructions() string {
	if w.SessionFile != nil && w.SessionFile() != "" {
		return "Run `" + w.AppName + " -r` to resume the session."
	}
	return "Start " + w.AppName + " to begin a new session."
}

// MaybeSaveImplicitProjectTrustAfterReload saves the implicit trust decision.
func (w *TrustCrashWiring) MaybeSaveImplicitProjectTrustAfterReload(autoTrustOnReloadCwd string) bool {
	if w.SessionInfo == nil || w.Settings == nil {
		return false
	}
	cwd := w.SessionInfo.GetCwd()
	if autoTrustOnReloadCwd != cwd {
		return false
	}
	if !w.Settings.IsProjectTrusted() || !coding.HasTrustRequiringProjectResources(cwd) {
		return false
	}
	store := coding.NewProjectTrustStore(w.AgentDir)
	existing := store.GetEntry(cwd)
	if existing != nil {
		return false
	}
	decision := true
	if err := store.Set(cwd, &decision); err != nil {
		if w.ShowError != nil {
			w.ShowError("Could not save project trust after reload: " + err.Error())
		}
		return false
	}
	return true
}

// AutocompleteWiring assembles the mode's autocomplete provider.
type AutocompleteWiring struct {
	Session     AutocompleteSession
	Settings    *coding.SettingsManager
	SessionInfo *coding.SessionManager
	UI          tui.TUI
	FdPath      string

	// DefaultEditor is the built-in editor.
	DefaultEditor *CustomEditor
	// Editor is the active editor (may differ).
	Editor tui.Component

	// LoginProviders lists the login provider options ("" auth type).
	LoginProviders func() []AuthSelectorProvider
	// Skills lists the skill commands (the resource loader is out of scope).
	Skills func() []SkillCommand

	// skillCommands maps command names to skill paths.
	skillCommands map[string]string
	// wrappers wrap the base provider (extension autocomplete factories).
	wrappers []func(tui.AutocompleteProvider) tui.AutocompleteProvider
	// provider is the installed provider.
	provider tui.AutocompleteProvider
}

// SkillCommand is one skill slash command.
type SkillCommand struct {
	Name        string
	Description string
	FilePath    string
}

// AutocompleteSession is the session surface the autocomplete needs.
type AutocompleteSession interface {
	ScopedModels() []coding.ScopedModel
	ModelRuntime() *coding.ModelRuntime
	GetAvailableThinkingLevels() []ai.ThinkingLevel
	PromptTemplates() []coding.PromptTemplate
}

// SkillCommands returns the registered skill commands (test helper).
func (w *AutocompleteWiring) SkillCommands() map[string]string {
	copied := map[string]string{}
	for key, value := range w.skillCommands {
		copied[key] = value
	}
	return copied
}

// CreateBaseAutocompleteProvider builds the base provider.
func (w *AutocompleteWiring) CreateBaseAutocompleteProvider() tui.AutocompleteProvider {
	commands := make([]tui.CommandEntry, 0, len(coding.BuiltinSlashCommands)+8)
	for _, command := range coding.BuiltinSlashCommands {
		commands = append(commands, tui.CommandEntry{
			Name: command.Name, Description: command.Description, ArgumentHint: command.ArgumentHint,
		})
	}

	// /model argument completions.
	for index := range commands {
		if commands[index].Name != "model" {
			continue
		}
		commands[index].GetArgumentCompletions = func(prefix string) ([]tui.AutocompleteItem, bool) {
			models := w.availableModels()
			if len(models) == 0 {
				return nil, false
			}
			items := make([]modelCompletionItem, 0, len(models))
			for _, model := range models {
				items = append(items, modelCompletionItem{
					ID: model.ID, Provider: model.Provider, Name: model.Name, Label: model.Provider + "/" + model.ID,
				})
			}
			return CreateFuzzyAutocompleteItems(items, prefix,
				func(item modelCompletionItem) string { return GetModelSearchText(item.searchItem()) },
				func(item modelCompletionItem) tui.AutocompleteItem {
					return tui.AutocompleteItem{Value: item.Label, Label: item.ID, Description: item.Provider}
				}), true
		}
	}
	// /thinking argument completions.
	for index := range commands {
		if commands[index].Name != "thinking" {
			continue
		}
		commands[index].GetArgumentCompletions = func(prefix string) ([]tui.AutocompleteItem, bool) {
			levels := w.Session.GetAvailableThinkingLevels()
			return CreateFuzzyAutocompleteItems(levels, prefix,
				func(level ai.ThinkingLevel) string { return level },
				func(level ai.ThinkingLevel) tui.AutocompleteItem {
					return tui.AutocompleteItem{Value: level, Label: level}
				}), true
		}
	}
	// /login argument completions.
	for index := range commands {
		if commands[index].Name != "login" {
			continue
		}
		commands[index].GetArgumentCompletions = func(prefix string) ([]tui.AutocompleteItem, bool) {
			if w.LoginProviders == nil {
				return nil, false
			}
			providers := GetLoginProviderCompletionOptions(w.LoginProviders())
			return CreateFuzzyAutocompleteItems(providers, prefix, GetLoginProviderSearchText,
				func(provider LoginProviderCompletionOption) tui.AutocompleteItem {
					return tui.AutocompleteItem{
						Value: provider.ID, Label: provider.ID,
						Description: FormatLoginProviderCompletionDescription(provider),
					}
				}), true
		}
	}

	// Prompt templates.
	for _, template := range w.Session.PromptTemplates() {
		commands = append(commands, tui.CommandEntry{
			Name: template.Name, Description: template.Description, ArgumentHint: template.ArgumentHint,
		})
	}

	// Skill commands.
	w.skillCommands = map[string]string{}
	if w.Settings != nil && w.Settings.GetEnableSkillCommands() && w.Skills != nil {
		for _, skill := range w.Skills() {
			name := "skill:" + skill.Name
			w.skillCommands[name] = skill.FilePath
			commands = append(commands, tui.CommandEntry{Name: name, Description: skill.Description})
		}
	}

	basePath := ""
	if w.SessionInfo != nil {
		basePath = w.SessionInfo.GetCwd()
	}
	return tui.NewCombinedAutocompleteProvider(commands, basePath, w.FdPath)
}

func (w *AutocompleteWiring) availableModels() []*ai.Model {
	if len(w.Session.ScopedModels()) > 0 {
		models := make([]*ai.Model, 0, len(w.Session.ScopedModels()))
		for _, scoped := range w.Session.ScopedModels() {
			models = append(models, scoped.Model)
		}
		return models
	}
	return w.Session.ModelRuntime().GetAvailableSnapshot()
}

type modelCompletionItem struct {
	ID       string
	Provider string
	Name     string
	Label    string
}

func (m modelCompletionItem) searchItem() ModelSearchItem {
	return ModelSearchItem{ID: m.ID, Provider: m.Provider, Name: m.Name, HasName: m.Name != ""}
}

// SetAutocompleteWrappers installs the provider wrappers.
func (w *AutocompleteWiring) SetAutocompleteWrappers(wrappers []func(tui.AutocompleteProvider) tui.AutocompleteProvider) {
	w.wrappers = wrappers
}

// SetupAutocompleteProvider builds and installs the provider.
func (w *AutocompleteWiring) SetupAutocompleteProvider() {
	provider := w.CreateBaseAutocompleteProvider()
	triggerCharacters := []string{}
	for _, wrapper := range w.wrappers {
		provider = wrapper(provider)
		if extended, ok := provider.(interface{ TriggerCharacters() []string }); ok {
			triggerCharacters = append(triggerCharacters, extended.TriggerCharacters()...)
		}
	}
	if len(triggerCharacters) > 0 {
		unique := make([]string, 0, len(triggerCharacters))
		seen := map[string]bool{}
		for _, character := range triggerCharacters {
			if !seen[character] {
				seen[character] = true
				unique = append(unique, character)
			}
		}
		if configurable, ok := provider.(interface{ SetTriggerCharacters([]string) }); ok {
			configurable.SetTriggerCharacters(unique)
		}
	}

	w.provider = provider
	if w.DefaultEditor != nil {
		w.DefaultEditor.SetAutocompleteProvider(provider)
	}
	if w.Editor != nil && w.Editor != tui.Component(w.DefaultEditor) {
		if editor, ok := w.Editor.(interface {
			SetAutocompleteProvider(tui.AutocompleteProvider)
		}); ok {
			editor.SetAutocompleteProvider(provider)
		}
	}
}

// Provider returns the installed provider (test helper).
func (w *AutocompleteWiring) Provider() tui.AutocompleteProvider { return w.provider }

// PrefixAutocompleteDescription prefixes a description with its source.
func PrefixAutocompleteDescription(description string, sourceInfo *coding.SourceInfo) string {
	if sourceInfo == nil {
		return description
	}
	source := ""
	switch sourceInfo.Scope {
	case coding.SourceScopeUser:
		source = "user"
	case coding.SourceScopeProject:
		source = "project"
	default:
		source = "temporary"
	}
	if description == "" {
		return source
	}
	return "[" + source + "] " + description
}

// trimSlash strips a leading slash.
func trimSlash(value string) string { return strings.TrimPrefix(value, "/") }

// newTrustCrashWiring assembles the TrustCrashWiring (port of the corresponding InteractiveMode wiring).
func newTrustCrashWiring(app *App) *TrustCrashWiring {
	return &TrustCrashWiring{
		// The trust prompt's confirm, same dialog again.
		ShowExtensionConfirm: func(ctx context.Context, title string, message string, onAnswer func(confirmed bool)) {
			app.askConfirm(title, message, onAnswer)
		},
		Chat:             app.chat,
		UI:               app.ui,
		Settings:         app.settings,
		SessionInfo:      app.sessionMgr,
		AppName:          app.options.AppName,
		Display:          app.display,
		AgentDir:         app.options.AgentDir,
		SessionFile:      func() string { return app.session.SessionFile() },
		ShowError:        func(message string) { app.showError(message) },
		RequestRender:    func() { app.ui.RequestRender(false) },
		StopThemeWatcher: func() { StopThemeWatcher() },
		// Upstream's fatal path calls stop(), which reads the
		// fullscreenExitOutput setting.
		Stop: func(string) { app.StopMode(app.options.Settings.GetFullscreenExitOutput()) },
		Exit: app.options.Exit,
	}
}

// ResolveAutocompleteFdPath locates the `fd` binary the fuzzy file completion
// uses. An empty result disables the fd-backed completion paths rather than
// failing: the provider guards on it, so path completion still works through
// the in-process directory listing. Hidden entries are not filtered by either
// path — fd runs with --hidden, and the in-process listing applies only a
// prefix filter — so `.pi` is offered after `~/` without typing the dot.
func ResolveAutocompleteFdPath() string {
	// Upstream looks for "fd" only. Debian and Ubuntu ship the same binary as
	// "fdfind", so the port accepts either rather than leaving @-file completion
	// silently unavailable on those systems.
	for _, name := range []string{"fd", "fdfind"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

// newAutocompleteWiring assembles the AutocompleteWiring (port of the corresponding InteractiveMode wiring).
func newAutocompleteWiring(app *App) *AutocompleteWiring {
	return &AutocompleteWiring{
		Session:        app.session,
		Settings:       app.settings,
		SessionInfo:    app.sessionMgr,
		UI:             app.ui,
		DefaultEditor:  app.defaultEditor,
		Editor:         app.defaultEditor,
		FdPath:         ResolveAutocompleteFdPath(),
		LoginProviders: func() []AuthSelectorProvider { return app.auth.GetLoginProviderOptions("") },
		Skills:         app.skillCommands}
}
