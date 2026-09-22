package interactive

import (
	"strings"

	"github.com/dat267/pier/tui"
)

// Port of the theme-derived factories from
// src/modes/interactive/theme/theme.ts (getMarkdownTheme, getSelectListTheme,
// getEditorTheme, getSettingsListTheme, getResolvedThemeColors,
// isLightTheme, getLanguageFromPath, highlightCode).
//
// The syntax highlighter (highlight.js) is out of scope (D74): HighlightCode
// always uses the per-line mdCodeBlock fallback, which is upstream's behaviour
// for unsupported languages.

// ActiveTheme returns the active theme; it panics when no theme is
// initialized (upstream's Proxy throws). Named ActiveTheme because Theme is
// the type (divergence D85).
func ActiveTheme() *Theme {
	theme := CurrentTheme()
	if theme == nil {
		panic("Theme not initialized. Call InitTheme() first.")
	}
	return theme
}

// GetMarkdownTheme builds the markdown theme from the active theme.
func GetMarkdownTheme() tui.MarkdownTheme {
	theme := ActiveTheme()
	return tui.MarkdownTheme{
		Heading:         func(text string) string { return theme.Fg("mdHeading", text) },
		Link:            func(text string) string { return theme.Fg("mdLink", text) },
		LinkURL:         func(text string) string { return theme.Fg("mdLinkUrl", text) },
		Code:            func(text string) string { return theme.Fg("mdCode", text) },
		CodeBlock:       func(text string) string { return theme.Fg("mdCodeBlock", text) },
		CodeBlockBorder: func(text string) string { return theme.Fg("mdCodeBlockBorder", text) },
		Quote:           func(text string) string { return theme.Fg("mdQuote", text) },
		QuoteBorder:     func(text string) string { return theme.Fg("mdQuoteBorder", text) },
		Hr:              func(text string) string { return theme.Fg("mdHr", text) },
		ListBullet:      func(text string) string { return theme.Fg("mdListBullet", text) },
		Bold:            theme.Bold,
		Italic:          theme.Italic,
		Underline:       theme.Underline,
		Strikethrough:   theme.Strikethrough,
		HighlightCode: func(code string, lang string) []string {
			// Validate the language before highlighting; without a valid
			// language the code block is styled per line (upstream's
			// fallback, and the Go port's only path: D74).
			return HighlightCode(code, lang)
		},
	}
}

// GetSelectListTheme builds the select-list theme.
func GetSelectListTheme() tui.SelectListTheme {
	theme := ActiveTheme()
	return tui.SelectListTheme{
		SelectedPrefix: func(text string) string { return theme.Fg("accent", text) },
		SelectedText:   func(text string) string { return theme.Fg("accent", text) },
		Description:    func(text string) string { return theme.Fg("muted", text) },
		ScrollInfo:     func(text string) string { return theme.Fg("muted", text) },
		NoMatch:        func(text string) string { return theme.Fg("muted", text) },
	}
}

// GetEditorTheme builds the editor theme.
func GetEditorTheme() tui.EditorTheme {
	theme := ActiveTheme()
	return tui.EditorTheme{
		BorderColor: func(text string) string { return theme.Fg("borderMuted", text) },
		SelectList:  GetSelectListTheme(),
	}
}

// GetSettingsListTheme builds the settings-list theme.
func GetSettingsListTheme() tui.SettingsListTheme {
	theme := ActiveTheme()
	return tui.SettingsListTheme{
		Label: func(text string, selected bool) string {
			if selected {
				return theme.Fg("accent", text)
			}
			return text
		},
		Value: func(text string, selected bool) string {
			if selected {
				return theme.Fg("accent", text)
			}
			return theme.Fg("muted", text)
		},
		Description: func(text string) string { return theme.Fg("dim", text) },
		Cursor:      theme.Fg("accent", "→ "),
		Hint:        func(text string) string { return theme.Fg("dim", text) },
	}
}

