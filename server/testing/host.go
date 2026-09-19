package testing

import (
	"context"
	"sync"

	"github.com/dat267/gpi/chord"
	"github.com/dat267/gpi/server"
)

// TestHarness is a scriptable RoutedSessionHandle (upstream TestHarness).
//
// D17: upstream wraps a pi-agent-core Session; the Go harness models the
// harness-visible session state (id, termination, close counting) without
// depending on the harness session repo, which is not ported yet.
type TestHarness struct {
	ID       string
	mu       sync.Mutex
	Closed   *Deferred[struct{}]
	terminal *Deferred[error]

	AttachedClients        int
	AttachmentReleaseCount int
	CloseCount             int
	ServiceCalls           []chord.ServiceCall

	// FailAttachmentRelease makes the next attachment release fail.
	FailAttachmentRelease error
	// FailClose makes the next close fail.
	FailClose error
	// NextServiceError makes the next service call fail.
	NextServiceError error
	// NextServiceResult is the next service result (defaults to a fresh map).
	NextServiceResult chord.JsonValue

	nextCloseGate   *openGate
	nextServiceGate *openGate
}

// NewTestHarness builds a harness for one session id.
func NewTestHarness(id string) *TestHarness {
	return &TestHarness{
		ID:       id,
		Closed:   NewDeferred[struct{}](),
		terminal: NewDeferred[error](),
	}
}

// Terminated resolves with the termination error, or nil after an expected close.
func (h *TestHarness) Terminated() <-chan error { return h.terminal.Promise() }

// AttachClient hands out one scriptable attachment.
func (h *TestHarness) AttachClient(ctx context.Context) (server.RoutedSessionAttachment, error) {
	h.mu.Lock()
	h.AttachedClients++
	h.mu.Unlock()
	return &testHarnessAttachment{harness: h}, nil
}

// Close counts a close, honours the next gate, and settles termination.
func (h *TestHarness) Close(ctx context.Context) error {
	h.mu.Lock()
	h.CloseCount++
	gate := h.nextCloseGate
	h.nextCloseGate = nil
	h.mu.Unlock()
	if gate != nil {
		gate.Entered.Resolve(struct{}{})
		if _, err := gate.Release.Await(ctx); err != nil {
			return err
		}
	}

	h.mu.Lock()
	failClose := h.FailClose
	h.FailClose = nil
	h.mu.Unlock()
	if failClose != nil {
		return failClose
	}
	h.Closed.Resolve(struct{}{})
	h.terminal.Resolve(nil)
	return nil
}

// Terminate settles the harness as unexpectedly terminated.
func (h *TestHarness) Terminate(err error) {
	h.Closed.Resolve(struct{}{})
	h.terminal.Resolve(err)
}

// GateNextClose makes the next close block until the returned gate releases.
func (h *TestHarness) GateNextClose() *openGate {
	gate := newOpenGate()
	h.mu.Lock()
	h.nextCloseGate = gate
	h.mu.Unlock()
	return gate
}

// GateNextServiceCall makes the next service call block until released.
func (h *TestHarness) GateNextServiceCall() *openGate {
	gate := newOpenGate()
	h.mu.Lock()
	h.nextServiceGate = gate
	h.mu.Unlock()
	return gate
}

// InvokeService records a call, honouring the scripted error, gate, and result.
func (h *TestHarness) InvokeService(call chord.ServiceCall, publish server.PublishFunc, ctx context.Context) (chord.JsonValue, error) {
	h.mu.Lock()
	h.ServiceCalls = append(h.ServiceCalls, call)
	nextError := h.NextServiceError
	h.NextServiceError = nil
	gate := h.nextServiceGate
	h.nextServiceGate = nil
	h.mu.Unlock()
	if nextError != nil {
		return nil, nextError
	}
	if gate != nil {
		gate.Entered.Resolve(struct{}{})
		if _, err := gate.Release.Await(ctx); err != nil {
			return nil, err
		}
	}
	h.mu.Lock()
	result := h.NextServiceResult
	h.NextServiceResult = nil
	h.mu.Unlock()
	if result == nil {
		return map[string]any{"ok": true}, nil
	}
	return result, nil
}

type testHarnessAttachment struct {
	harness  *TestHarness
	mu       sync.Mutex
	released bool
}

func (a *testHarnessAttachment) InvokeService(call chord.ServiceCall, publish server.PublishFunc, ctx context.Context) (chord.JsonValue, error) {
	return a.harness.InvokeService(call, publish, ctx)
}

func (a *testHarnessAttachment) Release(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.released {
		return nil
	}
	a.harness.mu.Lock()
	a.harness.AttachmentReleaseCount++
	fail := a.harness.FailAttachmentRelease
	a.harness.mu.Unlock()
	if fail != nil {
		return fail
	}
	a.released = true
	a.harness.mu.Lock()
	a.harness.AttachedClients--
	a.harness.mu.Unlock()
	return nil
}

type openGate struct {
	Entered *Deferred[struct{}]
	Release *Deferred[struct{}]
}

func newOpenGate() *openGate {
	return &openGate{Entered: NewDeferred[struct{}](), Release: NewDeferred[struct{}]()}
}

// CreateTestServerServices builds the pi.session-management test service host.
func CreateTestServerServices() server.RoutedServerServiceHost {
	return &testServerServices{}
}

