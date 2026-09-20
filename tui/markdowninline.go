package tui

import (
	"regexp"
	"strings"
)

// Inline half of the Marked-compatible lexer (divergence D67).

var (
	mdInlineAutolinkRegex = regexp.MustCompile(`^<([a-zA-Z][a-zA-Z0-9+.-]{1,31}:[^<> \t]*)>`)
	mdInlineEmailRegex    = regexp.MustCompile(`^<([a-zA-Z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*)>`)
	mdInlineHTMLRegex     = regexp.MustCompile(`^<!--[\s\S]*?-->|^</?[a-zA-Z][a-zA-Z0-9-]*(?:\s[^<>]*)?/?>|^<[a-zA-Z][a-zA-Z0-9-]*(?:\s[^<>]*)?>`)
	mdInlineLinkRegex     = regexp.MustCompile(`^\[((?:[^\[\]]|\[[^\]]*\])*)\]\(([^()\s]*(?:\([^()]*\)[^()\s]*)*)(?:\s+"([^"]*)")?\)`)
	mdInlineImageRegex    = regexp.MustCompile(`^!\[((?:[^\[\]]|\[[^\]]*\])*)\]\(([^()\s]*(?:\([^()]*\)[^()\s]*)*)(?:\s+"([^"]*)")?\)`)
	mdEscapeRegex         = regexp.MustCompile("^\\\\([!\"#$%&'()*+,\\-./:;<=>?@\\[\\\\\\]^_`{|}~])")
	mdLatexInlineRegex    = regexp.MustCompile(`^\$([^$\n]+)\$|^\\\(([\s\S]*?)\\\)|^\\\[([\s\S]*?)\\\]`)
)

type mdInline struct {
	lexer *mdLexer
}

// lexInline lexes inline tokens.
func (l *mdLexer) lexInline(source string) []*MdToken {
	if source == "" {
		return nil
	}
	return l.lexInlineTokens(source)
}

func (l *mdLexer) lexInlineTokens(source string) []*MdToken {
	var tokens []*MdToken
	position := 0
	textStart := 0

	flushText := func(end int) {
		if end <= textStart {
			return
		}
		raw := source[textStart:end]
		tokens = append(tokens, &MdToken{Type: "text", Raw: raw, Text: raw})
	}

	for position < len(source) {
		char := source[position]

		// The LaTeX extension runs before the built-in inline rules.
		// LaTeX inline extension.
		if char == '$' || (char == '\\' && position+1 < len(source) && (source[position+1] == '(' || source[position+1] == '[')) {
			if token, length, ok := lexLatexInline(source[position:]); ok {
				flushText(position)
				tokens = append(tokens, token)
				position += length
				textStart = position
				continue
			}
		}

		// Backslash escape.
		if char == '\\' {
			if match := mdEscapeRegex.FindStringSubmatch(source[position:]); match != nil {
				flushText(position)
				tokens = append(tokens, &MdToken{Type: "escape", Raw: match[0], Text: match[1]})
				position += len(match[0])
				textStart = position
				continue
			}
		}

		// Hard break: two trailing spaces before a newline, or a backslash.
		if char == '\n' {
			if position >= 2 && source[position-1] == ' ' && source[position-2] == ' ' {
				flushText(position - 2)
				tokens = append(tokens, &MdToken{Type: "br", Raw: "  \n"})
				position++
				textStart = position
				continue
			}
			position++
			continue
		}
		if char == '\\' && position+1 < len(source) && source[position+1] == '\n' {
			flushText(position)
			tokens = append(tokens, &MdToken{Type: "br", Raw: "\\\n"})
			position += 2
			textStart = position
			continue
		}

		// Code span.
		if char == '`' {
			if token, length, ok := lexCodespan(source[position:]); ok {
				flushText(position)
				tokens = append(tokens, token)
				position += length
				textStart = position
				continue
			}
		}

		// Image.
		if char == '!' && position+1 < len(source) && source[position+1] == '[' {
			if match := mdInlineImageRegex.FindStringSubmatch(source[position:]); match != nil {
				flushText(position)
				tokens = append(tokens, &MdToken{
					Type:   "image",
					Raw:    match[0],
					Text:   match[1],
					Href:   unescapeMarkdown(match[2]),
					Title:  match[3],
					Tokens: l.lexInlineTokens(match[1]),
				})
				position += len(match[0])
				textStart = position
				continue
			}
		}

		// Link.
		if char == '[' {
			if match := mdInlineLinkRegex.FindStringSubmatch(source[position:]); match != nil {
				flushText(position)
				tokens = append(tokens, &MdToken{
					Type:   "link",
					Raw:    match[0],
					Text:   match[1],
					Href:   unescapeMarkdown(match[2]),
					Title:  match[3],
					Tokens: l.lexInlineTokens(match[1]),
				})
				position += len(match[0])
				textStart = position
				continue
			}
		}

		// Autolink, email autolink, and inline HTML.
		if char == '<' {
			if match := mdInlineAutolinkRegex.FindStringSubmatch(source[position:]); match != nil {
				flushText(position)
				text := match[1]
				tokens = append(tokens, &MdToken{
					Type:   "link",
					Raw:    match[0],
					Text:   text,
					Href:   text,
					Tokens: []*MdToken{{Type: "text", Raw: text, Text: text}},
				})
				position += len(match[0])
				textStart = position
				continue
			}
			if match := mdInlineEmailRegex.FindStringSubmatch(source[position:]); match != nil {
				flushText(position)
				text := match[1]
				tokens = append(tokens, &MdToken{
					Type:   "link",
					Raw:    match[0],
					Text:   text,
					Href:   "mailto:" + text,
					Tokens: []*MdToken{{Type: "text", Raw: text, Text: text}},
				})
				position += len(match[0])
				textStart = position
				continue
			}
			if match := mdInlineHTMLRegex.FindStringSubmatch(source[position:]); match != nil {
				flushText(position)
				tokens = append(tokens, &MdToken{Type: "html", Raw: match[0], Text: match[0]})
				position += len(match[0])
				textStart = position
				continue
			}
		}

		// Emphasis and strong.
		if char == '*' || char == '_' {
			before := byte(0)
			if position > 0 {
				before = source[position-1]
			}
			if token, length, ok := lexEmphasis(l, source[position:], char, before); ok {
				flushText(position)
				tokens = append(tokens, token)
				position += length
				textStart = position
				continue
			}
		}

		// Strikethrough (strict rule from markdown.ts).
		if char == '~' && strings.HasPrefix(source[position:], "~~") {
			if token, length, ok := lexStrikethrough(l, source[position:]); ok {
				flushText(position)
				tokens = append(tokens, token)
				position += length
				textStart = position
				continue
			}
		}

		position++
	}

	flushText(len(source))
	return tokens
}

