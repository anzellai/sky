//go:build js

package rt

// Provider.chatStream on a device slot, in the client: the answer streams
// from the browser worker (`streamBegin` / `streamNext`), from the native
// shell (`sky:aiStreamBegin` / `sky:aiStreamNext`), or, when the decision is
// the server, from the slot's streaming fallback call
// (`POST /_sky/ai/device/<slot>/stream`, server-sent events). Every event is
// {"delta":…} or, last, {"done":true,"body":"…"} (Provider.StreamTransport).

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"syscall/js"
)

const (
	aiDeviceOpStreamBegin = "sky:aiStreamBegin"
	aiDeviceOpStreamNext  = "sky:aiStreamNext"
)

type aiDeviceStreamState struct {
	kind    string // "worker", "shell" or "server"
	reader  js.Value
	decoder js.Value
	buf     string
	events  []string
}

var (
	aiDeviceStreamsMu  sync.Mutex
	aiDeviceStreams    = map[string]*aiDeviceStreamState{}
	aiDeviceStreamSeqN int
)

func aiDeviceStreamAdd(st *aiDeviceStreamState) string {
	aiDeviceStreamsMu.Lock()
	defer aiDeviceStreamsMu.Unlock()
	aiDeviceStreamSeqN++
	id := "d" + strconv.Itoa(aiDeviceStreamSeqN)
	aiDeviceStreams[id] = st
	return id
}

func aiDeviceStreamTake(id string, forget bool) *aiDeviceStreamState {
	aiDeviceStreamsMu.Lock()
	defer aiDeviceStreamsMu.Unlock()
	st := aiDeviceStreams[id]
	if forget {
		delete(aiDeviceStreams, id)
	}
	return st
}

// AiDevice_streamOpen : String -> String -> String -> String -> String -> Task Error String
func AiDevice_streamOpen(backendArg, slotArg, offerArg, pathArg, bodyArg any) any {
	backend, slot, offer := AsString(backendArg), AsString(slotArg), AsString(offerArg)
	path, body := AsString(pathArg), AsString(bodyArg)
	return func() any {
		var err error
		kind := "worker"
		if backend == aiDeviceNative {
			kind = "shell"
			var present bool
			present, _, err = aiDeviceShell(aiDeviceOpStreamBegin, map[string]string{
				"slot": slot, "offer": offer, "path": path, "body": body, "origin": aiDeviceOrigin()})
			if err == nil && !present {
				err = aiDeviceUnsupportedf(aiDeviceNative, "this app has no native shell")
			}
		} else {
			_, err = aiDeviceAsk("streamBegin", map[string]any{
				"slot": slot, "backend": backend, "offer": offer, "path": path, "body": body})
		}
		if err != nil {
			aiDeviceRemember(backend, err)
			return aiTaskErr(err)
		}
		return aiDeviceOkString(aiDeviceStreamAdd(&aiDeviceStreamState{kind: kind}))
	}
}

// AiDevice_streamServer : String -> String -> String -> Task Error String
func AiDevice_streamServer(slotArg, pathArg, bodyArg any) any {
	slot, path, body := AsString(slotArg), AsString(pathArg), AsString(bodyArg)
	return func() any {
		st, err := aiDeviceFetchStream("/_sky/ai/device/"+slot+"/stream", "the server slot "+slot, path, body)
		if err != nil {
			return aiTaskErr(err)
		}
		return aiDeviceOkString(aiDeviceStreamAdd(st))
	}
}

