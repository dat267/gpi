package tui

import (
	"encoding/base64"
	"os"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// Port of src/tui-alt-screen.ts: the fullscreen renderer with an
// application-owned viewport (scroll keys, mouse wheel/drag, text selection
// with copy, transcript search, flashes, and the scroll-to-end indicator).
//
// Divergences: the kitty image caching/eviction is not ported (the transport
// is out of scope: D57/D81) — image lines pass through and the image protocol
// only drives the clear/delete escapes; selection auto-scroll uses a
// time.AfterFunc loop guarded by a mutex (D82); clipboard writes go through the
// injected CopySelection hook or an OSC 52 escape (native clipboard is out of
// scope).

const (
	enterAltScreen           = "\x1b[?1049h"
	exitAltScreen            = "\x1b[?1049l"
	disableAutowrap          = "\x1b[?7l"
	enableAutowrap           = "\x1b[?7h"
	enableButtonMotionMouse  = "\x1b[?1000h\x1b[?1002h\x1b[?1004h\x1b[?1006h"
	enableAllMotionMouse     = "\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?1004h\x1b[?1006h"
	disableMouse             = "\x1b[?1006l\x1b[?1004l\x1b[?1003l\x1b[?1002l\x1b[?1000l"
	focusIn                  = "\x1b[I"
	focusOut                 = "\x1b[O"
	beginSynchronizedOutput  = "\x1b[?2026h"
	endSynchronizedOutput    = "\x1b[?2026l"
	pageScrollOverlap        = 4
	altWheelScrollMultiplier = 5
	doubleClickIntervalMS    = 500
	copyErrorFlashDurationMS = 5000
)

var (
	osc133ZonePrefix  = regexp.MustCompile(`^(?:\x1b\]133;[ABC](?:\x07|\x1b\\))+`)
	osc133PromptStart = regexp.MustCompile(`^\x1b\]133;A(?:\x07|\x1b\\)`)
	// osc133ZonePrefixMatch is the literal the zone regexp needs to see at the
	// start of a line: checking it first keeps the per-frame pass over every
	// document line off the regexp engine.
	osc133ZonePrefixMatch = "\x1b]133;"
	altSgrMouseRegex      = regexp.MustCompile(`^\x1b\[<(\d+);(\d+);(\d+)([Mm])$`)
	wheelSgrRegex         = regexp.MustCompile(`^\x1b\[<(\d+);(\d+);(\d+)[Mm]$`)
)

var terminalWordSelectionJoiners = map[string]bool{"/": true, "-": true}

type altSelectionPoint struct {
	Row        int
	Col        int
	ScrollView *ScrollView
	// Boundary marks a point between terminal cells rather than on a cell.
	Boundary bool
}

type altSelectionRange struct {
	Start altSelectionPoint
	End   altSelectionPoint
}

type altClickTarget struct {
	Timestamp  int64
	Count      int
	Row        int
	ScrollView *ScrollView
	WordStart  int
	WordEnd    int
}

type sgrMouseEvent struct {
	Button  int
	X       int
	Y       int
	Release bool
}

type altWheelEvent struct {
	Direction int
	X         int
	Y         int
	Button    int
}

type altScrollbarDrag struct {
	ScrollView *ScrollView
	GrabOffset int
}

type altScrollbarTarget struct {
	ScrollView *ScrollView
	Geometry   ScrollbarGeometry
}

type scrollToEndIndicatorRect struct {
	Row    int
	Column int
	Width  int
}

type altSearchHighlightRange struct {
	StartCol int
	EndCol   int
	Current  bool
}

type altActiveSearch struct {
	Component      *AltScreenSearchComponent
	Index          *AltScreenSearchIndex
	Overlay        OverlayHandle
	Query          string
	Matches        []AltScreenSearchMatch
	SelectedIndex  int
	SelectedKey    string
	HasSelectedKey bool
	AnchorRow      int
	SelectionMode  string // "query" | "retain" | "next" | "previous"
}

// AltScreenOptions configure the fullscreen renderer.
type AltScreenOptions struct {
	WheelScrollLines            int
	Mouse                       *bool
	SearchMatchStyle            func(text string) string
	SearchCurrentMatchStyle     func(text string) string
	SearchNavigationButtonStyle func(text string, hovered bool) string
	ScrollToEndIndicator        func() string
	OpenURL                     func(url string)
	OnRightClickPaste           func()
	CopyOnSelect                *bool
	// CopySelection returns (handled, ok, message): handled=false falls back to
	// OSC 52.
	CopySelection func(text string) (handled bool, ok bool, message string)
}

// AltScreen is the fullscreen renderer.
type AltScreen struct {
	*Renderer

	previousScreen       []string
	lastDocument         []string
	previousScreenWidth  int
	previousScreenHeight int
	// previousCursor* record what the last frame set, so a low-bandwidth frame
	// whose rows and cursor are unchanged can be skipped entirely.
	previousCursorRow   int
	previousCursorCol   int
	previousCursorHas   bool
	previousCursorValid bool
	// layoutRoot is atomic: mountedRoots is called while the renderer lock is
	// held, so it must not take the alt-screen lock (lock order: screen ->
	// renderer).
	layoutRoot                   atomic.Pointer[Component]
	currentLayout                *LayoutFrame
	implicitDocument             Component
	implicitScrollView           *ScrollView
	flashes                      *AltScreenFlashContainer
	altScreenActive              bool
	imageProtocol                string
	selectionAnchor              *altSelectionPoint
	selectionFocus               *altSelectionPoint
	selectionGranularity         string
	selectionInitialRange        *altSelectionRange
	hasSelectionInitialRange     bool
	lastClick                    *altClickTarget
	selectionDragPointer         *struct{ X, Y int }
	selectionAutoScrollDirection int
	// lastSelectionAutoScrollAt paces the clock-driven selection auto-scroll
	// (stage 4: no ticker goroutine).
	lastSelectionAutoScrollAt time.Time
	selectionPressActive      bool
	scrollbarDrag             *altScrollbarDrag
	scrollbarHover            *ScrollView
	scrollToEndRect           *scrollToEndIndicatorRect
	activeSearch              *altActiveSearch
	pressedURL                string
	hasPressedURL             bool
	selectionDragged          bool
	mouseCapture              *TuiMouseDispatchTarget
	mousePressTarget          *TuiMouseDispatchTarget
	mousePressPoint           *struct{ X, Y int }
	mousePressMoved           bool
	lastComponentClick        *struct {
		Timestamp int64
		Count     int
		Component Component
		X, Y      int
	}

	wheelScrollLines            int
	mouseEnabled                bool
	searchMatchStyle            func(string) string
	searchCurrentMatchStyle     func(string) string
	searchNavigationButtonStyle func(string, bool) string
	scrollToEndIndicator        func() string
	openURL                     func(string)
	onRightClickPaste           func()
	copyOnSelect                bool
	copySelection               func(string) (bool, bool, string)
}

// NewAltScreen creates the fullscreen renderer.
func NewAltScreen(terminal Terminal, showHardwareCursor bool, logDirectory string, options AltScreenOptions) *AltScreen {
	screen := &AltScreen{
		Renderer:                    NewRenderer(terminal),
		wheelScrollLines:            max(1, options.WheelScrollLines),
		mouseEnabled:                true,
		searchMatchStyle:            options.SearchMatchStyle,
		searchCurrentMatchStyle:     options.SearchCurrentMatchStyle,
		searchNavigationButtonStyle: options.SearchNavigationButtonStyle,
		scrollToEndIndicator:        options.ScrollToEndIndicator,
		openURL:                     options.OpenURL,
		onRightClickPaste:           options.OnRightClickPaste,
		copyOnSelect:                true,
		copySelection:               options.CopySelection,
		altScreenActive:             false,
		selectionGranularity:        "character",
	}
	if options.Mouse != nil {
		screen.mouseEnabled = *options.Mouse
	}
	if options.CopyOnSelect != nil {
		screen.copyOnSelect = *options.CopyOnSelect
	}
	if screen.searchMatchStyle == nil {
		screen.searchMatchStyle = func(text string) string { return "\x1b[4m" + text + "\x1b[24m" }
	}
	if screen.searchCurrentMatchStyle == nil {
		screen.searchCurrentMatchStyle = func(text string) string { return "\x1b[1;7m" + text + "\x1b[22;27m" }
	}
	if screen.searchNavigationButtonStyle == nil {
		screen.searchNavigationButtonStyle = func(text string, hovered bool) string { return text }
	}

	screen.implicitDocument = &implicitDocumentComponent{renderer: screen.Renderer}
	screen.implicitScrollView = NewScrollView(screen.implicitDocument, ScrollViewOptions{Follow: "end", Primary: true})
	screen.flashes = NewAltScreenFlashContainer(func() { screen.RequestRender(false) })
	screen.ShowHardwareCursor = showHardwareCursor
	screen.LogDirectory = logDirectory
	screen.DoRender = screen.doRender
	screen.OnResetRenderState = screen.resetRenderState
	screen.OnBeforeTerminalStart = screen.beforeTerminalStart
	screen.OnBeforeTerminalStop = screen.beforeTerminalStop
	screen.OnAfterTerminalStop = screen.afterTerminalStop
	screen.MountedRoots = screen.mountedRoots
	screen.AddInputListener(func(data string) TuiInputListenerResult { return screen.handleViewportInput(data) })
	return screen
}

// implicitDocumentComponent renders the renderer's children (upstream's
// implicitDocument).
type implicitDocumentComponent struct {
	renderer *Renderer
}

func (c *implicitDocumentComponent) Render(width int) []string {
	return c.renderer.Container.Render(width)
}

func (c *implicitDocumentComponent) Invalidate() {
	for _, child := range c.renderer.Children {
		child.Invalidate()
	}
}

func (c *implicitDocumentComponent) HandleMouse(event TuiMouseEvent) *TuiMouseDispatchResult {
	return c.renderer.Container.HandleMouse(event)
}

func (s *AltScreen) mountedRoots() []Component {
	if root := s.layoutRoot.Load(); root != nil {
		return []Component{*root}
	}
	return s.Children
}

func (s *AltScreen) getLayoutRoot() Component {
	if root := s.layoutRoot.Load(); root != nil {
		return *root
	}
	return nil
}

func (s *AltScreen) setLayoutRootValue(component Component) {
	if component == nil {
		s.layoutRoot.Store(nil)
		return
	}
	s.layoutRoot.Store(&component)
}

// ViewportTop returns the primary scroll view's scroll offset.
func (s *AltScreen) ViewportTop() int {
	return s.getPrimaryScrollViewLocked().ScrollTop()
}

// IsFollowingOutput reports whether the primary scroll view follows the end.
func (s *AltScreen) IsFollowingOutput() bool {
	return s.getPrimaryScrollViewLocked().IsFollowingEnd()
}

// GetCopyOnSelect reports whether selecting copies automatically.
func (s *AltScreen) GetCopyOnSelect() bool {
	return s.copyOnSelect
}

// SetCopyOnSelect toggles copy-on-select.
func (s *AltScreen) SetCopyOnSelect(enabled bool) {
	s.copyOnSelect = enabled
}

// HasActiveSelection reports whether the fullscreen viewport has a non-empty
// text selection.
func (s *AltScreen) HasActiveSelection() bool {
	text, ok := s.getActiveSelectionTextLocked()
	return ok && text != ""
}

// CopyActiveSelectionToClipboard copies the active selection.
func (s *AltScreen) CopyActiveSelectionToClipboard() bool {
	text, ok := s.getActiveSelectionTextLocked()
	if !ok || text == "" {
		return false
	}
	return s.copyTextToClipboard(text)
}

// SetLayoutRoot installs the viewport layout root.
func (s *AltScreen) SetLayoutRoot(component Component) {
	if s.getLayoutRoot() == component {
		return
	}
	s.setLayoutRootValue(component)
	s.currentLayout = nil
	s.RequestRender(false)
}

// LayoutRoot returns the installed layout root.
func (s *AltScreen) LayoutRoot() Component {
	return s.getLayoutRoot()
}

// Render renders the layout root (or the children).
func (s *AltScreen) Render(width int) []string {
	if root := s.getLayoutRoot(); root != nil {
		return root.Render(width)
	}
	return s.Renderer.Container.Render(width)
}

func (s *AltScreen) getPrimaryScrollViewLocked() *ScrollView {
	if s.currentLayout != nil && s.currentLayout.PrimaryScrollView != nil {
		return s.currentLayout.PrimaryScrollView
	}
	return s.implicitScrollView
}

func (s *AltScreen) beforeTerminalStart() {
	s.stopSelectionAutoScrollLocked()
	s.selectionPressActive = false
	s.setScrollbarHoverLocked(nil)
	s.scrollbarDrag = nil
	s.flashes.Dispose()
	s.altScreenActive = true
	s.imageProtocol = GetTerminalCapabilities().Images
	// The iterm2 capability swap upstream performs only affects image
	// rendering, which is out of scope (D81).
	s.lastDocument = nil
	s.selectionAnchor = nil
	s.selectionFocus = nil
	s.selectionGranularity = "character"
	s.selectionInitialRange = nil
	s.hasSelectionInitialRange = false
	s.lastClick = nil
	s.pressedURL = ""
	s.hasPressedURL = false
	s.selectionDragged = false
	s.clearComponentMouseGestureLocked()
	s.lastComponentClick = nil
	s.resetRenderStateLocked()

	term := strings.ToLower(os.Getenv("TERM"))
	mouseSequence := enableAllMotionMouse
	if os.Getenv("TMUX") != "" || os.Getenv("ZELLIJ") != "" || os.Getenv("STY") != "" ||
		strings.HasPrefix(term, "tmux") || strings.HasPrefix(term, "screen") {
		mouseSequence = enableButtonMotionMouse
	}
	if s.mouseEnabled {
		s.Terminal.Write(enterAltScreen + disableAutowrap + mouseSequence + "\x1b[2J\x1b[H\x1b[?25l")
	} else {
		s.Terminal.Write(enterAltScreen + disableAutowrap + "\x1b[2J\x1b[H\x1b[?25l")
	}
}

func (s *AltScreen) beforeTerminalStop(options TuiStopOptions) {
	s.closeSearchLocked()
	s.stopSelectionAutoScrollLocked()
	s.selectionPressActive = false
	s.setScrollbarHoverLocked(nil)
	s.scrollbarDrag = nil
	s.clearComponentMouseGestureLocked()
	s.flashes.Dispose()
	if !s.altScreenActive {
		return
	}
	disable := ""
	if s.mouseEnabled {
		disable = disableMouse
	}
	s.Terminal.Write(beginSynchronizedOutput + s.deleteKittyImagesLocked() + disable + enableAutowrap + endSynchronizedOutput)
}

func (s *AltScreen) afterTerminalStop(options TuiStopOptions) {
	if !s.altScreenActive {
		return
	}
	s.altScreenActive = false
	if options.PreserveScreen {
		s.Terminal.Write(beginSynchronizedOutput + exitAltScreen + "\x1b[?25h" + endSynchronizedOutput)
		return
	}
	width := max(1, s.Terminal.Columns())
	documentLines := s.Render(width)
	trimmed := make([]string, 0, len(documentLines))
	for _, line := range documentLines {
		trimmed = append(trimmed, stripZonePrefix(line))
	}
	lines := s.ApplyLineResets(replaceAllStrings(trimmed, CursorMarker, ""))
	for index, line := range lines {
		if !IsImageLine(line) && VisibleWidth(line) > width {
			lines[index] = SliceByColumn(line, 0, width, true)
		}
	}
	s.lastDocument = lines

	var builder strings.Builder
	builder.WriteString(beginSynchronizedOutput + exitAltScreen + disableAutowrap)
	for row := 0; row < len(s.lastDocument); row++ {
		if row > 0 {
			builder.WriteString("\r\n")
		}
		builder.WriteString("\r\x1b[2K" + s.lastDocument[row])
	}
	builder.WriteString("\x1b[0m" + enableAutowrap + "\r\n\x1b[?25h" + endSynchronizedOutput)
	s.Terminal.Write(builder.String())
}

func replaceAllStrings(values []string, old string, replacement string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, strings.ReplaceAll(value, old, replacement))
	}
	return out
}

