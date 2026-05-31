// server_websocket.go — Sky.Http.Server.WebSocket — server-side
// WebSocket upgrade for v0.15.46.
//
// Mirror image of websocket.go (client-side outbound).  Where
// websocket.go DIALS a WebSocket endpoint and pushes incoming
// frames to a Sky.Live update loop as Subs, this file accepts an
// upgrade INSIDE a Sky.Http.Server handler — the user's handler
// returns a WebSocket "response" sentinel, the dispatcher hijacks
// the connection, runs nhooyr/websocket's Accept(), and invokes
// the user's WebSocketServerCfg callbacks against each connected
// peer.
//
// Sky surface:
//
//   handle : Server.Request -> Task Error Server.Response
//   handle req =
//       Server.WebSocket.upgrade req
//           { onConnect = \sock -> Log.println "client connected"
//           , onMessage = \sock msg -> Server.WebSocket.sendToClient sock ("echo: " ++ msg)
//           , onClose   = \sock -> Log.println "client gone"
//           , onError   = \sock err -> Log.println (Error.toString err)
//           , maxMessageBytes = 1048576
//           , originPatterns = [ "https://*.example.com" ]
//           }
//
// Lifecycle:
//
//   1. ServerWebSocket_upgrade builds a SkyResponse with a sentinel
//      Body ("__sky_ws:<token>") and stashes the WebSocketServerCfg
//      in pendingWebSocketCfgs.  The dispatcher in rt.go detects
//      the sentinel and routes to serveWebSocketUpgrade BEFORE the
//      normal buffered-body / streaming-body paths.
//
//   2. serveWebSocketUpgrade runs websocket.Accept (which Hijack()s
//      the connection), constructs a serverSocketHandle, calls
//      onConnect, then loops reading messages — onMessage per
//      frame, onClose on graceful close, onError on protocol /
//      network error.
//
//   3. sendToClient / sendBinaryToClient / broadcast / closeClient
//      kernels look the handle up by id and serialise writes via
//      the handle's writeMu.

package rt

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nhooyr.io/websocket"
)

// ═════════════════════════════════════════════════════════════════════
// serverSocketHandle — one connected peer on an upgraded endpoint
// ═════════════════════════════════════════════════════════════════════

type serverSocketHandle struct {
	id   int64
	conn *websocket.Conn

	ctx    context.Context
	cancel context.CancelFunc

	closed atomic.Bool

	// writeMu serialises concurrent writes (the user may call
	// broadcast from one goroutine while a handler emits from
	// another — Sky doesn't forbid this).
	writeMu sync.Mutex

	closeOnce sync.Once
	closed_ch chan struct{}
}

func (h *serverSocketHandle) Close() {
	h.closeOnce.Do(func() {
		h.closed.Store(true)
		if h.conn != nil {
			_ = h.conn.Close(websocket.StatusNormalClosure, "server closed")
		}
		h.cancel()
		close(h.closed_ch)
	})
}

func (h *serverSocketHandle) IsClosed() bool { return h.closed.Load() }

// ═════════════════════════════════════════════════════════════════════
// Registry — id → handle
// ═════════════════════════════════════════════════════════════════════

var serverSocketHandles sync.Map // map[int64]*serverSocketHandle
var serverSocketIDCounter atomic.Int64

func nextServerSocketID() int64 {
	for {
		id := serverSocketIDCounter.Add(1)
		if id != 0 {
			return id
		}
	}
}

func lookupServerSocket(id int64) *serverSocketHandle {
	v, ok := serverSocketHandles.Load(id)
	if !ok {
		return nil
	}
	return v.(*serverSocketHandle)
}

// ═════════════════════════════════════════════════════════════════════
// Pending-cfg registry (sentinel-token bridge through typed codegen)
// ═════════════════════════════════════════════════════════════════════
//
// Same shape as pendingStreamHandlers (server_stream.go) — when the
// user's handler returns a `Server.WebSocket.upgrade req cfg` value
// the result is a SkyResponse whose Body carries `__sky_ws:<token>`.
// The token resolves to the cfg via this map; the dispatcher detects
// the sentinel + routes through serveWebSocketUpgrade.

var pendingWebSocketCfgs sync.Map // map[string]webSocketUpgradeCfg
var pendingWebSocketTokenSeq atomic.Int64

