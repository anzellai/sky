//go:build !js

package rt

// stdlib_http_server.go — the net/http outbound-client kernels split out of
// stdlib_extra.go so the Sky.Spa CLIENT target (GOOS=js GOARCH=wasm, TinyGo)
// does not import net/http, which TinyGo cannot compile. The client issues its
// HTTP through the browser `fetch` API instead (Http_get / Http_post in
// http_wasm.go). The SERVER build (//go:build !js) compiles these here exactly
// as before — moving a function between files in the same package changes
// nothing the server emits. The portable helpers these used (HttpResponse,
// Http_parseQuery via net/url, isRecordArg, recordField, httpEnvTimeout) stay
// in stdlib_extra.go / rt.go so the client keeps them.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

// skyHTTPClient — the shared outbound client, built ONCE on first use (see the
// note that was in stdlib_extra.go: first use is necessarily after every
// init(), so a .env SKY_HTTP_CLIENT_TIMEOUT has been applied by then).
var skyHTTPClientOnce = sync.OnceValue(newSkyHttpClient)

// httpResponseHeaders is the `headers` Dict of an HTTP client response: the
// first value of each header, under its canonical name (canonicalHeaderDict).
func httpResponseHeaders(h http.Header) SkyDict[string] {
	return canonicalHeaderDict(firstHeaderValues(h))
}

func skyHTTPClient() *http.Client { return skyHTTPClientOnce() }

// skyHTTPClientTimeout — the whole-request deadline for outbound HTTP,
// resolved from the environment at the moment it is asked for.
func skyHTTPClientTimeout() time.Duration {
	// 30s default; overridable via SKY_HTTP_CLIENT_TIMEOUT (e.g. "180s",
	// "5m", or "0" to disable). Apps that call slow upstreams — LLM APIs
	// especially — routinely need more than 30s.
	return httpEnvTimeout("SKY_HTTP_CLIENT_TIMEOUT", 30*time.Second)
}

func newSkyHttpClient() *http.Client {
	return &http.Client{
		Timeout: skyHTTPClientTimeout(),
		// Bound redirect chains.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return nil
		},
		// Mock-by-default in test mode: intercept every outbound request (fixture
		// or fail-closed). A transparent passthrough outside test mode. See
		// test_http.go.
		Transport: &testHttpTransport{base: http.DefaultTransport},
	}
}

// Maximum response body size (64 MiB). Beyond this we truncate + error.
const clientMaxBodyBytes = 64 << 20

func readBoundedBody(body io.ReadCloser) (string, error) {
	defer body.Close()
	limited := io.LimitReader(body, clientMaxBodyBytes+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return "", err
	}
	if int64(len(buf)) > clientMaxBodyBytes {
		return "", fmt.Errorf("response body exceeds %d bytes", clientMaxBodyBytes)
	}
	return string(buf), nil
}

// P8/Http typed companion — Task-shaped string in, HttpResponse out.
func Http_getT(url string) func() SkyResult[string, HttpResponse] {
	return func() SkyResult[string, HttpResponse] {
		// Bounded by a Task.timeout deadline, as Http.get is.
		if left, ok := skyRemaining(); ok && left <= 0 {
			return Err[string, HttpResponse]("http.get: the deadline passed before the request was sent")
		}
		ctx, cancel := httpOutboundContext()
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return Err[string, HttpResponse]("http.get: " + redactURLError(err, url))
		}
		resp, err := skyHTTPClient().Do(req)
		if err != nil {
			return Err[string, HttpResponse]("http.get: " + redactURLError(err, url))
		}
		body, err := readBoundedBody(resp.Body)
		if err != nil {
			return Err[string, HttpResponse]("http.get read: " + redactURLError(err, url))
		}
		return Ok[string, HttpResponse](HttpResponse{
			Status:  resp.StatusCode,
			Body:    body,
			Headers: httpResponseHeaders(resp.Header),
		})
	}
}

