package interactive

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dat267/pier/agent"
	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
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

	// inputs receives submitted user text. The run loop is the single
	// consumer; the TUI submit handler is the producer (the TUI delivers
	// submissions from its own goroutine). Buffered with room for a turn's
	// worth of queued submissions (capacity >= senders).
	inputs chan string
	// inputsClosed unblocks a producer parked on a full inputs channel once
	// the app shuts down.
	inputsClosed chan struct{}
	// inputsClosedOnce makes Close idempotent.
	inputsClosedOnce sync.Once
	// ctx is the run context; submission sends also unblock on its cancellation
	// (stage 4 gap).
	ctx atomic.Pointer[context.Context]
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

// InitInputs creates the submission channel. The app calls it once at
// composition; the channel is loop-consumed (no lock on either side).
func (w *StartupWiring) InitInputs() {
	if w.inputs == nil {
		w.inputs = make(chan string, inputQueueCapacity)
	}
	if w.inputsClosed == nil {
		w.inputsClosed = make(chan struct{})
	}
}

// Inputs exposes the submission channel to the run loop.
func (w *StartupWiring) Inputs() <-chan string { return w.inputs }

// SetContext installs the run context for ctx-aware producer sends.
func (w *StartupWiring) SetContext(ctx context.Context) {
	if w == nil {
		return
	}
	w.ctx.Store(&ctx)
}

// done is the channel a producer send unblocks on (run cancellation or Close).
func (w *StartupWiring) done() <-chan struct{} {
	if ctx := w.ctx.Load(); ctx != nil {
		return (*ctx).Done()
	}
	return w.inputsClosed
}

// QueueUserInput delivers a submission to the run loop. The send is buffered;
// it only blocks when the queue is full (a turn's worth of pending
// submissions) and unblocks at shutdown or context cancellation.
func (w *StartupWiring) QueueUserInput(text string) {
	if w.inputs == nil {
		w.InitInputs()
	}
	select {
	case w.inputs <- text:
	case <-w.inputsClosed:
	case <-w.done():
	}
}

// GetUserInput waits for the next submission (test seam).
func (w *StartupWiring) GetUserInput(ctx context.Context) (string, bool) {
	if w.inputs == nil {
		w.InitInputs()
	}
	select {
	case text := <-w.inputs:
		return text, true
	case <-w.inputsClosed:
		return "", false
	case <-ctx.Done():
		// Prefer a delivered input over cancellation (both may be ready).
		select {
		case text := <-w.inputs:
			return text, true
		default:
		}
		return "", false
	}
}

// CloseInputs releases producers parked on the input queue.
func (w *StartupWiring) CloseInputs() {
	if w.inputsClosed == nil {
		return
	}
	w.inputsClosedOnce.Do(func() { close(w.inputsClosed) })
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

// newStartupWiring assembles the StartupWiring (port of the corresponding InteractiveMode wiring).
func newStartupWiring(app *App) *StartupWiring {
	return &StartupWiring{
		UI:              app.UI,
		Session:         app.Session,
		Settings:        app.Settings,
		Terminal:        app.UI.GetTerminal(),
		Chat:            app.Chat,
		PendingMessages: app.PendingMessages,
		LoadedResources: app.LoadedResourcesContainer,
		Transcript:      app.Transcript,
		FooterData:      app.FooterData,
		SessionInfo:     app.SessionMgr,
		Version:         app.options.Version,
		ShowWarning:     func(message string) { app.showWarning(message) },
		ShowError:       func(message string) { app.showError(message) },
		ShowStatus:      func(message string) { app.Transcript.ShowStatus(message) },
		RequestRender:   func() { app.UI.RequestRender(false) },
	}
}
