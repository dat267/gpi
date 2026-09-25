package coding

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Benchmarks for the session load path. They run only under -bench, never in
// the plain test run. The fixture mirrors a real aged session: a long message
// history, a compaction that cuts it, and a small projected tail — the shape
// that makes the buffered loader skip ~98% of the file's bytes.

func buildSessionFixture(tb testing.TB, history, tail int) (path string, size int) {
	tb.Helper()
	var buf strings.Builder
	buf.WriteString(`{"type":"session","version":3,"id":"s1","timestamp":"t0","cwd":"/tmp"}` + "\n")
	text := strings.Repeat("Some message content with a few words. ", 60) // ~2.2KB
	id := func(i int) string { return fmt.Sprintf("m%06d", i) }
	prev := ""
	writeMessage := func(i int) {
		fmt.Fprintf(&buf, `{"type":"message","id":%q,"parentId":%s,"timestamp":"t","message":{"role":"user","content":%q}}`+"\n",
			id(i), nullOrID(prev), text)
		prev = id(i)
	}
	for i := 0; i < history; i++ {
		writeMessage(i)
	}
	fmt.Fprintf(&buf, `{"type":"compaction","id":"c1","parentId":%q,"timestamp":"t","summary":"summary","firstKeptEntryId":%q}`+"\n", prev, prev)
	prev = "c1"
	for i := history; i < history+tail; i++ {
		writeMessage(i)
	}
	dir := tb.TempDir()
	path = filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(buf.String()), 0o644); err != nil {
		tb.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		tb.Fatal(err)
	}
	return path, int(info.Size())
}

func nullOrID(prev string) string {
	if prev == "" {
		return "null"
	}
	return `"` + prev + `"`
}

func BenchmarkLoadEntriesEager(b *testing.B) {
	for _, history := range []int{1000, 5000} {
		b.Run(fmt.Sprintf("history=%d", history), func(b *testing.B) {
			path, size := buildSessionFixture(b, history, 400)
			b.ReportAllocs()
			b.SetBytes(int64(size))
			for b.Loop() {
				if _, err := LoadEntriesFromFile(path); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkLoadEntriesBuffered(b *testing.B) {
	for _, history := range []int{1000, 5000} {
		b.Run(fmt.Sprintf("history=%d", history), func(b *testing.B) {
			path, size := buildSessionFixture(b, history, 400)
			b.ReportAllocs()
			b.SetBytes(int64(size))
			for b.Loop() {
				entries, data, fast, err := LoadEntriesFromFileBuffered(path)
				if err != nil || !fast {
					b.Fatalf("err=%v fast=%v", err, fast)
				}
				_ = entries
				_ = data
			}
		})
	}
}

// BenchmarkOpenSessionLazy covers the manager wiring around the load: the
// buffer retention, the index build, and the shell construction. The cache-scan
// seed runs in a background goroutine and may overlap iterations; its cost is
// off the measured path but its CPU competes, so treat this as an upper bound.
func BenchmarkOpenSessionLazy(b *testing.B) {
	for _, history := range []int{1000, 5000} {
		b.Run(fmt.Sprintf("history=%d", history), func(b *testing.B) {
			path, size := buildSessionFixture(b, history, 400)
			b.ReportAllocs()
			b.SetBytes(int64(size))
			for b.Loop() {
				m, err := OpenSession(path, filepath.Dir(path), "/tmp")
				if err != nil {
					b.Fatal(err)
				}
				select {
				case <-m.cacheSeedDone:
				default:
				}
			}
		})
	}
}

// BenchmarkProjectionAfterLoad measures the leaf projection on a lazily loaded
// manager. The first call decodes the window's messages into the memoization
// cache; later calls are warm, which is the steady state the UI renders in.
func BenchmarkProjectionAfterLoad(b *testing.B) {
	for _, warm := range []bool{false, true} {
		b.Run(fmt.Sprintf("warm=%v", warm), func(b *testing.B) {
			path, _ := buildSessionFixture(b, 5000, 400)
			m, err := OpenSession(path, filepath.Dir(path), "/tmp")
			if err != nil {
				b.Fatal(err)
			}
			<-m.cacheSeedDone
			if warm {
				m.BuildContextEntriesForLeaf()
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if entries := m.BuildContextEntriesForLeaf(); len(entries) == 0 {
					b.Fatal("empty projection")
				}
			}
		})
	}
}