// GetResolvedThemeColors returns CSS-compatible hex colors for HTML export.
func GetResolvedThemeColors(themeName string) (map[string]string, error) {
	name := themeName
	if name == "" {
		name = CurrentThemeName()
	}
	if name == "" {
		name = GetDefaultTheme()
	}
	isLight := name == "light"
	themeJSON, err := loadThemeJSON(name)
	if err != nil {
		return nil, err
	}
	resolved := ResolveThemeColors(withThemeColorFallbacks(themeJSON.Colors), themeJSON.Vars)
	defaultText := "#e5e5e7"
	if isLight {
		defaultText = "#000000"
	}
	cssColors := map[string]string{}
	for key, value := range resolved {
		switch {
		case value.IsIndex:
			cssColors[key] = ansi256ToHex(value.Index)
		case value.Value == "":
			cssColors[key] = defaultText
		default:
			cssColors[key] = value.Value
		}
	}
	return cssColors, nil
}

// IsLightTheme reports whether a theme name is the light theme.
func IsLightTheme(themeName string) bool { return themeName == "light" }

// GetThemeExportColors returns the explicit export colors of a theme
// (upstream getThemeExportColors: var references resolved, palette indexes
// converted to hex).
func GetThemeExportColors(themeName string) (pageBg string, cardBg string, infoBg string, err error) {
	name := themeName
	if name == "" {
		name = CurrentThemeName()
	}
	if name == "" {
		name = GetDefaultTheme()
	}
	themeJSON, themeErr := loadThemeJSON(name)
	if themeErr != nil {
		return "", "", "", themeErr
	}
	if themeJSON.Export == nil {
		return "", "", "", nil
	}
	colorValue := func(value *ColorValue) string {
		if value == nil {
			return ""
		}
		resolved := resolveVarRefs(*value, themeJSON.Vars, map[string]bool{})
		if resolved.IsIndex {
			return ansi256ToHex(resolved.Index)
		}
		if resolved.Value == "" {
			return ""
		}
		return resolved.Value
	}
	return colorValue(themeJSON.Export.PageBg), colorValue(themeJSON.Export.CardBg), colorValue(themeJSON.Export.InfoBg), nil
}

// HighlightCode styles a code block. Syntax highlighting is out of scope
// (D74), so every line is styled with the mdCodeBlock color, matching
// upstream's unsupported-language fallback.
func HighlightCode(code string, lang string) []string {
	theme := ActiveTheme()
	lines := strings.Split(code, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, theme.Fg("mdCodeBlock", line))
	}
	return out
}

// GetLanguageFromPath maps a file extension to a language identifier.
func GetLanguageFromPath(filePath string) (string, bool) {
	index := strings.LastIndex(filePath, ".")
	if index < 0 || index == len(filePath)-1 {
		return "", false
	}
	language, ok := extensionToLanguage[strings.ToLower(filePath[index+1:])]
	return language, ok
}

var extensionToLanguage = map[string]string{
	"ts": "typescript", "tsx": "typescript", "js": "javascript", "jsx": "javascript",
	"mjs": "javascript", "cjs": "javascript", "py": "python", "rb": "ruby", "rs": "rust",
	"go": "go", "java": "java", "kt": "kotlin", "swift": "swift", "c": "c", "h": "c",
	"cpp": "cpp", "cc": "cpp", "cxx": "cpp", "hpp": "cpp", "cs": "csharp", "php": "php",
	"sh": "bash", "bash": "bash", "zsh": "bash", "fish": "fish", "ps1": "powershell",
	"sql": "sql", "html": "html", "htm": "html", "css": "css", "scss": "scss", "sass": "sass",
	"less": "less", "json": "json", "yaml": "yaml", "yml": "yaml", "toml": "toml", "xml": "xml",
	"md": "markdown", "markdown": "markdown", "dockerfile": "dockerfile", "makefile": "makefile",
	"cmake": "cmake", "lua": "lua", "perl": "perl", "r": "r", "scala": "scala", "clj": "clojure",
	"ex": "elixir", "exs": "elixir", "erl": "erlang", "hs": "haskell", "ml": "ocaml", "vim": "vim",
	"graphql": "graphql", "proto": "protobuf", "tf": "hcl", "hcl": "hcl",
}
