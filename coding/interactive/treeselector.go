package interactive

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of src/modes/interactive/components/tree-selector.ts: the session tree
// navigator with folding, filters, labels and the horizontal viewport.

// FilterMode filters the tree display.
type FilterMode = string

// Filter modes.
const (
	FilterDefault     FilterMode = "default"
	FilterNoTools     FilterMode = "no-tools"
	FilterUserOnly    FilterMode = "user-only"
	FilterLabeledOnly FilterMode = "labeled-only"
	FilterAll         FilterMode = "all"
)

// TreeFilterModes is the filter cycle order.
var TreeFilterModes = []FilterMode{FilterDefault, FilterNoTools, FilterUserOnly, FilterLabeledOnly, FilterAll}

// gutterInfo marks an ancestor branch position.
type gutterInfo struct {
	Position int
	Show     bool
}

// flatNode is a flattened tree node with its visual structure.
type flatNode struct {
	Node               *coding.SessionTreeNode
	Indent             int
	ShowConnector      bool
	IsLast             bool
	Gutters            []gutterInfo
	IsVirtualRootChild bool
}

type horizontalViewportRow struct {
	Gutter     string
	Body       string
	AnchorCol  int
	BodyWidth  int
	IsSelected bool
}

const (
	treeGutterWidth              = 2
	minVisibleAnchorContentWidth = 4
	maxVisibleAnchorContentWidth = 20
	minAnchorContextWidth        = 2
	maxAnchorContextWidth        = 12
)

// renderHorizontalViewport clips the tree rows horizontally, keeping the
// gutter visible and panning only when the selected row's anchor needs it.
func renderHorizontalViewport(rows []horizontalViewportRow, width int) []string {
	viewportWidth := max(0, width-treeGutterWidth)
	maxBodyWidth := 0
	for _, row := range rows {
		if row.BodyWidth > maxBodyWidth {
			maxBodyWidth = row.BodyWidth
		}
	}
	maxHorizontalScroll := max(0, maxBodyWidth-viewportWidth)

	var selectedRow *horizontalViewportRow
	for index := range rows {
		if rows[index].IsSelected {
			selectedRow = &rows[index]
			break
		}
	}

	horizontalScroll := 0
	if selectedRow != nil && maxHorizontalScroll > 0 {
		minVisible := min(maxVisibleAnchorContentWidth,
			max(minVisibleAnchorContentWidth, viewportWidth/3))
		if selectedRow.AnchorCol > viewportWidth-minVisible {
			anchorContext := min(maxAnchorContextWidth,
				max(minAnchorContextWidth, viewportWidth/4))
			horizontalScroll = min(maxHorizontalScroll, selectedRow.AnchorCol-anchorContext)
		}
	}

	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		var line string
		if horizontalScroll > 0 {
			line = row.Gutter + tui.SliceByColumn(row.Body, horizontalScroll, viewportWidth, true) + "\x1b[0m"
		} else {
			line = row.Gutter + row.Body
		}
		lines = append(lines, tui.TruncateToWidth(line, width, "", false))
	}
	return lines
}

// treeMessage is the decoded view of a session entry's message.
type treeMessage struct {
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content"`
	StopReason   string          `json:"stopReason"`
	ErrorMessage string          `json:"errorMessage"`
	ToolCallID   string          `json:"toolCallId"`
	ToolName     string          `json:"toolName"`
	Command      string          `json:"command"`
}

// toolCallInfo is a tool call extracted from an assistant message.
type toolCallInfo struct {
	Name      string
	Arguments map[string]any
}

// TreeList is the tree list component.
type TreeList struct {
	flatNodes           []*flatNode
	filteredNodes       []*flatNode
	selectedIndex       int
	currentLeafID       *string
	maxVisibleLines     int
	filterMode          FilterMode
	searchQuery         string
	toolCallMap         map[string]toolCallInfo
	multipleRoots       bool
	showLabelTimestamps bool
	activePathIDs       map[string]bool
	visibleParentMap    map[string]*string
	visibleChildrenMap  map[string][]string
	lastSelectedID      *string
	foldedNodes         map[string]bool

	OnSelect    func(entryID string)
	OnCancel    func()
	OnCopy      func(text string, hasText bool)
	OnLabelEdit func(entryID string, currentLabel string, hasLabel bool)

	now func() time.Time
}

// NewTreeList creates the tree list.
func NewTreeList(tree []*coding.SessionTreeNode, currentLeafID *string, maxVisibleLines int, initialSelectedID *string, initialFilterMode FilterMode) *TreeList {
	list := &TreeList{
		currentLeafID:      currentLeafID,
		maxVisibleLines:    maxVisibleLines,
		filterMode:         FilterDefault,
		toolCallMap:        map[string]toolCallInfo{},
		activePathIDs:      map[string]bool{},
		visibleParentMap:   map[string]*string{},
		visibleChildrenMap: map[string][]string{},
		foldedNodes:        map[string]bool{},
		now:                time.Now,
	}
	if initialFilterMode != "" {
		list.filterMode = initialFilterMode
	}
	list.multipleRoots = len(tree) > 1
	list.flatNodes = list.flattenTree(tree)
	list.buildActivePath()
	list.ApplyFilter()

	targetID := initialSelectedID
	if targetID == nil {
		targetID = currentLeafID
	}
	list.selectedIndex = list.findNearestVisibleIndex(targetID)
	if node := list.selectedNode(); node != nil {
		id := node.Node.Entry.ID
		list.lastSelectedID = &id
	}
	return list
}

// SetNow overrides the clock (test seam for label timestamps).
func (t *TreeList) SetNow(now func() time.Time) { t.now = now }

func (t *TreeList) selectedNode() *flatNode {
	if t.selectedIndex < 0 || t.selectedIndex >= len(t.filteredNodes) {
		return nil
	}
	return t.filteredNodes[t.selectedIndex]
}

func (t *TreeList) findNearestVisibleIndex(entryID *string) int {
	if len(t.filteredNodes) == 0 {
		return 0
	}
	entryMap := map[string]*flatNode{}
	for _, node := range t.flatNodes {
		entryMap[node.Node.Entry.ID] = node
	}
	visibleIndex := map[string]int{}
	for index, node := range t.filteredNodes {
		visibleIndex[node.Node.Entry.ID] = index
	}

	currentID := entryID
	for currentID != nil {
		if index, ok := visibleIndex[*currentID]; ok {
			return index
		}
		node, ok := entryMap[*currentID]
		if !ok {
			break
		}
		currentID = node.Node.Entry.ParentID
	}
	return len(t.filteredNodes) - 1
}

func (t *TreeList) buildActivePath() {
	t.activePathIDs = map[string]bool{}
	if t.currentLeafID == nil {
		return
	}
	entryMap := map[string]*flatNode{}
	for _, node := range t.flatNodes {
		entryMap[node.Node.Entry.ID] = node
	}
	currentID := t.currentLeafID
	for currentID != nil {
		t.activePathIDs[*currentID] = true
		node, ok := entryMap[*currentID]
		if !ok {
			break
		}
		currentID = node.Node.Entry.ParentID
	}
}

