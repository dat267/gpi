package coding

import (
	"strings"
	"testing"
)

// TestPrepareCompactionProjectsTheContextOnce pins the manual-compaction freeze:
// the context entries were re-derived inside the loop that walks them
// (BuildContextEntries called once per entry), so preparing a manual compaction
// on a 15.5k-entry session burned ~20 s and ~50 GB of allocations before the
// summarization request was even built.
//
// Bytes allocated, not wall time: the redundant rebuilds of a path slice are
// quadratic in bytes but barely visible in allocation *count* (the projection is
// a view, not a copy), and a ratio keeps the test machine-independent.
func TestPrepareCompactionProjectsTheContextOnce(t *testing.T) {
	build := func(rounds int) []SessionEntry {
		m, _ := newTestSession(t)
		for i := 0; i < rounds; i++ {
			m.AppendMessage(createUserMessage(strings.Repeat("u", 400)))
			m.AppendMessage(assistantTestMessage())
		}
		return m.GetBranch("")
	}
	// A small keep budget so every size still cuts: the loop then walks the
	// whole context on each iteration.
	settings := CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecentTokens: 100}
	if prep := PrepareCompaction(build(20), settings); prep == nil {
		t.Fatal("preparation must not be empty for the scaling test to mean anything")
	}

	measure := func(entries []SessionEntry) int64 {
		return testing.Benchmark(func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				PrepareCompaction(entries, settings)
			}
		}).AllocedBytesPerOp()
	}
	small, large := build(80), build(160)
	smallBytes, largeBytes := measure(small), measure(large)

	// Twice the entries is twice the bytes when the projection is built once;
	// the per-entry rebuild measured 4.0x.
	if largeBytes > smallBytes*3 {
		t.Fatalf("preparing a compaction allocated %d bytes on a %d-entry path vs %d on a %d-entry one; "+
			"the context projection is being rebuilt per entry", largeBytes, len(large), smallBytes, len(small))
	}
}
