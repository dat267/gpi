package tui

import (
	"regexp"
	"strings"
)

// Port of the renderer half of src/components/markdown.ts: the Markdown
// component that turns the token tree into styled terminal lines.
//
// The LaTeX renderer (src/latex.ts) is a separate port (divergence D68): while
// it is pending, RenderLatex reports "unsupported", which is exactly upstream's
// fallback path (the raw LaTeX source is emitted).

// TerminalCapabilities holds the terminal feature flags the markdown renderer
// consults (upstream getCapabilities(); divergence D69: injectable instead of
// environment-detected).
type TerminalCapabilities struct {
	Hyperlinks bool
	TrueColor  bool
	// Images is the inline-image protocol ("" | "kitty" | "iterm2"); the
	// transport itself is out of scope (D57/D81).
	Images string
}

var terminalCapabilitiesState struct {
	capabilities TerminalCapabilities
}

// SetTerminalCapabilities sets the capability flags.
func SetTerminalCapabilities(capabilities TerminalCapabilities) {
	terminalCapabilitiesState.capabilities = capabilities
}

// GetTerminalCapabilities returns the capability flags.
func GetTerminalCapabilities() TerminalCapabilities {
	return terminalCapabilitiesState.capabilities
}

// DefaultTextStyle is the base styling applied to markdown text.
type DefaultTextStyle struct {
	Color         func(text string) string
	BgColor       func(text string) string
	Bold          bool
	Italic        bool
	Strikethrough bool
	Underline     bool
}

// MarkdownTheme styles markdown elements.
type MarkdownTheme struct {
	Heading         func(text string) string
	Link            func(text string) string
	LinkURL         func(text string) string
	Code            func(text string) string
	CodeBlock       func(text string) string
	CodeBlockBorder func(text string) string
	Quote           func(text string) string
	QuoteBorder     func(text string) string
	Hr              func(text string) string
	ListBullet      func(text string) string
	Bold            func(text string) string
	Italic          func(text string) string
	Strikethrough   func(text string) string
	Underline       func(text string) string
	// HighlightCode, when set, replaces the per-line code styling.
	HighlightCode func(code string, lang string) []string
	// CodeBlockIndent prefixes code block lines (default "  ").
	CodeBlockIndent string
}

// MarkdownOptions configure the Markdown component.
type MarkdownOptions struct {
	// PreserveOrderedListMarkers keeps source list markers instead of
	// normalizing them.
	PreserveOrderedListMarkers bool
	// PreserveBackslashEscapes keeps source backslash escapes.
	PreserveBackslashEscapes bool
	// Transform transforms the source markdown with the available content
	// width.
	Transform func(markdown string, availableWidth int) string
	// RenderLatex toggles LaTeX rendering (default true).
	RenderLatex *bool
}

type inlineStyleContext struct {
	applyText   func(text string) string
	stylePrefix string
}

// Markdown renders markdown text for the terminal.
type Markdown struct {
	Text             string
	PaddingX         int
	PaddingY         int
	Theme            MarkdownTheme
	DefaultTextStyle *DefaultTextStyle
	Options          MarkdownOptions

	defaultStylePrefix string
	hasStylePrefix     bool

	cachedText     string
	hasCachedText  bool
	cachedWidth    int
	hasCachedWidth bool
	cachedLines    []string
	hasCachedLines bool

	// Incremental render cache. Streaming appends to the source, so the token
	// list keeps a stable prefix; cacheTokenLines holds the final (wrapped,
	// padded) lines for cacheTokens[:len(cacheTokens)-1] so an append only
	// re-renders the changed tail instead of the whole message.
	cacheTokens     []*MdToken
	cacheTokenLines [][]string
	cacheWidth      int

	// renderedTokens counts renderToken calls (test seam).
	renderedTokens int
}

// NewMarkdown creates a markdown component.
func NewMarkdown(text string, paddingX int, paddingY int, theme MarkdownTheme, defaultTextStyle *DefaultTextStyle, options MarkdownOptions) *Markdown {
	theme = withDefaultMarkdownTheme(theme)
	return &Markdown{
		Text:             text,
		PaddingX:         paddingX,
		PaddingY:         paddingY,
		Theme:            theme,
		DefaultTextStyle: defaultTextStyle,
		Options:          options,
	}
}

func identityStyle(text string) string { return text }

