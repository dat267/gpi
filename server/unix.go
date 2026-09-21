package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dat267/pier/protocol"
)

// Port of src/transports/unix/*.ts.

const (
	defaultSocketMode             = 0o600
	defaultGracefulCloseTimeoutMS = 5_000
	socketProbeTimeout            = time.Second
	unixSocketSuffix              = ".sock"
	defaultSocketDirectoryMode    = 0o700
)

// UnixListenerOptions configures a Unix-domain socket listener.
type UnixListenerOptions struct {
	Path string
	// Mode is the socket filesystem permission. Defaults to 0o600.
	Mode *int
	// MaxPendingBytes bounds queued bytes per connection before a slow peer is
	// disconnected. Defaults to MaxFrameLength * 4.
	MaxPendingBytes *int
	// GracefulCloseTimeoutMS bounds how long a closing connection waits.
	GracefulCloseTimeoutMS *int
	// MaxFrameLength must match the server when customized.
	MaxFrameLength *int64
	OnError        func(err error)
}

// UnixServerOptions composes ServerOptions with the listener options.
type UnixServerOptions struct {
	ServerOptions
	UnixListenerOptions
}

// GetUnixSocketPath derives the local Unix socket path for one server identity.
func GetUnixSocketPath(serverID, serverDirectory string) (string, error) {
	if !protocol.IsServerID(serverID) {
		return "", fmt.Errorf("Unix serverId must be a canonical lowercase UUIDv4")
	}
	return filepath.Join(serverDirectory, serverID+unixSocketSuffix), nil
}

// CreateUnixServer composes a Server with one Unix-domain socket listener.
func CreateUnixServer(host ServerHost, options UnixServerOptions) (*Server, error) {
	listener, err := CreateUnixListener(options.UnixListenerOptions)
	if err != nil {
		return nil, err
	}
	serverOptions := options.ServerOptions
	serverOptions.Listeners = []ServerListener{listener}
	return NewServer(host, serverOptions)
}

type resolvedUnixListenerOptions struct {
	path                   string
	mode                   int
	maxPendingBytes        int
	gracefulCloseTimeoutMS int
	onError                func(err error)
}

type fileIdentity struct {
	dev uint64
	ino uint64
}

// UnixListener is the Unix-domain socket ServerListener.
type UnixListener struct {
	options resolvedUnixListenerOptions

	mu          sync.Mutex
	connections map[*UnixByteConnection]bool
	listener    net.Listener
	accept      ByteConnectionAcceptor
	socketID    *fileIdentity
	ownedPath   string
	closing     bool
	closeDone   chan struct{}
	closeErr    error
	closeOnce   sync.Once
	started     bool
}

// CreateUnixListener builds a Unix-domain socket listener.
func CreateUnixListener(options UnixListenerOptions) (*UnixListener, error) {
	resolved, err := resolveUnixListenerOptions(options)
	if err != nil {
		return nil, err
	}
	return &UnixListener{
		options:     resolved,
		connections: map[*UnixByteConnection]bool{},
		closeDone:   make(chan struct{}),
	}, nil
}

func resolveUnixListenerOptions(options UnixListenerOptions) (resolvedUnixListenerOptions, error) {
	if options.Path == "" {
		return resolvedUnixListenerOptions{}, fmt.Errorf("Server Unix socket path must not be empty")
	}
	mode := defaultSocketMode
	if options.Mode != nil {
		mode = *options.Mode
	}
	if mode < 0 || mode > 0o777 {
		return resolvedUnixListenerOptions{}, fmt.Errorf("Server Unix socket mode must be an integer between 0 and 0o777")
	}
	maxFrameLength := int64(protocol.DefaultMaxFrameLength)
	if options.MaxFrameLength != nil {
		maxFrameLength = *options.MaxFrameLength
	}
	if maxFrameLength <= 0 || maxFrameLength > maxUint32Value {
		return resolvedUnixListenerOptions{}, fmt.Errorf("Server maxFrameLength must be an integer between 1 and %d", maxUint32Value)
	}
	maxPendingBytes := int(maxFrameLength) * 4
	if options.MaxPendingBytes != nil {
		maxPendingBytes = *options.MaxPendingBytes
	}
	if int64(maxPendingBytes) < maxFrameLength+4 {
		return resolvedUnixListenerOptions{}, fmt.Errorf("Server maxPendingBytes must be a safe integer at least maxFrameLength + 4")
	}
	gracefulCloseTimeoutMS := defaultGracefulCloseTimeoutMS
	if options.GracefulCloseTimeoutMS != nil {
		gracefulCloseTimeoutMS = *options.GracefulCloseTimeoutMS
	}
	if gracefulCloseTimeoutMS <= 0 || gracefulCloseTimeoutMS > maxTimerDelayMS {
		return resolvedUnixListenerOptions{}, fmt.Errorf("Server gracefulCloseTimeoutMs must be an integer between 1 and %d", maxTimerDelayMS)
	}
	return resolvedUnixListenerOptions{
		path:                   options.Path,
		mode:                   mode,
		maxPendingBytes:        maxPendingBytes,
		gracefulCloseTimeoutMS: gracefulCloseTimeoutMS,
		onError:                options.OnError,
	}, nil
}

