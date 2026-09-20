package interactive

import (
	"github.com/dat267/gpi/tui"
)

// Port of src/modes/interactive/components/extension-selector.ts: the generic
// string selector used by the extension UI and the auth flows.

// ExtensionSelectorOptions configure the selector.
type ExtensionSelectorOptions struct {
	Tui                   tui.TUI
	TimeoutMS             int
	OnToggleToolsExpanded func()
	Description           string
}

// ExtensionSelectorComponent renders a list of string options.
type ExtensionSelectorComponent struct {
	*tui.Container

	options               []string
	selectedIndex         int
	listContainer         *tui.Container
	onSelectCallback      func(option string)
	onCancelCallback      func()
	titleText             *tui.Text
	baseTitle             string
	countdown             *CountdownTimer
	onToggleToolsExpanded func()
}

// NewExtensionSelectorComponent creates the selector.
func NewExtensionSelectorComponent(title string, options []string, onSelect func(string), onCancel func(), opts ...ExtensionSelectorOptions) *ExtensionSelectorComponent {
	theme := ActiveTheme()
	component := &ExtensionSelectorComponent{
		Container:        &tui.Container{},
		options:          options,
		onSelectCallback: onSelect,
		onCancelCallback: onCancel,
		baseTitle:        title,
	}
	var optionsValue ExtensionSelectorOptions
	if len(opts) > 0 {
		optionsValue = opts[0]
	}
	component.onToggleToolsExpanded = optionsValue.OnToggleToolsExpanded

	component.AddChild(NewDynamicBorder(nil))
	component.AddChild(tui.NewSpacer(1))
	component.titleText = tui.NewText(theme.Fg("accent", theme.Bold(title)), 1, 0, nil)
	component.AddChild(component.titleText)
	if optionsValue.Description != "" {
		component.AddChild(tui.NewSpacer(1))
		component.AddChild(tui.NewText(theme.Fg("text", optionsValue.Description), 1, 0, nil))
	}
	component.AddChild(tui.NewSpacer(1))

	if optionsValue.TimeoutMS > 0 && optionsValue.Tui != nil {
		component.countdown = NewCountdownTimer(optionsValue.TimeoutMS, countdownHostAdapter{optionsValue.Tui},
			func(seconds int) {
				component.titleText.SetText(theme.Fg("accent", theme.Bold(component.baseTitle+" ("+itoa(seconds)+"s)")))
			},
			func() { component.onCancelCallback() })
	}

	component.listContainer = &tui.Container{}
	component.AddChild(component.listContainer)
	component.AddChild(tui.NewSpacer(1))
	component.AddChild(tui.NewText(RawKeyHint("↑↓", "navigate")+"  "+
		KeyHint("tui.select.confirm", "select")+"  "+
		KeyHint("tui.select.cancel", "cancel"), 1, 0, nil))
	component.AddChild(tui.NewSpacer(1))
	component.AddChild(NewDynamicBorder(nil))

	component.updateList()
	return component
}

type countdownHostAdapter struct{ ui tui.TUI }

func (c countdownHostAdapter) RequestRender(force bool) { c.ui.RequestRender(force) }

func (c *ExtensionSelectorComponent) updateList() {
	theme := ActiveTheme()
	c.listContainer.Clear()
	for index, option := range c.options {
		var text string
		if index == c.selectedIndex {
			text = theme.Fg("accent", "→ ") + theme.Fg("accent", option)
		} else {
			text = "  " + theme.Fg("text", option)
		}
		c.listContainer.AddChild(tui.NewText(text, 1, 0, nil))
	}
}

// HandleInput processes the selector input.
func (c *ExtensionSelectorComponent) HandleInput(keyData string) {
	kb := tui.GetKeybindings()
	switch {
	case kb.Matches(keyData, "app.tools.expand"):
		if c.onToggleToolsExpanded != nil {
			c.onToggleToolsExpanded()
		}
	case kb.Matches(keyData, "tui.select.up") || keyData == "k":
		if c.selectedIndex > 0 {
			c.selectedIndex--
		}
		c.updateList()
	case kb.Matches(keyData, "tui.select.down") || keyData == "j":
		if c.selectedIndex < len(c.options)-1 {
			c.selectedIndex++
		}
		c.updateList()
	case kb.Matches(keyData, "tui.select.confirm") || keyData == "\n":
		if c.selectedIndex >= 0 && c.selectedIndex < len(c.options) {
			c.onSelectCallback(c.options[c.selectedIndex])
		}
	case kb.Matches(keyData, "tui.select.cancel"):
		c.onCancelCallback()
	}
}

// Dispose stops the countdown.
func (c *ExtensionSelectorComponent) Dispose() {
	if c.countdown != nil {
		c.countdown.Dispose()
	}
}

// SelectedIndex returns the selection (test helper).
func (c *ExtensionSelectorComponent) SelectedIndex() int { return c.selectedIndex }
