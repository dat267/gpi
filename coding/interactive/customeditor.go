package interactive

import (
	"strings"

	"github.com/dat267/gpi/tui"
)

// Port of src/modes/interactive/components/custom-editor.ts: the editor with
// the app-level keybindings and the embedded working-status border.
//
// Divergence D109: Go has no subclassing, so the editor's top-border override
// is a field hook (tui.Editor.TopBorder) and the app key handling lives in
// CustomEditor.HandleInput (callers hold the wrapper, not the embedded editor).

// CustomEditorOptions configure the custom editor.
type CustomEditorOptions struct {
	PaddingX               int
	AutocompleteMaxVisible int
	// EmbedWorkingStatus renders the working status in the editor's top border.
	EmbedWorkingStatus bool
}

// CustomEditor wraps the TUI editor with the coding-agent keybindings.
type CustomEditor struct {
	*tui.Editor

	keybindings            *AppKeybindingsManager
	workingStatusIndicator *StatusIndicator

	// EmbedWorkingStatus reports the border-status option.
	EmbedWorkingStatus bool
	// ActionHandlers maps app keybindings to handlers.
	ActionHandlers map[tui.Keybinding]func()

	OnEscape            func()
	OnCtrlD             func()
	OnPasteImage        func()
	OnExtensionShortcut func(data string) bool
}

// NewCustomEditor creates the editor.
func NewCustomEditor(host tui.EditorHost, theme tui.EditorTheme, keybindings *AppKeybindingsManager, options CustomEditorOptions) *CustomEditor {
	editor := tui.NewEditor(host, theme, tui.EditorOptions{
		PaddingX:               options.PaddingX,
		AutocompleteMaxVisible: options.AutocompleteMaxVisible,
	})
	custom := &CustomEditor{
		Editor:             editor,
		keybindings:        keybindings,
		EmbedWorkingStatus: options.EmbedWorkingStatus,
		ActionHandlers:     map[tui.Keybinding]func(){},
	}
	editor.TopBorder = custom.renderTopBorder
	return custom
}

// EmbedWorkingStatusValue reports the embed option (WorkingStatusEditor).
func (c *CustomEditor) EmbedWorkingStatusValue() bool { return c.EmbedWorkingStatus }

// WorkingBorderColor returns the editor's border color (WorkingStatusEditor).
func (c *CustomEditor) WorkingBorderColor() func(string) string { return c.BorderColor }

// SetWorkingStatusIndicator sets the embedded status indicator.
func (c *CustomEditor) SetWorkingStatusIndicator(indicator *StatusIndicator) {
	c.workingStatusIndicator = indicator
}

// GetWorkingStatusIndicator returns the embedded status indicator.
func (c *CustomEditor) GetWorkingStatusIndicator() *StatusIndicator {
	return c.workingStatusIndicator
}

// OnAction registers a handler for an app action.
func (c *CustomEditor) OnAction(action tui.Keybinding, handler func()) {
	c.ActionHandlers[action] = handler
}

// HandleInput processes the editor input with the app keybindings.
func (c *CustomEditor) HandleInput(data string) {
	if c.OnExtensionShortcut != nil && c.OnExtensionShortcut(data) {
		return
	}

	if c.keybindings != nil && c.keybindings.Matches(data, "app.clipboard.pasteImage") {
		if c.OnPasteImage != nil {
			c.OnPasteImage()
		}
		return
	}

	if c.keybindings != nil && c.keybindings.Matches(data, "app.interrupt") {
		if !c.Editor.IsShowingAutocomplete() {
			handler := c.OnEscape
			if handler == nil {
				handler = c.ActionHandlers["app.interrupt"]
			}
			if handler != nil {
				handler()
				return
			}
		}
		c.Editor.HandleInput(data)
		return
	}

	if c.keybindings != nil && c.keybindings.Matches(data, "app.exit") {
		if len(c.Editor.GetText()) == 0 {
			handler := c.OnCtrlD
			if handler == nil {
				handler = c.ActionHandlers["app.exit"]
			}
			if handler != nil {
				handler()
			}
			return
		}
		// Fall through for delete-char-forward when not empty.
	}

	if c.keybindings != nil &&
		(c.keybindings.Matches(data, "tui.editor.historyPrevious") ||
			c.keybindings.Matches(data, "tui.editor.historyNext")) {
		c.Editor.HandleInput(data)
		return
	}

	// Iterate in a stable order: upstream iterates the Map insertion order.
	for _, action := range c.actionOrder() {
		if action == "app.interrupt" || action == "app.exit" {
			continue
		}
		if c.keybindings != nil && c.keybindings.Matches(data, action) {
			c.ActionHandlers[action]()
			return
		}
	}

	c.Editor.HandleInput(data)
}

// actionOrder returns the registered actions (upstream Map insertion order,
// which the Go map does not preserve; sorted for determinism).
func (c *CustomEditor) actionOrder() []tui.Keybinding {
	actions := make([]tui.Keybinding, 0, len(c.ActionHandlers))
	for action := range c.ActionHandlers {
		actions = append(actions, action)
	}
	for i := 1; i < len(actions); i++ {
		for j := i; j > 0 && actions[j-1] > actions[j]; j-- {
			actions[j-1], actions[j] = actions[j], actions[j-1]
		}
	}
	return actions
}

// renderTopBorder renders the top border with the embedded working status.
func (c *CustomEditor) renderTopBorder(width int, hiddenLineCount int) string {
	if !c.EmbedWorkingStatus || c.workingStatusIndicator == nil || width <= 0 {
		return c.Editor.DefaultRenderTopBorder(width, hiddenLineCount)
	}

	status := c.workingStatusIndicator.RenderInBorder(maxIntLocal(1, width-5))
	statusWidth := tui.VisibleWidth(status)
	if statusWidth == 0 {
		return c.Editor.DefaultRenderTopBorder(width, hiddenLineCount)
	}

	overflowLabel := ""
	overflowLabelWidth := 0
	if hiddenLineCount > 0 {
		overflowLabel = " ↑ " + itoa(hiddenLineCount) + " more "
		overflowLabelWidth = tui.VisibleWidth(overflowLabel)
	}
	overflowStart := (width - overflowLabelWidth) / 2
	canFitOverflow := func() bool {
		return overflowLabel != "" && overflowLabelWidth+2 <= width &&
			overflowStart-(3+statusWidth+1) >= 1
	}

	if overflowLabel != "" && !canFitOverflow() {
		status = c.workingStatusIndicator.RenderSpinnerInBorder(width)
		statusWidth = tui.VisibleWidth(status)
	}

	if canFitOverflow() {
		leftBlockWidth := 3 + statusWidth + 1
		return c.BorderColor("── ") + status +
			c.BorderColor(" "+strings.Repeat("─", maxIntLocal(0, overflowStart-leftBlockWidth))+
				overflowLabel+strings.Repeat("─", maxIntLocal(0, width-overflowStart-overflowLabelWidth)))
	}

	if width >= statusWidth+5 {
		return c.BorderColor("── ") + status +
			c.BorderColor(" "+strings.Repeat("─", maxIntLocal(0, width-statusWidth-4)))
	}

	status = c.workingStatusIndicator.RenderSpinnerInBorder(width)
	statusWidth = tui.VisibleWidth(status)
	prefixWidth := minIntLocal(3, maxIntLocal(0, width-statusWidth))
	return c.BorderColor(strings.Repeat("─", prefixWidth)) + status +
		c.BorderColor(strings.Repeat("─", maxIntLocal(0, width-prefixWidth-statusWidth)))
}
