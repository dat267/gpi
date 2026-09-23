package interactive

// Port of packages/coding-agent/src/core/export-html (index.ts and
// ansi-to-html.ts): session HTML export for /export. The templates are the
// upstream 0.86.1 files, embedded verbatim. Upstream pre-renders only tools
// whose definitions carry TUI renderers — stock tools have none (verified:
// createGrepTool exposes no renderCall), so without extension renderers
// (D41/D133) renderedTools is always omitted and the template's structured
// fallback draws every tool, exactly like upstream.

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
)

//go:embed exporthtml/template.html
var exportTemplateHTML string

//go:embed exporthtml/template.css
var exportTemplateCSS string

//go:embed exporthtml/template.js
var exportTemplateJS string

//go:embed exporthtml/vendor/marked.min.js
var exportMarkedJS string

//go:embed exporthtml/vendor/highlight.min.js
var exportHighlightJS string

// D-row: upstream iterates the theme colors object in theme-file order; the
// Go port emits CSS custom properties in alphabetical key order. CSS custom
// property order has no rendering effect.
//
// templateRenderedTools mirrors upstream TEMPLATE_RENDERED_TOOLS: tools the
// HTML template draws itself instead of consuming pre-rendered HTML.
var templateRenderedTools = map[string]bool{
	"bash": true, "read": true, "write": true, "edit": true, "ls": true,
}

// ---- ANSI to HTML (ansi-to-html.ts) ----

var ansiColors = [16]string{
	"#000000", "#800000", "#008000", "#808000",
	"#000080", "#800080", "#008080", "#c0c0c0",
	"#808080", "#ff0000", "#00ff00", "#ffff00",
	"#0000ff", "#ff00ff", "#00ffff", "#ffffff",
}

var ansiSequenceRegex = regexp.MustCompile(`\x1b\[([\d;]*)m`)

func color256ToHex(index int) string {
	if index < 16 {
		return ansiColors[index]
	}
	if index < 232 {
		cubeIndex := index - 16
		r := cubeIndex / 36
		g := (cubeIndex % 36) / 6
		b := cubeIndex % 6
		component := func(n int) string {
			value := 0
			if n != 0 {
				value = 55 + n*40
			}
			return fmt.Sprintf("%02x", value)
		}
		return "#" + component(r) + component(g) + component(b)
	}
	gray := 8 + (index-232)*10
	return fmt.Sprintf("#%02x%02x%02x", gray, gray, gray)
}

func escapeHTMLText(text string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#039;",
	)
	return replacer.Replace(text)
}

type htmlTextStyle struct {
	fg, bg            string
	bold, dim         bool
	italic, underline bool
}

func styleToInlineCSS(style *htmlTextStyle) string {
	parts := make([]string, 0, 6)
	if style.fg != "" {
		parts = append(parts, "color:"+style.fg)
	}
	if style.bg != "" {
		parts = append(parts, "background-color:"+style.bg)
	}
	if style.bold {
		parts = append(parts, "font-weight:bold")
	}
	if style.dim {
		parts = append(parts, "opacity:0.6")
	}
	if style.italic {
		parts = append(parts, "font-style:italic")
	}
	if style.underline {
		parts = append(parts, "text-decoration:underline")
	}
	return strings.Join(parts, ";")
}

func hasHTMLStyle(style *htmlTextStyle) bool {
	return style.fg != "" || style.bg != "" || style.bold || style.dim || style.italic || style.underline
}

func applySGRCode(params []int, style *htmlTextStyle) {
	for i := 0; i < len(params); i++ {
		code := params[i]
		switch {
		case code == 0:
			*style = htmlTextStyle{}
		case code == 1:
			style.bold = true
		case code == 2:
			style.dim = true
		case code == 3:
			style.italic = true
		case code == 4:
			style.underline = true
		case code == 22:
			style.bold = false
			style.dim = false
		case code == 23:
			style.italic = false
		case code == 24:
			style.underline = false
		case code >= 30 && code <= 37:
			style.fg = ansiColors[code-30]
		case code == 38:
			if i+2 < len(params) && params[i+1] == 5 {
				style.fg = color256ToHex(params[i+2])
				i += 2
			} else if i+4 < len(params) && params[i+1] == 2 {
				style.fg = "rgb(" + strconv.Itoa(params[i+2]) + "," + strconv.Itoa(params[i+3]) + "," + strconv.Itoa(params[i+4]) + ")"
				i += 4
			}
		case code == 39:
			style.fg = ""
		case code >= 40 && code <= 47:
			style.bg = ansiColors[code-40]
		case code == 48:
			if i+2 < len(params) && params[i+1] == 5 {
				style.bg = color256ToHex(params[i+2])
				i += 2
			} else if i+4 < len(params) && params[i+1] == 2 {
				style.bg = "rgb(" + strconv.Itoa(params[i+2]) + "," + strconv.Itoa(params[i+3]) + "," + strconv.Itoa(params[i+4]) + ")"
				i += 4
			}
		case code == 49:
			style.bg = ""
		case code >= 90 && code <= 97:
			style.fg = ansiColors[code-90+8]
		case code >= 100 && code <= 107:
			style.bg = ansiColors[code-100+8]
		}
	}
}

