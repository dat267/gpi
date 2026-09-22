package interactive

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dat267/pier/coding"
	"github.com/dat267/pier/tui"
)

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
	return fmt.Sprintf("%dh %dm %ds", minutes/60, minutes%60, remainder)
}

// toolDetailsFrom decodes tool-result details into the typed form. Session
// results carry raw JSON; live results may already be typed. Upstream reads
// details.* through JS's dynamic typing, which the port must do explicitly.
func toolDetailsFrom[T any](details any) *T {
	switch d := details.(type) {
	case *T:
		return d
	case T:
		return &d
	case json.RawMessage:
		var decoded T
		if json.Unmarshal(d, &decoded) == nil {
			return &decoded
		}
	case []byte:
		var decoded T
		if json.Unmarshal(d, &decoded) == nil {
			return &decoded
		}
	}
	return nil
}
