package tui

// Port of src/layout.ts: the box-tree layout engine, the screen painter, and
// the hit-testing helpers.

// LayoutRect is a rectangle in screen cells.
type LayoutRect struct {
	X      int
	Y      int
	Width  int
	Height int
}

// LayoutBox is a laid-out component rectangle with its clip and children.
type LayoutBox struct {
	Component Component
	Rect      LayoutRect
	Clip      LayoutRect
	Children  []*LayoutBox
	Parent    *LayoutBox
	// Lines holds the component's rendered lines for leaf boxes.
	Lines              []string
	HasLines           bool
	LineOffset         int
	ScrollView         *ScrollView
	ScrollContentLines []string
	HasScrollContent   bool
	Layer              int
}

// LayoutFrame is the result of a layout pass.
type LayoutFrame struct {
	Root              *LayoutBox
	Width             int
	Height            int
	Lines             []string
	PrimaryScrollView *ScrollView
}

// ScrollbarGeometry is the resolved scrollbar rectangle.
type ScrollbarGeometry struct {
	Column       int
	TrackTop     int
	TrackHeight  int
	ThumbTop     int
	ThumbHeight  int
	MaxScrollTop int
}

type layoutContext struct {
	viewport          LayoutViewport
	renderCache       map[Component]map[int][]string
	requestRender     func()
	primaryScrollView *ScrollView
}

func intersectRect(a LayoutRect, b LayoutRect) LayoutRect {
	x := maxInt(a.X, b.X)
	y := maxInt(a.Y, b.Y)
	right := minInt(a.X+a.Width, b.X+b.Width)
	bottom := minInt(a.Y+a.Height, b.Y+b.Height)
	return LayoutRect{X: x, Y: y, Width: maxInt(0, right-x), Height: maxInt(0, bottom-y)}
}

func renderCached(context *layoutContext, component Component, width int) []string {
	safeWidth := maxInt(1, width)
	widths, ok := context.renderCache[component]
	if !ok {
		widths = map[int][]string{}
		context.renderCache[component] = widths
	}
	if lines, ok := widths[safeWidth]; ok {
		return lines
	}
	lines := component.Render(safeWidth)
	widths[safeWidth] = lines
	return lines
}

func measureHeight(context *layoutContext, component Component, width int) int {
	return len(renderCached(context, component, width))
}

func measureWidth(context *layoutContext, component Component, width int) int {
	maxWidth := 0
	for _, line := range renderCached(context, component, width) {
		if visible := VisibleWidth(line); visible > maxWidth {
			maxWidth = visible
		}
	}
	return maxWidth
}

func translateBox(box *LayoutBox, deltaY int) {
	box.Rect.Y += deltaY
	for _, child := range box.Children {
		translateBox(child, deltaY)
	}
}

func updateClips(box *LayoutBox, parentClip LayoutRect) {
	box.Clip = intersectRect(parentClip, box.Rect)
	for _, child := range box.Children {
		updateClips(child, box.Clip)
	}
}