type treeStackItem struct {
	node               *coding.SessionTreeNode
	indent             int
	justBranched       bool
	showConnector      bool
	isLast             bool
	gutters            []gutterInfo
	isVirtualRootChild bool
}

func (t *TreeList) flattenTree(roots []*coding.SessionTreeNode) []*flatNode {
	var result []*flatNode
	t.toolCallMap = map[string]toolCallInfo{}

	// Post-order computation of which subtrees contain the active leaf.
	containsActive := map[*coding.SessionTreeNode]bool{}
	{
		var allNodes []*coding.SessionTreeNode
		preOrder := append([]*coding.SessionTreeNode{}, roots...)
		for len(preOrder) > 0 {
			node := preOrder[len(preOrder)-1]
			preOrder = preOrder[:len(preOrder)-1]
			allNodes = append(allNodes, node)
			for index := len(node.Children) - 1; index >= 0; index-- {
				preOrder = append(preOrder, node.Children[index])
			}
		}
		for index := len(allNodes) - 1; index >= 0; index-- {
			node := allNodes[index]
			has := t.currentLeafID != nil && node.Entry.ID == *t.currentLeafID
			for _, child := range node.Children {
				if containsActive[child] {
					has = true
				}
			}
			containsActive[node] = has
		}
	}

	multipleRoots := len(roots) > 1
	orderedRoots := append([]*coding.SessionTreeNode{}, roots...)
	sort.SliceStable(orderedRoots, func(i int, j int) bool {
		return !containsActive[orderedRoots[i]] && containsActive[orderedRoots[j]]
	})

	var stack []treeStackItem
	for index := len(orderedRoots) - 1; index >= 0; index-- {
		isLast := index == len(orderedRoots)-1
		indent := 0
		if multipleRoots {
			indent = 1
		}
		stack = append(stack, treeStackItem{
			node: orderedRoots[index], indent: indent, justBranched: multipleRoots,
			showConnector: multipleRoots, isLast: isLast, isVirtualRootChild: multipleRoots,
		})
	}

	for len(stack) > 0 {
		item := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		entry := item.node.Entry
		if entry.Type == "message" {
			message := decodeTreeMessage(entry.Message)
			if message.Role == "assistant" {
				for _, call := range toolCallsFromContent(message.Content) {
					t.toolCallMap[call.ID] = toolCallInfo{Name: call.Name, Arguments: call.Arguments}
				}
			}
		}

		result = append(result, &flatNode{
			Node: item.node, Indent: item.indent, ShowConnector: item.showConnector,
			IsLast: item.isLast, Gutters: item.gutters, IsVirtualRootChild: item.isVirtualRootChild,
		})

		children := item.node.Children
		multipleChildren := len(children) > 1

		var prioritized, rest []*coding.SessionTreeNode
		for _, child := range children {
			if containsActive[child] {
				prioritized = append(prioritized, child)
			} else {
				rest = append(rest, child)
			}
		}
		orderedChildren := append(prioritized, rest...)

		childIndent := item.indent
		if multipleChildren {
			childIndent = item.indent + 1
		} else if item.justBranched && item.indent > 0 {
			childIndent = item.indent + 1
		}

		connectorDisplayed := item.showConnector && !item.isVirtualRootChild
		currentDisplayIndent := item.indent
		if t.multipleRoots {
			currentDisplayIndent = max(0, item.indent-1)
		}
		connectorPosition := max(0, currentDisplayIndent-1)
		childGutters := item.gutters
		if connectorDisplayed {
			childGutters = append(append([]gutterInfo{}, item.gutters...),
				gutterInfo{Position: connectorPosition, Show: !item.isLast})
		}

		for index := len(orderedChildren) - 1; index >= 0; index-- {
			childIsLast := index == len(orderedChildren)-1
			stack = append(stack, treeStackItem{
				node: orderedChildren[index], indent: childIndent, justBranched: multipleChildren,
				showConnector: multipleChildren, isLast: childIsLast, gutters: childGutters,
			})
		}
	}
	return result
}

// ApplyFilter applies the filter mode, search query and folded nodes.
func (t *TreeList) ApplyFilter() {
	if len(t.filteredNodes) > 0 {
		if node := t.selectedNode(); node != nil {
			id := node.Node.Entry.ID
			t.lastSelectedID = &id
		}
	}

	searchTokens := strings.Fields(strings.ToLower(t.searchQuery))

	t.filteredNodes = nil
	for _, flatNode := range t.flatNodes {
		entry := flatNode.Node.Entry
		if entry.Type == "usage" {
			continue
		}
		isCurrentLeaf := t.currentLeafID != nil && entry.ID == *t.currentLeafID

		if entry.Type == "message" && !isCurrentLeaf {
			message := decodeTreeMessage(entry.Message)
			if message.Role == "assistant" {
				hasText := hasTextContent(message.Content)
				isErrorOrAborted := message.StopReason != "" && message.StopReason != "stop" && message.StopReason != "toolUse"
				if !hasText && !isErrorOrAborted {
					continue
				}
			}
		}

		isSettingsEntry := entry.Type == "label" || entry.Type == "custom" ||
			entry.Type == "model_change" || entry.Type == "thinking_level_change" || entry.Type == "session_info"

		passesFilter := true
		switch t.filterMode {
		case FilterUserOnly:
			passesFilter = entry.Type == "message" && decodeTreeMessage(entry.Message).Role == "user"
		case FilterNoTools:
			passesFilter = !isSettingsEntry && !(entry.Type == "message" && decodeTreeMessage(entry.Message).Role == "toolResult")
		case FilterLabeledOnly:
			passesFilter = flatNode.Node.HasLabel
		case FilterAll:
			passesFilter = true
		default:
			passesFilter = !isSettingsEntry
		}
		if !passesFilter {
			continue
		}

		if len(searchTokens) > 0 {
			nodeText := strings.ToLower(t.getSearchableText(flatNode.Node))
			allMatch := true
			for _, token := range searchTokens {
				if !strings.Contains(nodeText, token) {
					allMatch = false
					break
				}
			}
			if !allMatch {
				continue
			}
		}
		t.filteredNodes = append(t.filteredNodes, flatNode)
	}

	if len(t.foldedNodes) > 0 {
		skip := map[string]bool{}
		for _, flatNode := range t.flatNodes {
			entry := flatNode.Node.Entry
			if entry.ParentID != nil && (t.foldedNodes[*entry.ParentID] || skip[*entry.ParentID]) {
				skip[entry.ID] = true
			}
		}
		kept := t.filteredNodes[:0]
		for _, flatNode := range t.filteredNodes {
			if !skip[flatNode.Node.Entry.ID] {
				kept = append(kept, flatNode)
			}
		}
		t.filteredNodes = kept
	}

	t.recalculateVisualStructure()

	if t.lastSelectedID != nil {
		t.selectedIndex = t.findNearestVisibleIndex(t.lastSelectedID)
	} else if t.selectedIndex >= len(t.filteredNodes) {
		t.selectedIndex = max(0, len(t.filteredNodes)-1)
	}

	if len(t.filteredNodes) > 0 {
		if node := t.selectedNode(); node != nil {
			id := node.Node.Entry.ID
			t.lastSelectedID = &id
		}
	}
}