func withDefaultMarkdownTheme(theme MarkdownTheme) MarkdownTheme {
	if theme.Heading == nil {
		theme.Heading = identityStyle
	}
	if theme.Link == nil {
		theme.Link = identityStyle
	}
	if theme.LinkURL == nil {
		theme.LinkURL = identityStyle
	}
	if theme.Code == nil {
		theme.Code = identityStyle
	}
	if theme.CodeBlock == nil {
		theme.CodeBlock = identityStyle
	}
	if theme.CodeBlockBorder == nil {
		theme.CodeBlockBorder = identityStyle
	}
	if theme.Quote == nil {
		theme.Quote = identityStyle
	}
	if theme.QuoteBorder == nil {
		theme.QuoteBorder = identityStyle
	}
	if theme.Hr == nil {
		theme.Hr = identityStyle
	}
	if theme.ListBullet == nil {
		theme.ListBullet = identityStyle
	}
	if theme.Bold == nil {
		theme.Bold = identityStyle
	}
	if theme.Italic == nil {
		theme.Italic = identityStyle
	}
	if theme.Strikethrough == nil {
		theme.Strikethrough = identityStyle
	}
	if theme.Underline == nil {
		theme.Underline = identityStyle
	}
	return theme
}

// SetText updates the markdown source. The incremental cache is kept: Render
// reuses the prefix that is still identical and re-renders the changed tail.
func (m *Markdown) SetText(text string) {
	m.Text = text
	m.cachedLines = nil
	m.hasCachedLines = false
	m.hasCachedText = false
	m.hasCachedWidth = false
}

// Invalidate drops the render cache.
func (m *Markdown) Invalidate() {
	m.cachedLines = nil
	m.hasCachedLines = false
	m.hasCachedText = false
	m.hasCachedWidth = false
	m.hasStylePrefix = false
	m.cacheTokens = nil
	m.cacheTokenLines = nil
	m.cacheWidth = 0
}

// Render renders the markdown at the given width.
func (m *Markdown) Render(width int) []string {
	if m.hasCachedLines && m.hasCachedText && m.cachedText == m.Text && m.hasCachedWidth && m.cachedWidth == width {
		return m.cachedLines
	}

	contentWidth := maxInt(1, width-m.PaddingX*2)
	text := m.Text
	if m.Options.Transform != nil {
		text = m.Options.Transform(text, contentWidth)
	}

	if text == "" || strings.TrimSpace(text) == "" {
		m.cachedText = m.Text
		m.hasCachedText = true
		m.cachedWidth = width
		m.hasCachedWidth = true
		m.cachedLines = []string{}
		m.hasCachedLines = true
		m.cacheTokens = nil
		m.cacheTokenLines = nil
		m.cacheWidth = width
		return m.cachedLines
	}

	normalizedText := strings.ReplaceAll(text, "\t", "   ")

	tokens := LexMarkdown(normalizedText)
	TrimPartialClosingFences(tokens)

	// Reuse the final lines of every token that did not change since the last
	// render; only the changed tail is re-parsed and re-styled.
	reuse := m.reusableTokens(tokens, width)

	leftMargin := repeatSpaces(maxInt(0, m.PaddingX))
	rightMargin := leftMargin
	var bgFn func(text string) string
	if m.DefaultTextStyle != nil {
		bgFn = m.DefaultTextStyle.BgColor
	}

	var contentLines []string
	tokenLines := make([][]string, 0, maxInt(0, len(tokens)-1))
	for index, token := range tokens {
		var final []string
		if index < reuse {
			final = m.cacheTokenLines[index]
		} else {
			nextType := ""
			if index+1 < len(tokens) {
				nextType = tokens[index+1].Type
			}
			m.renderedTokens++
			final = m.finalizeTokenLines(m.renderToken(token, contentWidth, nextType, nil), width, contentWidth, leftMargin, rightMargin, bgFn)
		}
		contentLines = append(contentLines, final...)
		if index < len(tokens)-1 {
			tokenLines = append(tokenLines, final)
		}
	}
	m.cacheTokens = tokens
	m.cacheTokenLines = tokenLines
	m.cacheWidth = width

	emptyLine := repeatSpaces(width)
	var emptyLines []string
	for i := 0; i < m.PaddingY; i++ {
		line := emptyLine
		if bgFn != nil {
			line = ApplyBackgroundToLine(emptyLine, width, bgFn)
		}
		emptyLines = append(emptyLines, line)
	}

	result := make([]string, 0, len(emptyLines)*2+len(contentLines))
	result = append(result, emptyLines...)
	result = append(result, contentLines...)
	result = append(result, emptyLines...)

	m.cachedText = m.Text
	m.hasCachedText = true
	m.cachedWidth = width
	m.hasCachedWidth = true
	m.cachedLines = result
	m.hasCachedLines = true

	if len(result) > 0 {
		return result
	}
	return []string{""}
}

