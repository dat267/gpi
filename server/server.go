package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/dat267/pier/chord"
	"github.com/dat267/pier/chord/services"
	"github.com/dat267/pier/protocol"
)

// Port of src/server.ts: the framed-CBOR server.

const (
	defaultHandshakeTimeoutMS = 5_000
	maxUint32Value            = 0xffff_ffff
	maxTimerDelayMS           = 2_147_483_647
)

// Server hosts sessions over framed protocol connections.
type Server struct {
	mu sync.Mutex

	serverID string
	host     ServerHost

	listeners          []ServerListener
	maxFrameLength     int64
	handshakeTimeoutMS int
	onCountChanged     func(count int)
	onError            func(err error)

	connections map[*ConnectionState]bool
	sessions    *SessionRouter

	closing   bool
	started   bool
	closeOnce sync.Once
	closeErr  error
	closedCh  chan struct{}
}

// NewServer builds a server with validated options.
func NewServer(host ServerHost, options ServerOptions) (*Server, error) {
	if host == nil {
		return nil, fmt.Errorf("server host is required")
	}
	if !protocol.IsServerID(options.ServerID) {
		return nil, fmt.Errorf("serverId must be a canonical lowercase UUIDv4")
	}
	maxFrameLength := int64(protocol.DefaultMaxFrameLength)
	if options.MaxFrameLength != nil {
		maxFrameLength = *options.MaxFrameLength
	}
	if maxFrameLength <= 0 || maxFrameLength > maxUint32Value {
		return nil, fmt.Errorf("Server maxFrameLength must be an integer between 1 and %d", maxUint32Value)
	}
	handshakeTimeoutMS := defaultHandshakeTimeoutMS
	if options.HandshakeTimeoutMS != nil {
		handshakeTimeoutMS = *options.HandshakeTimeoutMS
	}
	if handshakeTimeoutMS <= 0 || handshakeTimeoutMS > maxTimerDelayMS {
		return nil, fmt.Errorf("Server handshakeTimeoutMs must be an integer between 1 and %d", maxTimerDelayMS)
	}

	server := &Server{
		serverID:           options.ServerID,
		host:               host,
		listeners:          options.Listeners,
		maxFrameLength:     maxFrameLength,
		handshakeTimeoutMS: handshakeTimeoutMS,
		onCountChanged:     options.OnConnectionCountChanged,
		onError:            options.OnError,
		connections:        map[*ConnectionState]bool{},
		closedCh:           make(chan struct{}),
	}
	server.sessions = NewSessionRouter(SessionRouterOptions{
		Host:      host,
		ServerID:  server.serverID,
		IsClosing: func() bool { server.mu.Lock(); defer server.mu.Unlock(); return server.closing },
		PublishAttachment: func(client any, attachment *protocol.RpcTarget, ctx context.Context) error {
			state, ok := client.(*ConnectionState)
			if !ok {
				return nil
			}
			if !server.sendMessage(ctx, state, serverAttachmentMessage(attachment)) {
				return fmt.Errorf("failed to publish attachment")
			}
			return nil
		},
		ReportError: server.reportError,
	})
	return server, nil
}

// ServerID returns the logical server identity.
func (s *Server) ServerID() string { return s.serverID }

// Closed reports shutdown completion (upstream's `closed` promise).
func (s *Server) Closed() <-chan struct{} { return s.closedCh }

// Start begins listening on every listener.
func (s *Server) Start() error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("Server is already started")
	}
	if s.closing {
		s.mu.Unlock()
		return fmt.Errorf("Server is closing or closed")
	}
	s.mu.Unlock()

	var started []ServerListener
	for _, listener := range s.listeners {
		if err := listener.Start(s.Accept); err != nil {
			s.mu.Lock()
			s.closing = true
			s.mu.Unlock()
			var cleanupErrors []error
			for _, already := range started {
				if closeErr := already.Close(); closeErr != nil {
					cleanupErrors = append(cleanupErrors, closeErr)
				}
			}
			if closeErr := s.closeServerState(); closeErr != nil {
				cleanupErrors = append(cleanupErrors, closeErr)
			}
			if len(cleanupErrors) > 0 {
				s.settleClosed()
				return fmt.Errorf("Server startup and cleanup failed: %w", err)
			}
			s.settleClosed()
			return err
		}
		started = append(started, listener)
	}
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
	return nil
}

