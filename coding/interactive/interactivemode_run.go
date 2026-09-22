package interactive

import (
	"context"
	"fmt"
	"os"
	"strings"
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

	// work is the loop-owned work queue (see runLoop).
	work runnerWorkState
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

// renderUI paints the current state (loop goroutine only).
func (w *RunWiring) renderUI() {
	if w.UI != nil {
		w.UI.RenderNow(false)
	}
}

// armAnimation points the loop's timer at the next component animation frame
// and returns the channel to select on (nil when nothing animates). An already
// armed, equal-or-earlier deadline is left alone, so a busy event stream cannot
// starve the animation.
func (w *RunWiring) armAnimation(timer *time.Timer, deadline *time.Time) <-chan time.Time {
	// The input flush deadline (a lone ESC, an incomplete sequence, a split
	// keyboard-protocol response) shares the loop timer: waking for it is
	// handled in the fire path via flushExpiredInput.
	if w.RawTerminal != nil {
		if flushDeadline, ok := w.RawTerminal.NextInputFlushDeadline(); ok {
			if delay := time.Until(flushDeadline); delay <= 0 {
				// Already expired: flush on this beat, before painting.
				w.flushExpiredInput()
			} else if deadline.IsZero() || flushDeadline.Before(*deadline) {
				timer.Reset(delay)
				*deadline = flushDeadline
				return timer.C
			}
		}
	}
	if w.UI == nil {
		return nil
	}
	needs, delay := w.UI.NextAnimation()
	if !needs {
		if !deadline.IsZero() {
			timer.Stop()
			*deadline = time.Time{}
		}
		return nil
	}
	if delay <= 0 {
		delay = time.Millisecond
	}
	next := time.Now().Add(delay)
	if !deadline.IsZero() && !next.Before(*deadline) {
		return timer.C
	}
	timer.Reset(delay)
	*deadline = next
	return timer.C
}

// flushExpiredInput dispatches sequences whose input deadlines have expired
// (a lone ESC, an incomplete sequence, a split keyboard-protocol response).
func (w *RunWiring) flushExpiredInput() {
	if w.RawTerminal == nil || w.UI == nil {
		return
	}
	for _, sequence := range w.RawTerminal.FlushPendingInput() {
		w.UI.HandleTerminalInput(sequence)
	}
}

// LoopBeats reports the loop's iteration count (watchdog beat).
func (w *RunWiring) LoopBeats() uint64 { return w.beats.Load() }

// runnerWorkState is the loop-owned work bookkeeping: at most one blocking
// unit (a turn, a compaction-queue flush) runs at a time, with the rest
// queued. Nothing here is shared with other goroutines: only the loop
// goroutine mutates it.
type runnerWorkState struct {
	done    chan error
	ctx     context.Context
	active  bool
	pending []func(context.Context) error
}

// RunWork schedules blocking work on the loop. It never blocks, and it may
// only be called from the loop goroutine (handlers dispatch work this way);
// external goroutines must not touch the loop-owned work state.
func (w *RunWiring) RunWork(fn func(context.Context) error) {
	if fn == nil {
		return
	}
	if !w.work.active {
		w.startWork(fn)
		return
	}
	w.work.pending = append(w.work.pending, fn)
}

// LoopContext returns the run loop's work context. Only the loop goroutine
// may call it (the context is loop-owned state).
func (w *RunWiring) LoopContext() context.Context { return w.work.ctx }

// startWork launches fn in its own goroutine and records the completion
// channel. A panic is surfaced like a returned error instead of killing the
// process.
func (w *RunWiring) startWork(fn func(context.Context) error) {
	done := make(chan error, 1)
	w.work.done = done
	w.work.active = true
	workCtx := w.work.ctx
	if workCtx == nil {
		workCtx = context.Background()
	}
	go func() {
		var err error
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					err = fmt.Errorf("panic: %v", recovered)
				}
			}()
			err = fn(workCtx)
		}()
		done <- err
	}()
}