func (s *AltScreen) deleteKittyImagesLocked() string {
	if s.imageProtocol == "kitty" {
		return deleteAllKittyImages()
	}
	return ""
}

// DeleteAllKittyImages deletes every placed kitty image.
func DeleteAllKittyImages() string { return "\x1b_Ga=d,d=A,q=2\x1b\\" }

// DeleteAllKittyPlacements deletes placements while keeping the image data.
func DeleteAllKittyPlacements() string { return "\x1b_Ga=d,d=z,q=2\x1b\\" }

func deleteAllKittyImages() string     { return DeleteAllKittyImages() }
func deleteAllKittyPlacements() string { return DeleteAllKittyPlacements() }

func (s *AltScreen) resetRenderState() {
	s.resetRenderStateLocked()
}

func (s *AltScreen) resetRenderStateLocked() {
	s.previousScreen = nil
	s.previousScreenWidth = 0
	s.previousCursorValid = false
	s.previousScreenHeight = 0
	s.currentLayout = nil
}

// ScrollBy scrolls the primary view by n lines.
func (s *AltScreen) ScrollBy(lines int) {
	s.getPrimaryScrollViewLocked().ScrollBy(lines)
	s.RequestRender(false)
}

// ScrollToTop scrolls to the start.
func (s *AltScreen) ScrollToTop() {
	s.getPrimaryScrollViewLocked().ScrollToStart()
	s.RequestRender(false)
}

// ScrollToBottom scrolls to the end.
func (s *AltScreen) ScrollToBottom() {
	s.getPrimaryScrollViewLocked().ScrollToEnd()
	s.RequestRender(false)
}

func (s *AltScreen) scrollToPromptLocked(direction int) {
	if s.currentLayout == nil {
		return
	}
	scrollView := s.getPrimaryScrollViewLocked()
	box, ok := GetScrollViewBox(*s.currentLayout, scrollView)
	if !ok || box.ScrollContentLines == nil {
		return
	}
	lines := box.ScrollContentLines
	for row := scrollView.ScrollTop() + direction; row >= 0 && row < len(lines); row += direction {
		if !osc133PromptStart.MatchString(lines[row]) {
			continue
		}
		scrollView.ScrollTo(row)
		s.RequestRender(false)
		return
	}
}

// stripZonePrefix removes the OSC 133 zone markers a message prepends. The
// regexp only ever matches at the start of a line, and this runs over every
// document line on every paint, so the common case costs a literal check.
func stripZonePrefix(line string) string {
	if !strings.HasPrefix(line, osc133ZonePrefixMatch) {
		return line
	}
	return osc133ZonePrefix.ReplaceAllString(line, "")
}

// Flash shows a transient message.
func (s *AltScreen) Flash(message string, durationMS int) {
	s.flashes.Flash(message, durationMS)
}

// ---- Search ----

func (s *AltScreen) toggleSearchLocked() {
	if s.activeSearch != nil {
		s.closeSearchLocked()
		return
	}
	component := NewAltScreenSearchComponent(
		func(query string) { s.updateSearchQuery(query) },
		s.searchNavigationButtonStyle,
	)
	search := &altActiveSearch{
		Component:     component,
		Index:         &AltScreenSearchIndex{},
		AnchorRow:     s.getPrimaryScrollViewLocked().ScrollTop(),
		SelectionMode: "query",
	}
	s.activeSearch = search
	minWidth := 32
	search.Overlay = s.ShowOverlay(component, &OverlayOptions{
		Anchor:       OverlayAnchorTopRight,
		PercentWidth: "40%",
		MinWidth:     &minWidth,
		Margin:       OverlayMargin{Top: 1, Right: 1, Bottom: 1, Left: 1},
	})
}

func (s *AltScreen) closeSearchLocked() {
	search := s.activeSearch
	if search == nil {
		return
	}
	s.activeSearch = nil
	if search.Overlay != nil {
		search.Overlay.Hide()
	}
	s.RequestRender(false)
}

