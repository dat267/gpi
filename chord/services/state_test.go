package services_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/dat267/pier/chord"
	"github.com/dat267/pier/chord/delta"
	"github.com/dat267/pier/chord/services"
)

// Tests for the replicated-state runtime and the instance directory, keyed to
// upstream packages/chord/src/services/{state,instances}.ts.

func TestMutableStatePublishesDiffs(t *testing.T) {
	initial := map[string]any{"count": float64(0), "label": "a"}
	state, err := services.NewMutableState(initial)
	if err != nil {
		t.Fatal(err)
	}

	// The published value starts as a deep clone, so mutating the state does
	// not change it before publication.
	published := state.Published().(map[string]any)
	if published["count"] != float64(0) {
		t.Fatalf("published = %#v", published)
	}
	state.State().(map[string]any)["count"] = float64(1)
	if state.Published().(map[string]any)["count"] != float64(0) {
		t.Fatal("mutation must not affect the published value before publish")
	}
	if sequence := state.Sequence(); sequence != 0 {
		t.Fatalf("sequence = %d", sequence)
	}

	// Publishing emits the diff, bumps the sequence, and advances the value.
	var opsBatches [][]delta.Op
	var sequences []int
	state.SubscribeOps(func(ops []delta.Op, sequence int, ctx context.Context) error {
		opsBatches = append(opsBatches, ops)
		sequences = append(sequences, sequence)
		return nil
	})
	if err := state.PublishState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(opsBatches) != 1 || len(opsBatches[0]) == 0 {
		t.Fatalf("ops = %#v", opsBatches)
	}
	if sequences[0] != 1 {
		t.Fatalf("sequence = %d", sequences[0])
	}
	if state.Published().(map[string]any)["count"] != float64(1) {
		t.Fatalf("published = %#v", state.Published())
	}

	// A publication with no changes emits nothing.
	if err := state.PublishState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(opsBatches) != 1 {
		t.Fatalf("unchanged publish must be a no-op, ops = %d", len(opsBatches))
	}

	// Nested mutation through the state is published as a delta.
	state.State().(map[string]any)["nested"] = map[string]any{"deep": float64(2)}
	if err := state.PublishState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if state.Sequence() != 2 {
		t.Fatalf("sequence = %d", state.Sequence())
	}
}

