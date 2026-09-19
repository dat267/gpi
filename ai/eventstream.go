package ai

import (
	"context"
	"sync"
)

// EventStream is a port of upstream's utils/event-stream.ts EventStream<T, R>:
// a generic event stream with buffered FIFO delivery, waiting-consumer
// registration order, a terminal event that fixes the final result, and
// end() with an explicit result.
//
// Semantics preserved from upstream (each covered by a test):
//   - push after completion is ignored; the completing event itself IS
//     delivered to consumers;
//   - buffered events drain in order before blocking waits;
//   - events arriving while consumers drain are interleaved in order;
//   - multiple waiting consumers each receive one event, in registration
//     order (events are distributed, not broadcast);
//   - end(result) drains buffered events, resolves the explicit result, and
//     wakes every waiting consumer;
//   - end() without a result wakes waiting consumers and keeps any result
//     already fixed by a terminal event.
type EventStream[T any, R any] struct {
	mu      sync.Mutex
	queue   []T
	waiting []chan iteratorResult[T]
	done    bool

	resultOnce sync.Once
	resultCh   chan R
	hasResult  bool
	result     R

	isComplete    func(T) bool
	extractResult func(T) R
}

// iteratorResult mirrors upstream's IteratorResult<T>: a value or done.
type iteratorResult[T any] struct {
	value T
	done  bool
}

// NewEventStream builds a stream with the given terminal-event predicate and
// result extractor (upstream's isComplete/extractResult constructor args).
func NewEventStream[T any, R any](isComplete func(T) bool, extractResult func(T) R) *EventStream[T, R] {
	return &EventStream[T, R]{
		resultCh:      make(chan R, 1),
		isComplete:    isComplete,
		extractResult: extractResult,
	}
}

func (s *EventStream[T, R]) resolveResult(r R) {
	s.resultOnce.Do(func() {
		s.result = r
		s.hasResult = true
		s.resultCh <- r
	})
}

// Push delivers an event. After completion, pushes are ignored.
func (s *EventStream[T, R]) Push(event T) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	if s.isComplete != nil && s.isComplete(event) {
		s.done = true
		s.resolveResult(s.extractResult(event))
	}
	// Deliver to a waiting consumer or queue it.
	if len(s.waiting) > 0 {
		ch := s.waiting[0]
		s.waiting = s.waiting[1:]
		ch <- iteratorResult[T]{value: event}
		return
	}
	s.queue = append(s.queue, event)
}

// End marks the stream complete. A non-nil result resolves the final result
// (unless a terminal event already did). Buffered events remain drainable;
// waiting consumers are woken with done (upstream's
// `{ value: undefined, done: true }`).
func (s *EventStream[T, R]) End(result *R) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = true
	if result != nil {
		s.resolveResult(*result)
	}
	for _, ch := range s.waiting {
		ch <- iteratorResult[T]{done: true}
	}
	s.waiting = nil
}

// Next returns the next event. The second return is false when the stream is
// exhausted (upstream's `{ done: true }`).
func (s *EventStream[T, R]) Next(ctx context.Context) (T, bool) {
	for {
		s.mu.Lock()
		if len(s.queue) > 0 {
			ev := s.queue[0]
			s.queue = s.queue[1:]
			s.mu.Unlock()
			return ev, true
		}
		if s.done {
			s.mu.Unlock()
			var zero T
			return zero, false
		}
		ch := make(chan iteratorResult[T], 1)
		s.waiting = append(s.waiting, ch)
		s.mu.Unlock()

		select {
		case res := <-ch:
			if res.done {
				var zero T
				return zero, false
			}
			return res.value, true
		case <-ctx.Done():
			s.mu.Lock()
			for i, w := range s.waiting {
				if w == ch {
					s.waiting = append(s.waiting[:i], s.waiting[i+1:]...)
					break
				}
			}
			s.mu.Unlock()
			var zero T
			return zero, false
		}
	}
}

// Result resolves the stream's final result, blocking until a terminal event
// or End fixes it.
func (s *EventStream[T, R]) Result(ctx context.Context) (R, error) {
	s.mu.Lock()
	has := s.hasResult
	s.mu.Unlock()
	if has {
		return s.result, nil
	}
	select {
	case r := <-s.resultCh:
		return r, nil
	case <-ctx.Done():
		var zero R
		return zero, ctx.Err()
	}
}

// Events returns all remaining events as a slice (drains the stream).
func (s *EventStream[T, R]) Events(ctx context.Context) []T {
	var out []T
	for {
		ev, ok := s.Next(ctx)
		if !ok {
			return out
		}
		out = append(out, ev)
	}
}

// AssistantMessageEventStream is the uniform stream contract of an API
// implementation: it terminates with done (message) or error (error message).
type AssistantMessageEventStream struct {
	*EventStream[AssistantMessageEvent, *AssistantMessage]
}

// NewAssistantMessageEventStream builds a stream whose result is the final
// AssistantMessage extracted from the terminal done/error event.
func NewAssistantMessageEventStream() *AssistantMessageEventStream {
	return &AssistantMessageEventStream{
		EventStream: NewEventStream(
			IsTerminalEvent,
			func(event AssistantMessageEvent) *AssistantMessage {
				if event.Type == EventDone {
					return event.Message
				} else if event.Type == EventError {
					return event.Error
				}
				return nil
			},
		),
	}
}