// AnsiToHTML converts ANSI-escaped text to HTML with inline styles
// (upstream ansiToHtml).
func AnsiToHTML(text string) string {
	style := &htmlTextStyle{}
	var result strings.Builder
	lastIndex := 0
	inSpan := false

	for _, loc := range ansiSequenceRegex.FindAllStringSubmatchIndex(text, -1) {
		before := text[lastIndex:loc[0]]
		if before != "" {
			result.WriteString(escapeHTMLText(before))
		}
		paramStr := text[loc[2]:loc[3]]
		params := []int{0}
		if paramStr != "" {
			params = params[:0]
			for _, part := range strings.Split(paramStr, ";") {
				value, err := strconv.Atoi(part)
				if err != nil || value == 0 {
					value = 0
				}
				params = append(params, value)
			}
		}
		if inSpan {
			result.WriteString("</span>")
			inSpan = false
		}
		applySGRCode(params, style)
		if hasHTMLStyle(style) {
			result.WriteString(`<span style="` + styleToInlineCSS(style) + `">`)
			inSpan = true
		}
		lastIndex = loc[1]
	}
	if remaining := text[lastIndex:]; remaining != "" {
		result.WriteString(escapeHTMLText(remaining))
	}
	if inSpan {
		result.WriteString("</span>")
	}
	return result.String()
}

// AnsiLinesToHTML converts ANSI lines to wrapped div elements
// (upstream ansiLinesToHtml).
func AnsiLinesToHTML(lines []string) string {
	var result strings.Builder
	for _, line := range lines {
		html := AnsiToHTML(line)
		if html == "" {
			html = "&nbsp;"
		}
		result.WriteString(`<div class="ansi-line">` + html + `</div>`)
	}
	return result.String()
}

// ---- Export color derivation (index.ts) ----

func parseExportColor(color string) (r, g, b int, ok bool) {
	hex := regexp.MustCompile(`^#([0-9a-fA-F]{2})([0-9a-fA-F]{2})([0-9a-fA-F]{2})$`).FindStringSubmatch(color)
	if hex != nil {
		values := make([]int, 3)
		for i := 0; i < 3; i++ {
			value, err := strconv.ParseInt(hex[i+1], 16, 32)
			if err != nil {
				return 0, 0, 0, false
			}
			values[i] = int(value)
		}
		return values[0], values[1], values[2], true
	}
	rgb := regexp.MustCompile(`^rgb\s*\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*\)$`).FindStringSubmatch(color)
	if rgb != nil {
		values := make([]int, 3)
		for i := 0; i < 3; i++ {
			value, err := strconv.Atoi(rgb[i+1])
			if err != nil {
				return 0, 0, 0, false
			}
			values[i] = value
		}
		return values[0], values[1], values[2], true
	}
	return 0, 0, 0, false
}

