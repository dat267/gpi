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

// ApplyPageBackground makes bgAnsi the base background of a frame line. Every
// default-background reset stays on the page, and the line is padded to the
// viewport width, so the terminal's own background never shows through and the
// palette owns the whole surface (D165).
func ApplyPageBackground(line string, lineWidth int, bgAnsi string) string {
	if bgAnsi == "" || bgAnsi == "\x1b[49m" {
		return ApplyBackgroundToLine(line, lineWidth, nil)
	}
	line = strings.ReplaceAll(line, "\x1b[49m", bgAnsi)
	padding := lineWidth - VisibleWidth(line)
	if padding < 0 {
		padding = 0
	}
	return bgAnsi + line + strings.Repeat(" ", padding) + "\x1b[49m"
}
