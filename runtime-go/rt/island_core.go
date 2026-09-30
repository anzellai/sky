package rt

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
)

// Widget islands — a third-party JS widget (a code editor, a canvas painter)
// inside a Sky view, with typed messages both ways.
//
// An island is an element carrying three attributes the Sky builders set
// (Std.Ui.island / Std.Html.island):
//
//	data-sky-island     the widget name, registered by window.Sky.island
//	data-sky-island-id  the instance id, unique on the page
//	data-sky-props      the props, JSON text
//
// The widget owns everything under the element. The shared diff (diffNodes,
// used by Sky.Live and Sky.Spa alike) patches only the island's own
// attributes while its identity (name + id) holds, never its children, and
// replaces the element when the identity changes, so the client remounts it.
// The server renders no children for an island (renderVNodeInto), so the
// widget always mounts into an empty element.
//
// Events: the widget calls send(type, data); the island runtime
// (island_client.go) dispatches a CustomEvent "skyisland-<type>" on the
// element, and the client sends the detail as JSON TEXT. Std.Html.Events.
// onIslandEvent binds that event to a handler `String -> Result Error msg`
// (the Sky decoder, then toMsg). HtmlToVNode wraps such a handler in
// islandEventHandler, so every dispatch path (the Live event POST, a batched
// beacon, the Spa wasm listener) reads a Result: Ok is the Msg, and an Err (or
// a panic) is logged and the event dropped, never a crash.
//
// Commands: Cmd.toIsland id name payload is a cmdT of kind "island". Sky.Live
// pushes it to the session's tabs as the SSE event "island"; the Sky.Spa wasm
// client hands it straight to the island runtime. A terminal target has no
// widget, so there it does nothing.

const (
	islandNameAttr  = "data-sky-island"
	islandIDAttr    = "data-sky-island-id"
	islandPropsAttr = "data-sky-props"
	// islandEventPrefix starts every island event name (the DOM event type and
	// the Sky event key). Std.Html.Events.onIslandEvent adds it.
	islandEventPrefix = "skyisland-"
	// islandDecodeClass is the log class of a dropped island event.
	islandDecodeClass = "IslandEventDecode"
)

// isIsland reports whether n is an island element.
func isIsland(n *VNode) bool {
	return n != nil && n.Kind == "element" && n.Attrs[islandNameAttr] != ""
}

// islandIdentity is an island's identity (name + id), or "" for any other
// node. Two renders of the same island keep the widget; a different identity
// in the same slot remounts it.
func islandIdentity(n *VNode) string {
	if !isIsland(n) {
		return ""
	}
	return n.Attrs[islandNameAttr] + "\x00" + n.Attrs[islandIDAttr]
}

// islandEventHandler is the handler of an island event: fn takes the event
// detail as JSON text and returns `Result Error msg`.
type islandEventHandler struct {
	fn any
}

// wrapIslandHandler wraps the handler of an island event (by its name) so the
// dispatch paths read its Result. Any other handler is returned unchanged.
func wrapIslandHandler(event string, handler any) any {
	if !strings.HasPrefix(event, islandEventPrefix) {
		return handler
	}
	if h, ok := handler.(islandEventHandler); ok {
		return h
	}
	return islandEventHandler{fn: handler}
}

// islandLogDrop reports a dropped island event (a var so a test can observe
// it).
var islandLogDrop = islandLogDropLimited

// islandDropLogMax is how many dropped-event lines one window may log. A
// client can send malformed island events in a loop, and each used to write
// one warn line (D-8): the log volume was the client's to choose.
const (
	islandDropLogMax    = 20
	islandDropLogWindow = time.Minute
)

var islandDropLog struct {
	mu         sync.Mutex
	windowFrom time.Time
	logged     int
	suppressed int
}

