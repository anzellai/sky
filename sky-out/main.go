package main

import (
	"fmt"
	"bufio"
	"io"
	"os"
	net_http "net/http"
	"strconv"
	"strings"
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

func SkyJust(v any) SkyMaybe { return SkyMaybe{Tag: 0, SkyName: "Just", JustValue: v} }

func SkyNothing() SkyMaybe { return SkyMaybe{Tag: 1, SkyName: "Nothing"} }

func sky_asInt(v any) int { switch x := v.(type) { case int: return x; case float64: return int(x); default: return 0 } }

func sky_asFloat(v any) float64 { switch x := v.(type) { case float64: return x; case int: return float64(x); default: return 0 } }

func sky_asString(v any) string { if s, ok := v.(string); ok { return s }; return fmt.Sprintf("%v", v) }

func sky_asBool(v any) bool { if b, ok := v.(bool); ok { return b }; return false }

func sky_asList(v any) []any { if l, ok := v.([]any); ok { return l }; return []any{} }

func sky_numBinop(op string, a, b any) any { af, aIsF := a.(float64); bf, bIsF := b.(float64); if aIsF || bIsF { if !aIsF { af = sky_asFloat(a) }; if !bIsF { bf = sky_asFloat(b) }; switch op { case "+": return af + bf; case "-": return af - bf; case "*": return af * bf; case "%": return int(af) % int(bf) }; return af + bf }; ai, bi := sky_asInt(a), sky_asInt(b); switch op { case "+": return ai + bi; case "-": return ai - bi; case "*": return ai * bi; case "%": return ai % bi }; return ai + bi }

func sky_asMap(v any) map[string]any { if m, ok := v.(map[string]any); ok { return m }; return map[string]any{} }

func sky_concat(a, b any) any { if la, ok := a.([]any); ok { if lb, ok := b.([]any); ok { return append(la, lb...) } }; return sky_asString(a) + sky_asString(b) }

func sky_stringFromInt(v any) any { return strconv.Itoa(sky_asInt(v)) }

func sky_stringToInt(s any) any { n, err := strconv.Atoi(strings.TrimSpace(sky_asString(s))); if err != nil { return SkyNothing() }; return SkyJust(n) }

func sky_stringJoin(sep any) any { return func(list any) any { parts := sky_asList(list); ss := make([]string, len(parts)); for i, p := range parts { ss[i] = sky_asString(p) }; return strings.Join(ss, sky_asString(sep)) } }

func sky_println(args ...any) any { ss := make([]any, len(args)); for i, a := range args { ss[i] = sky_asString(a) }; fmt.Println(ss...); return struct{}{} }

func sky_asSkyResult(v any) SkyResult { if r, ok := v.(SkyResult); ok { return r }; return SkyResult{} }

func sky_asSkyMaybe(v any) SkyMaybe { if m, ok := v.(SkyMaybe); ok { return m }; return SkyMaybe{Tag: 1, SkyName: "Nothing"} }

func sky_asTuple2(v any) SkyTuple2 { if t, ok := v.(SkyTuple2); ok { return t }; return SkyTuple2{} }

func sky_not(v any) any { return !sky_asBool(v) }

func sky_js(v any) any { if s, ok := v.(string); ok && s == "nil" { return nil }; return v }

func sky_call(f any, arg any) any { if fn, ok := f.(func(any) any); ok { return fn(arg) }; if s, ok := f.(string); ok { if args, ok := arg.([]any); ok { parts := make([]string, len(args)); for i, a := range args { parts[i] = sky_asString(a) }; return s + "(" + strings.Join(parts, ", ") + ")" }; return s + " " + sky_asString(arg) }; panic(fmt.Sprintf("sky_call: cannot call %T", f)) }

func sky_taskSucceed(value any) any { return func() any { return SkyOk(value) } }

func sky_runTask(task any) any { if t, ok := task.(func() any); ok { var result any; func() { defer func() { if r := recover(); r != nil { result = SkyErr(fmt.Sprintf("panic: %v", r)) } }(); result = t() }(); return result }; if r, ok := task.(SkyResult); ok { return r }; return SkyOk(task) }

func sky_runMainTask(result any) { if _, ok := result.(func() any); ok { r := sky_runTask(result); if sky_asSkyResult(r).Tag == 1 { fmt.Fprintln(os.Stderr, sky_asSkyResult(r).ErrValue); os.Exit(1) } } }

func sky_serverListen(port any) any { return func(routes any) any { return func() any { mux := sky_buildMux(sky_asList(routes), ""); p := sky_asInt(port); if ep := os.Getenv("SKY_PORT"); ep != "" { if pv, err := strconv.Atoi(ep); err == nil { p = pv } }; if ep := os.Getenv("PORT"); ep != "" { if pv, err := strconv.Atoi(ep); err == nil { p = pv } }; addr := fmt.Sprintf(":%d", p); fmt.Fprintf(os.Stderr, "Sky server listening on %s\n", addr); err := net_http.ListenAndServe(addr, mux); if err != nil { return SkyErr(err.Error()) }; return SkyOk(struct{}{}) } } }

func sky_serverGet(pattern any) any { return func(handler any) any { return map[string]any{"SkyName": "RouteEntry", "V0": "GET", "V1": pattern, "V2": handler} } }

func sky_serverPost(pattern any) any { return func(handler any) any { return map[string]any{"SkyName": "RouteEntry", "V0": "POST", "V1": pattern, "V2": handler} } }

func sky_serverText(body any) any { return map[string]any{"status": 200, "body": body, "headers": []any{SkyTuple2{"Content-Type", "text/plain; charset=utf-8"}}, "cookies": []any{}} }

func sky_serverHtml(body any) any { return map[string]any{"status": 200, "body": body, "headers": []any{SkyTuple2{"Content-Type", "text/html; charset=utf-8"}}, "cookies": []any{}} }

func sky_serverJson(body any) any { return map[string]any{"status": 200, "body": body, "headers": []any{SkyTuple2{"Content-Type", "application/json"}}, "cookies": []any{}} }

func sky_serverRedirect(url any) any { return map[string]any{"status": 302, "body": "", "headers": []any{SkyTuple2{"Location", url}}, "cookies": []any{}} }

func sky_serverWithHeader(key any) any { return func(val any) any { return func(resp any) any { m := sky_asMap(resp); result := make(map[string]any); for k, v := range m { result[k] = v }; hdrs := sky_asList(m["headers"]); result["headers"] = append(hdrs, SkyTuple2{key, val}); return result } } }

func sky_serverWithCookie(name any) any { return func(value any) any { return func(options any) any { return func(resp any) any { m := sky_asMap(resp); result := make(map[string]any); for k, v := range m { result[k] = v }; cookies := sky_asList(m["cookies"]); cookie := map[string]any{"name": name, "value": value, "path": "/", "maxAge": 86400, "httpOnly": true, "secure": false}; result["cookies"] = append(cookies, cookie); return result } } } }

func sky_serverParam(name any) any { return func(req any) any { m := sky_asMap(req); path := sky_asString(m["path"]); params := sky_asList(m["params"]); for _, p := range params { t := sky_asTuple2(p); if sky_asString(t.V0) == sky_asString(name) { return SkyJust(t.V1) } }; return sky_extractPathParam(sky_asString(name), path) } }

func sky_extractPathParam(name string, path string) any { return SkyNothing() }

func sky_serverGetCookie(name any) any { return func(req any) any { m := sky_asMap(req); cookies := sky_asList(m["cookies"]); for _, c := range cookies { t := sky_asTuple2(c); if sky_asString(t.V0) == sky_asString(name) { return SkyJust(t.V1) } }; return SkyNothing() } }

func sky_serverCookie(name any) any { return func(val any) any { return map[string]any{"name": name, "value": val, "path": "/", "maxAge": 86400, "httpOnly": true, "secure": false, "sameSite": "lax"} } }

func sky_buildMux(routes []any, prefix string) *net_http.ServeMux { mux := net_http.NewServeMux(); for _, r := range routes { rm := sky_asMap(r); if rm == nil { continue }; skyName, _ := rm["SkyName"].(string); switch skyName { case "RouteEntry": method := sky_asString(rm["V0"]); pattern := prefix + sky_asString(rm["V1"]); handler := rm["V2"]; muxPattern := pattern; if method != "*" { muxPattern = method + " " + pattern }; mux.HandleFunc(muxPattern, sky_makeHandler(handler)); case "RouteGroup": groupPrefix := prefix + sky_asString(rm["V0"]); groupRoutes := sky_asList(rm["V1"]); subMux := sky_buildMux(groupRoutes, groupPrefix); mux.Handle(groupPrefix+"/", subMux); case "RouteStatic": urlPrefix := prefix + sky_asString(rm["V0"]); dirPath := sky_asString(rm["V1"]); fs := net_http.FileServer(net_http.Dir(dirPath)); mux.Handle(urlPrefix+"/", net_http.StripPrefix(urlPrefix, fs)) } }; return mux }

func sky_makeHandler(handler any) func(net_http.ResponseWriter, *net_http.Request) { return func(w net_http.ResponseWriter, r *net_http.Request) { defer func() { if rec := recover(); rec != nil { net_http.Error(w, fmt.Sprintf("Internal Server Error: %v", rec), 500); fmt.Fprintf(os.Stderr, "%s %s 500 (panic: %v)\n", r.Method, r.URL.Path, rec) } }(); skyReq := sky_buildRequest(r); fn, ok := handler.(func(any) any); if !ok { net_http.Error(w, "Invalid handler", 500); return }; taskResult := fn(skyReq); var skyResp any; if thunk, ok := taskResult.(func() any); ok { skyResp = thunk() } else if result, ok := taskResult.(SkyResult); ok { skyResp = result } else { skyResp = SkyOk(taskResult) }; result, ok2 := skyResp.(SkyResult); if !ok2 { sky_writeResponse(w, sky_asMap(skyResp)); return }; if result.Tag == 1 { net_http.Error(w, sky_asString(result.ErrValue), 500); return }; sky_writeResponse(w, sky_asMap(result.OkValue)) } }

func sky_buildRequest(r *net_http.Request) map[string]any { body, _ := io.ReadAll(r.Body); r.Body = io.NopCloser(strings.NewReader(string(body))); _ = r.ParseForm(); formValues := make([]any, 0); for k, vs := range r.Form { for _, v := range vs { formValues = append(formValues, SkyTuple2{k, v}) } }; headers := make([]any, 0); for k, vs := range r.Header { for _, v := range vs { headers = append(headers, SkyTuple2{k, v}) } }; cookies := make([]any, 0); for _, c := range r.Cookies() { cookies = append(cookies, SkyTuple2{c.Name, c.Value}) }; query := make([]any, 0); for k, vs := range r.URL.Query() { for _, v := range vs { query = append(query, SkyTuple2{k, v}) } }; params := make([]any, 0); protocol := "http"; if r.TLS != nil { protocol = "https" }; return map[string]any{"method": r.Method, "path": r.URL.Path, "body": string(body), "headers": headers, "params": params, "query": query, "cookies": cookies, "formValues": formValues, "remoteAddr": r.RemoteAddr, "host": r.Host, "protocol": protocol} }

func sky_writeResponse(w net_http.ResponseWriter, resp map[string]any) { if resp == nil { w.WriteHeader(200); return }; if cookies, ok := resp["cookies"].([]any); ok { for _, c := range cookies { cm := sky_asMap(c); if cm == nil { continue }; net_http.SetCookie(w, &net_http.Cookie{Name: sky_asString(cm["name"]), Value: sky_asString(cm["value"]), Path: sky_asString(cm["path"]), MaxAge: sky_asInt(cm["maxAge"]), HttpOnly: sky_asBool(cm["httpOnly"]), Secure: sky_asBool(cm["secure"])}) } }; if headers, ok := resp["headers"].([]any); ok { for _, h := range headers { if t, ok := h.(SkyTuple2); ok { w.Header().Set(sky_asString(t.V0), sky_asString(t.V1)) } } }; status := sky_asInt(resp["status"]); if status == 0 { status = 200 }; w.WriteHeader(status); fmt.Fprint(w, sky_asString(resp["body"])) }

func sky_liveApp(config any) any { return sky_liveAppImpl(config) }

func main() {
	sky_runMainTask(sky_call(sky_serverListen(8080), []any{sky_call(sky_serverGet("/"), handleHome), sky_call(sky_serverGet("/hello/:name"), handleHello), sky_call(sky_serverGet("/api/status"), handleStatus), sky_call(sky_serverPost("/api/echo"), handleEcho), sky_call(sky_serverGet("/cookie-demo"), handleCookieDemo), sky_call(sky_serverGet("/redirect"), handleRedirect)}))
}

func handleHome(req any) any {
	return sky_taskSucceed(sky_serverHtml(sky_call(sky_stringJoin(""), []any{"<h1>Sky HTTP Server</h1>", "<ul>", "<li><a href='/hello/world'>Hello World</a></li>", "<li><a href='/api/status'>API Status</a></li>", "<li><a href='/cookie-demo'>Cookie Demo</a></li>", "<li><a href='/redirect'>Redirect</a></li>", "</ul>"})))
}

func handleHello(req any) any {
	return func() any { name := func() any { return func() any { __subject := sky_call(sky_serverParam("name"), req); if sky_asSkyMaybe(__subject).SkyName == "Just" { n := sky_asSkyMaybe(__subject).JustValue; _ = n; return n };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "stranger" };  panic("non-exhaustive case expression") }() }(); _ = name; return sky_taskSucceed(sky_serverText(sky_concat("Hello, ", sky_concat(name, "!")))) }()
}

