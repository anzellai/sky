package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests for the SQLite backend. Each test gets a fresh temp DB to
// avoid cross-test interference; t.Cleanup deletes the file at end.

func newSQLiteFixture(t *testing.T) Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() {
		if closer, ok := store.(interface{ Close() error }); ok {
			closer.Close()
		}
	})
	return store
}

func TestSQLiteStore_EnqueueAndClaim(t *testing.T) {
	s := newSQLiteFixture(t)
	id, err := s.Enqueue(JobRecord{
		Queue:   "default",
		Name:    "greet",
		Payload: []byte(`"alice"`),
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if id == 0 {
		t.Errorf("expected non-zero JobID, got %d", id)
	}
	rec, err := s.Claim("default", time.Now())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if rec.ID != id || rec.Name != "greet" || string(rec.Payload) != `"alice"` {
		t.Errorf("claimed wrong record: %+v", rec)
	}
}

func TestSQLiteStore_ClaimRespectsNextRunAt(t *testing.T) {
	s := newSQLiteFixture(t)
	_, _ = s.Enqueue(JobRecord{
		Queue:     "default",
		Name:      "later",
		NextRunAt: time.Now().Add(1 * time.Hour),
	})
	if _, err := s.Claim("default", time.Now()); err != ErrNoJob {
		t.Errorf("future job should not be claimed; got %v", err)
	}
	_, _ = s.Enqueue(JobRecord{
		Queue:     "default",
		Name:      "ready",
		NextRunAt: time.Now().Add(-1 * time.Second),
	})
	rec, err := s.Claim("default", time.Now())
	if err != nil || rec.Name != "ready" {
		t.Errorf("ready job should be claimed; got %+v err=%v", rec, err)
	}
}

func TestSQLiteStore_ClaimSkipsAlreadyClaimed(t *testing.T) {
	// Two workers claiming concurrently — the second sees ErrNoJob
	// (the first's claim is still under lease).
	s := newSQLiteFixture(t)
	_, _ = s.Enqueue(JobRecord{Queue: "default", Name: "single"})
	if _, err := s.Claim("default", time.Now()); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := s.Claim("default", time.Now()); err != ErrNoJob {
		t.Errorf("second concurrent claim should be ErrNoJob (lease active); got %v", err)
	}
}

func TestSQLiteStore_ClaimReturnsAfterLeaseExpiry(t *testing.T) {
	// Simulate a crashed worker: claim, then claim again at a time
	// 31 minutes in the future. The lease has expired so the job is
	// re-claimable.
	s := newSQLiteFixture(t)
	_, _ = s.Enqueue(JobRecord{Queue: "default", Name: "leasetest"})
	now := time.Now()
	if _, err := s.Claim("default", now); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// 31 minutes later
	future := now.Add(31 * time.Minute)
	rec, err := s.Claim("default", future)
	if err != nil {
		t.Fatalf("post-lease re-claim should succeed; got %v", err)
	}
	if rec.Name != "leasetest" {
		t.Errorf("expected leasetest re-claimed, got %+v", rec)
	}
}

func TestSQLiteStore_Complete(t *testing.T) {
	s := newSQLiteFixture(t)
	id, _ := s.Enqueue(JobRecord{Queue: "default", Name: "x"})
	if _, err := s.Claim("default", time.Now()); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := s.Complete(id); err != nil {
		t.Fatalf("complete: %v", err)
	}
	// After Complete, even moving past the lease the job is gone.
	if _, err := s.Claim("default", time.Now().Add(1*time.Hour)); err != ErrNoJob {
		t.Errorf("after Complete, claim should be ErrNoJob")
	}
}

func TestSQLiteStore_Reschedule(t *testing.T) {
	s := newSQLiteFixture(t)
	id, _ := s.Enqueue(JobRecord{Queue: "default", Name: "retry"})
	rec, _ := s.Claim("default", time.Now())
	rec.Attempts = 1
	rec.NextRunAt = time.Now().Add(-1 * time.Second) // claimable immediately
	rec.LastError = "transient"
	if err := s.Reschedule(rec); err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	// Re-claim — should yield same job with bumped Attempts.
	again, err := s.Claim("default", time.Now())
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if again.ID != id || again.Attempts != 1 || again.LastError != "transient" {
		t.Errorf("rescheduled record lost metadata: %+v", again)
	}
	if again.Name != "retry" {
		t.Errorf("rescheduled record lost Name (sanity): got %q", again.Name)
	}
}

func TestSQLiteStore_DeadLetter(t *testing.T) {
	s := newSQLiteFixture(t)
	id, _ := s.Enqueue(JobRecord{
		Queue:    "default",
		Name:     "doomed",
		Payload:  []byte(`{"x":1}`),
		Attempts: 5,
	})
	rec, _ := s.Claim("default", time.Now())
	rec.LastError = "boom on attempt 5"
	_ = s.Reschedule(rec) // simulate last failed attempt
	_, _ = s.Claim("default", time.Now())

	if err := s.DeadLetter(id, "max attempts reached"); err != nil {
		t.Fatalf("DLQ: %v", err)
	}
	// Job gone from _sky_jobs.
	if _, err := s.Claim("default", time.Now().Add(1*time.Hour)); err != ErrNoJob {
		t.Errorf("dead-lettered job must not be re-claimable")
	}

	// Verify DLQ table has the row + the combined error message.
	sq := s.(*sqliteStore)
	var finalErr string
	err := sq.db.QueryRow(`SELECT final_error FROM _sky_jobs_dead WHERE id = ?`, int64(id)).Scan(&finalErr)
	if err != nil {
		t.Fatalf("dlq row missing: %v", err)
	}
	if !strings.Contains(finalErr, "max attempts reached") {
		t.Errorf("final_error should include caller's message; got %q", finalErr)
	}
	if !strings.Contains(finalErr, "last attempt") {
		t.Errorf("final_error should chain prior LastError; got %q", finalErr)
	}
}

func TestSQLiteStore_DeadLetterIdempotent(t *testing.T) {
	s := newSQLiteFixture(t)
	// DLQ on a non-existent ID should not error (idempotent —
	// matches the contract that a crashed worker re-running its
	// DLQ step is safe).
	if err := s.DeadLetter(JobID(99999), "doesn't exist"); err != nil {
		t.Errorf("DLQ on missing id should be no-op, got %v", err)
	}
}

func TestSQLiteStore_Cancel(t *testing.T) {
	s := newSQLiteFixture(t)
	id, _ := s.Enqueue(JobRecord{Queue: "default", Name: "skip"})
	if err := s.Cancel(id); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := s.Claim("default", time.Now()); err != ErrNoJob {
		t.Errorf("cancelled job must not be claimable")
	}
}

func TestSQLiteStore_CancelClaimedJobFails(t *testing.T) {
	// Once a job is in-flight, Cancel is a no-op (returns
	// ErrJobNotFound). Matches the safety contract: you can't
	// pull the rug from a running handler.
	s := newSQLiteFixture(t)
	id, _ := s.Enqueue(JobRecord{Queue: "default", Name: "running"})
	if _, err := s.Claim("default", time.Now()); err != nil {
		t.Fatalf("setup claim: %v", err)
	}
	if err := s.Cancel(id); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("cancel on claimed job should yield ErrJobNotFound, got %v", err)
	}
}

func TestSQLiteStore_QueueDepth(t *testing.T) {
	s := newSQLiteFixture(t)
	for i := 0; i < 7; i++ {
		_, _ = s.Enqueue(JobRecord{Queue: "default", Name: "x"})
	}
	for i := 0; i < 3; i++ {
		_, _ = s.Enqueue(JobRecord{Queue: "priority", Name: "y"})
	}
	if d, _ := s.QueueDepth("default"); d != 7 {
		t.Errorf("default depth: got %d, want 7", d)
	}
	if d, _ := s.QueueDepth("priority"); d != 3 {
		t.Errorf("priority depth: got %d, want 3", d)
	}
	if d, _ := s.QueueDepth("nonexistent"); d != 0 {
		t.Errorf("empty queue depth should be 0; got %d", d)
	}
}

func TestSQLiteStore_SurvivesReopen(t *testing.T) {
	// The defining feature of SQLite vs memory backend: jobs
	// persist across process restart.
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.db")
	s1, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("open1: %v", err)
	}
	_, _ = s1.Enqueue(JobRecord{Queue: "default", Name: "persistent",
		Payload: []byte(`"data"`)})
	if closer, ok := s1.(interface{ Close() error }); ok {
		closer.Close()
	}

	// Re-open the same file.
	s2, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("open2: %v", err)
	}
	defer func() {
		if closer, ok := s2.(interface{ Close() error }); ok {
			closer.Close()
		}
	}()
	rec, err := s2.Claim("default", time.Now())
	if err != nil {
		t.Fatalf("post-reopen claim: %v", err)
	}
	if rec.Name != "persistent" || string(rec.Payload) != `"data"` {
		t.Errorf("survived-reopen record: %+v", rec)
	}
}

func TestSQLiteStore_FileCreatedInTempDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir-doesnotexist-yet.db")
	// modernc/sqlite auto-creates the file. If the parent
	// directory doesn't exist, it errors — that's caller's job
	// (Sky init creates _sky/). This test confirms the file
	// itself doesn't need pre-creation.
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore should auto-create file: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected DB file at %s, stat err: %v", path, err)
	}
	if closer, ok := s.(interface{ Close() error }); ok {
		closer.Close()
	}
}
