package facets

import (
	"context"
	"fmt"
	"sync"

	"github.com/dat267/gpi/chord"
	"github.com/dat267/gpi/chord/services"
)

// Port of the LocalKeyedServiceRegistry, HostServiceSlots,
// StagedServiceSpawner, and provision machinery in src/facets/host.ts.

// localInstance is one live process-local keyed instance.
type localInstance struct {
	key        string
	generation int
	facade     *services.ServiceFacade
	target     *services.GuardedFacade
}

func (i *localInstance) EntryKey() string     { return i.key }
func (i *localInstance) EntryGeneration() int { return i.generation }

// EntryService hands the handler the entry itself, matching the consumer
// binding's directory convention; the handler reads target off the instance.
func (i *localInstance) EntryService() any { return i }
func (i *localInstance) Deactivate()       {}

// localKeyedRegistry owns process-local keyed instances.
type localKeyedRegistry struct {
	mu            sync.Mutex
	registrations map[string]*localKeyedRegistration
	disposed      bool
}

type localKeyedRegistration struct {
	generations map[string]int
	directory   *services.Directory[*localInstance]
}

func newLocalKeyedRegistry(serviceIDs []string, onError func(error)) (*localKeyedRegistry, error) {
	registry := &localKeyedRegistry{registrations: map[string]*localKeyedRegistration{}}
	seen := map[string]bool{}
	for _, serviceID := range serviceIDs {
		if seen[serviceID] {
			return nil, fmt.Errorf("Local keyed service registry has duplicate IDs")
		}
		seen[serviceID] = true
		registry.registrations[serviceID] = &localKeyedRegistration{
			generations: map[string]int{},
			directory:   services.NewDirectory[*localInstance](true, onError),
		}
	}
	return registry, nil
}

// Spawn adds one local instance and returns its close function.
func (r *localKeyedRegistry) Spawn(service chord.Service, key string, implementation any, members services.Members) (func(), error) {
	if err := r.assertActive(); err != nil {
		return nil, err
	}
	if key == "" {
		return nil, fmt.Errorf("Local service instance key must not be empty")
	}
	registration, err := r.registration(service.ID)
	if err != nil {
		return nil, err
	}
	if _, exists := registration.directory.Get(key); exists {
		return nil, fmt.Errorf("Local service %s already has a live instance with key %s", service.ID, key)
	}
	facade, err := services.NewMemberFacade(service.ID, nil, implementation, members, nil)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	registration.generations[key]++
	generation := registration.generations[key]
	r.mu.Unlock()
	instance := &localInstance{
		key:        key,
		generation: generation,
		facade:     facade,
		target:     services.NewGuardedFacade(facade, nil),
	}
	if err := registration.directory.Insert(instance); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() { registration.directory.Remove(instance) })
	}, nil
}

// Observe registers a handler for every live local instance (upstream
// KeyedServiceSource).
func (r *localKeyedRegistry) Observe(service chord.Service, handler func(target *services.GuardedFacade, ctx context.Context) error) (func(), error) {
	if err := r.assertActive(); err != nil {
		return nil, err
	}
	registration, err := r.registration(service.ID)
	if err != nil {
		return nil, err
	}
	return registration.directory.Observe(func(entry any, ctx context.Context) error {
		instance, ok := entry.(*localInstance)
		if !ok {
			return fmt.Errorf("Local keyed service directory entry is invalid")
		}
		return handler(instance.target, ctx)
	})
}

// Dispose releases every registration.
func (r *localKeyedRegistry) Dispose() error {
	r.mu.Lock()
	if r.disposed {
		r.mu.Unlock()
		return nil
	}
	r.disposed = true
	registrations := r.registrations
	r.registrations = map[string]*localKeyedRegistration{}
	r.mu.Unlock()
	for _, registration := range registrations {
		registration.directory.Dispose()
	}
	return nil
}

func (r *localKeyedRegistry) registration(serviceID string) (*localKeyedRegistration, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	registration, ok := r.registrations[serviceID]
	if !ok {
		return nil, fmt.Errorf("Local keyed service %s is not registered", serviceID)
	}
	return registration, nil
}

