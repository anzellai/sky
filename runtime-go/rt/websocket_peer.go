package rt

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// websocket_peer.go — the reader side of one Sky.Spa client WebSocket (the
// browser socket's events, websocket_wasm.go), portable so it is unit-tested
// on the host. The browser delivers each event through a JS callback that must
// not block; the peer queues it, or hands it to the socket's Subs, and a Task
// receive waits on the queue from its own goroutine.
//
// The semantics are the native client's (websocket.go, websocket_task.go):
//
//   - One reader per socket, claimed by the first Sub or Task that reads it.
//     A Task receive on a Sub-read socket is `Err InvalidInput`; a Sub on a
//     Task-read socket is refused.
//   - Events queued before the first reader are kept, in order: the Open
//     event of the handshake, and any frame that arrived before the app
//     subscribed. A Sub that claims the socket gets them first.
//   - A Task receive returns `Just frame` in order, `Nothing` after a close,
//     `Err` on a failed connection; receiveWithin fails with `Err Timeout`
//     and consumes nothing.
//
// The browser buffers incoming data itself and cannot be asked to stop
// reading, so the queue is not bounded here (the native client's 64-frame
// channel is its TCP backpressure; a browser socket has none to give).

// wsPeer is one socket's reader state.
type wsPeer struct {
	mu     sync.Mutex
	queue  []wsEvent
	notify chan struct{}
	owner  int32
	closed bool
	// subs maps a Sub kind ("open" | "message" | "close" | "error") to its
	// toMsg, for a Sub-owned socket.
	subs map[string]any
}

func newWsPeer() *wsPeer {
	return &wsPeer{notify: make(chan struct{}, 1)}
}

// signal wakes a waiting receive (non-blocking; one pending wake is enough,
// the receiver re-checks the queue).
func (p *wsPeer) signal() {
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

// push delivers one browser event. A Sub-owned socket hands it to the matching
// Sub at once (dispatch runs the Msg through the client's update); any other
// socket queues it for its reader.
func (p *wsPeer) push(ev wsEvent, dispatch func(any)) {
	p.mu.Lock()
	if p.owner == wsOwnerSub {
		subs := p.subs
		p.mu.Unlock()
		wsDispatchToSubs(subs, ev, dispatch)
		return
	}
	p.queue = append(p.queue, ev)
	p.mu.Unlock()
	p.signal()
}

// finish marks the socket closed: a waiting receive wakes, takes what is still
// queued, then sees Nothing.
func (p *wsPeer) finish() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.signal()
}

// claim makes want the reader if the socket has none yet, and reports whether
// want is (now) the reader.
func (p *wsPeer) claim(want int32) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.owner == wsOwnerNone {
		p.owner = want
	}
	return p.owner == want
}

// setSubs installs the socket's Subs (reconciled after each update). The first
// non-empty set claims the socket for the Subs and hands them the events that
// were queued before, in order. It reports false when a Task reads the socket
// (the Subs are refused).
func (p *wsPeer) setSubs(subs map[string]any, dispatch func(any)) bool {
	p.mu.Lock()
	if len(subs) == 0 {
		if p.owner == wsOwnerSub {
			p.subs = nil
		}
		p.mu.Unlock()
		return true
	}
	if p.owner == wsOwnerTask {
		p.mu.Unlock()
		return false
	}
	p.owner = wsOwnerSub
	p.subs = subs
	backlog := p.queue
	p.queue = nil
	p.mu.Unlock()
	for _, ev := range backlog {
		wsDispatchToSubs(subs, ev, dispatch)
	}
	return true
}

// awaitFrame waits for the next frame for a Task reader, on the Task's own
// goroutine (a wait that returns, not a background loop). hasLimit selects
// receiveWithin (limit <= 0 polls: a queued frame, else Timeout at once).
func (p *wsPeer) awaitFrame(hasLimit bool, limit time.Duration) (wsTaskOutcome, any) {
	var expired <-chan time.Time
	if hasLimit && limit > 0 {
		t := time.NewTimer(limit)
		defer t.Stop()
		expired = t.C
	}
	for {
		p.mu.Lock()
		for len(p.queue) > 0 {
			ev := p.queue[0]
			p.queue = p.queue[1:]
			if out, v, ok := wsTaskEvent(ev); ok {
				p.mu.Unlock()
				return out, v
			}
		}
		closed := p.closed
		p.mu.Unlock()
		if closed {
			return wsTaskClosed, nil
		}
		if hasLimit && limit <= 0 {
			return wsTaskTimeout, nil
		}
		select {
		case <-p.notify:
		case <-expired:
			return wsTaskTimeout, nil
		}
	}
}

// wsDispatchToSubs runs the Sub of ev's kind, if any, and dispatches its Msg.
// A toMsg that panics drops that event and is reported; the socket stays.
func wsDispatchToSubs(subs map[string]any, ev wsEvent, dispatch func(any)) {
	toMsg, ok := subs[wsEventKindToSubKind(ev.kind)]
	if !ok || toMsg == nil {
		return
	}
	var msg any
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("[sky.websocket] a Sub decoder panicked; the event (kind=%d) is dropped: %v\n", ev.kind, r)
				msg = nil
			}
		}()
		if !isFunc(toMsg) {
			// onOpen: the toMsg IS the Msg.
			msg = toMsg
			return
		}
		switch ev.kind {
		case wsMessageEv:
			msg = sky_call(toMsg, buildWebSocketMessageValue(ev))
		case wsCloseEv:
			msg = sky_call(toMsg, buildCloseCodeValue(ev.closeCode))
		case wsErrorEv:
			msg = sky_call(toMsg, ev.err)
		default:
			msg = sky_call(toMsg, nil)
		}
	}()
	if msg != nil {
		dispatch(msg)
	}
}

// collectWebSocketSubs gathers the WebSocket Sub leaves of a Sub tree: socket
// id → kind → toMsg (last one wins for a repeated kind, as on the server).
func collectWebSocketSubs(s subT, out map[int64]map[string]any) {
	switch s.kind {
	case "subscribeWebSocket":
		m := out[s.socketID]
		if m == nil {
			m = map[string]any{}
			out[s.socketID] = m
		}
		m[s.wsKind] = s.toMsg
	case "batch":
		for _, c := range s.batch {
			if cs, ok := c.(subT); ok {
				collectWebSocketSubs(cs, out)
			}
		}
	}
}

// wsBrowserURL resolves a Sky.Spa client WebSocket URL against the page's own
// URL: a path (`/ws`) or a relative reference connects to the page's host,
// `ws:` for an `http:` page and `wss:` for an `https:` one, so the default is
// same-origin and passes a strict `connect-src 'self'`. An `http(s)://` URL
// maps to `ws(s)://`; a `ws(s)://` URL is used as written.
func wsBrowserURL(raw, pageHref string) (string, error) {
	base, err := url.Parse(pageHref)
	if err != nil {
		return "", fmt.Errorf("the page URL %q does not parse: %v", pageHref, err)
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%q is not a URL: %v", raw, err)
	}
	u := base.ResolveReference(ref)
	switch strings.ToLower(u.Scheme) {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("%q is not a ws:, wss:, http: or https: URL", raw)
	}
	u.Fragment = ""
	return u.String(), nil
}

// wsBrowserCloseCodeOK reports whether a browser WebSocket may close with code
// (the WebSocket API accepts 1000 and 3000-4999 only).
func wsBrowserCloseCodeOK(code int) bool {
	return code == wsStatusNormal || (code >= 3000 && code <= 4999)
}
