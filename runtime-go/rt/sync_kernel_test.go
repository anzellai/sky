package rt

import (
	"sync"
	"testing"
	"time"
)

// syncRun runs a Std.Sync Task and splits its Result.
func syncRun(t *testing.T, task any) (any, any) {
	t.Helper()
	f, ok := task.(func() any)
	if !ok {
		t.Fatalf("a Std.Sync operation must be a Task thunk, got %T", task)
	}
	r := f().(SkyResult[any, any])
	if r.Tag == 0 {
		return r.OkValue, nil
	}
	return nil, r.ErrValue
}

func syncOk(t *testing.T, task any) any {
	t.Helper()
	v, e := syncRun(t, task)
	if e != nil {
		t.Fatalf("unexpected Err %v", Basics_errorToStringT(e))
	}
	return v
}

// Concurrent `update`s never lose a write (run under -race).
func TestSyncRefUpdateIsAtomic(t *testing.T) {
	ref := syncOk(t, Sync_newRef(0))
	inc := func(v any) any { return AsInt(v) + 1 }
	var wg sync.WaitGroup
	for g := 0; g < 64; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				syncOk(t, Sync_update(inc, ref))
			}
		}()
	}
	wg.Wait()
	if got := syncOk(t, Sync_get(ref)); got != 6400 {
		t.Fatalf("64 x 100 increments = %v", got)
	}
	syncOk(t, Sync_set(7, ref))
	if got := syncOk(t, Sync_get(ref)); got != 7 {
		t.Fatalf("set 7 then get = %v", got)
	}
}

// A compare-and-swap retry loop from many goroutines counts exactly.
func TestSyncRefCompareAndSwap(t *testing.T) {
	ref := syncOk(t, Sync_newRef(0))
	if swapped := syncOk(t, Sync_compareAndSwap(1, 2, ref)); swapped != false {
		t.Fatal("CAS with a wrong expected value must not swap")
	}
	var wg sync.WaitGroup
	for g := 0; g < 32; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				for {
					cur := syncOk(t, Sync_get(ref))
					if syncOk(t, Sync_compareAndSwap(cur, AsInt(cur)+1, ref)) == true {
						break
					}
				}
			}
		}()
	}
	wg.Wait()
	if got := syncOk(t, Sync_get(ref)); got != 1600 {
		t.Fatalf("CAS loop total = %v", got)
	}
	// Structural equality, as Sky `==`.
	rec := syncOk(t, Sync_newRef(Just[any]("a")))
	if syncOk(t, Sync_compareAndSwap(Just[any]("a"), Nothing[any](), rec)) != true {
		t.Fatal("CAS compares values structurally")
	}
}

// withLock serialises a get-then-set that would race without it, and
// releases the lock when the task fails.
func TestSyncMutexWithLock(t *testing.T) {
	m := syncOk(t, Sync_newMutex(struct{}{}))
	ref := syncOk(t, Sync_newRef(0))
	readModifyWrite := func() any {
		v := syncOk(t, Sync_get(ref))
		time.Sleep(time.Microsecond)
		return syncOk(t, Sync_set(AsInt(v)+1, ref))
	}
	var wg sync.WaitGroup
	for g := 0; g < 40; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task := func() any { readModifyWrite(); return Ok[any, any](struct{}{}) }
			syncOk(t, Sync_withLock(m, task))
		}()
	}
	wg.Wait()
	if got := syncOk(t, Sync_get(ref)); got != 40 {
		t.Fatalf("40 locked read-modify-writes = %v", got)
	}
	failing := func() any { return Err[any, any](ErrIo("boom")) }
	if _, e := syncRun(t, Sync_withLock(m, failing)); e == nil {
		t.Fatal("withLock returns the task's Err")
	}
	done := make(chan struct{})
	go func() {
		syncOk(t, Sync_withLock(m, func() any { return Ok[any, any](1) }))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the lock was not released after a failing task")
	}
}

