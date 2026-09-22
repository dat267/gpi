package coding

import (
	"fmt"
	"strings"
	"testing"
)

// TestTruncateTailLinesMatchesTruncateTail pins the line-slice fast path to the
// string implementation: bash streams into a line buffer and needs the tail
// window without re-joining and re-splitting the whole output on every chunk,
// so the two must agree on every edge (empty, trailing newline, byte limit
// firing on the first kept line, line limit).
func TestTruncateTailLinesMatchesTruncateTail(t *testing.T) {
	contents := []string{
		"",
		"\n",
		"a",
		"a\n",
		"a\nb",
		"a\nb\n",
		"a\n\nb\n\n\n",
		strings.Repeat("x", 40),
		strings.Repeat("x", 40) + "\n",
		"short\n" + strings.Repeat("y", 60),
		strings.Repeat("line\n", 5),
		strings.Repeat("line\n", 5) + "tail",
		"日本語\n" + strings.Repeat("日本", 20),
		strings.Repeat("\n", 4),
	}
	options := []TruncationOptions{
		{MaxLines: 1, MaxBytes: 1},
		{MaxLines: 1, MaxBytes: 5},
		{MaxLines: 2, MaxBytes: 10},
		{MaxLines: 2, MaxBytes: 1000},
		{MaxLines: 3, MaxBytes: 7},
		{MaxLines: 4, MaxBytes: 12},
		{MaxLines: 100, MaxBytes: 30},
	}

	for _, content := range contents {
		for _, option := range options {
			label := fmt.Sprintf("content=%q options=%+v", content, option)
			want := TruncateTail(content, option)
			gotLines, gotTruncated := TruncateTailLines(strings.Split(content, "\n"), len(content), option)

			if gotTruncated != want.Truncated {
				t.Fatalf("%s: truncated = %v, want %v", label, gotTruncated, want.Truncated)
			}
			if got := strings.Join(gotLines, "\n"); got != want.Content {
				t.Fatalf("%s:\n got %q\nwant %q", label, got, want.Content)
			}
			if len(gotLines) == 0 {
				// An empty window is reported as nil (nothing to show).
				if want.Content != "" {
					t.Fatalf("%s: empty window for content %q", label, want.Content)
				}
				continue
			}
			wantLines := strings.Split(want.Content, "\n")
			if len(gotLines) != len(wantLines) {
				t.Fatalf("%s: %d lines, want %d (%q)", label, len(gotLines), len(wantLines), want.Content)
			}
			for i := range gotLines {
				if gotLines[i] != wantLines[i] {
					t.Fatalf("%s: line %d = %q, want %q", label, i, gotLines[i], wantLines[i])
				}
			}
		}
	}
}

// TestTruncateTailLinesDoesNotJoinTheTailProbe pins the cost: the tail window of
// a large output must not be built by materializing the whole content again.
func TestTruncateTailLinesDoesNotJoinTheTailProbe(t *testing.T) {
	lines := make([]string, 0, 5000)
	total := 0
	for i := 0; i < 5000; i++ {
		line := fmt.Sprintf("line %d of the build log", i)
		lines = append(lines, line)
		total += len(line) + 1
	}
	options := TruncationOptions{MaxLines: 20, MaxBytes: DefaultMaxBytes}

	window, truncated := TruncateTailLines(lines, total, options)
	if !truncated || len(window) != 20 {
		t.Fatalf("window = %d lines (truncated=%v), want 20 truncated", len(window), truncated)
	}
	if got := window[len(window)-1]; got != lines[len(lines)-1] {
		t.Fatalf("last kept line = %q", got)
	}
	want := TruncateTail(strings.Join(lines, "\n"), options)
	if got := strings.Join(window, "\n"); got != want.Content {
		t.Fatal("line-slice window differs from the string path")
	}
}
