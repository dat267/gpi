package interactive

import (
	"context"
	"sync"

	"github.com/dat267/gpi/ai"
	"github.com/dat267/gpi/coding"
)

// Port of src/modes/interactive/model-catalog-refresh.ts: share concurrent
// all-catalog refreshes while keeping each caller's cancellation independent.

// ModelCatalogRuntime is the refresh surface the coordinator needs.
type ModelCatalogRuntime interface {
	Refresh(ctx context.Context, options *coding.ModelsRefreshCallOptions) (ai.ModelsRefreshResult, error)
}

type activeCatalogRefresh struct {
	cancel  context.CancelFunc
	done    chan struct{}
	mu      sync.Mutex
	result  ai.ModelsRefreshResult
	err     error
	waiters int
}

// ModelCatalogRefreshCoordinator deduplicates concurrent refreshes per
// runtime.
type ModelCatalogRefreshCoordinator struct {
	mu     sync.Mutex
	active map[ModelCatalogRuntime]*activeCatalogRefresh
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

	c.mu.Lock()
	if c.active == nil {
		c.active = map[ModelCatalogRuntime]*activeCatalogRefresh{}
	}
	active, ok := c.active[runtime]
	if !ok {
		refreshContext, cancel := context.WithCancel(context.Background())
		active = &activeCatalogRefresh{cancel: cancel, done: make(chan struct{})}
		c.active[runtime] = active
		go func() {
			result, err := runtime.Refresh(refreshContext, nil)
			active.mu.Lock()
			active.result = result
			active.err = err
			active.mu.Unlock()
			close(active.done)
			c.mu.Lock()
			if c.active[runtime] == active {
				delete(c.active, runtime)
			}
			c.mu.Unlock()
		}()
	}
	active.waiters++
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		c.releaseWaiter(runtime, active)
		return ai.ModelsRefreshResult{}, ctx.Err()
	case <-active.done:
		active.mu.Lock()
		result := active.result
		err := active.err
		active.mu.Unlock()
		c.releaseWaiter(runtime, active)
		return result, err
	}
}

func (c *ModelCatalogRefreshCoordinator) releaseWaiter(runtime ModelCatalogRuntime, active *activeCatalogRefresh) {
	c.mu.Lock()
	defer c.mu.Unlock()
	active.waiters--
	if active.waiters == 0 && c.active[runtime] == active {
		// The last waiter gave up: cancel the shared operation.
		active.cancel()
	}
}
