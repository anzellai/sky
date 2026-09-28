//go:build !js

// websocket_task.go — Task-based receive for Sky.Core.WebSocket (v0.27).
//
// The Sub surface (onMessage & co., websocket.go) only runs inside a Sky.Live
// update loop. A Task program — a CLI, a worker, a bridge between two sockets —
// had no way to read a frame: the frames went into the socket's 64-slot queue
// and nothing drained it. These kernels read the same queue from a Task:
//
//	receive        : WebSocket -> Task Error (Maybe WebSocketMessage)
//	receiveWithin  : Int -> WebSocket -> Task Error (Maybe WebSocketMessage)
//	forEachMessage : WebSocket -> (WebSocketMessage -> Task Error ()) -> Task Error ()
//
// Semantics:
//
//   - `Just msg` for each frame, in order. The Open event is skipped.
//   - `Nothing` on a clean close, and on every call after it (including after
//     WebSocket.close, which unregisters the id).
//   - `Err` (Network) on a read failure. The socket is then closed, so the
//     next call returns Nothing.
//   - receiveWithin: `Err Timeout` when no frame arrives in time. The timeout
//     consumes no frame and leaves the socket open. A non-positive timeout
//     polls: it returns a frame already queued, else Err Timeout at once.
//   - A receive that is waiting when the socket closes wakes, returns what is
//     still queued, then Nothing.
//   - Concurrent receivers share the queue; each frame goes to one of them.
//
// Ownership. A socket has one reader, claimed by the first consumer
// (wsHandle.claim). A Task receive on a socket a Sub drains returns
// Err InvalidInput; a Sub on a socket a Task reads is refused and logged
// (applyWsSubsDiff). Two readers on one queue would each see an arbitrary
// subset of the frames, which is never what the program meant.
//
// Backpressure. Once a Task owns the socket the reader goroutine never gives
// up on a full queue (deliver in websocket.go): it waits, stops calling
// conn.Read, and TCP slows the peer. The parked reader counts as alive for the
// heartbeat (no ping while parked, because the pong could not be read) and for
// the sessionless reaper.

package rt

import "time"

// wsTaskOutcome is what one receive step produced.
type wsTaskOutcome int

const (
	wsTaskFrame   wsTaskOutcome = iota // a message frame (value = WebSocketMessage)
	wsTaskClosed                       // clean close / socket gone — Nothing
	wsTaskFailed                       // read error (value = Sky Error)
	wsTaskTimeout                      // receiveWithin expired
)

// wsTaskEvent maps one queued event to an outcome. Open events are skipped
// (ok = false: keep waiting).
func wsTaskEvent(ev wsEvent) (wsTaskOutcome, any, bool) {
	switch ev.kind {
	case wsMessageEv:
		return wsTaskFrame, buildWebSocketMessageValue(ev), true
	case wsCloseEv:
		return wsTaskClosed, nil, true
	case wsErrorEv:
		return wsTaskFailed, ev.err, true
	}
	return 0, nil, false
}

// wsTaskNext waits for the next frame on sh. hasLimit selects receiveWithin
// semantics (limit <= 0 polls).
func wsTaskNext(sh *wsHandle, hasLimit bool, limit time.Duration) (wsTaskOutcome, any) {
	var expired <-chan time.Time
	if hasLimit {
		if limit <= 0 {
			// Poll: take what is queued, never wait.
			for {
				select {
				case ev := <-sh.ch:
					if out, v, ok := wsTaskEvent(ev); ok {
						return out, v
					}
				default:
					select {
					case <-sh.done:
						return wsTaskClosed, nil
					default:
						return wsTaskTimeout, nil
					}
				}
			}
		}
		t := time.NewTimer(limit)
		defer t.Stop()
		expired = t.C
	}
	for {
		select {
		case ev := <-sh.ch:
			if out, v, ok := wsTaskEvent(ev); ok {
				return out, v
			}
		case <-sh.done:
			// Closed: hand out what is still queued, then Nothing.
			for {
				select {
				case ev := <-sh.ch:
					if out, v, ok := wsTaskEvent(ev); ok {
						return out, v
					}
				default:
					return wsTaskClosed, nil
				}
			}
		case <-expired:
			return wsTaskTimeout, nil
		}
	}
}

const wsSubOwnedMsg = ": a Sub (WebSocket.onMessage / onOpen / onClose / onError) reads this socket. " +
	"A socket has one reader: use the Sub or receive, not both."

// wsTaskReceive backs receive and receiveWithin.
func wsTaskReceive(id int64, hasLimit bool, limit time.Duration, op string) any {
	sh := lookupWs(currentLiveSession(), id)
	if sh == nil {
		// Unknown or already closed and unregistered: the stream is over.
		return Ok[any, any](Nothing[any]())
	}
	if !sh.claim(wsOwnerTask) {
		return Err[any, any](ErrInvalidInput(op + wsSubOwnedMsg))
	}
	switch out, v := wsTaskNext(sh, hasLimit, limit); out {
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

// WebSocket_receive implements:
//
//	Sky.Core.WebSocket.receive : WebSocket -> Task Error (Maybe WebSocketMessage)
//
// (The Sky wrapper passes the inner Int.)
func WebSocket_receive(sidArg any) any {
	id := asInt64(sidArg)
	return func() any {
		return wsTaskReceive(id, false, 0, "websocket.receive")
	}
}

// WebSocket_receiveWithin implements:
//
//	Sky.Core.WebSocket.receiveWithin : Int -> WebSocket -> Task Error (Maybe WebSocketMessage)
//
// The Int is a timeout in milliseconds.
func WebSocket_receiveWithin(msArg any, sidArg any) any {
	limit := time.Duration(AsInt(msArg)) * time.Millisecond
	id := asInt64(sidArg)
	return func() any {
		return wsTaskReceive(id, true, limit, "websocket.receiveWithin")
	}
}

// WebSocket_forEachMessage implements:
//
//	Sky.Core.WebSocket.forEachMessage
//	    : WebSocket -> (WebSocketMessage -> Task Error ()) -> Task Error ()
//
// Runs `body` on each frame, in order, on the calling goroutine, until the
// socket closes (Ok ()) or fails (Err). A body Err stops at once and is
// returned. On every exit the socket is closed and unregistered, the same
// contract as Http.Stream.forEachChunk.
func WebSocket_forEachMessage(sidArg any, body any) any {
	id := asInt64(sidArg)
	return func() any {
		sess := currentLiveSession()
		sh := lookupWs(sess, id)
		if sh == nil {
			return Ok[any, any](skyUnit())
		}
		if !sh.claim(wsOwnerTask) {
			return Err[any, any](ErrInvalidInput("websocket.forEachMessage" + wsSubOwnedMsg))
		}
		defer func() {
			sh.Close()
			unregisterWs(sess, id)
			sessionlessSockets.Delete(id)
		}()
		for {
			out, v := wsTaskNext(sh, false, 0)
			switch out {
			case wsTaskFrame:
				res := anyTaskInvoke(SkyCall(body, v))
				if res.Tag != 0 {
					return Err[any, any](res.ErrValue)
				}
			case wsTaskFailed:
				return Err[any, any](v)
			default:
				return Ok[any, any](skyUnit())
			}
		}
	}
}
