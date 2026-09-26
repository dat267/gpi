package interactive

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/internal/offloop"
	"github.com/dat267/pier/tui"
)

// Port of the InteractiveMode constructor (src/modes/interactive/
// interactive-mode.ts): the composition layer that wires the ported
// interactive-mode wirings into a runnable app. main.ts owns the process-level
// boot (flags, session/services creation); this file owns the object graph.
//
// Divergences:
//   D131: process-level effects and resource-loader/extension collaborators are
//   injected seams.
//   D132: branch summarization is now tracked as compaction (IsCompacting) and
//   can be aborted (AbortBranchSummary), mirroring upstream's
//   _branchSummaryAbortController.
//   D133: the app's transcript session adapter returns no extension tool
//   renderers (D41); built-in tools render through the fallback renderer.
//   D134: the app takes a pre-booted session/settings/runtime (main.ts builds
//   them); the syntax-grammar load, package-update check and extension rebind
//   are no-ops (out of scope).

// AppSession adapts *coding.AgentSession to the interactive-mode session
// interfaces. It adds the extension tool-renderer lookup (D133) that the
// transcript expects.
type AppSession struct {
	*coding.AgentSession
}

// GetToolRenderers resolves a tool's custom renderers: an extension-registered
// definition falling back to the built-in one. Extension mechanics are out of
// scope (D41), so this always resolves to the built-in renderers.
func (s *AppSession) GetToolRenderers(toolName string) *ToolRenderers {
	return WithBuiltInRenderers(toolName, nil)
}

// AppOptions are the booted collaborators the app composes.
type AppOptions struct {
	Cwd      string
	AgentDir string
	Terminal tui.Terminal

	TuiMode      string
	Version      string
	AppName      string
	QuietStartup bool
	Verbose      bool

	Settings    *coding.SettingsManager
	Session     *coding.AgentSession
	Runtime     *coding.ModelRuntime
	SessionMgr  *coding.SessionManager
	Keybindings *AppKeybindingsManager

	// Offloop is the command's off-loop queue group. The app registers its own
	// queues (theme, pre-render) here and StopMode stops them all at teardown;
	// nil gives the app a group of its own.
	Offloop *offloop.Group

	// InitialThemeSetting seeds the theme controller.
	InitialThemeSetting *string
	// ProjectTrustOverride is --approve/--no-approve: it settles project trust
	// for the run without consulting or updating the trust store, which is what a
	// session switch re-resolves against.
	ProjectTrustOverride *bool
	// InitialProjectTrust is the boot answer for Cwd's project, which seeds the
	// per-project memory so a switch back to it reuses the decision.
	InitialProjectTrust *bool
	// Offline disables the startup catalog refresh.
	Offline bool
	// Hyperlinks enables OSC 8 links in the update cards.
	Hyperlinks bool
	// StdoutIsTTY gates the resume hint (upstream's process.stdout.isTTY).
	// Nil detects os.Stdout at shutdown.
	StdoutIsTTY *bool
	// InitialMessage/InitialMessages are sent after startup.
	InitialMessage  string
	InitialMessages []string
	// ModelFallbackMessage explains a model restore or resolution fallback; it is
	// shown as a chat warning at startup.
	ModelFallbackMessage string
	// StartupDiagnostics are reported before the first render (upstream's
	// startupDiagnostics, e.g. a --models pattern that matched nothing).
	StartupDiagnostics []StartupDiagnostic

	// Process seams (D122).
	Exit                func(code int)
	WriteOut            func(text string)
	WriteErr            func(text string)
	RegisterSignal      func(sig os.Signal, handler func()) func()
	OnTerminalError     func(handler func(error)) func()
	OnUncaughtException func(handler func(error)) func()
	Platform            string
}

