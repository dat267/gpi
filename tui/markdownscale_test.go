package tui

import (
	"fmt"
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

// TestMarkdownRenderScalesWithFencedCode bounds the fenced-code lexing path:
// lexFencedCode used to split the entire remaining document into lines on every
// fence, so a fence-heavy document cost O(fences × remaining bytes) — measured
// at 8.9s and 14.4GB for a 1MB fixture with ~5000 fences, which was the
// "random freeze after session load" on the UI goroutine. The test failed with
// 14995 MB allocated before the fix.
func TestMarkdownRenderScalesWithFencedCode(t *testing.T) {
	var b strings.Builder
	for b.Len() < 1<<20 {
		fmt.Fprintf(&b, "## Section %d\n\nSome prose with **bold**, *italic* and `inline code`.\n\n", b.Len())
		b.WriteString("```go\nfunc example() error {\n\tfor i := 0; i < 10; i++ {\n\t\tfmt.Println(i)\n\t}\n\treturn nil\n}\n```\n\n")
		b.WriteString("- list item one\n- list item two\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n")
	}
	markdown := b.String()[:1<<20]

	theme := mdTestTheme()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	md := NewMarkdown(markdown, 1, 0, theme, nil, MarkdownOptions{})
	md.Invalidate()
	_ = md.Render(100)
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc
	objects := after.Mallocs - before.Mallocs
	t.Logf("fence-heavy 1MB render: %d MB, %d allocs", allocated>>20, objects)
	// Byte counts inflate ~16x under -race (1.67GB vs 102MB) while allocation
	// counts stay within ~30%, so the bytes bound is race-tolerant and the
	// count bound carries the teeth: the quadratic lexFencedCode allocated
	// 15.9GB and ran ~480M allocations on this fixture.
	if allocated > 2<<30 {
		t.Fatalf("rendering 1MB of fence-heavy markdown allocated %d MB, want under 2 GB", allocated>>20)
	}
	if objects > 4_000_000 {
		t.Fatalf("rendering 1MB of fence-heavy markdown ran %d allocations, want under 4M", objects)
	}
}
