package tui

import (
	"strings"
)

// Port of src/utils.ts line primitives: truncation, column slicing with
// ANSI/handling, segment extraction, the ANSI code tracker, and ANSI-aware
// word wrapping.

// truncateFragmentToWidth clips one fragment to maxWidth, returning the
// kept text and its visible width. ANSI codes and tabs pass through.
func truncateFragmentToWidth(text string, maxWidth int) (string, int) {
	if maxWidth <= 0 || text == "" {
		return "", 0
	}
	if isPrintableASCII(text) {
		limit := maxWidth
		if limit > len(text) {
			limit = len(text)
		}
		return text[:limit], limit
	}

	hasAnsi := strings.Contains(text, "\x1b")
	hasTabs := strings.Contains(text, "\t")
	if !hasAnsi && !hasTabs {
		result := strings.Builder{}
		width := 0
		for _, segment := range segmentGraphemes(text) {
			w := graphemeWidth(segment)
			if width+w > maxWidth {
				break
			}
			result.WriteString(segment)
			width += w
		}
		return result.String(), width
	}

	var result strings.Builder
	width := 0
	i := 0
	pendingAnsi := ""
	for i < len(text) {
		if code, length := ExtractANSICode(text, i); code != "" {
			pendingAnsi += code
			i += length
			continue
		}
		if text[i] == '\t' {
			if width+3 > maxWidth {
				break
			}
			if pendingAnsi != "" {
				result.WriteString(pendingAnsi)
				pendingAnsi = ""
			}
			result.WriteByte('\t')
			width += 3
			i++
			continue
		}
		end := i
		for end < len(text) && text[end] != '\t' {
			if code, _ := ExtractANSICode(text, end); code != "" {
				break
			}
			end++
		}
		for _, segment := range segmentGraphemes(text[i:end]) {
			w := graphemeWidth(segment)
			if width+w > maxWidth {
				return result.String(), width
			}
			if pendingAnsi != "" {
				result.WriteString(pendingAnsi)
				pendingAnsi = ""
			}
			result.WriteString(segment)
			width += w
		}
		i = end
	}
	return result.String(), width
}

// activeOsc8Close returns the hyperlink close for an active OSC 8 link.
func activeOsc8Close(prefix string) string {
	index := 0
	for index < len(prefix) {
		code, length := ExtractANSICode(prefix, index)
		if code == "" {
			index++
			continue
		}
		if hyperlink := parseOsc8Hyperlink(code); hyperlink != nil {
			return hyperlink.close()
		}
		index += length
	}
	return ""
}

// finalizeTruncatedResult assembles the truncated text with reset codes and
// optional padding.
func finalizeTruncatedResult(prefix string, prefixWidth int, ellipsis string, ellipsisWidth int, maxWidth int, pad bool) string {
	const reset = "\x1b[0m"
	hyperlinkClose := activeOsc8Close(prefix)
	visible := prefixWidth + ellipsisWidth
	var result string
	if ellipsis != "" {
		result = prefix + hyperlinkClose + reset + ellipsis + reset
	} else {
		result = prefix + hyperlinkClose + reset
	}
	if pad {
		padding := maxWidth - visible
		if padding < 0 {
			padding = 0
		}
		result += strings.Repeat(" ", padding)
	}
	return result
}

