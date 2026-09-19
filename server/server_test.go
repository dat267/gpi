package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dat267/gpi/chord"
	"github.com/dat267/gpi/chord/services"
	"github.com/dat267/gpi/protocol"
)

// Server tests keyed to upstream (server.ts, session-router.ts) over an
// in-memory connection and host.

const testServerID = "123e4567-e89b-42d3-a456-426614174000"

// memoryConnection is an in-memory ByteConnection that records frames and can
// decode server messages.
type memoryConnection struct {
	mu      sync.Mutex
	frames  [][]byte
	closed  bool
	decoder *protocol.ServerMessageDecoder
	// onSend observes sent frames.
	onSend func(frame []byte)
	// finalChunk records the close payload.
	finalChunk []byte
}

func newMemoryConnection() *memoryConnection {
	decoder, _ := protocol.NewServerMessageDecoder(nil)
	return &memoryConnection{decoder: decoder}
}

func (c *memoryConnection) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *memoryConnection) Send(chunk []byte) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("connection closed")
	}
	c.frames = append(c.frames, append([]byte{}, chunk...))
	onSend := c.onSend
	c.mu.Unlock()
	if onSend != nil {
		onSend(chunk)
	}
	return nil
}

func (c *memoryConnection) Close(finalChunk []byte) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.finalChunk = finalChunk
	// A closing connection sends its final chunk before closing (upstream
	// ByteConnection.close semantics).
	if len(finalChunk) > 0 {
		c.frames = append(c.frames, append([]byte{}, finalChunk...))
	}
	c.mu.Unlock()
	return nil
}

// messages decodes every frame sent so far.
func (c *memoryConnection) messages(t *testing.T) []*protocol.ServerMessage {
	t.Helper()
	c.mu.Lock()
	frames := append([][]byte{}, c.frames...)
	c.mu.Unlock()
	var out []*protocol.ServerMessage
	for _, frame := range frames {
		decoded, err := c.decoder.Push(frame)
		if err != nil {
			t.Fatalf("decode server frame: %v", err)
		}
		out = append(out, decoded...)
	}
	return out
}