func (s *AltScreen) updateSearchQuery(query string) {
	search := s.activeSearch
	if search == nil || query == search.Query {
		return
	}
	if search.SelectedIndex >= 0 && search.SelectedIndex < len(search.Matches) {
		if len(search.Matches[search.SelectedIndex].Segments) > 0 {
			search.AnchorRow = search.Matches[search.SelectedIndex].Segments[0].Row
		}
	}
	search.Query = query
	search.SelectionMode = "query"
	search.Component.SetResult(-1, 0)
	s.RequestRender(false)
}

func (s *AltScreen) navigateSearchLocked(direction int) {
	search := s.activeSearch
	if search == nil || search.Query == "" {
		return
	}
	if direction < 0 {
		search.SelectionMode = "previous"
	} else {
		search.SelectionMode = "next"
	}
	s.RequestRender(false)
}

func (s *AltScreen) getSearchNavigationDirectionAt(x int, y int) (int, bool) {
	search := s.activeSearch
	if search == nil || search.Overlay == nil {
		return 0, false
	}
	bounds, ok := search.Overlay.GetBounds()
	if !ok {
		return 0, false
	}
	if x < bounds.Col || x >= bounds.Col+bounds.Width || y < bounds.Row || y >= bounds.Row+bounds.Height {
		return 0, false
	}
	return search.Component.GetNavigationDirectionAt(y-bounds.Row, x-bounds.Col)
}

func (s *AltScreen) handleSearchMouseEventLocked(event sgrMouseEvent) bool {
	search := s.activeSearch
	if search == nil {
		return false
	}
	direction, hasDirection := s.getSearchNavigationDirectionAt(event.X, event.Y)
	if search.Component.SetHoveredNavigationDirection(direction, hasDirection) {
		s.RequestRender(false)
	}
	if !hasDirection || event.Release || (event.Button&32) != 0 || (event.Button&3) != 0 {
		return false
	}
	s.navigateSearchLocked(direction)
	return true
}

// refreshSearchLocked updates the search state; it reports whether a re-layout
// is required.
func (s *AltScreen) refreshSearchLocked(layout LayoutFrame) bool {
	search := s.activeSearch
	if search == nil {
		return false
	}
	scrollView := layout.PrimaryScrollView
	if scrollView == nil {
		scrollView = s.implicitScrollView
	}
	box, hasBox := GetScrollViewBox(layout, scrollView)
	var lines []string
	if hasBox {
		lines = box.ScrollContentLines
	}
	if len(lines) == 0 || strings.TrimSpace(search.Query) == "" {
		search.Matches = nil
		search.SelectedIndex = -1
		search.SelectedKey = ""
		search.HasSelectedKey = false
		search.SelectionMode = "retain"
		search.Component.SetResult(-1, 0)
		return false
	}

	shouldRevealSelection := search.SelectionMode != "retain"
	result := search.Index.Search(lines, search.Query)
	matches := result.Matches
	search.Matches = matches
	if !result.Changed && search.SelectionMode == "retain" {
		return false
	}

	exactIndex := -1
	if result.Changed {
		if search.HasSelectedKey {
			for index, match := range matches {
				if GetAltScreenSearchMatchKey(match) == search.SelectedKey {
					exactIndex = index
					break
				}
			}
		}
	} else {
		exactIndex = search.SelectedIndex
	}
	selectedIndex := -1
	if len(matches) > 0 {
		switch search.SelectionMode {
		case "query":
			low, high := 0, len(matches)
			for low < high {
				middle := low + (high-low)/2
				row := 0
				if len(matches[middle].Segments) > 0 {
					row = matches[middle].Segments[0].Row
				}
				if row < search.AnchorRow {
					low = middle + 1
				} else {
					high = middle
				}
			}
			if low < len(matches) {
				selectedIndex = low
			} else {
				selectedIndex = 0
			}
		case "next":
			baseIndex := exactIndex
			if baseIndex < 0 {
				baseIndex = min(search.SelectedIndex, len(matches)-1)
			}
			if baseIndex < 0 {
				selectedIndex = 0
			} else {
				selectedIndex = (baseIndex + 1) % len(matches)
			}
		case "previous":
			baseIndex := exactIndex
			if baseIndex < 0 {
				baseIndex = min(search.SelectedIndex, len(matches)-1)
			}
			if baseIndex < 0 {
				selectedIndex = len(matches) - 1
			} else {
				selectedIndex = (baseIndex - 1 + len(matches)) % len(matches)
			}
		default:
			selectedIndex = exactIndex
			if selectedIndex < 0 {
				selectedIndex = min(max(0, search.SelectedIndex), len(matches)-1)
			}
		}
	}

	search.SelectedIndex = selectedIndex
	if selectedIndex >= 0 {
		search.SelectedKey = GetAltScreenSearchMatchKey(matches[selectedIndex])
		search.HasSelectedKey = true
	} else {
		search.SelectedKey = ""
		search.HasSelectedKey = false
	}
	search.SelectionMode = "retain"
	search.Component.SetResult(selectedIndex, len(matches))
	if !shouldRevealSelection {
		return false
	}

	if selectedIndex < 0 || selectedIndex >= len(matches) || !hasBox || scrollView.ViewportHeight() <= 0 {
		return false
	}
	selected := matches[selectedIndex]
	if len(selected.Segments) == 0 {
		return false
	}
	firstSegment := selected.Segments[0]
	lastSegment := selected.Segments[len(selected.Segments)-1]
	before := scrollView.ScrollTop()
	visibleBottom := before + scrollView.ViewportHeight() - 1
	target := before
	if firstSegment.Row < before || lastSegment.Row > visibleBottom {
		target = firstSegment.Row - scrollView.ViewportHeight()/3
	}
	scrollView.ScrollTo(target, ScrollToRequest{DisableFollow: true})
	return scrollView.ScrollTop() != before
}

func (s *AltScreen) shouldDeferViewportInputToOverlayLocked() bool {
	if !s.IsOverlayFocused() {
		return false
	}
	search := s.activeSearch
	return search == nil || search.Overlay == nil || !search.Overlay.IsFocused()
}

// ---- Input ----

func (s *AltScreen) handleViewportInput(data string) TuiInputListenerResult {

	if data == focusOut {
		hadActiveSelection := s.selectionPressActive
		_, hadNonEmpty := s.getSelectionBoundsLocked()
		s.selectionPressActive = false
		s.stopSelectionAutoScrollLocked()
		s.setScrollbarHoverLocked(nil)
		if s.activeSearch != nil {
			if s.activeSearch.Component.SetHoveredNavigationDirection(0, false) {
				s.RequestRender(false)
			}
		}
		s.scrollbarDrag = nil
		s.pressedURL = ""
		s.hasPressedURL = false
		s.selectionDragged = false
		s.clearComponentMouseGestureLocked()
		s.lastComponentClick = nil
		if hadActiveSelection {
			s.selectionAnchor = nil
			s.selectionFocus = nil
			s.selectionGranularity = "character"
			s.selectionInitialRange = nil
			s.hasSelectionInitialRange = false
			if hadNonEmpty {
				s.RequestRender(false)
			}
		}
		s.lastClick = nil
		return TuiInputListenerResult{Consume: true}
	}
	if data == focusIn {
		return TuiInputListenerResult{Consume: true}
	}

	if wheel, ok := s.parseWheelEvent(data); ok {
		event := s.createMouseEventLocked("wheel", wheel.Button, wheel.X, wheel.Y, wheel.Direction*s.getWheelScrollLines(wheel.Button), 0, false)
		overlayHit, overlayResult := s.DispatchMouseToOverlay(event)
		var result *TuiMouseDispatchResult
		if overlayResult != nil {
			result = overlayResult
		} else if !overlayHit {
			result = s.dispatchMouseToLayoutLocked(event)
		}
		if result != nil {
			if s.applyMouseDispatchResultLocked(event, result) {
				s.RequestRender(false)
			}
			return TuiInputListenerResult{Consume: true}
		}
		if s.shouldDeferViewportInputToOverlayLocked() {
			return TuiInputListenerResult{}
		}
		s.routeWheelLocked(wheel)
		return TuiInputListenerResult{Consume: true}
	}

	if event, ok := parseSgrMouseEvent(data); ok {
		s.handleMouseEventLocked(event)
		return TuiInputListenerResult{Consume: true}
	}
	if s.isMouseSequence(data) {
		return TuiInputListenerResult{Consume: true}
	}

	keybindings := GetKeybindings()
	isRelease := IsKeyRelease(data)
	if keybindings.Matches(data, "tui.altScreen.search") {
		if !isRelease {
			s.toggleSearchLocked()
		}
		return TuiInputListenerResult{Consume: true}
	}
	if s.activeSearch != nil && s.activeSearch.Overlay != nil && s.activeSearch.Overlay.IsFocused() {
		if keybindings.Matches(data, "tui.altScreen.searchNext") {
			if !isRelease {
				s.navigateSearchLocked(1)
			}
			return TuiInputListenerResult{Consume: true}
		}
		if keybindings.Matches(data, "tui.altScreen.searchPrevious") {
			if !isRelease {
				s.navigateSearchLocked(-1)
			}
			return TuiInputListenerResult{Consume: true}
		}
		if keybindings.Matches(data, "tui.altScreen.searchClose") {
			if !isRelease {
				s.closeSearchLocked()
			}
			return TuiInputListenerResult{Consume: true}
		}
	}
	if s.shouldDeferViewportInputToOverlayLocked() {
		return TuiInputListenerResult{}
	}

	type scrollAction struct {
		keybinding string
		handler    func()
	}
	viewportHeight := s.getPrimaryScrollViewLocked().ViewportHeight()
	actions := []scrollAction{
		{"tui.altScreen.pageUp", func() { s.scrollByLocked(-max(1, viewportHeight-pageScrollOverlap)) }},
		{"tui.altScreen.pageDown", func() { s.scrollByLocked(max(1, viewportHeight-pageScrollOverlap)) }},
		{"tui.altScreen.halfPageUp", func() { s.scrollByLocked(-max(1, viewportHeight/2)) }},
		{"tui.altScreen.halfPageDown", func() { s.scrollByLocked(max(1, viewportHeight/2)) }},
		{"tui.altScreen.lineUp", func() { s.scrollByLocked(-1) }},
		{"tui.altScreen.lineDown", func() { s.scrollByLocked(1) }},
		{"tui.altScreen.previousPrompt", func() { s.scrollToPromptLocked(-1) }},
		{"tui.altScreen.nextPrompt", func() { s.scrollToPromptLocked(1) }},
		{"tui.altScreen.top", func() { s.getPrimaryScrollViewLocked().ScrollToStart(); s.RequestRender(false) }},
		{"tui.altScreen.bottom", func() { s.getPrimaryScrollViewLocked().ScrollToEnd(); s.RequestRender(false) }},
	}
	for _, action := range actions {
		if keybindings.Matches(data, action.keybinding) {
			if !isRelease {
				action.handler()
			}
			return TuiInputListenerResult{Consume: true}
		}
	}
	return TuiInputListenerResult{}
}

