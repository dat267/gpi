package tui

import "strings"

// Port of the grapheme-cell and styling helpers from src/utils.ts used by the
// layout painter (getGraphemeCellRange, getOsc8LinkAtColumn,
// getActiveBackgroundAnsi).

// GraphemeCellRange is a grapheme's visible cell span.
type GraphemeCellRange struct {
	Start int
	End   int
}

// GetGraphemeCellRange returns the cell range of the grapheme covering a
// visible column.
func GetGraphemeCellRange(line string, column int) (GraphemeCellRange, bool) {
	currentCol := 0
	i := 0
	for i < len(line) {
		if _, length := ExtractANSICode(line, i); length > 0 {
			i += length
			continue
		}
		textEnd := i
		for textEnd < len(line) {
			if _, length := ExtractANSICode(line, textEnd); length > 0 {
				break
			}
			textEnd++
		}
		for _, segment := range segmentGraphemes(line[i:textEnd]) {
			width := graphemeWidth(segment)
			if width > 0 && column >= currentCol && column < currentCol+width {
				return GraphemeCellRange{Start: currentCol, End: currentCol + width}, true
			}
			currentCol += width
		}
		i = textEnd
	}
	return GraphemeCellRange{}, false
}

// GetOsc8LinkAtColumn returns the OSC 8 hyperlink covering a visible column.
func GetOsc8LinkAtColumn(line string, column int) (string, bool) {
	activeURL := ""
	i := 0
	currentCol := 0
	for i < len(line) {
		if code, length := ExtractANSICode(line, i); length > 0 {
			if hyperlink := parseOsc8Hyperlink(code); hyperlink != nil {
				activeURL = hyperlink.URL
			} else if strings.HasPrefix(code, "\x1b]8;") {
				// A close sequence clears the active link.
				activeURL = ""
			}
			i += length
			continue
		}
		textEnd := i
		for textEnd < len(line) {
			if _, length := ExtractANSICode(line, textEnd); length > 0 {
				break
			}
			textEnd++
		}
		for _, segment := range segmentGraphemes(line[i:textEnd]) {
			width := graphemeWidth(segment)
			if segment == "\t" {
				width = 3
			}
			if column >= currentCol && column < currentCol+width {
				if activeURL != "" {
					return activeURL, true
				}
				return "", false
			}
			currentCol += width
		}
		i = textEnd
	}
	return "", false
}

// GetActiveBackgroundAnsi returns the background escape active at the end of
// the text.
func GetActiveBackgroundAnsi(text string) string {
	var tracker ansiCodeTracker
	updateTrackerFromText(text, &tracker)
	return tracker.activeBackgroundCode()
}

// stripOSC133ZonePrefix removes leading OSC 133 prompt/command markers.
func stripOSC133ZonePrefix(line string) string {
	for {
		trimmed, ok := stripOneOSC133ZonePrefix(line)
		if !ok {
			return line
		}
		line = trimmed
	}
}

func stripOneOSC133ZonePrefix(line string) (string, bool) {
	for _, kind := range []string{"A", "B", "C"} {
		prefix := "\x1b]133;" + kind
		if strings.HasPrefix(line, prefix+"\x07") {
			return line[len(prefix)+1:], true
		}
		if strings.HasPrefix(line, prefix+"\x1b\\") {
			return line[len(prefix)+2:], true
		}
	}
	return line, false
}
