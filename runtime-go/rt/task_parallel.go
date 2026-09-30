// task_parallel.go — Task.parallel, Task.parallelN and Task.spawn: the Task
// kernels that run Sky code on goroutines of their own. Every such goroutine
// is started with goSky (task_go.go), so it inherits the caller's
// goroutine-local context and a panic in it never ends the process.

package rt

import "sync"

// parallelItem is one branch outcome sent to the collector.
type parallelItem struct {
	idx      int
	tag      int
	ok       any
	err      any
	panicked *skyPanic
}

// parallelCollector joins the branches of one Task.parallel / parallelN run.
//
// The result channel is buffered to the number of branches, so a branch never
// blocks on its send. That makes the "has the collector gone?" question exact
// only under a lock: a branch that panics after the collector returned (on
// another branch's Err or panic) must be LOGGED, and a buffered send would
// otherwise land in a channel nobody reads, losing the panic in silence.
// `finish` marks the collector gone under the lock, then drains what is
// already buffered and logs any panic found there.
type parallelCollector struct {
	context string
	ch      chan parallelItem
	mu      sync.Mutex
	gone    bool
}

func newParallelCollector(context string, n int) *parallelCollector {
	return &parallelCollector{context: context, ch: make(chan parallelItem, n)}
}

// send delivers a branch outcome, or reports false when the collector has
// already returned.
func (c *parallelCollector) send(it parallelItem) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gone {
		return false
	}
	c.ch <- it // buffered to n: never blocks
	return true
}

// sendPanic delivers a branch panic to the waiting caller, or logs it when
// the caller has already returned.
func (c *parallelCollector) sendPanic(idx int, p *skyPanic) {
	if !c.send(parallelItem{idx: idx, panicked: p}) {
		logSkyPanic(c.context, p)
	}
}

// isGone reports whether the collector has returned (a dispatcher stops
// launching once it has).
func (c *parallelCollector) isGone() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gone
}

// finish marks the collector gone and logs every panic already buffered.
func (c *parallelCollector) finish() {
	c.mu.Lock()
	c.gone = true
	c.mu.Unlock()
	for {
		select {
		case it := <-c.ch:
			if it.panicked != nil {
				logSkyPanic(c.context, it.panicked)
			}
		default:
			return
		}
	}
}

// branch starts task i on a goSky goroutine. `release`, when non-nil, runs
// when the branch ends, whether it returned or panicked.
func (c *parallelCollector) branch(i int, t any, release func()) {
	goSkyWith(c.context, func() {
		if release != nil {
			defer release()
		}
		r := forceTask(t)
		c.send(parallelItem{idx: i, tag: r.Tag, ok: r.OkValue, err: r.ErrValue})
	}, func(p *skyPanic) { c.sendPanic(i, p) })
}

// collect waits for n outcomes. The first Err is returned; the first panic is
// re-raised on the calling goroutine, carrying the branch's stack. Either way
// the siblings still running are abandoned: their results are discarded and a
// later panic among them is logged.
func (c *parallelCollector) collect(n int) any {
	results := make([]any, n)
	var raise *skyPanic
	var failed any
	isErr := false
	for received := 0; received < n; received++ {
		it := <-c.ch
		if it.panicked != nil {
			raise = it.panicked
			break
		}
		if it.tag != 0 {
			failed, isErr = it.err, true
			break
		}
		results[it.idx] = it.ok
	}
	c.finish()
	if raise != nil {
		reraiseSkyPanic(raise)
	}
	if isErr {
		return Err[any, any](failed)
	}
	return Ok[any, any](results)
}

