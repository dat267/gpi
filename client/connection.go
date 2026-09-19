package client

import (
	"context"
	"fmt"
	"sync"

	"github.com/dat267/gpi/protocol"
)

// Port of src/connection.ts: the connection state machine.

// ConnectionState names the lifecycle states.
type ConnectionState = string

const (
	StateDisconnected ConnectionState = "disconnected"
	StateConnecting   ConnectionState = "connecting"
	StateConnected    ConnectionState = "connected"
)

// ConnectionStateChange reports a state transition.
type ConnectionStateChange struct {
	State ConnectionState
	Err   error
}

const maxUint32 = 0xffff_ffff

// ConnectionOptions configure a connection.
type ConnectionOptions struct {
	TransportFactory ByteTransportFactory
	// ServerID is the logical server identity expected at the endpoint.
	ServerID string
	// MaxFrameLength overrides the default frame limit (1..2^32-1).
	MaxFrameLength *int64
	OnHandshake    func(hello *protocol.ServerHello)
	OnMessage      func(message *protocol.ServerMessage)
	OnStateChange  func(change ConnectionStateChange)
}

// Connection owns one transport lifecycle with the protocol handshake.
type Connection struct {
	mu    sync.Mutex
	state ConnectionState

	options        ConnectionOptions
	maxFrameLength int64

	id       int
	sequence int

	decoder   *protocol.ServerMessageDecoder
	transport ByteTransport

	handshakeDone   chan struct{}
	handshakeHello  *protocol.ServerHello
	handshakeErr    error
	handshakeClosed bool
}

// NewConnection builds a connection.
func NewConnection(options ConnectionOptions) (*Connection, error) {
	maxFrameLength := int64(protocol.DefaultMaxFrameLength)
	if options.MaxFrameLength != nil {
		maxFrameLength = *options.MaxFrameLength
	}
	if maxFrameLength <= 0 || maxFrameLength > maxUint32 {
		return nil, fmt.Errorf("Client maxFrameLength must be an integer between 1 and %d", maxUint32)
	}
	return &Connection{
		state: StateDisconnected, options: options, maxFrameLength: maxFrameLength,
	}, nil
}

// State returns the current state.
func (c *Connection) State() ConnectionState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

// MaxFrameLength returns the configured frame limit.
func (c *Connection) MaxFrameLength() int64 { return c.maxFrameLength }

