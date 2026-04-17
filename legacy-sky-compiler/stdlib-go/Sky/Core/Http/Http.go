package sky_sky_core_http

import (
	"fmt"
	"io"
	"net/http"
	"strings"
)

type SkyResult struct {
	Tag      int
	SkyName  string
	OkValue  any
	ErrValue any
}
type SkyTuple2 struct{ V0, V1 any }

func ok(v any) SkyResult  { return SkyResult{Tag: 0, SkyName: "Ok", OkValue: v} }
func err(v any) SkyResult { return SkyResult{Tag: 1, SkyName: "Err", ErrValue: v} }

// Get performs a GET request. Returns Task (thunk).
func Get(url any) any {
	return func() any {
		defer func() {
			if r := recover(); r != nil {
				// swallow panic
			}
		}()
		resp, e := http.Get(asString(url))
		if e != nil {
			return err(e.Error())
		}
		defer resp.Body.Close()
		body, e := io.ReadAll(resp.Body)
		if e != nil {
			return err(e.Error())
		}
		return ok(makeResponse(resp, string(body)))
	}
}

// Post performs a POST request. Returns Task (thunk).
func Post(url, body any) any {
	return func() any {
		defer func() {
			if r := recover(); r != nil {
				// swallow panic
			}
		}()
		resp, e := http.Post(asString(url), "application/json", strings.NewReader(asString(body)))
		if e != nil {
			return err(e.Error())
		}
		defer resp.Body.Close()
		respBody, e := io.ReadAll(resp.Body)
		if e != nil {
			return err(e.Error())
		}
		return ok(makeResponse(resp, string(respBody)))
	}
}

// Request performs a full HTTP request. Returns Task (thunk).
func Request(opts any) any {
	return func() any {
		defer func() {
			if r := recover(); r != nil {
				// swallow panic
			}
		}()
		m := asMap(opts)
		method := asString(m["method"])
		url := asString(m["url"])
		reqBody := asString(m["body"])

		req, e := http.NewRequest(method, url, strings.NewReader(reqBody))
		if e != nil {
			return err(e.Error())
		}

		// Set headers from List (String, String)
		headers := asList(m["headers"])
		for _, h := range headers {
			t := asTuple(h)
			req.Header.Set(asString(t.V0), asString(t.V1))
		}

		client := &http.Client{}
		resp, e := client.Do(req)
		if e != nil {
			return err(e.Error())
		}
		defer resp.Body.Close()
		body, e := io.ReadAll(resp.Body)
		if e != nil {
			return err(e.Error())
		}
		return ok(makeResponse(resp, string(body)))
	}
}

func makeResponse(resp *http.Response, body string) map[string]any {
	headers := make([]any, 0)
	for k, vs := range resp.Header {
		for _, v := range vs {
			headers = append(headers, SkyTuple2{V0: k, V1: v})
		}
	}
	return map[string]any{
		"status":  resp.StatusCode,
		"body":    body,
		"headers": headers,
	}
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}
func asList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}
func asTuple(v any) SkyTuple2 {
	if t, ok := v.(SkyTuple2); ok {
		return t
	}
	return SkyTuple2{}
}
