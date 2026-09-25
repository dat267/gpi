package uiblock

import (
	"strings"
	"testing"

	"golang.org/x/tools/go/ssa"
)

// TestNothingBlockingReachableFromTheUIGoroutine holds the invariant that no
// blocking work runs on the main event loop. Every finding is a call site that
// can block the loop — syscalls, network, exec, blocking channel operations,
// locks, sleeps, or CPU-bound JSON — reachable from the loop's phases, the
// components it renders, or the closures marshaled onto it. New violations
// fail here with the chain that reaches them; accepted sites get an explicit
// allowlist entry with a reason.
func TestNothingBlockingReachableFromTheUIGoroutine(t *testing.T) {
	if testing.Short() {
		t.Skip("loads and builds SSA for the whole module")
	}
	findings, err := Find("../..")
	if err != nil {
		t.Fatalf("analysis failed: %v", err)
	}
	// Accepted surfaces, keyed by the head of the reachability chain (the wiring
	// field or root that pulls it). A new blocking call under an allowed surface
	// is accepted with it; the reason says why the surface is safe.
	allowlist := map[string]string{
		"wiring field OnExternalEditor": "the external editor takes over the terminal synchronously, by design (upstream identical)",
		"wiring field ShowAuthSelect":   "the auth flow invokes it on its own goroutine; it blocks waiting for the dialog, which renders on the loop",
		"wiring field TakeCrash":        "runs during Init, before the loop starts",
		"wiring field CheckVersion":     "invoked only from the notifyNewVersion goroutine (go w.notifyNewVersion)",
		"wiring field WriteDebugLog":    "user-invoked /debug; MkdirAll, one WriteFile and agent-dir path resolution, sub-millisecond",
	}
	for _, f := range findings {
		if reason, ok := allowlist[f.Chain[0]]; ok {
			t.Logf("allowed: %s %s (%s)", f.Kind, f.Pos, reason)
			continue
		}
		t.Errorf("%s %s\n  chain: %s", f.Kind, f.Pos, strings.Join(f.Chain, " -> "))
	}
}

// TestBareBlockingGoroutinesAreAllowlisted lists `go` statements reachable
// from the UI loop whose goroutine body does blocking work. A goroutine does
// not block the loop — the loop never waits on it — so this is not the
// invariant above; it is a nudge toward internal/offloop, which supplies the
// submission order, coalescing and shutdown drain a bare goroutine lacks.
// Accepted sites get an allowlist entry with a reason.
func TestBareBlockingGoroutinesAreAllowlisted(t *testing.T) {
	if testing.Short() {
		t.Skip("loads and builds SSA for the whole module")
	}
	findings, err := FindGoroutines("../..")
	if err != nil {
		t.Fatalf("analysis failed: %v", err)
	}
	allowlist := map[string]string{}
	for _, f := range findings {
		if reason, ok := allowlist[f.Pos]; ok {
			t.Logf("allowed: %s %s (%s)", f.Kind, f.Pos, reason)
			continue
		}
		t.Errorf("%s %s: blocking work on a bare goroutine reachable from the UI loop; use internal/offloop\n  chain: %s",
			f.Kind, f.Pos, strings.Join(f.Chain, " -> "))
	}
}

// TestGoroutineBlockClassifierHasTeeth guards the check above from silently
// passing because the classifier never fires: at least one pier goroutine body
// in the module must be classified as blocking.
func TestGoroutineBlockClassifierHasTeeth(t *testing.T) {
	if testing.Short() {
		t.Skip("loads and builds SSA for the whole module")
	}
	m, err := loadModule("../..")
	if err != nil {
		t.Fatalf("analysis failed: %v", err)
	}
	for fn := range m.all {
		if fn.Blocks == nil || !isPier(fn) {
			continue
		}
		for _, b := range fn.Blocks {
			for _, instr := range b.Instrs {
				goIns, ok := instr.(*ssa.Go)
				if !ok {
					continue
				}
				if callee := goIns.Common().StaticCallee(); callee != nil && isPier(callee) && goroutineBlocks(callee, m) {
					return
				}
			}
		}
	}
	t.Fatal("no pier goroutine body classified as blocking; the classifier is likely broken")
}
