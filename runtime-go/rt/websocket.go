// websocket.go — Sky.Core.WebSocket client-side runtime (v0.15.46).
//
// Outbound WebSocket connections from a Sky program: open a socket,
// send text / binary frames, receive incoming frames as a Sub-driven
// stream of Msgs in Sky.Live update loops. Mirror of http_stream.go
// in shape; underlying transport is `nhooyr.io/websocket` (modern,
// context-aware, simpler API than gorilla/websocket).
//
// Architecture parallel: Cycle 4 HS (Sky.Core.Http.Stream).
//
//   Sky side:                          Runtime side:
//   ─────────                          ─────────────
//   WebSocket.connect url            → WebSocket_connect: dial, register
//                                      *wsHandle on session (OR on the
//                                      sessionless global map for plain
//                                      Sky.Http.Server / Cli callers),
//                                      start read goroutine.
//
//   WebSocket.send sock msg          → WebSocket_send: write a text
//                                      frame; blocks up to
//                                      wsConsumerTimeout if the write
//                                      buffer is full.
//
//   WebSocket.onMessage sock toMsg   → Sub.subscribeWebSocket sock toMsg.
//                                      The drain goroutine reads incoming
//                                      frames + dispatches via app.dispatch.
//
//   WebSocket.close sock             → WebSocket_close: send a Normal
//                                      close frame + tear down the
//                                      socket. Idempotent.
//
// Concurrency contract:
//
//   - Per-session registry. *wsHandle lives on liveSession.sockets
//     (a map[int64]*wsHandle), guarded by liveSession.socketsMu.
//     Cleanup is local to the session — markDone walks every socket
//     and closes it (mirrors http_stream.go).
//
//   - Bounded channel (cap 64) per socket for incoming frames; reader
//     goroutine drops with a consumer-stall timeout (30s) if the
//     drain consumer wedges.
//
//   - Heartbeat: nhooyr/websocket sends pings every `pingInterval`
//     (default 30s) via `Ping()` inside a separate goroutine. Server
//     -side reads tolerate idle indefinitely (LLM bidirectional chat
//     may have multi-minute pauses).
//
//   - Whole-connection timeout: NONE. WebSockets are long-lived by
//     design.

package rt

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nhooyr.io/websocket"
)

// ═════════════════════════════════════════════════════════════════════
// Per-socket constants
// ═════════════════════════════════════════════════════════════════════

const (
	// wsReadChanBuffer — bounded channel capacity per socket. The
	// reader goroutine drops with a consumer-stall timeout if the
	// drain consumer falls this far behind. 64 matches typical
	// bursty WebSocket traffic (collab editor ops, multiplayer
	// game state, financial ticks) without over-allocating idle
	// sockets.
	wsReadChanBuffer = 64

	// wsConsumerTimeout — if the reader goroutine cannot push an
	// event onto the channel within this window, it logs + abandons
	// the socket. Bounds the runaway-goroutine class.
	wsConsumerTimeout = 30 * time.Second

	// wsDefaultPingInterval — nhooyr/websocket's Ping cadence by
	// default. Tunable via WebSocketCfg.pingInterval. Matches the
	// SSE heartbeat cadence used by Sky.Live so reverse proxies
	// see consistent traffic shape.
	wsDefaultPingInterval = 30 * time.Second

	// wsDefaultDialTimeout — header / handshake timeout. Mirrors
	// streamHeaderTimeout (Sky.Core.Http.Stream).
	wsDefaultDialTimeout = 30 * time.Second

	// wsDefaultMaxMessageBytes — server-side default upper bound on
	// a single incoming message. Bounds memory amplification from
	// a buggy / hostile peer. 1 MiB matches the SSE maxBodyBytes
	// default.
	wsDefaultMaxMessageBytes = 1 << 20 // 1 MiB
)

// ═════════════════════════════════════════════════════════════════════
// wsEvent — value pushed from the reader goroutine to the drain
// goroutine. Mirrors streamEvent in http_stream.go.
// ═════════════════════════════════════════════════════════════════════

type wsEventKind int

