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
// default-background reset stays on the page, so the terminal's own background
// never shows through and the palette owns the whole surface (D165).
//
// Over a low-bandwidth link the fill is drawn with an erase-to-end-of-line
// instead of padding spaces: the row ends up identical on screen, but no
// padding cells go on the wire (this is the D163 trim's budget, kept while the
// background is filled).
func ApplyPageBackground(line string, lineWidth int, bgAnsi string) string {
	if bgAnsi == "" || bgAnsi == "\x1b[49m" {
		return ApplyBackgroundToLine(line, lineWidth, nil)
	}
	line = strings.ReplaceAll(line, "\x1b[49m", bgAnsi)
	if LowBandwidth() {
		// Set the background, erase the whole row onto it, then draw the text:
		// the row is filled with no padding cells on the wire (the D163 trim's
		// budget, kept while the background is filled).
		return bgAnsi + "\x1b[2K" + line + "\x1b[49m"
	}
	padding := lineWidth - VisibleWidth(line)
	if padding < 0 {
		padding = 0
	}
	return bgAnsi + line + strings.Repeat(" ", padding) + "\x1b[49m"
}