// reusableTokens returns how many leading tokens of the current render can
// reuse the cached final lines. A token's lines were rendered with the next
// token's type as context, so the last matching token is only reused when its
// successor also matches.
func (m *Markdown) reusableTokens(tokens []*MdToken, width int) int {
	if m.cacheWidth != width || len(m.cacheTokenLines) == 0 {
		return 0
	}
	limit := len(tokens)
	if len(m.cacheTokens) < limit {
		limit = len(m.cacheTokens)
	}
	matched := 0
	for matched < limit && sameMarkdownToken(tokens[matched], m.cacheTokens[matched]) {
		matched++
	}
	reuse := matched - 1
	maxReuse := len(m.cacheTokenLines)
	if len(tokens)-1 < maxReuse {
		maxReuse = len(tokens) - 1
	}
	if maxReuse < 0 {
		maxReuse = 0
	}
	if reuse > maxReuse {
		reuse = maxReuse
	}
	if reuse < 0 {
		reuse = 0
	}
	return reuse
}

// sameMarkdownToken reports whether two parse tokens are the same source
// region rendered the same way. Raw is the exact source slice, so equal Raw
// and Type imply an identical parse.
func sameMarkdownToken(a, b *MdToken) bool {
	if a == b {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Type == b.Type && a.Raw == b.Raw && a.Depth == b.Depth && a.Text == b.Text &&
		a.Href == b.Href && a.Title == b.Title && a.Lang == b.Lang &&
		a.Ordered == b.Ordered && a.Start == b.Start && a.HasStart == b.HasStart &&
		a.CellAlign == b.CellAlign && a.HasCellAlign == b.HasCellAlign &&
		len(a.Align) == len(b.Align) && len(a.Items) == len(b.Items) && len(a.Tokens) == len(b.Tokens)
}

// finalizeTokenLines wraps one token's rendered lines and applies the left and
// right margins and the background/padding.
func (m *Markdown) finalizeTokenLines(rendered []string, width int, contentWidth int, leftMargin string, rightMargin string, bgFn func(text string) string) []string {
	out := make([]string, 0, len(rendered))
	for _, line := range rendered {
		if IsImageLine(line) {
			out = append(out, line)
			continue
		}
		for _, wrapped := range WrapTextWithAnsi(line, contentWidth) {
			lineWithMargins := leftMargin + wrapped + rightMargin
			if bgFn != nil {
				out = append(out, ApplyBackgroundToLine(lineWithMargins, width, bgFn))
			} else {
				paddingNeeded := maxInt(0, width-VisibleWidth(lineWithMargins))
				out = append(out, lineWithMargins+repeatSpaces(paddingNeeded))
			}
		}
	}
	return out
}

// applyDefaultStyle applies the default text style (the background is applied
// later so it extends to the full line width).
func (m *Markdown) applyDefaultStyle(text string) string {
	if m.DefaultTextStyle == nil {
		return text
	}
	styled := text
	if m.DefaultTextStyle.Color != nil {
		styled = m.DefaultTextStyle.Color(styled)
	}
	if m.DefaultTextStyle.Bold {
		styled = m.Theme.Bold(styled)
	}
	if m.DefaultTextStyle.Italic {
		styled = m.Theme.Italic(styled)
	}
	if m.DefaultTextStyle.Strikethrough {
		styled = m.Theme.Strikethrough(styled)
	}
	if m.DefaultTextStyle.Underline {
		styled = m.Theme.Underline(styled)
	}
	return styled
}

func (m *Markdown) getDefaultStylePrefix() string {
	if m.DefaultTextStyle == nil {
		return ""
	}
	if m.hasStylePrefix {
		return m.defaultStylePrefix
	}

	const sentinel = "\u0000"
	styled := sentinel
	if m.DefaultTextStyle.Color != nil {
		styled = m.DefaultTextStyle.Color(styled)
	}
	if m.DefaultTextStyle.Bold {
		styled = m.Theme.Bold(styled)
	}
	if m.DefaultTextStyle.Italic {
		styled = m.Theme.Italic(styled)
	}
	if m.DefaultTextStyle.Strikethrough {
		styled = m.Theme.Strikethrough(styled)
	}
	if m.DefaultTextStyle.Underline {
		styled = m.Theme.Underline(styled)
	}

	sentinelIndex := strings.Index(styled, sentinel)
	if sentinelIndex >= 0 {
		m.defaultStylePrefix = styled[:sentinelIndex]
	} else {
		m.defaultStylePrefix = ""
	}
	m.hasStylePrefix = true
	return m.defaultStylePrefix
}

func (m *Markdown) getStylePrefix(styleFn func(string) string) string {
	const sentinel = "\u0000"
	styled := styleFn(sentinel)
	sentinelIndex := strings.Index(styled, sentinel)
	if sentinelIndex >= 0 {
		return styled[:sentinelIndex]
	}
	return ""
}

func (m *Markdown) getDefaultInlineStyleContext() inlineStyleContext {
	return inlineStyleContext{
		applyText:   func(text string) string { return m.applyDefaultStyle(text) },
		stylePrefix: m.getDefaultStylePrefix(),
	}
}

func (m *Markdown) renderLatexEnabled() bool {
	if m.Options.RenderLatex == nil {
		return true
	}
	return *m.Options.RenderLatex
}

func (m *Markdown) renderToken(token *MdToken, width int, nextTokenType string, styleContext *inlineStyleContext) []string {
	var lines []string

	switch token.Type {
	case "heading":
		headingLevel := token.Depth
		headingPrefix := strings.Repeat("#", headingLevel) + " "

		var headingStyleFn func(string) string
		if headingLevel == 1 {
			headingStyleFn = func(text string) string {
				return m.Theme.Heading(m.Theme.Bold(m.Theme.Underline(text)))
			}
		} else {
			headingStyleFn = func(text string) string {
				return m.Theme.Heading(m.Theme.Bold(text))
			}
		}
		headingStyleContext := inlineStyleContext{
			applyText:   headingStyleFn,
			stylePrefix: m.getStylePrefix(headingStyleFn),
		}

		headingText := m.renderInlineTokens(token.Tokens, &headingStyleContext)
		styledHeading := headingText
		if headingLevel >= 3 {
			styledHeading = headingStyleFn(headingPrefix) + headingText
		}
		lines = append(lines, styledHeading)
		if nextTokenType != "" && nextTokenType != "space" {
			lines = append(lines, "")
		}

	case "paragraph":
		paragraphText := m.renderInlineTokens(token.Tokens, styleContext)
		lines = append(lines, paragraphText)
		if nextTokenType != "" && nextTokenType != "list" && nextTokenType != "space" {
			lines = append(lines, "")
		}

	case "text":
		lines = append(lines, m.renderInlineTokens([]*MdToken{token}, styleContext))

	case "latexBlock":
		rendered := token.Raw
		if !token.Pending && m.renderLatexEnabled() {
			if latex, ok := RenderLatex(token.Text, RenderLatexOptions{Display: true}); ok {
				rendered = latex
			} else {
				rendered = strings.TrimSpace(token.Raw)
			}
		} else {
			rendered = strings.TrimSpace(token.Raw)
		}
		for _, line := range strings.Split(rendered, "\n") {
			lines = append(lines, m.applyDefaultStyle(line))
		}
		if nextTokenType != "" && nextTokenType != "space" {
			lines = append(lines, "")
		}

	case "code":
		indent := m.Theme.CodeBlockIndent
		if indent == "" {
			indent = "  "
		}
		lines = append(lines, m.Theme.CodeBlockBorder("```"+token.Lang))
		if m.Theme.HighlightCode != nil {
			for _, hlLine := range m.Theme.HighlightCode(token.Text, token.Lang) {
				lines = append(lines, indent+hlLine)
			}
		} else {
			for _, codeLine := range strings.Split(token.Text, "\n") {
				lines = append(lines, indent+m.Theme.CodeBlock(codeLine))
			}
		}
		lines = append(lines, m.Theme.CodeBlockBorder("```"))
		if nextTokenType != "" && nextTokenType != "space" {
			lines = append(lines, "")
		}

	case "list":
		lines = append(lines, m.renderList(token, 0, width, styleContext)...)

	case "table":
		lines = append(lines, m.renderTable(token, width, nextTokenType, styleContext)...)

	case "blockquote":
		quoteStyle := func(text string) string { return m.Theme.Quote(m.Theme.Italic(text)) }
		quoteStylePrefix := m.getStylePrefix(quoteStyle)
		applyQuoteStyle := func(line string) string {
			if quoteStylePrefix == "" {
				return quoteStyle(line)
			}
			lineWithReappliedStyle := strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+quoteStylePrefix)
			return quoteStyle(lineWithReappliedStyle)
		}

		quoteContentWidth := maxInt(1, width-2)
		quoteInlineStyleContext := inlineStyleContext{
			applyText:   func(text string) string { return text },
			stylePrefix: quoteStylePrefix,
		}

		var renderedQuoteLines []string
		quoteTokens := token.Tokens
		for index, quoteToken := range quoteTokens {
			nextQuoteType := ""
			if index+1 < len(quoteTokens) {
				nextQuoteType = quoteTokens[index+1].Type
			}
			renderedQuoteLines = append(renderedQuoteLines,
				m.renderToken(quoteToken, quoteContentWidth, nextQuoteType, &quoteInlineStyleContext)...)
		}

		for len(renderedQuoteLines) > 0 && renderedQuoteLines[len(renderedQuoteLines)-1] == "" {
			renderedQuoteLines = renderedQuoteLines[:len(renderedQuoteLines)-1]
		}

		for _, quoteLine := range renderedQuoteLines {
			styledLine := applyQuoteStyle(quoteLine)
			for _, wrappedLine := range WrapTextWithAnsi(styledLine, quoteContentWidth) {
				lines = append(lines, m.Theme.QuoteBorder("│ ")+wrappedLine)
			}
		}
		if nextTokenType != "" && nextTokenType != "space" {
			lines = append(lines, "")
		}

	case "hr":
		hrWidth := minInt(width, 80)
		lines = append(lines, m.Theme.Hr(strings.Repeat("─", maxInt(0, hrWidth))))
		if nextTokenType != "" && nextTokenType != "space" {
			lines = append(lines, "")
		}

	case "html":
		lines = append(lines, m.applyDefaultStyle(strings.TrimSpace(token.Raw)))

	case "space":
		lines = append(lines, "")

	default:
		if token.Text != "" {
			lines = append(lines, token.Text)
		}
	}

	return lines
}

