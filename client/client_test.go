package client

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dat267/gpi/protocol"
)

// Client tests keyed to upstream (connection.ts, client.ts). An in-memory
// transport pair stands in for the unix-socket transport.

const testServerID = "123e4567-e89b-42d3-a456-426614174000"

// pipeTransport connects a client and a server side in memory.
type pipeTransport struct {
	mu       sync.Mutex
	handlers ByteTransportHandlers
	peer     *pipeTransport
	closed   bool
}

func newPipePair() (clientSide, serverSide *pipeTransport) {
	clientSide = &pipeTransport{}
	serverSide = &pipeTransport{}
	clientSide.peer = serverSide
	serverSide.peer = clientSide
	return clientSide, serverSide
}

func (t *pipeTransport) Send(chunk []byte) error {
	t.mu.Lock()
	peer := t.peer
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return NewDisconnectedError("transport closed", nil)
	}
	delivered := make([]byte, len(chunk))
	copy(delivered, chunk)
	peer.mu.Lock()
	handlers := peer.handlers
	peerClosed := peer.closed
	peer.mu.Unlock()
	if peerClosed {
		return NewDisconnectedError("peer closed", nil)
	}
	if handlers.OnData != nil {
		handlers.OnData(delivered)
	}
	return nil
}

func (t *pipeTransport) Close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	handlers := t.handlers
	t.mu.Unlock()
	// Closing one side surfaces an orderly close on the peer.
	if handlers.OnClose != nil {
		handlers.OnClose()
	}
}

func (t *pipeTransport) attach(handlers ByteTransportHandlers) {
	t.mu.Lock()
	t.handlers = handlers
	t.mu.Unlock()
}

// serverHarness scripts a server side: it decodes client frames and lets the
// test send server frames.
type serverHarness struct {
	t         *testing.T
	transport *pipeTransport
	decoder   *protocol.ClientMessageDecoder

	mu             sync.Mutex
	receivedHellos []*protocol.ClientMessage
	received       []*protocol.ClientMessage
	helloMessages  chan *protocol.ClientMessage
}

func newServerHarness(t *testing.T, transport *pipeTransport) *serverHarness {
	return newServerHarnessWithHello(t, transport, true)
}

// newServerHarnessWithHello builds a harness that either answers the client
// hello automatically (a well-behaved server) or leaves the handshake to the
// test.
func newServerHarnessWithHello(t *testing.T, transport *pipeTransport, autoHello bool) *serverHarness {
	harness := &serverHarness{
		t: t, transport: transport,
		helloMessages: make(chan *protocol.ClientMessage, 16),
	}
	decoder, err := protocol.NewClientMessageDecoder(nil)
	if err != nil {
		t.Fatal(err)
	}
	harness.decoder = decoder
	transport.attach(ByteTransportHandlers{
		OnData: func(chunk []byte) {
			messages, err := decoder.Push(chunk)
			if err != nil {
				return
			}
			for _, message := range messages {
				harness.mu.Lock()
				harness.received = append(harness.received, message)
				harness.mu.Unlock()
				if autoHello && message.Type == protocol.ClientMessageHello {
					harness.sendHello()
				}
				select {
				case harness.helloMessages <- message:
				default:
				}
			}
		},
	})
	return harness
}

