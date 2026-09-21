package testing

import (
	"fmt"
	"net"
	"sync"

	"github.com/dat267/pier/chord"
	"github.com/dat267/pier/chord/services"
	"github.com/dat267/pier/protocol"
)

// WireChannel is the byte transport a ProtocolTestClient writes to.
type WireChannel interface {
	Send(chunk []byte) error
	// SendFragmented writes one payload in two chunks, exercising the decoder.
	SendFragmented(chunk []byte, splitAt int) error
	Close() error
}

type messageWaiter struct {
	predicate func(message *protocol.ServerMessage) bool
	resolve   chan *protocol.ServerMessage
	reject    chan error
}

// ProtocolTestClient speaks the framed protocol and records every server
// message (upstream ProtocolTestClient).
type ProtocolTestClient struct {
	mu       sync.Mutex
	messages []*protocol.ServerMessage
	channel  WireChannel
	decoder  *protocol.ServerMessageDecoder
	waiters  []*messageWaiter

	closedOnce sync.Once
	closedCh   chan struct{}
	closed     bool

	requestSequence int
	attachment      *attachmentRef
}

type attachmentRef struct {
	sessionID    string
	attachmentID string
}

// NewProtocolTestClient builds a client over a wire channel.
func NewProtocolTestClient(channel WireChannel) (*ProtocolTestClient, error) {
	decoder, err := protocol.NewServerMessageDecoder(nil)
	if err != nil {
		return nil, err
	}
	return &ProtocolTestClient{
		channel:  channel,
		decoder:  decoder,
		closedCh: make(chan struct{}),
	}, nil
}

// Closed reports whether the wire connection closed.
func (c *ProtocolTestClient) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Messages returns a snapshot of every received server message.
func (c *ProtocolTestClient) Messages() []*protocol.ServerMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*protocol.ServerMessage{}, c.messages...)
}

// Hello sends hello and resolves with hello or hello_error.
func (c *ProtocolTestClient) Hello(version int64) (*protocol.ServerMessage, error) {
	waiter, err := c.prepareNext(func(message *protocol.ServerMessage) bool {
		return message.Type == protocol.ServerMessageHello || message.Type == protocol.ServerMessageHelloError
	})
	if err != nil {
		return nil, err
	}
	if err := c.SendMessage(&protocol.ClientMessage{
		Type:  protocol.ClientMessageHello,
		Hello: &protocol.ClientHello{Version: version},
	}); err != nil {
		return nil, err
	}
	return waiter()
}

// RequestService sends one service call and resolves with its response.
func (c *ProtocolTestClient) RequestService(target protocol.RpcTarget, call chord.ServiceCall, id string) (*protocol.ResponseEnvelope, error) {
	if id == "" {
		c.mu.Lock()
		c.requestSequence++
		id = fmt.Sprintf("request-%d", c.requestSequence)
		c.mu.Unlock()
	}
	waiter, err := c.prepareNext(func(message *protocol.ServerMessage) bool {
		return message.Type == protocol.ServerMessageResponse && message.Response != nil && message.Response.ID == id
	})
	if err != nil {
		return nil, err
	}
	if err := c.SendMessage(&protocol.ClientMessage{
		Type:    protocol.ClientMessageRequest,
		Request: &protocol.RequestEnvelope{ID: id, Target: target, Call: services.ServiceCallValue(call)},
	}); err != nil {
		return nil, err
	}
	message, err := waiter()
	if err != nil {
		return nil, err
	}
	return message.Response, nil
}

// Attach attaches the client to a session through pi.session-management.
func (c *ProtocolTestClient) Attach(serverID, sessionID string) (*protocol.ResponseEnvelope, error) {
	return c.RequestService(
		protocol.RpcTarget{ServerID: serverID},
		chord.ServiceCall{ServiceID: "pi.session-management", Member: "attach", Args: []chord.JsonValue{sessionID}},
		"",
	)
}

// RequestSessionService targets the client's current attachment for a session,
// or a deliberately missing attachment id otherwise.
func (c *ProtocolTestClient) RequestSessionService(serverID, sessionID string, call chord.ServiceCall, id string) (*protocol.ResponseEnvelope, error) {
	c.mu.Lock()
	attachment := c.attachment
	c.mu.Unlock()
	var target protocol.RpcTarget
	if attachment == nil || attachment.sessionID != sessionID {
		missing := "missing-attachment"
		target = protocol.RpcTarget{ServerID: serverID, SessionID: &sessionID, AttachmentID: &missing}
	} else {
		target = protocol.RpcTarget{ServerID: serverID, SessionID: &sessionID, AttachmentID: &attachment.attachmentID}
	}
	return c.RequestService(target, call, id)
}

// SendMessage encodes and sends one client message.
func (c *ProtocolTestClient) SendMessage(message *protocol.ClientMessage) error {
	frame, err := protocol.EncodeClientMessage(message, nil)
	if err != nil {
		return err
	}
	return c.channel.Send(frame)
}

// SendBytes sends raw bytes.
func (c *ProtocolTestClient) SendBytes(chunk []byte) error { return c.channel.Send(chunk) }

// SendFragmentedMessage splits one encoded message at splitAt.
func (c *ProtocolTestClient) SendFragmentedMessage(message *protocol.ClientMessage, splitAt int) error {
	frame, err := protocol.EncodeClientMessage(message, nil)
	if err != nil {
		return err
	}
	return c.channel.SendFragmented(frame, splitAt)
}

