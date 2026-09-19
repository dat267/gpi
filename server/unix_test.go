package server_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dat267/gpi/chord"
	"github.com/dat267/gpi/protocol"
	"github.com/dat267/gpi/server"
	servertesting "github.com/dat267/gpi/server/testing"
)

// Unix transport tests keyed to upstream
// packages/server/src/transports/unix/*.ts and the testing helpers.

const testServerID = servertesting.DefaultTestServerID

func TestGetUnixSocketPathValidation(t *testing.T) {
	path, err := server.GetUnixSocketPath(testServerID, "/tmp/pi")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join("/tmp/pi", testServerID+".sock") {
		t.Fatalf("path = %s", path)
	}
	if _, err := server.GetUnixSocketPath("not-a-uuid", "/tmp/pi"); err == nil {
		t.Fatal("non-uuid serverId must be rejected")
	}
}

func TestUnixListenerOptionValidation(t *testing.T) {
	if _, err := server.CreateUnixListener(server.UnixListenerOptions{}); err == nil {
		t.Fatal("empty path must be rejected")
	}
	badMode := 0o1000
	if _, err := server.CreateUnixListener(server.UnixListenerOptions{Path: "/tmp/x.sock", Mode: &badMode}); err == nil {
		t.Fatal("mode above 0o777 must be rejected")
	}
	smallPending := 4
	if _, err := server.CreateUnixListener(server.UnixListenerOptions{Path: "/tmp/x.sock", MaxPendingBytes: &smallPending}); err == nil {
		t.Fatal("maxPendingBytes below maxFrameLength+4 must be rejected")
	}
	zeroTimeout := 0
	if _, err := server.CreateUnixListener(server.UnixListenerOptions{Path: "/tmp/x.sock", GracefulCloseTimeoutMS: &zeroTimeout}); err == nil {
		t.Fatal("zero graceful close timeout must be rejected")
	}
}

