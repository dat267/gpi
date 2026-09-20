package interactive

import (
	"strings"
	"time"

	"github.com/dat267/gpi/coding"
	"github.com/dat267/gpi/tui"
)

// Port of src/modes/interactive/components/{theme-selector,
// show-images-selector,thinking-selector,user-message-selector,
// trust-selector,custom-message}.ts.

var themeSelectListLayout = tui.SelectListLayoutOptions{
	MinPrimaryColumnWidth: 12, HasMin: true,
	MaxPrimaryColumnWidth: 32, HasMax: true,
}

// ThemeSelectorComponent renders the theme selector.
type ThemeSelectorComponent struct {
	*tui.Container

	selectList *tui.SelectList
	onPreview  func(themeName string)
}

// NewThemeSelectorComponent creates the selector.
func NewThemeSelectorComponent(currentTheme string, onSelect func(string), onCancel func(), onPreview func(string)) *ThemeSelectorComponent {
	component := &ThemeSelectorComponent{Container: &tui.Container{}, onPreview: onPreview}

	themes := AvailableThemes()
	items := make([]tui.SelectItem, 0, len(themes))
	for _, name := range themes {
		description := ""
		if name == currentTheme {
			description = "(current)"
		}
		items = append(items, tui.SelectItem{Value: name, Label: name, Description: description})
	}

	component.AddChild(NewDynamicBorder(nil))
	component.selectList = tui.NewSelectList(items, 10, GetSelectListTheme(), themeSelectListLayout)
	for index, name := range themes {
		if name == currentTheme {
			component.selectList.SetSelectedIndex(index)
			break
		}
	}
	component.selectList.OnSelect = func(item tui.SelectItem) {
		if onSelect != nil {
			onSelect(item.Value)
		}
	}
	component.selectList.OnCancel = func() {
		if onCancel != nil {
			onCancel()
		}
	}
	component.selectList.OnSelectionChange = func(item tui.SelectItem) {
		if component.onPreview != nil {
			component.onPreview(item.Value)
		}
	}
	component.AddChild(component.selectList)
	component.AddChild(NewDynamicBorder(nil))
	return component
}

// GetSelectList returns the underlying list.
func (c *ThemeSelectorComponent) GetSelectList() *tui.SelectList { return c.selectList }

// ShowImagesSelectorComponent renders the show-images selector.
type ShowImagesSelectorComponent struct {
	*tui.Container

	selectList *tui.SelectList
}

// NewShowImagesSelectorComponent creates the selector.
func NewShowImagesSelectorComponent(currentValue bool, onSelect func(show bool), onCancel func()) *ShowImagesSelectorComponent {
	component := &ShowImagesSelectorComponent{Container: &tui.Container{}}
	items := []tui.SelectItem{
		{Value: "yes", Label: "Yes", Description: "Show images inline in terminal"},
		{Value: "no", Label: "No", Description: "Show text placeholder instead"},
	}
	component.AddChild(NewDynamicBorder(nil))
	component.selectList = tui.NewSelectList(items, 5, GetSelectListTheme(), themeSelectListLayout)
	if currentValue {
		component.selectList.SetSelectedIndex(0)
	} else {
		component.selectList.SetSelectedIndex(1)
	}
	component.selectList.OnSelect = func(item tui.SelectItem) {
		if onSelect != nil {
			onSelect(item.Value == "yes")
		}
	}
	component.selectList.OnCancel = func() {
		if onCancel != nil {
			onCancel()
		}
	}
	component.AddChild(component.selectList)
	component.AddChild(NewDynamicBorder(nil))
	return component
}

// GetSelectList returns the underlying list.
func (c *ShowImagesSelectorComponent) GetSelectList() *tui.SelectList { return c.selectList }

