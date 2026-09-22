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
// zoneMarkedLines applies the OSC 133 zone markers to the first and last line
// of a component's rendered output. The lines it is given are a render cache
// shared with the parent — writing the markers into them in place grew the
// markers on every paint — so the marked lines are cached and rebuilt only when
// the container hands back a different slice.
type zoneMarkedLines struct {
	marked []string
	source []string
}

func (z *zoneMarkedLines) get(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	if len(z.source) == len(lines) && len(lines) > 0 && &z.source[0] == &lines[0] {
		return z.marked
	}
	marked := make([]string, len(lines))
	copy(marked, lines)
	marked[0] = osc133ZoneStart + marked[0]
	marked[len(marked)-1] = osc133ZoneEnd + osc133ZoneFinal + marked[len(marked)-1]
	z.marked = marked
	z.source = lines
	return marked
}

type UserMessageComponent struct {
	*tui.Container

	zones         zoneMarkedLines
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
	return c.zones.get(c.Container.Render(width))
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
