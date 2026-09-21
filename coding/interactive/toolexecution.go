package interactive

import (
	"encoding/json"
	"strings"

	"github.com/dat267/pier/tui"
)

// Port of src/modes/interactive/components/tool-execution.ts: the tool call
// and result renderer with the default/self shells, the fallback rendering,
// the partial/error/success backgrounds, the preview truncation, and the
// expansion toggle.
//
// Images are out of scope (D26/D89), so the image branches only produce the
// fallback indicators from GetTextOutput.

const fallbackPreviewLines = 10

// ToolRenderContext is passed to the tool renderers.
type ToolRenderContext struct {
	Args             any
	ToolCallID       string
	Invalidate       func()
	LastComponent    tui.Component
	State            any
	Cwd              string
	ExecutionStarted bool
	ArgsComplete     bool
	IsPartial        bool
	Expanded         bool
	ShowImages       bool
	IsError          bool
}

// ToolRenderResultOptions configure a result renderer.
type ToolRenderResultOptions struct {
	Expanded  bool
	IsPartial bool
}

// ToolRenderers is what the component needs from a tool: how to draw it.
type ToolRenderers struct {
	RenderShell  string // "" | "default" | "self"
	RenderCall   func(args any, theme *Theme, context *ToolRenderContext) tui.Component
	RenderResult func(result *SortToolResultContent, options ToolRenderResultOptions, theme *Theme, context *ToolRenderContext) tui.Component
}

// ToolExecutionOptions configure the component.
type ToolExecutionOptions struct {
	ShowImages      *bool
	ImageWidthCells int
}

// ToolExecutionComponent renders one tool call and its result.
type ToolExecutionComponent struct {
	*tui.Container

	imageWidthCells     int
	contentBox          *tui.Box
	contentText         *tui.Text
	contentTextRegion   *tui.MouseRegion
	selfRenderContainer *tui.Container
	selfRenderHeight    int

	callRendererComponent   tui.Component
	resultRendererComponent tui.Component
	rendererState           any

	toolName   string
	toolCallID string
	args       any
	expanded   bool
	showImages bool
	isPartial  bool
	definition *ToolRenderers
	host       tui.RenderRequester
	cwd        string

	executionStarted bool
	argsComplete     bool
	result           *SortToolResultContent
	hideComponent    bool
}

// NewToolExecutionComponent creates the component.
func NewToolExecutionComponent(toolName string, toolCallID string, args any, options ToolExecutionOptions, definition *ToolRenderers, host tui.RenderRequester, cwd string) *ToolExecutionComponent {
	showImages := true
	if options.ShowImages != nil {
		showImages = *options.ShowImages
	}
	imageWidthCells := options.ImageWidthCells
	if imageWidthCells == 0 {
		imageWidthCells = 60
	}
	component := &ToolExecutionComponent{
		Container:       &tui.Container{},
		toolName:        toolName,
		toolCallID:      toolCallID,
		args:            args,
		showImages:      showImages,
		imageWidthCells: imageWidthCells,
		isPartial:       true,
		definition:      definition,
		host:            host,
		cwd:             cwd,
	}

	theme := ActiveTheme()
	pendingBg := func(text string) string { return theme.Bg("toolPendingBg", text) }
	component.AddChild(tui.NewSpacer(1))
	component.contentBox = tui.NewBox(1, 1, pendingBg)
	component.contentText = tui.NewText("", 1, 1, pendingBg)
	component.contentTextRegion = component.createResultRegion(component.contentText)
	component.selfRenderContainer = &tui.Container{}

	if component.hasRendererDefinition() {
		if component.renderShell() == "self" {
			component.AddChild(component.selfRenderContainer)
		} else {
			component.AddChild(component.contentBox)
		}
	} else {
		component.AddChild(component.contentTextRegion)
	}

	component.updateDisplay()
	return component
}

func (c *ToolExecutionComponent) hasRendererDefinition() bool { return c.definition != nil }

func (c *ToolExecutionComponent) renderShell() string {
	if c.definition == nil || c.definition.RenderShell == "" {
		return "default"
	}
	return c.definition.RenderShell
}