// ThinkingLevelDescriptions maps thinking levels to descriptions.
var ThinkingLevelDescriptions = map[string]string{
	"off":     "No reasoning",
	"minimal": "Very brief reasoning (~1k tokens)",
	"low":     "Light reasoning (~2k tokens)",
	"medium":  "Moderate reasoning (~8k tokens)",
	"high":    "Deep reasoning (~16k tokens)",
	"xhigh":   "Extra-high reasoning (~32k tokens)",
	"max":     "Maximum reasoning",
}

// ThinkingSelectorComponent renders the thinking level selector.
type ThinkingSelectorComponent struct {
	*tui.Container

	searchInput          *tui.Input
	selectList           *tui.SelectList
	selectListChildIndex int
	allItems             []tui.SelectItem
	onSelect             func(level string)
	onCancel             func()
	onSelectAsDefault    func(level string)
	focused              bool
}

// NewThinkingSelectorComponent creates the selector.
func NewThinkingSelectorComponent(currentLevel string, availableLevels []string, onSelect func(string), onCancel func(), onSelectAsDefault func(string), defaultThinkingLevel string) *ThinkingSelectorComponent {
	theme := ActiveTheme()
	component := &ThinkingSelectorComponent{
		Container:         &tui.Container{},
		onSelect:          onSelect,
		onCancel:          onCancel,
		onSelectAsDefault: onSelectAsDefault,
	}

	for _, level := range availableLevels {
		marker := "  "
		if level == currentLevel {
			marker = "✓ "
		}
		description := ThinkingLevelDescriptions[level]
		if level == defaultThinkingLevel {
			description = ThinkingLevelDescriptions[level] + " · default"
		}
		component.allItems = append(component.allItems, tui.SelectItem{
			Value: level, Label: marker + level, Description: description,
		})
	}

	component.AddChild(NewDynamicBorder(nil))
	component.AddChild(tui.NewSpacer(1))
	component.AddChild(tui.NewText("Thinking Level", 0, 0, nil))
	component.AddChild(tui.NewSpacer(1))
	component.AddChild(tui.NewText(KeyDisplayText("app.thinking.cycle")+" cycles thinking levels in-session", 0, 0, nil))
	component.AddChild(tui.NewSpacer(1))

	component.searchInput = tui.NewInput(tui.InputOptions{})
	component.searchInput.OnSubmit = func(string) { component.selectList.HandleInput("\r") }
	component.AddChild(component.searchInput)
	component.AddChild(tui.NewSpacer(1))

	component.selectList = component.buildSelectList(component.allItems, currentLevel)
	component.selectListChildIndex = len(component.Children)
	component.AddChild(component.selectList)
	component.AddChild(tui.NewSpacer(1))
	component.AddChild(tui.NewText(theme.Fg("dim",
		"  "+KeyDisplayText("tui.select.confirm")+" to select · "+KeyDisplayText("app.thinking.save")+
			" to set as default · "+KeyDisplayText("tui.select.cancel")+" to cancel"), 0, 0, nil))
	component.AddChild(NewDynamicBorder(nil))
	return component
}

func (c *ThinkingSelectorComponent) buildSelectList(items []tui.SelectItem, preselect string) *tui.SelectList {
	maxVisible := len(items)
	if maxVisible < 1 {
		maxVisible = 1
	}
	list := tui.NewSelectList(items, maxVisible, GetSelectListTheme(), themeSelectListLayout)
	for index, item := range items {
		if item.Value == preselect {
			list.SetSelectedIndex(index)
			break
		}
	}
	list.OnSelect = func(item tui.SelectItem) {
		if c.onSelect != nil {
			c.onSelect(item.Value)
		}
	}
	list.OnCancel = func() {
		if c.onCancel != nil {
			c.onCancel()
		}
	}
	return list
}

func (c *ThinkingSelectorComponent) applyFilter(query string) {
	filtered := c.allItems
	if query != "" {
		filtered = tui.FuzzyFilter(c.allItems, query, func(item tui.SelectItem) string {
			return item.Value + " " + item.Description
		})
	}
	selectedValue := ""
	if selected, ok := c.selectList.GetSelectedItem(); ok {
		selectedValue = selected.Value
	}
	newList := c.buildSelectList(filtered, selectedValue)
	c.Children[c.selectListChildIndex] = newList
	c.selectList = newList
}