// App is the composed interactive mode.
type App struct {
	// projectTrustByCwd remembers each project's resolved trust for the run
	// (upstream main.ts's projectTrustByCwd): a project resolved once is neither
	// asked nor re-read, so a decision cannot change under the user's feet when
	// the store changes mid-run.
	projectTrustByCwd map[string]bool

	// AutoTrustOnReloadCwd is the cwd /reload may implicitly trust
	// (main.ts:706): captured at startup when the project has no
	// trust-requiring resources, so a later reload can save trust for a
	// project that gained them mid-session. Upstream also gates on the
	// --trust CLI override, which the port does not have.
	autoTrustOnReloadCwd string
	options              AppOptions

	// initialUI is the renderer created at composition time; the lifecycle
	// swaps it on /tui switches and the exit replay.
	initialUI tui.TUI

	ui          tui.TUI
	theme       *InteractiveThemeController
	settings    *coding.SettingsManager
	session     *AppSession
	sessionMgr  *coding.SessionManager
	runtime     *coding.ModelRuntime
	keybindings *AppKeybindingsManager

	headerContainer          *tui.Container
	loadedResourcesContainer *tui.Container
	documentContainer        *tui.Container
	chat                     *tui.Container
	pendingMessages          *tui.Container
	statusContainer          *tui.Container
	widgetAbove              *tui.Container
	widgetBelow              *tui.Container
	editorContainer          *tui.Container
	footerContainer          *tui.Container

	defaultEditor *CustomEditor
	footer        *FooterComponent
	footerData    *coding.FooterDataProvider
	// Display is the single owner of the display options shared by the
	// transcript, event dispatcher, run wiring, trust wiring and UI state.
	display    *DisplayOptions
	uiState    *InteractiveUIState
	transcript *TranscriptRenderer
	// TranscriptScrollView is the fullscreen transcript scroll view (upstream's
	// transcriptScrollView).
	transcriptScrollView *tui.ScrollView
	queue                *QueueController
	events               *EventDispatcher
	slot                 *SelectorSlot

	lifecycle    *Lifecycle
	startup      *StartupWiring
	runner       *RunWiring
	key          *KeyWiring
	submit       *SubmitWiring
	selectors    *SelectorWiring
	settingsW    *SettingsWiring
	models       *ModelWiring
	sessions     *SessionWiring
	auth         *AuthWiring
	commands     *CommandWiring
	trust        *TrustCrashWiring
	autocomplete *AutocompleteWiring

	unsubscribe func()
	// sessionEvents is the producer→loop queue (interactivemode_eventqueue.go).
	sessionEvents *sessionEventQueue
	// loopInputs/loopResizes are the terminal producers' channels: the stdin
	// reader sends complete sequences and the resize watcher sends ticks; the
	// run loop dispatches them (stage 3). loopInputsClosed releases a producer
	// parked on a full channel at shutdown.
	runCtx           atomic.Pointer[context.Context]
	loopInputs       chan string
	loopRawInputs    chan string
	rawTerminal      tui.RawInputTerminal
	loopResizes      chan struct{}
	loopSignals      chan os.Signal
	loopInputsClosed chan struct{}
	loopInputsOnce   sync.Once
	initialized      bool

	// prerenderQueue warms deferred transcript components off the UI loop.
	// Registered in offloopGroup; stopped by StopMode so a warm cannot touch the
	// renderer after teardown.
	prerenderQueue *offloop.Queue

	// offloopGroup owns the app's off-loop queues (theme, pre-render) so
	// teardown is one StopAll. In production the command passes its group, which
	// also owns the settings and session queues.
	offloopGroup *offloop.Group
}