func (c *ToolExecutionComponent) renderContext(lastComponent tui.Component) *ToolRenderContext {
	return &ToolRenderContext{
		Args:       c.args,
		ToolCallID: c.toolCallID,
		Invalidate: func() {
			c.Invalidate()
			if c.host != nil {
				c.host.RequestRender(false)
			}
		},
		LastComponent:    lastComponent,
		State:            c.rendererState,
		Cwd:              c.cwd,
		ExecutionStarted: c.executionStarted,
		ArgsComplete:     c.argsComplete,
		IsPartial:        c.isPartial,
		Expanded:         c.expanded,
		ShowImages:       c.showImages,
		IsError:          c.result != nil && c.result.IsError,
	}
}

func (c *ToolExecutionComponent) createCallFallback() tui.Component {
	theme := ActiveTheme()
	return tui.NewText(theme.Fg("toolTitle", theme.Bold(c.toolName)), 0, 0, nil)
}

func (c *ToolExecutionComponent) createResultFallback() tui.Component {
	output := c.textOutput()
	if output == "" {
		return nil
	}
	theme := ActiveTheme()
	lines := strings.Split(output, "\n")
	displayLines := lines
	if !c.expanded && len(displayLines) > fallbackPreviewLines {
		displayLines = displayLines[:fallbackPreviewLines]
	}
	remaining := len(lines) - len(displayLines)
	styled := make([]string, 0, len(displayLines))
	for _, line := range displayLines {
		styled = append(styled, theme.Fg("toolOutput", line))
	}
	text := strings.Join(styled, "\n")
	if remaining > 0 {
		text += theme.Fg("muted", "\n... ("+itoa(remaining)+" more lines, ") +
			KeyHint("app.tools.expand", "to expand") + theme.Fg("muted", ")")
	}
	return tui.NewText(text, 0, 0, nil)
}

func (c *ToolExecutionComponent) createResultRegion(component tui.Component) *tui.MouseRegion {
	return tui.NewMouseRegion(component, func(event tui.TuiMouseEvent) *tui.TuiMouseDispatchResult {
		if c.result == nil || event.Type != tui.MouseClick || event.Button != tui.MouseButtonLeft {
			return nil
		}
		c.SetExpanded(!c.expanded)
		return &tui.TuiMouseDispatchResult{TuiMouseEventResult: tui.TuiMouseEventResult{Handled: true}}
	})
}

// UpdateArgs replaces the tool arguments.
func (c *ToolExecutionComponent) UpdateArgs(args any) {
	c.args = args
	c.updateDisplay()
}

// MarkExecutionStarted records that execution began.
func (c *ToolExecutionComponent) MarkExecutionStarted() {
	c.executionStarted = true
	c.updateDisplay()
	if c.host != nil {
		c.host.RequestRender(false)
	}
}

// SetArgsComplete records that the arguments are complete.
func (c *ToolExecutionComponent) SetArgsComplete() {
	c.argsComplete = true
	c.updateDisplay()
	if c.host != nil {
		c.host.RequestRender(false)
	}
}

// UpdateResult records the tool result.
func (c *ToolExecutionComponent) UpdateResult(result *SortToolResultContent, isPartial bool) {
	c.result = result
	c.isPartial = isPartial
	c.updateDisplay()
}

// SetExpanded toggles the expanded state.
func (c *ToolExecutionComponent) SetExpanded(expanded bool) {
	c.expanded = expanded
	c.updateDisplay()
}

// SetShowImages toggles image rendering.
func (c *ToolExecutionComponent) SetShowImages(show bool) {
	c.showImages = show
	c.updateDisplay()
}

// SetImageWidthCells updates the image width.
func (c *ToolExecutionComponent) SetImageWidthCells(width int) {
	c.imageWidthCells = maxIntLocal(1, width)
	c.updateDisplay()
}

// Invalidate rebuilds the display.
func (c *ToolExecutionComponent) Invalidate() {
	c.Container.Invalidate()
	c.updateDisplay()
}

// Render renders the component.
func (c *ToolExecutionComponent) Render(width int) []string {
	if c.hideComponent {
		return nil
	}
	if c.hasRendererDefinition() && c.renderShell() == "self" {
		contentLines := c.selfRenderContainer.Render(width)
		c.selfRenderHeight = len(contentLines)
		if len(contentLines) == 0 {
			return nil
		}
		lines := []string{""}
		lines = append(lines, contentLines...)
		return lines
	}
	return c.Container.Render(width)
}