func (t *TreeList) recalculateVisualStructure() {
	if len(t.filteredNodes) == 0 {
		return
	}
	visibleIDs := map[string]bool{}
	for _, node := range t.filteredNodes {
		visibleIDs[node.Node.Entry.ID] = true
	}
	entryMap := map[string]*flatNode{}
	for _, node := range t.flatNodes {
		entryMap[node.Node.Entry.ID] = node
	}

	findVisibleAncestor := func(nodeID string) *string {
		var currentID *string
		if node, ok := entryMap[nodeID]; ok {
			currentID = node.Node.Entry.ParentID
		}
		for currentID != nil {
			if visibleIDs[*currentID] {
				return currentID
			}
			node, ok := entryMap[*currentID]
			if !ok {
				return nil
			}
			currentID = node.Node.Entry.ParentID
		}
		return nil
	}

	visibleParent := map[string]*string{}
	visibleChildren := map[string][]string{}
	rootKey := ""
	visibleChildren[rootKey] = nil

	for _, flatNode := range t.filteredNodes {
		nodeID := flatNode.Node.Entry.ID
		ancestorID := findVisibleAncestor(nodeID)
		visibleParent[nodeID] = ancestorID
		key := rootKey
		if ancestorID != nil {
			key = *ancestorID
		}
		visibleChildren[key] = append(visibleChildren[key], nodeID)
	}

	visibleRootIDs := visibleChildren[rootKey]
	t.multipleRoots = len(visibleRootIDs) > 1

	filteredNodeMap := map[string]*flatNode{}
	for _, flatNode := range t.filteredNodes {
		filteredNodeMap[flatNode.Node.Entry.ID] = flatNode
	}

	type recalcItem struct {
		nodeID             string
		indent             int
		justBranched       bool
		showConnector      bool
		isLast             bool
		gutters            []gutterInfo
		isVirtualRootChild bool
	}
	var stack []recalcItem
	for index := len(visibleRootIDs) - 1; index >= 0; index-- {
		isLast := index == len(visibleRootIDs)-1
		indent := 0
		if t.multipleRoots {
			indent = 1
		}
		stack = append(stack, recalcItem{
			nodeID: visibleRootIDs[index], indent: indent, justBranched: t.multipleRoots,
			showConnector: t.multipleRoots, isLast: isLast, isVirtualRootChild: t.multipleRoots,
		})
	}

	for len(stack) > 0 {
		item := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		flatNode, ok := filteredNodeMap[item.nodeID]
		if !ok {
			continue
		}
		flatNode.Indent = item.indent
		flatNode.ShowConnector = item.showConnector
		flatNode.IsLast = item.isLast
		flatNode.Gutters = item.gutters
		flatNode.IsVirtualRootChild = item.isVirtualRootChild

		children := visibleChildren[item.nodeID]
		multipleChildren := len(children) > 1

		childIndent := item.indent
		if multipleChildren {
			childIndent = item.indent + 1
		} else if item.justBranched && item.indent > 0 {
			childIndent = item.indent + 1
		}

		connectorDisplayed := item.showConnector && !item.isVirtualRootChild
		currentDisplayIndent := item.indent
		if t.multipleRoots {
			currentDisplayIndent = max(0, item.indent-1)
		}
		connectorPosition := max(0, currentDisplayIndent-1)
		childGutters := item.gutters
		if connectorDisplayed {
			childGutters = append(append([]gutterInfo{}, item.gutters...),
				gutterInfo{Position: connectorPosition, Show: !item.isLast})
		}

		for index := len(children) - 1; index >= 0; index-- {
			childIsLast := index == len(children)-1
			stack = append(stack, recalcItem{
				nodeID: children[index], indent: childIndent, justBranched: multipleChildren,
				showConnector: multipleChildren, isLast: childIsLast, gutters: childGutters,
			})
		}
	}

	t.visibleParentMap = visibleParent
	t.visibleChildrenMap = visibleChildren
}

func (t *TreeList) getSearchableText(node *coding.SessionTreeNode) string {
	entry := node.Entry
	var parts []string
	if node.HasLabel {
		parts = append(parts, node.Label)
	}
	switch entry.Type {
	case "message":
		message := decodeTreeMessage(entry.Message)
		parts = append(parts, message.Role)
		if len(message.Content) > 0 {
			parts = append(parts, t.extractContent(message.Content))
		}
		if message.Role == "bashExecution" && message.Command != "" {
			parts = append(parts, message.Command)
		}
	case "custom_message":
		parts = append(parts, entry.CustomType)
		parts = append(parts, t.extractContent(entry.Content))
	case "compaction":
		parts = append(parts, "compaction")
	case "branch_summary":
		parts = append(parts, "branch summary", entry.Summary)
	case "session_info":
		parts = append(parts, "title")
		if entry.Name != nil {
			parts = append(parts, *entry.Name)
		}
	case "model_change":
		parts = append(parts, "model", entry.ModelID)
	case "thinking_level_change":
		parts = append(parts, "thinking", entry.ThinkingLevel)
	case "custom":
		parts = append(parts, "custom", entry.CustomType)
	case "label":
		label := ""
		if entry.Label != nil {
			label = *entry.Label
		}
		parts = append(parts, "label", label)
	}
	return strings.Join(parts, " ")
}

// Invalidate is a no-op.
func (t *TreeList) Invalidate() {}

// GetSearchQuery returns the current search query.
func (t *TreeList) GetSearchQuery() string { return t.searchQuery }

// GetSelectedNode returns the selected tree node.
func (t *TreeList) GetSelectedNode() *coding.SessionTreeNode {
	node := t.selectedNode()
	if node == nil {
		return nil
	}
	return node.Node
}

// CopySelected copies the selected entry's text.
func (t *TreeList) CopySelected() {
	node := t.GetSelectedNode()
	if node == nil {
		if t.OnCopy != nil {
			t.OnCopy("", false)
		}
		return
	}
	text, ok := t.getEntryCopyText(node)
	if t.OnCopy != nil {
		t.OnCopy(text, ok)
	}
}

