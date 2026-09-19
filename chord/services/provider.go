package services

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/dat267/gpi/chord"
	"github.com/dat267/gpi/chord/delta"
)

// Port of src/services/provider.ts and src/services/loopback.ts.

// Method is one remotely exposable service method.
//
// Upstream methods are `(...args, context) => Promise<result | void>`; the Go
// port passes the decoded JSON arguments and the caller context explicitly.
type Method func(args []chord.JsonValue, ctx chord.Context) (chord.JsonValue, error)

// Members declares one service implementation's remotely exposable surface.
//
// D18: upstream classifies a JS implementation object by inspecting its data
// properties (functions are methods, registered replicated states are state).
// Go has no dynamic object introspection for dynamically-called methods, so an
// implementation declares its members. Classification keeps upstream's rules:
// names are visited in sorted order, a member is either a method or a state,
// and an implementation without members is rejected.
type Members struct {
	Methods map[string]Method
	States  map[string]ReplicatedStateSource
}

// AggregateError mirrors the JS AggregateError used by collected-error paths.
type AggregateError struct {
	Message string
	Errors  []error
}

func (e *AggregateError) Error() string { return e.Message }

// Unwrap exposes the collected failures.
func (e *AggregateError) Unwrap() []error { return e.Errors }

// ProviderEntry is one catalogue entry: a service and its replication mode
// (empty mode means singleton).
type ProviderEntry struct {
	Service chord.Service
	Mode    chord.ServiceMode
}

// ServiceUpdatePublisher forwards one provider update to a subscriber.
type ServiceUpdatePublisher func(subscriptionID string, update *ServiceProviderUpdate, ctx chord.Context) error

type instanceMember struct {
	kind   string
	method Method
	state  ReplicatedStateSource
}

type classifiedImplementation struct {
	implementation any
	names          []string
	members        map[string]instanceMember
}

type providerInstance struct {
	address  *chord.ServiceInstanceAddress
	impl     any
	names    []string
	members  map[string]instanceMember
	removers []func()
	active   bool
}

func (i *providerInstance) isActive() bool {
	return i.active
}

type providerSubscriberEntry struct {
	update *ServiceProviderUpdate
	ctx    chord.Context
}

type providerSubscriber struct {
	listener   func(update *ServiceProviderUpdate, ctx chord.Context)
	buffer     []providerSubscriberEntry
	active     bool
	terminated bool
	closed     bool
}

type serviceRegistration struct {
	serviceID      string
	mode           chord.ServiceMode
	singleton      *providerInstance
	singletonShape map[string]string
	instances      map[string]*providerInstance
	generations    map[string]int
	subscribers    []*providerSubscriber
}

// RemoteServiceProvider hosts one provider for one remote consumer and owns
// that consumer's subscriptions (upstream RemoteServiceProvider).
type RemoteServiceProvider struct {
	mu            sync.Mutex
	catalogue     []chord.ServiceCatalogueEntry
	registrations map[string]*serviceRegistration
	order         []string
	disposed      bool
}

// NewRemoteServiceProvider builds a provider over a catalogue of entries.
func NewRemoteServiceProvider(entries []ProviderEntry) (*RemoteServiceProvider, error) {
	provider := &RemoteServiceProvider{registrations: map[string]*serviceRegistration{}}
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.Service.Local {
			return nil, fmt.Errorf("Local service %s cannot be published remotely", entry.Service.ID)
		}
		if seen[entry.Service.ID] {
			return nil, fmt.Errorf("Remote service catalogue contains duplicate IDs")
		}
		seen[entry.Service.ID] = true
		mode := entry.Mode
		if mode == "" {
			mode = chord.ServiceModeSingleton
		}
		provider.catalogue = append(provider.catalogue, chord.ServiceCatalogueEntry{ServiceID: entry.Service.ID, Mode: mode})
		provider.registrations[entry.Service.ID] = &serviceRegistration{
			serviceID:   entry.Service.ID,
			mode:        mode,
			instances:   map[string]*providerInstance{},
			generations: map[string]int{},
		}
		provider.order = append(provider.order, entry.Service.ID)
	}
	return provider, nil
}

