package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dat267/pier/chord"
	"github.com/dat267/pier/protocol"
)

// Port of src/session-router.ts: per-client serialized routing, attachment
// leases, and hosted-session lifecycle.

// sessionCleanupError marks cleanup failures that shutdown must surface
// (upstream's SessionCleanupError).
type sessionCleanupError struct {
	message string
	causes  []error
}

func (e *sessionCleanupError) Error() string   { return e.message }
func (e *sessionCleanupError) Unwrap() []error { return e.causes }

// clientAttachment is one client's attachment to a hosted session.
type clientAttachment struct {
	id      string
	client  any
	session *hostedSession
	// operations tracks in-flight service calls for release ordering.
	operations sync.WaitGroup
	opMu       sync.Mutex
	acquiring  bool
	releasing  bool
	released   bool
	lease      RoutedSessionAttachment
}

func (a *clientAttachment) track(result func()) {
	a.opMu.Lock()
	a.operations.Add(1)
	a.opMu.Unlock()
	go func() {
		defer a.operations.Done()
		result()
	}()
}

// hostedSession is one open session handle plus its attachments.
type hostedSession struct {
	id          string
	handle      RoutedSessionHandle
	attachments map[*clientAttachment]bool
}

// SessionRouterOptions configure the router.
type SessionRouterOptions struct {
	Host      ServerHost
	ServerID  string
	IsClosing func() bool
	// PublishAttachment sends an attachment change to one client.
	PublishAttachment func(client any, attachment *protocol.RpcTarget, ctx context.Context) error
	ReportError       func(err error)
}

// SessionRouter routes service calls to hosted sessions and owns session
// lifecycle.
type SessionRouter struct {
	mu sync.Mutex

	options  SessionRouterOptions
	hosted   map[string]*hostedSession
	opening  map[string]chan struct{}
	byClient map[any]*clientAttachment
	// clientOps serializes operations per client (upstream's promise chain).
	clientOpsMutex map[any]*sync.Mutex
	disconnected   map[any]bool
	closeOnce      sync.Once
	closeErr       error
	closed         bool
}

// NewSessionRouter builds a router.
func NewSessionRouter(options SessionRouterOptions) *SessionRouter {
	return &SessionRouter{
		options:        options,
		hosted:         map[string]*hostedSession{},
		opening:        map[string]chan struct{}{},
		byClient:       map[any]*clientAttachment{},
		clientOpsMutex: map[any]*sync.Mutex{},
		disconnected:   map[any]bool{},
	}
}

func (r *SessionRouter) clientMutex(client any) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	mutex, ok := r.clientOpsMutex[client]
	if !ok {
		mutex = &sync.Mutex{}
		r.clientOpsMutex[client] = mutex
	}
	return mutex
}

// ExecuteServiceCall routes one service call for a client.
func (r *SessionRouter) ExecuteServiceCall(
	ctx context.Context,
	client any,
	target protocol.RpcTarget,
	call chord.ServiceCall,
	publish PublishFunc,
) (chord.JsonValue, error) {
	mutex := r.clientMutex(client)
	mutex.Lock()
	defer mutex.Unlock()
	return r.startServiceCall(ctx, client, target, call, publish)
}

// AttachClient attaches a client to a session.
func (r *SessionRouter) AttachClient(ctx context.Context, client any, sessionID string) error {
	if r.options.IsClosing() {
		return NewServerDrainingError()
	}
	mutex := r.clientMutex(client)
	mutex.Lock()
	defer mutex.Unlock()
	return r.attachClientNow(ctx, client, sessionID)
}

// DetachClient releases a client's attachment.
func (r *SessionRouter) DetachClient(ctx context.Context, client any) error {
	mutex := r.clientMutex(client)
	mutex.Lock()
	defer mutex.Unlock()
	r.mu.Lock()
	attachment := r.byClient[client]
	r.mu.Unlock()
	if attachment != nil {
		return r.releaseAttachment(ctx, attachment, true)
	}
	return nil
}

// RemoveSession releases every attachment and closes one session.
func (r *SessionRouter) RemoveSession(ctx context.Context, sessionID string) error {
	if r.options.IsClosing() {
		return NewServerDrainingError()
	}
	r.mu.Lock()
	hosted := r.hosted[sessionID]
	r.mu.Unlock()
	if hosted == nil {
		return nil
	}
	var errs []error
	attachments := r.attachmentsOf(hosted)
	for _, attachment := range attachments {
		if err := r.releaseAttachment(ctx, attachment, true); err != nil {
			errs = append(errs, err)
		}
	}
	if err := hosted.handle.Close(ctx); err != nil {
		errs = append(errs, err)
	}
	r.mu.Lock()
	if r.hosted[sessionID] == hosted {
		delete(r.hosted, sessionID)
	}
	r.mu.Unlock()
	if len(errs) == 1 {
		return errs[0]
	}
	if len(errs) > 1 {
		return &sessionCleanupError{message: fmt.Sprintf("Failed to close Session %s", sessionID), causes: errs}
	}
	return nil
}