// UpdateNodeLabel sets or clears a node's label.
func (t *TreeList) UpdateNodeLabel(entryID string, label *string, labelTimestamp string) {
	for _, flatNode := range t.flatNodes {
		if flatNode.Node.Entry.ID != entryID {
			continue
		}
		if label != nil {
			flatNode.Node.Label = *label
			flatNode.Node.HasLabel = true
			if labelTimestamp != "" {
				flatNode.Node.LabelTimestamp = labelTimestamp
			} else {
				flatNode.Node.LabelTimestamp = t.now().UTC().Format(time.RFC3339Nano)
			}
		} else {
			flatNode.Node.Label = ""
			flatNode.Node.HasLabel = false
			flatNode.Node.LabelTimestamp = ""
		}
		return
	}
}

func (t *TreeList) getStatusLabels() string {
	var labels string
	switch t.filterMode {
	case FilterNoTools:
		labels += " [no-tools]"
	case FilterUserOnly:
		labels += " [user]"
	case FilterLabeledOnly:
		labels += " [labeled]"
	case FilterAll:
		labels += " [all]"
	}
	if t.showLabelTimestamps {
		labels += " [+label time]"
	}
	return labels
}

// Render renders the tree list.
func (t *TreeList) Render(width int) []string {
	theme := ActiveTheme()
	var lines []string

	if len(t.filteredNodes) == 0 {
		lines = append(lines, tui.TruncateToWidth(theme.Fg("muted", "  No entries found"), width, "", false))
		lines = append(lines, tui.TruncateToWidth(theme.Fg("muted", "  (0/0)"+t.getStatusLabels()), width, "", false))
		return lines
	}

	startIndex := max(0, min(
		t.selectedIndex-t.maxVisibleLines/2,
		len(t.filteredNodes)-t.maxVisibleLines,
	))
	endIndex := min(startIndex+t.maxVisibleLines, len(t.filteredNodes))

	var rows []horizontalViewportRow
	for i := startIndex; i < endIndex; i++ {
		flatNode := t.filteredNodes[i]
		entry := flatNode.Node.Entry
		isSelected := i == t.selectedIndex

		cursor := "  "
		if isSelected {
			cursor = theme.Fg("accent", "› ")
		}

		displayIndent := flatNode.Indent
		if t.multipleRoots {
			displayIndent = max(0, flatNode.Indent-1)
		}

		connector := ""
		if flatNode.ShowConnector && !flatNode.IsVirtualRootChild {
			if flatNode.IsLast {
				connector = "└─ "
			} else {
				connector = "├─ "
			}
		}
		connectorPosition := -1
		if connector != "" {
			connectorPosition = displayIndent - 1
		}

		totalChars := displayIndent * 3
		prefixChars := make([]string, 0, totalChars)
		isFolded := t.foldedNodes[entry.ID]
		for index := 0; index < totalChars; index++ {
			level := index / 3
			posInLevel := index % 3

			var gutter *gutterInfo
			for gi := range flatNode.Gutters {
				if flatNode.Gutters[gi].Position == level {
					gutter = &flatNode.Gutters[gi]
					break
				}
			}
			switch {
			case gutter != nil:
				if posInLevel == 0 && gutter.Show {
					prefixChars = append(prefixChars, "│")
				} else {
					prefixChars = append(prefixChars, " ")
				}
			case connector != "" && level == connectorPosition:
				switch posInLevel {
				case 0:
					if flatNode.IsLast {
						prefixChars = append(prefixChars, "└")
					} else {
						prefixChars = append(prefixChars, "├")
					}
				case 1:
					foldable := t.isFoldable(entry.ID)
					switch {
					case isFolded:
						prefixChars = append(prefixChars, "⊞")
					case foldable:
						prefixChars = append(prefixChars, "⊟")
					default:
						prefixChars = append(prefixChars, "─")
					}
				default:
					prefixChars = append(prefixChars, " ")
				}
			default:
				prefixChars = append(prefixChars, " ")
			}
		}
		prefix := strings.Join(prefixChars, "")

		showsFoldInConnector := flatNode.ShowConnector && !flatNode.IsVirtualRootChild
		foldMarker := ""
		if isFolded && !showsFoldInConnector {
			foldMarker = theme.Fg("accent", "⊞ ")
		}

		pathMarker := ""
		if t.activePathIDs[entry.ID] {
			pathMarker = theme.Fg("accent", "• ")
		}

		label := ""
		if flatNode.Node.HasLabel {
			label = theme.Fg("warning", "["+flatNode.Node.Label+"] ")
		}
		labelTimestamp := ""
		if t.showLabelTimestamps && flatNode.Node.HasLabel && flatNode.Node.LabelTimestamp != "" {
			labelTimestamp = theme.Fg("muted", t.formatLabelTimestamp(flatNode.Node.LabelTimestamp)+" ")
		}
		content := t.getEntryDisplayText(flatNode.Node, isSelected)
		prefixPart := theme.Fg("dim", prefix) + foldMarker + pathMarker
		anchorCol := tui.VisibleWidth(prefixPart)
		gutter := cursor
		body := prefixPart + label + labelTimestamp + content
		if isSelected {
			gutter = theme.Bg("selectedBg", gutter)
			body = theme.Bg("selectedBg", body)
		}
		rows = append(rows, horizontalViewportRow{
			Gutter: gutter, Body: body, AnchorCol: anchorCol,
			BodyWidth: tui.VisibleWidth(body), IsSelected: isSelected,
		})
	}

	lines = append(lines, renderHorizontalViewport(rows, width)...)
	lines = append(lines, tui.TruncateToWidth(theme.Fg("muted",
		"  ("+itoa(t.selectedIndex+1)+"/"+itoa(len(t.filteredNodes))+")"+t.getStatusLabels()), width, "", false))
	return lines
}

