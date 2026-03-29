package main

import (
	"fmt"
	"bufio"
	"os"
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

func sky_asString(v any) string { if s, ok := v.(string); ok { return s }; return fmt.Sprintf("%v", v) }

func sky_println(args ...any) any { ss := make([]any, len(args)); for i, a := range args { ss[i] = sky_asString(a) }; fmt.Println(ss...); return struct{}{} }

func sky_asSkyResult(v any) SkyResult { if r, ok := v.(SkyResult); ok { return r }; return SkyResult{} }

func sky_js(v any) any { return v }

func sky_runTask(task any) any { if t, ok := task.(func() any); ok { defer func() { if r := recover(); r != nil { fmt.Fprintf(os.Stderr, "Task panic: %v\n", r); panic(r) } }(); return t() }; if r, ok := task.(SkyResult); ok { return r }; return SkyOk(task) }

func sky_runMainTask(result any) { if _, ok := result.(func() any); ok { r := sky_runTask(result); if sky_asSkyResult(r).Tag == 1 { fmt.Fprintln(os.Stderr, sky_asSkyResult(r).ErrValue); os.Exit(1) } } }

func sky_liveApp(config any) any { return sky_liveAppImpl(config) }

func main() {
	sky_runMainTask(sky_println("Hello from Sky!"))
}
