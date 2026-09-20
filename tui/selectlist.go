package tui

import (
	"strings"
	"sync"
	"time"
)

// Port of src/components/select-list.ts, src/components/truncated-text.ts,
// src/components/loader.ts, src/components/cancellable-loader.ts,
// src/components/mouse-region.ts, and src/components/alt-screen-flash.ts.

const (
	defaultPrimaryColumnWidth = 32
	primaryColumnGap          = 2
	minDescriptionWidth       = 10
)

func normalizeToSingleLine(text string) string {
	var builder strings.Builder
	previousWasNewline := false
	for _, r := range text {
		if r == '\r' || r == '\n' {
			if !previousWasNewline {
				builder.WriteRune(' ')
			}
			previousWasNewline = true
			continue
		}
		previousWasNewline = false
		builder.WriteRune(r)
	}
	return strings.TrimSpace(builder.String())
}

func clampInt(value int, min int, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// SelectItem is a selectable entry. The JSON field names match upstream's
// object shape.
type SelectItem struct {
	Value       string `json:"value"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// SelectListTheme styles the list parts.
type SelectListTheme struct {
	SelectedPrefix func(text string) string
	SelectedText   func(text string) string
	Description    func(text string) string
	ScrollInfo     func(text string) string
	NoMatch        func(text string) string
}

// SelectListTruncatePrimaryContext is passed to a custom primary truncator.
type SelectListTruncatePrimaryContext struct {
	Text        string
	MaxWidth    int
	ColumnWidth int
	Item        SelectItem
	IsSelected  bool
}

// SelectListLayoutOptions configure the list layout.
type SelectListLayoutOptions struct {
	MinPrimaryColumnWidth int
	HasMin                bool
	MaxPrimaryColumnWidth int
	HasMax                bool
	TruncatePrimary       func(context SelectListTruncatePrimaryContext) string
}

// SelectList is a filtered, scrolling selection list.
type SelectList struct {
	items             []SelectItem
	filteredItems     []SelectItem
	selectedIndex     int
	mousePressedIndex int
	hasMousePressed   bool
	maxVisible        int
	theme             SelectListTheme
	layout            SelectListLayoutOptions

	OnSelect          func(item SelectItem)
	OnCancel          func()
	OnSelectionChange func(item SelectItem)
}

// NewSelectList creates a select list.
func NewSelectList(items []SelectItem, maxVisible int, theme SelectListTheme, layout SelectListLayoutOptions) *SelectList {
	theme = withDefaultSelectListTheme(theme)
	return &SelectList{
		items:         items,
		filteredItems: items,
		maxVisible:    maxVisible,
		theme:         theme,
		layout:        layout,
	}
}

func withDefaultSelectListTheme(theme SelectListTheme) SelectListTheme {
	identity := func(text string) string { return text }
	if theme.SelectedPrefix == nil {
		theme.SelectedPrefix = identity
	}
	if theme.SelectedText == nil {
		theme.SelectedText = identity
	}
	if theme.Description == nil {
		theme.Description = identity
	}
	if theme.ScrollInfo == nil {
		theme.ScrollInfo = identity
	}
	if theme.NoMatch == nil {
		theme.NoMatch = identity
	}
	return theme
}

// SetFilter filters the items by value prefix and resets the selection.
func (s *SelectList) SetFilter(filter string) {
	lower := strings.ToLower(filter)
	filtered := make([]SelectItem, 0, len(s.items))
	for _, item := range s.items {
		if strings.HasPrefix(strings.ToLower(item.Value), lower) {
			filtered = append(filtered, item)
		}
	}
	s.filteredItems = filtered
	s.selectedIndex = 0
}

// SetSelectedIndex clamps and sets the selection.
func (s *SelectList) SetSelectedIndex(index int) {
	s.selectedIndex = maxInt(0, minInt(index, len(s.filteredItems)-1))
}

// Invalidate drops cached state (none).
func (s *SelectList) Invalidate() {}

// Render renders the visible items.
func (s *SelectList) Render(width int) []string {
	var lines []string

	if len(s.filteredItems) == 0 {
		lines = append(lines, s.theme.NoMatch("  No matching commands"))
		return lines
	}

	primaryColumnWidth := s.getPrimaryColumnWidth()
	startIndex, endIndex := s.getVisibleRange()

	for i := startIndex; i < endIndex; i++ {
		item := s.filteredItems[i]
		isSelected := i == s.selectedIndex
		description := ""
		if item.Description != "" {
			description = normalizeToSingleLine(item.Description)
		}
		lines = append(lines, s.renderItem(item, isSelected, width, description, primaryColumnWidth))
	}

	if startIndex > 0 || endIndex < len(s.filteredItems) {
		scrollText := "  (" + itoa(s.selectedIndex+1) + "/" + itoa(len(s.filteredItems)) + ")"
		lines = append(lines, s.theme.ScrollInfo(TruncateToWidth(scrollText, width-2, "", false)))
	}

	return lines
}

// HandleMouse processes wheel scrolling and press/click selection.
func (s *SelectList) HandleMouse(event TuiMouseEvent) *TuiMouseDispatchResult {
	if len(s.filteredItems) == 0 {
		return nil
	}
	if event.Type == MouseWheel && event.HasWheel && event.WheelDelta != 0 {
		delta := 1
		if event.WheelDelta < 0 {
			delta = -1
		}
		previousIndex := s.selectedIndex
		s.selectedIndex = maxInt(0, minInt(len(s.filteredItems)-1, s.selectedIndex+delta))
		if s.selectedIndex != previousIndex {
			s.notifySelectionChange()
		}
		return &TuiMouseDispatchResult{TuiMouseEventResult: TuiMouseEventResult{
			Handled: true, Render: s.selectedIndex != previousIndex, HasRender: true,
		}}
	}
	// Hover must not change selection: the visible range is centered on it.
	if event.Button != MouseButtonLeft || (event.Type != MousePress && event.Type != MouseClick) {
		return nil
	}
	startIndex, endIndex := s.getVisibleRange()
	itemIndex := startIndex + event.Y
	if itemIndex < startIndex || itemIndex >= endIndex {
		return nil
	}

	if event.Type == MousePress {
		s.mousePressedIndex = itemIndex
		s.hasMousePressed = true
		if s.selectedIndex != itemIndex {
			s.selectedIndex = itemIndex
			s.notifySelectionChange()
		}
		return &TuiMouseDispatchResult{TuiMouseEventResult: TuiMouseEventResult{Handled: true, Focus: true}}
	}
	// click
	clickedIndex := itemIndex
	if s.hasMousePressed {
		clickedIndex = s.mousePressedIndex
	}
	s.hasMousePressed = false
	changed := s.selectedIndex != clickedIndex
	s.selectedIndex = clickedIndex
	if changed {
		s.notifySelectionChange()
	}
	if s.selectedIndex >= 0 && s.selectedIndex < len(s.filteredItems) && s.OnSelect != nil {
		s.OnSelect(s.filteredItems[s.selectedIndex])
	}
	return &TuiMouseDispatchResult{TuiMouseEventResult: TuiMouseEventResult{Handled: true}}
}

// HandleInput processes navigation keys.
func (s *SelectList) HandleInput(keyData string) {
	kb := GetKeybindings()
	switch {
	case kb.Matches(keyData, "tui.select.up"):
		if s.selectedIndex == 0 {
			s.selectedIndex = len(s.filteredItems) - 1
		} else {
			s.selectedIndex--
		}
		s.notifySelectionChange()
	case kb.Matches(keyData, "tui.select.down"):
		if s.selectedIndex == len(s.filteredItems)-1 {
			s.selectedIndex = 0
		} else {
			s.selectedIndex++
		}
		s.notifySelectionChange()
	case kb.Matches(keyData, "tui.select.confirm"):
		if s.selectedIndex >= 0 && s.selectedIndex < len(s.filteredItems) && s.OnSelect != nil {
			s.OnSelect(s.filteredItems[s.selectedIndex])
		}
	case kb.Matches(keyData, "tui.select.cancel"):
		if s.OnCancel != nil {
			s.OnCancel()
		}
	}
}

func (s *SelectList) getVisibleRange() (startIndex int, endIndex int) {
	startIndex = maxInt(0, minInt(s.selectedIndex-s.maxVisible/2, len(s.filteredItems)-s.maxVisible))
	return startIndex, minInt(startIndex+s.maxVisible, len(s.filteredItems))
}

func (s *SelectList) renderItem(item SelectItem, isSelected bool, width int, descriptionSingleLine string, primaryColumnWidth int) string {
	prefix := "  "
	if isSelected {
		prefix = "→ "
	}
	prefixWidth := VisibleWidth(prefix)

	if descriptionSingleLine != "" && width > 40 {
		effectivePrimaryColumnWidth := maxInt(1, minInt(primaryColumnWidth, width-prefixWidth-4))
		maxPrimaryWidth := maxInt(1, effectivePrimaryColumnWidth-primaryColumnGap)
		truncatedValue := s.truncatePrimary(item, isSelected, maxPrimaryWidth, effectivePrimaryColumnWidth)
		truncatedValueWidth := VisibleWidth(truncatedValue)
		spacing := repeatSpaces(maxInt(1, effectivePrimaryColumnWidth-truncatedValueWidth))
		descriptionStart := prefixWidth + truncatedValueWidth + len(spacing)
		remainingWidth := width - descriptionStart - 2 // safety margin

		if remainingWidth > minDescriptionWidth {
			truncatedDesc := TruncateToWidth(descriptionSingleLine, remainingWidth, "", false)
			if isSelected {
				return s.theme.SelectedText(prefix + truncatedValue + spacing + truncatedDesc)
			}
			descText := s.theme.Description(spacing + truncatedDesc)
			return prefix + truncatedValue + descText
		}
	}

	maxWidth := width - prefixWidth - 2
	truncatedValue := s.truncatePrimary(item, isSelected, maxWidth, maxWidth)
	if isSelected {
		return s.theme.SelectedText(prefix + truncatedValue)
	}
	return prefix + truncatedValue
}

func (s *SelectList) getPrimaryColumnWidth() int {
	minWidth, maxWidth := s.getPrimaryColumnBounds()
	widest := 0
	for _, item := range s.filteredItems {
		widest = maxInt(widest, VisibleWidth(s.getDisplayValue(item))+primaryColumnGap)
	}
	return clampInt(widest, minWidth, maxWidth)
}

func (s *SelectList) getPrimaryColumnBounds() (minWidth int, maxWidth int) {
	rawMin := defaultPrimaryColumnWidth
	if s.layout.HasMin {
		rawMin = s.layout.MinPrimaryColumnWidth
	} else if s.layout.HasMax {
		rawMin = s.layout.MaxPrimaryColumnWidth
	}
	rawMax := defaultPrimaryColumnWidth
	if s.layout.HasMax {
		rawMax = s.layout.MaxPrimaryColumnWidth
	} else if s.layout.HasMin {
		rawMax = s.layout.MinPrimaryColumnWidth
	}
	return maxInt(1, minInt(rawMin, rawMax)), maxInt(1, maxInt(rawMin, rawMax))
}

func (s *SelectList) truncatePrimary(item SelectItem, isSelected bool, maxWidth int, columnWidth int) string {
	displayValue := s.getDisplayValue(item)
	truncatedValue := TruncateToWidth(displayValue, maxWidth, "", false)
	if s.layout.TruncatePrimary != nil {
		truncatedValue = s.layout.TruncatePrimary(SelectListTruncatePrimaryContext{
			Text:        displayValue,
			MaxWidth:    maxWidth,
			ColumnWidth: columnWidth,
			Item:        item,
			IsSelected:  isSelected,
		})
	}
	return TruncateToWidth(truncatedValue, maxWidth, "", false)
}

func (s *SelectList) getDisplayValue(item SelectItem) string {
	if item.Label != "" {
		return item.Label
	}
	return item.Value
}

func (s *SelectList) notifySelectionChange() {
	if s.selectedIndex >= 0 && s.selectedIndex < len(s.filteredItems) && s.OnSelectionChange != nil {
		s.OnSelectionChange(s.filteredItems[s.selectedIndex])
	}
}

// GetSelectedItem returns the selected item, if any.
func (s *SelectList) GetSelectedItem() (SelectItem, bool) {
	if s.selectedIndex < 0 || s.selectedIndex >= len(s.filteredItems) {
		return SelectItem{}, false
	}
	return s.filteredItems[s.selectedIndex], true
}

// FilteredItems returns the current filtered items.
func (s *SelectList) FilteredItems() []SelectItem { return s.filteredItems }

// SelectedIndex returns the current selection index.
func (s *SelectList) SelectedIndex() int { return s.selectedIndex }

// ---- TruncatedText ----

// TruncatedText renders a single truncated, padded line.
type TruncatedText struct {
	Text     string
	PaddingX int
	PaddingY int
}

// NewTruncatedText creates a truncated text component.
func NewTruncatedText(text string, paddingX int, paddingY int) *TruncatedText {
	return &TruncatedText{Text: text, PaddingX: paddingX, PaddingY: paddingY}
}

// Invalidate drops cached state (none).
func (t *TruncatedText) Invalidate() {}

// Render renders the first line padded to the width.
func (t *TruncatedText) Render(width int) []string {
	var result []string
	emptyLine := repeatSpaces(width)

	for i := 0; i < t.PaddingY; i++ {
		result = append(result, emptyLine)
	}

	availableWidth := maxInt(1, width-t.PaddingX*2)

	singleLineText := t.Text
	if newlineIndex := strings.Index(t.Text, "\n"); newlineIndex != -1 {
		singleLineText = t.Text[:newlineIndex]
	}

	displayText := TruncateToWidth(singleLineText, availableWidth, "...", false)

	leftPadding := repeatSpaces(maxInt(0, t.PaddingX))
	lineWithPadding := leftPadding + displayText + leftPadding
	paddingNeeded := maxInt(0, width-VisibleWidth(lineWithPadding))
	result = append(result, lineWithPadding+repeatSpaces(paddingNeeded))

	for i := 0; i < t.PaddingY; i++ {
		result = append(result, emptyLine)
	}

	return result
}

// ---- Loader ----

// RenderRequester is the render request surface the loader needs (upstream
// takes the whole TUI: divergence D62).
type RenderRequester interface {
	RequestRender(force bool)
}

// LoaderIndicatorOptions configure the loader animation.
type LoaderIndicatorOptions struct {
	// Frames are the animation frames; an empty slice hides the indicator.
	Frames     []string
	HasFrames  bool
	IntervalMS int
}

// Loader is an animated spinner with a message.
type Loader struct {
	*Text

	requestRender RenderRequester
	spinnerColor  func(string) string
	messageColor  func(string) string
	message       string

	mu             sync.Mutex
	frames         []string
	intervalMS     int
	currentFrame   int
	ticker         *time.Ticker
	done           chan struct{}
	renderVerbatim bool
	stopped        bool
}

var defaultLoaderFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const defaultLoaderIntervalMS = 80

// NewLoader creates a loader. Frames may be nil for the default animation.
func NewLoader(requestRender RenderRequester, spinnerColor func(string) string, messageColor func(string) string, message string, indicator *LoaderIndicatorOptions) *Loader {
	loader := &Loader{
		Text:          NewText("", 1, 0, nil),
		requestRender: requestRender,
		spinnerColor:  spinnerColor,
		messageColor:  messageColor,
		message:       message,
	}
	if loader.spinnerColor == nil {
		loader.spinnerColor = func(text string) string { return text }
	}
	if loader.messageColor == nil {
		loader.messageColor = func(text string) string { return text }
	}
	if loader.message == "" {
		loader.message = "Loading..."
	}
	loader.SetIndicator(indicator)
	return loader
}

// Render renders an empty line followed by the loader text.
func (l *Loader) Render(width int) []string {
	return append([]string{""}, l.Text.Render(width)...)
}

// Start updates the display and (re)starts the animation.
func (l *Loader) Start() {
	l.updateDisplay()
	l.restartAnimation()
}

// Stop halts the animation. The stopped flag makes a callback that already
// received a tick bail out under the lock (upstream's clearInterval is
// sufficient on a single-threaded event loop; Go needs the flag: D64).
func (l *Loader) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.stopped = true
	if l.ticker != nil {
		l.ticker.Stop()
		l.ticker = nil
	}
	if l.done != nil {
		close(l.done)
		l.done = nil
	}
}

// SetMessage updates the message.
func (l *Loader) SetMessage(message string) {
	l.mu.Lock()
	l.message = message
	l.mu.Unlock()
	l.updateDisplay()
}

// Invalidate drops the cache and refreshes the display.
func (l *Loader) Invalidate() {
	l.Text.Invalidate()
	l.updateDisplay()
}

// SetIndicator configures the animation frames and interval.
func (l *Loader) SetIndicator(indicator *LoaderIndicatorOptions) {
	l.mu.Lock()
	if indicator == nil {
		l.renderVerbatim = false
		l.frames = append([]string(nil), defaultLoaderFrames...)
		l.intervalMS = defaultLoaderIntervalMS
	} else {
		l.renderVerbatim = true
		if indicator.HasFrames {
			l.frames = append([]string{}, indicator.Frames...)
		} else {
			l.frames = append([]string(nil), defaultLoaderFrames...)
		}
		interval := indicator.IntervalMS
		if interval <= 0 {
			interval = defaultLoaderIntervalMS
		}
		l.intervalMS = interval
	}
	l.currentFrame = 0
	l.mu.Unlock()
	l.Start()
}

func (l *Loader) restartAnimation() {
	l.Stop()
	l.mu.Lock()
	if len(l.frames) <= 1 {
		l.mu.Unlock()
		return
	}
	ticker := time.NewTicker(time.Duration(l.intervalMS) * time.Millisecond)
	done := make(chan struct{})
	l.ticker = ticker
	l.done = done
	l.stopped = false
	l.mu.Unlock()

	go func() {
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				l.mu.Lock()
				if l.stopped {
					l.mu.Unlock()
					return
				}
				l.currentFrame = (l.currentFrame + 1) % len(l.frames)
				l.mu.Unlock()
				l.updateDisplay()
			}
		}
	}()
}

// RenderedIndicator returns the current frame, styled unless verbatim.
func (l *Loader) RenderedIndicator() string {
	l.mu.Lock()
	frame := ""
	if len(l.frames) > 0 {
		frame = l.frames[l.currentFrame%len(l.frames)]
	}
	verbatim := l.renderVerbatim
	l.mu.Unlock()
	if verbatim {
		return frame
	}
	return l.spinnerColor(frame)
}

func (l *Loader) updateDisplay() {
	renderedFrame := l.RenderedIndicator()
	indicator := ""
	if len(renderedFrame) > 0 {
		indicator = renderedFrame + " "
	}
	l.mu.Lock()
	message := l.message
	l.mu.Unlock()
	l.Text.SetText(indicator + l.messageColor(message))
	if l.requestRender != nil {
		l.requestRender.RequestRender(false)
	}
}

// ---- CancellableLoader ----

// CancellableLoader is a loader that can be cancelled with Escape.
type CancellableLoader struct {
	*Loader

	OnAbort func()

	cancel func()
	done   chan struct{}
}

// NewCancellableLoader creates a cancellable loader.
func NewCancellableLoader(requestRender RenderRequester, spinnerColor func(string) string, messageColor func(string) string, message string) *CancellableLoader {
	loader := &CancellableLoader{
		Loader: NewLoader(requestRender, spinnerColor, messageColor, message, nil),
		done:   make(chan struct{}),
	}
	loader.cancel = func() {
		select {
		case <-loader.done:
		default:
			close(loader.done)
		}
	}
	return loader
}

// Done is closed when the loader is aborted (upstream's AbortSignal).
func (c *CancellableLoader) Done() <-chan struct{} { return c.done }

// Aborted reports whether the loader was aborted.
func (c *CancellableLoader) Aborted() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

// HandleInput aborts on the cancel keybinding.
func (c *CancellableLoader) HandleInput(data string) {
	if GetKeybindings().Matches(data, "tui.select.cancel") {
		c.cancel()
		if c.OnAbort != nil {
			c.OnAbort()
		}
	}
}

// Dispose stops the loader.
func (c *CancellableLoader) Dispose() { c.Stop() }

// ---- MouseRegion ----

// MouseRegionHandler handles mouse events for a wrapped component.
type MouseRegionHandler func(event TuiMouseEvent) *TuiMouseDispatchResult

// MouseRegion adds mouse handling to a component without changing rendering.
type MouseRegion struct {
	child   Component
	onMouse MouseRegionHandler
}

// NewMouseRegion wraps a component with a mouse handler.
func NewMouseRegion(child Component, onMouse MouseRegionHandler) *MouseRegion {
	return &MouseRegion{child: child, onMouse: onMouse}
}

// Render renders the child.
func (m *MouseRegion) Render(width int) []string { return m.child.Render(width) }

// HandleMouse forwards to the child first, then the region handler.
func (m *MouseRegion) HandleMouse(event TuiMouseEvent) *TuiMouseDispatchResult {
	if result := DispatchMouseEvent(m.child, event); result != nil {
		return result
	}
	if m.onMouse == nil {
		return nil
	}
	return m.onMouse(event)
}

// Invalidate invalidates the child.
func (m *MouseRegion) Invalidate() { m.child.Invalidate() }

// ---- AltScreenFlashContainer ----

// AltScreenFlashContainer shows transient messages composited by the
// alternate-screen renderer.
type AltScreenFlashContainer struct {
	requestRender func()

	mu      sync.Mutex
	entries []flashEntry
	nextID  int
}

type flashEntry struct {
	id      int
	message string
	timer   *time.Timer
}

// NewAltScreenFlashContainer creates a flash container.
func NewAltScreenFlashContainer(requestRender func()) *AltScreenFlashContainer {
	return &AltScreenFlashContainer{requestRender: requestRender}
}

// Flash shows a message for the given duration.
func (c *AltScreenFlashContainer) Flash(message string, durationMS int) {
	if durationMS == 0 {
		durationMS = 1000
	}
	c.mu.Lock()
	id := c.nextID
	c.nextID++
	entry := flashEntry{id: id, message: message}
	entry.timer = time.AfterFunc(time.Duration(maxInt(0, durationMS))*time.Millisecond, func() {
		c.mu.Lock()
		for index, existing := range c.entries {
			if existing.id == id {
				c.entries = append(c.entries[:index], c.entries[index+1:]...)
				break
			}
		}
		c.mu.Unlock()
		if c.requestRender != nil {
			c.requestRender()
		}
	})
	c.entries = append(c.entries, entry)
	c.mu.Unlock()
	if c.requestRender != nil {
		c.requestRender()
	}
}

// Dispose clears all pending flashes.
func (c *AltScreenFlashContainer) Dispose() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, entry := range c.entries {
		entry.timer.Stop()
	}
	c.entries = nil
}

// Invalidate drops cached state (none).
func (c *AltScreenFlashContainer) Invalidate() {}

// Render renders the active flash messages, newest last.
func (c *AltScreenFlashContainer) Render(width int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	lines := make([]string, 0, len(c.entries))
	for _, entry := range c.entries {
		message := TruncateToWidth(" "+entry.message+" ", width, "", false)
		lines = append(lines, "\x1b[7m"+message+"\x1b[27m")
	}
	return lines
}

var (
	_ Component    = (*SelectList)(nil)
	_ MouseHandler = (*SelectList)(nil)
	_ Component    = (*TruncatedText)(nil)
	_ Component    = (*Loader)(nil)
	_ Component    = (*MouseRegion)(nil)
	_ MouseHandler = (*MouseRegion)(nil)
	_ Component    = (*AltScreenFlashContainer)(nil)
)
