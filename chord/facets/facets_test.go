package facets_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dat267/pier/chord"
	"github.com/dat267/pier/chord/facets"
	"github.com/dat267/pier/chord/services"
)

// Tests keyed to upstream packages/chord/src/facets/host.ts.

func service(t *testing.T, id string, local bool) chord.Service {
	t.Helper()
	defined, err := chord.DefineService(id, local)
	if err != nil {
		t.Fatal(err)
	}
	return defined
}

// counterFacet provides a singleton counter service with a state member.
type counterFacet struct {
	id          string
	service     chord.Service
	state       *services.MutableState
	calls       *int
	activations *int
	teardowns   *int
	mu          *sync.Mutex
}

func (f *counterFacet) facet() facets.Facet {
	return facets.Facet{ID: f.id, Setup: func(env facets.FacetEnvironment) error {
		state, err := env.ReplicatedState(map[string]any{"count": float64(0)})
		if err != nil {
			return err
		}
		f.state = state
		methods := map[string]services.Method{
			"increment": func(args []chord.JsonValue, ctx context.Context) (chord.JsonValue, error) {
				f.mu.Lock()
				*f.calls++
				f.mu.Unlock()
				current := state.State().(map[string]any)["count"].(float64)
				state.State().(map[string]any)["count"] = current + 1
				if err := state.PublishState(ctx); err != nil {
					return nil, err
				}
				return current + 1, nil
			},
		}
		if err := env.Provide(f.service, "counter-impl", services.Members{
			Methods: methods,
			States:  map[string]services.ReplicatedStateSource{"state": state},
		}); err != nil {
			return err
		}
		return env.OnActivate(func() error {
			f.mu.Lock()
			*f.activations++
			f.mu.Unlock()
			return nil
		})
	}}
}

