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

	// The overlay stack and its focus-restore state machine live in
	// overlaystack.go; the renderer keeps the public overlay surface as
	// delegators and owns the painted overlay layouts.
	overlays *overlayStack
	// renderedOverlayLayouts is the paint-time layout of the visible overlays,
	// used for overlay hit-testing and GetBounds.
	renderedOverlayLayouts []renderedOverlayLayout
	inputListeners         []TuiInputListener
	// postMu guards the posted-callback queue: Post is called from off-loop
	// goroutines (loaders, watchers) and drained by the owner's render pass
	// (D146: queue serialization only — no UI state under the lock).
	postMu  sync.Mutex
	posted  []func()
	stopped atomic.Bool
	started atomic.Bool

	renderRequested bool
	fullRedrawCount int

	// terminal color queries: the OSC 11 probe, the color-scheme reports and the
	// `CSI ? 2031` toggle belong to the terminalQueries module; this renderer
	// forwards its public methods to it (port of the TuiBase query surface).
	queries terminalQueries

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
	renderer := &Renderer{
		Terminal:           terminal,
		KeyReleaseDetector: IsKeyRelease,
		clock:              time.Now,
		renderTicks:        make(chan struct{}, 1),
	}
	renderer.overlays = newOverlayStack(renderer)
	return renderer
}

// FullRedraws returns the number of full redraws performed.
func (t *Renderer) FullRedraws() int { return t.fullRedrawCount }

