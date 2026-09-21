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
	posted           []func()

	renderRequested          bool
	autoRenderDisabled       bool
	immediateRenderScheduled bool
	renderTimer              *time.Timer
	lastRenderAt             time.Time
	fullRedrawCount          int
	stopped                  bool

	// terminal color queries (port of the TuiBase query surface)
	pendingOSC11Replies  int
	pendingOSC11Queries  []*pendingOSC11Query
	colorSchemeListeners []colorSchemeListener
	colorSchemeNotifyOn  bool
	nextColorSchemeID    int

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

	// renderMu serializes paints against focused-component input handling for
	// the renderer's own timer mode (the standalone session picker and library
	// users). In loop mode (renderTicks set) the owner goroutine both paints and
	// dispatches input, so the lock is never taken — see the lock inventory.
	renderMu sync.Mutex
	// renderTicks, when non-nil, replaces the render timer: requestRender
	// signals on it (capacity 1, so bursts coalesce) and the owner renders on
	// its own goroutine (stage 2 of the UI-loop refactor). A channel send,
	// rather than a callback, keeps user code out of the renderer lock.
	renderTicks chan struct{}
	// renderCount counts completed paints (test seam for coalescing).
	renderCount int64

	// mu guards the render scheduling, focus, overlay, and listener state.
	// Upstream is single-threaded (Node's event loop); the Go port drives the
	// renderer from one goroutine but timer callbacks arrive on others
	// (divergence D45).
	mu sync.Mutex
}

// NewRenderer creates a renderer rooted at the given terminal.
func NewRenderer(terminal Terminal) *Renderer {
	return &Renderer{
		Terminal:            terminal,
		KeyReleaseDetector:  IsKeyRelease,
		clock:               time.Now,
		overlayFocusRestore: overlayFocusRestoreState{status: "inactive"},
	}
}

// FullRedraws returns the number of full redraws performed.
func (t *Renderer) FullRedraws() int { t.mu.Lock(); defer t.mu.Unlock(); return t.fullRedrawCount }

// GetFocusedComponent returns the component with keyboard focus.
func (t *Renderer) GetFocusedComponent() Component {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.focusedComponent
}

// GetShowHardwareCursor reports whether the hardware cursor is enabled.
func (t *Renderer) GetShowHardwareCursor() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ShowHardwareCursor
}

// SetShowHardwareCursor toggles the hardware cursor.
func (t *Renderer) SetShowHardwareCursor(enabled bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ShowHardwareCursor == enabled {
		return
	}
	t.ShowHardwareCursor = enabled
	if !enabled {
		t.Terminal.HideCursor()
	}
	t.requestRenderLocked(false)
}

// GetClearOnShrink reports whether empty rows are cleared when content shrinks.
func (t *Renderer) GetClearOnShrink() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ClearOnShrink
}

// SetClearOnShrink sets the shrink behaviour.
func (t *Renderer) SetClearOnShrink(enabled bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ClearOnShrink = enabled
}

// Invalidate invalidates all mounted roots and overlays.
func (t *Renderer) Invalidate() {
	t.mu.Lock()
	defer t.mu.Unlock()
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

// Start starts the terminal and requests the first render.
func (t *Renderer) Start() {
	t.mu.Lock()
	t.stopped = false
	loopInput, loopResize := t.loopInput, t.loopResize
	t.mu.Unlock()
	if t.OnBeforeTerminalStart != nil {
		t.OnBeforeTerminalStart()
	}
	if loopInput != nil {
		// Loop mode (stage 3): the owner dispatches input and renders.
		t.Terminal.Start(loopInput, loopResize)
	} else {
		t.Terminal.Start(func(data string) { t.HandleTerminalInput(data) }, func() { t.RequestRender(false) })
	}
	if t.OnAfterTerminalStart != nil {
		t.OnAfterTerminalStart()
	}
	t.Terminal.HideCursor()
	t.RequestRender(false)
}

// Stop stops the renderer and restores the terminal.
func (t *Renderer) Stop(options TuiStopOptions) {
	t.mu.Lock()
	t.stopped = true
	t.cancelRenderTimerLocked()
	disableColorSchemeNotifications := t.colorSchemeNotifyOn
	t.colorSchemeNotifyOn = false
	t.mu.Unlock()
	if disableColorSchemeNotifications {
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
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inputListeners = append(t.inputListeners, listener)
	return func() { t.RemoveInputListener(listener) }
}

// RemoveInputListener removes an input listener. Go function values are not
// comparable, so listeners are identified by their code pointer (upstream's
// Set.remove compares object identity: divergence D47).
func (t *Renderer) RemoveInputListener(listener TuiInputListener) {
	t.mu.Lock()
	defer t.mu.Unlock()
	target := reflect.ValueOf(listener).Pointer()
	for i := range t.inputListeners {
		if reflect.ValueOf(t.inputListeners[i]).Pointer() == target {
			t.inputListeners = append(t.inputListeners[:i], t.inputListeners[i+1:]...)
			return
		}
	}
}

// EnableRenderTicks switches the renderer from its internal timer to the
// caller-driven tick channel: requestRender signals RenderTicks() instead of
// arming a timer, and the owner renders with RenderNow. Bursts coalesce in the
// capacity-1 channel.
func (t *Renderer) EnableRenderTicks() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.renderTicks == nil {
		t.renderTicks = make(chan struct{}, 1)
	}
}

// RenderTicks returns the render-request channel (nil until EnableRenderTicks).
func (t *Renderer) RenderTicks() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.renderTicks
}

