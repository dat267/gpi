package tui

import (
	"time"
)

// Port of the renderer surface from src/tui.ts: the TUI interface shared by
// MainScreen and AltScreen plus the swappable reference.
//
// Divergences: upstream's TUI is a class interface with mutable properties
// (children, terminal, onDebug) and createInteractiveTuiReference returns an
// ES Proxy that forwards property reads/writes. Go has no proxies, so the
// reference forwards the method set and exposes the few field-backed values
// through accessors (D105).

// TUI is the renderer surface shared by MainScreen and AltScreen.
type TUI interface {
	Component

	FullRedraws() int
	GetFocusedComponent() Component
	GetShowHardwareCursor() bool
	SetShowHardwareCursor(enabled bool)
	GetClearOnShrink() bool
	SetClearOnShrink(enabled bool)
	GetMountedRoots() []Component
	GetTerminal() Terminal
	Start()
	Stop(options TuiStopOptions)
	// Post runs fn on the UI side: serialized with renders and input handling
	// (under the render lock), never concurrent with a paint. Callbacks may
	// call back into the renderer, but must not call RenderNow or Stop.
	Post(fn func())
	AddInputListener(listener TuiInputListener) func()
	RemoveInputListener(listener TuiInputListener)
	RenderNow(force bool)
	// RenderTicks delivers coalesced render requests when the renderer runs in
	// loop mode (EnableRenderTicks); nil otherwise.
	RenderTicks() <-chan struct{}
	// EnableRenderTicks switches the renderer to caller-driven rendering.
	EnableRenderTicks()
	// EnableLoopInput routes terminal input and resize to the owner.
	EnableLoopInput(onInput func(string), onResize func())
	// HandleTerminalInput dispatches one terminal sequence to the focused
	// component (loop-owned when loop input is enabled).
	HandleTerminalInput(data string)
	// RenderCount reports completed paints (test seam).
	RenderCount() int64
	// NextAnimation reports whether any mounted component animates and how long
	// until its next frame (the owner drives frames).
	NextAnimation() (bool, time.Duration)
	RequestRender(force bool)
	SetFocus(component Component)
	ShowOverlay(component Component, options *OverlayOptions) OverlayHandle
	HideOverlay()
	HasOverlay() bool
	HasOverlayEntries() bool
	AddChild(component Component)
	RemoveChild(component Component)
	Clear()
}

// GetTerminal returns the renderer's terminal.
func (t *Renderer) GetTerminal() Terminal { return t.Terminal }

// TuiReference is a stable handle that always forwards to the currently active
// renderer (upstream createInteractiveTuiReference).
type TuiReference struct {
	get func() TUI
}

// NewTuiReference creates a reference to the renderer returned by get.
func NewTuiReference(get func() TUI) *TuiReference {
	return &TuiReference{get: get}
}

// Render renders through the active renderer.
func (r *TuiReference) Render(width int) []string { return r.get().Render(width) }

// Invalidate invalidates through the active renderer.
func (r *TuiReference) Invalidate() { r.get().Invalidate() }

// FullRedraws returns the active renderer's full redraw count.
func (r *TuiReference) FullRedraws() int { return r.get().FullRedraws() }

// GetFocusedComponent returns the focused component.
func (r *TuiReference) GetFocusedComponent() Component { return r.get().GetFocusedComponent() }

// GetShowHardwareCursor reports the hardware-cursor state.
func (r *TuiReference) GetShowHardwareCursor() bool { return r.get().GetShowHardwareCursor() }

// SetShowHardwareCursor updates the hardware-cursor state.
func (r *TuiReference) SetShowHardwareCursor(enabled bool) { r.get().SetShowHardwareCursor(enabled) }

// GetClearOnShrink reports the clear-on-shrink state.
func (r *TuiReference) GetClearOnShrink() bool { return r.get().GetClearOnShrink() }

// SetClearOnShrink updates the clear-on-shrink state.
func (r *TuiReference) SetClearOnShrink(enabled bool) { r.get().SetClearOnShrink(enabled) }

// GetMountedRoots returns the mounted roots.
func (r *TuiReference) GetMountedRoots() []Component { return r.get().GetMountedRoots() }

// GetTerminal returns the active terminal.
func (r *TuiReference) GetTerminal() Terminal { return r.get().GetTerminal() }

// Start starts the active renderer.
func (r *TuiReference) Start() { r.get().Start() }

// Stop stops the active renderer.
func (r *TuiReference) Stop(options TuiStopOptions) { r.get().Stop(options) }

// AddInputListener registers an input listener.
// Post forwards to the active renderer's post queue.
func (r *TuiReference) Post(fn func()) { r.get().Post(fn) }

func (r *TuiReference) AddInputListener(listener TuiInputListener) func() {
	return r.get().AddInputListener(listener)
}

// RemoveInputListener removes an input listener.
func (r *TuiReference) RemoveInputListener(listener TuiInputListener) {
	r.get().RemoveInputListener(listener)
}

// RenderNow renders immediately.
func (r *TuiReference) RenderNow(force bool) { r.get().RenderNow(force) }

