package interactive

import (
	"os"
	"strings"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of src/modes/interactive/components/bash-execution.ts and the
// render-utils helpers it and tool-execution.ts use.

const previewLines = 20

// ShortenPath replaces the home directory prefix with "~".
func ShortenPath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

// Str normalizes a value to a string: nil becomes "", other non-strings fail.
func Str(value any) (string, bool) {
	if value == nil {
		return "", true
	}
	text, ok := value.(string)
	return text, ok
}

// NormalizeDisplayText removes carriage returns.
func NormalizeDisplayText(text string) string { return strings.ReplaceAll(text, "\r", "") }

// ToolResultContent is one result content block.
type ToolResultContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
}

// SortToolResultContent is the result payload the components render.
type SortToolResultContent struct {
	Content []ToolResultContent
	Details any
	IsError bool
}

// GetTextOutput joins the text blocks and appends image indicators when images
// are unavailable or hidden.
func GetTextOutput(result *SortToolResultContent, showImages bool) string {
	if result == nil {
		return ""
	}
	var textBlocks []string
	var imageBlocks []ToolResultContent
	for _, block := range result.Content {
		switch block.Type {
		case "text":
			textBlocks = append(textBlocks, strings.ReplaceAll(coding.SanitizeBinaryOutput(coding.StripAnsi(block.Text)), "\r", ""))
		case "image":
			imageBlocks = append(imageBlocks, block)
		}
	}
	output := strings.Join(textBlocks, "\n")

	caps := tui.GetTerminalCapabilities()
	if len(imageBlocks) > 0 && (caps.Images == "" || !showImages) {
		indicators := make([]string, 0, len(imageBlocks))
		for _, block := range imageBlocks {
			mimeType := block.MimeType
			if mimeType == "" {
				mimeType = "image/unknown"
			}
			// Image dimensions need the image transport (out of scope: D26),
			// so the fallback is rendered without them.
			indicators = append(indicators, imageFallbackText(mimeType))
		}
		joined := strings.Join(indicators, "\n")
		if output != "" {
			output = output + "\n" + joined
		} else {
			output = joined
		}
	}
	return output
}

func imageFallbackText(mimeType string) string {
	// Port of terminal-image's imageFallback without dimensions (the image
	// transport is out of scope: D26).
	return "[Image: " + mimeType + "]"
}

// InvalidArgText renders the invalid-argument marker.
func InvalidArgText(theme *Theme) string { return theme.Fg("error", "[invalid arg]") }

// RenderToolPath renders a tool path argument.
func RenderToolPath(rawPath *string, theme *Theme, cwd string, emptyFallback string) string {
	if rawPath == nil {
		return InvalidArgText(theme)
	}
	value := *rawPath
	if value == "" {
		value = emptyFallback
	}
	if value == "" {
		return theme.Fg("toolOutput", "...")
	}
	// Hyperlinks need the path-to-file-URL conversion; with capabilities off
	// this is the plain styled path (same as upstream's fallback).
	return theme.Fg("accent", ShortenPath(value))
}

// BashExecutionComponent displays a bash command with streaming output.
type BashExecutionComponent struct {
	*tui.Container

	command          string
	outputLines      []string
	status           string // running | complete | cancelled | error
	exitCode         *int
	loader           *tui.Loader
	truncation       *coding.TruncationResult
	fullOutputPath   string
	expanded         bool
	contentContainer *tui.Container
	colorKey         string
}

// NewBashExecutionComponent creates the component.
func NewBashExecutionComponent(command string, host tui.RenderRequester, excludeFromContext bool) *BashExecutionComponent {
	colorKey := "bashMode"
	if excludeFromContext {
		colorKey = "dim"
	}
	theme := ActiveTheme()
	borderColor := func(str string) string { return theme.Fg(colorKey, str) }

	component := &BashExecutionComponent{
		Container:        &tui.Container{},
		command:          command,
		status:           "running",
		contentContainer: &tui.Container{},
		colorKey:         colorKey,
	}

	component.AddChild(tui.NewSpacer(1))
	component.AddChild(NewDynamicBorder(borderColor))
	component.AddChild(component.contentContainer)

	header := tui.NewText(theme.Fg(colorKey, theme.Bold("$ "+command)), 1, 0, nil)
	component.contentContainer.AddChild(header)

	component.loader = tui.NewLoader(host,
		func(spinner string) string { return theme.Fg(colorKey, spinner) },
		func(text string) string { return theme.Fg("muted", text) },
		"Running... ("+KeyText("tui.select.cancel")+" to cancel)", nil)
	component.contentContainer.AddChild(component.loader)

	component.AddChild(NewDynamicBorder(borderColor))
	return component
}

// SetExpanded toggles the preview/full output.
func (c *BashExecutionComponent) SetExpanded(expanded bool) {
	c.expanded = expanded
	c.updateDisplay()
}

// Invalidate rebuilds the display.
func (c *BashExecutionComponent) Invalidate() {
	c.Container.Invalidate()
	c.updateDisplay()
}

