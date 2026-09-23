//go:build !js

// Subscription manager shared across non-Live TEA backends (Sky.Cli,
// Sky.Tui, Sky.Webview). Sky.Live owns its own per-session manager
// in live.go (different lifetime + locking model — sessions, SSE,
// race-with-event-dispatch); the shape here is simpler because there's
// only one program, one model, one ticker set.
//
// Supported leaves: Sub.none, Sub.every, Sub.batch, Sub.subscribeTopic.
//
// Sub.every is RECONCILED, not rebuilt. The identity of a timer is its
// interval in milliseconds. After every update the manager diffs the
// desired intervals against the running ones: a new interval starts a
// ticker, an interval no longer requested stops its ticker, and an
// interval still requested KEEPS its ticker (and its phase) — only the
// Msg(s) it dispatches are refreshed. Rebuilding every ticker on every
// update (the pre-fix behaviour) meant a slow ticker never fired under a
// stream of faster Msgs: each update restarted its countdown. Every
// Sub.every is honoured — two subscriptions on the same interval both
// dispatch on each tick, in subscription order.
//
// Sub.subscribeTopic registers a topic -> toMsg handler. Terminal loops
// are a single process with a single program, so the pub/sub bus is
// in-process: a Cmd.publish from `update` is delivered to this app's own
// subscribeTopic subscriber for that topic (echo-by-default, as on
// Sky.Live). The handler is resolved when the published payload is
// dequeued by the loop, i.e. against the subscriptions of the model that
// is current at delivery time.

package rt

import (
	"sync"
	"time"
)

// everyTimer is one running Sub.every ticker. toMsgs holds every Msg (or
// Msg constructor) currently subscribed on this interval; it is replaced
// on each reconcile under mu so the ticker goroutine always dispatches
// the latest set.
type everyTimer struct {
	cancel chan struct{}
	mu     sync.Mutex
	toMsgs []any
}

func (t *everyTimer) setMsgs(msgs []any) {
	t.mu.Lock()
	t.toMsgs = msgs
	t.mu.Unlock()
}

func (t *everyTimer) msgs() []any {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]any(nil), t.toMsgs...)
}

type subManager struct {
	msgCh  chan<- any
	mu     sync.Mutex
	timers map[int]*everyTimer
	topics map[string]any
}

func newSubManager(msgCh chan<- any) *subManager {
	return &subManager{msgCh: msgCh, timers: map[int]*everyTimer{}, topics: map[string]any{}}
}

// update evaluates subscriptions(model) and reconciles the running
// tickers and topic handlers against it.
func (m *subManager) update(subsFn, model any) {
	desiredEvery := map[int][]any{}
	desiredTopics := map[string]any{}
	if subsFn != nil {
		collectTeaSubs(SkyCall(subsFn, model), desiredEvery, desiredTopics)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for ms, t := range m.timers {
		if _, keep := desiredEvery[ms]; !keep {
			close(t.cancel)
			delete(m.timers, ms)
		}
	}
	for ms, msgs := range desiredEvery {
		if t, ok := m.timers[ms]; ok {
			t.setMsgs(msgs)
			continue
		}
		t := &everyTimer{cancel: make(chan struct{}), toMsgs: msgs}
		m.timers[ms] = t
		m.spawnEvery(ms, t)
	}
	m.topics = desiredTopics
}

// collectTeaSubs flattens a Sub tree into the interval -> Msgs map and the
// topic -> toMsg map. A repeated topic is last-write-wins (a single
// decoder per topic, matching Sky.Live).
func collectTeaSubs(sub any, every map[int][]any, topics map[string]any) {
	s, ok := sub.(subT)
	if !ok {
		return
	}
	switch s.kind {
	case "every":
		if s.ms > 0 {
			every[s.ms] = append(every[s.ms], s.toMsg)
		}
	case "subscribeTopic":
		if s.topic != "" {
			topics[s.topic] = s.toMsg
		}
	case "batch":
		for _, item := range s.batch {
			collectTeaSubs(item, every, topics)
		}
	}
}

// hasTimers reports whether any Sub.every is currently running. A Sky.Cli
// program with no input handler stays alive while a timer is requested.
func (m *subManager) hasTimers() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.timers) > 0
}

// topicHandler returns the toMsg subscribed on topic, or nil.
func (m *subManager) topicHandler(topic string) any {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.topics[topic]
}

func (m *subManager) stopAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for ms, t := range m.timers {
		close(t.cancel)
		delete(m.timers, ms)
	}
	m.topics = map[string]any{}
}

func (m *subManager) spawnEvery(ms int, t *everyTimer) {
	cancel := t.cancel
	interval := time.Duration(ms) * time.Millisecond
	msgCh := m.msgCh
	// safeGo: a panic in the user's toMsg lambda (e.g. an unforced
	// Result mis-extracted) on a Sub.every tick would otherwise crash
	// silently and leave the Tui terminal stuck. With recovery the
	// user sees the actual error and the shell is restored.
	safeGo("Sub.every ticker", func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-cancel:
				return
			case now := <-ticker.C:
				for _, toMsg := range t.msgs() {
					msg := toMsg
					if isFunc(msg) {
						msg = sky_call(toMsg, now.UnixMilli())
					}
					select {
					case msgCh <- msg:
					case <-cancel:
						return
					}
				}
			}
		}
	})
}
