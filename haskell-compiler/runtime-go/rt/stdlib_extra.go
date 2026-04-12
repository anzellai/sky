package rt

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

// ═══════════════════════════════════════════════════════════
// Sky.Core.Set — backed by map[any]struct{}
// ═══════════════════════════════════════════════════════════

type SkySet struct {
	items map[string]any
}

func Set_empty() any {
	return SkySet{items: map[string]any{}}
}

func Set_fromList(list any) any {
	s := SkySet{items: map[string]any{}}
	for _, v := range asList(list) {
		k := fmt.Sprintf("%v", v)
		s.items[k] = v
	}
	return s
}

func Set_insert(v any, set any) any {
	s := set.(SkySet)
	out := SkySet{items: map[string]any{}}
	for k, v2 := range s.items {
		out.items[k] = v2
	}
	out.items[fmt.Sprintf("%v", v)] = v
	return out
}

func Set_remove(v any, set any) any {
	s := set.(SkySet)
	out := SkySet{items: map[string]any{}}
	k := fmt.Sprintf("%v", v)
	for k2, v2 := range s.items {
		if k2 != k {
			out.items[k2] = v2
		}
	}
	return out
}

func Set_member(v any, set any) any {
	s := set.(SkySet)
	_, ok := s.items[fmt.Sprintf("%v", v)]
	return ok
}

func Set_toList(set any) any {
	s := set.(SkySet)
	out := make([]any, 0, len(s.items))
	for _, v := range s.items {
		out = append(out, v)
	}
	return out
}

func Set_size(set any) any {
	return len(set.(SkySet).items)
}

func Set_union(a any, b any) any {
	out := SkySet{items: map[string]any{}}
	for k, v := range a.(SkySet).items {
		out.items[k] = v
	}
	for k, v := range b.(SkySet).items {
		out.items[k] = v
	}
	return out
}

func Set_intersect(a any, b any) any {
	out := SkySet{items: map[string]any{}}
	bi := b.(SkySet).items
	for k, v := range a.(SkySet).items {
		if _, ok := bi[k]; ok {
			out.items[k] = v
		}
	}
	return out
}

func Set_diff(a any, b any) any {
	out := SkySet{items: map[string]any{}}
	bi := b.(SkySet).items
	for k, v := range a.(SkySet).items {
		if _, ok := bi[k]; !ok {
			out.items[k] = v
		}
	}
	return out
}

// ═══════════════════════════════════════════════════════════
// Sky.Core.Json.Encode — build JSON values
// ═══════════════════════════════════════════════════════════

type JsonValue struct {
	raw any // string | int | float64 | bool | nil | []any | map[string]any
}

func JsonEnc_string(s any) any  { return JsonValue{raw: fmt.Sprintf("%v", s)} }
func JsonEnc_int(n any) any     { return JsonValue{raw: AsInt(n)} }
func JsonEnc_float(n any) any   { return JsonValue{raw: AsFloat(n)} }
func JsonEnc_bool(b any) any    { return JsonValue{raw: b} }
func JsonEnc_null() any         { return JsonValue{raw: nil} }

func JsonEnc_list(items any) any {
	var out []any
	for _, v := range asList(items) {
		if jv, ok := v.(JsonValue); ok {
			out = append(out, jv.raw)
		} else {
			out = append(out, v)
		}
	}
	return JsonValue{raw: out}
}

// object: takes a list of tuples (key, JsonValue)
func JsonEnc_object(pairs any) any {
	m := map[string]any{}
	for _, p := range asList(pairs) {
		// Expect SkyTuple2 { V0: string, V1: JsonValue }
		if t, ok := p.(SkyTuple2); ok {
			key := fmt.Sprintf("%v", t.V0)
			val := t.V1
			if jv, ok := val.(JsonValue); ok {
				m[key] = jv.raw
			} else {
				m[key] = val
			}
		}
	}
	return JsonValue{raw: m}
}

func JsonEnc_encode(indent any, v any) any {
	var val any
	if jv, ok := v.(JsonValue); ok {
		val = jv.raw
	} else {
		val = v
	}
	n := AsInt(indent)
	var b []byte
	var err error
	if n > 0 {
		b, err = json.MarshalIndent(val, "", strings.Repeat(" ", n))
	} else {
		b, err = json.Marshal(val)
	}
	if err != nil {
		return ""
	}
	return string(b)
}

// ═══════════════════════════════════════════════════════════
// Sky.Core.Json.Decode — parse JSON
// ═══════════════════════════════════════════════════════════

type JsonDecoder struct {
	run func(any) any // takes a decoded Go value, returns Result String T
}

