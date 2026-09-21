package interactive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

// Port of core/tools/renderers/{index,read,bash,edit,write,grep,find,ls}.ts:
// the built-in tool presentation, keyed by tool name. Renderers live apart
// from the tool implementations so a display-only process does not load the
// execution path.
//
// D133 previously returned nil here (raw-JSON fallback); the built-in
// renderers are now ported, so `withBuiltInRenderers` behaves like upstream.

// bashPreviewLines is the collapsed bash output preview height.
const bashPreviewLines = 5

// writePartialFullHighlightLines is the prefix re-highlighted during streaming.
const writePartialFullHighlightLines = 50

// toolArgs decodes raw tool arguments into a JSON object.
func toolArgs(args any) map[string]any {
	switch typed := args.(type) {
	case json.RawMessage:
		var decoded map[string]any
		if err := json.Unmarshal(typed, &decoded); err != nil {
			return nil
		}
		return decoded
	case []byte:
		var decoded map[string]any
		if err := json.Unmarshal(typed, &decoded); err != nil {
			return nil
		}
		return decoded
	case string:
		var decoded map[string]any
		if err := json.Unmarshal([]byte(typed), &decoded); err != nil {
			return nil
		}
		return decoded
	case map[string]any:
		return typed
	case nil:
		return nil
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return nil
		}
		var decoded map[string]any
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			return nil
		}
		return decoded
	}
}

// argString reads a string field (`str` upstream): a non-string JSON value is
// "invalid", a missing/null field is "".
// argString reads the first present string field (upstream's
// `str(args?.a ?? args?.b)`): a present non-string value is invalid, all-missing
// is "".
func argString(args map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		value, ok := args[key]
		if !ok || value == nil {
			continue
		}
		if text, ok := value.(string); ok {
			return text, true
		}
		return "", false
	}
	return "", true
}

// argNumber reads a numeric field.
func argNumber(args map[string]any, key string) (float64, bool) {
	value, ok := args[key]
	if !ok || value == nil {
		return 0, false
	}
	if number, ok := value.(float64); ok {
		return number, true
	}
	return 0, false
}