func (s *AltScreen) scrollByLocked(lines int) {
	s.getPrimaryScrollViewLocked().ScrollBy(lines)
	s.RequestRender(false)
}

func (s *AltScreen) decodeMouseButton(button int) TuiMouseButton {
	switch button & 3 {
	case 0:
		return MouseButtonLeft
	case 1:
		return MouseButtonMiddle
	case 2:
		return MouseButtonRight
	default:
		return MouseButtonNone
	}
}

func (s *AltScreen) createMouseEventLocked(eventType TuiMouseEventType, button int, x int, y int, wheelDelta int, clickCount int, hasClickCount bool) TuiMouseEvent {
	event := TuiMouseEvent{
		Type:    eventType,
		Button:  s.decodeMouseButton(button),
		X:       x,
		Y:       y,
		ScreenX: x,
		ScreenY: y,
		Width:   max(1, s.Terminal.Columns()),
		Height:  max(1, s.Terminal.Rows()),
		Shift:   (button & 4) != 0,
		Alt:     (button & 8) != 0,
		Ctrl:    (button & 16) != 0,
	}
	if eventType == MouseWheel {
		event.Button = MouseButtonNone
		event.WheelDelta = wheelDelta
		event.HasWheel = true
	}
	if hasClickCount {
		event.ClickCount = clickCount
		event.HasClick = true
	}
	return event
}

func (s *AltScreen) dispatchMouseToLayoutLocked(event TuiMouseEvent) *TuiMouseDispatchResult {
	if s.currentLayout == nil {
		return nil
	}
	visited := map[Component]bool{}
	for _, box := range GetLayoutBoxesAt(*s.currentLayout, event.ScreenX, event.ScreenY) {
		if visited[box.Component] {
			continue
		}
		if _, isProvider := box.Component.(LayoutNodeProvider); isProvider {
			if _, isContainer := box.Component.(childrenHolder); isContainer {
				continue
			}
		}
		visited[box.Component] = true
		childEvent := event
		childEvent.X = event.ScreenX - box.Rect.X
		childEvent.Y = event.ScreenY - box.Rect.Y
		childEvent.Width = box.Rect.Width
		childEvent.Height = box.Rect.Height
		if result := DispatchMouseEvent(box.Component, childEvent); result != nil {
			return result
		}
	}
	return nil
}

func (s *AltScreen) applyMouseDispatchResultLocked(event TuiMouseEvent, result *TuiMouseDispatchResult) bool {
	focusTarget := result.Target.Component
	if result.HasFocus && result.FocusTarget != nil {
		focusTarget = result.FocusTarget
	}
	focusTarget = s.ResolveMouseFocusTarget(focusTarget)
	focusChanged := result.Focus && s.GetFocusedComponent() != focusTarget
	if result.Focus {
		s.SetFocus(focusTarget)
	}
	if result.Capture {
		target := result.Target
		s.mouseCapture = &target
	}
	if result.HasRender {
		return result.Render
	}
	return focusChanged || event.Type == MousePress || event.Type == MouseClick ||
		event.Type == MouseDrag || event.Type == MouseWheel
}

func (s *AltScreen) dispatchMouseToTargetLocked(event TuiMouseEvent, target TuiMouseDispatchTarget) *TuiMouseDispatchResult {
	return DispatchMouseEvent(target.Component, RetargetMouseEvent(event, target))
}

func (s *AltScreen) getComponentClickCount(target TuiMouseDispatchTarget, x int, y int) int {
	now := time.Now().UnixMilli()
	count := 1
	if s.lastComponentClick != nil &&
		now-s.lastComponentClick.Timestamp <= doubleClickIntervalMS &&
		s.lastComponentClick.Component == target.Component &&
		s.lastComponentClick.X == x && s.lastComponentClick.Y == y {
		count = (s.lastComponentClick.Count % 3) + 1
	}
	s.lastComponentClick = &struct {
		Timestamp int64
		Count     int
		Component Component
		X, Y      int
	}{Timestamp: now, Count: count, Component: target.Component, X: x, Y: y}
	return count
}

func (s *AltScreen) clearTextSelectionLocked() {
	s.stopSelectionAutoScrollLocked()
	s.selectionPressActive = false
	s.selectionAnchor = nil
	s.selectionFocus = nil
	s.selectionGranularity = "character"
	s.selectionInitialRange = nil
	s.hasSelectionInitialRange = false
	s.pressedURL = ""
	s.hasPressedURL = false
	s.selectionDragged = false
}

func (s *AltScreen) clearComponentMouseGestureLocked() {
	s.mouseCapture = nil
	s.mousePressTarget = nil
	s.mousePressPoint = nil
	s.mousePressMoved = false
}

func (s *AltScreen) handleMouseEventLocked(raw sgrMouseEvent) {
	isMotion := (raw.Button & 32) != 0
	eventType := MousePress
	switch {
	case raw.Release:
		eventType = MouseRelease
	case isMotion:
		if s.decodeMouseButton(raw.Button) == MouseButtonNone {
			eventType = MouseMove
		} else {
			eventType = MouseDrag
		}
	}
	event := s.createMouseEventLocked(eventType, raw.Button, raw.X, raw.Y, 0, 0, false)

	if s.mouseCapture != nil || s.mousePressTarget != nil {
		target := s.mouseCapture
		if target == nil {
			target = s.mousePressTarget
		}
		if s.mousePressPoint != nil && (raw.X != s.mousePressPoint.X || raw.Y != s.mousePressPoint.Y) {
			s.mousePressMoved = true
			s.lastComponentClick = nil
		}
		render := false
		if targetResult := s.dispatchMouseToTargetLocked(event, *target); targetResult != nil {
			render = s.applyMouseDispatchResultLocked(event, targetResult)
		}
		if raw.Release {
			if !s.mousePressMoved && s.mousePressPoint != nil && s.mousePressPoint.X == raw.X && s.mousePressPoint.Y == raw.Y {
				clickEvent := s.createMouseEventLocked(MouseClick, raw.Button, raw.X, raw.Y, 0,
					s.getComponentClickCount(*target, raw.X, raw.Y), true)
				if clickResult := s.dispatchMouseToTargetLocked(clickEvent, *target); clickResult != nil {
					render = s.applyMouseDispatchResultLocked(clickEvent, clickResult) || render
				}
			}
			s.clearComponentMouseGestureLocked()
		}
		if render {
			s.RequestRender(false)
		}
		return
	}

	if s.handleSearchMouseEventLocked(raw) {
		return
	}

	overlayHit, overlayResult := s.DispatchMouseToOverlay(event)
	if !overlayHit {
		if s.handleScrollToEndIndicatorMouseEventLocked(raw) {
			return
		}
		scrollbarHandled := s.handleScrollbarMouseEventLocked(raw)
		if s.scrollbarDrag == nil {
			s.updateScrollbarHoverLocked(raw.X, raw.Y)
		}
		if scrollbarHandled {
			return
		}
	} else {
		s.setScrollbarHoverLocked(nil)
	}

	var result *TuiMouseDispatchResult
	if overlayResult != nil {
		result = overlayResult
	} else if !overlayHit {
		result = s.dispatchMouseToLayoutLocked(event)
	}
	if result != nil {
		render := s.applyMouseDispatchResultLocked(event, result)
		if eventType == MousePress {
			s.clearTextSelectionLocked()
			target := result.Target
			s.mousePressTarget = &target
			s.mousePressPoint = &struct{ X, Y int }{raw.X, raw.Y}
			s.mousePressMoved = false
		}
		if render {
			s.RequestRender(false)
		}
		return
	}

	if s.handleRightClickPasteLocked(raw) {
		return
	}
	s.handleSelectionMouseEventLocked(raw)
}

func (s *AltScreen) parseWheelEvent(data string) (altWheelEvent, bool) {
	if match := wheelSgrRegex.FindStringSubmatch(data); match != nil {
		button := atoiSafe(match[1])
		if button&64 == 0 {
			return altWheelEvent{}, false
		}
		direction := button & 3
		if direction != 0 && direction != 1 {
			return altWheelEvent{}, false
		}
		dir := 1
		if direction == 0 {
			dir = -1
		}
		return altWheelEvent{Direction: dir, X: atoiSafe(match[2]) - 1, Y: atoiSafe(match[3]) - 1, Button: button}, true
	}
	if len(data) == 6 && strings.HasPrefix(data, "\x1b[M") {
		button := int(data[3]) - 32
		if button&64 == 0 {
			return altWheelEvent{}, false
		}
		direction := button & 3
		if direction != 0 && direction != 1 {
			return altWheelEvent{}, false
		}
		dir := 1
		if direction == 0 {
			dir = -1
		}
		return altWheelEvent{Direction: dir, X: int(data[4]) - 33, Y: int(data[5]) - 33, Button: button}, true
	}
	return altWheelEvent{}, false
}

func (s *AltScreen) getWheelScrollLines(button int) int {
	// SGR mouse button codes use bit 3 (value 8) for the Alt modifier.
	if button&8 != 0 {
		return s.wheelScrollLines * altWheelScrollMultiplier
	}
	return s.wheelScrollLines
}

