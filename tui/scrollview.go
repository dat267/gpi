package tui

import (
	"time"
)

// Port of src/components/scroll-view.ts: the single-child scroll container
// that owns the scroll position, the follow-end behaviour, and the transient
// scrollbar.

// ScrollViewScrollbar is the scrollbar mode.
type ScrollViewScrollbar string

const (
	ScrollbarHidden ScrollViewScrollbar = "hidden"
	ScrollbarAuto   ScrollViewScrollbar = "auto"
	ScrollbarAlways ScrollViewScrollbar = "always"
)

// ScrollViewOptions configure a scroll view.
type ScrollViewOptions struct {
	Axis                 string // "" or "vertical"
	Follow               string // "none" | "end"
	Primary              bool
	Overscroll           string // "chain" | "contain"
	Scrollbar            ScrollViewScrollbar
	ScrollbarTrackStyle  func(text string) string
	ScrollbarThumbStyle  func(text string) string
	ScrollbarHideDelayMS int
	// HasScrollbarHideDelay distinguishes an explicit 0 from the unset default
	// (upstream's `?? 1000`).
	HasScrollbarHideDelay bool
}

// ScrollView is a container with exactly one child.
type ScrollView struct {
	*Container

	child       Component
	followEnd   bool
	primary     bool
	overscroll  string
	trackStyle  func(text string) string
	thumbStyle  func(text string) string
	hideDelayMS int

	currentScrollbar          ScrollViewScrollbar
	currentScrollTop          int
	contentHeight             int
	currentViewportHeight     int
	followingEnd              bool
	followSuppressedAtEnd     bool
	requestRenderCallback     func()
	transientScrollbarVisible bool
	scrollbarActive           bool
	// scrollbarHideDeadline drives the transient scrollbar's lazy hide: the
	// consumer's animation walk wakes on it and the next Render hides the
	// scrollbar (D146: no internal timer, no mutex — loop-owned state).
	scrollbarHideDeadline time.Time
}

// NewScrollView wraps a component in a scroll view.
func NewScrollView(component Component, options ScrollViewOptions) *ScrollView {
	if options.Axis != "" && options.Axis != "vertical" {
		panic("Unsupported ScrollView axis: " + options.Axis)
	}
	view := &ScrollView{
		Container:        &Container{},
		child:            component,
		followEnd:        options.Follow == "end",
		primary:          options.Primary,
		overscroll:       options.Overscroll,
		currentScrollbar: options.Scrollbar,
	}
	if view.overscroll == "" {
		view.overscroll = "chain"
	}
	if view.currentScrollbar == "" {
		view.currentScrollbar = ScrollbarHidden
	}
	view.trackStyle = options.ScrollbarTrackStyle
	if view.trackStyle == nil {
		view.trackStyle = func(text string) string { return "\x1b[90m" + text + "\x1b[39m" }
	}
	view.thumbStyle = options.ScrollbarThumbStyle
	if view.thumbStyle == nil {
		view.thumbStyle = func(text string) string { return "\x1b[37m" + text + "\x1b[39m" }
	}
	// Upstream defaults the transient scrollbar hide delay to 1000ms; an
	// explicit 0 disables the timer (D119).
	view.hideDelayMS = 1000
	if options.HasScrollbarHideDelay {
		view.hideDelayMS = max(0, options.ScrollbarHideDelayMS)
	}
	view.followingEnd = view.followEnd
	view.Container.Children = append(view.Container.Children, component)
	return view
}

// AnimationFrame implements Animator: wake when the transient scrollbar's
// hide deadline passes.
func (s *ScrollView) AnimationFrame(now time.Time) (bool, time.Duration) {
	if s.scrollbarHideDeadline.IsZero() {
		return false, 0
	}
	if now.Before(s.scrollbarHideDeadline) {
		return true, s.scrollbarHideDeadline.Sub(now)
	}
	// Expired: hide and ask for one more frame to repaint without it.
	s.hideTransientScrollbar()
	return true, time.Millisecond
}

// ScrollTop returns the current scroll offset.
func (s *ScrollView) ScrollTop() int {
	return s.currentScrollTop
}

// IsFollowingEnd reports whether the view follows the content end.
func (s *ScrollView) IsFollowingEnd() bool {
	return s.followingEnd
}

// ViewportHeight returns the last laid-out viewport height.
func (s *ScrollView) ViewportHeight() int {
	return s.currentViewportHeight
}

// FollowEnd reports whether the view follows the content end.
func (s *ScrollView) FollowEnd() bool { return s.followEnd }

// Primary reports whether this is the primary scroll view (layout interface).
func (s *ScrollView) Primary() bool { return s.primary }