// NewApp builds the interactive-mode object graph.
func NewApp(options AppOptions) *App {
	if options.AppName == "" {
		options.AppName = coding.AppName
	}
	if options.AgentDir == "" {
		options.AgentDir = coding.GetAgentDir()
	}
	if options.TuiMode == "" {
		options.TuiMode = "regular"
	}
	if options.Exit == nil {
		options.Exit = os.Exit
	}
	if options.WriteOut == nil {
		options.WriteOut = func(text string) { _, _ = os.Stdout.WriteString(text) }
	}
	if options.WriteErr == nil {
		options.WriteErr = func(text string) { _, _ = os.Stderr.WriteString(text) }
	}

	keybindings := options.Keybindings
	if keybindings == nil {
		keybindings = CreateAppKeybindings(options.AgentDir)
	}
	tui.SetKeybindings(keybindings.KeybindingsManager)

	terminal := options.Terminal
	if terminal == nil {
		terminal = tui.NewProcessTerminal(nil, nil)
	}

	options.Terminal = terminal

	app := &App{
		options:     options,
		settings:    options.Settings,
		sessionMgr:  options.SessionMgr,
		runtime:     options.Runtime,
		keybindings: keybindings,
	}

	// One owner for the off-loop queues. The command passes its group so a
	// single StopAll at teardown covers the settings and session queues too; a
	// standalone app gets its own.
	app.offloopGroup = options.Offloop
	if app.offloopGroup == nil {
		app.offloopGroup = offloop.NewGroup()
	}

	// Renderer + theme. app.UI is the stable forwarding reference (upstream's
	// createInteractiveTuiReference(() => this.renderer)): SwitchTuiMode swaps
	// the lifecycle's renderer and every holder of app.UI follows it.
	app.loopInputs = make(chan string, loopInputCapacity)
	app.loopRawInputs = make(chan string, loopInputCapacity)
	app.loopResizes = make(chan struct{}, 1)
	app.loopSignals = make(chan os.Signal, 4)
	app.loopInputsClosed = make(chan struct{})

	aInitialUI := app.newLoopTui(InteractiveTuiOptions{
		TuiMode:                options.TuiMode,
		ShowHardwareCursor:     options.Settings.GetShowHardwareCursor(),
		LogDirectory:           options.AgentDir,
		Terminal:               terminal,
		FullscreenCopyOnSelect: appBoolPtr(options.Settings.GetFullscreenCopyOnSelect()),
		OnRightClickPaste:      app.handleRightClickPaste,
	})
	app.initialUI = aInitialUI
	app.ui = tui.NewTuiReference(func() tui.TUI {
		if app.lifecycle != nil {
			if current := app.lifecycle.CurrentUI(); current != nil {
				return current
			}
		}
		return app.initialUI
	})
	app.ui.SetClearOnShrink(options.Settings.GetClearOnShrink())
	app.theme = NewInteractiveThemeController(ThemeControllerOptions{
		UI:                  themeUIAdapter{ui: app.ui},
		GetSettingsManager:  func() ThemeSettings { return themeSettingsAdapter{options.Settings} },
		ShowError:           func(message string) { app.showError(message) },
		OnChanged:           func() { app.updateEditorBorderColor() },
		InitialThemeSetting: options.InitialThemeSetting,
		// The COLORFGBG fallback for terminals that do not answer OSC 11
		// (upstream detectTerminalBackgroundFromEnv). The live probe is the
		// deferred OSC 11 request from RunWiring.OnStarted (D165).
		Env: os.Getenv,
		// Theme loads read files from disk; the selector paths that reach the
		// controller run on the UI loop, so they load off it.
		Marshal:    func(fn func()) { app.ui.Post(fn) },
		ThemeQueue: app.offloopGroup.Queue(),
	})

	// Containers.
	app.headerContainer = &tui.Container{}
	app.loadedResourcesContainer = &tui.Container{}
	app.chat = &tui.Container{}
	app.documentContainer = &tui.Container{}
	app.documentContainer.AddChild(app.headerContainer)
	app.documentContainer.AddChild(app.loadedResourcesContainer)
	app.documentContainer.AddChild(app.chat)
	app.pendingMessages = &tui.Container{}
	app.statusContainer = &tui.Container{}
	app.widgetAbove = &tui.Container{}
	app.widgetBelow = &tui.Container{}
	app.editorContainer = &tui.Container{}
	app.footerContainer = &tui.Container{}

	// Editor.
	app.defaultEditor = NewCustomEditor(editorHostAdapter{ui: app.ui}, GetEditorTheme(), keybindings, CustomEditorOptions{
		PaddingX:               options.Settings.GetEditorPaddingX(),
		AutocompleteMaxVisible: options.Settings.GetAutocompleteMaxVisible(),
		EmbedWorkingStatus:     true,
	})
	app.editorContainer.AddChild(app.defaultEditor)

	// Footer + data provider.
	cwd := options.Cwd
	if cwd == "" && options.SessionMgr != nil {
		cwd = options.SessionMgr.GetCwd()
	}
	app.footerData = coding.NewFooterDataProvider(cwd, coding.FooterDataProviderOptions{})
	app.session = &AppSession{AgentSession: options.Session}
	app.footer = NewFooterComponent(app.session, app.footerData)
	app.footer.SetAutoCompactEnabled(options.Session.AutoCompactionEnabled())
	app.footerContainer.AddChild(app.footer)

	// Display options: one value shared by the transcript, event dispatcher,
	// run wiring, trust wiring and UI state (see DisplayOptions). It must
	// exist before the wirings that reference it.
	app.display = &DisplayOptions{
		OutputPad:           options.Settings.GetOutputPad(),
		HideThinkingBlock:   options.Settings.GetHideThinkingBlock(),
		HiddenThinkingLabel: defaultHiddenThinkingLabel,
	}

	// Trust/crash helpers (used by the event dispatcher and lifecycle).
	app.trust = newTrustCrashWiring(app)

	// Upstream main.ts:706: capture at startup so a later /reload can save
	// an implicitly-trusted project whose cwd gained trust-requiring
	// resources during the session.
	app.projectTrustByCwd = map[string]bool{}
	if options.InitialProjectTrust != nil && options.Cwd != "" {
		app.projectTrustByCwd[coding.CanonicalizePath(coding.ResolvePath(options.Cwd, "", coding.PathInputOptions{}))] = *options.InitialProjectTrust
	}
	app.autoTrustOnReloadCwd = ""
	if app.sessionMgr != nil && !coding.HasTrustRequiringProjectResources(app.sessionMgr.GetCwd()) {
		app.autoTrustOnReloadCwd = app.sessionMgr.GetCwd()
	}

	// UI state + transcript.
	app.uiState = NewInteractiveUIState(app.ui)

	app.uiState.Display = app.display
	app.uiState.FooterData = app.footerData
	app.uiState.Footer = app.footer
	app.uiState.StatusContainer = app.statusContainer
	app.uiState.ChatContainer = app.chat
	app.uiState.WidgetContainerAbove = app.widgetAbove
	app.uiState.WidgetContainerBelow = app.widgetBelow
	app.uiState.FooterContainer = app.footerContainer
	app.uiState.HeaderContainer = app.headerContainer
	app.uiState.DefaultEditor = app.defaultEditor
	app.uiState.Editor = app.defaultEditor
	app.uiState.WorkingMessage = app.uiState.DefaultWorkingMessage

	app.transcript = NewTranscriptRenderer(app.chat, app.ui, app.settings, app.session, app.sessionMgr)
	app.prerenderQueue = app.offloopGroup.Queue()
	app.transcript.PrerenderQueue = app.prerenderQueue
	app.transcript.Footer = app.footer
	app.transcript.Editor = app.defaultEditor
	app.transcript.Display = app.display
	app.transcript.MarkdownTheme = app.markdownTheme()
	// Upstream's renderInitialMessages draws the untrusted-project warning, so the
	// warning appears at startup and again whenever the transcript is rebuilt.
	if app.trust != nil {
		app.transcript.RenderProjectTrustWarning = app.trust.RenderProjectTrustWarningIfNeeded
	}

	app.queue = NewQueueController(app.ui, app.session, app.settings, app.defaultEditor, app.chat, app.pendingMessages)
	// The queue reports through these functions, and an unassigned one is a silent
	// no-op — every status it raised (thinking blocks, tool output, thinking
	// level, queued messages) disappeared, which made ctrl+t and ctrl+o look like
	// dead keys. Upstream's queue controller calls the mode's reporters directly.
	app.queue.ShowStatus = func(message string) { app.transcript.ShowStatus(message) }
	app.queue.ShowError = func(message string) { app.showError(message) }
	app.queue.ShowWarning = func(message string) { app.showWarning(message) }

	app.events = NewEventDispatcher(app.transcript, app.uiState, app.footer, app.settings, app.session, app.sessionMgr, app.defaultEditor)
	app.events.ShowError = func(message string) { app.showError(message) }
	app.events.UpdatePendingMessagesDisplay = app.queue.UpdatePendingMessagesDisplay
	app.events.Display = app.display
	app.events.MarkdownTheme = app.markdownTheme()
	app.events.TerminalProgress = func(active bool) { terminal.SetProgress(active) }
	app.events.FlushCompactionQueue = func(willRetry bool) {
		app.queue.FlushCompactionQueue(context.Background(), willRetry)
	}
	app.events.CheckShutdownRequested = app.lifecycleCheckShutdown
	app.events.Init = func() { app.transcript.RenderInitialMessages() }
	// The compaction-queue flush can start a turn; run it off-loop so events
	// keep draining while it runs.
	app.events.StartWork = func(fn func(context.Context) error) {
		if app.runner.StartWork != nil {
			app.runner.StartWork(fn)
			return
		}
		fn(context.Background())
	}

	app.slot = NewSelectorSlot(app.ui, app.editorContainer, app.defaultEditor)

	// Upstream initializes the widget containers with their default spacers
	// before mounting ("renderWidgets(); // Initialize with default spacer"):
	// the empty widgets-above container renders the blank line on top of the
	// divider above the input box.
	app.uiState.RenderWidgets()

	app.lifecycle = NewLifecycle(LifecycleOptions{
		UI: aInitialUI,
		CreateTui: func(mode string) tui.TUI {
			return app.newLoopTui(InteractiveTuiOptions{
				TuiMode:                mode,
				ShowHardwareCursor:     options.Settings.GetShowHardwareCursor(),
				LogDirectory:           options.AgentDir,
				Terminal:               terminal,
				FullscreenCopyOnSelect: appBoolPtr(options.Settings.GetFullscreenCopyOnSelect()),
				OnRightClickPaste:      app.handleRightClickPaste,
			})
		},
		Session:      app.session,
		Settings:     app.settings,
		Terminal:     terminal,
		TuiMode:      options.TuiMode,
		LogDirectory: options.AgentDir,
		AppTitle:     options.AppName,
		SessionCwd:   func() string { return app.sessionMgr.GetCwd() },
		SessionName:  func() string { return app.sessionMgr.GetSessionName() },
		Exit:         options.Exit,
		WriteOut:     options.WriteOut,
		WriteErr:     options.WriteErr,
		Platform:     options.Platform,

		SignalSink: func(sig os.Signal) {
			// Non-blocking: shutdown signals coalesce.
			select {
			case app.loopSignals <- sig:
			default:
			}
		},
		RegisterSignal:      options.RegisterSignal,
		OnTerminalError:     options.OnTerminalError,
		OnUncaughtException: options.OnUncaughtException,

		DisableThemeAutoSync:    func() { StopThemeWatcher() },
		OnTuiModeSwitched:       func() { app.theme.RebindTUI() },
		RecordCrash:             func(kind string, err error) bool { return app.trust.RecordCrash(kind, err) },
		CrashReportInstructions: func() string { return app.trust.CrashReportInstructions() }, // Upstream prints "To resume this session: pi --session …" after the
		// interactive shutdown (interactive-mode.ts shutdown(), chalk.dim
		// prefix).
		ResumeCommand: func() string {
			stdoutIsTTY := options.StdoutIsTTY
			if stdoutIsTTY == nil {
				detected := term.IsTerminal(int(os.Stdout.Fd()))
				stdoutIsTTY = &detected
			}
			return FormatResumeCommand(app.sessionMgr, options.AppName, *stdoutIsTTY)
		},
		FormatResumeMessage: func(command string) string {
			return "\x1b[2mTo resume this session:\x1b[22m " + command
		},
		FullscreenExitOutput: func() string { return options.Settings.GetFullscreenExitOutput() },
		StopMode:             func(output string) { app.StopMode(output) },
	})

	app.startup = newStartupWiring(app)
	// The submission channel exists from composition so the run loop always
	// has a consumer side to select on.
	app.startup.InitInputs()

	app.sessionEvents = newSessionEventQueue()

	app.runner = newRunWiring(app)

	app.selectors = newSelectorWiring(app)

	app.settingsW = newSettingsWiring(app)

	app.models = newModelWiring(app)

	app.sessions = newSessionWiring(app)

	app.auth = newAuthWiring(app)

	app.commands = newCommandWiring(app)

	app.key = newKeyWiring(app)

	app.submit = newSubmitWiring(app)

	app.autocomplete = newAutocompleteWiring(app)

	return app
}

