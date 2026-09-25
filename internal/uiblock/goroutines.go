package uiblock

import (
	"sort"

	"golang.org/x/tools/go/ssa"
)

// FindGoroutines lists every bare `go` statement reachable from the UI loop
// whose goroutine body does blocking work.
//
// Such a goroutine does not block the loop itself — the loop never waits on it
// — so it is not a violation of the loop-blocking invariant that Find holds.
// It is *untracked* work: no submission order, no coalescing of overlapping
// runs, and nothing drains it at shutdown. internal/offloop is the port's
// uniform mechanism for exactly those properties, so a bare goroutine that does
// blocking work is a nudge to consider it. The finding is advisory and expected
// to be allowlisted with a reason, the same discipline as the loop invariant.
//
// Channel operations and sleeps are a goroutine's normal tools and are not
// counted as "blocking work"; file/network/exec I/O, lock acquisition, and
// CPU-bound JSON are.
func FindGoroutines(dir string) ([]Finding, error) {
	m, err := loadModule(dir)
	if err != nil {
		return nil, err
	}

	// Walk the loop-reachable functions with the same edges Find uses (pier
	// static calls plus interface-method invocations), but never follow a `go`
	// edge: the goroutine body is examined for blocking work instead.
	type node struct {
		fn    *ssa.Function
		chain []string
	}
	visited := map[*ssa.Function][]string{}
	queue := []node{}
	for fn, label := range m.roots {
		if fn.Blocks == nil {
			continue
		}
		visited[fn] = []string{label}
		queue = append(queue, node{fn, []string{label}})
	}

	var findings []Finding
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, b := range n.fn.Blocks {
			for _, instr := range b.Instrs {
				if goIns, isGo := instr.(*ssa.Go); isGo {
					callee := goIns.Common().StaticCallee()
					if callee != nil && isPier(callee) && goroutineBlocks(callee, m) {
						chain := append(append([]string{}, n.chain...), "go "+callee.RelString(nil))
						findings = append(findings, Finding{Kind: Goroutine, Pos: pos(m.prog, goIns), Chain: chain})
					}
					continue
				}
				call, ok := instr.(ssa.CallInstruction)
				if !ok {
					continue
				}
				com := call.Common()
				if callee := com.StaticCallee(); callee != nil {
					if isPier(callee) && callee.Blocks != nil {
						if _, seen := visited[callee]; !seen {
							chain := append(append([]string{}, n.chain...), callee.RelString(nil))
							visited[callee] = chain
							queue = append(queue, node{callee, chain})
						}
					}
					continue
				}
				if com.IsInvoke() && com.Method != nil {
					name := com.Method.Name()
					for _, impl := range m.pierMethods[name] {
						if !isPier(impl) || impl.Blocks == nil {
							continue
						}
						if _, seen := visited[impl]; !seen {
							chain := append(append([]string{}, n.chain...), impl.RelString(nil)+" (interface "+name+")")
							visited[impl] = chain
							queue = append(queue, node{impl, chain})
						}
					}
				}
			}
		}
	}

	seen := map[string]Finding{}
	for _, f := range findings {
		if prev, ok := seen[f.Pos]; !ok || len(prev.Chain) < len(f.Chain) {
			seen[f.Pos] = f
		}
	}
	list := make([]Finding, 0, len(seen))
	for _, f := range seen {
		list = append(list, f)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Pos < list[j].Pos })
	return list, nil
}

// goroutineBlocks reports whether fn — a goroutine body — does blocking work,
// transitively through pier calls. Channel operations and sleeps are not
// counted: waiting on a channel or sleeping is what a goroutine is for.
func goroutineBlocks(fn *ssa.Function, m *module) bool {
	seen := map[*ssa.Function]bool{}
	var walk func(*ssa.Function) bool
	walk = func(f *ssa.Function) bool {
		if f == nil || f.Blocks == nil || seen[f] {
			return false
		}
		seen[f] = true
		for _, b := range f.Blocks {
			for _, instr := range b.Instrs {
				call, ok := instr.(ssa.CallInstruction)
				if !ok {
					continue
				}
				com := call.Common()
				if callee := com.StaticCallee(); callee != nil {
					if k, ok := blockingCallee(callee); ok && k != Channel && k != Sleep {
						return true
					}
					if isPier(callee) && walk(callee) {
						return true
					}
					continue
				}
				if com.IsInvoke() && com.Method != nil {
					for _, impl := range m.pierMethods[com.Method.Name()] {
						if isPier(impl) && walk(impl) {
							return true
						}
					}
				}
			}
		}
		return false
	}
	return walk(fn)
}
