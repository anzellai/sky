//go:build !js

package rt

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// The dedupe set holds at most `limit` digests in memory however many keys
// it sees, spills the rest to sorted run files, still finds a repeat of any
// key it saw before (in memory, in a run, or after runs were merged), and
// removes its files on close.
func TestAiDedupeSetMemoryStaysBounded(t *testing.T) {
	root := t.TempDir()
	const limit = 500
	const distinct = 40000
	s, err := newAiDedupeSet(root, limit)
	if err != nil {
		t.Fatal(err)
	}
	batch := make([]string, 0, 1000)
	flush := func(wantNew bool) {
		t.Helper()
		fresh, err := s.add(batch)
		if err != nil {
			t.Fatal(err)
		}
		for i, f := range fresh {
			if f != wantNew {
				t.Fatalf("key %q: new=%v, want %v", batch[i], f, wantNew)
			}
		}
		if len(s.mem) > limit {
			t.Fatalf("the map holds %d digests, over the limit %d", len(s.mem), limit)
		}
		batch = batch[:0]
	}
	for i := 0; i < distinct; i++ {
		batch = append(batch, fmt.Sprintf("customer-%d@example.test", i))
		if len(batch) == cap(batch) {
			flush(true)
		}
	}
	flush(true)
	if s.peak > limit {
		t.Fatalf("peak %d digests in memory, over the limit %d", s.peak, limit)
	}
	if len(s.runs) == 0 || len(s.runs) > aiDedupeMaxRuns {
		t.Fatalf("runs = %d, want between 1 and %d", len(s.runs), aiDedupeMaxRuns)
	}
	// Every key again, in another order: each is a repeat, wherever it lives.
	for i := distinct - 1; i >= 0; i -= 7 {
		batch = append(batch, fmt.Sprintf("customer-%d@example.test", i))
		if len(batch) == cap(batch) {
			flush(false)
		}
	}
	flush(false)
	// A repeat within one batch counts once.
	fresh, err := s.add([]string{"new-a", "new-a", "new-b"})
	if err != nil {
		t.Fatal(err)
	}
	if !fresh[0] || fresh[1] || !fresh[2] {
		t.Fatalf("in-batch repeat: %v", fresh)
	}
	dir := s.dir
	if n := s.close(); n != distinct+2 {
		t.Fatalf("close counted %d distinct keys, want %d", n, distinct+2)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the work directory %s survived close", dir)
	}
}

// The heap a set needs does not grow with the number of keys: a set with a
// small limit seeing ten times more keys uses about the same live heap.
func TestAiDedupeSetHeapDoesNotGrowWithKeys(t *testing.T) {
	heapFor := func(keys int) uint64 {
		s, err := newAiDedupeSet(t.TempDir(), 1000)
		if err != nil {
			t.Fatal(err)
		}
		defer s.close()
		batch := make([]string, 0, 1000)
		for i := 0; i < keys; i++ {
			batch = append(batch, fmt.Sprintf("row-%d", i))
			if len(batch) == cap(batch) {
				if _, err := s.add(batch); err != nil {
					t.Fatal(err)
				}
				batch = batch[:0]
			}
		}
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		live := m.HeapAlloc
		runtime.KeepAlive(s)
		return live
	}
	small := heapFor(20000)
	large := heapFor(200000)
	// An unbounded set of 200000 digests needs over 6 MB more than one of
	// 20000; the bounded set must stay within 2 MB of its smaller run.
	if large > small+2<<20 {
		t.Fatalf("live heap grew from %d to %d bytes with ten times the keys", small, large)
	}
}

