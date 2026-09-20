package interactive

import (
	"context"
	"strings"

	"github.com/dat267/gpi/coding"
	"github.com/dat267/gpi/tui"
)

// Port of the run/init orchestration and the chat status/notification
// helpers of src/modes/interactive/interactive-mode.ts (init, run,
// clearEditor, showError, showWarning, showNewVersionNotification,
// showPackageUpdateNotification, getStartupExpansionState).
//
// Divergences: the collaborators are injected function values (D124); the
// extension/resource seams stay out of scope (D41).

// LatestRelease is the new-version notification payload.
type LatestRelease struct {
	Version string
	Note    string
}

// RunWiring drives init and the main loop.
type RunWiring struct {
	Startup *StartupWiring

	UI       tui.TUI
	Settings *coding.SettingsManager
	Terminal tui.Terminal

	// HeaderContainer holds the built-in/custom header.
	HeaderContainer *tui.Container
	// BuiltInHeader is the constructed startup header.
	BuiltInHeader tui.Component
	// Chat is the transcript container.
	Chat *tui.Container
	// OutputPad is the error-message padding.
	OutputPad int
	// ToolOutputExpanded seeds the header expansion.
	ToolOutputExpanded bool
	// Verbose forces the expanded header and the startup notices.
	Verbose bool
	// AppName is the product name.
	AppName string
	// Version is the running version.
	Version string

	// SetupKeyHandlers enables the full key handlers after tool setup.
	SetupKeyHandlers func()
	// SetupSubmitHandler enables the submit handler.
	SetupSubmitHandler func()
	// RebindSession rebinds the session (extensions/resources).
	RebindSession func(ctx context.Context) error
	// RenderInitialMessages renders the initial transcript.
	RenderInitialMessages func()
	// OnThemeChange registers the theme-file watcher callback.
	OnThemeChange func(callback func()) func()
	// OnBranchChange registers the git-branch watcher callback.
	OnBranchChange func(callback func()) func()
	// LoadHighlightLanguages loads the syntax grammars in the background.
	LoadHighlightLanguages func() error
	// RefreshModelCatalogs refreshes the catalogs in the background.
	RefreshModelCatalogs func(ctx context.Context) error
	// CheckVersion checks for a new release.
	CheckVersion func(version string) (*LatestRelease, bool)
	// CheckPackageUpdates checks for package updates.
	CheckPackageUpdates func() []string
	// CheckTmux returns the tmux warning ("" when fine).
	CheckTmux func() string
	// TakeCrash returns the unnotified crash record.
	TakeCrash func() *coding.CrashRecord
	// MaybeSaveTrust saves the implicit project trust after a reload.
	MaybeSaveTrust func() bool
	// Prompt sends a prompt to the session.
	Prompt func(ctx context.Context, text string) error
	// ShowStatus/ShowError/ShowWarning report messages.
	ShowStatus  func(message string)
	ShowError   func(message string)
	ShowWarning func(message string)
	// WarnAnthropic runs the subscription-auth warning check.
	WarnAnthropic func(ctx context.Context)
	// RequestRender requests a render.
	RequestRender func()

	initialized bool
}

func (w *RunWiring) showError(message string) {
	if w.ShowError != nil {
		w.ShowError(message)
	}
}

func (w *RunWiring) showWarning(message string) {
	if w.ShowWarning != nil {
		w.ShowWarning(message)
	}
}

func (w *RunWiring) showStatus(message string) {
	if w.ShowStatus != nil {
		w.ShowStatus(message)
	}
}

func (w *RunWiring) requestRender() {
	if w.RequestRender != nil {
		w.RequestRender()
	} else if w.UI != nil {
		w.UI.RequestRender(false)
	}
}

// ClearEditor empties the editor.
func (w *RunWiring) ClearEditor(setText func(string)) {
	if setText != nil {
		setText("")
	}
	w.requestRender()
}

// ShowChatError appends an error line to the chat.
func (w *RunWiring) ShowChatError(message string) {
	if w.Chat == nil {
		return
	}
	theme := ActiveTheme()
	w.Chat.AddChild(tui.NewSpacer(1))
	w.Chat.AddChild(tui.NewText(theme.Fg("error", "Error: "+message), w.OutputPad, 0, nil))
	w.requestRender()
}

