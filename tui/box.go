package tui

import "strings"

// Port of src/components/box.ts: a container that applies padding and an
// optional background to its children.

type boxRenderCache struct {
	childLines  []string
	width       int
	bgSample    string
	hasBgSample bool
	lines       []string
}

// Box applies padding and a background to all children.
type Box struct {
	Children []Component

	paddingX int
	paddingY int
	bgFn     func(text string) string

	cache            *boxRenderCache
	mouseLayout      []mouseChild
	mouseLayoutWidth int
}

// NewBox creates a box with the given padding and optional background.
func NewBox(paddingX int, paddingY int, bgFn func(text string) string) *Box {
	return &Box{paddingX: paddingX, paddingY: paddingY, bgFn: bgFn}
}

// AddChild appends a child.
func (b *Box) AddChild(component Component) {
	b.Children = append(b.Children, component)
	b.cache = nil
}

// RemoveChild removes a child.
func (b *Box) RemoveChild(component Component) {
	for i, child := range b.Children {
		if child == component {
			b.Children = append(b.Children[:i], b.Children[i+1:]...)
			b.cache = nil
			return
		}
	}
}

// Clear removes all children.
func (b *Box) Clear() {
	b.Children = nil
	b.cache = nil
}

// SetBgFn updates the background function. The cache is kept because the
// background change is detected by sampling the function output.
func (b *Box) SetBgFn(bgFn func(text string) string) { b.bgFn = bgFn }

// childComponents implements childrenHolder.
func (b *Box) childComponents() []Component { return b.Children }

// Invalidate drops the cache and invalidates the children.
func (b *Box) Invalidate() {
	b.cache = nil
	for _, child := range b.Children {
		child.Invalidate()
	}
}

func (b *Box) matchCache(width int, childLines []string, bgSample string, hasBgSample bool) bool {
	cache := b.cache
	if cache == nil {
		return false
	}
	if cache.width != width || cache.hasBgSample != hasBgSample || cache.bgSample != bgSample {
		return false
	}
	if len(cache.childLines) != len(childLines) {
		return false
	}
	for i, line := range cache.childLines {
		if line != childLines[i] {
			return false
		}
	}
	return true
}

// HandleMouse forwards an event to the child under the pointer.
func (b *Box) HandleMouse(event TuiMouseEvent) *TuiMouseDispatchResult {
	contentWidth := maxInt(1, event.Width-b.paddingX*2)
	contentY := event.Y - b.paddingY
	contentX := event.X - b.paddingX
	if contentY < 0 || contentX < 0 || contentX >= contentWidth {
		return nil
	}

	mouseChildren := b.mouseLayout
	if b.mouseLayoutWidth != contentWidth {
		mouseChildren = make([]mouseChild, 0, len(b.Children))
		for _, child := range b.Children {
			mouseChildren = append(mouseChildren, mouseChild{component: child, height: len(child.Render(contentWidth))})
		}
	}
	childY := 0
	for _, child := range mouseChildren {
		if contentY >= childY && contentY < childY+child.height {
			childEvent := event
			childEvent.X = contentX
			childEvent.Y = contentY - childY
			childEvent.Width = contentWidth
			childEvent.Height = child.height
			return DispatchMouseEvent(child.component, childEvent)
		}
		childY += child.height
	}
	return nil
}

// Render renders the children with padding and background.
func (b *Box) Render(width int) []string {
	if len(b.Children) == 0 {
		return nil
	}

	contentWidth := maxInt(1, width-b.paddingX*2)
	leftPad := strings.Repeat(" ", maxInt(0, b.paddingX))

	var childLines []string
	mouseChildren := make([]mouseChild, 0, len(b.Children))
	for _, child := range b.Children {
		lines := child.Render(contentWidth)
		mouseChildren = append(mouseChildren, mouseChild{component: child, height: len(lines)})
		for _, line := range lines {
			childLines = append(childLines, leftPad+line)
		}
	}
	b.mouseLayout = mouseChildren
	b.mouseLayoutWidth = contentWidth

	if len(childLines) == 0 {
		return nil
	}

	bgSample := ""
	hasBgSample := false
	if b.bgFn != nil {
		bgSample = b.bgFn("test")
		hasBgSample = true
	}

	if b.matchCache(width, childLines, bgSample, hasBgSample) {
		return b.cache.lines
	}

	var result []string
	for i := 0; i < b.paddingY; i++ {
		result = append(result, b.applyBg("", width))
	}
	for _, line := range childLines {
		result = append(result, b.applyBg(line, width))
	}
	for i := 0; i < b.paddingY; i++ {
		result = append(result, b.applyBg("", width))
	}

	b.cache = &boxRenderCache{childLines: childLines, width: width, bgSample: bgSample, hasBgSample: hasBgSample, lines: result}
	return result
}

func (b *Box) applyBg(line string, width int) string {
	padNeeded := maxInt(0, width-VisibleWidth(line))
	padded := line + strings.Repeat(" ", padNeeded)
	if b.bgFn != nil {
		return ApplyBackgroundToLine(padded, width, b.bgFn)
	}
	return padded
}

var _ Component = (*Box)(nil)
var _ MouseHandler = (*Box)(nil)
