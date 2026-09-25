package interactive

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of the lifecycle half of src/modes/interactive/interactive-mode.ts
// (mountInteractiveTui, stopInteractiveTui, switchTuiMode, init, run,
// updateTerminalTitle, getUserInput, handleCtrlC/D/Z, shutdown,
// emergencyTerminalExit, uncaughtCrash, checkShutdownRequested,
// register/unregisterSignalHandlers).
//
// Divergences: the process-level effects (exit, signals, terminal title,
// stdout writes) are injected so the lifecycle is testable (D122); the
// extension/resource collaborators are seams (D41).

// LifecycleSession is the session surface the lifecycle needs.
type LifecycleSession interface {
	IsStreaming() bool
	IsCompacting() bool
	Prompt(ctx context.Context, text string, options *coding.PromptOptions) error
}

// LifecycleOptions configure the lifecycle.
type LifecycleOptions struct {
	UI       tui.TUI
	Session  LifecycleSession
	Settings *coding.SettingsManager
	Terminal tui.Terminal

	// TuiMode is the current mode ("regular" | "fullscreen").
	TuiMode string
	// LogDirectory is the renderer log directory.
	LogDirectory string
	// AppTitle is the terminal title prefix.
	AppTitle string
	// SessionCwd and SessionName feed the terminal title.
	SessionCwd  func() string
	SessionName func() string

	// CreateTui builds a renderer for a mode (test seam).
	CreateTui func(mode string) tui.TUI
	// Exit terminates the process (test seam).
	Exit func(code int)
	// WriteOut writes to stdout (resume command).
	WriteOut func(text string)
	// WriteErr writes to stderr (crash report).
	WriteErr func(text string)
	// RecordCrash records an uncaught crash.
	RecordCrash func(kind string, err error) bool
	// CrashReportInstructions returns the /bug hint.
	CrashReportInstructions func() string
	// KillDetachedChildren kills tracked detached child processes.
	KillDetachedChildren func()
	// DisableThemeAutoSync stops the theme auto-sync.
	DisableThemeAutoSync func()
	// OnTuiModeSwitched runs after SwitchTuiMode installs a new renderer (the
	// theme controller rebinds its terminal listeners against it).
	OnTuiModeSwitched func()
	// DisposeRuntime disposes the runtime host.
	DisposeRuntime func()
	// ResumeCommand builds the resume command ("" to skip).
	ResumeCommand func() string
	// StopMode tears the whole mode down (upstream's stop()); the argument is
	// the fullscreen exit output setting. Nil falls back to Stop.
	StopMode func(fullscreenExitOutput string)
	// FullscreenExitOutput reads the fullscreenExitOutput setting (upstream's
	// settingsManager.getFullscreenExitOutput()).
	FullscreenExitOutput func() string

	// Platform is "win32" on Windows (suspend support).
	Platform string
	// Now overrides the clock (test seam).
	Now func() time.Time
	// OnRightClickPaste is the renderer's right-click paste hook.
	OnRightClickPaste func()
	// FullscreenCopyOnSelect seeds the fullscreen renderer.
	FullscreenCopyOnSelect *bool

	// FormatResumeMessage renders the "To resume this session:" prefix.
	FormatResumeMessage func(command string) string

	// SignalSink, when set, receives process signals instead of the lifecycle
	// handling them inline: the owner (the UI loop) performs the shutdown work
	// (stage 3). Nil keeps the inline behaviour.
	SignalSink func(sig os.Signal)
	// RegisterSignal registers a signal handler (test seam).
	RegisterSignal func(sig os.Signal, handler func()) func()
	// OnTerminalError registers the stdout/stderr error handler (test seam).
	OnTerminalError func(handler func(error)) func()
	// OnUncaughtException registers the uncaught-exception handler (test seam).
	OnUncaughtException func(handler func(error)) func()
}

// Lifecycle holds the interactive-mode lifecycle state.
type Lifecycle struct {
	options LifecycleOptions

	// Cross-goroutine flags are atomics (stage 4): the emergency terminal-error
	// path must stay reachable from the terminal goroutine without a lock and
	// without depending on the UI loop. mainScreenState is loop-owned.
	initialized       atomic.Bool
	shuttingDown      atomic.Bool
	shutdownRequested atomic.Bool
	lastSigintTimeMS  atomic.Int64
	// signalCleanups is swapped atomically (registration is loop-side, the
	// emergency path may unregister from the terminal goroutine).
	signalCleanups  atomic.Pointer[[]func()]
	mainScreenState *tui.MainScreenRenderState
	startupSubmit   bool
	// LayoutRoot is the fullscreen layout root set by the mode.
	LayoutRoot tui.Component

	now func() time.Time
}