func JsonDec_decodeString(decoder any, input any) any {
	s := fmt.Sprintf("%v", input)
	var raw any
	err := json.Unmarshal([]byte(s), &raw)
	if err != nil {
		return Err[any, any]("JSON parse error: " + err.Error())
	}
	d, ok := decoder.(JsonDecoder)
	if !ok {
		return Ok[any, any](raw)
	}
	return d.run(raw)
}

func JsonDec_string() any {
	return JsonDecoder{run: func(v any) any {
		if s, ok := v.(string); ok {
			return Ok[any, any](s)
		}
		return Err[any, any]("expected string")
	}}
}

func JsonDec_int() any {
	return JsonDecoder{run: func(v any) any {
		if f, ok := v.(float64); ok {
			return Ok[any, any](int(f))
		}
		return Err[any, any]("expected int")
	}}
}

func JsonDec_float() any {
	return JsonDecoder{run: func(v any) any {
		if f, ok := v.(float64); ok {
			return Ok[any, any](f)
		}
		return Err[any, any]("expected float")
	}}
}

func JsonDec_bool() any {
	return JsonDecoder{run: func(v any) any {
		if b, ok := v.(bool); ok {
			return Ok[any, any](b)
		}
		return Err[any, any]("expected bool")
	}}
}

func JsonDec_field(name any, inner any) any {
	return JsonDecoder{run: func(v any) any {
		m, ok := v.(map[string]any)
		if !ok {
			return Err[any, any]("expected object")
		}
		fv, exists := m[fmt.Sprintf("%v", name)]
		if !exists {
			return Err[any, any]("missing field: " + fmt.Sprintf("%v", name))
		}
		if d, ok := inner.(JsonDecoder); ok {
			return d.run(fv)
		}
		return Ok[any, any](fv)
	}}
}

func JsonDec_list(inner any) any {
	return JsonDecoder{run: func(v any) any {
		arr, ok := v.([]any)
		if !ok {
			return Err[any, any]("expected array")
		}
		out := make([]any, 0, len(arr))
		for _, item := range arr {
			if d, ok := inner.(JsonDecoder); ok {
				r := d.run(item)
				if sr, ok := r.(SkyResult[any, any]); ok {
					if sr.Tag != 0 {
						return r
					}
					out = append(out, sr.OkValue)
				}
			} else {
				out = append(out, item)
			}
		}
		return Ok[any, any](out)
	}}
}

func JsonDec_map(fn any, inner any) any {
	return JsonDecoder{run: func(v any) any {
		if d, ok := inner.(JsonDecoder); ok {
			r := d.run(v)
			if sr, ok := r.(SkyResult[any, any]); ok {
				if sr.Tag != 0 {
					return r
				}
				f := fn.(func(any) any)
				return Ok[any, any](f(sr.OkValue))
			}
		}
		return Err[any, any]("decode error")
	}}
}

func JsonDec_andThen(fn any, inner any) any {
	return JsonDecoder{run: func(v any) any {
		if d, ok := inner.(JsonDecoder); ok {
			r := d.run(v)
			if sr, ok := r.(SkyResult[any, any]); ok {
				if sr.Tag != 0 {
					return r
				}
				f := fn.(func(any) any)
				nextDec := f(sr.OkValue)
				if nd, ok := nextDec.(JsonDecoder); ok {
					return nd.run(v)
				}
			}
		}
		return Err[any, any]("decode error")
	}}
}

func JsonDec_succeed(v any) any {
	return JsonDecoder{run: func(_ any) any {
		return Ok[any, any](v)
	}}
}

func JsonDec_fail(msg any) any {
	m := fmt.Sprintf("%v", msg)
	return JsonDecoder{run: func(_ any) any {
		return Err[any, any](m)
	}}
}

// ═══════════════════════════════════════════════════════════
// Sky.Core.Json.Decode.Pipeline (NoRedInk-style applicative pipelines)
// ═══════════════════════════════════════════════════════════

