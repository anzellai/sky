//go:build js

package rt

// ai_device_wasm.go — the client half of Std.Ai.Device, in the Sky.Spa wasm
// client.
//
// The Sky side (sky-stdlib/Std/Ai/Device.sky) decides where a call runs; these
// kernels are its primitives:
//
//	AiDevice_offer        GET /_sky/ai/device/<slot> (kept 30 s, revalidated by ETag)
//	AiDevice_capabilities the native shell's sky:aiCaps, else the worker's probe
//	AiDevice_run          the native shell's sky:aiChat, else the worker
//	AiDevice_server       POST /_sky/ai/device/<slot>/call
//	AiDevice_prepare      sky:aiPrepare, else the worker's download and load
//	AiDevice_progress     sky:aiStatus, else the worker's progress events
//	AiDevice_evict        sky:aiEvict, else the worker
//
// The order is: a native shell (iOS, Android, macOS) when it answers the AI
// ops, else the same-origin Web Worker (ai_device_worker.go), else the
// server. A backend that fails to load with DeviceUnsupported is remembered
// for the session (`failed` in the capabilities), so the next decision skips
// it.

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"syscall/js"
	"time"
)

// aiSkyError carries a Sky `Error` value through Go code unchanged. The
// server's copy is in ai_endpoint.go; ai_failure.go names the type on both.
type aiSkyError struct{ v any }

func (e *aiSkyError) Error() string { return fmt.Sprintf("%v", e.v) }

const (
	aiDeviceOpCaps    = "sky:aiCaps"
	aiDeviceOpPrepare = "sky:aiPrepare"
	aiDeviceOpStatus  = "sky:aiStatus"
	aiDeviceOpChat    = "sky:aiChat"
	aiDeviceOpEvict   = "sky:aiEvict"
)

// aiJsAwait blocks the perform goroutine on a Promise and returns its value
// or its rejection text.
func aiJsAwait(p js.Value) (js.Value, error) {
	type res struct {
		v   js.Value
		err error
	}
	ch := make(chan res, 1)
	var onOk, onErr js.Func
	onOk = js.FuncOf(func(this js.Value, a []js.Value) any {
		v := js.Undefined()
		if len(a) > 0 {
			v = a[0]
		}
		ch <- res{v: v}
		onOk.Release()
		onErr.Release()
		return nil
	})
	onErr = js.FuncOf(func(this js.Value, a []js.Value) any {
		msg := "rejected"
		if len(a) > 0 {
			msg = jsRejectionText(a[0])
		}
		ch <- res{err: fmt.Errorf("%s", msg)}
		onOk.Release()
		onErr.Release()
		return nil
	})
	p.Call("then", onOk).Call("catch", onErr)
	r := <-ch
	return r.v, r.err
}

// aiJsFetch is one same-origin fetch: the status, the body text and the
// ETag.
func aiJsFetch(method, url string, headers map[string]string, body string) (status int, text, etag string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	opts := js.Global().Get("Object").New()
	opts.Set("method", method)
	opts.Set("credentials", "same-origin")
	h := js.Global().Get("Object").New()
	for k, v := range headers {
		h.Set(k, v)
	}
	opts.Set("headers", h)
	if body != "" {
		opts.Set("body", body)
	}
	resp, err := aiJsAwait(js.Global().Call("fetch", url, opts))
	if err != nil {
		return 0, "", "", err
	}
	status = resp.Get("status").Int()
	etag = resp.Get("headers").Call("get", "ETag").String()
	if etag == "<null>" {
		etag = ""
	}
	t, err := aiJsAwait(resp.Call("text"))
	if err != nil {
		return status, "", etag, err
	}
	return status, t.String(), etag, nil
}

// aiDeviceAnswerErr turns a server's failure answer into the typed failure
// it carries, else an Unreachable or RemoteRejected.
func aiDeviceAnswerErr(what string, status int, body string, err error) error {
	if err != nil {
		return aiFailf(aiUnreachable, "%s: %v", what, err).with("target", "the app's server")
	}
	if f, ok := aiDeviceFailureFrom([]byte(body)); ok {
		return f
	}
	f := aiFailf(aiRemoteRejected, "%s answered %d", what, status)
	f.httpStatus = status
	return f
}

// ─── the offer ──────────────────────────────────────────────────────────

type aiDeviceCachedOffer struct {
	body, etag string
	at         time.Time
}

var (
	aiDeviceOfferMu    sync.Mutex
	aiDeviceOfferCache = map[string]aiDeviceCachedOffer{}
)