func (r *localKeyedRegistry) assertActive() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disposed {
		return fmt.Errorf("Local keyed service registry is disposed")
	}
	return nil
}

// hostServiceSlots owns the per-service slots facets resolve their handles
// through (upstream HostServiceSlots).
type hostServiceSlots struct {
	mu           sync.Mutex
	singletons   map[string]*services.Slot
	keyedSources map[string]keyedServiceSource
}

// keyedServiceSource supplies per-instance targets for one keyed service.
type keyedServiceSource interface {
	Observe(service chord.Service, handler func(target *services.GuardedFacade, ctx context.Context) error) (func(), error)
}

func newHostServiceSlots() *hostServiceSlots {
	return &hostServiceSlots{
		singletons:   map[string]*services.Slot{},
		keyedSources: map[string]keyedServiceSource{},
	}
}

func (h *hostServiceSlots) getSingleton(service chord.Service, assertAccess func() error) *services.SlotView {
	h.mu.Lock()
	slot, ok := h.singletons[service.ID]
	if !ok {
		slot = services.NewSlot(service.ID)
		h.singletons[service.ID] = slot
	}
	h.mu.Unlock()
	return slot.View(assertAccess)
}

func (h *hostServiceSlots) hasSingleton(serviceID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.singletons[serviceID]
	return ok
}

// observe binds one fresh slot per live instance and hands the guarded view to
// the handler, mirroring upstream's per-instance ServiceSlot.
func (h *hostServiceSlots) observe(
	service chord.Service,
	assertAccess func() error,
	handler func(service *services.SlotView, ctx context.Context) error,
) (func(), error) {
	h.mu.Lock()
	source, ok := h.keyedSources[service.ID]
	h.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("Service %s is disconnected", service.ID)
	}
	var stoppedMu sync.Mutex
	stopped := false
	stop, err := source.Observe(service, func(target *services.GuardedFacade, ctx context.Context) error {
		slot := services.NewSlot(service.ID)
		slot.Bind(target)
		return handler(slot.View(func() error {
			if err := assertAccess(); err != nil {
				return err
			}
			stoppedMu.Lock()
			isStopped := stopped
			stoppedMu.Unlock()
			if isStopped || ctx.Err() != nil {
				return fmt.Errorf("Keyed service %s observation is closed", service.ID)
			}
			return nil
		}), ctx)
	})
	if err != nil {
		return nil, err
	}
	return func() {
		stoppedMu.Lock()
		if stopped {
			stoppedMu.Unlock()
			return
		}
		stopped = true
		stoppedMu.Unlock()
		stop()
	}, nil
}

func (h *hostServiceSlots) bindSingleton(serviceID string, target services.SlotTarget) {
	h.mu.Lock()
	slot := h.singletons[serviceID]
	h.mu.Unlock()
	if slot != nil {
		slot.Bind(target)
	}
}

func (h *hostServiceSlots) bindKeyed(serviceID string, source keyedServiceSource) {
	h.mu.Lock()
	h.keyedSources[serviceID] = source
	h.mu.Unlock()
}

func (h *hostServiceSlots) dispose() {
	h.mu.Lock()
	slots := h.singletons
	h.singletons = map[string]*services.Slot{}
	h.keyedSources = map[string]keyedServiceSource{}
	h.mu.Unlock()
	for _, slot := range slots {
		slot.Unbind()
	}
}

// stagedSpawner is a facet's keyed spawner: instances spawned during activation
// are staged until the installer connects (upstream StagedServiceSpawner).
type stagedSpawner struct {
	mu        sync.Mutex
	lifecycle *facetLifecycle
	service   chord.Service
	validate  func(key string, implementation any, members services.Members) error
	instances []*stagedInstance
	installer func(key string, implementation any, members services.Members) (func(), error)
	connected bool
}

type stagedInstance struct {
	key            string
	implementation any
	members        services.Members
	release        func()
}

func newStagedSpawner(
	lifecycle *facetLifecycle,
	service chord.Service,
	validate func(key string, implementation any, members services.Members) error,
) *stagedSpawner {
	return &stagedSpawner{lifecycle: lifecycle, service: service, validate: validate}
}

