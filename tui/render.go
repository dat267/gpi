package tui

import (
	"reflect"
	"strings"
	"sync"
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
	// MatchesDebugKey matches the global debug key (Shift+Ctrl+D). Nil leaves
	// the debug key disabled; the keys.ts port supplies it.
	MatchesDebugKey func(data string) bool
	// KeyReleaseDetector reports Kitty key-release events (keys.go).
	KeyReleaseDetector func(data string) bool

	// LogDirectory, when set, enables debug/crash logs.
	LogDirectory string

	focusedComponent Component
	inputListeners   []TuiInputListener

	renderRequested          bool
	immediateRenderScheduled bool
	renderTimer              *time.Timer
	lastRenderAt             time.Time
	fullRedrawCount          int
	stopped                  bool

	overlayStack           []*overlayEntry
	renderedOverlayLayouts []renderedOverlayLayout
	focusOrderCounter      int
	overlayFocusRestore    overlayFocusRestoreState

	clock func() time.Time

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
func (t *Renderer) GetMountedRoots() []Component { return t.Children }

// Start starts the terminal and requests the first render.
func (t *Renderer) Start() {
	t.mu.Lock()
	t.stopped = false
	t.mu.Unlock()
	t.BeforeTerminalStart()
	t.Terminal.Start(func(data string) { t.HandleTerminalInput(data) }, func() { t.RequestRender(false) })
	t.AfterTerminalStart()
	t.Terminal.HideCursor()
	t.RequestRender(false)
}

// Stop stops the renderer and restores the terminal.
func (t *Renderer) Stop(options TuiStopOptions) {
	t.mu.Lock()
	t.stopped = true
	t.cancelRenderTimerLocked()
	t.mu.Unlock()
	t.BeforeTerminalStop(options)
	t.Terminal.ShowCursor()
	t.Terminal.Stop()
	t.AfterTerminalStop(options)
}

// Hooks for the concrete screens (upstream's protected lifecycle methods).
func (t *Renderer) BeforeTerminalStart()                {}
func (t *Renderer) AfterTerminalStart()                 {}
func (t *Renderer) BeforeTerminalStop(_ TuiStopOptions) {}
func (t *Renderer) AfterTerminalStop(_ TuiStopOptions)  {}
func (t *Renderer) ResetRenderState()                   {}

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

// RenderNow renders immediately.
func (t *Renderer) RenderNow(force bool) {
	t.mu.Lock()
	if force {
		t.ResetRenderState()
	}
	t.renderRequested = false
	t.cancelRenderTimerLocked()
	t.lastRenderAt = t.clock()
	t.mu.Unlock()
	t.doRender()
}

// RequestRender schedules a throttled render (or an immediate one when forced).
func (t *Renderer) RequestRender(force bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.requestRenderLocked(force)
}

// requestRenderLocked is RequestRender for callers that hold the lock.
func (t *Renderer) requestRenderLocked(force bool) {
	if force {
		t.ResetRenderState()
		t.requestImmediateRenderLocked()
		return
	}
	if t.renderRequested {
		return
	}
	t.renderRequested = true
	t.scheduleRenderLocked()
}

// requestImmediateRenderLocked schedules a next-tick render. Callers hold the
// lock.
func (t *Renderer) requestImmediateRenderLocked() {
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

func (t *Renderer) doRender() {
	if t.DoRender != nil {
		t.DoRender()
	}
}

// HandleTerminalInput routes input: listeners first, then the focused
// component. Keyboard input preempts the throttled render path.
func (t *Renderer) HandleTerminalInput(data string) {
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
		// The component handler runs without the lock (it may call back into
		// the renderer, e.g. requestRender or setFocus).
		t.mu.Unlock()
		handler.HandleInput(data)
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
	container, ok := root.(*Container)
	if !ok {
		return false
	}
	for _, child := range container.Children {
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
