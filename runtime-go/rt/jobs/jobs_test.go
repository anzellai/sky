package jobs

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ─── Backoff schedule ─────────────────────────────────────────

func TestBackoffFor_Exponential(t *testing.T) {
	// Lower bound (jitter-free comparison): each step at least
	// 2^(N-1) seconds.
	for n, wantBase := range map[int]time.Duration{
		1: 1 * time.Second,
		2: 2 * time.Second,
		3: 4 * time.Second,
		4: 8 * time.Second,
		5: 16 * time.Second,
	} {
		got := BackoffFor(n)
		if got < wantBase {
			t.Errorf("BackoffFor(%d): got %v, want >= %v", n, got, wantBase)
		}
		if got > wantBase+wantBase/4 {
			t.Errorf("BackoffFor(%d): got %v, jitter exceeded 25%% (cap %v)",
				n, got, wantBase+wantBase/4)
		}
	}
}

func TestBackoffFor_CapsAt1Hour(t *testing.T) {
	// Attempt 100 → 2^99 sec which overflows. Cap must clamp.
	got := BackoffFor(100)
	if got > 1*time.Hour+15*time.Minute {
		t.Errorf("backoff cap broken: got %v, expected ≤ 1h+jitter", got)
	}
}

func TestBackoffFor_ZeroAttemptDoesNotPanic(t *testing.T) {
	// Defensive: even if a caller passes 0, must return something
	// sane (not negative shift / panic).
	d := BackoffFor(0)
	if d <= 0 {
		t.Errorf("non-positive backoff: %v", d)
	}
}

// ─── Handler registry ─────────────────────────────────────────

func TestDefine_AndLookup(t *testing.T) {
	ResetHandlersForTest()
	defer ResetHandlersForTest()
	Define("greet", func(p []byte) error {
		return nil
	})
	h, ok := LookupHandler("greet")
	if !ok || h == nil {
		t.Errorf("expected to find handler 'greet'")
	}
	if _, ok := LookupHandler("missing"); ok {
		t.Errorf("missing handler should return false")
	}
}

func TestDefine_OverwriteIsAllowed(t *testing.T) {
	ResetHandlersForTest()
	defer ResetHandlersForTest()
	Define("x", func([]byte) error { return errors.New("v1") })
	Define("x", func([]byte) error { return errors.New("v2") })
	h, _ := LookupHandler("x")
	if err := h(nil); err == nil || err.Error() != "v2" {
		t.Errorf("expected v2 handler to win, got %v", err)
	}
}

func TestDefine_EmptyNameIgnored(t *testing.T) {
	ResetHandlersForTest()
	defer ResetHandlersForTest()
	Define("", func([]byte) error { return nil })
	if _, ok := LookupHandler(""); ok {
		t.Errorf("empty name should not be registered")
	}
}

// ─── Memory store ─────────────────────────────────────────────

