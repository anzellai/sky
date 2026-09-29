//go:build js

package rt

import (
	"fmt"
	"syscall/js"
	"time"
)

// websocket_wasm.go — Sky.Core.WebSocket in the Sky.Spa wasm client, over the
// browser WebSocket API. The native client (websocket.go) dials with
// coder/websocket; a browser page can only use `new WebSocket(url)`, so this
// file maps the same kernels onto it:
//
//	connect / connectWith      → new WebSocket(url), the Task resolves on open
//	send / sendBinary          → ws.send(text) / ws.send(Uint8Array)
//	close / closeWithCode      → ws.close(code, reason)
//	receive / receiveWithin /
//	forEachMessage             → the socket's queue, from a Task (websocket_peer.go)
//	onOpen / onMessage /
//	onClose / onError          → Subs, reconciled after every update (live_wasm.go)
//
// A path URL (`WebSocket.connect "/ws"`) connects to the page's own origin,
// `ws:` or `wss:` after the page's scheme, so the default passes a strict
// `connect-src 'self'` (SKY_CSP=strict). The backend serves the socket with
// the existing server API (`Sky.Http.Server.WebSocket.upgrade`, mounted with
// `App.api`), whose origin check admits the page's own origin.
//
// What a browser socket cannot do, and says so rather than ignoring it:
// request headers (`withHeaders`) are refused at connect (the browser API has
// no way to send them; use a query parameter or the session cookie, which the
// browser sends itself); a close code other than 1000 or 3000-4999 is refused
// by the browser, so closeWithCode returns `Err InvalidInput` for it. The
// browser answers pings itself, so `withPingInterval` has no effect.

// wsJs is one browser socket.
type wsJs struct {
	id    int64
	ws    js.Value
	peer  *wsPeer
	funcs []js.Func
}

var (
	wsJsSockets = map[int64]*wsJs{}
	wsJsNextID  int64
	// wsJsRefused holds the sockets whose refused Sub was already reported.
	wsJsRefused = map[int64]bool{}
)

func wsJsLookup(id int64) *wsJs { return wsJsSockets[id] }

// wsJsDispatch routes a Sub's Msg into the client's TEA loop.
func wsJsDispatch(msg any) {
	if spaDispatch != nil {
		spaDispatch(msg)
	}
}

// WebSocket_connect implements `Sky.Core.WebSocket.connect : String -> Task Error Int`.
func WebSocket_connect(urlArg any) any {
	u := fmt.Sprintf("%v", urlArg)
	return func() any { return wsJsConnect(u, 30*time.Second) }
}

// WebSocket_connectWith implements the typed-record form. Request headers are
// refused: the browser WebSocket API cannot send them.
func WebSocket_connectWith(cfgArg any) any {
	u := fmt.Sprintf("%v", recordField(cfgArg, "Url", "url"))
	timeout := 30 * time.Second
	if t := AsInt(recordField(cfgArg, "Timeout", "timeout")); t > 0 {
		timeout = time.Duration(t) * time.Millisecond
	}
	headers := AsList(recordField(cfgArg, "Headers", "headers"))
	return func() any {
		if len(headers) > 0 {
			return Err[any, any](ErrInvalidInput("websocket.connectWith: a browser WebSocket cannot send request headers; " +
				"authenticate with the session cookie (the browser sends it) or a query parameter"))
		}
		return wsJsConnect(u, timeout)
	}
}

