package interactive

import (
	"fmt"
	"strings"

	"github.com/dat267/pier/tui"
)

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