func (t *TreeList) getEntryDisplayText(node *coding.SessionTreeNode, isSelected bool) string {
	theme := ActiveTheme()
	entry := node.Entry
	var result string

	normalize := func(value string) string {
		value = strings.ReplaceAll(value, "\n", " ")
		value = strings.ReplaceAll(value, "\t", " ")
		return strings.TrimSpace(value)
	}

	switch entry.Type {
	case "message":
		message := decodeTreeMessage(entry.Message)
		switch message.Role {
		case "user":
			result = theme.Fg("accent", "user: ") + normalize(t.extractContent(message.Content))
		case "assistant":
			textContent := normalize(t.extractContent(message.Content))
			switch {
			case textContent != "":
				result = theme.Fg("success", "assistant: ") + textContent
			case message.StopReason == "aborted":
				result = theme.Fg("success", "assistant: ") + theme.Fg("muted", "(aborted)")
			case message.ErrorMessage != "":
				errMsg := normalize(message.ErrorMessage)
				if len(errMsg) > 80 {
					errMsg = errMsg[:80]
				}
				result = theme.Fg("success", "assistant: ") + theme.Fg("error", errMsg)
			default:
				result = theme.Fg("success", "assistant: ") + theme.Fg("muted", "(no content)")
			}
		case "toolResult":
			call, ok := t.toolCallMap[message.ToolCallID]
			if ok {
				result = theme.Fg("muted", t.formatToolCall(call.Name, call.Arguments))
			} else {
				name := message.ToolName
				if name == "" {
					name = "tool"
				}
				result = theme.Fg("muted", "["+name+"]")
			}
		case "bashExecution":
			result = theme.Fg("dim", "[bash]: "+normalize(message.Command))
		default:
			result = theme.Fg("dim", "["+message.Role+"]")
		}
	case "custom_message":
		result = theme.Fg("customMessageLabel", "["+entry.CustomType+"]: ") + normalize(t.extractContent(entry.Content))
	case "compaction":
		tokens := (entry.TokensBefore + 500) / 1000
		result = theme.Fg("borderAccent", "[compaction: "+itoa(int(tokens))+"k tokens]")
	case "branch_summary":
		result = theme.Fg("warning", "[branch summary]: ") + normalize(entry.Summary)
	case "model_change":
		result = theme.Fg("dim", "[model: "+entry.ModelID+"]")
	case "thinking_level_change":
		result = theme.Fg("dim", "[thinking: "+entry.ThinkingLevel+"]")
	case "custom":
		result = theme.Fg("dim", "[custom: "+entry.CustomType+"]")
	case "label":
		label := "(cleared)"
		if entry.Label != nil {
			label = *entry.Label
		}
		result = theme.Fg("dim", "[label: "+label+"]")
	case "session_info":
		if entry.Name != nil {
			result = theme.Fg("dim", "[title: ") + theme.Fg("dim", *entry.Name) + theme.Fg("dim", "]")
		} else {
			result = theme.Fg("dim", "[title: ") + theme.Italic(theme.Fg("dim", "empty")) + theme.Fg("dim", "]")
		}
	}

	if isSelected {
		return theme.Bold(result)
	}
	return result
}

func (t *TreeList) formatLabelTimestamp(timestamp string) string {
	date, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		date, err = time.Parse(time.RFC3339, timestamp)
		if err != nil {
			return ""
		}
	}
	date = date.Local()
	now := t.now()
	timeText := pad2(date.Hour()) + ":" + pad2(date.Minute())
	if date.Year() == now.Year() && date.Month() == now.Month() && date.Day() == now.Day() {
		return timeText
	}
	month := int(date.Month())
	day := date.Day()
	if date.Year() == now.Year() {
		return itoa(month) + "/" + itoa(day) + " " + timeText
	}
	year := itoa(date.Year())
	if len(year) > 2 {
		year = year[len(year)-2:]
	}
	return year + "/" + itoa(month) + "/" + itoa(day) + " " + timeText
}

func pad2(value int) string {
	if value < 10 {
		return "0" + itoa(value)
	}
	return itoa(value)
}

func (t *TreeList) extractContent(content json.RawMessage) string {
	full := t.extractFullContent(content)
	if len(full) > 200 {
		return full[:200]
	}
	return full
}

func (t *TreeList) extractFullContent(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return ""
	}
	var builder strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			builder.WriteString(block.Text)
		}
	}
	return builder.String()
}

func (t *TreeList) getEntryCopyText(node *coding.SessionTreeNode) (string, bool) {
	entry := node.Entry
	var text string
	switch entry.Type {
	case "message":
		message := decodeTreeMessage(entry.Message)
		if message.Role == "bashExecution" {
			text = message.Command
		} else if len(message.Content) > 0 {
			text = t.extractFullContent(message.Content)
			if text == "" && message.Role == "assistant" {
				text = message.ErrorMessage
			}
		}
	case "custom_message":
		text = t.extractFullContent(entry.Content)
	case "compaction":
		text = entry.Summary
	case "branch_summary":
		text = entry.Summary
	}
	if strings.TrimSpace(text) == "" {
		return text, false
	}
	return text, true
}

func hasTextContent(content json.RawMessage) bool {
	if len(content) == 0 {
		return false
	}
	var text string
	if err := json.Unmarshal(content, &text); err == nil {
		return strings.TrimSpace(text) != ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return false
	}
	for _, block := range blocks {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			return true
		}
	}
	return false
}

func decodeTreeMessage(raw json.RawMessage) treeMessage {
	message := treeMessage{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &message)
	}
	return message
}

type treeToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