// RenderTicks forwards the active renderer's tick channel.
func (r *TuiReference) RenderTicks() <-chan struct{} { return r.get().RenderTicks() }

// EnableRenderTicks switches the active renderer to caller-driven rendering.
func (r *TuiReference) EnableRenderTicks() { r.get().EnableRenderTicks() }

// EnableLoopInput forwards loop input routing to the active renderer.
func (r *TuiReference) EnableLoopInput(onInput func(string), onResize func()) {
	r.get().EnableLoopInput(onInput, onResize)
}

// HandleTerminalInput forwards terminal input dispatch to the active renderer.
func (r *TuiReference) HandleTerminalInput(data string) { r.get().HandleTerminalInput(data) }

// SetTerminalColorSchemeNotifications forwards to the active renderer; the
// renderer's color-scheme/query surface is not part of the TUI interface, so
// the assertion is guarded.
func (r *TuiReference) SetTerminalColorSchemeNotifications(enabled bool) {
	if renderer, ok := r.get().(interface{ SetTerminalColorSchemeNotifications(bool) }); ok {
		renderer.SetTerminalColorSchemeNotifications(enabled)
	}
}

// OnTerminalColorSchemeChange forwards to the active renderer.
func (r *TuiReference) OnTerminalColorSchemeChange(listener func(TerminalColorScheme)) func() {
	if renderer, ok := r.get().(interface {
		OnTerminalColorSchemeChange(func(TerminalColorScheme)) func()
	}); ok {
		return renderer.OnTerminalColorSchemeChange(listener)
	}
	return func() {}
}

// OnTerminalBackgroundColorChange forwards to the active renderer.
func (r *TuiReference) OnTerminalBackgroundColorChange(listener func(RgbColor)) func() {
	if renderer, ok := r.get().(interface {
		OnTerminalBackgroundColorChange(func(RgbColor)) func()
	}); ok {
		return renderer.OnTerminalBackgroundColorChange(listener)
	}
	return func() {}
}

// RequestTerminalColorScheme forwards to the active renderer.
func (r *TuiReference) RequestTerminalColorScheme() {
	if renderer, ok := r.get().(interface{ RequestTerminalColorScheme() }); ok {
		renderer.RequestTerminalColorScheme()
	}
}

// RequestTerminalBackgroundColor forwards to the active renderer.
func (r *TuiReference) RequestTerminalBackgroundColor() {
	if renderer, ok := r.get().(interface{ RequestTerminalBackgroundColor() }); ok {
		renderer.RequestTerminalBackgroundColor()
	}
}

// QueryTerminalColorScheme forwards to the active renderer.
func (r *TuiReference) QueryTerminalColorScheme(timeoutMS int) (TerminalColorScheme, bool) {
	if renderer, ok := r.get().(interface {
		QueryTerminalColorScheme(int) (TerminalColorScheme, bool)
	}); ok {
		return renderer.QueryTerminalColorScheme(timeoutMS)
	}
	return "", false
}

// QueryTerminalBackgroundColor forwards to the active renderer.
func (r *TuiReference) QueryTerminalBackgroundColor(timeoutMS int) (RgbColor, bool) {
	if renderer, ok := r.get().(interface{ QueryTerminalBackgroundColor(int) (RgbColor, bool) }); ok {
		return renderer.QueryTerminalBackgroundColor(timeoutMS)
	}
	return RgbColor{}, false
}

// RenderCount forwards the active renderer's paint count.
func (r *TuiReference) RenderCount() int64 { return r.get().RenderCount() }

// NextAnimation forwards the active renderer's animation query.
func (r *TuiReference) NextAnimation() (bool, time.Duration) { return r.get().NextAnimation() }

// RequestRender requests a render.
func (r *TuiReference) RequestRender(force bool) { r.get().RequestRender(force) }

// SetFocus sets the focused component.
func (r *TuiReference) SetFocus(component Component) { r.get().SetFocus(component) }

// ShowOverlay shows an overlay.
func (r *TuiReference) ShowOverlay(component Component, options *OverlayOptions) OverlayHandle {
	return r.get().ShowOverlay(component, options)
}

// HideOverlay hides the top overlay.
func (r *TuiReference) HideOverlay() { r.get().HideOverlay() }

// HasOverlay reports whether an overlay is shown.
func (r *TuiReference) HasOverlay() bool { return r.get().HasOverlay() }

// HasOverlayEntries reports whether any overlay entry exists.
func (r *TuiReference) HasOverlayEntries() bool { return r.get().HasOverlayEntries() }

// AddChild adds a child component.
func (r *TuiReference) AddChild(component Component) { r.get().AddChild(component) }

// RemoveChild removes a child component.
func (r *TuiReference) RemoveChild(component Component) { r.get().RemoveChild(component) }

// Clear clears the children.
func (r *TuiReference) Clear() { r.get().Clear() }

var _ TUI = (*TuiReference)(nil)

var _ TUI = (*TuiReference)(nil)

// Current returns the active renderer (for code that must type-assert the
// concrete renderer, upstream's this.renderer).
func (r *TuiReference) Current() TUI { return r.get() }