// Task_parallel: goroutine-backed fan-out; preserves input order;
// short-circuits on the FIRST error (in arrival order).
//
//   - Tasks are dispatched eagerly (SkyTask thunks have no input ctx
//     parameter), so on the first Err the siblings still running are
//     abandoned: they run to their natural end in the background and their
//     results are discarded.
//   - Result order is preserved by index: results[i] holds task i's OkValue
//     when the function returns Ok with the full collected slice.
//   - When several tasks Err concurrently, the FIRST Err observed wins.
//   - A branch that PANICS is re-raised on the caller's goroutine, so the
//     caller's own recovery (a server's per-request net, a Live Cmd's net,
//     main's LogPanicAndExit) handles it like a panic in sequential code. A
//     branch that panics after the caller has returned is logged.
func Task_parallel(tasks any) any {
	return func() any {
		xs := AsList(tasks)
		n := len(xs)
		if n == 0 {
			return Ok[any, any]([]any{})
		}
		c := newParallelCollector("Task.parallel", n)
		for i, t := range xs {
			c.branch(i, t, nil)
		}
		return c.collect(n)
	}
}

// Task_spawn runs `t` on a background goroutine and returns Ok(unit) at once —
// fire-and-forget. The spawned task's result and any error are discarded, so use
// it only for a long-running background task (a server loop, a job poller) that
// must run ALONGSIDE the caller while the caller keeps its OWN goroutine.
//
// The desktop runner is exactly this case: on macOS the native webview MUST be
// created on the process main thread (`webview.New` faults on any other), so the
// server has to run on a spawned goroutine while the main goroutine goes on to
// open the window. `Task_parallel` cannot express that — it runs every branch on
// a child goroutine and blocks the caller — which is why the webview used to
// fault.
//
// A panic inside the spawned task is recovered, so a background failure never
// aborts the process, and it is LOGGED through the classified panic log
// (panic class, errId, hint and the production stack policy).
func Task_spawn(t any) any {
	return taskSpawnWith(t, nil)
}

// taskSpawnWith is Task_spawn with a completion callback. `finished`, when
// non-nil, runs on the spawned goroutine after the task has returned AND after
// any panic has been recovered and logged. Only tests pass it: a test that
// returns as soon as the task body ends races the panic log that is still
// being written on the spawned goroutine.
func taskSpawnWith(t any, finished func()) any {
	return func() any {
		done := func() {
			if finished != nil {
				finished()
			}
		}
		goSkyWith("Task.spawn", func() {
			_ = forceTask(t)
			done()
		}, func(p *skyPanic) {
			logSkyPanic("Task.spawn", p)
			done()
		})
		return Ok[any, any](struct{}{})
	}
}

// Task_parallelN: like Task_parallel, but runs at most `limit` tasks
// concurrently (a semaphore-bounded fan-out) and STOPS launching further tasks
// once the first error or panic is observed. Same result contract as
// Task_parallel: input order preserved, first Err short-circuits, a branch
// panic is re-raised on the caller, in-flight siblings drain in the background
// with their results discarded (no goroutine leak — every worker releases its
// slot and exits).
//
// This is the primitive to reach for under fan-out load: `Task_parallel` spawns
// len(tasks) goroutines at once (unbounded), which for a service fanning out to
// thousands of items is a goroutine/FD/connection storm. `parallelN` caps the
// live worker count at `limit` and, because the dispatcher halts on the first
// error, never launches the tail of a doomed batch. `limit` is clamped to >= 1.
func Task_parallelN(limit any, tasks any) any {
	return func() any {
		lim := AsInt(limit)
		if lim < 1 {
			lim = 1
		}
		xs := AsList(tasks)
		n := len(xs)
		if n == 0 {
			return Ok[any, any]([]any{})
		}
		c := newParallelCollector("Task.parallelN", n)
		sem := make(chan struct{}, lim)
		stop := make(chan struct{})
		// Dispatcher: acquire a slot before launching each task; stop launching
		// once the collector has returned (first error or panic), so a failed
		// batch does not keep spawning work. It runs no Sky code itself.
		go func() {
			for i, t := range xs {
				select {
				case <-stop:
					return
				case sem <- struct{}{}:
				}
				if c.isGone() {
					<-sem
					return
				}
				c.branch(i, t, func() { <-sem })
			}
		}()
		defer close(stop)
		return c.collect(n)
	}
}