func layoutComponent(
	context *layoutContext,
	component Component,
	x int,
	y int,
	width int,
	height *int,
	clip LayoutRect,
) *LayoutBox {
	safeWidth := maxInt(1, width)
	node, hasNode := GetLayoutNode(component)
	if !hasNode {
		lines := renderCached(context, component, safeWidth)
		allocatedHeight := len(lines)
		if height != nil {
			allocatedHeight = maxInt(0, *height)
		}
		lineOffset := 0
		if len(lines) > allocatedHeight && allocatedHeight > 0 {
			cursorLine := -1
			for index, line := range lines {
				if indexOf(line, CursorMarker) != -1 {
					cursorLine = index
					break
				}
			}
			if cursorLine >= allocatedHeight {
				lineOffset = cursorLine - allocatedHeight + 1
			}
		}
		rect := LayoutRect{X: x, Y: y, Width: safeWidth, Height: allocatedHeight}
		return &LayoutBox{
			Component:  component,
			Rect:       rect,
			Clip:       intersectRect(clip, rect),
			Lines:      lines,
			HasLines:   true,
			LineOffset: lineOffset,
		}
	}

	if node.Kind == "scroll" {
		state := node.Scroll.State
		previousScrollTop := state.ScrollTop()
		contentWidth := state.GetContentWidth(safeWidth)
		childBox := layoutComponent(context, node.Scroll.Component, x, y-previousScrollTop, contentWidth, nil, clip)
		contentHeight := childBox.Rect.Height
		viewportHeight := contentHeight
		if height != nil {
			viewportHeight = maxInt(0, *height)
		}
		state.UpdateLayout(contentHeight, viewportHeight, context.requestRender)
		translateBox(childBox, previousScrollTop-state.ScrollTop())
		scrollView, _ := state.(*ScrollView)
		if state.Primary() || context.primaryScrollView == nil {
			context.primaryScrollView = scrollView
		}
		rect := LayoutRect{X: x, Y: y, Width: safeWidth, Height: viewportHeight}
		childClip := intersectRect(clip, rect)
		box := &LayoutBox{
			Component:          component,
			Rect:               rect,
			Clip:               childClip,
			Children:           []*LayoutBox{childBox},
			ScrollView:         scrollView,
			ScrollContentLines: renderCached(context, node.Scroll.Component, contentWidth),
			HasScrollContent:   true,
		}
		childBox.Parent = box
		updateClips(childBox, childClip)
		return box
	}

	entries := VisibleStackEntries(node.Stack.Entries, context.viewport)
	gapTotal := maxInt(0, len(entries)-1) * node.Stack.Gap

	if node.Kind == "vstack" {
		intrinsicHeights := make([]int, len(entries))
		for index, entry := range entries {
			if entry.Basis != nil && entry.Basis.Mode == "fixed" {
				intrinsicHeights[index] = entry.Basis.Value
			} else {
				intrinsicHeights[index] = measureHeight(context, entry.Component, safeWidth)
			}
		}
		sizes := AllocateStackSizes(entries, intrinsicHeights, height, node.Stack.Gap)
		naturalHeight := gapTotal
		for _, size := range sizes {
			naturalHeight += size
		}
		allocatedHeight := naturalHeight
		if height != nil {
			allocatedHeight = maxInt(0, *height)
		}
		rect := LayoutRect{X: x, Y: y, Width: safeWidth, Height: allocatedHeight}
		box := &LayoutBox{Component: component, Rect: rect, Clip: intersectRect(clip, rect)}
		childY := y
		for index, entry := range entries {
			childHeight := sizes[index]
			child := layoutComponent(context, entry.Component, x, childY, safeWidth, &childHeight, box.Clip)
			child.Parent = box
			box.Children = append(box.Children, child)
			childY += sizes[index] + node.Stack.Gap
		}
		return box
	}

	// hstack
	intrinsicWidths := make([]int, len(entries))
	for index, entry := range entries {
		if entry.Basis != nil && entry.Basis.Mode == "fixed" {
			intrinsicWidths[index] = entry.Basis.Value
		} else {
			intrinsicWidths[index] = measureWidth(context, entry.Component, safeWidth)
		}
	}
	widths := AllocateStackSizes(entries, intrinsicWidths, &safeWidth, node.Stack.Gap)
	intrinsicHeights := make([]int, len(entries))
	for index, entry := range entries {
		intrinsicHeights[index] = measureHeight(context, entry.Component, maxInt(1, widths[index]))
	}
	allocatedHeight := 0
	if height != nil {
		allocatedHeight = maxInt(0, *height)
	} else {
		for _, childHeight := range intrinsicHeights {
			allocatedHeight = maxInt(allocatedHeight, childHeight)
		}
	}
	rect := LayoutRect{X: x, Y: y, Width: safeWidth, Height: allocatedHeight}
	box := &LayoutBox{Component: component, Rect: rect, Clip: intersectRect(clip, rect)}
	childX := x
	for index, entry := range entries {
		naturalChildHeight := intrinsicHeights[index]
		childHeight := minInt(allocatedHeight, naturalChildHeight)
		if node.Stack.Align == "stretch" {
			childHeight = allocatedHeight
		}
		childY := y
		if node.Stack.Align == "center" {
			childY += (allocatedHeight - childHeight) / 2
		} else if node.Stack.Align == "end" {
			childY += allocatedHeight - childHeight
		}
		childWidth := widths[index]
		if childWidth == 0 {
			box.Children = append(box.Children, &LayoutBox{
				Component: entry.Component,
				Rect:      LayoutRect{X: childX, Y: childY, Width: 0, Height: childHeight},
				Clip:      LayoutRect{X: childX, Y: childY, Width: 0, Height: 0},
				Parent:    box,
			})
		} else {
			child := layoutComponent(context, entry.Component, childX, childY, childWidth, &childHeight, box.Clip)
			child.Parent = box
			box.Children = append(box.Children, child)
		}
		childX += childWidth + node.Stack.Gap
	}
	return box
}

