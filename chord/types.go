package chord

import (
	"context"
	"fmt"
	"strings"
)

// Port of the service portion of packages/chord/src/types.ts.

// Service is a stable service identity (upstream Service<T>).
//
// Go has no phantom type parameter for the contract, so the contract type is
// not represented; the id and process-locality are.
type Service struct {
	ID string `json:"id"`
	// Local marks a process-local service that is never published remotely.
	Local bool `json:"local"`
}

// DefineService declares a service identity (upstream defineService): the id
// must be non-empty and must not use the reserved $chord. namespace.
func DefineService(id string, local bool) (Service, error) {
	if id == "" {
		return Service{}, fmt.Errorf("Service ID must not be empty")
	}
	if strings.HasPrefix(id, "$chord.") {
		return Service{}, fmt.Errorf("Service IDs beginning with $chord. are reserved")
	}
	return Service{ID: id, Local: local}, nil
}

// ServiceMode is the replication mode of a service.
type ServiceMode = string

const (
	ServiceModeSingleton ServiceMode = "singleton"
	ServiceModeKeyed     ServiceMode = "keyed"
)

// ServiceInstanceAddress identifies one service instance.
type ServiceInstanceAddress struct {
	Key        string `json:"key"`
	Generation int    `json:"generation"`
}

// ServiceCall routes one RPC call.
type ServiceCall struct {
	ServiceID string                  `json:"serviceId"`
	Member    string                  `json:"member"`
	Instance  *ServiceInstanceAddress `json:"instance,omitempty"`
	Args      []JsonValue             `json:"args"`
}

// ServiceCatalogueEntry is one catalogue row.
type ServiceCatalogueEntry struct {
	ServiceID string      `json:"serviceId"`
	Mode      ServiceMode `json:"mode"`
}

// Context is the Chord invocation context (upstream Context).
//
// D14: upstream's chord Context is an ambient capability handle carrying typed
// values plus an AbortSignal; the Go port aliases context.Context, so abort
// handling, cancellation, and value propagation use the standard library
// (withAbortSignal/withCancel/awaitWithContext map to context.WithCancel and
// select-on-Done).
type Context = context.Context

// ReplicatedStateDelivery describes one replicated-state delivery.
type ReplicatedStateDelivery struct {
	Kind     string
	Sequence int
}

// Delivery kinds.
const (
	DeliveryHydrate = "hydrate"
	DeliveryUpdate  = "update"
)

// ReplicatedState is a read-only replicated value (upstream ReplicatedState).
type ReplicatedState[T any] interface {
	// Value is the current immutable value, absent until hydration.
	Value() (T, bool)
	// Subscribe registers a listener; the returned function unsubscribes.
	Subscribe(listener func(value T, ctx Context, delivery ReplicatedStateDelivery)) func()
}

// MutableReplicatedState is host-owned state that publishes diffs (upstream
// MutableReplicatedState).
type MutableReplicatedState[T any] interface {
	ReplicatedState[T]
	// Published is the immutable published value (upstream `value`).
	Published() T
	// State is the mutable tracked state; all writes must go through it.
	State() T
	// Publish emits the changes made through State since the last publication.
	Publish(ctx Context) error
}

// JSONValue renders a service call as a plain JSON value for the wire
// (upstream passes the call object itself, which is already JSON).
func (c ServiceCall) JSONValue() map[string]any {
	value := map[string]any{
		"serviceId": c.ServiceID,
		"member":    c.Member,
		"args":      c.Args,
	}
	if c.Instance != nil {
		value["instance"] = map[string]any{"key": c.Instance.Key, "generation": c.Instance.Generation}
	}
	return value
}