const (
	wsOpenEv    wsEventKind = iota // socket connected — emitted once on connect
	wsMessageEv                    // text or binary frame received
	wsCloseEv                      // socket closed (Normal / GoingAway / etc.)
	wsErrorEv                      // protocol / network error
)

type wsEvent struct {
	kind      wsEventKind
	text      string   // wsMessageEv text — UTF-8 frame
	binary    string   // wsMessageEv binary — byte string (Sky.Core.Bytes alias)
	isBinary  bool     // distinguishes text vs binary message
	closeCode int      // wsCloseEv — WebSocket close code
	closeReason string // wsCloseEv — close reason string
	err       any      // wsErrorEv — Sky-shaped Error ADT
}

// ═════════════════════════════════════════════════════════════════════
// wsHandle — one live WebSocket connection
// ═════════════════════════════════════════════════════════════════════

type wsHandle struct {
	id   int64
	conn *websocket.Conn
	ch   chan wsEvent

	// ctx + cancel — bound to the connection lifecycle. cancel fires
	// from Close OR from the read goroutine on a fatal read error.
	ctx    context.Context
	cancel context.CancelFunc

	closed atomic.Bool
	done   chan struct{}

	// writeMu serialises concurrent writes from user code. nhooyr
	// requires one writer at a time; without this a concurrent
	// send-from-Cmd.perform plus a send-from-update-handler would
	// race the underlying conn.Write.
	writeMu sync.Mutex

	closeOnce sync.Once
}

// Close marks the handle closed, sends a Normal close frame, releases
// the conn, and cancels the context (terminating the reader). Idempotent.
func (sh *wsHandle) Close() {
	sh.closeOnce.Do(func() {
		sh.closed.Store(true)
		if sh.conn != nil {
			// Best-effort close; ignore errors (peer may have gone
			// away already, in which case the close write fails).
			_ = sh.conn.Close(websocket.StatusNormalClosure, "client closed")
		}
		sh.cancel()
		close(sh.done)
	})
}

func (sh *wsHandle) IsClosed() bool { return sh.closed.Load() }

// deliver pushes one event onto sh.ch with the consumer-stall timeout.
// Returns true if the event landed; false if the consumer stalled past
// wsConsumerTimeout (signal to abandon the connection).
func (sh *wsHandle) deliver(ev wsEvent) bool {
	select {
	case sh.ch <- ev:
		return true
	default:
	}
	t := time.NewTimer(wsConsumerTimeout)
	defer t.Stop()
	select {
	case sh.ch <- ev:
		return true
	case <-sh.done:
		return true
	case <-t.C:
		fmt.Printf("[sky.websocket] consumer stall on socket %d (event kind=%d dropped after %s)\n",
			sh.id, ev.kind, wsConsumerTimeout)
		return false
	}
}

// ═════════════════════════════════════════════════════════════════════
// Per-session + sessionless registries
// ═════════════════════════════════════════════════════════════════════

// wsIDCounter — process-wide monotonic source of unique socket ids.
var wsIDCounter atomic.Int64

// sessionlessSockets — fallback registry for sockets opened OUTSIDE a
// live session (plain Sky.Http.Server handlers, direct-Task callers,
// tests). Same pattern as sessionlessStreams.
var sessionlessSockets sync.Map // map[int64]*wsHandle

func nextWsID() int64 {
	for {
		id := wsIDCounter.Add(1)
		if id != 0 {
			return id
		}
	}
}

func registerWs(sess *liveSession, sh *wsHandle) {
	if sess == nil {
		sessionlessSockets.Store(sh.id, sh)
		return
	}
	sess.socketsMu.Lock()
	if sess.sockets == nil {
		sess.sockets = map[int64]*wsHandle{}
	}
	sess.sockets[sh.id] = sh
	sess.socketsMu.Unlock()
}

func unregisterWs(sess *liveSession, id int64) {
	if sess == nil {
		sessionlessSockets.Delete(id)
		return
	}
	sess.socketsMu.Lock()
	delete(sess.sockets, id)
	sess.socketsMu.Unlock()
}

