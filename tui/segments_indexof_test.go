package tui

import (
	"strings"
	"testing"
)

// TestIndexOfFindsTheFirstByteOccurrence pins the search semantics indexOf has
// to keep (it is the port's String.prototype.indexOf for byte-oriented
// callers): the first occurrence's byte offset, -1 when absent, 0 for an empty
// needle. A hand-rolled O(n*m) loop used to implement it, which cost 20% of a
// warm frame through IsImageLine.
func TestIndexOfFindsTheFirstByteOccurrence(t *testing.T) {
	cases := []struct {
		haystack string
		needle   string
		want     int
	}{
		{"hello world", "world", 6},
		{"hello world", "hello", 0},
		{"hello world", "zz", -1},
		{"hello", "", 0},
		{"hi", "hello", -1},
		{"", "", 0},
		{"abcabc", "bc", 1},
		// Byte offsets, not rune offsets (what the previous byte loop did too).
		{"héllo\x1b_Gx", "\x1b_G", len("héllo")},
		{"日本語", "本", len("日")},
	}
	for _, tc := range cases {
		got := indexOf(tc.haystack, tc.needle)
		if got != tc.want {
			t.Fatalf("indexOf(%q, %q) = %d, want %d", tc.haystack, tc.needle, got, tc.want)
		}
		if got != strings.Index(tc.haystack, tc.needle) {
			t.Fatalf("indexOf(%q, %q) = %d, strings.Index gives %d", tc.haystack, tc.needle, got, strings.Index(tc.haystack, tc.needle))
		}
	}
}