// A bounded queue: FIFO order, push blocks while full, pop blocks while
// empty, popWithin times out, close drains then ends.
func TestSyncQueue(t *testing.T) {
	if _, e := syncRun(t, Sync_newQueue(0)); e == nil {
		t.Fatal("capacity 0 is InvalidInput")
	}
	q := syncOk(t, Sync_newQueue(2))
	syncOk(t, Sync_push("a", q))
	syncOk(t, Sync_push("b", q))
	if n := syncOk(t, Sync_size(q)); n != 2 {
		t.Fatalf("size = %v", n)
	}
	pushed := make(chan struct{})
	go func() {
		syncOk(t, Sync_push("c", q)) // blocks: full
		close(pushed)
	}()
	select {
	case <-pushed:
		t.Fatal("push into a full queue must wait")
	case <-time.After(50 * time.Millisecond):
	}
	if v := syncOk(t, Sync_pop(q)); !deepEq(v, Just[any]("a")) {
		t.Fatalf("pop = %v", v)
	}
	<-pushed
	for _, want := range []string{"b", "c"} {
		if v := syncOk(t, Sync_pop(q)); !deepEq(v, Just[any](want)) {
			t.Fatalf("pop = %v, want %s", v, want)
		}
	}
	start := time.Now()
	_, e := syncRun(t, Sync_popWithin(30, q))
	if e == nil || errKindOf(e) != "Timeout" {
		t.Fatalf("popWithin on an empty queue = %v, want Timeout", e)
	}
	if time.Since(start) < 25*time.Millisecond {
		t.Fatal("popWithin returned before its timeout")
	}
	got := make(chan any, 1)
	go func() { got <- syncOk(t, Sync_popWithin(5000, q)) }()
	time.Sleep(20 * time.Millisecond)
	syncOk(t, Sync_push("d", q))
	if v := <-got; !deepEq(v, Just[any]("d")) {
		t.Fatalf("a waiting popWithin gets the pushed item, got %v", v)
	}

	// Close: queued items still pop, then Nothing; pushes fail; a waiting pop
	// and a waiting push both wake.
	syncOk(t, Sync_push("e", q))
	syncOk(t, Sync_close(q))
	syncOk(t, Sync_close(q)) // idempotent
	if _, e := syncRun(t, Sync_push("f", q)); e == nil || errKindOf(e) != "Unavailable" {
		t.Fatalf("push after close = %v", e)
	}
	if v := syncOk(t, Sync_pop(q)); !deepEq(v, Just[any]("e")) {
		t.Fatalf("a closed queue still yields its items, got %v", v)
	}
	if v := syncOk(t, Sync_pop(q)); !deepEq(v, Nothing[any]()) {
		t.Fatalf("a closed empty queue pops Nothing, got %v", v)
	}

	q2 := syncOk(t, Sync_newQueue(1))
	waitPop := make(chan any, 1)
	go func() { waitPop <- syncOk(t, Sync_pop(q2)) }()
	syncOk(t, Sync_push(1, q2))
	<-waitPop
	syncOk(t, Sync_push(2, q2))
	waitPush := make(chan any, 1)
	go func() { _, e := syncRun(t, Sync_push(3, q2)); waitPush <- e }()
	time.Sleep(20 * time.Millisecond)
	syncOk(t, Sync_close(q2))
	select {
	case e := <-waitPush:
		if e == nil {
			t.Fatal("a push waiting on a full queue fails when it closes")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close did not wake a waiting push")
	}
}

// Many producers and consumers through a small queue: every item arrives
// exactly once (run under -race).
func TestSyncQueueProducersConsumers(t *testing.T) {
	q := syncOk(t, Sync_newQueue(4))
	const producers, each = 8, 200
	var pw sync.WaitGroup
	for p := 0; p < producers; p++ {
		pw.Add(1)
		go func(p int) {
			defer pw.Done()
			for i := 0; i < each; i++ {
				syncOk(t, Sync_push(p*each+i, q))
			}
		}(p)
	}
	seen := make([]int, producers*each)
	var mu sync.Mutex
	var cw sync.WaitGroup
	for c := 0; c < 4; c++ {
		cw.Add(1)
		go func() {
			defer cw.Done()
			for {
				v := syncOk(t, Sync_pop(q)).(SkyMaybe[any])
				if v.Tag != 0 {
					return
				}
				mu.Lock()
				seen[AsInt(v.JustValue)]++
				mu.Unlock()
			}
		}()
	}
	pw.Wait()
	syncOk(t, Sync_close(q))
	cw.Wait()
	for i, n := range seen {
		if n != 1 {
			t.Fatalf("item %d arrived %d times", i, n)
		}
	}
}

// A value of another shape is an Error, never a panic.
func TestSyncRejectsForeignValues(t *testing.T) {
	if _, e := syncRun(t, Sync_get(42)); e == nil || errKindOf(e) != "InvalidInput" {
		t.Fatalf("get on a non-Ref = %v", e)
	}
	if _, e := syncRun(t, Sync_pop(SkyADT{SkyName: "Ref__Internal", Fields: []any{&syncRef{}}})); e == nil {
		t.Fatal("pop on a Ref must be an Error")
	}
	if s := SkyShow(syncOk(t, Sync_newRef(1))); s != "<Ref>" {
		t.Fatalf("a Ref prints as %q", s)
	}
}
