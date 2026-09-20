package tui

import (
	"strings"
)

// Port of the wrapping half of src/utils.ts: ANSI-aware word wrapping.

// cjkBreakRegex is satisfied by CJK ideographs and kana/hangul, which may
// break anywhere.
func cjkBreak(r rune) bool {
	switch {
	case r >= 0x2E80 && r <= 0x303F, // CJK radicals through CJK symbols
		r >= 0x3040 && r <= 0x30FF,   // hiragana, katakana
		r >= 0x3105 && r <= 0x312F,   // bopomofo
		r >= 0x3130 && r <= 0x318F,   // compatibility hangul
		r >= 0x3400 && r <= 0x4DBF,   // CJK ext A
		r >= 0x4E00 && r <= 0x9FFF,   // CJK unified
		r >= 0xAC00 && r <= 0xD7AF,   // hangul syllables
		r >= 0xF900 && r <= 0xFAFF,   // CJK compatibility
		r >= 0x20000 && r <= 0x2FA1F: // ext B through compat supplement
		return true
	}
	return false
}

// cjkPunctuation breaks prose from completions.
func cjkPunctuation(r rune) bool {
	switch r {
	case 0xFF0C, 0xFF0E, 0xFF1A, 0xFF1B, 0xFF01, 0xFF1F, 0xFF08, 0xFF09,
		0xFF3B, 0xFF3D, 0xFF5B, 0xFF5D, 0x201C, 0x201D, 0x2018, 0x2019,
		0x2026, 0x2014:
		return true
	}
	return false
}

// isSpaceRune reports a plain space (the wrap tokenizer only splits on the
// space character).
func isSpaceRune(r rune) bool { return r == ' ' }

// isPunctuationRune reports ASCII punctuation.
func isPunctuationRune(r rune) bool { return punctuationChars[r] }

// splitIntoTokensWithAnsi splits into word/space tokens, keeping ANSI codes
// attached to the following visible content. CJK characters break anywhere.
func splitIntoTokensWithAnsi(text string) []string {
	var tokens []string
	var current strings.Builder
	pendingAnsi := ""
	currentKind := "" // "space" | "word" | ""

	flushCurrent := func() {
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
			currentKind = ""
		}
	}

	index := 0
	for index < len(text) {
		if code, length := ExtractANSICode(text, index); code != "" {
			pendingAnsi += code
			index += length
			continue
		}

		end := index
		for end < len(text) {
			if code, _ := ExtractANSICode(text, end); code != "" {
				break
			}
			end++
		}

		for _, segment := range segmentGraphemes(text[index:end]) {
			runes := []rune(segment)
			segmentIsSpace := isSpaceRune(runes[0]) && len(runes) == 1
			if !segmentIsSpace && (cjkBreak(runes[0]) || cjkPunctuation(runes[0])) {
				flushCurrent()
				tokens = append(tokens, pendingAnsi+segment)
				pendingAnsi = ""
				continue
			}
			segmentKind := "word"
			if segmentIsSpace {
				segmentKind = "space"
			}
			if current.Len() > 0 && currentKind != segmentKind {
				flushCurrent()
			}
			if pendingAnsi != "" {
				current.WriteString(pendingAnsi)
				pendingAnsi = ""
			}
			currentKind = segmentKind
			current.WriteString(segment)
		}

		index = end
	}

	// Remaining pending ANSI codes attach to the last token.
	if pendingAnsi != "" {
		if current.Len() > 0 {
			current.WriteString(pendingAnsi)
		} else if len(tokens) > 0 {
			tokens[len(tokens)-1] += pendingAnsi
		} else {
			current.WriteString(pendingAnsi)
		}
	}
	if current.Len() > 0 {
		tokens = append(tokens, current.String())
	}
	return tokens
}

// breakLongWord breaks an over-long token, re-establishing styles per piece.
func breakLongWord(word string, maxWidth int, tracker *ansiCodeTracker) []string {
	var pieces []string
	current := tracker.activeCodes()
	currentWidth := 0
	index := 0
	for index < len(word) {
		if code, length := ExtractANSICode(word, index); code != "" {
			current += code
			tracker.process(code)
			index += length
			continue
		}
		end := index
		for end < len(word) {
			if code, _ := ExtractANSICode(word, end); code != "" {
				break
			}
			end++
		}
		for _, segment := range segmentGraphemes(word[index:end]) {
			w := graphemeWidth(segment)
			if currentWidth+w > maxWidth {
				pieces = append(pieces, current)
				current = tracker.activeCodes()
				currentWidth = 0
			}
			current += segment
			currentWidth += w
		}
		index = end
	}
	if current != "" {
		pieces = append(pieces, current)
	}
	if len(pieces) == 0 {
		pieces = []string{""}
	}
	return pieces
}

