// Package services ports the chord service layer
// (pi/packages/chord/src/services): the wire vocabulary, its strict
// validation, the replicated-state codec, and the remote-service errors.
//
// Ground truth: pi/packages/chord/src/services at the pinned upstream commit.
//
// NOTE on layout: upstream declares the op-carrying service types in
// chord/types.ts (the package root), but delta imports the root for JsonValue,
// so Go would have an import cycle. The op-free service types live in the
// chord root and the op-carrying snapshot/update types live here.
package services

import (
	"github.com/dat267/gpi/chord"
	"github.com/dat267/gpi/chord/delta"
)

// Port of src/services/errors.ts.

// RemoteServiceErrorCodes are the codes a remote service call can fail with.
var RemoteServiceErrorCodes = []string{
	"service_not_allowed",
	"service_not_found",
	"service_mode_mismatch",
	"service_member_not_found",
	"service_member_mismatch",
	"service_instance_not_found",
	"service_stale_instance",
	"service_invalid_value",
}

// IsRemoteServiceErrorCode reports whether a value is a known code.
func IsRemoteServiceErrorCode(value any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	for _, code := range RemoteServiceErrorCodes {
		if code == text {
			return true
		}
	}
	return false
}

// RemoteServiceError is a coded remote-service failure.
type RemoteServiceError struct {
	Code    string
	Message string
}

func (e *RemoteServiceError) Error() string { return e.Message }

// Port of the op-carrying types from src/types.ts.

// Member kinds.
const (
	MemberMethod = "method"
	MemberState  = "state"
)

// Provider update kinds.
const (
	UpdateState       = "state"
	UpdateUnavailable = "unavailable"
	UpdateReplaced    = "replaced"
	UpdateSpawned     = "spawned"
	UpdateClosed      = "closed"
)

// ServiceMemberSnapshot is one member in a decoded snapshot.
type ServiceMemberSnapshot struct {
	Name string
	Kind string
	// Sequence and Ops are set for state members.
	Sequence int
	Ops      []delta.Op
}

// ServiceInstanceSnapshot is one instance in a decoded snapshot.
type ServiceInstanceSnapshot struct {
	Instance *chord.ServiceInstanceAddress
	Members  []ServiceMemberSnapshot
}

// ServiceSubscriptionSnapshot is the decoded subscription snapshot.
type ServiceSubscriptionSnapshot struct {
	ServiceID string
	Mode      chord.ServiceMode
	Instances []ServiceInstanceSnapshot
}

// ServiceProviderUpdate is one decoded provider update (a tagged union).
type ServiceProviderUpdate struct {
	Type string

	// state
	Instance *chord.ServiceInstanceAddress
	Member   string
	Sequence int
	Ops      []delta.Op

	// replaced: the replacement snapshot; spawned: the instance that appeared.
	Snapshot        *ServiceInstanceSnapshot
	SpawnedInstance *ServiceInstanceSnapshot

	// closed
	ClosedInstance *chord.ServiceInstanceAddress
}

// WireServiceMemberSnapshot is one member in a wire snapshot.
type WireServiceMemberSnapshot struct {
	Name     string
	Kind     string
	Sequence int
	Ops      []delta.WireOp
}

// WireServiceInstanceSnapshot is one instance in a wire snapshot.
type WireServiceInstanceSnapshot struct {
	Instance *chord.ServiceInstanceAddress
	Members  []WireServiceMemberSnapshot
}

// WireServiceSubscriptionSnapshot is the wire subscription snapshot.
type WireServiceSubscriptionSnapshot struct {
	ServiceID string
	Mode      chord.ServiceMode
	Instances []WireServiceInstanceSnapshot
}

// WireServiceProviderUpdate is one wire provider update.
type WireServiceProviderUpdate struct {
	Type string

	Instance *chord.ServiceInstanceAddress
	Member   string
	Sequence int
	Ops      []delta.WireOp

	Snapshot        *WireServiceInstanceSnapshot
	SpawnedInstance *WireServiceInstanceSnapshot

	ClosedInstance *chord.ServiceInstanceAddress
}

// ServiceStateDecoder decodes every replicated state in one subscription.
type ServiceStateDecoder interface {
	DecodeSnapshot(snapshot *WireServiceSubscriptionSnapshot) (*ServiceSubscriptionSnapshot, error)
	DecodeUpdate(update *WireServiceProviderUpdate) (*ServiceProviderUpdate, error)
}

// ServiceStateEncoder encodes every replicated state in one subscription.
type ServiceStateEncoder interface {
	EncodeSnapshot(snapshot *ServiceSubscriptionSnapshot) (*WireServiceSubscriptionSnapshot, error)
	EncodeUpdate(update *ServiceProviderUpdate) (*WireServiceProviderUpdate, error)
}

// RemoteServiceTransport is the client-side transport a Chord service facade
// drives (upstream RemoteServiceTransport).
type RemoteServiceTransport interface {
	Invoke(call chord.ServiceCall, ctx chord.Context) (chord.JsonValue, error)
	Subscribe(
		serviceID string,
		mode chord.ServiceMode,
		listener func(update *ServiceProviderUpdate, ctx chord.Context),
		ctx chord.Context,
	) (*ServiceSubscription, error)
}

// ServiceSubscription is one live remote subscription handed back to the
// service facade.
type ServiceSubscription struct {
	Snapshot *ServiceSubscriptionSnapshot
	// Activate begins ordered update delivery after the snapshot is installed.
	Activate func()
	// Close ends the subscription.
	Close func() error
}

// ServiceCallValue renders a chord service call as the plain JSON value the
// protocol codec requires (Go structs are not JSON values).
func ServiceCallValue(call chord.ServiceCall) any { return call.JSONValue() }
