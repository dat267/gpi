package tui

import (
	"strings"
)

// Port of the overlay layout half of src/tui.ts: anchor/percentage/absolute
// positioning with margins, and line compositing.

// OverlayAnchor is the anchor position for overlays.
type OverlayAnchor = string

const (
	OverlayAnchorCenter       OverlayAnchor = "center"
	OverlayAnchorTopLeft      OverlayAnchor = "top-left"
	OverlayAnchorTopRight     OverlayAnchor = "top-right"
	OverlayAnchorBottomLeft   OverlayAnchor = "bottom-left"
	OverlayAnchorBottomRight  OverlayAnchor = "bottom-right"
	OverlayAnchorTopCenter    OverlayAnchor = "top-center"
	OverlayAnchorBottomCenter OverlayAnchor = "bottom-center"
	OverlayAnchorLeftCenter   OverlayAnchor = "left-center"
	OverlayAnchorRightCenter  OverlayAnchor = "right-center"
)

// OverlayMargin is the margin from terminal edges.
type OverlayMargin struct {
	Top    int
	Right  int
	Bottom int
	Left   int
}

// OverlayOptions configure overlay positioning and sizing. Size values can be
// absolute (int) or percentage (Percent).
type OverlayOptions struct {
	// Width in columns, or percentage of terminal width.
	Width        *int
	PercentWidth string
	MinWidth     *int
	// MaxHeight in rows, or percentage of terminal height.
	MaxHeight        *int
	PercentMaxHeight string
	// Anchor point for positioning (default center).
	Anchor OverlayAnchor
	// Offsets from the anchor position (positive = right/down).
	OffsetX int
	OffsetY int
	// Row position: absolute number, or percentage string (e.g. "25%").
	Row        *int
	RowPercent string
	// Column position: absolute number, or percentage string.
	Col        *int
	ColPercent string
	// Margin from terminal edges.
	Margin OverlayMargin
	// Visible controls overlay visibility from the terminal dimensions.
	Visible func(termWidth int, termHeight int) bool
	// NonCapturing suppresses focus capture when the overlay is shown.
	NonCapturing bool
}

// OverlayBounds is the last rendered terminal-relative overlay rectangle.
type OverlayBounds struct {
	Row    int
	Col    int
	Width  int
	Height int
}

// parsePercentValue parses "NN%" strings; ok is false otherwise.
func parsePercentValue(value string) (float64, bool) {
	if !strings.HasSuffix(value, "%") {
		return 0, false
	}
	number := strings.TrimSuffix(value, "%")
	if number == "" {
		return 0, false
	}
	negative := strings.HasPrefix(number, "-")
	number = strings.TrimPrefix(number, "-")
	parts := strings.SplitN(number, ".", 2)
	integer := ""
	for _, digit := range parts[0] {
		if digit < '0' || digit > '9' {
			return 0, false
		}
		integer += string(digit)
	}
	fraction := ""
	if len(parts) == 2 {
		for _, digit := range parts[1] {
			if digit < '0' || digit > '9' {
				return 0, false
			}
			fraction += string(digit)
		}
	}
	value_ := 0.0
	for _, digit := range integer {
		value_ = value_*10 + float64(digit-'0')
	}
	scale := 0.1
	for _, digit := range fraction {
		value_ += float64(digit-'0') * scale
		scale *= 0.1
	}
	if negative {
		value_ = -value_
	}
	return value_, true
}

// resolveOverlayLayout resolves the overlay width and position from options.
type overlayLayout struct {
	Width     int
	Row       int
	Col       int
	MaxHeight int // -1 when unset
	HasMax    bool
}