// skillCommands converts the session's loaded skills into autocomplete slash
// commands (upstream builds `/skill:<name>` entries from the resource loader).
func (a *App) skillCommands() []SkillCommand {
	skills := a.session.Skills()
	commands := make([]SkillCommand, 0, len(skills))
	for _, skill := range skills {
		commands = append(commands, SkillCommand{
			Name: skill.Name, Description: skill.Description, FilePath: skill.FilePath,
		})
	}
	return commands
}

// Init initializes and mounts the app.
func (a *App) Init(ctx context.Context) {
	if a.initialized {
		return
	}
	a.lifecycle.RegisterSignalHandlers()
	a.theme.ApplyFromSettings()
	// Build the shared fullscreen layout (scrollable transcript + fixed dock)
	// and mount it as the renderer's layout root (upstream init).
	theme := ActiveTheme()
	viewport := CreateChatViewport(ChatViewportOptions{
		Document:            a.documentContainer,
		PendingMessages:     a.pendingMessages,
		Status:              a.statusContainer,
		WidgetsAbove:        a.widgetAbove,
		Editor:              a.editorContainer,
		WidgetsBelow:        a.widgetBelow,
		Footer:              a.footerContainer,
		Scrollbar:           tui.ScrollViewScrollbar(a.settings.GetFullscreenScrollbar()),
		ScrollbarTrackStyle: func(text string) string { return theme.Fg("scrollbarTrack", text) },
		ScrollbarThumbStyle: func(text string) string { return theme.Fg("scrollbarThumb", text) },
	})
	a.transcriptScrollView = viewport.Transcript
	a.lifecycle.MountInteractiveTui(a.currentRenderer(), []tui.Component{
		a.documentContainer,
		a.pendingMessages,
		a.statusContainer,
		a.widgetAbove,
		a.editorContainer,
		a.widgetBelow,
		a.footerContainer,
	}, viewport.Root)
	a.ui.SetFocus(a.defaultEditor)
	a.initialized = true
	a.lifecycle.MarkInitialized()

	if a.unsubscribe == nil {
		// Pure producer: the callback only enqueues; the run loop applies the
		// event on the UI goroutine (interactivemode_eventqueue.go).
		a.unsubscribe = a.session.Subscribe(func(event *coding.SessionEvent) {
			a.sessionEvents.enqueue(event)
		})
	}
	a.autocomplete.SetupAutocompleteProvider()
}

