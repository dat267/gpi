package interactive

import (
	"fmt"
	"strings"
	"testing"

	"github.com/dat267/pier/tui"
)

// referenceBashPreview is the original O(output) computation: style the whole
// output, wrap it, keep the tail and the skipped count.
func referenceBashPreview(raw string, width int, theme *Theme) []string {
	preview := TruncateToVisualLines(styleBashOutput(raw, theme), bashPreviewLines, width, 0)
	lines := []string{""}
	if preview.SkippedCount > 0 {
		hint := theme.Fg("muted", fmt.Sprintf("... (%d earlier lines,", preview.SkippedCount)) +
			" " + KeyHint("app.tools.expand", "to expand") + theme.Fg("muted", ")")
		lines = append(lines, tui.TruncateToWidth(hint, width, "...", false))
	}
	return append(lines, preview.VisualLines...)
}

// checkBashPreviewPrefixes feeds the collapsed preview the corpus one step at a
// time (as streaming does) and requires it to render exactly like the
// full-output computation at every prefix.
func checkBashPreviewPrefixes(t *testing.T, theme *Theme, corpus string, step int, widths []int) {
	t.Helper()
	for _, width := range widths {
		state := &bashResultState{}
		for n := 1; n <= len(corpus); n += step {
			output := strings.TrimSpace(corpus[:n])
			if output == "" {
				continue
			}
			component := &bashPreviewComponent{output: output, state: state, theme: theme}
			got := strings.Join(component.Render(width), "\n")
			want := strings.Join(referenceBashPreview(output, width, theme), "\n")
			if got != want {
				t.Fatalf("width %d prefix %d:\n got %q\nwant %q", width, n, got, want)
			}
		}
		// A width change must rebuild the count and the tail.
		other := width/2 + 1
		trimmed := strings.TrimSpace(corpus)
		component := &bashPreviewComponent{output: trimmed, state: state, theme: theme}
		got := strings.Join(component.Render(other), "\n")
		want := strings.Join(referenceBashPreview(trimmed, other, theme), "\n")
		if got != want {
			t.Fatalf("width change to %d:\n got %q\nwant %q", other, got, want)
		}
	}
}

// TestBashPreviewIncrementalMatchesFull covers byte-granular prefixes of a small
// corpus (escapes, blank lines, wide characters, ANSI) and coarser prefixes of a
// longer one.
func TestBashPreviewIncrementalMatchesFull(t *testing.T) {
	theme := newRendererTestTheme(t)
	checkBashPreviewPrefixes(t, theme, "one\n\ntwo 中文\n\x1b[31mred\x1b[0m\n\ttabbed\nlast", 1, []int{20, 60})

	var sb strings.Builder
	for i := 0; i < 18; i++ {
		switch i % 6 {
		case 0:
			fmt.Fprintf(&sb, "line %d: %s\n", i, strings.Repeat("word ", 30))
		case 1:
			fmt.Fprintf(&sb, "line %d: \x1b[31mred\x1b[0m and 中文 text\n", i)
		case 2:
			sb.WriteString("\n")
		default:
			fmt.Fprintf(&sb, "line %d: short\n", i)
		}
	}
	checkBashPreviewPrefixes(t, theme, sb.String(), 7, []int{60})
}

// TestBashPreviewInvalidateClearsCache covers the theme-change path: after
// Invalidate, the next render must be recomputed (not the stale styled tail).
func TestBashPreviewInvalidateClearsCache(t *testing.T) {
	theme := newRendererTestTheme(t)
	output := "one\ntwo\nthree"
	state := &bashResultState{}
	component := &bashPreviewComponent{output: output, state: state, theme: theme}
	if lines := component.Render(40); len(lines) == 0 {
		t.Fatal("empty preview")
	}
	component.Invalidate()
	lines := component.Render(40)
	if strings.Join(lines, "\n") != strings.Join(referenceBashPreview(output, 40, theme), "\n") {
		t.Fatalf("render after invalidate = %q", lines)
	}
}

// BenchmarkBashPreviewChunk measures one streaming chunk: the output grows by
// a line and the collapsed preview re-renders. "incremental" is the new path;
// "full" is the previous per-chunk recomputation (style + wrap everything).
func BenchmarkBashPreviewChunk(b *testing.B) {
	InitTheme("dark", false)
	theme := ActiveTheme()
	for _, startLines := range []int{200, 1000} {
		b.Run(fmt.Sprintf("start=%d/incremental", startLines), func(b *testing.B) {
			var sb strings.Builder
			for i := 0; i < startLines; i++ {
				fmt.Fprintf(&sb, "line %d: compiler output with some text and \x1b[32mcolour\x1b[0m here\n", i)
			}
			state := &bashResultState{}
			component := &bashPreviewComponent{output: sb.String(), state: state, theme: theme}
			_ = component.Render(120)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				fmt.Fprintf(&sb, "line %d: more output here\n", startLines+i)
				component := &bashPreviewComponent{output: sb.String(), state: state, theme: theme}
				_ = component.Render(120)
			}
		})
		b.Run(fmt.Sprintf("start=%d/full", startLines), func(b *testing.B) {
			var sb strings.Builder
			for i := 0; i < startLines; i++ {
				fmt.Fprintf(&sb, "line %d: compiler output with some text and \x1b[32mcolour\x1b[0m here\n", i)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				fmt.Fprintf(&sb, "line %d: more output here\n", startLines+i)
				_ = referenceBashPreview(strings.TrimSpace(sb.String()), 120, theme)
			}
		})
	}
}

func BenchmarkGetTextOutput1000(b *testing.B) {
	var sb strings.Builder
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&sb, "line %d: compiler output with some text and \x1b[32mcolour\x1b[0m here\n", i)
	}
	result := &SortToolResultContent{Content: []ToolResultContent{{Type: "text", Text: sb.String()}}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = strings.TrimSpace(GetTextOutput(result, false))
	}
}
