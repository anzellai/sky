// Subscription manager shared across non-Live TEA backends (Sky.Cli,
// Sky.Tui, future Sky.Gui). Sky.Live owns its own per-session manager
// in live.go (different lifetime + locking model — sessions, SSE,
// race-with-event-dispatch); the shape here is simpler because there's
// only one program, one model, one ticker set.
//
// Currently supports Sub.none, Sub.every, Sub.batch. Each Sub.every
// spawns one goroutine running a `time.After` loop; the goroutine
// pushes the Msg into msgCh on each tick. Cancellation is via a
// per-sub close-channel.

package rt

import (
	"time"
)

type subEntry struct {
	cancel chan struct{}
}

type subManager struct {
	msgCh  chan<- any
	active []subEntry
}

func newSubManager(msgCh chan<- any) *subManager {
	return &subManager{msgCh: msgCh}
}

func (m *subManager) update(subsFn, model any) {
	m.stopAll()
	if subsFn == nil {
		return
	}
	sub := SkyCall(subsFn, model)
	m.spawnAll(sub)
}

func (m *subManager) stopAll() {
	for _, e := range m.active {
		select {
		case <-e.cancel:
		default:
			close(e.cancel)
		}
	}
	m.active = nil
}

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
	// safeGo: a panic in the user's toMsg lambda (e.g. an unforced
	// Result mis-extracted) on a Sub.every tick would otherwise crash
	// silently and leave the Tui terminal stuck. With recovery the
	// user sees the actual error and the shell is restored.
	safeGo("Sub.every ticker", func() {
		for {
			select {
			case <-cancel:
				return
			case <-time.After(interval):
			}
			msg := toMsg
			if isFunc(msg) {
				msg = sky_call(toMsg, time.Now().UnixMilli())
			}
			select {
			case msgCh <- msg:
			case <-cancel:
				return
			}
		}
	})
	m.active = append(m.active, subEntry{cancel: cancel})
}