// ShowChatWarning appends a warning line to the chat.
func (w *RunWiring) ShowChatWarning(message string) {
	if w.Chat == nil {
		return
	}
	theme := ActiveTheme()
	w.Chat.AddChild(tui.NewSpacer(1))
	w.Chat.AddChild(tui.NewText(theme.Fg("warning", "Warning: "+message), 1, 0, nil))
	w.requestRender()
}

// ShowNewVersionNotification renders the update card.
func (w *RunWiring) ShowNewVersionNotification(release LatestRelease, hyperlinks bool) {
	if w.Chat == nil {
		return
	}
	theme := ActiveTheme()
	action := theme.Fg("accent", w.AppName+" update")
	updateInstruction := theme.Fg("muted", "New version "+release.Version+" is available. Run ") + action
	changelogURL := "https://pi.dev/changelog"
	changelogLink := theme.Fg("accent", changelogURL)
	if hyperlinks {
		changelogLink = tui.Hyperlink(theme.Fg("accent", changelogURL), changelogURL)
	}
	changelogLine := theme.Fg("muted", "Changelog: ") + changelogLink

	warningBorder := func(text string) string { return theme.Fg("warning", text) }
	w.Chat.AddChild(tui.NewSpacer(1))
	w.Chat.AddChild(NewDynamicBorder(warningBorder))
	w.Chat.AddChild(tui.NewText(theme.Bold(theme.Fg("warning", "Update Available"))+"\n"+updateInstruction, 1, 0, nil))
	if note := strings.TrimSpace(release.Note); note != "" {
		w.Chat.AddChild(tui.NewSpacer(1))
		w.Chat.AddChild(tui.NewMarkdown(note, 1, 0, w.markdownTheme(),
			&tui.DefaultTextStyle{Color: func(text string) string { return theme.Fg("muted", text) }},
			tui.MarkdownOptions{}))
		w.Chat.AddChild(tui.NewSpacer(1))
	}
	w.Chat.AddChild(tui.NewText(changelogLine, 1, 0, nil))
	w.Chat.AddChild(NewDynamicBorder(warningBorder))
	w.requestRender()
}

// ShowPackageUpdateNotification renders the package-update card.
func (w *RunWiring) ShowPackageUpdateNotification(packages []string) {
	if w.Chat == nil {
		return
	}
	theme := ActiveTheme()
	action := theme.Fg("accent", w.AppName+" update --extensions")
	updateInstruction := theme.Fg("muted", "Package updates are available. Run ") + action
	lines := make([]string, 0, len(packages))
	for _, pkg := range packages {
		lines = append(lines, "- "+pkg)
	}
	packageLines := strings.Join(lines, "\n")

	warningBorder := func(text string) string { return theme.Fg("warning", text) }
	w.Chat.AddChild(tui.NewSpacer(1))
	w.Chat.AddChild(NewDynamicBorder(warningBorder))
	w.Chat.AddChild(tui.NewText(theme.Bold(theme.Fg("warning", "Package Updates Available"))+"\n"+
		updateInstruction+"\n"+theme.Fg("muted", "Packages:")+"\n"+packageLines, 1, 0, nil))
	w.Chat.AddChild(NewDynamicBorder(warningBorder))
	w.requestRender()
}

func (w *RunWiring) markdownTheme() tui.MarkdownTheme {
	if w.Startup != nil {
		return w.Startup.GetMarkdownThemeWithSettings(GetMarkdownTheme())
	}
	return GetMarkdownTheme()
}

// GetStartupExpansionState reports whether the header starts expanded.
func (w *RunWiring) GetStartupExpansionState() bool {
	return w.Verbose || w.ToolOutputExpanded
}

