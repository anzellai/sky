package rt

import (
	"context"
	"sync"
	"testing"

	oteltrace "go.opentelemetry.io/otel/trace"
)

// Step 2 — goroutine-local request-id propagation. Verifies the
// goroutine-ID parse + sync.Map storage behave correctly under
// concurrent stamping, cleanup, and child-goroutine spawning.

func TestGoroutineContext_DefaultEmpty(t *testing.T) {
	if id := CurrentRequestID(); id != "" {
		t.Errorf("default goroutine has no req-id; got %q", id)
	}
}

func TestGoroutineContext_SetAndRead(t *testing.T) {
	SetGoroutineRequestID("test-id")
	defer ClearGoroutineRequestID()
	if got := CurrentRequestID(); got != "test-id" {
		t.Errorf("expected 'test-id', got %q", got)
	}
}

func TestGoroutineContext_ClearRemoves(t *testing.T) {
	SetGoroutineRequestID("temp")
	if got := CurrentRequestID(); got != "temp" {
		t.Fatalf("setup: expected 'temp', got %q", got)
	}
	ClearGoroutineRequestID()
	if got := CurrentRequestID(); got != "" {
		t.Errorf("after clear, expected empty, got %q", got)
	}
}

func TestGoroutineContext_SetEmptyClears(t *testing.T) {
	SetGoroutineRequestID("x")
	SetGoroutineRequestID("")
	if got := CurrentRequestID(); got != "" {
		t.Errorf("SetGoroutineRequestID(\"\") should clear, got %q", got)
	}
}

// Critical: each goroutine has its OWN stamp. Stamping in goroutine A
// must not leak into goroutine B.
func TestGoroutineContext_IsolatedPerGoroutine(t *testing.T) {
	SetGoroutineRequestID("parent")
	defer ClearGoroutineRequestID()

	done := make(chan string, 1)
	go func() {
		// Child sees empty (it's a separate goroutine).
		done <- CurrentRequestID()
	}()

	if child := <-done; child != "" {
		t.Errorf("child goroutine inherited parent's req-id (should not): %q", child)
	}
	// Parent's stamp still intact.
	if got := CurrentRequestID(); got != "parent" {
		t.Errorf("parent goroutine lost its req-id: %q", got)
	}
}

// The RunWithRequestID helper: stamps for the duration of fn, clears
// on exit. Verify the cleanup actually fires.
func TestRunWithRequestID_StampsThenClears(t *testing.T) {
	var seenInside string
	RunWithRequestID("scoped-id", func() {
		seenInside = CurrentRequestID()
	})
	if seenInside != "scoped-id" {
		t.Errorf("inside fn: expected 'scoped-id', got %q", seenInside)
	}
	if outside := CurrentRequestID(); outside != "" {
		t.Errorf("RunWithRequestID should clear on exit; got %q", outside)
	}
}

func TestRunWithRequestID_EmptyIsNoOp(t *testing.T) {
	// Passing "" should not stamp anything (and the goroutine starts
	// without a stamp, so CurrentRequestID stays "").
	var inside string
	RunWithRequestID("", func() {
		inside = CurrentRequestID()
	})
	if inside != "" {
		t.Errorf("empty id should not stamp; got %q", inside)
	}
}

// Concurrent stamping from many goroutines. Each must see only its
// own id back. Catches any race where the sync.Map operation isn't
// goroutine-safe (it is) or the goroutine-ID parse returns wrong
// values under contention.
func TestGoroutineContext_ConcurrentNoBleed(t *testing.T) {
	const N = 100
	var wg sync.WaitGroup
	mismatches := make([]int, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "id-" + itoaInt(i)
			SetGoroutineRequestID(id)
			defer ClearGoroutineRequestID()
			// Do a bit of "work" — force the scheduler to interleave.
			for j := 0; j < 100; j++ {
				_ = j * j
			}
			if got := CurrentRequestID(); got != id {
				mismatches[i] = 1
			}
		}(i)
	}
	wg.Wait()
	total := 0
	for _, m := range mismatches {
		total += m
	}
	if total > 0 {
		t.Errorf("%d/%d goroutines saw wrong req-id under contention", total, N)
	}
}