// AppendOutput appends a streamed chunk (ANSI stripped, line endings
// normalized).
func (c *BashExecutionComponent) AppendOutput(chunk string) {
	clean := coding.StripAnsi(chunk)
	clean = strings.ReplaceAll(clean, "\r\n", "\n")
	clean = strings.ReplaceAll(clean, "\r", "\n")

	newLines := strings.Split(clean, "\n")
	if len(c.outputLines) > 0 && len(newLines) > 0 {
		c.outputLines[len(c.outputLines)-1] += newLines[0]
		c.outputLines = append(c.outputLines, newLines[1:]...)
	} else {
		c.outputLines = append(c.outputLines, newLines...)
	}
	c.updateDisplay()
}

// SetComplete marks the command finished.
func (c *BashExecutionComponent) SetComplete(exitCode *int, cancelled bool, truncation *coding.TruncationResult, fullOutputPath string) {
	c.exitCode = exitCode
	switch {
	case cancelled:
		c.status = "cancelled"
	case exitCode != nil && *exitCode != 0:
		c.status = "error"
	default:
		c.status = "complete"
	}
	c.truncation = truncation
	c.fullOutputPath = fullOutputPath
	c.loader.Stop()
	c.updateDisplay()
}

func (c *BashExecutionComponent) updateDisplay() {
	theme := ActiveTheme()
	fullOutput := strings.Join(c.outputLines, "\n")
	contextTruncation := coding.TruncateTail(fullOutput, coding.TruncationOptions{
		MaxLines: coding.DefaultMaxLines,
		MaxBytes: coding.DefaultMaxBytes,
	})

	var availableLines []string
	if contextTruncation.Content != "" {
		availableLines = strings.Split(contextTruncation.Content, "\n")
	}

	previewLogicalLines := availableLines
	if len(previewLogicalLines) > previewLines {
		previewLogicalLines = previewLogicalLines[len(previewLogicalLines)-previewLines:]
	}
	hiddenLineCount := len(availableLines) - len(previewLogicalLines)

	c.contentContainer.Clear()
	header := tui.NewText(theme.Fg("bashMode", theme.Bold("$ "+c.command)), 1, 0, nil)
	c.contentContainer.AddChild(header)

	if len(availableLines) > 0 {
		if c.expanded {
			styled := make([]string, 0, len(availableLines))
			for _, line := range availableLines {
				styled = append(styled, theme.Fg("muted", line))
			}
			c.contentContainer.AddChild(tui.NewText("\n"+strings.Join(styled, "\n"), 1, 0, nil))
		} else {
			styled := make([]string, 0, len(previewLogicalLines))
			for _, line := range previewLogicalLines {
				styled = append(styled, theme.Fg("muted", line))
			}
			styledInput := "\n" + strings.Join(styled, "\n")
			c.contentContainer.AddChild(&previewComponent{input: styledInput, maxLines: previewLines})
		}
	}

	if c.status == "running" {
		c.contentContainer.AddChild(c.loader)
		return
	}

	var statusParts []string
	if hiddenLineCount > 0 {
		if c.expanded {
			statusParts = append(statusParts, theme.Fg("muted", "(")+KeyHint("app.tools.expand", "to collapse")+theme.Fg("muted", ")"))
		} else {
			statusParts = append(statusParts, theme.Fg("muted", "... "+itoa(hiddenLineCount)+" more lines (")+
				KeyHint("app.tools.expand", "to expand")+theme.Fg("muted", ")"))
		}
	}
	switch c.status {
	case "cancelled":
		statusParts = append(statusParts, theme.Fg("warning", "(cancelled)"))
	case "error":
		exitCode := 0
		if c.exitCode != nil {
			exitCode = *c.exitCode
		}
		statusParts = append(statusParts, theme.Fg("error", "(exit "+itoa(exitCode)+")"))
	}

	wasTruncated := contextTruncation.Truncated || (c.truncation != nil && c.truncation.Truncated)
	if wasTruncated && c.fullOutputPath != "" {
		statusParts = append(statusParts, theme.Fg("warning", "Output truncated. Full output: "+c.fullOutputPath))
	}

	if len(statusParts) > 0 {
		c.contentContainer.AddChild(tui.NewText("\n"+strings.Join(statusParts, "\n"), 1, 0, nil))
	}
}

// previewComponent renders the width-aware preview (the upstream inline
// component with width caching).
type previewComponent struct {
	input    string
	maxLines int

	cachedWidth int
	hasWidth    bool
	cachedLines []string
}

func (p *previewComponent) Render(width int) []string {
	if p.cachedLines == nil || !p.hasWidth || p.cachedWidth != width {
		result := TruncateToVisualLines(p.input, p.maxLines, width, 1)
		p.cachedLines = result.VisualLines
		p.cachedWidth = width
		p.hasWidth = true
	}
	if p.cachedLines == nil {
		return []string{}
	}
	return p.cachedLines
}

func (p *previewComponent) Invalidate() {
	p.hasWidth = false
	p.cachedLines = nil
}

// GetOutput returns the raw output.
func (c *BashExecutionComponent) GetOutput() string { return strings.Join(c.outputLines, "\n") }

// GetCommand returns the executed command.
func (c *BashExecutionComponent) GetCommand() string { return c.command }