// Disconnect releases a disconnecting client's attachment.
func (r *SessionRouter) Disconnect(ctx context.Context, client any) error {
	r.mu.Lock()
	r.disconnected[client] = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.disconnected, client)
		r.mu.Unlock()
	}()
	mutex := r.clientMutex(client)
	mutex.Lock()
	defer mutex.Unlock()
	r.mu.Lock()
	attachment := r.byClient[client]
	r.mu.Unlock()
	if attachment != nil {
		return r.releaseAttachment(ctx, attachment, false)
	}
	return nil
}

// Close releases every session and attachment.
func (r *SessionRouter) Close(ctx context.Context) error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		r.closeErr = r.closeInternal(ctx)
	})
	return r.closeErr
}

func (r *SessionRouter) closeInternal(ctx context.Context) error {
	// Wait for in-flight per-client operations and session openings.
	var closeErrors []error
	r.mu.Lock()
	clientMutexes := make([]*sync.Mutex, 0, len(r.clientOpsMutex))
	for _, mutex := range r.clientOpsMutex {
		clientMutexes = append(clientMutexes, mutex)
	}
	r.mu.Unlock()
	for _, mutex := range clientMutexes {
		mutex.Lock()
		mutex.Unlock()
	}

	r.mu.Lock()
	hosted := make([]*hostedSession, 0, len(r.hosted))
	for _, session := range r.hosted {
		hosted = append(hosted, session)
	}
	r.mu.Unlock()

	for _, session := range hosted {
		for _, attachment := range r.attachmentsOf(session) {
			if err := r.releaseAttachment(ctx, attachment, true); err != nil {
				closeErrors = append(closeErrors, err)
			}
		}
	}
	for _, session := range hosted {
		if err := session.handle.Close(ctx); err != nil {
			r.options.ReportError(err)
			closeErrors = append(closeErrors, err)
			continue
		}
		r.mu.Lock()
		if r.hosted[session.id] == session {
			delete(r.hosted, session.id)
		}
		r.mu.Unlock()
	}
	r.mu.Lock()
	r.byClient = map[any]*clientAttachment{}
	r.clientOpsMutex = map[any]*sync.Mutex{}
	r.mu.Unlock()
	if len(closeErrors) > 0 {
		return &sessionCleanupError{message: "Failed to close routed Sessions", causes: closeErrors}
	}
	return nil
}

func (r *SessionRouter) attachmentsOf(session *hostedSession) []*clientAttachment {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*clientAttachment, 0, len(session.attachments))
	for attachment := range session.attachments {
		out = append(out, attachment)
	}
	return out
}

