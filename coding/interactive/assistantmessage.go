package interactive

import (
	"strings"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/tui"
)

// Port of src/modes/interactive/components/assistant-message.ts: the assistant
// transcript component with text/thinking blocks, the click-to-toggle thinking
// visibility, the truncation/abort/error notices, and the OSC 133 zone markers.

// AssistantMessageComponent renders a complete assistant message.
type AssistantMessageComponent struct {
	*tui.Container

	contentContainer *tui.Container
	zones            zoneMarkedLines

	hideThinkingBlock   bool
	markdownTheme       tui.MarkdownTheme
	hiddenThinkingLabel string
	outputPad           int
	transformers        []MarkdownTransformer

	lastMessage                 *ai.AssistantMessage
	hasToolCalls                bool
	isStreaming                 bool
	thinkingVisibilityOverrides map[int]bool

	// blockComponents reuses the block components across streaming updates so a
	// reused tui.Markdown keeps its incremental render cache (upstream mutates
	// `content` in place on every delta).
	blockComponents map[assistantBlockKey]tui.Component

	// thinkingMarkdowns caches the rendered thinking markdown per block run,
	// keyed *without* the visibility flag. Collapsing and re-expanding has to
	// reuse the same instance: a markdown that is rebuilt from scratch re-lexes
	// its whole text on the next paint, so without this every ctrl+t re-rendered
	// the thinking of the entire transcript and stalled the frame.
	thinkingMarkdowns map[assistantBlockKey]*tui.Markdown
	// streamingState is the isStreaming the current content was built for. The
	// streaming transform renders differently, so a transition has to drop the
	// reused render caches, which the text/token equality checks cannot see.
	streamingState bool
}

// assistantBlockKey identifies one block component across updates.
type assistantBlockKey struct {
	index  int
	kind   string // "text" | "thinking"
	hidden bool
}

// NewAssistantMessageComponent creates the component.
func NewAssistantMessageComponent(message *ai.AssistantMessage, hideThinkingBlock bool, markdownTheme *tui.MarkdownTheme, hiddenThinkingLabel string, outputPad int, transformers []MarkdownTransformer) *AssistantMessageComponent {
	if hiddenThinkingLabel == "" {
		hiddenThinkingLabel = "Thinking..."
	}
	component := &AssistantMessageComponent{
		Container:                   &tui.Container{},
		contentContainer:            &tui.Container{},
		hideThinkingBlock:           hideThinkingBlock,
		markdownTheme:               markdownThemeValue(markdownTheme),
		hiddenThinkingLabel:         hiddenThinkingLabel,
		outputPad:                   outputPad,
		transformers:                transformers,
		thinkingVisibilityOverrides: map[int]bool{},
		blockComponents:             map[assistantBlockKey]tui.Component{},
	}
	component.AddChild(component.contentContainer)
	if message != nil {
		component.UpdateContent(message, false)
	}
	return component
}

// Invalidate rebuilds the content.
func (c *AssistantMessageComponent) Invalidate() {
	c.Container.Invalidate()
	// The rendered lines carry the active theme's colours, so a theme switch has
	// to drop them even though the markdown text itself has not moved.
	c.invalidateMarkdownCaches()
	if c.lastMessage != nil {
		c.UpdateContent(c.lastMessage, c.isStreaming)
	}
}

// invalidateMarkdownCaches drops the reuse caches that only a rebuilt component
// fills. It is for changes the text-equality checks cannot see: the theme, the
// output padding and the streaming transform.
func (c *AssistantMessageComponent) invalidateMarkdownCaches() {
	for _, markdown := range c.thinkingMarkdowns {
		markdown.Invalidate()
	}
	for _, component := range c.blockComponents {
		if markdown, ok := component.(*tui.Markdown); ok {
			markdown.Invalidate()
		}
	}
}

// SetHideThinkingBlock toggles the hidden thinking blocks.
func (c *AssistantMessageComponent) SetHideThinkingBlock(hide bool) {
	// A visibility sweep walks every message in the transcript, and a message
	// that is already in the wanted state has nothing to rebuild — as long as it
	// carries no per-message override, which the sweep is what clears.
	if c.hideThinkingBlock == hide && len(c.thinkingVisibilityOverrides) == 0 {
		return
	}
	c.hideThinkingBlock = hide
	c.thinkingVisibilityOverrides = map[int]bool{}
	if c.lastMessage != nil {
		c.UpdateContent(c.lastMessage, c.isStreaming)
	}
}

