package coding

import (
	"testing"
	"time"
)

// TestBashUpdateThrottleCoalesces pins the streamed-update policy ported from
// upstream (updateDirty / lastUpdateAt / BASH_UPDATE_THROTTLE_MS = 100): at
// most one update per interval, whatever the chunk rate. The port emitted one
// snapshot per 64 KB read instead, so a fast command's output flooded the UI
// and every chunk paid a full-output snapshot.
func TestBashUpdateThrottleCoalesces(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	throttle := &bashUpdateThrottle{interval: BashUpdateThrottleMS, now: func() time.Time { return now }}

	// The first chunk emits immediately (no previous update).
	if emitNow, _ := throttle.mark(); !emitNow {
		t.Fatal("the first chunk must emit immediately")
	}
	if !throttle.take() {
		t.Fatal("the first chunk must have a pending update to take")
	}
	if throttle.take() {
		t.Fatal("taking twice must not emit twice")
	}

	// A burst inside the interval is deferred and coalesced into one update.
	emitNow, delay := throttle.mark()
	if emitNow {
		t.Fatal("a chunk inside the interval must not emit immediately")
	}
	if delay <= 0 || delay > BashUpdateThrottleMS {
		t.Fatalf("delay = %v, want (0, %v]", delay, BashUpdateThrottleMS)
	}
	for i := 0; i < 100; i++ {
		emitNow, _ := throttle.mark()
		if emitNow {
			t.Fatalf("burst chunk %d emitted inside the interval", i)
		}
	}
	if !throttle.take() {
		t.Fatal("the deferred update must be taken once the timer fires")
	}
	if throttle.take() {
		t.Fatal("the coalesced burst must produce exactly one update")
	}

	// After the interval the next chunk emits immediately again.
	now = now.Add(BashUpdateThrottleMS)
	if emitNow, _ := throttle.mark(); !emitNow {
		t.Fatal("a chunk after the interval must emit immediately")
	}
}
