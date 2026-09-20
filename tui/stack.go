package tui

import "math"

// Port of src/components/stack.ts (+ v-stack.ts/h-stack.ts): the flex-style
// stack sizing algorithm and the vertical/horizontal stack components.

// StackEntryOptions configure a stack child's sizing.
type StackEntryOptions struct {
	Basis   *Basis
	Grow    *int
	Shrink  *int
	MinSize *int
	MaxSize *int
	Visible func(viewport LayoutViewport) bool
}

// StackEntry is a stack child with its resolved sizing. Upstream declares
// StackEntry and StackLayoutEntry separately but they are structurally
// identical, so Go aliases them (divergence D56).
type StackEntry = StackLayoutEntry

// StackOptions configure a stack.
type StackOptions struct {
	Gap   *int
	Align string // "stretch" | "start" | "center" | "end"
}

func normalizeSize(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	if *value < 0 {
		return 0
	}
	return *value
}

// NewStackEntry resolves the options into a StackEntry.
func NewStackEntry(component Component, options StackEntryOptions) StackLayoutEntry {
	return StackEntry{
		Component: component,
		Basis:     options.Basis,
		Grow:      normalizeSize(options.Grow, 0),
		Shrink:    normalizeSize(options.Shrink, 1),
		MinSize:   normalizeSize(options.MinSize, 0),
		MaxSize:   normalizeSize(options.MaxSize, math.MaxInt),
		Visible:   options.Visible,
	}
}

// Stack is the base flex stack container.
type Stack struct {
	*Container

	Entries []StackLayoutEntry
	Gap     int
	Align   string

	layoutType string
}

// NewStack creates a stack with the given children and options.
func NewStack(layoutType string, children []Component, options StackOptions) *Stack {
	stack := &Stack{
		Container:  &Container{},
		Gap:        normalizeSize(options.Gap, 0),
		Align:      options.Align,
		layoutType: layoutType,
	}
	if stack.Align == "" {
		stack.Align = "stretch"
	}
	for _, child := range children {
		stack.AddChild(child)
	}
	return stack
}

// AddChild appends a child with sizing options (upstream's overridden
// addChild).
func (s *Stack) AddChild(component Component, options ...StackEntryOptions) {
	s.Container.AddChild(component)
	entryOptions := StackEntryOptions{}
	if len(options) > 0 {
		entryOptions = options[0]
	}
	s.Entries = append(s.Entries, NewStackEntry(component, entryOptions))
}

// AddStackEntry appends a child with sizing options (an explicit variant of
// the variadic AddChild).
func (s *Stack) AddStackEntry(component Component, options StackEntryOptions) {
	s.AddChild(component, options)
}

// RemoveChild removes a child.
func (s *Stack) RemoveChild(component Component) {
	s.Container.RemoveChild(component)
	for i, entry := range s.Entries {
		if entry.Component == component {
			s.Entries = append(s.Entries[:i], s.Entries[i+1:]...)
			return
		}
	}
}

// Clear removes all children.
func (s *Stack) Clear() {
	s.Container.Clear()
	s.Entries = nil
}

// LayoutNode exposes the stack to the layout engine.
func (s *Stack) LayoutNode() LayoutNode {
	node := &StackLayoutNode{Type: s.layoutType, Entries: s.Entries, Gap: s.Gap, Align: s.Align}
	return LayoutNode{Kind: s.layoutType, Stack: node}
}

// VisibleStackEntries filters out entries whose visible callback rejects the
// viewport.
func VisibleStackEntries(entries []StackLayoutEntry, viewport LayoutViewport) []StackLayoutEntry {
	result := make([]StackLayoutEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Visible != nil && !entry.Visible(viewport) {
			continue
		}
		result = append(result, entry)
	}
	return result
}

// visibleStackEntryList filters the Stack's entries.
func visibleStackEntryList(entries []StackLayoutEntry, viewport LayoutViewport) []StackLayoutEntry {
	result := make([]StackEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Visible != nil && !entry.Visible(viewport) {
			continue
		}
		result = append(result, entry)
	}
	return result
}

func clampStackSize(size int, entry StackLayoutEntry) int {
	minSize := entry.MinSize
	if minSize < 0 {
		minSize = 0
	}
	maxSize := entry.MaxSize
	if maxSize < minSize {
		maxSize = minSize
	}
	value := size
	if value < 0 {
		value = 0
	}
	if value < minSize {
		value = minSize
	}
	if value > maxSize {
		value = maxSize
	}
	return value
}