// Catalogue is the frozen service catalogue.
func (p *RemoteServiceProvider) Catalogue() []chord.ServiceCatalogueEntry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]chord.ServiceCatalogueEntry{}, p.catalogue...)
}

// Provide installs one singleton implementation.
func (p *RemoteServiceProvider) Provide(service chord.Service, implementation any, members Members) error {
	p.mu.Lock()
	if p.disposed {
		p.mu.Unlock()
		return fmt.Errorf("Remote service provider is disposed")
	}
	registration, err := p.registrationLocked(service, chord.ServiceModeSingleton)
	if err != nil {
		p.mu.Unlock()
		return err
	}
	if registration.singleton != nil {
		p.mu.Unlock()
		return &RemoteServiceError{Code: "service_mode_mismatch", Message: fmt.Sprintf("Remote service %s already has a provider", service.ID)}
	}
	classified, err := classifyMembers(registration.serviceID, members)
	if err != nil {
		p.mu.Unlock()
		return err
	}
	classified.implementation = implementation
	shape := memberShape(classified.members)
	if err := assertSingletonShape(registration, shape); err != nil {
		p.mu.Unlock()
		return err
	}
	registration.singleton = p.createInstance(registration, classified, nil)
	registration.singletonShape = shape
	p.mu.Unlock()
	return nil
}

// Withdraw disconnects one singleton while preserving active subscriptions and
// remote facades.
func (p *RemoteServiceProvider) Withdraw(service chord.Service) error {
	p.mu.Lock()
	if p.disposed {
		p.mu.Unlock()
		return fmt.Errorf("Remote service provider is disposed")
	}
	registration, err := p.registrationLocked(service, chord.ServiceModeSingleton)
	if err != nil {
		p.mu.Unlock()
		return err
	}
	previous := registration.singleton
	if previous == nil {
		p.mu.Unlock()
		return nil
	}
	previous.active = false
	for _, remove := range previous.removers {
		remove()
	}
	registration.singleton = nil
	p.mu.Unlock()
	return p.emit(registration, &ServiceProviderUpdate{Type: UpdateUnavailable}, nil)
}

// ValidateReplacement checks a singleton replacement without changing the
// active provider.
func (p *RemoteServiceProvider) ValidateReplacement(service chord.Service, members Members) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.disposed {
		return fmt.Errorf("Remote service provider is disposed")
	}
	registration, err := p.registrationLocked(service, chord.ServiceModeSingleton)
	if err != nil {
		return err
	}
	classified, err := classifyMembers(registration.serviceID, members)
	if err != nil {
		return err
	}
	return assertSingletonShape(registration, memberShape(classified.members))
}

// Replace swaps one singleton without making its stable remote facade
// unavailable.
func (p *RemoteServiceProvider) Replace(service chord.Service, implementation any, members Members) error {
	p.mu.Lock()
	if p.disposed {
		p.mu.Unlock()
		return fmt.Errorf("Remote service provider is disposed")
	}
	registration, err := p.registrationLocked(service, chord.ServiceModeSingleton)
	if err != nil {
		p.mu.Unlock()
		return err
	}
	classified, err := classifyMembers(registration.serviceID, members)
	if err != nil {
		p.mu.Unlock()
		return err
	}
	classified.implementation = implementation
	shape := memberShape(classified.members)
	if err := assertSingletonShape(registration, shape); err != nil {
		p.mu.Unlock()
		return err
	}
	replacement := p.createInstance(registration, classified, nil)
	previous := registration.singleton
	if previous != nil {
		previous.active = false
		for _, remove := range previous.removers {
			remove()
		}
	}
	registration.singleton = replacement
	registration.singletonShape = shape
	p.mu.Unlock()
	return p.emit(registration, &ServiceProviderUpdate{Type: UpdateReplaced, Snapshot: snapshotPtr(snapshotInstance(replacement))}, nil)
}

// Use returns the local singleton implementation.
func (p *RemoteServiceProvider) Use(service chord.Service) (any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.disposed {
		return nil, fmt.Errorf("Remote service provider is disposed")
	}
	if err := p.assertServiceLocked(service); err != nil {
		return nil, err
	}
	registration := p.registrations[service.ID]
	if registration == nil || registration.mode != chord.ServiceModeSingleton || registration.singleton == nil {
		return nil, &RemoteServiceError{Code: "service_not_found", Message: fmt.Sprintf("Remote service %s has no local provider", service.ID)}
	}
	return registration.singleton.impl, nil
}

