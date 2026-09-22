package coding

import (
	"fmt"
	"strings"
	"testing"
)

// TestTruncateTailBuildsTheWindowWithoutPrepending pins the bash tool's
// per-chunk snapshot cost: the tail window was built by prepending each kept
// line into a fresh slice, so a 100 KB tail (up to 2000 lines) cost 2713
// allocations and ~7 ms on every output chunk — the tool snapshots per 64 KB
// read, which is why a running command stuttered the UI.
func TestTruncateTailBuildsTheWindowWithoutPrepending(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 4000; i++ {
		fmt.Fprintf(&sb, "build output line %d with some text to make it a realistic width\n", i)
	}
	content := sb.String()

	measure := func(maxLines int) int64 {
		return testing.Benchmark(func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = TruncateTail(content, TruncationOptions{MaxLines: maxLines, MaxBytes: len(content)})
			}
		}).AllocedBytesPerOp()
	}
	smallBytes := measure(50)
	largeBytes := measure(2000)

	// Keeping 40x more lines must not cost ~40x more bytes: the content is
	// split once either way, and the window is appended, not prepended.
	if largeBytes > smallBytes*10 {
		t.Fatalf("truncating to 2000 lines allocated %d bytes vs %d for 50 lines; "+
			"the window is prepended per line (quadratic)", largeBytes, smallBytes)
	}
}

// TestTruncateTailKeepsTheNewestLines pins the window order after the fix.
func TestTruncateTailKeepsTheNewestLines(t *testing.T) {
	content := "one\ntwo\nthree\nfour\nfive"
	result := TruncateTail(content, TruncationOptions{MaxLines: 2, MaxBytes: len(content)})
	if result.Content != "four\nfive" {
		t.Fatalf("content = %q", result.Content)
	}
	if !result.Truncated || result.TruncatedBy != TruncatedByLines {
		t.Fatalf("truncation = %+v", result)
	}
	if result.OutputLines != 2 || result.TotalLines != 5 {
		t.Fatalf("lines = %d/%d", result.OutputLines, result.TotalLines)
	}
}
