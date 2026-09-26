package interactive

import (
	"context"
	"github.com/dat267/pier/ai"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dat267/pier/coding"
)

// TestSessionEventQueueCoalescesPartials asserts the drop-oldest policy:
// partials are latest-wins (the newest survives, older pending ones are
// dropped) while terminal events are never dropped.
func TestSessionEventQueueCoalescesPartials(t *testing.T) {
	queue := newSessionEventQueue()

	// Overfill partials with no consumer.
	partial := func(id string) *coding.SessionEvent {
		return &coding.SessionEvent{Type: coding.SessionMessageUpdate, ID: id}
	}
	for i := 0; i < sessionEventPartialCapacity*3; i++ {
		queue.enqueue(partial(itoa(i)))
	}
	// Terminal events never drop, even with a full partial buffer.
	terminal := &coding.SessionEvent{Type: coding.SessionMessageEnd, ID: "final"}
	queue.enqueue(terminal)

	// The newest partial is retained; the buffer is bounded by its capacity.
	var seenPartials []string
	for {
		select {
		case event := <-queue.Partials():
			seenPartials = append(seenPartials, event.ID)
			continue
		default:
		}
		break
	}
	if len(seenPartials) > sessionEventPartialCapacity {
		t.Fatalf("partial buffer exceeded its capacity: %d", len(seenPartials))
	}
	newest := itoa(sessionEventPartialCapacity*3 - 1)
	if len(seenPartials) == 0 || seenPartials[len(seenPartials)-1] != newest {
		t.Fatalf("newest partial not retained: %v (want trailing %q)", seenPartials, newest)
	}
	select {
	case event := <-queue.Events():
		if event.ID != "final" {
			t.Fatalf("terminal event %q, want final", event.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal event was dropped")
	}
	queue.Close()
}

// TestSessionEventQueueLosslessUnderPressure drives many terminal events from
// a producer goroutine with a live consumer: every event arrives, so a slow
// consumer back-pressures the producer instead of losing events.
func TestSessionEventQueueLosslessUnderPressure(t *testing.T) {
	queue := newSessionEventQueue()
	const total = sessionEventLosslessCapacity * 4

	var received int64
	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		for received < total {
			select {
			case <-queue.Events():
				atomic.AddInt64(&received, 1)
			case <-time.After(5 * time.Second):
				return
			}
		}
	}()

	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		for i := 0; i < total; i++ {
			queue.enqueue(&coding.SessionEvent{Type: coding.SessionEventType("entry_appended"), ID: itoa(i)})
		}
	}()

	select {
	case <-producerDone:
	case <-time.After(10 * time.Second):
		t.Fatal("producer blocked for too long")
	}
	select {
	case <-consumerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("consumer did not drain every terminal event")
	}
	if got := atomic.LoadInt64(&received); got != total {
		t.Fatalf("received %d events, want %d", got, total)
	}
	queue.Close()
}

// TestSessionEventQueueCloseUnblocksProducer asserts shutdown releases a
// producer parked on a full lossless channel instead of hanging forever.
func TestSessionEventQueueCloseUnblocksProducer(t *testing.T) {
	queue := newSessionEventQueue()
	for i := 0; i < sessionEventLosslessCapacity; i++ {
		queue.enqueue(&coding.SessionEvent{Type: coding.SessionMessageEnd})
	}
	producerExited := make(chan struct{})
	go func() {
		defer close(producerExited)
		queue.enqueue(&coding.SessionEvent{Type: coding.SessionMessageEnd}) // blocks
	}()
	select {
	case <-producerExited:
		t.Fatal("producer should be parked while the queue is full")
	case <-time.After(100 * time.Millisecond):
	}
	queue.Close()
	select {
	case <-producerExited:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock the parked producer")
	}
}

// TestSessionEventQueueHoldsNoMutex encodes the stage-1 invariant: the
// producer path is lock-free (channels only), so an agent goroutine can never
// block on a UI mutex.
func TestSessionEventQueueHoldsNoMutex(t *testing.T) {
	source, err := os.ReadFile("interactivemode_eventqueue.go")
	if err != nil {
		t.Fatalf("read queue source: %v", err)
	}
	text := string(source)
	for _, forbidden := range []string{"sync.Mutex", "sync.RWMutex", ".Lock()", ".Unlock()"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("event queue must stay lock-free, found %q", forbidden)
		}
	}
}