func replaceScrollbarCell(
	line string,
	column int,
	totalWidth int,
	replacement string,
	preserveTargetBackground bool,
) string {
	if IsImageLine(line) {
		return line
	}

	start := column
	end := column + 1
	if cellRange, ok := GetGraphemeCellRange(line, column); ok {
		start = cellRange.Start
		end = cellRange.End
	}
	before := SliceByColumn(line, 0, start, true)
	target := SliceByColumn(line, start, end-start, true)
	after := SliceByColumn(line, end, maxInt(0, totalWidth-end), true)

	targetPrefix := ""
	targetIndex := 0
	for targetIndex < len(target) {
		code, length := ExtractANSICode(target, targetIndex)
		if length == 0 {
			break
		}
		targetPrefix += code
		targetIndex += length
	}
	beforePadding := repeatSpaces(maxInt(0, start-VisibleWidth(before)))
	cellPaddingBefore := repeatSpaces(maxInt(0, column-start))
	cellPaddingAfter := repeatSpaces(maxInt(0, end-column-1))
	targetStyle := segmentReset
	if preserveTargetBackground {
		targetStyle += GetActiveBackgroundAnsi(targetPrefix)
	}
	return before + beforePadding + targetStyle + cellPaddingBefore + replacement + cellPaddingAfter + after
}

func repeatSpaces(count int) string {
	if count <= 0 {
		return ""
	}
	return spaces[0:count]
}

// GetScrollbarGeometry resolves the scrollbar rectangle for a scroll box.
func GetScrollbarGeometry(box *LayoutBox, includeHiddenAuto bool) (ScrollbarGeometry, bool) {
	if box.ScrollView == nil || box.Rect.Width <= 0 || box.Rect.Height <= 0 {
		return ScrollbarGeometry{}, false
	}

	contentHeight := 0
	if len(box.Children) > 0 {
		contentHeight = box.Children[0].Rect.Height
	} else if box.HasScrollContent {
		contentHeight = len(box.ScrollContentLines)
	}
	trackHeight := box.Rect.Height
	canRevealHiddenAuto := includeHiddenAuto && box.ScrollView.Scrollbar() == ScrollbarAuto && contentHeight > trackHeight
	if !box.ScrollView.IsScrollbarVisible() && !canRevealHiddenAuto {
		return ScrollbarGeometry{}, false
	}

	minThumbHeight := minInt(2, trackHeight)
	thumbHeight := maxInt(minThumbHeight, minInt(trackHeight, int(roundHalfUp(float64(trackHeight*trackHeight)/float64(contentHeight)))))
	maxScrollTop := maxInt(0, contentHeight-trackHeight)
	maxThumbTop := trackHeight - thumbHeight
	thumbOffset := 0
	if maxScrollTop != 0 {
		thumbOffset = int(roundHalfUp(float64(box.ScrollView.ScrollTop()) / float64(maxScrollTop) * float64(maxThumbTop)))
	}
	column := box.Rect.X + box.Rect.Width - 1
	if column < box.Clip.X || column >= box.Clip.X+box.Clip.Width {
		return ScrollbarGeometry{}, false
	}

	return ScrollbarGeometry{
		Column:       column,
		TrackTop:     box.Rect.Y,
		TrackHeight:  trackHeight,
		ThumbTop:     box.Rect.Y + thumbOffset,
		ThumbHeight:  thumbHeight,
		MaxScrollTop: maxScrollTop,
	}, true
}

