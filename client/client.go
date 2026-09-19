package client

import (
	"context"
	"fmt"
	"sync"

	"github.com/dat267/gpi/protocol"
)

// Port of src/client.ts. The chord-backed service subscription methods
// (serviceCatalogue, subscribeService, createClientServiceTransport) are
// deferred until the chord service wire subset is ported; the request/
// response, attachment, and connection-state surface is complete.

// Unsubscribe removes a listener.
type Unsubscribe func()

// ListenerErrorHandler reports subscriber failures without letting them
// corrupt client state.
type ListenerErrorHandler func(err error)

// AttachmentChangeListener receives attachment changes.
type AttachmentChangeListener func(attachment *protocol.RpcTarget)

// ClientOptions configure a client.
type ClientOptions struct {
	TransportFactory ByteTransportFactory
	// ServerID is the logical server identity expected at the endpoint.
	ServerID string
	// MaxFrameLength overrides the default frame limit.
	MaxFrameLength  *int64
	OnListenerError ListenerErrorHandler
}

type pendingRequest struct {
	resolve func(result any)
	reject  func(err error)
	cleanup func()
}

// Client is the transport-neutral client for remote pi sessions.
type Client struct {
	mu sync.Mutex

	options    ClientOptions
	connection *Connection

	pending      map[string]*pendingRequest
	stateWatches map[int]func(change ConnectionStateChange)
	attachments  map[int]AttachmentChangeListener
	watchSeq     int

	serviceListeners map[string]*activeServiceListener
	serviceSequence  int

	requestSequence int
	hello           *protocol.ServerHello
	attachment      *protocol.RpcTarget
	disposed        bool
}

// NewClient builds a client.
func NewClient(options ClientOptions) (*Client, error) {
	if !protocol.IsServerID(options.ServerID) {
		return nil, fmt.Errorf("serverId must be a canonical lowercase UUIDv4")
	}
	client := &Client{
		options:          options,
		pending:          map[string]*pendingRequest{},
		stateWatches:     map[int]func(change ConnectionStateChange){},
		attachments:      map[int]AttachmentChangeListener{},
		serviceListeners: map[string]*activeServiceListener{},
	}
	connection, err := NewConnection(ConnectionOptions{
		TransportFactory: options.TransportFactory,
		ServerID:         options.ServerID,
		MaxFrameLength:   options.MaxFrameLength,
		OnHandshake: func(hello *protocol.ServerHello) {
			client.mu.Lock()
			client.hello = hello
			client.mu.Unlock()
		},
		OnMessage: func(message *protocol.ServerMessage) { client.handleMessage(message) },
		OnStateChange: func(change ConnectionStateChange) {
			client.handleConnectionStateChange(change)
		},
	})
	if err != nil {
		return nil, err
	}
	client.connection = connection
	return client, nil
}

// ConnectClient builds a client and connects it, disposing on failure
// (upstream Client.connect static).
func ConnectClient(ctx context.Context, options ClientOptions) (*Client, error) {
	client, err := NewClient(options)
	if err != nil {
		return nil, err
	}
	if _, err := client.Connect(ctx); err != nil {
		_ = client.Dispose()
		return nil, err
	}
	return client, nil
}

// Disposed reports whether the client is disposed.
func (c *Client) Disposed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.disposed
}

// ConnectionState returns the connection state.
func (c *Client) ConnectionState() ConnectionState { return c.connection.State() }

// Connected reports whether the handshake completed.
func (c *Client) Connected() bool { return c.connection.State() == StateConnected }

// ServerID returns the expected server identity.
func (c *Client) ServerID() string { return c.options.ServerID }

// Hello returns the negotiated hello, if any.
func (c *Client) Hello() *protocol.ServerHello {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hello
}

// Attachment returns the current session attachment, if any.
func (c *Client) Attachment() *protocol.RpcTarget {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attachment
}

// Connect performs the handshake.
func (c *Client) Connect(ctx context.Context) (*protocol.ServerHello, error) {
	c.mu.Lock()
	if c.disposed {
		c.mu.Unlock()
		return nil, &ClientDisposedError{}
	}
	c.hello = nil
	c.mu.Unlock()
	return c.connection.Connect(ctx)
}

// Disconnect closes the connection.
func (c *Client) Disconnect(reason string) { c.connection.Disconnect(reason, nil) }