func (s *AltScreen) routeWheelLocked(event altWheelEvent) {
	remaining := event.Direction * s.getWheelScrollLines(event.Button)
	seen := map[*ScrollView]bool{}
	if s.currentLayout != nil {
		for _, scrollView := range GetScrollViewsAt(*s.currentLayout, event.X, event.Y) {
			seen[scrollView] = true
			remaining = scrollView.ScrollBy(remaining)
			if remaining == 0 || scrollView.Overscroll() == "contain" {
				break
			}
		}
	}
	primary := s.getPrimaryScrollViewLocked()
	if remaining != 0 && !seen[primary] {
		primary.ScrollBy(remaining)
	}
	s.updateScrollbarHoverLocked(event.X, event.Y)
	s.RequestRender(false)
}

func parseSgrMouseEvent(data string) (sgrMouseEvent, bool) {
	match := altSgrMouseRegex.FindStringSubmatch(data)
	if match == nil {
		return sgrMouseEvent{}, false
	}
	return sgrMouseEvent{
		Button:  atoiSafe(match[1]),
		X:       atoiSafe(match[2]) - 1,
		Y:       atoiSafe(match[3]) - 1,
		Release: match[4] == "m",
	}, true
}

func (s *AltScreen) handleRightClickPasteLocked(event sgrMouseEvent) bool {
	if s.onRightClickPaste == nil || !isWindows() {
		return false
	}
	if strings.ToLower(os.Getenv("TERM_PROGRAM")) == "vscode" {
		return false
	}
	if event.Release || event.Button != 2 {
		return false
	}
	func() {
		defer func() { _ = recover() }()
		s.onRightClickPaste()
	}()
	return true
}

func (s *AltScreen) handleScrollToEndIndicatorMouseEventLocked(event sgrMouseEvent) bool {
	rect := s.scrollToEndRect
	if rect == nil || event.Release || (event.Button&32) != 0 || (event.Button&3) != 0 {
		return false
	}
	if event.Y != rect.Row || event.X < rect.Column || event.X >= rect.Column+rect.Width {
		return false
	}
	s.getPrimaryScrollViewLocked().ScrollToEnd()
	s.RequestRender(false)
	return true
}

func (s *AltScreen) getScrollbarTargetAtLocked(x int, y int, includeHiddenAuto bool) (altScrollbarTarget, bool) {
	if s.HasOverlay() || s.currentLayout == nil {
		return altScrollbarTarget{}, false
	}
	for _, scrollView := range GetScrollViewsAt(*s.currentLayout, x, y) {
		box, ok := GetScrollViewBox(*s.currentLayout, scrollView)
		if !ok {
			continue
		}
		geometry, ok := GetScrollbarGeometry(box, includeHiddenAuto)
		if !ok {
			continue
		}
		if x == geometry.Column && y >= geometry.TrackTop && y < geometry.TrackTop+geometry.TrackHeight {
			return altScrollbarTarget{ScrollView: scrollView, Geometry: geometry}, true
		}
	}
	return altScrollbarTarget{}, false
}

func (s *AltScreen) setScrollbarHoverLocked(scrollView *ScrollView) {
	if scrollView == s.scrollbarHover {
		return
	}
	if s.scrollbarHover != nil {
		s.scrollbarHover.SetScrollbarActive(false)
	}
	s.scrollbarHover = scrollView
	if s.scrollbarHover != nil {
		s.scrollbarHover.SetScrollbarActive(true)
	}
}

func (s *AltScreen) updateScrollbarHoverLocked(x int, y int) {
	if target, ok := s.getScrollbarTargetAtLocked(x, y, true); ok {
		s.setScrollbarHoverLocked(target.ScrollView)
		return
	}
	s.setScrollbarHoverLocked(nil)
}

func (s *AltScreen) scrollScrollbarToPointerLocked(scrollView *ScrollView, geometry ScrollbarGeometry, pointerY int, grabOffset int) {
	maxThumbOffset := geometry.TrackHeight - geometry.ThumbHeight
	thumbOffset := max(0, min(maxThumbOffset, pointerY-geometry.TrackTop-grabOffset))
	scrollTop := 0
	if maxThumbOffset != 0 {
		scrollTop = int(float64(thumbOffset)/float64(maxThumbOffset)*float64(geometry.MaxScrollTop) + 0.5)
	}
	scrollView.ScrollTo(scrollTop)
}

func (s *AltScreen) handleScrollbarMouseEventLocked(event sgrMouseEvent) bool {
	if s.scrollbarDrag != nil {
		if event.Release {
			s.scrollbarDrag = nil
			return true
		}
		if s.currentLayout != nil {
			if box, ok := GetScrollViewBox(*s.currentLayout, s.scrollbarDrag.ScrollView); ok {
				if geometry, ok := GetScrollbarGeometry(box, false); ok {
					s.scrollScrollbarToPointerLocked(s.scrollbarDrag.ScrollView, geometry, event.Y, s.scrollbarDrag.GrabOffset)
				}
			}
		}
		return true
	}

	if event.Release || (event.Button&32) != 0 || (event.Button&3) != 0 {
		return false
	}
	target, ok := s.getScrollbarTargetAtLocked(event.X, event.Y, false)
	if !ok {
		return false
	}
	s.stopSelectionAutoScrollLocked()
	s.selectionPressActive = false
	s.selectionAnchor = nil
	s.selectionFocus = nil
	s.selectionGranularity = "character"
	s.selectionInitialRange = nil
	s.hasSelectionInitialRange = false
	s.lastClick = nil
	s.pressedURL = ""
	s.hasPressedURL = false
	s.selectionDragged = false
	s.setScrollbarHoverLocked(target.ScrollView)
	onThumb := event.Y >= target.Geometry.ThumbTop && event.Y < target.Geometry.ThumbTop+target.Geometry.ThumbHeight
	grabOffset := target.Geometry.ThumbHeight / 2
	if onThumb {
		grabOffset = event.Y - target.Geometry.ThumbTop
	} else {
		s.scrollScrollbarToPointerLocked(target.ScrollView, target.Geometry, event.Y, grabOffset)
	}
	s.scrollbarDrag = &altScrollbarDrag{ScrollView: target.ScrollView, GrabOffset: grabOffset}
	return true
}

// ---- Selection ----

func (s *AltScreen) getScrollSelectionPointLocked(scrollView *ScrollView, x int, y int) (altSelectionPoint, bool) {
	if s.currentLayout == nil {
		return altSelectionPoint{}, false
	}
	box, ok := GetScrollViewBox(*s.currentLayout, scrollView)
	if !ok || box.Rect.Height <= 0 || box.Clip.Height <= 0 {
		return altSelectionPoint{}, false
	}
	visibleTop := max(0, max(box.Rect.Y, box.Clip.Y))
	visibleBottom := min(s.Terminal.Rows()-1, min(box.Rect.Y+box.Rect.Height-1, box.Clip.Y+box.Clip.Height-1))
	if visibleBottom < visibleTop {
		return altSelectionPoint{}, false
	}
	pointerRow := max(visibleTop, min(visibleBottom, y))
	contentLines := 1
	if box.ScrollContentLines != nil {
		contentLines = len(box.ScrollContentLines)
	}
	maxContentRow := max(0, contentLines-1)
	return altSelectionPoint{
		Row:        max(0, min(maxContentRow, scrollView.ScrollTop()+pointerRow-box.Rect.Y)),
		Col:        max(0, min(box.Rect.Width-1, x-box.Rect.X)),
		ScrollView: scrollView,
	}, true
}

func (s *AltScreen) getSelectionPointLocked(event sgrMouseEvent, scrollView *ScrollView) altSelectionPoint {
	if scrollView != nil {
		if point, ok := s.getScrollSelectionPointLocked(scrollView, event.X, event.Y); ok {
			return point
		}
	}
	return altSelectionPoint{
		Row: max(0, min(s.Terminal.Rows()-1, event.Y)),
		Col: max(0, min(s.Terminal.Columns()-1, event.X)),
	}
}

func (s *AltScreen) getSelectionSourceLineLocked(point altSelectionPoint) string {
	if point.ScrollView != nil && s.currentLayout != nil {
		if box, ok := GetScrollViewBox(*s.currentLayout, point.ScrollView); ok && box.ScrollContentLines != nil {
			if point.Row < len(box.ScrollContentLines) {
				return box.ScrollContentLines[point.Row]
			}
			return ""
		}
	}
	if point.Row >= 0 && point.Row < len(s.previousScreen) {
		return s.previousScreen[point.Row]
	}
	return ""
}

func (s *AltScreen) getWordSelectionLocked(point altSelectionPoint) (altSelectionRange, bool) {
	line := StripTerminalSequences(s.getSelectionSourceLineLocked(point))
	type wordSegment struct {
		start, end int
		selectable bool
		joiner     bool
	}
	var segments []wordSegment
	start := 0
	for _, segment := range WordSegments(line) {
		end := start + VisibleWidth(segment.Segment)
		joiner := terminalWordSelectionJoiners[segment.Segment]
		segments = append(segments, wordSegment{start: start, end: end, selectable: segment.IsWordLike || joiner, joiner: joiner})
		start = end
	}
	clickedIndex := -1
	for index, segment := range segments {
		if point.Col >= segment.start && point.Col < segment.end {
			clickedIndex = index
			break
		}
	}
	if clickedIndex < 0 {
		return altSelectionRange{}, false
	}
	canJoin := func(left wordSegment, right wordSegment) bool {
		return left.selectable && right.selectable && (left.joiner || right.joiner)
	}
	selectionStart := segments[clickedIndex].start
	selectionEnd := segments[clickedIndex].end
	for index := clickedIndex; index > 0 && canJoin(segments[index-1], segments[index]); index-- {
		selectionStart = segments[index-1].start
	}
	for index := clickedIndex; index < len(segments)-1 && canJoin(segments[index], segments[index+1]); index++ {
		selectionEnd = segments[index+1].end
	}
	return altSelectionRange{
		Start: altSelectionPoint{Row: point.Row, Col: selectionStart, ScrollView: point.ScrollView},
		End:   altSelectionPoint{Row: point.Row, Col: selectionEnd, ScrollView: point.ScrollView, Boundary: true},
	}, true
}