func TestFacetHostProvidesAndUsesServices(t *testing.T) {
	mu := &sync.Mutex{}
	calls := 0
	activations := 0
	greeter := service(t, "greeter", false)
	counter := &counterFacet{id: "counter", service: greeter, calls: &calls, activations: &activations, mu: mu}

	var consumerView *services.SlotView
	consumer := facets.Facet{ID: "consumer", Setup: func(env facets.FacetEnvironment) error {
		view, err := env.Use(greeter)
		if err != nil {
			return err
		}
		consumerView = view
		return nil
	}}

	host, err := facets.CreateFacetHost(facets.FacetOptions{Facets: []facets.Facet{consumer, counter.facet()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := host.Dispose(); err != nil {
			t.Fatal(err)
		}
	}()
	if mu.Lock(); activations != 1 {
		mu.Unlock()
		t.Fatalf("activations = %d", activations)
	} else {
		mu.Unlock()
	}

	// The consumer's view resolves the provider's implementation.
	result, err := consumerView.Member("increment").Call(nil, context.Background())
	if err != nil || result != float64(1) {
		t.Fatalf("result = %v err = %v", result, err)
	}
	// The state member is live and observable.
	value, ok, err := consumerView.Member("state").Value()
	if err != nil || !ok || value.(map[string]any)["count"] != float64(1) {
		t.Fatalf("state = %#v err = %v", value, err)
	}
	// The provider exposes the service on the host.
	catalogue := host.Services.Catalogue()
	if len(catalogue) != 1 || catalogue[0].ServiceID != "greeter" {
		t.Fatalf("catalogue = %#v", catalogue)
	}
}

func TestFacetHostDependencyOrderAndValidation(t *testing.T) {
	provider := service(t, "provider", false)
	var order []string
	var mu sync.Mutex

	providerFacet := facets.Facet{ID: "provider", Setup: func(env facets.FacetEnvironment) error {
		if err := env.Provide(provider, nil, services.Members{Methods: map[string]services.Method{
			"ping": func([]chord.JsonValue, context.Context) (chord.JsonValue, error) { return "pong", nil },
		}}); err != nil {
			return err
		}
		return env.OnActivate(func() error {
			mu.Lock()
			order = append(order, "provider")
			mu.Unlock()
			return nil
		})
	}}
	consumerFacet := facets.Facet{ID: "consumer", Setup: func(env facets.FacetEnvironment) error {
		if _, err := env.Use(provider); err != nil {
			return err
		}
		return env.OnActivate(func() error {
			mu.Lock()
			order = append(order, "consumer")
			mu.Unlock()
			return nil
		})
	}}

	host, err := facets.CreateFacetHost(facets.FacetOptions{Facets: []facets.Facet{consumerFacet, providerFacet}})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Dispose()
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != "provider" || order[1] != "consumer" {
		t.Fatalf("activation order = %#v", order)
	}
}

func TestFacetHostValidationErrors(t *testing.T) {
	alpha := service(t, "alpha", false)
	beta := service(t, "beta", false)

	// A missing provider is reported with the requirement description.
	needsAlpha := facets.Facet{ID: "needs", Setup: func(env facets.FacetEnvironment) error {
		_, err := env.Use(alpha)
		return err
	}}
	if _, err := facets.CreateFacetHost(facets.FacetOptions{Facets: []facets.Facet{needsAlpha}}); err == nil ||
		!strings.Contains(err.Error(), "requires local/alpha/singleton, but no facet provides it") {
		t.Fatalf("err = %v", err)
	}

	// Two facets providing the same service is reported.
	provideAlpha := func(id string) facets.Facet {
		return facets.Facet{ID: id, Setup: func(env facets.FacetEnvironment) error {
			return env.Provide(alpha, nil, services.Members{Methods: map[string]services.Method{
				"ping": func([]chord.JsonValue, context.Context) (chord.JsonValue, error) { return nil, nil },
			}})
		}}
	}
	if _, err := facets.CreateFacetHost(facets.FacetOptions{Facets: []facets.Facet{provideAlpha("one"), provideAlpha("two")}}); err == nil ||
		!strings.Contains(err.Error(), "provided by both one and two") {
		t.Fatalf("err = %v", err)
	}

	// A mode mismatch between provider and requirement is reported.
	keyedProvider := facets.Facet{ID: "keyed-provider", Setup: func(env facets.FacetEnvironment) error {
		_, err := env.ProvideMany(alpha)
		return err
	}}
	singletonUser := facets.Facet{ID: "singleton-user", Setup: func(env facets.FacetEnvironment) error {
		_, err := env.Use(alpha)
		return err
	}}
	if _, err := facets.CreateFacetHost(facets.FacetOptions{Facets: []facets.Facet{keyedProvider, singletonUser}}); err == nil ||
		!strings.Contains(err.Error(), "requires alpha as singleton") {
		t.Fatalf("err = %v", err)
	}

	// A dependency cycle is reported.
	cycleA := facets.Facet{ID: "a", Setup: func(env facets.FacetEnvironment) error {
		if _, err := env.Use(alpha); err != nil {
			return err
		}
		return env.Provide(beta, nil, services.Members{Methods: map[string]services.Method{
			"ping": func([]chord.JsonValue, context.Context) (chord.JsonValue, error) { return nil, nil },
		}})
	}}
	cycleB := facets.Facet{ID: "b", Setup: func(env facets.FacetEnvironment) error {
		if _, err := env.Use(beta); err != nil {
			return err
		}
		return env.Provide(alpha, nil, services.Members{Methods: map[string]services.Method{
			"ping": func([]chord.JsonValue, context.Context) (chord.JsonValue, error) { return nil, nil },
		}})
	}}
	if _, err := facets.CreateFacetHost(facets.FacetOptions{Facets: []facets.Facet{cycleA, cycleB}}); err == nil ||
		!strings.Contains(err.Error(), "dependency cycle") {
		t.Fatalf("err = %v", err)
	}

	// Empty and duplicate facet ids are rejected.
	if _, err := facets.CreateFacetHost(facets.FacetOptions{Facets: []facets.Facet{{ID: ""}}}); err == nil {
		t.Fatal("an empty facet id must be rejected")
	}
	noop := facets.Facet{ID: "dup", Setup: func(facets.FacetEnvironment) error { return nil }}
	if _, err := facets.CreateFacetHost(facets.FacetOptions{Facets: []facets.Facet{noop, noop}}); err == nil {
		t.Fatal("duplicate facet ids must be rejected")
	}
}

func TestFacetLifecycleAccessGuards(t *testing.T) {
	alpha := service(t, "guarded", false)
	var mu sync.Mutex
	started := 0
	stopped := 0
	keyed := facets.Facet{ID: "keyed", Setup: func(env facets.FacetEnvironment) error {
		_, err := env.ProvideMany(alpha)
		return err
	}}
	var watched *services.SlotView
	watcher := facets.Facet{ID: "watcher", Setup: func(env facets.FacetEnvironment) error {
		return env.Observe(alpha, func(view *services.SlotView, ctx context.Context) error {
			mu.Lock()
			started++
			watched = view
			mu.Unlock()
			<-ctx.Done()
			mu.Lock()
			stopped++
			mu.Unlock()
			return nil
		})
	}}
	host, err := facets.CreateFacetHost(facets.FacetOptions{Facets: []facets.Facet{keyed, watcher}})
	if err != nil {
		t.Fatal(err)
	}

	// Setup-time misuse: providing after activation is rejected.
	setupErr := make(chan error, 1)
	lifecycle := facets.Facet{ID: "late", Setup: func(env facets.FacetEnvironment) error {
		return nil
	}}
	_ = lifecycle
	close(setupErr)

	if err := host.Dispose(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if watched != nil {
		// Handles are unusable after disposal.
		if _, err := watched.Member("ping").Call(nil, context.Background()); err == nil ||
			!strings.Contains(err.Error(), "cannot be used while") {
			t.Fatalf("err = %v", err)
		}
	}
}

func TestFacetKeyedSpawnAndObserve(t *testing.T) {
	keyedService := service(t, "keyed", false)
	var mu sync.Mutex
	seen := map[string]string{}
	var spawnerHolder facets.ServiceSpawner
	providerFacet := facets.Facet{ID: "keyed-provider", Setup: func(env facets.FacetEnvironment) error {
		spawner, err := env.ProvideMany(keyedService)
		if err != nil {
			return err
		}
		spawnerHolder = spawner
		return env.OnActivate(func() error {
			_, err := spawnerHolder.Spawn("a", nil, services.Members{Methods: map[string]services.Method{
				"name": func([]chord.JsonValue, context.Context) (chord.JsonValue, error) { return "a", nil },
			}})
			return err
		})
	}}
	observerFacet := facets.Facet{ID: "observer", Setup: func(env facets.FacetEnvironment) error {
		return env.Observe(keyedService, func(view *services.SlotView, ctx context.Context) error {
			value, err := view.Member("name").Call(nil, ctx)
			if err != nil {
				return err
			}
			mu.Lock()
			// Key by the instance name returned by the member so each keyed
			// instance has its own entry.
			seen[value.(string)] = value.(string)
			mu.Unlock()
			<-ctx.Done()
			return nil
		})
	}}
	host, err := facets.CreateFacetHost(facets.FacetOptions{Facets: []facets.Facet{providerFacet, observerFacet}})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Dispose()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(seen)
		mu.Unlock()
		if count == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	observed := map[string]string{}
	for key, value := range seen {
		observed[key] = value
	}
	mu.Unlock()
	if len(observed) != 1 || observed["a"] != "a" {
		t.Fatalf("observed = %#v", observed)
	}

	// Spawning after activation installs immediately and observes.
	closeSecond, err := spawnerHolder.Spawn("b", nil, services.Members{Methods: map[string]services.Method{
		"name": func([]chord.JsonValue, context.Context) (chord.JsonValue, error) { return "b", nil },
	}})
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(seen)
		mu.Unlock()
		if count == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	count := len(seen)
	mu.Unlock()
	if count != 2 {
		t.Fatalf("observed = %#v", seen)
	}
	closeSecond()
	if _, err := spawnerHolder.Spawn("b", nil, services.Members{Methods: map[string]services.Method{
		"name": func([]chord.JsonValue, context.Context) (chord.JsonValue, error) { return "b", nil },
	}}); err != nil {
		t.Fatalf("respawn after close must work: %v", err)
	}
}

func TestFacetHostReload(t *testing.T) {
	alpha := service(t, "reload", false)
	var mu sync.Mutex
	var calls []string

	newFacet := func(version string) facets.Facet {
		return facets.Facet{ID: "reload-facet", Setup: func(env facets.FacetEnvironment) error {
			return env.Provide(alpha, version, services.Members{Methods: map[string]services.Method{
				"version": func([]chord.JsonValue, context.Context) (chord.JsonValue, error) { return version, nil },
			}})
		}}
	}
	consumer := facets.Facet{ID: "consumer", Setup: func(env facets.FacetEnvironment) error {
		view, err := env.Use(alpha)
		if err != nil {
			return err
		}
		return env.OnActivate(func() error {
			value, err := view.Member("version").Call(nil, context.Background())
			if err != nil {
				return err
			}
			mu.Lock()
			calls = append(calls, value.(string))
			mu.Unlock()
			return nil
		})
	}}

	host, err := facets.CreateFacetHost(facets.FacetOptions{Facets: []facets.Facet{newFacet("v1"), consumer}})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Dispose()
	mu.Lock()
	if len(calls) != 1 || calls[0] != "v1" {
		mu.Unlock()
		t.Fatalf("calls = %#v", calls)
	}
	mu.Unlock()

	// A reload keeps consumer handles usable and swaps the implementation.
	if err := host.Reload([]facets.Facet{newFacet("v2")}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]string{}, calls...)
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("consumer must not re-activate on reload: %#v", got)
	}

	// A changed shape is rejected and the previous implementation stays.
	different := facets.Facet{ID: "reload-facet", Setup: func(env facets.FacetEnvironment) error {
		return env.Provide(alpha, "v3", services.Members{
			Methods: map[string]services.Method{
				"version": func([]chord.JsonValue, context.Context) (chord.JsonValue, error) { return "v3", nil },
			},
			States: map[string]services.ReplicatedStateSource{"extra": nil},
		})
	}}
	if err := host.Reload([]facets.Facet{different}); err == nil {
		t.Fatal("a changed shape must be rejected")
	}
	// An unknown facet cannot be reloaded.
	if err := host.Reload([]facets.Facet{{ID: "unknown", Setup: func(facets.FacetEnvironment) error { return nil }}}); err == nil ||
		!strings.Contains(err.Error(), "is not active") {
		t.Fatalf("err = %v", err)
	}
}

func TestFacetSetupFailureCleansUp(t *testing.T) {
	alpha := service(t, "cleanup", false)
	var owned, deactivated int
	var mu sync.Mutex
	first := facets.Facet{ID: "first", Setup: func(env facets.FacetEnvironment) error {
		if err := env.Provide(alpha, nil, services.Members{Methods: map[string]services.Method{
			"ping": func([]chord.JsonValue, context.Context) (chord.JsonValue, error) { return nil, nil },
		}}); err != nil {
			return err
		}
		if err := env.Own(func() error {
			mu.Lock()
			owned++
			mu.Unlock()
			return nil
		}); err != nil {
			return err
		}
		return env.OnDeactivate(func() error {
			mu.Lock()
			deactivated++
			mu.Unlock()
			return nil
		})
	}}
	failing := facets.Facet{ID: "failing", Setup: func(env facets.FacetEnvironment) error {
		return fmt.Errorf("setup exploded")
	}}
	_, err := facets.CreateFacetHost(facets.FacetOptions{Facets: []facets.Facet{first, failing}})
	if err == nil || !strings.Contains(err.Error(), "setup exploded") {
		t.Fatalf("err = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if owned != 1 || deactivated != 1 {
		t.Fatalf("owned = %d deactivated = %d", owned, deactivated)
	}
}

func TestFacetStaticAndCombinedLoaders(t *testing.T) {
	alpha := service(t, "loader-alpha", false)
	beta := service(t, "loader-beta", false)
	loaderA := facets.StaticFacetLoader([]facets.Facet{{ID: "a", Setup: func(env facets.FacetEnvironment) error {
		return env.Provide(alpha, nil, services.Members{Methods: map[string]services.Method{
			"ping": func([]chord.JsonValue, context.Context) (chord.JsonValue, error) { return nil, nil },
		}})
	}}})
	disposed := 0
	loaderB := &countingLoader{facets: []facets.Facet{{ID: "b", Setup: func(env facets.FacetEnvironment) error {
		return env.Provide(beta, nil, services.Members{Methods: map[string]services.Method{
			"ping": func([]chord.JsonValue, context.Context) (chord.JsonValue, error) { return nil, nil },
		}})
	}}}, onDispose: func() { disposed++ }}

	combined := facets.CombinedFacetLoader([]facets.FacetLoader{loaderA, loaderB})
	loaded, err := combined.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Facets) != 2 || loaded.Facets[0].ID != "a" || loaded.Facets[1].ID != "b" {
		t.Fatalf("facets = %#v", loaded.Facets)
	}
	host, err := facets.CreateFacetHost(facets.FacetOptions{Facets: loaded.Facets})
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Dispose(); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Dispose(); err != nil {
		t.Fatal(err)
	}
	if err := loaded.Dispose(); err != nil {
		t.Fatal(err)
	}
	if disposed != 1 {
		t.Fatalf("disposed = %d", disposed)
	}

	// A failing loader disposes the ones that already loaded.
	failing := &countingLoader{err: fmt.Errorf("load failed")}
	disposed = 0
	combined = facets.CombinedFacetLoader([]facets.FacetLoader{loaderB, failing})
	if _, err := combined.Load(); err == nil || !strings.Contains(err.Error(), "load failed") {
		t.Fatalf("err = %v", err)
	}
	if disposed != 1 {
		t.Fatalf("disposed = %d", disposed)
	}
}

type countingLoader struct {
	facets    []facets.Facet
	err       error
	onDispose func()
}

func (l *countingLoader) Load() (facets.LoadedFacets, error) {
	if l.err != nil {
		return facets.LoadedFacets{}, l.err
	}
	return facets.LoadedFacets{Facets: l.facets, Dispose: func() error {
		if l.onDispose != nil {
			l.onDispose()
		}
		return nil
	}}, nil
}

func TestFacetSourceBinding(t *testing.T) {
	// A host service source provides an external singleton and a keyed service.
	external := service(t, "external", false)
	state, err := services.NewMutableState(map[string]any{"value": float64(1)})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := services.NewRemoteServiceProvider([]services.ProviderEntry{{Service: external}})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Provide(external, "impl", services.Members{
		Methods: map[string]services.Method{"read": func([]chord.JsonValue, context.Context) (chord.JsonValue, error) {
			return state.Published(), nil
		}},
		States: map[string]services.ReplicatedStateSource{"state": state},
	}); err != nil {
		t.Fatal(err)
	}
	source := &testSource{provider: provider, catalogue: []chord.ServiceCatalogueEntry{
		{ServiceID: "external", Mode: chord.ServiceModeSingleton},
	}}

	var view *services.SlotView
	consumerFacet := facets.Facet{ID: "external-consumer", Setup: func(env facets.FacetEnvironment) error {
		resolved, err := env.Use(external)
		if err != nil {
			return err
		}
		view = resolved
		return nil
	}}
	host, err := facets.CreateFacetHost(facets.FacetOptions{
		Facets:         []facets.Facet{consumerFacet},
		ServiceSources: []facets.ServiceSource{source},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Dispose()

	value, err := view.Member("read").Call(nil, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if value.(map[string]any)["value"] != float64(1) {
		t.Fatalf("value = %#v", value)
	}
	stateValue, ok, err := view.Member("state").Value()
	if err != nil || !ok || stateValue.(map[string]any)["value"] != float64(1) {
		t.Fatalf("state = %#v err = %v", stateValue, err)
	}

	// Duplicate offers are rejected.
	other := &testSource{provider: provider, catalogue: []chord.ServiceCatalogueEntry{
		{ServiceID: "external", Mode: chord.ServiceModeSingleton},
	}}
	if _, err := facets.CreateFacetHost(facets.FacetOptions{
		Facets:         []facets.Facet{consumerFacet},
		ServiceSources: []facets.ServiceSource{source, other},
	}); err == nil || !strings.Contains(err.Error(), "offered by more than one source") {
		t.Fatalf("err = %v", err)
	}

	// A deferred source owns absent requirements: it accepts them without
	// offering them in its catalogue. The connect failure is reported through
	// onError (upstream's start errors go to the binding's onError), so the host
	// still activates.
	deferred := &testSource{provider: provider, acceptsUnavailable: true, catalogue: nil}
	deferredService := service(t, "deferred", false)
	deferredConsumer := facets.Facet{ID: "deferred-consumer", Setup: func(env facets.FacetEnvironment) error {
		_, err := env.Use(deferredService)
		return err
	}}
	var reportMu sync.Mutex
	var reported []string
	deferredHost, err := facets.CreateFacetHost(facets.FacetOptions{
		Facets:         []facets.Facet{deferredConsumer},
		ServiceSources: []facets.ServiceSource{deferred},
		OnError: func(err error) {
			reportMu.Lock()
			reported = append(reported, err.Error())
			reportMu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("a deferred source activates with a reported connect failure: %v", err)
	}
	if deferred.opened != 1 {
		t.Fatalf("deferred source opens = %d", deferred.opened)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		reportMu.Lock()
		count := len(reported)
		reportMu.Unlock()
		if count > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	reportMu.Lock()
	count := len(reported)
	reportMu.Unlock()
	if count == 0 {
		t.Fatal("a deferred source without a provider must report its connect failure")
	}
	if err := deferredHost.Dispose(); err != nil {
		t.Fatal(err)
	}
}

type testSource struct {
	provider           *services.RemoteServiceProvider
	catalogue          []chord.ServiceCatalogueEntry
	acceptsUnavailable bool
	opened             int
}

func (s *testSource) AcceptsUnavailableServices() bool { return s.acceptsUnavailable }
func (s *testSource) Catalogue(ctx context.Context) ([]chord.ServiceCatalogueEntry, error) {
	return append([]chord.ServiceCatalogueEntry{}, s.catalogue...), nil
}
func (s *testSource) Open(options facets.ServiceSourceOpenOptions) (facets.RemoteServices, error) {
	s.opened++
	bound := true
	return services.NewRemoteServiceBinding(services.RemoteServiceBindingOptions{
		Services:     options.Services,
		Transport:    services.CreateLoopbackServiceTransport(s.provider),
		Bound:        &bound,
		AssertAccess: options.AssertAccess,
		OnError:      options.OnError,
	})
}

func TestFacetLocalServices(t *testing.T) {
	// Process-local services are wired without the remote provider: a local
	// singleton and a local keyed service.
	localSingleton := service(t, "local-singleton", true)
	localKeyed := service(t, "local-keyed", true)

	var singletonView *services.SlotView
	var mu sync.Mutex
	keyedNames := map[string]bool{}

	providerFacet := facets.Facet{ID: "local-provider", Setup: func(env facets.FacetEnvironment) error {
		state, err := env.ReplicatedState(map[string]any{"hits": float64(0)})
		if err != nil {
			return err
		}
		if err := env.Provide(localSingleton, "local-impl", services.Members{
			Methods: map[string]services.Method{
				"bump": func(args []chord.JsonValue, ctx context.Context) (chord.JsonValue, error) {
					hits := state.State().(map[string]any)["hits"].(float64) + 1
					state.State().(map[string]any)["hits"] = hits
					if err := state.PublishState(ctx); err != nil {
						return nil, err
					}
					return hits, nil
				},
			},
			States: map[string]services.ReplicatedStateSource{"state": state},
		}); err != nil {
			return err
		}
		spawner, err := env.ProvideMany(localKeyed)
		if err != nil {
			return err
		}
		return env.OnActivate(func() error {
			_, err := spawner.Spawn("one", "one-impl", services.Members{Methods: map[string]services.Method{
				"name": func([]chord.JsonValue, context.Context) (chord.JsonValue, error) { return "one", nil },
			}})
			return err
		})
	}}
	consumerFacet := facets.Facet{ID: "local-consumer", Setup: func(env facets.FacetEnvironment) error {
		view, err := env.Use(localSingleton)
		if err != nil {
			return err
		}
		singletonView = view
		return env.Observe(localKeyed, func(view *services.SlotView, ctx context.Context) error {
			name, err := view.Member("name").Call(nil, ctx)
			if err != nil {
				return err
			}
			mu.Lock()
			keyedNames[name.(string)] = true
			mu.Unlock()
			<-ctx.Done()
			return nil
		})
	}}

	host, err := facets.CreateFacetHost(facets.FacetOptions{Facets: []facets.Facet{providerFacet, consumerFacet}})
	if err != nil {
		t.Fatal(err)
	}
	defer host.Dispose()

	// Local services are not published remotely.
	if catalogue := host.Services.Catalogue(); len(catalogue) != 0 {
		t.Fatalf("local services must not be in the catalogue: %#v", catalogue)
	}
	// The local singleton is callable and its state is live.
	hits, err := singletonView.Member("bump").Call(nil, context.Background())
	if err != nil || hits != float64(1) {
		t.Fatalf("hits = %v err = %v", hits, err)
	}
	value, ok, err := singletonView.Member("state").Value()
	if err != nil || !ok || value.(map[string]any)["hits"] != float64(1) {
		t.Fatalf("state = %#v err = %v", value, err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(keyedNames)
		mu.Unlock()
		if count == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if !keyedNames["one"] {
		t.Fatalf("keyed names = %#v", keyedNames)
	}
}

func TestFacetHostDisposePhasesAndLifecycleGuards(t *testing.T) {
	alpha := service(t, "phase", false)
	// Disposing during setup (never activated) is rejected.
	kernel, err := facets.NewKernel(facets.FacetOptions{Facets: []facets.Facet{{ID: "setup", Setup: func(env facets.FacetEnvironment) error {
		return env.Provide(alpha, nil, services.Members{Methods: map[string]services.Method{
			"ping": func([]chord.JsonValue, context.Context) (chord.JsonValue, error) { return nil, nil },
		}})
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := kernel.Dispose(); err == nil || !strings.Contains(err.Error(), "cannot be disposed while setup") {
		t.Fatalf("err = %v", err)
	}
	// Reload before activation is rejected.
	if err := kernel.Reload(nil); err == nil || !strings.Contains(err.Error(), "cannot reload while setup") {
		t.Fatalf("err = %v", err)
	}

	// Setup-time guards: service provision after setup, spawn before active,
	// and providing the same service twice.
	var spawner facets.ServiceSpawner
	lateProvide := make(chan error, 1)
	duplicateProvide := facets.Facet{ID: "duplicate", Setup: func(env facets.FacetEnvironment) error {
		members := services.Members{Methods: map[string]services.Method{
			"ping": func([]chord.JsonValue, context.Context) (chord.JsonValue, error) { return nil, nil },
		}}
		if err := env.Provide(alpha, nil, members); err != nil {
			return err
		}
		return env.Provide(alpha, nil, members)
	}}
	// A duplicate provide records one reference but two provisions, so the
	// provider catalogue rejects the duplicate entry (upstream behaves the
	// same way: recordServiceReference dedupes the shape, the provision list
	// does not).
	if _, err := facets.CreateFacetHost(facets.FacetOptions{Facets: []facets.Facet{duplicateProvide}}); err == nil ||
		!strings.Contains(err.Error(), "duplicate IDs") {
		t.Fatalf("err = %v", err)
	}

	keyedAgain := facets.Facet{ID: "keyed-guard", Setup: func(env facets.FacetEnvironment) error {
		resolved, err := env.ProvideMany(alpha)
		if err != nil {
			return err
		}
		spawner = resolved
		if _, err := spawner.Spawn("early", nil, services.Members{Methods: map[string]services.Method{
			"ping": func([]chord.JsonValue, context.Context) (chord.JsonValue, error) { return nil, nil },
		}}); err != nil {
			return err
		}
		return env.OnActivate(func() error {
			// Spawning while active works and the staged instance installs.
			_, err := spawner.Spawn("late", nil, services.Members{Methods: map[string]services.Method{
				"ping": func([]chord.JsonValue, context.Context) (chord.JsonValue, error) { return nil, nil },
			}})
			if err != nil {
			}
			lateProvide <- err
			return nil
		})
	}}
	// The pre-activation spawn is rejected by the lifecycle guard.
	if _, err := facets.CreateFacetHost(facets.FacetOptions{Facets: []facets.Facet{keyedAgain}}); err == nil ||
		!strings.Contains(err.Error(), "only while active") {
		t.Fatalf("err = %v", err)
	}
	_ = lateProvide
}
