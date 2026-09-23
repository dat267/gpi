package tui

import (
	"regexp"
	"runtime"
	"strings"
)

// Port of src/alt-screen-search.ts: the fullscreen transcript search index and
// the search overlay component.
//
// Divergences: JS's UTF-16 code-unit offsets become byte offsets; since the
// results are expressed in terminal cell columns the observable output is the
// same, and the golden corpus verifies it.

// AltScreenSearchSegment is a matched cell range on one row. The JSON field
// names match the upstream object shape.
type AltScreenSearchSegment struct {
	Row      int `json:"row"`
	StartCol int `json:"startCol"`
	EndCol   int `json:"endCol"`
}

// AltScreenSearchMatch is a match spanning one or more segments.
type AltScreenSearchMatch struct {
	Segments []AltScreenSearchSegment
}

type searchSourceSpan struct {
	textStart     int
	textEnd       int
	row           int
	startCol      int
	endCol        int
	linearColumns bool
}

type searchCorpus struct {
	text  string
	spans []searchSourceSpan
}

var printableASCIIRegex = regexp.MustCompile(`^[\x20-\x7e]*$`)

// buildSearchCorpus flattens the transcript lines into a searchable string and
// remembers where each run came from.
func buildSearchCorpus(lines []string) searchCorpus {
	var chunks []string
	var spans []searchSourceSpan
	textLength := 0
	pendingSeparator := false

	appendSeparator := func() {
		if !pendingSeparator {
			return
		}
		chunks = append(chunks, " ")
		textLength++
		pendingSeparator = false
	}

	for row, line := range lines {
		line = StripTerminalSequences(line)
		column := 0

		// Rendered transcripts are overwhelmingly ASCII: index complete
		// non-space runs at once.
		if printableASCIIRegex.MatchString(line) {
			index := 0
			for index < len(line) {
				if line[index] == ' ' {
					if textLength > 0 {
						pendingSeparator = true
					}
					column++
					index++
					continue
				}
				end := index + 1
				for end < len(line) && line[end] != ' ' {
					end++
				}
				appendSeparator()
				text := line[index:end]
				chunks = append(chunks, text)
				spans = append(spans, searchSourceSpan{
					textStart:     textLength,
					textEnd:       textLength + len(text),
					row:           row,
					startCol:      column,
					endCol:        column + len(text),
					linearColumns: true,
				})
				textLength += len(text)
				column += len(text)
				index = end
			}
		} else {
			for _, grapheme := range segmentGraphemes(line) {
				width := VisibleWidth(grapheme)
				if strings.TrimSpace(grapheme) == "" {
					if textLength > 0 {
						pendingSeparator = true
					}
					column += width
					continue
				}
				appendSeparator()
				chunks = append(chunks, grapheme)
				spans = append(spans, searchSourceSpan{
					textStart: textLength,
					textEnd:   textLength + len(grapheme),
					row:       row,
					startCol:  column,
					endCol:    column + width,
				})
				textLength += len(grapheme)
				column += width
			}
		}
		if textLength > 0 {
			pendingSeparator = true
		}
	}

	return searchCorpus{text: strings.Join(chunks, ""), spans: spans}
}

// searchQueryWhitespaceRegex collapses runs of whitespace in a search query.
var searchQueryWhitespaceRegex = regexp.MustCompile(`\s+`)

func normalizeSearchQuery(query string) string {
	return strings.TrimSpace(searchQueryWhitespaceRegex.ReplaceAllString(query, " "))
}

func findSearchCorpusMatches(corpus searchCorpus, normalizedQuery string) []AltScreenSearchMatch {
	if normalizedQuery == "" {
		return nil
	}
	expression := regexp.MustCompile("(?i)" + regexp.QuoteMeta(normalizedQuery))
	indexes := expression.FindAllStringIndex(corpus.text, -1)
	if len(indexes) == 0 {
		return nil
	}

	var matches []AltScreenSearchMatch
	spanIndex := 0
	for _, index := range indexes {
		start, end := index[0], index[1]
		for spanIndex < len(corpus.spans) && corpus.spans[spanIndex].textEnd <= start {
			spanIndex++
		}

		var segments []AltScreenSearchSegment
		for i := spanIndex; i < len(corpus.spans); i++ {
			span := corpus.spans[i]
			if span.textStart >= end {
				break
			}
			if span.textEnd <= start {
				continue
			}
			startCol := span.startCol
			endCol := span.endCol
			if span.linearColumns {
				startCol = span.startCol + max(start, span.textStart) - span.textStart
				endCol = span.startCol + min(end, span.textEnd) - span.textStart
			}
			if len(segments) > 0 {
				previous := &segments[len(segments)-1]
				if previous.Row == span.row && startCol <= previous.EndCol {
					previous.EndCol = max(previous.EndCol, endCol)
					continue
				}
			}
			segments = append(segments, AltScreenSearchSegment{Row: span.row, StartCol: startCol, EndCol: endCol})
		}
		for spanIndex < len(corpus.spans) && corpus.spans[spanIndex].textEnd <= end {
			spanIndex++
		}
		if len(segments) > 0 {
			matches = append(matches, AltScreenSearchMatch{Segments: segments})
		}
	}
	return matches
}

