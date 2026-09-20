package tui

import (
	"strings"
)

// Port of the AnsiCodeTracker (src/utils.ts): SGR state tracking so styling
// carries across line breaks, plus OSC 8 hyperlink state.

// osc8Hyperlink is one active OSC 8 hyperlink.
type osc8Hyperlink struct {
	Params string
	URL    string
	// Terminator is BEL or ESC \; some terminals only make BEL-terminated
	// links clickable, so the original terminator is preserved.
	Terminator string
}

// close renders the hyperlink close sequence with the original terminator.
func (h *osc8Hyperlink) close() string {
	if h.Terminator == "\x1b\\" {
		return "\x1b]8;;\x1b\\"
	}
	return "\x1b]8;;\x07"
}

// open renders the hyperlink open sequence.
func (h *osc8Hyperlink) open() string {
	return "\x1b]8;" + h.Params + ";" + h.URL + h.Terminator
}

// parseOsc8Hyperlink parses an OSC 8 hyperlink open sequence; nil for a close.
func parseOsc8Hyperlink(ansiCode string) *osc8Hyperlink {
	// ESC ] 8 ; params ; url ST/BEL
	if !strings.HasPrefix(ansiCode, "\x1b]8;") {
		return nil
	}
	body := strings.TrimPrefix(ansiCode, "\x1b]8;")
	body = strings.TrimSuffix(body, "\x07")
	body = strings.TrimSuffix(body, "\x1b\\")
	parts := strings.SplitN(body, ";", 2)
	if len(parts) != 2 {
		return nil
	}
	terminator := "\x07"
	if strings.HasSuffix(ansiCode, "\x1b\\") {
		terminator = "\x1b\\"
	}
	// An empty URL closes the link.
	if parts[1] == "" {
		return nil
	}
	return &osc8Hyperlink{Params: parts[0], URL: parts[1], Terminator: terminator}
}

// ansiCodeTracker tracks active SGR codes and hyperlink state.
type ansiCodeTracker struct {
	bold, dim, italic, underline          bool
	blink, inverse, hidden, strikethrough bool
	fgColor, bgColor                      string
	activeHyperlink                       *osc8Hyperlink
}

// process consumes one terminal sequence, updating tracked SGR state.
func (t *ansiCodeTracker) process(ansiCode string) {
	if hyperlink := parseOsc8Hyperlink(ansiCode); hyperlink != nil {
		t.activeHyperlink = hyperlink
		return
	}
	if !strings.HasSuffix(ansiCode, "m") {
		return
	}
	// Extract the parameters between ESC [ and m.
	inner := strings.TrimPrefix(ansiCode, "\x1b[")
	inner = strings.TrimSuffix(inner, "m")
	if inner == "" || inner == "0" {
		t.reset()
		return
	}
	parts := strings.Split(inner, ";")
	index := 0
	for index < len(parts) {
		code, ok := parseSGRNumber(parts[index])
		if !ok {
			index++
			continue
		}
		// 256-color (38;5;N / 48;5;N) and RGB (38;2;R;G;B / 48;2;R;G;B)
		// sequences consume multiple parameters.
		if code == 38 || code == 48 {
			if index+2 < len(parts) && parts[index+1] == "5" {
				colorCode := parts[index] + ";" + parts[index+1] + ";" + parts[index+2]
				if code == 38 {
					t.fgColor = colorCode
				} else {
					t.bgColor = colorCode
				}
				index += 3
				continue
			}
			if index+4 < len(parts) && parts[index+1] == "2" {
				colorCode := parts[index] + ";" + parts[index+1] + ";" + parts[index+2] +
					";" + parts[index+3] + ";" + parts[index+4]
				if code == 38 {
					t.fgColor = colorCode
				} else {
					t.bgColor = colorCode
				}
				index += 5
				continue
			}
		}
		switch code {
		case 0:
			t.reset()
		case 1:
			t.bold = true
		case 2:
			t.dim = true
		case 3:
			t.italic = true
		case 4:
			t.underline = true
		case 5:
			t.blink = true
		case 7:
			t.inverse = true
		case 8:
			t.hidden = true
		case 9:
			t.strikethrough = true
		case 21:
			t.bold = false // some terminals
		case 22:
			t.bold = false
			t.dim = false
		case 23:
			t.italic = false
		case 24:
			t.underline = false
		case 25:
			t.blink = false
		case 27:
			t.inverse = false
		case 28:
			t.hidden = false
		case 29:
			t.strikethrough = false
		case 39:
			t.fgColor = ""
		case 49:
			t.bgColor = ""
		default:
			if (code >= 30 && code <= 37) || (code >= 90 && code <= 97) {
				t.fgColor = parts[index]
			} else if (code >= 40 && code <= 47) || (code >= 100 && code <= 107) {
				t.bgColor = parts[index]
			}
		}
		index++
	}
}

