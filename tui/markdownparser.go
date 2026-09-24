package tui

import (
	"regexp"
	"strings"
)

// Port of the tokenizer half of src/components/markdown.ts: a
// Marked-compatible block and inline lexer producing the token tree the
// Markdown renderer consumes.
//
// Upstream uses the `marked` package (CommonMark + GFM) plus the strict
// strikethrough tokenizer and the LaTeX block/inline extensions. Go has no
// markdown dependency available offline, so this is a built-in subset lexer
// covering the constructs the renderer styles (divergence D67): reference
// links, HTML block subtleties, and some CommonMark corner cases are not
// reproduced. tui/testdata/md_golden.txt records the corpus verified against
// upstream.

// MdToken is a markdown token. Fields mirror the Marked token shapes the
// renderer reads.
type MdToken struct {
	Type  string
	Raw   string
	Text  string
	Depth int

	Ordered  bool
	Start    int
	HasStart bool

	Href  string
	Title string
	Lang  string

	// Align is the table alignment row; cells use CellAlign.
	Align        []string
	CellAlign    string
	HasCellAlign bool

	Items  []*MdToken
	Tokens []*MdToken
	Header []*MdToken
	Rows   [][]*MdToken

	Task    bool
	Checked bool
	Pending bool
	// Loose marks a list whose items are separated by blank lines.
	Loose bool

	// textSource is the item content with the task marker removed (used to
	// build the list item text; not part of the token projection).
	textSource    string
	hasTextSource bool
}

// LexMarkdown lexes markdown into Marked-compatible tokens.
func LexMarkdown(source string) []*MdToken {
	lexer := &mdLexer{source: source}
	return lexer.lexBlocks(source, false)
}

type mdLexer struct {
	source string
}

var (
	mdBlankLineRegex         = regexp.MustCompile(`^[ \t]*$`)
	mdATXHeadingRegex        = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?[ \t]*$`)
	mdSetextH1Regex          = regexp.MustCompile(`^ {0,3}=+[ \t]*$`)
	mdSetextH2Regex          = regexp.MustCompile(`^ {0,3}-+[ \t]*$`)
	mdHrRegex                = regexp.MustCompile(`^ {0,3}(?:(?:\*[ \t]*){3,}|(?:-[ \t]*){3,}|(?:_[ \t]*){3,})$`)
	mdFenceStartRegex        = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})[ \\t]*([^`\\n]*)$")
	mdFenceEndRegex          = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})[ \\t]*$")
	mdBlockquoteRegex        = regexp.MustCompile(`^ {0,3}>[ ]?`)
	mdListItemRegex          = regexp.MustCompile(`^( {0,3})([-+*]|\d{1,9}[.)])([ \t]+|$)`)
	mdTaskMarkerRegex        = regexp.MustCompile(`^\[([ xX])\][ \t]+`)
	mdTableDelimiterRegex    = regexp.MustCompile(`^ {0,3}\|?[ \t]*:?-+:?[ \t]*(\|[ \t]*:?-+:?[ \t]*)*\|?[ \t]*$`)
	mdIndentedCodeRegex      = regexp.MustCompile(`^(?: {4}|\t)`)
	mdHtmlBlockStartRegex    = regexp.MustCompile(`^ {0,3}<(?:[a-zA-Z][a-zA-Z0-9-]*(?:[ \t]|/?>)|/[a-zA-Z][a-zA-Z0-9-]*[ \t]*>|!--|\?|![a-zA-Z])`)
	mdLatexBlockStartRegex   = regexp.MustCompile(`^ {0,3}(?:\$\$|\\\[)`)
	mdLatexDollarBlockRegex  = regexp.MustCompile(`(?s)^ {0,3}\$\$[ \t]*(?:\n)?([\s\S]*?)\$\$[ \t]*(?:\n|$)`)
	mdLatexBracketBlockRegex = regexp.MustCompile(`(?s)^ {0,3}\\\[[ \t]*(?:\n)?([\s\S]*?)\\\][ \t]*(?:\n|$)`)
	mdPendingBracketRegex    = regexp.MustCompile(`(?s)^ {0,3}\\\[[ \t]*(?:\n)?([\s\S]*)$`)
	mdPendingDollarRegex     = regexp.MustCompile(`(?s)^ {0,3}\$\$[ \t]*(?:\n)?([\s\S]*)$`)
	mdPendingDollarMathRegex = regexp.MustCompile(`\\[A-Za-z]+|[_^=+*/<>()\[\]|±≤≥≠≈∈→⇒∞∫∑√-]`)
)

