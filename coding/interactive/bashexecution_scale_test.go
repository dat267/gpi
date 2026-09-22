package interactive

import (
	"strings"
	"testing"
)

// TestBashExecutionAppendDoesNotScaleWithOutput pins the cost of appending a
// streamed chunk to the user-invoked bash component: every chunk rebuilt the
// display from the whole accumulated output (join + TruncateTail + split), so a
// command with 2000 lines of output cost 14 ms and 4000 allocations per chunk
// (118 ms to replay the command).
func TestBashExecutionAppendDoesNotScaleWithOutput(t *testing.T) {
	newRendererTestTheme(t)

	build := func(lines int) *BashExecutionComponent {
		component := NewBashExecutionComponent("go test ./...", nil, false)
		for i := 0; i < lines; i++ {
			component.AppendOutput("a line of command output\n")
		}
		return component
	}
	small, large := build(20), build(2000)

	smallAllocs := testing.AllocsPerRun(5, func() { small.AppendOutput("chunk\n") })
	largeAllocs := testing.AllocsPerRun(5, func() { large.AppendOutput("chunk\n") })

	if largeAllocs > smallAllocs+64 {
		t.Fatalf("appending a chunk allocated %.0f times after 2000 lines vs %.0f after 20; "+
			"the display is rebuilt from the whole accumulated output", largeAllocs, smallAllocs)
	}

	// The tail is still what the preview shows.
	if joined := strings.Join(large.Render(80), "\n"); !strings.Contains(joined, "chunk") {
		t.Fatalf("preview does not show the newest output: %q", joined)
	}
}

// TestBashExecutionAppendKeepsTheTailWindow pins the truncation the display
// relies on now that it is computed from the line buffer instead of the joined
// output: the window must keep the newest lines and count the hidden ones.
func TestBashExecutionAppendKeepsTheTailWindow(t *testing.T) {
	newRendererTestTheme(t)

	component := NewBashExecutionComponent("seq 1 3000", nil, false)
	for i := 0; i < 2500; i++ {
		component.AppendOutput("output line " + itoa(i) + "\n")
	}
	zero := 0
	component.SetComplete(&zero, false, nil, "")

	rendered := strings.Join(component.Render(80), "\n")
	if !strings.Contains(rendered, "output line 2499") {
		t.Fatalf("preview lost the newest line:\n%s", rendered)
	}
	if strings.Contains(rendered, "output line 0\n") {
		t.Fatalf("preview kept the oldest lines:\n%s", rendered)
	}
	if !strings.Contains(rendered, "more lines") {
		t.Fatalf("hidden-line notice missing:\n%s", rendered)
	}
}
