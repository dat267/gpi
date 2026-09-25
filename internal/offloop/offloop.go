// Package offloop provides the uniform mechanism for running work off the UI
// goroutine: one background goroutine per queue, strict submission order,
// optional keyed coalescing, and a Flush for tests and shutdown.
//
// The invariant it serves: nothing on the main event loop may block — no
// syscalls, file or network I/O, subprocesses, lock retries, or sleeps. Work
// that would block is handed to a queue; results marshal back onto the loop
// through the caller's existing UI.Post / runOnUI pattern. The queue governs
// only the off-loop side and never touches the UI itself.
//
// Domains get their own queue: a 5s clipboard hang must not delay a settings
// write. Ordering is guaranteed per queue, not across queues. Flush must not
// be called from a task of the same queue. A Group bundles the queues of one
// composition root so shutdown is a single FlushAll (drain) or StopAll (drain
// and stop).
package offloop

import "sync"

type task struct {
	key string // coalesce key, "" when not coalesced
	run func()
}

// Queue serializes tasks onto one background goroutine.
type Queue struct {
	mu       sync.Mutex
	cond     *sync.Cond
	tasks    []task
	inFlight map[string]bool // coalesce keys with a queued or running task
	active   int             // accepted tasks not yet finished
	started  bool
	stopped  bool
}

// New builds an idle queue. The worker goroutine starts with the first task
// and exits after Stop once the queue drains.
func New() *Queue {
	q := &Queue{inFlight: map[string]bool{}}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Go submits a task to run in submission order. It never blocks. Tasks
// submitted after Stop are dropped.
func (q *Queue) Go(run func()) { q.enqueue(task{run: run}) }

// GoCoalesced submits a task unless a task with the same key is queued or
// running, in which case the submission is dropped. This reproduces the
// drop-overlapping-work idiom (a paste while a paste read is still running)
// without a hand-rolled flag. The key frees up when the task finishes.
func (q *Queue) GoCoalesced(key string, run func()) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped || q.inFlight[key] {
		return
	}
	q.inFlight[key] = true
	q.enqueueLocked(task{key: key, run: run})
}

// Flush blocks until every accepted task has finished. Tasks submitted after
// Stop are not accepted, so Flush returns immediately once the queue drained.
func (q *Queue) Flush() {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.active > 0 {
		q.cond.Wait()
	}
}

// Stop rejects future tasks and waits for the queued ones to finish. Safe to
// call more than once.
func (q *Queue) Stop() {
	q.mu.Lock()
	q.stopped = true
	q.cond.Broadcast()
	q.mu.Unlock()
	q.Flush()
}

func (q *Queue) enqueue(t task) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped {
		return
	}
	q.enqueueLocked(t)
}

func (q *Queue) enqueueLocked(t task) {
	q.tasks = append(q.tasks, t)
	q.active++
	if !q.started {
		q.started = true
		go q.loop()
	}
	q.cond.Signal()
}

func (q *Queue) loop() {
	for {
		q.mu.Lock()
		for len(q.tasks) == 0 && !q.stopped {
			q.cond.Wait()
		}
		if len(q.tasks) == 0 { // stopped and drained
			q.mu.Unlock()
			return
		}
		t := q.tasks[0]
		q.tasks = q.tasks[1:]
		q.mu.Unlock()

		t.run()

		q.mu.Lock()
		if t.key != "" {
			delete(q.inFlight, t.key)
		}
		q.active--
		if q.active == 0 {
			q.cond.Broadcast()
		}
		q.mu.Unlock()
	}
}