// Accept registers one established connection.
func (s *Server) Accept(connection ByteConnection) ByteConnectionHandler {
	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	if closing {
		_ = connection.Close(nil)
		return ByteConnectionHandler{
			OnData:  func([]byte) {},
			OnClose: func() {},
			OnError: func(err error) { s.reportError(err) },
		}
	}

	decoder, err := protocol.NewClientMessageDecoder(&protocol.FrameDecoderOptions{MaxFrameLength: &s.maxFrameLength})
	if err != nil {
		s.reportError(err)
	}
	timer := time.AfterFunc(time.Duration(s.handshakeTimeoutMS)*time.Millisecond, func() {
		s.mu.Lock()
		state := s.currentStateFor(connection)
		s.mu.Unlock()
		if state != nil {
			s.failProtocol(state, protocol.ProtocolError{Code: "invalid_request", Message: "Handshake timeout"})
		}
	})

	state := &ConnectionState{
		Connection:           connection,
		Decoder:              decoder,
		ServiceStateEncoders: map[string]services.ServiceStateEncoder{},
		Stage:                StageAwaitingHello,
		HandshakeTimeout:     func() { timer.Stop() },
		ActiveRequests:       map[string]*activeRequest{},
	}
	s.mu.Lock()
	s.connections[state] = true
	s.mu.Unlock()
	s.notifyConnectionCountChanged()

	// Inbound chunks are pumped in order by one goroutine per connection,
	// which is the Go equivalent of upstream's handshake promise queueing
	// later messages.
	inbound := make(chan []byte, 32)
	go s.pump(state, inbound)

	return ByteConnectionHandler{
		OnData: func(chunk []byte) {
			select {
			case inbound <- chunk:
			default:
				// A full inbound queue means the peer is flooding; failing the
				// protocol is safer than unbounded buffering.
				s.failProtocol(state, protocol.ProtocolError{Code: "invalid_request", Message: "Inbound queue overflow"})
			}
		},
		OnClose: func() { s.transportClosed(state) },
		OnError: func(err error) {
			s.reportError(err)
			_ = connection.Close(nil)
			s.disconnect(state)
		},
	}
}

func (s *Server) currentStateFor(connection ByteConnection) *ConnectionState {
	for state := range s.connections {
		if state.Connection == connection {
			return state
		}
	}
	return nil
}

// Close shuts the server down.
func (s *Server) Close(ctx context.Context) error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		s.mu.Unlock()
		s.closeErr = s.closeInternal(ctx)
	})
	return s.closeErr
}

func (s *Server) closeInternal(ctx context.Context) error {
	s.mu.Lock()
	s.started = false
	s.mu.Unlock()

	var errs []error
	for _, listener := range s.listeners {
		if err := listener.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if err := s.closeServerState(); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		s.settleClosed()
		if len(errs) == 1 {
			return errs[0]
		}
		return fmt.Errorf("Server shutdown failed: %w", errs[0])
	}
	s.settleClosed()
	return nil
}

func (s *Server) closeServerState() error {
	s.mu.Lock()
	connections := make([]*ConnectionState, 0, len(s.connections))
	for state := range s.connections {
		state.mu.Lock()
		state.Stage = StageClosing
		if state.HandshakeTimeout != nil {
			state.HandshakeTimeout()
		}
		state.mu.Unlock()
		connections = append(connections, state)
	}
	s.mu.Unlock()

	for _, state := range connections {
		_ = state.Connection.Close(nil)
		s.disconnect(state)
	}
	err := s.sessions.Close(context.Background())
	s.mu.Lock()
	s.connections = map[*ConnectionState]bool{}
	s.mu.Unlock()
	return err
}

