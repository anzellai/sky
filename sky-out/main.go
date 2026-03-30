package main

import (
	"fmt"
	"bufio"
	"os"
	"strconv"
	"strings"
	encoding_json "encoding/json"
)

var skyVersion = "dev"

type SkyTuple2 struct { V0, V1 any }

type SkyTuple3 struct { V0, V1, V2 any }

type SkyResult struct { Tag int; SkyName string; OkValue, ErrValue any }

type SkyMaybe struct { Tag int; SkyName string; JustValue any }

var stdinReader *bufio.Reader
var sky_jsonDecoder_string = func(v any) any { if s, ok := v.(string); ok { return SkyOk(s) }; return SkyErr("expected string") }
var sky_jsonDecoder_int = func(v any) any { switch n := v.(type) { case float64: return SkyOk(int(n)); case int: return SkyOk(n) }; return SkyErr("expected int") }
var sky_jsonDecoder_float = func(v any) any { if f, ok := v.(float64); ok { return SkyOk(f) }; return SkyErr("expected float") }
var sky_jsonDecoder_bool = func(v any) any { if b, ok := v.(bool); ok { return SkyOk(b) }; return SkyErr("expected bool") }
var sky_liveAppImpl = func(config any) any { return config }

func SkyOk(v any) SkyResult { return SkyResult{Tag: 0, SkyName: "Ok", OkValue: v} }

func SkyErr(v any) SkyResult { return SkyResult{Tag: 1, SkyName: "Err", ErrValue: v} }

func sky_asInt(v any) int { switch x := v.(type) { case int: return x; case float64: return int(x); default: return 0 } }

func sky_asString(v any) string { if s, ok := v.(string); ok { return s }; return fmt.Sprintf("%v", v) }

func sky_asBool(v any) bool { if b, ok := v.(bool); ok { return b }; return false }

func sky_asList(v any) []any { if l, ok := v.([]any); ok { return l }; return []any{} }

func sky_asMap(v any) map[string]any { if m, ok := v.(map[string]any); ok { return m }; return map[string]any{} }

func sky_concat(a, b any) any { if la, ok := a.([]any); ok { if lb, ok := b.([]any); ok { return append(la, lb...) } }; return sky_asString(a) + sky_asString(b) }

func sky_stringFromInt(v any) any { return strconv.Itoa(sky_asInt(v)) }

func sky_stringJoin(sep any) any { return func(list any) any { parts := sky_asList(list); ss := make([]string, len(parts)); for i, p := range parts { ss[i] = sky_asString(p) }; return strings.Join(ss, sky_asString(sep)) } }

func sky_listMap(fn any) any { return func(list any) any { items := sky_asList(list); result := make([]any, len(items)); for i, item := range items { result[i] = fn.(func(any) any)(item) }; return result } }

func sky_println(args ...any) any { ss := make([]any, len(args)); for i, a := range args { ss[i] = sky_asString(a) }; fmt.Println(ss...); return struct{}{} }

func sky_asSkyResult(v any) SkyResult { if r, ok := v.(SkyResult); ok { return r }; return SkyResult{} }

func sky_asTuple2(v any) SkyTuple2 { if t, ok := v.(SkyTuple2); ok { return t }; return SkyTuple2{} }

func sky_not(v any) any { return !sky_asBool(v) }

func sky_js(v any) any { if s, ok := v.(string); ok && s == "nil" { return nil }; return v }

func sky_call(f any, arg any) any { if fn, ok := f.(func(any) any); ok { return fn(arg) }; if s, ok := f.(string); ok { if args, ok := arg.([]any); ok { parts := make([]string, len(args)); for i, a := range args { parts[i] = sky_asString(a) }; return s + "(" + strings.Join(parts, ", ") + ")" }; return s + " " + sky_asString(arg) }; panic(fmt.Sprintf("sky_call: cannot call %T", f)) }

func sky_runTask(task any) any { if t, ok := task.(func() any); ok { var result any; func() { defer func() { if r := recover(); r != nil { result = SkyErr(fmt.Sprintf("panic: %v", r)) } }(); result = t() }(); return result }; if r, ok := task.(SkyResult); ok { return r }; return SkyOk(task) }

func sky_runMainTask(result any) { if _, ok := result.(func() any); ok { r := sky_runTask(result); if sky_asSkyResult(r).Tag == 1 { fmt.Fprintln(os.Stderr, sky_asSkyResult(r).ErrValue); os.Exit(1) } } }

func sky_resultMap(fn any) any { return func(r any) any { res := sky_asSkyResult(r); if res.Tag == 0 { return SkyOk(fn.(func(any) any)(res.OkValue)) }; return r } }