// HandleInput processes input.
func (c *ThinkingSelectorComponent) HandleInput(data string) {
	kb := tui.GetKeybindings()
	if kb.Matches(data, "app.thinking.save") && c.onSelectAsDefault != nil {
		if selected, ok := c.selectList.GetSelectedItem(); ok {
			c.onSelectAsDefault(selected.Value)
		}
		return
	}
	isNav := kb.Matches(data, "tui.select.up") || kb.Matches(data, "tui.select.down") ||
		kb.Matches(data, "tui.select.confirm") || kb.Matches(data, "tui.select.cancel")
	if isNav {
		c.selectList.HandleInput(data)
		return
	}
	c.searchInput.HandleInput(data)
	c.applyFilter(c.searchInput.Value())
}

// GetSelectList returns the underlying list.
func (c *ThinkingSelectorComponent) GetSelectList() *tui.SelectList { return c.selectList }

// SetFocused implements Focusable.
func (c *ThinkingSelectorComponent) SetFocused(focused bool) {
	c.focused = focused
	c.searchInput.SetFocused(focused)
}

// IsFocused implements Focusable.
func (c *ThinkingSelectorComponent) IsFocused() bool { return c.focused }

// UserMessageItem is a selectable user message.
type UserMessageItem struct {
	ID        string
	Text      string
	Timestamp string
}

// UserMessageList renders the user message list.
type UserMessageList struct {
	messages      []UserMessageItem
	selectedIndex int
	maxVisible    int

	OnSelect func(entryID string)
	OnCancel func()
}

// NewUserMessageList creates the list.
func NewUserMessageList(messages []UserMessageItem, initialSelectedID string) *UserMessageList {
	selected := -1
	if initialSelectedID != "" {
		for index, message := range messages {
			if message.ID == initialSelectedID {
				selected = index
				break
			}
		}
	}
	if selected < 0 {
		selected = maxIntLocal(0, len(messages)-1)
	}
	return &UserMessageList{messages: messages, selectedIndex: selected, maxVisible: 10}
}

// Invalidate drops cached state (none).
func (l *UserMessageList) Invalidate() {}

// Render renders the list.
func (l *UserMessageList) Render(width int) []string {
	theme := ActiveTheme()
	var lines []string
	if len(l.messages) == 0 {
		return []string{theme.Fg("muted", "  No user messages found")}
	}
	startIndex := maxIntLocal(0, minIntLocal(l.selectedIndex-l.maxVisible/2, len(l.messages)-l.maxVisible))
	endIndex := minIntLocal(startIndex+l.maxVisible, len(l.messages))
	for i := startIndex; i < endIndex; i++ {
		message := l.messages[i]
		isSelected := i == l.selectedIndex
		normalized := strings.TrimSpace(strings.ReplaceAll(message.Text, "\n", " "))
		cursor := "  "
		if isSelected {
			cursor = theme.Fg("accent", "› ")
		}
		truncated := tui.TruncateToWidth(normalized, width-2, "...", false)
		line := truncated
		if isSelected {
			line = theme.Bold(truncated)
		}
		lines = append(lines, cursor+line)
		lines = append(lines, theme.Fg("muted", "  Message "+itoa(i+1)+" of "+itoa(len(l.messages))))
		lines = append(lines, "")
	}
	if startIndex > 0 || endIndex < len(l.messages) {
		lines = append(lines, theme.Fg("muted", "  ("+itoa(l.selectedIndex+1)+"/"+itoa(len(l.messages))+")"))
	}
	return lines
}

