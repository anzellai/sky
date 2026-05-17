package jobs

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres store tests. Require a live Postgres at SKY_PG_TEST_URL
// (e.g. `postgres://postgres@localhost:5432/postgres`). Skip
// otherwise — CI without Postgres can still build + run the rest
// of the suite.
//
// Each test scopes itself to a unique queue name (per-test
// timestamp suffix) so concurrent runs / leftover state from a
// previous run can't interfere. Cleanup deletes test rows at end.

func newPostgresFixture(t *testing.T) (Store, string) {
	t.Helper()
	url := os.Getenv("SKY_PG_TEST_URL")
	if url == "" {
		t.Skip("SKY_PG_TEST_URL not set — Postgres tests require a live DB")
	}

	store, err := NewPostgresStore(url)
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}

	// Scope this test's writes to a unique queue.
	queue := "test_" + t.Name() + "_" + nowSuffix()

	t.Cleanup(func() {
		// Best-effort cleanup of this queue's rows. Touches both
		// the active + dead tables.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if p, ok := store.(*postgresStore); ok {
			_, _ = p.pool.Exec(ctx, `DELETE FROM _sky_jobs WHERE queue = $1`, queue)
			_, _ = p.pool.Exec(ctx, `DELETE FROM _sky_jobs_dead WHERE queue = $1`, queue)
		}
		if c, ok := store.(*postgresStore); ok {
			c.Close()
		}
	})
	return store, queue
}

func nowSuffix() string {
	// Test isolation suffix: not crypto-grade, just unique enough
	// to avoid collisions when N test cases share a Postgres.
	return time.Now().Format("20060102150405.000000")
}

// Compile-time sanity that pgxpool import isn't dropped by the
// linter when no Postgres is configured.
var _ = (*pgxpool.Pool)(nil)

func TestPostgresStore_EnqueueAndClaim(t *testing.T) {
	s, q := newPostgresFixture(t)
	id, err := s.Enqueue(JobRecord{Queue: q, Name: "greet", Payload: []byte(`"alice"`)})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	rec, err := s.Claim(q, time.Now())
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if rec.ID != id || rec.Name != "greet" {
		t.Errorf("claimed wrong: %+v", rec)
	}
}

func TestPostgresStore_SkipsClaimedRowsConcurrently(t *testing.T) {
	// The SELECT ... FOR UPDATE SKIP LOCKED pattern: two
	// concurrent workers must NOT both claim the same row.
	s, q := newPostgresFixture(t)
	id1, _ := s.Enqueue(JobRecord{Queue: q, Name: "j1"})
	id2, _ := s.Enqueue(JobRecord{Queue: q, Name: "j2"})

	rec1, err := s.Claim(q, time.Now())
	if err != nil {
		t.Fatalf("claim1: %v", err)
	}
	rec2, err := s.Claim(q, time.Now())
	if err != nil {
		t.Fatalf("claim2: %v", err)
	}
	if rec1.ID == rec2.ID {
		t.Errorf("two claims returned same row (SKIP LOCKED broken)")
	}
	ids := map[JobID]bool{rec1.ID: true, rec2.ID: true}
	if !ids[id1] || !ids[id2] {
		t.Errorf("expected both enqueued ids claimed; got %v", ids)
	}
}

func TestPostgresStore_LeaseExpiryReclaim(t *testing.T) {
	s, q := newPostgresFixture(t)
	_, _ = s.Enqueue(JobRecord{Queue: q, Name: "lease"})
	now := time.Now()
	if _, err := s.Claim(q, now); err != nil {
		t.Fatalf("first claim: %v", err)
	}
	// 31 minutes later — lease has expired.
	future := now.Add(31 * time.Minute)
	rec, err := s.Claim(q, future)
	if err != nil {
		t.Fatalf("post-lease re-claim: %v", err)
	}
	if rec.Name != "lease" {
		t.Errorf("expected lease re-claimed, got %+v", rec)
	}
}

