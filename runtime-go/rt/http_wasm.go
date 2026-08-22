//go:build js

package rt

import (
	"fmt"
	"strings"
	"syscall/js"
)

// http_wasm.go — the Sky.Spa client (GOOS=js GOARCH=wasm) implementation of
// the untyped Http.get / Http.post kernels. The host build (http_notjs.go)
// uses net/http and blocks the goroutine; the client has no goroutine
// scheduler to hide a blocking round trip behind, so it calls the browser
// `fetch` API and returns a jsAsync (see live_wasm.go) that the single-threaded
// Sky.Spa effect interpreter resolves via the Promise's .then/.catch — no
// goroutine, no blocking of the browser event loop.
//
// The returned Sky value is identical to the host build:
// `Task Error HttpResponse` — a thunk producing `Ok[any,any](HttpResponse)` on
// a completed response (any HTTP status, including 4xx/5xx — an error STATUS is
// still a successful round trip) and `Err[any,any](ErrNetwork …)` when the
// fetch itself rejects (network failure, CORS, DNS). This mirrors Go's
// net/http, where a non-2xx response is a value, not an `err`.

// Http.get : String -> Task Error HttpResponse
func Http_get(url any) any {
	u := fmt.Sprintf("%v", url)
	return func() any { return fetchAsync("GET", u, "") }
}

// Http.post : String -> String -> Task Error HttpResponse
// (url, body) — JSON content type, matching the host build.
func Http_post(url any, body any) any {
	u := fmt.Sprintf("%v", url)
	b := fmt.Sprintf("%v", body)
	return func() any { return fetchAsync("POST", u, b) }
}

// fetchAsync issues `globalThis.fetch(url, opts)` and returns a jsAsync whose
// promise resolves to a plain `{status, body}` JS object. Reading the response
// body is itself async (`Response.text()` is a Promise), so the chain is
// fetch → resp.text() → {status, body}. toResult/onReject build the Sky Result
// in Go, so no Go value ever has to survive a trip through a JS Promise.
func fetchAsync(method, url, body string) jsAsync {
	global := js.Global()
	fetch := global.Get("fetch")
	if fetch.Type() != js.TypeFunction {
		// No fetch in this environment: surface a resolved-Err jsAsync so the
		// error branch fires rather than a silent drop.
		return resolvedErr(ErrNetwork("http." + strings.ToLower(method) +
			": fetch is unavailable in this runtime"))
	}

	opts := global.Get("Object").New()
	opts.Set("method", method)
	if method == "POST" {
		opts.Set("body", body)
		hdr := global.Get("Object").New()
		hdr.Set("Content-Type", "application/json")
		opts.Set("headers", hdr)
	}

	respPromise := fetch.Invoke(url, opts)

	// resp => resp.text().then(text => ({ status: resp.status, body: text }))
	var toShape js.Func
	toShape = js.FuncOf(func(this js.Value, args []js.Value) any {
		defer toShape.Release()
		var resp js.Value
		if len(args) > 0 {
			resp = args[0]
		}
		status := resp.Get("status")
		textPromise := resp.Call("text")
		var wrap js.Func
		wrap = js.FuncOf(func(this js.Value, a2 []js.Value) any {
			defer wrap.Release()
			obj := global.Get("Object").New()
			obj.Set("status", status)
			if len(a2) > 0 {
				obj.Set("body", a2[0])
			} else {
				obj.Set("body", "")
			}
			return obj
		})
		// Returning a Promise from a .then handler chains it into the outer
		// promise, so `chained` resolves to the {status, body} object.
		return textPromise.Call("then", wrap)
	})
	chained := respPromise.Call("then", toShape)

	return jsAsync{
		promise: chained,
		toResult: func(v js.Value) any {
			status := 0
			if s := v.Get("status"); s.Type() == js.TypeNumber {
				status = s.Int()
			}
			bodyStr := ""
			if b := v.Get("body"); b.Type() == js.TypeString {
				bodyStr = b.String()
			}
			return Ok[any, any](HttpResponse{
				Status:  status,
				Body:    bodyStr,
				Headers: map[string]string{},
			})
		},
		onReject: func(v js.Value) any {
			detail := "request failed"
			if v.Truthy() {
				if m := v.Get("message"); m.Type() == js.TypeString {
					detail = m.String()
				} else {
					detail = v.Call("toString").String()
				}
			}
			return Err[any, any](ErrNetwork("http." + strings.ToLower(method) + ": " + detail))
		},
	}
}

// resolvedErr wraps an already-computed Sky Err value in a jsAsync backed by a
// resolved Promise, so the interpreter's uniform .then delivery path fires the
// error branch exactly once.
func resolvedErr(errResult any) jsAsync {
	promise := js.Global().Get("Promise").Call("resolve", js.Null())
	return jsAsync{
		promise:  promise,
		toResult: func(js.Value) any { return errResult },
		onReject: func(js.Value) any { return errResult },
	}
}
