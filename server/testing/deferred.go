// Package testing holds the Go port of @earendil-works/pi-server/testing:
// transport conformance helpers (test host, test server, wire client).
package testing

import (
	"context"
	"sync"
)

// Deferred is a promise-like single-assignment value (upstream's Deferred<T>).
type Deferred[T any] struct {
	once  sync.Once
	value chan T
}

// NewDeferred builds an unresolved deferred.
func NewDeferred[T any]() *Deferred[T] {
	return &Deferred[T]{value: make(chan T, 1)}
}

// Resolve settles the deferred; later calls are ignored.
func (d *Deferred[T]) Resolve(value T) {
	d.once.Do(func() { d.value <- value })
}

// Promise is the channel that receives the settled value.
func (d *Deferred[T]) Promise() <-chan T { return d.value }

// Await blocks for the value or the context deadline.
func (d *Deferred[T]) Await(ctx context.Context) (T, error) {
	var zero T
	select {
	case value := <-d.value:
		return value, nil
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}
