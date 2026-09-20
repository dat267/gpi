package tui

import (
	"sync"
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

	mu                        sync.Mutex
	currentScrollbar          ScrollViewScrollbar
	currentScrollTop          int
	contentHeight             int
	currentViewportHeight     int
	followingEnd              bool
	followSuppressedAtEnd     bool
	requestRenderCallback     func()
	transientScrollbarVisible bool
	scrollbarActive           bool
	scrollbarHideTimer        *time.Timer
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
	view.hideDelayMS = maxInt(0, options.ScrollbarHideDelayMS)
	view.followingEnd = view.followEnd
	view.Container.Children = append(view.Container.Children, component)
	return view
}

// ScrollTop returns the current scroll offset.
func (s *ScrollView) ScrollTop() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentScrollTop
}

// IsFollowingEnd reports whether the view follows the content end.
func (s *ScrollView) IsFollowingEnd() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.followingEnd
}

// ViewportHeight returns the last laid-out viewport height.
func (s *ScrollView) ViewportHeight() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentViewportHeight
}

// Primary reports whether this is the primary scroll view (layout interface).
func (s *ScrollView) Primary() bool { return s.primary }

// Overscroll reports the overscroll policy (layout interface).
func (s *ScrollView) Overscroll() string { return s.overscroll }

// Scrollbar returns the current scrollbar mode.
func (s *ScrollView) Scrollbar() ScrollViewScrollbar {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentScrollbar
}

// IsScrollbarVisible reports whether the scrollbar is shown.
func (s *ScrollView) IsScrollbarVisible() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentScrollbar == ScrollbarAlways {
		return s.currentViewportHeight > 0
	}
	return s.currentScrollbar == ScrollbarAuto && s.contentHeight > s.currentViewportHeight && s.transientScrollbarVisible
}

// IsScrollbarActive reports whether the scrollbar shows the active thumb.
func (s *ScrollView) IsScrollbarActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scrollbarActive
}

// SetScrollbar changes the scrollbar mode.
func (s *ScrollView) SetScrollbar(scrollbar ScrollViewScrollbar) {
	s.mu.Lock()
	if scrollbar == s.currentScrollbar {
		s.mu.Unlock()
		return
	}
	s.currentScrollbar = scrollbar
	callback := s.requestRenderCallback
	if scrollbar != ScrollbarAuto {
		s.hideTransientScrollbarLocked()
	} else if s.scrollbarActive {
		s.markScrollbarActivityLocked()
	}
	s.mu.Unlock()
	if callback != nil {
		callback()
	}
}

// GetContentWidth returns the child width (one column less when the scrollbar
// is always visible).
func (s *ScrollView) GetContentWidth(width int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
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
	if s.scrollbarHideTimer != nil {
		s.scrollbarHideTimer.Stop()
		s.scrollbarHideTimer = nil
	}
	if s.scrollbarActive {
		return
	}
	s.scrollbarHideTimer = time.AfterFunc(time.Duration(s.hideDelayMS)*time.Millisecond, func() {
		s.mu.Lock()
		s.scrollbarHideTimer = nil
		s.transientScrollbarVisible = false
		callback := s.requestRenderCallback
		s.mu.Unlock()
		if callback != nil {
			callback()
		}
	})
}

func (s *ScrollView) hideTransientScrollbarLocked() {
	s.transientScrollbarVisible = false
	if s.scrollbarHideTimer == nil {
		return
	}
	s.scrollbarHideTimer.Stop()
	s.scrollbarHideTimer = nil
}

// SetScrollbarActive marks the scrollbar active (dragging).
func (s *ScrollView) SetScrollbarActive(active bool) {
	s.mu.Lock()
	if active == s.scrollbarActive {
		s.mu.Unlock()
		return
	}
	s.scrollbarActive = active
	s.markScrollbarActivityLocked()
	callback := s.requestRenderCallback
	s.mu.Unlock()
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
	s.mu.Lock()
	requested := scrollTop
	maxScrollTop := maxInt(0, s.contentHeight-s.currentViewportHeight)
	next := maxInt(0, minInt(maxScrollTop, requested))
	nextFollowSuppressedAtEnd := disableFollow && next == maxScrollTop
	nextFollowingEnd := !nextFollowSuppressedAtEnd && s.followEnd && next == maxScrollTop
	if next == s.currentScrollTop && nextFollowingEnd == s.followingEnd &&
		nextFollowSuppressedAtEnd == s.followSuppressedAtEnd {
		s.mu.Unlock()
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
	s.mu.Unlock()
	if callback != nil {
		callback()
	}
}

// ScrollBy scrolls by a relative amount and returns the unconsumed lines.
func (s *ScrollView) ScrollBy(lines int) int {
	if lines == 0 {
		return 0
	}
	s.mu.Lock()
	maxScrollTop := maxInt(0, s.contentHeight-s.currentViewportHeight)
	start := s.currentScrollTop
	if s.followingEnd {
		start = maxScrollTop
	}
	next := maxInt(0, minInt(maxScrollTop, start+lines))
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
	s.mu.Unlock()
	if callback != nil {
		callback()
	}
	return lines - moved
}

// ScrollToStart scrolls to the top.
func (s *ScrollView) ScrollToStart() {
	s.mu.Lock()
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
	s.mu.Unlock()
	if callback != nil {
		callback()
	}
}

// ScrollToEnd scrolls to the end.
func (s *ScrollView) ScrollToEnd() {
	s.mu.Lock()
	next := maxInt(0, s.contentHeight-s.currentViewportHeight)
	changed := s.currentScrollTop != next || s.followingEnd != s.followEnd
	s.currentScrollTop = next
	s.followingEnd = s.followEnd
	s.followSuppressedAtEnd = false
	var callback func()
	if changed {
		s.markScrollbarActivityLocked()
		callback = s.requestRenderCallback
	}
	s.mu.Unlock()
	if callback != nil {
		callback()
	}
}

// UpdateLayout records the content and viewport heights (layout interface).
func (s *ScrollView) UpdateLayout(contentHeight int, viewportHeight int, requestRender func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.contentHeight = maxInt(0, contentHeight)
	s.currentViewportHeight = maxInt(0, viewportHeight)
	s.requestRenderCallback = requestRender
	maxScrollTop := maxInt(0, s.contentHeight-s.currentViewportHeight)
	if s.followingEnd {
		s.currentScrollTop = maxScrollTop
	} else {
		s.currentScrollTop = maxInt(0, minInt(s.currentScrollTop, maxScrollTop))
	}
	if s.currentScrollTop < maxScrollTop {
		s.followSuppressedAtEnd = false
	}
	if s.followEnd && s.currentScrollTop == maxScrollTop && !s.followSuppressedAtEnd {
		s.followingEnd = true
	}
	if s.contentHeight <= s.currentViewportHeight {
		s.hideTransientScrollbarLocked()
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

// Render renders the child at the content width, padding a trailing column
// when the scrollbar takes one.
func (s *ScrollView) Render(width int) []string {
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

// LayoutNode exposes the scroll view to the layout engine.
func (s *ScrollView) LayoutNode() LayoutNode {
	return LayoutNode{Kind: "scroll", Scroll: &ScrollLayoutNode{Type: "scroll", Component: s.child, State: s}}
}

var _ LayoutNodeProvider = (*ScrollView)(nil)
var _ Component = (*ScrollView)(nil)
