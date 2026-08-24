//go:build !js

package rt

import (
	"fmt"
	"net/http"
	"strings"
)

// http_notjs.go — the host (server / CLI / desktop) implementation of the
// untyped Http.get / Http.post kernels. These use Go's net/http client and
// block the calling goroutine until the response arrives — correct for the
// server backends, where Cmd.perform runs each Task in its own goroutine
// (see live.go runPerform).
//
// The client (GOOS=js GOARCH=wasm) counterpart lives in http_wasm.go
// (//go:build js): there is no goroutine scheduler to hide a blocking round
// trip behind, so the wasm build calls the browser `fetch` API and returns a
// jsAsync the single-threaded Sky.Spa interpreter resolves via Promise
// .then/.catch. Both builds return the SAME Sky shape —
// `Task Error HttpResponse` (an `Ok[any,any](HttpResponse)` / `Err[any,any]`
// thunk) — so `Sky.Core.Http.get`/`post` are identical Sky surfaces on either
// target. Split out of stdlib_extra.go so the two builds can diverge on the
// transport without diverging on the contract.

// Http.get : String -> Task Error HttpResponse
func Http_get(url any) any {
	u := fmt.Sprintf("%v", url)
	return func() any {
		return WithHTTPClientSpan("GET", u, func() any {
			req, err := http.NewRequest("GET", u, nil)
			if err != nil {
				return Err[any, any](ErrNetwork("http.get: " + err.Error()))
			}
			// Carry the current trace context + inject W3C traceparent
			// so the downstream service nests under this client span.
			req = req.WithContext(CurrentTraceContext())
			InjectTraceHeaders(req)
			resp, err := skyHTTPClient().Do(req)
			if err != nil {
				return Err[any, any](ErrNetwork("http.get: " + err.Error()))
			}
			body, err := readBoundedBody(resp.Body)
			if err != nil {
				return Err[any, any](ErrNetwork("http.get read: " + err.Error()))
			}
			hdrs := map[string]string{}
			for k, v := range resp.Header {
				if len(v) > 0 {
					hdrs[k] = v[0]
				}
			}
			return Ok[any, any](HttpResponse{
				Status:  resp.StatusCode,
				Body:    body,
				Headers: hdrs,
			})
		})
	}
}

// Http.post : String -> String -> Task Error HttpResponse
// (url, body)
func Http_post(url any, body any) any {
	u := fmt.Sprintf("%v", url)
	b := fmt.Sprintf("%v", body)
	return func() any {
		return WithHTTPClientSpan("POST", u, func() any {
			req, err := http.NewRequest("POST", u, strings.NewReader(b))
			if err != nil {
				return Err[any, any](ErrNetwork("http.post: " + err.Error()))
			}
			req.Header.Set("Content-Type", "application/json")
			req = req.WithContext(CurrentTraceContext())
			InjectTraceHeaders(req)
			resp, err := skyHTTPClient().Do(req)
			if err != nil {
				return Err[any, any](ErrNetwork("http.post: " + err.Error()))
			}
			rb, err := readBoundedBody(resp.Body)
			if err != nil {
				return Err[any, any](ErrNetwork("http.post read: " + err.Error()))
			}
			hdrs := map[string]string{}
			for k, v := range resp.Header {
				if len(v) > 0 {
					hdrs[k] = v[0]
				}
			}
			return Ok[any, any](HttpResponse{
				Status:  resp.StatusCode,
				Body:    rb,
				Headers: hdrs,
			})
		})
	}
}
