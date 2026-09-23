package tui

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Port of src/latex.ts: the LaTeX math-to-Unicode renderer (data tables in
// latexdata.go, extracted from the upstream source).
//
// RE2 has no lookbehind/lookahead, so the named-operator spacing patterns and
// the \limits modifier match are implemented with manual scans (D70).

const (
	latexNamedOperatorStart = "\U000F0004"
	latexNamedOperatorEnd   = "\U000F0005"
	latexLayoutMarkerStart  = "\U000F0000"
	latexLayoutMarkerEnd    = "\U000F0001"
	latexProtectedSpace     = "\U000F0002"
	latexNegativeSpace      = "\u0000"
)

var (
	latexNamedOperatorLeftSpacing = regexp.MustCompile(`^\s$`)
	latexLayoutMarkerRegex        = regexp.MustCompile("\U000F0000(\\d+)\U000F0001")
	latexTrailingMarkerRegex      = regexp.MustCompile("\U000F0000(\\d+)\U000F0001$")
	latexWhitespaceRunRegex       = regexp.MustCompile(`[ \t]+`)
	// Row separators are double backslashes; Go raw strings need four to
	// express two literal backslashes.
	latexEnvironmentRowRegex  = regexp.MustCompile(`\\\\(?:\[[^\]\n]*\])?`)
	latexArrayColumnSpecRegex = regexp.MustCompile(`^\s*\{[^}]*\}`)
	latexCasesConditionRegex  = regexp.MustCompile(`^(?:if|when|for|otherwise)\b`)
	latexScriptRelationRegex  = regexp.MustCompile(`\s*([=+-])\s*`)
	latexSimpleWordRegex      = regexp.MustCompile(`^[\p{L}\p{N}.]+$`)
	// The fraction denominator test is numeric-only upstream (an alphabetic
	// denominator gets parentheses).
	latexSimpleNumericRegex   = regexp.MustCompile(`^[\p{N}.]+$`)
	latexLettersOnlyRegex     = regexp.MustCompile(`^[A-Za-z]+$`)
	latexUppercaseOrStarRegex = regexp.MustCompile(`[A-Z*∗]`)
	// RE2 has no lookahead, so the modifier matcher captures the following
	// character (or nothing at the end of input) instead.
	latexLimitsModifierRegex = regexp.MustCompile(`^\\(limits|nolimits)($|[^A-Za-z])`)
)

func replaceCharacters(value string, replacements map[string]string) (string, bool) {
	var builder strings.Builder
	for _, character := range value {
		replacement, ok := replacements[string(character)]
		if !ok {
			return "", false
		}
		builder.WriteString(replacement)
	}
	return builder.String(), true
}

func normalizeScriptValue(value string) string {
	return latexScriptRelationRegex.ReplaceAllString(strings.TrimSpace(value), "${1}")
}

func formatUnicodeScript(value string, kind string) (string, bool) {
	normalized := normalizeScriptValue(value)
	if kind == "sub" {
		return replaceCharacters(normalized, latexSubscripts)
	}
	return replaceCharacters(normalized, latexSuperscripts)
}

func formatScript(value string, kind string) string {
	value = normalizeScriptValue(value)
	if unicode, ok := formatUnicodeScript(value, kind); ok {
		return unicode
	}
	prefix := "^"
	if kind == "sub" {
		prefix = "_"
	}
	if utf8.RuneCountInString(value) == 1 || (kind == "sub" && latexLettersOnlyRegex.MatchString(value)) {
		return prefix + value
	}
	return prefix + "(" + value + ")"
}

func formatFraction(numerator string, denominator string) string {
	numerator = strings.TrimSpace(numerator)
	denominator = strings.TrimSpace(denominator)
	simpleNumerator := latexSimpleWordRegex.MatchString(numerator)
	simpleDenominator := latexSimpleNumericRegex.MatchString(denominator) || utf8.RuneCountInString(denominator) == 1
	if simpleNumerator {
		if simpleDenominator {
			return numerator + "/" + denominator
		}
		return numerator + "/(" + denominator + ")"
	}
	if simpleDenominator {
		return "(" + numerator + ")/" + denominator
	}
	return "(" + numerator + ")/(" + denominator + ")"
}

func formatRoot(value string, symbol string) string {
	value = strings.TrimSpace(value)
	if latexSimpleWordRegex.MatchString(value) {
		return symbol + value
	}
	return symbol + "(" + value + ")"
}

// normalizeOutput resolves the named-operator spacing markers and collapses
// whitespace.
func normalizeOutput(value string) string {
	// The left-spacing and right-spacing patterns use lookaround upstream;
	// RE2 cannot, so the markers are scanned manually.
	var builder strings.Builder
	runes := []rune(value)
	for index, character := range runes {
		switch string(character) {
		case latexNamedOperatorStart:
			if index > 0 && isLatexSpacingLeftNeighbor(runes[index-1]) {
				builder.WriteString(" ")
			}
		case latexNamedOperatorEnd:
			if index+1 < len(runes) && isLatexSpacingRightNeighbor(runes[index+1]) {
				builder.WriteString(" ")
			}
		default:
			builder.WriteRune(character)
		}
	}
	normalized := builder.String()
	normalized = strings.ReplaceAll(normalized, latexNamedOperatorStart, "")
	normalized = strings.ReplaceAll(normalized, latexNamedOperatorEnd, "")

	lines := strings.Split(normalized, "\n")
	kept := make([]string, 0, len(lines))
	for index, line := range lines {
		line = strings.TrimSpace(latexWhitespaceRunRegex.ReplaceAllString(line, " "))
		if line != "" || (index > 0 && index < len(lines)-1) {
			kept = append(kept, line)
		}
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}

func isLatexSpacingLeftNeighbor(r rune) bool {
	if isLetterRune(r) || isDigitRune(r) {
		return true
	}
	switch r {
	case ')', '}', ']', '\U000F0001':
		return true
	}
	return false
}

func isLatexSpacingRightNeighbor(r rune) bool {
	if isLetterRune(r) || isDigitRune(r) {
		return true
	}
	switch r {
	case '√', '\U000F0000':
		return true
	}
	return false
}

// ---- Layout engine ----

type latexLayoutNode struct {
	kind string // "fraction" | "operator" | "script" | "matrix"

	numerator   string
	denominator string

	operator string
	lower    string
	upper    string
	hasLower bool
	hasUpper bool

	lines    []string
	baseline int
}

type latexLayout struct {
	lines    []string
	width    int
	baseline int
}

func padLayoutLine(line string, width int, centered bool) string {
	padding := max(0, width-VisibleWidth(line))
	left := 0
	if centered {
		left = padding / 2
	}
	return repeatSpaces(left) + line + repeatSpaces(padding-left)
}

func joinLayouts(layouts []latexLayout) latexLayout {
	if len(layouts) == 0 {
		return latexLayout{lines: []string{""}, width: 0, baseline: 0}
	}
	baseline := 0
	for _, layout := range layouts {
		baseline = max(baseline, layout.baseline)
	}
	below := 0
	for _, layout := range layouts {
		below = max(below, len(layout.lines)-layout.baseline-1)
	}
	var lines []string
	for row := 0; row <= baseline+below; row++ {
		var builder strings.Builder
		for _, layout := range layouts {
			sourceRow := row - baseline + layout.baseline
			if sourceRow >= 0 && sourceRow < len(layout.lines) {
				builder.WriteString(padLayoutLine(layout.lines[sourceRow], layout.width, false))
			} else {
				builder.WriteString(repeatSpaces(layout.width))
			}
		}
		lines = append(lines, strings.TrimRight(builder.String(), " "))
	}
	width := 0
	for _, layout := range layouts {
		width += layout.width
	}
	return latexLayout{lines: lines, width: width, baseline: baseline}
}

func renderLayout(source string, nodes []*latexLayoutNode) latexLayout {
	var renderedLines []string
	firstBaseline := 0

	for _, sourceLine := range strings.Split(source, "\n") {
		var layouts []latexLayout
		position := 0
		var previousNode *latexLayoutNode

		matches := latexLayoutMarkerRegex.FindAllStringSubmatchIndex(sourceLine, -1)
		for _, match := range matches {
			index := match[0]
			nodeIndex := atoiSafe(sourceLine[match[2]:match[3]])
			if nodeIndex < 0 || nodeIndex >= len(nodes) {
				continue
			}
			node := nodes[nodeIndex]
			if index > position {
				sliced := sourceLine[position:index]
				trimmed := sliced
				if previousNode != nil {
					trimmed = strings.TrimLeft(trimmed, " \t\n\r\v\f\u00a0")
				}
				trimmed = strings.TrimRight(trimmed, " \t\n\r\v\f\u00a0")
				preserveLeadingSpace := previousNode != nil && previousNode.kind == "matrix" && startsWithWhitespace(sliced)
				preserveTrailingSpace := node.kind == "matrix" && endsWithWhitespace(sliced)
				text := ""
				if trimmed != "" {
					text = trimmed
					if preserveLeadingSpace {
						text = " " + text
					}
					if preserveTrailingSpace {
						text = text + " "
					}
				} else if preserveLeadingSpace || preserveTrailingSpace {
					text = " "
				}
				layouts = append(layouts, latexLayout{lines: []string{text}, width: VisibleWidth(text), baseline: 0})
			}

			switch node.kind {
			case "fraction":
				numerator := renderLayout(node.numerator, nodes)
				denominator := renderLayout(node.denominator, nodes)
				contentWidth := max(max(numerator.width, denominator.width), 1)
				width := contentWidth + 2
				var lines []string
				for _, line := range numerator.lines {
					lines = append(lines, padLayoutLine(line, width, true))
				}
				lines = append(lines, " "+strings.Repeat("─", contentWidth)+" ")
				for _, line := range denominator.lines {
					lines = append(lines, padLayoutLine(line, width, true))
				}
				layouts = append(layouts, latexLayout{lines: lines, width: width, baseline: len(numerator.lines)})

			case "operator":
				contentWidth := VisibleWidth(node.operator)
				if node.hasLower {
					contentWidth = max(contentWidth, VisibleWidth(node.lower))
				}
				if node.hasUpper {
					contentWidth = max(contentWidth, VisibleWidth(node.upper))
				}
				var lines []string
				if node.hasUpper {
					lines = append(lines, padLayoutLine(node.upper, contentWidth, true)+" ")
				}
				lines = append(lines, padLayoutLine(node.operator, contentWidth, true)+" ")
				if node.hasLower {
					lines = append(lines, padLayoutLine(node.lower, contentWidth, true)+" ")
				}
				baseline := 0
				if node.hasUpper {
					baseline = 1
				}
				layouts = append(layouts, latexLayout{lines: lines, width: contentWidth + 1, baseline: baseline})

			case "script":
				var upper, lower *latexLayout
				if node.hasUpper {
					layout := renderLayout(node.upper, nodes)
					upper = &layout
				}
				if node.hasLower {
					layout := renderLayout(node.lower, nodes)
					lower = &layout
				}
				width := 0
				if upper != nil {
					width = max(width, upper.width)
				}
				if lower != nil {
					width = max(width, lower.width)
				}
				var lines []string
				if upper != nil {
					for _, line := range upper.lines {
						lines = append(lines, padLayoutLine(line, width, false))
					}
				}
				lines = append(lines, repeatSpaces(width))
				if lower != nil {
					for _, line := range lower.lines {
						lines = append(lines, padLayoutLine(line, width, false))
					}
				}
				baseline := 0
				if upper != nil {
					baseline = len(upper.lines)
				}
				layouts = append(layouts, latexLayout{lines: lines, width: width, baseline: baseline})

			default:
				width := 0
				for _, line := range node.lines {
					width = max(width, VisibleWidth(line))
				}
				lines := make([]string, 0, len(node.lines))
				for _, line := range node.lines {
					lines = append(lines, padLayoutLine(line, width, false))
				}
				layouts = append(layouts, latexLayout{lines: lines, width: width, baseline: node.baseline})
			}

			position = match[1]
			previousNode = node
		}

		if position < len(sourceLine) {
			sliced := sourceLine[position:]
			trimmed := sliced
			if previousNode != nil {
				trimmed = strings.TrimLeft(trimmed, " \t\n\r\v\f\u00a0")
			}
			text := trimmed
			if previousNode != nil && previousNode.kind == "matrix" && startsWithWhitespace(sliced) {
				text = " " + trimmed
			}
			layouts = append(layouts, latexLayout{lines: []string{text}, width: VisibleWidth(text), baseline: 0})
		}

		lineLayout := joinLayouts(layouts)
		if len(renderedLines) == 0 {
			firstBaseline = lineLayout.baseline
		}
		renderedLines = append(renderedLines, lineLayout.lines...)
	}

	width := 0
	for _, line := range renderedLines {
		width = max(width, VisibleWidth(line))
	}
	return latexLayout{lines: renderedLines, width: width, baseline: firstBaseline}
}

func startsWithWhitespace(value string) bool {
	if value == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(value)
	return isWhitespaceRune(r)
}

func endsWithWhitespace(value string) bool {
	if value == "" {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(value)
	return isWhitespaceRune(r)
}

// ---- Parser ----

type latexParser struct {
	source         string
	nodes          *[]*latexLayoutNode
	display        bool
	position       int
	supported      bool
	stackFractions bool
	scriptDepth    int
}

func (p *latexParser) peekRune() (rune, int) {
	if p.position >= len(p.source) {
		return 0, 0
	}
	return utf8.DecodeRuneInString(p.source[p.position:])
}

func (p *latexParser) render() (string, bool) {
	rendered := p.parseSequence("", false)
	if !p.supported || p.position != len(p.source) {
		return "", false
	}
	return normalizeOutput(rendered), true
}

func (p *latexParser) parseSequence(endCharacter string, hasEnd bool) string {
	result := ""
	for p.position < len(p.source) {
		character, size := p.peekRune()
		if hasEnd && string(character) == endCharacter {
			p.position += size
			return result
		}
		if character == '}' {
			p.supported = false
			return result
		}
		if character == '{' {
			p.position += size
			result += p.parseSequence("}", true)
			continue
		}
		if character == '\\' {
			command := p.parseCommand()
			if command == latexNegativeSpace {
				result = strings.TrimRight(result, " \t\n\r\v\f\u00a0")
				if strings.HasSuffix(result, latexNamedOperatorEnd) {
					result = result[:len(result)-len(latexNamedOperatorEnd)]
				}
			} else {
				result += command
			}
			continue
		}
		if character == '^' || character == '_' {
			p.position += size
			result = strings.TrimRight(result, " \t\n\r\v\f\u00a0")
			script := p.parseScripts(string(character))
			if strings.HasSuffix(result, latexNamedOperatorEnd) {
				result = result[:len(result)-len(latexNamedOperatorEnd)] + script + latexNamedOperatorEnd
			} else {
				result += script
			}
			continue
		}
		if isWhitespaceRune(character) {
			result += p.parseWhitespace()
			continue
		}
		if character == '=' || character == '<' || character == '>' {
			result = strings.TrimRight(result, " \t\n\r\v\f\u00a0") + " " + string(character) + " "
			p.position += size
			continue
		}
		if character == '&' {
			p.position += size
			continue
		}
		if character == '~' {
			p.position += size
			result += " "
			continue
		}
		if character == '.' {
			marker := latexTrailingMarkerRegex.FindStringSubmatch(result)
			var node *latexLayoutNode
			if marker != nil {
				index := atoiSafe(marker[1])
				if index >= 0 && index < len(*p.nodes) {
					node = (*p.nodes)[index]
				}
			}
			if node != nil && node.kind == "matrix" {
				lastLine := len(node.lines) - 1
				if lastLine >= 0 {
					node.lines[lastLine] = node.lines[lastLine] + string(character)
				}
				p.position += size
				continue
			}
		}
		result += string(character)
		p.position += size
	}

	if hasEnd {
		p.supported = false
	}
	return result
}

func (p *latexParser) parseScripts(initialMarker string) string {
	scripts := map[string]string{}
	var order []string
	hasSub, hasSup := false, false

	parse := func(marker string) {
		kind := "sup"
		if marker == "_" {
			kind = "sub"
		}
		p.scriptDepth++
		value := p.parseRequiredArgument(false)
		p.scriptDepth--
		scripts[kind] = value
		if kind == "sub" {
			hasSub = true
		} else {
			hasSup = true
		}
		order = append(order, kind)
	}

	parse(initialMarker)
	nextPosition := p.position
	for nextPosition < len(p.source) && isWhitespaceByte(p.source[nextPosition]) {
		nextPosition++
	}
	nextMarker := ""
	if nextPosition < len(p.source) {
		nextMarker = string(p.source[nextPosition])
	}
	if (nextMarker == "^" || nextMarker == "_") && nextMarker != initialMarker {
		p.position = nextPosition + 1
		parse(nextMarker)
	}

	subValue := scripts["sub"]
	supValue := scripts["sup"]
	subUnicode, subOK := "", false
	if hasSub {
		subUnicode, subOK = formatUnicodeScript(subValue, "sub")
	}
	supUnicode, supOK := "", false
	if hasSup {
		supUnicode, supOK = formatUnicodeScript(supValue, "sup")
	}

	canUseLayout := true
	for _, value := range []struct {
		value string
		has   bool
	}{{subValue, hasSub}, {supValue, hasSup}} {
		if !value.has {
			continue
		}
		if strings.Contains(value.value, "/") ||
			(!strings.Contains(value.value, latexLayoutMarkerStart) &&
				utf8.RuneCountInString(value.value) > 1 && !latexUppercaseOrStarRegex.MatchString(value.value)) {
			canUseLayout = false
			break
		}
	}
	needsLayout := p.display && canUseLayout &&
		(p.scriptDepth > 0 || (hasSub && !subOK) || (hasSup && !supOK))

	if !needsLayout {
		var builder strings.Builder
		for _, kind := range order {
			if kind == "sub" {
				if subOK {
					builder.WriteString(subUnicode)
				} else {
					builder.WriteString(formatScript(subValue, kind))
				}
			} else {
				if supOK {
					builder.WriteString(supUnicode)
				} else {
					builder.WriteString(formatScript(supValue, kind))
				}
			}
		}
		return builder.String()
	}

	node := &latexLayoutNode{kind: "script"}
	if hasSub {
		node.lower = normalizeOutput(subValue)
		node.hasLower = true
	}
	if hasSup {
		node.upper = normalizeOutput(supValue)
		node.hasUpper = true
	}
	index := p.pushNode(node)
	return latexLayoutMarkerStart + itoa(index) + latexLayoutMarkerEnd
}

func (p *latexParser) parseWhitespace() string {
	for p.position < len(p.source) {
		character, size := p.peekRune()
		if !isWhitespaceRune(character) {
			break
		}
		p.position += size
	}
	return " "
}

func isWhitespaceByte(b byte) bool {
	switch b {
	case ' ', '\t', '\n', '\v', '\f', '\r':
		return true
	}
	return false
}

func (p *latexParser) parseCommand() string {
	p.position++ // the backslash
	if p.position >= len(p.source) {
		p.supported = false
		return ""
	}

	first, size := p.peekRune()
	if first == '\n' || first == '\r' {
		p.position += size
		if first == '\r' && p.position < len(p.source) && p.source[p.position] == '\n' {
			p.position++
		}
		return " "
	}

	command := ""
	if isASCIILetter(first) {
		start := p.position
		for p.position < len(p.source) {
			character, characterSize := p.peekRune()
			if !isASCIILetter(character) {
				break
			}
			p.position += characterSize
		}
		command = p.source[start:p.position]
	} else {
		command = string(first)
		p.position += size
	}

	switch command {
	case "\\":
		return "\n"
	}
	if latexSpacingCommands[command] {
		return " "
	}
	if latexNegativeSpacingCommands[command] {
		return latexNegativeSpace
	}
	if latexFontSwitchCommands[command] {
		for p.position < len(p.source) {
			character, characterSize := p.peekRune()
			if !isWhitespaceRune(character) {
				break
			}
			p.position += characterSize
		}
		return ""
	}
	if latexIgnoredCommands[command] {
		return ""
	}
	switch command {
	case "{", "}", "$", "%", "#", "_", "&":
		return command
	case "|":
		return "‖"
	}
	if command == "not" {
		value := strings.TrimSpace(p.parseRequiredArgument(false))
		if negated, ok := latexNegatedSymbols[value]; ok {
			return " " + negated + " "
		}
		characters := []rune(value)
		if len(characters) == 0 {
			p.supported = false
			return ""
		}
		return " " + string(characters[0]) + "\u0338" + string(characters[1:]) + " "
	}
	if latexLimitOperators[command] {
		return p.parseOperator(command, "bracket", true, true)
	}
	if symbol, ok := latexSymbols[command]; ok {
		if latexDisplayLimitSymbols[command] {
			return p.parseOperator(symbol, "script", true, false)
		}
		if command == "cdot" || command == "times" || latexRelationCommands[command] {
			return " " + symbol + " "
		}
		return symbol
	}
	if latexNamedOperators[command] {
		return latexNamedOperatorStart + command + latexNamedOperatorEnd
	}
	if latexSizeCommands[command] {
		return ""
	}
	if command == "left" || command == "middle" || command == "right" {
		if p.position < len(p.source) && p.source[p.position] == '.' {
			p.position++
		}
		return ""
	}
	if command == "frac" || command == "dfrac" || command == "tfrac" {
		shouldStack := p.display && p.stackFractions && command != "tfrac"
		numerator := p.parseRequiredArgument(!shouldStack)
		denominator := p.parseRequiredArgument(!shouldStack)
		if shouldStack {
			node := &latexLayoutNode{
				kind:        "fraction",
				numerator:   normalizeOutput(numerator),
				denominator: normalizeOutput(denominator),
			}
			index := p.pushNode(node)
			return latexLayoutMarkerStart + itoa(index) + latexLayoutMarkerEnd
		}
		return formatFraction(numerator, denominator)
	}
	if command == "sqrt" {
		degree, hasDegree := p.parseOptionalArgument()
		degree = strings.TrimSpace(degree)
		value := p.parseRequiredArgument(true)
		if !hasDegree || degree == "2" {
			return formatRoot(value, "√")
		}
		if degree == "3" {
			return formatRoot(value, "∛")
		}
		if degree == "4" {
			return formatRoot(value, "∜")
		}
		return formatScript(degree, "sup") + formatRoot(value, "√")
	}
	if command == "boxed" || command == "fbox" {
		return "[" + strings.TrimSpace(p.parseRequiredArgument(true)) + "]"
	}
	if command == "binom" || command == "dbinom" || command == "tbinom" {
		return "(" + p.parseRequiredArgument(true) + " choose " + p.parseRequiredArgument(true) + ")"
	}
	if accent, ok := latexAccents[command]; ok {
		value := p.parseRequiredArgument(true)
		if utf8.RuneCountInString(value) == 1 {
			return value + accent
		}
		return command + "(" + value + ")"
	}
	if command == "mathbb" {
		value := p.parseRequiredArgument(true)
		var builder strings.Builder
		for _, character := range value {
			if mapped, ok := latexBlackboard[string(character)]; ok {
				builder.WriteString(mapped)
			} else {
				builder.WriteRune(character)
			}
		}
		return builder.String()
	}
	if command == "operatorname" {
		starred := p.position < len(p.source) && p.source[p.position] == '*'
		if starred {
			p.position++
		}
		operator := strings.TrimSpace(normalizeOutput(p.parseRequiredArgument(true)))
		return p.parseOperator(operator, "bracket", starred, true)
	}
	if command == "mod" || command == "bmod" {
		return " mod "
	}
	if command == "pmod" || command == "pod" {
		value := strings.TrimSpace(p.parseRequiredArgument(true))
		if command == "pmod" {
			return " (mod " + value + ")"
		}
		return " (" + value + ")"
	}
	if command == "overset" || command == "stackrel" {
		upper := p.parseRequiredArgument(true)
		value := strings.TrimSpace(p.parseRequiredArgument(true))
		return value + formatScript(upper, "sup")
	}
	if command == "underset" {
		lower := p.parseRequiredArgument(true)
		value := strings.TrimSpace(p.parseRequiredArgument(true))
		return value + formatScript(lower, "sub")
	}
	if latexPlainWrappers[command] {
		value := p.parseRequiredArgument(true)
		if strings.HasPrefix(command, "text") || command == "mbox" {
			return value
		}
		return strings.TrimSpace(value)
	}
	if command == "begin" {
		return p.parseEnvironment()
	}
	if command == "end" {
		p.supported = false
		return ""
	}

	p.supported = false
	return "\\" + command
}

func isASCIILetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func (p *latexParser) parseOperator(operator string, inlineLowerStyle string, displayLimits bool, spaced bool) string {
	useDisplayLimits := displayLimits
	modifierPosition := p.position
	for modifierPosition < len(p.source) && (p.source[modifierPosition] == ' ' || p.source[modifierPosition] == '\t') {
		modifierPosition++
	}
	modifier := latexLimitsModifierRegex.FindStringSubmatch(p.source[modifierPosition:])
	if modifier != nil {
		useDisplayLimits = modifier[1] == "limits"
		consumed := len("\\") + len(modifier[1])
		if modifier[2] != "" {
			// Keep the character that terminated the command.
			p.position = modifierPosition + consumed
		} else {
			p.position = modifierPosition + consumed
		}
	}

	lower, upper := "", ""
	hasLower, hasUpper := false, false
	for {
		scriptPosition := p.position
		for scriptPosition < len(p.source) && (p.source[scriptPosition] == ' ' || p.source[scriptPosition] == '\t') {
			scriptPosition++
		}
		kind := byte(0)
		if scriptPosition < len(p.source) {
			kind = p.source[scriptPosition]
		}
		if kind != '_' && kind != '^' {
			break
		}
		p.position = scriptPosition + 1
		value := strings.ReplaceAll(normalizeOutput(p.parseRequiredArgument(false)), " ", "")
		if kind == '_' {
			if hasLower {
				p.supported = false
			}
			lower = value
			hasLower = true
		} else {
			if hasUpper {
				p.supported = false
			}
			upper = value
			hasUpper = true
		}
	}

	if p.display && useDisplayLimits && (hasLower || hasUpper) {
		node := &latexLayoutNode{kind: "operator", operator: operator, lower: lower, upper: upper, hasLower: hasLower, hasUpper: hasUpper}
		index := p.pushNode(node)
		return latexLayoutMarkerStart + itoa(index) + latexLayoutMarkerEnd
	}

	rendered := operator
	if hasLower {
		if inlineLowerStyle == "bracket" {
			rendered += "[" + lower + "]"
		} else {
			rendered += formatScript(lower, "sub")
		}
	}
	if hasUpper {
		rendered += formatScript(upper, "sup")
	}
	if spaced {
		return " " + rendered + " "
	}
	return rendered
}

func (p *latexParser) parseRequiredArgument(stackFractions bool) string {
	previousStackFractions := p.stackFractions
	p.stackFractions = previousStackFractions && stackFractions
	value := p.parseRequiredArgumentValue()
	p.stackFractions = previousStackFractions
	return value
}

func (p *latexParser) parseRequiredArgumentValue() string {
	for p.position < len(p.source) {
		character, size := p.peekRune()
		if !isWhitespaceRune(character) {
			break
		}
		p.position += size
	}
	if p.position >= len(p.source) {
		p.supported = false
		return ""
	}
	if p.source[p.position] == '{' {
		p.position++
		return p.parseSequence("}", true)
	}
	if p.source[p.position] == '\\' {
		return p.parseCommand()
	}
	character, size := p.peekRune()
	p.position += size
	return string(character)
}

func (p *latexParser) parseOptionalArgument() (string, bool) {
	for p.position < len(p.source) && (p.source[p.position] == ' ' || p.source[p.position] == '\t') {
		p.position++
	}
	if p.position >= len(p.source) || p.source[p.position] != '[' {
		return "", false
	}
	end := strings.Index(p.source[p.position+1:], "]")
	if end < 0 {
		p.supported = false
		return "", false
	}
	end += p.position + 1
	value := p.source[p.position+1 : end]
	p.position = end + 1
	return p.renderNested(value, true), true
}

func (p *latexParser) readRawGroup() (string, bool) {
	for p.position < len(p.source) && (p.source[p.position] == ' ' || p.source[p.position] == '\t') {
		p.position++
	}
	if p.position >= len(p.source) || p.source[p.position] != '{' {
		p.supported = false
		return "", false
	}

	p.position++
	start := p.position
	depth := 1
	for p.position < len(p.source) {
		character := p.source[p.position]
		if character == '\\' {
			p.position += 2
			continue
		}
		if character == '{' {
			depth++
		}
		if character == '}' {
			depth--
		}
		if depth == 0 {
			value := p.source[start:p.position]
			p.position++
			return value, true
		}
		p.position++
	}
	p.supported = false
	return "", false
}

func (p *latexParser) splitEnvironmentRows(body string) []string {
	return latexEnvironmentRowRegex.Split(body, -1)
}

func (p *latexParser) parseEnvironment() string {
	environment, ok := p.readRawGroup()
	if !ok {
		return ""
	}
	endMarker := "\\end{" + environment + "}"
	end := strings.Index(p.source[p.position:], endMarker)
	if end < 0 {
		p.supported = false
		return ""
	}
	end += p.position
	body := p.source[p.position:end]
	p.position = end + len(endMarker)

	if environment == "equation" || environment == "equation*" || environment == "displaymath" {
		return strings.TrimSpace(p.renderNested(body, true))
	}

	switch environment {
	case "aligned", "align", "align*", "alignedat", "alignat", "alignat*",
		"gather", "gathered", "multline", "multline*", "split":
		alignedAt := environment == "alignedat" || environment == "alignat" || environment == "alignat*"
		alignedBody := body
		if alignedAt {
			alignedBody = latexArrayColumnSpecRegex.ReplaceAllString(alignedBody, "")
		}
		var rows []string
		for _, row := range p.splitEnvironmentRows(alignedBody) {
			cells := strings.Split(row, "&")
			source := ""
			if alignedAt {
				var joined []string
				for index := 0; index < (len(cells)+1)/2; index++ {
					start := index * 2
					end := min(index*2+2, len(cells))
					if start < len(cells) {
						joined = append(joined, strings.Join(cells[start:end], ""))
					}
				}
				source = strings.Join(joined, " ")
			} else {
				source = strings.Join(cells, "")
			}
			rendered := strings.TrimSpace(p.renderNested(source, true))
			if rendered != "" {
				rows = append(rows, rendered)
			}
		}
		return strings.Join(rows, "\n")

	case "cases", "cases*":
		return p.renderCases(body)

	case "array", "matrix", "smallmatrix", "pmatrix", "bmatrix", "Bmatrix", "vmatrix", "Vmatrix":
		matrixBody := body
		if environment == "array" {
			matrixBody = latexArrayColumnSpecRegex.ReplaceAllString(matrixBody, "")
		}
		return p.renderMatrix(environment, matrixBody)
	}

	p.supported = false
	return body
}

func (p *latexParser) renderCases(body string) string {
	var rows [][]string
	for _, row := range p.splitEnvironmentRows(body) {
		cells := strings.Split(row, "&")
		trimmed := make([]string, 0, len(cells))
		for _, cell := range cells {
			trimmed = append(trimmed, strings.TrimSpace(p.renderNested(cell, false)))
		}
		any := false
		for _, cell := range trimmed {
			if cell != "" {
				any = true
				break
			}
		}
		if any {
			rows = append(rows, trimmed)
		}
	}

	valueWidth := 0
	for _, row := range rows {
		value := ""
		if len(row) > 0 {
			value = latexTrailingCommaRegex.ReplaceAllString(row[0], "")
		}
		valueWidth = max(valueWidth, VisibleWidth(value))
	}

	var contents []string
	for _, row := range rows {
		value := ""
		if len(row) > 0 {
			value = latexTrailingCommaRegex.ReplaceAllString(row[0], "")
		}
		condition := ""
		if len(row) > 1 {
			condition = row[1]
		}
		if condition == "" {
			contents = append(contents, value)
			continue
		}
		conditionPrefix := " if "
		if latexCasesConditionRegex.MatchString(condition) {
			conditionPrefix = " "
		}
		contents = append(contents, value+strings.Repeat(latexProtectedSpace, max(0, valueWidth-VisibleWidth(value)))+conditionPrefix+condition)
	}
	if len(contents) <= 1 {
		if len(contents) == 0 {
			return ""
		}
		return "⎧ " + contents[0]
	}

	middle := len(contents) / 2
	// The gap row is represented as a nil entry (upstream inserts undefined).
	visualRows := make([]*string, 0, len(contents)+1)
	for index, content := range contents {
		if index == middle && len(contents)%2 == 0 {
			visualRows = append(visualRows, nil)
		}
		value := content
		visualRows = append(visualRows, &value)
	}

	var lines []string
	for index, content := range visualRows {
		delimiter := "⎨"
		if index == 0 {
			delimiter = "⎧"
		} else if index == len(visualRows)-1 {
			delimiter = "⎩"
		}
		if content == nil {
			lines = append(lines, delimiter)
			continue
		}
		lines = append(lines, delimiter+" "+*content)
	}
	node := &latexLayoutNode{kind: "matrix", lines: lines, baseline: middle}
	index := p.pushNode(node)
	return latexLayoutMarkerStart + itoa(index) + latexLayoutMarkerEnd
}

var latexTrailingCommaRegex = regexp.MustCompile(`,\s*$`)

func (p *latexParser) renderMatrix(environment string, body string) string {
	var matrix [][]string
	for _, row := range p.splitEnvironmentRows(body) {
		cells := strings.Split(row, "&")
		trimmed := make([]string, 0, len(cells))
		for _, cell := range cells {
			trimmed = append(trimmed, strings.TrimSpace(p.renderNested(cell, false)))
		}
		any := false
		for _, cell := range trimmed {
			if cell != "" {
				any = true
				break
			}
		}
		if any {
			matrix = append(matrix, trimmed)
		}
	}

	columnCount := 0
	for _, row := range matrix {
		columnCount = max(columnCount, len(row))
	}
	columnWidths := make([]int, columnCount)
	for column := 0; column < columnCount; column++ {
		for _, row := range matrix {
			cell := ""
			if column < len(row) {
				cell = row[column]
			}
			columnWidths[column] = max(columnWidths[column], VisibleWidth(cell))
		}
	}

	rows := make([]string, 0, len(matrix))
	for _, row := range matrix {
		cells := make([]string, 0, columnCount)
		for column := 0; column < columnCount; column++ {
			cell := ""
			if column < len(row) {
				cell = row[column]
			}
			cells = append(cells, cell+strings.Repeat(latexProtectedSpace, max(0, columnWidths[column]-VisibleWidth(cell))))
		}
		rows = append(rows, strings.Join(cells, " │ "))
	}

	var lines []string
	if environment == "array" || environment == "matrix" || environment == "smallmatrix" {
		lines = rows
	} else {
		delimiters := map[string][6]string{
			"pmatrix": {"⎛", "⎞", "⎜", "⎟", "⎝", "⎠"},
			"bmatrix": {"⎡", "⎤", "⎢", "⎥", "⎣", "⎦"},
			"Bmatrix": {"⎧", "⎫", "⎨", "⎬", "⎩", "⎭"},
			"vmatrix": {"│", "│", "│", "│", "│", "│"},
			"Vmatrix": {"║", "║", "║", "║", "║", "║"},
		}
		delimiter, ok := delimiters[environment]
		if !ok {
			p.supported = false
			return strings.Join(rows, "\n")
		}
		for index, row := range rows {
			left := delimiter[2]
			if index == 0 {
				left = delimiter[0]
			} else if index == len(rows)-1 {
				left = delimiter[4]
			}
			right := delimiter[3]
			if index == 0 {
				right = delimiter[1]
			} else if index == len(rows)-1 {
				right = delimiter[5]
			}
			lines = append(lines, left+" "+row+" "+right)
		}
	}

	if len(lines) <= 1 {
		if len(lines) == 0 {
			return ""
		}
		return lines[0]
	}
	node := &latexLayoutNode{kind: "matrix", lines: lines, baseline: 0}
	index := p.pushNode(node)
	return latexLayoutMarkerStart + itoa(index) + latexLayoutMarkerEnd
}

func (p *latexParser) renderNested(source string, stackFractions bool) string {
	nested := &latexParser{
		source:         source,
		nodes:          p.nodes,
		display:        p.display && stackFractions,
		supported:      true,
		stackFractions: true,
	}
	rendered, ok := nested.render()
	if !ok {
		p.supported = false
		return source
	}
	return rendered
}

func (p *latexParser) pushNode(node *latexLayoutNode) int {
	*p.nodes = append(*p.nodes, node)
	return len(*p.nodes) - 1
}

func renderLatexImpl(source string, options RenderLatexOptions) (string, bool) {
	var nodes []*latexLayoutNode
	parser := &latexParser{
		source:         source,
		nodes:          &nodes,
		display:        options.Display,
		supported:      true,
		stackFractions: true,
	}
	rendered, ok := parser.render()
	if !ok {
		return "", false
	}
	if len(nodes) == 0 {
		return strings.ReplaceAll(rendered, latexProtectedSpace, " "), true
	}

	lines := renderLayout(rendered, nodes).lines
	indentation := -1
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		leading := len(line) - len(strings.TrimLeft(line, " "))
		if indentation < 0 || leading < indentation {
			indentation = leading
		}
	}
	if indentation < 0 {
		indentation = 0
	}
	trimmed := make([]string, 0, len(lines))
	for _, line := range lines {
		if indentation > len(line) {
			indentation = len(line)
		}
		trimmed = append(trimmed, strings.TrimRight(line[indentation:], " "))
	}
	return strings.ReplaceAll(strings.TrimRight(strings.Join(trimmed, "\n"), " \t\n\r\v\f\u00a0"), latexProtectedSpace, " "), true
}
