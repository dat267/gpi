package interactive

import (
	"github.com/dat267/pier/tui"
)

// Port of src/modes/interactive/components/settings-submenu.ts: the
// single-step and stepped submenus.

var submenuSelectListLayout = tui.SelectListLayoutOptions{
	MinPrimaryColumnWidth: 12, HasMin: true,
	MaxPrimaryColumnWidth: 32, HasMax: true,
}

// SelectSubmenuOptions configure a submenu step.
type SelectSubmenuOptions struct {
	Searchable bool
	Layout     *tui.SelectListLayoutOptions
	HasLayout  bool
}

// SelectSubmenu is a single-step titled select list.
type SelectSubmenu struct {
	*tui.Container

	selectList     *tui.SelectList
	listChildIndex int
	allOptions     []tui.SelectItem
	listLayout     tui.SelectListLayoutOptions
	searchInput    *tui.Input

	onSelect          func(value string)
	onCancel          func()
	onSelectionChange func(value string)
}

// NewSelectSubmenu creates the submenu.
func NewSelectSubmenu(title string, description string, options []tui.SelectItem, currentValue string, onSelect func(string), onCancel func(), onSelectionChange func(string), submenuOptions SelectSubmenuOptions) *SelectSubmenu {
	theme := ActiveTheme()
	layout := submenuSelectListLayout
	if submenuOptions.HasLayout && submenuOptions.Layout != nil {
		layout = *submenuOptions.Layout
	}
	component := &SelectSubmenu{
		Container:         &tui.Container{},
		allOptions:        options,
		listLayout:        layout,
		onSelect:          onSelect,
		onCancel:          onCancel,
		onSelectionChange: onSelectionChange,
	}

	component.AddChild(tui.NewText(theme.Bold(theme.Fg("accent", title)), 0, 0, nil))
	if description != "" {
		component.AddChild(tui.NewSpacer(1))
		component.AddChild(tui.NewText(theme.Fg("muted", description), 0, 0, nil))
	}
	if submenuOptions.Searchable {
		component.AddChild(tui.NewSpacer(1))
		component.searchInput = tui.NewInput(tui.InputOptions{})
		component.searchInput.OnSubmit = func(string) { component.selectList.HandleInput("\r") }
		component.AddChild(component.searchInput)
	}
	component.AddChild(tui.NewSpacer(1))

	component.selectList = component.buildSelectList(options, currentValue)
	component.listChildIndex = len(component.Children)
	component.AddChild(component.selectList)

	component.AddChild(tui.NewSpacer(1))
	hint := "  Enter to select · Esc to go back"
	if submenuOptions.Searchable {
		hint = "  Type to filter · Enter to select · Esc to go back"
	}
	component.AddChild(tui.NewText(theme.Fg("dim", hint), 0, 0, nil))
	return component
}

