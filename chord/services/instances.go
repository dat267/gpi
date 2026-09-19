package services

import (
	"context"
	"fmt"
	"sync"
)

// Port of src/services/instances.ts.

// DirectoryEntry is one keyed instance in a Directory (upstream
// InstanceDirectoryEntry).
type DirectoryEntry interface {
	EntryKey() string
	EntryGeneration() int
	// EntryService is the implementation handed to observers.
	EntryService() any
	// Deactivate releases the instance.
	Deactivate()
}

// EntryKey is a DirectoryEntry that is comparable, so entries are compared by
// identity the way upstream compares object references.
type EntryKey interface {
	DirectoryEntry
	comparable
}

// Directory owns keyed instance lifetime and the cancellable tasks observing
// those instances (upstream InstanceDirectory).
type Directory[E EntryKey] struct {
	mu        sync.Mutex
	entries   map[string]E
	order     []string
	observers map[int]*directoryObserver
	nextID    int
	report    func(error)
	ready     bool
	disposed  bool
}

type directoryObserver struct {
	id      int
	handler func(service any, ctx context.Context) error
	tasks   map[string]context.CancelFunc
	closed  bool
}

// NewDirectory builds a directory that starts (ready=true) or buffered
// (ready=false) until Ready is called.
func NewDirectory[E EntryKey](ready bool, onError func(error)) *Directory[E] {
	if onError == nil {
		onError = func(error) {}
	}
	return &Directory[E]{
		entries:   map[string]E{},
		observers: map[int]*directoryObserver{},
		report:    onError,
		ready:     ready,
	}
}

// ObserverCount is the number of live observers.
func (d *Directory[E]) ObserverCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.observers)
}

// Get returns one entry by key.
func (d *Directory[E]) Get(key string) (E, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	entry, ok := d.entries[key]
	return entry, ok
}