// NewLifecycle creates the lifecycle.
func NewLifecycle(options LifecycleOptions) *Lifecycle {
	if options.Exit == nil {
		options.Exit = os.Exit
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Platform == "" {
		options.Platform = "linux"
	}
	if options.LogDirectory == "" {
		options.LogDirectory = coding.DefaultAgentDir()
	}
	if options.CreateTui == nil {
		options.CreateTui = func(mode string) tui.TUI {
			return CreateInteractiveTui(InteractiveTuiOptions{
				TuiMode: mode, LogDirectory: options.LogDirectory,
				Terminal: options.Terminal, OnRightClickPaste: options.OnRightClickPaste,
				FullscreenCopyOnSelect: options.FullscreenCopyOnSelect,
			})
		}
	}
	return &Lifecycle{options: options, now: options.Now}
}

// Now is the clock seam.
func (l *Lifecycle) Now() func() time.Time { return l.now }

// IsInitialized reports the init state.
func (l *Lifecycle) IsInitialized() bool { return l.initialized.Load() }

// IsShuttingDown reports the shutdown state.
func (l *Lifecycle) IsShuttingDown() bool { return l.shuttingDown.Load() }

// RequestShutdown marks a pending shutdown (the agent_settled hook).
func (l *Lifecycle) RequestShutdown() { l.shutdownRequested.Store(true) }

// MarkInitialized records that init completed (upstream sets isInitialized at
// the end of init).
func (l *Lifecycle) MarkInitialized() { l.initialized.Store(true) }

// MountInteractiveTui mounts the shared component tree on a renderer.
func (l *Lifecycle) MountInteractiveTui(renderer tui.TUI, components []tui.Component, layoutRoot tui.Component) {
	for _, component := range components {
		renderer.AddChild(component)
	}
	if altScreen, ok := renderer.(*tui.AltScreen); ok && layoutRoot != nil {
		altScreen.SetLayoutRoot(layoutRoot)
	}
}

// StopInteractiveTui stops the renderer, replaying the transcript when leaving
// fullscreen mode with the transcript output.
func (l *Lifecycle) StopInteractiveTui(fullscreenExitOutput string) {
	if TuiMode(l.options.UI) == "fullscreen" && fullscreenExitOutput == "transcript" {
		for l.options.UI.HasOverlayEntries() {
			l.options.UI.HideOverlay()
		}
		l.SwitchTuiMode("regular", false, false)
		l.options.UI.RenderNow(false)
	}
	l.options.UI.Stop(tui.TuiStopOptions{PreserveScreen: TuiMode(l.options.UI) == "fullscreen"})
}

// SwitchTuiMode swaps the renderer. It returns false when an overlay blocks
// the switch.
func (l *Lifecycle) SwitchTuiMode(mode string, restoreProgress bool, startRenderer bool) bool {
	previousUI := l.options.UI
	if mode == TuiMode(previousUI) {
		return true
	}
	if previousUI.HasOverlayEntries() {
		return false
	}

	components := previousUI.GetMountedRoots()
	focus := previousUI.GetFocusedComponent()
	terminal := previousUI.GetTerminal()
	clearOnShrink := previousUI.GetClearOnShrink()
	if mainScreen, ok := previousUI.(*tui.MainScreen); ok {
		state := mainScreen.CaptureRenderState()
		l.mainScreenState = &state
	}

	previousUI.Stop(tui.TuiStopOptions{PreserveScreen: true})
	previousUI.SetFocus(nil)
	previousUI.Clear()
	if altScreen, ok := previousUI.(*tui.AltScreen); ok {
		altScreen.SetLayoutRoot(nil)
	}

	nextUI := l.options.CreateTui(mode)
	nextUI.SetShowHardwareCursor(previousUI.GetShowHardwareCursor())
	nextUI.SetClearOnShrink(clearOnShrink)
	if mainScreen, ok := nextUI.(*tui.MainScreen); ok && l.mainScreenState != nil {
		mainScreen.RestoreRenderState(*l.mainScreenState)
	}
	l.options.UI = nextUI
	l.options.TuiMode = mode
	if l.options.Terminal == nil {
		l.options.Terminal = terminal
	}
	l.MountInteractiveTui(nextUI, components, l.layoutRoot())
	nextUI.Invalidate()
	nextUI.SetFocus(focus)
	if l.options.OnTuiModeSwitched != nil {
		l.options.OnTuiModeSwitched()
	}
	if !startRenderer {
		return true
	}
	nextUI.Start()
	if restoreProgress && l.options.Settings != nil && l.options.Settings.GetShowTerminalProgress() &&
		(l.options.Session != nil && (l.options.Session.IsStreaming() || l.options.Session.IsCompacting())) {
		terminal.SetProgress(true)
	}
	return true
}

// CurrentUI returns the active renderer (the mode swaps it on /tui switches
// and on the exit replay).
func (l *Lifecycle) CurrentUI() tui.TUI { return l.options.UI }

// layoutRoot is set by the mode after building the chat viewport.
func (l *Lifecycle) layoutRoot() tui.Component { return l.LayoutRoot }

// UpdateTerminalTitle sets the terminal title from the session name and cwd.
func (l *Lifecycle) UpdateTerminalTitle() {
	if l.options.Terminal == nil {
		return
	}
	cwdBasename := ""
	if l.options.SessionCwd != nil {
		cwdBasename = filepath.Base(l.options.SessionCwd())
	}
	sessionName := ""
	if l.options.SessionName != nil {
		sessionName = l.options.SessionName()
	}
	if sessionName != "" {
		l.options.Terminal.SetTitle(l.options.AppTitle + " - " + sessionName + " - " + cwdBasename)
		return
	}
	l.options.Terminal.SetTitle(l.options.AppTitle + " - " + cwdBasename)
}

// HandleCtrlC clears the editor, or shuts down on a double press within 500ms.
func (l *Lifecycle) HandleCtrlC(clearEditor func()) {
	now := l.now().UnixMilli()
	if now-l.lastSigintTimeMS.Load() < 500 {
		l.Shutdown(false)
		return
	}
	if clearEditor != nil {
		clearEditor()
	}
	l.lastSigintTimeMS.Store(now)
}

// HandleCtrlD shuts down (only called with an empty editor).
func (l *Lifecycle) HandleCtrlD() { l.Shutdown(false) }

// HandleCtrlZ suspends the process (unsupported on Windows).
func (l *Lifecycle) HandleCtrlZ(showStatus func(string), suspend func()) {
	if l.options.Platform == "win32" {
		if showStatus != nil {
			showStatus("Suspend to background is not supported on Windows")
		}
		return
	}
	if l.options.UI != nil {
		l.options.UI.Stop(tui.TuiStopOptions{})
	}
	if suspend != nil {
		suspend()
	}
}

// Shutdown gracefully stops the mode.
func (l *Lifecycle) Shutdown(fromSignal bool) {
	if l.shuttingDown.Swap(true) {
		return
	}

	if fromSignal {
		// Emit the extension cleanup before touching the terminal.
		if l.options.DisposeRuntime != nil {
			l.options.DisposeRuntime()
		}
		if l.options.DisableThemeAutoSync != nil {
			l.options.DisableThemeAutoSync()
		}
		if l.options.Terminal != nil {
			_ = l.options.Terminal.DrainInput(1000, 0)
		}
		l.stopMode()
		l.options.Exit(0)
		return
	}

	if l.options.DisableThemeAutoSync != nil {
		l.options.DisableThemeAutoSync()
	}
	if l.options.Terminal != nil {
		_ = l.options.Terminal.DrainInput(1000, 0)
	}
	l.stopMode()
	if l.options.DisposeRuntime != nil {
		l.options.DisposeRuntime()
	}
	if l.options.ResumeCommand != nil {
		if command := l.options.ResumeCommand(); command != "" {
			message := command
			if l.options.FormatResumeMessage != nil {
				message = l.options.FormatResumeMessage(command)
			}
			if l.options.WriteOut != nil {
				l.options.WriteOut(message + "\n")
			}
		}
	}
	l.options.Exit(0)
}

// stopMode tears the mode down (upstream's stop(), which reads the
// fullscreenExitOutput setting). Falls back to the plain renderer stop when
// the mode wiring is absent.
func (l *Lifecycle) stopMode() {
	output := "transcript"
	if l.options.FullscreenExitOutput != nil {
		output = l.options.FullscreenExitOutput()
	}
	if l.options.StopMode != nil {
		l.options.StopMode(output)
		return
	}
	l.Stop()
}

// Stop stops the renderer.
func (l *Lifecycle) Stop() {
	if l.options.UI != nil {
		l.options.UI.Stop(tui.TuiStopOptions{})
	}
}

// EmergencyTerminalExit exits when the terminal is gone.
func (l *Lifecycle) EmergencyTerminalExit() {
	l.shuttingDown.Store(true)
	l.UnregisterSignalHandlers()
	if l.options.KillDetachedChildren != nil {
		l.options.KillDetachedChildren()
	}
	l.options.Exit(129)
}

// UncaughtCrash restores the terminal and exits after an uncaught exception.
func (l *Lifecycle) UncaughtCrash(err error) {
	if l.shuttingDown.Load() {
		l.options.Exit(1)
		return
	}
	l.shuttingDown.Store(true)
	l.UnregisterSignalHandlers()
	if l.options.KillDetachedChildren != nil {
		l.options.KillDetachedChildren()
	}
	if l.options.UI != nil {
		l.options.UI.Stop(tui.TuiStopOptions{})
	}
	if l.options.WriteErr != nil {
		l.options.WriteErr(coding.AppName + " exiting due to uncaughtException:\n")
		l.options.WriteErr(err.Error() + "\n")
	}
	if l.options.RecordCrash != nil && l.options.RecordCrash("uncaught_exception", err) {
		if l.options.CrashReportInstructions != nil && l.options.WriteErr != nil {
			l.options.WriteErr("\n" + l.options.CrashReportInstructions() + "\n")
		}
	}
	l.options.Exit(1)
}

// CheckShutdownRequested shuts down when a shutdown was requested.
func (l *Lifecycle) CheckShutdownRequested() {
	if !l.shutdownRequested.Load() {
		return
	}
	l.Shutdown(false)
}

// RegisterSignalHandlers installs the SIGTERM/SIGHUP and process-error hooks.
func (l *Lifecycle) RegisterSignalHandlers() {
	l.UnregisterSignalHandlers()
	signals := []os.Signal{syscall.SIGTERM}
	if l.options.Platform != "win32" {
		signals = append(signals, syscall.SIGHUP)
	}
	register := l.options.RegisterSignal
	if register == nil {
		register = func(sig os.Signal, handler func()) func() {
			channel := make(chan os.Signal, 1)
			signal.Notify(channel, sig)
			done := make(chan struct{})
			go func() {
				for {
					select {
					case <-done:
						return
					case <-channel:
						handler()
					}
				}
			}()
			return func() {
				signal.Stop(channel)
				close(done)
			}
		}
	}
	for _, sig := range signals {
		sig := sig
		cleanup := register(sig, func() {
			if sink := l.options.SignalSink; sink != nil {
				sink(sig)
				return
			}
			l.HandleSignal(sig)
		})
		l.addSignalCleanup(cleanup)
	}

	if l.options.OnTerminalError != nil {
		cleanup := l.options.OnTerminalError(func(err error) {
			if isDeadTerminalErrorText(err.Error()) {
				l.EmergencyTerminalExit()
			}
			panic(err)
		})
		l.addSignalCleanup(cleanup)
	}
	if l.options.OnUncaughtException != nil {
		cleanup := l.options.OnUncaughtException(func(err error) { l.UncaughtCrash(err) })
		l.addSignalCleanup(cleanup)
	}
}

// HandleSignal performs the shutdown work for a process signal. It touches UI
// state, so the UI loop calls it (stage 3).
func (l *Lifecycle) HandleSignal(sig os.Signal) {
	if l.options.KillDetachedChildren != nil {
		l.options.KillDetachedChildren()
	}
	l.Shutdown(true)
}

// addSignalCleanup appends to the cleanup registry (lock-free).
func (l *Lifecycle) addSignalCleanup(cleanup func()) {
	for {
		old := l.signalCleanups.Load()
		var next []func()
		if old != nil {
			next = append(next, (*old)...)
		}
		next = append(next, cleanup)
		if l.signalCleanups.CompareAndSwap(old, &next) {
			return
		}
	}
}

// UnregisterSignalHandlers removes the installed handlers.
func (l *Lifecycle) UnregisterSignalHandlers() {
	cleanups := l.signalCleanups.Swap(nil)
	if cleanups == nil {
		return
	}
	for _, cleanup := range *cleanups {
		cleanup()
	}
}

// SignalSink returns the installed signal sink (test helper).
func (l *Lifecycle) SignalSink() func(os.Signal) { return l.options.SignalSink }

// SignalHandlerCount returns the number of installed handlers (test helper).
func (l *Lifecycle) SignalHandlerCount() int {
	cleanups := l.signalCleanups.Load()
	if cleanups == nil {
		return 0
	}
	return len(*cleanups)
}

func isDeadTerminalErrorText(message string) bool {
	for _, code := range []string{"EIO", "EPIPE", "ENOTCONN"} {
		if strings.Contains(message, code) {
			return true
		}
	}
	return false
}
