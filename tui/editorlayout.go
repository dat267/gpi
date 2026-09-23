package tui

import (
	"strings"
)

// Port of the module-level helpers of src/components/editor.ts: paste-marker
// aware segmentation, word wrapping, scroll borders, and the autocomplete
// trigger matching (implemented manually because RE2 has no lookaround: D70).

var pasteMarkerRegex = mustCompileRegex(`\[paste #(\d+)( (\+\d+ lines|\d+ chars))?\]`)
var pasteMarkerSingleRegex = mustCompileRegex(`^\[paste #(\d+)( (\+\d+ lines|\d+ chars))?\]$`)

// isPasteMarker reports whether a segment is a paste marker.
func isPasteMarker(segment string) bool {
	return len(segment) >= 10 && pasteMarkerSingleRegex.MatchString(segment)
}

// editorSegment is a segment with its byte index.
type editorSegment struct {
	Segment string
	Index   int
}

// segmentWithMarkers merges graphemes inside paste markers with valid ids into
// single atomic segments.
func segmentWithMarkers(text string, mode string, validIDs map[int]bool) []editorSegment {
	var base []editorSegment
	if mode == "word" {
		for _, segment := range WordSegments(text) {
			base = append(base, editorSegment{Segment: segment.Segment, Index: segment.Index})
		}
	} else {
		offset := 0
		for _, segment := range segmentGraphemes(text) {
			base = append(base, editorSegment{Segment: segment, Index: offset})
			offset += len(segment)
		}
	}

	if len(validIDs) == 0 || !strings.Contains(text, "[paste #") {
		return base
	}

	type span struct{ start, end int }
	var markers []span
	for _, match := range pasteMarkerRegex.FindAllStringSubmatchIndex(text, -1) {
		id := atoiSafe(text[match[2]:match[3]])
		if !validIDs[id] {
			continue
		}
		markers = append(markers, span{start: match[0], end: match[1]})
	}
	if len(markers) == 0 {
		return base
	}

	var result []editorSegment
	markerIndex := 0
	for _, segment := range base {
		for markerIndex < len(markers) && markers[markerIndex].end <= segment.Index {
			markerIndex++
		}
		if markerIndex < len(markers) {
			marker := markers[markerIndex]
			if segment.Index >= marker.start && segment.Index < marker.end {
				if segment.Index == marker.start {
					result = append(result, editorSegment{Segment: text[marker.start:marker.end], Index: marker.start})
				}
				continue
			}
		}
		result = append(result, segment)
	}
	return result
}

// TextChunk is a word-wrapped chunk with its source positions.
type TextChunk struct {
	Text       string
	StartIndex int
	EndIndex   int
}

// WordWrapLine splits a line into word-wrapped chunks.
func WordWrapLine(line string, maxWidth int, preSegmented []editorSegment) []TextChunk {
	if line == "" || maxWidth <= 0 {
		return []TextChunk{{Text: "", StartIndex: 0, EndIndex: 0}}
	}
	if VisibleWidth(line) <= maxWidth {
		return []TextChunk{{Text: line, StartIndex: 0, EndIndex: len(line)}}
	}

	var chunks []TextChunk
	segments := preSegmented
	if segments == nil {
		offset := 0
		for _, segment := range segmentGraphemes(line) {
			segments = append(segments, editorSegment{Segment: segment, Index: offset})
			offset += len(segment)
		}
	}

	currentWidth := 0
	chunkStart := 0
	wrapOppIndex := -1
	wrapOppWidth := 0

	for i := 0; i < len(segments); i++ {
		segment := segments[i]
		grapheme := segment.Segment
		gWidth := VisibleWidth(grapheme)
		charIndex := segment.Index
		isWS := !isPasteMarker(grapheme) && IsWhitespaceChar(grapheme)

		if currentWidth+gWidth > maxWidth {
			if wrapOppIndex >= 0 && currentWidth-wrapOppWidth+gWidth <= maxWidth {
				chunks = append(chunks, TextChunk{Text: line[chunkStart:wrapOppIndex], StartIndex: chunkStart, EndIndex: wrapOppIndex})
				chunkStart = wrapOppIndex
				currentWidth -= wrapOppWidth
			} else if chunkStart < charIndex {
				chunks = append(chunks, TextChunk{Text: line[chunkStart:charIndex], StartIndex: chunkStart, EndIndex: charIndex})
				chunkStart = charIndex
				currentWidth = 0
			}
			wrapOppIndex = -1
		}

		if gWidth > maxWidth {
			// A single atomic segment wider than the width is re-wrapped
			// (visual only; it stays atomic for editing).
			subChunks := WordWrapLine(grapheme, maxWidth, nil)
			for j := 0; j < len(subChunks)-1; j++ {
				sc := subChunks[j]
				chunks = append(chunks, TextChunk{
					Text:       sc.Text,
					StartIndex: charIndex + sc.StartIndex,
					EndIndex:   charIndex + sc.EndIndex,
				})
			}
			last := subChunks[len(subChunks)-1]
			chunkStart = charIndex + last.StartIndex
			currentWidth = VisibleWidth(last.Text)
			wrapOppIndex = -1
			continue
		}

		currentWidth += gWidth

		var next *editorSegment
		if i+1 < len(segments) {
			next = &segments[i+1]
		}
		if isWS && next != nil && (isPasteMarker(next.Segment) || !IsWhitespaceChar(next.Segment)) {
			wrapOppIndex = next.Index
			wrapOppWidth = currentWidth
		} else if !isWS && next != nil && !IsWhitespaceChar(next.Segment) {
			isCJK := !isPasteMarker(grapheme) && cjkBreakRegex.MatchString(grapheme)
			nextIsCJK := !isPasteMarker(next.Segment) && cjkBreakRegex.MatchString(next.Segment)
			if isCJK || nextIsCJK {
				wrapOppIndex = next.Index
				wrapOppWidth = currentWidth
			}
		}
	}

	chunks = append(chunks, TextChunk{Text: line[chunkStart:], StartIndex: chunkStart, EndIndex: len(line)})
	return chunks
}