func luminance(r, g, b int) float64 {
	toLinear := func(c int) float64 {
		s := float64(c) / 255
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*toLinear(r) + 0.7152*toLinear(g) + 0.0722*toLinear(b)
}

func adjustBrightness(color string, factor float64) string {
	r, g, b, ok := parseExportColor(color)
	if !ok {
		return color
	}
	adjust := func(c int) int {
		value := int(math.Round(float64(c) * factor))
		return min(max(value, 0), 255)
	}
	return "rgb(" + strconv.Itoa(adjust(r)) + ", " + strconv.Itoa(adjust(g)) + ", " + strconv.Itoa(adjust(b)) + ")"
}

type exportColors struct {
	pageBg, cardBg, infoBg string
}

func deriveExportColors(baseColor string) exportColors {
	r, g, b, ok := parseExportColor(baseColor)
	if !ok {
		return exportColors{pageBg: "rgb(24, 24, 30)", cardBg: "rgb(30, 30, 36)", infoBg: "rgb(60, 55, 40)"}
	}
	clamp := func(value int) int { return min(max(value, 0), 255) }
	if luminance(r, g, b) > 0.5 {
		return exportColors{
			pageBg: adjustBrightness(baseColor, 0.96),
			cardBg: baseColor,
			infoBg: "rgb(" + strconv.Itoa(clamp(r+10)) + ", " + strconv.Itoa(clamp(g+5)) + ", " + strconv.Itoa(clamp(b-20)) + ")",
		}
	}
	return exportColors{
		pageBg: adjustBrightness(baseColor, 0.7),
		cardBg: adjustBrightness(baseColor, 0.85),
		infoBg: "rgb(" + strconv.Itoa(clamp(r+20)) + ", " + strconv.Itoa(clamp(g+15)) + ", " + strconv.Itoa(b) + ")",
	}
}

func generateThemeVars(themeName string) string {
	colors, err := GetResolvedThemeColors(themeName)
	if err != nil {
		colors = map[string]string{}
	}
	keys := make([]string, 0, len(colors))
	for key := range colors {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys)+3)
	for _, key := range keys {
		lines = append(lines, "--"+key+": "+colors[key]+";")
	}

	exportPageBg, exportCardBg, exportInfoBg, _ := GetThemeExportColors(themeName)
	themeExport := exportColors{pageBg: exportPageBg, cardBg: exportCardBg, infoBg: exportInfoBg}
	userMessageBg := colors["userMessageBg"]
	if userMessageBg == "" {
		userMessageBg = "#343541"
	}
	derived := deriveExportColors(userMessageBg)
	pageBg, cardBg, infoBg := derived.pageBg, derived.cardBg, derived.infoBg
	if themeExport.pageBg != "" {
		pageBg = themeExport.pageBg
	}
	if themeExport.cardBg != "" {
		cardBg = themeExport.cardBg
	}
	if themeExport.infoBg != "" {
		infoBg = themeExport.infoBg
	}
	lines = append(lines, "--exportPageBg: "+pageBg+";", "--exportCardBg: "+cardBg+";", "--exportInfoBg: "+infoBg+";")
	return strings.Join(lines, "\n      ")
}

// ---- Session data + rendering (index.ts) ----

// renderedToolHTML is the pre-rendered HTML for one tool call/result
// (upstream RenderedToolHtml).
type renderedToolHTML struct {
	CallHTML            string `json:"callHtml,omitempty"`
	ResultHTMLCollapsed string `json:"resultHtmlCollapsed,omitempty"`
	ResultHTMLExpanded  string `json:"resultHtmlExpanded,omitempty"`
}

type exportTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type exportSessionData struct {
	// Header marshals with the leading "type":"session" discriminator, like
	// upstream's header entry object.
	Header        json.RawMessage              `json:"header"`
	Entries       []coding.SessionEntry        `json:"entries"`
	LeafID        *string                      `json:"leafId"`
	SystemPrompt  string                       `json:"systemPrompt,omitempty"`
	Tools         []exportTool                 `json:"tools,omitempty"`
	RenderedTools map[string]*renderedToolHTML `json:"renderedTools,omitempty"`
}

// GenerateSessionHTML renders the export document (upstream generateHtml).
func GenerateSessionHTML(data *exportSessionData, themeName string) string {
	themeVars := generateThemeVars(themeName)
	colors, _ := GetResolvedThemeColors(themeName)
	exportPageBg, exportCardBg, exportInfoBg, _ := GetThemeExportColors(themeName)
	themeExport := exportColors{pageBg: exportPageBg, cardBg: exportCardBg, infoBg: exportInfoBg}
	userMessageBg := colors["userMessageBg"]
	if userMessageBg == "" {
		userMessageBg = "#343541"
	}
	derived := deriveExportColors(userMessageBg)
	bodyBg, containerBg, infoBg := derived.pageBg, derived.cardBg, derived.infoBg
	if themeExport.pageBg != "" {
		bodyBg = themeExport.pageBg
	}
	if themeExport.cardBg != "" {
		containerBg = themeExport.cardBg
	}
	if themeExport.infoBg != "" {
		infoBg = themeExport.infoBg
	}

	// Base64 encode session data to avoid escaping issues.
	payload, err := ai.MarshalJSON(data)
	if err != nil {
		payload = []byte("{}")
	}
	sessionDataBase64 := base64.StdEncoding.EncodeToString(payload)

	css := strings.Replace(exportTemplateCSS, "{{THEME_VARS}}", themeVars, 1)
	css = strings.Replace(css, "{{BODY_BG}}", bodyBg, 1)
	css = strings.Replace(css, "{{CONTAINER_BG}}", containerBg, 1)
	css = strings.Replace(css, "{{INFO_BG}}", infoBg, 1)

	html := strings.Replace(exportTemplateHTML, "{{CSS}}", css, 1)
	html = strings.Replace(html, "{{JS}}", exportTemplateJS, 1)
	html = strings.Replace(html, "{{SESSION_DATA}}", sessionDataBase64, 1)
	html = strings.Replace(html, "{{MARKED_JS}}", exportMarkedJS, 1)
	html = strings.Replace(html, "{{HIGHLIGHT_JS}}", exportHighlightJS, 1)
	return html
}