// Pipeline.required : String -> JsonDecoder a -> JsonDecoder (a -> b) -> JsonDecoder b
// Applies (decoder for field name) to a function decoder.
func JsonDecP_required(name any, inner any, fnDec any) any {
	return JsonDecoder{run: func(v any) any {
		// Run fnDec to get a function
		fd, ok := fnDec.(JsonDecoder)
		if !ok {
			return Err[any, any]("pipeline: fn decoder required")
		}
		fnR := fd.run(v)
		fnSr, ok := fnR.(SkyResult[any, any])
		if !ok || fnSr.Tag != 0 {
			return fnR
		}
		// Extract field
		m, ok := v.(map[string]any)
		if !ok {
			return Err[any, any]("pipeline.required: expected object")
		}
		fv, exists := m[fmt.Sprintf("%v", name)]
		if !exists {
			return Err[any, any]("pipeline.required: missing field " + fmt.Sprintf("%v", name))
		}
		innerDec, ok := inner.(JsonDecoder)
		if !ok {
			return Err[any, any]("pipeline.required: invalid inner decoder")
		}
		innerR := innerDec.run(fv)
		innerSr, ok := innerR.(SkyResult[any, any])
		if !ok || innerSr.Tag != 0 {
			return innerR
		}
		// Apply function
		return Ok[any, any](pipelineApply(fnSr.OkValue, innerSr.OkValue))
	}}
}

// Pipeline.optional : String -> JsonDecoder a -> a -> JsonDecoder (a -> b) -> JsonDecoder b
func JsonDecP_optional(name any, inner any, def any, fnDec any) any {
	return JsonDecoder{run: func(v any) any {
		fd, ok := fnDec.(JsonDecoder)
		if !ok {
			return Err[any, any]("pipeline: fn decoder required")
		}
		fnR := fd.run(v)
		fnSr, ok := fnR.(SkyResult[any, any])
		if !ok || fnSr.Tag != 0 {
			return fnR
		}
		var val any = def
		if m, ok := v.(map[string]any); ok {
			if fv, exists := m[fmt.Sprintf("%v", name)]; exists {
				if innerDec, ok := inner.(JsonDecoder); ok {
					innerR := innerDec.run(fv)
					if innerSr, ok := innerR.(SkyResult[any, any]); ok && innerSr.Tag == 0 {
						val = innerSr.OkValue
					}
				}
			}
		}
		return Ok[any, any](pipelineApply(fnSr.OkValue, val))
	}}
}

// Pipeline.custom : JsonDecoder a -> JsonDecoder (a -> b) -> JsonDecoder b
func JsonDecP_custom(inner any, fnDec any) any {
	return JsonDecoder{run: func(v any) any {
		fd, ok := fnDec.(JsonDecoder)
		if !ok {
			return Err[any, any]("pipeline: fn decoder required")
		}
		fnR := fd.run(v)
		fnSr, ok := fnR.(SkyResult[any, any])
		if !ok || fnSr.Tag != 0 {
			return fnR
		}
		innerDec, ok := inner.(JsonDecoder)
		if !ok {
			return Err[any, any]("pipeline.custom: invalid inner")
		}
		innerR := innerDec.run(v)
		innerSr, ok := innerR.(SkyResult[any, any])
		if !ok || innerSr.Tag != 0 {
			return innerR
		}
		return Ok[any, any](pipelineApply(fnSr.OkValue, innerSr.OkValue))
	}}
}

// Pipeline.requiredAt : List String -> JsonDecoder a -> JsonDecoder (a -> b) -> JsonDecoder b
func JsonDecP_requiredAt(path any, inner any, fnDec any) any {
	return JsonDecoder{run: func(v any) any {
		fd, ok := fnDec.(JsonDecoder)
		if !ok {
			return Err[any, any]("pipeline: fn decoder required")
		}
		fnR := fd.run(v)
		fnSr, ok := fnR.(SkyResult[any, any])
		if !ok || fnSr.Tag != 0 {
			return fnR
		}
		cur := v
		for _, seg := range asList(path) {
			m, ok := cur.(map[string]any)
			if !ok {
				return Err[any, any]("pipeline.requiredAt: expected object at " + fmt.Sprintf("%v", seg))
			}
			fv, exists := m[fmt.Sprintf("%v", seg)]
			if !exists {
				return Err[any, any]("pipeline.requiredAt: missing " + fmt.Sprintf("%v", seg))
			}
			cur = fv
		}
		innerDec, ok := inner.(JsonDecoder)
		if !ok {
			return Err[any, any]("pipeline.requiredAt: invalid inner")
		}
		innerR := innerDec.run(cur)
		innerSr, ok := innerR.(SkyResult[any, any])
		if !ok || innerSr.Tag != 0 {
			return innerR
		}
		return Ok[any, any](pipelineApply(fnSr.OkValue, innerSr.OkValue))
	}}
}