// Overscroll reports the overscroll policy (layout interface).
func (s *ScrollView) Overscroll() string { return s.overscroll }

// Scrollbar returns the current scrollbar mode.
func (s *ScrollView) Scrollbar() ScrollViewScrollbar {
	return s.currentScrollbar
}

// IsScrollbarVisible reports whether the scrollbar is shown.
func (s *ScrollView) IsScrollbarVisible() bool {
	if s.currentScrollbar == ScrollbarAlways {
		return s.currentViewportHeight > 0
	}
	return s.currentScrollbar == ScrollbarAuto && s.contentHeight > s.currentViewportHeight && s.transientScrollbarVisible
}

// IsScrollbarActive reports whether the scrollbar shows the active thumb.
func (s *ScrollView) IsScrollbarActive() bool {
	return s.scrollbarActive
}

// SetScrollbar changes the scrollbar mode.
func (s *ScrollView) SetScrollbar(scrollbar ScrollViewScrollbar) {
	if scrollbar == s.currentScrollbar {
		return
	}
	s.currentScrollbar = scrollbar
	callback := s.requestRenderCallback
	if scrollbar != ScrollbarAuto {
		s.hideTransientScrollbar()
	} else if s.scrollbarActive {
		s.markScrollbarActivityLocked()
	}
	if callback != nil {
		callback()
	}
}

// GetContentWidth returns the child width (one column less when the scrollbar
// is always visible).
func (s *ScrollView) GetContentWidth(width int) int {
	if s.currentScrollbar == ScrollbarAlways && width > 1 {
		return width - 1
	}
	return width
}

func (s *ScrollView) markScrollbarActivityLocked() {
	if s.currentScrollbar != ScrollbarAuto || s.contentHeight <= s.currentViewportHeight {
		return
	}
	s.transientScrollbarVisible = true
	s.scrollbarHideDeadline = time.Now().Add(time.Duration(s.hideDelayMS) * time.Millisecond)
	if s.scrollbarActive {
		return
	}
}

func (s *ScrollView) hideTransientScrollbar() {
	s.transientScrollbarVisible = false
	s.scrollbarHideDeadline = time.Time{}
}

// SetScrollbarActive marks the scrollbar active (dragging).
func (s *ScrollView) SetScrollbarActive(active bool) {
	if active == s.scrollbarActive {
		return
	}
	s.scrollbarActive = active
	s.markScrollbarActivityLocked()
	callback := s.requestRenderCallback
	if callback != nil {
		callback()
	}
}

// ScrollToRequest holds the optional disableFollow flag.
type ScrollToRequest struct {
	DisableFollow bool
}

// ScrollTo scrolls to an absolute offset.
func (s *ScrollView) ScrollTo(scrollTop int, options ...ScrollToRequest) {
	disableFollow := false
	if len(options) > 0 {
		disableFollow = options[0].DisableFollow
	}
	requested := scrollTop
	maxScrollTop := max(0, s.contentHeight-s.currentViewportHeight)
	next := max(0, min(maxScrollTop, requested))
	nextFollowSuppressedAtEnd := disableFollow && next == maxScrollTop
	nextFollowingEnd := !nextFollowSuppressedAtEnd && s.followEnd && next == maxScrollTop
	if next == s.currentScrollTop && nextFollowingEnd == s.followingEnd &&
		nextFollowSuppressedAtEnd == s.followSuppressedAtEnd {
		return
	}
	moved := next != s.currentScrollTop
	s.currentScrollTop = next
	s.followingEnd = nextFollowingEnd
	s.followSuppressedAtEnd = nextFollowSuppressedAtEnd
	var callback func()
	if moved {
		s.markScrollbarActivityLocked()
		callback = s.requestRenderCallback
	}
	if callback != nil {
		callback()
	}
}

// ScrollBy scrolls by a relative amount and returns the unconsumed lines.
func (s *ScrollView) ScrollBy(lines int) int {
	if lines == 0 {
		return 0
	}
	maxScrollTop := max(0, s.contentHeight-s.currentViewportHeight)
	start := s.currentScrollTop
	if s.followingEnd {
		start = maxScrollTop
	}
	next := max(0, min(maxScrollTop, start+lines))
	moved := next - start
	wasFollowingEnd := s.followingEnd
	s.currentScrollTop = next
	s.followingEnd = s.followEnd && next == maxScrollTop
	s.followSuppressedAtEnd = false
	var callback func()
	if moved != 0 {
		s.markScrollbarActivityLocked()
	}
	if moved != 0 || s.followingEnd != wasFollowingEnd {
		callback = s.requestRenderCallback
	}
	if callback != nil {
		callback()
	}
	return lines - moved
}

