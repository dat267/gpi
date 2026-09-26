package interactive

import (
	"context"
	"fmt"
	"time"
)

// This file owns the UI loop's scheduling: everything that answers "when does
// the loop wake, and why". Three concerns share one timer slot and one piece of
// state each:
//
//   - the work queue: at most one blocking unit (a turn, a compaction flush)
//     runs at a time, and a running unit is what makes a component animate at
//     all (the shell elapsed label);
//   - the animation scan: the renderer's walk over every mounted component is
//     the expensive part of a wake, so its result is cached until a paint
//     invalidates it and a "nothing animates" result is additionally boxed in
//     time (see animationScanBox);
//   - the paint coalescing: a fast event stream paints at most once per frame
//     interval, while input paints only when the dispatch asked for a render
//     (D164).
//
// They live together because they are not independent — the work queue decides
// whether a running tool may hold a live timer the walk did not report, and a
// paint decides what the walk will say next. The loop keeps the select
// dispatch; this module owns the decisions and the timers behind it. Nothing
// here is shared with another goroutine: only the loop goroutine calls it.

// animationScanBox bounds how long a cached "nothing animates" walk is trusted.
// A paint is what changes a component's animation state, so the cache is
// normally dropped by renderUI; but a component can start animating between the
// walk and the next paint — a tool's elapsed label is built after its call
// event — and with no turn active nothing else would re-walk. The box is
// upstream's own tick period, so the worst case (a label one beat late) matches
// what upstream's per-call interval gives.
const animationScanBox = time.Second

// minInteractiveFrameInterval bounds the coalesced render-tick paint rate
// (60 fps). Input, resize and animation paints are not bounded: they are
// already rare and latency-sensitive.
const minInteractiveFrameInterval = 16 * time.Millisecond

// loopHost is the schedule's window on the outside world: the renderer's
// animation walk and render ticks, and the raw terminal's input-flush
// deadlines. It exists so the schedule's decisions can be tested without a
// renderer, a terminal, or a layout.
type loopHost interface {
	// NextAnimation reports whether a component needs another frame and how
	// long until it.
	NextAnimation() (bool, time.Duration)
	// NextInputFlushDeadline reports when pending input must be flushed (a lone
	// ESC, an incomplete sequence, a split keyboard-protocol response).
	NextInputFlushDeadline() (time.Time, bool)
	// FlushPendingInput dispatches the sequences whose deadlines expired.
	FlushPendingInput()
	// RenderTicks is the renderer's coalesced render-request channel: a pending
	// tick means "a paint is wanted".
	RenderTicks() <-chan struct{}
}

// runnerWorkState is the loop-owned work bookkeeping: at most one blocking
// unit (a turn, a compaction-queue flush) runs at a time, with the rest
// queued. Nothing here is shared with other goroutines: only the loop
// goroutine mutates it.
type runnerWorkState struct {
	done    chan error
	ctx     context.Context
	active  bool
	pending []func(context.Context) error
}

// loopSchedule is the loop's scheduling state. Loop goroutine only.
type loopSchedule struct {
	host loopHost
	// paint renders the current state. It is what invalidates the animation
	// scan, so the schedule calls it through here rather than the renderer.
	paint func()
	// markInputRead tags the coming frame with when the terminal read the
	// keystroke (see inputLatencyRecorder). Optional.
	markInputRead func()

	// work is the loop-owned work queue: at most one blocking unit at a time,
	// the rest queued in arrival order.
	work runnerWorkState

	animationTimer *time.Timer
	animationCh    <-chan time.Time
	deadline       time.Time
	scanValid      bool
	scanNeeds      bool
	scanAt         time.Time

	paintTimer *time.Timer
	paintCh    <-chan time.Time
	lastPaint  time.Time
}

func newLoopSchedule(host loopHost, paint, markInputRead func()) *loopSchedule {
	schedule := &loopSchedule{
		host:          host,
		paint:         paint,
		markInputRead: markInputRead,
		lastPaint:     time.Now(),
	}
	schedule.animationTimer = time.NewTimer(time.Hour)
	schedule.animationTimer.Stop()
	schedule.paintTimer = time.NewTimer(time.Hour)
	schedule.paintTimer.Stop()
	schedule.animationCh = nil
	return schedule
}

// close stops the timers. The loop defers it.
func (s *loopSchedule) close() {
	s.animationTimer.Stop()
	s.paintTimer.Stop()
	s.animationCh = nil
	s.paintCh = nil
}

// setContext installs the work context (the run context). Work started later
// runs under it.
func (s *loopSchedule) setContext(ctx context.Context) { s.work.ctx = ctx }

// workContext returns the work context. Only the loop goroutine may call it.
func (s *loopSchedule) workContext() context.Context { return s.work.ctx }

// workActive reports whether a blocking unit is running. The loop selects on
// input only while it is not.
func (s *loopSchedule) workActive() bool { return s.work.active }

// workDone is the completion channel of the running unit.
func (s *loopSchedule) workDone() <-chan error { return s.work.done }

// runWork schedules blocking work. It never blocks, and it may only be called
// from the loop goroutine (handlers dispatch work this way).
func (s *loopSchedule) runWork(fn func(context.Context) error) {
	if fn == nil {
		return
	}
	if !s.work.active {
		s.startWork(fn)
		return
	}
	s.work.pending = append(s.work.pending, fn)
}

// startWork launches fn in its own goroutine and records the completion
// channel. A panic is surfaced like a returned error instead of killing the
// process.
func (s *loopSchedule) startWork(fn func(context.Context) error) {
	done := make(chan error, 1)
	s.work.done = done
	s.work.active = true
	workCtx := s.work.ctx
	if workCtx == nil {
		workCtx = context.Background()
	}
	go func() {
		var err error
		func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					err = fmt.Errorf("panic: %v", recovered)
				}
			}()
			err = fn(workCtx)
		}()
		done <- err
	}()
}

