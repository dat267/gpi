package services

import (
	"context"
	"fmt"
	"sync"

	"github.com/dat267/gpi/chord"
	"github.com/dat267/gpi/chord/delta"
)

// Port of src/services/handle.ts plus the local (in-process) service targets
// the facet host binds into slots.

// SlotTarget is the bound target of a service slot: something that can resolve
// member handles. Both a consumer facade and a guarded facade satisfy it, so
// local implementations and remote facades share one slot implementation.
type SlotTarget interface {
	Member(name string) *RemoteMember
	ServiceID() string
}

// GuardMember returns a handle that shares the member's state but runs an
// additional access guard first (upstream's guarded slot views).
func GuardMember(member *RemoteMember, assertAccess func() error) *RemoteMember {
	guarded := *member
	inner := member.assertAccess
	guarded.assertAccess = func() error {
		if assertAccess != nil {
			if err := assertAccess(); err != nil {
				return err
			}
		}
		return inner()
	}
	return &guarded
}

// NewGuardedFacade wraps a facade with an extra access guard.
func NewGuardedFacade(facade *ServiceFacade, assertAccess func() error) *GuardedFacade {
	if assertAccess == nil {
		assertAccess = func() error { return nil }
	}
	return &GuardedFacade{facade: facade, assertAccess: assertAccess}
}

// NewMemberFacade builds an in-process facade over a declared member set
// (upstream resolves members with Reflect on the implementation object; Go
// resolves them from the declared members, see D18).
//
// State members are exposed as live replicas fed by the mutable state's op
// stream, so a local consumer observes the same values a remote consumer would.
func NewMemberFacade(
	serviceID string,
	address *chord.ServiceInstanceAddress,
	implementation any,
	members Members,
	reportError func(error),
) (*ServiceFacade, error) {
	if reportError == nil {
		reportError = func(error) {}
	}
	facade := newServiceFacade(serviceID, address,
		&memberTransport{implementation: implementation, members: members},
		func() bool { return true },
		func() error { return nil },
		reportError)

	for name, source := range members.States {
		member := facade.Member(name)
		if err := member.Describe(MemberState); err != nil {
			return nil, err
		}
		base := []delta.Op{{Verb: delta.VerbReplace, Path: delta.Path{}, Value: source.Published()}}
		if err := member.Hydrate(source.Sequence(), base, ServiceDeliveryContext()); err != nil {
			return nil, err
		}
		stateName := name
		source.SubscribeOps(func(ops []delta.Op, sequence int, ctx context.Context) error {
			return facade.Member(stateName).UpdateState(sequence, ops, ctx)
		})
	}
	for name := range members.Methods {
		if err := facade.Member(name).Describe(MemberMethod); err != nil {
			return nil, err
		}
	}
	return facade, nil
}

// memberTransport invokes declared members in process.
type memberTransport struct {
	implementation any
	members        Members
}

func (t *memberTransport) Invoke(call chord.ServiceCall, ctx chord.Context) (chord.JsonValue, error) {
	method, ok := t.members.Methods[call.Member]
	if !ok {
		if _, isState := t.members.States[call.Member]; isState {
			return nil, &RemoteServiceError{
				Code:    "service_member_mismatch",
				Message: fmt.Sprintf("Remote service member %s.%s is not a method", call.ServiceID, call.Member),
			}
		}
		return nil, &RemoteServiceError{
			Code:    "service_member_not_found",
			Message: fmt.Sprintf("Unknown remote service member %s.%s", call.ServiceID, call.Member),
		}
	}
	return method(call.Args, ctx)
}

func (t *memberTransport) Subscribe(
	serviceID string,
	mode chord.ServiceMode,
	listener func(update *ServiceProviderUpdate, ctx chord.Context),
	ctx chord.Context,
) (*ServiceSubscription, error) {
	return nil, fmt.Errorf("Local service %s does not support remote subscriptions", serviceID)
}

// Slot is a host-owned mutable target with consumer-owned guarded views
// (upstream ServiceSlot).
type Slot struct {
	mu        sync.Mutex
	serviceID string
	target    SlotTarget
}

// NewSlot builds an unbound slot for one service id.
func NewSlot(serviceID string) *Slot {
	return &Slot{serviceID: serviceID}
}

// ServiceID is the slot's service id.
func (s *Slot) ServiceID() string { return s.serviceID }

// Bind points the slot at a target.
func (s *Slot) Bind(target SlotTarget) {
	s.mu.Lock()
	s.target = target
	s.mu.Unlock()
}

// Unbind disconnects the slot.
func (s *Slot) Unbind() {
	s.mu.Lock()
	s.target = nil
	s.mu.Unlock()
}

// Resolve returns the bound target or the disconnected error.
func (s *Slot) Resolve() (SlotTarget, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.target == nil {
		return nil, fmt.Errorf("Service %s is disconnected", s.serviceID)
	}
	return s.target, nil
}

// View returns a guarded view of the slot. The view resolves the bound target
// at access time, so binding after the view was created works (upstream's
// ServiceView resolves through the slot on every property access).
func (s *Slot) View(assertAccess func() error) *SlotView {
	return &SlotView{slot: s, assertAccess: assertAccess}
}

// SlotView is a consumer-owned guarded view of a host-owned slot.
type SlotView struct {
	slot         *Slot
	assertAccess func() error
}

// ServiceID is the underlying slot's service id.
func (v *SlotView) ServiceID() string { return v.slot.serviceID }

// Member resolves one member handle from the currently bound target. An
// unbound slot yields a handle that fails on use, matching upstream's
// disconnected-target error.
func (v *SlotView) Member(name string) *RemoteMember {
	target, err := v.slot.Resolve()
	if err != nil {
		return disconnectedMember(v.slot.serviceID, name, err)
	}
	member, err := v.memberOf(target, name)
	if err != nil {
		return disconnectedMember(v.slot.serviceID, name, err)
	}
	return member
}

func (v *SlotView) memberOf(target SlotTarget, name string) (*RemoteMember, error) {
	member := target.Member(name)
	if member == nil {
		return nil, fmt.Errorf("Service %s has no member %s", v.slot.serviceID, name)
	}
	return GuardMember(member, v.assertAccess), nil
}

// disconnectedMember builds a handle whose every use fails with the given
// access error.
func disconnectedMember(serviceID, name string, cause error) *RemoteMember {
	fail := func() error { return cause }
	return &RemoteMember{
		state:        &memberState{replica: NewStateReplica(nil)},
		serviceID:    serviceID,
		name:         name,
		invoke:       func([]chord.JsonValue, chord.Context) (chord.JsonValue, error) { return nil, cause },
		isActive:     func() bool { return false },
		assertAccess: fail,
	}
}
