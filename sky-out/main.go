package main

import (
	"fmt"
	"bufio"
	"io"
	"os"
	exec "os/exec"
	net_http "net/http"
	"strconv"
	"strings"
	"sort"
	"math"
	crypto_sha256 "crypto/sha256"
	crypto_md5 "crypto/md5"
	hex "encoding/hex"
	base64 "encoding/base64"
	encoding_json "encoding/json"
	"time"
	"context"
)

var skyVersion = "dev"

type SkyTuple2 struct { V0, V1 any }

type SkyTuple3 struct { V0, V1, V2 any }

type SkyResult struct { Tag int; SkyName string; OkValue, ErrValue any }

type SkyMaybe struct { Tag int; SkyName string; JustValue any }

func SkyOk(v any) SkyResult { return SkyResult{Tag: 0, SkyName: "Ok", OkValue: v} }

func SkyErr(v any) SkyResult { return SkyResult{Tag: 1, SkyName: "Err", ErrValue: v} }

func SkyJust(v any) SkyMaybe { return SkyMaybe{Tag: 0, SkyName: "Just", JustValue: v} }

func SkyNothing() SkyMaybe { return SkyMaybe{Tag: 1, SkyName: "Nothing"} }

func sky_asInt(v any) int { switch x := v.(type) { case int: return x; case float64: return int(x); default: return 0 } }

func sky_asFloat(v any) float64 { switch x := v.(type) { case float64: return x; case int: return float64(x); default: return 0 } }

func sky_asString(v any) string { if s, ok := v.(string); ok { return s }; return fmt.Sprintf("%v", v) }

func sky_asBool(v any) bool { if b, ok := v.(bool); ok { return b }; return false }

func sky_asList(v any) []any { if l, ok := v.([]any); ok { return l }; return nil }

func sky_asBytes(v any) []byte { if b, ok := v.([]byte); ok { return b }; if s, ok := v.(string); ok { return []byte(s) }; return nil }

func sky_asError(v any) error { if e, ok := v.(error); ok { return e }; return fmt.Errorf("%v", v) }

func sky_asStringSlice(v any) []string { items := sky_asList(v); result := make([]string, len(items)); for i, item := range items { result[i] = sky_asString(item) }; return result }

func sky_asFixedBytes(v any) []byte { if b, ok := v.([]byte); ok { return b }; return nil }

func sky_stringToBytes(s any) any { return []byte(sky_asString(s)) }

func sky_stringFromBytes(b any) any { return string(sky_asBytes(b)) }

func sky_asMapStringAny(v any) map[string]interface{} { if m, ok := v.(map[string]interface{}); ok { return m }; return sky_asMap(v) }

func sky_asMapStringString(v any) map[string]string { if m, ok := v.(map[string]string); ok { return m }; result := make(map[string]string); for k, val := range sky_asMap(v) { result[sky_asString(k)] = sky_asString(val) }; return result }

func sky_asContext(v any) context.Context { if c, ok := v.(context.Context); ok { return c }; return context.Background() }

var _ = context.Background

func sky_asFloat32(v any) float32 { return float32(sky_asFloat(v)) }

func sky_asInt64(v any) int64 { return int64(sky_asInt(v)) }

func sky_asHttpHandler(v any) func(net_http.ResponseWriter, *net_http.Request) { fn := v.(func(any) any); return func(w net_http.ResponseWriter, r *net_http.Request) { fn(w).(func(any) any)(r) } }

func sky_asUint(v any) uint { return uint(sky_asInt(v)) }

func sky_asUint8(v any) uint8 { return uint8(sky_asInt(v)) }

func sky_asUint16(v any) uint16 { return uint16(sky_asInt(v)) }

func sky_asUint32(v any) uint32 { return uint32(sky_asInt(v)) }

func sky_asUint64(v any) uint64 { return uint64(sky_asInt(v)) }

func sky_asMap(v any) map[string]any { if m, ok := v.(map[string]any); ok { return m }; return nil }

func sky_equal(a, b any) bool { return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b) }

func sky_concat(a, b any) any { if la, ok := a.([]any); ok { if lb, ok := b.([]any); ok { return append(la, lb...) } }; return sky_asString(a) + sky_asString(b) }

func sky_stringFromInt(v any) any { return strconv.Itoa(sky_asInt(v)) }

func sky_stringFromFloat(v any) any { return strconv.FormatFloat(sky_asFloat(v), 'f', -1, 64) }

func sky_stringToUpper(v any) any { return strings.ToUpper(sky_asString(v)) }

func sky_stringToLower(v any) any { return strings.ToLower(sky_asString(v)) }

func sky_stringLength(v any) any { return len(sky_asString(v)) }

func sky_stringTrim(v any) any { return strings.TrimSpace(sky_asString(v)) }

func sky_stringContains(sub any) any { return func(s any) any { return strings.Contains(sky_asString(s), sky_asString(sub)) } }

func sky_stringIndexOf(needle any) any { return func(haystack any) any { return strings.Index(sky_asString(haystack), sky_asString(needle)) } }

func sky_jsonExtractBracketed(s any) any { str := sky_asString(s); depth := 0; inStr := false; esc := false; for i := 0; i < len(str); i++ { c := str[i]; if esc { esc = false; continue }; if c == '\\' && inStr { esc = true; continue }; if c == '"' { inStr = !inStr; continue }; if inStr { continue }; if c == '[' || c == '{' { depth++ } else if c == ']' || c == '}' { depth--; if depth == 0 { return str[:i+1] } } }; return str }

func sky_jsonSplitArray(s any) any { str := strings.TrimSpace(sky_asString(s)); if len(str) < 2 { return []any{} }; inner := strings.TrimSpace(str[1:len(str)-1]); if len(inner) == 0 { return []any{} }; var result []any; depth := 0; start := 0; inStr := false; esc := false; for i := 0; i < len(inner); i++ { c := inner[i]; if esc { esc = false; continue }; if c == '\\' && inStr { esc = true; continue }; if c == '"' { inStr = !inStr; continue }; if inStr { continue }; if c == '{' || c == '[' { depth++ } else if c == '}' || c == ']' { depth-- } else if c == ',' && depth == 0 { elem := strings.TrimSpace(inner[start:i]); if len(elem) > 0 { result = append(result, elem) }; start = i + 1 } }; last := strings.TrimSpace(inner[start:]); if len(last) > 0 { result = append(result, last) }; if result == nil { return []any{} }; return result }

func sky_filterSkyiByUsage(skyiSource any) any { return func(alias any) any { return func(sourceText any) any { src := sky_asString(skyiSource); al := sky_asString(alias); srcTxt := sky_asString(sourceText); lines := strings.Split(src, "\n"); var header, types, usedFuncs []string; inHeader := true; var curBlock []string; for _, line := range lines { if inHeader { if strings.HasPrefix(line, "module ") || strings.HasPrefix(line, "import ") || strings.HasPrefix(line, "foreign ") || strings.TrimSpace(line) == "" { header = append(header, line); continue } else { inHeader = false } }; if strings.HasPrefix(line, "type ") { if len(curBlock) > 0 { name := strings.SplitN(curBlock[0], " ", 2)[0]; if strings.Contains(srcTxt, al+"."+name) { usedFuncs = append(usedFuncs, curBlock...) }; curBlock = nil }; types = append(types, line); continue }; if strings.Contains(line, " : ") && !strings.HasPrefix(line, " ") && strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "type ") { if len(curBlock) > 0 { name := strings.SplitN(curBlock[0], " ", 2)[0]; if strings.Contains(srcTxt, al+"."+name) { usedFuncs = append(usedFuncs, curBlock...) }; curBlock = nil }; curBlock = append(curBlock, line) } else if len(curBlock) > 0 { curBlock = append(curBlock, line) } }; if len(curBlock) > 0 { name := strings.SplitN(curBlock[0], " ", 2)[0]; if strings.Contains(srcTxt, al+"."+name) { usedFuncs = append(usedFuncs, curBlock...) } }; result := append(header, ""); result = append(result, types...); result = append(result, ""); result = append(result, usedFuncs...); return strings.Join(result, "\n") } } }

func sky_stringStartsWith(prefix any) any { return func(s any) any { return strings.HasPrefix(sky_asString(s), sky_asString(prefix)) } }

func sky_stringEndsWith(suffix any) any { return func(s any) any { return strings.HasSuffix(sky_asString(s), sky_asString(suffix)) } }

func sky_stringSplit(sep any) any { return func(s any) any { parts := strings.Split(sky_asString(s), sky_asString(sep)); result := make([]any, len(parts)); for i, p := range parts { result[i] = p }; return result } }

func sky_stringReplace(old any) any { return func(new_ any) any { return func(s any) any { return strings.ReplaceAll(sky_asString(s), sky_asString(old), sky_asString(new_)) } } }

func sky_stringToInt(s any) any { n, err := strconv.Atoi(strings.TrimSpace(sky_asString(s))); if err != nil { return SkyNothing() }; return SkyJust(n) }

func sky_stringToFloat(s any) any { f, err := strconv.ParseFloat(strings.TrimSpace(sky_asString(s)), 64); if err != nil { return SkyNothing() }; return SkyJust(f) }

func sky_stringAppend(a any) any { return func(b any) any { return sky_asString(a) + sky_asString(b) } }

func sky_stringIsEmpty(v any) any { return sky_asString(v) == "" }

func sky_stringSlice(start any) any { return func(end any) any { return func(s any) any { str := sky_asString(s); return str[sky_asInt(start):sky_asInt(end)] } } }

func sky_stringJoin(sep any) any { return func(list any) any { parts := sky_asList(list); ss := make([]string, len(parts)); for i, p := range parts { ss[i] = sky_asString(p) }; return strings.Join(ss, sky_asString(sep)) } }

func sky_listMap(fn any) any { return func(list any) any { items := sky_asList(list); result := make([]any, len(items)); for i, item := range items { result[i] = fn.(func(any) any)(item) }; return result } }

func sky_listFilter(fn any) any { return func(list any) any { items := sky_asList(list); var result []any; for _, item := range items { if sky_asBool(fn.(func(any) any)(item)) { result = append(result, item) } }; return result } }

func sky_listFoldl(fn any) any { return func(init any) any { return func(list any) any { acc := init; for _, item := range sky_asList(list) { acc = fn.(func(any) any)(item).(func(any) any)(acc) }; return acc } } }

func sky_listFoldr(fn any) any { return func(init any) any { return func(list any) any { items := sky_asList(list); acc := init; for i := len(items) - 1; i >= 0; i-- { acc = fn.(func(any) any)(items[i]).(func(any) any)(acc) }; return acc } } }

func sky_listLength(list any) any { return len(sky_asList(list)) }

func sky_listHead(list any) any { items := sky_asList(list); if len(items) > 0 { return SkyJust(items[0]) }; return SkyNothing() }

func sky_listReverse(list any) any { items := sky_asList(list); result := make([]any, len(items)); for i, item := range items { result[len(items)-1-i] = item }; return result }

func sky_listIsEmpty(list any) any { return len(sky_asList(list)) == 0 }

func sky_listAppend(a any) any { return func(b any) any { return append(sky_asList(a), sky_asList(b)...) } }

func sky_listConcatMap(fn any) any { return func(list any) any { var result []any; for _, item := range sky_asList(list) { result = append(result, sky_asList(fn.(func(any) any)(item))...) }; if result == nil { return []any{} }; return result } }

func sky_listConcat(lists any) any { var result []any; for _, l := range sky_asList(lists) { result = append(result, sky_asList(l)...) }; if result == nil { return []any{} }; return result }

func sky_listFilterMap(fn any) any { return func(list any) any { var result []any; for _, item := range sky_asList(list) { r := fn.(func(any) any)(item); if m, ok := r.(SkyMaybe); ok && m.Tag == 0 { result = append(result, m.JustValue) } }; if result == nil { return []any{} }; return result } }

func sky_listIndexedMap(fn any) any { return func(list any) any { items := sky_asList(list); result := make([]any, len(items)); for i, item := range items { result[i] = fn.(func(any) any)(i).(func(any) any)(item) }; return result } }

func sky_listDrop(n any) any { return func(list any) any { items := sky_asList(list); c := sky_asInt(n); if c >= len(items) { return []any{} }; return items[c:] } }

func sky_listMember(item any) any { return func(list any) any { for _, x := range sky_asList(list) { if sky_equal(x, item) { return true } }; return false } }

func sky_recordUpdate(base any, updates any) any { m := sky_asMap(base); result := make(map[string]any); for k, v := range m { result[k] = v }; for k, v := range sky_asMap(updates) { result[k] = v }; return result }

func sky_println(args ...any) any { ss := make([]any, len(args)); for i, a := range args { ss[i] = sky_asString(a) }; fmt.Println(ss...); return struct{}{} }

func sky_exit(code any) any { os.Exit(sky_asInt(code)); return struct{}{} }

func sky_asSkyResult(v any) SkyResult { if r, ok := v.(SkyResult); ok { return r }; return SkyResult{} }

func sky_asSkyMaybe(v any) SkyMaybe { if m, ok := v.(SkyMaybe); ok { return m }; return SkyMaybe{Tag: 1, SkyName: "Nothing"} }

func sky_asTuple2(v any) SkyTuple2 { if t, ok := v.(SkyTuple2); ok { return t }; return SkyTuple2{} }

func sky_asTuple3(v any) SkyTuple3 { if t, ok := v.(SkyTuple3); ok { return t }; return SkyTuple3{} }

func sky_not(v any) any { return !sky_asBool(v) }

func sky_fileRead(path any) any { data, err := os.ReadFile(sky_asString(path)); if err != nil { return SkyErr(err.Error()) }; return SkyOk(string(data)) }

func sky_fileWrite(path any) any { return func(content any) any { err := os.WriteFile(sky_asString(path), []byte(sky_asString(content)), 0644); if err != nil { return SkyErr(err.Error()) }; return SkyOk(struct{}{}) } }

func sky_fileMkdirAll(path any) any { err := os.MkdirAll(sky_asString(path), 0755); if err != nil { return SkyErr(err.Error()) }; return SkyOk(struct{}{}) }

func sky_processRun(cmd any) any { return func(args any) any { argStrs := sky_asList(args); cmdArgs := make([]string, len(argStrs)); for i, a := range argStrs { cmdArgs[i] = sky_asString(a) }; out, err := exec.Command(sky_asString(cmd), cmdArgs...).CombinedOutput(); if err != nil { return SkyErr(err.Error() + ": " + string(out)) }; return SkyOk(string(out)) } }

func sky_processExit(code any) any { os.Exit(sky_asInt(code)); return struct{}{} }

func sky_processGetArgs(u any) any { args := make([]any, len(os.Args)); for i, a := range os.Args { args[i] = a }; return args }

func sky_processGetArg(n any) any { idx := sky_asInt(n); if idx < len(os.Args) { return SkyJust(os.Args[idx]) }; return SkyNothing() }

func sky_refNew(v any) any { return &SkyRef{Value: v} }

type SkyRef struct { Value any }

func sky_refGet(r any) any { return r.(*SkyRef).Value }

func sky_refSet(v any) any { return func(r any) any { r.(*SkyRef).Value = v; return struct{}{} } }

func sky_dictEmpty() any { return map[string]any{} }

func sky_dictInsert(k any) any { return func(v any) any { return func(d any) any { m := sky_asMap(d); result := make(map[string]any, len(m)+1); for key, val := range m { result[key] = val }; result[sky_asString(k)] = v; return result } } }

func sky_dictGet(k any) any { return func(d any) any { m := sky_asMap(d); if v, ok := m[sky_asString(k)]; ok { return SkyJust(v) }; return SkyNothing() } }

func sky_dictKeys(d any) any { m := sky_asMap(d); keys := make([]any, 0, len(m)); for k := range m { keys = append(keys, k) }; return keys }

func sky_dictValues(d any) any { m := sky_asMap(d); vals := make([]any, 0, len(m)); for _, v := range m { vals = append(vals, v) }; return vals }

func sky_dictToList(d any) any { m := sky_asMap(d); pairs := make([]any, 0, len(m)); for k, v := range m { pairs = append(pairs, SkyTuple2{k, v}) }; return pairs }

func sky_dictFromList(list any) any { result := make(map[string]any); for _, item := range sky_asList(list) { t := sky_asTuple2(item); result[sky_asString(t.V0)] = t.V1 }; return result }

func sky_dictMap(fn any) any { return func(d any) any { m := sky_asMap(d); result := make(map[string]any, len(m)); for k, v := range m { result[k] = fn.(func(any) any)(k).(func(any) any)(v) }; return result } }

func sky_dictFoldl(fn any) any { return func(init any) any { return func(d any) any { acc := init; for k, v := range sky_asMap(d) { acc = fn.(func(any) any)(k).(func(any) any)(v).(func(any) any)(acc) }; return acc } } }

func sky_dictUnion(a any) any { return func(b any) any { ma, mb := sky_asMap(a), sky_asMap(b); result := make(map[string]any, len(ma)+len(mb)); for k, v := range mb { result[k] = v }; for k, v := range ma { result[k] = v }; return result } }

func sky_dictRemove(k any) any { return func(d any) any { m := sky_asMap(d); result := make(map[string]any, len(m)); key := sky_asString(k); for k2, v := range m { if k2 != key { result[k2] = v } }; return result } }

func sky_dictMember(k any) any { return func(d any) any { _, ok := sky_asMap(d)[sky_asString(k)]; return ok } }

func sky_setEmpty() any { return map[string]bool{} }

func sky_setSingleton(v any) any { return map[string]bool{sky_asString(v): true} }

func sky_setInsert(v any) any { return func(s any) any { m := s.(map[string]bool); result := make(map[string]bool, len(m)+1); for k := range m { result[k] = true }; result[sky_asString(v)] = true; return result } }

func sky_setMember(v any) any { return func(s any) any { return s.(map[string]bool)[sky_asString(v)] } }

func sky_setUnion(a any) any { return func(b any) any { ma, mb := a.(map[string]bool), b.(map[string]bool); result := make(map[string]bool, len(ma)+len(mb)); for k := range mb { result[k] = true }; for k := range ma { result[k] = true }; return result } }

func sky_setDiff(a any) any { return func(b any) any { ma, mb := a.(map[string]bool), b.(map[string]bool); result := make(map[string]bool); for k := range ma { if !mb[k] { result[k] = true } }; return result } }

func sky_setToList(s any) any { m := s.(map[string]bool); result := make([]any, 0, len(m)); for k := range m { result = append(result, k) }; return result }

func sky_setFromList(list any) any { result := make(map[string]bool); for _, item := range sky_asList(list) { result[sky_asString(item)] = true }; return result }

func sky_setIsEmpty(s any) any { return len(s.(map[string]bool)) == 0 }

func sky_setRemove(v any) any { return func(s any) any { m := s.(map[string]bool); result := make(map[string]bool, len(m)); key := sky_asString(v); for k := range m { if k != key { result[k] = true } }; return result } }

func sky_readLine(u any) any { if stdinReader == nil { stdinReader = bufio.NewReader(os.Stdin) }; line, err := stdinReader.ReadString('\n'); if err != nil && len(line) == 0 { return SkyNothing() }; return SkyJust(strings.TrimRight(line, "\r\n")) }

func sky_readBytes(n any) any { if stdinReader == nil { stdinReader = bufio.NewReader(os.Stdin) }; count := sky_asInt(n); buf := make([]byte, count); total := 0; for total < count { nr, err := stdinReader.Read(buf[total:]); total += nr; if err != nil { break } }; if total == 0 { return SkyNothing() }; return SkyJust(string(buf[:total])) }

func sky_writeStdout(s any) any { fmt.Print(sky_asString(s)); return struct{}{} }

func sky_writeStderr(s any) any { fmt.Fprint(os.Stderr, sky_asString(s)); return struct{}{} }

var stdinReader *bufio.Reader

func sky_charIsUpper(c any) any { s := sky_asString(c); if len(s) > 0 { r := rune(s[0]); return r >= 'A' && r <= 'Z' }; return false }

func sky_charIsLower(c any) any { s := sky_asString(c); if len(s) > 0 { r := rune(s[0]); return r >= 'a' && r <= 'z' }; return false }

func sky_charIsDigit(c any) any { s := sky_asString(c); if len(s) > 0 { r := rune(s[0]); return r >= '0' && r <= '9' }; return false }

func sky_charIsAlpha(c any) any { s := sky_asString(c); if len(s) > 0 { r := rune(s[0]); return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') }; return false }

func sky_charIsAlphaNum(c any) any { s := sky_asString(c); if len(s) > 0 { r := rune(s[0]); return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') }; return false }

func sky_charToUpper(c any) any { return strings.ToUpper(sky_asString(c)) }

func sky_charToLower(c any) any { return strings.ToLower(sky_asString(c)) }

func sky_stringFromChar(c any) any { return sky_asString(c) }

func sky_stringToList(s any) any { str := sky_asString(s); result := make([]any, len(str)); for i, r := range str { result[i] = string(r) }; return result }

func sky_escapeGoString(s any) any { q := strconv.Quote(sky_asString(s)); return q[1:len(q)-1] }

func sky_goQuote(s any) any { return strconv.Quote(sky_asString(s)) }

func sky_fst(t any) any { return sky_asTuple2(t).V0 }

func sky_snd(t any) any { return sky_asTuple2(t).V1 }

func sky_errorToString(e any) any { return sky_asString(e) }

func sky_identity(v any) any { return v }

func sky_always(a any) any { return func(b any) any { return a } }

func sky_js(v any) any { return v }

func sky_call(f any, arg any) any { return f.(func(any) any)(arg) }

func sky_call2(f any, a any, b any) any { return f.(func(any) any)(a).(func(any) any)(b) }

func sky_call3(f any, a any, b any, c any) any { return f.(func(any) any)(a).(func(any) any)(b).(func(any) any)(c) }

func sky_taskSucceed(value any) any { return func() any { return SkyOk(value) } }

func sky_taskFail(err any) any { return func() any { return SkyErr(err) } }

func sky_taskMap(fn any) any { return func(task any) any { return func() any { r := sky_runTask(task); if sky_asSkyResult(r).Tag == 0 { return SkyOk(fn.(func(any) any)(sky_asSkyResult(r).OkValue)) }; return r } } }

func sky_taskAndThen(fn any) any { return func(task any) any { return func() any { r := sky_runTask(task); if sky_asSkyResult(r).Tag == 0 { next := fn.(func(any) any)(sky_asSkyResult(r).OkValue); return sky_runTask(next) }; return r } } }

func sky_taskPerform(task any) any { return sky_runTask(task) }

func sky_taskSequence(tasks any) any { return func() any { items := sky_asList(tasks); results := make([]any, 0, len(items)); for _, t := range items { r := sky_runTask(t); if sky_asSkyResult(r).Tag == 1 { return r }; results = append(results, sky_asSkyResult(r).OkValue) }; return SkyOk(results) } }

func sky_runTask(task any) any { if t, ok := task.(func() any); ok { defer func() { if r := recover(); r != nil { } }(); return t() }; if r, ok := task.(SkyResult); ok { return r }; return SkyOk(task) }

func sky_runMainTask(result any) { if _, ok := result.(func() any); ok { r := sky_runTask(result); if sky_asSkyResult(r).Tag == 1 { fmt.Fprintln(os.Stderr, sky_asSkyResult(r).ErrValue); os.Exit(1) } } }

func sky_serverListen(port any) any { return func(routes any) any { return func() any { mux := sky_buildMux(sky_asList(routes), ""); addr := fmt.Sprintf(":%d", sky_asInt(port)); fmt.Fprintf(os.Stderr, "Sky server listening on %s\n", addr); err := net_http.ListenAndServe(addr, mux); if err != nil { return SkyErr(err.Error()) }; return SkyOk(struct{}{}) } } }

func sky_serverGet(pattern any) any { return func(handler any) any { return map[string]any{"SkyName": "RouteEntry", "V0": "GET", "V1": pattern, "V2": handler} } }

func sky_serverPost(pattern any) any { return func(handler any) any { return map[string]any{"SkyName": "RouteEntry", "V0": "POST", "V1": pattern, "V2": handler} } }

func sky_serverPut(pattern any) any { return func(handler any) any { return map[string]any{"SkyName": "RouteEntry", "V0": "PUT", "V1": pattern, "V2": handler} } }

func sky_serverDelete(pattern any) any { return func(handler any) any { return map[string]any{"SkyName": "RouteEntry", "V0": "DELETE", "V1": pattern, "V2": handler} } }

func sky_serverAny(pattern any) any { return func(handler any) any { return map[string]any{"SkyName": "RouteEntry", "V0": "*", "V1": pattern, "V2": handler} } }

func sky_serverGroup(prefix any) any { return func(routes any) any { return map[string]any{"SkyName": "RouteGroup", "V0": prefix, "V1": routes} } }

func sky_serverStatic(urlPrefix any) any { return func(dir any) any { return map[string]any{"SkyName": "RouteStatic", "V0": urlPrefix, "V1": dir} } }

func sky_serverText(body any) any { return map[string]any{"status": 200, "body": body, "headers": []any{SkyTuple2{"Content-Type", "text/plain; charset=utf-8"}}, "cookies": []any{}} }

func sky_serverHtml(body any) any { return map[string]any{"status": 200, "body": body, "headers": []any{SkyTuple2{"Content-Type", "text/html; charset=utf-8"}}, "cookies": []any{}} }

func sky_serverJson(body any) any { return map[string]any{"status": 200, "body": body, "headers": []any{SkyTuple2{"Content-Type", "application/json"}}, "cookies": []any{}} }

func sky_serverRedirect(url any) any { return map[string]any{"status": 302, "body": "", "headers": []any{SkyTuple2{"Location", url}}, "cookies": []any{}} }

func sky_serverWithStatus(status any) any { return func(resp any) any { m := sky_asMap(resp); result := make(map[string]any); for k, v := range m { result[k] = v }; result["status"] = status; return result } }

func sky_serverWithHeader(key any) any { return func(val any) any { return func(resp any) any { m := sky_asMap(resp); result := make(map[string]any); for k, v := range m { result[k] = v }; hdrs := sky_asList(m["headers"]); result["headers"] = append(hdrs, SkyTuple2{key, val}); return result } } }

func sky_serverWithCookie(cookie any) any { return func(resp any) any { m := sky_asMap(resp); result := make(map[string]any); for k, v := range m { result[k] = v }; cookies := sky_asList(m["cookies"]); result["cookies"] = append(cookies, cookie); return result } }

func sky_serverParam(name any) any { return func(req any) any { m := sky_asMap(req); path := sky_asString(m["path"]); params := sky_asList(m["params"]); for _, p := range params { t := sky_asTuple2(p); if sky_asString(t.V0) == sky_asString(name) { return SkyJust(t.V1) } }; return sky_extractPathParam(sky_asString(name), path) } }

func sky_extractPathParam(name string, path string) any { return SkyNothing() }

func sky_serverQueryParam(name any) any { return func(req any) any { m := sky_asMap(req); query := sky_asList(m["query"]); for _, p := range query { t := sky_asTuple2(p); if sky_asString(t.V0) == sky_asString(name) { return SkyJust(t.V1) } }; return SkyNothing() } }

func sky_serverHeader(name any) any { return func(req any) any { m := sky_asMap(req); headers := sky_asList(m["headers"]); for _, h := range headers { t := sky_asTuple2(h); if strings.EqualFold(sky_asString(t.V0), sky_asString(name)) { return SkyJust(t.V1) } }; return SkyNothing() } }

func sky_serverGetCookie(name any) any { return func(req any) any { m := sky_asMap(req); cookies := sky_asList(m["cookies"]); for _, c := range cookies { t := sky_asTuple2(c); if sky_asString(t.V0) == sky_asString(name) { return SkyJust(t.V1) } }; return SkyNothing() } }

func sky_serverCookie(name any) any { return func(val any) any { return map[string]any{"name": name, "value": val, "path": "/", "maxAge": 86400, "httpOnly": true, "secure": false, "sameSite": "lax"} } }

func sky_buildMux(routes []any, prefix string) *net_http.ServeMux { mux := net_http.NewServeMux(); for _, r := range routes { rm := sky_asMap(r); if rm == nil { continue }; skyName, _ := rm["SkyName"].(string); switch skyName { case "RouteEntry": method := sky_asString(rm["V0"]); pattern := prefix + sky_asString(rm["V1"]); handler := rm["V2"]; muxPattern := pattern; if method != "*" { muxPattern = method + " " + pattern }; mux.HandleFunc(muxPattern, sky_makeHandler(handler)); case "RouteGroup": groupPrefix := prefix + sky_asString(rm["V0"]); groupRoutes := sky_asList(rm["V1"]); subMux := sky_buildMux(groupRoutes, groupPrefix); mux.Handle(groupPrefix+"/", subMux); case "RouteStatic": urlPrefix := prefix + sky_asString(rm["V0"]); dirPath := sky_asString(rm["V1"]); fs := net_http.FileServer(net_http.Dir(dirPath)); mux.Handle(urlPrefix+"/", net_http.StripPrefix(urlPrefix, fs)) } }; return mux }

func sky_makeHandler(handler any) func(net_http.ResponseWriter, *net_http.Request) { return func(w net_http.ResponseWriter, r *net_http.Request) { defer func() { if rec := recover(); rec != nil { net_http.Error(w, fmt.Sprintf("Internal Server Error: %v", rec), 500); fmt.Fprintf(os.Stderr, "%s %s 500 (panic: %v)\n", r.Method, r.URL.Path, rec) } }(); skyReq := sky_buildRequest(r); fn, ok := handler.(func(any) any); if !ok { net_http.Error(w, "Invalid handler", 500); return }; taskResult := fn(skyReq); var skyResp any; if thunk, ok := taskResult.(func() any); ok { skyResp = thunk() } else if result, ok := taskResult.(SkyResult); ok { skyResp = result } else { skyResp = SkyOk(taskResult) }; result, ok2 := skyResp.(SkyResult); if !ok2 { sky_writeResponse(w, sky_asMap(skyResp)); return }; if result.Tag == 1 { net_http.Error(w, sky_asString(result.ErrValue), 500); return }; sky_writeResponse(w, sky_asMap(result.OkValue)) } }

func sky_buildRequest(r *net_http.Request) map[string]any { _ = r.ParseForm(); body, _ := io.ReadAll(r.Body); headers := make([]any, 0); for k, vs := range r.Header { for _, v := range vs { headers = append(headers, SkyTuple2{k, v}) } }; cookies := make([]any, 0); for _, c := range r.Cookies() { cookies = append(cookies, SkyTuple2{c.Name, c.Value}) }; query := make([]any, 0); for k, vs := range r.URL.Query() { for _, v := range vs { query = append(query, SkyTuple2{k, v}) } }; params := make([]any, 0); protocol := "http"; if r.TLS != nil { protocol = "https" }; return map[string]any{"method": r.Method, "path": r.URL.Path, "body": string(body), "headers": headers, "params": params, "query": query, "cookies": cookies, "remoteAddr": r.RemoteAddr, "host": r.Host, "protocol": protocol} }

func sky_writeResponse(w net_http.ResponseWriter, resp map[string]any) { if resp == nil { w.WriteHeader(200); return }; if cookies, ok := resp["cookies"].([]any); ok { for _, c := range cookies { cm := sky_asMap(c); if cm == nil { continue }; net_http.SetCookie(w, &net_http.Cookie{Name: sky_asString(cm["name"]), Value: sky_asString(cm["value"]), Path: sky_asString(cm["path"]), MaxAge: sky_asInt(cm["maxAge"]), HttpOnly: sky_asBool(cm["httpOnly"]), Secure: sky_asBool(cm["secure"])}) } }; if headers, ok := resp["headers"].([]any); ok { for _, h := range headers { if t, ok := h.(SkyTuple2); ok { w.Header().Set(sky_asString(t.V0), sky_asString(t.V1)) } } }; status := sky_asInt(resp["status"]); if status == 0 { status = 200 }; w.WriteHeader(status); fmt.Fprint(w, sky_asString(resp["body"])) }

func sky_resultMap(fn any) any { return func(r any) any { res := sky_asSkyResult(r); if res.Tag == 0 { return SkyOk(fn.(func(any) any)(res.OkValue)) }; return r } }

func sky_resultWithDefault(def any) any { return func(r any) any { res := sky_asSkyResult(r); if res.Tag == 0 { return res.OkValue }; return def } }

func sky_resultAndThen(fn any) any { return func(r any) any { res := sky_asSkyResult(r); if res.Tag == 0 { return fn.(func(any) any)(res.OkValue) }; return r } }

func sky_resultMapError(fn any) any { return func(r any) any { res := sky_asSkyResult(r); if res.Tag == 1 { return SkyErr(fn.(func(any) any)(res.ErrValue)) }; return r } }

func sky_maybeWithDefault(def any) any { return func(m any) any { mb := sky_asSkyMaybe(m); if mb.Tag == 0 { return mb.JustValue }; return def } }

func sky_maybeMap(fn any) any { return func(m any) any { mb := sky_asSkyMaybe(m); if mb.Tag == 0 { return SkyJust(fn.(func(any) any)(mb.JustValue)) }; return m } }

func sky_maybeAndThen(fn any) any { return func(m any) any { mb := sky_asSkyMaybe(m); if mb.Tag == 0 { return fn.(func(any) any)(mb.JustValue) }; return m } }

func sky_listTake(n any) any { return func(list any) any { items := sky_asList(list); c := sky_asInt(n); if c >= len(items) { return list }; return items[:c] } }

func sky_listSort(list any) any { items := sky_asList(list); result := make([]any, len(items)); copy(result, items); sort.Slice(result, func(i, j int) bool { return fmt.Sprintf("%v", result[i]) < fmt.Sprintf("%v", result[j]) }); return result }

func sky_listZip(a any) any { return func(b any) any { la, lb := sky_asList(a), sky_asList(b); minLen := len(la); if len(lb) < minLen { minLen = len(lb) }; result := make([]any, minLen); for i := 0; i < minLen; i++ { result[i] = SkyTuple2{la[i], lb[i]} }; return result } }

func sky_listRange(from any) any { return func(to any) any { f, t := sky_asInt(from), sky_asInt(to); result := make([]any, 0); for i := f; i <= t; i++ { result = append(result, i) }; return result } }

func sky_listAny(fn any) any { return func(list any) any { for _, item := range sky_asList(list) { if sky_asBool(fn.(func(any) any)(item)) { return true } }; return false } }

func sky_listAll(fn any) any { return func(list any) any { for _, item := range sky_asList(list) { if !sky_asBool(fn.(func(any) any)(item)) { return false } }; return true } }

func sky_listSingleton(v any) any { return []any{v} }

func sky_listIntersperse(sep any) any { return func(list any) any { items := sky_asList(list); if len(items) <= 1 { return list }; result := make([]any, 0, len(items)*2-1); for i, item := range items { if i > 0 { result = append(result, sep) }; result = append(result, item) }; return result } }

func sky_stringLeft(n any) any { return func(s any) any { str := sky_asString(s); c := sky_asInt(n); if c >= len(str) { return str }; return str[:c] } }

func sky_stringRight(n any) any { return func(s any) any { str := sky_asString(s); c := sky_asInt(n); if c >= len(str) { return str }; return str[len(str)-c:] } }

func sky_stringPadLeft(n any) any { return func(ch any) any { return func(s any) any { str := sky_asString(s); pad := sky_asString(ch); for len(str) < sky_asInt(n) { str = pad + str }; return str } } }

func sky_stringLines(s any) any { parts := strings.Split(sky_asString(s), "\n"); result := make([]any, len(parts)); for i, p := range parts { result[i] = p }; return result }

func sky_stringWords(s any) any { words := strings.Fields(sky_asString(s)); result := make([]any, len(words)); for i, w := range words { result[i] = w }; return result }

func sky_stringRepeat(n any) any { return func(s any) any { return strings.Repeat(sky_asString(s), sky_asInt(n)) } }

func sky_mathSqrt(v any) any { return math.Sqrt(sky_asFloat(v)) }

func sky_mathPow(base any) any { return func(exp any) any { return math.Pow(sky_asFloat(base), sky_asFloat(exp)) } }

func sky_mathAbs(v any) any { return math.Abs(sky_asFloat(v)) }

func sky_mathFloor(v any) any { return int(math.Floor(sky_asFloat(v))) }

func sky_mathCeil(v any) any { return int(math.Ceil(sky_asFloat(v))) }

func sky_mathRound(v any) any { return int(math.Round(sky_asFloat(v))) }

func sky_mathMin(a any) any { return func(b any) any { af, bf := sky_asFloat(a), sky_asFloat(b); if af < bf { return af }; return bf } }

func sky_mathMax(a any) any { return func(b any) any { af, bf := sky_asFloat(a), sky_asFloat(b); if af > bf { return af }; return bf } }

func sky_modBy(m any) any { return func(n any) any { mod := sky_asInt(m); if mod == 0 { return 0 }; return sky_asInt(n) % mod } }

func sky_cryptoSha256(s any) any { h := crypto_sha256.Sum256([]byte(sky_asString(s))); return hex.EncodeToString(h[:]) }

func sky_cryptoMd5(s any) any { h := crypto_md5.Sum([]byte(sky_asString(s))); return hex.EncodeToString(h[:]) }

func sky_encodingHexEncode(s any) any { return hex.EncodeToString([]byte(sky_asString(s))) }

func sky_encodingBase64Encode(s any) any { return base64.StdEncoding.EncodeToString([]byte(sky_asString(s))) }

func sky_encodingBase64Decode(s any) any { b, err := base64.StdEncoding.DecodeString(sky_asString(s)); if err != nil { return SkyErr(err.Error()) }; return SkyOk(string(b)) }

func sky_timeNow(u any) any { return time.Now().UnixMilli() }

func sky_timePosixToMillis(t any) any { return sky_asInt(t) }

func sky_httpGetString(url any) any { return func() any { resp, err := net_http.Get(sky_asString(url)); if err != nil { return SkyErr(err.Error()) }; defer resp.Body.Close(); body, err := io.ReadAll(resp.Body); if err != nil { return SkyErr(err.Error()) }; return SkyOk(string(body)) } }

func sky_jsonEncString(v any) any { return sky_asString(v) }

func sky_jsonEncInt(v any) any { return sky_asInt(v) }

func sky_jsonEncFloat(v any) any { return sky_asFloat(v) }

func sky_jsonEncBool(v any) any { return sky_asBool(v) }

func sky_jsonEncNull() any { return nil }

func sky_jsonEncList(encoder any) any { return func(list any) any { items := sky_asList(list); result := make([]any, len(items)); for i, item := range items { result[i] = encoder.(func(any) any)(item) }; return result } }

func sky_jsonEncObject(pairs any) any { m := make(map[string]any); for _, p := range sky_asList(pairs) { t := sky_asTuple2(p); m[sky_asString(t.V0)] = t.V1 }; return m }

func sky_jsonEncode(indent any) any { return func(value any) any { var b []byte; var err error; n := sky_asInt(indent); if n > 0 { b, err = encoding_json.MarshalIndent(value, "", strings.Repeat(" ", n)) } else { b, err = encoding_json.Marshal(value) }; if err != nil { return "null" }; return string(b) } }

func sky_jsonDecString(decoder any) any { return func(jsonStr any) any { var v any; if err := encoding_json.Unmarshal([]byte(sky_asString(jsonStr)), &v); err != nil { return SkyErr(err.Error()) }; return decoder.(func(any) any)(v) } }

var sky_jsonDecoder_string = func(v any) any { if s, ok := v.(string); ok { return SkyOk(s) }; return SkyErr("expected string") }

var sky_jsonDecoder_int = func(v any) any { switch n := v.(type) { case float64: return SkyOk(int(n)); case int: return SkyOk(n) }; return SkyErr("expected int") }

var sky_jsonDecoder_float = func(v any) any { if f, ok := v.(float64); ok { return SkyOk(f) }; return SkyErr("expected float") }

var sky_jsonDecoder_bool = func(v any) any { if b, ok := v.(bool); ok { return SkyOk(b) }; return SkyErr("expected bool") }

func sky_jsonDecField(key any) any { return func(decoder any) any { return func(v any) any { m, ok := v.(map[string]any); if !ok { return SkyErr("expected object") }; val, exists := m[sky_asString(key)]; if !exists { return SkyErr("field '" + sky_asString(key) + "' not found") }; return decoder.(func(any) any)(val) } } }

func sky_jsonDecList(decoder any) any { return func(v any) any { arr, ok := v.([]any); if !ok { return SkyErr("expected array") }; result := make([]any, 0, len(arr)); for _, item := range arr { r := decoder.(func(any) any)(item); res := sky_asSkyResult(r); if res.Tag == 1 { return r }; result = append(result, res.OkValue) }; return SkyOk(result) } }

func sky_jsonDecMap(fn any) any { return func(decoder any) any { return func(v any) any { r := decoder.(func(any) any)(v); res := sky_asSkyResult(r); if res.Tag == 1 { return r }; return SkyOk(fn.(func(any) any)(res.OkValue)) } } }

func sky_jsonDecMap2(fn any) any { return func(d1 any) any { return func(d2 any) any { return func(v any) any { r1 := d1.(func(any) any)(v); res1 := sky_asSkyResult(r1); if res1.Tag == 1 { return r1 }; r2 := d2.(func(any) any)(v); res2 := sky_asSkyResult(r2); if res2.Tag == 1 { return r2 }; return SkyOk(fn.(func(any) any)(res1.OkValue).(func(any) any)(res2.OkValue)) } } } }

func sky_jsonDecMap3(fn any) any { return func(d1 any) any { return func(d2 any) any { return func(d3 any) any { return func(v any) any { r1 := d1.(func(any) any)(v); res1 := sky_asSkyResult(r1); if res1.Tag == 1 { return r1 }; r2 := d2.(func(any) any)(v); res2 := sky_asSkyResult(r2); if res2.Tag == 1 { return r2 }; r3 := d3.(func(any) any)(v); res3 := sky_asSkyResult(r3); if res3.Tag == 1 { return r3 }; return SkyOk(fn.(func(any) any)(res1.OkValue).(func(any) any)(res2.OkValue).(func(any) any)(res3.OkValue)) } } } } }

func sky_jsonDecMap4(fn any) any { return func(d1 any) any { return func(d2 any) any { return func(d3 any) any { return func(d4 any) any { return func(v any) any { r1 := d1.(func(any) any)(v); res1 := sky_asSkyResult(r1); if res1.Tag == 1 { return r1 }; r2 := d2.(func(any) any)(v); res2 := sky_asSkyResult(r2); if res2.Tag == 1 { return r2 }; r3 := d3.(func(any) any)(v); res3 := sky_asSkyResult(r3); if res3.Tag == 1 { return r3 }; r4 := d4.(func(any) any)(v); res4 := sky_asSkyResult(r4); if res4.Tag == 1 { return r4 }; return SkyOk(fn.(func(any) any)(res1.OkValue).(func(any) any)(res2.OkValue).(func(any) any)(res3.OkValue).(func(any) any)(res4.OkValue)) } } } } } }

func sky_jsonDecSucceed(v any) any { return func(_ any) any { return SkyOk(v) } }

func sky_jsonDecFail(msg any) any { return func(_ any) any { return SkyErr(msg) } }

func sky_jsonDecAndThen(fn any) any { return func(decoder any) any { return func(v any) any { r := decoder.(func(any) any)(v); res := sky_asSkyResult(r); if res.Tag == 1 { return r }; nextDecoder := fn.(func(any) any)(res.OkValue); return nextDecoder.(func(any) any)(v) } } }

func sky_jsonDecOneOf(decoders any) any { return func(v any) any { for _, d := range sky_asList(decoders) { r := d.(func(any) any)(v); if sky_asSkyResult(r).Tag == 0 { return r } }; return SkyErr("none of the decoders matched") } }

func sky_jsonDecNullable(decoder any) any { return func(v any) any { if v == nil { return SkyOk(SkyNothing()) }; r := decoder.(func(any) any)(v); res := sky_asSkyResult(r); if res.Tag == 0 { return SkyOk(SkyJust(res.OkValue)) }; return r } }

func sky_jsonDecAt(path any) any { return func(decoder any) any { return func(v any) any { current := v; for _, key := range sky_asList(path) { m, ok := current.(map[string]any); if !ok { return SkyErr("expected object at path") }; val, exists := m[sky_asString(key)]; if !exists { return SkyErr("key not found: " + sky_asString(key)) }; current = val }; return decoder.(func(any) any)(current) } } }

func sky_jsonPipeDecode(constructor any) any { return func(v any) any { return SkyOk(constructor) } }

func sky_jsonPipeRequired(key any) any { return func(decoder any) any { return func(pipeline any) any { return func(v any) any { pr := pipeline.(func(any) any)(v); pres := sky_asSkyResult(pr); if pres.Tag == 1 { return pr }; m, ok := v.(map[string]any); if !ok { return SkyErr("expected object") }; val, exists := m[sky_asString(key)]; if !exists { return SkyErr("field '" + sky_asString(key) + "' required") }; fr := decoder.(func(any) any)(val); fres := sky_asSkyResult(fr); if fres.Tag == 1 { return fr }; return SkyOk(pres.OkValue.(func(any) any)(fres.OkValue)) } } } }

func sky_jsonPipeOptional(key any) any { return func(decoder any) any { return func(def any) any { return func(pipeline any) any { return func(v any) any { pr := pipeline.(func(any) any)(v); pres := sky_asSkyResult(pr); if pres.Tag == 1 { return pr }; m, ok := v.(map[string]any); if !ok { return SkyOk(pres.OkValue.(func(any) any)(def)) }; val, exists := m[sky_asString(key)]; if !exists { return SkyOk(pres.OkValue.(func(any) any)(def)) }; fr := decoder.(func(any) any)(val); fres := sky_asSkyResult(fr); if fres.Tag == 1 { return SkyOk(pres.OkValue.(func(any) any)(def)) }; return SkyOk(pres.OkValue.(func(any) any)(fres.OkValue)) } } } } }

func sky_cmdNone() any { return []any{} }

func sky_cmdBatch(cmds any) any { return sky_asList(cmds) }

func sky_subNone() any { return map[string]any{"SkyName": "SubNone"} }

func sky_subBatch(subs any) any { return map[string]any{"SkyName": "SubBatch", "V0": subs} }

func sky_timeEvery(interval any) any { return func(msg any) any { return map[string]any{"SkyName": "SubTimer", "V0": interval, "V1": msg} } }

func sky_htmlEl(tag any) any { return func(attrs any) any { return func(children any) any { return map[string]any{"tag": tag, "attrs": attrs, "children": children, "text": ""} } } }

func sky_htmlVoid(tag any) any { return func(attrs any) any { return map[string]any{"tag": tag, "attrs": attrs, "children": []any{}, "text": ""} } }

func sky_htmlText(s any) any { return map[string]any{"tag": "", "attrs": []any{}, "children": []any{}, "text": sky_asString(s)} }

func sky_htmlRaw(s any) any { return map[string]any{"tag": "__raw__", "attrs": []any{}, "children": []any{}, "text": sky_asString(s)} }

func sky_htmlStyleNode(attrs any) any { return func(css any) any { return map[string]any{"tag": "style", "attrs": attrs, "children": []any{map[string]any{"tag": "", "attrs": []any{}, "children": []any{}, "text": sky_asString(css)}}, "text": ""} } }

func sky_htmlRender(vnode any) any { return sky_vnodeToHtml(vnode) }

func sky_vnodeToHtml(v any) string { m := sky_asMap(v); if m == nil { return "" }; tag := sky_asString(m["tag"]); if tag == "" { return sky_htmlEscapeStr(sky_asString(m["text"])) }; if tag == "__raw__" { return sky_asString(m["text"]) }; attrs := sky_renderAttrs(sky_asList(m["attrs"])); children := sky_asList(m["children"]); if tag == "input" || tag == "br" || tag == "hr" || tag == "img" || tag == "meta" { return "<" + tag + attrs + " />" }; var sb strings.Builder; sb.WriteString("<" + tag + attrs + ">"); for _, c := range children { sb.WriteString(sky_vnodeToHtml(c)) }; sb.WriteString("</" + tag + ">"); return sb.String() }

func sky_renderAttrs(attrs []any) string { var sb strings.Builder; for _, a := range attrs { t := sky_asTuple2(a); k := sky_asString(t.V0); v := sky_asString(t.V1); if v != "" { sb.WriteString(" " + k + "=\"" + sky_htmlEscapeStr(v) + "\"") } }; return sb.String() }

func sky_htmlEscapeStr(s string) string { s = strings.ReplaceAll(s, "&", "&amp;"); s = strings.ReplaceAll(s, "<", "&lt;"); s = strings.ReplaceAll(s, ">", "&gt;"); s = strings.ReplaceAll(s, "\"", "&quot;"); return s }

func sky_htmlEscapeHtml(s any) any { return sky_htmlEscapeStr(sky_asString(s)) }

func sky_htmlEscapeAttr(s any) any { return sky_htmlEscapeStr(sky_asString(s)) }

func sky_htmlAttrToString(attr any) any { t := sky_asTuple2(attr); return sky_asString(t.V0) + "=\"" + sky_htmlEscapeStr(sky_asString(t.V1)) + "\"" }

func sky_attrSimple(key any) any { return func(v any) any { return SkyTuple2{sky_asString(key), sky_asString(v)} } }

func sky_attrCustom(key any) any { return func(v any) any { return SkyTuple2{sky_asString(key), sky_asString(v)} } }

func sky_attrBool(key any) any { return func(v any) any { if sky_asBool(v) { return SkyTuple2{sky_asString(key), sky_asString(key)} }; return SkyTuple2{sky_asString(key), ""} } }

func sky_attrData(key any) any { return func(val any) any { return SkyTuple2{"data-" + sky_asString(key), sky_asString(val)} } }

func sky_evtHandler(evtType any) any { return func(msg any) any { return SkyTuple2{"sky-" + sky_asString(evtType), sky_msgName(msg)} } }

func sky_msgName(msg any) string { if m, ok := msg.(map[string]any); ok { if name, exists := m["SkyName"]; exists { return sky_asString(name) } }; return fmt.Sprintf("%v", msg) }

func sky_cssStylesheet(rules any) any { var sb strings.Builder; for _, r := range sky_asList(rules) { sb.WriteString(sky_asString(r)); sb.WriteString("\n") }; return sb.String() }

func sky_cssRule(selector any) any { return func(props any) any { var sb strings.Builder; sb.WriteString(sky_asString(selector)); sb.WriteString(" { "); for _, p := range sky_asList(props) { sb.WriteString(sky_asString(p)); sb.WriteString("; ") }; sb.WriteString("}"); return sb.String() } }

func sky_cssProp(key any) any { return func(val any) any { return sky_asString(key) + ": " + sky_asString(val) } }

func sky_cssPx(n any) any { return fmt.Sprintf("%dpx", sky_asInt(n)) }

func sky_cssRem(n any) any { return fmt.Sprintf("%.2frem", sky_asFloat(n)) }

func sky_cssEm(n any) any { return fmt.Sprintf("%.2fem", sky_asFloat(n)) }

func sky_cssPct(n any) any { return fmt.Sprintf("%.0f%%", sky_asFloat(n)) }

func sky_cssHex(s any) any { return "#" + sky_asString(s) }

func sky_cssRgb(r any) any { return func(g any) any { return func(b any) any { return fmt.Sprintf("rgb(%d, %d, %d)", sky_asInt(r), sky_asInt(g), sky_asInt(b)) } } }

func sky_cssStyles(props any) any { var parts []string; for _, p := range sky_asList(props) { parts = append(parts, sky_asString(p)) }; return strings.Join(parts, "; ") }

func sky_cssMargin2(v any) any { return func(h any) any { return "margin: " + sky_asString(v) + " " + sky_asString(h) } }

func sky_cssPadding2(v any) any { return func(h any) any { return "padding: " + sky_asString(v) + " " + sky_asString(h) } }

func sky_cssRgba(r any) any { return func(g any) any { return func(b any) any { return func(a any) any { return fmt.Sprintf("rgba(%d, %d, %d, %v)", sky_asInt(r), sky_asInt(g), sky_asInt(b), sky_asFloat(a)) } } } }

func sky_cssMedia(query any) any { return func(rules any) any { var sb strings.Builder; sb.WriteString("@media "); sb.WriteString(sky_asString(query)); sb.WriteString(" { "); for _, r := range sky_asList(rules) { sb.WriteString(sky_asString(r)); sb.WriteString(" ") }; sb.WriteString("}"); return sb.String() } }

func sky_cssKeyframes(name any) any { return func(frames any) any { var sb strings.Builder; sb.WriteString("@keyframes "); sb.WriteString(sky_asString(name)); sb.WriteString(" { "); for _, f := range sky_asList(frames) { sb.WriteString(sky_asString(f)); sb.WriteString(" ") }; sb.WriteString("}"); return sb.String() } }

func sky_cssFrame(pctVal any) any { return func(props any) any { var sb strings.Builder; sb.WriteString(fmt.Sprintf("%v%%", sky_asFloat(pctVal))); sb.WriteString(" { "); for _, p := range sky_asList(props) { sb.WriteString(sky_asString(p)); sb.WriteString("; ") }; sb.WriteString("}"); return sb.String() } }

func sky_cssPropFn(prop any) any { return func(val any) any { return sky_asString(prop) + ": " + sky_asString(val) } }

func sky_evt_fileMaxSize(v any) any { return SkyTuple2{"sky-file-maxsize", fmt.Sprintf("%d", sky_asInt(v))} }

func sky_evt_fileMaxWidth(v any) any { return SkyTuple2{"sky-file-maxwidth", fmt.Sprintf("%d", sky_asInt(v))} }

func sky_evt_fileMaxHeight(v any) any { return SkyTuple2{"sky-file-maxheight", fmt.Sprintf("%d", sky_asInt(v))} }

func sky_liveRoute(path any) any { return func(page any) any { return map[string]any{"path": path, "page": page} } }

func sky_liveApp(config any) any { return config }

var MapGoTypeToSky = Ffi_TypeMapper_MapGoTypeToSky

var IsGoPrimitive = Ffi_TypeMapper_IsGoPrimitive

var GoTypeToAssertion = Ffi_TypeMapper_GoTypeToAssertion

var GoTypeToCast = Ffi_TypeMapper_GoTypeToCast

var ShortTypeName = Ffi_TypeMapper_ShortTypeName

var FindLastDot = Ffi_TypeMapper_FindLastDot

var LowerCamelCase = Ffi_TypeMapper_LowerCamelCase

func Ffi_TypeMapper_MapGoTypeToSky(goType any) any {
	return func() any { if sky_asBool(sky_equal(goType, "string")) { return "String" }; return func() any { if sky_asBool(sky_equal(goType, "bool")) { return "Bool" }; return func() any { if sky_asBool(sky_asBool(sky_equal(goType, "int")) || sky_asBool(sky_asBool(sky_equal(goType, "int8")) || sky_asBool(sky_asBool(sky_equal(goType, "int16")) || sky_asBool(sky_asBool(sky_equal(goType, "int32")) || sky_asBool(sky_equal(goType, "int64")))))) { return "Int" }; return func() any { if sky_asBool(sky_asBool(sky_equal(goType, "uint")) || sky_asBool(sky_asBool(sky_equal(goType, "uint8")) || sky_asBool(sky_asBool(sky_equal(goType, "uint16")) || sky_asBool(sky_asBool(sky_equal(goType, "uint32")) || sky_asBool(sky_equal(goType, "uint64")))))) { return "Int" }; return func() any { if sky_asBool(sky_asBool(sky_equal(goType, "float32")) || sky_asBool(sky_equal(goType, "float64"))) { return "Float" }; return func() any { if sky_asBool(sky_equal(goType, "[]byte")) { return "Bytes" }; return func() any { if sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("["), goType)) && sky_asBool(sky_call(sky_stringEndsWith("byte"), goType))) { return "Bytes" }; return func() any { if sky_asBool(sky_equal(goType, "error")) { return "Error" }; return func() any { if sky_asBool(sky_asBool(sky_equal(goType, "interface{}")) || sky_asBool(sky_equal(goType, "any"))) { return "Any" }; return func() any { if sky_asBool(sky_equal(goType, "[]string")) { return "List String" }; return func() any { if sky_asBool(sky_equal(goType, "[]int")) { return "List Int" }; return func() any { if sky_asBool(sky_equal(goType, "[]float64")) { return "List Float" }; return func() any { if sky_asBool(sky_equal(goType, "[]bool")) { return "List Bool" }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("[]"), goType)) { return "List Any" }; return func() any { if sky_asBool(sky_equal(goType, "rune")) { return "Char" }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("map["), goType)) { return "Any" }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("func("), goType)) { return "Any" }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("*"), goType)) { return func() any { inner := sky_call2(sky_stringSlice(1), sky_stringLength(goType), goType); _ = inner; return func() any { if sky_asBool(Ffi_TypeMapper_IsGoPrimitive(inner)) { return sky_concat("Maybe ", Ffi_TypeMapper_MapGoTypeToSky(inner)) }; return Ffi_TypeMapper_MapGoTypeToSky(inner) }() }() }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("[]"), goType)) { return func() any { elem := sky_call2(sky_stringSlice(2), sky_stringLength(goType), goType); _ = elem; return sky_concat("List ", Ffi_TypeMapper_MapGoTypeToSky(elem)) }() }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("map["), goType)) { return "Any" }; return func() any { if sky_asBool(sky_call(sky_stringContains("."), goType)) { return func() any { parts := sky_call(sky_stringSplit("."), goType); _ = parts; return func() any { return func() any { __subject := sky_listReverse(parts); if len(sky_asList(__subject)) > 0 { last := sky_asList(__subject)[0]; _ = last; return last };  if len(sky_asList(__subject)) == 0 { return goType };  return nil }() }() }() }; return goType }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }()
}

func Ffi_TypeMapper_IsGoPrimitive(t any) any {
	return sky_asBool(sky_equal(t, "string")) || sky_asBool(sky_asBool(sky_equal(t, "int")) || sky_asBool(sky_asBool(sky_equal(t, "int8")) || sky_asBool(sky_asBool(sky_equal(t, "int16")) || sky_asBool(sky_asBool(sky_equal(t, "int32")) || sky_asBool(sky_asBool(sky_equal(t, "int64")) || sky_asBool(sky_asBool(sky_equal(t, "uint")) || sky_asBool(sky_asBool(sky_equal(t, "uint8")) || sky_asBool(sky_asBool(sky_equal(t, "uint16")) || sky_asBool(sky_asBool(sky_equal(t, "uint32")) || sky_asBool(sky_asBool(sky_equal(t, "uint64")) || sky_asBool(sky_asBool(sky_equal(t, "float32")) || sky_asBool(sky_asBool(sky_equal(t, "float64")) || sky_asBool(sky_asBool(sky_equal(t, "bool")) || sky_asBool(sky_equal(t, "rune")))))))))))))))
}

func Ffi_TypeMapper_GoTypeToAssertion(goType any) any {
	return func() any { if sky_asBool(sky_equal(goType, "string")) { return "sky_asString" }; return func() any { if sky_asBool(sky_equal(goType, "int")) { return "sky_asInt" }; return func() any { if sky_asBool(sky_equal(goType, "int64")) { return "sky_asInt64" }; return func() any { if sky_asBool(sky_asBool(sky_equal(goType, "int8")) || sky_asBool(sky_asBool(sky_equal(goType, "int16")) || sky_asBool(sky_equal(goType, "int32")))) { return "sky_asInt" }; return func() any { if sky_asBool(sky_equal(goType, "uint")) { return "sky_asUint" }; return func() any { if sky_asBool(sky_equal(goType, "uint8")) { return "sky_asUint8" }; return func() any { if sky_asBool(sky_equal(goType, "uint16")) { return "sky_asUint16" }; return func() any { if sky_asBool(sky_equal(goType, "uint32")) { return "sky_asUint32" }; return func() any { if sky_asBool(sky_equal(goType, "uint64")) { return "sky_asUint64" }; return func() any { if sky_asBool(sky_equal(goType, "float64")) { return "sky_asFloat" }; return func() any { if sky_asBool(sky_equal(goType, "float32")) { return "sky_asFloat32" }; return func() any { if sky_asBool(sky_equal(goType, "bool")) { return "sky_asBool" }; return func() any { if sky_asBool(sky_equal(goType, "[]byte")) { return "sky_asBytes" }; return func() any { if sky_asBool(sky_equal(goType, "context.Context")) { return "sky_asContext" }; return func() any { if sky_asBool(sky_equal(goType, "[]string")) { return "sky_asStringSlice" }; return func() any { if sky_asBool(sky_equal(goType, "error")) { return "sky_asError" }; return func() any { if sky_asBool(sky_asBool(sky_equal(goType, "interface{}")) || sky_asBool(sky_equal(goType, "any"))) { return "" }; return func() any { if sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("func("), goType)) && sky_asBool(sky_call(sky_stringContains("ResponseWriter"), goType))) { return "sky_asHttpHandler" }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("func("), goType)) { return "" }; return func() any { if sky_asBool(sky_equal(goType, "map[string]string")) { return "sky_asMapStringString" }; return func() any { if sky_asBool(sky_equal(goType, "map[string]interface{}")) { return "sky_asMapStringAny" }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("map["), goType)) { return "" }; return func() any { if sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("["), goType)) && sky_asBool(sky_call(sky_stringContains("]byte"), goType))) { return "sky_asFixedBytes" }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("[]"), goType)) { return "sky_asList" }; return "sky_asType" }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }()
}

func Ffi_TypeMapper_GoTypeToCast(goType any, argName any) any {
	return func() any { assertion := Ffi_TypeMapper_GoTypeToAssertion(goType); _ = assertion; return func() any { if sky_asBool(sky_equal(assertion, "")) { return argName }; return func() any { if sky_asBool(sky_equal(assertion, "sky_asType")) { return sky_concat(argName, sky_concat(".(", sky_concat(Ffi_TypeMapper_ShortTypeName(goType), ")"))) }; return sky_concat(assertion, sky_concat("(", sky_concat(argName, ")"))) }() }() }()
}

func Ffi_TypeMapper_ShortTypeName(goType any) any {
	return func() any { if sky_asBool(sky_call(sky_stringStartsWith("*"), goType)) { return sky_concat("*", Ffi_TypeMapper_ShortTypeName(sky_call2(sky_stringSlice(1), sky_stringLength(goType), goType))) }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("[]"), goType)) { return sky_concat("[]", Ffi_TypeMapper_ShortTypeName(sky_call2(sky_stringSlice(2), sky_stringLength(goType), goType))) }; return func() any { if sky_asBool(sky_call(sky_stringContains("."), goType)) { return func() any { lastDot := Ffi_TypeMapper_FindLastDot(goType, sky_asInt(sky_stringLength(goType)) - sky_asInt(1)); _ = lastDot; pkgPath := sky_call2(sky_stringSlice(0), lastDot, goType); _ = pkgPath; typeName := sky_call2(sky_stringSlice(sky_asInt(lastDot) + sky_asInt(1)), sky_stringLength(goType), goType); _ = typeName; shortPkg := func() any { return func() any { __subject := sky_listReverse(sky_call(sky_stringSplit("/"), pkgPath)); if len(sky_asList(__subject)) > 0 { last := sky_asList(__subject)[0]; _ = last; return last };  if len(sky_asList(__subject)) == 0 { return pkgPath };  return nil }() }(); _ = shortPkg; return sky_concat(shortPkg, sky_concat(".", typeName)) }() }; return goType }() }() }()
}

func Ffi_TypeMapper_FindLastDot(s any, idx any) any {
	return func() any { if sky_asBool(sky_asInt(idx) < sky_asInt(0)) { return 0 }; return func() any { if sky_asBool(sky_equal(sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1), s), ".")) { return idx }; return Ffi_TypeMapper_FindLastDot(s, sky_asInt(idx) - sky_asInt(1)) }() }()
}

func Ffi_TypeMapper_LowerCamelCase(s any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(s)) { return "" }; return sky_concat(sky_stringToLower(sky_call2(sky_stringSlice(0), 1, s)), sky_call2(sky_stringSlice(1), sky_stringLength(s), s)) }()
}

var ResolveProject = Compiler_Resolver_ResolveProject

var ResolveImports = Compiler_Resolver_ResolveImports

var ResolveModulePath = Compiler_Resolver_ResolveModulePath

var ResolveBindingPath = Compiler_Resolver_ResolveBindingPath

var CamelToUnderscore = Compiler_Resolver_CamelToUnderscore

var CamelInsertUnderscores = Compiler_Resolver_CamelInsertUnderscores

var IsStdlib = Compiler_Resolver_IsStdlib

var GetStdlibExports = Compiler_Resolver_GetStdlibExports

var CheckAllModules = Compiler_Resolver_CheckAllModules

var CheckModulesLoop = Compiler_Resolver_CheckModulesLoop

var BuildStdlibEnv = Compiler_Resolver_BuildStdlibEnv

var IsFfiBinding = Compiler_Resolver_IsFfiBinding

var ModuleNameToGoPath = Compiler_Resolver_ModuleNameToGoPath

var ModuleNameToSafeName = Compiler_Resolver_ModuleNameToSafeName

var ResolveWrapperPath = Compiler_Resolver_ResolveWrapperPath

var CollectForeignImports = Compiler_Resolver_CollectForeignImports

var CollectAllForeignImports = Compiler_Resolver_CollectAllForeignImports

var DeduplicateForeignImports = Compiler_Resolver_DeduplicateForeignImports

var DeduplicateForeignLoop = Compiler_Resolver_DeduplicateForeignLoop

var TripleFirst = Compiler_Resolver_TripleFirst

var TripleSecond = Compiler_Resolver_TripleSecond

var TripleThird = Compiler_Resolver_TripleThird

var IdentifierExpr = Compiler_Ast_IdentifierExpr

var QualifiedExpr = Compiler_Ast_QualifiedExpr

var IntLitExpr = Compiler_Ast_IntLitExpr

var FloatLitExpr = Compiler_Ast_FloatLitExpr

var StringLitExpr = Compiler_Ast_StringLitExpr

var CharLitExpr = Compiler_Ast_CharLitExpr

var BoolLitExpr = Compiler_Ast_BoolLitExpr

var UnitExpr = Compiler_Ast_UnitExpr

var TupleExpr = Compiler_Ast_TupleExpr

var ListExpr = Compiler_Ast_ListExpr

var RecordExpr = Compiler_Ast_RecordExpr

var RecordUpdateExpr = Compiler_Ast_RecordUpdateExpr

var FieldAccessExpr = Compiler_Ast_FieldAccessExpr

var CallExpr = Compiler_Ast_CallExpr

var LambdaExpr = Compiler_Ast_LambdaExpr

var IfExpr = Compiler_Ast_IfExpr

var LetExpr = Compiler_Ast_LetExpr

var CaseExpr = Compiler_Ast_CaseExpr

var BinaryExpr = Compiler_Ast_BinaryExpr

var NegateExpr = Compiler_Ast_NegateExpr

var ParenExpr = Compiler_Ast_ParenExpr

var PWildcard = Compiler_Ast_PWildcard

var PVariable = Compiler_Ast_PVariable

var PConstructor = Compiler_Ast_PConstructor

var PLiteral = Compiler_Ast_PLiteral

var PTuple = Compiler_Ast_PTuple

var PList = Compiler_Ast_PList

var PCons = Compiler_Ast_PCons

var PAs = Compiler_Ast_PAs

var PRecord = Compiler_Ast_PRecord

var LitInt = Compiler_Ast_LitInt

var LitFloat = Compiler_Ast_LitFloat

var LitString = Compiler_Ast_LitString

var LitChar = Compiler_Ast_LitChar

var LitBool = Compiler_Ast_LitBool

var TypeRef = Compiler_Ast_TypeRef

var TypeVar = Compiler_Ast_TypeVar

var FunType = Compiler_Ast_FunType

var RecordTypeExpr = Compiler_Ast_RecordTypeExpr

var TupleTypeExpr = Compiler_Ast_TupleTypeExpr

var UnitTypeExpr = Compiler_Ast_UnitTypeExpr

var FunDecl = Compiler_Ast_FunDecl

var TypeAnnotDecl = Compiler_Ast_TypeAnnotDecl

var TypeDecl = Compiler_Ast_TypeDecl

var TypeAliasDecl = Compiler_Ast_TypeAliasDecl

var ForeignImportDecl = Compiler_Ast_ForeignImportDecl

var ExposeAll = Compiler_Ast_ExposeAll

var ExposeList = Compiler_Ast_ExposeList

var ExposeNone = Compiler_Ast_ExposeNone

var TkIdentifier = Compiler_Token_TkIdentifier

var TkUpperIdentifier = Compiler_Token_TkUpperIdentifier

var TkInteger = Compiler_Token_TkInteger

var TkFloat = Compiler_Token_TkFloat

var TkString = Compiler_Token_TkString

var TkChar = Compiler_Token_TkChar

var TkKeyword = Compiler_Token_TkKeyword

var TkOperator = Compiler_Token_TkOperator

var TkEquals = Compiler_Token_TkEquals

var TkColon = Compiler_Token_TkColon

var TkComma = Compiler_Token_TkComma

var TkDot = Compiler_Token_TkDot

var TkPipe = Compiler_Token_TkPipe

var TkArrow = Compiler_Token_TkArrow

var TkBackslash = Compiler_Token_TkBackslash

var TkLParen = Compiler_Token_TkLParen

var TkRParen = Compiler_Token_TkRParen

var TkLBracket = Compiler_Token_TkLBracket

var TkRBracket = Compiler_Token_TkRBracket

var TkLBrace = Compiler_Token_TkLBrace

var TkRBrace = Compiler_Token_TkRBrace

var TkNewline = Compiler_Token_TkNewline

var TkIndent = Compiler_Token_TkIndent

var TkDedent = Compiler_Token_TkDedent

var TkEOF = Compiler_Token_TkEOF

var emptySpan = Compiler_Token_EmptySpan()

var EmptySpan = Compiler_Token_EmptySpan()

var isKeyword = Compiler_Token_IsKeyword

var IsKeyword = Compiler_Token_IsKeyword

var lex = Compiler_Lexer_Lex

var Lex = Compiler_Lexer_Lex

var lexLoop = Compiler_Lexer_LexLoop

var LexLoop = Compiler_Lexer_LexLoop

var handleNewline = Compiler_Lexer_HandleNewline

var HandleNewline = Compiler_Lexer_HandleNewline

var countIndent = Compiler_Lexer_CountIndent

var CountIndent = Compiler_Lexer_CountIndent

var skipWhitespace = Compiler_Lexer_SkipWhitespace

var SkipWhitespace = Compiler_Lexer_SkipWhitespace

var skipLineComment = Compiler_Lexer_SkipLineComment

var SkipLineComment = Compiler_Lexer_SkipLineComment

var lexString = Compiler_Lexer_LexString

var LexString = Compiler_Lexer_LexString

var lexStringBody = Compiler_Lexer_LexStringBody

var LexStringBody = Compiler_Lexer_LexStringBody

var lexChar = Compiler_Lexer_LexChar

var LexChar = Compiler_Lexer_LexChar

var lexNumber = Compiler_Lexer_LexNumber

var LexNumber = Compiler_Lexer_LexNumber

var consumeDigits = Compiler_Lexer_ConsumeDigits

var ConsumeDigits = Compiler_Lexer_ConsumeDigits

var lexIdentifier = Compiler_Lexer_LexIdentifier

var LexIdentifier = Compiler_Lexer_LexIdentifier

var consumeIdentChars = Compiler_Lexer_ConsumeIdentChars

var ConsumeIdentChars = Compiler_Lexer_ConsumeIdentChars

var lexOperatorOrPunctuation = Compiler_Lexer_LexOperatorOrPunctuation

var LexOperatorOrPunctuation = Compiler_Lexer_LexOperatorOrPunctuation

var consumeOperator = Compiler_Lexer_ConsumeOperator

var ConsumeOperator = Compiler_Lexer_ConsumeOperator

var isOperatorChar = Compiler_Lexer_IsOperatorChar

var IsOperatorChar = Compiler_Lexer_IsOperatorChar

var charAt = Compiler_Lexer_CharAt

var CharAt = Compiler_Lexer_CharAt

var peekChar = Compiler_Lexer_PeekChar

var PeekChar = Compiler_Lexer_PeekChar

var makeSpan = Compiler_Lexer_MakeSpan

var MakeSpan = Compiler_Lexer_MakeSpan

var dispatchDeclaration = Compiler_Parser_DispatchDeclaration

var DispatchDeclaration = Compiler_Parser_DispatchDeclaration

var parseVariantFields = Compiler_Parser_ParseVariantFields

var ParseVariantFields = Compiler_Parser_ParseVariantFields

var parseTypeArgs = Compiler_Parser_ParseTypeArgs

var ParseTypeArgs = Compiler_Parser_ParseTypeArgs

var parse = Compiler_Parser_Parse

var Parse = Compiler_Parser_Parse

var parseModule = Compiler_Parser_ParseModule

var ParseModule = Compiler_Parser_ParseModule

var parseModuleName = Compiler_Parser_ParseModuleName

var ParseModuleName = Compiler_Parser_ParseModuleName

var parseModuleNameParts = Compiler_Parser_ParseModuleNameParts

var ParseModuleNameParts = Compiler_Parser_ParseModuleNameParts

var parseOptionalExposing = Compiler_Parser_ParseOptionalExposing

var ParseOptionalExposing = Compiler_Parser_ParseOptionalExposing

var parseExposingClause = Compiler_Parser_ParseExposingClause

var ParseExposingClause = Compiler_Parser_ParseExposingClause

var parseExposedItems = Compiler_Parser_ParseExposedItems

var ParseExposedItems = Compiler_Parser_ParseExposedItems

var parseImports = Compiler_Parser_ParseImports

var ParseImports = Compiler_Parser_ParseImports

var parseImport = Compiler_Parser_ParseImport

var ParseImport = Compiler_Parser_ParseImport

var getLexemeAt1 = Compiler_Parser_GetLexemeAt1

var GetLexemeAt1 = Compiler_Parser_GetLexemeAt1

var parseDeclaration = Compiler_Parser_ParseDeclaration

var ParseDeclaration = Compiler_Parser_ParseDeclaration

var parseDeclarations = Compiler_Parser_ParseDeclarations

var ParseDeclarations = Compiler_Parser_ParseDeclarations

var parseDeclsHelper = Compiler_Parser_ParseDeclsHelper

var ParseDeclsHelper = Compiler_Parser_ParseDeclsHelper

var addDeclAndContinue = Compiler_Parser_AddDeclAndContinue

var AddDeclAndContinue = Compiler_Parser_AddDeclAndContinue

var prependToResult = Compiler_Parser_PrependToResult

var PrependToResult = Compiler_Parser_PrependToResult

var parseForeignImport = Compiler_Parser_ParseForeignImport

var ParseForeignImport = Compiler_Parser_ParseForeignImport

var parseTypeAlias = Compiler_Parser_ParseTypeAlias

var ParseTypeAlias = Compiler_Parser_ParseTypeAlias

var parseTypeDecl = Compiler_Parser_ParseTypeDecl

var ParseTypeDecl = Compiler_Parser_ParseTypeDecl

var parseTypeParams = Compiler_Parser_ParseTypeParams

var ParseTypeParams = Compiler_Parser_ParseTypeParams

var parseTypeVariants = Compiler_Parser_ParseTypeVariants

var ParseTypeVariants = Compiler_Parser_ParseTypeVariants

var buildVariant = Compiler_Parser_BuildVariant

var BuildVariant = Compiler_Parser_BuildVariant

var finishVariant = Compiler_Parser_FinishVariant

var FinishVariant = Compiler_Parser_FinishVariant

var prependVariant = Compiler_Parser_PrependVariant

var PrependVariant = Compiler_Parser_PrependVariant

var parseTypeExpr = Compiler_Parser_ParseTypeExpr

var ParseTypeExpr = Compiler_Parser_ParseTypeExpr

var parseTypeApp = Compiler_Parser_ParseTypeApp

var ParseTypeApp = Compiler_Parser_ParseTypeApp

var applyTypeArgs = Compiler_Parser_ApplyTypeArgs

var ApplyTypeArgs = Compiler_Parser_ApplyTypeArgs

var resolveTypeApp = Compiler_Parser_ResolveTypeApp

var ResolveTypeApp = Compiler_Parser_ResolveTypeApp

var parseTypePrimary = Compiler_Parser_ParseTypePrimary

var ParseTypePrimary = Compiler_Parser_ParseTypePrimary

var parseTupleTypeRest = Compiler_Parser_ParseTupleTypeRest

var ParseTupleTypeRest = Compiler_Parser_ParseTupleTypeRest

var parseRecordType = Compiler_Parser_ParseRecordType

var ParseRecordType = Compiler_Parser_ParseRecordType

var parseRecordTypeFields = Compiler_Parser_ParseRecordTypeFields

var ParseRecordTypeFields = Compiler_Parser_ParseRecordTypeFields

var parseTypeAnnot = Compiler_Parser_ParseTypeAnnot

var ParseTypeAnnot = Compiler_Parser_ParseTypeAnnot

var parseFunDecl = Compiler_Parser_ParseFunDecl

var ParseFunDecl = Compiler_Parser_ParseFunDecl

var parseFunParams = Compiler_Parser_ParseFunParams

var ParseFunParams = Compiler_Parser_ParseFunParams

var TVar = Compiler_Types_TVar

var TConst = Compiler_Types_TConst

var TFun = Compiler_Types_TFun

var TApp = Compiler_Types_TApp

var TTuple = Compiler_Types_TTuple

var TRecord = Compiler_Types_TRecord

var freshVar = Compiler_Types_FreshVar

var FreshVar = Compiler_Types_FreshVar

var emptySub = Compiler_Types_EmptySub()

var EmptySub = Compiler_Types_EmptySub()

var applySub = Compiler_Types_ApplySub

var ApplySub = Compiler_Types_ApplySub

var applySubToScheme = Compiler_Types_ApplySubToScheme

var ApplySubToScheme = Compiler_Types_ApplySubToScheme

var composeSubs = Compiler_Types_ComposeSubs

var ComposeSubs = Compiler_Types_ComposeSubs

var freeVars = Compiler_Types_FreeVars

var FreeVars = Compiler_Types_FreeVars

var freeVarsInScheme = Compiler_Types_FreeVarsInScheme

var FreeVarsInScheme = Compiler_Types_FreeVarsInScheme

var instantiate = Compiler_Types_Instantiate

var Instantiate = Compiler_Types_Instantiate

var generalize = Compiler_Types_Generalize

var Generalize = Compiler_Types_Generalize

var mono = Compiler_Types_Mono

var Mono = Compiler_Types_Mono

var formatType = Compiler_Types_FormatType

var FormatType = Compiler_Types_FormatType

var empty = Compiler_Env_Empty()

var Empty = Compiler_Env_Empty()

var lookup = Compiler_Env_Lookup

var Lookup = Compiler_Env_Lookup

var extend = Compiler_Env_Extend

var Extend = Compiler_Env_Extend

var extendMany = Compiler_Env_ExtendMany

var ExtendMany = Compiler_Env_ExtendMany

var remove = Compiler_Env_Remove

var Remove = Compiler_Env_Remove

var keys = Compiler_Env_Keys

var Keys = Compiler_Env_Keys

var toList = Compiler_Env_ToList

var ToList = Compiler_Env_ToList

var fromList = Compiler_Env_FromList

var FromList = Compiler_Env_FromList

var union = Compiler_Env_Union

var Union = Compiler_Env_Union

var freeVarsInEnv = Compiler_Env_FreeVarsInEnv

var FreeVarsInEnv = Compiler_Env_FreeVarsInEnv

var generalizeInEnv = Compiler_Env_GeneralizeInEnv

var GeneralizeInEnv = Compiler_Env_GeneralizeInEnv

var createPreludeEnv = Compiler_Env_CreatePreludeEnv()

var CreatePreludeEnv = Compiler_Env_CreatePreludeEnv()

var makeTypedDecl = Compiler_Checker_MakeTypedDecl

var MakeTypedDecl = Compiler_Checker_MakeTypedDecl

var checkModule = Compiler_Checker_CheckModule

var CheckModule = Compiler_Checker_CheckModule

var registerTypeAliases = Compiler_Checker_RegisterTypeAliases

var RegisterTypeAliases = Compiler_Checker_RegisterTypeAliases

var collectAnnotations = Compiler_Checker_CollectAnnotations

var CollectAnnotations = Compiler_Checker_CollectAnnotations

var collectAnnotationsLoop = Compiler_Checker_CollectAnnotationsLoop

var CollectAnnotationsLoop = Compiler_Checker_CollectAnnotationsLoop

var preRegisterFunctions = Compiler_Checker_PreRegisterFunctions

var PreRegisterFunctions = Compiler_Checker_PreRegisterFunctions

var preRegisterOneFunction = Compiler_Checker_PreRegisterOneFunction

var PreRegisterOneFunction = Compiler_Checker_PreRegisterOneFunction

var inferAllDeclarations = Compiler_Checker_InferAllDeclarations

var InferAllDeclarations = Compiler_Checker_InferAllDeclarations

var inferDeclsLoop = Compiler_Checker_InferDeclsLoop

var InferDeclsLoop = Compiler_Checker_InferDeclsLoop

var inferOneDecl = Compiler_Checker_InferOneDecl

var InferOneDecl = Compiler_Checker_InferOneDecl

var inferOneFunDecl = Compiler_Checker_InferOneFunDecl

var InferOneFunDecl = Compiler_Checker_InferOneFunDecl

var checkAllExhaustiveness = Compiler_Checker_CheckAllExhaustiveness

var CheckAllExhaustiveness = Compiler_Checker_CheckAllExhaustiveness

var checkDeclExhaustiveness = Compiler_Checker_CheckDeclExhaustiveness

var CheckDeclExhaustiveness = Compiler_Checker_CheckDeclExhaustiveness

var checkExprExhaustiveness = Compiler_Checker_CheckExprExhaustiveness

var CheckExprExhaustiveness = Compiler_Checker_CheckExprExhaustiveness

func Compiler_Resolver_ResolveProject(entryPath any, srcRoot any) any {
	return func() any { return func() any { __subject := sky_fileRead(entryPath); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Cannot read entry file: ", sky_concat(entryPath, sky_concat(" (", sky_concat(e, ")"))))) };  if sky_asSkyResult(__subject).SkyName == "Ok" { source := sky_asSkyResult(__subject).OkValue; _ = source; return func() any { lexResult := Compiler_Lexer_Lex(source); _ = lexResult; return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Parse error in ", sky_concat(entryPath, sky_concat(": ", e)))) };  if sky_asSkyResult(__subject).SkyName == "Ok" { entryMod := sky_asSkyResult(__subject).OkValue; _ = entryMod; return func() any { entryName := sky_call(sky_stringJoin("."), sky_asMap(entryMod)["name"]); _ = entryName; entryLoaded := map[string]any{"name": entryName, "qualifiedName": sky_asMap(entryMod)["name"], "filePath": entryPath, "ast": entryMod, "checkResult": SkyNothing()}; _ = entryLoaded; resolveResult := Compiler_Resolver_ResolveImports(srcRoot, sky_asMap(entryMod)["imports"], []any{entryLoaded}, sky_setEmpty(), []any{}); _ = resolveResult; allModules := sky_fst(resolveResult); _ = allModules; diags := sky_snd(resolveResult); _ = diags; order := sky_call(sky_listMap(func(m any) any { return sky_asMap(m)["name"] }), sky_listReverse(allModules)); _ = order; return SkyOk(map[string]any{"modules": sky_listReverse(allModules), "order": order, "diagnostics": diags}) }() };  return nil }() }() }() };  return nil }() }()
}

func Compiler_Resolver_ResolveImports(srcRoot any, imports any, loaded any, visited any, diagnostics any) any {
	return func() any { return func() any { __subject := imports; if len(sky_asList(__subject)) == 0 { return SkyTuple2{V0: loaded, V1: diagnostics} };  if len(sky_asList(__subject)) > 0 { imp := sky_asList(__subject)[0]; _ = imp; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { modName := sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]); _ = modName; return func() any { if sky_asBool(sky_asBool(sky_call(sky_setMember(modName), visited)) || sky_asBool(Compiler_Resolver_IsStdlib(modName))) { return Compiler_Resolver_ResolveImports(srcRoot, rest, loaded, visited, diagnostics) }; return func() any { if sky_asBool(sky_equal(sky_asMap(imp)["alias_"], "_")) { return Compiler_Resolver_ResolveImports(srcRoot, rest, loaded, sky_call(sky_setInsert(modName), visited), diagnostics) }; return func() any { filePath := Compiler_Resolver_ResolveModulePath(srcRoot, sky_asMap(imp)["moduleName"]); _ = filePath; return func() any { return func() any { __subject := sky_fileRead(filePath); if sky_asSkyResult(__subject).SkyName == "Err" { return func() any { skyiPath := Compiler_Resolver_ResolveBindingPath(modName); _ = skyiPath; return func() any { return func() any { __subject := sky_fileRead(skyiPath); if sky_asSkyResult(__subject).SkyName == "Ok" { skyiSource := sky_asSkyResult(__subject).OkValue; _ = skyiSource; return func() any { skyiLexResult := Compiler_Lexer_Lex(skyiSource); _ = skyiLexResult; return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(skyiLexResult)["tokens"]); if sky_asSkyResult(__subject).SkyName == "Ok" { skyiMod := sky_asSkyResult(__subject).OkValue; _ = skyiMod; return func() any { loaded2 := map[string]any{"name": modName, "qualifiedName": sky_asMap(imp)["moduleName"], "filePath": skyiPath, "ast": skyiMod, "checkResult": SkyNothing()}; _ = loaded2; return Compiler_Resolver_ResolveImports(srcRoot, rest, append([]any{loaded2}, sky_asList(loaded)...), sky_call(sky_setInsert(modName), visited), diagnostics) }() };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return Compiler_Resolver_ResolveImports(srcRoot, rest, loaded, sky_call(sky_setInsert(modName), visited), sky_concat(diagnostics, []any{sky_concat("Parse error in binding ", sky_concat(modName, sky_concat(": ", e)))})) };  if sky_asSkyResult(__subject).SkyName == "Err" { return Compiler_Resolver_ResolveImports(srcRoot, rest, loaded, sky_call(sky_setInsert(modName), visited), sky_concat(diagnostics, []any{sky_concat("Module not found: ", sky_concat(modName, sky_concat(" (looked at ", sky_concat(filePath, sky_concat(" and ", sky_concat(skyiPath, ")"))))))})) };  if sky_asSkyResult(__subject).SkyName == "Ok" { source := sky_asSkyResult(__subject).OkValue; _ = source; return func() any { lexResult := Compiler_Lexer_Lex(source); _ = lexResult; return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return Compiler_Resolver_ResolveImports(srcRoot, rest, loaded, sky_call(sky_setInsert(modName), visited), sky_concat(diagnostics, []any{sky_concat("Parse error in ", sky_concat(modName, sky_concat(": ", e)))})) };  if sky_asSkyResult(__subject).SkyName == "Ok" { modAst := sky_asSkyResult(__subject).OkValue; _ = modAst; return func() any { newLoaded := map[string]any{"name": modName, "qualifiedName": sky_asMap(imp)["moduleName"], "filePath": filePath, "ast": modAst, "checkResult": SkyNothing()}; _ = newLoaded; newVisited := sky_call(sky_setInsert(modName), visited); _ = newVisited; depResult := Compiler_Resolver_ResolveImports(srcRoot, sky_asMap(modAst)["imports"], append([]any{newLoaded}, sky_asList(loaded)...), newVisited, diagnostics); _ = depResult; return Compiler_Resolver_ResolveImports(srcRoot, rest, sky_fst(depResult), newVisited, sky_snd(depResult)) }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }() }() }() }() };  return nil }() }()
}

func Compiler_Resolver_ResolveModulePath(srcRoot any, parts any) any {
	return sky_concat(srcRoot, sky_concat("/", sky_concat(sky_call(sky_stringJoin("/"), parts), ".sky")))
}

func Compiler_Resolver_ResolveBindingPath(modName any) any {
	return func() any { pkgParts := sky_call(sky_stringSplit("."), modName); _ = pkgParts; safeParts := sky_call(sky_listMap(Compiler_Resolver_CamelToUnderscore), pkgParts); _ = safeParts; safeName := sky_call(sky_stringJoin("_"), safeParts); _ = safeName; return sky_concat(".skycache/go/", sky_concat(safeName, "/bindings.skyi")) }()
}

func Compiler_Resolver_CamelToUnderscore(s any) any {
	return sky_stringToLower(Compiler_Resolver_CamelInsertUnderscores(s, 0, ""))
}

func Compiler_Resolver_CamelInsertUnderscores(s any, idx any, acc any) any {
	return func() any { if sky_asBool(sky_asInt(idx) >= sky_asInt(sky_stringLength(s))) { return acc }; return func() any { ch := sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1), s); _ = ch; isUp := sky_asBool(sky_equal(ch, "A")) || sky_asBool(sky_asBool(sky_equal(ch, "B")) || sky_asBool(sky_asBool(sky_equal(ch, "C")) || sky_asBool(sky_asBool(sky_equal(ch, "D")) || sky_asBool(sky_asBool(sky_equal(ch, "E")) || sky_asBool(sky_asBool(sky_equal(ch, "F")) || sky_asBool(sky_asBool(sky_equal(ch, "G")) || sky_asBool(sky_asBool(sky_equal(ch, "H")) || sky_asBool(sky_asBool(sky_equal(ch, "I")) || sky_asBool(sky_asBool(sky_equal(ch, "J")) || sky_asBool(sky_asBool(sky_equal(ch, "K")) || sky_asBool(sky_asBool(sky_equal(ch, "L")) || sky_asBool(sky_asBool(sky_equal(ch, "M")) || sky_asBool(sky_asBool(sky_equal(ch, "N")) || sky_asBool(sky_asBool(sky_equal(ch, "O")) || sky_asBool(sky_asBool(sky_equal(ch, "P")) || sky_asBool(sky_asBool(sky_equal(ch, "Q")) || sky_asBool(sky_asBool(sky_equal(ch, "R")) || sky_asBool(sky_asBool(sky_equal(ch, "S")) || sky_asBool(sky_asBool(sky_equal(ch, "T")) || sky_asBool(sky_asBool(sky_equal(ch, "U")) || sky_asBool(sky_asBool(sky_equal(ch, "V")) || sky_asBool(sky_asBool(sky_equal(ch, "W")) || sky_asBool(sky_asBool(sky_equal(ch, "X")) || sky_asBool(sky_asBool(sky_equal(ch, "Y")) || sky_asBool(sky_equal(ch, "Z")))))))))))))))))))))))))); _ = isUp; return func() any { if sky_asBool(sky_asBool(isUp) && sky_asBool(sky_asInt(idx) > sky_asInt(0))) { return Compiler_Resolver_CamelInsertUnderscores(s, sky_asInt(idx) + sky_asInt(1), sky_concat(acc, sky_concat("_", ch))) }; return Compiler_Resolver_CamelInsertUnderscores(s, sky_asInt(idx) + sky_asInt(1), sky_concat(acc, ch)) }() }() }()
}

func Compiler_Resolver_IsStdlib(modName any) any {
	return sky_asBool(sky_call(sky_stringStartsWith("Sky.Core."), modName)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Sky.Http."), modName)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Std."), modName)) || sky_asBool(sky_asBool(sky_equal(modName, "Sky.Core.Prelude")) || sky_asBool(sky_equal(modName, "Sky.Interop")))))
}

func Compiler_Resolver_GetStdlibExports(modName any) any {
	return func() any { if sky_asBool(sky_equal(modName, "Std.Html")) { return []any{"text", "raw", "node", "voidNode", "styleNode", "render", "toString", "escapeHtml", "escapeAttr", "attrToString", "div", "span", "p", "h1", "h2", "h3", "h4", "h5", "h6", "a", "button", "form", "label", "textarea", "select", "option", "ul", "ol", "li", "table", "thead", "tbody", "tfoot", "tr", "th", "td", "nav", "headerNode", "footerNode", "section", "article", "aside", "mainNode", "strong", "em", "small", "pre", "codeNode", "blockquote", "input", "br", "hr", "img", "meta", "linkNode", "script", "fieldset", "legend", "body", "doctype", "htmlNode", "headNode", "titleNode"} }; return func() any { if sky_asBool(sky_equal(modName, "Std.Html.Attributes")) { return []any{"attribute", "boolAttribute", "class", "id", "style", "title", "hidden", "tabindex", "lang", "dir", "role", "href", "target", "rel", "download", "type_", "name", "value", "placeholder", "action", "method", "for", "enctype", "required", "disabled", "checked", "readonly", "autofocus", "multiple", "selected", "novalidate", "autocomplete", "minlength", "maxlength", "min", "max", "step", "pattern", "rows", "cols", "src", "alt", "width", "height", "charset", "content", "httpEquiv", "colspan", "rowspan", "scope", "ariaLabel", "ariaHidden", "ariaDescribedby", "ariaExpanded", "dataAttribute"} }; return func() any { if sky_asBool(sky_equal(modName, "Std.Live.Events")) { return []any{"onClick", "onInput", "onSubmit", "onDblClick", "onChange", "onFocus", "onBlur", "onImage", "onFile", "fileMaxSize", "fileMaxWidth", "fileMaxHeight"} }; return func() any { if sky_asBool(sky_equal(modName, "Std.Css")) { return []any{"styles", "rule", "media", "keyframes", "frame", "stylesheet", "property", "px", "rem", "em", "pct", "vh", "vw", "ch", "fr", "sec", "ms", "deg", "zero", "auto", "none", "inherit", "hex", "rgb", "rgba", "hsl", "hsla", "transparent", "cssVar", "cssVarOr", "display", "position", "top", "right_", "bottom", "left", "zIndex", "overflow", "overflowX", "overflowY", "float", "clear", "visibility", "flexDirection", "flexWrap", "justifyContent", "alignItems", "alignContent", "alignSelf", "flex", "flexGrow", "flexShrink", "flexBasis", "order", "gap", "rowGap", "columnGap", "gridTemplateColumns", "gridTemplateRows", "gridColumn", "gridRow", "gridArea", "repeat", "minmax", "margin", "margin2", "margin4", "marginTop", "marginRight", "marginBottom", "marginLeft", "padding", "padding2", "padding4", "paddingTop", "paddingRight", "paddingBottom", "paddingLeft", "width", "height", "minWidth", "maxWidth", "minHeight", "maxHeight", "fontFamily", "fontSize", "fontWeight", "fontStyle", "lineHeight", "letterSpacing", "textAlign", "textDecoration", "textTransform", "textOverflow", "whiteSpace", "wordBreak", "color", "backgroundColor", "background", "backgroundImage", "backgroundSize", "backgroundPosition", "backgroundRepeat", "opacity", "linearGradient", "border", "borderTop", "borderRight", "borderBottom", "borderLeft", "borderColor", "borderWidth", "borderStyle", "borderRadius", "borderRadius4", "borderCollapse", "outline", "outlineOffset", "boxShadow", "shadow", "shadows", "textShadow", "transform", "translateX", "translateY", "scale", "rotate", "transition", "transitionProp", "animation", "cursor", "pointerEvents", "userSelect", "filter", "backdropFilter", "calc", "important", "defineVar", "systemFont", "monoFont", "boxSizingBorderBox"} }; return func() any { if sky_asBool(sky_equal(modName, "Std.Live")) { return []any{"app", "route"} }; return func() any { if sky_asBool(sky_equal(modName, "Std.Cmd")) { return []any{"none", "batch", "perform"} }; return func() any { if sky_asBool(sky_equal(modName, "Std.Sub")) { return []any{"none", "batch"} }; return func() any { if sky_asBool(sky_equal(modName, "Std.Time")) { return []any{"every"} }; return []any{} }() }() }() }() }() }() }() }()
}

func Compiler_Resolver_CheckAllModules(graph any) any {
	return func() any { checkResult := Compiler_Resolver_CheckModulesLoop(sky_asMap(graph)["modules"], Compiler_Env_Empty(), []any{}); _ = checkResult; checkedModules := sky_fst(checkResult); _ = checkedModules; diagnostics := sky_snd(checkResult); _ = diagnostics; return SkyOk(sky_recordUpdate(graph, map[string]any{"modules": checkedModules, "diagnostics": sky_call(sky_listAppend(sky_asMap(graph)["diagnostics"]), diagnostics)})) }()
}

func Compiler_Resolver_CheckModulesLoop(modules any, importedEnv any, diagnostics any) any {
	return func() any { return func() any { __subject := modules; if len(sky_asList(__subject)) == 0 { return SkyTuple2{V0: []any{}, V1: diagnostics} };  if len(sky_asList(__subject)) > 0 { mod := sky_asList(__subject)[0]; _ = mod; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := Compiler_Checker_CheckModule(sky_asMap(mod)["ast"], SkyJust(importedEnv)); if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return func() any { checkedMod := sky_recordUpdate(mod, map[string]any{"checkResult": SkyJust(result)}); _ = checkedMod; newImportedEnv := Compiler_Env_Union(sky_asMap(result)["env"], importedEnv); _ = newImportedEnv; __tup_restModules_restDiags := Compiler_Resolver_CheckModulesLoop(rest, newImportedEnv, sky_call(sky_listAppend(diagnostics), sky_asMap(result)["diagnostics"])); restModules := sky_asTuple2(__tup_restModules_restDiags).V0; _ = restModules; restDiags := sky_asTuple2(__tup_restModules_restDiags).V1; _ = restDiags; return SkyTuple2{V0: append([]any{checkedMod}, sky_asList(restModules)...), V1: restDiags} }() };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { __tup_restModules_restDiags := Compiler_Resolver_CheckModulesLoop(rest, importedEnv, sky_call(sky_listAppend(diagnostics), []any{e})); restModules := sky_asTuple2(__tup_restModules_restDiags).V0; _ = restModules; restDiags := sky_asTuple2(__tup_restModules_restDiags).V1; _ = restDiags; return SkyTuple2{V0: append([]any{mod}, sky_asList(restModules)...), V1: restDiags} }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Resolver_BuildStdlibEnv() any {
	return func() any { prelude := Compiler_Env_CreatePreludeEnv(); _ = prelude; stdFunctions := sky_dictFromList([]any{SkyTuple2{V0: "String.toUpper", V1: mono(TFun(TConst("String"), TConst("String")))}, SkyTuple2{V0: "String.toLower", V1: mono(TFun(TConst("String"), TConst("String")))}, SkyTuple2{V0: "String.length", V1: mono(TFun(TConst("String"), TConst("Int")))}, SkyTuple2{V0: "String.fromInt", V1: mono(TFun(TConst("Int"), TConst("String")))}, SkyTuple2{V0: "String.fromFloat", V1: mono(TFun(TConst("Float"), TConst("String")))}, SkyTuple2{V0: "String.join", V1: mono(TFun(TConst("String"), TFun(TApp(TConst("List"), []any{TConst("String")}), TConst("String"))))}, SkyTuple2{V0: "String.split", V1: mono(TFun(TConst("String"), TFun(TConst("String"), TApp(TConst("List"), []any{TConst("String")}))))}, SkyTuple2{V0: "String.contains", V1: mono(TFun(TConst("String"), TFun(TConst("String"), TConst("Bool"))))}, SkyTuple2{V0: "String.startsWith", V1: mono(TFun(TConst("String"), TFun(TConst("String"), TConst("Bool"))))}, SkyTuple2{V0: "String.trim", V1: mono(TFun(TConst("String"), TConst("String")))}, SkyTuple2{V0: "String.isEmpty", V1: mono(TFun(TConst("String"), TConst("Bool")))}, SkyTuple2{V0: "String.slice", V1: mono(TFun(TConst("Int"), TFun(TConst("Int"), TFun(TConst("String"), TConst("String")))))}, SkyTuple2{V0: "String.append", V1: mono(TFun(TConst("String"), TFun(TConst("String"), TConst("String"))))}, SkyTuple2{V0: "List.map", V1: map[string]any{"quantified": []any{0, 1}, "type_": TFun(TFun(TVar(0, SkyJust("a")), TVar(1, SkyJust("b"))), TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TApp(TConst("List"), []any{TVar(1, SkyJust("b"))})))}}, SkyTuple2{V0: "List.filter", V1: map[string]any{"quantified": []any{0}, "type_": TFun(TFun(TVar(0, SkyJust("a")), TConst("Bool")), TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TApp(TConst("List"), []any{TVar(0, SkyJust("a"))})))}}, SkyTuple2{V0: "List.foldl", V1: map[string]any{"quantified": []any{0, 1}, "type_": TFun(TFun(TVar(0, SkyJust("a")), TFun(TVar(1, SkyJust("b")), TVar(1, SkyJust("b")))), TFun(TVar(1, SkyJust("b")), TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TVar(1, SkyJust("b")))))}}, SkyTuple2{V0: "List.foldr", V1: map[string]any{"quantified": []any{0, 1}, "type_": TFun(TFun(TVar(0, SkyJust("a")), TFun(TVar(1, SkyJust("b")), TVar(1, SkyJust("b")))), TFun(TVar(1, SkyJust("b")), TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TVar(1, SkyJust("b")))))}}, SkyTuple2{V0: "List.head", V1: map[string]any{"quantified": []any{0}, "type_": TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TApp(TConst("Maybe"), []any{TVar(0, SkyJust("a"))}))}}, SkyTuple2{V0: "List.length", V1: map[string]any{"quantified": []any{0}, "type_": TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TConst("Int"))}}, SkyTuple2{V0: "List.reverse", V1: map[string]any{"quantified": []any{0}, "type_": TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}))}}, SkyTuple2{V0: "List.isEmpty", V1: map[string]any{"quantified": []any{0}, "type_": TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TConst("Bool"))}}, SkyTuple2{V0: "List.append", V1: map[string]any{"quantified": []any{0}, "type_": TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TApp(TConst("List"), []any{TVar(0, SkyJust("a"))})))}}, SkyTuple2{V0: "List.concatMap", V1: map[string]any{"quantified": []any{0, 1}, "type_": TFun(TFun(TVar(0, SkyJust("a")), TApp(TConst("List"), []any{TVar(1, SkyJust("b"))})), TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TApp(TConst("List"), []any{TVar(1, SkyJust("b"))})))}}, SkyTuple2{V0: "Dict.empty", V1: map[string]any{"quantified": []any{0, 1}, "type_": TApp(TConst("Dict"), []any{TVar(0, SkyJust("k")), TVar(1, SkyJust("v"))})}}, SkyTuple2{V0: "Dict.insert", V1: map[string]any{"quantified": []any{0, 1}, "type_": TFun(TVar(0, SkyJust("k")), TFun(TVar(1, SkyJust("v")), TFun(TApp(TConst("Dict"), []any{TVar(0, SkyJust("k")), TVar(1, SkyJust("v"))}), TApp(TConst("Dict"), []any{TVar(0, SkyJust("k")), TVar(1, SkyJust("v"))}))))}}, SkyTuple2{V0: "Dict.get", V1: map[string]any{"quantified": []any{0, 1}, "type_": TFun(TVar(0, SkyJust("k")), TFun(TApp(TConst("Dict"), []any{TVar(0, SkyJust("k")), TVar(1, SkyJust("v"))}), TApp(TConst("Maybe"), []any{TVar(1, SkyJust("v"))})))}}, SkyTuple2{V0: "Log.println", V1: mono(TFun(TConst("String"), TConst("Unit")))}, SkyTuple2{V0: "println", V1: mono(TFun(TConst("String"), TConst("Unit")))}, SkyTuple2{V0: "Process.exit", V1: mono(TFun(TConst("Int"), TConst("Unit")))}, SkyTuple2{V0: "File.readFile", V1: mono(TFun(TConst("String"), TApp(TConst("Result"), []any{TConst("String"), TConst("String")})))}, SkyTuple2{V0: "File.writeFile", V1: mono(TFun(TConst("String"), TFun(TConst("String"), TApp(TConst("Result"), []any{TConst("String"), TConst("Unit")}))))}, SkyTuple2{V0: "File.mkdirAll", V1: mono(TFun(TConst("String"), TApp(TConst("Result"), []any{TConst("String"), TConst("Unit")})))}, SkyTuple2{V0: "Args.getArgs", V1: mono(TFun(TConst("Unit"), TApp(TConst("List"), []any{TConst("String")})))}, SkyTuple2{V0: "Args.getArg", V1: mono(TFun(TConst("Int"), TApp(TConst("Maybe"), []any{TConst("String")})))}, SkyTuple2{V0: "Ref.new", V1: map[string]any{"quantified": []any{0}, "type_": TFun(TVar(0, SkyJust("a")), TApp(TConst("Ref"), []any{TVar(0, SkyJust("a"))}))}}, SkyTuple2{V0: "Ref.get", V1: map[string]any{"quantified": []any{0}, "type_": TFun(TApp(TConst("Ref"), []any{TVar(0, SkyJust("a"))}), TVar(0, SkyJust("a")))}}, SkyTuple2{V0: "Ref.set", V1: map[string]any{"quantified": []any{0}, "type_": TFun(TVar(0, SkyJust("a")), TFun(TApp(TConst("Ref"), []any{TVar(0, SkyJust("a"))}), TConst("Unit")))}}, SkyTuple2{V0: "Task.succeed", V1: map[string]any{"quantified": []any{0, 1}, "type_": TFun(TVar(0, SkyJust("a")), TApp(TConst("Task"), []any{TVar(1, SkyJust("err")), TVar(0, SkyJust("a"))}))}}, SkyTuple2{V0: "Task.fail", V1: map[string]any{"quantified": []any{0, 1}, "type_": TFun(TVar(0, SkyJust("err")), TApp(TConst("Task"), []any{TVar(0, SkyJust("err")), TVar(1, SkyJust("a"))}))}}, SkyTuple2{V0: "Task.andThen", V1: map[string]any{"quantified": []any{0, 1, 2}, "type_": TFun(TFun(TVar(0, SkyJust("a")), TApp(TConst("Task"), []any{TVar(2, SkyJust("err")), TVar(1, SkyJust("b"))})), TFun(TApp(TConst("Task"), []any{TVar(2, SkyJust("err")), TVar(0, SkyJust("a"))}), TApp(TConst("Task"), []any{TVar(2, SkyJust("err")), TVar(1, SkyJust("b"))})))}}, SkyTuple2{V0: "Task.map", V1: map[string]any{"quantified": []any{0, 1, 2}, "type_": TFun(TFun(TVar(0, SkyJust("a")), TVar(1, SkyJust("b"))), TFun(TApp(TConst("Task"), []any{TVar(2, SkyJust("err")), TVar(0, SkyJust("a"))}), TApp(TConst("Task"), []any{TVar(2, SkyJust("err")), TVar(1, SkyJust("b"))})))}}, SkyTuple2{V0: "Task.perform", V1: map[string]any{"quantified": []any{0, 1}, "type_": TFun(TApp(TConst("Task"), []any{TVar(0, SkyJust("err")), TVar(1, SkyJust("a"))}), TApp(TConst("Result"), []any{TVar(0, SkyJust("err")), TVar(1, SkyJust("a"))}))}}, SkyTuple2{V0: "Task.sequence", V1: map[string]any{"quantified": []any{0, 1}, "type_": TFun(TApp(TConst("List"), []any{TApp(TConst("Task"), []any{TVar(0, SkyJust("err")), TVar(1, SkyJust("a"))})}), TApp(TConst("Task"), []any{TVar(0, SkyJust("err")), TApp(TConst("List"), []any{TVar(1, SkyJust("a"))})}))}}}); _ = stdFunctions; return Compiler_Env_Union(stdFunctions, prelude) }()
}

func Compiler_Resolver_IsFfiBinding(modName any) any {
	return func() any { skyiPath := Compiler_Resolver_ResolveBindingPath(modName); _ = skyiPath; return func() any { return func() any { __subject := sky_fileRead(skyiPath); if sky_asSkyResult(__subject).SkyName == "Ok" { return true };  if sky_asSkyResult(__subject).SkyName == "Err" { return false };  return nil }() }() }()
}

func Compiler_Resolver_ModuleNameToGoPath(modName any) any {
	return func() any { parts := sky_call(sky_stringSplit("."), modName); _ = parts; lowerParts := sky_call(sky_listMap(sky_stringToLower), parts); _ = lowerParts; return sky_call(sky_stringJoin("/"), lowerParts) }()
}

func Compiler_Resolver_ModuleNameToSafeName(modName any) any {
	return func() any { parts := sky_call(sky_stringSplit("."), modName); _ = parts; lowerParts := sky_call(sky_listMap(sky_stringToLower), parts); _ = lowerParts; return sky_call(sky_stringJoin("_"), lowerParts) }()
}

func Compiler_Resolver_ResolveWrapperPath(modName any) any {
	return func() any { safeName := Compiler_Resolver_ModuleNameToSafeName(modName); _ = safeName; return sky_concat(".skycache/go/", sky_concat(safeName, sky_concat("/sky_wrappers/", sky_concat(safeName, ".go")))) }()
}

func Compiler_Resolver_CollectForeignImports(imports any) any {
	return sky_call(sky_listFilterMap(func(imp any) any { return func() any { modName := sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]); _ = modName; return func() any { if sky_asBool(Compiler_Resolver_IsStdlib(modName)) { return SkyNothing() }; return func() any { if sky_asBool(sky_equal(sky_asMap(imp)["alias_"], "_")) { return SkyJust(SkyTuple3{V0: modName, V1: Compiler_Resolver_ModuleNameToGoPath(modName), V2: true}) }; return func() any { if sky_asBool(Compiler_Resolver_IsFfiBinding(modName)) { return SkyJust(SkyTuple3{V0: modName, V1: Compiler_Resolver_ModuleNameToGoPath(modName), V2: false}) }; return SkyNothing() }() }() }() }() }), imports)
}

func Compiler_Resolver_CollectAllForeignImports(entryImports any, loadedModules any) any {
	return func() any { entryForeign := Compiler_Resolver_CollectForeignImports(entryImports); _ = entryForeign; depForeign := sky_call(sky_listConcatMap(func(pair any) any { return func() any { mod := sky_snd(pair); _ = mod; return Compiler_Resolver_CollectForeignImports(sky_asMap(mod)["imports"]) }() }), loadedModules); _ = depForeign; return Compiler_Resolver_DeduplicateForeignImports(sky_call(sky_listAppend(entryForeign), depForeign)) }()
}

func Compiler_Resolver_DeduplicateForeignImports(imports any) any {
	return Compiler_Resolver_DeduplicateForeignLoop(imports, sky_setEmpty(), []any{})
}

func Compiler_Resolver_DeduplicateForeignLoop(imports any, seen any, acc any) any {
	return func() any { return func() any { __subject := imports; if len(sky_asList(__subject)) == 0 { return sky_listReverse(acc) };  if len(sky_asList(__subject)) > 0 { item := sky_asList(__subject)[0]; _ = item; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { modName := Compiler_Resolver_TripleFirst(item); _ = modName; return func() any { if sky_asBool(sky_call(sky_setMember(modName), seen)) { return Compiler_Resolver_DeduplicateForeignLoop(rest, seen, acc) }; return Compiler_Resolver_DeduplicateForeignLoop(rest, sky_call(sky_setInsert(modName), seen), append([]any{item}, sky_asList(acc)...)) }() }() };  return nil }() }()
}

func Compiler_Resolver_TripleFirst(triple any) any {
	return func() any { return func() any { __subject := triple; if true { a := sky_asTuple3(__subject).V0; _ = a; return a };  return nil }() }()
}

func Compiler_Resolver_TripleSecond(triple any) any {
	return func() any { return func() any { __subject := triple; if true { b := sky_asTuple3(__subject).V1; _ = b; return b };  return nil }() }()
}

func Compiler_Resolver_TripleThird(triple any) any {
	return func() any { return func() any { __subject := triple; if true { c := sky_asTuple3(__subject).V2; _ = c; return c };  return nil }() }()
}

var emptyCtx = Compiler_Lower_EmptyCtx

var BuildExposedStdlib = Compiler_Lower_BuildExposedStdlib

var GetExposedNames = Compiler_Lower_GetExposedNames

var LowerModule = Compiler_Lower_LowerModule

var LowerDeclarations = Compiler_Lower_LowerDeclarations

var LowerDecl = Compiler_Lower_LowerDecl

var LowerFunction = Compiler_Lower_LowerFunction

var GenerateParamBindings = Compiler_Lower_GenerateParamBindings

var GenerateOneParamBinding = Compiler_Lower_GenerateOneParamBinding

var LowerParam = Compiler_Lower_LowerParam

var LowerExpr = Compiler_Lower_LowerExpr

var LowerIdentifier = Compiler_Lower_LowerIdentifier

var LowerStdlibExposed = Compiler_Lower_LowerStdlibExposed

var CssNameToProperty = Compiler_Lower_CssNameToProperty

var CssNameToPropertyLoop = Compiler_Lower_CssNameToPropertyLoop

var LowerConstructorValue = Compiler_Lower_LowerConstructorValue

var LowerQualified = Compiler_Lower_LowerQualified

var LowerCall = Compiler_Lower_LowerCall

var IsDynamicCallee = Compiler_Lower_IsDynamicCallee

var ExtractPatternName = Compiler_Lower_ExtractPatternName

var IsWellKnownIdent = Compiler_Lower_IsWellKnownIdent

var IsUpperStart = Compiler_Lower_IsUpperStart

var CheckPartialApplication = Compiler_Lower_CheckPartialApplication

var GeneratePartialClosure = Compiler_Lower_GeneratePartialClosure

var BuildCurriedClosure = Compiler_Lower_BuildCurriedClosure

var ListRange = Compiler_Lower_ListRange

var FlattenCall = Compiler_Lower_FlattenCall

var LowerLambda = Compiler_Lower_LowerLambda

var LowerIf = Compiler_Lower_LowerIf

var LowerLet = Compiler_Lower_LowerLet

var LowerLetBinding = Compiler_Lower_LowerLetBinding

var ExtractTupleBindings = Compiler_Lower_ExtractTupleBindings

var LowerCase = Compiler_Lower_LowerCase

var LowerCaseToSwitch = Compiler_Lower_LowerCaseToSwitch

var EmitBranchCode = Compiler_Lower_EmitBranchCode

var PatternToCondition = Compiler_Lower_PatternToCondition

var LiteralCondition = Compiler_Lower_LiteralCondition

var PatternToBindings = Compiler_Lower_PatternToBindings

var BindConstructorArgs = Compiler_Lower_BindConstructorArgs

var BindAdtConstructorArgs = Compiler_Lower_BindAdtConstructorArgs

var BindTupleArgs = Compiler_Lower_BindTupleArgs

var BindListArgs = Compiler_Lower_BindListArgs

var LowerBinary = Compiler_Lower_LowerBinary

var LowerRecordUpdate = Compiler_Lower_LowerRecordUpdate

var GenerateConstructorDecls = Compiler_Lower_GenerateConstructorDecls

var GenerateCtorsForDecl = Compiler_Lower_GenerateCtorsForDecl

var GenerateCtorFunc = Compiler_Lower_GenerateCtorFunc

var GenerateHelperDecls = Compiler_Lower_GenerateHelperDecls

var CollectLocalFunctions = Compiler_Lower_CollectLocalFunctions

var CollectLocalFunctionArities = Compiler_Lower_CollectLocalFunctionArities

var BuildConstructorMap = Compiler_Lower_BuildConstructorMap

var AddCtorsFromList = Compiler_Lower_AddCtorsFromList

var CountFunArgs = Compiler_Lower_CountFunArgs

var SanitizeGoIdent = Compiler_Lower_SanitizeGoIdent

var IsGoKeyword = Compiler_Lower_IsGoKeyword

var IsStdlibCallee = Compiler_Lower_IsStdlibCallee

var MakeTupleKey = Compiler_Lower_MakeTupleKey

var GetPatVarName = Compiler_Lower_GetPatVarName

var IsParamOrBuiltin = Compiler_Lower_IsParamOrBuiltin

var LowerArgExpr = Compiler_Lower_LowerArgExpr

var IsZeroArityFn = Compiler_Lower_IsZeroArityFn

var GetFnArity = Compiler_Lower_GetFnArity

var MakeCurryWrapper = Compiler_Lower_MakeCurryWrapper

var ListContains = Compiler_Lower_ListContains

var IsLocalFn = Compiler_Lower_IsLocalFn

var IsLocalFunction = Compiler_Lower_IsLocalFunction

var GoQuote = Compiler_Lower_GoQuote

var CapitalizeFirst = Compiler_Lower_CapitalizeFirst

var LastPartOf = Compiler_Lower_LastPartOf

var ListGet = Compiler_Lower_ListGet

var ZipIndex = Compiler_Lower_ZipIndex

var ZipIndexLoop = Compiler_Lower_ZipIndexLoop

var EmitGoExprInline = Compiler_Lower_EmitGoExprInline

var EmitMapEntry = Compiler_Lower_EmitMapEntry

var EmitInlineParam = Compiler_Lower_EmitInlineParam

var ExprToGoString = Compiler_Lower_ExprToGoString

var LowerExprToStmts = Compiler_Lower_LowerExprToStmts

var StmtsToGoString = Compiler_Lower_StmtsToGoString

var FixCurriedCalls = Compiler_Lower_FixCurriedCalls

var StmtToGoString = Compiler_Lower_StmtToGoString

var GoIdent = Compiler_GoIr_GoIdent

var GoBasicLit = Compiler_GoIr_GoBasicLit

var GoStringLit = Compiler_GoIr_GoStringLit

var GoCallExpr = Compiler_GoIr_GoCallExpr

var GoSelectorExpr = Compiler_GoIr_GoSelectorExpr

var GoSliceLit = Compiler_GoIr_GoSliceLit

var GoMapLit = Compiler_GoIr_GoMapLit

var GoFuncLit = Compiler_GoIr_GoFuncLit

var GoRawExpr = Compiler_GoIr_GoRawExpr

var GoCompositeLit = Compiler_GoIr_GoCompositeLit

var GoBinaryExpr = Compiler_GoIr_GoBinaryExpr

var GoUnaryExpr = Compiler_GoIr_GoUnaryExpr

var GoIndexExpr = Compiler_GoIr_GoIndexExpr

var GoNilExpr = Compiler_GoIr_GoNilExpr

var GoExprStmt = Compiler_GoIr_GoExprStmt

var GoAssign = Compiler_GoIr_GoAssign

var GoShortDecl = Compiler_GoIr_GoShortDecl

var GoReturn = Compiler_GoIr_GoReturn

var GoReturnVoid = Compiler_GoIr_GoReturnVoid

var GoIf = Compiler_GoIr_GoIf

var GoBlock = Compiler_GoIr_GoBlock

var GoDeclFunc = Compiler_GoIr_GoDeclFunc

var GoDeclVar = Compiler_GoIr_GoDeclVar

var GoDeclRaw = Compiler_GoIr_GoDeclRaw

var emptyRegistry = Compiler_Adt_EmptyRegistry()

var EmptyRegistry = Compiler_Adt_EmptyRegistry()

var lookupConstructor = Compiler_Adt_LookupConstructor

var LookupConstructor = Compiler_Adt_LookupConstructor

var lookupCtorInEntries = Compiler_Adt_LookupCtorInEntries

var LookupCtorInEntries = Compiler_Adt_LookupCtorInEntries

var lookupConstructorAdt = Compiler_Adt_LookupConstructorAdt

var LookupConstructorAdt = Compiler_Adt_LookupConstructorAdt

var lookupCtorAdtInEntries = Compiler_Adt_LookupCtorAdtInEntries

var LookupCtorAdtInEntries = Compiler_Adt_LookupCtorAdtInEntries

var registerAdts = Compiler_Adt_RegisterAdts

var RegisterAdts = Compiler_Adt_RegisterAdts

var registerAdtsLoop = Compiler_Adt_RegisterAdtsLoop

var RegisterAdtsLoop = Compiler_Adt_RegisterAdtsLoop

var registerOneAdt = Compiler_Adt_RegisterOneAdt

var RegisterOneAdt = Compiler_Adt_RegisterOneAdt

var buildConstructorScheme = Compiler_Adt_BuildConstructorScheme

var BuildConstructorScheme = Compiler_Adt_BuildConstructorScheme

var buildParamMap = Compiler_Adt_BuildParamMap

var BuildParamMap = Compiler_Adt_BuildParamMap

var buildFunType = Compiler_Adt_BuildFunType

var BuildFunType = Compiler_Adt_BuildFunType

var getVarId = Compiler_Adt_GetVarId

var GetVarId = Compiler_Adt_GetVarId

var resolveTypeExpr = Compiler_Adt_ResolveTypeExpr

var ResolveTypeExpr = Compiler_Adt_ResolveTypeExpr

var initState = Compiler_ParserCore_InitState

var InitState = Compiler_ParserCore_InitState

var peek = Compiler_ParserCore_Peek

var Peek = Compiler_ParserCore_Peek

var peekAt = Compiler_ParserCore_PeekAt

var PeekAt = Compiler_ParserCore_PeekAt

var previous = Compiler_ParserCore_Previous

var Previous = Compiler_ParserCore_Previous

var advance = Compiler_ParserCore_Advance

var Advance = Compiler_ParserCore_Advance

var matchKind = Compiler_ParserCore_MatchKind

var MatchKind = Compiler_ParserCore_MatchKind

var matchLexeme = Compiler_ParserCore_MatchLexeme

var MatchLexeme = Compiler_ParserCore_MatchLexeme

var matchKindLex = Compiler_ParserCore_MatchKindLex

var MatchKindLex = Compiler_ParserCore_MatchKindLex

var consume = Compiler_ParserCore_Consume

var Consume = Compiler_ParserCore_Consume

var consumeLex = Compiler_ParserCore_ConsumeLex

var ConsumeLex = Compiler_ParserCore_ConsumeLex

var tokenKindEq = Compiler_ParserCore_TokenKindEq

var TokenKindEq = Compiler_ParserCore_TokenKindEq

var tokenKindStr = Compiler_ParserCore_TokenKindStr

var TokenKindStr = Compiler_ParserCore_TokenKindStr

var parseQualifiedParts = Compiler_ParserCore_ParseQualifiedParts

var ParseQualifiedParts = Compiler_ParserCore_ParseQualifiedParts

var peekLexeme = Compiler_ParserCore_PeekLexeme

var PeekLexeme = Compiler_ParserCore_PeekLexeme

var peekColumn = Compiler_ParserCore_PeekColumn

var PeekColumn = Compiler_ParserCore_PeekColumn

var peekKind = Compiler_ParserCore_PeekKind

var PeekKind = Compiler_ParserCore_PeekKind

var peekAt1Kind = Compiler_ParserCore_PeekAt1Kind

var PeekAt1Kind = Compiler_ParserCore_PeekAt1Kind

var unescapeString = Compiler_ParserCore_UnescapeString

var UnescapeString = Compiler_ParserCore_UnescapeString

var filterLayout = Compiler_ParserCore_FilterLayout

var FilterLayout = Compiler_ParserCore_FilterLayout

var resolveProject = Compiler_Resolver_ResolveProject

var resolveImports = Compiler_Resolver_ResolveImports

var resolveModulePath = Compiler_Resolver_ResolveModulePath

var resolveBindingPath = Compiler_Resolver_ResolveBindingPath

var camelToUnderscore = Compiler_Resolver_CamelToUnderscore

var camelInsertUnderscores = Compiler_Resolver_CamelInsertUnderscores

var isStdlib = Compiler_Resolver_IsStdlib

var getStdlibExports = Compiler_Resolver_GetStdlibExports

var checkAllModules = Compiler_Resolver_CheckAllModules

var checkModulesLoop = Compiler_Resolver_CheckModulesLoop

var buildStdlibEnv = Compiler_Resolver_BuildStdlibEnv()

var isFfiBinding = Compiler_Resolver_IsFfiBinding

var moduleNameToGoPath = Compiler_Resolver_ModuleNameToGoPath

var moduleNameToSafeName = Compiler_Resolver_ModuleNameToSafeName

var resolveWrapperPath = Compiler_Resolver_ResolveWrapperPath

var collectForeignImports = Compiler_Resolver_CollectForeignImports

var collectAllForeignImports = Compiler_Resolver_CollectAllForeignImports

var deduplicateForeignImports = Compiler_Resolver_DeduplicateForeignImports

var deduplicateForeignLoop = Compiler_Resolver_DeduplicateForeignLoop

var tripleFirst = Compiler_Resolver_TripleFirst

var tripleSecond = Compiler_Resolver_TripleSecond

var tripleThird = Compiler_Resolver_TripleThird

func Compiler_Lower_EmptyCtx() any {
	return map[string]any{"registry": Compiler_Adt_EmptyRegistry(), "moduleExports": sky_dictEmpty(), "importedConstructors": sky_dictEmpty(), "localFunctions": []any{}, "collectedImports": sky_setEmpty(), "importAliases": sky_dictEmpty(), "modulePrefix": "", "localFunctionArity": sky_dictEmpty(), "exposedStdlib": sky_dictEmpty(), "paramNames": sky_setEmpty()}
}

func Compiler_Lower_BuildExposedStdlib(imports any) any {
	return sky_call2(sky_listFoldl(func(imp any) any { return func(acc any) any { return func() any { modName := sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]); _ = modName; return func() any { names := Compiler_Lower_GetExposedNames(imp); _ = names; return sky_call2(sky_listFoldl(func(n any) any { return func(dict any) any { return sky_call2(sky_dictInsert(n), modName, dict) } }), acc, names) }() }() } }), sky_dictEmpty(), imports)
}

func Compiler_Lower_GetExposedNames(imp any) any {
	return func() any { modName := sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]); _ = modName; return func() any { return func() any { __subject := sky_asMap(imp)["exposing_"]; if sky_asMap(__subject)["SkyName"] == "ExposeAll" { return Compiler_Resolver_GetStdlibExports(modName) };  if sky_asMap(__subject)["SkyName"] == "ExposeList" { names := sky_asMap(__subject)["V0"]; _ = names; return sky_call(sky_listFilter(func(n any) any { return Compiler_Lower_ListContains(n, Compiler_Resolver_GetStdlibExports(modName)) }), names) };  if sky_asMap(__subject)["SkyName"] == "ExposeNone" { return []any{} };  return nil }() }() }()
}

func Compiler_Lower_LowerModule(registry any, mod any) any {
	return func() any { exposed := Compiler_Lower_BuildExposedStdlib(sky_asMap(mod)["imports"]); _ = exposed; ctx := sky_recordUpdate(Compiler_Lower_EmptyCtx(), map[string]any{"registry": registry, "localFunctions": Compiler_Lower_CollectLocalFunctions(sky_asMap(mod)["declarations"]), "importedConstructors": Compiler_Lower_BuildConstructorMap(registry), "exposedStdlib": exposed, "localFunctionArity": Compiler_Lower_CollectLocalFunctionArities(sky_asMap(mod)["declarations"])}); _ = ctx; goDecls := Compiler_Lower_LowerDeclarations(ctx, sky_asMap(mod)["declarations"]); _ = goDecls; ctorDecls := Compiler_Lower_GenerateConstructorDecls(registry, sky_asMap(mod)["declarations"]); _ = ctorDecls; imports := []any{map[string]any{"path": "fmt", "alias_": ""}, map[string]any{"path": "bufio", "alias_": ""}, map[string]any{"path": "io", "alias_": ""}, map[string]any{"path": "os", "alias_": ""}, map[string]any{"path": "os/exec", "alias_": "exec"}, map[string]any{"path": "net/http", "alias_": "net_http"}, map[string]any{"path": "strconv", "alias_": ""}, map[string]any{"path": "strings", "alias_": ""}, map[string]any{"path": "sort", "alias_": ""}, map[string]any{"path": "context", "alias_": ""}, map[string]any{"path": "math", "alias_": ""}, map[string]any{"path": "crypto/sha256", "alias_": "crypto_sha256"}, map[string]any{"path": "crypto/md5", "alias_": "crypto_md5"}, map[string]any{"path": "encoding/hex", "alias_": "hex"}, map[string]any{"path": "encoding/base64", "alias_": "base64"}, map[string]any{"path": "encoding/json", "alias_": "encoding_json"}, map[string]any{"path": "time", "alias_": ""}}; _ = imports; helperDecls := Compiler_Lower_GenerateHelperDecls(); _ = helperDecls; return map[string]any{"name": "main", "imports": imports, "declarations": sky_listConcat([]any{helperDecls, ctorDecls, goDecls})} }()
}

func Compiler_Lower_LowerDeclarations(ctx any, decls any) any {
	return sky_call(sky_listFilterMap(func(__pa0 any) any { return Compiler_Lower_LowerDecl(ctx, __pa0) }), decls)
}

func Compiler_Lower_LowerDecl(ctx any, decl any) any {
	return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "FunDecl" { name := sky_asMap(__subject)["V0"]; _ = name; params := sky_asMap(__subject)["V1"]; _ = params; body := sky_asMap(__subject)["V2"]; _ = body; return SkyJust(Compiler_Lower_LowerFunction(ctx, name, params, body)) };  if true { return SkyNothing() };  return nil }() }()
}

func Compiler_Lower_LowerFunction(ctx any, name any, params any, body any) any {
	return func() any { isMain := sky_equal(name, "main"); _ = isMain; goName := func() any { if sky_asBool(isMain) { return "main" }; return Compiler_Lower_SanitizeGoIdent(name) }(); _ = goName; goParams := sky_call(sky_listMap(Compiler_Lower_LowerParam), params); _ = goParams; returnType := func() any { if sky_asBool(isMain) { return "" }; return "any" }(); _ = returnType; pNames := sky_call(sky_listFilterMap(Compiler_Lower_ExtractPatternName), params); _ = pNames; bodyCtx := sky_recordUpdate(ctx, map[string]any{"paramNames": sky_call(sky_setUnion(sky_asMap(ctx)["paramNames"]), sky_setFromList(pNames))}); _ = bodyCtx; paramBindings := Compiler_Lower_GenerateParamBindings(bodyCtx, params); _ = paramBindings; goBody := func() any { if sky_asBool(isMain) { return sky_call(sky_listAppend(paramBindings), []any{GoExprStmt(GoRawExpr(sky_concat("sky_runMainTask(", sky_concat(Compiler_Lower_EmitGoExprInline(Compiler_Lower_LowerExpr(bodyCtx, body)), ")"))))}) }; return sky_call(sky_listAppend(paramBindings), []any{GoReturn(Compiler_Lower_LowerExpr(bodyCtx, body))}) }(); _ = goBody; return GoDeclFunc(map[string]any{"name": goName, "params": goParams, "returnType": returnType, "body": goBody}) }()
}

func Compiler_Lower_GenerateParamBindings(ctx any, params any) any {
	return sky_call(sky_listConcatMap(func(__pa0 any) any { return Compiler_Lower_GenerateOneParamBinding(ctx, __pa0) }), params)
}

func Compiler_Lower_GenerateOneParamBinding(ctx any, pat any) any {
	return func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PVariable" { return []any{} };  if sky_asMap(__subject)["SkyName"] == "PWildcard" { return []any{} };  if sky_asMap(__subject)["SkyName"] == "PTuple" { items := sky_asMap(__subject)["V0"]; _ = items; return func() any { paramName := "_p"; _ = paramName; bindings := Compiler_Lower_BindTupleArgs(ctx, paramName, items, 0); _ = bindings; return func() any { if sky_asBool(sky_stringIsEmpty(bindings)) { return []any{} }; return []any{GoExprStmt(GoRawExpr(bindings))} }() }() };  if true { return []any{} };  return nil }() }()
}

func Compiler_Lower_LowerParam(pat any) any {
	return func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PVariable" { name := sky_asMap(__subject)["V0"]; _ = name; return map[string]any{"name": Compiler_Lower_SanitizeGoIdent(name), "type_": "any"} };  if sky_asMap(__subject)["SkyName"] == "PWildcard" { return map[string]any{"name": "_", "type_": "any"} };  if true { return map[string]any{"name": "_p", "type_": "any"} };  return nil }() }()
}

func Compiler_Lower_LowerExpr(ctx any, expr any) any {
	return func() any { return func() any { __subject := expr; if sky_asMap(__subject)["SkyName"] == "IntLitExpr" { raw := sky_asMap(__subject)["V1"]; _ = raw; return GoBasicLit(raw) };  if sky_asMap(__subject)["SkyName"] == "FloatLitExpr" { raw := sky_asMap(__subject)["V1"]; _ = raw; return GoBasicLit(raw) };  if sky_asMap(__subject)["SkyName"] == "StringLitExpr" { s := sky_asMap(__subject)["V0"]; _ = s; return GoStringLit(s) };  if sky_asMap(__subject)["SkyName"] == "CharLitExpr" { s := sky_asMap(__subject)["V0"]; _ = s; return GoCallExpr(GoIdent("string"), []any{GoBasicLit(sky_concat("'", sky_concat(sky_call2(sky_stringSlice(1), sky_asInt(sky_stringLength(s)) - sky_asInt(1), s), "'")))}) };  if sky_asMap(__subject)["SkyName"] == "BoolLitExpr" { b := sky_asMap(__subject)["V0"]; _ = b; return func() any { if sky_asBool(b) { return GoIdent("true") }; return GoIdent("false") }() };  if sky_asMap(__subject)["SkyName"] == "UnitExpr" { return GoRawExpr("struct{}{}") };  if sky_asMap(__subject)["SkyName"] == "IdentifierExpr" { name := sky_asMap(__subject)["V0"]; _ = name; return Compiler_Lower_LowerIdentifier(ctx, name) };  if sky_asMap(__subject)["SkyName"] == "QualifiedExpr" { parts := sky_asMap(__subject)["V0"]; _ = parts; return Compiler_Lower_LowerQualified(ctx, parts) };  if sky_asMap(__subject)["SkyName"] == "TupleExpr" { items := sky_asMap(__subject)["V0"]; _ = items; return func() any { goItems := sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Lower_LowerExpr(ctx, __pa0) }), items); _ = goItems; n := sky_listLength(items); _ = n; return func() any { if sky_asBool(sky_equal(n, 2)) { return GoRawExpr(sky_concat("SkyTuple2{V0: ", sky_concat(Compiler_Lower_EmitGoExprInline(Compiler_Lower_ListGet(0, goItems)), sky_concat(", V1: ", sky_concat(Compiler_Lower_EmitGoExprInline(Compiler_Lower_ListGet(1, goItems)), "}"))))) }; return func() any { if sky_asBool(sky_equal(n, 3)) { return GoRawExpr(sky_concat("SkyTuple3{V0: ", sky_concat(Compiler_Lower_EmitGoExprInline(Compiler_Lower_ListGet(0, goItems)), sky_concat(", V1: ", sky_concat(Compiler_Lower_EmitGoExprInline(Compiler_Lower_ListGet(1, goItems)), sky_concat(", V2: ", sky_concat(Compiler_Lower_EmitGoExprInline(Compiler_Lower_ListGet(2, goItems)), "}"))))))) }; return GoSliceLit(goItems) }() }() }() };  if sky_asMap(__subject)["SkyName"] == "ListExpr" { items := sky_asMap(__subject)["V0"]; _ = items; return GoSliceLit(sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Lower_LowerExpr(ctx, __pa0) }), items)) };  if sky_asMap(__subject)["SkyName"] == "RecordExpr" { fields := sky_asMap(__subject)["V0"]; _ = fields; return GoMapLit(sky_call(sky_listMap(func(f any) any { return SkyTuple2{V0: GoStringLit(sky_asMap(f)["name"]), V1: Compiler_Lower_LowerExpr(ctx, sky_asMap(f)["value"])} }), fields)) };  if sky_asMap(__subject)["SkyName"] == "RecordUpdateExpr" { base := sky_asMap(__subject)["V0"]; _ = base; fields := sky_asMap(__subject)["V1"]; _ = fields; return Compiler_Lower_LowerRecordUpdate(ctx, base, fields) };  if sky_asMap(__subject)["SkyName"] == "FieldAccessExpr" { target := sky_asMap(__subject)["V0"]; _ = target; fieldName := sky_asMap(__subject)["V1"]; _ = fieldName; return GoIndexExpr(GoCallExpr(GoIdent("sky_asMap"), []any{Compiler_Lower_LowerExpr(ctx, target)}), GoStringLit(fieldName)) };  if sky_asMap(__subject)["SkyName"] == "CallExpr" { callee := sky_asMap(__subject)["V0"]; _ = callee; args := sky_asMap(__subject)["V1"]; _ = args; return Compiler_Lower_LowerCall(ctx, callee, args) };  if sky_asMap(__subject)["SkyName"] == "LambdaExpr" { params := sky_asMap(__subject)["V0"]; _ = params; body := sky_asMap(__subject)["V1"]; _ = body; return Compiler_Lower_LowerLambda(ctx, params, body) };  if sky_asMap(__subject)["SkyName"] == "IfExpr" { condition := sky_asMap(__subject)["V0"]; _ = condition; thenBranch := sky_asMap(__subject)["V1"]; _ = thenBranch; elseBranch := sky_asMap(__subject)["V2"]; _ = elseBranch; return Compiler_Lower_LowerIf(ctx, condition, thenBranch, elseBranch) };  if sky_asMap(__subject)["SkyName"] == "LetExpr" { bindings := sky_asMap(__subject)["V0"]; _ = bindings; body := sky_asMap(__subject)["V1"]; _ = body; return Compiler_Lower_LowerLet(ctx, bindings, body) };  if sky_asMap(__subject)["SkyName"] == "CaseExpr" { subject := sky_asMap(__subject)["V0"]; _ = subject; branches := sky_asMap(__subject)["V1"]; _ = branches; return Compiler_Lower_LowerCase(ctx, subject, branches) };  if sky_asMap(__subject)["SkyName"] == "BinaryExpr" { op := sky_asMap(__subject)["V0"]; _ = op; left := sky_asMap(__subject)["V1"]; _ = left; right := sky_asMap(__subject)["V2"]; _ = right; return Compiler_Lower_LowerBinary(ctx, op, left, right) };  if sky_asMap(__subject)["SkyName"] == "NegateExpr" { inner := sky_asMap(__subject)["V0"]; _ = inner; return GoUnaryExpr("-", Compiler_Lower_LowerExpr(ctx, inner)) };  if sky_asMap(__subject)["SkyName"] == "ParenExpr" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Compiler_Lower_LowerExpr(ctx, inner) };  return nil }() }()
}

func Compiler_Lower_LowerIdentifier(ctx any, name any) any {
	return func() any { return func() any { __subject := sky_call(sky_dictGet(name), sky_asMap(ctx)["importedConstructors"]); if sky_asSkyMaybe(__subject).SkyName == "Just" { info := sky_asSkyMaybe(__subject).JustValue; _ = info; return func() any { if sky_asBool(sky_equal(sky_asMap(info)["arity"], 0)) { return Compiler_Lower_LowerConstructorValue(name, info) }; return GoIdent(Compiler_Lower_SanitizeGoIdent(name)) }() };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return func() any { if sky_asBool(sky_equal(name, "Ok")) { return GoIdent("SkyOk") }; return func() any { if sky_asBool(sky_equal(name, "Err")) { return GoIdent("SkyErr") }; return func() any { if sky_asBool(sky_equal(name, "Just")) { return GoIdent("SkyJust") }; return func() any { if sky_asBool(sky_equal(name, "Nothing")) { return GoCallExpr(GoIdent("SkyNothing"), []any{}) }; return func() any { if sky_asBool(sky_equal(name, "True")) { return GoIdent("true") }; return func() any { if sky_asBool(sky_equal(name, "False")) { return GoIdent("false") }; return func() any { if sky_asBool(sky_equal(name, "not")) { return GoIdent("sky_not") }; return func() any { if sky_asBool(sky_equal(name, "fst")) { return GoIdent("sky_fst") }; return func() any { if sky_asBool(sky_equal(name, "snd")) { return GoIdent("sky_snd") }; return func() any { if sky_asBool(sky_equal(name, "errorToString")) { return GoIdent("sky_errorToString") }; return func() any { if sky_asBool(sky_equal(name, "println")) { return GoIdent("sky_println") }; return func() any { if sky_asBool(sky_equal(name, "identity")) { return GoIdent("sky_identity") }; return func() any { if sky_asBool(sky_equal(name, "always")) { return GoIdent("sky_always") }; return func() any { if sky_asBool(sky_equal(name, "js")) { return GoIdent("sky_js") }; return func() any { return func() any { __subject := sky_call(sky_dictGet(name), sky_asMap(ctx)["exposedStdlib"]); if sky_asSkyMaybe(__subject).SkyName == "Just" { modPath := sky_asSkyMaybe(__subject).JustValue; _ = modPath; return Compiler_Lower_LowerStdlibExposed(modPath, name) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return func() any { if sky_asBool(sky_asBool(sky_not(sky_stringIsEmpty(sky_asMap(ctx)["modulePrefix"]))) && sky_asBool(Compiler_Lower_ListContains(name, sky_asMap(ctx)["localFunctions"]))) { return func() any { goName := sky_concat(sky_asMap(ctx)["modulePrefix"], sky_concat("_", Compiler_Lower_CapitalizeFirst(Compiler_Lower_SanitizeGoIdent(name)))); _ = goName; return func() any { if sky_asBool(Compiler_Lower_IsZeroArityFn(name, ctx)) { return GoCallExpr(GoIdent(goName), []any{}) }; return GoIdent(goName) }() }() }; return func() any { if sky_asBool(Compiler_Lower_IsZeroArityFn(name, ctx)) { return GoCallExpr(GoIdent(Compiler_Lower_SanitizeGoIdent(name)), []any{}) }; return GoIdent(Compiler_Lower_SanitizeGoIdent(name)) }() }() };  return nil }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() };  return nil }() }()
}

func Compiler_Lower_LowerStdlibExposed(modPath any, name any) any {
	return func() any { if sky_asBool(sky_equal(modPath, "Std.Html")) { return func() any { if sky_asBool(sky_equal(name, "text")) { return GoIdent("sky_htmlText") }; return func() any { if sky_asBool(sky_equal(name, "raw")) { return GoIdent("sky_htmlRaw") }; return func() any { if sky_asBool(sky_equal(name, "styleNode")) { return GoIdent("sky_htmlStyleNode") }; return func() any { if sky_asBool(sky_asBool(sky_equal(name, "input")) || sky_asBool(sky_asBool(sky_equal(name, "br")) || sky_asBool(sky_asBool(sky_equal(name, "hr")) || sky_asBool(sky_asBool(sky_equal(name, "img")) || sky_asBool(sky_equal(name, "meta")))))) { return GoCallExpr(GoIdent("sky_htmlVoid"), []any{GoStringLit(name)}) }; return func() any { if sky_asBool(sky_asBool(sky_equal(name, "render")) || sky_asBool(sky_equal(name, "toString"))) { return GoIdent("sky_htmlRender") }; return func() any { if sky_asBool(sky_equal(name, "escapeHtml")) { return GoIdent("sky_htmlEscapeHtml") }; return func() any { if sky_asBool(sky_equal(name, "escapeAttr")) { return GoIdent("sky_htmlEscapeAttr") }; return func() any { if sky_asBool(sky_equal(name, "attrToString")) { return GoIdent("sky_htmlAttrToString") }; return func() any { if sky_asBool(sky_equal(name, "node")) { return GoIdent("sky_htmlEl") }; return func() any { if sky_asBool(sky_equal(name, "voidNode")) { return GoIdent("sky_htmlVoid") }; return func() any { tagName := func() any { if sky_asBool(sky_equal(name, "mainNode")) { return "main" }; return func() any { if sky_asBool(sky_equal(name, "headerNode")) { return "header" }; return func() any { if sky_asBool(sky_equal(name, "footerNode")) { return "footer" }; return func() any { if sky_asBool(sky_equal(name, "codeNode")) { return "code" }; return func() any { if sky_asBool(sky_equal(name, "linkNode")) { return "link" }; return func() any { if sky_asBool(sky_equal(name, "titleNode")) { return "title" }; return func() any { if sky_asBool(sky_equal(name, "htmlNode")) { return "html" }; return func() any { if sky_asBool(sky_equal(name, "headNode")) { return "head" }; return name }() }() }() }() }() }() }() }(); _ = tagName; return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit(tagName)}) }() }() }() }() }() }() }() }() }() }() }() }; return func() any { if sky_asBool(sky_equal(modPath, "Std.Html.Attributes")) { return func() any { if sky_asBool(sky_equal(name, "attribute")) { return GoIdent("sky_attrCustom") }; return func() any { if sky_asBool(sky_equal(name, "boolAttribute")) { return GoIdent("sky_attrBool") }; return func() any { if sky_asBool(sky_equal(name, "dataAttribute")) { return GoIdent("sky_attrData") }; return func() any { if sky_asBool(sky_equal(name, "type_")) { return GoCallExpr(GoIdent("sky_attrSimple"), []any{GoStringLit("type")}) }; return GoCallExpr(GoIdent("sky_attrSimple"), []any{GoStringLit(name)}) }() }() }() }() }; return func() any { if sky_asBool(sky_equal(modPath, "Std.Live.Events")) { return func() any { if sky_asBool(sky_call(sky_stringStartsWith("on"), name)) { return func() any { evtName := sky_stringToLower(sky_call2(sky_stringSlice(2), sky_stringLength(name), name)); _ = evtName; return GoCallExpr(GoIdent("sky_evtHandler"), []any{GoStringLit(evtName)}) }() }; return GoIdent(sky_concat("sky_evt_", name)) }() }; return func() any { if sky_asBool(sky_equal(modPath, "Std.Css")) { return func() any { if sky_asBool(sky_equal(name, "stylesheet")) { return GoIdent("sky_cssStylesheet") }; return func() any { if sky_asBool(sky_equal(name, "rule")) { return GoIdent("sky_cssRule") }; return func() any { if sky_asBool(sky_equal(name, "property")) { return GoIdent("sky_cssProp") }; return func() any { if sky_asBool(sky_equal(name, "styles")) { return GoIdent("sky_cssStyles") }; return func() any { if sky_asBool(sky_equal(name, "px")) { return GoIdent("sky_cssPx") }; return func() any { if sky_asBool(sky_equal(name, "rem")) { return GoIdent("sky_cssRem") }; return func() any { if sky_asBool(sky_equal(name, "em")) { return GoIdent("sky_cssEm") }; return func() any { if sky_asBool(sky_equal(name, "pct")) { return GoIdent("sky_cssPct") }; return func() any { if sky_asBool(sky_equal(name, "hex")) { return GoIdent("sky_cssHex") }; return func() any { if sky_asBool(sky_equal(name, "rgb")) { return GoIdent("sky_cssRgb") }; return func() any { if sky_asBool(sky_equal(name, "rgba")) { return GoIdent("sky_cssRgba") }; return func() any { if sky_asBool(sky_equal(name, "margin2")) { return GoIdent("sky_cssMargin2") }; return func() any { if sky_asBool(sky_equal(name, "padding2")) { return GoIdent("sky_cssPadding2") }; return func() any { if sky_asBool(sky_equal(name, "media")) { return GoIdent("sky_cssMedia") }; return func() any { if sky_asBool(sky_equal(name, "keyframes")) { return GoIdent("sky_cssKeyframes") }; return func() any { if sky_asBool(sky_equal(name, "frame")) { return GoIdent("sky_cssFrame") }; return GoCallExpr(GoIdent("sky_cssPropFn"), []any{GoStringLit(Compiler_Lower_CssNameToProperty(name))}) }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }; return func() any { if sky_asBool(sky_equal(modPath, "Std.Live")) { return func() any { if sky_asBool(sky_equal(name, "app")) { return GoIdent("sky_liveApp") }; return func() any { if sky_asBool(sky_equal(name, "route")) { return GoIdent("sky_liveRoute") }; return GoIdent(sky_concat("sky_live_", name)) }() }() }; return func() any { if sky_asBool(sky_equal(modPath, "Std.Cmd")) { return func() any { if sky_asBool(sky_equal(name, "none")) { return GoCallExpr(GoIdent("sky_cmdNone"), []any{}) }; return func() any { if sky_asBool(sky_equal(name, "batch")) { return GoIdent("sky_cmdBatch") }; return GoIdent(sky_concat("sky_cmd_", name)) }() }() }; return func() any { if sky_asBool(sky_equal(modPath, "Std.Sub")) { return func() any { if sky_asBool(sky_equal(name, "none")) { return GoCallExpr(GoIdent("sky_subNone"), []any{}) }; return func() any { if sky_asBool(sky_equal(name, "batch")) { return GoIdent("sky_subBatch") }; return GoIdent(sky_concat("sky_sub_", name)) }() }() }; return func() any { if sky_asBool(sky_equal(modPath, "Std.Time")) { return func() any { if sky_asBool(sky_equal(name, "every")) { return GoIdent("sky_timeEvery") }; return GoIdent(sky_concat("sky_time_", name)) }() }; return GoIdent(Compiler_Lower_SanitizeGoIdent(name)) }() }() }() }() }() }() }() }()
}

func Compiler_Lower_CssNameToProperty(name any) any {
	return Compiler_Lower_CssNameToPropertyLoop(name, 0, "")
}

func Compiler_Lower_CssNameToPropertyLoop(name any, idx any, acc any) any {
	return func() any { if sky_asBool(sky_asInt(idx) >= sky_asInt(sky_stringLength(name))) { return acc }; return func() any { ch := sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1), name); _ = ch; isUpper := sky_asBool(sky_equal(ch, "A")) || sky_asBool(sky_asBool(sky_equal(ch, "B")) || sky_asBool(sky_asBool(sky_equal(ch, "C")) || sky_asBool(sky_asBool(sky_equal(ch, "D")) || sky_asBool(sky_asBool(sky_equal(ch, "E")) || sky_asBool(sky_asBool(sky_equal(ch, "F")) || sky_asBool(sky_asBool(sky_equal(ch, "G")) || sky_asBool(sky_asBool(sky_equal(ch, "H")) || sky_asBool(sky_asBool(sky_equal(ch, "I")) || sky_asBool(sky_asBool(sky_equal(ch, "J")) || sky_asBool(sky_asBool(sky_equal(ch, "K")) || sky_asBool(sky_asBool(sky_equal(ch, "L")) || sky_asBool(sky_asBool(sky_equal(ch, "M")) || sky_asBool(sky_asBool(sky_equal(ch, "N")) || sky_asBool(sky_asBool(sky_equal(ch, "O")) || sky_asBool(sky_asBool(sky_equal(ch, "P")) || sky_asBool(sky_asBool(sky_equal(ch, "Q")) || sky_asBool(sky_asBool(sky_equal(ch, "R")) || sky_asBool(sky_asBool(sky_equal(ch, "S")) || sky_asBool(sky_asBool(sky_equal(ch, "T")) || sky_asBool(sky_asBool(sky_equal(ch, "U")) || sky_asBool(sky_asBool(sky_equal(ch, "V")) || sky_asBool(sky_asBool(sky_equal(ch, "W")) || sky_asBool(sky_asBool(sky_equal(ch, "X")) || sky_asBool(sky_asBool(sky_equal(ch, "Y")) || sky_asBool(sky_equal(ch, "Z")))))))))))))))))))))))))); _ = isUpper; return func() any { if sky_asBool(sky_asBool(isUpper) && sky_asBool(sky_asInt(idx) > sky_asInt(0))) { return Compiler_Lower_CssNameToPropertyLoop(name, sky_asInt(idx) + sky_asInt(1), sky_concat(acc, sky_concat("-", sky_stringToLower(ch)))) }; return Compiler_Lower_CssNameToPropertyLoop(name, sky_asInt(idx) + sky_asInt(1), sky_concat(acc, ch)) }() }() }()
}

func Compiler_Lower_LowerConstructorValue(name any, info any) any {
	return GoMapLit([]any{SkyTuple2{V0: GoStringLit("Tag"), V1: GoBasicLit(sky_stringFromInt(sky_asMap(info)["tagIndex"]))}, SkyTuple2{V0: GoStringLit("SkyName"), V1: GoStringLit(name)}})
}

func Compiler_Lower_LowerQualified(ctx any, parts any) any {
	return func() any { qualName := sky_call(sky_stringJoin("."), parts); _ = qualName; return func() any { if sky_asBool(sky_equal(qualName, "String.fromInt")) { return GoIdent("sky_stringFromInt") }; return func() any { if sky_asBool(sky_equal(qualName, "String.fromFloat")) { return GoIdent("sky_stringFromFloat") }; return func() any { if sky_asBool(sky_equal(qualName, "String.toUpper")) { return GoIdent("sky_stringToUpper") }; return func() any { if sky_asBool(sky_equal(qualName, "String.toLower")) { return GoIdent("sky_stringToLower") }; return func() any { if sky_asBool(sky_equal(qualName, "String.length")) { return GoIdent("sky_stringLength") }; return func() any { if sky_asBool(sky_equal(qualName, "String.join")) { return GoIdent("sky_stringJoin") }; return func() any { if sky_asBool(sky_equal(qualName, "String.contains")) { return GoIdent("sky_stringContains") }; return func() any { if sky_asBool(sky_equal(qualName, "String.trim")) { return GoIdent("sky_stringTrim") }; return func() any { if sky_asBool(sky_equal(qualName, "String.isEmpty")) { return GoIdent("sky_stringIsEmpty") }; return func() any { if sky_asBool(sky_equal(qualName, "String.startsWith")) { return GoIdent("sky_stringStartsWith") }; return func() any { if sky_asBool(sky_equal(qualName, "String.endsWith")) { return GoIdent("sky_stringEndsWith") }; return func() any { if sky_asBool(sky_equal(qualName, "String.split")) { return GoIdent("sky_stringSplit") }; return func() any { if sky_asBool(sky_equal(qualName, "String.replace")) { return GoIdent("sky_stringReplace") }; return func() any { if sky_asBool(sky_equal(qualName, "String.toInt")) { return GoIdent("sky_stringToInt") }; return func() any { if sky_asBool(sky_equal(qualName, "String.toFloat")) { return GoIdent("sky_stringToFloat") }; return func() any { if sky_asBool(sky_equal(qualName, "String.append")) { return GoIdent("sky_stringAppend") }; return func() any { if sky_asBool(sky_equal(qualName, "String.slice")) { return GoIdent("sky_stringSlice") }; return func() any { if sky_asBool(sky_equal(qualName, "List.map")) { return GoIdent("sky_listMap") }; return func() any { if sky_asBool(sky_equal(qualName, "List.filter")) { return GoIdent("sky_listFilter") }; return func() any { if sky_asBool(sky_equal(qualName, "List.foldl")) { return GoIdent("sky_listFoldl") }; return func() any { if sky_asBool(sky_equal(qualName, "List.foldr")) { return GoIdent("sky_listFoldr") }; return func() any { if sky_asBool(sky_equal(qualName, "List.length")) { return GoIdent("sky_listLength") }; return func() any { if sky_asBool(sky_equal(qualName, "List.head")) { return GoIdent("sky_listHead") }; return func() any { if sky_asBool(sky_equal(qualName, "List.reverse")) { return GoIdent("sky_listReverse") }; return func() any { if sky_asBool(sky_equal(qualName, "List.isEmpty")) { return GoIdent("sky_listIsEmpty") }; return func() any { if sky_asBool(sky_equal(qualName, "List.append")) { return GoIdent("sky_listAppend") }; return func() any { if sky_asBool(sky_equal(qualName, "List.concatMap")) { return GoIdent("sky_listConcatMap") }; return func() any { if sky_asBool(sky_equal(qualName, "List.filterMap")) { return GoIdent("sky_listFilterMap") }; return func() any { if sky_asBool(sky_equal(qualName, "List.indexedMap")) { return GoIdent("sky_listIndexedMap") }; return func() any { if sky_asBool(sky_equal(qualName, "List.concat")) { return GoIdent("sky_listConcat") }; return func() any { if sky_asBool(sky_equal(qualName, "List.drop")) { return GoIdent("sky_listDrop") }; return func() any { if sky_asBool(sky_equal(qualName, "List.member")) { return GoIdent("sky_listMember") }; return func() any { if sky_asBool(sky_equal(qualName, "Log.println")) { return GoIdent("sky_println") }; return func() any { if sky_asBool(sky_equal(qualName, "Args.skyVersion")) { return GoIdent("skyVersion") }; return func() any { if sky_asBool(sky_equal(qualName, "File.readFile")) { return GoIdent("sky_fileRead") }; return func() any { if sky_asBool(sky_equal(qualName, "File.writeFile")) { return GoIdent("sky_fileWrite") }; return func() any { if sky_asBool(sky_equal(qualName, "File.mkdirAll")) { return GoIdent("sky_fileMkdirAll") }; return func() any { if sky_asBool(sky_equal(qualName, "Process.exit")) { return GoIdent("sky_processExit") }; return func() any { if sky_asBool(sky_equal(qualName, "Process.run")) { return GoIdent("sky_processRun") }; return func() any { if sky_asBool(sky_equal(qualName, "Args.getArgs")) { return GoIdent("sky_processGetArgs") }; return func() any { if sky_asBool(sky_equal(qualName, "Args.getArg")) { return GoIdent("sky_processGetArg") }; return func() any { if sky_asBool(sky_equal(qualName, "Ref.new")) { return GoIdent("sky_refNew") }; return func() any { if sky_asBool(sky_equal(qualName, "Ref.get")) { return GoIdent("sky_refGet") }; return func() any { if sky_asBool(sky_equal(qualName, "Ref.set")) { return GoIdent("sky_refSet") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.empty")) { return GoCallExpr(GoIdent("sky_dictEmpty"), []any{}) }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.insert")) { return GoIdent("sky_dictInsert") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.get")) { return GoIdent("sky_dictGet") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.keys")) { return GoIdent("sky_dictKeys") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.values")) { return GoIdent("sky_dictValues") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.toList")) { return GoIdent("sky_dictToList") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.fromList")) { return GoIdent("sky_dictFromList") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.map")) { return GoIdent("sky_dictMap") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.foldl")) { return GoIdent("sky_dictFoldl") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.union")) { return GoIdent("sky_dictUnion") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.remove")) { return GoIdent("sky_dictRemove") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.member")) { return GoIdent("sky_dictMember") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.empty")) { return GoCallExpr(GoIdent("sky_setEmpty"), []any{}) }; return func() any { if sky_asBool(sky_equal(qualName, "Set.singleton")) { return GoIdent("sky_setSingleton") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.insert")) { return GoIdent("sky_setInsert") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.member")) { return GoIdent("sky_setMember") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.union")) { return GoIdent("sky_setUnion") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.diff")) { return GoIdent("sky_setDiff") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.toList")) { return GoIdent("sky_setToList") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.fromList")) { return GoIdent("sky_setFromList") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.isEmpty")) { return GoIdent("sky_setIsEmpty") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.remove")) { return GoIdent("sky_setRemove") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.isUpper")) { return GoIdent("sky_charIsUpper") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.isLower")) { return GoIdent("sky_charIsLower") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.isDigit")) { return GoIdent("sky_charIsDigit") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.isAlpha")) { return GoIdent("sky_charIsAlpha") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.isAlphaNum")) { return GoIdent("sky_charIsAlphaNum") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.toUpper")) { return GoIdent("sky_charToUpper") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.toLower")) { return GoIdent("sky_charToLower") }; return func() any { if sky_asBool(sky_equal(qualName, "String.fromChar")) { return GoIdent("sky_stringFromChar") }; return func() any { if sky_asBool(sky_equal(qualName, "String.toList")) { return GoIdent("sky_stringToList") }; return func() any { if sky_asBool(sky_equal(qualName, "String.toBytes")) { return GoIdent("sky_stringToBytes") }; return func() any { if sky_asBool(sky_equal(qualName, "String.fromBytes")) { return GoIdent("sky_stringFromBytes") }; return func() any { if sky_asBool(sky_equal(qualName, "Io.readLine")) { return GoIdent("sky_readLine") }; return func() any { if sky_asBool(sky_equal(qualName, "Io.readBytes")) { return GoIdent("sky_readBytes") }; return func() any { if sky_asBool(sky_equal(qualName, "Io.writeStdout")) { return GoIdent("sky_writeStdout") }; return func() any { if sky_asBool(sky_equal(qualName, "Io.writeStderr")) { return GoIdent("sky_writeStderr") }; return func() any { if sky_asBool(sky_equal(qualName, "Server.listen")) { return GoIdent("sky_serverListen") }; return func() any { if sky_asBool(sky_equal(qualName, "Server.get")) { return GoIdent("sky_serverGet") }; return func() any { if sky_asBool(sky_equal(qualName, "Server.post")) { return GoIdent("sky_serverPost") }; return func() any { if sky_asBool(sky_equal(qualName, "Server.put")) { return GoIdent("sky_serverPut") }; return func() any { if sky_asBool(sky_equal(qualName, "Server.delete")) { return GoIdent("sky_serverDelete") }; return func() any { if sky_asBool(sky_equal(qualName, "Server.any")) { return GoIdent("sky_serverAny") }; return func() any { if sky_asBool(sky_equal(qualName, "Server.group")) { return GoIdent("sky_serverGroup") }; return func() any { if sky_asBool(sky_equal(qualName, "Server.static")) { return GoIdent("sky_serverStatic") }; return func() any { if sky_asBool(sky_equal(qualName, "Server.text")) { return GoIdent("sky_serverText") }; return func() any { if sky_asBool(sky_equal(qualName, "Server.html")) { return GoIdent("sky_serverHtml") }; return func() any { if sky_asBool(sky_equal(qualName, "Server.json")) { return GoIdent("sky_serverJson") }; return func() any { if sky_asBool(sky_equal(qualName, "Server.redirect")) { return GoIdent("sky_serverRedirect") }; return func() any { if sky_asBool(sky_equal(qualName, "Server.withStatus")) { return GoIdent("sky_serverWithStatus") }; return func() any { if sky_asBool(sky_equal(qualName, "Server.withHeader")) { return GoIdent("sky_serverWithHeader") }; return func() any { if sky_asBool(sky_equal(qualName, "Server.withCookie")) { return GoIdent("sky_serverWithCookie") }; return func() any { if sky_asBool(sky_equal(qualName, "Server.param")) { return GoIdent("sky_serverParam") }; return func() any { if sky_asBool(sky_equal(qualName, "Server.queryParam")) { return GoIdent("sky_serverQueryParam") }; return func() any { if sky_asBool(sky_equal(qualName, "Server.header")) { return GoIdent("sky_serverHeader") }; return func() any { if sky_asBool(sky_equal(qualName, "Server.getCookie")) { return GoIdent("sky_serverGetCookie") }; return func() any { if sky_asBool(sky_equal(qualName, "Server.cookie")) { return GoIdent("sky_serverCookie") }; return func() any { if sky_asBool(sky_equal(qualName, "Task.succeed")) { return GoIdent("sky_taskSucceed") }; return func() any { if sky_asBool(sky_equal(qualName, "Task.fail")) { return GoIdent("sky_taskFail") }; return func() any { if sky_asBool(sky_equal(qualName, "Task.andThen")) { return GoIdent("sky_taskAndThen") }; return func() any { if sky_asBool(sky_equal(qualName, "Task.map")) { return GoIdent("sky_taskMap") }; return func() any { if sky_asBool(sky_equal(qualName, "Task.perform")) { return GoIdent("sky_taskPerform") }; return func() any { if sky_asBool(sky_equal(qualName, "Task.sequence")) { return GoIdent("sky_taskSequence") }; return func() any { if sky_asBool(sky_equal(qualName, "Result.map")) { return GoIdent("sky_resultMap") }; return func() any { if sky_asBool(sky_equal(qualName, "Result.withDefault")) { return GoIdent("sky_resultWithDefault") }; return func() any { if sky_asBool(sky_equal(qualName, "Result.andThen")) { return GoIdent("sky_resultAndThen") }; return func() any { if sky_asBool(sky_equal(qualName, "Result.mapError")) { return GoIdent("sky_resultMapError") }; return func() any { if sky_asBool(sky_equal(qualName, "Maybe.withDefault")) { return GoIdent("sky_maybeWithDefault") }; return func() any { if sky_asBool(sky_equal(qualName, "Maybe.map")) { return GoIdent("sky_maybeMap") }; return func() any { if sky_asBool(sky_equal(qualName, "Maybe.andThen")) { return GoIdent("sky_maybeAndThen") }; return func() any { if sky_asBool(sky_equal(qualName, "List.take")) { return GoIdent("sky_listTake") }; return func() any { if sky_asBool(sky_equal(qualName, "List.sort")) { return GoIdent("sky_listSort") }; return func() any { if sky_asBool(sky_equal(qualName, "List.zip")) { return GoIdent("sky_listZip") }; return func() any { if sky_asBool(sky_equal(qualName, "List.range")) { return GoIdent("sky_listRange") }; return func() any { if sky_asBool(sky_equal(qualName, "List.any")) { return GoIdent("sky_listAny") }; return func() any { if sky_asBool(sky_equal(qualName, "List.all")) { return GoIdent("sky_listAll") }; return func() any { if sky_asBool(sky_equal(qualName, "List.singleton")) { return GoIdent("sky_listSingleton") }; return func() any { if sky_asBool(sky_equal(qualName, "List.intersperse")) { return GoIdent("sky_listIntersperse") }; return func() any { if sky_asBool(sky_equal(qualName, "String.left")) { return GoIdent("sky_stringLeft") }; return func() any { if sky_asBool(sky_equal(qualName, "String.right")) { return GoIdent("sky_stringRight") }; return func() any { if sky_asBool(sky_equal(qualName, "String.padLeft")) { return GoIdent("sky_stringPadLeft") }; return func() any { if sky_asBool(sky_equal(qualName, "String.lines")) { return GoIdent("sky_stringLines") }; return func() any { if sky_asBool(sky_equal(qualName, "String.words")) { return GoIdent("sky_stringWords") }; return func() any { if sky_asBool(sky_equal(qualName, "String.repeat")) { return GoIdent("sky_stringRepeat") }; return func() any { if sky_asBool(sky_equal(qualName, "Math.sqrt")) { return GoIdent("sky_mathSqrt") }; return func() any { if sky_asBool(sky_equal(qualName, "Math.pow")) { return GoIdent("sky_mathPow") }; return func() any { if sky_asBool(sky_equal(qualName, "Math.abs")) { return GoIdent("sky_mathAbs") }; return func() any { if sky_asBool(sky_equal(qualName, "Math.floor")) { return GoIdent("sky_mathFloor") }; return func() any { if sky_asBool(sky_equal(qualName, "Math.ceil")) { return GoIdent("sky_mathCeil") }; return func() any { if sky_asBool(sky_equal(qualName, "Math.round")) { return GoIdent("sky_mathRound") }; return func() any { if sky_asBool(sky_equal(qualName, "Math.min")) { return GoIdent("sky_mathMin") }; return func() any { if sky_asBool(sky_equal(qualName, "Math.max")) { return GoIdent("sky_mathMax") }; return func() any { if sky_asBool(sky_equal(qualName, "Math.pi")) { return GoBasicLit("3.141592653589793") }; return func() any { if sky_asBool(sky_equal(qualName, "Math.modBy")) { return GoIdent("sky_modBy") }; return func() any { if sky_asBool(sky_equal(qualName, "Crypto.sha256")) { return GoIdent("sky_cryptoSha256") }; return func() any { if sky_asBool(sky_equal(qualName, "Crypto.md5")) { return GoIdent("sky_cryptoMd5") }; return func() any { if sky_asBool(sky_equal(qualName, "Encoding.hexEncode")) { return GoIdent("sky_encodingHexEncode") }; return func() any { if sky_asBool(sky_equal(qualName, "Encoding.base64Encode")) { return GoIdent("sky_encodingBase64Encode") }; return func() any { if sky_asBool(sky_equal(qualName, "Encoding.base64Decode")) { return GoIdent("sky_encodingBase64Decode") }; return func() any { if sky_asBool(sky_equal(qualName, "Time.now")) { return GoIdent("sky_timeNow") }; return func() any { if sky_asBool(sky_equal(qualName, "Time.posixToMillis")) { return GoIdent("sky_timePosixToMillis") }; return func() any { if sky_asBool(sky_equal(qualName, "Http.getString")) { return GoIdent("sky_httpGetString") }; return func() any { if sky_asBool(sky_equal(qualName, "Encode.string")) { return GoIdent("sky_jsonEncString") }; return func() any { if sky_asBool(sky_equal(qualName, "Encode.int")) { return GoIdent("sky_jsonEncInt") }; return func() any { if sky_asBool(sky_equal(qualName, "Encode.float")) { return GoIdent("sky_jsonEncFloat") }; return func() any { if sky_asBool(sky_equal(qualName, "Encode.bool")) { return GoIdent("sky_jsonEncBool") }; return func() any { if sky_asBool(sky_equal(qualName, "Encode.null")) { return GoCallExpr(GoIdent("sky_jsonEncNull"), []any{}) }; return func() any { if sky_asBool(sky_equal(qualName, "Encode.list")) { return GoIdent("sky_jsonEncList") }; return func() any { if sky_asBool(sky_equal(qualName, "Encode.object")) { return GoIdent("sky_jsonEncObject") }; return func() any { if sky_asBool(sky_equal(qualName, "Encode.encode")) { return GoIdent("sky_jsonEncode") }; return func() any { if sky_asBool(sky_equal(qualName, "Decode.decodeString")) { return GoIdent("sky_jsonDecString") }; return func() any { if sky_asBool(sky_equal(qualName, "Decode.string")) { return GoIdent("sky_jsonDecoder_string") }; return func() any { if sky_asBool(sky_equal(qualName, "Decode.int")) { return GoIdent("sky_jsonDecoder_int") }; return func() any { if sky_asBool(sky_equal(qualName, "Decode.float")) { return GoIdent("sky_jsonDecoder_float") }; return func() any { if sky_asBool(sky_equal(qualName, "Decode.bool")) { return GoIdent("sky_jsonDecoder_bool") }; return func() any { if sky_asBool(sky_equal(qualName, "Decode.field")) { return GoIdent("sky_jsonDecField") }; return func() any { if sky_asBool(sky_equal(qualName, "Decode.list")) { return GoIdent("sky_jsonDecList") }; return func() any { if sky_asBool(sky_equal(qualName, "Decode.map")) { return GoIdent("sky_jsonDecMap") }; return func() any { if sky_asBool(sky_equal(qualName, "Decode.map2")) { return GoIdent("sky_jsonDecMap2") }; return func() any { if sky_asBool(sky_equal(qualName, "Decode.map3")) { return GoIdent("sky_jsonDecMap3") }; return func() any { if sky_asBool(sky_equal(qualName, "Decode.map4")) { return GoIdent("sky_jsonDecMap4") }; return func() any { if sky_asBool(sky_equal(qualName, "Decode.succeed")) { return GoIdent("sky_jsonDecSucceed") }; return func() any { if sky_asBool(sky_equal(qualName, "Decode.fail")) { return GoIdent("sky_jsonDecFail") }; return func() any { if sky_asBool(sky_equal(qualName, "Decode.andThen")) { return GoIdent("sky_jsonDecAndThen") }; return func() any { if sky_asBool(sky_equal(qualName, "Decode.oneOf")) { return GoIdent("sky_jsonDecOneOf") }; return func() any { if sky_asBool(sky_equal(qualName, "Decode.nullable")) { return GoIdent("sky_jsonDecNullable") }; return func() any { if sky_asBool(sky_equal(qualName, "Decode.at")) { return GoIdent("sky_jsonDecAt") }; return func() any { if sky_asBool(sky_equal(qualName, "Pipeline.required")) { return GoIdent("sky_jsonPipeRequired") }; return func() any { if sky_asBool(sky_equal(qualName, "Pipeline.optional")) { return GoIdent("sky_jsonPipeOptional") }; return func() any { if sky_asBool(sky_equal(qualName, "Pipeline.decode")) { return GoIdent("sky_jsonPipeDecode") }; return func() any { if sky_asBool(sky_equal(qualName, "Cmd.none")) { return GoCallExpr(GoIdent("sky_cmdNone"), []any{}) }; return func() any { if sky_asBool(sky_equal(qualName, "Cmd.batch")) { return GoIdent("sky_cmdBatch") }; return func() any { if sky_asBool(sky_equal(qualName, "Sub.none")) { return GoCallExpr(GoIdent("sky_subNone"), []any{}) }; return func() any { if sky_asBool(sky_equal(qualName, "Sub.batch")) { return GoIdent("sky_subBatch") }; return func() any { if sky_asBool(sky_equal(qualName, "Time.every")) { return GoIdent("sky_timeEvery") }; return func() any { if sky_asBool(sky_equal(qualName, "Html.div")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("div")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.span")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("span")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.p")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("p")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.h1")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("h1")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.h2")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("h2")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.h3")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("h3")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.h4")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("h4")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.h5")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("h5")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.h6")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("h6")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.button")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("button")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.a")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("a")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.img")) { return GoCallExpr(GoIdent("sky_htmlVoid"), []any{GoStringLit("img")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.input")) { return GoCallExpr(GoIdent("sky_htmlVoid"), []any{GoStringLit("input")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.br")) { return GoCallExpr(GoIdent("sky_htmlVoid"), []any{GoStringLit("br")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.hr")) { return GoCallExpr(GoIdent("sky_htmlVoid"), []any{GoStringLit("hr")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.form")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("form")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.label")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("label")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.textarea")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("textarea")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.select")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("select")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.option")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("option")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.ul")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("ul")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.ol")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("ol")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.li")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("li")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.table")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("table")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.thead")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("thead")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.tbody")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("tbody")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.tr")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("tr")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.th")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("th")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.td")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("td")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.nav")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("nav")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.header")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("header")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.footer")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("footer")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.section")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("section")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.article")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("article")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.aside")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("aside")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.main_")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("main")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.pre")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("pre")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.code")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("code")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.em")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("em")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.strong")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("strong")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.small")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("small")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.blockquote")) { return GoCallExpr(GoIdent("sky_htmlEl"), []any{GoStringLit("blockquote")}) }; return func() any { if sky_asBool(sky_equal(qualName, "Html.text")) { return GoIdent("sky_htmlText") }; return func() any { if sky_asBool(sky_equal(qualName, "Html.raw")) { return GoIdent("sky_htmlRaw") }; return func() any { if sky_asBool(sky_equal(qualName, "Html.styleNode")) { return GoIdent("sky_htmlStyleNode") }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("Attr."), qualName)) { return func() any { attrName := sky_call2(sky_stringSlice(5), sky_stringLength(qualName), qualName); _ = attrName; return Compiler_Lower_LowerStdlibExposed("Std.Html.Attributes", attrName) }() }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("Events."), qualName)) { return func() any { evtName := sky_call2(sky_stringSlice(7), sky_stringLength(qualName), qualName); _ = evtName; return Compiler_Lower_LowerStdlibExposed("Std.Live.Events", evtName) }() }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("Css."), qualName)) { return func() any { cssName := sky_call2(sky_stringSlice(4), sky_stringLength(qualName), qualName); _ = cssName; return Compiler_Lower_LowerStdlibExposed("Std.Css", cssName) }() }; return func() any { if sky_asBool(sky_equal(qualName, "Live.app")) { return GoIdent("sky_liveApp") }; return func() any { if sky_asBool(sky_equal(qualName, "Live.route")) { return GoIdent("sky_liveRoute") }; return func() any { if sky_asBool(sky_equal(sky_listLength(parts), 2)) { return func() any { modPart := func() any { return func() any { __subject := sky_listHead(parts); if sky_asSkyMaybe(__subject).SkyName == "Just" { p := sky_asSkyMaybe(__subject).JustValue; _ = p; return p };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "" };  return nil }() }(); _ = modPart; funcPart := func() any { return func() any { __subject := sky_listHead(sky_call(sky_listDrop(1), parts)); if sky_asSkyMaybe(__subject).SkyName == "Just" { p := sky_asSkyMaybe(__subject).JustValue; _ = p; return p };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "" };  return nil }() }(); _ = funcPart; return func() any { return func() any { __subject := sky_call(sky_dictGet(modPart), sky_asMap(ctx)["importAliases"]); if sky_asSkyMaybe(__subject).SkyName == "Just" { qualModName := sky_asSkyMaybe(__subject).JustValue; _ = qualModName; return func() any { prefix := sky_call2(sky_stringReplace("."), "_", qualModName); _ = prefix; safeFuncPart := Compiler_Lower_SanitizeGoIdent(funcPart); _ = safeFuncPart; goName := sky_concat(prefix, sky_concat("_", Compiler_Lower_CapitalizeFirst(safeFuncPart))); _ = goName; return GoCallExpr(GoIdent(goName), []any{}) }() };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return GoSelectorExpr(GoIdent(sky_stringToLower(modPart)), funcPart) };  return nil }() }() }() }; return GoIdent(Compiler_Lower_SanitizeGoIdent(qualName)) }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }()
}

func Compiler_Lower_LowerCall(ctx any, callee any, args any) any {
	return func() any { flatResult := Compiler_Lower_FlattenCall(ctx, callee, args); _ = flatResult; flatCallee := sky_fst(flatResult); _ = flatCallee; flatArgs := sky_snd(flatResult); _ = flatArgs; argCount := sky_listLength(flatArgs); _ = argCount; partialResult := Compiler_Lower_CheckPartialApplication(ctx, flatCallee, argCount); _ = partialResult; return func() any { return func() any { __subject := partialResult; if sky_asSkyMaybe(__subject).SkyName == "Just" { closure := sky_asSkyMaybe(__subject).JustValue; _ = closure; return func() any { goArgs := sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Lower_LowerExpr(ctx, __pa0) }), flatArgs); _ = goArgs; return Compiler_Lower_GeneratePartialClosure(closure, goArgs, argCount) }() };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return func() any { goCallee := Compiler_Lower_LowerExpr(ctx, flatCallee); _ = goCallee; goArgs := sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Lower_LowerArgExpr(ctx, __pa0) }), flatArgs); _ = goArgs; return func() any { return func() any { __subject := goCallee; if sky_asMap(__subject)["SkyName"] == "GoCallExpr" { innerFn := sky_asMap(__subject)["V0"]; _ = innerFn; innerArgs := sky_asMap(__subject)["V1"]; _ = innerArgs; return func() any { if sky_asBool(sky_listIsEmpty(innerArgs)) { return GoCallExpr(innerFn, goArgs) }; return func() any { if sky_asBool(sky_equal(sky_listLength(goArgs), 1)) { return GoCallExpr(GoIdent("sky_call"), sky_call(sky_listAppend([]any{goCallee}), goArgs)) }; return func() any { if sky_asBool(sky_equal(sky_listLength(goArgs), 2)) { return GoCallExpr(GoIdent("sky_call2"), sky_call(sky_listAppend([]any{goCallee}), goArgs)) }; return func() any { if sky_asBool(sky_equal(sky_listLength(goArgs), 3)) { return GoCallExpr(GoIdent("sky_call3"), sky_call(sky_listAppend([]any{goCallee}), goArgs)) }; return GoCallExpr(GoIdent("sky_call"), sky_call(sky_listAppend([]any{goCallee}), goArgs)) }() }() }() }() };  if sky_asMap(__subject)["SkyName"] == "GoRawExpr" { code := sky_asMap(__subject)["V0"]; _ = code; return func() any { if sky_asBool(sky_call(sky_stringEndsWith("()"), code)) { return func() any { return func() any { __subject := goArgs; if len(sky_asList(__subject)) > 0 { singleArg := sky_asList(__subject)[0]; _ = singleArg; return GoCallExpr(GoIdent("sky_call"), []any{goCallee, singleArg}) };  if true { return GoCallExpr(goCallee, goArgs) };  return nil }() }() }; return GoCallExpr(goCallee, goArgs) }() };  if true { return func() any { if sky_asBool(Compiler_Lower_IsDynamicCallee(ctx, flatCallee)) { return func() any { if sky_asBool(sky_equal(sky_listLength(goArgs), 1)) { return GoCallExpr(GoIdent("sky_call"), []any{goCallee, Compiler_Lower_ListGet(0, goArgs)}) }; return func() any { if sky_asBool(sky_equal(sky_listLength(goArgs), 2)) { return GoCallExpr(GoIdent("sky_call2"), []any{goCallee, Compiler_Lower_ListGet(0, goArgs), Compiler_Lower_ListGet(1, goArgs)}) }; return func() any { if sky_asBool(sky_equal(sky_listLength(goArgs), 3)) { return GoCallExpr(GoIdent("sky_call3"), []any{goCallee, Compiler_Lower_ListGet(0, goArgs), Compiler_Lower_ListGet(1, goArgs), Compiler_Lower_ListGet(2, goArgs)}) }; return GoCallExpr(goCallee, goArgs) }() }() }() }; return GoCallExpr(goCallee, goArgs) }() };  return nil }() }() }() };  return nil }() }() }()
}

func Compiler_Lower_IsDynamicCallee(ctx any, expr any) any {
	return func() any { return func() any { __subject := expr; if sky_asMap(__subject)["SkyName"] == "IdentifierExpr" { name := sky_asMap(__subject)["V0"]; _ = name; return sky_call(sky_setMember(name), sky_asMap(ctx)["paramNames"]) };  if true { return false };  return nil }() }()
}

func Compiler_Lower_ExtractPatternName(pat any) any {
	return func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PVariable" { name := sky_asMap(__subject)["V0"]; _ = name; return SkyJust(name) };  if sky_asMap(__subject)["SkyName"] == "PWildcard" { return SkyNothing() };  if true { return SkyNothing() };  return nil }() }()
}

func Compiler_Lower_IsWellKnownIdent(name any) any {
	return sky_asBool(sky_equal(name, "Ok")) || sky_asBool(sky_asBool(sky_equal(name, "Err")) || sky_asBool(sky_asBool(sky_equal(name, "Just")) || sky_asBool(sky_asBool(sky_equal(name, "Nothing")) || sky_asBool(sky_asBool(sky_equal(name, "True")) || sky_asBool(sky_asBool(sky_equal(name, "False")) || sky_asBool(sky_asBool(sky_equal(name, "not")) || sky_asBool(sky_asBool(sky_equal(name, "fst")) || sky_asBool(sky_asBool(sky_equal(name, "snd")) || sky_asBool(sky_asBool(sky_equal(name, "identity")) || sky_asBool(sky_asBool(sky_equal(name, "always")) || sky_asBool(sky_asBool(sky_equal(name, "errorToString")) || sky_asBool(sky_asBool(sky_equal(name, "println")) || sky_asBool(sky_asBool(sky_equal(name, "js")) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("sky_"), name)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Sky"), name)) || sky_asBool(sky_asBool(Compiler_Lower_IsUpperStart(name)) || sky_asBool(sky_asBool(sky_call(sky_stringContains("_"), name)) && sky_asBool(sky_not(sky_call(sky_stringStartsWith("_"), name))))))))))))))))))))
}

func Compiler_Lower_IsUpperStart(s any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(s)) { return false }; return func() any { c := sky_call2(sky_stringSlice(0), 1, s); _ = c; return sky_asBool(sky_equal(c, "A")) || sky_asBool(sky_asBool(sky_equal(c, "B")) || sky_asBool(sky_asBool(sky_equal(c, "C")) || sky_asBool(sky_asBool(sky_equal(c, "D")) || sky_asBool(sky_asBool(sky_equal(c, "E")) || sky_asBool(sky_asBool(sky_equal(c, "F")) || sky_asBool(sky_asBool(sky_equal(c, "G")) || sky_asBool(sky_asBool(sky_equal(c, "H")) || sky_asBool(sky_asBool(sky_equal(c, "I")) || sky_asBool(sky_asBool(sky_equal(c, "J")) || sky_asBool(sky_asBool(sky_equal(c, "K")) || sky_asBool(sky_asBool(sky_equal(c, "L")) || sky_asBool(sky_asBool(sky_equal(c, "M")) || sky_asBool(sky_asBool(sky_equal(c, "N")) || sky_asBool(sky_asBool(sky_equal(c, "O")) || sky_asBool(sky_asBool(sky_equal(c, "P")) || sky_asBool(sky_asBool(sky_equal(c, "Q")) || sky_asBool(sky_asBool(sky_equal(c, "R")) || sky_asBool(sky_asBool(sky_equal(c, "S")) || sky_asBool(sky_asBool(sky_equal(c, "T")) || sky_asBool(sky_asBool(sky_equal(c, "U")) || sky_asBool(sky_asBool(sky_equal(c, "V")) || sky_asBool(sky_asBool(sky_equal(c, "W")) || sky_asBool(sky_asBool(sky_equal(c, "X")) || sky_asBool(sky_asBool(sky_equal(c, "Y")) || sky_asBool(sky_equal(c, "Z")))))))))))))))))))))))))) }() }()
}

func Compiler_Lower_CheckPartialApplication(ctx any, callee any, argCount any) any {
	return func() any { return func() any { __subject := callee; if sky_asMap(__subject)["SkyName"] == "IdentifierExpr" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { return func() any { __subject := sky_call(sky_dictGet(name), sky_asMap(ctx)["localFunctionArity"]); if sky_asSkyMaybe(__subject).SkyName == "Just" { arity := sky_asSkyMaybe(__subject).JustValue; _ = arity; return func() any { if sky_asBool(sky_asInt(argCount) < sky_asInt(arity)) { return func() any { goName := func() any { if sky_asBool(sky_stringIsEmpty(sky_asMap(ctx)["modulePrefix"])) { return Compiler_Lower_SanitizeGoIdent(name) }; return sky_concat(sky_asMap(ctx)["modulePrefix"], sky_concat("_", Compiler_Lower_CapitalizeFirst(Compiler_Lower_SanitizeGoIdent(name)))) }(); _ = goName; return SkyJust(map[string]any{"goFuncName": goName, "totalArity": arity}) }() }; return SkyNothing() }() };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyNothing() };  if true { return SkyNothing() };  return nil }() }() };  return nil }() }()
}

func Compiler_Lower_GeneratePartialClosure(partial any, providedArgs any, providedCount any) any {
	return func() any { remainingCount := sky_asInt(sky_asMap(partial)["totalArity"]) - sky_asInt(providedCount); _ = remainingCount; remainingParams := sky_call(sky_listMap(func(i any) any { return sky_concat("__pa", sky_stringFromInt(i)) }), Compiler_Lower_ListRange(0, sky_asInt(remainingCount) - sky_asInt(1))); _ = remainingParams; providedArgStrs := sky_call(sky_listMap(Compiler_Lower_EmitGoExprInline), providedArgs); _ = providedArgStrs; allArgStrs := sky_call(sky_listAppend(providedArgStrs), remainingParams); _ = allArgStrs; argList := sky_call(sky_stringJoin(", "), allArgStrs); _ = argList; innerCall := sky_concat(sky_asMap(partial)["goFuncName"], sky_concat("(", sky_concat(argList, ")"))); _ = innerCall; closureCode := Compiler_Lower_BuildCurriedClosure(remainingParams, innerCall); _ = closureCode; return GoRawExpr(closureCode) }()
}

func Compiler_Lower_BuildCurriedClosure(params any, innerCall any) any {
	return func() any { return func() any { __subject := params; if len(sky_asList(__subject)) == 0 { return innerCall };  if len(sky_asList(__subject)) > 0 { p := sky_asList(__subject)[0]; _ = p; rest := sky_asList(__subject)[1:]; _ = rest; return sky_concat("func(", sky_concat(p, sky_concat(" any) any { return ", sky_concat(Compiler_Lower_BuildCurriedClosure(rest, innerCall), " }")))) };  return nil }() }()
}

func Compiler_Lower_ListRange(start any, end any) any {
	return func() any { if sky_asBool(sky_asInt(start) > sky_asInt(end)) { return []any{} }; return append([]any{start}, sky_asList(Compiler_Lower_ListRange(sky_asInt(start) + sky_asInt(1), end))...) }()
}

func Compiler_Lower_FlattenCall(ctx any, callee any, args any) any {
	return func() any { return func() any { __subject := callee; if sky_asMap(__subject)["SkyName"] == "CallExpr" { innerCallee := sky_asMap(__subject)["V0"]; _ = innerCallee; innerArgs := sky_asMap(__subject)["V1"]; _ = innerArgs; return func() any { isLocalImport := func() any { return func() any { __subject := innerCallee; if sky_asMap(__subject)["SkyName"] == "QualifiedExpr" { parts := sky_asMap(__subject)["V0"]; _ = parts; return func() any { return func() any { __subject := sky_listHead(parts); if sky_asSkyMaybe(__subject).SkyName == "Just" { modName := sky_asSkyMaybe(__subject).JustValue; _ = modName; return sky_call(sky_dictMember(modName), sky_asMap(ctx)["importAliases"]) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return false };  if true { return false };  return nil }() }() };  return nil }() }(); _ = isLocalImport; isExposedStdlib := func() any { return func() any { __subject := innerCallee; if sky_asMap(__subject)["SkyName"] == "IdentifierExpr" { idName := sky_asMap(__subject)["V0"]; _ = idName; return sky_call(sky_dictMember(idName), sky_asMap(ctx)["exposedStdlib"]) };  if true { return false };  return nil }() }(); _ = isExposedStdlib; return func() any { if sky_asBool(isLocalImport) { return Compiler_Lower_FlattenCall(ctx, innerCallee, sky_call(sky_listAppend(innerArgs), args)) }; return func() any { if sky_asBool(isExposedStdlib) { return SkyTuple2{V0: callee, V1: args} }; return func() any { if sky_asBool(Compiler_Lower_IsStdlibCallee(innerCallee)) { return SkyTuple2{V0: callee, V1: args} }; return Compiler_Lower_FlattenCall(ctx, innerCallee, sky_call(sky_listAppend(innerArgs), args)) }() }() }() }() };  if true { return SkyTuple2{V0: callee, V1: args} };  return nil }() }()
}

func Compiler_Lower_LowerLambda(ctx any, params any, body any) any {
	return func() any { lambdaParamNames := sky_call(sky_listFilterMap(Compiler_Lower_ExtractPatternName), params); _ = lambdaParamNames; lambdaCtx := sky_recordUpdate(ctx, map[string]any{"paramNames": sky_call(sky_setUnion(sky_asMap(ctx)["paramNames"]), sky_setFromList(lambdaParamNames))}); _ = lambdaCtx; return func() any { if sky_asBool(sky_listIsEmpty(params)) { return Compiler_Lower_LowerExpr(lambdaCtx, body) }; return func() any { if sky_asBool(sky_equal(sky_listLength(params), 1)) { return func() any { singleParam := func() any { return func() any { __subject := sky_listHead(params); if sky_asSkyMaybe(__subject).SkyName == "Just" { p := sky_asSkyMaybe(__subject).JustValue; _ = p; return p };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return PWildcard(emptySpan) };  return nil }() }(); _ = singleParam; paramStr := Compiler_Lower_EmitInlineParam(Compiler_Lower_LowerParam(singleParam)); _ = paramStr; bodyStr := Compiler_Lower_EmitGoExprInline(Compiler_Lower_LowerExpr(lambdaCtx, body)); _ = bodyStr; return GoRawExpr(sky_concat("func(", sky_concat(paramStr, sky_concat(") any { return ", sky_concat(bodyStr, " }"))))) }() }; return func() any { firstParam := func() any { return func() any { __subject := sky_listHead(params); if sky_asSkyMaybe(__subject).SkyName == "Just" { p := sky_asSkyMaybe(__subject).JustValue; _ = p; return p };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return PWildcard(emptySpan) };  return nil }() }(); _ = firstParam; restParams := sky_call(sky_listDrop(1), params); _ = restParams; paramStr := Compiler_Lower_EmitInlineParam(Compiler_Lower_LowerParam(firstParam)); _ = paramStr; innerStr := Compiler_Lower_EmitGoExprInline(Compiler_Lower_LowerLambda(lambdaCtx, restParams, body)); _ = innerStr; return GoRawExpr(sky_concat("func(", sky_concat(paramStr, sky_concat(") any { return ", sky_concat(innerStr, " }"))))) }() }() }() }()
}

func Compiler_Lower_LowerIf(ctx any, condition any, thenBranch any, elseBranch any) any {
	return GoRawExpr(sky_concat("func() any { if sky_asBool(", sky_concat(Compiler_Lower_ExprToGoString(ctx, condition), sky_concat(") { return ", sky_concat(Compiler_Lower_ExprToGoString(ctx, thenBranch), sky_concat(" }; return ", sky_concat(Compiler_Lower_ExprToGoString(ctx, elseBranch), " }()")))))))
}

func Compiler_Lower_LowerLet(ctx any, bindings any, body any) any {
	return func() any { stmts := sky_call(sky_listConcatMap(func(__pa0 any) any { return Compiler_Lower_LowerLetBinding(ctx, __pa0) }), bindings); _ = stmts; returnStmt := []any{GoReturn(Compiler_Lower_LowerExpr(ctx, body))}; _ = returnStmt; allStmts := sky_call(sky_listAppend(stmts), returnStmt); _ = allStmts; return GoRawExpr(sky_concat("func() any { ", sky_concat(Compiler_Lower_StmtsToGoString(allStmts), " }()"))) }()
}

func Compiler_Lower_LowerLetBinding(ctx any, binding any) any {
	return func() any { return func() any { __subject := sky_asMap(binding)["pattern"]; if sky_asMap(__subject)["SkyName"] == "PVariable" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { goName := Compiler_Lower_SanitizeGoIdent(name); _ = goName; return []any{GoShortDecl(goName, Compiler_Lower_LowerExpr(ctx, sky_asMap(binding)["value"])), GoExprStmt(GoRawExpr(sky_concat("_ = ", goName)))} }() };  if sky_asMap(__subject)["SkyName"] == "PWildcard" { return []any{GoExprStmt(Compiler_Lower_LowerExpr(ctx, sky_asMap(binding)["value"]))} };  if sky_asMap(__subject)["SkyName"] == "PTuple" { items := sky_asMap(__subject)["V0"]; _ = items; return func() any { tmpName := sky_concat("__tup_", Compiler_Lower_MakeTupleKey(items)); _ = tmpName; tmpDecl := GoShortDecl(tmpName, Compiler_Lower_LowerExpr(ctx, sky_asMap(binding)["value"])); _ = tmpDecl; tupleSize := sky_listLength(items); _ = tupleSize; extracts := Compiler_Lower_ExtractTupleBindings(tmpName, items, 0, tupleSize); _ = extracts; return append([]any{tmpDecl}, sky_asList(extracts)...) }() };  if true { return []any{GoShortDecl("_", Compiler_Lower_LowerExpr(ctx, sky_asMap(binding)["value"]))} };  return nil }() }()
}

func Compiler_Lower_ExtractTupleBindings(tmpName any, items any, idx any, tupleSize any) any {
	return func() any { return func() any { __subject := items; if len(sky_asList(__subject)) == 0 { return []any{} };  if len(sky_asList(__subject)) > 0 { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { fieldName := sky_concat("V", sky_stringFromInt(idx)); _ = fieldName; assertFn := func() any { if sky_asBool(sky_asInt(tupleSize) >= sky_asInt(3)) { return "sky_asTuple3" }; return "sky_asTuple2" }(); _ = assertFn; extract := func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PVariable" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { goName := Compiler_Lower_SanitizeGoIdent(name); _ = goName; return []any{GoShortDecl(goName, GoSelectorExpr(GoCallExpr(GoIdent(assertFn), []any{GoIdent(tmpName)}), fieldName)), GoExprStmt(GoRawExpr(sky_concat("_ = ", goName)))} }() };  if true { return []any{} };  return nil }() }(); _ = extract; return sky_call(sky_listAppend(extract), Compiler_Lower_ExtractTupleBindings(tmpName, rest, sky_asInt(idx) + sky_asInt(1), tupleSize)) }() };  return nil }() }()
}

func Compiler_Lower_LowerCase(ctx any, subject any, branches any) any {
	return func() any { subjectExpr := Compiler_Lower_LowerExpr(ctx, subject); _ = subjectExpr; switchCode := Compiler_Lower_LowerCaseToSwitch(ctx, subjectExpr, branches); _ = switchCode; return GoCallExpr(GoFuncLit([]any{}, GoRawExpr(switchCode)), []any{}) }()
}

func Compiler_Lower_LowerCaseToSwitch(ctx any, subjectExpr any, branches any) any {
	return func() any { subjectCode := Compiler_Lower_EmitGoExprInline(subjectExpr); _ = subjectCode; return sky_concat("func() any { __subject := ", sky_concat(subjectCode, sky_concat("; ", sky_concat(sky_call(sky_stringJoin(" "), sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Lower_EmitBranchCode(ctx, __pa0) }), Compiler_Lower_ZipIndex(branches))), " return nil }()")))) }()
}

func Compiler_Lower_EmitBranchCode(ctx any, pair any) any {
	return func() any { branch := sky_snd(pair); _ = branch; return func() any { condition := Compiler_Lower_PatternToCondition(ctx, "__subject", sky_asMap(branch)["pattern"]); _ = condition; bindings := Compiler_Lower_PatternToBindings(ctx, "__subject", sky_asMap(branch)["pattern"]); _ = bindings; bodyCode := Compiler_Lower_ExprToGoString(ctx, sky_asMap(branch)["body"]); _ = bodyCode; return sky_concat("if ", sky_concat(condition, sky_concat(" { ", sky_concat(bindings, sky_concat("return ", sky_concat(bodyCode, " }; ")))))) }() }()
}

func Compiler_Lower_PatternToCondition(ctx any, varName any, pat any) any {
	return func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PWildcard" { return "true" };  if sky_asMap(__subject)["SkyName"] == "PVariable" { return "true" };  if sky_asMap(__subject)["SkyName"] == "PLiteral" { lit := sky_asMap(__subject)["V0"]; _ = lit; return Compiler_Lower_LiteralCondition(varName, lit) };  if sky_asMap(__subject)["SkyName"] == "PConstructor" { parts := sky_asMap(__subject)["V0"]; _ = parts; return func() any { ctorName := Compiler_Lower_LastPartOf(parts); _ = ctorName; return func() any { if sky_asBool(sky_asBool(sky_equal(ctorName, "Ok")) || sky_asBool(sky_equal(ctorName, "Err"))) { return sky_concat("sky_asSkyResult(", sky_concat(varName, sky_concat(").SkyName == \"", sky_concat(ctorName, "\"")))) }; return func() any { if sky_asBool(sky_asBool(sky_equal(ctorName, "Just")) || sky_asBool(sky_equal(ctorName, "Nothing"))) { return sky_concat("sky_asSkyMaybe(", sky_concat(varName, sky_concat(").SkyName == \"", sky_concat(ctorName, "\"")))) }; return func() any { if sky_asBool(sky_equal(ctorName, "True")) { return sky_concat("sky_asBool(", sky_concat(varName, ") == true")) }; return func() any { if sky_asBool(sky_equal(ctorName, "False")) { return sky_concat("sky_asBool(", sky_concat(varName, ") == false")) }; return sky_concat("sky_asMap(", sky_concat(varName, sky_concat(")[\"SkyName\"] == \"", sky_concat(ctorName, "\"")))) }() }() }() }() }() };  if sky_asMap(__subject)["SkyName"] == "PTuple" { return "true" };  if sky_asMap(__subject)["SkyName"] == "PList" { items := sky_asMap(__subject)["V0"]; _ = items; return sky_concat("len(sky_asList(", sky_concat(varName, sky_concat(")) == ", sky_stringFromInt(sky_listLength(items))))) };  if sky_asMap(__subject)["SkyName"] == "PCons" { return sky_concat("len(sky_asList(", sky_concat(varName, ")) > 0")) };  if sky_asMap(__subject)["SkyName"] == "PAs" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Compiler_Lower_PatternToCondition(ctx, varName, inner) };  if sky_asMap(__subject)["SkyName"] == "PRecord" { return "true" };  if true { return "true" };  return nil }() }()
}

func Compiler_Lower_LiteralCondition(varName any, lit any) any {
	return func() any { return func() any { __subject := lit; if sky_asMap(__subject)["SkyName"] == "LitInt" { n := sky_asMap(__subject)["V0"]; _ = n; return sky_concat("sky_asInt(", sky_concat(varName, sky_concat(") == ", sky_stringFromInt(n)))) };  if sky_asMap(__subject)["SkyName"] == "LitFloat" { f := sky_asMap(__subject)["V0"]; _ = f; return sky_concat("sky_asFloat(", sky_concat(varName, sky_concat(") == ", sky_stringFromFloat(f)))) };  if sky_asMap(__subject)["SkyName"] == "LitString" { s := sky_asMap(__subject)["V0"]; _ = s; return sky_concat("sky_asString(", sky_concat(varName, sky_concat(") == ", Compiler_Lower_GoQuote(s)))) };  if sky_asMap(__subject)["SkyName"] == "LitBool" { b := sky_asMap(__subject)["V0"]; _ = b; return func() any { if sky_asBool(b) { return sky_concat("sky_asBool(", sky_concat(varName, ") == true")) }; return sky_concat("sky_asBool(", sky_concat(varName, ") == false")) }() };  if sky_asMap(__subject)["SkyName"] == "LitChar" { c := sky_asMap(__subject)["V0"]; _ = c; return sky_concat("sky_asString(", sky_concat(varName, sky_concat(") == \"", sky_concat(c, "\"")))) };  if true { return "true" };  return nil }() }()
}

func Compiler_Lower_PatternToBindings(ctx any, varName any, pat any) any {
	return func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PWildcard" { return "" };  if sky_asMap(__subject)["SkyName"] == "PVariable" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { goName := Compiler_Lower_SanitizeGoIdent(name); _ = goName; return sky_concat(goName, sky_concat(" := ", sky_concat(varName, sky_concat("; _ = ", sky_concat(goName, "; "))))) }() };  if sky_asMap(__subject)["SkyName"] == "PLiteral" { return "" };  if sky_asMap(__subject)["SkyName"] == "PConstructor" { parts := sky_asMap(__subject)["V0"]; _ = parts; argPats := sky_asMap(__subject)["V1"]; _ = argPats; return func() any { ctorName := Compiler_Lower_LastPartOf(parts); _ = ctorName; return func() any { if sky_asBool(sky_equal(ctorName, "Ok")) { return Compiler_Lower_BindConstructorArgs(ctx, varName, "sky_asSkyResult", "OkValue", argPats) }; return func() any { if sky_asBool(sky_equal(ctorName, "Err")) { return Compiler_Lower_BindConstructorArgs(ctx, varName, "sky_asSkyResult", "ErrValue", argPats) }; return func() any { if sky_asBool(sky_equal(ctorName, "Just")) { return Compiler_Lower_BindConstructorArgs(ctx, varName, "sky_asSkyMaybe", "JustValue", argPats) }; return Compiler_Lower_BindAdtConstructorArgs(ctx, varName, argPats, 0) }() }() }() }() };  if sky_asMap(__subject)["SkyName"] == "PTuple" { items := sky_asMap(__subject)["V0"]; _ = items; return Compiler_Lower_BindTupleArgs(ctx, varName, items, 0) };  if sky_asMap(__subject)["SkyName"] == "PList" { items := sky_asMap(__subject)["V0"]; _ = items; return Compiler_Lower_BindListArgs(ctx, varName, items, 0) };  if sky_asMap(__subject)["SkyName"] == "PCons" { headPat := sky_asMap(__subject)["V0"]; _ = headPat; tailPat := sky_asMap(__subject)["V1"]; _ = tailPat; return func() any { headBinding := Compiler_Lower_PatternToBindings(ctx, sky_concat("sky_asList(", sky_concat(varName, ")[0]")), headPat); _ = headBinding; tailBinding := Compiler_Lower_PatternToBindings(ctx, sky_concat("sky_asList(", sky_concat(varName, ")[1:]")), tailPat); _ = tailBinding; return sky_concat(headBinding, tailBinding) }() };  if sky_asMap(__subject)["SkyName"] == "PAs" { inner := sky_asMap(__subject)["V0"]; _ = inner; name := sky_asMap(__subject)["V1"]; _ = name; return sky_concat(Compiler_Lower_SanitizeGoIdent(name), sky_concat(" := ", sky_concat(varName, sky_concat("; ", Compiler_Lower_PatternToBindings(ctx, varName, inner))))) };  if sky_asMap(__subject)["SkyName"] == "PRecord" { fields := sky_asMap(__subject)["V0"]; _ = fields; return sky_call2(sky_listFoldl(func(f any) any { return func(acc any) any { return sky_concat(acc, sky_concat(Compiler_Lower_SanitizeGoIdent(f), sky_concat(" := sky_asMap(", sky_concat(varName, sky_concat(")[\"", sky_concat(f, "\"]; ")))))) } }), "", fields) };  if true { return "" };  return nil }() }()
}

func Compiler_Lower_BindConstructorArgs(ctx any, varName any, wrapperFn any, fieldName any, argPats any) any {
	return func() any { return func() any { __subject := argPats; if len(sky_asList(__subject)) > 0 { onePat := sky_asList(__subject)[0]; _ = onePat; return Compiler_Lower_PatternToBindings(ctx, sky_concat(wrapperFn, sky_concat("(", sky_concat(varName, sky_concat(").", fieldName)))), onePat) };  if true { return "" };  return nil }() }()
}

func Compiler_Lower_BindAdtConstructorArgs(ctx any, varName any, argPats any, idx any) any {
	return func() any { return func() any { __subject := argPats; if len(sky_asList(__subject)) == 0 { return "" };  if len(sky_asList(__subject)) > 0 { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { fieldAccess := sky_concat("sky_asMap(", sky_concat(varName, sky_concat(")[\"V", sky_concat(sky_stringFromInt(idx), "\"]")))); _ = fieldAccess; binding := Compiler_Lower_PatternToBindings(ctx, fieldAccess, pat); _ = binding; return sky_concat(binding, Compiler_Lower_BindAdtConstructorArgs(ctx, varName, rest, sky_asInt(idx) + sky_asInt(1))) }() };  return nil }() }()
}

func Compiler_Lower_BindTupleArgs(ctx any, varName any, items any, idx any) any {
	return func() any { totalItems := sky_asInt(sky_listLength(items)) + sky_asInt(idx); _ = totalItems; return func() any { return func() any { __subject := items; if len(sky_asList(__subject)) == 0 { return "" };  if len(sky_asList(__subject)) > 0 { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { fieldAccess := func() any { if sky_asBool(sky_asInt(totalItems) <= sky_asInt(2)) { return sky_concat("sky_asTuple2(", sky_concat(varName, sky_concat(").V", sky_stringFromInt(idx)))) }; return func() any { if sky_asBool(sky_asInt(totalItems) <= sky_asInt(3)) { return sky_concat("sky_asTuple3(", sky_concat(varName, sky_concat(").V", sky_stringFromInt(idx)))) }; return sky_concat("sky_asList(", sky_concat(varName, sky_concat(")[", sky_concat(sky_stringFromInt(idx), "]")))) }() }(); _ = fieldAccess; binding := Compiler_Lower_PatternToBindings(ctx, fieldAccess, pat); _ = binding; return sky_concat(binding, Compiler_Lower_BindTupleArgs(ctx, varName, rest, sky_asInt(idx) + sky_asInt(1))) }() };  return nil }() }() }()
}

func Compiler_Lower_BindListArgs(ctx any, varName any, items any, idx any) any {
	return func() any { return func() any { __subject := items; if len(sky_asList(__subject)) == 0 { return "" };  if len(sky_asList(__subject)) > 0 { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { elemAccess := sky_concat("sky_asList(", sky_concat(varName, sky_concat(")[", sky_concat(sky_stringFromInt(idx), "]")))); _ = elemAccess; binding := Compiler_Lower_PatternToBindings(ctx, elemAccess, pat); _ = binding; return sky_concat(binding, Compiler_Lower_BindListArgs(ctx, varName, rest, sky_asInt(idx) + sky_asInt(1))) }() };  return nil }() }()
}

func Compiler_Lower_LowerBinary(ctx any, op any, left any, right any) any {
	return func() any { goLeft := Compiler_Lower_LowerExpr(ctx, left); _ = goLeft; goRight := Compiler_Lower_LowerExpr(ctx, right); _ = goRight; return func() any { if sky_asBool(sky_equal(op, "|>")) { return GoCallExpr(GoIdent("sky_call"), []any{goRight, goLeft}) }; return func() any { if sky_asBool(sky_equal(op, "<|")) { return GoCallExpr(GoIdent("sky_call"), []any{goLeft, goRight}) }; return func() any { if sky_asBool(sky_equal(op, "::")) { return GoCallExpr(GoIdent("append"), []any{GoSliceLit([]any{goLeft}), GoRawExpr(sky_concat("sky_asList(", sky_concat(Compiler_Lower_EmitGoExprInline(goRight), ")...")))}) }; return func() any { if sky_asBool(sky_equal(op, "++")) { return GoRawExpr(sky_concat("sky_concat(", sky_concat(Compiler_Lower_EmitGoExprInline(goLeft), sky_concat(", ", sky_concat(Compiler_Lower_EmitGoExprInline(goRight), ")"))))) }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "+")) || sky_asBool(sky_asBool(sky_equal(op, "-")) || sky_asBool(sky_asBool(sky_equal(op, "*")) || sky_asBool(sky_equal(op, "%"))))) { return GoRawExpr(sky_concat("sky_asInt(", sky_concat(Compiler_Lower_EmitGoExprInline(goLeft), sky_concat(") ", sky_concat(op, sky_concat(" sky_asInt(", sky_concat(Compiler_Lower_EmitGoExprInline(goRight), ")"))))))) }; return func() any { if sky_asBool(sky_equal(op, "/")) { return GoRawExpr(sky_concat("sky_asFloat(", sky_concat(Compiler_Lower_EmitGoExprInline(goLeft), sky_concat(") / sky_asFloat(", sky_concat(Compiler_Lower_EmitGoExprInline(goRight), ")"))))) }; return func() any { if sky_asBool(sky_equal(op, "//")) { return GoRawExpr(sky_concat("sky_asInt(", sky_concat(Compiler_Lower_EmitGoExprInline(goLeft), sky_concat(") / sky_asInt(", sky_concat(Compiler_Lower_EmitGoExprInline(goRight), ")"))))) }; return func() any { if sky_asBool(sky_equal(op, "/=")) { return GoRawExpr(sky_concat("!sky_equal(", sky_concat(Compiler_Lower_EmitGoExprInline(goLeft), sky_concat(", ", sky_concat(Compiler_Lower_EmitGoExprInline(goRight), ")"))))) }; return func() any { if sky_asBool(sky_equal(op, "==")) { return GoCallExpr(GoIdent("sky_equal"), []any{goLeft, goRight}) }; return func() any { if sky_asBool(sky_equal(op, "!=")) { return GoUnaryExpr("!", GoCallExpr(GoIdent("sky_equal"), []any{goLeft, goRight})) }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "<")) || sky_asBool(sky_asBool(sky_equal(op, "<=")) || sky_asBool(sky_asBool(sky_equal(op, ">")) || sky_asBool(sky_equal(op, ">="))))) { return GoRawExpr(sky_concat("sky_asInt(", sky_concat(Compiler_Lower_EmitGoExprInline(goLeft), sky_concat(") ", sky_concat(op, sky_concat(" sky_asInt(", sky_concat(Compiler_Lower_EmitGoExprInline(goRight), ")"))))))) }; return func() any { if sky_asBool(sky_equal(op, "&&")) { return GoRawExpr(sky_concat("sky_asBool(", sky_concat(Compiler_Lower_EmitGoExprInline(goLeft), sky_concat(") && sky_asBool(", sky_concat(Compiler_Lower_EmitGoExprInline(goRight), ")"))))) }; return func() any { if sky_asBool(sky_equal(op, "||")) { return GoRawExpr(sky_concat("sky_asBool(", sky_concat(Compiler_Lower_EmitGoExprInline(goLeft), sky_concat(") || sky_asBool(", sky_concat(Compiler_Lower_EmitGoExprInline(goRight), ")"))))) }; return GoBinaryExpr(op, goLeft, goRight) }() }() }() }() }() }() }() }() }() }() }() }() }() }()
}

func Compiler_Lower_LowerRecordUpdate(ctx any, base any, fields any) any {
	return func() any { goBase := Compiler_Lower_LowerExpr(ctx, base); _ = goBase; goFields := GoMapLit(sky_call(sky_listMap(func(f any) any { return SkyTuple2{V0: GoStringLit(sky_asMap(f)["name"]), V1: Compiler_Lower_LowerExpr(ctx, sky_asMap(f)["value"])} }), fields)); _ = goFields; return GoCallExpr(GoIdent("sky_recordUpdate"), []any{goBase, goFields}) }()
}

func Compiler_Lower_GenerateConstructorDecls(registry any, decls any) any {
	return sky_call(sky_listConcatMap(func(__pa0 any) any { return Compiler_Lower_GenerateCtorsForDecl(registry, __pa0) }), decls)
}

func Compiler_Lower_GenerateCtorsForDecl(registry any, decl any) any {
	return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "TypeDecl" { typeName := sky_asMap(__subject)["V0"]; _ = typeName; variants := sky_asMap(__subject)["V2"]; _ = variants; return sky_call(sky_listIndexedMap(func(__pa0 any) any { return func(__pa1 any) any { return Compiler_Lower_GenerateCtorFunc(typeName, __pa0, __pa1) } }), variants) };  if true { return []any{} };  return nil }() }()
}

func Compiler_Lower_GenerateCtorFunc(typeName any, tagIndex any, variant any) any {
	return func() any { arity := sky_listLength(sky_asMap(variant)["fields"]); _ = arity; return func() any { if sky_asBool(sky_equal(arity, 0)) { return GoDeclVar(Compiler_Lower_SanitizeGoIdent(sky_asMap(variant)["name"]), GoMapLit([]any{SkyTuple2{V0: GoStringLit("Tag"), V1: GoBasicLit(sky_stringFromInt(tagIndex))}, SkyTuple2{V0: GoStringLit("SkyName"), V1: GoStringLit(sky_asMap(variant)["name"])}})) }; return func() any { params := sky_call(sky_listIndexedMap(func(i any) any { return func(_ any) any { return map[string]any{"name": sky_concat("v", sky_stringFromInt(i)), "type_": "any"} } }), sky_asMap(variant)["fields"]); _ = params; fields := sky_concat([]any{SkyTuple2{V0: "Tag", V1: GoBasicLit(sky_stringFromInt(tagIndex))}, SkyTuple2{V0: "SkyName", V1: GoStringLit(sky_asMap(variant)["name"])}}, sky_call(sky_listIndexedMap(func(i any) any { return func(_ any) any { return SkyTuple2{V0: sky_concat("V", sky_stringFromInt(i)), V1: GoIdent(sky_concat("v", sky_stringFromInt(i)))} } }), sky_asMap(variant)["fields"])); _ = fields; return GoDeclFunc(map[string]any{"name": Compiler_Lower_SanitizeGoIdent(sky_asMap(variant)["name"]), "params": params, "returnType": "any", "body": []any{GoReturn(GoMapLit(sky_call(sky_listMap(func(pair any) any { return SkyTuple2{V0: GoStringLit(sky_fst(pair)), V1: sky_snd(pair)} }), fields)))}}) }() }() }()
}

func Compiler_Lower_GenerateHelperDecls() any {
	return []any{GoDeclRaw("var skyVersion = \"dev\""), GoDeclRaw("type SkyTuple2 struct { V0, V1 any }"), GoDeclRaw("type SkyTuple3 struct { V0, V1, V2 any }"), GoDeclRaw("type SkyResult struct { Tag int; SkyName string; OkValue, ErrValue any }"), GoDeclRaw("type SkyMaybe struct { Tag int; SkyName string; JustValue any }"), GoDeclRaw("func SkyOk(v any) SkyResult { return SkyResult{Tag: 0, SkyName: \"Ok\", OkValue: v} }"), GoDeclRaw("func SkyErr(v any) SkyResult { return SkyResult{Tag: 1, SkyName: \"Err\", ErrValue: v} }"), GoDeclRaw("func SkyJust(v any) SkyMaybe { return SkyMaybe{Tag: 0, SkyName: \"Just\", JustValue: v} }"), GoDeclRaw("func SkyNothing() SkyMaybe { return SkyMaybe{Tag: 1, SkyName: \"Nothing\"} }"), GoDeclRaw("func sky_asInt(v any) int { switch x := v.(type) { case int: return x; case float64: return int(x); default: return 0 } }"), GoDeclRaw("func sky_asFloat(v any) float64 { switch x := v.(type) { case float64: return x; case int: return float64(x); default: return 0 } }"), GoDeclRaw("func sky_asString(v any) string { if s, ok := v.(string); ok { return s }; return fmt.Sprintf(\"%v\", v) }"), GoDeclRaw("func sky_asBool(v any) bool { if b, ok := v.(bool); ok { return b }; return false }"), GoDeclRaw("func sky_asList(v any) []any { if l, ok := v.([]any); ok { return l }; return nil }"), GoDeclRaw("func sky_asBytes(v any) []byte { if b, ok := v.([]byte); ok { return b }; if s, ok := v.(string); ok { return []byte(s) }; return nil }"), GoDeclRaw("func sky_asError(v any) error { if e, ok := v.(error); ok { return e }; return fmt.Errorf(\"%v\", v) }"), GoDeclRaw("func sky_asStringSlice(v any) []string { items := sky_asList(v); result := make([]string, len(items)); for i, item := range items { result[i] = sky_asString(item) }; return result }"), GoDeclRaw("func sky_asFixedBytes(v any) []byte { if b, ok := v.([]byte); ok { return b }; return nil }"), GoDeclRaw("func sky_stringToBytes(s any) any { return []byte(sky_asString(s)) }"), GoDeclRaw("func sky_stringFromBytes(b any) any { return string(sky_asBytes(b)) }"), GoDeclRaw("func sky_asMapStringAny(v any) map[string]interface{} { if m, ok := v.(map[string]interface{}); ok { return m }; return sky_asMap(v) }"), GoDeclRaw("func sky_asMapStringString(v any) map[string]string { if m, ok := v.(map[string]string); ok { return m }; result := make(map[string]string); for k, val := range sky_asMap(v) { result[sky_asString(k)] = sky_asString(val) }; return result }"), GoDeclRaw("func sky_asContext(v any) context.Context { if c, ok := v.(context.Context); ok { return c }; return context.Background() }"), GoDeclRaw("var _ = context.Background"), GoDeclRaw("func sky_asFloat32(v any) float32 { return float32(sky_asFloat(v)) }"), GoDeclRaw("func sky_asInt64(v any) int64 { return int64(sky_asInt(v)) }"), GoDeclRaw("func sky_asHttpHandler(v any) func(net_http.ResponseWriter, *net_http.Request) { fn := v.(func(any) any); return func(w net_http.ResponseWriter, r *net_http.Request) { fn(w).(func(any) any)(r) } }"), GoDeclRaw("func sky_asUint(v any) uint { return uint(sky_asInt(v)) }"), GoDeclRaw("func sky_asUint8(v any) uint8 { return uint8(sky_asInt(v)) }"), GoDeclRaw("func sky_asUint16(v any) uint16 { return uint16(sky_asInt(v)) }"), GoDeclRaw("func sky_asUint32(v any) uint32 { return uint32(sky_asInt(v)) }"), GoDeclRaw("func sky_asUint64(v any) uint64 { return uint64(sky_asInt(v)) }"), GoDeclRaw("func sky_asMap(v any) map[string]any { if m, ok := v.(map[string]any); ok { return m }; return nil }"), GoDeclRaw("func sky_equal(a, b any) bool { return fmt.Sprintf(\"%v\", a) == fmt.Sprintf(\"%v\", b) }"), GoDeclRaw("func sky_concat(a, b any) any { if la, ok := a.([]any); ok { if lb, ok := b.([]any); ok { return append(la, lb...) } }; return sky_asString(a) + sky_asString(b) }"), GoDeclRaw("func sky_stringFromInt(v any) any { return strconv.Itoa(sky_asInt(v)) }"), GoDeclRaw("func sky_stringFromFloat(v any) any { return strconv.FormatFloat(sky_asFloat(v), 'f', -1, 64) }"), GoDeclRaw("func sky_stringToUpper(v any) any { return strings.ToUpper(sky_asString(v)) }"), GoDeclRaw("func sky_stringToLower(v any) any { return strings.ToLower(sky_asString(v)) }"), GoDeclRaw("func sky_stringLength(v any) any { return len(sky_asString(v)) }"), GoDeclRaw("func sky_stringTrim(v any) any { return strings.TrimSpace(sky_asString(v)) }"), GoDeclRaw("func sky_stringContains(sub any) any { return func(s any) any { return strings.Contains(sky_asString(s), sky_asString(sub)) } }"), GoDeclRaw("func sky_stringIndexOf(needle any) any { return func(haystack any) any { return strings.Index(sky_asString(haystack), sky_asString(needle)) } }"), GoDeclRaw("func sky_jsonExtractBracketed(s any) any { str := sky_asString(s); depth := 0; inStr := false; esc := false; for i := 0; i < len(str); i++ { c := str[i]; if esc { esc = false; continue }; if c == '\\\\' && inStr { esc = true; continue }; if c == '\"' { inStr = !inStr; continue }; if inStr { continue }; if c == '[' || c == '{' { depth++ } else if c == ']' || c == '}' { depth--; if depth == 0 { return str[:i+1] } } }; return str }"), GoDeclRaw("func sky_jsonSplitArray(s any) any { str := strings.TrimSpace(sky_asString(s)); if len(str) < 2 { return []any{} }; inner := strings.TrimSpace(str[1:len(str)-1]); if len(inner) == 0 { return []any{} }; var result []any; depth := 0; start := 0; inStr := false; esc := false; for i := 0; i < len(inner); i++ { c := inner[i]; if esc { esc = false; continue }; if c == '\\\\' && inStr { esc = true; continue }; if c == '\"' { inStr = !inStr; continue }; if inStr { continue }; if c == '{' || c == '[' { depth++ } else if c == '}' || c == ']' { depth-- } else if c == ',' && depth == 0 { elem := strings.TrimSpace(inner[start:i]); if len(elem) > 0 { result = append(result, elem) }; start = i + 1 } }; last := strings.TrimSpace(inner[start:]); if len(last) > 0 { result = append(result, last) }; if result == nil { return []any{} }; return result }"), GoDeclRaw("func sky_filterSkyiByUsage(skyiSource any) any { return func(alias any) any { return func(sourceText any) any { src := sky_asString(skyiSource); al := sky_asString(alias); srcTxt := sky_asString(sourceText); lines := strings.Split(src, \"\\n\"); var header, types, usedFuncs []string; inHeader := true; var curBlock []string; for _, line := range lines { if inHeader { if strings.HasPrefix(line, \"module \") || strings.HasPrefix(line, \"import \") || strings.HasPrefix(line, \"foreign \") || strings.TrimSpace(line) == \"\" { header = append(header, line); continue } else { inHeader = false } }; if strings.HasPrefix(line, \"type \") { if len(curBlock) > 0 { name := strings.SplitN(curBlock[0], \" \", 2)[0]; if strings.Contains(srcTxt, al+\".\"+name) { usedFuncs = append(usedFuncs, curBlock...) }; curBlock = nil }; types = append(types, line); continue }; if strings.Contains(line, \" : \") && !strings.HasPrefix(line, \" \") && strings.TrimSpace(line) != \"\" && !strings.HasPrefix(line, \"type \") { if len(curBlock) > 0 { name := strings.SplitN(curBlock[0], \" \", 2)[0]; if strings.Contains(srcTxt, al+\".\"+name) { usedFuncs = append(usedFuncs, curBlock...) }; curBlock = nil }; curBlock = append(curBlock, line) } else if len(curBlock) > 0 { curBlock = append(curBlock, line) } }; if len(curBlock) > 0 { name := strings.SplitN(curBlock[0], \" \", 2)[0]; if strings.Contains(srcTxt, al+\".\"+name) { usedFuncs = append(usedFuncs, curBlock...) } }; result := append(header, \"\"); result = append(result, types...); result = append(result, \"\"); result = append(result, usedFuncs...); return strings.Join(result, \"\\n\") } } }"), GoDeclRaw("func sky_stringStartsWith(prefix any) any { return func(s any) any { return strings.HasPrefix(sky_asString(s), sky_asString(prefix)) } }"), GoDeclRaw("func sky_stringEndsWith(suffix any) any { return func(s any) any { return strings.HasSuffix(sky_asString(s), sky_asString(suffix)) } }"), GoDeclRaw("func sky_stringSplit(sep any) any { return func(s any) any { parts := strings.Split(sky_asString(s), sky_asString(sep)); result := make([]any, len(parts)); for i, p := range parts { result[i] = p }; return result } }"), GoDeclRaw("func sky_stringReplace(old any) any { return func(new_ any) any { return func(s any) any { return strings.ReplaceAll(sky_asString(s), sky_asString(old), sky_asString(new_)) } } }"), GoDeclRaw("func sky_stringToInt(s any) any { n, err := strconv.Atoi(strings.TrimSpace(sky_asString(s))); if err != nil { return SkyNothing() }; return SkyJust(n) }"), GoDeclRaw("func sky_stringToFloat(s any) any { f, err := strconv.ParseFloat(strings.TrimSpace(sky_asString(s)), 64); if err != nil { return SkyNothing() }; return SkyJust(f) }"), GoDeclRaw("func sky_stringAppend(a any) any { return func(b any) any { return sky_asString(a) + sky_asString(b) } }"), GoDeclRaw("func sky_stringIsEmpty(v any) any { return sky_asString(v) == \"\" }"), GoDeclRaw("func sky_stringSlice(start any) any { return func(end any) any { return func(s any) any { str := sky_asString(s); return str[sky_asInt(start):sky_asInt(end)] } } }"), GoDeclRaw("func sky_stringJoin(sep any) any { return func(list any) any { parts := sky_asList(list); ss := make([]string, len(parts)); for i, p := range parts { ss[i] = sky_asString(p) }; return strings.Join(ss, sky_asString(sep)) } }"), GoDeclRaw("func sky_listMap(fn any) any { return func(list any) any { items := sky_asList(list); result := make([]any, len(items)); for i, item := range items { result[i] = fn.(func(any) any)(item) }; return result } }"), GoDeclRaw("func sky_listFilter(fn any) any { return func(list any) any { items := sky_asList(list); var result []any; for _, item := range items { if sky_asBool(fn.(func(any) any)(item)) { result = append(result, item) } }; return result } }"), GoDeclRaw("func sky_listFoldl(fn any) any { return func(init any) any { return func(list any) any { acc := init; for _, item := range sky_asList(list) { acc = fn.(func(any) any)(item).(func(any) any)(acc) }; return acc } } }"), GoDeclRaw("func sky_listFoldr(fn any) any { return func(init any) any { return func(list any) any { items := sky_asList(list); acc := init; for i := len(items) - 1; i >= 0; i-- { acc = fn.(func(any) any)(items[i]).(func(any) any)(acc) }; return acc } } }"), GoDeclRaw("func sky_listLength(list any) any { return len(sky_asList(list)) }"), GoDeclRaw("func sky_listHead(list any) any { items := sky_asList(list); if len(items) > 0 { return SkyJust(items[0]) }; return SkyNothing() }"), GoDeclRaw("func sky_listReverse(list any) any { items := sky_asList(list); result := make([]any, len(items)); for i, item := range items { result[len(items)-1-i] = item }; return result }"), GoDeclRaw("func sky_listIsEmpty(list any) any { return len(sky_asList(list)) == 0 }"), GoDeclRaw("func sky_listAppend(a any) any { return func(b any) any { return append(sky_asList(a), sky_asList(b)...) } }"), GoDeclRaw("func sky_listConcatMap(fn any) any { return func(list any) any { var result []any; for _, item := range sky_asList(list) { result = append(result, sky_asList(fn.(func(any) any)(item))...) }; if result == nil { return []any{} }; return result } }"), GoDeclRaw("func sky_listConcat(lists any) any { var result []any; for _, l := range sky_asList(lists) { result = append(result, sky_asList(l)...) }; if result == nil { return []any{} }; return result }"), GoDeclRaw("func sky_listFilterMap(fn any) any { return func(list any) any { var result []any; for _, item := range sky_asList(list) { r := fn.(func(any) any)(item); if m, ok := r.(SkyMaybe); ok && m.Tag == 0 { result = append(result, m.JustValue) } }; if result == nil { return []any{} }; return result } }"), GoDeclRaw("func sky_listIndexedMap(fn any) any { return func(list any) any { items := sky_asList(list); result := make([]any, len(items)); for i, item := range items { result[i] = fn.(func(any) any)(i).(func(any) any)(item) }; return result } }"), GoDeclRaw("func sky_listDrop(n any) any { return func(list any) any { items := sky_asList(list); c := sky_asInt(n); if c >= len(items) { return []any{} }; return items[c:] } }"), GoDeclRaw("func sky_listMember(item any) any { return func(list any) any { for _, x := range sky_asList(list) { if sky_equal(x, item) { return true } }; return false } }"), GoDeclRaw("func sky_recordUpdate(base any, updates any) any { m := sky_asMap(base); result := make(map[string]any); for k, v := range m { result[k] = v }; for k, v := range sky_asMap(updates) { result[k] = v }; return result }"), GoDeclRaw("func sky_println(args ...any) any { ss := make([]any, len(args)); for i, a := range args { ss[i] = sky_asString(a) }; fmt.Println(ss...); return struct{}{} }"), GoDeclRaw("func sky_exit(code any) any { os.Exit(sky_asInt(code)); return struct{}{} }"), GoDeclRaw("var _ = strings.Contains"), GoDeclRaw("var _ = strconv.Itoa"), GoDeclRaw("var _ = os.Exit"), GoDeclRaw("func sky_asSkyResult(v any) SkyResult { if r, ok := v.(SkyResult); ok { return r }; return SkyResult{} }"), GoDeclRaw("func sky_asSkyMaybe(v any) SkyMaybe { if m, ok := v.(SkyMaybe); ok { return m }; return SkyMaybe{Tag: 1, SkyName: \"Nothing\"} }"), GoDeclRaw("func sky_asTuple2(v any) SkyTuple2 { if t, ok := v.(SkyTuple2); ok { return t }; return SkyTuple2{} }"), GoDeclRaw("func sky_asTuple3(v any) SkyTuple3 { if t, ok := v.(SkyTuple3); ok { return t }; return SkyTuple3{} }"), GoDeclRaw("func sky_not(v any) any { return !sky_asBool(v) }"), GoDeclRaw("func sky_fileRead(path any) any { data, err := os.ReadFile(sky_asString(path)); if err != nil { return SkyErr(err.Error()) }; return SkyOk(string(data)) }"), GoDeclRaw("func sky_fileWrite(path any) any { return func(content any) any { err := os.WriteFile(sky_asString(path), []byte(sky_asString(content)), 0644); if err != nil { return SkyErr(err.Error()) }; return SkyOk(struct{}{}) } }"), GoDeclRaw("func sky_fileMkdirAll(path any) any { err := os.MkdirAll(sky_asString(path), 0755); if err != nil { return SkyErr(err.Error()) }; return SkyOk(struct{}{}) }"), GoDeclRaw("func sky_processRun(cmd any) any { return func(args any) any { argStrs := sky_asList(args); cmdArgs := make([]string, len(argStrs)); for i, a := range argStrs { cmdArgs[i] = sky_asString(a) }; out, err := exec.Command(sky_asString(cmd), cmdArgs...).CombinedOutput(); if err != nil { return SkyErr(err.Error() + \": \" + string(out)) }; return SkyOk(string(out)) } }"), GoDeclRaw("func sky_processExit(code any) any { os.Exit(sky_asInt(code)); return struct{}{} }"), GoDeclRaw("func sky_processGetArgs(u any) any { args := make([]any, len(os.Args)); for i, a := range os.Args { args[i] = a }; return args }"), GoDeclRaw("func sky_processGetArg(n any) any { idx := sky_asInt(n); if idx < len(os.Args) { return SkyJust(os.Args[idx]) }; return SkyNothing() }"), GoDeclRaw("func sky_refNew(v any) any { return &SkyRef{Value: v} }"), GoDeclRaw("type SkyRef struct { Value any }"), GoDeclRaw("func sky_refGet(r any) any { return r.(*SkyRef).Value }"), GoDeclRaw("func sky_refSet(v any) any { return func(r any) any { r.(*SkyRef).Value = v; return struct{}{} } }"), GoDeclRaw("func sky_dictEmpty() any { return map[string]any{} }"), GoDeclRaw("func sky_dictInsert(k any) any { return func(v any) any { return func(d any) any { m := sky_asMap(d); result := make(map[string]any, len(m)+1); for key, val := range m { result[key] = val }; result[sky_asString(k)] = v; return result } } }"), GoDeclRaw("func sky_dictGet(k any) any { return func(d any) any { m := sky_asMap(d); if v, ok := m[sky_asString(k)]; ok { return SkyJust(v) }; return SkyNothing() } }"), GoDeclRaw("func sky_dictKeys(d any) any { m := sky_asMap(d); keys := make([]any, 0, len(m)); for k := range m { keys = append(keys, k) }; return keys }"), GoDeclRaw("func sky_dictValues(d any) any { m := sky_asMap(d); vals := make([]any, 0, len(m)); for _, v := range m { vals = append(vals, v) }; return vals }"), GoDeclRaw("func sky_dictToList(d any) any { m := sky_asMap(d); pairs := make([]any, 0, len(m)); for k, v := range m { pairs = append(pairs, SkyTuple2{k, v}) }; return pairs }"), GoDeclRaw("func sky_dictFromList(list any) any { result := make(map[string]any); for _, item := range sky_asList(list) { t := sky_asTuple2(item); result[sky_asString(t.V0)] = t.V1 }; return result }"), GoDeclRaw("func sky_dictMap(fn any) any { return func(d any) any { m := sky_asMap(d); result := make(map[string]any, len(m)); for k, v := range m { result[k] = fn.(func(any) any)(k).(func(any) any)(v) }; return result } }"), GoDeclRaw("func sky_dictFoldl(fn any) any { return func(init any) any { return func(d any) any { acc := init; for k, v := range sky_asMap(d) { acc = fn.(func(any) any)(k).(func(any) any)(v).(func(any) any)(acc) }; return acc } } }"), GoDeclRaw("func sky_dictUnion(a any) any { return func(b any) any { ma, mb := sky_asMap(a), sky_asMap(b); result := make(map[string]any, len(ma)+len(mb)); for k, v := range mb { result[k] = v }; for k, v := range ma { result[k] = v }; return result } }"), GoDeclRaw("func sky_dictRemove(k any) any { return func(d any) any { m := sky_asMap(d); result := make(map[string]any, len(m)); key := sky_asString(k); for k2, v := range m { if k2 != key { result[k2] = v } }; return result } }"), GoDeclRaw("func sky_dictMember(k any) any { return func(d any) any { _, ok := sky_asMap(d)[sky_asString(k)]; return ok } }"), GoDeclRaw("func sky_setEmpty() any { return map[string]bool{} }"), GoDeclRaw("func sky_setSingleton(v any) any { return map[string]bool{sky_asString(v): true} }"), GoDeclRaw("func sky_setInsert(v any) any { return func(s any) any { m := s.(map[string]bool); result := make(map[string]bool, len(m)+1); for k := range m { result[k] = true }; result[sky_asString(v)] = true; return result } }"), GoDeclRaw("func sky_setMember(v any) any { return func(s any) any { return s.(map[string]bool)[sky_asString(v)] } }"), GoDeclRaw("func sky_setUnion(a any) any { return func(b any) any { ma, mb := a.(map[string]bool), b.(map[string]bool); result := make(map[string]bool, len(ma)+len(mb)); for k := range mb { result[k] = true }; for k := range ma { result[k] = true }; return result } }"), GoDeclRaw("func sky_setDiff(a any) any { return func(b any) any { ma, mb := a.(map[string]bool), b.(map[string]bool); result := make(map[string]bool); for k := range ma { if !mb[k] { result[k] = true } }; return result } }"), GoDeclRaw("func sky_setToList(s any) any { m := s.(map[string]bool); result := make([]any, 0, len(m)); for k := range m { result = append(result, k) }; return result }"), GoDeclRaw("func sky_setFromList(list any) any { result := make(map[string]bool); for _, item := range sky_asList(list) { result[sky_asString(item)] = true }; return result }"), GoDeclRaw("func sky_setIsEmpty(s any) any { return len(s.(map[string]bool)) == 0 }"), GoDeclRaw("func sky_setRemove(v any) any { return func(s any) any { m := s.(map[string]bool); result := make(map[string]bool, len(m)); key := sky_asString(v); for k := range m { if k != key { result[k] = true } }; return result } }"), GoDeclRaw("func sky_readLine(u any) any { if stdinReader == nil { stdinReader = bufio.NewReader(os.Stdin) }; line, err := stdinReader.ReadString('\\n'); if err != nil && len(line) == 0 { return SkyNothing() }; return SkyJust(strings.TrimRight(line, \"\\r\\n\")) }"), GoDeclRaw("func sky_readBytes(n any) any { if stdinReader == nil { stdinReader = bufio.NewReader(os.Stdin) }; count := sky_asInt(n); buf := make([]byte, count); total := 0; for total < count { nr, err := stdinReader.Read(buf[total:]); total += nr; if err != nil { break } }; if total == 0 { return SkyNothing() }; return SkyJust(string(buf[:total])) }"), GoDeclRaw("func sky_writeStdout(s any) any { fmt.Print(sky_asString(s)); return struct{}{} }"), GoDeclRaw("func sky_writeStderr(s any) any { fmt.Fprint(os.Stderr, sky_asString(s)); return struct{}{} }"), GoDeclRaw("var stdinReader *bufio.Reader"), GoDeclRaw("func sky_charIsUpper(c any) any { s := sky_asString(c); if len(s) > 0 { r := rune(s[0]); return r >= 'A' && r <= 'Z' }; return false }"), GoDeclRaw("func sky_charIsLower(c any) any { s := sky_asString(c); if len(s) > 0 { r := rune(s[0]); return r >= 'a' && r <= 'z' }; return false }"), GoDeclRaw("func sky_charIsDigit(c any) any { s := sky_asString(c); if len(s) > 0 { r := rune(s[0]); return r >= '0' && r <= '9' }; return false }"), GoDeclRaw("func sky_charIsAlpha(c any) any { s := sky_asString(c); if len(s) > 0 { r := rune(s[0]); return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') }; return false }"), GoDeclRaw("func sky_charIsAlphaNum(c any) any { s := sky_asString(c); if len(s) > 0 { r := rune(s[0]); return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') }; return false }"), GoDeclRaw("func sky_charToUpper(c any) any { return strings.ToUpper(sky_asString(c)) }"), GoDeclRaw("func sky_charToLower(c any) any { return strings.ToLower(sky_asString(c)) }"), GoDeclRaw("func sky_stringFromChar(c any) any { return sky_asString(c) }"), GoDeclRaw("func sky_stringToList(s any) any { str := sky_asString(s); result := make([]any, len(str)); for i, r := range str { result[i] = string(r) }; return result }"), GoDeclRaw("var _ = exec.Command"), GoDeclRaw("var _ = bufio.NewReader"), GoDeclRaw("func sky_escapeGoString(s any) any { q := strconv.Quote(sky_asString(s)); return q[1:len(q)-1] }"), GoDeclRaw("func sky_goQuote(s any) any { return strconv.Quote(sky_asString(s)) }"), GoDeclRaw("func sky_fst(t any) any { return sky_asTuple2(t).V0 }"), GoDeclRaw("func sky_snd(t any) any { return sky_asTuple2(t).V1 }"), GoDeclRaw("func sky_errorToString(e any) any { return sky_asString(e) }"), GoDeclRaw("func sky_identity(v any) any { return v }"), GoDeclRaw("func sky_always(a any) any { return func(b any) any { return a } }"), GoDeclRaw("func sky_js(v any) any { return v }"), GoDeclRaw("func sky_call(f any, arg any) any { return f.(func(any) any)(arg) }"), GoDeclRaw("func sky_call2(f any, a any, b any) any { return f.(func(any) any)(a).(func(any) any)(b) }"), GoDeclRaw("func sky_call3(f any, a any, b any, c any) any { return f.(func(any) any)(a).(func(any) any)(b).(func(any) any)(c) }"), GoDeclRaw("func sky_taskSucceed(value any) any { return func() any { return SkyOk(value) } }"), GoDeclRaw("func sky_taskFail(err any) any { return func() any { return SkyErr(err) } }"), GoDeclRaw("func sky_taskMap(fn any) any { return func(task any) any { return func() any { r := sky_runTask(task); if sky_asSkyResult(r).Tag == 0 { return SkyOk(fn.(func(any) any)(sky_asSkyResult(r).OkValue)) }; return r } } }"), GoDeclRaw("func sky_taskAndThen(fn any) any { return func(task any) any { return func() any { r := sky_runTask(task); if sky_asSkyResult(r).Tag == 0 { next := fn.(func(any) any)(sky_asSkyResult(r).OkValue); return sky_runTask(next) }; return r } } }"), GoDeclRaw("func sky_taskPerform(task any) any { return sky_runTask(task) }"), GoDeclRaw("func sky_taskSequence(tasks any) any { return func() any { items := sky_asList(tasks); results := make([]any, 0, len(items)); for _, t := range items { r := sky_runTask(t); if sky_asSkyResult(r).Tag == 1 { return r }; results = append(results, sky_asSkyResult(r).OkValue) }; return SkyOk(results) } }"), GoDeclRaw("func sky_runTask(task any) any { if t, ok := task.(func() any); ok { defer func() { if r := recover(); r != nil { } }(); return t() }; if r, ok := task.(SkyResult); ok { return r }; return SkyOk(task) }"), GoDeclRaw("func sky_runMainTask(result any) { if _, ok := result.(func() any); ok { r := sky_runTask(result); if sky_asSkyResult(r).Tag == 1 { fmt.Fprintln(os.Stderr, sky_asSkyResult(r).ErrValue); os.Exit(1) } } }"), GoDeclRaw("var _ = net_http.ListenAndServe"), GoDeclRaw("var _ = io.ReadAll"), GoDeclRaw("func sky_serverListen(port any) any { return func(routes any) any { return func() any { mux := sky_buildMux(sky_asList(routes), \"\"); addr := fmt.Sprintf(\":%d\", sky_asInt(port)); fmt.Fprintf(os.Stderr, \"Sky server listening on %s\\n\", addr); err := net_http.ListenAndServe(addr, mux); if err != nil { return SkyErr(err.Error()) }; return SkyOk(struct{}{}) } } }"), GoDeclRaw("func sky_serverGet(pattern any) any { return func(handler any) any { return map[string]any{\"SkyName\": \"RouteEntry\", \"V0\": \"GET\", \"V1\": pattern, \"V2\": handler} } }"), GoDeclRaw("func sky_serverPost(pattern any) any { return func(handler any) any { return map[string]any{\"SkyName\": \"RouteEntry\", \"V0\": \"POST\", \"V1\": pattern, \"V2\": handler} } }"), GoDeclRaw("func sky_serverPut(pattern any) any { return func(handler any) any { return map[string]any{\"SkyName\": \"RouteEntry\", \"V0\": \"PUT\", \"V1\": pattern, \"V2\": handler} } }"), GoDeclRaw("func sky_serverDelete(pattern any) any { return func(handler any) any { return map[string]any{\"SkyName\": \"RouteEntry\", \"V0\": \"DELETE\", \"V1\": pattern, \"V2\": handler} } }"), GoDeclRaw("func sky_serverAny(pattern any) any { return func(handler any) any { return map[string]any{\"SkyName\": \"RouteEntry\", \"V0\": \"*\", \"V1\": pattern, \"V2\": handler} } }"), GoDeclRaw("func sky_serverGroup(prefix any) any { return func(routes any) any { return map[string]any{\"SkyName\": \"RouteGroup\", \"V0\": prefix, \"V1\": routes} } }"), GoDeclRaw("func sky_serverStatic(urlPrefix any) any { return func(dir any) any { return map[string]any{\"SkyName\": \"RouteStatic\", \"V0\": urlPrefix, \"V1\": dir} } }"), GoDeclRaw("func sky_serverText(body any) any { return map[string]any{\"status\": 200, \"body\": body, \"headers\": []any{SkyTuple2{\"Content-Type\", \"text/plain; charset=utf-8\"}}, \"cookies\": []any{}} }"), GoDeclRaw("func sky_serverHtml(body any) any { return map[string]any{\"status\": 200, \"body\": body, \"headers\": []any{SkyTuple2{\"Content-Type\", \"text/html; charset=utf-8\"}}, \"cookies\": []any{}} }"), GoDeclRaw("func sky_serverJson(body any) any { return map[string]any{\"status\": 200, \"body\": body, \"headers\": []any{SkyTuple2{\"Content-Type\", \"application/json\"}}, \"cookies\": []any{}} }"), GoDeclRaw("func sky_serverRedirect(url any) any { return map[string]any{\"status\": 302, \"body\": \"\", \"headers\": []any{SkyTuple2{\"Location\", url}}, \"cookies\": []any{}} }"), GoDeclRaw("func sky_serverWithStatus(status any) any { return func(resp any) any { m := sky_asMap(resp); result := make(map[string]any); for k, v := range m { result[k] = v }; result[\"status\"] = status; return result } }"), GoDeclRaw("func sky_serverWithHeader(key any) any { return func(val any) any { return func(resp any) any { m := sky_asMap(resp); result := make(map[string]any); for k, v := range m { result[k] = v }; hdrs := sky_asList(m[\"headers\"]); result[\"headers\"] = append(hdrs, SkyTuple2{key, val}); return result } } }"), GoDeclRaw("func sky_serverWithCookie(cookie any) any { return func(resp any) any { m := sky_asMap(resp); result := make(map[string]any); for k, v := range m { result[k] = v }; cookies := sky_asList(m[\"cookies\"]); result[\"cookies\"] = append(cookies, cookie); return result } }"), GoDeclRaw("func sky_serverParam(name any) any { return func(req any) any { m := sky_asMap(req); path := sky_asString(m[\"path\"]); params := sky_asList(m[\"params\"]); for _, p := range params { t := sky_asTuple2(p); if sky_asString(t.V0) == sky_asString(name) { return SkyJust(t.V1) } }; return sky_extractPathParam(sky_asString(name), path) } }"), GoDeclRaw("func sky_extractPathParam(name string, path string) any { return SkyNothing() }"), GoDeclRaw("func sky_serverQueryParam(name any) any { return func(req any) any { m := sky_asMap(req); query := sky_asList(m[\"query\"]); for _, p := range query { t := sky_asTuple2(p); if sky_asString(t.V0) == sky_asString(name) { return SkyJust(t.V1) } }; return SkyNothing() } }"), GoDeclRaw("func sky_serverHeader(name any) any { return func(req any) any { m := sky_asMap(req); headers := sky_asList(m[\"headers\"]); for _, h := range headers { t := sky_asTuple2(h); if strings.EqualFold(sky_asString(t.V0), sky_asString(name)) { return SkyJust(t.V1) } }; return SkyNothing() } }"), GoDeclRaw("func sky_serverGetCookie(name any) any { return func(req any) any { m := sky_asMap(req); cookies := sky_asList(m[\"cookies\"]); for _, c := range cookies { t := sky_asTuple2(c); if sky_asString(t.V0) == sky_asString(name) { return SkyJust(t.V1) } }; return SkyNothing() } }"), GoDeclRaw("func sky_serverCookie(name any) any { return func(val any) any { return map[string]any{\"name\": name, \"value\": val, \"path\": \"/\", \"maxAge\": 86400, \"httpOnly\": true, \"secure\": false, \"sameSite\": \"lax\"} } }"), GoDeclRaw("func sky_buildMux(routes []any, prefix string) *net_http.ServeMux { mux := net_http.NewServeMux(); for _, r := range routes { rm := sky_asMap(r); if rm == nil { continue }; skyName, _ := rm[\"SkyName\"].(string); switch skyName { case \"RouteEntry\": method := sky_asString(rm[\"V0\"]); pattern := prefix + sky_asString(rm[\"V1\"]); handler := rm[\"V2\"]; muxPattern := pattern; if method != \"*\" { muxPattern = method + \" \" + pattern }; mux.HandleFunc(muxPattern, sky_makeHandler(handler)); case \"RouteGroup\": groupPrefix := prefix + sky_asString(rm[\"V0\"]); groupRoutes := sky_asList(rm[\"V1\"]); subMux := sky_buildMux(groupRoutes, groupPrefix); mux.Handle(groupPrefix+\"/\", subMux); case \"RouteStatic\": urlPrefix := prefix + sky_asString(rm[\"V0\"]); dirPath := sky_asString(rm[\"V1\"]); fs := net_http.FileServer(net_http.Dir(dirPath)); mux.Handle(urlPrefix+\"/\", net_http.StripPrefix(urlPrefix, fs)) } }; return mux }"), GoDeclRaw("func sky_makeHandler(handler any) func(net_http.ResponseWriter, *net_http.Request) { return func(w net_http.ResponseWriter, r *net_http.Request) { defer func() { if rec := recover(); rec != nil { net_http.Error(w, fmt.Sprintf(\"Internal Server Error: %v\", rec), 500); fmt.Fprintf(os.Stderr, \"%s %s 500 (panic: %v)\\n\", r.Method, r.URL.Path, rec) } }(); skyReq := sky_buildRequest(r); fn, ok := handler.(func(any) any); if !ok { net_http.Error(w, \"Invalid handler\", 500); return }; taskResult := fn(skyReq); var skyResp any; if thunk, ok := taskResult.(func() any); ok { skyResp = thunk() } else if result, ok := taskResult.(SkyResult); ok { skyResp = result } else { skyResp = SkyOk(taskResult) }; result, ok2 := skyResp.(SkyResult); if !ok2 { sky_writeResponse(w, sky_asMap(skyResp)); return }; if result.Tag == 1 { net_http.Error(w, sky_asString(result.ErrValue), 500); return }; sky_writeResponse(w, sky_asMap(result.OkValue)) } }"), GoDeclRaw("func sky_buildRequest(r *net_http.Request) map[string]any { _ = r.ParseForm(); body, _ := io.ReadAll(r.Body); headers := make([]any, 0); for k, vs := range r.Header { for _, v := range vs { headers = append(headers, SkyTuple2{k, v}) } }; cookies := make([]any, 0); for _, c := range r.Cookies() { cookies = append(cookies, SkyTuple2{c.Name, c.Value}) }; query := make([]any, 0); for k, vs := range r.URL.Query() { for _, v := range vs { query = append(query, SkyTuple2{k, v}) } }; params := make([]any, 0); protocol := \"http\"; if r.TLS != nil { protocol = \"https\" }; return map[string]any{\"method\": r.Method, \"path\": r.URL.Path, \"body\": string(body), \"headers\": headers, \"params\": params, \"query\": query, \"cookies\": cookies, \"remoteAddr\": r.RemoteAddr, \"host\": r.Host, \"protocol\": protocol} }"), GoDeclRaw("func sky_writeResponse(w net_http.ResponseWriter, resp map[string]any) { if resp == nil { w.WriteHeader(200); return }; if cookies, ok := resp[\"cookies\"].([]any); ok { for _, c := range cookies { cm := sky_asMap(c); if cm == nil { continue }; net_http.SetCookie(w, &net_http.Cookie{Name: sky_asString(cm[\"name\"]), Value: sky_asString(cm[\"value\"]), Path: sky_asString(cm[\"path\"]), MaxAge: sky_asInt(cm[\"maxAge\"]), HttpOnly: sky_asBool(cm[\"httpOnly\"]), Secure: sky_asBool(cm[\"secure\"])}) } }; if headers, ok := resp[\"headers\"].([]any); ok { for _, h := range headers { if t, ok := h.(SkyTuple2); ok { w.Header().Set(sky_asString(t.V0), sky_asString(t.V1)) } } }; status := sky_asInt(resp[\"status\"]); if status == 0 { status = 200 }; w.WriteHeader(status); fmt.Fprint(w, sky_asString(resp[\"body\"])) }"), GoDeclRaw("func sky_resultMap(fn any) any { return func(r any) any { res := sky_asSkyResult(r); if res.Tag == 0 { return SkyOk(fn.(func(any) any)(res.OkValue)) }; return r } }"), GoDeclRaw("func sky_resultWithDefault(def any) any { return func(r any) any { res := sky_asSkyResult(r); if res.Tag == 0 { return res.OkValue }; return def } }"), GoDeclRaw("func sky_resultAndThen(fn any) any { return func(r any) any { res := sky_asSkyResult(r); if res.Tag == 0 { return fn.(func(any) any)(res.OkValue) }; return r } }"), GoDeclRaw("func sky_resultMapError(fn any) any { return func(r any) any { res := sky_asSkyResult(r); if res.Tag == 1 { return SkyErr(fn.(func(any) any)(res.ErrValue)) }; return r } }"), GoDeclRaw("func sky_maybeWithDefault(def any) any { return func(m any) any { mb := sky_asSkyMaybe(m); if mb.Tag == 0 { return mb.JustValue }; return def } }"), GoDeclRaw("func sky_maybeMap(fn any) any { return func(m any) any { mb := sky_asSkyMaybe(m); if mb.Tag == 0 { return SkyJust(fn.(func(any) any)(mb.JustValue)) }; return m } }"), GoDeclRaw("func sky_maybeAndThen(fn any) any { return func(m any) any { mb := sky_asSkyMaybe(m); if mb.Tag == 0 { return fn.(func(any) any)(mb.JustValue) }; return m } }"), GoDeclRaw("func sky_listTake(n any) any { return func(list any) any { items := sky_asList(list); c := sky_asInt(n); if c >= len(items) { return list }; return items[:c] } }"), GoDeclRaw("func sky_listSort(list any) any { items := sky_asList(list); result := make([]any, len(items)); copy(result, items); sort.Slice(result, func(i, j int) bool { return fmt.Sprintf(\"%v\", result[i]) < fmt.Sprintf(\"%v\", result[j]) }); return result }"), GoDeclRaw("func sky_listZip(a any) any { return func(b any) any { la, lb := sky_asList(a), sky_asList(b); minLen := len(la); if len(lb) < minLen { minLen = len(lb) }; result := make([]any, minLen); for i := 0; i < minLen; i++ { result[i] = SkyTuple2{la[i], lb[i]} }; return result } }"), GoDeclRaw("func sky_listRange(from any) any { return func(to any) any { f, t := sky_asInt(from), sky_asInt(to); result := make([]any, 0); for i := f; i <= t; i++ { result = append(result, i) }; return result } }"), GoDeclRaw("func sky_listAny(fn any) any { return func(list any) any { for _, item := range sky_asList(list) { if sky_asBool(fn.(func(any) any)(item)) { return true } }; return false } }"), GoDeclRaw("func sky_listAll(fn any) any { return func(list any) any { for _, item := range sky_asList(list) { if !sky_asBool(fn.(func(any) any)(item)) { return false } }; return true } }"), GoDeclRaw("func sky_listSingleton(v any) any { return []any{v} }"), GoDeclRaw("func sky_listIntersperse(sep any) any { return func(list any) any { items := sky_asList(list); if len(items) <= 1 { return list }; result := make([]any, 0, len(items)*2-1); for i, item := range items { if i > 0 { result = append(result, sep) }; result = append(result, item) }; return result } }"), GoDeclRaw("func sky_stringLeft(n any) any { return func(s any) any { str := sky_asString(s); c := sky_asInt(n); if c >= len(str) { return str }; return str[:c] } }"), GoDeclRaw("func sky_stringRight(n any) any { return func(s any) any { str := sky_asString(s); c := sky_asInt(n); if c >= len(str) { return str }; return str[len(str)-c:] } }"), GoDeclRaw("func sky_stringPadLeft(n any) any { return func(ch any) any { return func(s any) any { str := sky_asString(s); pad := sky_asString(ch); for len(str) < sky_asInt(n) { str = pad + str }; return str } } }"), GoDeclRaw("func sky_stringLines(s any) any { parts := strings.Split(sky_asString(s), \"\\n\"); result := make([]any, len(parts)); for i, p := range parts { result[i] = p }; return result }"), GoDeclRaw("func sky_stringWords(s any) any { words := strings.Fields(sky_asString(s)); result := make([]any, len(words)); for i, w := range words { result[i] = w }; return result }"), GoDeclRaw("func sky_stringRepeat(n any) any { return func(s any) any { return strings.Repeat(sky_asString(s), sky_asInt(n)) } }"), GoDeclRaw("var _ = math.Sqrt"), GoDeclRaw("func sky_mathSqrt(v any) any { return math.Sqrt(sky_asFloat(v)) }"), GoDeclRaw("func sky_mathPow(base any) any { return func(exp any) any { return math.Pow(sky_asFloat(base), sky_asFloat(exp)) } }"), GoDeclRaw("func sky_mathAbs(v any) any { return math.Abs(sky_asFloat(v)) }"), GoDeclRaw("func sky_mathFloor(v any) any { return int(math.Floor(sky_asFloat(v))) }"), GoDeclRaw("func sky_mathCeil(v any) any { return int(math.Ceil(sky_asFloat(v))) }"), GoDeclRaw("func sky_mathRound(v any) any { return int(math.Round(sky_asFloat(v))) }"), GoDeclRaw("func sky_mathMin(a any) any { return func(b any) any { af, bf := sky_asFloat(a), sky_asFloat(b); if af < bf { return af }; return bf } }"), GoDeclRaw("func sky_mathMax(a any) any { return func(b any) any { af, bf := sky_asFloat(a), sky_asFloat(b); if af > bf { return af }; return bf } }"), GoDeclRaw("func sky_modBy(m any) any { return func(n any) any { mod := sky_asInt(m); if mod == 0 { return 0 }; return sky_asInt(n) % mod } }"), GoDeclRaw("var _ = crypto_sha256.New"), GoDeclRaw("var _ = crypto_md5.New"), GoDeclRaw("var _ = hex.EncodeToString"), GoDeclRaw("var _ = base64.StdEncoding"), GoDeclRaw("func sky_cryptoSha256(s any) any { h := crypto_sha256.Sum256([]byte(sky_asString(s))); return hex.EncodeToString(h[:]) }"), GoDeclRaw("func sky_cryptoMd5(s any) any { h := crypto_md5.Sum([]byte(sky_asString(s))); return hex.EncodeToString(h[:]) }"), GoDeclRaw("func sky_encodingHexEncode(s any) any { return hex.EncodeToString([]byte(sky_asString(s))) }"), GoDeclRaw("func sky_encodingBase64Encode(s any) any { return base64.StdEncoding.EncodeToString([]byte(sky_asString(s))) }"), GoDeclRaw("func sky_encodingBase64Decode(s any) any { b, err := base64.StdEncoding.DecodeString(sky_asString(s)); if err != nil { return SkyErr(err.Error()) }; return SkyOk(string(b)) }"), GoDeclRaw("func sky_timeNow(u any) any { return time.Now().UnixMilli() }"), GoDeclRaw("func sky_timePosixToMillis(t any) any { return sky_asInt(t) }"), GoDeclRaw("func sky_httpGetString(url any) any { return func() any { resp, err := net_http.Get(sky_asString(url)); if err != nil { return SkyErr(err.Error()) }; defer resp.Body.Close(); body, err := io.ReadAll(resp.Body); if err != nil { return SkyErr(err.Error()) }; return SkyOk(string(body)) } }"), GoDeclRaw("var _ = encoding_json.Marshal"), GoDeclRaw("func sky_jsonEncString(v any) any { return sky_asString(v) }"), GoDeclRaw("func sky_jsonEncInt(v any) any { return sky_asInt(v) }"), GoDeclRaw("func sky_jsonEncFloat(v any) any { return sky_asFloat(v) }"), GoDeclRaw("func sky_jsonEncBool(v any) any { return sky_asBool(v) }"), GoDeclRaw("func sky_jsonEncNull() any { return nil }"), GoDeclRaw("func sky_jsonEncList(encoder any) any { return func(list any) any { items := sky_asList(list); result := make([]any, len(items)); for i, item := range items { result[i] = encoder.(func(any) any)(item) }; return result } }"), GoDeclRaw("func sky_jsonEncObject(pairs any) any { m := make(map[string]any); for _, p := range sky_asList(pairs) { t := sky_asTuple2(p); m[sky_asString(t.V0)] = t.V1 }; return m }"), GoDeclRaw("func sky_jsonEncode(indent any) any { return func(value any) any { var b []byte; var err error; n := sky_asInt(indent); if n > 0 { b, err = encoding_json.MarshalIndent(value, \"\", strings.Repeat(\" \", n)) } else { b, err = encoding_json.Marshal(value) }; if err != nil { return \"null\" }; return string(b) } }"), GoDeclRaw("func sky_jsonDecString(decoder any) any { return func(jsonStr any) any { var v any; if err := encoding_json.Unmarshal([]byte(sky_asString(jsonStr)), &v); err != nil { return SkyErr(err.Error()) }; return decoder.(func(any) any)(v) } }"), GoDeclRaw("var sky_jsonDecoder_string = func(v any) any { if s, ok := v.(string); ok { return SkyOk(s) }; return SkyErr(\"expected string\") }"), GoDeclRaw("var sky_jsonDecoder_int = func(v any) any { switch n := v.(type) { case float64: return SkyOk(int(n)); case int: return SkyOk(n) }; return SkyErr(\"expected int\") }"), GoDeclRaw("var sky_jsonDecoder_float = func(v any) any { if f, ok := v.(float64); ok { return SkyOk(f) }; return SkyErr(\"expected float\") }"), GoDeclRaw("var sky_jsonDecoder_bool = func(v any) any { if b, ok := v.(bool); ok { return SkyOk(b) }; return SkyErr(\"expected bool\") }"), GoDeclRaw("func sky_jsonDecField(key any) any { return func(decoder any) any { return func(v any) any { m, ok := v.(map[string]any); if !ok { return SkyErr(\"expected object\") }; val, exists := m[sky_asString(key)]; if !exists { return SkyErr(\"field '\" + sky_asString(key) + \"' not found\") }; return decoder.(func(any) any)(val) } } }"), GoDeclRaw("func sky_jsonDecList(decoder any) any { return func(v any) any { arr, ok := v.([]any); if !ok { return SkyErr(\"expected array\") }; result := make([]any, 0, len(arr)); for _, item := range arr { r := decoder.(func(any) any)(item); res := sky_asSkyResult(r); if res.Tag == 1 { return r }; result = append(result, res.OkValue) }; return SkyOk(result) } }"), GoDeclRaw("func sky_jsonDecMap(fn any) any { return func(decoder any) any { return func(v any) any { r := decoder.(func(any) any)(v); res := sky_asSkyResult(r); if res.Tag == 1 { return r }; return SkyOk(fn.(func(any) any)(res.OkValue)) } } }"), GoDeclRaw("func sky_jsonDecMap2(fn any) any { return func(d1 any) any { return func(d2 any) any { return func(v any) any { r1 := d1.(func(any) any)(v); res1 := sky_asSkyResult(r1); if res1.Tag == 1 { return r1 }; r2 := d2.(func(any) any)(v); res2 := sky_asSkyResult(r2); if res2.Tag == 1 { return r2 }; return SkyOk(fn.(func(any) any)(res1.OkValue).(func(any) any)(res2.OkValue)) } } } }"), GoDeclRaw("func sky_jsonDecMap3(fn any) any { return func(d1 any) any { return func(d2 any) any { return func(d3 any) any { return func(v any) any { r1 := d1.(func(any) any)(v); res1 := sky_asSkyResult(r1); if res1.Tag == 1 { return r1 }; r2 := d2.(func(any) any)(v); res2 := sky_asSkyResult(r2); if res2.Tag == 1 { return r2 }; r3 := d3.(func(any) any)(v); res3 := sky_asSkyResult(r3); if res3.Tag == 1 { return r3 }; return SkyOk(fn.(func(any) any)(res1.OkValue).(func(any) any)(res2.OkValue).(func(any) any)(res3.OkValue)) } } } } }"), GoDeclRaw("func sky_jsonDecMap4(fn any) any { return func(d1 any) any { return func(d2 any) any { return func(d3 any) any { return func(d4 any) any { return func(v any) any { r1 := d1.(func(any) any)(v); res1 := sky_asSkyResult(r1); if res1.Tag == 1 { return r1 }; r2 := d2.(func(any) any)(v); res2 := sky_asSkyResult(r2); if res2.Tag == 1 { return r2 }; r3 := d3.(func(any) any)(v); res3 := sky_asSkyResult(r3); if res3.Tag == 1 { return r3 }; r4 := d4.(func(any) any)(v); res4 := sky_asSkyResult(r4); if res4.Tag == 1 { return r4 }; return SkyOk(fn.(func(any) any)(res1.OkValue).(func(any) any)(res2.OkValue).(func(any) any)(res3.OkValue).(func(any) any)(res4.OkValue)) } } } } } }"), GoDeclRaw("func sky_jsonDecSucceed(v any) any { return func(_ any) any { return SkyOk(v) } }"), GoDeclRaw("func sky_jsonDecFail(msg any) any { return func(_ any) any { return SkyErr(msg) } }"), GoDeclRaw("func sky_jsonDecAndThen(fn any) any { return func(decoder any) any { return func(v any) any { r := decoder.(func(any) any)(v); res := sky_asSkyResult(r); if res.Tag == 1 { return r }; nextDecoder := fn.(func(any) any)(res.OkValue); return nextDecoder.(func(any) any)(v) } } }"), GoDeclRaw("func sky_jsonDecOneOf(decoders any) any { return func(v any) any { for _, d := range sky_asList(decoders) { r := d.(func(any) any)(v); if sky_asSkyResult(r).Tag == 0 { return r } }; return SkyErr(\"none of the decoders matched\") } }"), GoDeclRaw("func sky_jsonDecNullable(decoder any) any { return func(v any) any { if v == nil { return SkyOk(SkyNothing()) }; r := decoder.(func(any) any)(v); res := sky_asSkyResult(r); if res.Tag == 0 { return SkyOk(SkyJust(res.OkValue)) }; return r } }"), GoDeclRaw("func sky_jsonDecAt(path any) any { return func(decoder any) any { return func(v any) any { current := v; for _, key := range sky_asList(path) { m, ok := current.(map[string]any); if !ok { return SkyErr(\"expected object at path\") }; val, exists := m[sky_asString(key)]; if !exists { return SkyErr(\"key not found: \" + sky_asString(key)) }; current = val }; return decoder.(func(any) any)(current) } } }"), GoDeclRaw("func sky_jsonPipeDecode(constructor any) any { return func(v any) any { return SkyOk(constructor) } }"), GoDeclRaw("func sky_jsonPipeRequired(key any) any { return func(decoder any) any { return func(pipeline any) any { return func(v any) any { pr := pipeline.(func(any) any)(v); pres := sky_asSkyResult(pr); if pres.Tag == 1 { return pr }; m, ok := v.(map[string]any); if !ok { return SkyErr(\"expected object\") }; val, exists := m[sky_asString(key)]; if !exists { return SkyErr(\"field '\" + sky_asString(key) + \"' required\") }; fr := decoder.(func(any) any)(val); fres := sky_asSkyResult(fr); if fres.Tag == 1 { return fr }; return SkyOk(pres.OkValue.(func(any) any)(fres.OkValue)) } } } }"), GoDeclRaw("func sky_jsonPipeOptional(key any) any { return func(decoder any) any { return func(def any) any { return func(pipeline any) any { return func(v any) any { pr := pipeline.(func(any) any)(v); pres := sky_asSkyResult(pr); if pres.Tag == 1 { return pr }; m, ok := v.(map[string]any); if !ok { return SkyOk(pres.OkValue.(func(any) any)(def)) }; val, exists := m[sky_asString(key)]; if !exists { return SkyOk(pres.OkValue.(func(any) any)(def)) }; fr := decoder.(func(any) any)(val); fres := sky_asSkyResult(fr); if fres.Tag == 1 { return SkyOk(pres.OkValue.(func(any) any)(def)) }; return SkyOk(pres.OkValue.(func(any) any)(fres.OkValue)) } } } } }"), GoDeclRaw("func sky_cmdNone() any { return []any{} }"), GoDeclRaw("func sky_cmdBatch(cmds any) any { return sky_asList(cmds) }"), GoDeclRaw("func sky_subNone() any { return map[string]any{\"SkyName\": \"SubNone\"} }"), GoDeclRaw("func sky_subBatch(subs any) any { return map[string]any{\"SkyName\": \"SubBatch\", \"V0\": subs} }"), GoDeclRaw("func sky_timeEvery(interval any) any { return func(msg any) any { return map[string]any{\"SkyName\": \"SubTimer\", \"V0\": interval, \"V1\": msg} } }"), GoDeclRaw("func sky_htmlEl(tag any) any { return func(attrs any) any { return func(children any) any { return map[string]any{\"tag\": tag, \"attrs\": attrs, \"children\": children, \"text\": \"\"} } } }"), GoDeclRaw("func sky_htmlVoid(tag any) any { return func(attrs any) any { return map[string]any{\"tag\": tag, \"attrs\": attrs, \"children\": []any{}, \"text\": \"\"} } }"), GoDeclRaw("func sky_htmlText(s any) any { return map[string]any{\"tag\": \"\", \"attrs\": []any{}, \"children\": []any{}, \"text\": sky_asString(s)} }"), GoDeclRaw("func sky_htmlRaw(s any) any { return map[string]any{\"tag\": \"__raw__\", \"attrs\": []any{}, \"children\": []any{}, \"text\": sky_asString(s)} }"), GoDeclRaw("func sky_htmlStyleNode(attrs any) any { return func(css any) any { return map[string]any{\"tag\": \"style\", \"attrs\": attrs, \"children\": []any{map[string]any{\"tag\": \"\", \"attrs\": []any{}, \"children\": []any{}, \"text\": sky_asString(css)}}, \"text\": \"\"} } }"), GoDeclRaw("func sky_htmlRender(vnode any) any { return sky_vnodeToHtml(vnode) }"), GoDeclRaw("func sky_vnodeToHtml(v any) string { m := sky_asMap(v); if m == nil { return \"\" }; tag := sky_asString(m[\"tag\"]); if tag == \"\" { return sky_htmlEscapeStr(sky_asString(m[\"text\"])) }; if tag == \"__raw__\" { return sky_asString(m[\"text\"]) }; attrs := sky_renderAttrs(sky_asList(m[\"attrs\"])); children := sky_asList(m[\"children\"]); if tag == \"input\" || tag == \"br\" || tag == \"hr\" || tag == \"img\" || tag == \"meta\" { return \"<\" + tag + attrs + \" />\" }; var sb strings.Builder; sb.WriteString(\"<\" + tag + attrs + \">\"); for _, c := range children { sb.WriteString(sky_vnodeToHtml(c)) }; sb.WriteString(\"</\" + tag + \">\"); return sb.String() }"), GoDeclRaw("func sky_renderAttrs(attrs []any) string { var sb strings.Builder; for _, a := range attrs { t := sky_asTuple2(a); k := sky_asString(t.V0); v := sky_asString(t.V1); if v != \"\" { sb.WriteString(\" \" + k + \"=\\\"\" + sky_htmlEscapeStr(v) + \"\\\"\") } }; return sb.String() }"), GoDeclRaw("func sky_htmlEscapeStr(s string) string { s = strings.ReplaceAll(s, \"&\", \"&amp;\"); s = strings.ReplaceAll(s, \"<\", \"&lt;\"); s = strings.ReplaceAll(s, \">\", \"&gt;\"); s = strings.ReplaceAll(s, \"\\\"\", \"&quot;\"); return s }"), GoDeclRaw("func sky_htmlEscapeHtml(s any) any { return sky_htmlEscapeStr(sky_asString(s)) }"), GoDeclRaw("func sky_htmlEscapeAttr(s any) any { return sky_htmlEscapeStr(sky_asString(s)) }"), GoDeclRaw("func sky_htmlAttrToString(attr any) any { t := sky_asTuple2(attr); return sky_asString(t.V0) + \"=\\\"\" + sky_htmlEscapeStr(sky_asString(t.V1)) + \"\\\"\" }"), GoDeclRaw("func sky_attrSimple(key any) any { return func(v any) any { return SkyTuple2{sky_asString(key), sky_asString(v)} } }"), GoDeclRaw("func sky_attrCustom(key any) any { return func(v any) any { return SkyTuple2{sky_asString(key), sky_asString(v)} } }"), GoDeclRaw("func sky_attrBool(key any) any { return func(v any) any { if sky_asBool(v) { return SkyTuple2{sky_asString(key), sky_asString(key)} }; return SkyTuple2{sky_asString(key), \"\"} } }"), GoDeclRaw("func sky_attrData(key any) any { return func(val any) any { return SkyTuple2{\"data-\" + sky_asString(key), sky_asString(val)} } }"), GoDeclRaw("func sky_evtHandler(evtType any) any { return func(msg any) any { return SkyTuple2{\"sky-\" + sky_asString(evtType), sky_msgName(msg)} } }"), GoDeclRaw("func sky_msgName(msg any) string { if m, ok := msg.(map[string]any); ok { if name, exists := m[\"SkyName\"]; exists { return sky_asString(name) } }; return fmt.Sprintf(\"%v\", msg) }"), GoDeclRaw("func sky_cssStylesheet(rules any) any { var sb strings.Builder; for _, r := range sky_asList(rules) { sb.WriteString(sky_asString(r)); sb.WriteString(\"\\n\") }; return sb.String() }"), GoDeclRaw("func sky_cssRule(selector any) any { return func(props any) any { var sb strings.Builder; sb.WriteString(sky_asString(selector)); sb.WriteString(\" { \"); for _, p := range sky_asList(props) { sb.WriteString(sky_asString(p)); sb.WriteString(\"; \") }; sb.WriteString(\"}\"); return sb.String() } }"), GoDeclRaw("func sky_cssProp(key any) any { return func(val any) any { return sky_asString(key) + \": \" + sky_asString(val) } }"), GoDeclRaw("func sky_cssPx(n any) any { return fmt.Sprintf(\"%dpx\", sky_asInt(n)) }"), GoDeclRaw("func sky_cssRem(n any) any { return fmt.Sprintf(\"%.2frem\", sky_asFloat(n)) }"), GoDeclRaw("func sky_cssEm(n any) any { return fmt.Sprintf(\"%.2fem\", sky_asFloat(n)) }"), GoDeclRaw("func sky_cssPct(n any) any { return fmt.Sprintf(\"%.0f%%\", sky_asFloat(n)) }"), GoDeclRaw("func sky_cssHex(s any) any { return \"#\" + sky_asString(s) }"), GoDeclRaw("func sky_cssRgb(r any) any { return func(g any) any { return func(b any) any { return fmt.Sprintf(\"rgb(%d, %d, %d)\", sky_asInt(r), sky_asInt(g), sky_asInt(b)) } } }"), GoDeclRaw("func sky_cssStyles(props any) any { var parts []string; for _, p := range sky_asList(props) { parts = append(parts, sky_asString(p)) }; return strings.Join(parts, \"; \") }"), GoDeclRaw("func sky_cssMargin2(v any) any { return func(h any) any { return \"margin: \" + sky_asString(v) + \" \" + sky_asString(h) } }"), GoDeclRaw("func sky_cssPadding2(v any) any { return func(h any) any { return \"padding: \" + sky_asString(v) + \" \" + sky_asString(h) } }"), GoDeclRaw("func sky_cssRgba(r any) any { return func(g any) any { return func(b any) any { return func(a any) any { return fmt.Sprintf(\"rgba(%d, %d, %d, %v)\", sky_asInt(r), sky_asInt(g), sky_asInt(b), sky_asFloat(a)) } } } }"), GoDeclRaw("func sky_cssMedia(query any) any { return func(rules any) any { var sb strings.Builder; sb.WriteString(\"@media \"); sb.WriteString(sky_asString(query)); sb.WriteString(\" { \"); for _, r := range sky_asList(rules) { sb.WriteString(sky_asString(r)); sb.WriteString(\" \") }; sb.WriteString(\"}\"); return sb.String() } }"), GoDeclRaw("func sky_cssKeyframes(name any) any { return func(frames any) any { var sb strings.Builder; sb.WriteString(\"@keyframes \"); sb.WriteString(sky_asString(name)); sb.WriteString(\" { \"); for _, f := range sky_asList(frames) { sb.WriteString(sky_asString(f)); sb.WriteString(\" \") }; sb.WriteString(\"}\"); return sb.String() } }"), GoDeclRaw("func sky_cssFrame(pctVal any) any { return func(props any) any { var sb strings.Builder; sb.WriteString(fmt.Sprintf(\"%v%%\", sky_asFloat(pctVal))); sb.WriteString(\" { \"); for _, p := range sky_asList(props) { sb.WriteString(sky_asString(p)); sb.WriteString(\"; \") }; sb.WriteString(\"}\"); return sb.String() } }"), GoDeclRaw("func sky_cssPropFn(prop any) any { return func(val any) any { return sky_asString(prop) + \": \" + sky_asString(val) } }"), GoDeclRaw("func sky_evt_fileMaxSize(v any) any { return SkyTuple2{\"sky-file-maxsize\", fmt.Sprintf(\"%d\", sky_asInt(v))} }"), GoDeclRaw("func sky_evt_fileMaxWidth(v any) any { return SkyTuple2{\"sky-file-maxwidth\", fmt.Sprintf(\"%d\", sky_asInt(v))} }"), GoDeclRaw("func sky_evt_fileMaxHeight(v any) any { return SkyTuple2{\"sky-file-maxheight\", fmt.Sprintf(\"%d\", sky_asInt(v))} }"), GoDeclRaw("func sky_liveRoute(path any) any { return func(page any) any { return map[string]any{\"path\": path, \"page\": page} } }"), GoDeclRaw("func sky_liveApp(config any) any { return config }")}
}

func Compiler_Lower_CollectLocalFunctions(decls any) any {
	return sky_call(sky_listFilterMap(func(d any) any { return func() any { return func() any { __subject := d; if sky_asMap(__subject)["SkyName"] == "FunDecl" { name := sky_asMap(__subject)["V0"]; _ = name; return SkyJust(name) };  if true { return SkyNothing() };  return nil }() }() }), decls)
}

func Compiler_Lower_CollectLocalFunctionArities(decls any) any {
	return sky_call2(sky_listFoldl(func(d any) any { return func(acc any) any { return func() any { return func() any { __subject := d; if sky_asMap(__subject)["SkyName"] == "FunDecl" { name := sky_asMap(__subject)["V0"]; _ = name; params := sky_asMap(__subject)["V1"]; _ = params; return sky_call2(sky_dictInsert(name), sky_listLength(params), acc) };  if true { return acc };  return nil }() }() } }), sky_dictEmpty(), decls)
}

func Compiler_Lower_BuildConstructorMap(registry any) any {
	return sky_call2(sky_dictFoldl(func(typeName any) any { return func(adt any) any { return func(acc any) any { return func() any { entries := sky_dictToList(sky_asMap(adt)["constructors"]); _ = entries; return Compiler_Lower_AddCtorsFromList(typeName, entries, 0, acc) }() } } }), sky_dictEmpty(), registry)
}

func Compiler_Lower_AddCtorsFromList(typeName any, entries any, idx any, acc any) any {
	return func() any { return func() any { __subject := entries; if len(sky_asList(__subject)) == 0 { return acc };  if len(sky_asList(__subject)) > 0 { entry := sky_asList(__subject)[0]; _ = entry; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { name := sky_fst(entry); _ = name; scheme := sky_snd(entry); _ = scheme; ctorArity := Compiler_Lower_CountFunArgs(sky_asMap(scheme)["type_"]); _ = ctorArity; return sky_call2(sky_dictInsert(name), map[string]any{"adtName": typeName, "tagIndex": idx, "arity": ctorArity}, Compiler_Lower_AddCtorsFromList(typeName, rest, sky_asInt(idx) + sky_asInt(1), acc)) }() };  return nil }() }()
}

func Compiler_Lower_CountFunArgs(t any) any {
	return func() any { return func() any { __subject := t; if sky_asMap(__subject)["SkyName"] == "TFun" { argT := sky_asMap(__subject)["V0"]; _ = argT; toT := sky_asMap(__subject)["V1"]; _ = toT; return sky_asInt(1) + sky_asInt(Compiler_Lower_CountFunArgs(toT)) };  if true { return 0 };  return nil }() }()
}

func Compiler_Lower_SanitizeGoIdent(name any) any {
	return func() any { if sky_asBool(Compiler_Lower_IsGoKeyword(name)) { return sky_concat(name, "_") }; return name }()
}

func Compiler_Lower_IsGoKeyword(name any) any {
	return sky_asBool(sky_equal(name, "go")) || sky_asBool(sky_asBool(sky_equal(name, "type")) || sky_asBool(sky_asBool(sky_equal(name, "func")) || sky_asBool(sky_asBool(sky_equal(name, "var")) || sky_asBool(sky_asBool(sky_equal(name, "return")) || sky_asBool(sky_asBool(sky_equal(name, "if")) || sky_asBool(sky_asBool(sky_equal(name, "else")) || sky_asBool(sky_asBool(sky_equal(name, "for")) || sky_asBool(sky_asBool(sky_equal(name, "range")) || sky_asBool(sky_asBool(sky_equal(name, "switch")) || sky_asBool(sky_asBool(sky_equal(name, "case")) || sky_asBool(sky_asBool(sky_equal(name, "default")) || sky_asBool(sky_asBool(sky_equal(name, "break")) || sky_asBool(sky_asBool(sky_equal(name, "continue")) || sky_asBool(sky_asBool(sky_equal(name, "select")) || sky_asBool(sky_asBool(sky_equal(name, "chan")) || sky_asBool(sky_asBool(sky_equal(name, "map")) || sky_asBool(sky_asBool(sky_equal(name, "struct")) || sky_asBool(sky_asBool(sky_equal(name, "interface")) || sky_asBool(sky_asBool(sky_equal(name, "package")) || sky_asBool(sky_asBool(sky_equal(name, "import")) || sky_asBool(sky_asBool(sky_equal(name, "const")) || sky_asBool(sky_asBool(sky_equal(name, "defer")) || sky_asBool(sky_asBool(sky_equal(name, "fallthrough")) || sky_asBool(sky_asBool(sky_equal(name, "goto")) || sky_asBool(sky_asBool(sky_equal(name, "init")) || sky_asBool(sky_asBool(sky_equal(name, "make")) || sky_asBool(sky_asBool(sky_equal(name, "new")) || sky_asBool(sky_asBool(sky_equal(name, "len")) || sky_asBool(sky_asBool(sky_equal(name, "cap")) || sky_asBool(sky_asBool(sky_equal(name, "append")) || sky_asBool(sky_asBool(sky_equal(name, "copy")) || sky_asBool(sky_asBool(sky_equal(name, "delete")) || sky_asBool(sky_asBool(sky_equal(name, "close")) || sky_asBool(sky_asBool(sky_equal(name, "exec")) || sky_asBool(sky_asBool(sky_equal(name, "error")) || sky_asBool(sky_asBool(sky_equal(name, "string")) || sky_asBool(sky_asBool(sky_equal(name, "int")) || sky_asBool(sky_asBool(sky_equal(name, "float64")) || sky_asBool(sky_asBool(sky_equal(name, "bool")) || sky_asBool(sky_asBool(sky_equal(name, "byte")) || sky_asBool(sky_asBool(sky_equal(name, "rune")) || sky_asBool(sky_asBool(sky_equal(name, "any")) || sky_asBool(sky_asBool(sky_equal(name, "nil")) || sky_asBool(sky_asBool(sky_equal(name, "panic")) || sky_asBool(sky_asBool(sky_equal(name, "recover")) || sky_asBool(sky_asBool(sky_equal(name, "print")) || sky_asBool(sky_asBool(sky_equal(name, "println")) || sky_asBool(sky_asBool(sky_equal(name, "int8")) || sky_asBool(sky_asBool(sky_equal(name, "int16")) || sky_asBool(sky_asBool(sky_equal(name, "int32")) || sky_asBool(sky_asBool(sky_equal(name, "int64")) || sky_asBool(sky_asBool(sky_equal(name, "uint")) || sky_asBool(sky_asBool(sky_equal(name, "uint8")) || sky_asBool(sky_asBool(sky_equal(name, "uint16")) || sky_asBool(sky_asBool(sky_equal(name, "uint32")) || sky_asBool(sky_asBool(sky_equal(name, "uint64")) || sky_asBool(sky_asBool(sky_equal(name, "float32")) || sky_asBool(sky_asBool(sky_equal(name, "complex64")) || sky_asBool(sky_asBool(sky_equal(name, "complex128")) || sky_asBool(sky_equal(name, "uintptr")))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))
}

func Compiler_Lower_IsStdlibCallee(expr any) any {
	return func() any { return func() any { __subject := expr; if sky_asMap(__subject)["SkyName"] == "QualifiedExpr" { parts := sky_asMap(__subject)["V0"]; _ = parts; return func() any { return func() any { __subject := sky_listHead(parts); if sky_asSkyMaybe(__subject).SkyName == "Just" { first := sky_asSkyMaybe(__subject).JustValue; _ = first; return sky_asBool(sky_equal(first, "String")) || sky_asBool(sky_asBool(sky_equal(first, "List")) || sky_asBool(sky_asBool(sky_equal(first, "Dict")) || sky_asBool(sky_asBool(sky_equal(first, "Set")) || sky_asBool(sky_asBool(sky_equal(first, "File")) || sky_asBool(sky_asBool(sky_equal(first, "Process")) || sky_asBool(sky_asBool(sky_equal(first, "Ref")) || sky_asBool(sky_asBool(sky_equal(first, "Io")) || sky_asBool(sky_asBool(sky_equal(first, "Args")) || sky_asBool(sky_asBool(sky_equal(first, "Log")) || sky_asBool(sky_asBool(sky_equal(first, "Task")) || sky_asBool(sky_asBool(sky_equal(first, "Server")) || sky_asBool(sky_asBool(sky_equal(first, "Result")) || sky_asBool(sky_asBool(sky_equal(first, "Maybe")) || sky_asBool(sky_asBool(sky_equal(first, "Math")) || sky_asBool(sky_asBool(sky_equal(first, "Crypto")) || sky_asBool(sky_asBool(sky_equal(first, "Encoding")) || sky_asBool(sky_asBool(sky_equal(first, "Time")) || sky_asBool(sky_asBool(sky_equal(first, "Http")) || sky_asBool(sky_asBool(sky_equal(first, "Encode")) || sky_asBool(sky_asBool(sky_equal(first, "Decode")) || sky_asBool(sky_asBool(sky_equal(first, "Pipeline")) || sky_asBool(sky_asBool(sky_equal(first, "Cmd")) || sky_asBool(sky_asBool(sky_equal(first, "Sub")) || sky_asBool(sky_asBool(sky_equal(first, "Html")) || sky_asBool(sky_asBool(sky_equal(first, "Attr")) || sky_asBool(sky_asBool(sky_equal(first, "Events")) || sky_asBool(sky_asBool(sky_equal(first, "Css")) || sky_asBool(sky_equal(first, "Live"))))))))))))))))))))))))))))) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return false };  if sky_asMap(__subject)["SkyName"] == "IdentifierExpr" { name := sky_asMap(__subject)["V0"]; _ = name; return sky_call(sky_stringStartsWith("sky_"), name) };  if sky_asMap(__subject)["SkyName"] == "CallExpr" { innerCallee := sky_asMap(__subject)["V0"]; _ = innerCallee; return Compiler_Lower_IsStdlibCallee(innerCallee) };  if true { return false };  return nil }() }() };  return nil }() }()
}

func Compiler_Lower_MakeTupleKey(items any) any {
	return sky_call(sky_stringJoin("_"), sky_call(sky_listFilterMap(Compiler_Lower_GetPatVarName), items))
}

func Compiler_Lower_GetPatVarName(pat any) any {
	return func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PVariable" { name := sky_asMap(__subject)["V0"]; _ = name; return SkyJust(name) };  if sky_asMap(__subject)["SkyName"] == "PWildcard" { return SkyJust("w") };  if true { return SkyJust("x") };  return nil }() }()
}

func Compiler_Lower_IsParamOrBuiltin(name any) any {
	return sky_asBool(sky_asInt(sky_stringLength(name)) <= sky_asInt(2)) || sky_asBool(sky_asBool(sky_equal(name, "acc")) || sky_asBool(sky_asBool(sky_equal(name, "rest")) || sky_asBool(sky_asBool(sky_equal(name, "state")) || sky_asBool(sky_asBool(sky_equal(name, "env")) || sky_asBool(sky_asBool(sky_equal(name, "ctx")) || sky_asBool(sky_asBool(sky_equal(name, "mod")) || sky_asBool(sky_asBool(sky_equal(name, "decl")) || sky_asBool(sky_asBool(sky_equal(name, "decls")) || sky_asBool(sky_asBool(sky_equal(name, "items")) || sky_asBool(sky_asBool(sky_equal(name, "fields")) || sky_asBool(sky_asBool(sky_equal(name, "args")) || sky_asBool(sky_asBool(sky_equal(name, "params")) || sky_asBool(sky_asBool(sky_equal(name, "body")) || sky_asBool(sky_asBool(sky_equal(name, "name")) || sky_asBool(sky_asBool(sky_equal(name, "result")) || sky_asBool(sky_asBool(sky_equal(name, "pair")) || sky_asBool(sky_asBool(sky_equal(name, "counter")) || sky_asBool(sky_asBool(sky_equal(name, "registry")) || sky_asBool(sky_asBool(sky_equal(name, "sub")) || sky_asBool(sky_asBool(sky_equal(name, "pat")) || sky_asBool(sky_asBool(sky_equal(name, "left")) || sky_asBool(sky_asBool(sky_equal(name, "right")) || sky_asBool(sky_asBool(sky_equal(name, "inner")) || sky_asBool(sky_asBool(sky_equal(name, "source")) || sky_asBool(sky_asBool(sky_equal(name, "code")) || sky_asBool(sky_asBool(sky_equal(name, "token")) || sky_asBool(sky_asBool(sky_equal(name, "tokens")) || sky_asBool(sky_asBool(sky_equal(name, "parts")) || sky_asBool(sky_asBool(sky_equal(name, "prefix")) || sky_asBool(sky_asBool(sky_equal(name, "msg")) || sky_asBool(sky_asBool(sky_equal(name, "key")) || sky_asBool(sky_asBool(sky_equal(name, "val")) || sky_asBool(sky_asBool(sky_equal(name, "value")) || sky_asBool(sky_asBool(sky_equal(name, "expr")) || sky_asBool(sky_asBool(sky_equal(name, "span")) || sky_asBool(sky_asBool(sky_equal(name, "type_")) || sky_asBool(sky_asBool(sky_equal(name, "scheme")) || sky_asBool(sky_asBool(sky_equal(name, "entry")) || sky_asBool(sky_asBool(sky_equal(name, "binding")) || sky_asBool(sky_asBool(sky_equal(name, "branch")) || sky_asBool(sky_asBool(sky_equal(name, "variant")) || sky_asBool(sky_asBool(sky_equal(name, "modName")) || sky_asBool(sky_asBool(sky_equal(name, "filePath")) || sky_asBool(sky_asBool(sky_equal(name, "outDir")) || sky_asBool(sky_asBool(sky_equal(name, "goCode")) || sky_asBool(sky_asBool(sky_equal(name, "goPackage")) || sky_asBool(sky_asBool(sky_equal(name, "goDecls")) || sky_asBool(sky_asBool(sky_equal(name, "goArgs")) || sky_asBool(sky_asBool(sky_equal(name, "goCallee")) || sky_asBool(sky_asBool(sky_equal(name, "goName")) || sky_asBool(sky_asBool(sky_equal(name, "goMod")) || sky_asBool(sky_asBool(sky_equal(name, "funcPart")) || sky_asBool(sky_asBool(sky_equal(name, "modPart")) || sky_asBool(sky_asBool(sky_equal(name, "qualName")) || sky_asBool(sky_asBool(sky_equal(name, "aliasMap")) || sky_asBool(sky_asBool(sky_equal(name, "stdlibEnv")) || sky_asBool(sky_asBool(sky_equal(name, "depDecls")) || sky_asBool(sky_asBool(sky_equal(name, "entryMod")) || sky_asBool(sky_asBool(sky_equal(name, "loadedModules")) || sky_asBool(sky_asBool(sky_equal(name, "entryRegistry")) || sky_asBool(sky_asBool(sky_equal(name, "entryCtx")) || sky_asBool(sky_asBool(sky_equal(name, "baseCtx")) || sky_asBool(sky_asBool(sky_equal(name, "entryGoDecls")) || sky_asBool(sky_asBool(sky_equal(name, "entryCtorDecls")) || sky_asBool(sky_asBool(sky_equal(name, "helperDecls")) || sky_asBool(sky_asBool(sky_equal(name, "allDecls")) || sky_asBool(sky_asBool(sky_equal(name, "rawGoCode")) || sky_asBool(sky_asBool(sky_equal(name, "outPath")) || sky_asBool(sky_asBool(sky_equal(name, "localImports")) || sky_asBool(sky_asBool(sky_equal(name, "depBaseCtx")) || sky_asBool(sky_asBool(sky_equal(name, "depAliasMap")) || sky_asBool(sky_asBool(sky_equal(name, "checkResult")) || sky_asBool(sky_asBool(sky_equal(name, "lexResult")) || sky_asBool(sky_asBool(sky_equal(name, "filtered")) || sky_asBool(sky_asBool(sky_equal(name, "prefixed")) || sky_asBool(sky_asBool(sky_equal(name, "aliases")) || sky_asBool(sky_asBool(sky_equal(name, "goType")) || sky_asBool(sky_asBool(sky_equal(name, "impMap")) || sky_asBool(sky_asBool(sky_equal(name, "imp")) || sky_asBool(sky_asBool(sky_equal(name, "imports")) || sky_asBool(sky_asBool(sky_equal(name, "header")) || sky_asBool(sky_asBool(sky_equal(name, "opToken")) || sky_asBool(sky_asBool(sky_equal(name, "info")) || sky_asBool(sky_asBool(sky_equal(name, "prec")) || sky_asBool(sky_asBool(sky_equal(name, "assoc")) || sky_asBool(sky_asBool(sky_equal(name, "nextMin")) || sky_asBool(sky_asBool(sky_equal(name, "condition")) || sky_asBool(sky_asBool(sky_equal(name, "thenBranch")) || sky_asBool(sky_asBool(sky_equal(name, "elseBranch")) || sky_asBool(sky_asBool(sky_equal(name, "subject")) || sky_asBool(sky_asBool(sky_equal(name, "bindings")) || sky_asBool(sky_asBool(sky_equal(name, "callee")) || sky_asBool(sky_asBool(sky_equal(name, "fn")) || sky_asBool(sky_asBool(sky_equal(name, "remaining")) || sky_asBool(sky_asBool(sky_equal(name, "idx")) || sky_asBool(sky_asBool(sky_equal(name, "ch")) || sky_asBool(sky_asBool(sky_equal(name, "str")) || sky_asBool(sky_asBool(sky_equal(name, "count")) || sky_asBool(sky_asBool(sky_equal(name, "len")) || sky_asBool(sky_asBool(sky_equal(name, "start")) || sky_asBool(sky_asBool(sky_equal(name, "end")) || sky_asBool(sky_asBool(sky_equal(name, "line")) || sky_asBool(sky_asBool(sky_equal(name, "character")) || sky_asBool(sky_asBool(sky_equal(name, "position")) || sky_asBool(sky_asBool(sky_equal(name, "textDoc")) || sky_asBool(sky_asBool(sky_equal(name, "uri")) || sky_asBool(sky_asBool(sky_equal(name, "text")) || sky_asBool(sky_asBool(sky_equal(name, "content")) || sky_asBool(sky_asBool(sky_equal(name, "json")) || sky_asBool(sky_asBool(sky_equal(name, "varName")) || sky_asBool(sky_asBool(sky_equal(name, "pattern")) || sky_asBool(sky_asBool(sky_equal(name, "arity")) || sky_asBool(sky_asBool(sky_equal(name, "closure")) || sky_asBool(sky_asBool(sky_equal(name, "separator")) || sky_asBool(sky_asBool(sky_equal(name, "list")) || sky_asBool(sky_asBool(sky_equal(name, "item")) || sky_asBool(sky_asBool(sky_equal(name, "elem")) || sky_asBool(sky_asBool(sky_equal(name, "init")) || sky_asBool(sky_asBool(sky_equal(name, "doc")) || sky_asBool(sky_asBool(sky_equal(name, "indent")) || sky_asBool(sky_asBool(sky_equal(name, "width")) || sky_asBool(sky_asBool(sky_equal(name, "wrapperResult")) || sky_asBool(sky_asBool(sky_equal(name, "skyiContent")) || sky_asBool(sky_asBool(sky_equal(name, "pkgName")) || sky_asBool(sky_asBool(sky_equal(name, "safePkg")) || sky_asBool(sky_asBool(sky_equal(name, "cacheDir")) || sky_asBool(sky_asBool(sky_equal(name, "cachePath")) || sky_asBool(sky_asBool(sky_equal(name, "inspectorDir")) || sky_asBool(sky_asBool(sky_equal(name, "buildResult")) || sky_asBool(sky_asBool(sky_equal(name, "output")) || sky_asBool(sky_asBool(sky_equal(name, "goModContent")) || sky_asBool(sky_asBool(sky_equal(name, "mainPath")) || sky_asBool(sky_asBool(sky_equal(name, "runtimeCode")) || sky_asBool(sky_asBool(sky_equal(name, "runtimeDir")) || sky_asBool(sky_asBool(sky_equal(name, "inspectJson")) || sky_asBool(sky_asBool(sky_equal(name, "wrapperCode")) || sky_asBool(sky_asBool(sky_equal(name, "wrapperDir")) || sky_asBool(sky_asBool(sky_equal(name, "moduleName")) || sky_asBool(sky_asBool(sky_equal(name, "funcName")) || sky_asBool(sky_asBool(sky_equal(name, "funcInfo")) || sky_asBool(sky_asBool(sky_equal(name, "methodInfo")) || sky_asBool(sky_asBool(sky_equal(name, "receiverCast")) || sky_asBool(sky_asBool(sky_equal(name, "argCasts")) || sky_asBool(sky_asBool(sky_equal(name, "castStr")) || sky_asBool(sky_asBool(sky_equal(name, "argNames")) || sky_asBool(sky_asBool(sky_equal(name, "goCall")) || sky_asBool(sky_asBool(sky_equal(name, "returnCode")) || sky_asBool(sky_asBool(sky_equal(name, "wrapperName")) || sky_asBool(sky_asBool(sky_equal(name, "paramList")) || sky_asBool(sky_asBool(sky_equal(name, "paramStr")) || sky_asBool(sky_asBool(sky_equal(name, "assertion")) || sky_asBool(sky_asBool(sky_equal(name, "entryFile")) || sky_asBool(sky_asBool(sky_equal(name, "command")) || sky_asBool(sky_asBool(sky_equal(name, "formatted")) || sky_asBool(sky_asBool(sky_equal(name, "readErr")) || sky_asBool(sky_asBool(sky_equal(name, "parseErr")) || sky_asBool(sky_asBool(sky_equal(name, "srcRoot")) || sky_asBool(sky_asBool(sky_equal(name, "hasLocalImports")) || sky_asBool(sky_asBool(sky_equal(name, "error")) || sky_asBool(sky_asBool(sky_equal(name, "diagnostics")) || sky_asBool(sky_asBool(sky_equal(name, "severity")) || sky_asBool(sky_asBool(sky_equal(name, "annotation")) || sky_asBool(sky_asBool(sky_equal(name, "bodyResult")) || sky_asBool(sky_asBool(sky_equal(name, "condResult")) || sky_asBool(sky_asBool(sky_equal(name, "leftResult")) || sky_asBool(sky_asBool(sky_equal(name, "rightResult")) || sky_asBool(sky_asBool(sky_equal(name, "patResult")) || sky_asBool(sky_asBool(sky_equal(name, "bodyType")) || sky_asBool(sky_asBool(sky_equal(name, "funType")) || sky_asBool(sky_asBool(sky_equal(name, "selfType")) || sky_asBool(sky_asBool(sky_equal(name, "selfVar")) || sky_asBool(sky_asBool(sky_equal(name, "paramVars")) || sky_asBool(sky_asBool(sky_equal(name, "bindResult")) || sky_asBool(sky_asBool(sky_equal(name, "paramSub")) || sky_asBool(sky_asBool(sky_equal(name, "envWithSelf")) || sky_asBool(sky_asBool(sky_equal(name, "resolvedParamTypes")) || sky_asBool(sky_asBool(sky_equal(name, "bodySub")) || sky_asBool(sky_asBool(sky_equal(name, "finalSub")) || sky_asBool(sky_asBool(sky_equal(name, "finalType")) || sky_asBool(sky_asBool(sky_equal(name, "scheme")) || sky_asBool(sky_asBool(sky_equal(name, "typed")) || sky_asBool(sky_asBool(sky_equal(name, "newEnv")) || sky_asBool(sky_asBool(sky_equal(name, "annotations")) || sky_asBool(sky_asBool(sky_equal(name, "inferDiags")) || sky_asBool(sky_asBool(sky_equal(name, "exhaustDiags")) || sky_asBool(sky_asBool(sky_equal(name, "adtDiags")) || sky_asBool(sky_asBool(sky_equal(name, "aliasEnv")) || sky_asBool(sky_asBool(sky_equal(name, "adtEnv")) || sky_asBool(sky_asBool(sky_equal(name, "env0")) || sky_asBool(sky_asBool(sky_equal(name, "env1")) || sky_asBool(sky_asBool(sky_equal(name, "newRegistry")) || sky_asBool(sky_asBool(sky_equal(name, "newDiags")) || sky_asBool(sky_asBool(sky_equal(name, "ctorSchemes")) || sky_asBool(sky_asBool(sky_equal(name, "adt")) || sky_asBool(sky_asBool(sky_equal(name, "paramMap")) || sky_asBool(sky_asBool(sky_equal(name, "resultType")) || sky_asBool(sky_asBool(sky_equal(name, "fieldTypes")) || sky_asBool(sky_asBool(sky_equal(name, "ctorType")) || sky_asBool(sky_asBool(sky_equal(name, "quantified")) || sky_asBool(sky_asBool(sky_equal(name, "elemVar")) || sky_asBool(sky_asBool(sky_equal(name, "listType")) || sky_asBool(sky_asBool(sky_equal(name, "elemType")) || sky_asBool(sky_asBool(sky_equal(name, "headPat")) || sky_asBool(sky_asBool(sky_equal(name, "tailPat")) || sky_asBool(sky_asBool(sky_equal(name, "headResult")) || sky_asBool(sky_asBool(sky_equal(name, "tailResult")) || sky_asBool(sky_asBool(sky_equal(name, "innerPat")) || sky_asBool(sky_asBool(sky_equal(name, "argPats")) || sky_asBool(sky_asBool(sky_equal(name, "argTypes")) || sky_asBool(sky_asBool(sky_equal(name, "ctorName")) || sky_asBool(sky_asBool(sky_equal(name, "instType")) || sky_asBool(sky_asBool(sky_equal(name, "splitResult")) || sky_asBool(sky_asBool(sky_equal(name, "subjectType")) || sky_asBool(sky_asBool(sky_equal(name, "patterns")) || sky_asBool(sky_asBool(sky_equal(name, "covered")) || sky_asBool(sky_asBool(sky_equal(name, "missing")) || sky_asBool(sky_asBool(sky_equal(name, "allCtors")) || sky_asBool(sky_asBool(sky_equal(name, "coveredCtors")) || sky_asBool(sky_asBool(sky_equal(name, "missingList")) || sky_asBool(sky_asBool(sky_equal(name, "headerLine")) || sky_asBool(sky_asBool(sky_equal(name, "contentLength")) || sky_asBool(sky_asBool(sky_equal(name, "blankLine")) || sky_asBool(sky_asBool(sky_equal(name, "searchKey")) || sky_asBool(sky_asBool(sky_equal(name, "keyIdx")) || sky_asBool(sky_asBool(sky_equal(name, "afterKey")) || sky_asBool(sky_asBool(sky_equal(name, "colonIdx")) || sky_asBool(sky_asBool(sky_equal(name, "afterColon")) || sky_asBool(sky_asBool(sky_equal(name, "numStr")) || sky_asBool(sky_equal(name, "raw"))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))
}

func Compiler_Lower_LowerArgExpr(ctx any, expr any) any {
	return func() any { return func() any { __subject := expr; if sky_asMap(__subject)["SkyName"] == "IdentifierExpr" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { if sky_asBool(sky_asBool(sky_not(sky_stringIsEmpty(sky_asMap(ctx)["modulePrefix"]))) && sky_asBool(Compiler_Lower_ListContains(name, sky_asMap(ctx)["localFunctions"]))) { return func() any { goName := sky_concat(sky_asMap(ctx)["modulePrefix"], sky_concat("_", Compiler_Lower_CapitalizeFirst(Compiler_Lower_SanitizeGoIdent(name)))); _ = goName; fnArity := Compiler_Lower_GetFnArity(name, ctx); _ = fnArity; return func() any { if sky_asBool(sky_asInt(fnArity) > sky_asInt(1)) { return Compiler_Lower_MakeCurryWrapper(goName, fnArity) }; return Compiler_Lower_LowerExpr(ctx, expr) }() }() }; return Compiler_Lower_LowerExpr(ctx, expr) }() };  if true { return Compiler_Lower_LowerExpr(ctx, expr) };  return nil }() }()
}

func Compiler_Lower_IsZeroArityFn(name any, ctx any) any {
	return func() any { return func() any { __subject := sky_call(sky_dictGet(name), sky_asMap(ctx)["localFunctionArity"]); if sky_asSkyMaybe(__subject).SkyName == "Just" { arity := sky_asSkyMaybe(__subject).JustValue; _ = arity; return sky_equal(arity, 0) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return false };  return nil }() }()
}

func Compiler_Lower_GetFnArity(name any, ctx any) any {
	return func() any { return func() any { __subject := sky_call(sky_dictGet(name), sky_asMap(ctx)["localFunctionArity"]); if sky_asSkyMaybe(__subject).SkyName == "Just" { arity := sky_asSkyMaybe(__subject).JustValue; _ = arity; return arity };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return 1 };  return nil }() }()
}

func Compiler_Lower_MakeCurryWrapper(goName any, arity any) any {
	return func() any { if sky_asBool(sky_equal(arity, 2)) { return GoRawExpr(sky_concat("func(__ca0 any) any { return func(__ca1 any) any { return ", sky_concat(goName, "(__ca0, __ca1) } }"))) }; return func() any { if sky_asBool(sky_equal(arity, 3)) { return GoRawExpr(sky_concat("func(__ca0 any) any { return func(__ca1 any) any { return func(__ca2 any) any { return ", sky_concat(goName, "(__ca0, __ca1, __ca2) } } }"))) }; return func() any { if sky_asBool(sky_equal(arity, 4)) { return GoRawExpr(sky_concat("func(__ca0 any) any { return func(__ca1 any) any { return func(__ca2 any) any { return func(__ca3 any) any { return ", sky_concat(goName, "(__ca0, __ca1, __ca2, __ca3) } } } }"))) }; return GoIdent(goName) }() }() }()
}

func Compiler_Lower_ListContains(needle any, haystack any) any {
	return sky_call2(sky_listFoldl(func(item any) any { return func(acc any) any { return func() any { if sky_asBool(acc) { return true }; return sky_equal(item, needle) }() } }), false, haystack)
}

func Compiler_Lower_IsLocalFn(name any, fns any) any {
	return func() any { return func() any { __subject := sky_call(sky_dictGet(name), fns); if sky_asSkyMaybe(__subject).SkyName == "Just" { return true };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return false };  return nil }() }()
}

func Compiler_Lower_IsLocalFunction(name any, ctx any) any {
	return func() any { return func() any { __subject := sky_call(sky_dictGet(name), sky_asMap(ctx)["localFunctionArity"]); if sky_asSkyMaybe(__subject).SkyName == "Just" { return true };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return false };  return nil }() }()
}

func Compiler_Lower_GoQuote(s any) any {
	return func() any { escaped := sky_call2(sky_stringReplace("\""), "\\\"", sky_call2(sky_stringReplace("\\"), "\\\\", s)); _ = escaped; return sky_concat("\"", sky_concat(escaped, "\"")) }()
}

func Compiler_Lower_CapitalizeFirst(s any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(s)) { return "" }; return sky_concat(sky_stringToUpper(sky_call2(sky_stringSlice(0), 1, s)), sky_call2(sky_stringSlice(1), sky_stringLength(s), s)) }()
}

func Compiler_Lower_LastPartOf(parts any) any {
	return func() any { return func() any { __subject := sky_listReverse(parts); if len(sky_asList(__subject)) > 0 { last := sky_asList(__subject)[0]; _ = last; return last };  if len(sky_asList(__subject)) == 0 { return "" };  return nil }() }()
}

func Compiler_Lower_ListGet(idx any, items any) any {
	return func() any { return func() any { __subject := sky_listHead(sky_call(sky_listDrop(idx), items)); if sky_asSkyMaybe(__subject).SkyName == "Just" { x := sky_asSkyMaybe(__subject).JustValue; _ = x; return x };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return Compiler_Lower_ListGet(0, items) };  return nil }() }()
}

func Compiler_Lower_ZipIndex(items any) any {
	return Compiler_Lower_ZipIndexLoop(0, items)
}

func Compiler_Lower_ZipIndexLoop(idx any, items any) any {
	return func() any { return func() any { __subject := items; if len(sky_asList(__subject)) == 0 { return []any{} };  if len(sky_asList(__subject)) > 0 { x := sky_asList(__subject)[0]; _ = x; rest := sky_asList(__subject)[1:]; _ = rest; return append([]any{SkyTuple2{V0: idx, V1: x}}, sky_asList(Compiler_Lower_ZipIndexLoop(sky_asInt(idx) + sky_asInt(1), rest))...) };  return nil }() }()
}

func Compiler_Lower_EmitGoExprInline(expr any) any {
	return func() any { return func() any { __subject := expr; if sky_asMap(__subject)["SkyName"] == "GoIdent" { name := sky_asMap(__subject)["V0"]; _ = name; return name };  if sky_asMap(__subject)["SkyName"] == "GoBasicLit" { val := sky_asMap(__subject)["V0"]; _ = val; return val };  if sky_asMap(__subject)["SkyName"] == "GoStringLit" { s := sky_asMap(__subject)["V0"]; _ = s; return sky_concat("\"", sky_concat(s, "\"")) };  if sky_asMap(__subject)["SkyName"] == "GoCallExpr" { fn := sky_asMap(__subject)["V0"]; _ = fn; args := sky_asMap(__subject)["V1"]; _ = args; return func() any { fnStr := Compiler_Lower_EmitGoExprInline(fn); _ = fnStr; calleeStr := func() any { if sky_asBool(sky_call(sky_stringEndsWith(")"), fnStr)) { return sky_concat(fnStr, ".(func(any) any)") }; return fnStr }(); _ = calleeStr; return sky_concat(calleeStr, sky_concat("(", sky_concat(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Lower_EmitGoExprInline), args)), ")"))) }() };  if sky_asMap(__subject)["SkyName"] == "GoSelectorExpr" { target := sky_asMap(__subject)["V0"]; _ = target; sel := sky_asMap(__subject)["V1"]; _ = sel; return sky_concat(Compiler_Lower_EmitGoExprInline(target), sky_concat(".", sel)) };  if sky_asMap(__subject)["SkyName"] == "GoSliceLit" { items := sky_asMap(__subject)["V0"]; _ = items; return sky_concat("[]any{", sky_concat(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Lower_EmitGoExprInline), items)), "}")) };  if sky_asMap(__subject)["SkyName"] == "GoMapLit" { entries := sky_asMap(__subject)["V0"]; _ = entries; return sky_concat("map[string]any{", sky_concat(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Lower_EmitMapEntry), entries)), "}")) };  if sky_asMap(__subject)["SkyName"] == "GoFuncLit" { params := sky_asMap(__subject)["V0"]; _ = params; body := sky_asMap(__subject)["V1"]; _ = body; return sky_concat("func(", sky_concat(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Lower_EmitInlineParam), params)), sky_concat(") any { return ", sky_concat(Compiler_Lower_EmitGoExprInline(body), " }")))) };  if sky_asMap(__subject)["SkyName"] == "GoRawExpr" { code := sky_asMap(__subject)["V0"]; _ = code; return code };  if sky_asMap(__subject)["SkyName"] == "GoCompositeLit" { typeName := sky_asMap(__subject)["V0"]; _ = typeName; fields := sky_asMap(__subject)["V1"]; _ = fields; return sky_concat(typeName, sky_concat("{", sky_concat(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(func(pair any) any { return sky_concat(sky_fst(pair), sky_concat(": ", Compiler_Lower_EmitGoExprInline(sky_snd(pair)))) }), fields)), "}"))) };  if sky_asMap(__subject)["SkyName"] == "GoBinaryExpr" { op := sky_asMap(__subject)["V0"]; _ = op; left := sky_asMap(__subject)["V1"]; _ = left; right := sky_asMap(__subject)["V2"]; _ = right; return sky_concat(Compiler_Lower_EmitGoExprInline(left), sky_concat(" ", sky_concat(op, sky_concat(" ", Compiler_Lower_EmitGoExprInline(right))))) };  if sky_asMap(__subject)["SkyName"] == "GoUnaryExpr" { op := sky_asMap(__subject)["V0"]; _ = op; operand := sky_asMap(__subject)["V1"]; _ = operand; return sky_concat(op, Compiler_Lower_EmitGoExprInline(operand)) };  if sky_asMap(__subject)["SkyName"] == "GoIndexExpr" { target := sky_asMap(__subject)["V0"]; _ = target; index := sky_asMap(__subject)["V1"]; _ = index; return sky_concat(Compiler_Lower_EmitGoExprInline(target), sky_concat("[", sky_concat(Compiler_Lower_EmitGoExprInline(index), "]"))) };  if sky_asMap(__subject)["SkyName"] == "GoNilExpr" { return "nil" };  return nil }() }()
}

func Compiler_Lower_EmitMapEntry(pair any) any {
	return func() any { key := sky_fst(pair); _ = key; val := sky_snd(pair); _ = val; return sky_concat(Compiler_Lower_EmitGoExprInline(key), sky_concat(": ", Compiler_Lower_EmitGoExprInline(val))) }()
}

func Compiler_Lower_EmitInlineParam(p any) any {
	return sky_concat(sky_asMap(p)["name"], sky_concat(" ", sky_asMap(p)["type_"]))
}

func Compiler_Lower_ExprToGoString(ctx any, expr any) any {
	return Compiler_Lower_EmitGoExprInline(Compiler_Lower_LowerExpr(ctx, expr))
}

func Compiler_Lower_LowerExprToStmts(ctx any, expr any) any {
	return []any{GoExprStmt(Compiler_Lower_LowerExpr(ctx, expr))}
}

func Compiler_Lower_StmtsToGoString(stmts any) any {
	return func() any { raw := sky_call(sky_stringJoin("; "), sky_call(sky_listMap(Compiler_Lower_StmtToGoString), stmts)); _ = raw; return Compiler_Lower_FixCurriedCalls(raw) }()
}

func Compiler_Lower_FixCurriedCalls(code any) any {
	return code
}

func Compiler_Lower_StmtToGoString(stmt any) any {
	return func() any { return func() any { __subject := stmt; if sky_asMap(__subject)["SkyName"] == "GoExprStmt" { expr := sky_asMap(__subject)["V0"]; _ = expr; return Compiler_Lower_EmitGoExprInline(expr) };  if sky_asMap(__subject)["SkyName"] == "GoAssign" { name := sky_asMap(__subject)["V0"]; _ = name; expr := sky_asMap(__subject)["V1"]; _ = expr; return sky_concat(name, sky_concat(" = ", Compiler_Lower_EmitGoExprInline(expr))) };  if sky_asMap(__subject)["SkyName"] == "GoShortDecl" { name := sky_asMap(__subject)["V0"]; _ = name; expr := sky_asMap(__subject)["V1"]; _ = expr; return sky_concat(name, sky_concat(" := ", Compiler_Lower_EmitGoExprInline(expr))) };  if sky_asMap(__subject)["SkyName"] == "GoReturn" { expr := sky_asMap(__subject)["V0"]; _ = expr; return sky_concat("return ", Compiler_Lower_EmitGoExprInline(expr)) };  if sky_asMap(__subject)["SkyName"] == "GoReturnVoid" { return "return" };  if sky_asMap(__subject)["SkyName"] == "GoIf" { cond := sky_asMap(__subject)["V0"]; _ = cond; thenBody := sky_asMap(__subject)["V1"]; _ = thenBody; elseBody := sky_asMap(__subject)["V2"]; _ = elseBody; return sky_concat("if ", sky_concat(Compiler_Lower_EmitGoExprInline(cond), sky_concat(" { ", sky_concat(Compiler_Lower_StmtsToGoString(thenBody), sky_concat(" } else { ", sky_concat(Compiler_Lower_StmtsToGoString(elseBody), " }")))))) };  if sky_asMap(__subject)["SkyName"] == "GoBlock" { body := sky_asMap(__subject)["V0"]; _ = body; return Compiler_Lower_StmtsToGoString(body) };  return nil }() }()
}

var CheckExhaustiveness = Compiler_Exhaustive_CheckExhaustiveness

var HasCatchAll = Compiler_Exhaustive_HasCatchAll

var IsCatchAll = Compiler_Exhaustive_IsCatchAll

var CheckTypeExhaustiveness = Compiler_Exhaustive_CheckTypeExhaustiveness

var CheckBoolExhaustiveness = Compiler_Exhaustive_CheckBoolExhaustiveness

var CollectBoolPatterns = Compiler_Exhaustive_CollectBoolPatterns

var CheckAdtExhaustiveness = Compiler_Exhaustive_CheckAdtExhaustiveness

var CollectConstructorPatterns = Compiler_Exhaustive_CollectConstructorPatterns

var LastPart = Compiler_Exhaustive_LastPart

func Compiler_Exhaustive_CheckExhaustiveness(registry any, subjectType any, branches any) any {
	return func() any { patterns := sky_call(sky_listMap(func(b any) any { return sky_asMap(b)["pattern"] }), branches); _ = patterns; return func() any { if sky_asBool(Compiler_Exhaustive_HasCatchAll(patterns)) { return SkyNothing() }; return Compiler_Exhaustive_CheckTypeExhaustiveness(registry, subjectType, patterns) }() }()
}

func Compiler_Exhaustive_HasCatchAll(patterns any) any {
	return func() any { return func() any { __subject := patterns; if len(sky_asList(__subject)) == 0 { return false };  if len(sky_asList(__subject)) > 0 { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { if sky_asBool(Compiler_Exhaustive_IsCatchAll(pat)) { return true }; return Compiler_Exhaustive_HasCatchAll(rest) }() };  return nil }() }()
}

func Compiler_Exhaustive_IsCatchAll(pat any) any {
	return func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PWildcard" { return true };  if sky_asMap(__subject)["SkyName"] == "PVariable" { return true };  if sky_asMap(__subject)["SkyName"] == "PAs" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Compiler_Exhaustive_IsCatchAll(inner) };  if true { return false };  return nil }() }()
}

func Compiler_Exhaustive_CheckTypeExhaustiveness(registry any, subjectType any, patterns any) any {
	return func() any { return func() any { __subject := subjectType; if sky_asMap(__subject)["SkyName"] == "TConst" { typeName := sky_asMap(__subject)["V0"]; _ = typeName; return func() any { if sky_asBool(sky_equal(typeName, "Bool")) { return Compiler_Exhaustive_CheckBoolExhaustiveness(patterns) }; return func() any { return func() any { __subject := sky_call(sky_dictGet(typeName), registry); if sky_asSkyMaybe(__subject).SkyName == "Just" { adt := sky_asSkyMaybe(__subject).JustValue; _ = adt; return Compiler_Exhaustive_CheckAdtExhaustiveness(adt, patterns) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyNothing() };  if sky_asMap(__subject)["SkyName"] == "TApp" { typeName := sky_asMap(sky_asMap(__subject)["V0"])["V0"]; _ = typeName; return func() any { return func() any { __subject := sky_call(sky_dictGet(typeName), registry); if sky_asSkyMaybe(__subject).SkyName == "Just" { adt := sky_asSkyMaybe(__subject).JustValue; _ = adt; return Compiler_Exhaustive_CheckAdtExhaustiveness(adt, patterns) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyNothing() };  if true { return SkyNothing() };  return nil }() }() };  return nil }() }() }() };  return nil }() }()
}

func Compiler_Exhaustive_CheckBoolExhaustiveness(patterns any) any {
	return func() any { covered := Compiler_Exhaustive_CollectBoolPatterns(patterns, sky_setEmpty()); _ = covered; hasTrue := sky_call(sky_setMember("True"), covered); _ = hasTrue; hasFalse := sky_call(sky_setMember("False"), covered); _ = hasFalse; return func() any { if sky_asBool(sky_asBool(hasTrue) && sky_asBool(hasFalse)) { return SkyNothing() }; return func() any { if sky_asBool(hasTrue) { return SkyJust("Missing pattern: False") }; return func() any { if sky_asBool(hasFalse) { return SkyJust("Missing pattern: True") }; return SkyJust("Missing patterns: True, False") }() }() }() }()
}

func Compiler_Exhaustive_CollectBoolPatterns(patterns any, acc any) any {
	return func() any { return func() any { __subject := patterns; if len(sky_asList(__subject)) == 0 { return acc };  if len(sky_asList(__subject)) > 0 { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PConstructor" { parts := sky_asMap(__subject)["V0"]; _ = parts; return func() any { name := Compiler_Exhaustive_LastPart(parts); _ = name; return Compiler_Exhaustive_CollectBoolPatterns(rest, sky_call(sky_setInsert(name), acc)) }() };  if sky_asMap(__subject)["SkyName"] == "PLiteral" { b := sky_asMap(sky_asMap(__subject)["V0"])["V0"]; _ = b; return func() any { if sky_asBool(b) { return Compiler_Exhaustive_CollectBoolPatterns(rest, sky_call(sky_setInsert("True"), acc)) }; return Compiler_Exhaustive_CollectBoolPatterns(rest, sky_call(sky_setInsert("False"), acc)) }() };  if true { return Compiler_Exhaustive_CollectBoolPatterns(rest, acc) };  return nil }() }() };  return nil }() }()
}

func Compiler_Exhaustive_CheckAdtExhaustiveness(adt any, patterns any) any {
	return func() any { allCtors := sky_setFromList(sky_dictKeys(sky_asMap(adt)["constructors"])); _ = allCtors; coveredCtors := Compiler_Exhaustive_CollectConstructorPatterns(patterns, sky_setEmpty()); _ = coveredCtors; missing := sky_call(sky_setDiff(allCtors), coveredCtors); _ = missing; return func() any { if sky_asBool(sky_setIsEmpty(missing)) { return SkyNothing() }; return func() any { missingList := sky_setToList(missing); _ = missingList; return SkyJust(sky_concat("Missing patterns: ", sky_call(sky_stringJoin(", "), missingList))) }() }() }()
}

func Compiler_Exhaustive_CollectConstructorPatterns(patterns any, acc any) any {
	return func() any { return func() any { __subject := patterns; if len(sky_asList(__subject)) == 0 { return acc };  if len(sky_asList(__subject)) > 0 { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PConstructor" { parts := sky_asMap(__subject)["V0"]; _ = parts; return func() any { name := Compiler_Exhaustive_LastPart(parts); _ = name; return Compiler_Exhaustive_CollectConstructorPatterns(rest, sky_call(sky_setInsert(name), acc)) }() };  if sky_asMap(__subject)["SkyName"] == "PAs" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Compiler_Exhaustive_CollectConstructorPatterns(append([]any{inner}, sky_asList(rest)...), acc) };  if true { return Compiler_Exhaustive_CollectConstructorPatterns(rest, acc) };  return nil }() }() };  return nil }() }()
}

func Compiler_Exhaustive_LastPart(parts any) any {
	return func() any { return func() any { __subject := sky_listReverse(parts); if len(sky_asList(__subject)) > 0 { last := sky_asList(__subject)[0]; _ = last; return last };  if len(sky_asList(__subject)) == 0 { return "" };  return nil }() }()
}

var emptyResult = Compiler_PatternCheck_EmptyResult

var CheckPattern = Compiler_PatternCheck_CheckPattern

var CheckConstructorPattern = Compiler_PatternCheck_CheckConstructorPattern

var SplitFunType = Compiler_PatternCheck_SplitFunType

var CheckPatternList = Compiler_PatternCheck_CheckPatternList

var CheckPatternListSame = Compiler_PatternCheck_CheckPatternListSame

var LiteralType = Compiler_PatternCheck_LiteralType

var unify = Compiler_Unify_Unify

var Unify = Compiler_Unify_Unify

var unifyConst = Compiler_Unify_UnifyConst

var UnifyConst = Compiler_Unify_UnifyConst

var unifyFun = Compiler_Unify_UnifyFun

var UnifyFun = Compiler_Unify_UnifyFun

var unifyApp = Compiler_Unify_UnifyApp

var UnifyApp = Compiler_Unify_UnifyApp

var unifyTuple = Compiler_Unify_UnifyTuple

var UnifyTuple = Compiler_Unify_UnifyTuple

var unifyRecord = Compiler_Unify_UnifyRecord

var UnifyRecord = Compiler_Unify_UnifyRecord

var bindVar = Compiler_Unify_BindVar

var BindVar = Compiler_Unify_BindVar

var isUniversalUnifier = Compiler_Unify_IsUniversalUnifier

var IsUniversalUnifier = Compiler_Unify_IsUniversalUnifier

var isNumericCoercion = Compiler_Unify_IsNumericCoercion

var IsNumericCoercion = Compiler_Unify_IsNumericCoercion

var unifyList = Compiler_Unify_UnifyList

var UnifyList = Compiler_Unify_UnifyList

var unifyRecords = Compiler_Unify_UnifyRecords

var UnifyRecords = Compiler_Unify_UnifyRecords

var unifyRecordFields = Compiler_Unify_UnifyRecordFields

var UnifyRecordFields = Compiler_Unify_UnifyRecordFields

func Compiler_PatternCheck_EmptyResult() any {
	return map[string]any{"substitution": emptySub, "bindings": []any{}}
}

func Compiler_PatternCheck_CheckPattern(counter any, registry any, env any, pat any, expectedType any) any {
	return func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PWildcard" { return SkyOk(Compiler_PatternCheck_EmptyResult()) };  if sky_asMap(__subject)["SkyName"] == "PVariable" { name := sky_asMap(__subject)["V0"]; _ = name; return SkyOk(map[string]any{"substitution": emptySub, "bindings": []any{SkyTuple2{V0: name, V1: expectedType}}}) };  if sky_asMap(__subject)["SkyName"] == "PLiteral" { lit := sky_asMap(__subject)["V0"]; _ = lit; return func() any { litType := Compiler_PatternCheck_LiteralType(lit); _ = litType; return func() any { return func() any { __subject := unify(expectedType, litType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Pattern literal type mismatch: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { sub := sky_asSkyResult(__subject).OkValue; _ = sub; return SkyOk(map[string]any{"substitution": sub, "bindings": []any{}}) };  if sky_asMap(__subject)["SkyName"] == "PTuple" { items := sky_asMap(__subject)["V0"]; _ = items; return func() any { freshVars := sky_call(sky_listMap(func(item any) any { return freshVar(counter, SkyNothing()) }), items); _ = freshVars; tupleType := TTuple(freshVars); _ = tupleType; return func() any { return func() any { __subject := unify(expectedType, tupleType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Tuple pattern mismatch: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { sub := sky_asSkyResult(__subject).OkValue; _ = sub; return Compiler_PatternCheck_CheckPatternList(counter, registry, env, items, sky_call(sky_listMap(func(x any) any { return applySub(sub, x) }), freshVars), sub, []any{}) };  if sky_asMap(__subject)["SkyName"] == "PList" { items := sky_asMap(__subject)["V0"]; _ = items; return func() any { elemVar := freshVar(counter, SkyJust("elem")); _ = elemVar; listType := TApp(TConst("List"), []any{elemVar}); _ = listType; return func() any { return func() any { __subject := unify(expectedType, listType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("List pattern mismatch: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { sub := sky_asSkyResult(__subject).OkValue; _ = sub; return func() any { elemType := applySub(sub, elemVar); _ = elemType; return Compiler_PatternCheck_CheckPatternListSame(counter, registry, env, items, elemType, sub, []any{}) }() };  if sky_asMap(__subject)["SkyName"] == "PCons" { headPat := sky_asMap(__subject)["V0"]; _ = headPat; tailPat := sky_asMap(__subject)["V1"]; _ = tailPat; return func() any { elemVar := freshVar(counter, SkyJust("elem")); _ = elemVar; listType := TApp(TConst("List"), []any{elemVar}); _ = listType; return func() any { return func() any { __subject := unify(expectedType, listType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Cons pattern mismatch: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { sub1 := sky_asSkyResult(__subject).OkValue; _ = sub1; return func() any { elemType := applySub(sub1, elemVar); _ = elemType; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, headPat, elemType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { headResult := sky_asSkyResult(__subject).OkValue; _ = headResult; return func() any { sub2 := composeSubs(sky_asMap(headResult)["substitution"], sub1); _ = sub2; tailExpected := applySub(sub2, listType); _ = tailExpected; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, tailPat, tailExpected); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { tailResult := sky_asSkyResult(__subject).OkValue; _ = tailResult; return func() any { finalSub := composeSubs(sky_asMap(tailResult)["substitution"], sub2); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "bindings": sky_call(sky_listAppend(sky_asMap(headResult)["bindings"]), sky_asMap(tailResult)["bindings"])}) }() };  if sky_asMap(__subject)["SkyName"] == "PAs" { innerPat := sky_asMap(__subject)["V0"]; _ = innerPat; name := sky_asMap(__subject)["V1"]; _ = name; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, innerPat, expectedType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return SkyOk(sky_recordUpdate(result, map[string]any{"bindings": append([]any{SkyTuple2{V0: name, V1: applySub(sky_asMap(result)["substitution"], expectedType)}}, sky_asList(sky_asMap(result)["bindings"])...)})) };  if sky_asMap(__subject)["SkyName"] == "PConstructor" { parts := sky_asMap(__subject)["V0"]; _ = parts; argPats := sky_asMap(__subject)["V1"]; _ = argPats; return func() any { ctorName := func() any { return func() any { __subject := sky_listReverse(parts); if len(sky_asList(__subject)) > 0 { last := sky_asList(__subject)[0]; _ = last; return last };  if len(sky_asList(__subject)) == 0 { return "" };  return nil }() }(); _ = ctorName; return func() any { return func() any { __subject := Compiler_Adt_LookupConstructor(ctorName, registry); if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return func() any { return func() any { __subject := Compiler_Env_Lookup(ctorName, env); if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyErr(sky_concat("Unknown constructor in pattern: ", ctorName)) };  if sky_asSkyMaybe(__subject).SkyName == "Just" { scheme := sky_asSkyMaybe(__subject).JustValue; _ = scheme; return Compiler_PatternCheck_CheckConstructorPattern(counter, registry, env, ctorName, scheme, argPats, expectedType) };  if sky_asSkyMaybe(__subject).SkyName == "Just" { scheme := sky_asSkyMaybe(__subject).JustValue; _ = scheme; return Compiler_PatternCheck_CheckConstructorPattern(counter, registry, env, ctorName, scheme, argPats, expectedType) };  if sky_asMap(__subject)["SkyName"] == "PRecord" { fields := sky_asMap(__subject)["V0"]; _ = fields; return func() any { fieldBindings := sky_call(sky_listMap(func(f any) any { return SkyTuple2{V0: f, V1: expectedType} }), fields); _ = fieldBindings; return SkyOk(map[string]any{"substitution": emptySub, "bindings": fieldBindings}) }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }()
}

func Compiler_PatternCheck_CheckConstructorPattern(counter any, registry any, env any, ctorName any, scheme any, argPats any, expectedType any) any {
	return func() any { instType := instantiate(counter, scheme); _ = instType; splitResult := Compiler_PatternCheck_SplitFunType(instType); _ = splitResult; argTypes := sky_fst(splitResult); _ = argTypes; resultType := sky_snd(splitResult); _ = resultType; return func() any { if sky_asBool(!sky_equal(sky_listLength(argPats), sky_listLength(argTypes))) { return SkyErr(sky_concat("Constructor ", sky_concat(ctorName, sky_concat(" expects ", sky_concat(sky_stringFromInt(sky_listLength(argTypes)), sky_concat(" arguments, got ", sky_stringFromInt(sky_listLength(argPats)))))))) }; return func() any { return func() any { __subject := unify(expectedType, resultType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Constructor pattern type mismatch: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { sub := sky_asSkyResult(__subject).OkValue; _ = sub; return Compiler_PatternCheck_CheckPatternList(counter, registry, env, argPats, sky_call(sky_listMap(func(x any) any { return applySub(sub, x) }), argTypes), sub, []any{}) };  return nil }() }() }() }()
}

func Compiler_PatternCheck_SplitFunType(t any) any {
	return func() any { return func() any { __subject := t; if sky_asMap(__subject)["SkyName"] == "TFun" { fromT := sky_asMap(__subject)["V0"]; _ = fromT; toT := sky_asMap(__subject)["V1"]; _ = toT; return func() any { inner := Compiler_PatternCheck_SplitFunType(toT); _ = inner; rest := sky_fst(inner); _ = rest; result := sky_snd(inner); _ = result; return SkyTuple2{V0: append([]any{fromT}, sky_asList(rest)...), V1: result} }() };  if true { return SkyTuple2{V0: []any{}, V1: t} };  return nil }() }()
}

func Compiler_PatternCheck_CheckPatternList(counter any, registry any, env any, pats any, types any, sub any, bindings any) any {
	return func() any { return func() any { __subject := pats; if len(sky_asList(__subject)) == 0 { return func() any { return func() any { __subject := types; if len(sky_asList(__subject)) == 0 { return SkyOk(map[string]any{"substitution": sub, "bindings": bindings}) };  if true { return SkyErr("Pattern/type count mismatch") };  if len(sky_asList(__subject)) > 0 { p := sky_asList(__subject)[0]; _ = p; ps := sky_asList(__subject)[1:]; _ = ps; return func() any { return func() any { __subject := types; if len(sky_asList(__subject)) == 0 { return SkyErr("Pattern/type count mismatch") };  if len(sky_asList(__subject)) > 0 { t := sky_asList(__subject)[0]; _ = t; ts := sky_asList(__subject)[1:]; _ = ts; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, p, applySub(sub, t)); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return Compiler_PatternCheck_CheckPatternList(counter, registry, env, ps, ts, composeSubs(sky_asMap(result)["substitution"], sub), sky_call(sky_listAppend(bindings), sky_asMap(result)["bindings"])) };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_PatternCheck_CheckPatternListSame(counter any, registry any, env any, pats any, elemType any, sub any, bindings any) any {
	return func() any { return func() any { __subject := pats; if len(sky_asList(__subject)) == 0 { return SkyOk(map[string]any{"substitution": sub, "bindings": bindings}) };  if len(sky_asList(__subject)) > 0 { p := sky_asList(__subject)[0]; _ = p; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, p, applySub(sub, elemType)); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return Compiler_PatternCheck_CheckPatternListSame(counter, registry, env, rest, elemType, composeSubs(sky_asMap(result)["substitution"], sub), sky_call(sky_listAppend(bindings), sky_asMap(result)["bindings"])) };  return nil }() }() };  return nil }() }()
}

func Compiler_PatternCheck_LiteralType(lit any) any {
	return func() any { return func() any { __subject := lit; if sky_asMap(__subject)["SkyName"] == "LitInt" { return TConst("Int") };  if sky_asMap(__subject)["SkyName"] == "LitFloat" { return TConst("Float") };  if sky_asMap(__subject)["SkyName"] == "LitString" { return TConst("String") };  if sky_asMap(__subject)["SkyName"] == "LitBool" { return TConst("Bool") };  if sky_asMap(__subject)["SkyName"] == "LitChar" { return TConst("Char") };  return nil }() }()
}

func Compiler_Adt_EmptyRegistry() any {
	return sky_dictEmpty()
}

func Compiler_Adt_LookupConstructor(ctorName any, registry any) any {
	return Compiler_Adt_LookupCtorInEntries(ctorName, sky_dictValues(registry))
}

func Compiler_Adt_LookupCtorInEntries(ctorName any, adts any) any {
	return func() any { return func() any { __subject := adts; if len(sky_asList(__subject)) == 0 { return SkyNothing() };  if len(sky_asList(__subject)) > 0 { adt := sky_asList(__subject)[0]; _ = adt; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := sky_call(sky_dictGet(ctorName), sky_asMap(adt)["constructors"]); if sky_asSkyMaybe(__subject).SkyName == "Just" { scheme := sky_asSkyMaybe(__subject).JustValue; _ = scheme; return SkyJust(scheme) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return Compiler_Adt_LookupCtorInEntries(ctorName, rest) };  return nil }() }() };  return nil }() }()
}

func Compiler_Adt_LookupConstructorAdt(ctorName any, registry any) any {
	return Compiler_Adt_LookupCtorAdtInEntries(ctorName, sky_dictValues(registry))
}

func Compiler_Adt_LookupCtorAdtInEntries(ctorName any, adts any) any {
	return func() any { return func() any { __subject := adts; if len(sky_asList(__subject)) == 0 { return SkyNothing() };  if len(sky_asList(__subject)) > 0 { adt := sky_asList(__subject)[0]; _ = adt; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := sky_call(sky_dictGet(ctorName), sky_asMap(adt)["constructors"]); if sky_asSkyMaybe(__subject).SkyName == "Just" { return SkyJust(adt) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return Compiler_Adt_LookupCtorAdtInEntries(ctorName, rest) };  return nil }() }() };  return nil }() }()
}

func Compiler_Adt_RegisterAdts(counter any, decls any) any {
	return Compiler_Adt_RegisterAdtsLoop(counter, decls, Compiler_Adt_EmptyRegistry(), Compiler_Env_Empty(), []any{})
}

func Compiler_Adt_RegisterAdtsLoop(counter any, decls any, registry any, env any, diagnostics any) any {
	return func() any { return func() any { __subject := decls; if len(sky_asList(__subject)) == 0 { return SkyTuple3{V0: registry, V1: env, V2: diagnostics} };  if len(sky_asList(__subject)) > 0 { decl := sky_asList(__subject)[0]; _ = decl; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "TypeDecl" { name := sky_asMap(__subject)["V0"]; _ = name; params := sky_asMap(__subject)["V1"]; _ = params; variants := sky_asMap(__subject)["V2"]; _ = variants; return func() any { __tup_newRegistry_newEnv_newDiags := Compiler_Adt_RegisterOneAdt(counter, name, params, variants, registry, env); newRegistry := sky_asTuple3(__tup_newRegistry_newEnv_newDiags).V0; _ = newRegistry; newEnv := sky_asTuple3(__tup_newRegistry_newEnv_newDiags).V1; _ = newEnv; newDiags := sky_asTuple3(__tup_newRegistry_newEnv_newDiags).V2; _ = newDiags; return Compiler_Adt_RegisterAdtsLoop(counter, rest, newRegistry, newEnv, sky_call(sky_listAppend(diagnostics), newDiags)) }() };  if true { return Compiler_Adt_RegisterAdtsLoop(counter, rest, registry, env, diagnostics) };  return nil }() }() };  return nil }() }()
}

func Compiler_Adt_RegisterOneAdt(counter any, typeName any, typeParams any, variants any, registry any, env any) any {
	return func() any { arity := sky_listLength(typeParams); _ = arity; ctorSchemes := sky_call2(sky_listFoldl(func(variant any) any { return func(acc any) any { return func() any { scheme := Compiler_Adt_BuildConstructorScheme(counter, typeName, typeParams, variant); _ = scheme; return sky_call2(sky_dictInsert(sky_asMap(variant)["name"]), scheme, acc) }() } }), sky_dictEmpty(), variants); _ = ctorSchemes; adt := map[string]any{"name": typeName, "arity": arity, "constructors": ctorSchemes}; _ = adt; newRegistry := sky_call2(sky_dictInsert(typeName), adt, registry); _ = newRegistry; newEnv := sky_call2(sky_dictFoldl(func(ctorName any) any { return func(scheme any) any { return func(acc any) any { return Compiler_Env_Extend(ctorName, scheme, acc) } } }), env, ctorSchemes); _ = newEnv; return SkyTuple3{V0: newRegistry, V1: newEnv, V2: []any{}} }()
}

func Compiler_Adt_BuildConstructorScheme(counter any, typeName any, typeParams any, variant any) any {
	return func() any { paramVars := sky_call(sky_listMap(func(p any) any { return freshVar(counter, SkyJust(p)) }), typeParams); _ = paramVars; paramMap := Compiler_Adt_BuildParamMap(typeParams, paramVars, sky_dictEmpty()); _ = paramMap; resultType := func() any { if sky_asBool(sky_listIsEmpty(paramVars)) { return TConst(typeName) }; return TApp(TConst(typeName), paramVars) }(); _ = resultType; fieldTypes := sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Adt_ResolveTypeExpr(paramMap, __pa0) }), sky_asMap(variant)["fields"]); _ = fieldTypes; ctorType := Compiler_Adt_BuildFunType(fieldTypes, resultType); _ = ctorType; quantified := sky_call(sky_listFilterMap(Compiler_Adt_GetVarId), paramVars); _ = quantified; return map[string]any{"quantified": quantified, "type_": ctorType} }()
}

func Compiler_Adt_BuildParamMap(names any, vars any, acc any) any {
	return func() any { return func() any { __subject := names; if len(sky_asList(__subject)) == 0 { return acc };  if len(sky_asList(__subject)) > 0 { nameH := sky_asList(__subject)[0]; _ = nameH; nameRest := sky_asList(__subject)[1:]; _ = nameRest; return func() any { return func() any { __subject := vars; if len(sky_asList(__subject)) == 0 { return acc };  if len(sky_asList(__subject)) > 0 { varH := sky_asList(__subject)[0]; _ = varH; varRest := sky_asList(__subject)[1:]; _ = varRest; return Compiler_Adt_BuildParamMap(nameRest, varRest, sky_call2(sky_dictInsert(nameH), varH, acc)) };  return nil }() }() };  return nil }() }()
}

func Compiler_Adt_BuildFunType(args any, result any) any {
	return func() any { return func() any { __subject := args; if len(sky_asList(__subject)) == 0 { return result };  if len(sky_asList(__subject)) > 0 { arg := sky_asList(__subject)[0]; _ = arg; rest := sky_asList(__subject)[1:]; _ = rest; return TFun(arg, Compiler_Adt_BuildFunType(rest, result)) };  return nil }() }()
}

func Compiler_Adt_GetVarId(t any) any {
	return func() any { return func() any { __subject := t; if sky_asMap(__subject)["SkyName"] == "TVar" { id := sky_asMap(__subject)["V0"]; _ = id; return SkyJust(id) };  if true { return SkyNothing() };  return nil }() }()
}

func Compiler_Adt_ResolveTypeExpr(paramMap any, texpr any) any {
	return func() any { return func() any { __subject := texpr; if sky_asMap(__subject)["SkyName"] == "TypeRef" { parts := sky_asMap(__subject)["V0"]; _ = parts; args := sky_asMap(__subject)["V1"]; _ = args; return func() any { name := sky_call(sky_stringJoin("."), parts); _ = name; resolvedArgs := sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Adt_ResolveTypeExpr(paramMap, __pa0) }), args); _ = resolvedArgs; return func() any { return func() any { __subject := sky_call(sky_dictGet(name), paramMap); if sky_asSkyMaybe(__subject).SkyName == "Just" { tv := sky_asSkyMaybe(__subject).JustValue; _ = tv; return tv };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return func() any { if sky_asBool(sky_listIsEmpty(resolvedArgs)) { return TConst(name) }; return TApp(TConst(name), resolvedArgs) }() };  if sky_asMap(__subject)["SkyName"] == "TypeVar" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { return func() any { __subject := sky_call(sky_dictGet(name), paramMap); if sky_asSkyMaybe(__subject).SkyName == "Just" { tv := sky_asSkyMaybe(__subject).JustValue; _ = tv; return tv };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return TVar(0, SkyJust(name)) };  if sky_asMap(__subject)["SkyName"] == "FunType" { fromT := sky_asMap(__subject)["V0"]; _ = fromT; toT := sky_asMap(__subject)["V1"]; _ = toT; return TFun(Compiler_Adt_ResolveTypeExpr(paramMap, fromT), Compiler_Adt_ResolveTypeExpr(paramMap, toT)) };  if sky_asMap(__subject)["SkyName"] == "RecordTypeExpr" { fields := sky_asMap(__subject)["V0"]; _ = fields; return func() any { fieldDict := sky_call2(sky_listFoldl(func(f any) any { return func(acc any) any { return sky_call2(sky_dictInsert(sky_asMap(f)["name"]), Compiler_Adt_ResolveTypeExpr(paramMap, sky_asMap(f)["type_"]), acc) } }), sky_dictEmpty(), fields); _ = fieldDict; return TRecord(fieldDict) }() };  if sky_asMap(__subject)["SkyName"] == "TupleTypeExpr" { items := sky_asMap(__subject)["V0"]; _ = items; return TTuple(sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Adt_ResolveTypeExpr(paramMap, __pa0) }), items)) };  if sky_asMap(__subject)["SkyName"] == "UnitTypeExpr" { return TConst("Unit") };  return nil }() }() };  return nil }() }() }() };  return nil }() }()
}

func Compiler_Types_TVar(v0 any, v1 any) any {
	return map[string]any{"Tag": 0, "SkyName": "TVar", "V0": v0, "V1": v1}
}

func Compiler_Types_TConst(v0 any) any {
	return map[string]any{"Tag": 1, "SkyName": "TConst", "V0": v0}
}

func Compiler_Types_TFun(v0 any, v1 any) any {
	return map[string]any{"Tag": 2, "SkyName": "TFun", "V0": v0, "V1": v1}
}

func Compiler_Types_TApp(v0 any, v1 any) any {
	return map[string]any{"Tag": 3, "SkyName": "TApp", "V0": v0, "V1": v1}
}

func Compiler_Types_TTuple(v0 any) any {
	return map[string]any{"Tag": 4, "SkyName": "TTuple", "V0": v0}
}

func Compiler_Types_TRecord(v0 any) any {
	return map[string]any{"Tag": 5, "SkyName": "TRecord", "V0": v0}
}

func Compiler_Types_FreshVar(counter any, name any) any {
	return func() any { id := sky_refGet(counter); _ = id; sky_call(sky_refSet(sky_asInt(id) + sky_asInt(1)), counter); return TVar(id, name) }()
}

func Compiler_Types_EmptySub() any {
	return sky_dictEmpty()
}

func Compiler_Types_ApplySub(sub any, t any) any {
	return func() any { return func() any { __subject := t; if sky_asMap(__subject)["SkyName"] == "TVar" { id := sky_asMap(__subject)["V0"]; _ = id; return func() any { return func() any { __subject := sky_call(sky_dictGet(id), sub); if sky_asSkyMaybe(__subject).SkyName == "Just" { replacement := sky_asSkyMaybe(__subject).JustValue; _ = replacement; return replacement };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return t };  if sky_asMap(__subject)["SkyName"] == "TConst" { return t };  if sky_asMap(__subject)["SkyName"] == "TFun" { fromT := sky_asMap(__subject)["V0"]; _ = fromT; toT := sky_asMap(__subject)["V1"]; _ = toT; return TFun(Compiler_Types_ApplySub(sub, fromT), Compiler_Types_ApplySub(sub, toT)) };  if sky_asMap(__subject)["SkyName"] == "TApp" { ctor := sky_asMap(__subject)["V0"]; _ = ctor; args := sky_asMap(__subject)["V1"]; _ = args; return TApp(Compiler_Types_ApplySub(sub, ctor), sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Types_ApplySub(sub, __pa0) }), args)) };  if sky_asMap(__subject)["SkyName"] == "TTuple" { items := sky_asMap(__subject)["V0"]; _ = items; return TTuple(sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Types_ApplySub(sub, __pa0) }), items)) };  if sky_asMap(__subject)["SkyName"] == "TRecord" { fields := sky_asMap(__subject)["V0"]; _ = fields; return TRecord(sky_call(sky_dictMap(func(kk any) any { return func(v any) any { return Compiler_Types_ApplySub(sub, v) } }), fields)) };  if true { return t };  return nil }() }() };  return nil }() }()
}

func Compiler_Types_ApplySubToScheme(sub any, scheme any) any {
	return func() any { filtered := sky_call2(sky_listFoldl(func(q any) any { return func(s any) any { return sky_call(sky_dictRemove(q), s) } }), sub, sky_asMap(scheme)["quantified"]); _ = filtered; return sky_recordUpdate(scheme, map[string]any{"type_": Compiler_Types_ApplySub(filtered, sky_asMap(scheme)["type_"])}) }()
}

func Compiler_Types_ComposeSubs(s1 any, s2 any) any {
	return func() any { applied := sky_call(sky_dictMap(func(kk any) any { return func(t any) any { return Compiler_Types_ApplySub(s1, t) } }), s2); _ = applied; return sky_call(sky_dictUnion(applied), s1) }()
}

func Compiler_Types_FreeVars(t any) any {
	return func() any { return func() any { __subject := t; if sky_asMap(__subject)["SkyName"] == "TVar" { id := sky_asMap(__subject)["V0"]; _ = id; return sky_setSingleton(id) };  if sky_asMap(__subject)["SkyName"] == "TConst" { return sky_setEmpty() };  if sky_asMap(__subject)["SkyName"] == "TFun" { fromT := sky_asMap(__subject)["V0"]; _ = fromT; toT := sky_asMap(__subject)["V1"]; _ = toT; return sky_call(sky_setUnion(Compiler_Types_FreeVars(fromT)), Compiler_Types_FreeVars(toT)) };  if sky_asMap(__subject)["SkyName"] == "TApp" { ctor := sky_asMap(__subject)["V0"]; _ = ctor; args := sky_asMap(__subject)["V1"]; _ = args; return sky_call2(sky_listFoldl(func(arg any) any { return func(acc any) any { return sky_call(sky_setUnion(Compiler_Types_FreeVars(arg)), acc) } }), Compiler_Types_FreeVars(ctor), args) };  if sky_asMap(__subject)["SkyName"] == "TTuple" { items := sky_asMap(__subject)["V0"]; _ = items; return sky_call2(sky_listFoldl(func(item any) any { return func(acc any) any { return sky_call(sky_setUnion(Compiler_Types_FreeVars(item)), acc) } }), sky_setEmpty(), items) };  if sky_asMap(__subject)["SkyName"] == "TRecord" { fields := sky_asMap(__subject)["V0"]; _ = fields; return sky_call2(sky_dictFoldl(func(kk any) any { return func(v any) any { return func(acc any) any { return sky_call(sky_setUnion(Compiler_Types_FreeVars(v)), acc) } } }), sky_setEmpty(), fields) };  if true { return sky_setEmpty() };  return nil }() }()
}

func Compiler_Types_FreeVarsInScheme(scheme any) any {
	return func() any { typeVars := Compiler_Types_FreeVars(sky_asMap(scheme)["type_"]); _ = typeVars; quantifiedSet := sky_setFromList(sky_asMap(scheme)["quantified"]); _ = quantifiedSet; return sky_call(sky_setDiff(typeVars), quantifiedSet) }()
}

func Compiler_Types_Instantiate(counter any, scheme any) any {
	return func() any { sub := sky_call2(sky_listFoldl(func(qv any) any { return func(s any) any { return func() any { fresh := Compiler_Types_FreshVar(counter, SkyNothing()); _ = fresh; return sky_call2(sky_dictInsert(qv), fresh, s) }() } }), Compiler_Types_EmptySub(), sky_asMap(scheme)["quantified"]); _ = sub; return Compiler_Types_ApplySub(sub, sky_asMap(scheme)["type_"]) }()
}

func Compiler_Types_Generalize(env any, t any) any {
	return func() any { typeVars := Compiler_Types_FreeVars(t); _ = typeVars; envVars := sky_call2(sky_dictFoldl(func(kk any) any { return func(scheme any) any { return func(acc any) any { return sky_call(sky_setUnion(Compiler_Types_FreeVarsInScheme(scheme)), acc) } } }), sky_setEmpty(), env); _ = envVars; quantified := sky_setToList(sky_call(sky_setDiff(typeVars), envVars)); _ = quantified; return map[string]any{"quantified": quantified, "type_": t} }()
}

func Compiler_Types_Mono(t any) any {
	return map[string]any{"quantified": []any{}, "type_": t}
}

func Compiler_Types_FormatType(t any) any {
	return func() any { return func() any { __subject := t; if sky_asMap(__subject)["SkyName"] == "TVar" { id := sky_asMap(__subject)["V0"]; _ = id; name := sky_asMap(__subject)["V1"]; _ = name; return func() any { return func() any { __subject := name; if sky_asSkyMaybe(__subject).SkyName == "Just" { n := sky_asSkyMaybe(__subject).JustValue; _ = n; return n };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return sky_concat("t", sky_stringFromInt(id)) };  if sky_asMap(__subject)["SkyName"] == "TConst" { name := sky_asMap(__subject)["V0"]; _ = name; return name };  if sky_asMap(__subject)["SkyName"] == "TFun" { fromT := sky_asMap(__subject)["V0"]; _ = fromT; toT := sky_asMap(__subject)["V1"]; _ = toT; return func() any { fromStr := func() any { return func() any { __subject := fromT; if sky_asMap(__subject)["SkyName"] == "TFun" { return sky_concat("(", sky_concat(Compiler_Types_FormatType(fromT), ")")) };  if true { return Compiler_Types_FormatType(fromT) };  return nil }() }(); _ = fromStr; return sky_concat(fromStr, sky_concat(" -> ", Compiler_Types_FormatType(toT))) }() };  if sky_asMap(__subject)["SkyName"] == "TApp" { ctor := sky_asMap(__subject)["V0"]; _ = ctor; args := sky_asMap(__subject)["V1"]; _ = args; return sky_concat(Compiler_Types_FormatType(ctor), sky_concat(" ", sky_call(sky_stringJoin(" "), sky_call(sky_listMap(Compiler_Types_FormatType), args)))) };  if sky_asMap(__subject)["SkyName"] == "TTuple" { items := sky_asMap(__subject)["V0"]; _ = items; return sky_concat("( ", sky_concat(sky_call(sky_stringJoin(" , "), sky_call(sky_listMap(Compiler_Types_FormatType), items)), " )")) };  if sky_asMap(__subject)["SkyName"] == "TRecord" { fields := sky_asMap(__subject)["V0"]; _ = fields; return func() any { fieldStrs := sky_call(sky_listMap(func(pair any) any { return sky_concat(sky_fst(pair), sky_concat(" : ", Compiler_Types_FormatType(sky_snd(pair)))) }), sky_dictToList(fields)); _ = fieldStrs; return sky_concat("{ ", sky_concat(sky_call(sky_stringJoin(" , "), fieldStrs), " }")) }() };  if true { return "?" };  return nil }() }() };  return nil }() }()
}

func Compiler_GoIr_GoIdent(v0 any) any {
	return map[string]any{"Tag": 0, "SkyName": "GoIdent", "V0": v0}
}

func Compiler_GoIr_GoBasicLit(v0 any) any {
	return map[string]any{"Tag": 1, "SkyName": "GoBasicLit", "V0": v0}
}

func Compiler_GoIr_GoStringLit(v0 any) any {
	return map[string]any{"Tag": 2, "SkyName": "GoStringLit", "V0": v0}
}

func Compiler_GoIr_GoCallExpr(v0 any, v1 any) any {
	return map[string]any{"Tag": 3, "SkyName": "GoCallExpr", "V0": v0, "V1": v1}
}

func Compiler_GoIr_GoSelectorExpr(v0 any, v1 any) any {
	return map[string]any{"Tag": 4, "SkyName": "GoSelectorExpr", "V0": v0, "V1": v1}
}

func Compiler_GoIr_GoSliceLit(v0 any) any {
	return map[string]any{"Tag": 5, "SkyName": "GoSliceLit", "V0": v0}
}

func Compiler_GoIr_GoMapLit(v0 any) any {
	return map[string]any{"Tag": 6, "SkyName": "GoMapLit", "V0": v0}
}

func Compiler_GoIr_GoFuncLit(v0 any, v1 any) any {
	return map[string]any{"Tag": 7, "SkyName": "GoFuncLit", "V0": v0, "V1": v1}
}

func Compiler_GoIr_GoRawExpr(v0 any) any {
	return map[string]any{"Tag": 8, "SkyName": "GoRawExpr", "V0": v0}
}

func Compiler_GoIr_GoCompositeLit(v0 any, v1 any) any {
	return map[string]any{"Tag": 9, "SkyName": "GoCompositeLit", "V0": v0, "V1": v1}
}

func Compiler_GoIr_GoBinaryExpr(v0 any, v1 any, v2 any) any {
	return map[string]any{"Tag": 10, "SkyName": "GoBinaryExpr", "V0": v0, "V1": v1, "V2": v2}
}

func Compiler_GoIr_GoUnaryExpr(v0 any, v1 any) any {
	return map[string]any{"Tag": 11, "SkyName": "GoUnaryExpr", "V0": v0, "V1": v1}
}

func Compiler_GoIr_GoIndexExpr(v0 any, v1 any) any {
	return map[string]any{"Tag": 12, "SkyName": "GoIndexExpr", "V0": v0, "V1": v1}
}

var Compiler_GoIr_GoNilExpr = map[string]any{"Tag": 13, "SkyName": "GoNilExpr"}

func Compiler_GoIr_GoExprStmt(v0 any) any {
	return map[string]any{"Tag": 0, "SkyName": "GoExprStmt", "V0": v0}
}

func Compiler_GoIr_GoAssign(v0 any, v1 any) any {
	return map[string]any{"Tag": 1, "SkyName": "GoAssign", "V0": v0, "V1": v1}
}

func Compiler_GoIr_GoShortDecl(v0 any, v1 any) any {
	return map[string]any{"Tag": 2, "SkyName": "GoShortDecl", "V0": v0, "V1": v1}
}

func Compiler_GoIr_GoReturn(v0 any) any {
	return map[string]any{"Tag": 3, "SkyName": "GoReturn", "V0": v0}
}

var Compiler_GoIr_GoReturnVoid = map[string]any{"Tag": 4, "SkyName": "GoReturnVoid"}

func Compiler_GoIr_GoIf(v0 any, v1 any, v2 any) any {
	return map[string]any{"Tag": 5, "SkyName": "GoIf", "V0": v0, "V1": v1, "V2": v2}
}

func Compiler_GoIr_GoBlock(v0 any) any {
	return map[string]any{"Tag": 6, "SkyName": "GoBlock", "V0": v0}
}

func Compiler_GoIr_GoDeclFunc(v0 any) any {
	return map[string]any{"Tag": 0, "SkyName": "GoDeclFunc", "V0": v0}
}

func Compiler_GoIr_GoDeclVar(v0 any, v1 any) any {
	return map[string]any{"Tag": 1, "SkyName": "GoDeclVar", "V0": v0, "V1": v1}
}

func Compiler_GoIr_GoDeclRaw(v0 any) any {
	return map[string]any{"Tag": 2, "SkyName": "GoDeclRaw", "V0": v0}
}

func Compiler_ParserCore_InitState(tokens any) any {
	return map[string]any{"tokens": tokens, "pos": 0, "errors": []any{}}
}

func Compiler_ParserCore_Peek(state any) any {
	return func() any { return func() any { __subject := sky_call(sky_listHead, sky_call(sky_listDrop(sky_asMap(state)["pos"]), sky_asMap(state)["tokens"])); if sky_asSkyMaybe(__subject).SkyName == "Just" { t := sky_asSkyMaybe(__subject).JustValue; _ = t; return t };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return map[string]any{"kind": TkEOF, "lexeme": "", "span": emptySpan} };  return nil }() }()
}

func Compiler_ParserCore_PeekAt(offset any, state any) any {
	return func() any { return func() any { __subject := sky_call(sky_listHead, sky_call(sky_listDrop(sky_asInt(sky_asMap(state)["pos"]) + sky_asInt(offset)), sky_asMap(state)["tokens"])); if sky_asSkyMaybe(__subject).SkyName == "Just" { t := sky_asSkyMaybe(__subject).JustValue; _ = t; return t };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return map[string]any{"kind": TkEOF, "lexeme": "", "span": emptySpan} };  return nil }() }()
}

func Compiler_ParserCore_Previous(state any) any {
	return func() any { if sky_asBool(sky_asInt(sky_asMap(state)["pos"]) > sky_asInt(0)) { return func() any { return func() any { __subject := sky_call(sky_listHead, sky_call(sky_listDrop(sky_asInt(sky_asMap(state)["pos"]) - sky_asInt(1)), sky_asMap(state)["tokens"])); if sky_asSkyMaybe(__subject).SkyName == "Just" { t := sky_asSkyMaybe(__subject).JustValue; _ = t; return t };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return map[string]any{"kind": TkEOF, "lexeme": "", "span": emptySpan} };  return nil }() }() }; return map[string]any{"kind": TkEOF, "lexeme": "", "span": emptySpan} }()
}

func Compiler_ParserCore_Advance(state any) any {
	return func() any { token := Compiler_ParserCore_Peek(state); _ = token; return func() any { if sky_asBool(Compiler_ParserCore_TokenKindEq(sky_asMap(token)["kind"], TkEOF)) { return SkyTuple2{V0: token, V1: state} }; return SkyTuple2{V0: token, V1: sky_recordUpdate(state, map[string]any{"pos": sky_asInt(sky_asMap(state)["pos"]) + sky_asInt(1)})} }() }()
}

func Compiler_ParserCore_MatchKind(kind any, state any) any {
	return Compiler_ParserCore_TokenKindEq(Compiler_ParserCore_PeekKind(state), kind)
}

func Compiler_ParserCore_MatchLexeme(lex any, state any) any {
	return sky_equal(Compiler_ParserCore_PeekLexeme(state), lex)
}

func Compiler_ParserCore_MatchKindLex(kind any, lex any, state any) any {
	return func() any { t := Compiler_ParserCore_Peek(state); _ = t; return sky_asBool(Compiler_ParserCore_TokenKindEq(sky_asMap(t)["kind"], kind)) && sky_asBool(sky_equal(sky_asMap(t)["lexeme"], lex)) }()
}

func Compiler_ParserCore_Consume(kind any, state any) any {
	return func() any { t := Compiler_ParserCore_Peek(state); _ = t; return func() any { if sky_asBool(Compiler_ParserCore_TokenKindEq(sky_asMap(t)["kind"], kind)) { return SkyOk(SkyTuple2{V0: t, V1: sky_recordUpdate(state, map[string]any{"pos": sky_asInt(sky_asMap(state)["pos"]) + sky_asInt(1)})}) }; return func() any { sp := sky_asMap(t)["span"]; _ = sp; st := sky_asMap(sp)["start"]; _ = st; return SkyErr(sky_concat("Expected ", sky_concat(Compiler_ParserCore_TokenKindStr(kind), sky_concat(" but got ", sky_concat(Compiler_ParserCore_TokenKindStr(sky_asMap(t)["kind"]), sky_concat(" '", sky_concat(sky_asMap(t)["lexeme"], sky_concat("' at ", sky_concat(sky_stringFromInt(sky_asMap(st)["line"]), sky_concat(":", sky_stringFromInt(sky_asMap(st)["column"]))))))))))) }() }() }()
}

func Compiler_ParserCore_ConsumeLex(kind any, lex any, state any) any {
	return func() any { t := Compiler_ParserCore_Peek(state); _ = t; return func() any { if sky_asBool(sky_asBool(Compiler_ParserCore_TokenKindEq(sky_asMap(t)["kind"], kind)) && sky_asBool(sky_equal(sky_asMap(t)["lexeme"], lex))) { return SkyOk(SkyTuple2{V0: t, V1: sky_recordUpdate(state, map[string]any{"pos": sky_asInt(sky_asMap(state)["pos"]) + sky_asInt(1)})}) }; return func() any { sp := sky_asMap(t)["span"]; _ = sp; st := sky_asMap(sp)["start"]; _ = st; return SkyErr(sky_concat("Expected ", sky_concat(lex, sky_concat(" but got '", sky_concat(sky_asMap(t)["lexeme"], sky_concat("' at ", sky_concat(sky_stringFromInt(sky_asMap(st)["line"]), sky_concat(":", sky_stringFromInt(sky_asMap(st)["column"]))))))))) }() }() }()
}

func Compiler_ParserCore_TokenKindEq(a any, b any) any {
	return sky_equal(Compiler_ParserCore_TokenKindStr(a), Compiler_ParserCore_TokenKindStr(b))
}

func Compiler_ParserCore_TokenKindStr(k any) any {
	return func() any { return func() any { __subject := k; if sky_asMap(__subject)["SkyName"] == "TkIdentifier" { return "Identifier" };  if sky_asMap(__subject)["SkyName"] == "TkUpperIdentifier" { return "UpperIdentifier" };  if sky_asMap(__subject)["SkyName"] == "TkInteger" { return "Integer" };  if sky_asMap(__subject)["SkyName"] == "TkFloat" { return "Float" };  if sky_asMap(__subject)["SkyName"] == "TkString" { return "String" };  if sky_asMap(__subject)["SkyName"] == "TkChar" { return "Char" };  if sky_asMap(__subject)["SkyName"] == "TkKeyword" { return "Keyword" };  if sky_asMap(__subject)["SkyName"] == "TkOperator" { return "Operator" };  if sky_asMap(__subject)["SkyName"] == "TkEquals" { return "=" };  if sky_asMap(__subject)["SkyName"] == "TkColon" { return ":" };  if sky_asMap(__subject)["SkyName"] == "TkComma" { return "," };  if sky_asMap(__subject)["SkyName"] == "TkDot" { return "." };  if sky_asMap(__subject)["SkyName"] == "TkPipe" { return "|" };  if sky_asMap(__subject)["SkyName"] == "TkArrow" { return "->" };  if sky_asMap(__subject)["SkyName"] == "TkBackslash" { return "\\" };  if sky_asMap(__subject)["SkyName"] == "TkLParen" { return "(" };  if sky_asMap(__subject)["SkyName"] == "TkRParen" { return ")" };  if sky_asMap(__subject)["SkyName"] == "TkLBracket" { return "[" };  if sky_asMap(__subject)["SkyName"] == "TkRBracket" { return "]" };  if sky_asMap(__subject)["SkyName"] == "TkLBrace" { return "{" };  if sky_asMap(__subject)["SkyName"] == "TkRBrace" { return "}" };  if sky_asMap(__subject)["SkyName"] == "TkNewline" { return "newline" };  if sky_asMap(__subject)["SkyName"] == "TkIndent" { return "indent" };  if sky_asMap(__subject)["SkyName"] == "TkDedent" { return "dedent" };  if sky_asMap(__subject)["SkyName"] == "TkEOF" { return "EOF" };  return nil }() }()
}

func Compiler_ParserCore_ParseQualifiedParts(parts any, state any) any {
	return func() any { if sky_asBool(Compiler_ParserCore_MatchKind(TkDot, state)) { return func() any { __tup_dotTok_s1 := Compiler_ParserCore_Advance(state); dotTok := sky_asTuple2(__tup_dotTok_s1).V0; _ = dotTok; s1 := sky_asTuple2(__tup_dotTok_s1).V1; _ = s1; return func() any { if sky_asBool(sky_asBool(Compiler_ParserCore_MatchKind(TkUpperIdentifier, s1)) || sky_asBool(Compiler_ParserCore_MatchKind(TkIdentifier, s1))) { return func() any { __tup_tok_s2 := Compiler_ParserCore_Advance(s1); tok := sky_asTuple2(__tup_tok_s2).V0; _ = tok; s2 := sky_asTuple2(__tup_tok_s2).V1; _ = s2; return Compiler_ParserCore_ParseQualifiedParts(sky_call(sky_listAppend(parts), []any{sky_asMap(tok)["lexeme"]}), s2) }() }; return SkyTuple2{V0: parts, V1: state} }() }() }; return SkyTuple2{V0: parts, V1: state} }()
}

func Compiler_ParserCore_PeekLexeme(state any) any {
	return func() any { t := Compiler_ParserCore_Peek(state); _ = t; return sky_asMap(t)["lexeme"] }()
}

func Compiler_ParserCore_PeekColumn(state any) any {
	return func() any { t := Compiler_ParserCore_Peek(state); _ = t; sp := sky_asMap(t)["span"]; _ = sp; st := sky_asMap(sp)["start"]; _ = st; return sky_asMap(st)["column"] }()
}

func Compiler_ParserCore_PeekKind(state any) any {
	return func() any { t := Compiler_ParserCore_Peek(state); _ = t; return sky_asMap(t)["kind"] }()
}

func Compiler_ParserCore_PeekAt1Kind(state any) any {
	return func() any { t := Compiler_ParserCore_PeekAt(1, state); _ = t; return sky_asMap(t)["kind"] }()
}

func Compiler_ParserCore_UnescapeString(s any) any {
	return func() any { s1 := sky_call2(sky_stringReplace("\\\\"), "\\", s); _ = s1; s2 := sky_call2(sky_stringReplace("\\\""), "\"", s1); _ = s2; s3 := sky_call2(sky_stringReplace("\\n"), "\n", s2); _ = s3; return sky_call2(sky_stringReplace("\\t"), "\t", s3) }()
}

func Compiler_ParserCore_FilterLayout(tokens any) any {
	return sky_call(sky_listFilter(func(t any) any { return func() any { return func() any { __subject := sky_asMap(t)["kind"]; if sky_asMap(__subject)["SkyName"] == "TkNewline" { return false };  if sky_asMap(__subject)["SkyName"] == "TkIndent" { return false };  if sky_asMap(__subject)["SkyName"] == "TkDedent" { return false };  if true { return true };  return nil }() }() }), tokens)
}

var parseExpr = Compiler_ParserExpr_ParseExpr

var ParseExpr = Compiler_ParserExpr_ParseExpr

var parseExprLoop = Compiler_ParserExpr_ParseExprLoop

var ParseExprLoop = Compiler_ParserExpr_ParseExprLoop

var getOperatorInfo = Compiler_ParserExpr_GetOperatorInfo

var GetOperatorInfo = Compiler_ParserExpr_GetOperatorInfo

var parseApplication = Compiler_ParserExpr_ParseApplication

var ParseApplication = Compiler_ParserExpr_ParseApplication

var parseApplicationArgs = Compiler_ParserExpr_ParseApplicationArgs

var ParseApplicationArgs = Compiler_ParserExpr_ParseApplicationArgs

var isStartOfPrimary = Compiler_ParserExpr_IsStartOfPrimary

var IsStartOfPrimary = Compiler_ParserExpr_IsStartOfPrimary

var parsePrimary = Compiler_ParserExpr_ParsePrimary

var ParsePrimary = Compiler_ParserExpr_ParsePrimary

var parseCaseExpr = Compiler_ParserExpr_ParseCaseExpr

var ParseCaseExpr = Compiler_ParserExpr_ParseCaseExpr

var parseCaseBranches = Compiler_ParserExpr_ParseCaseBranches

var ParseCaseBranches = Compiler_ParserExpr_ParseCaseBranches

var parseIfExpr = Compiler_ParserExpr_ParseIfExpr

var ParseIfExpr = Compiler_ParserExpr_ParseIfExpr

var parseLetExpr = Compiler_ParserExpr_ParseLetExpr

var ParseLetExpr = Compiler_ParserExpr_ParseLetExpr

var parseLetBindings = Compiler_ParserExpr_ParseLetBindings

var ParseLetBindings = Compiler_ParserExpr_ParseLetBindings

var parseLambdaExpr = Compiler_ParserExpr_ParseLambdaExpr

var ParseLambdaExpr = Compiler_ParserExpr_ParseLambdaExpr

var parseLambdaParams = Compiler_ParserExpr_ParseLambdaParams

var ParseLambdaParams = Compiler_ParserExpr_ParseLambdaParams

var parseRecordOrUpdate = Compiler_ParserExpr_ParseRecordOrUpdate

var ParseRecordOrUpdate = Compiler_ParserExpr_ParseRecordOrUpdate

var parseRecordFields = Compiler_ParserExpr_ParseRecordFields

var ParseRecordFields = Compiler_ParserExpr_ParseRecordFields

var parseParenOrTuple = Compiler_ParserExpr_ParseParenOrTuple

var ParseParenOrTuple = Compiler_ParserExpr_ParseParenOrTuple

var parseTupleRest = Compiler_ParserExpr_ParseTupleRest

var ParseTupleRest = Compiler_ParserExpr_ParseTupleRest

var parseListExpr = Compiler_ParserExpr_ParseListExpr

var ParseListExpr = Compiler_ParserExpr_ParseListExpr

var parseListItems = Compiler_ParserExpr_ParseListItems

var ParseListItems = Compiler_ParserExpr_ParseListItems

var parseQualifiedOrConstructor = Compiler_ParserExpr_ParseQualifiedOrConstructor

var ParseQualifiedOrConstructor = Compiler_ParserExpr_ParseQualifiedOrConstructor

var parseFieldAccess = Compiler_ParserExpr_ParseFieldAccess

var ParseFieldAccess = Compiler_ParserExpr_ParseFieldAccess

var parsePatternExpr = Compiler_ParserPattern_ParsePatternExpr

var ParsePatternExpr = Compiler_ParserPattern_ParsePatternExpr

var parsePrimaryPattern = Compiler_ParserPattern_ParsePrimaryPattern

var ParsePrimaryPattern = Compiler_ParserPattern_ParsePrimaryPattern

var parsePrimaryPatternUpper = Compiler_ParserPattern_ParsePrimaryPatternUpper

var ParsePrimaryPatternUpper = Compiler_ParserPattern_ParsePrimaryPatternUpper

var parsePrimaryPatternIdent = Compiler_ParserPattern_ParsePrimaryPatternIdent

var ParsePrimaryPatternIdent = Compiler_ParserPattern_ParsePrimaryPatternIdent

var parsePrimaryPatternInt = Compiler_ParserPattern_ParsePrimaryPatternInt

var ParsePrimaryPatternInt = Compiler_ParserPattern_ParsePrimaryPatternInt

var parsePrimaryPatternString = Compiler_ParserPattern_ParsePrimaryPatternString

var ParsePrimaryPatternString = Compiler_ParserPattern_ParsePrimaryPatternString

var parsePrimaryPatternParen = Compiler_ParserPattern_ParsePrimaryPatternParen

var ParsePrimaryPatternParen = Compiler_ParserPattern_ParsePrimaryPatternParen

var parsePrimaryPatternParenCont = Compiler_ParserPattern_ParsePrimaryPatternParenCont

var ParsePrimaryPatternParenCont = Compiler_ParserPattern_ParsePrimaryPatternParenCont

var parsePrimaryPatternBracket = Compiler_ParserPattern_ParsePrimaryPatternBracket

var ParsePrimaryPatternBracket = Compiler_ParserPattern_ParsePrimaryPatternBracket

var parsePatternArgs = Compiler_ParserPattern_ParsePatternArgs

var ParsePatternArgs = Compiler_ParserPattern_ParsePatternArgs

var parseTuplePatternRest = Compiler_ParserPattern_ParseTuplePatternRest

var ParseTuplePatternRest = Compiler_ParserPattern_ParseTuplePatternRest

var parsePatternList = Compiler_ParserPattern_ParsePatternList

var ParsePatternList = Compiler_ParserPattern_ParsePatternList

func Compiler_Parser_DispatchDeclaration(first any, second any, state any) any {
	return func() any { if sky_asBool(sky_asBool(sky_equal(first, "foreign")) && sky_asBool(sky_equal(second, "import"))) { return Compiler_Parser_ParseForeignImport(state) }; return func() any { if sky_asBool(sky_asBool(sky_equal(first, "type")) && sky_asBool(sky_equal(second, "alias"))) { return Compiler_Parser_ParseTypeAlias(state) }; return func() any { if sky_asBool(sky_equal(first, "type")) { return Compiler_Parser_ParseTypeDecl(state) }; return func() any { if sky_asBool(sky_asBool(matchKind(TkIdentifier, state)) && sky_asBool(tokenKindEq(peekAt1Kind(state), TkColon))) { return Compiler_Parser_ParseTypeAnnot(state) }; return func() any { if sky_asBool(matchKind(TkIdentifier, state)) { return Compiler_Parser_ParseFunDecl(state) }; return SkyErr(sky_concat("Unexpected token: ", first)) }() }() }() }() }()
}

func Compiler_Parser_ParseVariantFields(state any) any {
	return func() any { if sky_asBool(sky_asBool(matchKind(TkUpperIdentifier, state)) || sky_asBool(sky_asBool(matchKind(TkIdentifier, state)) || sky_asBool(sky_asBool(matchKind(TkLParen, state)) || sky_asBool(matchKind(TkLBrace, state))))) { return func() any { if sky_asBool(sky_asBool(sky_equal(peekColumn(state), 1)) || sky_asBool(matchKind(TkPipe, state))) { return SkyTuple2{V0: []any{}, V1: state} }; return func() any { return func() any { __subject := Compiler_Parser_ParseTypePrimary(state); if sky_asSkyResult(__subject).SkyName == "Ok" { te := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = te; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { __tup_rest_s2 := Compiler_Parser_ParseVariantFields(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return SkyTuple2{V0: append([]any{te}, sky_asList(rest)...), V1: s2} }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: []any{}, V1: state} };  return nil }() }() }() }; return SkyTuple2{V0: []any{}, V1: state} }()
}

func Compiler_Parser_ParseTypeArgs(state any) any {
	return func() any { if sky_asBool(sky_asBool(matchKind(TkUpperIdentifier, state)) || sky_asBool(sky_asBool(matchKind(TkIdentifier, state)) || sky_asBool(sky_asBool(matchKind(TkLParen, state)) || sky_asBool(matchKind(TkLBrace, state))))) { return func() any { if sky_asBool(sky_asBool(sky_equal(peekColumn(state), 1)) || sky_asBool(sky_asBool(matchKind(TkEquals, state)) || sky_asBool(matchKind(TkPipe, state)))) { return SkyTuple2{V0: []any{}, V1: state} }; return func() any { return func() any { __subject := Compiler_Parser_ParseTypePrimary(state); if sky_asSkyResult(__subject).SkyName == "Ok" { te := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = te; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { __tup_rest_s2 := Compiler_Parser_ParseTypeArgs(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return SkyTuple2{V0: append([]any{te}, sky_asList(rest)...), V1: s2} }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: []any{}, V1: state} };  return nil }() }() }() }; return SkyTuple2{V0: []any{}, V1: state} }()
}

func Compiler_Parser_Parse(tokens any) any {
	return func() any { state := initState(filterLayout(tokens)); _ = state; return func() any { return func() any { __subject := Compiler_Parser_ParseModule(state); if sky_asSkyResult(__subject).SkyName == "Ok" { mod := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = mod; return SkyOk(mod) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  return nil }() }() }()
}

func Compiler_Parser_ParseModule(state any) any {
	return func() any { return func() any { __subject := consumeLex(TkKeyword, "module", state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { return func() any { __subject := Compiler_Parser_ParseModuleName(s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { name := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = name; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { __tup_exposing__s3 := Compiler_Parser_ParseOptionalExposing(s2); exposing_ := sky_asTuple2(__tup_exposing__s3).V0; _ = exposing_; s3 := sky_asTuple2(__tup_exposing__s3).V1; _ = s3; __tup_imports_s4 := Compiler_Parser_ParseImports(s3); imports := sky_asTuple2(__tup_imports_s4).V0; _ = imports; s4 := sky_asTuple2(__tup_imports_s4).V1; _ = s4; __tup_decls_s5 := Compiler_Parser_ParseDeclarations(s4); decls := sky_asTuple2(__tup_decls_s5).V0; _ = decls; s5 := sky_asTuple2(__tup_decls_s5).V1; _ = s5; return SkyOk(SkyTuple2{V0: map[string]any{"name": name, "exposing_": exposing_, "imports": imports, "declarations": decls, "span": emptySpan}, V1: s5}) }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Parser_ParseModuleName(state any) any {
	return func() any { return func() any { __subject := consume(TkUpperIdentifier, state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { first := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = first; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return Compiler_Parser_ParseModuleNameParts([]any{sky_asMap(first)["lexeme"]}, s1) };  return nil }() }()
}

func Compiler_Parser_ParseModuleNameParts(parts any, state any) any {
	return func() any { if sky_asBool(matchKind(TkDot, state)) { return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := consume(TkUpperIdentifier, s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { part := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = part; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return Compiler_Parser_ParseModuleNameParts(sky_concat(parts, []any{sky_asMap(part)["lexeme"]}), s2) };  return nil }() }() }() }; return SkyOk(SkyTuple2{V0: parts, V1: state}) }()
}

func Compiler_Parser_ParseOptionalExposing(state any) any {
	return func() any { if sky_asBool(matchKindLex(TkKeyword, "exposing", state)) { return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := Compiler_Parser_ParseExposingClause(s1); if sky_asSkyResult(__subject).SkyName == "Ok" { ec := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = ec; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return SkyTuple2{V0: ec, V1: s2} };  if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: ExposeNone, V1: state} };  return nil }() }() }() }; return SkyTuple2{V0: ExposeNone, V1: state} }()
}

func Compiler_Parser_ParseExposingClause(state any) any {
	return func() any { return func() any { __subject := consume(TkLParen, state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkDot, s1)) { return func() any { __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; __tup_w_s3 := advance(s2); s3 := sky_asTuple2(__tup_w_s3).V1; _ = s3; return func() any { return func() any { __subject := consume(TkRParen, s3); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s4 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s4; return SkyOk(SkyTuple2{V0: ExposeAll, V1: s4}) };  return nil }() }() }() }; return func() any { __tup_items_s2 := Compiler_Parser_ParseExposedItems([]any{}, s1); items := sky_asTuple2(__tup_items_s2).V0; _ = items; s2 := sky_asTuple2(__tup_items_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkRParen, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk(SkyTuple2{V0: ExposeList(items), V1: s3}) };  return nil }() }() }() }() };  return nil }() }()
}

func Compiler_Parser_ParseExposedItems(items any, state any) any {
	return func() any { if sky_asBool(matchKind(TkRParen, state)) { return SkyTuple2{V0: items, V1: state} }; return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; newItems := sky_concat(items, []any{sky_asMap(tok)["lexeme"]}); _ = newItems; s2 := func() any { if sky_asBool(matchKind(TkComma, s1)) { return func() any { __tup_w_s := advance(s1); s := sky_asTuple2(__tup_w_s).V1; _ = s; return s }() }; return s1 }(); _ = s2; return Compiler_Parser_ParseExposedItems(newItems, s2) }() }()
}

func Compiler_Parser_ParseImports(state any) any {
	return func() any { if sky_asBool(matchKindLex(TkKeyword, "import", state)) { return func() any { return func() any { __subject := Compiler_Parser_ParseImport(state); if sky_asSkyResult(__subject).SkyName == "Ok" { imp := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = imp; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { __tup_rest_s2 := Compiler_Parser_ParseImports(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return SkyTuple2{V0: append([]any{imp}, sky_asList(rest)...), V1: s2} }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: []any{}, V1: state} };  return nil }() }() }; return SkyTuple2{V0: []any{}, V1: state} }()
}

func Compiler_Parser_ParseImport(state any) any {
	return func() any { return func() any { __subject := consumeLex(TkKeyword, "import", state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { return func() any { __subject := Compiler_Parser_ParseModuleName(s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { modName := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = modName; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { __tup_alias__s3 := func() any { if sky_asBool(matchKindLex(TkKeyword, "as", s2)) { return func() any { __tup_w_sa := advance(s2); sa := sky_asTuple2(__tup_w_sa).V1; _ = sa; __tup_tok_sb := advance(sa); tok := sky_asTuple2(__tup_tok_sb).V0; _ = tok; sb := sky_asTuple2(__tup_tok_sb).V1; _ = sb; return SkyTuple2{V0: sky_asMap(tok)["lexeme"], V1: sb} }() }; return SkyTuple2{V0: "", V1: s2} }(); alias_ := sky_asTuple2(__tup_alias__s3).V0; _ = alias_; s3 := sky_asTuple2(__tup_alias__s3).V1; _ = s3; __tup_exposing__s4 := Compiler_Parser_ParseOptionalExposing(s3); exposing_ := sky_asTuple2(__tup_exposing__s4).V0; _ = exposing_; s4 := sky_asTuple2(__tup_exposing__s4).V1; _ = s4; return SkyOk(SkyTuple2{V0: map[string]any{"moduleName": modName, "alias_": alias_, "exposing_": exposing_, "span": emptySpan}, V1: s4}) }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Parser_GetLexemeAt1(state any) any {
	return peekAt(1, state)
}

func Compiler_Parser_ParseDeclaration(state any) any {
	return Compiler_Parser_DispatchDeclaration(peekLexeme(state), Compiler_Parser_GetLexemeAt1(state), state)
}

func Compiler_Parser_ParseDeclarations(state any) any {
	return func() any { if sky_asBool(matchKind(TkEOF, state)) { return SkyTuple2{V0: []any{}, V1: state} }; return Compiler_Parser_ParseDeclsHelper(Compiler_Parser_ParseDeclaration(state), state) }()
}

func Compiler_Parser_ParseDeclsHelper(result any, origState any) any {
	return func() any { return func() any { __subject := result; if sky_asSkyResult(__subject).SkyName == "Ok" { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return Compiler_Parser_AddDeclAndContinue(sky_fst(pair), sky_snd(pair)) };  if sky_asSkyResult(__subject).SkyName == "Err" { return Compiler_Parser_ParseDeclarations(sky_snd(advance(origState))) };  return nil }() }()
}

func Compiler_Parser_AddDeclAndContinue(decl any, s1 any) any {
	return Compiler_Parser_PrependToResult(decl, Compiler_Parser_ParseDeclarations(s1))
}

func Compiler_Parser_PrependToResult(decl any, result any) any {
	return SkyTuple2{V0: append([]any{decl}, sky_asList(sky_fst(result))...), V1: sky_snd(result)}
}

func Compiler_Parser_ParseForeignImport(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkString, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { pkgToken := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = pkgToken; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { pkgName := sky_call2(sky_stringSlice(1), sky_asInt(sky_stringLength(sky_asMap(pkgToken)["lexeme"])) - sky_asInt(1), sky_asMap(pkgToken)["lexeme"]); _ = pkgName; __tup_exposing__s4 := Compiler_Parser_ParseOptionalExposing(s3); exposing_ := sky_asTuple2(__tup_exposing__s4).V0; _ = exposing_; s4 := sky_asTuple2(__tup_exposing__s4).V1; _ = s4; names := func() any { return func() any { __subject := exposing_; if sky_asMap(__subject)["SkyName"] == "ExposeList" { items := sky_asMap(__subject)["V0"]; _ = items; return items };  if true { return []any{} };  return nil }() }(); _ = names; firstDecl := func() any { return func() any { __subject := sky_listHead(names); if sky_asSkyMaybe(__subject).SkyName == "Just" { n := sky_asSkyMaybe(__subject).JustValue; _ = n; return SkyOk(SkyTuple2{V0: ForeignImportDecl(n, pkgName, n, emptySpan), V1: s4}) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyErr("Foreign import must expose at least one name") };  return nil }() }(); _ = firstDecl; return firstDecl }() };  return nil }() }() }()
}

func Compiler_Parser_ParseTypeAlias(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkUpperIdentifier, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { name := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = name; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { __tup_params_s4 := Compiler_Parser_ParseTypeParams(s3); params := sky_asTuple2(__tup_params_s4).V0; _ = params; s4 := sky_asTuple2(__tup_params_s4).V1; _ = s4; return func() any { return func() any { __subject := consume(TkEquals, s4); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s5 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s5; return func() any { return func() any { __subject := Compiler_Parser_ParseTypeExpr(s5); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { typeExpr := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = typeExpr; s6 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s6; return SkyOk(SkyTuple2{V0: TypeAliasDecl(sky_asMap(name)["lexeme"], params, typeExpr, emptySpan), V1: s6}) };  return nil }() }() };  return nil }() }() }() };  return nil }() }() }()
}

func Compiler_Parser_ParseTypeDecl(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := consume(TkUpperIdentifier, s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { name := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = name; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { __tup_params_s3 := Compiler_Parser_ParseTypeParams(s2); params := sky_asTuple2(__tup_params_s3).V0; _ = params; s3 := sky_asTuple2(__tup_params_s3).V1; _ = s3; return func() any { return func() any { __subject := consume(TkEquals, s3); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s4 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s4; return func() any { __tup_variants_s5 := Compiler_Parser_ParseTypeVariants(s4); variants := sky_asTuple2(__tup_variants_s5).V0; _ = variants; s5 := sky_asTuple2(__tup_variants_s5).V1; _ = s5; return SkyOk(SkyTuple2{V0: TypeDecl(sky_asMap(name)["lexeme"], params, variants, emptySpan), V1: s5}) }() };  return nil }() }() }() };  return nil }() }() }()
}

func Compiler_Parser_ParseTypeParams(state any) any {
	return func() any { if sky_asBool(matchKind(TkIdentifier, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; __tup_rest_s2 := Compiler_Parser_ParseTypeParams(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return SkyTuple2{V0: append([]any{sky_asMap(tok)["lexeme"]}, sky_asList(rest)...), V1: s2} }() }; return SkyTuple2{V0: []any{}, V1: state} }()
}

func Compiler_Parser_ParseTypeVariants(state any) any {
	return func() any { return func() any { __subject := consume(TkUpperIdentifier, state); if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: []any{}, V1: state} };  if sky_asSkyResult(__subject).SkyName == "Ok" { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return Compiler_Parser_BuildVariant(sky_fst(pair), sky_snd(pair)) };  return nil }() }()
}

func Compiler_Parser_BuildVariant(name any, s1 any) any {
	return Compiler_Parser_FinishVariant(sky_asMap(name)["lexeme"], Compiler_Parser_ParseVariantFields(s1))
}

func Compiler_Parser_FinishVariant(variantName any, fieldResult any) any {
	return func() any { if sky_asBool(matchKind(TkPipe, sky_snd(fieldResult))) { return Compiler_Parser_PrependVariant(map[string]any{"name": variantName, "fields": sky_fst(fieldResult), "span": emptySpan}, Compiler_Parser_ParseTypeVariants(sky_snd(advance(sky_snd(fieldResult))))) }; return SkyTuple2{V0: []any{map[string]any{"name": variantName, "fields": sky_fst(fieldResult), "span": emptySpan}}, V1: sky_snd(fieldResult)} }()
}

func Compiler_Parser_PrependVariant(v any, rest any) any {
	return SkyTuple2{V0: append([]any{v}, sky_asList(sky_fst(rest))...), V1: sky_snd(rest)}
}

func Compiler_Parser_ParseTypeExpr(state any) any {
	return func() any { return func() any { __subject := Compiler_Parser_ParseTypeApp(state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { left := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = left; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkArrow, s1)) { return func() any { __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; return func() any { return func() any { __subject := Compiler_Parser_ParseTypeExpr(s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { right := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = right; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk(SkyTuple2{V0: FunType(left, right, emptySpan), V1: s3}) };  return nil }() }() }() }; return SkyOk(SkyTuple2{V0: left, V1: s1}) }() };  return nil }() }()
}

func Compiler_Parser_ParseTypeApp(state any) any {
	return func() any { return func() any { __subject := Compiler_Parser_ParseTypePrimary(state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return Compiler_Parser_ApplyTypeArgs(sky_fst(pair), sky_snd(pair)) };  return nil }() }()
}

func Compiler_Parser_ApplyTypeArgs(target any, s1 any) any {
	return Compiler_Parser_ResolveTypeApp(target, Compiler_Parser_ParseTypeArgs(s1))
}

func Compiler_Parser_ResolveTypeApp(target any, argsResult any) any {
	return func() any { if sky_asBool(sky_listIsEmpty(sky_fst(argsResult))) { return SkyOk(SkyTuple2{V0: target, V1: sky_snd(argsResult)}) }; return func() any { return func() any { __subject := target; if sky_asMap(__subject)["SkyName"] == "TypeRef" { name := sky_asMap(__subject)["V0"]; _ = name; span := sky_asMap(__subject)["V2"]; _ = span; return SkyOk(SkyTuple2{V0: TypeRef(name, sky_fst(argsResult), span), V1: sky_snd(argsResult)}) };  if true { return SkyOk(SkyTuple2{V0: target, V1: sky_snd(argsResult)}) };  return nil }() }() }()
}

func Compiler_Parser_ParseTypePrimary(state any) any {
	return func() any { if sky_asBool(matchKind(TkUpperIdentifier, state)) { return func() any { __tup_id_s1 := advance(state); id := sky_asTuple2(__tup_id_s1).V0; _ = id; s1 := sky_asTuple2(__tup_id_s1).V1; _ = s1; __tup_parts_s2 := parseQualifiedParts([]any{sky_asMap(id)["lexeme"]}, s1); parts := sky_asTuple2(__tup_parts_s2).V0; _ = parts; s2 := sky_asTuple2(__tup_parts_s2).V1; _ = s2; return SkyOk(SkyTuple2{V0: TypeRef(parts, []any{}, emptySpan), V1: s2}) }() }; return func() any { if sky_asBool(matchKind(TkIdentifier, state)) { return func() any { __tup_id_s1 := advance(state); id := sky_asTuple2(__tup_id_s1).V0; _ = id; s1 := sky_asTuple2(__tup_id_s1).V1; _ = s1; return SkyOk(SkyTuple2{V0: TypeVar(sky_asMap(id)["lexeme"], emptySpan), V1: s1}) }() }; return func() any { if sky_asBool(matchKind(TkLParen, state)) { return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkRParen, s1)) { return func() any { __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; return SkyOk(SkyTuple2{V0: UnitTypeExpr(emptySpan), V1: s2}) }() }; return func() any { return func() any { __subject := Compiler_Parser_ParseTypeExpr(s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { inner := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = inner; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { if sky_asBool(matchKind(TkComma, s2)) { return Compiler_Parser_ParseTupleTypeRest([]any{inner}, s2) }; return func() any { return func() any { __subject := consume(TkRParen, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk(SkyTuple2{V0: inner, V1: s3}) };  return nil }() }() }() };  return nil }() }() }() }() }; return func() any { if sky_asBool(matchKind(TkLBrace, state)) { return Compiler_Parser_ParseRecordType(state) }; return SkyErr(sky_concat("Unexpected token in type: ", peekLexeme(state))) }() }() }() }()
}

func Compiler_Parser_ParseTupleTypeRest(items any, state any) any {
	return func() any { if sky_asBool(matchKind(TkComma, state)) { return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := Compiler_Parser_ParseTypeExpr(s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { item := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = item; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return Compiler_Parser_ParseTupleTypeRest(sky_concat(items, []any{item}), s2) };  return nil }() }() }() }; return func() any { return func() any { __subject := consume(TkRParen, state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return SkyOk(SkyTuple2{V0: TupleTypeExpr(items, emptySpan), V1: s1}) };  return nil }() }() }()
}

func Compiler_Parser_ParseRecordType(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; __tup_fields_s2 := Compiler_Parser_ParseRecordTypeFields([]any{}, s1); fields := sky_asTuple2(__tup_fields_s2).V0; _ = fields; s2 := sky_asTuple2(__tup_fields_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkRBrace, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk(SkyTuple2{V0: RecordTypeExpr(fields, emptySpan), V1: s3}) };  return nil }() }() }()
}

func Compiler_Parser_ParseRecordTypeFields(fields any, state any) any {
	return func() any { if sky_asBool(matchKind(TkRBrace, state)) { return SkyTuple2{V0: fields, V1: state} }; return func() any { return func() any { __subject := consume(TkIdentifier, state); if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: fields, V1: state} };  if sky_asSkyResult(__subject).SkyName == "Ok" { name := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = name; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { return func() any { __subject := consume(TkColon, s1); if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: fields, V1: state} };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := Compiler_Parser_ParseTypeExpr(s2); if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: fields, V1: state} };  if sky_asSkyResult(__subject).SkyName == "Ok" { te := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = te; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { newFields := sky_concat(fields, []any{map[string]any{"name": sky_asMap(name)["lexeme"], "type_": te}}); _ = newFields; s4 := func() any { if sky_asBool(matchKind(TkComma, s3)) { return func() any { __tup_w_s := advance(s3); s := sky_asTuple2(__tup_w_s).V1; _ = s; return s }() }; return s3 }(); _ = s4; return Compiler_Parser_ParseRecordTypeFields(newFields, s4) }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }()
}

func Compiler_Parser_ParseTypeAnnot(state any) any {
	return func() any { __tup_name_s1 := advance(state); name := sky_asTuple2(__tup_name_s1).V0; _ = name; s1 := sky_asTuple2(__tup_name_s1).V1; _ = s1; return func() any { return func() any { __subject := consume(TkColon, s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := Compiler_Parser_ParseTypeExpr(s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { te := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = te; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk(SkyTuple2{V0: TypeAnnotDecl(sky_asMap(name)["lexeme"], te, emptySpan), V1: s3}) };  return nil }() }() };  return nil }() }() }()
}

func Compiler_Parser_ParseFunDecl(state any) any {
	return func() any { __tup_name_s1 := advance(state); name := sky_asTuple2(__tup_name_s1).V0; _ = name; s1 := sky_asTuple2(__tup_name_s1).V1; _ = s1; __tup_params_s2 := Compiler_Parser_ParseFunParams(s1); params := sky_asTuple2(__tup_params_s2).V0; _ = params; s2 := sky_asTuple2(__tup_params_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkEquals, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s3); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { body := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = body; s4 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s4; return SkyOk(SkyTuple2{V0: FunDecl(sky_asMap(name)["lexeme"], params, body, emptySpan), V1: s4}) };  return nil }() }() };  return nil }() }() }()
}

func Compiler_Parser_ParseFunParams(state any) any {
	return func() any { if sky_asBool(matchKind(TkEquals, state)) { return SkyTuple2{V0: []any{}, V1: state} }; return func() any { if sky_asBool(matchKind(TkIdentifier, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; pat := func() any { if sky_asBool(sky_equal(sky_asMap(tok)["lexeme"], "_")) { return PWildcard(emptySpan) }; return PVariable(sky_asMap(tok)["lexeme"], emptySpan) }(); _ = pat; __tup_rest_s2 := Compiler_Parser_ParseFunParams(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return SkyTuple2{V0: append([]any{pat}, sky_asList(rest)...), V1: s2} }() }; return func() any { if sky_asBool(matchKind(TkLParen, state)) { return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(state); if sky_asSkyResult(__subject).SkyName == "Ok" { pat := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = pat; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { __tup_rest_s2 := Compiler_Parser_ParseFunParams(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return SkyTuple2{V0: append([]any{pat}, sky_asList(rest)...), V1: s2} }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: []any{}, V1: state} };  return nil }() }() }; return SkyTuple2{V0: []any{}, V1: state} }() }() }()
}

func Compiler_Lexer_Lex(source any) any {
	return func() any { initial := map[string]any{"source": source, "offset": 0, "line": 1, "column": 1, "tokens": []any{}, "indentStack": []any{0}}; _ = initial; final := Compiler_Lexer_LexLoop(initial); _ = final; eofToken := map[string]any{"kind": TkEOF, "lexeme": "", "span": Compiler_Lexer_MakeSpan(final)}; _ = eofToken; return map[string]any{"tokens": sky_listReverse(append([]any{eofToken}, sky_asList(sky_asMap(final)["tokens"])...)), "diagnostics": []any{}} }()
}

func Compiler_Lexer_LexLoop(state any) any {
	return func() any { if sky_asBool(sky_asInt(sky_asMap(state)["offset"]) >= sky_asInt(sky_stringLength(sky_asMap(state)["source"]))) { return state }; return func() any { ch := Compiler_Lexer_CharAt(sky_asMap(state)["source"], sky_asMap(state)["offset"]); _ = ch; return func() any { if sky_asBool(sky_equal(ch, string('\n'))) { return Compiler_Lexer_LexLoop(Compiler_Lexer_HandleNewline(state)) }; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, string(' '))) || sky_asBool(sky_equal(ch, string('\t')))) { return Compiler_Lexer_LexLoop(Compiler_Lexer_SkipWhitespace(state)) }; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, string('-'))) && sky_asBool(sky_equal(Compiler_Lexer_PeekChar(sky_asMap(state)["source"], sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(1)), string('-')))) { return Compiler_Lexer_LexLoop(Compiler_Lexer_SkipLineComment(state)) }; return func() any { if sky_asBool(sky_equal(ch, string('"'))) { return Compiler_Lexer_LexLoop(Compiler_Lexer_LexString(state)) }; return func() any { if sky_asBool(sky_equal(ch, string('\''))) { return Compiler_Lexer_LexLoop(Compiler_Lexer_LexChar(state)) }; return func() any { if sky_asBool(sky_charIsDigit(ch)) { return Compiler_Lexer_LexLoop(Compiler_Lexer_LexNumber(state)) }; return func() any { if sky_asBool(sky_asBool(sky_charIsAlpha(ch)) || sky_asBool(sky_equal(ch, string('_')))) { return Compiler_Lexer_LexLoop(Compiler_Lexer_LexIdentifier(state)) }; return Compiler_Lexer_LexLoop(Compiler_Lexer_LexOperatorOrPunctuation(state)) }() }() }() }() }() }() }() }() }()
}

func Compiler_Lexer_HandleNewline(state any) any {
	return func() any { newState := sky_recordUpdate(state, map[string]any{"offset": sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(1), "line": sky_asInt(sky_asMap(state)["line"]) + sky_asInt(1), "column": 1}); _ = newState; indent := Compiler_Lexer_CountIndent(sky_asMap(newState)["source"], sky_asMap(newState)["offset"]); _ = indent; newOffset := sky_asInt(sky_asMap(newState)["offset"]) + sky_asInt(indent); _ = newOffset; nlToken := map[string]any{"kind": TkNewline, "lexeme": "\\n", "span": Compiler_Lexer_MakeSpan(state)}; _ = nlToken; return sky_recordUpdate(newState, map[string]any{"offset": newOffset, "column": sky_asInt(indent) + sky_asInt(1), "tokens": append([]any{nlToken}, sky_asList(sky_asMap(state)["tokens"])...)}) }()
}

func Compiler_Lexer_CountIndent(source any, offset any) any {
	return func() any { if sky_asBool(sky_asInt(offset) >= sky_asInt(sky_stringLength(source))) { return 0 }; return func() any { ch := Compiler_Lexer_CharAt(source, offset); _ = ch; return func() any { if sky_asBool(sky_equal(ch, string(' '))) { return sky_asInt(1) + sky_asInt(Compiler_Lexer_CountIndent(source, sky_asInt(offset) + sky_asInt(1))) }; return func() any { if sky_asBool(sky_equal(ch, string('\t'))) { return sky_asInt(4) + sky_asInt(Compiler_Lexer_CountIndent(source, sky_asInt(offset) + sky_asInt(1))) }; return 0 }() }() }() }()
}

func Compiler_Lexer_SkipWhitespace(state any) any {
	return func() any { if sky_asBool(sky_asInt(sky_asMap(state)["offset"]) >= sky_asInt(sky_stringLength(sky_asMap(state)["source"]))) { return state }; return func() any { ch := Compiler_Lexer_CharAt(sky_asMap(state)["source"], sky_asMap(state)["offset"]); _ = ch; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, string(' '))) || sky_asBool(sky_equal(ch, string('\t')))) { return Compiler_Lexer_SkipWhitespace(sky_recordUpdate(state, map[string]any{"offset": sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(1), "column": sky_asInt(sky_asMap(state)["column"]) + sky_asInt(1)})) }; return state }() }() }()
}

func Compiler_Lexer_SkipLineComment(state any) any {
	return func() any { if sky_asBool(sky_asInt(sky_asMap(state)["offset"]) >= sky_asInt(sky_stringLength(sky_asMap(state)["source"]))) { return state }; return func() any { ch := Compiler_Lexer_CharAt(sky_asMap(state)["source"], sky_asMap(state)["offset"]); _ = ch; return func() any { if sky_asBool(sky_equal(ch, string('\n'))) { return state }; return Compiler_Lexer_SkipLineComment(sky_recordUpdate(state, map[string]any{"offset": sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(1), "column": sky_asInt(sky_asMap(state)["column"]) + sky_asInt(1)})) }() }() }()
}

func Compiler_Lexer_LexString(state any) any {
	return func() any { startOffset := sky_asMap(state)["offset"]; _ = startOffset; startCol := sky_asMap(state)["column"]; _ = startCol; s1 := sky_recordUpdate(state, map[string]any{"offset": sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(1), "column": sky_asInt(sky_asMap(state)["column"]) + sky_asInt(1)}); _ = s1; s2 := Compiler_Lexer_LexStringBody(s1); _ = s2; s3 := sky_recordUpdate(s2, map[string]any{"offset": sky_asInt(sky_asMap(s2)["offset"]) + sky_asInt(1), "column": sky_asInt(sky_asMap(s2)["column"]) + sky_asInt(1)}); _ = s3; lexeme := sky_call2(sky_stringSlice(startOffset), sky_asMap(s3)["offset"], sky_asMap(state)["source"]); _ = lexeme; token := map[string]any{"kind": TkString, "lexeme": lexeme, "span": map[string]any{"start": map[string]any{"offset": startOffset, "line": sky_asMap(state)["line"], "column": startCol}, "end": map[string]any{"offset": sky_asMap(s3)["offset"], "line": sky_asMap(s3)["line"], "column": sky_asMap(s3)["column"]}}}; _ = token; return sky_recordUpdate(s3, map[string]any{"tokens": append([]any{token}, sky_asList(sky_asMap(s3)["tokens"])...)}) }()
}

func Compiler_Lexer_LexStringBody(state any) any {
	return func() any { if sky_asBool(sky_asInt(sky_asMap(state)["offset"]) >= sky_asInt(sky_stringLength(sky_asMap(state)["source"]))) { return state }; return func() any { ch := Compiler_Lexer_CharAt(sky_asMap(state)["source"], sky_asMap(state)["offset"]); _ = ch; return func() any { if sky_asBool(sky_equal(ch, string('"'))) { return state }; return func() any { if sky_asBool(sky_equal(ch, string('\\'))) { return Compiler_Lexer_LexStringBody(sky_recordUpdate(state, map[string]any{"offset": sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(2), "column": sky_asInt(sky_asMap(state)["column"]) + sky_asInt(2)})) }; return Compiler_Lexer_LexStringBody(sky_recordUpdate(state, map[string]any{"offset": sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(1), "column": sky_asInt(sky_asMap(state)["column"]) + sky_asInt(1)})) }() }() }() }()
}

func Compiler_Lexer_LexChar(state any) any {
	return func() any { startOffset := sky_asMap(state)["offset"]; _ = startOffset; startCol := sky_asMap(state)["column"]; _ = startCol; s1 := sky_recordUpdate(state, map[string]any{"offset": sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(1), "column": sky_asInt(sky_asMap(state)["column"]) + sky_asInt(1)}); _ = s1; s2 := func() any { if sky_asBool(sky_equal(Compiler_Lexer_CharAt(sky_asMap(s1)["source"], sky_asMap(s1)["offset"]), string('\\'))) { return sky_recordUpdate(s1, map[string]any{"offset": sky_asInt(sky_asMap(s1)["offset"]) + sky_asInt(2), "column": sky_asInt(sky_asMap(s1)["column"]) + sky_asInt(2)}) }; return sky_recordUpdate(s1, map[string]any{"offset": sky_asInt(sky_asMap(s1)["offset"]) + sky_asInt(1), "column": sky_asInt(sky_asMap(s1)["column"]) + sky_asInt(1)}) }(); _ = s2; s3 := sky_recordUpdate(s2, map[string]any{"offset": sky_asInt(sky_asMap(s2)["offset"]) + sky_asInt(1), "column": sky_asInt(sky_asMap(s2)["column"]) + sky_asInt(1)}); _ = s3; lexeme := sky_call2(sky_stringSlice(startOffset), sky_asMap(s3)["offset"], sky_asMap(state)["source"]); _ = lexeme; token := map[string]any{"kind": TkChar, "lexeme": lexeme, "span": map[string]any{"start": map[string]any{"offset": startOffset, "line": sky_asMap(state)["line"], "column": startCol}, "end": map[string]any{"offset": sky_asMap(s3)["offset"], "line": sky_asMap(s3)["line"], "column": sky_asMap(s3)["column"]}}}; _ = token; return sky_recordUpdate(s3, map[string]any{"tokens": append([]any{token}, sky_asList(sky_asMap(s3)["tokens"])...)}) }()
}

func Compiler_Lexer_LexNumber(state any) any {
	return func() any { startOffset := sky_asMap(state)["offset"]; _ = startOffset; startCol := sky_asMap(state)["column"]; _ = startCol; s1 := Compiler_Lexer_ConsumeDigits(state); _ = s1; result := func() any { if sky_asBool(sky_asBool(sky_asInt(sky_asMap(s1)["offset"]) < sky_asInt(sky_stringLength(sky_asMap(s1)["source"]))) && sky_asBool(sky_equal(Compiler_Lexer_CharAt(sky_asMap(s1)["source"], sky_asMap(s1)["offset"]), string('.')))) { return func() any { s2 := Compiler_Lexer_ConsumeDigits(sky_recordUpdate(s1, map[string]any{"offset": sky_asInt(sky_asMap(s1)["offset"]) + sky_asInt(1), "column": sky_asInt(sky_asMap(s1)["column"]) + sky_asInt(1)})); _ = s2; return SkyTuple2{V0: s2, V1: TkFloat} }() }; return SkyTuple2{V0: s1, V1: TkInteger} }(); _ = result; finalState := sky_fst(result); _ = finalState; kind := sky_snd(result); _ = kind; lexeme := sky_call2(sky_stringSlice(startOffset), sky_asMap(finalState)["offset"], sky_asMap(state)["source"]); _ = lexeme; token := map[string]any{"kind": kind, "lexeme": lexeme, "span": map[string]any{"start": map[string]any{"offset": startOffset, "line": sky_asMap(state)["line"], "column": startCol}, "end": map[string]any{"offset": sky_asMap(finalState)["offset"], "line": sky_asMap(finalState)["line"], "column": sky_asMap(finalState)["column"]}}}; _ = token; return sky_recordUpdate(finalState, map[string]any{"tokens": append([]any{token}, sky_asList(sky_asMap(finalState)["tokens"])...)}) }()
}

func Compiler_Lexer_ConsumeDigits(state any) any {
	return func() any { if sky_asBool(sky_asInt(sky_asMap(state)["offset"]) >= sky_asInt(sky_stringLength(sky_asMap(state)["source"]))) { return state }; return func() any { if sky_asBool(sky_charIsDigit(Compiler_Lexer_CharAt(sky_asMap(state)["source"], sky_asMap(state)["offset"]))) { return Compiler_Lexer_ConsumeDigits(sky_recordUpdate(state, map[string]any{"offset": sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(1), "column": sky_asInt(sky_asMap(state)["column"]) + sky_asInt(1)})) }; return state }() }()
}

func Compiler_Lexer_LexIdentifier(state any) any {
	return func() any { startOffset := sky_asMap(state)["offset"]; _ = startOffset; startCol := sky_asMap(state)["column"]; _ = startCol; s1 := Compiler_Lexer_ConsumeIdentChars(state); _ = s1; lexeme := sky_call2(sky_stringSlice(startOffset), sky_asMap(s1)["offset"], sky_asMap(state)["source"]); _ = lexeme; kind := func() any { if sky_asBool(isKeyword(lexeme)) { return TkKeyword }; return func() any { if sky_asBool(sky_charIsUpper(Compiler_Lexer_CharAt(sky_asMap(state)["source"], startOffset))) { return TkUpperIdentifier }; return TkIdentifier }() }(); _ = kind; token := map[string]any{"kind": kind, "lexeme": lexeme, "span": map[string]any{"start": map[string]any{"offset": startOffset, "line": sky_asMap(state)["line"], "column": startCol}, "end": map[string]any{"offset": sky_asMap(s1)["offset"], "line": sky_asMap(s1)["line"], "column": sky_asMap(s1)["column"]}}}; _ = token; return sky_recordUpdate(s1, map[string]any{"tokens": append([]any{token}, sky_asList(sky_asMap(s1)["tokens"])...)}) }()
}

func Compiler_Lexer_ConsumeIdentChars(state any) any {
	return func() any { if sky_asBool(sky_asInt(sky_asMap(state)["offset"]) >= sky_asInt(sky_stringLength(sky_asMap(state)["source"]))) { return state }; return func() any { ch := Compiler_Lexer_CharAt(sky_asMap(state)["source"], sky_asMap(state)["offset"]); _ = ch; return func() any { if sky_asBool(sky_asBool(sky_charIsAlphaNum(ch)) || sky_asBool(sky_equal(ch, string('_')))) { return Compiler_Lexer_ConsumeIdentChars(sky_recordUpdate(state, map[string]any{"offset": sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(1), "column": sky_asInt(sky_asMap(state)["column"]) + sky_asInt(1)})) }; return state }() }() }()
}

func Compiler_Lexer_LexOperatorOrPunctuation(state any) any {
	return func() any { ch := Compiler_Lexer_CharAt(sky_asMap(state)["source"], sky_asMap(state)["offset"]); _ = ch; next := Compiler_Lexer_PeekChar(sky_asMap(state)["source"], sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(1)); _ = next; startOffset := sky_asMap(state)["offset"]; _ = startOffset; startCol := sky_asMap(state)["column"]; _ = startCol; __tup_kind_len := func() any { if sky_asBool(sky_equal(ch, string('('))) { return SkyTuple2{V0: TkLParen, V1: 1} }; return func() any { if sky_asBool(sky_equal(ch, string(')'))) { return SkyTuple2{V0: TkRParen, V1: 1} }; return func() any { if sky_asBool(sky_equal(ch, string('['))) { return SkyTuple2{V0: TkLBracket, V1: 1} }; return func() any { if sky_asBool(sky_equal(ch, string(']'))) { return SkyTuple2{V0: TkRBracket, V1: 1} }; return func() any { if sky_asBool(sky_equal(ch, string('{'))) { return SkyTuple2{V0: TkLBrace, V1: 1} }; return func() any { if sky_asBool(sky_equal(ch, string('}'))) { return SkyTuple2{V0: TkRBrace, V1: 1} }; return func() any { if sky_asBool(sky_equal(ch, string(','))) { return SkyTuple2{V0: TkComma, V1: 1} }; return func() any { if sky_asBool(sky_equal(ch, string('\\'))) { return SkyTuple2{V0: TkBackslash, V1: 1} }; return func() any { if sky_asBool(sky_equal(ch, string('.'))) { return SkyTuple2{V0: TkDot, V1: 1} }; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, string(':'))) && sky_asBool(!sky_equal(next, string(':')))) { return SkyTuple2{V0: TkColon, V1: 1} }; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, string('='))) && sky_asBool(!sky_equal(next, string('=')))) { return SkyTuple2{V0: TkEquals, V1: 1} }; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, string('|'))) && sky_asBool(sky_asBool(!sky_equal(next, string('>'))) && sky_asBool(!sky_equal(next, string('|'))))) { return SkyTuple2{V0: TkPipe, V1: 1} }; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, string('-'))) && sky_asBool(sky_equal(next, string('>')))) { return SkyTuple2{V0: TkArrow, V1: 2} }; return func() any { opLen := Compiler_Lexer_ConsumeOperator(sky_asMap(state)["source"], sky_asMap(state)["offset"]); _ = opLen; return SkyTuple2{V0: TkOperator, V1: opLen} }() }() }() }() }() }() }() }() }() }() }() }() }() }(); kind := sky_asTuple2(__tup_kind_len).V0; _ = kind; len_ := sky_asTuple2(__tup_kind_len).V1; _ = len_; s1 := sky_recordUpdate(state, map[string]any{"offset": sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(len_), "column": sky_asInt(sky_asMap(state)["column"]) + sky_asInt(len_)}); _ = s1; lexeme := sky_call2(sky_stringSlice(startOffset), sky_asMap(s1)["offset"], sky_asMap(state)["source"]); _ = lexeme; token := map[string]any{"kind": kind, "lexeme": lexeme, "span": map[string]any{"start": map[string]any{"offset": startOffset, "line": sky_asMap(state)["line"], "column": startCol}, "end": map[string]any{"offset": sky_asMap(s1)["offset"], "line": sky_asMap(s1)["line"], "column": sky_asMap(s1)["column"]}}}; _ = token; return sky_recordUpdate(s1, map[string]any{"tokens": append([]any{token}, sky_asList(sky_asMap(s1)["tokens"])...)}) }()
}

func Compiler_Lexer_ConsumeOperator(source any, offset any) any {
	return func() any { if sky_asBool(sky_asInt(offset) >= sky_asInt(sky_stringLength(source))) { return 0 }; return func() any { ch := Compiler_Lexer_CharAt(source, offset); _ = ch; return func() any { if sky_asBool(Compiler_Lexer_IsOperatorChar(ch)) { return sky_asInt(1) + sky_asInt(Compiler_Lexer_ConsumeOperator(source, sky_asInt(offset) + sky_asInt(1))) }; return 0 }() }() }()
}

func Compiler_Lexer_IsOperatorChar(ch any) any {
	return sky_asBool(sky_equal(ch, string('+'))) || sky_asBool(sky_asBool(sky_equal(ch, string('-'))) || sky_asBool(sky_asBool(sky_equal(ch, string('*'))) || sky_asBool(sky_asBool(sky_equal(ch, string('/'))) || sky_asBool(sky_asBool(sky_equal(ch, string('='))) || sky_asBool(sky_asBool(sky_equal(ch, string('<'))) || sky_asBool(sky_asBool(sky_equal(ch, string('>'))) || sky_asBool(sky_asBool(sky_equal(ch, string('!'))) || sky_asBool(sky_asBool(sky_equal(ch, string('&'))) || sky_asBool(sky_asBool(sky_equal(ch, string('|'))) || sky_asBool(sky_asBool(sky_equal(ch, string(':'))) || sky_asBool(sky_asBool(sky_equal(ch, string('%'))) || sky_asBool(sky_asBool(sky_equal(ch, string('^'))) || sky_asBool(sky_equal(ch, string('~')))))))))))))))
}

func Compiler_Lexer_CharAt(s any, i any) any {
	return func() any { c := sky_call2(sky_stringSlice(i), sky_asInt(i) + sky_asInt(1), s); _ = c; return func() any { if sky_asBool(sky_equal(c, "")) { return string(' ') }; return sky_js(c) }() }()
}

func Compiler_Lexer_PeekChar(s any, i any) any {
	return Compiler_Lexer_CharAt(s, i)
}

func Compiler_Lexer_MakeSpan(state any) any {
	return map[string]any{"start": map[string]any{"offset": sky_asMap(state)["offset"], "line": sky_asMap(state)["line"], "column": sky_asMap(state)["column"]}, "end": map[string]any{"offset": sky_asMap(state)["offset"], "line": sky_asMap(state)["line"], "column": sky_asMap(state)["column"]}}
}

var FindCharIdx = Compiler_Pipeline_FindCharIdx

var FindLastSlash = Compiler_Pipeline_FindLastSlash

var Compile = Compiler_Pipeline_Compile

var CompileMultiModule = Compiler_Pipeline_CompileMultiModule

var CompileMultiModuleEntry = Compiler_Pipeline_CompileMultiModuleEntry

var EmitMultiModuleGo = Compiler_Pipeline_EmitMultiModuleGo

var WriteMultiModuleOutput = Compiler_Pipeline_WriteMultiModuleOutput

var OptimiseJsonFunctions = Compiler_Pipeline_OptimiseJsonFunctions

var WriteNativeJsonFile = Compiler_Pipeline_WriteNativeJsonFile

var NativeJsonHelperCode = Compiler_Pipeline_NativeJsonHelperCode

var FixUnusedVars = Compiler_Pipeline_FixUnusedVars

var EliminateDeadCode = Compiler_Pipeline_EliminateDeadCode

var CountFuncsInFile = Compiler_Pipeline_CountFuncsInFile

var TrimWrapperFile = Compiler_Pipeline_TrimWrapperFile

var TrimWrapperContent = Compiler_Pipeline_TrimWrapperContent

var IsWrapperUsed = Compiler_Pipeline_IsWrapperUsed

var ExtractWrapperFuncName = Compiler_Pipeline_ExtractWrapperFuncName

var SplitWrapperSections = Compiler_Pipeline_SplitWrapperSections

var SplitWrapperLoop = Compiler_Pipeline_SplitWrapperLoop

var MakeGoPackage = Compiler_Pipeline_MakeGoPackage

var LoadLocalModules = Compiler_Pipeline_LoadLocalModules

var LoadFromSkydepsOrSkip = Compiler_Pipeline_LoadFromSkydepsOrSkip

var LoadFromCandidatesOrSkip = Compiler_Pipeline_LoadFromCandidatesOrSkip

var TryOneSkydepCandidate = Compiler_Pipeline_TryOneSkydepCandidate

var ParseAndLoadSkydep = Compiler_Pipeline_ParseAndLoadSkydep

var FindSkydepCandidates = Compiler_Pipeline_FindSkydepCandidates

var SkydepSrcRoot = Compiler_Pipeline_SkydepSrcRoot

var LoadFfiBindings = Compiler_Pipeline_LoadFfiBindings

var LoadOneFfiBinding = Compiler_Pipeline_LoadOneFfiBinding

var TryLoadBinding = Compiler_Pipeline_TryLoadBinding

var CopyFfiWrappers = Compiler_Pipeline_CopyFfiWrappers

var CopyProjectWrappers = Compiler_Pipeline_CopyProjectWrappers

var CopyWrapperDir = Compiler_Pipeline_CopyWrapperDir

var CopyOneFfiWrapper = Compiler_Pipeline_CopyOneFfiWrapper

var BuildAliasMap = Compiler_Pipeline_BuildAliasMap

var GeneratePrefixAliases = Compiler_Pipeline_GeneratePrefixAliases

var MakePrefixAlias = Compiler_Pipeline_MakePrefixAlias

var IsCommonName = Compiler_Pipeline_IsCommonName

var IsSharedValue = Compiler_Pipeline_IsSharedValue

var DeduplicateDecls = Compiler_Pipeline_DeduplicateDecls

var DeduplicateDeclsLoop = Compiler_Pipeline_DeduplicateDeclsLoop

var GetDeclName = Compiler_Pipeline_GetDeclName

var ExtractVarName = Compiler_Pipeline_ExtractVarName

var ExtractFuncName = Compiler_Pipeline_ExtractFuncName

var ExtractTypeName = Compiler_Pipeline_ExtractTypeName

var GenerateImportAliases = Compiler_Pipeline_GenerateImportAliases

var GenerateAliasesForImport = Compiler_Pipeline_GenerateAliasesForImport

var GenerateAliasesFromModule = Compiler_Pipeline_GenerateAliasesFromModule

var IsExposingAll = Compiler_Pipeline_IsExposingAll

var IsExposingNone = Compiler_Pipeline_IsExposingNone

var GetExposeNames = Compiler_Pipeline_GetExposeNames

var IsZeroArityInModule = Compiler_Pipeline_IsZeroArityInModule

var FindModule = Compiler_Pipeline_FindModule

var ExtractExportedNames = Compiler_Pipeline_ExtractExportedNames

var ExtractDeclNames = Compiler_Pipeline_ExtractDeclNames

var CollectAllFunctionNames = Compiler_Pipeline_CollectAllFunctionNames

var ExtractDeclNameForFn = Compiler_Pipeline_ExtractDeclNameForFn

var DeduplicateStringList = Compiler_Pipeline_DeduplicateStringList

var DeduplicateStringsLoop = Compiler_Pipeline_DeduplicateStringsLoop

var IsModuleLoaded = Compiler_Pipeline_IsModuleLoaded

var AddImportAlias = Compiler_Pipeline_AddImportAlias

var ImportAlias = Compiler_Pipeline_ImportAlias

var CompileDependencyModule = Compiler_Pipeline_CompileDependencyModule

var GenerateConstructorAliases = Compiler_Pipeline_GenerateConstructorAliases

var MakeConstructorAlias = Compiler_Pipeline_MakeConstructorAlias

var GenerateOriginalAliases = Compiler_Pipeline_GenerateOriginalAliases

var MakeOriginalAlias = Compiler_Pipeline_MakeOriginalAlias

var IsExportableDecl = Compiler_Pipeline_IsExportableDecl

var DirOfPath = Compiler_Pipeline_DirOfPath

var NeedsStdlibWrapper = Compiler_Pipeline_NeedsStdlibWrapper

var BuildStdlibGoImports = Compiler_Pipeline_BuildStdlibGoImports

var ImportToGoImport = Compiler_Pipeline_ImportToGoImport

var CopyStdlibGo = Compiler_Pipeline_CopyStdlibGo

var FfiSafePart = Compiler_Pipeline_FfiSafePart

var FfiSafePartLoop = Compiler_Pipeline_FfiSafePartLoop

var PrefixDecl = Compiler_Pipeline_PrefixDecl

var CompileSource = Compiler_Pipeline_CompileSource

var CompileModule = Compiler_Pipeline_CompileModule

var CompileProject = Compiler_Pipeline_CompileProject

var InferSrcRoot = Compiler_Pipeline_InferSrcRoot

var InferSrcRootFromEntry = Compiler_Pipeline_InferSrcRootFromEntry

var FindSubstring = Compiler_Pipeline_FindSubstring

var FindSubstringAt = Compiler_Pipeline_FindSubstringAt

var PrintDiagnostics = Compiler_Pipeline_PrintDiagnostics

var PrintTypedDecls = Compiler_Pipeline_PrintTypedDecls

var emitPackage = Compiler_Emit_EmitPackage

var EmitPackage = Compiler_Emit_EmitPackage

var emitImports = Compiler_Emit_EmitImports

var EmitImports = Compiler_Emit_EmitImports

var emitDecl = Compiler_Emit_EmitDecl

var EmitDecl = Compiler_Emit_EmitDecl

var emitFuncDecl = Compiler_Emit_EmitFuncDecl

var EmitFuncDecl = Compiler_Emit_EmitFuncDecl

var emitParam = Compiler_Emit_EmitParam

var EmitParam = Compiler_Emit_EmitParam

var emitStmt = Compiler_Emit_EmitStmt

var EmitStmt = Compiler_Emit_EmitStmt

var emitExpr = Compiler_Emit_EmitExpr

var EmitExpr = Compiler_Emit_EmitExpr

var goQuote = Compiler_Emit_GoQuote

var EmptyCtx = Compiler_Lower_EmptyCtx()

var buildExposedStdlib = Compiler_Lower_BuildExposedStdlib

var getExposedNames = Compiler_Lower_GetExposedNames

var lowerModule = Compiler_Lower_LowerModule

var lowerDeclarations = Compiler_Lower_LowerDeclarations

var lowerDecl = Compiler_Lower_LowerDecl

var lowerFunction = Compiler_Lower_LowerFunction

var generateParamBindings = Compiler_Lower_GenerateParamBindings

var generateOneParamBinding = Compiler_Lower_GenerateOneParamBinding

var lowerParam = Compiler_Lower_LowerParam

var lowerExpr = Compiler_Lower_LowerExpr

var lowerIdentifier = Compiler_Lower_LowerIdentifier

var lowerStdlibExposed = Compiler_Lower_LowerStdlibExposed

var cssNameToProperty = Compiler_Lower_CssNameToProperty

var cssNameToPropertyLoop = Compiler_Lower_CssNameToPropertyLoop

var lowerConstructorValue = Compiler_Lower_LowerConstructorValue

var lowerQualified = Compiler_Lower_LowerQualified

var lowerCall = Compiler_Lower_LowerCall

var isDynamicCallee = Compiler_Lower_IsDynamicCallee

var extractPatternName = Compiler_Lower_ExtractPatternName

var isWellKnownIdent = Compiler_Lower_IsWellKnownIdent

var isUpperStart = Compiler_Lower_IsUpperStart

var checkPartialApplication = Compiler_Lower_CheckPartialApplication

var generatePartialClosure = Compiler_Lower_GeneratePartialClosure

var buildCurriedClosure = Compiler_Lower_BuildCurriedClosure

var listRange = Compiler_Lower_ListRange

var flattenCall = Compiler_Lower_FlattenCall

var lowerLambda = Compiler_Lower_LowerLambda

var lowerIf = Compiler_Lower_LowerIf

var lowerLet = Compiler_Lower_LowerLet

var lowerLetBinding = Compiler_Lower_LowerLetBinding

var extractTupleBindings = Compiler_Lower_ExtractTupleBindings

var lowerCase = Compiler_Lower_LowerCase

var lowerCaseToSwitch = Compiler_Lower_LowerCaseToSwitch

var emitBranchCode = Compiler_Lower_EmitBranchCode

var patternToCondition = Compiler_Lower_PatternToCondition

var literalCondition = Compiler_Lower_LiteralCondition

var patternToBindings = Compiler_Lower_PatternToBindings

var bindConstructorArgs = Compiler_Lower_BindConstructorArgs

var bindAdtConstructorArgs = Compiler_Lower_BindAdtConstructorArgs

var bindTupleArgs = Compiler_Lower_BindTupleArgs

var bindListArgs = Compiler_Lower_BindListArgs

var lowerBinary = Compiler_Lower_LowerBinary

var lowerRecordUpdate = Compiler_Lower_LowerRecordUpdate

var generateConstructorDecls = Compiler_Lower_GenerateConstructorDecls

var generateCtorsForDecl = Compiler_Lower_GenerateCtorsForDecl

var generateCtorFunc = Compiler_Lower_GenerateCtorFunc

var generateHelperDecls = Compiler_Lower_GenerateHelperDecls()

var collectLocalFunctions = Compiler_Lower_CollectLocalFunctions

var collectLocalFunctionArities = Compiler_Lower_CollectLocalFunctionArities

var buildConstructorMap = Compiler_Lower_BuildConstructorMap

var addCtorsFromList = Compiler_Lower_AddCtorsFromList

var countFunArgs = Compiler_Lower_CountFunArgs

var sanitizeGoIdent = Compiler_Lower_SanitizeGoIdent

var isGoKeyword = Compiler_Lower_IsGoKeyword

var isStdlibCallee = Compiler_Lower_IsStdlibCallee

var makeTupleKey = Compiler_Lower_MakeTupleKey

var getPatVarName = Compiler_Lower_GetPatVarName

var isParamOrBuiltin = Compiler_Lower_IsParamOrBuiltin

var lowerArgExpr = Compiler_Lower_LowerArgExpr

var isZeroArityFn = Compiler_Lower_IsZeroArityFn

var getFnArity = Compiler_Lower_GetFnArity

var makeCurryWrapper = Compiler_Lower_MakeCurryWrapper

var listContains = Compiler_Lower_ListContains

var isLocalFn = Compiler_Lower_IsLocalFn

var isLocalFunction = Compiler_Lower_IsLocalFunction

var capitalizeFirst = Compiler_Lower_CapitalizeFirst

var lastPartOf = Compiler_Lower_LastPartOf

var listGet = Compiler_Lower_ListGet

var zipIndex = Compiler_Lower_ZipIndex

var zipIndexLoop = Compiler_Lower_ZipIndexLoop

var emitGoExprInline = Compiler_Lower_EmitGoExprInline

var emitMapEntry = Compiler_Lower_EmitMapEntry

var emitInlineParam = Compiler_Lower_EmitInlineParam

var exprToGoString = Compiler_Lower_ExprToGoString

var lowerExprToStmts = Compiler_Lower_LowerExprToStmts

var stmtsToGoString = Compiler_Lower_StmtsToGoString

var fixCurriedCalls = Compiler_Lower_FixCurriedCalls

var stmtToGoString = Compiler_Lower_StmtToGoString

func Compiler_Pipeline_FindCharIdx(ch any, str any, idx any) any {
	return func() any { if sky_asBool(sky_asInt(idx) >= sky_asInt(sky_stringLength(str))) { return -1 }; return func() any { if sky_asBool(sky_equal(sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1), str), sky_stringFromChar(ch))) { return idx }; return Compiler_Pipeline_FindCharIdx(ch, str, sky_asInt(idx) + sky_asInt(1)) }() }()
}

func Compiler_Pipeline_FindLastSlash(path any, idx any) any {
	return func() any { if sky_asBool(sky_asInt(idx) < sky_asInt(0)) { return -1 }; return func() any { if sky_asBool(sky_equal(sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1), path), "/")) { return idx }; return Compiler_Pipeline_FindLastSlash(path, sky_asInt(idx) - sky_asInt(1)) }() }()
}

func Compiler_Pipeline_Compile(filePath any, outDir any) any {
	return func() any { sky_println("╔══════════════════════════════════════════════════╗"); sky_println("║  Sky Self-Hosted Compiler v0.4.2                ║"); sky_println("╚══════════════════════════════════════════════════╝"); sky_println(""); return func() any { return func() any { __subject := sky_fileRead(filePath); if sky_asSkyResult(__subject).SkyName == "Err" { readErr := sky_asSkyResult(__subject).ErrValue; _ = readErr; return SkyErr(sky_concat("Cannot read file: ", sky_concat(filePath, sky_concat(" (", sky_concat(readErr, ")"))))) };  if sky_asSkyResult(__subject).SkyName == "Ok" { source := sky_asSkyResult(__subject).OkValue; _ = source; return func() any { lexResult := Compiler_Lexer_Lex(source); _ = lexResult; return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if sky_asSkyResult(__subject).SkyName == "Err" { parseErr := sky_asSkyResult(__subject).ErrValue; _ = parseErr; return SkyErr(sky_concat("Parse error: ", parseErr)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { mod := sky_asSkyResult(__subject).OkValue; _ = mod; return func() any { srcRoot := Compiler_Pipeline_DirOfPath(filePath); _ = srcRoot; hasLocalImports := sky_call2(sky_listFoldl(func(imp any) any { return func(acc any) any { return sky_asBool(acc) || sky_asBool(sky_not(Compiler_Resolver_IsStdlib(sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"])))) } }), false, sky_asMap(mod)["imports"]); _ = hasLocalImports; return func() any { if sky_asBool(hasLocalImports) { return Compiler_Pipeline_CompileMultiModule(filePath, outDir, srcRoot, mod) }; return Compiler_Pipeline_CompileSource(filePath, outDir, source) }() }() };  return nil }() }() }() };  return nil }() }() }()
}

func Compiler_Pipeline_CompileMultiModule(entryPath any, outDir any, srcRoot any, entryMod any) any {
	return func() any { localImports := sky_call(sky_listFilter(func(imp any) any { return sky_not(Compiler_Resolver_IsStdlib(sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]))) }), sky_asMap(entryMod)["imports"]); _ = localImports; localModules := Compiler_Pipeline_LoadLocalModules(srcRoot, localImports, []any{}); _ = localModules; entryFfiModules := Compiler_Pipeline_LoadFfiBindings(localImports); _ = entryFfiModules; depFfiModules := sky_call(sky_listConcatMap(func(pair any) any { return func() any { mod := sky_snd(pair); _ = mod; return Compiler_Pipeline_LoadFfiBindings(sky_asMap(mod)["imports"]) }() }), localModules); _ = depFfiModules; ffiModules := sky_call(sky_listAppend(entryFfiModules), depFfiModules); _ = ffiModules; loadedModules := sky_call(sky_listAppend(localModules), ffiModules); _ = loadedModules; sky_println(sky_concat("   [MULTI] Loaded ", sky_concat(sky_stringFromInt(sky_listLength(localModules)), sky_concat(" local + ", sky_concat(sky_stringFromInt(sky_listLength(ffiModules)), " FFI modules"))))); aliasMap := Compiler_Pipeline_BuildAliasMap(sky_asMap(entryMod)["imports"]); _ = aliasMap; stdlibEnv := Compiler_Resolver_BuildStdlibEnv(); _ = stdlibEnv; depDecls := sky_call(sky_listConcatMap(func(__pa0 any) any { return Compiler_Pipeline_CompileDependencyModule(stdlibEnv, loadedModules, __pa0) }), loadedModules); _ = depDecls; return Compiler_Pipeline_CompileMultiModuleEntry(outDir, entryMod, aliasMap, stdlibEnv, depDecls, loadedModules) }()
}

func Compiler_Pipeline_CompileMultiModuleEntry(outDir any, entryMod any, aliasMap any, stdlibEnv any, depDecls any, loadedModules any) any {
	return func() any { entryCheckResult := Compiler_Checker_CheckModule(entryMod, SkyJust(stdlibEnv)); _ = entryCheckResult; entryRegistry := func() any { return func() any { __subject := entryCheckResult; if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return sky_asMap(result)["registry"] };  if sky_asSkyResult(__subject).SkyName == "Err" { return Compiler_Adt_EmptyRegistry() };  return nil }() }(); _ = entryRegistry; return Compiler_Pipeline_EmitMultiModuleGo(outDir, entryMod, entryRegistry, aliasMap, depDecls, loadedModules) }()
}

func Compiler_Pipeline_EmitMultiModuleGo(outDir any, entryMod any, entryRegistry any, aliasMap any, depDecls any, loadedModules any) any {
	return func() any { baseCtx := Compiler_Lower_EmptyCtx(); _ = baseCtx; entryExposed := Compiler_Lower_BuildExposedStdlib(sky_asMap(entryMod)["imports"]); _ = entryExposed; entryCtx := sky_recordUpdate(baseCtx, map[string]any{"registry": entryRegistry, "localFunctions": Compiler_Lower_CollectLocalFunctions(sky_asMap(entryMod)["declarations"]), "importedConstructors": Compiler_Lower_BuildConstructorMap(entryRegistry), "importAliases": aliasMap, "exposedStdlib": entryExposed, "localFunctionArity": Compiler_Lower_CollectLocalFunctionArities(sky_asMap(entryMod)["declarations"])}); _ = entryCtx; entryGoDecls := Compiler_Lower_LowerDeclarations(entryCtx, sky_asMap(entryMod)["declarations"]); _ = entryGoDecls; entryCtorDecls := Compiler_Lower_GenerateConstructorDecls(entryRegistry, sky_asMap(entryMod)["declarations"]); _ = entryCtorDecls; helperDecls := Compiler_Lower_GenerateHelperDecls(); _ = helperDecls; entryImportAliases := Compiler_Pipeline_GenerateImportAliases(sky_asMap(entryMod)["imports"], loadedModules); _ = entryImportAliases; allDecls := Compiler_Pipeline_DeduplicateDecls(sky_listConcat([]any{helperDecls, depDecls, entryCtorDecls, entryGoDecls, entryImportAliases})); _ = allDecls; allImports := sky_call(sky_listAppend(sky_asMap(entryMod)["imports"]), sky_call(sky_listConcatMap(func(pair any) any { return func() any { mod := sky_snd(pair); _ = mod; return sky_asMap(mod)["imports"] }() }), loadedModules)); _ = allImports; return Compiler_Pipeline_WriteMultiModuleOutput(outDir, allDecls, allImports) }()
}

func Compiler_Pipeline_WriteMultiModuleOutput(outDir any, allDecls any, imports any) any {
	return func() any { goPackage := Compiler_Pipeline_MakeGoPackage(allDecls); _ = goPackage; rawGoCode := Compiler_Emit_EmitPackage(goPackage); _ = rawGoCode; goCode := Compiler_Pipeline_FixUnusedVars(rawGoCode); _ = goCode; outPath := sky_concat(outDir, "/main.go"); _ = outPath; sky_fileMkdirAll(outDir); Compiler_Pipeline_CopyFfiWrappers(outDir, imports, []any{}); sky_call(sky_fileWrite(outPath), goCode); sky_println(sky_concat("   Wrote ", outPath)); sky_println(sky_concat("   ", sky_concat(sky_stringFromInt(sky_listLength(allDecls)), " total Go declarations"))); Compiler_Pipeline_EliminateDeadCode(outDir, goCode); sky_println(""); sky_println("✓ Compilation successful"); return SkyOk(goCode) }()
}

func Compiler_Pipeline_OptimiseJsonFunctions(code any) any {
	return func() any { s1 := sky_call2(sky_stringReplace("Lsp_JsonRpc_ExtractBraced("), "sky_nativeExtractBracketed(", code); _ = s1; s2 := sky_call2(sky_stringReplace("Lsp_JsonRpc_JsonSplitArray("), "sky_nativeJsonSplitArray(", s1); _ = s2; s3 := sky_call2(sky_stringReplace("Lsp_JsonRpc_SplitJsonElements("), "sky_nativeSplitJsonElements(", s2); _ = s3; s4 := sky_call2(sky_stringReplace("Lsp_JsonRpc_ExtractBracketed("), "sky_nativeExtractBracketed(", s3); _ = s4; s5 := sky_call2(sky_stringReplace("Lsp_JsonRpc_FindInString("), "sky_nativeFindInString(", s4); _ = s5; return s5 }()
}

func Compiler_Pipeline_WriteNativeJsonFile(outDir any) any {
	return func() any { sky_call(sky_processRun("sh"), []any{"-c", sky_concat("cat > ", sky_concat(outDir, "/sky_json_native.go << 'GOEOF'\npackage main\n\nimport \"strings\"\n\nvar _ = strings.TrimSpace\n\nfunc sky_nativeFindInString(needle any, haystack any, idx any) any {\n\tn := sky_asString(needle); h := sky_asString(haystack); i := sky_asInt(idx)\n\tif i > 0 && i < len(h) { h = h[i:] } else if i >= len(h) { return -1 }\n\tpos := strings.Index(h, n); if pos < 0 { return -1 }; return i + pos\n}\n\nfunc sky_nativeExtractBracketed(remaining any, _ any, _ any) any {\n\tstr := sky_asString(remaining); depth := 0; inStr := false; esc := false\n\tfor i := 0; i < len(str); i++ {\n\t\tc := str[i]; if esc { esc = false; continue }; if c == 92 && inStr { esc = true; continue }\n\t\tif c == 34 { inStr = !inStr; continue }; if inStr { continue }\n\t\tif c == 91 || c == 123 { depth++ } else if c == 93 || c == 125 { depth--; if depth == 0 { return str[:i+1] } }\n\t}\n\treturn str\n}\n\nfunc sky_nativeSplitJsonElements(s any, _ any, _ any, _ any, _ any) any {\n\tstr := strings.TrimSpace(sky_asString(s)); if len(str) == 0 { return []any{} }\n\tvar result []any; depth := 0; start := 0; inStr := false; esc := false\n\tfor i := 0; i < len(str); i++ {\n\t\tc := str[i]; if esc { esc = false; continue }; if c == 92 && inStr { esc = true; continue }\n\t\tif c == 34 { inStr = !inStr; continue }; if inStr { continue }\n\t\tif c == 123 || c == 91 { depth++ } else if c == 125 || c == 93 { depth-- } else if c == 44 && depth == 0 {\n\t\t\telem := strings.TrimSpace(str[start:i]); if len(elem) > 0 { result = append(result, elem) }; start = i + 1\n\t\t}\n\t}\n\tlast := strings.TrimSpace(str[start:]); if len(last) > 0 { result = append(result, last) }\n\tif result == nil { return []any{} }; return result\n}\n\nfunc sky_nativeJsonSplitArray(arrayStr any) any {\n\tstr := strings.TrimSpace(sky_asString(arrayStr))\n\tif len(str) < 2 { return []any{} }\n\tinner := strings.TrimSpace(str[1:len(str)-1])\n\tif len(inner) == 0 { return []any{} }\n\treturn sky_nativeSplitJsonElements(inner, nil, nil, nil, nil)\n}\nGOEOF"))}); sky_call(sky_processRun("sh"), []any{"-c", sky_concat("cd ", sky_concat(outDir, " && python3 -c \"import re; f=open('main.go'); c=f.read(); f.close(); c=re.sub(r'(?<!func )Lsp_JsonRpc_FindInString\\(', 'sky_nativeFindInString(', c); c=re.sub(r'(?<!func )Lsp_JsonRpc_ExtractBracketed\\(', 'sky_nativeExtractBracketed(', c); c=re.sub(r'(?<!func )Lsp_JsonRpc_ExtractBraced\\(', 'sky_nativeExtractBracketed(', c); c=re.sub(r'(?<!func )Lsp_JsonRpc_SplitJsonElements\\(', 'sky_nativeSplitJsonElements(', c); c=re.sub(r'(?<!func )Lsp_JsonRpc_JsonSplitArray\\(', 'sky_nativeJsonSplitArray(', c); f=open('main.go','w'); f.write(c); f.close()\""))}); return struct{}{} }()
}

func Compiler_Pipeline_NativeJsonHelperCode() any {
	return sky_concat("package main\n\nimport \"strings\"\n\nvar _ = strings.TrimSpace\n\n", sky_concat("func sky_nativeFindInString(needle any, haystack any, idx any) any {\n", sky_concat("\tn := sky_asString(needle); h := sky_asString(haystack); i := sky_asInt(idx)\n", sky_concat("\tif i > 0 && i < len(h) { h = h[i:] } else if i >= len(h) { return -1 }\n", sky_concat("\tpos := strings.Index(h, n); if pos < 0 { return -1 }; return i + pos\n}\n\n", sky_concat("func sky_nativeExtractBracketed(remaining any, _ any, _ any) any {\n", sky_concat("\tstr := sky_asString(remaining); depth := 0; inStr := false; esc := false\n", sky_concat("\tfor i := 0; i < len(str); i++ {\n", sky_concat("\t\tc := str[i]; if esc { esc = false; continue }; if c == '\\\\' && inStr { esc = true; continue }\n", sky_concat("\t\tif c == '\"' { inStr = !inStr; continue }; if inStr { continue }\n", sky_concat("\t\tif c == '[' || c == '{' { depth++ } else if c == ']' || c == '}' { depth--; if depth == 0 { return str[:i+1] } }\n", sky_concat("\t}\n\treturn str\n}\n\n", sky_concat("func sky_nativeSplitJsonElements(s any, _ any, _ any, _ any, _ any) any {\n", sky_concat("\tstr := strings.TrimSpace(sky_asString(s)); if len(str) == 0 { return []any{} }\n", sky_concat("\tvar result []any; depth := 0; start := 0; inStr := false; esc := false\n", sky_concat("\tfor i := 0; i < len(str); i++ {\n", sky_concat("\t\tc := str[i]; if esc { esc = false; continue }; if c == '\\\\' && inStr { esc = true; continue }\n", sky_concat("\t\tif c == '\"' { inStr = !inStr; continue }; if inStr { continue }\n", sky_concat("\t\tif c == '{' || c == '[' { depth++ } else if c == '}' || c == ']' { depth-- } else if c == ',' && depth == 0 {\n", sky_concat("\t\t\telem := strings.TrimSpace(str[start:i]); if len(elem) > 0 { result = append(result, elem) }; start = i + 1\n", sky_concat("\t\t}\n\t}\n", sky_concat("\tlast := strings.TrimSpace(str[start:]); if len(last) > 0 { result = append(result, last) }\n", sky_concat("\tif result == nil { return []any{} }; return result\n}\n\n", sky_concat("func sky_nativeJsonSplitArray(arrayStr any) any {\n", sky_concat("\tstr := strings.TrimSpace(sky_asString(arrayStr))\n", sky_concat("\tif len(str) < 2 { return []any{} }\n", sky_concat("\tinner := strings.TrimSpace(str[1:len(str)-1])\n", sky_concat("\tif len(inner) == 0 { return []any{} }\n", "\treturn sky_nativeSplitJsonElements(inner, nil, nil, nil, nil)\n}\n"))))))))))))))))))))))))))))
}

func Compiler_Pipeline_FixUnusedVars(code any) any {
	return code
}

func Compiler_Pipeline_EliminateDeadCode(outDir any, mainGoCode any) any {
	return func() any { listResult := sky_call(sky_processRun("sh"), []any{"-c", sky_concat("ls ", sky_concat(outDir, sky_concat("/sky_ffi_*.go ", sky_concat(outDir, "/sky_*.go 2>/dev/null | sort -u"))))}); _ = listResult; return func() any { return func() any { __subject := listResult; if sky_asSkyResult(__subject).SkyName == "Ok" { output := sky_asSkyResult(__subject).OkValue; _ = output; return func() any { wrapperFiles := sky_call(sky_listFilter(func(f any) any { return sky_not(sky_stringIsEmpty(sky_stringTrim(f))) }), sky_call(sky_stringSplit("\n"), output)); _ = wrapperFiles; totalBefore := sky_call2(sky_listFoldl(func(f any) any { return func(acc any) any { return sky_asInt(acc) + sky_asInt(Compiler_Pipeline_CountFuncsInFile(f)) } }), 0, wrapperFiles); _ = totalBefore; sky_call(sky_listMap(func(f any) any { return Compiler_Pipeline_TrimWrapperFile(f, mainGoCode) }), wrapperFiles); totalAfter := sky_call2(sky_listFoldl(func(f any) any { return func(acc any) any { return sky_asInt(acc) + sky_asInt(Compiler_Pipeline_CountFuncsInFile(f)) } }), 0, wrapperFiles); _ = totalAfter; return func() any { if sky_asBool(sky_asInt(totalBefore) > sky_asInt(totalAfter)) { return sky_println(sky_concat("   [DCE] Eliminated ", sky_concat(sky_stringFromInt(sky_asInt(totalBefore) - sky_asInt(totalAfter)), sky_concat(" unused wrapper functions (", sky_concat(sky_stringFromInt(totalBefore), sky_concat(" → ", sky_concat(sky_stringFromInt(totalAfter), ")"))))))) }; return struct{}{} }() }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return struct{}{} };  return nil }() }() }()
}

func Compiler_Pipeline_CountFuncsInFile(filePath any) any {
	return func() any { return func() any { __subject := sky_fileRead(filePath); if sky_asSkyResult(__subject).SkyName == "Ok" { content := sky_asSkyResult(__subject).OkValue; _ = content; return sky_listLength(sky_call(sky_listFilter(func(line any) any { return sky_call(sky_stringStartsWith("func Sky_"), sky_stringTrim(line)) }), sky_call(sky_stringSplit("\n"), content))) };  if sky_asSkyResult(__subject).SkyName == "Err" { return 0 };  return nil }() }()
}

func Compiler_Pipeline_TrimWrapperFile(filePath any, mainGoCode any) any {
	return func() any { return func() any { __subject := sky_fileRead(filePath); if sky_asSkyResult(__subject).SkyName == "Ok" { content := sky_asSkyResult(__subject).OkValue; _ = content; return func() any { trimmed := Compiler_Pipeline_TrimWrapperContent(content, mainGoCode); _ = trimmed; return func() any { if sky_asBool(!sky_equal(trimmed, content)) { return sky_call(sky_fileWrite(filePath), trimmed) }; return struct{}{} }() }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return struct{}{} };  return nil }() }()
}

func Compiler_Pipeline_TrimWrapperContent(wrapperCode any, mainGoCode any) any {
	return func() any { sections := Compiler_Pipeline_SplitWrapperSections(wrapperCode); _ = sections; header := sky_asMap(sections)["header"]; _ = header; funcBlocks := sky_asMap(sections)["functions"]; _ = funcBlocks; keptFuncs := sky_call(sky_listFilter(func(block any) any { return Compiler_Pipeline_IsWrapperUsed(block, mainGoCode) }), funcBlocks); _ = keptFuncs; keptNonSky := sky_call(sky_listFilter(func(block any) any { return sky_not(sky_call(sky_stringStartsWith("func Sky_"), block)) }), funcBlocks); _ = keptNonSky; allKept := sky_call(sky_listAppend(keptFuncs), keptNonSky); _ = allKept; return func() any { if sky_asBool(sky_listIsEmpty(allKept)) { return sky_concat(header, "\n") }; return sky_concat(header, sky_concat("\n\n", sky_concat(sky_call(sky_stringJoin("\n\n"), allKept), "\n"))) }() }()
}

func Compiler_Pipeline_IsWrapperUsed(funcBlock any, mainGoCode any) any {
	return func() any { funcName := Compiler_Pipeline_ExtractWrapperFuncName(funcBlock); _ = funcName; return func() any { if sky_asBool(sky_stringIsEmpty(funcName)) { return true }; return func() any { if sky_asBool(sky_not(sky_call(sky_stringStartsWith("Sky_"), funcName))) { return true }; return sky_call(sky_stringContains(funcName), mainGoCode) }() }() }()
}

func Compiler_Pipeline_ExtractWrapperFuncName(block any) any {
	return func() any { if sky_asBool(sky_call(sky_stringStartsWith("func "), block)) { return func() any { afterFunc := sky_call2(sky_stringSlice(5), sky_stringLength(block), block); _ = afterFunc; parenIdx := Compiler_Pipeline_FindCharIdx(string('('), afterFunc, 0); _ = parenIdx; return func() any { if sky_asBool(sky_asInt(parenIdx) > sky_asInt(0)) { return sky_call2(sky_stringSlice(0), parenIdx, afterFunc) }; return "" }() }() }; return "" }()
}

func Compiler_Pipeline_SplitWrapperSections(code any) any {
	return func() any { lines := sky_call(sky_stringSplit("\n"), code); _ = lines; result := Compiler_Pipeline_SplitWrapperLoop(lines, "", []any{}, "", false); _ = result; return result }()
}

func Compiler_Pipeline_SplitWrapperLoop(lines any, header any, funcs any, currentFunc any, inFunc any) any {
	return func() any { return func() any { __subject := lines; if len(sky_asList(__subject)) == 0 { return func() any { finalFuncs := func() any { if sky_asBool(sky_stringIsEmpty(sky_stringTrim(currentFunc))) { return funcs }; return sky_call(sky_listAppend(funcs), []any{sky_stringTrim(currentFunc)}) }(); _ = finalFuncs; return map[string]any{"header": sky_stringTrim(header), "functions": finalFuncs} }() };  if len(sky_asList(__subject)) > 0 { line := sky_asList(__subject)[0]; _ = line; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("func "), line)) { return func() any { updatedFuncs := func() any { if sky_asBool(sky_stringIsEmpty(sky_stringTrim(currentFunc))) { return funcs }; return sky_call(sky_listAppend(funcs), []any{sky_stringTrim(currentFunc)}) }(); _ = updatedFuncs; return Compiler_Pipeline_SplitWrapperLoop(rest, header, updatedFuncs, line, true) }() }; return func() any { if sky_asBool(inFunc) { return Compiler_Pipeline_SplitWrapperLoop(rest, header, funcs, sky_concat(currentFunc, sky_concat("\n", line)), true) }; return Compiler_Pipeline_SplitWrapperLoop(rest, sky_concat(header, sky_concat("\n", line)), funcs, currentFunc, false) }() }() };  return nil }() }()
}

func Compiler_Pipeline_MakeGoPackage(decls any) any {
	return map[string]any{"name": "main", "imports": []any{map[string]any{"path": "fmt", "alias_": ""}, map[string]any{"path": "bufio", "alias_": ""}, map[string]any{"path": "io", "alias_": ""}, map[string]any{"path": "os", "alias_": ""}, map[string]any{"path": "os/exec", "alias_": "exec"}, map[string]any{"path": "net/http", "alias_": "net_http"}, map[string]any{"path": "strconv", "alias_": ""}, map[string]any{"path": "strings", "alias_": ""}, map[string]any{"path": "sort", "alias_": ""}, map[string]any{"path": "math", "alias_": ""}, map[string]any{"path": "crypto/sha256", "alias_": "crypto_sha256"}, map[string]any{"path": "crypto/md5", "alias_": "crypto_md5"}, map[string]any{"path": "encoding/hex", "alias_": "hex"}, map[string]any{"path": "encoding/base64", "alias_": "base64"}, map[string]any{"path": "encoding/json", "alias_": "encoding_json"}, map[string]any{"path": "time", "alias_": ""}, map[string]any{"path": "context", "alias_": ""}}, "declarations": decls}
}

func Compiler_Pipeline_LoadLocalModules(srcRoot any, imports any, acc any) any {
	return func() any { return func() any { __subject := imports; if len(sky_asList(__subject)) == 0 { return sky_listReverse(acc) };  if len(sky_asList(__subject)) > 0 { imp := sky_asList(__subject)[0]; _ = imp; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { modName := sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]); _ = modName; filePath := Compiler_Resolver_ResolveModulePath(srcRoot, sky_asMap(imp)["moduleName"]); _ = filePath; return func() any { if sky_asBool(sky_asBool(Compiler_Resolver_IsStdlib(modName)) || sky_asBool(Compiler_Pipeline_IsModuleLoaded(modName, acc))) { return Compiler_Pipeline_LoadLocalModules(srcRoot, rest, acc) }; return func() any { if sky_asBool(sky_equal(sky_asMap(imp)["alias_"], "_")) { return Compiler_Pipeline_LoadLocalModules(srcRoot, rest, acc) }; return func() any { return func() any { __subject := sky_fileRead(filePath); if sky_asSkyResult(__subject).SkyName == "Err" { return Compiler_Pipeline_LoadFromSkydepsOrSkip(srcRoot, modName, sky_asMap(imp)["moduleName"], rest, acc) };  if sky_asSkyResult(__subject).SkyName == "Ok" { source := sky_asSkyResult(__subject).OkValue; _ = source; return func() any { lexResult := Compiler_Lexer_Lex(source); _ = lexResult; return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if sky_asSkyResult(__subject).SkyName == "Err" { return Compiler_Pipeline_LoadLocalModules(srcRoot, rest, acc) };  if sky_asSkyResult(__subject).SkyName == "Ok" { mod := sky_asSkyResult(__subject).OkValue; _ = mod; return func() any { transImports := sky_call(sky_listFilter(func(i any) any { return sky_not(Compiler_Resolver_IsStdlib(sky_call(sky_stringJoin("."), sky_asMap(i)["moduleName"]))) }), sky_asMap(mod)["imports"]); _ = transImports; withTransitive := Compiler_Pipeline_LoadLocalModules(srcRoot, transImports, append([]any{SkyTuple2{V0: modName, V1: mod}}, sky_asList(acc)...)); _ = withTransitive; return Compiler_Pipeline_LoadLocalModules(srcRoot, rest, withTransitive) }() };  return nil }() }() }() };  return nil }() }() }() }() }() };  return nil }() }()
}

func Compiler_Pipeline_LoadFromSkydepsOrSkip(srcRoot any, modName any, moduleParts any, rest any, acc any) any {
	return func() any { candidates := Compiler_Pipeline_FindSkydepCandidates(sky_concat(Compiler_Pipeline_DirOfPath(srcRoot), "/.skydeps"), moduleParts); _ = candidates; return Compiler_Pipeline_LoadFromCandidatesOrSkip(srcRoot, modName, candidates, rest, acc) }()
}

func Compiler_Pipeline_LoadFromCandidatesOrSkip(srcRoot any, modName any, candidates any, rest any, acc any) any {
	return func() any { if sky_asBool(sky_listIsEmpty(candidates)) { return Compiler_Pipeline_LoadLocalModules(srcRoot, rest, acc) }; return Compiler_Pipeline_TryOneSkydepCandidate(srcRoot, modName, candidates, rest, acc) }()
}

func Compiler_Pipeline_TryOneSkydepCandidate(srcRoot any, modName any, candidates any, rest any, acc any) any {
	return func() any { firstPath := func() any { return func() any { __subject := sky_listHead(candidates); if sky_asSkyMaybe(__subject).SkyName == "Just" { p := sky_asSkyMaybe(__subject).JustValue; _ = p; return p };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "" };  return nil }() }(); _ = firstPath; restCandidates := sky_call(sky_listDrop(1), candidates); _ = restCandidates; return func() any { if sky_asBool(sky_stringIsEmpty(firstPath)) { return Compiler_Pipeline_LoadLocalModules(srcRoot, rest, acc) }; return func() any { return func() any { __subject := sky_fileRead(firstPath); if sky_asSkyResult(__subject).SkyName == "Err" { return Compiler_Pipeline_LoadFromCandidatesOrSkip(srcRoot, modName, restCandidates, rest, acc) };  if sky_asSkyResult(__subject).SkyName == "Ok" { source := sky_asSkyResult(__subject).OkValue; _ = source; return Compiler_Pipeline_ParseAndLoadSkydep(srcRoot, modName, source, restCandidates, rest, acc) };  return nil }() }() }() }()
}

func Compiler_Pipeline_ParseAndLoadSkydep(srcRoot any, modName any, source any, restCandidates any, rest any, acc any) any {
	return func() any { lexResult := Compiler_Lexer_Lex(source); _ = lexResult; return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if sky_asSkyResult(__subject).SkyName == "Err" { return Compiler_Pipeline_LoadFromCandidatesOrSkip(srcRoot, modName, restCandidates, rest, acc) };  if sky_asSkyResult(__subject).SkyName == "Ok" { depMod := sky_asSkyResult(__subject).OkValue; _ = depMod; return func() any { depSrcRoot := Compiler_Pipeline_SkydepSrcRoot(srcRoot, modName); _ = depSrcRoot; depTransImports := sky_call(sky_listFilter(func(i any) any { return sky_not(Compiler_Resolver_IsStdlib(sky_call(sky_stringJoin("."), sky_asMap(i)["moduleName"]))) }), sky_asMap(depMod)["imports"]); _ = depTransImports; withDep := Compiler_Pipeline_LoadLocalModules(depSrcRoot, depTransImports, append([]any{SkyTuple2{V0: modName, V1: depMod}}, sky_asList(acc)...)); _ = withDep; return Compiler_Pipeline_LoadLocalModules(srcRoot, rest, withDep) }() };  return nil }() }() }()
}

func Compiler_Pipeline_FindSkydepCandidates(skydepsDir any, moduleParts any) any {
	return func() any { moduleFile := sky_concat(sky_call(sky_stringJoin("/"), moduleParts), ".sky"); _ = moduleFile; listResult := sky_call(sky_processRun("sh"), []any{"-c", sky_concat("find ", sky_concat(skydepsDir, " -name 'src' -type d 2>/dev/null"))}); _ = listResult; return func() any { return func() any { __subject := listResult; if sky_asSkyResult(__subject).SkyName == "Ok" { output := sky_asSkyResult(__subject).OkValue; _ = output; return func() any { dirs := sky_call(sky_listFilter(func(d any) any { return sky_not(sky_stringIsEmpty(sky_stringTrim(d))) }), sky_call(sky_stringSplit("\n"), output)); _ = dirs; return sky_call(sky_listMap(func(d any) any { return sky_concat(d, sky_concat("/", moduleFile)) }), dirs) }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  return nil }() }() }()
}

func Compiler_Pipeline_SkydepSrcRoot(srcRoot any, modName any) any {
	return func() any { projectRoot := Compiler_Pipeline_DirOfPath(srcRoot); _ = projectRoot; skydepsDir := sky_concat(projectRoot, "/.skydeps"); _ = skydepsDir; findResult := sky_call(sky_processRun("sh"), []any{"-c", sky_concat("find ", sky_concat(skydepsDir, sky_concat(" -path '*/src/", sky_concat(modName, ".sky' 2>/dev/null | head -1"))))}); _ = findResult; return func() any { return func() any { __subject := findResult; if sky_asSkyResult(__subject).SkyName == "Ok" { path := sky_asSkyResult(__subject).OkValue; _ = path; return func() any { trimmed := sky_stringTrim(path); _ = trimmed; modFile := sky_concat("/", sky_concat(modName, ".sky")); _ = modFile; srcDir := sky_call2(sky_stringSlice(0), sky_asInt(sky_stringLength(trimmed)) - sky_asInt(sky_stringLength(modFile)), trimmed); _ = srcDir; return func() any { if sky_asBool(sky_stringIsEmpty(trimmed)) { return srcRoot }; return srcDir }() }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return srcRoot };  return nil }() }() }()
}

func Compiler_Pipeline_LoadFfiBindings(imports any) any {
	return sky_call(sky_listFilterMap(Compiler_Pipeline_LoadOneFfiBinding), imports)
}

func Compiler_Pipeline_LoadOneFfiBinding(imp any) any {
	return func() any { modName := sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]); _ = modName; return func() any { if sky_asBool(sky_asBool(Compiler_Resolver_IsStdlib(modName)) || sky_asBool(sky_equal(sky_asMap(imp)["alias_"], "_"))) { return SkyNothing() }; return func() any { skyiPath := Compiler_Resolver_ResolveBindingPath(modName); _ = skyiPath; return func() any { return func() any { __subject := sky_fileRead(skyiPath); if sky_asSkyResult(__subject).SkyName == "Ok" { skyiSource := sky_asSkyResult(__subject).OkValue; _ = skyiSource; return func() any { lexResult := Compiler_Lexer_Lex(skyiSource); _ = lexResult; return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if sky_asSkyResult(__subject).SkyName == "Ok" { skyiMod := sky_asSkyResult(__subject).OkValue; _ = skyiMod; return SkyJust(SkyTuple2{V0: modName, V1: skyiMod}) };  if sky_asSkyResult(__subject).SkyName == "Err" { return SkyNothing() };  if sky_asSkyResult(__subject).SkyName == "Err" { return SkyNothing() };  return nil }() }() }() };  return nil }() }() }() }() }()
}

func Compiler_Pipeline_TryLoadBinding(srcRoot any, modName any, rest any, acc any) any {
	return func() any { skyiPath := Compiler_Resolver_ResolveBindingPath(modName); _ = skyiPath; sky_println(sky_concat("   [FFI] Looking for binding: ", sky_concat(modName, sky_concat(" at ", skyiPath)))); return func() any { return func() any { __subject := sky_fileRead(skyiPath); if sky_asSkyResult(__subject).SkyName == "Ok" { skyiSource := sky_asSkyResult(__subject).OkValue; _ = skyiSource; return func() any { sky_println(sky_concat("   [FFI] Found binding for ", modName)); lexResult := Compiler_Lexer_Lex(skyiSource); _ = lexResult; return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if sky_asSkyResult(__subject).SkyName == "Ok" { skyiMod := sky_asSkyResult(__subject).OkValue; _ = skyiMod; return func() any { sky_println(sky_concat("   [FFI] Parsed binding: ", sky_concat(modName, sky_concat(" (", sky_concat(sky_stringFromInt(sky_listLength(sky_asMap(skyiMod)["declarations"])), " declarations)"))))); return Compiler_Pipeline_LoadLocalModules(srcRoot, rest, append([]any{SkyTuple2{V0: modName, V1: skyiMod}}, sky_asList(acc)...)) }() };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { sky_println(sky_concat("   [FFI] Parse error in binding ", sky_concat(modName, sky_concat(": ", e)))); return Compiler_Pipeline_LoadLocalModules(srcRoot, rest, acc) }() };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { sky_println(sky_concat("   [FFI] Binding not found: ", skyiPath)); return Compiler_Pipeline_LoadLocalModules(srcRoot, rest, acc) }() };  return nil }() }() }() };  return nil }() }() }()
}

func Compiler_Pipeline_CopyFfiWrappers(outDir any, imports any, loadedModules any) any {
	return func() any { sky_call2(sky_listFoldl(func(imp any) any { return func(_ any) any { return func() any { modName := sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]); _ = modName; return func() any { if sky_asBool(sky_asBool(Compiler_Resolver_IsStdlib(modName)) || sky_asBool(sky_equal(sky_asMap(imp)["alias_"], "_"))) { return struct{}{} }; return Compiler_Pipeline_CopyOneFfiWrapper(outDir, modName) }() }() } }), struct{}{}, imports); Compiler_Pipeline_CopyProjectWrappers(outDir); return struct{}{} }()
}

func Compiler_Pipeline_CopyProjectWrappers(outDir any) any {
	return func() any { dirs := []any{"dist/sky_wrappers", "sky_wrappers"}; _ = dirs; return sky_call2(sky_listFoldl(func(dir any) any { return func(_ any) any { return Compiler_Pipeline_CopyWrapperDir(outDir, dir) } }), struct{}{}, dirs) }()
}

func Compiler_Pipeline_CopyWrapperDir(outDir any, wrapperDir any) any {
	return func() any { return func() any { __subject := sky_call(sky_processRun("sh"), []any{"-c", sky_concat("ls ", sky_concat(wrapperDir, "/*.go 2>/dev/null"))}); if sky_asSkyResult(__subject).SkyName == "Ok" { output := sky_asSkyResult(__subject).OkValue; _ = output; return func() any { files := sky_call(sky_listFilter(func(f any) any { return sky_not(sky_stringIsEmpty(sky_stringTrim(f))) }), sky_call(sky_stringSplit("\n"), output)); _ = files; return sky_call2(sky_listFoldl(func(filePath any) any { return func(_ any) any { return func() any { if sky_asBool(sky_asBool(sky_call(sky_stringContains("00_sky_helpers"), filePath)) || sky_asBool(sky_call(sky_stringContains("fmt.go"), filePath))) { return struct{}{} }; return func() any { return func() any { __subject := sky_fileRead(filePath); if sky_asSkyResult(__subject).SkyName == "Ok" { content := sky_asSkyResult(__subject).OkValue; _ = content; return func() any { rewritten := sky_call2(sky_stringReplace("package sky_wrappers"), "package main", content); _ = rewritten; fileName := func() any { return func() any { __subject := sky_listReverse(sky_call(sky_stringSplit("/"), filePath)); if len(sky_asList(__subject)) > 0 { last := sky_asList(__subject)[0]; _ = last; return last };  if len(sky_asList(__subject)) == 0 { return "wrapper.go" };  return nil }() }(); _ = fileName; dst := sky_concat(outDir, sky_concat("/sky_", fileName)); _ = dst; sky_call(sky_fileWrite(dst), rewritten); return struct{}{} }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return struct{}{} };  return nil }() }() }() } }), struct{}{}, files) }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return struct{}{} };  return nil }() }()
}

func Compiler_Pipeline_CopyOneFfiWrapper(outDir any, modName any) any {
	return func() any { safeName := sky_call(sky_stringJoin("_"), sky_call(sky_listMap(Compiler_Pipeline_FfiSafePart), sky_call(sky_stringSplit("."), modName))); _ = safeName; wrapperSrc := sky_concat(".skycache/go/", sky_concat(safeName, sky_concat("/sky_wrappers/", sky_concat(safeName, ".go")))); _ = wrapperSrc; return func() any { return func() any { __subject := sky_fileRead(wrapperSrc); if sky_asSkyResult(__subject).SkyName == "Ok" { content := sky_asSkyResult(__subject).OkValue; _ = content; return func() any { rewritten := sky_call2(sky_stringReplace("package sky_wrappers"), "package main", content); _ = rewritten; wrapperDst := sky_concat(outDir, sky_concat("/sky_ffi_", sky_concat(safeName, ".go"))); _ = wrapperDst; sky_call(sky_fileWrite(wrapperDst), rewritten); return sky_println(sky_concat("   Copied wrapper: ", wrapperDst)) }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return struct{}{} };  return nil }() }() }()
}

func Compiler_Pipeline_BuildAliasMap(imports any) any {
	return sky_call2(sky_listFoldl(func(__ca0 any) any { return func(__ca1 any) any { return Compiler_Pipeline_AddImportAlias(__ca0, __ca1) } }), sky_dictEmpty(), imports)
}

func Compiler_Pipeline_GeneratePrefixAliases(prefix any, decls any) any {
	return sky_call(sky_listFilterMap(func(__pa0 any) any { return Compiler_Pipeline_MakePrefixAlias(prefix, __pa0) }), decls)
}

func Compiler_Pipeline_MakePrefixAlias(prefix any, decl any) any {
	return func() any { fullName := Compiler_Pipeline_GetDeclName(decl); _ = fullName; return func() any { if sky_asBool(sky_stringIsEmpty(fullName)) { return SkyNothing() }; return func() any { if sky_asBool(sky_not(sky_call(sky_stringStartsWith(prefix), fullName))) { return SkyNothing() }; return func() any { unprefixed := sky_call2(sky_stringSlice(sky_asInt(sky_stringLength(prefix)) + sky_asInt(1)), sky_stringLength(fullName), fullName); _ = unprefixed; firstChar := sky_call2(sky_stringSlice(0), 1, unprefixed); _ = firstChar; return func() any { if sky_asBool(sky_stringIsEmpty(unprefixed)) { return SkyNothing() }; return func() any { if sky_asBool(Compiler_Pipeline_IsCommonName(unprefixed)) { return SkyNothing() }; return SkyJust(GoDeclRaw(sky_concat("var ", sky_concat(unprefixed, sky_concat(" = ", fullName))))) }() }() }() }() }() }()
}

func Compiler_Pipeline_IsCommonName(name any) any {
	return sky_asBool(sky_equal(name, "main")) || sky_asBool(sky_equal(name, "_"))
}

func Compiler_Pipeline_IsSharedValue(name any) any {
	return sky_asBool(sky_equal(name, "emptySub")) || sky_asBool(sky_asBool(sky_equal(name, "emptySpan")) || sky_asBool(sky_asBool(sky_equal(name, "emptyResult")) || sky_asBool(sky_asBool(sky_equal(name, "emptyRegistry")) || sky_asBool(sky_asBool(sky_equal(name, "emptyCtx")) || sky_asBool(sky_asBool(sky_equal(name, "applySub")) || sky_asBool(sky_asBool(sky_equal(name, "freshVar")) || sky_asBool(sky_asBool(sky_equal(name, "unify")) || sky_asBool(sky_asBool(sky_equal(name, "composeSubs")) || sky_asBool(sky_asBool(sky_equal(name, "freeVars")) || sky_asBool(sky_asBool(sky_equal(name, "freeVarsInScheme")) || sky_asBool(sky_asBool(sky_equal(name, "instantiate")) || sky_asBool(sky_asBool(sky_equal(name, "generalize")) || sky_asBool(sky_asBool(sky_equal(name, "mono")) || sky_asBool(sky_asBool(sky_equal(name, "formatType")) || sky_asBool(sky_asBool(sky_equal(name, "applySubToScheme")) || sky_asBool(sky_asBool(sky_equal(name, "initState")) || sky_asBool(sky_asBool(sky_equal(name, "filterLayout")) || sky_asBool(sky_asBool(sky_equal(name, "consume")) || sky_asBool(sky_asBool(sky_equal(name, "consumeLex")) || sky_asBool(sky_asBool(sky_equal(name, "matchKind")) || sky_asBool(sky_asBool(sky_equal(name, "matchLexeme")) || sky_asBool(sky_asBool(sky_equal(name, "matchKindLex")) || sky_asBool(sky_asBool(sky_equal(name, "advance")) || sky_asBool(sky_asBool(sky_equal(name, "peek")) || sky_asBool(sky_asBool(sky_equal(name, "peekAt")) || sky_asBool(sky_asBool(sky_equal(name, "previous")) || sky_asBool(sky_asBool(sky_equal(name, "tokenKindEq")) || sky_asBool(sky_asBool(sky_equal(name, "tokenKindStr")) || sky_asBool(sky_asBool(sky_equal(name, "parseQualifiedParts")) || sky_asBool(sky_asBool(sky_equal(name, "isKeyword")) || sky_asBool(sky_asBool(sky_equal(name, "peekLexeme")) || sky_asBool(sky_asBool(sky_equal(name, "peekColumn")) || sky_asBool(sky_asBool(sky_equal(name, "peekKind")) || sky_asBool(sky_asBool(sky_equal(name, "peekAt1Kind")) || sky_asBool(sky_asBool(sky_equal(name, "getLexemeAt1")) || sky_asBool(sky_asBool(sky_equal(name, "dispatchDeclaration")) || sky_asBool(sky_asBool(sky_equal(name, "parseDeclsHelper")) || sky_asBool(sky_asBool(sky_equal(name, "addDeclAndContinue")) || sky_asBool(sky_asBool(sky_equal(name, "prependToResult")) || sky_asBool(sky_asBool(sky_equal(name, "buildVariant")) || sky_asBool(sky_asBool(sky_equal(name, "finishVariant")) || sky_asBool(sky_asBool(sky_equal(name, "prependVariant")) || sky_asBool(sky_asBool(sky_equal(name, "applyTypeArgs")) || sky_asBool(sky_asBool(sky_equal(name, "resolveTypeApp")) || sky_asBool(sky_asBool(sky_equal(name, "parseVariantFields")) || sky_asBool(sky_asBool(sky_equal(name, "parseTypeParams")) || sky_asBool(sky_asBool(sky_equal(name, "parseTypeVariants")) || sky_asBool(sky_asBool(sky_equal(name, "parseTypeExpr")) || sky_asBool(sky_asBool(sky_equal(name, "parseTypeApp")) || sky_asBool(sky_asBool(sky_equal(name, "parseTypeArgs")) || sky_asBool(sky_asBool(sky_equal(name, "parseTypePrimary")) || sky_asBool(sky_asBool(sky_equal(name, "parseTupleTypeRest")) || sky_asBool(sky_asBool(sky_equal(name, "parseRecordType")) || sky_asBool(sky_asBool(sky_equal(name, "parseRecordTypeFields")) || sky_asBool(sky_asBool(sky_equal(name, "parseExposingClause")) || sky_asBool(sky_asBool(sky_equal(name, "parseExposedItems")) || sky_asBool(sky_asBool(sky_equal(name, "parseModuleName")) || sky_asBool(sky_asBool(sky_equal(name, "parseModuleNameParts")) || sky_asBool(sky_asBool(sky_equal(name, "parseOptionalExposing")) || sky_asBool(sky_asBool(sky_equal(name, "parseImports")) || sky_asBool(sky_asBool(sky_equal(name, "parseImport")) || sky_asBool(sky_asBool(sky_equal(name, "parseForeignImport")) || sky_asBool(sky_asBool(sky_equal(name, "parseTypeAlias")) || sky_asBool(sky_asBool(sky_equal(name, "parseTypeDecl")) || sky_asBool(sky_asBool(sky_equal(name, "parseTypeAnnot")) || sky_asBool(sky_asBool(sky_equal(name, "parseFunDecl")) || sky_asBool(sky_asBool(sky_equal(name, "parseFunParams")) || sky_asBool(sky_asBool(sky_equal(name, "parseDeclaration")) || sky_asBool(sky_asBool(sky_equal(name, "parseDeclarations")) || sky_asBool(sky_asBool(sky_equal(name, "parseModule")) || sky_asBool(sky_asBool(sky_equal(name, "isStartOfPrimary")) || sky_asBool(sky_asBool(sky_equal(name, "getOperatorInfo")) || sky_asBool(sky_asBool(sky_equal(name, "parseExpr")) || sky_asBool(sky_asBool(sky_equal(name, "parseExprLoop")) || sky_asBool(sky_asBool(sky_equal(name, "parseApplication")) || sky_asBool(sky_asBool(sky_equal(name, "parseApplicationArgs")) || sky_asBool(sky_asBool(sky_equal(name, "parsePrimary")) || sky_asBool(sky_asBool(sky_equal(name, "parseCaseExpr")) || sky_asBool(sky_asBool(sky_equal(name, "parseCaseBranches")) || sky_asBool(sky_asBool(sky_equal(name, "parseIfExpr")) || sky_asBool(sky_asBool(sky_equal(name, "parseLetExpr")) || sky_asBool(sky_asBool(sky_equal(name, "parseLetBindings")) || sky_asBool(sky_asBool(sky_equal(name, "parseLambdaExpr")) || sky_asBool(sky_asBool(sky_equal(name, "parseLambdaParams")) || sky_asBool(sky_asBool(sky_equal(name, "parseRecordOrUpdate")) || sky_asBool(sky_asBool(sky_equal(name, "parseRecordFields")) || sky_asBool(sky_asBool(sky_equal(name, "parseParenOrTuple")) || sky_asBool(sky_asBool(sky_equal(name, "parseTupleRest")) || sky_asBool(sky_asBool(sky_equal(name, "parseListExpr")) || sky_asBool(sky_asBool(sky_equal(name, "parseListItems")) || sky_asBool(sky_asBool(sky_equal(name, "parseQualifiedOrConstructor")) || sky_asBool(sky_asBool(sky_equal(name, "parseFieldAccess")) || sky_asBool(sky_asBool(sky_equal(name, "parsePatternExpr")) || sky_asBool(sky_asBool(sky_equal(name, "parsePrimaryPattern")) || sky_asBool(sky_asBool(sky_equal(name, "parsePatternArgs")) || sky_asBool(sky_asBool(sky_equal(name, "parseTuplePatternRest")) || sky_asBool(sky_asBool(sky_equal(name, "parsePatternList")) || sky_asBool(sky_asBool(sky_equal(name, "parseVariantFields")) || sky_asBool(sky_equal(name, "parseTypeArgs"))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))
}

func Compiler_Pipeline_DeduplicateDecls(decls any) any {
	return Compiler_Pipeline_DeduplicateDeclsLoop(decls, sky_dictEmpty(), []any{})
}

func Compiler_Pipeline_DeduplicateDeclsLoop(decls any, seen any, acc any) any {
	return func() any { return func() any { __subject := decls; if len(sky_asList(__subject)) == 0 { return sky_listReverse(acc) };  if len(sky_asList(__subject)) > 0 { decl := sky_asList(__subject)[0]; _ = decl; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { name := Compiler_Pipeline_GetDeclName(decl); _ = name; return func() any { if sky_asBool(sky_stringIsEmpty(name)) { return Compiler_Pipeline_DeduplicateDeclsLoop(rest, seen, append([]any{decl}, sky_asList(acc)...)) }; return func() any { return func() any { __subject := sky_call(sky_dictGet(name), seen); if sky_asSkyMaybe(__subject).SkyName == "Just" { return Compiler_Pipeline_DeduplicateDeclsLoop(rest, seen, acc) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return Compiler_Pipeline_DeduplicateDeclsLoop(rest, sky_call2(sky_dictInsert(name), "1", seen), append([]any{decl}, sky_asList(acc)...)) };  return nil }() }() }() }() };  return nil }() }()
}

func Compiler_Pipeline_GetDeclName(decl any) any {
	return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "GoDeclFunc" { funcDecl := sky_asMap(__subject)["V0"]; _ = funcDecl; return sky_asMap(funcDecl)["name"] };  if sky_asMap(__subject)["SkyName"] == "GoDeclVar" { name := sky_asMap(__subject)["V0"]; _ = name; return name };  if sky_asMap(__subject)["SkyName"] == "GoDeclRaw" { code := sky_asMap(__subject)["V0"]; _ = code; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("var "), code)) { return Compiler_Pipeline_ExtractVarName(code) }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("func "), code)) { return Compiler_Pipeline_ExtractFuncName(code) }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("type "), code)) { return Compiler_Pipeline_ExtractTypeName(code) }; return "" }() }() }() };  return nil }() }()
}

func Compiler_Pipeline_ExtractVarName(code any) any {
	return func() any { afterVar := sky_call2(sky_stringSlice(4), sky_stringLength(code), code); _ = afterVar; spaceIdx := Compiler_Pipeline_FindCharIdx(string(' '), afterVar, 0); _ = spaceIdx; return func() any { if sky_asBool(sky_asInt(spaceIdx) > sky_asInt(0)) { return sky_call2(sky_stringSlice(0), spaceIdx, afterVar) }; return "" }() }()
}

func Compiler_Pipeline_ExtractFuncName(code any) any {
	return func() any { afterFunc := sky_call2(sky_stringSlice(5), sky_stringLength(code), code); _ = afterFunc; parenIdx := Compiler_Pipeline_FindCharIdx(string('('), afterFunc, 0); _ = parenIdx; return func() any { if sky_asBool(sky_asInt(parenIdx) > sky_asInt(0)) { return sky_call2(sky_stringSlice(0), parenIdx, afterFunc) }; return "" }() }()
}

func Compiler_Pipeline_ExtractTypeName(code any) any {
	return func() any { afterType := sky_call2(sky_stringSlice(5), sky_stringLength(code), code); _ = afterType; spaceIdx := Compiler_Pipeline_FindCharIdx(string(' '), afterType, 0); _ = spaceIdx; return func() any { if sky_asBool(sky_asInt(spaceIdx) > sky_asInt(0)) { return sky_call2(sky_stringSlice(0), spaceIdx, afterType) }; return "" }() }()
}

func Compiler_Pipeline_GenerateImportAliases(imports any, allModules any) any {
	return sky_call(sky_listConcatMap(func(__pa0 any) any { return Compiler_Pipeline_GenerateAliasesForImport(allModules, __pa0) }), imports)
}

func Compiler_Pipeline_GenerateAliasesForImport(allModules any, imp any) any {
	return func() any { modName := sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]); _ = modName; return func() any { if sky_asBool(Compiler_Resolver_IsStdlib(modName)) { return []any{} }; return Compiler_Pipeline_GenerateAliasesFromModule(modName, allModules) }() }()
}

func Compiler_Pipeline_GenerateAliasesFromModule(modName any, allModules any) any {
	return func() any { return func() any { __subject := Compiler_Pipeline_FindModule(modName, allModules); if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return []any{} };  if sky_asSkyMaybe(__subject).SkyName == "Just" { mod := sky_asSkyMaybe(__subject).JustValue; _ = mod; return func() any { prefix := sky_call2(sky_stringReplace("."), "_", modName); _ = prefix; declNames := Compiler_Pipeline_ExtractExportedNames(mod); _ = declNames; return sky_call(sky_listConcatMap(func(name any) any { return func() any { if sky_asBool(sky_stringIsEmpty(name)) { return []any{} }; return func() any { safeName := Compiler_Lower_SanitizeGoIdent(name); _ = safeName; prefixedName := sky_concat(prefix, sky_concat("_", Compiler_Pipeline_CapitalizeFirst(safeName))); _ = prefixedName; callSuffix := func() any { if sky_asBool(Compiler_Pipeline_IsZeroArityInModule(name, mod)) { return "()" }; return "" }(); _ = callSuffix; return []any{GoDeclRaw(sky_concat("var ", sky_concat(safeName, sky_concat(" = ", sky_concat(prefixedName, callSuffix))))), GoDeclRaw(sky_concat("var ", sky_concat(Compiler_Pipeline_CapitalizeFirst(safeName), sky_concat(" = ", sky_concat(prefixedName, callSuffix)))))} }() }() }), declNames) }() };  return nil }() }()
}

func Compiler_Pipeline_IsExposingAll(clause any) any {
	return sky_asBool(sky_not(Compiler_Pipeline_IsExposingNone(clause))) && sky_asBool(sky_listIsEmpty(Compiler_Pipeline_GetExposeNames(clause)))
}

func Compiler_Pipeline_IsExposingNone(clause any) any {
	return func() any { return func() any { __subject := clause; if sky_asMap(__subject)["SkyName"] == "ExposeNone" { return true };  if true { return false };  return nil }() }()
}

func Compiler_Pipeline_GetExposeNames(clause any) any {
	return func() any { return func() any { __subject := clause; if sky_asMap(__subject)["SkyName"] == "ExposeList" { names := sky_asMap(__subject)["V0"]; _ = names; return names };  if true { return []any{} };  return nil }() }()
}

func Compiler_Pipeline_IsZeroArityInModule(name any, mod any) any {
	return sky_call2(sky_listFoldl(func(decl any) any { return func(acc any) any { return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "FunDecl" { dName := sky_asMap(__subject)["V0"]; _ = dName; params := sky_asMap(__subject)["V1"]; _ = params; return func() any { if sky_asBool(sky_equal(dName, name)) { return sky_listIsEmpty(params) }; return acc }() };  if true { return acc };  return nil }() }() } }), false, sky_asMap(mod)["declarations"])
}

func Compiler_Pipeline_FindModule(modName any, modules any) any {
	return func() any { return func() any { __subject := modules; if len(sky_asList(__subject)) == 0 { return SkyNothing() };  if len(sky_asList(__subject)) > 0 { pair := sky_asList(__subject)[0]; _ = pair; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { if sky_asBool(sky_equal(sky_fst(pair), modName)) { return SkyJust(sky_snd(pair)) }; return Compiler_Pipeline_FindModule(modName, rest) }() };  return nil }() }()
}

func Compiler_Pipeline_ExtractExportedNames(mod any) any {
	return sky_call(sky_listConcatMap(Compiler_Pipeline_ExtractDeclNames), sky_asMap(mod)["declarations"])
}

func Compiler_Pipeline_ExtractDeclNames(decl any) any {
	return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "FunDecl" { name := sky_asMap(__subject)["V0"]; _ = name; return []any{name} };  if sky_asMap(__subject)["SkyName"] == "TypeDecl" { name := sky_asMap(__subject)["V0"]; _ = name; variants := sky_asMap(__subject)["V2"]; _ = variants; return sky_call(sky_listMap(func(v any) any { return sky_asMap(v)["name"] }), variants) };  if sky_asMap(__subject)["SkyName"] == "TypeAliasDecl" { name := sky_asMap(__subject)["V0"]; _ = name; return []any{} };  if sky_asMap(__subject)["SkyName"] == "TypeAnnotDecl" { return []any{} };  if sky_asMap(__subject)["SkyName"] == "ForeignImportDecl" { name := sky_asMap(__subject)["V0"]; _ = name; return []any{name} };  return nil }() }()
}

func Compiler_Pipeline_CollectAllFunctionNames(decls any) any {
	return Compiler_Pipeline_DeduplicateStringList(sky_call(sky_listFilterMap(Compiler_Pipeline_ExtractDeclNameForFn), decls))
}

func Compiler_Pipeline_ExtractDeclNameForFn(d any) any {
	return func() any { return func() any { __subject := d; if sky_asMap(__subject)["SkyName"] == "FunDecl" { name := sky_asMap(__subject)["V0"]; _ = name; return SkyJust(name) };  if true { return SkyNothing() };  return nil }() }()
}

func Compiler_Pipeline_DeduplicateStringList(items any) any {
	return Compiler_Pipeline_DeduplicateStringsLoop(items, sky_dictEmpty(), []any{})
}

func Compiler_Pipeline_DeduplicateStringsLoop(items any, seen any, acc any) any {
	return func() any { return func() any { __subject := items; if len(sky_asList(__subject)) == 0 { return sky_listReverse(acc) };  if len(sky_asList(__subject)) > 0 { item := sky_asList(__subject)[0]; _ = item; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := sky_call(sky_dictGet(item), seen); if sky_asSkyMaybe(__subject).SkyName == "Just" { return Compiler_Pipeline_DeduplicateStringsLoop(rest, seen, acc) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return Compiler_Pipeline_DeduplicateStringsLoop(rest, sky_call2(sky_dictInsert(item), "1", seen), append([]any{item}, sky_asList(acc)...)) };  return nil }() }() };  return nil }() }()
}

func Compiler_Pipeline_IsModuleLoaded(modName any, loaded any) any {
	return sky_call2(sky_listFoldl(func(pair any) any { return func(acc any) any { return sky_asBool(acc) || sky_asBool(sky_equal(sky_fst(pair), modName)) } }), false, loaded)
}

func Compiler_Pipeline_AddImportAlias(imp any, acc any) any {
	return func() any { modName := sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]); _ = modName; return func() any { if sky_asBool(Compiler_Resolver_IsStdlib(modName)) { return acc }; return sky_call2(sky_dictInsert(Compiler_Pipeline_ImportAlias(imp)), modName, acc) }() }()
}

func Compiler_Pipeline_ImportAlias(imp any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(sky_asMap(imp)["alias_"])) { return func() any { return func() any { __subject := sky_listReverse(sky_asMap(imp)["moduleName"]); if len(sky_asList(__subject)) > 0 { last := sky_asList(__subject)[0]; _ = last; return last };  if len(sky_asList(__subject)) == 0 { return "" };  return nil }() }() }; return sky_asMap(imp)["alias_"] }()
}

func Compiler_Pipeline_CompileDependencyModule(stdlibEnv any, allModules any, pair any) any {
	return func() any { modName := sky_fst(pair); _ = modName; mod := sky_snd(pair); _ = mod; prefix := sky_call2(sky_stringReplace("."), "_", modName); _ = prefix; checkResult := Compiler_Checker_CheckModule(mod, SkyJust(stdlibEnv)); _ = checkResult; registry := func() any { return func() any { __subject := checkResult; if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return sky_asMap(result)["registry"] };  if sky_asSkyResult(__subject).SkyName == "Err" { return Compiler_Adt_EmptyRegistry() };  return nil }() }(); _ = registry; depBaseCtx := Compiler_Lower_EmptyCtx(); _ = depBaseCtx; depAliasMap := Compiler_Pipeline_BuildAliasMap(sky_asMap(mod)["imports"]); _ = depAliasMap; localFns := Compiler_Pipeline_CollectAllFunctionNames(sky_asMap(mod)["declarations"]); _ = localFns; sky_println(sky_concat("   [DEBUG] ", sky_concat(modName, sky_concat(": ", sky_concat(sky_stringFromInt(sky_listLength(localFns)), sky_concat(" fns: ", sky_call(sky_stringJoin(","), localFns))))))); depExposed := Compiler_Lower_BuildExposedStdlib(sky_asMap(mod)["imports"]); _ = depExposed; ctx := sky_recordUpdate(depBaseCtx, map[string]any{"registry": registry, "localFunctions": localFns, "importedConstructors": Compiler_Lower_BuildConstructorMap(registry), "modulePrefix": prefix, "importAliases": depAliasMap, "localFunctionArity": Compiler_Lower_CollectLocalFunctionArities(sky_asMap(mod)["declarations"]), "exposedStdlib": depExposed}); _ = ctx; goDecls := Compiler_Lower_LowerDeclarations(ctx, sky_asMap(mod)["declarations"]); _ = goDecls; ctorDecls := Compiler_Lower_GenerateConstructorDecls(registry, sky_asMap(mod)["declarations"]); _ = ctorDecls; filtered := sky_call(sky_listFilter(Compiler_Pipeline_IsExportableDecl), sky_call(sky_listAppend(ctorDecls), goDecls)); _ = filtered; prefixed := sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Pipeline_PrefixDecl(prefix, __pa0) }), filtered); _ = prefixed; aliases := Compiler_Pipeline_GenerateConstructorAliases(prefix, prefixed); _ = aliases; depImportAliases := Compiler_Pipeline_GenerateImportAliases(sky_asMap(mod)["imports"], allModules); _ = depImportAliases; return Compiler_Pipeline_DeduplicateDecls(sky_listConcat([]any{aliases, depImportAliases, prefixed})) }()
}

func Compiler_Pipeline_GenerateConstructorAliases(prefix any, decls any) any {
	return sky_call(sky_listFilterMap(func(__pa0 any) any { return Compiler_Pipeline_MakeConstructorAlias(prefix, __pa0) }), decls)
}

func Compiler_Pipeline_MakeConstructorAlias(prefix any, decl any) any {
	return func() any { fullName := Compiler_Pipeline_GetDeclName(decl); _ = fullName; prefixLen := sky_asInt(sky_stringLength(prefix)) + sky_asInt(1); _ = prefixLen; unprefixed := func() any { if sky_asBool(sky_call(sky_stringStartsWith(sky_concat(prefix, "_")), fullName)) { return sky_call2(sky_stringSlice(prefixLen), sky_stringLength(fullName), fullName) }; return "" }(); _ = unprefixed; return func() any { if sky_asBool(sky_asBool(sky_stringIsEmpty(unprefixed)) || sky_asBool(sky_equal(unprefixed, "Main"))) { return SkyNothing() }; return func() any { firstChar := sky_call2(sky_stringSlice(0), 1, unprefixed); _ = firstChar; aliasName := func() any { if sky_asBool(Compiler_Pipeline_IsSharedValue(sky_concat(sky_stringToLower(sky_call2(sky_stringSlice(0), 1, unprefixed)), sky_call2(sky_stringSlice(1), sky_stringLength(unprefixed), unprefixed)))) { return sky_concat(sky_stringToLower(sky_call2(sky_stringSlice(0), 1, unprefixed)), sky_call2(sky_stringSlice(1), sky_stringLength(unprefixed), unprefixed)) }; return unprefixed }(); _ = aliasName; return func() any { if sky_asBool(sky_asBool(sky_equal(firstChar, sky_stringToUpper(firstChar))) || sky_asBool(Compiler_Pipeline_IsSharedValue(aliasName))) { return SkyJust(GoDeclRaw(sky_concat("var ", sky_concat(aliasName, sky_concat(" = ", fullName))))) }; return SkyNothing() }() }() }() }()
}

func Compiler_Pipeline_GenerateOriginalAliases(prefix any, decls any) any {
	return sky_call(sky_listFilterMap(func(__pa0 any) any { return Compiler_Pipeline_MakeOriginalAlias(prefix, __pa0) }), decls)
}

func Compiler_Pipeline_MakeOriginalAlias(prefix any, decl any) any {
	return func() any { name := Compiler_Pipeline_GetDeclName(decl); _ = name; return func() any { if sky_asBool(sky_asBool(sky_stringIsEmpty(name)) || sky_asBool(sky_asBool(sky_equal(name, "_")) || sky_asBool(sky_asBool(sky_equal(name, "main")) || sky_asBool(Compiler_Pipeline_IsCommonName(name))))) { return SkyNothing() }; return func() any { prefixedName := sky_concat(prefix, sky_concat("_", Compiler_Pipeline_CapitalizeFirst(name))); _ = prefixedName; return SkyJust(GoDeclRaw(sky_concat("var ", sky_concat(name, sky_concat(" = ", prefixedName))))) }() }() }()
}

func Compiler_Pipeline_IsExportableDecl(decl any) any {
	return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "GoDeclFunc" { funcDecl := sky_asMap(__subject)["V0"]; _ = funcDecl; return sky_asBool(!sky_equal(sky_asMap(funcDecl)["name"], "_")) && sky_asBool(!sky_equal(sky_asMap(funcDecl)["name"], "main")) };  if sky_asMap(__subject)["SkyName"] == "GoDeclVar" { name := sky_asMap(__subject)["V0"]; _ = name; return !sky_equal(name, "_") };  if sky_asMap(__subject)["SkyName"] == "GoDeclRaw" { return true };  return nil }() }()
}

func Compiler_Pipeline_DirOfPath(path any) any {
	return func() any { lastSlash := Compiler_Pipeline_FindLastSlash(path, sky_asInt(sky_stringLength(path)) - sky_asInt(1)); _ = lastSlash; return func() any { if sky_asBool(sky_asInt(lastSlash) > sky_asInt(0)) { return sky_call2(sky_stringSlice(0), lastSlash, path) }; return "." }() }()
}

func Compiler_Pipeline_NeedsStdlibWrapper(modName any) any {
	return sky_asBool(sky_call(sky_stringStartsWith("Sky.Core.Json"), modName)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Std.Html"), modName)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Std.Css"), modName)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Std.Live"), modName)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Std.Cmd"), modName)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Std.Sub"), modName)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Std.Task"), modName)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Std.Time"), modName)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Std.Program"), modName)) || sky_asBool(sky_asBool(sky_equal(modName, "Sky.Core.Result")) || sky_asBool(sky_equal(modName, "Sky.Core.Maybe")))))))))))
}

func Compiler_Pipeline_BuildStdlibGoImports(imports any) any {
	return func() any { stdImports := sky_call(sky_listFilterMap(Compiler_Pipeline_ImportToGoImport), imports); _ = stdImports; return append([]any{map[string]any{"path": "sky-out/sky_wrappers", "alias_": "sky_wrappers"}}, sky_asList(stdImports)...) }()
}

func Compiler_Pipeline_ImportToGoImport(imp any) any {
	return func() any { modName := sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]); _ = modName; return func() any { if sky_asBool(Compiler_Resolver_IsStdlib(modName)) { return SkyJust(map[string]any{"path": sky_concat("sky-out/", sky_call(sky_stringJoin("/"), sky_asMap(imp)["moduleName"])), "alias_": sky_concat("sky_", sky_stringToLower(sky_call2(sky_stringReplace("."), "_", modName)))}) }; return SkyNothing() }() }()
}

func Compiler_Pipeline_CopyStdlibGo(outDir any) any {
	return func() any { cpResult := sky_call(sky_processRun("cp"), []any{"-r", "sky-compiler/stdlib-go/Sky", sky_concat(outDir, "/")}); _ = cpResult; cpResult2 := sky_call(sky_processRun("cp"), []any{"-r", "sky-compiler/stdlib-go/Std", sky_concat(outDir, "/")}); _ = cpResult2; cpResult3 := sky_call(sky_processRun("cp"), []any{"-r", "sky-compiler/stdlib-go/sky_wrappers", sky_concat(outDir, "/")}); _ = cpResult3; return SkyOk(struct{}{}) }()
}

func Compiler_Pipeline_CapitalizeFirst(s any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(s)) { return "" }; return sky_concat(sky_stringToUpper(sky_call2(sky_stringSlice(0), 1, s)), sky_call2(sky_stringSlice(1), sky_stringLength(s), s)) }()
}

func Compiler_Pipeline_FfiSafePart(s any) any {
	return Compiler_Pipeline_FfiSafePartLoop(s, 0, "")
}

func Compiler_Pipeline_FfiSafePartLoop(s any, idx any, acc any) any {
	return func() any { if sky_asBool(sky_asInt(idx) >= sky_asInt(sky_stringLength(s))) { return sky_stringToLower(acc) }; return func() any { ch := sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1), s); _ = ch; isUpper := sky_asBool(sky_equal(ch, sky_stringToUpper(ch))) && sky_asBool(!sky_equal(ch, sky_stringToLower(ch))); _ = isUpper; needsUnderscore := sky_asBool(isUpper) && sky_asBool(sky_asInt(idx) > sky_asInt(0)); _ = needsUnderscore; return func() any { if sky_asBool(needsUnderscore) { return Compiler_Pipeline_FfiSafePartLoop(s, sky_asInt(idx) + sky_asInt(1), sky_concat(acc, sky_concat("_", ch))) }; return Compiler_Pipeline_FfiSafePartLoop(s, sky_asInt(idx) + sky_asInt(1), sky_concat(acc, ch)) }() }() }()
}

func Compiler_Pipeline_PrefixDecl(prefix any, decl any) any {
	return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "GoDeclFunc" { funcDecl := sky_asMap(__subject)["V0"]; _ = funcDecl; return func() any { if sky_asBool(sky_equal(sky_asMap(funcDecl)["name"], "main")) { return decl }; return GoDeclFunc(sky_recordUpdate(funcDecl, map[string]any{"name": sky_concat(prefix, sky_concat("_", Compiler_Pipeline_CapitalizeFirst(sky_asMap(funcDecl)["name"])))})) }() };  if sky_asMap(__subject)["SkyName"] == "GoDeclVar" { name := sky_asMap(__subject)["V0"]; _ = name; expr := sky_asMap(__subject)["V1"]; _ = expr; return func() any { if sky_asBool(sky_asBool(sky_equal(name, "_")) || sky_asBool(sky_call(sky_stringStartsWith("var _ ="), name))) { return decl }; return GoDeclVar(sky_concat(prefix, sky_concat("_", Compiler_Pipeline_CapitalizeFirst(name))), expr) }() };  if sky_asMap(__subject)["SkyName"] == "GoDeclRaw" { code := sky_asMap(__subject)["V0"]; _ = code; return decl };  return nil }() }()
}

func Compiler_Pipeline_CompileSource(filePath any, outDir any, source any) any {
	return func() any { sky_println(sky_concat("── Lexing ", filePath)); lexResult := Compiler_Lexer_Lex(source); _ = lexResult; sky_println(sky_concat("   ", sky_concat(sky_stringFromInt(sky_listLength(sky_asMap(lexResult)["tokens"])), " tokens"))); return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if sky_asSkyResult(__subject).SkyName == "Err" { parseErr := sky_asSkyResult(__subject).ErrValue; _ = parseErr; return SkyErr(sky_concat("Parse error: ", parseErr)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { mod := sky_asSkyResult(__subject).OkValue; _ = mod; return func() any { sky_println("── Parsing"); sky_println(sky_concat("   Module: ", sky_call(sky_stringJoin("."), sky_asMap(mod)["name"]))); sky_println(sky_concat("   ", sky_concat(sky_stringFromInt(sky_listLength(sky_asMap(mod)["declarations"])), sky_concat(" declarations, ", sky_concat(sky_stringFromInt(sky_listLength(sky_asMap(mod)["imports"])), " imports"))))); return Compiler_Pipeline_CompileModule(filePath, outDir, mod) }() };  return nil }() }() }()
}

func Compiler_Pipeline_CompileModule(filePath any, outDir any, mod any) any {
	return func() any { counter := sky_refNew(100); _ = counter; sky_println(sky_concat("── Type Checking (src: ", sky_concat(Compiler_Pipeline_InferSrcRoot(filePath, sky_asMap(mod)["name"]), ")"))); stdlibEnv := Compiler_Resolver_BuildStdlibEnv(); _ = stdlibEnv; checkResult := Compiler_Checker_CheckModule(mod, SkyJust(stdlibEnv)); _ = checkResult; return func() any { return func() any { __subject := checkResult; if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Type error: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return func() any { Compiler_Pipeline_PrintDiagnostics(sky_asMap(result)["diagnostics"]); Compiler_Pipeline_PrintTypedDecls(sky_asMap(result)["declarations"]); sky_println("── Lowering to Go IR"); goPackage := Compiler_Lower_LowerModule(sky_asMap(result)["registry"], mod); _ = goPackage; sky_println(sky_concat("   ", sky_concat(sky_stringFromInt(sky_listLength(sky_asMap(goPackage)["declarations"])), " Go declarations"))); sky_println("── Emitting Go"); goCode := Compiler_Emit_EmitPackage(goPackage); _ = goCode; outPath := sky_concat(outDir, "/main.go"); _ = outPath; sky_fileMkdirAll(outDir); sky_call(sky_fileWrite(outPath), goCode); sky_println(sky_concat("   Wrote ", outPath)); sky_println(""); sky_println("✓ Compilation successful"); return SkyOk(goCode) }() };  return nil }() }() }()
}

func Compiler_Pipeline_CompileProject(entryPath any, outDir any) any {
	return func() any { srcRoot := Compiler_Pipeline_InferSrcRootFromEntry(entryPath); _ = srcRoot; return func() any { return func() any { __subject := Compiler_Resolver_ResolveProject(entryPath, srcRoot); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { graph := sky_asSkyResult(__subject).OkValue; _ = graph; return func() any { return func() any { __subject := Compiler_Resolver_CheckAllModules(graph); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { checkedGraph := sky_asSkyResult(__subject).OkValue; _ = checkedGraph; return func() any { return func() any { __subject := sky_listReverse(sky_asMap(checkedGraph)["modules"]); if len(sky_asList(__subject)) == 0 { return SkyErr("No modules to compile") };  if len(sky_asList(__subject)) > 0 { entryMod := sky_asList(__subject)[0]; _ = entryMod; return func() any { registry := func() any { return func() any { __subject := sky_asMap(entryMod)["checkResult"]; if sky_asSkyMaybe(__subject).SkyName == "Just" { r := sky_asSkyMaybe(__subject).JustValue; _ = r; return sky_asMap(r)["registry"] };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return Compiler_Adt_EmptyRegistry() };  return nil }() }(); _ = registry; goPackage := Compiler_Lower_LowerModule(registry, sky_asMap(entryMod)["ast"]); _ = goPackage; goCode := Compiler_Emit_EmitPackage(goPackage); _ = goCode; outPath := sky_concat(outDir, "/main.go"); _ = outPath; sky_fileMkdirAll(outDir); sky_call(sky_fileWrite(outPath), goCode); return SkyOk(goCode) }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }()
}

func Compiler_Pipeline_InferSrcRoot(filePath any, moduleName any) any {
	return func() any { modulePath := sky_concat(sky_call(sky_stringJoin("/"), moduleName), ".sky"); _ = modulePath; modulePathLen := sky_stringLength(modulePath); _ = modulePathLen; filePathLen := sky_stringLength(filePath); _ = filePathLen; return func() any { if sky_asBool(sky_call(sky_stringEndsWith(modulePath), filePath)) { return func() any { rootLen := sky_asInt(sky_asInt(filePathLen) - sky_asInt(modulePathLen)) - sky_asInt(1); _ = rootLen; return func() any { if sky_asBool(sky_asInt(rootLen) > sky_asInt(0)) { return sky_call2(sky_stringSlice(0), rootLen, filePath) }; return "." }() }() }; return "src" }() }()
}

func Compiler_Pipeline_InferSrcRootFromEntry(entryPath any) any {
	return func() any { if sky_asBool(sky_call(sky_stringContains("/src/"), entryPath)) { return func() any { idx := Compiler_Pipeline_FindSubstring("/src/", entryPath); _ = idx; return sky_call2(sky_stringSlice(0), sky_asInt(idx) + sky_asInt(4), entryPath) }() }; return "src" }()
}

func Compiler_Pipeline_FindSubstring(needle any, haystack any) any {
	return Compiler_Pipeline_FindSubstringAt(needle, haystack, 0)
}

func Compiler_Pipeline_FindSubstringAt(needle any, haystack any, idx any) any {
	return func() any { if sky_asBool(sky_asInt(idx) >= sky_asInt(sky_stringLength(haystack))) { return 0 }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith(needle), sky_call2(sky_stringSlice(idx), sky_stringLength(haystack), haystack))) { return idx }; return Compiler_Pipeline_FindSubstringAt(needle, haystack, sky_asInt(idx) + sky_asInt(1)) }() }()
}

func Compiler_Pipeline_PrintDiagnostics(diags any) any {
	return func() any { return func() any { __subject := diags; if len(sky_asList(__subject)) == 0 { return struct{}{} };  if true { return func() any { sky_println(sky_concat("   ⚠ ", sky_concat(sky_stringFromInt(sky_listLength(diags)), " diagnostics:"))); sky_call(sky_listMap(func(d any) any { return sky_println(sky_concat("     ", d)) }), diags); return struct{}{} }() };  return nil }() }()
}

func Compiler_Pipeline_PrintTypedDecls(decls any) any {
	return func() any { sky_call(sky_listMap(func(d any) any { return sky_println(sky_concat("   ", sky_concat(sky_asMap(d)["name"], sky_concat(" : ", sky_asMap(d)["prettyType"])))) }), decls); return struct{}{} }()
}

var Compiler_Token_TkIdentifier = map[string]any{"Tag": 0, "SkyName": "TkIdentifier"}

var Compiler_Token_TkUpperIdentifier = map[string]any{"Tag": 1, "SkyName": "TkUpperIdentifier"}

var Compiler_Token_TkInteger = map[string]any{"Tag": 2, "SkyName": "TkInteger"}

var Compiler_Token_TkFloat = map[string]any{"Tag": 3, "SkyName": "TkFloat"}

var Compiler_Token_TkString = map[string]any{"Tag": 4, "SkyName": "TkString"}

var Compiler_Token_TkChar = map[string]any{"Tag": 5, "SkyName": "TkChar"}

var Compiler_Token_TkKeyword = map[string]any{"Tag": 6, "SkyName": "TkKeyword"}

var Compiler_Token_TkOperator = map[string]any{"Tag": 7, "SkyName": "TkOperator"}

var Compiler_Token_TkEquals = map[string]any{"Tag": 8, "SkyName": "TkEquals"}

var Compiler_Token_TkColon = map[string]any{"Tag": 9, "SkyName": "TkColon"}

var Compiler_Token_TkComma = map[string]any{"Tag": 10, "SkyName": "TkComma"}

var Compiler_Token_TkDot = map[string]any{"Tag": 11, "SkyName": "TkDot"}

var Compiler_Token_TkPipe = map[string]any{"Tag": 12, "SkyName": "TkPipe"}

var Compiler_Token_TkArrow = map[string]any{"Tag": 13, "SkyName": "TkArrow"}

var Compiler_Token_TkBackslash = map[string]any{"Tag": 14, "SkyName": "TkBackslash"}

var Compiler_Token_TkLParen = map[string]any{"Tag": 15, "SkyName": "TkLParen"}

var Compiler_Token_TkRParen = map[string]any{"Tag": 16, "SkyName": "TkRParen"}

var Compiler_Token_TkLBracket = map[string]any{"Tag": 17, "SkyName": "TkLBracket"}

var Compiler_Token_TkRBracket = map[string]any{"Tag": 18, "SkyName": "TkRBracket"}

var Compiler_Token_TkLBrace = map[string]any{"Tag": 19, "SkyName": "TkLBrace"}

var Compiler_Token_TkRBrace = map[string]any{"Tag": 20, "SkyName": "TkRBrace"}

var Compiler_Token_TkNewline = map[string]any{"Tag": 21, "SkyName": "TkNewline"}

var Compiler_Token_TkIndent = map[string]any{"Tag": 22, "SkyName": "TkIndent"}

var Compiler_Token_TkDedent = map[string]any{"Tag": 23, "SkyName": "TkDedent"}

var Compiler_Token_TkEOF = map[string]any{"Tag": 24, "SkyName": "TkEOF"}

func Compiler_Token_EmptySpan() any {
	return map[string]any{"start": map[string]any{"offset": 0, "line": 0, "column": 0}, "end": map[string]any{"offset": 0, "line": 0, "column": 0}}
}

func Compiler_Token_IsKeyword(word any) any {
	return sky_asBool(sky_equal(word, "module")) || sky_asBool(sky_asBool(sky_equal(word, "exposing")) || sky_asBool(sky_asBool(sky_equal(word, "import")) || sky_asBool(sky_asBool(sky_equal(word, "as")) || sky_asBool(sky_asBool(sky_equal(word, "type")) || sky_asBool(sky_asBool(sky_equal(word, "alias")) || sky_asBool(sky_asBool(sky_equal(word, "let")) || sky_asBool(sky_asBool(sky_equal(word, "in")) || sky_asBool(sky_asBool(sky_equal(word, "if")) || sky_asBool(sky_asBool(sky_equal(word, "then")) || sky_asBool(sky_asBool(sky_equal(word, "else")) || sky_asBool(sky_asBool(sky_equal(word, "case")) || sky_asBool(sky_asBool(sky_equal(word, "of")) || sky_asBool(sky_asBool(sky_equal(word, "foreign")) || sky_asBool(sky_asBool(sky_equal(word, "port")) || sky_asBool(sky_equal(word, "from"))))))))))))))))
}

func Compiler_Ast_IdentifierExpr(v0 any, v1 any) any {
	return map[string]any{"Tag": 0, "SkyName": "IdentifierExpr", "V0": v0, "V1": v1}
}

func Compiler_Ast_QualifiedExpr(v0 any, v1 any) any {
	return map[string]any{"Tag": 1, "SkyName": "QualifiedExpr", "V0": v0, "V1": v1}
}

func Compiler_Ast_IntLitExpr(v0 any, v1 any, v2 any) any {
	return map[string]any{"Tag": 2, "SkyName": "IntLitExpr", "V0": v0, "V1": v1, "V2": v2}
}

func Compiler_Ast_FloatLitExpr(v0 any, v1 any, v2 any) any {
	return map[string]any{"Tag": 3, "SkyName": "FloatLitExpr", "V0": v0, "V1": v1, "V2": v2}
}

func Compiler_Ast_StringLitExpr(v0 any, v1 any) any {
	return map[string]any{"Tag": 4, "SkyName": "StringLitExpr", "V0": v0, "V1": v1}
}

func Compiler_Ast_CharLitExpr(v0 any, v1 any) any {
	return map[string]any{"Tag": 5, "SkyName": "CharLitExpr", "V0": v0, "V1": v1}
}

func Compiler_Ast_BoolLitExpr(v0 any, v1 any) any {
	return map[string]any{"Tag": 6, "SkyName": "BoolLitExpr", "V0": v0, "V1": v1}
}

func Compiler_Ast_UnitExpr(v0 any) any {
	return map[string]any{"Tag": 7, "SkyName": "UnitExpr", "V0": v0}
}

func Compiler_Ast_TupleExpr(v0 any, v1 any) any {
	return map[string]any{"Tag": 8, "SkyName": "TupleExpr", "V0": v0, "V1": v1}
}

func Compiler_Ast_ListExpr(v0 any, v1 any) any {
	return map[string]any{"Tag": 9, "SkyName": "ListExpr", "V0": v0, "V1": v1}
}

func Compiler_Ast_RecordExpr(v0 any, v1 any) any {
	return map[string]any{"Tag": 10, "SkyName": "RecordExpr", "V0": v0, "V1": v1}
}

func Compiler_Ast_RecordUpdateExpr(v0 any, v1 any, v2 any) any {
	return map[string]any{"Tag": 11, "SkyName": "RecordUpdateExpr", "V0": v0, "V1": v1, "V2": v2}
}

func Compiler_Ast_FieldAccessExpr(v0 any, v1 any, v2 any) any {
	return map[string]any{"Tag": 12, "SkyName": "FieldAccessExpr", "V0": v0, "V1": v1, "V2": v2}
}

func Compiler_Ast_CallExpr(v0 any, v1 any, v2 any) any {
	return map[string]any{"Tag": 13, "SkyName": "CallExpr", "V0": v0, "V1": v1, "V2": v2}
}

func Compiler_Ast_LambdaExpr(v0 any, v1 any, v2 any) any {
	return map[string]any{"Tag": 14, "SkyName": "LambdaExpr", "V0": v0, "V1": v1, "V2": v2}
}

func Compiler_Ast_IfExpr(v0 any, v1 any, v2 any, v3 any) any {
	return map[string]any{"Tag": 15, "SkyName": "IfExpr", "V0": v0, "V1": v1, "V2": v2, "V3": v3}
}

func Compiler_Ast_LetExpr(v0 any, v1 any, v2 any) any {
	return map[string]any{"Tag": 16, "SkyName": "LetExpr", "V0": v0, "V1": v1, "V2": v2}
}

func Compiler_Ast_CaseExpr(v0 any, v1 any, v2 any) any {
	return map[string]any{"Tag": 17, "SkyName": "CaseExpr", "V0": v0, "V1": v1, "V2": v2}
}

func Compiler_Ast_BinaryExpr(v0 any, v1 any, v2 any, v3 any) any {
	return map[string]any{"Tag": 18, "SkyName": "BinaryExpr", "V0": v0, "V1": v1, "V2": v2, "V3": v3}
}

func Compiler_Ast_NegateExpr(v0 any, v1 any) any {
	return map[string]any{"Tag": 19, "SkyName": "NegateExpr", "V0": v0, "V1": v1}
}

func Compiler_Ast_ParenExpr(v0 any, v1 any) any {
	return map[string]any{"Tag": 20, "SkyName": "ParenExpr", "V0": v0, "V1": v1}
}

func Compiler_Ast_PWildcard(v0 any) any {
	return map[string]any{"Tag": 0, "SkyName": "PWildcard", "V0": v0}
}

func Compiler_Ast_PVariable(v0 any, v1 any) any {
	return map[string]any{"Tag": 1, "SkyName": "PVariable", "V0": v0, "V1": v1}
}

func Compiler_Ast_PConstructor(v0 any, v1 any, v2 any) any {
	return map[string]any{"Tag": 2, "SkyName": "PConstructor", "V0": v0, "V1": v1, "V2": v2}
}

func Compiler_Ast_PLiteral(v0 any, v1 any) any {
	return map[string]any{"Tag": 3, "SkyName": "PLiteral", "V0": v0, "V1": v1}
}

func Compiler_Ast_PTuple(v0 any, v1 any) any {
	return map[string]any{"Tag": 4, "SkyName": "PTuple", "V0": v0, "V1": v1}
}

func Compiler_Ast_PList(v0 any, v1 any) any {
	return map[string]any{"Tag": 5, "SkyName": "PList", "V0": v0, "V1": v1}
}

func Compiler_Ast_PCons(v0 any, v1 any, v2 any) any {
	return map[string]any{"Tag": 6, "SkyName": "PCons", "V0": v0, "V1": v1, "V2": v2}
}

func Compiler_Ast_PAs(v0 any, v1 any, v2 any) any {
	return map[string]any{"Tag": 7, "SkyName": "PAs", "V0": v0, "V1": v1, "V2": v2}
}

func Compiler_Ast_PRecord(v0 any, v1 any) any {
	return map[string]any{"Tag": 8, "SkyName": "PRecord", "V0": v0, "V1": v1}
}

func Compiler_Ast_LitInt(v0 any) any {
	return map[string]any{"Tag": 0, "SkyName": "LitInt", "V0": v0}
}

func Compiler_Ast_LitFloat(v0 any) any {
	return map[string]any{"Tag": 1, "SkyName": "LitFloat", "V0": v0}
}

func Compiler_Ast_LitString(v0 any) any {
	return map[string]any{"Tag": 2, "SkyName": "LitString", "V0": v0}
}

func Compiler_Ast_LitChar(v0 any) any {
	return map[string]any{"Tag": 3, "SkyName": "LitChar", "V0": v0}
}

func Compiler_Ast_LitBool(v0 any) any {
	return map[string]any{"Tag": 4, "SkyName": "LitBool", "V0": v0}
}

func Compiler_Ast_TypeRef(v0 any, v1 any, v2 any) any {
	return map[string]any{"Tag": 0, "SkyName": "TypeRef", "V0": v0, "V1": v1, "V2": v2}
}

func Compiler_Ast_TypeVar(v0 any, v1 any) any {
	return map[string]any{"Tag": 1, "SkyName": "TypeVar", "V0": v0, "V1": v1}
}

func Compiler_Ast_FunType(v0 any, v1 any, v2 any) any {
	return map[string]any{"Tag": 2, "SkyName": "FunType", "V0": v0, "V1": v1, "V2": v2}
}

func Compiler_Ast_RecordTypeExpr(v0 any, v1 any) any {
	return map[string]any{"Tag": 3, "SkyName": "RecordTypeExpr", "V0": v0, "V1": v1}
}

func Compiler_Ast_TupleTypeExpr(v0 any, v1 any) any {
	return map[string]any{"Tag": 4, "SkyName": "TupleTypeExpr", "V0": v0, "V1": v1}
}

func Compiler_Ast_UnitTypeExpr(v0 any) any {
	return map[string]any{"Tag": 5, "SkyName": "UnitTypeExpr", "V0": v0}
}

func Compiler_Ast_FunDecl(v0 any, v1 any, v2 any, v3 any) any {
	return map[string]any{"Tag": 0, "SkyName": "FunDecl", "V0": v0, "V1": v1, "V2": v2, "V3": v3}
}

func Compiler_Ast_TypeAnnotDecl(v0 any, v1 any, v2 any) any {
	return map[string]any{"Tag": 1, "SkyName": "TypeAnnotDecl", "V0": v0, "V1": v1, "V2": v2}
}

func Compiler_Ast_TypeDecl(v0 any, v1 any, v2 any, v3 any) any {
	return map[string]any{"Tag": 2, "SkyName": "TypeDecl", "V0": v0, "V1": v1, "V2": v2, "V3": v3}
}

func Compiler_Ast_TypeAliasDecl(v0 any, v1 any, v2 any, v3 any) any {
	return map[string]any{"Tag": 3, "SkyName": "TypeAliasDecl", "V0": v0, "V1": v1, "V2": v2, "V3": v3}
}

func Compiler_Ast_ForeignImportDecl(v0 any, v1 any, v2 any, v3 any) any {
	return map[string]any{"Tag": 4, "SkyName": "ForeignImportDecl", "V0": v0, "V1": v1, "V2": v2, "V3": v3}
}

var Compiler_Ast_ExposeAll = map[string]any{"Tag": 0, "SkyName": "ExposeAll"}

func Compiler_Ast_ExposeList(v0 any) any {
	return map[string]any{"Tag": 1, "SkyName": "ExposeList", "V0": v0}
}

var Compiler_Ast_ExposeNone = map[string]any{"Tag": 2, "SkyName": "ExposeNone"}

func Compiler_ParserExpr_ParseExpr(minPrec any, state any) any {
	return func() any { return func() any { __subject := Compiler_ParserExpr_ParseApplication(state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return Compiler_ParserExpr_ParseExprLoop(minPrec, sky_fst(pair), sky_snd(pair)) };  return nil }() }()
}

func Compiler_ParserExpr_ParseExprLoop(minPrec any, left any, state any) any {
	return func() any { if sky_asBool(matchKind(TkEquals, state)) { return SkyOk(SkyTuple2{V0: left, V1: state}) }; return func() any { if sky_asBool(sky_asInt(peekColumn(state)) <= sky_asInt(1)) { return SkyOk(SkyTuple2{V0: left, V1: state}) }; return func() any { if sky_asBool(matchKind(TkPipe, state)) { return SkyOk(SkyTuple2{V0: left, V1: state}) }; return func() any { if sky_asBool(sky_asBool(matchKind(TkIdentifier, state)) && sky_asBool(tokenKindEq(peekAt1Kind(state), TkColon))) { return SkyOk(SkyTuple2{V0: left, V1: state}) }; return func() any { if sky_asBool(matchKind(TkOperator, state)) { return func() any { opToken := peek(state); _ = opToken; info := Compiler_ParserExpr_GetOperatorInfo(sky_asMap(opToken)["lexeme"]); _ = info; return func() any { return func() any { __subject := info; if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyOk(SkyTuple2{V0: left, V1: state}) };  if sky_asSkyMaybe(__subject).SkyName == "Just" { pair := sky_asSkyMaybe(__subject).JustValue; _ = pair; return func() any { prec := sky_fst(pair); _ = prec; assoc := sky_snd(pair); _ = assoc; return func() any { if sky_asBool(sky_asInt(prec) < sky_asInt(minPrec)) { return SkyOk(SkyTuple2{V0: left, V1: state}) }; return func() any { __tup_advTok_s1 := advance(state); advTok := sky_asTuple2(__tup_advTok_s1).V0; _ = advTok; s1 := sky_asTuple2(__tup_advTok_s1).V1; _ = s1; nextMin := func() any { if sky_asBool(sky_equal(assoc, "left")) { return sky_asInt(prec) + sky_asInt(1) }; return prec }(); _ = nextMin; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(nextMin, s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { right := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = right; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return Compiler_ParserExpr_ParseExprLoop(minPrec, BinaryExpr(sky_asMap(opToken)["lexeme"], left, right, emptySpan), s2) };  return nil }() }() }() }() }() };  return nil }() }() }() }; return SkyOk(SkyTuple2{V0: left, V1: state}) }() }() }() }() }()
}

func Compiler_ParserExpr_GetOperatorInfo(op any) any {
	return func() any { if sky_asBool(sky_equal(op, "||")) { return SkyJust(SkyTuple2{V0: 2, V1: "right"}) }; return func() any { if sky_asBool(sky_equal(op, "&&")) { return SkyJust(SkyTuple2{V0: 3, V1: "right"}) }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "==")) || sky_asBool(sky_asBool(sky_equal(op, "!=")) || sky_asBool(sky_asBool(sky_equal(op, "/=")) || sky_asBool(sky_asBool(sky_equal(op, "<")) || sky_asBool(sky_asBool(sky_equal(op, "<=")) || sky_asBool(sky_asBool(sky_equal(op, ">")) || sky_asBool(sky_equal(op, ">=")))))))) { return SkyJust(SkyTuple2{V0: 4, V1: "left"}) }; return func() any { if sky_asBool(sky_equal(op, "++")) { return SkyJust(SkyTuple2{V0: 5, V1: "right"}) }; return func() any { if sky_asBool(sky_equal(op, "::")) { return SkyJust(SkyTuple2{V0: 5, V1: "right"}) }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "+")) || sky_asBool(sky_equal(op, "-"))) { return SkyJust(SkyTuple2{V0: 6, V1: "left"}) }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "*")) || sky_asBool(sky_asBool(sky_equal(op, "/")) || sky_asBool(sky_asBool(sky_equal(op, "//")) || sky_asBool(sky_equal(op, "%"))))) { return SkyJust(SkyTuple2{V0: 7, V1: "left"}) }; return func() any { if sky_asBool(sky_equal(op, "|>")) { return SkyJust(SkyTuple2{V0: 1, V1: "left"}) }; return func() any { if sky_asBool(sky_equal(op, "<|")) { return SkyJust(SkyTuple2{V0: 1, V1: "right"}) }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, ">>")) || sky_asBool(sky_equal(op, "<<"))) { return SkyJust(SkyTuple2{V0: 9, V1: "right"}) }; return SkyNothing() }() }() }() }() }() }() }() }() }() }()
}

func Compiler_ParserExpr_ParseApplication(state any) any {
	return func() any { fnCol := peekColumn(state); _ = fnCol; return func() any { return func() any { __subject := Compiler_ParserExpr_ParsePrimary(state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { fn := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = fn; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return Compiler_ParserExpr_ParseApplicationArgs(fnCol, fn, s1) };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseApplicationArgs(fnCol any, fn any, state any) any {
	return func() any { if sky_asBool(Compiler_ParserExpr_IsStartOfPrimary(state)) { return func() any { if sky_asBool(sky_asInt(peekColumn(state)) <= sky_asInt(1)) { return SkyOk(SkyTuple2{V0: fn, V1: state}) }; return func() any { if sky_asBool(sky_asInt(peekColumn(state)) < sky_asInt(fnCol)) { return SkyOk(SkyTuple2{V0: fn, V1: state}) }; return func() any { if sky_asBool(sky_asBool(matchKind(TkIdentifier, state)) && sky_asBool(tokenKindEq(peekAt1Kind(state), TkEquals))) { return SkyOk(SkyTuple2{V0: fn, V1: state}) }; return func() any { if sky_asBool(sky_asBool(matchKind(TkIdentifier, state)) && sky_asBool(sky_asInt(peekColumn(state)) <= sky_asInt(1))) { return SkyOk(SkyTuple2{V0: fn, V1: state}) }; return func() any { if sky_asBool(tokenKindEq(peekAt1Kind(state), TkArrow)) { return SkyOk(SkyTuple2{V0: fn, V1: state}) }; return func() any { if sky_asBool(matchKind(TkPipe, state)) { return SkyOk(SkyTuple2{V0: fn, V1: state}) }; return func() any { if sky_asBool(sky_asBool(matchKind(TkIdentifier, state)) && sky_asBool(tokenKindEq(peekAt1Kind(state), TkColon))) { return SkyOk(SkyTuple2{V0: fn, V1: state}) }; return func() any { return func() any { __subject := Compiler_ParserExpr_ParsePrimary(state); if sky_asSkyResult(__subject).SkyName == "Err" { return SkyOk(SkyTuple2{V0: fn, V1: state}) };  if sky_asSkyResult(__subject).SkyName == "Ok" { arg := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = arg; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return Compiler_ParserExpr_ParseApplicationArgs(fnCol, CallExpr(fn, []any{arg}, emptySpan), s1) };  return nil }() }() }() }() }() }() }() }() }() }; return SkyOk(SkyTuple2{V0: fn, V1: state}) }()
}

func Compiler_ParserExpr_IsStartOfPrimary(state any) any {
	return sky_asBool(matchKind(TkIdentifier, state)) || sky_asBool(sky_asBool(matchKind(TkUpperIdentifier, state)) || sky_asBool(sky_asBool(matchKind(TkInteger, state)) || sky_asBool(sky_asBool(matchKind(TkFloat, state)) || sky_asBool(sky_asBool(matchKind(TkString, state)) || sky_asBool(sky_asBool(matchKind(TkChar, state)) || sky_asBool(sky_asBool(matchKind(TkLParen, state)) || sky_asBool(sky_asBool(matchKind(TkLBrace, state)) || sky_asBool(sky_asBool(matchKind(TkLBracket, state)) || sky_asBool(sky_asBool(matchKind(TkBackslash, state)) || sky_asBool(sky_asBool(matchKindLex(TkKeyword, "case", state)) || sky_asBool(sky_asBool(matchKindLex(TkKeyword, "if", state)) || sky_asBool(matchKindLex(TkKeyword, "let", state)))))))))))))
}

func Compiler_ParserExpr_ParsePrimary(state any) any {
	return func() any { if sky_asBool(matchKindLex(TkKeyword, "case", state)) { return Compiler_ParserExpr_ParseCaseExpr(state) }; return func() any { if sky_asBool(matchKindLex(TkKeyword, "if", state)) { return Compiler_ParserExpr_ParseIfExpr(state) }; return func() any { if sky_asBool(matchKindLex(TkKeyword, "let", state)) { return Compiler_ParserExpr_ParseLetExpr(state) }; return func() any { if sky_asBool(matchKind(TkBackslash, state)) { return Compiler_ParserExpr_ParseLambdaExpr(state) }; return func() any { if sky_asBool(sky_asBool(matchKind(TkOperator, state)) && sky_asBool(sky_equal(peekLexeme(state), "-"))) { return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := Compiler_ParserExpr_ParsePrimary(s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { inner := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = inner; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return SkyOk(SkyTuple2{V0: NegateExpr(inner, emptySpan), V1: s2}) };  return nil }() }() }() }; return func() any { if sky_asBool(matchKind(TkLBrace, state)) { return Compiler_ParserExpr_ParseRecordOrUpdate(state) }; return func() any { if sky_asBool(matchKind(TkLParen, state)) { return Compiler_ParserExpr_ParseParenOrTuple(state) }; return func() any { if sky_asBool(matchKind(TkLBracket, state)) { return Compiler_ParserExpr_ParseListExpr(state) }; return func() any { if sky_asBool(matchKind(TkUpperIdentifier, state)) { return Compiler_ParserExpr_ParseQualifiedOrConstructor(state) }; return func() any { if sky_asBool(matchKind(TkIdentifier, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; return func() any { if sky_asBool(sky_asBool(matchKind(TkDot, s1)) && sky_asBool(matchKind(TkIdentifier, func() any { __tup_w_s := advance(s1); s := sky_asTuple2(__tup_w_s).V1; _ = s; return s }()))) { return Compiler_ParserExpr_ParseFieldAccess(tok, s1) }; return SkyOk(SkyTuple2{V0: IdentifierExpr(sky_asMap(tok)["lexeme"], sky_asMap(tok)["span"]), V1: s1}) }() }() }; return func() any { if sky_asBool(matchKind(TkInteger, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; return SkyOk(SkyTuple2{V0: IntLitExpr(0, sky_asMap(tok)["lexeme"], sky_asMap(tok)["span"]), V1: s1}) }() }; return func() any { if sky_asBool(matchKind(TkFloat, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; return SkyOk(SkyTuple2{V0: FloatLitExpr(0.0, sky_asMap(tok)["lexeme"], sky_asMap(tok)["span"]), V1: s1}) }() }; return func() any { if sky_asBool(matchKind(TkString, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; value := sky_call2(sky_stringSlice(1), sky_asInt(sky_stringLength(sky_asMap(tok)["lexeme"])) - sky_asInt(1), sky_asMap(tok)["lexeme"]); _ = value; return SkyOk(SkyTuple2{V0: StringLitExpr(value, sky_asMap(tok)["span"]), V1: s1}) }() }; return func() any { if sky_asBool(matchKind(TkChar, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; return SkyOk(SkyTuple2{V0: CharLitExpr(sky_asMap(tok)["lexeme"], sky_asMap(tok)["span"]), V1: s1}) }() }; return func() any { if sky_asBool(matchKindLex(TkKeyword, "True", state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; return SkyOk(SkyTuple2{V0: BoolLitExpr(true, sky_asMap(tok)["span"]), V1: s1}) }() }; return func() any { if sky_asBool(matchKindLex(TkKeyword, "False", state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; return SkyOk(SkyTuple2{V0: BoolLitExpr(false, sky_asMap(tok)["span"]), V1: s1}) }() }; return SkyErr }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }()
}

func Compiler_ParserExpr_ParseCaseExpr(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { subject := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = subject; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := consumeLex(TkKeyword, "of", s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { __tup_branches_s4 := Compiler_ParserExpr_ParseCaseBranches(s3); branches := sky_asTuple2(__tup_branches_s4).V0; _ = branches; s4 := sky_asTuple2(__tup_branches_s4).V1; _ = s4; return SkyOk(SkyTuple2{V0: CaseExpr(subject, branches, emptySpan), V1: s4}) }() };  return nil }() }() };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseCaseBranches(state any) any {
	return func() any { if sky_asBool(matchKind(TkEOF, state)) { return SkyTuple2{V0: []any{}, V1: state} }; return func() any { if sky_asBool(sky_asInt(peekColumn(state)) <= sky_asInt(1)) { return SkyTuple2{V0: []any{}, V1: state} }; return func() any { s0 := func() any { if sky_asBool(matchKind(TkPipe, state)) { return func() any { __tup_w_s := advance(state); s := sky_asTuple2(__tup_w_s).V1; _ = s; return s }() }; return state }(); _ = s0; return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(s0); if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: []any{}, V1: state} };  if sky_asSkyResult(__subject).SkyName == "Ok" { pat := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = pat; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { return func() any { __subject := consume(TkArrow, s1); if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: []any{}, V1: state} };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s2); if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: []any{}, V1: state} };  if sky_asSkyResult(__subject).SkyName == "Ok" { body := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = body; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { __tup_rest_s4 := Compiler_ParserExpr_ParseCaseBranches(s3); rest := sky_asTuple2(__tup_rest_s4).V0; _ = rest; s4 := sky_asTuple2(__tup_rest_s4).V1; _ = s4; return SkyTuple2{V0: append([]any{map[string]any{"pattern": pat, "body": body}}, sky_asList(rest)...), V1: s4} }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }() }() }()
}

func Compiler_ParserExpr_ParseIfExpr(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { cond := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = cond; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := consumeLex(TkKeyword, "then", s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s3); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { thenExpr := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = thenExpr; s4 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s4; return func() any { return func() any { __subject := consumeLex(TkKeyword, "else", s4); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s5 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s5; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s5); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { elseExpr := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = elseExpr; s6 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s6; return SkyOk(SkyTuple2{V0: IfExpr(cond, thenExpr, elseExpr, emptySpan), V1: s6}) };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseLetExpr(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; __tup_bindings_s2 := Compiler_ParserExpr_ParseLetBindings(s1); bindings := sky_asTuple2(__tup_bindings_s2).V0; _ = bindings; s2 := sky_asTuple2(__tup_bindings_s2).V1; _ = s2; return func() any { return func() any { __subject := consumeLex(TkKeyword, "in", s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s3); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { body := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = body; s4 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s4; return SkyOk(SkyTuple2{V0: LetExpr(bindings, body, emptySpan), V1: s4}) };  return nil }() }() };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseLetBindings(state any) any {
	return func() any { if sky_asBool(sky_asBool(matchKindLex(TkKeyword, "in", state)) || sky_asBool(matchKind(TkEOF, state))) { return SkyTuple2{V0: []any{}, V1: state} }; return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(state); if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: []any{}, V1: state} };  if sky_asSkyResult(__subject).SkyName == "Ok" { pat := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = pat; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { return func() any { __subject := consume(TkEquals, s1); if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: []any{}, V1: state} };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s2); if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: []any{}, V1: state} };  if sky_asSkyResult(__subject).SkyName == "Ok" { value := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = value; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { __tup_rest_s4 := Compiler_ParserExpr_ParseLetBindings(s3); rest := sky_asTuple2(__tup_rest_s4).V0; _ = rest; s4 := sky_asTuple2(__tup_rest_s4).V1; _ = s4; return SkyTuple2{V0: append([]any{map[string]any{"pattern": pat, "value": value}}, sky_asList(rest)...), V1: s4} }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseLambdaExpr(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; __tup_params_s2 := Compiler_ParserExpr_ParseLambdaParams(s1); params := sky_asTuple2(__tup_params_s2).V0; _ = params; s2 := sky_asTuple2(__tup_params_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkArrow, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s3); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { body := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = body; s4 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s4; return SkyOk(SkyTuple2{V0: LambdaExpr(params, body, emptySpan), V1: s4}) };  return nil }() }() };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseLambdaParams(state any) any {
	return func() any { if sky_asBool(matchKind(TkArrow, state)) { return SkyTuple2{V0: []any{}, V1: state} }; return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(state); if sky_asSkyResult(__subject).SkyName == "Ok" { pat := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = pat; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { __tup_rest_s2 := Compiler_ParserExpr_ParseLambdaParams(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return SkyTuple2{V0: append([]any{pat}, sky_asList(rest)...), V1: s2} }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: []any{}, V1: state} };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseRecordOrUpdate(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; t1 := peekAt(1, s1); _ = t1; return func() any { if sky_asBool(sky_asBool(matchKind(TkIdentifier, s1)) && sky_asBool(tokenKindEq(sky_asMap(t1)["kind"], TkPipe))) { return func() any { __tup_base_s2 := advance(s1); base := sky_asTuple2(__tup_base_s2).V0; _ = base; s2 := sky_asTuple2(__tup_base_s2).V1; _ = s2; __tup_w_s3 := advance(s2); s3 := sky_asTuple2(__tup_w_s3).V1; _ = s3; __tup_fields_s4 := Compiler_ParserExpr_ParseRecordFields(s3); fields := sky_asTuple2(__tup_fields_s4).V0; _ = fields; s4 := sky_asTuple2(__tup_fields_s4).V1; _ = s4; return func() any { return func() any { __subject := consume(TkRBrace, s4); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s5 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s5; return SkyOk(SkyTuple2{V0: RecordUpdateExpr(IdentifierExpr(sky_asMap(base)["lexeme"], sky_asMap(base)["span"]), fields, emptySpan), V1: s5}) };  return nil }() }() }() }; return func() any { __tup_fields_s2 := Compiler_ParserExpr_ParseRecordFields(s1); fields := sky_asTuple2(__tup_fields_s2).V0; _ = fields; s2 := sky_asTuple2(__tup_fields_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkRBrace, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk(SkyTuple2{V0: RecordExpr(fields, emptySpan), V1: s3}) };  return nil }() }() }() }() }()
}

func Compiler_ParserExpr_ParseRecordFields(state any) any {
	return func() any { if sky_asBool(matchKind(TkRBrace, state)) { return SkyTuple2{V0: []any{}, V1: state} }; return func() any { return func() any { __subject := consume(TkIdentifier, state); if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: []any{}, V1: state} };  if sky_asSkyResult(__subject).SkyName == "Ok" { name := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = name; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { return func() any { __subject := consume(TkEquals, s1); if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: []any{}, V1: state} };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s2); if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: []any{}, V1: state} };  if sky_asSkyResult(__subject).SkyName == "Ok" { value := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = value; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { s4 := func() any { if sky_asBool(matchKind(TkComma, s3)) { return func() any { __tup_w_s := advance(s3); s := sky_asTuple2(__tup_w_s).V1; _ = s; return s }() }; return s3 }(); _ = s4; __tup_rest_s5 := Compiler_ParserExpr_ParseRecordFields(s4); rest := sky_asTuple2(__tup_rest_s5).V0; _ = rest; s5 := sky_asTuple2(__tup_rest_s5).V1; _ = s5; return SkyTuple2{V0: append([]any{map[string]any{"name": sky_asMap(name)["lexeme"], "value": value}}, sky_asList(rest)...), V1: s5} }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseParenOrTuple(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkRParen, s1)) { return func() any { __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; return SkyOk(SkyTuple2{V0: UnitExpr(emptySpan), V1: s2}) }() }; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { first := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = first; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { if sky_asBool(matchKind(TkComma, s2)) { return Compiler_ParserExpr_ParseTupleRest([]any{first}, s2) }; return func() any { return func() any { __subject := consume(TkRParen, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk(SkyTuple2{V0: ParenExpr(first, emptySpan), V1: s3}) };  return nil }() }() }() };  return nil }() }() }() }()
}

func Compiler_ParserExpr_ParseTupleRest(items any, state any) any {
	return func() any { if sky_asBool(matchKind(TkComma, state)) { return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { item := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = item; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return Compiler_ParserExpr_ParseTupleRest(sky_concat(items, []any{item}), s2) };  return nil }() }() }() }; return func() any { return func() any { __subject := consume(TkRParen, state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return SkyOk(SkyTuple2{V0: TupleExpr(items, emptySpan), V1: s1}) };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseListExpr(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkRBracket, s1)) { return func() any { __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; return SkyOk(SkyTuple2{V0: ListExpr([]any{}, emptySpan), V1: s2}) }() }; return func() any { __tup_items_s2 := Compiler_ParserExpr_ParseListItems(s1); items := sky_asTuple2(__tup_items_s2).V0; _ = items; s2 := sky_asTuple2(__tup_items_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkRBracket, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk(SkyTuple2{V0: ListExpr(items, emptySpan), V1: s3}) };  return nil }() }() }() }() }()
}

func Compiler_ParserExpr_ParseListItems(state any) any {
	return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, state); if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: []any{}, V1: state} };  if sky_asSkyResult(__subject).SkyName == "Ok" { item := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = item; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkComma, s1)) { return func() any { __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; __tup_rest_s3 := Compiler_ParserExpr_ParseListItems(s2); rest := sky_asTuple2(__tup_rest_s3).V0; _ = rest; s3 := sky_asTuple2(__tup_rest_s3).V1; _ = s3; return SkyTuple2{V0: append([]any{item}, sky_asList(rest)...), V1: s3} }() }; return SkyTuple2{V0: []any{item}, V1: s1} }() };  return nil }() }()
}

func Compiler_ParserExpr_ParseQualifiedOrConstructor(state any) any {
	return func() any { __tup_id_s1 := advance(state); id := sky_asTuple2(__tup_id_s1).V0; _ = id; s1 := sky_asTuple2(__tup_id_s1).V1; _ = s1; __tup_parts_s2 := parseQualifiedParts([]any{sky_asMap(id)["lexeme"]}, s1); parts := sky_asTuple2(__tup_parts_s2).V0; _ = parts; s2 := sky_asTuple2(__tup_parts_s2).V1; _ = s2; return func() any { if sky_asBool(sky_asInt(sky_listLength(parts)) > sky_asInt(1)) { return SkyOk(SkyTuple2{V0: QualifiedExpr(parts, emptySpan), V1: s2}) }; return SkyOk(SkyTuple2{V0: IdentifierExpr(sky_asMap(id)["lexeme"], sky_asMap(id)["span"]), V1: s2}) }() }()
}

func Compiler_ParserExpr_ParseFieldAccess(base any, state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; __tup_field_s2 := advance(s1); field := sky_asTuple2(__tup_field_s2).V0; _ = field; s2 := sky_asTuple2(__tup_field_s2).V1; _ = s2; return SkyOk(SkyTuple2{V0: FieldAccessExpr(IdentifierExpr(sky_asMap(base)["lexeme"], sky_asMap(base)["span"]), sky_asMap(field)["lexeme"], emptySpan), V1: s2}) }()
}

func Compiler_ParserPattern_ParsePatternExpr(state any) any {
	return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePrimaryPattern(state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return func() any { pat := sky_fst(pair); _ = pat; s1 := sky_snd(pair); _ = s1; return func() any { if sky_asBool(sky_asBool(matchKind(TkOperator, s1)) && sky_asBool(sky_equal(peekLexeme(s1), "::"))) { return func() any { advResult := advance(s1); _ = advResult; s2 := sky_snd(advResult); _ = s2; return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { pair2 := sky_asSkyResult(__subject).OkValue; _ = pair2; return func() any { tail := sky_fst(pair2); _ = tail; s3 := sky_snd(pair2); _ = s3; return SkyOk(SkyTuple2{V0: PCons(pat, tail, emptySpan), V1: s3}) }() };  return nil }() }() }() }; return func() any { if sky_asBool(matchKindLex(TkKeyword, "as", s1)) { return func() any { advResult := advance(s1); _ = advResult; s2 := sky_snd(advResult); _ = s2; advResult2 := advance(s2); _ = advResult2; nameTok := sky_fst(advResult2); _ = nameTok; s3 := sky_snd(advResult2); _ = s3; return SkyOk(SkyTuple2{V0: PAs(pat, sky_asMap(nameTok)["lexeme"], emptySpan), V1: s3}) }() }; return SkyOk(SkyTuple2{V0: pat, V1: s1}) }() }() }() };  return nil }() }()
}

func Compiler_ParserPattern_ParsePrimaryPattern(state any) any {
	return func() any { if sky_asBool(matchKind(TkUpperIdentifier, state)) { return Compiler_ParserPattern_ParsePrimaryPatternUpper(state) }; return func() any { if sky_asBool(matchKind(TkIdentifier, state)) { return Compiler_ParserPattern_ParsePrimaryPatternIdent(state) }; return func() any { if sky_asBool(matchKind(TkInteger, state)) { return Compiler_ParserPattern_ParsePrimaryPatternInt(state) }; return func() any { if sky_asBool(matchKind(TkString, state)) { return Compiler_ParserPattern_ParsePrimaryPatternString(state) }; return func() any { if sky_asBool(matchKind(TkLParen, state)) { return Compiler_ParserPattern_ParsePrimaryPatternParen(state) }; return func() any { if sky_asBool(matchKind(TkLBracket, state)) { return Compiler_ParserPattern_ParsePrimaryPatternBracket(state) }; return SkyErr(sky_concat("Unexpected token in pattern: ", peekLexeme(state))) }() }() }() }() }() }()
}

func Compiler_ParserPattern_ParsePrimaryPatternUpper(state any) any {
	return func() any { advResult := advance(state); _ = advResult; id := sky_fst(advResult); _ = id; s1 := sky_snd(advResult); _ = s1; qualResult := parseQualifiedParts([]any{sky_asMap(id)["lexeme"]}, s1); _ = qualResult; parts := sky_fst(qualResult); _ = parts; s2 := sky_snd(qualResult); _ = s2; argsResult := Compiler_ParserPattern_ParsePatternArgs(s2); _ = argsResult; args := sky_fst(argsResult); _ = args; s3 := sky_snd(argsResult); _ = s3; return SkyOk(SkyTuple2{V0: PConstructor(parts, args, emptySpan), V1: s3}) }()
}

func Compiler_ParserPattern_ParsePrimaryPatternIdent(state any) any {
	return func() any { advResult := advance(state); _ = advResult; id := sky_fst(advResult); _ = id; s1 := sky_snd(advResult); _ = s1; return func() any { if sky_asBool(sky_equal(sky_asMap(id)["lexeme"], "_")) { return SkyOk(SkyTuple2{V0: PWildcard(emptySpan), V1: s1}) }; return SkyOk(SkyTuple2{V0: PVariable(sky_asMap(id)["lexeme"], emptySpan), V1: s1}) }() }()
}

func Compiler_ParserPattern_ParsePrimaryPatternInt(state any) any {
	return func() any { advResult := advance(state); _ = advResult; tok := sky_fst(advResult); _ = tok; s1 := sky_snd(advResult); _ = s1; return func() any { return func() any { __subject := sky_stringToInt(sky_asMap(tok)["lexeme"]); if sky_asSkyMaybe(__subject).SkyName == "Just" { n := sky_asSkyMaybe(__subject).JustValue; _ = n; return SkyOk(SkyTuple2{V0: PLiteral(LitInt(n), emptySpan), V1: s1}) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyOk(SkyTuple2{V0: PLiteral(LitInt(0), emptySpan), V1: s1}) };  return nil }() }() }()
}

func Compiler_ParserPattern_ParsePrimaryPatternString(state any) any {
	return func() any { advResult := advance(state); _ = advResult; tok := sky_fst(advResult); _ = tok; s1 := sky_snd(advResult); _ = s1; return SkyOk(SkyTuple2{V0: PLiteral(LitString(sky_asMap(tok)["lexeme"]), emptySpan), V1: s1}) }()
}

func Compiler_ParserPattern_ParsePrimaryPatternParen(state any) any {
	return func() any { advResult := advance(state); _ = advResult; s1 := sky_snd(advResult); _ = s1; return func() any { if sky_asBool(matchKind(TkRParen, s1)) { return func() any { advResult2 := advance(s1); _ = advResult2; s2 := sky_snd(advResult2); _ = s2; return SkyOk(SkyTuple2{V0: PLiteral(LitString("()"), emptySpan), V1: s2}) }() }; return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return Compiler_ParserPattern_ParsePrimaryPatternParenCont(sky_fst(pair), sky_snd(pair)) };  return nil }() }() }() }()
}

func Compiler_ParserPattern_ParsePrimaryPatternParenCont(first any, s2 any) any {
	return func() any { if sky_asBool(matchKind(TkComma, s2)) { return Compiler_ParserPattern_ParseTuplePatternRest([]any{first}, s2) }; return func() any { return func() any { __subject := consume(TkRParen, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return SkyOk(SkyTuple2{V0: first, V1: sky_snd(pair)}) };  return nil }() }() }()
}

func Compiler_ParserPattern_ParsePrimaryPatternBracket(state any) any {
	return func() any { advResult := advance(state); _ = advResult; s1 := sky_snd(advResult); _ = s1; return func() any { if sky_asBool(matchKind(TkRBracket, s1)) { return func() any { advResult2 := advance(s1); _ = advResult2; s2 := sky_snd(advResult2); _ = s2; return SkyOk(SkyTuple2{V0: PList([]any{}, emptySpan), V1: s2}) }() }; return func() any { listResult := Compiler_ParserPattern_ParsePatternList(s1); _ = listResult; items := sky_fst(listResult); _ = items; s2 := sky_snd(listResult); _ = s2; return func() any { return func() any { __subject := consume(TkRBracket, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return SkyOk(SkyTuple2{V0: PList(items, emptySpan), V1: sky_snd(pair)}) };  return nil }() }() }() }() }()
}

func Compiler_ParserPattern_ParsePatternArgs(state any) any {
	return func() any { if sky_asBool(sky_asBool(matchKind(TkIdentifier, state)) || sky_asBool(sky_asBool(matchKind(TkUpperIdentifier, state)) || sky_asBool(matchKind(TkLParen, state)))) { return func() any { if sky_asBool(sky_asBool(matchKind(TkArrow, state)) || sky_asBool(sky_asInt(peekColumn(state)) <= sky_asInt(1))) { return SkyTuple2{V0: []any{}, V1: state} }; return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePrimaryPattern(state); if sky_asSkyResult(__subject).SkyName == "Ok" { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return func() any { pat := sky_fst(pair); _ = pat; s1 := sky_snd(pair); _ = s1; restResult := Compiler_ParserPattern_ParsePatternArgs(s1); _ = restResult; rest := sky_fst(restResult); _ = rest; s2 := sky_snd(restResult); _ = s2; return SkyTuple2{V0: append([]any{pat}, sky_asList(rest)...), V1: s2} }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: []any{}, V1: state} };  return nil }() }() }() }; return SkyTuple2{V0: []any{}, V1: state} }()
}

func Compiler_ParserPattern_ParseTuplePatternRest(items any, state any) any {
	return func() any { if sky_asBool(matchKind(TkComma, state)) { return func() any { advResult := advance(state); _ = advResult; s1 := sky_snd(advResult); _ = s1; return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return Compiler_ParserPattern_ParseTuplePatternRest(sky_concat(items, []any{sky_fst(pair)}), sky_snd(pair)) };  return nil }() }() }() }; return func() any { return func() any { __subject := consume(TkRParen, state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return SkyOk(SkyTuple2{V0: PTuple(items, emptySpan), V1: sky_snd(pair)}) };  return nil }() }() }()
}

func Compiler_ParserPattern_ParsePatternList(state any) any {
	return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(state); if sky_asSkyResult(__subject).SkyName == "Err" { return SkyTuple2{V0: []any{}, V1: state} };  if sky_asSkyResult(__subject).SkyName == "Ok" { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return func() any { item := sky_fst(pair); _ = item; s1 := sky_snd(pair); _ = s1; return func() any { if sky_asBool(matchKind(TkComma, s1)) { return func() any { advResult := advance(s1); _ = advResult; s2 := sky_snd(advResult); _ = s2; restResult := Compiler_ParserPattern_ParsePatternList(s2); _ = restResult; rest := sky_fst(restResult); _ = rest; s3 := sky_snd(restResult); _ = s3; return SkyTuple2{V0: append([]any{item}, sky_asList(rest)...), V1: s3} }() }; return SkyTuple2{V0: []any{item}, V1: s1} }() }() };  return nil }() }()
}

func Compiler_Emit_EmitPackage(pkg any) any {
	return func() any { header := sky_concat("package ", sky_concat(sky_asMap(pkg)["name"], "\n\n")); _ = header; imports := func() any { if sky_asBool(sky_listIsEmpty(sky_asMap(pkg)["imports"])) { return "" }; return sky_concat(Compiler_Emit_EmitImports(sky_asMap(pkg)["imports"]), "\n") }(); _ = imports; decls := sky_call(sky_stringJoin("\n\n"), sky_call(sky_listMap(Compiler_Emit_EmitDecl), sky_asMap(pkg)["declarations"])); _ = decls; return sky_concat(header, sky_concat(imports, sky_concat(decls, "\n"))) }()
}

func Compiler_Emit_EmitImports(imports any) any {
	return func() any { lines := sky_call(sky_listMap(func(imp any) any { return func() any { if sky_asBool(sky_equal(sky_asMap(imp)["alias_"], "")) { return sky_concat("\t\"", sky_concat(sky_asMap(imp)["path"], "\"")) }; return sky_concat("\t", sky_concat(sky_asMap(imp)["alias_"], sky_concat(" \"", sky_concat(sky_asMap(imp)["path"], "\"")))) }() }), imports); _ = lines; return sky_concat("import (\n", sky_concat(sky_call(sky_stringJoin("\n"), lines), "\n)\n")) }()
}

func Compiler_Emit_EmitDecl(decl any) any {
	return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "GoDeclFunc" { fd := sky_asMap(__subject)["V0"]; _ = fd; return Compiler_Emit_EmitFuncDecl(fd) };  if sky_asMap(__subject)["SkyName"] == "GoDeclVar" { name := sky_asMap(__subject)["V0"]; _ = name; expr := sky_asMap(__subject)["V1"]; _ = expr; return sky_concat("var ", sky_concat(name, sky_concat(" = ", Compiler_Emit_EmitExpr(expr)))) };  if sky_asMap(__subject)["SkyName"] == "GoDeclRaw" { code := sky_asMap(__subject)["V0"]; _ = code; return code };  return nil }() }()
}

func Compiler_Emit_EmitFuncDecl(fd any) any {
	return func() any { params := sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Emit_EmitParam), sky_asMap(fd)["params"])); _ = params; ret := func() any { if sky_asBool(sky_equal(sky_asMap(fd)["returnType"], "")) { return "" }; return sky_concat(" ", sky_asMap(fd)["returnType"]) }(); _ = ret; body := sky_call(sky_stringJoin("\n"), sky_call(sky_listMap(func(s any) any { return sky_concat("\t", Compiler_Emit_EmitStmt(s)) }), sky_asMap(fd)["body"])); _ = body; return sky_concat("func ", sky_concat(sky_asMap(fd)["name"], sky_concat("(", sky_concat(params, sky_concat(")", sky_concat(ret, sky_concat(" {\n", sky_concat(body, "\n}")))))))) }()
}

func Compiler_Emit_EmitParam(p any) any {
	return sky_concat(sky_asMap(p)["name"], sky_concat(" ", sky_asMap(p)["type_"]))
}

func Compiler_Emit_EmitStmt(stmt any) any {
	return func() any { return func() any { __subject := stmt; if sky_asMap(__subject)["SkyName"] == "GoExprStmt" { expr := sky_asMap(__subject)["V0"]; _ = expr; return Compiler_Emit_EmitExpr(expr) };  if sky_asMap(__subject)["SkyName"] == "GoAssign" { name := sky_asMap(__subject)["V0"]; _ = name; expr := sky_asMap(__subject)["V1"]; _ = expr; return sky_concat(name, sky_concat(" = ", Compiler_Emit_EmitExpr(expr))) };  if sky_asMap(__subject)["SkyName"] == "GoShortDecl" { name := sky_asMap(__subject)["V0"]; _ = name; expr := sky_asMap(__subject)["V1"]; _ = expr; return sky_concat(name, sky_concat(" := ", Compiler_Emit_EmitExpr(expr))) };  if sky_asMap(__subject)["SkyName"] == "GoReturn" { expr := sky_asMap(__subject)["V0"]; _ = expr; return sky_concat("return ", Compiler_Emit_EmitExpr(expr)) };  if sky_asMap(__subject)["SkyName"] == "GoReturnVoid" { return "return" };  if sky_asMap(__subject)["SkyName"] == "GoIf" { cond := sky_asMap(__subject)["V0"]; _ = cond; thenStmts := sky_asMap(__subject)["V1"]; _ = thenStmts; elseStmts := sky_asMap(__subject)["V2"]; _ = elseStmts; return func() any { thenBody := sky_call(sky_stringJoin("\n\t"), sky_call(sky_listMap(Compiler_Emit_EmitStmt), thenStmts)); _ = thenBody; elseBody := sky_call(sky_stringJoin("\n\t"), sky_call(sky_listMap(Compiler_Emit_EmitStmt), elseStmts)); _ = elseBody; return func() any { if sky_asBool(sky_listIsEmpty(elseStmts)) { return sky_concat("if ", sky_concat(Compiler_Emit_EmitExpr(cond), sky_concat(" {\n\t", sky_concat(thenBody, "\n}")))) }; return sky_concat("if ", sky_concat(Compiler_Emit_EmitExpr(cond), sky_concat(" {\n\t", sky_concat(thenBody, sky_concat("\n} else {\n\t", sky_concat(elseBody, "\n}")))))) }() }() };  if sky_asMap(__subject)["SkyName"] == "GoBlock" { stmts := sky_asMap(__subject)["V0"]; _ = stmts; return sky_call(sky_stringJoin("\n\t"), sky_call(sky_listMap(Compiler_Emit_EmitStmt), stmts)) };  return nil }() }()
}

func Compiler_Emit_EmitExpr(expr any) any {
	return func() any { return func() any { __subject := expr; if sky_asMap(__subject)["SkyName"] == "GoIdent" { name := sky_asMap(__subject)["V0"]; _ = name; return name };  if sky_asMap(__subject)["SkyName"] == "GoBasicLit" { value := sky_asMap(__subject)["V0"]; _ = value; return value };  if sky_asMap(__subject)["SkyName"] == "GoStringLit" { value := sky_asMap(__subject)["V0"]; _ = value; return Compiler_Emit_GoQuote(value) };  if sky_asMap(__subject)["SkyName"] == "GoCallExpr" { fn := sky_asMap(__subject)["V0"]; _ = fn; args := sky_asMap(__subject)["V1"]; _ = args; return sky_concat(Compiler_Emit_EmitExpr(fn), sky_concat("(", sky_concat(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Emit_EmitExpr), args)), ")"))) };  if sky_asMap(__subject)["SkyName"] == "GoSelectorExpr" { target := sky_asMap(__subject)["V0"]; _ = target; sel := sky_asMap(__subject)["V1"]; _ = sel; return sky_concat(Compiler_Emit_EmitExpr(target), sky_concat(".", sel)) };  if sky_asMap(__subject)["SkyName"] == "GoSliceLit" { elems := sky_asMap(__subject)["V0"]; _ = elems; return sky_concat("[]any{", sky_concat(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Emit_EmitExpr), elems)), "}")) };  if sky_asMap(__subject)["SkyName"] == "GoMapLit" { entries := sky_asMap(__subject)["V0"]; _ = entries; return func() any { pairs := sky_call(sky_listMap(func(pair any) any { return sky_concat(Compiler_Emit_EmitExpr(sky_fst(pair)), sky_concat(": ", Compiler_Emit_EmitExpr(sky_snd(pair)))) }), entries); _ = pairs; return sky_concat("map[string]any{", sky_concat(sky_call(sky_stringJoin(", "), pairs), "}")) }() };  if sky_asMap(__subject)["SkyName"] == "GoFuncLit" { params := sky_asMap(__subject)["V0"]; _ = params; body := sky_asMap(__subject)["V1"]; _ = body; return func() any { ps := sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Emit_EmitParam), params)); _ = ps; return sky_concat("func(", sky_concat(ps, sky_concat(") any { return ", sky_concat(Compiler_Emit_EmitExpr(body), " }")))) }() };  if sky_asMap(__subject)["SkyName"] == "GoRawExpr" { code := sky_asMap(__subject)["V0"]; _ = code; return code };  if sky_asMap(__subject)["SkyName"] == "GoCompositeLit" { typeName := sky_asMap(__subject)["V0"]; _ = typeName; fields := sky_asMap(__subject)["V1"]; _ = fields; return func() any { fs := sky_call(sky_listMap(func(pair any) any { return sky_concat(sky_fst(pair), sky_concat(": ", Compiler_Emit_EmitExpr(sky_snd(pair)))) }), fields); _ = fs; return sky_concat(typeName, sky_concat("{", sky_concat(sky_call(sky_stringJoin(", "), fs), "}"))) }() };  if sky_asMap(__subject)["SkyName"] == "GoBinaryExpr" { op := sky_asMap(__subject)["V0"]; _ = op; left := sky_asMap(__subject)["V1"]; _ = left; right := sky_asMap(__subject)["V2"]; _ = right; return sky_concat(Compiler_Emit_EmitExpr(left), sky_concat(" ", sky_concat(op, sky_concat(" ", Compiler_Emit_EmitExpr(right))))) };  if sky_asMap(__subject)["SkyName"] == "GoUnaryExpr" { op := sky_asMap(__subject)["V0"]; _ = op; operand := sky_asMap(__subject)["V1"]; _ = operand; return sky_concat(op, Compiler_Emit_EmitExpr(operand)) };  if sky_asMap(__subject)["SkyName"] == "GoIndexExpr" { target := sky_asMap(__subject)["V0"]; _ = target; index := sky_asMap(__subject)["V1"]; _ = index; return sky_concat(Compiler_Emit_EmitExpr(target), sky_concat("[", sky_concat(Compiler_Emit_EmitExpr(index), "]"))) };  if sky_asMap(__subject)["SkyName"] == "GoNilExpr" { return "nil" };  return nil }() }()
}

func Compiler_Emit_GoQuote(s any) any {
	return sky_concat("\"", sky_concat(s, "\""))
}

func Compiler_Env_Empty() any {
	return sky_dictEmpty()
}

func Compiler_Env_Lookup(name any, env any) any {
	return sky_call(sky_dictGet(name), env)
}

func Compiler_Env_Extend(name any, scheme any, env any) any {
	return sky_call2(sky_dictInsert(name), scheme, env)
}

func Compiler_Env_ExtendMany(bindings any, env any) any {
	return sky_call2(sky_listFoldl(func(pair any) any { return func(acc any) any { return sky_call2(sky_dictInsert(sky_fst(pair)), sky_snd(pair), acc) } }), env, bindings)
}

func Compiler_Env_Remove(name any, env any) any {
	return sky_call(sky_dictRemove(name), env)
}

func Compiler_Env_Keys(env any) any {
	return sky_dictKeys(env)
}

func Compiler_Env_ToList(env any) any {
	return sky_dictToList(env)
}

func Compiler_Env_FromList(pairs any) any {
	return sky_dictFromList(pairs)
}

func Compiler_Env_Union(a any, b any) any {
	return sky_call(sky_dictUnion(a), b)
}

func Compiler_Env_FreeVarsInEnv(env any) any {
	return sky_call2(sky_dictFoldl(func(kk any) any { return func(scheme any) any { return func(acc any) any { return sky_call(sky_setUnion(freeVarsInScheme(scheme)), acc) } } }), sky_setEmpty(), env)
}

func Compiler_Env_GeneralizeInEnv(env any, t any) any {
	return func() any { typeVars := freeVars(t); _ = typeVars; envVars := Compiler_Env_FreeVarsInEnv(env); _ = envVars; quantified := sky_setToList(sky_call(sky_setDiff(typeVars), envVars)); _ = quantified; return map[string]any{"quantified": quantified, "type_": t} }()
}

func Compiler_Env_CreatePreludeEnv() any {
	return func() any { intT := TConst("Int"); _ = intT; floatT := TConst("Float"); _ = floatT; stringT := TConst("String"); _ = stringT; boolT := TConst("Bool"); _ = boolT; charT := TConst("Char"); _ = charT; unitT := TConst("Unit"); _ = unitT; identityScheme := map[string]any{"quantified": []any{0}, "type_": TFun(TVar(0, SkyJust("a")), TVar(0, SkyJust("a")))}; _ = identityScheme; notScheme := mono(TFun(boolT, boolT)); _ = notScheme; alwaysScheme := map[string]any{"quantified": []any{0, 1}, "type_": TFun(TVar(0, SkyJust("a")), TFun(TVar(1, SkyJust("b")), TVar(0, SkyJust("a"))))}; _ = alwaysScheme; fstScheme := map[string]any{"quantified": []any{0, 1}, "type_": TFun(TTuple([]any{TVar(0, SkyJust("a")), TVar(1, SkyJust("b"))}), TVar(0, SkyJust("a")))}; _ = fstScheme; sndScheme := map[string]any{"quantified": []any{0, 1}, "type_": TFun(TTuple([]any{TVar(0, SkyJust("a")), TVar(1, SkyJust("b"))}), TVar(1, SkyJust("b")))}; _ = sndScheme; clampScheme := map[string]any{"quantified": []any{0}, "type_": TFun(TVar(0, SkyJust("comparable")), TFun(TVar(0, SkyJust("comparable")), TFun(TVar(0, SkyJust("comparable")), TVar(0, SkyJust("comparable")))))}; _ = clampScheme; modByScheme := mono(TFun(intT, TFun(intT, intT))); _ = modByScheme; errorToStringScheme := mono(TFun(TConst("Error"), stringT)); _ = errorToStringScheme; okScheme := map[string]any{"quantified": []any{0, 1}, "type_": TFun(TVar(0, SkyJust("a")), TApp(TConst("Result"), []any{TVar(1, SkyJust("e")), TVar(0, SkyJust("a"))}))}; _ = okScheme; errScheme := map[string]any{"quantified": []any{0, 1}, "type_": TFun(TVar(0, SkyJust("e")), TApp(TConst("Result"), []any{TVar(0, SkyJust("e")), TVar(1, SkyJust("a"))}))}; _ = errScheme; justScheme := map[string]any{"quantified": []any{0}, "type_": TFun(TVar(0, SkyJust("a")), TApp(TConst("Maybe"), []any{TVar(0, SkyJust("a"))}))}; _ = justScheme; nothingScheme := map[string]any{"quantified": []any{0}, "type_": TApp(TConst("Maybe"), []any{TVar(0, SkyJust("a"))})}; _ = nothingScheme; trueScheme := mono(boolT); _ = trueScheme; falseScheme := mono(boolT); _ = falseScheme; return sky_dictFromList([]any{SkyTuple2{V0: "identity", V1: identityScheme}, SkyTuple2{V0: "not", V1: notScheme}, SkyTuple2{V0: "always", V1: alwaysScheme}, SkyTuple2{V0: "fst", V1: fstScheme}, SkyTuple2{V0: "snd", V1: sndScheme}, SkyTuple2{V0: "clamp", V1: clampScheme}, SkyTuple2{V0: "modBy", V1: modByScheme}, SkyTuple2{V0: "errorToString", V1: errorToStringScheme}, SkyTuple2{V0: "Ok", V1: okScheme}, SkyTuple2{V0: "Err", V1: errScheme}, SkyTuple2{V0: "Just", V1: justScheme}, SkyTuple2{V0: "Nothing", V1: nothingScheme}, SkyTuple2{V0: "True", V1: trueScheme}, SkyTuple2{V0: "False", V1: falseScheme}}) }()
}

var inferExpr = Compiler_Infer_InferExpr

var InferExpr = Compiler_Infer_InferExpr

var inferCall = Compiler_Infer_InferCall

var InferCall = Compiler_Infer_InferCall

var inferCallArgs = Compiler_Infer_InferCallArgs

var InferCallArgs = Compiler_Infer_InferCallArgs

var inferLambda = Compiler_Infer_InferLambda

var InferLambda = Compiler_Infer_InferLambda

var inferLambdaParams = Compiler_Infer_InferLambdaParams

var InferLambdaParams = Compiler_Infer_InferLambdaParams

var inferIf = Compiler_Infer_InferIf

var InferIf = Compiler_Infer_InferIf

var inferLet = Compiler_Infer_InferLet

var InferLet = Compiler_Infer_InferLet

var inferLetBindings = Compiler_Infer_InferLetBindings

var InferLetBindings = Compiler_Infer_InferLetBindings

var inferCase = Compiler_Infer_InferCase

var InferCase = Compiler_Infer_InferCase

var inferCaseBranches = Compiler_Infer_InferCaseBranches

var InferCaseBranches = Compiler_Infer_InferCaseBranches

var inferBinary = Compiler_Infer_InferBinary

var InferBinary = Compiler_Infer_InferBinary

var inferBinaryOp = Compiler_Infer_InferBinaryOp

var InferBinaryOp = Compiler_Infer_InferBinaryOp

var inferDeclaration = Compiler_Infer_InferDeclaration

var InferDeclaration = Compiler_Infer_InferDeclaration

var inferFunction = Compiler_Infer_InferFunction

var InferFunction = Compiler_Infer_InferFunction

var bindParams = Compiler_Infer_BindParams

var BindParams = Compiler_Infer_BindParams

var bindParamsLoop = Compiler_Infer_BindParamsLoop

var BindParamsLoop = Compiler_Infer_BindParamsLoop

var checkAnnotation = Compiler_Infer_CheckAnnotation

var CheckAnnotation = Compiler_Infer_CheckAnnotation

var applySubToEnv = Compiler_Infer_ApplySubToEnv

var ApplySubToEnv = Compiler_Infer_ApplySubToEnv

var inferTupleItems = Compiler_Infer_InferTupleItems

var InferTupleItems = Compiler_Infer_InferTupleItems

var inferListItems = Compiler_Infer_InferListItems

var InferListItems = Compiler_Infer_InferListItems

var inferListItemsLoop = Compiler_Infer_InferListItemsLoop

var InferListItemsLoop = Compiler_Infer_InferListItemsLoop

var inferRecordFields = Compiler_Infer_InferRecordFields

var InferRecordFields = Compiler_Infer_InferRecordFields

var inferRecordUpdateFields = Compiler_Infer_InferRecordUpdateFields

var InferRecordUpdateFields = Compiler_Infer_InferRecordUpdateFields

var inferRecordUpdateFieldsLoop = Compiler_Infer_InferRecordUpdateFieldsLoop

var InferRecordUpdateFieldsLoop = Compiler_Infer_InferRecordUpdateFieldsLoop

var checkExhaustiveness = Compiler_Exhaustive_CheckExhaustiveness

var hasCatchAll = Compiler_Exhaustive_HasCatchAll

var isCatchAll = Compiler_Exhaustive_IsCatchAll

var checkTypeExhaustiveness = Compiler_Exhaustive_CheckTypeExhaustiveness

var checkBoolExhaustiveness = Compiler_Exhaustive_CheckBoolExhaustiveness

var collectBoolPatterns = Compiler_Exhaustive_CollectBoolPatterns

var checkAdtExhaustiveness = Compiler_Exhaustive_CheckAdtExhaustiveness

var collectConstructorPatterns = Compiler_Exhaustive_CollectConstructorPatterns

var lastPart = Compiler_Exhaustive_LastPart

func Compiler_Checker_MakeTypedDecl(n any, s any, t any) any {
	return map[string]any{"name": n, "scheme": s, "prettyType": formatType(t)}
}

func Compiler_Checker_CheckModule(mod any, imports any) any {
	return func() any { counter := sky_refNew(100); _ = counter; baseEnv := func() any { return func() any { __subject := imports; if sky_asSkyMaybe(__subject).SkyName == "Just" { importedEnv := sky_asSkyMaybe(__subject).JustValue; _ = importedEnv; return Compiler_Env_Union(importedEnv, Compiler_Env_CreatePreludeEnv()) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return Compiler_Env_CreatePreludeEnv() };  return nil }() }(); _ = baseEnv; aliasEnv := Compiler_Checker_RegisterTypeAliases(sky_asMap(mod)["declarations"], baseEnv); _ = aliasEnv; __tup_registry_adtEnv_adtDiags := Compiler_Adt_RegisterAdts(counter, sky_asMap(mod)["declarations"]); registry := sky_asTuple3(__tup_registry_adtEnv_adtDiags).V0; _ = registry; adtEnv := sky_asTuple3(__tup_registry_adtEnv_adtDiags).V1; _ = adtEnv; adtDiags := sky_asTuple3(__tup_registry_adtEnv_adtDiags).V2; _ = adtDiags; env0 := Compiler_Env_Union(adtEnv, aliasEnv); _ = env0; annotations := Compiler_Checker_CollectAnnotations(sky_asMap(mod)["declarations"]); _ = annotations; env1 := Compiler_Checker_PreRegisterFunctions(counter, sky_asMap(mod)["declarations"], env0); _ = env1; __tup_typedDecls_finalEnv_inferDiags := Compiler_Checker_InferAllDeclarations(counter, registry, env1, sky_asMap(mod)["declarations"], annotations); typedDecls := sky_asTuple3(__tup_typedDecls_finalEnv_inferDiags).V0; _ = typedDecls; finalEnv := sky_asTuple3(__tup_typedDecls_finalEnv_inferDiags).V1; _ = finalEnv; inferDiags := sky_asTuple3(__tup_typedDecls_finalEnv_inferDiags).V2; _ = inferDiags; exhaustDiags := Compiler_Checker_CheckAllExhaustiveness(registry, sky_asMap(mod)["declarations"]); _ = exhaustDiags; allDiags := sky_listConcat([]any{adtDiags, inferDiags, exhaustDiags}); _ = allDiags; return SkyOk(map[string]any{"env": finalEnv, "registry": registry, "declarations": typedDecls, "diagnostics": allDiags}) }()
}

func Compiler_Checker_RegisterTypeAliases(decls any, env any) any {
	return func() any { return func() any { __subject := decls; if len(sky_asList(__subject)) == 0 { return env };  if len(sky_asList(__subject)) > 0 { decl := sky_asList(__subject)[0]; _ = decl; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "TypeAliasDecl" { aliasName := sky_asMap(__subject)["V0"]; _ = aliasName; aliasParams := sky_asMap(__subject)["V1"]; _ = aliasParams; aliasType := sky_asMap(__subject)["V2"]; _ = aliasType; return Compiler_Checker_RegisterTypeAliases(rest, env) };  if true { return Compiler_Checker_RegisterTypeAliases(rest, env) };  return nil }() }() };  return nil }() }()
}

func Compiler_Checker_CollectAnnotations(decls any) any {
	return Compiler_Checker_CollectAnnotationsLoop(decls, sky_dictEmpty())
}

func Compiler_Checker_CollectAnnotationsLoop(decls any, acc any) any {
	return func() any { return func() any { __subject := decls; if len(sky_asList(__subject)) == 0 { return acc };  if len(sky_asList(__subject)) > 0 { decl := sky_asList(__subject)[0]; _ = decl; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "TypeAnnotDecl" { name := sky_asMap(__subject)["V0"]; _ = name; typeExpr := sky_asMap(__subject)["V1"]; _ = typeExpr; return Compiler_Checker_CollectAnnotationsLoop(rest, sky_call2(sky_dictInsert(name), typeExpr, acc)) };  if true { return Compiler_Checker_CollectAnnotationsLoop(rest, acc) };  return nil }() }() };  return nil }() }()
}

func Compiler_Checker_PreRegisterFunctions(counter any, decls any, env any) any {
	return func() any { return func() any { __subject := decls; if len(sky_asList(__subject)) == 0 { return env };  if len(sky_asList(__subject)) > 0 { decl := sky_asList(__subject)[0]; _ = decl; rest := sky_asList(__subject)[1:]; _ = rest; return Compiler_Checker_PreRegisterOneFunction(counter, decl, rest, env) };  return nil }() }()
}

func Compiler_Checker_PreRegisterOneFunction(counter any, decl any, rest any, env any) any {
	return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "FunDecl" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { tv := freshVar(counter, SkyNothing()); _ = tv; newEnv := Compiler_Env_Extend(name, mono(tv), env); _ = newEnv; return Compiler_Checker_PreRegisterFunctions(counter, rest, newEnv) }() };  if true { return Compiler_Checker_PreRegisterFunctions(counter, rest, env) };  return nil }() }()
}

func Compiler_Checker_InferAllDeclarations(counter any, registry any, env any, decls any, annotations any) any {
	return Compiler_Checker_InferDeclsLoop(counter, registry, env, decls, annotations, []any{}, []any{})
}

func Compiler_Checker_InferDeclsLoop(counter any, registry any, env any, decls any, annotations any, typedDecls any, diagnostics any) any {
	return func() any { return func() any { __subject := decls; if len(sky_asList(__subject)) == 0 { return SkyTuple3{V0: sky_listReverse(typedDecls), V1: env, V2: diagnostics} };  if len(sky_asList(__subject)) > 0 { decl := sky_asList(__subject)[0]; _ = decl; rest := sky_asList(__subject)[1:]; _ = rest; return Compiler_Checker_InferOneDecl(counter, registry, env, decl, rest, annotations, typedDecls, diagnostics) };  return nil }() }()
}

func Compiler_Checker_InferOneDecl(counter any, registry any, env any, decl any, rest any, annotations any, typedDecls any, diagnostics any) any {
	return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "FunDecl" { name := sky_asMap(__subject)["V0"]; _ = name; return Compiler_Checker_InferOneFunDecl(counter, registry, env, name, decl, rest, annotations, typedDecls, diagnostics) };  if true { return Compiler_Checker_InferDeclsLoop(counter, registry, env, rest, annotations, typedDecls, diagnostics) };  return nil }() }()
}

func Compiler_Checker_InferOneFunDecl(counter any, registry any, env any, fnName any, decl any, rest any, annotations any, typedDecls any, diagnostics any) any {
	return func() any { annotation := sky_call(sky_dictGet(fnName), annotations); _ = annotation; return func() any { return func() any { __subject := Compiler_Infer_InferDeclaration(counter, registry, env, decl, annotation); if sky_asSkyResult(__subject).SkyName == "Ok" { inferResult := sky_asSkyResult(__subject).OkValue; _ = inferResult; return func(__pa0 any) any { return func(__pa1 any) any { return Compiler_Checker_InferDeclsLoop(counter, registry, Compiler_Env_Extend(sky_asMap(inferResult)["name"], sky_asMap(inferResult)["scheme"], env), rest, annotations, __pa0, __pa1) } } };  return nil }() }() }()
}

func Compiler_Checker_CheckAllExhaustiveness(registry any, decls any) any {
	return sky_call(sky_listConcatMap(func(__pa0 any) any { return Compiler_Checker_CheckDeclExhaustiveness(registry, __pa0) }), decls)
}

func Compiler_Checker_CheckDeclExhaustiveness(registry any, decl any) any {
	return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "FunDecl" { body := sky_asMap(__subject)["V2"]; _ = body; return Compiler_Checker_CheckExprExhaustiveness(registry, body) };  if true { return []any{} };  return nil }() }()
}

func Compiler_Checker_CheckExprExhaustiveness(registry any, expr any) any {
	return func() any { return func() any { __subject := expr; if sky_asMap(__subject)["SkyName"] == "CaseExpr" { branches := sky_asMap(__subject)["V1"]; _ = branches; return sky_call(sky_listConcatMap(func(b any) any { return Compiler_Checker_CheckExprExhaustiveness(registry, sky_asMap(b)["body"]) }), branches) };  if sky_asMap(__subject)["SkyName"] == "IfExpr" { cond := sky_asMap(__subject)["V0"]; _ = cond; thenB := sky_asMap(__subject)["V1"]; _ = thenB; elseB := sky_asMap(__subject)["V2"]; _ = elseB; return sky_listConcat([]any{Compiler_Checker_CheckExprExhaustiveness(registry, cond), Compiler_Checker_CheckExprExhaustiveness(registry, thenB), Compiler_Checker_CheckExprExhaustiveness(registry, elseB)}) };  if sky_asMap(__subject)["SkyName"] == "LetExpr" { bindings := sky_asMap(__subject)["V0"]; _ = bindings; body := sky_asMap(__subject)["V1"]; _ = body; return sky_call(sky_listAppend(sky_call(sky_listConcatMap(func(b any) any { return Compiler_Checker_CheckExprExhaustiveness(registry, sky_asMap(b)["value"]) }), bindings)), Compiler_Checker_CheckExprExhaustiveness(registry, body)) };  if sky_asMap(__subject)["SkyName"] == "LambdaExpr" { body := sky_asMap(__subject)["V1"]; _ = body; return Compiler_Checker_CheckExprExhaustiveness(registry, body) };  if sky_asMap(__subject)["SkyName"] == "CallExpr" { callee := sky_asMap(__subject)["V0"]; _ = callee; args := sky_asMap(__subject)["V1"]; _ = args; return sky_call(sky_listAppend(Compiler_Checker_CheckExprExhaustiveness(registry, callee)), sky_call(sky_listConcatMap(func(__pa0 any) any { return Compiler_Checker_CheckExprExhaustiveness(registry, __pa0) }), args)) };  if sky_asMap(__subject)["SkyName"] == "BinaryExpr" { left := sky_asMap(__subject)["V1"]; _ = left; right := sky_asMap(__subject)["V2"]; _ = right; return sky_call(sky_listAppend(Compiler_Checker_CheckExprExhaustiveness(registry, left)), Compiler_Checker_CheckExprExhaustiveness(registry, right)) };  if sky_asMap(__subject)["SkyName"] == "TupleExpr" { items := sky_asMap(__subject)["V0"]; _ = items; return sky_call(sky_listConcatMap(func(__pa0 any) any { return Compiler_Checker_CheckExprExhaustiveness(registry, __pa0) }), items) };  if sky_asMap(__subject)["SkyName"] == "ListExpr" { items := sky_asMap(__subject)["V0"]; _ = items; return sky_call(sky_listConcatMap(func(__pa0 any) any { return Compiler_Checker_CheckExprExhaustiveness(registry, __pa0) }), items) };  if sky_asMap(__subject)["SkyName"] == "ParenExpr" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Compiler_Checker_CheckExprExhaustiveness(registry, inner) };  if true { return []any{} };  return nil }() }()
}

var EmptyResult = Compiler_PatternCheck_EmptyResult()

var checkPattern = Compiler_PatternCheck_CheckPattern

var checkConstructorPattern = Compiler_PatternCheck_CheckConstructorPattern

var splitFunType = Compiler_PatternCheck_SplitFunType

var checkPatternList = Compiler_PatternCheck_CheckPatternList

var checkPatternListSame = Compiler_PatternCheck_CheckPatternListSame

var literalType = Compiler_PatternCheck_LiteralType

func Compiler_Infer_InferExpr(counter any, registry any, env any, expr any) any {
	return func() any { return func() any { __subject := expr; if sky_asMap(__subject)["SkyName"] == "IntLitExpr" { return SkyOk(map[string]any{"substitution": emptySub, "type_": TConst("Int")}) };  if sky_asMap(__subject)["SkyName"] == "FloatLitExpr" { return SkyOk(map[string]any{"substitution": emptySub, "type_": TConst("Float")}) };  if sky_asMap(__subject)["SkyName"] == "StringLitExpr" { return SkyOk(map[string]any{"substitution": emptySub, "type_": TConst("String")}) };  if sky_asMap(__subject)["SkyName"] == "CharLitExpr" { return SkyOk(map[string]any{"substitution": emptySub, "type_": TConst("Char")}) };  if sky_asMap(__subject)["SkyName"] == "BoolLitExpr" { return SkyOk(map[string]any{"substitution": emptySub, "type_": TConst("Bool")}) };  if sky_asMap(__subject)["SkyName"] == "UnitExpr" { return SkyOk(map[string]any{"substitution": emptySub, "type_": TConst("Unit")}) };  if sky_asMap(__subject)["SkyName"] == "IdentifierExpr" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { return func() any { __subject := Compiler_Env_Lookup(name, env); if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyErr(sky_concat("Unbound variable: ", name)) };  if sky_asSkyMaybe(__subject).SkyName == "Just" { scheme := sky_asSkyMaybe(__subject).JustValue; _ = scheme; return func() any { t := instantiate(counter, scheme); _ = t; return SkyOk(map[string]any{"substitution": emptySub, "type_": t}) }() };  if sky_asMap(__subject)["SkyName"] == "QualifiedExpr" { parts := sky_asMap(__subject)["V0"]; _ = parts; return func() any { qualName := sky_call(sky_stringJoin("."), parts); _ = qualName; return func() any { return func() any { __subject := Compiler_Env_Lookup(qualName, env); if sky_asSkyMaybe(__subject).SkyName == "Just" { scheme := sky_asSkyMaybe(__subject).JustValue; _ = scheme; return SkyOk(map[string]any{"substitution": emptySub, "type_": instantiate(counter, scheme)}) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return func() any { return func() any { __subject := sky_listReverse(parts); if len(sky_asList(__subject)) > 0 { last := sky_asList(__subject)[0]; _ = last; return func() any { return func() any { __subject := Compiler_Env_Lookup(last, env); if sky_asSkyMaybe(__subject).SkyName == "Just" { scheme := sky_asSkyMaybe(__subject).JustValue; _ = scheme; return SkyOk(map[string]any{"substitution": emptySub, "type_": instantiate(counter, scheme)}) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyErr(sky_concat("Unbound qualified name: ", qualName)) };  if len(sky_asList(__subject)) == 0 { return SkyErr("Empty qualified name") };  if sky_asMap(__subject)["SkyName"] == "TupleExpr" { items := sky_asMap(__subject)["V0"]; _ = items; return Compiler_Infer_InferTupleItems(counter, registry, env, items, emptySub, []any{}) };  if sky_asMap(__subject)["SkyName"] == "ListExpr" { items := sky_asMap(__subject)["V0"]; _ = items; return Compiler_Infer_InferListItems(counter, registry, env, items) };  if sky_asMap(__subject)["SkyName"] == "RecordExpr" { fields := sky_asMap(__subject)["V0"]; _ = fields; return Compiler_Infer_InferRecordFields(counter, registry, env, fields, emptySub, sky_dictEmpty()) };  if sky_asMap(__subject)["SkyName"] == "RecordUpdateExpr" { base := sky_asMap(__subject)["V0"]; _ = base; fields := sky_asMap(__subject)["V1"]; _ = fields; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, base); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { baseResult := sky_asSkyResult(__subject).OkValue; _ = baseResult; return func() any { return func() any { __subject := Compiler_Infer_InferRecordUpdateFields(counter, registry, env, fields, sky_asMap(baseResult)["substitution"]); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { fieldSub := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = fieldSub; fieldTypes := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = fieldTypes; return func() any { combinedSub := composeSubs(fieldSub, sky_asMap(baseResult)["substitution"]); _ = combinedSub; baseType := applySub(combinedSub, sky_asMap(baseResult)["type_"]); _ = baseType; return func() any { return func() any { __subject := baseType; if sky_asMap(__subject)["SkyName"] == "TRecord" { existingFields := sky_asMap(__subject)["V0"]; _ = existingFields; return func() any { updatedFields := sky_call(sky_dictUnion(fieldTypes), existingFields); _ = updatedFields; return SkyOk(map[string]any{"substitution": combinedSub, "type_": TRecord(updatedFields)}) }() };  if true { return SkyOk(map[string]any{"substitution": combinedSub, "type_": TRecord(fieldTypes)}) };  if sky_asMap(__subject)["SkyName"] == "FieldAccessExpr" { target := sky_asMap(__subject)["V0"]; _ = target; fieldName := sky_asMap(__subject)["V1"]; _ = fieldName; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, target); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { targetResult := sky_asSkyResult(__subject).OkValue; _ = targetResult; return func() any { resultVar := freshVar(counter, SkyNothing()); _ = resultVar; targetType := applySub(sky_asMap(targetResult)["substitution"], sky_asMap(targetResult)["type_"]); _ = targetType; return func() any { return func() any { __subject := targetType; if sky_asMap(__subject)["SkyName"] == "TRecord" { fields := sky_asMap(__subject)["V0"]; _ = fields; return func() any { return func() any { __subject := sky_call(sky_dictGet(fieldName), fields); if sky_asSkyMaybe(__subject).SkyName == "Just" { fieldType := sky_asSkyMaybe(__subject).JustValue; _ = fieldType; return SkyOk(map[string]any{"substitution": sky_asMap(targetResult)["substitution"], "type_": fieldType}) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyErr(sky_concat("Record has no field '", sky_concat(fieldName, "'"))) };  if true { return SkyOk(map[string]any{"substitution": sky_asMap(targetResult)["substitution"], "type_": resultVar}) };  if sky_asMap(__subject)["SkyName"] == "CallExpr" { callee := sky_asMap(__subject)["V0"]; _ = callee; args := sky_asMap(__subject)["V1"]; _ = args; return Compiler_Infer_InferCall(counter, registry, env, callee, args) };  if sky_asMap(__subject)["SkyName"] == "LambdaExpr" { params := sky_asMap(__subject)["V0"]; _ = params; body := sky_asMap(__subject)["V1"]; _ = body; return Compiler_Infer_InferLambda(counter, registry, env, params, body) };  if sky_asMap(__subject)["SkyName"] == "IfExpr" { condition := sky_asMap(__subject)["V0"]; _ = condition; thenBranch := sky_asMap(__subject)["V1"]; _ = thenBranch; elseBranch := sky_asMap(__subject)["V2"]; _ = elseBranch; return Compiler_Infer_InferIf(counter, registry, env, condition, thenBranch, elseBranch) };  if sky_asMap(__subject)["SkyName"] == "LetExpr" { bindings := sky_asMap(__subject)["V0"]; _ = bindings; body := sky_asMap(__subject)["V1"]; _ = body; return Compiler_Infer_InferLet(counter, registry, env, bindings, body) };  if sky_asMap(__subject)["SkyName"] == "CaseExpr" { subject := sky_asMap(__subject)["V0"]; _ = subject; branches := sky_asMap(__subject)["V1"]; _ = branches; return Compiler_Infer_InferCase(counter, registry, env, subject, branches) };  if sky_asMap(__subject)["SkyName"] == "BinaryExpr" { op := sky_asMap(__subject)["V0"]; _ = op; left := sky_asMap(__subject)["V1"]; _ = left; right := sky_asMap(__subject)["V2"]; _ = right; return Compiler_Infer_InferBinary(counter, registry, env, op, left, right) };  if sky_asMap(__subject)["SkyName"] == "NegateExpr" { inner := sky_asMap(__subject)["V0"]; _ = inner; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, inner); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return func() any { return func() any { __subject := unify(sky_asMap(result)["type_"], TConst("Int")); if sky_asSkyResult(__subject).SkyName == "Ok" { sub := sky_asSkyResult(__subject).OkValue; _ = sub; return SkyOk(map[string]any{"substitution": composeSubs(sub, sky_asMap(result)["substitution"]), "type_": applySub(sub, sky_asMap(result)["type_"])}) };  if sky_asSkyResult(__subject).SkyName == "Err" { return func() any { return func() any { __subject := unify(sky_asMap(result)["type_"], TConst("Float")); if sky_asSkyResult(__subject).SkyName == "Ok" { sub := sky_asSkyResult(__subject).OkValue; _ = sub; return SkyOk(map[string]any{"substitution": composeSubs(sub, sky_asMap(result)["substitution"]), "type_": applySub(sub, sky_asMap(result)["type_"])}) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Negation requires a number type: ", e)) };  if sky_asMap(__subject)["SkyName"] == "ParenExpr" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Compiler_Infer_InferExpr(counter, registry, env, inner) };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_InferCall(counter any, registry any, env any, callee any, args any) any {
	return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, callee); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { calleeResult := sky_asSkyResult(__subject).OkValue; _ = calleeResult; return Compiler_Infer_InferCallArgs(counter, registry, env, sky_asMap(calleeResult)["type_"], sky_asMap(calleeResult)["substitution"], args) };  return nil }() }()
}

func Compiler_Infer_InferCallArgs(counter any, registry any, env any, fnType any, sub any, args any) any {
	return func() any { return func() any { __subject := args; if len(sky_asList(__subject)) == 0 { return SkyOk(map[string]any{"substitution": sub, "type_": fnType}) };  if len(sky_asList(__subject)) > 0 { arg := sky_asList(__subject)[0]; _ = arg; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { resultVar := freshVar(counter, SkyNothing()); _ = resultVar; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, Compiler_Infer_ApplySubToEnv(sub, env), arg); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { argResult := sky_asSkyResult(__subject).OkValue; _ = argResult; return func() any { combinedSub := composeSubs(sky_asMap(argResult)["substitution"], sub); _ = combinedSub; expectedFnType := TFun(sky_asMap(argResult)["type_"], resultVar); _ = expectedFnType; actualFnType := applySub(combinedSub, fnType); _ = actualFnType; return func() any { return func() any { __subject := unify(actualFnType, expectedFnType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Type error in function call: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return func() any { finalSub := composeSubs(unifySub, combinedSub); _ = finalSub; resultType := applySub(finalSub, resultVar); _ = resultType; return Compiler_Infer_InferCallArgs(counter, registry, env, resultType, finalSub, rest) }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }()
}

func Compiler_Infer_InferLambda(counter any, registry any, env any, params any, body any) any {
	return Compiler_Infer_InferLambdaParams(counter, registry, env, params, body, []any{})
}

func Compiler_Infer_InferLambdaParams(counter any, registry any, env any, params any, body any, paramTypes any) any {
	return func() any { return func() any { __subject := params; if len(sky_asList(__subject)) == 0 { return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, body); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { bodyResult := sky_asSkyResult(__subject).OkValue; _ = bodyResult; return func() any { resultType := sky_call2(sky_listFoldr(func(pt any) any { return func(acc any) any { return TFun(pt, acc) } }), sky_asMap(bodyResult)["type_"], paramTypes); _ = resultType; return SkyOk(map[string]any{"substitution": sky_asMap(bodyResult)["substitution"], "type_": applySub(sky_asMap(bodyResult)["substitution"], resultType)}) }() };  if len(sky_asList(__subject)) > 0 { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { paramVar := freshVar(counter, SkyNothing()); _ = paramVar; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, pat, paramVar); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { patResult := sky_asSkyResult(__subject).OkValue; _ = patResult; return func() any { newEnv := sky_call2(sky_listFoldl(func(pair any) any { return func(acc any) any { return Compiler_Env_Extend(sky_fst(pair), mono(sky_snd(pair)), acc) } }), env, sky_asMap(patResult)["bindings"]); _ = newEnv; boundParamType := applySub(sky_asMap(patResult)["substitution"], paramVar); _ = boundParamType; return Compiler_Infer_InferLambdaParams(counter, registry, newEnv, rest, body, sky_call(sky_listAppend(paramTypes), []any{boundParamType})) }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_InferIf(counter any, registry any, env any, condition any, thenBranch any, elseBranch any) any {
	return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, condition); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { condResult := sky_asSkyResult(__subject).OkValue; _ = condResult; return func() any { return func() any { __subject := unify(sky_asMap(condResult)["type_"], TConst("Bool")); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Condition must be Bool: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { condSub := sky_asSkyResult(__subject).OkValue; _ = condSub; return func() any { sub1 := composeSubs(condSub, sky_asMap(condResult)["substitution"]); _ = sub1; env1 := Compiler_Infer_ApplySubToEnv(sub1, env); _ = env1; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env1, thenBranch); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { thenResult := sky_asSkyResult(__subject).OkValue; _ = thenResult; return func() any { sub2 := composeSubs(sky_asMap(thenResult)["substitution"], sub1); _ = sub2; env2 := Compiler_Infer_ApplySubToEnv(sub2, env); _ = env2; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env2, elseBranch); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { elseResult := sky_asSkyResult(__subject).OkValue; _ = elseResult; return func() any { sub3 := composeSubs(sky_asMap(elseResult)["substitution"], sub2); _ = sub3; return func() any { return func() any { __subject := unify(applySub(sub3, sky_asMap(thenResult)["type_"]), applySub(sub3, sky_asMap(elseResult)["type_"])); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("If branches have different types: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { branchSub := sky_asSkyResult(__subject).OkValue; _ = branchSub; return func() any { finalSub := composeSubs(branchSub, sub3); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "type_": applySub(finalSub, sky_asMap(thenResult)["type_"])}) }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_InferLet(counter any, registry any, env any, bindings any, body any) any {
	return Compiler_Infer_InferLetBindings(counter, registry, env, bindings, body)
}

func Compiler_Infer_InferLetBindings(counter any, registry any, env any, bindings any, body any) any {
	return func() any { return func() any { __subject := bindings; if len(sky_asList(__subject)) == 0 { return Compiler_Infer_InferExpr(counter, registry, env, body) };  if len(sky_asList(__subject)) > 0 { binding := sky_asList(__subject)[0]; _ = binding; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, sky_asMap(binding)["value"]); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { valueResult := sky_asSkyResult(__subject).OkValue; _ = valueResult; return func() any { sub := sky_asMap(valueResult)["substitution"]; _ = sub; envWithSub := Compiler_Infer_ApplySubToEnv(sub, env); _ = envWithSub; generalizedScheme := Compiler_Env_GeneralizeInEnv(envWithSub, applySub(sub, sky_asMap(valueResult)["type_"])); _ = generalizedScheme; return func() any { return func() any { __subject := sky_asMap(binding)["pattern"]; if sky_asMap(__subject)["SkyName"] == "PVariable" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { newEnv := Compiler_Env_Extend(name, generalizedScheme, envWithSub); _ = newEnv; return func() any { return func() any { __subject := Compiler_Infer_InferLetBindings(counter, registry, newEnv, rest, body); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { bodyResult := sky_asSkyResult(__subject).OkValue; _ = bodyResult; return SkyOk(map[string]any{"substitution": composeSubs(sky_asMap(bodyResult)["substitution"], sub), "type_": sky_asMap(bodyResult)["type_"]}) };  if true { return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, sky_asMap(binding)["pattern"], applySub(sub, sky_asMap(valueResult)["type_"])); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { patResult := sky_asSkyResult(__subject).OkValue; _ = patResult; return func() any { combinedSub := composeSubs(sky_asMap(patResult)["substitution"], sub); _ = combinedSub; newEnv := sky_call2(sky_listFoldl(func(pair any) any { return func(acc any) any { return Compiler_Env_Extend(sky_fst(pair), mono(sky_snd(pair)), acc) } }), Compiler_Infer_ApplySubToEnv(combinedSub, env), sky_asMap(patResult)["bindings"]); _ = newEnv; return func() any { return func() any { __subject := Compiler_Infer_InferLetBindings(counter, registry, newEnv, rest, body); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { bodyResult := sky_asSkyResult(__subject).OkValue; _ = bodyResult; return SkyOk(map[string]any{"substitution": composeSubs(sky_asMap(bodyResult)["substitution"], combinedSub), "type_": sky_asMap(bodyResult)["type_"]}) };  return nil }() }() }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_InferCase(counter any, registry any, env any, subject any, branches any) any {
	return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, subject); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { subjectResult := sky_asSkyResult(__subject).OkValue; _ = subjectResult; return func() any { resultVar := freshVar(counter, SkyNothing()); _ = resultVar; return Compiler_Infer_InferCaseBranches(counter, registry, env, sky_asMap(subjectResult)["type_"], sky_asMap(subjectResult)["substitution"], branches, resultVar) }() };  return nil }() }()
}

func Compiler_Infer_InferCaseBranches(counter any, registry any, env any, subjectType any, sub any, branches any, resultType any) any {
	return func() any { return func() any { __subject := branches; if len(sky_asList(__subject)) == 0 { return SkyOk(map[string]any{"substitution": sub, "type_": applySub(sub, resultType)}) };  if len(sky_asList(__subject)) > 0 { branch := sky_asList(__subject)[0]; _ = branch; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { currentSubjectType := applySub(sub, subjectType); _ = currentSubjectType; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, sky_asMap(branch)["pattern"], currentSubjectType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { patResult := sky_asSkyResult(__subject).OkValue; _ = patResult; return func() any { patSub := composeSubs(sky_asMap(patResult)["substitution"], sub); _ = patSub; branchEnv := sky_call2(sky_listFoldl(func(pair any) any { return func(acc any) any { return Compiler_Env_Extend(sky_fst(pair), mono(applySub(patSub, sky_snd(pair))), acc) } }), Compiler_Infer_ApplySubToEnv(patSub, env), sky_asMap(patResult)["bindings"]); _ = branchEnv; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, branchEnv, sky_asMap(branch)["body"]); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { bodyResult := sky_asSkyResult(__subject).OkValue; _ = bodyResult; return func() any { bodySub := composeSubs(sky_asMap(bodyResult)["substitution"], patSub); _ = bodySub; return func() any { return func() any { __subject := unify(applySub(bodySub, resultType), applySub(bodySub, sky_asMap(bodyResult)["type_"])); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Case branch type mismatch: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return func() any { finalSub := composeSubs(unifySub, bodySub); _ = finalSub; return Compiler_Infer_InferCaseBranches(counter, registry, env, subjectType, finalSub, rest, resultType) }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }()
}

func Compiler_Infer_InferBinary(counter any, registry any, env any, op any, left any, right any) any {
	return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, left); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { leftResult := sky_asSkyResult(__subject).OkValue; _ = leftResult; return func() any { sub1 := sky_asMap(leftResult)["substitution"]; _ = sub1; env1 := Compiler_Infer_ApplySubToEnv(sub1, env); _ = env1; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env1, right); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { rightResult := sky_asSkyResult(__subject).OkValue; _ = rightResult; return func() any { sub2 := composeSubs(sky_asMap(rightResult)["substitution"], sub1); _ = sub2; lt := applySub(sub2, sky_asMap(leftResult)["type_"]); _ = lt; rt := applySub(sub2, sky_asMap(rightResult)["type_"]); _ = rt; return Compiler_Infer_InferBinaryOp(counter, op, lt, rt, sub2) }() };  return nil }() }() }() };  return nil }() }()
}

func Compiler_Infer_InferBinaryOp(counter any, op any, lt any, rt any, sub any) any {
	return func() any { if sky_asBool(sky_asBool(sky_equal(op, "+")) || sky_asBool(sky_asBool(sky_equal(op, "-")) || sky_asBool(sky_asBool(sky_equal(op, "*")) || sky_asBool(sky_equal(op, "%"))))) { return func() any { return func() any { __subject := unify(lt, rt); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Arithmetic operator '", sky_concat(op, sky_concat("': ", e)))) };  if sky_asSkyResult(__subject).SkyName == "Ok" { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return func() any { finalSub := composeSubs(unifySub, sub); _ = finalSub; resultType := applySub(finalSub, lt); _ = resultType; return SkyOk(map[string]any{"substitution": finalSub, "type_": resultType}) }() };  return nil }() }() }; return func() any { if sky_asBool(sky_equal(op, "/")) { return func() any { return func() any { __subject := unify(lt, TConst("Float")); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Division requires Float: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asSkyResult(__subject).OkValue; _ = s1; return func() any { return func() any { __subject := unify(rt, TConst("Float")); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Division requires Float: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asSkyResult(__subject).OkValue; _ = s2; return func() any { finalSub := composeSubs(s2, composeSubs(s1, sub)); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "type_": TConst("Float")}) }() };  return nil }() }() };  return nil }() }() }; return func() any { if sky_asBool(sky_equal(op, "//")) { return func() any { return func() any { __subject := unify(lt, TConst("Int")); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Integer division requires Int: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asSkyResult(__subject).OkValue; _ = s1; return func() any { return func() any { __subject := unify(rt, TConst("Int")); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Integer division requires Int: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asSkyResult(__subject).OkValue; _ = s2; return SkyOk(map[string]any{"substitution": composeSubs(s2, composeSubs(s1, sub)), "type_": TConst("Int")}) };  return nil }() }() };  return nil }() }() }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "==")) || sky_asBool(sky_asBool(sky_equal(op, "!=")) || sky_asBool(sky_asBool(sky_equal(op, "/=")) || sky_asBool(sky_asBool(sky_equal(op, "<")) || sky_asBool(sky_asBool(sky_equal(op, "<=")) || sky_asBool(sky_asBool(sky_equal(op, ">")) || sky_asBool(sky_equal(op, ">=")))))))) { return func() any { return func() any { __subject := unify(lt, rt); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Comparison operator '", sky_concat(op, sky_concat("': ", e)))) };  if sky_asSkyResult(__subject).SkyName == "Ok" { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return SkyOk(map[string]any{"substitution": composeSubs(unifySub, sub), "type_": TConst("Bool")}) };  return nil }() }() }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "&&")) || sky_asBool(sky_equal(op, "||"))) { return func() any { return func() any { __subject := unify(lt, TConst("Bool")); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Logical operator requires Bool: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asSkyResult(__subject).OkValue; _ = s1; return func() any { return func() any { __subject := unify(applySub(s1, rt), TConst("Bool")); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Logical operator requires Bool: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asSkyResult(__subject).OkValue; _ = s2; return SkyOk(map[string]any{"substitution": composeSubs(s2, composeSubs(s1, sub)), "type_": TConst("Bool")}) };  return nil }() }() };  return nil }() }() }; return func() any { if sky_asBool(sky_equal(op, "++")) { return func() any { return func() any { __subject := unify(lt, rt); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Append operator: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return SkyOk(map[string]any{"substitution": composeSubs(unifySub, sub), "type_": applySub(composeSubs(unifySub, sub), lt)}) };  return nil }() }() }; return func() any { if sky_asBool(sky_equal(op, "::")) { return func() any { listType := TApp(TConst("List"), []any{lt}); _ = listType; return func() any { return func() any { __subject := unify(rt, listType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Cons operator: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return SkyOk(map[string]any{"substitution": composeSubs(unifySub, sub), "type_": applySub(composeSubs(unifySub, sub), rt)}) };  return nil }() }() }() }; return func() any { if sky_asBool(sky_equal(op, "|>")) { return func() any { resultVar := freshVar(counter, SkyNothing()); _ = resultVar; expectedFnType := TFun(lt, resultVar); _ = expectedFnType; return func() any { return func() any { __subject := unify(rt, expectedFnType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Pipeline operator: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return func() any { finalSub := composeSubs(unifySub, sub); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "type_": applySub(finalSub, resultVar)}) }() };  return nil }() }() }() }; return func() any { if sky_asBool(sky_equal(op, "<|")) { return func() any { resultVar := freshVar(counter, SkyNothing()); _ = resultVar; expectedFnType := TFun(rt, resultVar); _ = expectedFnType; return func() any { return func() any { __subject := unify(lt, expectedFnType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Reverse pipeline operator: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return func() any { finalSub := composeSubs(unifySub, sub); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "type_": applySub(finalSub, resultVar)}) }() };  return nil }() }() }() }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, ">>")) || sky_asBool(sky_equal(op, "<<"))) { return func() any { aVar := freshVar(counter, SkyJust("a")); _ = aVar; bVar := freshVar(counter, SkyJust("b")); _ = bVar; cVar := freshVar(counter, SkyJust("c")); _ = cVar; return func() any { if sky_asBool(sky_equal(op, ">>")) { return func() any { return func() any { __subject := unify(lt, TFun(aVar, bVar)); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Composition: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asSkyResult(__subject).OkValue; _ = s1; return func() any { return func() any { __subject := unify(applySub(s1, rt), TFun(applySub(s1, bVar), cVar)); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Composition: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asSkyResult(__subject).OkValue; _ = s2; return func() any { finalSub := composeSubs(s2, composeSubs(s1, sub)); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "type_": TFun(applySub(finalSub, aVar), applySub(finalSub, cVar))}) }() };  return nil }() }() };  return nil }() }() }; return func() any { return func() any { __subject := unify(rt, TFun(aVar, bVar)); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Composition: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asSkyResult(__subject).OkValue; _ = s1; return func() any { return func() any { __subject := unify(applySub(s1, lt), TFun(applySub(s1, bVar), cVar)); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Composition: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asSkyResult(__subject).OkValue; _ = s2; return func() any { finalSub := composeSubs(s2, composeSubs(s1, sub)); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "type_": TFun(applySub(finalSub, aVar), applySub(finalSub, cVar))}) }() };  return nil }() }() };  return nil }() }() }() }() }; return SkyErr(sky_concat("Unknown operator: ", op)) }() }() }() }() }() }() }() }() }() }()
}

func Compiler_Infer_InferDeclaration(counter any, registry any, env any, decl any, annotation any) any {
	return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "FunDecl" { name := sky_asMap(__subject)["V0"]; _ = name; params := sky_asMap(__subject)["V1"]; _ = params; body := sky_asMap(__subject)["V2"]; _ = body; return Compiler_Infer_InferFunction(counter, registry, env, name, params, body, annotation) };  if true { return SkyErr("inferDeclaration: not a function declaration") };  return nil }() }()
}

func Compiler_Infer_InferFunction(counter any, registry any, env any, name any, params any, body any, annotation any) any {
	return func() any { paramVars := sky_call(sky_listMap(func(p any) any { return freshVar(counter, SkyNothing()) }), params); _ = paramVars; bindResult := Compiler_Infer_BindParams(counter, registry, env, params, paramVars); _ = bindResult; return func() any { return func() any { __subject := bindResult; if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { paramSub := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = paramSub; paramEnv := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = paramEnv; return func() any { selfVar := freshVar(counter, SkyNothing()); _ = selfVar; envWithSelf := Compiler_Env_Extend(name, mono(selfVar), paramEnv); _ = envWithSelf; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, envWithSelf, body); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("In function '", sky_concat(name, sky_concat("': ", e)))) };  if sky_asSkyResult(__subject).SkyName == "Ok" { bodyResult := sky_asSkyResult(__subject).OkValue; _ = bodyResult; return func() any { bodySub := composeSubs(sky_asMap(bodyResult)["substitution"], paramSub); _ = bodySub; resolvedParamTypes := sky_call(sky_listMap(func(pv any) any { return applySub(bodySub, pv) }), paramVars); _ = resolvedParamTypes; bodyType := applySub(bodySub, sky_asMap(bodyResult)["type_"]); _ = bodyType; funType := sky_call2(sky_listFoldr(func(pt any) any { return func(acc any) any { return TFun(pt, acc) } }), bodyType, resolvedParamTypes); _ = funType; selfType := applySub(bodySub, selfVar); _ = selfType; return func() any { return func() any { __subject := unify(selfType, funType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Recursive type error in '", sky_concat(name, sky_concat("': ", e)))) };  if sky_asSkyResult(__subject).SkyName == "Ok" { selfSub := sky_asSkyResult(__subject).OkValue; _ = selfSub; return func() any { finalSub := composeSubs(selfSub, bodySub); _ = finalSub; finalType := applySub(finalSub, funType); _ = finalType; diagnostics := Compiler_Infer_CheckAnnotation(counter, env, finalType, annotation); _ = diagnostics; scheme := Compiler_Env_GeneralizeInEnv(env, finalType); _ = scheme; return SkyOk(map[string]any{"name": name, "scheme": scheme, "diagnostics": diagnostics}) }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }()
}

func Compiler_Infer_BindParams(counter any, registry any, env any, params any, types any) any {
	return Compiler_Infer_BindParamsLoop(counter, registry, env, params, types, emptySub)
}

func Compiler_Infer_BindParamsLoop(counter any, registry any, env any, params any, types any, sub any) any {
	return func() any { return func() any { __subject := params; if len(sky_asList(__subject)) == 0 { return SkyOk(SkyTuple2{V0: sub, V1: env}) };  if len(sky_asList(__subject)) > 0 { pat := sky_asList(__subject)[0]; _ = pat; restPats := sky_asList(__subject)[1:]; _ = restPats; return func() any { return func() any { __subject := types; if len(sky_asList(__subject)) == 0 { return SkyErr("Parameter count mismatch") };  if len(sky_asList(__subject)) > 0 { t := sky_asList(__subject)[0]; _ = t; restTypes := sky_asList(__subject)[1:]; _ = restTypes; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, pat, applySub(sub, t)); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { patResult := sky_asSkyResult(__subject).OkValue; _ = patResult; return func() any { combinedSub := composeSubs(sky_asMap(patResult)["substitution"], sub); _ = combinedSub; newEnv := sky_call2(sky_listFoldl(func(pair any) any { return func(acc any) any { return Compiler_Env_Extend(sky_fst(pair), mono(applySub(combinedSub, sky_snd(pair))), acc) } }), env, sky_asMap(patResult)["bindings"]); _ = newEnv; return Compiler_Infer_BindParamsLoop(counter, registry, newEnv, restPats, restTypes, combinedSub) }() };  return nil }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_CheckAnnotation(counter any, env any, inferredType any, annotation any) any {
	return func() any { return func() any { __subject := annotation; if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return []any{} };  if sky_asSkyMaybe(__subject).SkyName == "Just" { annotExpr := sky_asSkyMaybe(__subject).JustValue; _ = annotExpr; return func() any { annotType := Compiler_Adt_ResolveTypeExpr(sky_dictEmpty(), annotExpr); _ = annotType; return func() any { return func() any { __subject := unify(inferredType, annotType); if sky_asSkyResult(__subject).SkyName == "Ok" { return []any{} };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return []any{sky_concat("Type annotation mismatch: declared ", sky_concat(formatType(annotType), sky_concat(" but inferred ", sky_concat(formatType(inferredType), sky_concat(" (", sky_concat(e, ")"))))))} };  return nil }() }() }() };  return nil }() }()
}

func Compiler_Infer_ApplySubToEnv(sub any, env any) any {
	return sky_call(sky_dictMap(func(kk any) any { return func(scheme any) any { return applySubToScheme(sub, scheme) } }), env)
}

func Compiler_Infer_InferTupleItems(counter any, registry any, env any, items any, sub any, types any) any {
	return func() any { return func() any { __subject := items; if len(sky_asList(__subject)) == 0 { return SkyOk(map[string]any{"substitution": sub, "type_": TTuple(sky_listReverse(types))}) };  if len(sky_asList(__subject)) > 0 { item := sky_asList(__subject)[0]; _ = item; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, Compiler_Infer_ApplySubToEnv(sub, env), item); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return Compiler_Infer_InferTupleItems(counter, registry, env, rest, composeSubs(sky_asMap(result)["substitution"], sub), append([]any{sky_asMap(result)["type_"]}, sky_asList(types)...)) };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_InferListItems(counter any, registry any, env any, items any) any {
	return func() any { elemVar := freshVar(counter, SkyJust("elem")); _ = elemVar; return Compiler_Infer_InferListItemsLoop(counter, registry, env, items, emptySub, elemVar) }()
}

func Compiler_Infer_InferListItemsLoop(counter any, registry any, env any, items any, sub any, elemType any) any {
	return func() any { return func() any { __subject := items; if len(sky_asList(__subject)) == 0 { return SkyOk(map[string]any{"substitution": sub, "type_": TApp(TConst("List"), []any{applySub(sub, elemType)})}) };  if len(sky_asList(__subject)) > 0 { item := sky_asList(__subject)[0]; _ = item; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, Compiler_Infer_ApplySubToEnv(sub, env), item); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return func() any { itemSub := composeSubs(sky_asMap(result)["substitution"], sub); _ = itemSub; return func() any { return func() any { __subject := unify(applySub(itemSub, elemType), sky_asMap(result)["type_"]); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("List element type mismatch: ", e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return Compiler_Infer_InferListItemsLoop(counter, registry, env, rest, composeSubs(unifySub, itemSub), elemType) };  return nil }() }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_InferRecordFields(counter any, registry any, env any, fields any, sub any, fieldTypes any) any {
	return func() any { return func() any { __subject := fields; if len(sky_asList(__subject)) == 0 { return SkyOk(map[string]any{"substitution": sub, "type_": TRecord(fieldTypes)}) };  if len(sky_asList(__subject)) > 0 { field := sky_asList(__subject)[0]; _ = field; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, Compiler_Infer_ApplySubToEnv(sub, env), sky_asMap(field)["value"]); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return func() any { newSub := composeSubs(sky_asMap(result)["substitution"], sub); _ = newSub; return Compiler_Infer_InferRecordFields(counter, registry, env, rest, newSub, sky_call2(sky_dictInsert(sky_asMap(field)["name"]), sky_asMap(result)["type_"], fieldTypes)) }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_InferRecordUpdateFields(counter any, registry any, env any, fields any, sub any) any {
	return Compiler_Infer_InferRecordUpdateFieldsLoop(counter, registry, env, fields, sub, sky_dictEmpty())
}

func Compiler_Infer_InferRecordUpdateFieldsLoop(counter any, registry any, env any, fields any, sub any, fieldTypes any) any {
	return func() any { return func() any { __subject := fields; if len(sky_asList(__subject)) == 0 { return SkyOk(SkyTuple2{V0: sub, V1: fieldTypes}) };  if len(sky_asList(__subject)) > 0 { field := sky_asList(__subject)[0]; _ = field; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, Compiler_Infer_ApplySubToEnv(sub, env), sky_asMap(field)["value"]); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return Compiler_Infer_InferRecordUpdateFieldsLoop(counter, registry, env, rest, composeSubs(sky_asMap(result)["substitution"], sub), sky_call2(sky_dictInsert(sky_asMap(field)["name"]), sky_asMap(result)["type_"], fieldTypes)) };  return nil }() }() };  return nil }() }()
}

func Compiler_Unify_Unify(t1 any, t2 any) any {
	return func() any { return func() any { __subject := t1; if sky_asMap(__subject)["SkyName"] == "TVar" { id1 := sky_asMap(__subject)["V0"]; _ = id1; return Compiler_Unify_BindVar(id1, t2) };  if sky_asMap(__subject)["SkyName"] == "TConst" { nameA := sky_asMap(__subject)["V0"]; _ = nameA; return Compiler_Unify_UnifyConst(nameA, t1, t2) };  if sky_asMap(__subject)["SkyName"] == "TFun" { fromA := sky_asMap(__subject)["V0"]; _ = fromA; toA := sky_asMap(__subject)["V1"]; _ = toA; return Compiler_Unify_UnifyFun(fromA, toA, t2) };  if sky_asMap(__subject)["SkyName"] == "TApp" { ctorA := sky_asMap(__subject)["V0"]; _ = ctorA; argsA := sky_asMap(__subject)["V1"]; _ = argsA; return Compiler_Unify_UnifyApp(ctorA, argsA, t2) };  if sky_asMap(__subject)["SkyName"] == "TTuple" { itemsA := sky_asMap(__subject)["V0"]; _ = itemsA; return Compiler_Unify_UnifyTuple(itemsA, t2) };  if sky_asMap(__subject)["SkyName"] == "TRecord" { fieldsA := sky_asMap(__subject)["V0"]; _ = fieldsA; return Compiler_Unify_UnifyRecord(fieldsA, t2) };  return nil }() }()
}

func Compiler_Unify_UnifyConst(nameA any, t1 any, t2 any) any {
	return func() any { return func() any { __subject := t2; if sky_asMap(__subject)["SkyName"] == "TVar" { id2 := sky_asMap(__subject)["V0"]; _ = id2; return Compiler_Unify_BindVar(id2, t1) };  if sky_asMap(__subject)["SkyName"] == "TConst" { nameB := sky_asMap(__subject)["V0"]; _ = nameB; return func() any { if sky_asBool(sky_equal(nameA, nameB)) { return SkyOk(emptySub) }; return func() any { if sky_asBool(sky_asBool(Compiler_Unify_IsUniversalUnifier(nameA)) || sky_asBool(Compiler_Unify_IsUniversalUnifier(nameB))) { return SkyOk(emptySub) }; return func() any { if sky_asBool(Compiler_Unify_IsNumericCoercion(nameA, nameB)) { return SkyOk(emptySub) }; return SkyErr(sky_concat("Type mismatch: ", sky_concat(nameA, sky_concat(" vs ", nameB)))) }() }() }() };  if true { return func() any { if sky_asBool(Compiler_Unify_IsUniversalUnifier(nameA)) { return SkyOk(emptySub) }; return SkyErr(sky_concat("Cannot unify ", sky_concat(formatType(t1), sky_concat(" with ", formatType(t2))))) }() };  return nil }() }()
}

func Compiler_Unify_UnifyFun(fromA any, toA any, t2 any) any {
	return func() any { return func() any { __subject := t2; if sky_asMap(__subject)["SkyName"] == "TVar" { id2 := sky_asMap(__subject)["V0"]; _ = id2; return Compiler_Unify_BindVar(id2, TFun(fromA, toA)) };  if sky_asMap(__subject)["SkyName"] == "TFun" { fromB := sky_asMap(__subject)["V0"]; _ = fromB; toB := sky_asMap(__subject)["V1"]; _ = toB; return func() any { return func() any { __subject := Compiler_Unify_Unify(fromA, fromB); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asSkyResult(__subject).OkValue; _ = s1; return func() any { return func() any { __subject := Compiler_Unify_Unify(applySub(s1, toA), applySub(s1, toB)); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asSkyResult(__subject).OkValue; _ = s2; return SkyOk(composeSubs(s2, s1)) };  if sky_asMap(__subject)["SkyName"] == "TConst" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { if sky_asBool(Compiler_Unify_IsUniversalUnifier(name)) { return SkyOk(emptySub) }; return SkyErr(sky_concat("Cannot unify ", sky_concat(formatType(TFun(fromA, toA)), sky_concat(" with ", formatType(t2))))) }() };  if true { return SkyErr(sky_concat("Cannot unify ", sky_concat(formatType(TFun(fromA, toA)), sky_concat(" with ", formatType(t2))))) };  return nil }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Unify_UnifyApp(ctorA any, argsA any, t2 any) any {
	return func() any { return func() any { __subject := t2; if sky_asMap(__subject)["SkyName"] == "TVar" { id2 := sky_asMap(__subject)["V0"]; _ = id2; return Compiler_Unify_BindVar(id2, TApp(ctorA, argsA)) };  if sky_asMap(__subject)["SkyName"] == "TApp" { ctorB := sky_asMap(__subject)["V0"]; _ = ctorB; argsB := sky_asMap(__subject)["V1"]; _ = argsB; return func() any { return func() any { __subject := Compiler_Unify_Unify(ctorA, ctorB); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s0 := sky_asSkyResult(__subject).OkValue; _ = s0; return Compiler_Unify_UnifyList(sky_call(sky_listMap(func(x any) any { return applySub(s0, x) }), argsA), sky_call(sky_listMap(func(x any) any { return applySub(s0, x) }), argsB), s0) };  if sky_asMap(__subject)["SkyName"] == "TConst" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { if sky_asBool(Compiler_Unify_IsUniversalUnifier(name)) { return SkyOk(emptySub) }; return SkyErr(sky_concat("Cannot unify ", sky_concat(formatType(TApp(ctorA, argsA)), sky_concat(" with ", formatType(t2))))) }() };  if true { return SkyErr(sky_concat("Cannot unify ", sky_concat(formatType(TApp(ctorA, argsA)), sky_concat(" with ", formatType(t2))))) };  return nil }() }() };  return nil }() }()
}

func Compiler_Unify_UnifyTuple(itemsA any, t2 any) any {
	return func() any { return func() any { __subject := t2; if sky_asMap(__subject)["SkyName"] == "TVar" { id2 := sky_asMap(__subject)["V0"]; _ = id2; return Compiler_Unify_BindVar(id2, TTuple(itemsA)) };  if sky_asMap(__subject)["SkyName"] == "TTuple" { itemsB := sky_asMap(__subject)["V0"]; _ = itemsB; return func() any { if sky_asBool(!sky_equal(sky_listLength(itemsA), sky_listLength(itemsB))) { return SkyErr(sky_concat("Tuple arity mismatch: ", sky_concat(sky_stringFromInt(sky_listLength(itemsA)), sky_concat(" vs ", sky_stringFromInt(sky_listLength(itemsB)))))) }; return Compiler_Unify_UnifyList(itemsA, itemsB, emptySub) }() };  if sky_asMap(__subject)["SkyName"] == "TConst" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { if sky_asBool(Compiler_Unify_IsUniversalUnifier(name)) { return SkyOk(emptySub) }; return SkyErr(sky_concat("Cannot unify ", sky_concat(formatType(TTuple(itemsA)), sky_concat(" with ", formatType(t2))))) }() };  if true { return SkyErr(sky_concat("Cannot unify ", sky_concat(formatType(TTuple(itemsA)), sky_concat(" with ", formatType(t2))))) };  return nil }() }()
}

func Compiler_Unify_UnifyRecord(fieldsA any, t2 any) any {
	return func() any { return func() any { __subject := t2; if sky_asMap(__subject)["SkyName"] == "TVar" { id2 := sky_asMap(__subject)["V0"]; _ = id2; return Compiler_Unify_BindVar(id2, TRecord(fieldsA)) };  if sky_asMap(__subject)["SkyName"] == "TRecord" { fieldsB := sky_asMap(__subject)["V0"]; _ = fieldsB; return Compiler_Unify_UnifyRecords(fieldsA, fieldsB) };  if sky_asMap(__subject)["SkyName"] == "TConst" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { if sky_asBool(Compiler_Unify_IsUniversalUnifier(name)) { return SkyOk(emptySub) }; return SkyErr(sky_concat("Cannot unify ", sky_concat(formatType(TRecord(fieldsA)), sky_concat(" with ", formatType(t2))))) }() };  if true { return SkyErr(sky_concat("Cannot unify ", sky_concat(formatType(TRecord(fieldsA)), sky_concat(" with ", formatType(t2))))) };  return nil }() }()
}

func Compiler_Unify_BindVar(id any, t any) any {
	return func() any { return func() any { __subject := t; if sky_asMap(__subject)["SkyName"] == "TVar" { otherId := sky_asMap(__subject)["V0"]; _ = otherId; return func() any { if sky_asBool(sky_equal(id, otherId)) { return SkyOk(emptySub) }; return SkyOk(sky_call2(sky_dictInsert(id), t, sky_dictEmpty())) }() };  if true { return func() any { if sky_asBool(sky_call(sky_setMember(id), freeVars(t))) { return SkyErr(sky_concat("Infinite type: t", sky_concat(sky_stringFromInt(id), sky_concat(" occurs in ", formatType(t))))) }; return SkyOk(sky_call2(sky_dictInsert(id), t, sky_dictEmpty())) }() };  return nil }() }()
}

func Compiler_Unify_IsUniversalUnifier(name any) any {
	return sky_asBool(sky_equal(name, "JsValue")) || sky_asBool(sky_asBool(sky_equal(name, "Foreign")) || sky_asBool(sky_equal(name, "Any")))
}

func Compiler_Unify_IsNumericCoercion(nameA any, nameB any) any {
	return sky_asBool(sky_asBool(sky_equal(nameA, "Int")) && sky_asBool(sky_equal(nameB, "Float"))) || sky_asBool(sky_asBool(sky_equal(nameA, "Float")) && sky_asBool(sky_equal(nameB, "Int")))
}

func Compiler_Unify_UnifyList(ts1 any, ts2 any, sub any) any {
	return func() any { return func() any { __subject := ts1; if len(sky_asList(__subject)) == 0 { return func() any { return func() any { __subject := ts2; if len(sky_asList(__subject)) == 0 { return SkyOk(sub) };  if true { return SkyErr("Type argument count mismatch") };  if len(sky_asList(__subject)) > 0 { t1head := sky_asList(__subject)[0]; _ = t1head; rest1 := sky_asList(__subject)[1:]; _ = rest1; return func() any { return func() any { __subject := ts2; if len(sky_asList(__subject)) == 0 { return SkyErr("Type argument count mismatch") };  if len(sky_asList(__subject)) > 0 { t2head := sky_asList(__subject)[0]; _ = t2head; rest2 := sky_asList(__subject)[1:]; _ = rest2; return func() any { return func() any { __subject := Compiler_Unify_Unify(applySub(sub, t1head), applySub(sub, t2head)); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s := sky_asSkyResult(__subject).OkValue; _ = s; return Compiler_Unify_UnifyList(rest1, rest2, composeSubs(s, sub)) };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Unify_UnifyRecords(fieldsA any, fieldsB any) any {
	return func() any { allKeys := sky_setToList(sky_call(sky_setUnion(sky_setFromList(sky_dictKeys(fieldsA))), sky_setFromList(sky_dictKeys(fieldsB)))); _ = allKeys; return Compiler_Unify_UnifyRecordFields(allKeys, fieldsA, fieldsB, emptySub) }()
}

func Compiler_Unify_UnifyRecordFields(keys any, fieldsA any, fieldsB any, sub any) any {
	return func() any { return func() any { __subject := keys; if len(sky_asList(__subject)) == 0 { return SkyOk(sub) };  if len(sky_asList(__subject)) > 0 { key := sky_asList(__subject)[0]; _ = key; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { valA := sky_call(sky_dictGet(key), fieldsA); _ = valA; valB := sky_call(sky_dictGet(key), fieldsB); _ = valB; return func() any { return func() any { __subject := valA; if sky_asSkyMaybe(__subject).SkyName == "Just" { typeA := sky_asSkyMaybe(__subject).JustValue; _ = typeA; return func() any { return func() any { __subject := valB; if sky_asSkyMaybe(__subject).SkyName == "Just" { typeB := sky_asSkyMaybe(__subject).JustValue; _ = typeB; return func() any { return func() any { __subject := Compiler_Unify_Unify(applySub(sub, typeA), applySub(sub, typeB)); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("In record field '", sky_concat(key, sky_concat("': ", e)))) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s := sky_asSkyResult(__subject).OkValue; _ = s; return Compiler_Unify_UnifyRecordFields(rest, fieldsA, fieldsB, composeSubs(s, sub)) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return Compiler_Unify_UnifyRecordFields(rest, fieldsA, fieldsB, sub) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return Compiler_Unify_UnifyRecordFields(rest, fieldsA, fieldsB, sub) };  return nil }() }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }()
}

var FormatModule = Formatter_Format_FormatModule

var FormatModuleHeader = Formatter_Format_FormatModuleHeader

var FormatExposing = Formatter_Format_FormatExposing

var FormatImport = Formatter_Format_FormatImport

var FormatDeclarations = Formatter_Format_FormatDeclarations

var FormatDeclaration = Formatter_Format_FormatDeclaration

var FormatFunction = Formatter_Format_FormatFunction

var FormatTypeDecl = Formatter_Format_FormatTypeDecl

var FormatVariant = Formatter_Format_FormatVariant

var FormatTypeAlias = Formatter_Format_FormatTypeAlias

var FormatExpr = Formatter_Format_FormatExpr

var FormatTuple = Formatter_Format_FormatTuple

var FormatList = Formatter_Format_FormatList

var FormatRecord = Formatter_Format_FormatRecord

var FormatField = Formatter_Format_FormatField

var FormatRecordUpdate = Formatter_Format_FormatRecordUpdate

var FormatCall = Formatter_Format_FormatCall

var FormatLambda = Formatter_Format_FormatLambda

var FormatBinary = Formatter_Format_FormatBinary

var FormatIf = Formatter_Format_FormatIf

var FormatLet = Formatter_Format_FormatLet

var FormatLetBinding = Formatter_Format_FormatLetBinding

var FormatCase = Formatter_Format_FormatCase

var FormatBranch = Formatter_Format_FormatBranch

var FormatPattern = Formatter_Format_FormatPattern

var FormatPatternParens = Formatter_Format_FormatPatternParens

var FormatLiteral = Formatter_Format_FormatLiteral

var FormatTypeExpr = Formatter_Format_FormatTypeExpr

var FormatTypeExprParens = Formatter_Format_FormatTypeExprParens

var FormatRecordType = Formatter_Format_FormatRecordType

var FormatNamedType = Formatter_Format_FormatNamedType

var QuoteString = Formatter_Format_QuoteString

var DocText = Formatter_Doc_DocText

var DocLine = Formatter_Doc_DocLine

var DocSoftline = Formatter_Doc_DocSoftline

var DocHardline = Formatter_Doc_DocHardline

var DocConcat = Formatter_Doc_DocConcat

var DocIndent = Formatter_Doc_DocIndent

var DocGroup = Formatter_Doc_DocGroup

var DocAlign = Formatter_Doc_DocAlign

var text = Formatter_Doc_Text

var Text = Formatter_Doc_Text

var line = Formatter_Doc_Line()

var Line = Formatter_Doc_Line()

var hardline = Formatter_Doc_Hardline()

var Hardline = Formatter_Doc_Hardline()

var softline = Formatter_Doc_Softline()

var Softline = Formatter_Doc_Softline()

var concat = Formatter_Doc_Concat

var Concat = Formatter_Doc_Concat

var indent = Formatter_Doc_Indent

var Indent = Formatter_Doc_Indent

var group = Formatter_Doc_Group

var Group = Formatter_Doc_Group

var align = Formatter_Doc_Align

var Align = Formatter_Doc_Align

var joinDocs = Formatter_Doc_JoinDocs

var JoinDocs = Formatter_Doc_JoinDocs

var maxWidth = Formatter_Doc_MaxWidth()

var MaxWidth = Formatter_Doc_MaxWidth()

var indentWidth = Formatter_Doc_IndentWidth()

var IndentWidth = Formatter_Doc_IndentWidth()

var render = Formatter_Doc_Render

var Render = Formatter_Doc_Render

var writeStr = Formatter_Doc_WriteStr

var WriteStr = Formatter_Doc_WriteStr

var newline = Formatter_Doc_Newline

var Newline = Formatter_Doc_Newline

var makeSpaces = Formatter_Doc_MakeSpaces

var MakeSpaces = Formatter_Doc_MakeSpaces

var flatWidth = Formatter_Doc_FlatWidth

var FlatWidth = Formatter_Doc_FlatWidth

var fits = Formatter_Doc_Fits

var Fits = Formatter_Doc_Fits

var fitsConcat = Formatter_Doc_FitsConcat

var FitsConcat = Formatter_Doc_FitsConcat

var walk = Formatter_Doc_Walk

var Walk = Formatter_Doc_Walk

var walkParts = Formatter_Doc_WalkParts

var WalkParts = Formatter_Doc_WalkParts

var flatten = Formatter_Doc_Flatten

var Flatten = Formatter_Doc_Flatten

var flattenParts = Formatter_Doc_FlattenParts

var FlattenParts = Formatter_Doc_FlattenParts

func Formatter_Format_FormatModule(mod any) any {
	return func() any { header := Formatter_Format_FormatModuleHeader(mod); _ = header; importDocs := sky_call(sky_listMap(Formatter_Format_FormatImport), sky_asMap(mod)["imports"]); _ = importDocs; declDocs := Formatter_Format_FormatDeclarations(sky_asMap(mod)["declarations"]); _ = declDocs; allDocs := func() any { if sky_asBool(sky_listIsEmpty(sky_asMap(mod)["imports"])) { return concat([]any{header, hardline, hardline, declDocs}) }; return concat([]any{header, hardline, hardline, concat(sky_call(sky_listMap(func(d any) any { return concat([]any{d, hardline}) }), importDocs)), hardline, declDocs}) }(); _ = allDocs; return sky_concat(sky_stringTrim(render(allDocs)), "\n") }()
}

func Formatter_Format_FormatModuleHeader(mod any) any {
	return func() any { name := sky_call(sky_stringJoin("."), sky_asMap(mod)["name"]); _ = name; exposing_ := Formatter_Format_FormatExposing(sky_asMap(mod)["exposing_"]); _ = exposing_; return concat([]any{text("module "), text(name), text(" exposing "), exposing_}) }()
}

func Formatter_Format_FormatExposing(clause any) any {
	return func() any { return func() any { __subject := clause; if sky_asMap(__subject)["SkyName"] == "ExposeAll" { return text("(..)") };  if sky_asMap(__subject)["SkyName"] == "ExposeList" { items := sky_asMap(__subject)["V0"]; _ = items; return group(concat([]any{text("("), text(sky_call(sky_stringJoin(", "), items)), text(")")})) };  if sky_asMap(__subject)["SkyName"] == "ExposeNone" { return text("(..)") };  return nil }() }()
}

func Formatter_Format_FormatImport(imp any) any {
	return func() any { name := sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]); _ = name; aliasDoc := func() any { if sky_asBool(sky_stringIsEmpty(sky_asMap(imp)["alias_"])) { return text("") }; return concat([]any{text(" as "), text(sky_asMap(imp)["alias_"])}) }(); _ = aliasDoc; exposingDoc := func() any { return func() any { __subject := sky_asMap(imp)["exposing_"]; if sky_asMap(__subject)["SkyName"] == "ExposeAll" { return text(" exposing (..)") };  if sky_asMap(__subject)["SkyName"] == "ExposeList" { items := sky_asMap(__subject)["V0"]; _ = items; return concat([]any{text(" exposing ("), text(sky_call(sky_stringJoin(", "), items)), text(")")}) };  if sky_asMap(__subject)["SkyName"] == "ExposeNone" { return text("") };  return nil }() }(); _ = exposingDoc; return concat([]any{text("import "), text(name), aliasDoc, exposingDoc}) }()
}

func Formatter_Format_FormatDeclarations(decls any) any {
	return joinDocs(sky_call(sky_listMap(Formatter_Format_FormatDeclaration), decls), concat([]any{hardline, hardline, hardline}))
}

func Formatter_Format_FormatDeclaration(decl any) any {
	return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "FunDecl" { name := sky_asMap(__subject)["V0"]; _ = name; params := sky_asMap(__subject)["V1"]; _ = params; body := sky_asMap(__subject)["V2"]; _ = body; return Formatter_Format_FormatFunction(name, params, body) };  if sky_asMap(__subject)["SkyName"] == "TypeAnnotDecl" { name := sky_asMap(__subject)["V0"]; _ = name; typeExpr := sky_asMap(__subject)["V1"]; _ = typeExpr; return concat([]any{text(name), text(" : "), Formatter_Format_FormatTypeExpr(typeExpr)}) };  if sky_asMap(__subject)["SkyName"] == "TypeDecl" { name := sky_asMap(__subject)["V0"]; _ = name; typeParams := sky_asMap(__subject)["V1"]; _ = typeParams; variants := sky_asMap(__subject)["V2"]; _ = variants; return Formatter_Format_FormatTypeDecl(name, typeParams, variants) };  if sky_asMap(__subject)["SkyName"] == "TypeAliasDecl" { name := sky_asMap(__subject)["V0"]; _ = name; typeParams := sky_asMap(__subject)["V1"]; _ = typeParams; aliasType := sky_asMap(__subject)["V2"]; _ = aliasType; return Formatter_Format_FormatTypeAlias(name, typeParams, aliasType) };  if sky_asMap(__subject)["SkyName"] == "ForeignImportDecl" { name := sky_asMap(__subject)["V0"]; _ = name; pkg := sky_asMap(__subject)["V1"]; _ = pkg; importName := sky_asMap(__subject)["V2"]; _ = importName; return concat([]any{text("foreign import \""), text(pkg), text("\" exposing ("), text(name), text(")")}) };  return nil }() }()
}

func Formatter_Format_FormatFunction(name any, params any, body any) any {
	return func() any { paramDocs := sky_call(sky_listMap(Formatter_Format_FormatPattern), params); _ = paramDocs; header := func() any { if sky_asBool(sky_listIsEmpty(params)) { return concat([]any{text(name), text(" =")}) }; return concat([]any{text(name), text(" "), joinDocs(paramDocs, text(" ")), text(" =")}) }(); _ = header; return concat([]any{header, indent(concat([]any{hardline, Formatter_Format_FormatExpr(body)}))}) }()
}

func Formatter_Format_FormatTypeDecl(name any, params any, variants any) any {
	return func() any { paramStr := func() any { if sky_asBool(sky_listIsEmpty(params)) { return "" }; return sky_concat(" ", sky_call(sky_stringJoin(" "), params)) }(); _ = paramStr; header := text(sky_concat("type ", sky_concat(name, paramStr))); _ = header; return func() any { return func() any { __subject := variants; if len(sky_asList(__subject)) == 0 { return header };  if len(sky_asList(__subject)) > 0 { first := sky_asList(__subject)[0]; _ = first; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { firstDoc := concat([]any{text("= "), Formatter_Format_FormatVariant(first)}); _ = firstDoc; restDocs := sky_call(sky_listMap(func(v any) any { return concat([]any{text("| "), Formatter_Format_FormatVariant(v)}) }), rest); _ = restDocs; return concat([]any{header, indent(concat([]any{hardline, joinDocs(append([]any{firstDoc}, sky_asList(restDocs)...), hardline)}))}) }() };  return nil }() }() }()
}

func Formatter_Format_FormatVariant(variant any) any {
	return func() any { if sky_asBool(sky_listIsEmpty(sky_asMap(variant)["fields"])) { return text(sky_asMap(variant)["name"]) }; return concat([]any{text(sky_asMap(variant)["name"]), text(" "), joinDocs(sky_call(sky_listMap(Formatter_Format_FormatTypeExprParens), sky_asMap(variant)["fields"]), text(" "))}) }()
}

func Formatter_Format_FormatTypeAlias(name any, params any, aliasType any) any {
	return func() any { paramStr := func() any { if sky_asBool(sky_listIsEmpty(params)) { return "" }; return sky_concat(" ", sky_call(sky_stringJoin(" "), params)) }(); _ = paramStr; return concat([]any{text(sky_concat("type alias ", sky_concat(name, sky_concat(paramStr, " =")))), indent(concat([]any{hardline, Formatter_Format_FormatTypeExpr(aliasType)}))}) }()
}

func Formatter_Format_FormatExpr(expr any) any {
	return func() any { return func() any { __subject := expr; if sky_asMap(__subject)["SkyName"] == "IdentifierExpr" { name := sky_asMap(__subject)["V0"]; _ = name; return text(name) };  if sky_asMap(__subject)["SkyName"] == "QualifiedExpr" { parts := sky_asMap(__subject)["V0"]; _ = parts; return text(sky_call(sky_stringJoin("."), parts)) };  if sky_asMap(__subject)["SkyName"] == "IntLitExpr" { raw := sky_asMap(__subject)["V1"]; _ = raw; return text(raw) };  if sky_asMap(__subject)["SkyName"] == "FloatLitExpr" { raw := sky_asMap(__subject)["V1"]; _ = raw; return text(raw) };  if sky_asMap(__subject)["SkyName"] == "StringLitExpr" { s := sky_asMap(__subject)["V0"]; _ = s; return text(sky_concat("\"", sky_concat(Formatter_Format_QuoteString(s), "\""))) };  if sky_asMap(__subject)["SkyName"] == "CharLitExpr" { s := sky_asMap(__subject)["V0"]; _ = s; return text(s) };  if sky_asMap(__subject)["SkyName"] == "BoolLitExpr" { b := sky_asMap(__subject)["V0"]; _ = b; return func() any { if sky_asBool(b) { return text("True") }; return text("False") }() };  if sky_asMap(__subject)["SkyName"] == "UnitExpr" { return text("()") };  if sky_asMap(__subject)["SkyName"] == "TupleExpr" { items := sky_asMap(__subject)["V0"]; _ = items; return Formatter_Format_FormatTuple(items) };  if sky_asMap(__subject)["SkyName"] == "ListExpr" { items := sky_asMap(__subject)["V0"]; _ = items; return Formatter_Format_FormatList(items) };  if sky_asMap(__subject)["SkyName"] == "RecordExpr" { fields := sky_asMap(__subject)["V0"]; _ = fields; return Formatter_Format_FormatRecord(fields) };  if sky_asMap(__subject)["SkyName"] == "RecordUpdateExpr" { base := sky_asMap(__subject)["V0"]; _ = base; fields := sky_asMap(__subject)["V1"]; _ = fields; return Formatter_Format_FormatRecordUpdate(base, fields) };  if sky_asMap(__subject)["SkyName"] == "FieldAccessExpr" { target := sky_asMap(__subject)["V0"]; _ = target; fieldName := sky_asMap(__subject)["V1"]; _ = fieldName; return concat([]any{Formatter_Format_FormatExpr(target), text("."), text(fieldName)}) };  if sky_asMap(__subject)["SkyName"] == "CallExpr" { callee := sky_asMap(__subject)["V0"]; _ = callee; args := sky_asMap(__subject)["V1"]; _ = args; return Formatter_Format_FormatCall(callee, args) };  if sky_asMap(__subject)["SkyName"] == "LambdaExpr" { params := sky_asMap(__subject)["V0"]; _ = params; body := sky_asMap(__subject)["V1"]; _ = body; return Formatter_Format_FormatLambda(params, body) };  if sky_asMap(__subject)["SkyName"] == "IfExpr" { condition := sky_asMap(__subject)["V0"]; _ = condition; thenBranch := sky_asMap(__subject)["V1"]; _ = thenBranch; elseBranch := sky_asMap(__subject)["V2"]; _ = elseBranch; return Formatter_Format_FormatIf(condition, thenBranch, elseBranch) };  if sky_asMap(__subject)["SkyName"] == "LetExpr" { bindings := sky_asMap(__subject)["V0"]; _ = bindings; body := sky_asMap(__subject)["V1"]; _ = body; return Formatter_Format_FormatLet(bindings, body) };  if sky_asMap(__subject)["SkyName"] == "CaseExpr" { subject := sky_asMap(__subject)["V0"]; _ = subject; branches := sky_asMap(__subject)["V1"]; _ = branches; return Formatter_Format_FormatCase(subject, branches) };  if sky_asMap(__subject)["SkyName"] == "BinaryExpr" { op := sky_asMap(__subject)["V0"]; _ = op; leftExpr := sky_asMap(__subject)["V1"]; _ = leftExpr; rightExpr := sky_asMap(__subject)["V2"]; _ = rightExpr; return Formatter_Format_FormatBinary(op, leftExpr, rightExpr) };  if sky_asMap(__subject)["SkyName"] == "NegateExpr" { inner := sky_asMap(__subject)["V0"]; _ = inner; return concat([]any{text("-"), Formatter_Format_FormatExpr(inner)}) };  if sky_asMap(__subject)["SkyName"] == "ParenExpr" { inner := sky_asMap(__subject)["V0"]; _ = inner; return group(concat([]any{text("("), softline, Formatter_Format_FormatExpr(inner), softline, text(")")})) };  return nil }() }()
}

func Formatter_Format_FormatTuple(items any) any {
	return func() any { return func() any { __subject := items; if len(sky_asList(__subject)) == 0 { return text("()") };  if len(sky_asList(__subject)) > 0 { first := sky_asList(__subject)[0]; _ = first; rest := sky_asList(__subject)[1:]; _ = rest; return group(concat([]any{text("( "), Formatter_Format_FormatExpr(first), concat(sky_call(sky_listMap(func(item any) any { return concat([]any{line, text(", "), Formatter_Format_FormatExpr(item)}) }), rest)), line, text(")")})) };  return nil }() }()
}

func Formatter_Format_FormatList(items any) any {
	return func() any { return func() any { __subject := items; if len(sky_asList(__subject)) == 0 { return text("[]") };  if len(sky_asList(__subject)) > 0 { first := sky_asList(__subject)[0]; _ = first; rest := sky_asList(__subject)[1:]; _ = rest; return group(concat([]any{text("[ "), Formatter_Format_FormatExpr(first), concat(sky_call(sky_listMap(func(item any) any { return concat([]any{line, text(", "), Formatter_Format_FormatExpr(item)}) }), rest)), line, text("]")})) };  return nil }() }()
}

func Formatter_Format_FormatRecord(fields any) any {
	return func() any { return func() any { __subject := fields; if len(sky_asList(__subject)) == 0 { return text("{}") };  if len(sky_asList(__subject)) > 0 { first := sky_asList(__subject)[0]; _ = first; rest := sky_asList(__subject)[1:]; _ = rest; return group(align(concat([]any{text("{ "), Formatter_Format_FormatField(first), concat(sky_call(sky_listMap(func(f any) any { return concat([]any{line, text(", "), Formatter_Format_FormatField(f)}) }), rest)), line, text("}")}))) };  return nil }() }()
}

func Formatter_Format_FormatField(field any) any {
	return concat([]any{text(sky_asMap(field)["name"]), text(" = "), Formatter_Format_FormatExpr(sky_asMap(field)["value"])})
}

func Formatter_Format_FormatRecordUpdate(base any, fields any) any {
	return group(align(concat([]any{text("{ "), Formatter_Format_FormatExpr(base), line, text("| "), joinDocs(sky_call(sky_listMap(Formatter_Format_FormatField), fields), concat([]any{line, text(", ")})), line, text("}")})))
}

func Formatter_Format_FormatCall(callee any, args any) any {
	return group(concat([]any{Formatter_Format_FormatExpr(callee), indent(concat(sky_call(sky_listMap(func(arg any) any { return concat([]any{line, Formatter_Format_FormatExpr(arg)}) }), args)))}))
}

func Formatter_Format_FormatLambda(params any, body any) any {
	return group(concat([]any{text("\\"), joinDocs(sky_call(sky_listMap(Formatter_Format_FormatPattern), params), text(" ")), text(" ->"), indent(concat([]any{line, Formatter_Format_FormatExpr(body)}))}))
}

func Formatter_Format_FormatBinary(op any, leftExpr any, rightExpr any) any {
	return func() any { if sky_asBool(sky_asBool(sky_equal(op, "|>")) || sky_asBool(sky_equal(op, "<|"))) { return concat([]any{Formatter_Format_FormatExpr(leftExpr), indent(concat([]any{hardline, text(op), text(" "), Formatter_Format_FormatExpr(rightExpr)}))}) }; return group(concat([]any{Formatter_Format_FormatExpr(leftExpr), text(" "), text(op), line, Formatter_Format_FormatExpr(rightExpr)})) }()
}

func Formatter_Format_FormatIf(condition any, thenBranch any, elseBranch any) any {
	return concat([]any{text("if "), Formatter_Format_FormatExpr(condition), text(" then"), indent(concat([]any{hardline, Formatter_Format_FormatExpr(thenBranch)})), hardline, text("else"), indent(concat([]any{hardline, Formatter_Format_FormatExpr(elseBranch)}))})
}

func Formatter_Format_FormatLet(bindings any, body any) any {
	return concat([]any{text("let"), indent(concat(sky_call(sky_listMap(Formatter_Format_FormatLetBinding), bindings))), hardline, text("in"), indent(concat([]any{hardline, Formatter_Format_FormatExpr(body)}))})
}

func Formatter_Format_FormatLetBinding(binding any) any {
	return concat([]any{hardline, Formatter_Format_FormatPattern(sky_asMap(binding)["pattern"]), text(" ="), indent(concat([]any{hardline, Formatter_Format_FormatExpr(sky_asMap(binding)["value"])}))})
}

func Formatter_Format_FormatCase(subject any, branches any) any {
	return concat([]any{text("case "), Formatter_Format_FormatExpr(subject), text(" of"), indent(concat(sky_call(sky_listMap(Formatter_Format_FormatBranch), branches)))})
}

func Formatter_Format_FormatBranch(branch any) any {
	return concat([]any{hardline, hardline, Formatter_Format_FormatPattern(sky_asMap(branch)["pattern"]), text(" ->"), indent(concat([]any{hardline, Formatter_Format_FormatExpr(sky_asMap(branch)["body"])}))})
}

func Formatter_Format_FormatPattern(pat any) any {
	return func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PWildcard" { return text("_") };  if sky_asMap(__subject)["SkyName"] == "PVariable" { name := sky_asMap(__subject)["V0"]; _ = name; return text(name) };  if sky_asMap(__subject)["SkyName"] == "PConstructor" { parts := sky_asMap(__subject)["V0"]; _ = parts; argPats := sky_asMap(__subject)["V1"]; _ = argPats; return func() any { ctorName := sky_call(sky_stringJoin("."), parts); _ = ctorName; return func() any { if sky_asBool(sky_listIsEmpty(argPats)) { return text(ctorName) }; return concat([]any{text(ctorName), text(" "), joinDocs(sky_call(sky_listMap(Formatter_Format_FormatPatternParens), argPats), text(" "))}) }() }() };  if sky_asMap(__subject)["SkyName"] == "PLiteral" { lit := sky_asMap(__subject)["V0"]; _ = lit; return Formatter_Format_FormatLiteral(lit) };  if sky_asMap(__subject)["SkyName"] == "PTuple" { items := sky_asMap(__subject)["V0"]; _ = items; return group(concat([]any{text("( "), joinDocs(sky_call(sky_listMap(Formatter_Format_FormatPattern), items), text(" , ")), text(" )")})) };  if sky_asMap(__subject)["SkyName"] == "PList" { items := sky_asMap(__subject)["V0"]; _ = items; return group(concat([]any{text("[ "), joinDocs(sky_call(sky_listMap(Formatter_Format_FormatPattern), items), text(" , ")), text(" ]")})) };  if sky_asMap(__subject)["SkyName"] == "PCons" { headPat := sky_asMap(__subject)["V0"]; _ = headPat; tailPat := sky_asMap(__subject)["V1"]; _ = tailPat; return concat([]any{Formatter_Format_FormatPattern(headPat), text(" :: "), Formatter_Format_FormatPattern(tailPat)}) };  if sky_asMap(__subject)["SkyName"] == "PAs" { innerPat := sky_asMap(__subject)["V0"]; _ = innerPat; name := sky_asMap(__subject)["V1"]; _ = name; return concat([]any{Formatter_Format_FormatPattern(innerPat), text(" as "), text(name)}) };  if sky_asMap(__subject)["SkyName"] == "PRecord" { fields := sky_asMap(__subject)["V0"]; _ = fields; return text(sky_concat("{ ", sky_concat(sky_call(sky_stringJoin(" , "), fields), " }"))) };  return nil }() }()
}

func Formatter_Format_FormatPatternParens(pat any) any {
	return func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PConstructor" { args := sky_asMap(__subject)["V1"]; _ = args; return func() any { if sky_asBool(sky_listIsEmpty(args)) { return Formatter_Format_FormatPattern(pat) }; return concat([]any{text("("), Formatter_Format_FormatPattern(pat), text(")")}) }() };  if sky_asMap(__subject)["SkyName"] == "PTuple" { return Formatter_Format_FormatPattern(pat) };  if true { return Formatter_Format_FormatPattern(pat) };  return nil }() }()
}

func Formatter_Format_FormatLiteral(lit any) any {
	return func() any { return func() any { __subject := lit; if sky_asMap(__subject)["SkyName"] == "LitInt" { n := sky_asMap(__subject)["V0"]; _ = n; return text(sky_stringFromInt(n)) };  if sky_asMap(__subject)["SkyName"] == "LitFloat" { f := sky_asMap(__subject)["V0"]; _ = f; return text(sky_stringFromFloat(f)) };  if sky_asMap(__subject)["SkyName"] == "LitString" { s := sky_asMap(__subject)["V0"]; _ = s; return text(sky_concat("\"", sky_concat(Formatter_Format_QuoteString(s), "\""))) };  if sky_asMap(__subject)["SkyName"] == "LitChar" { c := sky_asMap(__subject)["V0"]; _ = c; return text(sky_concat("'", sky_concat(c, "'"))) };  if sky_asMap(__subject)["SkyName"] == "LitBool" { b := sky_asMap(__subject)["V0"]; _ = b; return func() any { if sky_asBool(b) { return text("True") }; return text("False") }() };  return nil }() }()
}

func Formatter_Format_FormatTypeExpr(texpr any) any {
	return func() any { return func() any { __subject := texpr; if sky_asMap(__subject)["SkyName"] == "TypeRef" { parts := sky_asMap(__subject)["V0"]; _ = parts; args := sky_asMap(__subject)["V1"]; _ = args; return func() any { name := sky_call(sky_stringJoin("."), parts); _ = name; return func() any { if sky_asBool(sky_listIsEmpty(args)) { return text(name) }; return concat([]any{text(name), text(" "), joinDocs(sky_call(sky_listMap(Formatter_Format_FormatTypeExprParens), args), text(" "))}) }() }() };  if sky_asMap(__subject)["SkyName"] == "TypeVar" { name := sky_asMap(__subject)["V0"]; _ = name; return text(name) };  if sky_asMap(__subject)["SkyName"] == "FunType" { fromT := sky_asMap(__subject)["V0"]; _ = fromT; toT := sky_asMap(__subject)["V1"]; _ = toT; return concat([]any{Formatter_Format_FormatTypeExprParens(fromT), text(" -> "), Formatter_Format_FormatTypeExpr(toT)}) };  if sky_asMap(__subject)["SkyName"] == "RecordTypeExpr" { fields := sky_asMap(__subject)["V0"]; _ = fields; return Formatter_Format_FormatRecordType(fields) };  if sky_asMap(__subject)["SkyName"] == "TupleTypeExpr" { items := sky_asMap(__subject)["V0"]; _ = items; return group(concat([]any{text("( "), joinDocs(sky_call(sky_listMap(Formatter_Format_FormatTypeExpr), items), text(" , ")), text(" )")})) };  if sky_asMap(__subject)["SkyName"] == "UnitTypeExpr" { return text("()") };  return nil }() }()
}

func Formatter_Format_FormatTypeExprParens(texpr any) any {
	return func() any { return func() any { __subject := texpr; if sky_asMap(__subject)["SkyName"] == "FunType" { return concat([]any{text("("), Formatter_Format_FormatTypeExpr(texpr), text(")")}) };  if sky_asMap(__subject)["SkyName"] == "TypeRef" { args := sky_asMap(__subject)["V1"]; _ = args; return func() any { if sky_asBool(sky_listIsEmpty(args)) { return Formatter_Format_FormatTypeExpr(texpr) }; return concat([]any{text("("), Formatter_Format_FormatTypeExpr(texpr), text(")")}) }() };  if true { return Formatter_Format_FormatTypeExpr(texpr) };  return nil }() }()
}

func Formatter_Format_FormatRecordType(fields any) any {
	return func() any { return func() any { __subject := fields; if len(sky_asList(__subject)) == 0 { return text("{}") };  if len(sky_asList(__subject)) > 0 { first := sky_asList(__subject)[0]; _ = first; rest := sky_asList(__subject)[1:]; _ = rest; return group(concat([]any{text("{ "), Formatter_Format_FormatNamedType(first), concat(sky_call(sky_listMap(func(f any) any { return concat([]any{line, text(", "), Formatter_Format_FormatNamedType(f)}) }), rest)), line, text("}")})) };  return nil }() }()
}

func Formatter_Format_FormatNamedType(field any) any {
	return concat([]any{text(sky_asMap(field)["name"]), text(" : "), Formatter_Format_FormatTypeExpr(sky_asMap(field)["type_"])})
}

func Formatter_Format_QuoteString(s any) any {
	return func() any { s1 := sky_call2(sky_stringReplace("\\"), "\\\\", s); _ = s1; s2 := sky_call2(sky_stringReplace("\""), "\\\"", s1); _ = s2; s3 := sky_call2(sky_stringReplace("\n"), "\\n", s2); _ = s3; s4 := sky_call2(sky_stringReplace("\r"), "\\r", s3); _ = s4; return sky_call2(sky_stringReplace("\t"), "\\t", s4) }()
}

func Formatter_Doc_DocText(v0 any) any {
	return map[string]any{"Tag": 0, "SkyName": "DocText", "V0": v0}
}

var Formatter_Doc_DocLine = map[string]any{"Tag": 1, "SkyName": "DocLine"}

var Formatter_Doc_DocSoftline = map[string]any{"Tag": 2, "SkyName": "DocSoftline"}

var Formatter_Doc_DocHardline = map[string]any{"Tag": 3, "SkyName": "DocHardline"}

func Formatter_Doc_DocConcat(v0 any) any {
	return map[string]any{"Tag": 4, "SkyName": "DocConcat", "V0": v0}
}

func Formatter_Doc_DocIndent(v0 any) any {
	return map[string]any{"Tag": 5, "SkyName": "DocIndent", "V0": v0}
}

func Formatter_Doc_DocGroup(v0 any) any {
	return map[string]any{"Tag": 6, "SkyName": "DocGroup", "V0": v0}
}

func Formatter_Doc_DocAlign(v0 any) any {
	return map[string]any{"Tag": 7, "SkyName": "DocAlign", "V0": v0}
}

func Formatter_Doc_Text(s any) any {
	return DocText(s)
}

func Formatter_Doc_Line() any {
	return map[string]any{"Tag": 0, "SkyName": "DocLine"}
}

func Formatter_Doc_Hardline() any {
	return map[string]any{"Tag": 7, "SkyName": "DocHardline"}
}

func Formatter_Doc_Softline() any {
	return map[string]any{"Tag": 1, "SkyName": "DocSoftline"}
}

func Formatter_Doc_Concat(parts any) any {
	return DocConcat(parts)
}

func Formatter_Doc_Indent(doc any) any {
	return DocIndent(doc)
}

func Formatter_Doc_Group(doc any) any {
	return DocGroup(doc)
}

func Formatter_Doc_Align(doc any) any {
	return DocAlign(doc)
}

func Formatter_Doc_JoinDocs(docs any, sep any) any {
	return func() any { return func() any { __subject := docs; if len(sky_asList(__subject)) == 0 { return DocConcat([]any{}) };  if len(sky_asList(__subject)) > 0 { first := sky_asList(__subject)[0]; _ = first; rest := sky_asList(__subject)[1:]; _ = rest; return DocConcat(append([]any{first}, sky_asList(sky_call(sky_listConcatMap(func(d any) any { return []any{sep, d} }), rest))...)) };  return nil }() }()
}

func Formatter_Doc_MaxWidth() any {
	return 80
}

func Formatter_Doc_IndentWidth() any {
	return 4
}

func Formatter_Doc_Render(doc any) any {
	return func() any { outputRef := sky_refNew(""); _ = outputRef; colRef := sky_refNew(0); _ = colRef; indentRef := sky_refNew(0); _ = indentRef; return func() any { Formatter_Doc_Walk(doc, outputRef, colRef, indentRef); return sky_refGet(outputRef) }() }()
}

func Formatter_Doc_WriteStr(s any, outputRef any, colRef any) any {
	return func() any { sky_call(sky_refSet(sky_concat(sky_refGet(outputRef), s)), outputRef); sky_call(sky_refSet(sky_asInt(sky_refGet(colRef)) + sky_asInt(sky_stringLength(s))), colRef); return struct{}{} }()
}

func Formatter_Doc_Newline(outputRef any, colRef any, indentRef any) any {
	return func() any { indentLevel := sky_refGet(indentRef); _ = indentLevel; spaces := Formatter_Doc_MakeSpaces(indentLevel); _ = spaces; sky_call(sky_refSet(sky_concat(sky_refGet(outputRef), sky_concat("\n", spaces))), outputRef); sky_call(sky_refSet(indentLevel), colRef); return struct{}{} }()
}

func Formatter_Doc_MakeSpaces(n any) any {
	return func() any { if sky_asBool(sky_asInt(n) <= sky_asInt(0)) { return "" }; return sky_concat(" ", Formatter_Doc_MakeSpaces(sky_asInt(n) - sky_asInt(1))) }()
}

func Formatter_Doc_FlatWidth(doc any) any {
	return func() any { return func() any { __subject := doc; if sky_asMap(__subject)["SkyName"] == "DocText" { s := sky_asMap(__subject)["V0"]; _ = s; return sky_stringLength(s) };  if sky_asMap(__subject)["SkyName"] == "DocLine" { return 1 };  if sky_asMap(__subject)["SkyName"] == "DocSoftline" { return 0 };  if sky_asMap(__subject)["SkyName"] == "DocHardline" { return 9999 };  if sky_asMap(__subject)["SkyName"] == "DocConcat" { parts := sky_asMap(__subject)["V0"]; _ = parts; return sky_call2(sky_listFoldl(func(part any) any { return func(acc any) any { return sky_asInt(acc) + sky_asInt(Formatter_Doc_FlatWidth(part)) } }), 0, parts) };  if sky_asMap(__subject)["SkyName"] == "DocIndent" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Formatter_Doc_FlatWidth(inner) };  if sky_asMap(__subject)["SkyName"] == "DocGroup" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Formatter_Doc_FlatWidth(inner) };  if sky_asMap(__subject)["SkyName"] == "DocAlign" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Formatter_Doc_FlatWidth(inner) };  return nil }() }()
}

func Formatter_Doc_Fits(doc any, remaining any) any {
	return func() any { if sky_asBool(sky_asInt(remaining) < sky_asInt(0)) { return false }; return func() any { return func() any { __subject := doc; if sky_asMap(__subject)["SkyName"] == "DocText" { s := sky_asMap(__subject)["V0"]; _ = s; return sky_asInt(sky_stringLength(s)) <= sky_asInt(remaining) };  if sky_asMap(__subject)["SkyName"] == "DocLine" { return true };  if sky_asMap(__subject)["SkyName"] == "DocSoftline" { return true };  if sky_asMap(__subject)["SkyName"] == "DocHardline" { return false };  if sky_asMap(__subject)["SkyName"] == "DocConcat" { parts := sky_asMap(__subject)["V0"]; _ = parts; return Formatter_Doc_FitsConcat(parts, remaining) };  if sky_asMap(__subject)["SkyName"] == "DocIndent" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Formatter_Doc_Fits(inner, remaining) };  if sky_asMap(__subject)["SkyName"] == "DocGroup" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Formatter_Doc_Fits(inner, remaining) };  if sky_asMap(__subject)["SkyName"] == "DocAlign" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Formatter_Doc_Fits(inner, remaining) };  return nil }() }() }()
}

func Formatter_Doc_FitsConcat(parts any, remaining any) any {
	return func() any { return func() any { __subject := parts; if len(sky_asList(__subject)) == 0 { return true };  if len(sky_asList(__subject)) > 0 { part := sky_asList(__subject)[0]; _ = part; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { w := Formatter_Doc_FlatWidth(part); _ = w; return func() any { if sky_asBool(sky_asInt(w) > sky_asInt(remaining)) { return false }; return Formatter_Doc_FitsConcat(rest, sky_asInt(remaining) - sky_asInt(w)) }() }() };  return nil }() }()
}

func Formatter_Doc_Walk(doc any, outputRef any, colRef any, indentRef any) any {
	return func() any { return func() any { __subject := doc; if sky_asMap(__subject)["SkyName"] == "DocText" { s := sky_asMap(__subject)["V0"]; _ = s; return Formatter_Doc_WriteStr(s, outputRef, colRef) };  if sky_asMap(__subject)["SkyName"] == "DocLine" { return Formatter_Doc_Newline(outputRef, colRef, indentRef) };  if sky_asMap(__subject)["SkyName"] == "DocSoftline" { return Formatter_Doc_Newline(outputRef, colRef, indentRef) };  if sky_asMap(__subject)["SkyName"] == "DocHardline" { return Formatter_Doc_Newline(outputRef, colRef, indentRef) };  if sky_asMap(__subject)["SkyName"] == "DocConcat" { parts := sky_asMap(__subject)["V0"]; _ = parts; return Formatter_Doc_WalkParts(parts, outputRef, colRef, indentRef) };  if sky_asMap(__subject)["SkyName"] == "DocIndent" { inner := sky_asMap(__subject)["V0"]; _ = inner; return func() any { oldIndent := sky_refGet(indentRef); _ = oldIndent; sky_call(sky_refSet(sky_asInt(oldIndent) + sky_asInt(Formatter_Doc_IndentWidth())), indentRef); Formatter_Doc_Walk(inner, outputRef, colRef, indentRef); sky_call(sky_refSet(oldIndent), indentRef); return struct{}{} }() };  if sky_asMap(__subject)["SkyName"] == "DocGroup" { inner := sky_asMap(__subject)["V0"]; _ = inner; return func() any { remaining := sky_asInt(Formatter_Doc_MaxWidth()) - sky_asInt(sky_refGet(colRef)); _ = remaining; return func() any { if sky_asBool(Formatter_Doc_Fits(inner, remaining)) { return Formatter_Doc_Flatten(inner, outputRef, colRef, indentRef) }; return Formatter_Doc_Walk(inner, outputRef, colRef, indentRef) }() }() };  if sky_asMap(__subject)["SkyName"] == "DocAlign" { inner := sky_asMap(__subject)["V0"]; _ = inner; return func() any { oldIndent := sky_refGet(indentRef); _ = oldIndent; sky_call(sky_refSet(sky_refGet(colRef)), indentRef); Formatter_Doc_Walk(inner, outputRef, colRef, indentRef); sky_call(sky_refSet(oldIndent), indentRef); return struct{}{} }() };  return nil }() }()
}

func Formatter_Doc_WalkParts(parts any, outputRef any, colRef any, indentRef any) any {
	return func() any { return func() any { __subject := parts; if len(sky_asList(__subject)) == 0 { return struct{}{} };  if len(sky_asList(__subject)) > 0 { part := sky_asList(__subject)[0]; _ = part; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { Formatter_Doc_Walk(part, outputRef, colRef, indentRef); return Formatter_Doc_WalkParts(rest, outputRef, colRef, indentRef) }() };  return nil }() }()
}

func Formatter_Doc_Flatten(doc any, outputRef any, colRef any, indentRef any) any {
	return func() any { return func() any { __subject := doc; if sky_asMap(__subject)["SkyName"] == "DocText" { s := sky_asMap(__subject)["V0"]; _ = s; return Formatter_Doc_WriteStr(s, outputRef, colRef) };  if sky_asMap(__subject)["SkyName"] == "DocLine" { return Formatter_Doc_WriteStr(" ", outputRef, colRef) };  if sky_asMap(__subject)["SkyName"] == "DocSoftline" { return struct{}{} };  if sky_asMap(__subject)["SkyName"] == "DocHardline" { return Formatter_Doc_Newline(outputRef, colRef, indentRef) };  if sky_asMap(__subject)["SkyName"] == "DocConcat" { parts := sky_asMap(__subject)["V0"]; _ = parts; return Formatter_Doc_FlattenParts(parts, outputRef, colRef, indentRef) };  if sky_asMap(__subject)["SkyName"] == "DocIndent" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Formatter_Doc_Flatten(inner, outputRef, colRef, indentRef) };  if sky_asMap(__subject)["SkyName"] == "DocGroup" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Formatter_Doc_Flatten(inner, outputRef, colRef, indentRef) };  if sky_asMap(__subject)["SkyName"] == "DocAlign" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Formatter_Doc_Flatten(inner, outputRef, colRef, indentRef) };  return nil }() }()
}

func Formatter_Doc_FlattenParts(parts any, outputRef any, colRef any, indentRef any) any {
	return func() any { return func() any { __subject := parts; if len(sky_asList(__subject)) == 0 { return struct{}{} };  if len(sky_asList(__subject)) > 0 { part := sky_asList(__subject)[0]; _ = part; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { Formatter_Doc_Flatten(part, outputRef, colRef, indentRef); return Formatter_Doc_FlattenParts(rest, outputRef, colRef, indentRef) }() };  return nil }() }()
}

var EmptyState = Lsp_Server_EmptyState

var StartServer = Lsp_Server_StartServer

var ServerLoop = Lsp_Server_ServerLoop

var HandleMessage = Lsp_Server_HandleMessage

var SendAndReturn = Lsp_Server_SendAndReturn

var SendNotifyAndReturn = Lsp_Server_SendNotifyAndReturn

var HandleInitialize = Lsp_Server_HandleInitialize

var HandleDidOpen = Lsp_Server_HandleDidOpen

var HandleDidChange = Lsp_Server_HandleDidChange

var AnalyzeAndPublishDiagnostics = Lsp_Server_AnalyzeAndPublishDiagnostics

var PublishAndUpdateState = Lsp_Server_PublishAndUpdateState

var MakeDiagnostic = Lsp_Server_MakeDiagnostic

var MakeRange = Lsp_Server_MakeRange

var HandleHover = Lsp_Server_HandleHover

var GetHoverForPosition = Lsp_Server_GetHoverForPosition

var GetLineAt = Lsp_Server_GetLineAt

var GetLineAtLoop = Lsp_Server_GetLineAtLoop

var FindLineStart = Lsp_Server_FindLineStart

var FindLineStartLoop = Lsp_Server_FindLineStartLoop

var GetWordAt = Lsp_Server_GetWordAt

var FindWordStart = Lsp_Server_FindWordStart

var FindWordEnd = Lsp_Server_FindWordEnd

var IsIdentChar = Lsp_Server_IsIdentChar

var FindAnnotationInSource = Lsp_Server_FindAnnotationInSource

var FindAnnotationLoop = Lsp_Server_FindAnnotationLoop

var ExtractUntilNewline = Lsp_Server_ExtractUntilNewline

var HandleCompletion = Lsp_Server_HandleCompletion

var MakeCompletionItem = Lsp_Server_MakeCompletionItem

var HandleDefinition = Lsp_Server_HandleDefinition

var FindDefinitionForPosition = Lsp_Server_FindDefinitionForPosition

var FindDefinitionInSource = Lsp_Server_FindDefinitionInSource

var FindDefLineInSource = Lsp_Server_FindDefLineInSource

var HandleFormatting = Lsp_Server_HandleFormatting

var readMessage = Lsp_JsonRpc_ReadMessage

var ReadMessage = Lsp_JsonRpc_ReadMessage

var readMessageBody = Lsp_JsonRpc_ReadMessageBody

var ReadMessageBody = Lsp_JsonRpc_ReadMessageBody

var parseContentLength = Lsp_JsonRpc_ParseContentLength

var ParseContentLength = Lsp_JsonRpc_ParseContentLength

var writeMessage = Lsp_JsonRpc_WriteMessage

var WriteMessage = Lsp_JsonRpc_WriteMessage

var makeResponse = Lsp_JsonRpc_MakeResponse

var MakeResponse = Lsp_JsonRpc_MakeResponse

var makeNotification = Lsp_JsonRpc_MakeNotification

var MakeNotification = Lsp_JsonRpc_MakeNotification

var jsonString = Lsp_JsonRpc_JsonString

var JsonString = Lsp_JsonRpc_JsonString

var jsonInt = Lsp_JsonRpc_JsonInt

var JsonInt = Lsp_JsonRpc_JsonInt

var jsonBool = Lsp_JsonRpc_JsonBool

var JsonBool = Lsp_JsonRpc_JsonBool

var jsonNull = Lsp_JsonRpc_JsonNull()

var JsonNull = Lsp_JsonRpc_JsonNull()

var jsonObject = Lsp_JsonRpc_JsonObject

var JsonObject = Lsp_JsonRpc_JsonObject

var formatField = Lsp_JsonRpc_FormatField

var jsonArray = Lsp_JsonRpc_JsonArray

var JsonArray = Lsp_JsonRpc_JsonArray

var escapeJson = Lsp_JsonRpc_EscapeJson

var EscapeJson = Lsp_JsonRpc_EscapeJson

var jsonGetString = Lsp_JsonRpc_JsonGetString

var JsonGetString = Lsp_JsonRpc_JsonGetString

var jsonGetRaw = Lsp_JsonRpc_JsonGetRaw

var JsonGetRaw = Lsp_JsonRpc_JsonGetRaw

var jsonGetInt = Lsp_JsonRpc_JsonGetInt

var JsonGetInt = Lsp_JsonRpc_JsonGetInt

var jsonGetObject = Lsp_JsonRpc_JsonGetObject

var JsonGetObject = Lsp_JsonRpc_JsonGetObject

var jsonGetArrayRaw = Lsp_JsonRpc_JsonGetArrayRaw

var JsonGetArrayRaw = Lsp_JsonRpc_JsonGetArrayRaw

var extractBracketed = Lsp_JsonRpc_ExtractBracketed

var ExtractBracketed = Lsp_JsonRpc_ExtractBracketed

var skipQuotedString = Lsp_JsonRpc_SkipQuotedString

var SkipQuotedString = Lsp_JsonRpc_SkipQuotedString

var jsonSplitArray = Lsp_JsonRpc_JsonSplitArray

var JsonSplitArray = Lsp_JsonRpc_JsonSplitArray

var splitJsonElements = Lsp_JsonRpc_SplitJsonElements

var SplitJsonElements = Lsp_JsonRpc_SplitJsonElements

var jsonGetBool = Lsp_JsonRpc_JsonGetBool

var JsonGetBool = Lsp_JsonRpc_JsonGetBool

var findInString = Lsp_JsonRpc_FindInString

var FindInString = Lsp_JsonRpc_FindInString

var extractQuotedString = Lsp_JsonRpc_ExtractQuotedString

var ExtractQuotedString = Lsp_JsonRpc_ExtractQuotedString

var takeWhileDigit = Lsp_JsonRpc_TakeWhileDigit

var TakeWhileDigit = Lsp_JsonRpc_TakeWhileDigit

var takeUntilDelimiter = Lsp_JsonRpc_TakeUntilDelimiter

var TakeUntilDelimiter = Lsp_JsonRpc_TakeUntilDelimiter

var extractBraced = Lsp_JsonRpc_ExtractBraced

var ExtractBraced = Lsp_JsonRpc_ExtractBraced

func Lsp_Server_EmptyState() any {
	return map[string]any{"documents": sky_dictEmpty(), "astCache": sky_dictEmpty(), "typeCache": sky_dictEmpty()}
}

func Lsp_Server_StartServer(_ any) any {
	return func() any { stateRef := sky_refNew(Lsp_Server_EmptyState()); _ = stateRef; return Lsp_Server_ServerLoop(stateRef) }()
}

func Lsp_Server_ServerLoop(stateRef any) any {
	return func() any { return func() any { __subject := readMessage(struct{}{}); if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return struct{}{} };  if sky_asSkyMaybe(__subject).SkyName == "Just" { body := sky_asSkyMaybe(__subject).JustValue; _ = body; return func() any { state := sky_refGet(stateRef); _ = state; method := jsonGetString("method", body); _ = method; id := jsonGetRaw("id", body); _ = id; newState := Lsp_Server_HandleMessage(state, id, method, body); _ = newState; sky_call(sky_refSet(newState), stateRef); return Lsp_Server_ServerLoop(stateRef) }() };  return nil }() }()
}

func Lsp_Server_HandleMessage(state any, id any, method any, body any) any {
	return func() any { if sky_asBool(sky_equal(method, "initialize")) { return Lsp_Server_HandleInitialize(state, id) }; return func() any { if sky_asBool(sky_equal(method, "initialized")) { return state }; return func() any { if sky_asBool(sky_equal(method, "shutdown")) { return Lsp_Server_SendAndReturn(makeResponse(id, jsonNull), state) }; return func() any { if sky_asBool(sky_equal(method, "textDocument/didOpen")) { return Lsp_Server_HandleDidOpen(state, body) }; return func() any { if sky_asBool(sky_equal(method, "textDocument/didChange")) { return Lsp_Server_HandleDidChange(state, body) }; return func() any { if sky_asBool(sky_equal(method, "textDocument/hover")) { return Lsp_Server_HandleHover(state, id, body) }; return func() any { if sky_asBool(sky_equal(method, "textDocument/completion")) { return Lsp_Server_HandleCompletion(state, id, body) }; return func() any { if sky_asBool(sky_equal(method, "textDocument/definition")) { return Lsp_Server_HandleDefinition(state, id, body) }; return func() any { if sky_asBool(sky_equal(method, "textDocument/formatting")) { return Lsp_Server_HandleFormatting(state, id, body) }; return state }() }() }() }() }() }() }() }() }()
}

func Lsp_Server_SendAndReturn(msg any, state any) any {
	return func() any { writeMessage(msg); return state }()
}

func Lsp_Server_SendNotifyAndReturn(msg any, state any) any {
	return func() any { writeMessage(msg); return state }()
}

func Lsp_Server_HandleInitialize(state any, id any) any {
	return func() any { capabilities := jsonObject([]any{SkyTuple2{V0: "textDocumentSync", V1: jsonInt(1)}, SkyTuple2{V0: "hoverProvider", V1: jsonBool(true)}, SkyTuple2{V0: "completionProvider", V1: jsonObject([]any{SkyTuple2{V0: "triggerCharacters", V1: jsonArray([]any{jsonString(".")})}})}, SkyTuple2{V0: "definitionProvider", V1: jsonBool(true)}, SkyTuple2{V0: "documentFormattingProvider", V1: jsonBool(true)}}); _ = capabilities; result := jsonObject([]any{SkyTuple2{V0: "capabilities", V1: capabilities}, SkyTuple2{V0: "serverInfo", V1: jsonObject([]any{SkyTuple2{V0: "name", V1: jsonString("sky-lsp")}, SkyTuple2{V0: "version", V1: jsonString("0.1.0")}})}}); _ = result; return Lsp_Server_SendAndReturn(makeResponse(id, result), state) }()
}

func Lsp_Server_HandleDidOpen(state any, body any) any {
	return func() any { params := jsonGetObject("params", body); _ = params; textDoc := jsonGetObject("textDocument", params); _ = textDoc; uri := jsonGetString("uri", textDoc); _ = uri; text := jsonGetString("text", textDoc); _ = text; newDocs := sky_call2(sky_dictInsert(uri), text, sky_asMap(state)["documents"]); _ = newDocs; newState := sky_recordUpdate(state, map[string]any{"documents": newDocs}); _ = newState; analyzed := Lsp_Server_AnalyzeAndPublishDiagnostics(newState, uri, text); _ = analyzed; return analyzed }()
}

func Lsp_Server_HandleDidChange(state any, body any) any {
	return func() any { params := jsonGetObject("params", body); _ = params; textDoc := jsonGetObject("textDocument", params); _ = textDoc; uri := jsonGetString("uri", textDoc); _ = uri; changes := jsonGetString("text", jsonGetObject("contentChanges", params)); _ = changes; text := func() any { if sky_asBool(sky_stringIsEmpty(changes)) { return func() any { return func() any { __subject := sky_call(sky_dictGet(uri), sky_asMap(state)["documents"]); if sky_asSkyMaybe(__subject).SkyName == "Just" { existing := sky_asSkyMaybe(__subject).JustValue; _ = existing; return existing };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "" };  return nil }() }() }; return changes }(); _ = text; newDocs := sky_call2(sky_dictInsert(uri), text, sky_asMap(state)["documents"]); _ = newDocs; newState := sky_recordUpdate(state, map[string]any{"documents": newDocs}); _ = newState; analyzed := Lsp_Server_AnalyzeAndPublishDiagnostics(newState, uri, text); _ = analyzed; return analyzed }()
}

func Lsp_Server_AnalyzeAndPublishDiagnostics(state any, uri any, text any) any {
	return func() any { lexResult := Compiler_Lexer_Lex(text); _ = lexResult; parseResult := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); _ = parseResult; return func() any { return func() any { __subject := parseResult; if sky_asSkyResult(__subject).SkyName == "Err" { parseErr := sky_asSkyResult(__subject).ErrValue; _ = parseErr; return func() any { diag := jsonObject([]any{SkyTuple2{V0: "range", V1: Lsp_Server_MakeRange(0, 0, 0, 1)}, SkyTuple2{V0: "severity", V1: jsonInt(1)}, SkyTuple2{V0: "message", V1: jsonString(parseErr)}}); _ = diag; return Lsp_Server_SendNotifyAndReturn(makeNotification("textDocument/publishDiagnostics", jsonObject([]any{SkyTuple2{V0: "uri", V1: jsonString(uri)}, SkyTuple2{V0: "diagnostics", V1: jsonArray([]any{diag})}})), state) }() };  if sky_asSkyResult(__subject).SkyName == "Ok" { mod := sky_asSkyResult(__subject).OkValue; _ = mod; return func() any { stdlibEnv := Compiler_Resolver_BuildStdlibEnv(); _ = stdlibEnv; checkResult := Compiler_Checker_CheckModule(mod, SkyJust(stdlibEnv)); _ = checkResult; diagnostics := func() any { return func() any { __subject := checkResult; if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return sky_call(sky_listMap(func(msg any) any { return Lsp_Server_MakeDiagnostic(2, msg) }), sky_asMap(result)["diagnostics"]) };  if sky_asSkyResult(__subject).SkyName == "Err" { err := sky_asSkyResult(__subject).ErrValue; _ = err; return []any{Lsp_Server_MakeDiagnostic(1, err)} };  return nil }() }(); _ = diagnostics; return Lsp_Server_PublishAndUpdateState(state, uri, mod, checkResult, diagnostics) }() };  return nil }() }() }()
}

func Lsp_Server_PublishAndUpdateState(state any, uri any, mod any, checkResult any, diagnostics any) any {
	return func() any { writeMessage(makeNotification("textDocument/publishDiagnostics", jsonObject([]any{SkyTuple2{V0: "uri", V1: jsonString(uri)}, SkyTuple2{V0: "diagnostics", V1: jsonArray(diagnostics)}}))); newAstCache := sky_call2(sky_dictInsert(uri), mod, sky_asMap(state)["astCache"]); _ = newAstCache; newTypeCache := func() any { return func() any { __subject := checkResult; if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return sky_call2(sky_dictInsert(uri), result, sky_asMap(state)["typeCache"]) };  if sky_asSkyResult(__subject).SkyName == "Err" { return sky_asMap(state)["typeCache"] };  return nil }() }(); _ = newTypeCache; return sky_recordUpdate(state, map[string]any{"astCache": newAstCache, "typeCache": newTypeCache}) }()
}

func Lsp_Server_MakeDiagnostic(severity any, msg any) any {
	return jsonObject([]any{SkyTuple2{V0: "range", V1: Lsp_Server_MakeRange(0, 0, 0, 1)}, SkyTuple2{V0: "severity", V1: jsonInt(severity)}, SkyTuple2{V0: "message", V1: jsonString(msg)}})
}

func Lsp_Server_MakeRange(startLine any, startChar any, endLine any, endChar any) any {
	return jsonObject([]any{SkyTuple2{V0: "start", V1: jsonObject([]any{SkyTuple2{V0: "line", V1: jsonInt(startLine)}, SkyTuple2{V0: "character", V1: jsonInt(startChar)}})}, SkyTuple2{V0: "end", V1: jsonObject([]any{SkyTuple2{V0: "line", V1: jsonInt(endLine)}, SkyTuple2{V0: "character", V1: jsonInt(endChar)}})}})
}

func Lsp_Server_HandleHover(state any, id any, body any) any {
	return func() any { params := jsonGetObject("params", body); _ = params; textDoc := jsonGetObject("textDocument", params); _ = textDoc; uri := jsonGetString("uri", textDoc); _ = uri; position := jsonGetObject("position", params); _ = position; line := jsonGetInt("line", position); _ = line; character := jsonGetInt("character", position); _ = character; hoverResult := Lsp_Server_GetHoverForPosition(state, uri, line, character); _ = hoverResult; return Lsp_Server_SendAndReturn(makeResponse(id, hoverResult), state) }()
}

func Lsp_Server_GetHoverForPosition(state any, uri any, line any, character any) any {
	return func() any { return func() any { __subject := sky_call(sky_dictGet(uri), sky_asMap(state)["documents"]); if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return jsonNull };  if sky_asSkyMaybe(__subject).SkyName == "Just" { source := sky_asSkyMaybe(__subject).JustValue; _ = source; return func() any { lineText := Lsp_Server_GetLineAt(line, source); _ = lineText; word := Lsp_Server_GetWordAt(character, lineText); _ = word; info := func() any { if sky_asBool(sky_stringIsEmpty(word)) { return "" }; return Lsp_Server_FindAnnotationInSource(word, source) }(); _ = info; return func() any { if sky_asBool(sky_stringIsEmpty(info)) { return jsonNull }; return jsonObject([]any{SkyTuple2{V0: "contents", V1: jsonObject([]any{SkyTuple2{V0: "kind", V1: jsonString("markdown")}, SkyTuple2{V0: "value", V1: jsonString(info)}})}}) }() }() };  return nil }() }()
}

func Lsp_Server_GetLineAt(lineNum any, source any) any {
	return Lsp_Server_GetLineAtLoop(lineNum, source, 0, 0)
}

func Lsp_Server_GetLineAtLoop(target any, source any, currentLine any, idx any) any {
	return func() any { if sky_asBool(sky_asInt(idx) >= sky_asInt(sky_stringLength(source))) { return "" }; return func() any { ch := sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1), source); _ = ch; return func() any { if sky_asBool(sky_equal(ch, "\n")) { return func() any { if sky_asBool(sky_equal(currentLine, target)) { return sky_call2(sky_stringSlice(Lsp_Server_FindLineStart(currentLine, source, idx)), idx, source) }; return Lsp_Server_GetLineAtLoop(target, source, sky_asInt(currentLine) + sky_asInt(1), sky_asInt(idx) + sky_asInt(1)) }() }; return func() any { if sky_asBool(sky_equal(idx, sky_asInt(sky_stringLength(source)) - sky_asInt(1))) { return func() any { if sky_asBool(sky_equal(currentLine, target)) { return sky_call2(sky_stringSlice(Lsp_Server_FindLineStart(currentLine, source, idx)), sky_asInt(idx) + sky_asInt(1), source) }; return "" }() }; return Lsp_Server_GetLineAtLoop(target, source, currentLine, sky_asInt(idx) + sky_asInt(1)) }() }() }() }()
}

func Lsp_Server_FindLineStart(lineNum any, source any, endIdx any) any {
	return Lsp_Server_FindLineStartLoop(source, sky_asInt(endIdx) - sky_asInt(1))
}

func Lsp_Server_FindLineStartLoop(source any, idx any) any {
	return func() any { if sky_asBool(sky_asInt(idx) < sky_asInt(0)) { return 0 }; return func() any { ch := sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1), source); _ = ch; return func() any { if sky_asBool(sky_equal(ch, "\n")) { return sky_asInt(idx) + sky_asInt(1) }; return Lsp_Server_FindLineStartLoop(source, sky_asInt(idx) - sky_asInt(1)) }() }() }()
}

func Lsp_Server_GetWordAt(col any, lineText any) any {
	return func() any { if sky_asBool(sky_asInt(col) >= sky_asInt(sky_stringLength(lineText))) { return "" }; return func() any { wordStart := Lsp_Server_FindWordStart(lineText, col); _ = wordStart; wordEnd := Lsp_Server_FindWordEnd(lineText, col); _ = wordEnd; return sky_call2(sky_stringSlice(wordStart), wordEnd, lineText) }() }()
}

func Lsp_Server_FindWordStart(text any, idx any) any {
	return func() any { if sky_asBool(sky_asInt(idx) <= sky_asInt(0)) { return 0 }; return func() any { ch := sky_call2(sky_stringSlice(sky_asInt(idx) - sky_asInt(1)), idx, text); _ = ch; return func() any { if sky_asBool(Lsp_Server_IsIdentChar(ch)) { return Lsp_Server_FindWordStart(text, sky_asInt(idx) - sky_asInt(1)) }; return idx }() }() }()
}

func Lsp_Server_FindWordEnd(text any, idx any) any {
	return func() any { if sky_asBool(sky_asInt(idx) >= sky_asInt(sky_stringLength(text))) { return idx }; return func() any { ch := sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1), text); _ = ch; return func() any { if sky_asBool(Lsp_Server_IsIdentChar(ch)) { return Lsp_Server_FindWordEnd(text, sky_asInt(idx) + sky_asInt(1)) }; return idx }() }() }()
}

func Lsp_Server_IsIdentChar(ch any) any {
	return sky_asBool(sky_equal(ch, "_")) || sky_asBool(sky_asBool(sky_equal(ch, "a")) || sky_asBool(sky_asBool(sky_equal(ch, "b")) || sky_asBool(sky_asBool(sky_equal(ch, "c")) || sky_asBool(sky_asBool(sky_equal(ch, "d")) || sky_asBool(sky_asBool(sky_equal(ch, "e")) || sky_asBool(sky_asBool(sky_equal(ch, "f")) || sky_asBool(sky_asBool(sky_equal(ch, "g")) || sky_asBool(sky_asBool(sky_equal(ch, "h")) || sky_asBool(sky_asBool(sky_equal(ch, "i")) || sky_asBool(sky_asBool(sky_equal(ch, "j")) || sky_asBool(sky_asBool(sky_equal(ch, "k")) || sky_asBool(sky_asBool(sky_equal(ch, "l")) || sky_asBool(sky_asBool(sky_equal(ch, "m")) || sky_asBool(sky_asBool(sky_equal(ch, "n")) || sky_asBool(sky_asBool(sky_equal(ch, "o")) || sky_asBool(sky_asBool(sky_equal(ch, "p")) || sky_asBool(sky_asBool(sky_equal(ch, "q")) || sky_asBool(sky_asBool(sky_equal(ch, "r")) || sky_asBool(sky_asBool(sky_equal(ch, "s")) || sky_asBool(sky_asBool(sky_equal(ch, "t")) || sky_asBool(sky_asBool(sky_equal(ch, "u")) || sky_asBool(sky_asBool(sky_equal(ch, "v")) || sky_asBool(sky_asBool(sky_equal(ch, "w")) || sky_asBool(sky_asBool(sky_equal(ch, "x")) || sky_asBool(sky_asBool(sky_equal(ch, "y")) || sky_asBool(sky_asBool(sky_equal(ch, "z")) || sky_asBool(sky_asBool(sky_equal(ch, "A")) || sky_asBool(sky_asBool(sky_equal(ch, "B")) || sky_asBool(sky_asBool(sky_equal(ch, "C")) || sky_asBool(sky_asBool(sky_equal(ch, "D")) || sky_asBool(sky_asBool(sky_equal(ch, "E")) || sky_asBool(sky_asBool(sky_equal(ch, "F")) || sky_asBool(sky_asBool(sky_equal(ch, "G")) || sky_asBool(sky_asBool(sky_equal(ch, "H")) || sky_asBool(sky_asBool(sky_equal(ch, "I")) || sky_asBool(sky_asBool(sky_equal(ch, "J")) || sky_asBool(sky_asBool(sky_equal(ch, "K")) || sky_asBool(sky_asBool(sky_equal(ch, "L")) || sky_asBool(sky_asBool(sky_equal(ch, "M")) || sky_asBool(sky_asBool(sky_equal(ch, "N")) || sky_asBool(sky_asBool(sky_equal(ch, "O")) || sky_asBool(sky_asBool(sky_equal(ch, "P")) || sky_asBool(sky_asBool(sky_equal(ch, "Q")) || sky_asBool(sky_asBool(sky_equal(ch, "R")) || sky_asBool(sky_asBool(sky_equal(ch, "S")) || sky_asBool(sky_asBool(sky_equal(ch, "T")) || sky_asBool(sky_asBool(sky_equal(ch, "U")) || sky_asBool(sky_asBool(sky_equal(ch, "V")) || sky_asBool(sky_asBool(sky_equal(ch, "W")) || sky_asBool(sky_asBool(sky_equal(ch, "X")) || sky_asBool(sky_asBool(sky_equal(ch, "Y")) || sky_asBool(sky_asBool(sky_equal(ch, "Z")) || sky_asBool(sky_asBool(sky_equal(ch, "0")) || sky_asBool(sky_asBool(sky_equal(ch, "1")) || sky_asBool(sky_asBool(sky_equal(ch, "2")) || sky_asBool(sky_asBool(sky_equal(ch, "3")) || sky_asBool(sky_asBool(sky_equal(ch, "4")) || sky_asBool(sky_asBool(sky_equal(ch, "5")) || sky_asBool(sky_asBool(sky_equal(ch, "6")) || sky_asBool(sky_asBool(sky_equal(ch, "7")) || sky_asBool(sky_asBool(sky_equal(ch, "8")) || sky_asBool(sky_equal(ch, "9")))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))
}

func Lsp_Server_FindAnnotationInSource(name any, source any) any {
	return Lsp_Server_FindAnnotationLoop(name, source, 0)
}

func Lsp_Server_FindAnnotationLoop(name any, source any, idx any) any {
	return func() any { searchFor := sky_concat(name, " : "); _ = searchFor; searchLen := sky_stringLength(searchFor); _ = searchLen; return func() any { if sky_asBool(sky_asInt(sky_asInt(idx) + sky_asInt(searchLen)) > sky_asInt(sky_stringLength(source))) { return "" }; return func() any { candidate := sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(searchLen), source); _ = candidate; return func() any { if sky_asBool(sky_equal(candidate, searchFor)) { return func() any { atStart := func() any { if sky_asBool(sky_equal(idx, 0)) { return true }; return sky_equal(sky_call2(sky_stringSlice(sky_asInt(idx) - sky_asInt(1)), idx, source), "\n") }(); _ = atStart; return func() any { if sky_asBool(atStart) { return func() any { restOfLine := Lsp_Server_ExtractUntilNewline(source, sky_asInt(idx) + sky_asInt(searchLen)); _ = restOfLine; return sky_concat(name, sky_concat(" : ", restOfLine)) }() }; return Lsp_Server_FindAnnotationLoop(name, source, sky_asInt(idx) + sky_asInt(1)) }() }() }; return Lsp_Server_FindAnnotationLoop(name, source, sky_asInt(idx) + sky_asInt(1)) }() }() }() }()
}

func Lsp_Server_ExtractUntilNewline(source any, idx any) any {
	return func() any { if sky_asBool(sky_asInt(idx) >= sky_asInt(sky_stringLength(source))) { return "" }; return func() any { ch := sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1), source); _ = ch; return func() any { if sky_asBool(sky_equal(ch, "\n")) { return "" }; return sky_concat(ch, Lsp_Server_ExtractUntilNewline(source, sky_asInt(idx) + sky_asInt(1))) }() }() }()
}

func Lsp_Server_HandleCompletion(state any, id any, body any) any {
	return func() any { keywords := []any{"module", "exposing", "import", "as", "type", "alias", "let", "in", "if", "then", "else", "case", "of", "foreign", "True", "False"}; _ = keywords; keywordItems := sky_call(sky_listMap(func(kw any) any { return jsonObject([]any{SkyTuple2{V0: "label", V1: jsonString(kw)}, SkyTuple2{V0: "kind", V1: jsonInt(14)}}) }), keywords); _ = keywordItems; stdlibItems := []any{Lsp_Server_MakeCompletionItem("println", "function", 3), Lsp_Server_MakeCompletionItem("identity", "function", 3), Lsp_Server_MakeCompletionItem("not", "function", 3), Lsp_Server_MakeCompletionItem("always", "function", 3), Lsp_Server_MakeCompletionItem("fst", "function", 3), Lsp_Server_MakeCompletionItem("snd", "function", 3), Lsp_Server_MakeCompletionItem("Ok", "constructor", 4), Lsp_Server_MakeCompletionItem("Err", "constructor", 4), Lsp_Server_MakeCompletionItem("Just", "constructor", 4), Lsp_Server_MakeCompletionItem("Nothing", "constructor", 4)}; _ = stdlibItems; allItems := sky_call(sky_listAppend(keywordItems), stdlibItems); _ = allItems; result := jsonObject([]any{SkyTuple2{V0: "isIncomplete", V1: jsonBool(false)}, SkyTuple2{V0: "items", V1: jsonArray(allItems)}}); _ = result; return Lsp_Server_SendAndReturn(makeResponse(id, result), state) }()
}

func Lsp_Server_MakeCompletionItem(label any, detail any, kind any) any {
	return jsonObject([]any{SkyTuple2{V0: "label", V1: jsonString(label)}, SkyTuple2{V0: "kind", V1: jsonInt(kind)}, SkyTuple2{V0: "detail", V1: jsonString(detail)}})
}

func Lsp_Server_HandleDefinition(state any, id any, body any) any {
	return func() any { params := jsonGetObject("params", body); _ = params; textDoc := jsonGetObject("textDocument", params); _ = textDoc; uri := jsonGetString("uri", textDoc); _ = uri; position := jsonGetObject("position", params); _ = position; line := jsonGetInt("line", position); _ = line; character := jsonGetInt("character", position); _ = character; defResult := Lsp_Server_FindDefinitionForPosition(state, uri, line, character); _ = defResult; return Lsp_Server_SendAndReturn(makeResponse(id, defResult), state) }()
}

func Lsp_Server_FindDefinitionForPosition(state any, uri any, line any, character any) any {
	return func() any { return func() any { __subject := sky_call(sky_dictGet(uri), sky_asMap(state)["documents"]); if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return jsonNull };  if sky_asSkyMaybe(__subject).SkyName == "Just" { source := sky_asSkyMaybe(__subject).JustValue; _ = source; return func() any { lineText := Lsp_Server_GetLineAt(line, source); _ = lineText; word := Lsp_Server_GetWordAt(character, lineText); _ = word; return func() any { if sky_asBool(sky_stringIsEmpty(word)) { return jsonNull }; return Lsp_Server_FindDefinitionInSource(word, source, uri) }() }() };  return nil }() }()
}

func Lsp_Server_FindDefinitionInSource(name any, source any, uri any) any {
	return func() any { defLine := Lsp_Server_FindDefLineInSource(name, source, 0, 0); _ = defLine; return func() any { if sky_asBool(sky_asInt(defLine) < sky_asInt(0)) { return jsonNull }; return jsonObject([]any{SkyTuple2{V0: "uri", V1: jsonString(uri)}, SkyTuple2{V0: "range", V1: Lsp_Server_MakeRange(defLine, 0, defLine, sky_stringLength(name))}}) }() }()
}

func Lsp_Server_FindDefLineInSource(name any, source any, lineNum any, idx any) any {
	return func() any { if sky_asBool(sky_asInt(idx) >= sky_asInt(sky_stringLength(source))) { return -1 }; return func() any { atStart := func() any { if sky_asBool(sky_equal(idx, 0)) { return true }; return sky_equal(sky_call2(sky_stringSlice(sky_asInt(idx) - sky_asInt(1)), idx, source), "\n") }(); _ = atStart; return func() any { if sky_asBool(atStart) { return func() any { nameLen := sky_stringLength(name); _ = nameLen; candidate := sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(nameLen), source); _ = candidate; return func() any { if sky_asBool(sky_equal(candidate, name)) { return func() any { afterName := sky_call2(sky_stringSlice(sky_asInt(idx) + sky_asInt(nameLen)), sky_asInt(sky_asInt(idx) + sky_asInt(nameLen)) + sky_asInt(1), source); _ = afterName; return func() any { if sky_asBool(sky_equal(afterName, " ")) { return func() any { afterSpace := sky_call2(sky_stringSlice(sky_asInt(sky_asInt(idx) + sky_asInt(nameLen)) + sky_asInt(1)), sky_asInt(sky_asInt(idx) + sky_asInt(nameLen)) + sky_asInt(2), source); _ = afterSpace; return func() any { if sky_asBool(sky_asBool(sky_equal(afterSpace, "=")) || sky_asBool(sky_equal(afterSpace, ":"))) { return func() any { if sky_asBool(sky_equal(afterSpace, ":")) { return Lsp_Server_FindDefLineInSource(name, source, lineNum, sky_asInt(idx) + sky_asInt(1)) }; return lineNum }() }; return lineNum }() }() }; return Lsp_Server_FindDefLineInSource(name, source, lineNum, sky_asInt(idx) + sky_asInt(1)) }() }() }; return func() any { ch := sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1), source); _ = ch; return func() any { if sky_asBool(sky_equal(ch, "\n")) { return Lsp_Server_FindDefLineInSource(name, source, sky_asInt(lineNum) + sky_asInt(1), sky_asInt(idx) + sky_asInt(1)) }; return Lsp_Server_FindDefLineInSource(name, source, lineNum, sky_asInt(idx) + sky_asInt(1)) }() }() }() }() }; return func() any { ch := sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1), source); _ = ch; return func() any { if sky_asBool(sky_equal(ch, "\n")) { return Lsp_Server_FindDefLineInSource(name, source, sky_asInt(lineNum) + sky_asInt(1), sky_asInt(idx) + sky_asInt(1)) }; return Lsp_Server_FindDefLineInSource(name, source, lineNum, sky_asInt(idx) + sky_asInt(1)) }() }() }() }() }()
}

func Lsp_Server_HandleFormatting(state any, id any, body any) any {
	return Lsp_Server_SendAndReturn(makeResponse(id, jsonArray([]any{})), state)
}

func Lsp_JsonRpc_ReadMessage(_ any) any {
	return func() any { return func() any { __subject := sky_readLine(struct{}{}); if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyNothing() };  if sky_asSkyMaybe(__subject).SkyName == "Just" { headerLine := sky_asSkyMaybe(__subject).JustValue; _ = headerLine; return func() any { contentLength := Lsp_JsonRpc_ParseContentLength(headerLine); _ = contentLength; return func() any { if sky_asBool(sky_asInt(contentLength) <= sky_asInt(0)) { return SkyNothing() }; return Lsp_JsonRpc_ReadMessageBody(contentLength) }() }() };  return nil }() }()
}

func Lsp_JsonRpc_ReadMessageBody(len_ any) any {
	return func() any { return func() any { __subject := sky_readLine(struct{}{}); if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyNothing() };  if sky_asSkyMaybe(__subject).SkyName == "Just" { return sky_readBytes(len_) };  return nil }() }()
}

func Lsp_JsonRpc_ParseContentLength(header any) any {
	return func() any { if sky_asBool(sky_call(sky_stringStartsWith("Content-Length: "), header)) { return func() any { raw := sky_call2(sky_stringSlice(16), sky_stringLength(header), header); _ = raw; numStr := sky_call2(sky_stringReplace("\r"), "", sky_stringTrim(raw)); _ = numStr; return func() any { return func() any { __subject := sky_stringToInt(numStr); if sky_asSkyMaybe(__subject).SkyName == "Just" { n := sky_asSkyMaybe(__subject).JustValue; _ = n; return n };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return 0 };  return nil }() }() }() }; return 0 }()
}

func Lsp_JsonRpc_WriteMessage(json any) any {
	return func() any { len_ := sky_stringLength(json); _ = len_; header := sky_concat("Content-Length: ", sky_concat(sky_stringFromInt(len_), "\r\n\r\n")); _ = header; return sky_writeStdout(sky_concat(header, json)) }()
}

func Lsp_JsonRpc_MakeResponse(id any, resultJson any) any {
	return sky_concat("{\"jsonrpc\":\"2.0\",\"id\":", sky_concat(id, sky_concat(",\"result\":", sky_concat(resultJson, "}"))))
}

func Lsp_JsonRpc_MakeNotification(method any, paramsJson any) any {
	return sky_concat("{\"jsonrpc\":\"2.0\",\"method\":\"", sky_concat(method, sky_concat("\",\"params\":", sky_concat(paramsJson, "}"))))
}

func Lsp_JsonRpc_JsonString(s any) any {
	return sky_concat("\"", sky_concat(Lsp_JsonRpc_EscapeJson(s), "\""))
}

func Lsp_JsonRpc_JsonInt(n any) any {
	return sky_stringFromInt(n)
}

func Lsp_JsonRpc_JsonBool(b any) any {
	return func() any { if sky_asBool(b) { return "true" }; return "false" }()
}

func Lsp_JsonRpc_JsonNull() any {
	return "null"
}

func Lsp_JsonRpc_JsonObject(fields any) any {
	return sky_concat("{", sky_concat(sky_call(sky_stringJoin(","), sky_call(sky_listMap(Lsp_JsonRpc_FormatField), fields)), "}"))
}

func Lsp_JsonRpc_FormatField(pair any) any {
	return sky_concat(Lsp_JsonRpc_JsonString(sky_fst(pair)), sky_concat(":", sky_snd(pair)))
}

func Lsp_JsonRpc_JsonArray(items any) any {
	return sky_concat("[", sky_concat(sky_call(sky_stringJoin(","), items), "]"))
}

func Lsp_JsonRpc_EscapeJson(s any) any {
	return func() any { s1 := sky_call2(sky_stringReplace("\\"), "\\\\", s); _ = s1; s2 := sky_call2(sky_stringReplace("\""), "\\\"", s1); _ = s2; s3 := sky_call2(sky_stringReplace("\n"), "\\n", s2); _ = s3; s4 := sky_call2(sky_stringReplace("\r"), "\\r", s3); _ = s4; return sky_call2(sky_stringReplace("\t"), "\\t", s4) }()
}

func Lsp_JsonRpc_JsonGetString(key any, json any) any {
	return func() any { searchKey := sky_concat("\"", sky_concat(key, "\"")); _ = searchKey; keyIdx := Lsp_JsonRpc_FindInString(searchKey, json, 0); _ = keyIdx; return func() any { if sky_asBool(sky_asInt(keyIdx) < sky_asInt(0)) { return "" }; return func() any { afterKey := sky_call2(sky_stringSlice(sky_asInt(keyIdx) + sky_asInt(sky_stringLength(searchKey))), sky_stringLength(json), json); _ = afterKey; colonIdx := Lsp_JsonRpc_FindInString(":", afterKey, 0); _ = colonIdx; return func() any { if sky_asBool(sky_asInt(colonIdx) < sky_asInt(0)) { return "" }; return func() any { afterColon := sky_stringTrim(sky_call2(sky_stringSlice(sky_asInt(colonIdx) + sky_asInt(1)), sky_stringLength(afterKey), afterKey)); _ = afterColon; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("\""), afterColon)) { return Lsp_JsonRpc_ExtractQuotedString(sky_call2(sky_stringSlice(1), sky_stringLength(afterColon), afterColon), "") }; return Lsp_JsonRpc_TakeUntilDelimiter(afterColon, "") }() }() }() }() }() }()
}

func Lsp_JsonRpc_JsonGetRaw(key any, json any) any {
	return func() any { searchKey := sky_concat("\"", sky_concat(key, "\"")); _ = searchKey; keyIdx := Lsp_JsonRpc_FindInString(searchKey, json, 0); _ = keyIdx; return func() any { if sky_asBool(sky_asInt(keyIdx) < sky_asInt(0)) { return "null" }; return func() any { afterKey := sky_call2(sky_stringSlice(sky_asInt(keyIdx) + sky_asInt(sky_stringLength(searchKey))), sky_stringLength(json), json); _ = afterKey; colonIdx := Lsp_JsonRpc_FindInString(":", afterKey, 0); _ = colonIdx; return func() any { if sky_asBool(sky_asInt(colonIdx) < sky_asInt(0)) { return "null" }; return func() any { afterColon := sky_stringTrim(sky_call2(sky_stringSlice(sky_asInt(colonIdx) + sky_asInt(1)), sky_stringLength(afterKey), afterKey)); _ = afterColon; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("\""), afterColon)) { return sky_concat("\"", sky_concat(Lsp_JsonRpc_ExtractQuotedString(sky_call2(sky_stringSlice(1), sky_stringLength(afterColon), afterColon), ""), "\"")) }; return Lsp_JsonRpc_TakeUntilDelimiter(afterColon, "") }() }() }() }() }() }()
}

func Lsp_JsonRpc_JsonGetInt(key any, json any) any {
	return func() any { searchKey := sky_concat("\"", sky_concat(key, "\"")); _ = searchKey; keyIdx := Lsp_JsonRpc_FindInString(searchKey, json, 0); _ = keyIdx; return func() any { if sky_asBool(sky_asInt(keyIdx) < sky_asInt(0)) { return 0 }; return func() any { afterKey := sky_call2(sky_stringSlice(sky_asInt(keyIdx) + sky_asInt(sky_stringLength(searchKey))), sky_stringLength(json), json); _ = afterKey; colonIdx := Lsp_JsonRpc_FindInString(":", afterKey, 0); _ = colonIdx; return func() any { if sky_asBool(sky_asInt(colonIdx) < sky_asInt(0)) { return 0 }; return func() any { afterColon := sky_stringTrim(sky_call2(sky_stringSlice(sky_asInt(colonIdx) + sky_asInt(1)), sky_stringLength(afterKey), afterKey)); _ = afterColon; numStr := Lsp_JsonRpc_TakeWhileDigit(afterColon, ""); _ = numStr; return func() any { return func() any { __subject := sky_stringToInt(numStr); if sky_asSkyMaybe(__subject).SkyName == "Just" { n := sky_asSkyMaybe(__subject).JustValue; _ = n; return n };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return 0 };  return nil }() }() }() }() }() }() }()
}

func Lsp_JsonRpc_JsonGetObject(key any, json any) any {
	return func() any { searchKey := sky_concat("\"", sky_concat(key, "\"")); _ = searchKey; keyIdx := Lsp_JsonRpc_FindInString(searchKey, json, 0); _ = keyIdx; return func() any { if sky_asBool(sky_asInt(keyIdx) < sky_asInt(0)) { return "{}" }; return func() any { afterKey := sky_call2(sky_stringSlice(sky_asInt(keyIdx) + sky_asInt(sky_stringLength(searchKey))), sky_stringLength(json), json); _ = afterKey; colonIdx := Lsp_JsonRpc_FindInString(":", afterKey, 0); _ = colonIdx; return func() any { if sky_asBool(sky_asInt(colonIdx) < sky_asInt(0)) { return "{}" }; return func() any { afterColon := sky_stringTrim(sky_call2(sky_stringSlice(sky_asInt(colonIdx) + sky_asInt(1)), sky_stringLength(afterKey), afterKey)); _ = afterColon; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("{"), afterColon)) { return Lsp_JsonRpc_ExtractBraced(afterColon, 0, 0) }; return "{}" }() }() }() }() }() }()
}

func Lsp_JsonRpc_JsonGetArrayRaw(key any, json any) any {
	return func() any { searchKey := sky_concat("\"", sky_concat(key, "\"")); _ = searchKey; keyIdx := Lsp_JsonRpc_FindInString(searchKey, json, 0); _ = keyIdx; return func() any { if sky_asBool(sky_asInt(keyIdx) < sky_asInt(0)) { return "[]" }; return func() any { afterKey := sky_call2(sky_stringSlice(sky_asInt(keyIdx) + sky_asInt(sky_stringLength(searchKey))), sky_stringLength(json), json); _ = afterKey; colonIdx := Lsp_JsonRpc_FindInString(":", afterKey, 0); _ = colonIdx; return func() any { if sky_asBool(sky_asInt(colonIdx) < sky_asInt(0)) { return "[]" }; return func() any { afterColon := sky_stringTrim(sky_call2(sky_stringSlice(sky_asInt(colonIdx) + sky_asInt(1)), sky_stringLength(afterKey), afterKey)); _ = afterColon; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("["), afterColon)) { return Lsp_JsonRpc_ExtractBracketed(afterColon, 0, 0) }; return "[]" }() }() }() }() }() }()
}

func Lsp_JsonRpc_ExtractBracketed(remaining any, depth any, idx any) any {
	return func() any { if sky_asBool(sky_asInt(idx) >= sky_asInt(sky_stringLength(remaining))) { return remaining }; return func() any { ch := sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1), remaining); _ = ch; return func() any { if sky_asBool(sky_equal(ch, "[")) { return Lsp_JsonRpc_ExtractBracketed(remaining, sky_asInt(depth) + sky_asInt(1), sky_asInt(idx) + sky_asInt(1)) }; return func() any { if sky_asBool(sky_equal(ch, "]")) { return func() any { if sky_asBool(sky_asInt(depth) <= sky_asInt(1)) { return sky_call2(sky_stringSlice(0), sky_asInt(idx) + sky_asInt(1), remaining) }; return Lsp_JsonRpc_ExtractBracketed(remaining, sky_asInt(depth) - sky_asInt(1), sky_asInt(idx) + sky_asInt(1)) }() }; return func() any { if sky_asBool(sky_equal(ch, "\"")) { return func() any { endQuote := Lsp_JsonRpc_SkipQuotedString(remaining, sky_asInt(idx) + sky_asInt(1)); _ = endQuote; return Lsp_JsonRpc_ExtractBracketed(remaining, depth, endQuote) }() }; return Lsp_JsonRpc_ExtractBracketed(remaining, depth, sky_asInt(idx) + sky_asInt(1)) }() }() }() }() }()
}

func Lsp_JsonRpc_SkipQuotedString(s any, idx any) any {
	return func() any { if sky_asBool(sky_asInt(idx) >= sky_asInt(sky_stringLength(s))) { return idx }; return func() any { ch := sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1), s); _ = ch; return func() any { if sky_asBool(sky_equal(ch, "\"")) { return sky_asInt(idx) + sky_asInt(1) }; return func() any { if sky_asBool(sky_equal(ch, "\\")) { return Lsp_JsonRpc_SkipQuotedString(s, sky_asInt(idx) + sky_asInt(2)) }; return Lsp_JsonRpc_SkipQuotedString(s, sky_asInt(idx) + sky_asInt(1)) }() }() }() }()
}

func Lsp_JsonRpc_JsonSplitArray(arrayStr any) any {
	return func() any { inner := sky_stringTrim(sky_call2(sky_stringSlice(1), sky_asInt(sky_stringLength(arrayStr)) - sky_asInt(1), arrayStr)); _ = inner; return func() any { if sky_asBool(sky_stringIsEmpty(inner)) { return []any{} }; return Lsp_JsonRpc_SplitJsonElements(inner, 0, 0, 0, []any{}) }() }()
}

func Lsp_JsonRpc_SplitJsonElements(s any, idx any, start any, depth any, acc any) any {
	return func() any { if sky_asBool(sky_asInt(idx) >= sky_asInt(sky_stringLength(s))) { return func() any { last := sky_stringTrim(sky_call2(sky_stringSlice(start), idx, s)); _ = last; return func() any { if sky_asBool(sky_stringIsEmpty(last)) { return acc }; return sky_call(sky_listAppend(acc), []any{last}) }() }() }; return func() any { ch := sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1), s); _ = ch; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, "{")) || sky_asBool(sky_equal(ch, "["))) { return Lsp_JsonRpc_SplitJsonElements(s, sky_asInt(idx) + sky_asInt(1), start, sky_asInt(depth) + sky_asInt(1), acc) }; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, "}")) || sky_asBool(sky_equal(ch, "]"))) { return Lsp_JsonRpc_SplitJsonElements(s, sky_asInt(idx) + sky_asInt(1), start, sky_asInt(depth) - sky_asInt(1), acc) }; return func() any { if sky_asBool(sky_equal(ch, "\"")) { return func() any { endQ := Lsp_JsonRpc_SkipQuotedString(s, sky_asInt(idx) + sky_asInt(1)); _ = endQ; return Lsp_JsonRpc_SplitJsonElements(s, endQ, start, depth, acc) }() }; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, ",")) && sky_asBool(sky_equal(depth, 0))) { return func() any { element := sky_stringTrim(sky_call2(sky_stringSlice(start), idx, s)); _ = element; return Lsp_JsonRpc_SplitJsonElements(s, sky_asInt(idx) + sky_asInt(1), sky_asInt(idx) + sky_asInt(1), depth, sky_call(sky_listAppend(acc), []any{element})) }() }; return Lsp_JsonRpc_SplitJsonElements(s, sky_asInt(idx) + sky_asInt(1), start, depth, acc) }() }() }() }() }() }()
}

func Lsp_JsonRpc_JsonGetBool(key any, json any) any {
	return func() any { val := Lsp_JsonRpc_JsonGetString(key, json); _ = val; return sky_equal(val, "true") }()
}

func Lsp_JsonRpc_FindInString(needle any, haystack any, idx any) any {
	return func() any { if sky_asBool(sky_asInt(sky_asInt(idx) + sky_asInt(sky_stringLength(needle))) > sky_asInt(sky_stringLength(haystack))) { return -1 }; return func() any { if sky_asBool(sky_equal(sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(sky_stringLength(needle)), haystack), needle)) { return idx }; return Lsp_JsonRpc_FindInString(needle, haystack, sky_asInt(idx) + sky_asInt(1)) }() }()
}

func Lsp_JsonRpc_ExtractQuotedString(remaining any, acc any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(remaining)) { return acc }; return func() any { ch := sky_call2(sky_stringSlice(0), 1, remaining); _ = ch; rest := sky_call2(sky_stringSlice(1), sky_stringLength(remaining), remaining); _ = rest; return func() any { if sky_asBool(sky_equal(ch, "\"")) { return acc }; return func() any { if sky_asBool(sky_equal(ch, "\\")) { return func() any { escaped := sky_call2(sky_stringSlice(0), 1, rest); _ = escaped; decoded := func() any { if sky_asBool(sky_equal(escaped, "n")) { return "\n" }; return func() any { if sky_asBool(sky_equal(escaped, "t")) { return "\t" }; return func() any { if sky_asBool(sky_equal(escaped, "r")) { return "\r" }; return escaped }() }() }(); _ = decoded; return Lsp_JsonRpc_ExtractQuotedString(sky_call2(sky_stringSlice(1), sky_stringLength(rest), rest), sky_concat(acc, decoded)) }() }; return Lsp_JsonRpc_ExtractQuotedString(rest, sky_concat(acc, ch)) }() }() }() }()
}

func Lsp_JsonRpc_TakeWhileDigit(remaining any, acc any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(remaining)) { return acc }; return func() any { ch := sky_call2(sky_stringSlice(0), 1, remaining); _ = ch; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, "0")) || sky_asBool(sky_asBool(sky_equal(ch, "1")) || sky_asBool(sky_asBool(sky_equal(ch, "2")) || sky_asBool(sky_asBool(sky_equal(ch, "3")) || sky_asBool(sky_asBool(sky_equal(ch, "4")) || sky_asBool(sky_asBool(sky_equal(ch, "5")) || sky_asBool(sky_asBool(sky_equal(ch, "6")) || sky_asBool(sky_asBool(sky_equal(ch, "7")) || sky_asBool(sky_asBool(sky_equal(ch, "8")) || sky_asBool(sky_equal(ch, "9"))))))))))) { return Lsp_JsonRpc_TakeWhileDigit(sky_call2(sky_stringSlice(1), sky_stringLength(remaining), remaining), sky_concat(acc, ch)) }; return acc }() }() }()
}

func Lsp_JsonRpc_TakeUntilDelimiter(remaining any, acc any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(remaining)) { return acc }; return func() any { ch := sky_call2(sky_stringSlice(0), 1, remaining); _ = ch; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, ",")) || sky_asBool(sky_asBool(sky_equal(ch, "}")) || sky_asBool(sky_asBool(sky_equal(ch, "]")) || sky_asBool(sky_asBool(sky_equal(ch, " ")) || sky_asBool(sky_asBool(sky_equal(ch, "\n")) || sky_asBool(sky_equal(ch, "\r"))))))) { return acc }; return Lsp_JsonRpc_TakeUntilDelimiter(sky_call2(sky_stringSlice(1), sky_stringLength(remaining), remaining), sky_concat(acc, ch)) }() }() }()
}

func Lsp_JsonRpc_ExtractBraced(remaining any, depth any, idx any) any {
	return func() any { if sky_asBool(sky_asInt(idx) >= sky_asInt(sky_stringLength(remaining))) { return remaining }; return func() any { ch := sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1), remaining); _ = ch; return func() any { if sky_asBool(sky_equal(ch, "{")) { return Lsp_JsonRpc_ExtractBraced(remaining, sky_asInt(depth) + sky_asInt(1), sky_asInt(idx) + sky_asInt(1)) }; return func() any { if sky_asBool(sky_equal(ch, "}")) { return func() any { if sky_asBool(sky_asInt(depth) <= sky_asInt(1)) { return sky_call2(sky_stringSlice(0), sky_asInt(idx) + sky_asInt(1), remaining) }; return Lsp_JsonRpc_ExtractBraced(remaining, sky_asInt(depth) - sky_asInt(1), sky_asInt(idx) + sky_asInt(1)) }() }; return Lsp_JsonRpc_ExtractBraced(remaining, depth, sky_asInt(idx) + sky_asInt(1)) }() }() }() }()
}

var GenerateBindings = Ffi_BindingGen_GenerateBindings

var GenerateSkyiFile = Ffi_BindingGen_GenerateSkyiFile

var PkgToModuleName = Ffi_BindingGen_PkgToModuleName

var ExtractFieldBindings = Ffi_BindingGen_ExtractFieldBindings

var ExtractFieldsForType = Ffi_BindingGen_ExtractFieldsForType

var IsGenericTypeBinding = Ffi_BindingGen_IsGenericTypeBinding

var GenerateFieldBinding = Ffi_BindingGen_GenerateFieldBinding

var IsSafeFieldType = Ffi_BindingGen_IsSafeFieldType

var ExtractVarBindings = Ffi_BindingGen_ExtractVarBindings

var GenerateVarBinding = Ffi_BindingGen_GenerateVarBinding

var ExtractMethodBindings = Ffi_BindingGen_ExtractMethodBindings

var ExtractMethodsForType = Ffi_BindingGen_ExtractMethodsForType

var GenerateMethodBinding = Ffi_BindingGen_GenerateMethodBinding

var HyphenToCamel = Ffi_BindingGen_HyphenToCamel

var ExtractFuncBindings = Ffi_BindingGen_ExtractFuncBindings

var IsSupportedFuncForPkg = Ffi_BindingGen_IsSupportedFuncForPkg

var AllMethodParamsSafe = Ffi_BindingGen_AllMethodParamsSafe

var AllMethodParamsLoop = Ffi_BindingGen_AllMethodParamsLoop

var IsSafeTypeForPkg = Ffi_BindingGen_IsSafeTypeForPkg

var IsSafeType = Ffi_BindingGen_IsSafeType

var NeedsExtraImportBinding = Ffi_BindingGen_NeedsExtraImportBinding

var GenerateFuncBinding = Ffi_BindingGen_GenerateFuncBinding

var BuildReturnType = Ffi_BindingGen_BuildReturnType

var ExtractTypeBindings = Ffi_BindingGen_ExtractTypeBindings

var GenerateTypeBinding = Ffi_BindingGen_GenerateTypeBinding

var inspectPackage = Ffi_Inspector_InspectPackage

var InspectPackage = Ffi_Inspector_InspectPackage

var runInspector = Ffi_Inspector_RunInspector

var RunInspector = Ffi_Inspector_RunInspector

var safePkgName = Ffi_Inspector_SafePkgName

var SafePkgName = Ffi_Inspector_SafePkgName

var inspectorGoCode = Ffi_Inspector_InspectorGoCode()

var InspectorGoCode = Ffi_Inspector_InspectorGoCode()

var mapGoTypeToSky = Ffi_TypeMapper_MapGoTypeToSky

var isGoPrimitive = Ffi_TypeMapper_IsGoPrimitive

var goTypeToAssertion = Ffi_TypeMapper_GoTypeToAssertion

var goTypeToCast = Ffi_TypeMapper_GoTypeToCast

var shortTypeName = Ffi_TypeMapper_ShortTypeName

var findLastDot = Ffi_TypeMapper_FindLastDot

var lowerCamelCase = Ffi_TypeMapper_LowerCamelCase

var generateWrappers = Ffi_WrapperGen_GenerateWrappers

var GenerateWrappers = Ffi_WrapperGen_GenerateWrappers

var Pure = Ffi_WrapperGen_Pure

var Fallible = Ffi_WrapperGen_Fallible

var Effectful = Ffi_WrapperGen_Effectful

var classifyFunc = Ffi_WrapperGen_ClassifyFunc

var ClassifyFunc = Ffi_WrapperGen_ClassifyFunc

var isEffectfulName = Ffi_WrapperGen_IsEffectfulName

var IsEffectfulName = Ffi_WrapperGen_IsEffectfulName

var generateWrapperFile = Ffi_WrapperGen_GenerateWrapperFile

var GenerateWrapperFile = Ffi_WrapperGen_GenerateWrapperFile

var canWrapFuncForPkg = Ffi_WrapperGen_CanWrapFuncForPkg

var CanWrapFuncForPkg = Ffi_WrapperGen_CanWrapFuncForPkg

var canWrapMethodForPkg = Ffi_WrapperGen_CanWrapMethodForPkg

var CanWrapMethodForPkg = Ffi_WrapperGen_CanWrapMethodForPkg

var isSupportedTypeForPkg = Ffi_WrapperGen_IsSupportedTypeForPkg

var IsSupportedTypeForPkg = Ffi_WrapperGen_IsSupportedTypeForPkg

var isSupportedType = Ffi_WrapperGen_IsSupportedType

var IsSupportedType = Ffi_WrapperGen_IsSupportedType

var needsExtraImport = Ffi_WrapperGen_NeedsExtraImport

var NeedsExtraImport = Ffi_WrapperGen_NeedsExtraImport

var isAdaptableFuncType = Ffi_WrapperGen_IsAdaptableFuncType

var IsAdaptableFuncType = Ffi_WrapperGen_IsAdaptableFuncType

var isSupportedResultType = Ffi_WrapperGen_IsSupportedResultType

var IsSupportedResultType = Ffi_WrapperGen_IsSupportedResultType

var generateFuncWrapper = Ffi_WrapperGen_GenerateFuncWrapper

var GenerateFuncWrapper = Ffi_WrapperGen_GenerateFuncWrapper

var generateMethodWrapper = Ffi_WrapperGen_GenerateMethodWrapper

var GenerateMethodWrapper = Ffi_WrapperGen_GenerateMethodWrapper

var generateArgCast = Ffi_WrapperGen_GenerateArgCast

var GenerateArgCast = Ffi_WrapperGen_GenerateArgCast

var wrapReturn = Ffi_WrapperGen_WrapReturn

var WrapReturn = Ffi_WrapperGen_WrapReturn

var wrapPureReturn = Ffi_WrapperGen_WrapPureReturn

var WrapPureReturn = Ffi_WrapperGen_WrapPureReturn

var wrapFallibleReturn = Ffi_WrapperGen_WrapFallibleReturn

var WrapFallibleReturn = Ffi_WrapperGen_WrapFallibleReturn

var wrapEffectfulReturn = Ffi_WrapperGen_WrapEffectfulReturn

var WrapEffectfulReturn = Ffi_WrapperGen_WrapEffectfulReturn

var shortPkgName = Ffi_WrapperGen_ShortPkgName

var ShortPkgName = Ffi_WrapperGen_ShortPkgName

var extractFunctions = Ffi_WrapperGen_ExtractFunctions

var ExtractFunctions = Ffi_WrapperGen_ExtractFunctions

var parseFuncEntry = Ffi_WrapperGen_ParseFuncEntry

var ParseFuncEntry = Ffi_WrapperGen_ParseFuncEntry

var parseParamPair = Ffi_WrapperGen_ParseParamPair

var ParseParamPair = Ffi_WrapperGen_ParseParamPair

var extractMethods = Ffi_WrapperGen_ExtractMethods

var ExtractMethods = Ffi_WrapperGen_ExtractMethods

var extractFieldAccessors = Ffi_WrapperGen_ExtractFieldAccessors

var ExtractFieldAccessors = Ffi_WrapperGen_ExtractFieldAccessors

var extractFieldsFromType = Ffi_WrapperGen_ExtractFieldsFromType

var ExtractFieldsFromType = Ffi_WrapperGen_ExtractFieldsFromType

var generateFieldAccessor = Ffi_WrapperGen_GenerateFieldAccessor

var GenerateFieldAccessor = Ffi_WrapperGen_GenerateFieldAccessor

var isSafeFieldType = Ffi_WrapperGen_IsSafeFieldType

var varTypeCast = Ffi_WrapperGen_VarTypeCast

var VarTypeCast = Ffi_WrapperGen_VarTypeCast

var extractVarAccessors = Ffi_WrapperGen_ExtractVarAccessors

var ExtractVarAccessors = Ffi_WrapperGen_ExtractVarAccessors

var generateVarAccessor = Ffi_WrapperGen_GenerateVarAccessor

var GenerateVarAccessor = Ffi_WrapperGen_GenerateVarAccessor

var isGenericType = Ffi_WrapperGen_IsGenericType

var IsGenericType = Ffi_WrapperGen_IsGenericType

var extractMethodsFromType = Ffi_WrapperGen_ExtractMethodsFromType

var ExtractMethodsFromType = Ffi_WrapperGen_ExtractMethodsFromType

var parseMethodEntry = Ffi_WrapperGen_ParseMethodEntry

var ParseMethodEntry = Ffi_WrapperGen_ParseMethodEntry

func Ffi_BindingGen_GenerateBindings(pkgName any, outDir any) any {
	return func() any { return func() any { __subject := Ffi_Inspector_InspectPackage(pkgName); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { inspectJson := sky_asSkyResult(__subject).OkValue; _ = inspectJson; return func() any { skyiContent := Ffi_BindingGen_GenerateSkyiFile(pkgName, inspectJson); _ = skyiContent; skyiPath := sky_concat(outDir, "/bindings.skyi"); _ = skyiPath; sky_fileMkdirAll(outDir); sky_call(sky_fileWrite(skyiPath), skyiContent); wrapperResult := Ffi_WrapperGen_GenerateWrappers(pkgName, inspectJson, outDir); _ = wrapperResult; return func() any { return func() any { __subject := wrapperResult; if sky_asSkyResult(__subject).SkyName == "Ok" { return SkyOk(skyiContent) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  return nil }() }() }() };  return nil }() }()
}

func Ffi_BindingGen_GenerateSkyiFile(pkgName any, inspectJson any) any {
	return func() any { moduleName := Ffi_BindingGen_PkgToModuleName(pkgName); _ = moduleName; funcLines := Ffi_BindingGen_ExtractFuncBindings(pkgName, inspectJson); _ = funcLines; typeLines := Ffi_BindingGen_ExtractTypeBindings(inspectJson); _ = typeLines; fieldLines := Ffi_BindingGen_ExtractFieldBindings(pkgName, inspectJson); _ = fieldLines; methodLines := Ffi_BindingGen_ExtractMethodBindings(pkgName, inspectJson); _ = methodLines; varLines := Ffi_BindingGen_ExtractVarBindings(pkgName, inspectJson); _ = varLines; return sky_call(sky_stringJoin("\n"), []any{sky_concat("module ", sky_concat(moduleName, " exposing (..)")), "", sky_concat("foreign import \"", sky_concat(pkgName, "\" exposing (..)")), "", sky_call(sky_stringJoin("\n\n"), funcLines), "", sky_call(sky_stringJoin("\n\n"), typeLines), "", sky_call(sky_stringJoin("\n\n"), fieldLines), "", sky_call(sky_stringJoin("\n\n"), methodLines), "", sky_call(sky_stringJoin("\n\n"), varLines)}) }()
}

func Ffi_BindingGen_PkgToModuleName(pkgPath any) any {
	return func() any { slashParts := sky_call(sky_stringSplit("/"), pkgPath); _ = slashParts; allParts := sky_call(sky_listConcatMap(func(seg any) any { return sky_call(sky_stringSplit("."), seg) }), sky_call(sky_listMap(func(seg any) any { return Ffi_BindingGen_HyphenToCamel(seg) }), slashParts)); _ = allParts; capitalized := sky_call(sky_listMap(Ffi_BindingGen_CapitalizeFirst), allParts); _ = capitalized; return sky_call(sky_stringJoin("."), capitalized) }()
}

func Ffi_BindingGen_ExtractFieldBindings(pkgName any, json any) any {
	return func() any { typesArrayStr := Lsp_JsonRpc_JsonGetArrayRaw("types", json); _ = typesArrayStr; typeElements := Lsp_JsonRpc_JsonSplitArray(typesArrayStr); _ = typeElements; return func() any { safePkg := sky_call2(sky_stringReplace("-"), "_", sky_call2(sky_stringReplace("/"), "_", sky_call2(sky_stringReplace("."), "_", pkgName))); _ = safePkg; return sky_call(sky_listConcatMap(func(t any) any { return Ffi_BindingGen_ExtractFieldsForType(safePkg, t) }), typeElements) }() }()
}

func Ffi_BindingGen_ExtractFieldsForType(safePkg any, typeJson any) any {
	return func() any { typeName := Lsp_JsonRpc_JsonGetString("name", typeJson); _ = typeName; kind := Lsp_JsonRpc_JsonGetString("kind", typeJson); _ = kind; fieldsArrayStr := Lsp_JsonRpc_JsonGetArrayRaw("fields", typeJson); _ = fieldsArrayStr; fieldElements := Lsp_JsonRpc_JsonSplitArray(fieldsArrayStr); _ = fieldElements; return func() any { if sky_asBool(sky_asBool(sky_equal(kind, "struct")) && sky_asBool(sky_not(Ffi_BindingGen_IsGenericTypeBinding(typeJson)))) { return sky_call(sky_listFilterMap(func(f any) any { return Ffi_BindingGen_GenerateFieldBinding(safePkg, typeName, f) }), fieldElements) }; return []any{} }() }()
}

func Ffi_BindingGen_IsGenericTypeBinding(typeJson any) any {
	return func() any { fieldsStr := Lsp_JsonRpc_JsonGetArrayRaw("fields", typeJson); _ = fieldsStr; fields := Lsp_JsonRpc_JsonSplitArray(fieldsStr); _ = fields; fieldTypes := sky_call(sky_listMap(func(f any) any { return Lsp_JsonRpc_JsonGetString("type", f) }), fields); _ = fieldTypes; return sky_call(sky_listAny(func(t any) any { return sky_equal(sky_stringLength(t), 1) }), fieldTypes) }()
}

func Ffi_BindingGen_GenerateFieldBinding(safePkg any, typeName any, fieldJson any) any {
	return func() any { fieldName := Lsp_JsonRpc_JsonGetString("name", fieldJson); _ = fieldName; fieldType := Lsp_JsonRpc_JsonGetString("type", fieldJson); _ = fieldType; skyFieldName := sky_concat(Ffi_TypeMapper_LowerCamelCase(typeName), fieldName); _ = skyFieldName; skyType := Ffi_TypeMapper_MapGoTypeToSky(fieldType); _ = skyType; wrapperName := sky_concat("Sky_", sky_concat(safePkg, sky_concat("_FIELD_", sky_concat(typeName, sky_concat("_", fieldName))))); _ = wrapperName; return func() any { if sky_asBool(sky_asBool(sky_stringIsEmpty(fieldName)) || sky_asBool(sky_not(Ffi_BindingGen_IsSafeFieldType(fieldType)))) { return SkyNothing() }; return SkyJust(sky_concat(skyFieldName, sky_concat(" : Any -> ", sky_concat(skyType, sky_concat("\n", sky_concat(skyFieldName, sky_concat(" receiver =\n", sky_concat("    ", sky_concat(wrapperName, " receiver"))))))))) }() }()
}

func Ffi_BindingGen_IsSafeFieldType(_ any) any {
	return true
}

func Ffi_BindingGen_ExtractVarBindings(pkgName any, json any) any {
	return func() any { varsArrayStr := Lsp_JsonRpc_JsonGetArrayRaw("vars", json); _ = varsArrayStr; varElements := Lsp_JsonRpc_JsonSplitArray(varsArrayStr); _ = varElements; safePkg := sky_call2(sky_stringReplace("-"), "_", sky_call2(sky_stringReplace("/"), "_", sky_call2(sky_stringReplace("."), "_", pkgName))); _ = safePkg; return sky_call(sky_listFilterMap(func(v any) any { return Ffi_BindingGen_GenerateVarBinding(pkgName, safePkg, v) }), varElements) }()
}

func Ffi_BindingGen_GenerateVarBinding(pkgName any, safePkg any, varJson any) any {
	return func() any { name := Lsp_JsonRpc_JsonGetString("name", varJson); _ = name; varType := Lsp_JsonRpc_JsonGetString("type", varJson); _ = varType; skyName := Ffi_TypeMapper_LowerCamelCase(name); _ = skyName; skyType := Ffi_TypeMapper_MapGoTypeToSky(varType); _ = skyType; wrapperName := sky_concat("Sky_", sky_concat(safePkg, sky_concat("_", name))); _ = wrapperName; return func() any { if sky_asBool(sky_asBool(sky_stringIsEmpty(name)) || sky_asBool(sky_equal(varType, "error"))) { return SkyNothing() }; return func() any { if sky_asBool(sky_equal(varType, "string")) { return func() any { setterName := sky_concat("set", name); _ = setterName; setterWrapper := sky_concat("Sky_", sky_concat(safePkg, sky_concat("_Set", name))); _ = setterWrapper; return SkyJust(sky_concat(skyName, sky_concat(" : () -> ", sky_concat(skyType, sky_concat("\n", sky_concat(skyName, sky_concat(" _ =\n", sky_concat("    ", sky_concat(wrapperName, sky_concat("\n\n", sky_concat(setterName, sky_concat(" : ", sky_concat(skyType, sky_concat(" -> ()\n", sky_concat(setterName, sky_concat(" val =\n", sky_concat("    ", sky_concat(setterWrapper, " val")))))))))))))))))) }() }; return SkyJust(sky_concat(skyName, sky_concat(" : () -> ", sky_concat(skyType, sky_concat("\n", sky_concat(skyName, sky_concat(" _ =\n", sky_concat("    ", wrapperName)))))))) }() }() }()
}

func Ffi_BindingGen_ExtractMethodBindings(pkgName any, json any) any {
	return func() any { typesArrayStr := Lsp_JsonRpc_JsonGetArrayRaw("types", json); _ = typesArrayStr; typeElements := Lsp_JsonRpc_JsonSplitArray(typesArrayStr); _ = typeElements; return sky_call(sky_listConcatMap(func(t any) any { return Ffi_BindingGen_ExtractMethodsForType(pkgName, t) }), typeElements) }()
}

func Ffi_BindingGen_ExtractMethodsForType(pkgName any, typeJson any) any {
	return func() any { typeName := Lsp_JsonRpc_JsonGetString("name", typeJson); _ = typeName; methodsArrayStr := Lsp_JsonRpc_JsonGetArrayRaw("methods", typeJson); _ = methodsArrayStr; methodElements := Lsp_JsonRpc_JsonSplitArray(methodsArrayStr); _ = methodElements; return func() any { if sky_asBool(Ffi_BindingGen_IsGenericTypeBinding(typeJson)) { return []any{} }; return sky_call(sky_listFilterMap(func(m any) any { return Ffi_BindingGen_GenerateMethodBinding(pkgName, typeName, m) }), methodElements) }() }()
}

func Ffi_BindingGen_GenerateMethodBinding(pkgName any, typeName any, methodJson any) any {
	return func() any { name := Lsp_JsonRpc_JsonGetString("name", methodJson); _ = name; paramsStr := Lsp_JsonRpc_JsonGetArrayRaw("params", methodJson); _ = paramsStr; resultsStr := Lsp_JsonRpc_JsonGetArrayRaw("results", methodJson); _ = resultsStr; params := Lsp_JsonRpc_JsonSplitArray(paramsStr); _ = params; results := Lsp_JsonRpc_JsonSplitArray(resultsStr); _ = results; paramTypes := sky_call(sky_listMap(func(p any) any { return Lsp_JsonRpc_JsonGetString("type", p) }), params); _ = paramTypes; resultTypes := sky_call(sky_listMap(func(r any) any { return Lsp_JsonRpc_JsonGetString("type", r) }), results); _ = resultTypes; variadic := Lsp_JsonRpc_JsonGetBool("variadic", methodJson); _ = variadic; return func() any { if sky_asBool(sky_asBool(sky_stringIsEmpty(name)) || sky_asBool(sky_asInt(sky_listLength(results)) > sky_asInt(2))) { return SkyNothing() }; return func() any { if sky_asBool(sky_not(Ffi_BindingGen_AllMethodParamsSafe(pkgName, paramTypes, variadic))) { return SkyNothing() }; return func() any { if sky_asBool(sky_not(sky_call(sky_listAll(func(t any) any { return Ffi_BindingGen_IsSafeTypeForPkg(pkgName, t) }), resultTypes))) { return SkyNothing() }; return func() any { safePkg := sky_call2(sky_stringReplace("-"), "_", sky_call2(sky_stringReplace("/"), "_", sky_call2(sky_stringReplace("."), "_", pkgName))); _ = safePkg; skyName := sky_concat(Ffi_TypeMapper_LowerCamelCase(typeName), name); _ = skyName; wrapperName := sky_concat("Sky_", sky_concat(safePkg, sky_concat("_", sky_concat(typeName, name)))); _ = wrapperName; skyParamTypes := sky_call(sky_listMap(func(t any) any { return Ffi_TypeMapper_MapGoTypeToSky(t) }), paramTypes); _ = skyParamTypes; skyReturnType := Ffi_BindingGen_BuildReturnType(resultTypes); _ = skyReturnType; allTypes := sky_call(sky_listAppend(sky_concat([]any{typeName}, skyParamTypes)), []any{skyReturnType}); _ = allTypes; typeSignature := sky_call(sky_stringJoin(" -> "), allTypes); _ = typeSignature; paramNames := sky_call(sky_listIndexedMap(func(i any) any { return func(_ any) any { return sky_concat("arg", sky_stringFromInt(i)) } }), params); _ = paramNames; allParamNames := append([]any{"receiver"}, sky_asList(paramNames)...); _ = allParamNames; paramStr := sky_call(sky_stringJoin(" "), allParamNames); _ = paramStr; callArgs := sky_call(sky_stringJoin(" "), allParamNames); _ = callArgs; return SkyJust(sky_concat(skyName, sky_concat(" : ", sky_concat(typeSignature, sky_concat("\n", sky_concat(skyName, sky_concat(" ", sky_concat(paramStr, sky_concat(" =\n", sky_concat("    ", sky_concat(wrapperName, sky_concat(" ", callArgs)))))))))))) }() }() }() }() }()
}

func Ffi_BindingGen_HyphenToCamel(s any) any {
	return func() any { parts := sky_call(sky_stringSplit("-"), s); _ = parts; return func() any { return func() any { __subject := parts; if len(sky_asList(__subject)) == 0 { return s };  if len(sky_asList(__subject)) > 0 { first := sky_asList(__subject)[0]; _ = first; rest := sky_asList(__subject)[1:]; _ = rest; return sky_concat(first, sky_call(sky_stringJoin(""), sky_call(sky_listMap(Ffi_BindingGen_CapitalizeFirst), rest))) };  return nil }() }() }()
}

func Ffi_BindingGen_CapitalizeFirst(s any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(s)) { return "" }; return sky_concat(sky_stringToUpper(sky_call2(sky_stringSlice(0), 1, s)), sky_call2(sky_stringSlice(1), sky_stringLength(s), s)) }()
}

func Ffi_BindingGen_ExtractFuncBindings(pkgName any, json any) any {
	return func() any { funcsArrayStr := Lsp_JsonRpc_JsonGetArrayRaw("funcs", json); _ = funcsArrayStr; elements := Lsp_JsonRpc_JsonSplitArray(funcsArrayStr); _ = elements; return sky_call(sky_listFilterMap(func(e any) any { return Ffi_BindingGen_GenerateFuncBinding(pkgName, e) }), sky_call(sky_listFilter(func(f any) any { return Ffi_BindingGen_IsSupportedFuncForPkg(pkgName, f) }), elements)) }()
}

func Ffi_BindingGen_IsSupportedFuncForPkg(pkgName any, funcJson any) any {
	return func() any { paramsStr := Lsp_JsonRpc_JsonGetArrayRaw("params", funcJson); _ = paramsStr; resultsStr := Lsp_JsonRpc_JsonGetArrayRaw("results", funcJson); _ = resultsStr; params := Lsp_JsonRpc_JsonSplitArray(paramsStr); _ = params; results := Lsp_JsonRpc_JsonSplitArray(resultsStr); _ = results; paramTypes := sky_call(sky_listMap(func(p any) any { return Lsp_JsonRpc_JsonGetString("type", p) }), params); _ = paramTypes; resultTypes := sky_call(sky_listMap(func(r any) any { return Lsp_JsonRpc_JsonGetString("type", r) }), results); _ = resultTypes; variadic := Lsp_JsonRpc_JsonGetBool("variadic", funcJson); _ = variadic; paramCount := sky_listLength(paramTypes); _ = paramCount; return sky_asBool(sky_asInt(sky_listLength(results)) <= sky_asInt(2)) && sky_asBool(sky_asBool(sky_call(sky_listAll(func(pair any) any { return func() any { idx := sky_fst(pair); _ = idx; t := sky_snd(pair); _ = t; return func() any { if sky_asBool(sky_asBool(variadic) && sky_asBool(sky_equal(idx, sky_asInt(paramCount) - sky_asInt(1)))) { return true }; return Ffi_BindingGen_IsSafeTypeForPkg(pkgName, t) }() }() }), sky_call(sky_listIndexedMap(func(i any) any { return func(t any) any { return SkyTuple2{V0: i, V1: t} } }), paramTypes))) && sky_asBool(sky_call(sky_listAll(func(t any) any { return Ffi_BindingGen_IsSafeTypeForPkg(pkgName, t) }), resultTypes))) }()
}

func Ffi_BindingGen_AllMethodParamsSafe(pkgName any, paramTypes any, variadic any) any {
	return Ffi_BindingGen_AllMethodParamsLoop(pkgName, paramTypes, variadic, 0, sky_listLength(paramTypes))
}

func Ffi_BindingGen_AllMethodParamsLoop(pkgName any, params any, variadic any, idx any, total any) any {
	return func() any { return func() any { __subject := params; if len(sky_asList(__subject)) == 0 { return true };  if len(sky_asList(__subject)) > 0 { t := sky_asList(__subject)[0]; _ = t; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { if sky_asBool(sky_asBool(variadic) && sky_asBool(sky_equal(idx, sky_asInt(total) - sky_asInt(1)))) { return Ffi_BindingGen_AllMethodParamsLoop(pkgName, rest, variadic, sky_asInt(idx) + sky_asInt(1), total) }; return func() any { if sky_asBool(Ffi_BindingGen_IsSafeTypeForPkg(pkgName, t)) { return Ffi_BindingGen_AllMethodParamsLoop(pkgName, rest, variadic, sky_asInt(idx) + sky_asInt(1), total) }; return false }() }() };  return nil }() }()
}

func Ffi_BindingGen_IsSafeTypeForPkg(pkgName any, goType any) any {
	return func() any { if sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("func("), goType)) && sky_asBool(sky_not(sky_call(sky_stringContains("ResponseWriter"), goType)))) { return false }; return func() any { if sky_asBool(sky_asBool(sky_call(sky_stringContains("["), goType)) && sky_asBool(sky_asBool(sky_call(sky_stringContains("]"), goType)) && sky_asBool(sky_asBool(sky_not(sky_call(sky_stringStartsWith("["), goType))) && sky_asBool(sky_not(sky_call(sky_stringStartsWith("map[string]interface{}"), goType)))))) { return false }; return func() any { if sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("map["), goType)) && sky_asBool(sky_asBool(!sky_equal(goType, "map[string]interface{}")) && sky_asBool(sky_asBool(!sky_equal(goType, "map[string]any")) && sky_asBool(!sky_equal(goType, "map[string]string"))))) { return false }; return func() any { if sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("[]"), goType)) && sky_asBool(sky_asBool(!sky_equal(goType, "[]byte")) && sky_asBool(sky_asBool(!sky_equal(goType, "[]string")) && sky_asBool(!sky_equal(goType, "[]any"))))) { return false }; return func() any { parts := sky_call(sky_stringSplit("/"), pkgName); _ = parts; firstPart := func() any { return func() any { __subject := parts; if len(sky_asList(__subject)) > 0 { f := sky_asList(__subject)[0]; _ = f; return f };  if len(sky_asList(__subject)) == 0 { return "" };  return nil }() }(); _ = firstPart; lastPart := func() any { return func() any { __subject := sky_listReverse(parts); if len(sky_asList(__subject)) > 0 { l := sky_asList(__subject)[0]; _ = l; return l };  if len(sky_asList(__subject)) == 0 { return "" };  return nil }() }(); _ = lastPart; isExternal := sky_call(sky_stringContains("."), firstPart); _ = isExternal; candidateParent := sky_call(sky_stringJoin("/"), sky_listReverse(sky_call(sky_listDrop(1), sky_listReverse(parts)))); _ = candidateParent; parentParts := sky_call(sky_stringSplit("/"), candidateParent); _ = parentParts; isSubPkg := sky_asBool(isExternal) && sky_asBool(sky_asBool(sky_asInt(sky_listLength(parentParts)) >= sky_asInt(3)) && sky_asBool(sky_not(sky_asBool(sky_call(sky_stringStartsWith("v"), lastPart)) && sky_asBool(sky_asInt(sky_stringLength(lastPart)) <= sky_asInt(3))))); _ = isSubPkg; parentPkg := func() any { if sky_asBool(isSubPkg) { return candidateParent }; return "" }(); _ = parentPkg; return sky_asBool(Ffi_BindingGen_IsSafeType(goType)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith(sky_concat(pkgName, ".")), goType)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith(sky_concat("*", sky_concat(pkgName, "."))), goType)) || sky_asBool(sky_asBool(sky_asBool(sky_not(sky_stringIsEmpty(parentPkg))) && sky_asBool(sky_call(sky_stringStartsWith(sky_concat(parentPkg, ".")), goType))) || sky_asBool(sky_asBool(sky_not(sky_stringIsEmpty(parentPkg))) && sky_asBool(sky_call(sky_stringStartsWith(sky_concat("*", sky_concat(parentPkg, "."))), goType)))))) }() }() }() }() }()
}

func Ffi_BindingGen_IsSafeType(goType any) any {
	return sky_asBool(sky_equal(goType, "string")) || sky_asBool(sky_asBool(sky_equal(goType, "bool")) || sky_asBool(sky_asBool(sky_equal(goType, "int")) || sky_asBool(sky_asBool(sky_equal(goType, "[]byte")) || sky_asBool(sky_asBool(sky_equal(goType, "int8")) || sky_asBool(sky_asBool(sky_equal(goType, "int16")) || sky_asBool(sky_asBool(sky_equal(goType, "int32")) || sky_asBool(sky_asBool(sky_equal(goType, "int64")) || sky_asBool(sky_asBool(sky_equal(goType, "uint")) || sky_asBool(sky_asBool(sky_equal(goType, "uint8")) || sky_asBool(sky_asBool(sky_equal(goType, "uint16")) || sky_asBool(sky_asBool(sky_equal(goType, "uint32")) || sky_asBool(sky_asBool(sky_equal(goType, "uint64")) || sky_asBool(sky_asBool(sky_equal(goType, "float32")) || sky_asBool(sky_asBool(sky_equal(goType, "float64")) || sky_asBool(sky_asBool(sky_equal(goType, "interface{}")) || sky_asBool(sky_asBool(sky_equal(goType, "any")) || sky_asBool(sky_asBool(sky_equal(goType, "error")) || sky_asBool(sky_asBool(sky_equal(goType, "context.Context")) || sky_asBool(sky_asBool(sky_equal(goType, "[]string")) || sky_asBool(sky_asBool(sky_equal(goType, "[]int")) || sky_asBool(sky_asBool(sky_equal(goType, "[]float64")) || sky_asBool(sky_asBool(sky_equal(goType, "[]bool")) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("["), goType)) || sky_asBool(sky_asBool(sky_asBool(sky_call(sky_stringContains("."), goType)) && sky_asBool(sky_not(Ffi_BindingGen_NeedsExtraImportBinding(goType)))) || sky_asBool(sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("*"), goType)) && sky_asBool(Ffi_BindingGen_IsSafeType(sky_call2(sky_stringSlice(1), sky_stringLength(goType), goType)))) || sky_asBool(sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("[]"), goType)) && sky_asBool(Ffi_BindingGen_IsSafeType(sky_call2(sky_stringSlice(2), sky_stringLength(goType), goType)))) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("func("), goType)) || sky_asBool(sky_call(sky_stringStartsWith("map["), goType)))))))))))))))))))))))))))))
}

func Ffi_BindingGen_NeedsExtraImportBinding(goType any) any {
	return func() any { t := func() any { if sky_asBool(sky_call(sky_stringStartsWith("*"), goType)) { return sky_call2(sky_stringSlice(1), sky_stringLength(goType), goType) }; return goType }(); _ = t; return sky_asBool(sky_call(sky_stringContains("/"), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("io."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("hash."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("context."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("sync."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("net."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("crypto."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("encoding."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("reflect."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("testing."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("log."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("regexp."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("mime."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("html."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("text."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("bufio."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("fs."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("os."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("time."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("fmt."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("sort."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("math."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("strings."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("bytes."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("strconv."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("database."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("sql."), t)) || sky_asBool(sky_call(sky_stringStartsWith("unicode."), t)))))))))))))))))))))))))))) }()
}

func Ffi_BindingGen_GenerateFuncBinding(pkgName any, funcJson any) any {
	return func() any { name := Lsp_JsonRpc_JsonGetString("name", funcJson); _ = name; return func() any { if sky_asBool(sky_stringIsEmpty(name)) { return SkyNothing() }; return func() any { skyName := Ffi_TypeMapper_LowerCamelCase(name); _ = skyName; safePkg := sky_call2(sky_stringReplace("-"), "_", sky_call2(sky_stringReplace("/"), "_", sky_call2(sky_stringReplace("."), "_", pkgName))); _ = safePkg; wrapperName := sky_concat("Sky_", sky_concat(safePkg, sky_concat("_", name))); _ = wrapperName; paramsArrayStr := Lsp_JsonRpc_JsonGetArrayRaw("params", funcJson); _ = paramsArrayStr; paramElements := Lsp_JsonRpc_JsonSplitArray(paramsArrayStr); _ = paramElements; paramTypes := sky_call(sky_listMap(func(p any) any { return Ffi_TypeMapper_MapGoTypeToSky(Lsp_JsonRpc_JsonGetString("type", p)) }), paramElements); _ = paramTypes; resultsArrayStr := Lsp_JsonRpc_JsonGetArrayRaw("results", funcJson); _ = resultsArrayStr; resultElements := Lsp_JsonRpc_JsonSplitArray(resultsArrayStr); _ = resultElements; resultTypes := sky_call(sky_listMap(func(r any) any { return Lsp_JsonRpc_JsonGetString("type", r) }), resultElements); _ = resultTypes; skyReturnType := Ffi_BindingGen_BuildReturnType(resultTypes); _ = skyReturnType; allTypes := sky_call(sky_listAppend(paramTypes), []any{skyReturnType}); _ = allTypes; typeSignature := sky_call(sky_stringJoin(" -> "), allTypes); _ = typeSignature; paramNames := sky_call(sky_listIndexedMap(func(i any) any { return func(_ any) any { return sky_concat("arg", sky_stringFromInt(i)) } }), paramElements); _ = paramNames; paramStr := sky_call(sky_stringJoin(" "), paramNames); _ = paramStr; callArgs := sky_call(sky_stringJoin(" "), paramNames); _ = callArgs; hasParams := sky_not(sky_listIsEmpty(paramElements)); _ = hasParams; finalParamStr := func() any { if sky_asBool(hasParams) { return paramStr }; return "_" }(); _ = finalParamStr; finalTypeSignature := func() any { if sky_asBool(hasParams) { return typeSignature }; return sky_concat("() -> ", skyReturnType) }(); _ = finalTypeSignature; return SkyJust(sky_concat(skyName, sky_concat(" : ", sky_concat(finalTypeSignature, sky_concat("\n", sky_concat(skyName, sky_concat(" ", sky_concat(finalParamStr, sky_concat(" =\n", sky_concat("    ", sky_concat(wrapperName, sky_concat(" ", callArgs)))))))))))) }() }() }()
}

func Ffi_BindingGen_BuildReturnType(results any) any {
	return func() any { if sky_asBool(sky_listIsEmpty(results)) { return "()" }; return func() any { if sky_asBool(sky_equal(sky_listLength(results), 1)) { return func() any { single := func() any { return func() any { __subject := sky_listHead(results); if sky_asSkyMaybe(__subject).SkyName == "Just" { s := sky_asSkyMaybe(__subject).JustValue; _ = s; return s };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "" };  return nil }() }(); _ = single; return func() any { if sky_asBool(sky_equal(single, "error")) { return "Result String ()" }; return Ffi_TypeMapper_MapGoTypeToSky(single) }() }() }; return func() any { lastResult := func() any { return func() any { __subject := sky_listReverse(results); if len(sky_asList(__subject)) > 0 { last := sky_asList(__subject)[0]; _ = last; return last };  if len(sky_asList(__subject)) == 0 { return "" };  return nil }() }(); _ = lastResult; return func() any { if sky_asBool(sky_equal(lastResult, "error")) { return func() any { return func() any { __subject := results; if len(sky_asList(__subject)) > 0 { first := sky_asList(__subject)[0]; _ = first; return sky_concat("Result String ", Ffi_TypeMapper_MapGoTypeToSky(first)) };  if len(sky_asList(__subject)) == 0 { return "Result String ()" };  return nil }() }() }; return sky_concat("(", sky_concat(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(func(r any) any { return Ffi_TypeMapper_MapGoTypeToSky(r) }), results)), ")")) }() }() }() }()
}

func Ffi_BindingGen_ExtractTypeBindings(json any) any {
	return func() any { typesArrayStr := Lsp_JsonRpc_JsonGetArrayRaw("types", json); _ = typesArrayStr; elements := Lsp_JsonRpc_JsonSplitArray(typesArrayStr); _ = elements; return sky_call(sky_listFilterMap(Ffi_BindingGen_GenerateTypeBinding), elements) }()
}

func Ffi_BindingGen_GenerateTypeBinding(typeJson any) any {
	return func() any { name := Lsp_JsonRpc_JsonGetString("name", typeJson); _ = name; kind := Lsp_JsonRpc_JsonGetString("kind", typeJson); _ = kind; return func() any { if sky_asBool(sky_stringIsEmpty(name)) { return SkyNothing() }; return SkyJust(sky_concat("type ", sky_concat(name, sky_concat(" = ", name)))) }() }()
}

func Ffi_Inspector_InspectPackage(pkgName any) any {
	return func() any { cacheDir := sky_concat(".skycache/go/", Ffi_Inspector_SafePkgName(pkgName)); _ = cacheDir; cachePath := sky_concat(cacheDir, "/inspect.json"); _ = cachePath; return func() any { return func() any { __subject := sky_fileRead(cachePath); if sky_asSkyResult(__subject).SkyName == "Ok" { cached := sky_asSkyResult(__subject).OkValue; _ = cached; return SkyOk(cached) };  if sky_asSkyResult(__subject).SkyName == "Err" { return Ffi_Inspector_RunInspector(pkgName, cacheDir, cachePath) };  return nil }() }() }()
}

func Ffi_Inspector_RunInspector(pkgName any, cacheDir any, cachePath any) any {
	return func() any { inspectorDir := ".skycache/inspector"; _ = inspectorDir; sky_fileMkdirAll(inspectorDir); sky_fileMkdirAll(cacheDir); sky_call(sky_fileWrite(sky_concat(inspectorDir, "/main.go")), Ffi_Inspector_InspectorGoCode()); sky_call(sky_fileWrite(sky_concat(inspectorDir, "/go.mod")), "module sky-inspector\n\ngo 1.24.0\n\nrequire golang.org/x/tools v0.30.0\n"); buildResult := sky_call(sky_processRun("sh"), []any{"-c", sky_concat("cd ", sky_concat(inspectorDir, " && go mod tidy 2>/dev/null && go build -gcflags=all=-l -o inspector . 2>&1"))}); _ = buildResult; return func() any { return func() any { __subject := buildResult; if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Failed to build Go inspector: ", sky_errorToString(e))) };  if sky_asSkyResult(__subject).SkyName == "Ok" { return func() any { gomodDir := ".skycache/gomod"; _ = gomodDir; sky_call(sky_processRun("sh"), []any{"-c", sky_concat("cd ", sky_concat(gomodDir, sky_concat(" && go get ", sky_concat(pkgName, " 2>/dev/null"))))}); sky_call(sky_processRun("cp"), []any{sky_concat(inspectorDir, "/inspector"), sky_concat(gomodDir, "/inspector")}); return func() any { absCache := sky_concat("../../", cachePath); _ = absCache; runResult := sky_call(sky_processRun("sh"), []any{"-c", sky_concat("cd ", sky_concat(gomodDir, sky_concat(" && GOFLAGS='-gcflags=all=-l' ./inspector ", sky_concat(pkgName, sky_concat(" > ", sky_concat(absCache, " 2>/dev/null"))))))}); _ = runResult; return func() any { return func() any { __subject := runResult; if sky_asSkyResult(__subject).SkyName == "Ok" { return sky_fileRead(cachePath) };  if sky_asSkyResult(__subject).SkyName == "Err" { return func() any { retryResult := sky_call(sky_processRun("sh"), []any{"-c", sky_concat("cd ", sky_concat(gomodDir, sky_concat(" && GOFLAGS='-gcflags=all=-l' ./inspector --minimal ", sky_concat(pkgName, sky_concat(" > ", sky_concat(absCache, " 2>/dev/null"))))))}); _ = retryResult; return func() any { return func() any { __subject := retryResult; if sky_asSkyResult(__subject).SkyName == "Ok" { return sky_fileRead(cachePath) };  if sky_asSkyResult(__subject).SkyName == "Err" { e2 := sky_asSkyResult(__subject).ErrValue; _ = e2; return SkyErr(sky_concat("Failed to inspect package ", sky_concat(pkgName, sky_concat(": ", sky_errorToString(e2))))) };  return nil }() }() }() };  return nil }() }() }() }() };  return nil }() }() }()
}

func Ffi_Inspector_SafePkgName(name any) any {
	return sky_call2(sky_stringReplace("-"), "_", sky_call2(sky_stringReplace("/"), "_", sky_call2(sky_stringReplace("."), "_", name)))
}

func Ffi_Inspector_InspectorGoCode() any {
	return sky_call(sky_stringJoin("\n"), []any{"package main", "", "import (", "\t\"encoding/json\"", "\t\"fmt\"", "\t\"go/importer\"", "\t\"go/token\"", "\t\"go/types\"", "\t\"os\"", "\t_ \"strings\"", "\t\"golang.org/x/tools/go/packages\"", ")", "", "type Output struct {", "\tName   string     `json:\"name\"`", "\tPath   string     `json:\"path\"`", "\tTypes  []TypeDecl `json:\"types\"`", "\tFuncs  []FuncDecl `json:\"funcs\"`", "\tVars   []VarDecl  `json:\"vars\"`", "\tConsts []ConstDecl `json:\"consts\"`", "}", "", "type TypeDecl struct {", "\tName    string      `json:\"name\"`", "\tKind    string      `json:\"kind\"`", "\tFields  []FieldDecl `json:\"fields,omitempty\"`", "\tMethods []MethodDecl `json:\"methods,omitempty\"`", "}", "", "type FieldDecl struct {", "\tName string `json:\"name\"`", "\tType string `json:\"type\"`", "}", "", "type MethodDecl struct {", "\tName     string      `json:\"name\"`", "\tParams   []ParamDecl `json:\"params\"`", "\tResults  []ParamDecl `json:\"results\"`", "\tVariadic bool        `json:\"variadic,omitempty\"`", "}", "", "type FuncDecl struct {", "\tName     string      `json:\"name\"`", "\tParams   []ParamDecl `json:\"params\"`", "\tResults  []ParamDecl `json:\"results\"`", "\tVariadic bool        `json:\"variadic,omitempty\"`", "}", "", "type ParamDecl struct {", "\tName string `json:\"name\"`", "\tType string `json:\"type\"`", "}", "", "type VarDecl struct {", "\tName string `json:\"name\"`", "\tType string `json:\"type\"`", "}", "", "type ConstDecl struct {", "\tName  string `json:\"name\"`", "\tType  string `json:\"type\"`", "\tValue string `json:\"value,omitempty\"`", "}", "", "func typeStr(t types.Type) string {", "\tswitch u := t.(type) {", "\tcase *types.Named:", "\t\tobj := u.Obj()", "\t\tpkg := obj.Pkg()", "\t\tif pkg != nil {", "\t\t\treturn pkg.Path() + \".\" + obj.Name()", "\t\t}", "\t\treturn obj.Name()", "\tcase *types.Pointer:", "\t\treturn \"*\" + typeStr(u.Elem())", "\tcase *types.Slice:", "\t\treturn \"[]\" + typeStr(u.Elem())", "\tcase *types.Map:", "\t\treturn \"map[\" + typeStr(u.Key()) + \"]\" + typeStr(u.Elem())", "\tcase *types.Interface:", "\t\tif u.Empty() { return \"interface{}\" }", "\t\treturn \"interface{}\"", "\tdefault:", "\t\treturn t.String()", "\t}", "}", "", "func main() {", "\tif len(os.Args) < 2 {", "\t\tfmt.Fprintln(os.Stderr, \"usage: inspector <package>\")", "\t\tos.Exit(1)", "\t}", "\tminimal := len(os.Args) > 2 && os.Args[1] == \"--minimal\"", "\tpkgPath := os.Args[len(os.Args)-1]", "", "\tvar pkg *types.Package", "\tif minimal {", "\t\t// Use go/importer for large packages (avoids OOM from go/packages NeedSyntax)", "\t\tfset := token.NewFileSet()", "\t\timp := importer.ForCompiler(fset, \"source\", nil)", "\t\tvar err error", "\t\tpkg, err = imp.Import(pkgPath)", "\t\tif err != nil {", "\t\t\tfmt.Fprintf(os.Stderr, \"import error: %v\\n\", err)", "\t\t\tos.Exit(1)", "\t\t}", "\t} else {", "\t\tcfg := &packages.Config{Mode: packages.NeedTypes | packages.NeedName | packages.NeedImports | packages.NeedDeps | packages.NeedSyntax}", "\t\tpkgs, err := packages.Load(cfg, pkgPath)", "\t\tif err != nil {", "\t\t\tfmt.Fprintf(os.Stderr, \"load error: %v\\n\", err)", "\t\t\tos.Exit(1)", "\t\t}", "\t\tif len(pkgs) == 0 || pkgs[0].Types == nil {", "\t\t\tfmt.Fprintln(os.Stderr, \"no types found\")", "\t\t\tos.Exit(1)", "\t\t}", "\t\tpkg = pkgs[0].Types", "\t}", "", "\tscope := pkg.Scope()", "\tout := Output{Name: pkg.Name(), Path: pkg.Path()}", "", "\tfor _, name := range scope.Names() {", "\t\tobj := scope.Lookup(name)", "\t\tif !obj.Exported() { continue }", "\t\tswitch o := obj.(type) {", "\t\tcase *types.TypeName:", "\t\t\tnamed, ok := o.Type().(*types.Named)", "\t\t\tif !ok { continue }", "\t\t\ttd := TypeDecl{Name: name}", "\t\t\tswitch u := named.Underlying().(type) {", "\t\t\tcase *types.Struct:", "\t\t\t\ttd.Kind = \"struct\"", "\t\t\t\tfor i := 0; i < u.NumFields(); i++ {", "\t\t\t\t\tf := u.Field(i)", "\t\t\t\t\tif f.Exported() {", "\t\t\t\t\t\ttd.Fields = append(td.Fields, FieldDecl{Name: f.Name(), Type: typeStr(f.Type())})", "\t\t\t\t\t}", "\t\t\t\t}", "\t\t\tcase *types.Interface:", "\t\t\t\ttd.Kind = \"interface\"", "\t\t\t\tfor i := 0; i < u.NumMethods(); i++ {", "\t\t\t\t\tm := u.Method(i)", "\t\t\t\t\tif !m.Exported() { continue }", "\t\t\t\t\tsig := m.Type().(*types.Signature)", "\t\t\t\t\tmd := MethodDecl{Name: m.Name(), Variadic: sig.Variadic()}", "\t\t\t\t\tfor j := 0; j < sig.Params().Len(); j++ {", "\t\t\t\t\t\tp := sig.Params().At(j)", "\t\t\t\t\t\tmd.Params = append(md.Params, ParamDecl{Name: p.Name(), Type: typeStr(p.Type())})", "\t\t\t\t\t}", "\t\t\t\t\tfor j := 0; j < sig.Results().Len(); j++ {", "\t\t\t\t\t\tr := sig.Results().At(j)", "\t\t\t\t\t\tmd.Results = append(md.Results, ParamDecl{Name: r.Name(), Type: typeStr(r.Type())})", "\t\t\t\t\t}", "\t\t\t\t\ttd.Methods = append(td.Methods, md)", "\t\t\t\t}", "\t\t\tdefault:", "\t\t\t\ttd.Kind = \"other\"", "\t\t\t}", "\t\t\tmset := types.NewMethodSet(types.NewPointer(named))", "\t\t\tfor i := 0; i < mset.Len(); i++ {", "\t\t\t\tm := mset.At(i)", "\t\t\t\tfn, ok := m.Obj().(*types.Func)", "\t\t\t\tif !ok || !fn.Exported() { continue }", "\t\t\t\tsig := fn.Type().(*types.Signature)", "\t\t\t\tmd := MethodDecl{Name: fn.Name(), Variadic: sig.Variadic()}", "\t\t\t\tfor j := 0; j < sig.Params().Len(); j++ {", "\t\t\t\t\tp := sig.Params().At(j)", "\t\t\t\t\tmd.Params = append(md.Params, ParamDecl{Name: p.Name(), Type: typeStr(p.Type())})", "\t\t\t\t}", "\t\t\t\tfor j := 0; j < sig.Results().Len(); j++ {", "\t\t\t\t\tr := sig.Results().At(j)", "\t\t\t\t\tmd.Results = append(md.Results, ParamDecl{Name: r.Name(), Type: typeStr(r.Type())})", "\t\t\t\t}", "\t\t\t\ttd.Methods = append(td.Methods, md)", "\t\t\t}", "\t\t\tout.Types = append(out.Types, td)", "\t\tcase *types.Func:", "\t\t\tsig := o.Type().(*types.Signature)", "\t\t\tfd := FuncDecl{Name: name, Variadic: sig.Variadic()}", "\t\t\tfor i := 0; i < sig.Params().Len(); i++ {", "\t\t\t\tp := sig.Params().At(i)", "\t\t\t\tfd.Params = append(fd.Params, ParamDecl{Name: p.Name(), Type: typeStr(p.Type())})", "\t\t\t}", "\t\t\tfor i := 0; i < sig.Results().Len(); i++ {", "\t\t\t\tr := sig.Results().At(i)", "\t\t\t\tfd.Results = append(fd.Results, ParamDecl{Name: r.Name(), Type: typeStr(r.Type())})", "\t\t\t}", "\t\t\tout.Funcs = append(out.Funcs, fd)", "\t\tcase *types.Var:", "\t\t\tout.Vars = append(out.Vars, VarDecl{Name: name, Type: typeStr(o.Type())})", "\t\tcase *types.Const:", "\t\t\tout.Consts = append(out.Consts, ConstDecl{Name: name, Type: typeStr(o.Type()), Value: o.Val().String()})", "\t\t}", "\t}", "", "\tenc := json.NewEncoder(os.Stdout)", "\tenc.SetIndent(\"\", \"  \")", "\tif err := enc.Encode(out); err != nil {", "\t\tfmt.Fprintf(os.Stderr, \"encode error: %v\\n\", err)", "\t\tos.Exit(1)", "\t}", "}"})
}

var Ffi_WrapperGen_Pure = map[string]any{"Tag": 0, "SkyName": "Pure"}

var Ffi_WrapperGen_Fallible = map[string]any{"Tag": 1, "SkyName": "Fallible"}

var Ffi_WrapperGen_Effectful = map[string]any{"Tag": 2, "SkyName": "Effectful"}

func Ffi_WrapperGen_GenerateWrappers(pkgName any, inspectJson any, outDir any) any {
	return func() any { safePkg := sky_call2(sky_stringReplace("-"), "_", sky_call2(sky_stringReplace("/"), "_", sky_call2(sky_stringReplace("."), "_", pkgName))); _ = safePkg; wrapperDir := sky_concat(outDir, "/sky_wrappers"); _ = wrapperDir; sky_fileMkdirAll(wrapperDir); funcs := Ffi_WrapperGen_ExtractFunctions(inspectJson); _ = funcs; methods := Ffi_WrapperGen_ExtractMethods(inspectJson); _ = methods; fieldAccessors := Ffi_WrapperGen_ExtractFieldAccessors(safePkg, inspectJson); _ = fieldAccessors; varAccessors := Ffi_WrapperGen_ExtractVarAccessors(pkgName, inspectJson); _ = varAccessors; wrapperCode := Ffi_WrapperGen_GenerateWrapperFile(safePkg, pkgName, funcs, methods, sky_call(sky_listAppend(fieldAccessors), varAccessors)); _ = wrapperCode; sky_call(sky_fileWrite(sky_concat(wrapperDir, sky_concat("/", sky_concat(safePkg, ".go")))), wrapperCode); return SkyOk(wrapperCode) }()
}

func Ffi_WrapperGen_ClassifyFunc(results any, funcName any) any {
	return func() any { return func() any { __subject := results; if len(sky_asList(__subject)) == 0 { return map[string]any{"Tag": 2, "SkyName": "Effectful"} };  if len(sky_asList(__subject)) > 0 { single := sky_asList(__subject)[0]; _ = single; return func() any { if sky_asBool(sky_equal(single, "error")) { return map[string]any{"Tag": 1, "SkyName": "Fallible"} }; return func() any { if sky_asBool(Ffi_WrapperGen_IsEffectfulName(funcName)) { return map[string]any{"Tag": 2, "SkyName": "Effectful"} }; return map[string]any{"Tag": 0, "SkyName": "Pure"} }() }() };  if true { return func() any { lastResult := func() any { return func() any { __subject := sky_listReverse(results); if len(sky_asList(__subject)) > 0 { last := sky_asList(__subject)[0]; _ = last; return last };  if len(sky_asList(__subject)) == 0 { return "" };  return nil }() }(); _ = lastResult; return func() any { if sky_asBool(sky_equal(lastResult, "error")) { return map[string]any{"Tag": 1, "SkyName": "Fallible"} }; return map[string]any{"Tag": 2, "SkyName": "Effectful"} }() }() };  return nil }() }()
}

func Ffi_WrapperGen_IsEffectfulName(name any) any {
	return sky_asBool(sky_call(sky_stringStartsWith("Read"), name)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Write"), name)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Load"), name)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Save"), name)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Create"), name)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Delete"), name)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Open"), name)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Close"), name)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Send"), name)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Fetch"), name)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Set"), name)) || sky_asBool(sky_call(sky_stringStartsWith("Print"), name))))))))))))
}

func Ffi_WrapperGen_GenerateWrapperFile(safePkg any, pkgName any, funcs any, methods any, fieldAccessors any) any {
	return func() any { safeFuncs := sky_call(sky_listFilter(func(f any) any { return Ffi_WrapperGen_CanWrapFuncForPkg(pkgName, f) }), funcs); _ = safeFuncs; safeMethods := sky_call(sky_listFilter(func(m any) any { return Ffi_WrapperGen_CanWrapMethodForPkg(pkgName, m) }), methods); _ = safeMethods; shortPkg := Ffi_WrapperGen_ShortPkgName(pkgName); _ = shortPkg; hasFields := sky_not(sky_listIsEmpty(fieldAccessors)); _ = hasFields; reflectImport := func() any { if sky_asBool(hasFields) { return "\t_ffi_reflect \"reflect\"\n" }; return "" }(); _ = reflectImport; parts := sky_call(sky_stringSplit("/"), pkgName); _ = parts; lastPart := func() any { return func() any { __subject := sky_listReverse(parts); if len(sky_asList(__subject)) > 0 { l := sky_asList(__subject)[0]; _ = l; return l };  if len(sky_asList(__subject)) == 0 { return "" };  return nil }() }(); _ = lastPart; firstPart := func() any { return func() any { __subject := parts; if len(sky_asList(__subject)) > 0 { f := sky_asList(__subject)[0]; _ = f; return f };  if len(sky_asList(__subject)) == 0 { return "" };  return nil }() }(); _ = firstPart; isExternal := sky_call(sky_stringContains("."), firstPart); _ = isExternal; candidateParent := sky_call(sky_stringJoin("/"), sky_listReverse(sky_call(sky_listDrop(1), sky_listReverse(parts)))); _ = candidateParent; parentParts := sky_call(sky_stringSplit("/"), candidateParent); _ = parentParts; isSubPkg := sky_asBool(isExternal) && sky_asBool(sky_asBool(sky_asInt(sky_listLength(parentParts)) >= sky_asInt(3)) && sky_asBool(sky_not(sky_asBool(sky_call(sky_stringStartsWith("v"), lastPart)) && sky_asBool(sky_asInt(sky_stringLength(lastPart)) <= sky_asInt(3))))); _ = isSubPkg; parentPkg := func() any { if sky_asBool(isSubPkg) { return candidateParent }; return "" }(); _ = parentPkg; hasParentTypeRef := func() any { if sky_asBool(sky_stringIsEmpty(parentPkg)) { return false }; return func() any { parentShort := Ffi_WrapperGen_ShortPkgName(parentPkg); _ = parentShort; allTypes := sky_call(sky_listConcatMap(func(f any) any { return sky_call(sky_listAppend(sky_call(sky_listMap(sky_snd), sky_asMap(f)["params"])), sky_asMap(f)["results"]) }), funcs); _ = allTypes; allMethodTypes := sky_call(sky_listConcatMap(func(m any) any { return sky_call(sky_listAppend(sky_call(sky_listMap(sky_snd), sky_asMap(m)["params"])), sky_asMap(m)["results"]) }), methods); _ = allMethodTypes; return sky_call(sky_listAny(func(t any) any { return sky_call(sky_stringContains(sky_concat(parentShort, ".")), t) }), sky_call(sky_listAppend(allTypes), allMethodTypes)) }() }(); _ = hasParentTypeRef; parentImport := func() any { if sky_asBool(sky_asBool(sky_stringIsEmpty(parentPkg)) || sky_asBool(sky_asBool(sky_equal(parentPkg, pkgName)) || sky_asBool(sky_not(hasParentTypeRef)))) { return "" }; return sky_concat("\t", sky_concat(Ffi_WrapperGen_ShortPkgName(parentPkg), sky_concat(" \"", sky_concat(parentPkg, "\"\n")))) }(); _ = parentImport; return sky_call(sky_stringJoin("\n"), []any{"package sky_wrappers", "", "import (", "\t_ffi_fmt \"fmt\"", sky_concat(reflectImport, sky_concat(parentImport, sky_concat("\t", sky_concat(shortPkg, sky_concat(" \"", sky_concat(pkgName, "\"")))))), ")", "", "var _ = _ffi_fmt.Sprintf", "// Auto-generated by Sky compiler", "", sky_call(sky_stringJoin("\n\n"), sky_call(sky_listMap(func(f any) any { return Ffi_WrapperGen_GenerateFuncWrapper(safePkg, pkgName, f) }), safeFuncs)), "", sky_call(sky_stringJoin("\n\n"), sky_call(sky_listMap(func(m any) any { return Ffi_WrapperGen_GenerateMethodWrapper(safePkg, pkgName, m) }), safeMethods)), "", sky_call(sky_stringJoin("\n\n"), fieldAccessors)}) }()
}

func Ffi_WrapperGen_CanWrapFuncForPkg(pkgName any, func_ any) any {
	return func() any { paramCount := sky_listLength(sky_asMap(func_)["params"]); _ = paramCount; paramsOk := sky_call(sky_listAll(func(pair any) any { return func() any { idx := sky_fst(pair); _ = idx; return func() any { p := sky_snd(pair); _ = p; return func() any { if sky_asBool(sky_asBool(sky_asMap(func_)["variadic"]) && sky_asBool(sky_equal(idx, sky_asInt(paramCount) - sky_asInt(1)))) { return true }; return Ffi_WrapperGen_IsSupportedTypeForPkg(pkgName, sky_snd(p)) }() }() }() }), Ffi_WrapperGen_ZipIndex(sky_asMap(func_)["params"], 0)); _ = paramsOk; return sky_asBool(paramsOk) && sky_asBool(sky_asBool(sky_call(sky_listAll(func(r any) any { return Ffi_WrapperGen_IsSupportedTypeForPkg(pkgName, r) }), sky_asMap(func_)["results"])) && sky_asBool(sky_asInt(sky_listLength(sky_asMap(func_)["results"])) <= sky_asInt(2))) }()
}

func Ffi_WrapperGen_ZipIndex(items any, idx any) any {
	return func() any { return func() any { __subject := items; if len(sky_asList(__subject)) == 0 { return []any{} };  if len(sky_asList(__subject)) > 0 { item := sky_asList(__subject)[0]; _ = item; rest := sky_asList(__subject)[1:]; _ = rest; return append([]any{SkyTuple2{V0: idx, V1: item}}, sky_asList(Ffi_WrapperGen_ZipIndex(rest, sky_asInt(idx) + sky_asInt(1)))...) };  return nil }() }()
}

func Ffi_WrapperGen_CanWrapMethodForPkg(pkgName any, method any) any {
	return func() any { mParamCount := sky_listLength(sky_asMap(method)["params"]); _ = mParamCount; mParamsOk := sky_call(sky_listAll(func(pair any) any { return func() any { idx := sky_fst(pair); _ = idx; return func() any { p := sky_snd(pair); _ = p; return func() any { if sky_asBool(sky_asBool(sky_asMap(method)["variadic"]) && sky_asBool(sky_equal(idx, sky_asInt(mParamCount) - sky_asInt(1)))) { return true }; return Ffi_WrapperGen_IsSupportedTypeForPkg(pkgName, sky_snd(p)) }() }() }() }), Ffi_WrapperGen_ZipIndex(sky_asMap(method)["params"], 0)); _ = mParamsOk; return sky_asBool(mParamsOk) && sky_asBool(sky_asBool(sky_call(sky_listAll(func(r any) any { return Ffi_WrapperGen_IsSupportedTypeForPkg(pkgName, r) }), sky_asMap(method)["results"])) && sky_asBool(sky_asInt(sky_listLength(sky_asMap(method)["results"])) <= sky_asInt(2))) }()
}

func Ffi_WrapperGen_IsSupportedTypeForPkg(pkgName any, goType any) any {
	return func() any { if sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("func("), goType)) && sky_asBool(sky_not(Ffi_WrapperGen_IsAdaptableFuncType(goType)))) { return false }; return func() any { if sky_asBool(sky_asBool(sky_call(sky_stringContains("["), goType)) && sky_asBool(sky_asBool(sky_call(sky_stringContains("]"), goType)) && sky_asBool(sky_asBool(sky_not(sky_call(sky_stringStartsWith("["), goType))) && sky_asBool(sky_not(sky_call(sky_stringStartsWith("map[string]interface{}"), goType)))))) { return false }; return func() any { if sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("map["), goType)) && sky_asBool(sky_asBool(!sky_equal(goType, "map[string]interface{}")) && sky_asBool(!sky_equal(goType, "map[string]any")))) { return false }; return func() any { if sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("[]"), goType)) && sky_asBool(sky_asBool(!sky_equal(goType, "[]byte")) && sky_asBool(sky_asBool(!sky_equal(goType, "[]string")) && sky_asBool(!sky_equal(goType, "[]any"))))) { return false }; return func() any { typeParts := sky_call(sky_stringSplit("/"), pkgName); _ = typeParts; typeFirstPart := func() any { return func() any { __subject := typeParts; if len(sky_asList(__subject)) > 0 { f := sky_asList(__subject)[0]; _ = f; return f };  if len(sky_asList(__subject)) == 0 { return "" };  return nil }() }(); _ = typeFirstPart; typeLastPart := func() any { return func() any { __subject := sky_listReverse(typeParts); if len(sky_asList(__subject)) > 0 { l := sky_asList(__subject)[0]; _ = l; return l };  if len(sky_asList(__subject)) == 0 { return "" };  return nil }() }(); _ = typeLastPart; typeIsExternal := sky_call(sky_stringContains("."), typeFirstPart); _ = typeIsExternal; candidateParent2 := sky_call(sky_stringJoin("/"), sky_listReverse(sky_call(sky_listDrop(1), sky_listReverse(typeParts)))); _ = candidateParent2; parentParts2 := sky_call(sky_stringSplit("/"), candidateParent2); _ = parentParts2; typeIsSubPkg := sky_asBool(typeIsExternal) && sky_asBool(sky_asBool(sky_asInt(sky_listLength(parentParts2)) >= sky_asInt(3)) && sky_asBool(sky_not(sky_asBool(sky_call(sky_stringStartsWith("v"), typeLastPart)) && sky_asBool(sky_asInt(sky_stringLength(typeLastPart)) <= sky_asInt(3))))); _ = typeIsSubPkg; parentPkg := func() any { if sky_asBool(typeIsSubPkg) { return candidateParent2 }; return "" }(); _ = parentPkg; return sky_asBool(Ffi_WrapperGen_IsSupportedType(goType)) || sky_asBool(sky_asBool(Ffi_WrapperGen_IsSupportedResultType(goType)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith(sky_concat(pkgName, ".")), goType)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith(sky_concat("*", sky_concat(pkgName, "."))), goType)) || sky_asBool(sky_asBool(sky_asBool(sky_not(sky_stringIsEmpty(parentPkg))) && sky_asBool(sky_call(sky_stringStartsWith(sky_concat(parentPkg, ".")), goType))) || sky_asBool(sky_asBool(sky_not(sky_stringIsEmpty(parentPkg))) && sky_asBool(sky_call(sky_stringStartsWith(sky_concat("*", sky_concat(parentPkg, "."))), goType))))))) }() }() }() }() }()
}

func Ffi_WrapperGen_IsSupportedType(goType any) any {
	return sky_asBool(sky_equal(goType, "string")) || sky_asBool(sky_asBool(sky_equal(goType, "bool")) || sky_asBool(sky_asBool(sky_equal(goType, "int")) || sky_asBool(sky_asBool(sky_equal(goType, "[]byte")) || sky_asBool(sky_asBool(sky_equal(goType, "int8")) || sky_asBool(sky_asBool(sky_equal(goType, "int16")) || sky_asBool(sky_asBool(sky_equal(goType, "int32")) || sky_asBool(sky_asBool(sky_equal(goType, "int64")) || sky_asBool(sky_asBool(sky_equal(goType, "uint")) || sky_asBool(sky_asBool(sky_equal(goType, "uint8")) || sky_asBool(sky_asBool(sky_equal(goType, "uint16")) || sky_asBool(sky_asBool(sky_equal(goType, "uint32")) || sky_asBool(sky_asBool(sky_equal(goType, "uint64")) || sky_asBool(sky_asBool(sky_equal(goType, "float32")) || sky_asBool(sky_asBool(sky_equal(goType, "float64")) || sky_asBool(sky_asBool(sky_equal(goType, "interface{}")) || sky_asBool(sky_asBool(sky_equal(goType, "any")) || sky_asBool(sky_asBool(sky_equal(goType, "error")) || sky_asBool(sky_asBool(sky_equal(goType, "context.Context")) || sky_asBool(sky_asBool(sky_equal(goType, "[]string")) || sky_asBool(sky_asBool(sky_equal(goType, "[]int")) || sky_asBool(sky_asBool(sky_equal(goType, "[]float64")) || sky_asBool(sky_asBool(sky_equal(goType, "[]bool")) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("["), goType)) || sky_asBool(sky_asBool(sky_asBool(sky_call(sky_stringContains("."), goType)) && sky_asBool(sky_not(Ffi_WrapperGen_NeedsExtraImport(goType)))) || sky_asBool(sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("*"), goType)) && sky_asBool(Ffi_WrapperGen_IsSupportedType(sky_call2(sky_stringSlice(1), sky_stringLength(goType), goType)))) || sky_asBool(sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("[]"), goType)) && sky_asBool(Ffi_WrapperGen_IsSupportedType(sky_call2(sky_stringSlice(2), sky_stringLength(goType), goType)))) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("func("), goType)) || sky_asBool(sky_call(sky_stringStartsWith("map["), goType)))))))))))))))))))))))))))))
}

func Ffi_WrapperGen_NeedsExtraImport(goType any) any {
	return func() any { t := func() any { if sky_asBool(sky_call(sky_stringStartsWith("*"), goType)) { return sky_call2(sky_stringSlice(1), sky_stringLength(goType), goType) }; return goType }(); _ = t; return sky_asBool(sky_call(sky_stringContains("/"), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("io."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("hash."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("context."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("sync."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("net."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("crypto."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("encoding."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("reflect."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("testing."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("log."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("regexp."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("mime."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("html."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("text."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("bufio."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("fs."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("os."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("time."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("fmt."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("sort."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("math."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("strings."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("bytes."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("strconv."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("database."), t)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("sql."), t)) || sky_asBool(sky_call(sky_stringStartsWith("unicode."), t)))))))))))))))))))))))))))) }()
}

func Ffi_WrapperGen_IsAdaptableFuncType(goType any) any {
	return sky_asBool(sky_call(sky_stringContains("ResponseWriter"), goType)) && sky_asBool(sky_call(sky_stringContains("Request"), goType))
}

func Ffi_WrapperGen_IsSupportedResultType(goType any) any {
	return sky_asBool(Ffi_WrapperGen_IsSupportedType(goType)) || sky_asBool(sky_equal(goType, "error"))
}

func Ffi_WrapperGen_GenerateFuncWrapper(safePkg any, pkgName any, func_ any) any {
	return func() any { wrapperName := sky_concat("Sky_", sky_concat(safePkg, sky_concat("_", sky_asMap(func_)["name"]))); _ = wrapperName; paramList := sky_call(sky_listIndexedMap(func(i any) any { return func(p any) any { return sky_concat("arg", sky_concat(sky_stringFromInt(i), " any")) } }), sky_asMap(func_)["params"]); _ = paramList; paramStr := sky_call(sky_stringJoin(", "), paramList); _ = paramStr; allArgCasts := sky_call(sky_listIndexedMap(func(i any) any { return func(p any) any { return Ffi_WrapperGen_GenerateArgCast(i, p) } }), sky_asMap(func_)["params"]); _ = allArgCasts; argNames := sky_call(sky_listIndexedMap(func(i any) any { return func(p any) any { return sky_concat("_arg", sky_stringFromInt(i)) } }), sky_asMap(func_)["params"]); _ = argNames; lastParamType := func() any { return func() any { __subject := sky_listReverse(sky_asMap(func_)["params"]); if len(sky_asList(__subject)) > 0 { last := sky_asList(__subject)[0]; _ = last; return sky_snd(last) };  if len(sky_asList(__subject)) == 0 { return "" };  return nil }() }(); _ = lastParamType; canSpreadDirect := sky_asBool(sky_equal(lastParamType, "[]any")) || sky_asBool(sky_asBool(sky_equal(lastParamType, "[]string")) || sky_asBool(sky_asBool(sky_equal(lastParamType, "[]byte")) || sky_asBool(sky_equal(lastParamType, "[]int")))); _ = canSpreadDirect; lastArgIdx := sky_asInt(sky_listLength(argNames)) - sky_asInt(1); _ = lastArgIdx; lastArgName := sky_concat("_arg", sky_stringFromInt(lastArgIdx)); _ = lastArgName; argNamesForCall := func() any { if sky_asBool(sky_asBool(sky_asMap(func_)["variadic"]) && sky_asBool(sky_not(sky_listIsEmpty(argNames)))) { return func() any { allButLast := sky_call(sky_listTake(lastArgIdx), argNames); _ = allButLast; return func() any { if sky_asBool(canSpreadDirect) { return sky_call(sky_listAppend(allButLast), []any{sky_concat(lastArgName, "...")}) }; return allButLast }() }() }; return argNames }(); _ = argNamesForCall; argCasts := func() any { if sky_asBool(sky_asBool(sky_asMap(func_)["variadic"]) && sky_asBool(sky_asBool(sky_not(canSpreadDirect)) && sky_asBool(sky_not(sky_listIsEmpty(allArgCasts))))) { return sky_call(sky_listTake(sky_asInt(sky_listLength(allArgCasts)) - sky_asInt(1)), allArgCasts) }; return allArgCasts }(); _ = argCasts; castStr := sky_call(sky_stringJoin("\n\t"), argCasts); _ = castStr; goCall := sky_concat(Ffi_WrapperGen_ShortPkgName(pkgName), sky_concat(".", sky_concat(sky_asMap(func_)["name"], sky_concat("(", sky_concat(sky_call(sky_stringJoin(", "), argNamesForCall), ")"))))); _ = goCall; kind := Ffi_WrapperGen_ClassifyFunc(sky_asMap(func_)["results"], sky_asMap(func_)["name"]); _ = kind; returnCode := Ffi_WrapperGen_WrapReturn(kind, sky_asMap(func_)["results"], goCall); _ = returnCode; return sky_concat("func ", sky_concat(wrapperName, sky_concat("(", sky_concat(paramStr, sky_concat(") any {\n\t", sky_concat(castStr, sky_concat("\n\t", sky_concat(returnCode, "\n}")))))))) }()
}

func Ffi_WrapperGen_GenerateMethodWrapper(safePkg any, pkgName any, method any) any {
	return func() any { wrapperName := sky_concat("Sky_", sky_concat(safePkg, sky_concat("_", sky_concat(sky_asMap(method)["typeName"], sky_asMap(method)["name"])))); _ = wrapperName; paramList := append([]any{"receiver any"}, sky_asList(sky_call(sky_listIndexedMap(func(i any) any { return func(p any) any { return sky_concat("arg", sky_concat(sky_stringFromInt(i), " any")) } }), sky_asMap(method)["params"]))...); _ = paramList; paramStr := sky_call(sky_stringJoin(", "), paramList); _ = paramStr; receiverCast := func() any { if sky_asBool(sky_asMap(method)["isInterface"]) { return sky_concat("_receiver := receiver.(", sky_concat(Ffi_WrapperGen_ShortPkgName(pkgName), sky_concat(".", sky_concat(sky_asMap(method)["typeName"], ")")))) }; return sky_concat("_receiver := receiver.(*", sky_concat(Ffi_WrapperGen_ShortPkgName(pkgName), sky_concat(".", sky_concat(sky_asMap(method)["typeName"], ")")))) }(); _ = receiverCast; allMethodArgCasts := sky_call(sky_listIndexedMap(func(i any) any { return func(p any) any { return Ffi_WrapperGen_GenerateArgCast(i, p) } }), sky_asMap(method)["params"]); _ = allMethodArgCasts; argNames := sky_call(sky_listIndexedMap(func(i any) any { return func(p any) any { return sky_concat("_arg", sky_stringFromInt(i)) } }), sky_asMap(method)["params"]); _ = argNames; mLastParamType := func() any { return func() any { __subject := sky_listReverse(sky_asMap(method)["params"]); if len(sky_asList(__subject)) > 0 { last := sky_asList(__subject)[0]; _ = last; return sky_snd(last) };  if len(sky_asList(__subject)) == 0 { return "" };  return nil }() }(); _ = mLastParamType; mCanSpreadDirect := sky_asBool(sky_equal(mLastParamType, "[]any")) || sky_asBool(sky_asBool(sky_equal(mLastParamType, "[]string")) || sky_asBool(sky_asBool(sky_equal(mLastParamType, "[]byte")) || sky_asBool(sky_equal(mLastParamType, "[]int")))); _ = mCanSpreadDirect; mLastArgIdx := sky_asInt(sky_listLength(argNames)) - sky_asInt(1); _ = mLastArgIdx; mLastArgName := sky_concat("_arg", sky_stringFromInt(mLastArgIdx)); _ = mLastArgName; argNamesForCall := func() any { if sky_asBool(sky_asBool(sky_asMap(method)["variadic"]) && sky_asBool(sky_not(sky_listIsEmpty(argNames)))) { return func() any { allButLast := sky_call(sky_listTake(mLastArgIdx), argNames); _ = allButLast; return func() any { if sky_asBool(mCanSpreadDirect) { return sky_call(sky_listAppend(allButLast), []any{sky_concat(mLastArgName, "...")}) }; return allButLast }() }() }; return argNames }(); _ = argNamesForCall; methodArgCasts := func() any { if sky_asBool(sky_asBool(sky_asMap(method)["variadic"]) && sky_asBool(sky_asBool(sky_not(mCanSpreadDirect)) && sky_asBool(sky_not(sky_listIsEmpty(allMethodArgCasts))))) { return sky_call(sky_listTake(sky_asInt(sky_listLength(allMethodArgCasts)) - sky_asInt(1)), allMethodArgCasts) }; return allMethodArgCasts }(); _ = methodArgCasts; castStr := sky_call(sky_stringJoin("\n\t"), append([]any{receiverCast}, sky_asList(methodArgCasts)...)); _ = castStr; goCall := sky_concat("_receiver.", sky_concat(sky_asMap(method)["name"], sky_concat("(", sky_concat(sky_call(sky_stringJoin(", "), argNamesForCall), ")")))); _ = goCall; kind := Ffi_WrapperGen_ClassifyFunc(sky_asMap(method)["results"], sky_asMap(method)["name"]); _ = kind; returnCode := Ffi_WrapperGen_WrapReturn(kind, sky_asMap(method)["results"], goCall); _ = returnCode; return sky_concat("func ", sky_concat(wrapperName, sky_concat("(", sky_concat(paramStr, sky_concat(") any {\n\t", sky_concat(castStr, sky_concat("\n\t", sky_concat(returnCode, "\n}")))))))) }()
}

func Ffi_WrapperGen_GenerateArgCast(idx any, param any) any {
	return func() any { goType := sky_snd(param); _ = goType; argName := sky_concat("arg", sky_stringFromInt(idx)); _ = argName; castExpr := Ffi_TypeMapper_GoTypeToCast(goType, argName); _ = castExpr; return sky_concat("_arg", sky_concat(sky_stringFromInt(idx), sky_concat(" := ", castExpr))) }()
}

func Ffi_WrapperGen_WrapReturn(kind any, results any, goCall any) any {
	return func() any { return func() any { __subject := kind; if sky_asMap(__subject)["SkyName"] == "Pure" { return Ffi_WrapperGen_WrapPureReturn(results, goCall) };  if sky_asMap(__subject)["SkyName"] == "Fallible" { return Ffi_WrapperGen_WrapFallibleReturn(results, goCall) };  if sky_asMap(__subject)["SkyName"] == "Effectful" { return Ffi_WrapperGen_WrapEffectfulReturn(results, goCall) };  return nil }() }()
}

func Ffi_WrapperGen_WrapPureReturn(results any, goCall any) any {
	return func() any { return func() any { __subject := results; if len(sky_asList(__subject)) == 0 { return sky_concat(goCall, "\n\treturn struct{}{}") };  if len(sky_asList(__subject)) > 0 { single := sky_asList(__subject)[0]; _ = single; return func() any { if sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("["), single)) && sky_asBool(sky_call(sky_stringContains("]"), single))) { return sky_concat("result := ", sky_concat(goCall, "\n\treturn result[:]")) }; return sky_concat("return ", goCall) }() };  if true { return func() any { n := sky_listLength(results); _ = n; vars := sky_call(sky_stringJoin(", "), sky_call(sky_listIndexedMap(func(i any) any { return func(_ any) any { return sky_concat("_r", sky_stringFromInt(i)) } }), results)); _ = vars; return sky_concat(vars, sky_concat(" := ", sky_concat(goCall, sky_concat("\n\t", sky_concat("return SkyTuple", sky_concat(sky_stringFromInt(n), sky_concat("{", sky_concat(vars, "}")))))))) }() };  return nil }() }()
}

func Ffi_WrapperGen_WrapFallibleReturn(results any, goCall any) any {
	return func() any { hasValueReturn := sky_asInt(sky_listLength(results)) > sky_asInt(1); _ = hasValueReturn; return func() any { if sky_asBool(hasValueReturn) { return sky_concat("return func() any {\n\t\t", sky_concat("defer func() { recover() }()\n\t\t", sky_concat("result, err := ", sky_concat(goCall, sky_concat("\n\t\t", sky_concat("if err != nil { return SkyErr(err.Error()) }\n\t\t", "return SkyOk(result)\n\t}")))))) }; return sky_concat("return func() any {\n\t\t", sky_concat("defer func() { recover() }()\n\t\t", sky_concat("err := ", sky_concat(goCall, sky_concat("\n\t\t", sky_concat("if err != nil { return SkyErr(err.Error()) }\n\t\t", "return SkyOk(struct{}{})\n\t}")))))) }() }()
}

func Ffi_WrapperGen_WrapEffectfulReturn(results any, goCall any) any {
	return func() any { return func() any { __subject := results; if len(sky_asList(__subject)) == 0 { return sky_concat("return func() any {\n\t\t", sky_concat("defer func() { recover() }()\n\t\t", sky_concat(goCall, sky_concat("\n\t\t", "return SkyOk(struct{}{})\n\t}")))) };  if len(sky_asList(__subject)) > 0 { return sky_concat("return func() any {\n\t\t", sky_concat("defer func() { recover() }()\n\t\t", sky_concat("result := ", sky_concat(goCall, sky_concat("\n\t\t", "return SkyOk(result)\n\t}"))))) };  if true { return func() any { n := sky_listLength(results); _ = n; vars := sky_call(sky_stringJoin(", "), sky_call(sky_listIndexedMap(func(i any) any { return func(_ any) any { return sky_concat("_r", sky_stringFromInt(i)) } }), results)); _ = vars; return sky_concat("return func() any {\n\t\t", sky_concat("defer func() { recover() }()\n\t\t", sky_concat(vars, sky_concat(" := ", sky_concat(goCall, sky_concat("\n\t\t", sky_concat("return SkyOk(SkyTuple", sky_concat(sky_stringFromInt(n), sky_concat("{", sky_concat(vars, "})\n\t}")))))))))) }() };  return nil }() }()
}

func Ffi_WrapperGen_ShortPkgName(pkgPath any) any {
	return func() any { return func() any { __subject := sky_listReverse(sky_call(sky_stringSplit("/"), pkgPath)); if len(sky_asList(__subject)) > 0 { last := sky_asList(__subject)[0]; _ = last; return last };  if len(sky_asList(__subject)) == 0 { return pkgPath };  return nil }() }()
}

func Ffi_WrapperGen_ExtractFunctions(json any) any {
	return func() any { funcsArrayStr := Lsp_JsonRpc_JsonGetArrayRaw("funcs", json); _ = funcsArrayStr; elements := Lsp_JsonRpc_JsonSplitArray(funcsArrayStr); _ = elements; return sky_call(sky_listFilterMap(Ffi_WrapperGen_ParseFuncEntry), elements) }()
}

func Ffi_WrapperGen_ParseFuncEntry(json any) any {
	return func() any { name := Lsp_JsonRpc_JsonGetString("name", json); _ = name; return func() any { if sky_asBool(sky_stringIsEmpty(name)) { return SkyNothing() }; return func() any { paramsArrayStr := Lsp_JsonRpc_JsonGetArrayRaw("params", json); _ = paramsArrayStr; paramElements := Lsp_JsonRpc_JsonSplitArray(paramsArrayStr); _ = paramElements; params := sky_call(sky_listMap(Ffi_WrapperGen_ParseParamPair), paramElements); _ = params; resultsArrayStr := Lsp_JsonRpc_JsonGetArrayRaw("results", json); _ = resultsArrayStr; resultElements := Lsp_JsonRpc_JsonSplitArray(resultsArrayStr); _ = resultElements; results := sky_call(sky_listMap(func(r any) any { return Lsp_JsonRpc_JsonGetString("type", r) }), resultElements); _ = results; variadic := Lsp_JsonRpc_JsonGetBool("variadic", json); _ = variadic; return SkyJust(map[string]any{"name": name, "params": params, "results": results, "variadic": variadic}) }() }() }()
}

func Ffi_WrapperGen_ParseParamPair(json any) any {
	return SkyTuple2{V0: Lsp_JsonRpc_JsonGetString("name", json), V1: Lsp_JsonRpc_JsonGetString("type", json)}
}

func Ffi_WrapperGen_ExtractMethods(json any) any {
	return func() any { typesArrayStr := Lsp_JsonRpc_JsonGetArrayRaw("types", json); _ = typesArrayStr; typeElements := Lsp_JsonRpc_JsonSplitArray(typesArrayStr); _ = typeElements; return sky_call(sky_listConcatMap(Ffi_WrapperGen_ExtractMethodsFromType), typeElements) }()
}

func Ffi_WrapperGen_ExtractFieldAccessors(safePkg any, json any) any {
	return func() any { typesArrayStr := Lsp_JsonRpc_JsonGetArrayRaw("types", json); _ = typesArrayStr; typeElements := Lsp_JsonRpc_JsonSplitArray(typesArrayStr); _ = typeElements; return sky_call(sky_listConcatMap(func(t any) any { return Ffi_WrapperGen_ExtractFieldsFromType(safePkg, t) }), typeElements) }()
}

func Ffi_WrapperGen_ExtractFieldsFromType(safePkg any, typeJson any) any {
	return func() any { typeName := Lsp_JsonRpc_JsonGetString("name", typeJson); _ = typeName; kind := Lsp_JsonRpc_JsonGetString("kind", typeJson); _ = kind; fieldsArrayStr := Lsp_JsonRpc_JsonGetArrayRaw("fields", typeJson); _ = fieldsArrayStr; fieldElements := Lsp_JsonRpc_JsonSplitArray(fieldsArrayStr); _ = fieldElements; return func() any { if sky_asBool(sky_asBool(sky_equal(kind, "struct")) && sky_asBool(sky_not(Ffi_WrapperGen_IsGenericType(typeJson)))) { return sky_call(sky_listFilterMap(func(f any) any { return Ffi_WrapperGen_GenerateFieldAccessor(safePkg, typeName, f) }), fieldElements) }; return []any{} }() }()
}

func Ffi_WrapperGen_GenerateFieldAccessor(safePkg any, typeName any, fieldJson any) any {
	return func() any { fieldName := Lsp_JsonRpc_JsonGetString("name", fieldJson); _ = fieldName; fieldType := Lsp_JsonRpc_JsonGetString("type", fieldJson); _ = fieldType; return func() any { if sky_asBool(sky_asBool(sky_stringIsEmpty(fieldName)) || sky_asBool(sky_not(Ffi_WrapperGen_IsSafeFieldType(fieldType)))) { return SkyNothing() }; return SkyJust(sky_concat("func Sky_", sky_concat(safePkg, sky_concat("_FIELD_", sky_concat(typeName, sky_concat("_", sky_concat(fieldName, sky_concat("(receiver any) any {\n", sky_concat("\tv := _ffi_reflect.ValueOf(receiver)\n", sky_concat("\tfor v.Kind() == _ffi_reflect.Ptr { v = v.Elem() }\n", sky_concat("\tif v.Kind() == _ffi_reflect.Struct {\n", sky_concat("\t\tf := v.FieldByName(\"", sky_concat(fieldName, sky_concat("\")\n", sky_concat("\t\tif f.IsValid() { return f.Interface() }\n", sky_concat("\t}\n", sky_concat("\treturn nil\n", "}"))))))))))))))))) }() }()
}

func Ffi_WrapperGen_IsSafeFieldType(_ any) any {
	return true
}

func Ffi_WrapperGen_VarTypeCast(goType any) any {
	return func() any { if sky_asBool(sky_equal(goType, "string")) { return "sky_asString" }; return func() any { if sky_asBool(sky_equal(goType, "int")) { return "sky_asInt" }; return func() any { if sky_asBool(sky_equal(goType, "bool")) { return "sky_asBool" }; return func() any { if sky_asBool(sky_equal(goType, "float64")) { return "sky_asFloat" }; return "sky_asString" }() }() }() }()
}

func Ffi_WrapperGen_ExtractVarAccessors(pkgName any, json any) any {
	return func() any { varsArrayStr := Lsp_JsonRpc_JsonGetArrayRaw("vars", json); _ = varsArrayStr; varElements := Lsp_JsonRpc_JsonSplitArray(varsArrayStr); _ = varElements; safePkg := sky_call2(sky_stringReplace("-"), "_", sky_call2(sky_stringReplace("/"), "_", sky_call2(sky_stringReplace("."), "_", pkgName))); _ = safePkg; shortPkg := Ffi_WrapperGen_ShortPkgName(pkgName); _ = shortPkg; return sky_call(sky_listFilterMap(func(v any) any { return Ffi_WrapperGen_GenerateVarAccessor(safePkg, shortPkg, v) }), varElements) }()
}

func Ffi_WrapperGen_GenerateVarAccessor(safePkg any, shortPkg any, varJson any) any {
	return func() any { name := Lsp_JsonRpc_JsonGetString("name", varJson); _ = name; varType := Lsp_JsonRpc_JsonGetString("type", varJson); _ = varType; return func() any { if sky_asBool(sky_stringIsEmpty(name)) { return SkyNothing() }; return func() any { if sky_asBool(sky_equal(varType, "[]string")) { return SkyJust(sky_concat("func Sky_", sky_concat(safePkg, sky_concat("_", sky_concat(name, sky_concat("() any {\n", sky_concat("\tsrc := ", sky_concat(shortPkg, sky_concat(".", sky_concat(name, sky_concat("\n", sky_concat("\tresult := make([]any, len(src))\n", sky_concat("\tfor i, v := range src { result[i] = v }\n", sky_concat("\treturn result\n", "}")))))))))))))) }; return func() any { if sky_asBool(sky_equal(varType, "error")) { return SkyNothing() }; return func() any { if sky_asBool(sky_equal(varType, "string")) { return SkyJust(sky_concat("func Sky_", sky_concat(safePkg, sky_concat("_", sky_concat(name, sky_concat("() any {\n", sky_concat("\treturn ", sky_concat(shortPkg, sky_concat(".", sky_concat(name, sky_concat("\n", sky_concat("}\n\n", sky_concat("func Sky_", sky_concat(safePkg, sky_concat("_Set", sky_concat(name, sky_concat("(v any) any {\n", sky_concat("\t", sky_concat(shortPkg, sky_concat(".", sky_concat(name, sky_concat(" = sky_asString(v)\n", sky_concat("\treturn struct{}{}\n", "}"))))))))))))))))))))))) }; return SkyJust(sky_concat("func Sky_", sky_concat(safePkg, sky_concat("_", sky_concat(name, sky_concat("() any {\n", sky_concat("\treturn ", sky_concat(shortPkg, sky_concat(".", sky_concat(name, sky_concat("\n", "}"))))))))))) }() }() }() }() }()
}

func Ffi_WrapperGen_IsGenericType(typeJson any) any {
	return func() any { fieldsStr := Lsp_JsonRpc_JsonGetArrayRaw("fields", typeJson); _ = fieldsStr; fields := Lsp_JsonRpc_JsonSplitArray(fieldsStr); _ = fields; fieldTypes := sky_call(sky_listMap(func(f any) any { return Lsp_JsonRpc_JsonGetString("type", f) }), fields); _ = fieldTypes; return sky_call(sky_listAny(func(t any) any { return sky_asBool(sky_equal(sky_stringLength(t), 1)) && sky_asBool(isUpperStart(t)) }), fieldTypes) }()
}

func Ffi_WrapperGen_ExtractMethodsFromType(typeJson any) any {
	return func() any { typeName := Lsp_JsonRpc_JsonGetString("name", typeJson); _ = typeName; typeKind := Lsp_JsonRpc_JsonGetString("kind", typeJson); _ = typeKind; methodsArrayStr := Lsp_JsonRpc_JsonGetArrayRaw("methods", typeJson); _ = methodsArrayStr; methodElements := Lsp_JsonRpc_JsonSplitArray(methodsArrayStr); _ = methodElements; return func() any { if sky_asBool(Ffi_WrapperGen_IsGenericType(typeJson)) { return []any{} }; return sky_call(sky_listFilterMap(func(m any) any { return Ffi_WrapperGen_ParseMethodEntry(typeName, sky_equal(typeKind, "interface"), m) }), methodElements) }() }()
}

func Ffi_WrapperGen_ParseMethodEntry(typeName any, isIface any, json any) any {
	return func() any { name := Lsp_JsonRpc_JsonGetString("name", json); _ = name; return func() any { if sky_asBool(sky_stringIsEmpty(name)) { return SkyNothing() }; return func() any { paramsArrayStr := Lsp_JsonRpc_JsonGetArrayRaw("params", json); _ = paramsArrayStr; paramElements := Lsp_JsonRpc_JsonSplitArray(paramsArrayStr); _ = paramElements; params := sky_call(sky_listMap(Ffi_WrapperGen_ParseParamPair), paramElements); _ = params; resultsArrayStr := Lsp_JsonRpc_JsonGetArrayRaw("results", json); _ = resultsArrayStr; resultElements := Lsp_JsonRpc_JsonSplitArray(resultsArrayStr); _ = resultElements; results := sky_call(sky_listMap(func(r any) any { return Lsp_JsonRpc_JsonGetString("type", r) }), resultElements); _ = results; variadic := Lsp_JsonRpc_JsonGetBool("variadic", json); _ = variadic; return SkyJust(map[string]any{"typeName": typeName, "name": name, "params": params, "results": results, "variadic": variadic, "isInterface": isIface}) }() }() }()
}

func main() {
	sky_runMainTask(func() any { command := func() any { return func() any { __subject := sky_processGetArg(1); if sky_asSkyMaybe(__subject).SkyName == "Just" { cmd := sky_asSkyMaybe(__subject).JustValue; _ = cmd; return cmd };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "help" };  return nil }() }(); _ = command; return runCommand(command) }())
}

func runCommand(command any) any {
	return func() any { if sky_asBool(sky_equal(command, "build")) { return cmdBuild(struct{}{}) }; return func() any { if sky_asBool(sky_equal(command, "run")) { return cmdRun(struct{}{}) }; return func() any { if sky_asBool(sky_equal(command, "check")) { return cmdCheck(struct{}{}) }; return func() any { if sky_asBool(sky_equal(command, "fmt")) { return cmdFmt(struct{}{}) }; return func() any { if sky_asBool(sky_equal(command, "clean")) { return cmdClean(struct{}{}) }; return func() any { if sky_asBool(sky_equal(command, "lsp")) { return Lsp_Server_StartServer(struct{}{}) }; return func() any { if sky_asBool(sky_equal(command, "add")) { return cmdAdd(struct{}{}) }; return func() any { if sky_asBool(sky_equal(command, "install")) { return cmdInstall(struct{}{}) }; return func() any { if sky_asBool(sky_equal(command, "remove")) { return cmdRemove(struct{}{}) }; return func() any { if sky_asBool(sky_equal(command, "update")) { return cmdUpdate(struct{}{}) }; return func() any { if sky_asBool(sky_equal(command, "upgrade")) { return cmdUpgrade(struct{}{}) }; return func() any { if sky_asBool(sky_asBool(sky_equal(command, "version")) || sky_asBool(sky_asBool(sky_equal(command, "--version")) || sky_asBool(sky_equal(command, "-v")))) { return sky_println(sky_concat("sky ", skyVersion)) }; return cmdHelp(struct{}{}) }() }() }() }() }() }() }() }() }() }() }() }()
}

func cmdBuild(_ any) any {
	return func() any { entryFile := func() any { return func() any { __subject := sky_processGetArg(2); if sky_asSkyMaybe(__subject).SkyName == "Just" { f := sky_asSkyMaybe(__subject).JustValue; _ = f; return f };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "src/Main.sky" };  return nil }() }(); _ = entryFile; outDir := "sky-out"; _ = outDir; return func() any { return func() any { __subject := Compiler_Pipeline_Compile(entryFile, outDir); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { sky_println(sky_concat("Error: ", e)); return sky_processExit(1) }() };  if sky_asSkyResult(__subject).SkyName == "Ok" { code := sky_asSkyResult(__subject).OkValue; _ = code; return func() any { goModPath := sky_concat(outDir, "/go.mod"); _ = goModPath; sky_call(sky_fileWrite(goModPath), "module sky-app\n\ngo 1.21.0\n"); hasFfiWrappers := func() any { return func() any { __subject := sky_call(sky_processRun("sh"), []any{"-c", sky_concat("ls ", sky_concat(outDir, "/sky_*.go 2>/dev/null | head -1"))}); if sky_asSkyResult(__subject).SkyName == "Ok" { output := sky_asSkyResult(__subject).OkValue; _ = output; return sky_not(sky_stringIsEmpty(sky_stringTrim(output))) };  if sky_asSkyResult(__subject).SkyName == "Err" { return false };  return nil }() }(); _ = hasFfiWrappers; func() any { if sky_asBool(hasFfiWrappers) { return func() any { sky_println("Running go mod tidy..."); sky_call(sky_processRun("sh"), []any{"-c", sky_concat("cd ", sky_concat(outDir, " && grep -h '\"' sky_*.go 2>/dev/null | grep -v '_ffi_\\|sky_wrappers\\|package\\|//' | sed 's/.*\"\\(.*\\)\".*/\\1/' | sort -u | while read pkg; do go get \"$pkg\" 2>/dev/null; done && go mod tidy 2>&1"))}); return SkyOk("") }() }; return SkyOk("") }(); sky_println("Running go build..."); buildResult := sky_call(sky_processRun("sh"), []any{"-c", sky_concat("cd ", sky_concat(outDir, sky_concat(" && go build -gcflags=all=-l -ldflags='-X main.skyVersion=", sky_concat(skyVersion, "' -o app"))))}); _ = buildResult; return func() any { return func() any { __subject := buildResult; if sky_asSkyResult(__subject).SkyName == "Ok" { return sky_println(sky_concat("Build complete: ", sky_concat(outDir, "/app"))) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { sky_println(sky_concat("go build failed: ", sky_errorToString(e))); return sky_processExit(1) }() };  return nil }() }() }() };  return nil }() }() }()
}

func cmdRun(_ any) any {
	return func() any { cmdBuild(struct{}{}); runResult := sky_call(sky_processRun("sh"), []any{"-c", "./sky-out/app"}); _ = runResult; return func() any { return func() any { __subject := runResult; if sky_asSkyResult(__subject).SkyName == "Ok" { output := sky_asSkyResult(__subject).OkValue; _ = output; return sky_println(output) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { sky_println(sky_errorToString(e)); return sky_processExit(1) }() };  return nil }() }() }()
}

func cmdCheck(_ any) any {
	return func() any { entryFile := func() any { return func() any { __subject := sky_processGetArg(2); if sky_asSkyMaybe(__subject).SkyName == "Just" { f := sky_asSkyMaybe(__subject).JustValue; _ = f; return f };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "src/Main.sky" };  return nil }() }(); _ = entryFile; return func() any { return func() any { __subject := sky_fileRead(entryFile); if sky_asSkyResult(__subject).SkyName == "Err" { readErr := sky_asSkyResult(__subject).ErrValue; _ = readErr; return func() any { sky_println(sky_concat("Cannot read: ", entryFile)); return sky_processExit(1) }() };  if sky_asSkyResult(__subject).SkyName == "Ok" { source := sky_asSkyResult(__subject).OkValue; _ = source; return func() any { lexResult := Compiler_Lexer_Lex(source); _ = lexResult; return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { sky_println(sky_concat("Parse error: ", e)); return sky_processExit(1) }() };  if sky_asSkyResult(__subject).SkyName == "Ok" { mod := sky_asSkyResult(__subject).OkValue; _ = mod; return func() any { stdlibEnv := Compiler_Resolver_BuildStdlibEnv(); _ = stdlibEnv; checkResult := Compiler_Checker_CheckModule(mod, SkyJust(stdlibEnv)); _ = checkResult; return func() any { return func() any { __subject := checkResult; if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return func() any { sky_call(sky_listMap(func(d any) any { return sky_println(sky_concat("  ", sky_concat(sky_asMap(d)["name"], sky_concat(" : ", sky_asMap(d)["prettyType"])))) }), sky_asMap(result)["declarations"]); return sky_println(sky_concat("Type check passed: ", entryFile)) }() };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { sky_println(sky_concat("Type error: ", e)); return sky_processExit(1) }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }()
}

func isGoStdlib(name any) any {
	return sky_asBool(sky_equal(name, "archive")) || sky_asBool(sky_asBool(sky_equal(name, "bufio")) || sky_asBool(sky_asBool(sky_equal(name, "bytes")) || sky_asBool(sky_asBool(sky_equal(name, "compress")) || sky_asBool(sky_asBool(sky_equal(name, "container")) || sky_asBool(sky_asBool(sky_equal(name, "context")) || sky_asBool(sky_asBool(sky_equal(name, "crypto")) || sky_asBool(sky_asBool(sky_equal(name, "database")) || sky_asBool(sky_asBool(sky_equal(name, "debug")) || sky_asBool(sky_asBool(sky_equal(name, "embed")) || sky_asBool(sky_asBool(sky_equal(name, "encoding")) || sky_asBool(sky_asBool(sky_equal(name, "errors")) || sky_asBool(sky_asBool(sky_equal(name, "flag")) || sky_asBool(sky_asBool(sky_equal(name, "fmt")) || sky_asBool(sky_asBool(sky_equal(name, "go")) || sky_asBool(sky_asBool(sky_equal(name, "hash")) || sky_asBool(sky_asBool(sky_equal(name, "html")) || sky_asBool(sky_asBool(sky_equal(name, "image")) || sky_asBool(sky_asBool(sky_equal(name, "io")) || sky_asBool(sky_asBool(sky_equal(name, "log")) || sky_asBool(sky_asBool(sky_equal(name, "math")) || sky_asBool(sky_asBool(sky_equal(name, "mime")) || sky_asBool(sky_asBool(sky_equal(name, "net")) || sky_asBool(sky_asBool(sky_equal(name, "os")) || sky_asBool(sky_asBool(sky_equal(name, "path")) || sky_asBool(sky_asBool(sky_equal(name, "reflect")) || sky_asBool(sky_asBool(sky_equal(name, "regexp")) || sky_asBool(sky_asBool(sky_equal(name, "runtime")) || sky_asBool(sky_asBool(sky_equal(name, "sort")) || sky_asBool(sky_asBool(sky_equal(name, "strconv")) || sky_asBool(sky_asBool(sky_equal(name, "strings")) || sky_asBool(sky_asBool(sky_equal(name, "sync")) || sky_asBool(sky_asBool(sky_equal(name, "syscall")) || sky_asBool(sky_asBool(sky_equal(name, "testing")) || sky_asBool(sky_asBool(sky_equal(name, "text")) || sky_asBool(sky_asBool(sky_equal(name, "time")) || sky_asBool(sky_asBool(sky_equal(name, "unicode")) || sky_asBool(sky_equal(name, "unsafe"))))))))))))))))))))))))))))))))))))))
}

func cmdAdd(_ any) any {
	return func() any { return func() any { __subject := sky_processGetArg(2); if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return sky_println("Usage: sky add <package>") };  if sky_asSkyMaybe(__subject).SkyName == "Just" { pkg := sky_asSkyMaybe(__subject).JustValue; _ = pkg; return func() any { sky_println(sky_concat("Adding ", sky_concat(pkg, "..."))); firstPart := func() any { return func() any { __subject := sky_call(sky_stringSplit("/"), pkg); if len(sky_asList(__subject)) > 0 { first := sky_asList(__subject)[0]; _ = first; return first };  if len(sky_asList(__subject)) == 0 { return pkg };  return nil }() }(); _ = firstPart; return func() any { if sky_asBool(sky_asBool(isGoStdlib(firstPart)) || sky_asBool(isGoStdlib(pkg))) { return func() any { cacheDir := sky_concat(".skycache/go/", sky_call2(sky_stringReplace("-"), "_", sky_call2(sky_stringReplace("/"), "_", sky_call2(sky_stringReplace("."), "_", pkg)))); _ = cacheDir; sky_fileMkdirAll(cacheDir); ensureGoModDir(struct{}{}); return generateGoBindings(pkg, cacheDir) }() }; return func() any { if sky_asBool(sky_call(sky_stringContains("."), firstPart)) { return detectAndInstall(pkg) }; return sky_println(sky_concat("Unknown package: ", sky_concat(pkg, ". Use github.com/owner/repo format."))) }() }() }() };  return nil }() }()
}

func detectAndInstall(pkg any) any {
	return func() any { tmpDir := sky_concat(".skycache/detect_", sky_call2(sky_stringReplace("/"), "_", pkg)); _ = tmpDir; sky_fileMkdirAll(".skycache"); cloneResult := sky_call(sky_processRun("git"), []any{"clone", "--depth", "1", sky_concat("https://", pkg), tmpDir}); _ = cloneResult; return func() any { return func() any { __subject := cloneResult; if sky_asSkyResult(__subject).SkyName == "Ok" { return func() any { hasSkyToml := func() any { return func() any { __subject := sky_fileRead(sky_concat(tmpDir, "/sky.toml")); if sky_asSkyResult(__subject).SkyName == "Ok" { return true };  if sky_asSkyResult(__subject).SkyName == "Err" { return false };  return nil }() }(); _ = hasSkyToml; return func() any { if sky_asBool(hasSkyToml) { return installAsSkyPackage(pkg, tmpDir) }; return installAsGoPackage(pkg, tmpDir) }() }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return installAsGoPackage(pkg, "") };  return nil }() }() }()
}

func installAsSkyPackage(pkg any, tmpDir any) any {
	return func() any { targetDir := sky_concat(".skydeps/", pkg); _ = targetDir; sky_fileMkdirAll(".skydeps"); sky_call(sky_processRun("rm"), []any{"-rf", targetDir}); sky_call(sky_processRun("mv"), []any{tmpDir, targetDir}); return sky_println(sky_concat("Added Sky package: ", pkg)) }()
}

func installAsGoPackage(pkg any, tmpDir any) any {
	return func() any { func() any { if sky_asBool(sky_stringIsEmpty(tmpDir)) { return struct{}{} }; return sky_call(sky_processRun("rm"), []any{"-rf", tmpDir}) }(); ensureGoModDir(struct{}{}); getResult := sky_call(sky_processRun("sh"), []any{"-c", sky_concat("cd .skycache/gomod && go get ", sky_concat(pkg, "@latest"))}); _ = getResult; return func() any { return func() any { __subject := getResult; if sky_asSkyResult(__subject).SkyName == "Ok" { return func() any { sky_println(sky_concat("Added Go package: ", pkg)); cacheDir := sky_concat(".skycache/go/", sky_call2(sky_stringReplace("-"), "_", sky_call2(sky_stringReplace("/"), "_", sky_call2(sky_stringReplace("."), "_", pkg)))); _ = cacheDir; sky_fileMkdirAll(cacheDir); return generateGoBindings(pkg, cacheDir) }() };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { sky_println(sky_concat("Failed to add ", sky_concat(pkg, sky_concat(": ", sky_errorToString(e))))); return sky_processExit(1) }() };  return nil }() }() }()
}

func generateGoBindings(pkg any, cacheDir any) any {
	return func() any { return func() any { __subject := Ffi_BindingGen_GenerateBindings(pkg, cacheDir); if sky_asSkyResult(__subject).SkyName == "Ok" { content := sky_asSkyResult(__subject).OkValue; _ = content; return sky_println(sky_concat("Generated bindings for ", sky_concat(pkg, sky_concat(" at ", cacheDir)))) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return sky_println(sky_concat("Warning: binding generation failed: ", e)) };  return nil }() }()
}

func ensureGoModDir(_ any) any {
	return func() any { sky_fileMkdirAll(".skycache/gomod"); goModPath := ".skycache/gomod/go.mod"; _ = goModPath; return func() any { return func() any { __subject := sky_fileRead(goModPath); if sky_asSkyResult(__subject).SkyName == "Ok" { return struct{}{} };  if sky_asSkyResult(__subject).SkyName == "Err" { return sky_call(sky_fileWrite(goModPath), "module skycache\n\ngo 1.21.0\n") };  return nil }() }() }()
}

func cmdInstall(_ any) any {
	return func() any { sky_println("Installing dependencies..."); ensureGoModDir(struct{}{}); autoGenerateBindings(struct{}{}); tidyResult := sky_call(sky_processRun("sh"), []any{"-c", "cd .skycache/gomod && go mod tidy"}); _ = tidyResult; func() any { return func() any { __subject := tidyResult; if sky_asSkyResult(__subject).SkyName == "Ok" { return struct{}{} };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return sky_println(sky_concat("Install warning: ", sky_errorToString(e))) };  return nil }() }(); checkForUpdates(struct{}{}); return sky_println("Install complete.") }()
}

func autoGenerateBindings(_ any) any {
	return func() any { findResult := sky_call(sky_processRun("sh"), []any{"-c", "find src/ -name '*.sky' 2>/dev/null | head -100"}); _ = findResult; return func() any { return func() any { __subject := findResult; if sky_asSkyResult(__subject).SkyName == "Ok" { output := sky_asSkyResult(__subject).OkValue; _ = output; return func() any { files := sky_call(sky_listFilter(func(f any) any { return sky_not(sky_stringIsEmpty(sky_stringTrim(f))) }), sky_call(sky_stringSplit("\n"), output)); _ = files; return sky_call(sky_listMap(func(f any) any { return scanFileForFfiImports(f) }), files) }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return struct{}{} };  return nil }() }() }()
}

func scanFileForFfiImports(filePath any) any {
	return func() any { return func() any { __subject := sky_fileRead(filePath); if sky_asSkyResult(__subject).SkyName == "Err" { return struct{}{} };  if sky_asSkyResult(__subject).SkyName == "Ok" { source := sky_asSkyResult(__subject).OkValue; _ = source; return func() any { lines := sky_call(sky_stringSplit("\n"), source); _ = lines; importLines := sky_call(sky_listFilter(func(l any) any { return sky_asBool(sky_call(sky_stringStartsWith("import "), l)) && sky_asBool(sky_asBool(sky_not(sky_call(sky_stringContains("Sky.Core"), l))) && sky_asBool(sky_asBool(sky_not(sky_call(sky_stringContains("Std."), l))) && sky_asBool(sky_asBool(sky_not(sky_call(sky_stringContains("Compiler."), l))) && sky_asBool(sky_asBool(sky_not(sky_call(sky_stringContains("Formatter."), l))) && sky_asBool(sky_asBool(sky_not(sky_call(sky_stringContains("Lsp."), l))) && sky_asBool(sky_not(sky_call(sky_stringContains("Ffi."), l)))))))) }), lines); _ = importLines; return sky_call(sky_listMap(func(l any) any { return processImportLine(l) }), importLines) }() };  return nil }() }()
}

func processImportLine(line any) any {
	return func() any { parts := sky_call(sky_stringSplit(" "), sky_stringTrim(line)); _ = parts; modName := func() any { if sky_asBool(sky_asInt(sky_listLength(parts)) >= sky_asInt(2)) { return func() any { return func() any { __subject := sky_listHead(sky_call(sky_listDrop(1), parts)); if sky_asSkyMaybe(__subject).SkyName == "Just" { n := sky_asSkyMaybe(__subject).JustValue; _ = n; return n };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "" };  return nil }() }() }; return "" }(); _ = modName; return func() any { if sky_asBool(sky_asBool(sky_stringIsEmpty(modName)) || sky_asBool(Compiler_Resolver_IsStdlib(modName))) { return struct{}{} }; return func() any { goPath := Compiler_Resolver_ModuleNameToGoPath(modName); _ = goPath; safeName := sky_call2(sky_stringReplace("-"), "_", sky_call2(sky_stringReplace("/"), "_", sky_call2(sky_stringReplace("."), "_", goPath))); _ = safeName; bindingPath := sky_concat(".skycache/go/", sky_concat(safeName, "/bindings.skyi")); _ = bindingPath; return func() any { return func() any { __subject := sky_fileRead(bindingPath); if sky_asSkyResult(__subject).SkyName == "Ok" { return struct{}{} };  if sky_asSkyResult(__subject).SkyName == "Err" { return func() any { sky_println(sky_concat("   Generating bindings for ", sky_concat(goPath, "..."))); firstPart := func() any { return func() any { __subject := sky_call(sky_stringSplit("/"), goPath); if len(sky_asList(__subject)) > 0 { first := sky_asList(__subject)[0]; _ = first; return first };  if len(sky_asList(__subject)) == 0 { return "" };  return nil }() }(); _ = firstPart; return func() any { if sky_asBool(sky_asBool(isGoStdlib(firstPart)) || sky_asBool(isGoStdlib(goPath))) { return func() any { cacheDir := sky_concat(".skycache/go/", safeName); _ = cacheDir; sky_fileMkdirAll(cacheDir); ensureGoModDir(struct{}{}); return generateGoBindings(goPath, cacheDir) }() }; return func() any { if sky_asBool(sky_call(sky_stringContains("."), firstPart)) { return detectAndInstall(goPath) }; return struct{}{} }() }() }() };  return nil }() }() }() }() }()
}

func cmdUpdate(_ any) any {
	return func() any { return func() any { __subject := sky_fileRead("sky.toml"); if sky_asSkyResult(__subject).SkyName == "Err" { return sky_println("No sky.toml found. Nothing to update.") };  if sky_asSkyResult(__subject).SkyName == "Ok" { content := sky_asSkyResult(__subject).OkValue; _ = content; return func() any { sky_println("Updating dependencies..."); lines := sky_call(sky_stringSplit("\n"), content); _ = lines; updateDependencies(lines); return sky_println("Update complete.") }() };  return nil }() }()
}

func updateDependencies(lines any) any {
	return updateDepsLoop(lines, false)
}

func updateDepsLoop(lines any, inDeps any) any {
	return func() any { if sky_asBool(sky_listIsEmpty(lines)) { return sky_println("") }; return func() any { line := func() any { return func() any { __subject := sky_listHead(lines); if sky_asSkyMaybe(__subject).SkyName == "Just" { l := sky_asSkyMaybe(__subject).JustValue; _ = l; return l };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "" };  return nil }() }(); _ = line; rest := sky_call(sky_listDrop(1), lines); _ = rest; trimmed := sky_stringTrim(line); _ = trimmed; return func() any { if sky_asBool(sky_equal(trimmed, "[dependencies]")) { return updateDepsLoop(rest, true) }; return func() any { if sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("["), trimmed)) && sky_asBool(!sky_equal(trimmed, "[dependencies]"))) { return updateDepsLoop(rest, false) }; return func() any { if sky_asBool(sky_asBool(inDeps) && sky_asBool(sky_call(sky_stringContains("="), trimmed))) { return func() any { updateOneDep(trimmed); return updateDepsLoop(rest, true) }() }; return updateDepsLoop(rest, inDeps) }() }() }() }() }()
}

func updateOneDep(depLine any) any {
	return func() any { parts := sky_call(sky_stringSplit("="), depLine); _ = parts; pkgName := func() any { return func() any { __subject := parts; if len(sky_asList(__subject)) > 0 { name := sky_asList(__subject)[0]; _ = name; return sky_stringTrim(name) };  if true { return "" };  return nil }() }(); _ = pkgName; return func() any { if sky_asBool(sky_stringIsEmpty(pkgName)) { return sky_println("") }; return func() any { sky_println(sky_concat("   Updating ", sky_concat(pkgName, "..."))); ensureGoModDir(""); sky_call(sky_processRun("sh"), []any{"-c", sky_concat("cd .skycache/gomod && go get ", sky_concat(pkgName, "@latest 2>/dev/null"))}); return sky_println("") }() }() }()
}

func cmdUpgrade(_ any) any {
	return func() any { sky_println("Checking for latest Sky release..."); latestTag := fetchLatestVersion(true); _ = latestTag; return func() any { if sky_asBool(sky_stringIsEmpty(latestTag)) { return sky_println("Could not determine latest version.") }; return func() any { if sky_asBool(sky_equal(latestTag, skyVersion)) { return sky_println(sky_concat("Already at latest version: ", latestTag)) }; return performUpgrade(latestTag) }() }() }()
}

func performUpgrade(tag any) any {
	return func() any { sky_println(sky_concat("Upgrading to ", sky_concat(tag, "..."))); skyBin := func() any { return func() any { __subject := sky_call(sky_processRun("which"), []any{"sky"}); if sky_asSkyResult(__subject).SkyName == "Ok" { path := sky_asSkyResult(__subject).OkValue; _ = path; return sky_stringTrim(path) };  if sky_asSkyResult(__subject).SkyName == "Err" { return "" };  return nil }() }(); _ = skyBin; return func() any { if sky_asBool(sky_stringIsEmpty(skyBin)) { return sky_println("Could not find sky binary path. Please upgrade manually.") }; return func() any { artifact := detectPlatformArtifact(struct{}{}); _ = artifact; url := sky_concat("https://github.com/anzellai/sky/releases/download/", sky_concat(tag, sky_concat("/", artifact))); _ = url; sky_println(sky_concat("   Downloading ", url)); downloadResult := sky_call(sky_processRun("sh"), []any{"-c", sky_concat("curl -fsSL '", sky_concat(url, sky_concat("' -o /tmp/sky-upgrade && chmod +x /tmp/sky-upgrade && mv /tmp/sky-upgrade ", skyBin)))}); _ = downloadResult; return func() any { return func() any { __subject := downloadResult; if sky_asSkyResult(__subject).SkyName == "Ok" { return sky_println(sky_concat("Upgraded to ", sky_concat(tag, " successfully!"))) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return sky_println(sky_concat("Upgrade failed: ", sky_concat(sky_errorToString(e), "\nDownload manually from: https://github.com/anzellai/sky/releases"))) };  return nil }() }() }() }() }()
}

func detectPlatformArtifact(_ any) any {
	return func() any { osResult := sky_call(sky_processRun("uname"), []any{"-s"}); _ = osResult; archResult := sky_call(sky_processRun("uname"), []any{"-m"}); _ = archResult; os := func() any { return func() any { __subject := osResult; if sky_asSkyResult(__subject).SkyName == "Ok" { o := sky_asSkyResult(__subject).OkValue; _ = o; return sky_stringToLower(sky_stringTrim(o)) };  if sky_asSkyResult(__subject).SkyName == "Err" { return "linux" };  return nil }() }(); _ = os; arch := func() any { return func() any { __subject := archResult; if sky_asSkyResult(__subject).SkyName == "Ok" { a := sky_asSkyResult(__subject).OkValue; _ = a; return sky_stringTrim(a) };  if sky_asSkyResult(__subject).SkyName == "Err" { return "x86_64" };  return nil }() }(); _ = arch; skyOs := func() any { if sky_asBool(sky_equal(os, "darwin")) { return "darwin" }; return func() any { if sky_asBool(sky_equal(os, "linux")) { return "linux" }; return "linux" }() }(); _ = skyOs; skyArch := func() any { if sky_asBool(sky_asBool(sky_equal(arch, "arm64")) || sky_asBool(sky_equal(arch, "aarch64"))) { return "arm64" }; return "x64" }(); _ = skyArch; return sky_concat("sky-", sky_concat(skyOs, sky_concat("-", skyArch))) }()
}

func fetchLatestVersion(forceRefresh any) any {
	return func() any { cacheDir := func() any { return func() any { __subject := sky_call(sky_processRun("sh"), []any{"-c", "echo $HOME"}); if sky_asSkyResult(__subject).SkyName == "Ok" { home := sky_asSkyResult(__subject).OkValue; _ = home; return sky_concat(sky_stringTrim(home), "/.sky") };  if sky_asSkyResult(__subject).SkyName == "Err" { return "/tmp/.sky" };  return nil }() }(); _ = cacheDir; cachePath := sky_concat(cacheDir, "/last-update-check.json"); _ = cachePath; return func() any { if sky_asBool(sky_not(forceRefresh)) { return func() any { return func() any { __subject := sky_call(sky_processRun("sh"), []any{"-c", sky_concat("if [ -f ", sky_concat(cachePath, sky_concat(" ] && [ $(( $(date +%s) - $(stat -f %m ", sky_concat(cachePath, sky_concat(" 2>/dev/null || stat -c %Y ", sky_concat(cachePath, sky_concat(" 2>/dev/null || echo 0) )) -lt 86400 ]; then cat ", sky_concat(cachePath, "; fi"))))))))}); if sky_asSkyResult(__subject).SkyName == "Ok" { cached := sky_asSkyResult(__subject).OkValue; _ = cached; return func() any { trimmed := sky_stringTrim(cached); _ = trimmed; return func() any { if sky_asBool(sky_not(sky_stringIsEmpty(trimmed))) { return trimmed }; return fetchAndCacheVersion(cacheDir, cachePath) }() }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return fetchAndCacheVersion(cacheDir, cachePath) };  return nil }() }() }; return fetchAndCacheVersion(cacheDir, cachePath) }() }()
}

func fetchAndCacheVersion(cacheDir any, cachePath any) any {
	return func() any { result := sky_call(sky_processRun("sh"), []any{"-c", "curl -fsSL https://api.github.com/repos/anzellai/sky/releases/latest 2>/dev/null | grep '\"tag_name\"' | sed 's/.*\"\\(v[^\"]*\\)\".*/\\1/'"}); _ = result; return func() any { return func() any { __subject := result; if sky_asSkyResult(__subject).SkyName == "Ok" { output := sky_asSkyResult(__subject).OkValue; _ = output; return func() any { tag := sky_stringTrim(output); _ = tag; func() any { if sky_asBool(sky_not(sky_stringIsEmpty(tag))) { return func() any { sky_fileMkdirAll(cacheDir); return sky_call(sky_fileWrite(cachePath), tag) }() }; return struct{}{} }(); return tag }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return "" };  return nil }() }() }()
}

func checkForUpdates(_ any) any {
	return func() any { latestTag := fetchLatestVersion(false); _ = latestTag; return func() any { if sky_asBool(sky_asBool(sky_not(sky_stringIsEmpty(latestTag))) && sky_asBool(!sky_equal(latestTag, skyVersion))) { return func() any { sky_println(""); sky_println(sky_concat("   A newer version of Sky is available: ", sky_concat(latestTag, sky_concat(" (current: ", sky_concat(skyVersion, ")"))))); return sky_println("   Run 'sky upgrade' to update.") }() }; return struct{}{} }() }()
}

func cmdRemove(_ any) any {
	return func() any { return func() any { __subject := sky_processGetArg(2); if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return sky_println("Usage: sky remove <package>") };  if sky_asSkyMaybe(__subject).SkyName == "Just" { pkg := sky_asSkyMaybe(__subject).JustValue; _ = pkg; return func() any { sky_println(sky_concat("Removing ", sky_concat(pkg, "..."))); skyDepDir := sky_concat(".skydeps/", pkg); _ = skyDepDir; goDepDir := sky_concat(".skycache/go/", sky_stringToLower(pkg)); _ = goDepDir; sky_call(sky_processRun("rm"), []any{"-rf", skyDepDir}); sky_call(sky_processRun("rm"), []any{"-rf", goDepDir}); return sky_println(sky_concat("Removed: ", pkg)) }() };  return nil }() }()
}

func cmdFmt(_ any) any {
	return func() any { filePath := func() any { return func() any { __subject := sky_processGetArg(2); if sky_asSkyMaybe(__subject).SkyName == "Just" { f := sky_asSkyMaybe(__subject).JustValue; _ = f; return f };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "src/Main.sky" };  return nil }() }(); _ = filePath; return func() any { return func() any { __subject := sky_fileRead(filePath); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { sky_println(sky_concat("Cannot read: ", filePath)); return sky_processExit(1) }() };  if sky_asSkyResult(__subject).SkyName == "Ok" { source := sky_asSkyResult(__subject).OkValue; _ = source; return func() any { lexResult := Compiler_Lexer_Lex(source); _ = lexResult; return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { sky_println(sky_concat("Parse error: ", e)); return sky_processExit(1) }() };  if sky_asSkyResult(__subject).SkyName == "Ok" { mod := sky_asSkyResult(__subject).OkValue; _ = mod; return func() any { formatted := Formatter_Format_FormatModule(mod); _ = formatted; sky_call(sky_fileWrite(filePath), formatted); return sky_println(sky_concat("Formatted: ", filePath)) }() };  return nil }() }() }() };  return nil }() }() }()
}

func cmdClean(_ any) any {
	return func() any { sky_call(sky_processRun("rm"), []any{"-rf", "dist"}); sky_call(sky_processRun("rm"), []any{"-rf", "sky-out"}); return sky_println("Cleaned.") }()
}

func cmdHelp(_ any) any {
	return func() any { sky_println(sky_concat("Sky Programming Language ", skyVersion)); sky_println(""); sky_println("Usage: sky <command> [args]"); sky_println(""); sky_println("Commands:"); sky_println("  build [file]    Compile to Go binary"); sky_println("  run [file]      Build and run"); sky_println("  check [file]    Type-check only"); sky_println("  fmt [file]      Format Sky source code"); sky_println("  add <package>   Add Go or Sky dependency"); sky_println("  install         Install all dependencies + generate bindings"); sky_println("  remove <pkg>    Remove a dependency"); sky_println("  update          Update sky.toml dependencies to latest"); sky_println("  upgrade         Upgrade Sky compiler to latest release"); sky_println("  clean           Remove build artifacts"); sky_println("  version         Show version"); return struct{}{} }()
}

var findCharIdx = Compiler_Pipeline_FindCharIdx

var findLastSlash = Compiler_Pipeline_FindLastSlash

var compile = Compiler_Pipeline_Compile

var compileMultiModule = Compiler_Pipeline_CompileMultiModule

var compileMultiModuleEntry = Compiler_Pipeline_CompileMultiModuleEntry

var emitMultiModuleGo = Compiler_Pipeline_EmitMultiModuleGo

var writeMultiModuleOutput = Compiler_Pipeline_WriteMultiModuleOutput

var optimiseJsonFunctions = Compiler_Pipeline_OptimiseJsonFunctions

var writeNativeJsonFile = Compiler_Pipeline_WriteNativeJsonFile

var nativeJsonHelperCode = Compiler_Pipeline_NativeJsonHelperCode()

var fixUnusedVars = Compiler_Pipeline_FixUnusedVars

var eliminateDeadCode = Compiler_Pipeline_EliminateDeadCode

var countFuncsInFile = Compiler_Pipeline_CountFuncsInFile

var trimWrapperFile = Compiler_Pipeline_TrimWrapperFile

var trimWrapperContent = Compiler_Pipeline_TrimWrapperContent

var isWrapperUsed = Compiler_Pipeline_IsWrapperUsed

var extractWrapperFuncName = Compiler_Pipeline_ExtractWrapperFuncName

var splitWrapperSections = Compiler_Pipeline_SplitWrapperSections

var splitWrapperLoop = Compiler_Pipeline_SplitWrapperLoop

var makeGoPackage = Compiler_Pipeline_MakeGoPackage

var loadLocalModules = Compiler_Pipeline_LoadLocalModules

var loadFromSkydepsOrSkip = Compiler_Pipeline_LoadFromSkydepsOrSkip

var loadFromCandidatesOrSkip = Compiler_Pipeline_LoadFromCandidatesOrSkip

var tryOneSkydepCandidate = Compiler_Pipeline_TryOneSkydepCandidate

var parseAndLoadSkydep = Compiler_Pipeline_ParseAndLoadSkydep

var findSkydepCandidates = Compiler_Pipeline_FindSkydepCandidates

var skydepSrcRoot = Compiler_Pipeline_SkydepSrcRoot

var loadFfiBindings = Compiler_Pipeline_LoadFfiBindings

var loadOneFfiBinding = Compiler_Pipeline_LoadOneFfiBinding

var tryLoadBinding = Compiler_Pipeline_TryLoadBinding

var copyFfiWrappers = Compiler_Pipeline_CopyFfiWrappers

var copyProjectWrappers = Compiler_Pipeline_CopyProjectWrappers

var copyWrapperDir = Compiler_Pipeline_CopyWrapperDir

var copyOneFfiWrapper = Compiler_Pipeline_CopyOneFfiWrapper

var buildAliasMap = Compiler_Pipeline_BuildAliasMap

var generatePrefixAliases = Compiler_Pipeline_GeneratePrefixAliases

var makePrefixAlias = Compiler_Pipeline_MakePrefixAlias

var isCommonName = Compiler_Pipeline_IsCommonName

var isSharedValue = Compiler_Pipeline_IsSharedValue

var deduplicateDecls = Compiler_Pipeline_DeduplicateDecls

var deduplicateDeclsLoop = Compiler_Pipeline_DeduplicateDeclsLoop

var getDeclName = Compiler_Pipeline_GetDeclName

var extractVarName = Compiler_Pipeline_ExtractVarName

var extractFuncName = Compiler_Pipeline_ExtractFuncName

var extractTypeName = Compiler_Pipeline_ExtractTypeName

var generateImportAliases = Compiler_Pipeline_GenerateImportAliases

var generateAliasesForImport = Compiler_Pipeline_GenerateAliasesForImport

var generateAliasesFromModule = Compiler_Pipeline_GenerateAliasesFromModule

var isExposingAll = Compiler_Pipeline_IsExposingAll

var isExposingNone = Compiler_Pipeline_IsExposingNone

var getExposeNames = Compiler_Pipeline_GetExposeNames

var isZeroArityInModule = Compiler_Pipeline_IsZeroArityInModule

var findModule = Compiler_Pipeline_FindModule

var extractExportedNames = Compiler_Pipeline_ExtractExportedNames

var extractDeclNames = Compiler_Pipeline_ExtractDeclNames

var collectAllFunctionNames = Compiler_Pipeline_CollectAllFunctionNames

var extractDeclNameForFn = Compiler_Pipeline_ExtractDeclNameForFn

var deduplicateStringList = Compiler_Pipeline_DeduplicateStringList

var deduplicateStringsLoop = Compiler_Pipeline_DeduplicateStringsLoop

var isModuleLoaded = Compiler_Pipeline_IsModuleLoaded

var addImportAlias = Compiler_Pipeline_AddImportAlias

var importAlias = Compiler_Pipeline_ImportAlias

var compileDependencyModule = Compiler_Pipeline_CompileDependencyModule

var generateConstructorAliases = Compiler_Pipeline_GenerateConstructorAliases

var makeConstructorAlias = Compiler_Pipeline_MakeConstructorAlias

var generateOriginalAliases = Compiler_Pipeline_GenerateOriginalAliases

var makeOriginalAlias = Compiler_Pipeline_MakeOriginalAlias

var isExportableDecl = Compiler_Pipeline_IsExportableDecl

var dirOfPath = Compiler_Pipeline_DirOfPath

var needsStdlibWrapper = Compiler_Pipeline_NeedsStdlibWrapper

var buildStdlibGoImports = Compiler_Pipeline_BuildStdlibGoImports

var importToGoImport = Compiler_Pipeline_ImportToGoImport

var copyStdlibGo = Compiler_Pipeline_CopyStdlibGo

var ffiSafePart = Compiler_Pipeline_FfiSafePart

var ffiSafePartLoop = Compiler_Pipeline_FfiSafePartLoop

var prefixDecl = Compiler_Pipeline_PrefixDecl

var compileSource = Compiler_Pipeline_CompileSource

var compileModule = Compiler_Pipeline_CompileModule

var compileProject = Compiler_Pipeline_CompileProject

var inferSrcRoot = Compiler_Pipeline_InferSrcRoot

var inferSrcRootFromEntry = Compiler_Pipeline_InferSrcRootFromEntry

var findSubstring = Compiler_Pipeline_FindSubstring

var findSubstringAt = Compiler_Pipeline_FindSubstringAt

var printDiagnostics = Compiler_Pipeline_PrintDiagnostics

var printTypedDecls = Compiler_Pipeline_PrintTypedDecls

var formatModule = Formatter_Format_FormatModule

var formatModuleHeader = Formatter_Format_FormatModuleHeader

var formatExposing = Formatter_Format_FormatExposing

var formatImport = Formatter_Format_FormatImport

var formatDeclarations = Formatter_Format_FormatDeclarations

var formatDeclaration = Formatter_Format_FormatDeclaration

var formatFunction = Formatter_Format_FormatFunction

var formatTypeDecl = Formatter_Format_FormatTypeDecl

var formatVariant = Formatter_Format_FormatVariant

var formatTypeAlias = Formatter_Format_FormatTypeAlias

var formatExpr = Formatter_Format_FormatExpr

var formatTuple = Formatter_Format_FormatTuple

var formatList = Formatter_Format_FormatList

var formatRecord = Formatter_Format_FormatRecord

var formatRecordUpdate = Formatter_Format_FormatRecordUpdate

var formatCall = Formatter_Format_FormatCall

var formatLambda = Formatter_Format_FormatLambda

var formatBinary = Formatter_Format_FormatBinary

var formatIf = Formatter_Format_FormatIf

var formatLet = Formatter_Format_FormatLet

var formatLetBinding = Formatter_Format_FormatLetBinding

var formatCase = Formatter_Format_FormatCase

var formatBranch = Formatter_Format_FormatBranch

var formatPattern = Formatter_Format_FormatPattern

var formatPatternParens = Formatter_Format_FormatPatternParens

var formatLiteral = Formatter_Format_FormatLiteral

var formatTypeExpr = Formatter_Format_FormatTypeExpr

var formatTypeExprParens = Formatter_Format_FormatTypeExprParens

var formatRecordType = Formatter_Format_FormatRecordType

var formatNamedType = Formatter_Format_FormatNamedType

var quoteString = Formatter_Format_QuoteString

var emptyState = Lsp_Server_EmptyState()

var startServer = Lsp_Server_StartServer

var serverLoop = Lsp_Server_ServerLoop

var handleMessage = Lsp_Server_HandleMessage

var sendAndReturn = Lsp_Server_SendAndReturn

var sendNotifyAndReturn = Lsp_Server_SendNotifyAndReturn

var handleInitialize = Lsp_Server_HandleInitialize

var handleDidOpen = Lsp_Server_HandleDidOpen

var handleDidChange = Lsp_Server_HandleDidChange

var analyzeAndPublishDiagnostics = Lsp_Server_AnalyzeAndPublishDiagnostics

var publishAndUpdateState = Lsp_Server_PublishAndUpdateState

var makeDiagnostic = Lsp_Server_MakeDiagnostic

var makeRange = Lsp_Server_MakeRange

var handleHover = Lsp_Server_HandleHover

var getHoverForPosition = Lsp_Server_GetHoverForPosition

var getLineAt = Lsp_Server_GetLineAt

var getLineAtLoop = Lsp_Server_GetLineAtLoop

var findLineStart = Lsp_Server_FindLineStart

var findLineStartLoop = Lsp_Server_FindLineStartLoop

var getWordAt = Lsp_Server_GetWordAt

var findWordStart = Lsp_Server_FindWordStart

var findWordEnd = Lsp_Server_FindWordEnd

var isIdentChar = Lsp_Server_IsIdentChar

var findAnnotationInSource = Lsp_Server_FindAnnotationInSource

var findAnnotationLoop = Lsp_Server_FindAnnotationLoop

var extractUntilNewline = Lsp_Server_ExtractUntilNewline

var handleCompletion = Lsp_Server_HandleCompletion

var makeCompletionItem = Lsp_Server_MakeCompletionItem

var handleDefinition = Lsp_Server_HandleDefinition

var findDefinitionForPosition = Lsp_Server_FindDefinitionForPosition

var findDefinitionInSource = Lsp_Server_FindDefinitionInSource

var findDefLineInSource = Lsp_Server_FindDefLineInSource

var handleFormatting = Lsp_Server_HandleFormatting

var generateBindings = Ffi_BindingGen_GenerateBindings

var generateSkyiFile = Ffi_BindingGen_GenerateSkyiFile

var pkgToModuleName = Ffi_BindingGen_PkgToModuleName

var extractFieldBindings = Ffi_BindingGen_ExtractFieldBindings

var extractFieldsForType = Ffi_BindingGen_ExtractFieldsForType

var isGenericTypeBinding = Ffi_BindingGen_IsGenericTypeBinding

var generateFieldBinding = Ffi_BindingGen_GenerateFieldBinding

var extractVarBindings = Ffi_BindingGen_ExtractVarBindings

var generateVarBinding = Ffi_BindingGen_GenerateVarBinding

var extractMethodBindings = Ffi_BindingGen_ExtractMethodBindings

var extractMethodsForType = Ffi_BindingGen_ExtractMethodsForType

var generateMethodBinding = Ffi_BindingGen_GenerateMethodBinding

var hyphenToCamel = Ffi_BindingGen_HyphenToCamel

var extractFuncBindings = Ffi_BindingGen_ExtractFuncBindings

var isSupportedFuncForPkg = Ffi_BindingGen_IsSupportedFuncForPkg

var allMethodParamsSafe = Ffi_BindingGen_AllMethodParamsSafe

var allMethodParamsLoop = Ffi_BindingGen_AllMethodParamsLoop

var isSafeTypeForPkg = Ffi_BindingGen_IsSafeTypeForPkg

var isSafeType = Ffi_BindingGen_IsSafeType

var needsExtraImportBinding = Ffi_BindingGen_NeedsExtraImportBinding

var generateFuncBinding = Ffi_BindingGen_GenerateFuncBinding

var buildReturnType = Ffi_BindingGen_BuildReturnType

var extractTypeBindings = Ffi_BindingGen_ExtractTypeBindings

var generateTypeBinding = Ffi_BindingGen_GenerateTypeBinding