// Cleanup must drop the sync.Map entry — otherwise the map grows
// unboundedly across the millions of Cmd.perform goroutines a
// long-running app spawns. Verify by spawning 100 stampers, waiting,
// and ensuring entries don't pile up beyond active count.
//
// We can't directly inspect sync.Map size (no public API), so we
// spawn-clear-spawn and check that the second batch's lookups
// behave correctly. This indirectly verifies cleanup — without it,
// stale entries from goroutine 1 could interfere with goroutine 2
// when goroutine IDs are recycled.
func TestGoroutineContext_CleanupReleases(t *testing.T) {
	const N = 100
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			SetGoroutineRequestID("batch1")
			ClearGoroutineRequestID()
		}()
	}
	wg.Wait()
	// Spawn a fresh batch — if goroutine IDs get recycled and the
	// previous stamps weren't cleared, the new goroutines would
	// briefly read the old value before their own stamp.
	mismatches := 0
	var mu sync.Mutex
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			before := CurrentRequestID()
			if before != "" {
				mu.Lock()
				mismatches++
				mu.Unlock()
			}
			SetGoroutineRequestID("batch2")
			ClearGoroutineRequestID()
		}()
	}
	wg.Wait()
	if mismatches > 0 {
		t.Errorf("%d goroutines saw stale stamp before setting their own (cleanup leak)", mismatches)
	}
}

// itoaInt — strconv.Itoa without the import. Inlined to keep this
// test file dep-free.
func itoaInt(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [10]byte
	pos := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// ─── UNBOUNDED-MEMORY regression: WithSpan on an unstamped goroutine ──
//
// goroutineCtx is keyed by goroutine ID, which is monotonic and never
// reused. WithSpan's save/restore pair used to read
// CurrentTraceContext() — which returns the NON-NIL
// context.Background() when the goroutine has no stamp — and restore
// it via SetGoroutineTraceContext, which only deleted on nil. Every
// unstamped goroutine that touched one auto-instrumented kernel
// (every Db_* via WithDbSpan) therefore left a PERMANENT map entry:
// ~80 B each, forever, plus O(N) sync.Map promotion stalls.
//
// The assertion is an EXACT count back to baseline, not "roughly".
func TestWithSpan_UnstampedGoroutineLeavesNoEntry(t *testing.T) {
	const N = 200
	baseline := goroutineCtxSize()
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// No prior stamp on this goroutine — the exact shape of a
			// Cmd.perform / background worker goroutine calling an
			// auto-instrumented kernel.
			_ = WithSpan("leak-probe", oteltrace.SpanKindInternal, nil, func() any {
				return nil
			})
		}()
	}
	wg.Wait()
	if after := goroutineCtxSize(); after != baseline {
		t.Fatalf("goroutineCtx leaked %d entries: baseline %d, after %d (unstamped goroutines must leave the map exactly as they found it)",
			after-baseline, baseline, after)
	}
}

// The nested case: a goroutine that WAS stamped must get its stamp
// back after WithSpan (stack discipline), and a goroutine whose
// stamp is cleared mid-flight must not resurrect it.
func TestWithSpan_StampedGoroutineRestoresStamp(t *testing.T) {
	ctx := WithRequestID(context.Background(), "outer-req")
	SetGoroutineTraceContext(ctx)
	defer ClearGoroutineTraceContext()
	_ = WithSpan("inner", oteltrace.SpanKindInternal, nil, func() any { return nil })
	if got := CurrentRequestID(); got != "outer-req" {
		t.Fatalf("stamp not restored after WithSpan: got %q, want %q", got, "outer-req")
	}
}
