package interactive

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/dat267/gpi/agent"
	"github.com/dat267/gpi/ai"
	"github.com/dat267/gpi/coding"
	"github.com/dat267/gpi/tui"
)

// Port of the startup/orchestration helpers of
// src/modes/interactive/interactive-mode.ts (getUserInput,
// rebuildChatFromMessages, renderCurrentSessionState,
// checkTmuxKeyboardSetup, getChangelogForDisplay, reportInstallTelemetry,
// getMarkdownThemeWithSettings, updateAvailableProviderCount,
// maybeWarnAboutAnthropicSubscriptionAuth).
//
// Divergences: the terminal/process collaborators are injected (D123); the
// extension/resource seams stay out of scope (D41).

// StartupSession is the session surface the startup helpers need.
type StartupSession interface {
	Messages() []ai.Message
	ScopedModels() []coding.ScopedModel
	ModelRuntime() *coding.ModelRuntime
	Model() *ai.Model
	GetToolDefinition(name string) *agent.AgentTool
}

// StartupWiring wires the startup/orchestration helpers.
type StartupWiring struct {
	UI       tui.TUI
	Session  StartupSession
	Settings *coding.SettingsManager
	Terminal tui.Terminal

	// Chat and PendingMessages are the transcript containers.
	Chat            *tui.Container
	PendingMessages *tui.Container
	LoadedResources *tui.Container

	// Transcript renders the session entries.
	Transcript *TranscriptRenderer
	// FooterData receives the provider count.
	FooterData *coding.FooterDataProvider
	// SessionInfo is the session manager.
	SessionInfo *coding.SessionManager

	// RenderInitialMessages re-renders the initial transcript.
	RenderInitialMessages func()
	// ShowWarning/ShowError/ShowStatus report messages.
	ShowWarning func(message string)
	ShowError   func(message string)
	ShowStatus  func(message string)
	// Version is the running version.
	Version string
	// ReportInstall sends the install telemetry ping.
	ReportInstall func(version string) error
	// TmuxShow queries a tmux option (test seam).
	TmuxShow func(option string) (string, bool)
	// RequestRender requests a render.
	RequestRender func()

	// mu guards the pending input queue and callback (the TUI can submit from
	// another goroutine; D123).
	mu sync.Mutex
	// pendingUserInputs queues inputs submitted before the main loop starts.
	pendingUserInputs []string
	// onInputCallback receives the next submitted input.
	onInputCallback func(string)
	// anthropicSubscriptionWarningShown dedupes the warning.
	anthropicSubscriptionWarningShown bool
	// MainScreenRenderState is the captured main-screen state.
	mainScreenRenderState *tui.MainScreenRenderState
}

func (w *StartupWiring) showWarning(message string) {
	if w.ShowWarning != nil {
		w.ShowWarning(message)
	}
}

func (w *StartupWiring) showStatus(message string) {
	if w.ShowStatus != nil {
		w.ShowStatus(message)
	}
}

// QueueUserInput records a submission for the main loop.
func (w *StartupWiring) QueueUserInput(text string) {
	w.mu.Lock()
	callback := w.onInputCallback
	if callback != nil {
		w.onInputCallback = nil
	}
	if callback == nil {
		w.pendingUserInputs = append(w.pendingUserInputs, text)
	}
	w.mu.Unlock()
	if callback != nil {
		callback(text)
	}
}

// GetUserInput waits for the next user input.
func (w *StartupWiring) GetUserInput(ctx context.Context) (string, bool) {
	w.mu.Lock()
	if len(w.pendingUserInputs) > 0 {
		value := w.pendingUserInputs[0]
		w.pendingUserInputs = w.pendingUserInputs[1:]
		w.mu.Unlock()
		return value, true
	}
	results := make(chan string, 1)
	w.onInputCallback = func(text string) { results <- text }
	w.mu.Unlock()

	select {
	case text := <-results:
		return text, true
	case <-ctx.Done():
		w.mu.Lock()
		w.onInputCallback = nil
		w.mu.Unlock()
		return "", false
	}
}

// HasInputWaiter reports whether a GetUserInput call is waiting (test helper).
func (w *StartupWiring) HasInputWaiter() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.onInputCallback != nil
}

// RebuildChatFromMessages re-renders the transcript from the session context.
func (w *StartupWiring) RebuildChatFromMessages() {
	if w.Chat == nil || w.SessionInfo == nil || w.Transcript == nil {
		return
	}
	w.Chat.Clear()
	w.Transcript.RenderSessionEntries(w.SessionInfo.BuildContextEntriesForLeaf(), false, false)
}

// RenderCurrentSessionState resets the chat state and re-renders.
func (w *StartupWiring) RenderCurrentSessionState() {
	if w.LoadedResources != nil {
		w.LoadedResources.Clear()
	}
	if w.Chat != nil {
		w.Chat.Clear()
	}
	if w.PendingMessages != nil {
		w.PendingMessages.Clear()
	}
	if w.Transcript != nil {
		w.Transcript.StreamingComponent = nil
		w.Transcript.pendingTools = map[string]*ToolExecutionComponent{}
	}
	if w.RenderInitialMessages != nil {
		w.RenderInitialMessages()
	}
}