// aiDeviceFetchStream opens the streaming fallback call and keeps its body
// reader.
func aiDeviceFetchStream(url, what, path, body string) (st *aiDeviceStreamState, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = aiFailf(aiUnreachable, "%s: %v", what, r).with("target", what)
		}
	}()
	payload, _ := json.Marshal(aiDeviceCallBody{Path: path, Body: body})
	opts := js.Global().Get("Object").New()
	opts.Set("method", "POST")
	opts.Set("credentials", "same-origin")
	h := js.Global().Get("Object").New()
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "text/event-stream")
	opts.Set("headers", h)
	opts.Set("body", string(payload))
	resp, ferr := aiJsAwait(js.Global().Call("fetch", url, opts))
	if ferr != nil {
		return nil, aiDeviceAnswerErr(what, 0, "", ferr)
	}
	status := resp.Get("status").Int()
	if status != 200 {
		t, _ := aiJsAwait(resp.Call("text"))
		text := ""
		if t.Type() == js.TypeString {
			text = t.String()
		}
		return nil, aiDeviceAnswerErr(what, status, text, nil)
	}
	return &aiDeviceStreamState{
		kind:    "server",
		reader:  resp.Get("body").Call("getReader"),
		decoder: js.Global().Get("TextDecoder").New(),
	}, nil
}

// nextServerEvent reads the fallback stream to its next event.
func (st *aiDeviceStreamState) nextServerEvent() (string, error) {
	for {
		if len(st.events) > 0 {
			ev := st.events[0]
			st.events = st.events[1:]
			return ev, nil
		}
		chunk, err := aiJsAwait(st.reader.Call("read"))
		if err != nil {
			return "", aiFailf(aiUnreachable, "the server stream broke: %v", err).with("target", "server")
		}
		if chunk.Get("done").Bool() {
			return "", aiFailf(aiUnreachable, "the server stream ended before the answer did").with("target", "server")
		}
		opts := js.Global().Get("Object").New()
		opts.Set("stream", true)
		st.buf += st.decoder.Call("decode", chunk.Get("value"), opts).String()
		for {
			i := strings.Index(st.buf, "\n\n")
			if i < 0 {
				break
			}
			block := st.buf[:i]
			st.buf = st.buf[i+2:]
			for _, line := range strings.Split(block, "\n") {
				if data, ok := strings.CutPrefix(line, "data: "); ok {
					st.events = append(st.events, data)
				}
			}
		}
	}
}

// AiDevice_streamNext : String -> Task Error String
func AiDevice_streamNext(idArg any) any {
	id := AsString(idArg)
	return func() any {
		st := aiDeviceStreamTake(id, false)
		if st == nil {
			return aiTaskErr(aiFailf(aiInvalidSetting, "stream %q is not open", id).
				with("setting", "stream").with("reason", "not open"))
		}
		var ev string
		var err error
		switch st.kind {
		case "worker":
			ev, err = aiDeviceAsk("streamNext", map[string]any{})
		case "shell":
			_, ev, err = aiDeviceShell(aiDeviceOpStreamNext, map[string]string{})
		default:
			ev, err = st.nextServerEvent()
			if err == nil && strings.HasPrefix(ev, `{"failure":`) {
				var w struct {
					Failure json.RawMessage `json:"failure"`
				}
				_ = json.Unmarshal([]byte(ev), &w)
				if f, ok := aiDeviceFailureFrom(w.Failure); ok {
					err = f
				} else {
					err = aiFailf(aiRemoteRejected, "the server stream failed: %.300s", ev)
				}
			}
		}
		if err != nil {
			aiDeviceStreamClose(id)
			return aiTaskErr(err)
		}
		var done struct {
			Done bool `json:"done"`
		}
		if json.Unmarshal([]byte(ev), &done) == nil && done.Done {
			aiDeviceStreamClose(id)
		}
		return aiDeviceOkString(ev)
	}
}

func aiDeviceStreamClose(id string) {
	st := aiDeviceStreamTake(id, true)
	if st != nil && st.kind == "server" && st.reader.Truthy() {
		func() {
			defer func() { _ = recover() }()
			st.reader.Call("cancel")
		}()
	}
}

// AiDevice_streamClose : String -> Task Error ()
func AiDevice_streamClose(idArg any) any {
	id := AsString(idArg)
	return func() any {
		aiDeviceStreamClose(id)
		return Ok[any, any](struct{}{})
	}
}