func toolCallsFromContent(content json.RawMessage) []treeToolCall {
	if len(content) == 0 {
		return nil
	}
	var blocks []struct {
		Type      string         `json:"type"`
		ID        string         `json:"id"`
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(content, &blocks); err != nil {
		return nil
	}
	var calls []treeToolCall
	for _, block := range blocks {
		if block.Type == "toolCall" {
			calls = append(calls, treeToolCall{ID: block.ID, Name: block.Name, Arguments: block.Arguments})
		}
	}
	return calls
}

func treeShortenPath(path string) string {
	home := os.Getenv("HOME")
	if home == "" {
		home = os.Getenv("USERPROFILE")
	}
	if home != "" && strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

func (t *TreeList) formatToolCall(name string, args map[string]any) string {
	argString := func(key string) string {
		value, ok := args[key]
		if !ok || value == nil {
			return ""
		}
		text, _ := value.(string)
		return text
	}
	pathArg := func() string {
		if value := argString("path"); value != "" {
			return value
		}
		return argString("file_path")
	}
	intArg := func(key string) (int, bool) {
		value, ok := args[key]
		if !ok {
			return 0, false
		}
		switch typed := value.(type) {
		case float64:
			return int(typed), true
		case int:
			return typed, true
		}
		return 0, false
	}

	switch name {
	case "read":
		path := treeShortenPath(pathArg())
		offset, hasOffset := intArg("offset")
		limit, hasLimit := intArg("limit")
		display := path
		if hasOffset || hasLimit {
			start := offset
			if !hasOffset {
				start = 1
			}
			end := ""
			if hasLimit {
				end = "-" + itoa(start+limit-1)
			}
			display += ":" + itoa(start) + end
		}
		return "[read: " + display + "]"
	case "write":
		return "[write: " + treeShortenPath(pathArg()) + "]"
	case "edit":
		return "[edit: " + treeShortenPath(pathArg()) + "]"
	case "bash":
		rawCmd := argString("command")
		cmd := strings.TrimSpace(strings.NewReplacer("\n", " ", "\t", " ").Replace(rawCmd))
		if len(cmd) > 50 {
			cmd = cmd[:50]
		}
		suffix := ""
		if len(rawCmd) > 50 {
			suffix = "..."
		}
		return "[bash: " + cmd + suffix + "]"
	case "grep":
		path := argString("path")
		if path == "" {
			path = "."
		}
		return "[grep: /" + argString("pattern") + "/ in " + treeShortenPath(path) + "]"
	case "find":
		path := argString("path")
		if path == "" {
			path = "."
		}
		return "[find: " + argString("pattern") + " in " + treeShortenPath(path) + "]"
	case "ls":
		path := argString("path")
		if path == "" {
			path = "."
		}
		return "[ls: " + treeShortenPath(path) + "]"
	default:
		encoded, _ := json.Marshal(args)
		argsStr := string(encoded)
		suffix := ""
		if len(argsStr) > 40 {
			argsStr = argsStr[:40]
			suffix = "..."
		}
		return "[" + name + ": " + argsStr + suffix + "]"
	}
}

// HandleInput processes the tree input.
func (t *TreeList) HandleInput(keyData string) {
	kb := tui.GetKeybindings()
	switch {
	case kb.Matches(keyData, "tui.select.up"):
		if t.selectedIndex == 0 {
			t.selectedIndex = len(t.filteredNodes) - 1
		} else {
			t.selectedIndex--
		}
	case kb.Matches(keyData, "tui.select.down"):
		if t.selectedIndex == len(t.filteredNodes)-1 {
			t.selectedIndex = 0
		} else {
			t.selectedIndex++
		}
	case kb.Matches(keyData, "app.tree.foldOrUp"):
		current := t.selectedNode()
		if current != nil && t.isFoldable(current.Node.Entry.ID) && !t.foldedNodes[current.Node.Entry.ID] {
			t.foldedNodes[current.Node.Entry.ID] = true
			t.ApplyFilter()
		} else {
			t.selectedIndex = t.findBranchSegmentStart("up")
		}
	case kb.Matches(keyData, "app.tree.unfoldOrDown"):
		current := t.selectedNode()
		if current != nil && t.foldedNodes[current.Node.Entry.ID] {
			delete(t.foldedNodes, current.Node.Entry.ID)
			t.ApplyFilter()
		} else {
			t.selectedIndex = t.findBranchSegmentStart("down")
		}
	case kb.Matches(keyData, "tui.editor.cursorLeft") || kb.Matches(keyData, "tui.select.pageUp"):
		t.selectedIndex = max(0, t.selectedIndex-t.maxVisibleLines)
	case kb.Matches(keyData, "tui.editor.cursorRight") || kb.Matches(keyData, "tui.select.pageDown"):
		t.selectedIndex = min(len(t.filteredNodes)-1, t.selectedIndex+t.maxVisibleLines)
	case kb.Matches(keyData, "tui.select.confirm"):
		if node := t.selectedNode(); node != nil && t.OnSelect != nil {
			t.OnSelect(node.Node.Entry.ID)
		}
	case kb.Matches(keyData, "app.message.copy"):
		t.CopySelected()
	case kb.Matches(keyData, "tui.select.cancel"):
		if t.searchQuery != "" {
			t.searchQuery = ""
			t.foldedNodes = map[string]bool{}
			t.ApplyFilter()
		} else if t.OnCancel != nil {
			t.OnCancel()
		}
	case kb.Matches(keyData, "app.tree.filter.default"):
		t.filterMode = FilterDefault
		t.foldedNodes = map[string]bool{}
		t.ApplyFilter()
	case kb.Matches(keyData, "app.tree.filter.noTools"):
		t.filterMode = toggleFilterMode(t.filterMode, FilterNoTools)
		t.foldedNodes = map[string]bool{}
		t.ApplyFilter()
	case kb.Matches(keyData, "app.tree.filter.userOnly"):
		t.filterMode = toggleFilterMode(t.filterMode, FilterUserOnly)
		t.foldedNodes = map[string]bool{}
		t.ApplyFilter()
	case kb.Matches(keyData, "app.tree.filter.labeledOnly"):
		t.filterMode = toggleFilterMode(t.filterMode, FilterLabeledOnly)
		t.foldedNodes = map[string]bool{}
		t.ApplyFilter()
	case kb.Matches(keyData, "app.tree.filter.all"):
		t.filterMode = toggleFilterMode(t.filterMode, FilterAll)
		t.foldedNodes = map[string]bool{}
		t.ApplyFilter()
	case kb.Matches(keyData, "app.tree.filter.cycleBackward"):
		index := indexOfFilterMode(t.filterMode)
		t.filterMode = TreeFilterModes[(index-1+len(TreeFilterModes))%len(TreeFilterModes)]
		t.foldedNodes = map[string]bool{}
		t.ApplyFilter()
	case kb.Matches(keyData, "app.tree.filter.cycleForward"):
		index := indexOfFilterMode(t.filterMode)
		t.filterMode = TreeFilterModes[(index+1)%len(TreeFilterModes)]
		t.foldedNodes = map[string]bool{}
		t.ApplyFilter()
	case kb.Matches(keyData, "tui.editor.deleteCharBackward"):
		if len(t.searchQuery) > 0 {
			t.searchQuery = t.searchQuery[:len(t.searchQuery)-1]
			t.foldedNodes = map[string]bool{}
			t.ApplyFilter()
		}
	case kb.Matches(keyData, "app.tree.editLabel"):
		if node := t.selectedNode(); node != nil && t.OnLabelEdit != nil {
			t.OnLabelEdit(node.Node.Entry.ID, node.Node.Label, node.Node.HasLabel)
		}
	case kb.Matches(keyData, "app.tree.toggleLabelTimestamp"):
		t.showLabelTimestamps = !t.showLabelTimestamps
	default:
		hasControlChars := false
		for _, ch := range keyData {
			code := int(ch)
			if code < 32 || code == 0x7f || (code >= 0x80 && code <= 0x9f) {
				hasControlChars = true
				break
			}
		}
		if !hasControlChars && len(keyData) > 0 {
			t.searchQuery += keyData
			t.foldedNodes = map[string]bool{}
			t.ApplyFilter()
		}
	}
}

func toggleFilterMode(current FilterMode, mode FilterMode) FilterMode {
	if current == mode {
		return FilterDefault
	}
	return mode
}

func indexOfFilterMode(mode FilterMode) int {
	for index, candidate := range TreeFilterModes {
		if candidate == mode {
			return index
		}
	}
	return 0
}

func (t *TreeList) isFoldable(entryID string) bool {
	children := t.visibleChildrenMap[entryID]
	if len(children) == 0 {
		return false
	}
	parentID := t.visibleParentMap[entryID]
	if parentID == nil {
		return true
	}
	siblings := t.visibleChildrenMap[*parentID]
	return len(siblings) > 1
}

func (t *TreeList) findBranchSegmentStart(direction string) int {
	current := t.selectedNode()
	if current == nil {
		return t.selectedIndex
	}
	selectedID := current.Node.Entry.ID
	indexByEntryID := map[string]int{}
	for index, node := range t.filteredNodes {
		indexByEntryID[node.Node.Entry.ID] = index
	}
	currentID := selectedID
	if direction == "down" {
		for {
			children := t.visibleChildrenMap[currentID]
			if len(children) == 0 {
				return indexByEntryID[currentID]
			}
			if len(children) > 1 {
				return indexByEntryID[children[0]]
			}
			currentID = children[0]
		}
	}
	for {
		parentID := t.visibleParentMap[currentID]
		if parentID == nil {
			return indexByEntryID[currentID]
		}
		children := t.visibleChildrenMap[*parentID]
		if len(children) > 1 {
			segmentStart := indexByEntryID[currentID]
			if segmentStart < t.selectedIndex {
				return segmentStart
			}
		}
		currentID = *parentID
	}
}

// SearchLine renders the current search query.
type SearchLine struct {
	treeList *TreeList
}

// NewSearchLine creates the search line.
func NewSearchLine(treeList *TreeList) *SearchLine { return &SearchLine{treeList: treeList} }

// Invalidate is a no-op.
func (s *SearchLine) Invalidate() {}

// Render renders the search line.
func (s *SearchLine) Render(width int) []string {
	theme := ActiveTheme()
	query := s.treeList.GetSearchQuery()
	if query != "" {
		return []string{tui.TruncateToWidth("  "+theme.Fg("muted", "Type to search:")+" "+theme.Fg("accent", query), width, "", false)}
	}
	return []string{tui.TruncateToWidth("  "+theme.Fg("muted", "Type to search:"), width, "", false)}
}

// HandleInput is a no-op.
func (s *SearchLine) HandleInput(string) {}

type treeHelpItem struct {
	Keys       []tui.Keybinding
	Label      string
	LabelFirst bool
}

var treeHelpItems = []treeHelpItem{
	{Keys: []tui.Keybinding{"tui.select.up", "tui.select.down"}, Label: "move"},
	{Keys: []tui.Keybinding{"tui.editor.cursorLeft", "tui.editor.cursorRight"}, Label: "page"},
	{Keys: []tui.Keybinding{"app.tree.foldOrUp", "app.tree.unfoldOrDown"}, Label: "branch"},
	{Keys: []tui.Keybinding{"app.message.copy"}, Label: "copy"},
	{Keys: []tui.Keybinding{"app.tree.editLabel"}, Label: "label"},
	{Keys: []tui.Keybinding{"app.tree.toggleLabelTimestamp"}, Label: "label time"},
	{
		Keys: []tui.Keybinding{
			"app.tree.filter.default", "app.tree.filter.noTools", "app.tree.filter.userOnly",
			"app.tree.filter.labeledOnly", "app.tree.filter.all",
		},
		Label: "filters", LabelFirst: true,
	},
	{Keys: []tui.Keybinding{"app.tree.filter.cycleForward", "app.tree.filter.cycleBackward"}, Label: "cycle", LabelFirst: true},
}

// TreeHelp renders the keybinding help.
type TreeHelp struct{}

// Invalidate is a no-op.
func (h *TreeHelp) Invalidate() {}

// Render renders the help rows.
func (h *TreeHelp) Render(width int) []string {
	theme := ActiveTheme()
	items := make([]string, 0, len(treeHelpItems))
	for _, item := range treeHelpItems {
		text := formatHelpKeys(item.Keys)
		if text == "" {
			items = append(items, item.Label)
			continue
		}
		if item.LabelFirst {
			items = append(items, item.Label+" "+text)
		} else {
			items = append(items, text+" "+item.Label)
		}
	}

	availableWidth := max(1, width)
	const indent = "  "
	const separator = " · "
	var lines []string
	currentLine := ""

	for _, item := range items {
		candidate := ""
		if currentLine != "" {
			candidate = currentLine + separator + item
		} else if tui.VisibleWidth(indent+item) <= availableWidth {
			candidate = indent + item
		} else {
			candidate = item
		}
		if currentLine == "" || tui.VisibleWidth(candidate) <= availableWidth {
			currentLine = candidate
			continue
		}
		lines = append(lines, tui.WrapTextWithAnsi(strings.TrimRight(currentLine, " "), availableWidth)...)
		if tui.VisibleWidth(indent+item) <= availableWidth {
			currentLine = indent + item
		} else {
			currentLine = item
		}
	}
	if currentLine != "" {
		lines = append(lines, tui.WrapTextWithAnsi(strings.TrimRight(currentLine, " "), availableWidth)...)
	}

	result := make([]string, 0, len(lines))
	for _, line := range lines {
		result = append(result, theme.Fg("muted", line))
	}
	return result
}

func formatHelpKeys(keybindings []tui.Keybinding) string {
	var keys []string
	for _, keybinding := range keybindings {
		all := tui.GetKeybindings().GetKeys(keybinding)
		if len(all) > 0 {
			keys = append(keys, all[0])
		}
	}
	if len(keys) == 0 {
		return ""
	}
	text := FormatKeyText(compactRawKeys(keys), KeyTextFormatOptions{})
	return replaceWholeWords(text)
}

// replaceWholeWords applies the upstream word-boundary replacements.
func replaceWholeWords(text string) string {
	var builder strings.Builder
	for index := 0; index < len(text); {
		matched := false
		for _, pair := range []struct{ from, to string }{
			{"pageUp", "pgup"}, {"pageDown", "pgdn"},
			{"up", "↑"}, {"down", "↓"}, {"left", "←"}, {"right", "→"},
		} {
			if !strings.HasPrefix(text[index:], pair.from) {
				continue
			}
			before := byte(' ')
			if index > 0 {
				before = text[index-1]
			}
			after := byte(' ')
			if index+len(pair.from) < len(text) {
				after = text[index+len(pair.from)]
			}
			if !isWordByte(before) && !isWordByte(after) {
				builder.WriteString(pair.to)
				index += len(pair.from)
				matched = true
				break
			}
		}
		if matched {
			continue
		}
		builder.WriteByte(text[index])
		index++
	}
	return builder.String()
}

func isWordByte(value byte) bool {
	return value == '_' || (value >= '0' && value <= '9') || (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z')
}

func compactRawKeys(keys []string) string {
	if len(keys) == 1 {
		return keys[0]
	}
	type part struct{ prefix, suffix string }
	parts := make([]part, 0, len(keys))
	for _, key := range keys {
		index := strings.LastIndex(key, "+")
		if index == -1 {
			parts = append(parts, part{suffix: key})
		} else {
			parts = append(parts, part{prefix: key[:index+1], suffix: key[index+1:]})
		}
	}
	prefix := parts[0].prefix
	if prefix != "" {
		same := true
		for _, entry := range parts {
			if entry.prefix != prefix {
				same = false
				break
			}
		}
		if same {
			suffixes := make([]string, 0, len(parts))
			for _, entry := range parts {
				suffixes = append(suffixes, entry.suffix)
			}
			return prefix + strings.Join(suffixes, "/")
		}
	}
	return strings.Join(keys, "/")
}

// LabelInput edits an entry label.
type LabelInput struct {
	input   *tui.Input
	entryID string

	OnSubmit func(entryID string, label *string)
	OnCancel func()

	focused bool
}

// NewLabelInput creates the label input.
func NewLabelInput(entryID string, currentLabel string, hasLabel bool) *LabelInput {
	component := &LabelInput{input: tui.NewInput(tui.InputOptions{}), entryID: entryID}
	if hasLabel {
		component.input.SetValue(currentLabel)
	}
	return component
}

// SetFocused focuses the input.
func (l *LabelInput) SetFocused(focused bool) {
	l.focused = focused
	l.input.SetFocused(focused)
}

// Focused reports the focus state.
func (l *LabelInput) Focused() bool { return l.focused }

// Invalidate is a no-op.
func (l *LabelInput) Invalidate() {}

// Render renders the label input.
func (l *LabelInput) Render(width int) []string {
	theme := ActiveTheme()
	const indent = "  "
	availableWidth := width - len(indent)
	var lines []string
	lines = append(lines, tui.TruncateToWidth(indent+theme.Fg("muted", "Label (empty to remove):"), width, "", false))
	for _, line := range l.input.Render(availableWidth) {
		lines = append(lines, tui.TruncateToWidth(indent+line, width, "", false))
	}
	lines = append(lines, tui.TruncateToWidth(
		indent+KeyHint("tui.select.confirm", "save")+"  "+KeyHint("tui.select.cancel", "cancel"), width, "", false))
	return lines
}

// HandleInput processes the label input.
func (l *LabelInput) HandleInput(keyData string) {
	kb := tui.GetKeybindings()
	switch {
	case kb.Matches(keyData, "tui.select.confirm"):
		value := strings.TrimSpace(l.input.Value())
		if l.OnSubmit != nil {
			if value == "" {
				l.OnSubmit(l.entryID, nil)
			} else {
				l.OnSubmit(l.entryID, &value)
			}
		}
	case kb.Matches(keyData, "tui.select.cancel"):
		if l.OnCancel != nil {
			l.OnCancel()
		}
	default:
		l.input.HandleInput(keyData)
	}
}

// TreeSelectorComponent renders the session tree selector.
type TreeSelectorComponent struct {
	*tui.Container

	treeList            *TreeList
	labelInput          *LabelInput
	labelInputContainer *tui.Container
	treeContainer       *tui.Container
	onLabelChange       func(entryID string, label *string)

	OnCopy func(text string, hasText bool)

	focused bool

	scheduleTimer func(ms int, fn func())
}

// TreeSelectorOptions configure the selector.
type TreeSelectorOptions struct {
	OnLabelChange func(entryID string, label *string)
	// InitialSelectedID preselects an entry.
	InitialSelectedID *string
	// InitialFilterMode starts with a filter applied.
	InitialFilterMode FilterMode
	// ScheduleTimer overrides the empty-tree auto-cancel timer (test seam).
	ScheduleTimer func(ms int, fn func())
	// Now overrides the clock (test seam).
	Now func() time.Time
}

// NewTreeSelectorComponent creates the selector.
func NewTreeSelectorComponent(tree []*coding.SessionTreeNode, currentLeafID *string, terminalHeight int, onSelect func(entryID string), onCancel func(), options TreeSelectorOptions) *TreeSelectorComponent {
	component := &TreeSelectorComponent{
		Container:     &tui.Container{},
		onLabelChange: options.OnLabelChange,
		scheduleTimer: func(ms int, fn func()) { time.AfterFunc(time.Duration(ms)*time.Millisecond, fn) },
	}
	if options.ScheduleTimer != nil {
		component.scheduleTimer = options.ScheduleTimer
	}

	maxVisibleLines := max(5, terminalHeight/2)
	component.treeList = NewTreeList(tree, currentLeafID, maxVisibleLines, options.InitialSelectedID, options.InitialFilterMode)
	if options.Now != nil {
		component.treeList.SetNow(options.Now)
	}
	component.treeList.OnSelect = onSelect
	component.treeList.OnCancel = onCancel
	component.treeList.OnCopy = func(text string, hasText bool) {
		if component.OnCopy != nil {
			component.OnCopy(text, hasText)
		}
	}
	component.treeList.OnLabelEdit = func(entryID string, currentLabel string, hasLabel bool) {
		component.showLabelInput(entryID, currentLabel, hasLabel)
	}

	component.treeContainer = &tui.Container{}
	component.treeContainer.AddChild(component.treeList)
	component.labelInputContainer = &tui.Container{}

	theme := ActiveTheme()
	component.AddChild(tui.NewSpacer(1))
	component.AddChild(NewDynamicBorder(nil))
	component.AddChild(tui.NewText(theme.Bold("  Session Tree"), 1, 0, nil))
	component.AddChild(&TreeHelp{})
	component.AddChild(NewSearchLine(component.treeList))
	component.AddChild(NewDynamicBorder(nil))
	component.AddChild(tui.NewSpacer(1))
	component.AddChild(component.treeContainer)
	component.AddChild(component.labelInputContainer)
	component.AddChild(tui.NewSpacer(1))
	component.AddChild(NewDynamicBorder(nil))

	if len(tree) == 0 {
		component.scheduleTimer(100, func() {
			if onCancel != nil {
				onCancel()
			}
		})
	}
	return component
}

func (c *TreeSelectorComponent) showLabelInput(entryID string, currentLabel string, hasLabel bool) {
	c.labelInput = NewLabelInput(entryID, currentLabel, hasLabel)
	c.labelInput.OnSubmit = func(id string, label *string) {
		c.treeList.UpdateNodeLabel(id, label, "")
		if c.onLabelChange != nil {
			c.onLabelChange(id, label)
		}
		c.hideLabelInput()
	}
	c.labelInput.OnCancel = func() { c.hideLabelInput() }
	c.labelInput.SetFocused(c.focused)

	c.treeContainer.Clear()
	c.labelInputContainer.Clear()
	c.labelInputContainer.AddChild(c.labelInput)
}

func (c *TreeSelectorComponent) hideLabelInput() {
	c.labelInput = nil
	c.labelInputContainer.Clear()
	c.treeContainer.Clear()
	c.treeContainer.AddChild(c.treeList)
}

// HandleInput processes the selector input.
func (c *TreeSelectorComponent) HandleInput(keyData string) {
	if c.labelInput != nil {
		c.labelInput.HandleInput(keyData)
		return
	}
	c.treeList.HandleInput(keyData)
}

// SetFocused focuses the selector.
func (c *TreeSelectorComponent) SetFocused(focused bool) {
	c.focused = focused
	if c.labelInput != nil {
		c.labelInput.SetFocused(focused)
	}
}

// Focused reports the focus state.
func (c *TreeSelectorComponent) Focused() bool { return c.focused }

// GetTreeList returns the tree list.
func (c *TreeSelectorComponent) GetTreeList() *TreeList { return c.treeList }