func TestMemoryStore_EnqueueAndClaim(t *testing.T) {
	s := NewMemoryStore()
	id, err := s.Enqueue(JobRecord{
		Queue:   "default",
		Name:    "myJob",
		Payload: []byte(`{"foo":"bar"}`),
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if id == 0 {
		t.Errorf("ID should be non-zero")
	}
	rec, err := s.Claim("default", time.Now())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if rec.ID != id || rec.Name != "myJob" {
		t.Errorf("claimed wrong record: %+v", rec)
	}
}

func TestMemoryStore_ClaimRespectsNextRunAt(t *testing.T) {
	s := NewMemoryStore()
	// Enqueue with NextRunAt in the future.
	_, _ = s.Enqueue(JobRecord{
		Queue:     "default",
		Name:      "later",
		NextRunAt: time.Now().Add(1 * time.Hour),
	})
	_, err := s.Claim("default", time.Now())
	if err != ErrNoJob {
		t.Errorf("future job should not be claimed; got err=%v", err)
	}
	// But past NextRunAt is claimable.
	_, _ = s.Enqueue(JobRecord{
		Queue:     "default",
		Name:      "ready",
		NextRunAt: time.Now().Add(-1 * time.Second),
	})
	rec, err := s.Claim("default", time.Now())
	if err != nil || rec.Name != "ready" {
		t.Errorf("ready job should be claimed; got rec=%+v err=%v", rec, err)
	}
}

func TestMemoryStore_QueuesIsolated(t *testing.T) {
	s := NewMemoryStore()
	_, _ = s.Enqueue(JobRecord{Queue: "fast", Name: "f"})
	_, _ = s.Enqueue(JobRecord{Queue: "slow", Name: "s"})
	if _, err := s.Claim("other", time.Now()); err != ErrNoJob {
		t.Errorf("claim from unrelated queue should be empty")
	}
	fast, err := s.Claim("fast", time.Now())
	if err != nil || fast.Name != "f" {
		t.Errorf("'fast' claim: %+v err=%v", fast, err)
	}
}

func TestMemoryStore_Reschedule(t *testing.T) {
	s := NewMemoryStore()
	id, _ := s.Enqueue(JobRecord{Queue: "default", Name: "retry-me"})
	rec, _ := s.Claim("default", time.Now())
	// Simulate retry: bump attempts, set future NextRunAt.
	rec.Attempts = 1
	rec.NextRunAt = time.Now().Add(-1 * time.Second) // immediate re-claim
	rec.LastError = "boom"
	if err := s.Reschedule(rec); err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	// Should be claimable again with retained Name + bumped Attempts.
	again, err := s.Claim("default", time.Now())
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if again.ID != id || again.Name != "retry-me" {
		t.Errorf("rescheduled record lost identity: %+v", again)
	}
	if again.Attempts != 1 {
		t.Errorf("expected Attempts=1, got %d", again.Attempts)
	}
	if again.LastError != "boom" {
		t.Errorf("LastError lost on reschedule")
	}
}

func TestMemoryStore_Cancel(t *testing.T) {
	s := NewMemoryStore()
	id, _ := s.Enqueue(JobRecord{Queue: "default", Name: "skip"})
	if err := s.Cancel(id); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := s.Claim("default", time.Now()); err != ErrNoJob {
		t.Errorf("cancelled job should not be claimable")
	}
}

func TestMemoryStore_CancelMissingReturnsError(t *testing.T) {
	s := NewMemoryStore()
	if err := s.Cancel(JobID(999)); err != ErrJobNotFound {
		t.Errorf("expected ErrJobNotFound, got %v", err)
	}
}

func TestMemoryStore_QueueDepth(t *testing.T) {
	s := NewMemoryStore()
	for i := 0; i < 5; i++ {
		_, _ = s.Enqueue(JobRecord{Queue: "default", Name: "x"})
	}
	for i := 0; i < 3; i++ {
		_, _ = s.Enqueue(JobRecord{Queue: "other", Name: "y"})
	}
	if d, _ := s.QueueDepth("default"); d != 5 {
		t.Errorf("default depth: got %d, want 5", d)
	}
	if d, _ := s.QueueDepth("other"); d != 3 {
		t.Errorf("other depth: got %d, want 3", d)
	}
}

// ─── Worker dispatch ──────────────────────────────────────────

// fakeStore — minimal in-memory implementation used by worker
// tests to assert lifecycle calls (Complete / Reschedule /
// DeadLetter) without timing-dependent polling.
type fakeStore struct {
	mu        sync.Mutex
	jobs      []JobRecord
	completed []JobID
	rescheds  []JobRecord
	dlq       []JobID
}

func (s *fakeStore) Enqueue(rec JobRecord) (JobID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec.ID = JobID(len(s.jobs) + 1)
	s.jobs = append(s.jobs, rec)
	return rec.ID, nil
}

func (s *fakeStore) Claim(queue string, now time.Time) (JobRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.jobs {
		if r.Queue == queue && !r.NextRunAt.After(now) {
			out := r
			s.jobs = append(s.jobs[:i], s.jobs[i+1:]...)
			return out, nil
		}
	}
	return JobRecord{}, ErrNoJob
}

func (s *fakeStore) Complete(id JobID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completed = append(s.completed, id)
	return nil
}

func (s *fakeStore) Reschedule(rec JobRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rescheds = append(s.rescheds, rec)
	s.jobs = append(s.jobs, rec)
	return nil
}

func (s *fakeStore) DeadLetter(id JobID, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dlq = append(s.dlq, id)
	return nil
}

func (s *fakeStore) Cancel(id JobID) error { return nil }

func (s *fakeStore) QueueDepth(_ string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.jobs), nil
}

func TestWorker_SuccessfulDispatch(t *testing.T) {
	ResetHandlersForTest()
	defer ResetHandlersForTest()
	var called atomic.Int32
	Define("ok", func([]byte) error {
		called.Add(1)
		return nil
	})

	s := &fakeStore{}
	_, _ = s.Enqueue(JobRecord{Queue: "default", Name: "ok"})
	w := NewWorker(s, "default")
	w.pollInterval = 10 * time.Millisecond
	w.Start()
	defer w.Stop(1 * time.Second)

	// Wait briefly for dispatch.
	for i := 0; i < 100 && called.Load() == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if called.Load() != 1 {
		t.Errorf("handler should fire exactly once; got %d", called.Load())
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.completed) != 1 {
		t.Errorf("expected Complete called once, got %d", len(s.completed))
	}
}

func TestWorker_RetriesOnFailure(t *testing.T) {
	ResetHandlersForTest()
	defer ResetHandlersForTest()
	var attempts atomic.Int32
	Define("fail", func([]byte) error {
		attempts.Add(1)
		return errors.New("boom")
	})

	s := &fakeStore{}
	_, _ = s.Enqueue(JobRecord{Queue: "default", Name: "fail"})
	w := NewWorker(s, "default")
	w.pollInterval = 5 * time.Millisecond
	w.Start()
	defer w.Stop(1 * time.Second)

	// Wait for at least one Reschedule.
	for i := 0; i < 200; i++ {
		s.mu.Lock()
		n := len(s.rescheds)
		s.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rescheds) < 1 {
		t.Errorf("expected at least one Reschedule call after failure")
	}
	if len(s.completed) > 0 {
		t.Errorf("failure must NOT be marked completed")
	}
	if s.rescheds[0].Attempts != 1 {
		t.Errorf("first retry should bump Attempts to 1, got %d",
			s.rescheds[0].Attempts)
	}
	if s.rescheds[0].LastError != "boom" {
		t.Errorf("expected LastError='boom', got %q", s.rescheds[0].LastError)
	}
}

func TestWorker_DeadLetterAfterMaxAttempts(t *testing.T) {
	ResetHandlersForTest()
	defer ResetHandlersForTest()
	Define("alwaysFail", func([]byte) error {
		return errors.New("doomed")
	})

	s := &fakeStore{}
	// Enqueue at attempt MaxAttempts-1 so next failure tips it to DLQ.
	_, _ = s.Enqueue(JobRecord{
		Queue:    "default",
		Name:     "alwaysFail",
		Attempts: MaxAttempts - 1,
	})
	w := NewWorker(s, "default")
	w.pollInterval = 5 * time.Millisecond
	w.Start()
	defer w.Stop(1 * time.Second)

	for i := 0; i < 200; i++ {
		s.mu.Lock()
		n := len(s.dlq)
		s.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.dlq) != 1 {
		t.Errorf("expected exactly one DLQ entry, got %d", len(s.dlq))
	}
	if len(s.rescheds) > 0 {
		t.Errorf("max-attempts failure should NOT reschedule, got %d", len(s.rescheds))
	}
}

func TestWorker_MissingHandlerGoesToDLQ(t *testing.T) {
	ResetHandlersForTest()
	defer ResetHandlersForTest()
	// No Define call — the worker can't dispatch.
	s := &fakeStore{}
	_, _ = s.Enqueue(JobRecord{Queue: "default", Name: "unknown"})
	w := NewWorker(s, "default")
	w.pollInterval = 5 * time.Millisecond
	w.Start()
	defer w.Stop(1 * time.Second)
	for i := 0; i < 100 && true; i++ {
		s.mu.Lock()
		n := len(s.dlq)
		s.mu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.dlq) != 1 {
		t.Errorf("missing-handler job should go straight to DLQ; got %d", len(s.dlq))
	}
	if len(s.rescheds) != 0 {
		t.Errorf("missing handler must NOT trigger retry (no point)")
	}
}

func TestWorker_PanicInHandlerDoesNotKillWorker(t *testing.T) {
	ResetHandlersForTest()
	defer ResetHandlersForTest()
	var ranSecond atomic.Bool
	Define("panic", func([]byte) error {
		panic("boom")
	})
	Define("ok", func([]byte) error {
		ranSecond.Store(true)
		return nil
	})

	s := &fakeStore{}
	_, _ = s.Enqueue(JobRecord{Queue: "default", Name: "panic"})
	_, _ = s.Enqueue(JobRecord{Queue: "default", Name: "ok"})
	w := NewWorker(s, "default")
	w.pollInterval = 5 * time.Millisecond
	w.Start()
	defer w.Stop(1 * time.Second)

	for i := 0; i < 200 && !ranSecond.Load(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if !ranSecond.Load() {
		t.Errorf("second job should still run after first panicked")
	}
}

func TestWorker_StopExits(t *testing.T) {
	s := &fakeStore{}
	w := NewWorker(s, "default")
	w.Start()
	w.Stop(500 * time.Millisecond)
	if !w.stopped.Load() {
		t.Errorf("worker should be stopped after Stop()")
	}
}

func TestWorker_MetricsCallbacksFire(t *testing.T) {
	ResetHandlersForTest()
	defer ResetHandlersForTest()
	Define("ok", func([]byte) error { return nil })

	var success, failure, dlq, inflightCalls atomic.Int32
	s := &fakeStore{}
	_, _ = s.Enqueue(JobRecord{Queue: "default", Name: "ok"})
	w := NewWorker(s, "default")
	w.pollInterval = 5 * time.Millisecond
	w.OnSuccess = func(string, time.Duration) { success.Add(1) }
	w.OnFailure = func(string, time.Duration, int) { failure.Add(1) }
	w.OnDeadLetter = func(string) { dlq.Add(1) }
	w.OnInflight = func(string, int) { inflightCalls.Add(1) }
	w.Start()
	defer w.Stop(1 * time.Second)

	for i := 0; i < 100 && success.Load() == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if success.Load() != 1 {
		t.Errorf("expected one success callback, got %d", success.Load())
	}
	if failure.Load() != 0 {
		t.Errorf("expected zero failure callbacks, got %d", failure.Load())
	}
	if dlq.Load() != 0 {
		t.Errorf("expected zero dlq callbacks, got %d", dlq.Load())
	}
	// Inflight fires +1 / -1 → 2 calls per dispatch.
	if inflightCalls.Load() != 2 {
		t.Errorf("expected 2 inflight callbacks (+1/-1), got %d", inflightCalls.Load())
	}
}

// ─── Payload helpers ──────────────────────────────────────────

func TestEncodeDecodePayload_RoundTrip(t *testing.T) {
	in := map[string]any{"email": "user@example.com", "count": 42}
	b, err := EncodePayload(in)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var out map[string]any
	if err := DecodePayload(b, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["email"] != "user@example.com" {
		t.Errorf("round-trip lost field: %+v", out)
	}
}
