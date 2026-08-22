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
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
)

// skyHTTPClient — the shared outbound client, built ONCE on first use (see the
// note that was in stdlib_extra.go: first use is necessarily after every
// init(), so a .env SKY_HTTP_CLIENT_TIMEOUT has been applied by then).
var skyHTTPClientOnce = sync.OnceValue(newSkyHttpClient)

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
		resp, err := skyHTTPClient().Get(url)
		if err != nil {
			return Err[string, HttpResponse]("http.get: " + err.Error())
		}
		body, err := readBoundedBody(resp.Body)
		if err != nil {
			return Err[string, HttpResponse]("http.get read: " + err.Error())
		}
		hdrs := map[string]string{}
		for k, v := range resp.Header {
			if len(v) > 0 {
				hdrs[k] = v[0]
			}
		}
		return Ok[string, HttpResponse](HttpResponse{
			Status:  resp.StatusCode,
			Body:    body,
			Headers: hdrs,
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
		req, err := http.NewRequest(method, url, strings.NewReader(body))
		if err != nil {
			return Err[any, any](ErrNetwork("http.request: " + err.Error()))
		}
		applyHttpHeaders(req, headers)
		client := skyHTTPClient()
		if timeoutMs >= 0 || !followRedirects || maxRedirects != 10 {
			client = httpClientFor(timeoutMs, followRedirects, maxRedirects)
		}
		resp, err := client.Do(req)
		if err != nil {
			return Err[any, any](ErrNetwork("http.request do: " + err.Error()))
		}
		rb, err := readBoundedBody(resp.Body)
		if err != nil {
			return Err[any, any](ErrNetwork("http.request read: " + err.Error()))
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
	}
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