// pump processes inbound chunks in order.
func (s *Server) pump(state *ConnectionState, inbound <-chan []byte) {
	for chunk := range inbound {
		if IsTerminalConnection(state) {
			continue
		}
		messages, err := state.Decoder.Push(chunk)
		if err != nil {
			s.failProtocol(state, s.toProtocolError(err))
			continue
		}
		for _, message := range messages {
			if IsTerminalConnection(state) {
				break
			}
			s.dispatchMessage(state, message)
		}
	}
}

func (s *Server) dispatchMessage(state *ConnectionState, message *protocol.ClientMessage) {
	state.mu.Lock()
	stage := state.Stage
	handshake := state.handshakeDone
	state.mu.Unlock()

	if stage == StageAwaitingHello {
		if message.Type != protocol.ClientMessageHello {
			s.failProtocol(state, protocol.ProtocolError{Code: "invalid_request", Message: "The first client message must be hello"})
			return
		}
		// Synchronous handshake inside the pump: later messages wait for it.
		s.finishHandshake(state, message)
		return
	}
	if message.Type == protocol.ClientMessageHello {
		s.failProtocol(state, protocol.ProtocolError{Code: "invalid_request", Message: "hello may only be sent as the first message"})
		return
	}
	if handshake != nil {
		<-handshake
	}
	state.mu.Lock()
	ready := state.Stage == StageReady && !state.Disconnected
	state.mu.Unlock()
	if !ready {
		return
	}
	if message.Type == protocol.ClientMessageCancel {
		s.handleCancel(state, message)
		return
	}
	go s.handleRequest(state, message)
}

func (s *Server) finishHandshake(state *ConnectionState, message *protocol.ClientMessage) {
	state.mu.Lock()
	if state.Stage != StageAwaitingHello {
		state.mu.Unlock()
		return
	}
	state.Stage = StageHandshaking
	done := make(chan struct{})
	state.handshakeDone = done
	state.mu.Unlock()
	defer close(done)

	if !protocol.IsSupportedProtocolVersion(message.Hello.Version) {
		s.failProtocol(state, protocol.ProtocolError{
			Code:    "version",
			Message: fmt.Sprintf("Unsupported protocol version %d; expected %d", message.Hello.Version, protocol.ProtocolVersion),
		})
		return
	}

	state.mu.Lock()
	skip := s.isClosingLocked() || state.Disconnected || state.Stage != StageHandshaking || state.Connection.Closed()
	state.mu.Unlock()
	if skip {
		return
	}

	servicesAttachment, err := s.host.ServerServices().AttachClient(context.Background(), &serverPresentation{server: s, state: state})
	if err != nil {
		s.failProtocol(state, s.toProtocolError(err))
		return
	}

	state.mu.Lock()
	skip = s.isClosingLocked() || state.Disconnected || state.Stage != StageHandshaking || state.Connection.Closed()
	state.mu.Unlock()
	if skip {
		_ = servicesAttachment.Release(context.Background())
		return
	}
	state.mu.Lock()
	state.ServerServices = servicesAttachment
	state.mu.Unlock()

	sent := s.sendMessage(context.Background(), state, &protocol.ServerMessage{
		Type:  protocol.ServerMessageHello,
		Hello: &protocol.ServerHello{Version: protocol.ProtocolVersion, ServerID: s.serverID},
	})
	state.mu.Lock()
	if sent && !state.Disconnected && state.Stage == StageHandshaking {
		state.Stage = StageReady
		if state.HandshakeTimeout != nil {
			state.HandshakeTimeout()
		}
	}
	state.mu.Unlock()
}

// serverPresentation adapts the router to the host's presentation interface.
type serverPresentation struct {
	server *Server
	state  *ConnectionState
}