// Spawn adds one keyed instance and returns its close function.
func (p *RemoteServiceProvider) Spawn(service chord.Service, key string, implementation any, members Members) (func(), error) {
	p.mu.Lock()
	if p.disposed {
		p.mu.Unlock()
		return nil, fmt.Errorf("Remote service provider is disposed")
	}
	if err := p.assertServiceLocked(service); err != nil {
		p.mu.Unlock()
		return nil, err
	}
	if key == "" {
		p.mu.Unlock()
		return nil, fmt.Errorf("Remote service instance key must not be empty")
	}
	registration, err := p.registrationLocked(service, chord.ServiceModeKeyed)
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}
	if _, exists := registration.instances[key]; exists {
		p.mu.Unlock()
		return nil, &RemoteServiceError{
			Code:    "service_mode_mismatch",
			Message: fmt.Sprintf("Remote service %s already has a live instance with key %s", service.ID, key),
		}
	}
	generation := registration.generations[key] + 1
	registration.generations[key] = generation
	address := &chord.ServiceInstanceAddress{Key: key, Generation: generation}
	classified, err := classifyMembers(registration.serviceID, members)
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}
	classified.implementation = implementation
	instance := p.createInstance(registration, classified, address)
	registration.instances[key] = instance
	p.mu.Unlock()

	if err := p.emit(registration, &ServiceProviderUpdate{Type: UpdateSpawned, SpawnedInstance: snapshotPtr(snapshotInstance(instance))}, nil); err != nil {
		return nil, err
	}
	closed := false
	var closeMu sync.Mutex
	return func() {
		closeMu.Lock()
		if closed {
			closeMu.Unlock()
			return
		}
		closed = true
		closeMu.Unlock()
		p.mu.Lock()
		if registration.instances[key] != instance {
			p.mu.Unlock()
			return
		}
		instance.active = false
		for _, remove := range instance.removers {
			remove()
		}
		delete(registration.instances, key)
		p.mu.Unlock()
		_ = p.emit(registration, &ServiceProviderUpdate{Type: UpdateClosed, ClosedInstance: address}, nil)
	}, nil
}

// Invoke calls one remote method.
func (p *RemoteServiceProvider) Invoke(call chord.ServiceCall, ctx chord.Context) (chord.JsonValue, error) {
	p.mu.Lock()
	if p.disposed {
		p.mu.Unlock()
		return nil, fmt.Errorf("Remote service provider is disposed")
	}
	if err := p.assertAllowedLocked(call.ServiceID); err != nil {
		p.mu.Unlock()
		return nil, err
	}
	registration := p.registrations[call.ServiceID]
	instance, err := registration.resolveInstance(call.Instance)
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}
	member, ok := instance.members[call.Member]
	if !ok {
		p.mu.Unlock()
		return nil, &RemoteServiceError{
			Code:    "service_member_not_found",
			Message: fmt.Sprintf("Unknown remote service member %s.%s", call.ServiceID, call.Member),
		}
	}
	if member.kind != MemberMethod {
		p.mu.Unlock()
		return nil, &RemoteServiceError{
			Code:    "service_member_mismatch",
			Message: fmt.Sprintf("Remote service member %s.%s is not a method", call.ServiceID, call.Member),
		}
	}
	method := member.method
	p.mu.Unlock()
	return method(call.Args, ctx)
}

// ProviderSubscription is one live provider subscription.
type ProviderSubscription struct {
	Snapshot *ServiceSubscriptionSnapshot
	activate func() error
	closeFn  func()
}

// Activate begins ordered update delivery after the snapshot is installed.
func (s *ProviderSubscription) Activate() error {
	if s.activate == nil {
		return nil
	}
	return s.activate()
}

// Close ends the subscription.
func (s *ProviderSubscription) Close() {
	if s.closeFn != nil {
		s.closeFn()
	}
}

