package testing

import (
	"context"
	"testing"
	"time"

	"github.com/dat267/gpi/chord"
	"github.com/dat267/gpi/server"
)

func TestDeferredResolvesOnceAndAwaitsContext(t *testing.T) {
	deferred := NewDeferred[string]()
	deferred.Resolve("first")
	deferred.Resolve("second")
	value, err := deferred.Await(context.Background())
	if err != nil || value != "first" {
		t.Fatalf("value = %q err = %v", value, err)
	}

	empty := NewDeferred[string]()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := empty.Await(ctx); err == nil {
		t.Fatal("awaiting an unresolved deferred must honour the context")
	}
}

func TestTestHarnessScriptsServicesAndRelease(t *testing.T) {
	harness := NewTestHarness("session-1")
	attachment, err := harness.AttachClient(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if harness.AttachedClients != 1 {
		t.Fatalf("attachedClients = %d", harness.AttachedClients)
	}

	result, err := attachment.InvokeService(chord.ServiceCall{ServiceID: "demo", Member: "ping"}, nil, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["ok"] != true {
		t.Fatalf("result = %#v", result)
	}

	// A scripted error is consumed once.
	boom := &server.ServerError{Code: "invalid_request", Message: "boom"}
	harness.NextServiceError = boom
	if _, err := attachment.InvokeService(chord.ServiceCall{}, nil, context.Background()); err != boom {
		t.Fatalf("err = %v", err)
	}
	if _, err := attachment.InvokeService(chord.ServiceCall{}, nil, context.Background()); err != nil {
		t.Fatalf("err = %v", err)
	}

	// Release is idempotent and counted.
	if err := attachment.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := attachment.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if harness.AttachmentReleaseCount != 1 || harness.AttachedClients != 0 {
		t.Fatalf("release count = %d attached = %d", harness.AttachmentReleaseCount, harness.AttachedClients)
	}

	// A failing release is reported without decrementing.
	failing := NewTestHarness("session-2")
	failingAttachment, _ := failing.AttachClient(context.Background())
	failing.FailAttachmentRelease = &server.ServerError{Code: "internal_error", Message: "nope"}
	if err := failingAttachment.Release(context.Background()); err == nil {
		t.Fatal("a scripted release failure must surface")
	}
	if failing.AttachedClients != 1 {
		t.Fatalf("attachedClients = %d", failing.AttachedClients)
	}
}

func TestTestHarnessCloseGateAndTermination(t *testing.T) {
	harness := NewTestHarness("session-1")
	gate := harness.GateNextClose()
	done := make(chan error, 1)
	go func() { done <- harness.Close(context.Background()) }()
	select {
	case <-gate.Entered.Promise():
	case <-time.After(2 * time.Second):
		t.Fatal("close must reach the gate")
	}
	select {
	case <-done:
		t.Fatal("close must wait for the gate release")
	case <-time.After(20 * time.Millisecond):
	}
	gate.Release.Resolve(struct{}{})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-harness.Terminated():
		if err != nil {
			t.Fatalf("expected a clean termination, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("termination must settle after close")
	}
	<-harness.Closed.Promise()

	// An unexpected termination carries its error.
	other := NewTestHarness("session-2")
	failure := &server.ServerError{Code: "internal_error", Message: "crashed"}
	other.Terminate(failure)
	select {
	case err := <-other.Terminated():
		if err != failure {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminate must settle termination")
	}
}

func TestTestServerHostResolveAmbiguityAndOpenGate(t *testing.T) {
	host := NewTestServerHost()
	metadata := host.Seed("session-1", "parent-1")
	if metadata.ID != "session-1" || metadata.CreatedAt != 1 || metadata.StorageVersion != 1 || metadata.ParentSessionID != "parent-1" {
		t.Fatalf("metadata = %+v", metadata)
	}
	resolved, err := host.ResolveSession(context.Background(), "session-1")
	if err != nil || resolved.ID != "session-1" {
		t.Fatalf("resolved = %+v err = %v", resolved, err)
	}
	if _, err := host.ResolveSession(context.Background(), "missing"); !isCode(err, server.ErrSessionNotFound) {
		t.Fatalf("err = %v", err)
	}
	// A duplicated id is ambiguous.
	host.Seed("session-1", "")
	if _, err := host.ResolveSession(context.Background(), "session-1"); !isCode(err, server.ErrSessionAmbiguous) {
		t.Fatalf("err = %v", err)
	}

	// The open gate blocks openSession and counts attempts.
	host = NewTestServerHost()
	host.Seed("session-1", "")
	gate := host.GateNextOpenSession()
	opened := make(chan server.RoutedSessionHandle, 1)
	go func() {
		handle, _ := host.OpenSession(context.Background(), server.SessionMetadata{ID: "session-1", CreatedAt: 1, StorageVersion: 1})
		opened <- handle
	}()
	select {
	case <-gate.Entered.Promise():
	case <-time.After(2 * time.Second):
		t.Fatal("openSession must reach the gate")
	}
	select {
	case <-opened:
		t.Fatal("openSession must wait for the gate release")
	case <-time.After(20 * time.Millisecond):
	}
	gate.Release.Resolve(struct{}{})
	handle := <-opened
	if handle == nil || host.LatestHarness("session-1") == nil {
		t.Fatal("openSession must return a harness")
	}
	if host.OpenSessionCount() != 1 {
		t.Fatalf("openSessionCount = %d", host.OpenSessionCount())
	}
	// A scripted open error is consumed once.
	host.NextOpenSessionError = server.NewSessionNotFoundError("")
	if _, err := host.OpenSession(context.Background(), server.SessionMetadata{ID: "session-1"}); err == nil {
		t.Fatal("scripted open error must surface")
	}
	if _, err := host.OpenSession(context.Background(), server.SessionMetadata{ID: "session-1", CreatedAt: 1, StorageVersion: 1}); err != nil {
		t.Fatalf("err = %v", err)
	}
}

func TestTestServerServicesAttachAndDetach(t *testing.T) {
	services := CreateTestServerServices()
	presentation := &recordingPresentation{}
	attachment, err := services.AttachClient(context.Background(), presentation)
	if err != nil {
		t.Fatal(err)
	}
	if result, err := attachment.InvokeService(chord.ServiceCall{ServiceID: "pi.session-management", Member: "attach", Args: []chord.JsonValue{"session-9"}}, nil, context.Background()); err != nil || result != nil {
		t.Fatalf("result = %#v err = %v", result, err)
	}
	if result, err := attachment.InvokeService(chord.ServiceCall{ServiceID: "pi.session-management", Member: "detach"}, nil, context.Background()); err != nil || result != nil {
		t.Fatalf("result = %#v err = %v", result, err)
	}
	if _, err := attachment.InvokeService(chord.ServiceCall{ServiceID: "pi.session-management", Member: "explode"}, nil, context.Background()); err == nil {
		t.Fatal("unsupported members must fail")
	}
	if presentation.attached != "session-9" || !presentation.detached {
		t.Fatalf("presentation = %+v", presentation)
	}
	if err := attachment.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type recordingPresentation struct {
	attached string
	detached bool
}

func (p *recordingPresentation) AttachSession(ctx context.Context, sessionID string) error {
	p.attached = sessionID
	return nil
}
func (p *recordingPresentation) DetachSession(ctx context.Context) error {
	p.detached = true
	return nil
}
func (p *recordingPresentation) PrepareSessionRemoval(ctx context.Context, sessionID string) error {
	return nil
}

func TestCreateTestServerDefaults(t *testing.T) {
	created, err := CreateTestServer(TestServerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if created.Server.ServerID() != DefaultTestServerID {
		t.Fatalf("serverId = %s", created.Server.ServerID())
	}
	if _, ok := created.Host.(*TestServerHost); !ok {
		t.Fatalf("host = %T", created.Host)
	}
	if created.Server.Start() != nil {
		t.Fatal("a listener-less server still starts")
	}
	if err := created.Server.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func isCode(err error, want string) bool {
	code, ok := server.ServerErrorCode(err)
	return ok && code == want
}
