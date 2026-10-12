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