func (p *serverPresentation) AttachSession(ctx context.Context, sessionID string) error {
	return p.server.sessions.AttachClient(ctx, p.state, sessionID)
}

func (p *serverPresentation) DetachSession(ctx context.Context) error {
	return p.server.sessions.DetachClient(ctx, p.state)
}

func (p *serverPresentation) PrepareSessionRemoval(ctx context.Context, sessionID string) error {
	return p.server.sessions.RemoveSession(ctx, sessionID)
}

func (s *Server) handleCancel(state *ConnectionState, message *protocol.ClientMessage) {
	if message.Cancel.Target.ServerID != s.serverID {
		return
	}
	state.mu.Lock()
	active := state.ActiveRequests[message.Cancel.ID]
	state.mu.Unlock()
	if active != nil && sameTarget(active.target, message.Cancel.Target) {
		active.cancel()
	}
}

func (s *Server) handleRequest(state *ConnectionState, message *protocol.ClientMessage) {
	envelope := message.Request

	state.mu.Lock()
	if _, exists := state.ActiveRequests[envelope.ID]; exists {
		state.mu.Unlock()
		_ = s.sendMessage(context.Background(), state, requestResponse(envelope.ID, false,
			protocol.ProtocolError{Code: "invalid_request", Message: "Request ID is already active"}, nil))
		return
	}
	state.mu.Unlock()

	call, err := services.ParseServiceCall(chordServiceCallValue(envelope.Call))
	if err != nil {
		_ = s.sendMessage(context.Background(), state, requestResponse(envelope.ID, false,
			protocol.ProtocolError{Code: "invalid_request", Message: "Invalid service call"}, nil))
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	active := &activeRequest{cancel: cancel, target: envelope.Target}
	state.mu.Lock()
	state.ActiveRequests[envelope.ID] = active
	state.mu.Unlock()

	control := services.DecodeServiceControlCall(call)
	var subscribing *services.ServiceControlCall
	if control != nil && control.Type == "subscribe" {
		subscribing = control
	}

	var pendingMu sync.Mutex
	var pendingUpdates []*services.ServiceProviderUpdate
	subscriptionReady := subscribing == nil
	installedEncoder := false
	responded := false

	publish := func(subscriptionID string, update *services.ServiceProviderUpdate, _ context.Context) error {
		if subscribing != nil && subscriptionID == subscribing.SubscriptionID && !subscriptionReady {
			pendingMu.Lock()
			pendingUpdates = append(pendingUpdates, update)
			pendingMu.Unlock()
			return nil
		}
		return s.sendServiceUpdate(context.Background(), state, subscriptionID, update)
	}

	defer func() {
		cancel()
		state.mu.Lock()
		if state.ActiveRequests[envelope.ID] == active {
			delete(state.ActiveRequests, envelope.ID)
		}
		state.mu.Unlock()
	}()

	fail := func(err error) {
		if subscribing != nil && installedEncoder && !responded {
			state.mu.Lock()
			delete(state.ServiceStateEncoders, subscribing.SubscriptionID)
			state.mu.Unlock()
		}
		if responded {
			s.reportError(err)
			_ = state.Connection.Close(nil)
			s.disconnect(state)
			return
		}
		bounded := s.toProtocolError(err)
		if ctx.Err() != nil {
			bounded = protocol.ProtocolError{Code: "cancelled", Message: "RPC request cancelled"}
		}
		_ = s.sendMessage(context.Background(), state, requestResponse(envelope.ID, false, bounded, nil))
	}

	result, err := s.invokeRequest(state, envelope.Target, call, publish, ctx)
	if err != nil {
		fail(err)
		return
	}
	if subscribing != nil {
		if result == nil {
			fail(&protocol.ProtocolValidationError{Message: "Service subscription did not return a snapshot"})
			return
		}
		snapshot, parseErr := services.ParseServiceSubscriptionSnapshot(result)
		if parseErr != nil {
			fail(parseErr)
			return
		}
		encoder := services.NewServiceStateEncoder()
		encoded, encodeErr := encoder.EncodeSnapshot(snapshot)
		if encodeErr != nil {
			fail(encodeErr)
			return
		}
		result = wireSnapshotValue(encoded)
		state.mu.Lock()
		state.ServiceStateEncoders[subscribing.SubscriptionID] = encoder
		state.mu.Unlock()
		installedEncoder = true
	} else if control != nil && control.Type == "unsubscribe" {
		state.mu.Lock()
		delete(state.ServiceStateEncoders, control.SubscriptionID)
		state.mu.Unlock()
	}

	if !s.sendMessage(context.Background(), state, requestResponse(envelope.ID, true, protocol.ProtocolError{}, result)) {
		return
	}
	responded = true

	if subscribing != nil {
		for {
			pendingMu.Lock()
			if len(pendingUpdates) == 0 {
				pendingMu.Unlock()
				break
			}
			next := pendingUpdates[0]
			pendingUpdates = pendingUpdates[1:]
			pendingMu.Unlock()
			_ = s.sendServiceUpdate(context.Background(), state, subscribing.SubscriptionID, next)
		}
		subscriptionReady = true
	}
}

// invokeRequest dispatches to the session router or the server services.
func (s *Server) invokeRequest(
	state *ConnectionState,
	target protocol.RpcTarget,
	call chord.ServiceCall,
	publish PublishFunc,
	ctx context.Context,
) (chord.JsonValue, error) {
	if target.ServerID != s.serverID {
		return nil, NewWrongServerError()
	}
	if target.SessionID != nil {
		return s.sessions.ExecuteServiceCall(ctx, state, target, call, publish)
	}
	state.mu.Lock()
	serverServices := state.ServerServices
	state.mu.Unlock()
	if serverServices == nil {
		return nil, &protocol.ProtocolValidationError{
			Message: fmt.Sprintf("Unknown service member %s.%s", call.ServiceID, call.Member),
		}
	}
	return serverServices.InvokeService(call, publish, ctx)
}

func (s *Server) transportClosed(state *ConnectionState) {
	state.mu.Lock()
	needsEnd := !state.Disconnected && state.Stage != StageClosing
	decoder := state.Decoder
	state.mu.Unlock()
	if needsEnd && decoder != nil {
		if err := decoder.End(); err != nil {
			s.reportError(err)
		}
	}
	s.disconnect(state)
}

func (s *Server) disconnect(state *ConnectionState) {
	state.mu.Lock()
	if state.Disconnected {
		state.mu.Unlock()
		return
	}
	state.Disconnected = true
	state.Stage = StageClosed
	if state.HandshakeTimeout != nil {
		state.HandshakeTimeout()
	}
	for _, request := range state.ActiveRequests {
		request.cancel()
	}
	state.ActiveRequests = map[string]*activeRequest{}
	state.ServiceStateEncoders = map[string]services.ServiceStateEncoder{}
	serverServices := state.ServerServices
	state.ServerServices = nil
	state.mu.Unlock()

	s.mu.Lock()
	removed := s.connections[state]
	delete(s.connections, state)
	s.mu.Unlock()
	if removed {
		s.notifyConnectionCountChanged()
	}

	go func() {
		if err := s.sessions.Disconnect(context.Background(), state); err != nil {
			s.reportError(err)
		}
		if serverServices != nil {
			if err := serverServices.Release(context.Background()); err != nil {
				s.reportError(err)
			}
		}
	}()
}

func (s *Server) sendServiceUpdate(ctx context.Context, state *ConnectionState, subscriptionID string, update *services.ServiceProviderUpdate) error {
	state.mu.Lock()
	encoder := state.ServiceStateEncoders[subscriptionID]
	state.mu.Unlock()
	if encoder == nil {
		return nil
	}
	wire, err := encoder.EncodeUpdate(update)
	if err != nil {
		return err
	}
	_ = s.sendMessage(ctx, state, &protocol.ServerMessage{
		Type: protocol.ServerMessageServiceUpdate,
		ServiceUpdate: &protocol.ServiceEventEnvelope{
			SubscriptionID: subscriptionID,
			Update:         wireUpdateValue(wire),
		},
	})
	return nil
}

// sendMu serializes frame writes per connection.
var sendLocks sync.Map // *ConnectionState → *sync.Mutex

func (s *Server) sendMessage(ctx context.Context, state *ConnectionState, message *protocol.ServerMessage) bool {
	state.mu.Lock()
	closed := state.Disconnected || state.Connection.Closed()
	state.mu.Unlock()
	if closed {
		return false
	}
	frame, err := protocol.EncodeServerMessage(message, &protocol.FrameDecoderOptions{MaxFrameLength: &s.maxFrameLength})
	if err != nil {
		s.reportError(err)
		_ = state.Connection.Close(nil)
		s.disconnect(state)
		return false
	}
	lockValue, _ := sendLocks.LoadOrStore(state, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()
	if err := state.Connection.Send(frame); err != nil {
		s.reportError(err)
		_ = state.Connection.Close(nil)
		s.disconnect(state)
		return false
	}
	return true
}

func (s *Server) failProtocol(state *ConnectionState, protocolError protocol.ProtocolError) {
	state.mu.Lock()
	if state.Disconnected || state.Stage == StageClosing || state.Stage == StageClosed {
		state.mu.Unlock()
		return
	}
	state.Stage = StageClosing
	if state.HandshakeTimeout != nil {
		state.HandshakeTimeout()
	}
	state.mu.Unlock()

	message := &protocol.ServerMessage{
		Type:       protocol.ServerMessageHelloError,
		HelloError: &protocol.ServerHelloError{Error: protocolError},
	}
	var finalFrame []byte
	if frame, err := protocol.EncodeServerMessage(message, &protocol.FrameDecoderOptions{MaxFrameLength: &s.maxFrameLength}); err == nil {
		finalFrame = frame
	} else {
		s.reportError(err)
	}
	_ = state.Connection.Close(finalFrame)
	s.disconnect(state)
}

// toProtocolError maps an error to a bounded protocol error.
func (s *Server) toProtocolError(err error) protocol.ProtocolError {
	if code, ok := ServerErrorCode(err); ok {
		return protocol.ProtocolError{Code: code, Message: err.Error()}
	}
	if remote, ok := err.(*services.RemoteServiceError); ok {
		return protocol.ProtocolError{Code: remote.Code, Message: remote.Message}
	}
	if validation, ok := err.(*protocol.ProtocolValidationError); ok {
		return protocol.ProtocolError{Code: "invalid_request", Message: validation.Message}
	}
	s.reportError(err)
	return protocol.ProtocolError{Code: "internal_error", Message: InternalServerErrorMessage}
}

func (s *Server) notifyConnectionCountChanged() {
	if s.onCountChanged == nil {
		return
	}
	s.mu.Lock()
	count := len(s.connections)
	s.mu.Unlock()
	defer func() { _ = recover() }()
	s.onCountChanged(count)
}

func (s *Server) reportError(err error) {
	if s.onError == nil || err == nil {
		return
	}
	defer func() { _ = recover() }()
	s.onError(err)
}

func (s *Server) settleClosed() {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.closedCh:
	default:
		close(s.closedCh)
	}
}

func (s *Server) isClosingLocked() bool { return s.closing }

func sameTarget(left, right protocol.RpcTarget) bool {
	if left.ServerID != right.ServerID {
		return false
	}
	if left.SessionID == nil || right.SessionID == nil {
		return left.SessionID == nil && right.SessionID == nil
	}
	return *left.SessionID == *right.SessionID &&
		derefString(left.AttachmentID) == derefString(right.AttachmentID)
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// requestResponse builds a response envelope.
func requestResponse(id string, ok bool, protocolError protocol.ProtocolError, result chord.JsonValue) *protocol.ServerMessage {
	response := &protocol.ResponseEnvelope{ID: id, OK: ok}
	if ok {
		response.Result = result
	} else {
		response.Error = &protocolError
	}
	return &protocol.ServerMessage{Type: protocol.ServerMessageResponse, Response: response}
}

// serverAttachmentMessage renders an attachment change.
func serverAttachmentMessage(attachment *protocol.RpcTarget) *protocol.ServerMessage {
	return &protocol.ServerMessage{
		Type:       protocol.ServerMessageAttachment,
		Attachment: &protocol.AttachmentEnvelope{Attachment: attachment},
	}
}

// chordServiceCallValue converts a decoded call value into the JSON map the
// chord parser expects.
func chordServiceCallValue(call any) any { return call }

// wireSnapshotValue renders a wire snapshot as a JSON value for the wire.
func wireSnapshotValue(snapshot *services.WireServiceSubscriptionSnapshot) chord.JsonValue {
	instances := make([]any, 0, len(snapshot.Instances))
	for _, instance := range snapshot.Instances {
		members := make([]any, 0, len(instance.Members))
		for _, member := range instance.Members {
			entry := map[string]any{"name": member.Name, "kind": member.Kind}
			if member.Kind == services.MemberState {
				ops := make([]any, 0, len(member.Ops))
				for _, op := range member.Ops {
					ops = append(ops, op.WireTuple())
				}
				entry["sequence"] = member.Sequence
				entry["ops"] = ops
			}
			members = append(members, entry)
		}
		value := map[string]any{"members": members}
		if instance.Instance != nil {
			value["instance"] = map[string]any{"key": instance.Instance.Key, "generation": instance.Instance.Generation}
		}
		instances = append(instances, value)
	}
	return map[string]any{"serviceId": snapshot.ServiceID, "mode": snapshot.Mode, "instances": instances}
}

// wireUpdateValue renders a wire provider update as a JSON value.
func wireUpdateValue(update *services.WireServiceProviderUpdate) chord.JsonValue {
	value := map[string]any{"type": update.Type}
	switch update.Type {
	case services.UpdateState:
		ops := make([]any, 0, len(update.Ops))
		for _, op := range update.Ops {
			ops = append(ops, op.WireTuple())
		}
		value["member"] = update.Member
		value["sequence"] = update.Sequence
		value["ops"] = ops
		if update.Instance != nil {
			value["instance"] = map[string]any{"key": update.Instance.Key, "generation": update.Instance.Generation}
		}
	case services.UpdateReplaced:
		value["snapshot"] = wireInstanceValue(update.Snapshot)
	case services.UpdateSpawned:
		value["instance"] = wireInstanceValue(update.SpawnedInstance)
	case services.UpdateClosed:
		if update.ClosedInstance != nil {
			value["instance"] = map[string]any{"key": update.ClosedInstance.Key, "generation": update.ClosedInstance.Generation}
		}
	}
	return value
}

func wireInstanceValue(instance *services.WireServiceInstanceSnapshot) any {
	if instance == nil {
		return nil
	}
	members := make([]any, 0, len(instance.Members))
	for _, member := range instance.Members {
		entry := map[string]any{"name": member.Name, "kind": member.Kind}
		if member.Kind == services.MemberState {
			ops := make([]any, 0, len(member.Ops))
			for _, op := range member.Ops {
				ops = append(ops, op.WireTuple())
			}
			entry["sequence"] = member.Sequence
			entry["ops"] = ops
		}
		members = append(members, entry)
	}
	value := map[string]any{"members": members}
	if instance.Instance != nil {
		value["instance"] = map[string]any{"key": instance.Instance.Key, "generation": instance.Instance.Generation}
	}
	return value
}

var (
	_ = json.Marshal
	_ = math.MaxInt64
	_ = strconv.Itoa
)