// Http.request supports two calling shapes:
//
//   - Positional (legacy): `Http.request method url body headers` →
//     `Http_request(method, url, body, headers)`
//   - Record (Elm-style): `Http.request { method, url, headers, body }`
//     — single Sky record argument. This is the documented form in
//     templates/CLAUDE.md and matches Elm's `Http.request` API.
func Http_request(firstArg any, rest ...any) any {
	var method, url, body string
	var headers any
	// v0.15.44: per-request timeout / redirect overrides.
	timeoutMs := -1 // -1 = inherit skyHttpClient default
	followRedirects := true
	maxRedirects := 10
	minRemainingMs := 0 // Http.withMinRemaining; 0 = refuse only past the deadline
	if isRecordArg(firstArg) {
		method = fmt.Sprintf("%v", recordField(firstArg, "Method", "method"))
		url = fmt.Sprintf("%v", recordField(firstArg, "Url", "url"))
		body = fmt.Sprintf("%v", recordField(firstArg, "Body", "body"))
		headers = recordField(firstArg, "Headers", "headers")
		if t := recordField(firstArg, "Timeout", "timeout"); t != nil {
			timeoutMs = AsInt(t)
		}
		if fr := recordField(firstArg, "FollowRedirects", "followRedirects"); fr != nil {
			if b, ok := fr.(bool); ok {
				followRedirects = b
			}
		}
		if mr := recordField(firstArg, "MaxRedirects", "maxRedirects"); mr != nil {
			maxRedirects = AsInt(mr)
			if maxRedirects <= 0 {
				maxRedirects = 10
			}
		}
		if m := recordField(firstArg, "MinRemaining", "minRemaining"); m != nil {
			minRemainingMs = AsInt(m)
		}
	} else {
		method = fmt.Sprintf("%v", firstArg)
		if len(rest) >= 1 {
			url = fmt.Sprintf("%v", rest[0])
		}
		if len(rest) >= 2 {
			body = fmt.Sprintf("%v", rest[1])
		}
		if len(rest) >= 3 {
			headers = rest[2]
		}
	}
	if method == "" {
		method = "GET"
	}
	return func() any {
		// A Task.timeout deadline refuses the call before it is sent when
		// the deadline has passed, or when less than `minRemaining` ms is
		// left (Http.withMinRemaining).
		if r := httpDeadlineRefusal("Http.request", minRemainingMs); r != nil {
			return r
		}
		// One client span per call, as Http.get and Http.post have. The
		// span records the redacted URL (WithHTTPClientSpan). No trace
		// headers are added: Http.request sends exactly the headers the
		// app gave it.
		return WithHTTPClientSpan(method, url, func() any {
			return httpRequestDo(method, url, body, headers, timeoutMs, followRedirects, maxRedirects)
		})
	}
}

// httpRequestDo runs one Http.request call (see Http_request).
//
// The request carries the calling goroutine's trace context
// (httpOutboundContext): the client span is its parent for any
// instrumentation that reads the request context, and a Task.timeout deadline
// stops the call. Before v0.30.0 it was sent with context.Background(), so a
// deadline-bound Http.request ran on until the client timeout.
func httpRequestDo(method, url, body string, headers any, timeoutMs int, followRedirects bool, maxRedirects int) any {
	ctx, cancel := httpOutboundContext()
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	if err != nil {
		return Err[any, any](ErrNetwork("http.request: " + redactURLError(err, url)))
	}
	applyHttpHeaders(req, headers)
	client := skyHTTPClient()
	if timeoutMs >= 0 || !followRedirects || maxRedirects != 10 {
		client = httpClientFor(timeoutMs, followRedirects, maxRedirects)
	}
	resp, err := client.Do(req)
	if err != nil {
		if r := httpDeadlinePassed("Http.request"); r != nil {
			return r
		}
		return Err[any, any](ErrNetwork("http.request do: " + redactURLError(err, url)))
	}
	rb, err := readBoundedBody(resp.Body)
	if err != nil {
		if r := httpDeadlinePassed("Http.request"); r != nil {
			return r
		}
		return Err[any, any](ErrNetwork("http.request read: " + redactURLError(err, url)))
	}
	return Ok[any, any](HttpResponse{
		Status:  resp.StatusCode,
		Body:    rb,
		Headers: httpResponseHeaders(resp.Header),
	})
}

