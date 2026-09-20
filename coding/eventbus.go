package coding

import (
	"fmt"
	"sync"
)

// Port of core/event-bus.ts: a channel-keyed pub/sub bus whose handlers cannot
// affect the emitter.

// EventHandler receives one event payload.
type EventHandler func(data any)

// EventBus is the pub/sub surface.
type EventBus interface {
	Emit(channel string, data any)
	// On subscribes a handler and returns the unsubscribe function.
	On(channel string, handler EventHandler) func()
}

// EventBusController is an EventBus that can drop every subscription.
type EventBusController interface {
	EventBus
	// SetHandlerErrorReporter observes handler failures (upstream logs them to
	// stderr).
	SetHandlerErrorReporter(reporter func(channel string, err error))
	Clear()
}

type eventBus struct {
	mu          sync.Mutex
	nextID      int
	subscribers map[string]map[int]EventHandler
	// OnHandlerError observes handler failures; upstream logs to stderr.
	OnHandlerError func(channel string, err error)
}

// CreateEventBus builds an event bus (upstream createEventBus).
func CreateEventBus() EventBusController {
	return &eventBus{subscribers: map[string]map[int]EventHandler{}}
}

func (b *eventBus) SetHandlerErrorReporter(reporter func(channel string, err error)) {
	b.mu.Lock()
	b.OnHandlerError = reporter
	b.mu.Unlock()
}

func (b *eventBus) Emit(channel string, data any) {
	b.mu.Lock()
	handlers := make([]EventHandler, 0, len(b.subscribers[channel]))
	for _, handler := range b.subscribers[channel] {
		handlers = append(handlers, handler)
	}
	onError := b.OnHandlerError
	b.mu.Unlock()

	for _, handler := range handlers {
		b.safeCall(channel, handler, data, onError)
	}
}

// safeCall runs one handler, reporting failures instead of propagating them
// (upstream wraps handlers in a try/catch).
func (b *eventBus) safeCall(channel string, handler EventHandler, data any, onError func(string, error)) {
	defer func() {
		if recovered := recover(); recovered != nil {
			b.report(onError, channel, toError(recovered))
		}
	}()
	handler(data)
}

func (b *eventBus) report(onError func(string, error), channel string, err error) {
	if onError == nil {
		return
	}
	defer func() { _ = recover() }()
	onError(channel, err)
}

func (b *eventBus) On(channel string, handler EventHandler) func() {
	b.mu.Lock()
	if b.subscribers[channel] == nil {
		b.subscribers[channel] = map[int]EventHandler{}
	}
	id := b.nextID
	b.nextID++
	b.subscribers[channel][id] = handler
	b.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subscribers[channel], id)
			b.mu.Unlock()
		})
	}
}

func (b *eventBus) Clear() {
	b.mu.Lock()
	b.subscribers = map[string]map[int]EventHandler{}
	b.mu.Unlock()
}

// toError converts a recovered panic value into an error.
func toError(value any) error {
	if err, ok := value.(error); ok {
		return err
	}
	return fmt.Errorf("%v", value)
}

// FormatEventHandlerError renders the upstream log line.
func FormatEventHandlerError(channel string, err error) string {
	return fmt.Sprintf("Event handler error (%s): %v", channel, err)
}