func lookupWs(sess *liveSession, id int64) *wsHandle {
	if sess != nil {
		sess.socketsMu.Lock()
		sh := sess.sockets[id]
		sess.socketsMu.Unlock()
		if sh != nil {
			return sh
		}
	}
	if v, ok := sessionlessSockets.Load(id); ok {
		return v.(*wsHandle)
	}
	return nil
}

// closeAllSockets walks every active socket on the session, closes it,
// clears the map. Called from markDone (session terminal teardown).
// Returns count for logging.
func closeAllSockets(sess *liveSession) int {
	if sess == nil {
		return 0
	}
	sess.socketsMu.Lock()
	handles := make([]*wsHandle, 0, len(sess.sockets))
	for _, sh := range sess.sockets {
		if sh != nil {
			handles = append(handles, sh)
		}
	}
	sess.sockets = nil
	sess.socketsMu.Unlock()
	for _, sh := range handles {
		sh.Close()
	}
	return len(handles)
}

// ═════════════════════════════════════════════════════════════════════
// Reader goroutine — pulls frames from conn.Read + pushes wsEvents
// ═════════════════════════════════════════════════════════════════════

func (sh *wsHandle) runReader() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Printf("[sky.websocket] reader panic on socket %d: %v\n", sh.id, r)
			sh.Close()
		}
	}()
	// Emit Open event first so the user's Sub sees the connection
	// is live.
	if !sh.deliver(wsEvent{kind: wsOpenEv}) {
		return
	}
	for {
		if sh.IsClosed() {
			return
		}
		typ, data, err := sh.conn.Read(sh.ctx)
		if err != nil {
			// Peer closed cleanly: surface a Close event with the
			// status code. Other errors → Error event.
			closeStatus := websocket.CloseStatus(err)
			if closeStatus != -1 {
				sh.deliver(wsEvent{
					kind:        wsCloseEv,
					closeCode:   int(closeStatus),
					closeReason: err.Error(),
				})
			} else if err == io.EOF || sh.ctx.Err() != nil {
				// Context cancelled (we asked to close) — exit silently.
				sh.deliver(wsEvent{
					kind:        wsCloseEv,
					closeCode:   int(websocket.StatusNormalClosure),
					closeReason: "",
				})
			} else {
				sh.deliver(wsEvent{
					kind: wsErrorEv,
					err:  ErrNetwork("websocket read: " + err.Error()),
				})
			}
			sh.Close()
			return
		}
		ev := wsEvent{kind: wsMessageEv}
		if typ == websocket.MessageBinary {
			ev.isBinary = true
			ev.binary = string(data)
		} else {
			ev.text = string(data)
		}
		if !sh.deliver(ev) {
			sh.Close()
			return
		}
	}
}

// ═════════════════════════════════════════════════════════════════════
// Sky-facing kernel entries — client side
// ═════════════════════════════════════════════════════════════════════

// WebSocket_connect implements:
//
//	Sky.Core.WebSocket.connect : String -> Task Error Int
//
// The Sky wrapper wraps the returned Int in `WebSocket Int`.
func WebSocket_connect(urlArg any) any {
	url := fmt.Sprintf("%v", urlArg)
	return func() any {
		return doWebSocketConnect(url, nil, nil, wsDefaultDialTimeout, wsDefaultPingInterval)
	}
}

// WebSocket_connectWith implements:
//
//	Sky.Core.WebSocket.connectWith
//	    : { url : String, headers : List (String, String)
//	      , timeout : Int, pingInterval : Int }
//	    -> Task Error Int
func WebSocket_connectWith(cfgArg any) any {
	urlV := recordField(cfgArg, "Url", "url")
	headersV := recordField(cfgArg, "Headers", "headers")
	timeoutV := recordField(cfgArg, "Timeout", "timeout")
	pingIntervalV := recordField(cfgArg, "PingInterval", "pingInterval")
	url := fmt.Sprintf("%v", urlV)
	timeout := wsDefaultDialTimeout
	if t := AsInt(timeoutV); t > 0 {
		timeout = time.Duration(t) * time.Millisecond
	}
	ping := wsDefaultPingInterval
	if p := AsInt(pingIntervalV); p > 0 {
		ping = time.Duration(p) * time.Millisecond
	}
	headers := wsHeaderListToHttp(headersV)
	return func() any {
		return doWebSocketConnect(url, headers, nil, timeout, ping)
	}
}