// Connect starts the transport and blocks until the handshake resolves.
func (c *Connection) Connect(ctx context.Context) (*protocol.ServerHello, error) {
	c.mu.Lock()
	if c.state != StateDisconnected {
		state := c.state
		c.mu.Unlock()
		return nil, NewDisconnectedError("Client is already "+state, nil)
	}
	c.sequence++
	id := c.sequence
	c.id = id
	decoder, err := protocol.NewServerMessageDecoder(&protocol.FrameDecoderOptions{MaxFrameLength: &c.maxFrameLength})
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	c.decoder = decoder
	c.state = StateConnecting
	done := make(chan struct{})
	c.handshakeDone = done
	c.handshakeHello = nil
	c.handshakeErr = nil
	c.handshakeClosed = false
	c.mu.Unlock()

	c.notifyState(ConnectionStateChange{State: StateConnecting})

	handlers := ByteTransportHandlers{
		OnData: func(chunk []byte) { c.handleData(id, chunk) },
		OnClose: func() {
			if c.isCurrent(id) {
				c.handleClose()
			}
		},
		OnError: func(err error) {
			if c.isCurrent(id) {
				c.failAndClose(ToDisconnectedError(err))
			}
		},
	}
	go c.openTransport(id, handlers)

	select {
	case <-done:
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.handshakeErr != nil {
			return nil, c.handshakeErr
		}
		return c.handshakeHello, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Disconnect closes the connection with a reason.
func (c *Connection) Disconnect(reason string, err error) {
	c.mu.Lock()
	if c.state == StateDisconnected {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	if err != nil {
		c.failAndClose(err)
		return
	}
	c.failAndClose(NewDisconnectedError(reason, nil))
}

// Fail moves the connection to the disconnected state, rejecting the
// handshake and announcing the change (upstream #fail).
func (c *Connection) Fail(err error) {
	c.mu.Lock()
	if c.state == StateDisconnected {
		c.mu.Unlock()
		return
	}
	c.state = StateDisconnected
	c.transport = nil
	done := c.handshakeDone
	shouldClose := false
	if done != nil && !c.handshakeClosed {
		c.handshakeClosed = true
		c.handshakeErr = err
		shouldClose = true
	}
	c.mu.Unlock()
	if shouldClose {
		close(done)
	}
	c.notifyState(ConnectionStateChange{State: StateDisconnected, Err: err})
}

// Send transmits a frame; errors fail the connection.
func (c *Connection) Send(frame []byte) error {
	c.mu.Lock()
	if c.state != StateConnected {
		c.mu.Unlock()
		return NewDisconnectedError("", nil)
	}
	transport := c.transport
	c.mu.Unlock()

	if err := transport.Send(frame); err != nil {
		c.mu.Lock()
		stillCurrent := c.state != StateDisconnected && c.transport == transport
		c.mu.Unlock()
		if stillCurrent {
			c.failAndClose(ToDisconnectedError(err))
		}
		return err
	}
	return nil
}

func (c *Connection) openTransport(id int, handlers ByteTransportHandlers) {
	transport, err := c.options.TransportFactory(handlers)
	if err != nil {
		if c.isCurrent(id) {
			c.Fail(ToDisconnectedError(err))
		}
		return
	}
	c.mu.Lock()
	if c.state != StateConnecting || c.id != id {
		c.mu.Unlock()
		transport.Close()
		return
	}
	c.transport = transport
	c.mu.Unlock()

	frame, encodeErr := protocol.EncodeClientMessage(&protocol.ClientMessage{
		Type:  protocol.ClientMessageHello,
		Hello: &protocol.ClientHello{Version: protocol.ProtocolVersion},
	}, &protocol.FrameDecoderOptions{MaxFrameLength: &c.maxFrameLength})
	if encodeErr != nil {
		if c.isCurrent(id) {
			c.failAndClose(ToDisconnectedError(encodeErr))
		}
		return
	}
	if err := transport.Send(frame); err != nil {
		if c.isCurrent(id) {
			c.failAndClose(ToDisconnectedError(err))
		}
	}
}

func (c *Connection) handleData(id int, chunk []byte) {
	c.mu.Lock()
	if c.state == StateDisconnected || c.id != id {
		c.mu.Unlock()
		return
	}
	if c.state == StateConnecting && c.transport == nil {
		c.mu.Unlock()
		c.failAndClose(&protocol.ProtocolValidationError{
			Message: "Received server data before the client hello was sent",
		})
		return
	}
	decoder := c.decoder
	c.mu.Unlock()

	messages, err := decoder.Push(chunk)
	if err != nil {
		c.failAndClose(ToError(err))
		return
	}
	for _, message := range messages {
		c.mu.Lock()
		disconnected := c.state == StateDisconnected
		c.mu.Unlock()
		if disconnected {
			return
		}
		c.handleMessage(message)
	}
}

func (c *Connection) handleMessage(message *protocol.ServerMessage) {
	c.mu.Lock()
	state := c.state

	if state == StateConnecting {
		if message.Type == protocol.ServerMessageHelloError {
			c.mu.Unlock()
			c.failAndClose(&ServerError{Code: message.HelloError.Error.Code, Message: message.HelloError.Error.Message})
			return
		}
		if message.Type != protocol.ServerMessageHello {
			c.mu.Unlock()
			c.failAndClose(&protocol.ProtocolValidationError{Message: "Expected server hello as first message"})
			return
		}
		if message.Hello.ServerID != c.options.ServerID {
			expected := c.options.ServerID
			c.mu.Unlock()
			c.failAndClose(&protocol.ProtocolValidationError{
				Message: fmt.Sprintf("Connected server %q does not match %q", message.Hello.ServerID, expected),
			})
			return
		}
		if c.transport == nil {
			c.mu.Unlock()
			c.failAndClose(&protocol.ProtocolValidationError{
				Message: "Received server hello before the client hello was sent",
			})
			return
		}
		// Transition to connected.
		connectedTransport := c.transport
		c.state = StateConnected
		hello := message.Hello
		c.mu.Unlock()

		if c.options.OnHandshake != nil {
			func() {
				defer func() {
					if r := recover(); r != nil {
						c.failIfConnected(connectedTransport, ToError(panicError(r)))
					}
				}()
				c.options.OnHandshake(hello)
			}()
		}
		c.mu.Lock()
		if c.state != StateConnected || c.transport != connectedTransport {
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()

		c.notifyState(ConnectionStateChange{State: StateConnected})

		c.mu.Lock()
		if c.state != StateConnected || c.transport != connectedTransport {
			c.mu.Unlock()
			return
		}
		c.resolveHandshake(hello)
		c.mu.Unlock()
		return
	}

	if state != StateConnected {
		c.mu.Unlock()
		return
	}
	if message.Type == protocol.ServerMessageHello || message.Type == protocol.ServerMessageHelloError {
		c.mu.Unlock()
		c.failAndClose(&protocol.ProtocolValidationError{Message: "Unexpected handshake message"})
		return
	}
	c.mu.Unlock()
	if c.options.OnMessage != nil {
		c.options.OnMessage(message)
	}
}

func (c *Connection) handleClose() {
	c.mu.Lock()
	if c.state == StateDisconnected {
		c.mu.Unlock()
		return
	}
	decoder := c.decoder
	c.mu.Unlock()

	var err error = NewDisconnectedError("Byte transport closed", nil)
	if decoder != nil {
		if decoderErr := decoder.End(); decoderErr != nil {
			err = ToError(decoderErr)
		}
	}
	c.Fail(err)
}

func (c *Connection) failAndClose(err error) {
	c.mu.Lock()
	var transport ByteTransport
	if c.state != StateDisconnected {
		transport = c.transport
	}
	c.mu.Unlock()
	c.Fail(err)
	if transport != nil {
		transport.Close()
	}
}

// failIfConnected fails only when the transport is still the current one.
func (c *Connection) failIfConnected(transport ByteTransport, err error) {
	c.mu.Lock()
	current := c.state != StateDisconnected && c.transport == transport
	c.mu.Unlock()
	if current {
		c.failAndClose(err)
	}
}

func (c *Connection) resolveHandshake(hello *protocol.ServerHello) {
	if c.handshakeClosed {
		return
	}
	c.handshakeClosed = true
	c.handshakeHello = hello
	if c.handshakeDone != nil {
		close(c.handshakeDone)
	}
}

func (c *Connection) notifyState(change ConnectionStateChange) {
	if c.options.OnStateChange != nil {
		c.options.OnStateChange(change)
	}
}

func (c *Connection) isCurrent(id int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state != StateDisconnected && c.id == id
}

func panicError(recovered any) error {
	if err, ok := recovered.(error); ok {
		return err
	}
	return fmt.Errorf("%v", recovered)
}
