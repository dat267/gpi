package offloop

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The queue is the uniform mechanism for running work off the UI goroutine:
// one background goroutine per queue, strict submission order, optional
// keyed coalescing, and a Flush for tests and shutdown. Completions marshal
// back onto the loop through the caller's existing UI.Post pattern; the queue
// governs only the off-loop side.

func TestQueueRunsTasksInSubmissionOrder(t *testing.T) {
	q := New()
	var mu sync.Mutex
	var order []int
	for i := 0; i < 50; i++ {
		i := i
		q.Go(func() {
			mu.Lock()
			defer mu.Unlock()
			order = append(order, i)
		})
	}
	q.Flush()
	mu.Lock()
	defer mu.Unlock()
	for i, got := range order {
		if got != i {
			t.Fatalf("order[%d] = %d", i, got)
		}
	}
}

func TestQueueCoalescesSameKeyWhileTaskRuns(t *testing.T) {
	q := New()
	release := make(chan struct{})
	var ran atomic.Int32
	q.GoCoalesced("paste", func() {
		<-release
		ran.Add(1)
	})
	// While the first task runs, same-key submissions are dropped and
	// different-key submissions queue behind it.
	q.GoCoalesced("paste", func() { ran.Add(100) })
	q.GoCoalesced("other", func() { ran.Add(10) })
	close(release)
	q.Flush()
	if got := ran.Load(); got != 11 {
		t.Fatalf("ran = %d, want 11 (paste dropped, other ran)", got)
	}
	// The key is free again once the task finished.
	q.GoCoalesced("paste", func() { ran.Add(1) })
	q.Flush()
	if got := ran.Load(); got != 12 {
		t.Fatalf("ran = %d, want 12 (key reusable)", got)
	}
}

func TestQueueFlushWaitsForRunningTask(t *testing.T) {
	q := New()
	var done atomic.Bool
	q.Go(func() {
		time.Sleep(20 * time.Millisecond)
		done.Store(true)
	})
	q.Flush()
	if !done.Load() {
		t.Fatal("Flush returned before the task finished")
	}
}

func TestQueueStopRejectsNewTasksButDrainsQueued(t *testing.T) {
	q := New()
	var ran atomic.Int32
	q.Go(func() { ran.Add(1) })
	q.Stop()
	q.Go(func() { ran.Add(1) })
	if got := ran.Load(); got != 1 {
		t.Fatalf("ran = %d, want 1 (queued task drained, later task dropped)", got)
	}
	q.Flush() // Flush after Stop returns immediately
}

func TestQueueConcurrentSubmitsKeepOrderPerSubmission(t *testing.T) {
	q := New()
	const n = 200
	var counter atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q.Go(func() { counter.Add(1) })
		}()
	}
	wg.Wait()
	q.Flush()
	if got := counter.Load(); got != n {
		t.Fatalf("ran = %d, want %d", got, n)
	}
}
