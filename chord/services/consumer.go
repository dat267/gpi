package services

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/dat267/gpi/chord"
	"github.com/dat267/gpi/chord/delta"
)

// Port of src/services/consumer.ts and src/services/handle.ts.

// RemoteServiceBindingOptions configure one binding.
type RemoteServiceBindingOptions struct {
	// Services is the allowlist of service identities.
	Services  []chord.Service
	Transport RemoteServiceTransport
	// Bound starts the binding attached (default true).
	Bound *bool
	// OnError observes asynchronous failures.
	OnError func(error)
	// AssertAccess runs before every handle access.
	AssertAccess func() error
}

// memberState is the mutable kind bookkeeping shared by every view of one
// member (upstream MemberSlot state, which guarded views share).
type memberState struct {
	mu           sync.Mutex
	kind         string
	expectedKind string
	replica      *StateReplica
}

// RemoteMember is one member handle of a service facade.
//
// D18: upstream exposes a callable Proxy per member (`member(...args, context)`,
// `member.value`, `member.subscribe(listener)`); the Go port exposes the same
// semantics through explicit methods, keeping the kind tracking, access guard,
// and staleness errors.
type RemoteMember struct {
	state        *memberState
	serviceID    string
	name         string
	invoke       func(args []chord.JsonValue, ctx chord.Context) (chord.JsonValue, error)
	isActive     func() bool
	assertAccess func() error
}

// Kind is the member kind once described by a snapshot, or "" when unknown.
func (m *RemoteMember) Kind() string {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	return m.state.kind
}

// Name is the member name.
func (m *RemoteMember) Name() string { return m.name }

// Call invokes the member as a method.
func (m *RemoteMember) Call(args []chord.JsonValue, ctx chord.Context) (chord.JsonValue, error) {
	if err := m.assertAccess(); err != nil {
		return nil, err
	}
	if err := m.expect(MemberMethod); err != nil {
		return nil, err
	}
	if !m.isActive() {
		return nil, &RemoteServiceError{
			Code:    "service_stale_instance",
			Message: fmt.Sprintf("Remote service %s binding is closed", m.serviceID),
		}
	}
	return m.invoke(args, ctx)
}

// Value reads the member as a replicated state. The bool reports whether the
// state is hydrated (upstream's `member.value` returning undefined while cold);
// the error reports a kind or access failure (upstream's getter throwing).
func (m *RemoteMember) Value() (chord.JsonValue, bool, error) {
	if err := m.assertAccess(); err != nil {
		return nil, false, err
	}
	if err := m.expect(MemberState); err != nil {
		return nil, false, err
	}
	value, ok := m.state.replica.Value()
	return value, ok, nil
}

// Subscribe observes the member as a replicated state.
func (m *RemoteMember) Subscribe(listener func(value chord.JsonValue, ctx chord.Context, delivery chord.ReplicatedStateDelivery)) (func(), error) {
	if listener == nil {
		return nil, fmt.Errorf("Replicated state subscription listener must be a function")
	}
	if err := m.assertAccess(); err != nil {
		return nil, err
	}
	if err := m.expect(MemberState); err != nil {
		return nil, err
	}
	return m.state.replica.Subscribe(listener), nil
}

// Describe records the member kind from a snapshot (upstream setDescription).
func (m *RemoteMember) Describe(kind string) error {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	if m.state.kind != "" && m.state.kind != kind {
		return fmt.Errorf("Remote service member %s.%s changed kind", m.serviceID, m.name)
	}
	m.state.kind = kind
	if m.state.expectedKind != "" && m.state.expectedKind != kind {
		return &RemoteServiceError{
			Code:    "service_member_mismatch",
			Message: fmt.Sprintf("Remote service member %s.%s is %s, not %s", m.serviceID, m.name, kind, m.state.expectedKind),
		}
	}
	return nil
}

// Hydrate installs a state snapshot.
func (m *RemoteMember) Hydrate(sequence int, ops []delta.Op, ctx chord.Context) error {
	if err := m.Describe(MemberState); err != nil {
		return err
	}
	return m.state.replica.Hydrate(sequence, ops, ctx)
}

