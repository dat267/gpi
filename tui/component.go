package tui

import "sync"

// Port of the component model from src/tui.ts: the Component interface, the
// mouse event types, focusability, and the Container.

// TuiMouseEventType is the normalized mouse event kind.
type TuiMouseEventType string

const (
	MousePress   TuiMouseEventType = "press"
	MouseRelease TuiMouseEventType = "release"
	MouseMove    TuiMouseEventType = "move"
	MouseDrag    TuiMouseEventType = "drag"
	MouseClick   TuiMouseEventType = "click"
	MouseWheel   TuiMouseEventType = "wheel"
)

// TuiMouseButton is the button involved in a mouse event.
type TuiMouseButton string

const (
	MouseButtonLeft   TuiMouseButton = "left"
	MouseButtonMiddle TuiMouseButton = "middle"
	MouseButtonRight  TuiMouseButton = "right"
	MouseButtonNone   TuiMouseButton = "none"
)

// TuiMouseEvent is a normalized cell-based mouse event (zero-based).
type TuiMouseEvent struct {
	Type   TuiMouseEventType
	Button TuiMouseButton
	// X and Y are local to the receiving component.
	X int
	Y int
	// ScreenX and ScreenY are absolute terminal coordinates.
	ScreenX int
	ScreenY int
	// Width and Height are the current component bounds.
	Width  int
	Height int
	Shift  bool
	Alt    bool
	Ctrl   bool
	// WheelDelta is in logical lines (negative scrolls up).
	WheelDelta int
	HasWheel   bool
	// ClickCount is the consecutive click count when Type is click.
	ClickCount int
	HasClick   bool
}

// TuiMouseEventResult is a component's mouse handling outcome.
type TuiMouseEventResult struct {
	// Handled stops propagation and suppresses renderer-level fallback behavior.
	Handled bool
	// Capture routes subsequent drag/release events to this component.
	// Implies handled.
	Capture bool
	// Focus gives keyboard focus to this component. Implies handled.
	Focus bool
	// Render explicitly requests or suppresses a render. Move and release
	// default to false; press, click, drag, and wheel default to true.
	Render    bool
	HasRender bool
}

// TuiMouseDispatchTarget is the resolved target of a dispatched event.
type TuiMouseDispatchTarget struct {
	Component Component
	OriginX   int
	OriginY   int
	Width     int
	Height    int
}

// TuiMouseDispatchResult is the result of dispatching to a concrete component.
// Handled lives in the embedded TuiMouseEventResult (upstream narrows it to the
// literal `true`; Go keeps the same single field: divergence D48).
type TuiMouseDispatchResult struct {
	TuiMouseEventResult
	Target TuiMouseDispatchTarget
	// FocusTarget is the keyboard focus target, which may be a delegating
	// parent container.
	FocusTarget Component
	HasFocus    bool
}

// Component is implemented by every renderable UI element. Upstream's optional
// handleInput/handleMouse/wantsKeyRelease appear as the narrower InputHandler,
// MouseHandler, and KeyReleaseWanter interfaces, probed with type assertions
// (Go interfaces cannot have optional methods).
type Component interface {
	// Render renders the component to lines for the given viewport width.
	Render(width int) []string
	// Invalidate drops cached rendering state.
	Invalidate()
}

// InputHandler is implemented by components that accept keyboard input.
type InputHandler interface {
	HandleInput(data string)
}

// MouseHandler is implemented by components that accept mouse events.
type MouseHandler interface {
	HandleMouse(event TuiMouseEvent) *TuiMouseDispatchResult
}

// KeyReleaseWanter opts a component into Kitty key-release events.
type KeyReleaseWanter interface {
	WantsKeyRelease() bool
}

// Focusable is implemented by components that can receive focus and display a
// hardware cursor, emitting CursorMarker at the cursor position when focused.
type Focusable interface {
	SetFocused(focused bool)
	IsFocused() bool
}

// CursorMarker is an APC (Application Program Command) sequence. It is a
// zero-width escape that terminals ignore; components emit it at the cursor
// position when focused, and the renderer strips it and positions the hardware
// cursor there. (Upstream exposes a public `focused` field; Go uses
// SetFocused/IsFocused: divergence D43.)
const CursorMarker = "\x1b_pi:c\x07"

// IsFocusable reports whether a component implements Focusable.
func IsFocusable(component Component) bool {
	_, ok := component.(Focusable)
	return ok
}

// DispatchMouseEvent dispatches an event to a component and retains the exact
// target and coordinate transform. Containers use this when forwarding events
// to nested children.
func DispatchMouseEvent(component Component, event TuiMouseEvent) *TuiMouseDispatchResult {
	handler, ok := component.(MouseHandler)
	if !ok {
		return nil
	}
	result := handler.HandleMouse(event)
	if result == nil {
		return nil
	}
	// Upstream returns a result untouched once it already carries a target
	// ("if (\"target\" in result) return result"): nested dispatches own the
	// target/focus resolution, outer levels must not re-stamp focus to
	// themselves — that let a container claim keyboard focus after a press on
	// the editor's autocomplete popup, and the container has no HandleInput,
	// so keystrokes were dropped (fullscreen input box froze).
	if result.Target.Component != nil {
		return result
	}
	if !result.Handled && !result.Capture && !result.Focus {
		return nil
	}
	result.Handled = true
	if result.Focus {
		result.FocusTarget = component
		result.HasFocus = true
	}
	result.Target = TuiMouseDispatchTarget{
		Component: component,
		OriginX:   event.ScreenX - event.X,
		OriginY:   event.ScreenY - event.Y,
		Width:     event.Width,
		Height:    event.Height,
	}
	return result
}

