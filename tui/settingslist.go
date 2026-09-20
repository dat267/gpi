package tui

// Port of src/components/settings-list.ts: a settings list with optional
// fuzzy search, value cycling, and submenus.

// SettingItem is one settings row.
type SettingItem struct {
	ID           string
	Label        string
	Description  string
	CurrentValue string
	// Values, when set, are cycled by Enter/Space.
	Values []string
	// Submenu, when set, is opened by Enter. The done callback reports the
	// optional selected value and an optional navigation target.
	Submenu func(currentValue string, done func(result SubmenuResult)) Component
}

// SubmenuResult is passed to a submenu's done callback.
type SubmenuResult struct {
	SelectedValue    string
	HasSelectedValue bool
	NavigateTo       string
}

// SettingsListTheme styles the list parts.
type SettingsListTheme struct {
	Label       func(text string, selected bool) string
	Value       func(text string, selected bool) string
	Description func(text string) string
	Cursor      string
	Hint        func(text string) string
}

// SettingsListOptions configure the list.
type SettingsListOptions struct {
	EnableSearch bool
}

// SettingsList renders configurable settings rows.
type SettingsList struct {
	items         []SettingItem
	filteredItems []SettingItem
	// filteredIndexes maps display positions to indexes in items, so
	// activation mutates the stored item rather than a filtered copy (D97).
	filteredIndexes   []int
	theme             SettingsListTheme
	selectedIndex     int
	mousePressedIndex int
	hasMousePressed   bool
	maxVisible        int
	onChange          func(id string, newValue string)
	onCancel          func()
	searchInput       *Input
	searchEnabled     bool

	submenuComponent    Component
	submenuItemIndex    int
	hasSubmenuItemIndex bool
	navigateAfterClose  string
}

// NewSettingsList creates a settings list.
func NewSettingsList(items []SettingItem, maxVisible int, theme SettingsListTheme, onChange func(id string, newValue string), onCancel func(), options SettingsListOptions) *SettingsList {
	list := &SettingsList{
		items:         items,
		filteredItems: items,
		maxVisible:    maxVisible,
		theme:         theme,
		onChange:      onChange,
		onCancel:      onCancel,
		searchEnabled: options.EnableSearch,
	}
	if list.theme.Label == nil {
		list.theme.Label = func(text string, selected bool) string { return text }
	}
	if list.theme.Value == nil {
		list.theme.Value = func(text string, selected bool) string { return text }
	}
	if list.theme.Description == nil {
		list.theme.Description = func(text string) string { return text }
	}
	if list.theme.Hint == nil {
		list.theme.Hint = func(text string) string { return text }
	}
	if list.searchEnabled {
		list.searchInput = NewInput(InputOptions{})
	}
	return list
}

// UpdateValue updates an item's current value.
func (s *SettingsList) UpdateValue(id string, newValue string) {
	for index := range s.items {
		if s.items[index].ID == id {
			s.items[index].CurrentValue = newValue
			return
		}
	}
}

// SelectItem moves the selection to the item with the given id.
func (s *SettingsList) SelectItem(id string) {
	items := s.getDisplayItems()
	for index, item := range items {
		if item.ID == id {
			s.selectedIndex = index
			return
		}
	}
}

// Invalidate invalidates the active submenu.
func (s *SettingsList) Invalidate() {
	if s.submenuComponent != nil {
		s.submenuComponent.Invalidate()
	}
}

// Render renders the list (or the active submenu).
func (s *SettingsList) Render(width int) []string {
	if s.submenuComponent != nil {
		return s.submenuComponent.Render(width)
	}
	return s.renderMainList(width)
}