func (c *SelectSubmenu) buildSelectList(options []tui.SelectItem, preselect string) *tui.SelectList {
	maxVisible := minIntLocal(len(options), 10)
	list := tui.NewSelectList(options, maxVisible, GetSelectListTheme(), c.listLayout)
	for index, option := range options {
		if option.Value == preselect {
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
	if c.onSelectionChange != nil {
		callback := c.onSelectionChange
		list.OnSelectionChange = func(item tui.SelectItem) { callback(item.Value) }
	}
	return list
}

func (c *SelectSubmenu) applyFilter(query string) {
	filtered := c.allOptions
	if query != "" {
		filtered = tui.FuzzyFilter(c.allOptions, query, func(item tui.SelectItem) string {
			return item.Label + " " + item.Description
		})
	}
	newList := c.buildSelectList(filtered, "")
	c.Children[c.listChildIndex] = newList
	c.selectList = newList
}

// HandleInput processes input.
func (c *SelectSubmenu) HandleInput(data string) {
	if c.searchInput == nil {
		c.selectList.HandleInput(data)
		return
	}
	kb := tui.GetKeybindings()
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
func (c *SelectSubmenu) GetSelectList() *tui.SelectList { return c.selectList }

// SteppedSubmenuStep is one step of a stepped submenu.
type SteppedSubmenuStep struct {
	Key         string
	Title       func(context map[string]string) string
	Description func(context map[string]string) string
	Options     func(context map[string]string) []tui.SelectItem
	Preselect   func(context map[string]string) (string, bool)
	Searchable  bool
	Layout      *tui.SelectListLayoutOptions
	HasLayout   bool
}

// SteppedSubmenuOptions configure a stepped submenu.
type SteppedSubmenuOptions struct {
	StartAtStep    *int
	InitialContext map[string]string
	Loop           bool
}

// SteppedSubmenu is an N-step submenu built on SelectSubmenu.
type SteppedSubmenu struct {
	*tui.Container

	steps           []SteppedSubmenuStep
	onComplete      func(context map[string]string)
	onCancel        func()
	options         SteppedSubmenuOptions
	activeComponent interface {
		tui.Component
		HandleInput(data string)
	}
	context map[string]string
}

// NewSteppedSubmenu creates the stepped submenu.
func NewSteppedSubmenu(steps []SteppedSubmenuStep, onComplete func(map[string]string), onCancel func(), options SteppedSubmenuOptions) *SteppedSubmenu {
	component := &SteppedSubmenu{
		Container:  &tui.Container{},
		steps:      steps,
		onComplete: onComplete,
		onCancel:   onCancel,
		options:    options,
		context:    map[string]string{},
	}
	for key, value := range options.InitialContext {
		component.context[key] = value
	}
	startAt := 0
	if options.StartAtStep != nil {
		startAt = *options.StartAtStep
	}
	component.activeComponent = component.buildStep(startAt)
	return component
}

func (c *SteppedSubmenu) buildStep(stepIndex int) interface {
	tui.Component
	HandleInput(data string)
} {
	step := c.steps[stepIndex]
	total := len(c.steps)
	stepLabel := ""
	if total > 1 {
		stepLabel = "Step " + itoa(stepIndex+1) + "/" + itoa(total) + " · "
	}
	title := step.Title(c.context)
	description := step.Description(c.context)
	items := step.Options(c.context)
	preselect := ""
	if step.Preselect != nil {
		if value, ok := step.Preselect(c.context); ok {
			preselect = value
		}
	}
	submenuOptions := SelectSubmenuOptions{Searchable: step.Searchable, Layout: step.Layout, HasLayout: step.HasLayout}

	return NewSelectSubmenu(title, stepLabel+description, items, preselect,
		func(value string) {
			c.context[step.Key] = value
			if stepIndex < total-1 {
				c.activeComponent = c.buildStep(stepIndex + 1)
				return
			}
			snapshot := map[string]string{}
			for key, entry := range c.context {
				snapshot[key] = entry
			}
			if c.onComplete != nil {
				c.onComplete(snapshot)
			}
			if c.options.Loop {
				c.context = map[string]string{}
				c.activeComponent = c.buildStep(0)
			} else if c.onCancel != nil {
				c.onCancel()
			}
		},
		func() {
			if stepIndex > 0 {
				delete(c.context, step.Key)
				c.activeComponent = c.buildStep(stepIndex - 1)
			} else if c.onCancel != nil {
				c.onCancel()
			}
		},
		nil,
		submenuOptions)
}

// Render renders the active step.
func (c *SteppedSubmenu) Render(width int) []string {
	return c.activeComponent.Render(width)
}

// HandleInput forwards input to the active step.
func (c *SteppedSubmenu) HandleInput(data string) {
	c.activeComponent.HandleInput(data)
}

// Invalidate invalidates the active step.
func (c *SteppedSubmenu) Invalidate() {
	c.activeComponent.Invalidate()
}

// Context returns the accumulated step selections.
func (c *SteppedSubmenu) Context() map[string]string {
	out := map[string]string{}
	for key, value := range c.context {
		out[key] = value
	}
	return out
}

var (
	_ tui.Component = (*SelectSubmenu)(nil)
	_ tui.Component = (*SteppedSubmenu)(nil)
)