func aiDeviceFetchOffer(slot string) (string, error) {
	aiDeviceOfferMu.Lock()
	c, ok := aiDeviceOfferCache[slot]
	aiDeviceOfferMu.Unlock()
	if ok && time.Since(c.at) < 30*time.Second {
		return c.body, nil
	}
	headers := map[string]string{"Accept": "application/json"}
	if ok && c.etag != "" {
		headers["If-None-Match"] = c.etag
	}
	what := "the offer of slot " + slot
	status, body, etag, err := aiJsFetch("GET", "/_sky/ai/device/"+slot, headers, "")
	if err == nil && status == 304 && ok {
		c.at = time.Now()
		aiDeviceOfferMu.Lock()
		aiDeviceOfferCache[slot] = c
		aiDeviceOfferMu.Unlock()
		return c.body, nil
	}
	if err != nil || status != 200 {
		return "", aiDeviceAnswerErr(what, status, body, err)
	}
	aiDeviceOfferMu.Lock()
	aiDeviceOfferCache[slot] = aiDeviceCachedOffer{body: body, etag: etag, at: time.Now()}
	aiDeviceOfferMu.Unlock()
	return body, nil
}

// ─── the native shell ───────────────────────────────────────────────────

// aiDeviceShell asks the native shell for one AI op. present is false in a
// plain browser and in a shell that does not know the AI ops.
func aiDeviceShell(op string, payload map[string]string) (present bool, data string, err error) {
	r := nativeShellJS(op, nativePayload(payload))
	if !r.Present {
		return false, "", nil
	}
	if !r.Ok {
		if strings.HasPrefix(r.Data, "invalid: unknown op") || strings.Contains(r.Data, "no native handler") {
			return false, "", nil
		}
		return true, "", aiDeviceShellFailure(r.Data)
	}
	return true, r.Data, nil
}

// ─── the worker ─────────────────────────────────────────────────────────

type aiDeviceReply struct {
	ok    bool
	value string
	err   string
}

var (
	aiDeviceWorkerMu   sync.Mutex
	aiDeviceWorkerVal  js.Value
	aiDeviceWorkerOn   js.Func
	aiDeviceSeq        int
	aiDevicePending    = map[int]chan aiDeviceReply{}
	aiDeviceProgressOf = map[string]aiDeviceProgress{}
	aiDeviceFailed     = map[string]bool{}
)

// aiDeviceEngines reads the engine files the build staged from the page's
// `sky-ai-engine` meta: {"cpu":{"mjs":…,"wasm":…},"webgpu":{…}}.
func aiDeviceEngines() js.Value {
	doc := js.Global().Get("document")
	if !doc.Truthy() {
		return js.Global().Get("Object").New()
	}
	meta := doc.Call("querySelector", `meta[name="sky-ai-engine"]`)
	if !meta.Truthy() {
		return js.Global().Get("Object").New()
	}
	v, err := aiJsParse(meta.Call("getAttribute", "content").String())
	if err != nil {
		return js.Global().Get("Object").New()
	}
	return v
}

func aiJsParse(s string) (v js.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	return js.Global().Get("JSON").Call("parse", s), nil
}

func aiDeviceWorker() (js.Value, error) {
	aiDeviceWorkerMu.Lock()
	defer aiDeviceWorkerMu.Unlock()
	if aiDeviceWorkerVal.Truthy() {
		return aiDeviceWorkerVal, nil
	}
	ctor := js.Global().Get("Worker")
	if ctor.Type() != js.TypeFunction {
		return js.Undefined(), aiDeviceUnsupportedf(aiDeviceWasmCpu, "this browser has no Web Worker")
	}
	opts := js.Global().Get("Object").New()
	opts.Set("type", "module")
	w := ctor.New(AiDeviceWorkerPath, opts)
	aiDeviceWorkerOn = js.FuncOf(func(this js.Value, a []js.Value) any {
		if len(a) == 0 {
			return nil
		}
		d := a[0].Get("data")
		if d.Get("type").String() == "progress" {
			key := d.Get("key").String()
			aiDeviceWorkerMu.Lock()
			aiDeviceProgressOf[key] = aiDeviceProgress{State: "downloading",
				DoneBytes: int64(d.Get("doneBytes").Float()), TotalBytes: int64(d.Get("totalBytes").Float())}
			aiDeviceWorkerMu.Unlock()
			return nil
		}
		id := d.Get("id").Int()
		rep := aiDeviceReply{ok: d.Get("ok").Bool()}
		if rep.ok {
			rep.value = d.Get("value").String()
		} else {
			rep.err = d.Get("error").String()
		}
		aiDeviceWorkerMu.Lock()
		ch := aiDevicePending[id]
		delete(aiDevicePending, id)
		aiDeviceWorkerMu.Unlock()
		if ch != nil {
			ch <- rep
		}
		return nil
	})
	w.Set("onmessage", aiDeviceWorkerOn)
	aiDeviceWorkerVal = w
	return w, nil
}

