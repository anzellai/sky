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
		r, _ := fetchOnce(method, url, body, false)
		return r
	}
	lower := strings.ToLower(method)
	return spaRetryLoop(spaCoord, spaOriginOf(url),
		func() (SkyResult[any, any], spaOutcome) { return fetchOnce(method, url, body, true) },
		func() SkyResult[any, any] {
			return Err[any, any](ErrNetwork("http." + lower + ": held back: the server is overloaded"))
		})
}

// fetchOnce is one attempt: fetch with an AbortController that aborts after
// spaFetchTimeout of running time (a hang becomes Err Timeout), classified for
// the retry coordinator. `resend` says the coordinator re-sends this request.
//
// The timeout counts running time, not wall time (v0.27.6). A timer that fires
// long after it was due was held by a suspension (a frozen or hidden page, a
// sleeping device): the attempt has not had its 30 s. A request the runtime
// does not re-send gets a fresh timeout then, instead of an Err Timeout the
// app never earned. A re-sent request is aborted, and the coordinator finds the
// attempt outlived its timeout, takes that as a resume and re-sends it at once
// with a fresh budget (spaRetryCoord.done).
func fetchOnce(method, url, body string, resend bool) (SkyResult[any, any], spaOutcome) {
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
	var timer js.Value
	armedAt := time.Now()
	status := 0
	retryAfter := ""
	respHeaders := SkyDict[string]{}

	onText = js.FuncOf(func(this js.Value, a []js.Value) any {
		b := ""
		if len(a) > 0 && a[0].Type() == js.TypeString {
			b = a[0].String()
		}
		finish(Ok[any, any](HttpResponse{
			Status:  status,
			Body:    b,
			Headers: respHeaders,
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
		respHeaders = fetchResponseHeaders(hdrs)
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
		if now := time.Now(); !resend && spaFetchTimerSuspended(armedAt, now) {
			armedAt = now
			timer = global.Call("setTimeout", onTimeout, int(spaFetchTimeout/time.Millisecond))
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
	timer = global.Call("setTimeout", onTimeout, int(spaFetchTimeout/time.Millisecond))

	// Cleanup also runs if the host violates the Promise contract below. Those
	// programming faults still panic; a started timeout must not retain its
	// Go callbacks while that panic unwinds.
	defer func() {
		global.Call("clearTimeout", timer)
		onResp.Release()
		onErr.Release()
		onText.Release()
		onTextErr.Release()
		onTimeout.Release()
	}()

	promise, started := invokeBrowserFetch(fetch, url, opts)
	if started {
		promise.Call("then", onResp).Call("catch", onErr)
	} else {
		finish(Err[any, any](ErrNetwork("http."+lower+": fetch could not be started")), spaNetworkOutcome())
	}

	res := <-ch
	return res.r, res.o
}

// invokeBrowserFetch contains only the host invocation. JavaScript can throw
// before fetch returns a Promise (so Promise.catch cannot observe it). Map
// that external exception to the caller's Network result; never swallow a Go
// programming panic or an invalid Promise returned by an altered host API.
func invokeBrowserFetch(fetch js.Value, url string, opts js.Value) (promise js.Value, started bool) {
	defer func() {
		if p := recover(); p != nil {
			if _, external := p.(js.Error); !external {
				panic(p)
			}
			started = false
		}
	}()
	return fetch.Invoke(url, opts), true
}

// rejectReason extracts a human-readable message from a Promise rejection
// value (an Error, a string, or anything else).
func rejectReason(a []js.Value) (reason string) {
	reason = "request failed"
	defer func() {
		if p := recover(); p != nil {
			if _, external := p.(js.Error); !external {
				panic(p)
			}
			// A rejection object is untrusted. Do not inspect it again after
			// a getter or method throws while we describe the original error.
			reason = "request failed"
		}
	}()
	if len(a) == 0 || !a[0].Truthy() {
		return reason
	}
	v := a[0]
	switch v.Type() {
	case js.TypeString:
		return v.String()
	case js.TypeBoolean, js.TypeNumber, js.TypeSymbol:
		return js.Global().Get("String").Invoke(v).String()
	case js.TypeObject, js.TypeFunction:
		// Value.Get is not a protected JavaScript call. Reflect.get through
		// Call turns an external getter exception into a recoverable js.Error.
		reflect := js.Global().Get("Reflect")
		if m := reflect.Call("get", v, "message"); m.Type() == js.TypeString {
			return m.String()
		}
		method := reflect.Call("get", v, "toString")
		if method.Type() != js.TypeFunction {
			return reason
		}
		// Read the method once and preserve its receiver, including accessors.
		return reflect.Call("apply", method, v, js.ValueOf([]any{})).String()
	default:
		return reason
	}
}

// fetchResponseHeaders reads a fetch Response's headers into the response's
// `headers` Dict. The browser gives every name in lower case; the Dict holds
// the canonical name (`Content-Type`), as on the server, so one
// `Dict.get "Content-Type" resp.headers` works on both sides of a Sky.Spa app.
// A repeated header arrives as one value joined with ", " (the Fetch rule).
// Before v0.29.1 the client Dict was always empty.
func fetchResponseHeaders(hdrs js.Value) SkyDict[string] {
	if hdrs.Type() != js.TypeObject {
		return SkyDict[string]{}
	}
	if fe := hdrs.Get("forEach"); fe.Type() != js.TypeFunction {
		return SkyDict[string]{}
	}
	var names, values []string
	cb := js.FuncOf(func(this js.Value, a []js.Value) any {
		if len(a) >= 2 && a[0].Type() == js.TypeString && a[1].Type() == js.TypeString {
			values = append(values, a[0].String())
			names = append(names, a[1].String())
		}
		return nil
	})
	defer cb.Release()
	hdrs.Call("forEach", cb)
	return canonicalHeaderDict(names, values)
}