func distributeStackSizes(sizes []int, entries []StackLayoutEntry, amount int, mode string) {
	remaining := amount
	for remaining > 0 {
		type candidate struct {
			entry StackLayoutEntry
			index int
		}
		var candidates []candidate
		for index, entry := range entries {
			if mode == "grow" {
				if entry.Grow > 0 && sizes[index] < entry.MaxSize {
					candidates = append(candidates, candidate{entry, index})
				}
			} else if entry.Shrink > 0 && sizes[index] > entry.MinSize {
				candidates = append(candidates, candidate{entry, index})
			}
		}
		if len(candidates) == 0 {
			return
		}

		totalWeight := 0
		for _, item := range candidates {
			if mode == "grow" {
				totalWeight += item.entry.Grow
			} else {
				weight := item.entry.Shrink * maxInt(1, sizes[item.index])
				totalWeight += weight
			}
		}
		distributed := 0
		for _, item := range candidates {
			if remaining <= 0 {
				break
			}
			weight := 0
			if mode == "grow" {
				weight = item.entry.Grow
			} else {
				weight = item.entry.Shrink * maxInt(1, sizes[item.index])
			}
			proposed := maxInt(1, remaining*weight/totalWeight)
			capacity := 0
			if mode == "grow" {
				capacity = item.entry.MaxSize - sizes[item.index]
			} else {
				capacity = sizes[item.index] - item.entry.MinSize
			}
			delta := minInt(remaining, proposed, capacity)
			if delta <= 0 {
				continue
			}
			if mode == "grow" {
				sizes[item.index] += delta
			} else {
				sizes[item.index] -= delta
			}
			remaining -= delta
			distributed += delta
		}
		if distributed == 0 {
			return
		}
	}
}

// AllocateStackSizes sizes the entries given their intrinsic sizes and the
// available space.
func AllocateStackSizes(entries []StackLayoutEntry, intrinsicSizes []int, availableSize *int, gap int) []int {
	sizes := make([]int, len(entries))
	for index, entry := range entries {
		intrinsic := 0
		if index < len(intrinsicSizes) {
			intrinsic = intrinsicSizes[index]
		}
		if entry.Basis != nil && entry.Basis.Mode == "fixed" {
			intrinsic = entry.Basis.Value
		}
		sizes[index] = clampStackSize(intrinsic, entry)
	}
	if availableSize == nil {
		return sizes
	}

	contentSize := maxInt(0, *availableSize-maxInt(0, len(entries)-1)*gap)
	total := 0
	for _, size := range sizes {
		total += size
	}
	if total < contentSize {
		distributeStackSizes(sizes, entries, contentSize-total, "grow")
	} else if total > contentSize {
		distributeStackSizes(sizes, entries, total-contentSize, "shrink")
	}
	return sizes
}

// VStack is a vertical stack (src/components/v-stack.ts).
type VStack struct {
	*Stack
}

// NewVStack creates a vertical stack.
func NewVStack(children []Component, options StackOptions) *VStack {
	return &VStack{NewStack("vstack", children, options)}
}

// Render renders the children with the allocated heights and gaps.
func (v *VStack) Render(width int) []string {
	viewport := LayoutViewport{Width: maxInt(1, width), Height: math.MaxInt}
	entries := visibleStackEntryList(v.Entries, viewport)
	rendered := make([][]string, len(entries))
	for index, entry := range entries {
		rendered[index] = entry.Component.Render(viewport.Width)
	}
	intrinsic := make([]int, len(rendered))
	for index, lines := range rendered {
		intrinsic[index] = len(lines)
	}
	sizes := AllocateStackSizes(entries, intrinsic, nil, v.Gap)

	var lines []string
	for index := range entries {
		if index > 0 {
			for gap := 0; gap < v.Gap; gap++ {
				lines = append(lines, "")
			}
		}
		childLines := rendered[index]
		if sizes[index] < len(childLines) {
			childLines = childLines[:sizes[index]]
		}
		lines = append(lines, childLines...)
		for padding := len(childLines); padding < sizes[index]; padding++ {
			lines = append(lines, "")
		}
	}
	return lines
}

// HStack is a horizontal stack (src/components/h-stack.ts).
type HStack struct {
	*Stack
}

// NewHStack creates a horizontal stack.
func NewHStack(children []Component, options StackOptions) *HStack {
	return &HStack{NewStack("hstack", children, options)}
}

// Render composites the children side by side.
func (h *HStack) Render(width int) []string {
	safeWidth := maxInt(1, width)
	viewport := LayoutViewport{Width: safeWidth, Height: math.MaxInt}
	entries := visibleStackEntryList(h.Entries, viewport)
	if len(entries) == 0 {
		return nil
	}

	intrinsicWidths := make([]int, len(entries))
	for index, entry := range entries {
		lines := entry.Component.Render(safeWidth)
		maxWidth := 0
		for _, line := range lines {
			if visible := VisibleWidth(line); visible > maxWidth {
				maxWidth = visible
			}
		}
		intrinsicWidths[index] = maxWidth
	}
	widths := AllocateStackSizes(entries, intrinsicWidths, &safeWidth, h.Gap)

	rendered := make([][]string, len(entries))
	for index, entry := range entries {
		if widths[index] == 0 {
			rendered[index] = nil
			continue
		}
		rendered[index] = entry.Component.Render(widths[index])
	}
	height := 0
	for _, lines := range rendered {
		if len(lines) > height {
			height = len(lines)
		}
	}
	result := make([]string, height)
	x := 0
	for index, lines := range rendered {
		childWidth := widths[index]
		offset := 0
		if h.Align == "center" {
			offset = (height - len(lines)) / 2
		} else if h.Align == "end" {
			offset = height - len(lines)
		}
		for row, line := range lines {
			target := row + offset
			if target < 0 || target >= len(result) {
				continue
			}
			result[target] = CompositeTuiLine(result[target], line, x, childWidth, safeWidth)
		}
		x += childWidth + h.Gap
	}
	return result
}