func (s *AltScreen) getLineSelectionLocked(point altSelectionPoint) altSelectionRange {
	return altSelectionRange{
		Start: altSelectionPoint{Row: point.Row, Col: 0, ScrollView: point.ScrollView},
		End: altSelectionPoint{
			Row: point.Row, Col: VisibleWidth(s.getSelectionSourceLineLocked(point)),
			ScrollView: point.ScrollView, Boundary: true,
		},
	}
}

func (s *AltScreen) updateSelectionFocusLocked(point altSelectionPoint) {
	if s.selectionGranularity == "character" || !s.hasSelectionInitialRange {
		s.selectionFocus = &point
		return
	}
	var selectionRange altSelectionRange
	var ok bool
	if s.selectionGranularity == "word" {
		selectionRange, ok = s.getWordSelectionLocked(point)
	} else {
		selectionRange, ok = s.getLineSelectionLocked(point), true
	}
	if !ok {
		return
	}
	initial := *s.selectionInitialRange
	targetBeforeInitial := selectionRange.Start.Row < initial.Start.Row ||
		(selectionRange.Start.Row == initial.Start.Row && selectionRange.Start.Col < initial.Start.Col)
	if targetBeforeInitial {
		anchor := initial.End
		focus := selectionRange.Start
		s.selectionAnchor = &anchor
		s.selectionFocus = &focus
		return
	}
	anchor := initial.Start
	focus := selectionRange.End
	s.selectionAnchor = &anchor
	s.selectionFocus = &focus
}

func (s *AltScreen) getClickCountLocked(point altSelectionPoint, word altSelectionRange, hasWord bool) int {
	now := time.Now().UnixMilli()
	count := 1
	if hasWord && s.lastClick != nil &&
		now-s.lastClick.Timestamp <= doubleClickIntervalMS &&
		s.lastClick.Row == point.Row &&
		s.lastClick.ScrollView == point.ScrollView &&
		s.lastClick.WordStart == word.Start.Col &&
		s.lastClick.WordEnd == word.End.Col {
		count = (s.lastClick.Count % 3) + 1
	}
	if hasWord {
		s.lastClick = &altClickTarget{
			Timestamp: now, Count: count, Row: point.Row, ScrollView: point.ScrollView,
			WordStart: word.Start.Col, WordEnd: word.End.Col,
		}
	} else {
		s.lastClick = nil
	}
	return count
}

func (s *AltScreen) updateSelectionAutoScrollLocked(event sgrMouseEvent) {
	if s.selectionAnchor == nil || s.selectionAnchor.ScrollView == nil || s.currentLayout == nil {
		s.stopSelectionAutoScrollLocked()
		return
	}
	scrollView := s.selectionAnchor.ScrollView
	box, ok := GetScrollViewBox(*s.currentLayout, scrollView)
	if !ok || box.Rect.Height <= 0 || box.Clip.Height <= 0 {
		s.stopSelectionAutoScrollLocked()
		return
	}
	visibleTop := max(0, max(box.Rect.Y, box.Clip.Y))
	visibleBottom := min(s.Terminal.Rows()-1, min(box.Rect.Y+box.Rect.Height-1, box.Clip.Y+box.Clip.Height-1))
	pointer := struct{ X, Y int }{event.X, event.Y}
	s.selectionDragPointer = &pointer
	switch {
	case event.Y <= visibleTop:
		s.selectionAutoScrollDirection = -1
	case event.Y >= visibleBottom:
		s.selectionAutoScrollDirection = 1
	default:
		s.selectionAutoScrollDirection = 0
	}
	if s.selectionAutoScrollDirection == 0 {
		s.stopSelectionAutoScrollLocked()
		return
	}
	// No ticker: AnimationFrame steps the scroll from the render clock while a
	// drag holds the pointer at an edge (stage 4).
	s.lastSelectionAutoScrollAt = time.Time{}
}

// selectionAutoScrollIntervalMS is the legacy ticker period, kept so the
// clock-driven steps match the previous cadence.
const selectionAutoScrollIntervalMS = 50

// AnimationFrame implements Animator: a held selection drag scrolls at the
// legacy cadence.
func (s *AltScreen) AnimationFrame(now time.Time) (bool, time.Duration) {
	interval := selectionAutoScrollIntervalMS * time.Millisecond
	if s.selectionAutoScrollDirection == 0 || s.selectionAnchor == nil || s.selectionDragPointer == nil {
		return false, 0
	}
	if s.lastSelectionAutoScrollAt.IsZero() || now.Sub(s.lastSelectionAutoScrollAt) >= interval {
		s.lastSelectionAutoScrollAt = now
		s.autoScrollSelection()
	}
	if s.selectionAutoScrollDirection == 0 {
		return false, 0
	}
	return true, interval
}

func (s *AltScreen) autoScrollSelection() {
	if s.selectionAnchor == nil || s.selectionAnchor.ScrollView == nil ||
		s.selectionDragPointer == nil || s.selectionAutoScrollDirection == 0 {
		s.stopSelectionAutoScrollLocked()
		return
	}
	scrollView := s.selectionAnchor.ScrollView
	direction := s.selectionAutoScrollDirection
	pointer := *s.selectionDragPointer
	remaining := scrollView.ScrollBy(direction)
	if remaining == direction {
		s.stopSelectionAutoScrollLocked()
		return
	}
	if point, ok := s.getScrollSelectionPointLocked(scrollView, pointer.X, pointer.Y); ok {
		s.updateSelectionFocusLocked(point)
	}
	s.RequestRender(false)
}

func (s *AltScreen) stopSelectionAutoScrollLocked() {
	s.selectionAutoScrollDirection = 0
	s.selectionDragPointer = nil
}

func (s *AltScreen) handleSelectionMouseEventLocked(event sgrMouseEvent) {
	button := event.Button & 3
	if button != 0 && !(event.Release && button == 3) {
		return
	}
	var anchorScrollView *ScrollView
	if s.selectionAnchor != nil {
		anchorScrollView = s.selectionAnchor.ScrollView
	}
	point := s.getSelectionPointLocked(event, anchorScrollView)
	if event.Release {
		if !s.selectionPressActive {
			return
		}
		s.selectionPressActive = false
		s.stopSelectionAutoScrollLocked()
		if s.selectionAnchor == nil {
			return
		}
		s.updateSelectionFocusLocked(point)
		isClick := !s.selectionDragged &&
			s.selectionAnchor.ScrollView == point.ScrollView &&
			s.selectionAnchor.Row == point.Row &&
			s.selectionAnchor.Col == point.Col
		clickedURL := ""
		if isClick && s.hasPressedURL {
			clickedURL = s.pressedURL
		}
		s.pressedURL = ""
		s.hasPressedURL = false
		if clickedURL != "" && s.openURL != nil {
			s.selectionAnchor = nil
			s.selectionFocus = nil
			func() {
				defer func() { _ = recover() }()
				s.openURL(clickedURL)
			}()
			s.RequestRender(false)
			return
		}
		if isClick {
			clickCount := 1
			if s.lastClick != nil {
				clickCount = s.lastClick.Count
			}
			clickEvent := s.createMouseEventLocked(MouseClick, event.Button, event.X, event.Y, 0, clickCount, true)
			overlayHit, overlayResult := s.DispatchMouseToOverlay(clickEvent)
			var result *TuiMouseDispatchResult
			if overlayResult != nil {
				result = overlayResult
			} else if !overlayHit {
				result = s.dispatchMouseToLayoutLocked(clickEvent)
			}
			if result != nil {
				render := s.applyMouseDispatchResultLocked(clickEvent, result)
				s.clearTextSelectionLocked()
				if render {
					s.RequestRender(false)
				}
				return
			}
		}
		if s.copyOnSelect {
			text, ok := s.getActiveSelectionTextLocked()
			if ok && text != "" {
				// An injected clipboard implementation may be asynchronous;
				// the OSC 52 fallback is synchronous (upstream fires and
				// forgets the promise).
				if s.copySelection != nil {
					go s.copyTextToClipboard(text)
				} else {
					s.copyTextToClipboard(text)
				}
			}
		}
		s.RequestRender(false)
		return
	}

	if (event.Button & 32) != 0 {
		if !s.selectionPressActive || s.selectionAnchor == nil {
			return
		}
		s.selectionDragged = true
		s.lastClick = nil
		s.pressedURL = ""
		s.hasPressedURL = false
		s.updateSelectionFocusLocked(point)
		s.updateSelectionAutoScrollLocked(event)
		s.RequestRender(false)
		return
	}

	s.stopSelectionAutoScrollLocked()
	s.selectionPressActive = true
	var scrollView *ScrollView
	if !s.HasOverlay() && s.currentLayout != nil {
		if views := GetScrollViewsAt(*s.currentLayout, event.X, event.Y); len(views) > 0 {
			scrollView = views[0]
		}
	}
	anchor := s.getSelectionPointLocked(event, scrollView)
	word, hasWord := s.getWordSelectionLocked(anchor)
	clickCount := s.getClickCountLocked(anchor, word, hasWord)
	var selectionRange altSelectionRange
	hasRange := false
	if clickCount == 2 && hasWord {
		selectionRange = word
		hasRange = true
		s.selectionGranularity = "word"
	} else if clickCount == 3 {
		selectionRange = s.getLineSelectionLocked(anchor)
		hasRange = true
		s.selectionGranularity = "line"
	} else {
		s.selectionGranularity = "character"
	}
	if hasRange {
		start := selectionRange.Start
		end := selectionRange.End
		s.selectionInitialRange = &selectionRange
		s.hasSelectionInitialRange = true
		s.selectionAnchor = &start
		s.selectionFocus = &end
	} else {
		s.selectionInitialRange = nil
		s.hasSelectionInitialRange = false
		s.selectionAnchor = &anchor
		s.selectionFocus = &anchor
	}
	s.selectionDragged = false
	s.pressedURL = ""
	s.hasPressedURL = false
	if !hasRange {
		row := max(0, min(s.Terminal.Rows()-1, event.Y))
		col := max(0, min(s.Terminal.Columns()-1, event.X))
		line := ""
		if row < len(s.previousScreen) {
			line = s.previousScreen[row]
		}
		if url, ok := GetOsc8LinkAtColumn(line, col); ok && url != "" {
			s.pressedURL = url
			s.hasPressedURL = true
		}
	}
	s.RequestRender(false)
}