func TestMutableStateSubscribeHydratesAndUpdates(t *testing.T) {
	state, err := services.NewMutableState(map[string]any{"value": float64(0)})
	if err != nil {
		t.Fatal(err)
	}
	// A pending change is flushed by subscribe before the hydrate delivery.
	state.State().(map[string]any)["value"] = float64(1)

	type delivery struct {
		value    chord.JsonValue
		kind     string
		sequence int
	}
	var deliveries []delivery
	var mu sync.Mutex
	unsubscribe := state.Subscribe(func(value chord.JsonValue, ctx context.Context, d chord.ReplicatedStateDelivery) {
		mu.Lock()
		deliveries = append(deliveries, delivery{value: chord.CloneJSON(value), kind: d.Kind, sequence: d.Sequence})
		mu.Unlock()
	})
	defer unsubscribe()

	mu.Lock()
	first := deliveries[0]
	mu.Unlock()
	if first.kind != chord.DeliveryHydrate || first.sequence != 1 || first.value.(map[string]any)["value"] != float64(1) {
		t.Fatalf("first delivery = %#v", first)
	}

	state.State().(map[string]any)["value"] = float64(2)
	if err := state.PublishState(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	last := deliveries[len(deliveries)-1]
	mu.Unlock()
	if last.kind != chord.DeliveryUpdate || last.sequence != 2 || last.value.(map[string]any)["value"] != float64(2) {
		t.Fatalf("last delivery = %#v", last)
	}

	// The delivered values are immutable snapshots: later publications do not
	// mutate earlier revisions.
	if first.value.(map[string]any)["value"] != float64(1) {
		t.Fatalf("earlier revision changed: %#v", first.value)
	}

	unsubscribe()
	state.State().(map[string]any)["value"] = float64(3)
	if err := state.PublishState(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	count := len(deliveries)
	mu.Unlock()
	if count != 2 {
		t.Fatalf("unsubscribed listener received %d deliveries", count)
	}
}

func TestMutableStateRejectsNonJSON(t *testing.T) {
	if _, err := services.NewMutableState(func() {}); err == nil {
		t.Fatal("non-JSON state must be rejected")
	}
	if _, err := services.NewMutableState(map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("non-JSON members must be rejected")
	}
}

func TestReplicatedStateInternalsLookup(t *testing.T) {
	state, err := services.NewMutableState(map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	source, ok := services.GetReplicatedStateInternals(state)
	if !ok {
		t.Fatal("a mutable state must expose its internals")
	}
	if source.Sequence() != 0 {
		t.Fatalf("sequence = %d", source.Sequence())
	}
	if _, ok := services.GetReplicatedStateInternals(map[string]any{}); ok {
		t.Fatal("a plain value must not expose internals")
	}
	if _, ok := services.GetReplicatedStateInternals(nil); ok {
		t.Fatal("nil must not expose internals")
	}
}

func TestStateReplicaHydrateUpdateAndGaps(t *testing.T) {
	var reported []error
	replica := services.NewStateReplica(func(err error) { reported = append(reported, err) })

	if _, ok := replica.Value(); ok {
		t.Fatal("a cold replica has no value")
	}
	if err := replica.Update(1, []delta.Op{{Verb: delta.VerbReplace, Path: delta.Path{}, Value: map[string]any{}}}, context.Background()); err == nil {
		t.Fatal("update before hydration must fail")
	}

	// Hydration requires a base batch.
	if err := replica.Hydrate(1, []delta.Op{{Verb: delta.VerbSet, Path: delta.Path{"a"}, Value: float64(1)}}, context.Background()); err == nil {
		t.Fatal("a non-base snapshot must be rejected")
	}

	var deliveries []chord.ReplicatedStateDelivery
	var values []chord.JsonValue
	replica.Subscribe(func(value chord.JsonValue, ctx context.Context, delivery chord.ReplicatedStateDelivery) {
		values = append(values, chord.CloneJSON(value))
		deliveries = append(deliveries, delivery)
	})

	base := []delta.Op{{Verb: delta.VerbReplace, Path: delta.Path{}, Value: map[string]any{"list": []any{float64(1)}}}}
	if err := replica.Hydrate(1, base, context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 || deliveries[0].Kind != chord.DeliveryHydrate || deliveries[0].Sequence != 1 {
		t.Fatalf("hydrate deliveries = %#v", deliveries)
	}

	// A late subscriber hydrates from the current value.
	late := 0
	unsubscribeLate := replica.Subscribe(func(value chord.JsonValue, ctx context.Context, delivery chord.ReplicatedStateDelivery) {
		late++
		if delivery.Kind != chord.DeliveryHydrate || delivery.Sequence != 1 {
			t.Errorf("late delivery = %#v", delivery)
		}
	})
	unsubscribeLate()
	if late != 1 {
		t.Fatalf("late subscriber deliveries = %d", late)
	}

	// A contiguous update applies as a delta.
	// Array appends are recorded as `s` one past the end (upstream's tracker).
	update := []delta.Op{{Verb: delta.VerbSet, Path: delta.Path{"list", 1}, Value: float64(2)}}
	if err := replica.Update(2, update, context.Background()); err != nil {
		t.Fatal(err)
	}
	value, _ := replica.Value()
	if list := value.(map[string]any)["list"].([]any); len(list) != 2 || list[1] != float64(2) {
		t.Fatalf("value = %#v", value)
	}
	if deliveries[len(deliveries)-1].Kind != chord.DeliveryUpdate {
		t.Fatalf("delivery = %#v", deliveries[len(deliveries)-1])
	}

	// A sequence gap clears the replica and fails.
	if err := replica.Update(4, update, context.Background()); err == nil {
		t.Fatal("a sequence gap must fail")
	}
	if _, ok := replica.Value(); ok {
		t.Fatal("a gap must clear the value")
	}
}

func TestStateReplicaReportsListenerFailures(t *testing.T) {
	var reported []error
	replica := services.NewStateReplica(func(err error) { reported = append(reported, err) })
	second := 0
	replica.Subscribe(func(value chord.JsonValue, ctx context.Context, delivery chord.ReplicatedStateDelivery) {
		panic("listener exploded")
	})
	replica.Subscribe(func(value chord.JsonValue, ctx context.Context, delivery chord.ReplicatedStateDelivery) {
		second++
	})
	base := []delta.Op{{Verb: delta.VerbReplace, Path: delta.Path{}, Value: map[string]any{}}}
	if err := replica.Hydrate(1, base, context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(reported) != 1 || reported[0].Error() != "listener exploded" {
		t.Fatalf("reported = %#v", reported)
	}
	if second != 1 {
		t.Fatalf("later listeners must still be delivered, second = %d", second)
	}

	replica.Clear()
	if _, ok := replica.Value(); ok {
		t.Fatal("clear must drop the value")
	}
}

type testEntry struct {
	key        string
	generation int
	service    any
}

func (e *testEntry) EntryKey() string     { return e.key }
func (e *testEntry) EntryGeneration() int { return e.generation }
func (e *testEntry) EntryService() any    { return e.service }
func (e *testEntry) Deactivate()          {}

func TestDirectoryInsertReplaceRemove(t *testing.T) {
	var deactivated []string
	entry := &deactivatingEntry{key: "a", generation: 1, onDeactivate: func() { deactivated = append(deactivated, "a") }}

	directory := services.NewDirectory[*deactivatingEntry](false, nil)
	if err := directory.Insert(entry); err != nil {
		t.Fatal(err)
	}
	if err := directory.Insert(entry); err == nil {
		t.Fatal("a duplicate key must be rejected")
	}
	if _, ok := directory.Get("a"); !ok {
		t.Fatal("entry must be present")
	}

	// Replacing with the same generation is rejected; a new generation
	// deactivates the previous instance.
	if err := directory.Replace(&deactivatingEntry{key: "a", generation: 1}); err == nil {
		t.Fatal("a repeated live generation must be rejected")
	}
	replacement := &deactivatingEntry{key: "a", generation: 2}
	if err := directory.Replace(replacement); err != nil {
		t.Fatal(err)
	}
	if len(deactivated) != 1 {
		t.Fatalf("deactivated = %#v", deactivated)
	}

	// Removing a stale entry is a no-op.
	directory.Remove(entry)
	if _, ok := directory.Get("a"); !ok {
		t.Fatal("a stale removal must not clear the live instance")
	}
	directory.Remove(replacement)
	if _, ok := directory.Get("a"); ok {
		t.Fatal("the live instance must be removed")
	}
	if !replacement.deactivated() {
		t.Fatal("the replacement must be deactivated")
	}
}

type deactivatingEntry struct {
	key          string
	generation   int
	mu           sync.Mutex
	onDeactivate func()
	service      any
	dead         bool
}

func (e *deactivatingEntry) deactivated() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dead
}

func (e *deactivatingEntry) EntryKey() string     { return e.key }
func (e *deactivatingEntry) EntryGeneration() int { return e.generation }
func (e *deactivatingEntry) EntryService() any    { return e.service }
func (e *deactivatingEntry) Deactivate() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.dead = true
	if e.onDeactivate != nil {
		e.onDeactivate()
	}
}

func TestDirectoryReadyGatesObservers(t *testing.T) {
	directory := services.NewDirectory[*deactivatingEntry](false, nil)
	entry := &deactivatingEntry{key: "a", generation: 1}
	if err := directory.Insert(entry); err != nil {
		t.Fatal(err)
	}

	started := make(chan string, 4)
	stop, err := directory.Observe(func(service any, ctx context.Context) error {
		started <- "started"
		<-ctx.Done()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
		t.Fatal("observers must not start before ready")
	case <-time.After(30 * time.Millisecond):
	}
	if directory.ObserverCount() != 1 {
		t.Fatalf("observerCount = %d", directory.ObserverCount())
	}

	if err := directory.Ready(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("ready must start the pending observer")
	}

	// Insertions after ready start immediately.
	second := &deactivatingEntry{key: "b", generation: 1}
	if err := directory.Insert(second); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("a new instance must start the observer")
	}

	// Stopping the observer cancels every task.
	stop()
	if directory.ObserverCount() != 0 {
		t.Fatalf("observerCount = %d", directory.ObserverCount())
	}
}

func TestDirectoryResetAndDispose(t *testing.T) {
	var mu sync.Mutex
	tasks := 0
	cancelled := 0
	directory := services.NewDirectory[*deactivatingEntry](false, nil)
	directory.Insert(&deactivatingEntry{key: "a", generation: 1})
	directory.Insert(&deactivatingEntry{key: "b", generation: 1})

	_, err := directory.Observe(func(service any, ctx context.Context) error {
		mu.Lock()
		tasks++
		mu.Unlock()
		<-ctx.Done()
		mu.Lock()
		cancelled++
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := directory.Ready(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		started := tasks
		mu.Unlock()
		if started == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	started := tasks
	mu.Unlock()
	if started != 2 {
		t.Fatalf("tasks = %d, want 2", started)
	}

	// Reset drops readiness and every instance but keeps observers.
	directory.Reset()
	if len(directory.Entries()) != 0 {
		t.Fatalf("entries = %#v", directory.Entries())
	}
	entry := &deactivatingEntry{key: "c", generation: 1}
	if err := directory.Insert(entry); err != nil {
		t.Fatal(err)
	}
	select {
	case <-time.After(30 * time.Millisecond):
		// Reset cleared readiness, so the new instance must not start yet.
	}

	// Dispose cancels observers and deactivates entries; further use fails.
	directory.Dispose()
	if _, err := directory.Observe(func(service any, ctx context.Context) error { return nil }); err == nil {
		t.Fatal("observing a disposed directory must fail")
	}
	if err := directory.Insert(&deactivatingEntry{key: "d", generation: 1}); err == nil {
		t.Fatal("inserting into a disposed directory must fail")
	}
	directory.Dispose()
}

func TestDirectoryReportsObserverFailures(t *testing.T) {
	var mu sync.Mutex
	var reported []string
	directory := services.NewDirectory[*deactivatingEntry](true, func(err error) {
		mu.Lock()
		reported = append(reported, err.Error())
		mu.Unlock()
	})
	if err := directory.Insert(&deactivatingEntry{key: "a", generation: 1}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	_, err := directory.Observe(func(service any, ctx context.Context) error {
		defer close(done)
		return fmt.Errorf("observer failed")
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("observer must run")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(reported)
		mu.Unlock()
		if count > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reported) != 1 || reported[0] != "observer failed" {
		t.Fatalf("reported = %#v", reported)
	}
}
