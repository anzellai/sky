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
	"unicode/utf8"

	"github.com/rivo/uniseg"
	"golang.org/x/text/unicode/norm"
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

// JsonEnc.list may be called as:
//   Encode.list items                   -- 1-arg form (legacy)
//   Encode.list Encode.string [...]     -- 2-arg (Elm style: map each item)
// The variadic-args signature accommodates both.
func JsonEnc_list(args ...any) any {
	switch len(args) {
	case 1:
		var out []any
		for _, v := range asList(args[0]) {
			if jv, ok := v.(JsonValue); ok {
				out = append(out, jv.raw)
			} else {
				out = append(out, v)
			}
		}
		return JsonValue{raw: out}
	case 2:
		fn := args[0]
		items := asList(args[1])
		var out []any
		for _, v := range items {
			var mapped any
			if f, ok := fn.(func(any) any); ok {
				mapped = f(v)
			} else {
				mapped = v
			}
			if jv, ok := mapped.(JsonValue); ok {
				out = append(out, jv.raw)
			} else {
				out = append(out, mapped)
			}
		}
		return JsonValue{raw: out}
	default:
		return JsonValue{raw: []any{}}
	}
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

// JsonDec.oneOf : List (JsonDecoder a) -> JsonDecoder a
// Tries each decoder in order, returns first Ok or composite Err.
func JsonDec_oneOf(decoders any) any {
	return JsonDecoder{run: func(v any) any {
		for _, d := range asList(decoders) {
			if dd, ok := d.(JsonDecoder); ok {
				r := dd.run(v)
				if sr, ok := r.(SkyResult[any, any]); ok && sr.Tag == 0 {
					return r
				}
			}
		}
		return Err[any, any]("oneOf: no decoder matched")
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

// JsonDec.at : List String -> JsonDecoder a -> JsonDecoder a
// Drill into a nested path before applying the inner decoder.
func JsonDec_at(path any, inner any) any {
	return JsonDecoder{run: func(v any) any {
		cur := v
		for _, seg := range asList(path) {
			m, ok := cur.(map[string]any)
			if !ok {
				return Err[any, any]("at: expected object at " + fmt.Sprintf("%v", seg))
			}
			fv, exists := m[fmt.Sprintf("%v", seg)]
			if !exists {
				return Err[any, any]("at: missing " + fmt.Sprintf("%v", seg))
			}
			cur = fv
		}
		if d, ok := inner.(JsonDecoder); ok {
			return d.run(cur)
		}
		return Ok[any, any](cur)
	}}
}

// JsonDec.map2..map5 — apply a function to N decoded results.
func JsonDec_map2(fn, d1, d2 any) any {
	return JsonDecoder{run: func(v any) any {
		a := runDec(d1, v); if isErr(a) { return a }
		b := runDec(d2, v); if isErr(b) { return b }
		return Ok[any, any](apply2(fn, okVal(a), okVal(b)))
	}}
}

func JsonDec_map3(fn, d1, d2, d3 any) any {
	return JsonDecoder{run: func(v any) any {
		a := runDec(d1, v); if isErr(a) { return a }
		b := runDec(d2, v); if isErr(b) { return b }
		c := runDec(d3, v); if isErr(c) { return c }
		return Ok[any, any](apply3(fn, okVal(a), okVal(b), okVal(c)))
	}}
}

func JsonDec_map4(fn, d1, d2, d3, d4 any) any {
	return JsonDecoder{run: func(v any) any {
		a := runDec(d1, v); if isErr(a) { return a }
		b := runDec(d2, v); if isErr(b) { return b }
		c := runDec(d3, v); if isErr(c) { return c }
		d := runDec(d4, v); if isErr(d) { return d }
		return Ok[any, any](apply4(fn, okVal(a), okVal(b), okVal(c), okVal(d)))
	}}
}

func JsonDec_map5(fn, d1, d2, d3, d4, d5 any) any {
	return JsonDecoder{run: func(v any) any {
		a := runDec(d1, v); if isErr(a) { return a }
		b := runDec(d2, v); if isErr(b) { return b }
		c := runDec(d3, v); if isErr(c) { return c }
		d := runDec(d4, v); if isErr(d) { return d }
		e := runDec(d5, v); if isErr(e) { return e }
		return Ok[any, any](apply5(fn, okVal(a), okVal(b), okVal(c), okVal(d), okVal(e)))
	}}
}

// Helpers for decoder mapN.
func runDec(d, v any) any {
	dd, ok := d.(JsonDecoder)
	if !ok {
		return Err[any, any]("expected decoder")
	}
	return dd.run(v)
}

func isErr(r any) bool {
	sr, ok := r.(SkyResult[any, any])
	return ok && sr.Tag != 0
}

func okVal(r any) any {
	sr, _ := r.(SkyResult[any, any])
	return sr.OkValue
}

// applyN uses pipelineApply from validate.go to support both curried
// func(any) any and uncurried multi-arg fn signatures.
func apply2(f, a, b any) any       { return pipelineApply(pipelineApply(f, a), b) }
func apply3(f, a, b, c any) any    { return pipelineApply(apply2(f, a, b), c) }
func apply4(f, a, b, c, d any) any { return pipelineApply(apply3(f, a, b, c), d) }
func apply5(f, a, b, c, d, e any) any {
	return pipelineApply(apply4(f, a, b, c, d), e)
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

// P8/Path typed companions — direct string in, string/bool out.
func Path_dirT(p string) string        { return filepath.Dir(p) }
func Path_baseT(p string) string       { return filepath.Base(p) }
func Path_extT(p string) string        { return filepath.Ext(p) }
func Path_isAbsoluteT(p string) bool   { return filepath.IsAbs(p) }
func Path_joinT(parts []string) string { return filepath.Join(parts...) }

// Path.safeJoin : String -> String -> Result String String
// (root, relative) — joins `root` and `relative`, cleans the result, and
// returns Err if the resulting path escapes `root` (e.g. via "../../etc/passwd").
// Use when constructing filesystem paths from user-supplied input to prevent
// directory-traversal attacks.
func Path_safeJoin(root any, rel any) any {
	rootStr := fmt.Sprintf("%v", root)
	relStr := fmt.Sprintf("%v", rel)

	// Resolve the root to its absolute canonical form (does not follow
	// symlinks — that would require os.Stat which may fail for non-existent
	// paths; strict lexical comparison is sufficient here).
	absRoot, err := filepath.Abs(rootStr)
	if err != nil {
		return Err[any, any]("safeJoin: " + err.Error())
	}
	absRoot = filepath.Clean(absRoot)

	joined := filepath.Clean(filepath.Join(absRoot, relStr))

	// Require joined to be absRoot or a descendant of absRoot.
	if joined == absRoot {
		return Ok[any, any](joined)
	}
	sep := string(filepath.Separator)
	if !strings.HasPrefix(joined, absRoot+sep) {
		return Err[any, any]("safeJoin: path escapes root")
	}
	return Ok[any, any](joined)
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

// ═══════════════════════════════════════════════════════════
// Sky.Core.String — Unicode-correct helpers
// ═══════════════════════════════════════════════════════════

// String.isValid : String -> Bool
// Returns True iff s is valid UTF-8. Use before passing a string from an
// untrusted source (bytes off the wire, file contents, etc.) to other
// Unicode-aware functions.
func String_isValid(s any) any {
	return utf8.ValidString(fmt.Sprintf("%v", s))
}

// String.normalize : String -> String
// Unicode Normalization Form C (NFC) — the canonical form recommended by W3C
// for web/HTML content. Ensures that visually-identical strings compare equal
// byte-wise, e.g. "café" composed (U+00E9) vs decomposed (e + U+0301) both
// normalize to the composed form.
func String_normalize(s any) any {
	return norm.NFC.String(fmt.Sprintf("%v", s))
}

// String.normalizeNFD : String -> String (decomposed form — useful for diacritic-insensitive search)
func String_normalizeNFD(s any) any {
	return norm.NFD.String(fmt.Sprintf("%v", s))
}

// String.casefold : String -> String
// Unicode-aware case folding for comparison. Unlike toLower this handles
// locale-independent equivalences like German ß ↔ "ss" approximations and
// Turkic dotless-i ↔ i. We use simple case folding (full Unicode casing is
// locale-dependent; simple is language-neutral).
// NOTE: Go's standard library has strings.ToLower / unicode.SimpleFold.
// For real internationalised comparison use strings.EqualFold via this helper.
func String_casefold(s any) any {
	// strings.ToLower performs Unicode-aware lowercasing which is the closest
	// stdlib approximation to simple case folding for comparison purposes.
	return strings.ToLower(fmt.Sprintf("%v", s))
}

// String.equalFold : String -> String -> Bool
// Case-insensitive Unicode-aware string equality.
func String_equalFold(a any, b any) any {
	return strings.EqualFold(fmt.Sprintf("%v", a), fmt.Sprintf("%v", b))
}

// String.graphemes : String -> Int
// Returns the number of extended grapheme clusters per UAX #29 — what a user
// perceives as "one character". Differs from rune count for combining marks,
// emoji ZWJ sequences, regional indicators, etc.
// E.g. "👨‍👩‍👧" (family emoji) = 5 code points, 1 grapheme.
//      "é" composed = 1 rune = 1 grapheme.
//      "é" decomposed (e + ◌́) = 2 runes = 1 grapheme.
//      "🇬🇧" (UK flag) = 2 regional-indicator code points = 1 grapheme.
func String_graphemes(s any) any {
	return uniseg.GraphemeClusterCount(fmt.Sprintf("%v", s))
}

// String.trimStart : String -> String (Unicode whitespace)
func String_trimStart(s any) any {
	return strings.TrimLeftFunc(fmt.Sprintf("%v", s), unicodeIsSpace)
}

// String.trimEnd : String -> String
func String_trimEnd(s any) any {
	return strings.TrimRightFunc(fmt.Sprintf("%v", s), unicodeIsSpace)
}

// unicodeIsSpace wraps unicode.IsSpace for use with strings.TrimXxxFunc.
func unicodeIsSpace(r rune) bool {
	// Use unicode.IsSpace which covers all Unicode whitespace (includes NBSP,
	// ideographic space, zero-width space, etc.)
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' ||
		r == '\v' || r == '\f' || r == 0xA0 ||
		(r >= 0x2000 && r <= 0x200A) ||
		r == 0x2028 || r == 0x2029 || r == 0x3000 || r == 0xFEFF
}