// doWebSocketConnect runs the actual dial + handle registration.
// Shared by both connect and connectWith. Returns Ok[int64] / Err Error.
func doWebSocketConnect(url string, headers http.Header, tlsCfg *tls.Config, dialTimeout, pingInterval time.Duration) any {
	dialCtx, cancelDial := context.WithTimeout(context.Background(), dialTimeout)
	defer cancelDial()
	opts := &websocket.DialOptions{
		HTTPHeader: headers,
	}
	if tlsCfg != nil {
		opts.HTTPClient = &http.Client{
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		}
	}
	conn, _, err := websocket.Dial(dialCtx, url, opts)
	if err != nil {
		return Err[any, any](ErrNetwork("websocket.connect: " + err.Error()))
	}
	conn.SetReadLimit(int64(wsDefaultMaxMessageBytes))

	connCtx, connCancel := context.WithCancel(context.Background())
	sh := &wsHandle{
		id:     nextWsID(),
		conn:   conn,
		ch:     make(chan wsEvent, wsReadChanBuffer),
		ctx:    connCtx,
		cancel: connCancel,
		done:   make(chan struct{}),
	}
	sess := currentLiveSession()
	registerWs(sess, sh)

	// Start the reader goroutine.
	go sh.runReader()
	// Heartbeat: emit application-level pings on the interval.
	if pingInterval > 0 {
		go wsHeartbeat(sh, pingInterval)
	}
	return Ok[any, any](sh.id)
}

// wsHeaderListToHttp converts a Sky `List (String, String)` into an
// http.Header. Accepts nil / empty list / nested SkyList shape.
func wsHeaderListToHttp(headers any) http.Header {
	if headers == nil {
		return nil
	}
	items := AsList(headers)
	if len(items) == 0 {
		return nil
	}
	h := http.Header{}
	for _, item := range items {
		// Each item is a Sky tuple (String, String). Tuples lower
		// to a SkyTuple struct OR a 2-element slice.
		k, v := wsExtractStringPair(item)
		if k != "" {
			h.Add(k, v)
		}
	}
	if len(h) == 0 {
		return nil
	}
	return h
}

// wsExtractStringPair pulls (k, v) from a 2-tuple-like value.
func wsExtractStringPair(v any) (string, string) {
	if v == nil {
		return "", ""
	}
	if pair, ok := v.(SkyTuple2); ok {
		return fmt.Sprintf("%v", pair.V0), fmt.Sprintf("%v", pair.V1)
	}
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return "", ""
	}
	switch rv.Kind() {
	case reflect.Slice, reflect.Array:
		if rv.Len() >= 2 {
			return fmt.Sprintf("%v", rv.Index(0).Interface()),
				fmt.Sprintf("%v", rv.Index(1).Interface())
		}
	case reflect.Struct:
		// SkyTuple2-shaped struct (Tag/SkyName/Fields) OR plain {A,B}.
		fieldsF := rv.FieldByName("Fields")
		if fieldsF.IsValid() && fieldsF.Kind() == reflect.Slice && fieldsF.Len() >= 2 {
			return fmt.Sprintf("%v", fieldsF.Index(0).Interface()),
				fmt.Sprintf("%v", fieldsF.Index(1).Interface())
		}
		aF := rv.FieldByName("V0")
		bF := rv.FieldByName("V1")
		if aF.IsValid() && bF.IsValid() {
			return fmt.Sprintf("%v", aF.Interface()), fmt.Sprintf("%v", bF.Interface())
		}
	}
	return "", ""
}

// wsHeartbeat fires Ping at the given interval. Exits on socket close.
func wsHeartbeat(sh *wsHandle, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-sh.done:
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(sh.ctx, 10*time.Second)
			err := sh.conn.Ping(pingCtx)
			cancel()
			if err != nil {
				// Peer dead — close the connection so the reader
				// surfaces the error.
				sh.Close()
				return
			}
		}
	}
}