// ExportFromFile exports a session file to HTML (upstream exportFromFile). An
// empty outputPath defaults to "<app>-session-<basename>.html" in the working
// directory, and an empty themeName uses the current/default theme.
//
// Unlike ExportSessionToHTML this reads a session that this process never
// opened, so the exported document carries no system prompt or tool set —
// upstream reads the same fields from the file's header and entries.
func ExportFromFile(inputPath string, outputPath string, themeName string) (string, error) {
	resolvedInput := coding.ResolvePath(inputPath, "", coding.PathInputOptions{})
	if _, err := os.Stat(resolvedInput); err != nil {
		return "", fmt.Errorf("File not found: %s", resolvedInput)
	}
	sessionManager, err := coding.OpenSession(resolvedInput, "", "")
	if err != nil {
		return "", err
	}

	data := &exportSessionData{
		Entries: sessionManager.GetEntries(),
		LeafID:  sessionManager.GetLeafID(),
	}
	if header := sessionManager.GetHeader(); header != nil {
		data.Header = exportHeaderJSON(header)
	}

	html := GenerateSessionHTML(data, themeName)

	if outputPath == "" {
		base := strings.TrimSuffix(filepath.Base(resolvedInput), ".jsonl")
		outputPath = coding.AppName + "-session-" + base + ".html"
	}
	if err := os.WriteFile(outputPath, []byte(html), 0o644); err != nil {
		return "", err
	}
	return outputPath, nil
}

// exportHeaderJSON wraps the session header in an entry carrying the "session"
// discriminator, the shape the export document's header field expects.
func exportHeaderJSON(header *coding.SessionHeader) json.RawMessage {
	headerJSON, err := ai.MarshalJSON(struct {
		Type string `json:"type"`
		*coding.SessionHeader
	}{Type: "session", SessionHeader: header})
	if err != nil {
		return json.RawMessage("null")
	}
	return headerJSON
}

// ExportSessionToHTML exports the current session branch to an HTML file
// (upstream exportSessionToHtml, via agent-session.exportToHtml). An empty
// themeName uses the currently applied theme.
func (s *AppSession) ExportSessionToHTML(outputPath string, themeName string) (string, error) {
	sessionManager := s.Sessions
	sessionFile := sessionManager.GetSessionFile()
	if sessionFile == "" {
		return "", fmt.Errorf("Cannot export in-memory session to HTML")
	}
	if _, err := os.Stat(sessionFile); err != nil {
		return "", fmt.Errorf("Nothing to export yet - start a conversation first")
	}

	entries := sessionManager.GetEntries()
	state := s.Agent.State()
	tools := make([]exportTool, 0, len(state.Tools))
	for _, tool := range state.Tools {
		tools = append(tools, exportTool{
			Name: tool.Name, Description: tool.Description, Parameters: tool.Parameters,
		})
	}

	data := &exportSessionData{
		Entries:      entries,
		LeafID:       sessionManager.GetLeafID(),
		SystemPrompt: state.SystemPrompt,
		Tools:        tools,
	}
	if header := sessionManager.GetHeader(); header != nil {
		data.Header = exportHeaderJSON(header)
	}
	if themeName == "" {
		themeName = CurrentThemeName()
	}

	html := GenerateSessionHTML(data, themeName)

	if outputPath == "" {
		base := filepath.Base(sessionFile)
		base = strings.TrimSuffix(base, ".jsonl")
		outputPath = coding.AppName + "-session-" + base + ".html"
	}
	if err := os.WriteFile(outputPath, []byte(html), 0o644); err != nil {
		return "", err
	}
	return outputPath, nil
}