// RenderCount reports completed paints (test seam).
func (t *Renderer) RenderCount() int64 { return atomic.LoadInt64(&t.renderCount) }

// signalRenderLocked coalesces a render request onto the tick channel.
// Callers hold the lock; a channel send is not user code, so it is safe here.
func (t *Renderer) signalRenderLocked() {
	if t.renderTicks == nil {
		return
	}
	select {
	case t.renderTicks <- struct{}{}:
	default:
	}
}

// RenderNow renders immediately.
func (t *Renderer) RenderNow(force bool) {
	t.mu.Lock()
	if force {
		t.resetRenderState()
	}
	t.renderRequested = false
	t.cancelRenderTimerLocked()
	t.lastRenderAt = t.clock()
	t.mu.Unlock()
	t.doRender()
}

// loopMode reports whether the renderer is driven by the owner's tick channel
// instead of its own timer (stage 2). Callers must hold t.mu.
func (t *Renderer) loopMode() bool { return t.renderTicks != nil }

// EnableLoopInput routes terminal input and resize notifications to the given
// sinks instead of dispatching them inline; the owner must call
// HandleTerminalInput and render. Call before Start (stage 3).
func (t *Renderer) EnableLoopInput(onInput func(string), onResize func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.loopInput = onInput
	t.loopResize = onResize
}

// RequestRender schedules a throttled render (or an immediate one when forced).
func (t *Renderer) RequestRender(force bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.requestRenderLocked(force)
}

// DisableAutoRender turns RequestRender into a no-op so tests can drive
// rendering deterministically with RenderNow (the Go renderer schedules real
// timers, unlike upstream's injectable seam: divergence D83).
func (t *Renderer) DisableAutoRender() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.autoRenderDisabled = true
}

// requestRenderLocked is RequestRender for callers that hold the lock.
func (t *Renderer) requestRenderLocked(force bool) {
	if t.autoRenderDisabled {
		return
	}
	if force {
		t.resetRenderState()
		t.requestImmediateRenderLocked()
		return
	}
	if t.renderTicks != nil {
		// Loop mode: coalesce onto the tick channel instead of arming a timer.
		t.renderRequested = true
		t.signalRenderLocked()
		return
	}
	if t.renderRequested {
		return
	}
	t.renderRequested = true
	t.scheduleRenderLocked()
}

// requestImmediateRenderLocked schedules a next-tick render. Callers hold the
// lock. Auto-render is disabled in tests (D83), including this path.
func (t *Renderer) requestImmediateRenderLocked() {
	if t.autoRenderDisabled {
		return
	}
	if t.renderTicks != nil {
		// Loop mode: input latency is the loop's business; just signal.
		t.renderRequested = true
		t.signalRenderLocked()
		return
	}
	t.cancelRenderTimerLocked()
	t.renderRequested = true
	if t.immediateRenderScheduled {
		return
	}
	t.immediateRenderScheduled = true
	// Upstream defers to process.nextTick; Go runs the callback on a goroutine.
	time.AfterFunc(0, func() {
		t.mu.Lock()
		t.immediateRenderScheduled = false
		if t.stopped || !t.renderRequested {
			t.mu.Unlock()
			return
		}
		// A previously queued scheduleRender can create a timer before this
		// callback runs. User input must preempt that throttled frame.
		t.cancelRenderTimerLocked()
		t.renderRequested = false
		t.lastRenderAt = t.clock()
		t.mu.Unlock()
		t.doRender()
	})
}

func (t *Renderer) cancelRenderTimerLocked() {
	if t.renderTimer == nil {
		return
	}
	t.renderTimer.Stop()
	t.renderTimer = nil
}

// scheduleRenderLocked arms the throttled render timer. Callers hold the lock.
func (t *Renderer) scheduleRenderLocked() {
	if t.stopped || t.renderTimer != nil || !t.renderRequested {
		return
	}
	elapsed := t.clock().Sub(t.lastRenderAt)
	delay := MinRenderIntervalMS*time.Millisecond - elapsed
	if delay < 0 {
		delay = 0
	}
	t.renderTimer = time.AfterFunc(delay, func() {
		t.mu.Lock()
		t.renderTimer = nil
		if t.stopped || !t.renderRequested {
			t.mu.Unlock()
			return
		}
		t.renderRequested = false
		t.lastRenderAt = t.clock()
		t.mu.Unlock()
		t.doRender()
		t.mu.Lock()
		if t.renderRequested {
			t.scheduleRenderLocked()
		}
		t.mu.Unlock()
	})
}

