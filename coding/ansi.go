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

// ansiStripPattern is the upstream pattern: OSC sequences terminated by BEL,
// ESC\, or 0x9c, plus CSI and related sequences.
var ansiStripPattern = regexp.MustCompile(
	// OSC: ESC ] ... ST (non-greedy up to the first ST)
	"(?:\\x1b\\][\\s\\S]*?(?:\\x07|\\x1b\\\\|\\x9c))" +
		"|" +
		// CSI and related: ESC/C1, optional intermediates, optional params
		// (supports ; and :) then a final byte
		"[\\x1b\\x{9b}][\\[\\]()#;?]*(?:\\d{1,4}(?:[;:]\\d{0,4})*)?[\\dA-PR-TZcf-nq-uy=><~]",
)

// StripAnsi removes ANSI escape sequences from a string.
func StripAnsi(value string) string {
	// Fast path: ANSI codes require the ESC (7-bit) or CSI (8-bit) introducer.
	if !strings.Contains(value, "\x1b") && !strings.Contains(value, "\u009b") {
		return value
	}
	return ansiStripPattern.ReplaceAllString(value, "")
}