// aiDeviceAsk sends one request to the worker and waits for its answer.
func aiDeviceAsk(op string, fields map[string]any) (string, error) {
	w, err := aiDeviceWorker()
	if err != nil {
		return "", err
	}
	aiDeviceWorkerMu.Lock()
	aiDeviceSeq++
	id := aiDeviceSeq
	ch := make(chan aiDeviceReply, 1)
	aiDevicePending[id] = ch
	aiDeviceWorkerMu.Unlock()
	msg := js.Global().Get("Object").New()
	msg.Set("id", id)
	msg.Set("op", op)
	msg.Set("engines", aiDeviceEngines())
	for k, v := range fields {
		msg.Set(k, v)
	}
	w.Call("postMessage", msg)
	rep := <-ch
	if !rep.ok {
		return "", aiDeviceShellFailure(rep.err)
	}
	return rep.value, nil
}

// ─── kernels ────────────────────────────────────────────────────────────

func aiDeviceOkString(s string) any { return Ok[any, any](s) }

// AiDevice_offer : String -> Task Error String
func AiDevice_offer(slotArg any) any {
	slot := AsString(slotArg)
	return func() any {
		body, err := aiDeviceFetchOffer(slot)
		if err != nil {
			return aiTaskErr(err)
		}
		return aiDeviceOkString(body)
	}
}

// AiDevice_capabilities : String -> Task Error String
func AiDevice_capabilities(_ any) any {
	return func() any {
		raw := ""
		present, data, err := aiDeviceShell(aiDeviceOpCaps, map[string]string{})
		switch {
		case err != nil:
			return aiTaskErr(err)
		case present:
			raw = data
		default:
			raw, err = aiDeviceAsk("caps", nil)
			if err != nil {
				return aiTaskErr(err)
			}
		}
		var c aiDeviceCaps
		if json.Unmarshal([]byte(raw), &c) != nil {
			return aiTaskErr(aiFailf(aiEngineFailed, "the device reported unreadable capabilities"))
		}
		aiDeviceWorkerMu.Lock()
		for b := range aiDeviceFailed {
			c.Failed = append(c.Failed, b)
		}
		aiDeviceWorkerMu.Unlock()
		c.normalise()
		b, _ := json.Marshal(c)
		return aiDeviceOkString(string(b))
	}
}

// aiDeviceRemember records a backend that failed to load with
// DeviceUnsupported, so this session's next decision skips it.
func aiDeviceRemember(backend string, err error) {
	if f, ok := err.(*aiFailure); ok && f.code == aiDeviceUnsupported {
		aiDeviceWorkerMu.Lock()
		aiDeviceFailed[backend] = true
		aiDeviceWorkerMu.Unlock()
	}
}

// AiDevice_run : String -> String -> String -> String -> String -> Task Error String
func AiDevice_run(backendArg, slotArg, offerArg, pathArg, bodyArg any) any {
	backend, slot, offer := AsString(backendArg), AsString(slotArg), AsString(offerArg)
	path, body := AsString(pathArg), AsString(bodyArg)
	return func() any {
		var out string
		var err error
		if backend == aiDeviceNative {
			var present bool
			present, out, err = aiDeviceShell(aiDeviceOpChat, map[string]string{
				"slot": slot, "offer": offer, "path": path, "body": body, "origin": aiDeviceOrigin()})
			if err == nil && !present {
				err = aiDeviceUnsupportedf(aiDeviceNative, "this app has no native shell")
			}
		} else {
			out, err = aiDeviceAsk("chat", map[string]any{
				"slot": slot, "backend": backend, "offer": offer, "path": path, "body": body})
		}
		if err != nil {
			aiDeviceRemember(backend, err)
			return aiTaskErr(err)
		}
		return aiDeviceOkString(out)
	}
}

func aiDeviceOrigin() string {
	loc := js.Global().Get("location")
	if !loc.Truthy() {
		return ""
	}
	return loc.Get("origin").String()
}