// roundHalfUp mirrors Math.round (half away from zero for positives).
func roundHalfUp(value float64) float64 {
	if value < 0 {
		return -roundHalfUp(-value)
	}
	return float64(int64(value + 0.5))
}

func paintScrollbar(box *LayoutBox, screen []string, totalWidth int) {
	geometry, ok := GetScrollbarGeometry(box, false)
	if !ok || box.ScrollView == nil {
		return
	}

	for offset := 0; offset < geometry.TrackHeight; offset++ {
		row := geometry.TrackTop + offset
		if row < box.Clip.Y || row >= box.Clip.Y+box.Clip.Height || row < 0 || row >= len(screen) {
			continue
		}
		isThumb := row >= geometry.ThumbTop && row < geometry.ThumbTop+geometry.ThumbHeight
		base := screen[row]
		replacement := ""
		if isThumb {
			thumb := "┃"
			if box.ScrollView.IsScrollbarActive() {
				thumb = "█"
			}
			replacement = box.ScrollView.thumbStyle(thumb)
		} else {
			replacement = box.ScrollView.trackStyle("│")
		}
		screen[row] = replaceScrollbarCell(base, geometry.Column, totalWidth, replacement,
			box.ScrollView.Scrollbar() != ScrollbarAlways)
	}
}

func paintBox(box *LayoutBox, screen []string, totalWidth int) {
	if box.HasLines {
		offset := box.LineOffset
		firstRow := maxInt(box.Rect.Y, maxInt(box.Clip.Y, 0))
		lastRow := minInt(box.Rect.Y+box.Rect.Height, minInt(box.Clip.Y+box.Clip.Height, len(screen)))
		for row := firstRow; row < lastRow; row++ {
			sourceIndex := offset + row - box.Rect.Y
			if sourceIndex < 0 || sourceIndex >= len(box.Lines) {
				continue
			}
			line := stripOSC133ZonePrefix(box.Lines[sourceIndex])
			if metadata, hasMetadata := GetKittyImageMetadata(line); hasMetadata {
				clipBottom := minInt(len(screen), box.Clip.Y+box.Clip.Height)
				visibleRows := minInt(metadata.Rows, clipBottom-row)
				if visibleRows < metadata.Rows {
					line = CropKittyImageLine(line, 0, visibleRows)
				}
			}
			// Fast path: a full-width box painting onto an untouched row can use
			// the source line directly.
			if box.Rect.X == 0 && box.Rect.Width >= totalWidth && (IsImageLine(line) || screen[row] == "") {
				screen[row] = line
			} else {
				screen[row] = CompositeTuiLine(screen[row], line, box.Rect.X, box.Rect.Width, totalWidth)
			}
		}
	}
	for _, child := range box.Children {
		paintBox(child, screen, totalWidth)
	}

	if box.ScrollView != nil && box.HasScrollContent && box.ScrollView.ScrollTop() > 0 && box.Rect.Height > 0 {
		for imageRow := box.ScrollView.ScrollTop() - 1; imageRow >= 0; imageRow-- {
			if imageRow >= len(box.ScrollContentLines) {
				break
			}
			imageLine := box.ScrollContentLines[imageRow]
			if metadata, hasMetadata := GetKittyImageMetadata(imageLine); hasMetadata {
				hiddenRows := box.ScrollView.ScrollTop() - imageRow
				if hiddenRows < metadata.Rows {
					visibleRows := minInt(box.Rect.Height, metadata.Rows-hiddenRows)
					cropped := CropKittyImageLine(imageLine, hiddenRows, visibleRows)
					if box.Rect.X == 0 && box.Rect.Width >= totalWidth {
						if box.Rect.Y >= 0 && box.Rect.Y < len(screen) {
							screen[box.Rect.Y] = cropped
						}
					}
				}
				break
			}
			if imageLine != "" {
				break
			}
		}
	}

	paintScrollbar(box, screen, totalWidth)
}

