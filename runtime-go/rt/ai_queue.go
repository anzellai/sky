//go:build !js

package rt

// ai_queue.go — the bounded queue in front of every resolved slot.
//
// A slot admits at most maxInFlight calls at once. Up to `queue` more wait
// for a permit. A call that finds the waiting room full fails at once with
// Overloaded, and a call that waits longer than queueWait fails with
// TimedOut. Latency therefore stays bounded under load, and the caller (or a
// Provider.router) decides what to do with the overflow, on purpose.

import (
	"sync"
	"time"
)

type aiQueue struct {
	slot      string
	permits   chan struct{}
	mu        sync.Mutex
	waiting   int
	capacity  int
	queueWait time.Duration
}

func newAiQueue(slot string, maxInFlight, capacity int, queueWait time.Duration) *aiQueue {
	if maxInFlight < 1 {
		maxInFlight = 1
	}
	return &aiQueue{
		slot:      slot,
		permits:   make(chan struct{}, maxInFlight),
		capacity:  capacity,
		queueWait: queueWait,
	}
}

// acquire takes a permit, or fails with Overloaded or TimedOut. The returned
// release must be called exactly once.
func (q *aiQueue) acquire() (func(), error) {
	release := func() { <-q.permits }
	select {
	case q.permits <- struct{}{}:
		return release, nil
	default:
	}
	q.mu.Lock()
	if q.waiting >= q.capacity {
		in, waiting := cap(q.permits), q.waiting
		q.mu.Unlock()
		return nil, aiFailf(aiOverloaded,
			"slot %q is full: %d calls in flight and %d waiting (raise its max in flight or queue, or route the overflow elsewhere)",
			q.slot, in, waiting)
	}
	q.waiting++
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		q.waiting--
		q.mu.Unlock()
	}()
	timer := time.NewTimer(q.queueWait)
	defer timer.Stop()
	select {
	case q.permits <- struct{}{}:
		return release, nil
	case <-timer.C:
		return nil, aiFailf(aiTimedOut, "slot %q: waited %s in the queue for a free call", q.slot, q.queueWait)
	}
}

// depth reports in-flight and waiting calls, for status and describe.
func (q *aiQueue) depth() (inFlight, waiting int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.permits), q.waiting
}
