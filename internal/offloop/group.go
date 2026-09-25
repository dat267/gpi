package offloop

import "sync"

// Group owns the queues of one composition root so shutdown is one call:
// FlushAll drains every queue (a clean exit cannot lose a save) and StopAll
// drains and stops them (the workers exit). The queues stay independent —
// ordering is per queue, not across the group — so a slow or stuck queue cannot
// delay another. It exists because the queues are created in several places and
// were torn down by hand, and one was forgotten.
type Group struct {
	mu      sync.Mutex
	queues  []*Queue
	stopped bool
}

// NewGroup builds an empty group.
func NewGroup() *Group { return &Group{} }

// Queue creates a queue and registers it with the group.
func (g *Group) Queue() *Queue {
	q := New()
	g.Add(q)
	return q
}

// Add registers an existing queue. A queue added after StopAll is stopped
// immediately, so a late registration cannot leak a worker.
func (g *Group) Add(q *Queue) {
	g.mu.Lock()
	stopped := g.stopped
	if !stopped {
		g.queues = append(g.queues, q)
	}
	g.mu.Unlock()
	if stopped {
		q.Stop()
	}
}

// FlushAll blocks until every registered queue has drained. The queue list is
// snapshotted under the lock and the waits run outside it, so a slow queue
// cannot block a concurrent FlushAll or StopAll.
func (g *Group) FlushAll() {
	for _, q := range g.snapshot() {
		q.Flush()
	}
}

// StopAll drains and stops every registered queue: the workers exit and later
// submissions are dropped. Safe to call more than once.
func (g *Group) StopAll() {
	g.mu.Lock()
	g.stopped = true
	queues := g.queues
	g.mu.Unlock()
	for _, q := range queues {
		q.Stop()
	}
}

func (g *Group) snapshot() []*Queue {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]*Queue(nil), g.queues...)
}