// wsJsConnect opens the socket and waits for the handshake (or its failure,
// or the timeout). It runs on the Task's goroutine, so the wait yields to the
// browser event loop that delivers the socket's events.
func wsJsConnect(raw string, timeout time.Duration) (result any) {
	href := js.Global().Get("location").Get("href")
	page := ""
	if href.Type() == js.TypeString {
		page = href.String()
	}
	target, err := wsBrowserURL(raw, page)
	if err != nil {
		return Err[any, any](ErrInvalidInput("websocket.connect: " + err.Error()))
	}
	var ws js.Value
	func() {
		defer func() {
			if r := recover(); r != nil {
				result = Err[any, any](ErrInvalidInput(fmt.Sprintf("websocket.connect %s: %v", target, r)))
			}
		}()
		ws = js.Global().Get("WebSocket").New(target)
	}()
	if result != nil {
		return result
	}
	ws.Set("binaryType", "arraybuffer")
	wsJsNextID++
	s := &wsJs{id: wsJsNextID, ws: ws, peer: newWsPeer()}
	opened := make(chan bool, 1)
	settled := false
	settle := func(ok bool) {
		if !settled {
			settled = true
			opened <- ok
		}
	}
	onOpen := js.FuncOf(func(this js.Value, args []js.Value) any {
		// The handshake completed: the Open event is the first one a reader
		// sees (an onOpen Sub dispatches it), as on the native client.
		s.peer.push(wsEvent{kind: wsOpenEv}, wsJsDispatch)
		settle(true)
		return nil
	})
	onMessage := js.FuncOf(func(this js.Value, args []js.Value) any {
		if len(args) == 0 {
			return nil
		}
		data := args[0].Get("data")
		ev := wsEvent{kind: wsMessageEv}
		if data.Type() == js.TypeString {
			ev.text = data.String()
		} else {
			u8 := js.Global().Get("Uint8Array").New(data)
			buf := make([]byte, u8.Get("length").Int())
			js.CopyBytesToGo(buf, u8)
			ev.isBinary = true
			ev.binary = string(buf)
		}
		s.peer.push(ev, wsJsDispatch)
		return nil
	})
	onClose := js.FuncOf(func(this js.Value, args []js.Value) any {
		code := 1006
		if len(args) > 0 {
			if c := args[0].Get("code"); c.Type() == js.TypeNumber {
				code = c.Int()
			}
		}
		if !settled {
			// Closed before it opened: the connection failed.
			settle(false)
			s.release()
			return nil
		}
		if code == 1006 {
			// No close frame: the connection was lost (the browser reports
			// no more than that).
			s.peer.push(wsEvent{kind: wsErrorEv, err: ErrNetwork("websocket: the connection was lost (code 1006)")}, wsJsDispatch)
		} else {
			s.peer.push(wsEvent{kind: wsCloseEv, closeCode: code}, wsJsDispatch)
		}
		s.peer.finish()
		delete(wsJsSockets, s.id)
		s.release()
		return nil
	})
	s.funcs = []js.Func{onOpen, onMessage, onClose}
	ws.Call("addEventListener", "open", onOpen)
	ws.Call("addEventListener", "message", onMessage)
	ws.Call("addEventListener", "close", onClose)
	wsJsSockets[s.id] = s

	var expired <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		expired = t.C
	}
	select {
	case ok := <-opened:
		if ok {
			return Ok[any, any](s.id)
		}
		delete(wsJsSockets, s.id)
		return Err[any, any](ErrNetwork("websocket.connect " + target + ": the connection failed"))
	case <-expired:
		delete(wsJsSockets, s.id)
		// The close event that follows releases the callbacks.
		settled = true
		ws.Call("close")
		return Err[any, any](ErrTimeout())
	}
}

// release removes the socket's JS callbacks (after its last event).
func (s *wsJs) release() {
	for _, f := range s.funcs {
		f.Release()
	}
	s.funcs = nil
}

// wsJsOpen reports whether the socket is open for sending.
func (s *wsJs) wsJsOpen() bool {
	return s.ws.Get("readyState").Int() == 1
}

// WebSocket_send implements `send : Int -> String -> Task Error ()`.
func WebSocket_send(sidArg any, msgArg any) any {
	id := asInt64(sidArg)
	msg := fmt.Sprintf("%v", msgArg)
	return func() any {
		s := wsJsLookup(id)
		if s == nil || !s.wsJsOpen() {
			return Err[any, any](ErrUnavailable("websocket.send: socket closed"))
		}
		s.ws.Call("send", msg)
		return Ok[any, any](struct{}{})
	}
}

// WebSocket_sendBinary implements `sendBinary : Int -> String -> Task Error ()`
// (the String carries raw bytes).
func WebSocket_sendBinary(sidArg any, msgArg any) any {
	id := asInt64(sidArg)
	msg := fmt.Sprintf("%v", msgArg)
	return func() any {
		s := wsJsLookup(id)
		if s == nil || !s.wsJsOpen() {
			return Err[any, any](ErrUnavailable("websocket.sendBinary: socket closed"))
		}
		u8 := js.Global().Get("Uint8Array").New(len(msg))
		js.CopyBytesToJS(u8, []byte(msg))
		s.ws.Call("send", u8)
		return Ok[any, any](struct{}{})
	}
}

// WebSocket_close implements `close : Int -> Task Error ()`. Idempotent.
func WebSocket_close(sidArg any) any {
	id := asInt64(sidArg)
	return func() any {
		if s := wsJsLookup(id); s != nil {
			delete(wsJsSockets, id)
			s.ws.Call("close", wsStatusNormal, "client closed")
		}
		return Ok[any, any](struct{}{})
	}
}