// lexBlocks lexes block-level tokens from source. atEnd marks the final block
// of the source, whose raw includes the trailing newline like Marked's.
func (l *mdLexer) lexBlocks(source string, atEnd bool) []*MdToken {
	var tokens []*MdToken
	remaining := source

	for len(remaining) > 0 {
		// Blank-line run becomes a space token.
		if mdBlankLineRegex.MatchString(firstLine(remaining)) {
			run := consumeBlankLines(remaining)
			tokens = append(tokens, &MdToken{Type: "space", Raw: rawWithTrailingNewline(run.consumed, remaining, run.rest)})
			remaining = run.rest
			continue
		}

		// LaTeX block extension (Marked runs extensions before the built-ins).
		if mdLatexBlockStartRegex.MatchString(remaining) {
			if token := lexBlockLatex(remaining); token != nil {
				rest := remaining[len(token.Raw):]
				// A single blank-line separator after the block is absorbed
				// into the raw (Marked's extension-token behaviour).
				if strings.HasPrefix(rest, "\n") && len(rest) > 1 && rest[1] != '\n' {
					token.Raw += "\n"
					rest = rest[1:]
				}
				tokens = append(tokens, token)
				remaining = rest
				continue
			}
		}

		// Fenced code.
		if match := mdFenceStartRegex.FindStringSubmatch(firstLine(remaining)); match != nil {
			token, rest := lexFencedCode(remaining, match[1], strings.TrimSpace(match[2]))
			tokens = append(tokens, token)
			remaining = rest
			continue
		}

		// ATX heading.
		if match := mdATXHeadingRegex.FindStringSubmatch(firstLine(remaining)); match != nil {
			content := strings.TrimRight(match[2], " \t")
			line := mdNextLine(remaining)
			raw := line.text
			rest := remaining[line.length:]
			if rest == "" && line.hasNewline {
				raw += "\n"
			}
			tokens = append(tokens, &MdToken{
				Type:   "heading",
				Raw:    raw,
				Text:   content,
				Depth:  len(match[1]),
				Tokens: l.lexInline(content),
			})
			remaining = rest
			continue
		}

		// Horizontal rule.
		if mdHrRegex.MatchString(firstLine(remaining)) {
			line := mdNextLine(remaining)
			rest := remaining[line.length:]
			tokens = append(tokens, &MdToken{Type: "hr", Raw: rawWithTrailingNewline(line.text, remaining, rest)})
			remaining = rest
			continue
		}

		// Blockquote.
		if mdBlockquoteRegex.MatchString(firstLine(remaining)) {
			content, rest := consumeBlockquote(remaining)
			content = strings.TrimRight(content, "\n")
			tokens = append(tokens, &MdToken{
				Type:   "blockquote",
				Text:   strings.TrimRight(content, "\n"),
				Tokens: l.lexBlocks(content, false),
			})
			remaining = rest
			continue
		}

		// List.
		if mdListItemRegex.MatchString(firstLine(remaining)) {
			token, rest := l.lexList(remaining)
			tokens = append(tokens, token)
			remaining = rest
			continue
		}

		// HTML block (basic: until a blank line).
		if mdHtmlBlockStartRegex.MatchString(firstLine(remaining)) {
			content, rest := consumeUntilBlankLine(remaining)
			raw := rawWithTrailingNewline(content, remaining, rest)
			tokens = append(tokens, &MdToken{Type: "html", Raw: raw, Text: raw})
			remaining = rest
			continue
		}

		// Indented code.
		if mdIndentedCodeRegex.MatchString(remaining) {
			token, rest := lexIndentedCode(remaining)
			tokens = append(tokens, token)
			remaining = rest
			continue
		}

		// Table (a paragraph line followed by a delimiter row).
		if token, rest, ok := l.tryLexTable(remaining); ok {
			tokens = append(tokens, token)
			remaining = rest
			continue
		}

		// Setext heading.
		if token, rest, ok := l.tryLexSetextHeading(remaining); ok {
			tokens = append(tokens, token)
			remaining = rest
			continue
		}

		// Paragraph.
		token, rest := l.lexParagraph(remaining)
		tokens = append(tokens, token)
		remaining = rest
	}

	_ = atEnd
	return tokens
}

