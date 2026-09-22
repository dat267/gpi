package tui

import (
	"strings"
	"testing"
)

const incrementalSample = `# Title

Intro paragraph with **bold**, ` + "`code`" + ` and a [link](https://example.com).

- item one
- item two

> a quote
> second line

` + "```go" + `
func main() {
	fmt.Println("hi")
}
` + "```" + `

| a | b |
| - | - |
| 1 | 2 |

Final paragraph that keeps growing.`

// TestMarkdownIncrementalMatchesFullRender feeds the sample one byte at a time
// (as streaming does) and requires the incrementally-rendered output to be
// byte-identical to a fresh render of every prefix.
func TestMarkdownIncrementalMatchesFullRender(t *testing.T) {
	theme := mdTestTheme()
	for _, width := range []int{20, 80} {
		incremental := NewMarkdown("", 1, 0, theme, nil, MarkdownOptions{})
		for n := 1; n <= len(incrementalSample); n++ {
			incremental.SetText(incrementalSample[:n])
			got := incremental.Render(width)

			fresh := NewMarkdown(incrementalSample[:n], 1, 0, theme, nil, MarkdownOptions{})
			want := fresh.Render(width)

			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("width %d prefix %d:\n got %q\nwant %q", width, n, got, want)
			}
		}
	}
}

// TestMarkdownStreamingReusesStablePrefix pins that appending to a rendered
// message only re-renders the changed tail, not every token.
func TestMarkdownStreamingReusesStablePrefix(t *testing.T) {
	theme := mdTestTheme()
	m := NewMarkdown(incrementalSample, 0, 0, theme, nil, MarkdownOptions{})
	m.Render(80)
	full := m.renderedTokens
	if full < 5 {
		t.Fatalf("first render rendered %d tokens, want the whole sample", full)
	}

	m.SetText(incrementalSample + " one more word")
	m.Render(80)
	if tail := m.renderedTokens - full; tail > 3 {
		t.Fatalf("append re-rendered %d tokens, want only the changed tail", tail)
	}
}
