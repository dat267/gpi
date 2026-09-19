package services

import (
	"context"
	"fmt"
	"sync"

	"github.com/dat267/gpi/chord"
	"github.com/dat267/gpi/chord/delta"
)

// Port of src/services/state.ts and src/services/state-internals.ts.

// ReplicatedStateSource is the internal view a provider uses to observe and
// publish one replicated state (upstream's ReplicatedStateInternals).
//
// D19: upstream keeps a WeakMap from state object to internals; Go states
// implement this interface directly, so GetReplicatedStateInternals is a type
// assertion.
type ReplicatedStateSource interface {
	// Sequence is the last published sequence (0 before the first publish).
	Sequence() int
	// Published is the current immutable published value.
	Published() chord.JsonValue
	// PublishState emits pending changes.
	PublishState(ctx context.Context) error
	// SubscribeOps observes published op batches.
	SubscribeOps(listener func(ops []delta.Op, sequence int, ctx context.Context)) func()
}

// GetReplicatedStateInternals returns the internals of a replicated state value,
// or false when the value is not one (upstream getReplicatedStateInternals).
func GetReplicatedStateInternals(value any) (ReplicatedStateSource, bool) {
	source, ok := value.(ReplicatedStateSource)
	return source, ok
}

// MutableState is the host-owned mutable replicated state (upstream
// MutableReplicatedStateImpl).
//
// D11/D12 note: upstream tracks mutations through a JS Proxy and replays the
// recorded ops; the Go port diffs the mutable state against the last published
// value at publish time. The published value is identical, but the op batch may
// be ordered/compressed differently (same rule as the delta engine).
type MutableState struct {
	mu          sync.Mutex
	state       chord.JsonValue
	published   chord.JsonValue
	sequence    int
	listeners   map[int]func(value chord.JsonValue, ctx context.Context, delivery chord.ReplicatedStateDelivery)
	sources     map[int]func(ops []delta.Op, sequence int, ctx context.Context)
	nextID      int
	publishLock sync.Mutex
}

// NewMutableState builds a replicated state over a mutable JSON value.
//
// The state keeps the caller's value (writes must go through State); the
// published value starts as a deep clone, matching upstream's
// applyImmutable(undefined, flush()).
func NewMutableState(initial chord.JsonValue) (*MutableState, error) {
	if !chord.IsJSONValue(initial) {
		return nil, fmt.Errorf("Replicated state must be a JSON value")
	}
	// The fresh tracker's first flush is a base batch carrying a deep clone
	// (upstream tracks `forceBase = true` and emits [["r", cloneJson(root)]]).
	published := chord.CloneJSON(initial)
	return &MutableState{
		state:     initial,
		published: published,
		listeners: map[int]func(value chord.JsonValue, ctx context.Context, delivery chord.ReplicatedStateDelivery){},
		sources:   map[int]func(ops []delta.Op, sequence int, ctx context.Context){},
	}, nil
}

// Published is the immutable published value.
func (s *MutableState) Published() chord.JsonValue {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.published
}

// Value is the immutable published value (upstream's `value` getter).
func (s *MutableState) Value() (chord.JsonValue, bool) {
	return s.Published(), true
}

// State is the mutable tracked state.
func (s *MutableState) State() chord.JsonValue {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// Sequence is the last published sequence.
func (s *MutableState) Sequence() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sequence
}

// PublishState diffs the mutable state against the published value and emits
// the resulting ops.
func (s *MutableState) PublishState(ctx context.Context) error {
	// Publication is serialized so concurrent publishers cannot interleave
	// diffs against the same published value.
	s.publishLock.Lock()
	defer s.publishLock.Unlock()

	s.mu.Lock()
	state := s.state
	published := s.published
	s.mu.Unlock()

	ops, err := delta.Diff(published, state, nil)
	if err != nil {
		return err
	}
	if len(ops) == 0 {
		return nil
	}
	next, err := delta.ApplyImmutable(published, ops)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.sequence++
	sequence := s.sequence
	s.published = next
	next = s.published
	sourceListeners := make([]func(ops []delta.Op, sequence int, ctx context.Context), 0, len(s.sources))
	for _, listener := range s.sources {
		sourceListeners = append(sourceListeners, listener)
	}
	valueListeners := make([]func(value chord.JsonValue, ctx context.Context, delivery chord.ReplicatedStateDelivery), 0, len(s.listeners))
	for _, listener := range s.listeners {
		valueListeners = append(valueListeners, listener)
	}
	s.mu.Unlock()

	for _, listener := range sourceListeners {
		listener(ops, sequence, ctx)
	}
	delivery := chord.ReplicatedStateDelivery{Kind: chord.DeliveryUpdate, Sequence: sequence}
	for _, listener := range valueListeners {
		listener(next, ctx, delivery)
	}
	return nil
}

// Publish is the upstream `publish(context)` (values are JSON, so it cannot fail
// on the state itself).
func (s *MutableState) Publish(ctx context.Context) {
	if err := s.PublishState(ctx); err != nil {
		panic(err)
	}
}

// Subscribe registers a value listener, flushing pending changes first
// (upstream's subscribe).
func (s *MutableState) Subscribe(listener func(value chord.JsonValue, ctx context.Context, delivery chord.ReplicatedStateDelivery)) func() {
	ctx := context.Background()
	_ = s.PublishState(ctx)

	s.mu.Lock()
	id := s.nextID
	s.nextID++
	s.listeners[id] = listener
	value := s.published
	sequence := s.sequence
	s.mu.Unlock()

	listener(value, ctx, chord.ReplicatedStateDelivery{Kind: chord.DeliveryHydrate, Sequence: sequence})
	return func() {
		s.mu.Lock()
		delete(s.listeners, id)
		s.mu.Unlock()
	}
}