func (s *AltScreen) getSelectionBoundsLocked() (altSelectionRange, bool) {
	if s.selectionAnchor == nil || s.selectionFocus == nil {
		return altSelectionRange{}, false
	}
	if s.selectionAnchor.ScrollView != s.selectionFocus.ScrollView {
		return altSelectionRange{}, false
	}
	if s.selectionAnchor.Row == s.selectionFocus.Row && s.selectionAnchor.Col == s.selectionFocus.Col {
		return altSelectionRange{}, false
	}
	anchorBeforeFocus := s.selectionAnchor.Row < s.selectionFocus.Row ||
		(s.selectionAnchor.Row == s.selectionFocus.Row && s.selectionAnchor.Col < s.selectionFocus.Col)
	if anchorBeforeFocus {
		return altSelectionRange{Start: *s.selectionAnchor, End: *s.selectionFocus}, true
	}
	return altSelectionRange{Start: *s.selectionFocus, End: *s.selectionAnchor}, true
}

func (s *AltScreen) getSelectionColumnsLocked(line string, row int, selection altSelectionRange, minColumn int, maxColumn int) (int, int) {
	lineWidth := VisibleWidth(line)
	start := max(0, minColumn)
	end := min(lineWidth, maxColumn)
	if row == selection.Start.Row {
		if cellRange, ok := GetGraphemeCellRange(line, selection.Start.Col); ok {
			start = cellRange.Start
		} else {
			start = min(selection.Start.Col, lineWidth)
		}
	}
	if row == selection.End.Row {
		if selection.End.Boundary {
			end = min(selection.End.Col, lineWidth)
		} else if cellRange, ok := GetGraphemeCellRange(line, selection.End.Col); ok {
			end = cellRange.End
		} else {
			end = min(selection.End.Col+1, lineWidth)
		}
	}
	return max(minColumn, start), min(maxColumn, end)
}

func (s *AltScreen) getActiveSelectionTextLocked() (string, bool) {
	selection, ok := s.getSelectionBoundsLocked()
	if !ok {
		return "", false
	}
	sourceLines := s.previousScreen
	if selection.Start.ScrollView != nil {
		if s.currentLayout == nil {
			return "", false
		}
		box, ok := GetScrollViewBox(*s.currentLayout, selection.Start.ScrollView)
		if !ok || box.ScrollContentLines == nil {
			return "", false
		}
		sourceLines = box.ScrollContentLines
	}
	var lines []string
	for row := selection.Start.Row; row <= selection.End.Row; row++ {
		line := ""
		if row >= 0 && row < len(sourceLines) {
			line = sourceLines[row]
		}
		start, end := s.getSelectionColumnsLocked(line, row, selection, 0, VisibleWidth(line))
		selected := SliceByColumn(line, start, max(0, end-start), true)
		lines = append(lines, strings.TrimRight(StripTerminalSequences(selected), " \t"))
	}
	text := strings.Join(lines, "\n")
	if text == "" {
		return "", false
	}
	return text, true
}

func (s *AltScreen) copyTextToClipboard(text string) bool {
	if s.copySelection != nil {
		handled, ok, message := s.copySelection(text)
		if handled {
			if ok {
				s.Flash("Copied!", 0)
			} else if message != "" {
				s.Flash(message, copyErrorFlashDurationMS)
			} else {
				s.Flash("Copy failed", copyErrorFlashDurationMS)
			}
			return ok
		}
	}
	s.Terminal.Write("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07")
	s.Flash("Copied!", 0)
	return true
}

// ---- Rendering ----

func (s *AltScreen) applySearchTextHighlight(text string, current bool) string {
	style := s.searchMatchStyle
	if current {
		style = s.searchCurrentMatchStyle
	}
	var builder strings.Builder
	plainStart := 0
	index := 0
	for index < len(text) {
		code, length := ExtractANSICode(text, index)
		if length == 0 {
			index++
			continue
		}
		if index > plainStart {
			builder.WriteString(style(text[plainStart:index]))
		}
		builder.WriteString(code)
		index += length
		plainStart = index
	}
	if plainStart < len(text) {
		builder.WriteString(style(text[plainStart:]))
	}
	return builder.String()
}

func (s *AltScreen) applySearchHighlights(screen []string, layout LayoutFrame) []string {
	search := s.activeSearch
	if search == nil || search.SelectedIndex < 0 || len(search.Matches) == 0 {
		return screen
	}
	scrollView := layout.PrimaryScrollView
	if scrollView == nil {
		scrollView = s.implicitScrollView
	}
	box, ok := GetScrollViewBox(layout, scrollView)
	if !ok {
		return screen
	}

	rangesByRow := map[int][]altSearchHighlightRange{}
	var scrollbarColumn int
	hasScrollbarColumn := false
	if geometry, ok := GetScrollbarGeometry(box, false); ok {
		scrollbarColumn = geometry.Column
		hasScrollbarColumn = true
	}
	minRow := max(0, max(box.Rect.Y, box.Clip.Y))
	maxRow := min(len(screen), min(box.Rect.Y+box.Rect.Height, box.Clip.Y+box.Clip.Height))
	minColumn := max(0, max(box.Rect.X, box.Clip.X))
	maxColumn := min(s.Terminal.Columns(), min(box.Rect.X+box.Rect.Width, box.Clip.X+box.Clip.Width))
	if hasScrollbarColumn && scrollbarColumn < maxColumn {
		maxColumn = scrollbarColumn
	}
	minContentRow := scrollView.ScrollTop() + minRow - box.Rect.Y
	maxContentRow := scrollView.ScrollTop() + maxRow - box.Rect.Y - 1

	low, high := 0, len(search.Matches)
	for low < high {
		middle := low + (high-low)/2
		match := search.Matches[middle]
		lastRow := -1
		if len(match.Segments) > 0 {
			lastRow = match.Segments[len(match.Segments)-1].Row
		}
		if lastRow < minContentRow {
			low = middle + 1
		} else {
			high = middle
		}
	}
	for matchIndex := low; matchIndex < len(search.Matches); matchIndex++ {
		match := search.Matches[matchIndex]
		firstRow := 0
		if len(match.Segments) > 0 {
			firstRow = match.Segments[0].Row
		}
		if firstRow > maxContentRow {
			break
		}
		for _, segment := range match.Segments {
			row := box.Rect.Y + segment.Row - scrollView.ScrollTop()
			if row < minRow || row >= maxRow {
				continue
			}
			startCol := max(minColumn, box.Rect.X+segment.StartCol)
			endCol := min(maxColumn, box.Rect.X+segment.EndCol)
			if endCol <= startCol {
				continue
			}
			rangesByRow[row] = append(rangesByRow[row], altSearchHighlightRange{
				StartCol: startCol, EndCol: endCol, Current: matchIndex == search.SelectedIndex,
			})
		}
	}

	result := append([]string(nil), screen...)
	for row, ranges := range rangesByRow {
		line := result[row]
		if IsImageLine(line) {
			continue
		}
		lineWidth := VisibleWidth(line)
		sortSearchRangesDescending(ranges)
		for _, searchRange := range ranges {
			startCol := min(searchRange.StartCol, lineWidth)
			endCol := min(searchRange.EndCol, lineWidth)
			if endCol <= startCol {
				continue
			}
			before := SliceByColumn(line, 0, startCol, true)
			highlighted := SliceByColumn(line, startCol, endCol-startCol, true)
			after := SliceByColumn(line, endCol, max(0, lineWidth-endCol), true)
			line = before + s.applySearchTextHighlight(highlighted, searchRange.Current) + after
		}
		result[row] = line
	}
	return result
}

func sortSearchRangesDescending(ranges []altSearchHighlightRange) {
	for i := 1; i < len(ranges); i++ {
		for j := i; j > 0 && ranges[j].StartCol > ranges[j-1].StartCol; j-- {
			ranges[j], ranges[j-1] = ranges[j-1], ranges[j]
		}
	}
}

func (s *AltScreen) applySelectionHighlight(text string) string {
	var builder strings.Builder
	builder.WriteString("\x1b[7m")
	index := 0
	for index < len(text) {
		code, length := ExtractANSICode(text, index)
		if length == 0 {
			builder.WriteByte(text[index])
			index++
			continue
		}
		builder.WriteString(code)
		if strings.HasSuffix(code, "m") {
			builder.WriteString("\x1b[7m")
		}
		index += length
	}
	builder.WriteString("\x1b[27m")
	return builder.String()
}

