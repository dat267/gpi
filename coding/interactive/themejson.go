package interactive

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Port of src/modes/interactive/theme/theme-json.ts: the theme document shape
// and its structural validation.
//
// Upstream validates with typebox (kept out of the palette path to avoid the
// module graph). The Go port validates structurally and reports the same
// missing-color summary; the exact typebox message wording is not reproduced
// (divergence D34).

// ColorValue is a theme color: a hex string ("#rrggbb"), a variable reference
// (a name in "vars"), an empty string (terminal default), or a 256-color index.
type ColorValue struct {
	// Index is set when the JSON value was an integer.
	Index   int
	IsIndex bool
	// Value is the string form (hex, variable name, or "").
	Value string
}

// UnmarshalJSON accepts a string or an integer.
func (c *ColorValue) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, `"`) {
		var value string
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		c.Value = value
		c.IsIndex = false
		return nil
	}
	index := 0
	if err := json.Unmarshal(data, &index); err != nil {
		return fmt.Errorf("theme color must be a string or an integer")
	}
	c.Index = index
	c.IsIndex = true
	return nil
}

// MarshalJSON round-trips the value.
func (c ColorValue) MarshalJSON() ([]byte, error) {
	if c.IsIndex {
		return json.Marshal(c.Index)
	}
	return json.Marshal(c.Value)
}

// ThemeExport is the optional HTML export palette.
type ThemeExport struct {
	PageBg *ColorValue `json:"pageBg,omitempty"`
	CardBg *ColorValue `json:"cardBg,omitempty"`
	InfoBg *ColorValue `json:"infoBg,omitempty"`
}

// ThemeJSON is a validated theme document.
type ThemeJSON struct {
	Schema string                `json:"$schema"`
	Name   string                `json:"name"`
	Vars   map[string]ColorValue `json:"vars"`
	Colors map[string]ColorValue `json:"colors"`
	Export *ThemeExport          `json:"export,omitempty"`
}

// requiredThemeColors are the color tokens every theme must define.
var requiredThemeColors = []string{
	// Core UI.
	"accent", "border", "borderAccent", "borderMuted", "success", "error", "warning",
	"muted", "dim", "text", "thinkingText",
	// Backgrounds & content text.
	"selectedBg", "userMessageBg", "userMessageText", "customMessageBg", "customMessageText",
	"customMessageLabel", "toolPendingBg", "toolSuccessBg", "toolErrorBg", "toolTitle", "toolOutput",
	// Markdown.
	"mdHeading", "mdLink", "mdLinkUrl", "mdCode", "mdCodeBlock", "mdCodeBlockBorder",
	"mdQuote", "mdQuoteBorder", "mdHr", "mdListBullet",
	// Tool diffs.
	"toolDiffAdded", "toolDiffRemoved", "toolDiffContext",
	// Syntax highlighting.
	"syntaxComment", "syntaxKeyword", "syntaxFunction", "syntaxVariable", "syntaxString",
	"syntaxNumber", "syntaxType", "syntaxOperator", "syntaxPunctuation",
	// Thinking level borders.
	"thinkingOff", "thinkingMinimal", "thinkingLow", "thinkingMedium", "thinkingHigh", "thinkingXhigh",
	// Bash mode.
	"bashMode",
}

// optionalThemeColors may be omitted (they fall back to other tokens).
var optionalThemeColors = []string{
	"scrollbarTrack", "scrollbarThumb", "searchMatchText", "searchMatchBg", "thinkingMax",
}

// ValidateThemeJSON validates a theme document and returns it.
func ValidateThemeJSON(label string, raw json.RawMessage) (*ThemeJSON, error) {
	var theme ThemeJSON
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if err := decoder.Decode(&theme); err != nil {
		return nil, fmt.Errorf("Invalid theme %q: expected an object with a \"colors\" map.", label)
	}
	if theme.Colors == nil {
		return nil, fmt.Errorf("Invalid theme %q: expected an object with a \"colors\" map.", label)
	}

	var missing []string
	for _, name := range requiredThemeColors {
		if _, ok := theme.Colors[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		var builder strings.Builder
		fmt.Fprintf(&builder, "Invalid theme %q:\n", label)
		builder.WriteString("\nMissing required color tokens:\n")
		for _, name := range missing {
			builder.WriteString("  - " + name + "\n")
		}
		builder.WriteString("\nPlease add these colors to your theme's \"colors\" object.")
		builder.WriteString("\nSee the built-in themes (dark.json, light.json) for reference values.")
		return nil, fmt.Errorf("%s", builder.String())
	}
	if strings.Contains(theme.Name, "/") {
		return nil, fmt.Errorf("Invalid theme name %q: theme names cannot contain \"/\" because it is reserved for automatic light/dark theme settings.", theme.Name)
	}
	return &theme, nil
}

// ParseThemeJSON validates a document, or accepts it as-is when no validator is
// installed (upstream behaviour for built-in themes).
func ParseThemeJSON(label string, raw json.RawMessage, validate bool) (*ThemeJSON, error) {
	if validate {
		return ValidateThemeJSON(label, raw)
	}
	var theme ThemeJSON
	if err := json.Unmarshal(raw, &theme); err != nil {
		return nil, fmt.Errorf("Invalid theme %q: expected an object with a \"colors\" map.", label)
	}
	if theme.Colors == nil {
		return nil, fmt.Errorf("Invalid theme %q: expected an object with a \"colors\" map.", label)
	}
	return &theme, nil
}
