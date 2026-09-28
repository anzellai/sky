package rt

import "testing"

// A WebSocket-delivered Msg whose SSE frame is dropped (sess.sseCh full) must
// flag every connection out of sync, exactly like a Time.every tick or an
// Http.Stream chunk (#9: an ingress drop means every connection missed the
// frame). The WebSocket path used to count the drop and nothing else, so the
// browser stayed silently diverged until the next unrelated frame.
func TestWsSubDispatch_IngressDropFlagsAllConns(t *testing.T) {
	n := 0
	app := &liveApp{
		update: func(msg, model any) any {
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			n++
			return velement("div", nil, []any{vtext(string(rune('a' + n%26)))})
		},
	}
	sess := &liveSession{
		cancelSub: make(chan struct{}),
		sseCh:     make(chan sseFrame, 1),
		model:     "m",
	}
	sess.lastShippedBody = app.dispatch(sess, "bootstrap")
	id, _, resync := sess.registerSSEConn("tab")
	sess.sseCh <- sseFrame{data: "occupied"} // ingress full: the next frame drops

	reg := &wsSubReg{socketID: 1, kind: "message", toMsg: func(any) any { return "Got" }}
	app.dispatchOneWsSub(sess, reg, wsEvent{kind: wsMessageEv, text: "hi"})

	if !sess.connOutOfSync(id) {
		t.Fatal("a dropped WebSocket-delivered frame must flag the connection out of sync")
	}
	if !signalled(resync) {
		t.Fatal("a dropped WebSocket-delivered frame must signal the connection's resync")
	}
}