// TestRunLoopDrainsProducedEventsAndShutsDown asserts the stage-1 contract:
// a producer enqueues N terminal events without applying anything, the loop
// drains all of them in order, and cancellation stops the loop within 2s.
func TestRunLoopDrainsProducedEventsAndShutsDown(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	// Observable sink on the loop side: every applied agent_settled event
	// bumps the counter.
	var applied int64
	app.events.CheckShutdownRequested = func() { atomic.AddInt64(&applied, 1) }

	const total = 64
	for i := 0; i < total; i++ {
		app.sessionEvents.enqueue(&coding.SessionEvent{Type: coding.SessionAgentSettled})
	}
	// Producers only enqueue: nothing was applied before the loop ran.
	if got := atomic.LoadInt64(&applied); got != 0 {
		t.Fatalf("producer applied %d events; it must only enqueue", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.Run(ctx)
	}()

	waitForConditionWithin(t, func() bool { return atomic.LoadInt64(&applied) == total }, 6*time.Second)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run loop did not exit within 2s of cancellation")
	}
	if got := atomic.LoadInt64(&applied); got != total {
		t.Fatalf("applied %d events, want %d", got, total)
	}
}

// TestRunLoopConcurrentProducerKeepsDraining hammers the queue from several
// producer goroutines while a turn-like work item is active: partials coalesce,
// terminal events all land, and the loop stays responsive (no mutual blocking).
func TestRunLoopConcurrentProducerKeepsDraining(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	var terminal, partial int64
	app.events.CheckShutdownRequested = func() { atomic.AddInt64(&terminal, 1) }
	app.runner.OnPartialEventApplied = func() { atomic.AddInt64(&partial, 1) }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.Run(ctx)
	}()
	waitForConditionWithin(t, func() bool { return app.lifecycle.IsInitialized() }, 6*time.Second)

	var wg sync.WaitGroup
	for producer := 0; producer < 3; producer++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				app.sessionEvents.enqueue(&coding.SessionEvent{Type: coding.SessionMessageUpdate})
				app.sessionEvents.enqueue(&coding.SessionEvent{Type: coding.SessionAgentSettled})
			}
		}()
	}
	wg.Wait()

	waitForConditionWithin(t, func() bool { return atomic.LoadInt64(&terminal) == 600 }, 6*time.Second)

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run loop did not exit within 2s of cancellation")
	}
	if got := atomic.LoadInt64(&terminal); got != 600 {
		t.Fatalf("applied %d terminal events, want 600", got)
	}
	if got := atomic.LoadInt64(&partial); got == 0 {
		t.Fatal("no partial event was applied")
	}
}

// TestRunLoopAppliesEventsWhileTurnRuns is the core stage-1 property: the loop
// must keep draining session events while a turn is in flight, because the
// turn runs on its own goroutine (upstream awaits the prompt and processes the
// event queue meanwhile).
func TestRunLoopAppliesEventsWhileTurnRuns(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	applied := make(chan struct{}, 8)
	app.events.CheckShutdownRequested = func() {
		select {
		case applied <- struct{}{}:
		default:
		}
	}

	promptStarted := make(chan struct{})
	releasePrompt := make(chan struct{})
	app.runner.Prompt = func(context.Context, string, []ai.ImageContent) error {
		close(promptStarted)
		<-releasePrompt
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.Run(ctx)
	}()

	app.startup.QueueUserInput("start the turn")
	select {
	case <-promptStarted:
	case <-time.After(6 * time.Second):
		cancel()
		t.Fatal("turn never started")
	}

	// The turn is blocked; the loop must still apply a produced event.
	app.sessionEvents.enqueue(&coding.SessionEvent{Type: coding.SessionAgentSettled})
	select {
	case <-applied:
	case <-time.After(2 * time.Second):
		close(releasePrompt)
		cancel()
		t.Fatal("event was not applied while the turn was running (loop blocked by work)")
	}

	close(releasePrompt)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run loop did not exit within 2s of cancellation")
	}
}

// TestDrainReadyEventsRendersOncePerBurst asserts the stage-2 coalescing
// contract at the loop level: N queued events drain into a single paint.
func TestDrainReadyEventsRendersOncePerBurst(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	var applied int64
	app.events.CheckShutdownRequested = func() { atomic.AddInt64(&applied, 1) }

	wiring := app.runner
	const burst = 32
	for i := 0; i < burst; i++ {
		app.sessionEvents.enqueue(&coding.SessionEvent{Type: coding.SessionAgentSettled})
	}
	for i := 0; i < burst; i++ {
		app.sessionEvents.enqueue(&coding.SessionEvent{Type: coding.SessionMessageUpdate})
	}

	// One drain applies the whole burst; one paint follows.
	before := app.ui.RenderCount()
	wiring.drainReadyEvents()
	wiring.renderUI()

	if got := atomic.LoadInt64(&applied); got != burst {
		t.Fatalf("applied %d terminal events, want %d", got, burst)
	}
	if got := app.ui.RenderCount() - before; got != 1 {
		t.Fatalf("renders for one burst = %d, want 1", got)
	}
}