// pipelineApply: apply an accumulator to one more argument.
// Accumulators in elm-style pipelines start as a multi-arg function and are
// progressively applied one field at a time. The function may be a Go
// func(any) any or func(any, any, ...) any — we dispatch via reflect.
// Returns either the next partially-applied function or the final value.
func pipelineApply(acc any, arg any) any {
	if acc == nil {
		return nil
	}
	// 1-arg curried function — fast path
	if f, ok := acc.(func(any) any); ok {
		return f(arg)
	}
	// Multi-arg Go function via reflect: take arg and produce a partial
	rv := reflect.ValueOf(acc)
	if rv.Kind() != reflect.Func {
		return acc
	}
	ft := rv.Type()
	n := ft.NumIn()
	if n == 0 {
		return acc
	}
	if n == 1 {
		out := rv.Call([]reflect.Value{reflect.ValueOf(arg)})
		if len(out) > 0 {
			return out[0].Interface()
		}
		return nil
	}
	// n >= 2: partially apply — return a new func(any) any that captures arg
	// and takes the remaining n-1 args one at a time.
	applied := []any{arg}
	var build func([]any) any
	build = func(collected []any) any {
		if len(collected) == n {
			vs := make([]reflect.Value, n)
			for i, a := range collected {
				if a == nil {
					vs[i] = reflect.Zero(ft.In(i))
				} else {
					vs[i] = reflect.ValueOf(a)
				}
			}
			out := rv.Call(vs)
			if len(out) > 0 {
				return out[0].Interface()
			}
			return nil
		}
		return func(next any) any {
			return build(append(collected, next))
		}
	}
	return build(applied)
}

// ═══════════════════════════════════════════════════════════
// Sky.Core.Path
// ═══════════════════════════════════════════════════════════

func Path_join(parts any) any {
	ps := asList(parts)
	segs := make([]string, len(ps))
	for i, p := range ps {
		segs[i] = fmt.Sprintf("%v", p)
	}
	return filepath.Join(segs...)
}

func Path_dir(p any) any  { return filepath.Dir(fmt.Sprintf("%v", p)) }
func Path_base(p any) any { return filepath.Base(fmt.Sprintf("%v", p)) }
func Path_ext(p any) any  { return filepath.Ext(fmt.Sprintf("%v", p)) }
func Path_isAbsolute(p any) any {
	return filepath.IsAbs(fmt.Sprintf("%v", p))
}

// ═══════════════════════════════════════════════════════════
// Sky.Core.Http — client
// ═══════════════════════════════════════════════════════════

// HttpResponse is a record-style struct for returning results
type HttpResponse struct {
	Status  int
	Body    string
	Headers map[string]string
}

// HTTP client safety defaults. Each outbound request gets these limits so
// a hostile or misconfigured server can't hang a Sky process forever.
// Users can bring their own *http.Client via Http.request when they need
// custom limits.
var skyHttpClient = newSkyHttpClient()

func newSkyHttpClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
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

// Http.get : String -> Task String HttpResponse
func Http_get(url any) any {
	u := fmt.Sprintf("%v", url)
	return func() any {
		resp, err := skyHttpClient.Get(u)
		if err != nil {
			return Err[any, any]("http.get: " + err.Error())
		}
		body, err := readBoundedBody(resp.Body)
		if err != nil {
			return Err[any, any]("http.get read: " + err.Error())
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
	}
}

// Http.post : String -> String -> Task String HttpResponse
// (url, body)
func Http_post(url any, body any) any {
	u := fmt.Sprintf("%v", url)
	b := fmt.Sprintf("%v", body)
	return func() any {
		resp, err := skyHttpClient.Post(u, "application/json", strings.NewReader(b))
		if err != nil {
			return Err[any, any]("http.post: " + err.Error())
		}
		rb, err := readBoundedBody(resp.Body)
		if err != nil {
			return Err[any, any]("http.post read: " + err.Error())
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

// Http.request : String -> String -> String -> Dict String String -> Task String HttpResponse
// (method, url, body, headers)
func Http_request(method any, url any, body any, headers any) any {
	m := fmt.Sprintf("%v", method)
	u := fmt.Sprintf("%v", url)
	b := fmt.Sprintf("%v", body)
	return func() any {
		req, err := http.NewRequest(m, u, strings.NewReader(b))
		if err != nil {
			return Err[any, any]("http.request: " + err.Error())
		}
		if hm, ok := headers.(map[string]any); ok {
			for k, v := range hm {
				req.Header.Set(k, fmt.Sprintf("%v", v))
			}
		}
		resp, err := skyHttpClient.Do(req)
		if err != nil {
			return Err[any, any]("http.request do: " + err.Error())
		}
		rb, err := readBoundedBody(resp.Body)
		if err != nil {
			return Err[any, any]("http.request read: " + err.Error())
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

// Keep encoding/json referenced
var _ = json.Marshal
