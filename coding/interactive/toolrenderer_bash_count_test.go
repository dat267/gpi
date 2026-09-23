package interactive

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dat267/pier/tui"
)

// TestPlainWrappedLineCountMatchesTheGenericWrapper pins the fast path to
// tui.WrapTextWithAnsi: tui.Text.Render is what the preview's line count must
// agree with, so any divergence shows up as a wrong "... (N earlier lines)".
func TestPlainWrappedLineCountMatchesTheGenericWrapper(t *testing.T) {
	corpus := []string{
		"", " ", "  ", "\t", "a", "a b c",
		"ok  \tgithub.com/dat267/pier/coding/pkg1\t0.100s\t(line 1 with some words)",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"aaaa bbbb cccc dddd eeee ffff gggg hhhh",
		"word 1234567890123456789012345678901234567890 end",
		"trailing spaces   ",
		"   leading spaces",
		"one-two-three-four-five-six-seven-eight-nine-ten",
		"a b",
		"ab cd ef gh ij kl",
		"x y z x y z x y z x y z x y z x y z x y z x y z x y z",
	}
	// Deterministic generated lines over a small alphabet, including runs of
	// spaces and tabs at arbitrary boundaries.
	alphabet := []byte("a bb   \t")
	seed := uint32(12345)
	next := func() byte {
		seed = seed*1664525 + 1013904223
		return alphabet[int(seed>>16)%len(alphabet)]
	}
	for i := 0; i < 4000; i++ {
		length := int(next()) % 30
		line := make([]byte, length)
		for j := range line {
			line[j] = next()
		}
		corpus = append(corpus, string(line))
	}

	for width := 1; width <= 40; width++ {
		for _, line := range corpus {
			want := len(tui.WrapTextWithAnsi(strings.ReplaceAll(line, "\t", "   "), width))
			if want == 0 {
				want = 1
			}
			if got, ok := plainWrappedLineCount(line, width); !ok {
				t.Fatalf("plain fast path declined %q", line)
			} else if got != want {
				t.Fatalf("width=%d line=%q: fast count=%d, wrapper=%d", width, line, got, want)
			}
		}
	}
}

// TestPlainWrappedLineCountDeclinesNonPlainLines keeps the fast path honest: the
// generic wrapper must handle anything with escapes, wide runes, or tabs at the
// edges of the ASCII set.
func TestPlainWrappedLineCountDeclinesNonPlainLines(t *testing.T) {
	for _, line := range []string{
		"\x1b[31mred\x1b[0m",
		"wide 世界 text",
		"carriage\rreturn",
		"bell\x07",
		"del\x7f",
		"newline\nsplit",
	} {
		if _, ok := plainWrappedLineCount(line, 20); ok {
			t.Fatalf("fast path accepted %q", line)
		}
	}
}

// TestBashPreviewCountIsAllocationFree guards the UI-thread cost: counting a
// 2000-line output used ~8000 allocations per call.
func TestBashPreviewCountIsAllocationFree(t *testing.T) {
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)

	var builder strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&builder, "ok  \tgithub.com/dat267/pier/coding/pkg%d\t0.1%03ds\t(line %d with some words)\n", i, i%1000, i)
	}
	output := builder.String()
	allocs := testing.AllocsPerRun(20, func() {
		state := &bashResultState{}
		component := &bashPreviewComponent{output: output, state: state, theme: ActiveTheme()}
		_ = component.visualLineCount(88)
	})
	if allocs > 100 {
		t.Fatalf("cold visual line count allocated %.0f objects for 2000 lines", allocs)
	}
}
