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

func sky_refNew(v any) any { type SkyRef struct { Value any }; return &SkyRef{Value: v} }

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

func sky_fst(t any) any { return sky_asTuple2(t).V0 }

func sky_snd(t any) any { return sky_asTuple2(t).V1 }

func sky_errorToString(e any) any { return sky_asString(e) }

func sky_identity(v any) any { return v }

func sky_always(a any) any { return func(b any) any { return a } }

func sky_js(v any) any { return v }

func sky_call(f any, arg any) any { return f.(func(any) any)(arg) }

func sky_call2(f any, a any, b any) any { return f.(func(any) any)(a).(func(any) any)(b) }

func sky_call3(f any, a any, b any, c any) any { return f.(func(any) any)(a).(func(any) any)(b).(func(any) any)(c) }

var MapGoTypeToSky = Ffi_TypeMapper_MapGoTypeToSky

var IsGoPrimitive = Ffi_TypeMapper_IsGoPrimitive

var GoTypeToAssertion = Ffi_TypeMapper_GoTypeToAssertion

var LowerCamelCase = Ffi_TypeMapper_LowerCamelCase

func Ffi_TypeMapper_MapGoTypeToSky(goType any) any {
	return func() any { if sky_asBool(sky_equal(goType, "string")) { return "String" }; return func() any { if sky_asBool(sky_equal(goType, "bool")) { return "Bool" }; return func() any { if sky_asBool(sky_asBool(sky_equal(goType, "int")) || sky_asBool(sky_asBool(sky_equal(goType, "int8")) || sky_asBool(sky_asBool(sky_equal(goType, "int16")) || sky_asBool(sky_asBool(sky_equal(goType, "int32")) || sky_asBool(sky_equal(goType, "int64")))))) { return "Int" }; return func() any { if sky_asBool(sky_asBool(sky_equal(goType, "uint")) || sky_asBool(sky_asBool(sky_equal(goType, "uint8")) || sky_asBool(sky_asBool(sky_equal(goType, "uint16")) || sky_asBool(sky_asBool(sky_equal(goType, "uint32")) || sky_asBool(sky_equal(goType, "uint64")))))) { return "Int" }; return func() any { if sky_asBool(sky_asBool(sky_equal(goType, "float32")) || sky_asBool(sky_equal(goType, "float64"))) { return "Float" }; return func() any { if sky_asBool(sky_equal(goType, "[]byte")) { return "Bytes" }; return func() any { if sky_asBool(sky_equal(goType, "error")) { return "Error" }; return func() any { if sky_asBool(sky_asBool(sky_equal(goType, "interface{}")) || sky_asBool(sky_equal(goType, "any"))) { return "Any" }; return func() any { if sky_asBool(sky_equal(goType, "rune")) { return "Char" }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("*"), goType)) { return func() any { inner := sky_call(sky_call(sky_stringSlice(1), sky_stringLength(goType)), goType); _ = inner; return func() any { if sky_asBool(Ffi_TypeMapper_IsGoPrimitive(inner)) { return sky_asString("Maybe ") + sky_asString(Ffi_TypeMapper_MapGoTypeToSky(inner)) }; return Ffi_TypeMapper_MapGoTypeToSky(inner) }() }() }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("[]"), goType)) { return func() any { elem := sky_call(sky_call(sky_stringSlice(2), sky_stringLength(goType)), goType); _ = elem; return sky_asString("List ") + sky_asString(Ffi_TypeMapper_MapGoTypeToSky(elem)) }() }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("map["), goType)) { return "Any" }; return func() any { if sky_asBool(sky_call(sky_stringContains("."), goType)) { return func() any { parts := sky_call(sky_stringSplit("."), goType); _ = parts; return func() any { return func() any { __subject := sky_listReverse(parts); if len(sky_asList(__subject)) > 0 { last := sky_asList(__subject)[0]; _ = last; return last };  if len(sky_asList(__subject)) == 0 { return goType };  return nil }() }() }() }; return goType }() }() }() }() }() }() }() }() }() }() }() }() }()
}

func Ffi_TypeMapper_IsGoPrimitive(t any) any {
	return sky_asBool(sky_equal(t, "string")) || sky_asBool(sky_asBool(sky_equal(t, "int")) || sky_asBool(sky_asBool(sky_equal(t, "int8")) || sky_asBool(sky_asBool(sky_equal(t, "int16")) || sky_asBool(sky_asBool(sky_equal(t, "int32")) || sky_asBool(sky_asBool(sky_equal(t, "int64")) || sky_asBool(sky_asBool(sky_equal(t, "uint")) || sky_asBool(sky_asBool(sky_equal(t, "uint8")) || sky_asBool(sky_asBool(sky_equal(t, "uint16")) || sky_asBool(sky_asBool(sky_equal(t, "uint32")) || sky_asBool(sky_asBool(sky_equal(t, "uint64")) || sky_asBool(sky_asBool(sky_equal(t, "float32")) || sky_asBool(sky_asBool(sky_equal(t, "float64")) || sky_asBool(sky_asBool(sky_equal(t, "bool")) || sky_asBool(sky_equal(t, "rune")))))))))))))))
}

func Ffi_TypeMapper_GoTypeToAssertion(goType any) any {
	return func() any { if sky_asBool(sky_equal(goType, "string")) { return "sky_asString" }; return func() any { if sky_asBool(sky_asBool(sky_equal(goType, "int")) || sky_asBool(sky_asBool(sky_equal(goType, "int8")) || sky_asBool(sky_asBool(sky_equal(goType, "int16")) || sky_asBool(sky_asBool(sky_equal(goType, "int32")) || sky_asBool(sky_equal(goType, "int64")))))) { return "sky_asInt" }; return func() any { if sky_asBool(sky_asBool(sky_equal(goType, "uint")) || sky_asBool(sky_asBool(sky_equal(goType, "uint8")) || sky_asBool(sky_asBool(sky_equal(goType, "uint16")) || sky_asBool(sky_asBool(sky_equal(goType, "uint32")) || sky_asBool(sky_equal(goType, "uint64")))))) { return "sky_asInt" }; return func() any { if sky_asBool(sky_asBool(sky_equal(goType, "float32")) || sky_asBool(sky_equal(goType, "float64"))) { return "sky_asFloat" }; return func() any { if sky_asBool(sky_equal(goType, "bool")) { return "sky_asBool" }; return "" }() }() }() }() }()
}

func Ffi_TypeMapper_LowerCamelCase(s any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(s)) { return "" }; return sky_asString(sky_stringToLower(sky_call(sky_call(sky_stringSlice(0), 1), s))) + sky_asString(sky_call(sky_call(sky_stringSlice(1), sky_stringLength(s)), s)) }()
}

var GenerateBindings = Ffi_BindingGen_GenerateBindings

var GenerateSkyiFile = Ffi_BindingGen_GenerateSkyiFile

var PkgToModuleName = Ffi_BindingGen_PkgToModuleName

var CapitalizeFirst = Ffi_BindingGen_CapitalizeFirst

var ExtractFuncBindings = Ffi_BindingGen_ExtractFuncBindings

var ExtractTypeBindings = Ffi_BindingGen_ExtractTypeBindings

func Ffi_BindingGen_GenerateBindings(pkgName any, outDir any) any {
	return func() any { return func() any { __subject := Ffi_Inspector_InspectPackage(pkgName); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { inspectJson := sky_asSkyResult(__subject).OkValue; _ = inspectJson; return func() any { skyiContent := Ffi_BindingGen_GenerateSkyiFile(pkgName, inspectJson); _ = skyiContent; skyiPath := sky_asString(outDir) + sky_asString("/bindings.skyi"); _ = skyiPath; sky_fileMkdirAll(outDir); sky_call(sky_fileWrite(skyiPath), skyiContent); wrapperResult := Ffi_WrapperGen_GenerateWrappers(pkgName, inspectJson, outDir); _ = wrapperResult; return func() any { return func() any { __subject := wrapperResult; if sky_asSkyResult(__subject).SkyName == "Ok" { return SkyOk(skyiContent) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  return nil }() }() }() };  return nil }() }()
}

func Ffi_BindingGen_GenerateSkyiFile(pkgName any, inspectJson any) any {
	return func() any { moduleName := Ffi_BindingGen_PkgToModuleName(pkgName); _ = moduleName; funcLines := Ffi_BindingGen_ExtractFuncBindings(pkgName, inspectJson); _ = funcLines; typeLines := Ffi_BindingGen_ExtractTypeBindings(inspectJson); _ = typeLines; return sky_call(sky_stringJoin("\n"), []any{sky_asString("module ") + sky_asString(sky_asString(moduleName) + sky_asString(" exposing (..)")), "", sky_asString("foreign import \"") + sky_asString(sky_asString(pkgName) + sky_asString("\" exposing (..)")), "", sky_call(sky_stringJoin("\n\n"), funcLines), "", sky_call(sky_stringJoin("\n\n"), typeLines)}) }()
}

func Ffi_BindingGen_PkgToModuleName(pkgPath any) any {
	return func() any { parts := sky_call(sky_stringSplit("/"), pkgPath); _ = parts; capitalized := sky_call(sky_listMap(Ffi_BindingGen_CapitalizeFirst), parts); _ = capitalized; return sky_call(sky_stringJoin("."), capitalized) }()
}

func Ffi_BindingGen_CapitalizeFirst(s any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(s)) { return "" }; return sky_asString(sky_stringToUpper(sky_call(sky_call(sky_stringSlice(0), 1), s))) + sky_asString(sky_call(sky_call(sky_stringSlice(1), sky_stringLength(s)), s)) }()
}

func Ffi_BindingGen_ExtractFuncBindings(pkgName any, json any) any {
	return func() any { funcsJson := Lsp_JsonRpc_JsonGetObject("funcs", json); _ = funcsJson; return []any{} }()
}

func Ffi_BindingGen_ExtractTypeBindings(json any) any {
	return []any{}
}

var emptyCtx = Compiler_Lower_EmptyCtx

var LowerModule = Compiler_Lower_LowerModule

var LowerDeclarations = Compiler_Lower_LowerDeclarations

var LowerDecl = Compiler_Lower_LowerDecl

var LowerFunction = Compiler_Lower_LowerFunction

var LowerParam = Compiler_Lower_LowerParam

var LowerExpr = Compiler_Lower_LowerExpr

var LowerIdentifier = Compiler_Lower_LowerIdentifier

var LowerConstructorValue = Compiler_Lower_LowerConstructorValue

var LowerQualified = Compiler_Lower_LowerQualified

var LowerCall = Compiler_Lower_LowerCall

var CheckPartialApplication = Compiler_Lower_CheckPartialApplication

var GeneratePartialClosure = Compiler_Lower_GeneratePartialClosure

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

var ListContains = Compiler_Lower_ListContains

var IsLocalFn = Compiler_Lower_IsLocalFn

var IsLocalFunction = Compiler_Lower_IsLocalFunction

var GoQuote = Compiler_Lower_GoQuote

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

var AddFuncAssertion = Compiler_Lower_AddFuncAssertion

var StmtToGoString = Compiler_Lower_StmtToGoString

func Compiler_Lower_EmptyCtx() any {
	return map[string]any{"registry": Compiler_Adt_EmptyRegistry(), "moduleExports": sky_dictEmpty, "importedConstructors": sky_dictEmpty, "localFunctions": []any{}, "collectedImports": sky_setEmpty, "importAliases": sky_dictEmpty, "modulePrefix": "", "localFunctionArity": sky_dictEmpty}
}

func Compiler_Lower_LowerModule(registry any, mod any) any {
	return func() any { ctx := sky_recordUpdate(Compiler_Lower_EmptyCtx, map[string]any{"registry": registry, "localFunctions": Compiler_Lower_CollectLocalFunctions(sky_asMap(mod)["declarations"]), "importedConstructors": Compiler_Lower_BuildConstructorMap(registry)}); _ = ctx; goDecls := Compiler_Lower_LowerDeclarations(ctx, sky_asMap(mod)["declarations"]); _ = goDecls; ctorDecls := Compiler_Lower_GenerateConstructorDecls(registry, sky_asMap(mod)["declarations"]); _ = ctorDecls; imports := []any{map[string]any{"path": "fmt", "alias_": ""}, map[string]any{"path": "bufio", "alias_": ""}, map[string]any{"path": "os", "alias_": ""}, map[string]any{"path": "os/exec", "alias_": "exec"}, map[string]any{"path": "strconv", "alias_": ""}, map[string]any{"path": "strings", "alias_": ""}}; _ = imports; helperDecls := Compiler_Lower_GenerateHelperDecls; _ = helperDecls; return map[string]any{"name": "main", "imports": imports, "declarations": sky_listConcat([]any{helperDecls, ctorDecls, goDecls})} }()
}

func Compiler_Lower_LowerDeclarations(ctx any, decls any) any {
	return sky_call(sky_listFilterMap(func(__pa0 any) any { return Compiler_Lower_LowerDecl(ctx, __pa0) }), decls)
}

func Compiler_Lower_LowerDecl(ctx any, decl any) any {
	return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "FunDecl" { name := sky_asMap(__subject)["V0"]; _ = name; params := sky_asMap(__subject)["V1"]; _ = params; body := sky_asMap(__subject)["V2"]; _ = body; return SkyJust(Compiler_Lower_LowerFunction(ctx, name, params, body)) };  if true { return SkyNothing() };  return nil }() }()
}

func Compiler_Lower_LowerFunction(ctx any, name any, params any, body any) any {
	return func() any { isMain := sky_equal(name, "main"); _ = isMain; goName := func() any { if sky_asBool(isMain) { return "main" }; return Compiler_Lower_SanitizeGoIdent(name) }(); _ = goName; goParams := sky_call(sky_listMap(Compiler_Lower_LowerParam), params); _ = goParams; returnType := func() any { if sky_asBool(isMain) { return "" }; return "any" }(); _ = returnType; goBody := func() any { if sky_asBool(isMain) { return Compiler_Lower_LowerExprToStmts(ctx, body) }; return []any{GoReturn(Compiler_Lower_LowerExpr(ctx, body))} }(); _ = goBody; return GoDeclFunc(map[string]any{"name": goName, "params": goParams, "returnType": returnType, "body": goBody}) }()
}

func Compiler_Lower_LowerParam(pat any) any {
	return func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PVariable" { name := sky_asMap(__subject)["V0"]; _ = name; return map[string]any{"name": Compiler_Lower_SanitizeGoIdent(name), "type_": "any"} };  if sky_asMap(__subject)["SkyName"] == "PWildcard" { return map[string]any{"name": "_", "type_": "any"} };  if true { return map[string]any{"name": "_p", "type_": "any"} };  return nil }() }()
}

func Compiler_Lower_LowerExpr(ctx any, expr any) any {
	return func() any { return func() any { __subject := expr; if sky_asMap(__subject)["SkyName"] == "IntLitExpr" { raw := sky_asMap(__subject)["V1"]; _ = raw; return GoBasicLit(raw) };  if sky_asMap(__subject)["SkyName"] == "FloatLitExpr" { raw := sky_asMap(__subject)["V1"]; _ = raw; return GoBasicLit(raw) };  if sky_asMap(__subject)["SkyName"] == "StringLitExpr" { s := sky_asMap(__subject)["V0"]; _ = s; return GoStringLit(s) };  if sky_asMap(__subject)["SkyName"] == "CharLitExpr" { s := sky_asMap(__subject)["V0"]; _ = s; return GoCallExpr(GoIdent("string"), []any{GoBasicLit(sky_asString("'") + sky_asString(sky_asString(sky_call(sky_call(sky_stringSlice(1), sky_asInt(sky_stringLength(s)) - sky_asInt(1)), s)) + sky_asString("'")))}) };  if sky_asMap(__subject)["SkyName"] == "BoolLitExpr" { b := sky_asMap(__subject)["V0"]; _ = b; return func() any { if sky_asBool(b) { return GoIdent("true") }; return GoIdent("false") }() };  if sky_asMap(__subject)["SkyName"] == "UnitExpr" { return GoRawExpr("struct{}{}") };  if sky_asMap(__subject)["SkyName"] == "IdentifierExpr" { name := sky_asMap(__subject)["V0"]; _ = name; return Compiler_Lower_LowerIdentifier(ctx, name) };  if sky_asMap(__subject)["SkyName"] == "QualifiedExpr" { parts := sky_asMap(__subject)["V0"]; _ = parts; return Compiler_Lower_LowerQualified(ctx, parts) };  if sky_asMap(__subject)["SkyName"] == "TupleExpr" { items := sky_asMap(__subject)["V0"]; _ = items; return func() any { goItems := sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Lower_LowerExpr(ctx, __pa0) }), items); _ = goItems; n := sky_listLength(items); _ = n; return func() any { if sky_asBool(sky_equal(n, 2)) { return GoRawExpr(sky_asString("SkyTuple2{V0: ") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(Compiler_Lower_ListGet(0, goItems))) + sky_asString(sky_asString(", V1: ") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(Compiler_Lower_ListGet(1, goItems))) + sky_asString("}"))))) }; return func() any { if sky_asBool(sky_equal(n, 3)) { return GoRawExpr(sky_asString("SkyTuple3{V0: ") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(Compiler_Lower_ListGet(0, goItems))) + sky_asString(sky_asString(", V1: ") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(Compiler_Lower_ListGet(1, goItems))) + sky_asString(sky_asString(", V2: ") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(Compiler_Lower_ListGet(2, goItems))) + sky_asString("}"))))))) }; return GoSliceLit(goItems) }() }() }() };  if sky_asMap(__subject)["SkyName"] == "ListExpr" { items := sky_asMap(__subject)["V0"]; _ = items; return GoSliceLit(sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Lower_LowerExpr(ctx, __pa0) }), items)) };  if sky_asMap(__subject)["SkyName"] == "RecordExpr" { fields := sky_asMap(__subject)["V0"]; _ = fields; return GoMapLit(sky_call(sky_listMap(func(f any) any { return []any{} }), fields)) };  if sky_asMap(__subject)["SkyName"] == "RecordUpdateExpr" { base := sky_asMap(__subject)["V0"]; _ = base; fields := sky_asMap(__subject)["V1"]; _ = fields; return Compiler_Lower_LowerRecordUpdate(ctx, base, fields) };  if sky_asMap(__subject)["SkyName"] == "FieldAccessExpr" { target := sky_asMap(__subject)["V0"]; _ = target; fieldName := sky_asMap(__subject)["V1"]; _ = fieldName; return GoIndexExpr(GoCallExpr(GoIdent("sky_asMap"), []any{Compiler_Lower_LowerExpr(ctx, target)}), GoStringLit(fieldName)) };  if sky_asMap(__subject)["SkyName"] == "CallExpr" { callee := sky_asMap(__subject)["V0"]; _ = callee; args := sky_asMap(__subject)["V1"]; _ = args; return Compiler_Lower_LowerCall(ctx, callee, args) };  if sky_asMap(__subject)["SkyName"] == "LambdaExpr" { params := sky_asMap(__subject)["V0"]; _ = params; body := sky_asMap(__subject)["V1"]; _ = body; return Compiler_Lower_LowerLambda(ctx, params, body) };  if sky_asMap(__subject)["SkyName"] == "IfExpr" { condition := sky_asMap(__subject)["V0"]; _ = condition; thenBranch := sky_asMap(__subject)["V1"]; _ = thenBranch; elseBranch := sky_asMap(__subject)["V2"]; _ = elseBranch; return Compiler_Lower_LowerIf(ctx, condition, thenBranch, elseBranch) };  if sky_asMap(__subject)["SkyName"] == "LetExpr" { bindings := sky_asMap(__subject)["V0"]; _ = bindings; body := sky_asMap(__subject)["V1"]; _ = body; return Compiler_Lower_LowerLet(ctx, bindings, body) };  if sky_asMap(__subject)["SkyName"] == "CaseExpr" { subject := sky_asMap(__subject)["V0"]; _ = subject; branches := sky_asMap(__subject)["V1"]; _ = branches; return Compiler_Lower_LowerCase(ctx, subject, branches) };  if sky_asMap(__subject)["SkyName"] == "BinaryExpr" { op := sky_asMap(__subject)["V0"]; _ = op; left := sky_asMap(__subject)["V1"]; _ = left; right := sky_asMap(__subject)["V2"]; _ = right; return Compiler_Lower_LowerBinary(ctx, op, left, right) };  if sky_asMap(__subject)["SkyName"] == "NegateExpr" { inner := sky_asMap(__subject)["V0"]; _ = inner; return GoUnaryExpr("-", Compiler_Lower_LowerExpr(ctx, inner)) };  if sky_asMap(__subject)["SkyName"] == "ParenExpr" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Compiler_Lower_LowerExpr(ctx, inner) };  return nil }() }()
}

func Compiler_Lower_LowerIdentifier(ctx any, name any) any {
	return func() any { return func() any { __subject := sky_call(sky_dictGet(name), sky_asMap(ctx)["importedConstructors"]); if sky_asSkyMaybe(__subject).SkyName == "Just" { info := sky_asSkyMaybe(__subject).JustValue; _ = info; return func() any { if sky_asBool(sky_equal(sky_asMap(info)["arity"], 0)) { return Compiler_Lower_LowerConstructorValue(name, info) }; return GoIdent(Compiler_Lower_SanitizeGoIdent(name)) }() };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return func() any { if sky_asBool(sky_equal(name, "Ok")) { return GoIdent("SkyOk") }; return func() any { if sky_asBool(sky_equal(name, "Err")) { return GoIdent("SkyErr") }; return func() any { if sky_asBool(sky_equal(name, "Just")) { return GoIdent("SkyJust") }; return func() any { if sky_asBool(sky_equal(name, "Nothing")) { return GoCallExpr(GoIdent("SkyNothing"), []any{}) }; return func() any { if sky_asBool(sky_equal(name, "True")) { return GoIdent("true") }; return func() any { if sky_asBool(sky_equal(name, "False")) { return GoIdent("false") }; return func() any { if sky_asBool(sky_equal(name, "not")) { return GoIdent("sky_not") }; return func() any { if sky_asBool(sky_equal(name, "fst")) { return GoIdent("sky_fst") }; return func() any { if sky_asBool(sky_equal(name, "snd")) { return GoIdent("sky_snd") }; return func() any { if sky_asBool(sky_equal(name, "errorToString")) { return GoIdent("sky_errorToString") }; return func() any { if sky_asBool(sky_equal(name, "println")) { return GoIdent("sky_println") }; return func() any { if sky_asBool(sky_equal(name, "identity")) { return GoIdent("sky_identity") }; return func() any { if sky_asBool(sky_equal(name, "always")) { return GoIdent("sky_always") }; return func() any { if sky_asBool(sky_equal(name, "js")) { return GoIdent("sky_js") }; return func() any { if sky_asBool(sky_asBool(sky_not(sky_stringIsEmpty(sky_asMap(ctx)["modulePrefix"]))) && sky_asBool(Compiler_Lower_ListContains(name, sky_asMap(ctx)["localFunctions"]))) { return GoIdent(sky_asString(sky_asMap(ctx)["modulePrefix"]) + sky_asString(sky_asString("_") + sky_asString(Compiler_Lower_CapitalizeFirst(Compiler_Lower_SanitizeGoIdent(name))))) }; return GoIdent(Compiler_Lower_SanitizeGoIdent(name)) }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() };  return nil }() }()
}

func Compiler_Lower_LowerConstructorValue(name any, info any) any {
	return GoMapLit([]any{[]any{}, []any{}})
}

func Compiler_Lower_LowerQualified(ctx any, parts any) any {
	return func() any { qualName := sky_call(sky_stringJoin("."), parts); _ = qualName; return func() any { if sky_asBool(sky_equal(qualName, "String.fromInt")) { return GoIdent("sky_stringFromInt") }; return func() any { if sky_asBool(sky_equal(qualName, "String.fromFloat")) { return GoIdent("sky_stringFromFloat") }; return func() any { if sky_asBool(sky_equal(qualName, "String.toUpper")) { return GoIdent("sky_stringToUpper") }; return func() any { if sky_asBool(sky_equal(qualName, "String.toLower")) { return GoIdent("sky_stringToLower") }; return func() any { if sky_asBool(sky_equal(qualName, "String.length")) { return GoIdent("sky_stringLength") }; return func() any { if sky_asBool(sky_equal(qualName, "String.join")) { return GoIdent("sky_stringJoin") }; return func() any { if sky_asBool(sky_equal(qualName, "String.contains")) { return GoIdent("sky_stringContains") }; return func() any { if sky_asBool(sky_equal(qualName, "String.trim")) { return GoIdent("sky_stringTrim") }; return func() any { if sky_asBool(sky_equal(qualName, "String.isEmpty")) { return GoIdent("sky_stringIsEmpty") }; return func() any { if sky_asBool(sky_equal(qualName, "String.startsWith")) { return GoIdent("sky_stringStartsWith") }; return func() any { if sky_asBool(sky_equal(qualName, "String.endsWith")) { return GoIdent("sky_stringEndsWith") }; return func() any { if sky_asBool(sky_equal(qualName, "String.split")) { return GoIdent("sky_stringSplit") }; return func() any { if sky_asBool(sky_equal(qualName, "String.replace")) { return GoIdent("sky_stringReplace") }; return func() any { if sky_asBool(sky_equal(qualName, "String.toInt")) { return GoIdent("sky_stringToInt") }; return func() any { if sky_asBool(sky_equal(qualName, "String.toFloat")) { return GoIdent("sky_stringToFloat") }; return func() any { if sky_asBool(sky_equal(qualName, "String.append")) { return GoIdent("sky_stringAppend") }; return func() any { if sky_asBool(sky_equal(qualName, "String.slice")) { return GoIdent("sky_stringSlice") }; return func() any { if sky_asBool(sky_equal(qualName, "List.map")) { return GoIdent("sky_listMap") }; return func() any { if sky_asBool(sky_equal(qualName, "List.filter")) { return GoIdent("sky_listFilter") }; return func() any { if sky_asBool(sky_equal(qualName, "List.foldl")) { return GoIdent("sky_listFoldl") }; return func() any { if sky_asBool(sky_equal(qualName, "List.foldr")) { return GoIdent("sky_listFoldr") }; return func() any { if sky_asBool(sky_equal(qualName, "List.length")) { return GoIdent("sky_listLength") }; return func() any { if sky_asBool(sky_equal(qualName, "List.head")) { return GoIdent("sky_listHead") }; return func() any { if sky_asBool(sky_equal(qualName, "List.reverse")) { return GoIdent("sky_listReverse") }; return func() any { if sky_asBool(sky_equal(qualName, "List.isEmpty")) { return GoIdent("sky_listIsEmpty") }; return func() any { if sky_asBool(sky_equal(qualName, "List.append")) { return GoIdent("sky_listAppend") }; return func() any { if sky_asBool(sky_equal(qualName, "List.concatMap")) { return GoIdent("sky_listConcatMap") }; return func() any { if sky_asBool(sky_equal(qualName, "List.filterMap")) { return GoIdent("sky_listFilterMap") }; return func() any { if sky_asBool(sky_equal(qualName, "List.indexedMap")) { return GoIdent("sky_listIndexedMap") }; return func() any { if sky_asBool(sky_equal(qualName, "List.concat")) { return GoIdent("sky_listConcat") }; return func() any { if sky_asBool(sky_equal(qualName, "List.drop")) { return GoIdent("sky_listDrop") }; return func() any { if sky_asBool(sky_equal(qualName, "List.member")) { return GoIdent("sky_listMember") }; return func() any { if sky_asBool(sky_equal(qualName, "Log.println")) { return GoIdent("sky_println") }; return func() any { if sky_asBool(sky_equal(qualName, "File.readFile")) { return GoIdent("sky_fileRead") }; return func() any { if sky_asBool(sky_equal(qualName, "File.writeFile")) { return GoIdent("sky_fileWrite") }; return func() any { if sky_asBool(sky_equal(qualName, "File.mkdirAll")) { return GoIdent("sky_fileMkdirAll") }; return func() any { if sky_asBool(sky_equal(qualName, "Process.exit")) { return GoIdent("sky_processExit") }; return func() any { if sky_asBool(sky_equal(qualName, "Process.run")) { return GoIdent("sky_processRun") }; return func() any { if sky_asBool(sky_equal(qualName, "Args.getArgs")) { return GoIdent("sky_processGetArgs") }; return func() any { if sky_asBool(sky_equal(qualName, "Args.getArg")) { return GoIdent("sky_processGetArg") }; return func() any { if sky_asBool(sky_equal(qualName, "Ref.new")) { return GoIdent("sky_refNew") }; return func() any { if sky_asBool(sky_equal(qualName, "Ref.get")) { return GoIdent("sky_refGet") }; return func() any { if sky_asBool(sky_equal(qualName, "Ref.set")) { return GoIdent("sky_refSet") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.empty")) { return GoIdent("sky_dictEmpty") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.insert")) { return GoIdent("sky_dictInsert") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.get")) { return GoIdent("sky_dictGet") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.keys")) { return GoIdent("sky_dictKeys") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.values")) { return GoIdent("sky_dictValues") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.toList")) { return GoIdent("sky_dictToList") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.fromList")) { return GoIdent("sky_dictFromList") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.map")) { return GoIdent("sky_dictMap") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.foldl")) { return GoIdent("sky_dictFoldl") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.union")) { return GoIdent("sky_dictUnion") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.remove")) { return GoIdent("sky_dictRemove") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.member")) { return GoIdent("sky_dictMember") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.empty")) { return GoIdent("sky_setEmpty") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.singleton")) { return GoIdent("sky_setSingleton") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.insert")) { return GoIdent("sky_setInsert") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.member")) { return GoIdent("sky_setMember") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.union")) { return GoIdent("sky_setUnion") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.diff")) { return GoIdent("sky_setDiff") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.toList")) { return GoIdent("sky_setToList") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.fromList")) { return GoIdent("sky_setFromList") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.isEmpty")) { return GoIdent("sky_setIsEmpty") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.remove")) { return GoIdent("sky_setRemove") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.isUpper")) { return GoIdent("sky_charIsUpper") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.isLower")) { return GoIdent("sky_charIsLower") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.isDigit")) { return GoIdent("sky_charIsDigit") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.isAlpha")) { return GoIdent("sky_charIsAlpha") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.isAlphaNum")) { return GoIdent("sky_charIsAlphaNum") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.toUpper")) { return GoIdent("sky_charToUpper") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.toLower")) { return GoIdent("sky_charToLower") }; return func() any { if sky_asBool(sky_equal(qualName, "String.fromChar")) { return GoIdent("sky_stringFromChar") }; return func() any { if sky_asBool(sky_equal(qualName, "String.toList")) { return GoIdent("sky_stringToList") }; return func() any { if sky_asBool(sky_equal(qualName, "Io.readLine")) { return GoIdent("sky_readLine") }; return func() any { if sky_asBool(sky_equal(qualName, "Io.readBytes")) { return GoIdent("sky_readBytes") }; return func() any { if sky_asBool(sky_equal(qualName, "Io.writeStdout")) { return GoIdent("sky_writeStdout") }; return func() any { if sky_asBool(sky_equal(qualName, "Io.writeStderr")) { return GoIdent("sky_writeStderr") }; return func() any { if sky_asBool(sky_equal(sky_listLength(parts), 2)) { return func() any { modPart := func() any { return func() any { __subject := sky_listHead(parts); if sky_asSkyMaybe(__subject).SkyName == "Just" { p := sky_asSkyMaybe(__subject).JustValue; _ = p; return p };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "" };  return nil }() }(); _ = modPart; funcPart := func() any { return func() any { __subject := sky_listHead(sky_call(sky_listDrop(1), parts)); if sky_asSkyMaybe(__subject).SkyName == "Just" { p := sky_asSkyMaybe(__subject).JustValue; _ = p; return p };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "" };  return nil }() }(); _ = funcPart; return func() any { return func() any { __subject := sky_call(sky_dictGet(modPart), sky_asMap(ctx)["importAliases"]); if sky_asSkyMaybe(__subject).SkyName == "Just" { qualModName := sky_asSkyMaybe(__subject).JustValue; _ = qualModName; return func() any { prefix := sky_call(sky_call(sky_stringReplace("."), "_"), qualModName); _ = prefix; goName := sky_asString(prefix) + sky_asString(sky_asString("_") + sky_asString(Compiler_Lower_CapitalizeFirst(funcPart))); _ = goName; return GoCallExpr(GoIdent(goName), []any{}) }() };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return GoSelectorExpr(GoIdent(sky_stringToLower(modPart)), funcPart) };  return nil }() }() }() }; return GoIdent(Compiler_Lower_SanitizeGoIdent(qualName)) }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }()
}

func Compiler_Lower_LowerCall(ctx any, callee any, args any) any {
	return func() any { flatResult := Compiler_Lower_FlattenCall(callee, args); _ = flatResult; flatCallee := sky_fst(flatResult); _ = flatCallee; flatArgs := sky_snd(flatResult); _ = flatArgs; argCount := sky_listLength(flatArgs); _ = argCount; partialResult := Compiler_Lower_CheckPartialApplication(ctx, flatCallee, argCount); _ = partialResult; return func() any { return func() any { __subject := partialResult; if sky_asSkyMaybe(__subject).SkyName == "Just" { closure := sky_asSkyMaybe(__subject).JustValue; _ = closure; return func() any { goArgs := sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Lower_LowerExpr(ctx, __pa0) }), flatArgs); _ = goArgs; return Compiler_Lower_GeneratePartialClosure(closure, goArgs, argCount) }() };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return func() any { goCallee := Compiler_Lower_LowerExpr(ctx, flatCallee); _ = goCallee; goArgs := sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Lower_LowerExpr(ctx, __pa0) }), flatArgs); _ = goArgs; return func() any { return func() any { __subject := goCallee; if sky_asMap(__subject)["SkyName"] == "GoCallExpr" { innerFn := sky_asMap(__subject)["V0"]; _ = innerFn; innerArgs := sky_asMap(__subject)["V1"]; _ = innerArgs; return func() any { if sky_asBool(sky_listIsEmpty(innerArgs)) { return GoCallExpr(innerFn, goArgs) }; return func() any { if sky_asBool(sky_equal(sky_listLength(goArgs), 1)) { return GoCallExpr(GoIdent("sky_call"), sky_call(sky_listAppend([]any{goCallee}), goArgs)) }; return func() any { if sky_asBool(sky_equal(sky_listLength(goArgs), 2)) { return GoCallExpr(GoIdent("sky_call2"), sky_call(sky_listAppend([]any{goCallee}), goArgs)) }; return func() any { if sky_asBool(sky_equal(sky_listLength(goArgs), 3)) { return GoCallExpr(GoIdent("sky_call3"), sky_call(sky_listAppend([]any{goCallee}), goArgs)) }; return GoCallExpr(GoIdent("sky_call"), sky_call(sky_listAppend([]any{goCallee}), goArgs)) }() }() }() }() };  if sky_asMap(__subject)["SkyName"] == "GoRawExpr" { code := sky_asMap(__subject)["V0"]; _ = code; return func() any { if sky_asBool(sky_call(sky_stringEndsWith("()"), code)) { return func() any { return func() any { __subject := goArgs; if len(sky_asList(__subject)) > 0 { singleArg := sky_asList(__subject)[0]; _ = singleArg; return GoCallExpr(GoIdent("sky_call"), []any{goCallee, singleArg}) };  if true { return GoCallExpr(goCallee, goArgs) };  return nil }() }() }; return GoCallExpr(goCallee, goArgs) }() };  if true { return GoCallExpr(goCallee, goArgs) };  return nil }() }() }() };  return nil }() }() }()
}

func Compiler_Lower_CheckPartialApplication(ctx any, callee any, argCount any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(sky_asMap(ctx)["modulePrefix"])) { return SkyNothing() }; return func() any { return func() any { __subject := callee; if sky_asMap(__subject)["SkyName"] == "IdentifierExpr" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { return func() any { __subject := sky_call(sky_dictGet(name), sky_asMap(ctx)["localFunctionArity"]); if sky_asSkyMaybe(__subject).SkyName == "Just" { arity := sky_asSkyMaybe(__subject).JustValue; _ = arity; return func() any { if sky_asBool(sky_asInt(argCount) < sky_asInt(arity)) { return SkyJust(map[string]any{"goFuncName": sky_asString(sky_asMap(ctx)["modulePrefix"]) + sky_asString(sky_asString("_") + sky_asString(Compiler_Lower_CapitalizeFirst(Compiler_Lower_SanitizeGoIdent(name)))), "totalArity": arity}) }; return SkyNothing() }() };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyNothing() };  if true { return SkyNothing() };  return nil }() }() };  return nil }() }() }()
}

func Compiler_Lower_GeneratePartialClosure(partial any, providedArgs any, providedCount any) any {
	return func() any { remainingCount := sky_asInt(sky_asMap(partial)["totalArity"]) - sky_asInt(providedCount); _ = remainingCount; remainingParams := sky_call(sky_listMap(func(i any) any { return sky_asString("__pa") + sky_asString(sky_stringFromInt(i)) }), Compiler_Lower_ListRange(0, sky_asInt(remainingCount) - sky_asInt(1))); _ = remainingParams; providedArgStrs := sky_call(sky_listMap(Compiler_Lower_EmitGoExprInline), providedArgs); _ = providedArgStrs; allArgStrs := sky_call(sky_listAppend(providedArgStrs), remainingParams); _ = allArgStrs; paramList := sky_call(sky_stringJoin(", "), sky_call(sky_listMap(func(p any) any { return sky_asString(p) + sky_asString(" any") }), remainingParams)); _ = paramList; argList := sky_call(sky_stringJoin(", "), allArgStrs); _ = argList; closureCode := sky_asString("func(") + sky_asString(sky_asString(paramList) + sky_asString(sky_asString(") any { return ") + sky_asString(sky_asString(sky_asMap(partial)["goFuncName"]) + sky_asString(sky_asString("(") + sky_asString(sky_asString(argList) + sky_asString(") }")))))); _ = closureCode; return GoRawExpr(closureCode) }()
}

func Compiler_Lower_ListRange(start any, end any) any {
	return func() any { if sky_asBool(sky_asInt(start) > sky_asInt(end)) { return []any{} }; return append([]any{start}, sky_asList(Compiler_Lower_ListRange(sky_asInt(start) + sky_asInt(1), end))...) }()
}

func Compiler_Lower_FlattenCall(callee any, args any) any {
	return func() any { return func() any { __subject := callee; if sky_asMap(__subject)["SkyName"] == "CallExpr" { innerCallee := sky_asMap(__subject)["V0"]; _ = innerCallee; innerArgs := sky_asMap(__subject)["V1"]; _ = innerArgs; return func() any { if sky_asBool(Compiler_Lower_IsStdlibCallee(innerCallee)) { return []any{} }; return Compiler_Lower_FlattenCall(innerCallee, sky_call(sky_listAppend(innerArgs), args)) }() };  if true { return []any{} };  return nil }() }()
}

func Compiler_Lower_LowerLambda(ctx any, params any, body any) any {
	return func() any { if sky_asBool(sky_listIsEmpty(params)) { return Compiler_Lower_LowerExpr(ctx, body) }; return func() any { if sky_asBool(sky_equal(sky_listLength(params), 1)) { return func() any { singleParam := func() any { return func() any { __subject := sky_listHead(params); if sky_asSkyMaybe(__subject).SkyName == "Just" { p := sky_asSkyMaybe(__subject).JustValue; _ = p; return p };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return PWildcard(emptySpan) };  return nil }() }(); _ = singleParam; paramStr := Compiler_Lower_EmitInlineParam(Compiler_Lower_LowerParam(singleParam)); _ = paramStr; bodyStr := Compiler_Lower_EmitGoExprInline(Compiler_Lower_LowerExpr(ctx, body)); _ = bodyStr; return GoRawExpr(sky_asString("func(") + sky_asString(sky_asString(paramStr) + sky_asString(sky_asString(") any { return ") + sky_asString(sky_asString(bodyStr) + sky_asString(" }"))))) }() }; return func() any { firstParam := func() any { return func() any { __subject := sky_listHead(params); if sky_asSkyMaybe(__subject).SkyName == "Just" { p := sky_asSkyMaybe(__subject).JustValue; _ = p; return p };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return PWildcard(emptySpan) };  return nil }() }(); _ = firstParam; restParams := sky_call(sky_listDrop(1), params); _ = restParams; paramStr := Compiler_Lower_EmitInlineParam(Compiler_Lower_LowerParam(firstParam)); _ = paramStr; innerStr := Compiler_Lower_EmitGoExprInline(Compiler_Lower_LowerLambda(ctx, restParams, body)); _ = innerStr; return GoRawExpr(sky_asString("func(") + sky_asString(sky_asString(paramStr) + sky_asString(sky_asString(") any { return ") + sky_asString(sky_asString(innerStr) + sky_asString(" }"))))) }() }() }()
}

func Compiler_Lower_LowerIf(ctx any, condition any, thenBranch any, elseBranch any) any {
	return GoRawExpr(sky_asString("func() any { if sky_asBool(") + sky_asString(sky_asString(Compiler_Lower_ExprToGoString(ctx, condition)) + sky_asString(sky_asString(") { return ") + sky_asString(sky_asString(Compiler_Lower_ExprToGoString(ctx, thenBranch)) + sky_asString(sky_asString(" }; return ") + sky_asString(sky_asString(Compiler_Lower_ExprToGoString(ctx, elseBranch)) + sky_asString(" }()")))))))
}

func Compiler_Lower_LowerLet(ctx any, bindings any, body any) any {
	return func() any { stmts := sky_call(sky_listConcatMap(func(__pa0 any) any { return Compiler_Lower_LowerLetBinding(ctx, __pa0) }), bindings); _ = stmts; returnStmt := []any{GoReturn(Compiler_Lower_LowerExpr(ctx, body))}; _ = returnStmt; allStmts := sky_call(sky_listAppend(stmts), returnStmt); _ = allStmts; return GoRawExpr(sky_asString("func() any { ") + sky_asString(sky_asString(Compiler_Lower_StmtsToGoString(allStmts)) + sky_asString(" }()"))) }()
}

func Compiler_Lower_LowerLetBinding(ctx any, binding any) any {
	return func() any { return func() any { __subject := sky_asMap(binding)["pattern"]; if sky_asMap(__subject)["SkyName"] == "PVariable" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { goName := Compiler_Lower_SanitizeGoIdent(name); _ = goName; return []any{GoShortDecl(goName, Compiler_Lower_LowerExpr(ctx, sky_asMap(binding)["value"])), GoExprStmt(GoRawExpr(sky_asString("_ = ") + sky_asString(goName)))} }() };  if sky_asMap(__subject)["SkyName"] == "PWildcard" { return []any{GoExprStmt(Compiler_Lower_LowerExpr(ctx, sky_asMap(binding)["value"]))} };  if sky_asMap(__subject)["SkyName"] == "PTuple" { items := sky_asMap(__subject)["V0"]; _ = items; return func() any { tmpName := sky_asString("__tup_") + sky_asString(Compiler_Lower_MakeTupleKey(items)); _ = tmpName; tmpDecl := GoShortDecl(tmpName, Compiler_Lower_LowerExpr(ctx, sky_asMap(binding)["value"])); _ = tmpDecl; tupleSize := sky_listLength(items); _ = tupleSize; extracts := Compiler_Lower_ExtractTupleBindings(tmpName, items, 0, tupleSize); _ = extracts; return append([]any{tmpDecl}, sky_asList(extracts)...) }() };  if true { return []any{GoShortDecl("_", Compiler_Lower_LowerExpr(ctx, sky_asMap(binding)["value"]))} };  return nil }() }()
}

func Compiler_Lower_ExtractTupleBindings(tmpName any, items any, idx any, tupleSize any) any {
	return func() any { return func() any { __subject := items; if len(sky_asList(__subject)) == 0 { return []any{} };  if len(sky_asList(__subject)) > 0 { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { fieldName := sky_asString("V") + sky_asString(sky_stringFromInt(idx)); _ = fieldName; assertFn := func() any { if sky_asBool(sky_asInt(tupleSize) >= sky_asInt(3)) { return "sky_asTuple3" }; return "sky_asTuple2" }(); _ = assertFn; extract := func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PVariable" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { goName := Compiler_Lower_SanitizeGoIdent(name); _ = goName; return []any{GoShortDecl(goName, GoSelectorExpr(GoCallExpr(GoIdent(assertFn), []any{GoIdent(tmpName)}), fieldName)), GoExprStmt(GoRawExpr(sky_asString("_ = ") + sky_asString(goName)))} }() };  if true { return []any{} };  return nil }() }(); _ = extract; return sky_call(sky_listAppend(extract), Compiler_Lower_ExtractTupleBindings(tmpName, rest, sky_asInt(idx) + sky_asInt(1), tupleSize)) }() };  return nil }() }()
}

func Compiler_Lower_LowerCase(ctx any, subject any, branches any) any {
	return func() any { subjectExpr := Compiler_Lower_LowerExpr(ctx, subject); _ = subjectExpr; switchCode := Compiler_Lower_LowerCaseToSwitch(ctx, subjectExpr, branches); _ = switchCode; return GoCallExpr(GoFuncLit([]any{}, GoRawExpr(switchCode)), []any{}) }()
}

func Compiler_Lower_LowerCaseToSwitch(ctx any, subjectExpr any, branches any) any {
	return func() any { subjectCode := Compiler_Lower_EmitGoExprInline(subjectExpr); _ = subjectCode; return sky_asString("func() any { __subject := ") + sky_asString(sky_asString(subjectCode) + sky_asString(sky_asString("; ") + sky_asString(sky_asString(sky_call(sky_stringJoin(" "), sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Lower_EmitBranchCode(ctx, __pa0) }), Compiler_Lower_ZipIndex(branches)))) + sky_asString(" return nil }()")))) }()
}

func Compiler_Lower_EmitBranchCode(ctx any, pair any) any {
	return func() any { branch := sky_snd(pair); _ = branch; return func() any { condition := Compiler_Lower_PatternToCondition(ctx, "__subject", sky_asMap(branch)["pattern"]); _ = condition; bindings := Compiler_Lower_PatternToBindings(ctx, "__subject", sky_asMap(branch)["pattern"]); _ = bindings; bodyCode := Compiler_Lower_ExprToGoString(ctx, sky_asMap(branch)["body"]); _ = bodyCode; return sky_asString("if ") + sky_asString(sky_asString(condition) + sky_asString(sky_asString(" { ") + sky_asString(sky_asString(bindings) + sky_asString(sky_asString("return ") + sky_asString(sky_asString(bodyCode) + sky_asString(" }; ")))))) }() }()
}

func Compiler_Lower_PatternToCondition(ctx any, varName any, pat any) any {
	return func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PWildcard" { return "true" };  if sky_asMap(__subject)["SkyName"] == "PVariable" { return "true" };  if sky_asMap(__subject)["SkyName"] == "PLiteral" { lit := sky_asMap(__subject)["V0"]; _ = lit; return func() any { return func() any { __subject := lit; if sky_asMap(__subject)["SkyName"] == "LitInt" { n := sky_asMap(__subject)["V0"]; _ = n; return sky_asString("sky_asInt(") + sky_asString(sky_asString(varName) + sky_asString(sky_asString(") == ") + sky_asString(sky_stringFromInt(n)))) };  if sky_asMap(__subject)["SkyName"] == "LitFloat" { f := sky_asMap(__subject)["V0"]; _ = f; return sky_asString("sky_asFloat(") + sky_asString(sky_asString(varName) + sky_asString(sky_asString(") == ") + sky_asString(sky_stringFromFloat(f)))) };  if sky_asMap(__subject)["SkyName"] == "LitString" { s := sky_asMap(__subject)["V0"]; _ = s; return sky_asString("sky_asString(") + sky_asString(sky_asString(varName) + sky_asString(sky_asString(") == ") + sky_asString(Compiler_Lower_GoQuote(s)))) };  if sky_asMap(__subject)["SkyName"] == "LitBool" { b := sky_asMap(__subject)["V0"]; _ = b; return func() any { if sky_asBool(b) { return sky_asString("sky_asBool(") + sky_asString(sky_asString(varName) + sky_asString(") == true")) }; return sky_asString("sky_asBool(") + sky_asString(sky_asString(varName) + sky_asString(") == false")) }() };  if sky_asMap(__subject)["SkyName"] == "LitChar" { c := sky_asMap(__subject)["V0"]; _ = c; return sky_asString("sky_asString(") + sky_asString(sky_asString(varName) + sky_asString(sky_asString(") == \"") + sky_asString(sky_asString(c) + sky_asString("\"")))) };  if sky_asMap(__subject)["SkyName"] == "PConstructor" { parts := sky_asMap(__subject)["V0"]; _ = parts; return func() any { ctorName := Compiler_Lower_LastPartOf(parts); _ = ctorName; return func() any { if sky_asBool(sky_asBool(sky_equal(ctorName, "Ok")) || sky_asBool(sky_equal(ctorName, "Err"))) { return sky_asString("sky_asSkyResult(") + sky_asString(sky_asString(varName) + sky_asString(sky_asString(").SkyName == \"") + sky_asString(sky_asString(ctorName) + sky_asString("\"")))) }; return func() any { if sky_asBool(sky_asBool(sky_equal(ctorName, "Just")) || sky_asBool(sky_equal(ctorName, "Nothing"))) { return sky_asString("sky_asSkyMaybe(") + sky_asString(sky_asString(varName) + sky_asString(sky_asString(").SkyName == \"") + sky_asString(sky_asString(ctorName) + sky_asString("\"")))) }; return func() any { if sky_asBool(sky_equal(ctorName, "True")) { return sky_asString("sky_asBool(") + sky_asString(sky_asString(varName) + sky_asString(") == true")) }; return func() any { if sky_asBool(sky_equal(ctorName, "False")) { return sky_asString("sky_asBool(") + sky_asString(sky_asString(varName) + sky_asString(") == false")) }; return sky_asString("sky_asMap(") + sky_asString(sky_asString(varName) + sky_asString(sky_asString(")[\"SkyName\"] == \"") + sky_asString(sky_asString(ctorName) + sky_asString("\"")))) }() }() }() }() }() };  if sky_asMap(__subject)["SkyName"] == "PTuple" { return "true" };  if sky_asMap(__subject)["SkyName"] == "PList" { items := sky_asMap(__subject)["V0"]; _ = items; return sky_asString("len(sky_asList(") + sky_asString(sky_asString(varName) + sky_asString(sky_asString(")) == ") + sky_asString(sky_stringFromInt(sky_listLength(items))))) };  if sky_asMap(__subject)["SkyName"] == "PCons" { return sky_asString("len(sky_asList(") + sky_asString(sky_asString(varName) + sky_asString(")) > 0")) };  if sky_asMap(__subject)["SkyName"] == "PAs" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Compiler_Lower_PatternToCondition(ctx, varName, inner) };  if sky_asMap(__subject)["SkyName"] == "PRecord" { return "true" };  return nil }() }() };  return nil }() }()
}

func Compiler_Lower_PatternToBindings(ctx any, varName any, pat any) any {
	return func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PWildcard" { return "" };  if sky_asMap(__subject)["SkyName"] == "PVariable" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { goName := Compiler_Lower_SanitizeGoIdent(name); _ = goName; return sky_asString(goName) + sky_asString(sky_asString(" := ") + sky_asString(sky_asString(varName) + sky_asString(sky_asString("; _ = ") + sky_asString(sky_asString(goName) + sky_asString("; "))))) }() };  if sky_asMap(__subject)["SkyName"] == "PLiteral" { return "" };  if sky_asMap(__subject)["SkyName"] == "PConstructor" { parts := sky_asMap(__subject)["V0"]; _ = parts; argPats := sky_asMap(__subject)["V1"]; _ = argPats; return func() any { ctorName := Compiler_Lower_LastPartOf(parts); _ = ctorName; return func() any { if sky_asBool(sky_equal(ctorName, "Ok")) { return Compiler_Lower_BindConstructorArgs(ctx, varName, "sky_asSkyResult", "OkValue", argPats) }; return func() any { if sky_asBool(sky_equal(ctorName, "Err")) { return Compiler_Lower_BindConstructorArgs(ctx, varName, "sky_asSkyResult", "ErrValue", argPats) }; return func() any { if sky_asBool(sky_equal(ctorName, "Just")) { return Compiler_Lower_BindConstructorArgs(ctx, varName, "sky_asSkyMaybe", "JustValue", argPats) }; return Compiler_Lower_BindAdtConstructorArgs(ctx, varName, argPats, 0) }() }() }() }() };  if sky_asMap(__subject)["SkyName"] == "PTuple" { items := sky_asMap(__subject)["V0"]; _ = items; return Compiler_Lower_BindTupleArgs(ctx, varName, items, 0) };  if sky_asMap(__subject)["SkyName"] == "PList" { items := sky_asMap(__subject)["V0"]; _ = items; return Compiler_Lower_BindListArgs(ctx, varName, items, 0) };  if sky_asMap(__subject)["SkyName"] == "PCons" { headPat := sky_asMap(__subject)["V0"]; _ = headPat; tailPat := sky_asMap(__subject)["V1"]; _ = tailPat; return func() any { headBinding := Compiler_Lower_PatternToBindings(ctx, sky_asString("sky_asList(") + sky_asString(sky_asString(varName) + sky_asString(")[0]")), headPat); _ = headBinding; tailBinding := Compiler_Lower_PatternToBindings(ctx, sky_asString("sky_asList(") + sky_asString(sky_asString(varName) + sky_asString(")[1:]")), tailPat); _ = tailBinding; return sky_asString(headBinding) + sky_asString(tailBinding) }() };  if sky_asMap(__subject)["SkyName"] == "PAs" { inner := sky_asMap(__subject)["V0"]; _ = inner; name := sky_asMap(__subject)["V1"]; _ = name; return sky_asString(Compiler_Lower_SanitizeGoIdent(name)) + sky_asString(sky_asString(" := ") + sky_asString(sky_asString(varName) + sky_asString(sky_asString("; ") + sky_asString(Compiler_Lower_PatternToBindings(ctx, varName, inner))))) };  if sky_asMap(__subject)["SkyName"] == "PRecord" { fields := sky_asMap(__subject)["V0"]; _ = fields; return sky_call(sky_call(sky_listFoldl(func(f any) any { return func(acc any) any { return sky_asString(acc) + sky_asString(sky_asString(Compiler_Lower_SanitizeGoIdent(f)) + sky_asString(sky_asString(" := sky_asMap(") + sky_asString(sky_asString(varName) + sky_asString(sky_asString(")[\"") + sky_asString(sky_asString(f) + sky_asString("\"]; ")))))) } }), ""), fields) };  return nil }() }()
}

func Compiler_Lower_BindConstructorArgs(ctx any, varName any, wrapperFn any, fieldName any, argPats any) any {
	return func() any { return func() any { __subject := argPats; if len(sky_asList(__subject)) > 0 { onePat := sky_asList(__subject)[0]; _ = onePat; return Compiler_Lower_PatternToBindings(ctx, sky_asString(wrapperFn) + sky_asString(sky_asString("(") + sky_asString(sky_asString(varName) + sky_asString(sky_asString(").") + sky_asString(fieldName)))), onePat) };  if true { return "" };  return nil }() }()
}

func Compiler_Lower_BindAdtConstructorArgs(ctx any, varName any, argPats any, idx any) any {
	return func() any { return func() any { __subject := argPats; if len(sky_asList(__subject)) == 0 { return "" };  if len(sky_asList(__subject)) > 0 { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { fieldAccess := sky_asString("sky_asMap(") + sky_asString(sky_asString(varName) + sky_asString(sky_asString(")[\"V") + sky_asString(sky_asString(sky_stringFromInt(idx)) + sky_asString("\"]")))); _ = fieldAccess; binding := Compiler_Lower_PatternToBindings(ctx, fieldAccess, pat); _ = binding; return sky_asString(binding) + sky_asString(Compiler_Lower_BindAdtConstructorArgs(ctx, varName, rest, sky_asInt(idx) + sky_asInt(1))) }() };  return nil }() }()
}

func Compiler_Lower_BindTupleArgs(ctx any, varName any, items any, idx any) any {
	return func() any { return func() any { __subject := items; if len(sky_asList(__subject)) == 0 { return "" };  if len(sky_asList(__subject)) > 0 { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { fieldAccess := sky_asString("sky_asTuple2(") + sky_asString(sky_asString(varName) + sky_asString(sky_asString(").V") + sky_asString(sky_stringFromInt(idx)))); _ = fieldAccess; binding := Compiler_Lower_PatternToBindings(ctx, fieldAccess, pat); _ = binding; return sky_asString(binding) + sky_asString(Compiler_Lower_BindTupleArgs(ctx, varName, rest, sky_asInt(idx) + sky_asInt(1))) }() };  return nil }() }()
}

func Compiler_Lower_BindListArgs(ctx any, varName any, items any, idx any) any {
	return func() any { return func() any { __subject := items; if len(sky_asList(__subject)) == 0 { return "" };  if len(sky_asList(__subject)) > 0 { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { elemAccess := sky_asString("sky_asList(") + sky_asString(sky_asString(varName) + sky_asString(sky_asString(")[") + sky_asString(sky_asString(sky_stringFromInt(idx)) + sky_asString("]")))); _ = elemAccess; binding := Compiler_Lower_PatternToBindings(ctx, elemAccess, pat); _ = binding; return sky_asString(binding) + sky_asString(Compiler_Lower_BindListArgs(ctx, varName, rest, sky_asInt(idx) + sky_asInt(1))) }() };  return nil }() }()
}

func Compiler_Lower_LowerBinary(ctx any, op any, left any, right any) any {
	return func() any { goLeft := Compiler_Lower_LowerExpr(ctx, left); _ = goLeft; goRight := Compiler_Lower_LowerExpr(ctx, right); _ = goRight; return func() any { if sky_asBool(sky_equal(op, "|>")) { return GoCallExpr(goRight, []any{goLeft}) }; return func() any { if sky_asBool(sky_equal(op, "<|")) { return GoCallExpr(goLeft, []any{goRight}) }; return func() any { if sky_asBool(sky_equal(op, "::")) { return GoCallExpr(GoIdent("append"), []any{GoSliceLit([]any{goLeft}), GoRawExpr(sky_asString("sky_asList(") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(goRight)) + sky_asString(")...")))}) }; return func() any { if sky_asBool(sky_equal(op, "++")) { return GoRawExpr(sky_asString("sky_asString(") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(goLeft)) + sky_asString(sky_asString(") + sky_asString(") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(goRight)) + sky_asString(")"))))) }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "+")) || sky_asBool(sky_asBool(sky_equal(op, "-")) || sky_asBool(sky_asBool(sky_equal(op, "*")) || sky_asBool(sky_equal(op, "%"))))) { return GoRawExpr(sky_asString("sky_asInt(") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(goLeft)) + sky_asString(sky_asString(") ") + sky_asString(sky_asString(op) + sky_asString(sky_asString(" sky_asInt(") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(goRight)) + sky_asString(")"))))))) }; return func() any { if sky_asBool(sky_equal(op, "/")) { return GoRawExpr(sky_asString("sky_asFloat(") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(goLeft)) + sky_asString(sky_asString(") / sky_asFloat(") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(goRight)) + sky_asString(")"))))) }; return func() any { if sky_asBool(sky_equal(op, "//")) { return GoRawExpr(sky_asString("sky_asInt(") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(goLeft)) + sky_asString(sky_asString(") / sky_asInt(") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(goRight)) + sky_asString(")"))))) }; return func() any { if sky_asBool(sky_equal(op, "/=")) { return GoRawExpr(sky_asString("!sky_equal(") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(goLeft)) + sky_asString(sky_asString(", ") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(goRight)) + sky_asString(")"))))) }; return func() any { if sky_asBool(sky_equal(op, "==")) { return GoCallExpr(GoIdent("sky_equal"), []any{goLeft, goRight}) }; return func() any { if sky_asBool(sky_equal(op, "!=")) { return GoUnaryExpr("!", GoCallExpr(GoIdent("sky_equal"), []any{goLeft, goRight})) }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "<")) || sky_asBool(sky_asBool(sky_equal(op, "<=")) || sky_asBool(sky_asBool(sky_equal(op, ">")) || sky_asBool(sky_equal(op, ">="))))) { return GoRawExpr(sky_asString("sky_asInt(") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(goLeft)) + sky_asString(sky_asString(") ") + sky_asString(sky_asString(op) + sky_asString(sky_asString(" sky_asInt(") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(goRight)) + sky_asString(")"))))))) }; return func() any { if sky_asBool(sky_equal(op, "&&")) { return GoRawExpr(sky_asString("sky_asBool(") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(goLeft)) + sky_asString(sky_asString(") && sky_asBool(") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(goRight)) + sky_asString(")"))))) }; return func() any { if sky_asBool(sky_equal(op, "||")) { return GoRawExpr(sky_asString("sky_asBool(") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(goLeft)) + sky_asString(sky_asString(") || sky_asBool(") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(goRight)) + sky_asString(")"))))) }; return GoBinaryExpr(op, goLeft, goRight) }() }() }() }() }() }() }() }() }() }() }() }() }() }()
}

func Compiler_Lower_LowerRecordUpdate(ctx any, base any, fields any) any {
	return func() any { goBase := Compiler_Lower_LowerExpr(ctx, base); _ = goBase; goFields := GoMapLit(sky_call(sky_listMap(func(f any) any { return []any{} }), fields)); _ = goFields; return GoCallExpr(GoIdent("sky_recordUpdate"), []any{goBase, goFields}) }()
}

func Compiler_Lower_GenerateConstructorDecls(registry any, decls any) any {
	return sky_call(sky_listConcatMap(func(__pa0 any) any { return Compiler_Lower_GenerateCtorsForDecl(registry, __pa0) }), decls)
}

func Compiler_Lower_GenerateCtorsForDecl(registry any, decl any) any {
	return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "TypeDecl" { typeName := sky_asMap(__subject)["V0"]; _ = typeName; variants := sky_asMap(__subject)["V2"]; _ = variants; return sky_call(sky_listIndexedMap(func(__pa0 any, __pa1 any) any { return Compiler_Lower_GenerateCtorFunc(typeName, __pa0, __pa1) }), variants) };  if true { return []any{} };  return nil }() }()
}

func Compiler_Lower_GenerateCtorFunc(typeName any, tagIndex any, variant any) any {
	return func() any { arity := sky_listLength(sky_asMap(variant)["fields"]); _ = arity; return func() any { if sky_asBool(sky_equal(arity, 0)) { return GoDeclVar(Compiler_Lower_SanitizeGoIdent(sky_asMap(variant)["name"]), GoMapLit([]any{[]any{}, []any{}})) }; return func() any { params := sky_call(sky_listIndexedMap(func(i any) any { return func(_ any) any { return map[string]any{"name": sky_asString("v") + sky_asString(sky_stringFromInt(i)), "type_": "any"} } }), sky_asMap(variant)["fields"]); _ = params; fields := sky_asString([]any{[]any{}, []any{}}) + sky_asString(sky_call(sky_listIndexedMap(func(i any) any { return func(_ any) any { return []any{} } }), sky_asMap(variant)["fields"])); _ = fields; return GoDeclFunc(map[string]any{"name": Compiler_Lower_SanitizeGoIdent(sky_asMap(variant)["name"]), "params": params, "returnType": "any", "body": []any{GoReturn(GoMapLit(sky_call(sky_listMap(func(pair any) any { return []any{} }), fields)))}}) }() }() }()
}

func Compiler_Lower_GenerateHelperDecls() any {
	return []any{GoDeclRaw("type SkyTuple2 struct { V0, V1 any }"), GoDeclRaw("type SkyTuple3 struct { V0, V1, V2 any }"), GoDeclRaw("type SkyResult struct { Tag int; SkyName string; OkValue, ErrValue any }"), GoDeclRaw("type SkyMaybe struct { Tag int; SkyName string; JustValue any }"), GoDeclRaw("func SkyOk(v any) SkyResult { return SkyResult{Tag: 0, SkyName: \\\"Ok\\\", OkValue: v} }"), GoDeclRaw("func SkyErr(v any) SkyResult { return SkyResult{Tag: 1, SkyName: \\\"Err\\\", ErrValue: v} }"), GoDeclRaw("func SkyJust(v any) SkyMaybe { return SkyMaybe{Tag: 0, SkyName: \\\"Just\\\", JustValue: v} }"), GoDeclRaw("func SkyNothing() SkyMaybe { return SkyMaybe{Tag: 1, SkyName: \\\"Nothing\\\"} }"), GoDeclRaw("func sky_asInt(v any) int { switch x := v.(type) { case int: return x; case float64: return int(x); default: return 0 } }"), GoDeclRaw("func sky_asFloat(v any) float64 { switch x := v.(type) { case float64: return x; case int: return float64(x); default: return 0 } }"), GoDeclRaw("func sky_asString(v any) string { if s, ok := v.(string); ok { return s }; return fmt.Sprintf(\\\"%v\\\", v) }"), GoDeclRaw("func sky_asBool(v any) bool { if b, ok := v.(bool); ok { return b }; return false }"), GoDeclRaw("func sky_asList(v any) []any { if l, ok := v.([]any); ok { return l }; return nil }"), GoDeclRaw("func sky_asMap(v any) map[string]any { if m, ok := v.(map[string]any); ok { return m }; return nil }"), GoDeclRaw("func sky_equal(a, b any) bool { return fmt.Sprintf(\\\"%v\\\", a) == fmt.Sprintf(\\\"%v\\\", b) }"), GoDeclRaw("func sky_stringFromInt(v any) any { return strconv.Itoa(sky_asInt(v)) }"), GoDeclRaw("func sky_stringFromFloat(v any) any { return strconv.FormatFloat(sky_asFloat(v), 'f', -1, 64) }"), GoDeclRaw("func sky_stringToUpper(v any) any { return strings.ToUpper(sky_asString(v)) }"), GoDeclRaw("func sky_stringToLower(v any) any { return strings.ToLower(sky_asString(v)) }"), GoDeclRaw("func sky_stringLength(v any) any { return len(sky_asString(v)) }"), GoDeclRaw("func sky_stringTrim(v any) any { return strings.TrimSpace(sky_asString(v)) }"), GoDeclRaw("func sky_stringContains(sub any) any { return func(s any) any { return strings.Contains(sky_asString(s), sky_asString(sub)) } }"), GoDeclRaw("func sky_stringStartsWith(prefix any) any { return func(s any) any { return strings.HasPrefix(sky_asString(s), sky_asString(prefix)) } }"), GoDeclRaw("func sky_stringEndsWith(suffix any) any { return func(s any) any { return strings.HasSuffix(sky_asString(s), sky_asString(suffix)) } }"), GoDeclRaw("func sky_stringSplit(sep any) any { return func(s any) any { parts := strings.Split(sky_asString(s), sky_asString(sep)); result := make([]any, len(parts)); for i, p := range parts { result[i] = p }; return result } }"), GoDeclRaw("func sky_stringReplace(old any) any { return func(new_ any) any { return func(s any) any { return strings.ReplaceAll(sky_asString(s), sky_asString(old), sky_asString(new_)) } } }"), GoDeclRaw("func sky_stringToInt(s any) any { n, err := strconv.Atoi(strings.TrimSpace(sky_asString(s))); if err != nil { return SkyNothing() }; return SkyJust(n) }"), GoDeclRaw("func sky_stringToFloat(s any) any { f, err := strconv.ParseFloat(strings.TrimSpace(sky_asString(s)), 64); if err != nil { return SkyNothing() }; return SkyJust(f) }"), GoDeclRaw("func sky_stringAppend(a any) any { return func(b any) any { return sky_asString(a) + sky_asString(b) } }"), GoDeclRaw("func sky_stringIsEmpty(v any) any { return sky_asString(v) == \\\"\\\" }"), GoDeclRaw("func sky_stringSlice(start any) any { return func(end any) any { return func(s any) any { str := sky_asString(s); return str[sky_asInt(start):sky_asInt(end)] } } }"), GoDeclRaw("func sky_stringJoin(sep any) any { return func(list any) any { parts := sky_asList(list); ss := make([]string, len(parts)); for i, p := range parts { ss[i] = sky_asString(p) }; return strings.Join(ss, sky_asString(sep)) } }"), GoDeclRaw("func sky_listMap(fn any) any { return func(list any) any { items := sky_asList(list); result := make([]any, len(items)); for i, item := range items { result[i] = fn.(func(any) any)(item) }; return result } }"), GoDeclRaw("func sky_listFilter(fn any) any { return func(list any) any { items := sky_asList(list); var result []any; for _, item := range items { if sky_asBool(fn.(func(any) any)(item)) { result = append(result, item) } }; return result } }"), GoDeclRaw("func sky_listFoldl(fn any) any { return func(init any) any { return func(list any) any { acc := init; for _, item := range sky_asList(list) { acc = fn.(func(any) any)(item).(func(any) any)(acc) }; return acc } } }"), GoDeclRaw("func sky_listLength(list any) any { return len(sky_asList(list)) }"), GoDeclRaw("func sky_listHead(list any) any { items := sky_asList(list); if len(items) > 0 { return SkyJust(items[0]) }; return SkyNothing() }"), GoDeclRaw("func sky_listReverse(list any) any { items := sky_asList(list); result := make([]any, len(items)); for i, item := range items { result[len(items)-1-i] = item }; return result }"), GoDeclRaw("func sky_listIsEmpty(list any) any { return len(sky_asList(list)) == 0 }"), GoDeclRaw("func sky_listAppend(a any) any { return func(b any) any { return append(sky_asList(a), sky_asList(b)...) } }"), GoDeclRaw("func sky_listConcatMap(fn any) any { return func(list any) any { var result []any; for _, item := range sky_asList(list) { result = append(result, sky_asList(fn.(func(any) any)(item))...) }; if result == nil { return []any{} }; return result } }"), GoDeclRaw("func sky_listConcat(lists any) any { var result []any; for _, l := range sky_asList(lists) { result = append(result, sky_asList(l)...) }; if result == nil { return []any{} }; return result }"), GoDeclRaw("func sky_listFilterMap(fn any) any { return func(list any) any { var result []any; for _, item := range sky_asList(list) { r := fn.(func(any) any)(item); if m, ok := r.(SkyMaybe); ok && m.Tag == 0 { result = append(result, m.JustValue) } }; if result == nil { return []any{} }; return result } }"), GoDeclRaw("func sky_listIndexedMap(fn any) any { return func(list any) any { items := sky_asList(list); result := make([]any, len(items)); for i, item := range items { result[i] = fn.(func(any) any)(i).(func(any) any)(item) }; return result } }"), GoDeclRaw("func sky_listDrop(n any) any { return func(list any) any { items := sky_asList(list); c := sky_asInt(n); if c >= len(items) { return []any{} }; return items[c:] } }"), GoDeclRaw("func sky_listMember(item any) any { return func(list any) any { for _, x := range sky_asList(list) { if sky_equal(x, item) { return true } }; return false } }"), GoDeclRaw("func sky_recordUpdate(base any, updates any) any { m := sky_asMap(base); result := make(map[string]any); for k, v := range m { result[k] = v }; for k, v := range sky_asMap(updates) { result[k] = v }; return result }"), GoDeclRaw("func sky_println(args ...any) any { ss := make([]any, len(args)); for i, a := range args { ss[i] = sky_asString(a) }; fmt.Println(ss...); return struct{}{} }"), GoDeclRaw("func sky_exit(code any) any { os.Exit(sky_asInt(code)); return struct{}{} }"), GoDeclRaw("var _ = strings.Contains"), GoDeclRaw("var _ = strconv.Itoa"), GoDeclRaw("var _ = os.Exit"), GoDeclRaw("func sky_asSkyResult(v any) SkyResult { if r, ok := v.(SkyResult); ok { return r }; return SkyResult{} }"), GoDeclRaw("func sky_asSkyMaybe(v any) SkyMaybe { if m, ok := v.(SkyMaybe); ok { return m }; return SkyMaybe{Tag: 1, SkyName: \\\"Nothing\\\"} }"), GoDeclRaw("func sky_asTuple2(v any) SkyTuple2 { if t, ok := v.(SkyTuple2); ok { return t }; return SkyTuple2{} }"), GoDeclRaw("func sky_asTuple3(v any) SkyTuple3 { if t, ok := v.(SkyTuple3); ok { return t }; return SkyTuple3{} }"), GoDeclRaw("func sky_not(v any) any { return !sky_asBool(v) }"), GoDeclRaw("func sky_fileRead(path any) any { data, err := os.ReadFile(sky_asString(path)); if err != nil { return SkyErr(err.Error()) }; return SkyOk(string(data)) }"), GoDeclRaw("func sky_fileWrite(path any) any { return func(content any) any { err := os.WriteFile(sky_asString(path), []byte(sky_asString(content)), 0644); if err != nil { return SkyErr(err.Error()) }; return SkyOk(struct{}{}) } }"), GoDeclRaw("func sky_fileMkdirAll(path any) any { err := os.MkdirAll(sky_asString(path), 0755); if err != nil { return SkyErr(err.Error()) }; return SkyOk(struct{}{}) }"), GoDeclRaw("func sky_processRun(cmd any) any { return func(args any) any { argStrs := sky_asList(args); cmdArgs := make([]string, len(argStrs)); for i, a := range argStrs { cmdArgs[i] = sky_asString(a) }; out, err := exec.Command(sky_asString(cmd), cmdArgs...).CombinedOutput(); if err != nil { return SkyErr(err.Error() + \\\": \\\" + string(out)) }; return SkyOk(string(out)) } }"), GoDeclRaw("func sky_processExit(code any) any { os.Exit(sky_asInt(code)); return struct{}{} }"), GoDeclRaw("func sky_processGetArgs(u any) any { args := make([]any, len(os.Args)); for i, a := range os.Args { args[i] = a }; return args }"), GoDeclRaw("func sky_processGetArg(n any) any { idx := sky_asInt(n); if idx < len(os.Args) { return SkyJust(os.Args[idx]) }; return SkyNothing() }"), GoDeclRaw("func sky_refNew(v any) any { type SkyRef struct { Value any }; return &SkyRef{Value: v} }"), GoDeclRaw("type SkyRef struct { Value any }"), GoDeclRaw("func sky_refGet(r any) any { return r.(*SkyRef).Value }"), GoDeclRaw("func sky_refSet(v any) any { return func(r any) any { r.(*SkyRef).Value = v; return struct{}{} } }"), GoDeclRaw("func sky_dictEmpty() any { return map[string]any{} }"), GoDeclRaw("func sky_dictInsert(k any) any { return func(v any) any { return func(d any) any { m := sky_asMap(d); result := make(map[string]any, len(m)+1); for key, val := range m { result[key] = val }; result[sky_asString(k)] = v; return result } } }"), GoDeclRaw("func sky_dictGet(k any) any { return func(d any) any { m := sky_asMap(d); if v, ok := m[sky_asString(k)]; ok { return SkyJust(v) }; return SkyNothing() } }"), GoDeclRaw("func sky_dictKeys(d any) any { m := sky_asMap(d); keys := make([]any, 0, len(m)); for k := range m { keys = append(keys, k) }; return keys }"), GoDeclRaw("func sky_dictValues(d any) any { m := sky_asMap(d); vals := make([]any, 0, len(m)); for _, v := range m { vals = append(vals, v) }; return vals }"), GoDeclRaw("func sky_dictToList(d any) any { m := sky_asMap(d); pairs := make([]any, 0, len(m)); for k, v := range m { pairs = append(pairs, SkyTuple2{k, v}) }; return pairs }"), GoDeclRaw("func sky_dictFromList(list any) any { result := make(map[string]any); for _, item := range sky_asList(list) { t := sky_asTuple2(item); result[sky_asString(t.V0)] = t.V1 }; return result }"), GoDeclRaw("func sky_dictMap(fn any) any { return func(d any) any { m := sky_asMap(d); result := make(map[string]any, len(m)); for k, v := range m { result[k] = fn.(func(any) any)(k).(func(any) any)(v) }; return result } }"), GoDeclRaw("func sky_dictFoldl(fn any) any { return func(init any) any { return func(d any) any { acc := init; for k, v := range sky_asMap(d) { acc = fn.(func(any) any)(k).(func(any) any)(v).(func(any) any)(acc) }; return acc } } }"), GoDeclRaw("func sky_dictUnion(a any) any { return func(b any) any { ma, mb := sky_asMap(a), sky_asMap(b); result := make(map[string]any, len(ma)+len(mb)); for k, v := range mb { result[k] = v }; for k, v := range ma { result[k] = v }; return result } }"), GoDeclRaw("func sky_dictRemove(k any) any { return func(d any) any { m := sky_asMap(d); result := make(map[string]any, len(m)); key := sky_asString(k); for k2, v := range m { if k2 != key { result[k2] = v } }; return result } }"), GoDeclRaw("func sky_dictMember(k any) any { return func(d any) any { _, ok := sky_asMap(d)[sky_asString(k)]; return ok } }"), GoDeclRaw("func sky_setEmpty() any { return map[string]bool{} }"), GoDeclRaw("func sky_setSingleton(v any) any { return map[string]bool{sky_asString(v): true} }"), GoDeclRaw("func sky_setInsert(v any) any { return func(s any) any { m := s.(map[string]bool); result := make(map[string]bool, len(m)+1); for k := range m { result[k] = true }; result[sky_asString(v)] = true; return result } }"), GoDeclRaw("func sky_setMember(v any) any { return func(s any) any { return s.(map[string]bool)[sky_asString(v)] } }"), GoDeclRaw("func sky_setUnion(a any) any { return func(b any) any { ma, mb := a.(map[string]bool), b.(map[string]bool); result := make(map[string]bool, len(ma)+len(mb)); for k := range mb { result[k] = true }; for k := range ma { result[k] = true }; return result } }"), GoDeclRaw("func sky_setDiff(a any) any { return func(b any) any { ma, mb := a.(map[string]bool), b.(map[string]bool); result := make(map[string]bool); for k := range ma { if !mb[k] { result[k] = true } }; return result } }"), GoDeclRaw("func sky_setToList(s any) any { m := s.(map[string]bool); result := make([]any, 0, len(m)); for k := range m { result = append(result, k) }; return result }"), GoDeclRaw("func sky_setFromList(list any) any { result := make(map[string]bool); for _, item := range sky_asList(list) { result[sky_asString(item)] = true }; return result }"), GoDeclRaw("func sky_setIsEmpty(s any) any { return len(s.(map[string]bool)) == 0 }"), GoDeclRaw("func sky_setRemove(v any) any { return func(s any) any { m := s.(map[string]bool); result := make(map[string]bool, len(m)); key := sky_asString(v); for k := range m { if k != key { result[k] = true } }; return result } }"), GoDeclRaw("func sky_readLine(u any) any { if stdinReader == nil { stdinReader = bufio.NewReader(os.Stdin) }; line, err := stdinReader.ReadString('\\\\n'); if err != nil && len(line) == 0 { return SkyNothing() }; return SkyJust(strings.TrimRight(line, \\\"\\\\r\\\\n\\\")) }"), GoDeclRaw("func sky_readBytes(n any) any { if stdinReader == nil { stdinReader = bufio.NewReader(os.Stdin) }; count := sky_asInt(n); buf := make([]byte, count); total := 0; for total < count { nr, err := stdinReader.Read(buf[total:]); total += nr; if err != nil { break } }; if total == 0 { return SkyNothing() }; return SkyJust(string(buf[:total])) }"), GoDeclRaw("func sky_writeStdout(s any) any { fmt.Print(sky_asString(s)); return struct{}{} }"), GoDeclRaw("func sky_writeStderr(s any) any { fmt.Fprint(os.Stderr, sky_asString(s)); return struct{}{} }"), GoDeclRaw("var stdinReader *bufio.Reader"), GoDeclRaw("func sky_charIsUpper(c any) any { s := sky_asString(c); if len(s) > 0 { r := rune(s[0]); return r >= 'A' && r <= 'Z' }; return false }"), GoDeclRaw("func sky_charIsLower(c any) any { s := sky_asString(c); if len(s) > 0 { r := rune(s[0]); return r >= 'a' && r <= 'z' }; return false }"), GoDeclRaw("func sky_charIsDigit(c any) any { s := sky_asString(c); if len(s) > 0 { r := rune(s[0]); return r >= '0' && r <= '9' }; return false }"), GoDeclRaw("func sky_charIsAlpha(c any) any { s := sky_asString(c); if len(s) > 0 { r := rune(s[0]); return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') }; return false }"), GoDeclRaw("func sky_charIsAlphaNum(c any) any { s := sky_asString(c); if len(s) > 0 { r := rune(s[0]); return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') }; return false }"), GoDeclRaw("func sky_charToUpper(c any) any { return strings.ToUpper(sky_asString(c)) }"), GoDeclRaw("func sky_charToLower(c any) any { return strings.ToLower(sky_asString(c)) }"), GoDeclRaw("func sky_stringFromChar(c any) any { return sky_asString(c) }"), GoDeclRaw("func sky_stringToList(s any) any { str := sky_asString(s); result := make([]any, len(str)); for i, r := range str { result[i] = string(r) }; return result }"), GoDeclRaw("var _ = exec.Command"), GoDeclRaw("var _ = bufio.NewReader"), GoDeclRaw("func sky_fst(t any) any { return sky_asTuple2(t).V0 }"), GoDeclRaw("func sky_snd(t any) any { return sky_asTuple2(t).V1 }"), GoDeclRaw("func sky_errorToString(e any) any { return sky_asString(e) }"), GoDeclRaw("func sky_identity(v any) any { return v }"), GoDeclRaw("func sky_always(a any) any { return func(b any) any { return a } }"), GoDeclRaw("func sky_js(v any) any { return v }"), GoDeclRaw("func sky_call(f any, arg any) any { return f.(func(any) any)(arg) }"), GoDeclRaw("func sky_call2(f any, a any, b any) any { return f.(func(any) any)(a).(func(any) any)(b) }"), GoDeclRaw("func sky_call3(f any, a any, b any, c any) any { return f.(func(any) any)(a).(func(any) any)(b).(func(any) any)(c) }")}
}

func Compiler_Lower_CollectLocalFunctions(decls any) any {
	return sky_call(sky_listFilterMap(func(d any) any { return func() any { return func() any { __subject := d; if sky_asMap(__subject)["SkyName"] == "FunDecl" { name := sky_asMap(__subject)["V0"]; _ = name; return SkyJust(name) };  if true { return SkyNothing() };  return nil }() }() }), decls)
}

func Compiler_Lower_CollectLocalFunctionArities(decls any) any {
	return sky_call(sky_call(sky_listFoldl(func(d any) any { return func(acc any) any { return func() any { return func() any { __subject := d; if sky_asMap(__subject)["SkyName"] == "FunDecl" { name := sky_asMap(__subject)["V0"]; _ = name; params := sky_asMap(__subject)["V1"]; _ = params; return sky_call(sky_call(sky_dictInsert(name), sky_listLength(params)), acc) };  if true { return acc };  return nil }() }() } }), sky_dictEmpty), decls)
}

func Compiler_Lower_BuildConstructorMap(registry any) any {
	return sky_call(sky_call(sky_dictFoldl(func(typeName any) any { return func(adt any) any { return func(acc any) any { return func() any { entries := sky_dictToList(sky_asMap(adt)["constructors"]); _ = entries; return Compiler_Lower_AddCtorsFromList(typeName, entries, 0, acc) }() } } }), sky_dictEmpty), registry)
}

func Compiler_Lower_AddCtorsFromList(typeName any, entries any, idx any, acc any) any {
	return func() any { return func() any { __subject := entries; if len(sky_asList(__subject)) == 0 { return acc };  if len(sky_asList(__subject)) > 0 { entry := sky_asList(__subject)[0]; _ = entry; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { name := sky_fst(entry); _ = name; scheme := sky_snd(entry); _ = scheme; ctorArity := Compiler_Lower_CountFunArgs(sky_asMap(scheme)["type_"]); _ = ctorArity; return sky_call(sky_call(sky_dictInsert(name), map[string]any{"adtName": typeName, "tagIndex": idx, "arity": ctorArity}), Compiler_Lower_AddCtorsFromList(typeName, rest, sky_asInt(idx) + sky_asInt(1), acc)) }() };  return nil }() }()
}

func Compiler_Lower_CountFunArgs(t any) any {
	return func() any { return func() any { __subject := t; if sky_asMap(__subject)["SkyName"] == "TFun" { argT := sky_asMap(__subject)["V0"]; _ = argT; toT := sky_asMap(__subject)["V1"]; _ = toT; return sky_asInt(1) + sky_asInt(Compiler_Lower_CountFunArgs(toT)) };  if true { return 0 };  return nil }() }()
}

func Compiler_Lower_SanitizeGoIdent(name any) any {
	return func() any { if sky_asBool(Compiler_Lower_IsGoKeyword(name)) { return sky_asString(name) + sky_asString("_") }; return name }()
}

func Compiler_Lower_IsGoKeyword(name any) any {
	return sky_asBool(sky_equal(name, "go")) || sky_asBool(sky_asBool(sky_equal(name, "type")) || sky_asBool(sky_asBool(sky_equal(name, "func")) || sky_asBool(sky_asBool(sky_equal(name, "var")) || sky_asBool(sky_asBool(sky_equal(name, "return")) || sky_asBool(sky_asBool(sky_equal(name, "if")) || sky_asBool(sky_asBool(sky_equal(name, "else")) || sky_asBool(sky_asBool(sky_equal(name, "for")) || sky_asBool(sky_asBool(sky_equal(name, "range")) || sky_asBool(sky_asBool(sky_equal(name, "switch")) || sky_asBool(sky_asBool(sky_equal(name, "case")) || sky_asBool(sky_asBool(sky_equal(name, "default")) || sky_asBool(sky_asBool(sky_equal(name, "break")) || sky_asBool(sky_asBool(sky_equal(name, "continue")) || sky_asBool(sky_asBool(sky_equal(name, "select")) || sky_asBool(sky_asBool(sky_equal(name, "chan")) || sky_asBool(sky_asBool(sky_equal(name, "map")) || sky_asBool(sky_asBool(sky_equal(name, "struct")) || sky_asBool(sky_asBool(sky_equal(name, "interface")) || sky_asBool(sky_asBool(sky_equal(name, "package")) || sky_asBool(sky_asBool(sky_equal(name, "import")) || sky_asBool(sky_asBool(sky_equal(name, "const")) || sky_asBool(sky_asBool(sky_equal(name, "defer")) || sky_asBool(sky_asBool(sky_equal(name, "fallthrough")) || sky_asBool(sky_equal(name, "goto")))))))))))))))))))))))))
}

func Compiler_Lower_IsStdlibCallee(expr any) any {
	return func() any { return func() any { __subject := expr; if sky_asMap(__subject)["SkyName"] == "QualifiedExpr" { parts := sky_asMap(__subject)["V0"]; _ = parts; return func() any { return func() any { __subject := sky_listHead(parts); if sky_asSkyMaybe(__subject).SkyName == "Just" { first := sky_asSkyMaybe(__subject).JustValue; _ = first; return sky_asBool(sky_equal(first, "String")) || sky_asBool(sky_asBool(sky_equal(first, "List")) || sky_asBool(sky_asBool(sky_equal(first, "Dict")) || sky_asBool(sky_asBool(sky_equal(first, "Set")) || sky_asBool(sky_asBool(sky_equal(first, "File")) || sky_asBool(sky_asBool(sky_equal(first, "Process")) || sky_asBool(sky_asBool(sky_equal(first, "Ref")) || sky_asBool(sky_asBool(sky_equal(first, "Io")) || sky_asBool(sky_asBool(sky_equal(first, "Args")) || sky_asBool(sky_equal(first, "Log")))))))))) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return false };  if sky_asMap(__subject)["SkyName"] == "IdentifierExpr" { name := sky_asMap(__subject)["V0"]; _ = name; return sky_call(sky_stringStartsWith("sky_"), name) };  if sky_asMap(__subject)["SkyName"] == "CallExpr" { innerCallee := sky_asMap(__subject)["V0"]; _ = innerCallee; return Compiler_Lower_IsStdlibCallee(innerCallee) };  if true { return false };  return nil }() }() };  return nil }() }()
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

func Compiler_Lower_ListContains(needle any, haystack any) any {
	return sky_call(sky_call(sky_listFoldl(func(item any) any { return func(acc any) any { return func() any { if sky_asBool(acc) { return true }; return sky_equal(item, needle) }() } }), false), haystack)
}

func Compiler_Lower_IsLocalFn(name any, fns any) any {
	return func() any { return func() any { __subject := sky_call(sky_dictGet(name), fns); if sky_asSkyMaybe(__subject).SkyName == "Just" { return true };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return false };  return nil }() }()
}

func Compiler_Lower_IsLocalFunction(name any, ctx any) any {
	return func() any { return func() any { __subject := sky_call(sky_dictGet(name), sky_asMap(ctx)["localFunctionArity"]); if sky_asSkyMaybe(__subject).SkyName == "Just" { return true };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return false };  return nil }() }()
}

func Compiler_Lower_GoQuote(s any) any {
	return func() any { escaped := sky_call(sky_stringReplace("\""), "\\\"").(func(any) any)(sky_call(sky_stringReplace("\\"), "\\\\").(func(any) any)(s)); _ = escaped; return sky_asString("\"") + sky_asString(sky_asString(escaped) + sky_asString("\"")) }()
}

func Compiler_Lower_CapitalizeFirst(s any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(s)) { return "" }; return sky_asString(sky_stringToUpper(sky_call(sky_call(sky_stringSlice(0), 1), s))) + sky_asString(sky_call(sky_call(sky_stringSlice(1), sky_stringLength(s)), s)) }()
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
	return func() any { return func() any { __subject := items; if len(sky_asList(__subject)) == 0 { return []any{} };  if len(sky_asList(__subject)) > 0 { x := sky_asList(__subject)[0]; _ = x; rest := sky_asList(__subject)[1:]; _ = rest; return append([]any{[]any{}}, sky_asList(Compiler_Lower_ZipIndexLoop(sky_asInt(idx) + sky_asInt(1), rest))...) };  return nil }() }()
}

func Compiler_Lower_EmitGoExprInline(expr any) any {
	return func() any { return func() any { __subject := expr; if sky_asMap(__subject)["SkyName"] == "GoIdent" { name := sky_asMap(__subject)["V0"]; _ = name; return name };  if sky_asMap(__subject)["SkyName"] == "GoBasicLit" { val := sky_asMap(__subject)["V0"]; _ = val; return val };  if sky_asMap(__subject)["SkyName"] == "GoStringLit" { s := sky_asMap(__subject)["V0"]; _ = s; return sky_asString("\"") + sky_asString(sky_asString(s) + sky_asString("\"")) };  if sky_asMap(__subject)["SkyName"] == "GoCallExpr" { fn := sky_asMap(__subject)["V0"]; _ = fn; args := sky_asMap(__subject)["V1"]; _ = args; return func() any { fnStr := Compiler_Lower_EmitGoExprInline(fn); _ = fnStr; calleeStr := func() any { if sky_asBool(sky_call(sky_stringEndsWith(")"), fnStr)) { return sky_asString(fnStr) + sky_asString(".(func(any) any)") }; return fnStr }(); _ = calleeStr; return sky_asString(calleeStr) + sky_asString(sky_asString("(") + sky_asString(sky_asString(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Lower_EmitGoExprInline), args))) + sky_asString(")"))) }() };  if sky_asMap(__subject)["SkyName"] == "GoSelectorExpr" { target := sky_asMap(__subject)["V0"]; _ = target; sel := sky_asMap(__subject)["V1"]; _ = sel; return sky_asString(Compiler_Lower_EmitGoExprInline(target)) + sky_asString(sky_asString(".") + sky_asString(sel)) };  if sky_asMap(__subject)["SkyName"] == "GoSliceLit" { items := sky_asMap(__subject)["V0"]; _ = items; return sky_asString("[]any{") + sky_asString(sky_asString(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Lower_EmitGoExprInline), items))) + sky_asString("}")) };  if sky_asMap(__subject)["SkyName"] == "GoMapLit" { entries := sky_asMap(__subject)["V0"]; _ = entries; return sky_asString("map[string]any{") + sky_asString(sky_asString(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Lower_EmitMapEntry), entries))) + sky_asString("}")) };  if sky_asMap(__subject)["SkyName"] == "GoFuncLit" { params := sky_asMap(__subject)["V0"]; _ = params; body := sky_asMap(__subject)["V1"]; _ = body; return sky_asString("func(") + sky_asString(sky_asString(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Lower_EmitInlineParam), params))) + sky_asString(sky_asString(") any { return ") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(body)) + sky_asString(" }")))) };  if sky_asMap(__subject)["SkyName"] == "GoRawExpr" { code := sky_asMap(__subject)["V0"]; _ = code; return code };  if sky_asMap(__subject)["SkyName"] == "GoCompositeLit" { typeName := sky_asMap(__subject)["V0"]; _ = typeName; fields := sky_asMap(__subject)["V1"]; _ = fields; return sky_asString(typeName) + sky_asString(sky_asString("{") + sky_asString(sky_asString(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(func(pair any) any { return sky_asString(sky_fst(pair)) + sky_asString(sky_asString(": ") + sky_asString(Compiler_Lower_EmitGoExprInline(sky_snd(pair)))) }), fields))) + sky_asString("}"))) };  if sky_asMap(__subject)["SkyName"] == "GoBinaryExpr" { op := sky_asMap(__subject)["V0"]; _ = op; left := sky_asMap(__subject)["V1"]; _ = left; right := sky_asMap(__subject)["V2"]; _ = right; return sky_asString(Compiler_Lower_EmitGoExprInline(left)) + sky_asString(sky_asString(" ") + sky_asString(sky_asString(op) + sky_asString(sky_asString(" ") + sky_asString(Compiler_Lower_EmitGoExprInline(right))))) };  if sky_asMap(__subject)["SkyName"] == "GoUnaryExpr" { op := sky_asMap(__subject)["V0"]; _ = op; operand := sky_asMap(__subject)["V1"]; _ = operand; return sky_asString(op) + sky_asString(Compiler_Lower_EmitGoExprInline(operand)) };  if sky_asMap(__subject)["SkyName"] == "GoIndexExpr" { target := sky_asMap(__subject)["V0"]; _ = target; index := sky_asMap(__subject)["V1"]; _ = index; return sky_asString(Compiler_Lower_EmitGoExprInline(target)) + sky_asString(sky_asString("[") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(index)) + sky_asString("]"))) };  if sky_asMap(__subject)["SkyName"] == "GoNilExpr" { return "nil" };  return nil }() }()
}

func Compiler_Lower_EmitMapEntry(pair any) any {
	return func() any { key := sky_fst(pair); _ = key; val := sky_snd(pair); _ = val; return sky_asString(Compiler_Lower_EmitGoExprInline(key)) + sky_asString(sky_asString(": ") + sky_asString(Compiler_Lower_EmitGoExprInline(val))) }()
}

func Compiler_Lower_EmitInlineParam(p any) any {
	return sky_asString(sky_asMap(p)["name"]) + sky_asString(sky_asString(" ") + sky_asString(sky_asMap(p)["type_"]))
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
	return func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_refSet", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_processRun", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_fileWrite", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_stringAppend", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_stringJoin", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_stringSlice", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_stringReplace", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_stringSplit", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_stringContains", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_stringEndsWith", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_stringStartsWith", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_setRemove", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_setDiff", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_setUnion", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_setMember", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_setInsert", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_dictMember", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_dictRemove", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_dictUnion", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_dictFoldl", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_dictMap", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_dictGet", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_dictInsert", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_listAppend", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_listMember", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_listDrop", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_listIndexedMap", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_listMap", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_listFilter", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_listFoldr", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_listFoldl", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_listConcatMap", __pa0) }(func(__pa0 any) any { return Compiler_Lower_AddFuncAssertion("sky_listFilterMap", __pa0) }(sky_call(sky_stringReplace("sky_listMap("), "sky_listMap(")(code))))))))))))))))))))))))))))))))))
}

func Compiler_Lower_AddFuncAssertion(funcName any, code any) any {
	return code
}

func Compiler_Lower_StmtToGoString(stmt any) any {
	return func() any { return func() any { __subject := stmt; if sky_asMap(__subject)["SkyName"] == "GoExprStmt" { expr := sky_asMap(__subject)["V0"]; _ = expr; return Compiler_Lower_EmitGoExprInline(expr) };  if sky_asMap(__subject)["SkyName"] == "GoAssign" { name := sky_asMap(__subject)["V0"]; _ = name; expr := sky_asMap(__subject)["V1"]; _ = expr; return sky_asString(name) + sky_asString(sky_asString(" = ") + sky_asString(Compiler_Lower_EmitGoExprInline(expr))) };  if sky_asMap(__subject)["SkyName"] == "GoShortDecl" { name := sky_asMap(__subject)["V0"]; _ = name; expr := sky_asMap(__subject)["V1"]; _ = expr; return sky_asString(name) + sky_asString(sky_asString(" := ") + sky_asString(Compiler_Lower_EmitGoExprInline(expr))) };  if sky_asMap(__subject)["SkyName"] == "GoReturn" { expr := sky_asMap(__subject)["V0"]; _ = expr; return sky_asString("return ") + sky_asString(Compiler_Lower_EmitGoExprInline(expr)) };  if sky_asMap(__subject)["SkyName"] == "GoReturnVoid" { return "return" };  if sky_asMap(__subject)["SkyName"] == "GoIf" { cond := sky_asMap(__subject)["V0"]; _ = cond; thenBody := sky_asMap(__subject)["V1"]; _ = thenBody; elseBody := sky_asMap(__subject)["V2"]; _ = elseBody; return sky_asString("if ") + sky_asString(sky_asString(Compiler_Lower_EmitGoExprInline(cond)) + sky_asString(sky_asString(" { ") + sky_asString(sky_asString(Compiler_Lower_StmtsToGoString(thenBody)) + sky_asString(sky_asString(" } else { ") + sky_asString(sky_asString(Compiler_Lower_StmtsToGoString(elseBody)) + sky_asString(" }")))))) };  if sky_asMap(__subject)["SkyName"] == "GoBlock" { body := sky_asMap(__subject)["V0"]; _ = body; return Compiler_Lower_StmtsToGoString(body) };  return nil }() }()
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
	return func() any { covered := Compiler_Exhaustive_CollectBoolPatterns(patterns, sky_setEmpty); _ = covered; hasTrue := sky_call(sky_setMember("True"), covered); _ = hasTrue; hasFalse := sky_call(sky_setMember("False"), covered); _ = hasFalse; return func() any { if sky_asBool(sky_asBool(hasTrue) && sky_asBool(hasFalse)) { return SkyNothing() }; return func() any { if sky_asBool(hasTrue) { return SkyJust("Missing pattern: False") }; return func() any { if sky_asBool(hasFalse) { return SkyJust("Missing pattern: True") }; return SkyJust("Missing patterns: True, False") }() }() }() }()
}

func Compiler_Exhaustive_CollectBoolPatterns(patterns any, acc any) any {
	return func() any { return func() any { __subject := patterns; if len(sky_asList(__subject)) == 0 { return acc };  if len(sky_asList(__subject)) > 0 { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PConstructor" { parts := sky_asMap(__subject)["V0"]; _ = parts; return func() any { name := Compiler_Exhaustive_LastPart(parts); _ = name; return Compiler_Exhaustive_CollectBoolPatterns(rest, sky_call(sky_setInsert(name), acc)) }() };  if sky_asMap(__subject)["SkyName"] == "PLiteral" { b := sky_asMap(sky_asMap(__subject)["V0"])["V0"]; _ = b; return func() any { if sky_asBool(b) { return Compiler_Exhaustive_CollectBoolPatterns(rest, sky_call(sky_setInsert("True"), acc)) }; return Compiler_Exhaustive_CollectBoolPatterns(rest, sky_call(sky_setInsert("False"), acc)) }() };  if true { return Compiler_Exhaustive_CollectBoolPatterns(rest, acc) };  return nil }() }() };  return nil }() }()
}

func Compiler_Exhaustive_CheckAdtExhaustiveness(adt any, patterns any) any {
	return func() any { allCtors := sky_setFromList(sky_dictKeys(sky_asMap(adt)["constructors"])); _ = allCtors; coveredCtors := Compiler_Exhaustive_CollectConstructorPatterns(patterns, sky_setEmpty); _ = coveredCtors; missing := sky_call(sky_setDiff(allCtors), coveredCtors); _ = missing; return func() any { if sky_asBool(sky_setIsEmpty(missing)) { return SkyNothing() }; return func() any { missingList := sky_setToList(missing); _ = missingList; return SkyJust(sky_asString("Missing patterns: ") + sky_asString(sky_call(sky_stringJoin(", "), missingList))) }() }() }()
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

func Compiler_PatternCheck_EmptyResult() any {
	return map[string]any{"substitution": emptySub, "bindings": []any{}}
}

func Compiler_PatternCheck_CheckPattern(counter any, registry any, env any, pat any, expectedType any) any {
	return func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PWildcard" { return SkyOk(Compiler_PatternCheck_EmptyResult) };  if sky_asMap(__subject)["SkyName"] == "PVariable" { name := sky_asMap(__subject)["V0"]; _ = name; return SkyOk(map[string]any{"substitution": emptySub, "bindings": []any{[]any{}}}) };  if sky_asMap(__subject)["SkyName"] == "PLiteral" { lit := sky_asMap(__subject)["V0"]; _ = lit; return func() any { litType := Compiler_PatternCheck_LiteralType(lit); _ = litType; return func() any { return func() any { __subject := unify(expectedType, litType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Pattern literal type mismatch: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { sub := sky_asSkyResult(__subject).OkValue; _ = sub; return SkyOk(map[string]any{"substitution": sub, "bindings": []any{}}) };  if sky_asMap(__subject)["SkyName"] == "PTuple" { items := sky_asMap(__subject)["V0"]; _ = items; return func() any { freshVars := sky_call(sky_listMap(func(item any) any { return freshVar(counter, SkyNothing()) }), items); _ = freshVars; tupleType := TTuple(freshVars); _ = tupleType; return func() any { return func() any { __subject := unify(expectedType, tupleType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Tuple pattern mismatch: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { sub := sky_asSkyResult(__subject).OkValue; _ = sub; return Compiler_PatternCheck_CheckPatternList(counter, registry, env, items, sky_call(sky_listMap(func(x any) any { return applySub(sub, x) }), freshVars), sub, []any{}) };  if sky_asMap(__subject)["SkyName"] == "PList" { items := sky_asMap(__subject)["V0"]; _ = items; return func() any { elemVar := freshVar(counter, SkyJust("elem")); _ = elemVar; listType := TApp(TConst("List"), []any{elemVar}); _ = listType; return func() any { return func() any { __subject := unify(expectedType, listType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("List pattern mismatch: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { sub := sky_asSkyResult(__subject).OkValue; _ = sub; return func() any { elemType := applySub(sub, elemVar); _ = elemType; return Compiler_PatternCheck_CheckPatternListSame(counter, registry, env, items, elemType, sub, []any{}) }() };  if sky_asMap(__subject)["SkyName"] == "PCons" { headPat := sky_asMap(__subject)["V0"]; _ = headPat; tailPat := sky_asMap(__subject)["V1"]; _ = tailPat; return func() any { elemVar := freshVar(counter, SkyJust("elem")); _ = elemVar; listType := TApp(TConst("List"), []any{elemVar}); _ = listType; return func() any { return func() any { __subject := unify(expectedType, listType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Cons pattern mismatch: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { sub1 := sky_asSkyResult(__subject).OkValue; _ = sub1; return func() any { elemType := applySub(sub1, elemVar); _ = elemType; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, headPat, elemType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { headResult := sky_asSkyResult(__subject).OkValue; _ = headResult; return func() any { sub2 := composeSubs(sky_asMap(headResult)["substitution"], sub1); _ = sub2; tailExpected := applySub(sub2, listType); _ = tailExpected; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, tailPat, tailExpected); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { tailResult := sky_asSkyResult(__subject).OkValue; _ = tailResult; return func() any { finalSub := composeSubs(sky_asMap(tailResult)["substitution"], sub2); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "bindings": sky_call(sky_listAppend(sky_asMap(headResult)["bindings"]), sky_asMap(tailResult)["bindings"])}) }() };  if sky_asMap(__subject)["SkyName"] == "PAs" { innerPat := sky_asMap(__subject)["V0"]; _ = innerPat; name := sky_asMap(__subject)["V1"]; _ = name; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, innerPat, expectedType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return SkyOk(sky_recordUpdate(result, map[string]any{"bindings": append([]any{[]any{}}, sky_asList(sky_asMap(result)["bindings"])...)})) };  if sky_asMap(__subject)["SkyName"] == "PConstructor" { parts := sky_asMap(__subject)["V0"]; _ = parts; argPats := sky_asMap(__subject)["V1"]; _ = argPats; return func() any { ctorName := func() any { return func() any { __subject := sky_listReverse(parts); if len(sky_asList(__subject)) > 0 { last := sky_asList(__subject)[0]; _ = last; return last };  if len(sky_asList(__subject)) == 0 { return "" };  return nil }() }(); _ = ctorName; return func() any { return func() any { __subject := Compiler_Adt_LookupConstructor(ctorName, registry); if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return func() any { return func() any { __subject := Compiler_Env_Lookup(ctorName, env); if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyErr(sky_asString("Unknown constructor in pattern: ") + sky_asString(ctorName)) };  if sky_asSkyMaybe(__subject).SkyName == "Just" { scheme := sky_asSkyMaybe(__subject).JustValue; _ = scheme; return Compiler_PatternCheck_CheckConstructorPattern(counter, registry, env, ctorName, scheme, argPats, expectedType) };  if sky_asSkyMaybe(__subject).SkyName == "Just" { scheme := sky_asSkyMaybe(__subject).JustValue; _ = scheme; return Compiler_PatternCheck_CheckConstructorPattern(counter, registry, env, ctorName, scheme, argPats, expectedType) };  if sky_asMap(__subject)["SkyName"] == "PRecord" { fields := sky_asMap(__subject)["V0"]; _ = fields; return func() any { fieldBindings := sky_call(sky_listMap(func(f any) any { return []any{} }), fields); _ = fieldBindings; return SkyOk(map[string]any{"substitution": emptySub, "bindings": fieldBindings}) }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }()
}

func Compiler_PatternCheck_CheckConstructorPattern(counter any, registry any, env any, ctorName any, scheme any, argPats any, expectedType any) any {
	return func() any { instType := instantiate(counter, scheme); _ = instType; splitResult := Compiler_PatternCheck_SplitFunType(instType); _ = splitResult; argTypes := sky_fst(splitResult); _ = argTypes; resultType := sky_snd(splitResult); _ = resultType; return func() any { if sky_asBool(!sky_equal(sky_listLength(argPats), sky_listLength(argTypes))) { return SkyErr(sky_asString("Constructor ") + sky_asString(sky_asString(ctorName) + sky_asString(sky_asString(" expects ") + sky_asString(sky_asString(sky_stringFromInt(sky_listLength(argTypes))) + sky_asString(sky_asString(" arguments, got ") + sky_asString(sky_stringFromInt(sky_listLength(argPats)))))))) }; return func() any { return func() any { __subject := unify(expectedType, resultType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Constructor pattern type mismatch: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { sub := sky_asSkyResult(__subject).OkValue; _ = sub; return Compiler_PatternCheck_CheckPatternList(counter, registry, env, argPats, sky_call(sky_listMap(func(x any) any { return applySub(sub, x) }), argTypes), sub, []any{}) };  return nil }() }() }() }()
}

func Compiler_PatternCheck_SplitFunType(t any) any {
	return func() any { return func() any { __subject := t; if sky_asMap(__subject)["SkyName"] == "TFun" { fromT := sky_asMap(__subject)["V0"]; _ = fromT; toT := sky_asMap(__subject)["V1"]; _ = toT; return func() any { inner := Compiler_PatternCheck_SplitFunType(toT); _ = inner; rest := sky_fst(inner); _ = rest; result := sky_snd(inner); _ = result; return []any{} }() };  if true { return []any{} };  return nil }() }()
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

var emptyRegistry = Compiler_Adt_EmptyRegistry

var LookupConstructor = Compiler_Adt_LookupConstructor

var LookupCtorInEntries = Compiler_Adt_LookupCtorInEntries

var LookupConstructorAdt = Compiler_Adt_LookupConstructorAdt

var LookupCtorAdtInEntries = Compiler_Adt_LookupCtorAdtInEntries

var RegisterAdts = Compiler_Adt_RegisterAdts

var RegisterAdtsLoop = Compiler_Adt_RegisterAdtsLoop

var RegisterOneAdt = Compiler_Adt_RegisterOneAdt

var BuildConstructorScheme = Compiler_Adt_BuildConstructorScheme

var BuildParamMap = Compiler_Adt_BuildParamMap

var BuildFunType = Compiler_Adt_BuildFunType

var GetVarId = Compiler_Adt_GetVarId

var ResolveTypeExpr = Compiler_Adt_ResolveTypeExpr

func Compiler_Adt_EmptyRegistry() any {
	return sky_dictEmpty
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
	return Compiler_Adt_RegisterAdtsLoop(counter, decls, Compiler_Adt_EmptyRegistry, Compiler_Env_Empty(), []any{})
}

func Compiler_Adt_RegisterAdtsLoop(counter any, decls any, registry any, env any, diagnostics any) any {
	return func() any { return func() any { __subject := decls; if len(sky_asList(__subject)) == 0 { return []any{} };  if len(sky_asList(__subject)) > 0 { decl := sky_asList(__subject)[0]; _ = decl; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "TypeDecl" { name := sky_asMap(__subject)["V0"]; _ = name; params := sky_asMap(__subject)["V1"]; _ = params; variants := sky_asMap(__subject)["V2"]; _ = variants; return func() any { __tup_newRegistry_newEnv_newDiags := Compiler_Adt_RegisterOneAdt(counter, name, params, variants, registry, env); newRegistry := sky_asTuple3(__tup_newRegistry_newEnv_newDiags).V0; _ = newRegistry; newEnv := sky_asTuple3(__tup_newRegistry_newEnv_newDiags).V1; _ = newEnv; newDiags := sky_asTuple3(__tup_newRegistry_newEnv_newDiags).V2; _ = newDiags; return Compiler_Adt_RegisterAdtsLoop(counter, rest, newRegistry, newEnv, sky_call(sky_listAppend(diagnostics), newDiags)) }() };  if true { return Compiler_Adt_RegisterAdtsLoop(counter, rest, registry, env, diagnostics) };  return nil }() }() };  return nil }() }()
}

func Compiler_Adt_RegisterOneAdt(counter any, typeName any, typeParams any, variants any, registry any, env any) any {
	return func() any { arity := sky_listLength(typeParams); _ = arity; ctorSchemes := sky_call(sky_call(sky_listFoldl(func(variant any) any { return func(acc any) any { return func() any { scheme := Compiler_Adt_BuildConstructorScheme(counter, typeName, typeParams, variant); _ = scheme; return sky_call(sky_call(sky_dictInsert(sky_asMap(variant)["name"]), scheme), acc) }() } }), sky_dictEmpty), variants); _ = ctorSchemes; adt := map[string]any{"name": typeName, "arity": arity, "constructors": ctorSchemes}; _ = adt; newRegistry := sky_call(sky_call(sky_dictInsert(typeName), adt), registry); _ = newRegistry; newEnv := sky_call(sky_call(sky_dictFoldl(func(ctorName any) any { return func(scheme any) any { return func(acc any) any { return Compiler_Env_Extend(ctorName, scheme, acc) } } }), env), ctorSchemes); _ = newEnv; return []any{} }()
}

func Compiler_Adt_BuildConstructorScheme(counter any, typeName any, typeParams any, variant any) any {
	return func() any { paramVars := sky_call(sky_listMap(func(p any) any { return freshVar(counter, SkyJust(p)) }), typeParams); _ = paramVars; paramMap := Compiler_Adt_BuildParamMap(typeParams, paramVars, sky_dictEmpty); _ = paramMap; resultType := func() any { if sky_asBool(sky_listIsEmpty(paramVars)) { return TConst(typeName) }; return TApp(TConst(typeName), paramVars) }(); _ = resultType; fieldTypes := sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Adt_ResolveTypeExpr(paramMap, __pa0) }), sky_asMap(variant)["fields"]); _ = fieldTypes; ctorType := Compiler_Adt_BuildFunType(fieldTypes, resultType); _ = ctorType; quantified := sky_call(sky_listFilterMap(Compiler_Adt_GetVarId), paramVars); _ = quantified; return map[string]any{"quantified": quantified, "type_": ctorType} }()
}

func Compiler_Adt_BuildParamMap(names any, vars any, acc any) any {
	return func() any { return func() any { __subject := names; if len(sky_asList(__subject)) == 0 { return acc };  if len(sky_asList(__subject)) > 0 { nameH := sky_asList(__subject)[0]; _ = nameH; nameRest := sky_asList(__subject)[1:]; _ = nameRest; return func() any { return func() any { __subject := vars; if len(sky_asList(__subject)) == 0 { return acc };  if len(sky_asList(__subject)) > 0 { varH := sky_asList(__subject)[0]; _ = varH; varRest := sky_asList(__subject)[1:]; _ = varRest; return Compiler_Adt_BuildParamMap(nameRest, varRest, sky_call(sky_call(sky_dictInsert(nameH), varH), acc)) };  return nil }() }() };  return nil }() }()
}

func Compiler_Adt_BuildFunType(args any, result any) any {
	return func() any { return func() any { __subject := args; if len(sky_asList(__subject)) == 0 { return result };  if len(sky_asList(__subject)) > 0 { arg := sky_asList(__subject)[0]; _ = arg; rest := sky_asList(__subject)[1:]; _ = rest; return TFun(arg, Compiler_Adt_BuildFunType(rest, result)) };  return nil }() }()
}

func Compiler_Adt_GetVarId(t any) any {
	return func() any { return func() any { __subject := t; if sky_asMap(__subject)["SkyName"] == "TVar" { id := sky_asMap(__subject)["V0"]; _ = id; return SkyJust(id) };  if true { return SkyNothing() };  return nil }() }()
}

func Compiler_Adt_ResolveTypeExpr(paramMap any, texpr any) any {
	return func() any { return func() any { __subject := texpr; if sky_asMap(__subject)["SkyName"] == "TypeRef" { parts := sky_asMap(__subject)["V0"]; _ = parts; args := sky_asMap(__subject)["V1"]; _ = args; return func() any { name := sky_call(sky_stringJoin("."), parts); _ = name; resolvedArgs := sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Adt_ResolveTypeExpr(paramMap, __pa0) }), args); _ = resolvedArgs; return func() any { return func() any { __subject := sky_call(sky_dictGet(name), paramMap); if sky_asSkyMaybe(__subject).SkyName == "Just" { tv := sky_asSkyMaybe(__subject).JustValue; _ = tv; return tv };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return func() any { if sky_asBool(sky_listIsEmpty(resolvedArgs)) { return TConst(name) }; return TApp(TConst(name), resolvedArgs) }() };  if sky_asMap(__subject)["SkyName"] == "TypeVar" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { return func() any { __subject := sky_call(sky_dictGet(name), paramMap); if sky_asSkyMaybe(__subject).SkyName == "Just" { tv := sky_asSkyMaybe(__subject).JustValue; _ = tv; return tv };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return TVar(0, SkyJust(name)) };  if sky_asMap(__subject)["SkyName"] == "FunType" { fromT := sky_asMap(__subject)["V0"]; _ = fromT; toT := sky_asMap(__subject)["V1"]; _ = toT; return TFun(Compiler_Adt_ResolveTypeExpr(paramMap, fromT), Compiler_Adt_ResolveTypeExpr(paramMap, toT)) };  if sky_asMap(__subject)["SkyName"] == "RecordTypeExpr" { fields := sky_asMap(__subject)["V0"]; _ = fields; return func() any { fieldDict := sky_call(sky_call(sky_listFoldl(func(f any) any { return func(acc any) any { return sky_call(sky_call(sky_dictInsert(sky_asMap(f)["name"]), Compiler_Adt_ResolveTypeExpr(paramMap, sky_asMap(f)["type_"])), acc) } }), sky_dictEmpty), fields); _ = fieldDict; return TRecord(fieldDict) }() };  if sky_asMap(__subject)["SkyName"] == "TupleTypeExpr" { items := sky_asMap(__subject)["V0"]; _ = items; return TTuple(sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Adt_ResolveTypeExpr(paramMap, __pa0) }), items)) };  if sky_asMap(__subject)["SkyName"] == "UnitTypeExpr" { return TConst("Unit") };  return nil }() }() };  return nil }() }() }() };  return nil }() }()
}

var TVar = Compiler_Types_TVar

var TConst = Compiler_Types_TConst

var TFun = Compiler_Types_TFun

var TApp = Compiler_Types_TApp

var TTuple = Compiler_Types_TTuple

var TRecord = Compiler_Types_TRecord

var freshVar = Compiler_Types_FreshVar

var emptySub = Compiler_Types_EmptySub

var applySub = Compiler_Types_ApplySub

var applySubToScheme = Compiler_Types_ApplySubToScheme

var composeSubs = Compiler_Types_ComposeSubs

var freeVars = Compiler_Types_FreeVars

var freeVarsInScheme = Compiler_Types_FreeVarsInScheme

var instantiate = Compiler_Types_Instantiate

var generalize = Compiler_Types_Generalize

var mono = Compiler_Types_Mono

var formatType = Compiler_Types_FormatType

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
	return sky_dictEmpty
}

func Compiler_Types_ApplySub(sub any, t any) any {
	return func() any { return func() any { __subject := t; if sky_asMap(__subject)["SkyName"] == "TVar" { id := sky_asMap(__subject)["V0"]; _ = id; return func() any { return func() any { __subject := sky_call(sky_dictGet(id), sub); if sky_asSkyMaybe(__subject).SkyName == "Just" { replacement := sky_asSkyMaybe(__subject).JustValue; _ = replacement; return replacement };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return t };  if sky_asMap(__subject)["SkyName"] == "TConst" { return t };  if sky_asMap(__subject)["SkyName"] == "TFun" { fromT := sky_asMap(__subject)["V0"]; _ = fromT; toT := sky_asMap(__subject)["V1"]; _ = toT; return TFun(Compiler_Types_ApplySub(sub, fromT), Compiler_Types_ApplySub(sub, toT)) };  if sky_asMap(__subject)["SkyName"] == "TApp" { ctor := sky_asMap(__subject)["V0"]; _ = ctor; args := sky_asMap(__subject)["V1"]; _ = args; return TApp(Compiler_Types_ApplySub(sub, ctor), sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Types_ApplySub(sub, __pa0) }), args)) };  if sky_asMap(__subject)["SkyName"] == "TTuple" { items := sky_asMap(__subject)["V0"]; _ = items; return TTuple(sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Types_ApplySub(sub, __pa0) }), items)) };  if sky_asMap(__subject)["SkyName"] == "TRecord" { fields := sky_asMap(__subject)["V0"]; _ = fields; return TRecord(sky_call(sky_dictMap(func(kk any) any { return func(v any) any { return Compiler_Types_ApplySub(sub, v) } }), fields)) };  return nil }() }() };  return nil }() }()
}

func Compiler_Types_ApplySubToScheme(sub any, scheme any) any {
	return func() any { filtered := sky_call(sky_call(sky_listFoldl(func(q any) any { return func(s any) any { return sky_call(sky_dictRemove(q), s) } }), sub), sky_asMap(scheme)["quantified"]); _ = filtered; return sky_recordUpdate(scheme, map[string]any{"type_": Compiler_Types_ApplySub(filtered, sky_asMap(scheme)["type_"])}) }()
}

func Compiler_Types_ComposeSubs(s1 any, s2 any) any {
	return func() any { applied := sky_call(sky_dictMap(func(kk any) any { return func(t any) any { return Compiler_Types_ApplySub(s1, t) } }), s2); _ = applied; return sky_call(sky_dictUnion(applied), s1) }()
}

func Compiler_Types_FreeVars(t any) any {
	return func() any { return func() any { __subject := t; if sky_asMap(__subject)["SkyName"] == "TVar" { id := sky_asMap(__subject)["V0"]; _ = id; return sky_setSingleton(id) };  if sky_asMap(__subject)["SkyName"] == "TConst" { return sky_setEmpty };  if sky_asMap(__subject)["SkyName"] == "TFun" { fromT := sky_asMap(__subject)["V0"]; _ = fromT; toT := sky_asMap(__subject)["V1"]; _ = toT; return sky_call(sky_setUnion(Compiler_Types_FreeVars(fromT)), Compiler_Types_FreeVars(toT)) };  if sky_asMap(__subject)["SkyName"] == "TApp" { ctor := sky_asMap(__subject)["V0"]; _ = ctor; args := sky_asMap(__subject)["V1"]; _ = args; return sky_call(sky_call(sky_listFoldl(func(arg any) any { return func(acc any) any { return sky_call(sky_setUnion(Compiler_Types_FreeVars(arg)), acc) } }), Compiler_Types_FreeVars(ctor)), args) };  if sky_asMap(__subject)["SkyName"] == "TTuple" { items := sky_asMap(__subject)["V0"]; _ = items; return sky_call(sky_call(sky_listFoldl(func(item any) any { return func(acc any) any { return sky_call(sky_setUnion(Compiler_Types_FreeVars(item)), acc) } }), sky_setEmpty), items) };  if sky_asMap(__subject)["SkyName"] == "TRecord" { fields := sky_asMap(__subject)["V0"]; _ = fields; return sky_call(sky_call(sky_dictFoldl(func(kk any) any { return func(v any) any { return func(acc any) any { return sky_call(sky_setUnion(Compiler_Types_FreeVars(v)), acc) } } }), sky_setEmpty), fields) };  return nil }() }()
}

func Compiler_Types_FreeVarsInScheme(scheme any) any {
	return func() any { typeVars := Compiler_Types_FreeVars(sky_asMap(scheme)["type_"]); _ = typeVars; quantifiedSet := sky_setFromList(sky_asMap(scheme)["quantified"]); _ = quantifiedSet; return sky_call(sky_setDiff(typeVars), quantifiedSet) }()
}

func Compiler_Types_Instantiate(counter any, scheme any) any {
	return func() any { sub := sky_call(sky_call(sky_listFoldl(func(qv any) any { return func(s any) any { return func() any { fresh := Compiler_Types_FreshVar(counter, SkyNothing()); _ = fresh; return sky_call(sky_call(sky_dictInsert(qv), fresh), s) }() } }), Compiler_Types_EmptySub), sky_asMap(scheme)["quantified"]); _ = sub; return Compiler_Types_ApplySub(sub, sky_asMap(scheme)["type_"]) }()
}

func Compiler_Types_Generalize(env any, t any) any {
	return func() any { typeVars := Compiler_Types_FreeVars(t); _ = typeVars; envVars := sky_call(sky_call(sky_dictFoldl(func(kk any) any { return func(scheme any) any { return func(acc any) any { return sky_call(sky_setUnion(Compiler_Types_FreeVarsInScheme(scheme)), acc) } } }), sky_setEmpty), env); _ = envVars; quantified := sky_setToList(sky_call(sky_setDiff(typeVars), envVars)); _ = quantified; return map[string]any{"quantified": quantified, "type_": t} }()
}

func Compiler_Types_Mono(t any) any {
	return map[string]any{"quantified": []any{}, "type_": t}
}

func Compiler_Types_FormatType(t any) any {
	return func() any { return func() any { __subject := t; if sky_asMap(__subject)["SkyName"] == "TVar" { id := sky_asMap(__subject)["V0"]; _ = id; name := sky_asMap(__subject)["V1"]; _ = name; return func() any { return func() any { __subject := name; if sky_asSkyMaybe(__subject).SkyName == "Just" { n := sky_asSkyMaybe(__subject).JustValue; _ = n; return n };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return sky_asString("t") + sky_asString(sky_stringFromInt(id)) };  if sky_asMap(__subject)["SkyName"] == "TConst" { name := sky_asMap(__subject)["V0"]; _ = name; return name };  if sky_asMap(__subject)["SkyName"] == "TFun" { fromT := sky_asMap(__subject)["V0"]; _ = fromT; toT := sky_asMap(__subject)["V1"]; _ = toT; return func() any { fromStr := func() any { return func() any { __subject := fromT; if sky_asMap(__subject)["SkyName"] == "TFun" { return sky_asString("(") + sky_asString(sky_asString(Compiler_Types_FormatType(fromT)) + sky_asString(")")) };  if true { return Compiler_Types_FormatType(fromT) };  return nil }() }(); _ = fromStr; return sky_asString(fromStr) + sky_asString(sky_asString(" -> ") + sky_asString(Compiler_Types_FormatType(toT))) }() };  if sky_asMap(__subject)["SkyName"] == "TApp" { ctor := sky_asMap(__subject)["V0"]; _ = ctor; args := sky_asMap(__subject)["V1"]; _ = args; return sky_asString(Compiler_Types_FormatType(ctor)) + sky_asString(sky_asString(" ") + sky_asString(sky_call(sky_stringJoin(" "), sky_call(sky_listMap(Compiler_Types_FormatType), args)))) };  if sky_asMap(__subject)["SkyName"] == "TTuple" { items := sky_asMap(__subject)["V0"]; _ = items; return sky_asString("( ") + sky_asString(sky_asString(sky_call(sky_stringJoin(" , "), sky_call(sky_listMap(Compiler_Types_FormatType), items))) + sky_asString(" )")) };  if sky_asMap(__subject)["SkyName"] == "TRecord" { fields := sky_asMap(__subject)["V0"]; _ = fields; return func() any { fieldStrs := sky_listMap(func(pair any) any { return sky_asString(sky_fst(pair)) + sky_asString(sky_asString(" : ") + sky_asString(Compiler_Types_FormatType(sky_snd(pair)))) }).(func(any) any)(sky_dictToList(fields)); _ = fieldStrs; return sky_asString("{ ") + sky_asString(sky_asString(sky_call(sky_stringJoin(" , "), fieldStrs)) + sky_asString(" }")) }() };  return nil }() }() };  return nil }() }()
}

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

var initState = Compiler_ParserCore_InitState

var peek = Compiler_ParserCore_Peek

var peekAt = Compiler_ParserCore_PeekAt

var previous = Compiler_ParserCore_Previous

var advance = Compiler_ParserCore_Advance

var matchKind = Compiler_ParserCore_MatchKind

var matchLexeme = Compiler_ParserCore_MatchLexeme

var matchKindLex = Compiler_ParserCore_MatchKindLex

var consume = Compiler_ParserCore_Consume

var consumeLex = Compiler_ParserCore_ConsumeLex

var tokenKindEq = Compiler_ParserCore_TokenKindEq

var tokenKindStr = Compiler_ParserCore_TokenKindStr

var parseQualifiedParts = Compiler_ParserCore_ParseQualifiedParts

var peekLexeme = Compiler_ParserCore_PeekLexeme

var peekColumn = Compiler_ParserCore_PeekColumn

var peekKind = Compiler_ParserCore_PeekKind

var peekAt1Kind = Compiler_ParserCore_PeekAt1Kind

var filterLayout = Compiler_ParserCore_FilterLayout

func Compiler_ParserCore_InitState(tokens any) any {
	return map[string]any{"tokens": tokens, "pos": 0, "errors": []any{}}
}

func Compiler_ParserCore_Peek(state any) any {
	return func() any { return func() any { __subject := sky_listHead(sky_call(sky_listDrop(sky_asMap(state)["pos"]), sky_asMap(state)["tokens"])); if sky_asSkyMaybe(__subject).SkyName == "Just" { t := sky_asSkyMaybe(__subject).JustValue; _ = t; return t };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return map[string]any{"kind": TkEOF, "lexeme": "", "span": emptySpan} };  return nil }() }()
}

func Compiler_ParserCore_PeekAt(offset any, state any) any {
	return func() any { return func() any { __subject := sky_listHead(sky_call(sky_listDrop(sky_asInt(sky_asMap(state)["pos"]) + sky_asInt(offset)), sky_asMap(state)["tokens"])); if sky_asSkyMaybe(__subject).SkyName == "Just" { t := sky_asSkyMaybe(__subject).JustValue; _ = t; return t };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return map[string]any{"kind": TkEOF, "lexeme": "", "span": emptySpan} };  return nil }() }()
}

func Compiler_ParserCore_Previous(state any) any {
	return func() any { if sky_asBool(sky_asInt(sky_asMap(state)["pos"]) > sky_asInt(0)) { return func() any { return func() any { __subject := sky_listHead(sky_call(sky_listDrop(sky_asInt(sky_asMap(state)["pos"]) - sky_asInt(1)), sky_asMap(state)["tokens"])); if sky_asSkyMaybe(__subject).SkyName == "Just" { t := sky_asSkyMaybe(__subject).JustValue; _ = t; return t };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return map[string]any{"kind": TkEOF, "lexeme": "", "span": emptySpan} };  return nil }() }() }; return map[string]any{"kind": TkEOF, "lexeme": "", "span": emptySpan} }()
}

func Compiler_ParserCore_Advance(state any) any {
	return func() any { token := Compiler_ParserCore_Peek(state); _ = token; return func() any { if sky_asBool(Compiler_ParserCore_TokenKindEq(sky_asMap(token)["kind"], TkEOF)) { return []any{} }; return []any{} }() }()
}

func Compiler_ParserCore_MatchKind(kind any, state any) any {
	return func(__pa0 any) any { return Compiler_ParserCore_TokenKindEq(Compiler_ParserCore_Peek(state), __pa0) }
}

func Compiler_ParserCore_MatchLexeme(lex any, state any) any {
	return Compiler_ParserCore_Peek(state)
}

func Compiler_ParserCore_MatchKindLex(kind any, lex any, state any) any {
	return func() any { t := Compiler_ParserCore_Peek(state); _ = t; return sky_asBool(Compiler_ParserCore_TokenKindEq(sky_asMap(t)["kind"], kind)) && sky_asBool(sky_equal(sky_asMap(t)["lexeme"], lex)) }()
}

func Compiler_ParserCore_Consume(kind any, state any) any {
	return func() any { t := Compiler_ParserCore_Peek(state); _ = t; return func() any { if sky_asBool(Compiler_ParserCore_TokenKindEq(sky_asMap(t)["kind"], kind)) { return SkyOk([]any{}) }; return SkyErr }() }()
}

func Compiler_ParserCore_ConsumeLex(kind any, lex any, state any) any {
	return func() any { t := Compiler_ParserCore_Peek(state); _ = t; return func() any { if sky_asBool(sky_asBool(Compiler_ParserCore_TokenKindEq(sky_asMap(t)["kind"], kind)) && sky_asBool(sky_equal(sky_asMap(t)["lexeme"], lex))) { return SkyOk([]any{}) }; return SkyErr }() }()
}

func Compiler_ParserCore_TokenKindEq(a any, b any) any {
	return sky_equal(Compiler_ParserCore_TokenKindStr(a), Compiler_ParserCore_TokenKindStr(b))
}

func Compiler_ParserCore_TokenKindStr(k any) any {
	return func() any { return func() any { __subject := k; if sky_asMap(__subject)["SkyName"] == "TkIdentifier" { return "Identifier" };  if sky_asMap(__subject)["SkyName"] == "TkUpperIdentifier" { return "UpperIdentifier" };  if sky_asMap(__subject)["SkyName"] == "TkInteger" { return "Integer" };  if sky_asMap(__subject)["SkyName"] == "TkFloat" { return "Float" };  if sky_asMap(__subject)["SkyName"] == "TkString" { return "String" };  if sky_asMap(__subject)["SkyName"] == "TkChar" { return "Char" };  if sky_asMap(__subject)["SkyName"] == "TkKeyword" { return "Keyword" };  if sky_asMap(__subject)["SkyName"] == "TkOperator" { return "Operator" };  if sky_asMap(__subject)["SkyName"] == "TkEquals" { return "=" };  if sky_asMap(__subject)["SkyName"] == "TkColon" { return ":" };  if sky_asMap(__subject)["SkyName"] == "TkComma" { return "," };  if sky_asMap(__subject)["SkyName"] == "TkDot" { return "." };  if sky_asMap(__subject)["SkyName"] == "TkPipe" { return "|" };  if sky_asMap(__subject)["SkyName"] == "TkArrow" { return "->" };  if sky_asMap(__subject)["SkyName"] == "TkBackslash" { return "\\" };  if sky_asMap(__subject)["SkyName"] == "TkLParen" { return "(" };  if sky_asMap(__subject)["SkyName"] == "TkRParen" { return ")" };  if sky_asMap(__subject)["SkyName"] == "TkLBracket" { return "[" };  if sky_asMap(__subject)["SkyName"] == "TkRBracket" { return "]" };  if sky_asMap(__subject)["SkyName"] == "TkLBrace" { return "{" };  if sky_asMap(__subject)["SkyName"] == "TkRBrace" { return "}" };  if sky_asMap(__subject)["SkyName"] == "TkNewline" { return "newline" };  if sky_asMap(__subject)["SkyName"] == "TkIndent" { return "indent" };  if sky_asMap(__subject)["SkyName"] == "TkDedent" { return "dedent" };  if sky_asMap(__subject)["SkyName"] == "TkEOF" { return "EOF" };  return nil }() }()
}

func Compiler_ParserCore_ParseQualifiedParts(parts any, state any) any {
	return func() any { if sky_asBool(Compiler_ParserCore_MatchKind(TkDot, state)) { return func() any { __tup_dotTok_s1 := Compiler_ParserCore_Advance(state); dotTok := sky_asTuple2(__tup_dotTok_s1).V0; _ = dotTok; s1 := sky_asTuple2(__tup_dotTok_s1).V1; _ = s1; return func() any { if sky_asBool(sky_asBool(Compiler_ParserCore_MatchKind(TkUpperIdentifier, s1)) || sky_asBool(Compiler_ParserCore_MatchKind(TkIdentifier, s1))) { return func() any { __tup_tok_s2 := Compiler_ParserCore_Advance(s1); tok := sky_asTuple2(__tup_tok_s2).V0; _ = tok; s2 := sky_asTuple2(__tup_tok_s2).V1; _ = s2; return Compiler_ParserCore_ParseQualifiedParts(sky_call(sky_listAppend(parts), []any{sky_asMap(tok)["lexeme"]}), s2) }() }; return []any{} }() }() }; return []any{} }()
}

func Compiler_ParserCore_PeekLexeme(state any) any {
	return Compiler_ParserCore_Peek(state)
}

func Compiler_ParserCore_PeekColumn(state any) any {
	return Compiler_ParserCore_Peek(state)
}

func Compiler_ParserCore_PeekKind(state any) any {
	return Compiler_ParserCore_Peek(state)
}

func Compiler_ParserCore_PeekAt1Kind(state any) any {
	return Compiler_ParserCore_PeekAt(1, state)
}

func Compiler_ParserCore_FilterLayout(tokens any) any {
	return sky_call(sky_listFilter(func(t any) any { return func() any { return func() any { __subject := sky_asMap(t)["kind"]; if sky_asMap(__subject)["SkyName"] == "TkNewline" { return false };  if sky_asMap(__subject)["SkyName"] == "TkIndent" { return false };  if sky_asMap(__subject)["SkyName"] == "TkDedent" { return false };  if true { return true };  return nil }() }() }), tokens)
}

var parseVariantFields = Compiler_Parser_ParseVariantFields

var parseTypeArgs = Compiler_Parser_ParseTypeArgs

var Parse = Compiler_Parser_Parse

var parseModule = Compiler_Parser_ParseModule

var parseModuleName = Compiler_Parser_ParseModuleName

var parseModuleNameParts = Compiler_Parser_ParseModuleNameParts

var parseOptionalExposing = Compiler_Parser_ParseOptionalExposing

var parseExposingClause = Compiler_Parser_ParseExposingClause

var parseExposedItems = Compiler_Parser_ParseExposedItems

var parseImports = Compiler_Parser_ParseImports

var parseImport = Compiler_Parser_ParseImport

var getLexemeAt1 = Compiler_Parser_GetLexemeAt1

var parseDeclaration = Compiler_Parser_ParseDeclaration

var parseDeclarations = Compiler_Parser_ParseDeclarations

var parseDeclsHelper = Compiler_Parser_ParseDeclsHelper

var addDeclAndContinue = Compiler_Parser_AddDeclAndContinue

var prependToResult = Compiler_Parser_PrependToResult

var parseForeignImport = Compiler_Parser_ParseForeignImport

var parseTypeAlias = Compiler_Parser_ParseTypeAlias

var parseTypeDecl = Compiler_Parser_ParseTypeDecl

var parseTypeParams = Compiler_Parser_ParseTypeParams

var parseTypeVariants = Compiler_Parser_ParseTypeVariants

var buildVariant = Compiler_Parser_BuildVariant

var finishVariant = Compiler_Parser_FinishVariant

var prependVariant = Compiler_Parser_PrependVariant

var parseTypeExpr = Compiler_Parser_ParseTypeExpr

var parseTypeApp = Compiler_Parser_ParseTypeApp

var applyTypeArgs = Compiler_Parser_ApplyTypeArgs

var resolveTypeApp = Compiler_Parser_ResolveTypeApp

var parseTypePrimary = Compiler_Parser_ParseTypePrimary

var parseTupleTypeRest = Compiler_Parser_ParseTupleTypeRest

var parseRecordType = Compiler_Parser_ParseRecordType

var parseRecordTypeFields = Compiler_Parser_ParseRecordTypeFields

var parseTypeAnnot = Compiler_Parser_ParseTypeAnnot

var parseFunDecl = Compiler_Parser_ParseFunDecl

var parseFunParams = Compiler_Parser_ParseFunParams

func Compiler_Parser_ParseVariantFields(state any) any {
	return func() any { if sky_asBool(sky_asBool(matchKind(TkUpperIdentifier, state)) || sky_asBool(sky_asBool(matchKind(TkIdentifier, state)) || sky_asBool(sky_asBool(matchKind(TkLParen, state)) || sky_asBool(matchKind(TkLBrace, state))))) { return func() any { if sky_asBool(sky_asBool(sky_equal(peekColumn(state), 1)) || sky_asBool(matchKind(TkPipe, state))) { return []any{} }; return func() any { return func() any { __subject := Compiler_Parser_ParseTypePrimary(state); if sky_asSkyResult(__subject).SkyName == "Ok" { te := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = te; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { __tup_rest_s2 := Compiler_Parser_ParseVariantFields(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return []any{} }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  return nil }() }() }() }; return []any{} }()
}

func Compiler_Parser_ParseTypeArgs(state any) any {
	return func() any { if sky_asBool(sky_asBool(matchKind(TkUpperIdentifier, state)) || sky_asBool(sky_asBool(matchKind(TkIdentifier, state)) || sky_asBool(sky_asBool(matchKind(TkLParen, state)) || sky_asBool(matchKind(TkLBrace, state))))) { return func() any { if sky_asBool(sky_asBool(sky_equal(peekColumn(state), 1)) || sky_asBool(sky_asBool(matchKind(TkEquals, state)) || sky_asBool(matchKind(TkPipe, state)))) { return []any{} }; return func() any { return func() any { __subject := Compiler_Parser_ParseTypePrimary(state); if sky_asSkyResult(__subject).SkyName == "Ok" { te := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = te; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { __tup_rest_s2 := Compiler_Parser_ParseTypeArgs(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return []any{} }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  return nil }() }() }() }; return []any{} }()
}

func Compiler_Parser_Parse(tokens any) any {
	return func() any { state := initState(filterLayout(tokens)); _ = state; return func() any { return func() any { __subject := Compiler_Parser_ParseModule(state); if sky_asSkyResult(__subject).SkyName == "Ok" { mod := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = mod; return SkyOk(mod) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  return nil }() }() }()
}

func Compiler_Parser_ParseModule(state any) any {
	return func() any { return func() any { __subject := consumeLex(TkKeyword, "module", state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { return func() any { __subject := Compiler_Parser_ParseModuleName(s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { name := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = name; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { __tup_exposing__s3 := Compiler_Parser_ParseOptionalExposing(s2); exposing_ := sky_asTuple2(__tup_exposing__s3).V0; _ = exposing_; s3 := sky_asTuple2(__tup_exposing__s3).V1; _ = s3; __tup_imports_s4 := Compiler_Parser_ParseImports(s3); imports := sky_asTuple2(__tup_imports_s4).V0; _ = imports; s4 := sky_asTuple2(__tup_imports_s4).V1; _ = s4; __tup_decls_s5 := Compiler_Parser_ParseDeclarations(s4); decls := sky_asTuple2(__tup_decls_s5).V0; _ = decls; s5 := sky_asTuple2(__tup_decls_s5).V1; _ = s5; return SkyOk([]any{}) }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Parser_ParseModuleName(state any) any {
	return func() any { return func() any { __subject := consume(TkUpperIdentifier, state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { first := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = first; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return Compiler_Parser_ParseModuleNameParts([]any{sky_asMap(first)["lexeme"]}, s1) };  return nil }() }()
}

func Compiler_Parser_ParseModuleNameParts(parts any, state any) any {
	return func() any { if sky_asBool(matchKind(TkDot, state)) { return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := consume(TkUpperIdentifier, s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { part := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = part; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return Compiler_Parser_ParseModuleNameParts(sky_asString(parts) + sky_asString([]any{sky_asMap(part)["lexeme"]}), s2) };  return nil }() }() }() }; return SkyOk([]any{}) }()
}

func Compiler_Parser_ParseOptionalExposing(state any) any {
	return func() any { if sky_asBool(matchKindLex(TkKeyword, "exposing", state)) { return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := Compiler_Parser_ParseExposingClause(s1); if sky_asSkyResult(__subject).SkyName == "Ok" { ec := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = ec; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return []any{} };  if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  return nil }() }() }() }; return []any{} }()
}

func Compiler_Parser_ParseExposingClause(state any) any {
	return func() any { return func() any { __subject := consume(TkLParen, state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkDot, s1)) { return func() any { __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; __tup_w_s3 := advance(s2); s3 := sky_asTuple2(__tup_w_s3).V1; _ = s3; return func() any { return func() any { __subject := consume(TkRParen, s3); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s4 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s4; return SkyOk([]any{}) };  return nil }() }() }() }; return func() any { __tup_items_s2 := Compiler_Parser_ParseExposedItems([]any{}, s1); items := sky_asTuple2(__tup_items_s2).V0; _ = items; s2 := sky_asTuple2(__tup_items_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkRParen, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk([]any{}) };  return nil }() }() }() }() };  return nil }() }()
}

func Compiler_Parser_ParseExposedItems(items any, state any) any {
	return func() any { if sky_asBool(matchKind(TkRParen, state)) { return []any{} }; return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; newItems := sky_asString(items) + sky_asString([]any{sky_asMap(tok)["lexeme"]}); _ = newItems; s2 := func() any { if sky_asBool(matchKind(TkComma, s1)) { return func() any { __tup_w_s := advance(s1); s := sky_asTuple2(__tup_w_s).V1; _ = s; return s }() }; return s1 }(); _ = s2; return Compiler_Parser_ParseExposedItems(newItems, s2) }() }()
}

func Compiler_Parser_ParseImports(state any) any {
	return func() any { if sky_asBool(matchKindLex(TkKeyword, "import", state)) { return func() any { return func() any { __subject := Compiler_Parser_ParseImport(state); if sky_asSkyResult(__subject).SkyName == "Ok" { imp := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = imp; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { __tup_rest_s2 := Compiler_Parser_ParseImports(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return []any{} }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  return nil }() }() }; return []any{} }()
}

func Compiler_Parser_ParseImport(state any) any {
	return func() any { return func() any { __subject := consumeLex(TkKeyword, "import", state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { return func() any { __subject := Compiler_Parser_ParseModuleName(s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { modName := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = modName; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { __tup_alias__s3 := func() any { if sky_asBool(matchKindLex(TkKeyword, "as", s2)) { return func() any { __tup_w_sa := advance(s2); sa := sky_asTuple2(__tup_w_sa).V1; _ = sa; __tup_tok_sb := advance(sa); tok := sky_asTuple2(__tup_tok_sb).V0; _ = tok; sb := sky_asTuple2(__tup_tok_sb).V1; _ = sb; return []any{} }() }; return []any{} }(); alias_ := sky_asTuple2(__tup_alias__s3).V0; _ = alias_; s3 := sky_asTuple2(__tup_alias__s3).V1; _ = s3; __tup_exposing__s4 := Compiler_Parser_ParseOptionalExposing(s3); exposing_ := sky_asTuple2(__tup_exposing__s4).V0; _ = exposing_; s4 := sky_asTuple2(__tup_exposing__s4).V1; _ = s4; return SkyOk([]any{}) }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Parser_GetLexemeAt1(state any) any {
	return peekAt(1, state)
}

func Compiler_Parser_ParseDeclaration(state any) any {
	return dispatchDeclaration(peekLexeme(state), Compiler_Parser_GetLexemeAt1(state), state)
}

func Compiler_Parser_ParseDeclarations(state any) any {
	return func() any { if sky_asBool(matchKind(TkEOF, state)) { return []any{} }; return Compiler_Parser_ParseDeclsHelper(Compiler_Parser_ParseDeclaration(state), state) }()
}

func Compiler_Parser_ParseDeclsHelper(result any, origState any) any {
	return func() any { return func() any { __subject := result; if sky_asSkyResult(__subject).SkyName == "Ok" { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return Compiler_Parser_AddDeclAndContinue(sky_fst(pair), sky_snd(pair)) };  if sky_asSkyResult(__subject).SkyName == "Err" { return Compiler_Parser_ParseDeclarations(sky_snd(advance(origState))) };  return nil }() }()
}

func Compiler_Parser_AddDeclAndContinue(decl any, s1 any) any {
	return Compiler_Parser_PrependToResult(decl, Compiler_Parser_ParseDeclarations(s1))
}

func Compiler_Parser_PrependToResult(decl any, result any) any {
	return []any{}
}

func Compiler_Parser_ParseForeignImport(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkString, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { pkgToken := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = pkgToken; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { pkgName := sky_call(sky_call(sky_stringSlice(1), sky_asInt(sky_stringLength(sky_asMap(pkgToken)["lexeme"])) - sky_asInt(1)), sky_asMap(pkgToken)["lexeme"]); _ = pkgName; __tup_exposing__s4 := Compiler_Parser_ParseOptionalExposing(s3); exposing_ := sky_asTuple2(__tup_exposing__s4).V0; _ = exposing_; s4 := sky_asTuple2(__tup_exposing__s4).V1; _ = s4; names := func() any { return func() any { __subject := exposing_; if sky_asMap(__subject)["SkyName"] == "ExposeList" { items := sky_asMap(__subject)["V0"]; _ = items; return items };  if true { return []any{} };  return nil }() }(); _ = names; firstDecl := func() any { return func() any { __subject := sky_listHead(names); if sky_asSkyMaybe(__subject).SkyName == "Just" { n := sky_asSkyMaybe(__subject).JustValue; _ = n; return SkyOk([]any{}) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyErr("Foreign import must expose at least one name") };  return nil }() }(); _ = firstDecl; return firstDecl }() };  return nil }() }() }()
}

func Compiler_Parser_ParseTypeAlias(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkUpperIdentifier, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { name := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = name; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { __tup_params_s4 := Compiler_Parser_ParseTypeParams(s3); params := sky_asTuple2(__tup_params_s4).V0; _ = params; s4 := sky_asTuple2(__tup_params_s4).V1; _ = s4; return func() any { return func() any { __subject := consume(TkEquals, s4); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s5 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s5; return func() any { return func() any { __subject := Compiler_Parser_ParseTypeExpr(s5); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { typeExpr := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = typeExpr; s6 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s6; return SkyOk([]any{}) };  return nil }() }() };  return nil }() }() }() };  return nil }() }() }()
}

func Compiler_Parser_ParseTypeDecl(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := consume(TkUpperIdentifier, s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { name := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = name; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { __tup_params_s3 := Compiler_Parser_ParseTypeParams(s2); params := sky_asTuple2(__tup_params_s3).V0; _ = params; s3 := sky_asTuple2(__tup_params_s3).V1; _ = s3; return func() any { return func() any { __subject := consume(TkEquals, s3); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s4 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s4; return func() any { __tup_variants_s5 := Compiler_Parser_ParseTypeVariants(s4); variants := sky_asTuple2(__tup_variants_s5).V0; _ = variants; s5 := sky_asTuple2(__tup_variants_s5).V1; _ = s5; return SkyOk([]any{}) }() };  return nil }() }() }() };  return nil }() }() }()
}

func Compiler_Parser_ParseTypeParams(state any) any {
	return func() any { if sky_asBool(matchKind(TkIdentifier, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; __tup_rest_s2 := Compiler_Parser_ParseTypeParams(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return []any{} }() }; return []any{} }()
}

func Compiler_Parser_ParseTypeVariants(state any) any {
	return func() any { return func() any { __subject := consume(TkUpperIdentifier, state); if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  if sky_asSkyResult(__subject).SkyName == "Ok" { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return Compiler_Parser_BuildVariant(sky_fst(pair), sky_snd(pair)) };  return nil }() }()
}

func Compiler_Parser_BuildVariant(name any, s1 any) any {
	return Compiler_Parser_FinishVariant(sky_asMap(name)["lexeme"], Compiler_Parser_ParseVariantFields(s1))
}

func Compiler_Parser_FinishVariant(variantName any, fieldResult any) any {
	return func() any { if sky_asBool(matchKind(TkPipe, sky_snd(fieldResult))) { return Compiler_Parser_PrependVariant(map[string]any{"name": variantName, "fields": sky_fst(fieldResult), "span": emptySpan}, Compiler_Parser_ParseTypeVariants(sky_snd(advance(sky_snd(fieldResult))))) }; return []any{} }()
}

func Compiler_Parser_PrependVariant(v any, rest any) any {
	return []any{}
}

func Compiler_Parser_ParseTypeExpr(state any) any {
	return func() any { return func() any { __subject := Compiler_Parser_ParseTypeApp(state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { left := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = left; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkArrow, s1)) { return func() any { __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; return func() any { return func() any { __subject := Compiler_Parser_ParseTypeExpr(s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { right := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = right; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk([]any{}) };  return nil }() }() }() }; return SkyOk([]any{}) }() };  return nil }() }()
}

func Compiler_Parser_ParseTypeApp(state any) any {
	return func() any { return func() any { __subject := Compiler_Parser_ParseTypePrimary(state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return Compiler_Parser_ApplyTypeArgs(sky_fst(pair), sky_snd(pair)) };  return nil }() }()
}

func Compiler_Parser_ApplyTypeArgs(target any, s1 any) any {
	return Compiler_Parser_ResolveTypeApp(target, Compiler_Parser_ParseTypeArgs(s1))
}

func Compiler_Parser_ResolveTypeApp(target any, argsResult any) any {
	return func() any { if sky_asBool(sky_listIsEmpty(sky_fst(argsResult))) { return SkyOk([]any{}) }; return func() any { return func() any { __subject := target; if sky_asMap(__subject)["SkyName"] == "TypeRef" { name := sky_asMap(__subject)["V0"]; _ = name; span := sky_asMap(__subject)["V2"]; _ = span; return SkyOk([]any{}) };  if true { return SkyOk([]any{}) };  return nil }() }() }()
}

func Compiler_Parser_ParseTypePrimary(state any) any {
	return func() any { if sky_asBool(matchKind(TkUpperIdentifier, state)) { return func() any { __tup_id_s1 := advance(state); id := sky_asTuple2(__tup_id_s1).V0; _ = id; s1 := sky_asTuple2(__tup_id_s1).V1; _ = s1; __tup_parts_s2 := parseQualifiedParts([]any{sky_asMap(id)["lexeme"]}, s1); parts := sky_asTuple2(__tup_parts_s2).V0; _ = parts; s2 := sky_asTuple2(__tup_parts_s2).V1; _ = s2; return SkyOk([]any{}) }() }; return func() any { if sky_asBool(matchKind(TkIdentifier, state)) { return func() any { __tup_id_s1 := advance(state); id := sky_asTuple2(__tup_id_s1).V0; _ = id; s1 := sky_asTuple2(__tup_id_s1).V1; _ = s1; return SkyOk([]any{}) }() }; return func() any { if sky_asBool(matchKind(TkLParen, state)) { return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkRParen, s1)) { return func() any { __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; return SkyOk([]any{}) }() }; return func() any { return func() any { __subject := Compiler_Parser_ParseTypeExpr(s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { inner := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = inner; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { if sky_asBool(matchKind(TkComma, s2)) { return Compiler_Parser_ParseTupleTypeRest([]any{inner}, s2) }; return func() any { return func() any { __subject := consume(TkRParen, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk([]any{}) };  return nil }() }() }() };  return nil }() }() }() }() }; return func() any { if sky_asBool(matchKind(TkLBrace, state)) { return Compiler_Parser_ParseRecordType(state) }; return SkyErr(sky_asString("Unexpected token in type: ") + sky_asString(peekLexeme(state))) }() }() }() }()
}

func Compiler_Parser_ParseTupleTypeRest(items any, state any) any {
	return func() any { if sky_asBool(matchKind(TkComma, state)) { return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := Compiler_Parser_ParseTypeExpr(s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { item := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = item; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return Compiler_Parser_ParseTupleTypeRest(sky_asString(items) + sky_asString([]any{item}), s2) };  return nil }() }() }() }; return func() any { return func() any { __subject := consume(TkRParen, state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return SkyOk([]any{}) };  return nil }() }() }()
}

func Compiler_Parser_ParseRecordType(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; __tup_fields_s2 := Compiler_Parser_ParseRecordTypeFields([]any{}, s1); fields := sky_asTuple2(__tup_fields_s2).V0; _ = fields; s2 := sky_asTuple2(__tup_fields_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkRBrace, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk([]any{}) };  return nil }() }() }()
}

func Compiler_Parser_ParseRecordTypeFields(fields any, state any) any {
	return func() any { if sky_asBool(matchKind(TkRBrace, state)) { return []any{} }; return func() any { return func() any { __subject := consume(TkIdentifier, state); if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  if sky_asSkyResult(__subject).SkyName == "Ok" { name := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = name; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { return func() any { __subject := consume(TkColon, s1); if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := Compiler_Parser_ParseTypeExpr(s2); if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  if sky_asSkyResult(__subject).SkyName == "Ok" { te := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = te; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { newFields := sky_asString(fields) + sky_asString([]any{map[string]any{"name": sky_asMap(name)["lexeme"], "type_": te}}); _ = newFields; s4 := func() any { if sky_asBool(matchKind(TkComma, s3)) { return func() any { __tup_w_s := advance(s3); s := sky_asTuple2(__tup_w_s).V1; _ = s; return s }() }; return s3 }(); _ = s4; return Compiler_Parser_ParseRecordTypeFields(newFields, s4) }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }()
}

func Compiler_Parser_ParseTypeAnnot(state any) any {
	return func() any { __tup_name_s1 := advance(state); name := sky_asTuple2(__tup_name_s1).V0; _ = name; s1 := sky_asTuple2(__tup_name_s1).V1; _ = s1; return func() any { return func() any { __subject := consume(TkColon, s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := Compiler_Parser_ParseTypeExpr(s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { te := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = te; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk([]any{}) };  return nil }() }() };  return nil }() }() }()
}

func Compiler_Parser_ParseFunDecl(state any) any {
	return func() any { __tup_name_s1 := advance(state); name := sky_asTuple2(__tup_name_s1).V0; _ = name; s1 := sky_asTuple2(__tup_name_s1).V1; _ = s1; __tup_params_s2 := Compiler_Parser_ParseFunParams(s1); params := sky_asTuple2(__tup_params_s2).V0; _ = params; s2 := sky_asTuple2(__tup_params_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkEquals, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s3); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { body := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = body; s4 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s4; return SkyOk([]any{}) };  return nil }() }() };  return nil }() }() }()
}

func Compiler_Parser_ParseFunParams(state any) any {
	return func() any { if sky_asBool(matchKind(TkEquals, state)) { return []any{} }; return func() any { if sky_asBool(matchKind(TkIdentifier, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; pat := func() any { if sky_asBool(sky_equal(sky_asMap(tok)["lexeme"], "_")) { return PWildcard(emptySpan) }; return PVariable(sky_asMap(tok)["lexeme"], emptySpan) }(); _ = pat; __tup_rest_s2 := Compiler_Parser_ParseFunParams(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return []any{} }() }; return func() any { if sky_asBool(matchKind(TkLParen, state)) { return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(state); if sky_asSkyResult(__subject).SkyName == "Ok" { pat := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = pat; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { __tup_rest_s2 := Compiler_Parser_ParseFunParams(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return []any{} }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  return nil }() }() }; return []any{} }() }() }()
}

var Lex = Compiler_Lexer_Lex

var LexLoop = Compiler_Lexer_LexLoop

var HandleNewline = Compiler_Lexer_HandleNewline

var CountIndent = Compiler_Lexer_CountIndent

var SkipWhitespace = Compiler_Lexer_SkipWhitespace

var SkipLineComment = Compiler_Lexer_SkipLineComment

var LexString = Compiler_Lexer_LexString

var LexStringBody = Compiler_Lexer_LexStringBody

var LexChar = Compiler_Lexer_LexChar

var LexNumber = Compiler_Lexer_LexNumber

var ConsumeDigits = Compiler_Lexer_ConsumeDigits

var LexIdentifier = Compiler_Lexer_LexIdentifier

var ConsumeIdentChars = Compiler_Lexer_ConsumeIdentChars

var LexOperatorOrPunctuation = Compiler_Lexer_LexOperatorOrPunctuation

var ConsumeOperator = Compiler_Lexer_ConsumeOperator

var IsOperatorChar = Compiler_Lexer_IsOperatorChar

var CharAt = Compiler_Lexer_CharAt

var PeekChar = Compiler_Lexer_PeekChar

var MakeSpan = Compiler_Lexer_MakeSpan

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
	return func() any { startOffset := sky_asMap(state)["offset"]; _ = startOffset; startCol := sky_asMap(state)["column"]; _ = startCol; s1 := sky_recordUpdate(state, map[string]any{"offset": sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(1), "column": sky_asInt(sky_asMap(state)["column"]) + sky_asInt(1)}); _ = s1; s2 := Compiler_Lexer_LexStringBody(s1); _ = s2; s3 := sky_recordUpdate(s2, map[string]any{"offset": sky_asInt(sky_asMap(s2)["offset"]) + sky_asInt(1), "column": sky_asInt(sky_asMap(s2)["column"]) + sky_asInt(1)}); _ = s3; lexeme := sky_call(sky_call(sky_stringSlice(startOffset), sky_asMap(s3)["offset"]), sky_asMap(state)["source"]); _ = lexeme; token := map[string]any{"kind": TkString, "lexeme": lexeme, "span": map[string]any{"start": map[string]any{"offset": startOffset, "line": sky_asMap(state)["line"], "column": startCol}, "end": map[string]any{"offset": sky_asMap(s3)["offset"], "line": sky_asMap(s3)["line"], "column": sky_asMap(s3)["column"]}}}; _ = token; return sky_recordUpdate(s3, map[string]any{"tokens": append([]any{token}, sky_asList(sky_asMap(s3)["tokens"])...)}) }()
}

func Compiler_Lexer_LexStringBody(state any) any {
	return func() any { if sky_asBool(sky_asInt(sky_asMap(state)["offset"]) >= sky_asInt(sky_stringLength(sky_asMap(state)["source"]))) { return state }; return func() any { ch := Compiler_Lexer_CharAt(sky_asMap(state)["source"], sky_asMap(state)["offset"]); _ = ch; return func() any { if sky_asBool(sky_equal(ch, string('"'))) { return state }; return func() any { if sky_asBool(sky_equal(ch, string('\\'))) { return Compiler_Lexer_LexStringBody(sky_recordUpdate(state, map[string]any{"offset": sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(2), "column": sky_asInt(sky_asMap(state)["column"]) + sky_asInt(2)})) }; return Compiler_Lexer_LexStringBody(sky_recordUpdate(state, map[string]any{"offset": sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(1), "column": sky_asInt(sky_asMap(state)["column"]) + sky_asInt(1)})) }() }() }() }()
}

func Compiler_Lexer_LexChar(state any) any {
	return func() any { startOffset := sky_asMap(state)["offset"]; _ = startOffset; startCol := sky_asMap(state)["column"]; _ = startCol; s1 := sky_recordUpdate(state, map[string]any{"offset": sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(1), "column": sky_asInt(sky_asMap(state)["column"]) + sky_asInt(1)}); _ = s1; s2 := func() any { if sky_asBool(sky_equal(Compiler_Lexer_CharAt(sky_asMap(s1)["source"], sky_asMap(s1)["offset"]), string('\\'))) { return sky_recordUpdate(s1, map[string]any{"offset": sky_asInt(sky_asMap(s1)["offset"]) + sky_asInt(2), "column": sky_asInt(sky_asMap(s1)["column"]) + sky_asInt(2)}) }; return sky_recordUpdate(s1, map[string]any{"offset": sky_asInt(sky_asMap(s1)["offset"]) + sky_asInt(1), "column": sky_asInt(sky_asMap(s1)["column"]) + sky_asInt(1)}) }(); _ = s2; s3 := sky_recordUpdate(s2, map[string]any{"offset": sky_asInt(sky_asMap(s2)["offset"]) + sky_asInt(1), "column": sky_asInt(sky_asMap(s2)["column"]) + sky_asInt(1)}); _ = s3; lexeme := sky_call(sky_call(sky_stringSlice(startOffset), sky_asMap(s3)["offset"]), sky_asMap(state)["source"]); _ = lexeme; token := map[string]any{"kind": TkChar, "lexeme": lexeme, "span": map[string]any{"start": map[string]any{"offset": startOffset, "line": sky_asMap(state)["line"], "column": startCol}, "end": map[string]any{"offset": sky_asMap(s3)["offset"], "line": sky_asMap(s3)["line"], "column": sky_asMap(s3)["column"]}}}; _ = token; return sky_recordUpdate(s3, map[string]any{"tokens": append([]any{token}, sky_asList(sky_asMap(s3)["tokens"])...)}) }()
}

func Compiler_Lexer_LexNumber(state any) any {
	return func() any { startOffset := sky_asMap(state)["offset"]; _ = startOffset; startCol := sky_asMap(state)["column"]; _ = startCol; s1 := Compiler_Lexer_ConsumeDigits(state); _ = s1; result := func() any { if sky_asBool(sky_asBool(sky_asInt(sky_asMap(s1)["offset"]) < sky_asInt(sky_stringLength(sky_asMap(s1)["source"]))) && sky_asBool(sky_equal(Compiler_Lexer_CharAt(sky_asMap(s1)["source"], sky_asMap(s1)["offset"]), string('.')))) { return func() any { s2 := Compiler_Lexer_ConsumeDigits(sky_recordUpdate(s1, map[string]any{"offset": sky_asInt(sky_asMap(s1)["offset"]) + sky_asInt(1), "column": sky_asInt(sky_asMap(s1)["column"]) + sky_asInt(1)})); _ = s2; return []any{} }() }; return []any{} }(); _ = result; finalState := sky_fst(result); _ = finalState; kind := sky_snd(result); _ = kind; lexeme := sky_call(sky_call(sky_stringSlice(startOffset), sky_asMap(finalState)["offset"]), sky_asMap(state)["source"]); _ = lexeme; token := map[string]any{"kind": kind, "lexeme": lexeme, "span": map[string]any{"start": map[string]any{"offset": startOffset, "line": sky_asMap(state)["line"], "column": startCol}, "end": map[string]any{"offset": sky_asMap(finalState)["offset"], "line": sky_asMap(finalState)["line"], "column": sky_asMap(finalState)["column"]}}}; _ = token; return sky_recordUpdate(finalState, map[string]any{"tokens": append([]any{token}, sky_asList(sky_asMap(finalState)["tokens"])...)}) }()
}

func Compiler_Lexer_ConsumeDigits(state any) any {
	return func() any { if sky_asBool(sky_asInt(sky_asMap(state)["offset"]) >= sky_asInt(sky_stringLength(sky_asMap(state)["source"]))) { return state }; return func() any { if sky_asBool(sky_charIsDigit(Compiler_Lexer_CharAt(sky_asMap(state)["source"], sky_asMap(state)["offset"]))) { return Compiler_Lexer_ConsumeDigits(sky_recordUpdate(state, map[string]any{"offset": sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(1), "column": sky_asInt(sky_asMap(state)["column"]) + sky_asInt(1)})) }; return state }() }()
}

func Compiler_Lexer_LexIdentifier(state any) any {
	return func() any { startOffset := sky_asMap(state)["offset"]; _ = startOffset; startCol := sky_asMap(state)["column"]; _ = startCol; s1 := Compiler_Lexer_ConsumeIdentChars(state); _ = s1; lexeme := sky_call(sky_call(sky_stringSlice(startOffset), sky_asMap(s1)["offset"]), sky_asMap(state)["source"]); _ = lexeme; kind := func() any { if sky_asBool(isKeyword(lexeme)) { return TkKeyword }; return func() any { if sky_asBool(sky_charIsUpper(Compiler_Lexer_CharAt(sky_asMap(state)["source"], startOffset))) { return TkUpperIdentifier }; return TkIdentifier }() }(); _ = kind; token := map[string]any{"kind": kind, "lexeme": lexeme, "span": map[string]any{"start": map[string]any{"offset": startOffset, "line": sky_asMap(state)["line"], "column": startCol}, "end": map[string]any{"offset": sky_asMap(s1)["offset"], "line": sky_asMap(s1)["line"], "column": sky_asMap(s1)["column"]}}}; _ = token; return sky_recordUpdate(s1, map[string]any{"tokens": append([]any{token}, sky_asList(sky_asMap(s1)["tokens"])...)}) }()
}

func Compiler_Lexer_ConsumeIdentChars(state any) any {
	return func() any { if sky_asBool(sky_asInt(sky_asMap(state)["offset"]) >= sky_asInt(sky_stringLength(sky_asMap(state)["source"]))) { return state }; return func() any { ch := Compiler_Lexer_CharAt(sky_asMap(state)["source"], sky_asMap(state)["offset"]); _ = ch; return func() any { if sky_asBool(sky_asBool(sky_charIsAlphaNum(ch)) || sky_asBool(sky_equal(ch, string('_')))) { return Compiler_Lexer_ConsumeIdentChars(sky_recordUpdate(state, map[string]any{"offset": sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(1), "column": sky_asInt(sky_asMap(state)["column"]) + sky_asInt(1)})) }; return state }() }() }()
}

func Compiler_Lexer_LexOperatorOrPunctuation(state any) any {
	return func() any { ch := Compiler_Lexer_CharAt(sky_asMap(state)["source"], sky_asMap(state)["offset"]); _ = ch; next := Compiler_Lexer_PeekChar(sky_asMap(state)["source"], sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(1)); _ = next; startOffset := sky_asMap(state)["offset"]; _ = startOffset; startCol := sky_asMap(state)["column"]; _ = startCol; __tup_kind_len := func() any { if sky_asBool(sky_equal(ch, string('('))) { return []any{} }; return func() any { if sky_asBool(sky_equal(ch, string(')'))) { return []any{} }; return func() any { if sky_asBool(sky_equal(ch, string('['))) { return []any{} }; return func() any { if sky_asBool(sky_equal(ch, string(']'))) { return []any{} }; return func() any { if sky_asBool(sky_equal(ch, string('{'))) { return []any{} }; return func() any { if sky_asBool(sky_equal(ch, string('}'))) { return []any{} }; return func() any { if sky_asBool(sky_equal(ch, string(','))) { return []any{} }; return func() any { if sky_asBool(sky_equal(ch, string('\\'))) { return []any{} }; return func() any { if sky_asBool(sky_equal(ch, string('.'))) { return []any{} }; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, string(':'))) && sky_asBool(!sky_equal(next, string(':')))) { return []any{} }; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, string('='))) && sky_asBool(!sky_equal(next, string('=')))) { return []any{} }; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, string('|'))) && sky_asBool(sky_asBool(!sky_equal(next, string('>'))) && sky_asBool(!sky_equal(next, string('|'))))) { return []any{} }; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, string('-'))) && sky_asBool(sky_equal(next, string('>')))) { return []any{} }; return func() any { opLen := Compiler_Lexer_ConsumeOperator(sky_asMap(state)["source"], sky_asMap(state)["offset"]); _ = opLen; return []any{} }() }() }() }() }() }() }() }() }() }() }() }() }() }(); kind := sky_asTuple2(__tup_kind_len).V0; _ = kind; len := sky_asTuple2(__tup_kind_len).V1; _ = len; s1 := sky_recordUpdate(state, map[string]any{"offset": sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(len), "column": sky_asInt(sky_asMap(state)["column"]) + sky_asInt(len)}); _ = s1; lexeme := sky_call(sky_call(sky_stringSlice(startOffset), sky_asMap(s1)["offset"]), sky_asMap(state)["source"]); _ = lexeme; token := map[string]any{"kind": kind, "lexeme": lexeme, "span": map[string]any{"start": map[string]any{"offset": startOffset, "line": sky_asMap(state)["line"], "column": startCol}, "end": map[string]any{"offset": sky_asMap(s1)["offset"], "line": sky_asMap(s1)["line"], "column": sky_asMap(s1)["column"]}}}; _ = token; return sky_recordUpdate(s1, map[string]any{"tokens": append([]any{token}, sky_asList(sky_asMap(s1)["tokens"])...)}) }()
}

func Compiler_Lexer_ConsumeOperator(source any, offset any) any {
	return func() any { if sky_asBool(sky_asInt(offset) >= sky_asInt(sky_stringLength(source))) { return 0 }; return func() any { ch := Compiler_Lexer_CharAt(source, offset); _ = ch; return func() any { if sky_asBool(Compiler_Lexer_IsOperatorChar(ch)) { return sky_asInt(1) + sky_asInt(Compiler_Lexer_ConsumeOperator(source, sky_asInt(offset) + sky_asInt(1))) }; return 0 }() }() }()
}

func Compiler_Lexer_IsOperatorChar(ch any) any {
	return sky_asBool(sky_equal(ch, string('+'))) || sky_asBool(sky_asBool(sky_equal(ch, string('-'))) || sky_asBool(sky_asBool(sky_equal(ch, string('*'))) || sky_asBool(sky_asBool(sky_equal(ch, string('/'))) || sky_asBool(sky_asBool(sky_equal(ch, string('='))) || sky_asBool(sky_asBool(sky_equal(ch, string('<'))) || sky_asBool(sky_asBool(sky_equal(ch, string('>'))) || sky_asBool(sky_asBool(sky_equal(ch, string('!'))) || sky_asBool(sky_asBool(sky_equal(ch, string('&'))) || sky_asBool(sky_asBool(sky_equal(ch, string('|'))) || sky_asBool(sky_asBool(sky_equal(ch, string(':'))) || sky_asBool(sky_asBool(sky_equal(ch, string('%'))) || sky_asBool(sky_asBool(sky_equal(ch, string('^'))) || sky_asBool(sky_equal(ch, string('~')))))))))))))))
}

func Compiler_Lexer_CharAt(s any, i any) any {
	return func() any { c := sky_call(sky_call(sky_stringSlice(i), sky_asInt(i) + sky_asInt(1)), s); _ = c; return func() any { if sky_asBool(sky_equal(c, "")) { return string(' ') }; return sky_js(c) }() }()
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

var FixUnusedVars = Compiler_Pipeline_FixUnusedVars

var MakeGoPackage = Compiler_Pipeline_MakeGoPackage

var LoadLocalModules = Compiler_Pipeline_LoadLocalModules

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

var FindModule = Compiler_Pipeline_FindModule

var ExtractExportedNames = Compiler_Pipeline_ExtractExportedNames

var ExtractDeclNames = Compiler_Pipeline_ExtractDeclNames

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

func Compiler_Pipeline_FindCharIdx(ch any, str any, idx any) any {
	return func() any { if sky_asBool(sky_asInt(idx) >= sky_asInt(sky_stringLength(str))) { return -1 }; return func() any { if sky_asBool(sky_equal(sky_call(sky_call(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1)), str), sky_stringFromChar(ch))) { return idx }; return Compiler_Pipeline_FindCharIdx(ch, str, sky_asInt(idx) + sky_asInt(1)) }() }()
}

func Compiler_Pipeline_FindLastSlash(path any, idx any) any {
	return func() any { if sky_asBool(sky_asInt(idx) < sky_asInt(0)) { return -1 }; return func() any { if sky_asBool(sky_equal(sky_call(sky_call(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1)), path), "/")) { return idx }; return Compiler_Pipeline_FindLastSlash(path, sky_asInt(idx) - sky_asInt(1)) }() }()
}

func Compiler_Pipeline_Compile(filePath any, outDir any) any {
	return func() any { sky_println("╔══════════════════════════════════════════════════╗"); sky_println("║  Sky Self-Hosted Compiler v0.4.2                ║"); sky_println("╚══════════════════════════════════════════════════╝"); sky_println(""); return func() any { return func() any { __subject := sky_fileRead(filePath); if sky_asSkyResult(__subject).SkyName == "Err" { readErr := sky_asSkyResult(__subject).ErrValue; _ = readErr; return SkyErr(sky_asString("Cannot read file: ") + sky_asString(sky_asString(filePath) + sky_asString(sky_asString(" (") + sky_asString(sky_asString(readErr) + sky_asString(")"))))) };  if sky_asSkyResult(__subject).SkyName == "Ok" { source := sky_asSkyResult(__subject).OkValue; _ = source; return func() any { lexResult := Compiler_Lexer_Lex(source); _ = lexResult; return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if sky_asSkyResult(__subject).SkyName == "Err" { parseErr := sky_asSkyResult(__subject).ErrValue; _ = parseErr; return SkyErr(sky_asString("Parse error: ") + sky_asString(parseErr)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { mod := sky_asSkyResult(__subject).OkValue; _ = mod; return func() any { srcRoot := Compiler_Pipeline_DirOfPath(filePath); _ = srcRoot; hasLocalImports := sky_call(sky_call(sky_listFoldl(func(imp any) any { return func(acc any) any { return sky_asBool(acc) || sky_asBool(sky_not(Compiler_Resolver_IsStdlib(sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"])))) } }), false), sky_asMap(mod)["imports"]); _ = hasLocalImports; return func() any { if sky_asBool(hasLocalImports) { return Compiler_Pipeline_CompileMultiModule(filePath, outDir, srcRoot, mod) }; return Compiler_Pipeline_CompileSource(filePath, outDir, source) }() }() };  return nil }() }() }() };  return nil }() }() }()
}

func Compiler_Pipeline_CompileMultiModule(entryPath any, outDir any, srcRoot any, entryMod any) any {
	return func() any { localImports := sky_call(sky_listFilter(func(imp any) any { return sky_not(Compiler_Resolver_IsStdlib(sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]))) }), sky_asMap(entryMod)["imports"]); _ = localImports; loadedModules := Compiler_Pipeline_LoadLocalModules(srcRoot, localImports, []any{}); _ = loadedModules; aliasMap := Compiler_Pipeline_BuildAliasMap(sky_asMap(entryMod)["imports"]); _ = aliasMap; stdlibEnv := Compiler_Resolver_BuildStdlibEnv(); _ = stdlibEnv; depDecls := sky_call(sky_listConcatMap(func(__pa0 any) any { return Compiler_Pipeline_CompileDependencyModule(stdlibEnv, loadedModules, __pa0) }), loadedModules); _ = depDecls; return Compiler_Pipeline_CompileMultiModuleEntry(outDir, entryMod, aliasMap, stdlibEnv, depDecls, loadedModules) }()
}

func Compiler_Pipeline_CompileMultiModuleEntry(outDir any, entryMod any, aliasMap any, stdlibEnv any, depDecls any, loadedModules any) any {
	return func() any { entryCheckResult := Compiler_Checker_CheckModule(entryMod, SkyJust(stdlibEnv)); _ = entryCheckResult; entryRegistry := func() any { return func() any { __subject := entryCheckResult; if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return sky_asMap(result)["registry"] };  if sky_asSkyResult(__subject).SkyName == "Err" { return Compiler_Adt_EmptyRegistry() };  return nil }() }(); _ = entryRegistry; return Compiler_Pipeline_EmitMultiModuleGo(outDir, entryMod, entryRegistry, aliasMap, depDecls, loadedModules) }()
}

func Compiler_Pipeline_EmitMultiModuleGo(outDir any, entryMod any, entryRegistry any, aliasMap any, depDecls any, loadedModules any) any {
	return func() any { baseCtx := Compiler_Lower_EmptyCtx(); _ = baseCtx; entryCtx := sky_recordUpdate(baseCtx, map[string]any{"registry": entryRegistry, "localFunctions": Compiler_Lower_CollectLocalFunctions(sky_asMap(entryMod)["declarations"]), "importedConstructors": Compiler_Lower_BuildConstructorMap(entryRegistry), "importAliases": aliasMap}); _ = entryCtx; entryGoDecls := Compiler_Lower_LowerDeclarations(entryCtx, sky_asMap(entryMod)["declarations"]); _ = entryGoDecls; entryCtorDecls := Compiler_Lower_GenerateConstructorDecls(entryRegistry, sky_asMap(entryMod)["declarations"]); _ = entryCtorDecls; helperDecls := Compiler_Lower_GenerateHelperDecls(); _ = helperDecls; allDecls := Compiler_Pipeline_DeduplicateDecls(sky_listConcat([]any{helperDecls, depDecls, entryCtorDecls, entryGoDecls})); _ = allDecls; return Compiler_Pipeline_WriteMultiModuleOutput(outDir, allDecls) }()
}

func Compiler_Pipeline_WriteMultiModuleOutput(outDir any, allDecls any) any {
	return func() any { goPackage := Compiler_Pipeline_MakeGoPackage(allDecls); _ = goPackage; rawGoCode := Compiler_Emit_EmitPackage(goPackage); _ = rawGoCode; goCode := Compiler_Pipeline_FixUnusedVars(rawGoCode); _ = goCode; outPath := sky_asString(outDir) + sky_asString("/main.go"); _ = outPath; sky_fileMkdirAll(outDir); sky_call(sky_fileWrite(outPath), goCode); sky_println(sky_asString("   Wrote ") + sky_asString(outPath)); sky_println(sky_asString("   ") + sky_asString(sky_asString(sky_stringFromInt(sky_listLength(allDecls))) + sky_asString(" total Go declarations"))); sky_println(""); sky_println("✓ Compilation successful"); return SkyOk(goCode) }()
}

func Compiler_Pipeline_FixUnusedVars(code any) any {
	return code
}

func Compiler_Pipeline_MakeGoPackage(decls any) any {
	return map[string]any{"name": "main", "imports": []any{map[string]any{"path": "fmt", "alias_": ""}, map[string]any{"path": "bufio", "alias_": ""}, map[string]any{"path": "os", "alias_": ""}, map[string]any{"path": "os/exec", "alias_": "exec"}, map[string]any{"path": "strconv", "alias_": ""}, map[string]any{"path": "strings", "alias_": ""}}, "declarations": decls}
}

func Compiler_Pipeline_LoadLocalModules(srcRoot any, imports any, acc any) any {
	return func() any { return func() any { __subject := imports; if len(sky_asList(__subject)) == 0 { return sky_listReverse(acc) };  if len(sky_asList(__subject)) > 0 { imp := sky_asList(__subject)[0]; _ = imp; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { modName := sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]); _ = modName; filePath := Compiler_Resolver_ResolveModulePath(srcRoot, sky_asMap(imp)["moduleName"]); _ = filePath; return func() any { if sky_asBool(sky_asBool(Compiler_Resolver_IsStdlib(modName)) || sky_asBool(Compiler_Pipeline_IsModuleLoaded(modName, acc))) { return Compiler_Pipeline_LoadLocalModules(srcRoot, rest, acc) }; return func() any { return func() any { __subject := sky_fileRead(filePath); if sky_asSkyResult(__subject).SkyName == "Err" { return Compiler_Pipeline_LoadLocalModules(srcRoot, rest, acc) };  if sky_asSkyResult(__subject).SkyName == "Ok" { source := sky_asSkyResult(__subject).OkValue; _ = source; return func() any { lexResult := Compiler_Lexer_Lex(source); _ = lexResult; return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if sky_asSkyResult(__subject).SkyName == "Err" { return Compiler_Pipeline_LoadLocalModules(srcRoot, rest, acc) };  if sky_asSkyResult(__subject).SkyName == "Ok" { mod := sky_asSkyResult(__subject).OkValue; _ = mod; return func() any { transImports := sky_call(sky_listFilter(func(i any) any { return sky_not(Compiler_Resolver_IsStdlib(sky_call(sky_stringJoin("."), sky_asMap(i)["moduleName"]))) }), sky_asMap(mod)["imports"]); _ = transImports; withTransitive := Compiler_Pipeline_LoadLocalModules(srcRoot, transImports, append([]any{[]any{}}, sky_asList(acc)...)); _ = withTransitive; return Compiler_Pipeline_LoadLocalModules(srcRoot, rest, withTransitive) }() };  return nil }() }() }() };  return nil }() }() }() }() };  return nil }() }()
}

func Compiler_Pipeline_BuildAliasMap(imports any) any {
	return sky_call(sky_call(sky_listFoldl(Compiler_Pipeline_AddImportAlias), sky_dictEmpty), imports)
}

func Compiler_Pipeline_GeneratePrefixAliases(prefix any, decls any) any {
	return sky_call(sky_listFilterMap(func(__pa0 any) any { return Compiler_Pipeline_MakePrefixAlias(prefix, __pa0) }), decls)
}

func Compiler_Pipeline_MakePrefixAlias(prefix any, decl any) any {
	return func() any { fullName := Compiler_Pipeline_GetDeclName(decl); _ = fullName; return func() any { if sky_asBool(sky_stringIsEmpty(fullName)) { return SkyNothing() }; return func() any { if sky_asBool(sky_not(sky_call(sky_stringStartsWith(prefix), fullName))) { return SkyNothing() }; return func() any { unprefixed := sky_call(sky_call(sky_stringSlice(sky_asInt(sky_stringLength(prefix)) + sky_asInt(1)), sky_stringLength(fullName)), fullName); _ = unprefixed; firstChar := sky_call(sky_call(sky_stringSlice(0), 1), unprefixed); _ = firstChar; return func() any { if sky_asBool(sky_stringIsEmpty(unprefixed)) { return SkyNothing() }; return func() any { if sky_asBool(Compiler_Pipeline_IsCommonName(unprefixed)) { return SkyNothing() }; return SkyJust(GoDeclRaw(sky_asString("var ") + sky_asString(sky_asString(unprefixed) + sky_asString(sky_asString(" = ") + sky_asString(fullName))))) }() }() }() }() }() }()
}

func Compiler_Pipeline_IsCommonName(name any) any {
	return sky_asBool(sky_equal(name, "main")) || sky_asBool(sky_equal(name, "_"))
}

func Compiler_Pipeline_IsSharedValue(name any) any {
	return sky_asBool(sky_equal(name, "emptySub")) || sky_asBool(sky_asBool(sky_equal(name, "emptySpan")) || sky_asBool(sky_asBool(sky_equal(name, "emptyResult")) || sky_asBool(sky_asBool(sky_equal(name, "emptyRegistry")) || sky_asBool(sky_asBool(sky_equal(name, "emptyCtx")) || sky_asBool(sky_asBool(sky_equal(name, "applySub")) || sky_asBool(sky_asBool(sky_equal(name, "freshVar")) || sky_asBool(sky_asBool(sky_equal(name, "unify")) || sky_asBool(sky_asBool(sky_equal(name, "composeSubs")) || sky_asBool(sky_asBool(sky_equal(name, "freeVars")) || sky_asBool(sky_asBool(sky_equal(name, "freeVarsInScheme")) || sky_asBool(sky_asBool(sky_equal(name, "instantiate")) || sky_asBool(sky_asBool(sky_equal(name, "generalize")) || sky_asBool(sky_asBool(sky_equal(name, "mono")) || sky_asBool(sky_asBool(sky_equal(name, "formatType")) || sky_asBool(sky_asBool(sky_equal(name, "applySubToScheme")) || sky_asBool(sky_asBool(sky_equal(name, "initState")) || sky_asBool(sky_asBool(sky_equal(name, "filterLayout")) || sky_asBool(sky_asBool(sky_equal(name, "consume")) || sky_asBool(sky_asBool(sky_equal(name, "consumeLex")) || sky_asBool(sky_asBool(sky_equal(name, "matchKind")) || sky_asBool(sky_asBool(sky_equal(name, "matchLexeme")) || sky_asBool(sky_asBool(sky_equal(name, "matchKindLex")) || sky_asBool(sky_asBool(sky_equal(name, "advance")) || sky_asBool(sky_asBool(sky_equal(name, "peek")) || sky_asBool(sky_asBool(sky_equal(name, "peekAt")) || sky_asBool(sky_asBool(sky_equal(name, "previous")) || sky_asBool(sky_asBool(sky_equal(name, "tokenKindEq")) || sky_asBool(sky_asBool(sky_equal(name, "tokenKindStr")) || sky_asBool(sky_asBool(sky_equal(name, "parseQualifiedParts")) || sky_asBool(sky_asBool(sky_equal(name, "isKeyword")) || sky_asBool(sky_asBool(sky_equal(name, "peekLexeme")) || sky_asBool(sky_asBool(sky_equal(name, "peekColumn")) || sky_asBool(sky_asBool(sky_equal(name, "peekKind")) || sky_asBool(sky_asBool(sky_equal(name, "peekAt1Kind")) || sky_asBool(sky_asBool(sky_equal(name, "getLexemeAt1")) || sky_asBool(sky_asBool(sky_equal(name, "dispatchDeclaration")) || sky_asBool(sky_asBool(sky_equal(name, "parseDeclsHelper")) || sky_asBool(sky_asBool(sky_equal(name, "addDeclAndContinue")) || sky_asBool(sky_asBool(sky_equal(name, "prependToResult")) || sky_asBool(sky_asBool(sky_equal(name, "buildVariant")) || sky_asBool(sky_asBool(sky_equal(name, "finishVariant")) || sky_asBool(sky_asBool(sky_equal(name, "prependVariant")) || sky_asBool(sky_asBool(sky_equal(name, "applyTypeArgs")) || sky_asBool(sky_asBool(sky_equal(name, "resolveTypeApp")) || sky_asBool(sky_asBool(sky_equal(name, "parseVariantFields")) || sky_asBool(sky_asBool(sky_equal(name, "parseTypeParams")) || sky_asBool(sky_asBool(sky_equal(name, "parseTypeVariants")) || sky_asBool(sky_asBool(sky_equal(name, "parseTypeExpr")) || sky_asBool(sky_asBool(sky_equal(name, "parseTypeApp")) || sky_asBool(sky_asBool(sky_equal(name, "parseTypeArgs")) || sky_asBool(sky_asBool(sky_equal(name, "parseTypePrimary")) || sky_asBool(sky_asBool(sky_equal(name, "parseTupleTypeRest")) || sky_asBool(sky_asBool(sky_equal(name, "parseRecordType")) || sky_asBool(sky_asBool(sky_equal(name, "parseRecordTypeFields")) || sky_asBool(sky_asBool(sky_equal(name, "parseExposingClause")) || sky_asBool(sky_asBool(sky_equal(name, "parseExposedItems")) || sky_asBool(sky_asBool(sky_equal(name, "parseModuleName")) || sky_asBool(sky_asBool(sky_equal(name, "parseModuleNameParts")) || sky_asBool(sky_asBool(sky_equal(name, "parseOptionalExposing")) || sky_asBool(sky_asBool(sky_equal(name, "parseImports")) || sky_asBool(sky_asBool(sky_equal(name, "parseImport")) || sky_asBool(sky_asBool(sky_equal(name, "parseForeignImport")) || sky_asBool(sky_asBool(sky_equal(name, "parseTypeAlias")) || sky_asBool(sky_asBool(sky_equal(name, "parseTypeDecl")) || sky_asBool(sky_asBool(sky_equal(name, "parseTypeAnnot")) || sky_asBool(sky_asBool(sky_equal(name, "parseFunDecl")) || sky_asBool(sky_asBool(sky_equal(name, "parseFunParams")) || sky_asBool(sky_asBool(sky_equal(name, "parseDeclaration")) || sky_asBool(sky_asBool(sky_equal(name, "parseDeclarations")) || sky_asBool(sky_asBool(sky_equal(name, "parseModule")) || sky_asBool(sky_asBool(sky_equal(name, "isStartOfPrimary")) || sky_asBool(sky_asBool(sky_equal(name, "getOperatorInfo")) || sky_asBool(sky_asBool(sky_equal(name, "parseExpr")) || sky_asBool(sky_asBool(sky_equal(name, "parseExprLoop")) || sky_asBool(sky_asBool(sky_equal(name, "parseApplication")) || sky_asBool(sky_asBool(sky_equal(name, "parseApplicationArgs")) || sky_asBool(sky_asBool(sky_equal(name, "parsePrimary")) || sky_asBool(sky_asBool(sky_equal(name, "parseCaseExpr")) || sky_asBool(sky_asBool(sky_equal(name, "parseCaseBranches")) || sky_asBool(sky_asBool(sky_equal(name, "parseIfExpr")) || sky_asBool(sky_asBool(sky_equal(name, "parseLetExpr")) || sky_asBool(sky_asBool(sky_equal(name, "parseLetBindings")) || sky_asBool(sky_asBool(sky_equal(name, "parseLambdaExpr")) || sky_asBool(sky_asBool(sky_equal(name, "parseLambdaParams")) || sky_asBool(sky_asBool(sky_equal(name, "parseRecordOrUpdate")) || sky_asBool(sky_asBool(sky_equal(name, "parseRecordFields")) || sky_asBool(sky_asBool(sky_equal(name, "parseParenOrTuple")) || sky_asBool(sky_asBool(sky_equal(name, "parseTupleRest")) || sky_asBool(sky_asBool(sky_equal(name, "parseListExpr")) || sky_asBool(sky_asBool(sky_equal(name, "parseListItems")) || sky_asBool(sky_asBool(sky_equal(name, "parseQualifiedOrConstructor")) || sky_asBool(sky_asBool(sky_equal(name, "parseFieldAccess")) || sky_asBool(sky_asBool(sky_equal(name, "parsePatternExpr")) || sky_asBool(sky_asBool(sky_equal(name, "parsePrimaryPattern")) || sky_asBool(sky_asBool(sky_equal(name, "parsePatternArgs")) || sky_asBool(sky_asBool(sky_equal(name, "parseTuplePatternRest")) || sky_asBool(sky_asBool(sky_equal(name, "parsePatternList")) || sky_asBool(sky_asBool(sky_equal(name, "parseVariantFields")) || sky_asBool(sky_equal(name, "parseTypeArgs"))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))
}

func Compiler_Pipeline_DeduplicateDecls(decls any) any {
	return Compiler_Pipeline_DeduplicateDeclsLoop(decls, sky_dictEmpty, []any{})
}

func Compiler_Pipeline_DeduplicateDeclsLoop(decls any, seen any, acc any) any {
	return func() any { return func() any { __subject := decls; if len(sky_asList(__subject)) == 0 { return sky_listReverse(acc) };  if len(sky_asList(__subject)) > 0 { decl := sky_asList(__subject)[0]; _ = decl; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { name := Compiler_Pipeline_GetDeclName(decl); _ = name; return func() any { if sky_asBool(sky_stringIsEmpty(name)) { return Compiler_Pipeline_DeduplicateDeclsLoop(rest, seen, append([]any{decl}, sky_asList(acc)...)) }; return func() any { return func() any { __subject := sky_call(sky_dictGet(name), seen); if sky_asSkyMaybe(__subject).SkyName == "Just" { return Compiler_Pipeline_DeduplicateDeclsLoop(rest, seen, acc) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return Compiler_Pipeline_DeduplicateDeclsLoop(rest, sky_call(sky_call(sky_dictInsert(name), "1"), seen), append([]any{decl}, sky_asList(acc)...)) };  return nil }() }() }() }() };  return nil }() }()
}

func Compiler_Pipeline_GetDeclName(decl any) any {
	return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "GoDeclFunc" { funcDecl := sky_asMap(__subject)["V0"]; _ = funcDecl; return sky_asMap(funcDecl)["name"] };  if sky_asMap(__subject)["SkyName"] == "GoDeclVar" { name := sky_asMap(__subject)["V0"]; _ = name; return name };  if sky_asMap(__subject)["SkyName"] == "GoDeclRaw" { code := sky_asMap(__subject)["V0"]; _ = code; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("var "), code)) { return Compiler_Pipeline_ExtractVarName(code) }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("func "), code)) { return Compiler_Pipeline_ExtractFuncName(code) }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("type "), code)) { return Compiler_Pipeline_ExtractTypeName(code) }; return "" }() }() }() };  return nil }() }()
}

func Compiler_Pipeline_ExtractVarName(code any) any {
	return func() any { afterVar := sky_call(sky_call(sky_stringSlice(4), sky_stringLength(code)), code); _ = afterVar; spaceIdx := Compiler_Pipeline_FindCharIdx(string(' '), afterVar, 0); _ = spaceIdx; return func() any { if sky_asBool(sky_asInt(spaceIdx) > sky_asInt(0)) { return sky_call(sky_call(sky_stringSlice(0), spaceIdx), afterVar) }; return "" }() }()
}

func Compiler_Pipeline_ExtractFuncName(code any) any {
	return func() any { afterFunc := sky_call(sky_call(sky_stringSlice(5), sky_stringLength(code)), code); _ = afterFunc; parenIdx := Compiler_Pipeline_FindCharIdx(string('('), afterFunc, 0); _ = parenIdx; return func() any { if sky_asBool(sky_asInt(parenIdx) > sky_asInt(0)) { return sky_call(sky_call(sky_stringSlice(0), parenIdx), afterFunc) }; return "" }() }()
}

func Compiler_Pipeline_ExtractTypeName(code any) any {
	return func() any { afterType := sky_call(sky_call(sky_stringSlice(5), sky_stringLength(code)), code); _ = afterType; spaceIdx := Compiler_Pipeline_FindCharIdx(string(' '), afterType, 0); _ = spaceIdx; return func() any { if sky_asBool(sky_asInt(spaceIdx) > sky_asInt(0)) { return sky_call(sky_call(sky_stringSlice(0), spaceIdx), afterType) }; return "" }() }()
}

func Compiler_Pipeline_GenerateImportAliases(imports any, allModules any) any {
	return sky_call(sky_listConcatMap(func(__pa0 any) any { return Compiler_Pipeline_GenerateAliasesForImport(allModules, __pa0) }), imports)
}

func Compiler_Pipeline_GenerateAliasesForImport(allModules any, imp any) any {
	return func() any { modName := sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]); _ = modName; return func() any { if sky_asBool(Compiler_Resolver_IsStdlib(modName)) { return []any{} }; return Compiler_Pipeline_GenerateAliasesFromModule(modName, allModules) }() }()
}

func Compiler_Pipeline_GenerateAliasesFromModule(modName any, allModules any) any {
	return func() any { return func() any { __subject := Compiler_Pipeline_FindModule(modName, allModules); if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return []any{} };  if sky_asSkyMaybe(__subject).SkyName == "Just" { mod := sky_asSkyMaybe(__subject).JustValue; _ = mod; return func() any { prefix := sky_call(sky_call(sky_stringReplace("."), "_"), modName); _ = prefix; declNames := Compiler_Pipeline_ExtractExportedNames(mod); _ = declNames; return sky_call(sky_listFilterMap(func(name any) any { return func() any { if sky_asBool(sky_stringIsEmpty(name)) { return SkyNothing() }; return SkyJust(GoDeclRaw(sky_asString("var ") + sky_asString(sky_asString(name) + sky_asString(sky_asString(" = ") + sky_asString(sky_asString(prefix) + sky_asString(sky_asString("_") + sky_asString(Compiler_Pipeline_CapitalizeFirst(name)))))))) }() }), declNames) }() };  return nil }() }()
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

func Compiler_Pipeline_FindModule(modName any, modules any) any {
	return func() any { return func() any { __subject := modules; if len(sky_asList(__subject)) == 0 { return SkyNothing() };  if len(sky_asList(__subject)) > 0 { pair := sky_asList(__subject)[0]; _ = pair; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { if sky_asBool(sky_equal(sky_fst(pair), modName)) { return SkyJust(sky_snd(pair)) }; return Compiler_Pipeline_FindModule(modName, rest) }() };  return nil }() }()
}

func Compiler_Pipeline_ExtractExportedNames(mod any) any {
	return sky_call(sky_listConcatMap(Compiler_Pipeline_ExtractDeclNames), sky_asMap(mod)["declarations"])
}

func Compiler_Pipeline_ExtractDeclNames(decl any) any {
	return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "FunDecl" { name := sky_asMap(__subject)["V0"]; _ = name; return []any{name} };  if sky_asMap(__subject)["SkyName"] == "TypeDecl" { name := sky_asMap(__subject)["V0"]; _ = name; variants := sky_asMap(__subject)["V2"]; _ = variants; return append([]any{name}, sky_asList(sky_call(sky_listMap(func(v any) any { return sky_asMap(v)["name"] }), variants))...) };  if sky_asMap(__subject)["SkyName"] == "TypeAliasDecl" { name := sky_asMap(__subject)["V0"]; _ = name; return []any{name} };  if sky_asMap(__subject)["SkyName"] == "TypeAnnotDecl" { return []any{} };  if sky_asMap(__subject)["SkyName"] == "ForeignImportDecl" { name := sky_asMap(__subject)["V0"]; _ = name; return []any{name} };  return nil }() }()
}

func Compiler_Pipeline_IsModuleLoaded(modName any, loaded any) any {
	return sky_call(sky_call(sky_listFoldl(func(pair any) any { return func(acc any) any { return sky_asBool(acc) || sky_asBool(sky_equal(sky_fst(pair), modName)) } }), false), loaded)
}

func Compiler_Pipeline_AddImportAlias(imp any, acc any) any {
	return func() any { modName := sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]); _ = modName; return func() any { if sky_asBool(Compiler_Resolver_IsStdlib(modName)) { return acc }; return sky_call(sky_call(sky_dictInsert(Compiler_Pipeline_ImportAlias(imp)), modName), acc) }() }()
}

func Compiler_Pipeline_ImportAlias(imp any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(sky_asMap(imp)["alias_"])) { return func() any { return func() any { __subject := sky_listReverse(sky_asMap(imp)["moduleName"]); if len(sky_asList(__subject)) > 0 { last := sky_asList(__subject)[0]; _ = last; return last };  if len(sky_asList(__subject)) == 0 { return "" };  return nil }() }() }; return sky_asMap(imp)["alias_"] }()
}

func Compiler_Pipeline_CompileDependencyModule(stdlibEnv any, allModules any, pair any) any {
	return func() any { modName := sky_fst(pair); _ = modName; mod := sky_snd(pair); _ = mod; prefix := sky_call(sky_call(sky_stringReplace("."), "_"), modName); _ = prefix; checkResult := Compiler_Checker_CheckModule(mod, SkyJust(stdlibEnv)); _ = checkResult; registry := func() any { return func() any { __subject := checkResult; if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return sky_asMap(result)["registry"] };  if sky_asSkyResult(__subject).SkyName == "Err" { return Compiler_Adt_EmptyRegistry() };  return nil }() }(); _ = registry; depBaseCtx := Compiler_Lower_EmptyCtx(); _ = depBaseCtx; depAliasMap := Compiler_Pipeline_BuildAliasMap(sky_asMap(mod)["imports"]); _ = depAliasMap; localFns := Compiler_Lower_CollectLocalFunctions(sky_asMap(mod)["declarations"]); _ = localFns; totalDecls := sky_listLength(sky_asMap(mod)["declarations"]); _ = totalDecls; sky_println(sky_asString("   [DEBUG] ") + sky_asString(sky_asString(modName) + sky_asString(sky_asString(": ") + sky_asString(sky_asString(sky_stringFromInt(sky_listLength(localFns))) + sky_asString(sky_asString(" functions, ") + sky_asString(sky_asString(sky_stringFromInt(totalDecls)) + sky_asString(sky_asString(" total decls: ") + sky_asString(sky_call(sky_stringJoin(","), localFns))))))))); ctx := sky_recordUpdate(depBaseCtx, map[string]any{"registry": registry, "localFunctions": localFns, "importedConstructors": Compiler_Lower_BuildConstructorMap(registry), "modulePrefix": prefix, "importAliases": depAliasMap, "localFunctionArity": Compiler_Lower_CollectLocalFunctionArities(sky_asMap(mod)["declarations"])}); _ = ctx; goDecls := Compiler_Lower_LowerDeclarations(ctx, sky_asMap(mod)["declarations"]); _ = goDecls; ctorDecls := Compiler_Lower_GenerateConstructorDecls(registry, sky_asMap(mod)["declarations"]); _ = ctorDecls; filtered := sky_call(sky_listFilter(Compiler_Pipeline_IsExportableDecl), sky_call(sky_listAppend(ctorDecls), goDecls)); _ = filtered; prefixed := sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Pipeline_PrefixDecl(prefix, __pa0) }), filtered); _ = prefixed; aliases := Compiler_Pipeline_GenerateConstructorAliases(prefix, prefixed); _ = aliases; return Compiler_Pipeline_DeduplicateDecls(sky_call(sky_listAppend(aliases), prefixed)) }()
}

func Compiler_Pipeline_GenerateConstructorAliases(prefix any, decls any) any {
	return sky_call(sky_listFilterMap(func(__pa0 any) any { return Compiler_Pipeline_MakeConstructorAlias(prefix, __pa0) }), decls)
}

func Compiler_Pipeline_MakeConstructorAlias(prefix any, decl any) any {
	return func() any { fullName := Compiler_Pipeline_GetDeclName(decl); _ = fullName; prefixLen := sky_asInt(sky_stringLength(prefix)) + sky_asInt(1); _ = prefixLen; unprefixed := func() any { if sky_asBool(sky_call(sky_stringStartsWith(sky_asString(prefix) + sky_asString("_")), fullName)) { return sky_call(sky_call(sky_stringSlice(prefixLen), sky_stringLength(fullName)), fullName) }; return "" }(); _ = unprefixed; return func() any { if sky_asBool(sky_asBool(sky_stringIsEmpty(unprefixed)) || sky_asBool(sky_equal(unprefixed, "Main"))) { return SkyNothing() }; return func() any { firstChar := sky_call(sky_call(sky_stringSlice(0), 1), unprefixed); _ = firstChar; aliasName := func() any { if sky_asBool(Compiler_Pipeline_IsSharedValue(sky_asString(sky_stringToLower(sky_call(sky_call(sky_stringSlice(0), 1), unprefixed))) + sky_asString(sky_call(sky_call(sky_stringSlice(1), sky_stringLength(unprefixed)), unprefixed)))) { return sky_asString(sky_stringToLower(sky_call(sky_call(sky_stringSlice(0), 1), unprefixed))) + sky_asString(sky_call(sky_call(sky_stringSlice(1), sky_stringLength(unprefixed)), unprefixed)) }; return unprefixed }(); _ = aliasName; return func() any { if sky_asBool(sky_asBool(sky_equal(firstChar, sky_stringToUpper(firstChar))) || sky_asBool(Compiler_Pipeline_IsSharedValue(aliasName))) { return SkyJust(GoDeclRaw(sky_asString("var ") + sky_asString(sky_asString(aliasName) + sky_asString(sky_asString(" = ") + sky_asString(fullName))))) }; return SkyNothing() }() }() }() }()
}

func Compiler_Pipeline_GenerateOriginalAliases(prefix any, decls any) any {
	return sky_call(sky_listFilterMap(func(__pa0 any) any { return Compiler_Pipeline_MakeOriginalAlias(prefix, __pa0) }), decls)
}

func Compiler_Pipeline_MakeOriginalAlias(prefix any, decl any) any {
	return func() any { name := Compiler_Pipeline_GetDeclName(decl); _ = name; return func() any { if sky_asBool(sky_asBool(sky_stringIsEmpty(name)) || sky_asBool(sky_asBool(sky_equal(name, "_")) || sky_asBool(sky_asBool(sky_equal(name, "main")) || sky_asBool(Compiler_Pipeline_IsCommonName(name))))) { return SkyNothing() }; return func() any { prefixedName := sky_asString(prefix) + sky_asString(sky_asString("_") + sky_asString(Compiler_Pipeline_CapitalizeFirst(name))); _ = prefixedName; return SkyJust(GoDeclRaw(sky_asString("var ") + sky_asString(sky_asString(name) + sky_asString(sky_asString(" = ") + sky_asString(prefixedName))))) }() }() }()
}

func Compiler_Pipeline_IsExportableDecl(decl any) any {
	return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "GoDeclFunc" { funcDecl := sky_asMap(__subject)["V0"]; _ = funcDecl; return sky_asBool(!sky_equal(sky_asMap(funcDecl)["name"], "_")) && sky_asBool(!sky_equal(sky_asMap(funcDecl)["name"], "main")) };  if sky_asMap(__subject)["SkyName"] == "GoDeclVar" { name := sky_asMap(__subject)["V0"]; _ = name; return !sky_equal(name, "_") };  if sky_asMap(__subject)["SkyName"] == "GoDeclRaw" { return true };  return nil }() }()
}

func Compiler_Pipeline_DirOfPath(path any) any {
	return func() any { lastSlash := Compiler_Pipeline_FindLastSlash(path, sky_asInt(sky_stringLength(path)) - sky_asInt(1)); _ = lastSlash; return func() any { if sky_asBool(sky_asInt(lastSlash) > sky_asInt(0)) { return sky_call(sky_call(sky_stringSlice(0), lastSlash), path) }; return "." }() }()
}

func Compiler_Pipeline_NeedsStdlibWrapper(modName any) any {
	return sky_asBool(sky_call(sky_stringStartsWith("Sky.Core.Json"), modName)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Std.Html"), modName)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Std.Css"), modName)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Std.Live"), modName)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Std.Cmd"), modName)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Std.Sub"), modName)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Std.Task"), modName)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Std.Time"), modName)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Std.Program"), modName)) || sky_asBool(sky_asBool(sky_equal(modName, "Sky.Core.Result")) || sky_asBool(sky_equal(modName, "Sky.Core.Maybe")))))))))))
}

func Compiler_Pipeline_BuildStdlibGoImports(imports any) any {
	return func() any { stdImports := sky_call(sky_listFilterMap(Compiler_Pipeline_ImportToGoImport), imports); _ = stdImports; return append([]any{map[string]any{"path": "sky-out/sky_wrappers", "alias_": "sky_wrappers"}}, sky_asList(stdImports)...) }()
}

func Compiler_Pipeline_ImportToGoImport(imp any) any {
	return func() any { modName := sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]); _ = modName; return func() any { if sky_asBool(Compiler_Resolver_IsStdlib(modName)) { return SkyJust(map[string]any{"path": sky_asString("sky-out/") + sky_asString(sky_call(sky_stringJoin("/"), sky_asMap(imp)["moduleName"])), "alias_": sky_asString("sky_") + sky_asString(sky_stringToLower(sky_call(sky_call(sky_stringReplace("."), "_"), modName)))}) }; return SkyNothing() }() }()
}

func Compiler_Pipeline_CopyStdlibGo(outDir any) any {
	return func() any { cpResult := sky_call(sky_processRun("cp"), []any{"-r", "sky-compiler/stdlib-go/Sky", sky_asString(outDir) + sky_asString("/")}); _ = cpResult; cpResult2 := sky_call(sky_processRun("cp"), []any{"-r", "sky-compiler/stdlib-go/Std", sky_asString(outDir) + sky_asString("/")}); _ = cpResult2; cpResult3 := sky_call(sky_processRun("cp"), []any{"-r", "sky-compiler/stdlib-go/sky_wrappers", sky_asString(outDir) + sky_asString("/")}); _ = cpResult3; return SkyOk(struct{}{}) }()
}

func Compiler_Pipeline_CapitalizeFirst(s any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(s)) { return "" }; return sky_asString(sky_stringToUpper(sky_call(sky_call(sky_stringSlice(0), 1), s))) + sky_asString(sky_call(sky_call(sky_stringSlice(1), sky_stringLength(s)), s)) }()
}

func Compiler_Pipeline_PrefixDecl(prefix any, decl any) any {
	return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "GoDeclFunc" { funcDecl := sky_asMap(__subject)["V0"]; _ = funcDecl; return func() any { if sky_asBool(sky_equal(sky_asMap(funcDecl)["name"], "main")) { return decl }; return GoDeclFunc(sky_recordUpdate(funcDecl, map[string]any{"name": sky_asString(prefix) + sky_asString(sky_asString("_") + sky_asString(Compiler_Pipeline_CapitalizeFirst(sky_asMap(funcDecl)["name"])))})) }() };  if sky_asMap(__subject)["SkyName"] == "GoDeclVar" { name := sky_asMap(__subject)["V0"]; _ = name; expr := sky_asMap(__subject)["V1"]; _ = expr; return func() any { if sky_asBool(sky_asBool(sky_equal(name, "_")) || sky_asBool(sky_call(sky_stringStartsWith("var _ ="), name))) { return decl }; return GoDeclVar(sky_asString(prefix) + sky_asString(sky_asString("_") + sky_asString(Compiler_Pipeline_CapitalizeFirst(name))), expr) }() };  if sky_asMap(__subject)["SkyName"] == "GoDeclRaw" { code := sky_asMap(__subject)["V0"]; _ = code; return decl };  return nil }() }()
}

func Compiler_Pipeline_CompileSource(filePath any, outDir any, source any) any {
	return func() any { sky_println(sky_asString("── Lexing ") + sky_asString(filePath)); lexResult := Compiler_Lexer_Lex(source); _ = lexResult; sky_println(sky_asString("   ") + sky_asString(sky_asString(sky_stringFromInt(sky_listLength(sky_asMap(lexResult)["tokens"]))) + sky_asString(" tokens"))); return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if sky_asSkyResult(__subject).SkyName == "Err" { parseErr := sky_asSkyResult(__subject).ErrValue; _ = parseErr; return SkyErr(sky_asString("Parse error: ") + sky_asString(parseErr)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { mod := sky_asSkyResult(__subject).OkValue; _ = mod; return func() any { sky_println("── Parsing"); sky_println(sky_asString("   Module: ") + sky_asString(sky_call(sky_stringJoin("."), sky_asMap(mod)["name"]))); sky_println(sky_asString("   ") + sky_asString(sky_asString(sky_stringFromInt(sky_listLength(sky_asMap(mod)["declarations"]))) + sky_asString(sky_asString(" declarations, ") + sky_asString(sky_asString(sky_stringFromInt(sky_listLength(sky_asMap(mod)["imports"]))) + sky_asString(" imports"))))); return Compiler_Pipeline_CompileModule(filePath, outDir, mod) }() };  return nil }() }() }()
}

func Compiler_Pipeline_CompileModule(filePath any, outDir any, mod any) any {
	return func() any { counter := sky_refNew(100); _ = counter; sky_println(sky_asString("── Type Checking (src: ") + sky_asString(sky_asString(Compiler_Pipeline_InferSrcRoot(filePath, sky_asMap(mod)["name"])) + sky_asString(")"))); stdlibEnv := Compiler_Resolver_BuildStdlibEnv(); _ = stdlibEnv; checkResult := Compiler_Checker_CheckModule(mod, SkyJust(stdlibEnv)); _ = checkResult; return func() any { return func() any { __subject := checkResult; if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Type error: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return func() any { Compiler_Pipeline_PrintDiagnostics(sky_asMap(result)["diagnostics"]); Compiler_Pipeline_PrintTypedDecls(sky_asMap(result)["declarations"]); sky_println("── Lowering to Go IR"); goPackage := Compiler_Lower_LowerModule(sky_asMap(result)["registry"], mod); _ = goPackage; sky_println(sky_asString("   ") + sky_asString(sky_asString(sky_stringFromInt(sky_listLength(sky_asMap(goPackage)["declarations"]))) + sky_asString(" Go declarations"))); sky_println("── Emitting Go"); goCode := Compiler_Emit_EmitPackage(goPackage); _ = goCode; outPath := sky_asString(outDir) + sky_asString("/main.go"); _ = outPath; sky_fileMkdirAll(outDir); sky_call(sky_fileWrite(outPath), goCode); sky_println(sky_asString("   Wrote ") + sky_asString(outPath)); sky_println(""); sky_println("✓ Compilation successful"); return SkyOk(goCode) }() };  return nil }() }() }()
}

func Compiler_Pipeline_CompileProject(entryPath any, outDir any) any {
	return func() any { srcRoot := Compiler_Pipeline_InferSrcRootFromEntry(entryPath); _ = srcRoot; return func() any { return func() any { __subject := Compiler_Resolver_ResolveProject(entryPath, srcRoot); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { graph := sky_asSkyResult(__subject).OkValue; _ = graph; return func() any { return func() any { __subject := Compiler_Resolver_CheckAllModules(graph); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { checkedGraph := sky_asSkyResult(__subject).OkValue; _ = checkedGraph; return func() any { return func() any { __subject := sky_listReverse(sky_asMap(checkedGraph)["modules"]); if len(sky_asList(__subject)) == 0 { return SkyErr("No modules to compile") };  if len(sky_asList(__subject)) > 0 { entryMod := sky_asList(__subject)[0]; _ = entryMod; return func() any { registry := func() any { return func() any { __subject := sky_asMap(entryMod)["checkResult"]; if sky_asSkyMaybe(__subject).SkyName == "Just" { r := sky_asSkyMaybe(__subject).JustValue; _ = r; return sky_asMap(r)["registry"] };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return Compiler_Adt_EmptyRegistry() };  return nil }() }(); _ = registry; goPackage := Compiler_Lower_LowerModule(registry, sky_asMap(entryMod)["ast"]); _ = goPackage; goCode := Compiler_Emit_EmitPackage(goPackage); _ = goCode; outPath := sky_asString(outDir) + sky_asString("/main.go"); _ = outPath; sky_fileMkdirAll(outDir); sky_call(sky_fileWrite(outPath), goCode); return SkyOk(goCode) }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }()
}

func Compiler_Pipeline_InferSrcRoot(filePath any, moduleName any) any {
	return func() any { modulePath := sky_asString(sky_call(sky_stringJoin("/"), moduleName)) + sky_asString(".sky"); _ = modulePath; modulePathLen := sky_stringLength(modulePath); _ = modulePathLen; filePathLen := sky_stringLength(filePath); _ = filePathLen; return func() any { if sky_asBool(sky_call(sky_stringEndsWith(modulePath), filePath)) { return func() any { rootLen := sky_asInt(sky_asInt(filePathLen) - sky_asInt(modulePathLen)) - sky_asInt(1); _ = rootLen; return func() any { if sky_asBool(sky_asInt(rootLen) > sky_asInt(0)) { return sky_call(sky_call(sky_stringSlice(0), rootLen), filePath) }; return "." }() }() }; return "src" }() }()
}

func Compiler_Pipeline_InferSrcRootFromEntry(entryPath any) any {
	return func() any { if sky_asBool(sky_call(sky_stringContains("/src/"), entryPath)) { return func() any { idx := Compiler_Pipeline_FindSubstring("/src/", entryPath); _ = idx; return sky_call(sky_call(sky_stringSlice(0), sky_asInt(idx) + sky_asInt(4)), entryPath) }() }; return "src" }()
}

func Compiler_Pipeline_FindSubstring(needle any, haystack any) any {
	return Compiler_Pipeline_FindSubstringAt(needle, haystack, 0)
}

func Compiler_Pipeline_FindSubstringAt(needle any, haystack any, idx any) any {
	return func() any { if sky_asBool(sky_asInt(idx) >= sky_asInt(sky_stringLength(haystack))) { return 0 }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith(needle), sky_call(sky_call(sky_stringSlice(idx), sky_stringLength(haystack)), haystack))) { return idx }; return Compiler_Pipeline_FindSubstringAt(needle, haystack, sky_asInt(idx) + sky_asInt(1)) }() }()
}

func Compiler_Pipeline_PrintDiagnostics(diags any) any {
	return func() any { return func() any { __subject := diags; if len(sky_asList(__subject)) == 0 { return struct{}{} };  if true { return func() any { sky_println(sky_asString("   ⚠ ") + sky_asString(sky_asString(sky_stringFromInt(sky_listLength(diags))) + sky_asString(" diagnostics:"))); sky_call(sky_listMap(func(d any) any { return sky_println(sky_asString("     ") + sky_asString(d)) }), diags); return struct{}{} }() };  return nil }() }()
}

func Compiler_Pipeline_PrintTypedDecls(decls any) any {
	return func() any { sky_call(sky_listMap(func(d any) any { return sky_println(sky_asString("   ") + sky_asString(sky_asString(sky_asMap(d)["name"]) + sky_asString(sky_asString(" : ") + sky_asString(sky_asMap(d)["prettyType"])))) }), decls); return struct{}{} }()
}

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

var emptySpan = Compiler_Token_EmptySpan

var isKeyword = Compiler_Token_IsKeyword

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

var parseExpr = Compiler_ParserExpr_ParseExpr

var parseExprLoop = Compiler_ParserExpr_ParseExprLoop

var getOperatorInfo = Compiler_ParserExpr_GetOperatorInfo

var parseApplication = Compiler_ParserExpr_ParseApplication

var parseApplicationArgs = Compiler_ParserExpr_ParseApplicationArgs

var isStartOfPrimary = Compiler_ParserExpr_IsStartOfPrimary

var parsePrimary = Compiler_ParserExpr_ParsePrimary

var parseCaseExpr = Compiler_ParserExpr_ParseCaseExpr

var parseCaseBranches = Compiler_ParserExpr_ParseCaseBranches

var parseIfExpr = Compiler_ParserExpr_ParseIfExpr

var parseLetExpr = Compiler_ParserExpr_ParseLetExpr

var parseLetBindings = Compiler_ParserExpr_ParseLetBindings

var parseLambdaExpr = Compiler_ParserExpr_ParseLambdaExpr

var parseLambdaParams = Compiler_ParserExpr_ParseLambdaParams

var parseRecordOrUpdate = Compiler_ParserExpr_ParseRecordOrUpdate

var parseRecordFields = Compiler_ParserExpr_ParseRecordFields

var parseParenOrTuple = Compiler_ParserExpr_ParseParenOrTuple

var parseTupleRest = Compiler_ParserExpr_ParseTupleRest

var parseListExpr = Compiler_ParserExpr_ParseListExpr

var parseListItems = Compiler_ParserExpr_ParseListItems

var parseQualifiedOrConstructor = Compiler_ParserExpr_ParseQualifiedOrConstructor

var parseFieldAccess = Compiler_ParserExpr_ParseFieldAccess

func Compiler_ParserExpr_ParseExpr(minPrec any, state any) any {
	return func() any { return func() any { __subject := Compiler_ParserExpr_ParseApplication(state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return Compiler_ParserExpr_ParseExprLoop(minPrec, sky_fst(pair), sky_snd(pair)) };  return nil }() }()
}

func Compiler_ParserExpr_ParseExprLoop(minPrec any, left any, state any) any {
	return func() any { if sky_asBool(matchKind(TkEquals, state)) { return SkyOk([]any{}) }; return func() any { if sky_asBool(sky_asInt(peekColumn(state)) <= sky_asInt(1)) { return SkyOk([]any{}) }; return func() any { if sky_asBool(matchKind(TkPipe, state)) { return SkyOk([]any{}) }; return func() any { if sky_asBool(false) { return SkyOk([]any{}) }; return func() any { if sky_asBool(matchKind(TkOperator, state)) { return func() any { opToken := peek(state); _ = opToken; info := Compiler_ParserExpr_GetOperatorInfo(sky_asMap(opToken)["lexeme"]); _ = info; return func() any { return func() any { __subject := info; if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyOk([]any{}) };  if sky_asSkyMaybe(__subject).SkyName == "Just" { pair := sky_asSkyMaybe(__subject).JustValue; _ = pair; return func() any { prec := sky_fst(pair); _ = prec; assoc := sky_snd(pair); _ = assoc; return func() any { if sky_asBool(sky_asInt(prec) < sky_asInt(minPrec)) { return SkyOk([]any{}) }; return func() any { __tup_advTok_s1 := advance(state); advTok := sky_asTuple2(__tup_advTok_s1).V0; _ = advTok; s1 := sky_asTuple2(__tup_advTok_s1).V1; _ = s1; nextMin := func() any { if sky_asBool(sky_equal(assoc, "left")) { return sky_asInt(prec) + sky_asInt(1) }; return prec }(); _ = nextMin; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(nextMin, s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { right := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = right; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return Compiler_ParserExpr_ParseExprLoop(minPrec, BinaryExpr(sky_asMap(opToken)["lexeme"], left, right, emptySpan), s2) };  return nil }() }() }() }() }() };  return nil }() }() }() }; return SkyOk([]any{}) }() }() }() }() }()
}

func Compiler_ParserExpr_GetOperatorInfo(op any) any {
	return func() any { if sky_asBool(sky_equal(op, "||")) { return SkyJust([]any{}) }; return func() any { if sky_asBool(sky_equal(op, "&&")) { return SkyJust([]any{}) }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "==")) || sky_asBool(sky_asBool(sky_equal(op, "!=")) || sky_asBool(sky_asBool(sky_equal(op, "/=")) || sky_asBool(sky_asBool(sky_equal(op, "<")) || sky_asBool(sky_asBool(sky_equal(op, "<=")) || sky_asBool(sky_asBool(sky_equal(op, ">")) || sky_asBool(sky_equal(op, ">=")))))))) { return SkyJust([]any{}) }; return func() any { if sky_asBool(sky_equal(op, "++")) { return SkyJust([]any{}) }; return func() any { if sky_asBool(sky_equal(op, "::")) { return SkyJust([]any{}) }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "+")) || sky_asBool(sky_equal(op, "-"))) { return SkyJust([]any{}) }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "*")) || sky_asBool(sky_asBool(sky_equal(op, "/")) || sky_asBool(sky_asBool(sky_equal(op, "//")) || sky_asBool(sky_equal(op, "%"))))) { return SkyJust([]any{}) }; return func() any { if sky_asBool(sky_equal(op, "|>")) { return SkyJust([]any{}) }; return func() any { if sky_asBool(sky_equal(op, "<|")) { return SkyJust([]any{}) }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, ">>")) || sky_asBool(sky_equal(op, "<<"))) { return SkyJust([]any{}) }; return SkyNothing() }() }() }() }() }() }() }() }() }() }()
}

func Compiler_ParserExpr_ParseApplication(state any) any {
	return func() any { fnCol := peekColumn(state); _ = fnCol; return func() any { return func() any { __subject := Compiler_ParserExpr_ParsePrimary(state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { fn := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = fn; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return Compiler_ParserExpr_ParseApplicationArgs(fnCol, fn, s1) };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseApplicationArgs(fnCol any, fn any, state any) any {
	return func() any { if sky_asBool(Compiler_ParserExpr_IsStartOfPrimary(state)) { return func() any { if sky_asBool(sky_asInt(peekColumn(state)) <= sky_asInt(1)) { return SkyOk([]any{}) }; return func() any { if sky_asBool(sky_asInt(peekColumn(state)) < sky_asInt(fnCol)) { return SkyOk([]any{}) }; return func() any { if sky_asBool(sky_asBool(matchKind(TkIdentifier, state)) && sky_asBool(tokenKindEq(peekAt1Kind(state), TkEquals))) { return SkyOk([]any{}) }; return func() any { if sky_asBool(tokenKindEq(peekAt1Kind(state), TkArrow)) { return SkyOk([]any{}) }; return func() any { if sky_asBool(matchKind(TkPipe, state)) { return SkyOk([]any{}) }; return func() any { return func() any { __subject := Compiler_ParserExpr_ParsePrimary(state); if sky_asSkyResult(__subject).SkyName == "Err" { return SkyOk([]any{}) };  if sky_asSkyResult(__subject).SkyName == "Ok" { arg := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = arg; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return Compiler_ParserExpr_ParseApplicationArgs(fnCol, CallExpr(fn, []any{arg}, emptySpan), s1) };  return nil }() }() }() }() }() }() }() }; return SkyOk([]any{}) }()
}

func Compiler_ParserExpr_IsStartOfPrimary(state any) any {
	return sky_asBool(matchKind(TkIdentifier, state)) || sky_asBool(sky_asBool(matchKind(TkUpperIdentifier, state)) || sky_asBool(sky_asBool(matchKind(TkInteger, state)) || sky_asBool(sky_asBool(matchKind(TkFloat, state)) || sky_asBool(sky_asBool(matchKind(TkString, state)) || sky_asBool(sky_asBool(matchKind(TkChar, state)) || sky_asBool(sky_asBool(matchKind(TkLParen, state)) || sky_asBool(sky_asBool(matchKind(TkLBrace, state)) || sky_asBool(sky_asBool(matchKind(TkLBracket, state)) || sky_asBool(sky_asBool(matchKind(TkBackslash, state)) || sky_asBool(sky_asBool(matchKindLex(TkKeyword, "case", state)) || sky_asBool(sky_asBool(matchKindLex(TkKeyword, "if", state)) || sky_asBool(matchKindLex(TkKeyword, "let", state)))))))))))))
}

func Compiler_ParserExpr_ParsePrimary(state any) any {
	return func() any { if sky_asBool(matchKindLex(TkKeyword, "case", state)) { return Compiler_ParserExpr_ParseCaseExpr(state) }; return func() any { if sky_asBool(matchKindLex(TkKeyword, "if", state)) { return Compiler_ParserExpr_ParseIfExpr(state) }; return func() any { if sky_asBool(matchKindLex(TkKeyword, "let", state)) { return Compiler_ParserExpr_ParseLetExpr(state) }; return func() any { if sky_asBool(matchKind(TkBackslash, state)) { return Compiler_ParserExpr_ParseLambdaExpr(state) }; return func() any { if sky_asBool(sky_asBool(matchKind(TkOperator, state)) && sky_asBool(sky_equal(peekLexeme(state), "-"))) { return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := Compiler_ParserExpr_ParsePrimary(s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { inner := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = inner; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return SkyOk([]any{}) };  return nil }() }() }() }; return func() any { if sky_asBool(matchKind(TkLBrace, state)) { return Compiler_ParserExpr_ParseRecordOrUpdate(state) }; return func() any { if sky_asBool(matchKind(TkLParen, state)) { return Compiler_ParserExpr_ParseParenOrTuple(state) }; return func() any { if sky_asBool(matchKind(TkLBracket, state)) { return Compiler_ParserExpr_ParseListExpr(state) }; return func() any { if sky_asBool(matchKind(TkUpperIdentifier, state)) { return Compiler_ParserExpr_ParseQualifiedOrConstructor(state) }; return func() any { if sky_asBool(matchKind(TkIdentifier, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; return func() any { if sky_asBool(sky_asBool(matchKind(TkDot, s1)) && sky_asBool(matchKind(TkIdentifier, func() any { __tup_w_s := advance(s1); s := sky_asTuple2(__tup_w_s).V1; _ = s; return s }()))) { return Compiler_ParserExpr_ParseFieldAccess(tok, s1) }; return SkyOk([]any{}) }() }() }; return func() any { if sky_asBool(matchKind(TkInteger, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; return SkyOk([]any{}) }() }; return func() any { if sky_asBool(matchKind(TkFloat, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; return SkyOk([]any{}) }() }; return func() any { if sky_asBool(matchKind(TkString, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; value := sky_call(sky_call(sky_stringSlice(1), sky_asInt(sky_stringLength(sky_asMap(tok)["lexeme"])) - sky_asInt(1)), sky_asMap(tok)["lexeme"]); _ = value; return SkyOk([]any{}) }() }; return func() any { if sky_asBool(matchKind(TkChar, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; return SkyOk([]any{}) }() }; return func() any { if sky_asBool(matchKindLex(TkKeyword, "True", state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; return SkyOk([]any{}) }() }; return func() any { if sky_asBool(matchKindLex(TkKeyword, "False", state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; return SkyOk([]any{}) }() }; return SkyErr }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }()
}

func Compiler_ParserExpr_ParseCaseExpr(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { subject := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = subject; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := consumeLex(TkKeyword, "of", s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { __tup_branches_s4 := Compiler_ParserExpr_ParseCaseBranches(s3); branches := sky_asTuple2(__tup_branches_s4).V0; _ = branches; s4 := sky_asTuple2(__tup_branches_s4).V1; _ = s4; return SkyOk([]any{}) }() };  return nil }() }() };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseCaseBranches(state any) any {
	return func() any { if sky_asBool(matchKind(TkEOF, state)) { return []any{} }; return func() any { if sky_asBool(sky_asInt(peekColumn(state)) <= sky_asInt(1)) { return []any{} }; return func() any { s0 := func() any { if sky_asBool(matchKind(TkPipe, state)) { return func() any { __tup_w_s := advance(state); s := sky_asTuple2(__tup_w_s).V1; _ = s; return s }() }; return state }(); _ = s0; return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(s0); if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  if sky_asSkyResult(__subject).SkyName == "Ok" { pat := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = pat; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { return func() any { __subject := consume(TkArrow, s1); if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s2); if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  if sky_asSkyResult(__subject).SkyName == "Ok" { body := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = body; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { __tup_rest_s4 := Compiler_ParserExpr_ParseCaseBranches(s3); rest := sky_asTuple2(__tup_rest_s4).V0; _ = rest; s4 := sky_asTuple2(__tup_rest_s4).V1; _ = s4; return []any{} }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }() }() }()
}

func Compiler_ParserExpr_ParseIfExpr(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { cond := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = cond; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := consumeLex(TkKeyword, "then", s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s3); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { thenExpr := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = thenExpr; s4 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s4; return func() any { return func() any { __subject := consumeLex(TkKeyword, "else", s4); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s5 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s5; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s5); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { elseExpr := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = elseExpr; s6 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s6; return SkyOk([]any{}) };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseLetExpr(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; __tup_bindings_s2 := Compiler_ParserExpr_ParseLetBindings(s1); bindings := sky_asTuple2(__tup_bindings_s2).V0; _ = bindings; s2 := sky_asTuple2(__tup_bindings_s2).V1; _ = s2; return func() any { return func() any { __subject := consumeLex(TkKeyword, "in", s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s3); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { body := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = body; s4 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s4; return SkyOk([]any{}) };  return nil }() }() };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseLetBindings(state any) any {
	return func() any { if sky_asBool(sky_asBool(matchKindLex(TkKeyword, "in", state)) || sky_asBool(matchKind(TkEOF, state))) { return []any{} }; return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(state); if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  if sky_asSkyResult(__subject).SkyName == "Ok" { pat := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = pat; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { return func() any { __subject := consume(TkEquals, s1); if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s2); if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  if sky_asSkyResult(__subject).SkyName == "Ok" { value := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = value; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { __tup_rest_s4 := Compiler_ParserExpr_ParseLetBindings(s3); rest := sky_asTuple2(__tup_rest_s4).V0; _ = rest; s4 := sky_asTuple2(__tup_rest_s4).V1; _ = s4; return []any{} }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseLambdaExpr(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; __tup_params_s2 := Compiler_ParserExpr_ParseLambdaParams(s1); params := sky_asTuple2(__tup_params_s2).V0; _ = params; s2 := sky_asTuple2(__tup_params_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkArrow, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s3); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { body := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = body; s4 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s4; return SkyOk([]any{}) };  return nil }() }() };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseLambdaParams(state any) any {
	return func() any { if sky_asBool(matchKind(TkArrow, state)) { return []any{} }; return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(state); if sky_asSkyResult(__subject).SkyName == "Ok" { pat := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = pat; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { __tup_rest_s2 := Compiler_ParserExpr_ParseLambdaParams(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return []any{} }() };  if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseRecordOrUpdate(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; t1 := peekAt(1, s1); _ = t1; return func() any { if sky_asBool(sky_asBool(matchKind(TkIdentifier, s1)) && sky_asBool(tokenKindEq(sky_asMap(t1)["kind"], TkPipe))) { return func() any { __tup_base_s2 := advance(s1); base := sky_asTuple2(__tup_base_s2).V0; _ = base; s2 := sky_asTuple2(__tup_base_s2).V1; _ = s2; __tup_w_s3 := advance(s2); s3 := sky_asTuple2(__tup_w_s3).V1; _ = s3; __tup_fields_s4 := Compiler_ParserExpr_ParseRecordFields(s3); fields := sky_asTuple2(__tup_fields_s4).V0; _ = fields; s4 := sky_asTuple2(__tup_fields_s4).V1; _ = s4; return func() any { return func() any { __subject := consume(TkRBrace, s4); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s5 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s5; return SkyOk([]any{}) };  return nil }() }() }() }; return func() any { __tup_fields_s2 := Compiler_ParserExpr_ParseRecordFields(s1); fields := sky_asTuple2(__tup_fields_s2).V0; _ = fields; s2 := sky_asTuple2(__tup_fields_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkRBrace, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk([]any{}) };  return nil }() }() }() }() }()
}

func Compiler_ParserExpr_ParseRecordFields(state any) any {
	return func() any { if sky_asBool(matchKind(TkRBrace, state)) { return []any{} }; return func() any { return func() any { __subject := consume(TkIdentifier, state); if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  if sky_asSkyResult(__subject).SkyName == "Ok" { name := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = name; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { return func() any { __subject := consume(TkEquals, s1); if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s2); if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  if sky_asSkyResult(__subject).SkyName == "Ok" { value := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = value; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { s4 := func() any { if sky_asBool(matchKind(TkComma, s3)) { return func() any { __tup_w_s := advance(s3); s := sky_asTuple2(__tup_w_s).V1; _ = s; return s }() }; return s3 }(); _ = s4; __tup_rest_s5 := Compiler_ParserExpr_ParseRecordFields(s4); rest := sky_asTuple2(__tup_rest_s5).V0; _ = rest; s5 := sky_asTuple2(__tup_rest_s5).V1; _ = s5; return []any{} }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseParenOrTuple(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkRParen, s1)) { return func() any { __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; return SkyOk([]any{}) }() }; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { first := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = first; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { if sky_asBool(matchKind(TkComma, s2)) { return Compiler_ParserExpr_ParseTupleRest([]any{first}, s2) }; return func() any { return func() any { __subject := consume(TkRParen, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk([]any{}) };  return nil }() }() }() };  return nil }() }() }() }()
}

func Compiler_ParserExpr_ParseTupleRest(items any, state any) any {
	return func() any { if sky_asBool(matchKind(TkComma, state)) { return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { item := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = item; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return Compiler_ParserExpr_ParseTupleRest(sky_asString(items) + sky_asString([]any{item}), s2) };  return nil }() }() }() }; return func() any { return func() any { __subject := consume(TkRParen, state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return SkyOk([]any{}) };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseListExpr(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkRBracket, s1)) { return func() any { __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; return SkyOk([]any{}) }() }; return func() any { __tup_items_s2 := Compiler_ParserExpr_ParseListItems(s1); items := sky_asTuple2(__tup_items_s2).V0; _ = items; s2 := sky_asTuple2(__tup_items_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkRBracket, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk([]any{}) };  return nil }() }() }() }() }()
}

func Compiler_ParserExpr_ParseListItems(state any) any {
	return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, state); if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  if sky_asSkyResult(__subject).SkyName == "Ok" { item := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = item; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkComma, s1)) { return func() any { __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; __tup_rest_s3 := Compiler_ParserExpr_ParseListItems(s2); rest := sky_asTuple2(__tup_rest_s3).V0; _ = rest; s3 := sky_asTuple2(__tup_rest_s3).V1; _ = s3; return []any{} }() }; return []any{} }() };  return nil }() }()
}

func Compiler_ParserExpr_ParseQualifiedOrConstructor(state any) any {
	return func() any { __tup_id_s1 := advance(state); id := sky_asTuple2(__tup_id_s1).V0; _ = id; s1 := sky_asTuple2(__tup_id_s1).V1; _ = s1; __tup_parts_s2 := parseQualifiedParts([]any{sky_asMap(id)["lexeme"]}, s1); parts := sky_asTuple2(__tup_parts_s2).V0; _ = parts; s2 := sky_asTuple2(__tup_parts_s2).V1; _ = s2; return func() any { if sky_asBool(sky_asInt(sky_listLength(parts)) > sky_asInt(1)) { return SkyOk([]any{}) }; return SkyOk([]any{}) }() }()
}

func Compiler_ParserExpr_ParseFieldAccess(base any, state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; __tup_field_s2 := advance(s1); field := sky_asTuple2(__tup_field_s2).V0; _ = field; s2 := sky_asTuple2(__tup_field_s2).V1; _ = s2; return SkyOk([]any{}) }()
}

var parsePatternExpr = Compiler_ParserPattern_ParsePatternExpr

var parsePrimaryPattern = Compiler_ParserPattern_ParsePrimaryPattern

var T = Compiler_ParserPattern_T

var parseTuplePatternRest = Compiler_ParserPattern_ParseTuplePatternRest

var parsePatternList = Compiler_ParserPattern_ParsePatternList

func Compiler_ParserPattern_ParsePatternExpr(state any) any {
	return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePrimaryPattern(state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  return nil }() }(SkyOk, []any{})
}

func Compiler_ParserPattern_Advance(s1 any, _p any) any {
	return func(__pa0 any) any { return Compiler_ParserPattern_Advance(s2, __pa0) }
}

func Compiler_ParserPattern_ParsePrimaryPattern(state any) any {
	return func() any { if sky_asBool(matchKind(TkUpperIdentifier, state)) { return func() any { __tup_id_s1 := func(__pa0 any) any { return Compiler_ParserPattern_Advance(state, __pa0) }; id := sky_asTuple2(__tup_id_s1).V0; _ = id; s1 := sky_asTuple2(__tup_id_s1).V1; _ = s1; __tup_parts_s2 := parseQualifiedParts([]any{sky_asMap(id)["lexeme"]}, s1); parts := sky_asTuple2(__tup_parts_s2).V0; _ = parts; s2 := sky_asTuple2(__tup_parts_s2).V1; _ = s2; __tup_args_s3 := parsePatternArgs(s2); args := sky_asTuple2(__tup_args_s3).V0; _ = args; s3 := sky_asTuple2(__tup_args_s3).V1; _ = s3; return SkyOk([]any{}) }() }; return func() any { if sky_asBool(matchKind(TkIdentifier, state)) { return func() any { __tup_id_s1 := func(__pa0 any) any { return Compiler_ParserPattern_Advance(state, __pa0) }; id := sky_asTuple2(__tup_id_s1).V0; _ = id; s1 := sky_asTuple2(__tup_id_s1).V1; _ = s1; return func() any { if sky_asBool(sky_equal(sky_asMap(id)["lexeme"], "_")) { return SkyOk([]any{}) }; return SkyOk([]any{}) }() }() }; return func() any { if sky_asBool(matchKind(TkInteger, state)) { return func() any { __tup_tok_s1 := func(__pa0 any) any { return Compiler_ParserPattern_Advance(state, __pa0) }; tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; return func() any { return func() any { __subject := sky_stringToInt(sky_asMap(tok)["lexeme"]); if sky_asSkyMaybe(__subject).SkyName == "Just" { n := sky_asSkyMaybe(__subject).JustValue; _ = n; return SkyOk([]any{}) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyOk([]any{}) };  return nil }() }() }() }; return func() any { if sky_asBool(matchKind(TkString, state)) { return func() any { __tup_tok_s1 := func(__pa0 any) any { return Compiler_ParserPattern_Advance(state, __pa0) }; tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; return SkyOk([]any{}) }() }; return func() any { if sky_asBool(matchKind(TkLParen, state)) { return func() any { __tup_w_s1 := func(__pa0 any) any { return Compiler_ParserPattern_Advance(state, __pa0) }; s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkRParen, s1)) { return func() any { __tup_w_s2 := func(__pa0 any) any { return Compiler_ParserPattern_Advance(s1, __pa0) }; s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; return SkyOk([]any{}) }() }; return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { first := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = first; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { if sky_asBool(matchKind(TkComma, s2)) { return Compiler_ParserPattern_ParseTuplePatternRest([]any{first}, s2) }; return func() any { return func() any { __subject := consume(TkRParen, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk([]any{}) };  return nil }() }() }() };  return nil }() }() }() }() }; return func() any { if sky_asBool(matchKind(TkLBracket, state)) { return func() any { __tup_w_s1 := func(__pa0 any) any { return Compiler_ParserPattern_Advance(state, __pa0) }; s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkRBracket, s1)) { return func() any { __tup_w_s2 := func(__pa0 any) any { return Compiler_ParserPattern_Advance(s1, __pa0) }; s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; return SkyOk([]any{}) }() }; return func() any { __tup_items_s2 := Compiler_ParserPattern_ParsePatternList(s1); items := sky_asTuple2(__tup_items_s2).V0; _ = items; s2 := sky_asTuple2(__tup_items_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkRBracket, s2); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk([]any{}) };  return nil }() }() }() }() }() }; return SkyErr }() }() }() }() }() }()
}

func Compiler_ParserPattern_T() any {
	return peek(state)
}

func Compiler_ParserPattern_ParseTuplePatternRest(items any, state any) any {
	return func() any { if sky_asBool(matchKind(TkComma, state)) { return func() any { __tup_w_s1 := func(__pa0 any) any { return Compiler_ParserPattern_Advance(state, __pa0) }; s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(s1); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { item := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = item; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return Compiler_ParserPattern_ParseTuplePatternRest(sky_asString(items) + sky_asString([]any{item}), s2) };  return nil }() }() }() }; return func() any { return func() any { __subject := consume(TkRParen, state); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return SkyOk([]any{}) };  return nil }() }() }()
}

func Compiler_ParserPattern_ParsePatternList(state any) any {
	return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(state); if sky_asSkyResult(__subject).SkyName == "Err" { return []any{} };  if sky_asSkyResult(__subject).SkyName == "Ok" { item := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = item; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkComma, s1)) { return func() any { __tup_w_s2 := func(__pa0 any) any { return Compiler_ParserPattern_Advance(s1, __pa0) }; s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; __tup_rest_s3 := Compiler_ParserPattern_ParsePatternList(s2); rest := sky_asTuple2(__tup_rest_s3).V0; _ = rest; s3 := sky_asTuple2(__tup_rest_s3).V1; _ = s3; return []any{} }() }; return []any{} }() };  return nil }() }()
}

var EmitPackage = Compiler_Emit_EmitPackage

var EmitImports = Compiler_Emit_EmitImports

var EmitDecl = Compiler_Emit_EmitDecl

var EmitFuncDecl = Compiler_Emit_EmitFuncDecl

var EmitParam = Compiler_Emit_EmitParam

var EmitStmt = Compiler_Emit_EmitStmt

var EmitExpr = Compiler_Emit_EmitExpr

var EscapeGoString = Compiler_Emit_EscapeGoString

func Compiler_Emit_EmitPackage(pkg any) any {
	return func() any { header := sky_asString("package ") + sky_asString(sky_asString(sky_asMap(pkg)["name"]) + sky_asString("\n\n")); _ = header; imports := func() any { if sky_asBool(sky_listIsEmpty(sky_asMap(pkg)["imports"])) { return "" }; return sky_asString(Compiler_Emit_EmitImports(sky_asMap(pkg)["imports"])) + sky_asString("\n") }(); _ = imports; decls := sky_call(sky_stringJoin("\n\n"), sky_call(sky_listMap(Compiler_Emit_EmitDecl), sky_asMap(pkg)["declarations"])); _ = decls; return sky_asString(header) + sky_asString(sky_asString(imports) + sky_asString(sky_asString(decls) + sky_asString("\n"))) }()
}

func Compiler_Emit_EmitImports(imports any) any {
	return func() any { lines := sky_call(sky_listMap(func(imp any) any { return func() any { if sky_asBool(sky_equal(sky_asMap(imp)["alias_"], "")) { return sky_asString("\t\"") + sky_asString(sky_asString(sky_asMap(imp)["path"]) + sky_asString("\"")) }; return sky_asString("\t") + sky_asString(sky_asString(sky_asMap(imp)["alias_"]) + sky_asString(sky_asString(" \"") + sky_asString(sky_asString(sky_asMap(imp)["path"]) + sky_asString("\"")))) }() }), imports); _ = lines; return sky_asString("import (\n") + sky_asString(sky_asString(sky_call(sky_stringJoin("\n"), lines)) + sky_asString("\n)\n")) }()
}

func Compiler_Emit_EmitDecl(decl any) any {
	return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "GoDeclFunc" { fd := sky_asMap(__subject)["V0"]; _ = fd; return Compiler_Emit_EmitFuncDecl(fd) };  if sky_asMap(__subject)["SkyName"] == "GoDeclVar" { name := sky_asMap(__subject)["V0"]; _ = name; expr := sky_asMap(__subject)["V1"]; _ = expr; return sky_asString("var ") + sky_asString(sky_asString(name) + sky_asString(sky_asString(" = ") + sky_asString(Compiler_Emit_EmitExpr(expr)))) };  if sky_asMap(__subject)["SkyName"] == "GoDeclRaw" { code := sky_asMap(__subject)["V0"]; _ = code; return code };  return nil }() }()
}

func Compiler_Emit_EmitFuncDecl(fd any) any {
	return func() any { params := sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Emit_EmitParam), sky_asMap(fd)["params"])); _ = params; ret := func() any { if sky_asBool(sky_equal(sky_asMap(fd)["returnType"], "")) { return "" }; return sky_asString(" ") + sky_asString(sky_asMap(fd)["returnType"]) }(); _ = ret; body := sky_call(sky_stringJoin("\n"), sky_call(sky_listMap(func(s any) any { return sky_asString("\t") + sky_asString(Compiler_Emit_EmitStmt(s)) }), sky_asMap(fd)["body"])); _ = body; return sky_asString("func ") + sky_asString(sky_asString(sky_asMap(fd)["name"]) + sky_asString(sky_asString("(") + sky_asString(sky_asString(params) + sky_asString(sky_asString(")") + sky_asString(sky_asString(ret) + sky_asString(sky_asString(" {\n") + sky_asString(sky_asString(body) + sky_asString("\n}")))))))) }()
}

func Compiler_Emit_EmitParam(p any) any {
	return sky_asString(sky_asMap(p)["name"]) + sky_asString(sky_asString(" ") + sky_asString(sky_asMap(p)["type_"]))
}

func Compiler_Emit_EmitStmt(stmt any) any {
	return func() any { return func() any { __subject := stmt; if sky_asMap(__subject)["SkyName"] == "GoExprStmt" { expr := sky_asMap(__subject)["V0"]; _ = expr; return Compiler_Emit_EmitExpr(expr) };  if sky_asMap(__subject)["SkyName"] == "GoAssign" { name := sky_asMap(__subject)["V0"]; _ = name; expr := sky_asMap(__subject)["V1"]; _ = expr; return sky_asString(name) + sky_asString(sky_asString(" = ") + sky_asString(Compiler_Emit_EmitExpr(expr))) };  if sky_asMap(__subject)["SkyName"] == "GoShortDecl" { name := sky_asMap(__subject)["V0"]; _ = name; expr := sky_asMap(__subject)["V1"]; _ = expr; return sky_asString(name) + sky_asString(sky_asString(" := ") + sky_asString(Compiler_Emit_EmitExpr(expr))) };  if sky_asMap(__subject)["SkyName"] == "GoReturn" { expr := sky_asMap(__subject)["V0"]; _ = expr; return sky_asString("return ") + sky_asString(Compiler_Emit_EmitExpr(expr)) };  if sky_asMap(__subject)["SkyName"] == "GoReturnVoid" { return "return" };  if sky_asMap(__subject)["SkyName"] == "GoIf" { cond := sky_asMap(__subject)["V0"]; _ = cond; thenStmts := sky_asMap(__subject)["V1"]; _ = thenStmts; elseStmts := sky_asMap(__subject)["V2"]; _ = elseStmts; return func() any { thenBody := sky_call(sky_stringJoin("\n\t"), sky_call(sky_listMap(Compiler_Emit_EmitStmt), thenStmts)); _ = thenBody; elseBody := sky_call(sky_stringJoin("\n\t"), sky_call(sky_listMap(Compiler_Emit_EmitStmt), elseStmts)); _ = elseBody; return func() any { if sky_asBool(sky_listIsEmpty(elseStmts)) { return sky_asString("if ") + sky_asString(sky_asString(Compiler_Emit_EmitExpr(cond)) + sky_asString(sky_asString(" {\n\t") + sky_asString(sky_asString(thenBody) + sky_asString("\n}")))) }; return sky_asString("if ") + sky_asString(sky_asString(Compiler_Emit_EmitExpr(cond)) + sky_asString(sky_asString(" {\n\t") + sky_asString(sky_asString(thenBody) + sky_asString(sky_asString("\n} else {\n\t") + sky_asString(sky_asString(elseBody) + sky_asString("\n}")))))) }() }() };  if sky_asMap(__subject)["SkyName"] == "GoBlock" { stmts := sky_asMap(__subject)["V0"]; _ = stmts; return sky_call(sky_stringJoin("\n\t"), sky_call(sky_listMap(Compiler_Emit_EmitStmt), stmts)) };  return nil }() }()
}

func Compiler_Emit_EmitExpr(expr any) any {
	return func() any { return func() any { __subject := expr; if sky_asMap(__subject)["SkyName"] == "GoIdent" { name := sky_asMap(__subject)["V0"]; _ = name; return name };  if sky_asMap(__subject)["SkyName"] == "GoBasicLit" { value := sky_asMap(__subject)["V0"]; _ = value; return value };  if sky_asMap(__subject)["SkyName"] == "GoStringLit" { value := sky_asMap(__subject)["V0"]; _ = value; return sky_asString("\"") + sky_asString(sky_asString(Compiler_Emit_EscapeGoString(value)) + sky_asString("\"")) };  if sky_asMap(__subject)["SkyName"] == "GoCallExpr" { fn := sky_asMap(__subject)["V0"]; _ = fn; args := sky_asMap(__subject)["V1"]; _ = args; return sky_asString(Compiler_Emit_EmitExpr(fn)) + sky_asString(sky_asString("(") + sky_asString(sky_asString(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Emit_EmitExpr), args))) + sky_asString(")"))) };  if sky_asMap(__subject)["SkyName"] == "GoSelectorExpr" { target := sky_asMap(__subject)["V0"]; _ = target; sel := sky_asMap(__subject)["V1"]; _ = sel; return sky_asString(Compiler_Emit_EmitExpr(target)) + sky_asString(sky_asString(".") + sky_asString(sel)) };  if sky_asMap(__subject)["SkyName"] == "GoSliceLit" { elems := sky_asMap(__subject)["V0"]; _ = elems; return sky_asString("[]any{") + sky_asString(sky_asString(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Emit_EmitExpr), elems))) + sky_asString("}")) };  if sky_asMap(__subject)["SkyName"] == "GoMapLit" { entries := sky_asMap(__subject)["V0"]; _ = entries; return func() any { pairs := sky_call(sky_listMap(func(_p any) any { return sky_asString(Compiler_Emit_EmitExpr(k)) + sky_asString(sky_asString(": ") + sky_asString(Compiler_Emit_EmitExpr(v))) }), entries); _ = pairs; return sky_asString("map[string]any{") + sky_asString(sky_asString(sky_call(sky_stringJoin(", "), pairs)) + sky_asString("}")) }() };  if sky_asMap(__subject)["SkyName"] == "GoFuncLit" { params := sky_asMap(__subject)["V0"]; _ = params; body := sky_asMap(__subject)["V1"]; _ = body; return func() any { ps := sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Emit_EmitParam), params)); _ = ps; return sky_asString("func(") + sky_asString(sky_asString(ps) + sky_asString(sky_asString(") any { return ") + sky_asString(sky_asString(Compiler_Emit_EmitExpr(body)) + sky_asString(" }")))) }() };  if sky_asMap(__subject)["SkyName"] == "GoRawExpr" { code := sky_asMap(__subject)["V0"]; _ = code; return code };  if sky_asMap(__subject)["SkyName"] == "GoCompositeLit" { typeName := sky_asMap(__subject)["V0"]; _ = typeName; fields := sky_asMap(__subject)["V1"]; _ = fields; return func() any { fs := sky_call(sky_listMap(func(_p any) any { return sky_asString(k) + sky_asString(sky_asString(": ") + sky_asString(Compiler_Emit_EmitExpr(v))) }), fields); _ = fs; return sky_asString(typeName) + sky_asString(sky_asString("{") + sky_asString(sky_asString(sky_call(sky_stringJoin(", "), fs)) + sky_asString("}"))) }() };  if sky_asMap(__subject)["SkyName"] == "GoBinaryExpr" { op := sky_asMap(__subject)["V0"]; _ = op; left := sky_asMap(__subject)["V1"]; _ = left; right := sky_asMap(__subject)["V2"]; _ = right; return sky_asString(Compiler_Emit_EmitExpr(left)) + sky_asString(sky_asString(" ") + sky_asString(sky_asString(op) + sky_asString(sky_asString(" ") + sky_asString(Compiler_Emit_EmitExpr(right))))) };  if sky_asMap(__subject)["SkyName"] == "GoUnaryExpr" { op := sky_asMap(__subject)["V0"]; _ = op; operand := sky_asMap(__subject)["V1"]; _ = operand; return sky_asString(op) + sky_asString(Compiler_Emit_EmitExpr(operand)) };  if sky_asMap(__subject)["SkyName"] == "GoIndexExpr" { target := sky_asMap(__subject)["V0"]; _ = target; index := sky_asMap(__subject)["V1"]; _ = index; return sky_asString(Compiler_Emit_EmitExpr(target)) + sky_asString(sky_asString("[") + sky_asString(sky_asString(Compiler_Emit_EmitExpr(index)) + sky_asString("]"))) };  if sky_asMap(__subject)["SkyName"] == "GoNilExpr" { return "nil" };  return nil }() }()
}

func Compiler_Emit_EscapeGoString(s any) any {
	return sky_call(sky_stringReplace("\\t"), "\\\\t")(sky_call(sky_stringReplace("\\n"), "\\\\n")(sky_call(sky_stringReplace("\\\""), "\\\\\\\"")(sky_call(sky_stringReplace("\\\\"), "\\\\\\\\")(s))))
}

var Empty = Compiler_Env_Empty

var Lookup = Compiler_Env_Lookup

var Extend = Compiler_Env_Extend

var ExtendMany = Compiler_Env_ExtendMany

var Remove = Compiler_Env_Remove

var Keys = Compiler_Env_Keys

var ToList = Compiler_Env_ToList

var FromList = Compiler_Env_FromList

var Union = Compiler_Env_Union

var FreeVarsInEnv = Compiler_Env_FreeVarsInEnv

var GeneralizeInEnv = Compiler_Env_GeneralizeInEnv

var CreatePreludeEnv = Compiler_Env_CreatePreludeEnv

func Compiler_Env_Empty() any {
	return sky_dictEmpty
}

func Compiler_Env_Lookup(name any, env any) any {
	return sky_call(sky_dictGet(name), env)
}

func Compiler_Env_Extend(name any, scheme any, env any) any {
	return sky_call(sky_call(sky_dictInsert(name), scheme), env)
}

func Compiler_Env_ExtendMany(bindings any, env any) any {
	return sky_call(sky_call(sky_listFoldl(func(_p any) any { return func(acc any) any { return sky_call(sky_call(sky_dictInsert(name), scheme), acc) } }), env), bindings)
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
	return sky_call(sky_call(sky_dictFoldl(func(kk any) any { return func(scheme any) any { return func(acc any) any { return sky_call(sky_setUnion(freeVarsInScheme(scheme)), acc) } } }), sky_setEmpty), env)
}

func Compiler_Env_GeneralizeInEnv(env any, t any) any {
	return func() any { typeVars := freeVars(t); _ = typeVars; envVars := Compiler_Env_FreeVarsInEnv(env); _ = envVars; quantified := sky_setToList(sky_call(sky_setDiff(typeVars), envVars)); _ = quantified; return map[string]any{"quantified": quantified, "type_": t} }()
}

func Compiler_Env_CreatePreludeEnv() any {
	return func() any { intT := TConst("Int"); _ = intT; floatT := TConst("Float"); _ = floatT; stringT := TConst("String"); _ = stringT; boolT := TConst("Bool"); _ = boolT; charT := TConst("Char"); _ = charT; unitT := TConst("Unit"); _ = unitT; identityScheme := map[string]any{"quantified": []any{0}, "type_": TFun(TVar(0, SkyJust("a")), TVar(0, SkyJust("a")))}; _ = identityScheme; notScheme := mono(TFun(boolT, boolT)); _ = notScheme; alwaysScheme := map[string]any{"quantified": []any{0, 1}, "type_": TFun(TVar(0, SkyJust("a")), TFun(TVar(1, SkyJust("b")), TVar(0, SkyJust("a"))))}; _ = alwaysScheme; fstScheme := map[string]any{"quantified": []any{0, 1}, "type_": TFun(TTuple([]any{TVar(0, SkyJust("a")), TVar(1, SkyJust("b"))}), TVar(0, SkyJust("a")))}; _ = fstScheme; sndScheme := map[string]any{"quantified": []any{0, 1}, "type_": TFun(TTuple([]any{TVar(0, SkyJust("a")), TVar(1, SkyJust("b"))}), TVar(1, SkyJust("b")))}; _ = sndScheme; clampScheme := map[string]any{"quantified": []any{0}, "type_": TFun(TVar(0, SkyJust("comparable")), TFun(TVar(0, SkyJust("comparable")), TFun(TVar(0, SkyJust("comparable")), TVar(0, SkyJust("comparable")))))}; _ = clampScheme; modByScheme := mono(TFun(intT, TFun(intT, intT))); _ = modByScheme; errorToStringScheme := mono(TFun(TConst("Error"), stringT)); _ = errorToStringScheme; okScheme := map[string]any{"quantified": []any{0, 1}, "type_": TFun(TVar(0, SkyJust("a")), TApp(TConst("Result"), []any{TVar(1, SkyJust("e")), TVar(0, SkyJust("a"))}))}; _ = okScheme; errScheme := map[string]any{"quantified": []any{0, 1}, "type_": TFun(TVar(0, SkyJust("e")), TApp(TConst("Result"), []any{TVar(0, SkyJust("e")), TVar(1, SkyJust("a"))}))}; _ = errScheme; justScheme := map[string]any{"quantified": []any{0}, "type_": TFun(TVar(0, SkyJust("a")), TApp(TConst("Maybe"), []any{TVar(0, SkyJust("a"))}))}; _ = justScheme; nothingScheme := map[string]any{"quantified": []any{0}, "type_": TApp(TConst("Maybe"), []any{TVar(0, SkyJust("a"))})}; _ = nothingScheme; trueScheme := mono(boolT); _ = trueScheme; falseScheme := mono(boolT); _ = falseScheme; return sky_dictFromList([]any{[]any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}}) }()
}

var CheckModule = Compiler_Checker_CheckModule

var RegisterTypeAliases = Compiler_Checker_RegisterTypeAliases

var CollectAnnotations = Compiler_Checker_CollectAnnotations

var CollectAnnotationsLoop = Compiler_Checker_CollectAnnotationsLoop

var PreRegisterFunctions = Compiler_Checker_PreRegisterFunctions

var InferAllDeclarations = Compiler_Checker_InferAllDeclarations

var InferDeclsLoop = Compiler_Checker_InferDeclsLoop

var NewEnv = Compiler_Checker_NewEnv

var Name = Compiler_Checker_Name

var Scheme = Compiler_Checker_Scheme

var PrettyType = Compiler_Checker_PrettyType

var CheckAllExhaustiveness = Compiler_Checker_CheckAllExhaustiveness

var CheckDeclExhaustiveness = Compiler_Checker_CheckDeclExhaustiveness

var CheckExprExhaustiveness = Compiler_Checker_CheckExprExhaustiveness

func Compiler_Checker_CheckModule(mod any, imports any) any {
	return func() any { counter := sky_refNew(100); _ = counter; baseEnv := func() any { return func() any { __subject := imports; if sky_asSkyMaybe(__subject).SkyName == "Just" { importedEnv := sky_asSkyMaybe(__subject).JustValue; _ = importedEnv; return Compiler_Env_Union(importedEnv, Compiler_Env_CreatePreludeEnv()) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return Compiler_Env_CreatePreludeEnv() };  return nil }() }(); _ = baseEnv; aliasEnv := Compiler_Checker_RegisterTypeAliases(sky_asMap(mod)["declarations"], baseEnv); _ = aliasEnv; __tup_registry_adtEnv_adtDiags := Compiler_Adt_RegisterAdts(counter, sky_asMap(mod)["declarations"]); registry := sky_asTuple3(__tup_registry_adtEnv_adtDiags).V0; _ = registry; adtEnv := sky_asTuple3(__tup_registry_adtEnv_adtDiags).V1; _ = adtEnv; adtDiags := sky_asTuple3(__tup_registry_adtEnv_adtDiags).V2; _ = adtDiags; env0 := Compiler_Env_Union(adtEnv, aliasEnv); _ = env0; annotations := Compiler_Checker_CollectAnnotations(sky_asMap(mod)["declarations"]); _ = annotations; env1 := Compiler_Checker_PreRegisterFunctions(counter, sky_asMap(mod)["declarations"], env0); _ = env1; __tup_typedDecls_finalEnv_inferDiags := Compiler_Checker_InferAllDeclarations(counter, registry, env1, sky_asMap(mod)["declarations"], annotations); typedDecls := sky_asTuple3(__tup_typedDecls_finalEnv_inferDiags).V0; _ = typedDecls; finalEnv := sky_asTuple3(__tup_typedDecls_finalEnv_inferDiags).V1; _ = finalEnv; inferDiags := sky_asTuple3(__tup_typedDecls_finalEnv_inferDiags).V2; _ = inferDiags; exhaustDiags := Compiler_Checker_CheckAllExhaustiveness(registry, sky_asMap(mod)["declarations"]); _ = exhaustDiags; allDiags := sky_listConcat([]any{adtDiags, inferDiags, exhaustDiags}); _ = allDiags; return SkyOk(map[string]any{"env": finalEnv, "registry": registry, "declarations": typedDecls, "diagnostics": allDiags}) }()
}

func Compiler_Checker_RegisterTypeAliases(decls any, env any) any {
	return func() any { return func() any { __subject := decls; if len(sky_asList(__subject)) == 0 { return env };  if len(sky_asList(__subject)) > 0 { decl := sky_asList(__subject)[0]; _ = decl; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "TypeAliasDecl" { aliasName := sky_asMap(__subject)["V0"]; _ = aliasName; aliasParams := sky_asMap(__subject)["V1"]; _ = aliasParams; aliasType := sky_asMap(__subject)["V2"]; _ = aliasType; return Compiler_Checker_RegisterTypeAliases(rest, env) };  if true { return Compiler_Checker_RegisterTypeAliases(rest, env) };  return nil }() }() };  return nil }() }()
}

func Compiler_Checker_CollectAnnotations(decls any) any {
	return Compiler_Checker_CollectAnnotationsLoop(decls, sky_dictEmpty)
}

func Compiler_Checker_CollectAnnotationsLoop(decls any, acc any) any {
	return func() any { return func() any { __subject := decls; if len(sky_asList(__subject)) == 0 { return acc };  if len(sky_asList(__subject)) > 0 { decl := sky_asList(__subject)[0]; _ = decl; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "TypeAnnotDecl" { name := sky_asMap(__subject)["V0"]; _ = name; typeExpr := sky_asMap(__subject)["V1"]; _ = typeExpr; return Compiler_Checker_CollectAnnotationsLoop(rest, sky_call(sky_call(sky_dictInsert(Compiler_Checker_Name), typeExpr), acc)) };  if true { return Compiler_Checker_CollectAnnotationsLoop(rest, acc) };  return nil }() }() };  return nil }() }()
}

func Compiler_Checker_PreRegisterFunctions(counter any, decls any, env any) any {
	return func() any { return func() any { __subject := decls; if len(sky_asList(__subject)) == 0 { return env };  if len(sky_asList(__subject)) > 0 { decl := sky_asList(__subject)[0]; _ = decl; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "FunDecl" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { tv := freshVar(counter, SkyNothing()); _ = tv; newEnv := Compiler_Env_Extend(Compiler_Checker_Name, mono(tv), env); _ = newEnv; return Compiler_Checker_PreRegisterFunctions(counter, rest, Compiler_Checker_NewEnv) }() };  if true { return Compiler_Checker_PreRegisterFunctions(counter, rest, env) };  return nil }() }() };  return nil }() }()
}

func Compiler_Checker_InferAllDeclarations(counter any, registry any, env any, decls any, annotations any) any {
	return Compiler_Checker_InferDeclsLoop(counter, registry, env, decls, annotations, []any{}, []any{})
}

func Compiler_Checker_InferDeclsLoop(counter any, registry any, env any, decls any, annotations any, typedDecls any, diagnostics any) any {
	return func() any { return func() any { __subject := decls; if len(sky_asList(__subject)) == 0 { return []any{} };  if len(sky_asList(__subject)) > 0 { decl := sky_asList(__subject)[0]; _ = decl; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "FunDecl" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { annotation := sky_call(sky_dictGet(Compiler_Checker_Name), annotations); _ = annotation; return func() any { return func() any { __subject := Compiler_Infer_InferDeclaration(counter, registry, env, decl, annotation);  return nil }() }(SkyOk) }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Checker_NewEnv() any {
	return Compiler_Env_Extend(sky_asMap(result)["name"], sky_asMap(result)["scheme"], env)
}

func Compiler_Checker_Name() any {
	return sky_asMap(result)["name"]
}

func Compiler_Checker_Scheme() any {
	return sky_asMap(result)["scheme"]
}

func Compiler_Checker_PrettyType() any {
	return formatType(sky_asMap(result)["scheme"])
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

var InferExpr = Compiler_Infer_InferExpr

var InferCall = Compiler_Infer_InferCall

var InferCallArgs = Compiler_Infer_InferCallArgs

var InferLambda = Compiler_Infer_InferLambda

var InferLambdaParams = Compiler_Infer_InferLambdaParams

var InferIf = Compiler_Infer_InferIf

var InferLet = Compiler_Infer_InferLet

var InferLetBindings = Compiler_Infer_InferLetBindings

var InferCase = Compiler_Infer_InferCase

var InferCaseBranches = Compiler_Infer_InferCaseBranches

var InferBinary = Compiler_Infer_InferBinary

var InferBinaryOp = Compiler_Infer_InferBinaryOp

var InferDeclaration = Compiler_Infer_InferDeclaration

var InferFunction = Compiler_Infer_InferFunction

var BindParams = Compiler_Infer_BindParams

var BindParamsLoop = Compiler_Infer_BindParamsLoop

var CheckAnnotation = Compiler_Infer_CheckAnnotation

var ApplySubToEnv = Compiler_Infer_ApplySubToEnv

var InferTupleItems = Compiler_Infer_InferTupleItems

var InferListItems = Compiler_Infer_InferListItems

var InferListItemsLoop = Compiler_Infer_InferListItemsLoop

var InferRecordFields = Compiler_Infer_InferRecordFields

var InferRecordUpdateFields = Compiler_Infer_InferRecordUpdateFields

var InferRecordUpdateFieldsLoop = Compiler_Infer_InferRecordUpdateFieldsLoop

func Compiler_Infer_InferExpr(counter any, registry any, env any, expr any) any {
	return func() any { return func() any { __subject := expr; if sky_asMap(__subject)["SkyName"] == "IntLitExpr" { return SkyOk(map[string]any{"substitution": emptySub, "type_": TConst("Int")}) };  if sky_asMap(__subject)["SkyName"] == "FloatLitExpr" { return SkyOk(map[string]any{"substitution": emptySub, "type_": TConst("Float")}) };  if sky_asMap(__subject)["SkyName"] == "StringLitExpr" { return SkyOk(map[string]any{"substitution": emptySub, "type_": TConst("String")}) };  if sky_asMap(__subject)["SkyName"] == "CharLitExpr" { return SkyOk(map[string]any{"substitution": emptySub, "type_": TConst("Char")}) };  if sky_asMap(__subject)["SkyName"] == "BoolLitExpr" { return SkyOk(map[string]any{"substitution": emptySub, "type_": TConst("Bool")}) };  if sky_asMap(__subject)["SkyName"] == "UnitExpr" { return SkyOk(map[string]any{"substitution": emptySub, "type_": TConst("Unit")}) };  if sky_asMap(__subject)["SkyName"] == "IdentifierExpr" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { return func() any { __subject := Compiler_Env_Lookup(name, env); if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyErr(sky_asString("Unbound variable: ") + sky_asString(name)) };  if sky_asSkyMaybe(__subject).SkyName == "Just" { scheme := sky_asSkyMaybe(__subject).JustValue; _ = scheme; return func() any { t := instantiate(counter, scheme); _ = t; return SkyOk(map[string]any{"substitution": emptySub, "type_": t}) }() };  if sky_asMap(__subject)["SkyName"] == "QualifiedExpr" { parts := sky_asMap(__subject)["V0"]; _ = parts; return func() any { qualName := sky_call(sky_stringJoin("."), parts); _ = qualName; return func() any { return func() any { __subject := Compiler_Env_Lookup(qualName, env); if sky_asSkyMaybe(__subject).SkyName == "Just" { scheme := sky_asSkyMaybe(__subject).JustValue; _ = scheme; return SkyOk(map[string]any{"substitution": emptySub, "type_": instantiate(counter, scheme)}) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return func() any { return func() any { __subject := sky_listReverse(parts); if len(sky_asList(__subject)) > 0 { last := sky_asList(__subject)[0]; _ = last; return func() any { return func() any { __subject := Compiler_Env_Lookup(last, env); if sky_asSkyMaybe(__subject).SkyName == "Just" { scheme := sky_asSkyMaybe(__subject).JustValue; _ = scheme; return SkyOk(map[string]any{"substitution": emptySub, "type_": instantiate(counter, scheme)}) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyErr(sky_asString("Unbound qualified name: ") + sky_asString(qualName)) };  if len(sky_asList(__subject)) == 0 { return SkyErr("Empty qualified name") };  if sky_asMap(__subject)["SkyName"] == "TupleExpr" { items := sky_asMap(__subject)["V0"]; _ = items; return Compiler_Infer_InferTupleItems(counter, registry, env, items, emptySub, []any{}) };  if sky_asMap(__subject)["SkyName"] == "ListExpr" { items := sky_asMap(__subject)["V0"]; _ = items; return Compiler_Infer_InferListItems(counter, registry, env, items) };  if sky_asMap(__subject)["SkyName"] == "RecordExpr" { fields := sky_asMap(__subject)["V0"]; _ = fields; return Compiler_Infer_InferRecordFields(counter, registry, env, fields, emptySub, sky_dictEmpty) };  if sky_asMap(__subject)["SkyName"] == "RecordUpdateExpr" { base := sky_asMap(__subject)["V0"]; _ = base; fields := sky_asMap(__subject)["V1"]; _ = fields; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, base); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { baseResult := sky_asSkyResult(__subject).OkValue; _ = baseResult; return func() any { return func() any { __subject := Compiler_Infer_InferRecordUpdateFields(counter, registry, env, fields, sky_asMap(baseResult)["substitution"]); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { fieldSub := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = fieldSub; fieldTypes := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = fieldTypes; return func() any { combinedSub := composeSubs(fieldSub, sky_asMap(baseResult)["substitution"]); _ = combinedSub; baseType := applySub(combinedSub, sky_asMap(baseResult)["type_"]); _ = baseType; return func() any { return func() any { __subject := baseType; if sky_asMap(__subject)["SkyName"] == "TRecord" { existingFields := sky_asMap(__subject)["V0"]; _ = existingFields; return func() any { updatedFields := sky_call(sky_dictUnion(fieldTypes), existingFields); _ = updatedFields; return SkyOk(map[string]any{"substitution": combinedSub, "type_": TRecord(updatedFields)}) }() };  if true { return SkyOk(map[string]any{"substitution": combinedSub, "type_": TRecord(fieldTypes)}) };  if sky_asMap(__subject)["SkyName"] == "FieldAccessExpr" { target := sky_asMap(__subject)["V0"]; _ = target; fieldName := sky_asMap(__subject)["V1"]; _ = fieldName; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, target); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { targetResult := sky_asSkyResult(__subject).OkValue; _ = targetResult; return func() any { resultVar := freshVar(counter, SkyNothing()); _ = resultVar; targetType := applySub(sky_asMap(targetResult)["substitution"], sky_asMap(targetResult)["type_"]); _ = targetType; return func() any { return func() any { __subject := targetType; if sky_asMap(__subject)["SkyName"] == "TRecord" { fields := sky_asMap(__subject)["V0"]; _ = fields; return func() any { return func() any { __subject := sky_call(sky_dictGet(fieldName), fields); if sky_asSkyMaybe(__subject).SkyName == "Just" { fieldType := sky_asSkyMaybe(__subject).JustValue; _ = fieldType; return SkyOk(map[string]any{"substitution": sky_asMap(targetResult)["substitution"], "type_": fieldType}) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyErr(sky_asString("Record has no field '") + sky_asString(sky_asString(fieldName) + sky_asString("'"))) };  if true { return SkyOk(map[string]any{"substitution": sky_asMap(targetResult)["substitution"], "type_": resultVar}) };  if sky_asMap(__subject)["SkyName"] == "CallExpr" { callee := sky_asMap(__subject)["V0"]; _ = callee; args := sky_asMap(__subject)["V1"]; _ = args; return Compiler_Infer_InferCall(counter, registry, env, callee, args) };  if sky_asMap(__subject)["SkyName"] == "LambdaExpr" { params := sky_asMap(__subject)["V0"]; _ = params; body := sky_asMap(__subject)["V1"]; _ = body; return Compiler_Infer_InferLambda(counter, registry, env, params, body) };  if sky_asMap(__subject)["SkyName"] == "IfExpr" { condition := sky_asMap(__subject)["V0"]; _ = condition; thenBranch := sky_asMap(__subject)["V1"]; _ = thenBranch; elseBranch := sky_asMap(__subject)["V2"]; _ = elseBranch; return Compiler_Infer_InferIf(counter, registry, env, condition, thenBranch, elseBranch) };  if sky_asMap(__subject)["SkyName"] == "LetExpr" { bindings := sky_asMap(__subject)["V0"]; _ = bindings; body := sky_asMap(__subject)["V1"]; _ = body; return Compiler_Infer_InferLet(counter, registry, env, bindings, body) };  if sky_asMap(__subject)["SkyName"] == "CaseExpr" { subject := sky_asMap(__subject)["V0"]; _ = subject; branches := sky_asMap(__subject)["V1"]; _ = branches; return Compiler_Infer_InferCase(counter, registry, env, subject, branches) };  if sky_asMap(__subject)["SkyName"] == "BinaryExpr" { op := sky_asMap(__subject)["V0"]; _ = op; left := sky_asMap(__subject)["V1"]; _ = left; right := sky_asMap(__subject)["V2"]; _ = right; return Compiler_Infer_InferBinary(counter, registry, env, op, left, right) };  if sky_asMap(__subject)["SkyName"] == "NegateExpr" { inner := sky_asMap(__subject)["V0"]; _ = inner; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, inner); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return func() any { return func() any { __subject := unify(sky_asMap(result)["type_"], TConst("Int")); if sky_asSkyResult(__subject).SkyName == "Ok" { sub := sky_asSkyResult(__subject).OkValue; _ = sub; return SkyOk(map[string]any{"substitution": composeSubs(sub, sky_asMap(result)["substitution"]), "type_": applySub(sub, sky_asMap(result)["type_"])}) };  if sky_asSkyResult(__subject).SkyName == "Err" { return func() any { return func() any { __subject := unify(sky_asMap(result)["type_"], TConst("Float")); if sky_asSkyResult(__subject).SkyName == "Ok" { sub := sky_asSkyResult(__subject).OkValue; _ = sub; return SkyOk(map[string]any{"substitution": composeSubs(sub, sky_asMap(result)["substitution"]), "type_": applySub(sub, sky_asMap(result)["type_"])}) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Negation requires a number type: ") + sky_asString(e)) };  if sky_asMap(__subject)["SkyName"] == "ParenExpr" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Compiler_Infer_InferExpr(counter, registry, env, inner) };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_InferCall(counter any, registry any, env any, callee any, args any) any {
	return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, callee); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { calleeResult := sky_asSkyResult(__subject).OkValue; _ = calleeResult; return Compiler_Infer_InferCallArgs(counter, registry, env, sky_asMap(calleeResult)["type_"], sky_asMap(calleeResult)["substitution"], args) };  return nil }() }()
}

func Compiler_Infer_InferCallArgs(counter any, registry any, env any, fnType any, sub any, args any) any {
	return func() any { return func() any { __subject := args; if len(sky_asList(__subject)) == 0 { return SkyOk(map[string]any{"substitution": sub, "type_": fnType}) };  if len(sky_asList(__subject)) > 0 { arg := sky_asList(__subject)[0]; _ = arg; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { resultVar := freshVar(counter, SkyNothing()); _ = resultVar; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, Compiler_Infer_ApplySubToEnv(sub, env), arg); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { argResult := sky_asSkyResult(__subject).OkValue; _ = argResult; return func() any { combinedSub := composeSubs(sky_asMap(argResult)["substitution"], sub); _ = combinedSub; expectedFnType := TFun(sky_asMap(argResult)["type_"], resultVar); _ = expectedFnType; actualFnType := applySub(combinedSub, fnType); _ = actualFnType; return func() any { return func() any { __subject := unify(actualFnType, expectedFnType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Type error in function call: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return func() any { finalSub := composeSubs(unifySub, combinedSub); _ = finalSub; resultType := applySub(finalSub, resultVar); _ = resultType; return Compiler_Infer_InferCallArgs(counter, registry, env, resultType, finalSub, rest) }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }()
}

func Compiler_Infer_InferLambda(counter any, registry any, env any, params any, body any) any {
	return Compiler_Infer_InferLambdaParams(counter, registry, env, params, body, []any{})
}

func Compiler_Infer_InferLambdaParams(counter any, registry any, env any, params any, body any, paramTypes any) any {
	return func() any { return func() any { __subject := params; if len(sky_asList(__subject)) == 0 { return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, body); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { bodyResult := sky_asSkyResult(__subject).OkValue; _ = bodyResult; return func() any { resultType := sky_call(sky_call(sky_listFoldr(func(pt any) any { return func(acc any) any { return TFun(pt, acc) } }), sky_asMap(bodyResult)["type_"]), paramTypes); _ = resultType; return SkyOk(map[string]any{"substitution": sky_asMap(bodyResult)["substitution"], "type_": applySub(sky_asMap(bodyResult)["substitution"], resultType)}) }() };  if len(sky_asList(__subject)) > 0 { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { paramVar := freshVar(counter, SkyNothing()); _ = paramVar; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, pat, paramVar); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { patResult := sky_asSkyResult(__subject).OkValue; _ = patResult; return func() any { newEnv := sky_call(sky_call(sky_listFoldl(func(_p any) any { return func(acc any) any { return Compiler_Env_Extend(name, mono(t), acc) } }), env), sky_asMap(patResult)["bindings"]); _ = newEnv; boundParamType := applySub(sky_asMap(patResult)["substitution"], paramVar); _ = boundParamType; return Compiler_Infer_InferLambdaParams(counter, registry, newEnv, rest, body, sky_call(sky_listAppend(paramTypes), []any{boundParamType})) }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_InferIf(counter any, registry any, env any, condition any, thenBranch any, elseBranch any) any {
	return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, condition); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { condResult := sky_asSkyResult(__subject).OkValue; _ = condResult; return func() any { return func() any { __subject := unify(sky_asMap(condResult)["type_"], TConst("Bool")); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Condition must be Bool: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { condSub := sky_asSkyResult(__subject).OkValue; _ = condSub; return func() any { sub1 := composeSubs(condSub, sky_asMap(condResult)["substitution"]); _ = sub1; env1 := Compiler_Infer_ApplySubToEnv(sub1, env); _ = env1; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env1, thenBranch); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { thenResult := sky_asSkyResult(__subject).OkValue; _ = thenResult; return func() any { sub2 := composeSubs(sky_asMap(thenResult)["substitution"], sub1); _ = sub2; env2 := Compiler_Infer_ApplySubToEnv(sub2, env); _ = env2; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env2, elseBranch); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { elseResult := sky_asSkyResult(__subject).OkValue; _ = elseResult; return func() any { sub3 := composeSubs(sky_asMap(elseResult)["substitution"], sub2); _ = sub3; return func() any { return func() any { __subject := unify(applySub(sub3, sky_asMap(thenResult)["type_"]), applySub(sub3, sky_asMap(elseResult)["type_"])); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("If branches have different types: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { branchSub := sky_asSkyResult(__subject).OkValue; _ = branchSub; return func() any { finalSub := composeSubs(branchSub, sub3); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "type_": applySub(finalSub, sky_asMap(thenResult)["type_"])}) }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_InferLet(counter any, registry any, env any, bindings any, body any) any {
	return Compiler_Infer_InferLetBindings(counter, registry, env, bindings, body)
}

func Compiler_Infer_InferLetBindings(counter any, registry any, env any, bindings any, body any) any {
	return func() any { return func() any { __subject := bindings; if len(sky_asList(__subject)) == 0 { return Compiler_Infer_InferExpr(counter, registry, env, body) };  if len(sky_asList(__subject)) > 0 { binding := sky_asList(__subject)[0]; _ = binding; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, sky_asMap(binding)["value"]); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { valueResult := sky_asSkyResult(__subject).OkValue; _ = valueResult; return func() any { sub := sky_asMap(valueResult)["substitution"]; _ = sub; envWithSub := Compiler_Infer_ApplySubToEnv(sub, env); _ = envWithSub; generalizedScheme := Compiler_Env_GeneralizeInEnv(envWithSub, applySub(sub, sky_asMap(valueResult)["type_"])); _ = generalizedScheme; return func() any { return func() any { __subject := sky_asMap(binding)["pattern"]; if sky_asMap(__subject)["SkyName"] == "PVariable" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { newEnv := Compiler_Env_Extend(name, generalizedScheme, envWithSub); _ = newEnv; return func() any { return func() any { __subject := Compiler_Infer_InferLetBindings(counter, registry, newEnv, rest, body); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { bodyResult := sky_asSkyResult(__subject).OkValue; _ = bodyResult; return SkyOk(map[string]any{"substitution": composeSubs(sky_asMap(bodyResult)["substitution"], sub), "type_": sky_asMap(bodyResult)["type_"]}) };  if true { return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, sky_asMap(binding)["pattern"], applySub(sub, sky_asMap(valueResult)["type_"])); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { patResult := sky_asSkyResult(__subject).OkValue; _ = patResult; return func() any { combinedSub := composeSubs(sky_asMap(patResult)["substitution"], sub); _ = combinedSub; newEnv := sky_call(sky_call(sky_listFoldl(func(_p any) any { return func(acc any) any { return Compiler_Env_Extend(name, mono(t), acc) } }), Compiler_Infer_ApplySubToEnv(combinedSub, env)), sky_asMap(patResult)["bindings"]); _ = newEnv; return func() any { return func() any { __subject := Compiler_Infer_InferLetBindings(counter, registry, newEnv, rest, body); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { bodyResult := sky_asSkyResult(__subject).OkValue; _ = bodyResult; return SkyOk(map[string]any{"substitution": composeSubs(sky_asMap(bodyResult)["substitution"], combinedSub), "type_": sky_asMap(bodyResult)["type_"]}) };  return nil }() }() }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_InferCase(counter any, registry any, env any, subject any, branches any) any {
	return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, subject); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { subjectResult := sky_asSkyResult(__subject).OkValue; _ = subjectResult; return func() any { resultVar := freshVar(counter, SkyNothing()); _ = resultVar; return Compiler_Infer_InferCaseBranches(counter, registry, env, sky_asMap(subjectResult)["type_"], sky_asMap(subjectResult)["substitution"], branches, resultVar) }() };  return nil }() }()
}

func Compiler_Infer_InferCaseBranches(counter any, registry any, env any, subjectType any, sub any, branches any, resultType any) any {
	return func() any { return func() any { __subject := branches; if len(sky_asList(__subject)) == 0 { return SkyOk(map[string]any{"substitution": sub, "type_": applySub(sub, resultType)}) };  if len(sky_asList(__subject)) > 0 { branch := sky_asList(__subject)[0]; _ = branch; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { currentSubjectType := applySub(sub, subjectType); _ = currentSubjectType; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, sky_asMap(branch)["pattern"], currentSubjectType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { patResult := sky_asSkyResult(__subject).OkValue; _ = patResult; return func() any { patSub := composeSubs(sky_asMap(patResult)["substitution"], sub); _ = patSub; branchEnv := sky_call(sky_call(sky_listFoldl(func(_p any) any { return func(acc any) any { return Compiler_Env_Extend(name, mono(applySub(patSub, t)), acc) } }), Compiler_Infer_ApplySubToEnv(patSub, env)), sky_asMap(patResult)["bindings"]); _ = branchEnv; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, branchEnv, sky_asMap(branch)["body"]); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { bodyResult := sky_asSkyResult(__subject).OkValue; _ = bodyResult; return func() any { bodySub := composeSubs(sky_asMap(bodyResult)["substitution"], patSub); _ = bodySub; return func() any { return func() any { __subject := unify(applySub(bodySub, resultType), applySub(bodySub, sky_asMap(bodyResult)["type_"])); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Case branch type mismatch: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return func() any { finalSub := composeSubs(unifySub, bodySub); _ = finalSub; return Compiler_Infer_InferCaseBranches(counter, registry, env, subjectType, finalSub, rest, resultType) }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }()
}

func Compiler_Infer_InferBinary(counter any, registry any, env any, op any, left any, right any) any {
	return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, left); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { leftResult := sky_asSkyResult(__subject).OkValue; _ = leftResult; return func() any { sub1 := sky_asMap(leftResult)["substitution"]; _ = sub1; env1 := Compiler_Infer_ApplySubToEnv(sub1, env); _ = env1; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env1, right); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { rightResult := sky_asSkyResult(__subject).OkValue; _ = rightResult; return func() any { sub2 := composeSubs(sky_asMap(rightResult)["substitution"], sub1); _ = sub2; lt := applySub(sub2, sky_asMap(leftResult)["type_"]); _ = lt; rt := applySub(sub2, sky_asMap(rightResult)["type_"]); _ = rt; return Compiler_Infer_InferBinaryOp(counter, op, lt, rt, sub2) }() };  return nil }() }() }() };  return nil }() }()
}

func Compiler_Infer_InferBinaryOp(counter any, op any, lt any, rt any, sub any) any {
	return func() any { if sky_asBool(sky_asBool(sky_equal(op, "+")) || sky_asBool(sky_asBool(sky_equal(op, "-")) || sky_asBool(sky_asBool(sky_equal(op, "*")) || sky_asBool(sky_equal(op, "%"))))) { return func() any { return func() any { __subject := unify(lt, rt); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Arithmetic operator '") + sky_asString(sky_asString(op) + sky_asString(sky_asString("': ") + sky_asString(e)))) };  if sky_asSkyResult(__subject).SkyName == "Ok" { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return func() any { finalSub := composeSubs(unifySub, sub); _ = finalSub; resultType := applySub(finalSub, lt); _ = resultType; return SkyOk(map[string]any{"substitution": finalSub, "type_": resultType}) }() };  return nil }() }() }; return func() any { if sky_asBool(sky_equal(op, "/")) { return func() any { return func() any { __subject := unify(lt, TConst("Float")); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Division requires Float: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asSkyResult(__subject).OkValue; _ = s1; return func() any { return func() any { __subject := unify(rt, TConst("Float")); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Division requires Float: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asSkyResult(__subject).OkValue; _ = s2; return func() any { finalSub := composeSubs(s2, composeSubs(s1, sub)); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "type_": TConst("Float")}) }() };  return nil }() }() };  return nil }() }() }; return func() any { if sky_asBool(sky_equal(op, "//")) { return func() any { return func() any { __subject := unify(lt, TConst("Int")); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Integer division requires Int: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asSkyResult(__subject).OkValue; _ = s1; return func() any { return func() any { __subject := unify(rt, TConst("Int")); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Integer division requires Int: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asSkyResult(__subject).OkValue; _ = s2; return SkyOk(map[string]any{"substitution": composeSubs(s2, composeSubs(s1, sub)), "type_": TConst("Int")}) };  return nil }() }() };  return nil }() }() }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "==")) || sky_asBool(sky_asBool(sky_equal(op, "!=")) || sky_asBool(sky_asBool(sky_equal(op, "/=")) || sky_asBool(sky_asBool(sky_equal(op, "<")) || sky_asBool(sky_asBool(sky_equal(op, "<=")) || sky_asBool(sky_asBool(sky_equal(op, ">")) || sky_asBool(sky_equal(op, ">=")))))))) { return func() any { return func() any { __subject := unify(lt, rt); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Comparison operator '") + sky_asString(sky_asString(op) + sky_asString(sky_asString("': ") + sky_asString(e)))) };  if sky_asSkyResult(__subject).SkyName == "Ok" { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return SkyOk(map[string]any{"substitution": composeSubs(unifySub, sub), "type_": TConst("Bool")}) };  return nil }() }() }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "&&")) || sky_asBool(sky_equal(op, "||"))) { return func() any { return func() any { __subject := unify(lt, TConst("Bool")); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Logical operator requires Bool: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asSkyResult(__subject).OkValue; _ = s1; return func() any { return func() any { __subject := unify(applySub(s1, rt), TConst("Bool")); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Logical operator requires Bool: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asSkyResult(__subject).OkValue; _ = s2; return SkyOk(map[string]any{"substitution": composeSubs(s2, composeSubs(s1, sub)), "type_": TConst("Bool")}) };  return nil }() }() };  return nil }() }() }; return func() any { if sky_asBool(sky_equal(op, "++")) { return func() any { return func() any { __subject := unify(lt, rt); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Append operator: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return SkyOk(map[string]any{"substitution": composeSubs(unifySub, sub), "type_": applySub(composeSubs(unifySub, sub), lt)}) };  return nil }() }() }; return func() any { if sky_asBool(sky_equal(op, "::")) { return func() any { listType := TApp(TConst("List"), []any{lt}); _ = listType; return func() any { return func() any { __subject := unify(rt, listType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Cons operator: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return SkyOk(map[string]any{"substitution": composeSubs(unifySub, sub), "type_": applySub(composeSubs(unifySub, sub), rt)}) };  return nil }() }() }() }; return func() any { if sky_asBool(sky_equal(op, "|>")) { return func() any { resultVar := freshVar(counter, SkyNothing()); _ = resultVar; expectedFnType := TFun(lt, resultVar); _ = expectedFnType; return func() any { return func() any { __subject := unify(rt, expectedFnType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Pipeline operator: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return func() any { finalSub := composeSubs(unifySub, sub); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "type_": applySub(finalSub, resultVar)}) }() };  return nil }() }() }() }; return func() any { if sky_asBool(sky_equal(op, "<|")) { return func() any { resultVar := freshVar(counter, SkyNothing()); _ = resultVar; expectedFnType := TFun(rt, resultVar); _ = expectedFnType; return func() any { return func() any { __subject := unify(lt, expectedFnType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Reverse pipeline operator: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return func() any { finalSub := composeSubs(unifySub, sub); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "type_": applySub(finalSub, resultVar)}) }() };  return nil }() }() }() }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, ">>")) || sky_asBool(sky_equal(op, "<<"))) { return func() any { aVar := freshVar(counter, SkyJust("a")); _ = aVar; bVar := freshVar(counter, SkyJust("b")); _ = bVar; cVar := freshVar(counter, SkyJust("c")); _ = cVar; return func() any { if sky_asBool(sky_equal(op, ">>")) { return func() any { return func() any { __subject := unify(lt, TFun(aVar, bVar)); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Composition: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asSkyResult(__subject).OkValue; _ = s1; return func() any { return func() any { __subject := unify(applySub(s1, rt), TFun(applySub(s1, bVar), cVar)); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Composition: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asSkyResult(__subject).OkValue; _ = s2; return func() any { finalSub := composeSubs(s2, composeSubs(s1, sub)); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "type_": TFun(applySub(finalSub, aVar), applySub(finalSub, cVar))}) }() };  return nil }() }() };  return nil }() }() }; return func() any { return func() any { __subject := unify(rt, TFun(aVar, bVar)); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Composition: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asSkyResult(__subject).OkValue; _ = s1; return func() any { return func() any { __subject := unify(applySub(s1, lt), TFun(applySub(s1, bVar), cVar)); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Composition: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asSkyResult(__subject).OkValue; _ = s2; return func() any { finalSub := composeSubs(s2, composeSubs(s1, sub)); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "type_": TFun(applySub(finalSub, aVar), applySub(finalSub, cVar))}) }() };  return nil }() }() };  return nil }() }() }() }() }; return SkyErr(sky_asString("Unknown operator: ") + sky_asString(op)) }() }() }() }() }() }() }() }() }() }()
}

func Compiler_Infer_InferDeclaration(counter any, registry any, env any, decl any, annotation any) any {
	return func() any { return func() any { __subject := decl; if sky_asMap(__subject)["SkyName"] == "FunDecl" { name := sky_asMap(__subject)["V0"]; _ = name; params := sky_asMap(__subject)["V1"]; _ = params; body := sky_asMap(__subject)["V2"]; _ = body; return Compiler_Infer_InferFunction(counter, registry, env, name, params, body, annotation) };  if true { return SkyErr("inferDeclaration: not a function declaration") };  return nil }() }()
}

func Compiler_Infer_InferFunction(counter any, registry any, env any, name any, params any, body any, annotation any) any {
	return func() any { paramVars := sky_call(sky_listMap(func(p any) any { return freshVar(counter, SkyNothing()) }), params); _ = paramVars; bindResult := Compiler_Infer_BindParams(counter, registry, env, params, paramVars); _ = bindResult; return func() any { return func() any { __subject := bindResult; if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { paramSub := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = paramSub; paramEnv := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = paramEnv; return func() any { selfVar := freshVar(counter, SkyNothing()); _ = selfVar; envWithSelf := Compiler_Env_Extend(name, mono(selfVar), paramEnv); _ = envWithSelf; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, envWithSelf, body); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("In function '") + sky_asString(sky_asString(name) + sky_asString(sky_asString("': ") + sky_asString(e)))) };  if sky_asSkyResult(__subject).SkyName == "Ok" { bodyResult := sky_asSkyResult(__subject).OkValue; _ = bodyResult; return func() any { bodySub := composeSubs(sky_asMap(bodyResult)["substitution"], paramSub); _ = bodySub; resolvedParamTypes := sky_call(sky_listMap(func(pv any) any { return applySub(bodySub, pv) }), paramVars); _ = resolvedParamTypes; bodyType := applySub(bodySub, sky_asMap(bodyResult)["type_"]); _ = bodyType; funType := sky_call(sky_call(sky_listFoldr(func(pt any) any { return func(acc any) any { return TFun(pt, acc) } }), bodyType), resolvedParamTypes); _ = funType; selfType := applySub(bodySub, selfVar); _ = selfType; return func() any { return func() any { __subject := unify(selfType, funType); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Recursive type error in '") + sky_asString(sky_asString(name) + sky_asString(sky_asString("': ") + sky_asString(e)))) };  if sky_asSkyResult(__subject).SkyName == "Ok" { selfSub := sky_asSkyResult(__subject).OkValue; _ = selfSub; return func() any { finalSub := composeSubs(selfSub, bodySub); _ = finalSub; finalType := applySub(finalSub, funType); _ = finalType; diagnostics := Compiler_Infer_CheckAnnotation(counter, env, finalType, annotation); _ = diagnostics; scheme := Compiler_Env_GeneralizeInEnv(env, finalType); _ = scheme; return SkyOk(map[string]any{"name": name, "scheme": scheme, "diagnostics": diagnostics}) }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }()
}

func Compiler_Infer_BindParams(counter any, registry any, env any, params any, types any) any {
	return Compiler_Infer_BindParamsLoop(counter, registry, env, params, types, emptySub)
}

func Compiler_Infer_BindParamsLoop(counter any, registry any, env any, params any, types any, sub any) any {
	return func() any { return func() any { __subject := params; if len(sky_asList(__subject)) == 0 { return SkyOk([]any{}) };  if len(sky_asList(__subject)) > 0 { pat := sky_asList(__subject)[0]; _ = pat; restPats := sky_asList(__subject)[1:]; _ = restPats; return func() any { return func() any { __subject := types; if len(sky_asList(__subject)) == 0 { return SkyErr("Parameter count mismatch") };  if len(sky_asList(__subject)) > 0 { t := sky_asList(__subject)[0]; _ = t; restTypes := sky_asList(__subject)[1:]; _ = restTypes; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, pat, applySub(sub, t)); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { patResult := sky_asSkyResult(__subject).OkValue; _ = patResult; return func() any { combinedSub := composeSubs(sky_asMap(patResult)["substitution"], sub); _ = combinedSub; newEnv := sky_call(sky_call(sky_listFoldl(func(_p any) any { return func(acc any) any { return Compiler_Env_Extend(name, mono(applySub(combinedSub, bt)), acc) } }), env), sky_asMap(patResult)["bindings"]); _ = newEnv; return Compiler_Infer_BindParamsLoop(counter, registry, newEnv, restPats, restTypes, combinedSub) }() };  return nil }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_CheckAnnotation(counter any, env any, inferredType any, annotation any) any {
	return func() any { return func() any { __subject := annotation; if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return []any{} };  if sky_asSkyMaybe(__subject).SkyName == "Just" { annotExpr := sky_asSkyMaybe(__subject).JustValue; _ = annotExpr; return func() any { annotType := Compiler_Adt_ResolveTypeExpr(sky_dictEmpty, annotExpr); _ = annotType; return func() any { return func() any { __subject := unify(inferredType, annotType); if sky_asSkyResult(__subject).SkyName == "Ok" { return []any{} };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return []any{sky_asString("Type annotation mismatch: declared ") + sky_asString(sky_asString(formatType(annotType)) + sky_asString(sky_asString(" but inferred ") + sky_asString(sky_asString(formatType(inferredType)) + sky_asString(sky_asString(" (") + sky_asString(sky_asString(e) + sky_asString(")"))))))} };  return nil }() }() }() };  return nil }() }()
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
	return func() any { return func() any { __subject := items; if len(sky_asList(__subject)) == 0 { return SkyOk(map[string]any{"substitution": sub, "type_": TApp(TConst("List"), []any{applySub(sub, elemType)})}) };  if len(sky_asList(__subject)) > 0 { item := sky_asList(__subject)[0]; _ = item; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, Compiler_Infer_ApplySubToEnv(sub, env), item); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return func() any { itemSub := composeSubs(sky_asMap(result)["substitution"], sub); _ = itemSub; return func() any { return func() any { __subject := unify(applySub(itemSub, elemType), sky_asMap(result)["type_"]); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("List element type mismatch: ") + sky_asString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return Compiler_Infer_InferListItemsLoop(counter, registry, env, rest, composeSubs(unifySub, itemSub), elemType) };  return nil }() }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_InferRecordFields(counter any, registry any, env any, fields any, sub any, fieldTypes any) any {
	return func() any { return func() any { __subject := fields; if len(sky_asList(__subject)) == 0 { return SkyOk(map[string]any{"substitution": sub, "type_": TRecord(fieldTypes)}) };  if len(sky_asList(__subject)) > 0 { field := sky_asList(__subject)[0]; _ = field; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, Compiler_Infer_ApplySubToEnv(sub, env), sky_asMap(field)["value"]); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return func() any { newSub := composeSubs(sky_asMap(result)["substitution"], sub); _ = newSub; return Compiler_Infer_InferRecordFields(counter, registry, env, rest, newSub, sky_call(sky_call(sky_dictInsert(sky_asMap(field)["name"]), sky_asMap(result)["type_"]), fieldTypes)) }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_InferRecordUpdateFields(counter any, registry any, env any, fields any, sub any) any {
	return Compiler_Infer_InferRecordUpdateFieldsLoop(counter, registry, env, fields, sub, sky_dictEmpty)
}

func Compiler_Infer_InferRecordUpdateFieldsLoop(counter any, registry any, env any, fields any, sub any, fieldTypes any) any {
	return func() any { return func() any { __subject := fields; if len(sky_asList(__subject)) == 0 { return SkyOk([]any{}) };  if len(sky_asList(__subject)) > 0 { field := sky_asList(__subject)[0]; _ = field; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, Compiler_Infer_ApplySubToEnv(sub, env), sky_asMap(field)["value"]); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return Compiler_Infer_InferRecordUpdateFieldsLoop(counter, registry, env, rest, composeSubs(sky_asMap(result)["substitution"], sub), sky_call(sky_call(sky_dictInsert(sky_asMap(field)["name"]), sky_asMap(result)["type_"]), fieldTypes)) };  return nil }() }() };  return nil }() }()
}

var unify = Compiler_Unify_Unify

var UnifyConst = Compiler_Unify_UnifyConst

var UnifyFun = Compiler_Unify_UnifyFun

var UnifyApp = Compiler_Unify_UnifyApp

var UnifyTuple = Compiler_Unify_UnifyTuple

var UnifyRecord = Compiler_Unify_UnifyRecord

var BindVar = Compiler_Unify_BindVar

var IsUniversalUnifier = Compiler_Unify_IsUniversalUnifier

var IsNumericCoercion = Compiler_Unify_IsNumericCoercion

var UnifyList = Compiler_Unify_UnifyList

var UnifyRecords = Compiler_Unify_UnifyRecords

var UnifyRecordFields = Compiler_Unify_UnifyRecordFields

func Compiler_Unify_Unify(t1 any, t2 any) any {
	return func() any { return func() any { __subject := t1; if sky_asMap(__subject)["SkyName"] == "TVar" { id1 := sky_asMap(__subject)["V0"]; _ = id1; return Compiler_Unify_BindVar(id1, t2) };  if sky_asMap(__subject)["SkyName"] == "TConst" { nameA := sky_asMap(__subject)["V0"]; _ = nameA; return Compiler_Unify_UnifyConst(nameA, t1, t2) };  if sky_asMap(__subject)["SkyName"] == "TFun" { fromA := sky_asMap(__subject)["V0"]; _ = fromA; toA := sky_asMap(__subject)["V1"]; _ = toA; return Compiler_Unify_UnifyFun(fromA, toA, t2) };  if sky_asMap(__subject)["SkyName"] == "TApp" { ctorA := sky_asMap(__subject)["V0"]; _ = ctorA; argsA := sky_asMap(__subject)["V1"]; _ = argsA; return Compiler_Unify_UnifyApp(ctorA, argsA, t2) };  if sky_asMap(__subject)["SkyName"] == "TTuple" { itemsA := sky_asMap(__subject)["V0"]; _ = itemsA; return Compiler_Unify_UnifyTuple(itemsA, t2) };  if sky_asMap(__subject)["SkyName"] == "TRecord" { fieldsA := sky_asMap(__subject)["V0"]; _ = fieldsA; return Compiler_Unify_UnifyRecord(fieldsA, t2) };  return nil }() }()
}

func Compiler_Unify_UnifyConst(nameA any, t1 any, t2 any) any {
	return func() any { return func() any { __subject := t2; if sky_asMap(__subject)["SkyName"] == "TVar" { id2 := sky_asMap(__subject)["V0"]; _ = id2; return Compiler_Unify_BindVar(id2, t1) };  if sky_asMap(__subject)["SkyName"] == "TConst" { nameB := sky_asMap(__subject)["V0"]; _ = nameB; return func() any { if sky_asBool(sky_equal(nameA, nameB)) { return SkyOk(emptySub) }; return func() any { if sky_asBool(sky_asBool(Compiler_Unify_IsUniversalUnifier(nameA)) || sky_asBool(Compiler_Unify_IsUniversalUnifier(nameB))) { return SkyOk(emptySub) }; return func() any { if sky_asBool(Compiler_Unify_IsNumericCoercion(nameA, nameB)) { return SkyOk(emptySub) }; return SkyErr(sky_asString("Type mismatch: ") + sky_asString(sky_asString(nameA) + sky_asString(sky_asString(" vs ") + sky_asString(nameB)))) }() }() }() };  if true { return func() any { if sky_asBool(Compiler_Unify_IsUniversalUnifier(nameA)) { return SkyOk(emptySub) }; return SkyErr(sky_asString("Cannot unify ") + sky_asString(sky_asString(formatType(t1)) + sky_asString(sky_asString(" with ") + sky_asString(formatType(t2))))) }() };  return nil }() }()
}

func Compiler_Unify_UnifyFun(fromA any, toA any, t2 any) any {
	return func() any { return func() any { __subject := t2; if sky_asMap(__subject)["SkyName"] == "TVar" { id2 := sky_asMap(__subject)["V0"]; _ = id2; return Compiler_Unify_BindVar(id2, TFun(fromA, toA)) };  if sky_asMap(__subject)["SkyName"] == "TFun" { fromB := sky_asMap(__subject)["V0"]; _ = fromB; toB := sky_asMap(__subject)["V1"]; _ = toB; return func() any { return func() any { __subject := Compiler_Unify_Unify(fromA, fromB); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s1 := sky_asSkyResult(__subject).OkValue; _ = s1; return func() any { return func() any { __subject := Compiler_Unify_Unify(applySub(s1, toA), applySub(s1, toB)); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s2 := sky_asSkyResult(__subject).OkValue; _ = s2; return SkyOk(composeSubs(s2, s1)) };  if sky_asMap(__subject)["SkyName"] == "TConst" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { if sky_asBool(Compiler_Unify_IsUniversalUnifier(name)) { return SkyOk(emptySub) }; return SkyErr(sky_asString("Cannot unify ") + sky_asString(sky_asString(formatType(TFun(fromA, toA))) + sky_asString(sky_asString(" with ") + sky_asString(formatType(t2))))) }() };  if true { return SkyErr(sky_asString("Cannot unify ") + sky_asString(sky_asString(formatType(TFun(fromA, toA))) + sky_asString(sky_asString(" with ") + sky_asString(formatType(t2))))) };  return nil }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Unify_UnifyApp(ctorA any, argsA any, t2 any) any {
	return func() any { return func() any { __subject := t2; if sky_asMap(__subject)["SkyName"] == "TVar" { id2 := sky_asMap(__subject)["V0"]; _ = id2; return Compiler_Unify_BindVar(id2, TApp(ctorA, argsA)) };  if sky_asMap(__subject)["SkyName"] == "TApp" { ctorB := sky_asMap(__subject)["V0"]; _ = ctorB; argsB := sky_asMap(__subject)["V1"]; _ = argsB; return func() any { return func() any { __subject := Compiler_Unify_Unify(ctorA, ctorB); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s0 := sky_asSkyResult(__subject).OkValue; _ = s0; return Compiler_Unify_UnifyList(sky_call(sky_listMap(func(x any) any { return applySub(s0, x) }), argsA), sky_call(sky_listMap(func(x any) any { return applySub(s0, x) }), argsB), s0) };  if sky_asMap(__subject)["SkyName"] == "TConst" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { if sky_asBool(Compiler_Unify_IsUniversalUnifier(name)) { return SkyOk(emptySub) }; return SkyErr(sky_asString("Cannot unify ") + sky_asString(sky_asString(formatType(TApp(ctorA, argsA))) + sky_asString(sky_asString(" with ") + sky_asString(formatType(t2))))) }() };  if true { return SkyErr(sky_asString("Cannot unify ") + sky_asString(sky_asString(formatType(TApp(ctorA, argsA))) + sky_asString(sky_asString(" with ") + sky_asString(formatType(t2))))) };  return nil }() }() };  return nil }() }()
}

func Compiler_Unify_UnifyTuple(itemsA any, t2 any) any {
	return func() any { return func() any { __subject := t2; if sky_asMap(__subject)["SkyName"] == "TVar" { id2 := sky_asMap(__subject)["V0"]; _ = id2; return Compiler_Unify_BindVar(id2, TTuple(itemsA)) };  if sky_asMap(__subject)["SkyName"] == "TTuple" { itemsB := sky_asMap(__subject)["V0"]; _ = itemsB; return func() any { if sky_asBool(!sky_equal(sky_listLength(itemsA), sky_listLength(itemsB))) { return SkyErr(sky_asString("Tuple arity mismatch: ") + sky_asString(sky_asString(sky_stringFromInt(sky_listLength(itemsA))) + sky_asString(sky_asString(" vs ") + sky_asString(sky_stringFromInt(sky_listLength(itemsB)))))) }; return Compiler_Unify_UnifyList(itemsA, itemsB, emptySub) }() };  if sky_asMap(__subject)["SkyName"] == "TConst" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { if sky_asBool(Compiler_Unify_IsUniversalUnifier(name)) { return SkyOk(emptySub) }; return SkyErr(sky_asString("Cannot unify ") + sky_asString(sky_asString(formatType(TTuple(itemsA))) + sky_asString(sky_asString(" with ") + sky_asString(formatType(t2))))) }() };  if true { return SkyErr(sky_asString("Cannot unify ") + sky_asString(sky_asString(formatType(TTuple(itemsA))) + sky_asString(sky_asString(" with ") + sky_asString(formatType(t2))))) };  return nil }() }()
}

func Compiler_Unify_UnifyRecord(fieldsA any, t2 any) any {
	return func() any { return func() any { __subject := t2; if sky_asMap(__subject)["SkyName"] == "TVar" { id2 := sky_asMap(__subject)["V0"]; _ = id2; return Compiler_Unify_BindVar(id2, TRecord(fieldsA)) };  if sky_asMap(__subject)["SkyName"] == "TRecord" { fieldsB := sky_asMap(__subject)["V0"]; _ = fieldsB; return Compiler_Unify_UnifyRecords(fieldsA, fieldsB) };  if sky_asMap(__subject)["SkyName"] == "TConst" { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { if sky_asBool(Compiler_Unify_IsUniversalUnifier(name)) { return SkyOk(emptySub) }; return SkyErr(sky_asString("Cannot unify ") + sky_asString(sky_asString(formatType(TRecord(fieldsA))) + sky_asString(sky_asString(" with ") + sky_asString(formatType(t2))))) }() };  if true { return SkyErr(sky_asString("Cannot unify ") + sky_asString(sky_asString(formatType(TRecord(fieldsA))) + sky_asString(sky_asString(" with ") + sky_asString(formatType(t2))))) };  return nil }() }()
}

func Compiler_Unify_BindVar(id any, t any) any {
	return func() any { return func() any { __subject := t; if sky_asMap(__subject)["SkyName"] == "TVar" { otherId := sky_asMap(__subject)["V0"]; _ = otherId; return func() any { if sky_asBool(sky_equal(id, otherId)) { return SkyOk(emptySub) }; return SkyOk(sky_call(dict.singleton(id), t)) }() };  if true { return func() any { if sky_asBool(sky_call(sky_setMember(id), freeVars(t))) { return SkyErr(sky_asString("Infinite type: t") + sky_asString(sky_asString(sky_stringFromInt(id)) + sky_asString(sky_asString(" occurs in ") + sky_asString(formatType(t))))) }; return SkyOk(sky_call(dict.singleton(id), t)) }() };  return nil }() }()
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
	return func() any { return func() any { __subject := keys; if len(sky_asList(__subject)) == 0 { return SkyOk(sub) };  if len(sky_asList(__subject)) > 0 { key := sky_asList(__subject)[0]; _ = key; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { valA := sky_call(sky_dictGet(key), fieldsA); _ = valA; valB := sky_call(sky_dictGet(key), fieldsB); _ = valB; return func() any { return func() any { __subject := valA; if sky_asSkyMaybe(__subject).SkyName == "Just" { typeA := sky_asSkyMaybe(__subject).JustValue; _ = typeA; return func() any { return func() any { __subject := valB; if sky_asSkyMaybe(__subject).SkyName == "Just" { typeB := sky_asSkyMaybe(__subject).JustValue; _ = typeB; return func() any { return func() any { __subject := Compiler_Unify_Unify(applySub(sub, typeA), applySub(sub, typeB)); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("In record field '") + sky_asString(sky_asString(key) + sky_asString(sky_asString("': ") + sky_asString(e)))) };  if sky_asSkyResult(__subject).SkyName == "Ok" { s := sky_asSkyResult(__subject).OkValue; _ = s; return Compiler_Unify_UnifyRecordFields(rest, fieldsA, fieldsB, composeSubs(s, sub)) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return Compiler_Unify_UnifyRecordFields(rest, fieldsA, fieldsB, sub) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return Compiler_Unify_UnifyRecordFields(rest, fieldsA, fieldsB, sub) };  return nil }() }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }()
}

var ResolveProject = Compiler_Resolver_ResolveProject

var ResolveImports = Compiler_Resolver_ResolveImports

var ResolveModulePath = Compiler_Resolver_ResolveModulePath

var IsStdlib = Compiler_Resolver_IsStdlib

var CheckAllModules = Compiler_Resolver_CheckAllModules

var CheckModulesLoop = Compiler_Resolver_CheckModulesLoop

var BuildStdlibEnv = Compiler_Resolver_BuildStdlibEnv

func Compiler_Resolver_ResolveProject(entryPath any, srcRoot any) any {
	return func() any { return func() any { __subject := sky_fileRead(entryPath); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Cannot read entry file: ") + sky_asString(sky_asString(entryPath) + sky_asString(sky_asString(" (") + sky_asString(sky_asString(e) + sky_asString(")"))))) };  if sky_asSkyResult(__subject).SkyName == "Ok" { source := sky_asSkyResult(__subject).OkValue; _ = source; return func() any { lexResult := Compiler_Lexer_Lex(source); _ = lexResult; return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Parse error in ") + sky_asString(sky_asString(entryPath) + sky_asString(sky_asString(": ") + sky_asString(e)))) };  if sky_asSkyResult(__subject).SkyName == "Ok" { entryMod := sky_asSkyResult(__subject).OkValue; _ = entryMod; return func() any { entryName := sky_call(sky_stringJoin("."), sky_asMap(entryMod)["name"]); _ = entryName; entryLoaded := map[string]any{"name": entryName, "qualifiedName": sky_asMap(entryMod)["name"], "filePath": entryPath, "ast": entryMod, "checkResult": SkyNothing()}; _ = entryLoaded; __tup_allModules_diags := Compiler_Resolver_ResolveImports(srcRoot, sky_asMap(entryMod)["imports"], []any{entryLoaded}, sky_setEmpty, sky_listReverse([]any{})); allModules := sky_asTuple2(__tup_allModules_diags).V0; _ = allModules; diags := sky_asTuple2(__tup_allModules_diags).V1; _ = diags; order := sky_call(sky_listMap(func(m any) any { return sky_asMap(m)["name"] }), sky_listReverse(allModules)); _ = order; return SkyOk(map[string]any{"modules": sky_listReverse(allModules), "order": order, "diagnostics": diags}) }() };  return nil }() }() }() };  return nil }() }()
}

func Compiler_Resolver_ResolveImports(srcRoot any, imports any, loaded any, visited any, diagnostics any) any {
	return func() any { return func() any { __subject := imports; if len(sky_asList(__subject)) == 0 { return []any{} };  if len(sky_asList(__subject)) > 0 { imp := sky_asList(__subject)[0]; _ = imp; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { modName := sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]); _ = modName; return func() any { if sky_asBool(sky_asBool(sky_call(sky_setMember(modName), visited)) || sky_asBool(Compiler_Resolver_IsStdlib(modName))) { return Compiler_Resolver_ResolveImports(srcRoot, rest, loaded, visited, diagnostics) }; return func() any { filePath := Compiler_Resolver_ResolveModulePath(srcRoot, sky_asMap(imp)["moduleName"]); _ = filePath; return func() any { return func() any { __subject := sky_fileRead(filePath); if sky_asSkyResult(__subject).SkyName == "Err" { return Compiler_Resolver_ResolveImports(srcRoot, rest, loaded, sky_call(sky_setInsert(modName), visited), sky_asString(diagnostics) + sky_asString([]any{sky_asString("Module not found: ") + sky_asString(sky_asString(modName) + sky_asString(sky_asString(" (looked at ") + sky_asString(sky_asString(filePath) + sky_asString(")"))))})) };  if sky_asSkyResult(__subject).SkyName == "Ok" { source := sky_asSkyResult(__subject).OkValue; _ = source; return func() any { lexResult := Compiler_Lexer_Lex(source); _ = lexResult; return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return Compiler_Resolver_ResolveImports(srcRoot, rest, loaded, sky_call(sky_setInsert(modName), visited), sky_asString(diagnostics) + sky_asString([]any{sky_asString("Parse error in ") + sky_asString(sky_asString(modName) + sky_asString(sky_asString(": ") + sky_asString(e)))})) };  if sky_asSkyResult(__subject).SkyName == "Ok" { modAst := sky_asSkyResult(__subject).OkValue; _ = modAst; return func() any { newLoaded := map[string]any{"name": modName, "qualifiedName": sky_asMap(imp)["moduleName"], "filePath": filePath, "ast": modAst, "checkResult": SkyNothing()}; _ = newLoaded; newVisited := sky_call(sky_setInsert(modName), visited); _ = newVisited; __tup_withDeps_depDiags := Compiler_Resolver_ResolveImports(srcRoot, sky_asMap(modAst)["imports"], append([]any{newLoaded}, sky_asList(loaded)...), newVisited, diagnostics); withDeps := sky_asTuple2(__tup_withDeps_depDiags).V0; _ = withDeps; depDiags := sky_asTuple2(__tup_withDeps_depDiags).V1; _ = depDiags; return Compiler_Resolver_ResolveImports(srcRoot, rest, withDeps, newVisited, depDiags) }() };  return nil }() }() }() };  return nil }() }() }() }() }() };  return nil }() }()
}

func Compiler_Resolver_ResolveModulePath(srcRoot any, parts any) any {
	return sky_asString(srcRoot) + sky_asString(sky_asString("/") + sky_asString(sky_asString(sky_call(sky_stringJoin("/"), parts)) + sky_asString(".sky")))
}

func Compiler_Resolver_IsStdlib(modName any) any {
	return sky_asBool(sky_call(sky_stringStartsWith("Sky.Core."), modName)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Std."), modName)) || sky_asBool(sky_asBool(sky_equal(modName, "Sky.Core.Prelude")) || sky_asBool(sky_equal(modName, "Sky.Interop"))))
}

func Compiler_Resolver_CheckAllModules(graph any) any {
	return func() any { __tup_checkedModules_diagnostics := Compiler_Resolver_CheckModulesLoop(sky_asMap(graph)["modules"], Compiler_Env_Empty(), []any{}); checkedModules := sky_asTuple2(__tup_checkedModules_diagnostics).V0; _ = checkedModules; diagnostics := sky_asTuple2(__tup_checkedModules_diagnostics).V1; _ = diagnostics; return SkyOk(sky_recordUpdate(graph, map[string]any{"modules": checkedModules, "diagnostics": sky_call(sky_listAppend(sky_asMap(graph)["diagnostics"]), diagnostics)})) }()
}

func Compiler_Resolver_CheckModulesLoop(modules any, importedEnv any, diagnostics any) any {
	return func() any { return func() any { __subject := modules; if len(sky_asList(__subject)) == 0 { return []any{} };  if len(sky_asList(__subject)) > 0 { mod := sky_asList(__subject)[0]; _ = mod; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := Compiler_Checker_CheckModule(sky_asMap(mod)["ast"], SkyJust(importedEnv)); if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return func() any { checkedMod := sky_recordUpdate(mod, map[string]any{"checkResult": SkyJust(result)}); _ = checkedMod; newImportedEnv := Compiler_Env_Union(sky_asMap(result)["env"], importedEnv); _ = newImportedEnv; __tup_restModules_restDiags := Compiler_Resolver_CheckModulesLoop(rest, newImportedEnv, sky_call(sky_listAppend(diagnostics), sky_asMap(result)["diagnostics"])); restModules := sky_asTuple2(__tup_restModules_restDiags).V0; _ = restModules; restDiags := sky_asTuple2(__tup_restModules_restDiags).V1; _ = restDiags; return []any{} }() };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { __tup_restModules_restDiags := Compiler_Resolver_CheckModulesLoop(rest, importedEnv, sky_call(sky_listAppend(diagnostics), []any{e})); restModules := sky_asTuple2(__tup_restModules_restDiags).V0; _ = restModules; restDiags := sky_asTuple2(__tup_restModules_restDiags).V1; _ = restDiags; return []any{} }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Resolver_BuildStdlibEnv() any {
	return func() any { prelude := Compiler_Env_CreatePreludeEnv(); _ = prelude; stdFunctions := sky_dictFromList([]any{[]any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}, []any{}}); _ = stdFunctions; return Compiler_Env_Union(stdFunctions, prelude) }()
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

var MatchingDecl = Lsp_Server_MatchingDecl

var TypeStr = Lsp_Server_TypeStr

var Content = Lsp_Server_Content

var LookupDeclType = Lsp_Server_LookupDeclType

var FindTypeInResults = Lsp_Server_FindTypeInResults

var FindTypeInDecls = Lsp_Server_FindTypeInDecls

var HandleCompletion = Lsp_Server_HandleCompletion

var MakeCompletionItem = Lsp_Server_MakeCompletionItem

var HandleDefinition = Lsp_Server_HandleDefinition

var Matching = Lsp_Server_Matching

var HandleFormatting = Lsp_Server_HandleFormatting

func Lsp_Server_EmptyState() any {
	return map[string]any{"documents": sky_dictEmpty, "astCache": sky_dictEmpty, "typeCache": sky_dictEmpty}
}

func Lsp_Server_StartServer(_ any) any {
	return func() any { stateRef := sky_refNew(Lsp_Server_EmptyState); _ = stateRef; return Lsp_Server_ServerLoop(stateRef) }()
}

func Lsp_Server_ServerLoop(stateRef any) any {
	return func() any { return func() any { __subject := readMessage(struct{}{}); if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return struct{}{} };  if sky_asSkyMaybe(__subject).SkyName == "Just" { body := sky_asSkyMaybe(__subject).JustValue; _ = body; return func() any { state := sky_refGet(stateRef); _ = state; method := jsonGetString("method", body); _ = method; id := jsonGetString("id", body); _ = id; newState := Lsp_Server_HandleMessage(state, id, method, body); _ = newState; sky_call(sky_refSet(newState), stateRef); return Lsp_Server_ServerLoop(stateRef) }() };  return nil }() }()
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
	return func() any { capabilities := jsonObject([]any{[]any{}, []any{}, []any{}, []any{}, []any{}}); _ = capabilities; result := jsonObject([]any{[]any{}, []any{}}); _ = result; return Lsp_Server_SendAndReturn(makeResponse(id, result), state) }()
}

func Lsp_Server_HandleDidOpen(state any, body any) any {
	return func() any { params := jsonGetObject("params", body); _ = params; textDoc := jsonGetObject("textDocument", params); _ = textDoc; uri := jsonGetString("uri", textDoc); _ = uri; text := jsonGetString("text", textDoc); _ = text; newDocs := sky_call(sky_call(sky_dictInsert(uri), text), sky_asMap(state)["documents"]); _ = newDocs; newState := sky_recordUpdate(state, map[string]any{"documents": newDocs}); _ = newState; analyzed := Lsp_Server_AnalyzeAndPublishDiagnostics(newState, uri, text); _ = analyzed; return analyzed }()
}

func Lsp_Server_HandleDidChange(state any, body any) any {
	return func() any { params := jsonGetObject("params", body); _ = params; textDoc := jsonGetObject("textDocument", params); _ = textDoc; uri := jsonGetString("uri", textDoc); _ = uri; changes := jsonGetString("text", jsonGetObject("contentChanges", params)); _ = changes; text := func() any { if sky_asBool(sky_stringIsEmpty(changes)) { return func() any { return func() any { __subject := sky_call(sky_dictGet(uri), sky_asMap(state)["documents"]); if sky_asSkyMaybe(__subject).SkyName == "Just" { existing := sky_asSkyMaybe(__subject).JustValue; _ = existing; return existing };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "" };  return nil }() }() }; return changes }(); _ = text; newDocs := sky_call(sky_call(sky_dictInsert(uri), text), sky_asMap(state)["documents"]); _ = newDocs; newState := sky_recordUpdate(state, map[string]any{"documents": newDocs}); _ = newState; analyzed := Lsp_Server_AnalyzeAndPublishDiagnostics(newState, uri, text); _ = analyzed; return analyzed }()
}

func Lsp_Server_AnalyzeAndPublishDiagnostics(state any, uri any, text any) any {
	return func() any { lexResult := Compiler_Lexer_Lex(text); _ = lexResult; parseResult := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); _ = parseResult; return func() any { return func() any { __subject := parseResult; if sky_asSkyResult(__subject).SkyName == "Err" { parseErr := sky_asSkyResult(__subject).ErrValue; _ = parseErr; return func() any { diag := jsonObject([]any{[]any{}, []any{}, []any{}}); _ = diag; return Lsp_Server_SendNotifyAndReturn(makeNotification("textDocument/publishDiagnostics", jsonObject([]any{[]any{}, []any{}})), state) }() };  if sky_asSkyResult(__subject).SkyName == "Ok" { mod := sky_asSkyResult(__subject).OkValue; _ = mod; return func() any { stdlibEnv := Compiler_Resolver_BuildStdlibEnv(); _ = stdlibEnv; checkResult := Compiler_Checker_CheckModule(mod, SkyJust(stdlibEnv)); _ = checkResult; diagnostics := func() any { return func() any { __subject := checkResult; if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return sky_call(sky_listMap(func(msg any) any { return Lsp_Server_MakeDiagnostic(2, msg) }), sky_asMap(result)["diagnostics"]) };  if sky_asSkyResult(__subject).SkyName == "Err" { err := sky_asSkyResult(__subject).ErrValue; _ = err; return []any{Lsp_Server_MakeDiagnostic(1, err)} };  return nil }() }(); _ = diagnostics; return Lsp_Server_PublishAndUpdateState(state, uri, mod, checkResult, diagnostics) }() };  return nil }() }() }()
}

func Lsp_Server_PublishAndUpdateState(state any, uri any, mod any, checkResult any, diagnostics any) any {
	return func() any { writeMessage(makeNotification("textDocument/publishDiagnostics", jsonObject([]any{[]any{}, []any{}}))); newAstCache := sky_call(sky_call(sky_dictInsert(uri), mod), sky_asMap(state)["astCache"]); _ = newAstCache; newTypeCache := func() any { return func() any { __subject := checkResult; if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return sky_call(sky_call(sky_dictInsert(uri), result), sky_asMap(state)["typeCache"]) };  if sky_asSkyResult(__subject).SkyName == "Err" { return sky_asMap(state)["typeCache"] };  return nil }() }(); _ = newTypeCache; return sky_recordUpdate(state, map[string]any{"astCache": newAstCache, "typeCache": newTypeCache}) }()
}

func Lsp_Server_MakeDiagnostic(severity any, msg any) any {
	return jsonObject([]any{[]any{}, []any{}, []any{}})
}

func Lsp_Server_MakeRange(startLine any, startChar any, endLine any, endChar any) any {
	return jsonObject([]any{[]any{}, []any{}})
}

func Lsp_Server_HandleHover(state any, id any, body any) any {
	return func() any { params := jsonGetObject("params", body); _ = params; textDoc := jsonGetObject("textDocument", params); _ = textDoc; uri := jsonGetString("uri", textDoc); _ = uri; position := jsonGetObject("position", params); _ = position; line := jsonGetInt("line", position); _ = line; character := jsonGetInt("character", position); _ = character; hoverResult := func() any { return func() any { __subject := sky_call(sky_dictGet(uri), sky_asMap(state)["astCache"]); if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return jsonNull };  if sky_asSkyMaybe(__subject).SkyName == "Just" { mod := sky_asSkyMaybe(__subject).JustValue; _ = mod; return getHoverInfo(state, mod, line, character) };  return nil }() }(); _ = hoverResult; return Lsp_Server_SendAndReturn(makeResponse(id, hoverResult), state) }()
}

func Lsp_Server_MatchingDecl() any {
	return sky_listFoldl
}

func Lsp_Server_TypeStr() any {
	return Lsp_Server_LookupDeclType(state, name)
}

func Lsp_Server_Content() any {
	return sky_asString("```elm\\n") + sky_asString(sky_asString(name) + sky_asString(sky_asString(" : ") + sky_asString(sky_asString(Lsp_Server_TypeStr) + sky_asString("\\n```"))))
}

func Lsp_Server_LookupDeclType(state any, name any) any {
	return func() any { allResults := sky_dictValues(sky_asMap(state)["typeCache"]); _ = allResults; return Lsp_Server_FindTypeInResults(name, allResults) }()
}

func Lsp_Server_FindTypeInResults(name any, results any) any {
	return func() any { return func() any { __subject := results; if len(sky_asList(__subject)) == 0 { return "unknown" };  if len(sky_asList(__subject)) > 0 { result := sky_asList(__subject)[0]; _ = result; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := Lsp_Server_FindTypeInDecls(name, sky_asMap(result)["declarations"]); if sky_asSkyMaybe(__subject).SkyName == "Just" { typeStr := sky_asSkyMaybe(__subject).JustValue; _ = typeStr; return Lsp_Server_TypeStr };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return Lsp_Server_FindTypeInResults(name, rest) };  return nil }() }() };  return nil }() }()
}

func Lsp_Server_FindTypeInDecls(name any, decls any) any {
	return func() any { return func() any { __subject := decls; if len(sky_asList(__subject)) == 0 { return SkyNothing() };  if len(sky_asList(__subject)) > 0 { decl := sky_asList(__subject)[0]; _ = decl; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { if sky_asBool(sky_equal(sky_asMap(decl)["name"], name)) { return SkyJust(sky_asMap(decl)["prettyType"]) }; return Lsp_Server_FindTypeInDecls(name, rest) }() };  return nil }() }()
}

func Lsp_Server_HandleCompletion(state any, id any, body any) any {
	return func() any { keywords := []any{"module", "exposing", "import", "as", "type", "alias", "let", "in", "if", "then", "else", "case", "of", "foreign", "True", "False"}; _ = keywords; keywordItems := sky_call(sky_listMap(func(kw any) any { return jsonObject([]any{[]any{}, []any{}}) }), keywords); _ = keywordItems; stdlibItems := []any{Lsp_Server_MakeCompletionItem("println", "function", 3), Lsp_Server_MakeCompletionItem("identity", "function", 3), Lsp_Server_MakeCompletionItem("not", "function", 3), Lsp_Server_MakeCompletionItem("always", "function", 3), Lsp_Server_MakeCompletionItem("fst", "function", 3), Lsp_Server_MakeCompletionItem("snd", "function", 3), Lsp_Server_MakeCompletionItem("Ok", "constructor", 4), Lsp_Server_MakeCompletionItem("Err", "constructor", 4), Lsp_Server_MakeCompletionItem("Just", "constructor", 4), Lsp_Server_MakeCompletionItem("Nothing", "constructor", 4)}; _ = stdlibItems; allItems := sky_call(sky_listAppend(keywordItems), stdlibItems); _ = allItems; result := jsonObject([]any{[]any{}, []any{}}); _ = result; return Lsp_Server_SendAndReturn(makeResponse(id, result), state) }()
}

func Lsp_Server_MakeCompletionItem(label any, detail any, kind any) any {
	return jsonObject([]any{[]any{}, []any{}, []any{}})
}

func Lsp_Server_HandleDefinition(state any, id any, body any) any {
	return func() any { params := jsonGetObject("params", body); _ = params; textDoc := jsonGetObject("textDocument", params); _ = textDoc; uri := jsonGetString("uri", textDoc); _ = uri; position := jsonGetObject("position", params); _ = position; line := jsonGetInt("line", position); _ = line; character := jsonGetInt("character", position); _ = character; defResult := func() any { return func() any { __subject := sky_call(sky_dictGet(uri), sky_asMap(state)["astCache"]); if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return jsonNull };  if sky_asSkyMaybe(__subject).SkyName == "Just" { mod := sky_asSkyMaybe(__subject).JustValue; _ = mod; return findDefinition(mod, uri, line, character) };  return nil }() }(); _ = defResult; return Lsp_Server_SendAndReturn(makeResponse(id, defResult), state) }()
}

func Lsp_Server_Matching() any {
	return sky_listFoldl
}

func Lsp_Server_HandleFormatting(state any, id any, body any) any {
	return Lsp_Server_SendAndReturn(makeResponse(id, jsonArray([]any{})), state)
}

var ReadMessage = Lsp_JsonRpc_ReadMessage

var ReadMessageBody = Lsp_JsonRpc_ReadMessageBody

var ParseContentLength = Lsp_JsonRpc_ParseContentLength

var WriteMessage = Lsp_JsonRpc_WriteMessage

var MakeResponse = Lsp_JsonRpc_MakeResponse

var MakeNotification = Lsp_JsonRpc_MakeNotification

var JsonString = Lsp_JsonRpc_JsonString

var JsonInt = Lsp_JsonRpc_JsonInt

var JsonBool = Lsp_JsonRpc_JsonBool

var JsonNull = Lsp_JsonRpc_JsonNull

var JsonObject = Lsp_JsonRpc_JsonObject

var FormatField = Lsp_JsonRpc_FormatField

var JsonArray = Lsp_JsonRpc_JsonArray

var EscapeJson = Lsp_JsonRpc_EscapeJson

var JsonGetString = Lsp_JsonRpc_JsonGetString

var JsonGetInt = Lsp_JsonRpc_JsonGetInt

var JsonGetObject = Lsp_JsonRpc_JsonGetObject

var FindInString = Lsp_JsonRpc_FindInString

var ExtractQuotedString = Lsp_JsonRpc_ExtractQuotedString

var TakeWhileDigit = Lsp_JsonRpc_TakeWhileDigit

var TakeUntilDelimiter = Lsp_JsonRpc_TakeUntilDelimiter

var ExtractBraced = Lsp_JsonRpc_ExtractBraced

func Lsp_JsonRpc_ReadMessage(_ any) any {
	return func() any { return func() any { __subject := sky_readLine(struct{}{}); if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyNothing() };  if sky_asSkyMaybe(__subject).SkyName == "Just" { headerLine := sky_asSkyMaybe(__subject).JustValue; _ = headerLine; return func() any { contentLength := Lsp_JsonRpc_ParseContentLength(headerLine); _ = contentLength; return func() any { if sky_asBool(sky_asInt(contentLength) <= sky_asInt(0)) { return SkyNothing() }; return Lsp_JsonRpc_ReadMessageBody(contentLength) }() }() };  return nil }() }()
}

func Lsp_JsonRpc_ReadMessageBody(len any) any {
	return func() any { return func() any { __subject := sky_readLine(struct{}{}); if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyNothing() };  if sky_asSkyMaybe(__subject).SkyName == "Just" { return sky_readBytes(len) };  return nil }() }()
}

func Lsp_JsonRpc_ParseContentLength(header any) any {
	return func() any { if sky_asBool(sky_call(sky_stringStartsWith("Content-Length: "), header)) { return func() any { raw := sky_call(sky_call(sky_stringSlice(16), sky_stringLength(header)), header); _ = raw; numStr := sky_call(sky_call(sky_stringReplace("\r"), ""), sky_stringTrim(raw)); _ = numStr; return func() any { return func() any { __subject := sky_stringToInt(numStr); if sky_asSkyMaybe(__subject).SkyName == "Just" { n := sky_asSkyMaybe(__subject).JustValue; _ = n; return n };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return 0 };  return nil }() }() }() }; return 0 }()
}

func Lsp_JsonRpc_WriteMessage(json any) any {
	return func() any { len := sky_stringLength(json); _ = len; header := sky_asString("Content-Length: ") + sky_asString(sky_asString(sky_stringFromInt(len)) + sky_asString("\r\n\r\n")); _ = header; return sky_writeStdout(sky_asString(header) + sky_asString(json)) }()
}

func Lsp_JsonRpc_MakeResponse(id any, resultJson any) any {
	return sky_asString("{\"jsonrpc\":\"2.0\",\"id\":") + sky_asString(sky_asString(id) + sky_asString(sky_asString(",\"result\":") + sky_asString(sky_asString(resultJson) + sky_asString("}"))))
}

func Lsp_JsonRpc_MakeNotification(method any, paramsJson any) any {
	return sky_asString("{\"jsonrpc\":\"2.0\",\"method\":\"") + sky_asString(sky_asString(method) + sky_asString(sky_asString("\",\"params\":") + sky_asString(sky_asString(paramsJson) + sky_asString("}"))))
}

func Lsp_JsonRpc_JsonString(s any) any {
	return sky_asString("\"") + sky_asString(sky_asString(Lsp_JsonRpc_EscapeJson(s)) + sky_asString("\""))
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
	return sky_asString("{") + sky_asString(sky_asString(sky_call(sky_stringJoin(","), sky_call(sky_listMap(Lsp_JsonRpc_FormatField), fields))) + sky_asString("}"))
}

func Lsp_JsonRpc_FormatField(pair any) any {
	return sky_asString(Lsp_JsonRpc_JsonString(sky_fst(pair))) + sky_asString(sky_asString(":") + sky_asString(sky_snd(pair)))
}

func Lsp_JsonRpc_JsonArray(items any) any {
	return sky_asString("[") + sky_asString(sky_asString(sky_call(sky_stringJoin(","), items)) + sky_asString("]"))
}

func Lsp_JsonRpc_EscapeJson(s any) any {
	return sky_call(sky_stringReplace("\\t"), "\\\\t")(sky_call(sky_stringReplace("\\r"), "\\\\r")(sky_call(sky_stringReplace("\\n"), "\\\\n")(sky_call(sky_stringReplace("\\\""), "\\\\\\\"")(sky_call(sky_stringReplace("\\\\"), "\\\\\\\\")(s)))))
}

func Lsp_JsonRpc_JsonGetString(key any, json any) any {
	return func() any { searchKey := sky_asString("\"") + sky_asString(sky_asString(key) + sky_asString("\"")); _ = searchKey; keyIdx := Lsp_JsonRpc_FindInString(searchKey, json, 0); _ = keyIdx; return func() any { if sky_asBool(sky_asInt(keyIdx) < sky_asInt(0)) { return "" }; return func() any { afterKey := sky_call(sky_call(sky_stringSlice(sky_asInt(keyIdx) + sky_asInt(sky_stringLength(searchKey))), sky_stringLength(json)), json); _ = afterKey; colonIdx := Lsp_JsonRpc_FindInString(":", afterKey, 0); _ = colonIdx; return func() any { if sky_asBool(sky_asInt(colonIdx) < sky_asInt(0)) { return "" }; return func() any { afterColon := sky_stringTrim(sky_call(sky_call(sky_stringSlice(sky_asInt(colonIdx) + sky_asInt(1)), sky_stringLength(afterKey)), afterKey)); _ = afterColon; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("\""), afterColon)) { return Lsp_JsonRpc_ExtractQuotedString(sky_call(sky_call(sky_stringSlice(1), sky_stringLength(afterColon)), afterColon), "") }; return Lsp_JsonRpc_TakeUntilDelimiter(afterColon, "") }() }() }() }() }() }()
}

func Lsp_JsonRpc_JsonGetInt(key any, json any) any {
	return func() any { searchKey := sky_asString("\"") + sky_asString(sky_asString(key) + sky_asString("\"")); _ = searchKey; keyIdx := Lsp_JsonRpc_FindInString(searchKey, json, 0); _ = keyIdx; return func() any { if sky_asBool(sky_asInt(keyIdx) < sky_asInt(0)) { return 0 }; return func() any { afterKey := sky_call(sky_call(sky_stringSlice(sky_asInt(keyIdx) + sky_asInt(sky_stringLength(searchKey))), sky_stringLength(json)), json); _ = afterKey; colonIdx := Lsp_JsonRpc_FindInString(":", afterKey, 0); _ = colonIdx; return func() any { if sky_asBool(sky_asInt(colonIdx) < sky_asInt(0)) { return 0 }; return func() any { afterColon := sky_stringTrim(sky_call(sky_call(sky_stringSlice(sky_asInt(colonIdx) + sky_asInt(1)), sky_stringLength(afterKey)), afterKey)); _ = afterColon; numStr := Lsp_JsonRpc_TakeWhileDigit(afterColon, ""); _ = numStr; return func() any { return func() any { __subject := sky_stringToInt(numStr); if sky_asSkyMaybe(__subject).SkyName == "Just" { n := sky_asSkyMaybe(__subject).JustValue; _ = n; return n };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return 0 };  return nil }() }() }() }() }() }() }()
}

func Lsp_JsonRpc_JsonGetObject(key any, json any) any {
	return func() any { searchKey := sky_asString("\"") + sky_asString(sky_asString(key) + sky_asString("\"")); _ = searchKey; keyIdx := Lsp_JsonRpc_FindInString(searchKey, json, 0); _ = keyIdx; return func() any { if sky_asBool(sky_asInt(keyIdx) < sky_asInt(0)) { return "{}" }; return func() any { afterKey := sky_call(sky_call(sky_stringSlice(sky_asInt(keyIdx) + sky_asInt(sky_stringLength(searchKey))), sky_stringLength(json)), json); _ = afterKey; colonIdx := Lsp_JsonRpc_FindInString(":", afterKey, 0); _ = colonIdx; return func() any { if sky_asBool(sky_asInt(colonIdx) < sky_asInt(0)) { return "{}" }; return func() any { afterColon := sky_stringTrim(sky_call(sky_call(sky_stringSlice(sky_asInt(colonIdx) + sky_asInt(1)), sky_stringLength(afterKey)), afterKey)); _ = afterColon; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("{"), afterColon)) { return Lsp_JsonRpc_ExtractBraced(afterColon, 0, 0) }; return "{}" }() }() }() }() }() }()
}

func Lsp_JsonRpc_FindInString(needle any, haystack any, idx any) any {
	return func() any { if sky_asBool(sky_asInt(sky_asInt(idx) + sky_asInt(sky_stringLength(needle))) > sky_asInt(sky_stringLength(haystack))) { return -1 }; return func() any { if sky_asBool(sky_equal(sky_call(sky_call(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(sky_stringLength(needle))), haystack), needle)) { return idx }; return Lsp_JsonRpc_FindInString(needle, haystack, sky_asInt(idx) + sky_asInt(1)) }() }()
}

func Lsp_JsonRpc_ExtractQuotedString(remaining any, acc any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(remaining)) { return acc }; return func() any { ch := sky_call(sky_call(sky_stringSlice(0), 1), remaining); _ = ch; rest := sky_call(sky_call(sky_stringSlice(1), sky_stringLength(remaining)), remaining); _ = rest; return func() any { if sky_asBool(sky_equal(ch, "\"")) { return acc }; return func() any { if sky_asBool(sky_equal(ch, "\\")) { return Lsp_JsonRpc_ExtractQuotedString(sky_call(sky_call(sky_stringSlice(1), sky_stringLength(rest)), rest), sky_asString(acc) + sky_asString(sky_call(sky_call(sky_stringSlice(0), 1), rest))) }; return Lsp_JsonRpc_ExtractQuotedString(rest, sky_asString(acc) + sky_asString(ch)) }() }() }() }()
}

func Lsp_JsonRpc_TakeWhileDigit(remaining any, acc any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(remaining)) { return acc }; return func() any { ch := sky_call(sky_call(sky_stringSlice(0), 1), remaining); _ = ch; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, "0")) || sky_asBool(sky_asBool(sky_equal(ch, "1")) || sky_asBool(sky_asBool(sky_equal(ch, "2")) || sky_asBool(sky_asBool(sky_equal(ch, "3")) || sky_asBool(sky_asBool(sky_equal(ch, "4")) || sky_asBool(sky_asBool(sky_equal(ch, "5")) || sky_asBool(sky_asBool(sky_equal(ch, "6")) || sky_asBool(sky_asBool(sky_equal(ch, "7")) || sky_asBool(sky_asBool(sky_equal(ch, "8")) || sky_asBool(sky_equal(ch, "9"))))))))))) { return Lsp_JsonRpc_TakeWhileDigit(sky_call(sky_call(sky_stringSlice(1), sky_stringLength(remaining)), remaining), sky_asString(acc) + sky_asString(ch)) }; return acc }() }() }()
}

func Lsp_JsonRpc_TakeUntilDelimiter(remaining any, acc any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(remaining)) { return acc }; return func() any { ch := sky_call(sky_call(sky_stringSlice(0), 1), remaining); _ = ch; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, ",")) || sky_asBool(sky_asBool(sky_equal(ch, "}")) || sky_asBool(sky_asBool(sky_equal(ch, "]")) || sky_asBool(sky_asBool(sky_equal(ch, " ")) || sky_asBool(sky_asBool(sky_equal(ch, "\n")) || sky_asBool(sky_equal(ch, "\r"))))))) { return acc }; return Lsp_JsonRpc_TakeUntilDelimiter(sky_call(sky_call(sky_stringSlice(1), sky_stringLength(remaining)), remaining), sky_asString(acc) + sky_asString(ch)) }() }() }()
}

func Lsp_JsonRpc_ExtractBraced(remaining any, depth any, idx any) any {
	return func() any { if sky_asBool(sky_asInt(idx) >= sky_asInt(sky_stringLength(remaining))) { return remaining }; return func() any { ch := sky_call(sky_call(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1)), remaining); _ = ch; return func() any { if sky_asBool(sky_equal(ch, "{")) { return Lsp_JsonRpc_ExtractBraced(remaining, sky_asInt(depth) + sky_asInt(1), sky_asInt(idx) + sky_asInt(1)) }; return func() any { if sky_asBool(sky_equal(ch, "}")) { return func() any { if sky_asBool(sky_asInt(depth) <= sky_asInt(1)) { return sky_call(sky_call(sky_stringSlice(0), sky_asInt(idx) + sky_asInt(1)), remaining) }; return Lsp_JsonRpc_ExtractBraced(remaining, sky_asInt(depth) - sky_asInt(1), sky_asInt(idx) + sky_asInt(1)) }() }; return Lsp_JsonRpc_ExtractBraced(remaining, depth, sky_asInt(idx) + sky_asInt(1)) }() }() }() }()
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

func Formatter_Format_FormatModule(mod any) any {
	return func() any { header := Formatter_Format_FormatModuleHeader(mod); _ = header; importDocs := sky_call(sky_listMap(Formatter_Format_FormatImport), sky_asMap(mod)["imports"]); _ = importDocs; declDocs := Formatter_Format_FormatDeclarations(sky_asMap(mod)["declarations"]); _ = declDocs; allDocs := func() any { if sky_asBool(sky_listIsEmpty(sky_asMap(mod)["imports"])) { return concat([]any{header, hardline, hardline, declDocs}) }; return concat([]any{header, hardline, hardline, concat(sky_call(sky_listMap(func(d any) any { return concat([]any{d, hardline}) }), importDocs)), hardline, declDocs}) }(); _ = allDocs; return sky_asString(sky_stringTrim(render(allDocs))) + sky_asString("\n") }()
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
	return func() any { paramStr := func() any { if sky_asBool(sky_listIsEmpty(params)) { return "" }; return sky_asString(" ") + sky_asString(sky_call(sky_stringJoin(" "), params)) }(); _ = paramStr; header := text(sky_asString("type ") + sky_asString(sky_asString(name) + sky_asString(paramStr))); _ = header; return func() any { return func() any { __subject := variants; if len(sky_asList(__subject)) == 0 { return header };  if len(sky_asList(__subject)) > 0 { first := sky_asList(__subject)[0]; _ = first; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { firstDoc := concat([]any{text("= "), Formatter_Format_FormatVariant(first)}); _ = firstDoc; restDocs := sky_call(sky_listMap(func(v any) any { return concat([]any{text("| "), Formatter_Format_FormatVariant(v)}) }), rest); _ = restDocs; return concat([]any{header, indent(concat([]any{hardline, joinDocs(append([]any{firstDoc}, sky_asList(restDocs)...), hardline)}))}) }() };  return nil }() }() }()
}

func Formatter_Format_FormatVariant(variant any) any {
	return func() any { if sky_asBool(sky_listIsEmpty(sky_asMap(variant)["fields"])) { return text(sky_asMap(variant)["name"]) }; return concat([]any{text(sky_asMap(variant)["name"]), text(" "), joinDocs(sky_call(sky_listMap(Formatter_Format_FormatTypeExprParens), sky_asMap(variant)["fields"]), text(" "))}) }()
}

func Formatter_Format_FormatTypeAlias(name any, params any, aliasType any) any {
	return func() any { paramStr := func() any { if sky_asBool(sky_listIsEmpty(params)) { return "" }; return sky_asString(" ") + sky_asString(sky_call(sky_stringJoin(" "), params)) }(); _ = paramStr; return concat([]any{text(sky_asString("type alias ") + sky_asString(sky_asString(name) + sky_asString(sky_asString(paramStr) + sky_asString(" =")))), indent(concat([]any{hardline, Formatter_Format_FormatTypeExpr(aliasType)}))}) }()
}

func Formatter_Format_FormatExpr(expr any) any {
	return func() any { return func() any { __subject := expr; if sky_asMap(__subject)["SkyName"] == "IdentifierExpr" { name := sky_asMap(__subject)["V0"]; _ = name; return text(name) };  if sky_asMap(__subject)["SkyName"] == "QualifiedExpr" { parts := sky_asMap(__subject)["V0"]; _ = parts; return text(sky_call(sky_stringJoin("."), parts)) };  if sky_asMap(__subject)["SkyName"] == "IntLitExpr" { raw := sky_asMap(__subject)["V1"]; _ = raw; return text(raw) };  if sky_asMap(__subject)["SkyName"] == "FloatLitExpr" { raw := sky_asMap(__subject)["V1"]; _ = raw; return text(raw) };  if sky_asMap(__subject)["SkyName"] == "StringLitExpr" { s := sky_asMap(__subject)["V0"]; _ = s; return text(sky_asString("\"") + sky_asString(sky_asString(Formatter_Format_QuoteString(s)) + sky_asString("\""))) };  if sky_asMap(__subject)["SkyName"] == "CharLitExpr" { s := sky_asMap(__subject)["V0"]; _ = s; return text(s) };  if sky_asMap(__subject)["SkyName"] == "BoolLitExpr" { b := sky_asMap(__subject)["V0"]; _ = b; return func() any { if sky_asBool(b) { return text("True") }; return text("False") }() };  if sky_asMap(__subject)["SkyName"] == "UnitExpr" { return text("()") };  if sky_asMap(__subject)["SkyName"] == "TupleExpr" { items := sky_asMap(__subject)["V0"]; _ = items; return Formatter_Format_FormatTuple(items) };  if sky_asMap(__subject)["SkyName"] == "ListExpr" { items := sky_asMap(__subject)["V0"]; _ = items; return Formatter_Format_FormatList(items) };  if sky_asMap(__subject)["SkyName"] == "RecordExpr" { fields := sky_asMap(__subject)["V0"]; _ = fields; return Formatter_Format_FormatRecord(fields) };  if sky_asMap(__subject)["SkyName"] == "RecordUpdateExpr" { base := sky_asMap(__subject)["V0"]; _ = base; fields := sky_asMap(__subject)["V1"]; _ = fields; return Formatter_Format_FormatRecordUpdate(base, fields) };  if sky_asMap(__subject)["SkyName"] == "FieldAccessExpr" { target := sky_asMap(__subject)["V0"]; _ = target; fieldName := sky_asMap(__subject)["V1"]; _ = fieldName; return concat([]any{Formatter_Format_FormatExpr(target), text("."), text(fieldName)}) };  if sky_asMap(__subject)["SkyName"] == "CallExpr" { callee := sky_asMap(__subject)["V0"]; _ = callee; args := sky_asMap(__subject)["V1"]; _ = args; return Formatter_Format_FormatCall(callee, args) };  if sky_asMap(__subject)["SkyName"] == "LambdaExpr" { params := sky_asMap(__subject)["V0"]; _ = params; body := sky_asMap(__subject)["V1"]; _ = body; return Formatter_Format_FormatLambda(params, body) };  if sky_asMap(__subject)["SkyName"] == "IfExpr" { condition := sky_asMap(__subject)["V0"]; _ = condition; thenBranch := sky_asMap(__subject)["V1"]; _ = thenBranch; elseBranch := sky_asMap(__subject)["V2"]; _ = elseBranch; return Formatter_Format_FormatIf(condition, thenBranch, elseBranch) };  if sky_asMap(__subject)["SkyName"] == "LetExpr" { bindings := sky_asMap(__subject)["V0"]; _ = bindings; body := sky_asMap(__subject)["V1"]; _ = body; return Formatter_Format_FormatLet(bindings, body) };  if sky_asMap(__subject)["SkyName"] == "CaseExpr" { subject := sky_asMap(__subject)["V0"]; _ = subject; branches := sky_asMap(__subject)["V1"]; _ = branches; return Formatter_Format_FormatCase(subject, branches) };  if sky_asMap(__subject)["SkyName"] == "BinaryExpr" { op := sky_asMap(__subject)["V0"]; _ = op; leftExpr := sky_asMap(__subject)["V1"]; _ = leftExpr; rightExpr := sky_asMap(__subject)["V2"]; _ = rightExpr; return Formatter_Format_FormatBinary(op, leftExpr, rightExpr) };  if sky_asMap(__subject)["SkyName"] == "NegateExpr" { inner := sky_asMap(__subject)["V0"]; _ = inner; return concat([]any{text("-"), Formatter_Format_FormatExpr(inner)}) };  if sky_asMap(__subject)["SkyName"] == "ParenExpr" { inner := sky_asMap(__subject)["V0"]; _ = inner; return group(concat([]any{text("("), softline, Formatter_Format_FormatExpr(inner), softline, text(")")})) };  return nil }() }()
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
	return group(concat([]any{text("\\\\"), joinDocs(sky_call(sky_listMap(Formatter_Format_FormatPattern), params), text(" ")), text(" ->"), indent(concat([]any{line, Formatter_Format_FormatExpr(body)}))}))
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
	return func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PWildcard" { return text("_") };  if sky_asMap(__subject)["SkyName"] == "PVariable" { name := sky_asMap(__subject)["V0"]; _ = name; return text(name) };  if sky_asMap(__subject)["SkyName"] == "PConstructor" { parts := sky_asMap(__subject)["V0"]; _ = parts; argPats := sky_asMap(__subject)["V1"]; _ = argPats; return func() any { ctorName := sky_call(sky_stringJoin("."), parts); _ = ctorName; return func() any { if sky_asBool(sky_listIsEmpty(argPats)) { return text(ctorName) }; return concat([]any{text(ctorName), text(" "), joinDocs(sky_call(sky_listMap(Formatter_Format_FormatPatternParens), argPats), text(" "))}) }() }() };  if sky_asMap(__subject)["SkyName"] == "PLiteral" { lit := sky_asMap(__subject)["V0"]; _ = lit; return Formatter_Format_FormatLiteral(lit) };  if sky_asMap(__subject)["SkyName"] == "PTuple" { items := sky_asMap(__subject)["V0"]; _ = items; return group(concat([]any{text("( "), joinDocs(sky_call(sky_listMap(Formatter_Format_FormatPattern), items), text(" , ")), text(" )")})) };  if sky_asMap(__subject)["SkyName"] == "PList" { items := sky_asMap(__subject)["V0"]; _ = items; return group(concat([]any{text("[ "), joinDocs(sky_call(sky_listMap(Formatter_Format_FormatPattern), items), text(" , ")), text(" ]")})) };  if sky_asMap(__subject)["SkyName"] == "PCons" { headPat := sky_asMap(__subject)["V0"]; _ = headPat; tailPat := sky_asMap(__subject)["V1"]; _ = tailPat; return concat([]any{Formatter_Format_FormatPattern(headPat), text(" :: "), Formatter_Format_FormatPattern(tailPat)}) };  if sky_asMap(__subject)["SkyName"] == "PAs" { innerPat := sky_asMap(__subject)["V0"]; _ = innerPat; name := sky_asMap(__subject)["V1"]; _ = name; return concat([]any{Formatter_Format_FormatPattern(innerPat), text(" as "), text(name)}) };  if sky_asMap(__subject)["SkyName"] == "PRecord" { fields := sky_asMap(__subject)["V0"]; _ = fields; return text(sky_asString("{ ") + sky_asString(sky_asString(sky_call(sky_stringJoin(" , "), fields)) + sky_asString(" }"))) };  return nil }() }()
}

func Formatter_Format_FormatPatternParens(pat any) any {
	return func() any { return func() any { __subject := pat; if sky_asMap(__subject)["SkyName"] == "PConstructor" { args := sky_asMap(__subject)["V1"]; _ = args; return func() any { if sky_asBool(sky_listIsEmpty(args)) { return Formatter_Format_FormatPattern(pat) }; return concat([]any{text("("), Formatter_Format_FormatPattern(pat), text(")")}) }() };  if sky_asMap(__subject)["SkyName"] == "PTuple" { return Formatter_Format_FormatPattern(pat) };  if true { return Formatter_Format_FormatPattern(pat) };  return nil }() }()
}

func Formatter_Format_FormatLiteral(lit any) any {
	return func() any { return func() any { __subject := lit; if sky_asMap(__subject)["SkyName"] == "LitInt" { n := sky_asMap(__subject)["V0"]; _ = n; return text(sky_stringFromInt(n)) };  if sky_asMap(__subject)["SkyName"] == "LitFloat" { f := sky_asMap(__subject)["V0"]; _ = f; return text(sky_stringFromFloat(f)) };  if sky_asMap(__subject)["SkyName"] == "LitString" { s := sky_asMap(__subject)["V0"]; _ = s; return text(sky_asString("\"") + sky_asString(sky_asString(Formatter_Format_QuoteString(s)) + sky_asString("\""))) };  if sky_asMap(__subject)["SkyName"] == "LitChar" { c := sky_asMap(__subject)["V0"]; _ = c; return text(sky_asString("'") + sky_asString(sky_asString(c) + sky_asString("'"))) };  if sky_asMap(__subject)["SkyName"] == "LitBool" { b := sky_asMap(__subject)["V0"]; _ = b; return func() any { if sky_asBool(b) { return text("True") }; return text("False") }() };  return nil }() }()
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
	return sky_call(sky_stringReplace("\\t"), "\\\\t")(sky_call(sky_stringReplace("\\r"), "\\\\r")(sky_call(sky_stringReplace("\\n"), "\\\\n")(sky_call(sky_stringReplace("\\\""), "\\\\\\\"")(sky_call(sky_stringReplace("\\\\"), "\\\\\\\\")(s)))))
}

var DocText = Formatter_Doc_DocText

var DocLine = Formatter_Doc_DocLine

var DocSoftline = Formatter_Doc_DocSoftline

var DocHardline = Formatter_Doc_DocHardline

var DocConcat = Formatter_Doc_DocConcat

var DocIndent = Formatter_Doc_DocIndent

var DocGroup = Formatter_Doc_DocGroup

var DocAlign = Formatter_Doc_DocAlign

var Text = Formatter_Doc_Text

var Line = Formatter_Doc_Line

var Hardline = Formatter_Doc_Hardline

var Softline = Formatter_Doc_Softline

var Concat = Formatter_Doc_Concat

var Indent = Formatter_Doc_Indent

var Group = Formatter_Doc_Group

var Align = Formatter_Doc_Align

var JoinDocs = Formatter_Doc_JoinDocs

var MaxWidth = Formatter_Doc_MaxWidth

var IndentWidth = Formatter_Doc_IndentWidth

var Render = Formatter_Doc_Render

var WriteStr = Formatter_Doc_WriteStr

var Newline = Formatter_Doc_Newline

var MakeSpaces = Formatter_Doc_MakeSpaces

var FlatWidth = Formatter_Doc_FlatWidth

var Fits = Formatter_Doc_Fits

var FitsConcat = Formatter_Doc_FitsConcat

var Walk = Formatter_Doc_Walk

var WalkParts = Formatter_Doc_WalkParts

var Flatten = Formatter_Doc_Flatten

var FlattenParts = Formatter_Doc_FlattenParts

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
	return map[string]any{"Tag": 6, "SkyName": "DocLine"}
}

func Formatter_Doc_Hardline() any {
	return map[string]any{"Tag": 3, "SkyName": "DocHardline"}
}

func Formatter_Doc_Softline() any {
	return map[string]any{"Tag": 7, "SkyName": "DocSoftline"}
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
	return func() any { sky_call(sky_refSet(sky_asString(sky_refGet(outputRef)) + sky_asString(s)), outputRef); sky_call(sky_refSet(sky_asInt(sky_refGet(colRef)) + sky_asInt(sky_stringLength(s))), colRef); return struct{}{} }()
}

func Formatter_Doc_Newline(outputRef any, colRef any, indentRef any) any {
	return func() any { indentLevel := sky_refGet(indentRef); _ = indentLevel; spaces := Formatter_Doc_MakeSpaces(indentLevel); _ = spaces; sky_call(sky_refSet(sky_asString(sky_refGet(outputRef)) + sky_asString(sky_asString("\n") + sky_asString(spaces))), outputRef); sky_call(sky_refSet(indentLevel), colRef); return struct{}{} }()
}

func Formatter_Doc_MakeSpaces(n any) any {
	return func() any { if sky_asBool(sky_asInt(n) <= sky_asInt(0)) { return "" }; return sky_asString(" ") + sky_asString(Formatter_Doc_MakeSpaces(sky_asInt(n) - sky_asInt(1))) }()
}

func Formatter_Doc_FlatWidth(doc any) any {
	return func() any { return func() any { __subject := doc; if sky_asMap(__subject)["SkyName"] == "DocText" { s := sky_asMap(__subject)["V0"]; _ = s; return sky_stringLength(s) };  if sky_asMap(__subject)["SkyName"] == "DocLine" { return 1 };  if sky_asMap(__subject)["SkyName"] == "DocSoftline" { return 0 };  if sky_asMap(__subject)["SkyName"] == "DocHardline" { return 9999 };  if sky_asMap(__subject)["SkyName"] == "DocConcat" { parts := sky_asMap(__subject)["V0"]; _ = parts; return sky_call(sky_call(sky_listFoldl(func(part any) any { return func(acc any) any { return sky_asInt(acc) + sky_asInt(Formatter_Doc_FlatWidth(part)) } }), 0), parts) };  if sky_asMap(__subject)["SkyName"] == "DocIndent" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Formatter_Doc_FlatWidth(inner) };  if sky_asMap(__subject)["SkyName"] == "DocGroup" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Formatter_Doc_FlatWidth(inner) };  if sky_asMap(__subject)["SkyName"] == "DocAlign" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Formatter_Doc_FlatWidth(inner) };  return nil }() }()
}

func Formatter_Doc_Fits(doc any, remaining any) any {
	return func() any { if sky_asBool(sky_asInt(remaining) < sky_asInt(0)) { return false }; return func() any { return func() any { __subject := doc; if sky_asMap(__subject)["SkyName"] == "DocText" { s := sky_asMap(__subject)["V0"]; _ = s; return sky_asInt(sky_stringLength(s)) <= sky_asInt(remaining) };  if sky_asMap(__subject)["SkyName"] == "DocLine" { return true };  if sky_asMap(__subject)["SkyName"] == "DocSoftline" { return true };  if sky_asMap(__subject)["SkyName"] == "DocHardline" { return false };  if sky_asMap(__subject)["SkyName"] == "DocConcat" { parts := sky_asMap(__subject)["V0"]; _ = parts; return Formatter_Doc_FitsConcat(parts, remaining) };  if sky_asMap(__subject)["SkyName"] == "DocIndent" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Formatter_Doc_Fits(inner, remaining) };  if sky_asMap(__subject)["SkyName"] == "DocGroup" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Formatter_Doc_Fits(inner, remaining) };  if sky_asMap(__subject)["SkyName"] == "DocAlign" { inner := sky_asMap(__subject)["V0"]; _ = inner; return Formatter_Doc_Fits(inner, remaining) };  return nil }() }() }()
}

func Formatter_Doc_FitsConcat(parts any, remaining any) any {
	return func() any { return func() any { __subject := parts; if len(sky_asList(__subject)) == 0 { return true };  if len(sky_asList(__subject)) > 0 { part := sky_asList(__subject)[0]; _ = part; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { w := Formatter_Doc_FlatWidth(part); _ = w; return func() any { if sky_asBool(sky_asInt(w) > sky_asInt(remaining)) { return false }; return Formatter_Doc_FitsConcat(rest, sky_asInt(remaining) - sky_asInt(w)) }() }() };  return nil }() }()
}

func Formatter_Doc_Walk(doc any, outputRef any, colRef any, indentRef any) any {
	return func() any { return func() any { __subject := doc; if sky_asMap(__subject)["SkyName"] == "DocText" { s := sky_asMap(__subject)["V0"]; _ = s; return Formatter_Doc_WriteStr(s, outputRef, colRef) };  if sky_asMap(__subject)["SkyName"] == "DocLine" { return Formatter_Doc_Newline(outputRef, colRef, indentRef) };  if sky_asMap(__subject)["SkyName"] == "DocSoftline" { return Formatter_Doc_Newline(outputRef, colRef, indentRef) };  if sky_asMap(__subject)["SkyName"] == "DocHardline" { return Formatter_Doc_Newline(outputRef, colRef, indentRef) };  if sky_asMap(__subject)["SkyName"] == "DocConcat" { parts := sky_asMap(__subject)["V0"]; _ = parts; return Formatter_Doc_WalkParts(parts, outputRef, colRef, indentRef) };  if sky_asMap(__subject)["SkyName"] == "DocIndent" { inner := sky_asMap(__subject)["V0"]; _ = inner; return func() any { oldIndent := sky_refGet(indentRef); _ = oldIndent; sky_call(sky_refSet(sky_asInt(oldIndent) + sky_asInt(Formatter_Doc_IndentWidth)), indentRef); Formatter_Doc_Walk(inner, outputRef, colRef, indentRef); sky_call(sky_refSet(oldIndent), indentRef); return struct{}{} }() };  if sky_asMap(__subject)["SkyName"] == "DocGroup" { inner := sky_asMap(__subject)["V0"]; _ = inner; return func() any { remaining := sky_asInt(Formatter_Doc_MaxWidth) - sky_asInt(sky_refGet(colRef)); _ = remaining; return func() any { if sky_asBool(Formatter_Doc_Fits(inner, remaining)) { return Formatter_Doc_Flatten(inner, outputRef, colRef, indentRef) }; return Formatter_Doc_Walk(inner, outputRef, colRef, indentRef) }() }() };  if sky_asMap(__subject)["SkyName"] == "DocAlign" { inner := sky_asMap(__subject)["V0"]; _ = inner; return func() any { oldIndent := sky_refGet(indentRef); _ = oldIndent; sky_call(sky_refSet(sky_refGet(colRef)), indentRef); Formatter_Doc_Walk(inner, outputRef, colRef, indentRef); sky_call(sky_refSet(oldIndent), indentRef); return struct{}{} }() };  return nil }() }()
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

var InspectPackage = Ffi_Inspector_InspectPackage

var RunInspector = Ffi_Inspector_RunInspector

var SafePkgName = Ffi_Inspector_SafePkgName

var InspectorGoCode = Ffi_Inspector_InspectorGoCode

func Ffi_Inspector_InspectPackage(pkgName any) any {
	return func() any { cacheDir := sky_asString(".skycache/go/") + sky_asString(Ffi_Inspector_SafePkgName(pkgName)); _ = cacheDir; cachePath := sky_asString(cacheDir) + sky_asString("/inspect.json"); _ = cachePath; return func() any { return func() any { __subject := sky_fileRead(cachePath); if sky_asSkyResult(__subject).SkyName == "Ok" { cached := sky_asSkyResult(__subject).OkValue; _ = cached; return SkyOk(cached) };  if sky_asSkyResult(__subject).SkyName == "Err" { return Ffi_Inspector_RunInspector(pkgName, cacheDir, cachePath) };  return nil }() }() }()
}

func Ffi_Inspector_RunInspector(pkgName any, cacheDir any, cachePath any) any {
	return func() any { inspectorDir := ".skycache/inspector"; _ = inspectorDir; sky_fileMkdirAll(inspectorDir); sky_fileMkdirAll(cacheDir); sky_call(sky_fileWrite(sky_asString(inspectorDir) + sky_asString("/main.go")), Ffi_Inspector_InspectorGoCode); sky_call(sky_fileWrite(sky_asString(inspectorDir) + sky_asString("/go.mod")), "module sky-inspector\n\ngo 1.24.0\n\nrequire golang.org/x/tools v0.30.0\n"); buildResult := sky_call(sky_processRun("sh"), []any{"-c", sky_asString("cd ") + sky_asString(sky_asString(inspectorDir) + sky_asString(" && go mod tidy 2>/dev/null && go build -o inspector . 2>&1"))}); _ = buildResult; return func() any { return func() any { __subject := buildResult; if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Failed to build Go inspector: ") + sky_asString(sky_errorToString(e))) };  if sky_asSkyResult(__subject).SkyName == "Ok" { return func() any { return func() any { __subject := sky_call(sky_processRun(sky_asString(inspectorDir) + sky_asString("/inspector")), []any{pkgName}); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_asString("Failed to inspect package ") + sky_asString(sky_asString(pkgName) + sky_asString(sky_asString(": ") + sky_asString(sky_errorToString(e))))) };  if sky_asSkyResult(__subject).SkyName == "Ok" { output := sky_asSkyResult(__subject).OkValue; _ = output; return func() any { sky_call(sky_fileWrite(cachePath), output); return SkyOk(output) }() };  return nil }() }() };  return nil }() }() }()
}

func Ffi_Inspector_SafePkgName(name any) any {
	return sky_call(sky_call(sky_stringReplace("/"), "_"), sky_call(sky_call(sky_stringReplace("."), "_"), name))
}

func Ffi_Inspector_InspectorGoCode() any {
	return sky_call(sky_stringJoin("\\n"), []any{"package main", "", "import (", "\\t\\\"encoding/json\\\"", "\\t\\\"fmt\\\"", "\\t\\\"go/types\\\"", "\\t\\\"os\\\"", "\\t_ \\\"strings\\\"", "\\t\\\"golang.org/x/tools/go/packages\\\"", ")", "", "type Output struct {", "\\tName   string     `json:\\\"name\\\"`", "\\tPath   string     `json:\\\"path\\\"`", "\\tTypes  []TypeDecl `json:\\\"types\\\"`", "\\tFuncs  []FuncDecl `json:\\\"funcs\\\"`", "\\tVars   []VarDecl  `json:\\\"vars\\\"`", "\\tConsts []ConstDecl `json:\\\"consts\\\"`", "}", "", "type TypeDecl struct {", "\\tName    string      `json:\\\"name\\\"`", "\\tKind    string      `json:\\\"kind\\\"`", "\\tFields  []FieldDecl `json:\\\"fields,omitempty\\\"`", "\\tMethods []MethodDecl `json:\\\"methods,omitempty\\\"`", "}", "", "type FieldDecl struct {", "\\tName string `json:\\\"name\\\"`", "\\tType string `json:\\\"type\\\"`", "}", "", "type MethodDecl struct {", "\\tName     string      `json:\\\"name\\\"`", "\\tParams   []ParamDecl `json:\\\"params\\\"`", "\\tResults  []ParamDecl `json:\\\"results\\\"`", "\\tVariadic bool        `json:\\\"variadic,omitempty\\\"`", "}", "", "type FuncDecl struct {", "\\tName     string      `json:\\\"name\\\"`", "\\tParams   []ParamDecl `json:\\\"params\\\"`", "\\tResults  []ParamDecl `json:\\\"results\\\"`", "\\tVariadic bool        `json:\\\"variadic,omitempty\\\"`", "}", "", "type ParamDecl struct {", "\\tName string `json:\\\"name\\\"`", "\\tType string `json:\\\"type\\\"`", "}", "", "type VarDecl struct {", "\\tName string `json:\\\"name\\\"`", "\\tType string `json:\\\"type\\\"`", "}", "", "type ConstDecl struct {", "\\tName  string `json:\\\"name\\\"`", "\\tType  string `json:\\\"type\\\"`", "\\tValue string `json:\\\"value,omitempty\\\"`", "}", "", "func typeStr(t types.Type) string {", "\\tswitch u := t.(type) {", "\\tcase *types.Named:", "\\t\\tobj := u.Obj()", "\\t\\tpkg := obj.Pkg()", "\\t\\tif pkg != nil {", "\\t\\t\\treturn pkg.Path() + \\\".\\\" + obj.Name()", "\\t\\t}", "\\t\\treturn obj.Name()", "\\tcase *types.Pointer:", "\\t\\treturn \\\"*\\\" + typeStr(u.Elem())", "\\tcase *types.Slice:", "\\t\\treturn \\\"[]\\\" + typeStr(u.Elem())", "\\tcase *types.Map:", "\\t\\treturn \\\"map[\\\" + typeStr(u.Key()) + \\\"]\\\" + typeStr(u.Elem())", "\\tcase *types.Interface:", "\\t\\tif u.Empty() { return \\\"interface{}\\\" }", "\\t\\treturn \\\"interface{}\\\"", "\\tdefault:", "\\t\\treturn t.String()", "\\t}", "}", "", "func main() {", "\\tif len(os.Args) < 2 {", "\\t\\tfmt.Fprintln(os.Stderr, \\\"usage: inspector <package>\\\")", "\\t\\tos.Exit(1)", "\\t}", "\\tpkgPath := os.Args[1]", "", "\\tcfg := &packages.Config{Mode: packages.NeedTypes | packages.NeedImports | packages.NeedDeps}", "\\tpkgs, err := packages.Load(cfg, pkgPath)", "\\tif err != nil {", "\\t\\tfmt.Fprintf(os.Stderr, \\\"load error: %v\\\\n\\\", err)", "\\t\\tos.Exit(1)", "\\t}", "\\tif len(pkgs) == 0 || pkgs[0].Types == nil {", "\\t\\tfmt.Fprintln(os.Stderr, \\\"no types found\\\")", "\\t\\tos.Exit(1)", "\\t}", "", "\\tpkg := pkgs[0]", "\\tscope := pkg.Types.Scope()", "\\tout := Output{Name: pkg.Types.Name(), Path: pkg.Types.Path()}", "", "\\tfor _, name := range scope.Names() {", "\\t\\tobj := scope.Lookup(name)", "\\t\\tif !obj.Exported() { continue }", "\\t\\tswitch o := obj.(type) {", "\\t\\tcase *types.TypeName:", "\\t\\t\\tnamed, ok := o.Type().(*types.Named)", "\\t\\t\\tif !ok { continue }", "\\t\\t\\ttd := TypeDecl{Name: name}", "\\t\\t\\tswitch u := named.Underlying().(type) {", "\\t\\t\\tcase *types.Struct:", "\\t\\t\\t\\ttd.Kind = \\\"struct\\\"", "\\t\\t\\t\\tfor i := 0; i < u.NumFields(); i++ {", "\\t\\t\\t\\t\\tf := u.Field(i)", "\\t\\t\\t\\t\\tif f.Exported() {", "\\t\\t\\t\\t\\t\\ttd.Fields = append(td.Fields, FieldDecl{Name: f.Name(), Type: typeStr(f.Type())})", "\\t\\t\\t\\t\\t}", "\\t\\t\\t\\t}", "\\t\\t\\tcase *types.Interface:", "\\t\\t\\t\\ttd.Kind = \\\"interface\\\"", "\\t\\t\\tdefault:", "\\t\\t\\t\\ttd.Kind = \\\"other\\\"", "\\t\\t\\t}", "\\t\\t\\t// Methods", "\\t\\t\\tmset := types.NewMethodSet(types.NewPointer(named))", "\\t\\t\\tfor i := 0; i < mset.Len(); i++ {", "\\t\\t\\t\\tm := mset.At(i)", "\\t\\t\\t\\tfn, ok := m.Obj().(*types.Func)", "\\t\\t\\t\\tif !ok || !fn.Exported() { continue }", "\\t\\t\\t\\tsig := fn.Type().(*types.Signature)", "\\t\\t\\t\\tmd := MethodDecl{Name: fn.Name(), Variadic: sig.Variadic()}", "\\t\\t\\t\\tfor j := 0; j < sig.Params().Len(); j++ {", "\\t\\t\\t\\t\\tp := sig.Params().At(j)", "\\t\\t\\t\\t\\tmd.Params = append(md.Params, ParamDecl{Name: p.Name(), Type: typeStr(p.Type())})", "\\t\\t\\t\\t}", "\\t\\t\\t\\tfor j := 0; j < sig.Results().Len(); j++ {", "\\t\\t\\t\\t\\tr := sig.Results().At(j)", "\\t\\t\\t\\t\\tmd.Results = append(md.Results, ParamDecl{Name: r.Name(), Type: typeStr(r.Type())})", "\\t\\t\\t\\t}", "\\t\\t\\t\\ttd.Methods = append(td.Methods, md)", "\\t\\t\\t}", "\\t\\t\\tout.Types = append(out.Types, td)", "\\t\\tcase *types.Func:", "\\t\\t\\tsig := o.Type().(*types.Signature)", "\\t\\t\\tfd := FuncDecl{Name: name, Variadic: sig.Variadic()}", "\\t\\t\\tfor i := 0; i < sig.Params().Len(); i++ {", "\\t\\t\\t\\tp := sig.Params().At(i)", "\\t\\t\\t\\tfd.Params = append(fd.Params, ParamDecl{Name: p.Name(), Type: typeStr(p.Type())})", "\\t\\t\\t}", "\\t\\t\\tfor i := 0; i < sig.Results().Len(); i++ {", "\\t\\t\\t\\tr := sig.Results().At(i)", "\\t\\t\\t\\tfd.Results = append(fd.Results, ParamDecl{Name: r.Name(), Type: typeStr(r.Type())})", "\\t\\t\\t}", "\\t\\t\\tout.Funcs = append(out.Funcs, fd)", "\\t\\tcase *types.Var:", "\\t\\t\\tout.Vars = append(out.Vars, VarDecl{Name: name, Type: typeStr(o.Type())})", "\\t\\tcase *types.Const:", "\\t\\t\\tout.Consts = append(out.Consts, ConstDecl{Name: name, Type: typeStr(o.Type()), Value: o.Val().String()})", "\\t\\t}", "\\t}", "", "\\tenc := json.NewEncoder(os.Stdout)", "\\tenc.SetIndent(\\\"\\\", \\\"  \\\")", "\\tif err := enc.Encode(out); err != nil {", "\\t\\tfmt.Fprintf(os.Stderr, \\\"encode error: %v\\\\n\\\", err)", "\\t\\tos.Exit(1)", "\\t}", "}"})
}

var GenerateWrappers = Ffi_WrapperGen_GenerateWrappers

var GenerateWrapperFile = Ffi_WrapperGen_GenerateWrapperFile

var GenerateFuncWrapper = Ffi_WrapperGen_GenerateFuncWrapper

var GenerateMethodWrapper = Ffi_WrapperGen_GenerateMethodWrapper

var GenerateArgCast = Ffi_WrapperGen_GenerateArgCast

var WrapReturn = Ffi_WrapperGen_WrapReturn

var ShortPkgName = Ffi_WrapperGen_ShortPkgName

var ExtractFunctions = Ffi_WrapperGen_ExtractFunctions

var ExtractMethods = Ffi_WrapperGen_ExtractMethods

func Ffi_WrapperGen_GenerateWrappers(pkgName any, inspectJson any, outDir any) any {
	return func() any { safePkg := sky_call(sky_call(sky_stringReplace("/"), "_"), sky_call(sky_call(sky_stringReplace("."), "_"), pkgName)); _ = safePkg; wrapperDir := sky_asString(outDir) + sky_asString("/sky_wrappers"); _ = wrapperDir; sky_fileMkdirAll(wrapperDir); funcs := Ffi_WrapperGen_ExtractFunctions(inspectJson); _ = funcs; methods := Ffi_WrapperGen_ExtractMethods(inspectJson); _ = methods; wrapperCode := Ffi_WrapperGen_GenerateWrapperFile(safePkg, pkgName, funcs, methods); _ = wrapperCode; sky_call(sky_fileWrite(sky_asString(wrapperDir) + sky_asString(sky_asString("/") + sky_asString(sky_asString(safePkg) + sky_asString(".go")))), wrapperCode); return SkyOk(wrapperCode) }()
}

func Ffi_WrapperGen_GenerateWrapperFile(safePkg any, pkgName any, funcs any, methods any) any {
	return sky_call(sky_stringJoin("\\n"), []any{"package sky_wrappers", "", "import (", sky_asString("\t\"") + sky_asString(sky_asString(pkgName) + sky_asString("\"")), ")", "", sky_call(sky_stringJoin("\\n\\n"), sky_call(sky_listMap(func(__pa0 any) any { return Ffi_WrapperGen_GenerateFuncWrapper(safePkg, pkgName, __pa0) }), funcs)), "", sky_call(sky_stringJoin("\\n\\n"), sky_call(sky_listMap(func(__pa0 any) any { return Ffi_WrapperGen_GenerateMethodWrapper(safePkg, pkgName, __pa0) }), methods))})
}

func Ffi_WrapperGen_GenerateFuncWrapper(safePkg any, pkgName any, func_ any) any {
	return func() any { wrapperName := sky_asString("Sky_") + sky_asString(sky_asString(safePkg) + sky_asString(sky_asString("_") + sky_asString(sky_asMap(func_)["name"]))); _ = wrapperName; paramList := sky_call(sky_listIndexedMap(func(i any) any { return func(p any) any { return sky_asString("arg") + sky_asString(sky_asString(sky_stringFromInt(i)) + sky_asString(" any")) } }), sky_asMap(func_)["params"]); _ = paramList; paramStr := sky_call(sky_stringJoin(", "), paramList); _ = paramStr; argCasts := sky_call(sky_listIndexedMap(Ffi_WrapperGen_GenerateArgCast), sky_asMap(func_)["params"]); _ = argCasts; castStr := sky_call(sky_stringJoin("\n\t"), argCasts); _ = castStr; argNames := sky_call(sky_listIndexedMap(func(i any) any { return func(p any) any { return sky_asString("_arg") + sky_asString(sky_stringFromInt(i)) } }), sky_asMap(func_)["params"]); _ = argNames; goCall := sky_asString(Ffi_WrapperGen_ShortPkgName(pkgName)) + sky_asString(sky_asString(".") + sky_asString(sky_asString(sky_asMap(func_)["name"]) + sky_asString(sky_asString("(") + sky_asString(sky_asString(sky_call(sky_stringJoin(", "), argNames)) + sky_asString(")"))))); _ = goCall; returnCode := Ffi_WrapperGen_WrapReturn(sky_asMap(func_)["results"], goCall); _ = returnCode; return sky_asString("func ") + sky_asString(sky_asString(wrapperName) + sky_asString(sky_asString("(") + sky_asString(sky_asString(paramStr) + sky_asString(sky_asString(") any {\n\t") + sky_asString(sky_asString(castStr) + sky_asString(sky_asString("\n\t") + sky_asString(sky_asString(returnCode) + sky_asString("\n}")))))))) }()
}

func Ffi_WrapperGen_GenerateMethodWrapper(safePkg any, pkgName any, method any) any {
	return func() any { wrapperName := sky_asString("Sky_") + sky_asString(sky_asString(safePkg) + sky_asString(sky_asString("_") + sky_asString(sky_asString(sky_asMap(method)["typeName"]) + sky_asString(sky_asMap(method)["name"])))); _ = wrapperName; paramList := append([]any{"receiver any"}, sky_asList(sky_call(sky_listIndexedMap(func(i any) any { return func(p any) any { return sky_asString("arg") + sky_asString(sky_asString(sky_stringFromInt(i)) + sky_asString(" any")) } }), sky_asMap(method)["params"]))...); _ = paramList; paramStr := sky_call(sky_stringJoin(", "), paramList); _ = paramStr; receiverCast := sky_asString("_receiver := receiver.(*") + sky_asString(sky_asString(Ffi_WrapperGen_ShortPkgName(pkgName)) + sky_asString(sky_asString(".") + sky_asString(sky_asString(sky_asMap(method)["typeName"]) + sky_asString(")")))); _ = receiverCast; argCasts := sky_call(sky_listIndexedMap(Ffi_WrapperGen_GenerateArgCast), sky_asMap(method)["params"]); _ = argCasts; castStr := sky_call(sky_stringJoin("\n\t"), append([]any{receiverCast}, sky_asList(argCasts)...)); _ = castStr; argNames := sky_call(sky_listIndexedMap(func(i any) any { return func(p any) any { return sky_asString("_arg") + sky_asString(sky_stringFromInt(i)) } }), sky_asMap(method)["params"]); _ = argNames; goCall := sky_asString("_receiver.") + sky_asString(sky_asString(sky_asMap(method)["name"]) + sky_asString(sky_asString("(") + sky_asString(sky_asString(sky_call(sky_stringJoin(", "), argNames)) + sky_asString(")")))); _ = goCall; returnCode := Ffi_WrapperGen_WrapReturn(sky_asMap(method)["results"], goCall); _ = returnCode; return sky_asString("func ") + sky_asString(sky_asString(wrapperName) + sky_asString(sky_asString("(") + sky_asString(sky_asString(paramStr) + sky_asString(sky_asString(") any {\n\t") + sky_asString(sky_asString(castStr) + sky_asString(sky_asString("\n\t") + sky_asString(sky_asString(returnCode) + sky_asString("\n}")))))))) }()
}

func Ffi_WrapperGen_GenerateArgCast(idx any, param any) any {
	return func() any { goType := sky_snd(param); _ = goType; assertion := Ffi_TypeMapper_GoTypeToAssertion(goType); _ = assertion; return func() any { if sky_asBool(sky_stringIsEmpty(assertion)) { return sky_asString("_arg") + sky_asString(sky_asString(sky_stringFromInt(idx)) + sky_asString(sky_asString(" := arg") + sky_asString(sky_stringFromInt(idx)))) }; return sky_asString("_arg") + sky_asString(sky_asString(sky_stringFromInt(idx)) + sky_asString(sky_asString(" := ") + sky_asString(sky_asString(assertion) + sky_asString(sky_asString("(arg") + sky_asString(sky_asString(sky_stringFromInt(idx)) + sky_asString(")")))))) }() }()
}

func Ffi_WrapperGen_WrapReturn(results any, goCall any) any {
	return func() any { return func() any { __subject := results; if len(sky_asList(__subject)) == 0 { return sky_asString(goCall) + sky_asString("\n\treturn struct{}{}") };  if len(sky_asList(__subject)) == 1 { single := sky_asList(__subject)[0]; _ = single; return func() any { if sky_asBool(sky_equal(single, "error")) { return sky_asString("result, err := ") + sky_asString(sky_asString(goCall) + sky_asString("\n\tif err != nil { return SkyErr(err.Error()) }\n\treturn SkyOk(result)")) }; return sky_asString("return ") + sky_asString(goCall) }() };  if true { return func() any { lastResult := func() any { return func() any { __subject := sky_listReverse(results); if len(sky_asList(__subject)) > 0 { last := sky_asList(__subject)[0]; _ = last; return last };  if len(sky_asList(__subject)) == 0 { return "" };  return nil }() }(); _ = lastResult; return func() any { if sky_asBool(sky_equal(lastResult, "error")) { return sky_asString("result, err := ") + sky_asString(sky_asString(goCall) + sky_asString("\n\tif err != nil { return SkyErr(err.Error()) }\n\treturn SkyOk(result)")) }; return sky_asString("return ") + sky_asString(goCall) }() }() };  return nil }() }()
}

func Ffi_WrapperGen_ShortPkgName(pkgPath any) any {
	return func() any { return func() any { __subject := sky_listReverse(sky_call(sky_stringSplit("/"), pkgPath)); if len(sky_asList(__subject)) > 0 { last := sky_asList(__subject)[0]; _ = last; return last };  if len(sky_asList(__subject)) == 0 { return pkgPath };  return nil }() }()
}

func Ffi_WrapperGen_ExtractFunctions(json any) any {
	return []any{}
}

func Ffi_WrapperGen_ExtractMethods(json any) any {
	return []any{}
}

func main() {
	func() any { args := sky_processGetArgs(struct{}{}); _ = args; command := func() any { return func() any { __subject := sky_processGetArg(1); if sky_asSkyMaybe(__subject).SkyName == "Just" { cmd := sky_asSkyMaybe(__subject).JustValue; _ = cmd; return cmd };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "help" };  return nil }() }(); _ = command; return runCommand(command, args) }()
}

func runCommand(command any, args any) any {
	return func() any { if sky_asBool(sky_equal(command, "build")) { return cmdBuild(args) }; return func() any { if sky_asBool(sky_equal(command, "run")) { return cmdRun(args) }; return func() any { if sky_asBool(sky_equal(command, "check")) { return cmdCheck(args) }; return func() any { if sky_asBool(sky_equal(command, "fmt")) { return cmdFmt(args) }; return func() any { if sky_asBool(sky_equal(command, "lsp")) { return cmdLsp(args) }; return func() any { if sky_asBool(sky_equal(command, "inspect")) { return cmdInspect(args) }; return func() any { if sky_asBool(sky_asBool(sky_equal(command, "version")) || sky_asBool(sky_equal(command, "--version"))) { return sky_println("sky v0.4.2 (self-hosted)") }; return cmdHelp(struct{}{}) }() }() }() }() }() }() }()
}

func cmdBuild(args any) any {
	return func() any { entryFile := func() any { return func() any { __subject := sky_processGetArg(2); if sky_asSkyMaybe(__subject).SkyName == "Just" { f := sky_asSkyMaybe(__subject).JustValue; _ = f; return f };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "src/Main.sky" };  return nil }() }(); _ = entryFile; outDir := "sky-out"; _ = outDir; return func() any { return func() any { __subject := Compiler_Pipeline_Compile(entryFile, outDir); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { sky_println(sky_asString("Error: ") + sky_asString(e)); return sky_processExit(1) }() };  if sky_asSkyResult(__subject).SkyName == "Ok" { code := sky_asSkyResult(__subject).OkValue; _ = code; return func() any { sky_call(sky_fileWrite(sky_asString(outDir) + sky_asString("/go.mod")), "module sky-app\n\ngo 1.21.0\n"); sky_println("Compiled Sky to Go."); buildResult := sky_call(sky_processRun("sh"), []any{"-c", sky_asString("cd ") + sky_asString(sky_asString(outDir) + sky_asString(" && go build -gcflags=all=-l -o app"))}); _ = buildResult; return func() any { return func() any { __subject := buildResult; if sky_asSkyResult(__subject).SkyName == "Ok" { buildOutput := sky_asSkyResult(__subject).OkValue; _ = buildOutput; return func() any { buildOutput; return sky_println(sky_asString("Build complete: ") + sky_asString(sky_asString(outDir) + sky_asString("/app"))) }() };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { sky_println(sky_asString("go build failed: ") + sky_asString(sky_errorToString(e))); return sky_processExit(1) }() };  return nil }() }() }() };  return nil }() }() }()
}

func cmdRun(args any) any {
	return func() any { cmdBuild(args); runResult := sky_call(sky_processRun("./dist/app"), []any{}); _ = runResult; return func() any { return func() any { __subject := runResult; if sky_asSkyResult(__subject).SkyName == "Ok" { output := sky_asSkyResult(__subject).OkValue; _ = output; return sky_println(output) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { sky_println(sky_errorToString(e)); return sky_processExit(1) }() };  return nil }() }() }()
}

func cmdCheck(args any) any {
	return func() any { entryFile := func() any { return func() any { __subject := sky_processGetArg(2); if sky_asSkyMaybe(__subject).SkyName == "Just" { f := sky_asSkyMaybe(__subject).JustValue; _ = f; return f };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "src/Main.sky" };  return nil }() }(); _ = entryFile; return func() any { return func() any { __subject := sky_fileRead(entryFile); if sky_asSkyResult(__subject).SkyName == "Err" { readErr := sky_asSkyResult(__subject).ErrValue; _ = readErr; return func() any { sky_println(sky_asString("Cannot read: ") + sky_asString(sky_asString(entryFile) + sky_asString(sky_asString(" (") + sky_asString(sky_asString(readErr) + sky_asString(")"))))); return sky_processExit(1) }() };  if sky_asSkyResult(__subject).SkyName == "Ok" { source := sky_asSkyResult(__subject).OkValue; _ = source; return func() any { lexResult := Compiler_Lexer_Lex(source); _ = lexResult; return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { sky_println(sky_asString("Parse error: ") + sky_asString(e)); return sky_processExit(1) }() };  if sky_asSkyResult(__subject).SkyName == "Ok" { mod := sky_asSkyResult(__subject).OkValue; _ = mod; return func() any { stdlibEnv := Compiler_Resolver_BuildStdlibEnv(); _ = stdlibEnv; checkResult := Compiler_Checker_CheckModule(mod, SkyJust(stdlibEnv)); _ = checkResult; return func() any { return func() any { __subject := checkResult; if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return func() any { sky_call(sky_listMap(func(d any) any { return sky_println(sky_asString("  ") + sky_asString(sky_asString(sky_asMap(d)["name"]) + sky_asString(sky_asString(" : ") + sky_asString(sky_asMap(d)["prettyType"])))) }), sky_asMap(result)["declarations"]); sky_call(sky_listMap(func(d any) any { return sky_println(sky_asString("  warning: ") + sky_asString(d)) }), sky_asMap(result)["diagnostics"]); return func() any { if sky_asBool(sky_listIsEmpty(sky_asMap(result)["diagnostics"])) { return sky_println(sky_asString("Type check passed: ") + sky_asString(entryFile)) }; return sky_println(sky_asString("Type check passed with ") + sky_asString(sky_asString(sky_stringFromInt(sky_listLength(sky_asMap(result)["diagnostics"]))) + sky_asString(sky_asString(" warnings: ") + sky_asString(entryFile)))) }() }() };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { sky_println(sky_asString("Type error: ") + sky_asString(e)); return sky_processExit(1) }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }()
}

func cmdLsp(args any) any {
	return Lsp_Server_StartServer(struct{}{})
}

func cmdFmt(args any) any {
	return func() any { filePath := func() any { return func() any { __subject := sky_processGetArg(2); if sky_asSkyMaybe(__subject).SkyName == "Just" { f := sky_asSkyMaybe(__subject).JustValue; _ = f; return f };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "src/Main.sky" };  return nil }() }(); _ = filePath; return func() any { return func() any { __subject := sky_fileRead(filePath); if sky_asSkyResult(__subject).SkyName == "Err" { readErr := sky_asSkyResult(__subject).ErrValue; _ = readErr; return func() any { sky_println(sky_asString("Cannot read: ") + sky_asString(sky_asString(filePath) + sky_asString(sky_asString(" (") + sky_asString(sky_asString(readErr) + sky_asString(")"))))); return sky_processExit(1) }() };  if sky_asSkyResult(__subject).SkyName == "Ok" { source := sky_asSkyResult(__subject).OkValue; _ = source; return func() any { lexResult := Compiler_Lexer_Lex(source); _ = lexResult; return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if sky_asSkyResult(__subject).SkyName == "Err" { parseErr := sky_asSkyResult(__subject).ErrValue; _ = parseErr; return func() any { sky_println(sky_asString("Parse error: ") + sky_asString(parseErr)); return sky_processExit(1) }() };  if sky_asSkyResult(__subject).SkyName == "Ok" { mod := sky_asSkyResult(__subject).OkValue; _ = mod; return func() any { formatted := Formatter_Format_FormatModule(mod); _ = formatted; return func() any { if sky_asBool(sky_equal(formatted, source)) { return struct{}{} }; return func() any { sky_call(sky_fileWrite(filePath), formatted); return sky_println(sky_asString("Formatted ") + sky_asString(filePath)) }() }() }() };  return nil }() }() }() };  return nil }() }() }()
}

func cmdInspect(args any) any {
	return func() any { pkgName := func() any { return func() any { __subject := sky_processGetArg(2); if sky_asSkyMaybe(__subject).SkyName == "Just" { p := sky_asSkyMaybe(__subject).JustValue; _ = p; return p };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "" };  return nil }() }(); _ = pkgName; return func() any { if sky_asBool(sky_stringIsEmpty(pkgName)) { return sky_println("Usage: sky inspect <go-package>") }; return func() any { return func() any { __subject := Ffi_Inspector_InspectPackage(pkgName); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { sky_println(sky_asString("Error: ") + sky_asString(e)); return sky_processExit(1) }() };  if sky_asSkyResult(__subject).SkyName == "Ok" { result := sky_asSkyResult(__subject).OkValue; _ = result; return sky_println(result) };  return nil }() }() }() }()
}

func cmdHelp(_ any) any {
	return func() any { sky_println("Sky Programming Language v0.4.0 (self-hosted)"); sky_println(""); sky_println("Usage: sky <command> [args]"); sky_println(""); sky_println("Commands:"); sky_println("  build [file]    Compile to Go binary (default: src/Main.sky)"); sky_println("  run [file]      Build and run"); sky_println("  check [file]    Type-check without compiling"); sky_println("  fmt [file]      Format Sky source code"); sky_println("  lsp             Start Language Server"); sky_println("  version         Show version"); return struct{}{} }()
}