// mdLine is one source line with its terminator.
type mdLine struct {
	text       string // without the newline
	hasNewline bool
	length     int // bytes consumed including the newline
}

func mdNextLine(source string) mdLine {
	if index := strings.IndexByte(source, '\n'); index != -1 {
		return mdLine{text: source[:index], hasNewline: true, length: index + 1}
	}
	return mdLine{text: source, hasNewline: false, length: len(source)}
}

func firstLine(source string) string { return mdNextLine(source).text }

// rawWithTrailingNewline appends the newline that followed a block when the
// block was the last one in the source (Marked keeps it there and folds it
// into the following space token otherwise).
func rawWithTrailingNewline(content string, original string, rest string) string {
	if rest == "" && strings.HasSuffix(original, "\n") && !strings.HasSuffix(content, "\n") {
		return content + "\n"
	}
	return content
}

type blankRun struct {
	consumed string
	rest     string
}

func consumeBlankLines(source string) blankRun {
	position := 0
	for position < len(source) {
		line := mdNextLine(source[position:])
		if !mdBlankLineRegex.MatchString(line.text) {
			break
		}
		position += line.length
		if !line.hasNewline {
			break
		}
	}
	return blankRun{consumed: source[:position], rest: source[position:]}
}

func consumeUntilBlankLine(source string) (content string, rest string) {
	position := 0
	for position < len(source) {
		line := mdNextLine(source[position:])
		if mdBlankLineRegex.MatchString(line.text) {
			break
		}
		position += line.length
		if !line.hasNewline {
			break
		}
	}
	return source[:position], source[position:]
}

func consumeBlockquote(source string) (content string, rest string) {
	var builder strings.Builder
	position := 0
	for position < len(source) {
		line := mdNextLine(source[position:])
		if mdBlockquoteRegex.MatchString(line.text) {
			stripped := mdBlockquoteRegex.ReplaceAllString(line.text, "")
			builder.WriteString(stripped)
			builder.WriteString("\n")
			position += line.length
			continue
		}
		// A blank line ends the blockquote (the following space token keeps it).
		break
	}
	return builder.String(), source[position:]
}

func lexFencedCode(source string, marker string, lang string) (*MdToken, string) {
	fenceChar := marker[0]
	var body strings.Builder
	// Scan forward from the opening fence. The source here is the entire
	// remaining document, so splitting it into lines (the previous approach)
	// cost O(remaining) per fence and O(fences x remaining) per document —
	// measured at 15.9 GB of allocations for a 1MB fence-heavy render.
	position := 0
	if first := strings.IndexByte(source, '\n'); first == -1 {
		position = len(source)
	} else {
		position = first + 1
	}
	closed := false

	for position < len(source) {
		line := source[position:]
		if end := strings.IndexByte(line, '\n'); end != -1 {
			line = line[:end+1]
		}
		content := strings.TrimRight(line, "\n")
		if match := mdFenceEndRegex.FindStringSubmatch(content); match != nil &&
			match[1][0] == fenceChar && len(match[1]) >= len(marker) {
			// The closing fence's newline stays for the following space token
			// (or is folded into the raw at the end of the source).
			position += len(content)
			closed = true
			break
		}
		body.WriteString(line)
		position += len(line)
	}

	text := body.String()
	text = strings.TrimSuffix(text, "\n")
	raw := source[:position]
	rest := source[position:]
	// A newline directly after the closing fence belongs to the code block
	// unless a blank line follows (Marked folds the blank line into the next
	// space token instead).
	if strings.HasPrefix(rest, "\n") && (len(rest) == 1 || rest[1] != '\n') {
		raw += "\n"
		rest = rest[1:]
	}
	token := &MdToken{
		Type: "code",
		Raw:  raw,
		Text: text,
		Lang: lang,
	}
	_ = closed
	return token, rest
}