// Subscribe registers one subscriber and returns its snapshot.
func (p *RemoteServiceProvider) Subscribe(
	serviceID string,
	mode chord.ServiceMode,
	listener func(update *ServiceProviderUpdate, ctx chord.Context),
) (*ProviderSubscription, error) {
	p.mu.Lock()
	if p.disposed {
		p.mu.Unlock()
		return nil, fmt.Errorf("Remote service provider is disposed")
	}
	if err := p.assertAllowedLocked(serviceID); err != nil {
		p.mu.Unlock()
		return nil, err
	}
	registration, err := p.registrationLockedByID(serviceID, mode)
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}
	if registration.mode == chord.ServiceModeSingleton && registration.singleton == nil {
		p.mu.Unlock()
		return nil, &RemoteServiceError{Code: "service_not_found", Message: fmt.Sprintf("Remote service %s has no provider", serviceID)}
	}

	subscriber := &providerSubscriber{listener: listener}
	p.publishPendingLocked(registration)
	registration.subscribers = append(registration.subscribers, subscriber)
	snapshot := p.snapshotLocked(registration)
	p.mu.Unlock()

	return &ProviderSubscription{
		Snapshot: snapshot,
		activate: func() error {
			p.mu.Lock()
			if subscriber.closed || subscriber.active {
				p.mu.Unlock()
				return nil
			}
			subscriber.active = true
			buffered := subscriber.buffer
			subscriber.buffer = nil
			p.mu.Unlock()

			var collected []error
			for _, entry := range buffered {
				if err := callProviderListener(listener, entry.update, entry.ctx); err != nil {
					collected = append(collected, err)
				}
			}
			p.mu.Lock()
			if subscriber.terminated {
				subscriber.closed = true
				p.removeSubscriberLocked(registration, subscriber)
			}
			p.mu.Unlock()
			return collectErrors(collected, "Failed to activate remote service subscription")
		},
		closeFn: func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if subscriber.closed {
				return
			}
			subscriber.closed = true
			subscriber.buffer = nil
			p.removeSubscriberLocked(registration, subscriber)
		},
	}, nil
}

// Dispose releases every instance and subscription.
func (p *RemoteServiceProvider) Dispose() error {
	p.mu.Lock()
	if p.disposed {
		p.mu.Unlock()
		return nil
	}
	p.disposed = true
	// Instances are retired first; the lifecycle updates are emitted after the
	// provider lock is released (emit takes the same lock), and subscribers are
	// settled only after those updates were delivered.
	type emission struct {
		registration *serviceRegistration
		update       *ServiceProviderUpdate
	}
	var emissions []emission
	registrations := make([]*serviceRegistration, 0, len(p.order))
	for _, serviceID := range p.order {
		registration := p.registrations[serviceID]
		if registration == nil {
			continue
		}
		registrations = append(registrations, registration)
		if singleton := takeSingleton(registration); singleton != nil {
			emissions = append(emissions, emission{registration, &ServiceProviderUpdate{Type: UpdateUnavailable}})
		}
		for _, key := range sortedInstanceKeys(registration) {
			instance := registration.instances[key]
			instance.active = false
			for _, remove := range instance.removers {
				remove()
			}
			delete(registration.instances, key)
			emissions = append(emissions, emission{registration, &ServiceProviderUpdate{Type: UpdateClosed, ClosedInstance: instance.address}})
		}
	}
	p.mu.Unlock()

	var collected []error
	for _, entry := range emissions {
		if err := p.emit(entry.registration, entry.update, nil); err != nil {
			collected = append(collected, err)
		}
	}

	p.mu.Lock()
	for _, registration := range registrations {
		for _, subscriber := range registration.subscribers {
			if subscriber.active {
				subscriber.closed = true
				subscriber.buffer = nil
			} else {
				subscriber.terminated = true
			}
		}
		registration.subscribers = nil
	}
	p.registrations = map[string]*serviceRegistration{}
	p.order = nil
	p.mu.Unlock()
	return collectErrors(collected, "Failed to dispose remote service provider")
}

func takeSingleton(registration *serviceRegistration) *providerInstance {
	singleton := registration.singleton
	if singleton == nil {
		return nil
	}
	singleton.active = false
	for _, remove := range singleton.removers {
		remove()
	}
	registration.singleton = nil
	return singleton
}

