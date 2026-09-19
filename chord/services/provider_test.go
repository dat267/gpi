package services_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dat267/gpi/chord"
	"github.com/dat267/gpi/chord/delta"
	"github.com/dat267/gpi/chord/services"
)

// Tests keyed to upstream packages/chord/src/services/{provider,consumer}.ts.

func mustService(t *testing.T, id string) chord.Service {
	t.Helper()
	service, err := chord.DefineService(id, false)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestDefineServiceRules(t *testing.T) {
	local, err := chord.DefineService("app.local", true)
	if err != nil {
		t.Fatal(err)
	}
	if !local.Local {
		t.Fatal("local services must keep their flag")
	}
	if _, err := chord.DefineService("", false); err == nil {
		t.Fatal("an empty service id must be rejected")
	}
	if _, err := chord.DefineService("$chord.internal", false); err == nil {
		t.Fatal("the reserved namespace must be rejected")
	}
}

func TestProviderCatalogueValidation(t *testing.T) {
	service := mustService(t, "demo")
	if _, err := services.NewRemoteServiceProvider([]services.ProviderEntry{
		{Service: service},
		{Service: service},
	}); err == nil {
		t.Fatal("duplicate catalogue ids must be rejected")
	}
	local, _ := chord.DefineService("local", true)
	if _, err := services.NewRemoteServiceProvider([]services.ProviderEntry{{Service: local}}); err == nil {
		t.Fatal("local services must not be published remotely")
	}

	provider, err := services.NewRemoteServiceProvider([]services.ProviderEntry{
		{Service: service, Mode: chord.ServiceModeSingleton},
	})
	if err != nil {
		t.Fatal(err)
	}
	catalogue := provider.Catalogue()
	if len(catalogue) != 1 || catalogue[0].ServiceID != "demo" || catalogue[0].Mode != chord.ServiceModeSingleton {
		t.Fatalf("catalogue = %#v", catalogue)
	}
	// An unregistered service is not allowlisted.
	other := mustService(t, "other")
	if err := provider.Provide(other, nil, services.Members{}); !isRemoteCode(err, "service_not_allowed") {
		t.Fatalf("err = %v", err)
	}
	if _, err := provider.Use(other); !isRemoteCode(err, "service_not_allowed") {
		t.Fatalf("err = %v", err)
	}
	// A service with no members is rejected.
	if err := provider.Provide(service, nil, services.Members{}); err == nil ||
		!strings.Contains(err.Error(), "has no members") {
		t.Fatalf("err = %v", err)
	}
}

func isRemoteCode(err error, code string) bool {
	var remote *services.RemoteServiceError
	if ok := asRemote(err, &remote); !ok {
		return false
	}
	return remote.Code == code
}

func asRemote(err error, target **services.RemoteServiceError) bool {
	for err != nil {
		if remote, ok := err.(*services.RemoteServiceError); ok {
			*target = remote
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

// countingState builds a mutable state with one key.
func countingState(t *testing.T, initial float64) *services.MutableState {
	t.Helper()
	state, err := services.NewMutableState(map[string]any{"count": initial})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestProviderSingletonLifecycle(t *testing.T) {
	service := mustService(t, "counter")
	state := countingState(t, 0)
	provider, err := services.NewRemoteServiceProvider([]services.ProviderEntry{{Service: service}})
	if err != nil {
		t.Fatal(err)
	}
	greet := func(args []chord.JsonValue, ctx chord.Context) (chord.JsonValue, error) {
		name := "world"
		if len(args) > 0 {
			if text, ok := args[0].(string); ok {
				name = text
			}
		}
		return "hello " + name, nil
	}
	members := services.Members{
		Methods: map[string]services.Method{"greet": greet},
		States:  map[string]services.ReplicatedStateSource{"state": state},
	}
	if err := provider.Provide(service, "impl-1", members); err != nil {
		t.Fatal(err)
	}
	if err := provider.Provide(service, "impl-2", members); !isRemoteCode(err, "service_mode_mismatch") {
		t.Fatalf("err = %v", err)
	}
	implementation, err := provider.Use(service)
	if err != nil || implementation != "impl-1" {
		t.Fatalf("implementation = %v err = %v", implementation, err)
	}

	// A subscription snapshots the singleton with sorted members.
	subscription, err := provider.Subscribe("counter", chord.ServiceModeSingleton, func(*services.ServiceProviderUpdate, chord.Context) {})
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	snapshot := subscription.Snapshot
	if snapshot.Mode != chord.ServiceModeSingleton || snapshot.ServiceID != "counter" || len(snapshot.Instances) != 1 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	membersOut := snapshot.Instances[0].Members
	if len(membersOut) != 2 || membersOut[0].Name != "greet" || membersOut[1].Name != "state" {
		t.Fatalf("members = %#v", membersOut)
	}
	if membersOut[1].Kind != services.MemberState || membersOut[1].Sequence != 0 {
		t.Fatalf("state member = %#v", membersOut[1])
	}
	if len(membersOut[1].Ops) != 1 || membersOut[1].Ops[0].Verb != delta.VerbReplace {
		t.Fatalf("state ops = %#v", membersOut[1].Ops)
	}

	// An unknown service fails the allowlist check first (upstream
	// assertAllowed precedes the registration lookup).
	if _, err := provider.Subscribe("unknown", chord.ServiceModeSingleton, nil); !isRemoteCode(err, "service_not_allowed") {
		t.Fatalf("err = %v", err)
	}
	if _, err := provider.Subscribe("counter", chord.ServiceModeKeyed, nil); !isRemoteCode(err, "service_mode_mismatch") {
		t.Fatalf("err = %v", err)
	}

	// Invoke routes to the method with a trailing context by construction.
	result, err := provider.Invoke(chord.ServiceCall{ServiceID: "counter", Member: "greet", Args: []chord.JsonValue{"pi"}}, context.Background())
	if err != nil || result != "hello pi" {
		t.Fatalf("result = %v err = %v", result, err)
	}
	if _, err := provider.Invoke(chord.ServiceCall{ServiceID: "counter", Member: "missing"}, context.Background()); !isRemoteCode(err, "service_member_not_found") {
		t.Fatalf("err = %v", err)
	}
	if _, err := provider.Invoke(chord.ServiceCall{ServiceID: "counter", Member: "state"}, context.Background()); !isRemoteCode(err, "service_member_mismatch") {
		t.Fatalf("err = %v", err)
	}
	// An explicit instance address on a singleton is a mode mismatch.
	address := &chord.ServiceInstanceAddress{Key: "k", Generation: 1}
	if _, err := provider.Invoke(chord.ServiceCall{ServiceID: "counter", Member: "greet", Instance: address}, context.Background()); !isRemoteCode(err, "service_mode_mismatch") {
		t.Fatalf("err = %v", err)
	}
}

func TestProviderShapePreservationAndReplacement(t *testing.T) {
	service := mustService(t, "shape")
	provider, err := services.NewRemoteServiceProvider([]services.ProviderEntry{{Service: service}})
	if err != nil {
		t.Fatal(err)
	}
	base := services.Members{Methods: map[string]services.Method{"a": func([]chord.JsonValue, chord.Context) (chord.JsonValue, error) {
		return "a", nil
	}}}
	if err := provider.Provide(service, 1, base); err != nil {
		t.Fatal(err)
	}
	// A different shape is rejected by validate and by replace.
	different := services.Members{
		Methods: map[string]services.Method{"a": base.Methods["a"]},
		States:  map[string]services.ReplicatedStateSource{"b": countingState(t, 0)},
	}
	if err := provider.ValidateReplacement(service, different); !isRemoteCode(err, "service_member_mismatch") {
		t.Fatalf("err = %v", err)
	}
	if err := provider.Replace(service, 2, different); !isRemoteCode(err, "service_member_mismatch") {
		t.Fatalf("err = %v", err)
	}
	if implementation, _ := provider.Use(service); implementation != 1 {
		t.Fatalf("a rejected replacement must keep the provider, got %v", implementation)
	}

	// A replacement with the same shape emits `replaced` and keeps the facade.
	updates := make(chan string, 4)
	subscription, err := provider.Subscribe("shape", chord.ServiceModeSingleton, func(update *services.ServiceProviderUpdate, ctx chord.Context) {
		updates <- update.Type
	})
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	_ = subscription.Activate()
	if err := provider.Replace(service, 3, base); err != nil {
		t.Fatal(err)
	}
	if err := provider.Withdraw(service); err != nil {
		t.Fatal(err)
	}
	if implementation, _ := provider.Use(service); implementation != nil {
		t.Fatalf("withdrawn provider must not be usable, got %v", implementation)
	}
	got := []string{}
	for len(got) < 2 {
		select {
		case kind := <-updates:
			got = append(got, kind)
		case <-time.After(2 * time.Second):
			t.Fatalf("updates = %#v", got)
		}
	}
	if got[0] != services.UpdateReplaced || got[1] != services.UpdateUnavailable {
		t.Fatalf("updates = %#v", got)
	}
}

func TestProviderKeyedInstances(t *testing.T) {
	service := mustService(t, "keyed")
	provider, err := services.NewRemoteServiceProvider([]services.ProviderEntry{{Service: service, Mode: chord.ServiceModeKeyed}})
	if err != nil {
		t.Fatal(err)
	}
	state := countingState(t, 0)
	members := services.Members{States: map[string]services.ReplicatedStateSource{"state": state}}
	if _, err := provider.Spawn(service, "", "impl", members); err == nil {
		t.Fatal("an empty key must be rejected")
	}
	closeFirst, err := provider.Spawn(service, "a", "impl-a", members)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Spawn(service, "a", "impl-b", members); !isRemoteCode(err, "service_mode_mismatch") {
		t.Fatalf("err = %v", err)
	}
	closeSecond, err := provider.Spawn(service, "b", "impl-b", members)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSecond()

	updates := make(chan *services.ServiceProviderUpdate, 8)
	subscription, err := provider.Subscribe("keyed", chord.ServiceModeKeyed, func(update *services.ServiceProviderUpdate, ctx chord.Context) {
		updates <- update
	})
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	if err := subscription.Activate(); err != nil {
		t.Fatal(err)
	}
	if len(subscription.Snapshot.Instances) != 2 {
		t.Fatalf("snapshot instances = %#v", subscription.Snapshot.Instances)
	}
	if subscription.Snapshot.Instances[0].Instance.Key != "a" || subscription.Snapshot.Instances[1].Instance.Key != "b" {
		t.Fatalf("instances must be key-sorted: %#v", subscription.Snapshot.Instances)
	}

	// A stale generation is rejected; the live one resolves.
	stale := &chord.ServiceInstanceAddress{Key: "a", Generation: 99}
	if _, err := provider.Invoke(chord.ServiceCall{ServiceID: "keyed", Instance: stale, Member: "state"}, context.Background()); !isRemoteCode(err, "service_stale_instance") {
		t.Fatalf("err = %v", err)
	}
	if _, err := provider.Invoke(chord.ServiceCall{ServiceID: "keyed", Instance: &chord.ServiceInstanceAddress{Key: "zz", Generation: 1}, Member: "state"}, context.Background()); !isRemoteCode(err, "service_instance_not_found") {
		t.Fatalf("err = %v", err)
	}
	// A keyed call without an address is a mode mismatch.
	if _, err := provider.Invoke(chord.ServiceCall{ServiceID: "keyed", Member: "state"}, context.Background()); !isRemoteCode(err, "service_mode_mismatch") {
		t.Fatalf("err = %v", err)
	}

	// Closing an instance emits `closed` with its address, and a respawn uses
	// the next generation.
	closeFirst()
	var closed *services.ServiceProviderUpdate
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case update := <-updates:
			if update.Type == services.UpdateClosed {
				closed = update
			}
		default:
		}
		if closed != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if closed == nil || closed.ClosedInstance == nil || closed.ClosedInstance.Key != "a" || closed.ClosedInstance.Generation != 1 {
		t.Fatalf("closed update = %#v", closed)
	}
	closeAgain, err := provider.Spawn(service, "a", "impl-a2", members)
	if err != nil {
		t.Fatal(err)
	}
	defer closeAgain()
	if instance, _ := provider.Invoke(chord.ServiceCall{ServiceID: "keyed",
		Instance: &chord.ServiceInstanceAddress{Key: "a", Generation: 1}, Member: "state"}, context.Background()); instance != nil {
		t.Fatal("generation 1 must be stale after a respawn")
	}
}

func TestProviderStateUpdatesAndSubscriptionBuffering(t *testing.T) {
	service := mustService(t, "stream")
	state := countingState(t, 0)
	provider, err := services.NewRemoteServiceProvider([]services.ProviderEntry{{Service: service}})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Provide(service, "impl", services.Members{States: map[string]services.ReplicatedStateSource{"state": state}}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var updates []*services.ServiceProviderUpdate
	subscription, err := provider.Subscribe("stream", chord.ServiceModeSingleton, func(update *services.ServiceProviderUpdate, ctx chord.Context) {
		mu.Lock()
		updates = append(updates, update)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()

	// A publication before activation is buffered and replayed on activate.
	state.State().(map[string]any)["count"] = float64(1)
	if err := state.PublishState(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	buffered := len(updates)
	mu.Unlock()
	if buffered != 0 {
		t.Fatalf("updates before activation = %d", buffered)
	}
	if err := subscription.Activate(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	delivered := append([]*services.ServiceProviderUpdate{}, updates...)
	mu.Unlock()
	if len(delivered) != 1 || delivered[0].Type != services.UpdateState {
		t.Fatalf("delivered = %#v", delivered)
	}
	if delivered[0].Sequence != 1 || delivered[0].Member != "state" || len(delivered[0].Ops) == 0 {
		t.Fatalf("state update = %#v", delivered[0])
	}

	// Later publications deliver immediately, and closing stops delivery.
	state.State().(map[string]any)["count"] = float64(2)
	if err := state.PublishState(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(updates)
		mu.Unlock()
		if count == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	subscription.Close()
	state.State().(map[string]any)["count"] = float64(3)
	if err := state.PublishState(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	count := len(updates)
	mu.Unlock()
	if count != 2 {
		t.Fatalf("updates after close = %d", count)
	}

	// Activating after close is a no-op.
	if err := subscription.Activate(); err != nil {
		t.Fatal(err)
	}
}

func TestProviderEndpointControlCalls(t *testing.T) {
	service := mustService(t, "endpoint")
	state := countingState(t, 0)
	provider, err := services.NewRemoteServiceProvider([]services.ProviderEntry{{Service: service}})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Provide(service, "impl", services.Members{
		Methods: map[string]services.Method{"ping": func([]chord.JsonValue, chord.Context) (chord.JsonValue, error) {
			return "pong", nil
		}},
		States: map[string]services.ReplicatedStateSource{"state": state},
	}); err != nil {
		t.Fatal(err)
	}
	endpoint := services.CreateRemoteServiceEndpoint(provider)
	defer endpoint.Dispose()

	// The catalogue control call returns the catalogue.
	catalogue, err := endpoint.Invoke(services.CreateServiceCatalogueCall(), nil, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	entries, ok := catalogue.([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("catalogue = %#v", catalogue)
	}
	if entry := entries[0].(map[string]any); entry["serviceId"] != "endpoint" || entry["mode"] != chord.ServiceModeSingleton {
		t.Fatalf("entry = %#v", entries[0])
	}

	// Subscribe publishes through the publisher and returns the snapshot.
	var published []string
	subscriptionID := "sub-1"
	publish := func(id string, update *services.ServiceProviderUpdate, ctx chord.Context) error {
		if id != subscriptionID {
			return fmt.Errorf("unexpected subscription id %s", id)
		}
		published = append(published, update.Type)
		return nil
	}
	snapshot, err := endpoint.Invoke(services.CreateServiceSubscribeCall(subscriptionID, "endpoint", chord.ServiceModeSingleton), publish, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil {
		t.Fatal("subscribe must return a snapshot")
	}
	if _, err := endpoint.Invoke(services.CreateServiceSubscribeCall(subscriptionID, "endpoint", chord.ServiceModeSingleton), publish, context.Background()); err == nil ||
		!strings.Contains(err.Error(), "already active") {
		t.Fatalf("err = %v", err)
	}

	// Updates after activation flow through publish; publisher failures are
	// swallowed (upstream attaches a catch).
	state.State().(map[string]any)["count"] = float64(1)
	if err := state.PublishState(context.Background()); err != nil {
		t.Fatal(err)
	}
	failing := func(id string, update *services.ServiceProviderUpdate, ctx chord.Context) error {
		return fmt.Errorf("publish failed")
	}
	state.State().(map[string]any)["count"] = float64(2)
	if err := state.PublishState(context.Background()); err != nil {
		t.Fatalf("publisher failures must not propagate: %v", err)
	}
	_ = failing

	// A method call passes through the endpoint.
	result, err := endpoint.Invoke(chord.ServiceCall{ServiceID: "endpoint", Member: "ping"}, publish, context.Background())
	if err != nil || result != "pong" {
		t.Fatalf("result = %v err = %v", result, err)
	}

	// Unsubscribe removes the subscription; a second unsubscribe fails.
	if _, err := endpoint.Invoke(services.CreateServiceUnsubscribeCall(subscriptionID), publish, context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := endpoint.Invoke(services.CreateServiceUnsubscribeCall(subscriptionID), publish, context.Background()); err == nil ||
		!strings.Contains(err.Error(), "was not found") {
		t.Fatalf("err = %v", err)
	}
	endpoint.Dispose()
	if _, err := endpoint.Invoke(chord.ServiceCall{ServiceID: "endpoint", Member: "ping"}, publish, context.Background()); err == nil ||
		!strings.Contains(err.Error(), "disposed") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoopbackTransportEndToEnd(t *testing.T) {
	service := mustService(t, "loop")
	state := countingState(t, 0)
	provider, err := services.NewRemoteServiceProvider([]services.ProviderEntry{
		{Service: service},
		{Service: mustService(t, "keyed-svc"), Mode: chord.ServiceModeKeyed},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Provide(service, "impl", services.Members{
		Methods: map[string]services.Method{"sum": func(args []chord.JsonValue, ctx chord.Context) (chord.JsonValue, error) {
			total := float64(0)
			for _, arg := range args {
				if number, ok := arg.(float64); ok {
					total += number
				}
			}
			return total, nil
		}},
		States: map[string]services.ReplicatedStateSource{"state": state},
	}); err != nil {
		t.Fatal(err)
	}

	transport := services.CreateLoopbackServiceTransport(provider)
	binding, err := services.NewRemoteServiceBinding(services.RemoteServiceBindingOptions{
		Services:  []chord.Service{service, mustService(t, "keyed-svc")},
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	facade, err := binding.Use(service)
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The method is callable and the state hydrates from the snapshot.
	result, err := facade.Member("sum").Call([]chord.JsonValue{float64(1), float64(2)}, context.Background())
	if err != nil || result != float64(3) {
		t.Fatalf("result = %v err = %v", result, err)
	}
	value, ok, err := facade.Member("state").Value()
	if err != nil || !ok || value.(map[string]any)["count"] != float64(0) {
		t.Fatalf("state = %#v err = %v", value, err)
	}

	// A published state change reaches the consumer replica.
	state.State().(map[string]any)["count"] = float64(5)
	if err := state.PublishState(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if current, hydrated, _ := facade.Member("state").Value(); hydrated && current.(map[string]any)["count"] == float64(5) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	value, _, err = facade.Member("state").Value()
	if err != nil || value.(map[string]any)["count"] != float64(5) {
		t.Fatalf("state = %#v err = %v", value, err)
	}

	// Using a member as the wrong kind is a coded error.
	if _, _, err := facade.Member("sum").Value(); !isRemoteCode(err, "service_member_mismatch") {
		t.Fatalf("err = %v", err)
	}
	if _, err := facade.Member("state").Call(nil, context.Background()); !isRemoteCode(err, "service_member_mismatch") {
		t.Fatalf("err = %v", err)
	}

	// Singleton replacement and withdrawal flow through the same facade.
	if err := provider.Replace(service, "impl2", services.Members{
		Methods: map[string]services.Method{"sum": func(args []chord.JsonValue, ctx chord.Context) (chord.JsonValue, error) {
			return float64(42), nil
		}},
		States: map[string]services.ReplicatedStateSource{"state": countingState(t, 7)},
	}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if current, hydrated, _ := facade.Member("state").Value(); hydrated && current.(map[string]any)["count"] == float64(7) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if value, _, err := facade.Member("state").Value(); err != nil || value.(map[string]any)["count"] != float64(7) {
		t.Fatalf("replacement state = %#v err = %v", value, err)
	}

	// Rebinding to detached makes handle access fail, and rebinding back
	// re-hydrates the state.
	if err := binding.Rebind(false, context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := facade.Member("sum").Call(nil, context.Background()); !isRemoteCode(err, "service_stale_instance") {
		t.Fatalf("err = %v", err)
	}
	if err := binding.Rebind(true, context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := binding.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if value, _, err := facade.Member("state").Value(); err != nil || value.(map[string]any)["count"] != float64(7) {
		t.Fatalf("state after rebind = %#v err = %v", value, err)
	}

	if err := binding.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := binding.Use(service); err == nil {
		t.Fatal("using a disposed binding must fail")
	}
	if err := binding.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestKeyedObserveAndLifecycle(t *testing.T) {
	keyedService := mustService(t, "keyed-observe")
	provider, err := services.NewRemoteServiceProvider([]services.ProviderEntry{{Service: keyedService, Mode: chord.ServiceModeKeyed}})
	if err != nil {
		t.Fatal(err)
	}
	providerName := func(name string) services.Members {
		return services.Members{
			Methods: map[string]services.Method{"name": func([]chord.JsonValue, chord.Context) (chord.JsonValue, error) {
				return name, nil
			}},
			States: map[string]services.ReplicatedStateSource{"state": countingState(t, 0)},
		}
	}
	closeFirst, err := provider.Spawn(keyedService, "a", "a", providerName("a"))
	if err != nil {
		t.Fatal(err)
	}
	closeSecond, err := provider.Spawn(keyedService, "b", "b", providerName("b"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeSecond()
	defer closeFirst()

	binding, err := services.NewRemoteServiceBinding(services.RemoteServiceBindingOptions{
		Services:  []chord.Service{keyedService},
		Transport: services.CreateLoopbackServiceTransport(provider),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Dispose(context.Background())

	var mu sync.Mutex
	started := map[string]*services.GuardedFacade{}
	released := map[string]bool{}
	_, err = binding.Observe(keyedService, func(service *services.GuardedFacade, ctx context.Context) error {
		member := service.Member("state")
		mu.Lock()
		started[service.Address().Key] = service
		mu.Unlock()
		member.Subscribe(func(value chord.JsonValue, ctx chord.Context, delivery chord.ReplicatedStateDelivery) {})
		<-ctx.Done()
		mu.Lock()
		released[service.Address().Key] = true
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(started)
		mu.Unlock()
		if count == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	count := len(started)
	mu.Unlock()
	if count != 2 {
		t.Fatalf("observed instances = %d", count)
	}

	// The guarded handle calls the instance method.
	mu.Lock()
	facadeA := started["a"]
	mu.Unlock()
	name, err := facadeA.Member("name").Call(nil, context.Background())
	if err != nil || name != "a" {
		t.Fatalf("name = %v err = %v", name, err)
	}

	// A new instance starts immediately for the live observer.
	closeThird, err := provider.Spawn(keyedService, "c", "c", providerName("c"))
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(started)
		mu.Unlock()
		if count == 3 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	count = len(started)
	mu.Unlock()
	if count != 3 {
		t.Fatalf("observed instances after spawn = %d", count)
	}

	// Closing an instance cancels its observation.
	closeThird()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := released["c"]
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	done := released["c"]
	mu.Unlock()
	if !done {
		t.Fatal("closing an instance must cancel its observation")
	}

	// A stopped observation rejects access through the guarded handle.
	mu.Lock()
	facadeC := started["c"]
	mu.Unlock()
	if _, err := facadeC.Member("name").Call(nil, context.Background()); !isRemoteCode(err, "service_stale_instance") {
		t.Fatalf("err = %v", err)
	}
}

func TestConsumerAllowlistAndModeChecks(t *testing.T) {
	service := mustService(t, "allowed")
	provider, err := services.NewRemoteServiceProvider([]services.ProviderEntry{{Service: service}})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Provide(service, "impl", services.Members{Methods: map[string]services.Method{"ping": func([]chord.JsonValue, chord.Context) (chord.JsonValue, error) {
		return "pong", nil
	}}}); err != nil {
		t.Fatal(err)
	}
	binding, err := services.NewRemoteServiceBinding(services.RemoteServiceBindingOptions{
		Services:  []chord.Service{service},
		Transport: services.CreateLoopbackServiceTransport(provider),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer binding.Dispose(context.Background())

	if _, err := binding.Use(mustService(t, "not-allowed")); !isRemoteCode(err, "service_not_allowed") {
		t.Fatalf("err = %v", err)
	}
	local, _ := chord.DefineService("local", true)
	if _, err := binding.Use(local); !isRemoteCode(err, "service_not_allowed") {
		t.Fatalf("err = %v", err)
	}
	if _, err := binding.Use(service); err != nil {
		t.Fatal(err)
	}
	// The same service cannot be used as both singleton and keyed.
	if _, err := binding.Observe(service, func(*services.GuardedFacade, context.Context) error { return nil }); !isRemoteCode(err, "service_mode_mismatch") {
		t.Fatalf("err = %v", err)
	}
	// Duplicate allowlist entries are rejected.
	if _, err := services.NewRemoteServiceBinding(services.RemoteServiceBindingOptions{
		Services:  []chord.Service{service, service},
		Transport: services.CreateLoopbackServiceTransport(provider),
	}); err == nil {
		t.Fatal("duplicate allowlist ids must be rejected")
	}
}

func TestConsumerReportsStartFailures(t *testing.T) {
	service := mustService(t, "failing")
	var mu sync.Mutex
	var reported []error
	binding, err := services.NewRemoteServiceBinding(services.RemoteServiceBindingOptions{
		Services:  []chord.Service{service},
		Transport: &failingTransport{},
		OnError: func(err error) {
			mu.Lock()
			reported = append(reported, err)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = binding.Use(service)
	if err != nil {
		t.Fatal(err)
	}
	if readyErr := binding.Ready(context.Background()); readyErr != nil {
		t.Fatalf("readiness must not fail on a start error: %v", readyErr)
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
	if len(reported) != 1 || !strings.Contains(reported[0].Error(), "transport exploded") {
		t.Fatalf("reported = %#v", reported)
	}
	_ = binding.Dispose(context.Background())
}

type failingTransport struct{}

func (t *failingTransport) Invoke(call chord.ServiceCall, ctx chord.Context) (chord.JsonValue, error) {
	return nil, fmt.Errorf("transport exploded")
}

func (t *failingTransport) Subscribe(
	serviceID string,
	mode chord.ServiceMode,
	listener func(update *services.ServiceProviderUpdate, ctx chord.Context),
	ctx chord.Context,
) (*services.ServiceSubscription, error) {
	return nil, fmt.Errorf("transport exploded")
}