// OnConnectionStateChange registers a state listener.
func (c *Client) OnConnectionStateChange(listener func(change ConnectionStateChange)) (Unsubscribe, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disposed {
		return nil, &ClientDisposedError{}
	}
	c.watchSeq++
	id := c.watchSeq
	c.stateWatches[id] = listener
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.stateWatches, id)
	}, nil
}

// OnAttachmentChange registers an attachment listener.
func (c *Client) OnAttachmentChange(listener AttachmentChangeListener) (Unsubscribe, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disposed {
		return nil, &ClientDisposedError{}
	}
	c.watchSeq++
	id := c.watchSeq
	c.attachments[id] = listener
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.attachments, id)
	}, nil
}

// Request invokes one protocol call against an explicit routed target. The
// call is an opaque JSON value (chord service calls are the usual payload).
func (c *Client) Request(ctx context.Context, target protocol.RpcTarget, call any) (any, error) {
	return c.requestWithTransform(ctx, target, call, nil)
}

// requestWithTransform is Request with an optional result transform that runs
// before the caller sees the result (upstream's transform argument). A
// transform failure fails the connection, because the peer produced a payload
// that cannot be trusted.
func (c *Client) requestWithTransform(ctx context.Context, target protocol.RpcTarget, call any, transform func(result any) (any, error)) (any, error) {
	c.mu.Lock()
	if c.disposed {
		c.mu.Unlock()
		return nil, &ClientDisposedError{}
	}
	if c.connection.State() != StateConnected {
		c.mu.Unlock()
		return nil, NewDisconnectedError("", nil)
	}
	c.requestSequence++
	// Calls are structs with a wire rendering (chord service calls); other
	// callers pass plain JSON values directly.
	callValue := call
	if renderer, ok := call.(interface{ JSONValue() map[string]any }); ok {
		callValue = renderer.JSONValue()
	}
	id := fmt.Sprintf("request-%d", c.requestSequence)
	result := make(chan any, 1)
	failure := make(chan error, 1)
	c.pending[id] = &pendingRequest{
		resolve: func(value any) {
			if transform == nil {
				result <- value
				return
			}
			transformed, err := transform(value)
			if err != nil {
				validationError := &protocol.ProtocolValidationError{
					Message: fmt.Sprintf("Invalid service operation stream: %s", err.Error()),
				}
				c.connection.Fail(validationError)
				failure <- validationError
				return
			}
			result <- transformed
		},
		reject:  func(err error) { failure <- err },
		cleanup: func() {},
	}
	c.mu.Unlock()

	sendCancel := func() {
		c.mu.Lock()
		canSend := c.connection.State() == StateConnected
		c.mu.Unlock()
		if !canSend {
			return
		}
		frame, err := protocol.EncodeClientMessage(&protocol.ClientMessage{
			Type:   protocol.ClientMessageCancel,
			Cancel: &protocol.CancelEnvelope{ID: id, Target: target},
		}, &protocol.FrameDecoderOptions{MaxFrameLength: maxFrameLengthPtr(c.connection.MaxFrameLength())})
		if err != nil {
			c.connection.Fail(ToError(err))
			return
		}
		if err := c.connection.Send(frame); err != nil {
			c.connection.Fail(ToError(err))
		}
	}

	frame, err := protocol.EncodeClientMessage(&protocol.ClientMessage{
		Type: protocol.ClientMessageRequest,
		Request: &protocol.RequestEnvelope{
			ID:     id,
			Target: target,
			Call:   callValue,
		},
	}, &protocol.FrameDecoderOptions{MaxFrameLength: maxFrameLengthPtr(c.connection.MaxFrameLength())})
	if err != nil {
		if pending := c.takePendingRequest(id); pending != nil {
			pending.reject(ToError(err))
		}
	}
	if frame != nil {
		if err := c.connection.Send(frame); err != nil {
			if pending := c.takePendingRequest(id); pending != nil {
				pending.reject(ToError(err))
			}
		}
	}

	select {
	case value := <-result:
		return value, nil
	case err := <-failure:
		return nil, err
	case <-ctx.Done():
		if pending := c.takePendingRequest(id); pending != nil {
			sendCancel()
		}
		return nil, ctx.Err()
	}
}

func maxFrameLengthPtr(value int64) *int64 { return &value }