const pendingWebSocketSentinelPrefix = "__sky_ws:"

// webSocketUpgradeCfg packs everything the upgrade dispatcher needs
// from the user's Sky-side cfg record.
type webSocketUpgradeCfg struct {
	onConnect       any
	onMessage       any
	onClose         any
	onError         any
	maxMessageBytes int
	originPatterns  []string
}

func registerPendingWebSocketCfg(cfg webSocketUpgradeCfg) string {
	id := pendingWebSocketTokenSeq.Add(1)
	token := fmt.Sprintf("%d", id)
	pendingWebSocketCfgs.Store(token, cfg)
	return token
}

func takePendingWebSocketCfg(token string) (webSocketUpgradeCfg, bool) {
	v, ok := pendingWebSocketCfgs.LoadAndDelete(token)
	if !ok {
		return webSocketUpgradeCfg{}, false
	}
	return v.(webSocketUpgradeCfg), true
}

// extractPendingWebSocketToken — token extractor for the sentinel.
func extractPendingWebSocketToken(body string) (string, bool) {
	if !strings.HasPrefix(body, pendingWebSocketSentinelPrefix) {
		return "", false
	}
	return body[len(pendingWebSocketSentinelPrefix):], true
}

// ═════════════════════════════════════════════════════════════════════
// Sky-facing kernels — server side
// ═════════════════════════════════════════════════════════════════════

// ServerWebSocket_upgrade implements:
//
//	Sky.Http.Server.WebSocket.upgrade
//	    : Server.Request -> WebSocketServerCfg msg -> Task Error Server.Response
//
// The actual upgrade Hijack runs LATER (in serveWebSocketUpgrade)
// once the dispatcher detects the sentinel.  This function just
// packages the cfg + returns a sentinel-carrying SkyResponse.
func ServerWebSocket_upgrade(_ any, cfgArg any) any {
	cfg := webSocketUpgradeCfg{
		onConnect:       recordField(cfgArg, "OnConnect", "onConnect"),
		onMessage:       recordField(cfgArg, "OnMessage", "onMessage"),
		onClose:         recordField(cfgArg, "OnClose", "onClose"),
		onError:         recordField(cfgArg, "OnError", "onError"),
		maxMessageBytes: wsDefaultMaxMessageBytes,
	}
	if m := AsInt(recordField(cfgArg, "MaxMessageBytes", "maxMessageBytes")); m > 0 {
		cfg.maxMessageBytes = m
	}
	patternsV := recordField(cfgArg, "OriginPatterns", "originPatterns")
	if patternsV != nil {
		items := AsList(patternsV)
		for _, it := range items {
			cfg.originPatterns = append(cfg.originPatterns, fmt.Sprintf("%v", it))
		}
	}
	token := registerPendingWebSocketCfg(cfg)
	return func() any {
		return Ok[any, any](SkyResponse{
			Status:      200, // ignored — upgrade overrides with 101
			ContentType: "application/octet-stream",
			Body:        pendingWebSocketSentinelPrefix + token,
		})
	}
}

// ServerWebSocket_sendToClient implements:
//
//	Sky.Http.Server.WebSocket.sendToClient
//	    : WebSocketServer -> String -> Task Error ()
func ServerWebSocket_sendToClient(sidArg any, msgArg any) any {
	id := asInt64(sidArg)
	msg := fmt.Sprintf("%v", msgArg)
	return func() any {
		h := lookupServerSocket(id)
		if h == nil || h.IsClosed() {
			return Err[any, any](ErrUnavailable("server.websocket.sendToClient: socket closed"))
		}
		h.writeMu.Lock()
		defer h.writeMu.Unlock()
		writeCtx, cancel := context.WithTimeout(h.ctx, wsConsumerTimeout)
		defer cancel()
		if err := h.conn.Write(writeCtx, websocket.MessageText, []byte(msg)); err != nil {
			return Err[any, any](ErrNetwork("server.websocket.sendToClient: " + err.Error()))
		}
		return Ok[any, any](skyUnit())
	}
}

