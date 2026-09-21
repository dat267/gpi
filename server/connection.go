package server

import (
	"context"
	"sync"

	"github.com/dat267/pier/chord/services"
	"github.com/dat267/pier/protocol"
)

// Port of src/connection.ts and src/listener.ts.

// ByteConnection is an established, authorized ordered byte connection.
type ByteConnection interface {
	// Closed reports whether the connection is closed.
	Closed() bool
	Send(chunk []byte) error
	// Close closes the connection, optionally sending a final chunk first.
	Close(finalChunk []byte) error
}

// ByteConnectionHandler receives connection events.
type ByteConnectionHandler struct {
	OnData  func(chunk []byte)
	OnClose func()
	OnError func(err error)
}

// ByteConnectionAcceptor accepts an established connection and returns its
// handlers.
type ByteConnectionAcceptor func(connection ByteConnection) ByteConnectionHandler

// ConnectionStage names the connection lifecycle.
type ConnectionStage = string

const (
	StageAwaitingHello ConnectionStage = "awaitingHello"
	StageHandshaking   ConnectionStage = "handshaking"
	StageReady         ConnectionStage = "ready"
	StageClosing       ConnectionStage = "closing"
	StageClosed        ConnectionStage = "closed"
)

// activeRequest is one in-flight RPC.
type activeRequest struct {
	cancel context.CancelFunc
	target protocol.RpcTarget
}

// ConnectionState is one connection's server-side state.
type ConnectionState struct {
	mu sync.Mutex

	Connection ByteConnection
	Decoder    *protocol.ClientMessageDecoder
	// ServiceStateEncoders holds per-subscription state encoders.
	ServiceStateEncoders map[string]services.ServiceStateEncoder
	Stage                ConnectionStage
	Disconnected         bool
	// HandshakeTimeout cancels the handshake timer.
	HandshakeTimeout func()
	// handshakeDone is closed when the handshake attempt settles; later
	// messages wait on it (the Go equivalent of upstream's handshake promise).
	handshakeDone  chan struct{}
	ServerServices RoutedServerServiceAttachment
	ActiveRequests map[string]*activeRequest
}

// IsTerminalConnection reports whether a connection can no longer carry work.
func IsTerminalConnection(state *ConnectionState) bool {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.Disconnected || state.Stage == StageClosing || state.Stage == StageClosed
}

// ServerListener supplies established byte connections after any required
// transport authentication.
type ServerListener interface {
	// Start listens and passes authorized connections to accept. It blocks
	// until listening has begun.
	Start(accept ByteConnectionAcceptor) error
	Close() error
}
