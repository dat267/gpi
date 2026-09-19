// Package client is a faithful Go port of @earendil-works/pi-client
// (pi/packages/client): the transport-neutral client for remote pi sessions
// over framed CBOR bytes.
//
// Ground truth: pi/packages/client/src at the pinned upstream commit.
// The chord-backed service subscription layer (subscribeService,
// serviceCatalogue, createClientServiceTransport) is deferred until the
// chord service wire subset is ported; everything else matches upstream.
package client

import (
	"fmt"
)

// Port of src/transport.ts: the byte transport contract.

// ByteTransport sends and closes framed bytes. Send calls must be delivered
// in invocation order.
type ByteTransport interface {
	// Send transmits one byte chunk.
	Send(chunk []byte) error
	// Close closes the transport; repeated calls must be harmless.
	Close()
}

// ByteTransportHandlers receive transport events. Exactly one terminal
// handler (OnClose or OnError) is expected.
type ByteTransportHandlers struct {
	// OnData delivers an arbitrary inbound byte chunk.
	OnData func(chunk []byte)
	// OnClose reports an orderly terminal close.
	OnClose func()
	// OnError reports a terminal transport failure.
	OnError func(err error)
}

// ByteTransportFactory creates a fresh connected, authenticated transport.
type ByteTransportFactory func(handlers ByteTransportHandlers) (ByteTransport, error)

// Port of src/errors.ts.

// ServerError carries a protocol error from the server.
type ServerError struct {
	Code    string
	Message string
}

func (e *ServerError) Error() string { return e.Message }

// DisconnectedError reports a disconnected client.
type DisconnectedError struct {
	Message string
	Cause   error
}

func (e *DisconnectedError) Error() string { return e.Message }
func (e *DisconnectedError) Unwrap() error { return e.Cause }

// NewDisconnectedError builds a DisconnectedError with the default message.
func NewDisconnectedError(message string, cause error) *DisconnectedError {
	if message == "" {
		message = "Client is disconnected"
	}
	return &DisconnectedError{Message: message, Cause: cause}
}

// ClientDisposedError reports use of a disposed client.
type ClientDisposedError struct{}

func (e *ClientDisposedError) Error() string { return "Client is disposed" }

// ToError normalizes a non-error value.
func ToError(err error) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("unknown error")
}

// ToDisconnectedError normalizes any error into a DisconnectedError.
func ToDisconnectedError(err error) *DisconnectedError {
	if err == nil {
		return NewDisconnectedError("", nil)
	}
	var disconnected *DisconnectedError
	if asDisconnected(err, &disconnected) {
		return disconnected
	}
	return NewDisconnectedError(err.Error(), err)
}

func asDisconnected(err error, target **DisconnectedError) bool {
	for err != nil {
		if disconnected, ok := err.(*DisconnectedError); ok {
			*target = disconnected
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}