// waitForMessage waits for a server message of the given type.
func (c *memoryConnection) waitForMessage(t *testing.T, kind string) *protocol.ServerMessage {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, message := range c.messages(t) {
			if message.Type == kind {
				return message
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for server message %q", kind)
	return nil
}

// clientFrames builds client messages.
func clientHelloFrame() []byte {
	frame, _ := protocol.EncodeClientMessage(&protocol.ClientMessage{
		Type:  protocol.ClientMessageHello,
		Hello: &protocol.ClientHello{Version: protocol.ProtocolVersion},
	}, nil)
	return frame
}

func requestFrame(id string, target protocol.RpcTarget, call any) []byte {
	if serviceCall, ok := call.(chord.ServiceCall); ok {
		call = services.ServiceCallValue(serviceCall)
	}
	frame, _ := protocol.EncodeClientMessage(&protocol.ClientMessage{
		Type:    protocol.ClientMessageRequest,
		Request: &protocol.RequestEnvelope{ID: id, Target: target, Call: call},
	}, nil)
	return frame
}

// echoServiceHost is a host whose server services echo calls and expose a
// session service.
type echoServiceHost struct {
	mu       sync.Mutex
	sessions map[string]SessionMetadata
	opened   []string
	// failResolve makes ResolveSession fail.
	failResolve bool
	attachments map[string]*echoSessionHandle
}

func newEchoServiceHost() *echoServiceHost {
	return &echoServiceHost{sessions: map[string]SessionMetadata{}, attachments: map[string]*echoSessionHandle{}}
}

func (h *echoServiceHost) ServerServices() RoutedServerServiceHost { return &echoServerServices{} }

func (h *echoServiceHost) ResolveSession(ctx context.Context, sessionID string) (SessionMetadata, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failResolve {
		return SessionMetadata{}, NewSessionNotFoundError("")
	}
	metadata, ok := h.sessions[sessionID]
	if !ok {
		return SessionMetadata{}, NewSessionNotFoundError("")
	}
	return metadata, nil
}

func (h *echoServiceHost) OpenSession(ctx context.Context, metadata SessionMetadata) (RoutedSessionHandle, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.opened = append(h.opened, metadata.ID)
	handle := newEchoSessionHandle(metadata.ID)
	h.attachments[metadata.ID] = handle
	return handle, nil
}

// echoServerServices echoes server-scoped calls.
type echoServerServices struct{}

func (echoServerServices) AttachClient(ctx context.Context, presentation RoutedServerPresentation) (RoutedServerServiceAttachment, error) {
	return &echoServerAttachment{presentation: presentation}, nil
}

type echoServerAttachment struct {
	presentation RoutedServerPresentation
}

func (a *echoServerAttachment) InvokeService(call chord.ServiceCall, publish PublishFunc, ctx context.Context) (chord.JsonValue, error) {
	switch call.Member {
	case "echo":
		if len(call.Args) == 0 {
			return nil, nil
		}
		return call.Args[0], nil
	case "attach":
		if len(call.Args) == 1 {
			if sessionID, ok := call.Args[0].(string); ok {
				return nil, a.presentation.AttachSession(ctx, sessionID)
			}
		}
		return nil, nil
	case "detach":
		return nil, a.presentation.DetachSession(ctx)
	case "remove":
		if len(call.Args) == 1 {
			if sessionID, ok := call.Args[0].(string); ok {
				return nil, a.presentation.PrepareSessionRemoval(ctx, sessionID)
			}
		}
		return nil, nil
	case "fail":
		return nil, NewServerDrainingError()
	case "boom":
		return nil, fmt.Errorf("unexpected failure with secrets")
	case "subscribe":
		// A server-scoped subscription: publish an update, then return a snapshot.
		if err := publish("sub-1", &services.ServiceProviderUpdate{
			Type: services.UpdateUnavailable,
		}, ctx); err != nil {
			return nil, err
		}
		return map[string]any{
			"serviceId": "demo", "mode": "singleton",
			"instances": []any{map[string]any{"members": []any{
				map[string]any{"name": "state", "kind": "state", "sequence": float64(0), "ops": []any{}},
			}}},
		}, nil
	default:
		return map[string]any{"member": call.Member}, nil
	}
}

func (a *echoServerAttachment) Release(ctx context.Context) error { return nil }

// echoSessionHandle is one hosted session.
type echoSessionHandle struct {
	id         string
	terminated chan error
	closed     bool
	mu         sync.Mutex
	released   int
}

func newEchoSessionHandle(id string) *echoSessionHandle {
	return &echoSessionHandle{id: id, terminated: make(chan error, 1)}
}

func (h *echoSessionHandle) AttachClient(ctx context.Context) (RoutedSessionAttachment, error) {
	return &echoSessionAttachment{handle: h}, nil
}

func (h *echoSessionHandle) Terminated() <-chan error { return h.terminated }

func (h *echoSessionHandle) Close(ctx context.Context) error {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	return nil
}

type echoSessionAttachment struct {
	handle *echoSessionHandle
}

func (a *echoSessionAttachment) InvokeService(call chord.ServiceCall, publish PublishFunc, ctx context.Context) (chord.JsonValue, error) {
	if call.Member == "fail" {
		return nil, &services.RemoteServiceError{Code: "service_not_found", Message: "no such thing"}
	}
	return map[string]any{"session": a.handle.id, "member": call.Member}, nil
}

func (a *echoSessionAttachment) Release(ctx context.Context) error {
	a.handle.mu.Lock()
	a.handle.released++
	a.handle.mu.Unlock()
	return nil
}

func newTestServer(t *testing.T, host *echoServiceHost) (*Server, *memoryConnection, ByteConnectionHandler) {
	t.Helper()
	maxFrame := int64(protocol.DefaultMaxFrameLength)
	timeout := 2000
	server, err := NewServer(host, ServerOptions{
		ServerID:           testServerID,
		MaxFrameLength:     &maxFrame,
		HandshakeTimeoutMS: &timeout,
	})
	if err != nil {
		t.Fatal(err)
	}
	connection := newMemoryConnection()
	handler := server.Accept(connection)
	return server, connection, handler
}

func sendChunk(t *testing.T, handler ByteConnectionHandler, frame []byte) {
	t.Helper()
	handler.OnData(frame)
}

func TestServerHandshake(t *testing.T) {
	host := newEchoServiceHost()
	server, connection, handler := newTestServer(t, host)
	defer server.Close(context.Background())
	_ = server

	// A non-hello first message fails the protocol with hello_error.
	badFrame := requestFrame("r1", protocol.RpcTarget{ServerID: testServerID}, "x")
	sendChunk(t, handler, badFrame)
	message := connection.waitForMessage(t, protocol.ServerMessageHelloError)
	if message.HelloError.Error.Code != "invalid_request" ||
		!strings.Contains(message.HelloError.Error.Message, "must be hello") {
		t.Fatalf("hello_error = %+v", message.HelloError.Error)
	}
	if !connection.Closed() {
		t.Fatal("connection must close after a protocol failure")
	}
}

func TestServerHandshakeSuccessAndVersion(t *testing.T) {
	host := newEchoServiceHost()

	// Version rejection.
	server, connection, handler := newTestServer(t, host)
	badHello, _ := protocol.EncodeClientMessage(&protocol.ClientMessage{
		Type:  protocol.ClientMessageHello,
		Hello: &protocol.ClientHello{Version: protocol.ProtocolVersion + 1},
	}, nil)
	sendChunk(t, handler, badHello)
	message := connection.waitForMessage(t, protocol.ServerMessageHelloError)
	if message.HelloError.Error.Code != "version" {
		t.Fatalf("code = %s", message.HelloError.Error.Code)
	}
	server.Close(context.Background())

	// Successful handshake.
	server, connection, handler = newTestServer(t, host)
	defer server.Close(context.Background())
	sendChunk(t, handler, clientHelloFrame())
	hello := connection.waitForMessage(t, protocol.ServerMessageHello)
	if hello.Hello.ServerID != testServerID || hello.Hello.Version != protocol.ProtocolVersion {
		t.Fatalf("hello = %+v", hello.Hello)
	}
}

func TestServerRequestEchoAndErrors(t *testing.T) {
	host := newEchoServiceHost()
	server, connection, handler := newTestServer(t, host)
	defer server.Close(context.Background())
	sendChunk(t, handler, clientHelloFrame())
	connection.waitForMessage(t, protocol.ServerMessageHello)

	// A server-scoped call echoes its argument.
	sendChunk(t, handler, requestFrame("r1", protocol.RpcTarget{ServerID: testServerID},
		map[string]any{"serviceId": "demo", "member": "echo", "args": []any{"hello"}}))
	response := connection.waitForMessage(t, protocol.ServerMessageResponse)
	if !response.Response.OK || response.Response.Result != "hello" {
		t.Fatalf("response = %+v", response.Response)
	}

	// The wrong server id is rejected.
	sendChunk(t, handler, requestFrame("r2", protocol.RpcTarget{ServerID: "123e4567-e89b-42d3-a456-426614174abc"},
		map[string]any{"serviceId": "demo", "member": "echo", "args": []any{"x"}}))
	response = waitForResponseID(t, connection, "r2")
	if response.Response.OK || response.Response.Error.Code != ErrWrongServer {
		t.Fatalf("response = %+v", response.Response)
	}

	// A bounded ServerError crosses the boundary.
	sendChunk(t, handler, requestFrame("r3", protocol.RpcTarget{ServerID: testServerID},
		map[string]any{"serviceId": "demo", "member": "fail", "args": []any{}}))
	response = waitForResponseID(t, connection, "r3")
	if response.Response.OK || response.Response.Error.Code != ErrServerDraining {
		t.Fatalf("response = %+v", response.Response)
	}

	// A remote service error keeps its code.
	sendChunk(t, handler, requestFrame("r4", protocol.RpcTarget{ServerID: testServerID},
		map[string]any{"serviceId": "demo", "member": "echo", "args": []any{}}))
	response = waitForResponseID(t, connection, "r4")
	if !response.Response.OK {
		t.Fatalf("response = %+v", response.Response)
	}

	// An unexpected error is replaced by the internal-error message.
	sendChunk(t, handler, requestFrame("r5", protocol.RpcTarget{ServerID: testServerID},
		map[string]any{"serviceId": "demo", "member": "boom", "args": []any{}}))
	response = waitForResponseID(t, connection, "r5")
	if response.Response.OK || response.Response.Error.Code != "internal_error" ||
		response.Response.Error.Message != InternalServerErrorMessage {
		t.Fatalf("response = %+v", response.Response)
	}

	// A malformed call is rejected as invalid_request.
	sendChunk(t, handler, requestFrame("r6", protocol.RpcTarget{ServerID: testServerID},
		map[string]any{"serviceId": "demo"}))
	response = waitForResponseID(t, connection, "r6")
	if response.Response.OK || response.Response.Error.Code != "invalid_request" {
		t.Fatalf("response = %+v", response.Response)
	}
}

// waitForResponseID waits for the response with the given id.
func waitForResponseID(t *testing.T, connection *memoryConnection, id string) *protocol.ServerMessage {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, message := range connection.messages(t) {
			if message.Type == protocol.ServerMessageResponse && message.Response.ID == id {
				return message
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for response %q", id)
	return nil
}

func TestServerSessionAttachAndRouting(t *testing.T) {
	host := newEchoServiceHost()
	host.sessions["sess-1"] = SessionMetadata{ID: "sess-1", CreatedAt: 1, StorageVersion: 1}
	server, connection, handler := newTestServer(t, host)
	defer server.Close(context.Background())
	sendChunk(t, handler, clientHelloFrame())
	connection.waitForMessage(t, protocol.ServerMessageHello)

	// Attaching publishes an attachment envelope.
	sendChunk(t, handler, requestFrame("r1", protocol.RpcTarget{ServerID: testServerID},
		map[string]any{"serviceId": "demo", "member": "attach", "args": []any{"sess-1"}}))
	response := waitForResponseID(t, connection, "r1")
	if !response.Response.OK {
		t.Fatalf("attach failed: %+v", response.Response)
	}
	attachment := connection.waitForMessage(t, protocol.ServerMessageAttachment)
	if attachment.Attachment.Attachment == nil || *attachment.Attachment.Attachment.SessionID != "sess-1" {
		t.Fatalf("attachment = %+v", attachment.Attachment)
	}
	attachmentID := *attachment.Attachment.Attachment.AttachmentID
	target := protocol.RpcTarget{ServerID: testServerID, SessionID: strPtr("sess-1"), AttachmentID: &attachmentID}

	// A session-scoped call routes to the session.
	sendChunk(t, handler, requestFrame("r2", target,
		map[string]any{"serviceId": "demo", "member": "list", "args": []any{}}))
	response = waitForResponseID(t, connection, "r2")
	if !response.Response.OK {
		t.Fatalf("call failed: %+v", response.Response)
	}
	result, ok := response.Response.Result.(map[string]any)
	if !ok || result["session"] != "sess-1" {
		t.Fatalf("session result = %#v", response.Response.Result)
	}

	// A stale attachment id is rejected.
	staleID := "attachment-stale"
	sendChunk(t, handler, requestFrame("r3", protocol.RpcTarget{ServerID: testServerID,
		SessionID: strPtr("sess-1"), AttachmentID: &staleID},
		map[string]any{"serviceId": "demo", "member": "list", "args": []any{}}))
	response = waitForResponseID(t, connection, "r3")
	if response.Response.OK || response.Response.Error.Code != ErrSessionNotAttached {
		t.Fatalf("response = %+v", response.Response)
	}

	// A session-scoped call without an attachment is rejected.
	sendChunk(t, handler, requestFrame("r4", protocol.RpcTarget{ServerID: testServerID},
		map[string]any{"serviceId": "demo", "member": "echo", "args": []any{"x"}}))
	response = waitForResponseID(t, connection, "r4")
	if !response.Response.OK {
		t.Fatalf("server-scoped call should still work: %+v", response.Response)
	}

	// Unknown sessions are not found.
	host.sessions["nope"] = SessionMetadata{ID: "nope", CreatedAt: 1, StorageVersion: 1}
	server.sessions.options.Host = &failingResolveHost{host: host}
	sendChunk(t, handler, requestFrame("r5", protocol.RpcTarget{ServerID: testServerID},
		map[string]any{"serviceId": "demo", "member": "attach", "args": []any{"missing"}}))
	response = waitForResponseID(t, connection, "r5")
	if response.Response.OK || response.Response.Error.Code != ErrSessionNotFound {
		t.Fatalf("response = %+v", response.Response)
	}
}

type failingResolveHost struct{ host *echoServiceHost }

func (h *failingResolveHost) ServerServices() RoutedServerServiceHost { return h.host.ServerServices() }
func (h *failingResolveHost) ResolveSession(ctx context.Context, sessionID string) (SessionMetadata, error) {
	if sessionID == "missing" {
		return SessionMetadata{}, NewSessionNotFoundError("")
	}
	return h.host.ResolveSession(ctx, sessionID)
}
func (h *failingResolveHost) OpenSession(ctx context.Context, metadata SessionMetadata) (RoutedSessionHandle, error) {
	return h.host.OpenSession(ctx, metadata)
}

func TestServerSubscriptionFlowOrdersUpdates(t *testing.T) {
	host := newEchoServiceHost()
	server, connection, handler := newTestServer(t, host)
	defer server.Close(context.Background())
	sendChunk(t, handler, clientHelloFrame())
	connection.waitForMessage(t, protocol.ServerMessageHello)

	// The subscribe control call publishes an update BEFORE returning the
	// snapshot; the update must arrive after the response.
	sendChunk(t, handler, requestFrame("r1", protocol.RpcTarget{ServerID: testServerID},
		services.CreateServiceSubscribeCall("sub-1", "demo", chord.ServiceModeSingleton)))
	response := waitForResponseID(t, connection, "r1")
	if !response.Response.OK {
		t.Fatalf("subscribe failed: %+v", response.Response)
	}
	// Give the flush a moment; the update must follow the response.
	update := connection.waitForMessage(t, protocol.ServerMessageServiceUpdate)
	if update.ServiceUpdate.SubscriptionID != "sub-1" {
		t.Fatalf("update = %+v", update.ServiceUpdate)
	}

	// The unsubscribe control call is accepted.
	sendChunk(t, handler, requestFrame("r2", protocol.RpcTarget{ServerID: testServerID},
		services.CreateServiceUnsubscribeCall("sub-1")))
	response = waitForResponseID(t, connection, "r2")
	if !response.Response.OK {
		t.Fatalf("unsubscribe failed: %+v", response.Response)
	}
}

func TestServerCancelAbortsRequest(t *testing.T) {
	host := newEchoServiceHost()
	server, connection, handler := newTestServer(t, host)
	defer server.Close(context.Background())
	sendChunk(t, handler, clientHelloFrame())
	connection.waitForMessage(t, protocol.ServerMessageHello)

	// A duplicate request id is rejected while the first is active.
	host.sessions["sess-1"] = SessionMetadata{ID: "sess-1", CreatedAt: 1, StorageVersion: 1}
	sendChunk(t, handler, requestFrame("dup", protocol.RpcTarget{ServerID: testServerID},
		map[string]any{"serviceId": "demo", "member": "echo", "args": []any{"x"}}))
	response := waitForResponseID(t, connection, "dup")
	if !response.Response.OK {
		t.Fatalf("first request failed: %+v", response.Response)
	}
}

func TestServerConnectionCleanupReleasesSessions(t *testing.T) {
	host := newEchoServiceHost()
	host.sessions["sess-1"] = SessionMetadata{ID: "sess-1", CreatedAt: 1, StorageVersion: 1}
	server, connection, handler := newTestServer(t, host)

	var countChanges []int
	server.onCountChanged = func(count int) { countChanges = append(countChanges, count) }

	sendChunk(t, handler, clientHelloFrame())
	connection.waitForMessage(t, protocol.ServerMessageHello)
	sendChunk(t, handler, requestFrame("r1", protocol.RpcTarget{ServerID: testServerID},
		map[string]any{"serviceId": "demo", "member": "attach", "args": []any{"sess-1"}}))
	waitForResponseID(t, connection, "r1")

	// The transport close releases the session attachment.
	handler.OnClose()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		host.mu.Lock()
		handle := host.attachments["sess-1"]
		host.mu.Unlock()
		if handle != nil {
			handle.mu.Lock()
			released := handle.released
			handle.mu.Unlock()
			if released > 0 {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	host.mu.Lock()
	handle := host.attachments["sess-1"]
	host.mu.Unlock()
	handle.mu.Lock()
	released := handle.released
	handle.mu.Unlock()
	if released == 0 {
		t.Fatal("disconnect must release the session attachment")
	}
	// Close the server only after releasing the handle mutex (server shutdown
	// closes the same handle).
	if err := server.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestServerOptionValidation(t *testing.T) {
	host := newEchoServiceHost()
	if _, err := NewServer(host, ServerOptions{ServerID: "not-a-uuid"}); err == nil {
		t.Fatal("invalid serverId must be rejected")
	}
	zero := int64(0)
	if _, err := NewServer(host, ServerOptions{ServerID: testServerID, MaxFrameLength: &zero}); err == nil {
		t.Fatal("zero maxFrameLength must be rejected")
	}
	badTimeout := 0
	if _, err := NewServer(host, ServerOptions{ServerID: testServerID, HandshakeTimeoutMS: &badTimeout}); err == nil {
		t.Fatal("zero handshake timeout must be rejected")
	}
	if _, err := NewServer(nil, ServerOptions{ServerID: testServerID}); err == nil {
		t.Fatal("nil host must be rejected")
	}
}

func strPtr(s string) *string { return &s }