// AiDevice_server : String -> String -> String -> Task Error String
func AiDevice_server(slotArg, pathArg, bodyArg any) any {
	slot, path, body := AsString(slotArg), AsString(pathArg), AsString(bodyArg)
	return func() any {
		payload, _ := json.Marshal(aiDeviceCallBody{Path: path, Body: body})
		status, out, _, err := aiJsFetch("POST", "/_sky/ai/device/"+slot+"/call",
			map[string]string{"Content-Type": "application/json"}, string(payload))
		if err != nil || status != 200 {
			return aiTaskErr(aiDeviceAnswerErr("the server slot "+slot, status, out, err))
		}
		return aiDeviceOkString(out)
	}
}

// aiDeviceCallBody is the fallback call's body (the server reads the same
// shape, ai_device_serve.go).
type aiDeviceCallBody struct {
	Path string `json:"path"`
	Body string `json:"body"`
}

func aiDeviceProgressKey(slot, offer string) string {
	var o aiDeviceOffer
	_ = json.Unmarshal([]byte(offer), &o)
	key := slot + ":" + o.Model.Sha256
	if o.Adapter != nil {
		key += ":" + o.Adapter.Sha256
	}
	return key
}

// AiDevice_prepare : String -> String -> String -> Task Error String
func AiDevice_prepare(slotArg, backendArg, offerArg any) any {
	slot, backend, offer := AsString(slotArg), AsString(backendArg), AsString(offerArg)
	return func() any {
		key := aiDeviceProgressKey(slot, offer)
		var out string
		var err error
		if backend == aiDeviceNative {
			var present bool
			present, out, err = aiDeviceShell(aiDeviceOpPrepare, map[string]string{
				"slot": slot, "offer": offer, "origin": aiDeviceOrigin()})
			if err == nil && !present {
				err = aiDeviceUnsupportedf(aiDeviceNative, "this app has no native shell")
			}
		} else {
			aiDevicePersist()
			aiDeviceWorkerMu.Lock()
			aiDeviceProgressOf[key] = aiDeviceProgress{State: "downloading"}
			aiDeviceWorkerMu.Unlock()
			out, err = aiDeviceAsk("prepare", map[string]any{
				"slot": slot, "backend": backend, "offer": offer, "key": key})
		}
		aiDeviceWorkerMu.Lock()
		if err != nil {
			aiDeviceProgressOf[key] = aiDeviceProgress{State: "failed", Error: err.Error()}
		} else {
			aiDeviceProgressOf[key] = aiDeviceProgress{State: "ready"}
		}
		aiDeviceWorkerMu.Unlock()
		if err != nil {
			aiDeviceRemember(backend, err)
			return aiTaskErr(err)
		}
		return aiDeviceOkString(out)
	}
}

// aiDevicePersist asks the browser to keep the origin's storage (a stored
// model survives storage pressure). The answer is advisory.
func aiDevicePersist() {
	defer func() { _ = recover() }()
	st := js.Global().Get("navigator").Get("storage")
	if st.Truthy() && st.Get("persist").Type() == js.TypeFunction {
		_, _ = aiJsAwait(st.Call("persist"))
	}
}

// AiDevice_progress : String -> String -> Task Error String
func AiDevice_progress(slotArg, offerArg any) any {
	slot, offer := AsString(slotArg), AsString(offerArg)
	return func() any {
		present, data, err := aiDeviceShell(aiDeviceOpStatus, map[string]string{"slot": slot, "offer": offer})
		if err != nil {
			return aiTaskErr(err)
		}
		if present {
			return aiDeviceOkString(data)
		}
		aiDeviceWorkerMu.Lock()
		p, ok := aiDeviceProgressOf[aiDeviceProgressKey(slot, offer)]
		aiDeviceWorkerMu.Unlock()
		if !ok {
			p = aiDeviceProgress{State: "idle"}
		}
		b, _ := json.Marshal(p)
		return aiDeviceOkString(string(b))
	}
}

// AiDevice_evict : String -> String -> Task Error String
func AiDevice_evict(slotArg, offerArg any) any {
	slot, offer := AsString(slotArg), AsString(offerArg)
	return func() any {
		present, _, err := aiDeviceShell(aiDeviceOpEvict, map[string]string{"slot": slot, "offer": offer})
		if err == nil && !present {
			_, err = aiDeviceAsk("evict", map[string]any{"slot": slot, "offer": offer})
		}
		if err != nil {
			return aiTaskErr(err)
		}
		aiDeviceWorkerMu.Lock()
		delete(aiDeviceProgressOf, aiDeviceProgressKey(slot, offer))
		aiDeviceWorkerMu.Unlock()
		return aiDeviceOkString("")
	}
}