func (s *SettingsList) renderMainList(width int) []string {
	var lines []string

	if s.searchEnabled && s.searchInput != nil {
		lines = append(lines, s.searchInput.Render(width)...)
		lines = append(lines, "")
	}

	if len(s.items) == 0 {
		lines = append(lines, s.theme.Hint("  No settings available"))
		if s.searchEnabled {
			s.addHintLine(&lines, width)
		}
		return lines
	}

	displayItems := s.getDisplayItems()
	if len(displayItems) == 0 {
		lines = append(lines, TruncateToWidth(s.theme.Hint("  No matching settings"), width, "...", false))
		s.addHintLine(&lines, width)
		return lines
	}

	startIndex, endIndex := s.getVisibleRange(displayItems)

	// Max label width for alignment.
	maxLabelWidth := 36
	widest := 0
	for _, item := range s.items {
		widest = maxInt(widest, VisibleWidth(item.Label))
	}
	maxLabelWidth = minInt(maxLabelWidth, widest)

	for i := startIndex; i < endIndex; i++ {
		item := displayItems[i]
		isSelected := i == s.selectedIndex
		prefix := "  "
		if isSelected {
			prefix = s.theme.Cursor
		}
		prefixWidth := VisibleWidth(prefix)

		labelPadded := item.Label + repeatSpaces(maxInt(0, maxLabelWidth-VisibleWidth(item.Label)))
		labelText := s.theme.Label(labelPadded, isSelected)

		separator := "  "
		usedWidth := prefixWidth + maxLabelWidth + VisibleWidth(separator)
		valueMaxWidth := width - usedWidth - 2

		valueText := s.theme.Value(TruncateToWidth(item.CurrentValue, valueMaxWidth, "", false), isSelected)

		lines = append(lines, TruncateToWidth(prefix+labelText+separator+valueText, width, "...", false))
	}

	if startIndex > 0 || endIndex < len(displayItems) {
		scrollText := "  (" + itoa(s.selectedIndex+1) + "/" + itoa(len(displayItems)) + ")"
		lines = append(lines, s.theme.Hint(TruncateToWidth(scrollText, width-2, "", false)))
	}

	if s.selectedIndex >= 0 && s.selectedIndex < len(displayItems) && displayItems[s.selectedIndex].Description != "" {
		lines = append(lines, "")
		wrappedDesc := WrapTextWithAnsi(displayItems[s.selectedIndex].Description, width-4)
		for _, line := range wrappedDesc {
			lines = append(lines, s.theme.Description("  "+line))
		}
	}

	s.addHintLine(&lines, width)
	return lines
}

// HandleMouse processes wheel scrolling, search-input clicks, and selection.
func (s *SettingsList) HandleMouse(event TuiMouseEvent) *TuiMouseDispatchResult {
	if s.submenuComponent != nil {
		result := DispatchMouseEvent(s.submenuComponent, event)
		if result != nil {
			result.Focus = true
		}
		return result
	}

	if s.searchEnabled && s.searchInput != nil {
		if event.Y == 0 {
			result := DispatchMouseEvent(s.searchInput, event)
			if result != nil {
				result.Focus = true
			}
			return result
		}
		if event.Y == 1 {
			return nil
		}
	}

	displayItems := s.getDisplayItems()
	if len(displayItems) == 0 {
		return nil
	}
	if event.Type == MouseWheel && event.HasWheel && event.WheelDelta != 0 {
		delta := 1
		if event.WheelDelta < 0 {
			delta = -1
		}
		previousIndex := s.selectedIndex
		s.selectedIndex = maxInt(0, minInt(len(displayItems)-1, s.selectedIndex+delta))
		return &TuiMouseDispatchResult{TuiMouseEventResult: TuiMouseEventResult{
			Handled: true, Render: s.selectedIndex != previousIndex, HasRender: true,
		}}
	}
	// Hover must not change selection.
	if event.Button != MouseButtonLeft || (event.Type != MousePress && event.Type != MouseClick) {
		return nil
	}

	rowOffset := 0
	if s.searchEnabled {
		rowOffset = 2
	}
	startIndex, endIndex := s.getVisibleRange(displayItems)
	itemIndex := startIndex + event.Y - rowOffset
	if itemIndex < startIndex || itemIndex >= endIndex {
		return nil
	}
	if event.Type == MousePress {
		s.mousePressedIndex = itemIndex
		s.hasMousePressed = true
		s.selectedIndex = itemIndex
		return &TuiMouseDispatchResult{TuiMouseEventResult: TuiMouseEventResult{Handled: true, Focus: true}}
	}
	// click
	s.selectedIndex = itemIndex
	if s.hasMousePressed {
		s.selectedIndex = s.mousePressedIndex
	}
	s.hasMousePressed = false
	s.activateItem()
	return &TuiMouseDispatchResult{TuiMouseEventResult: TuiMouseEventResult{Handled: true}}
}

// HandleInput processes navigation, activation, and search input.
func (s *SettingsList) HandleInput(data string) {
	if s.submenuComponent != nil {
		if handler, ok := s.submenuComponent.(InputHandler); ok {
			handler.HandleInput(data)
		}
		return
	}

	kb := GetKeybindings()
	displayItems := s.getDisplayItems()
	switch {
	case kb.Matches(data, "tui.select.up"):
		if len(displayItems) == 0 {
			return
		}
		if s.selectedIndex == 0 {
			s.selectedIndex = len(displayItems) - 1
		} else {
			s.selectedIndex--
		}
	case kb.Matches(data, "tui.select.down"):
		if len(displayItems) == 0 {
			return
		}
		if s.selectedIndex == len(displayItems)-1 {
			s.selectedIndex = 0
		} else {
			s.selectedIndex++
		}
	case kb.Matches(data, "tui.select.confirm") ||
		(data == " " && (!s.searchEnabled || s.searchInput == nil || len(s.searchInput.Value()) == 0)):
		s.activateItem()
	case kb.Matches(data, "tui.select.cancel"):
		s.onCancel()
	default:
		if s.searchEnabled && s.searchInput != nil {
			s.searchInput.HandleInput(data)
			s.applyFilter(s.searchInput.Value())
		}
	}
}