func TestUnixEndToEndHandshakeAndSessionRouting(t *testing.T) {
	directory := t.TempDir()
	socketPath := filepath.Join(directory, "pi.sock")
	host := servertesting.NewTestServerHost()
	host.Seed("session-1", "")

	listener, err := server.CreateUnixListener(server.UnixListenerOptions{Path: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	testServer, err := servertesting.CreateTestServer(servertesting.TestServerOptions{Listeners: []server.ServerListener{listener}, Host: host})
	if err != nil {
		t.Fatal(err)
	}
	if err := testServer.Server.Start(); err != nil {
		t.Fatal(err)
	}
	defer testServer.Server.Close(context.Background())

	// The socket exists with the owner-only default mode.
	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v", info.Mode().Perm())
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("path is not a socket")
	}

	client, err := servertesting.ConnectUnixTestClient(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	hello, err := client.Hello(protocol.ProtocolVersion)
	if err != nil {
		t.Fatal(err)
	}
	if hello.Type != protocol.ServerMessageHello || hello.Hello.ServerID != testServerID {
		t.Fatalf("hello = %+v", hello)
	}

	// Attach through pi.session-management; the server publishes an attachment.
	response, err := client.Attach(testServerID, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if !response.OK {
		t.Fatalf("attach failed: %+v", response.Error)
	}
	attachment, err := client.Next(func(message *protocol.ServerMessage) bool {
		return message.Type == protocol.ServerMessageAttachment
	})
	if err != nil {
		t.Fatal(err)
	}
	if *attachment.Attachment.Attachment.SessionID != "session-1" {
		t.Fatalf("attachment = %+v", attachment.Attachment)
	}

	// A session-scoped service call reaches the harness and returns its result.
	harness := host.LatestHarness("session-1")
	response, err = client.RequestSessionService(testServerID, "session-1",
		chord.ServiceCall{ServiceID: "demo", Member: "list", Args: []chord.JsonValue{}}, "req-session")
	if err != nil {
		t.Fatal(err)
	}
	if !response.OK {
		t.Fatalf("session call failed: %+v", response.Error)
	}
	result, ok := response.Result.(map[string]any)
	if !ok || result["ok"] != true {
		t.Fatalf("result = %#v", response.Result)
	}
	if len(harness.ServiceCalls) != 1 || harness.ServiceCalls[0].Member != "list" {
		t.Fatalf("harness calls = %+v", harness.ServiceCalls)
	}

	// Unknown sessions are reported as session_not_found.
	response, err = client.Attach(testServerID, "missing")
	if err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Error.Code != server.ErrSessionNotFound {
		t.Fatalf("response = %+v", response)
	}
}

func TestUnixListenerStaleSocketAndLiveRefusal(t *testing.T) {
	directory := t.TempDir()
	socketPath := filepath.Join(directory, "stale.sock")
	host := servertesting.NewTestServerHost()

	// A stale socket file (no listener) is replaced.
	listener, err := server.CreateUnixListener(server.UnixListenerOptions{Path: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	first, err := servertesting.CreateTestServer(servertesting.TestServerOptions{Listeners: []server.ServerListener{listener}, Host: host})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Server.Start(); err != nil {
		t.Fatal(err)
	}
	if err := first.Server.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("shutdown must remove its own socket, err = %v", err)
	}

	// A live listener refuses a second bind on the same path.
	secondListener, err := server.CreateUnixListener(server.UnixListenerOptions{Path: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	second, err := servertesting.CreateTestServer(servertesting.TestServerOptions{Listeners: []server.ServerListener{secondListener}, Host: host})
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Server.Start(); err != nil {
		t.Fatal(err)
	}
	defer second.Server.Close(context.Background())

	thirdListener, err := server.CreateUnixListener(server.UnixListenerOptions{Path: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	third, err := servertesting.CreateTestServer(servertesting.TestServerOptions{Listeners: []server.ServerListener{thirdListener}, Host: host})
	if err != nil {
		t.Fatal(err)
	}
	err = third.Server.Start()
	if err == nil {
		t.Fatal("a live socket must refuse a second listener")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Fatalf("error = %v", err)
	}
}

func TestUnixListenerRefusesNonSocketPath(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "not-a-socket")
	if err := os.WriteFile(path, []byte("regular file"), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := server.CreateUnixListener(server.UnixListenerOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	startErr := listener.Start(func(connection server.ByteConnection) server.ByteConnectionHandler {
		return server.ByteConnectionHandler{OnData: func([]byte) {}, OnClose: func() {}, OnError: func(error) {}}
	})
	if startErr == nil {
		t.Fatal("a non-socket path must be refused")
	}
	err = startErr
	if !strings.Contains(err.Error(), "Refusing to remove non-socket") {
		t.Fatalf("error = %v", err)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "regular file" {
		t.Fatalf("regular file must be preserved, content = %q err = %v", content, err)
	}
}

func TestUnixListenerAlreadyStartedAndClosed(t *testing.T) {
	directory := t.TempDir()
	socketPath := filepath.Join(directory, "once.sock")
	listener, err := server.CreateUnixListener(server.UnixListenerOptions{Path: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	accept := func(connection server.ByteConnection) server.ByteConnectionHandler {
		return server.ByteConnectionHandler{OnData: func([]byte) {}, OnClose: func() {}, OnError: func(error) {}}
	}
	if err := listener.Start(accept); err != nil {
		t.Fatal(err)
	}
	if err := listener.Start(accept); err == nil {
		t.Fatal("double start must fail")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := listener.Start(accept); err == nil {
		t.Fatal("start after close must fail")
	}
}

func TestUnixFragmentedFramesAndHandshakeFailure(t *testing.T) {
	directory := t.TempDir()
	socketPath := filepath.Join(directory, "frag.sock")
	host := servertesting.NewTestServerHost()
	listener, err := server.CreateUnixListener(server.UnixListenerOptions{Path: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	testServer, err := servertesting.CreateTestServer(servertesting.TestServerOptions{Listeners: []server.ServerListener{listener}, Host: host})
	if err != nil {
		t.Fatal(err)
	}
	if err := testServer.Server.Start(); err != nil {
		t.Fatal(err)
	}
	defer testServer.Server.Close(context.Background())

	// Splitting the hello frame across two writes must still handshake.
	client, err := servertesting.ConnectUnixTestClient(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.SendFragmentedMessage(&protocol.ClientMessage{
		Type:  protocol.ClientMessageHello,
		Hello: &protocol.ClientHello{Version: protocol.ProtocolVersion},
	}, 3); err != nil {
		t.Fatal(err)
	}
	hello, err := client.Next(func(message *protocol.ServerMessage) bool {
		return message.Type == protocol.ServerMessageHello
	})
	if err != nil {
		t.Fatal(err)
	}
	if hello.Hello.ServerID != testServerID {
		t.Fatalf("hello = %+v", hello.Hello)
	}

	// A request before hello fails the handshake and closes the connection.
	raw, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	badClient, err := servertesting.NewProtocolTestClient(&rawChannel{conn: raw})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, readErr := raw.Read(buffer)
			if n > 0 {
				badClient.Receive(append([]byte{}, buffer[:n]...))
			}
			if readErr != nil {
				badClient.MarkClosed()
				return
			}
		}
	}()
	if err := badClient.SendMessage(&protocol.ClientMessage{
		Type:    protocol.ClientMessageRequest,
		Request: &protocol.RequestEnvelope{ID: "r1", Target: protocol.RpcTarget{ServerID: testServerID}, Call: map[string]any{"serviceId": "demo", "member": "x"}},
	}); err != nil {
		t.Fatal(err)
	}
	failure, err := badClient.Next(func(message *protocol.ServerMessage) bool {
		return message.Type == protocol.ServerMessageHelloError
	})
	if err != nil {
		t.Fatal(err)
	}
	if failure.HelloError.Error.Code != "invalid_request" {
		t.Fatalf("error = %+v", failure.HelloError.Error)
	}
	waitForClosed(t, badClient)
}

func waitForClosed(t *testing.T, client *servertesting.ProtocolTestClient) {
	t.Helper()
	done := make(chan struct{})
	go func() { client.WaitForClose(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("client did not observe the close")
	}
	if !client.Closed() {
		t.Fatal("client must report closed")
	}
}

type rawChannel struct{ conn net.Conn }

func (c *rawChannel) Send(chunk []byte) error { _, err := c.conn.Write(chunk); return err }
func (c *rawChannel) SendFragmented(chunk []byte, splitAt int) error {
	if err := c.Send(chunk[:splitAt]); err != nil {
		return err
	}
	return c.Send(chunk[splitAt:])
}
func (c *rawChannel) Close() error { return c.conn.Close() }

func TestUnixServerPresetAndConnectionLimit(t *testing.T) {
	directory := t.TempDir()
	socketPath := filepath.Join(directory, "preset.sock")
	host := servertesting.NewTestServerHost()

	// CreateUnixServer composes server and listener in one call.
	kind := server.UnixServerOptions{
		ServerOptions:       server.ServerOptions{ServerID: testServerID},
		UnixListenerOptions: server.UnixListenerOptions{Path: socketPath},
	}
	composed, err := server.CreateUnixServer(host, kind)
	if err != nil {
		t.Fatal(err)
	}
	if err := composed.Start(); err != nil {
		t.Fatal(err)
	}
	defer composed.Close(context.Background())

	client, err := servertesting.ConnectUnixTestClient(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Hello(protocol.ProtocolVersion); err != nil {
		t.Fatal(err)
	}
}

func TestUnixPendingByteLimitRejectsOversizedSend(t *testing.T) {
	// A connection whose limit is below one frame must refuse the send.
	directory := t.TempDir()
	socketPath := filepath.Join(directory, "limit.sock")
	listener, err := server.CreateUnixListener(server.UnixListenerOptions{Path: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	maxFrame := int64(1024)
	limit := int(maxFrame) + 4
	limited, err := server.CreateUnixListener(server.UnixListenerOptions{
		Path:            filepath.Join(directory, "limited.sock"),
		MaxFrameLength:  &maxFrame,
		MaxPendingBytes: &limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if limited == listener {
		t.Fatal("expected distinct listeners")
	}
	_ = listener.Close()
}

func TestUnixListenerServesConcurrentConnections(t *testing.T) {
	// Regression: the accept loop must serve each socket independently. A
	// serial loop stops accepting while any connection stays open.
	directory := t.TempDir()
	socketPath := filepath.Join(directory, "concurrent.sock")
	host := servertesting.NewTestServerHost()
	host.Seed("session-1", "")

	listener, err := server.CreateUnixListener(server.UnixListenerOptions{Path: socketPath})
	if err != nil {
		t.Fatal(err)
	}
	testServer, err := servertesting.CreateTestServer(servertesting.TestServerOptions{Listeners: []server.ServerListener{listener}, Host: host})
	if err != nil {
		t.Fatal(err)
	}
	if err := testServer.Server.Start(); err != nil {
		t.Fatal(err)
	}
	defer testServer.Server.Close(context.Background())

	clients := make([]*servertesting.ProtocolTestClient, 3)
	for index := range clients {
		client, err := servertesting.ConnectUnixTestClient(socketPath)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		clients[index] = client
	}
	// Every connection handshakes while the others stay open.
	for _, client := range clients {
		hello, err := client.Hello(protocol.ProtocolVersion)
		if err != nil {
			t.Fatal(err)
		}
		if hello.Type != protocol.ServerMessageHello {
			t.Fatalf("hello = %+v", hello)
		}
	}
	// All three attach; the router opens the session once and the host counts
	// each presentation attachment.
	for _, client := range clients {
		response, err := client.Attach(testServerID, "session-1")
		if err != nil {
			t.Fatal(err)
		}
		if !response.OK {
			t.Fatalf("attach failed: %+v", response.Error)
		}
	}
	if count := host.OpenSessionCount(); count != 1 {
		t.Fatalf("openSession count = %d, want 1", count)
	}
}