// AltScreenSearchResult is a search outcome.
type AltScreenSearchResult struct {
	Matches []AltScreenSearchMatch
	Changed bool
}

// AltScreenSearchIndex caches the searchable corpus and matches while the
// rendered transcript lines remain unchanged.
type AltScreenSearchIndex struct {
	sourceLines     []string
	hasSource       bool
	corpus          *searchCorpus
	normalizedQuery string
	hasQuery        bool
	matches         []AltScreenSearchMatch
}

// Search finds the matches for a query.
func (i *AltScreenSearchIndex) Search(lines []string, query string) AltScreenSearchResult {
	sourceChanged := !i.hasSource || len(i.sourceLines) != len(lines)
	if !sourceChanged {
		for index := range lines {
			if i.sourceLines[index] != lines[index] {
				sourceChanged = true
				break
			}
		}
	}
	if sourceChanged || i.corpus == nil {
		i.sourceLines = append([]string(nil), lines...)
		i.hasSource = true
		corpus := buildSearchCorpus(lines)
		i.corpus = &corpus
	}

	normalizedQuery := normalizeSearchQuery(query)
	changed := sourceChanged || normalizedQuery != i.normalizedQuery || !i.hasQuery
	if changed {
		i.normalizedQuery = normalizedQuery
		i.hasQuery = true
		i.matches = findSearchCorpusMatches(*i.corpus, normalizedQuery)
	}
	return AltScreenSearchResult{Matches: i.matches, Changed: changed}
}

// FindAltScreenSearchMatches searches a set of lines without caching.
func FindAltScreenSearchMatches(lines []string, query string) []AltScreenSearchMatch {
	normalizedQuery := normalizeSearchQuery(query)
	if normalizedQuery == "" {
		return nil
	}
	return findSearchCorpusMatches(buildSearchCorpus(lines), normalizedQuery)
}

// GetAltScreenSearchMatchKey returns a stable key for a match.
func GetAltScreenSearchMatchKey(match AltScreenSearchMatch) string {
	if len(match.Segments) == 0 {
		return ""
	}
	first := match.Segments[0]
	last := match.Segments[len(match.Segments)-1]
	return itoa(first.Row) + ":" + itoa(first.StartCol) + ":" + itoa(last.Row) + ":" + itoa(last.EndCol)
}

// AltScreenSearchComponent is the search overlay (an input plus navigation
// buttons).
type AltScreenSearchComponent struct {
	input               *Input
	onQueryChange       func(query string)
	buttonStyle         func(text string, hovered bool) string
	resultCount         int
	resultIndex         int
	previousButtonStart int
	previousButtonEnd   int
	nextButtonStart     int
	nextButtonEnd       int
	hoveredDirection    int // 0 = none, -1 = previous, 1 = next
	hasHoveredDirection bool
	hasHovered          bool
	focused             bool
}

// NewAltScreenSearchComponent creates the search overlay component.
func NewAltScreenSearchComponent(onQueryChange func(query string), buttonStyle func(text string, hovered bool) string) *AltScreenSearchComponent {
	if buttonStyle == nil {
		buttonStyle = func(text string, hovered bool) string { return text }
	}
	component := &AltScreenSearchComponent{
		onQueryChange: onQueryChange,
		buttonStyle:   buttonStyle,
	}
	component.input = NewInput(InputOptions{
		Prompt:      " ",
		Placeholder: "Find in transcript",
		PlaceholderStyle: func(text string) string {
			return "\x1b[2m" + text + "\x1b[22m"
		},
	})
	return component
}

// SetFocused implements Focusable.
func (c *AltScreenSearchComponent) SetFocused(focused bool) {
	c.focused = focused
	c.input.SetFocused(focused)
}

// IsFocused implements Focusable.
func (c *AltScreenSearchComponent) IsFocused() bool { return c.focused }

// SetResult updates the result counter.
func (c *AltScreenSearchComponent) SetResult(index int, count int) {
	c.resultIndex = index
	c.resultCount = count
}

// GetNavigationDirectionAt resolves the navigation button under a cell. The
// bool reports whether a button was hit (upstream returns undefined).
func (c *AltScreenSearchComponent) GetNavigationDirectionAt(row int, column int) (int, bool) {
	if row != 2 {
		return 0, false
	}
	if column >= c.previousButtonStart && column < c.previousButtonEnd {
		return -1, true
	}
	if column >= c.nextButtonStart && column < c.nextButtonEnd {
		return 1, true
	}
	return 0, false
}

// SetHoveredNavigationDirection updates the hovered button; hasDirection marks
// the "no hover" state.
func (c *AltScreenSearchComponent) SetHoveredNavigationDirection(direction int, hasDirection bool) bool {
	next := direction
	if !hasDirection {
		next = 0
	}
	if c.hasHovered && c.hoveredDirection == next && c.hasHoveredDirection == hasDirection {
		return false
	}
	c.hoveredDirection = next
	c.hasHoveredDirection = hasDirection
	c.hasHovered = true
	return true
}