// Entries returns the live entries in insertion order.
func (d *Directory[E]) Entries() []E {
	d.mu.Lock()
	defer d.mu.Unlock()
	entries := make([]E, 0, len(d.order))
	for _, key := range d.order {
		if entry, ok := d.entries[key]; ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

// Insert adds a new instance; a live instance with the same key is an error.
func (d *Directory[E]) Insert(entry E) error {
	if err := d.assertActive(); err != nil {
		return err
	}
	d.mu.Lock()
	key := entry.EntryKey()
	if _, exists := d.entries[key]; exists {
		d.mu.Unlock()
		return fmt.Errorf("Keyed service already has a live instance with key %s", key)
	}
	d.entries[key] = entry
	d.order = append(d.order, key)
	ready := d.ready
	d.mu.Unlock()
	if ready {
		d.startAll(entry)
	}
	return nil
}

// Replace swaps an instance, rejecting a repeated generation.
func (d *Directory[E]) Replace(entry E) error {
	if err := d.assertActive(); err != nil {
		return err
	}
	d.mu.Lock()
	key := entry.EntryKey()
	previous, exists := d.entries[key]
	if exists && previous.EntryGeneration() == entry.EntryGeneration() {
		d.mu.Unlock()
		return fmt.Errorf("Keyed service repeated a live generation")
	}
	d.mu.Unlock()
	if exists {
		d.remove(previous)
	}
	d.mu.Lock()
	d.entries[key] = entry
	d.order = append(d.order, key)
	ready := d.ready
	d.mu.Unlock()
	if ready {
		d.startAll(entry)
	}
	return nil
}

// Remove deactivates an instance by identity (a no-op for stale entries).
func (d *Directory[E]) Remove(entry E) {
	d.remove(entry)
}

// Ready starts observing every live instance.
func (d *Directory[E]) Ready() error {
	if err := d.assertActive(); err != nil {
		return err
	}
	d.mu.Lock()
	if d.ready {
		d.mu.Unlock()
		return nil
	}
	d.ready = true
	entries := d.entriesSnapshotLocked()
	d.mu.Unlock()
	for _, entry := range entries {
		d.startAll(entry)
	}
	return nil
}

// Reset drops readiness and every live instance.
func (d *Directory[E]) Reset() {
	d.mu.Lock()
	if d.disposed {
		d.mu.Unlock()
		return
	}
	d.ready = false
	entries := d.entriesSnapshotLocked()
	d.mu.Unlock()
	for _, entry := range entries {
		d.remove(entry)
	}
}

// Observe registers a handler that runs per live instance with its own
// cancellable context; the returned function stops observing.
func (d *Directory[E]) Observe(handler func(service any, ctx context.Context) error) (func(), error) {
	if err := d.assertActive(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	id := d.nextID
	d.nextID++
	observer := &directoryObserver{id: id, handler: handler, tasks: map[string]context.CancelFunc{}}
	d.observers[id] = observer
	ready := d.ready
	entries := d.entriesSnapshotLocked()
	d.mu.Unlock()

	if ready {
		for _, entry := range entries {
			d.start(observer, entry)
		}
	}
	return func() {
		d.mu.Lock()
		if observer.closed {
			d.mu.Unlock()
			return
		}
		observer.closed = true
		for _, cancel := range observer.tasks {
			cancel()
		}
		observer.tasks = map[string]context.CancelFunc{}
		delete(d.observers, id)
		d.mu.Unlock()
	}, nil
}

// Dispose stops every observer and deactivates every instance.
func (d *Directory[E]) Dispose() {
	d.mu.Lock()
	if d.disposed {
		d.mu.Unlock()
		return
	}
	d.disposed = true
	entries := d.entriesSnapshotLocked()
	observers := make([]*directoryObserver, 0, len(d.observers))
	for _, observer := range d.observers {
		observer.closed = true
		for _, cancel := range observer.tasks {
			cancel()
		}
		observer.tasks = map[string]context.CancelFunc{}
		observers = append(observers, observer)
	}
	d.observers = map[int]*directoryObserver{}
	d.entries = map[string]E{}
	d.order = nil
	d.mu.Unlock()
	for _, entry := range entries {
		entry.Deactivate()
	}
}

func (d *Directory[E]) remove(entry E) {
	key := entry.EntryKey()
	d.mu.Lock()
	current, ok := d.entries[key]
	if !ok || current != entry {
		d.mu.Unlock()
		return
	}
	delete(d.entries, key)
	d.order = removeKey(d.order, key)
	observers := make([]*directoryObserver, 0, len(d.observers))
	for _, observer := range d.observers {
		observers = append(observers, observer)
	}
	for _, observer := range observers {
		if cancel, ok := observer.tasks[key]; ok {
			cancel()
			delete(observer.tasks, key)
		}
	}
	d.mu.Unlock()
	entry.Deactivate()
}

func (d *Directory[E]) startAll(entry E) {
	d.mu.Lock()
	observers := make([]*directoryObserver, 0, len(d.observers))
	for _, observer := range d.observers {
		observers = append(observers, observer)
	}
	d.mu.Unlock()
	for _, observer := range observers {
		d.start(observer, entry)
	}
}

func (d *Directory[E]) start(observer *directoryObserver, entry E) {
	key := entry.EntryKey()
	d.mu.Lock()
	if observer.closed {
		d.mu.Unlock()
		return
	}
	if _, running := observer.tasks[key]; running {
		d.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	observer.tasks[key] = cancel
	d.mu.Unlock()

	// The handler runs asynchronously; failures are reported unless the
	// observation was cancelled (upstream's ignored promise with a catch).
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				if ctx.Err() == nil {
					d.report(toReplicatedStateError(recovered))
				}
			}
		}()
		if err := observer.handler(entry.EntryService(), ctx); err != nil && ctx.Err() == nil {
			d.report(err)
		}
	}()
}

func (d *Directory[E]) entriesSnapshotLocked() []E {
	entries := make([]E, 0, len(d.order))
	for _, key := range d.order {
		if entry, ok := d.entries[key]; ok {
			entries = append(entries, entry)
		}
	}
	return entries
}

func (d *Directory[E]) assertActive() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.disposed {
		return fmt.Errorf("Keyed service directory is disposed")
	}
	return nil
}

func removeKey(keys []string, target string) []string {
	out := keys[:0]
	for _, key := range keys {
		if key != target {
			out = append(out, key)
		}
	}
	return out
}