func (r *SessionRouter) attachClientNow(ctx context.Context, client any, sessionID string) error {
	r.mu.Lock()
	if r.options.IsClosing() || r.disconnected[client] {
		r.mu.Unlock()
		return NewServerDrainingError()
	}
	current := r.byClient[client]
	if current != nil && current.session.id == sessionID {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()

	hosted, err := r.acquire(ctx, sessionID)
	if err != nil {
		return err
	}
	r.mu.Lock()
	if r.options.IsClosing() || r.disconnected[client] {
		r.mu.Unlock()
		return NewServerDrainingError()
	}
	r.mu.Unlock()
	if current != nil {
		if err := r.releaseAttachment(ctx, current, false); err != nil {
			return err
		}
	}

	attachment := &clientAttachment{id: newAttachmentID(), client: client, session: hosted}
	r.mu.Lock()
	hosted.attachments[attachment] = true
	r.mu.Unlock()

	lease, err := hosted.handle.AttachClient(ctx)
	if err != nil {
		r.mu.Lock()
		delete(hosted.attachments, attachment)
		r.mu.Unlock()
		return err
	}
	attachment.lease = lease

	r.mu.Lock()
	stale := r.hosted[hosted.id] != hosted || !hosted.attachments[attachment] ||
		r.disconnected[client] || r.options.IsClosing()
	if !stale {
		r.byClient[client] = attachment
	}
	r.mu.Unlock()
	if stale {
		if err := r.releaseAttachment(ctx, attachment, true); err != nil {
			return err
		}
		return NewServerDrainingError()
	}
	return r.options.PublishAttachment(client, &protocol.RpcTarget{
		ServerID: r.options.ServerID, SessionID: &sessionID, AttachmentID: &attachment.id,
	}, ctx)
}

func (r *SessionRouter) startServiceCall(
	ctx context.Context,
	client any,
	target protocol.RpcTarget,
	call chord.ServiceCall,
	publish PublishFunc,
) (chord.JsonValue, error) {
	attachment, err := r.requireAttachment(client, target)
	if err != nil {
		return nil, err
	}
	// The call runs while the client's operation lock is held (serialized per
	// client, as upstream's promise chain does).
	return attachment.lease.InvokeService(call, publish, ctx)
}

func (r *SessionRouter) requireAttachment(client any, target protocol.RpcTarget) (*clientAttachment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.options.IsClosing() || r.disconnected[client] {
		return nil, NewServerDrainingError()
	}
	if target.SessionID == nil {
		return nil, NewSessionNotAttachedError()
	}
	attachment := r.byClient[client]
	if attachment == nil || attachment.session.id != *target.SessionID ||
		target.AttachmentID == nil || attachment.id != *target.AttachmentID {
		return nil, NewSessionNotAttachedError()
	}
	return attachment, nil
}

func (r *SessionRouter) releaseAttachment(ctx context.Context, attachment *clientAttachment, publish bool) error {
	r.mu.Lock()
	if attachment.releasing || attachment.released {
		r.mu.Unlock()
		return nil
	}
	attachment.releasing = true
	r.mu.Unlock()

	var errs []error
	attachment.operations.Wait()
	if attachment.lease != nil {
		if err := attachment.lease.Release(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	r.clearAttachment(ctx, attachment, publish)
	r.mu.Lock()
	attachment.released = true
	attachment.releasing = false
	r.mu.Unlock()
	if len(errs) == 1 {
		return errs[0]
	}
	if len(errs) > 1 {
		return &sessionCleanupError{message: "Failed to release Session attachment", causes: errs}
	}
	return nil
}

func (r *SessionRouter) clearAttachment(ctx context.Context, attachment *clientAttachment, publish bool) {
	r.mu.Lock()
	delete(attachment.session.attachments, attachment)
	wasCurrent := r.byClient[attachment.client] == attachment
	if wasCurrent {
		delete(r.byClient, attachment.client)
	}
	r.mu.Unlock()
	if wasCurrent && publish {
		if err := r.options.PublishAttachment(attachment.client, nil, ctx); err != nil {
			r.options.ReportError(err)
		}
	}
}

// acquire returns the hosted session, opening it once (concurrent callers wait
// on the same opening).
func (r *SessionRouter) acquire(ctx context.Context, sessionID string) (*hostedSession, error) {
	for {
		r.mu.Lock()
		if existing := r.hosted[sessionID]; existing != nil {
			r.mu.Unlock()
			return existing, nil
		}
		if wait, opening := r.opening[sessionID]; opening {
			r.mu.Unlock()
			<-wait
			continue
		}
		wait := make(chan struct{})
		r.opening[sessionID] = wait
		r.mu.Unlock()

		hosted, err := r.open(ctx, sessionID)

		r.mu.Lock()
		delete(r.opening, sessionID)
		r.mu.Unlock()
		close(wait)
		return hosted, err
	}
}

func (r *SessionRouter) open(ctx context.Context, sessionID string) (*hostedSession, error) {
	metadata, err := r.options.Host.ResolveSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	handle, err := r.options.Host.OpenSession(ctx, metadata)
	if err != nil {
		return nil, err
	}
	if r.options.IsClosing() {
		if closeErr := handle.Close(ctx); closeErr != nil {
			r.options.ReportError(closeErr)
			return nil, &sessionCleanupError{
				message: "Failed to close routed Session acquired while draining",
				causes:  []error{NewServerDrainingError(), closeErr},
			}
		}
		return nil, NewServerDrainingError()
	}
	hosted := &hostedSession{id: metadata.ID, handle: handle, attachments: map[*clientAttachment]bool{}}
	r.mu.Lock()
	r.hosted[hosted.id] = hosted
	r.mu.Unlock()

	if terminated := handle.Terminated(); terminated != nil {
		go func() {
			err, _ := <-terminated
			r.invalidate(hosted, err)
		}()
	}
	return hosted, nil
}

// invalidate drops a terminated session and releases its attachments
// (upstream's invalidate; the background context matches upstream).
func (r *SessionRouter) invalidate(hosted *hostedSession, err error) {
	r.mu.Lock()
	if r.hosted[hosted.id] != hosted {
		r.mu.Unlock()
		return
	}
	delete(r.hosted, hosted.id)
	r.mu.Unlock()
	for _, attachment := range r.attachmentsOf(hosted) {
		if releaseErr := r.releaseAttachment(context.Background(), attachment, true); releaseErr != nil {
			r.options.ReportError(releaseErr)
		}
	}
	if err != nil {
		r.options.ReportError(err)
	}
}

func newAttachmentID() string {
	// Upstream uses node:crypto randomUUID() (v4) for attachment ids.
	id, err := newRandomID()
	if err != nil {
		return fmt.Sprintf("attachment-%d", time.Now().UnixNano())
	}
	return id
}

var _ = errors.Is