func (m *Markdown) renderInlineTokens(tokens []*MdToken, styleContext *inlineStyleContext) string {
	result := ""
	resolved := styleContext
	if resolved == nil {
		defaultContext := m.getDefaultInlineStyleContext()
		resolved = &defaultContext
	}
	applyText := resolved.applyText
	stylePrefix := resolved.stylePrefix

	applyTextWithNewlines := func(text string) string {
		segments := strings.Split(text, "\n")
		for index, segment := range segments {
			segments[index] = applyText(segment)
		}
		return strings.Join(segments, "\n")
	}

	for _, token := range tokens {
		switch token.Type {
		case "latex":
			rendered := token.Raw
			if !token.Pending && m.renderLatexEnabled() {
				if latex, ok := RenderLatex(token.Text, RenderLatexOptions{}); ok {
					rendered = latex
				}
			}
			result += applyTextWithNewlines(rendered)

		case "escape":
			if m.Options.PreserveBackslashEscapes {
				result += applyTextWithNewlines(token.Raw)
			} else {
				result += applyTextWithNewlines(token.Text)
			}

		case "text":
			if len(token.Tokens) > 0 {
				result += m.renderInlineTokens(token.Tokens, resolved)
			} else {
				result += applyTextWithNewlines(token.Text)
			}

		case "paragraph":
			result += m.renderInlineTokens(token.Tokens, resolved)

		case "strong":
			boldContent := m.renderInlineTokens(token.Tokens, resolved)
			result += m.Theme.Bold(boldContent) + stylePrefix

		case "em":
			italicContent := m.renderInlineTokens(token.Tokens, resolved)
			result += m.Theme.Italic(italicContent) + stylePrefix

		case "codespan":
			result += m.Theme.Code(token.Text) + stylePrefix

		case "link":
			linkText := m.renderInlineTokens(token.Tokens, resolved)
			styledLink := m.Theme.Link(m.Theme.Underline(linkText))
			if GetTerminalCapabilities().Hyperlinks {
				result += Hyperlink(styledLink, token.Href) + stylePrefix
			} else {
				hrefForComparison := token.Href
				if strings.HasPrefix(hrefForComparison, "mailto:") {
					hrefForComparison = hrefForComparison[len("mailto:"):]
				}
				if token.Text == token.Href || token.Text == hrefForComparison {
					result += styledLink + stylePrefix
				} else {
					result += styledLink + m.Theme.LinkURL(" ("+token.Href+")") + stylePrefix
				}
			}

		case "br":
			result += "\n"

		case "del":
			delContent := m.renderInlineTokens(token.Tokens, resolved)
			result += m.Theme.Strikethrough(delContent) + stylePrefix

		case "html":
			result += applyTextWithNewlines(token.Raw)

		default:
			if token.Text != "" {
				result += applyTextWithNewlines(token.Text)
			}
		}
	}

	for stylePrefix != "" && strings.HasSuffix(result, stylePrefix) {
		result = result[:len(result)-len(stylePrefix)]
	}
	return result
}

