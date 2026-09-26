package interactive

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of the run/init orchestration and the chat status/notification
// helpers of src/modes/interactive/interactive-mode.ts (init, run,
// clearEditor, showError, showWarning, showNewVersionNotification,
// showPackageUpdateNotification, getStartupExpansionState).
//
// Divergences: the collaborators are injected function values (D124); the
// extension/resource seams stay out of scope (D41).

// LatestRelease is the new-version notification payload. It carries a version
// and nothing else: the module proxy this port checks reports only that, while
// upstream's feed also carries release notes (D41).
type LatestRelease struct {
	Version string
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
	// Display is the shared display options (error padding, header expansion).
	Display *DisplayOptions
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
	// ShowLoadedResources renders the loaded-resource sections (skills).
	ShowLoadedResources func(force bool)
	// OnPartialEventApplied observes one applied partial-channel event
	// (test seam: partial events are otherwise superseded no-ops without a
	// streaming assistant component).
	OnPartialEventApplied func()
	// OnStarted runs once after the terminal is started (raw mode + input reader
	// live). Terminal probes that write a query belong here: a query written
	// before raw mode is echoed back into the input stream.
	OnStarted func()
	// StallLogPath, when non-empty, receives a record with a goroutine dump for
	// any UI-loop phase that exceeds StallLogThreshold. It exists to diagnose
	// stutters that do not reproduce elsewhere: the threshold defaults to 100ms
	// and PIER_STALL_MS overrides it (0 disables).
	StallLogPath      string
	StallLogThreshold time.Duration
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
	// CheckTmux returns the tmux warning ("" when fine).
	CheckTmux func() string
	// TakeCrash returns the unnotified crash record.
	TakeCrash func() *coding.CrashRecord
	// Prompt sends a prompt to the session.
	Prompt func(ctx context.Context, text string) error
	// Events applies session events. Only the run loop calls it.
	Events *EventDispatcher
	// SessionEvents carries the lossless session events and PartialEvents the
	// coalescable streaming updates (interactivemode_eventqueue.go). Both are
	// producer-written, loop-consumed channels.
	SessionEvents <-chan *coding.SessionEvent
	PartialEvents <-chan *coding.SessionEvent
	// InputEvents carries complete terminal sequences from the stdin reader and
	// ResizeEvents the resize ticks (stage 3). The loop dispatches both.
	InputEvents  <-chan string
	ResizeEvents <-chan struct{}
	// SignalEvents carries process signals (SIGTERM/SIGHUP) to the loop.
	SignalEvents <-chan os.Signal
	// OnSignal performs the signal work on the loop goroutine.
	OnSignal func(sig os.Signal)
	// StartWork schedules blocking work off the loop (the event dispatcher
	// uses it to run the compaction-queue flush). The loop installs it while
	// running; callers must be on the loop goroutine.
	StartWork func(fn func(context.Context) error)

	// schedule is the loop's scheduling state: the work queue, the animation
	// scan cache and the paint coalescing (interactivemode_schedule.go). runLoop
	// installs it and only the loop goroutine touches it; nil outside the loop.
	schedule *loopSchedule
	// beats counts loop iterations (the watchdog beat: a stalled loop stops
	// advancing it, so a watchdog can detect a hang).
	beats atomic.Uint64
	// OnBeat runs once per loop iteration (on the loop goroutine): the lazy
	// transcript materializer uses it to attach deferred chunks between
	// paints.
	OnBeat func()
	// RawInputs carries RAW stdin chunks from a D147 raw-input terminal;
	// the loop reassembles them through RawTerminal.FeedInput on the loop
	// goroutine. Nil for sequence-mode terminals (fakes, library use).
	RawInputs <-chan string
	// RawTerminal is the raw-input terminal (nil for sequence mode).
	RawTerminal tui.RawInputTerminal
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
	w.Chat.AddChild(tui.NewText(theme.Fg("error", "Error: "+message), w.Display.OutputPad, 0, nil))
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

// upgradeCommand is how this binary is upgraded, for the update card. Upstream
// says `<app> update`, which is its package manager; the port has neither a
// package manager nor an update command (D41), so the card names the install
// path this module actually uses.
const upgradeCommand = "go install github.com/dat267/pier@latest"

// ShowNewVersionNotification renders the update card.
func (w *RunWiring) ShowNewVersionNotification(release LatestRelease, hyperlinks bool) {
	if w.Chat == nil {
		return
	}
	theme := ActiveTheme()
	// Two lines, unlike upstream's one: the install path is longer than
	// "<app> update", and a card is truncated to the terminal width.
	action := theme.Fg("accent", upgradeCommand)
	updateInstruction := theme.Fg("muted", "New version "+release.Version+" is available.") + "\n" +
		theme.Fg("muted", "Upgrade with ") + action
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
	w.Chat.AddChild(tui.NewText(changelogLine, 1, 0, nil))
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
	return w.Verbose || w.Display.ToolOutputExpanded
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

	if w.OnStarted != nil {
		w.OnStarted()
	}

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
	if w.ShowLoadedResources != nil {
		w.ShowLoadedResources(false)
	}
	// Upstream sets isInitialized inside startup init, before events flow,
	// so the first-event fallback never re-runs init mid-session. Without
	// this, the dispatcher's Init (RenderInitialMessages) fired on the first
	// session event — the user's first message — re-rendering the whole
	// transcript without clearing and duplicating every entry on screen.
	if w.Events != nil {
		w.Events.Initialized = true
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
	go w.notifyNewVersion(runOptions.Hyperlinks)
	go w.notifyTmuxWarning()

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
	// modeldefault (D151): report the sync the way the extension notified on
	// session_start — informational when it switched, a warning when the
	// configured default could not be applied.
	if runOptions.ModelDefaultMessage != "" {
		if runOptions.ModelDefaultWarning {
			w.ShowChatWarning(runOptions.ModelDefaultMessage)
		} else {
			w.ShowStatus(runOptions.ModelDefaultMessage)
		}
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

	// Main loop.
	if w.Startup == nil || w.Prompt == nil {
		return
	}

	// Initial messages are ordinary prompts that run before the loop accepts
	// submissions (upstream awaits them ahead of the input loop), so they are
	// seeded as pending loop work rather than into the submission channel.
	initial := make([]string, 0, 1+len(runOptions.InitialMessages))
	if runOptions.InitialMessage != "" {
		initial = append(initial, runOptions.InitialMessage)
	}
	initial = append(initial, runOptions.InitialMessages...)

	w.runLoop(ctx, initial)
}

// renderTicks returns the active renderer's tick channel (nil when the
// renderer has its own timer, e.g. in tests that drive RenderNow directly).
func (w *RunWiring) renderTicks() <-chan struct{} {
	if w.UI == nil {
		return nil
	}
	return w.UI.RenderTicks()
}

// drainReadyEvents applies every session event that is already queued. The
// caller then paints once (loop-side coalescing).
func (w *RunWiring) drainReadyEvents() {
	defer w.phase("events")()
	for {
		select {
		case event, ok := <-w.SessionEvents:
			if !ok {
				w.SessionEvents = nil
				continue
			}
			if w.Events != nil {
				w.Events.HandleEvent(event)
			}
			continue
		default:
		}
		select {
		case event, ok := <-w.PartialEvents:
			if !ok {
				w.PartialEvents = nil
				continue
			}
			if w.Events != nil {
				w.Events.HandleEvent(event)
			}
			if w.OnPartialEventApplied != nil {
				w.OnPartialEventApplied()
			}
			continue
		default:
		}
		return
	}
}

// renderUI paints the current state (loop goroutine only). A paint is the only
// thing that can change a component's animation state, so it also invalidates
// the cached animation walk (D164): the next loop iteration re-walks and
// re-arms the animation timer.
func (w *RunWiring) renderUI() {
	defer w.phase("render")()
	if w.schedule != nil {
		w.schedule.invalidateScan()
	}
	if w.UI != nil {
		w.UI.RenderNow(false)
	}
}

// feedRawInput reassembles one raw stdin chunk on the loop goroutine and
// dispatches the complete sequences it carries.
func (w *RunWiring) feedRawInput(raw string) {
	if w.RawTerminal == nil || w.UI == nil {
		return
	}
	for _, sequence := range w.RawTerminal.FeedInput([]byte(raw)) {
		w.UI.HandleTerminalInput(sequence)
	}
}

// drainReadyRawInput feeds every raw chunk already queued before the caller
// paints, so a burst (mouse motion, a fast typist, a paste) is dispatched once
// and painted once rather than once per chunk — the same coalescing the event
// channels get from drainReadyEvents, and half of D164. The drain is bounded
// by the channel capacity, so a producer that never pauses cannot pin the loop
// here. Loop goroutine only.
func (w *RunWiring) drainReadyRawInput() {
	if w.RawTerminal == nil || w.UI == nil {
		return
	}
	for drained := 0; drained < loopInputCapacity; drained++ {
		select {
		case raw, ok := <-w.RawInputs:
			if !ok {
				w.RawInputs = nil
				return
			}
			w.feedRawInput(raw)
		default:
			return
		}
	}
}

// phase measures one UI-loop phase, recording it (with the goroutine stacks)
// when it exceeds the stall threshold. Usage: defer w.phase("render")().
// A phase still running at the threshold is recorded mid-flight by a timer
// goroutine, whose dump shows where the loop was — the after-phase record only
// shows the loop back in its event loop.
func (w *RunWiring) phase(name string) func() {
	if w.StallLogThreshold <= 0 || w.StallLogPath == "" {
		return func() {}
	}
	start := time.Now()
	watchdog := time.AfterFunc(w.StallLogThreshold, func() {
		w.writeStallRecord(name, time.Since(start), " still running")
	})
	return func() {
		watchdog.Stop()
		if elapsed := time.Since(start); elapsed >= w.StallLogThreshold {
			w.writeStallRecord(name, elapsed, "")
		}
	}
}

// stallLogMaxBytes bounds the stall log; an oversized file is truncated before
// the next record.
const stallLogMaxBytes = 4 << 20

// writeStallRecord appends the slow phase and every goroutine stack, so a stall
// names where the loop was instead of only how long it took. note (possibly
// empty) distinguishes a mid-flight capture from the after-phase one. The mutex
// serializes the UI goroutine's final record against the watchdog timer's
// mid-flight one.
var stallWriteMu sync.Mutex

func (w *RunWiring) writeStallRecord(name string, elapsed time.Duration, note string) {
	stallWriteMu.Lock()
	defer stallWriteMu.Unlock()
	flags := os.O_APPEND | os.O_CREATE | os.O_WRONLY
	if info, err := os.Stat(w.StallLogPath); err == nil && info.Size() > stallLogMaxBytes {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	file, err := os.OpenFile(w.StallLogPath, flags, 0o644)
	if err != nil {
		return
	}
	defer file.Close()
	fmt.Fprintf(file, "%s slow UI phase %q %v%s\n", time.Now().Format(time.RFC3339Nano), name, elapsed.Round(time.Millisecond), note)
	_ = pprof.Lookup("goroutine").WriteTo(file, 1)
}

// LoopBeats reports the loop's iteration count (watchdog beat).
func (w *RunWiring) LoopBeats() uint64 { return w.beats.Load() }

// RunWork schedules blocking work on the loop. It never blocks, and it may
// only be called from the loop goroutine (handlers dispatch work this way);
// external goroutines must not touch the loop-owned work state. It is a no-op
// when no loop is running.
func (w *RunWiring) RunWork(fn func(context.Context) error) {
	if w.schedule != nil {
		w.schedule.runWork(fn)
	}
}

// LoopContext returns the run loop's work context. Only the loop goroutine
// may call it (the context is loop-owned state).
func (w *RunWiring) LoopContext() context.Context {
	if w.schedule == nil {
		return nil
	}
	return w.schedule.workContext()
}

// runLoopHost adapts the wiring's renderer and raw terminal to the schedule's
// seam (loopHost). Every method tolerates a missing renderer or terminal.
type runLoopHost struct{ w *RunWiring }

func (h runLoopHost) NextAnimation() (bool, time.Duration) {
	if h.w.UI == nil {
		return false, 0
	}
	return h.w.UI.NextAnimation()
}

func (h runLoopHost) NextInputFlushDeadline() (time.Time, bool) {
	if h.w.RawTerminal == nil {
		return time.Time{}, false
	}
	return h.w.RawTerminal.NextInputFlushDeadline()
}

func (h runLoopHost) FlushPendingInput() {
	if h.w.RawTerminal == nil || h.w.UI == nil {
		return
	}
	for _, sequence := range h.w.RawTerminal.FlushPendingInput() {
		h.w.UI.HandleTerminalInput(sequence)
	}
}

func (h runLoopHost) RenderTicks() <-chan struct{} { return h.w.renderTicks() }

// runLoop is the UI's single writer. It applies session events and user input
// in arrival order and runs blocking work in a goroutine so a turn's events
// keep draining while it runs (upstream awaits the prompt and processes the
// event queue meanwhile; D-row: see AGENTS.md).
func (w *RunWiring) runLoop(ctx context.Context, initialWork []string) {
	inputs := w.Startup.Inputs()
	// The schedule owns the loop's timers and its work queue; the select below
	// is the only thing that waits on them (interactivemode_schedule.go). It
	// paints through w.renderUI, which is also what invalidates the animation
	// scan. Coalesced render requests paint at most once per frame interval, so
	// a fast event stream (streaming deltas) cannot saturate the loop with
	// back-to-back full repaints; resize and animation paints stay immediate.
	schedule := newLoopSchedule(runLoopHost{w}, w.renderUI, w.markInputRead)
	w.schedule = schedule
	schedule.setContext(ctx)
	w.StartWork = w.RunWork
	defer func() {
		schedule.close()
		schedule.setContext(nil)
		w.schedule = nil
		w.StartWork = nil
	}()
	var animationCh <-chan time.Time

	for _, text := range initialWork {
		text := text
		// The first starts, the rest queue behind it.
		schedule.runWork(func(context.Context) error {
			return w.Prompt(ctx, text)
		})
	}

	for {
		w.beats.Add(1)
		if w.OnBeat != nil {
			func() {
				defer w.phase("beat")()
				w.OnBeat()
			}()
		}
		var (
			inputsCh <-chan string
			doneCh   <-chan error
		)
		if !schedule.workActive() {
			inputsCh = inputs
		} else {
			doneCh = schedule.workDone()
		}
		animationCh = schedule.arm()

		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.SessionEvents:
			if !ok {
				w.SessionEvents = nil
				continue
			}
			if w.Events != nil {
				func() {
					defer w.phase("event-apply")()
					w.Events.HandleEvent(event)
				}()
			}
		case event, ok := <-w.PartialEvents:
			if !ok {
				w.PartialEvents = nil
				continue
			}
			if w.Events != nil {
				// Phased like event-apply: partial events stream in continuously,
				// and an unphased slow pass would freeze without any stall record
				// (the blind spot the first armed sessions exposed).
				func() {
					defer w.phase("partial-event-apply")()
					w.Events.HandleEvent(event)
				}()
			}
			if w.OnPartialEventApplied != nil {
				w.OnPartialEventApplied()
			}
		case raw, ok := <-w.RawInputs:
			if !ok {
				w.RawInputs = nil
				continue
			}
			// Raw input is latency-sensitive: reassemble, dispatch every
			// complete sequence already queued, then paint once — if the
			// dispatch asked for a paint at all (D164).
			func() {
				defer w.phase("raw-input")()
				w.feedRawInput(raw)
				w.drainReadyRawInput()
			}()
			w.drainReadyEvents()
			schedule.paintIfRequested()
		case data, ok := <-w.InputEvents:
			if !ok {
				w.InputEvents = nil
				continue
			}
			// Input is latency-sensitive: dispatch, then paint once if it asked
			// for one.
			if w.UI != nil {
				func() {
					defer w.phase("input")()
					w.UI.HandleTerminalInput(data)
				}()
			}
			w.drainReadyEvents()
			schedule.paintIfRequested()
		case _, ok := <-w.ResizeEvents:
			if !ok {
				w.ResizeEvents = nil
				continue
			}
			schedule.paintNow()
		case sig, ok := <-w.SignalEvents:
			if !ok {
				w.SignalEvents = nil
				continue
			}
			if w.OnSignal != nil {
				w.OnSignal(sig)
			}
		case <-animationCh:
			// A beat that woke for an expired input flush does that in arm();
			// here the animation deadline clears so the next iteration re-walks.
			schedule.animationFired()
			schedule.flushExpiredInput()
			schedule.paintNow()
		case <-w.renderTicks():
			// Coalesce: apply every event already queued, then paint once, so
			// a burst of N messages produces one render rather than N.
			w.drainReadyEvents()
			schedule.coalescePaint()
		case <-schedule.paintChannel():
			schedule.paintNow()
		case text := <-inputsCh:
			schedule.runWork(func(context.Context) error { return w.Prompt(ctx, text) })
		case err := <-doneCh:
			schedule.finishWork()
			if err != nil {
				w.ShowChatError(err.Error())
			}
		}
	}
}

// markInputRead tags the coming frame with when the terminal read the
// keystroke, so the writer can report the end-to-end latency (see
// inputLatencyRecorder). No-op without a raw terminal or instrumentation.
func (w *RunWiring) markInputRead() {
	if w.RawTerminal == nil {
		return
	}
	if readAt := w.RawTerminal.LastInputAt(); !readAt.IsZero() {
		w.RawTerminal.MarkInputRead(readAt)
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
	// ModelDefaultMessage is the modeldefault sync notice (D151).
	ModelDefaultMessage string
	ModelDefaultWarning bool
	InitialMessage      string
	InitialMessages     []string
}

// StartupDiagnostic is a pre-init diagnostic.
type StartupDiagnostic struct {
	Type    string // "error" | "warning" | other
	Message string
}

// newRunWiring assembles the RunWiring (port of the corresponding InteractiveMode wiring).
func newRunWiring(app *App) *RunWiring {
	installInputLatencyObserver(app)
	return &RunWiring{
		OnBeat: func() {
			if app.Transcript.HasDeferred() {
				app.Transcript.MaterializeDeferred(app.terminalWidth())
			}
			app.Queue.MaterializeThinkingChunk()
		},
		RawTerminal:     app.rawTerminal,
		RawInputs:       app.loopRawInputs,
		Startup:         app.Startup,
		Events:          app.Events,
		SessionEvents:   app.sessionEvents.Events(),
		PartialEvents:   app.sessionEvents.Partials(),
		InputEvents:     app.loopInputs,
		ResizeEvents:    app.loopResizes,
		SignalEvents:    app.loopSignals,
		OnSignal:        app.Lifecycle.HandleSignal,
		UI:              app.UI,
		Settings:        app.Settings,
		Terminal:        app.UI.GetTerminal(),
		HeaderContainer: app.HeaderContainer,
		Chat:            app.Chat,
		Display:         app.Display,
		Verbose:         app.options.Verbose,
		AppName:         app.options.AppName,
		Version:         app.options.Version,

		SetupKeyHandlers:      app.KeySetup,
		SetupSubmitHandler:    app.SubmitSetup,
		RenderInitialMessages: func() { app.Transcript.RenderInitialMessages() },
		ShowLoadedResources:   app.ShowLoadedResources,
		OnThemeChange: func(callback func()) func() {
			// The theme watcher fires on its own goroutine; deliver the change
			// on the UI loop (stage 4).
			OnThemeChange(func() {
				if app.UI != nil {
					app.UI.Post(callback)
					return
				}
				callback()
			})
			return func() {}
		},
		OnBranchChange: func(callback func()) func() { return app.FooterData.OnBranchChange(callback) },
		RefreshModelCatalogs: func(ctx context.Context) error {
			_, err := RefreshModelCatalogs(ctx, app.Runtime)
			return err
		},
		CheckVersion: checkForNewVersionNotification,
		CheckTmux:    func() string { return app.Startup.CheckTmuxKeyboardSetup(os.Getenv("TMUX") != "") },
		TakeCrash: func() *coding.CrashRecord {
			return coding.TakeUnnotifiedCrash(coding.GetCrashLogPath(app.options.AgentDir), time.Now().UnixMilli())
		},
		StallLogPath:      stallLogPath(app.options.AgentDir),
		StallLogThreshold: stallLogThreshold(),

		Prompt:      func(ctx context.Context, text string) error { return app.Session.Prompt(ctx, text, nil) },
		ShowStatus:  func(message string) { app.Transcript.ShowStatus(message) },
		ShowError:   app.RunnerShowChatError,
		ShowWarning: app.RunnerShowChatWarning,
		WarnAnthropic: func(ctx context.Context) {
			app.Startup.MaybeWarnAboutAnthropicSubscriptionAuth(ctx, app.Session.Model())
		},
		RequestRender: func() { app.UI.RequestRender(false) },
		OnStarted:     func() { app.Theme.ProbeTerminalBackground() },
	}
}

// notifyNewVersion runs the release check and reports the result on the UI loop.
func (w *RunWiring) notifyNewVersion(hyperlinks bool) {
	if w.CheckVersion == nil {
		return
	}
	release, ok := w.CheckVersion(w.Version)
	if !ok || release == nil {
		return
	}
	w.runOnUI(func() { w.ShowNewVersionNotification(*release, hyperlinks) })
}

// notifyTmuxWarning runs the tmux keyboard check and reports the result on the
// UI loop.
func (w *RunWiring) notifyTmuxWarning() {
	if w.CheckTmux == nil {
		return
	}
	if warning := w.CheckTmux(); warning != "" {
		w.runOnUI(func() { w.showWarning(warning) })
	}
}

// runOnUI marshals a mutation onto the UI loop: the checks above run on their
// own goroutines and every touch of the chat tree has to happen on the loop that
// renders it and routes input (upstream's checks run on its single-threaded
// event loop). A headless wiring has no loop, and runs the work inline.
func (w *RunWiring) runOnUI(fn func()) {
	if w.UI != nil {
		w.UI.Post(fn)
		return
	}
	fn()
}

// checkForNewVersionNotification adapts the release check to the wiring seam:
// only a strictly newer release of this module becomes a notification (the
// check compares semver and swallows its own errors). The request is bounded by
// the check's own timeout rather than the run context, which upstream's check
// does not take either, and it is skipped when offline.
func checkForNewVersionNotification(version string) (*LatestRelease, bool) {
	return versionNotification(coding.CheckForLatestPortRelease(context.Background(), version))
}

// versionNotification is the seam's contract: no release (offline, no newer
// version, or a failed check) means no card.
func versionNotification(release *coding.LatestRelease) (*LatestRelease, bool) {
	if release == nil {
		return nil, false
	}
	return &LatestRelease{Version: release.Version}, true
}

// stallLogThreshold defaults to 100ms so a freeze is captured even when the
// session was not started with PIER_STALL_MS — the first two freezes after the
// markdown fix struck sessions with the logger off, and both went undiagnosed.
// PIER_STALL_MS overrides it; PIER_STALL_MS=0 disables the log entirely.
func stallLogThreshold() time.Duration {
	value := os.Getenv("PIER_STALL_MS")
	if value == "" {
		return 100 * time.Millisecond
	}
	millis, err := strconv.Atoi(value)
	if err != nil || millis < 0 {
		return 100 * time.Millisecond
	}
	return time.Duration(millis) * time.Millisecond
}

// stallLogPath is where slow UI phases are recorded (next to the crash log).
func stallLogPath(agentDir string) string {
	if agentDir == "" {
		return ""
	}
	return filepath.Join(agentDir, "pier-stall.log")
}