func parseSGRNumber(part string) (int, bool) {
	if part == "" {
		return 0, false
	}
	value := 0
	for _, digit := range part {
		if digit < '0' || digit > '9' {
			return 0, false
		}
		value = value*10 + int(digit-'0')
	}
	return value, true
}

func (t *ansiCodeTracker) reset() {
	t.bold, t.dim, t.italic, t.underline = false, false, false, false
	t.blink, t.inverse, t.hidden, t.strikethrough = false, false, false, false
	t.fgColor, t.bgColor = "", ""
	// SGR reset does not affect OSC 8 hyperlink state.
}

// clear resets everything for reuse.
func (t *ansiCodeTracker) clear() {
	t.reset()
	t.activeHyperlink = nil
}

// activeCodes renders the sequence that re-establishes the tracked state.
func (t *ansiCodeTracker) activeCodes() string {
	var codes []string
	if t.bold {
		codes = append(codes, "1")
	}
	if t.dim {
		codes = append(codes, "2")
	}
	if t.italic {
		codes = append(codes, "3")
	}
	if t.underline {
		codes = append(codes, "4")
	}
	if t.blink {
		codes = append(codes, "5")
	}
	if t.inverse {
		codes = append(codes, "7")
	}
	if t.hidden {
		codes = append(codes, "8")
	}
	if t.strikethrough {
		codes = append(codes, "9")
	}
	if t.fgColor != "" {
		codes = append(codes, t.fgColor)
	}
	if t.bgColor != "" {
		codes = append(codes, t.bgColor)
	}
	result := ""
	if len(codes) > 0 {
		result = "\x1b[" + strings.Join(codes, ";") + "m"
	}
	if t.activeHyperlink != nil {
		result += t.activeHyperlink.open()
	}
	return result
}

// activeBackgroundCode returns the background color sequence.
func (t *ansiCodeTracker) activeBackgroundCode() string {
	if t.bgColor != "" {
		return "\x1b[" + t.bgColor + "m"
	}
	return ""
}

// hasActiveCodes reports whether any state is tracked.
func (t *ansiCodeTracker) hasActiveCodes() bool {
	return t.bold || t.dim || t.italic || t.underline || t.blink || t.inverse ||
		t.hidden || t.strikethrough || t.fgColor != "" || t.bgColor != "" ||
		t.activeHyperlink != nil
}

// lineEndReset returns the reset codes needed at line end: underline must close
// to prevent bleeding into padding, and active hyperlinks must close (and be
// re-opened on the next line).
func (t *ansiCodeTracker) lineEndReset() string {
	result := ""
	if t.underline {
		result += "\x1b[24m"
	}
	if t.activeHyperlink != nil {
		result += t.activeHyperlink.close()
	}
	return result
}

// updateTrackerFromText feeds a text fragment's sequences into the tracker.
func updateTrackerFromText(text string, tracker *ansiCodeTracker) {
	index := 0
	for index < len(text) {
		code, length := ExtractANSICode(text, index)
		if code == "" {
			index++
			continue
		}
		tracker.process(code)
		index += length
	}
}