// UpdateState applies one state delta.
func (m *RemoteMember) UpdateState(sequence int, ops []delta.Op, ctx chord.Context) error {
	if err := m.Describe(MemberState); err != nil {
		return err
	}
	return m.state.replica.Update(sequence, ops, ctx)
}

// Clear drops the member's state.
func (m *RemoteMember) Clear() { m.state.replica.Clear() }

// expect records the usage kind, rejecting a conflicting one.
func (m *RemoteMember) expect(kind string) error {
	m.state.mu.Lock()
	defer m.state.mu.Unlock()
	if m.state.expectedKind != "" && m.state.expectedKind != kind {
		return &RemoteServiceError{
			Code:    "service_member_mismatch",
			Message: fmt.Sprintf("Remote service member %s.%s was used as two different kinds", m.serviceID, m.name),
		}
	}
	m.state.expectedKind = kind
	if m.state.kind != "" && m.state.kind != kind {
		return &RemoteServiceError{
			Code:    "service_member_mismatch",
			Message: fmt.Sprintf("Remote service member %s.%s is %s, not %s", m.serviceID, m.name, m.state.kind, kind),
		}
	}
	return nil
}

// ServiceFacade is the consumer-side handle for one service instance.
type ServiceFacade struct {
	mu           sync.Mutex
	serviceID    string
	address      *chord.ServiceInstanceAddress
	transport    RemoteServiceTransport
	isActive     func() bool
	assertAccess func() error
	reportError  func(error)
	members      map[string]*RemoteMember
	descriptions map[string]string
}

func newServiceFacade(
	serviceID string,
	address *chord.ServiceInstanceAddress,
	transport RemoteServiceTransport,
	isActive func() bool,
	assertAccess func() error,
	reportError func(error),
) *ServiceFacade {
	return &ServiceFacade{
		serviceID:    serviceID,
		address:      address,
		transport:    transport,
		isActive:     isActive,
		assertAccess: assertAccess,
		reportError:  reportError,
		members:      map[string]*RemoteMember{},
		descriptions: map[string]string{},
	}
}

// ServiceID is the facade's service id.
func (f *ServiceFacade) ServiceID() string { return f.serviceID }

// Address is the facade's instance address (nil for singletons).
func (f *ServiceFacade) Address() *chord.ServiceInstanceAddress { return f.address }

// Member returns the handle for one member name, creating it on demand
// (upstream's facade proxy resolves any property name).
func (f *ServiceFacade) Member(name string) *RemoteMember {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.memberLocked(name)
}

func (f *ServiceFacade) memberLocked(name string) *RemoteMember {
	if member, ok := f.members[name]; ok {
		return member
	}
	address := f.address
	member := &RemoteMember{
		state:     &memberState{replica: NewStateReplica(f.reportError)},
		serviceID: f.serviceID,
		name:      name,
		invoke: func(args []chord.JsonValue, ctx chord.Context) (chord.JsonValue, error) {
			return f.transport.Invoke(chord.ServiceCall{
				ServiceID: f.serviceID,
				Instance:  address,
				Member:    name,
				Args:      args,
			}, ctx)
		},
		isActive:     f.isActive,
		assertAccess: f.assertAccess,
	}
	if kind, ok := f.descriptions[name]; ok {
		_ = member.Describe(kind)
	}
	f.members[name] = member
	return member
}