// ScrollToStart scrolls to the top.
func (s *ScrollView) ScrollToStart() {
	changed := s.currentScrollTop != 0 ||
		s.followingEnd != (s.followEnd && s.contentHeight <= s.currentViewportHeight)
	s.currentScrollTop = 0
	s.followingEnd = s.followEnd && s.contentHeight <= s.currentViewportHeight
	s.followSuppressedAtEnd = false
	var callback func()
	if changed {
		s.markScrollbarActivityLocked()
		callback = s.requestRenderCallback
	}
	if callback != nil {
		callback()
	}
}

// ScrollToEnd scrolls to the end.
func (s *ScrollView) ScrollToEnd() {
	next := max(0, s.contentHeight-s.currentViewportHeight)
	changed := s.currentScrollTop != next || s.followingEnd != s.followEnd
	s.currentScrollTop = next
	s.followingEnd = s.followEnd
	s.followSuppressedAtEnd = false
	var callback func()
	if changed {
		s.markScrollbarActivityLocked()
		callback = s.requestRenderCallback
	}
	if callback != nil {
		callback()
	}
}

// UpdateLayout records the content and viewport heights (layout interface).
func (s *ScrollView) UpdateLayout(contentHeight int, viewportHeight int, requestRender func()) {
	s.contentHeight = max(0, contentHeight)
	s.currentViewportHeight = max(0, viewportHeight)
	s.requestRenderCallback = requestRender
	maxScrollTop := max(0, s.contentHeight-s.currentViewportHeight)
	if s.followingEnd {
		s.currentScrollTop = maxScrollTop
	} else {
		s.currentScrollTop = max(0, min(s.currentScrollTop, maxScrollTop))
	}
	if s.currentScrollTop < maxScrollTop {
		s.followSuppressedAtEnd = false
	}
	if s.followEnd && s.currentScrollTop == maxScrollTop && !s.followSuppressedAtEnd {
		s.followingEnd = true
	}
	if s.contentHeight <= s.currentViewportHeight {
		s.hideTransientScrollbar()
	}
}

// AddChild is not supported: a ScrollView has exactly one child.
func (s *ScrollView) AddChild(component Component) {
	panic("ScrollView has exactly one child")
}

// RemoveChild is not supported.
func (s *ScrollView) RemoveChild(component Component) {
	panic("ScrollView child cannot be removed")
}

// Clear is not supported.
func (s *ScrollView) Clear() {
	panic("ScrollView child cannot be cleared")
}

// Child returns the wrapped component.
func (s *ScrollView) Child() Component { return s.child }

// childComponents exposes the wrapped content to the tree walks (the animation
// scan). ScrollView holds its one child in a field rather than in the embedded
// Container's Children, so without this the walk stopped at the scroll view and
// never reached an animator inside the transcript (the elapsed-time label froze
// while a tool ran with no output).
func (s *ScrollView) childComponents() []Component {
	if s.child == nil {
		return nil
	}
	return []Component{s.child}
}

// Render renders the child at the content width, padding a trailing column
// when the scrollbar takes one.
// Render renders the scrolled view, lazily expiring the transient scrollbar's
// hide deadline (D146: the deadline is driven by the consumer's animation
// walk; the next render after it passes hides the scrollbar).
func (s *ScrollView) Render(width int) []string {
	if !s.scrollbarHideDeadline.IsZero() && !time.Now().Before(s.scrollbarHideDeadline) {
		s.hideTransientScrollbar()
	}
	contentWidth := s.GetContentWidth(width)
	lines := s.child.Render(contentWidth)
	if contentWidth == width {
		return lines
	}
	out := make([]string, len(lines))
	for index, line := range lines {
		out[index] = line + " "
	}
	return out
}

// RenderVersion forwards the child's revision. Render hands back the child's own
// lines (or a fresh padded copy of them), so a child that rewrites its suffix in
// place — the same backing array with a bumped revision — must reach the parent,
// which cannot see it through slice identity. The embedded container's revision
// says nothing about what this view renders, so it must not be promoted here.
func (s *ScrollView) RenderVersion() (uint64, bool) {
	if versioned, ok := s.child.(renderVersioner); ok {
		if version, has := versioned.RenderVersion(); has {
			return version, true
		}
	}
	return 0, false
}

// LayoutNode exposes the scroll view to the layout engine.
func (s *ScrollView) LayoutNode() LayoutNode {
	return LayoutNode{Kind: "scroll", Scroll: &ScrollLayoutNode{Type: "scroll", Component: s.child, State: s}}
}

var _ LayoutNodeProvider = (*ScrollView)(nil)
var _ Component = (*ScrollView)(nil)
