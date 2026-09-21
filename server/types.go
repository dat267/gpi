// Package server is a Go port of @earendil-works/pi-server
// (pi/packages/server): the framed-CBOR server for remote pi sessions.
//
// Ground truth: pi/packages/server/src at the pinned upstream commit. The
// unix-socket transport and the testing helpers land with the transport round.
//
// D-row D15: upstream threads an ambient chord Context and uses promise chains
// for ordering; the Go port threads context.Context and uses per-connection
// pumps plus per-client operation serialization for the same ordering
// guarantees. MaybePromise collapses to blocking calls.
package server

import (
	"context"

	"github.com/dat267/pier/chord"
	"github.com/dat267/pier/chord/services"
)

// SessionMetadata identifies one durable session (port of pi-agent-core's
// SessionMetadata).
type SessionMetadata struct {
	ID string `json:"id"`
	// CreatedAt is a Unix timestamp in milliseconds.
	CreatedAt int64 `json:"createdAt"`
	// StorageVersion is the durable storage schema version.
	StorageVersion          int    `json:"storageVersion"`
	Cwd                     string `json:"cwd,omitempty"`
	ParentSessionID         string `json:"parentSessionId,omitempty"`
	LegacyParentSessionPath string `json:"legacyParentSessionPath,omitempty"`
}

// ServerOptions configure a server.
type ServerOptions struct {
	Listeners []ServerListener
	// ServerID is the stable logical server identity supplied by the
	// installation or profile.
	ServerID string
	// MaxFrameLength overrides the default frame limit.
	MaxFrameLength *int64
	// HandshakeTimeoutMS overrides the default handshake timeout.
	HandshakeTimeoutMS *int
	// OnConnectionCountChanged observes the live connection count.
	OnConnectionCountChanged func(count int)
	// OnError observes server errors.
	OnError func(err error)
}

// PublishFunc publishes one service update to a connection.
type PublishFunc func(subscriptionID string, update *services.ServiceProviderUpdate, ctx context.Context) error

// RoutedSessionAttachment is one presentation connection's live capability for
// a hosted session.
type RoutedSessionAttachment interface {
	// InvokeService routes one contract-agnostic service operation to the
	// attached session endpoint.
	InvokeService(call chord.ServiceCall, publish PublishFunc, ctx context.Context) (chord.JsonValue, error)
	Release(ctx context.Context) error
}

// RoutedServerPresentation is the presentation-scoped routing capability
// available to server service implementations.
type RoutedServerPresentation interface {
	AttachSession(ctx context.Context, sessionID string) error
	DetachSession(ctx context.Context) error
	// PrepareSessionRemoval releases routed attachments and handles before the
	// application deletes durable metadata.
	PrepareSessionRemoval(ctx context.Context, sessionID string) error
}

// RoutedServerServiceAttachment is one connection's server-scoped service
// endpoint.
type RoutedServerServiceAttachment interface {
	InvokeService(call chord.ServiceCall, publish PublishFunc, ctx context.Context) (chord.JsonValue, error)
	Release(ctx context.Context) error
}

// RoutedServerServiceHost attaches server-scoped service endpoints to
// presentations.
type RoutedServerServiceHost interface {
	AttachClient(ctx context.Context, presentation RoutedServerPresentation) (RoutedServerServiceAttachment, error)
}

// RoutedSessionHandle is a process-safe handle that acquires
// presentation-scoped session capabilities.
type RoutedSessionHandle interface {
	AttachClient(ctx context.Context) (RoutedSessionAttachment, error)
	// Terminated resolves with an error for unexpected termination, or nil
	// after an expected close.
	Terminated() <-chan error
	Close(ctx context.Context) error
}

// ServerHost is the application capability surface used by server-wide
// management and session routing.
type ServerHost interface {
	ServerServices() RoutedServerServiceHost
	// ResolveSession resolves one durable session id or returns a bounded
	// routing error.
	ResolveSession(ctx context.Context, sessionID string) (SessionMetadata, error)
	OpenSession(ctx context.Context, metadata SessionMetadata) (RoutedSessionHandle, error)
}
