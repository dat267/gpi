package interactive

import (
	"github.com/dat267/pier/tui"
)

// Port of src/modes/interactive/components/user-message.ts and
// custom-entry.ts.

const (
	osc133ZoneStart = "\x1b]133;A\x07"
	osc133ZoneEnd   = "\x1b]133;B\x07"
	osc133ZoneFinal = "\x1b]133;C\x07"
)

// UserMessageComponent renders a user message with the user background.
type UserMessageComponent struct {
	*tui.Container

	text          string
	markdownTheme tui.MarkdownTheme
	outputPad     int
	transformers  []MarkdownTransformer
}

// NewUserMessageComponent creates the component.
func NewUserMessageComponent(text string, markdownTheme *tui.MarkdownTheme, outputPad int, transformers []MarkdownTransformer) *UserMessageComponent {
	component := &UserMessageComponent{
		Container:    &tui.Container{},
		text:         text,
		outputPad:    outputPad,
		transformers: transformers,
	}
	if markdownTheme != nil {
		component.markdownTheme = *markdownTheme
	} else {
		component.markdownTheme = GetMarkdownTheme()
	}
	component.rebuild()
	return component
}

// SetOutputPad updates the horizontal padding.
func (c *UserMessageComponent) SetOutputPad(padding int) {
	c.outputPad = padding
	c.rebuild()
}

// SetText replaces the message text.
func (c *UserMessageComponent) SetText(text string) {
	c.text = text
	c.rebuild()
}

func (c *UserMessageComponent) rebuild() {
	c.Container.Clear()
	theme := ActiveTheme()
	contentBox := tui.NewBox(c.outputPad, 1, func(content string) string {
		return theme.Bg("userMessageBg", content)
	})
	transform := CreateMarkdownTransform("user", false, c.transformers)
	contentBox.AddChild(tui.NewMarkdown(c.text, 0, 0, c.markdownTheme,
		&tui.DefaultTextStyle{Color: func(content string) string { return theme.Fg("userMessageText", content) }},
		tui.MarkdownOptions{
			PreserveOrderedListMarkers: true,
			PreserveBackslashEscapes:   true,
			Transform:                  transform,
		}))
	c.Container.AddChild(contentBox)
}

// Render renders the message with the OSC 133 zone markers.
func (c *UserMessageComponent) Render(width int) []string {
	lines := c.Container.Render(width)
	if len(lines) == 0 {
		return lines
	}
	lines[0] = osc133ZoneStart + lines[0]
	lines[len(lines)-1] = osc133ZoneEnd + osc133ZoneFinal + lines[len(lines)-1]
	return lines
}

// EntryRenderer renders a custom session entry (extension surface; the
// extension runtime itself is out of scope: D41).
type EntryRenderer func(entry CustomEntry, options EntryRenderOptions, theme *Theme) tui.Component

// EntryRenderOptions configure the entry rendering.
type EntryRenderOptions struct {
	Expanded bool
}

// CustomEntry is a custom session entry payload.
type CustomEntry struct {
	CustomType string
	Data       any
}

// CustomEntryComponent renders a custom session entry.
type CustomEntryComponent struct {
	*tui.Container

	entry    CustomEntry
	renderer EntryRenderer
	custom   tui.Component
	expanded bool
}

// NewCustomEntryComponent creates the component.
func NewCustomEntryComponent(entry CustomEntry, renderer EntryRenderer) *CustomEntryComponent {
	component := &CustomEntryComponent{
		Container: &tui.Container{},
		entry:     entry,
		renderer:  renderer,
	}
	component.rebuild()
	return component
}

// HasContent reports whether the renderer produced a component.
func (c *CustomEntryComponent) HasContent() bool { return c.custom != nil }

// SetExpanded toggles the expanded state.
func (c *CustomEntryComponent) SetExpanded(expanded bool) {
	if c.expanded != expanded {
		c.expanded = expanded
		c.rebuild()
	}
}

// Invalidate invalidates the children and rebuilds.
func (c *CustomEntryComponent) Invalidate() {
	c.Container.Invalidate()
	c.rebuild()
}

func (c *CustomEntryComponent) rebuild() {
	c.Container.Clear()
	c.custom = nil

	var component tui.Component
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				theme := ActiveTheme()
				message := errorMessage(recovered)
				box := tui.NewBox(1, 1, func(text string) string { return theme.Bg("customMessageBg", text) })
				box.AddChild(tui.NewText(theme.Fg("error",
					"["+c.entry.CustomType+"] renderer failed: "+message), 0, 0, nil))
				component = box
			}
		}()
		component = c.renderer(c.entry, EntryRenderOptions{Expanded: c.expanded}, ActiveTheme())
	}()

	if component == nil {
		return
	}
	c.custom = component
	c.Container.AddChild(tui.NewSpacer(1))
	c.Container.AddChild(component)
}

func errorMessage(value any) string {
	if err, ok := value.(error); ok {
		return err.Error()
	}
	if text, ok := value.(string); ok {
		return text
	}
	return "unknown error"
}
