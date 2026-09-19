package coding

import "strings"

// MatchGlob matches a value against a minimatch-style pattern as used by
// model-scope resolution (upstream uses minimatch with { nocase: true }).
//
// Supported syntax: `*` (any run of characters except "/"), `**` (any run
// including "/"), `?` (one character except "/"), `[...]` character classes
// with ranges and `!`/`^` negation, `{a,b}` brace expansion, and `\` escaping.
//
// D22: minimatch's extended globs (`!(...)`, `+(...)`, `?(...)`, `@(...)`,
// `*(...)`), extglob negation patterns (`!pattern`), and its
// dotfile/`matchBase`/`nocase` options beyond case folding are not
// implemented; model ids never use them.
func MatchGlob(pattern, value string, nocase bool) bool {
	for _, expanded := range expandBraces(pattern) {
		if matchGlobOne(expanded, value, nocase) {
			return true
		}
	}
	return false
}

// expandBraces expands the first brace group into alternatives, recursively.
func expandBraces(pattern string) []string {
	open := -1
	depth := 0
	for index, char := range pattern {
		switch char {
		case '\\':
			continue
		case '{':
			if depth == 0 {
				open = index
			}
			depth++
		case '}':
			if depth > 0 {
				depth--
				if depth == 0 {
					prefix := pattern[:open]
					body := pattern[open+1 : index]
					suffix := pattern[index+1:]
					var out []string
					for _, alternative := range splitTopLevel(body) {
						for _, expanded := range expandBraces(prefix + alternative + suffix) {
							out = append(out, expanded)
						}
					}
					return out
				}
			}
		}
	}
	return []string{pattern}
}

func splitTopLevel(body string) []string {
	var parts []string
	depth := 0
	current := strings.Builder{}
	for _, char := range body {
		switch char {
		case '{':
			depth++
		case '}':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, current.String())
				current.Reset()
				continue
			}
		}
		current.WriteRune(char)
	}
	parts = append(parts, current.String())
	return parts
}

func matchGlobOne(pattern, value string, nocase bool) bool {
	p := []rune(pattern)
	s := []rune(value)
	if nocase {
		for index := range p {
			p[index] = toLowerRune(p[index])
		}
		for index := range s {
			s[index] = toLowerRune(s[index])
		}
	}
	return matchHere(p, s)
}

func toLowerRune(char rune) rune {
	if char >= 'A' && char <= 'Z' {
		return char + ('a' - 'A')
	}
	return char
}

func matchHere(pattern, value []rune) bool {
	for len(pattern) > 0 {
		switch pattern[0] {
		case '*':
			globstar := len(pattern) > 1 && pattern[1] == '*'
			rest := pattern[1:]
			if globstar {
				rest = pattern[2:]
			}
			// Try every split point; a single star never crosses "/".
			for index := 0; index <= len(value); index++ {
				if !globstar && index > 0 && value[index-1] == '/' {
					break
				}
				if matchHere(rest, value[index:]) {
					return true
				}
			}
			return false
		case '?':
			if len(value) == 0 || value[0] == '/' {
				return false
			}
			pattern = pattern[1:]
			value = value[1:]
		case '[':
			if len(value) == 0 || value[0] == '/' {
				return false
			}
			consumed, ok := matchClass(pattern, value[0])
			if !ok {
				return false
			}
			pattern = pattern[consumed:]
			value = value[1:]
		case '\\':
			if len(pattern) > 1 {
				pattern = pattern[1:]
			}
			if len(value) == 0 || value[0] != pattern[0] {
				return false
			}
			pattern = pattern[1:]
			value = value[1:]
		default:
			if len(value) == 0 || value[0] != pattern[0] {
				return false
			}
			pattern = pattern[1:]
			value = value[1:]
		}
	}
	return len(value) == 0
}

// matchClass matches one character against a character class, returning the
// number of pattern runes the class consumed.
func matchClass(pattern []rune, char rune) (int, bool) {
	index := 1
	negated := false
	if index < len(pattern) && (pattern[index] == '!' || pattern[index] == '^') {
		negated = true
		index++
	}
	matched := false
	first := true
	for index < len(pattern) && (pattern[index] != ']' || first) {
		first = false
		if index+2 < len(pattern) && pattern[index+1] == '-' && pattern[index+2] != ']' {
			low, high := pattern[index], pattern[index+2]
			if low <= char && char <= high {
				matched = true
			}
			index += 3
			continue
		}
		if pattern[index] == char {
			matched = true
		}
		index++
	}
	if index >= len(pattern) {
		// Unterminated class: treat "[" as a literal.
		return 1, char == '['
	}
	consumed := index + 1
	return consumed, matched != negated
}