type testServerServices struct{}

func (testServerServices) AttachClient(ctx context.Context, presentation server.RoutedServerPresentation) (server.RoutedServerServiceAttachment, error) {
	return &testServerServicesAttachment{presentation: presentation}, nil
}

type testServerServicesAttachment struct {
	presentation server.RoutedServerPresentation
}

func (a *testServerServicesAttachment) InvokeService(call chord.ServiceCall, publish server.PublishFunc, ctx context.Context) (chord.JsonValue, error) {
	if call.Instance == nil && call.ServiceID == "pi.session-management" && call.Member == "attach" && len(call.Args) == 1 {
		if sessionID, ok := call.Args[0].(string); ok {
			if err := a.presentation.AttachSession(ctx, sessionID); err != nil {
				return nil, err
			}
			return nil, nil
		}
	}
	if call.Instance == nil && call.ServiceID == "pi.session-management" && call.Member == "detach" && len(call.Args) == 0 {
		if err := a.presentation.DetachSession(ctx); err != nil {
			return nil, err
		}
		return nil, nil
	}
	return nil, &server.ServerError{
		Code:    "invalid_request",
		Message: "Unsupported test server service " + call.ServiceID + "." + call.Member,
	}
}

func (a *testServerServicesAttachment) Release(ctx context.Context) error { return nil }

// TestServerHost is the deterministic ServerHost used by transport tests.
type TestServerHost struct {
	ServerServicesValue server.RoutedServerServiceHost

	mu             sync.Mutex
	metadata       []server.SessionMetadata
	harnesses      map[string][]*TestHarness
	openSessionNum int

	// NextOpenSessionError makes the next openSession fail.
	NextOpenSessionError error
	// NextHarnessCloseError makes the next opened harness fail its first close.
	NextHarnessCloseError error

	nextOpenSessionGate *openGate
}

// NewTestServerHost builds a seeded-deterministic test host.
func NewTestServerHost() *TestServerHost {
	return &TestServerHost{
		ServerServicesValue: CreateTestServerServices(),
		harnesses:           map[string][]*TestHarness{},
	}
}

// ServerServices returns the pi.session-management test services.
func (h *TestServerHost) ServerServices() server.RoutedServerServiceHost {
	return h.ServerServicesValue
}

// ResolveSession resolves a seeded session, mirroring the repo lookup errors.
func (h *TestServerHost) ResolveSession(ctx context.Context, sessionID string) (server.SessionMetadata, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var matches []server.SessionMetadata
	for _, metadata := range h.metadata {
		if metadata.ID == sessionID {
			matches = append(matches, metadata)
		}
	}
	if len(matches) == 0 {
		return server.SessionMetadata{}, server.NewSessionNotFoundError("Unknown session: " + sessionID)
	}
	if len(matches) > 1 {
		return server.SessionMetadata{}, server.NewSessionAmbiguousError()
	}
	return matches[0], nil
}

// OpenSession opens one harness, counting attempts.
func (h *TestServerHost) OpenSession(ctx context.Context, metadata server.SessionMetadata) (server.RoutedSessionHandle, error) {
	h.mu.Lock()
	h.openSessionNum++
	gate := h.nextOpenSessionGate
	h.nextOpenSessionGate = nil
	h.mu.Unlock()
	if gate != nil {
		gate.Entered.Resolve(struct{}{})
		if _, err := gate.Release.Await(ctx); err != nil {
			return nil, err
		}
	}

	h.mu.Lock()
	openError := h.NextOpenSessionError
	h.NextOpenSessionError = nil
	if openError != nil {
		h.mu.Unlock()
		return nil, openError
	}
	harness := NewTestHarness(metadata.ID)
	closeError := h.NextHarnessCloseError
	h.NextHarnessCloseError = nil
	if closeError != nil {
		harness.FailClose = closeError
	}
	h.harnesses[metadata.ID] = append(h.harnesses[metadata.ID], harness)
	h.mu.Unlock()
	return harness, nil
}

// OpenSessionCount reports how many opens were attempted.
func (h *TestServerHost) OpenSessionCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.openSessionNum
}

// Seed records one session's metadata.
func (h *TestServerHost) Seed(id string, parentSessionID string) server.SessionMetadata {
	if id == "" {
		id = "session-1"
	}
	metadata := server.SessionMetadata{ID: id, CreatedAt: 1, StorageVersion: 1}
	if parentSessionID != "" {
		metadata.ParentSessionID = parentSessionID
	}
	h.mu.Lock()
	h.metadata = append(h.metadata, metadata)
	h.mu.Unlock()
	return metadata
}

// GateNextOpenSession makes the next openSession block until released.
func (h *TestServerHost) GateNextOpenSession() *openGate {
	gate := newOpenGate()
	h.mu.Lock()
	h.nextOpenSessionGate = gate
	h.mu.Unlock()
	return gate
}

// LatestHarness returns the most recently opened harness for a session.
func (h *TestServerHost) LatestHarness(id string) *TestHarness {
	h.mu.Lock()
	defer h.mu.Unlock()
	harnesses := h.harnesses[id]
	if len(harnesses) == 0 {
		return nil
	}
	return harnesses[len(harnesses)-1]
}