// WebSocket_send implements:
//
//	Sky.Core.WebSocket.send : Int -> String -> Task Error ()
//
// Sends a text frame. Blocks up to wsConsumerTimeout if the underlying
// write buffer is full (slow client). The Sky wrapper unwraps
// `WebSocket Int` to the inner int before calling.
func WebSocket_send(sidArg any, msgArg any) any {
	id := asInt64(sidArg)
	msg := fmt.Sprintf("%v", msgArg)
	return func() any {
		sess := currentLiveSession()
		sh := lookupWs(sess, id)
		if sh == nil || sh.IsClosed() {
			return Err[any, any](ErrUnavailable("websocket.send: socket closed"))
		}
		sh.writeMu.Lock()
		defer sh.writeMu.Unlock()
		writeCtx, cancel := context.WithTimeout(sh.ctx, wsConsumerTimeout)
		defer cancel()
		if err := sh.conn.Write(writeCtx, websocket.MessageText, []byte(msg)); err != nil {
			return Err[any, any](ErrNetwork("websocket.send: " + err.Error()))
		}
		return Ok[any, any](skyUnit())
	}
}

// WebSocket_sendBinary implements:
//
//	Sky.Core.WebSocket.sendBinary : Int -> String -> Task Error ()
//
// Sends a binary frame. The String carries raw bytes (Sky.Core.Bytes
// alias = String for v0.15.x).
func WebSocket_sendBinary(sidArg any, msgArg any) any {
	id := asInt64(sidArg)
	msg := fmt.Sprintf("%v", msgArg)
	return func() any {
		sess := currentLiveSession()
		sh := lookupWs(sess, id)
		if sh == nil || sh.IsClosed() {
			return Err[any, any](ErrUnavailable("websocket.sendBinary: socket closed"))
		}
		sh.writeMu.Lock()
		defer sh.writeMu.Unlock()
		writeCtx, cancel := context.WithTimeout(sh.ctx, wsConsumerTimeout)
		defer cancel()
		if err := sh.conn.Write(writeCtx, websocket.MessageBinary, []byte(msg)); err != nil {
			return Err[any, any](ErrNetwork("websocket.sendBinary: " + err.Error()))
		}
		return Ok[any, any](skyUnit())
	}
}

// WebSocket_close implements:
//
//	Sky.Core.WebSocket.close : Int -> Task Error ()
//
// Idempotent.
func WebSocket_close(sidArg any) any {
	id := asInt64(sidArg)
	return func() any {
		sess := currentLiveSession()
		sh := lookupWs(sess, id)
		if sh != nil {
			sh.Close()
			unregisterWs(sess, id)
		}
		return Ok[any, any](skyUnit())
	}
}

// WebSocket_closeWithCode implements:
//
//	Sky.Core.WebSocket.closeWithCode : Int -> String -> Int -> Task Error ()
//
// (Sky-side: `CloseCode -> String -> WebSocket -> Task Error ()`. The
// wrapper extracts the close code int from the CloseCode ADT and the
// inner Int from the WebSocket handle.)
func WebSocket_closeWithCode(codeArg, reasonArg, sidArg any) any {
	code := websocket.StatusNormalClosure
	if c := AsInt(codeArg); c > 0 {
		code = websocket.StatusCode(c)
	}
	reason := fmt.Sprintf("%v", reasonArg)
	id := asInt64(sidArg)
	return func() any {
		sess := currentLiveSession()
		sh := lookupWs(sess, id)
		if sh == nil {
			return Ok[any, any](skyUnit())
		}
		// Bespoke close path — DON'T reuse Close() because that sends
		// StatusNormalClosure. We want the user's chosen code.
		sh.closeOnce.Do(func() {
			sh.closed.Store(true)
			if sh.conn != nil {
				_ = sh.conn.Close(code, reason)
			}
			sh.cancel()
			close(sh.done)
		})
		unregisterWs(sess, id)
		return Ok[any, any](skyUnit())
	}
}

