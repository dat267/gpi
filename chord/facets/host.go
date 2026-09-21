package facets

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/dat267/pier/chord"
	"github.com/dat267/pier/chord/services"
)

// Port of src/facets/host.ts (FacetKernel) and the api.ts composition helpers.

// Generation phases (upstream GenerationPhase).
const (
	phaseSetup      = "setup"
	phaseAssembling = "assembling"
	phaseConnecting = "connecting"
	phaseActivating = "activating"
	phaseActive     = "active"
	phaseReloading  = "reloading"
	phaseDisposing  = "disposing"
	phaseDead       = "dead"
)

// Kernel is the private lifecycle and dependency kernel behind the atomic host
// entry point (upstream FacetKernel).
type Kernel struct {
	mu sync.Mutex

	initialFacets   []Facet
	serviceSources  []ServiceSource
	reportError     func(error)
	facets          map[string]*facetRuntime
	order           []string
	slots           *hostServiceSlots
	sourceBindings  map[ServiceSource]RemoteServices
	activationOrder []string
	provider        *services.RemoteServiceProvider
	internal        *services.RemoteServiceBinding
	localKeyed      *localKeyedRegistry
	phase           string
}

// NewKernel builds a kernel for one generation of facets.
func NewKernel(options FacetOptions) (*Kernel, error) {
	seen := map[string]bool{}
	for _, facet := range options.Facets {
		if facet.ID == "" {
			return nil, fmt.Errorf("Facet ID must not be empty")
		}
		if seen[facet.ID] {
			return nil, fmt.Errorf("Facet IDs must be unique within a generation")
		}
		seen[facet.ID] = true
	}
	reportError := options.OnError
	if reportError == nil {
		reportError = func(error) {}
	}
	return &Kernel{
		initialFacets:  options.Facets,
		serviceSources: options.ServiceSources,
		reportError:    reportError,
		facets:         map[string]*facetRuntime{},
		slots:          newHostServiceSlots(),
		sourceBindings: map[ServiceSource]RemoteServices{},
		phase:          phaseSetup,
	}, nil
}

// Provider is the assembled service provider.
func (k *Kernel) Provider() (*services.RemoteServiceProvider, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.provider == nil {
		return nil, fmt.Errorf("Facet service provider is not assembled")
	}
	return k.provider, nil
}

func (k *Kernel) createFacetRuntime(facetID string) *facetRuntime {
	return &facetRuntime{
		facetID:   facetID,
		lifecycle: newFacetLifecycle(facetID),
		views:     map[string]*services.SlotView{},
	}
}

func (k *Kernel) setupFacet(facet Facet, record *facetRuntime) error {
	if err := facet.Setup(k.environment(record)); err != nil {
		return err
	}
	return record.lifecycle.prepared()
}

// Activate sets up every facet, assembles the providers, connects the bindings,
// and activates in dependency order.
func (k *Kernel) Activate() error {
	k.mu.Lock()
	activate := k.activateInner
	k.mu.Unlock()
	if err := activate(); err != nil {
		return err
	}
	return nil
}

func (k *Kernel) activateInner() error {
	records := []*facetRuntime{}
	run := func() error {
		for _, facet := range k.initialFacets {
			record := k.createFacetRuntime(facet.ID)
			k.mu.Lock()
			k.facets[facet.ID] = record
			k.order = append(k.order, facet.ID)
			k.mu.Unlock()
			if err := k.setupFacet(facet, record); err != nil {
				return err
			}
			records = append(records, record)
		}

		k.setPhase(phaseAssembling)
		externalServices, err := k.resolveExternalServices(records)
		if err != nil {
			return err
		}
		order, err := validateFacets(records, externalServices)
		if err != nil {
			return err
		}
		k.mu.Lock()
		k.activationOrder = order
		k.mu.Unlock()
		if err := k.assembleProviders(); err != nil {
			return err
		}
		if err := k.bindServices(externalServices); err != nil {
			return err
		}

		k.setPhase(phaseConnecting)
		if err := k.readyBindings(); err != nil {
			return err
		}

		k.setPhase(phaseActivating)
		for _, id := range order {
			record, err := k.record(id)
			if err != nil {
				return err
			}
			if err := record.lifecycle.activate(); err != nil {
				return err
			}
		}
		k.setPhase(phaseActive)
		return nil
	}
	if err := run(); err != nil {
		cleanupErrors := k.terminate(nil)
		if len(cleanupErrors) > 0 {
			return &services.AggregateError{
				Message: "Facet generation startup and cleanup failed",
				Errors:  append([]error{err}, cleanupErrors...),
			}
		}
		return err
	}
	return nil
}