func splitLinesKeepEnds(source string) []string {
	var lines []string
	position := 0
	for position < len(source) {
		index := strings.IndexByte(source[position:], '\n')
		if index == -1 {
			lines = append(lines, source[position:])
			break
		}
		lines = append(lines, source[position:position+index+1])
		position += index + 1
	}
	return lines
}

func lexIndentedCode(source string) (*MdToken, string) {
	var body strings.Builder
	position := 0
	for position < len(source) {
		line := mdNextLine(source[position:])
		if mdIndentedCodeRegex.MatchString(line.text) {
			body.WriteString(stripIndent(line.text, 4))
			body.WriteString("\n")
		} else if mdBlankLineRegex.MatchString(line.text) {
			body.WriteString("\n")
		} else {
			break
		}
		position += line.length
		if !line.hasNewline {
			break
		}
	}
	// Indented code keeps a single trailing newline when the consumed source
	// ended with one (Marked).
	text := strings.TrimRight(body.String(), "\n")
	if text != "" && strings.HasSuffix(source[:position], "\n") {
		text += "\n"
	}
	return &MdToken{Type: "code", Raw: source[:position], Text: text}, source[position:]
}

func stripIndent(line string, width int) string {
	removed := 0
	for removed < width && removed < len(line) && line[removed] == ' ' {
		removed++
	}
	if removed == 0 && strings.HasPrefix(line, "\t") {
		return line[1:]
	}
	return line[removed:]
}

// tryLexTable detects a GFM table: a header row followed by a delimiter row.
func (l *mdLexer) tryLexTable(source string) (*MdToken, string, bool) {
	first := mdNextLine(source)
	if !first.hasNewline {
		return nil, "", false
	}
	second := mdNextLine(source[first.length:])
	headerLine := first.text
	delimiterLine := second.text
	if !strings.Contains(headerLine, "|") || !mdTableDelimiterRegex.MatchString(delimiterLine) {
		return nil, "", false
	}

	alignments, ok := parseTableAlignments(delimiterLine)
	if !ok {
		return nil, "", false
	}
	headerCells := splitTableRow(headerLine)
	if len(headerCells) == 0 {
		return nil, "", false
	}
	if len(headerCells) != len(alignments) {
		return nil, "", false
	}

	position := first.length + second.length
	var rows [][]*MdToken
	for position < len(source) {
		line := mdNextLine(source[position:])
		content := line.text
		if mdBlankLineRegex.MatchString(content) || !strings.Contains(content, "|") {
			break
		}
		cells := splitTableRow(content)
		row := make([]*MdToken, 0, len(cells))
		for index, cell := range cells {
			align := ""
			if index < len(alignments) {
				align = alignments[index]
			}
			row = append(row, &MdToken{Text: cell, Tokens: l.lexInline(cell), CellAlign: align, HasCellAlign: align != ""})
		}
		rows = append(rows, row)
		position += line.length
	}

	token := &MdToken{
		Type:   "table",
		Raw:    source[:position],
		Align:  alignments,
		Tokens: nil,
	}
	for index, cell := range headerCells {
		align := ""
		if index < len(alignments) {
			align = alignments[index]
		}
		token.Header = append(token.Header, &MdToken{Text: cell, Tokens: l.lexInline(cell), CellAlign: align, HasCellAlign: align != ""})
	}
	token.Rows = rows
	return token, source[position:], true
}

