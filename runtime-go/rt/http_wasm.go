//go:build js

package rt

import (
	"fmt"
	"strings"
	"syscall/js"
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

// fetchBlocking issues globalThis.fetch(url, opts) and blocks until the
// response (and its body text) settle, returning the Sky Result. It MUST be
// called from a goroutine (the perform goroutine) so the block yields control
// to the browser event loop that resolves the Promise.
func fetchBlocking(method, url, body string) SkyResult[any, any] {
	lower := strings.ToLower(method)
	global := js.Global()
	fetch := global.Get("fetch")
	if fetch.Type() != js.TypeFunction {
		return Err[any, any](ErrNetwork("http." + lower + ": fetch is unavailable in this runtime"))
	}

	opts := global.Get("Object").New()
	opts.Set("method", method)
	if method == "POST" {
		opts.Set("body", body)
		hdr := global.Get("Object").New()
		hdr.Set("Content-Type", "application/json")
		opts.Set("headers", hdr)
	}

	ch := make(chan SkyResult[any, any], 1)
	done := false
	finish := func(r SkyResult[any, any]) {
		if done {
			return
		}
		done = true
		ch <- r
	}

	var onResp, onErr, onText, onTextErr js.Func
	status := 0

	onText = js.FuncOf(func(this js.Value, a []js.Value) any {
		b := ""
		if len(a) > 0 && a[0].Type() == js.TypeString {
			b = a[0].String()
		}
		finish(Ok[any, any](HttpResponse{
			Status:  status,
			Body:    b,
			Headers: map[string]string{},
		}))
		return nil
	})
	onTextErr = js.FuncOf(func(this js.Value, a []js.Value) any {
		finish(Err[any, any](ErrNetwork("http." + lower + ": read failed: " + rejectReason(a))))
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
		// Response.text() is itself a Promise; chain it.
		resp.Call("text").Call("then", onText).Call("catch", onTextErr)
		return nil
	})
	onErr = js.FuncOf(func(this js.Value, a []js.Value) any {
		finish(Err[any, any](ErrNetwork("http." + lower + ": " + rejectReason(a))))
		return nil
	})

	fetch.Invoke(url, opts).Call("then", onResp).Call("catch", onErr)

	result := <-ch
	// Settled exactly once; the other callbacks will never fire now, so it is
	// safe to release them all from here (outside any callback invocation).
	onResp.Release()
	onErr.Release()
	onText.Release()
	onTextErr.Release()
	return result
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