// ServerWebSocket_sendBinaryToClient implements:
//
//	Sky.Http.Server.WebSocket.sendBinaryToClient
//	    : WebSocketServer -> String -> Task Error ()
func ServerWebSocket_sendBinaryToClient(sidArg any, msgArg any) any {
	id := asInt64(sidArg)
	msg := fmt.Sprintf("%v", msgArg)
	return func() any {
		h := lookupServerSocket(id)
		if h == nil || h.IsClosed() {
			return Err[any, any](ErrUnavailable("server.websocket.sendBinaryToClient: socket closed"))
		}
		h.writeMu.Lock()
		defer h.writeMu.Unlock()
		writeCtx, cancel := context.WithTimeout(h.ctx, wsConsumerTimeout)
		defer cancel()
		if err := h.conn.Write(writeCtx, websocket.MessageBinary, []byte(msg)); err != nil {
			return Err[any, any](ErrNetwork("server.websocket.sendBinaryToClient: " + err.Error()))
		}
		return Ok[any, any](skyUnit())
	}
}

// ServerWebSocket_broadcast implements:
//
//	Sky.Http.Server.WebSocket.broadcast
//	    : List WebSocketServer -> String -> Task Error ()
//
// Best-effort fan-out: sends to every handle in `socks`; collects
// failures into a single Err if every send errored. A partial
// success (some peers got the message, others didn't) returns Ok —
// the failure is logged + the broken connection is closed.
func ServerWebSocket_broadcast(socksArg any, msgArg any) any {
	items := AsList(socksArg)
	msg := fmt.Sprintf("%v", msgArg)
	return func() any {
		if len(items) == 0 {
			return Ok[any, any](skyUnit())
		}
		var lastErr error
		successes := 0
		for _, it := range items {
			id := asInt64(it)
			h := lookupServerSocket(id)
			if h == nil || h.IsClosed() {
				continue
			}
			h.writeMu.Lock()
			writeCtx, cancel := context.WithTimeout(h.ctx, wsConsumerTimeout)
			err := h.conn.Write(writeCtx, websocket.MessageText, []byte(msg))
			cancel()
			h.writeMu.Unlock()
			if err != nil {
				lastErr = err
				h.Close()
				continue
			}
			successes++
		}
		if successes == 0 && lastErr != nil {
			return Err[any, any](ErrNetwork("server.websocket.broadcast: " + lastErr.Error()))
		}
		return Ok[any, any](skyUnit())
	}
}

// ServerWebSocket_closeClient implements:
//
//	Sky.Http.Server.WebSocket.closeClient
//	    : WebSocketServer -> Task Error ()
//
// Idempotent.
func ServerWebSocket_closeClient(sidArg any) any {
	id := asInt64(sidArg)
	return func() any {
		h := lookupServerSocket(id)
		if h != nil {
			h.Close()
		}
		return Ok[any, any](skyUnit())
	}
}

// ═════════════════════════════════════════════════════════════════════
// Dispatcher integration — called from rt.go's handler closure
// ═════════════════════════════════════════════════════════════════════