// prepareNext registers the waiter before the request is sent, mirroring
// upstream's `const response = this.next(...); void this.sendMessage(...)`
// ordering that avoids missing a fast response.
func (c *ProtocolTestClient) prepareNext(predicate func(message *protocol.ServerMessage) bool) (func() (*protocol.ServerMessage, error), error) {
	c.mu.Lock()
	for _, message := range c.messages {
		if predicate(message) {
			c.mu.Unlock()
			return func() (*protocol.ServerMessage, error) { return message, nil }, nil
		}
	}
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("Wire client is closed")
	}
	resolve := make(chan *protocol.ServerMessage, 1)
	reject := make(chan error, 1)
	waiter := &messageWaiter{predicate: predicate, resolve: resolve, reject: reject}
	c.waiters = append(c.waiters, waiter)
	c.mu.Unlock()
	return func() (*protocol.ServerMessage, error) {
		select {
		case message := <-resolve:
			return message, nil
		case err := <-reject:
			return nil, err
		}
	}, nil
}

// Next waits for the next message matching the predicate.
func (c *ProtocolTestClient) Next(predicate func(message *protocol.ServerMessage) bool) (*protocol.ServerMessage, error) {
	return c.NextFrom(0, predicate)
}

// NextFrom waits for a message at or after index matching the predicate.
func (c *ProtocolTestClient) NextFrom(index int, predicate func(message *protocol.ServerMessage) bool) (*protocol.ServerMessage, error) {
	c.mu.Lock()
	for _, message := range c.messages[min(index, len(c.messages)):] {
		if predicate(message) {
			c.mu.Unlock()
			return message, nil
		}
	}
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("Wire client is closed")
	}
	resolve := make(chan *protocol.ServerMessage, 1)
	reject := make(chan error, 1)
	c.waiters = append(c.waiters, &messageWaiter{predicate: predicate, resolve: resolve, reject: reject})
	c.mu.Unlock()
	select {
	case message := <-resolve:
		return message, nil
	case err := <-reject:
		return nil, err
	}
}

// WaitForClose blocks until the wire connection closes.
func (c *ProtocolTestClient) WaitForClose() { <-c.closedCh }

// Close closes the wire channel.
func (c *ProtocolTestClient) Close() error { return c.channel.Close() }

// Receive feeds inbound bytes into the decoder.
func (c *ProtocolTestClient) Receive(chunk []byte) {
	messages, err := c.decoder.Push(chunk)
	if err != nil {
		c.Fail(err)
		return
	}
	for _, message := range messages {
		c.mu.Lock()
		if message.Type == protocol.ServerMessageAttachment {
			if message.Attachment.Attachment == nil {
				c.attachment = nil
			} else {
				c.attachment = &attachmentRef{
					sessionID:    deref(message.Attachment.Attachment.SessionID),
					attachmentID: deref(message.Attachment.Attachment.AttachmentID),
				}
			}
		}
		c.messages = append(c.messages, message)
		waiters := c.waiters
		for _, waiter := range waiters {
			if !waiter.predicate(message) {
				continue
			}
			c.removeWaiter(waiter)
			waiter.resolve <- message
		}
		c.mu.Unlock()
	}
}

// MarkClosed records a closed wire connection.
func (c *ProtocolTestClient) MarkClosed() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	waiters := c.waiters
	c.waiters = nil
	c.mu.Unlock()
	for _, waiter := range waiters {
		waiter.reject <- fmt.Errorf("Wire connection closed")
	}
	c.closedOnce.Do(func() { close(c.closedCh) })
}

// Fail rejects every pending waiter.
func (c *ProtocolTestClient) Fail(err error) {
	c.mu.Lock()
	waiters := c.waiters
	c.waiters = nil
	c.mu.Unlock()
	for _, waiter := range waiters {
		waiter.reject <- err
	}
}

func (c *ProtocolTestClient) removeWaiter(target *messageWaiter) {
	for index, waiter := range c.waiters {
		if waiter == target {
			c.waiters = append(c.waiters[:index], c.waiters[index+1:]...)
			return
		}
	}
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// ConnectUnixTestClient dials a Unix socket and drives a protocol test client.
func ConnectUnixTestClient(path string) (*ProtocolTestClient, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, err
	}
	client, err := NewProtocolTestClient(&unixWireChannel{conn: conn})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	go func() {
		buffer := make([]byte, 32*1024)
		for {
			n, readErr := conn.Read(buffer)
			if n > 0 {
				client.Receive(append([]byte{}, buffer[:n]...))
			}
			if readErr != nil {
				if readErr.Error() == "EOF" {
					client.MarkClosed()
				} else {
					client.Fail(readErr)
					client.MarkClosed()
				}
				return
			}
		}
	}()
	return client, nil
}

type unixWireChannel struct {
	conn     net.Conn
	writeMux sync.Mutex
}

func (c *unixWireChannel) Send(chunk []byte) error {
	c.writeMux.Lock()
	defer c.writeMux.Unlock()
	_, err := c.conn.Write(chunk)
	return err
}

func (c *unixWireChannel) SendFragmented(chunk []byte, splitAt int) error {
	if splitAt > len(chunk) {
		splitAt = len(chunk)
	}
	if err := c.Send(chunk[:splitAt]); err != nil {
		return err
	}
	return c.Send(chunk[splitAt:])
}

func (c *unixWireChannel) Close() error { return c.conn.Close() }