// CheckTmuxKeyboardSetup warns about suboptimal tmux keyboard settings.
func (w *StartupWiring) CheckTmuxKeyboardSetup(hasTmux bool) string {
	if !hasTmux {
		return ""
	}
	show := w.TmuxShow
	if show == nil {
		show = runTmuxShow
	}
	extendedKeys, ok := show("extended-keys")
	if !ok {
		return ""
	}
	if extendedKeys != "on" && extendedKeys != "always" {
		return "tmux extended-keys is off. Modified Enter keys may not work. Add `set -g extended-keys on` to ~/.tmux.conf and restart tmux."
	}
	extendedKeysFormat, _ := show("extended-keys-format")
	if extendedKeysFormat == "xterm" {
		return "tmux extended-keys-format is xterm. Pi works best with csi-u. Add `set -g extended-keys-format csi-u` to ~/.tmux.conf and restart tmux."
	}
	return ""
}

// runTmuxShow queries one tmux option with a 2s timeout.
func runTmuxShow(option string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "tmux", "show", "-gv", option)
	output, err := command.Output()
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(output)), true
}

// GetChangelogForDisplay returns the new changelog markdown, recording the
// version and reporting telemetry as a side effect.
func (w *StartupWiring) GetChangelogForDisplay() string {
	if w.Session != nil && len(w.Session.Messages()) > 0 {
		return ""
	}
	lastVersion := ""
	if w.Settings != nil {
		if value := w.Settings.GetLastChangelogVersion(); value != nil {
			lastVersion = *value
		}
	}
	entries := coding.ParseChangelog(coding.GetChangelogPath())
	if lastVersion == "" {
		// Fresh install: record the version, send telemetry, show nothing.
		if w.Settings != nil {
			w.Settings.SetLastChangelogVersion(w.Version)
		}
		w.ReportInstallTelemetry(w.Version)
		return ""
	}
	newEntries := coding.GetNewChangelogEntries(entries, lastVersion)
	if len(newEntries) == 0 {
		return ""
	}
	if w.Settings != nil {
		w.Settings.SetLastChangelogVersion(w.Version)
	}
	w.ReportInstallTelemetry(w.Version)
	parts := make([]string, 0, len(newEntries))
	for _, entry := range newEntries {
		parts = append(parts, coding.NormalizeChangelogLinks(entry.Content, changelogEntryVersion(entry)))
	}
	return strings.Join(parts, "\n\n")
}

func changelogEntryVersion(entry coding.ChangelogEntry) string {
	return itoa(entry.Major) + "." + itoa(entry.Minor) + "." + itoa(entry.Patch)
}

// ReportInstallTelemetry sends the install ping when enabled.
func (w *StartupWiring) ReportInstallTelemetry(version string) {
	report := w.ReportInstall
	settings := w.Settings
	if report == nil || settings == nil {
		return
	}
	if !coding.IsInstallTelemetryEnabled(settings) {
		return
	}
	// Capture the collaborator before spawning: the goroutine must not read
	// mutable wiring fields (D123).
	go func() { _ = report(version) }()
}

// GetMarkdownThemeWithSettings applies the code-block indent setting.
func (w *StartupWiring) GetMarkdownThemeWithSettings(base tui.MarkdownTheme) tui.MarkdownTheme {
	if w.Settings != nil {
		if indent := w.Settings.GetCodeBlockIndent(); indent != "" {
			base.CodeBlockIndent = indent
		}
	}
	return base
}

// UpdateAvailableProviderCount refreshes the footer provider count.
func (w *StartupWiring) UpdateAvailableProviderCount() {
	if w.FooterData == nil {
		return
	}
	var models []*ai.Model
	if w.Session != nil && len(w.Session.ScopedModels()) > 0 {
		for _, scoped := range w.Session.ScopedModels() {
			models = append(models, scoped.Model)
		}
	} else if w.Session != nil {
		models = w.Session.ModelRuntime().GetAvailableSnapshot()
	}
	providers := map[string]bool{}
	for _, model := range models {
		providers[model.Provider] = true
	}
	w.FooterData.SetAvailableProviderCount(len(providers))
}

// MaybeWarnAboutAnthropicSubscriptionAuth warns once about Anthropic
// subscription auth.
func (w *StartupWiring) MaybeWarnAboutAnthropicSubscriptionAuth(ctx context.Context, model *ai.Model) {
	if w.Settings != nil {
		warnings := w.Settings.GetWarnings()
		if warnings.AnthropicExtraUsage != nil && !*warnings.AnthropicExtraUsage {
			return
		}
	}
	if w.anthropicSubscriptionWarningShown {
		return
	}
	if model == nil {
		if w.Session != nil {
			model = w.Session.Model()
		}
	}
	if model == nil || model.Provider != "anthropic" {
		return
	}
	runtime := w.Session.ModelRuntime()
	check, err := runtime.CheckAuth("anthropic", ctx)
	if err == nil && check != nil && check.Type == "oauth" {
		w.anthropicSubscriptionWarningShown = true
		w.showWarning(AnthropicSubscriptionAuthWarning)
		return
	}
	auth, err := runtime.GetAuth(model.Provider, nil)
	if err != nil || auth == nil {
		return
	}
	if !IsAnthropicSubscriptionAuthKey(auth.Auth.APIKey) {
		return
	}
	w.anthropicSubscriptionWarningShown = true
	w.showWarning(AnthropicSubscriptionAuthWarning)
}