// HandleInput processes navigation and selection.
func (l *UserMessageList) HandleInput(data string) {
	kb := tui.GetKeybindings()
	switch {
	case kb.Matches(data, "tui.select.up"):
		if l.selectedIndex == 0 {
			l.selectedIndex = len(l.messages) - 1
		} else {
			l.selectedIndex--
		}
	case kb.Matches(data, "tui.select.down"):
		if l.selectedIndex == len(l.messages)-1 {
			l.selectedIndex = 0
		} else {
			l.selectedIndex++
		}
	case kb.Matches(data, "tui.select.confirm"):
		if l.selectedIndex >= 0 && l.selectedIndex < len(l.messages) && l.OnSelect != nil {
			l.OnSelect(l.messages[l.selectedIndex].ID)
		}
	case kb.Matches(data, "tui.select.cancel"):
		if l.OnCancel != nil {
			l.OnCancel()
		}
	}
}

// SelectedIndex returns the selected index.
func (l *UserMessageList) SelectedIndex() int { return l.selectedIndex }

// UserMessageSelectorComponent renders the fork-from-message selector.
type UserMessageSelectorComponent struct {
	*tui.Container

	messageList *UserMessageList
}

// NewUserMessageSelectorComponent creates the selector.
func NewUserMessageSelectorComponent(messages []UserMessageItem, onSelect func(entryID string), onCancel func(), initialSelectedID string) *UserMessageSelectorComponent {
	theme := ActiveTheme()
	component := &UserMessageSelectorComponent{Container: &tui.Container{}}
	component.AddChild(tui.NewSpacer(1))
	component.AddChild(tui.NewText(theme.Bold("Fork from Message"), 1, 0, nil))
	component.AddChild(tui.NewText(theme.Fg("muted",
		"Select a user message to copy the active path up to that point into a new session"), 1, 0, nil))
	component.AddChild(tui.NewSpacer(1))
	component.AddChild(NewDynamicBorder(nil))
	component.AddChild(tui.NewSpacer(1))

	component.messageList = NewUserMessageList(messages, initialSelectedID)
	component.messageList.OnSelect = onSelect
	component.messageList.OnCancel = onCancel
	component.AddChild(component.messageList)

	component.AddChild(tui.NewSpacer(1))
	component.AddChild(NewDynamicBorder(nil))

	if len(messages) == 0 {
		go func() {
			// Auto-cancel shortly after mounting (upstream's 100ms timer).
			delayMillis(100)
			if onCancel != nil {
				onCancel()
			}
		}()
	}
	return component
}

// GetMessageList returns the message list.
func (c *UserMessageSelectorComponent) GetMessageList() *UserMessageList { return c.messageList }

// TrustSelection is the selected trust decision.
type TrustSelection struct {
	Trusted bool
	Updates []coding.ProjectTrustUpdate
}

// TrustSelectorOptions configure the trust selector.
type TrustSelectorOptions struct {
	Cwd                string
	SavedDecision      *coding.ProjectTrustStoreEntry
	ProjectTrusted     bool
	OnSelect           func(selection TrustSelection)
	OnCancel           func()
	IncludeSessionOnly bool
}

// TrustSelectorComponent renders the project trust selector.
type TrustSelectorComponent struct {
	*tui.Container

	selectedIndex int
	listContainer *tui.Container
	trustOptions  []coding.ProjectTrustOption
	savedDecision *coding.ProjectTrustStoreEntry
	onSelect      func(TrustSelection)
	onCancel      func()
}

