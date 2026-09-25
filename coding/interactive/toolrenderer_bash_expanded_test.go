package interactive

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dat267/pier/tui"
)

// referenceBashExpanded is the original O(output) computation: style the whole
// output, replace tabs, wrap, and pad every line to the width (what tui.Text
// did for the expanded result).
func referenceBashExpanded(output string, width int, theme *Theme) []string {
	text := strings.ReplaceAll("\n"+styleBashOutput(output, theme), "\t", "   ")
	wrapped := tui.WrapTextWithAnsi(text, width)
	for i, line := range wrapped {
		if pad := width - tui.VisibleWidth(line); pad > 0 {
			wrapped[i] = line + strings.Repeat(" ", pad)
		}
	}
	return wrapped
}

func testBashExpandedTheme(t *testing.T) *Theme {
	t.Helper()
	SetCustomThemesDir(t.TempDir())
	SetRegisteredThemes(nil)
	SetTrueColorSupport(true)
	SetStyleColorsEnabled(true)
	InitTheme("dark", false)
	return ActiveTheme()
}

// TestBashExpandedComponentMatchesReference feeds the expanded renderer the
// output one step at a time (as streaming does) and requires it to render
// exactly like the full-output computation at every prefix, including width
// changes and a rewritten (non-append) output.
func TestBashExpandedComponentMatchesReference(t *testing.T) {
	theme := testBashExpandedTheme(t)
	widths := []int{40, 73, 40} // a width change forces the full path

	corpus := &strings.Builder{}
	for i := 0; i < 40; i++ {
		fmt.Fprintf(corpus, "line %d with enough words to wrap around the narrow width here\n", i)
		corpus.WriteString("a partial line")
	}
	text := corpus.String()

	check := func(label, output string, width int, state *bashResultState) {
		component := &bashExpandedComponent{output: output, state: state, theme: theme}
		got := strings.Join(component.Render(width), "\n")
		want := strings.Join(referenceBashExpanded(output, width, theme), "\n")
		if got != want {
			t.Fatalf("%s width=%d:\n got %q\nwant %q", label, width, got, want)
		}
	}

	state := &bashResultState{}
	for _, width := range widths {
		for n := 1; n <= len(text); n += 7 {
			output := text[:n]
			check(fmt.Sprintf("prefix %d", n), output, width, state)
		}
		check("full", text, width, state)
		// A rewritten (shorter, non-prefix) output must resync.
		check("rewrite", "completely different\ncontent\n", width, state)
	}
}

// TestBashExpandedStreamingIsLinear guards the O(output²) regression: 20k lines
// streamed in 100-line chunks used to spend ~468 ms per chunk re-wrapping the
// whole output. The incremental cache must make the whole stream cheap.
func TestBashExpandedStreamingIsLinear(t *testing.T) {
	theme := testBashExpandedTheme(t)
	state := &bashResultState{}
	component := &bashExpandedComponent{state: state, theme: theme}

	var output strings.Builder
	output.Grow(1 << 20)
	start := time.Now()
	for i := 0; i < 20000; i++ {
		fmt.Fprintf(&output, "ok  github.com/dat267/pier/coding  0.12s (line %d)\n", i)
		if i%100 == 0 {
			component.output = output.String()
			component.Render(100)
		}
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("streaming 20k lines took %v; the expanded renderer is re-wrapping the whole output", elapsed)
	}
}