// HandleInput processes input.
func (c *AltScreenSearchComponent) HandleInput(data string) {
	previous := c.input.Value()
	c.input.HandleInput(data)
	query := c.input.Value()
	if query != previous && c.onQueryChange != nil {
		c.onQueryChange(query)
	}
}

// Invalidate drops cached state.
func (c *AltScreenSearchComponent) Invalidate() { c.input.Invalidate() }

// Render renders the overlay.
func (c *AltScreenSearchComponent) Render(width int) []string {
	safeWidth := max(1, width)
	innerWidth := max(0, safeWidth-2)
	formatKey := func(key string, hasKey bool) string {
		if !hasKey || key == "" {
			return "Unbound"
		}
		parts := strings.Split(key, "+")
		for index, part := range parts {
			if runtime.GOOS == "darwin" && strings.ToLower(part) == "alt" {
				parts[index] = "Option"
				continue
			}
			if part != "" {
				parts[index] = strings.ToUpper(part[:1]) + part[1:]
			}
		}
		return strings.Join(parts, "+")
	}
	keybindings := GetKeybindings()
	previousKeys := keybindings.GetKeys("tui.altScreen.searchPrevious")
	nextKeys := keybindings.GetKeys("tui.altScreen.searchNext")
	previousKey := ""
	if len(previousKeys) > 0 {
		previousKey = previousKeys[0]
	}
	nextKey := ""
	if len(nextKeys) > 0 {
		nextKey = nextKeys[0]
	}
	previousKey = formatKey(previousKey, len(previousKeys) > 0)
	nextKey = formatKey(nextKey, len(nextKeys) > 0)

	query := c.input.Value()
	result := ""
	if query != "" {
		if c.resultCount == 0 {
			result = "No matches"
		} else {
			result = itoa(c.resultIndex+1) + "/" + itoa(c.resultCount)
		}
	}
	resultSpace := max(0, innerWidth-3)
	visibleResult := TruncateToWidth(result, resultSpace, "", false)
	resultText := ""
	if visibleResult != "" {
		resultText = "\x1b[2m " + visibleResult + " \x1b[22m"
	}
	inputWidth := max(0, innerWidth-VisibleWidth(resultText))
	inputLine := ""
	if lines := c.input.Render(max(1, inputWidth)); len(lines) > 0 {
		inputLine = lines[0]
	}
	inputLine = TruncateToWidth(inputLine, inputWidth, "", false)
	inputPadding := repeatSpaces(max(0, inputWidth-VisibleWidth(inputLine)))
	content := inputLine + inputPadding + resultText

	previousButton := "↑ " + previousKey
	nextButton := "↓ " + nextKey
	separator := " · "
	outerGapWidth := 1
	availableControlsWidth := max(0, innerWidth-outerGapWidth*2-1)
	controlsWidth := VisibleWidth(previousButton) + VisibleWidth(separator) + VisibleWidth(nextButton)
	if controlsWidth > availableControlsWidth {
		previousButton = "↑"
		nextButton = "↓"
		separator = " "
		controlsWidth = VisibleWidth(previousButton) + VisibleWidth(separator) + VisibleWidth(nextButton)
	}
	showButtons := controlsWidth <= availableControlsWidth
	renderedButtons := ""
	if showButtons {
		renderedButtons = c.buttonStyle(previousButton, c.hoveredDirection == -1) +
			separator +
			c.buttonStyle(nextButton, c.hoveredDirection == 1)
	}
	outerGapsWidth := 0
	if showButtons {
		outerGapsWidth = outerGapWidth * 2
	}
	rightRuleWidth := 0
	if renderedButtons != "" && innerWidth > controlsWidth+outerGapsWidth {
		rightRuleWidth = 1
	}
	controlsWidthIfShown := 0
	if showButtons {
		controlsWidthIfShown = controlsWidth
	}
	leftRuleWidth := max(0, innerWidth-controlsWidthIfShown-outerGapsWidth-rightRuleWidth)
	previousStart := 1 + leftRuleWidth + outerGapWidth
	if showButtons {
		c.previousButtonStart = previousStart
		c.previousButtonEnd = previousStart + VisibleWidth(previousButton)
		c.nextButtonStart = c.previousButtonEnd + VisibleWidth(separator)
		c.nextButtonEnd = c.nextButtonStart + VisibleWidth(nextButton)
	} else {
		c.previousButtonStart = -1
		c.previousButtonEnd = -1
		c.nextButtonStart = -1
		c.nextButtonEnd = -1
	}

	if safeWidth == 1 {
		return []string{"┌", "│", "└"}
	}
	buttonGap := ""
	if renderedButtons != "" {
		buttonGap = " "
	}
	return []string{
		"┌" + strings.Repeat("─", innerWidth) + "┐",
		"│" + content + "│",
		"└" + strings.Repeat("─", leftRuleWidth) + buttonGap + renderedButtons + buttonGap +
			strings.Repeat("─", rightRuleWidth) + "┘",
	}
}

var (
	_ Component = (*AltScreenSearchComponent)(nil)
	_ Focusable = (*AltScreenSearchComponent)(nil)
)