// serveWebSocketUpgrade runs the upgrade-and-loop dance for one
// connected peer.  Headers are already on the wire when this fires
// (websocket.Accept does its own WriteHeader 101).  The dispatcher
// passed the cfg through `pendingWebSocketCfgs` via the sentinel.
//
// Semantics:
//
//   - Accept fails (Origin denied / hijack unsupported / handshake
//     error) → user's onError invoked with an ErrNetwork; 500 is
//     written to the client; serveWebSocketUpgrade returns.
//
//   - onConnect runs first (user side-effects: log, broadcast
//     announcement, etc).  If onConnect errors, the socket closes
//     immediately.
//
//   - Each successful Read frame dispatches onMessage(sock, text).
//     The loop continues until the peer closes, a read errors, OR
//     the user calls closeClient.
//
//   - Graceful peer close → onClose(sock).
//
//   - Read error (network) → onError(sock, ErrNetwork msg) then close.
func serveWebSocketUpgrade(w http.ResponseWriter, r *http.Request, cfg webSocketUpgradeCfg) {
	acceptOpts := &websocket.AcceptOptions{
		OriginPatterns: cfg.originPatterns,
	}
	// If no origin patterns specified AND we're in dev, allow all.
	// Production gate: when ENV=production AND originPatterns is
	// empty, reject — user must explicitly allow origins.
	if len(cfg.originPatterns) == 0 {
		if productionFromEnv() {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "WebSocket upgrade denied: originPatterns required in production")
			return
		}
		acceptOpts.InsecureSkipVerify = true
	}

	conn, err := websocket.Accept(w, r, acceptOpts)
	if err != nil {
		// Accept already wrote a 4xx/5xx — nothing more to do.
		return
	}
	conn.SetReadLimit(int64(cfg.maxMessageBytes))

	ctx, cancel := context.WithCancel(r.Context())
	h := &serverSocketHandle{
		id:        nextServerSocketID(),
		conn:      conn,
		ctx:       ctx,
		cancel:    cancel,
		closed_ch: make(chan struct{}),
	}
	serverSocketHandles.Store(h.id, h)
	defer func() {
		serverSocketHandles.Delete(h.id)
		h.Close()
	}()

	// Pass the SAME ADT value to all callbacks: opaque
	// `WebSocketServer Int` ADT.
	sockADT := SkyADT{
		Tag:     0,
		SkyName: "WebSocketServer",
		Fields:  []any{h.id},
	}

	// onConnect first.  Drive its returned Task to completion; if
	// it errors, log + close.
	if cfg.onConnect != nil {
		runWsServerCallback(cfg.onConnect, sockADT, "onConnect")
	}

	// Heartbeat: same pattern as the client side.
	hbDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(wsDefaultPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-h.closed_ch:
				close(hbDone)
				return
			case <-ticker.C:
				pingCtx, pcancel := context.WithTimeout(h.ctx, 10*time.Second)
				err := h.conn.Ping(pingCtx)
				pcancel()
				if err != nil {
					h.Close()
					close(hbDone)
					return
				}
			}
		}
	}()
	defer func() {
		<-hbDone
	}()

	// Read loop.
	for {
		typ, data, err := h.conn.Read(h.ctx)
		if err != nil {
			closeStatus := websocket.CloseStatus(err)
			// Graceful close (WebSocket close frame received) OR
			// peer-side TCP teardown (websocat -n1 + similar) — both
			// route to onClose.  Only treat truly unexpected errors
			// (read deadline, connection reset mid-frame) as onError.
			errStr := err.Error()
			isPeerClosed := closeStatus != -1 ||
				strings.Contains(errStr, "EOF") ||
				strings.Contains(errStr, "connection reset") ||
				strings.Contains(errStr, "use of closed network connection") ||
				strings.Contains(errStr, "broken pipe") ||
				strings.Contains(errStr, "websocket: close 1000") ||
				strings.Contains(errStr, "websocket: close 1001") ||
				strings.Contains(errStr, "failed to read frame header: EOF") ||
				h.ctx.Err() != nil
			if isPeerClosed {
				if cfg.onClose != nil {
					runWsServerCallback(cfg.onClose, sockADT, "onClose")
				}
			} else {
				if cfg.onError != nil {
					errVal := ErrNetwork("server.websocket.read: " + errStr)
					runWsServerCallback2(cfg.onError, sockADT, errVal, "onError")
				}
			}
			return
		}
		var payload string
		if typ == websocket.MessageBinary {
			// Sky's String covers byte content — Bytes alias = String.
			payload = string(data)
		} else {
			payload = string(data)
		}
		if cfg.onMessage != nil {
			runWsServerCallback2(cfg.onMessage, sockADT, payload, "onMessage")
		}
	}
}

// runWsServerCallback invokes a single-arg user callback (Task-returning)
// and drives the result to completion.  Errors are logged but don't
// propagate further — the upgrade loop continues until the connection
// breaks.
func runWsServerCallback(cb any, arg any, name string) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[sky.websocket.server] %s panic: %v\n", name, r)
		}
	}()
	taskVal := SkyCall(cb, arg)
	res := anyTaskInvoke(taskVal)
	if res.Tag != 0 {
		fmt.Printf("[sky.websocket.server] %s task errored: %v\n", name, res.ErrValue)
	}
}

// runWsServerCallback2 invokes a two-arg user callback.
func runWsServerCallback2(cb any, arg1, arg2 any, name string) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[sky.websocket.server] %s panic: %v\n", name, r)
		}
	}()
	// First apply arg1 (curried Sky callback shape), then arg2.
	partial := SkyCall(cb, arg1)
	taskVal := SkyCall(partial, arg2)
	res := anyTaskInvoke(taskVal)
	if res.Tag != 0 {
		fmt.Printf("[sky.websocket.server] %s task errored: %v\n", name, res.ErrValue)
	}
}