func parseTableAlignments(line string) ([]string, bool) {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")
	parts := strings.Split(trimmed, "|")
	alignments := make([]string, 0, len(parts))
	for _, part := range parts {
		cell := strings.TrimSpace(part)
		if cell == "" || !strings.Contains(cell, "-") {
			return nil, false
		}
		left := strings.HasPrefix(cell, ":")
		right := strings.HasSuffix(cell, ":")
		switch {
		case left && right:
			alignments = append(alignments, "center")
		case left:
			alignments = append(alignments, "left")
		case right:
			alignments = append(alignments, "right")
		default:
			alignments = append(alignments, "")
		}
	}
	return alignments, len(alignments) > 0
}

func splitTableRow(line string) []string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimPrefix(trimmed, "|")
	trimmed = strings.TrimSuffix(trimmed, "|")
	parts := strings.Split(trimmed, "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cells = append(cells, strings.TrimSpace(part))
	}
	return cells
}

// tryLexSetextHeading detects "text\n===" / "text\n---" headings. Marked
// allows a multi-line paragraph before the underline.
func (l *mdLexer) tryLexSetextHeading(source string) (*MdToken, string, bool) {
	var textLines []string
	position := 0
	for position < len(source) {
		line := mdNextLine(source[position:])
		if mdBlankLineRegex.MatchString(line.text) {
			return nil, "", false
		}
		if len(textLines) > 0 {
			depth := 0
			if mdSetextH1Regex.MatchString(line.text) {
				depth = 1
			} else if mdSetextH2Regex.MatchString(line.text) {
				depth = 2
			}
			if depth > 0 {
				text := strings.Join(textLines, "\n")
				endPosition := position + line.length
				return &MdToken{
					Type:   "heading",
					Raw:    source[:endPosition],
					Text:   text,
					Depth:  depth,
					Tokens: l.lexInline(text),
				}, source[endPosition:], true
			}
		}
		textLines = append(textLines, line.text)
		position += line.length
		if !line.hasNewline {
			break
		}
	}
	return nil, "", false
}

func (l *mdLexer) lexParagraph(source string) (*MdToken, string) {
	var rawLines []string
	position := 0
	for position < len(source) {
		line := mdNextLine(source[position:])
		if mdBlankLineRegex.MatchString(line.text) {
			break
		}
		// A new block starts here.
		if len(rawLines) > 0 && l.startsNewBlock(line.text) {
			break
		}
		// A GFM table interrupts the paragraph (marked: a line containing a
		// pipe followed by a delimiter row ends the paragraph and lexes as a
		// table; the hotkeys/export tables follow bold headings this way).
		if len(rawLines) > 0 && strings.Contains(line.text, "|") {
			next := mdNextLine(source[position+line.length:])
			if mdTableDelimiterRegex.MatchString(next.text) {
				break
			}
		}
		rawLines = append(rawLines, line.text)
		position += line.length
	}

	raw := strings.Join(rawLines, "\n")
	text := strings.Join(rawLines, "\n")
	if position == len(source) && strings.HasSuffix(source, "\n") {
		raw += "\n"
	}
	return &MdToken{
		Type:   "paragraph",
		Raw:    raw,
		Text:   text,
		Tokens: l.lexInline(text),
	}, source[position:]
}

// startsNewBlock reports whether a line begins a new block (used to terminate
// a paragraph).
func (l *mdLexer) startsNewBlock(line string) bool {
	if mdBlankLineRegex.MatchString(line) {
		return true
	}
	if mdATXHeadingRegex.MatchString(line) || mdHrRegex.MatchString(line) ||
		mdFenceStartRegex.MatchString(line) || mdBlockquoteRegex.MatchString(line) ||
		mdListItemRegex.MatchString(line) || mdHtmlBlockStartRegex.MatchString(line) {
		return true
	}
	return false
}