// RemoteServiceEndpoint hosts one provider for one remote consumer.
type RemoteServiceEndpoint struct {
	mu            sync.Mutex
	provider      *RemoteServiceProvider
	subscriptions map[string]*ProviderSubscription
	disposed      bool
}

// CreateRemoteServiceEndpoint binds a provider to an endpoint.
func CreateRemoteServiceEndpoint(provider *RemoteServiceProvider) *RemoteServiceEndpoint {
	return &RemoteServiceEndpoint{provider: provider, subscriptions: map[string]*ProviderSubscription{}}
}

// Invoke dispatches one call, handling the subscribe/unsubscribe/catalogue
// control calls.
func (e *RemoteServiceEndpoint) Invoke(call chord.ServiceCall, publish ServiceUpdatePublisher, ctx chord.Context) (chord.JsonValue, error) {
	e.mu.Lock()
	if e.disposed {
		e.mu.Unlock()
		return nil, fmt.Errorf("Remote service endpoint is disposed")
	}
	e.mu.Unlock()

	if control := DecodeServiceControlCall(call); control != nil {
		switch control.Type {
		case "catalogue":
			return catalogueValue(e.provider.Catalogue()), nil
		case "subscribe":
			e.mu.Lock()
			if _, exists := e.subscriptions[control.SubscriptionID]; exists {
				e.mu.Unlock()
				return nil, fmt.Errorf("Service subscription ID is already active")
			}
			e.mu.Unlock()
			subscriptionID := control.SubscriptionID
			subscription, err := e.provider.Subscribe(control.ServiceID, control.Mode,
				func(update *ServiceProviderUpdate, updateCtx chord.Context) {
					// Publication failures are the subscriber's problem; upstream
					// attaches a catch that swallows them.
					if publish != nil {
						_ = publish(subscriptionID, update, updateCtx)
					}
				})
			if err != nil {
				return nil, err
			}
			e.mu.Lock()
			if e.disposed {
				e.mu.Unlock()
				subscription.Close()
				return nil, fmt.Errorf("Remote service endpoint is disposed")
			}
			e.subscriptions[subscriptionID] = subscription
			e.mu.Unlock()
			if err := subscription.Activate(); err != nil {
				return nil, err
			}
			return snapshotValue(subscription.Snapshot), nil
		case "unsubscribe":
			e.mu.Lock()
			subscription, ok := e.subscriptions[control.SubscriptionID]
			if ok {
				delete(e.subscriptions, control.SubscriptionID)
			}
			e.mu.Unlock()
			if !ok {
				return nil, fmt.Errorf("Service subscription was not found")
			}
			subscription.Close()
			return nil, nil
		}
	}
	return e.provider.Invoke(call, ctx)
}

// Dispose closes every subscription owned by the endpoint.
func (e *RemoteServiceEndpoint) Dispose() {
	e.mu.Lock()
	if e.disposed {
		e.mu.Unlock()
		return
	}
	e.disposed = true
	subscriptions := e.subscriptions
	e.subscriptions = map[string]*ProviderSubscription{}
	e.mu.Unlock()
	for _, subscription := range subscriptions {
		subscription.Close()
	}
}

// CreateLoopbackServiceTransport connects a provider to a binding without
// changing remote service semantics (upstream createLoopbackServiceTransport).
func CreateLoopbackServiceTransport(provider *RemoteServiceProvider) RemoteServiceTransport {
	return &loopbackTransport{provider: provider}
}

type loopbackTransport struct {
	provider *RemoteServiceProvider
}

func (t *loopbackTransport) Invoke(call chord.ServiceCall, ctx chord.Context) (chord.JsonValue, error) {
	return t.provider.Invoke(call, ctx)
}

func (t *loopbackTransport) Subscribe(
	serviceID string,
	mode chord.ServiceMode,
	listener func(update *ServiceProviderUpdate, ctx chord.Context),
	ctx chord.Context,
) (*ServiceSubscription, error) {
	subscription, err := t.provider.Subscribe(serviceID, mode, listener)
	if err != nil {
		return nil, err
	}
	return &ServiceSubscription{
		Snapshot: subscription.Snapshot,
		Activate: func() { _ = subscription.Activate() },
		Close: func() error {
			subscription.Close()
			return nil
		},
	}, nil
}