// WebSocket_closeWithCode implements `closeWithCode : Int -> String -> Int ->
// Task Error ()`. A browser accepts 1000 and 3000-4999 only.
func WebSocket_closeWithCode(codeArg, reasonArg, sidArg any) any {
	code := AsInt(codeArg)
	reason := fmt.Sprintf("%v", reasonArg)
	id := asInt64(sidArg)
	return func() any {
		if !wsBrowserCloseCodeOK(code) {
			return Err[any, any](ErrInvalidInput(fmt.Sprintf(
				"websocket.closeWithCode: a browser WebSocket may close with Normal or a Custom code 3000-4999, not %d", code)))
		}
		if s := wsJsLookup(id); s != nil {
			delete(wsJsSockets, id)
			s.ws.Call("close", code, reason)
		}
		return Ok[any, any](struct{}{})
	}
}

// wsJsReceive backs receive and receiveWithin.
func wsJsReceive(id int64, hasLimit bool, limit time.Duration, op string) any {
	s := wsJsLookup(id)
	if s == nil {
		return Ok[any, any](Nothing[any]())
	}
	if !s.peer.claim(wsOwnerTask) {
		return Err[any, any](ErrInvalidInput(op + wsSubOwnedMsg))
	}
	switch out, v := s.peer.next(hasLimit, limit); out {
	case wsTaskFrame:
		return Ok[any, any](Just[any](v))
	case wsTaskFailed:
		return Err[any, any](v)
	case wsTaskTimeout:
		return Err[any, any](ErrTimeout())
	default:
		return Ok[any, any](Nothing[any]())
	}
}

// WebSocket_receive implements `receive : Int -> Task Error (Maybe WebSocketMessage)`.
func WebSocket_receive(sidArg any) any {
	id := asInt64(sidArg)
	return func() any { return wsJsReceive(id, false, 0, "websocket.receive") }
}

// WebSocket_receiveWithin implements `receiveWithin : Int -> Int -> Task Error
// (Maybe WebSocketMessage)` (the first Int is a timeout in milliseconds).
func WebSocket_receiveWithin(msArg any, sidArg any) any {
	limit := time.Duration(AsInt(msArg)) * time.Millisecond
	id := asInt64(sidArg)
	return func() any { return wsJsReceive(id, true, limit, "websocket.receiveWithin") }
}

// WebSocket_forEachMessage implements `forEachMessage : Int -> (WebSocketMessage
// -> Task Error ()) -> Task Error ()`: body runs on each frame, in order, until
// the socket closes (Ok) or fails (Err); a body Err stops at once. The socket is
// closed on every exit.
func WebSocket_forEachMessage(sidArg any, body any) any {
	id := asInt64(sidArg)
	return func() any {
		s := wsJsLookup(id)
		if s == nil {
			return Ok[any, any](struct{}{})
		}
		if !s.peer.claim(wsOwnerTask) {
			return Err[any, any](ErrInvalidInput("websocket.forEachMessage" + wsSubOwnedMsg))
		}
		defer func() {
			if wsJsLookup(id) != nil {
				delete(wsJsSockets, id)
				s.ws.Call("close", wsStatusNormal, "client closed")
			}
		}()
		for {
			out, v := s.peer.next(false, 0)
			switch out {
			case wsTaskFrame:
				res := anyTaskInvoke(SkyCall(body, v))
				if res.Tag != 0 {
					return Err[any, any](res.ErrValue)
				}
			case wsTaskFailed:
				return Err[any, any](v)
			default:
				return Ok[any, any](struct{}{})
			}
		}
	}
}

// wsReconcileSubs installs the WebSocket Subs of the current subscriptions on
// their sockets (called after every update, from reconcileSubs). A socket a
// Task reads keeps its Task; the refused Sub is reported once.
func wsReconcileSubs(root subT) {
	desired := map[int64]map[string]any{}
	collectWebSocketSubs(root, desired)
	for id, s := range wsJsSockets {
		subs := desired[id]
		if !s.peer.setSubs(subs, wsJsDispatch) && !wsJsRefused[id] {
			wsJsRefused[id] = true
			if c := js.Global().Get("console"); c.Truthy() {
				c.Call("warn", fmt.Sprintf("[sky.websocket] Sub on socket %d ignored: a Task reads this socket "+
					"(WebSocket.receive / forEachMessage). A socket has one reader.", id))
			}
		}
	}
}
