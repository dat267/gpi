package coding

import (
	"math/rand"
	"strings"
	"testing"
)

func referenceStripAnsi(value string) string {
	return ansiStripPattern.ReplaceAllString(value, "")
}

// TestStripAnsiMatchesPattern pins the scanner to the reference pattern over a
// corpus of hand-picked sequences plus fuzzed strings.
func TestStripAnsiMatchesPattern(t *testing.T) {
	cases := []string{
		"",
		"plain text",
		"\x1b[0m",
		"\x1b[1;31mred\x1b[0m",
		"\x1b[38;2;255;0;0mtruecolor\x1b[39m",
		"\x1b[38:2:255:0:0mcolon params\x1b[0m",
		"\x1b[K",
		"\x1b[2K",
		"\x1b[?25l",
		"\x1b[?25h",
		"\x1b(B",
		"\x1b[>0c",
		"\x1b[#8",
		"\x1b]0;window title\x07",
		"\x1b]0;window title\x1b\\",
		"\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\",
		"\x1b]0;unterminated title",
		"\x1b]",
		"\x1b",
		"\x1b[",
		"\x1b[;",
		"\x1bz",
		"\x1b[12345;6789m",
		"\x1b[;5m",
		"a\x1b[0mb\x1b[1mc",
		"\x1b[0m\x1b[0m",
		"\x1b]0;t\x07dangling\x1b[",
		"\u009b0m",
		"\u009b31mred\u009b0m",
		"pre \u009b?25l post",
		"\x1b]title\u009c",
		"text with \x1b2K lines",
	}
	for _, value := range cases {
		if got, want := StripAnsi(value), referenceStripAnsi(value); got != want {
			t.Errorf("StripAnsi(%q) = %q, want %q", value, got, want)
		}
	}

	// Fuzz: random sequences over the escape grammar alphabet.
	alphabet := []string{
		"\x1b", "\u009b", "\u009c", "\x07", "[", "]", "(", ")", "#", ";", ":", "?", "\\",
		"0", "1", "9", "25", "1234", "12345", "m", "K", "H", "l", "h", "B", "c", "f", "n", "q", "u", "y", "=", ">", "<", "~",
		"A", "P", "R", "T", "Z", "a", "g", "o", "r", "s", "t", "z", " ", "x", "\x1b]0;t", "\x1b\\",
	}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 30000; i++ {
		var sb strings.Builder
		parts := rng.Intn(8)
		for j := 0; j < parts; j++ {
			sb.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		value := sb.String()
		if got, want := StripAnsi(value), referenceStripAnsi(value); got != want {
			t.Fatalf("fuzz %d: StripAnsi(%q) = %q, want %q", i, value, got, want)
		}
	}
}

func BenchmarkStripAnsiColoured(b *testing.B) {
	var sb strings.Builder
	for i := 0; i < 1000; i++ {
		sb.WriteString("line: \x1b[32mok\x1b[0m and \x1b[1;31mfail\x1b[0m and \x1b]0;t\x07 text\n")
	}
	value := sb.String()
	b.SetBytes(int64(len(value)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = StripAnsi(value)
	}
}

func BenchmarkStripAnsiPatternReference(b *testing.B) {
	var sb strings.Builder
	for i := 0; i < 1000; i++ {
		sb.WriteString("line: \x1b[32mok\x1b[0m and \x1b[1;31mfail\x1b[0m and \x1b]0;t\x07 text\n")
	}
	value := sb.String()
	b.SetBytes(int64(len(value)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = referenceStripAnsi(value)
	}
}