// httpOutboundContext is the context an Http.request call is sent with: the
// calling goroutine's trace context (span parentage, request id) without its
// cancellation, bounded by the Task.timeout deadline when one is in force.
// The end of the HTTP request that started the work does not cancel the call
// (Http.request never did), but the deadline does.
func httpOutboundContext() (context.Context, context.CancelFunc) {
	ctx := context.WithoutCancel(CurrentTraceContext())
	if dl, ok := currentSkyDeadline(); ok {
		return context.WithDeadline(ctx, dl)
	}
	return ctx, func() {}
}

// The http domain's failure code for a call refused by Http.withMinRemaining.
const httpCodeDeadlineTooShort = "DeadlineTooShort"

// httpDeadlineRefusal refuses an outbound call before it is sent: with the
// deadline's DeadlineExceeded error once the Task.timeout deadline has passed,
// and with a DeadlineTooShort error (kind Timeout, domain "http", not
// retryable) when less than minMs milliseconds are left. nil means send it.
func httpDeadlineRefusal(op string, minMs int) any {
	left, ok := skyRemaining()
	if !ok {
		return nil
	}
	if left <= 0 {
		return Err[any, any](deadlineExceededError(op, -1))
	}
	if minMs > 0 && left < time.Duration(minMs)*time.Millisecond {
		leftMs := int(left / time.Millisecond)
		f := DomainFailure{
			Domain:       "http",
			Code:         httpCodeDeadlineTooShort,
			RetryAfterMs: -1,
		}.With("op", op).
			With("minRemainingMs", strconv.Itoa(minMs)).
			With("remainingMs", strconv.Itoa(leftMs))
		msg := fmt.Sprintf("%s: refused before sending, %d ms left before the deadline and the request needs %d ms", op, leftMs, minMs)
		return Err[any, any](ErrDomain(errKindTimeout, msg, f))
	}
	return nil
}

// httpDeadlinePassed turns a transport failure into the deadline's
// DeadlineExceeded error when the Task.timeout deadline has passed (the
// request context stopped the call), and returns nil otherwise.
func httpDeadlinePassed(op string) any {
	if dl, ok := currentSkyDeadline(); ok && !time.Now().Before(dl) {
		return Err[any, any](deadlineExceededError(op, -1))
	}
	return nil
}

// httpClientFor returns a *http.Client that honours per-request
// overrides on top of the shared skyHttpClient transport. timeoutMs<0
// inherits the env default; ==0 disables. followRedirects=false
// returns the first response (resp.Body must still be read & closed).
func httpClientFor(timeoutMs int, followRedirects bool, maxRedirects int) *http.Client {
	timeout := skyHTTPClient().Timeout
	if timeoutMs == 0 {
		timeout = 0
	} else if timeoutMs > 0 {
		timeout = time.Duration(timeoutMs) * time.Millisecond
	}
	check := func(req *http.Request, via []*http.Request) error {
		if !followRedirects {
			return http.ErrUseLastResponse
		}
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		return nil
	}
	return &http.Client{
		Transport:     skyHTTPClient().Transport,
		Timeout:       timeout,
		CheckRedirect: check,
	}
}

// applyHttpHeaders: Sky-side headers can arrive as `[(k, v), ...]`
// (list of tuples — the Elm convention, what users write in the
// record literal), `map[string]any` (legacy), or nil.
func applyHttpHeaders(req *http.Request, headers any) {
	if headers == nil {
		return
	}
	if d, ok := headers.(skyDictAny); ok {
		headers = dictToMapAny(d)
	}
	if hm, ok := headers.(map[string]any); ok {
		for k, v := range hm {
			req.Header.Set(k, fmt.Sprintf("%v", v))
		}
		return
	}
	rv := reflect.ValueOf(headers)
	if rv.Kind() == reflect.Slice {
		for i := 0; i < rv.Len(); i++ {
			item := rv.Index(i).Interface()
			iv := reflect.ValueOf(item)
			if iv.Kind() != reflect.Struct {
				continue
			}
			v0 := iv.FieldByName("V0")
			v1 := iv.FieldByName("V1")
			if v0.IsValid() && v1.IsValid() {
				req.Header.Set(
					fmt.Sprintf("%v", v0.Interface()),
					fmt.Sprintf("%v", v1.Interface()),
				)
			}
		}
	}
}