// SubscribeOps observes published op batches (upstream's internals subscribe).
func (s *MutableState) SubscribeOps(listener func(ops []delta.Op, sequence int, ctx context.Context)) func() {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	s.sources[id] = listener
	s.mu.Unlock()
	return func() {
		s.mu.Lock()
		delete(s.sources, id)
		s.mu.Unlock()
	}
}

// StateReplica is a cold read-only state used by service consumers until a
// complete snapshot arrives (upstream ReplicatedStateReplica).
type StateReplica struct {
	mu       sync.Mutex
	report   func(error)
	value    chord.JsonValue
	hasValue bool
	sequence int

	listeners map[int]func(value chord.JsonValue, ctx context.Context, delivery chord.ReplicatedStateDelivery)
	nextID    int
}

// NewStateReplica builds a cold replica that reports listener failures.
func NewStateReplica(reportError func(error)) *StateReplica {
	if reportError == nil {
		reportError = func(error) {}
	}
	return &StateReplica{
		report:    reportError,
		listeners: map[int]func(value chord.JsonValue, ctx context.Context, delivery chord.ReplicatedStateDelivery){},
	}
}

// Value is the current value, absent until hydration.
func (r *StateReplica) Value() (chord.JsonValue, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.value, r.hasValue
}

// Subscribe registers a listener; a hydrated replica delivers immediately.
func (r *StateReplica) Subscribe(listener func(value chord.JsonValue, ctx context.Context, delivery chord.ReplicatedStateDelivery)) func() {
	r.mu.Lock()
	id := r.nextID
	r.nextID++
	r.listeners[id] = listener
	value, hasValue := r.value, r.hasValue
	sequence := r.sequence
	r.mu.Unlock()

	if hasValue {
		r.deliver(listener, value, context.Background(), chord.ReplicatedStateDelivery{Kind: chord.DeliveryHydrate, Sequence: sequence})
	}
	return func() {
		r.mu.Lock()
		delete(r.listeners, id)
		r.mu.Unlock()
	}
}

// Hydrate installs a base snapshot.
func (r *StateReplica) Hydrate(sequence int, ops []delta.Op, ctx context.Context) error {
	if !delta.IsBase(ops) {
		return fmt.Errorf("Replicated state snapshot is not a base operation batch")
	}
	value, err := delta.ApplyImmutable(nil, ops)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.sequence = sequence
	r.value = value
	r.hasValue = true
	listeners := r.snapshotListeners()
	r.mu.Unlock()
	r.deliverAll(listeners, value, ctx, chord.ReplicatedStateDelivery{Kind: chord.DeliveryHydrate, Sequence: sequence})
	return nil
}

// Update applies one ordered delta on top of the hydrated value.
func (r *StateReplica) Update(sequence int, ops []delta.Op, ctx context.Context) error {
	r.mu.Lock()
	if !r.hasValue {
		r.mu.Unlock()
		return fmt.Errorf("Replicated state received an update before hydration")
	}
	if sequence != r.sequence+1 {
		r.mu.Unlock()
		r.Clear()
		return fmt.Errorf("Replicated state update sequence has a gap")
	}
	value, err := delta.ApplyImmutable(r.value, ops)
	if err != nil {
		r.mu.Unlock()
		return err
	}
	r.sequence = sequence
	r.value = value
	listeners := r.snapshotListeners()
	r.mu.Unlock()
	r.deliverAll(listeners, value, ctx, chord.ReplicatedStateDelivery{Kind: chord.DeliveryUpdate, Sequence: sequence})
	return nil
}

// Clear drops the hydrated value.
func (r *StateReplica) Clear() {
	r.mu.Lock()
	r.value = nil
	r.hasValue = false
	r.sequence = 0
	r.mu.Unlock()
}

// Sequence is the hydrated sequence, or 0 when cold.
func (r *StateReplica) Sequence() (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sequence, r.hasValue
}

func (r *StateReplica) snapshotListeners() []func(value chord.JsonValue, ctx context.Context, delivery chord.ReplicatedStateDelivery) {
	listeners := make([]func(value chord.JsonValue, ctx context.Context, delivery chord.ReplicatedStateDelivery), 0, len(r.listeners))
	for _, listener := range r.listeners {
		listeners = append(listeners, listener)
	}
	return listeners
}

func (r *StateReplica) deliverAll(
	listeners []func(value chord.JsonValue, ctx context.Context, delivery chord.ReplicatedStateDelivery),
	value chord.JsonValue,
	ctx context.Context,
	delivery chord.ReplicatedStateDelivery,
) {
	for _, listener := range listeners {
		r.deliver(listener, value, ctx, delivery)
	}
}

// deliver runs one listener, reporting panics instead of propagating them
// (upstream wraps listener calls in try/catch and reports the error).
func (r *StateReplica) deliver(
	listener func(value chord.JsonValue, ctx context.Context, delivery chord.ReplicatedStateDelivery),
	value chord.JsonValue,
	ctx context.Context,
	delivery chord.ReplicatedStateDelivery,
) {
	defer func() {
		if recovered := recover(); recovered != nil {
			r.report(toReplicatedStateError(recovered))
		}
	}()
	listener(value, ctx, delivery)
}

// ServiceDeliveryContext is the synthetic context for deliveries without a
// caller (upstream serviceDeliveryContext).
func ServiceDeliveryContext() context.Context { return context.Background() }

func toReplicatedStateError(value any) error {
	if err, ok := value.(error); ok {
		return err
	}
	return fmt.Errorf("%v", value)
}
