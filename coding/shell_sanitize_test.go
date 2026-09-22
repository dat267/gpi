package coding

import (
	"strings"
	"testing"
)

// TestSanitizeBinaryOutputKeepsCleanTextUnchanged pins the streaming cost: the
// sanitizer is called on the whole accumulated tool output on every result
// update (shell.go is the only filter in that path), so a clean 155 KB build
// log used to be copied rune by rune — 670 us per update, and quadratic over a
// streaming command. Clean text must be returned as-is.
func TestSanitizeBinaryOutputKeepsCleanTextUnchanged(t *testing.T) {
	lines := make([]string, 0, 2000)
	for i := 0; i < 2000; i++ {
		lines = append(lines, "build line with some tabs\tand 中文 text")
	}
	clean := strings.Join(lines, "\n")

	if got := SanitizeBinaryOutput(clean); got != clean {
		t.Fatalf("clean text changed: %d -> %d bytes", len(clean), len(got))
	}
	// Identity plus no copy: the caller's string must be returned, not rebuilt.
	allocs := testing.AllocsPerRun(20, func() { _ = SanitizeBinaryOutput(clean) })
	if allocs != 0 {
		t.Fatalf("sanitizing clean text allocated %.0f times; want 0", allocs)
	}
}

// TestSanitizeBinaryOutputFiltersBelowTheFastPath pins the filtering that the
// fast path must not skip.
func TestSanitizeBinaryOutputFiltersBelowTheFastPath(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"control", "null\x00bell\x07format\uFFF9tail", "nullbellformattail"},
		{"kept", "ok\tline\nnext\r", "ok\tline\nnext\r"},
		{"del survives", "\x7f", "\x7f"},
		{"mixed", "clean\x1b[0m text\x02more", "clean[0m textmore"},
		// Invalid UTF-8 is replaced (range over a string yields U+FFFD per
		// invalid byte), which the fast path must still detect.
		{"invalid utf8", "a\xffb", "a\ufffdb"},
		{"truncated rune", "a\xe4\xb8b", "a\ufffd\ufffdb"},
		// A valid rune from the removed range must be dropped, not replaced.
		{"format char", "x\uFFFAy", "xy"},
		// Non-ASCII next to the boundary of the removed range stays.
		{"near format", "x\uFFF8y\uFFFCz", "x\uFFF8y\uFFFCz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeBinaryOutput(tc.input); got != tc.want {
				t.Fatalf("SanitizeBinaryOutput(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
