package client

import (
	"context"
	"fmt"
	"sync"

	"github.com/dat267/pier/chord"
	"github.com/dat267/pier/chord/services"
	"github.com/dat267/pier/protocol"
)

// Port of the chord-backed service layer of src/client.ts: serviceCatalogue,
// subscribeService, and createClientServiceTransport.

// ServiceSubscription is one live remote service subscription.
type ServiceSubscription struct {
	ID       string
	Target   protocol.RpcTarget
	Snapshot *services.ServiceSubscriptionSnapshot
	// Start begins ordered update delivery after the caller has installed the
	// snapshot.
	Start func()
	// Dispose ends the subscription (unsubscribing on the server when still
	// connected to the same target).
	Dispose func() error
}

// activeServiceListener tracks one subscription's decode/delivery state.
type activeServiceListener struct {
	mu       sync.Mutex
	target   protocol.RpcTarget
	listener func(update *services.ServiceProviderUpdate)
	decoder  services.ServiceStateDecoder

	// queuedWireUpdates holds raw updates that arrive before the snapshot has
	// hydrated the decoder.
	queuedWireUpdates []any
	// queued holds decoded updates that arrived before start().
	queued []*services.ServiceProviderUpdate

	deliveryMu sync.Mutex
	hydrated   bool
	ready      bool
}

// ServiceCatalogue lists the services a target exposes.
func (c *Client) ServiceCatalogue(ctx context.Context, target protocol.RpcTarget) ([]chord.ServiceCatalogueEntry, error) {
	value, err := c.requestWithTransform(ctx, target, services.CreateServiceCatalogueCall(), nil)
	if err != nil {
		return nil, err
	}
	entries, parseErr := services.ParseServiceCatalogue(value)
	if parseErr != nil {
		// A malformed catalogue is a protocol failure: fail the connection so
		// the client does not keep using a peer that cannot be trusted.
		validationError := &protocol.ProtocolValidationError{
			Message: fmt.Sprintf("Invalid service catalogue: %s", parseErr.Error()),
		}
		c.connection.Fail(validationError)
		return nil, validationError
	}
	return entries, nil
}

// SubscribeService subscribes to one service. Updates are decoded in order and
// delivered only after Start.
func (c *Client) SubscribeService(
	ctx context.Context,
	target protocol.RpcTarget,
	serviceID string,
	mode chord.ServiceMode,
	listener func(update *services.ServiceProviderUpdate),
) (*ServiceSubscription, error) {
	c.mu.Lock()
	if c.disposed {
		c.mu.Unlock()
		return nil, &ClientDisposedError{}
	}
	c.serviceSequence++
	subscriptionID := fmt.Sprintf("service-%d", c.serviceSequence)
	active := &activeServiceListener{
		target: target, listener: listener, decoder: services.NewServiceStateDecoder(),
	}
	c.serviceListeners[subscriptionID] = active
	c.mu.Unlock()

	call := services.CreateServiceSubscribeCall(subscriptionID, serviceID, mode)
	value, err := c.requestWithTransform(ctx, target, call, func(result any) (any, error) {
		wireSnapshot, parseErr := services.ParseWireServiceSubscriptionSnapshot(result)
		if parseErr != nil {
			return nil, parseErr
		}
		snapshot, decodeErr := active.decoder.DecodeSnapshot(wireSnapshot)
		if decodeErr != nil {
			return nil, decodeErr
		}
		active.mu.Lock()
		active.hydrated = true
		queuedWire := active.queuedWireUpdates
		active.queuedWireUpdates = nil
		active.mu.Unlock()
		for _, raw := range queuedWire {
			wireUpdate, parseErr := services.ParseWireServiceProviderUpdate(raw)
			if parseErr != nil {
				return nil, parseErr
			}
			update, decodeErr := active.decoder.DecodeUpdate(wireUpdate)
			if decodeErr != nil {
				return nil, decodeErr
			}
			active.queued = append(active.queued, update)
		}
		return snapshot, nil
	})
	if err != nil {
		c.mu.Lock()
		if c.serviceListeners[subscriptionID] == active {
			delete(c.serviceListeners, subscriptionID)
		}
		c.mu.Unlock()
		return nil, err
	}
	snapshot, ok := value.(*services.ServiceSubscriptionSnapshot)
	if !ok {
		c.mu.Lock()
		if c.serviceListeners[subscriptionID] == active {
			delete(c.serviceListeners, subscriptionID)
		}
		c.mu.Unlock()
		return nil, &DisconnectedError{Message: "subscription result was not a snapshot"}
	}

	c.mu.Lock()
	current := c.serviceListeners[subscriptionID] == active
	c.mu.Unlock()
	if !current {
		return nil, NewDisconnectedError("", nil)
	}

	disposed := false
	subscription := &ServiceSubscription{ID: subscriptionID, Target: target, Snapshot: snapshot}
	subscription.Start = func() {
		active.mu.Lock()
		if disposed || active.ready {
			active.mu.Unlock()
			return
		}
		active.ready = true
		pending := active.queued
		active.queued = nil
		active.mu.Unlock()
		for _, update := range pending {
			c.deliverServiceUpdate(active, update)
		}
	}
	subscription.Dispose = func() error {
		active.mu.Lock()
		if disposed {
			active.mu.Unlock()
			return nil
		}
		disposed = true
		active.mu.Unlock()

		c.mu.Lock()
		if c.serviceListeners[subscriptionID] == active {
			delete(c.serviceListeners, subscriptionID)
		}
		connected := c.connection.State() == StateConnected
		c.mu.Unlock()

		if connected && c.targetIsCurrent(target) {
			if _, err := c.requestWithTransform(context.Background(), target,
				services.CreateServiceUnsubscribeCall(subscriptionID), nil); err != nil {
				return err
			}
		}
		active.mu.Lock()
		active.queuedWireUpdates = nil
		active.queued = nil
		active.mu.Unlock()
		return nil
	}
	return subscription, nil
}

