package client

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/dat267/pier/chord"
	"github.com/dat267/pier/chord/delta"
	"github.com/dat267/pier/chord/services"
	"github.com/dat267/pier/protocol"
)

// Client service-layer tests keyed to upstream client.ts: serviceCatalogue,
// subscribeService (hydration, queued updates, start gating), and dispose.

func (h *serverHarness) sendServiceUpdate(subscriptionID string, update any) {
	frame, err := protocol.EncodeServerMessage(&protocol.ServerMessage{
		Type: protocol.ServerMessageServiceUpdate,
		ServiceUpdate: &protocol.ServiceEventEnvelope{
			SubscriptionID: subscriptionID,
			Update:         update,
		},
	}, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.transport.Send(frame); err != nil {
		h.t.Fatal(err)
	}
}

// catalogueValue builds a service catalogue result.
func catalogueValue() []any {
	return []any{
		map[string]any{"serviceId": "sessions", "mode": "singleton"},
		map[string]any{"serviceId": "files", "mode": "keyed"},
	}
}

// wireSnapshotValue builds a wire subscription snapshot with a state member
// whose ops set the member to a value.
func wireSnapshotValue(stateValue any) map[string]any {
	encoder := delta.NewEncoder()
	wireOps := encoder.Encode([]delta.Op{{Verb: delta.VerbReplace, Value: stateValue}})
	ops := make([]any, 0, len(wireOps))
	for _, op := range wireOps {
		ops = append(ops, op.WireTuple())
	}
	return map[string]any{
		"serviceId": "sessions",
		"mode":      "singleton",
		"instances": []any{map[string]any{
			"members": []any{
				map[string]any{"name": "list", "kind": "method"},
				map[string]any{"name": "state", "kind": "state", "sequence": float64(1), "ops": ops},
			},
		}},
	}
}

func TestClientServiceCatalogue(t *testing.T) {
	clientSide, serverSide := newPipePair()
	harness := newServerHarness(t, serverSide)
	client := newTestClient(t, clientSide)
	if _, err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	harness.waitForMessage(protocol.ClientMessageHello)

	type outcome struct {
		entries []chord.ServiceCatalogueEntry
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		entries, err := client.ServiceCatalogue(context.Background(), protocol.RpcTarget{ServerID: testServerID})
		done <- outcome{entries, err}
	}()
	request := harness.waitForMessage(protocol.ClientMessageRequest)
	// The call is the control catalogue call.
	call, _ := request.Request.Call.(map[string]any)
	if call["serviceId"] != "$chord.service" || call["member"] != "catalogue" {
		t.Fatalf("call = %v", request.Request.Call)
	}
	harness.sendResponse(request.Request.ID, catalogueValue())

	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if len(result.entries) != 2 || result.entries[1].ServiceID != "files" ||
			result.entries[1].Mode != chord.ServiceModeKeyed {
			t.Fatalf("entries = %+v", result.entries)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("catalogue timed out")
	}
}

func TestClientServiceCatalogueInvalidFailsConnection(t *testing.T) {
	clientSide, serverSide := newPipePair()
	harness := newServerHarness(t, serverSide)
	client := newTestClient(t, clientSide)
	if _, err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	harness.waitForMessage(protocol.ClientMessageHello)

	done := make(chan error, 1)
	go func() {
		_, err := client.ServiceCatalogue(context.Background(), protocol.RpcTarget{ServerID: testServerID})
		done <- err
	}()
	request := harness.waitForMessage(protocol.ClientMessageRequest)
	// A malformed catalogue (unknown key) must fail both the call and the
	// connection.
	harness.sendResponse(request.Request.ID, []any{map[string]any{"serviceId": "x", "mode": "singleton", "extra": true}})

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("invalid catalogue must fail")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("catalogue timed out")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if client.ConnectionState() == StateDisconnected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("connection should fail after an invalid catalogue")
}

func TestClientSubscribeServiceHydrationAndUpdates(t *testing.T) {
	clientSide, serverSide := newPipePair()
	harness := newServerHarness(t, serverSide)
	client := newTestClient(t, clientSide)
	if _, err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	harness.waitForMessage(protocol.ClientMessageHello)

	type delivery struct {
		update *services.ServiceProviderUpdate
	}
	deliveries := make(chan delivery, 8)

	type subscribeResult struct {
		subscription *ServiceSubscription
		err          error
	}
	subscribed := make(chan subscribeResult, 1)
	go func() {
		subscription, err := client.SubscribeService(context.Background(),
			protocol.RpcTarget{ServerID: testServerID}, "sessions", chord.ServiceModeSingleton,
			func(update *services.ServiceProviderUpdate) { deliveries <- delivery{update} })
		subscribed <- subscribeResult{subscription, err}
	}()

	request := harness.waitForMessage(protocol.ClientMessageRequest)
	call, _ := request.Request.Call.(map[string]any)
	if call["member"] != "subscribe" {
		t.Fatalf("call = %v", request.Request.Call)
	}
	args, _ := call["args"].([]any)
	subscriptionID, _ := args[0].(string)
	if subscriptionID == "" {
		t.Fatalf("subscription id missing: %v", call)
	}

	// An update that arrives BEFORE the snapshot hydrates the decoder must be
	// queued as a wire payload, not dropped.
	harness.sendServiceUpdate(subscriptionID, map[string]any{
		"type": "state", "member": "state", "sequence": float64(9),
		"ops": []any{},
	})
	time.Sleep(50 * time.Millisecond)

	// The snapshot response hydrates the decoder.
	harness.sendResponse(request.Request.ID, wireSnapshotValue(map[string]any{"count": float64(0)}))

	var subscription *ServiceSubscription
	select {
	case result := <-subscribed:
		if result.err != nil {
			t.Fatal(result.err)
		}
		subscription = result.subscription
	case <-time.After(3 * time.Second):
		t.Fatal("subscribe timed out")
	}
	if subscription.Snapshot == nil || subscription.Snapshot.ServiceID != "sessions" {
		t.Fatalf("snapshot = %+v", subscription.Snapshot)
	}
	// Nothing is delivered before Start.
	select {
	case <-deliveries:
		t.Fatal("no updates may be delivered before Start")
	case <-time.After(50 * time.Millisecond):
	}

	// Start drains the update that arrived before hydration.
	subscription.Start()
	select {
	case delivered := <-deliveries:
		if delivered.update.Type != services.UpdateState || delivered.update.Sequence != 9 {
			t.Fatalf("queued update = %+v", delivered.update)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued update was not delivered after Start")
	}

	// A post-hydration update is decoded and delivered immediately.
	harness.sendServiceUpdate(subscriptionID, map[string]any{
		"type": "state", "member": "state", "sequence": float64(10),
		"ops": []any{},
	})
	select {
	case delivered := <-deliveries:
		if delivered.update.Sequence != 10 {
			t.Fatalf("live update = %+v", delivered.update)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("live update was not delivered")
	}

	// Dispose unsubscribes on the server.
	disposed := make(chan error, 1)
	go func() { disposed <- subscription.Dispose() }()
	unsubscribe := harness.waitForMessage(protocol.ClientMessageRequest)
	unsubscribeCall, _ := unsubscribe.Request.Call.(map[string]any)
	if unsubscribeCall["member"] != "unsubscribe" {
		t.Fatalf("call = %v", unsubscribe.Request.Call)
	}
	harness.sendResponse(unsubscribe.Request.ID, nil)
	select {
	case err := <-disposed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dispose timed out")
	}
}

func TestClientServiceUpdateUnknownSubscriptionIgnored(t *testing.T) {
	clientSide, serverSide := newPipePair()
	harness := newServerHarness(t, serverSide)
	client := newTestClient(t, clientSide)
	if _, err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	harness.waitForMessage(protocol.ClientMessageHello)

	// An update for a subscription we do not track is ignored, not fatal.
	harness.sendServiceUpdate("service-999", map[string]any{"type": "unavailable"})
	time.Sleep(50 * time.Millisecond)
	if client.ConnectionState() != StateConnected {
		t.Fatal("unknown subscription updates must be ignored")
	}
}

func TestClientServiceUpdateInvalidFailsConnection(t *testing.T) {
	clientSide, serverSide := newPipePair()
	harness := newServerHarness(t, serverSide)
	client := newTestClient(t, clientSide)
	if _, err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	harness.waitForMessage(protocol.ClientMessageHello)

	// Subscribe so a listener exists, then send a malformed update.
	subscribed := make(chan *ServiceSubscription, 1)
	go func() {
		subscription, err := client.SubscribeService(context.Background(),
			protocol.RpcTarget{ServerID: testServerID}, "sessions", chord.ServiceModeSingleton,
			func(*services.ServiceProviderUpdate) {})
		if err == nil {
			subscribed <- subscription
		}
	}()
	request := harness.waitForMessage(protocol.ClientMessageRequest)
	call, _ := request.Request.Call.(map[string]any)
	args, _ := call["args"].([]any)
	subscriptionID, _ := args[0].(string)
	harness.sendResponse(request.Request.ID, wireSnapshotValue(map[string]any{"count": float64(0)}))

	select {
	case <-subscribed:
	case <-time.After(3 * time.Second):
		t.Fatal("subscribe timed out")
	}

	// A malformed provider update (missing ops) fails the connection.
	harness.sendServiceUpdate(subscriptionID, map[string]any{
		"type": "state", "member": "state", "sequence": float64(2),
	})
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if client.ConnectionState() == StateDisconnected {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("invalid service update must fail the connection")
}

func TestClientServiceTransport(t *testing.T) {
	clientSide, serverSide := newPipePair()
	harness := newServerHarness(t, serverSide)
	client := newTestClient(t, clientSide)
	if _, err := client.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	harness.waitForMessage(protocol.ClientMessageHello)

	target := protocol.RpcTarget{ServerID: testServerID}
	transport := ClientServiceTransport(client, func() protocol.RpcTarget { return target })

	// Invoke routes through the client with the resolved target.
	done := make(chan error, 1)
	go func() {
		_, err := transport.Invoke(chord.ServiceCall{ServiceID: "sessions", Member: "list", Args: []chord.JsonValue{}},
			context.Background())
		done <- err
	}()
	request := harness.waitForMessage(protocol.ClientMessageRequest)
	harness.sendResponse(request.Request.ID, map[string]any{"ok": true})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("invoke timed out")
	}

	// An unavailable target is rejected locally.
	empty := ClientServiceTransport(client, func() protocol.RpcTarget { return protocol.RpcTarget{} })
	if _, err := empty.Invoke(chord.ServiceCall{ServiceID: "s", Member: "m", Args: []chord.JsonValue{}},
		context.Background()); err == nil {
		t.Fatal("unavailable target must fail")
	}
}

var _ = sync.Mutex{}