// NewTrustSelectorComponent creates the selector.
func NewTrustSelectorComponent(options TrustSelectorOptions) *TrustSelectorComponent {
	theme := ActiveTheme()
	component := &TrustSelectorComponent{
		Container:     &tui.Container{},
		savedDecision: options.SavedDecision,
		trustOptions:  coding.GetProjectTrustOptions(options.Cwd, options.IncludeSessionOnly),
		onSelect:      options.OnSelect,
		onCancel:      options.OnCancel,
	}
	for index, option := range component.trustOptions {
		if component.isSavedOption(option) {
			component.selectedIndex = index
			break
		}
	}

	component.AddChild(NewDynamicBorder(nil))
	component.AddChild(tui.NewSpacer(1))
	component.AddChild(tui.NewText(theme.Fg("accent", theme.Bold("Project trust")), 1, 0, nil))
	component.AddChild(tui.NewText(theme.Fg("muted", options.Cwd), 1, 0, nil))
	component.AddChild(tui.NewSpacer(1))

	savedPath := ""
	if len(component.trustOptions) > 0 {
		savedPath = component.trustOptions[0].SavedPath
	}
	component.AddChild(tui.NewText(theme.Fg("muted", "Saved decision: "+formatTrustDecision(savedPath, options.SavedDecision)), 1, 0, nil))
	current := "untrusted"
	if options.ProjectTrusted {
		current = "trusted"
	}
	component.AddChild(tui.NewText(theme.Fg("muted", "Current session: "+current), 1, 0, nil))
	component.AddChild(tui.NewSpacer(1))

	component.listContainer = &tui.Container{}
	component.AddChild(component.listContainer)
	component.AddChild(tui.NewSpacer(1))
	component.AddChild(tui.NewText(RawKeyHint("↑↓", "navigate")+"  "+
		KeyHint("tui.select.confirm", "save")+"  "+KeyHint("tui.select.cancel", "cancel"), 1, 0, nil))
	component.AddChild(tui.NewSpacer(1))
	component.AddChild(NewDynamicBorder(nil))

	component.updateList()
	return component
}

func (c *TrustSelectorComponent) isSavedOption(option coding.ProjectTrustOption) bool {
	return option.SavedPath != "" && c.savedDecision != nil &&
		c.savedDecision.Decision == option.Trusted && c.savedDecision.Path == option.SavedPath
}

func (c *TrustSelectorComponent) updateList() {
	theme := ActiveTheme()
	c.listContainer.Clear()
	for index, option := range c.trustOptions {
		isSelected := index == c.selectedIndex
		isCurrent := c.isSavedOption(option)
		currentMarker := "  "
		if isCurrent {
			currentMarker = theme.Fg("accent", "✓ ")
		}
		prefix := "  "
		if isSelected {
			prefix = theme.Fg("accent", "→ ")
		}
		label := theme.Fg("text", option.Label)
		if isSelected {
			label = theme.Fg("accent", option.Label)
		}
		c.listContainer.AddChild(tui.NewText(prefix+currentMarker+label, 1, 0, nil))
	}
}

// HandleInput processes navigation and selection.
func (c *TrustSelectorComponent) HandleInput(data string) {
	kb := tui.GetKeybindings()
	switch {
	case kb.Matches(data, "tui.select.up") || data == "k":
		c.selectedIndex = maxIntLocal(0, c.selectedIndex-1)
		c.updateList()
	case kb.Matches(data, "tui.select.down") || data == "j":
		c.selectedIndex = minIntLocal(len(c.trustOptions)-1, c.selectedIndex+1)
		c.updateList()
	case kb.Matches(data, "tui.select.confirm") || data == "\n":
		if c.selectedIndex >= 0 && c.selectedIndex < len(c.trustOptions) && c.onSelect != nil {
			option := c.trustOptions[c.selectedIndex]
			c.onSelect(TrustSelection{Trusted: option.Trusted, Updates: option.Updates})
		}
	case kb.Matches(data, "tui.select.cancel"):
		if c.onCancel != nil {
			c.onCancel()
		}
	}
}

func formatTrustDecision(trustPath string, decision *coding.ProjectTrustStoreEntry) string {
	if decision == nil {
		return "none"
	}
	label := "untrusted"
	if decision.Decision {
		label = "trusted"
	}
	if trustPath != "" && decision.Path != trustPath {
		return label + " (inherited from " + decision.Path + ")"
	}
	return label + " (" + decision.Path + ")"
}