// lexCodespan lexes a backtick code span (RE2 has no backreferences, so the
// delimiter run is matched manually).
func lexCodespan(source string) (*MdToken, int, bool) {
	run := 0
	for run < len(source) && source[run] == '`' {
		run++
	}
	if run == 0 {
		return nil, 0, false
	}
	delimiter := strings.Repeat("`", run)
	rest := source[run:]
	index := 0
	for {
		found := strings.Index(rest[index:], delimiter)
		if found < 0 {
			return nil, 0, false
		}
		found += index
		// The closing run must not be followed by another backtick (which
		// would extend the run).
		if found+run < len(rest) && rest[found+run] == '`' {
			index = found + run
			continue
		}
		content := rest[:found]
		raw := source[:run+found+run]
		text := strings.TrimSpace(strings.ReplaceAll(content, "\n", " "))
		if len(content) >= 2 && strings.HasPrefix(content, " ") && strings.HasSuffix(content, " ") &&
			strings.TrimSpace(content) != "" {
			text = content[1 : len(content)-1]
		}
		return &MdToken{Type: "codespan", Raw: raw, Text: text}, len(raw), true
	}
}

// lexLatexInline implements the inline LaTeX tokenizer extension.
func lexLatexInline(source string) (*MdToken, int, bool) {
	var opening, closing string
	switch {
	case strings.HasPrefix(source, "$$"):
		opening, closing = "$$", "$$"
	case strings.HasPrefix(source, "\\("):
		opening, closing = "\\(", "\\)"
	case strings.HasPrefix(source, "$"):
		opening, closing = "$", "$"
	case strings.HasPrefix(source, "\\["):
		opening, closing = "\\[", "\\]"
	default:
		return nil, 0, false
	}

	closingIndex := findUnescapedDelimiter(source, closing, len(opening))
	if closingIndex < 0 {
		return nil, 0, false
	}
	text := source[len(opening):closingIndex]
	if opening == "$" && (text == "" || strings.HasPrefix(text, " ") || strings.HasSuffix(text, " ")) {
		return nil, 0, false
	}
	raw := source[:closingIndex+len(closing)]
	return &MdToken{Type: "latex", Raw: raw, Text: text}, len(raw), true
}

func findUnescapedDelimiter(source string, closing string, start int) int {
	index := strings.Index(source[start:], closing)
	if index < 0 {
		return -1
	}
	index += start
	for index >= 0 && isEscapedAt(source, index) {
		next := strings.Index(source[index+len(closing):], closing)
		if next < 0 {
			return -1
		}
		index = index + len(closing) + next
	}
	return index
}

