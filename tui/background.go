package tui

import (
	"strings"
)

// Port of applyBackgroundToLine (src/utils.ts): applying a background color
// function to the content plus padding (the padding is inside the background).

// ApplyBackgroundToLine applies the background to a line, padding to width.
func ApplyBackgroundToLine(line string, lineWidth int, bgFn func(text string) string) string {
	padding := lineWidth - VisibleWidth(line)
	if padding < 0 {
		padding = 0
	}
	withPadding := line + strings.Repeat(" ", padding)
	if bgFn == nil {
		return withPadding
	}
	return bgFn(withPadding)
}
