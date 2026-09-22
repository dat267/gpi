package interactive

import (
	"fmt"
	"strings"

	"github.com/dat267/pier/tui"
)

// writePartialFullHighlightLines is the prefix re-highlighted during streaming.
const writePartialFullHighlightLines = 50

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