// MessageRenderer renders a custom message (extension surface).
type MessageRenderer func(message CustomMessagePayload, options MessageRenderOptions, theme *Theme) tui.Component

// MessageRenderOptions configure the custom message rendering.
type MessageRenderOptions struct {
	Expanded  bool
	OutputPad int
}

// CustomMessagePayload is the custom message content.
type CustomMessagePayload struct {
	CustomType string
	// Content is either plain text or a list of text blocks.
	Text   string
	Blocks []string
}

// CustomMessageComponent renders a custom session message.
type CustomMessageComponent struct {
	*tui.Container

	message        CustomMessagePayload
	customRenderer MessageRenderer
	box            *tui.Box
	custom         tui.Component
	markdownTheme  tui.MarkdownTheme
	expanded       bool
	outputPad      int
}

// NewCustomMessageComponent creates the component.
func NewCustomMessageComponent(message CustomMessagePayload, customRenderer MessageRenderer, markdownTheme *tui.MarkdownTheme, outputPad int) *CustomMessageComponent {
	theme := ActiveTheme()
	component := &CustomMessageComponent{
		Container:      &tui.Container{},
		message:        message,
		customRenderer: customRenderer,
		markdownTheme:  markdownThemeValue(markdownTheme),
		outputPad:      outputPad,
	}
	component.AddChild(tui.NewSpacer(1))
	component.box = tui.NewBox(1, 1, func(text string) string { return theme.Bg("customMessageBg", text) })
	component.rebuild()
	return component
}

// SetExpanded toggles the expanded state.
func (c *CustomMessageComponent) SetExpanded(expanded bool) {
	if c.expanded != expanded {
		c.expanded = expanded
		c.rebuild()
	}
}

// SetOutputPad updates the horizontal padding.
func (c *CustomMessageComponent) SetOutputPad(pad int) {
	if c.outputPad != pad {
		c.outputPad = pad
		c.rebuild()
	}
}

// Invalidate rebuilds the content.
func (c *CustomMessageComponent) Invalidate() {
	c.Container.Invalidate()
	c.rebuild()
}

func (c *CustomMessageComponent) rebuild() {
	theme := ActiveTheme()
	if c.custom != nil {
		c.Container.RemoveChild(c.custom)
		c.custom = nil
	}
	c.Container.RemoveChild(c.box)

	if c.customRenderer != nil {
		component := safeMessageRender(c.customRenderer, c.message,
			MessageRenderOptions{Expanded: c.expanded, OutputPad: c.outputPad}, theme)
		if component != nil {
			c.custom = component
			c.Container.AddChild(component)
			return
		}
	}

	c.Container.AddChild(c.box)
	c.box.Clear()

	label := theme.Fg("customMessageLabel", "\x1b[1m["+c.message.CustomType+"]\x1b[22m")
	c.box.AddChild(tui.NewText(label, 0, 0, nil))
	c.box.AddChild(tui.NewSpacer(1))

	text := c.message.Text
	if text == "" && len(c.message.Blocks) > 0 {
		text = strings.Join(c.message.Blocks, "\n")
	}
	c.box.AddChild(tui.NewMarkdown(text, 0, 0, c.markdownTheme,
		&tui.DefaultTextStyle{Color: func(value string) string { return theme.Fg("customMessageText", value) }},
		tui.MarkdownOptions{}))
}

func safeMessageRender(renderer MessageRenderer, message CustomMessagePayload, options MessageRenderOptions, theme *Theme) (component tui.Component) {
	defer func() {
		if recover() != nil {
			component = nil
		}
	}()
	return renderer(message, options, theme)
}

// delayMillis sleeps for the given duration (the upstream setTimeout seam).
func delayMillis(ms int) {
	timerSleep(ms)
}

// timerSleep sleeps for the given number of milliseconds.
func timerSleep(ms int) {
	if ms <= 0 {
		return
	}
	time.Sleep(time.Duration(ms) * time.Millisecond)
}
