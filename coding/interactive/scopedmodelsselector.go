package interactive

import (
	"strings"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of src/modes/interactive/components/scoped-models-selector.ts.

// EnabledIds is nil for "all enabled", or an explicit ordered list.
type EnabledIds struct {
	All bool
	IDs []string
}

func enabledIdsContains(enabled EnabledIds, id string) bool {
	if enabled.All {
		return true
	}
	for _, candidate := range enabled.IDs {
		if candidate == id {
			return true
		}
	}
	return false
}

func cloneEnabled(enabled EnabledIds) EnabledIds {
	if enabled.All {
		return EnabledIds{All: true}
	}
	return EnabledIds{IDs: append([]string(nil), enabled.IDs...)}
}

func normalizeEnabled(result []string, allIDs []string) EnabledIds {
	if len(result) == len(allIDs) {
		covered := true
		for _, id := range result {
			found := false
			for _, allID := range allIDs {
				if id == allID {
					found = true
					break
				}
			}
			if !found {
				covered = false
				break
			}
		}
		if covered {
			return EnabledIds{All: true}
		}
	}
	return EnabledIds{IDs: result}
}

func toggleEnabled(enabled EnabledIds, allIDs []string, id string) EnabledIds {
	if enabled.All {
		result := make([]string, 0, len(allIDs))
		for _, modelID := range allIDs {
			if modelID != id {
				result = append(result, modelID)
			}
		}
		return EnabledIds{IDs: result}
	}
	for index, candidate := range enabled.IDs {
		if candidate == id {
			result := append([]string(nil), enabled.IDs[:index]...)
			result = append(result, enabled.IDs[index+1:]...)
			return EnabledIds{IDs: result}
		}
	}
	return normalizeEnabled(append(append([]string(nil), enabled.IDs...), id), allIDs)
}

func enableAllModels(enabled EnabledIds, allIDs []string, targetIDs []string, hasTargets bool) EnabledIds {
	if enabled.All {
		return EnabledIds{All: true}
	}
	targets := allIDs
	if hasTargets {
		targets = targetIDs
	}
	result := append([]string(nil), enabled.IDs...)
	for _, id := range targets {
		found := false
		for _, candidate := range result {
			if candidate == id {
				found = true
				break
			}
		}
		if !found {
			result = append(result, id)
		}
	}
	return normalizeEnabled(result, allIDs)
}

func clearAllModels(enabled EnabledIds, allIDs []string, targetIDs []string, hasTargets bool) EnabledIds {
	if enabled.All {
		if hasTargets {
			result := make([]string, 0, len(allIDs))
			for _, id := range allIDs {
				excluded := false
				for _, target := range targetIDs {
					if target == id {
						excluded = true
						break
					}
				}
				if !excluded {
					result = append(result, id)
				}
			}
			return EnabledIds{IDs: result}
		}
		return EnabledIds{IDs: []string{}}
	}
	targets := enabled.IDs
	if hasTargets {
		targets = targetIDs
	}
	excluded := map[string]bool{}
	for _, id := range targets {
		excluded[id] = true
	}
	result := make([]string, 0, len(enabled.IDs))
	for _, id := range enabled.IDs {
		if !excluded[id] {
			result = append(result, id)
		}
	}
	return EnabledIds{IDs: result}
}

func moveEnabled(enabled EnabledIds, id string, delta int) EnabledIds {
	if enabled.All {
		return EnabledIds{All: true}
	}
	list := append([]string(nil), enabled.IDs...)
	index := -1
	for i, candidate := range list {
		if candidate == id {
			index = i
			break
		}
	}
	if index < 0 {
		return EnabledIds{IDs: list}
	}
	newIndex := index + delta
	if newIndex < 0 || newIndex >= len(list) {
		return EnabledIds{IDs: list}
	}
	list[index], list[newIndex] = list[newIndex], list[index]
	return EnabledIds{IDs: list}
}

func getSortedIDs(enabled EnabledIds, allIDs []string) []string {
	if enabled.All {
		return allIDs
	}
	enabledSet := map[string]bool{}
	for _, id := range enabled.IDs {
		enabledSet[id] = true
	}
	result := append([]string(nil), enabled.IDs...)
	for _, id := range allIDs {
		if !enabledSet[id] {
			result = append(result, id)
		}
	}
	return result
}

type scopedModelItem struct {
	fullID  string
	model   *ai.Model
	enabled bool
}

// ModelsConfig configures the scoped models selector.
type ModelsConfig struct {
	AllModels       []*ai.Model
	EnabledModelIDs *EnabledIds
	RefreshStatus   string
}

// ModelsCallbacks are the selector callbacks.
type ModelsCallbacks struct {
	OnChange  func(enabled EnabledIds)
	OnPersist func(enabled EnabledIds)
	OnCancel  func()
}

// ChangeEnabled invokes the change callback (test seam).
func (c *ScopedModelsSelectorComponent) ChangeEnabled(enabled EnabledIds) {
	callback := c.callbacks.OnChange
	if callback != nil {
		callback(enabled)
	}
}

// PersistEnabled invokes the persist callback (test seam).
func (c *ScopedModelsSelectorComponent) PersistEnabled(enabled EnabledIds) {
	callback := c.callbacks.OnPersist
	if callback != nil {
		callback(enabled)
	}
}

// RefreshStatus returns the current refresh status text (test seam).
func (c *ScopedModelsSelectorComponent) RefreshStatus() string {
	if c.refreshStatus == nil {
		return ""
	}
	lines := c.refreshStatus.Render(200)
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(coding.StripAnsi(lines[0]))
}

// ScopedModelsSelectorComponent enables/disables models for cycling.
type ScopedModelsSelectorComponent struct {
	*tui.Container

	modelsByID    map[string]*ai.Model
	allIDs        []string
	enabledIDs    EnabledIds
	filteredItems []scopedModelItem
	selectedIndex int
	searchInput   *tui.Input
	focused       bool
	listContainer *tui.Container
	footerText    *tui.Text
	callbacks     ModelsCallbacks
	maxVisible    int
	isDirty       bool
	refreshStatus *tui.Text

	// mu serializes rendering/input against the background catalog refresh
	// (the Go port has no single-threaded event loop: D119/D96).
}

// NewScopedModelsSelectorComponent creates the selector.
func NewScopedModelsSelectorComponent(config ModelsConfig, callbacks ModelsCallbacks) *ScopedModelsSelectorComponent {
	theme := ActiveTheme()
	component := &ScopedModelsSelectorComponent{
		Container:  &tui.Container{},
		modelsByID: map[string]*ai.Model{},
		callbacks:  callbacks,
		maxVisible: 8,
		enabledIDs: EnabledIds{All: true},
	}
	for _, model := range config.AllModels {
		fullID := string(model.Provider) + "/" + model.ID
		component.modelsByID[fullID] = model
		component.allIDs = append(component.allIDs, fullID)
	}
	if config.EnabledModelIDs != nil {
		component.enabledIDs = cloneEnabled(*config.EnabledModelIDs)
	}
	component.filteredItems = component.buildItems()

	component.AddChild(NewDynamicBorder(nil))
	component.AddChild(tui.NewSpacer(1))
	component.AddChild(tui.NewText(theme.Fg("accent", theme.Bold("Model Configuration")), 0, 0, nil))
	component.AddChild(tui.NewText(theme.Fg("muted",
		"Session-only. "+KeyDisplayText("app.models.save")+" to save to settings."), 0, 0, nil))
	component.AddChild(tui.NewSpacer(1))

	component.searchInput = tui.NewInput(tui.InputOptions{})
	component.AddChild(component.searchInput)
	component.AddChild(tui.NewSpacer(1))

	component.listContainer = &tui.Container{}
	component.AddChild(component.listContainer)
	component.AddChild(tui.NewSpacer(1))
	if config.RefreshStatus != "" {
		component.refreshStatus = tui.NewText(theme.Fg("muted", "  "+config.RefreshStatus), 0, 0, nil)
		component.AddChild(component.refreshStatus)
	}
	component.footerText = tui.NewText(component.getFooterText(), 0, 0, nil)
	component.AddChild(component.footerText)
	component.AddChild(NewDynamicBorder(nil))
	component.updateList()
	return component
}

// Render renders the selector.
func (c *ScopedModelsSelectorComponent) Render(width int) []string {
	return c.Container.Render(width)
}

// UpdateModels replaces the model list and optionally the enabled set.
func (c *ScopedModelsSelectorComponent) UpdateModels(models []*ai.Model, enabledModelIDs *EnabledIds) {
	c.updateModels(models, enabledModelIDs)
}

func (c *ScopedModelsSelectorComponent) updateModels(models []*ai.Model, enabledModelIDs *EnabledIds) {
	selectedID := ""
	if c.selectedIndex >= 0 && c.selectedIndex < len(c.filteredItems) {
		selectedID = c.filteredItems[c.selectedIndex].fullID
	}
	if enabledModelIDs != nil {
		c.enabledIDs = cloneEnabled(*enabledModelIDs)
	}
	c.modelsByID = map[string]*ai.Model{}
	c.allIDs = nil
	for _, model := range models {
		fullID := string(model.Provider) + "/" + model.ID
		c.modelsByID[fullID] = model
		c.allIDs = append(c.allIDs, fullID)
	}
	c.refreshList()
	if selectedID != "" {
		for index, item := range c.filteredItems {
			if item.fullID == selectedID {
				c.selectedIndex = index
				c.updateList()
				break
			}
		}
	}
}

// SetRefreshStatus updates the refresh status line.
func (c *ScopedModelsSelectorComponent) SetRefreshStatus(message string, kind string) {
	if c.refreshStatus == nil {
		return
	}
	c.refreshStatus.SetText(ActiveTheme().Fg(kind, "  "+message))
}

func (c *ScopedModelsSelectorComponent) buildItems() []scopedModelItem {
	ids := getSortedIDs(c.enabledIDs, c.allIDs)
	items := make([]scopedModelItem, 0, len(ids))
	for _, id := range ids {
		items = append(items, scopedModelItem{
			fullID:  id,
			model:   c.modelsByID[id],
			enabled: enabledIdsContains(c.enabledIDs, id),
		})
	}
	return items
}

func (c *ScopedModelsSelectorComponent) getFooterText() string {
	theme := ActiveTheme()
	enabledCount := len(c.allIDs)
	unavailableCount := 0
	if !c.enabledIDs.All {
		enabledCount = 0
		for _, id := range c.enabledIDs.IDs {
			if _, ok := c.modelsByID[id]; ok {
				enabledCount++
			} else {
				unavailableCount++
			}
		}
	}
	countText := "all enabled"
	if !c.enabledIDs.All {
		countText = itoa(enabledCount) + "/" + itoa(len(c.allIDs)) + " enabled"
		if unavailableCount > 0 {
			countText += " · " + itoa(unavailableCount) + " unavailable"
		}
	}
	parts := []string{
		KeyDisplayText("tui.select.confirm") + " toggle",
		KeyDisplayText("app.models.enableAll") + " all",
		KeyDisplayText("app.models.clearAll") + " clear",
		KeyDisplayText("app.models.toggleProvider") + " provider",
		KeyDisplayText("app.models.reorderUp") + "/" + KeyDisplayText("app.models.reorderDown") + " reorder",
		KeyDisplayText("app.models.save") + " save",
		countText,
	}
	joined := "  " + strings.Join(parts, " · ")
	if c.isDirty {
		return theme.Fg("dim", joined+" ") + theme.Fg("warning", "(unsaved)")
	}
	return theme.Fg("dim", joined)
}

func (c *ScopedModelsSelectorComponent) refreshList() {
	query := c.searchInput.Value()
	items := c.buildItems()
	if query != "" {
		c.filteredItems = tui.FuzzyFilter(items, query, func(item scopedModelItem) string {
			if item.model != nil {
				return GetModelSearchText(ModelSearchItem{
					ID: item.model.ID, Provider: string(item.model.Provider),
					Name: item.model.Name, HasName: item.model.Name != "",
				})
			}
			return item.fullID
		})
	} else {
		c.filteredItems = items
	}
	c.selectedIndex = min(c.selectedIndex, max(0, len(c.filteredItems)-1))
	c.updateList()
	c.footerText.SetText(c.getFooterText())
}

func (c *ScopedModelsSelectorComponent) notifyChange() {
	if c.callbacks.OnChange == nil {
		return
	}
	if c.enabledIDs.All {
		c.callbacks.OnChange(EnabledIds{All: true})
		return
	}
	c.callbacks.OnChange(EnabledIds{IDs: append([]string(nil), c.enabledIDs.IDs...)})
}

func (c *ScopedModelsSelectorComponent) updateList() {
	theme := ActiveTheme()
	c.listContainer.Clear()

	if len(c.filteredItems) == 0 {
		c.listContainer.AddChild(tui.NewText(theme.Fg("muted", "  No matching models"), 0, 0, nil))
		return
	}

	startIndex := max(0, min(c.selectedIndex-c.maxVisible/2, len(c.filteredItems)-c.maxVisible))
	endIndex := min(startIndex+c.maxVisible, len(c.filteredItems))
	for i := startIndex; i < endIndex; i++ {
		item := c.filteredItems[i]
		isSelected := i == c.selectedIndex
		prefix := "  "
		if isSelected {
			prefix = theme.Fg("accent", "→ ")
		}
		id := item.fullID
		if item.model != nil {
			id = item.model.ID
		}
		styledID := id
		if item.model == nil {
			styledID = theme.Strikethrough(id)
		}
		modelText := styledID
		if isSelected {
			modelText = theme.Fg("accent", styledID)
		}
		providerBadge := theme.Fg("muted", " [unavailable]")
		if item.model != nil {
			providerBadge = theme.Fg("muted", " ["+string(item.model.Provider)+"]")
		}
		status := "  "
		if item.model != nil && item.enabled {
			status = theme.Fg("accent", "✓ ")
		}
		c.listContainer.AddChild(tui.NewText(prefix+status+modelText+providerBadge, 0, 0, nil))
	}

	if startIndex > 0 || endIndex < len(c.filteredItems) {
		c.listContainer.AddChild(tui.NewText(theme.Fg("muted",
			"  ("+itoa(c.selectedIndex+1)+"/"+itoa(len(c.filteredItems))+")"), 0, 0, nil))
	}

	if len(c.filteredItems) > 0 {
		selected := c.filteredItems[c.selectedIndex]
		detail := "Model unavailable"
		if selected.model != nil {
			detail = "Model Name: " + selected.model.Name
		}
		c.listContainer.AddChild(tui.NewSpacer(1))
		c.listContainer.AddChild(tui.NewText(theme.Fg("muted", "  "+detail), 0, 0, nil))
	}
}

// HandleInput processes input.
func (c *ScopedModelsSelectorComponent) HandleInput(data string) {
	c.handleInput(data)
}

func (c *ScopedModelsSelectorComponent) handleInput(data string) {
	kb := tui.GetKeybindings()

	switch {
	case kb.Matches(data, "tui.select.up"):
		if len(c.filteredItems) == 0 {
			return
		}
		if c.selectedIndex == 0 {
			c.selectedIndex = len(c.filteredItems) - 1
		} else {
			c.selectedIndex--
		}
		c.updateList()
		return
	case kb.Matches(data, "tui.select.down"):
		if len(c.filteredItems) == 0 {
			return
		}
		if c.selectedIndex == len(c.filteredItems)-1 {
			c.selectedIndex = 0
		} else {
			c.selectedIndex++
		}
		c.updateList()
		return
	case kb.Matches(data, "app.models.reorderUp") || kb.Matches(data, "app.models.reorderDown"):
		if c.enabledIDs.All {
			return
		}
		if c.selectedIndex < 0 || c.selectedIndex >= len(c.filteredItems) {
			return
		}
		item := c.filteredItems[c.selectedIndex]
		if !enabledIdsContains(c.enabledIDs, item.fullID) {
			return
		}
		delta := 1
		if kb.Matches(data, "app.models.reorderUp") {
			delta = -1
		}
		currentIndex := -1
		for index, id := range c.enabledIDs.IDs {
			if id == item.fullID {
				currentIndex = index
				break
			}
		}
		newIndex := currentIndex + delta
		if newIndex >= 0 && newIndex < len(c.enabledIDs.IDs) {
			c.enabledIDs = moveEnabled(c.enabledIDs, item.fullID, delta)
			c.isDirty = true
			c.selectedIndex += delta
			c.refreshList()
			c.notifyChange()
		}
		return
	case kb.Matches(data, "tui.select.confirm"):
		if c.selectedIndex >= 0 && c.selectedIndex < len(c.filteredItems) {
			c.enabledIDs = toggleEnabled(c.enabledIDs, c.allIDs, c.filteredItems[c.selectedIndex].fullID)
			c.isDirty = true
			c.refreshList()
			c.notifyChange()
		}
		return
	case kb.Matches(data, "app.models.enableAll"):
		targetIDs, hasTargets := c.filteredTargets()
		c.enabledIDs = enableAllModels(c.enabledIDs, c.allIDs, targetIDs, hasTargets)
		c.isDirty = true
		c.refreshList()
		c.notifyChange()
		return
	case kb.Matches(data, "app.models.clearAll"):
		targetIDs, hasTargets := c.filteredTargets()
		c.enabledIDs = clearAllModels(c.enabledIDs, c.allIDs, targetIDs, hasTargets)
		c.isDirty = true
		c.refreshList()
		c.notifyChange()
		return
	case kb.Matches(data, "app.models.toggleProvider"):
		if c.selectedIndex >= 0 && c.selectedIndex < len(c.filteredItems) && c.filteredItems[c.selectedIndex].model != nil {
			provider := string(c.filteredItems[c.selectedIndex].model.Provider)
			var providerIDs []string
			for _, id := range c.allIDs {
				if model := c.modelsByID[id]; model != nil && string(model.Provider) == provider {
					providerIDs = append(providerIDs, id)
				}
			}
			allEnabled := true
			for _, id := range providerIDs {
				if !enabledIdsContains(c.enabledIDs, id) {
					allEnabled = false
					break
				}
			}
			if allEnabled {
				c.enabledIDs = clearAllModels(c.enabledIDs, c.allIDs, providerIDs, true)
			} else {
				c.enabledIDs = enableAllModels(c.enabledIDs, c.allIDs, providerIDs, true)
			}
			c.isDirty = true
			c.refreshList()
			c.notifyChange()
		}
		return
	case kb.Matches(data, "app.models.save"):
		if c.callbacks.OnPersist != nil {
			if c.enabledIDs.All {
				c.callbacks.OnPersist(EnabledIds{All: true})
			} else {
				c.callbacks.OnPersist(EnabledIds{IDs: append([]string(nil), c.enabledIDs.IDs...)})
			}
		}
		c.isDirty = false
		c.footerText.SetText(c.getFooterText())
		return
	case tui.MatchesKey(data, "ctrl+c"):
		if c.searchInput.Value() != "" {
			c.searchInput.SetValue("")
			c.refreshList()
		} else if c.callbacks.OnCancel != nil {
			c.callbacks.OnCancel()
		}
		return
	case tui.MatchesKey(data, "escape"):
		if c.callbacks.OnCancel != nil {
			c.callbacks.OnCancel()
		}
		return
	}

	c.searchInput.HandleInput(data)
	c.refreshList()
}

func (c *ScopedModelsSelectorComponent) filteredTargets() ([]string, bool) {
	if c.searchInput.Value() == "" {
		return nil, false
	}
	ids := make([]string, 0, len(c.filteredItems))
	for _, item := range c.filteredItems {
		ids = append(ids, item.fullID)
	}
	return ids, true
}

// GetSearchInput returns the search input.
func (c *ScopedModelsSelectorComponent) GetSearchInput() *tui.Input { return c.searchInput }

// EnabledIDs returns the current enabled set.
func (c *ScopedModelsSelectorComponent) EnabledIDs() EnabledIds {
	return cloneEnabled(c.enabledIDs)
}

// SetFocused implements Focusable.
func (c *ScopedModelsSelectorComponent) SetFocused(focused bool) {
	c.focused = focused
	c.searchInput.SetFocused(focused)
}

// IsFocused implements Focusable.
func (c *ScopedModelsSelectorComponent) IsFocused() bool { return c.focused }

var (
	_ tui.Component = (*ScopedModelsSelectorComponent)(nil)
	_ tui.Focusable = (*ScopedModelsSelectorComponent)(nil)
)