// HandleMouse forwards mouse events for the self-render shell.
func (c *ToolExecutionComponent) HandleMouse(event tui.TuiMouseEvent) *tui.TuiMouseDispatchResult {
	if !c.hasRendererDefinition() || c.renderShell() != "self" {
		return c.Container.HandleMouse(event)
	}
	if event.Y <= 0 || event.Y > c.selfRenderHeight {
		return nil
	}
	childEvent := event
	childEvent.Y = event.Y - 1
	childEvent.Height = c.selfRenderHeight
	return c.selfRenderContainer.HandleMouse(childEvent)
}

func (c *ToolExecutionComponent) updateDisplay() {
	theme := ActiveTheme()
	var bgFn func(string) string
	switch {
	case c.isPartial:
		bgFn = func(text string) string { return theme.Bg("toolPendingBg", text) }
	case c.result != nil && c.result.IsError:
		bgFn = func(text string) string { return theme.Bg("toolErrorBg", text) }
	default:
		bgFn = func(text string) string { return theme.Bg("toolSuccessBg", text) }
	}

	hasContent := false
	c.hideComponent = false
	if c.hasRendererDefinition() {
		renderContainer := c.contentBox
		if c.renderShell() == "self" {
			renderContainer = nil
		}
		var container tui.Component
		if renderContainer != nil {
			renderContainer.SetBgFn(bgFn)
			renderContainer.Clear()
			container = renderContainer
		} else {
			c.selfRenderContainer.Clear()
			container = c.selfRenderContainer
		}

		addChild := func(child tui.Component) {
			switch target := container.(type) {
			case *tui.Box:
				target.AddChild(child)
			case *tui.Container:
				target.AddChild(child)
			}
		}

		if c.definition.RenderCall == nil {
			addChild(c.createResultRegion(c.createCallFallback()))
			hasContent = true
		} else if component := safeRenderCall(c.definition.RenderCall, c.args, theme, c.renderContext(c.callRendererComponent)); component != nil {
			c.callRendererComponent = component
			addChild(c.createResultRegion(component))
			hasContent = true
		} else {
			c.callRendererComponent = nil
			addChild(c.createResultRegion(c.createCallFallback()))
			hasContent = true
		}

		if c.result != nil {
			if c.definition.RenderResult == nil {
				if component := c.createResultFallback(); component != nil {
					addChild(c.createResultRegion(component))
					hasContent = true
				}
			} else if component := safeRenderResult(c.definition.RenderResult, c.result,
				ToolRenderResultOptions{Expanded: c.expanded, IsPartial: c.isPartial},
				theme, c.renderContext(c.resultRendererComponent)); component != nil {
				c.resultRendererComponent = component
				addChild(c.createResultRegion(component))
				hasContent = true
			} else {
				c.resultRendererComponent = nil
				if component := c.createResultFallback(); component != nil {
					addChild(c.createResultRegion(component))
					hasContent = true
				}
			}
		}
	} else {
		c.contentText.SetCustomBgFn(bgFn)
		c.contentText.SetText(c.formatToolExecution())
		hasContent = true
	}

	if c.hasRendererDefinition() && !hasContent {
		c.hideComponent = true
	}
}

func safeRenderCall(renderer func(any, *Theme, *ToolRenderContext) tui.Component, args any, theme *Theme, context *ToolRenderContext) (component tui.Component) {
	defer func() {
		if recover() != nil {
			component = nil
		}
	}()
	return renderer(args, theme, context)
}

func safeRenderResult(renderer func(*SortToolResultContent, ToolRenderResultOptions, *Theme, *ToolRenderContext) tui.Component, result *SortToolResultContent, options ToolRenderResultOptions, theme *Theme, context *ToolRenderContext) (component tui.Component) {
	defer func() {
		if recover() != nil {
			component = nil
		}
	}()
	return renderer(result, options, theme, context)
}

func (c *ToolExecutionComponent) textOutput() string {
	return GetTextOutput(c.result, c.showImages)
}

func (c *ToolExecutionComponent) formatToolExecution() string {
	theme := ActiveTheme()
	text := theme.Fg("toolTitle", theme.Bold(c.toolName))
	if c.args != nil {
		if encoded, err := json.MarshalIndent(c.args, "", "  "); err == nil && len(encoded) > 0 {
			text += "\n\n" + string(encoded)
		}
	}
	if output := c.textOutput(); output != "" {
		text += "\n" + output
	}
	return text
}

var (
	_ tui.Component    = (*ToolExecutionComponent)(nil)
	_ tui.MouseHandler = (*ToolExecutionComponent)(nil)
)