// connect installs the real spawner and flushes staged instances.
func (s *stagedSpawner) connect(installer func(key string, implementation any, members services.Members) (func(), error)) error {
	s.mu.Lock()
	if s.connected {
		s.mu.Unlock()
		return fmt.Errorf("Facet service provider is already connected")
	}
	s.connected = true
	s.installer = installer
	staged := s.instances
	s.mu.Unlock()
	for _, instance := range staged {
		release, err := installer(instance.key, instance.implementation, instance.members)
		if err != nil {
			return err
		}
		s.mu.Lock()
		instance.release = release
		s.mu.Unlock()
	}
	return nil
}

// Spawn creates one instance (upstream spawn): allowed while active, staged
// until the installer is connected.
func (s *stagedSpawner) Spawn(key string, implementation any, members services.Members) (func(), error) {
	if err := s.lifecycle.assertActive("spawn service instances"); err != nil {
		return nil, err
	}
	if err := s.validate(key, implementation, members); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.connected {
		// Already-connected spawners install immediately.
		installer := s.installer
		s.mu.Unlock()
		release, err := installer(key, implementation, members)
		if err != nil {
			return nil, err
		}
		instance := &stagedInstance{key: key, implementation: implementation, members: members, release: release}
		s.mu.Lock()
		s.instances = append(s.instances, instance)
		s.mu.Unlock()
		return s.closeFunc(instance, release), nil
	}
	for _, instance := range s.instances {
		if instance.key == key {
			s.mu.Unlock()
			return nil, fmt.Errorf("Facet service already has a live instance with key %s", key)
		}
	}
	instance := &stagedInstance{key: key, implementation: implementation, members: members}
	s.instances = append(s.instances, instance)
	s.mu.Unlock()
	closeFunc := func() {
		s.mu.Lock()
		found := false
		kept := s.instances[:0]
		for _, candidate := range s.instances {
			if candidate == instance {
				found = true
				continue
			}
			kept = append(kept, candidate)
		}
		s.instances = kept
		release := instance.release
		s.mu.Unlock()
		if !found {
			return
		}
		if release != nil {
			release()
		}
	}
	// The lifecycle owns the close function so disposal retires staged
	// instances (upstream calls lifecycle.own(close)).
	if err := s.lifecycle.own(func() error {
		closeFunc()
		return nil
	}); err != nil {
		return nil, err
	}
	return closeFunc, nil
}

func (s *stagedSpawner) closeFunc(instance *stagedInstance, release func()) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			kept := s.instances[:0]
			for _, candidate := range s.instances {
				if candidate != instance {
					kept = append(kept, candidate)
				}
			}
			s.instances = kept
			s.mu.Unlock()
			if release != nil {
				release()
			}
		})
	}
}

// provision is one facet provision (upstream FacetProvision).
type provision struct {
	kind           string // "singleton" | "keyed"
	service        chord.Service
	implementation any
	members        services.Members

	install             func(provider *services.RemoteServiceProvider) error
	validateReplacement func(provider *services.RemoteServiceProvider) error
	replace             func(provider *services.RemoteServiceProvider) error

	connectLocal  func(registry *localKeyedRegistry) error
	connectRemote func(provider *services.RemoteServiceProvider) error

	// facade caches the in-process facade of a local singleton.
	facade *services.ServiceFacade
}

// facetRuntime is the per-facet record (upstream FacetRuntime).
type facetRuntime struct {
	facetID    string
	requires   []serviceReference
	provides   []serviceReference
	lifecycle  *facetLifecycle
	provisions []*provision
	views      map[string]*services.SlotView
}

// serviceReference is one recorded requirement or provision.
type serviceReference struct {
	serviceID string
	service   chord.Service
	mode      chord.ServiceMode
}

func recordServiceReference(target []serviceReference, service chord.Service, mode chord.ServiceMode) []serviceReference {
	for _, reference := range target {
		if reference.serviceID == service.ID && reference.mode == mode {
			return target
		}
	}
	return append(target, serviceReference{serviceID: service.ID, service: service, mode: mode})
}