// lexList lexes a list block. Marked's list item text is approximated: tight
// items keep their raw content without trailing newlines, loose items add the
// newline of the blank line that made the list loose (divergence D67).
func (l *mdLexer) lexList(source string) (*MdToken, string) {
	firstMatch := mdListItemRegex.FindStringSubmatch(firstLine(source))
	ordered := firstMatch[2][0] >= '0' && firstMatch[2][0] <= '9'
	start := 1
	if ordered {
		start = atoiSafe(strings.TrimRight(firstMatch[2], ".)"))
	}
	bullet := firstMatch[2][len(firstMatch[2])-1]

	type rawItem struct {
		raw           string
		content       string
		trailingBlank bool
	}
	var rawItems []rawItem
	position := 0
	loose := false

	for position < len(source) {
		line := mdNextLine(source[position:])
		match := mdListItemRegex.FindStringSubmatch(line.text)
		if match == nil {
			break
		}
		sameKind := (match[2][0] >= '0' && match[2][0] <= '9') == ordered
		sameBullet := match[2][len(match[2])-1] == bullet || ordered
		if !sameKind || !sameBullet {
			break
		}

		markerWidth := len(match[1]) + len(match[2]) + len(match[3])
		itemLines := []string{line.text[markerWidth:]}
		itemPosition := position + line.length
		itemConsumed := itemPosition - position

		// Continuation lines: more-indented content, nested lists, or lazy
		// paragraph continuation (which ends the item).
		for itemPosition < len(source) {
			continuation := mdNextLine(source[itemPosition:])
			if mdBlankLineRegex.MatchString(continuation.text) {
				// A blank line followed by an indented line belongs to the item
				// and makes the list loose.
				peek := itemPosition
				for peek < len(source) {
					peekLine := mdNextLine(source[peek:])
					if !mdBlankLineRegex.MatchString(peekLine.text) {
						break
					}
					peek += peekLine.length
					if !peekLine.hasNewline {
						break
					}
				}
				if peek >= len(source) {
					break
				}
				nextLine := firstLine(source[peek:])
				if strings.HasPrefix(nextLine, " ") || strings.HasPrefix(nextLine, "\t") {
					loose = true
					itemLines = append(itemLines, "")
					itemConsumed = peek - position
					itemPosition = peek
					continue
				}
				break
			}
			if strings.HasPrefix(continuation.text, " ") || strings.HasPrefix(continuation.text, "\t") {
				itemLines = append(itemLines, stripIndent(continuation.text, markerWidth))
				itemConsumed = itemPosition + continuation.length - position
				itemPosition += continuation.length
				continue
			}
			break
		}

		content := strings.Join(itemLines, "\n")
		rawItems = append(rawItems, rawItem{raw: source[position : position+itemConsumed], content: content})
		position += itemConsumed

		// Skip blank lines between items, recording looseness.
		peek := position
		skippedBlanks := false
		for peek < len(source) {
			peekLine := mdNextLine(source[peek:])
			if !mdBlankLineRegex.MatchString(peekLine.text) {
				break
			}
			skippedBlanks = true
			peek += peekLine.length
			if !peekLine.hasNewline {
				break
			}
		}
		if skippedBlanks {
			rawItems[len(rawItems)-1].trailingBlank = true
		}
		if skippedBlanks && peek < len(source) && mdListItemRegex.MatchString(firstLine(source[peek:])) {
			loose = true
			position = peek
			continue
		}
		if peek >= len(source) {
			break
		}
	}

	token := &MdToken{Type: "list", Ordered: ordered, HasStart: ordered, Start: start}
	type pendingItem struct {
		item   *MdToken
		raw    rawItem
		blocks []*MdToken
	}
	var pending []pendingItem
	for _, item := range rawItems {
		listItem, itemLoose := l.lexListItem(item.content, item.raw)
		if itemLoose {
			loose = true
		}
		pending = append(pending, pendingItem{item: listItem, raw: item, blocks: listItem.Tokens})
	}

	for index, entry := range pending {
		tokens := entry.blocks
		if !loose {
			tokens = tightItemTokens(tokens, l)
		}
		item := entry.item
		item.Tokens = tokens
		// Only a blank line between items keeps the trailing newline in the
		// item text (Marked).
		textSource := entry.raw.content
		if item.hasTextSource {
			textSource = item.textSource
		}
		item.Text = itemTextFor(textSource, entry.raw.trailingBlank && index < len(pending)-1)
		token.Items = append(token.Items, item)
	}
	token.Loose = loose
	return token, source[position:]
}