// RetargetMouseEvent recreates local coordinates for a previously dispatched
// mouse target.
func RetargetMouseEvent(event TuiMouseEvent, target TuiMouseDispatchTarget) TuiMouseEvent {
	event.X = event.ScreenX - target.OriginX
	event.Y = event.ScreenY - target.OriginY
	event.Width = target.Width
	event.Height = target.Height
	return event
}

// childrenHolder exposes a component's children to the renderer's mount
// checks (upstream uses `instanceof Container`; D55).
type childrenHolder interface {
	childComponents() []Component
}

// Container is a component that contains other components.
type Container struct {
	Children []Component

	// mu guards the mutation/read pairs on Children and the mouse layout:
	// session-event goroutines mutate the chat/document containers while the
	// render timer renders them (upstream is single-threaded; D141's race
	// report made this one explicit).
	mu               sync.Mutex
	mouseLayout      []mouseChild
	mouseLayoutWidth int
}

type mouseChild struct {
	component Component
	height    int
}

// childComponents implements childrenHolder.
func (c *Container) childComponents() []Component { return c.Children }

// AddChild appends a child component.
func (c *Container) AddChild(component Component) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Children = append(c.Children, component)
}

// InsertChildAt inserts a child at the given index (clamped to the current
// range).
func (c *Container) InsertChildAt(index int, component Component) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if index < 0 || index > len(c.Children) {
		index = len(c.Children)
	}
	c.Children = append(c.Children, nil)
	copy(c.Children[index+1:], c.Children[index:])
	c.Children[index] = component
}

// RemoveChild removes a child component.
func (c *Container) RemoveChild(component Component) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, child := range c.Children {
		if child == component {
			c.Children = append(c.Children[:i], c.Children[i+1:]...)
			return
		}
	}
}

// Clear removes all children.
func (c *Container) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Children = nil
}

// Invalidate invalidates every child.
func (c *Container) Invalidate() {
	// Snapshot under the lock, deliver outside it: a child's Invalidate may
	// re-enter (AssistantMessageComponent.Invalidate rebuilds its content).
	c.mu.Lock()
	children := append([]Component{}, c.Children...)
	c.mu.Unlock()
	for _, child := range children {
		child.Invalidate()
	}
}

// HandleMouse forwards an event to the child under the pointer.
func (c *Container) HandleMouse(event TuiMouseEvent) *TuiMouseDispatchResult {
	if event.Y < 0 || event.Y >= event.Height {
		return nil
	}
	c.mu.Lock()
	mouseChildren := c.mouseLayout
	mouseLayoutWidth := c.mouseLayoutWidth
	children := append([]Component{}, c.Children...)
	c.mu.Unlock()
	if mouseLayoutWidth != event.Width {
		mouseChildren = make([]mouseChild, 0, len(children))
		for _, child := range children {
			mouseChildren = append(mouseChildren, mouseChild{component: child, height: len(child.Render(event.Width))})
		}
	}
	childY := 0
	for _, child := range mouseChildren {
		if event.Y >= childY && event.Y < childY+child.height {
			childEvent := event
			childEvent.Y = event.Y - childY
			childEvent.Height = child.height
			result := DispatchMouseEvent(child.component, childEvent)
			if result != nil && result.Focus {
				if _, ok := any(c).(InputHandler); ok {
					result.FocusTarget = c
					result.HasFocus = true
				}
			}
			return result
		}
		childY += child.height
	}
	return nil
}

// Render renders every child and records the mouse layout.
func (c *Container) Render(width int) []string {
	c.mu.Lock()
	children := append([]Component{}, c.Children...)
	c.mu.Unlock()
	var lines []string
	mouseChildren := make([]mouseChild, 0, len(children))
	for _, child := range children {
		childLines := child.Render(width)
		mouseChildren = append(mouseChildren, mouseChild{component: child, height: len(childLines)})
		lines = append(lines, childLines...)
	}
	c.mu.Lock()
	c.mouseLayout = mouseChildren
	c.mouseLayoutWidth = width
	c.mu.Unlock()
	return lines
}

// MouseLayout returns the layout recorded by the last Render.
func (c *Container) MouseLayout() (width int, children []struct {
	Component Component
	Height    int
}) {
	c.mu.Lock()
	mouseLayout := append([]mouseChild{}, c.mouseLayout...)
	mouseLayoutWidth := c.mouseLayoutWidth
	c.mu.Unlock()
	out := make([]struct {
		Component Component
		Height    int
	}, 0, len(mouseLayout))
	for _, child := range mouseLayout {
		out = append(out, struct {
			Component Component
			Height    int
		}{child.component, child.height})
	}
	return mouseLayoutWidth, out
}

var (
	_ Component    = (*Container)(nil)
	_ MouseHandler = (*Container)(nil)
)