// Start binds the socket and passes authorized connections to accept.
func (l *UnixListener) Start(accept ByteConnectionAcceptor) error {
	l.mu.Lock()
	if l.started {
		l.mu.Unlock()
		return fmt.Errorf("Unix listener is already started")
	}
	if l.closing {
		l.mu.Unlock()
		return fmt.Errorf("Unix listener is closing or closed")
	}
	l.accept = accept
	l.mu.Unlock()

	ownedPath := getOwnedBindPath(l.options.path)
	if err := os.MkdirAll(filepath.Dir(l.options.path), defaultSocketDirectoryMode); err != nil {
		return err
	}
	if err := removeStaleSocket(l.options.path); err != nil {
		return err
	}
	if err := removeStaleSocket(ownedPath); err != nil {
		return err
	}
	l.mu.Lock()
	l.ownedPath = ownedPath
	l.mu.Unlock()

	listener, err := net.Listen("unix", ownedPath)
	if err != nil {
		l.closeListenerAndCleanup(nil)
		l.mu.Lock()
		l.ownedPath = ""
		l.mu.Unlock()
		return err
	}
	if unixListener, ok := listener.(*net.UnixListener); ok {
		// Cleanup is explicit (upstream's cleanupOwnedSocket), not Go's
		// implicit unlink-on-close.
		unixListener.SetUnlinkOnClose(false)
	}
	if err := l.finishBind(listener, ownedPath); err != nil {
		l.closeListenerAndCleanup(listener)
		l.mu.Lock()
		l.ownedPath = ""
		l.mu.Unlock()
		return err
	}
	l.mu.Lock()
	l.listener = listener
	l.started = true
	l.mu.Unlock()
	go l.acceptLoop(listener)
	return nil
}

func (l *UnixListener) finishBind(listener net.Listener, ownedPath string) error {
	stats, err := os.Lstat(ownedPath)
	if err != nil {
		return err
	}
	if stats.Mode()&fs.ModeSocket == 0 {
		return fmt.Errorf("Unix listener path is not a socket after binding: %s", ownedPath)
	}
	identity := identityOf(stats)
	l.mu.Lock()
	l.socketID = identity
	l.mu.Unlock()
	if err := os.Link(ownedPath, l.options.path); err != nil {
		return err
	}
	if err := setSocketMode(l.options.path, l.options.mode); err != nil {
		return err
	}
	if err := removePath(ownedPath); err != nil {
		return err
	}
	l.mu.Lock()
	l.ownedPath = ""
	l.mu.Unlock()
	return nil
}

func (l *UnixListener) acceptLoop(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			l.mu.Lock()
			closing := l.closing
			l.mu.Unlock()
			if !closing {
				l.reportError(err)
			}
			return
		}
		// Each accepted socket is served independently (upstream's
		// createServer callback per socket); a serial loop would stop
		// accepting while any connection stayed open.
		go l.acceptSocket(conn)
	}
}

func (l *UnixListener) acceptSocket(conn net.Conn) {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		_ = conn.Close()
		return
	}
	l.mu.Lock()
	if l.closing {
		l.mu.Unlock()
		_ = conn.Close()
		return
	}
	connection := newUnixByteConnection(unixConn, l.options.gracefulCloseTimeoutMS, l.options.maxPendingBytes)
	l.connections[connection] = true
	accept := l.accept
	l.mu.Unlock()
	if accept == nil {
		_ = conn.Close()
		return
	}
	handler := accept(connection)

	buffer := make([]byte, 32*1024)
	for {
		n, err := unixConn.Read(buffer)
		if n > 0 {
			chunk := append([]byte{}, buffer[:n]...)
			handler.OnData(chunk)
		}
		if err != nil {
			if isBenignReadError(err) {
				connection.markClosed()
				l.forget(connection)
				handler.OnClose()
			} else {
				handler.OnError(err)
				_ = unixConn.Close()
				connection.markClosed()
				l.forget(connection)
				handler.OnClose()
			}
			return
		}
	}
}