// islandLogDropLimited logs a dropped island event, at most islandDropLogMax
// lines a minute. The first line of the next window says how many were not
// logged, so the count is never lost.
func islandLogDropLimited(reason string) {
	now := time.Now()
	islandDropLog.mu.Lock()
	if now.Sub(islandDropLog.windowFrom) >= islandDropLogWindow {
		if islandDropLog.suppressed > 0 {
			n := islandDropLog.suppressed
			defer logEmit(logLevelWarn, "warn",
				fmt.Sprintf("sky.island: %d more widget events were dropped and not logged in the last minute", n),
				map[string]any{"class": islandDecodeClass, "suppressed": n})
		}
		islandDropLog.windowFrom, islandDropLog.logged, islandDropLog.suppressed = now, 0, 0
	}
	if islandDropLog.logged >= islandDropLogMax {
		islandDropLog.suppressed++
		islandDropLog.mu.Unlock()
		return
	}
	islandDropLog.logged++
	islandDropLog.mu.Unlock()
	logEmit(logLevelWarn, "warn",
		"sky.island: a widget event was dropped: "+reason,
		map[string]any{"class": islandDecodeClass})
}

// decode runs the handler on the event detail (JSON text). ok is false when
// the decoder failed or the handler panicked; the drop is logged.
func (h islandEventHandler) decode(detail string) (msg any, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			islandLogDrop(fmt.Sprintf("the handler panicked: %v", r))
			msg, ok = nil, false
		}
	}()
	res := sky_call(h.fn, detail)
	tag, okV, errV, isResult := islandResultParts(res)
	if !isResult {
		islandLogDrop(fmt.Sprintf("the handler returned %T, not a Result", res))
		return nil, false
	}
	if tag != 0 {
		islandLogDrop("the payload did not decode: " + Basics_errorToStringT(errV))
		return nil, false
	}
	return okV, true
}

// applyWire decodes the wire args of an island event: exactly one JSON string
// (the detail as JSON text). Anything else is dropped as msgDecodeError.
func (h islandEventHandler) applyWire(args []json.RawMessage) any {
	if len(args) == 0 {
		islandLogDrop("the event carried no payload")
		return msgDecodeError{}
	}
	var detail string
	if err := json.Unmarshal(args[0], &detail); err != nil {
		islandLogDrop("the payload is not JSON text")
		return msgDecodeError{}
	}
	msg, ok := h.decode(detail)
	if !ok {
		return msgDecodeError{}
	}
	return msg
}

// islandResultParts reads a Sky Result of any instantiation.
func islandResultParts(r any) (tag int, okV, errV any, isResult bool) {
	switch v := r.(type) {
	case SkyResult[any, any]:
		return v.Tag, v.OkValue, v.ErrValue, true
	case SkyResult[SkyADT, any]:
		return v.Tag, v.OkValue, v.ErrValue, true
	case nil:
		return 0, nil, nil, false
	}
	rv := reflect.ValueOf(r)
	if rv.Kind() != reflect.Struct {
		return 0, nil, nil, false
	}
	t, o, e := rv.FieldByName("Tag"), rv.FieldByName("OkValue"), rv.FieldByName("ErrValue")
	if !t.IsValid() || !o.IsValid() || !e.IsValid() || t.Kind() != reflect.Int {
		return 0, nil, nil, false
	}
	return int(t.Int()), o.Interface(), e.Interface(), true
}

// islandCmd is the payload of an "island" command.
type islandCmd struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Payload json.RawMessage `json:"payload"`
	// Seq is the command's per-island sequence number on Sky.Live
	// (live_island_delivery.go); 0 (omitted) on Sky.Spa, whose commands
	// never cross a network.
	Seq int64 `json:"seq,omitempty"`
}

// Cmd_toIsland builds the "island" command. Sky-side surface:
//
//	Std.Cmd.toIsland : String -> String -> Json.Encode.Value -> Cmd msg
//
// The payload is serialised here, once, so every client gets the same JSON
// (a NaN Float is the classified JsonEncodeFailure, as in Json.Encode.encode).
func Cmd_toIsland(id, name, payload any) SkyCmd {
	return cmdT{kind: "island", payload: islandCmd{
		ID:      AsString(id),
		Name:    AsString(name),
		Payload: json.RawMessage(AsString(JsonEnc_encode(0, payload))),
	}}
}

// islandCmdOf reads the payload of an "island" command.
func islandCmdOf(c cmdT) (islandCmd, bool) {
	ic, ok := c.payload.(islandCmd)
	return ic, ok
}

// islandFrameData is the JSON the clients receive for an island command.
func islandFrameData(ic islandCmd) string {
	b, err := json.Marshal(ic)
	if err != nil {
		return `{"id":"","name":"","payload":null}`
	}
	return string(b)
}