// applySettingsDependentUI re-applies the settings-derived state (upstream
// applyRuntimeSettings, minus the terminal-capability and HTTP-dispatcher parts;
// the latter is configured once before any request exists, D40). It runs after
// /reload and after a session switch re-points the settings manager at another
// project.
func (a *App) applySettingsDependentUI() {
	hidden := a.settings.GetHideThinkingBlock()
	pad := a.settings.GetOutputPad()
	a.updateThinkingBlockVisibility(hidden)
	a.display.OutputPad = pad
	a.applyFullscreenScrollbarSetting()
	if altscreen, ok := tuiConcrete(a.ui).(*tui.AltScreen); ok {
		altscreen.SetCopyOnSelect(a.settings.GetFullscreenCopyOnSelect())
	}
	a.ui.SetShowHardwareCursor(a.settings.GetShowHardwareCursor())
	clearOnShrink := a.settings.GetClearOnShrink()
	a.ui.SetClearOnShrink(clearOnShrink)
	if !clearOnShrink && a.uiState != nil {
		a.uiState.ClearStatusContainerIfIdle()
	}
	a.defaultEditor.SetPaddingX(a.settings.GetEditorPaddingX())
	a.defaultEditor.SetAutocompleteMaxVisible(a.settings.GetAutocompleteMaxVisible())
}

// runContext is the active run's context (producers select on it).
var _ = 0