func (p *RemoteServiceProvider) registrationLocked(service chord.Service, mode chord.ServiceMode) (*serviceRegistration, error) {
	if err := p.assertServiceLocked(service); err != nil {
		return nil, err
	}
	return p.registrationLockedByID(service.ID, mode)
}

// assertServiceLocked applies the remotable and allowlist checks.
func (p *RemoteServiceProvider) assertServiceLocked(service chord.Service) error {
	if service.Local {
		return &RemoteServiceError{Code: "service_not_allowed", Message: fmt.Sprintf("Service %s is process-local", service.ID)}
	}
	return p.assertAllowedLocked(service.ID)
}

func (p *RemoteServiceProvider) assertAllowedLocked(serviceID string) error {
	if _, ok := p.registrations[serviceID]; !ok {
		return &RemoteServiceError{Code: "service_not_allowed", Message: fmt.Sprintf("Remote service %s is not allowlisted", serviceID)}
	}
	return nil
}

func (p *RemoteServiceProvider) registrationLockedByID(serviceID string, mode chord.ServiceMode) (*serviceRegistration, error) {
	registration, ok := p.registrations[serviceID]
	if !ok {
		return nil, &RemoteServiceError{Code: "service_not_found", Message: fmt.Sprintf("Unknown remote service %s", serviceID)}
	}
	if registration.mode != mode {
		return nil, &RemoteServiceError{
			Code:    "service_mode_mismatch",
			Message: fmt.Sprintf("Remote service %s is %s, not %s", serviceID, registration.mode, mode),
		}
	}
	return registration, nil
}

func (p *RemoteServiceProvider) createInstance(
	registration *serviceRegistration,
	classified *classifiedImplementation,
	address *chord.ServiceInstanceAddress,
) *providerInstance {
	instance := &providerInstance{
		address: address,
		impl:    classified.implementation,
		names:   classified.names,
		members: classified.members,
		active:  true,
	}
	for _, name := range classified.names {
		member := classified.members[name]
		if member.kind != MemberState {
			continue
		}
		memberName := name
		state := member.state
		instance.removers = append(instance.removers, state.SubscribeOps(
			func(ops []delta.Op, sequence int, ctx chord.Context) error {
				if !instance.isActive() {
					return nil
				}
				return p.emit(registration, &ServiceProviderUpdate{
					Type:     UpdateState,
					Instance: address,
					Member:   memberName,
					Sequence: sequence,
					Ops:      ops,
				}, ctx)
			}))
	}
	return instance
}

// resolveInstance resolves the call target, mapping failures to coded errors.
func (r *serviceRegistration) resolveInstance(address *chord.ServiceInstanceAddress) (*providerInstance, error) {
	if r.mode == chord.ServiceModeSingleton {
		if address != nil {
			return nil, &RemoteServiceError{Code: "service_mode_mismatch", Message: fmt.Sprintf("Remote service %s is singleton", r.serviceID)}
		}
		if r.singleton == nil {
			return nil, &RemoteServiceError{Code: "service_not_found", Message: fmt.Sprintf("Remote service %s has no provider", r.serviceID)}
		}
		return r.singleton, nil
	}
	if address == nil {
		return nil, &RemoteServiceError{Code: "service_mode_mismatch", Message: fmt.Sprintf("Remote service %s is keyed", r.serviceID)}
	}
	instance := r.instances[address.Key]
	if instance == nil {
		return nil, &RemoteServiceError{
			Code:    "service_instance_not_found",
			Message: fmt.Sprintf("Remote service %s has no instance %s", r.serviceID, address.Key),
		}
	}
	if instance.address == nil || instance.address.Generation != address.Generation {
		return nil, &RemoteServiceError{
			Code:    "service_stale_instance",
			Message: fmt.Sprintf("Remote service %s instance %s is stale", r.serviceID, address.Key),
		}
	}
	return instance, nil
}