// SetHiddenThinkingLabel updates the collapsed thinking label.
func (c *AssistantMessageComponent) SetHiddenThinkingLabel(label string) {
	c.hiddenThinkingLabel = label
	if c.lastMessage != nil {
		c.UpdateContent(c.lastMessage, c.isStreaming)
	}
}

// SetOutputPad updates the horizontal padding.
func (c *AssistantMessageComponent) SetOutputPad(padding int) {
	if c.outputPad == padding {
		return
	}
	c.outputPad = padding
	// The padding is baked into the rendered lines, which no cache key includes.
	c.invalidateMarkdownCaches()
	if c.lastMessage != nil {
		c.UpdateContent(c.lastMessage, c.isStreaming)
	}
}

// Render renders the message with the OSC 133 zone markers.
func (c *AssistantMessageComponent) Render(width int) []string {
	lines := c.Container.Render(width)
	if c.hasToolCalls {
		return lines
	}
	version, _ := c.Container.RenderVersion()
	return c.zones.get(lines, version)
}

// RenderVersion forwards the content container's revision so a parent can
// detect an in-place change (the has-tool-calls path returns its lines).
func (c *AssistantMessageComponent) RenderVersion() (uint64, bool) {
	return c.Container.RenderVersion()
}

// UpdateContent rebuilds the content for a message.
func (c *AssistantMessageComponent) UpdateContent(message *ai.AssistantMessage, isStreaming bool) {
	c.lastMessage = message
	// The streaming transform renders a partial fence differently, so a
	// transition invalidates the caches we are otherwise careful to keep.
	streamingChanged := c.streamingState != isStreaming
	c.streamingState = isStreaming
	if streamingChanged {
		c.invalidateMarkdownCaches()
	}
	if c.thinkingMarkdowns == nil {
		c.thinkingMarkdowns = map[assistantBlockKey]*tui.Markdown{}
	}
	c.isStreaming = isStreaming
	c.contentContainer.Clear()

	blocks := message.Content
	hasVisibleContent := false
	for _, block := range blocks {
		switch content := block.(type) {
		case ai.TextContent:
			if trimmedNonEmpty(content.Text) {
				hasVisibleContent = true
			}
		case ai.ThinkingContent:
			if trimmedNonEmpty(content.Thinking) {
				hasVisibleContent = true
			}
		}
	}
	if hasVisibleContent {
		c.contentContainer.AddChild(tui.NewSpacer(1))
	}

	theme := ActiveTheme()
	previous := c.blockComponents
	c.blockComponents = map[assistantBlockKey]tui.Component{}
	thinkingRunIndex := 0
	for i := 0; i < len(blocks); i++ {
		switch content := blocks[i].(type) {
		case ai.TextContent:
			if !trimmedNonEmpty(content.Text) {
				continue
			}
			key := assistantBlockKey{index: i, kind: "text"}
			markdown, _ := previous[key].(*tui.Markdown)
			if markdown == nil {
				markdown = tui.NewMarkdown("", c.outputPad, 0, c.markdownTheme, nil, tui.MarkdownOptions{})
			}
			markdown.PaddingX = c.outputPad
			markdown.Options = tui.MarkdownOptions{
				Transform: CreateMarkdownTransform("assistant", c.isStreaming, c.transformers),
			}
			markdown.SetTextIfChanged(strings.TrimSpace(content.Text))
			c.blockComponents[key] = markdown
			c.contentContainer.AddChild(markdown)
		case ai.ThinkingContent:
			var thinkingBlocks []string
			for ; i < len(blocks); i++ {
				if thinking, ok := blocks[i].(ai.ThinkingContent); ok {
					if trimmedNonEmpty(thinking.Thinking) {
						thinkingBlocks = append(thinkingBlocks, strings.TrimSpace(thinking.Thinking))
					}
					continue
				}
				break
			}
			i--

			if len(thinkingBlocks) == 0 {
				continue
			}

			hasVisibleContentAfter := false
			for _, block := range blocks[i+1:] {
				switch later := block.(type) {
				case ai.TextContent:
					if trimmedNonEmpty(later.Text) {
						hasVisibleContentAfter = true
					}
				case ai.ThinkingContent:
					if trimmedNonEmpty(later.Thinking) {
						hasVisibleContentAfter = true
					}
				}
				if hasVisibleContentAfter {
					break
				}
			}

			runIndex := thinkingRunIndex
			thinkingRunIndex++
			hidden := c.hideThinkingBlock
			if override, ok := c.thinkingVisibilityOverrides[runIndex]; ok {
				hidden = override
			}

			key := assistantBlockKey{index: i, kind: "thinking", hidden: hidden}
			var thinkingComponent tui.Component
			if hidden {
				thinkingComponent = tui.NewText(theme.Italic(theme.Fg("thinkingText", c.hiddenThinkingLabel)), c.outputPad, 0, nil)
			} else {
				markdownKey := assistantBlockKey{index: i, kind: "thinking"}
				markdown := c.thinkingMarkdowns[markdownKey]
				if markdown == nil {
					markdown = tui.NewMarkdown("", c.outputPad, 0, c.markdownTheme, nil, tui.MarkdownOptions{})
					c.thinkingMarkdowns[markdownKey] = markdown
				}
				markdown.PaddingX = c.outputPad
				markdown.DefaultTextStyle = &tui.DefaultTextStyle{
					Color: func(text string) string { return theme.Fg("thinkingText", text) }, Italic: true,
				}
				markdown.Options = tui.MarkdownOptions{
					Transform: CreateMarkdownTransform("assistant-thinking", c.isStreaming, c.transformers),
				}
				markdown.SetTextIfChanged(joinThinkingBlocks(thinkingBlocks))
				thinkingComponent = markdown
			}
			c.blockComponents[key] = thinkingComponent
			c.contentContainer.AddChild(tui.NewMouseRegion(thinkingComponent, func(event tui.TuiMouseEvent) *tui.TuiMouseDispatchResult {
				if event.Type != tui.MouseClick || event.Button != tui.MouseButtonLeft {
					return nil
				}
				c.thinkingVisibilityOverrides[runIndex] = !hidden
				if c.lastMessage != nil {
					c.UpdateContent(c.lastMessage, c.isStreaming)
				}
				return &tui.TuiMouseDispatchResult{TuiMouseEventResult: tui.TuiMouseEventResult{Handled: true}}
			}))
			if hasVisibleContentAfter {
				c.contentContainer.AddChild(tui.NewSpacer(1))
			}
		}
	}

	hasToolCalls := false
	for _, block := range blocks {
		if _, ok := block.(ai.ToolCall); ok {
			hasToolCalls = true
			break
		}
	}
	c.hasToolCalls = hasToolCalls

	stopReason := string(message.StopReason)
	switch {
	case stopReason == "length":
		c.contentContainer.AddChild(tui.NewSpacer(1))
		c.contentContainer.AddChild(tui.NewText(theme.Fg("error", "Response was truncated before completion."), c.outputPad, 0, nil))
	case !hasToolCalls && stopReason == "aborted":
		abortMessage := "Operation aborted"
		if message.ErrorMessage != nil && *message.ErrorMessage != "" && *message.ErrorMessage != "Request was aborted" {
			abortMessage = *message.ErrorMessage
		}
		c.contentContainer.AddChild(tui.NewSpacer(1))
		c.contentContainer.AddChild(tui.NewText(theme.Fg("error", abortMessage), c.outputPad, 0, nil))
	case !hasToolCalls && stopReason == "error":
		errorMessage := "Unknown error"
		if message.ErrorMessage != nil && *message.ErrorMessage != "" {
			errorMessage = *message.ErrorMessage
		}
		c.contentContainer.AddChild(tui.NewSpacer(1))
		c.contentContainer.AddChild(tui.NewText(theme.Fg("error", "Error: "+errorMessage), c.outputPad, 0, nil))
	}
}

func trimmedNonEmpty(value string) bool { return strings.TrimSpace(value) != "" }

func joinThinkingBlocks(blocks []string) string {
	result := ""
	for index, block := range blocks {
		if index > 0 {
			result += "\n\n"
		}
		result += block
	}
	return result
}

var _ tui.Component = (*AssistantMessageComponent)(nil)