// Reload replaces facets with matching IDs without disconnecting consumer
// service handles (upstream reload).
func (k *Kernel) Reload(facets []Facet) error {
	k.mu.Lock()
	if k.phase != phaseActive {
		phase := k.phase
		k.mu.Unlock()
		return fmt.Errorf("Facet host cannot reload while %s", phase)
	}
	seen := map[string]bool{}
	for _, facet := range facets {
		if facet.ID == "" {
			k.mu.Unlock()
			return fmt.Errorf("Facet ID must not be empty")
		}
		if seen[facet.ID] {
			k.mu.Unlock()
			return fmt.Errorf("Reloaded facet IDs must be unique")
		}
		seen[facet.ID] = true
		if _, ok := k.facets[facet.ID]; !ok {
			k.mu.Unlock()
			return fmt.Errorf("Facet %s is not active", facet.ID)
		}
	}
	k.phase = phaseReloading
	k.mu.Unlock()

	staged := []*facetRuntime{}
	candidates := []*facetRuntime{}
	for _, facet := range facets {
		record := k.createFacetRuntime(facet.ID)
		staged = append(staged, record)
		if err := k.setupFacet(facet, record); err != nil {
			cleanupErrors := disposeFacetRecords(reverseRecords(staged))
			if len(cleanupErrors) > 0 {
				abortErrors := k.abort(nil)
				return &services.AggregateError{
					Message: "Facet reload setup and cleanup failed",
					Errors:  append(append([]error{err}, cleanupErrors...), abortErrors...),
				}
			}
			k.setPhase(phaseActive)
			return err
		}
		previous, _ := k.record(facet.ID)
		if !sameFacetShape(previous, record) {
			err := fmt.Errorf("Reloaded facet %s must preserve its service requirements and provisions", facet.ID)
			cleanupErrors := disposeFacetRecords(reverseRecords(staged))
			if len(cleanupErrors) > 0 {
				abortErrors := k.abort(nil)
				return &services.AggregateError{
					Message: "Facet reload setup and cleanup failed",
					Errors:  append(append([]error{err}, cleanupErrors...), abortErrors...),
				}
			}
			k.setPhase(phaseActive)
			return err
		}
		if err := k.validateReplacementProvisions(record.provisions); err != nil {
			cleanupErrors := disposeFacetRecords(reverseRecords(staged))
			if len(cleanupErrors) > 0 {
				abortErrors := k.abort(nil)
				return &services.AggregateError{
					Message: "Facet reload setup and cleanup failed",
					Errors:  append(append([]error{err}, cleanupErrors...), abortErrors...),
				}
			}
			k.setPhase(phaseActive)
			return err
		}
		candidates = append(candidates, record)
	}

	replacements := map[string]*facetRuntime{}
	for _, record := range candidates {
		replacements[record.facetID] = record
	}
	candidateOrder := []*facetRuntime{}
	k.mu.Lock()
	for _, id := range k.activationOrder {
		if candidate, ok := replacements[id]; ok {
			candidateOrder = append(candidateOrder, candidate)
		}
	}
	k.mu.Unlock()

	activatePhase := func() error {
		for _, candidate := range candidateOrder {
			if err := candidate.lifecycle.activate(); err != nil {
				return err
			}
		}
		for _, candidate := range candidateOrder {
			if err := k.validateReplacementProvisions(candidate.provisions); err != nil {
				return err
			}
		}
		return nil
	}
	if err := activatePhase(); err != nil {
		cleanupErrors := disposeFacetRecords(reverseRecords(candidateOrder))
		if len(cleanupErrors) > 0 {
			abortErrors := k.abort(nil)
			return &services.AggregateError{
				Message: "Facet reload activation and cleanup failed",
				Errors:  append(append([]error{err}, cleanupErrors...), abortErrors...),
			}
		}
		k.setPhase(phaseActive)
		return err
	}

	previous := make([]*facetRuntime, 0, len(candidateOrder))
	for _, candidate := range candidateOrder {
		record, _ := k.record(candidate.facetID)
		previous = append(previous, record)
	}
	k.mu.Lock()
	for _, candidate := range candidateOrder {
		k.facets[candidate.facetID] = candidate
	}
	k.mu.Unlock()

	cutover := func() error {
		for _, candidate := range candidateOrder {
			for _, provision := range candidate.provisions {
				if provision.kind != "singleton" {
					continue
				}
				if provision.service.Local {
					facade, err := localFacadeFor(provision)
					if err != nil {
						return err
					}
					k.slots.bindSingleton(provision.service.ID, facade)
					continue
				}
				provider, err := k.Provider()
				if err != nil {
					return err
				}
				if err := provision.replace(provider); err != nil {
					return err
				}
			}
		}
		retirementErrors := disposeFacetRecords(reverseRecords(previous))
		if len(retirementErrors) == 1 {
			return retirementErrors[0]
		}
		if len(retirementErrors) > 1 {
			return &services.AggregateError{Message: "Failed to retire replaced facets", Errors: retirementErrors}
		}
		for _, candidate := range candidateOrder {
			for _, provision := range candidate.provisions {
				if provision.kind != "keyed" {
					continue
				}
				if provision.service.Local {
					if err := provision.connectLocal(k.localKeyedRegistry()); err != nil {
						return err
					}
					continue
				}
				provider, err := k.Provider()
				if err != nil {
					return err
				}
				if err := provision.connectRemote(provider); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := cutover(); err != nil {
		abortErrors := k.abort(previous)
		return &services.AggregateError{
			Message: "Facet reload failed after cutover",
			Errors:  append([]error{err}, abortErrors...),
		}
	}
	k.setPhase(phaseActive)
	return nil
}

// Dispose tears the generation down.
func (k *Kernel) Dispose() error {
	k.mu.Lock()
	if k.phase == phaseDead {
		k.mu.Unlock()
		return nil
	}
	if k.phase != phaseActive {
		phase := k.phase
		k.mu.Unlock()
		return fmt.Errorf("Facet host cannot be disposed while %s", phase)
	}
	k.mu.Unlock()
	errors := k.terminate(nil)
	return collectErrors(errors, "Failed to dispose facet generation")
}

func (k *Kernel) validateReplacementProvisions(provisions []*provision) error {
	for _, provision := range provisions {
		if provision.kind != "singleton" || provision.service.Local {
			continue
		}
		provider, err := k.Provider()
		if err != nil {
			return err
		}
		if err := provision.validateReplacement(provider); err != nil {
			return err
		}
	}
	return nil
}

// environment builds the setup-time environment for one facet.
func (k *Kernel) environment(runtime *facetRuntime) FacetEnvironment {
	return &facetEnvironment{kernel: k, runtime: runtime}
}

type facetEnvironment struct {
	kernel  *Kernel
	runtime *facetRuntime
}

func (e *facetEnvironment) Provide(service chord.Service, implementation any, members services.Members) error {
	lifecycle := e.runtime.lifecycle
	if err := lifecycle.assertSettingUp("provide services"); err != nil {
		return err
	}
	e.runtime.provides = recordServiceReference(e.runtime.provides, service, chord.ServiceModeSingleton)
	provision := &provision{
		kind:           "singleton",
		service:        service,
		implementation: implementation,
		members:        members,
	}
	provision.install = func(provider *services.RemoteServiceProvider) error {
		return provider.Provide(service, implementation, members)
	}
	provision.validateReplacement = func(provider *services.RemoteServiceProvider) error {
		return provider.ValidateReplacement(service, members)
	}
	provision.replace = func(provider *services.RemoteServiceProvider) error {
		return provider.Replace(service, implementation, members)
	}
	e.runtime.provisions = append(e.runtime.provisions, provision)
	return nil
}

func (e *facetEnvironment) ProvideMany(service chord.Service) (ServiceSpawner, error) {
	lifecycle := e.runtime.lifecycle
	if err := lifecycle.assertSettingUp("provide service instances"); err != nil {
		return nil, err
	}
	e.runtime.provides = recordServiceReference(e.runtime.provides, service, chord.ServiceModeKeyed)
	spawner := newStagedSpawner(lifecycle, service,
		func(key string, implementation any, members services.Members) error {
			if key == "" {
				return fmt.Errorf("Facet service instance key must not be empty")
			}
			if len(members.Methods) == 0 && len(members.States) == 0 {
				return fmt.Errorf("Remote service %s has no members", service.ID)
			}
			if !service.Local {
				return services.ValidateRemoteServiceImplementation(service.ID, members)
			}
			return nil
		})
	provision := &provision{kind: "keyed", service: service}
	provision.connectLocal = func(registry *localKeyedRegistry) error {
		return spawner.connect(func(key string, implementation any, members services.Members) (func(), error) {
			return registry.Spawn(service, key, implementation, members)
		})
	}
	provision.connectRemote = func(provider *services.RemoteServiceProvider) error {
		return spawner.connect(func(key string, implementation any, members services.Members) (func(), error) {
			return provider.Spawn(service, key, implementation, members)
		})
	}
	e.runtime.provisions = append(e.runtime.provisions, provision)
	return spawner, nil
}

func (e *facetEnvironment) Use(service chord.Service) (*services.SlotView, error) {
	lifecycle := e.runtime.lifecycle
	if err := lifecycle.assertSettingUp("acquire services"); err != nil {
		return nil, err
	}
	e.runtime.requires = recordServiceReference(e.runtime.requires, service, chord.ServiceModeSingleton)
	view, ok := e.runtime.views[service.ID]
	if !ok {
		view = e.kernel.slots.getSingleton(service, lifecycle.assertServiceAccess)
		e.runtime.views[service.ID] = view
	}
	return view, nil
}

func (e *facetEnvironment) Observe(service chord.Service, handler func(service *services.SlotView, ctx context.Context) error) error {
	lifecycle := e.runtime.lifecycle
	if err := lifecycle.assertSettingUp("observe services"); err != nil {
		return err
	}
	e.runtime.requires = recordServiceReference(e.runtime.requires, service, chord.ServiceModeKeyed)
	return lifecycle.observe(func() (func(), error) {
		return e.kernel.slots.observe(service, lifecycle.assertServiceAccess, handler)
	})
}

func (e *facetEnvironment) ReplicatedState(initial chord.JsonValue) (*services.MutableState, error) {
	if err := e.runtime.lifecycle.assertRunning("create replicated state"); err != nil {
		return nil, err
	}
	return services.NewMutableState(initial)
}

func (e *facetEnvironment) Own(disposal func() error) error { return e.runtime.lifecycle.own(disposal) }

func (e *facetEnvironment) OnActivate(callback func() error) error {
	return e.runtime.lifecycle.onActivate(callback)
}

func (e *facetEnvironment) OnDeactivate(callback func() error) error {
	return e.runtime.lifecycle.own(callback)
}

// externalService is one service requirement satisfied by a source.
type externalService struct {
	service chord.Service
	mode    chord.ServiceMode
	source  ServiceSource
}

func (k *Kernel) resolveExternalServices(records []*facetRuntime) (map[string]externalService, error) {
	offered := map[string]struct {
		mode   chord.ServiceMode
		source ServiceSource
	}{}
	for _, source := range k.serviceSources {
		entries, err := source.Catalogue(context.Background())
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if _, exists := offered[entry.ServiceID]; exists {
				return nil, fmt.Errorf("Facet host service %s is offered by more than one source", entry.ServiceID)
			}
			offered[entry.ServiceID] = struct {
				mode   chord.ServiceMode
				source ServiceSource
			}{mode: entry.Mode, source: source}
		}
	}

	local := map[string]bool{}
	for _, record := range records {
		for _, provided := range record.provides {
			local[provided.serviceID] = true
		}
	}
	external := map[string]externalService{}
	for _, record := range records {
		for _, requirement := range record.requires {
			if local[requirement.serviceID] {
				continue
			}
			if _, exists := external[requirement.serviceID]; exists {
				continue
			}
			entry, ok := offered[requirement.serviceID]
			var source ServiceSource
			var mode chord.ServiceMode
			if ok {
				source = entry.source
				mode = entry.mode
			} else {
				var deferred []ServiceSource
				for _, candidate := range k.serviceSources {
					if candidate.AcceptsUnavailableServices() {
						deferred = append(deferred, candidate)
					}
				}
				if len(deferred) > 1 {
					return nil, fmt.Errorf("Facet host service %s has more than one deferred source", requirement.serviceID)
				}
				if len(deferred) == 1 {
					source = deferred[0]
					mode = requirement.mode
				}
			}
			if source != nil {
				external[requirement.serviceID] = externalService{service: requirement.service, mode: mode, source: source}
			}
		}
	}

	bySource := map[ServiceSource][]string{}
	for serviceID, entry := range external {
		bySource[entry.source] = append(bySource[entry.source], serviceID)
	}
	for source, serviceIDs := range bySource {
		var wanted []chord.Service
		for _, serviceID := range serviceIDs {
			wanted = append(wanted, chord.Service{ID: serviceID})
		}
		binding, err := source.Open(ServiceSourceOpenOptions{
			Services:     wanted,
			AssertAccess: k.assertServiceTargetAccess,
			OnError:      k.reportError,
		})
		if err != nil {
			return nil, err
		}
		k.mu.Lock()
		k.sourceBindings[source] = binding
		k.mu.Unlock()
	}
	return external, nil
}

func (k *Kernel) assembleProviders() error {
	provisions := k.allProvisions()
	var remote []services.ProviderEntry
	for _, provision := range provisions {
		if provision.service.Local {
			continue
		}
		mode := chord.ServiceModeSingleton
		if provision.kind == "keyed" {
			mode = chord.ServiceModeKeyed
		}
		remote = append(remote, services.ProviderEntry{Service: provision.service, Mode: mode})
	}
	provider, err := services.NewRemoteServiceProvider(remote)
	if err != nil {
		return err
	}
	var allowlist []chord.Service
	for _, provision := range provisions {
		if provision.service.Local {
			continue
		}
		allowlist = append(allowlist, provision.service)
	}
	bound := true
	internal, err := services.NewRemoteServiceBinding(services.RemoteServiceBindingOptions{
		Services:     allowlist,
		Transport:    services.CreateLoopbackServiceTransport(provider),
		Bound:        &bound,
		AssertAccess: k.assertServiceTargetAccess,
		OnError:      k.reportError,
	})
	if err != nil {
		return err
	}
	var localIDs []string
	for _, provision := range provisions {
		if provision.kind == "keyed" && provision.service.Local {
			localIDs = append(localIDs, provision.service.ID)
		}
	}
	localKeyed, err := newLocalKeyedRegistry(localIDs, k.reportError)
	if err != nil {
		return err
	}
	k.mu.Lock()
	k.provider = provider
	k.internal = internal
	k.localKeyed = localKeyed
	k.mu.Unlock()

	for _, provision := range provisions {
		switch {
		case provision.kind == "singleton" && !provision.service.Local:
			if err := provision.install(provider); err != nil {
				return err
			}
		case provision.kind == "keyed" && provision.service.Local:
			if err := provision.connectLocal(localKeyed); err != nil {
				return err
			}
		case provision.kind == "keyed":
			if err := provision.connectRemote(provider); err != nil {
				return err
			}
		}
	}
	return nil
}

func (k *Kernel) bindServices(externalServices map[string]externalService) error {
	for _, provision := range k.allProvisions() {
		if provision.kind == "singleton" {
			if !k.slots.hasSingleton(provision.service.ID) {
				continue
			}
			var target services.SlotTarget
			if provision.service.Local {
				facade, err := localFacadeFor(provision)
				if err != nil {
					return err
				}
				target = facade
			} else {
				facade, err := k.internalBinding().Use(provision.service)
				if err != nil {
					return err
				}
				target = facade
			}
			k.slots.bindSingleton(provision.service.ID, target)
			continue
		}
		var source keyedServiceSource
		if provision.service.Local {
			source = k.localKeyedRegistry()
		} else {
			source = k.internalBinding()
		}
		k.slots.bindKeyed(provision.service.ID, source)
	}
	for serviceID, entry := range externalServices {
		k.mu.Lock()
		binding, ok := k.sourceBindings[entry.source]
		k.mu.Unlock()
		if !ok {
			return fmt.Errorf("Service source for %s is not open", serviceID)
		}
		if entry.mode == chord.ServiceModeSingleton {
			facade, err := binding.Use(entry.service)
			if err != nil {
				return err
			}
			k.slots.bindSingleton(serviceID, facade)
			continue
		}
		k.slots.bindKeyed(serviceID, binding)
	}
	return nil
}

func (k *Kernel) readyBindings() error {
	k.mu.Lock()
	bindings := make([]RemoteServices, 0, len(k.sourceBindings)+1)
	for _, binding := range k.sourceBindings {
		bindings = append(bindings, binding)
	}
	if k.internal != nil {
		bindings = append(bindings, k.internal)
	}
	k.mu.Unlock()
	for _, binding := range bindings {
		if err := binding.Ready(context.Background()); err != nil {
			return err
		}
	}
	return nil
}

func (k *Kernel) disposeServiceBindings() []error {
	k.mu.Lock()
	bindings := make([]RemoteServices, 0, len(k.sourceBindings)+1)
	for _, binding := range k.sourceBindings {
		bindings = append(bindings, binding)
	}
	k.sourceBindings = map[ServiceSource]RemoteServices{}
	if k.internal != nil {
		bindings = append(bindings, k.internal)
		k.internal = nil
	}
	k.mu.Unlock()

	var errors []error
	for _, binding := range bindings {
		if err := binding.Dispose(context.Background()); err != nil {
			errors = append(errors, err)
		}
	}
	return errors
}

func (k *Kernel) assertServiceTargetAccess() error {
	k.mu.Lock()
	phase := k.phase
	k.mu.Unlock()
	switch phase {
	case phaseActivating, phaseActive, phaseReloading, phaseDisposing:
		return nil
	default:
		return fmt.Errorf("Facet service targets cannot be used during %s", phase)
	}
}

func (k *Kernel) abort(extraRecords []*facetRuntime) []error {
	k.mu.Lock()
	for _, record := range k.facets {
		record.lifecycle.revoke()
	}
	k.mu.Unlock()
	for _, record := range extraRecords {
		record.lifecycle.revoke()
	}
	return k.terminate(extraRecords)
}

func (k *Kernel) terminate(extraRecords []*facetRuntime) []error {
	k.setPhase(phaseDisposing)
	errors := k.disposeLifecycles()
	errors = append(errors, disposeFacetRecords(reverseRecords(extraRecords))...)

	k.mu.Lock()
	localKeyed := k.localKeyed
	k.localKeyed = nil
	k.mu.Unlock()
	if localKeyed != nil {
		if err := localKeyed.Dispose(); err != nil {
			errors = append(errors, err)
		}
	}
	errors = append(errors, k.disposeServiceBindings()...)

	k.slots.dispose()
	k.mu.Lock()
	provider := k.provider
	k.provider = nil
	k.mu.Unlock()
	if provider != nil {
		if err := provider.Dispose(); err != nil {
			errors = append(errors, err)
		}
	}
	k.setPhase(phaseDead)
	return errors
}

func (k *Kernel) disposeLifecycles() []error {
	k.mu.Lock()
	order := append([]string{}, k.activationOrder...)
	if len(order) == 0 {
		order = append([]string{}, k.order...)
	}
	records := make([]*facetRuntime, 0, len(order))
	for index := len(order) - 1; index >= 0; index-- {
		record, ok := k.facets[order[index]]
		if !ok {
			continue
		}
		delete(k.facets, order[index])
		records = append(records, record)
	}
	k.mu.Unlock()

	var errors []error
	for _, record := range records {
		if err := record.lifecycle.dispose(); err != nil {
			errors = append(errors, err)
		}
	}
	return errors
}

func (k *Kernel) allProvisions() []*provision {
	k.mu.Lock()
	defer k.mu.Unlock()
	var provisions []*provision
	for _, id := range k.order {
		record, ok := k.facets[id]
		if !ok {
			continue
		}
		provisions = append(provisions, record.provisions...)
	}
	return provisions
}

func (k *Kernel) record(id string) (*facetRuntime, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	record, ok := k.facets[id]
	if !ok {
		return nil, fmt.Errorf("Facet %s is not registered", id)
	}
	return record, nil
}

func (k *Kernel) setPhase(phase string) {
	k.mu.Lock()
	k.phase = phase
	k.mu.Unlock()
}

func (k *Kernel) localKeyedRegistry() *localKeyedRegistry {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.localKeyed
}

func (k *Kernel) internalBinding() *services.RemoteServiceBinding {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.internal
}

// localFacadeFor builds (once) the in-process facade of a local singleton
// provision.
func localFacadeFor(provision *provision) (*services.ServiceFacade, error) {
	if provision.facade == nil {
		facade, err := services.NewMemberFacade(provision.service.ID, nil, provision.implementation, provision.members, nil)
		if err != nil {
			return nil, err
		}
		provision.facade = facade
	}
	return provision.facade, nil
}

func reverseRecords(records []*facetRuntime) []*facetRuntime {
	reversed := make([]*facetRuntime, 0, len(records))
	for index := len(records) - 1; index >= 0; index-- {
		reversed = append(reversed, records[index])
	}
	return reversed
}

func disposeFacetRecords(records []*facetRuntime) []error {
	var errors []error
	for _, record := range records {
		if err := record.lifecycle.dispose(); err != nil {
			errors = append(errors, err)
		}
	}
	return errors
}

// validateFacets checks provision ownership and computes the activation order.
func validateFacets(records []*facetRuntime, externalServices map[string]externalService) ([]string, error) {
	providers := map[string]struct {
		facetID string
		mode    chord.ServiceMode
	}{}
	for serviceID, entry := range externalServices {
		providers[serviceID] = struct {
			facetID string
			mode    chord.ServiceMode
		}{facetID: "", mode: entry.mode}
	}
	for _, record := range records {
		for _, provision := range record.provides {
			existing, ok := providers[provision.serviceID]
			if ok {
				if existing.mode != "" && existing.mode != provision.mode {
					return nil, fmt.Errorf("Service %s is provided as both singleton and keyed", provision.serviceID)
				}
				if existing.facetID == "" {
					return nil, fmt.Errorf("Service %s is provided by both the host and %s", provision.serviceID, record.facetID)
				}
				return nil, fmt.Errorf("Service %s is provided by both %s and %s", provision.serviceID, existing.facetID, record.facetID)
			}
			providers[provision.serviceID] = struct {
				facetID string
				mode    chord.ServiceMode
			}{facetID: record.facetID, mode: provision.mode}
		}
	}

	dependencies := map[string]map[string]bool{}
	dependents := map[string]map[string]bool{}
	for _, record := range records {
		dependencies[record.facetID] = map[string]bool{}
		dependents[record.facetID] = map[string]bool{}
	}
	for _, record := range records {
		for _, requirement := range record.requires {
			provider, ok := providers[requirement.serviceID]
			if !ok {
				return nil, fmt.Errorf("Facet %s requires local/%s/%s, but no facet provides it",
					record.facetID, requirement.serviceID, requirement.mode)
			}
			if provider.mode != "" && provider.mode != requirement.mode {
				owner := provider.facetID
				if owner == "" {
					owner = "the host"
				}
				return nil, fmt.Errorf("Facet %s requires %s as %s, but %s provides it as %s",
					record.facetID, requirement.serviceID, requirement.mode, owner, provider.mode)
			}
			if provider.facetID == "" || provider.facetID == record.facetID {
				continue
			}
			dependencies[record.facetID][provider.facetID] = true
			dependents[provider.facetID][record.facetID] = true
		}
	}
	return topologicalOrder(records, dependencies, dependents)
}

func topologicalOrder(records []*facetRuntime, dependencies, dependents map[string]map[string]bool) ([]string, error) {
	remaining := map[string]int{}
	for id, values := range dependencies {
		remaining[id] = len(values)
	}
	var ready []string
	for _, record := range records {
		if remaining[record.facetID] == 0 {
			ready = append(ready, record.facetID)
		}
	}
	var order []string
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		for _, dependent := range sortedKeys(dependents[id]) {
			remaining[dependent]--
			if remaining[dependent] == 0 {
				ready = append(ready, dependent)
			}
		}
	}
	if len(order) != len(records) {
		var cycle []string
		for _, record := range records {
			if remaining[record.facetID] > 0 {
				cycle = append(cycle, record.facetID)
			}
		}
		return nil, fmt.Errorf("Facet dependency cycle: %s", strings.Join(cycle, ", "))
	}
	return order, nil
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	// Upstream iterates a Set in insertion order; the ready queue order can
	// differ (Go maps), but the resulting topological constraints are the same.
	return keys
}

func sameFacetShape(left, right *facetRuntime) bool {
	return sameReferences(left.requires, right.requires) && sameReferences(left.provides, right.provides)
}

func sameReferences(left, right []serviceReference) bool {
	if len(left) != len(right) {
		return false
	}
	for _, reference := range left {
		found := false
		for _, other := range right {
			if other.serviceID == reference.serviceID && other.mode == reference.mode {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func collectErrors(collected []error, message string) error {
	switch len(collected) {
	case 0:
		return nil
	case 1:
		return collected[0]
	default:
		return &services.AggregateError{Message: message, Errors: collected}
	}
}

// CreateFacetHost creates an active host for one complete set of facets
// (upstream createFacetHost).
func CreateFacetHost(options FacetOptions) (*FacetHost, error) {
	kernel, err := NewKernel(options)
	if err != nil {
		return nil, err
	}
	if err := kernel.Activate(); err != nil {
		return nil, err
	}
	provider, err := kernel.Provider()
	if err != nil {
		return nil, err
	}
	return &FacetHost{
		Services: provider,
		Reload:   kernel.Reload,
		Dispose:  kernel.Dispose,
	}, nil
}

// StaticFacetLoader serves one fixed facet set (upstream
// createStaticFacetLoader).
func StaticFacetLoader(facets []Facet) FacetLoader {
	loaded := append([]Facet{}, facets...)
	return &staticLoader{facets: loaded}
}

type staticLoader struct{ facets []Facet }

func (l *staticLoader) Load() (LoadedFacets, error) {
	return LoadedFacets{Facets: l.facets, Dispose: func() error { return nil }}, nil
}

// CombinedFacetLoader loads several loaders in order (upstream
// combineFacetLoaders).
func CombinedFacetLoader(loaders []FacetLoader) FacetLoader {
	return &combinedLoader{loaders: append([]FacetLoader{}, loaders...)}
}

type combinedLoader struct{ loaders []FacetLoader }

func (l *combinedLoader) Load() (LoadedFacets, error) {
	loaded := []LoadedFacets{}
	for _, loader := range l.loaders {
		result, err := loader.Load()
		if err != nil {
			cleanupErrors := disposeLoadedFacets(reverseLoaded(loaded))
			if len(cleanupErrors) > 0 {
				return LoadedFacets{}, &services.AggregateError{
					Message: "Facet loading and cleanup failed",
					Errors:  append([]error{err}, cleanupErrors...),
				}
			}
			return LoadedFacets{}, err
		}
		loaded = append(loaded, result)
	}
	var facets []Facet
	for _, entry := range loaded {
		facets = append(facets, entry.Facets...)
	}
	var disposed bool
	return LoadedFacets{
		Facets: facets,
		Dispose: func() error {
			if disposed {
				return nil
			}
			disposed = true
			errors := disposeLoadedFacets(reverseLoaded(loaded))
			return collectErrors(errors, "Failed to dispose loaded facets")
		},
	}, nil
}

func reverseLoaded(loaded []LoadedFacets) []LoadedFacets {
	reversed := make([]LoadedFacets, 0, len(loaded))
	for index := len(loaded) - 1; index >= 0; index-- {
		reversed = append(reversed, loaded[index])
	}
	return reversed
}

func disposeLoadedFacets(loaded []LoadedFacets) []error {
	var errors []error
	for _, entry := range loaded {
		if entry.Dispose == nil {
			continue
		}
		if err := entry.Dispose(); err != nil {
			errors = append(errors, err)
		}
	}
	return errors
}