var (
	orderedListMarkerRegex = regexp.MustCompile(`^(?: {0,3})(\d{1,9}[.)])[ \t]+`)
	// Go's RE2 has no lookahead, so the "marker followed by whitespace or the
	// end of input" case is handled in the matcher below.
	unorderedListMarkerRegex = regexp.MustCompile(`^(?: {0,3})([-+*])(?:[ \t]+|$)`)
)

func (m *Markdown) getOrderedListMarker(item *MdToken) (string, bool) {
	match := orderedListMarkerRegex.FindStringSubmatch(item.Raw)
	if match == nil {
		return "", false
	}
	return match[1] + " ", true
}

func (m *Markdown) getUnorderedListMarker(item *MdToken) (string, bool) {
	match := unorderedListMarkerRegex.FindStringSubmatch(item.Raw)
	if match == nil {
		return "", false
	}
	return match[1] + " ", true
}

// renderList renders a list with nesting support.
func (m *Markdown) renderList(token *MdToken, depth int, width int, styleContext *inlineStyleContext) []string {
	var lines []string
	indent := strings.Repeat("    ", depth)
	startNumber := 1
	if token.HasStart {
		startNumber = token.Start
	}

	for index, item := range token.Items {
		isLastItem := index == len(token.Items)-1
		bullet := "- "
		if token.Ordered {
			if m.Options.PreserveOrderedListMarkers {
				if marker, ok := m.getOrderedListMarker(item); ok {
					bullet = marker
				} else {
					bullet = itoa(startNumber+index) + ". "
				}
			} else {
				bullet = itoa(startNumber+index) + ". "
			}
		} else if m.Options.PreserveOrderedListMarkers {
			if marker, ok := m.getUnorderedListMarker(item); ok {
				bullet = marker
			}
		}
		taskMarker := ""
		if item.Task {
			if item.Checked {
				taskMarker = "[x] "
			} else {
				taskMarker = "[ ] "
			}
		}
		marker := bullet + taskMarker
		firstPrefix := indent + m.Theme.ListBullet(marker)
		continuationPrefix := indent + repeatSpaces(VisibleWidth(marker))
		itemWidth := maxInt(1, width-VisibleWidth(firstPrefix))
		renderedAnyLine := false

		for _, itemToken := range item.Tokens {
			if itemToken.Type == "list" {
				lines = append(lines, m.renderList(itemToken, depth+1, width, styleContext)...)
				renderedAnyLine = true
				continue
			}
			itemLines := m.renderToken(itemToken, itemWidth, "", styleContext)
			for _, line := range itemLines {
				for _, wrappedLine := range WrapTextWithAnsi(line, itemWidth) {
					linePrefix := continuationPrefix
					if !renderedAnyLine {
						linePrefix = firstPrefix
					}
					lines = append(lines, linePrefix+wrappedLine)
					renderedAnyLine = true
				}
			}
		}

		if !renderedAnyLine {
			lines = append(lines, firstPrefix)
		}

		if token.Loose && !isLastItem {
			lines = append(lines, "")
		}
	}

	return lines
}