// publishPendingLocked flushes every state member before a snapshot.
func (p *RemoteServiceProvider) publishPendingLocked(registration *serviceRegistration) {
	ctx := ServiceDeliveryContext()
	var instances []*providerInstance
	if registration.mode == chord.ServiceModeSingleton {
		if registration.singleton != nil {
			instances = append(instances, registration.singleton)
		}
	} else {
		for _, key := range sortedInstanceKeys(registration) {
			instances = append(instances, registration.instances[key])
		}
	}
	for _, instance := range instances {
		for _, name := range instance.names {
			member := instance.members[name]
			if member.kind == MemberState {
				_ = member.state.PublishState(ctx)
			}
		}
	}
}

// snapshotLocked builds the subscription snapshot for one registration.
func (p *RemoteServiceProvider) snapshotLocked(registration *serviceRegistration) *ServiceSubscriptionSnapshot {
	snapshot := &ServiceSubscriptionSnapshot{ServiceID: registration.serviceID, Mode: registration.mode}
	if registration.mode == chord.ServiceModeSingleton {
		if registration.singleton != nil {
			snapshot.Instances = append(snapshot.Instances, snapshotInstance(registration.singleton))
		}
		return snapshot
	}
	keys := sortedInstanceKeys(registration)
	for _, key := range keys {
		snapshot.Instances = append(snapshot.Instances, snapshotInstance(registration.instances[key]))
	}
	return snapshot
}

func snapshotInstance(instance *providerInstance) ServiceInstanceSnapshot {
	snapshot := ServiceInstanceSnapshot{Instance: instance.address}
	for _, name := range instance.names {
		member := instance.members[name]
		if member.kind == MemberMethod {
			snapshot.Members = append(snapshot.Members, ServiceMemberSnapshot{Name: name, Kind: MemberMethod})
			continue
		}
		snapshot.Members = append(snapshot.Members, ServiceMemberSnapshot{
			Name:     name,
			Kind:     MemberState,
			Sequence: member.state.Sequence(),
			Ops:      []delta.Op{{Verb: delta.VerbReplace, Path: delta.Path{}, Value: member.state.Published()}},
		})
	}
	return snapshot
}

// emit publishes one update to every live subscriber, buffering for
// subscribers that have not activated yet.
func (p *RemoteServiceProvider) emit(registration *serviceRegistration, update *ServiceProviderUpdate, ctx chord.Context) error {
	p.mu.Lock()
	if len(registration.subscribers) == 0 {
		p.mu.Unlock()
		return nil
	}
	deliveryContext := ctx
	if deliveryContext == nil {
		deliveryContext = ServiceDeliveryContext()
	}
	var deliverable []func(*ServiceProviderUpdate, chord.Context)
	for _, subscriber := range registration.subscribers {
		if subscriber.closed {
			continue
		}
		if !subscriber.active {
			subscriber.buffer = append(subscriber.buffer, providerSubscriberEntry{update: update, ctx: deliveryContext})
			continue
		}
		deliverable = append(deliverable, subscriber.listener)
	}
	p.mu.Unlock()

	var collected []error
	for _, listener := range deliverable {
		if err := callProviderListener(listener, update, deliveryContext); err != nil {
			collected = append(collected, err)
		}
	}
	return collectErrors(collected, fmt.Sprintf("Failed to publish remote service %s update", registration.serviceID))
}

func (p *RemoteServiceProvider) removeSubscriberLocked(registration *serviceRegistration, target *providerSubscriber) {
	kept := registration.subscribers[:0]
	for _, subscriber := range registration.subscribers {
		if subscriber != target {
			kept = append(kept, subscriber)
		}
	}
	registration.subscribers = kept
}

func callProviderListener(listener func(*ServiceProviderUpdate, chord.Context), update *ServiceProviderUpdate, ctx chord.Context) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = toError(recovered)
		}
	}()
	listener(update, ctx)
	return nil
}