// SelectedIndex returns the current selection index.
func (s *SettingsList) SelectedIndex() int { return s.selectedIndex }

// SearchInput exposes the search input, if enabled.
func (s *SettingsList) SearchInput() *Input { return s.searchInput }

func (s *SettingsList) getDisplayItems() []SettingItem {
	if !s.searchEnabled || s.filteredIndexes == nil {
		return s.items
	}
	// Rebuild from the stored items so value mutations are visible (D97):
	// upstream's filtered array holds references to the same objects.
	display := make([]SettingItem, 0, len(s.filteredIndexes))
	for _, index := range s.filteredIndexes {
		if index >= 0 && index < len(s.items) {
			display = append(display, s.items[index])
		}
	}
	return display
}

func (s *SettingsList) getVisibleRange(displayItems []SettingItem) (startIndex int, endIndex int) {
	startIndex = maxInt(0, minInt(s.selectedIndex-s.maxVisible/2, len(displayItems)-s.maxVisible))
	return startIndex, minInt(startIndex+s.maxVisible, len(displayItems))
}

func (s *SettingsList) activateItem() {
	displayItems := s.getDisplayItems()
	if s.selectedIndex < 0 || s.selectedIndex >= len(displayItems) {
		return
	}
	realIndex := s.realItemIndex(s.selectedIndex)
	if realIndex < 0 || realIndex >= len(s.items) {
		return
	}
	item := &s.items[realIndex]

	if item.Submenu != nil {
		s.submenuItemIndex = s.selectedIndex
		s.hasSubmenuItemIndex = true
		s.submenuComponent = item.Submenu(item.CurrentValue, func(result SubmenuResult) {
			if result.HasSelectedValue {
				s.items[realIndex].CurrentValue = result.SelectedValue
				s.onChange(s.items[realIndex].ID, result.SelectedValue)
			}
			if result.NavigateTo != "" {
				s.navigateAfterClose = result.NavigateTo
			}
			s.closeSubmenu()
		})
		return
	}

	if len(item.Values) > 0 {
		currentIndex := -1
		for index, value := range item.Values {
			if value == item.CurrentValue {
				currentIndex = index
				break
			}
		}
		nextIndex := (currentIndex + 1) % len(item.Values)
		newValue := item.Values[nextIndex]
		item.CurrentValue = newValue
		s.onChange(item.ID, newValue)
	}
}

// realItemIndex resolves a display position to an index in items.
func (s *SettingsList) realItemIndex(displayIndex int) int {
	if !s.searchEnabled || s.filteredIndexes == nil {
		return displayIndex
	}
	if displayIndex < 0 || displayIndex >= len(s.filteredIndexes) {
		return -1
	}
	return s.filteredIndexes[displayIndex]
}

func (s *SettingsList) closeSubmenu() {
	s.submenuComponent = nil
	if s.navigateAfterClose != "" {
		id := s.navigateAfterClose
		s.navigateAfterClose = ""
		s.hasSubmenuItemIndex = false
		s.SelectItem(id)
		s.activateItem()
		return
	}
	if s.hasSubmenuItemIndex {
		s.selectedIndex = s.submenuItemIndex
		s.hasSubmenuItemIndex = false
	}
}

func (s *SettingsList) applyFilter(query string) {
	s.filteredItems = FuzzyFilter(s.items, query, func(item SettingItem) string { return item.Label })
	s.filteredIndexes = make([]int, 0, len(s.filteredItems))
	used := make([]bool, len(s.items))
	for _, filtered := range s.filteredItems {
		for index, item := range s.items {
			if used[index] || item.ID != filtered.ID {
				continue
			}
			used[index] = true
			s.filteredIndexes = append(s.filteredIndexes, index)
			break
		}
	}
	s.selectedIndex = 0
}

func (s *SettingsList) addHintLine(lines *[]string, width int) {
	*lines = append(*lines, "")
	hint := "  Enter/Space to change · Esc to cancel"
	if s.searchEnabled {
		hint = "  Type to search · Enter/Space to change · Esc to cancel"
	}
	*lines = append(*lines, TruncateToWidth(s.theme.Hint(hint), width, "...", false))
}

var (
	_ Component    = (*SettingsList)(nil)
	_ MouseHandler = (*SettingsList)(nil)
)