func sky_resultWithDefault(def any) any { return func(r any) any { res := sky_asSkyResult(r); if res.Tag == 0 { return res.OkValue }; return def } }

func sky_jsonEncString(v any) any { return sky_asString(v) }

func sky_jsonEncInt(v any) any { return sky_asInt(v) }

func sky_jsonEncBool(v any) any { return sky_asBool(v) }

func sky_jsonEncList(encoder any) any { return func(list any) any { items := sky_asList(list); result := make([]any, len(items)); for i, item := range items { result[i] = encoder.(func(any) any)(item) }; return result } }

func sky_jsonEncObject(pairs any) any { m := make(map[string]any); for _, p := range sky_asList(pairs) { t := sky_asTuple2(p); m[sky_asString(t.V0)] = t.V1 }; return m }

func sky_jsonEncode(indent any) any { return func(value any) any { var b []byte; var err error; n := sky_asInt(indent); if n > 0 { b, err = encoding_json.MarshalIndent(value, "", strings.Repeat(" ", n)) } else { b, err = encoding_json.Marshal(value) }; if err != nil { return "null" }; return string(b) } }

func sky_jsonDecString(decoder any) any { return func(jsonStr any) any { var v any; if err := encoding_json.Unmarshal([]byte(sky_asString(jsonStr)), &v); err != nil { return SkyErr(err.Error()) }; return decoder.(func(any) any)(v) } }

func sky_jsonDecField(key any) any { return func(decoder any) any { return func(v any) any { m, ok := v.(map[string]any); if !ok { return SkyErr("expected object") }; val, exists := m[sky_asString(key)]; if !exists { return SkyErr("field '" + sky_asString(key) + "' not found") }; return decoder.(func(any) any)(val) } } }

func sky_jsonDecList(decoder any) any { return func(v any) any { arr, ok := v.([]any); if !ok { return SkyErr("expected array") }; result := make([]any, 0, len(arr)); for _, item := range arr { r := decoder.(func(any) any)(item); res := sky_asSkyResult(r); if res.Tag == 1 { return r }; result = append(result, res.OkValue) }; return SkyOk(result) } }

func sky_jsonDecMap(fn any) any { return func(decoder any) any { return func(v any) any { r := decoder.(func(any) any)(v); res := sky_asSkyResult(r); if res.Tag == 1 { return r }; return SkyOk(fn.(func(any) any)(res.OkValue)) } } }

func sky_jsonDecMap2(fn any) any { return func(d1 any) any { return func(d2 any) any { return func(v any) any { r1 := d1.(func(any) any)(v); res1 := sky_asSkyResult(r1); if res1.Tag == 1 { return r1 }; r2 := d2.(func(any) any)(v); res2 := sky_asSkyResult(r2); if res2.Tag == 1 { return r2 }; return SkyOk(fn.(func(any) any)(res1.OkValue).(func(any) any)(res2.OkValue)) } } } }

func sky_jsonDecSucceed(v any) any { return func(_ any) any { return SkyOk(v) } }

func sky_jsonDecOneOf(decoders any) any { return func(v any) any { for _, d := range sky_asList(decoders) { r := d.(func(any) any)(v); if sky_asSkyResult(r).Tag == 0 { return r } }; return SkyErr("none of the decoders matched") } }

func sky_jsonDecAt(path any) any { return func(decoder any) any { return func(v any) any { current := v; for _, key := range sky_asList(path) { m, ok := current.(map[string]any); if !ok { return SkyErr("expected object at path") }; val, exists := m[sky_asString(key)]; if !exists { return SkyErr("key not found: " + sky_asString(key)) }; current = val }; return decoder.(func(any) any)(current) } } }

func sky_jsonPipeRequired(key any) any { return func(decoder any) any { return func(pipeline any) any { return func(v any) any { pr := pipeline.(func(any) any)(v); pres := sky_asSkyResult(pr); if pres.Tag == 1 { return pr }; m, ok := v.(map[string]any); if !ok { return SkyErr("expected object") }; val, exists := m[sky_asString(key)]; if !exists { return SkyErr("field '" + sky_asString(key) + "' required") }; fr := decoder.(func(any) any)(val); fres := sky_asSkyResult(fr); if fres.Tag == 1 { return fr }; return SkyOk(pres.OkValue.(func(any) any)(fres.OkValue)) } } } }

