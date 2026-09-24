package coding

import (
	"runtime"
	"testing"
)

// allocatedBytesPerRun returns the bytes TotalAlloc grew by, per call, over
// runs calls of fn. TotalAlloc is a monotonic counter, so the delta is exact
// (GC interleaving cannot perturb it) and the ratios these tests assert on stay
// machine-independent.
//
// It exists because testing.Benchmark — the obvious tool, and what the scaling
// tests used — has a hard one-second floor per measurement, so those tests paid
// ~1 s twice each of pure timer wait on every run, race detector or not.
// Bytes rather than allocation counts because the regressions they guard are
// quadratic in bytes but nearly invisible in counts (a rebuilt path slice grows,
// it does not multiply).
func allocatedBytesPerRun(t *testing.T, runs int, fn func()) int64 {
	t.Helper()
	if runs < 1 {
		t.Fatal("allocatedBytesPerRun needs at least one run")
	}
	fn() // warm lazily built state, the way testing.AllocsPerRun does
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < runs; i++ {
		fn()
	}
	runtime.ReadMemStats(&after)
	return int64(after.TotalAlloc-before.TotalAlloc) / int64(runs)
}