func argStringList(args map[string]any, key string) []string {
	value, ok := args[key]
	if !ok {
		return nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, entry := range list {
		if text, ok := entry.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func invalidArgText(theme *Theme) string { return theme.Fg("error", "[invalid arg]") }

func normalizeDisplayText(text string) string { return strings.ReplaceAll(text, "\r", "") }

func trimTrailingEmptyLines(lines []string) []string {
	end := len(lines)
	for end > 0 && lines[end-1] == "" {
		end--
	}
	return lines[:end]
}

// toolTextComponent reuses the last rendered component when it is a Text.
func toolTextComponent(context *ToolRenderContext) *tui.Text {
	if context != nil {
		if text, ok := context.LastComponent.(*tui.Text); ok {
			return text
		}
	}
	return tui.NewText("", 0, 0, nil)
}

func toolContainerComponent(context *ToolRenderContext) *tui.Container {
	if context != nil {
		if container, ok := context.LastComponent.(*tui.Container); ok {
			return container
		}
	}
	return &tui.Container{}
}

// linkPath wraps a styled path in an OSC 8 hyperlink (upstream checks the
// terminal's hyperlink capability).
func linkPath(styledText string, rawPath string, cwd string) string {
	absolute := coding.ResolveToCwd(rawPath, cwd)
	return tui.Hyperlink(styledText, "file://"+absolute)
}

// --- read -------------------------------------------------------------------

type compactReadClassification struct {
	kind  string // "docs" | "resource" | "skill"
	label string
}

var compactResourceFileNames = map[string]bool{
	"AGENTS.override.md": true, "AGENTS.md": true, "AGENTS.MD": true,
	"CLAUDE.md": true, "CLAUDE.MD": true,
}

func formatReadLineRange(args map[string]any, theme *Theme) string {
	offset, hasOffset := argNumber(args, "offset")
	limit, hasLimit := argNumber(args, "limit")
	if !hasOffset && !hasLimit {
		return ""
	}
	startLine := 1
	if hasOffset {
		startLine = int(offset)
	}
	if hasLimit {
		endLine := startLine + int(limit) - 1
		return theme.Fg("warning", fmt.Sprintf(":%d-%d", startLine, endLine))
	}
	return theme.Fg("warning", fmt.Sprintf(":%d", startLine))
}

func formatReadCall(args map[string]any, theme *Theme, cwd string) string {
	rawPath, _ := argString(args, "file_path", "path")
	pathDisplay := RenderToolPath(&rawPath, theme, cwd, "")
	return theme.Fg("toolTitle", theme.Bold("read")) + " " + pathDisplay + formatReadLineRange(args, theme)
}

func toPosixPath(filePath string) string {
	return filepath.ToSlash(filePath)
}

// formatPathRelativeToCwdOrAbsolute shows a cwd-relative path when the file is
// inside the cwd, otherwise the absolute path (upstream utils/paths.ts).
func formatPathRelativeToCwdOrAbsolute(filePath string, cwd string) string {
	relativePath, inside := coding.GetCwdRelativePath(filePath, cwd)
	if inside && relativePath != "" {
		return filepath.ToSlash(relativePath)
	}
	return filepath.ToSlash(filePath)
}

func getPiDocsClassification(absolutePath string) *compactReadClassification {
	packageRoot := filepath.Dir(coding.GetReadmePath())
	relativePath, err := filepath.Rel(packageRoot, absolutePath)
	if err != nil {
		return nil
	}
	if relativePath == "." || relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relativePath) {
		return nil
	}
	label := toPosixPath(relativePath)
	if label == "README.md" || strings.HasPrefix(label, "docs/") || strings.HasPrefix(label, "examples/") {
		return &compactReadClassification{kind: "docs", label: label}
	}
	return nil
}

func getCompactReadClassification(args map[string]any, cwd string) *compactReadClassification {
	rawPath, ok := argString(args, "file_path", "path")
	if !ok || rawPath == "" {
		return nil
	}
	absolutePath := coding.ResolveToCwd(rawPath, cwd)
	fileName := filepath.Base(absolutePath)
	if fileName == "SKILL.md" {
		label := filepath.Base(filepath.Dir(absolutePath))
		if label == "" || label == "." || label == string(filepath.Separator) {
			label = fileName
		}
		return &compactReadClassification{kind: "skill", label: label}
	}
	if docs := getPiDocsClassification(absolutePath); docs != nil {
		return docs
	}
	if compactResourceFileNames[fileName] {
		return &compactReadClassification{kind: "resource", label: formatPathRelativeToCwdOrAbsolute(absolutePath, cwd)}
	}
	return nil
}

func formatCompactReadCall(classification compactReadClassification, args map[string]any, theme *Theme) string {
	expandHint := theme.Fg("dim", " ("+KeyDisplayText("app.tools.expand")+" to expand)")
	if classification.kind == "skill" {
		return theme.Fg("customMessageLabel", "\x1b[1m[skill]\x1b[22m ") +
			theme.Fg("customMessageText", classification.label) +
			formatReadLineRange(args, theme) + expandHint
	}
	return theme.Fg("toolTitle", theme.Bold("read "+classification.kind)) +
		" " + theme.Fg("accent", classification.label) +
		formatReadLineRange(args, theme) + expandHint
}

func formatReadResult(args map[string]any, result *SortToolResultContent, options ToolRenderResultOptions,
	theme *Theme, showImages bool, isError bool) string {
	if !options.Expanded && !isError {
		return ""
	}
	rawPath, _ := argString(args, "file_path", "path")
	output := GetTextOutput(result, showImages)
	lang := ""
	hasLang := false
	if !isError && rawPath != "" {
		lang, hasLang = GetLanguageFromPath(rawPath)
	}
	var lines []string
	if hasLang {
		lines = HighlightCode(replaceTabs(output), lang)
	} else {
		lines = strings.Split(output, "\n")
	}
	lines = trimTrailingEmptyLines(lines)
	maxLines := 10
	if options.Expanded {
		maxLines = len(lines)
	}
	displayLines := lines
	if len(displayLines) > maxLines {
		displayLines = displayLines[:maxLines]
	}
	remaining := len(lines) - len(displayLines)
	parts := make([]string, 0, len(displayLines))
	for _, line := range displayLines {
		if hasLang {
			parts = append(parts, replaceTabs(line))
		} else {
			parts = append(parts, theme.Fg("toolOutput", replaceTabs(line)))
		}
	}
	text := "\n" + strings.Join(parts, "\n")
	if remaining > 0 {
		text += theme.Fg("muted", fmt.Sprintf("\n... (%d more lines,", remaining)) +
			" " + KeyHint("app.tools.expand", "to expand") + theme.Fg("muted", ")")
	}
	if details, ok := result.Details.(*coding.ReadToolDetails); ok && details.Truncation != nil {
		truncation := details.Truncation
		if truncation.Truncated {
			if truncation.FirstLineExceedsLimit {
				text += "\n" + theme.Fg("warning",
					fmt.Sprintf("[First line exceeds %s limit]", coding.FormatSize(int64(coding.DefaultMaxBytes))))
			} else if truncation.TruncatedBy == "lines" {
				text += "\n" + theme.Fg("warning", fmt.Sprintf(
					"[Truncated: showing %d of %d lines (%d line limit)]",
					truncation.OutputLines, truncation.TotalLines, coding.DefaultMaxLines))
			} else {
				text += "\n" + theme.Fg("warning", fmt.Sprintf(
					"[Truncated: %d lines shown (%s limit)]", truncation.OutputLines,
					coding.FormatSize(int64(coding.DefaultMaxBytes))))
			}
		}
	}
	return text
}

var readRenderers = ToolRenderers{
	RenderCall: func(args any, theme *Theme, context *ToolRenderContext) tui.Component {
		decoded := toolArgs(args)
		text := toolTextComponent(context)
		classification := (*compactReadClassification)(nil)
		if context != nil && !context.Expanded {
			classification = getCompactReadClassification(decoded, context.Cwd)
		}
		if classification != nil {
			text.SetText(formatCompactReadCall(*classification, decoded, theme))
		} else {
			text.SetText(formatReadCall(decoded, theme, context.Cwd))
		}
		return text
	},
	RenderResult: func(result *SortToolResultContent, options ToolRenderResultOptions, theme *Theme, context *ToolRenderContext) tui.Component {
		text := toolTextComponent(context)
		text.SetText(formatReadResult(toolArgs(context.Args), result, options, theme, context.ShowImages, context.IsError))
		return text
	},
}

// --- ls ---------------------------------------------------------------------

func formatLsCall(args map[string]any, theme *Theme, cwd string) string {
	limit, hasLimit := argNumber(args, "limit")
	rawPath, _ := argString(args, "path")
	pathDisplay := RenderToolPath(&rawPath, theme, cwd, ".")
	text := theme.Fg("toolTitle", theme.Bold("ls")) + " " + pathDisplay
	if hasLimit {
		text += theme.Fg("toolOutput", fmt.Sprintf(" (limit %d)", int(limit)))
	}
	return text
}

func appendTruncatedLines(text string, output string, maxLines int, theme *Theme) string {
	if output == "" {
		return text
	}
	lines := strings.Split(output, "\n")
	displayLines := lines
	if len(displayLines) > maxLines {
		displayLines = displayLines[:maxLines]
	}
	remaining := len(lines) - len(displayLines)
	parts := make([]string, 0, len(displayLines))
	for _, line := range displayLines {
		parts = append(parts, theme.Fg("toolOutput", line))
	}
	text += "\n" + strings.Join(parts, "\n")
	if remaining > 0 {
		text += theme.Fg("muted", fmt.Sprintf("\n... (%d more lines,", remaining)) +
			" " + KeyHint("app.tools.expand", "to expand") + theme.Fg("muted", ")")
	}
	return text
}

func toolTruncationWarnings(details any) (matchLimit string, truncated bool, maxBytes int, extra []string) {
	switch typed := details.(type) {
	case *coding.GrepToolDetails:
		if typed.MatchLimitReached != nil {
			matchLimit = fmt.Sprintf("%d matches limit", *typed.MatchLimitReached)
		}
		if typed.Truncation != nil && typed.Truncation.Truncated {
			truncated = true
			maxBytes = typed.Truncation.MaxBytes
		}
		if typed.LinesTruncated != nil && *typed.LinesTruncated {
			extra = append(extra, "some lines truncated")
		}
	case *coding.FindToolDetails:
		if typed.ResultLimitReached != nil {
			matchLimit = fmt.Sprintf("%d results limit", *typed.ResultLimitReached)
		}
		if typed.Truncation != nil && typed.Truncation.Truncated {
			truncated = true
			maxBytes = typed.Truncation.MaxBytes
		}
	case *coding.LsToolDetails:
		if typed.EntryLimitReached != nil {
			matchLimit = fmt.Sprintf("%d entries limit", *typed.EntryLimitReached)
		}
		if typed.Truncation != nil && typed.Truncation.Truncated {
			truncated = true
			maxBytes = typed.Truncation.MaxBytes
		}
	}
	return matchLimit, truncated, maxBytes, extra
}

func formatSearchResult(result *SortToolResultContent, options ToolRenderResultOptions, theme *Theme,
	showImages bool, maxLines int) string {
	output := strings.TrimSpace(GetTextOutput(result, showImages))
	text := appendTruncatedLines("", output, maxLines, theme)
	matchLimit, truncated, maxBytes, extra := toolTruncationWarnings(result.Details)
	if matchLimit != "" || truncated || len(extra) > 0 {
		warnings := make([]string, 0, 3)
		if matchLimit != "" {
			warnings = append(warnings, matchLimit)
		}
		if truncated {
			bytes := int64(maxBytes)
			if bytes <= 0 {
				bytes = int64(coding.DefaultMaxBytes)
			}
			warnings = append(warnings, coding.FormatSize(bytes)+" limit")
		}
		warnings = append(warnings, extra...)
		text += "\n" + theme.Fg("warning", "[Truncated: "+strings.Join(warnings, ", ")+"]")
	}
	return text
}

// --- grep / find ------------------------------------------------------------

func formatGrepCall(args map[string]any, theme *Theme) string {
	pattern, patternOK := argString(args, "pattern")
	rawPath, pathOK := argString(args, "path")
	glob, _ := argString(args, "glob")
	limit, hasLimit := argNumber(args, "limit")
	invalidArg := invalidArgText(theme)
	path := "."
	if !pathOK {
		path = ""
	} else if rawPath != "" {
		path = ShortenPath(rawPath)
	}
	text := theme.Fg("toolTitle", theme.Bold("grep")) + " "
	if !patternOK {
		text += invalidArg
	} else {
		text += theme.Fg("accent", "/"+pattern+"/")
	}
	shownPath := path
	if !pathOK {
		shownPath = ""
		text += theme.Fg("toolOutput", " in "+invalidArg)
	} else {
		text += theme.Fg("toolOutput", " in "+shownPath)
	}
	if glob != "" {
		text += theme.Fg("toolOutput", " ("+glob+")")
	}
	if hasLimit {
		text += theme.Fg("toolOutput", fmt.Sprintf(" limit %d", int(limit)))
	}
	return text
}

var grepRenderers = ToolRenderers{
	RenderCall: func(args any, theme *Theme, context *ToolRenderContext) tui.Component {
		text := toolTextComponent(context)
		text.SetText(formatGrepCall(toolArgs(args), theme))
		return text
	},
	RenderResult: func(result *SortToolResultContent, options ToolRenderResultOptions, theme *Theme, context *ToolRenderContext) tui.Component {
		text := toolTextComponent(context)
		text.SetText(formatSearchResult(result, options, theme, context.ShowImages, 15))
		return text
	},
}

func formatFindCall(args map[string]any, theme *Theme) string {
	pattern, patternOK := argString(args, "pattern")
	rawPath, pathOK := argString(args, "path")
	limit, hasLimit := argNumber(args, "limit")
	invalidArg := invalidArgText(theme)
	text := theme.Fg("toolTitle", theme.Bold("find")) + " "
	if !patternOK {
		text += invalidArg
	} else {
		text += theme.Fg("accent", pattern)
	}
	if !pathOK {
		text += theme.Fg("toolOutput", " in "+invalidArg)
	} else {
		shown := "."
		if rawPath != "" {
			shown = ShortenPath(rawPath)
		}
		text += theme.Fg("toolOutput", " in "+shown)
	}
	if hasLimit {
		text += theme.Fg("toolOutput", fmt.Sprintf(" (limit %d)", int(limit)))
	}
	return text
}

var findRenderers = ToolRenderers{
	RenderCall: func(args any, theme *Theme, context *ToolRenderContext) tui.Component {
		text := toolTextComponent(context)
		text.SetText(formatFindCall(toolArgs(args), theme))
		return text
	},
	RenderResult: func(result *SortToolResultContent, options ToolRenderResultOptions, theme *Theme, context *ToolRenderContext) tui.Component {
		text := toolTextComponent(context)
		text.SetText(formatSearchResult(result, options, theme, context.ShowImages, 20))
		return text
	},
}

var lsRenderers = ToolRenderers{
	RenderCall: func(args any, theme *Theme, context *ToolRenderContext) tui.Component {
		text := toolTextComponent(context)
		text.SetText(formatLsCall(toolArgs(args), theme, context.Cwd))
		return text
	},
	RenderResult: func(result *SortToolResultContent, options ToolRenderResultOptions, theme *Theme, context *ToolRenderContext) tui.Component {
		text := toolTextComponent(context)
		output := strings.TrimSpace(GetTextOutput(result, context.ShowImages))
		rendered := appendTruncatedLines("", output, 20, theme)
		text.SetText(rendered + truncationSuffix(result.Details, theme))
		return text
	},
}

func truncationSuffix(details any, theme *Theme) string {
	matchLimit, truncated, maxBytes, extra := toolTruncationWarnings(details)
	if matchLimit == "" && !truncated && len(extra) == 0 {
		return ""
	}
	warnings := make([]string, 0, 3)
	if matchLimit != "" {
		warnings = append(warnings, matchLimit)
	}
	if truncated {
		bytes := int64(maxBytes)
		if bytes <= 0 {
			bytes = int64(coding.DefaultMaxBytes)
		}
		warnings = append(warnings, coding.FormatSize(bytes)+" limit")
	}
	warnings = append(warnings, extra...)
	return "\n" + theme.Fg("warning", "[Truncated: "+strings.Join(warnings, ", ")+"]")
}

// --- bash / powershell ------------------------------------------------------

func formatDuration(ms float64) string {
	seconds := ms / 1000
	if seconds < 60 {
		return fmt.Sprintf("%.1fs", seconds)
	}
	totalSeconds := int(seconds)
	minutes := totalSeconds / 60
	remainder := totalSeconds % 60
	if minutes < 60 {
		return fmt.Sprintf("%dm %ds", minutes, remainder)
	}
	return fmt.Sprintf("%dh %dm %ds", minutes/60, minutes, remainder)
}

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

func bashStateFor(context *ToolRenderContext) *bashResultState {
	if context == nil {
		return &bashResultState{}
	}
	if state, ok := context.State.(*bashResultState); ok {
		return state
	}
	state := &bashResultState{}
	context.State = state
	return state
}

func rebuildBashResult(result *SortToolResultContent, options ToolRenderResultOptions, theme *Theme,
	showImages bool, state *bashResultState) []tui.Component {
	children := make([]tui.Component, 0, 3)
	output := strings.TrimSpace(GetTextOutput(result, showImages))
	var truncation *coding.TruncationResult
	var fullOutputPath string
	if details, ok := result.Details.(*coding.BashToolDetails); ok {
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
			if state.startedAtMS != 0 && options.IsPartial && state.endedAtMS == 0 {
				state.endedAtMS = time.Now().UnixMilli()
			}
			container := &tui.Container{}
			if context != nil {
				if existing, ok := context.LastComponent.(*tui.Container); ok {
					container = existing
				}
			}
			container.Clear()
			for _, child := range rebuildBashResult(result, options, theme, context.ShowImages, bashStateFor(context)) {
				container.AddChild(child)
			}
			if state.startedAtMS != 0 {
				label := "Took"
				if options.IsPartial {
					label = "Elapsed"
				}
				end := state.endedAtMS
				if end == 0 {
					end = time.Now().UnixMilli()
				}
				container.AddChild(tui.NewText("\n"+theme.Fg("muted",
					fmt.Sprintf("%s %s", label, formatDuration(float64(end-state.startedAtMS)))), 0, 0, nil))
			}
			return container
		},
	}
}

type shellCallState struct {
	startedAtMS int64
	endedAtMS   int64
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

type writeHighlightCache struct {
	rawPath          string
	hasRawPath       bool
	lang             string
	rawContent       string
	normalizedLines  []string
	highlightedLines []string
}

func highlightSingleLine(line string, lang string) string {
	highlighted := HighlightCode(line, lang)
	if len(highlighted) > 0 {
		return highlighted[0]
	}
	return ""
}

func rebuildWriteHighlightCacheFull(rawPath string, hasRawPath bool, fileContent string) *writeHighlightCache {
	if !hasRawPath {
		return nil
	}
	lang, ok := GetLanguageFromPath(rawPath)
	if !ok {
		return nil
	}
	normalized := replaceTabs(normalizeDisplayText(fileContent))
	return &writeHighlightCache{
		rawPath: rawPath, hasRawPath: true, lang: lang, rawContent: fileContent,
		normalizedLines:  strings.Split(normalized, "\n"),
		highlightedLines: HighlightCode(normalized, lang),
	}
}

func updateWriteHighlightCacheIncremental(cache *writeHighlightCache, rawPath string, hasRawPath bool,
	fileContent string) *writeHighlightCache {
	if !hasRawPath {
		return nil
	}
	lang, ok := GetLanguageFromPath(rawPath)
	if !ok {
		return nil
	}
	if cache == nil {
		return rebuildWriteHighlightCacheFull(rawPath, hasRawPath, fileContent)
	}
	if cache.lang != lang || cache.rawPath != rawPath {
		return rebuildWriteHighlightCacheFull(rawPath, hasRawPath, fileContent)
	}
	if !strings.HasPrefix(fileContent, cache.rawContent) {
		return rebuildWriteHighlightCacheFull(rawPath, hasRawPath, fileContent)
	}
	if len(fileContent) == len(cache.rawContent) {
		return cache
	}
	delta := replaceTabs(normalizeDisplayText(fileContent[len(cache.rawContent):]))
	cache.rawContent = fileContent
	if len(cache.normalizedLines) == 0 {
		cache.normalizedLines = append(cache.normalizedLines, "")
		cache.highlightedLines = append(cache.highlightedLines, "")
	}
	segments := strings.Split(delta, "\n")
	lastIndex := len(cache.normalizedLines) - 1
	cache.normalizedLines[lastIndex] += segments[0]
	cache.highlightedLines[lastIndex] = highlightSingleLine(cache.normalizedLines[lastIndex], cache.lang)
	for i := 1; i < len(segments); i++ {
		cache.normalizedLines = append(cache.normalizedLines, segments[i])
		cache.highlightedLines = append(cache.highlightedLines, highlightSingleLine(segments[i], cache.lang))
	}
	prefixCount := writePartialFullHighlightLines
	if prefixCount > len(cache.normalizedLines) {
		prefixCount = len(cache.normalizedLines)
	}
	if prefixCount > 0 {
		prefixSource := strings.Join(cache.normalizedLines[:prefixCount], "\n")
		prefixHighlighted := HighlightCode(prefixSource, cache.lang)
		for i := 0; i < prefixCount; i++ {
			if i < len(prefixHighlighted) {
				cache.highlightedLines[i] = prefixHighlighted[i]
			} else {
				cache.highlightedLines[i] = highlightSingleLine(cache.normalizedLines[i], cache.lang)
			}
		}
	}
	return cache
}

func formatWriteCall(args map[string]any, options ToolRenderResultOptions, theme *Theme,
	cache *writeHighlightCache, cwd string) string {
	rawPath, _ := argString(args, "file_path", "path")
	fileContent, hasContent := argString(args, "content")
	pathDisplay := RenderToolPath(&rawPath, theme, cwd, "")
	text := theme.Fg("toolTitle", theme.Bold("write")) + " " + pathDisplay
	if !hasContent {
		text += "\n\n" + theme.Fg("error", "[invalid content arg - expected string]")
	} else if fileContent != "" {
		lang, hasLang := GetLanguageFromPath(rawPath)
		var renderedLines []string
		if hasLang && cache != nil {
			renderedLines = cache.highlightedLines
		} else if hasLang {
			renderedLines = HighlightCode(replaceTabs(normalizeDisplayText(fileContent)), lang)
		} else {
			renderedLines = strings.Split(normalizeDisplayText(fileContent), "\n")
		}
		lines := trimTrailingEmptyLines(renderedLines)
		totalLines := len(lines)
		maxLines := 10
		if options.Expanded {
			maxLines = len(lines)
		}
		displayLines := lines
		if len(displayLines) > maxLines {
			displayLines = displayLines[:maxLines]
		}
		remaining := len(lines) - len(displayLines)
		parts := make([]string, 0, len(displayLines))
		for _, line := range displayLines {
			if hasLang {
				parts = append(parts, line)
			} else {
				parts = append(parts, theme.Fg("toolOutput", replaceTabs(line)))
			}
		}
		text += "\n\n" + strings.Join(parts, "\n")
		if remaining > 0 {
			text += theme.Fg("muted", fmt.Sprintf("\n... (%d more lines, %d total,", remaining, totalLines)) +
				" " + KeyHint("app.tools.expand", "to expand") + theme.Fg("muted", ")")
		}
	}
	return text
}

type writeCallComponent struct {
	*tui.Text
	cache *writeHighlightCache
}

var writeRenderers = ToolRenderers{
	RenderCall: func(args any, theme *Theme, context *ToolRenderContext) tui.Component {
		decoded := toolArgs(args)
		rawPath, hasRawPath := argString(decoded, "file_path", "path")
		fileContent, hasContent := argString(decoded, "content")
		var component *writeCallComponent
		if existing, ok := context.LastComponent.(*writeCallComponent); ok {
			component = existing
		} else {
			component = &writeCallComponent{Text: tui.NewText("", 0, 0, nil)}
		}
		if hasContent {
			if context.ArgsComplete {
				component.cache = rebuildWriteHighlightCacheFull(rawPath, hasRawPath, fileContent)
			} else {
				component.cache = updateWriteHighlightCacheIncremental(component.cache, rawPath, hasRawPath, fileContent)
			}
		} else {
			component.cache = nil
		}
		component.SetText(formatWriteCall(decoded, ToolRenderResultOptions{
			Expanded: context.Expanded, IsPartial: context.IsPartial,
		}, theme, component.cache, context.Cwd))
		return component
	},
	RenderResult: func(result *SortToolResultContent, options ToolRenderResultOptions, theme *Theme, context *ToolRenderContext) tui.Component {
		output := ""
		if context.IsError {
			var blocks []string
			for _, block := range result.Content {
				if block.Type == "text" && block.Text != "" {
					blocks = append(blocks, block.Text)
				}
			}
			if len(blocks) > 0 {
				output = "\n" + theme.Fg("error", strings.Join(blocks, "\n"))
			}
		}
		if output == "" {
			component := toolContainerComponent(context)
			component.Clear()
			return component
		}
		text := toolTextComponent(context)
		text.SetText(output)
		return text
	},
}

// --- edit -------------------------------------------------------------------

type editRenderArgs struct {
	Path    string
	HasPath bool
	Edits   []coding.Edit
}

// getRenderablePreviewInput extracts the path/edits pair that can be previewed.
func getRenderablePreviewInput(args any) *editRenderArgs {
	decoded := toolArgs(args)
	if decoded == nil {
		return nil
	}
	path, hasPath := argString(decoded, "path", "file_path")
	if !hasPath || path == "" {
		return nil
	}
	edits := parseEditArgs(decoded)
	if len(edits) == 0 {
		return nil
	}
	return &editRenderArgs{Path: path, HasPath: hasPath, Edits: edits}
}

func parseEditArgs(decoded map[string]any) []coding.Edit {
	var edits []coding.Edit
	if rawList, ok := decoded["edits"].([]any); ok && len(rawList) > 0 {
		for _, entry := range rawList {
			editMap, ok := entry.(map[string]any)
			if !ok {
				return nil
			}
			oldText, oldOK := editMap["oldText"].(string)
			newText, newOK := editMap["newText"].(string)
			if !oldOK || !newOK {
				return nil
			}
			edits = append(edits, coding.Edit{OldText: oldText, NewText: newText})
		}
		return edits
	}
	oldText, oldOK := decoded["oldText"].(string)
	newText, newOK := decoded["newText"].(string)
	if oldOK && newOK {
		return []coding.Edit{{OldText: oldText, NewText: newText}}
	}
	return nil
}

func formatEditCall(args *editRenderArgs, theme *Theme, cwd string) string {
	path := ""
	if args != nil {
		path = args.Path
	}
	pathDisplay := RenderToolPath(&path, theme, cwd, "")
	return theme.Fg("toolTitle", theme.Bold("edit")) + " " + pathDisplay
}

func getEditHeaderBg(preview *editPreview, settledError bool, theme *Theme) func(string) string {
	if preview != nil {
		if preview.Error != "" {
			return func(text string) string { return theme.Bg("toolErrorBg", text) }
		}
		return func(text string) string { return theme.Bg("toolSuccessBg", text) }
	}
	if settledError {
		return func(text string) string { return theme.Bg("toolErrorBg", text) }
	}
	return func(text string) string { return theme.Bg("toolPendingBg", text) }
}

type editPreview struct {
	Diff             string
	FirstChangedLine *int
	Error            string
}

type editCallComponent struct {
	*tui.Box
	preview        *editPreview
	previewArgsKey string
	previewPending bool
	settledError   bool
	builtArgsKey   string
}

func (c *editCallComponent) build(args *editRenderArgs, theme *Theme, cwd string) {
	c.SetBgFn(getEditHeaderBg(c.preview, c.settledError, theme))
	c.Clear()
	c.AddChild(tui.NewText(formatEditCall(args, theme, cwd), 0, 0, nil))
	if c.preview == nil {
		return
	}
	body := ""
	if c.preview.Error != "" {
		body = theme.Fg("error", c.preview.Error)
	} else {
		body = RenderDiff(c.preview.Diff, RenderDiffOptions{})
	}
	c.AddChild(tui.NewSpacer(1))
	c.AddChild(tui.NewText(body, 0, 0, nil))
}

func argsKeyFor(args *editRenderArgs) string {
	if args == nil {
		return ""
	}
	encoded, err := json.Marshal(map[string]any{"path": args.Path, "edits": args.Edits})
	if err != nil {
		return ""
	}
	return string(encoded)
}

var editRenderers = ToolRenderers{
	RenderCall: func(args any, theme *Theme, context *ToolRenderContext) tui.Component {
		var component *editCallComponent
		if box, ok := context.LastComponent.(*editCallComponent); ok {
			component = box
		} else if box, ok := context.LastComponent.(*tui.Box); ok {
			component = &editCallComponent{Box: box}
		} else if state, ok := context.State.(*editCallComponent); ok {
			component = state
		} else {
			component = &editCallComponent{Box: tui.NewBox(1, 1, nil)}
			context.State = component
		}
		previewInput := getRenderablePreviewInput(args)
		argsKey := argsKeyFor(previewInput)
		if component.previewArgsKey != argsKey {
			component.preview = nil
			component.previewArgsKey = argsKey
			component.previewPending = false
			component.settledError = false
		}
		if context.ArgsComplete && previewInput != nil && component.preview == nil && !component.previewPending {
			component.previewPending = true
			request := previewInput
			requestKey := argsKey
			cwd := context.Cwd
			invalidate := context.Invalidate
			go func() {
				preview := computeEditsPreview(request.Path, request.Edits, cwd)
				if component.previewArgsKey == requestKey {
					component.preview = preview
					component.previewPending = false
					if invalidate != nil {
						invalidate()
					}
				}
			}()
		}
		component.build(previewInput, theme, context.Cwd)
		return component
	},
	RenderResult: func(result *SortToolResultContent, options ToolRenderResultOptions, theme *Theme, context *ToolRenderContext) tui.Component {
		callComponent, _ := context.State.(*editCallComponent)
		previewInput := getRenderablePreviewInput(context.Args)
		argsKey := argsKeyFor(previewInput)
		var resultDiff string
		var firstChangedLine *int
		if !context.IsError {
			if details, ok := result.Details.(*coding.EditToolDetails); ok {
				resultDiff = details.Diff
				firstChangedLine = details.FirstChangedLine
			}
		}
		if callComponent != nil {
			changed := false
			if resultDiff != "" {
				callComponent.preview = &editPreview{Diff: resultDiff, FirstChangedLine: firstChangedLine}
				callComponent.previewArgsKey = argsKey
				callComponent.previewPending = false
				changed = true
			}
			if callComponent.settledError != context.IsError {
				callComponent.settledError = context.IsError
				changed = true
			}
			if changed {
				callComponent.build(previewInput, theme, context.Cwd)
			}
		}
		output := formatEditResult(previewInput, callComponent, result, theme, context.IsError)
		component := toolContainerComponent(context)
		component.Clear()
		if output == "" {
			return component
		}
		component.AddChild(tui.NewSpacer(1))
		component.AddChild(tui.NewText(output, 1, 0, nil))
		return component
	},
}

func formatEditResult(args *editRenderArgs, callComponent *editCallComponent, result *SortToolResultContent,
	theme *Theme, isError bool) string {
	rawPath := ""
	if args != nil {
		rawPath = args.Path
	}
	var previewDiff string
	var previewError string
	if callComponent != nil && callComponent.preview != nil {
		previewDiff = callComponent.preview.Diff
		previewError = callComponent.preview.Error
	}
	if isError {
		var blocks []string
		for _, block := range result.Content {
			if block.Type == "text" && block.Text != "" {
				blocks = append(blocks, block.Text)
			}
		}
		errorText := strings.Join(blocks, "\n")
		if errorText == "" || errorText == previewError {
			return ""
		}
		return theme.Fg("error", errorText)
	}
	var resultDiff string
	if details, ok := result.Details.(*coding.EditToolDetails); ok {
		resultDiff = details.Diff
	}
	if resultDiff != "" && resultDiff != previewDiff {
		return RenderDiff(resultDiff, RenderDiffOptions{FilePath: rawPath})
	}
	return ""
}

// computeEditsPreview mirrors computeEditsDiff: read the file, apply the edits
// to the normalized content, and diff (Go is synchronous).
func computeEditsPreview(path string, edits []coding.Edit, cwd string) *editPreview {
	absolutePath := coding.ResolveToCwd(path, cwd)
	rawContent, err := os.ReadFile(absolutePath)
	if err != nil {
		return &editPreview{Error: fmt.Sprintf("Could not edit file: %s. %v.", path, err)}
	}
	_, content := coding.SplitBom(string(rawContent))
	normalizedContent := coding.NormalizeToLF(content)
	result, err := coding.ApplyEditsToNormalizedContent(normalizedContent, edits, path)
	if err != nil {
		return &editPreview{Error: err.Error()}
	}
	diff, firstChangedLine, ok := coding.GenerateDiffString(result.BaseContent, result.NewContent, 3)
	if !ok {
		return &editPreview{Diff: "", FirstChangedLine: nil}
	}
	return &editPreview{Diff: diff, FirstChangedLine: &firstChangedLine}
}

// --- registry ---------------------------------------------------------------

// builtinToolRenderers are the renderers for every built-in tool, keyed by name.
var builtinToolRenderers = map[string]ToolRenderers{
	"read":       readRenderers,
	"bash":       bashRenderers,
	"powershell": powershellRenderers,
	"edit":       editRenderers,
	"write":      writeRenderers,
	"grep":       grepRenderers,
	"find":       findRenderers,
	"ls":         lsRenderers,
}

// BuiltinToolRendererNames lists the tools with built-in renderers.
func BuiltinToolRendererNames() []string {
	names := make([]string, 0, len(builtinToolRenderers))
	for name := range builtinToolRenderers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// WithBuiltInRenderers merges the built-in renderers into a tool definition
// that does not supply its own (upstream withBuiltInRenderers).
func WithBuiltInRenderers(toolName string, definition *ToolRenderers) *ToolRenderers {
	builtIn, ok := builtinToolRenderers[toolName]
	if !ok {
		return definition
	}
	if definition == nil {
		merged := builtIn
		return &merged
	}
	merged := *definition
	if merged.RenderCall == nil {
		merged.RenderCall = builtIn.RenderCall
	}
	if merged.RenderResult == nil {
		merged.RenderResult = builtIn.RenderResult
	}
	return &merged
}