func TestPostgresStore_Complete(t *testing.T) {
	s, q := newPostgresFixture(t)
	id, _ := s.Enqueue(JobRecord{Queue: q, Name: "x"})
	_, _ = s.Claim(q, time.Now())
	if err := s.Complete(id); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, err := s.Claim(q, time.Now().Add(1*time.Hour)); err != ErrNoJob {
		t.Errorf("completed job must not be re-claimable")
	}
}

func TestPostgresStore_Reschedule(t *testing.T) {
	s, q := newPostgresFixture(t)
	_, _ = s.Enqueue(JobRecord{Queue: q, Name: "retry"})
	rec, _ := s.Claim(q, time.Now())
	rec.Attempts = 2
	rec.NextRunAt = time.Now().Add(-1 * time.Second)
	rec.LastError = "boom"
	if err := s.Reschedule(rec); err != nil {
		t.Fatalf("reschedule: %v", err)
	}
	again, err := s.Claim(q, time.Now())
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if again.Attempts != 2 || again.LastError != "boom" {
		t.Errorf("rescheduled record lost metadata: %+v", again)
	}
}

func TestPostgresStore_DeadLetterChainsError(t *testing.T) {
	s, q := newPostgresFixture(t)
	id, _ := s.Enqueue(JobRecord{Queue: q, Name: "doomed", Attempts: 5})
	rec, _ := s.Claim(q, time.Now())
	rec.LastError = "503 from upstream"
	_ = s.Reschedule(rec)
	_, _ = s.Claim(q, time.Now())

	if err := s.DeadLetter(id, "max attempts reached"); err != nil {
		t.Fatalf("DLQ: %v", err)
	}

	// Read dlq row directly.
	p := s.(*postgresStore)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var finalErr string
	err := p.pool.QueryRow(ctx,
		`SELECT final_error FROM _sky_jobs_dead WHERE id = $1`, int64(id)).Scan(&finalErr)
	if err != nil {
		t.Fatalf("dlq row read: %v", err)
	}
	if !strings.Contains(finalErr, "max attempts reached") {
		t.Errorf("final_error missing caller message: %q", finalErr)
	}
	if !strings.Contains(finalErr, "last attempt") {
		t.Errorf("final_error should chain prior LastError: %q", finalErr)
	}
}

func TestPostgresStore_DeadLetterIdempotent(t *testing.T) {
	s, _ := newPostgresFixture(t)
	if err := s.DeadLetter(JobID(987654321), "doesn't exist"); err != nil {
		t.Errorf("DLQ on missing id should be no-op, got %v", err)
	}
}

func TestPostgresStore_Cancel(t *testing.T) {
	s, q := newPostgresFixture(t)
	id, _ := s.Enqueue(JobRecord{Queue: q, Name: "skip"})
	if err := s.Cancel(id); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := s.Claim(q, time.Now()); err != ErrNoJob {
		t.Errorf("cancelled job should not be claimable")
	}
}

func TestPostgresStore_CancelClaimedJobFails(t *testing.T) {
	s, q := newPostgresFixture(t)
	id, _ := s.Enqueue(JobRecord{Queue: q, Name: "running"})
	_, _ = s.Claim(q, time.Now())
	if err := s.Cancel(id); !errors.Is(err, ErrJobNotFound) {
		t.Errorf("cancel on claimed job should be ErrJobNotFound, got %v", err)
	}
}

func TestPostgresStore_QueueDepth(t *testing.T) {
	s, q := newPostgresFixture(t)
	for i := 0; i < 4; i++ {
		_, _ = s.Enqueue(JobRecord{Queue: q, Name: "x"})
	}
	d, err := s.QueueDepth(q)
	if err != nil {
		t.Fatalf("queue depth: %v", err)
	}
	if d != 4 {
		t.Errorf("expected depth=4, got %d", d)
	}
}
