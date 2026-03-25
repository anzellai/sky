package main

import (
	"fmt"
	"bufio"
	"os"
	exec "os/exec"
	"strconv"
	"strings"
)

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

var _ = strings.Contains

var _ = strconv.Itoa

var _ = os.Exit

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

var _ = exec.Command

var _ = bufio.NewReader

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

func main() {
	sky_println("Hello from Sky!")
}