// GetFocusedComponent returns the component with keyboard focus.
func (t *Renderer) GetFocusedComponent() Component {
	return t.overlays.Focused()
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
	for _, overlay := range t.overlays.Entries() {
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
	t.queries.NotifyOnStart(t.Terminal)
	if t.OnAfterTerminalStart != nil {
		t.OnAfterTerminalStart()
	}
	t.Terminal.HideCursor()
	t.RequestRender(false)
}

// Stop stops the renderer and restores the terminal.
func (t *Renderer) Stop(options TuiStopOptions) {
	t.stopped.Store(true)
	t.started.Store(false)
	// Disables the `CSI ? 2031` notifications if they were on.
	t.queries.NotifyOnStop(t.Terminal)
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

// OnTerminalBackgroundColorChange subscribes to OSC 11 background-color replies
// (terminalqueries.go). The listener runs on the owner loop (input dispatch).
func (t *Renderer) OnTerminalBackgroundColorChange(listener func(RgbColor)) func() {
	return t.queries.OnBackgroundChange(listener)
}

// RequestTerminalBackgroundColor writes the OSC 11 query. It never blocks: the
// reply is delivered to the OnTerminalBackgroundColorChange listeners on the
// owner loop. Callers must invoke it only after the terminal is in raw mode and
// the input reader is live, so the reply is dispatched rather than echoed (D165).
func (t *Renderer) RequestTerminalBackgroundColor() {
	t.queries.RequestBackground(t.Terminal)
}

// OnTerminalColorSchemeChange subscribes to terminal color-scheme reports.
func (t *Renderer) OnTerminalColorSchemeChange(listener func(TerminalColorScheme)) func() {
	return t.queries.OnColorSchemeChange(listener)
}

// SetTerminalColorSchemeNotifications enables the `CSI ? 2031` notifications.
func (t *Renderer) SetTerminalColorSchemeNotifications(enabled bool) {
	t.queries.SetNotify(t.Terminal, enabled)
}

// QueryTerminalBackgroundColor queries the terminal's background color with
// OSC 11. It returns ok=false on timeout or an unparsable reply.
func (t *Renderer) QueryTerminalBackgroundColor(timeoutMS int) (RgbColor, bool) {
	return t.queries.QueryBackground(t.Terminal, timeoutMS)
}

// QueryTerminalColorScheme queries the terminal's color-scheme preference with
// DSR (`CSI ? 996 n`).
func (t *Renderer) QueryTerminalColorScheme(timeoutMS int) (TerminalColorScheme, bool) {
	return t.queries.QueryColorScheme(t.Terminal, timeoutMS)
}

// HandleTerminalInput routes input: listeners first, then the focused
// component. Keyboard input preempts the throttled render path.
func (t *Renderer) HandleTerminalInput(data string) {
	// Terminal query replies (an OSC 11 reply, a color-scheme report) are
	// consumed before any listener sees them: a reply is never user input.
	if t.queries.ConsumeInput(data) {
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

	// A focused overlay can stop being visible (a resize or a visible()
	// callback), and a pending restore can resume or be abandoned.
	t.overlays.ReconcileFocus()

	// Pass input to the focused component (including Ctrl+C); the component
	// decides how to handle it. No locks are held: input and paints share the
	// owner goroutine (D146).
	focused := t.overlays.Focused()
	handler, ok := focused.(InputHandler)
	if ok && focused != nil {
		if releaseDetector := t.KeyReleaseDetector; releaseDetector != nil && releaseDetector(data) {
			if wanter, ok := focused.(KeyReleaseWanter); !ok || !wanter.WantsKeyRelease() {
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
	t.overlays.SetFocus(component)
}

// overlayVisible implements overlayHost: the terminal-dependent half of overlay
// visibility (the entry's own Visible callback for the current terminal size).
func (t *Renderer) overlayVisible(entry *overlayEntry) bool {
	if entry.options == nil || entry.options.Visible == nil {
		return true
	}
	return entry.options.Visible(t.Terminal.Columns(), t.Terminal.Rows())
}

// componentMounted implements overlayHost: whether a component is still mounted.
func (t *Renderer) componentMounted(component Component) bool {
	return t.isComponentMounted(component)
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

// ---- Overlay stack ----

// ShowOverlay shows an overlay component with configurable positioning. The
// returned handle controls the overlay's visibility.
func (t *Renderer) ShowOverlay(component Component, options *OverlayOptions) OverlayHandle {
	entry := t.overlays.Push(component, options)
	nonCapturing := options != nil && options.NonCapturing
	if !nonCapturing && t.overlays.Visible(entry) {
		t.overlays.SetFocus(component)
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
	if !t.overlays.Hide(h.entry) {
		return
	}
	if !t.overlays.HasEntries() {
		t.Terminal.HideCursor()
	}
	t.requestRender(false)
}

func (h *overlayHandle) SetHidden(hidden bool) {
	t := h.renderer
	if !t.overlays.SetHidden(h.entry, hidden, h.nonCapturing) {
		return
	}
	t.requestRender(false)
}

func (h *overlayHandle) IsHidden() bool {
	return h.entry.hidden
}

func (h *overlayHandle) Focus() {
	t := h.renderer
	if !t.overlays.FocusEntry(h.entry) {
		return
	}
	t.requestRender(false)
}

func (h *overlayHandle) Unfocus(target Component, hasTarget bool) {
	t := h.renderer
	if !t.overlays.Unfocus(h.entry, target, hasTarget) {
		return
	}
	t.requestRender(false)
}

func (h *overlayHandle) IsFocused() bool {
	return h.renderer.overlays.Focused() == h.component
}

func (h *overlayHandle) GetBounds() (OverlayBounds, bool) {
	t := h.renderer
	if !t.overlays.Contains(h.entry) || !t.overlays.Visible(h.entry) || !h.entry.hasBounds {
		return OverlayBounds{}, false
	}
	return *h.entry.bounds, true
}

// HideOverlay hides the topmost overlay and restores previous focus.
func (t *Renderer) HideOverlay() {
	if t.overlays.HideTop() == nil {
		return
	}
	if !t.overlays.HasEntries() {
		t.Terminal.HideCursor()
	}
	t.requestRender(false)
}

// HasOverlay reports whether any overlay is visible.
func (t *Renderer) HasOverlay() bool {
	return t.overlays.HasVisible()
}

// HasOverlayEntries reports whether the overlay stack is non-empty.
func (t *Renderer) HasOverlayEntries() bool {
	return t.overlays.HasEntries()
}

// IsOverlayFocused reports whether the focused component is a visible overlay.
func (t *Renderer) IsOverlayFocused() bool {
	return t.overlays.IsFocused()
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
	for index := len(t.overlays.Entries()) - 1; index >= 0; index-- {
		overlay := t.overlays.Entries()[index]
		if t.overlays.Visible(overlay) && t.containsComponent(overlay.component, component) {
			return overlay.component
		}
	}
	return component
}

// CompositeOverlays composites all overlays into content lines (sorted by
// focusOrder, higher = on top).
func (t *Renderer) CompositeOverlays(lines []string, termWidth int, termHeight int) []string {
	if len(t.overlays.Entries()) == 0 {
		t.renderedOverlayLayouts = nil
		return lines
	}
	result := append([]string(nil), lines...)

	for _, entry := range t.overlays.Entries() {
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

	visibleEntries := make([]*overlayEntry, 0, len(t.overlays.Entries()))
	for _, entry := range t.overlays.Entries() {
		if t.overlays.Visible(entry) {
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