// getLongestWordWidth returns the visible width of the longest word.
func (m *Markdown) getLongestWordWidth(text string, maxWidth int, hasMaxWidth bool) int {
	words := strings.Fields(text)
	longest := 0
	for _, word := range words {
		longest = maxInt(longest, VisibleWidth(word))
	}
	if !hasMaxWidth {
		return longest
	}
	return minInt(longest, maxWidth)
}

// wrapCellText wraps a table cell, resetting styles after each non-final
// fragment and restoring the surrounding style.
func (m *Markdown) wrapCellText(text string, maxWidth int, stylePrefix string) []string {
	lines := WrapTextWithAnsi(text, maxInt(1, maxWidth))
	out := make([]string, 0, len(lines))
	for index, line := range lines {
		styleReset := ""
		if index < len(lines)-1 {
			styleReset = "\x1b[22;23;24;25;27;28;29;39m"
		}
		out = append(out, line+styleReset+stylePrefix)
	}
	return out
}

// renderTable renders a width-aware table.
func (m *Markdown) renderTable(token *MdToken, availableWidth int, nextTokenType string, styleContext *inlineStyleContext) []string {
	var lines []string
	numCols := len(token.Header)
	if numCols == 0 {
		return lines
	}

	borderOverhead := 3*numCols + 1
	availableForCells := availableWidth - borderOverhead
	if availableForCells < numCols {
		// Too narrow for a stable table: fall back to the raw markdown.
		var fallbackLines []string
		if token.Raw != "" {
			fallbackLines = WrapTextWithAnsi(token.Raw, availableWidth)
		}
		if nextTokenType != "" && nextTokenType != "space" {
			fallbackLines = append(fallbackLines, "")
		}
		return fallbackLines
	}

	const maxUnbrokenWordWidth = 30

	naturalWidths := make([]int, numCols)
	minWordWidths := make([]int, numCols)
	for i := 0; i < numCols; i++ {
		headerText := m.renderInlineTokens(token.Header[i].Tokens, styleContext)
		naturalWidths[i] = VisibleWidth(headerText)
		minWordWidths[i] = maxInt(1, m.getLongestWordWidth(headerText, maxUnbrokenWordWidth, true))
	}
	for _, row := range token.Rows {
		for i := 0; i < len(row); i++ {
			cellText := m.renderInlineTokens(row[i].Tokens, styleContext)
			if i < numCols {
				naturalWidths[i] = maxInt(naturalWidths[i], VisibleWidth(cellText))
				minWordWidths[i] = maxInt(minWordWidths[i], m.getLongestWordWidth(cellText, maxUnbrokenWordWidth, true))
			}
		}
	}

	minColumnWidths := append([]int(nil), minWordWidths...)
	minCellsWidth := sumInts(minColumnWidths)

	if minCellsWidth > availableForCells {
		minColumnWidths = make([]int, numCols)
		for i := range minColumnWidths {
			minColumnWidths[i] = 1
		}
		remaining := availableForCells - numCols

		if remaining > 0 {
			totalWeight := 0
			for _, width := range minWordWidths {
				totalWeight += maxInt(0, width-1)
			}
			growth := make([]int, numCols)
			for i, width := range minWordWidths {
				weight := maxInt(0, width-1)
				if totalWeight > 0 {
					growth[i] = weight * remaining / totalWeight
				}
			}
			for i := 0; i < numCols; i++ {
				minColumnWidths[i] += growth[i]
			}
			allocated := sumInts(growth)
			leftover := remaining - allocated
			for i := 0; leftover > 0 && i < numCols; i++ {
				minColumnWidths[i]++
				leftover--
			}
		}
		minCellsWidth = sumInts(minColumnWidths)
	}

	totalNaturalWidth := sumInts(naturalWidths) + borderOverhead
	var columnWidths []int

	if totalNaturalWidth <= availableWidth {
		columnWidths = make([]int, numCols)
		for i := 0; i < numCols; i++ {
			columnWidths[i] = maxInt(naturalWidths[i], minColumnWidths[i])
		}
	} else {
		totalGrowPotential := 0
		for i := 0; i < numCols; i++ {
			totalGrowPotential += maxInt(0, naturalWidths[i]-minColumnWidths[i])
		}
		extraWidth := maxInt(0, availableForCells-minCellsWidth)
		columnWidths = make([]int, numCols)
		for i := 0; i < numCols; i++ {
			naturalWidth := naturalWidths[i]
			minWidthDelta := maxInt(0, naturalWidth-minColumnWidths[i])
			grow := 0
			if totalGrowPotential > 0 {
				grow = minWidthDelta * extraWidth / totalGrowPotential
			}
			columnWidths[i] = minColumnWidths[i] + grow
		}

		allocated := sumInts(columnWidths)
		remaining := availableForCells - allocated
		for remaining > 0 {
			grew := false
			for i := 0; i < numCols && remaining > 0; i++ {
				if columnWidths[i] < naturalWidths[i] {
					columnWidths[i]++
					remaining--
					grew = true
				}
			}
			if !grew {
				break
			}
		}
	}

	topBorderCells := make([]string, numCols)
	for i, w := range columnWidths {
		topBorderCells[i] = strings.Repeat("─", maxInt(0, w))
	}
	lines = append(lines, "┌─"+strings.Join(topBorderCells, "─┬─")+"─┐")

	headerCellLines := make([][]string, numCols)
	for i, cell := range token.Header {
		text := m.renderInlineTokens(cell.Tokens, styleContext)
		headerCellLines[i] = m.wrapCellText(text, columnWidths[i], stylePrefixOf(styleContext))
	}
	headerLineCount := maxLineCount(headerCellLines)

	for lineIdx := 0; lineIdx < headerLineCount; lineIdx++ {
		rowParts := make([]string, numCols)
		for colIdx := 0; colIdx < numCols; colIdx++ {
			text := ""
			if lineIdx < len(headerCellLines[colIdx]) {
				text = headerCellLines[colIdx][lineIdx]
			}
			padded := text + repeatSpaces(maxInt(0, columnWidths[colIdx]-VisibleWidth(text)))
			rowParts[colIdx] = m.Theme.Bold(padded)
		}
		lines = append(lines, "│ "+strings.Join(rowParts, " │ ")+" │")
	}

	separatorCells := make([]string, numCols)
	for i, w := range columnWidths {
		separatorCells[i] = strings.Repeat("─", maxInt(0, w))
	}
	separatorLine := "├─" + strings.Join(separatorCells, "─┼─") + "─┤"
	lines = append(lines, separatorLine)

	for rowIndex, row := range token.Rows {
		rowCellLines := make([][]string, numCols)
		for i := 0; i < numCols; i++ {
			text := ""
			if i < len(row) {
				text = m.renderInlineTokens(row[i].Tokens, styleContext)
			}
			rowCellLines[i] = m.wrapCellText(text, columnWidths[i], stylePrefixOf(styleContext))
		}
		rowLineCount := maxLineCount(rowCellLines)

		for lineIdx := 0; lineIdx < rowLineCount; lineIdx++ {
			rowParts := make([]string, numCols)
			for colIdx := 0; colIdx < numCols; colIdx++ {
				text := ""
				if lineIdx < len(rowCellLines[colIdx]) {
					text = rowCellLines[colIdx][lineIdx]
				}
				rowParts[colIdx] = text + repeatSpaces(maxInt(0, columnWidths[colIdx]-VisibleWidth(text)))
			}
			lines = append(lines, "│ "+strings.Join(rowParts, " │ ")+" │")
		}

		if rowIndex < len(token.Rows)-1 {
			lines = append(lines, separatorLine)
		}
	}

	bottomBorderCells := make([]string, numCols)
	for i, w := range columnWidths {
		bottomBorderCells[i] = strings.Repeat("─", maxInt(0, w))
	}
	lines = append(lines, "└─"+strings.Join(bottomBorderCells, "─┴─")+"─┘")

	if nextTokenType != "" && nextTokenType != "space" {
		lines = append(lines, "")
	}
	return lines
}

func stylePrefixOf(styleContext *inlineStyleContext) string {
	if styleContext == nil {
		return ""
	}
	return styleContext.stylePrefix
}

func sumInts(values []int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}

func maxLineCount(lines [][]string) int {
	max := 0
	for _, entry := range lines {
		if len(entry) > max {
			max = len(entry)
		}
	}
	return max
}

var _ Component = (*Markdown)(nil)

// Hyperlink wraps text in an OSC 8 hyperlink.
func Hyperlink(text string, url string) string {
	terminator := "\x1b\\"
	if strings.Contains(text, "\x07") {
		terminator = "\x07"
	}
	return "\x1b]8;;" + url + terminator + text + "\x1b]8;;" + terminator
}

// RenderLatexOptions configure the LaTeX renderer.
type RenderLatexOptions struct {
	// Display stacks fractions and operator limits vertically.
	Display bool
}

// RenderLatex renders a LaTeX math expression as terminal-friendly Unicode.
// It reports false when the expression contains unsupported or malformed
// syntax (upstream returns undefined). See latex.go: the full renderer lands
// in a later round (divergence D68).
func RenderLatex(source string, options RenderLatexOptions) (string, bool) {
	return renderLatexImpl(source, options)
}
