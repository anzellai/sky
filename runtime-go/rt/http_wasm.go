//go:build js

package rt

import (
	"fmt"
	"math/rand"
	"strings"
	"syscall/js"
	"time"
)

// http_wasm.go — the Sky.Spa client (GOOS=js GOARCH=wasm) implementation of
// the untyped Http.get / Http.post kernels. The host build (http_notjs.go)
// uses net/http; the client calls the browser `fetch` API.
//
// # Why this returns a real Result (and blocks), not a Promise placeholder
//
// Typed codegen wraps a `Cmd.perform`'s Task in `rt.TaskCoerceT[E, A]`
// (rt.go), which RUNS the task and coerces its synchronous return value to the
// declared result type — `HttpResponse` here. So the task thunk MUST return a
// `SkyResult` when it is called; a "pending" placeholder (e.g. a Promise) is
// coerced to HttpResponse and panics before it can settle. The client kernel
// therefore issues fetch, BLOCKS the calling goroutine on a channel that the
// fetch Promise's .then/.catch callbacks fill, and returns the settled Sky
// Result — the canonical Go/wasm "await a JS Promise" pattern. The perform
// interpreter runs this on a cooperatively-scheduled goroutine (NOT an OS
// thread — wasm is single-threaded), so the block yields to the browser event
// loop rather than freezing it (see live_wasm.go performTask).
//
// The returned Sky value is identical to the host build: a
// `Task Error HttpResponse` thunk producing `Ok[any,any](HttpResponse)` on a
// completed response (any HTTP status — a 4xx/5xx is a successful round trip,
// a value not an error, matching net/http) and `Err[any,any](ErrNetwork …)`
// when the fetch itself rejects (network failure, CORS, DNS).

// Http.get : String -> Task Error HttpResponse
func Http_get(url any) any {
	u := fmt.Sprintf("%v", url)
	return func() any { return fetchBlocking("GET", u, "") }
}

// Http.post : String -> String -> Task Error HttpResponse
// (url, body) — JSON content type, matching the host build.
func Http_post(url any, body any) any {
	u := fmt.Sprintf("%v", url)
	b := fmt.Sprintf("%v", body)
	return func() any { return fetchBlocking("POST", u, b) }
}

// spaFetchTick is the Sub.every interval whose tick started the perform now
// beginning to run (0 for any other perform or RPC). The goroutine that runs a
// perform sets it before it runs the task; fetchBlocking reads and clears it on
// entry, before anything can block, so on wasm's single cooperative thread the
// value is the perform's own (a task that blocks BEFORE its first fetch is not
// attributed: the safe direction, nothing is gated).
var spaFetchTick int

// spaCoord is the client's retry coordinator (spa_retry.go). Its timers are Go
// timers (setTimeout under GOOS=js); a change of its queue repaints the
// connection state (spaConnNotify, spa_neterror_wasm.go).
var spaCoord = func() *spaRetryCoord {
	c := newSpaRetryCoord(time.Now, rand.Float64, func(d time.Duration, f func()) func() {
		t := time.AfterFunc(d, f)
		return func() { t.Stop() }
	})
	return c
}()

// The coordinator repaints the connection state on every change of its queue
// (set in init: spaConnNotify reads spaCoord).
func init() { spaCoord.change = spaConnNotify }

// fetchBlocking issues globalThis.fetch(url, opts) and blocks until the
// response (and its body text) settle, returning the Sky Result. It MUST be
// called from a goroutine (the perform goroutine) so the block yields control
// to the browser event loop that resolves the Promise.
//
// A request the runtime may re-send (an auto-split RPC, a GET:
// spaRetryable) that fails TRANSIENTLY (no answer, a timeout, 408 / 425 / 429 /
// 502 / 503 / 504) is retried through spaCoord before anything is returned, so
// the caller sees only the final result: a success, a FINAL answer, or the last
// transient failure once the retry budget is spent.
func fetchBlocking(method, url, body string) SkyResult[any, any] {
	if tick := spaFetchTick; tick > 0 {
		spaFetchTick = 0
		spaSch.tickNetBegin(tick)
		defer spaSch.tickNetEnd(tick)
	}
	if !spaRetryable(method, url) {
		r, _ := fetchOnce(method, url, body)
		return r
	}
	lower := strings.ToLower(method)
	return spaRetryLoop(spaCoord, spaOriginOf(url),
		func() (SkyResult[any, any], spaOutcome) { return fetchOnce(method, url, body) },
		func() SkyResult[any, any] {
			return Err[any, any](ErrNetwork("http." + lower + ": held back: the server is overloaded"))
		})
}

