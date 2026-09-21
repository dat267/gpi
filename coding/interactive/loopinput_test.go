package interactive

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// startLoopApp runs the app's loop in the background and returns a stop
// function that cancels and waits for it.
func startLoopApp(t *testing.T, app *App) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		app.Run(ctx)
	}()
	waitForConditionWithin(t, func() bool { return app.Lifecycle.IsInitialized() }, 6*time.Second)
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("run loop did not exit within 2s of cancellation")
		}
	}
}

// TestLoopDispatchesTerminalInput asserts the stdin producer path: a terminal
// sequence posted on the input channel is dispatched to the focused editor by
// the loop (stage 3), not by the reader goroutine.
func TestLoopDispatchesTerminalInput(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	stop := startLoopApp(t, app)
	defer stop()

	app.PostTerminalInput("hi there")
	waitForConditionWithin(t, func() bool {
		return strings.Contains(app.DefaultEditor.GetText(), "hi there")
	}, 6*time.Second)
}

// TestLoopDispatchesResize asserts the resize producer wakes the loop's paint.
func TestLoopDispatchesResize(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	stop := startLoopApp(t, app)
	defer stop()

	// Loop mode with the test seam: no automatic ticks, so drive one resize and
	// observe the loop-side paint through the render counter.
	before := app.UI.RenderCount()
	select {
	case app.loopResizes <- struct{}{}:
	case <-time.After(time.Second):
		t.Fatal("resize channel full")
	}
	waitForConditionWithin(t, func() bool { return app.UI.RenderCount() > before }, 6*time.Second)
}

// TestLoopDispatchesSignals asserts the signal producer path: a process signal
// reaching the lifecycle's registered handler is posted to the loop, which
// performs the shutdown work on its own goroutine.
func TestLoopDispatchesSignals(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	var handled atomic.Int64
	// The loop's signal work is a counter here so the test does not shut down.
	app.Runner.OnSignal = func(os.Signal) { handled.Add(1) }

	stop := startLoopApp(t, app)
	defer stop()

	sink := app.Lifecycle.SignalSink()
	if sink == nil {
		t.Fatal("lifecycle has no signal sink wired")
	}
	sink(syscall.SIGHUP) // the registered handler forwards to the loop
	waitForConditionWithin(t, func() bool { return handled.Load() == 1 }, 6*time.Second)
}

// TestLoopModeTakesNoRenderLock encodes the stage-3 invariant: in loop mode the
// renderer never takes its timer-mode render lock (input dispatch and painting
// share the loop goroutine).
func TestLoopModeTakesNoRenderLock(t *testing.T) {
	source, err := os.ReadFile("../../tui/render.go")
	if err != nil {
		t.Fatalf("read renderer source: %v", err)
	}
	text := string(source)
	for _, guarded := range []string{
		"if !t.loopMode() {\n\t\tt.renderMu.Lock()", // paint path
		"if !loopMode {\n\t\t\tt.renderMu.Lock()",   // input path
	} {
		if !strings.Contains(text, guarded) {
			t.Fatalf("renderMu must be loop-mode conditional; missing %q", guarded)
		}
	}
	if strings.Contains(text, "t.renderMu.Lock()\n\tdefer t.renderMu.Unlock()\n\tt.drainPosted()") {
		t.Fatal("doRender still takes renderMu unconditionally")
	}
}

// TestConcurrentTerminalProducersStayOrdered hammers the input channel from
// two producers while the loop dispatches, asserting no loss for typed text.
func TestConcurrentTerminalProducersStayOrdered(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()
	stop := startLoopApp(t, app)
	defer stop()

	var wg sync.WaitGroup
	for producer := 0; producer < 2; producer++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				app.PostTerminalInput("ab")
			}
		}(producer)
	}
	wg.Wait()
	waitForConditionWithin(t, func() bool {
		return strings.Count(app.DefaultEditor.GetText(), "ab") == 40
	}, 6*time.Second)
}