// Install applies one instance snapshot.
func (f *ServiceFacade) Install(snapshot ServiceInstanceSnapshot, ctx chord.Context) error {
	if !sameAddress(snapshot.Instance, f.address) {
		return fmt.Errorf("Remote service snapshot has the wrong address")
	}
	members, err := validateMembers(snapshot.Members)
	if err != nil {
		return err
	}

	f.mu.Lock()
	for name := range f.members {
		if _, ok := members[name]; !ok {
			f.mu.Unlock()
			return &RemoteServiceError{
				Code:    "service_member_not_found",
				Message: fmt.Sprintf("Unknown remote service member %s.%s", f.serviceID, name),
			}
		}
	}
	f.descriptions = map[string]string{}
	names := make([]string, 0, len(members))
	for name, member := range members {
		f.descriptions[name] = member.Kind
		names = append(names, name)
	}
	sort.Strings(names)
	// Method descriptions only apply to existing handles; state members
	// hydrate through their handle (creating it when needed).
	var existingMethods []*RemoteMember
	for _, name := range names {
		member := members[name]
		if member.Kind == MemberState {
			f.memberLocked(member.Name)
			continue
		}
		if slot, ok := f.members[member.Name]; ok {
			existingMethods = append(existingMethods, slot)
		}
	}
	f.mu.Unlock()

	for _, slot := range existingMethods {
		if err := slot.Describe(MemberMethod); err != nil {
			return err
		}
	}
	for _, name := range names {
		member := members[name]
		if member.Kind != MemberState {
			continue
		}
		if err := f.Member(name).Hydrate(member.Sequence, member.Ops, ctx); err != nil {
			return err
		}
	}
	return nil
}

// UpdateState applies one state update to a member.
func (f *ServiceFacade) UpdateState(member string, sequence int, ops []delta.Op, ctx chord.Context) error {
	f.mu.Lock()
	kind := f.descriptions[member]
	f.mu.Unlock()
	if kind != MemberState {
		return fmt.Errorf("Remote service update targets non-state member %s.%s", f.serviceID, member)
	}
	return f.Member(member).UpdateState(sequence, ops, ctx)
}

// Clear drops every member's state.
func (f *ServiceFacade) Clear() {
	f.mu.Lock()
	members := make([]*RemoteMember, 0, len(f.members))
	for _, member := range f.members {
		members = append(members, member)
	}
	f.mu.Unlock()
	for _, member := range members {
		member.Clear()
	}
}

// GuardedFacade is a guarded view of a facade (upstream ServiceSlot view): the
// member handles share the facade's state but run the view's access checks.
type GuardedFacade struct {
	facade       *ServiceFacade
	assertAccess func() error
}

// Member returns the guarded handle for one member name. The handle shares the
// facade member's state and runs the view guard before the member's own guard.
func (g *GuardedFacade) Member(name string) *RemoteMember {
	return GuardMember(g.facade.Member(name), g.assertAccess)
}

// ServiceID is the underlying facade's service id.
func (g *GuardedFacade) ServiceID() string { return g.facade.ServiceID() }

// Address is the underlying facade's instance address.
func (g *GuardedFacade) Address() *chord.ServiceInstanceAddress { return g.facade.Address() }

// singletonBinding tracks one singleton facade and its subscription.
type singletonBinding struct {
	facade       *ServiceFacade
	subscription *ServiceSubscription
	starting     *completion
	active       bool
	revision     int
}

// keyedInstance is one live keyed instance.
type keyedInstance struct {
	mu         sync.Mutex
	key        string
	generation int
	facade     *ServiceFacade
	active     bool
}

func (i *keyedInstance) EntryKey() string     { return i.key }
func (i *keyedInstance) EntryGeneration() int { return i.generation }
func (i *keyedInstance) EntryService() any    { return i }
func (i *keyedInstance) Deactivate() {
	i.mu.Lock()
	i.active = false
	i.mu.Unlock()
	i.facade.Clear()
}
func (i *keyedInstance) isActive() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.active
}

// keyedBinding consumes one keyed service (upstream KeyedBinding).
type keyedBinding struct {
	mu           sync.Mutex
	service      chord.Service
	transport    RemoteServiceTransport
	reportError  func(error)
	assertAccess func() error
	onEmpty      func()
	instances    *Directory[*keyedInstance]
	subscription *ServiceSubscription
	starting     *completion
	closed       bool
	bound        bool
	revision     int
}

func newKeyedBinding(
	service chord.Service,
	transport RemoteServiceTransport,
	reportError func(error),
	assertAccess func() error,
	onEmpty func(),
	bound bool,
) *keyedBinding {
	return &keyedBinding{
		service:      service,
		transport:    transport,
		reportError:  reportError,
		assertAccess: assertAccess,
		onEmpty:      onEmpty,
		instances:    NewDirectory[*keyedInstance](false, reportError),
		bound:        bound,
	}
}

