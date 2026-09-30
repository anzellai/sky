//go:build unix

package rt

import (
	"strconv"
	"strings"
	"testing"
)

// The A-1b regressions: five v0.26.1 handle types (WebSocket,
// WebSocketServer, StreamId, StreamWriter, Cache) used per-boot counters
// starting at 1, and the client socket and stream lookups fell back from the
// session's own map to the sessionless one. A restored model holding
// `WebSocket 3` reached sessionless socket 3 (say, a background feed); a
// stale `Cache 2` reached whichever cache got id 2 on this boot.

func assertRandomID(t *testing.T, what string, ids ...int64) {
	t.Helper()
	for i, id := range ids {
		if id < 1<<32 {
			t.Errorf("%s id %d = %d: not a random 62-bit id", what, i, id)
		}
		if i > 0 && (id == ids[i-1]+1 || id == ids[i-1]-1) {
			t.Errorf("%s ids %d and %d are sequential", what, ids[i-1], id)
		}
	}
}

func TestHandleIDs_AreRandomForEveryHandleType(t *testing.T) {
	assertRandomID(t, "WebSocket", nextWsID(), nextWsID(), nextWsID())
	assertRandomID(t, "StreamId", nextStreamID(), nextStreamID(), nextStreamID())
	assertRandomID(t, "WebSocketServer", nextServerSocketID(), nextServerSocketID(), nextServerSocketID())
	assertRandomID(t, "StreamWriter", nextServerStreamID(), nextServerStreamID(), nextServerStreamID())
	var caches []int64
	for i := 0; i < 3; i++ {
		c := procOk(t, procTask(t, Cache_new(map[string]any{"maxEntries": 4, "ttlMs": 0}))).(int)
		caches = append(caches, int64(c))
	}
	assertRandomID(t, "Cache", caches...)
	for old := 1; old <= 16; old++ {
		res := procTask(t, Cache_get(old, "k"))
		if res.Tag == 0 {
			t.Fatalf("stale cache id %d resolved", old)
		}
		if !strings.Contains(errorMessageOf(res.ErrValue), "not live in this server") {
			t.Fatalf("stale cache id %d: unclear Err %q", old, errorMessageOf(res.ErrValue))
		}
	}
}

// TestHandleIDs_PendingUpgradeTokensAreRandom: the token that bridges a
// handler's `upgrade` / `stream` response to the dispatcher travels in the
// response BODY. A counter made it guessable, so a handler that echoes client
// input could name another request's pending token and take its handler.
func TestHandleIDs_PendingUpgradeTokensAreRandom(t *testing.T) {
	var ids []int64
	for i := 0; i < 3; i++ {
		ws := registerPendingWebSocketCfg(webSocketUpgradeCfg{})
		st := registerPendingStreamHandler(nil)
		for _, tok := range []string{ws, st} {
			n, err := strconv.ParseInt(tok, 10, 64)
			if err != nil {
				t.Fatalf("token %q is not an id", tok)
			}
			ids = append(ids, n)
		}
		takePendingWebSocketCfg(ws)
		takePendingStreamHandler(st)
	}
	assertRandomID(t, "pending token", ids...)
}

// TestHandleIDs_NoSessionlessFallbackInsideASession: a session reaches only
// its own client sockets and streams, never the sessionless registry.
func TestHandleIDs_NoSessionlessFallbackInsideASession(t *testing.T) {
	sess := &liveSession{done: make(chan struct{})}
	ws := &wsHandle{id: nextWsID()}
	registerWs(nil, ws)
	defer unregisterWs(nil, ws.id)
	if lookupWs(nil, ws.id) != ws {
		t.Fatal("a sessionless caller lost its own socket")
	}
	if lookupWs(sess, ws.id) != nil {
		t.Error("a session reached a sessionless socket")
	}
	st := &streamHandle{id: nextStreamID()}
	registerStream(nil, st)
	defer unregisterStream(nil, st.id)
	if lookupStream(nil, st.id) != st {
		t.Fatal("a sessionless caller lost its own stream")
	}
	if lookupStream(sess, st.id) != nil {
		t.Error("a session reached a sessionless stream")
	}
	// And the session's own handles still resolve.
	own := &wsHandle{id: nextWsID()}
	registerWs(sess, own)
	if lookupWs(sess, own.id) != own {
		t.Error("a session lost its own socket")
	}
	var res SkyResult[any, any]
	runWithLiveSession(sess, func() { res = procTask(t, WebSocket_send(int(ws.id), "x")) })
	if res.Tag == 0 || !strings.Contains(errorMessageOf(res.ErrValue), "not live in this server") {
		t.Errorf("send from a session to a sessionless socket: %+v", res)
	}
}
