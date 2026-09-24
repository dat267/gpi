package tui

import (
	"runtime"
	"strings"
	"testing"
)

// Rendering one large message must stay proportional to its size.
//
// The inline renderer accumulated into a string with `result += ...`, and its
// loop runs about once per token, so every append copied the whole accumulation:
// the cost grew quadratically. Measured before the fix on a 1 MB markdown
// message: 24 GB allocated and 6.3 s on the UI goroutine — a freeze the user
// sees, since nothing paints and no keystroke is dispatched while it runs (100 KB
// allocated ~113 ms and cost 113 ms). The same fixture allocates ~150 MB now.
//
// The bound is deliberately loose: it only has to catch a return to quadratic,
// not defend the current constant factor.
func TestMarkdownRenderScalesWithInputSize(t *testing.T) {
	theme := mdTestTheme()
	line := "Line with `code` and **bold** text and a [link](https://example.com) that wraps at eighty columns.\n"
	text := strings.Repeat(line, (1<<20)/len(line))

	markdown := NewMarkdown(text, 1, 0, theme, nil, MarkdownOptions{})
	markdown.Invalidate()

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	lines := markdown.Render(80)
	runtime.ReadMemStats(&after)

	if len(lines) == 0 {
		t.Fatal("nothing rendered")
	}
	// ~150 MB linear; 24 GB quadratic.
	const limit = 1 << 30
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > limit {
		t.Fatalf("rendering %d bytes of markdown allocated %d MB, want under %d MB",
			len(text), allocated>>20, limit>>20)
	}
}
