package interactive

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of src/modes/interactive/components/model-selector.ts and
// scoped-models-selector.ts.

// ModelSelectorRuntime is the model runtime surface the selector needs
// (upstream passes the concrete ModelRuntime; the interface keeps the
// component testable: D90).
type ModelSelectorRuntime interface {
	GetAvailableSnapshot() []*ai.Model
	GetModel(providerID string, modelID string) *ai.Model
	GetError() string
	Refresh(ctx context.Context, options *coding.ModelsRefreshCallOptions) (ai.ModelsRefreshResult, error)
}

// ScopedModelItem is a scoped model with an optional thinking level.
type ScopedModelItem struct {
	Model         *ai.Model
	ThinkingLevel string
}

// DefaultModelReference identifies the default model.
type DefaultModelReference struct {
	Provider string
	ID       string
}

type modelItem struct {
	provider string
	id       string
	model    *ai.Model
}

type modelScopeKind string

const (
	modelScopeAll    modelScopeKind = "all"
	modelScopeScoped modelScopeKind = "scoped"
)

// ModelSelectorComponent renders the model selector with search.
type ModelSelectorComponent struct {
	*tui.Container

	// mu serializes rendering, input handling, and the background refresh
	// application (the Go port has no single-threaded event loop: D84/D96).

	searchInput *tui.Input
	focused     bool

	listContainer  *tui.Container
	allModels      []modelItem
	scopedItems    []modelItem
	activeModels   []modelItem
	filteredModels []modelItem
	selectedIndex  int

	currentModel      *ai.Model
	runtime           ModelSelectorRuntime
	onSelect          func(model *ai.Model)
	onSelectAsDefault func(model *ai.Model)
	onCancel          func()
	host              tui.RenderRequester
	// Post marshals a background result onto the UI loop. Nil runs inline
	// (tests / no loop). The refresh worker must not mutate selector state.
	Post func(fn func())

	errorMessage         string
	refreshStatusMessage string
	refreshStatusSuccess bool

	scopedModels  []ScopedModelItem
	defaultModel  *DefaultModelReference
	scope         modelScopeKind
	scopeText     *tui.Text
	scopeHintText *tui.Text

	closed bool
	cancel context.CancelFunc
}

// NewModelSelectorComponent creates the selector.
func NewModelSelectorComponent(host tui.RenderRequester, post func(fn func()), currentModel *ai.Model, runtime ModelSelectorRuntime, scopedModels []ScopedModelItem, onSelect func(*ai.Model), onCancel func(), initialSearchInput string, onSelectAsDefault func(*ai.Model), defaultModel *DefaultModelReference) *ModelSelectorComponent {
	component := &ModelSelectorComponent{
		Container:            &tui.Container{},
		host:                 host,
		Post:                 post,
		currentModel:         currentModel,
		runtime:              runtime,
		scopedModels:         scopedModels,
		onSelect:             onSelect,
		onCancel:             onCancel,
		onSelectAsDefault:    onSelectAsDefault,
		defaultModel:         defaultModel,
		scope:                modelScopeAll,
		refreshStatusMessage: "Refreshing model catalogs…",
	}
	if len(scopedModels) > 0 {
		component.scope = modelScopeScoped
	}

	component.AddChild(NewDynamicBorder(nil))
	component.AddChild(tui.NewSpacer(1))

	if len(scopedModels) > 0 {
		component.scopeText = tui.NewText(component.getScopeText(), 0, 0, nil)
		component.AddChild(component.scopeText)
		component.scopeHintText = tui.NewText(component.getScopeHintText(), 0, 0, nil)
		component.AddChild(component.scopeHintText)
	} else {
		component.AddChild(tui.NewText(ActiveTheme().Fg("warning",
			"Only showing models from configured providers. Use /login to add providers."), 0, 0, nil))
	}
	component.AddChild(tui.NewSpacer(1))

	component.searchInput = tui.NewInput(tui.InputOptions{})
	if initialSearchInput != "" {
		component.searchInput.SetValue(initialSearchInput)
	}
	component.searchInput.OnSubmit = func(string) {
		if component.selectedIndex >= 0 && component.selectedIndex < len(component.filteredModels) {
			component.selectModel(component.filteredModels[component.selectedIndex].model)
		}
	}
	component.AddChild(component.searchInput)
	component.AddChild(tui.NewSpacer(1))

	component.listContainer = &tui.Container{}
	component.AddChild(component.listContainer)
	component.AddChild(tui.NewSpacer(1))

	if component.onSelectAsDefault != nil {
		component.AddChild(tui.NewText(ActiveTheme().Fg("dim",
			"  "+KeyDisplayText("tui.select.confirm")+" to select · "+KeyDisplayText("app.models.save")+
				" to set as default · "+KeyDisplayText("tui.select.cancel")+" to cancel"), 0, 0, nil))
	}
	component.AddChild(NewDynamicBorder(nil))

	// Render the current snapshot immediately, then refresh in the background.
	component.loadModelsFromSnapshot()
	if initialSearchInput != "" {
		component.filterModels(initialSearchInput)
	} else {
		component.updateList()
	}
	if component.host != nil {
		component.host.RequestRender(false)
	}
	component.startRefresh()
	return component
}

