// Subscription manager shared across non-Live TEA backends (Sky.Cli,
// Sky.Tui, future Sky.Gui). Sky.Live owns its own per-session manager
// in live.go (different lifetime + locking model — sessions, SSE,
// race-with-event-dispatch); the shape here is simpler because there's
// only one program, one model, one ticker set.
//
// Lifecycle:
//   - newSubManager(msgCh): create with a target channel for emitted Msgs
//   - subMgr.update(subsFn, model): re-evaluate subs; tear down ones that
//     no longer apply, spawn new ones
//   - subMgr.stopAll(): clean shutdown on EOF / program exit
//
// Currently supports Sub.none, Sub.every, Sub.batch. Each Sub.every
// spawns one goroutine running a time.Ticker; the goroutine pushes the
// Msg into msgCh on each tick. Cancellation is via a per-sub close-
// channel — closing it unblocks the goroutine's select and returns.
//
// Design note: we re-spawn unchanged subs on every model update rather
// than diffing. Tickers are cheap to recreate; the simpler code is
// worth the few ms of churn. If a real perf problem ever shows up,
// add a structural-equality check on the previous sub spec.

package rt

import (
	"time"
)

type subEntry struct {
	cancel chan struct{}
}

type subManager struct {
	msgCh   chan<- any
	active  []subEntry
}

func newSubManager(msgCh chan<- any) *subManager {
	return &subManager{msgCh: msgCh}
}

// update reads the current subscriptions(model) value, tears down any
// previously-active tickers, and spawns fresh ones for the new spec.
// nil subsFn (program didn't declare a subscriptions field) is fine —
// just stop all current subs and leave the empty list.
func (m *subManager) update(subsFn, model any) {
	m.stopAll()
	if subsFn == nil {
		return
	}
	sub := SkyCall(subsFn, model)
	m.spawnAll(sub)
}

// stopAll cancels every active ticker. Safe to call repeatedly.
func (m *subManager) stopAll() {
	for _, e := range m.active {
		select {
		case <-e.cancel:
			// already closed
		default:
			close(e.cancel)
		}
	}
	m.active = nil
}

// spawnAll walks a Sub value (which may be Sub.none, Sub.every, or
// Sub.batch of those) and starts a goroutine per Sub.every leaf.
func (m *subManager) spawnAll(sub any) {
	s, ok := sub.(subT)
	if !ok {
		return
	}
	switch s.kind {
	case "none":
		return
	case "every":
		m.spawnEvery(s)
	case "batch":
		for _, item := range s.batch {
			m.spawnAll(item)
		}
	}
}

func (m *subManager) spawnEvery(s subT) {
	if s.ms <= 0 {
		return
	}
	cancel := make(chan struct{})
	toMsg := s.toMsg
	interval := time.Duration(s.ms) * time.Millisecond
	msgCh := m.msgCh
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-cancel:
				return
			case t := <-ticker.C:
				msg := toMsg
				// If the Msg constructor expects a timestamp arg
				// (Time.every-style), apply it; otherwise pass it
				// through unchanged. Mirrors live.go's behaviour so
				// `Time.every 1000 Tick` and `Time.every 1000 (\ms -> Tick ms)`
				// both work.
				if isFunc(msg) {
					msg = sky_call(toMsg, t.UnixMilli())
				}
				select {
				case msgCh <- msg:
				case <-cancel:
					return
				}
			}
		}
	}()
	m.active = append(m.active, subEntry{cancel: cancel})
}