// Observe registers a handler for every live instance with a guarded handle.
func (b *keyedBinding) Observe(handler func(service *GuardedFacade, ctx context.Context) error) (func(), error) {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, fmt.Errorf("Remote keyed service binding is closed")
	}
	service := b.service
	b.mu.Unlock()

	var stoppedMu sync.Mutex
	stopped := false
	stop, err := b.instances.Observe(func(entry any, ctx context.Context) error {
		instance, ok := entry.(*keyedInstance)
		if !ok {
			return fmt.Errorf("Remote keyed service directory entry is invalid")
		}
		guarded := &GuardedFacade{
			facade: instance.facade,
			assertAccess: func() error {
				if err := b.assertAccess(); err != nil {
					return err
				}
				stoppedMu.Lock()
				isStopped := stopped
				stoppedMu.Unlock()
				if isStopped || ctx.Err() != nil {
					return &RemoteServiceError{
						Code:    "service_stale_instance",
						Message: fmt.Sprintf("Remote service %s observation is closed", service.ID),
					}
				}
				return nil
			},
		}
		return handler(guarded, ctx)
	})
	if err != nil {
		return nil, err
	}

	b.mu.Lock()
	bound := b.bound
	revision := b.revision
	var completion *completion
	if bound && b.starting == nil {
		completion = newCompletion()
		b.starting = completion
	}
	b.mu.Unlock()
	if completion != nil {
		go func() {
			err := b.start(revision)
			completion.finish()
			if err == nil {
				return
			}
			b.mu.Lock()
			report := !b.closed && b.revision == revision && b.bound
			b.mu.Unlock()
			if report {
				b.reportError(err)
			}
		}()
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
		if b.instances.ObserverCount() == 0 {
			b.onEmpty()
		}
	}, nil
}

// Rebind reconfigures the binding (upstream rebind).
func (b *keyedBinding) Rebind(bound bool, ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.bound = bound
	b.revision++
	revision := b.revision
	b.mu.Unlock()

	if err := b.reset(ctx, false); err != nil {
		return err
	}

	b.mu.Lock()
	skip := b.closed || b.revision != revision || b.bound != bound
	observers := b.instances.ObserverCount()
	b.mu.Unlock()
	if skip || !bound || observers == 0 {
		return nil
	}
	completion := newCompletion()
	b.mu.Lock()
	b.starting = completion
	b.mu.Unlock()
	err := b.start(revision)
	completion.finish()
	return err
}

// Ready resolves once the current start attempt settles.
func (b *keyedBinding) Ready() *completion {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.starting
}

// Close ends the binding (upstream close).
func (b *keyedBinding) Close(ctx context.Context) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.revision++
	b.mu.Unlock()
	if err := b.reset(ctx, true); err != nil {
		return err
	}
	b.instances.Dispose()
	return nil
}

