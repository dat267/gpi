package ai

import (
	"strings"
	"unicode/utf16"
)

// Port of utils/sanitize-unicode.ts.
//
// In JavaScript, strings are UTF-16 code-unit sequences and unpaired
// surrogates can appear in strings (e.g. from mangled binary data). Go
// strings are UTF-8, where lone surrogates appear as utf8.RuneError (U+FFFD)
// after decoding — the original code units are unrecoverable. Sanitizing
// therefore operates on the UTF-16 re-encoding of the Go string: any unpaired
// surrogate produced there is dropped. For strings decoded from valid UTF-8
// this is a no-op, matching upstream behavior for well-formed input.
//
// D-row D2: JS strings are UTF-16; Go strings are UTF-8. Unpaired surrogates
// cannot survive a Go string round-trip, so the sanitized output can only
// differ from upstream's on inputs that were already corrupt in JS.

// SanitizeSurrogates removes unpaired Unicode surrogate code units from the
// UTF-16 view of the text (upstream sanitizeSurrogates).
func SanitizeSurrogates(text string) string {
	units := utf16.Encode([]rune(text))
	drop := make([]bool, len(units))
	dirty := false
	for i, u := range units {
		if u >= 0xD800 && u <= 0xDBFF { // high surrogate
			if i+1 >= len(units) || units[i+1] < 0xDC00 || units[i+1] > 0xDFFF {
				drop[i] = true
				dirty = true
			}
		} else if u >= 0xDC00 && u <= 0xDFFF { // low surrogate
			if i == 0 || units[i-1] < 0xD800 || units[i-1] > 0xDBFF {
				drop[i] = true
				dirty = true
			}
		}
	}
	if !dirty {
		return text
	}
	out := make([]uint16, 0, len(units))
	for i, u := range units {
		if !drop[i] {
			out = append(out, u)
		}
	}
	return string(utf16.Decode(out))
}

// jsTrim implements String.prototype.trim semantics (ECMA-402 Whitespace,
// which includes Unicode space separators like U+00A0, U+2028, U+2029, and
// U+FEFF that Go's strings.TrimSpace does not all cover).
var jsSpace = func(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0x85, 0xA0, 0x1680, 0x2028, 0x2029, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

func JSTrim(s string) string { return strings.TrimFunc(s, jsSpace) }

// JSTrimLength reports the UTF-16 length of the JS-trimmed string, the form
// upstream compares against zero (`text.trim().length === 0`).
func JSTrimIsEmpty(s string) bool {
	return JSTrim(s) == ""
}

// JSLength counts UTF-16 code units, matching JavaScript's string.length
// (D-row D1). The canonical implementation lives here; faux re-exports.
func JSLength(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// JSSlice slices by UTF-16 code units, matching JavaScript's String.slice.
func JSSlice(s string, start, end int) string {
	units := utf16.Encode([]rune(s))
	if start < 0 {
		start = 0
	}
	if end > len(units) {
		end = len(units)
	}
	if start >= end {
		return ""
	}
	return string(utf16.Decode(units[start:end]))
}