// blockingCompactSession wraps the real command session and blocks inside
// CompactSession.
type blockingCompactSession struct {
	CommandSession
	started chan struct{}
	release chan struct{}
}

func (b *blockingCompactSession) CompactSession(context.Context, string) error {
	close(b.started)
	<-b.release
	return nil
}

// TestCompactCommandDoesNotBlockInput is the regression for the reported
// freeze: /compact used to run on the TUI input goroutine and block the whole
// UI (including the advertised ESC cancel) for the summarization. It must now
// run off the loop while the loop keeps dispatching input and events.
func TestCompactCommandDoesNotBlockInput(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	session := &blockingCompactSession{started: make(chan struct{}), release: make(chan struct{})}
	session.CommandSession = app.Commands.Session
	app.Commands.Session = session

	stop := startLoopApp(t, app)
	defer stop()

	// Submit /compact through the real input path (the loop dispatches the
	// terminal sequence to the editor, whose submit handler runs on the loop,
	// exactly like production). The autocomplete may consume the first Enter,
	// so retry once.
	app.PostTerminalInput("/compact\r")
	select {
	case <-session.started:
	case <-time.After(700 * time.Millisecond):
		app.PostTerminalInput("\r")
		select {
		case <-session.started:
		case <-time.After(3 * time.Second):
			t.Fatal("compaction never started")
		}
	}

	// The loop is free while the compaction blocks.
	app.PostTerminalInput("still typing")
	waitForConditionWithin(t, func() bool {
		return strings.Contains(app.DefaultEditor.GetText(), "still typing")
	}, 6*time.Second)

	close(session.release)
}

// compactWhileBusySession records when the compaction starts.
type compactWhileBusySession struct {
	CommandSession
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *compactWhileBusySession) CompactSession(context.Context, string) error {
	c.once.Do(func() { close(c.started) })
	<-c.release
	return nil
}

// TestCompactCommandDuringTurnDoesNotQueueBehindIt is the regression for the
// reported "compact does not work": a manual /compact while a turn occupies
// RunWork's single work slot used to be queued behind that turn (RunWork
// appends to work.pending), so nothing happened until the turn finished on
// its own. Upstream session.compact() aborts the active run and compacts
// immediately; the compaction is event-only, so it must run detached.
func TestCompactCommandDuringTurnDoesNotQueueBehindIt(t *testing.T) {
	app, cleanup := newTestApp(t)
	defer cleanup()

	session := &compactWhileBusySession{started: make(chan struct{}), release: make(chan struct{})}
	session.CommandSession = app.Commands.Session
	app.Commands.Session = session

	// Occupy the work slot through the loop's own prompt path with a turn
	// that never finishes on its own.
	releaseTurn := make(chan struct{})
	turnStarted := make(chan struct{})
	previousPrompt := app.Runner.Prompt
	app.Runner.Prompt = func(ctx context.Context, text string) error {
		close(turnStarted)
		<-releaseTurn
		return previousPrompt(ctx, text)
	}

	stop := startLoopApp(t, app)
	defer stop()

	app.PostTerminalInput("long running turn")
	waitForConditionWithin(t, func() bool {
		return strings.Contains(app.DefaultEditor.GetText(), "long running turn")
	}, 6*time.Second)
	app.PostTerminalInput("\r")
	select {
	case <-turnStarted:
	case <-time.After(700 * time.Millisecond):
		// The autocomplete may consume the first Enter; retry once.
		app.PostTerminalInput("\r")
		select {
		case <-turnStarted:
		case <-time.After(3 * time.Second):
			t.Fatal("turn never started")
		}
	}

	// /compact while the turn is active: the compaction must start now, not
	// after the turn is released.
	app.PostTerminalInput("/compact\r")
	select {
	case <-session.started:
	case <-time.After(700 * time.Millisecond):
		// The autocomplete may consume the first Enter; retry once.
		app.PostTerminalInput("\r")
		select {
		case <-session.started:
		case <-time.After(3 * time.Second):
			t.Fatal("manual /compact during an active turn was queued behind it and never started")
		}
	}

	close(releaseTurn)
}
