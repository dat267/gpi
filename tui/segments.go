package tui

// Port of the terminal-image line detection (src/terminal-image.ts) and the
// extractSegments helper (src/utils.ts).

const (
	kittyPrefix  = "\x1b_G"
	iterm2Prefix = "\x1b]1337;File="
)

// IsImageLine reports whether a rendered line carries an inline image escape.
// The fast path checks the sequence at the line start (single-row images); the
// slow path finds sequences elsewhere (multi-row images have a cursor-up
// prefix).
func IsImageLine(line string) bool {
	if len(line) >= len(kittyPrefix) && line[:len(kittyPrefix)] == kittyPrefix {
		return true
	}
	if len(line) >= len(iterm2Prefix) && line[:len(iterm2Prefix)] == iterm2Prefix {
		return true
	}
	return indexOf(line, kittyPrefix) >= 0 || indexOf(line, iterm2Prefix) >= 0
}

func isImageLine(line string) bool { return IsImageLine(line) }

func indexOf(haystack string, needle string) int {
	if len(needle) == 0 {
		return 0
	}
	if len(needle) > len(haystack) {
		return -1
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// ExtractedSegments is the before/after extraction result.
type ExtractedSegments struct {
	Before      string
	BeforeWidth int
	After       string
	AfterWidth  int
}

// ExtractSegments splits a line into a prefix ending at beforeEnd and a suffix
// starting at afterStart (of afterLen columns, when positive). The after
// segment inherits the styling active at afterStart. Graphemes that straddle a
// boundary are dropped. When strictAfter is set, a grapheme extending past the
// after range is dropped too.
func ExtractSegments(line string, beforeEnd int, afterStart int, afterLen int, strictAfter bool) ExtractedSegments {
	before := ""
	beforeWidth := 0
	after := ""
	afterWidth := 0
	currentCol := 0
	i := 0
	pendingAnsiBefore := ""
	afterStarted := false
	afterEnd := afterStart + afterLen

	// Track styling state so "after" inherits styling from before the overlay.
	var styleTracker ansiCodeTracker
	styleTracker.clear()

	for i < len(line) {
		code, length := ExtractANSICode(line, i)
		if length > 0 {
			// Track all SGR codes to know the styling state at afterStart.
			styleTracker.process(code)
			// Include ANSI codes in their respective segments.
			if currentCol < beforeEnd {
				pendingAnsiBefore += code
			} else if currentCol >= afterStart && currentCol < afterEnd && afterStarted {
				// Only include after we've started "after" (styling already
				// prepended).
				after += code
			}
			i += length
			continue
		}

		textEnd := i
		for textEnd < len(line) {
			if _, l := ExtractANSICode(line, textEnd); l > 0 {
				break
			}
			textEnd++
		}

		for _, segment := range segmentGraphemes(line[i:textEnd]) {
			w := graphemeWidth(segment)

			if currentCol < beforeEnd && currentCol+w <= beforeEnd {
				if pendingAnsiBefore != "" {
					before += pendingAnsiBefore
					pendingAnsiBefore = ""
				}
				before += segment
				beforeWidth += w
			} else if currentCol >= afterStart && currentCol < afterEnd {
				fits := !strictAfter || currentCol+w <= afterEnd
				if fits {
					// On the first "after" grapheme, prepend the inherited
					// styling from before the overlay.
					if !afterStarted {
						after += styleTracker.activeCodes()
						afterStarted = true
					}
					after += segment
					afterWidth += w
				}
			}

			currentCol += w
			// Early exit: done with "before" only, or done with both segments.
			if done(currentCol, beforeEnd, afterEnd, afterLen) {
				break
			}
		}
		i = textEnd
		if done(currentCol, beforeEnd, afterEnd, afterLen) {
			break
		}
	}

	return ExtractedSegments{Before: before, BeforeWidth: beforeWidth, After: after, AfterWidth: afterWidth}
}

// done reports the extraction early-exit condition.
func done(currentCol int, beforeEnd int, afterEnd int, afterLen int) bool {
	if afterLen <= 0 {
		return currentCol >= beforeEnd
	}
	return currentCol >= afterEnd
}