// WrapTextWithAnsi wraps text to width with ANSI codes preserved: word
// wrapping only, no padding, no background colors. Active ANSI codes are
// carried across line breaks and newlines in the input.
func WrapTextWithAnsi(text string, width int) []string {
	if text == "" {
		return []string{""}
	}

	// Newlines: each line wraps separately with ANSI state carried over.
	inputLines := splitLines(text)
	var result []string
	tracker := &ansiCodeTracker{}

	for _, inputLine := range inputLines {
		prefix := ""
		if len(result) > 0 {
			prefix = tracker.activeCodes()
		}
		wrappedLines := wrapSingleLine(prefix+inputLine, width)
		result = append(result, wrappedLines...)
		updateTrackerFromText(inputLine, tracker)
	}

	if len(result) == 0 {
		return []string{""}
	}
	return result
}

// splitLines splits on \r\n, \r, and \n.
func splitLines(text string) []string {
	lines := strings.Split(text, "\n")
	for index, line := range lines {
		lines[index] = strings.TrimSuffix(line, "\r")
	}
	return lines
}

func wrapSingleLine(line string, width int) []string {
	if line == "" {
		return []string{""}
	}
	visibleLength := VisibleWidth(line)
	if visibleLength <= width {
		return []string{line}
	}

	var wrapped []string
	tracker := &ansiCodeTracker{}
	tokens := splitIntoTokensWithAnsi(line)

	var currentLine strings.Builder
	currentVisibleLength := 0

	for _, token := range tokens {
		tokenVisibleLength := VisibleWidth(token)
		tokenRunes := []rune(StripTerminalSequences(token))
		isWhitespace := len(tokenRunes) > 0 && isSpaceRune(tokenRunes[0]) &&
			strings.TrimSpace(StripTerminalSequences(token)) == ""

		// A token longer than the width breaks character by character.
		if tokenVisibleLength > width && !isWhitespace {
			if currentLine.Len() > 0 {
				if lineEndReset := tracker.lineEndReset(); lineEndReset != "" {
					currentLine.WriteString(lineEndReset)
				}
				wrapped = append(wrapped, currentLine.String())
				currentLine.Reset()
				currentVisibleLength = 0
			}
			broken := breakLongWord(token, width, tracker)
			for index := 0; index < len(broken)-1; index++ {
				wrapped = append(wrapped, broken[index])
			}
			currentLine.WriteString(broken[len(broken)-1])
			currentVisibleLength = VisibleWidth(currentLine.String())
			continue
		}

		// Adding the token would exceed the width: wrap.
		if currentVisibleLength+tokenVisibleLength > width && currentVisibleLength > 0 {
			lineToWrap := strings.TrimRight(currentLine.String(), " \t")
			if lineEndReset := tracker.lineEndReset(); lineEndReset != "" {
				lineToWrap += lineEndReset
			}
			wrapped = append(wrapped, lineToWrap)
			if isWhitespace {
				// Do not start a new line with whitespace.
				currentLine.Reset()
				currentLine.WriteString(tracker.activeCodes())
				currentVisibleLength = 0
			} else {
				currentLine.Reset()
				currentLine.WriteString(tracker.activeCodes() + token)
				currentVisibleLength = tokenVisibleLength
			}
		} else {
			currentLine.WriteString(token)
			currentVisibleLength += tokenVisibleLength
		}

		updateTrackerFromText(token, tracker)
	}

	if currentLine.Len() > 0 {
		// No reset on the final line; the caller handles it.
		wrapped = append(wrapped, currentLine.String())
	}

	// Trailing whitespace can push lines past the requested width.
	if len(wrapped) > 0 {
		trimmed := make([]string, 0, len(wrapped))
		for _, line := range wrapped {
			trimmed = append(trimmed, strings.TrimRight(line, " \t"))
		}
		return trimmed
	}
	return []string{""}
}
