package interactive

import (
	"fmt"
	"strings"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

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
