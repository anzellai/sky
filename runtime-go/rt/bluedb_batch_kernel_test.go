package rt

import (
	"testing"
)

// batchTriples builds a Sky List (String,String,String) of (tag, key, value).
func batchTriples(ops ...[3]string) []any {
	out := []any{}
	for _, p := range ops {
		out = append(out, SkyTuple3{V0: p[0], V1: p[1], V2: p[2]})
	}
	return out
}

// runBatchErr forces the task and asserts it returned Err, returning the Err value.
func runBatchErr(t *testing.T, task any) any {
	t.Helper()
	res := task.(func() any)()
	r, ok := res.(SkyResult[any, any])
	if !ok || r.Tag == 0 {
		t.Fatalf("expected Err, got %#v", res)
	}
	return r.ErrValue
}

// getVal reads a key directly from the engine (bypasses the Sky Maybe wrap).
func getVal(t *testing.T, id int, key string) (string, bool) {
	t.Helper()
	v, ok := idxDB(id).Get([]byte(key))
	return string(v), ok
}

func TestBatchAtomicLandingAndOneCommit(t *testing.T) {
	id := registerIdxStore(t)
	b0, w0, _ := idxDB(id).Stats()

	runOK(t, BlueDB_batch(id, batchTriples(
		[3]string{"put", "k1", "v1"},
		[3]string{"put", "k2", "v2"},
		[3]string{"put", "k3", "v3"},
		[3]string{"put", "k4", "v4"},
		[3]string{"put", "k5", "v5"},
	)))

	// All five readable.
	for _, kv := range [][2]string{{"k1", "v1"}, {"k2", "v2"}, {"k3", "v3"}, {"k4", "v4"}, {"k5", "v5"}} {
		if got, ok := getVal(t, id, kv[0]); !ok || got != kv[1] {
			t.Fatalf("%s: want %q, got %q (present=%v)", kv[0], kv[1], got, ok)
		}
	}

	// The whole 5-op batch is ONE group-commit: batches +1, writes +5.
	b1, w1, _ := idxDB(id).Stats()
	if b1-b0 != 1 {
		t.Fatalf("batch must be ONE commit: batches delta = %d, want 1", b1-b0)
	}
	if w1-w0 != 5 {
		t.Fatalf("5-op batch: writes delta = %d, want 5", w1-w0)
	}
}

func TestBatchVsSeparatePutsCommitCount(t *testing.T) {
	id := registerIdxStore(t)

	// N separate BlueDB_put calls → N commits (each sequential Put is its own
	// group-commit iteration).
	bP0, _, _ := idxDB(id).Stats()
	for i, kv := range [][2]string{{"p1", "a"}, {"p2", "b"}, {"p3", "c"}} {
		_ = i
		runOK(t, BlueDB_put(id, kv[0], kv[1]))
	}
	bP1, _, _ := idxDB(id).Stats()
	if bP1-bP0 != 3 {
		t.Fatalf("3 separate puts: batches delta = %d, want 3", bP1-bP0)
	}

	// The same 3 writes as ONE batch → exactly 1 commit (the point of batch).
	bB0, _, _ := idxDB(id).Stats()
	runOK(t, BlueDB_batch(id, batchTriples(
		[3]string{"put", "q1", "a"},
		[3]string{"put", "q2", "b"},
		[3]string{"put", "q3", "c"},
	)))
	bB1, _, _ := idxDB(id).Stats()
	if bB1-bB0 != 1 {
		t.Fatalf("3-op batch: batches delta = %d, want 1", bB1-bB0)
	}
}

func TestBatchMixedPutDelete(t *testing.T) {
	id := registerIdxStore(t)
	// Pre-seed.
	runOK(t, BlueDB_put(id, "keep", "old"))
	runOK(t, BlueDB_put(id, "drop", "gone"))

	runOK(t, BlueDB_batch(id, batchTriples(
		[3]string{"put", "keep", "new"}, // overwrite
		[3]string{"put", "fresh", "1"},  // insert
		[3]string{"del", "drop", ""},    // delete
	)))

	if got, ok := getVal(t, id, "keep"); !ok || got != "new" {
		t.Fatalf("keep: want new, got %q (present=%v)", got, ok)
	}
	if got, ok := getVal(t, id, "fresh"); !ok || got != "1" {
		t.Fatalf("fresh: want 1, got %q (present=%v)", got, ok)
	}
	if _, ok := getVal(t, id, "drop"); ok {
		t.Fatalf("drop must be deleted")
	}
}

func TestBatchNulKeyRejectedNoPartialApply(t *testing.T) {
	id := registerIdxStore(t)
	runOK(t, BlueDB_put(id, "seed", "v0"))
	b0, _, _ := idxDB(id).Stats()

	// A NUL-containing key anywhere in the batch → the whole batch is rejected
	// BEFORE WriteBatch, so nothing partially applies.
	runBatchErr(t, BlueDB_batch(id, batchTriples(
		[3]string{"put", "good", "1"},
		[3]string{"put", "bad\x00key", "2"},
	)))

	if _, ok := getVal(t, id, "good"); ok {
		t.Fatalf("guard must fire before WriteBatch: no op may land")
	}
	if got, ok := getVal(t, id, "seed"); !ok || got != "v0" {
		t.Fatalf("seed must be untouched: got %q (present=%v)", got, ok)
	}
	if b1, _, _ := idxDB(id).Stats(); b1 != b0 {
		t.Fatalf("rejected batch must not commit: batches delta = %d, want 0", b1-b0)
	}
}

func TestBatchUnknownTagRejectedNoApply(t *testing.T) {
	id := registerIdxStore(t)
	runOK(t, BlueDB_put(id, "seed", "v0"))
	b0, _, _ := idxDB(id).Stats()

	runBatchErr(t, BlueDB_batch(id, batchTriples(
		[3]string{"put", "good", "1"},
		[3]string{"upsert", "x", "2"}, // unknown tag
	)))

	if _, ok := getVal(t, id, "good"); ok {
		t.Fatalf("unknown tag must abort before WriteBatch: no op may land")
	}
	if got, ok := getVal(t, id, "seed"); !ok || got != "v0" {
		t.Fatalf("seed must be untouched: got %q", got)
	}
	if b1, _, _ := idxDB(id).Stats(); b1 != b0 {
		t.Fatalf("rejected batch must not commit: batches delta = %d, want 0", b1-b0)
	}
}

func TestBatchEmptyIsNoOp(t *testing.T) {
	id := registerIdxStore(t)
	b0, _, _ := idxDB(id).Stats()
	runOK(t, BlueDB_batch(id, batchTriples())) // empty list → Ok, no commit
	if b1, _, _ := idxDB(id).Stats(); b1 != b0 {
		t.Fatalf("empty batch must not commit: batches delta = %d, want 0", b1-b0)
	}
}

func TestBatchClosedStore(t *testing.T) {
	// A handle that was never registered → store not found.
	missing := int(bluedbNextID.Add(1))
	runBatchErr(t, BlueDB_batch(missing, batchTriples([3]string{"put", "k", "v"})))
}