// tightItemTokens converts an item's leading paragraph block into the text
// token Marked emits for tight list items.
func tightItemTokens(blocks []*MdToken, l *mdLexer) []*MdToken {
	tokens := make([]*MdToken, 0, len(blocks))
	for _, block := range blocks {
		if block.Type == "paragraph" {
			tokens = append(tokens, &MdToken{
				Type:   "text",
				Raw:    block.Raw,
				Text:   block.Text,
				Tokens: block.Tokens,
			})
			continue
		}
		tokens = append(tokens, block)
	}
	return tokens
}

// itemTextFor approximates Marked's list item text: items followed by a blank
// line keep a trailing newline (divergence D67).
func itemTextFor(content string, trailingBlank bool) string {
	trimmed := strings.TrimRight(content, "\n")
	if trailingBlank {
		return trimmed + "\n"
	}
	return trimmed
}

// lexListItem builds a list item token from its content, detecting task
// markers.
func (l *mdLexer) lexListItem(content string, raw string) (*MdToken, bool) {
	item := &MdToken{Type: "list_item", Raw: raw}

	trimmed := content
	if match := mdTaskMarkerRegex.FindStringSubmatch(trimmed); match != nil {
		item.Task = true
		item.Checked = match[1] == "x" || match[1] == "X"
		markerEnd := strings.Index(trimmed, "]")
		spaceEnd := markerEnd + 1
		for spaceEnd < len(trimmed) && (trimmed[spaceEnd] == ' ' || trimmed[spaceEnd] == '\t') {
			spaceEnd++
		}
		item.Tokens = append(item.Tokens, &MdToken{Type: "checkbox", Raw: trimmed[:spaceEnd]})
		trimmed = trimmed[spaceEnd:]
	}

	item.Tokens = append(item.Tokens, l.lexBlocks(trimmed, false)...)
	item.textSource = trimmed
	item.hasTextSource = true
	return item, strings.Contains(content, "\n\n")
}

// lexBlockLatex is the LaTeX block tokenizer extension.
func lexBlockLatex(source string) *MdToken {
	if match := mdLatexDollarBlockRegex.FindStringSubmatch(source); match != nil && match[1] != "" {
		return &MdToken{Type: "latexBlock", Raw: match[0], Text: strings.TrimSpace(match[1])}
	}
	if match := mdLatexBracketBlockRegex.FindStringSubmatch(source); match != nil && match[1] != "" {
		return &MdToken{Type: "latexBlock", Raw: match[0], Text: strings.TrimSpace(match[1])}
	}
	if match := mdPendingBracketRegex.FindStringSubmatch(source); match != nil {
		return &MdToken{Type: "latexBlock", Raw: match[0], Text: match[1], Pending: true}
	}
	if match := mdPendingDollarRegex.FindStringSubmatch(source); match != nil && match[1] != "" &&
		mdPendingDollarMathRegex.MatchString(match[1]) {
		return &MdToken{Type: "latexBlock", Raw: match[0], Text: match[1], Pending: true}
	}
	return nil
}

// TrimPartialClosingFences mirrors markdown.ts's trimPartialClosingFences.
func TrimPartialClosingFences(tokens []*MdToken) {
	if len(tokens) == 0 {
		return
	}
	token := tokens[len(tokens)-1]
	switch token.Type {
	case "list":
		if len(token.Items) > 0 {
			TrimPartialClosingFences(token.Items[len(token.Items)-1].Tokens)
		}
		return
	case "blockquote":
		TrimPartialClosingFences(token.Tokens)
		return
	case "code":
	default:
		return
	}

	match := mdFenceStartRegex.FindStringSubmatch(firstLine(token.Raw))
	if match == nil {
		return
	}
	marker := match[1]
	lines := strings.Split(token.Raw, "\n")
	lastLine := lines[len(lines)-1]
	if lastLine == "" || len(lastLine) >= len(marker) || lastLine != strings.Repeat(string(marker[0]), len(lastLine)) {
		return
	}
	token.Text = strings.TrimSuffix(token.Text[:len(token.Text)-len(lastLine)], "\n")
}
