package tui

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

	// mouseLayout is the child hit-test layout from the last render. Render is
	// loop-owned (D146), so it needs no lock.
	mouseLayout      []mouseChild
	mouseLayoutWidth int

	// cacheLines is the concatenation of the children's lines at cacheWidth.
	// Reusing it keeps a warm frame off the heap: rebuilding it every paint
	// allocated (and then garbage-collected) the whole transcript.
	cacheLines []string
	// cacheChildren holds each child's rendered lines from that pass, compared
	// to decide whether cacheLines is still current.
	cacheChildren [][]string
	cacheWidth    int

	// frame scratch, reused across renders so a frame does not allocate for
	// every child and every line.
	childrenSnapshot []Component
	childRenders     [][]string
	lineScratch      []string
}

type mouseChild struct {
	component Component
	height    int
}

// childComponents implements childrenHolder.
func (c *Container) childComponents() []Component { return c.Children }

// Preparer warms a component's render caches without producing output, so an
// off-loop worker can pre-render the expensive parts (markdown lex + styling)
// before the UI loop paints. The loop's later Render at the same width is then
// a cache hit. Components with no cacheable subtree do not implement it, and a
// warmer type-asserts and skips them.
//
// Prepare must be safe to call on a component that is not attached to the
// rendered tree: it may only touch the component's own captured state (never
// the process-wide theme), because the UI loop keeps running while it runs on
// another goroutine.
type Preparer interface {
	Prepare(width int)
}

// Prepare warms every child that implements Preparer.
func (c *Container) Prepare(width int) {
	for _, child := range c.Children {
		if p, ok := child.(Preparer); ok {
			p.Prepare(width)
		}
	}
}

// AddChild appends a child component.
func (c *Container) AddChild(component Component) {
	c.Children = append(c.Children, component)
	c.dropRenderCache()
}

// InsertChildAt inserts a child at the given index (clamped to the current
// range).
func (c *Container) InsertChildAt(index int, component Component) {
	if index < 0 || index > len(c.Children) {
		index = len(c.Children)
	}
	c.Children = append(c.Children, nil)
	copy(c.Children[index+1:], c.Children[index:])
	c.Children[index] = component
	c.dropRenderCache()
}

// RemoveChild removes a child component.
func (c *Container) RemoveChild(component Component) {
	for i, child := range c.Children {
		if child == component {
			c.Children = append(c.Children[:i], c.Children[i+1:]...)
			c.dropRenderCache()
			return
		}
	}
}

// Clear removes all children.
func (c *Container) Clear() {
	c.Children = nil
	c.dropRenderCache()
}

// dropRenderCache clears the cached concatenation.
func (c *Container) dropRenderCache() {
	c.cacheLines = nil
	c.cacheChildren = nil
	c.cacheWidth = 0
}

// Invalidate invalidates every child.
func (c *Container) Invalidate() {
	c.dropRenderCache()
	// Snapshot under the lock, deliver outside it: a child's Invalidate may
	// re-enter (AssistantMessageComponent.Invalidate rebuilds its content).
	children := append([]Component{}, c.Children...)
	for _, child := range children {
		child.Invalidate()
	}
}

// HandleMouse forwards an event to the child under the pointer.
func (c *Container) HandleMouse(event TuiMouseEvent) *TuiMouseDispatchResult {
	if event.Y < 0 || event.Y >= event.Height {
		return nil
	}
	mouseChildren := c.mouseLayout
	mouseLayoutWidth := c.mouseLayoutWidth
	children := append([]Component{}, c.Children...)
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
	// Snapshot the children: a child's Render may re-enter and mutate
	// c.Children. The scratch buffer is reused, not reallocated.
	if cap(c.childrenSnapshot) < len(c.Children) {
		c.childrenSnapshot = make([]Component, len(c.Children))
	}
	c.childrenSnapshot = c.childrenSnapshot[:len(c.Children)]
	copy(c.childrenSnapshot, c.Children)

	if cap(c.mouseLayout) < len(c.childrenSnapshot) {
		c.mouseLayout = make([]mouseChild, 0, len(c.childrenSnapshot))
	} else {
		c.mouseLayout = c.mouseLayout[:0]
	}
	if cap(c.childRenders) < len(c.childrenSnapshot) {
		c.childRenders = make([][]string, len(c.childrenSnapshot))
	}
	c.childRenders = c.childRenders[:len(c.childrenSnapshot)]
	for i, child := range c.childrenSnapshot {
		childLines := child.Render(width)
		c.childRenders[i] = childLines
		c.mouseLayout = append(c.mouseLayout, mouseChild{component: child, height: len(childLines)})
	}
	c.mouseLayoutWidth = width

	if c.matchRenderCache(width) {
		return c.cacheLines
	}

	c.lineScratch = c.lineScratch[:0]
	for _, childLines := range c.childRenders {
		c.lineScratch = append(c.lineScratch, childLines...)
	}
	lines := make([]string, len(c.lineScratch))
	copy(lines, c.lineScratch)

	c.cacheLines = lines
	c.cacheChildren = append(c.cacheChildren[:0], c.childRenders...)
	c.cacheWidth = width
	return lines
}

// matchRenderCache reports whether cacheLines still describes the children's
// current lines.
func (c *Container) matchRenderCache(width int) bool {
	if c.cacheLines == nil || c.cacheWidth != width || len(c.cacheChildren) != len(c.childRenders) {
		return false
	}
	for i, cached := range c.cacheChildren {
		lines := c.childRenders[i]
		if len(cached) != len(lines) {
			return false
		}
		// A child that reuses its slice is unchanged by definition.
		if len(lines) > 0 && &cached[0] == &lines[0] {
			continue
		}
		for j, line := range lines {
			if cached[j] != line {
				return false
			}
		}
	}
	return true
}

// MouseLayout returns the layout recorded by the last Render.
func (c *Container) MouseLayout() (width int, children []struct {
	Component Component
	Height    int
}) {
	mouseLayout := append([]mouseChild{}, c.mouseLayout...)
	mouseLayoutWidth := c.mouseLayoutWidth
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