func (c *Client) handleMessage(message *protocol.ServerMessage) {
	switch message.Type {
	case protocol.ServerMessageAttachment:
		if message.Attachment.Attachment != nil && message.Attachment.Attachment.ServerID != c.options.ServerID {
			c.connection.Fail(&protocol.ProtocolValidationError{Message: "Attachment update belongs to another server"})
			return
		}
		c.setAttachment(message.Attachment.Attachment)
		return
	case protocol.ServerMessageServiceUpdate:
		c.handleServiceUpdate(message)
		return
	}

	pending := c.takePendingRequest(message.Response.ID)
	if pending == nil {
		c.connection.Fail(&protocol.ProtocolValidationError{Message: "Response has no matching request"})
		return
	}
	if !message.Response.OK {
		pending.reject(&ServerError{Code: message.Response.Error.Code, Message: message.Response.Error.Message})
		return
	}
	pending.resolve(message.Response.Result)
}

func (c *Client) handleConnectionStateChange(change ConnectionStateChange) {
	if change.State == StateDisconnected {
		c.mu.Lock()
		c.hello = nil
		watches := make([]func(change ConnectionStateChange), 0, len(c.stateWatches))
		for _, listener := range c.stateWatches {
			watches = append(watches, listener)
		}
		c.mu.Unlock()
		if change.Err != nil {
			c.rejectPendingRequests(change.Err)
		} else {
			c.rejectPendingRequests(NewDisconnectedError("", nil))
		}
		c.setAttachment(nil)
		for _, listener := range watches {
			c.safeListener(func() { listener(change) })
		}
		return
	}

	c.mu.Lock()
	watches := make([]func(change ConnectionStateChange), 0, len(c.stateWatches))
	for _, listener := range c.stateWatches {
		watches = append(watches, listener)
	}
	c.mu.Unlock()
	for _, listener := range watches {
		c.safeListener(func() { listener(change) })
	}
}

func (c *Client) takePendingRequest(id string) *pendingRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	request, ok := c.pending[id]
	if !ok {
		return nil
	}
	delete(c.pending, id)
	request.cleanup()
	return request
}

func (c *Client) rejectPendingRequests(err error) {
	c.mu.Lock()
	requests := make([]*pendingRequest, 0, len(c.pending))
	for _, request := range c.pending {
		requests = append(requests, request)
	}
	c.pending = map[string]*pendingRequest{}
	c.mu.Unlock()
	for _, request := range requests {
		request.cleanup()
		request.reject(err)
	}
}

// Dispose tears the client down.
func (c *Client) Dispose() error {
	c.mu.Lock()
	if c.disposed {
		c.mu.Unlock()
		return nil
	}
	c.disposed = true
	c.stateWatches = map[int]func(change ConnectionStateChange){}
	c.attachments = map[int]AttachmentChangeListener{}
	c.serviceListeners = map[string]*activeServiceListener{}
	c.hello = nil
	c.attachment = nil
	c.mu.Unlock()

	c.rejectPendingRequests(&ClientDisposedError{})
	c.connection.Disconnect("", &ClientDisposedError{})
	return nil
}

func (c *Client) setAttachment(attachment *protocol.RpcTarget) {
	c.mu.Lock()
	previous := c.attachment
	if sameTarget(previous, attachment) {
		c.mu.Unlock()
		return
	}
	c.attachment = attachment
	listeners := make([]AttachmentChangeListener, 0, len(c.attachments))
	for _, listener := range c.attachments {
		listeners = append(listeners, listener)
	}
	c.mu.Unlock()
	for _, listener := range listeners {
		c.safeListener(func() { listener(attachment) })
	}
}

func sameTarget(left, right *protocol.RpcTarget) bool {
	if left == nil && right == nil {
		return true
	}
	if left == nil || right == nil {
		return false
	}
	return left.ServerID == right.ServerID &&
		derefString(left.SessionID) == derefString(right.SessionID) &&
		derefString(left.AttachmentID) == derefString(right.AttachmentID)
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (c *Client) safeListener(fn func()) {
	defer func() {
		if recovered := recover(); recovered != nil {
			c.reportListenerError(ToError(panicError(recovered)))
		}
	}()
	fn()
}

func (c *Client) reportListenerError(err error) {
	if c.options.OnListenerError == nil {
		return
	}
	defer func() { _ = recover() }() // diagnostics cannot affect transport state
	c.options.OnListenerError(ToError(err))
}