// runDone returns the run context's Done channel (closed when Run's context is
// cancelled); a background context when no run is active.
func (a *App) runDone() <-chan struct{} {
	if ctx := a.runCtx.Load(); ctx != nil {
		return (*ctx).Done()
	}
	return context.Background().Done()
}

// Run initializes and runs the interactive loop.
func (a *App) Run(ctx context.Context) {
	a.runCtx.Store(&ctx)
	if a.sessionEvents != nil {
		a.sessionEvents.SetContext(ctx)
	}
	if a.startup != nil {
		a.startup.SetContext(ctx)
	}
	a.Init(ctx)
	// modeldefault (D151): the session-start sync runs during creation, so its
	// notice is read back from the session and reported with the other startup
	// diagnostics.
	modelDefault := a.session.LastModelDefaultSync()
	a.runner.Run(ctx, InitOptions{
		ScopedModels:    a.session.ScopedModels(),
		QuietStartup:    a.options.QuietStartup,
		RegisterSignals: func() { a.lifecycle.RegisterSignalHandlers() },
		Mount:           func() {},
	}, RunOptions{
		Offline:              a.options.Offline,
		Hyperlinks:           a.options.Hyperlinks,
		StartupDiagnostics:   a.options.StartupDiagnostics,
		ModelFallbackMessage: a.options.ModelFallbackMessage,
		InitialMessage:       a.options.InitialMessage,
		InitialMessages:      a.options.InitialMessages,
		ModelDefaultMessage:  modelDefault.Message,
		ModelDefaultWarning:  modelDefault.Warning,
	})
	a.Close()
}

// Close unsubscribes and disposes the app.
func (a *App) Close() {
	if a.unsubscribe != nil {
		a.unsubscribe()
		a.unsubscribe = nil
	}
	// Release producers parked on the event/input queues (the loop has
	// stopped consuming by now).
	a.sessionEvents.Close()
	if a.startup != nil {
		a.startup.CloseInputs()
	}
	a.loopInputsOnce.Do(func() { close(a.loopInputsClosed) })
	a.footer.Dispose()
	a.footerData.Dispose()
	StopThemeWatcher()
}

// LifecycleCheckShutdown performs a requested shutdown.
func (a *App) lifecycleCheckShutdown() { a.lifecycle.CheckShutdownRequested() }

// newLoopTui creates a renderer wired to the UI loop: terminal input and
// resize notifications are delivered as channel messages instead of being
// dispatched inline (stage 3). With a D147 raw-input terminal the handler
// receives RAW stdin chunks; the loop reassembles them through FeedInput on
// the loop goroutine.
func (a *App) newLoopTui(options InteractiveTuiOptions) tui.TUI {
	// The global debug key runs the debug command (upstream sets
	// `ui.onDebug = () => this.handleDebugCommand()` after creating the renderer).
	if options.OnDebug == nil {
		options.OnDebug = a.runDebugCommand
	}
	screen := CreateInteractiveTui(options)
	if screen == nil {
		return screen
	}
	if raw, ok := options.Terminal.(tui.RawInputTerminal); ok {
		raw.EnableRawInput()
		a.rawTerminal = raw
	}
	screen.EnableLoopInput(
		func(data string) {
			select {
			case a.loopRawInputs <- data:
			case <-a.loopInputsClosed:
			case <-a.runDone():
			}
		},
		func() {
			select {
			case a.loopResizes <- struct{}{}:
			default:
			case <-a.runDone():
			}
		},
	)
	return screen
}

// runDebugCommand writes the debug log, which is what /debug does and what the
// renderer's global debug key runs (upstream wires ui.onDebug to
// handleDebugCommand).
func (a *App) runDebugCommand() {
	if a.commands == nil {
		return
	}
	a.commands.HandleDebugCommand(time.Now().UTC().Format("2006-01-02T15:04:05.000Z"))
}

// PostTerminalInput delivers a terminal sequence to the loop (test seam).
func (a *App) postTerminalInput(data string) {
	select {
	case a.loopInputs <- data:
	case <-a.loopInputsClosed:
	}
}

// LoopBeats reports the run loop's watchdog beat.
func (a *App) loopBeats() uint64 {
	if a.runner == nil {
		return 0
	}
	return a.runner.LoopBeats()
}

// LoopInputs/LoopResizes expose the producer channels to the run loop.

// runOffLoop dispatches blocking work to the loop's work goroutine when the
// loop is running; it reports whether the work was dispatched. Callers use the
// non-dispatched path for direct/test invocation.
// runDetached runs event-only work in its own goroutine without occupying
// RunWork's single work slot, so it cannot queue behind the active turn. It
// uses the run loop's context so shutdown cancellation still reaches it.
// handleRightClickPaste reads the clipboard and feeds it to the focused
// component as a bracketed paste, mirroring upstream's onRightClickPaste
// (interactive-mode.ts handleRightClickPaste). The renderer only invokes it on
// a Windows terminal session. Clipboard errors are swallowed (platform may
// have no clipboard tool or deny access); the focus is re-read after the read
// like upstream guards against it moving.
func (a *App) handleRightClickPaste() {
	target := a.ui.GetFocusedComponent()
	if target == nil {
		return
	}
	text, err := readClipboardText()
	if err != nil || text == "" {
		return
	}
	if a.ui.GetFocusedComponent() != target {
		return
	}
	handler, ok := target.(tui.InputHandler)
	if !ok {
		return
	}
	handler.HandleInput("\x1b[200~" + text + "\x1b[201~")
	a.ui.RequestRender(false)
}

