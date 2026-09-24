package interactive

import (
	"context"
	"sync/atomic"

	"github.com/dat267/pier/ai"
	"github.com/dat267/pier/coding"
)

// Port of src/modes/interactive/model-catalog-refresh.ts: share concurrent
// all-catalog refreshes while keeping each caller's cancellation independent.

// ModelCatalogRuntime is the refresh surface the coordinator needs.
type ModelCatalogRuntime interface {
	Refresh(ctx context.Context, options *coding.ModelsRefreshCallOptions) (ai.ModelsRefreshResult, error)
}

type refreshOutcome struct {
	result ai.ModelsRefreshResult
	err    error
}

type activeCatalogRefresh struct {
	cancel context.CancelFunc
	done   chan struct{}

	// Stage 4 gap: no mutex. The outcome is published atomically, waiters and
	// the canceled flag are atomics, and the coordinator's map is republished
	// copy-on-write.
	outcome  atomic.Pointer[refreshOutcome]
	waiters  atomic.Int64
	canceled atomic.Bool
	settled  atomic.Bool
}

// ModelCatalogRefreshCoordinator deduplicates concurrent refreshes per
// runtime (the registry is shared by the model/scoped selectors, the auth
// flows and the CLI's create-time refresh).
type ModelCatalogRefreshCoordinator struct {
	active atomic.Pointer[map[ModelCatalogRuntime]*activeCatalogRefresh]
}

// loadActive returns a snapshot of the registry.
func (c *ModelCatalogRefreshCoordinator) loadActive() map[ModelCatalogRuntime]*activeCatalogRefresh {
	if published := c.active.Load(); published != nil {
		return *published
	}
	return nil
}

// publishIfAbsent inserts the entry unless another caller published one for
// the runtime first; the compare-and-swap loop keeps the check and the store
// atomic.
func (c *ModelCatalogRefreshCoordinator) publishIfAbsent(runtime ModelCatalogRuntime, entry *activeCatalogRefresh) bool {
	for {
		previous := c.active.Load()
		var current map[ModelCatalogRuntime]*activeCatalogRefresh
		if previous != nil {
			current = *previous
		}
		if _, exists := current[runtime]; exists {
			return false
		}
		next := make(map[ModelCatalogRuntime]*activeCatalogRefresh, len(current)+1)
		for key, value := range current {
			next[key] = value
		}
		next[runtime] = entry
		if c.active.CompareAndSwap(previous, &next) {
			return true
		}
	}
}

// unpublish removes the runtime's entry only while it still points at entry.
func (c *ModelCatalogRefreshCoordinator) unpublish(runtime ModelCatalogRuntime, entry *activeCatalogRefresh) {
	for {
		previous := c.active.Load()
		if previous == nil {
			return
		}
		current := *previous
		if current[runtime] != entry {
			return
		}
		next := make(map[ModelCatalogRuntime]*activeCatalogRefresh, len(current))
		for key, value := range current {
			if key == runtime {
				continue
			}
			next[key] = value
		}
		if c.active.CompareAndSwap(previous, &next) {
			return
		}
	}
}

// RefreshModelCatalogs refreshes the model catalogs, sharing an in-flight
// refresh for the same runtime.
func RefreshModelCatalogs(ctx context.Context, runtime ModelCatalogRuntime) (ai.ModelsRefreshResult, error) {
	return defaultCatalogCoordinator.Refresh(ctx, runtime)
}

var defaultCatalogCoordinator = &ModelCatalogRefreshCoordinator{}

// Refresh performs a shared refresh.
func (c *ModelCatalogRefreshCoordinator) Refresh(ctx context.Context, runtime ModelCatalogRuntime) (ai.ModelsRefreshResult, error) {
	if ctx.Err() != nil {
		return ai.ModelsRefreshResult{}, ctx.Err()
	}

	active := c.join(runtime)

	select {
	case <-ctx.Done():
		c.releaseWaiter(runtime, active)
		return ai.ModelsRefreshResult{}, ctx.Err()
	case <-active.done:
		outcome := active.outcome.Load()
		c.releaseWaiter(runtime, active)
		if outcome == nil {
			return ai.ModelsRefreshResult{}, nil
		}
		return outcome.result, outcome.err
	}
}

// join returns the in-flight refresh for the runtime, starting one when none is
// published (or when the previous one was cancelled), and claims a waiter slot
// only once the entry is confirmed published and unsettled.
func (c *ModelCatalogRefreshCoordinator) join(runtime ModelCatalogRuntime) *activeCatalogRefresh {
	for {
		current := c.loadActive()[runtime]
		if current != nil && !current.canceled.Load() && !current.settled.Load() {
			current.waiters.Add(1)
			// Re-validate: a concurrent caller may have replaced the entry
			// between the load and the claim.
			if c.loadActive()[runtime] == current && !current.canceled.Load() && !current.settled.Load() {
				return current
			}
			current.waiters.Add(-1)
			continue
		}
		refreshContext, cancel := context.WithCancel(context.Background())
		created := &activeCatalogRefresh{cancel: cancel, done: make(chan struct{})}
		if !c.publishIfAbsent(runtime, created) {
			// Another caller published first: join theirs on the next pass.
			cancel()
			continue
		}
		created.waiters.Add(1)
		go c.run(runtime, created, refreshContext)
		return created
	}
}

// run performs the refresh and publishes its outcome.
func (c *ModelCatalogRefreshCoordinator) run(runtime ModelCatalogRuntime, active *activeCatalogRefresh, ctx context.Context) {
	result, err := runtime.Refresh(ctx, nil)
	active.outcome.Store(&refreshOutcome{result: result, err: err})
	// Unpublish before signalling completion (D98): the Go port has real
	// concurrency, so a caller arriving after the shared refresh finished must
	// start a fresh refresh instead of joining the completed one and observing
	// its (possibly cancelled) error.
	c.unpublish(runtime, active)
	active.settled.Store(true)
	close(active.done)
}

// activeWaiters reports the number of waiters on the in-flight refresh for a
// runtime (test observation helper).
func (c *ModelCatalogRefreshCoordinator) activeWaiters(runtime ModelCatalogRuntime) int {
	if active, ok := c.loadActive()[runtime]; ok {
		return int(active.waiters.Load())
	}
	return 0
}

func (c *ModelCatalogRefreshCoordinator) releaseWaiter(runtime ModelCatalogRuntime, active *activeCatalogRefresh) {
	if active.waiters.Add(-1) > 0 {
		return
	}
	// The last waiter gave up: cancel the shared operation and let a later
	// caller start fresh (D98). done is closed by run.
	if active.settled.Load() {
		return
	}
	if active.canceled.CompareAndSwap(false, true) {
		c.unpublish(runtime, active)
		active.cancel()
	}
}