func sky_jsonPipeOptional(key any) any { return func(decoder any) any { return func(def any) any { return func(pipeline any) any { return func(v any) any { pr := pipeline.(func(any) any)(v); pres := sky_asSkyResult(pr); if pres.Tag == 1 { return pr }; m, ok := v.(map[string]any); if !ok { return SkyOk(pres.OkValue.(func(any) any)(def)) }; val, exists := m[sky_asString(key)]; if !exists { return SkyOk(pres.OkValue.(func(any) any)(def)) }; fr := decoder.(func(any) any)(val); fres := sky_asSkyResult(fr); if fres.Tag == 1 { return SkyOk(pres.OkValue.(func(any) any)(def)) }; return SkyOk(pres.OkValue.(func(any) any)(fres.OkValue)) } } } } }

func sky_liveApp(config any) any { return sky_liveAppImpl(config) }

func encodeSimple() any {
	return func() any { val := sky_jsonEncObject([]any{SkyTuple2{V0: "name", V1: sky_jsonEncString("Alice")}, SkyTuple2{V0: "age", V1: sky_jsonEncInt(30)}, SkyTuple2{V0: "active", V1: sky_jsonEncBool(true)}}); _ = val; return sky_call(sky_jsonEncode(2), val) }()
}

func encodeComplex() any {
	return func() any { val := sky_jsonEncObject([]any{SkyTuple2{V0: "user", V1: sky_jsonEncObject([]any{SkyTuple2{V0: "name", V1: sky_jsonEncString("Bob")}, SkyTuple2{V0: "email", V1: sky_jsonEncString("bob@example.com")}, SkyTuple2{V0: "age", V1: sky_jsonEncInt(25)}})}, SkyTuple2{V0: "tags", V1: sky_call(sky_jsonEncList(sky_jsonEncString), []any{"admin", "premium"})}, SkyTuple2{V0: "scores", V1: sky_call(sky_jsonEncList(sky_jsonEncInt), []any{95, 87, 92})}}); _ = val; return sky_call(sky_jsonEncode(2), val) }()
}

func simpleDecoder() any {
	return sky_call(sky_call(sky_jsonDecMap2(func(n any) any { return func(a any) any { return sky_concat(n, sky_concat(", age ", sky_stringFromInt(a))) } }), sky_call(sky_jsonDecField("name"), sky_jsonDecoder_string)), sky_call(sky_jsonDecField("age"), sky_jsonDecoder_int))
}

func decodeSimpleExample() any {
	return sky_call(sky_resultWithDefault("decode error"), sky_call(sky_jsonDecString(simpleDecoder()), "{\"name\":\"Charlie\",\"age\":28}"))
}

func userDecoder() any {
	return sky_call(sky_call(sky_jsonPipeRequired("verified"), sky_jsonDecoder_bool), sky_call(sky_call(sky_jsonPipeRequired("age"), sky_jsonDecoder_int), sky_call(sky_call(sky_jsonPipeRequired("email"), sky_jsonDecoder_string), sky_call(sky_call(sky_jsonPipeRequired("name"), sky_jsonDecoder_string), sky_jsonDecSucceed(func(name any) any { return func(email any) any { return func(age any) any { return func(verified any) any { return map[string]any{"name": name, "email": email, "age": age, "verified": verified} } } } })))))
}

func pipelineExample() any {
	return func() any { result := sky_call(sky_jsonDecString(userDecoder()), "{\"name\":\"Diana\",\"email\":\"diana@example.com\",\"age\":32,\"verified\":true}"); _ = result; return sky_call(sky_resultWithDefault("pipeline error"), sky_call(sky_resultMap(func(u any) any { return sky_concat(sky_asMap(u)["name"], sky_concat(" (", sky_concat(sky_asMap(u)["email"], ")"))) }), result)) }()
}

func profileDecoder() any {
	return sky_call(sky_call(sky_call(sky_jsonPipeOptional("followers"), sky_jsonDecoder_int), 0), sky_call(sky_call(sky_call(sky_jsonPipeOptional("bio"), sky_jsonDecoder_string), "No bio provided"), sky_call(sky_call(sky_jsonPipeRequired("username"), sky_jsonDecoder_string), sky_jsonDecSucceed(func(username any) any { return func(bio any) any { return func(followers any) any { return map[string]any{"username": username, "bio": bio, "followers": followers} } } }))))
}

func optionalExample() any {
	return func() any { result := sky_call(sky_jsonDecString(profileDecoder()), "{\"username\":\"skydev\",\"followers\":42}"); _ = result; return sky_call(sky_resultWithDefault("profile error"), sky_call(sky_resultMap(func(p any) any { return sky_concat("@", sky_concat(sky_asMap(p)["username"], sky_concat(", bio: ", sky_concat(sky_asMap(p)["bio"], sky_concat(", followers: ", sky_stringFromInt(sky_asMap(p)["followers"])))))) }), result)) }()
}