func (c *ModelSelectorComponent) loadModelsFromSnapshot() {
	var items []modelItem
	for _, model := range c.runtime.GetAvailableSnapshot() {
		items = append(items, modelItem{provider: string(model.Provider), id: model.ID, model: model})
	}
	c.allModels = c.sortModels(items)

	for index, scoped := range c.scopedModels {
		if refreshed := c.runtime.GetModel(string(scoped.Model.Provider), scoped.Model.ID); refreshed != nil {
			c.scopedModels[index].Model = refreshed
		}
	}
	c.scopedItems = make([]modelItem, 0, len(c.scopedModels))
	for _, scoped := range c.scopedModels {
		c.scopedItems = append(c.scopedItems, modelItem{
			provider: string(scoped.Model.Provider), id: scoped.Model.ID, model: scoped.Model,
		})
	}
	c.activeModels = c.allModels
	if c.scope == modelScopeScoped {
		c.activeModels = c.scopedItems
	}
	c.filteredModels = c.activeModels

	currentIndex := -1
	for index, item := range c.filteredModels {
		if ai.ModelsAreEqual(c.currentModel, item.model) {
			currentIndex = index
			break
		}
	}
	if currentIndex >= 0 {
		c.selectedIndex = currentIndex
	} else {
		c.selectedIndex = minIntLocal(c.selectedIndex, maxIntLocal(0, len(c.filteredModels)-1))
	}
}

// startRefresh refreshes the catalogs in the background.
func (c *ModelSelectorComponent) startRefresh() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	c.cancel = cancel
	go func() {
		defer cancel()
		result, err := RefreshModelCatalogs(ctx, c.runtime)
		// The refresh worker is a pure producer: it hands the outcome to the
		// loop, which owns the selector state (stage 4).
		c.postApply(func() {
			if c.closed {
				return
			}
			c.refreshStatusMessage = ""
			switch {
			case err != nil:
				c.refreshStatusMessage = ""
				if ctx.Err() != nil {
					c.errorMessage = "Model refresh timed out; showing cached models."
				} else {
					c.errorMessage = "Could not refresh model catalogs: " + err.Error()
				}
			case result.Aborted && ctx.Err() != nil:
				c.errorMessage = "Model refresh timed out; showing cached models."
			case len(result.Errors) == 1:
				key := firstErrorKey(result.Errors)
				c.errorMessage = "Could not refresh " + key + "; showing cached models."
			case len(result.Errors) > 1:
				keys := make([]string, 0, len(result.Errors))
				for key := range result.Errors {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				c.errorMessage = "Could not refresh " + itoa(len(result.Errors)) + " model catalogs (" +
					strings.Join(keys, ", ") + "); showing cached models."
			default:
				c.errorMessage = c.runtime.GetError()
				if c.errorMessage == "" {
					c.refreshStatusMessage = "Model catalogs refreshed."
					c.refreshStatusSuccess = true
				}
			}

			c.loadModelsFromSnapshot()
			c.filterModels(c.searchInput.Value())
			c.updateList()
			if c.host != nil {
				c.host.RequestRender(false)
			}
		})
	}()
}