// resetRenderState invokes the screen's reset hook.
func (t *Renderer) resetRenderState() {
	if t.OnResetRenderState != nil {
		t.OnResetRenderState()
	}
}

func (t *Renderer) doRender() {
	if !t.loopMode() {
		t.renderMu.Lock()
		defer t.renderMu.Unlock()
	}
	t.drainPosted()
	if t.DoRender != nil {
		t.DoRender()
	}
	atomic.AddInt64(&t.renderCount, 1)
}

// Post schedules fn to run on the UI side: drained at the next render, under
// the render lock, so it is serialized with renders and input handling and
// never concurrent with a paint. Callbacks may call back into the renderer
// (RequestRender/SetFocus are fine) but must not call RenderNow or Stop.
func (t *Renderer) Post(fn func()) {
	t.mu.Lock()
	t.posted = append(t.posted, fn)
	t.mu.Unlock()
	t.RequestRender(false)
}

// drainPosted runs queued callbacks. Looping
// covers callbacks that post more work.
func (t *Renderer) drainPosted() {
	for {
		t.mu.Lock()
		pending := t.posted
		t.posted = nil
		t.mu.Unlock()
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

// OnTerminalColorSchemeChange subscribes to terminal color-scheme reports.
func (t *Renderer) OnTerminalColorSchemeChange(listener func(TerminalColorScheme)) func() {
	t.mu.Lock()
	t.nextColorSchemeID++
	id := t.nextColorSchemeID
	t.colorSchemeListeners = append(t.colorSchemeListeners, colorSchemeListener{id: id, listener: listener})
	t.mu.Unlock()
	return func() {
		t.mu.Lock()
		defer t.mu.Unlock()
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
	t.mu.Lock()
	if t.colorSchemeNotifyOn == enabled {
		t.mu.Unlock()
		return
	}
	t.colorSchemeNotifyOn = enabled
	stopped := t.stopped
	terminal := t.Terminal
	t.mu.Unlock()
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
	t.mu.Lock()
	t.pendingOSC11Queries = append(t.pendingOSC11Queries, query)
	t.pendingOSC11Replies++
	terminal := t.Terminal
	t.mu.Unlock()

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
		t.mu.Lock()
		if !query.settled {
			query.settled = true
		}
		t.mu.Unlock()
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
	t.mu.Lock()
	terminal := t.Terminal
	t.mu.Unlock()
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

// consumeOSC11BackgroundResponse resolves the oldest pending OSC 11 query.
func (t *Renderer) consumeOSC11BackgroundResponse(data string) bool {
	t.mu.Lock()
	if t.pendingOSC11Replies <= 0 {
		t.mu.Unlock()
		return false
	}
	t.mu.Unlock()
	if !IsOsc11BackgroundColorResponse(data) {
		return false
	}
	color, ok := ParseOsc11BackgroundColor(data)

	t.mu.Lock()
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
	t.mu.Unlock()
	return true
}

// consumeTerminalColorSchemeReport notifies the color-scheme listeners.
func (t *Renderer) consumeTerminalColorSchemeReport(data string) bool {
	scheme, ok := ParseTerminalColorSchemeReport(data)
	if !ok {
		return false
	}
	t.mu.Lock()
	listeners := append([]colorSchemeListener{}, t.colorSchemeListeners...)
	t.mu.Unlock()
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
	// Listeners are user code and may call back into the renderer; run them
	// from a snapshot without the lock.
	t.mu.Lock()
	listeners := append([]TuiInputListener(nil), t.inputListeners...)
	t.mu.Unlock()

	current := data
	for _, listener := range listeners {
		result := listener(current)
		if result.Consume {
			return
		}
		if result.HasData {
			current = result.Data
		}
	}
	if len(current) == 0 {
		return
	}
	data = current

	t.mu.Lock()
	if t.MatchesDebugKey != nil && t.OnDebug != nil && t.MatchesDebugKey(data) {
		onDebug := t.OnDebug
		t.mu.Unlock()
		onDebug()
		return
	}
	defer t.mu.Unlock()

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
	// decides how to handle it.
	handler, ok := t.focusedComponent.(InputHandler)
	if ok && t.focusedComponent != nil {
		if releaseDetector := t.KeyReleaseDetector; releaseDetector != nil && releaseDetector(data) {
			if wanter, ok := t.focusedComponent.(KeyReleaseWanter); !ok || !wanter.WantsKeyRelease() {
				return
			}
		}
		// The component handler runs without the renderer lock (it may call
		// back into the renderer, e.g. requestRender or setFocus). Input is
		// serialized against paints only in timer mode: in loop mode the owner
		// goroutine does both, so no render lock is involved (stage 3).
		loopMode := t.loopMode()
		t.mu.Unlock()
		if !loopMode {
			t.renderMu.Lock()
		}
		handler.HandleInput(data)
		if !loopMode {
			t.renderMu.Unlock()
		}
		t.mu.Lock()
		// Keyboard input is latency-sensitive: avoid the throttled path.
		t.requestImmediateRenderLocked()
	}
}

// ---- Focus management ----

// SetFocus sets keyboard focus.
func (t *Renderer) SetFocus(component Component) {
	t.mu.Lock()
	defer t.mu.Unlock()
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
	t.mu.Lock()
	defer t.mu.Unlock()
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
	t.requestRenderLocked(false)

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
	t.mu.Lock()
	defer t.mu.Unlock()
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
	t.requestRenderLocked(false)
}

func (h *overlayHandle) SetHidden(hidden bool) {
	t := h.renderer
	t.mu.Lock()
	defer t.mu.Unlock()
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
	t.requestRenderLocked(false)
}

func (h *overlayHandle) IsHidden() bool {
	h.renderer.mu.Lock()
	defer h.renderer.mu.Unlock()
	return h.entry.hidden
}

func (h *overlayHandle) Focus() {
	t := h.renderer
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.overlayIndexOf(h.entry) == -1 || !t.isOverlayVisible(h.entry) {
		return
	}
	t.focusOrderCounter++
	h.entry.focusOrder = t.focusOrderCounter
	t.setFocusInternal(h.component, "clear")
	t.requestRenderLocked(false)
}

func (h *overlayHandle) Unfocus(target Component, hasTarget bool) {
	t := h.renderer
	t.mu.Lock()
	defer t.mu.Unlock()
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
		t.requestRenderLocked(false)
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
	t.requestRenderLocked(false)
}

func (h *overlayHandle) IsFocused() bool {
	h.renderer.mu.Lock()
	defer h.renderer.mu.Unlock()
	return h.renderer.focusedComponent == h.component
}

func (h *overlayHandle) GetBounds() (OverlayBounds, bool) {
	t := h.renderer
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.overlayIndexOf(h.entry) == -1 || !t.isOverlayVisible(h.entry) || !h.entry.hasBounds {
		return OverlayBounds{}, false
	}
	return *h.entry.bounds, true
}

// HideOverlay hides the topmost overlay and restores previous focus.
func (t *Renderer) HideOverlay() {
	t.mu.Lock()
	defer t.mu.Unlock()
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
	t.requestRenderLocked(false)
}

// HasOverlay reports whether any overlay is visible.
func (t *Renderer) HasOverlay() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, entry := range t.overlayStack {
		if t.isOverlayVisible(entry) {
			return true
		}
	}
	return false
}

// HasOverlayEntries reports whether the overlay stack is non-empty.
func (t *Renderer) HasOverlayEntries() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.overlayStack) > 0
}

// IsOverlayFocused reports whether the focused component is a visible overlay.
func (t *Renderer) IsOverlayFocused() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
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
	t.mu.Lock()
	defer t.mu.Unlock()
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
	t.mu.Lock()
	defer t.mu.Unlock()
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
	t.mu.Lock()
	defer t.mu.Unlock()
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
		minLinesNeeded = maxInt(minLinesNeeded, final.Row+len(overlayLines))
	}

	t.renderedOverlayLayouts = make([]renderedOverlayLayout, 0, len(rendered))
	for _, item := range rendered {
		t.renderedOverlayLayouts = append(t.renderedOverlayLayouts, renderedOverlayLayout{
			entry: item.entry, row: item.row, col: item.col, width: item.w, height: len(item.overlayLines),
		})
	}

	// Pad to at least the terminal height so overlays have screen-relative
	// positions.
	workingHeight := maxInt(len(result), termHeight, minLinesNeeded)
	for len(result) < workingHeight {
		result = append(result, "")
	}

	viewportStart := maxInt(0, workingHeight-termHeight)

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

// ApplyLineResets appends the segment reset to every non-image line.
func (t *Renderer) ApplyLineResets(lines []string) []string {
	for i, line := range lines {
		if !IsImageLine(line) {
			lines[i] = NormalizeTerminalOutput(line) + segmentReset
		}
	}
	return lines
}

// ExtractCursorPosition finds and extracts the cursor position from rendered
// lines, stripping the marker. Only the bottom height lines are scanned.
func (t *Renderer) ExtractCursorPosition(lines []string, height int) (row int, col int, ok bool) {
	viewportTop := maxInt(0, len(lines)-height)
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