// createScrollBorder renders the scroll indicator border.
func createScrollBorder(direction string, hiddenLineCount int, width int) string {
	availableWidth := max(0, width)
	label := " " + direction + " " + itoa(hiddenLineCount) + " more "
	labelWidth := VisibleWidth(label)
	if labelWidth+2 <= availableWidth {
		leftWidth := (availableWidth - labelWidth) / 2
		return strings.Repeat("─", leftWidth) + label + strings.Repeat("─", availableWidth-leftWidth-labelWidth)
	}

	indicator := "─── " + direction + " " + itoa(hiddenLineCount) + " more "
	remaining := availableWidth - VisibleWidth(indicator)
	if remaining >= 0 {
		return indicator + strings.Repeat("─", remaining)
	}

	ellipsisRunes := []rune("...")
	if availableWidth < len(ellipsisRunes) {
		ellipsisRunes = ellipsisRunes[:max(0, availableWidth)]
	}
	ellipsis := string(ellipsisRunes)
	indicatorWidth := availableWidth - VisibleWidth(ellipsis)
	return SliceByColumn(indicator, 0, indicatorWidth, true) + ellipsis
}

// editorTriggerMatch reports whether text before the cursor ends with an
// autocomplete trigger token at a boundary, mirroring the editor's
// buildTriggerPattern regex (see D70 for the lookaround replacement).
func editorTriggerMatch(text string, triggerCharacters []string, includeAtUnquoted bool) bool {
	runes := []rune(text)
	if len(runes) == 0 {
		return false
	}

	// Find the token start: the run of non-separator characters ending at the
	// end of the text.
	start := len(runes)
	for start > 0 {
		previous := runes[start-1]
		next := rune(0)
		hasNext := start < len(runes)
		if hasNext {
			next = runes[start]
		}
		if isAutocompleteSeparator(previous, next, hasNext) {
			break
		}
		start--
	}
	token := string(runes[start:])
	if token == "" {
		return false
	}

	// The quoted attachment form: @"..." (with no closing quote yet). The
	// unquoted form is covered by the trigger character "@" below.
	if strings.HasPrefix(token, `@"`) {
		return !strings.Contains(token[1:], `"`)
	}
	_ = includeAtUnquoted

	for _, trigger := range triggerCharacters {
		if trigger == "" {
			continue
		}
		if strings.HasPrefix(token, trigger) {
			return true
		}
	}
	return false
}

// editorDebounceMatch reports whether the token before the cursor is a symbol
// based completion context (upstream's buildDebouncePattern, which excludes
// the bare trigger characters other than @).
func editorDebounceMatch(text string, triggerCharacters []string) bool {
	runes := []rune(text)
	if len(runes) == 0 {
		return false
	}
	start := len(runes)
	for start > 0 {
		previous := runes[start-1]
		next := rune(0)
		hasNext := start < len(runes)
		if hasNext {
			next = runes[start]
		}
		if isAutocompleteSeparator(previous, next, hasNext) {
			break
		}
		start--
	}
	token := string(runes[start:])
	if token == "" {
		return false
	}
	if strings.HasPrefix(token, `@"`) || strings.HasPrefix(token, "@") {
		// @ followed by a quoted or unquoted suffix.
		return true
	}
	for _, trigger := range triggerCharacters {
		if trigger == "@" || trigger == "" {
			continue
		}
		if strings.HasPrefix(token, trigger) {
			return true
		}
	}
	return false
}