// BuildStartupHeader builds the logo + instructions header.
func (w *RunWiring) BuildStartupHeader(scopedModels []coding.ScopedModel) tui.Component {
	theme := ActiveTheme()
	logo := theme.Bold(theme.Fg("accent", w.AppName)) + theme.Fg("dim", " v"+w.Version)

	hint := func(keybinding string, description string) string { return KeyHint(keybinding, description) }
	expandedInstructions := strings.Join([]string{
		hint("app.interrupt", "to interrupt"),
		hint("app.clear", "to clear"),
		RawKeyHint(KeyText("app.clear")+" twice", "to exit"),
		hint("app.exit", "to exit (empty)"),
		hint("app.suspend", "to suspend"),
		KeyHint("tui.editor.deleteToLineEnd", "to delete to end"),
		hint("app.thinking.cycle", "to cycle thinking level"),
		RawKeyHint(KeyText("app.model.cycleForward")+"/"+KeyText("app.model.cycleBackward"), "to cycle models"),
		hint("app.model.select", "to select model"),
		hint("app.tools.expand", "to expand tools"),
		hint("app.thinking.toggle", "to expand thinking"),
		hint("app.editor.external", "for external editor"),
		RawKeyHint("/", "for commands"),
		RawKeyHint("!", "to run bash"),
		RawKeyHint("!!", "to run bash (no context)"),
		hint("app.message.followUp", "to queue follow-up"),
		hint("app.message.dequeue", "to edit all queued messages"),
		hint("app.clipboard.pasteImage", "to paste image (with text fallback)"),
		RawKeyHint("drop files", "to attach"),
	}, "\n")
	compactInstructions := strings.Join([]string{
		hint("app.interrupt", "interrupt"),
		RawKeyHint(KeyText("app.clear")+"/"+KeyText("app.exit"), "clear/exit"),
		RawKeyHint("/", "commands"),
		RawKeyHint("!", "bash"),
		hint("app.tools.expand", "more"),
	}, theme.Fg("muted", " · "))
	compactOnboarding := theme.Fg("dim",
		"Press "+KeyText("app.tools.expand")+" to show full startup help and loaded resources.")
	onboarding := theme.Fg("dim",
		"Pi can explain its own features and look up its docs. Ask it how to use or extend Pi.")
	expanded := w.GetStartupExpansionState()
	header := NewExpandableText(
		func() string {
			return logo + "\n" + compactInstructions + "\n" + compactOnboarding + "\n\n" + onboarding
		},
		func() string { return logo + "\n" + expandedInstructions + "\n\n" + onboarding },
		expanded, 1, 0)
	return header
}

// BuildMinimalHeader builds the silenced header.
func (w *RunWiring) BuildMinimalHeader() tui.Component {
	return tui.NewText("", 0, 0, nil)
}

// Init mounts the UI, renders the header and wires the startup handlers.
func (w *RunWiring) Init(ctx context.Context, scopedModels []coding.ScopedModel, registerSignals func(), mount func(), quietStartup bool) {
	if w.initialized {
		return
	}
	if registerSignals != nil {
		registerSignals()
	}
	if mount != nil {
		mount()
	}
	if w.UI != nil {
		w.UI.Start()
	}
	w.initialized = true

	// Header (unless silenced).
	if w.HeaderContainer != nil {
		if w.Verbose || !quietStartup {
			w.BuiltInHeader = w.BuildStartupHeader(scopedModels)
			w.HeaderContainer.AddChild(tui.NewSpacer(1))
			w.HeaderContainer.AddChild(w.BuiltInHeader)
			w.HeaderContainer.AddChild(tui.NewSpacer(1))
		} else {
			w.BuiltInHeader = w.BuildMinimalHeader()
			w.HeaderContainer.AddChild(w.BuiltInHeader)
		}
	}
	w.requestRender()

	// Enable the remaining handlers after the managed-tool setup.
	if w.SetupKeyHandlers != nil {
		w.SetupKeyHandlers()
	}
	if w.SetupSubmitHandler != nil {
		w.SetupSubmitHandler()
	}
	w.requestRender()

	// Session binding before the initial messages.
	if w.RebindSession != nil {
		_ = w.RebindSession(ctx)
	}
	if w.RenderInitialMessages != nil {
		w.RenderInitialMessages()
	}

	// Watchers.
	if w.OnThemeChange != nil {
		w.OnThemeChange(func() {
			if w.UI != nil {
				w.UI.Invalidate()
			}
			w.requestRender()
		})
	}
	if w.OnBranchChange != nil {
		w.OnBranchChange(func() { w.requestRender() })
	}
	if w.Startup != nil {
		w.Startup.UpdateAvailableProviderCount()
	}
	if w.UI != nil {
		w.UI.RenderNow(false)
	}
	if w.LoadHighlightLanguages != nil {
		go func() {
			_ = w.LoadHighlightLanguages()
			if !w.initialized {
				return
			}
			if w.UI != nil {
				w.UI.Invalidate()
			}
			w.requestRender()
		}()
	}
}

