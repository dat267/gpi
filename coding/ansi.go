package coding

import (
	"regexp"
	"strings"
)

// Port of utils/ansi.ts: stripAnsi, derived upstream from ansi-regex /
// strip-ansi (MIT, Sindre Sorhus).
//
// D29: upstream runs on JavaScript UTF-16 strings, so the 8-bit CSI introducer
// is the single code unit 0x9b. Go strings are UTF-8, where a bare 0x9b byte is
// invalid and decodes to U+FFFD; the pattern therefore matches the code point
// U+009B (and ESC). Real terminals emit ESC, so this only affects streams that
// use the 8-bit C1 form.
//
// ansiStripPattern is the upstream pattern, kept as the reference: the scanner
// below must produce byte-identical output (TestStripAnsiMatchesPattern).
var ansiStripPattern = regexp.MustCompile(
	// OSC: ESC ] ... ST (non-greedy up to the first ST)
	"(?:\\x1b\\][\\s\\S]*?(?:\\x07|\\x1b\\\\|\\x9c))" +
		"|" +
		// CSI and related: ESC/C1, optional intermediates, optional params
		// (supports ; and :) then a final byte
		"[\\x1b\\x{9b}][\\[\\]()#;?]*(?:\\d{1,4}(?:[;:]\\d{0,4})*)?[\\dA-PR-TZcf-nq-uy=><~]",
)

// StripAnsi removes ANSI escape sequences from a string.
//
// A hand-written scanner replaces the reference pattern: the regexp engine is
// ~25x slower (measured 4 ms for 60 KB of coloured output), and tool output
// re-strips the whole accumulated text on every streaming chunk.
func StripAnsi(value string) string {
	// Fast path: ANSI codes require the ESC (7-bit) or CSI (8-bit) introducer.
	if !strings.Contains(value, "\x1b") && !strings.Contains(value, "\u009b") {
		return value
	}
	return stripAnsiFast(value)
}

// stripAnsiFast removes every leftmost match of ansiStripPattern in one pass.
func stripAnsiFast(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	last := 0
	for i := 0; i < len(value); {
		if !isAnsiIntroducerAt(value, i) {
			i++
			continue
		}
		if length := matchAnsiAt(value, i); length > 0 {
			builder.WriteString(value[last:i])
			i += length
			last = i
			continue
		}
		i++
	}
	builder.WriteString(value[last:])
	return builder.String()
}

// isAnsiIntroducerAt reports whether an escape sequence can start at i: ESC, or
// the UTF-8 encoding of U+009B (the 8-bit CSI introducer).
func isAnsiIntroducerAt(value string, i int) bool {
	if value[i] == 0x1b {
		return true
	}
	return value[i] == 0xc2 && i+1 < len(value) && value[i+1] == 0x9b
}

// matchAnsiAt returns the length of the sequence starting at start, or 0 when
// nothing matches there.
func matchAnsiAt(value string, start int) int {
	escape := value[start] == 0x1b
	i := start + 1
	if !escape {
		i = start + 2 // U+009B
	}

	// OSC (ESC ] ...), non-greedy up to the first BEL, ESC \ or U+009C. An
	// unterminated OSC falls through to the CSI branch, like the pattern.
	if escape && i < len(value) && value[i] == ']' {
		for j := i + 1; j < len(value); j++ {
			switch {
			case value[j] == 0x07: // BEL
				return j + 1 - start
			case value[j] == 0xc2 && j+1 < len(value) && value[j+1] == 0x9c: // U+009C
				return j + 2 - start
			case value[j] == 0x1b && j+1 < len(value) && value[j+1] == '\\': // ESC \
				return j + 2 - start
			}
		}
	}

	// Intermediates: [ ] ( ) # ; ?
	for i < len(value) && isAnsiIntermediate(value[i]) {
		i++
	}
	// Parameters: digits, then any number of ;/: separated digit groups.
	paramsStart := i
	if i < len(value) && isASCIIDigit(value[i]) {
		i += runOfDigits(value[i:], 4)
		for i < len(value) && (value[i] == ';' || value[i] == ':') {
			i++
			i += runOfDigits(value[i:], 4)
		}
	}
	// Greedy parameters, then backtrack shortest-last: a digit already taken as
	// a parameter can serve as the final byte (\x1b[#8, C1 "1234").
	for c := i; c >= paramsStart; c-- {
		if c < len(value) && isAnsiFinal(value[c]) {
			return c + 1 - start
		}
	}
	return 0
}

func runOfDigits(value string, limit int) int {
	count := 0
	for count < limit && count < len(value) && isASCIIDigit(value[count]) {
		count++
	}
	return count
}

func isASCIIDigit(value byte) bool { return value >= '0' && value <= '9' }

func isAnsiIntermediate(value byte) bool {
	switch value {
	case '[', ']', '(', ')', '#', ';', '?':
		return true
	}
	return false
}

// isAnsiFinal reports whether value is in the pattern's final-byte class
// [\dA-PR-TZcf-nq-uy=><~]: digits, A-P, R, T-Z, c, f-n, q-u, y, = > < ~.
func isAnsiFinal(value byte) bool {
	switch {
	case isASCIIDigit(value):
		return true
	case value >= 'A' && value <= 'P':
		return true
	case value == 'R':
		return true
	case value >= 'T' && value <= 'Z':
		return true
	case value == 'c':
		return true
	case value >= 'f' && value <= 'n':
		return true
	case value >= 'q' && value <= 'u':
		return true
	case value == 'y':
		return true
	}
	switch value {
	case '=', '>', '<', '~':
		return true
	}
	return false
}