// isWordByte reports whether a byte is alphanumeric or underscore (the
// intraword emphasis test).
func isWordByte(char byte) bool {
	return char == '_' || (char >= '0' && char <= '9') || (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z')
}

func isEscapedAt(source string, index int) bool {
	backslashes := 0
	for position := index - 1; position >= 0 && source[position] == '\\'; position-- {
		backslashes++
	}
	return backslashes%2 == 1
}

// lexEmphasis lexes emphasis/strong runs.
func lexEmphasis(l *mdLexer, source string, marker byte, before byte) (*MdToken, int, bool) {
	// Underscores inside a word do not open emphasis (CommonMark).
	if marker == '_' && isWordByte(before) {
		return nil, 0, false
	}
	runLength := 0
	for runLength < len(source) && source[runLength] == marker {
		runLength++
	}
	if runLength == 0 || runLength > 3 {
		// Runs longer than three are treated as plain text by Marked's
		// tokenizer for our purposes.
		return nil, 0, false
	}
	// The character after the opening run must not be whitespace.
	if runLength >= len(source) || source[runLength] == ' ' || source[runLength] == '\n' {
		return nil, 0, false
	}

	// A run of three is emphasis wrapping the remaining two delimiters:
	// "***x***" becomes em("**x**"), which lexes to em(strong(x)).
	if runLength == 3 {
		runStart := findNextRun(source, marker, runLength)
		if runStart < 0 {
			return nil, 0, false
		}
		end := runStart
		for end < len(source) && source[end] == marker {
			end++
		}
		closingDelimiter := end - 1
		inner := source[1:closingDelimiter]
		if inner == "" || strings.HasSuffix(inner, " ") || strings.HasSuffix(inner, "\n") {
			return nil, 0, false
		}
		raw := source[:closingDelimiter+1]
		return &MdToken{Type: "em", Raw: raw, Text: inner, Tokens: l.lexInlineTokens(inner)}, len(raw), true
	}

	opening := source[:runLength]
	closingIndex := findClosingRun(source, opening, runLength)
	if closingIndex < 0 {
		return nil, 0, false
	}
	inner := source[runLength:closingIndex]
	if inner == "" || strings.HasSuffix(inner, " ") || strings.HasSuffix(inner, "\n") {
		return nil, 0, false
	}

	raw := source[:closingIndex+runLength]
	tokenType := "em"
	if runLength == 2 {
		tokenType = "strong"
	}
	token := &MdToken{Type: tokenType, Raw: raw, Text: inner, Tokens: l.lexInlineTokens(inner)}
	return token, len(raw), true
}

// findNextRun returns the start index of the next marker run at or after
// from.
func findNextRun(source string, marker byte, from int) int {
	for index := from; index < len(source); index++ {
		if source[index] == marker {
			return index
		}
	}
	return -1
}

// findClosingRun returns the index of the matching delimiter run.
func findClosingRun(source string, opening string, runLength int) int {
	position := runLength
	for position < len(source) {
		index := strings.Index(source[position:], opening)
		if index < 0 {
			return -1
		}
		index += position
		// The run must be exactly runLength long (a longer run belongs to an
		// outer delimiter).
		end := index
		for end < len(source) && source[end] == opening[0] {
			end++
		}
		runLen := end - index
		if runLen < runLength {
			position = end
			continue
		}
		if runLen > runLength {
			// Use the trailing part of the longer run.
			index = end - runLength
		}
		if index > 0 && source[index-1] == '\\' {
			position = index + runLength
			continue
		}
		if opening[0] == '_' && index+runLength < len(source) && isWordByte(source[index+runLength]) {
			position = index + runLength
			continue
		}
		return index
	}
	return -1
}

// lexStrikethrough applies the strict strikethrough rule from markdown.ts.
func lexStrikethrough(l *mdLexer, source string) (*MdToken, int, bool) {
	if !strings.HasPrefix(source, "~~") {
		return nil, 0, false
	}
	rest := source[2:]
	if rest == "" || rest[0] == ' ' || rest[0] == '~' {
		return nil, 0, false
	}
	// Find the closing "~~" that is preceded by a non-space, non-backslash
	// character and followed by a non-"~".
	position := 0
	for position < len(rest) {
		index := strings.Index(rest[position:], "~~")
		if index < 0 {
			return nil, 0, false
		}
		index += position
		before := rest[index-1]
		after := byte(0)
		if index+2 < len(rest) {
			after = rest[index+2]
		}
		if before != ' ' && before != '~' && before != '\\' && after != '~' {
			inner := rest[:index]
			if strings.HasSuffix(inner, "\\") {
				position = index + 2
				continue
			}
			inner = unescapeMarkdown(inner)
			raw := source[:index+4]
			return &MdToken{Type: "del", Raw: raw, Text: inner, Tokens: l.lexInlineTokens(source[2 : index+2])}, len(raw), true
		}
		position = index + 2
	}
	return nil, 0, false
}

// unescapeMarkdown removes backslash escapes from a URL or text fragment.
func unescapeMarkdown(text string) string {
	if !strings.Contains(text, "\\") {
		return text
	}
	var builder strings.Builder
	position := 0
	for position < len(text) {
		if match := mdEscapeRegex.FindStringSubmatch(text[position:]); match != nil {
			builder.WriteString(match[1])
			position += len(match[0])
			continue
		}
		builder.WriteByte(text[position])
		position++
	}
	return builder.String()
}