// ClientServiceTransport adapts a lazily resolved routed client target to the
// Chord remote-service transport.
func ClientServiceTransport(client *Client, getTarget func() protocol.RpcTarget) services.RemoteServiceTransport {
	return &clientServiceTransport{client: client, getTarget: getTarget}
}

type clientServiceTransport struct {
	client    *Client
	getTarget func() protocol.RpcTarget
}

func (t *clientServiceTransport) Invoke(call chord.ServiceCall, ctx chord.Context) (chord.JsonValue, error) {
	target, err := t.resolve()
	if err != nil {
		return nil, err
	}
	return t.client.Request(ctx, target, call)
}

func (t *clientServiceTransport) Subscribe(
	serviceID string,
	mode chord.ServiceMode,
	listener func(update *services.ServiceProviderUpdate, ctx chord.Context),
	ctx chord.Context,
) (*services.ServiceSubscription, error) {
	target, err := t.resolve()
	if err != nil {
		return nil, err
	}
	subscription, err := t.client.SubscribeService(ctx, target, serviceID, mode,
		func(update *services.ServiceProviderUpdate) { listener(update, ctx) })
	if err != nil {
		return nil, err
	}
	return &services.ServiceSubscription{
		Snapshot: subscription.Snapshot,
		Activate: subscription.Start,
		Close:    subscription.Dispose,
	}, nil
}

func (t *clientServiceTransport) resolve() (protocol.RpcTarget, error) {
	target := t.getTarget()
	if target.ServerID == "" {
		return protocol.RpcTarget{}, fmt.Errorf("Remote service target is unavailable")
	}
	return target, nil
}

// deliverServiceUpdate serializes listener calls in arrival order.
func (c *Client) deliverServiceUpdate(active *activeServiceListener, update *services.ServiceProviderUpdate) {
	active.deliveryMu.Lock()
	defer active.deliveryMu.Unlock()
	c.safeListener(func() { active.listener(update) })
}

// handleServiceUpdate routes one service_update envelope.
func (c *Client) handleServiceUpdate(message *protocol.ServerMessage) {
	c.mu.Lock()
	active := c.serviceListeners[message.ServiceUpdate.SubscriptionID]
	c.mu.Unlock()
	if active == nil {
		return // an update for a subscription we do not track
	}

	active.mu.Lock()
	if !active.hydrated {
		active.queuedWireUpdates = append(active.queuedWireUpdates, message.ServiceUpdate.Update)
		active.mu.Unlock()
		return
	}
	ready := active.ready
	active.mu.Unlock()

	wireUpdate, err := services.ParseWireServiceProviderUpdate(message.ServiceUpdate.Update)
	if err != nil {
		c.connection.Fail(&protocol.ProtocolValidationError{
			Message: fmt.Sprintf("Invalid service operation stream: %s", err.Error()),
		})
		return
	}
	update, err := active.decoder.DecodeUpdate(wireUpdate)
	if err != nil {
		c.connection.Fail(&protocol.ProtocolValidationError{
			Message: fmt.Sprintf("Invalid service operation stream: %s", err.Error()),
		})
		return
	}
	if ready {
		c.deliverServiceUpdate(active, update)
		return
	}
	active.mu.Lock()
	active.queued = append(active.queued, update)
	active.mu.Unlock()
}

// targetIsCurrent reports whether a target still addresses this connection's
// live attachment (or, for server targets, the connected server).
func (c *Client) targetIsCurrent(target protocol.RpcTarget) bool {
	c.mu.Lock()
	hello := c.hello
	attachment := c.attachment
	c.mu.Unlock()

	if target.SessionID == nil {
		return hello != nil && hello.ServerID == target.ServerID
	}
	if attachment == nil {
		return false
	}
	return attachment.ServerID == target.ServerID &&
		derefString(attachment.SessionID) == derefString(target.SessionID) &&
		derefString(attachment.AttachmentID) == derefString(target.AttachmentID)
}