// Open, add and close through the kernels; a closed handle is refused.
func TestAiDedupeKernels(t *testing.T) {
	root := t.TempDir()
	h, e := runAiTask(t, Ai_dedupeOpen(root, 2))
	if e != nil {
		t.Fatalf("open: %v", e)
	}
	got, e := runAiTask(t, Ai_dedupeAdd(h, []any{"a", "b", "a", "c", "b"}))
	if e != nil {
		t.Fatalf("add: %v", e)
	}
	if fmt.Sprint(got) != fmt.Sprint([]any{true, true, false, true, false}) {
		t.Fatalf("add = %v", got)
	}
	n, e := runAiTask(t, Ai_dedupeClose(h))
	if e != nil || n.(int) != 3 {
		t.Fatalf("close = %v, %v; want 3", n, e)
	}
	if _, e := runAiTask(t, Ai_dedupeAdd(h, []any{"x"})); e == nil {
		t.Fatal("a closed set must refuse new keys")
	} else if aiErrCode(t, e) != "InvalidSetting" {
		t.Fatalf("closed set: %v", e)
	}
}

// A set directory older than a day is swept by the next open.
func TestAiDedupeSweepsStaleDirectories(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, "set-stale")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	s, err := newAiDedupeSet(root, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("a stale set directory was not removed")
	}
}

// The duplicate check of `inspect` / `validate` (Ai_dedupeFirst) uses the
// same bounded set: it holds at most `limit` digests in memory however many
// conversations it sees, and still answers every repeat with the row where
// the conversation first appeared, whether that row is in memory, in a run,
// or in a merged run.
func TestAiDedupeFirstRowsStayBounded(t *testing.T) {
	root := t.TempDir()
	const limit = 64
	const distinct = 5000
	h := Ai_dedupeOpen(root, limit).(func() any)()
	handle := AsString(aiDedupeTestOk(t, h))
	s, err := aiDedupeLookup(handle)
	if err != nil {
		t.Fatal(err)
	}
	row := 1
	check := func(keys []string, want []int) {
		t.Helper()
		got := AsList(aiDedupeTestOk(t, Ai_dedupeFirst(handle, row, keys).(func() any)()))
		if len(got) != len(want) {
			t.Fatalf("got %d answers, want %d", len(got), len(want))
		}
		for i := range want {
			if AsInt(got[i]) != want[i] {
				t.Fatalf("row %d (%q): first = %d, want %d", row+i, keys[i], AsInt(got[i]), want[i])
			}
		}
		if len(s.mem) > limit {
			t.Fatalf("the map holds %d digests, over the limit %d", len(s.mem), limit)
		}
		row += len(keys)
	}
	conv := func(i int) string { return fmt.Sprintf("user\x01question %d\x00assistant\x01answer", i) }
	// Rows 1..distinct are all new, sent in chunks of 100.
	for c := 0; c < distinct; c += 100 {
		keys := make([]string, 0, 100)
		want := make([]int, 0, 100)
		for i := c; i < c+100; i++ {
			keys = append(keys, conv(i))
			want = append(want, 0)
		}
		check(keys, want)
	}
	if s.peak > limit {
		t.Fatalf("peak %d digests in memory, over the limit %d", s.peak, limit)
	}
	if len(s.runs) == 0 || len(s.runs) > aiDedupeMaxRuns {
		t.Fatalf("runs = %d, want between 1 and %d", len(s.runs), aiDedupeMaxRuns)
	}
	// Every conversation again: each repeats the row it first appeared at.
	for c := distinct - 100; c >= 0; c -= 100 {
		keys := make([]string, 0, 100)
		want := make([]int, 0, 100)
		for i := c; i < c+100; i++ {
			keys = append(keys, conv(i))
			want = append(want, i+1)
		}
		check(keys, want)
	}
	// A repeat inside one chunk names the chunk's own earlier row.
	next := row
	check([]string{"fresh", "other", "fresh"}, []int{0, 0, next})
	dir := s.dir
	if n := AsInt(aiDedupeTestOk(t, Ai_dedupeClose(handle).(func() any)())); n != distinct+2 {
		t.Fatalf("close counted %d distinct conversations, want %d", n, distinct+2)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the work directory %s survived close", dir)
	}
}

// aiDedupeTestOk unwraps an `Ok` task result, failing the test on `Err`.
func aiDedupeTestOk(t *testing.T, r any) any {
	t.Helper()
	res, ok := r.(SkyResult[any, any])
	if !ok {
		t.Fatalf("not a Result: %T", r)
	}
	if res.Tag != 0 {
		t.Fatalf("Err: %v", res.ErrValue)
	}
	return res.OkValue
}