func (l *UnixListener) forget(connection *UnixByteConnection) {
	l.mu.Lock()
	delete(l.connections, connection)
	l.mu.Unlock()
}

// Close stops listening and releases the socket.
func (l *UnixListener) Close() error {
	l.closeOnce.Do(func() {
		l.mu.Lock()
		l.closing = true
		l.mu.Unlock()
		l.closeErr = l.closeInternal()
		close(l.closeDone)
	})
	<-l.closeDone
	return l.closeErr
}

func (l *UnixListener) closeInternal() error {
	l.mu.Lock()
	listener := l.listener
	connections := make([]*UnixByteConnection, 0, len(l.connections))
	for connection := range l.connections {
		connections = append(connections, connection)
	}
	l.mu.Unlock()

	var errs []error
	if listener != nil {
		if err := l.closeListenerAndCleanup(listener); err != nil {
			errs = append(errs, err)
		}
	} else if err := l.cleanupOwnedSocket(); err != nil {
		errs = append(errs, err)
	}
	for _, connection := range connections {
		if err := connection.Close(nil); err != nil {
			errs = append(errs, err)
		}
	}
	l.mu.Lock()
	l.ownedPath = ""
	l.connections = map[*UnixByteConnection]bool{}
	l.listener = nil
	l.mu.Unlock()
	return errors.Join(errs...)
}

func (l *UnixListener) closeListenerAndCleanup(listener net.Listener) error {
	var errs []error
	if listener != nil {
		if err := listener.Close(); err != nil && !isClosedNetworkError(err) {
			l.reportError(err)
		}
	}
	// Remove an unpublished startup bind path before the public route.
	if err := l.cleanupOwnedBindPath(); err != nil {
		errs = append(errs, err)
	}
	if err := l.cleanupOwnedSocket(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (l *UnixListener) cleanupOwnedBindPath() error {
	l.mu.Lock()
	ownedPath := l.ownedPath
	l.ownedPath = ""
	l.mu.Unlock()
	if ownedPath == "" {
		return nil
	}
	return removePath(ownedPath)
}

func (l *UnixListener) cleanupOwnedSocket() error {
	l.mu.Lock()
	identity := l.socketID
	l.socketID = nil
	l.mu.Unlock()
	if identity == nil {
		return nil
	}
	current, err := os.Lstat(l.options.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	currentID := identityOf(current)
	if current.Mode()&fs.ModeSocket == 0 || currentID == nil || *currentID != *identity {
		return nil
	}

	preserved := filepath.Join(filepath.Dir(l.options.path), "cleanup-"+shortRandomID())
	if err := os.Rename(l.options.path, preserved); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	moved, err := os.Lstat(preserved)
	if err != nil {
		return err
	}
	movedID := identityOf(moved)
	if moved.Mode()&fs.ModeSocket != 0 && movedID != nil && *movedID == *identity {
		return removePath(preserved)
	}
	if _, err := os.Lstat(l.options.path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			_ = os.Rename(preserved, l.options.path)
		} else {
			return err
		}
	}
	return fmt.Errorf("Unix listener path changed during cleanup; preserved replacement at %s", preserved)
}

func (l *UnixListener) reportError(err error) {
	if l.options.onError == nil || err == nil {
		return
	}
	defer func() { _ = recover() }()
	l.options.onError(err)
}

// UnixByteConnection is the socket-backed ByteConnection.
type UnixByteConnection struct {
	conn                 *net.UnixConn
	gracefulCloseTimeout time.Duration
	maxPendingBytes      int

	mu           sync.Mutex
	pendingBytes int
	closed       bool
	closing      bool
	writeMu      sync.Mutex
}

func newUnixByteConnection(conn *net.UnixConn, gracefulCloseTimeoutMS, maxPendingBytes int) *UnixByteConnection {
	return &UnixByteConnection{
		conn:                 conn,
		gracefulCloseTimeout: time.Duration(gracefulCloseTimeoutMS) * time.Millisecond,
		maxPendingBytes:      maxPendingBytes,
	}
}

// Closed reports whether the connection is closed.
func (c *UnixByteConnection) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// Send writes one framed chunk, enforcing the pending-byte bound.
func (c *UnixByteConnection) Send(chunk []byte) error {
	c.mu.Lock()
	if c.closed || c.closing {
		c.mu.Unlock()
		return fmt.Errorf("Unix connection is closed")
	}
	if c.pendingBytes+len(chunk) > c.maxPendingBytes {
		c.mu.Unlock()
		return fmt.Errorf("Unix connection exceeded its pending byte limit")
	}
	c.pendingBytes += len(chunk)
	c.mu.Unlock()

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	c.pendingBytes -= len(chunk)
	closed := c.closed || c.closing
	c.mu.Unlock()
	if closed {
		return fmt.Errorf("Unix connection is closed")
	}
	if _, err := c.conn.Write(chunk); err != nil {
		return err
	}
	return nil
}

// Close half-closes the connection, sending finalChunk first when present.
//
// D16: upstream's close() returns a promise that settles on the socket "close"
// event (after the peer also closes) and queues the final chunk behind the
// async write tail; the Go port writes the final chunk synchronously, half
// closes the write side, marks the connection closed immediately, and force
// closes the socket after the graceful window. Observable behavior (no further
// sends, final frame delivered before teardown, bounded lingering) is the
// same, but Close does not block a caller on the peer. The pending-byte bound
// is enforced too, though a blocking write means it is rarely reached
// (upstream queues writes asynchronously).
func (c *UnixByteConnection) Close(finalChunk []byte) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	if c.closing {
		c.mu.Unlock()
		return nil
	}
	c.closing = true
	c.mu.Unlock()

	c.writeMu.Lock()
	if len(finalChunk) > 0 {
		_, _ = c.conn.Write(finalChunk)
	}
	_ = c.conn.CloseWrite()
	c.writeMu.Unlock()

	c.markClosed()
	// A peer that never reads must not pin the socket forever; upstream
	// destroys the socket after the graceful close window.
	timeout := c.gracefulCloseTimeout
	time.AfterFunc(timeout, func() { _ = c.conn.Close() })
	return nil
}

func (c *UnixByteConnection) markClosed() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.closing = true
	c.mu.Unlock()
}