func handleStatus(req any) any {
	return sky_taskSucceed(sky_serverJson("{\"status\":\"ok\",\"server\":\"Sky\"}"))
}

func handleEcho(req any) any {
	return sky_taskSucceed(sky_call(sky_call(sky_serverWithHeader("X-Echo"), "true"), sky_serverJson(sky_asMap(req)["body"])))
}

func handleCookieDemo(req any) any {
	return func() any { visitCount := func() any { return func() any { __subject := sky_call(sky_serverGetCookie("visits"), req); if sky_asSkyMaybe(__subject).SkyName == "Just" { v := sky_asSkyMaybe(__subject).JustValue; _ = v; return func() any { return func() any { __subject := sky_stringToInt(v); if sky_asSkyMaybe(__subject).SkyName == "Just" { n := sky_asSkyMaybe(__subject).JustValue; _ = n; return sky_numBinop("+", n, 1) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return 1 };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return 1 };  panic("non-exhaustive case expression") }() }() };  panic("non-exhaustive case expression") }() }(); _ = visitCount; visitCookie := sky_call(sky_serverCookie("visits"), sky_stringFromInt(visitCount)); _ = visitCookie; return sky_taskSucceed(sky_call(sky_serverWithCookie(visitCookie), sky_serverHtml(sky_concat("<h1>Visit #", sky_concat(sky_stringFromInt(visitCount), "</h1><p>Refresh to increment!</p>"))))) }()
}

func handleRedirect(req any) any {
	return sky_taskSucceed(sky_serverRedirect("/hello/redirected"))
}
