package interactive

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"

	"github.com/dat267/pier/coding"
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

	// InitialThemeSetting seeds the theme controller.
	InitialThemeSetting *string
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
	// AutoTrustOnReloadCwd is the cwd /reload may implicitly trust
	// (main.ts:706): captured at startup when the project has no
	// trust-requiring resources, so a later reload can save trust for a
	// project that gained them mid-session. Upstream also gates on the
	// --trust CLI override, which the port does not have.
	AutoTrustOnReloadCwd string
	options              AppOptions

	// initialUI is the renderer created at composition time; the lifecycle
	// swaps it on /tui switches and the exit replay.
	initialUI tui.TUI

	UI          tui.TUI
	Theme       *InteractiveThemeController
	Settings    *coding.SettingsManager
	Session     *AppSession
	SessionMgr  *coding.SessionManager
	Runtime     *coding.ModelRuntime
	Keybindings *AppKeybindingsManager

	HeaderContainer          *tui.Container
	LoadedResourcesContainer *tui.Container
	DocumentContainer        *tui.Container
	Chat                     *tui.Container
	PendingMessages          *tui.Container
	StatusContainer          *tui.Container
	WidgetAbove              *tui.Container
	WidgetBelow              *tui.Container
	EditorContainer          *tui.Container
	FooterContainer          *tui.Container

	DefaultEditor *CustomEditor
	Footer        *FooterComponent
	FooterData    *coding.FooterDataProvider
	UIState       *InteractiveUIState
	Transcript    *TranscriptRenderer
	// TranscriptScrollView is the fullscreen transcript scroll view (upstream's
	// transcriptScrollView).
	TranscriptScrollView *tui.ScrollView
	Queue                *QueueController
	Events               *EventDispatcher
	Slot                 *SelectorSlot

	Lifecycle    *Lifecycle
	Startup      *StartupWiring
	Runner       *RunWiring
	Key          *KeyWiring
	Submit       *SubmitWiring
	Selectors    *SelectorWiring
	SettingsW    *SettingsWiring
	Models       *ModelWiring
	Sessions     *SessionWiring
	Auth         *AuthWiring
	Commands     *CommandWiring
	Trust        *TrustCrashWiring
	Autocomplete *AutocompleteWiring

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
		Settings:    options.Settings,
		SessionMgr:  options.SessionMgr,
		Runtime:     options.Runtime,
		Keybindings: keybindings,
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
	app.UI = tui.NewTuiReference(func() tui.TUI {
		if app.Lifecycle != nil {
			if current := app.Lifecycle.CurrentUI(); current != nil {
				return current
			}
		}
		return app.initialUI
	})
	app.UI.SetClearOnShrink(options.Settings.GetClearOnShrink())
	app.Theme = NewInteractiveThemeController(ThemeControllerOptions{
		UI:                  themeUIAdapter{ui: app.UI},
		GetSettingsManager:  func() ThemeSettings { return themeSettingsAdapter{options.Settings} },
		ShowError:           func(message string) { app.showError(message) },
		OnChanged:           func() { app.updateEditorBorderColor() },
		InitialThemeSetting: options.InitialThemeSetting,
	})

	// Containers.
	app.HeaderContainer = &tui.Container{}
	app.LoadedResourcesContainer = &tui.Container{}
	app.Chat = &tui.Container{}
	app.DocumentContainer = &tui.Container{}
	app.DocumentContainer.AddChild(app.HeaderContainer)
	app.DocumentContainer.AddChild(app.LoadedResourcesContainer)
	app.DocumentContainer.AddChild(app.Chat)
	app.PendingMessages = &tui.Container{}
	app.StatusContainer = &tui.Container{}
	app.WidgetAbove = &tui.Container{}
	app.WidgetBelow = &tui.Container{}
	app.EditorContainer = &tui.Container{}
	app.FooterContainer = &tui.Container{}

	// Editor.
	app.DefaultEditor = NewCustomEditor(editorHostAdapter{ui: app.UI}, GetEditorTheme(), keybindings, CustomEditorOptions{
		PaddingX:               options.Settings.GetEditorPaddingX(),
		AutocompleteMaxVisible: options.Settings.GetAutocompleteMaxVisible(),
		EmbedWorkingStatus:     true,
	})
	app.EditorContainer.AddChild(app.DefaultEditor)

	// Footer + data provider.
	cwd := options.Cwd
	if cwd == "" && options.SessionMgr != nil {
		cwd = options.SessionMgr.GetCwd()
	}
	app.FooterData = coding.NewFooterDataProvider(cwd, coding.FooterDataProviderOptions{})
	app.Session = &AppSession{AgentSession: options.Session}
	app.Footer = NewFooterComponent(app.Session, app.FooterData)
	app.Footer.SetAutoCompactEnabled(options.Session.AutoCompactionEnabled())
	app.FooterContainer.AddChild(app.Footer)

	// Trust/crash helpers (used by the event dispatcher and lifecycle).
	app.Trust = newTrustCrashWiring(app)

	// Upstream main.ts:706: capture at startup so a later /reload can save
	// an implicitly-trusted project whose cwd gained trust-requiring
	// resources during the session.
	app.AutoTrustOnReloadCwd = ""
	if app.SessionMgr != nil && !coding.HasTrustRequiringProjectResources(app.SessionMgr.GetCwd()) {
		app.AutoTrustOnReloadCwd = app.SessionMgr.GetCwd()
	}

	// UI state + transcript.
	app.UIState = NewInteractiveUIState(app.UI)
	app.UIState.FooterData = app.FooterData
	app.UIState.Footer = app.Footer
	app.UIState.StatusContainer = app.StatusContainer
	app.UIState.ChatContainer = app.Chat
	app.UIState.WidgetContainerAbove = app.WidgetAbove
	app.UIState.WidgetContainerBelow = app.WidgetBelow
	app.UIState.FooterContainer = app.FooterContainer
	app.UIState.HeaderContainer = app.HeaderContainer
	app.UIState.DefaultEditor = app.DefaultEditor
	app.UIState.Editor = app.DefaultEditor
	app.UIState.WorkingMessage = app.UIState.DefaultWorkingMessage
	app.UIState.ToolOutputExpanded = false

	app.Transcript = NewTranscriptRenderer(app.Chat, app.UI, app.Settings, app.Session, app.SessionMgr)
	app.Transcript.Footer = app.Footer
	app.Transcript.Editor = app.DefaultEditor
	app.Transcript.OutputPad = options.Settings.GetOutputPad()
	app.Transcript.HideThinkingBlock = options.Settings.GetHideThinkingBlock()
	app.Transcript.HiddenThinkingLabel = app.UIState.DefaultHiddenThinkingLabel
	app.Transcript.MarkdownTheme = app.markdownTheme()

	app.Queue = NewQueueController(app.UI, app.Session, app.Settings, app.DefaultEditor, app.Chat, app.PendingMessages)

	app.Events = NewEventDispatcher(app.Transcript, app.UIState, app.Footer, app.Settings, app.Session, app.SessionMgr, app.DefaultEditor)
	app.Events.ShowError = func(message string) { app.showError(message) }
	app.Events.UpdatePendingMessagesDisplay = app.Queue.UpdatePendingMessagesDisplay
	app.Events.HideThinkingBlock = options.Settings.GetHideThinkingBlock()
	app.Events.HiddenThinkingLabel = app.UIState.DefaultHiddenThinkingLabel
	app.Events.OutputPad = options.Settings.GetOutputPad()
	app.Events.MarkdownTheme = app.markdownTheme()
	app.Events.TerminalProgress = func(active bool) { terminal.SetProgress(active) }
	app.Events.FlushCompactionQueue = func(willRetry bool) {
		app.Queue.FlushCompactionQueue(context.Background(), willRetry)
	}
	app.Events.CheckShutdownRequested = app.LifecycleCheckShutdown
	app.Events.Init = func() { app.Transcript.RenderInitialMessages() }
	// The compaction-queue flush can start a turn; run it off-loop so events
	// keep draining while it runs.
	app.Events.StartWork = func(fn func(context.Context) error) {
		if app.Runner.StartWork != nil {
			app.Runner.StartWork(fn)
			return
		}
		fn(context.Background())
	}

	app.Slot = NewSelectorSlot(app.UI, app.EditorContainer, app.DefaultEditor)

	// Upstream initializes the widget containers with their default spacers
	// before mounting ("renderWidgets(); // Initialize with default spacer"):
	// the empty widgets-above container renders the blank line on top of the
	// divider above the input box.
	app.UIState.RenderWidgets()

	app.Lifecycle = NewLifecycle(LifecycleOptions{
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
		Session:      app.Session,
		Settings:     app.Settings,
		Terminal:     terminal,
		TuiMode:      options.TuiMode,
		LogDirectory: options.AgentDir,
		AppTitle:     options.AppName,
		SessionCwd:   func() string { return app.SessionMgr.GetCwd() },
		SessionName:  func() string { return app.SessionMgr.GetSessionName() },
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
		RecordCrash:             func(kind string, err error) bool { return app.Trust.RecordCrash(kind, err) },
		CrashReportInstructions: func() string { return app.Trust.CrashReportInstructions() }, // Upstream prints "To resume this session: pi --session …" after the
		// interactive shutdown (interactive-mode.ts shutdown(), chalk.dim
		// prefix).
		ResumeCommand: func() string {
			stdoutIsTTY := options.StdoutIsTTY
			if stdoutIsTTY == nil {
				detected := term.IsTerminal(int(os.Stdout.Fd()))
				stdoutIsTTY = &detected
			}
			return FormatResumeCommand(app.SessionMgr, options.AppName, *stdoutIsTTY)
		},
		FormatResumeMessage: func(command string) string {
			return "\x1b[2mTo resume this session:\x1b[22m " + command
		},
		FullscreenExitOutput: func() string { return options.Settings.GetFullscreenExitOutput() },
		StopMode:             func(output string) { app.StopMode(output) },
	})

	app.Startup = newStartupWiring(app)
	// The submission channel exists from composition so the run loop always
	// has a consumer side to select on.
	app.Startup.InitInputs()

	app.sessionEvents = newSessionEventQueue()

	app.Runner = newRunWiring(app)

	app.Selectors = newSelectorWiring(app)

	app.SettingsW = newSettingsWiring(app)

	app.Models = newModelWiring(app)

	app.Sessions = newSessionWiring(app)

	app.Auth = newAuthWiring(app)

	app.Commands = newCommandWiring(app)

	app.Key = newKeyWiring(app)

	app.Submit = newSubmitWiring(app)

	app.Autocomplete = newAutocompleteWiring(app)

	return app
}