// fetchOnce is one attempt: fetch with an AbortController that aborts after
// spaFetchTimeout (a hang becomes Err Timeout), classified for the retry
// coordinator.
func fetchOnce(method, url, body string) (SkyResult[any, any], spaOutcome) {
	lower := strings.ToLower(method)
	global := js.Global()
	fetch := global.Get("fetch")
	if fetch.Type() != js.TypeFunction {
		return Err[any, any](ErrNetwork("http." + lower + ": fetch is unavailable in this runtime")),
			spaOutcome{kind: spaOutcomeFinal}
	}

	opts := global.Get("Object").New()
	opts.Set("method", method)
	if method == "POST" {
		opts.Set("body", body)
		hdr := global.Get("Object").New()
		hdr.Set("Content-Type", "application/json")
		// E-4: every auto-split RPC names the wire schema the page was built
		// for (spa_wire.go).
		if strings.HasPrefix(url, "/_rpc/") {
			if h := spaPageWireHash(); h != "" {
				hdr.Set(spaWireHeader, h)
			}
		}
		opts.Set("headers", hdr)
	}
	var ctrl js.Value
	if ac := global.Get("AbortController"); ac.Type() == js.TypeFunction {
		ctrl = ac.New()
		opts.Set("signal", ctrl.Get("signal"))
	}

	type settled struct {
		r SkyResult[any, any]
		o spaOutcome
	}
	ch := make(chan settled, 1)
	done := false
	timedOut := false
	finish := func(r SkyResult[any, any], o spaOutcome) {
		if done {
			return
		}
		done = true
		ch <- settled{r, o}
	}

	var onResp, onErr, onText, onTextErr, onTimeout js.Func
	status := 0
	retryAfter := ""

	onText = js.FuncOf(func(this js.Value, a []js.Value) any {
		b := ""
		if len(a) > 0 && a[0].Type() == js.TypeString {
			b = a[0].String()
		}
		finish(Ok[any, any](HttpResponse{
			Status:  status,
			Body:    b,
			Headers: map[string]string{},
		}), spaClassifyResponse(status, retryAfter, time.Now()))
		return nil
	})
	failed := func(what string, a []js.Value) {
		if timedOut {
			finish(Err[any, any](ErrTimeout()), spaNetworkOutcome())
			return
		}
		finish(Err[any, any](ErrNetwork("http."+lower+": "+what+rejectReason(a))), spaNetworkOutcome())
	}
	onTextErr = js.FuncOf(func(this js.Value, a []js.Value) any {
		failed("read failed: ", a)
		return nil
	})
	onResp = js.FuncOf(func(this js.Value, a []js.Value) any {
		var resp js.Value
		if len(a) > 0 {
			resp = a[0]
		}
		if s := resp.Get("status"); s.Type() == js.TypeNumber {
			status = s.Int()
		}
		hdrs := resp.Get("headers")
		if ra := hdrs.Call("get", "Retry-After"); ra.Type() == js.TypeString {
			retryAfter = ra.String()
		}
		// E-4: the backend runs another wire schema: reload (guarded).
		if status == 409 && strings.HasPrefix(url, "/_rpc/") {
			if st := hdrs.Call("get", "X-Sky-Status"); st.Type() == js.TypeString && st.String() == "reload" {
				spaWireReload()
			}
		}
		// Response.text() is itself a Promise; chain it.
		resp.Call("text").Call("then", onText).Call("catch", onTextErr)
		return nil
	})
	onErr = js.FuncOf(func(this js.Value, a []js.Value) any {
		failed("", a)
		return nil
	})
	onTimeout = js.FuncOf(func(this js.Value, a []js.Value) any {
		if done {
			return nil
		}
		timedOut = true
		if ctrl.Truthy() {
			ctrl.Call("abort") // rejects the fetch (or the body read): failed() maps it to Timeout
		} else {
			finish(Err[any, any](ErrTimeout()), spaNetworkOutcome())
		}
		return nil
	})
	timer := global.Call("setTimeout", onTimeout, int(spaFetchTimeout/time.Millisecond))

	fetch.Invoke(url, opts).Call("then", onResp).Call("catch", onErr)

	res := <-ch
	global.Call("clearTimeout", timer)
	// Settled exactly once; the other callbacks will never fire now, so it is
	// safe to release them all from here (outside any callback invocation).
	onResp.Release()
	onErr.Release()
	onText.Release()
	onTextErr.Release()
	onTimeout.Release()
	return res.r, res.o
}

// rejectReason extracts a human-readable message from a Promise rejection
// value (an Error, a string, or anything else).
func rejectReason(a []js.Value) string {
	if len(a) == 0 {
		return "request failed"
	}
	v := a[0]
	if !v.Truthy() {
		return "request failed"
	}
	if m := v.Get("message"); m.Type() == js.TypeString {
		return m.String()
	}
	if v.Type() == js.TypeString {
		return v.String()
	}
	return v.Call("toString").String()
}