func (a *App) runDetached(fn func(ctx context.Context) error) {
	ctx := context.Background()
	if a.runner != nil {
		if loopCtx := a.runner.LoopContext(); loopCtx != nil {
			ctx = loopCtx
		}
	}
	go func() { _ = fn(ctx) }()
}

// currentRenderer returns the concrete active renderer (upstream's
// this.renderer); app.UI is the forwarding reference.
func (a *App) currentRenderer() tui.TUI {
	if a.lifecycle != nil {
		if current := a.lifecycle.CurrentUI(); current != nil {
			return current
		}
	}
	return a.initialUI
}

// terminalWidth is the current terminal width for the deferred-transcript
// pre-render. It is read on the loop (the same call the screen makes when it
// renders); a missing terminal falls back to 80.
func (a *App) terminalWidth() int {
	if a.ui == nil {
		return 80
	}
	if terminal := a.ui.GetTerminal(); terminal != nil {
		return terminal.Columns()
	}
	return 80
}

// StopMode tears the whole mode down (upstream's stop()): the active selector,
// terminal progress, the status indicator, extension terminal input listeners,
// the footer and its data provider, the session-event subscription, the
// renderer (with the fullscreen exit output setting) and the signal handlers.
func (a *App) StopMode(fullscreenExitOutput string) {
	// Drain the settings and session queues (a clean exit cannot lose the last
	// save; signals route through the same hook), then stop every off-loop
	// queue the app owns — including the theme queue, which nothing used to
	// stop.
	if a.settings != nil {
		a.settings.FlushPersists()
	}
	if a.sessionMgr != nil {
		a.sessionMgr.FlushWrites()
	}
	if a.offloopGroup != nil {
		a.offloopGroup.StopAll()
	}
	if a.commands == nil {
		// Teardown before Init finished: stop the renderer only.
		a.lifecycle.StopInteractiveTui(fullscreenExitOutput)
		return
	}
	a.commands.Stop(
		fullscreenExitOutput,
		func() { a.slot.DisposeActiveSelector() },
		func() { a.uiState.ClearExtensionTerminalInputListeners() },
		func() { a.footer.Dispose() },
		func() { a.footerData.Dispose() },
		func() {
			if a.unsubscribe != nil {
				a.unsubscribe()
			}
		},
		func(output string) {
			// Upstream only stops the TUI once init completed.
			if a.lifecycle.IsInitialized() {
				a.lifecycle.StopInteractiveTui(output)
			}
		},
		a.lifecycle.UnregisterSignalHandlers,
	)
}

// RunnerShowChatError appends an error line.
func (a *App) runnerShowChatError(message string) { a.runner.ShowChatError(message) }

// RunnerShowChatWarning appends a warning line.
func (a *App) runnerShowChatWarning(message string) { a.runner.ShowChatWarning(message) }

// KeySetup enables the key handlers.
func (a *App) keySetup() { a.key.SetupKeyHandlers(func() int64 { return time.Now().UnixMilli() }) }

// SubmitSetup installs the editor submit handler.
func (a *App) submitSetup() {
	a.defaultEditor.OnSubmit = func(text string) {
		if a.lifecycle.IsInitialized() {
			a.submit.HandleSubmit(context.Background(), text)
		} else {
			a.submit.HandleStartupSubmit(text)
		}
	}
}

func (a *App) showError(message string) {
	if a.runner != nil {
		a.runner.ShowChatError(message)
		return
	}
	a.transcript.ShowStatus(message)
}

func (a *App) showWarning(message string) {
	if a.runner != nil {
		a.runner.ShowChatWarning(message)
		return
	}
	a.transcript.ShowStatus(message)
}

func (a *App) updateEditorBorderColor() {
	if a.queue != nil {
		a.queue.UpdateEditorBorderColor()
	}
}

// applyReloadedSettings re-applies settings-dependent state after /reload
// (upstream applyRuntimeSettings + restoreChatBeforeSessionStart +
// themeController.applyFromSettings + setupAutocompleteProvider). It runs on
// the UI loop. Terminal capability overrides and the HTTP dispatcher have no
// port counterpart (D41 scope).
func (a *App) applyReloadedSettings() {
	a.applySettingsDependentUI()
	// Upstream rebuildChatFromMessages (the reload's beforeSessionStart hook).
	if a.startup != nil {
		a.startup.RebuildChatFromMessages()
	}
	// Header expansion (upstream activeHeader.setExpanded).
	if a.uiState != nil {
		if expandable, ok := IsExpandable(a.uiState.BuiltInHeader); ok {
			expandable.SetExpanded(a.display.ToolOutputExpanded)
		}
	}
	// Reloaded resources (upstream showLoadedResources after /reload).
	a.showLoadedResources(false)
	// Custom theme files are re-read from disk by ApplyFromSettings.
	a.theme.ApplyFromSettings()
	// Rebuild the autocomplete provider (upstream setupAutocompleteProvider);
	// the skills list is a func, so it re-reads on the next query.
	a.autocomplete.SetupAutocompleteProvider()
}