func (b *keyedBinding) reset(ctx context.Context, waitForStarting bool) error {
	b.instances.Reset()
	b.mu.Lock()
	starting := b.starting
	b.starting = nil
	subscription := b.subscription
	b.subscription = nil
	b.mu.Unlock()

	var errs []error
	if waitForStarting && starting != nil {
		// Upstream swallows a start failure while resetting.
		_ = starting.wait(ctx)
	}
	if subscription != nil && subscription.Close != nil {
		if err := subscription.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return collectErrors(errs, "Failed to reset keyed service binding")
}

func (b *keyedBinding) start(revision int) error {
	b.mu.Lock()
	service := b.service
	b.mu.Unlock()

	subscription, err := b.transport.Subscribe(service.ID, chord.ServiceModeKeyed,
		func(update *ServiceProviderUpdate, ctx chord.Context) {
			b.mu.Lock()
			current := b.revision
			b.mu.Unlock()
			if current == revision {
				b.update(update, ctx)
			}
		}, context.Background())
	if err != nil {
		return err
	}

	b.mu.Lock()
	skip := b.closed || !b.bound || b.revision != revision
	if !skip {
		b.subscription = subscription
	}
	b.mu.Unlock()
	if skip {
		if subscription.Close != nil {
			_ = subscription.Close()
		}
		return nil
	}

	if subscription.Snapshot.Mode != chord.ServiceModeKeyed || subscription.Snapshot.ServiceID != service.ID {
		return fmt.Errorf("Remote service %s returned the wrong keyed snapshot", service.ID)
	}
	for _, snapshot := range subscription.Snapshot.Instances {
		b.spawn(snapshot, ServiceDeliveryContext())
	}
	if subscription.Activate != nil {
		subscription.Activate()
	}
	return b.instances.Ready()
}

func (b *keyedBinding) update(update *ServiceProviderUpdate, ctx chord.Context) {
	b.mu.Lock()
	closed := b.closed
	b.mu.Unlock()
	if closed {
		return
	}
	var err error
	switch update.Type {
	case UpdateUnavailable, UpdateReplaced:
		err = fmt.Errorf("Keyed service received a singleton lifecycle update")
	case UpdateSpawned:
		if update.SpawnedInstance == nil {
			err = fmt.Errorf("Keyed service instance snapshot has no address")
			break
		}
		err = b.spawn(*update.SpawnedInstance, ctx)
	case UpdateClosed:
		if update.ClosedInstance != nil {
			if instance, ok := b.instances.Get(update.ClosedInstance.Key); ok && instance.generation == update.ClosedInstance.Generation {
				b.instances.Remove(instance)
			}
		}
	case UpdateState:
		if update.Instance == nil {
			err = fmt.Errorf("Keyed state update has no instance address")
			break
		}
		instance, ok := b.instances.Get(update.Instance.Key)
		if !ok || instance.generation != update.Instance.Generation {
			return
		}
		err = instance.facade.UpdateState(update.Member, update.Sequence, update.Ops, ctx)
	}
	if err != nil {
		b.reportError(err)
	}
}

func (b *keyedBinding) spawn(snapshot ServiceInstanceSnapshot, ctx chord.Context) error {
	address := snapshot.Instance
	if address == nil {
		return fmt.Errorf("Keyed service instance snapshot has no address")
	}
	instance := &keyedInstance{key: address.Key, generation: address.Generation, active: true}
	instance.facade = newServiceFacade(
		b.service.ID,
		address,
		b.transport,
		instance.isActive,
		b.assertAccess,
		b.reportError,
	)
	if err := instance.facade.Install(snapshot, ctx); err != nil {
		return err
	}
	return b.instances.Replace(instance)
}

// RemoteServiceBinding consumes remote services (upstream
// RemoteServiceBindingImpl).
type RemoteServiceBinding struct {
	mu                sync.Mutex
	transport         RemoteServiceTransport
	allowlist         map[string]bool
	reportError       func(error)
	modes             map[string]chord.ServiceMode
	assertAccess      func() error
	singletons        map[string]*singletonBinding
	singletonOrder    []string
	keyed             map[string]*keyedBinding
	keyedOrder        []string
	bound             bool
	readinessRevision int
	bindingTransition *completion
	disposed          bool
}

// NewRemoteServiceBinding builds a binding over an allowlist and transport.
func NewRemoteServiceBinding(options RemoteServiceBindingOptions) (*RemoteServiceBinding, error) {
	allowlist := map[string]bool{}
	for _, service := range options.Services {
		if allowlist[service.ID] {
			return nil, fmt.Errorf("Remote service binding has duplicate service IDs")
		}
		allowlist[service.ID] = true
	}
	reportError := options.OnError
	if reportError == nil {
		reportError = func(error) {}
	}
	assertAccess := options.AssertAccess
	if assertAccess == nil {
		assertAccess = func() error { return nil }
	}
	bound := true
	if options.Bound != nil {
		bound = *options.Bound
	}
	return &RemoteServiceBinding{
		transport:    options.Transport,
		allowlist:    allowlist,
		reportError:  reportError,
		modes:        map[string]chord.ServiceMode{},
		assertAccess: assertAccess,
		singletons:   map[string]*singletonBinding{},
		keyed:        map[string]*keyedBinding{},
		bound:        bound,
	}, nil
}

// Use acquires the singleton facade for one service.
func (b *RemoteServiceBinding) Use(service chord.Service) (*ServiceFacade, error) {
	if err := b.assertAvailable(service, chord.ServiceModeSingleton); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if binding, ok := b.singletons[service.ID]; ok {
		return binding.facade, nil
	}
	binding := &singletonBinding{active: true}
	serviceID := service.ID
	binding.facade = newServiceFacade(
		serviceID,
		nil,
		b.transport,
		func() bool { return binding.active && !b.isDisposed() && b.isBound() },
		func() error { return b.assertHandleAccess() },
		b.reportError,
	)
	b.singletons[serviceID] = binding
	b.singletonOrder = append(b.singletonOrder, serviceID)
	b.readinessRevision++

	if b.bound {
		revision := binding.revision
		completion := newCompletion()
		binding.starting = completion
		go func() {
			err := b.startSingleton(serviceID, binding, revision)
			completion.finish()
			if err == nil {
				return
			}
			b.mu.Lock()
			report := binding.active && binding.revision == revision && !b.disposed && b.bound
			b.mu.Unlock()
			if report {
				b.reportError(err)
			}
		}()
	}
	return binding.facade, nil
}

// Observe registers a handler for every live keyed instance.
func (b *RemoteServiceBinding) Observe(service chord.Service, handler func(service *GuardedFacade, ctx context.Context) error) (func(), error) {
	if err := b.assertAvailable(service, chord.ServiceModeKeyed); err != nil {
		return nil, err
	}
	b.mu.Lock()
	binding, ok := b.keyed[service.ID]
	if !ok {
		serviceID := service.ID
		binding = newKeyedBinding(
			service,
			b.transport,
			b.reportError,
			func() error { return b.assertHandleAccess() },
			func() {
				b.mu.Lock()
				if b.keyed[serviceID] != binding {
					b.mu.Unlock()
					return
				}
				delete(b.keyed, serviceID)
				b.keyedOrder = removeString(b.keyedOrder, serviceID)
				b.readinessRevision++
				b.mu.Unlock()
				go func() { _ = binding.Close(context.Background()) }()
			},
			b.bound,
		)
		b.keyed[serviceID] = binding
		b.keyedOrder = append(b.keyedOrder, serviceID)
		b.readinessRevision++
	}
	b.mu.Unlock()
	return binding.Observe(handler)
}

// Ready waits until every currently acquired service has installed its initial
// snapshot.
func (b *RemoteServiceBinding) Ready(ctx context.Context) error {
	for {
		b.mu.Lock()
		if b.disposed {
			b.mu.Unlock()
			return fmt.Errorf("Remote service binding is disposed")
		}
		revision := b.readinessRevision
		waits := []*completion{}
		if b.bindingTransition != nil {
			waits = append(waits, b.bindingTransition)
		}
		for _, serviceID := range b.singletonOrder {
			if binding, ok := b.singletons[serviceID]; ok && binding.starting != nil {
				waits = append(waits, binding.starting)
			}
		}
		for _, serviceID := range b.keyedOrder {
			if binding, ok := b.keyed[serviceID]; ok {
				if starting := binding.Ready(); starting != nil {
					waits = append(waits, starting)
				}
			}
		}
		b.mu.Unlock()

		for _, wait := range waits {
			if err := wait.wait(ctx); err != nil {
				return err
			}
		}

		b.mu.Lock()
		if b.disposed {
			b.mu.Unlock()
			return fmt.Errorf("Remote service binding is disposed")
		}
		done := revision == b.readinessRevision
		b.mu.Unlock()
		if done {
			return nil
		}
	}
}

// Rebind attaches or detaches every acquired service.
func (b *RemoteServiceBinding) Rebind(bound bool, ctx context.Context) error {
	b.mu.Lock()
	if b.disposed {
		b.mu.Unlock()
		return fmt.Errorf("Remote service binding is disposed")
	}
	b.bound = bound
	b.readinessRevision++
	singletonIDs := append([]string{}, b.singletonOrder...)
	keyedIDs := append([]string{}, b.keyedOrder...)
	completion := newCompletion()
	b.bindingTransition = completion
	b.mu.Unlock()

	transitions := newCompletionGroup(len(singletonIDs) + len(keyedIDs))
	for _, serviceID := range singletonIDs {
		b.mu.Lock()
		binding, ok := b.singletons[serviceID]
		if !ok {
			b.mu.Unlock()
			transitions.finishOne(nil)
			continue
		}
		binding.revision++
		binding.facade.Clear()
		subscription := binding.subscription
		binding.subscription = nil
		revision := binding.revision
		start := newCompletion()
		binding.starting = start
		b.mu.Unlock()

		go func(serviceID string, binding *singletonBinding) {
			var errs []error
			if subscription != nil && subscription.Close != nil {
				if err := subscription.Close(); err != nil {
					errs = append(errs, err)
				}
			}
			if bound {
				if err := b.startSingleton(serviceID, binding, revision); err != nil {
					errs = append(errs, err)
				}
			}
			start.finish()
			transitions.finishOne(collectErrors(errs, "Failed to rebind service "+serviceID))
		}(serviceID, binding)
	}
	for _, serviceID := range keyedIDs {
		b.mu.Lock()
		binding, ok := b.keyed[serviceID]
		b.mu.Unlock()
		if !ok {
			transitions.finishOne(nil)
			continue
		}
		go func(binding *keyedBinding) {
			transitions.finishOne(binding.Rebind(bound, ctx))
		}(binding)
	}

	err := transitions.wait()
	completion.finish()
	return err
}

// Dispose releases every acquired service.
func (b *RemoteServiceBinding) Dispose(ctx context.Context) error {
	b.mu.Lock()
	if b.disposed {
		b.mu.Unlock()
		return nil
	}
	b.disposed = true
	singletonIDs := append([]string{}, b.singletonOrder...)
	keyedIDs := append([]string{}, b.keyedOrder...)
	singletons := b.singletons
	keyed := b.keyed
	b.singletons = map[string]*singletonBinding{}
	b.keyed = map[string]*keyedBinding{}
	b.singletonOrder = nil
	b.keyedOrder = nil
	b.mu.Unlock()

	group := newCompletionGroup(len(singletonIDs) + len(keyedIDs))
	for _, serviceID := range singletonIDs {
		b.mu.Lock()
		binding := singletons[serviceID]
		if binding == nil {
			b.mu.Unlock()
			group.finishOne(nil)
			continue
		}
		binding.active = false
		binding.facade.Clear()
		starting := binding.starting
		subscription := binding.subscription
		binding.subscription = nil
		b.mu.Unlock()
		go func() {
			var errs []error
			if starting != nil {
				// Upstream swallows a pending start failure while disposing.
				_ = starting.wait(ctx)
			}
			if subscription != nil && subscription.Close != nil {
				if err := subscription.Close(); err != nil {
					errs = append(errs, err)
				}
			}
			group.finishOne(collectErrors(errs, "Failed to dispose services"))
		}()
	}
	for _, serviceID := range keyedIDs {
		binding := keyed[serviceID]
		go func() { group.finishOne(binding.Close(ctx)) }()
	}
	return group.wait()
}

func (b *RemoteServiceBinding) startSingleton(serviceID string, binding *singletonBinding, revision int) error {
	subscription, err := b.transport.Subscribe(serviceID, chord.ServiceModeSingleton,
		func(update *ServiceProviderUpdate, ctx chord.Context) {
			b.mu.Lock()
			deliver := binding.active && binding.revision == revision
			b.mu.Unlock()
			if !deliver {
				return
			}
			var err error
			switch update.Type {
			case UpdateUnavailable:
				binding.facade.Clear()
			case UpdateReplaced:
				if update.Snapshot != nil && update.Snapshot.Instance != nil {
					err = fmt.Errorf("Singleton replacement has an instance address")
					break
				}
				if update.Snapshot != nil {
					err = binding.facade.Install(*update.Snapshot, ctx)
				}
			case UpdateState:
				if update.Instance == nil {
					err = binding.facade.UpdateState(update.Member, update.Sequence, update.Ops, ctx)
				}
			}
			if err != nil {
				b.reportError(err)
			}
		}, context.Background())
	if err != nil {
		return err
	}

	b.mu.Lock()
	skip := !binding.active || b.disposed || !b.bound || binding.revision != revision
	if !skip {
		binding.subscription = subscription
	}
	b.mu.Unlock()
	if skip {
		if subscription.Close != nil {
			_ = subscription.Close()
		}
		return nil
	}

	snapshot := subscription.Snapshot
	if snapshot == nil || snapshot.Mode != chord.ServiceModeSingleton || snapshot.ServiceID != serviceID || len(snapshot.Instances) != 1 {
		return fmt.Errorf("Remote service %s returned an invalid singleton snapshot", serviceID)
	}
	if err := binding.facade.Install(snapshot.Instances[0], ServiceDeliveryContext()); err != nil {
		return err
	}
	if subscription.Activate != nil {
		subscription.Activate()
	}
	return nil
}

func (b *RemoteServiceBinding) assertHandleAccess() error {
	b.mu.Lock()
	disposed := b.disposed
	b.mu.Unlock()
	if disposed {
		return fmt.Errorf("Remote service binding is disposed")
	}
	return b.assertAccess()
}

func (b *RemoteServiceBinding) assertAvailable(service chord.Service, mode chord.ServiceMode) error {
	if service.Local {
		return &RemoteServiceError{Code: "service_not_allowed", Message: fmt.Sprintf("Service %s is process-local", service.ID)}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.disposed {
		return fmt.Errorf("Remote service binding is disposed")
	}
	if !b.allowlist[service.ID] {
		return &RemoteServiceError{Code: "service_not_allowed", Message: fmt.Sprintf("Remote service %s is not allowlisted", service.ID)}
	}
	if existing, ok := b.modes[service.ID]; ok && existing != mode {
		return &RemoteServiceError{
			Code:    "service_mode_mismatch",
			Message: fmt.Sprintf("Remote service %s is already used as %s", service.ID, existing),
		}
	}
	b.modes[service.ID] = mode
	return nil
}

func (b *RemoteServiceBinding) isDisposed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.disposed
}

func (b *RemoteServiceBinding) isBound() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bound
}

