package tui

import (
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Port of the renderer base (src/tui.ts TuiBase): focus, the overlay stack,
// input routing, the differential-render scheduling, and the shared render
// pipeline helpers (overlay compositing, line resets, cursor extraction).
//
// Upstream's TuiBase declares an abstract doRender() implemented by
// TuiMainScreen/TuiAltScreen. Go has no abstract methods, so Renderer carries a
// DoRender hook that the concrete screen constructor sets (divergence D44).
// The renderer is single-goroutine driven (an event loop); a mutex guards the
// timer callbacks that arrive from other goroutines (divergence D45).

// Terminal is the terminal abstraction used by the renderer (see
// src/terminal.ts). Getters are methods in Go (divergence D46). The concrete
// ProcessTerminal lands with the terminal port.
type Terminal interface {
	// Start starts the terminal with input and resize handlers.
	Start(onInput func(data string), onResize func())
	// Stop restores the terminal state.
	Stop()
	// DrainInput drains stdin before exiting.
	DrainInput(maxMs int, idleMs int) error
	// Write writes output to the terminal.
	Write(data string)
	// Columns and Rows are the current terminal dimensions.
	Columns() int
	Rows() int
	// KittyProtocolActive reports whether the Kitty keyboard protocol is active.
	KittyProtocolActive() bool
	// MoveBy moves the cursor up (negative) or down (positive) by N lines.
	MoveBy(lines int)
	HideCursor()
	ShowCursor()
	ClearLine()
	ClearFromCursor()
	ClearScreen()
	SetTitle(title string)
	// SetProgress drives the OSC 9;4 progress indicator.
	SetProgress(active bool)
}

// TuiInputListenerResult is a listener's verdict on an input chunk.
type TuiInputListenerResult struct {
	Consume bool
	Data    string
	HasData bool
}

// TuiInputListener observes input before it reaches the focused component.
type TuiInputListener func(data string) TuiInputListenerResult

// TuiStopOptions configures Stop.
type TuiStopOptions struct {
	// PreserveScreen leaves renderer output in place for another TUI taking
	// over the same terminal.
	PreserveScreen bool
}

// MinRenderIntervalMS is the floor between throttled renders.
const MinRenderIntervalMS = 16

type overlayEntry struct {
	component  Component
	options    *OverlayOptions
	preFocus   Component
	hidden     bool
	focusOrder int
	bounds     *OverlayBounds
	hasBounds  bool
}

type renderedOverlayLayout struct {
	entry  *overlayEntry
	row    int
	col    int
	width  int
	height int
}

// OverlayHandle controls an overlay's visibility.
type OverlayHandle interface {
	// Hide permanently removes the overlay (it cannot be shown again).
	Hide()
	SetHidden(hidden bool)
	IsHidden() bool
	Focus()
	Unfocus(target Component, hasTarget bool)
	IsFocused() bool
	GetBounds() (OverlayBounds, bool)
}

// overlayFocusRestoreState models upstream's inactive|eligible|blocked union.
type overlayFocusRestoreState struct {
	status   string // "inactive" | "eligible" | "blocked"
	overlay  *overlayEntry
	blocked  Component
	resumeTo bool // true = restore-overlay, false = focus-target
	target   Component
}

// Renderer is the base for the terminal screens.
type Renderer struct {
	Container

	Terminal           Terminal
	OnDebug            func()
	ShowHardwareCursor bool
	ClearOnShrink      bool

	// DoRender is the screen-specific render implementation.
	DoRender func()
	// Lifecycle hooks (see the OnBeforeTerminalStart comment above).
	OnBeforeTerminalStart func()
	OnAfterTerminalStart  func()
	OnBeforeTerminalStop  func(options TuiStopOptions)
	OnAfterTerminalStop   func(options TuiStopOptions)
	OnResetRenderState    func()
	// MountedRoots overrides the roots used by mount checks and Invalidate
	// (upstream's getMountedRoots override).
	MountedRoots func() []Component
	// MatchesDebugKey matches the global debug key (Shift+Ctrl+D). Nil leaves
	// the debug key disabled; the keys.ts port supplies it.
	MatchesDebugKey func(data string) bool
	// KeyReleaseDetector reports Kitty key-release events (keys.go).
	KeyReleaseDetector func(data string) bool

	// LogDirectory, when set, enables debug/crash logs.
	LogDirectory string

	focusedComponent Component
	inputListeners   []TuiInputListener
	// postMu guards the posted-callback queue: Post is called from off-loop
	// goroutines (loaders, watchers) and drained by the owner's render pass
	// (D146: queue serialization only — no UI state under the lock).
	postMu  sync.Mutex
	posted  []func()
	stopped atomic.Bool
	started atomic.Bool

	renderRequested bool
	fullRedrawCount int

	// terminal color queries (port of the TuiBase query surface)
	pendingOSC11Replies  int
	pendingOSC11Queries  []*pendingOSC11Query
	colorSchemeListeners []colorSchemeListener
	colorSchemeNotifyOn  atomic.Bool
	nextColorSchemeID    int
	backgroundListeners  []backgroundListener
	nextBackgroundID     int

	overlayStack           []*overlayEntry
	renderedOverlayLayouts []renderedOverlayLayout
	focusOrderCounter      int
	overlayFocusRestore    overlayFocusRestoreState

	clock func() time.Time

	// loopInput/loopResize, when set, receive terminal input and resize
	// notifications instead of the renderer dispatching them inline: the owner
	// (the interactive UI loop) calls HandleTerminalInput and renders (stage 3).
	loopInput  func(string)
	loopResize func()

	// renderTicks receives render requests (capacity 1, so bursts coalesce):
	// the owner renders on its own goroutine (D146: there is no internal
	// render timer — input and paints share the owner goroutine).
	renderTicks chan struct{}
	// renderCount counts completed paints (test seam for coalescing).
	renderCount int64

	// mu guards the render scheduling, focus, overlay, and listener state.
	// Upstream is single-threaded (Node's event loop); the Go port drives the
	// renderer from one goroutine but timer callbacks arrive on others
	// (divergence D45).
}

// NewRenderer creates a renderer rooted at the given terminal.
func NewRenderer(terminal Terminal) *Renderer {
	return &Renderer{
		Terminal:            terminal,
		KeyReleaseDetector:  IsKeyRelease,
		clock:               time.Now,
		renderTicks:         make(chan struct{}, 1),
		overlayFocusRestore: overlayFocusRestoreState{status: "inactive"},
	}
}

// FullRedraws returns the number of full redraws performed.
func (t *Renderer) FullRedraws() int { return t.fullRedrawCount }

// GetFocusedComponent returns the component with keyboard focus.
func (t *Renderer) GetFocusedComponent() Component {
	return t.focusedComponent
}

// GetShowHardwareCursor reports whether the hardware cursor is enabled.
func (t *Renderer) GetShowHardwareCursor() bool {
	return t.ShowHardwareCursor
}

// SetShowHardwareCursor toggles the hardware cursor.
func (t *Renderer) SetShowHardwareCursor(enabled bool) {
	if t.ShowHardwareCursor == enabled {
		return
	}
	t.ShowHardwareCursor = enabled
	if !enabled {
		t.Terminal.HideCursor()
	}
	t.requestRender(false)
}

// GetClearOnShrink reports whether empty rows are cleared when content shrinks.
func (t *Renderer) GetClearOnShrink() bool {
	return t.ClearOnShrink
}

// SetClearOnShrink sets the shrink behaviour.
func (t *Renderer) SetClearOnShrink(enabled bool) {
	t.ClearOnShrink = enabled
}

// Invalidate invalidates all mounted roots and overlays.
func (t *Renderer) Invalidate() {
	for _, root := range t.GetMountedRoots() {
		root.Invalidate()
	}
	for _, overlay := range t.overlayStack {
		overlay.component.Invalidate()
	}
}

// GetMountedRoots returns the mounted root components. It is a plain field
// access; callers that race with rendering hold the renderer lock.
func (t *Renderer) GetMountedRoots() []Component {
	if t.MountedRoots != nil {
		return t.MountedRoots()
	}
	return t.Children
}

// NextAnimation reports whether any mounted component animates and how long
// until its next frame. Components own no timers: the owner renders and asks
// again (stage 4).
func (t *Renderer) NextAnimation() (bool, time.Duration) {
	now := t.clock()
	return nextAnimationFor(t.GetMountedRoots(), now)
}

// nextAnimationFor walks the component tree for the earliest animation frame.
func nextAnimationFor(components []Component, now time.Time) (bool, time.Duration) {
	var (
		needs bool
		best  time.Duration
	)
	var walk func(component Component)
	walk = func(component Component) {
		if component == nil {
			return
		}
		if animator, ok := component.(Animator); ok {
			if want, delay := animator.AnimationFrame(now); want {
				if delay <= 0 {
					delay = time.Millisecond
				}
				if !needs || delay < best {
					needs, best = true, delay
				}
			}
		}
		if holder, ok := component.(childrenHolder); ok {
			for _, child := range holder.childComponents() {
				walk(child)
			}
		}
	}
	for _, component := range components {
		walk(component)
	}
	return needs, best
}

// Start starts the terminal and requests the first render.
func (t *Renderer) Start() {
	t.stopped.Store(false)
	t.started.Store(true)
	loopInput, loopResize := t.loopInput, t.loopResize
	if t.OnBeforeTerminalStart != nil {
		t.OnBeforeTerminalStart()
	}
	if loopInput != nil {
		// Loop mode (stage 3): the owner dispatches input and renders.
		t.Terminal.Start(loopInput, loopResize)
	} else {
		t.Terminal.Start(func(data string) { t.HandleTerminalInput(data) }, func() { t.RequestRender(false) })
	}
	// A color-scheme notification toggle requested before Start only flips the
	// flag; replay it now, after the terminal is in raw mode, so the mode sequence
	// is not echoed into the input stream.
	if t.colorSchemeNotifyOn.Load() {
		t.Terminal.Write("\x1b[?2031h")
	}
	if t.OnAfterTerminalStart != nil {
		t.OnAfterTerminalStart()
	}
	t.Terminal.HideCursor()
	t.RequestRender(false)
}

// Stop stops the renderer and restores the terminal.
func (t *Renderer) Stop(options TuiStopOptions) {
	t.stopped.Store(true)
	started := t.started.Load()
	t.started.Store(false)
	if started && t.colorSchemeNotifyOn.Load() {
		t.colorSchemeNotifyOn.Store(false)
		t.Terminal.Write("\x1b[?2031l")
	}
	if false {
		t.Terminal.Write("[?2031l")
	}
	if t.OnBeforeTerminalStop != nil {
		t.OnBeforeTerminalStop(options)
	}
	t.Terminal.ShowCursor()
	t.Terminal.Stop()
	if t.OnAfterTerminalStop != nil {
		t.OnAfterTerminalStop(options)
	}
	// The post-stop hook can enqueue the alt-screen exit; flush it before
	// returning. A caller that then writes to stdout (the resume hint) is a
	// direct synchronous write, so it races the async writer otherwise and the
	// hint lands on the still-active alt screen, mangled into the last frame.
	if flusher, ok := t.Terminal.(interface{ FlushWrites() }); ok {
		flusher.FlushWrites()
	}
}

// Hooks for the concrete screens (upstream's protected lifecycle methods are
// overridable; Go uses function fields: D44).
//
// OnBeforeTerminalStart func()
// OnAfterTerminalStart  func()
// OnBeforeTerminalStop  func(options TuiStopOptions)
// OnAfterTerminalStop   func(options TuiStopOptions)
// OnResetRenderState    func()

// AddInputListener registers an input listener and returns a removal function.
func (t *Renderer) AddInputListener(listener TuiInputListener) func() {
	t.inputListeners = append(t.inputListeners, listener)
	return func() { t.RemoveInputListener(listener) }
}

// RemoveInputListener removes an input listener. Go function values are not
// comparable, so listeners are identified by their code pointer (upstream's
// Set.remove compares object identity: divergence D47).
func (t *Renderer) RemoveInputListener(listener TuiInputListener) {
	target := reflect.ValueOf(listener).Pointer()
	for i := range t.inputListeners {
		if reflect.ValueOf(t.inputListeners[i]).Pointer() == target {
			t.inputListeners = append(t.inputListeners[:i], t.inputListeners[i+1:]...)
			return
		}
	}
}

// EnableRenderTicks is retained for compatibility: every renderer is
// tick-driven since D146 removed the internal timer (the channel is created
// at construction, so requests never race its initialization).
func (t *Renderer) EnableRenderTicks() {}

// RenderTicks returns the render-request channel (nil until EnableRenderTicks).
func (t *Renderer) RenderTicks() <-chan struct{} {
	return t.renderTicks
}

// RenderCount reports completed paints (test seam).
func (t *Renderer) RenderCount() int64 { return atomic.LoadInt64(&t.renderCount) }

// signalRender coalesces a render request onto the tick channel.
func (t *Renderer) signalRender() {
	if t.renderTicks == nil {
		return
	}
	select {
	case t.renderTicks <- struct{}{}:
	default:
	}
}

// RenderNow paints immediately.
func (t *Renderer) RenderNow(force bool) {
	if force {
		t.resetRenderState()
	}
	t.renderRequested = false
	t.doRender()
}

// EnableLoopInput routes terminal input and resize notifications to the given
// sinks instead of dispatching them inline; the owner must call
// HandleTerminalInput and render. Call before Start (stage 3).
func (t *Renderer) EnableLoopInput(onInput func(string), onResize func()) {
	t.loopInput = onInput
	t.loopResize = onResize
}

// RequestRender requests a paint. With a tick channel the request coalesces
// onto it and the owner paints (D146: no internal timer, no throttling).
func (t *Renderer) RequestRender(force bool) {
	t.signalRender()
}

// DisableAutoRender is retained as a no-op for compatibility: rendering is
// always owner-driven (D146 removed the internal timer the seam used to
// disable; divergence D83).
func (t *Renderer) DisableAutoRender() {}

// requestRender is RequestRender for internal callers.
func (t *Renderer) requestRender(force bool) {
	t.RequestRender(force)
}

// requestImmediateRender requests a paint (owner-driven; same as
// RequestRender now that the internal timer is gone).
func (t *Renderer) requestImmediateRender() {
	t.RequestRender(false)
}

// resetRenderState invokes the screen's reset hook.
func (t *Renderer) resetRenderState() {
	if t.OnResetRenderState != nil {
		t.OnResetRenderState()
	}
}

func (t *Renderer) doRender() {
	t.drainPosted()
	if coalescer, ok := t.Terminal.(frameCoalescer); ok {
		coalescer.BeginFrame()
		defer coalescer.EndFrame()
	}
	if t.DoRender != nil {
		t.DoRender()
	}
	atomic.AddInt64(&t.renderCount, 1)
}

// frameCoalescer is implemented by terminals whose writes run off the caller
// (ProcessTerminal): the renderer brackets a paint so the paint's writes are
// submitted as one ordered batch. Frames are not dropped (the screens are
// differential; see ProcessTerminal.EndFrame).
type frameCoalescer interface {
	BeginFrame()
	EndFrame()
}

// Post schedules fn to run on the UI side: drained at the next render pass,
// serialized with renders and input handling by the owner goroutine. The
// queue itself takes postMu (Post is called from off-loop goroutines).
// Callbacks may call back into the renderer (RequestRender/SetFocus are
// fine) but must not call RenderNow or Stop.
func (t *Renderer) Post(fn func()) {
	t.postMu.Lock()
	t.posted = append(t.posted, fn)
	t.postMu.Unlock()
	t.requestRender(false)
}

// drainPosted runs queued callbacks. Looping
// covers callbacks that post more work.
func (t *Renderer) drainPosted() {
	for {
		t.postMu.Lock()
		pending := t.posted
		t.posted = nil
		t.postMu.Unlock()
		if len(pending) == 0 {
			return
		}
		for _, fn := range pending {
			fn()
		}
	}
}

// HandleTerminalInput routes input: listeners first, then the focused
// component. Keyboard input preempts the throttled render path.
// pendingOSC11Query is one in-flight OSC 11 query.
type pendingOSC11Query struct {
	settled bool
	result  chan osc11Result
}

type osc11Result struct {
	color RgbColor
	ok    bool
}

type colorSchemeListener struct {
	id       int
	listener func(TerminalColorScheme)
}

type backgroundListener struct {
	id       int
	listener func(RgbColor)
}

// OnTerminalBackgroundColorChange subscribes to OSC 11 background-color
// replies. The listener runs on the owner loop (input dispatch); registration
// happens during setup/teardown only, so the registry needs no lock.
func (t *Renderer) OnTerminalBackgroundColorChange(listener func(RgbColor)) func() {
	t.nextBackgroundID++
	id := t.nextBackgroundID
	t.backgroundListeners = append(t.backgroundListeners, backgroundListener{id: id, listener: listener})
	return func() {
		filtered := t.backgroundListeners[:0]
		for _, entry := range t.backgroundListeners {
			if entry.id != id {
				filtered = append(filtered, entry)
			}
		}
		t.backgroundListeners = filtered
	}
}

// RequestTerminalBackgroundColor writes the OSC 11 query. It never blocks: the
// reply is delivered to the OnTerminalBackgroundColorChange listeners on the
// owner loop. Callers must invoke it only after the terminal is in raw mode and
// the input reader is live, so the reply is dispatched rather than echoed (D165).
func (t *Renderer) RequestTerminalBackgroundColor() {
	if t.Terminal == nil || t.stopped.Load() {
		return
	}
	t.Terminal.Write("\x1b]11;?\x07")
}

// OnTerminalColorSchemeChange subscribes to terminal color-scheme reports.
func (t *Renderer) OnTerminalColorSchemeChange(listener func(TerminalColorScheme)) func() {
	t.nextColorSchemeID++
	id := t.nextColorSchemeID
	t.colorSchemeListeners = append(t.colorSchemeListeners, colorSchemeListener{id: id, listener: listener})
	return func() {
		filtered := t.colorSchemeListeners[:0]
		for _, entry := range t.colorSchemeListeners {
			if entry.id != id {
				filtered = append(filtered, entry)
			}
		}
		t.colorSchemeListeners = filtered
	}
}

// SetTerminalColorSchemeNotifications enables the `CSI ? 2031` notifications.
func (t *Renderer) SetTerminalColorSchemeNotifications(enabled bool) {
	if t.colorSchemeNotifyOn.Load() == enabled {
		return
	}
	t.colorSchemeNotifyOn.Store(enabled)
	// Before the terminal is started the request only flips the flag: Start
	// replays it once raw mode is active, so the sequence is never echoed into
	// the input stream (the SSH-launch freeze).
	if !t.started.Load() {
		return
	}
	stopped := t.stopped.Load()
	terminal := t.Terminal
	if !stopped && terminal != nil {
		if enabled {
			terminal.Write("[?2031h")
		} else {
			terminal.Write("[?2031l")
		}
	}
}

// QueryTerminalBackgroundColor queries the terminal's background color with
// OSC 11. It returns ok=false on timeout or an unparsable reply.
func (t *Renderer) QueryTerminalBackgroundColor(timeoutMS int) (RgbColor, bool) {
	query := &pendingOSC11Query{result: make(chan osc11Result, 1)}
	t.pendingOSC11Queries = append(t.pendingOSC11Queries, query)
	t.pendingOSC11Replies++
	terminal := t.Terminal

	if terminal == nil {
		return RgbColor{}, false
	}
	terminal.Write("]11;?")

	timer := time.NewTimer(time.Duration(timeoutMS) * time.Millisecond)
	defer timer.Stop()
	select {
	case result := <-query.result:
		return result.color, result.ok
	case <-timer.C:
		if !query.settled {
			query.settled = true
		}
		return RgbColor{}, false
	}
}

// QueryTerminalColorScheme queries the terminal's color-scheme preference with
// DSR (`CSI ? 996 n`).
func (t *Renderer) QueryTerminalColorScheme(timeoutMS int) (TerminalColorScheme, bool) {
	results := make(chan TerminalColorScheme, 1)
	unsubscribe := t.OnTerminalColorSchemeChange(func(scheme TerminalColorScheme) {
		select {
		case results <- scheme:
		default:
		}
	})
	defer unsubscribe()
	terminal := t.Terminal
	if terminal == nil {
		return "", false
	}
	terminal.Write("[?996n")

	timer := time.NewTimer(time.Duration(timeoutMS) * time.Millisecond)
	defer timer.Stop()
	select {
	case scheme := <-results:
		return scheme, true
	case <-timer.C:
		return "", false
	}
}

// consumeOSC11BackgroundResponse resolves the oldest pending OSC 11 query and
// notifies the background listeners. An OSC 11 reply is never user input, so it
// is consumed even when no query is pending; otherwise a proactive probe's reply
// would leak into the editor.
func (t *Renderer) consumeOSC11BackgroundResponse(data string) bool {
	if !IsOsc11BackgroundColorResponse(data) {
		return false
	}
	color, ok := ParseOsc11BackgroundColor(data)

	if t.pendingOSC11Replies > 0 {
		t.pendingOSC11Replies--
		var query *pendingOSC11Query
		if len(t.pendingOSC11Queries) > 0 {
			query = t.pendingOSC11Queries[0]
			t.pendingOSC11Queries = t.pendingOSC11Queries[1:]
		}
		if query != nil && !query.settled {
			query.settled = true
			select {
			case query.result <- osc11Result{color: color, ok: ok}:
			default:
			}
		}
	}

	if ok && len(t.backgroundListeners) > 0 {
		listeners := append([]backgroundListener{}, t.backgroundListeners...)
		for _, entry := range listeners {
			entry.listener(color)
		}
	}
	return true
}

// consumeTerminalColorSchemeReport notifies the color-scheme listeners.
func (t *Renderer) consumeTerminalColorSchemeReport(data string) bool {
	scheme, ok := ParseTerminalColorSchemeReport(data)
	if !ok {
		return false
	}
	listeners := append([]colorSchemeListener{}, t.colorSchemeListeners...)
	for _, entry := range listeners {
		entry.listener(scheme)
	}
	return true
}

func (t *Renderer) HandleTerminalInput(data string) {
	if t.consumeOSC11BackgroundResponse(data) {
		return
	}
	if t.consumeTerminalColorSchemeReport(data) {
		return
	}
	// Listeners are user code and may call back into the renderer. They run
	// from a snapshot; the listener registry itself only changes during setup
	// and teardown (loop goroutine).
	current := data
	for _, listener := range t.inputListeners {
		result := listener(current)
		if result.Consume {
			return
		}
		if result.HasData {
			current = result.Data
		}
	}
	data = current
	if len(data) == 0 {
		return
	}

	if t.MatchesDebugKey != nil && t.OnDebug != nil && t.MatchesDebugKey(data) {
		t.OnDebug()
		return
	}

	// If the focused component is an overlay, verify it is still visible
	// (visibility can change due to terminal resize or a visible() callback).
	focusedOverlay := t.findOverlay(t.focusedComponent)
	if focusedOverlay != nil && !t.isOverlayVisible(focusedOverlay) {
		if topVisible := t.getTopmostVisibleOverlay(); topVisible != nil {
			t.setFocusInternal(topVisible.component, "clear")
		} else {
			t.setFocusInternal(focusedOverlay.preFocus, "preserve")
		}
	}

	focusIsOverlay := t.isOverlayComponent(t.focusedComponent)
	if !focusIsOverlay {
		status, overlay, blockedBy, resumeTo, target := t.getVisibleOverlayFocusRestore()
		switch status {
		case "eligible":
			t.setFocusInternal(overlay.component, "clear")
		case "blocked":
			if blockedBy != t.focusedComponent {
				if resumeTo {
					t.setFocusInternal(overlay.component, "clear")
				} else {
					t.clearOverlayFocusRestore()
					t.setFocusInternal(target, "clear")
				}
			}
		}
	}

	// Pass input to the focused component (including Ctrl+C); the component
	// decides how to handle it. No locks are held: input and paints share the
	// owner goroutine (D146).
	handler, ok := t.focusedComponent.(InputHandler)
	if ok && t.focusedComponent != nil {
		if releaseDetector := t.KeyReleaseDetector; releaseDetector != nil && releaseDetector(data) {
			if wanter, ok := t.focusedComponent.(KeyReleaseWanter); !ok || !wanter.WantsKeyRelease() {
				return
			}
		}
		handler.HandleInput(data)
		// Keyboard input is latency-sensitive: the owner paints on the next
		// tick (coalesced).
		t.signalRender()
	}
}

// ---- Focus management ----

// SetFocus sets keyboard focus.
func (t *Renderer) SetFocus(component Component) {
	t.setFocusInternal(component, "clear")
}

func (t *Renderer) setFocusInternal(component Component, overlayFocusRestore string) {
	previousFocus := t.focusedComponent
	nextFocus := component

	previousFocusedOverlay := (*overlayEntry)(nil)
	if previousFocus != nil {
		if entry := t.findOverlay(previousFocus); entry != nil && t.isOverlayVisible(entry) {
			previousFocusedOverlay = entry
		}
	}
	nextFocusIsOverlay := nextFocus != nil && t.isOverlayComponent(nextFocus)
	restoreStatus, restoreOverlay, restoreBlockedBy, restoreResumeTo, restoreTarget := t.getVisibleOverlayFocusRestore()

	if nextFocus != nil && !nextFocusIsOverlay {
		if restoreStatus == "blocked" && restoreBlockedBy == previousFocus {
			if !restoreResumeTo || !t.isComponentMounted(restoreBlockedBy) {
				nextFocus = t.resolveBlockedOverlayFocusResume(restoreOverlay, restoreResumeTo, restoreTarget)
			} else {
				t.overlayFocusRestore = overlayFocusRestoreState{
					status: "blocked", overlay: restoreOverlay, blocked: nextFocus,
					resumeTo: restoreResumeTo, target: restoreTarget,
				}
			}
		} else if previousFocusedOverlay != nil && restoreStatus != "inactive" &&
			restoreOverlay == previousFocusedOverlay && !t.isOverlayFocusAncestor(previousFocusedOverlay, nextFocus) {
			t.overlayFocusRestore = overlayFocusRestoreState{
				status: "blocked", overlay: previousFocusedOverlay, blocked: nextFocus, resumeTo: true,
			}
		}
	} else if nextFocus == nil {
		if restoreStatus == "blocked" && restoreBlockedBy == previousFocus {
			nextFocus = t.resolveBlockedOverlayFocusResume(restoreOverlay, restoreResumeTo, restoreTarget)
		} else if overlayFocusRestore == "clear" {
			t.clearOverlayFocusRestore()
		}
	}

	if focusable, ok := t.focusedComponent.(Focusable); ok && t.focusedComponent != nil {
		focusable.SetFocused(false)
	}

	t.focusedComponent = nextFocus

	if focusable, ok := nextFocus.(Focusable); ok && nextFocus != nil {
		focusable.SetFocused(true)
	}

	if nextFocus != nil {
		if entry := t.findOverlay(nextFocus); entry != nil && t.isOverlayVisible(entry) {
			t.overlayFocusRestore = overlayFocusRestoreState{status: "eligible", overlay: entry}
		}
	}
}

func (t *Renderer) clearOverlayFocusRestore() {
	t.overlayFocusRestore = overlayFocusRestoreState{status: "inactive"}
}

func (t *Renderer) clearOverlayFocusRestoreFor(overlay *overlayEntry) {
	if t.overlayFocusRestore.status != "inactive" && t.overlayFocusRestore.overlay == overlay {
		t.clearOverlayFocusRestore()
	}
}

func (t *Renderer) resolveBlockedOverlayFocusResume(overlay *overlayEntry, resumeTo bool, target Component) Component {
	if overlay != nil && resumeTo {
		return overlay.component
	}
	t.clearOverlayFocusRestore()
	return target
}

func (t *Renderer) getVisibleOverlayFocusRestore() (string, *overlayEntry, Component, bool, Component) {
	state := t.overlayFocusRestore
	if state.status == "inactive" {
		return "inactive", nil, nil, false, nil
	}
	if !t.overlayInStack(state.overlay) || !t.isOverlayVisible(state.overlay) {
		return "inactive", nil, nil, false, nil
	}
	return state.status, state.overlay, state.blocked, state.resumeTo, state.target
}

func (t *Renderer) isOverlayFocusAncestor(entry *overlayEntry, component Component) bool {
	visited := map[Component]bool{}
	current := entry.preFocus
	for current != nil && !visited[current] {
		visited[current] = true
		if current == component {
			return true
		}
		if parent := t.findOverlay(current); parent != nil {
			current = parent.preFocus
		} else {
			current = nil
		}
	}
	return false
}

func (t *Renderer) retargetOverlayPreFocus(removed *overlayEntry) {
	for _, overlay := range t.overlayStack {
		if overlay != removed && overlay.preFocus == removed.component {
			overlay.preFocus = removed.preFocus
		}
	}
}

func (t *Renderer) isComponentMounted(component Component) bool {
	for _, child := range t.GetMountedRoots() {
		if t.containsComponent(child, component) {
			return true
		}
	}
	return false
}

func (t *Renderer) containsComponent(root Component, target Component) bool {
	if root == target {
		return true
	}
	// Upstream checks `root instanceof Container`; in Go any component that
	// exposes its children through the Container (or embeds one, like Stack)
	// is treated the same (divergence D55).
	holder, ok := root.(childrenHolder)
	if !ok {
		return false
	}
	for _, child := range holder.childComponents() {
		if t.containsComponent(child, target) {
			return true
		}
	}
	return false
}

func (t *Renderer) findOverlay(component Component) *overlayEntry {
	return t.findOverlayIn(t.overlayStack, component)
}

func (t *Renderer) findOverlayIn(stack []*overlayEntry, component Component) *overlayEntry {
	if component == nil {
		return nil
	}
	for _, entry := range stack {
		if entry.component == component {
			return entry
		}
	}
	return nil
}

func (t *Renderer) isOverlayComponent(component Component) bool {
	return t.findOverlay(component) != nil
}

// ---- Overlay stack ----

// ShowOverlay shows an overlay component with configurable positioning. The
// returned handle controls the overlay's visibility.
func (t *Renderer) ShowOverlay(component Component, options *OverlayOptions) OverlayHandle {
	t.focusOrderCounter++
	entry := &overlayEntry{
		component:  component,
		options:    options,
		preFocus:   t.focusedComponent,
		focusOrder: t.focusOrderCounter,
	}
	t.overlayStack = append(t.overlayStack, entry)

	nonCapturing := options != nil && options.NonCapturing
	if !nonCapturing && t.isOverlayVisible(entry) {
		t.setFocusInternal(component, "clear")
	}
	t.Terminal.HideCursor()
	t.requestRender(false)

	return &overlayHandle{renderer: t, entry: entry, component: component, nonCapturing: nonCapturing}
}

type overlayHandle struct {
	renderer     *Renderer
	entry        *overlayEntry
	component    Component
	nonCapturing bool
}

func (h *overlayHandle) Hide() {
	t := h.renderer
	index := t.overlayIndexOf(h.entry)
	if index == -1 {
		return
	}
	t.clearOverlayFocusRestoreFor(h.entry)
	t.retargetOverlayPreFocus(h.entry)
	t.overlayStack = append(t.overlayStack[:index], t.overlayStack[index+1:]...)
	if t.focusedComponent == h.component {
		if topVisible := t.getTopmostVisibleOverlay(); topVisible != nil {
			t.setFocusInternal(topVisible.component, "clear")
		} else {
			t.setFocusInternal(h.entry.preFocus, "clear")
		}
	}
	if len(t.overlayStack) == 0 {
		t.Terminal.HideCursor()
	}
	t.requestRender(false)
}

func (h *overlayHandle) SetHidden(hidden bool) {
	t := h.renderer
	if h.entry.hidden == hidden {
		return
	}
	h.entry.hidden = hidden
	if hidden {
		t.clearOverlayFocusRestoreFor(h.entry)
		if t.focusedComponent == h.component {
			if topVisible := t.getTopmostVisibleOverlay(); topVisible != nil {
				t.setFocusInternal(topVisible.component, "clear")
			} else {
				t.setFocusInternal(h.entry.preFocus, "clear")
			}
		}
	} else if !h.nonCapturing && t.isOverlayVisible(h.entry) {
		t.focusOrderCounter++
		h.entry.focusOrder = t.focusOrderCounter
		t.setFocusInternal(h.component, "clear")
	}
	t.requestRender(false)
}

func (h *overlayHandle) IsHidden() bool {
	return h.entry.hidden
}

func (h *overlayHandle) Focus() {
	t := h.renderer
	if t.overlayIndexOf(h.entry) == -1 || !t.isOverlayVisible(h.entry) {
		return
	}
	t.focusOrderCounter++
	h.entry.focusOrder = t.focusOrderCounter
	t.setFocusInternal(h.component, "clear")
	t.requestRender(false)
}

func (h *overlayHandle) Unfocus(target Component, hasTarget bool) {
	t := h.renderer
	isFocused := t.focusedComponent == h.component
	state := t.overlayFocusRestore
	hasPendingRestore := state.status != "inactive" && state.overlay == h.entry
	if !isFocused && !hasPendingRestore {
		return
	}
	if state.status == "blocked" && state.overlay == h.entry && t.focusedComponent == state.blocked {
		if hasTarget {
			t.overlayFocusRestore = overlayFocusRestoreState{
				status: "blocked", overlay: h.entry, blocked: state.blocked, target: target,
			}
		} else {
			t.clearOverlayFocusRestore()
		}
		t.requestRender(false)
		return
	}
	t.clearOverlayFocusRestoreFor(h.entry)
	if isFocused || hasTarget {
		topVisible := t.getTopmostVisibleOverlay()
		fallbackTarget := h.entry.preFocus
		if topVisible != nil && topVisible != h.entry {
			fallbackTarget = topVisible.component
		}
		if hasTarget {
			t.setFocusInternal(target, "clear")
		} else {
			t.setFocusInternal(fallbackTarget, "clear")
		}
	}
	t.requestRender(false)
}

func (h *overlayHandle) IsFocused() bool {
	return h.renderer.focusedComponent == h.component
}

func (h *overlayHandle) GetBounds() (OverlayBounds, bool) {
	t := h.renderer
	if t.overlayIndexOf(h.entry) == -1 || !t.isOverlayVisible(h.entry) || !h.entry.hasBounds {
		return OverlayBounds{}, false
	}
	return *h.entry.bounds, true
}

// HideOverlay hides the topmost overlay and restores previous focus.
func (t *Renderer) HideOverlay() {
	if len(t.overlayStack) == 0 {
		return
	}
	overlay := t.overlayStack[len(t.overlayStack)-1]
	t.clearOverlayFocusRestoreFor(overlay)
	t.retargetOverlayPreFocus(overlay)
	t.overlayStack = t.overlayStack[:len(t.overlayStack)-1]
	if t.focusedComponent == overlay.component {
		if topVisible := t.getTopmostVisibleOverlay(); topVisible != nil {
			t.setFocusInternal(topVisible.component, "clear")
		} else {
			t.setFocusInternal(overlay.preFocus, "clear")
		}
	}
	if len(t.overlayStack) == 0 {
		t.Terminal.HideCursor()
	}
	t.requestRender(false)
}

// HasOverlay reports whether any overlay is visible.
func (t *Renderer) HasOverlay() bool {
	for _, entry := range t.overlayStack {
		if t.isOverlayVisible(entry) {
			return true
		}
	}
	return false
}

// HasOverlayEntries reports whether the overlay stack is non-empty.
func (t *Renderer) HasOverlayEntries() bool {
	return len(t.overlayStack) > 0
}

// IsOverlayFocused reports whether the focused component is a visible overlay.
func (t *Renderer) IsOverlayFocused() bool {
	entry := t.findOverlay(t.focusedComponent)
	return entry != nil && t.isOverlayVisible(entry)
}

func (t *Renderer) overlayIndexOf(entry *overlayEntry) int {
	for i, existing := range t.overlayStack {
		if existing == entry {
			return i
		}
	}
	return -1
}

func (t *Renderer) overlayInStack(entry *overlayEntry) bool {
	return entry != nil && t.overlayIndexOf(entry) != -1
}

// isOverlayVisible reports whether an overlay entry is currently visible.
func (t *Renderer) isOverlayVisible(entry *overlayEntry) bool {
	if entry == nil || entry.hidden {
		return false
	}
	if entry.options != nil && entry.options.Visible != nil {
		return entry.options.Visible(t.Terminal.Columns(), t.Terminal.Rows())
	}
	return true
}

// getTopmostVisibleOverlay finds the visual-frontmost visible capturing
// overlay, if any.
func (t *Renderer) getTopmostVisibleOverlay() *overlayEntry {
	var topmost *overlayEntry
	for _, overlay := range t.overlayStack {
		if overlay.options != nil && overlay.options.NonCapturing {
			continue
		}
		if !t.isOverlayVisible(overlay) {
			continue
		}
		if topmost == nil || overlay.focusOrder > topmost.focusOrder {
			topmost = overlay
		}
	}
	return topmost
}

// DispatchMouseToOverlay dispatches to the visually topmost overlay under the
// pointer, reporting whether an overlay captured the point.
func (t *Renderer) DispatchMouseToOverlay(event TuiMouseEvent) (bool, *TuiMouseDispatchResult) {
	for index := len(t.renderedOverlayLayouts) - 1; index >= 0; index-- {
		layout := t.renderedOverlayLayouts[index]
		if event.ScreenX < layout.col || event.ScreenX >= layout.col+layout.width ||
			event.ScreenY < layout.row || event.ScreenY >= layout.row+layout.height {
			continue
		}
		childEvent := event
		childEvent.X = event.ScreenX - layout.col
		childEvent.Y = event.ScreenY - layout.row
		childEvent.Width = layout.width
		childEvent.Height = layout.height
		result := DispatchMouseEvent(layout.entry.component, childEvent)
		if result == nil {
			return true, nil
		}
		if result.Focus {
			result.FocusTarget = layout.entry.component
			result.HasFocus = true
		}
		return true, result
	}
	return false, nil
}

// ResolveMouseFocusTarget keeps overlay containers as keyboard focus owners
// when a nested control is clicked.
func (t *Renderer) ResolveMouseFocusTarget(component Component) Component {
	for index := len(t.overlayStack) - 1; index >= 0; index-- {
		overlay := t.overlayStack[index]
		if t.isOverlayVisible(overlay) && t.containsComponent(overlay.component, component) {
			return overlay.component
		}
	}
	return component
}

// CompositeOverlays composites all overlays into content lines (sorted by
// focusOrder, higher = on top).
func (t *Renderer) CompositeOverlays(lines []string, termWidth int, termHeight int) []string {
	if len(t.overlayStack) == 0 {
		t.renderedOverlayLayouts = nil
		return lines
	}
	result := append([]string(nil), lines...)

	for _, entry := range t.overlayStack {
		entry.hasBounds = false
	}

	type renderedOverlay struct {
		entry        *overlayEntry
		overlayLines []string
		row          int
		col          int
		w            int
	}
	var rendered []renderedOverlay
	minLinesNeeded := len(result)

	visibleEntries := make([]*overlayEntry, 0, len(t.overlayStack))
	for _, entry := range t.overlayStack {
		if t.isOverlayVisible(entry) {
			visibleEntries = append(visibleEntries, entry)
		}
	}
	sortOverlaysByFocusOrder(visibleEntries)
	for _, entry := range visibleEntries {
		component := entry.component
		options := entry.options

		// Resolve width and maxHeight with height=0 first (both do not depend
		// on the overlay height).
		layout := resolveOverlayLayout(options, 0, termWidth, termHeight)
		width := layout.Width

		overlayLines := component.Render(width)
		if layout.HasMax && len(overlayLines) > layout.MaxHeight {
			overlayLines = overlayLines[:layout.MaxHeight]
		}

		// Final position with the actual overlay height.
		final := resolveOverlayLayout(options, len(overlayLines), termWidth, termHeight)
		bounds := OverlayBounds{Row: final.Row, Col: final.Col, Width: width, Height: len(overlayLines)}
		entry.bounds = &bounds
		entry.hasBounds = true

		rendered = append(rendered, renderedOverlay{entry: entry, overlayLines: overlayLines, row: final.Row, col: final.Col, w: width})
		minLinesNeeded = max(minLinesNeeded, final.Row+len(overlayLines))
	}

	t.renderedOverlayLayouts = make([]renderedOverlayLayout, 0, len(rendered))
	for _, item := range rendered {
		t.renderedOverlayLayouts = append(t.renderedOverlayLayouts, renderedOverlayLayout{
			entry: item.entry, row: item.row, col: item.col, width: item.w, height: len(item.overlayLines),
		})
	}

	// Pad to at least the terminal height so overlays have screen-relative
	// positions.
	workingHeight := max(len(result), termHeight, minLinesNeeded)
	for len(result) < workingHeight {
		result = append(result, "")
	}

	viewportStart := max(0, workingHeight-termHeight)

	for _, item := range rendered {
		for i := 0; i < len(item.overlayLines); i++ {
			idx := viewportStart + item.row + i
			if idx < 0 || idx >= len(result) {
				continue
			}
			// Defensive: truncate the overlay line to its declared width before
			// compositing.
			overlayLine := item.overlayLines[i]
			if VisibleWidth(overlayLine) > item.w {
				overlayLine = SliceByColumn(overlayLine, 0, item.w, true)
			}
			result[idx] = CompositeTuiLine(result[idx], overlayLine, item.col, item.w, termWidth)
		}
	}

	return result
}

func sortOverlaysByFocusOrder(entries []*overlayEntry) {
	for i := 1; i < len(entries); i++ {
		for j := i; j > 0 && entries[j-1].focusOrder > entries[j].focusOrder; j-- {
			entries[j-1], entries[j] = entries[j], entries[j-1]
		}
	}
}

// ApplyLineResets appends the segment reset to every non-image line. In
// low-bandwidth mode a line with no hyperlink needs only the SGR reset, so the
// 7-byte OSC 8 close is dropped.
func (t *Renderer) ApplyLineResets(lines []string) []string {
	for i, line := range lines {
		if !IsImageLine(line) {
			reset := segmentReset
			if LowBandwidth() && !lineHasHyperlink(line) {
				reset = "\x1b[0m"
			}
			lines[i] = NormalizeTerminalOutput(line) + reset
		}
	}
	return lines
}

// lineHasHyperlink reports whether the line opens an OSC 8 hyperlink (a
// non-empty URL after the `ESC ] 8 ; ;` prefix). The close form uses an empty
// URL and does not need the trailing close appended by ApplyLineResets.
func lineHasHyperlink(line string) bool {
	const prefix = "\x1b]8;;"
	for {
		i := strings.Index(line, prefix)
		if i < 0 {
			return false
		}
		line = line[i+len(prefix):]
		if line != "" && line[0] != '\a' && line[0] != '\x1b' {
			return true
		}
	}
}

// ExtractCursorPosition finds and extracts the cursor position from rendered
// lines, stripping the marker. Only the bottom height lines are scanned.
func (t *Renderer) ExtractCursorPosition(lines []string, height int) (row int, col int, ok bool) {
	viewportTop := max(0, len(lines)-height)
	for r := len(lines) - 1; r >= viewportTop; r-- {
		line := lines[r]
		markerIndex := indexOf(line, CursorMarker)
		if markerIndex != -1 {
			beforeMarker := line[:markerIndex]
			col := VisibleWidth(beforeMarker)
			lines[r] = line[:markerIndex] + line[markerIndex+len(CursorMarker):]
			return r, col, true
		}
	}
	return 0, 0, false
}

// NormalizeTerminalOutput rewrites Thai/Lao AM vowels into their canonical
// decomposed forms (some terminals drop the precomposed cell) and expands tab
// characters to the fixed layout width, leaving tabs inside escape sequences
// untouched.
func NormalizeTerminalOutput(str string) string {
	normalized := str
	if strings.ContainsRune(normalized, '\u0e33') || strings.ContainsRune(normalized, '\u0eb3') {
		var builder strings.Builder
		for _, char := range normalized {
			switch char {
			case '\u0e33':
				builder.WriteString("\u0e4d\u0e32")
			case '\u0eb3':
				builder.WriteString("\u0ecd\u0eb2")
			default:
				builder.WriteRune(char)
			}
		}
		normalized = builder.String()
	}
	if !strings.ContainsRune(normalized, '\t') {
		return normalized
	}

	var result strings.Builder
	i := 0
	for i < len(normalized) {
		code, length := ExtractANSICode(normalized, i)
		if length > 0 {
			result.WriteString(code)
			i += length
			continue
		}
		if normalized[i] == '\t' {
			result.WriteString("   ")
		} else {
			result.WriteByte(normalized[i])
		}
		i++
	}
	return result.String()
}
