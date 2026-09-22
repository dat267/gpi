package interactive

import (
	"sort"
)

// Port of core/tools/renderers/{index,read,bash,edit,write,grep,find,ls}.ts:
// the built-in tool presentation, keyed by tool name. Renderers live apart
// from the tool implementations so a display-only process does not load the
// execution path.
//
// D133 previously returned nil here (raw-JSON fallback); the built-in
// renderers are now ported, so `withBuiltInRenderers` behaves like upstream.

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