// ═════════════════════════════════════════════════════════════════════
// Sub wiring — drain wsHandle.ch, dispatch via the user's toMsg
// decoder.
// ═════════════════════════════════════════════════════════════════════

// wsSubReg is one live `WebSocket.onMessage` (or onOpen/onClose/onError)
// subscription on a session. Mirrors streamSubReg.
type wsSubReg struct {
	socketID int64
	kind     string // "message" | "open" | "close" | "error"
	toMsg    any
	cancel   func()
}

// Sub_subscribeWebSocket builds a Sub for incoming WS events. Sky-side
// surface:
//
//	WebSocket.onMessage : WebSocket -> (WebSocketMessage -> msg) -> Sub msg
//	WebSocket.onOpen    : WebSocket -> msg -> Sub msg
//	WebSocket.onClose   : WebSocket -> (CloseCode -> msg) -> Sub msg
//	WebSocket.onError   : WebSocket -> (Error -> msg) -> Sub msg
//
// All four route through this kernel; the `kind` string distinguishes
// which event the subscription wants delivered. The drain goroutine
// only invokes toMsg on matching events; non-matching are ignored by
// THIS subscription (but observed by its siblings).
func Sub_subscribeWebSocket(socketID, kindArg, toMsg any) SkySub {
	return subT{
		kind:     "subscribeWebSocket",
		socketID: asInt64(socketID),
		wsKind:   fmt.Sprintf("%v", kindArg),
		toMsg:    toMsg,
	}
}

// ═════════════════════════════════════════════════════════════════════
// ADT construction — WebSocketMessage + CloseCode
// ═════════════════════════════════════════════════════════════════════

// Sky-side ADTs:
//
//	type WebSocketMessage = Text String | Binary String
//	type CloseCode = Normal | GoingAway | UnsupportedData
//	               | InternalError | Custom Int

const (
	wsMessageTextTag    = 0
	wsMessageBinaryTag  = 1
	wsCloseCodeNormalTag          = 0
	wsCloseCodeGoingAwayTag       = 1
	wsCloseCodeUnsupportedTag     = 2
	wsCloseCodeInternalTag        = 3
	wsCloseCodeCustomTag          = 4
)

func buildWebSocketMessageValue(ev wsEvent) any {
	if ev.isBinary {
		return SkyADT{
			Tag:     wsMessageBinaryTag,
			SkyName: "Binary",
			Fields:  []any{ev.binary},
		}
	}
	return SkyADT{
		Tag:     wsMessageTextTag,
		SkyName: "Text",
		Fields:  []any{ev.text},
	}
}

func buildCloseCodeValue(code int) any {
	switch websocket.StatusCode(code) {
	case websocket.StatusNormalClosure:
		return SkyADT{Tag: wsCloseCodeNormalTag, SkyName: "Normal"}
	case websocket.StatusGoingAway:
		return SkyADT{Tag: wsCloseCodeGoingAwayTag, SkyName: "GoingAway"}
	case websocket.StatusUnsupportedData:
		return SkyADT{Tag: wsCloseCodeUnsupportedTag, SkyName: "UnsupportedData"}
	case websocket.StatusInternalError:
		return SkyADT{Tag: wsCloseCodeInternalTag, SkyName: "InternalError"}
	default:
		return SkyADT{
			Tag:     wsCloseCodeCustomTag,
			SkyName: "Custom",
			Fields:  []any{code},
		}
	}
}

// ═════════════════════════════════════════════════════════════════════
// Subscription diff + drain goroutine wiring
// ═════════════════════════════════════════════════════════════════════

