package uiblock

import (
	"strings"
	"testing"
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
