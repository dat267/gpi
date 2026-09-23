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

// bashResultState caches the collapsed preview. Command output is
// append-only while the tool streams, so the visual-line count of the
// already-seen prefix and the rendered tail are carried between frames:
// re-wrapping the whole output on every chunk was O(output) and stalled the
// loop on large command output.
type bashResultState struct {
	cachedOutput   string
	cachedWidth    int
	hasCached      bool
	cachedRendered []string

	countWidth  int
	countedText string
	totalVisual int
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
		if options.Expanded {
			children = append(children, tui.NewText("\n"+styleBashOutput(output, theme), 0, 0, nil))
		} else {
			// The collapsed preview styles and wraps only the lines it shows;
			// styling the whole output here was the per-chunk cost.
			children = append(children, &bashPreviewComponent{output: output, state: state, theme: theme})
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

// styleBashOutput applies the tool-output style per logical line.
func styleBashOutput(output string, theme *Theme) string {
	lines := strings.Split(output, "\n")
	for index, line := range lines {
		lines[index] = theme.Fg("toolOutput", line)
	}
	return strings.Join(lines, "\n")
}

func (c *bashPreviewComponent) Render(width int) []string {
	state := c.state
	if state.hasCached && state.cachedWidth == width && state.cachedOutput == c.output {
		return state.cachedRendered
	}

	tail := c.tailVisualLines(width)
	skipped := c.visualLineCount(width) - len(tail)
	if skipped < 0 {
		skipped = 0
	}

	lines := make([]string, 0, len(tail)+2)
	lines = append(lines, "")
	if skipped > 0 {
		hint := c.theme.Fg("muted", fmt.Sprintf("... (%d earlier lines,", skipped)) +
			" " + KeyHint("app.tools.expand", "to expand") + c.theme.Fg("muted", ")")
		lines = append(lines, tui.TruncateToWidth(hint, width, "...", false))
	}
	lines = append(lines, tail...)

	state.cachedOutput = c.output
	state.cachedWidth = width
	state.cachedRendered = lines
	state.hasCached = true
	return lines
}

// wrappedLineCount is how many visual lines one logical line wraps to, matching
// tui.Text.Render (tabs become three spaces; a blank line still occupies one).
func wrappedLineCount(line string, contentWidth int) int {
	if contentWidth < 1 {
		contentWidth = 1
	}
	if count, ok := plainWrappedLineCount(line, contentWidth); ok {
		return count
	}
	wrapped := tui.WrapTextWithAnsi(strings.ReplaceAll(line, "\t", "   "), contentWidth)
	if len(wrapped) == 0 {
		return 1
	}
	return len(wrapped)
}

// plainWrappedLineCount counts the visual lines of a plain ASCII line (no
// escape sequences, control bytes, or wide runes) without allocating, mirroring
// tui.wrapSingleLine's greedy word wrap with a tab counting as three spaces. It
// reports ok=false when the line needs the generic ANSI-aware path.
//
// The preview counts every line of a command's output, and a long output is
// counted again whenever the preview cache is cold (a rebuilt transcript) or
// the trailing line grows: the generic path allocated ~4 objects per line,
// which cost ~1 ms per 2000-line output and stacked up across the transcript
// into visible frame time.
func plainWrappedLineCount(line string, width int) (int, bool) {
	emitted := 0
	lineWidth := 0
	wordWidth := 0
	spaceWidth := 0
	place := func(isSpace bool, tokenWidth int) {
		if tokenWidth > width && !isSpace {
			// breakLongWord: full-width pieces, then the remainder.
			if lineWidth > 0 {
				emitted++
				lineWidth = 0
			}
			pieces := (tokenWidth + width - 1) / width
			emitted += pieces - 1
			lineWidth = tokenWidth - (pieces-1)*width
			return
		}
		if lineWidth+tokenWidth > width && lineWidth > 0 {
			emitted++
			if isSpace {
				// A wrapped whitespace run is dropped, not carried over.
				lineWidth = 0
			} else {
				lineWidth = tokenWidth
			}
			return
		}
		lineWidth += tokenWidth
	}
	for index := 0; index < len(line); index++ {
		char := line[index]
		if char >= 0x80 || char == 0x1b || char == '\r' || char == '\n' || char == 0x7f || (char < 0x20 && char != '\t') {
			return 0, false
		}
		if char == ' ' || char == '\t' {
			if wordWidth > 0 {
				place(false, wordWidth)
				wordWidth = 0
			}
			if char == '\t' {
				spaceWidth += 3
			} else {
				spaceWidth++
			}
			continue
		}
		if spaceWidth > 0 {
			place(true, spaceWidth)
			spaceWidth = 0
		}
		wordWidth++
	}
	if wordWidth > 0 {
		place(false, wordWidth)
	} else if spaceWidth > 0 {
		place(true, spaceWidth)
	}
	if lineWidth > 0 {
		emitted++
	}
	if emitted == 0 {
		// An empty line still occupies one visual line.
		emitted = 1
	}
	return emitted, true
}

// plainIsSpaceByte reports the bytes tui.wrapSingleLine treats as whitespace
// once tabs were expanded to three spaces.
func plainIsSpaceByte(char byte) bool { return char == ' ' || char == '\t' }

// visualLineCount returns the number of visual lines the whole output wraps
// to. Wrapping is per logical line, so only the lines appended since the last
// call are counted.
func (c *bashPreviewComponent) visualLineCount(width int) int {
	state := c.state
	if state.countWidth != width || !strings.HasPrefix(c.output, state.countedText) {
		state.countWidth = width
		state.countedText = ""
		state.totalVisual = 0
	}
	contentWidth := width
	if contentWidth < 1 {
		contentWidth = 1
	}
	consumed := len(state.countedText)
	rest := c.output[consumed:]
	if lastNewline := strings.LastIndexByte(rest, '\n'); lastNewline >= 0 {
		complete := rest[:lastNewline+1]
		body := complete[:len(complete)-1]
		for _, line := range strings.Split(body, "\n") {
			if count, ok := plainWrappedLineCount(line, contentWidth); ok {
				state.totalVisual += count
			} else {
				state.totalVisual += wrappedLineCount(line, contentWidth)
			}
		}
		consumed += len(complete)
		// Slice the output rather than concatenating: the counted prefix can be
		// large and copying it per chunk was itself O(output).
		state.countedText = c.output[:consumed]
		rest = c.output[consumed:]
	}
	// The trailing (still growing) line is re-counted each call.
	return state.totalVisual + wrappedLineCount(rest, contentWidth)
}

// tailVisualLines renders the last bashPreviewLines visual lines. Only the
// last bashPreviewLines logical lines can contain them, because a logical line
// is at least one visual line.
func (c *bashPreviewComponent) tailVisualLines(width int) []string {
	suffix := c.output
	end := len(suffix)
	for i := 0; i < bashPreviewLines; i++ {
		index := strings.LastIndexByte(suffix[:end], '\n')
		if index < 0 {
			end = 0
			break
		}
		end = index
	}
	suffix = suffix[end:]
	suffix = strings.TrimPrefix(suffix, "\n")
	return TruncateToVisualLines(styleBashOutput(suffix, c.theme), bashPreviewLines, width, 0).VisualLines
}

func (c *bashPreviewComponent) Invalidate() {
	state := c.state
	state.hasCached = false
	state.cachedOutput = ""
	state.cachedWidth = 0
	state.cachedRendered = nil
	state.countWidth = 0
	state.countedText = ""
	state.totalVisual = 0
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
