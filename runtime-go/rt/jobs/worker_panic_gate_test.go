// worker_panic_gate_test.go — the jobs worker survives a panic in the
// machinery around the handler, and never discards a store write's error.
//
//	jobs-worker-survives-a-panic        TestJobsWorkerSurvivesAPanic
//	jobs-store-errors-are-reported      TestJobsCompleteFailureIsReported
//
// # The two defects
//
//  1. `safeHandle` already recovered the USER's handler, so a panicking job
//     did not kill the worker. Nothing recovered the surrounding machinery: a
//     panic in `Claim`, in `LookupHandler`, in an `OnInflight`/`OnSuccess`
//     callback, or in a store write unwound straight out of `run`, past the
//     loop, and ended the worker for the process lifetime. Every job on the
//     queue then sat unclaimed forever with no log line — and `Stop` reported
//     a clean exit, because `done` closes on the way out either way.
//
//  2. `_ = w.store.Complete(rec.ID)`. A job whose handler SUCCEEDED but whose
//     completion write failed stays claimed, its lease expires, it is
//     redelivered, it succeeds again, and its completion fails again.
//     At-least-once delivery becomes an INFINITE redelivery loop running the
//     handler's side effects forever, and the discarded error meant there was
//     nothing to correlate that with.
//
// Fixture isolation: the store is in-memory, per-test.
package jobs

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

// scriptedStore is a Store whose Nth Claim panics and whose Complete fails on
// demand. Both behaviours are unreachable through a real backend, and both are
// exactly what the worker has to survive.
type scriptedStore struct {
	mu           sync.Mutex
	claims       int
	completes    int
	panicClaimOn map[int]bool
	completeErr  error
	jobs         int // how many jobs to hand out before returning ErrNoJob
}

func (s *scriptedStore) Claim(queue string, now time.Time) (JobRecord, error) {
	s.mu.Lock()
	s.claims++
	n := s.claims
	remaining := s.jobs
	if remaining > 0 {
		s.jobs--
	}
	s.mu.Unlock()
	if s.panicClaimOn[n] {
		panic(fmt.Sprintf("injected store panic on claim %d", n))
	}
	if remaining <= 0 {
		return JobRecord{}, ErrNoJob
	}
	return JobRecord{ID: JobID(n), Queue: queue, Name: "test.job"}, nil
}

func (s *scriptedStore) Complete(id JobID) error {
	s.mu.Lock()
	s.completes++
	s.mu.Unlock()
	return s.completeErr
}

func (s *scriptedStore) claimCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claims
}

func (s *scriptedStore) completeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completes
}

// The rest of the Store surface. These gates exercise the claim/complete path
// only; a call to any of these from a test that did not expect it is a fact
// worth failing on rather than a silent zero.
func (s *scriptedStore) Enqueue(JobRecord) (JobID, error) { return 0, errors.New("unused") }
func (s *scriptedStore) Reschedule(JobRecord) error       { return errors.New("unused") }
func (s *scriptedStore) DeadLetter(JobID, string) error   { return errors.New("unused") }
func (s *scriptedStore) Cancel(JobID) error               { return errors.New("unused") }
func (s *scriptedStore) QueueDepth(string) (int, error)   { return 0, errors.New("unused") }

// newTestWorker builds a worker with a poll interval short enough that the
// second claim happens in milliseconds rather than at the shipped 100 ms.
func newTestWorker(t *testing.T, st Store) *Worker {
	t.Helper()
	w := NewWorker(st, "test-queue")
	w.pollInterval = time.Millisecond
	return w
}

// TestJobsWorkerSurvivesAPanic — a panic in the claim path costs THAT
// iteration, not the worker.
//
// The discriminating assertion is about claims 2 and 3: "claim 1 panicked" is
// true under the broken and the fixed shape alike.
func TestJobsWorkerSurvivesAPanic(t *testing.T) {
	st := &scriptedStore{panicClaimOn: map[int]bool{1: true}}
	w := newTestWorker(t, st)

	done := make(chan struct{})
	go func() {
		// Stands in for the absent recover the shipped worker had, so the
		// defect lands as a failed assertion rather than a crashed binary.
		defer func() {
			_ = recover()
			close(done)
		}()
		w.run()
	}()

	deadline := time.Now().Add(5 * time.Second)
	for st.claimCount() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	got := st.claimCount()
	w.stopOnce.Do(func() { close(w.stop) })
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the worker did not return after stop closed")
	}

	n := 0

	n++
	if got < 3 {
		t.Errorf("the worker made %d claim(s) after a panic in its first one, want >= 3.\n"+
			"The panic killed the worker: every job on queue %q now sits unclaimed for the "+
			"lifetime of the process, and Stop() still reports a clean exit because `done` "+
			"closes on the way out either way. The recover must be scoped to ONE "+
			"claim-and-dispatch (periodic.Guard).", got, w.queue)
	}

	reportAssertions(t, n)
}

// TestJobsCompleteFailureIsReported — a failing Complete is reported, because
// discarding it turns at-least-once into infinite redelivery.
func TestJobsCompleteFailureIsReported(t *testing.T) {
	wantErr := errors.New("database is locked")
	st := &scriptedStore{jobs: 1, completeErr: wantErr}
	Define("test.job", func([]byte) error { return nil })
	w := newTestWorker(t, st)

	err := w.dispatch(JobRecord{ID: 1, Queue: w.queue, Name: "test.job"})

	n := 0

	n++
	if err == nil {
		t.Fatal("dispatch returned nil for a job whose Complete failed.\n" +
			"With the error discarded, the job stays claimed, its lease expires, it is " +
			"redelivered, it succeeds again and its completion fails again — at-least-once " +
			"delivery becomes an INFINITE redelivery loop that re-runs the handler's side " +
			"effects forever, and nothing anywhere says so.")
	}

	n++
	if !errors.Is(err, wantErr) {
		t.Errorf("dispatch returned %v, want it to wrap the store's error %v", err, wantErr)
	}

	n++
	if st.completeCount() != 1 {
		t.Errorf("Complete was called %d time(s), want 1", st.completeCount())
	}

	reportAssertions(t, n)
}

func reportAssertions(t *testing.T, n int) {
	t.Helper()
	fmt.Printf("ASSERTIONS: %d\n", n)
}