// postApply runs fn on the UI loop when a Post sink is installed, else inline.
func (c *ModelSelectorComponent) postApply(fn func()) {
	if c.Post != nil {
		c.Post(fn)
		return
	}
	fn()
}

func firstErrorKey(errors map[string]error) string {
	keys := make([]string, 0, len(errors))
	for key := range errors {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

// GetDefaultModelReference returns the default-model reference (test seam).
func (c *ModelSelectorComponent) GetDefaultModelReference() *DefaultModelReference {
	return c.defaultModel
}

// SelectModel invokes the selection callback (test seam).
func (c *ModelSelectorComponent) SelectModel(model *ai.Model) {
	if c.onSelect != nil {
		c.onSelect(model)
	}
}

// SelectModelAsDefault invokes the save-as-default callback (test seam).
func (c *ModelSelectorComponent) SelectModelAsDefault(model *ai.Model) {
	if c.onSelectAsDefault != nil {
		c.onSelectAsDefault(model)
	}
}

// Dispose stops the background refresh.
func (c *ModelSelectorComponent) Dispose() {
	c.dispose()
}

// disposeLocked stops the background refresh for callers that already hold the
// state mutex (the input handler; D137).
func (c *ModelSelectorComponent) dispose() {
	if c.closed {
		return
	}
	c.closed = true
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
}

func (c *ModelSelectorComponent) sortModels(models []modelItem) []modelItem {
	sorted := append([]modelItem(nil), models...)
	sort.SliceStable(sorted, func(a, b int) bool {
		aCurrent := ai.ModelsAreEqual(c.currentModel, sorted[a].model)
		bCurrent := ai.ModelsAreEqual(c.currentModel, sorted[b].model)
		if aCurrent != bCurrent {
			return aCurrent
		}
		aDefault := c.isDefaultModel(sorted[a].model)
		bDefault := c.isDefaultModel(sorted[b].model)
		if aDefault != bDefault {
			return aDefault
		}
		return localeCompareTheme(sorted[a].provider, sorted[b].provider) < 0
	})
	return sorted
}

func (c *ModelSelectorComponent) getScopeText() string {
	theme := ActiveTheme()
	allText := theme.Fg("muted", "all")
	if c.scope == modelScopeAll {
		allText = theme.Fg("accent", "all")
	}
	scopedText := theme.Fg("muted", "scoped")
	if c.scope == modelScopeScoped {
		scopedText = theme.Fg("accent", "scoped")
	}
	return theme.Fg("muted", "Scope: ") + allText + theme.Fg("muted", " | ") + scopedText
}

func (c *ModelSelectorComponent) getScopeHintText() string {
	return KeyHint("tui.input.tab", "scope") + ActiveTheme().Fg("muted", " (all/scoped)")
}

func (c *ModelSelectorComponent) isDefaultModel(model *ai.Model) bool {
	return c.defaultModel != nil && model != nil &&
		c.defaultModel.Provider == string(model.Provider) && c.defaultModel.ID == model.ID
}

func (c *ModelSelectorComponent) isDefaultSearch(query string) bool {
	normalized := strings.ToLower(strings.TrimSpace(query))
	return normalized != "" && strings.HasPrefix("default", normalized)
}

// setScope is only called with the lock held.
func (c *ModelSelectorComponent) setScope(scope modelScopeKind) {
	if c.scope == scope {
		return
	}
	c.scope = scope
	c.activeModels = c.allModels
	if scope == modelScopeScoped {
		c.activeModels = c.scopedItems
	}
	c.selectedIndex = 0
	for index, item := range c.activeModels {
		if ai.ModelsAreEqual(c.currentModel, item.model) {
			c.selectedIndex = index
			break
		}
	}
	c.filterModels(c.searchInput.Value())
	if c.scopeText != nil {
		c.scopeText.SetText(c.getScopeText())
	}
}

// Render renders the selector under the state lock.
func (c *ModelSelectorComponent) Render(width int) []string {
	return c.Container.Render(width)
}

// filterModels is the locked implementation of the filter+list update.
func (c *ModelSelectorComponent) filterModels(query string) {
	if query != "" {
		filtered := tui.FuzzyFilter(c.activeModels, query, func(item modelItem) string {
			defaultText := ""
			if c.isDefaultModel(item.model) {
				defaultText = " default"
			}
			return GetModelSelectorSearchText(ModelSearchItem{
				ID: item.id, Provider: item.provider, Name: item.model.Name, HasName: item.model.Name != "",
			}) + defaultText
		})
		if c.isDefaultSearch(query) {
			var defaultItems []modelItem
			seen := map[string]bool{}
			for _, item := range c.activeModels {
				if c.isDefaultModel(item.model) {
					defaultItems = append(defaultItems, item)
					seen[item.provider+"\x00"+item.id] = true
				}
			}
			rest := make([]modelItem, 0, len(filtered))
			for _, item := range filtered {
				if !seen[item.provider+"\x00"+item.id] {
					rest = append(rest, item)
				}
			}
			c.filteredModels = append(defaultItems, rest...)
		} else {
			c.filteredModels = filtered
		}
	} else {
		c.filteredModels = c.activeModels
	}
	if query != "" {
		c.selectedIndex = 0
	} else {
		c.selectedIndex = minIntLocal(c.selectedIndex, maxIntLocal(0, len(c.filteredModels)-1))
	}
	c.updateList()
}

func (c *ModelSelectorComponent) updateList() {
	theme := ActiveTheme()
	c.listContainer.Clear()

	const maxVisible = 10
	startIndex := maxIntLocal(0, minIntLocal(c.selectedIndex-maxVisible/2, len(c.filteredModels)-maxVisible))
	endIndex := minIntLocal(startIndex+maxVisible, len(c.filteredModels))

	for i := startIndex; i < endIndex; i++ {
		item := c.filteredModels[i]
		isSelected := i == c.selectedIndex
		isCurrent := ai.ModelsAreEqual(c.currentModel, item.model)
		defaultBadge := ""
		if c.isDefaultModel(item.model) {
			defaultBadge = theme.Fg("muted", " · default")
		}
		cursor := "  "
		if isSelected {
			cursor = theme.Fg("accent", "→ ")
		}
		currentMarker := "  "
		if isCurrent {
			currentMarker = theme.Fg("accent", "✓ ")
		}
		modelText := item.id
		if isSelected {
			modelText = theme.Fg("accent", item.id)
		}
		providerBadge := theme.Fg("muted", "["+item.provider+"]")
		c.listContainer.AddChild(tui.NewText(cursor+currentMarker+modelText+" "+providerBadge+defaultBadge, 0, 0, nil))
	}

	if startIndex > 0 || endIndex < len(c.filteredModels) {
		c.listContainer.AddChild(tui.NewText(theme.Fg("muted",
			"  ("+itoa(c.selectedIndex+1)+"/"+itoa(len(c.filteredModels))+")"), 0, 0, nil))
	}

	switch {
	case c.errorMessage != "":
		for _, line := range strings.Split(c.errorMessage, "\n") {
			c.listContainer.AddChild(tui.NewText(theme.Fg("error", line), 0, 0, nil))
		}
	case len(c.filteredModels) == 0:
		c.listContainer.AddChild(tui.NewText(theme.Fg("muted", "  No matching models"), 0, 0, nil))
	default:
		if c.selectedIndex >= 0 && c.selectedIndex < len(c.filteredModels) {
			c.listContainer.AddChild(tui.NewSpacer(1))
			c.listContainer.AddChild(tui.NewText(theme.Fg("muted",
				"  Model Name: "+c.filteredModels[c.selectedIndex].model.Name), 0, 0, nil))
		}
	}
	if c.refreshStatusMessage != "" {
		color := "muted"
		if c.refreshStatusSuccess {
			color = "success"
		}
		c.listContainer.AddChild(tui.NewSpacer(1))
		c.listContainer.AddChild(tui.NewText(theme.Fg(color, "  "+c.refreshStatusMessage), 0, 0, nil))
	}
}

// HandleInput processes input.
func (c *ModelSelectorComponent) HandleInput(data string) {
	kb := tui.GetKeybindings()
	var (
		selectModel   *ai.Model
		selectDefault *ai.Model
		cancelled     bool
	)
	switch {
	case kb.Matches(data, "tui.input.tab"):
		if len(c.scopedItems) > 0 {
			next := modelScopeAll
			if c.scope == modelScopeAll {
				next = modelScopeScoped
			}
			c.setScope(next)
			if c.scopeHintText != nil {
				c.scopeHintText.SetText(c.getScopeHintText())
			}
		}
	case kb.Matches(data, "tui.select.up"):
		if len(c.filteredModels) == 0 {
			break
		}
		if c.selectedIndex == 0 {
			c.selectedIndex = len(c.filteredModels) - 1
		} else {
			c.selectedIndex--
		}
		c.updateList()
	case kb.Matches(data, "tui.select.down"):
		if len(c.filteredModels) == 0 {
			break
		}
		if c.selectedIndex == len(c.filteredModels)-1 {
			c.selectedIndex = 0
		} else {
			c.selectedIndex++
		}
		c.updateList()
	case kb.Matches(data, "tui.select.confirm"):
		if c.selectedIndex >= 0 && c.selectedIndex < len(c.filteredModels) {
			c.dispose()
			selectModel = c.filteredModels[c.selectedIndex].model
		}
	case kb.Matches(data, "tui.select.cancel"):
		c.dispose()
		cancelled = true
	case kb.Matches(data, "app.models.save") && c.onSelectAsDefault != nil:
		if c.selectedIndex >= 0 && c.selectedIndex < len(c.filteredModels) {
			c.dispose()
			selectDefault = c.filteredModels[c.selectedIndex].model
		}
	default:
		c.searchInput.HandleInput(data)
		c.filterModels(c.searchInput.Value())
	}

	// Invoke callbacks outside the state mutex (D137): they mutate the session
	// and request renders, which can otherwise invert with the renderer lock.
	if selectModel != nil && c.onSelect != nil {
		c.onSelect(selectModel)
	}
	if selectDefault != nil && c.onSelectAsDefault != nil {
		c.onSelectAsDefault(selectDefault)
	}
	if cancelled && c.onCancel != nil {
		c.onCancel()
	}
}

// selectModel disposes the selector and reports the selection (callers must
// not hold the state mutex).
func (c *ModelSelectorComponent) selectModel(model *ai.Model) {
	c.dispose()
	if c.onSelect != nil {
		c.onSelect(model)
	}
}

// GetSearchInput returns the search input (for IME focus propagation).
func (c *ModelSelectorComponent) GetSearchInput() *tui.Input { return c.searchInput }

// SetFocused implements Focusable.
func (c *ModelSelectorComponent) SetFocused(focused bool) {
	c.focused = focused
	c.searchInput.SetFocused(focused)
}

// IsFocused implements Focusable.
func (c *ModelSelectorComponent) IsFocused() bool { return c.focused }

// SelectedModel returns the currently highlighted model.
func (c *ModelSelectorComponent) SelectedModel() (*ai.Model, bool) {
	if c.selectedIndex < 0 || c.selectedIndex >= len(c.filteredModels) {
		return nil, false
	}
	return c.filteredModels[c.selectedIndex].model, true
}

var (
	_ tui.Component = (*ModelSelectorComponent)(nil)
	_ tui.Focusable = (*ModelSelectorComponent)(nil)
)