// applyWsSubsDiff is the WebSocket counterpart of applyStreamSubsDiff.
// Diff-mode: open drain goroutines for newly-added (socketID, kind)
// pairs, cancel removed ones, leave the intersection untouched.
//
// Multiple subs (onMessage + onClose + onError, say) on the SAME
// socket share ONE underlying read goroutine — `runReader`. Each
// registered sub gets its own dispatch goroutine that fans events
// from a per-sub channel. The shared model is the socket; the per-
// sub channel decouples a slow decoder from siblings.
//
// Implementation simplification: each sub gets its own drain
// goroutine that pulls from the SHARED sh.ch using a non-destructive
// peek pattern via a small router goroutine. To keep the model
// simple, we use ONE drain goroutine per socket that's started the
// first time ANY sub is registered for the socket and exits when
// the LAST sub for the socket is removed (OR the socket closes).
// The drain goroutine consults activeWsSubs at dispatch time.
func (app *liveApp) applyWsSubsDiff(sess *liveSession, desired map[string]subT) {
	sess.activeWsSubsMu.Lock()
	if sess.activeWsSubs == nil && len(desired) > 0 {
		sess.activeWsSubs = make(map[string]*wsSubReg, len(desired))
	}
	old := sess.activeWsSubs

	// Compute added / removed by key.
	added := make([]string, 0)
	removed := make([]string, 0)
	for k := range desired {
		if _, ok := old[k]; !ok {
			added = append(added, k)
		}
	}
	for k := range old {
		if _, ok := desired[k]; !ok {
			removed = append(removed, k)
		}
	}

	removedRegs := make([]*wsSubReg, 0, len(removed))
	for _, k := range removed {
		if reg := old[k]; reg != nil {
			removedRegs = append(removedRegs, reg)
			delete(sess.activeWsSubs, k)
		}
	}

	// Track which socket IDs need a fresh drain goroutine spawned.
	// We only spawn one drain per socket — even if 4 subs land in
	// the same dispatch. The check: was there ANY reg for this
	// socketID in `old` BEFORE this diff? If not, spawn now.
	socketsThatNeedDrain := make(map[int64]bool)

	// First, snapshot which sockets already had a drain (any reg in
	// old AFTER we removed the removed ones).
	socketsAlreadyDraining := make(map[int64]bool)
	for _, reg := range sess.activeWsSubs {
		if reg != nil {
			socketsAlreadyDraining[reg.socketID] = true
		}
	}

	for _, k := range added {
		leaf := desired[k]
		// Sanity-check the socket exists.
		sh := lookupWs(sess, leaf.socketID)
		if sh == nil {
			continue
		}
		reg := &wsSubReg{
			socketID: leaf.socketID,
			kind:     leaf.wsKind,
			toMsg:    leaf.toMsg,
			cancel:   func() {}, // populated below if this triggers a drain spawn
		}
		sess.activeWsSubs[k] = reg
		if !socketsAlreadyDraining[leaf.socketID] {
			socketsThatNeedDrain[leaf.socketID] = true
			socketsAlreadyDraining[leaf.socketID] = true
		}
	}
	sess.activeWsSubsMu.Unlock()

	for _, reg := range removedRegs {
		if reg.cancel != nil {
			reg.cancel()
		}
	}

	// Spawn drain goroutines AFTER releasing the registry lock.
	for socketID := range socketsThatNeedDrain {
		sh := lookupWs(sess, socketID)
		if sh == nil {
			continue
		}
		parentCtx := CurrentTraceContext()
		go app.runWsSubscriberLoop(sess, sh, parentCtx)
	}
}

// runWsSubscriberLoop drains one socket's ch + fans events to every
// currently-registered sub for that socket. Exits when (a) the socket
// closes OR (b) no subs remain for the socket.
func (app *liveApp) runWsSubscriberLoop(sess *liveSession, sh *wsHandle, parentCtx context.Context) {
	sessDone := sess.done
	RunWithTraceContext(parentCtx, func() {
		runWithLiveSession(sess, func() {
			for {
				select {
				case <-sessDone:
					return
				case <-sh.done:
					// Drain any remaining events queued on sh.ch before exit.
					for {
						select {
						case ev := <-sh.ch:
							app.dispatchWsEvent(sess, sh.id, ev)
						default:
							return
						}
					}
				case ev, open := <-sh.ch:
					if !open {
						return
					}
					app.dispatchWsEvent(sess, sh.id, ev)
				}
				// After each event, check whether any subs remain
				// for this socket. If not, retire — a subsequent
				// applyWsSubsDiff will respawn us if needed.
				if !app.socketHasAnyWsSub(sess, sh.id) {
					return
				}
			}
		})
	})
}

