//go:build !js

package rt

// sub_source.go — the "source" Sub leaf: a subscription fed by a runtime
// object that produces events over time (a child process's output, a file
// watcher's change batches).
//
// One mechanism serves every TEA backend: Sky.Live (live.go,
// applySourceSubsDiff) and the single-process loops (tea_subs.go: Sky.Cli,
// Sky.Tui, Sky.Webview). Each backend supplies only its `deliver` — how a Msg
// reaches `update` — and the runner owns the rest: calling `toMsg`, recovering
// a panicking decoder, and stopping.
//
// # Teardown order: the stream leaves first
//
// When a subscription is dropped (the model no longer asks for it, the
// session ends, the program exits) the runner's `stop` channel closes. The
// runner stops READING the source first — the source's pump returns because
// it selects on `stop` — and only then, on its own goroutine, releases its
// claim on the source (`releaseSub`). So:
//
//   - no goroutine is left blocked on a source nobody consumes (the pump
//     waits on `stop` alongside the source's own wake-up channel);
//   - no Msg is sent after the consumer went away: every send selects on
//     `stop` too, and tea_subs never closes its message channel;
//   - a source handle released after the Sub is gone (Process.close,
//     Watch.close) is never read by a runner that outlived it: the handle's
//     own close wakes the pump, which then observes the closed state and
//     returns.

import (
	"fmt"
	"sync"
	"time"

	"sky-app/rt/periodic"
)

// subSource is implemented by a runtime object that can feed a Sub.
type subSource interface {
	// claimSub makes the Sub the object's consumer. An object has one
	// consumer mode (a Sub or a Task) and a second one is refused.
	claimSub() error
	// releaseSub is called once the runner has stopped reading.
	releaseSub()
	// pump emits events in order until the source ends (returns) or stop
	// closes. emit reports false when the runner is stopping; pump must then
	// return without emitting more.
	pump(stop <-chan struct{}, emit func(ev any) bool)
}

// sourceRunner is one running source subscription.
type sourceRunner struct {
	key      string
	src      subSource
	mu       sync.Mutex
	toMsg    any
	stop     chan struct{}
	stopOnce sync.Once
	done     chan struct{}
	// finished is set when the source ended on its own (a process exited and
	// its Exited event was delivered). Sky.Cli stops counting a finished
	// runner as a reason to stay alive.
	finished bool
}

func (r *sourceRunner) setToMsg(f any) {
	r.mu.Lock()
	r.toMsg = f
	r.mu.Unlock()
}

func (r *sourceRunner) currentToMsg() any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.toMsg
}

func (r *sourceRunner) isFinished() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finished
}

// cancel asks the runner to stop. Non-blocking and idempotent: it may be
// called from inside a delivery (a Sky.Live update that drops its own
// subscription runs on the runner's goroutine).
func (r *sourceRunner) cancel() {
	r.stopOnce.Do(func() { close(r.stop) })
}

// cancelAndWait stops the runner and waits (bounded) until it has stopped
// reading and released the source. Never call it from the runner goroutine.
func (r *sourceRunner) cancelAndWait(timeout time.Duration) bool {
	r.cancel()
	select {
	case <-r.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// startSourceRunner claims src for a Sub and starts its runner goroutine.
// deliver hands one Msg to the backend; it must select on stop and return
// false when stop closes. onEnd (may be nil) runs on the runner goroutine
// after the source is released.
func startSourceRunner(key string, src subSource, toMsg any,
	deliver func(msg any, stop <-chan struct{}) bool, onEnd func()) (*sourceRunner, error) {
	if err := src.claimSub(); err != nil {
		return nil, err
	}
	r := &sourceRunner{
		key:   key,
		src:   src,
		toMsg: toMsg,
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go r.run(deliver, onEnd)
	return r, nil
}

func (r *sourceRunner) run(deliver func(msg any, stop <-chan struct{}) bool, onEnd func()) {
	defer close(r.done)
	defer func() {
		if onEnd != nil {
			onEnd()
		}
	}()
	// Released AFTER the pump returned: the stream leaves first.
	defer r.src.releaseSub()
	defer func() {
		if rec := recover(); rec != nil {
			LogRecoveredPanic("sky.sub", "subscription source "+r.key, rec)
		}
	}()
	r.src.pump(r.stop, func(ev any) bool {
		select {
		case <-r.stop:
			return false
		default:
		}
		msg := r.decode(ev)
		if msg == nil {
			return true
		}
		return deliver(msg, r.stop)
	})
	select {
	case <-r.stop:
	default:
		r.mu.Lock()
		r.finished = true
		r.mu.Unlock()
	}
}

// decode turns one event into a Msg through the current toMsg. A panicking
// decoder drops that event (logged) and the subscription keeps running.
func (r *sourceRunner) decode(ev any) (msg any) {
	defer func() {
		if rec := recover(); rec != nil {
			LogRecoveredPanic("sky.sub", fmt.Sprintf("subscription %s decoder, dropping one event", r.key), rec)
			msg = nil
		}
	}()
	toMsg := r.currentToMsg()
	if isFunc(toMsg) {
		return sky_call(toMsg, ev)
	}
	return toMsg
}

// subSourceOf returns the source a "subscribeSource" leaf carries.
func subSourceOf(leaf subT) (subSource, bool) {
	src, ok := leaf.source.(subSource)
	return src, ok && leaf.sourceKey != ""
}

// runSourceCycles drives a source pump one cycle at a time, each cycle under
// its own recover (periodic.Guard). A panicking cycle is reported and lost,
// and the pump continues after a short pause; it does not take the
// subscription down with it. cycle reports true when the pump is finished.
func runSourceCycles(name string, stop <-chan struct{}, cycle func() bool) {
	for {
		done, completed := false, false
		periodic.Guard(name, periodicReport, func() error {
			done = cycle()
			completed = true
			return nil
		})
		if done {
			return
		}
		if !completed {
			select {
			case <-stop:
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
}