// TestRenderTicksThrottlePaintRate asserts that a sustained stream of render
// requests (what streaming deltas produce) does not paint back-to-back: the
// loop coalesces them to at most one paint per frame interval.
func TestRenderTicksThrottlePaintRate(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	stop := startLoopApp(t, app)
	defer stop()

	before := app.ui.RenderCount()
	const requests = 200
	start := time.Now()
	for i := 0; i < requests; i++ {
		app.ui.RequestRender(false)
		time.Sleep(time.Millisecond)
	}
	elapsed := time.Since(start)

	paints := app.ui.RenderCount() - before
	// Without the throttle the loop paints for (nearly) every request.
	limit := int64(elapsed/minInteractiveFrameInterval) + 8
	if paints > limit {
		t.Fatalf("painted %d times over %v, want at most %d", paints, elapsed, limit)
	}
	if paints == 0 {
		t.Fatal("the loop never painted")
	}
}

// TestLoopBeatAdvances is the watchdog gap: the loop beats once per iteration
// while it runs and stops beating after cancellation, so a watchdog can detect
// a stalled loop.
func TestLoopBeatAdvances(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.Run(ctx)
	}()
	waitForConditionWithin(t, func() bool { return app.lifecycle.IsInitialized() }, 6*time.Second)

	// Events wake the loop and advance the beat. Enqueue one at a time and wait
	// for the beat it produces: an input/resize iteration calls
	// drainReadyEvents, which consumes every ready lossless event, so a
	// synchronous burst of five can legitimately reach the loop as fewer than
	// five beats. Waiting per event still catches a loop that stops iterating,
	// and a bare timeout cannot tell a stall from a slow phase, so on timeout we
	// dump every goroutine stack the way the stall log does.
	for i := 0; i < 5; i++ {
		before := app.loopBeats()
		app.sessionEvents.enqueue(&coding.SessionEvent{Type: coding.SessionAgentSettled})
		deadline := time.Now().Add(6 * time.Second)
		for app.loopBeats() == before {
			select {
			case <-done:
				t.Fatalf("the run loop exited at beat %d (event %d)", before, i)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatalf("the loop stopped beating at beat %d (event %d):\n%s", before, i, goroutineStacks())
			}
			time.Sleep(2 * time.Millisecond)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit within 2s of cancellation")
	}
	stopped := app.loopBeats()
	time.Sleep(150 * time.Millisecond)
	if beat := app.loopBeats(); beat != stopped {
		t.Fatalf("loop kept beating after cancellation: %d -> %d", stopped, beat)
	}
}

// TestProducerSendUnblocksOnContextCancel asserts the ctx.Done() arm: a
// producer parked on a full queue is released by cancelling the run context,
// without Close.
func TestProducerSendUnblocksOnContextCancel(t *testing.T) {
	queue := newSessionEventQueue()
	ctx, cancel := context.WithCancel(context.Background())
	queue.SetContext(ctx)
	for i := 0; i < sessionEventLosslessCapacity; i++ {
		queue.enqueue(&coding.SessionEvent{Type: coding.SessionMessageEnd})
	}
	producerExited := make(chan struct{})
	go func() {
		defer close(producerExited)
		queue.enqueue(&coding.SessionEvent{Type: coding.SessionMessageEnd})
	}()
	select {
	case <-producerExited:
		t.Fatal("producer should be parked while the queue is full")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case <-producerExited:
	case <-time.After(2 * time.Second):
		t.Fatal("context cancellation did not release the parked producer")
	}
	queue.Close()
}

// TestSubmitSendUnblocksOnContextCancel covers the submission channel.
func TestSubmitSendUnblocksOnContextCancel(t *testing.T) {
	wiring := &StartupWiring{}
	ctx, cancel := context.WithCancel(context.Background())
	wiring.SetContext(ctx)
	for i := 0; i < inputQueueCapacity; i++ {
		wiring.QueueUserInput("queued")
	}
	producerExited := make(chan struct{})
	go func() {
		defer close(producerExited)
		wiring.QueueUserInput("blocked")
	}()
	select {
	case <-producerExited:
		t.Fatal("producer should be parked while the queue is full")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case <-producerExited:
	case <-time.After(2 * time.Second):
		t.Fatal("context cancellation did not release the parked submit")
	}
}

// goroutineStacks dumps every goroutine's stack, so a stalled-loop failure names
// the frame the loop is parked in instead of costing another CI round-trip.
func goroutineStacks() string {
	buf := make([]byte, 1<<20)
	return string(buf[:runtime.Stack(buf, true)])
}