// socketHasAnyWsSub reports whether the session still has any active
// WebSocket sub for the given socket id.
func (app *liveApp) socketHasAnyWsSub(sess *liveSession, socketID int64) bool {
	sess.activeWsSubsMu.Lock()
	defer sess.activeWsSubsMu.Unlock()
	for _, reg := range sess.activeWsSubs {
		if reg != nil && reg.socketID == socketID {
			return true
		}
	}
	return false
}

// dispatchWsEvent fans one wsEvent to every matching sub on the session.
// Matching: open → "open"; message → "message"; close → "close";
// error → "error". A sub whose toMsg lambda panics is logged + skipped
// (event still delivered to siblings).
func (app *liveApp) dispatchWsEvent(sess *liveSession, socketID int64, ev wsEvent) {
	wantKind := wsEventKindToSubKind(ev.kind)
	// Snapshot the relevant regs under lock; invoke decoders outside.
	sess.activeWsSubsMu.Lock()
	regs := make([]*wsSubReg, 0)
	for _, reg := range sess.activeWsSubs {
		if reg != nil && reg.socketID == socketID && reg.kind == wantKind {
			regs = append(regs, reg)
		}
	}
	sess.activeWsSubsMu.Unlock()
	for _, reg := range regs {
		app.dispatchOneWsSub(sess, reg, ev)
	}
}

// wsEventKindToSubKind maps the runtime event kind to the Sky-side
// subscription kind label.
func wsEventKindToSubKind(k wsEventKind) string {
	switch k {
	case wsOpenEv:
		return "open"
	case wsMessageEv:
		return "message"
	case wsCloseEv:
		return "close"
	case wsErrorEv:
		return "error"
	}
	return ""
}

// dispatchOneWsSub decodes one event for one sub and routes through
// app.dispatch (the same path Cmd.perform completions take).
func (app *liveApp) dispatchOneWsSub(sess *liveSession, reg *wsSubReg, ev wsEvent) {
	var arg any
	switch ev.kind {
	case wsOpenEv:
		// onOpen's toMsg is a plain Msg value (not a function — Sky-side
		// surface: `onOpen sock msg`). We pass through as-is and let
		// sky_call's isFunc check route it.
		arg = nil
	case wsMessageEv:
		arg = buildWebSocketMessageValue(ev)
	case wsCloseEv:
		arg = buildCloseCodeValue(ev.closeCode)
	case wsErrorEv:
		arg = ev.err
	}
	var msg any
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr,
					"[sky.websocket] decoder panic, dropping event kind=%d: %v\n%s\n",
					ev.kind, r, debug.Stack())
				msg = nil
			}
		}()
		if !isFunc(reg.toMsg) {
			// onOpen — the toMsg IS the Msg.
			msg = reg.toMsg
		} else {
			msg = sky_call(reg.toMsg, arg)
		}
	}()
	if msg == nil {
		return
	}

	sess.mu.Lock()
	prevShipped := sess.lastShippedBody
	prevTreeBeforeDispatch := sess.prevTree
	body := app.dispatch(sess, msg)
	newTreeAfterDispatch := sess.prevTree
	var snap frameSnapshot
	var patches []Patch
	var haveFrame bool
	if body != "" && body != prevShipped {
		snap = sess.prepareFrameSnapshot(body)
		sess.lastShippedBody = body
		if prevTreeBeforeDispatch != nil && newTreeAfterDispatch != nil {
			patches = diffTrees(prevTreeBeforeDispatch, newTreeAfterDispatch, nil)
		}
		haveFrame = true
	}
	sess.mu.Unlock()
	if !haveFrame {
		return
	}
	frame := chooseSSEFrame(snap, prevTreeBeforeDispatch, patches)
	select {
	case sess.sseCh <- frame:
	default:
		recordSseDrop(sess.sid)
	}
}

// reservedToAvoidUnusedImport — silence the linter for io+strings if
// they end up unreferenced after refactors.
var _ = io.EOF
var _ = strings.Builder{}