func nestedDecoder() any {
	return sky_call(sky_call(sky_jsonDecMap2(func(n any) any { return func(c any) any { return sky_concat(n, sky_concat(" from ", c)) } }), sky_call(sky_jsonDecAt([]any{"user", "profile", "name"}), sky_jsonDecoder_string)), sky_call(sky_jsonDecAt([]any{"user", "profile", "city"}), sky_jsonDecoder_string))
}

func nestedExample() any {
	return sky_call(sky_resultWithDefault("nested error"), sky_call(sky_jsonDecString(nestedDecoder()), "{\"user\":{\"profile\":{\"name\":\"Eve\",\"city\":\"London\"}}}"))
}

func todoDecoder() any {
	return sky_call(sky_call(sky_jsonPipeRequired("done"), sky_jsonDecoder_bool), sky_call(sky_call(sky_jsonPipeRequired("title"), sky_jsonDecoder_string), sky_call(sky_call(sky_jsonPipeRequired("id"), sky_jsonDecoder_int), sky_jsonDecSucceed(func(id any) any { return func(title any) any { return func(done any) any { return map[string]any{"id": id, "title": title, "done": done} } } }))))
}

func todoListDecoder() any {
	return sky_jsonDecList(todoDecoder())
}

func listExample() any {
	return func() any { result := sky_call(sky_jsonDecString(todoListDecoder()), "[{\"id\":1,\"title\":\"Buy milk\",\"done\":false},{\"id\":2,\"title\":\"Write Sky\",\"done\":true}]"); _ = result; return sky_call(sky_resultWithDefault("list error"), sky_call(sky_resultMap(func(todos any) any { return sky_call(sky_stringJoin(", "), sky_call(sky_listMap(func(t any) any { return sky_asMap(t)["title"] }), todos)) }), result)) }()
}

func roundtrip() any {
	return func() any { original := sky_jsonEncObject([]any{SkyTuple2{V0: "items", V1: sky_call(sky_jsonEncList(func(n any) any { return sky_jsonEncObject([]any{SkyTuple2{V0: "name", V1: sky_jsonEncString(n)}}) }), []any{"Alice", "Bob", "Charlie"})}, SkyTuple2{V0: "count", V1: sky_jsonEncInt(3)}}); _ = original; jsonStr := sky_call(sky_jsonEncode(0), original); _ = jsonStr; decoder := sky_call(sky_call(sky_jsonDecMap2(func(c any) any { return func(ns any) any { return sky_concat("Count: ", sky_concat(sky_stringFromInt(c), sky_concat(", Names: ", sky_call(sky_stringJoin(", "), ns)))) } }), sky_call(sky_jsonDecField("count"), sky_jsonDecoder_int)), sky_call(sky_jsonDecField("items"), sky_jsonDecList(sky_call(sky_jsonDecField("name"), sky_jsonDecoder_string)))); _ = decoder; result := sky_call(sky_jsonDecString(decoder), jsonStr); _ = result; return sky_call(sky_resultWithDefault("roundtrip error"), result) }()
}

func flexibleDecoder() any {
	return sky_call(sky_jsonDecField("value"), sky_jsonDecOneOf([]any{sky_call(sky_jsonDecMap(sky_stringFromInt), sky_jsonDecoder_int), sky_jsonDecoder_string}))
}

func flexibleExample() any {
	return func() any { r1 := sky_call(sky_resultWithDefault("?"), sky_call(sky_jsonDecString(flexibleDecoder()), "{\"value\":42}")); _ = r1; r2 := sky_call(sky_resultWithDefault("?"), sky_call(sky_jsonDecString(flexibleDecoder()), "{\"value\":\"hello\"}")); _ = r2; return sky_concat(r1, sky_concat(" and ", r2)) }()
}

func main() {
	sky_runMainTask(func() any { sky_println("=== Sky JSON Examples (Elm-compatible API) ==="); sky_println(""); sky_println("1. Simple encoding:"); sky_println(encodeSimple()); sky_println(""); sky_println("2. Complex encoding:"); sky_println(encodeComplex()); sky_println(""); sky_println("3. Simple decoding (map2):"); sky_println(decodeSimpleExample()); sky_println(""); sky_println("4. Pipeline decoding:"); sky_println(pipelineExample()); sky_println(""); sky_println("5. Optional fields:"); sky_println(optionalExample()); sky_println(""); sky_println("6. Nested at:"); sky_println(nestedExample()); sky_println(""); sky_println("7. List of objects:"); sky_println(listExample()); sky_println(""); sky_println("8. Roundtrip:"); sky_println(roundtrip()); sky_println(""); sky_println("9. oneOf (flexible):"); sky_println(flexibleExample()); return sky_println("Done!") }())
}