func (s *AltScreen) applySelection(screen []string, layout *LayoutFrame) []string {
	selection, ok := s.getSelectionBoundsLocked()
	if !ok {
		return screen
	}
	screenSelection := selection
	minRow := 0
	maxRow := len(screen) - 1
	minColumn := 0
	maxColumn := s.Terminal.Columns()
	if selection.Start.ScrollView != nil {
		if layout == nil {
			return screen
		}
		box, ok := GetScrollViewBox(*layout, selection.Start.ScrollView)
		if !ok {
			return screen
		}
		minRow = max(0, max(box.Rect.Y, box.Clip.Y))
		maxRow = min(len(screen)-1, min(box.Rect.Y+box.Rect.Height-1, box.Clip.Y+box.Clip.Height-1))
		minColumn = max(0, max(box.Rect.X, box.Clip.X))
		maxColumn = min(s.Terminal.Columns(), min(box.Rect.X+box.Rect.Width, box.Clip.X+box.Clip.Width))
		scrollTop := selection.Start.ScrollView.ScrollTop()
		screenSelection = altSelectionRange{
			Start: altSelectionPoint{
				Row: box.Rect.Y + selection.Start.Row - scrollTop,
				Col: box.Rect.X + selection.Start.Col, ScrollView: selection.Start.ScrollView,
				Boundary: selection.Start.Boundary,
			},
			End: altSelectionPoint{
				Row: box.Rect.Y + selection.End.Row - scrollTop,
				Col: box.Rect.X + selection.End.Col, ScrollView: selection.End.ScrollView,
				Boundary: selection.End.Boundary,
			},
		}
	}
	out := make([]string, 0, len(screen))
	for row, line := range screen {
		if row < minRow || row > maxRow || row < screenSelection.Start.Row || row > screenSelection.End.Row ||
			IsImageLine(line) {
			out = append(out, line)
			continue
		}
		lineWidth := VisibleWidth(line)
		start, end := s.getSelectionColumnsLocked(line, row, screenSelection, minColumn, maxColumn)
		if end <= start {
			out = append(out, line)
			continue
		}
		before := SliceByColumn(line, 0, start, true)
		selected := SliceByColumn(line, start, end-start, true)
		after := SliceByColumn(line, end, max(0, lineWidth-end), true)
		out = append(out, before+s.applySelectionHighlight(selected)+after)
	}
	return out
}

func (s *AltScreen) isMouseSequence(data string) bool {
	if altSgrMouseRegex.MatchString(data) {
		return true
	}
	return len(data) == 6 && strings.HasPrefix(data, "\x1b[M")
}

func (s *AltScreen) compositeScrollToEndIndicator(screen []string, layout LayoutFrame, width int) []string {
	s.scrollToEndRect = nil
	scrollView := layout.PrimaryScrollView
	if scrollView == nil {
		scrollView = s.implicitScrollView
	}
	if s.scrollToEndIndicator == nil || !scrollView.FollowEnd() || scrollView.IsFollowingEnd() {
		return screen
	}
	box, ok := GetScrollViewBox(layout, scrollView)
	if !ok {
		return screen
	}
	clip := box.Clip
	if clip.Width <= 0 || clip.Height <= 0 {
		return screen
	}
	row := clip.Y + clip.Height - 1
	if row >= len(screen) || IsImageLine(screen[row]) {
		return screen
	}
	// v0.87 (upstream tui-alt-screen.ts compositeScrollToEndIndicator): the
	// label is truncated to the clip width, centered within the clip, and only
	// then clamped so it never overlaps the scrollbar column. The pre-0.87
	// version centered the label in the space left of the scrollbar instead,
	// which shifted it left by one cell for an even remainder.
	label := TruncateToWidth(s.scrollToEndIndicator(), clip.Width, "", false)
	labelWidth := VisibleWidth(label)
	column := clip.X + (clip.Width-labelWidth)/2
	rightEdge := clip.X + clip.Width
	if geometry, ok := GetScrollbarGeometry(box, false); ok {
		rightEdge = geometry.Column
	}
	availableWidth := max(0, rightEdge-column)
	text := TruncateToWidth(label, availableWidth, "", false)
	textWidth := VisibleWidth(text)
	if textWidth == 0 {
		return screen
	}
	result := append([]string(nil), screen...)
	result[row] = CompositeTuiLine(result[row], text, column, textWidth, width)
	s.scrollToEndRect = &scrollToEndIndicatorRect{Row: row, Column: column, Width: textWidth}
	return result
}

func (s *AltScreen) compositeFlashes(screen []string, width int, height int) []string {
	flashLines := s.flashes.Render(width)
	if len(flashLines) > height {
		flashLines = flashLines[len(flashLines)-height:]
	}
	if len(flashLines) == 0 {
		return screen
	}
	result := append([]string(nil), screen...)
	for len(result) < height {
		result = append(result, "")
	}
	for row, line := range flashLines {
		flashWidth := VisibleWidth(line)
		if flashWidth == 0 {
			continue
		}
		result[row] = CompositeTuiLine(result[row], line, width-flashWidth, flashWidth, width)
	}
	return result
}

func (s *AltScreen) doRender() {
	if s.stopped.Load() || !s.altScreenActive {
		return
	}
	width := max(1, s.Terminal.Columns())
	height := max(1, s.Terminal.Rows())
	root := s.getLayoutRoot()
	if root == nil {
		root = s.implicitScrollView
	}
	nextLayout := RenderLayoutFrame(root, width, height, func() { s.RequestRender(false) })
	if s.refreshSearchLocked(nextLayout) {
		nextLayout = RenderLayoutFrame(root, width, height, func() { s.RequestRender(false) })
	}
	screen := make([]string, 0, len(nextLayout.Lines))
	for _, line := range nextLayout.Lines {
		screen = append(screen, stripZonePrefix(line))
	}
	screen = s.applySearchHighlights(screen, nextLayout)
	screen = s.compositeScrollToEndIndicator(screen, nextLayout, width)
	screen = s.CompositeOverlays(screen, width, height)
	if len(screen) > height {
		screen = screen[len(screen)-height:]
	}
	screen = s.applySelection(screen, &nextLayout)
	screen = s.compositeFlashes(screen, width, height)

	row, col, hasCursor := s.ExtractCursorPosition(screen, height)
	if LowBandwidth() {
		// Trailing padding is invisible (each row is cleared before writing) but
		// costs a byte per cell on the wire; drop it.
		for index, line := range screen {
			if !IsImageLine(line) {
				screen[index] = strings.TrimRight(line, " ")
			}
		}
	}
	screen = s.ApplyLineResets(screen)
	for index, line := range screen {
		if !IsImageLine(line) && VisibleWidth(line) > width {
			screen[index] = SliceByColumn(line, 0, width, true)
		}
	}

	fullRedraw := len(s.previousScreen) == 0 || s.previousScreenWidth != width || s.previousScreenHeight != height
	imagesNeedRedraw := false
	for index, line := range screen {
		previous := ""
		if index < len(s.previousScreen) {
			previous = s.previousScreen[index]
		}
		if line != previous && (IsImageLine(line) || IsImageLine(previous)) {
			imagesNeedRedraw = true
			break
		}
	}
	redrawImages := fullRedraw || imagesNeedRedraw

	if LowBandwidth() && !fullRedraw && !imagesNeedRedraw && s.rowsAndCursorUnchanged(screen, row, col, hasCursor) {
		// Nothing to say: skip the synchronized-output frame entirely (the
		// cursor, if shown, was already positioned by the previous frame).
		s.currentLayout = &nextLayout
		return
	}

	var builder strings.Builder
	builder.WriteString(beginSynchronizedOutput)
	if fullRedraw {
		s.fullRedrawCount++
		builder.WriteString(s.deleteKittyImagesLocked())
		builder.WriteString("\x1b[2J")
	} else if imagesNeedRedraw {
		if s.imageProtocol == "iterm2" {
			builder.WriteString("\x1b[2J")
		} else if s.imageProtocol == "kitty" {
			builder.WriteString(deleteAllKittyPlacements())
		}
	}

	clearRowsBeforeKittyImages := redrawImages && s.imageProtocol == "kitty" && containsImageLine(screen) &&
		(os.Getenv("WEZTERM_PANE") != "" || strings.ToLower(os.Getenv("TERM_PROGRAM")) == "wezterm")
	if clearRowsBeforeKittyImages {
		for row := 0; row < height; row++ {
			previous := ""
			if row < len(s.previousScreen) {
				previous = s.previousScreen[row]
			}
			if !fullRedraw && !imagesNeedRedraw && screen[row] == previous {
				continue
			}
			builder.WriteString("\x1b[" + itoa(row+1) + ";1H\x1b[2K")
		}
	}

	for row := 0; row < height; row++ {
		previous := ""
		if row < len(s.previousScreen) {
			previous = s.previousScreen[row]
		}
		if !fullRedraw && !imagesNeedRedraw && screen[row] == previous {
			continue
		}
		line := ""
		if row < len(screen) {
			line = screen[row]
		}
		// A full repaint already erased the screen, and a low-bandwidth frame
		// whose new line is at least as wide as the old one overwrites every old
		// cell: neither needs the per-row clear.
		clear := "\x1b[2K"
		if clearRowsBeforeKittyImages ||
			(LowBandwidth() && (fullRedraw || VisibleWidth(line) >= VisibleWidth(previous))) {
			clear = ""
		}
		builder.WriteString("\x1b[" + itoa(row+1) + ";1H" + clear + line)
	}

	if hasCursor {
		builder.WriteString("\x1b[" + itoa(row+1) + ";" + itoa(min(width, col)+1) + "H")
		if s.ShowHardwareCursor {
			builder.WriteString("\x1b[?25h")
		} else {
			builder.WriteString("\x1b[?25l")
		}
	} else {
		builder.WriteString("\x1b[?25l")
	}
	builder.WriteString(endSynchronizedOutput)
	s.Terminal.Write(builder.String())

	s.previousScreen = screen
	s.previousScreenWidth = width
	s.previousScreenHeight = height
	s.previousCursorRow = row
	s.previousCursorCol = col
	s.previousCursorHas = hasCursor
	s.previousCursorValid = true
	s.currentLayout = &nextLayout
}

// rowsAndCursorUnchanged reports whether screen and the cursor match the last
// frame (used by the low-bandwidth no-op skip).
func (s *AltScreen) rowsAndCursorUnchanged(screen []string, row, col int, hasCursor bool) bool {
	if len(screen) != len(s.previousScreen) {
		return false
	}
	for i := range screen {
		if screen[i] != s.previousScreen[i] {
			return false
		}
	}
	if !s.previousCursorValid {
		return false
	}
	if hasCursor != s.previousCursorHas {
		return false
	}
	return !hasCursor || (row == s.previousCursorRow && col == s.previousCursorCol)
}

func containsImageLine(lines []string) bool {
	for _, line := range lines {
		if IsImageLine(line) {
			return true
		}
	}
	return false
}

var (
	_ Component    = (*AltScreen)(nil)
	_ Component    = (*implicitDocumentComponent)(nil)
	_ MouseHandler = (*implicitDocumentComponent)(nil)
)
