package ai

import (
	"context"
	"testing"
	"time"
)

// Port of packages/ai/test/event-stream.test.ts — regression tests for
// upstream issue #9055.

func TestEventStreamDrainsBufferedInOrderAndIgnoresAfterCompletion(t *testing.T) {
	stream := NewEventStream(
		func(event int) bool { return event == 3 },
		func(event int) int { return event },
	)
	stream.Push(1)
	stream.Push(2)
	stream.Push(3)
	stream.Push(4)

	result, err := stream.Result(context.Background())
	if err != nil || result != 3 {
		t.Fatalf("Result() = %v, %v; want 3", result, err)
	}

	events := stream.Events(context.Background())
	if len(events) != 3 || events[0] != 1 || events[1] != 2 || events[2] != 3 {
		t.Fatalf("events = %v; want [1 2 3]", events)
	}
}

func TestEventStreamPreservesOrderWhenEventsArriveAfterDrainingStarts(t *testing.T) {
	stream := NewEventStream(
		func(event int) bool { return false },
		func(event int) int { return event },
	)
	stream.Push(1)
	stream.Push(2)
	ctx := context.Background()

	if ev, ok := stream.Next(ctx); !ok || ev != 1 {
		t.Fatalf("first Next = %v, %v; want 1", ev, ok)
	}
	stream.Push(3)
	if ev, ok := stream.Next(ctx); !ok || ev != 2 {
		t.Fatalf("second Next = %v, %v; want 2", ev, ok)
	}
	if ev, ok := stream.Next(ctx); !ok || ev != 3 {
		t.Fatalf("third Next = %v, %v; want 3", ev, ok)
	}
	three := 3
	stream.End(&three)
	if ev, ok := stream.Next(ctx); ok {
		t.Fatalf("Next after End = %v, %v; want done", ev, ok)
	}
}

// waitRegistered polls until n consumers are registered as waiters.
func waitRegistered(t *testing.T, stream *EventStream[int, int], n int) {
	t.Helper()
	for deadline := time.Now().Add(time.Second); ; {
		stream.mu.Lock()
		nWait := len(stream.waiting)
		stream.mu.Unlock()
		if nWait >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("consumer %d did not register", n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestEventStreamDeliversToWaitingConsumersInRegistrationOrder(t *testing.T) {
	stream := NewEventStream(
		func(event int) bool { return false },
		func(event int) int { return event },
	)

	start := make(chan struct{})
	first := make(chan int, 1)
	second := make(chan int, 1)
	go func() {
		<-start
		ev, ok := stream.Next(context.Background())
		if ok {
			first <- ev
		}
	}()
	go func() {
		<-start
		ev, ok := stream.Next(context.Background())
		if ok {
			second <- ev
		}
	}()

	// Wake both consumers so they register as waiters, strictly in order.
	close(start)
	waitRegistered(t, stream, 1)
	waitRegistered(t, stream, 2)
	stream.Push(1)
	stream.Push(2)

	if got := <-first; got != 1 {
		t.Fatalf("first consumer got %d; want 1", got)
	}
	if got := <-second; got != 2 {
		t.Fatalf("second consumer got %d; want 2", got)
	}
}

func TestEventStreamDrainsAfterEndAndResolvesExplicitResult(t *testing.T) {
	stream := NewEventStream(
		func(event int) bool { return false },
		func(event int) string { return string(rune('0' + event)) },
	)
	stream.Push(1)
	stream.Push(2)
	complete := "complete"
	stream.End(&complete)

	result, err := stream.Result(context.Background())
	if err != nil || result != "complete" {
		t.Fatalf("Result() = %q, %v; want complete", result, err)
	}

	got := stream.Events(context.Background())
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("events = %v; want [1 2]", got)
	}
}

func TestEventStreamWakesAllWaitingConsumersWhenEndedWithoutResult(t *testing.T) {
	stream := NewEventStream(
		func(event int) bool { return false },
		func(event int) int { return event },
	)

	first := make(chan bool, 1)
	second := make(chan bool, 1)
	go func() { _, ok := stream.Next(context.Background()); first <- ok }()
	waitRegistered(t, stream, 1)
	go func() { _, ok := stream.Next(context.Background()); second <- ok }()
	waitRegistered(t, stream, 2)
	stream.End(nil)

	if ok := <-first; ok {
		t.Fatal("first consumer should be done")
	}
	if ok := <-second; ok {
		t.Fatal("second consumer should be done")
	}
}

func TestAssistantMessageEventStreamResult(t *testing.T) {
	stream := NewAssistantMessageEventStream()
	msg := &AssistantMessage{StopReason: StopStop}
	stream.Push(AssistantMessageEvent{Type: EventStart, Partial: msg})
	stream.Push(AssistantMessageEvent{Type: EventDone, Reason: StopStop, Message: msg})
	// Pushes after terminal events are ignored.
	stream.Push(AssistantMessageEvent{Type: EventTextDelta, Delta: "late"})

	result, err := stream.Result(context.Background())
	if err != nil || result != msg {
		t.Fatalf("Result() = %v, %v; want message", result, err)
	}
	events := stream.Events(context.Background())
	if len(events) != 2 {
		t.Fatalf("events = %d; want 2 (start + done)", len(events))
	}
}

func TestAssistantMessageEventStreamErrorResult(t *testing.T) {
	stream := NewAssistantMessageEventStream()
	errMsg := &AssistantMessage{StopReason: StopError}
	stream.Push(AssistantMessageEvent{Type: EventError, Reason: StopError, Error: errMsg})

	result, err := stream.Result(context.Background())
	if err != nil || result != errMsg {
		t.Fatalf("Result() = %v, %v; want error message", result, err)
	}
}