// skillCommands converts the session's loaded skills into autocomplete slash
// commands (upstream builds `/skill:<name>` entries from the resource loader).
func (a *App) skillCommands() []SkillCommand {
	skills := a.Session.Skills()
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
	a.Lifecycle.RegisterSignalHandlers()
	a.Theme.ApplyFromSettings()
	// Build the shared fullscreen layout (scrollable transcript + fixed dock)
	// and mount it as the renderer's layout root (upstream init).
	theme := ActiveTheme()
	viewport := CreateChatViewport(ChatViewportOptions{
		Document:            a.DocumentContainer,
		PendingMessages:     a.PendingMessages,
		Status:              a.StatusContainer,
		WidgetsAbove:        a.WidgetAbove,
		Editor:              a.EditorContainer,
		WidgetsBelow:        a.WidgetBelow,
		Footer:              a.FooterContainer,
		Scrollbar:           tui.ScrollViewScrollbar(a.Settings.GetFullscreenScrollbar()),
		ScrollbarTrackStyle: func(text string) string { return theme.Fg("scrollbarTrack", text) },
		ScrollbarThumbStyle: func(text string) string { return theme.Fg("scrollbarThumb", text) },
	})
	a.TranscriptScrollView = viewport.Transcript
	a.Lifecycle.MountInteractiveTui(a.currentRenderer(), []tui.Component{
		a.DocumentContainer,
		a.PendingMessages,
		a.StatusContainer,
		a.WidgetAbove,
		a.EditorContainer,
		a.WidgetBelow,
		a.FooterContainer,
	}, viewport.Root)
	a.UI.SetFocus(a.DefaultEditor)
	a.initialized = true
	a.Lifecycle.MarkInitialized()

	if a.unsubscribe == nil {
		// Pure producer: the callback only enqueues; the run loop applies the
		// event on the UI goroutine (interactivemode_eventqueue.go).
		a.unsubscribe = a.Session.Subscribe(func(event *coding.SessionEvent) {
			a.sessionEvents.enqueue(event)
		})
	}
	a.Autocomplete.SetupAutocompleteProvider()
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
	if a.Startup != nil {
		a.Startup.SetContext(ctx)
	}
	a.Init(ctx)
	a.Runner.Run(ctx, InitOptions{
		ScopedModels:    a.Session.ScopedModels(),
		QuietStartup:    a.options.QuietStartup,
		RegisterSignals: func() { a.Lifecycle.RegisterSignalHandlers() },
		Mount:           func() {},
	}, RunOptions{
		Offline:         a.options.Offline,
		Hyperlinks:      a.options.Hyperlinks,
		InitialMessage:  a.options.InitialMessage,
		InitialMessages: a.options.InitialMessages,
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
	if a.Startup != nil {
		a.Startup.CloseInputs()
	}
	a.loopInputsOnce.Do(func() { close(a.loopInputsClosed) })
	a.Footer.Dispose()
	a.FooterData.Dispose()
	StopThemeWatcher()
}

// LifecycleCheckShutdown performs a requested shutdown.
func (a *App) LifecycleCheckShutdown() { a.Lifecycle.CheckShutdownRequested() }

// newLoopTui creates a renderer wired to the UI loop: terminal input and
// resize notifications are delivered as channel messages instead of being
// dispatched inline (stage 3). With a D147 raw-input terminal the handler
// receives RAW stdin chunks; the loop reassembles them through FeedInput on
// the loop goroutine.
func (a *App) newLoopTui(options InteractiveTuiOptions) tui.TUI {
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

// PostTerminalInput delivers a terminal sequence to the loop (test seam).
func (a *App) PostTerminalInput(data string) {
	select {
	case a.loopInputs <- data:
	case <-a.loopInputsClosed:
	}
}

// LoopBeats reports the run loop's watchdog beat.
func (a *App) LoopBeats() uint64 {
	if a.Runner == nil {
		return 0
	}
	return a.Runner.LoopBeats()
}

// LoopInputs/LoopResizes expose the producer channels to the run loop.
func (a *App) LoopInputs() <-chan string    { return a.loopInputs }
func (a *App) LoopResizes() <-chan struct{} { return a.loopResizes }

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
	target := a.UI.GetFocusedComponent()
	if target == nil {
		return
	}
	text, err := readClipboardText()
	if err != nil || text == "" {
		return
	}
	if a.UI.GetFocusedComponent() != target {
		return
	}
	handler, ok := target.(tui.InputHandler)
	if !ok {
		return
	}
	handler.HandleInput("\x1b[200~" + text + "\x1b[201~")
	a.UI.RequestRender(false)
}

func (a *App) runDetached(fn func(ctx context.Context) error) {
	ctx := context.Background()
	if a.Runner != nil {
		if loopCtx := a.Runner.LoopContext(); loopCtx != nil {
			ctx = loopCtx
		}
	}
	go func() { _ = fn(ctx) }()
}

// currentRenderer returns the concrete active renderer (upstream's
// this.renderer); app.UI is the forwarding reference.
func (a *App) currentRenderer() tui.TUI {
	if a.Lifecycle != nil {
		if current := a.Lifecycle.CurrentUI(); current != nil {
			return current
		}
	}
	return a.initialUI
}

// StopMode tears the whole mode down (upstream's stop()): the active selector,
// terminal progress, the status indicator, extension terminal input listeners,
// the footer and its data provider, the session-event subscription, the
// renderer (with the fullscreen exit output setting) and the signal handlers.
func (a *App) StopMode(fullscreenExitOutput string) {
	if a.Commands == nil {
		// Teardown before Init finished: stop the renderer only.
		a.Lifecycle.StopInteractiveTui(fullscreenExitOutput)
		return
	}
	a.Commands.Stop(
		fullscreenExitOutput,
		func() { a.Slot.DisposeActiveSelector() },
		func() { a.UIState.ClearExtensionTerminalInputListeners() },
		func() { a.Footer.Dispose() },
		func() { a.FooterData.Dispose() },
		func() {
			if a.unsubscribe != nil {
				a.unsubscribe()
			}
		},
		func(output string) {
			// Upstream only stops the TUI once init completed.
			if a.Lifecycle.IsInitialized() {
				a.Lifecycle.StopInteractiveTui(output)
			}
		},
		a.Lifecycle.UnregisterSignalHandlers,
	)
}

// RunnerShowChatError appends an error line.
func (a *App) RunnerShowChatError(message string) { a.Runner.ShowChatError(message) }

// RunnerShowChatWarning appends a warning line.
func (a *App) RunnerShowChatWarning(message string) { a.Runner.ShowChatWarning(message) }

// KeySetup enables the key handlers.
func (a *App) KeySetup() { a.Key.SetupKeyHandlers(func() int64 { return time.Now().UnixMilli() }) }

// SubmitSetup installs the editor submit handler.
func (a *App) SubmitSetup() {
	a.DefaultEditor.OnSubmit = func(text string) {
		if a.Lifecycle.IsInitialized() {
			a.Submit.HandleSubmit(context.Background(), text)
		} else {
			a.Submit.HandleStartupSubmit(text)
		}
	}
}

func (a *App) showError(message string) {
	if a.Runner != nil {
		a.Runner.ShowChatError(message)
		return
	}
	a.Transcript.ShowStatus(message)
}

func (a *App) showWarning(message string) {
	if a.Runner != nil {
		a.Runner.ShowChatWarning(message)
		return
	}
	a.Transcript.ShowStatus(message)
}

func (a *App) updateEditorBorderColor() {
	if a.Queue != nil {
		a.Queue.UpdateEditorBorderColor()
	}
}

// applyReloadedSettings re-applies settings-dependent state after /reload
// (upstream applyRuntimeSettings + restoreChatBeforeSessionStart +
// themeController.applyFromSettings + setupAutocompleteProvider). It runs on
// the UI loop. Terminal capability overrides and the HTTP dispatcher have no
// port counterpart (D41 scope).
func (a *App) applyReloadedSettings() {
	hidden := a.Settings.GetHideThinkingBlock()
	pad := a.Settings.GetOutputPad()
	a.updateThinkingBlockVisibility(hidden)
	a.Transcript.OutputPad = pad
	a.Events.OutputPad = pad
	// Upstream rebuildChatFromMessages (the reload's beforeSessionStart hook).
	if a.Startup != nil {
		a.Startup.RebuildChatFromMessages()
	}
	// Header expansion (upstream activeHeader.setExpanded).
	if a.UIState != nil {
		if expandable, ok := IsExpandable(a.UIState.BuiltInHeader); ok {
			expandable.SetExpanded(a.UIState.ToolOutputExpanded)
		}
	}
	// Reloaded resources (upstream showLoadedResources after /reload).
	a.ShowLoadedResources(false)
	if a.TranscriptScrollView != nil {
		a.TranscriptScrollView.SetScrollbar(tui.ScrollViewScrollbar(a.Settings.GetFullscreenScrollbar()))
	}
	if altscreen, ok := tuiConcrete(a.UI).(*tui.AltScreen); ok {
		altscreen.SetCopyOnSelect(a.Settings.GetFullscreenCopyOnSelect())
	}
	a.UI.SetShowHardwareCursor(a.Settings.GetShowHardwareCursor())
	clearOnShrink := a.Settings.GetClearOnShrink()
	a.UI.SetClearOnShrink(clearOnShrink)
	if !clearOnShrink && a.UIState != nil && a.UIState.ActiveStatusIndicator == nil && a.StatusContainer != nil {
		a.StatusContainer.Clear()
	}
	editorPad := a.Settings.GetEditorPaddingX()
	maxVisible := a.Settings.GetAutocompleteMaxVisible()
	a.DefaultEditor.SetPaddingX(editorPad)
	a.DefaultEditor.SetAutocompleteMaxVisible(maxVisible)
	// Custom theme files are re-read from disk by ApplyFromSettings.
	a.Theme.ApplyFromSettings()
	// Rebuild the autocomplete provider (upstream setupAutocompleteProvider);
	// the skills list is a func, so it re-reads on the next query.
	a.Autocomplete.SetupAutocompleteProvider()
}

func (a *App) updateThinkingBlockVisibility(hidden bool) {
	a.Transcript.HideThinkingBlock = hidden
	a.Events.HideThinkingBlock = hidden
}

func (a *App) markdownTheme() *tui.MarkdownTheme {
	theme := GetMarkdownTheme()
	if a.Startup != nil {
		theme = a.Startup.GetMarkdownThemeWithSettings(theme)
	}
	return &theme
}

// modelSession returns the ModelSession adapter (its ModelRuntime returns the
// selector-runtime interface).
func (a *App) modelSession() ModelSession { return appModelSession{a.Session} }

// commandSession returns the CommandSession adapter.
func (a *App) commandSession() CommandSession { return appCommandSession{a.Session} }

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

// themeSettingsAdapter adapts *coding.SettingsManager to ThemeSettings.
type themeSettingsAdapter struct{ *coding.SettingsManager }

// Flush is a no-op: SetTheme persists immediately.
func (themeSettingsAdapter) Flush() {}

// editorHostAdapter adapts tui.TUI to tui.EditorHost.
type editorHostAdapter struct{ ui tui.TUI }

func (h editorHostAdapter) Rows() int                { return h.ui.GetTerminal().Rows() }
func (h editorHostAdapter) RequestRender(force bool) { h.ui.RequestRender(force) }
