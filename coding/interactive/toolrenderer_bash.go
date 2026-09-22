package interactive

import (
	"fmt"
	"strings"
	"time"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// bashPreviewLines is the collapsed bash output preview height.
const bashPreviewLines = 5

func formatShellCall(args map[string]any, prompt string, theme *Theme) string {
	command, commandOK := argString(args, "command")
	timeout, hasTimeout := argNumber(args, "timeout")
	timeoutSuffix := ""
	if hasTimeout {
		timeoutSuffix = theme.Fg("muted", fmt.Sprintf(" (timeout %ds)", int(timeout)))
	}
	commandDisplay := ""
	if !commandOK {
		commandDisplay = invalidArgText(theme)
	} else if command == "" {
		commandDisplay = theme.Fg("toolOutput", "...")
	} else {
		commandDisplay = command
	}
	return theme.Fg("toolTitle", theme.Bold(prompt+" "+commandDisplay)) + timeoutSuffix
}

// bashResultState caches the collapsed preview per width.
type bashResultState struct {
	cachedWidth    int
	hasCached      bool
	cachedLines    []string
	cachedSkipped  int
	cachedRendered []string
}

func rebuildBashResult(result *SortToolResultContent, options ToolRenderResultOptions, theme *Theme,
	showImages bool, state *bashResultState) []tui.Component {
	children := make([]tui.Component, 0, 3)
	output := strings.TrimSpace(GetTextOutput(result, showImages))
	var truncation *coding.TruncationResult
	var fullOutputPath string
	if details := toolDetailsFrom[coding.BashToolDetails](result.Details); details != nil {
		if details.Truncation != nil {
			truncation = details.Truncation
		}
		if details.FullOutputPath != nil {
			fullOutputPath = *details.FullOutputPath
		}
	}
	if !options.IsPartial && truncation != nil && truncation.Truncated && fullOutputPath != "" &&
		strings.HasSuffix(output, "]") {
		if footerStart := strings.LastIndex(output, "\n\n["); footerStart != -1 &&
			strings.Contains(output[footerStart:], fullOutputPath) {
			output = strings.TrimRight(output[:footerStart], " \t\n")
		}
	}
	if output != "" {
		styledLines := make([]string, 0, strings.Count(output, "\n")+1)
		for _, line := range strings.Split(output, "\n") {
			styledLines = append(styledLines, theme.Fg("toolOutput", line))
		}
		styledOutput := strings.Join(styledLines, "\n")
		if options.Expanded {
			children = append(children, tui.NewText("\n"+styledOutput, 0, 0, nil))
		} else {
			state.cachedWidth = 0
			state.hasCached = false
			state.cachedLines = nil
			state.cachedSkipped = 0
			children = append(children, &bashPreviewComponent{output: styledOutput, state: state, theme: theme})
		}
	}
	if (truncation != nil && truncation.Truncated) || fullOutputPath != "" {
		warnings := make([]string, 0, 2)
		if fullOutputPath != "" {
			warnings = append(warnings, "Full output: "+fullOutputPath)
		}
		if truncation != nil && truncation.Truncated {
			if truncation.TruncatedBy == "lines" {
				warnings = append(warnings, fmt.Sprintf("Truncated: showing %d of %d lines",
					truncation.OutputLines, truncation.TotalLines))
			} else {
				bytes := int64(truncation.MaxBytes)
				if bytes <= 0 {
					bytes = int64(coding.DefaultMaxBytes)
				}
				warnings = append(warnings, fmt.Sprintf("Truncated: %d lines shown (%s limit)",
					truncation.OutputLines, coding.FormatSize(bytes)))
			}
		}
		children = append(children, tui.NewText("\n"+theme.Fg("warning", "["+strings.Join(warnings, ". ")+"]"), 0, 0, nil))
	}
	return children
}

// bashPreviewComponent renders the collapsed streaming preview.
type bashPreviewComponent struct {
	output string
	state  *bashResultState
	theme  *Theme
}

// bashResultState additionally caches the fully rendered preview lines (the
// hint included), so a warm frame reuses them (upstream caches visualLines the
// same way, bash-execution.ts).

func (c *bashPreviewComponent) Render(width int) []string {
	if c.state.hasCached && c.state.cachedWidth == width && c.state.cachedRendered != nil {
		return c.state.cachedRendered
	}
	if !c.state.hasCached || c.state.cachedWidth != width {
		preview := TruncateToVisualLines(c.output, bashPreviewLines, width, 0)
		c.state.cachedLines = preview.VisualLines
		c.state.cachedSkipped = preview.SkippedCount
		c.state.cachedWidth = width
		c.state.hasCached = true
	}
	lines := make([]string, 0, len(c.state.cachedLines)+2)
	lines = append(lines, "")
	if c.state.cachedSkipped > 0 {
		hint := c.theme.Fg("muted", fmt.Sprintf("... (%d earlier lines,", c.state.cachedSkipped)) +
			" " + KeyHint("app.tools.expand", "to expand") + c.theme.Fg("muted", ")")
		lines = append(lines, tui.TruncateToWidth(hint, width, "...", false))
	}
	lines = append(lines, c.state.cachedLines...)
	c.state.cachedRendered = lines
	return lines
}

func (c *bashPreviewComponent) Invalidate() {
	c.state.hasCached = false
	c.state.cachedWidth = 0
	c.state.cachedLines = nil
	c.state.cachedRendered = nil
	c.state.cachedSkipped = 0
}

// CreateShellRenderers builds the shell tool renderers (bash and powershell
// differ only in the prompt they display).
func CreateShellRenderers(prompt string) ToolRenderers {
	return ToolRenderers{
		RenderCall: func(args any, theme *Theme, context *ToolRenderContext) tui.Component {
			state := shellCallStateFor(context)
			if context != nil && context.ExecutionStarted && state.startedAtMS == 0 {
				state.startedAtMS = time.Now().UnixMilli()
				state.endedAtMS = 0
			}
			text := toolTextComponent(context)
			text.SetText(formatShellCall(toolArgs(args), prompt, theme))
			return text
		},
		RenderResult: func(result *SortToolResultContent, options ToolRenderResultOptions, theme *Theme, context *ToolRenderContext) tui.Component {
			state := shellCallStateFor(context)
			// Upstream stops the timer on the final (or error) result
			// (`!isPartial || isError`), not on a partial update.
			if state.startedAtMS != 0 && state.endedAtMS == 0 && (!options.IsPartial || (context != nil && context.IsError)) {
				state.endedAtMS = time.Now().UnixMilli()
			}
			container := &tui.Container{}
			if context != nil {
				if existing, ok := context.LastComponent.(*tui.Container); ok {
					container = existing
				}
			}
			container.Clear()
			for _, child := range rebuildBashResult(result, options, theme, context.ShowImages, &state.preview) {
				container.AddChild(child)
			}
			if state.startedAtMS != 0 {
				container.AddChild(&shellElapsedComponent{state: state, theme: theme})
			}
			return container
		},
	}
}

type shellCallState struct {
	startedAtMS int64
	endedAtMS   int64
	// preview caches the collapsed bash preview. It lives here (upstream keeps
	// it on the result component) because the render context has a single
	// State slot: a second bashStateFor(context) used to overwrite the shell
	// timer state, resetting the elapsed duration on every update.
	preview bashResultState
}

// shellElapsedComponent renders the running/final duration. The value is
// computed at render time (so a frame shows the current elapsed time), and
// AnimationFrame keeps the owner re-rendering once a second while the call
// runs. Upstream arms the same 1s redraw with setInterval(context.invalidate).
type shellElapsedComponent struct {
	state *shellCallState
	theme *Theme
}

func (c *shellElapsedComponent) Render(width int) []string {
	if c.state.startedAtMS == 0 {
		return nil
	}
	label := "Took"
	if c.state.endedAtMS == 0 {
		label = "Elapsed"
	}
	end := c.state.endedAtMS
	if end == 0 {
		end = time.Now().UnixMilli()
	}
	return []string{"", c.theme.Fg("muted", fmt.Sprintf("%s %s", label, formatDuration(float64(end-c.state.startedAtMS))))}
}

func (c *shellElapsedComponent) Invalidate() {}

// AnimationFrame keeps the elapsed label ticking while the call runs.
func (c *shellElapsedComponent) AnimationFrame(now time.Time) (bool, time.Duration) {
	if c.state.startedAtMS == 0 || c.state.endedAtMS != 0 {
		return false, 0
	}
	return true, time.Second
}

func shellCallStateFor(context *ToolRenderContext) *shellCallState {
	if context == nil {
		return &shellCallState{}
	}
	if state, ok := context.State.(*shellCallState); ok {
		return state
	}
	state := &shellCallState{}
	context.State = state
	return state
}

var bashRenderers = CreateShellRenderers("$")

var powershellRenderers = CreateShellRenderers("PS>")

// --- write ------------------------------------------------------------------