// completion is a one-shot completion signal (upstream's promise).
type completion struct {
	once sync.Once
	done chan struct{}
}

func newCompletion() *completion {
	return &completion{done: make(chan struct{})}
}

func (c *completion) finish() { c.once.Do(func() { close(c.done) }) }

// wait blocks until the completion or the context deadline.
func (c *completion) wait(ctx context.Context) error {
	select {
	case <-c.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// completionGroup is an all-settled-style group of completions.
type completionGroup struct {
	mu     sync.Mutex
	done   chan struct{}
	errors []error
	left   int
}

func newCompletionGroup(count int) *completionGroup {
	group := &completionGroup{done: make(chan struct{})}
	group.left = count
	if count == 0 {
		close(group.done)
	}
	return group
}

func (g *completionGroup) finishOne(err error) {
	g.mu.Lock()
	if err != nil {
		g.errors = append(g.errors, err)
	}
	g.left--
	finished := g.left <= 0
	g.mu.Unlock()
	if finished {
		g.mu.Lock()
		select {
		case <-g.done:
		default:
			close(g.done)
		}
		g.mu.Unlock()
	}
}

func (g *completionGroup) wait() error {
	<-g.done
	g.mu.Lock()
	defer g.mu.Unlock()
	return collectErrors(g.errors, "Failed to dispose services")
}

func validateMembers(members []ServiceMemberSnapshot) (map[string]ServiceMemberSnapshot, error) {
	result := map[string]ServiceMemberSnapshot{}
	for _, member := range members {
		if member.Name == "" {
			return nil, fmt.Errorf("Remote service has invalid member descriptions")
		}
		if _, duplicate := result[member.Name]; duplicate {
			return nil, fmt.Errorf("Remote service has invalid member descriptions")
		}
		result[member.Name] = member
	}
	return result, nil
}

func removeString(values []string, target string) []string {
	kept := values[:0]
	for _, value := range values {
		if value != target {
			kept = append(kept, value)
		}
	}
	return kept
}

var _ = strings.Compare