// Socket helpers.

func getOwnedBindPath(path string) string {
	sum := sha256.Sum256([]byte(path))
	return filepath.Join(filepath.Dir(path), "bind-"+hex.EncodeToString(sum[:])[:8])
}

func removeStaleSocket(path string) error {
	original, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	if original.Mode()&fs.ModeSocket == 0 {
		return fmt.Errorf("Refusing to remove non-socket Unix listener path: %s", path)
	}
	live, err := isSocketLive(path)
	if err != nil {
		return err
	}
	if live {
		return fmt.Errorf("Unix listener is already running: %s", path)
	}

	preserved := filepath.Join(filepath.Dir(path), "stale-"+shortRandomID())
	if err := os.Rename(path, preserved); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	current, err := os.Lstat(preserved)
	if err != nil {
		return err
	}
	currentID := identityOf(current)
	originalID := identityOf(original)
	if current.Mode()&fs.ModeSocket == 0 || currentID == nil || originalID == nil || *currentID != *originalID {
		if _, err := os.Lstat(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				_ = os.Rename(preserved, path)
			} else {
				return err
			}
		}
		return fmt.Errorf("Unix listener path changed while checking for a stale socket: %s", path)
	}
	return removePath(preserved)
}

func removePath(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// isSocketLive probes the socket path; a live listener accepts the connection.
func isSocketLive(path string) (bool, error) {
	conn, err := net.DialTimeout("unix", path, socketProbeTimeout)
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return true, nil
		}
		if isConnectionRefusedError(err) {
			return false, nil
		}
		return false, nil
	}
	_ = conn.Close()
	return true, nil
}

func setSocketMode(path string, mode int) error {
	if err := os.Chmod(path, fs.FileMode(mode)); err != nil {
		if errors.Is(err, fs.ErrInvalid) || errors.Is(err, errors.ErrUnsupported) {
			return nil
		}
		return err
	}
	return nil
}

func isBenignReadError(err error) bool {
	if errors.Is(err, net.ErrClosed) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	if err == nil {
		return true
	}
	return strings.Contains(err.Error(), "use of closed network connection") ||
		strings.Contains(err.Error(), "EOF")
}

func isClosedNetworkError(err error) bool {
	return errors.Is(err, net.ErrClosed)
}

func isConnectionRefusedError(err error) bool {
	return strings.Contains(err.Error(), "connection refused") ||
		errors.Is(err, fs.ErrNotExist) ||
		errors.Is(err, fs.ErrPermission)
}

// shortRandomID mirrors upstream's randomUUID().slice(0, 6) suffix for
// preserved-path naming.
func shortRandomID() string {
	id, err := newRandomID()
	if err != nil {
		return "000000"
	}
	return id[:6]
}
