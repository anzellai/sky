package rt

import "fmt"

// websocket_core.go — the Sky.Core.WebSocket values both clients share: the
// native client (websocket.go, websocket_task.go) and the Sky.Spa wasm client
// over the browser WebSocket (websocket_wasm.go). No build tag: the frame and
// close-code ADTs, the event kinds and the Task receive outcome are the same on
// both, so a Sky program reads a socket the same way in either.

// The WebSocket close codes the CloseCode ADT names (RFC 6455 §7.4.1).
const (
	wsStatusNormal          = 1000
	wsStatusGoingAway       = 1001
	wsStatusUnsupportedData = 1003
	wsStatusInternalError   = 1011
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
	kind        wsEventKind
	text        string // wsMessageEv text — UTF-8 frame
	binary      string // wsMessageEv binary — byte string (Sky.Core.Bytes alias)
	isBinary    bool   // distinguishes text vs binary message
	closeCode   int    // wsCloseEv — WebSocket close code
	closeReason string // wsCloseEv — close reason string
	err         any    // wsErrorEv — Sky-shaped Error ADT
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
	wsMessageTextTag          = 0
	wsMessageBinaryTag        = 1
	wsCloseCodeNormalTag      = 0
	wsCloseCodeGoingAwayTag   = 1
	wsCloseCodeUnsupportedTag = 2
	wsCloseCodeInternalTag    = 3
	wsCloseCodeCustomTag      = 4
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
	switch code {
	case wsStatusNormal:
		return SkyADT{Tag: wsCloseCodeNormalTag, SkyName: "Normal"}
	case wsStatusGoingAway:
		return SkyADT{Tag: wsCloseCodeGoingAwayTag, SkyName: "GoingAway"}
	case wsStatusUnsupportedData:
		return SkyADT{Tag: wsCloseCodeUnsupportedTag, SkyName: "UnsupportedData"}
	case wsStatusInternalError:
		return SkyADT{Tag: wsCloseCodeInternalTag, SkyName: "InternalError"}
	default:
		return SkyADT{
			Tag:     wsCloseCodeCustomTag,
			SkyName: "Custom",
			Fields:  []any{code},
		}
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

// Socket owners: the one consumer that reads a socket's frames, claimed by the
// first Sub or Task that reads it.
const (
	wsOwnerNone int32 = iota
	wsOwnerSub
	wsOwnerTask
)

const wsSubOwnedMsg = ": a Sub (WebSocket.onMessage / onOpen / onClose / onError) reads this socket. " +
	"A socket has one reader: use the Sub or receive, not both."

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