func resolveOverlayLayout(options *OverlayOptions, overlayHeight int, termWidth int, termHeight int) overlayLayout {
	opt := OverlayOptions{}
	if options != nil {
		opt = *options
	}

	marginTop := maxInt(0, opt.Margin.Top)
	marginRight := maxInt(0, opt.Margin.Right)
	marginBottom := maxInt(0, opt.Margin.Bottom)
	marginLeft := maxInt(0, opt.Margin.Left)

	availWidth := maxInt(1, termWidth-marginLeft-marginRight)
	availHeight := maxInt(1, termHeight-marginTop-marginBottom)

	// Width: percentage of the terminal, else min(80, available).
	layoutWidth := 0
	hasWidth := false
	if opt.Width != nil {
		layoutWidth = *opt.Width
		hasWidth = true
	} else if opt.PercentWidth != "" {
		if percent, ok := parsePercentValue(opt.PercentWidth); ok {
			layoutWidth = int(float64(termWidth) * percent / 100)
			hasWidth = true
		}
	}
	if !hasWidth {
		layoutWidth = minInt(80, availWidth)
	}
	if opt.MinWidth != nil {
		layoutWidth = maxInt(layoutWidth, *opt.MinWidth)
	}
	layoutWidth = maxInt(1, minInt(layoutWidth, availWidth))

	// Max height: percentage of the terminal, clamped to available space.
	maxHeight := 0
	hasMax := false
	if opt.MaxHeight != nil {
		maxHeight = *opt.MaxHeight
		hasMax = true
	} else if opt.PercentMaxHeight != "" {
		if percent, ok := parsePercentValue(opt.PercentMaxHeight); ok {
			maxHeight = int(float64(termHeight) * percent / 100)
			hasMax = true
		}
	}
	if hasMax {
		maxHeight = maxInt(1, minInt(maxHeight, availHeight))
	}

	effectiveHeight := overlayHeight
	if hasMax {
		effectiveHeight = minInt(overlayHeight, maxHeight)
	}

	// Row: explicit, percentage, or anchor-based.
	row := 0
	switch {
	case opt.Row != nil:
		row = *opt.Row
	case opt.RowPercent != "":
		if percent, ok := parsePercentValue(opt.RowPercent); ok {
			maxRow := maxInt(0, availHeight-effectiveHeight)
			row = marginTop + int(float64(maxRow)*percent/100)
		} else {
			row = resolveAnchorRow(OverlayAnchorCenter, effectiveHeight, availHeight, marginTop)
		}
	default:
		anchor := opt.Anchor
		if anchor == "" {
			anchor = OverlayAnchorCenter
		}
		row = resolveAnchorRow(anchor, effectiveHeight, availHeight, marginTop)
	}

	col := 0
	switch {
	case opt.Col != nil:
		col = *opt.Col
	case opt.ColPercent != "":
		if percent, ok := parsePercentValue(opt.ColPercent); ok {
			maxCol := maxInt(0, availWidth-layoutWidth)
			col = marginLeft + int(float64(maxCol)*percent/100)
		} else {
			col = resolveAnchorCol(OverlayAnchorCenter, layoutWidth, availWidth, marginLeft)
		}
	default:
		anchor := opt.Anchor
		if anchor == "" {
			anchor = OverlayAnchorCenter
		}
		col = resolveAnchorCol(anchor, layoutWidth, availWidth, marginLeft)
	}

	// Offsets, then clamping within the margins.
	row += opt.OffsetY
	col += opt.OffsetX
	row = maxInt(marginTop, minInt(row, termHeight-marginBottom-effectiveHeight))
	col = maxInt(marginLeft, minInt(col, termWidth-marginRight-layoutWidth))

	return overlayLayout{Width: layoutWidth, Row: row, Col: col, MaxHeight: maxHeight, HasMax: hasMax}
}

func resolveAnchorRow(anchor OverlayAnchor, height int, availHeight int, marginTop int) int {
	switch anchor {
	case OverlayAnchorTopLeft, OverlayAnchorTopCenter, OverlayAnchorTopRight:
		return marginTop
	case OverlayAnchorBottomLeft, OverlayAnchorBottomCenter, OverlayAnchorBottomRight:
		return marginTop + availHeight - height
	default: // center
		return marginTop + (availHeight-height)/2
	}
}

func resolveAnchorCol(anchor OverlayAnchor, width int, availWidth int, marginLeft int) int {
	switch anchor {
	case OverlayAnchorTopLeft, OverlayAnchorLeftCenter, OverlayAnchorBottomLeft:
		return marginLeft
	case OverlayAnchorTopRight, OverlayAnchorRightCenter, OverlayAnchorBottomRight:
		return marginLeft + availWidth - width
	default: // center
		return marginLeft + (availWidth-width)/2
	}
}

// segmentReset is the reset appended around composited overlay content.
const segmentReset = "\x1b[0m\x1b]8;;\x07"

// CompositeTuiLine composites overlay content into a terminal line at a fixed
// column. Image lines pass through untouched.
func CompositeTuiLine(baseLine string, overlayLine string, startCol int, overlayWidth int, totalWidth int) string {
	if isImageLine(baseLine) {
		return baseLine
	}

	afterStart := startCol + overlayWidth
	base := ExtractSegments(baseLine, startCol, afterStart, totalWidth-afterStart, true)
	overlay := SliceWithWidth(overlayLine, 0, overlayWidth, true)
	beforePad := maxInt(0, startCol-base.BeforeWidth)
	overlayPad := maxInt(0, overlayWidth-overlay.Width)
	actualBeforeWidth := maxInt(startCol, base.BeforeWidth)
	actualOverlayWidth := maxInt(overlayWidth, overlay.Width)
	afterTarget := maxInt(0, totalWidth-actualBeforeWidth-actualOverlayWidth)
	afterPad := maxInt(0, afterTarget-base.AfterWidth)

	var result strings.Builder
	result.WriteString(base.Before)
	result.WriteString(strings.Repeat(" ", beforePad))
	result.WriteString(segmentReset)
	result.WriteString(overlay.Text)
	result.WriteString(strings.Repeat(" ", overlayPad))
	result.WriteString(segmentReset)
	result.WriteString(base.After)
	result.WriteString(strings.Repeat(" ", afterPad))

	if VisibleWidth(result.String()) <= totalWidth {
		return result.String()
	}
	return SliceByColumn(result.String(), 0, totalWidth, true)
}

// minInt and maxInt mirror Math.min/Math.max's variadic form.
func minInt(values ...int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value < result {
			result = value
		}
	}
	return result
}

func maxInt(values ...int) int {
	result := values[0]
	for _, value := range values[1:] {
		if value > result {
			result = value
		}
	}
	return result
}