func (h *serverHarness) sendHello() {
	frame, err := protocol.EncodeServerMessage(&protocol.ServerMessage{
		Type:  protocol.ServerMessageHello,
		Hello: &protocol.ServerHello{Version: protocol.ProtocolVersion, ServerID: testServerID},
	}, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.transport.Send(frame); err != nil {
		h.t.Fatal(err)
	}
}

func (h *serverHarness) sendResponse(id string, result any) {
	frame, err := protocol.EncodeServerMessage(&protocol.ServerMessage{
		Type:     protocol.ServerMessageResponse,
		Response: &protocol.ResponseEnvelope{ID: id, OK: true, Result: result},
	}, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.transport.Send(frame); err != nil {
		h.t.Fatal(err)
	}
}

func (h *serverHarness) sendError(id, code, message string) {
	frame, err := protocol.EncodeServerMessage(&protocol.ServerMessage{
		Type: protocol.ServerMessageResponse,
		Response: &protocol.ResponseEnvelope{ID: id, OK: false,
			Error: &protocol.ProtocolError{Code: code, Message: message}},
	}, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.transport.Send(frame); err != nil {
		h.t.Fatal(err)
	}
}

// waitForMessage waits for the first client message of the given type.
func (h *serverHarness) waitForMessage(kind string) *protocol.ClientMessage {
	deadline := time.After(3 * time.Second)
	for {
		select {
		case message := <-h.helloMessages:
			if message.Type == kind {
				return message
			}
		case <-deadline:
			h.t.Fatalf("timed out waiting for client message %q", kind)
			return nil
		}
	}
}

func newTestClient(t *testing.T, transport *pipeTransport) *Client {
	t.Helper()
	client, err := NewClient(ClientOptions{
		TransportFactory: func(handlers ByteTransportHandlers) (ByteTransport, error) {
			transport.attach(handlers)
			return transport, nil
		},
		ServerID: testServerID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestClientHandshake(t *testing.T) {
	clientSide, serverSide := newPipePair()
	harness := newServerHarness(t, serverSide)
	client := newTestClient(t, clientSide)

	var states []ConnectionState
	var mu sync.Mutex
	_, err := client.OnConnectionStateChange(func(change ConnectionStateChange) {
		mu.Lock()
		states = append(states, change.State)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}

	hello, err := client.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hello.ServerID != testServerID || hello.Version != protocol.ProtocolVersion {
		t.Fatalf("hello = %+v", hello)
	}
	if !client.Connected() || client.ConnectionState() != StateConnected {
		t.Fatal("client should be connected")
	}
	// The client sent the handshake first.
	first := harness.waitForMessage(protocol.ClientMessageHello)
	if first.Hello.Version != protocol.ProtocolVersion {
		t.Fatalf("client hello = %+v", first.Hello)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(states) < 2 || states[0] != StateConnecting || states[len(states)-1] != StateConnected {
		t.Fatalf("states = %v", states)
	}
}

func TestClientRejectsMismatchedServerID(t *testing.T) {
	clientSide, serverSide := newPipePair()
	_ = newServerHarnessWithHello(t, serverSide, false)
	client := newTestClient(t, clientSide)

	go func() {
		// Wait for the client hello, then answer with the wrong server id.
		time.Sleep(50 * time.Millisecond)
		frame, _ := protocol.EncodeServerMessage(&protocol.ServerMessage{
			Type:  protocol.ServerMessageHello,
			Hello: &protocol.ServerHello{Version: protocol.ProtocolVersion, ServerID: "123e4567-e89b-42d3-a456-426614174abc"},
		}, nil)
		_ = serverSide.Send(frame)
	}()

	if _, err := client.Connect(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v", err)
	}
	if client.Connected() {
		t.Fatal("client must not be connected")
	}
}

func TestClientHandshakeError(t *testing.T) {
	clientSide, serverSide := newPipePair()
	_ = newServerHarnessWithHello(t, serverSide, false)
	client := newTestClient(t, clientSide)

	go func() {
		time.Sleep(50 * time.Millisecond)
		frame, _ := protocol.EncodeServerMessage(&protocol.ServerMessage{
			Type: protocol.ServerMessageHelloError,
			HelloError: &protocol.ServerHelloError{Error: protocol.ProtocolError{
				Code: "unsupported_version", Message: "try another version",
			}},
		}, nil)
		_ = serverSide.Send(frame)
	}()

	_, err := client.Connect(context.Background())
	serverErr, ok := err.(*ServerError)
	if !ok || serverErr.Code != "unsupported_version" || serverErr.Message != "try another version" {
		t.Fatalf("err = %v", err)
	}
}

func TestClientRequestRoundTrip(t *testing.T) {
	clientSide, serverSide := newPipePair()
	harness := newServerHarness(t, serverSide)
	client := newTestClient(t, clientSide)

	if _, err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	harness.waitForMessage(protocol.ClientMessageHello)

	target := protocol.RpcTarget{ServerID: testServerID}
	type result struct {
		value any
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := client.Request(context.Background(), target, map[string]any{"method": "sessions.list"})
		done <- result{value, err}
	}()

	request := harness.waitForMessage(protocol.ClientMessageRequest)
	if request.Request.Target.ServerID != testServerID {
		t.Fatalf("target = %+v", request.Request.Target)
	}
	call, _ := request.Request.Call.(map[string]any)
	if call["method"] != "sessions.list" {
		t.Fatalf("call = %v", request.Request.Call)
	}
	harness.sendResponse(request.Request.ID, map[string]any{"sessions": []any{"a"}})

	select {
	case outcome := <-done:
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		value, _ := outcome.value.(map[string]any)
		sessions, _ := value["sessions"].([]any)
		if len(sessions) != 1 || sessions[0] != "a" {
			t.Fatalf("result = %v", outcome.value)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("request timed out")
	}
}

func TestClientRequestServerError(t *testing.T) {
	clientSide, serverSide := newPipePair()
	harness := newServerHarness(t, serverSide)
	client := newTestClient(t, clientSide)
	if _, err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	harness.waitForMessage(protocol.ClientMessageHello)

	done := make(chan error, 1)
	go func() {
		_, err := client.Request(context.Background(), protocol.RpcTarget{ServerID: testServerID}, "call")
		done <- err
	}()
	request := harness.waitForMessage(protocol.ClientMessageRequest)
	harness.sendError(request.Request.ID, "not_found", "no such session")

	select {
	case err := <-done:
		serverErr, ok := err.(*ServerError)
		if !ok || serverErr.Code != "not_found" {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("request timed out")
	}
}

func TestClientRequestBeforeConnect(t *testing.T) {
	clientSide, _ := newPipePair()
	client := newTestClient(t, clientSide)
	if _, err := client.Request(context.Background(), protocol.RpcTarget{ServerID: testServerID}, "call"); err == nil {
		t.Fatal("requests before connect must fail")
	} else if _, ok := err.(*DisconnectedError); !ok {
		t.Fatalf("err = %T", err)
	}
}

func TestClientAttachmentChanges(t *testing.T) {
	clientSide, serverSide := newPipePair()
	harness := newServerHarness(t, serverSide)
	client := newTestClient(t, clientSide)
	if _, err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	harness.waitForMessage(protocol.ClientMessageHello)

	changes := make(chan *protocol.RpcTarget, 4)
	if _, err := client.OnAttachmentChange(func(attachment *protocol.RpcTarget) {
		changes <- attachment
	}); err != nil {
		t.Fatal(err)
	}

	sessionID, attachmentID := "session-1", "attachment-1"
	frame, err := protocol.EncodeServerMessage(&protocol.ServerMessage{
		Type: protocol.ServerMessageAttachment,
		Attachment: &protocol.AttachmentEnvelope{Attachment: &protocol.RpcTarget{
			ServerID: testServerID, SessionID: &sessionID, AttachmentID: &attachmentID,
		}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := serverSide.Send(frame); err != nil {
		t.Fatal(err)
	}

	select {
	case attachment := <-changes:
		if attachment == nil || *attachment.SessionID != "session-1" {
			t.Fatalf("attachment = %+v", attachment)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("attachment change timed out")
	}
	if client.Attachment() == nil {
		t.Fatal("attachment should be set")
	}

	// A null attachment clears it.
	clearFrame, _ := protocol.EncodeServerMessage(&protocol.ServerMessage{
		Type:       protocol.ServerMessageAttachment,
		Attachment: &protocol.AttachmentEnvelope{Attachment: nil},
	}, nil)
	if err := serverSide.Send(clearFrame); err != nil {
		t.Fatal(err)
	}
	select {
	case attachment := <-changes:
		if attachment != nil {
			t.Fatalf("expected cleared attachment, got %+v", attachment)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("attachment clear timed out")
	}
}

func TestClientAttachmentFromAnotherServerFails(t *testing.T) {
	clientSide, serverSide := newPipePair()
	harness := newServerHarness(t, serverSide)
	client := newTestClient(t, clientSide)
	if _, err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	harness.waitForMessage(protocol.ClientMessageHello)

	frame, _ := protocol.EncodeServerMessage(&protocol.ServerMessage{
		Type: protocol.ServerMessageAttachment,
		Attachment: &protocol.AttachmentEnvelope{Attachment: &protocol.RpcTarget{
			ServerID: "123e4567-e89b-42d3-a456-426614174abc",
		}},
	}, nil)
	_ = serverSide.Send(frame)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if client.ConnectionState() == StateDisconnected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("cross-server attachment must fail the connection")
}

func TestClientDisconnectRejectsPending(t *testing.T) {
	clientSide, serverSide := newPipePair()
	harness := newServerHarness(t, serverSide)
	client := newTestClient(t, clientSide)
	if _, err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	harness.waitForMessage(protocol.ClientMessageHello)

	done := make(chan error, 1)
	go func() {
		_, err := client.Request(context.Background(), protocol.RpcTarget{ServerID: testServerID}, "call")
		done <- err
	}()
	harness.waitForMessage(protocol.ClientMessageRequest)

	client.Disconnect("server went away")
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("pending request must fail")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pending request not rejected")
	}
	if client.Connected() || client.Hello() != nil {
		t.Fatal("client must be disconnected with no hello")
	}
}

func TestClientDispose(t *testing.T) {
	clientSide, serverSide := newPipePair()
	harness := newServerHarness(t, serverSide)
	client := newTestClient(t, clientSide)
	if _, err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	harness.waitForMessage(protocol.ClientMessageHello)

	if err := client.Dispose(); err != nil {
		t.Fatal(err)
	}
	if !client.Disposed() {
		t.Fatal("client should be disposed")
	}
	// Further operations fail with ClientDisposedError.
	if _, err := client.Connect(context.Background()); err == nil {
		t.Fatal("connect after dispose must fail")
	} else if _, ok := err.(*ClientDisposedError); !ok {
		t.Fatalf("err = %T", err)
	}
	if _, err := client.Request(context.Background(), protocol.RpcTarget{ServerID: testServerID}, "call"); err == nil {
		t.Fatal("request after dispose must fail")
	}
	// Dispose is idempotent.
	if err := client.Dispose(); err != nil {
		t.Fatal(err)
	}
}

func TestClientRejectsInvalidServerID(t *testing.T) {
	if _, err := NewClient(ClientOptions{ServerID: "not-a-uuid"}); err == nil {
		t.Fatal("invalid serverId must be rejected")
	}
}

func TestClientRejectsDoubleConnect(t *testing.T) {
	clientSide, serverSide := newPipePair()
	harness := newServerHarness(t, serverSide)
	client := newTestClient(t, clientSide)
	if _, err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	harness.waitForMessage(protocol.ClientMessageHello)

	if _, err := client.Connect(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "already connected") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientResponseWithoutRequestFails(t *testing.T) {
	clientSide, serverSide := newPipePair()
	harness := newServerHarness(t, serverSide)
	client := newTestClient(t, clientSide)
	if _, err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	harness.waitForMessage(protocol.ClientMessageHello)

	harness.sendResponse("request-999", "unexpected")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if client.ConnectionState() == StateDisconnected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("unmatched response must fail the connection")
}