// classifyMembers validates and orders a declared member set.
func classifyMembers(serviceID string, members Members) (*classifiedImplementation, error) {
	names := make([]string, 0, len(members.Methods)+len(members.States))
	for name := range members.Methods {
		names = append(names, name)
	}
	for name := range members.States {
		if _, duplicate := members.Methods[name]; duplicate {
			return nil, fmt.Errorf("Remote service member %s.%s is not remotely exposable", serviceID, name)
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("Remote service %s has no members", serviceID)
	}
	classified := &classifiedImplementation{names: names, members: map[string]instanceMember{}}
	for _, name := range names {
		if method, ok := members.Methods[name]; ok {
			if method == nil {
				return nil, fmt.Errorf("Remote service member %s.%s is not remotely exposable", serviceID, name)
			}
			classified.members[name] = instanceMember{kind: MemberMethod, method: method}
			continue
		}
		state := members.States[name]
		if state == nil {
			return nil, fmt.Errorf("Remote service member %s.%s is not remotely exposable", serviceID, name)
		}
		classified.members[name] = instanceMember{kind: MemberState, state: state}
	}
	return classified, nil
}

func memberShape(members map[string]instanceMember) map[string]string {
	shape := make(map[string]string, len(members))
	for name, member := range members {
		shape[name] = member.kind
	}
	return shape
}

func assertSingletonShape(registration *serviceRegistration, replacement map[string]string) error {
	current := registration.singletonShape
	if current == nil || sameShape(current, replacement) {
		return nil
	}
	return &RemoteServiceError{
		Code:    "service_member_mismatch",
		Message: fmt.Sprintf("Remote service %s replacement must preserve its member shape", registration.serviceID),
	}
}

func sameShape(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for name, kind := range left {
		if right[name] != kind {
			return false
		}
	}
	return true
}

func sortedInstanceKeys(registration *serviceRegistration) []string {
	keys := make([]string, 0, len(registration.instances))
	for key := range registration.instances {
		keys = append(keys, key)
	}
	// Upstream sorts with localeCompare; keys are service instance ids, so
	// byte-wise ordering matches for the ASCII keys the runtime uses.
	sort.Slice(keys, func(i, j int) bool { return strings.Compare(keys[i], keys[j]) < 0 })
	return keys
}

func collectErrors(collected []error, message string) error {
	switch len(collected) {
	case 0:
		return nil
	case 1:
		return collected[0]
	default:
		return &AggregateError{Message: message, Errors: collected}
	}
}

func toError(value any) error {
	if err, ok := value.(error); ok {
		return err
	}
	return fmt.Errorf("%v", value)
}

// catalogueValue renders the catalogue as a wire JSON value.
func catalogueValue(catalogue []chord.ServiceCatalogueEntry) chord.JsonValue {
	entries := make([]any, 0, len(catalogue))
	for _, entry := range catalogue {
		entries = append(entries, map[string]any{"serviceId": entry.ServiceID, "mode": entry.Mode})
	}
	return entries
}

// snapshotValue renders a subscription snapshot as a wire JSON value.
func snapshotValue(snapshot *ServiceSubscriptionSnapshot) chord.JsonValue {
	if snapshot == nil {
		return nil
	}
	instances := make([]any, 0, len(snapshot.Instances))
	for _, instance := range snapshot.Instances {
		instances = append(instances, instanceValue(instance))
	}
	return map[string]any{"serviceId": snapshot.ServiceID, "mode": snapshot.Mode, "instances": instances}
}

func instanceValue(instance ServiceInstanceSnapshot) any {
	members := make([]any, 0, len(instance.Members))
	for _, member := range instance.Members {
		entry := map[string]any{"name": member.Name, "kind": member.Kind}
		if member.Kind == MemberState {
			ops := make([]any, 0, len(member.Ops))
			for _, op := range member.Ops {
				ops = append(ops, op.Tuple())
			}
			entry["sequence"] = member.Sequence
			entry["ops"] = ops
		}
		members = append(members, entry)
	}
	value := map[string]any{"members": members}
	if instance.Instance != nil {
		value["instance"] = map[string]any{"key": instance.Instance.Key, "generation": instance.Instance.Generation}
	}
	return value
}

// snapshotPtr boxes an instance snapshot for the tagged update unions.
func snapshotPtr(snapshot ServiceInstanceSnapshot) *ServiceInstanceSnapshot { return &snapshot }

// ValidateRemoteServiceImplementation checks one declared member set (upstream
// validateRemoteServiceImplementation).
func ValidateRemoteServiceImplementation(serviceID string, members Members) error {
	_, err := classifyMembers(serviceID, members)
	return err
}

var _ = errors.Join
var _ = context.Background