// InitOptions are the init orchestration knobs.
type InitOptions struct {
	ScopedModels    []coding.ScopedModel
	QuietStartup    bool
	RegisterSignals func()
	Mount           func()
}

// Run initializes and runs the interactive loop.
func (w *RunWiring) Run(ctx context.Context, options InitOptions, runOptions RunOptions) {
	w.Init(ctx, options.ScopedModels, options.RegisterSignals, options.Mount, options.QuietStartup)

	if runOptions.Offline != true && w.RefreshModelCatalogs != nil {
		refreshCtx, cancel := context.WithCancel(ctx)
		go func() {
			defer cancel()
			_ = w.RefreshModelCatalogs(refreshCtx)
			if w.Startup != nil {
				w.Startup.UpdateAvailableProviderCount()
			}
		}()
	}
	if w.CheckVersion != nil {
		go func() {
			if release, ok := w.CheckVersion(w.Version); ok && release != nil {
				w.ShowNewVersionNotification(*release, runOptions.Hyperlinks)
			}
		}()
	}
	if w.CheckPackageUpdates != nil {
		go func() {
			if updates := w.CheckPackageUpdates(); len(updates) > 0 {
				w.ShowPackageUpdateNotification(updates)
			}
		}()
	}
	if w.CheckTmux != nil {
		go func() {
			if warning := w.CheckTmux(); warning != "" {
				w.showWarning(warning)
			}
		}()
	}

	// Startup warnings (in upstream order).
	for _, diagnostic := range runOptions.StartupDiagnostics {
		switch diagnostic.Type {
		case "error":
			w.ShowChatError(diagnostic.Message)
		case "warning":
			w.ShowChatWarning(diagnostic.Message)
		default:
			w.showStatus(diagnostic.Message)
		}
	}
	if len(runOptions.MigratedProviders) > 0 {
		w.ShowChatWarning("Migrated credentials to auth.json: " + strings.Join(runOptions.MigratedProviders, ", "))
	}
	if runOptions.ModelsJSONError != "" {
		w.ShowChatError("models.json error: " + runOptions.ModelsJSONError)
	}
	if runOptions.ModelFallbackMessage != "" {
		w.ShowChatWarning(runOptions.ModelFallbackMessage)
	}
	if w.TakeCrash != nil {
		if crash := w.TakeCrash(); crash != nil {
			w.ShowChatWarning(w.AppName + " crashed on " + crash.Timestamp + " (" + crash.Message +
				"). Run /bug to report it; the crash details are attached automatically.")
		}
	}
	if w.WarnAnthropic != nil {
		go w.WarnAnthropic(ctx)
	}

	// Initial messages.
	if runOptions.InitialMessage != "" && w.Prompt != nil {
		if err := w.Prompt(ctx, runOptions.InitialMessage); err != nil {
			w.ShowChatError(err.Error())
		}
	}
	for _, message := range runOptions.InitialMessages {
		if w.Prompt == nil {
			break
		}
		if err := w.Prompt(ctx, message); err != nil {
			w.ShowChatError(err.Error())
		}
	}

	// Main loop.
	if w.Startup == nil || w.Prompt == nil {
		return
	}
	for {
		input, ok := w.Startup.GetUserInput(ctx)
		if !ok {
			return
		}
		if err := w.Prompt(ctx, input); err != nil {
			w.ShowChatError(err.Error())
		}
	}
}

// RunOptions are the run orchestration inputs.
type RunOptions struct {
	Offline              bool
	Hyperlinks           bool
	StartupDiagnostics   []StartupDiagnostic
	MigratedProviders    []string
	ModelsJSONError      string
	ModelFallbackMessage string
	InitialMessage       string
	InitialMessages      []string
}

// StartupDiagnostic is a pre-init diagnostic.
type StartupDiagnostic struct {
	Type    string // "error" | "warning" | other
	Message string
}