// finishWork records that the running unit completed and starts the next
// queued one, if any. The caller reports the error.
func (s *loopSchedule) finishWork() {
	s.work.active = false
	s.work.done = nil
	if pending := s.work.pending; len(pending) > 0 {
		next := pending[0]
		s.work.pending = pending[1:]
		s.startWork(next)
	}
}

// renderTicks is the renderer's coalesced render-request channel.
func (s *loopSchedule) renderTicks() <-chan struct{} { return s.host.RenderTicks() }

// paintChannel is the pending coalesced-paint timer, nil when none is armed.
func (s *loopSchedule) paintChannel() <-chan time.Time { return s.paintCh }

// invalidateScan drops the cached animation walk. renderUI calls it: a paint is
// the only thing that can change a component's animation state.
func (s *loopSchedule) invalidateScan() { s.scanValid = false }

// flushExpiredInput dispatches the input sequences whose deadlines expired.
func (s *loopSchedule) flushExpiredInput() { s.host.FlushPendingInput() }

// animationFired clears the animation deadline when the loop's timer fires, so
// the next iteration re-walks and re-arms.
func (s *loopSchedule) animationFired() { s.deadline = time.Time{} }

// paintNow paints the current state and records when, so the next render tick
// can be coalesced against it.
func (s *loopSchedule) paintNow() {
	s.paint()
	// A paint is the only thing that can change a component's animation state,
	// so it invalidates the cached walk: the next iteration re-walks and
	// re-arms.
	s.invalidateScan()
	s.lastPaint = time.Now()
	if s.paintCh != nil {
		s.paintTimer.Stop()
		s.paintCh = nil
	}
}

// paintIfRequested paints when the dispatch queued a render request.
// RequestRender coalesces onto the tick channel (cap 1), so a pending tick is
// exactly "a paint is wanted"; consuming it here also stops the frame timer
// from repainting the same request.
//
// Input that changed nothing — a bare mouse move, a key release, a terminal
// reply, an already-correct hover — asks for nothing and therefore costs no
// frame. A full frame is O(the transcript), so painting per mouse event was
// what made mouse interaction unusable on a large session (D164).
func (s *loopSchedule) paintIfRequested() {
	select {
	case <-s.host.RenderTicks():
	default:
		return
	}
	if s.markInputRead != nil {
		s.markInputRead()
	}
	s.paintNow()
}

// coalescePaint handles one render tick: paint now, or arm the frame timer when
// the last paint is inside the frame interval, so a burst of streaming updates
// paints once rather than once per delta.
func (s *loopSchedule) coalescePaint() {
	if wait := minInteractiveFrameInterval - time.Since(s.lastPaint); wait > 0 {
		if s.paintCh == nil {
			s.paintTimer.Reset(wait)
			s.paintCh = s.paintTimer.C
		}
		return
	}
	s.paintNow()
}

// arm points the loop's timer at the next wake — an input flush deadline or an
// animation frame — and returns the channel to select on (nil when there is
// nothing to wait for). An already armed, equal-or-earlier deadline is left
// alone, so a busy event stream cannot starve it.
func (s *loopSchedule) arm() <-chan time.Time {
	// The input flush deadline shares the loop timer; waking for it is handled
	// in the fire path via flushExpiredInput.
	if flushDeadline, ok := s.host.NextInputFlushDeadline(); ok {
		if delay := time.Until(flushDeadline); delay <= 0 {
			// Already expired: flush on this beat, before painting.
			s.flushExpiredInput()
		} else if s.deadline.IsZero() || flushDeadline.Before(s.deadline) {
			s.animationTimer.Reset(delay)
			s.deadline = flushDeadline
			return s.animationTimer.C
		}
	}
	// The animation walk visits every mounted component, and this runs once per
	// loop iteration — once per input event. Reuse the last walk while it still
	// describes the tree: an input event that paints nothing must not pay for
	// the walk (D164). A walk with a deadline stays valid until that deadline
	// passes, which is when the owner must ask again.
	//
	// A "nothing animates" walk is trusted only while it is fresh and no turn is
	// running: a tool with a live timer can be built between the scan and the
	// next paint, and while work is active the fallback below covers it.
	if s.scanValid && !s.scanNeeds && s.deadline.IsZero() && !s.work.active &&
		time.Since(s.scanAt) < animationScanBox {
		return nil
	}
	if s.scanValid && s.scanNeeds && !s.deadline.IsZero() && time.Now().Before(s.deadline) {
		return s.animationTimer.C
	}
	needs, delay := s.host.NextAnimation()
	s.scanAt = time.Now()
	// A running turn can hold a live timer (the shell elapsed label) even when
	// the animation walk did not report one: the walk descends through wrappers
	// and may not reach a freshly built component before the next paint. Tick at
	// least once a second while work is active so such a label cannot freeze.
	if s.work.active && (!needs || delay > time.Second) {
		needs, delay = true, time.Second
	}
	s.scanValid = true
	s.scanNeeds = needs
	if !needs {
		if !s.deadline.IsZero() {
			s.animationTimer.Stop()
			s.deadline = time.Time{}
		}
		return nil
	}
	if delay <= 0 {
		delay = time.Millisecond
	}
	next := time.Now().Add(delay)
	if !s.deadline.IsZero() && !next.Before(s.deadline) {
		return s.animationTimer.C
	}
	s.animationTimer.Reset(delay)
	s.deadline = next
	return s.animationTimer.C
}
