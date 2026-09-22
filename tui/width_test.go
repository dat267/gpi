package tui

import (
	"fmt"
	"strings"
	"testing"
)

// Round 121 tests: the width foundation — ANSI extraction, sequence stripping,
// grapheme segmentation, and visible width.

func TestExtractANSICode(t *testing.T) {
	cases := []struct {
		text   string
		pos    int
		code   string
		length int
	}{
		{"\x1b[31mred", 0, "\x1b[31m", 5},
		{"\x1b[2K", 0, "\x1b[2K", 4},
		{"\x1b[38;5;196m", 0, "\x1b[38;5;196m", 11},
		{"\x1b]8;;https://x\x07link\x1b]8;;\x07", 0, "\x1b]8;;https://x\x07", 15},
		{"\x1b]8;;u\x1b\\", 0, "\x1b]8;;u\x1b\\", 8},
		{"\x1b_pi:c\x07", 0, "\x1b_pi:c\x07", 7},
		{"\x1b_pi:c\x1b\\", 0, "\x1b_pi:c\x1b\\", 8},
		{"plain", 0, "", 0},
		{"\x1b", 0, "", 0},
		{"\x1b[31m", 2, "", 0},
	}
	for _, testCase := range cases {
		code, length := ExtractANSICode(testCase.text, testCase.pos)
		if code != testCase.code || length != testCase.length {
			t.Errorf("ExtractANSICode(%q, %d) = %q, %d; want %q, %d",
				testCase.text, testCase.pos, code, length, testCase.code, testCase.length)
		}
	}
}

func TestStripTerminalSequences(t *testing.T) {
	cases := map[string]string{
		"\x1b[31mred\x1b[0m":                    "red",
		"\x1b]8;;https://x\x07link\x1b]8;;\x07": "link",
		"plain text":                            "plain text",
		"a\x1b_pi:c\x07b":                       "ab",
		"\x1b[38;5;196mcolor\x1b[0m":            "color",
	}
	for input, want := range cases {
		if got := StripTerminalSequences(input); got != want {
			t.Errorf("StripTerminalSequences(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSegmentGraphemes(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"abc", []string{"a", "b", "c"}},
		{"", nil},
		{"caf\u00e9", []string{"c", "a", "f", "\u00e9"}},
		{"a\u0301", []string{"a\u0301"}}, // a + combining acute: one cluster
		{"\U0001F468\u200d\U0001F469\u200d\U0001F467", []string{"\U0001F468\u200d\U0001F469\u200d\U0001F467"}}, // ZWJ family
		{"\U0001F1E9\U0001F1EAx", []string{"\U0001F1E9\U0001F1EA", "x"}},                                       // flag
		{"\u0915\u094d\u0937", []string{"\u0915\u094d\u0937"}},                                                 // Devanagari cluster
	}
	for _, testCase := range cases {
		got := segmentGraphemes(testCase.input)
		if strings.Join(got, "|") != strings.Join(testCase.want, "|") {
			t.Errorf("segmentGraphemes(%q) = %q, want %q", testCase.input, got, testCase.want)
		}
	}
}

func TestVisibleWidthASCII(t *testing.T) {
	if got := VisibleWidth(""); got != 0 {
		t.Fatalf("empty = %d", got)
	}
	if got := VisibleWidth("hello"); got != 5 {
		t.Fatalf("hello = %d", got)
	}
	// Tab counts three.
	if got := VisibleWidth("\t"); got != 3 {
		t.Fatalf("tab = %d", got)
	}
	// Terminal sequences are invisible.
	if got := VisibleWidth("\x1b[31mab\x1b[0m"); got != 2 {
		t.Fatalf("ansi = %d", got)
	}
	// The cursor marker is invisible.
	if got := VisibleWidth("a\x1b_pi:c\x07b"); got != 2 {
		t.Fatalf("marker = %d", got)
	}
}

func TestVisibleWidthWide(t *testing.T) {
	cases := map[string]int{
		"\u65e5\u672c\u8a9e":                   6,  // CJK: double width
		"\uff21\uff22":                         4,  // fullwidth forms
		"caf\u00e9":                            4,  // latin
		"\u00e9":                               1,  // precomposed
		"a\u0301":                              1,  // combining mark adds nothing
		"\U0001F600":                           2,  // emoji
		"\U0001F1E9\U0001F1EA":                 2,  // flag
		"\u2192":                               1,  // arrows are narrow
		"\u203b":                               1,  // ambiguous width counts one (get-east-asian-width default)
		"\u65e5\u672c\u8a9e\u30c6\u30b9\u30c8": 12, // six wide runes
	}
	for input, want := range cases {
		if got := VisibleWidth(input); got != want {
			t.Errorf("VisibleWidth(%q) = %d, want %d", input, got, want)
		}
	}
}

func TestVisibleWidthCache(t *testing.T) {
	// Cached and computed values agree.
	first := VisibleWidth("日本語テスト")
	second := VisibleWidth("日本語テスト")
	if first != second || first != 12 {
		t.Fatalf("width = %d vs %d", first, second)
	}
	// Cache churn keeps results stable.
	for index := 0; index < 600; index++ {
		VisibleWidth(strings.Repeat("x", index+1) + "日")
	}
	if got := VisibleWidth("日本語テスト"); got != 12 {
		t.Fatalf("width after churn = %d", got)
	}
}

func TestGraphemeWidthSpecials(t *testing.T) {
	// Combining marks alone are zero width.
	if got := graphemeWidth("\u0301"); got != 0 {
		t.Fatalf("mark = %d", got)
	}
	// ZWJ is zero width.
	if got := graphemeWidth("\u200d"); got != 0 {
		t.Fatalf("zwj = %d", got)
	}
	// Tab is three.
	if got := graphemeWidth("\t"); got != 3 {
		t.Fatalf("tab = %d", got)
	}
}

// TestWidthCacheEvictsInBatches pins the eviction amortization: the cache was
// counted by ranging it (a full sync.Map walk) once per miss, so a full cache
// cost O(cache) per miss — the scroll benchmark spent ~8% of a warm frame in
// the sync.Map iterator alone. A full cache must therefore drop a batch of
// entries, not one per miss, and must stay bounded.
func TestWidthCacheEvictsInBatches(t *testing.T) {
	// The cache is process-global, so the probe keys must be unique per run
	// (the suite runs with -count=2).
	widthCacheProbeNonce++
	fillUntilFull := func() {
		for index := 0; index < widthCacheSize*4; index++ {
			VisibleWidth(fmt.Sprintf("batch-probe-%d-%d日", widthCacheProbeNonce, index))
			if widthCacheEntryCount() >= widthCacheSize {
				return
			}
		}
	}
	fillUntilFull()
	before := widthCacheEntryCount()
	if before != widthCacheSize {
		t.Fatalf("cache filled to %d entries, want the cap %d", before, widthCacheSize)
	}
	VisibleWidth(fmt.Sprintf("batch-probe-%d-overflow-日", widthCacheProbeNonce))
	after := widthCacheEntryCount()
	if after > widthCacheSize {
		t.Fatalf("cache size %d exceeds the cap after a miss", after)
	}
	if dropped := before - after; dropped < 2 {
		t.Fatalf("a full cache dropped %d entries on one miss; want a batch so eviction is amortized", dropped)
	}
	// Eviction must not corrupt the memoized widths.
	if got := VisibleWidth("日本語テスト"); got != 12 {
		t.Fatalf("width after eviction = %d", got)
	}
}

// widthCacheProbeNonce keeps the eviction probe's keys unique between runs.
var widthCacheProbeNonce int