// RenderLayoutFrame lays out a component tree and paints it into a screen
// buffer.
func RenderLayoutFrame(root Component, width int, height int, requestRender func()) LayoutFrame {
	safeWidth := maxInt(1, width)
	safeHeight := maxInt(1, height)
	context := &layoutContext{
		viewport:      LayoutViewport{Width: safeWidth, Height: safeHeight},
		renderCache:   map[Component]map[int][]string{},
		requestRender: requestRender,
	}
	rootHeight := safeHeight
	rootBox := layoutComponent(context, root, 0, 0, safeWidth, &rootHeight, LayoutRect{
		X: 0, Y: 0, Width: safeWidth, Height: safeHeight,
	})
	lines := make([]string, safeHeight)
	paintBox(rootBox, lines, safeWidth)
	return LayoutFrame{
		Root:              rootBox,
		Width:             safeWidth,
		Height:            safeHeight,
		Lines:             lines,
		PrimaryScrollView: context.primaryScrollView,
	}
}

func containsPoint(rect LayoutRect, x int, y int) bool {
	return x >= rect.X && x < rect.X+rect.Width && y >= rect.Y && y < rect.Y+rect.Height
}

// GetLayoutBoxesAt returns the visual hit path from the deepest component to
// the layout root.
func GetLayoutBoxesAt(frame LayoutFrame, x int, y int) []*LayoutBox {
	type entry struct {
		box   *LayoutBox
		depth int
	}
	var result []entry
	var visit func(box *LayoutBox, depth int)
	visit = func(box *LayoutBox, depth int) {
		if !containsPoint(box.Clip, x, y) {
			return
		}
		result = append(result, entry{box, depth})
		for _, child := range box.Children {
			visit(child, depth+1)
		}
	}
	visit(frame.Root, 0)
	sortBoxes(result, func(a, b entry) bool {
		if a.box.Layer != b.box.Layer {
			return a.box.Layer > b.box.Layer
		}
		return a.depth > b.depth
	})
	boxes := make([]*LayoutBox, 0, len(result))
	for _, item := range result {
		boxes = append(boxes, item.box)
	}
	return boxes
}

// GetScrollViewBox returns the layout box for a scroll view.
func GetScrollViewBox(frame LayoutFrame, scrollView *ScrollView) (*LayoutBox, bool) {
	var visit func(box *LayoutBox) *LayoutBox
	visit = func(box *LayoutBox) *LayoutBox {
		if box.ScrollView == scrollView {
			return box
		}
		for _, child := range box.Children {
			if match := visit(child); match != nil {
				return match
			}
		}
		return nil
	}
	match := visit(frame.Root)
	return match, match != nil
}

// GetScrollViewsAt returns the scroll views under a point, deepest first.
func GetScrollViewsAt(frame LayoutFrame, x int, y int) []*ScrollView {
	type entry struct {
		scrollView *ScrollView
		depth      int
	}
	var result []entry
	var visit func(box *LayoutBox, depth int)
	visit = func(box *LayoutBox, depth int) {
		if !containsPoint(box.Clip, x, y) {
			return
		}
		if box.ScrollView != nil && containsPoint(box.Rect, x, y) {
			result = append(result, entry{box.ScrollView, depth})
		}
		for _, child := range box.Children {
			visit(child, depth+1)
		}
	}
	visit(frame.Root, 0)
	sortBoxes(result, func(a, b entry) bool { return a.depth > b.depth })
	views := make([]*ScrollView, 0, len(result))
	for _, item := range result {
		views = append(views, item.scrollView)
	}
	return views
}

// sortBoxes is an insertion sort (the hit paths are tiny).
func sortBoxes[T any](items []T, less func(a, b T) bool) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && less(items[j], items[j-1]); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

var spaces = func() string {
	buffer := make([]byte, 4096)
	for i := range buffer {
		buffer[i] = ' '
	}
	return string(buffer)
}()