// runLoop is the UI's single writer. It applies session events and user input
// in arrival order and runs blocking work in a goroutine so a turn's events
// keep draining while it runs (upstream awaits the prompt and processes the
// event queue meanwhile; D-row: see AGENTS.md).
func (w *RunWiring) runLoop(ctx context.Context, initialWork []string) {
	inputs := w.Startup.Inputs()
	w.work.ctx = ctx
	w.StartWork = w.RunWork
	// One loop-owned timer drives component animation (loaders, flashes): the
	// renderer reports the next frame delay and the loop wakes to paint it.
	animationTimer := time.NewTimer(time.Hour)
	animationTimer.Stop()
	defer func() {
		animationTimer.Stop()
		w.StartWork = nil
		w.work.ctx = nil
	}()
	var (
		animationCh       <-chan time.Time
		animationDeadline time.Time
	)

	for _, text := range initialWork {
		text := text
		w.work.pending = append(w.work.pending, func(context.Context) error {
			return w.Prompt(ctx, text)
		})
	}
	if len(w.work.pending) > 0 {
		next := w.work.pending[0]
		w.work.pending = w.work.pending[1:]
		w.startWork(next)
	}

	for {
		w.beats.Add(1)
		if w.OnBeat != nil {
			w.OnBeat()
		}
		var (
			inputsCh <-chan string
			doneCh   chan error
		)
		if !w.work.active {
			inputsCh = inputs
		} else {
			doneCh = w.work.done
		}
		animationCh = w.armAnimation(animationTimer, &animationDeadline)

		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.SessionEvents:
			if !ok {
				w.SessionEvents = nil
				continue
			}
			if w.Events != nil {
				w.Events.HandleEvent(event)
			}
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
		case raw, ok := <-w.RawInputs:
			if !ok {
				w.RawInputs = nil
				continue
			}
			// Raw input is latency-sensitive: reassemble, dispatch every
			// complete sequence, then paint once.
			if w.RawTerminal != nil && w.UI != nil {
				for _, sequence := range w.RawTerminal.FeedInput([]byte(raw)) {
					w.UI.HandleTerminalInput(sequence)
				}
			}
			w.drainReadyEvents()
			w.renderUI()
		case data, ok := <-w.InputEvents:
			if !ok {
				w.InputEvents = nil
				continue
			}
			// Input is latency-sensitive: dispatch, then paint once.
			if w.UI != nil {
				w.UI.HandleTerminalInput(data)
			}
			w.drainReadyEvents()
			w.renderUI()
		case _, ok := <-w.ResizeEvents:
			if !ok {
				w.ResizeEvents = nil
				continue
			}
			w.renderUI()
		case sig, ok := <-w.SignalEvents:
			if !ok {
				w.SignalEvents = nil
				continue
			}
			if w.OnSignal != nil {
				w.OnSignal(sig)
			}
		case <-animationCh:
			animationDeadline = time.Time{}
			w.flushExpiredInput()
			w.renderUI()
		case <-w.renderTicks():
			// Coalesce: apply every event already queued, then paint once, so
			// a burst of N messages produces one render rather than N.
			w.drainReadyEvents()
			w.renderUI()
		case text := <-inputsCh:
			w.startWork(func(context.Context) error { return w.Prompt(ctx, text) })
		case err := <-doneCh:
			w.work.active = false
			w.work.done = nil
			if pending := w.work.pending; len(pending) > 0 {
				next := pending[0]
				w.work.pending = pending[1:]
				w.startWork(next)
			}
			if err != nil {
				w.ShowChatError(err.Error())
			}
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

// newRunWiring assembles the RunWiring (port of the corresponding InteractiveMode wiring).
func newRunWiring(app *App) *RunWiring {
	return &RunWiring{
		OnBeat:          func() { app.Transcript.MaterializeDeferred() },
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
		CheckTmux: func() string { return app.Startup.CheckTmuxKeyboardSetup(os.Getenv("TMUX") != "") },
		TakeCrash: func() *coding.CrashRecord {
			return coding.TakeUnnotifiedCrash(coding.GetCrashLogPath(app.options.AgentDir), time.Now().UnixMilli())
		},
		Prompt:      func(ctx context.Context, text string) error { return app.Session.Prompt(ctx, text, nil) },
		ShowStatus:  func(message string) { app.Transcript.ShowStatus(message) },
		ShowError:   app.RunnerShowChatError,
		ShowWarning: app.RunnerShowChatWarning,
		WarnAnthropic: func(ctx context.Context) {
			app.Startup.MaybeWarnAboutAnthropicSubscriptionAuth(ctx, app.Session.Model())
		},
		RequestRender: func() { app.UI.RequestRender(false) },
	}
}
