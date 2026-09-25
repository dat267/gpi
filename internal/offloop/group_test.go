package offloop

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A Group owns the queues of one composition root so shutdown is one call:
// FlushAll drains every queue (a clean exit cannot lose a save), StopAll drains
// and stops them (the workers exit). It exists because the queues were created
// in several places and torn down by hand, and one of them was forgotten.

func TestGroupFlushAllWaitsForEveryQueue(t *testing.T) {
	g := NewGroup()
	a, b := g.Queue(), g.Queue()
	var done atomic.Int32
	a.Go(func() {
		time.Sleep(10 * time.Millisecond)
		done.Add(1)
	})
	b.Go(func() {
		time.Sleep(10 * time.Millisecond)
		done.Add(1)
	})
	g.FlushAll()
	if got := done.Load(); got != 2 {
		t.Fatalf("FlushAll returned with %d/2 tasks done", got)
	}
}

func TestGroupFlushAllDoesNotStopQueues(t *testing.T) {
	g := NewGroup()
	q := g.Queue()
	g.FlushAll()
	var ran atomic.Bool
	q.Go(func() { ran.Store(true) })
	g.FlushAll()
	if !ran.Load() {
		t.Fatal("a task submitted after FlushAll was dropped or not awaited")
	}
}

func TestGroupStopAllStopsEveryQueue(t *testing.T) {
	g := NewGroup()
	a, b := g.Queue(), g.Queue()
	var ran atomic.Int32
	a.Go(func() { ran.Add(1) })
	b.Go(func() { ran.Add(1) })
	g.StopAll()
	if got := ran.Load(); got != 2 {
		t.Fatalf("StopAll returned with %d/2 queued tasks done", got)
	}
	// Every queue rejects work afterwards.
	a.Go(func() { ran.Add(1) })
	b.Go(func() { ran.Add(1) })
	g.StopAll() // idempotent
	if got := ran.Load(); got != 2 {
		t.Fatalf("ran = %d, want 2 (tasks after StopAll dropped)", got)
	}
}

func TestGroupAddRegistersAnExistingQueue(t *testing.T) {
	g := NewGroup()
	q := New() // created outside the group
	g.Add(q)
	var ran atomic.Bool
	q.Go(func() { ran.Store(true) })
	g.FlushAll()
	if !ran.Load() {
		t.Fatal("Add did not register the queue for FlushAll")
	}
}

func TestGroupQueueCreatedAfterStopAllIsStopped(t *testing.T) {
	g := NewGroup()
	g.StopAll()
	q := g.Queue()
	var ran atomic.Bool
	q.Go(func() { ran.Store(true) })
	q.Flush()
	if ran.Load() {
		t.Fatal("a queue created after StopAll still ran work")
	}
}

func TestGroupConcurrentQueueCreation(t *testing.T) {
	g := NewGroup()
	const n = 32
	var wg sync.WaitGroup
	var ran atomic.Int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			g.Queue().Go(func() { ran.Add(1) })
		}()
	}
	wg.Wait()
	g.FlushAll()
	if got := ran.Load(); got != n {
		t.Fatalf("ran = %d, want %d", got, n)
	}
}
