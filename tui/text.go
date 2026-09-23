package tui

import "strings"

// Port of src/components/spacer.ts and src/components/text.ts.

// Spacer renders empty lines.
type Spacer struct {
	Lines int

	// cached is the rendered blank block: a transcript holds one spacer per
	// message, so re-allocating it on every paint was the largest per-frame
	// allocator (and it is a pure function of Lines).
	cached []string
}

// NewSpacer creates a spacer with the given line count.
func NewSpacer(lines int) *Spacer {
	return &Spacer{Lines: lines}
}

// SetLines updates the line count.
func (s *Spacer) SetLines(lines int) {
	s.Lines = lines
	s.cached = nil
}

// Invalidate drops cached state (none).
func (s *Spacer) Invalidate() { s.cached = nil }

// Render renders the empty lines.
func (s *Spacer) Render(width int) []string {
	if len(s.cached) != s.Lines {
		result := make([]string, s.Lines)
		s.cached = result
	}
	return s.cached
}

// Text displays multi-line text with word wrapping, padding, and an optional
// background function.
type Text struct {
	text       string
	paddingX   int
	paddingY   int
	customBgFn func(text string) string

	cachedText     string
	hasCachedText  bool
	cachedWidth    int
	hasCachedWidth bool
	cachedLines    []string
	hasCachedLines bool
}

// NewText creates a text component.
func NewText(text string, paddingX int, paddingY int, customBgFn func(text string) string) *Text {
	return &Text{text: text, paddingX: paddingX, paddingY: paddingY, customBgFn: customBgFn}
}

// SetText updates the text and clears the cache.
func (t *Text) SetText(text string) {
	t.text = text
	t.invalidateCache()
}

// SetCustomBgFn updates the background function and clears the cache.
func (t *Text) SetCustomBgFn(customBgFn func(text string) string) {
	t.customBgFn = customBgFn
	t.invalidateCache()
}

// Text returns the current text.
func (t *Text) Text() string { return t.text }

func (t *Text) invalidateCache() {
	t.cachedLines = nil
	t.hasCachedLines = false
	t.hasCachedText = false
	t.hasCachedWidth = false
}

// Invalidate drops the render cache.
func (t *Text) Invalidate() { t.invalidateCache() }

// Render wraps the text and applies the padding and background.
func (t *Text) Render(width int) []string {
	if t.hasCachedLines && t.hasCachedText && t.cachedText == t.text && t.hasCachedWidth && t.cachedWidth == width {
		return t.cachedLines
	}

	// Nothing is rendered when there is no actual text.
	if t.text == "" || strings.TrimSpace(t.text) == "" {
		t.cachedText = t.text
		t.hasCachedText = true
		t.cachedWidth = width
		t.hasCachedWidth = true
		t.cachedLines = []string{}
		t.hasCachedLines = true
		return t.cachedLines
	}

	// Tabs become three spaces.
	normalizedText := strings.ReplaceAll(t.text, "\t", "   ")

	// Reduce the margins so content and padding fit the available width.
	paddingX := min(t.paddingX, max(0, (width-1)/2))
	contentWidth := max(1, width-paddingX*2)

	// Wrap (ANSI-preserving, no padding).
	wrappedLines := WrapTextWithAnsi(normalizedText, contentWidth)

	leftMargin := strings.Repeat(" ", paddingX)
	rightMargin := leftMargin
	var contentLines []string

	for _, line := range wrappedLines {
		lineWithMargins := leftMargin + line + rightMargin
		if t.customBgFn != nil {
			contentLines = append(contentLines, ApplyBackgroundToLine(lineWithMargins, width, t.customBgFn))
		} else {
			paddingNeeded := max(0, width-VisibleWidth(lineWithMargins))
			contentLines = append(contentLines, lineWithMargins+strings.Repeat(" ", paddingNeeded))
		}
	}

	// Top/bottom padding (empty lines).
	emptyLine := strings.Repeat(" ", width)
	var emptyLines []string
	for i := 0; i < t.paddingY; i++ {
		line := emptyLine
		if t.customBgFn != nil {
			line = ApplyBackgroundToLine(emptyLine, width, t.customBgFn)
		}
		emptyLines = append(emptyLines, line)
	}

	result := make([]string, 0, len(emptyLines)*2+len(contentLines))
	result = append(result, emptyLines...)
	result = append(result, contentLines...)
	result = append(result, emptyLines...)

	t.cachedText = t.text
	t.hasCachedText = true
	t.cachedWidth = width
	t.hasCachedWidth = true
	t.cachedLines = result
	t.hasCachedLines = true

	if len(result) > 0 {
		return result
	}
	return []string{""}
}

var _ Component = (*Text)(nil)
var _ Component = (*Spacer)(nil)
var _ LayoutNodeProvider = (*Stack)(nil)