func (a *App) updateThinkingBlockVisibility(hidden bool) {
	a.display.HideThinkingBlock = hidden
}

func (a *App) markdownTheme() *tui.MarkdownTheme {
	theme := GetMarkdownTheme()
	if a.startup != nil {
		theme = a.startup.GetMarkdownThemeWithSettings(theme)
	}
	return &theme
}

// applyFullscreenScrollbarSetting applies the fullscreen scrollbar setting to the
// transcript's scroll view.
func (a *App) applyFullscreenScrollbarSetting() {
	if a.transcriptScrollView != nil {
		a.transcriptScrollView.SetScrollbar(tui.ScrollViewScrollbar(a.settings.GetFullscreenScrollbar()))
	}
}

// modelSession returns the ModelSession adapter (its ModelRuntime returns the
// selector-runtime interface).
func (a *App) modelSession() ModelSession { return appModelSession{a.session} }

// commandSession returns the CommandSession adapter.
func (a *App) commandSession() CommandSession { return appCommandSession{a.session} }

// appModelSession overrides ModelRuntime to return ModelSelectorRuntime.
type appModelSession struct{ *AppSession }

func (m appModelSession) ModelRuntime() ModelSelectorRuntime { return m.AgentSession.ModelRuntime() }

// appCommandSession adapts CompactSession to the error-only signature.
type appCommandSession struct{ *AppSession }

func (c appCommandSession) CompactSession(ctx context.Context, customInstructions string) error {
	_, err := c.AgentSession.CompactSession(ctx, customInstructions)
	return err
}

// themeSettingsControllerAdapter adapts the theme controller to
// SettingsThemeController (error-returning SetThemeSetting).
type themeSettingsControllerAdapter struct{ *InteractiveThemeController }

func (a themeSettingsControllerAdapter) SetThemeSetting(theme string) error {
	a.InteractiveThemeController.SetThemeSetting(theme)
	return nil
}

// --- adapters ---------------------------------------------------------------

func appBoolPtr(value bool) *bool { return &value }

// themeUIAdapter adapts tui.TUI to ThemeControllerUI.
type themeUIAdapter struct{ ui tui.TUI }

func (t themeUIAdapter) Invalidate()    { t.ui.Invalidate() }
func (t themeUIAdapter) RequestRender() { t.ui.RequestRender(false) }
func (t themeUIAdapter) SetTerminalColorSchemeNotifications(enabled bool) {
	if renderer, ok := t.ui.(interface{ SetTerminalColorSchemeNotifications(bool) }); ok {
		renderer.SetTerminalColorSchemeNotifications(enabled)
	}
}
func (t themeUIAdapter) OnTerminalColorSchemeChange(listener func(theme TerminalTheme)) func() {
	if renderer, ok := t.ui.(interface {
		OnTerminalColorSchemeChange(func(tui.TerminalColorScheme)) func()
	}); ok {
		return renderer.OnTerminalColorSchemeChange(func(scheme tui.TerminalColorScheme) {
			listener(TerminalTheme(scheme))
		})
	}
	return func() {}
}

// OnTerminalBackgroundColorChange forwards OSC 11 replies.
func (t themeUIAdapter) OnTerminalBackgroundColorChange(listener func(tui.RgbColor)) func() {
	if renderer, ok := t.ui.(interface {
		OnTerminalBackgroundColorChange(func(tui.RgbColor)) func()
	}); ok {
		return renderer.OnTerminalBackgroundColorChange(listener)
	}
	return func() {}
}

// RequestTerminalBackgroundColor forwards the OSC 11 probe.
func (t themeUIAdapter) RequestTerminalBackgroundColor() {
	if renderer, ok := t.ui.(interface{ RequestTerminalBackgroundColor() }); ok {
		renderer.RequestTerminalBackgroundColor()
	}
}

// themeSettingsAdapter adapts *coding.SettingsManager to ThemeSettings.
type themeSettingsAdapter struct{ *coding.SettingsManager }

// Flush is a no-op: SetTheme persists immediately.
func (themeSettingsAdapter) Flush() {}

// editorHostAdapter adapts tui.TUI to tui.EditorHost.
type editorHostAdapter struct{ ui tui.TUI }

func (h editorHostAdapter) Rows() int                { return h.ui.GetTerminal().Rows() }
func (h editorHostAdapter) RequestRender(force bool) { h.ui.RequestRender(force) }