// TruncateToWidth truncates text to the visible width, appending the ellipsis
// when content is cut and optionally padding to maxWidth. ANSI styling is
// preserved up to the cut; only the contiguous prefix counts toward the kept
// content (a later styled segment does not "rescue" the tail).
func TruncateToWidth(text string, maxWidth int, ellipsis string, pad bool) string {
	if maxWidth <= 0 {
		return ""
	}
	if text == "" {
		if pad {
			return strings.Repeat(" ", maxWidth)
		}
		return ""
	}

	ellipsisWidth := VisibleWidth(ellipsis)
	if ellipsisWidth >= maxWidth {
		textWidth := VisibleWidth(text)
		if textWidth <= maxWidth {
			if pad {
				return text + strings.Repeat(" ", maxWidth-textWidth)
			}
			return text
		}
		clippedText, clippedWidth := truncateFragmentToWidth(ellipsis, maxWidth)
		if clippedWidth == 0 {
			if pad {
				return strings.Repeat(" ", maxWidth)
			}
			return ""
		}
		return finalizeTruncatedResult("", 0, clippedText, clippedWidth, maxWidth, pad)
	}

	if isPrintableASCII(text) {
		if len(text) <= maxWidth {
			if pad {
				return text + strings.Repeat(" ", maxWidth-len(text))
			}
			return text
		}
		targetWidth := maxWidth - ellipsisWidth
		return finalizeTruncatedResult(text[:targetWidth], targetWidth, ellipsis, ellipsisWidth, maxWidth, pad)
	}

	targetWidth := maxWidth - ellipsisWidth
	var result strings.Builder
	pendingAnsi := ""
	visibleSoFar := 0
	keptWidth := 0
	keepContiguousPrefix := true
	overflowed := false
	exhaustedInput := false
	hasAnsi := strings.Contains(text, "\x1b")
	hasTabs := strings.Contains(text, "\t")

	if !hasAnsi && !hasTabs {
		for _, segment := range segmentGraphemes(text) {
			w := graphemeWidth(segment)
			if keepContiguousPrefix && keptWidth+w <= targetWidth {
				result.WriteString(segment)
				keptWidth += w
			} else {
				keepContiguousPrefix = false
			}
			visibleSoFar += w
			if visibleSoFar > maxWidth {
				overflowed = true
				break
			}
		}
		exhaustedInput = !overflowed
	} else {
		i := 0
		for i < len(text) {
			if code, length := ExtractANSICode(text, i); code != "" {
				pendingAnsi += code
				i += length
				continue
			}
			if text[i] == '\t' {
				if keepContiguousPrefix && keptWidth+3 <= targetWidth {
					if pendingAnsi != "" {
						result.WriteString(pendingAnsi)
						pendingAnsi = ""
					}
					result.WriteByte('\t')
					keptWidth += 3
				} else {
					keepContiguousPrefix = false
					pendingAnsi = ""
				}
				visibleSoFar += 3
				if visibleSoFar > maxWidth {
					overflowed = true
					break
				}
				i++
				continue
			}

			end := i
			for end < len(text) && text[end] != '\t' {
				if code, _ := ExtractANSICode(text, end); code != "" {
					break
				}
				end++
			}

			for _, segment := range segmentGraphemes(text[i:end]) {
				w := graphemeWidth(segment)
				if keepContiguousPrefix && keptWidth+w <= targetWidth {
					if pendingAnsi != "" {
						result.WriteString(pendingAnsi)
						pendingAnsi = ""
					}
					result.WriteString(segment)
					keptWidth += w
				} else {
					keepContiguousPrefix = false
					pendingAnsi = ""
				}
				visibleSoFar += w
				if visibleSoFar > maxWidth {
					overflowed = true
					break
				}
			}
			if overflowed {
				break
			}
			i = end
		}
		exhaustedInput = i >= len(text)
	}

	if !overflowed && exhaustedInput {
		if pad {
			padding := maxWidth - visibleSoFar
			if padding < 0 {
				padding = 0
			}
			return text + strings.Repeat(" ", padding)
		}
		return text
	}

	return finalizeTruncatedResult(result.String(), keptWidth, ellipsis, ellipsisWidth, maxWidth, pad)
}

// SliceByColumn extracts a range of visible columns from a line, handling ANSI
// codes and wide characters.
func SliceByColumn(line string, startCol int, length int, strict bool) string {
	return SliceWithWidth(line, startCol, length, strict).Text
}

// SlicedLine is the slice outcome with its visible width.
type SlicedLine struct {
	Text  string
	Width int
}

// SliceWithWidth is SliceByColumn with the visible width of the result.
// strict excludes wide characters that would extend past the range.
func SliceWithWidth(line string, startCol int, length int, strict bool) SlicedLine {
	if length <= 0 {
		return SlicedLine{}
	}
	endCol := startCol + length
	var result strings.Builder
	resultWidth := 0
	currentCol := 0
	i := 0
	pendingAnsi := ""

	for i < len(line) {
		if code, codeLength := ExtractANSICode(line, i); code != "" {
			if currentCol >= startCol && currentCol < endCol {
				// Carry the style from before the slice across the boundary first.
				// A code that applies at the slice's own first column — a reset
				// ending an inline-code span, say — must come after the prefix it
				// belongs after; written first, the reset lands ahead of the colour
				// it cancels, and the slice's text keeps a style that had ended.
				if pendingAnsi != "" {
					result.WriteString(pendingAnsi)
					pendingAnsi = ""
				}
				result.WriteString(code)
			} else if currentCol < startCol {
				pendingAnsi += code
			}
			i += codeLength
			continue
		}

		textEnd := i
		for textEnd < len(line) {
			if code, _ := ExtractANSICode(line, textEnd); code != "" {
				break
			}
			textEnd++
		}

		for _, segment := range segmentGraphemes(line[i:textEnd]) {
			w := graphemeWidth(segment)
			inRange := currentCol >= startCol && currentCol < endCol
			fits := !strict || currentCol+w <= endCol
			if inRange && fits {
				if pendingAnsi != "" {
					result.WriteString(pendingAnsi)
					pendingAnsi = ""
				}
				result.WriteString(segment)
				resultWidth += w
			}
			currentCol += w
			if currentCol >= endCol {
				break
			}
		}
		i = textEnd
		if currentCol >= endCol {
			break
		}
	}
	return SlicedLine{Text: result.String(), Width: resultWidth}
}
