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

func sky_fst(t any) any { return sky_asTuple2(t).V0 }

func sky_snd(t any) any { return sky_asTuple2(t).V1 }

func sky_errorToString(e any) any { return sky_asString(e) }

func sky_identity(v any) any { return v }

func sky_always(a any) any { return func(b any) any { return a } }

func sky_js(v any) any { return v }

func sky_call(f any, arg any) any { return f.(func(any) any)(arg) }

func sky_call2(f any, a any, b any) any { return f.(func(any) any)(a).(func(any) any)(b) }

func sky_call3(f any, a any, b any, c any) any { return f.(func(any) any)(a).(func(any) any)(b).(func(any) any)(c) }

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

func Compiler_Lower_EmptyCtx() any {
	return map[string]any{"registry": Compiler_Adt_EmptyRegistry(), "moduleExports": sky_dictEmpty(), "importedConstructors": sky_dictEmpty(), "localFunctions": []any{}, "collectedImports": sky_setEmpty(), "importAliases": sky_dictEmpty(), "modulePrefix": "", "localFunctionArity": sky_dictEmpty()}
}

func Compiler_Lower_LowerModule(registry any, mod any) any {
	return func() any { ctx := sky_recordUpdate(Compiler_Lower_EmptyCtx(), map[string]any{"registry": Compiler_Lower_Registry, "localFunctions": Compiler_Lower_CollectLocalFunctions(sky_asMap(mod)["declarations"]), "importedConstructors": Compiler_Lower_BuildConstructorMap(Compiler_Lower_Registry)}); _ = ctx; goDecls := Compiler_Lower_LowerDeclarations(ctx, sky_asMap(mod)["declarations"]); _ = goDecls; ctorDecls := Compiler_Lower_GenerateConstructorDecls(Compiler_Lower_Registry, sky_asMap(mod)["declarations"]); _ = ctorDecls; imports := []any{map[string]any{"path": "fmt", "alias_": ""}, map[string]any{"path": "bufio", "alias_": ""}, map[string]any{"path": "os", "alias_": ""}, map[string]any{"path": "os/exec", "alias_": "exec"}, map[string]any{"path": "strconv", "alias_": ""}, map[string]any{"path": "strings", "alias_": ""}}; _ = imports; helperDecls := Compiler_Lower_GenerateHelperDecls(); _ = helperDecls; return map[string]any{"name": "main", "imports": imports, "declarations": sky_listConcat([]any{helperDecls, ctorDecls, goDecls})} }()
}

func Compiler_Lower_LowerDeclarations(ctx any, decls any) any {
	return sky_call(sky_listFilterMap(func(__pa0 any) any { return Compiler_Lower_LowerDecl(ctx, __pa0) }), decls)
}

func Compiler_Lower_LowerDecl(ctx any, decl any) any {
	return func() any { return func() any { __subject := decl; if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; params := sky_asMap(__subject)["V1"]; _ = params; body := sky_asMap(__subject)["V2"]; _ = body; return SkyJust(Compiler_Lower_LowerFunction(ctx, name, params, body)) };  if true { return SkyNothing() };  return nil }() }()
}

func Compiler_Lower_LowerFunction(ctx any, name any, params any, body any) any {
	return func() any { isMain := sky_equal(name, "main"); _ = isMain; goName := func() any { if sky_asBool(isMain) { return "main" }; return Compiler_Lower_SanitizeGoIdent(name) }(); _ = goName; goParams := sky_call(sky_listMap(Compiler_Lower_LowerParam), params); _ = goParams; returnType := func() any { if sky_asBool(isMain) { return "" }; return "any" }(); _ = returnType; goBody := func() any { if sky_asBool(isMain) { return Compiler_Lower_LowerExprToStmts(ctx, body) }; return []any{GoReturn(Compiler_Lower_LowerExpr(ctx, body))} }(); _ = goBody; return GoDeclFunc(map[string]any{"name": goName, "params": goParams, "returnType": returnType, "body": goBody}) }()
}

func Compiler_Lower_LowerParam(pat any) any {
	return func() any { return func() any { __subject := pat; if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return map[string]any{"name": Compiler_Lower_SanitizeGoIdent(name), "type_": "any"} };  if <nil> { return map[string]any{"name": "_", "type_": "any"} };  if true { return map[string]any{"name": "_p", "type_": "any"} };  return nil }() }()
}

func Compiler_Lower_LowerExpr(ctx any, expr any) any {
	return func() any { return func() any { __subject := expr; if <nil> { raw := sky_asMap(__subject)["V1"]; _ = raw; return GoBasicLit(raw) };  if <nil> { raw := sky_asMap(__subject)["V1"]; _ = raw; return GoBasicLit(raw) };  if <nil> { s := sky_asMap(__subject)["V0"]; _ = s; return GoStringLit(s) };  if <nil> { s := sky_asMap(__subject)["V0"]; _ = s; return GoCallExpr(GoIdent("string"), []any{GoBasicLit(sky_concat("'", sky_concat(sky_call2(sky_stringSlice(1), sky_asInt(sky_stringLength(s)) - sky_asInt(1), s), "'")))}) };  if <nil> { b := sky_asMap(__subject)["V0"]; _ = b; return func() any { if sky_asBool(b) { return GoIdent("true") }; return GoIdent("false") }() };  if <nil> { return GoRawExpr("struct{}{}") };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return Compiler_Lower_LowerIdentifier(ctx, name) };  if <nil> { parts := sky_asMap(__subject)["V0"]; _ = parts; return Compiler_Lower_LowerQualified(ctx, parts) };  if <nil> { items := sky_asMap(__subject)["V0"]; _ = items; return func() any { goItems := sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Lower_LowerExpr(ctx, __pa0) }), items); _ = goItems; n := sky_listLength(items); _ = n; return func() any { if sky_asBool(sky_equal(n, 2)) { return GoRawExpr(sky_concat("SkyTuple2{V0: ", sky_concat(Compiler_Lower_EmitGoExprInline(Compiler_Lower_ListGet(0, goItems)), sky_concat(", V1: ", sky_concat(Compiler_Lower_EmitGoExprInline(Compiler_Lower_ListGet(1, goItems)), "}"))))) }; return func() any { if sky_asBool(sky_equal(n, 3)) { return GoRawExpr(sky_concat("SkyTuple3{V0: ", sky_concat(Compiler_Lower_EmitGoExprInline(Compiler_Lower_ListGet(0, goItems)), sky_concat(", V1: ", sky_concat(Compiler_Lower_EmitGoExprInline(Compiler_Lower_ListGet(1, goItems)), sky_concat(", V2: ", sky_concat(Compiler_Lower_EmitGoExprInline(Compiler_Lower_ListGet(2, goItems)), "}"))))))) }; return GoSliceLit(goItems) }() }() }() };  if <nil> { items := sky_asMap(__subject)["V0"]; _ = items; return GoSliceLit(sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Lower_LowerExpr(ctx, __pa0) }), items)) };  if <nil> { fields := sky_asMap(__subject)["V0"]; _ = fields; return GoMapLit(sky_call(sky_listMap(func(f any) any { return SkyTuple2{V0: GoStringLit(sky_asMap(f)["name"]), V1: Compiler_Lower_LowerExpr(ctx, sky_asMap(f)["value"])} }), fields)) };  if <nil> { base := sky_asMap(__subject)["V0"]; _ = base; fields := sky_asMap(__subject)["V1"]; _ = fields; return Compiler_Lower_LowerRecordUpdate(ctx, base, fields) };  if <nil> { target := sky_asMap(__subject)["V0"]; _ = target; fieldName := sky_asMap(__subject)["V1"]; _ = fieldName; return GoIndexExpr(GoCallExpr(GoIdent("sky_asMap"), []any{Compiler_Lower_LowerExpr(ctx, target)}), GoStringLit(fieldName)) };  if <nil> { callee := sky_asMap(__subject)["V0"]; _ = callee; args := sky_asMap(__subject)["V1"]; _ = args; return Compiler_Lower_LowerCall(ctx, callee, args) };  if <nil> { params := sky_asMap(__subject)["V0"]; _ = params; body := sky_asMap(__subject)["V1"]; _ = body; return Compiler_Lower_LowerLambda(ctx, params, body) };  if <nil> { condition := sky_asMap(__subject)["V0"]; _ = condition; thenBranch := sky_asMap(__subject)["V1"]; _ = thenBranch; elseBranch := sky_asMap(__subject)["V2"]; _ = elseBranch; return Compiler_Lower_LowerIf(ctx, condition, thenBranch, elseBranch) };  if <nil> { bindings := sky_asMap(__subject)["V0"]; _ = bindings; body := sky_asMap(__subject)["V1"]; _ = body; return Compiler_Lower_LowerLet(ctx, bindings, body) };  if <nil> { subject := sky_asMap(__subject)["V0"]; _ = subject; branches := sky_asMap(__subject)["V1"]; _ = branches; return Compiler_Lower_LowerCase(ctx, subject, branches) };  if <nil> { op := sky_asMap(__subject)["V0"]; _ = op; left := sky_asMap(__subject)["V1"]; _ = left; right := sky_asMap(__subject)["V2"]; _ = right; return Compiler_Lower_LowerBinary(ctx, op, left, right) };  if <nil> { inner := sky_asMap(__subject)["V0"]; _ = inner; return GoUnaryExpr("-", Compiler_Lower_LowerExpr(ctx, inner)) };  if <nil> { inner := sky_asMap(__subject)["V0"]; _ = inner; return Compiler_Lower_LowerExpr(ctx, inner) };  return nil }() }()
}

func Compiler_Lower_LowerIdentifier(ctx any, name any) any {
	return func() any { return func() any { __subject := sky_call(sky_dictGet(name), sky_asMap(ctx)["importedConstructors"]); if <nil> { info := sky_asSkyMaybe(__subject).JustValue; _ = info; return func() any { if sky_asBool(sky_equal(sky_asMap(info)["arity"], 0)) { return Compiler_Lower_LowerConstructorValue(name, info) }; return GoIdent(Compiler_Lower_SanitizeGoIdent(name)) }() };  if <nil> { return func() any { if sky_asBool(sky_equal(name, "Ok")) { return GoIdent("SkyOk") }; return func() any { if sky_asBool(sky_equal(name, "Err")) { return GoIdent("SkyErr") }; return func() any { if sky_asBool(sky_equal(name, "Just")) { return GoIdent("SkyJust") }; return func() any { if sky_asBool(sky_equal(name, "Nothing")) { return GoCallExpr(GoIdent("SkyNothing"), []any{}) }; return func() any { if sky_asBool(sky_equal(name, "True")) { return GoIdent("true") }; return func() any { if sky_asBool(sky_equal(name, "False")) { return GoIdent("false") }; return func() any { if sky_asBool(sky_equal(name, "not")) { return GoIdent("sky_not") }; return func() any { if sky_asBool(sky_equal(name, "fst")) { return GoIdent("sky_fst") }; return func() any { if sky_asBool(sky_equal(name, "snd")) { return GoIdent("sky_snd") }; return func() any { if sky_asBool(sky_equal(name, "errorToString")) { return GoIdent("sky_errorToString") }; return func() any { if sky_asBool(sky_equal(name, "println")) { return GoIdent("sky_println") }; return func() any { if sky_asBool(sky_equal(name, "identity")) { return GoIdent("sky_identity") }; return func() any { if sky_asBool(sky_equal(name, "always")) { return GoIdent("sky_always") }; return func() any { if sky_asBool(sky_equal(name, "js")) { return GoIdent("sky_js") }; return func() any { if sky_asBool(sky_asBool(sky_not(sky_stringIsEmpty(sky_asMap(ctx)["modulePrefix"]))) && sky_asBool(Compiler_Lower_ListContains(name, sky_asMap(ctx)["localFunctions"]))) { return func() any { goName := sky_concat(sky_asMap(ctx)["modulePrefix"], sky_concat("_", Compiler_Lower_CapitalizeFirst(Compiler_Lower_SanitizeGoIdent(name)))); _ = goName; return func() any { if sky_asBool(Compiler_Lower_IsZeroArityFn(name, ctx)) { return GoCallExpr(GoIdent(goName), []any{}) }; return GoIdent(goName) }() }() }; return func() any { if sky_asBool(Compiler_Lower_IsZeroArityFn(name, ctx)) { return GoCallExpr(GoIdent(Compiler_Lower_SanitizeGoIdent(name)), []any{}) }; return GoIdent(Compiler_Lower_SanitizeGoIdent(name)) }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() };  return nil }() }()
}

func Compiler_Lower_LowerConstructorValue(name any, info any) any {
	return GoMapLit([]any{SkyTuple2{V0: GoStringLit("Tag"), V1: GoBasicLit(sky_stringFromInt(sky_asMap(info)["tagIndex"]))}, SkyTuple2{V0: GoStringLit("SkyName"), V1: GoStringLit(name)}})
}

func Compiler_Lower_LowerQualified(ctx any, parts any) any {
	return func() any { qualName := sky_call(sky_stringJoin("."), parts); _ = qualName; return func() any { if sky_asBool(sky_equal(qualName, "String.fromInt")) { return GoIdent("sky_stringFromInt") }; return func() any { if sky_asBool(sky_equal(qualName, "String.fromFloat")) { return GoIdent("sky_stringFromFloat") }; return func() any { if sky_asBool(sky_equal(qualName, "String.toUpper")) { return GoIdent("sky_stringToUpper") }; return func() any { if sky_asBool(sky_equal(qualName, "String.toLower")) { return GoIdent("sky_stringToLower") }; return func() any { if sky_asBool(sky_equal(qualName, "String.length")) { return GoIdent("sky_stringLength") }; return func() any { if sky_asBool(sky_equal(qualName, "String.join")) { return GoIdent("sky_stringJoin") }; return func() any { if sky_asBool(sky_equal(qualName, "String.contains")) { return GoIdent("sky_stringContains") }; return func() any { if sky_asBool(sky_equal(qualName, "String.trim")) { return GoIdent("sky_stringTrim") }; return func() any { if sky_asBool(sky_equal(qualName, "String.isEmpty")) { return GoIdent("sky_stringIsEmpty") }; return func() any { if sky_asBool(sky_equal(qualName, "String.startsWith")) { return GoIdent("sky_stringStartsWith") }; return func() any { if sky_asBool(sky_equal(qualName, "String.endsWith")) { return GoIdent("sky_stringEndsWith") }; return func() any { if sky_asBool(sky_equal(qualName, "String.split")) { return GoIdent("sky_stringSplit") }; return func() any { if sky_asBool(sky_equal(qualName, "String.replace")) { return GoIdent("sky_stringReplace") }; return func() any { if sky_asBool(sky_equal(qualName, "String.toInt")) { return GoIdent("sky_stringToInt") }; return func() any { if sky_asBool(sky_equal(qualName, "String.toFloat")) { return GoIdent("sky_stringToFloat") }; return func() any { if sky_asBool(sky_equal(qualName, "String.append")) { return GoIdent("sky_stringAppend") }; return func() any { if sky_asBool(sky_equal(qualName, "String.slice")) { return GoIdent("sky_stringSlice") }; return func() any { if sky_asBool(sky_equal(qualName, "List.map")) { return GoIdent("sky_listMap") }; return func() any { if sky_asBool(sky_equal(qualName, "List.filter")) { return GoIdent("sky_listFilter") }; return func() any { if sky_asBool(sky_equal(qualName, "List.foldl")) { return GoIdent("sky_listFoldl") }; return func() any { if sky_asBool(sky_equal(qualName, "List.foldr")) { return GoIdent("sky_listFoldr") }; return func() any { if sky_asBool(sky_equal(qualName, "List.length")) { return GoIdent("sky_listLength") }; return func() any { if sky_asBool(sky_equal(qualName, "List.head")) { return GoIdent("sky_listHead") }; return func() any { if sky_asBool(sky_equal(qualName, "List.reverse")) { return GoIdent("sky_listReverse") }; return func() any { if sky_asBool(sky_equal(qualName, "List.isEmpty")) { return GoIdent("sky_listIsEmpty") }; return func() any { if sky_asBool(sky_equal(qualName, "List.append")) { return GoIdent("sky_listAppend") }; return func() any { if sky_asBool(sky_equal(qualName, "List.concatMap")) { return GoIdent("sky_listConcatMap") }; return func() any { if sky_asBool(sky_equal(qualName, "List.filterMap")) { return GoIdent("sky_listFilterMap") }; return func() any { if sky_asBool(sky_equal(qualName, "List.indexedMap")) { return GoIdent("sky_listIndexedMap") }; return func() any { if sky_asBool(sky_equal(qualName, "List.concat")) { return GoIdent("sky_listConcat") }; return func() any { if sky_asBool(sky_equal(qualName, "List.drop")) { return GoIdent("sky_listDrop") }; return func() any { if sky_asBool(sky_equal(qualName, "List.member")) { return GoIdent("sky_listMember") }; return func() any { if sky_asBool(sky_equal(qualName, "Log.println")) { return GoIdent("sky_println") }; return func() any { if sky_asBool(sky_equal(qualName, "File.readFile")) { return GoIdent("sky_fileRead") }; return func() any { if sky_asBool(sky_equal(qualName, "File.writeFile")) { return GoIdent("sky_fileWrite") }; return func() any { if sky_asBool(sky_equal(qualName, "File.mkdirAll")) { return GoIdent("sky_fileMkdirAll") }; return func() any { if sky_asBool(sky_equal(qualName, "Process.exit")) { return GoIdent("sky_processExit") }; return func() any { if sky_asBool(sky_equal(qualName, "Process.run")) { return GoIdent("sky_processRun") }; return func() any { if sky_asBool(sky_equal(qualName, "Args.getArgs")) { return GoIdent("sky_processGetArgs") }; return func() any { if sky_asBool(sky_equal(qualName, "Args.getArg")) { return GoIdent("sky_processGetArg") }; return func() any { if sky_asBool(sky_equal(qualName, "Ref.new")) { return GoIdent("sky_refNew") }; return func() any { if sky_asBool(sky_equal(qualName, "Ref.get")) { return GoIdent("sky_refGet") }; return func() any { if sky_asBool(sky_equal(qualName, "Ref.set")) { return GoIdent("sky_refSet") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.empty")) { return GoCallExpr(GoIdent("sky_dictEmpty"), []any{}) }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.insert")) { return GoIdent("sky_dictInsert") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.get")) { return GoIdent("sky_dictGet") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.keys")) { return GoIdent("sky_dictKeys") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.values")) { return GoIdent("sky_dictValues") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.toList")) { return GoIdent("sky_dictToList") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.fromList")) { return GoIdent("sky_dictFromList") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.map")) { return GoIdent("sky_dictMap") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.foldl")) { return GoIdent("sky_dictFoldl") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.union")) { return GoIdent("sky_dictUnion") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.remove")) { return GoIdent("sky_dictRemove") }; return func() any { if sky_asBool(sky_equal(qualName, "Dict.member")) { return GoIdent("sky_dictMember") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.empty")) { return GoCallExpr(GoIdent("sky_setEmpty"), []any{}) }; return func() any { if sky_asBool(sky_equal(qualName, "Set.singleton")) { return GoIdent("sky_setSingleton") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.insert")) { return GoIdent("sky_setInsert") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.member")) { return GoIdent("sky_setMember") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.union")) { return GoIdent("sky_setUnion") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.diff")) { return GoIdent("sky_setDiff") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.toList")) { return GoIdent("sky_setToList") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.fromList")) { return GoIdent("sky_setFromList") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.isEmpty")) { return GoIdent("sky_setIsEmpty") }; return func() any { if sky_asBool(sky_equal(qualName, "Set.remove")) { return GoIdent("sky_setRemove") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.isUpper")) { return GoIdent("sky_charIsUpper") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.isLower")) { return GoIdent("sky_charIsLower") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.isDigit")) { return GoIdent("sky_charIsDigit") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.isAlpha")) { return GoIdent("sky_charIsAlpha") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.isAlphaNum")) { return GoIdent("sky_charIsAlphaNum") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.toUpper")) { return GoIdent("sky_charToUpper") }; return func() any { if sky_asBool(sky_equal(qualName, "Char.toLower")) { return GoIdent("sky_charToLower") }; return func() any { if sky_asBool(sky_equal(qualName, "String.fromChar")) { return GoIdent("sky_stringFromChar") }; return func() any { if sky_asBool(sky_equal(qualName, "String.toList")) { return GoIdent("sky_stringToList") }; return func() any { if sky_asBool(sky_equal(qualName, "Io.readLine")) { return GoIdent("sky_readLine") }; return func() any { if sky_asBool(sky_equal(qualName, "Io.readBytes")) { return GoIdent("sky_readBytes") }; return func() any { if sky_asBool(sky_equal(qualName, "Io.writeStdout")) { return GoIdent("sky_writeStdout") }; return func() any { if sky_asBool(sky_equal(qualName, "Io.writeStderr")) { return GoIdent("sky_writeStderr") }; return func() any { if sky_asBool(sky_equal(sky_listLength(parts), 2)) { return func() any { modPart := func() any { return func() any { __subject := sky_listHead(parts); if <nil> { p := sky_asSkyMaybe(__subject).JustValue; _ = p; return p };  if <nil> { return "" };  return nil }() }(); _ = modPart; funcPart := func() any { return func() any { __subject := sky_listHead(sky_call(sky_listDrop(1), parts)); if <nil> { p := sky_asSkyMaybe(__subject).JustValue; _ = p; return p };  if <nil> { return "" };  return nil }() }(); _ = funcPart; return func() any { return func() any { __subject := sky_call(sky_dictGet(modPart), sky_asMap(ctx)["importAliases"]); if <nil> { qualModName := sky_asSkyMaybe(__subject).JustValue; _ = qualModName; return func() any { prefix := sky_call2(sky_stringReplace("."), "_", qualModName); _ = prefix; goName := sky_concat(prefix, sky_concat("_", Compiler_Lower_CapitalizeFirst(funcPart))); _ = goName; return GoCallExpr(GoIdent(goName), []any{}) }() };  if <nil> { return GoSelectorExpr(GoIdent(sky_stringToLower(modPart)), funcPart) };  return nil }() }() }() }; return GoIdent(Compiler_Lower_SanitizeGoIdent(qualName)) }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }()
}

func Compiler_Lower_LowerCall(ctx any, callee any, args any) any {
	return func() any { flatResult := Compiler_Lower_FlattenCall(callee, args); _ = flatResult; flatCallee := sky_fst(flatResult); _ = flatCallee; flatArgs := sky_snd(flatResult); _ = flatArgs; argCount := sky_listLength(flatArgs); _ = argCount; partialResult := Compiler_Lower_CheckPartialApplication(ctx, flatCallee, argCount); _ = partialResult; return func() any { return func() any { __subject := partialResult; if <nil> { closure := sky_asSkyMaybe(__subject).JustValue; _ = closure; return func() any { goArgs := sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Lower_LowerExpr(ctx, __pa0) }), flatArgs); _ = goArgs; return Compiler_Lower_GeneratePartialClosure(closure, goArgs, argCount) }() };  if <nil> { return func() any { goCallee := Compiler_Lower_LowerExpr(ctx, flatCallee); _ = goCallee; goArgs := sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Lower_LowerArgExpr(ctx, __pa0) }), flatArgs); _ = goArgs; return func() any { return func() any { __subject := goCallee; if <nil> { innerFn := sky_asMap(__subject)["V0"]; _ = innerFn; innerArgs := sky_asMap(__subject)["V1"]; _ = innerArgs; return func() any { if sky_asBool(sky_listIsEmpty(innerArgs)) { return GoCallExpr(innerFn, goArgs) }; return func() any { if sky_asBool(sky_equal(sky_listLength(goArgs), 1)) { return GoCallExpr(GoIdent("sky_call"), sky_call(sky_listAppend([]any{goCallee}), goArgs)) }; return func() any { if sky_asBool(sky_equal(sky_listLength(goArgs), 2)) { return GoCallExpr(GoIdent("sky_call2"), sky_call(sky_listAppend([]any{goCallee}), goArgs)) }; return func() any { if sky_asBool(sky_equal(sky_listLength(goArgs), 3)) { return GoCallExpr(GoIdent("sky_call3"), sky_call(sky_listAppend([]any{goCallee}), goArgs)) }; return GoCallExpr(GoIdent("sky_call"), sky_call(sky_listAppend([]any{goCallee}), goArgs)) }() }() }() }() };  if <nil> { code := sky_asMap(__subject)["V0"]; _ = code; return func() any { if sky_asBool(sky_call(sky_stringEndsWith("()"), code)) { return func() any { return func() any { __subject := goArgs; if <nil> { singleArg := sky_asList(__subject)[0]; _ = singleArg; return GoCallExpr(GoIdent("sky_call"), []any{goCallee, singleArg}) };  if true { return GoCallExpr(goCallee, goArgs) };  return nil }() }() }; return GoCallExpr(goCallee, goArgs) }() };  if true { return GoCallExpr(goCallee, goArgs) };  return nil }() }() }() };  return nil }() }() }()
}

func Compiler_Lower_CheckPartialApplication(ctx any, callee any, argCount any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(sky_asMap(ctx)["modulePrefix"])) { return SkyNothing() }; return func() any { return func() any { __subject := callee; if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { return func() any { __subject := sky_call(sky_dictGet(name), sky_asMap(ctx)["localFunctionArity"]); if <nil> { arity := sky_asSkyMaybe(__subject).JustValue; _ = arity; return func() any { if sky_asBool(sky_asInt(argCount) < sky_asInt(Compiler_Lower_Arity)) { return SkyJust(map[string]any{"goFuncName": sky_concat(sky_asMap(ctx)["modulePrefix"], sky_concat("_", Compiler_Lower_CapitalizeFirst(Compiler_Lower_SanitizeGoIdent(name)))), "totalArity": Compiler_Lower_Arity}) }; return SkyNothing() }() };  if <nil> { return SkyNothing() };  if true { return SkyNothing() };  return nil }() }() };  return nil }() }() }()
}

func Compiler_Lower_GeneratePartialClosure(partial any, providedArgs any, providedCount any) any {
	return func() any { remainingCount := sky_asInt(sky_asMap(partial)["totalArity"]) - sky_asInt(providedCount); _ = remainingCount; remainingParams := sky_call(sky_listMap(func(i any) any { return sky_concat("__pa", sky_stringFromInt(i)) }), Compiler_Lower_ListRange(0, sky_asInt(remainingCount) - sky_asInt(1))); _ = remainingParams; providedArgStrs := sky_call(sky_listMap(Compiler_Lower_EmitGoExprInline), providedArgs); _ = providedArgStrs; allArgStrs := sky_call(sky_listAppend(providedArgStrs), remainingParams); _ = allArgStrs; argList := sky_call(sky_stringJoin(", "), allArgStrs); _ = argList; innerCall := sky_concat(sky_asMap(partial)["goFuncName"], sky_concat("(", sky_concat(argList, ")"))); _ = innerCall; closureCode := Compiler_Lower_BuildCurriedClosure(remainingParams, innerCall); _ = closureCode; return GoRawExpr(closureCode) }()
}

func Compiler_Lower_BuildCurriedClosure(params any, innerCall any) any {
	return func() any { return func() any { __subject := params; if <nil> { return innerCall };  if <nil> { p := sky_asList(__subject)[0]; _ = p; rest := sky_asList(__subject)[1:]; _ = rest; return sky_concat("func(", sky_concat(p, sky_concat(" any) any { return ", sky_concat(Compiler_Lower_BuildCurriedClosure(rest, innerCall), " }")))) };  return nil }() }()
}

func Compiler_Lower_ListRange(start any, end any) any {
	return func() any { if sky_asBool(sky_asInt(start) > sky_asInt(end)) { return []any{} }; return append([]any{start}, sky_asList(Compiler_Lower_ListRange(sky_asInt(start) + sky_asInt(1), end))...) }()
}

func Compiler_Lower_FlattenCall(callee any, args any) any {
	return func() any { return func() any { __subject := callee; if <nil> { innerCallee := sky_asMap(__subject)["V0"]; _ = innerCallee; innerArgs := sky_asMap(__subject)["V1"]; _ = innerArgs; return func() any { if sky_asBool(Compiler_Lower_IsStdlibCallee(innerCallee)) { return SkyTuple2{V0: callee, V1: args} }; return Compiler_Lower_FlattenCall(innerCallee, sky_call(sky_listAppend(innerArgs), args)) }() };  if true { return SkyTuple2{V0: callee, V1: args} };  return nil }() }()
}

func Compiler_Lower_LowerLambda(ctx any, params any, body any) any {
	return func() any { if sky_asBool(sky_listIsEmpty(params)) { return Compiler_Lower_LowerExpr(ctx, body) }; return func() any { if sky_asBool(sky_equal(sky_listLength(params), 1)) { return func() any { singleParam := func() any { return func() any { __subject := sky_listHead(params); if <nil> { p := sky_asSkyMaybe(__subject).JustValue; _ = p; return p };  if <nil> { return PWildcard(emptySpan) };  return nil }() }(); _ = singleParam; paramStr := Compiler_Lower_EmitInlineParam(Compiler_Lower_LowerParam(singleParam)); _ = paramStr; bodyStr := Compiler_Lower_EmitGoExprInline(Compiler_Lower_LowerExpr(ctx, body)); _ = bodyStr; return GoRawExpr(sky_concat("func(", sky_concat(paramStr, sky_concat(") any { return ", sky_concat(bodyStr, " }"))))) }() }; return func() any { firstParam := func() any { return func() any { __subject := sky_listHead(params); if <nil> { p := sky_asSkyMaybe(__subject).JustValue; _ = p; return p };  if <nil> { return PWildcard(emptySpan) };  return nil }() }(); _ = firstParam; restParams := sky_call(sky_listDrop(1), params); _ = restParams; paramStr := Compiler_Lower_EmitInlineParam(Compiler_Lower_LowerParam(firstParam)); _ = paramStr; innerStr := Compiler_Lower_EmitGoExprInline(Compiler_Lower_LowerLambda(ctx, restParams, body)); _ = innerStr; return GoRawExpr(sky_concat("func(", sky_concat(paramStr, sky_concat(") any { return ", sky_concat(innerStr, " }"))))) }() }() }()
}

func Compiler_Lower_LowerIf(ctx any, condition any, thenBranch any, elseBranch any) any {
	return GoRawExpr(sky_concat("func() any { if sky_asBool(", sky_concat(Compiler_Lower_ExprToGoString(ctx, condition), sky_concat(") { return ", sky_concat(Compiler_Lower_ExprToGoString(ctx, thenBranch), sky_concat(" }; return ", sky_concat(Compiler_Lower_ExprToGoString(ctx, elseBranch), " }()")))))))
}

func Compiler_Lower_LowerLet(ctx any, bindings any, body any) any {
	return func() any { stmts := sky_call(sky_listConcatMap(func(__pa0 any) any { return Compiler_Lower_LowerLetBinding(ctx, __pa0) }), bindings); _ = stmts; returnStmt := []any{GoReturn(Compiler_Lower_LowerExpr(ctx, body))}; _ = returnStmt; allStmts := sky_call(sky_listAppend(stmts), returnStmt); _ = allStmts; return GoRawExpr(sky_concat("func() any { ", sky_concat(Compiler_Lower_StmtsToGoString(allStmts), " }()"))) }()
}

func Compiler_Lower_LowerLetBinding(ctx any, binding any) any {
	return func() any { return func() any { __subject := sky_asMap(binding)["pattern"]; if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { goName := Compiler_Lower_SanitizeGoIdent(name); _ = goName; return []any{GoShortDecl(goName, Compiler_Lower_LowerExpr(ctx, sky_asMap(binding)["value"])), GoExprStmt(GoRawExpr(sky_concat("_ = ", goName)))} }() };  if <nil> { return []any{GoExprStmt(Compiler_Lower_LowerExpr(ctx, sky_asMap(binding)["value"]))} };  if <nil> { items := sky_asMap(__subject)["V0"]; _ = items; return func() any { tmpName := sky_concat("__tup_", Compiler_Lower_MakeTupleKey(items)); _ = tmpName; tmpDecl := GoShortDecl(tmpName, Compiler_Lower_LowerExpr(ctx, sky_asMap(binding)["value"])); _ = tmpDecl; tupleSize := sky_listLength(items); _ = tupleSize; extracts := Compiler_Lower_ExtractTupleBindings(tmpName, items, 0, tupleSize); _ = extracts; return append([]any{tmpDecl}, sky_asList(extracts)...) }() };  if true { return []any{GoShortDecl("_", Compiler_Lower_LowerExpr(ctx, sky_asMap(binding)["value"]))} };  return nil }() }()
}

func Compiler_Lower_ExtractTupleBindings(tmpName any, items any, idx any, tupleSize any) any {
	return func() any { return func() any { __subject := items; if <nil> { return []any{} };  if <nil> { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { fieldName := sky_concat("V", sky_stringFromInt(idx)); _ = fieldName; assertFn := func() any { if sky_asBool(sky_asInt(tupleSize) >= sky_asInt(3)) { return "sky_asTuple3" }; return "sky_asTuple2" }(); _ = assertFn; extract := func() any { return func() any { __subject := pat; if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { goName := Compiler_Lower_SanitizeGoIdent(name); _ = goName; return []any{GoShortDecl(goName, GoSelectorExpr(GoCallExpr(GoIdent(assertFn), []any{GoIdent(tmpName)}), fieldName)), GoExprStmt(GoRawExpr(sky_concat("_ = ", goName)))} }() };  if true { return []any{} };  return nil }() }(); _ = extract; return sky_call(sky_listAppend(extract), Compiler_Lower_ExtractTupleBindings(tmpName, rest, sky_asInt(idx) + sky_asInt(1), tupleSize)) }() };  return nil }() }()
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
	return func() any { return func() any { __subject := pat; if <nil> { return "true" };  if <nil> { return "true" };  if <nil> { lit := sky_asMap(__subject)["V0"]; _ = lit; return func() any { return func() any { __subject := lit; if <nil> { n := sky_asMap(__subject)["V0"]; _ = n; return sky_concat("sky_asInt(", sky_concat(varName, sky_concat(") == ", sky_stringFromInt(n)))) };  if <nil> { f := sky_asMap(__subject)["V0"]; _ = f; return sky_concat("sky_asFloat(", sky_concat(varName, sky_concat(") == ", sky_stringFromFloat(f)))) };  if <nil> { s := sky_asMap(__subject)["V0"]; _ = s; return sky_concat("sky_asString(", sky_concat(varName, sky_concat(") == ", Compiler_Lower_GoQuote(s)))) };  if <nil> { b := sky_asMap(__subject)["V0"]; _ = b; return func() any { if sky_asBool(b) { return sky_concat("sky_asBool(", sky_concat(varName, ") == true")) }; return sky_concat("sky_asBool(", sky_concat(varName, ") == false")) }() };  if <nil> { c := sky_asMap(__subject)["V0"]; _ = c; return sky_concat("sky_asString(", sky_concat(varName, sky_concat(") == \"", sky_concat(c, "\"")))) };  if <nil> { parts := sky_asMap(__subject)["V0"]; _ = parts; return func() any { ctorName := Compiler_Lower_LastPartOf(parts); _ = ctorName; return func() any { if sky_asBool(sky_asBool(sky_equal(ctorName, "Ok")) || sky_asBool(sky_equal(ctorName, "Err"))) { return sky_concat("sky_asSkyResult(", sky_concat(varName, sky_concat(").SkyName == \"", sky_concat(ctorName, "\"")))) }; return func() any { if sky_asBool(sky_asBool(sky_equal(ctorName, "Just")) || sky_asBool(sky_equal(ctorName, "Nothing"))) { return sky_concat("sky_asSkyMaybe(", sky_concat(varName, sky_concat(").SkyName == \"", sky_concat(ctorName, "\"")))) }; return func() any { if sky_asBool(sky_equal(ctorName, "True")) { return sky_concat("sky_asBool(", sky_concat(varName, ") == true")) }; return func() any { if sky_asBool(sky_equal(ctorName, "False")) { return sky_concat("sky_asBool(", sky_concat(varName, ") == false")) }; return sky_concat("sky_asMap(", sky_concat(varName, sky_concat(")[\"SkyName\"] == \"", sky_concat(ctorName, "\"")))) }() }() }() }() }() };  if <nil> { return "true" };  if <nil> { items := sky_asMap(__subject)["V0"]; _ = items; return sky_concat("len(sky_asList(", sky_concat(varName, sky_concat(")) == ", sky_stringFromInt(sky_listLength(items))))) };  if <nil> { return sky_concat("len(sky_asList(", sky_concat(varName, ")) > 0")) };  if <nil> { inner := sky_asMap(__subject)["V0"]; _ = inner; return Compiler_Lower_PatternToCondition(ctx, varName, inner) };  if <nil> { return "true" };  return nil }() }() };  return nil }() }()
}

func Compiler_Lower_PatternToBindings(ctx any, varName any, pat any) any {
	return func() any { return func() any { __subject := pat; if <nil> { return "" };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { goName := Compiler_Lower_SanitizeGoIdent(name); _ = goName; return sky_concat(goName, sky_concat(" := ", sky_concat(varName, sky_concat("; _ = ", sky_concat(goName, "; "))))) }() };  if <nil> { return "" };  if <nil> { parts := sky_asMap(__subject)["V0"]; _ = parts; argPats := sky_asMap(__subject)["V1"]; _ = argPats; return func() any { ctorName := Compiler_Lower_LastPartOf(parts); _ = ctorName; return func() any { if sky_asBool(sky_equal(ctorName, "Ok")) { return Compiler_Lower_BindConstructorArgs(ctx, varName, "sky_asSkyResult", "OkValue", argPats) }; return func() any { if sky_asBool(sky_equal(ctorName, "Err")) { return Compiler_Lower_BindConstructorArgs(ctx, varName, "sky_asSkyResult", "ErrValue", argPats) }; return func() any { if sky_asBool(sky_equal(ctorName, "Just")) { return Compiler_Lower_BindConstructorArgs(ctx, varName, "sky_asSkyMaybe", "JustValue", argPats) }; return Compiler_Lower_BindAdtConstructorArgs(ctx, varName, argPats, 0) }() }() }() }() };  if <nil> { items := sky_asMap(__subject)["V0"]; _ = items; return Compiler_Lower_BindTupleArgs(ctx, varName, items, 0) };  if <nil> { items := sky_asMap(__subject)["V0"]; _ = items; return Compiler_Lower_BindListArgs(ctx, varName, items, 0) };  if <nil> { headPat := sky_asMap(__subject)["V0"]; _ = headPat; tailPat := sky_asMap(__subject)["V1"]; _ = tailPat; return func() any { headBinding := Compiler_Lower_PatternToBindings(ctx, sky_concat("sky_asList(", sky_concat(varName, ")[0]")), headPat); _ = headBinding; tailBinding := Compiler_Lower_PatternToBindings(ctx, sky_concat("sky_asList(", sky_concat(varName, ")[1:]")), tailPat); _ = tailBinding; return sky_concat(headBinding, tailBinding) }() };  if <nil> { inner := sky_asMap(__subject)["V0"]; _ = inner; name := sky_asMap(__subject)["V1"]; _ = name; return sky_concat(Compiler_Lower_SanitizeGoIdent(name), sky_concat(" := ", sky_concat(varName, sky_concat("; ", Compiler_Lower_PatternToBindings(ctx, varName, inner))))) };  if <nil> { fields := sky_asMap(__subject)["V0"]; _ = fields; return sky_call2(sky_listFoldl(func(f any) any { return func(acc any) any { return sky_concat(acc, sky_concat(Compiler_Lower_SanitizeGoIdent(f), sky_concat(" := sky_asMap(", sky_concat(varName, sky_concat(")[\"", sky_concat(f, "\"]; ")))))) } }), "", fields) };  return nil }() }()
}

func Compiler_Lower_BindConstructorArgs(ctx any, varName any, wrapperFn any, fieldName any, argPats any) any {
	return func() any { return func() any { __subject := argPats; if <nil> { onePat := sky_asList(__subject)[0]; _ = onePat; return Compiler_Lower_PatternToBindings(ctx, sky_concat(wrapperFn, sky_concat("(", sky_concat(varName, sky_concat(").", fieldName)))), onePat) };  if true { return "" };  return nil }() }()
}

func Compiler_Lower_BindAdtConstructorArgs(ctx any, varName any, argPats any, idx any) any {
	return func() any { return func() any { __subject := argPats; if <nil> { return "" };  if <nil> { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { fieldAccess := sky_concat("sky_asMap(", sky_concat(varName, sky_concat(")[\"V", sky_concat(sky_stringFromInt(idx), "\"]")))); _ = fieldAccess; binding := Compiler_Lower_PatternToBindings(ctx, fieldAccess, pat); _ = binding; return sky_concat(binding, Compiler_Lower_BindAdtConstructorArgs(ctx, varName, rest, sky_asInt(idx) + sky_asInt(1))) }() };  return nil }() }()
}

func Compiler_Lower_BindTupleArgs(ctx any, varName any, items any, idx any) any {
	return func() any { return func() any { __subject := items; if <nil> { return "" };  if <nil> { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { fieldAccess := sky_concat("sky_asTuple2(", sky_concat(varName, sky_concat(").V", sky_stringFromInt(idx)))); _ = fieldAccess; binding := Compiler_Lower_PatternToBindings(ctx, fieldAccess, pat); _ = binding; return sky_concat(binding, Compiler_Lower_BindTupleArgs(ctx, varName, rest, sky_asInt(idx) + sky_asInt(1))) }() };  return nil }() }()
}

func Compiler_Lower_BindListArgs(ctx any, varName any, items any, idx any) any {
	return func() any { return func() any { __subject := items; if <nil> { return "" };  if <nil> { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { elemAccess := sky_concat("sky_asList(", sky_concat(varName, sky_concat(")[", sky_concat(sky_stringFromInt(idx), "]")))); _ = elemAccess; binding := Compiler_Lower_PatternToBindings(ctx, elemAccess, pat); _ = binding; return sky_concat(binding, Compiler_Lower_BindListArgs(ctx, varName, rest, sky_asInt(idx) + sky_asInt(1))) }() };  return nil }() }()
}

func Compiler_Lower_LowerBinary(ctx any, op any, left any, right any) any {
	return func() any { goLeft := Compiler_Lower_LowerExpr(ctx, left); _ = goLeft; goRight := Compiler_Lower_LowerExpr(ctx, right); _ = goRight; return func() any { if sky_asBool(sky_equal(op, "|>")) { return GoCallExpr(goRight, []any{goLeft}) }; return func() any { if sky_asBool(sky_equal(op, "<|")) { return GoCallExpr(goLeft, []any{goRight}) }; return func() any { if sky_asBool(sky_equal(op, "::")) { return GoCallExpr(GoIdent("append"), []any{GoSliceLit([]any{goLeft}), GoRawExpr(sky_concat("sky_asList(", sky_concat(Compiler_Lower_EmitGoExprInline(goRight), ")...")))}) }; return func() any { if sky_asBool(sky_equal(op, "++")) { return GoRawExpr(sky_concat("sky_concat(", sky_concat(Compiler_Lower_EmitGoExprInline(goLeft), sky_concat(", ", sky_concat(Compiler_Lower_EmitGoExprInline(goRight), ")"))))) }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "+")) || sky_asBool(sky_asBool(sky_equal(op, "-")) || sky_asBool(sky_asBool(sky_equal(op, "*")) || sky_asBool(sky_equal(op, "%"))))) { return GoRawExpr(sky_concat("sky_asInt(", sky_concat(Compiler_Lower_EmitGoExprInline(goLeft), sky_concat(") ", sky_concat(op, sky_concat(" sky_asInt(", sky_concat(Compiler_Lower_EmitGoExprInline(goRight), ")"))))))) }; return func() any { if sky_asBool(sky_equal(op, "/")) { return GoRawExpr(sky_concat("sky_asFloat(", sky_concat(Compiler_Lower_EmitGoExprInline(goLeft), sky_concat(") / sky_asFloat(", sky_concat(Compiler_Lower_EmitGoExprInline(goRight), ")"))))) }; return func() any { if sky_asBool(sky_equal(op, "//")) { return GoRawExpr(sky_concat("sky_asInt(", sky_concat(Compiler_Lower_EmitGoExprInline(goLeft), sky_concat(") / sky_asInt(", sky_concat(Compiler_Lower_EmitGoExprInline(goRight), ")"))))) }; return func() any { if sky_asBool(sky_equal(op, "/=")) { return GoRawExpr(sky_concat("!sky_equal(", sky_concat(Compiler_Lower_EmitGoExprInline(goLeft), sky_concat(", ", sky_concat(Compiler_Lower_EmitGoExprInline(goRight), ")"))))) }; return func() any { if sky_asBool(sky_equal(op, "==")) { return GoCallExpr(GoIdent("sky_equal"), []any{goLeft, goRight}) }; return func() any { if sky_asBool(sky_equal(op, "!=")) { return GoUnaryExpr("!", GoCallExpr(GoIdent("sky_equal"), []any{goLeft, goRight})) }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "<")) || sky_asBool(sky_asBool(sky_equal(op, "<=")) || sky_asBool(sky_asBool(sky_equal(op, ">")) || sky_asBool(sky_equal(op, ">="))))) { return GoRawExpr(sky_concat("sky_asInt(", sky_concat(Compiler_Lower_EmitGoExprInline(goLeft), sky_concat(") ", sky_concat(op, sky_concat(" sky_asInt(", sky_concat(Compiler_Lower_EmitGoExprInline(goRight), ")"))))))) }; return func() any { if sky_asBool(sky_equal(op, "&&")) { return GoRawExpr(sky_concat("sky_asBool(", sky_concat(Compiler_Lower_EmitGoExprInline(goLeft), sky_concat(") && sky_asBool(", sky_concat(Compiler_Lower_EmitGoExprInline(goRight), ")"))))) }; return func() any { if sky_asBool(sky_equal(op, "||")) { return GoRawExpr(sky_concat("sky_asBool(", sky_concat(Compiler_Lower_EmitGoExprInline(goLeft), sky_concat(") || sky_asBool(", sky_concat(Compiler_Lower_EmitGoExprInline(goRight), ")"))))) }; return GoBinaryExpr(op, goLeft, goRight) }() }() }() }() }() }() }() }() }() }() }() }() }() }()
}

func Compiler_Lower_LowerRecordUpdate(ctx any, base any, fields any) any {
	return func() any { goBase := Compiler_Lower_LowerExpr(ctx, base); _ = goBase; goFields := GoMapLit(sky_call(sky_listMap(func(f any) any { return SkyTuple2{V0: GoStringLit(sky_asMap(f)["name"]), V1: Compiler_Lower_LowerExpr(ctx, sky_asMap(f)["value"])} }), fields)); _ = goFields; return GoCallExpr(GoIdent("sky_recordUpdate"), []any{goBase, goFields}) }()
}

func Compiler_Lower_GenerateConstructorDecls(registry any, decls any) any {
	return sky_call(sky_listConcatMap(func(__pa0 any) any { return Compiler_Lower_GenerateCtorsForDecl(Compiler_Lower_Registry, __pa0) }), decls)
}

func Compiler_Lower_GenerateCtorsForDecl(registry any, decl any) any {
	return func() any { return func() any { __subject := decl; if <nil> { typeName := sky_asMap(__subject)["V0"]; _ = typeName; variants := sky_asMap(__subject)["V2"]; _ = variants; return sky_call(sky_listIndexedMap(func(__pa0 any) any { return func(__pa1 any) any { return Compiler_Lower_GenerateCtorFunc(typeName, __pa0, __pa1) } }), variants) };  if true { return []any{} };  return nil }() }()
}

func Compiler_Lower_GenerateCtorFunc(typeName any, tagIndex any, variant any) any {
	return func() any { arity := sky_listLength(sky_asMap(variant)["fields"]); _ = arity; return func() any { if sky_asBool(sky_equal(Compiler_Lower_Arity, 0)) { return GoDeclVar(Compiler_Lower_SanitizeGoIdent(sky_asMap(variant)["name"]), GoMapLit([]any{SkyTuple2{V0: GoStringLit("Tag"), V1: GoBasicLit(sky_stringFromInt(Compiler_Lower_TagIndex))}, SkyTuple2{V0: GoStringLit("SkyName"), V1: GoStringLit(sky_asMap(variant)["name"])}})) }; return func() any { params := sky_call(sky_listIndexedMap(func(i any) any { return func(_ any) any { return map[string]any{"name": sky_concat("v", sky_stringFromInt(i)), "type_": "any"} } }), sky_asMap(variant)["fields"]); _ = params; fields := sky_concat([]any{SkyTuple2{V0: "Tag", V1: GoBasicLit(sky_stringFromInt(Compiler_Lower_TagIndex))}, SkyTuple2{V0: "SkyName", V1: GoStringLit(sky_asMap(variant)["name"])}}, sky_call(sky_listIndexedMap(func(i any) any { return func(_ any) any { return SkyTuple2{V0: sky_concat("V", sky_stringFromInt(i)), V1: GoIdent(sky_concat("v", sky_stringFromInt(i)))} } }), sky_asMap(variant)["fields"])); _ = fields; return GoDeclFunc(map[string]any{"name": Compiler_Lower_SanitizeGoIdent(sky_asMap(variant)["name"]), "params": params, "returnType": "any", "body": []any{GoReturn(GoMapLit(sky_call(sky_listMap(func(pair any) any { return SkyTuple2{V0: GoStringLit(sky_fst(pair)), V1: sky_snd(pair)} }), fields)))}}) }() }() }()
}

func Compiler_Lower_GenerateHelperDecls() any {
	return []any{GoDeclRaw("type SkyTuple2 struct { V0, V1 any }"), GoDeclRaw("type SkyTuple3 struct { V0, V1, V2 any }"), GoDeclRaw("type SkyResult struct { Tag int; SkyName string; OkValue, ErrValue any }"), GoDeclRaw("type SkyMaybe struct { Tag int; SkyName string; JustValue any }"), GoDeclRaw("func SkyOk(v any) SkyResult { return SkyResult{Tag: 0, SkyName: \"Ok\", OkValue: v} }"), GoDeclRaw("func SkyErr(v any) SkyResult { return SkyResult{Tag: 1, SkyName: \"Err\", ErrValue: v} }"), GoDeclRaw("func SkyJust(v any) SkyMaybe { return SkyMaybe{Tag: 0, SkyName: \"Just\", JustValue: v} }"), GoDeclRaw("func SkyNothing() SkyMaybe { return SkyMaybe{Tag: 1, SkyName: \"Nothing\"} }"), GoDeclRaw("func sky_asInt(v any) int { switch x := v.(type) { case int: return x; case float64: return int(x); default: return 0 } }"), GoDeclRaw("func sky_asFloat(v any) float64 { switch x := v.(type) { case float64: return x; case int: return float64(x); default: return 0 } }"), GoDeclRaw("func sky_asString(v any) string { if s, ok := v.(string); ok { return s }; return fmt.Sprintf(\"%v\", v) }"), GoDeclRaw("func sky_asBool(v any) bool { if b, ok := v.(bool); ok { return b }; return false }"), GoDeclRaw("func sky_asList(v any) []any { if l, ok := v.([]any); ok { return l }; return nil }"), GoDeclRaw("func sky_asMap(v any) map[string]any { if m, ok := v.(map[string]any); ok { return m }; return nil }"), GoDeclRaw("func sky_equal(a, b any) bool { return fmt.Sprintf(\"%v\", a) == fmt.Sprintf(\"%v\", b) }"), GoDeclRaw("func sky_concat(a, b any) any { if la, ok := a.([]any); ok { if lb, ok := b.([]any); ok { return append(la, lb...) } }; return sky_asString(a) + sky_asString(b) }"), GoDeclRaw("func sky_stringFromInt(v any) any { return strconv.Itoa(sky_asInt(v)) }"), GoDeclRaw("func sky_stringFromFloat(v any) any { return strconv.FormatFloat(sky_asFloat(v), 'f', -1, 64) }"), GoDeclRaw("func sky_stringToUpper(v any) any { return strings.ToUpper(sky_asString(v)) }"), GoDeclRaw("func sky_stringToLower(v any) any { return strings.ToLower(sky_asString(v)) }"), GoDeclRaw("func sky_stringLength(v any) any { return len(sky_asString(v)) }"), GoDeclRaw("func sky_stringTrim(v any) any { return strings.TrimSpace(sky_asString(v)) }"), GoDeclRaw("func sky_stringContains(sub any) any { return func(s any) any { return strings.Contains(sky_asString(s), sky_asString(sub)) } }"), GoDeclRaw("func sky_stringStartsWith(prefix any) any { return func(s any) any { return strings.HasPrefix(sky_asString(s), sky_asString(prefix)) } }"), GoDeclRaw("func sky_stringEndsWith(suffix any) any { return func(s any) any { return strings.HasSuffix(sky_asString(s), sky_asString(suffix)) } }"), GoDeclRaw("func sky_stringSplit(sep any) any { return func(s any) any { parts := strings.Split(sky_asString(s), sky_asString(sep)); result := make([]any, len(parts)); for i, p := range parts { result[i] = p }; return result } }"), GoDeclRaw("func sky_stringReplace(old any) any { return func(new_ any) any { return func(s any) any { return strings.ReplaceAll(sky_asString(s), sky_asString(old), sky_asString(new_)) } } }"), GoDeclRaw("func sky_stringToInt(s any) any { n, err := strconv.Atoi(strings.TrimSpace(sky_asString(s))); if err != nil { return SkyNothing() }; return SkyJust(n) }"), GoDeclRaw("func sky_stringToFloat(s any) any { f, err := strconv.ParseFloat(strings.TrimSpace(sky_asString(s)), 64); if err != nil { return SkyNothing() }; return SkyJust(f) }"), GoDeclRaw("func sky_stringAppend(a any) any { return func(b any) any { return sky_asString(a) + sky_asString(b) } }"), GoDeclRaw("func sky_stringIsEmpty(v any) any { return sky_asString(v) == \"\" }"), GoDeclRaw("func sky_stringSlice(start any) any { return func(end any) any { return func(s any) any { str := sky_asString(s); return str[sky_asInt(start):sky_asInt(end)] } } }"), GoDeclRaw("func sky_stringJoin(sep any) any { return func(list any) any { parts := sky_asList(list); ss := make([]string, len(parts)); for i, p := range parts { ss[i] = sky_asString(p) }; return strings.Join(ss, sky_asString(sep)) } }"), GoDeclRaw("func sky_listMap(fn any) any { return func(list any) any { items := sky_asList(list); result := make([]any, len(items)); for i, item := range items { result[i] = fn.(func(any) any)(item) }; return result } }"), GoDeclRaw("func sky_listFilter(fn any) any { return func(list any) any { items := sky_asList(list); var result []any; for _, item := range items { if sky_asBool(fn.(func(any) any)(item)) { result = append(result, item) } }; return result } }"), GoDeclRaw("func sky_listFoldl(fn any) any { return func(init any) any { return func(list any) any { acc := init; for _, item := range sky_asList(list) { acc = fn.(func(any) any)(item).(func(any) any)(acc) }; return acc } } }"), GoDeclRaw("func sky_listFoldr(fn any) any { return func(init any) any { return func(list any) any { items := sky_asList(list); acc := init; for i := len(items) - 1; i >= 0; i-- { acc = fn.(func(any) any)(items[i]).(func(any) any)(acc) }; return acc } } }"), GoDeclRaw("func sky_listLength(list any) any { return len(sky_asList(list)) }"), GoDeclRaw("func sky_listHead(list any) any { items := sky_asList(list); if len(items) > 0 { return SkyJust(items[0]) }; return SkyNothing() }"), GoDeclRaw("func sky_listReverse(list any) any { items := sky_asList(list); result := make([]any, len(items)); for i, item := range items { result[len(items)-1-i] = item }; return result }"), GoDeclRaw("func sky_listIsEmpty(list any) any { return len(sky_asList(list)) == 0 }"), GoDeclRaw("func sky_listAppend(a any) any { return func(b any) any { return append(sky_asList(a), sky_asList(b)...) } }"), GoDeclRaw("func sky_listConcatMap(fn any) any { return func(list any) any { var result []any; for _, item := range sky_asList(list) { result = append(result, sky_asList(fn.(func(any) any)(item))...) }; if result == nil { return []any{} }; return result } }"), GoDeclRaw("func sky_listConcat(lists any) any { var result []any; for _, l := range sky_asList(lists) { result = append(result, sky_asList(l)...) }; if result == nil { return []any{} }; return result }"), GoDeclRaw("func sky_listFilterMap(fn any) any { return func(list any) any { var result []any; for _, item := range sky_asList(list) { r := fn.(func(any) any)(item); if m, ok := r.(SkyMaybe); ok && m.Tag == 0 { result = append(result, m.JustValue) } }; if result == nil { return []any{} }; return result } }"), GoDeclRaw("func sky_listIndexedMap(fn any) any { return func(list any) any { items := sky_asList(list); result := make([]any, len(items)); for i, item := range items { result[i] = fn.(func(any) any)(i).(func(any) any)(item) }; return result } }"), GoDeclRaw("func sky_listDrop(n any) any { return func(list any) any { items := sky_asList(list); c := sky_asInt(n); if c >= len(items) { return []any{} }; return items[c:] } }"), GoDeclRaw("func sky_listMember(item any) any { return func(list any) any { for _, x := range sky_asList(list) { if sky_equal(x, item) { return true } }; return false } }"), GoDeclRaw("func sky_recordUpdate(base any, updates any) any { m := sky_asMap(base); result := make(map[string]any); for k, v := range m { result[k] = v }; for k, v := range sky_asMap(updates) { result[k] = v }; return result }"), GoDeclRaw("func sky_println(args ...any) any { ss := make([]any, len(args)); for i, a := range args { ss[i] = sky_asString(a) }; fmt.Println(ss...); return struct{}{} }"), GoDeclRaw("func sky_exit(code any) any { os.Exit(sky_asInt(code)); return struct{}{} }"), GoDeclRaw("var _ = strings.Contains"), GoDeclRaw("var _ = strconv.Itoa"), GoDeclRaw("var _ = os.Exit"), GoDeclRaw("func sky_asSkyResult(v any) SkyResult { if r, ok := v.(SkyResult); ok { return r }; return SkyResult{} }"), GoDeclRaw("func sky_asSkyMaybe(v any) SkyMaybe { if m, ok := v.(SkyMaybe); ok { return m }; return SkyMaybe{Tag: 1, SkyName: \"Nothing\"} }"), GoDeclRaw("func sky_asTuple2(v any) SkyTuple2 { if t, ok := v.(SkyTuple2); ok { return t }; return SkyTuple2{} }"), GoDeclRaw("func sky_asTuple3(v any) SkyTuple3 { if t, ok := v.(SkyTuple3); ok { return t }; return SkyTuple3{} }"), GoDeclRaw("func sky_not(v any) any { return !sky_asBool(v) }"), GoDeclRaw("func sky_fileRead(path any) any { data, err := os.ReadFile(sky_asString(path)); if err != nil { return SkyErr(err.Error()) }; return SkyOk(string(data)) }"), GoDeclRaw("func sky_fileWrite(path any) any { return func(content any) any { err := os.WriteFile(sky_asString(path), []byte(sky_asString(content)), 0644); if err != nil { return SkyErr(err.Error()) }; return SkyOk(struct{}{}) } }"), GoDeclRaw("func sky_fileMkdirAll(path any) any { err := os.MkdirAll(sky_asString(path), 0755); if err != nil { return SkyErr(err.Error()) }; return SkyOk(struct{}{}) }"), GoDeclRaw("func sky_processRun(cmd any) any { return func(args any) any { argStrs := sky_asList(args); cmdArgs := make([]string, len(argStrs)); for i, a := range argStrs { cmdArgs[i] = sky_asString(a) }; out, err := exec.Command(sky_asString(cmd), cmdArgs...).CombinedOutput(); if err != nil { return SkyErr(err.Error() + \": \" + string(out)) }; return SkyOk(string(out)) } }"), GoDeclRaw("func sky_processExit(code any) any { os.Exit(sky_asInt(code)); return struct{}{} }"), GoDeclRaw("func sky_processGetArgs(u any) any { args := make([]any, len(os.Args)); for i, a := range os.Args { args[i] = a }; return args }"), GoDeclRaw("func sky_processGetArg(n any) any { idx := sky_asInt(n); if idx < len(os.Args) { return SkyJust(os.Args[idx]) }; return SkyNothing() }"), GoDeclRaw("func sky_refNew(v any) any { return &SkyRef{Value: v} }"), GoDeclRaw("type SkyRef struct { Value any }"), GoDeclRaw("func sky_refGet(r any) any { return r.(*SkyRef).Value }"), GoDeclRaw("func sky_refSet(v any) any { return func(r any) any { r.(*SkyRef).Value = v; return struct{}{} } }"), GoDeclRaw("func sky_dictEmpty() any { return map[string]any{} }"), GoDeclRaw("func sky_dictInsert(k any) any { return func(v any) any { return func(d any) any { m := sky_asMap(d); result := make(map[string]any, len(m)+1); for key, val := range m { result[key] = val }; result[sky_asString(k)] = v; return result } } }"), GoDeclRaw("func sky_dictGet(k any) any { return func(d any) any { m := sky_asMap(d); if v, ok := m[sky_asString(k)]; ok { return SkyJust(v) }; return SkyNothing() } }"), GoDeclRaw("func sky_dictKeys(d any) any { m := sky_asMap(d); keys := make([]any, 0, len(m)); for k := range m { keys = append(keys, k) }; return keys }"), GoDeclRaw("func sky_dictValues(d any) any { m := sky_asMap(d); vals := make([]any, 0, len(m)); for _, v := range m { vals = append(vals, v) }; return vals }"), GoDeclRaw("func sky_dictToList(d any) any { m := sky_asMap(d); pairs := make([]any, 0, len(m)); for k, v := range m { pairs = append(pairs, SkyTuple2{k, v}) }; return pairs }"), GoDeclRaw("func sky_dictFromList(list any) any { result := make(map[string]any); for _, item := range sky_asList(list) { t := sky_asTuple2(item); result[sky_asString(t.V0)] = t.V1 }; return result }"), GoDeclRaw("func sky_dictMap(fn any) any { return func(d any) any { m := sky_asMap(d); result := make(map[string]any, len(m)); for k, v := range m { result[k] = fn.(func(any) any)(k).(func(any) any)(v) }; return result } }"), GoDeclRaw("func sky_dictFoldl(fn any) any { return func(init any) any { return func(d any) any { acc := init; for k, v := range sky_asMap(d) { acc = fn.(func(any) any)(k).(func(any) any)(v).(func(any) any)(acc) }; return acc } } }"), GoDeclRaw("func sky_dictUnion(a any) any { return func(b any) any { ma, mb := sky_asMap(a), sky_asMap(b); result := make(map[string]any, len(ma)+len(mb)); for k, v := range mb { result[k] = v }; for k, v := range ma { result[k] = v }; return result } }"), GoDeclRaw("func sky_dictRemove(k any) any { return func(d any) any { m := sky_asMap(d); result := make(map[string]any, len(m)); key := sky_asString(k); for k2, v := range m { if k2 != key { result[k2] = v } }; return result } }"), GoDeclRaw("func sky_dictMember(k any) any { return func(d any) any { _, ok := sky_asMap(d)[sky_asString(k)]; return ok } }"), GoDeclRaw("func sky_setEmpty() any { return map[string]bool{} }"), GoDeclRaw("func sky_setSingleton(v any) any { return map[string]bool{sky_asString(v): true} }"), GoDeclRaw("func sky_setInsert(v any) any { return func(s any) any { m := s.(map[string]bool); result := make(map[string]bool, len(m)+1); for k := range m { result[k] = true }; result[sky_asString(v)] = true; return result } }"), GoDeclRaw("func sky_setMember(v any) any { return func(s any) any { return s.(map[string]bool)[sky_asString(v)] } }"), GoDeclRaw("func sky_setUnion(a any) any { return func(b any) any { ma, mb := a.(map[string]bool), b.(map[string]bool); result := make(map[string]bool, len(ma)+len(mb)); for k := range mb { result[k] = true }; for k := range ma { result[k] = true }; return result } }"), GoDeclRaw("func sky_setDiff(a any) any { return func(b any) any { ma, mb := a.(map[string]bool), b.(map[string]bool); result := make(map[string]bool); for k := range ma { if !mb[k] { result[k] = true } }; return result } }"), GoDeclRaw("func sky_setToList(s any) any { m := s.(map[string]bool); result := make([]any, 0, len(m)); for k := range m { result = append(result, k) }; return result }"), GoDeclRaw("func sky_setFromList(list any) any { result := make(map[string]bool); for _, item := range sky_asList(list) { result[sky_asString(item)] = true }; return result }"), GoDeclRaw("func sky_setIsEmpty(s any) any { return len(s.(map[string]bool)) == 0 }"), GoDeclRaw("func sky_setRemove(v any) any { return func(s any) any { m := s.(map[string]bool); result := make(map[string]bool, len(m)); key := sky_asString(v); for k := range m { if k != key { result[k] = true } }; return result } }"), GoDeclRaw("func sky_readLine(u any) any { if stdinReader == nil { stdinReader = bufio.NewReader(os.Stdin) }; line, err := stdinReader.ReadString('\\n'); if err != nil && len(line) == 0 { return SkyNothing() }; return SkyJust(strings.TrimRight(line, \"\\r\\n\")) }"), GoDeclRaw("func sky_readBytes(n any) any { if stdinReader == nil { stdinReader = bufio.NewReader(os.Stdin) }; count := sky_asInt(n); buf := make([]byte, count); total := 0; for total < count { nr, err := stdinReader.Read(buf[total:]); total += nr; if err != nil { break } }; if total == 0 { return SkyNothing() }; return SkyJust(string(buf[:total])) }"), GoDeclRaw("func sky_writeStdout(s any) any { fmt.Print(sky_asString(s)); return struct{}{} }"), GoDeclRaw("func sky_writeStderr(s any) any { fmt.Fprint(os.Stderr, sky_asString(s)); return struct{}{} }"), GoDeclRaw("var stdinReader *bufio.Reader"), GoDeclRaw("func sky_charIsUpper(c any) any { s := sky_asString(c); if len(s) > 0 { r := rune(s[0]); return r >= 'A' && r <= 'Z' }; return false }"), GoDeclRaw("func sky_charIsLower(c any) any { s := sky_asString(c); if len(s) > 0 { r := rune(s[0]); return r >= 'a' && r <= 'z' }; return false }"), GoDeclRaw("func sky_charIsDigit(c any) any { s := sky_asString(c); if len(s) > 0 { r := rune(s[0]); return r >= '0' && r <= '9' }; return false }"), GoDeclRaw("func sky_charIsAlpha(c any) any { s := sky_asString(c); if len(s) > 0 { r := rune(s[0]); return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') }; return false }"), GoDeclRaw("func sky_charIsAlphaNum(c any) any { s := sky_asString(c); if len(s) > 0 { r := rune(s[0]); return (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') }; return false }"), GoDeclRaw("func sky_charToUpper(c any) any { return strings.ToUpper(sky_asString(c)) }"), GoDeclRaw("func sky_charToLower(c any) any { return strings.ToLower(sky_asString(c)) }"), GoDeclRaw("func sky_stringFromChar(c any) any { return sky_asString(c) }"), GoDeclRaw("func sky_stringToList(s any) any { str := sky_asString(s); result := make([]any, len(str)); for i, r := range str { result[i] = string(r) }; return result }"), GoDeclRaw("var _ = exec.Command"), GoDeclRaw("var _ = bufio.NewReader"), GoDeclRaw("func sky_escapeGoString(s any) any { q := strconv.Quote(sky_asString(s)); return q[1:len(q)-1] }"), GoDeclRaw("func sky_fst(t any) any { return sky_asTuple2(t).V0 }"), GoDeclRaw("func sky_snd(t any) any { return sky_asTuple2(t).V1 }"), GoDeclRaw("func sky_errorToString(e any) any { return sky_asString(e) }"), GoDeclRaw("func sky_identity(v any) any { return v }"), GoDeclRaw("func sky_always(a any) any { return func(b any) any { return a } }"), GoDeclRaw("func sky_js(v any) any { return v }"), GoDeclRaw("func sky_call(f any, arg any) any { return f.(func(any) any)(arg) }"), GoDeclRaw("func sky_call2(f any, a any, b any) any { return f.(func(any) any)(a).(func(any) any)(b) }"), GoDeclRaw("func sky_call3(f any, a any, b any, c any) any { return f.(func(any) any)(a).(func(any) any)(b).(func(any) any)(c) }")}
}

func Compiler_Lower_CollectLocalFunctions(decls any) any {
	return sky_call(sky_listFilterMap(func(d any) any { return func() any { return func() any { __subject := d; if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return SkyJust(name) };  if true { return SkyNothing() };  return nil }() }() }), decls)
}

func Compiler_Lower_CollectLocalFunctionArities(decls any) any {
	return sky_call2(sky_listFoldl(func(d any) any { return func(acc any) any { return func() any { return func() any { __subject := d; if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; params := sky_asMap(__subject)["V1"]; _ = params; return sky_call2(sky_dictInsert(name), sky_listLength(params), acc) };  if true { return acc };  return nil }() }() } }), sky_dictEmpty(), decls)
}

func Compiler_Lower_BuildConstructorMap(registry any) any {
	return sky_call2(sky_dictFoldl(func(typeName any) any { return func(adt any) any { return func(acc any) any { return func() any { entries := sky_dictToList(sky_asMap(adt)["constructors"]); _ = entries; return Compiler_Lower_AddCtorsFromList(typeName, entries, 0, acc) }() } } }), sky_dictEmpty(), Compiler_Lower_Registry)
}

func Compiler_Lower_AddCtorsFromList(typeName any, entries any, idx any, acc any) any {
	return func() any { return func() any { __subject := entries; if <nil> { return acc };  if <nil> { entry := sky_asList(__subject)[0]; _ = entry; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { name := sky_fst(entry); _ = name; scheme := sky_snd(entry); _ = scheme; ctorArity := Compiler_Lower_CountFunArgs(sky_asMap(scheme)["type_"]); _ = ctorArity; return sky_call2(sky_dictInsert(name), map[string]any{"adtName": typeName, "tagIndex": idx, "arity": ctorArity}, Compiler_Lower_AddCtorsFromList(typeName, rest, sky_asInt(idx) + sky_asInt(1), acc)) }() };  return nil }() }()
}

func Compiler_Lower_CountFunArgs(t any) any {
	return func() any { return func() any { __subject := t; if <nil> { argT := sky_asMap(__subject)["V0"]; _ = argT; toT := sky_asMap(__subject)["V1"]; _ = toT; return sky_asInt(1) + sky_asInt(Compiler_Lower_CountFunArgs(toT)) };  if true { return 0 };  return nil }() }()
}

func Compiler_Lower_SanitizeGoIdent(name any) any {
	return func() any { if sky_asBool(Compiler_Lower_IsGoKeyword(name)) { return sky_concat(name, "_") }; return name }()
}

func Compiler_Lower_IsGoKeyword(name any) any {
	return sky_asBool(sky_equal(name, "go")) || sky_asBool(sky_asBool(sky_equal(name, "type")) || sky_asBool(sky_asBool(sky_equal(name, "func")) || sky_asBool(sky_asBool(sky_equal(name, "var")) || sky_asBool(sky_asBool(sky_equal(name, "return")) || sky_asBool(sky_asBool(sky_equal(name, "if")) || sky_asBool(sky_asBool(sky_equal(name, "else")) || sky_asBool(sky_asBool(sky_equal(name, "for")) || sky_asBool(sky_asBool(sky_equal(name, "range")) || sky_asBool(sky_asBool(sky_equal(name, "switch")) || sky_asBool(sky_asBool(sky_equal(name, "case")) || sky_asBool(sky_asBool(sky_equal(name, "default")) || sky_asBool(sky_asBool(sky_equal(name, "break")) || sky_asBool(sky_asBool(sky_equal(name, "continue")) || sky_asBool(sky_asBool(sky_equal(name, "select")) || sky_asBool(sky_asBool(sky_equal(name, "chan")) || sky_asBool(sky_asBool(sky_equal(name, "map")) || sky_asBool(sky_asBool(sky_equal(name, "struct")) || sky_asBool(sky_asBool(sky_equal(name, "interface")) || sky_asBool(sky_asBool(sky_equal(name, "package")) || sky_asBool(sky_asBool(sky_equal(name, "import")) || sky_asBool(sky_asBool(sky_equal(name, "const")) || sky_asBool(sky_asBool(sky_equal(name, "defer")) || sky_asBool(sky_asBool(sky_equal(name, "fallthrough")) || sky_asBool(sky_equal(name, "goto")))))))))))))))))))))))))
}

func Compiler_Lower_IsStdlibCallee(expr any) any {
	return func() any { return func() any { __subject := expr; if <nil> { parts := sky_asMap(__subject)["V0"]; _ = parts; return func() any { return func() any { __subject := sky_listHead(parts); if <nil> { first := sky_asSkyMaybe(__subject).JustValue; _ = first; return sky_asBool(sky_equal(first, "String")) || sky_asBool(sky_asBool(sky_equal(first, "List")) || sky_asBool(sky_asBool(sky_equal(first, "Dict")) || sky_asBool(sky_asBool(sky_equal(first, "Set")) || sky_asBool(sky_asBool(sky_equal(first, "File")) || sky_asBool(sky_asBool(sky_equal(first, "Process")) || sky_asBool(sky_asBool(sky_equal(first, "Ref")) || sky_asBool(sky_asBool(sky_equal(first, "Io")) || sky_asBool(sky_asBool(sky_equal(first, "Args")) || sky_asBool(sky_equal(first, "Log")))))))))) };  if <nil> { return false };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return sky_call(sky_stringStartsWith("sky_"), name) };  if <nil> { innerCallee := sky_asMap(__subject)["V0"]; _ = innerCallee; return Compiler_Lower_IsStdlibCallee(innerCallee) };  if true { return false };  return nil }() }() };  return nil }() }()
}

func Compiler_Lower_MakeTupleKey(items any) any {
	return sky_call(sky_stringJoin("_"), sky_call(sky_listFilterMap(Compiler_Lower_GetPatVarName), items))
}

func Compiler_Lower_GetPatVarName(pat any) any {
	return func() any { return func() any { __subject := pat; if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return SkyJust(name) };  if <nil> { return SkyJust("w") };  if true { return SkyJust("x") };  return nil }() }()
}

func Compiler_Lower_IsParamOrBuiltin(name any) any {
	return sky_asBool(sky_asInt(sky_stringLength(name)) <= sky_asInt(2)) || sky_asBool(sky_asBool(sky_equal(name, "acc")) || sky_asBool(sky_asBool(sky_equal(name, "rest")) || sky_asBool(sky_asBool(sky_equal(name, "state")) || sky_asBool(sky_asBool(sky_equal(name, "env")) || sky_asBool(sky_asBool(sky_equal(name, "ctx")) || sky_asBool(sky_asBool(sky_equal(name, "mod")) || sky_asBool(sky_asBool(sky_equal(name, "decl")) || sky_asBool(sky_asBool(sky_equal(name, "decls")) || sky_asBool(sky_asBool(sky_equal(name, "items")) || sky_asBool(sky_asBool(sky_equal(name, "fields")) || sky_asBool(sky_asBool(sky_equal(name, "args")) || sky_asBool(sky_asBool(sky_equal(name, "params")) || sky_asBool(sky_asBool(sky_equal(name, "body")) || sky_asBool(sky_asBool(sky_equal(name, "name")) || sky_asBool(sky_asBool(sky_equal(name, "result")) || sky_asBool(sky_asBool(sky_equal(name, "pair")) || sky_asBool(sky_asBool(sky_equal(name, "counter")) || sky_asBool(sky_asBool(sky_equal(name, "registry")) || sky_asBool(sky_asBool(sky_equal(name, "sub")) || sky_asBool(sky_asBool(sky_equal(name, "pat")) || sky_asBool(sky_asBool(sky_equal(name, "left")) || sky_asBool(sky_asBool(sky_equal(name, "right")) || sky_asBool(sky_asBool(sky_equal(name, "inner")) || sky_asBool(sky_asBool(sky_equal(name, "source")) || sky_asBool(sky_asBool(sky_equal(name, "code")) || sky_asBool(sky_asBool(sky_equal(name, "token")) || sky_asBool(sky_asBool(sky_equal(name, "tokens")) || sky_asBool(sky_asBool(sky_equal(name, "parts")) || sky_asBool(sky_asBool(sky_equal(name, "prefix")) || sky_asBool(sky_asBool(sky_equal(name, "msg")) || sky_asBool(sky_asBool(sky_equal(name, "key")) || sky_asBool(sky_asBool(sky_equal(name, "val")) || sky_asBool(sky_asBool(sky_equal(name, "value")) || sky_asBool(sky_asBool(sky_equal(name, "expr")) || sky_asBool(sky_asBool(sky_equal(name, "span")) || sky_asBool(sky_asBool(sky_equal(name, "type_")) || sky_asBool(sky_asBool(sky_equal(name, "scheme")) || sky_asBool(sky_asBool(sky_equal(name, "entry")) || sky_asBool(sky_asBool(sky_equal(name, "binding")) || sky_asBool(sky_asBool(sky_equal(name, "branch")) || sky_asBool(sky_asBool(sky_equal(name, "variant")) || sky_asBool(sky_asBool(sky_equal(name, "modName")) || sky_asBool(sky_asBool(sky_equal(name, "filePath")) || sky_asBool(sky_asBool(sky_equal(name, "outDir")) || sky_asBool(sky_asBool(sky_equal(name, "goCode")) || sky_asBool(sky_asBool(sky_equal(name, "goPackage")) || sky_asBool(sky_asBool(sky_equal(name, "goDecls")) || sky_asBool(sky_asBool(sky_equal(name, "goArgs")) || sky_asBool(sky_asBool(sky_equal(name, "goCallee")) || sky_asBool(sky_asBool(sky_equal(name, "goName")) || sky_asBool(sky_asBool(sky_equal(name, "goMod")) || sky_asBool(sky_asBool(sky_equal(name, "funcPart")) || sky_asBool(sky_asBool(sky_equal(name, "modPart")) || sky_asBool(sky_asBool(sky_equal(name, "qualName")) || sky_asBool(sky_asBool(sky_equal(name, "aliasMap")) || sky_asBool(sky_asBool(sky_equal(name, "stdlibEnv")) || sky_asBool(sky_asBool(sky_equal(name, "depDecls")) || sky_asBool(sky_asBool(sky_equal(name, "entryMod")) || sky_asBool(sky_asBool(sky_equal(name, "loadedModules")) || sky_asBool(sky_asBool(sky_equal(name, "entryRegistry")) || sky_asBool(sky_asBool(sky_equal(name, "entryCtx")) || sky_asBool(sky_asBool(sky_equal(name, "baseCtx")) || sky_asBool(sky_asBool(sky_equal(name, "entryGoDecls")) || sky_asBool(sky_asBool(sky_equal(name, "entryCtorDecls")) || sky_asBool(sky_asBool(sky_equal(name, "helperDecls")) || sky_asBool(sky_asBool(sky_equal(name, "allDecls")) || sky_asBool(sky_asBool(sky_equal(name, "rawGoCode")) || sky_asBool(sky_asBool(sky_equal(name, "outPath")) || sky_asBool(sky_asBool(sky_equal(name, "localImports")) || sky_asBool(sky_asBool(sky_equal(name, "depBaseCtx")) || sky_asBool(sky_asBool(sky_equal(name, "depAliasMap")) || sky_asBool(sky_asBool(sky_equal(name, "checkResult")) || sky_asBool(sky_asBool(sky_equal(name, "lexResult")) || sky_asBool(sky_asBool(sky_equal(name, "filtered")) || sky_asBool(sky_asBool(sky_equal(name, "prefixed")) || sky_asBool(sky_asBool(sky_equal(name, "aliases")) || sky_asBool(sky_asBool(sky_equal(name, "goType")) || sky_asBool(sky_asBool(sky_equal(name, "impMap")) || sky_asBool(sky_asBool(sky_equal(name, "imp")) || sky_asBool(sky_asBool(sky_equal(name, "imports")) || sky_asBool(sky_asBool(sky_equal(name, "header")) || sky_asBool(sky_asBool(sky_equal(name, "opToken")) || sky_asBool(sky_asBool(sky_equal(name, "info")) || sky_asBool(sky_asBool(sky_equal(name, "prec")) || sky_asBool(sky_asBool(sky_equal(name, "assoc")) || sky_asBool(sky_asBool(sky_equal(name, "nextMin")) || sky_asBool(sky_asBool(sky_equal(name, "condition")) || sky_asBool(sky_asBool(sky_equal(name, "thenBranch")) || sky_asBool(sky_asBool(sky_equal(name, "elseBranch")) || sky_asBool(sky_asBool(sky_equal(name, "subject")) || sky_asBool(sky_asBool(sky_equal(name, "bindings")) || sky_asBool(sky_asBool(sky_equal(name, "callee")) || sky_asBool(sky_asBool(sky_equal(name, "fn")) || sky_asBool(sky_asBool(sky_equal(name, "remaining")) || sky_asBool(sky_asBool(sky_equal(name, "idx")) || sky_asBool(sky_asBool(sky_equal(name, "ch")) || sky_asBool(sky_asBool(sky_equal(name, "str")) || sky_asBool(sky_asBool(sky_equal(name, "count")) || sky_asBool(sky_asBool(sky_equal(name, "len")) || sky_asBool(sky_asBool(sky_equal(name, "start")) || sky_asBool(sky_asBool(sky_equal(name, "end")) || sky_asBool(sky_asBool(sky_equal(name, "line")) || sky_asBool(sky_asBool(sky_equal(name, "character")) || sky_asBool(sky_asBool(sky_equal(name, "position")) || sky_asBool(sky_asBool(sky_equal(name, "textDoc")) || sky_asBool(sky_asBool(sky_equal(name, "uri")) || sky_asBool(sky_asBool(sky_equal(name, "text")) || sky_asBool(sky_asBool(sky_equal(name, "content")) || sky_asBool(sky_asBool(sky_equal(name, "json")) || sky_asBool(sky_asBool(sky_equal(name, "varName")) || sky_asBool(sky_asBool(sky_equal(name, "pattern")) || sky_asBool(sky_asBool(sky_equal(name, "arity")) || sky_asBool(sky_asBool(sky_equal(name, "closure")) || sky_asBool(sky_asBool(sky_equal(name, "separator")) || sky_asBool(sky_asBool(sky_equal(name, "list")) || sky_asBool(sky_asBool(sky_equal(name, "item")) || sky_asBool(sky_asBool(sky_equal(name, "elem")) || sky_asBool(sky_asBool(sky_equal(name, "init")) || sky_asBool(sky_asBool(sky_equal(name, "doc")) || sky_asBool(sky_asBool(sky_equal(name, "indent")) || sky_asBool(sky_asBool(sky_equal(name, "width")) || sky_asBool(sky_asBool(sky_equal(name, "wrapperResult")) || sky_asBool(sky_asBool(sky_equal(name, "skyiContent")) || sky_asBool(sky_asBool(sky_equal(name, "pkgName")) || sky_asBool(sky_asBool(sky_equal(name, "safePkg")) || sky_asBool(sky_asBool(sky_equal(name, "cacheDir")) || sky_asBool(sky_asBool(sky_equal(name, "cachePath")) || sky_asBool(sky_asBool(sky_equal(name, "inspectorDir")) || sky_asBool(sky_asBool(sky_equal(name, "buildResult")) || sky_asBool(sky_asBool(sky_equal(name, "output")) || sky_asBool(sky_asBool(sky_equal(name, "goModContent")) || sky_asBool(sky_asBool(sky_equal(name, "mainPath")) || sky_asBool(sky_asBool(sky_equal(name, "runtimeCode")) || sky_asBool(sky_asBool(sky_equal(name, "runtimeDir")) || sky_asBool(sky_asBool(sky_equal(name, "inspectJson")) || sky_asBool(sky_asBool(sky_equal(name, "wrapperCode")) || sky_asBool(sky_asBool(sky_equal(name, "wrapperDir")) || sky_asBool(sky_asBool(sky_equal(name, "moduleName")) || sky_asBool(sky_asBool(sky_equal(name, "funcName")) || sky_asBool(sky_asBool(sky_equal(name, "funcInfo")) || sky_asBool(sky_asBool(sky_equal(name, "methodInfo")) || sky_asBool(sky_asBool(sky_equal(name, "receiverCast")) || sky_asBool(sky_asBool(sky_equal(name, "argCasts")) || sky_asBool(sky_asBool(sky_equal(name, "castStr")) || sky_asBool(sky_asBool(sky_equal(name, "argNames")) || sky_asBool(sky_asBool(sky_equal(name, "goCall")) || sky_asBool(sky_asBool(sky_equal(name, "returnCode")) || sky_asBool(sky_asBool(sky_equal(name, "wrapperName")) || sky_asBool(sky_asBool(sky_equal(name, "paramList")) || sky_asBool(sky_asBool(sky_equal(name, "paramStr")) || sky_asBool(sky_asBool(sky_equal(name, "assertion")) || sky_asBool(sky_asBool(sky_equal(name, "entryFile")) || sky_asBool(sky_asBool(sky_equal(name, "command")) || sky_asBool(sky_asBool(sky_equal(name, "formatted")) || sky_asBool(sky_asBool(sky_equal(name, "readErr")) || sky_asBool(sky_asBool(sky_equal(name, "parseErr")) || sky_asBool(sky_asBool(sky_equal(name, "srcRoot")) || sky_asBool(sky_asBool(sky_equal(name, "hasLocalImports")) || sky_asBool(sky_asBool(sky_equal(name, "error")) || sky_asBool(sky_asBool(sky_equal(name, "diagnostics")) || sky_asBool(sky_asBool(sky_equal(name, "severity")) || sky_asBool(sky_asBool(sky_equal(name, "annotation")) || sky_asBool(sky_asBool(sky_equal(name, "bodyResult")) || sky_asBool(sky_asBool(sky_equal(name, "condResult")) || sky_asBool(sky_asBool(sky_equal(name, "leftResult")) || sky_asBool(sky_asBool(sky_equal(name, "rightResult")) || sky_asBool(sky_asBool(sky_equal(name, "patResult")) || sky_asBool(sky_asBool(sky_equal(name, "bodyType")) || sky_asBool(sky_asBool(sky_equal(name, "funType")) || sky_asBool(sky_asBool(sky_equal(name, "selfType")) || sky_asBool(sky_asBool(sky_equal(name, "selfVar")) || sky_asBool(sky_asBool(sky_equal(name, "paramVars")) || sky_asBool(sky_asBool(sky_equal(name, "bindResult")) || sky_asBool(sky_asBool(sky_equal(name, "paramSub")) || sky_asBool(sky_asBool(sky_equal(name, "envWithSelf")) || sky_asBool(sky_asBool(sky_equal(name, "resolvedParamTypes")) || sky_asBool(sky_asBool(sky_equal(name, "bodySub")) || sky_asBool(sky_asBool(sky_equal(name, "finalSub")) || sky_asBool(sky_asBool(sky_equal(name, "finalType")) || sky_asBool(sky_asBool(sky_equal(name, "scheme")) || sky_asBool(sky_asBool(sky_equal(name, "typed")) || sky_asBool(sky_asBool(sky_equal(name, "newEnv")) || sky_asBool(sky_asBool(sky_equal(name, "annotations")) || sky_asBool(sky_asBool(sky_equal(name, "inferDiags")) || sky_asBool(sky_asBool(sky_equal(name, "exhaustDiags")) || sky_asBool(sky_asBool(sky_equal(name, "adtDiags")) || sky_asBool(sky_asBool(sky_equal(name, "aliasEnv")) || sky_asBool(sky_asBool(sky_equal(name, "adtEnv")) || sky_asBool(sky_asBool(sky_equal(name, "env0")) || sky_asBool(sky_asBool(sky_equal(name, "env1")) || sky_asBool(sky_asBool(sky_equal(name, "newRegistry")) || sky_asBool(sky_asBool(sky_equal(name, "newDiags")) || sky_asBool(sky_asBool(sky_equal(name, "ctorSchemes")) || sky_asBool(sky_asBool(sky_equal(name, "adt")) || sky_asBool(sky_asBool(sky_equal(name, "paramMap")) || sky_asBool(sky_asBool(sky_equal(name, "resultType")) || sky_asBool(sky_asBool(sky_equal(name, "fieldTypes")) || sky_asBool(sky_asBool(sky_equal(name, "ctorType")) || sky_asBool(sky_asBool(sky_equal(name, "quantified")) || sky_asBool(sky_asBool(sky_equal(name, "elemVar")) || sky_asBool(sky_asBool(sky_equal(name, "listType")) || sky_asBool(sky_asBool(sky_equal(name, "elemType")) || sky_asBool(sky_asBool(sky_equal(name, "headPat")) || sky_asBool(sky_asBool(sky_equal(name, "tailPat")) || sky_asBool(sky_asBool(sky_equal(name, "headResult")) || sky_asBool(sky_asBool(sky_equal(name, "tailResult")) || sky_asBool(sky_asBool(sky_equal(name, "innerPat")) || sky_asBool(sky_asBool(sky_equal(name, "argPats")) || sky_asBool(sky_asBool(sky_equal(name, "argTypes")) || sky_asBool(sky_asBool(sky_equal(name, "ctorName")) || sky_asBool(sky_asBool(sky_equal(name, "instType")) || sky_asBool(sky_asBool(sky_equal(name, "splitResult")) || sky_asBool(sky_asBool(sky_equal(name, "subjectType")) || sky_asBool(sky_asBool(sky_equal(name, "patterns")) || sky_asBool(sky_asBool(sky_equal(name, "covered")) || sky_asBool(sky_asBool(sky_equal(name, "missing")) || sky_asBool(sky_asBool(sky_equal(name, "allCtors")) || sky_asBool(sky_asBool(sky_equal(name, "coveredCtors")) || sky_asBool(sky_asBool(sky_equal(name, "missingList")) || sky_asBool(sky_asBool(sky_equal(name, "headerLine")) || sky_asBool(sky_asBool(sky_equal(name, "contentLength")) || sky_asBool(sky_asBool(sky_equal(name, "blankLine")) || sky_asBool(sky_asBool(sky_equal(name, "searchKey")) || sky_asBool(sky_asBool(sky_equal(name, "keyIdx")) || sky_asBool(sky_asBool(sky_equal(name, "afterKey")) || sky_asBool(sky_asBool(sky_equal(name, "colonIdx")) || sky_asBool(sky_asBool(sky_equal(name, "afterColon")) || sky_asBool(sky_asBool(sky_equal(name, "numStr")) || sky_asBool(sky_equal(name, "raw"))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))))
}

func Compiler_Lower_LowerArgExpr(ctx any, expr any) any {
	return func() any { return func() any { __subject := expr; if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { if sky_asBool(sky_asBool(sky_not(sky_stringIsEmpty(sky_asMap(ctx)["modulePrefix"]))) && sky_asBool(Compiler_Lower_ListContains(name, sky_asMap(ctx)["localFunctions"]))) { return func() any { goName := sky_concat(sky_asMap(ctx)["modulePrefix"], sky_concat("_", Compiler_Lower_CapitalizeFirst(Compiler_Lower_SanitizeGoIdent(name)))); _ = goName; fnArity := Compiler_Lower_GetFnArity(name, ctx); _ = fnArity; return func() any { if sky_asBool(sky_asInt(fnArity) > sky_asInt(1)) { return Compiler_Lower_MakeCurryWrapper(goName, fnArity) }; return Compiler_Lower_LowerExpr(ctx, expr) }() }() }; return Compiler_Lower_LowerExpr(ctx, expr) }() };  if true { return Compiler_Lower_LowerExpr(ctx, expr) };  return nil }() }()
}

func Compiler_Lower_IsZeroArityFn(name any, ctx any) any {
	return func() any { return func() any { __subject := sky_call(sky_dictGet(name), sky_asMap(ctx)["localFunctionArity"]); if <nil> { arity := sky_asSkyMaybe(__subject).JustValue; _ = arity; return sky_equal(Compiler_Lower_Arity, 0) };  if <nil> { return false };  return nil }() }()
}

func Compiler_Lower_GetFnArity(name any, ctx any) any {
	return func() any { return func() any { __subject := sky_call(sky_dictGet(name), sky_asMap(ctx)["localFunctionArity"]); if <nil> { arity := sky_asSkyMaybe(__subject).JustValue; _ = arity; return Compiler_Lower_Arity };  if <nil> { return 1 };  return nil }() }()
}

func Compiler_Lower_MakeCurryWrapper(goName any, arity any) any {
	return func() any { if sky_asBool(sky_equal(Compiler_Lower_Arity, 2)) { return GoRawExpr(sky_concat("func(__ca0 any) any { return func(__ca1 any) any { return ", sky_concat(goName, "(__ca0, __ca1) } }"))) }; return func() any { if sky_asBool(sky_equal(Compiler_Lower_Arity, 3)) { return GoRawExpr(sky_concat("func(__ca0 any) any { return func(__ca1 any) any { return func(__ca2 any) any { return ", sky_concat(goName, "(__ca0, __ca1, __ca2) } } }"))) }; return func() any { if sky_asBool(sky_equal(Compiler_Lower_Arity, 4)) { return GoRawExpr(sky_concat("func(__ca0 any) any { return func(__ca1 any) any { return func(__ca2 any) any { return func(__ca3 any) any { return ", sky_concat(goName, "(__ca0, __ca1, __ca2, __ca3) } } } }"))) }; return GoIdent(goName) }() }() }()
}

func Compiler_Lower_ListContains(needle any, haystack any) any {
	return sky_call2(sky_listFoldl(func(item any) any { return func(acc any) any { return func() any { if sky_asBool(acc) { return true }; return sky_equal(item, needle) }() } }), false, haystack)
}

func Compiler_Lower_IsLocalFn(name any, fns any) any {
	return func() any { return func() any { __subject := sky_call(sky_dictGet(name), fns); if <nil> { return true };  if <nil> { return false };  return nil }() }()
}

func Compiler_Lower_IsLocalFunction(name any, ctx any) any {
	return func() any { return func() any { __subject := sky_call(sky_dictGet(name), sky_asMap(ctx)["localFunctionArity"]); if <nil> { return true };  if <nil> { return false };  return nil }() }()
}

func Compiler_Lower_GoQuote(s any) any {
	return func() any { escaped := sky_call2(sky_stringReplace("\""), "\\\"", sky_call2(sky_stringReplace("\\"), "\\\\", s)); _ = escaped; return sky_concat("\"", sky_concat(escaped, "\"")) }()
}

func Compiler_Lower_CapitalizeFirst(s any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(s)) { return "" }; return sky_concat(sky_stringToUpper(sky_call2(sky_stringSlice(0), 1, s)), sky_call2(sky_stringSlice(1), sky_stringLength(s), s)) }()
}

func Compiler_Lower_LastPartOf(parts any) any {
	return func() any { return func() any { __subject := sky_listReverse(parts); if <nil> { last := sky_asList(__subject)[0]; _ = last; return last };  if <nil> { return "" };  return nil }() }()
}

func Compiler_Lower_ListGet(idx any, items any) any {
	return func() any { return func() any { __subject := sky_listHead(sky_call(sky_listDrop(idx), items)); if <nil> { x := sky_asSkyMaybe(__subject).JustValue; _ = x; return x };  if <nil> { return Compiler_Lower_ListGet(0, items) };  return nil }() }()
}

func Compiler_Lower_ZipIndex(items any) any {
	return Compiler_Lower_ZipIndexLoop(0, items)
}

func Compiler_Lower_ZipIndexLoop(idx any, items any) any {
	return func() any { return func() any { __subject := items; if <nil> { return []any{} };  if <nil> { x := sky_asList(__subject)[0]; _ = x; rest := sky_asList(__subject)[1:]; _ = rest; return append([]any{SkyTuple2{V0: idx, V1: x}}, sky_asList(Compiler_Lower_ZipIndexLoop(sky_asInt(idx) + sky_asInt(1), rest))...) };  return nil }() }()
}

func Compiler_Lower_EmitGoExprInline(expr any) any {
	return func() any { return func() any { __subject := expr; if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return name };  if <nil> { val := sky_asMap(__subject)["V0"]; _ = val; return val };  if <nil> { s := sky_asMap(__subject)["V0"]; _ = s; return sky_concat("\"", sky_concat(s, "\"")) };  if <nil> { fn := sky_asMap(__subject)["V0"]; _ = fn; args := sky_asMap(__subject)["V1"]; _ = args; return func() any { fnStr := Compiler_Lower_EmitGoExprInline(fn); _ = fnStr; calleeStr := func() any { if sky_asBool(sky_call(sky_stringEndsWith(")"), fnStr)) { return sky_concat(fnStr, ".(func(any) any)") }; return fnStr }(); _ = calleeStr; return sky_concat(calleeStr, sky_concat("(", sky_concat(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Lower_EmitGoExprInline), args)), ")"))) }() };  if <nil> { target := sky_asMap(__subject)["V0"]; _ = target; sel := sky_asMap(__subject)["V1"]; _ = sel; return sky_concat(Compiler_Lower_EmitGoExprInline(target), sky_concat(".", sel)) };  if <nil> { items := sky_asMap(__subject)["V0"]; _ = items; return sky_concat("[]any{", sky_concat(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Lower_EmitGoExprInline), items)), "}")) };  if <nil> { entries := sky_asMap(__subject)["V0"]; _ = entries; return sky_concat("map[string]any{", sky_concat(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Lower_EmitMapEntry), entries)), "}")) };  if <nil> { params := sky_asMap(__subject)["V0"]; _ = params; body := sky_asMap(__subject)["V1"]; _ = body; return sky_concat("func(", sky_concat(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Lower_EmitInlineParam), params)), sky_concat(") any { return ", sky_concat(Compiler_Lower_EmitGoExprInline(body), " }")))) };  if <nil> { code := sky_asMap(__subject)["V0"]; _ = code; return code };  if <nil> { typeName := sky_asMap(__subject)["V0"]; _ = typeName; fields := sky_asMap(__subject)["V1"]; _ = fields; return sky_concat(typeName, sky_concat("{", sky_concat(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(func(pair any) any { return sky_concat(sky_fst(pair), sky_concat(": ", Compiler_Lower_EmitGoExprInline(sky_snd(pair)))) }), fields)), "}"))) };  if <nil> { op := sky_asMap(__subject)["V0"]; _ = op; left := sky_asMap(__subject)["V1"]; _ = left; right := sky_asMap(__subject)["V2"]; _ = right; return sky_concat(Compiler_Lower_EmitGoExprInline(left), sky_concat(" ", sky_concat(op, sky_concat(" ", Compiler_Lower_EmitGoExprInline(right))))) };  if <nil> { op := sky_asMap(__subject)["V0"]; _ = op; operand := sky_asMap(__subject)["V1"]; _ = operand; return sky_concat(op, Compiler_Lower_EmitGoExprInline(operand)) };  if <nil> { target := sky_asMap(__subject)["V0"]; _ = target; index := sky_asMap(__subject)["V1"]; _ = index; return sky_concat(Compiler_Lower_EmitGoExprInline(target), sky_concat("[", sky_concat(Compiler_Lower_EmitGoExprInline(index), "]"))) };  if <nil> { return "nil" };  return nil }() }()
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
	return func() any { return func() any { __subject := stmt; if <nil> { expr := sky_asMap(__subject)["V0"]; _ = expr; return Compiler_Lower_EmitGoExprInline(expr) };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; expr := sky_asMap(__subject)["V1"]; _ = expr; return sky_concat(name, sky_concat(" = ", Compiler_Lower_EmitGoExprInline(expr))) };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; expr := sky_asMap(__subject)["V1"]; _ = expr; return sky_concat(name, sky_concat(" := ", Compiler_Lower_EmitGoExprInline(expr))) };  if <nil> { expr := sky_asMap(__subject)["V0"]; _ = expr; return sky_concat("return ", Compiler_Lower_EmitGoExprInline(expr)) };  if <nil> { return "return" };  if <nil> { cond := sky_asMap(__subject)["V0"]; _ = cond; thenBody := sky_asMap(__subject)["V1"]; _ = thenBody; elseBody := sky_asMap(__subject)["V2"]; _ = elseBody; return sky_concat("if ", sky_concat(Compiler_Lower_EmitGoExprInline(cond), sky_concat(" { ", sky_concat(Compiler_Lower_StmtsToGoString(thenBody), sky_concat(" } else { ", sky_concat(Compiler_Lower_StmtsToGoString(elseBody), " }")))))) };  if <nil> { body := sky_asMap(__subject)["V0"]; _ = body; return Compiler_Lower_StmtsToGoString(body) };  return nil }() }()
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
	return func() any { return func() any { __subject := patterns; if <nil> { return false };  if <nil> { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { if sky_asBool(Compiler_Exhaustive_IsCatchAll(pat)) { return true }; return Compiler_Exhaustive_HasCatchAll(rest) }() };  return nil }() }()
}

func Compiler_Exhaustive_IsCatchAll(pat any) any {
	return func() any { return func() any { __subject := pat; if <nil> { return true };  if <nil> { return true };  if <nil> { inner := sky_asMap(__subject)["V0"]; _ = inner; return Compiler_Exhaustive_IsCatchAll(inner) };  if true { return false };  return nil }() }()
}

func Compiler_Exhaustive_CheckTypeExhaustiveness(registry any, subjectType any, patterns any) any {
	return func() any { return func() any { __subject := subjectType; if <nil> { typeName := sky_asMap(__subject)["V0"]; _ = typeName; return func() any { if sky_asBool(sky_equal(typeName, "Bool")) { return Compiler_Exhaustive_CheckBoolExhaustiveness(patterns) }; return func() any { return func() any { __subject := sky_call(sky_dictGet(typeName), registry); if <nil> { adt := sky_asSkyMaybe(__subject).JustValue; _ = adt; return Compiler_Exhaustive_CheckAdtExhaustiveness(adt, patterns) };  if <nil> { return SkyNothing() };  if <nil> { typeName := sky_asMap(sky_asMap(__subject)["V0"])["V0"]; _ = typeName; return func() any { return func() any { __subject := sky_call(sky_dictGet(typeName), registry); if <nil> { adt := sky_asSkyMaybe(__subject).JustValue; _ = adt; return Compiler_Exhaustive_CheckAdtExhaustiveness(adt, patterns) };  if <nil> { return SkyNothing() };  if true { return SkyNothing() };  return nil }() }() };  return nil }() }() }() };  return nil }() }()
}

func Compiler_Exhaustive_CheckBoolExhaustiveness(patterns any) any {
	return func() any { covered := Compiler_Exhaustive_CollectBoolPatterns(patterns, sky_setEmpty()); _ = covered; hasTrue := sky_call(sky_setMember("True"), covered); _ = hasTrue; hasFalse := sky_call(sky_setMember("False"), covered); _ = hasFalse; return func() any { if sky_asBool(sky_asBool(hasTrue) && sky_asBool(hasFalse)) { return SkyNothing() }; return func() any { if sky_asBool(hasTrue) { return SkyJust("Missing pattern: False") }; return func() any { if sky_asBool(hasFalse) { return SkyJust("Missing pattern: True") }; return SkyJust("Missing patterns: True, False") }() }() }() }()
}

func Compiler_Exhaustive_CollectBoolPatterns(patterns any, acc any) any {
	return func() any { return func() any { __subject := patterns; if <nil> { return acc };  if <nil> { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := pat; if <nil> { parts := sky_asMap(__subject)["V0"]; _ = parts; return func() any { name := Compiler_Exhaustive_LastPart(parts); _ = name; return Compiler_Exhaustive_CollectBoolPatterns(rest, sky_call(sky_setInsert(name), acc)) }() };  if <nil> { b := sky_asMap(sky_asMap(__subject)["V0"])["V0"]; _ = b; return func() any { if sky_asBool(b) { return Compiler_Exhaustive_CollectBoolPatterns(rest, sky_call(sky_setInsert("True"), acc)) }; return Compiler_Exhaustive_CollectBoolPatterns(rest, sky_call(sky_setInsert("False"), acc)) }() };  if true { return Compiler_Exhaustive_CollectBoolPatterns(rest, acc) };  return nil }() }() };  return nil }() }()
}

func Compiler_Exhaustive_CheckAdtExhaustiveness(adt any, patterns any) any {
	return func() any { allCtors := sky_setFromList(sky_dictKeys(sky_asMap(adt)["constructors"])); _ = allCtors; coveredCtors := Compiler_Exhaustive_CollectConstructorPatterns(patterns, sky_setEmpty()); _ = coveredCtors; missing := sky_call(sky_setDiff(allCtors), coveredCtors); _ = missing; return func() any { if sky_asBool(sky_setIsEmpty(missing)) { return SkyNothing() }; return func() any { missingList := sky_setToList(missing); _ = missingList; return SkyJust(sky_concat("Missing patterns: ", sky_call(sky_stringJoin(", "), missingList))) }() }() }()
}

func Compiler_Exhaustive_CollectConstructorPatterns(patterns any, acc any) any {
	return func() any { return func() any { __subject := patterns; if <nil> { return acc };  if <nil> { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := pat; if <nil> { parts := sky_asMap(__subject)["V0"]; _ = parts; return func() any { name := Compiler_Exhaustive_LastPart(parts); _ = name; return Compiler_Exhaustive_CollectConstructorPatterns(rest, sky_call(sky_setInsert(name), acc)) }() };  if <nil> { inner := sky_asMap(__subject)["V0"]; _ = inner; return Compiler_Exhaustive_CollectConstructorPatterns(append([]any{inner}, sky_asList(rest)...), acc) };  if true { return Compiler_Exhaustive_CollectConstructorPatterns(rest, acc) };  return nil }() }() };  return nil }() }()
}

func Compiler_Exhaustive_LastPart(parts any) any {
	return func() any { return func() any { __subject := sky_listReverse(parts); if <nil> { last := sky_asList(__subject)[0]; _ = last; return last };  if <nil> { return "" };  return nil }() }()
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
	return func() any { return func() any { __subject := pat; if <nil> { return SkyOk(Compiler_PatternCheck_EmptyResult()) };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return SkyOk(map[string]any{"substitution": emptySub, "bindings": []any{SkyTuple2{V0: name, V1: expectedType}}}) };  if <nil> { lit := sky_asMap(__subject)["V0"]; _ = lit; return func() any { litType := Compiler_PatternCheck_LiteralType(lit); _ = litType; return func() any { return func() any { __subject := unify(expectedType, litType); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Pattern literal type mismatch: ", e)) };  if <nil> { sub := sky_asSkyResult(__subject).OkValue; _ = sub; return SkyOk(map[string]any{"substitution": sub, "bindings": []any{}}) };  if <nil> { items := sky_asMap(__subject)["V0"]; _ = items; return func() any { freshVars := sky_call(sky_listMap(func(item any) any { return freshVar(counter, SkyNothing()) }), items); _ = freshVars; tupleType := TTuple(freshVars); _ = tupleType; return func() any { return func() any { __subject := unify(expectedType, tupleType); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Tuple pattern mismatch: ", e)) };  if <nil> { sub := sky_asSkyResult(__subject).OkValue; _ = sub; return Compiler_PatternCheck_CheckPatternList(counter, registry, env, items, sky_call(sky_listMap(func(x any) any { return applySub(sub, x) }), freshVars), sub, []any{}) };  if <nil> { items := sky_asMap(__subject)["V0"]; _ = items; return func() any { elemVar := freshVar(counter, SkyJust("elem")); _ = elemVar; listType := TApp(TConst("List"), []any{elemVar}); _ = listType; return func() any { return func() any { __subject := unify(expectedType, listType); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("List pattern mismatch: ", e)) };  if <nil> { sub := sky_asSkyResult(__subject).OkValue; _ = sub; return func() any { elemType := applySub(sub, elemVar); _ = elemType; return Compiler_PatternCheck_CheckPatternListSame(counter, registry, env, items, elemType, sub, []any{}) }() };  if <nil> { headPat := sky_asMap(__subject)["V0"]; _ = headPat; tailPat := sky_asMap(__subject)["V1"]; _ = tailPat; return func() any { elemVar := freshVar(counter, SkyJust("elem")); _ = elemVar; listType := TApp(TConst("List"), []any{elemVar}); _ = listType; return func() any { return func() any { __subject := unify(expectedType, listType); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Cons pattern mismatch: ", e)) };  if <nil> { sub1 := sky_asSkyResult(__subject).OkValue; _ = sub1; return func() any { elemType := applySub(sub1, elemVar); _ = elemType; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, headPat, elemType); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { headResult := sky_asSkyResult(__subject).OkValue; _ = headResult; return func() any { sub2 := composeSubs(sky_asMap(headResult)["substitution"], sub1); _ = sub2; tailExpected := applySub(sub2, listType); _ = tailExpected; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, tailPat, tailExpected); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { tailResult := sky_asSkyResult(__subject).OkValue; _ = tailResult; return func() any { finalSub := composeSubs(sky_asMap(tailResult)["substitution"], sub2); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "bindings": sky_call(sky_listAppend(sky_asMap(headResult)["bindings"]), sky_asMap(tailResult)["bindings"])}) }() };  if <nil> { innerPat := sky_asMap(__subject)["V0"]; _ = innerPat; name := sky_asMap(__subject)["V1"]; _ = name; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, innerPat, expectedType); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { result := sky_asSkyResult(__subject).OkValue; _ = result; return SkyOk(sky_recordUpdate(result, map[string]any{"bindings": append([]any{SkyTuple2{V0: name, V1: applySub(sky_asMap(result)["substitution"], expectedType)}}, sky_asList(sky_asMap(result)["bindings"])...)})) };  if <nil> { parts := sky_asMap(__subject)["V0"]; _ = parts; argPats := sky_asMap(__subject)["V1"]; _ = argPats; return func() any { ctorName := func() any { return func() any { __subject := sky_listReverse(parts); if <nil> { last := sky_asList(__subject)[0]; _ = last; return last };  if <nil> { return "" };  return nil }() }(); _ = ctorName; return func() any { return func() any { __subject := Compiler_Adt_LookupConstructor(ctorName, registry); if <nil> { return func() any { return func() any { __subject := Compiler_Env_Lookup(ctorName, env); if <nil> { return SkyErr(sky_concat("Unknown constructor in pattern: ", ctorName)) };  if <nil> { scheme := sky_asSkyMaybe(__subject).JustValue; _ = scheme; return Compiler_PatternCheck_CheckConstructorPattern(counter, registry, env, ctorName, scheme, argPats, expectedType) };  if <nil> { scheme := sky_asSkyMaybe(__subject).JustValue; _ = scheme; return Compiler_PatternCheck_CheckConstructorPattern(counter, registry, env, ctorName, scheme, argPats, expectedType) };  if <nil> { fields := sky_asMap(__subject)["V0"]; _ = fields; return func() any { fieldBindings := sky_call(sky_listMap(func(f any) any { return SkyTuple2{V0: f, V1: expectedType} }), fields); _ = fieldBindings; return SkyOk(map[string]any{"substitution": emptySub, "bindings": fieldBindings}) }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }()
}

func Compiler_PatternCheck_CheckConstructorPattern(counter any, registry any, env any, ctorName any, scheme any, argPats any, expectedType any) any {
	return func() any { instType := instantiate(counter, scheme); _ = instType; splitResult := Compiler_PatternCheck_SplitFunType(instType); _ = splitResult; argTypes := sky_fst(splitResult); _ = argTypes; resultType := sky_snd(splitResult); _ = resultType; return func() any { if sky_asBool(!sky_equal(sky_listLength(argPats), sky_listLength(argTypes))) { return SkyErr(sky_concat("Constructor ", sky_concat(ctorName, sky_concat(" expects ", sky_concat(sky_stringFromInt(sky_listLength(argTypes)), sky_concat(" arguments, got ", sky_stringFromInt(sky_listLength(argPats)))))))) }; return func() any { return func() any { __subject := unify(expectedType, resultType); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Constructor pattern type mismatch: ", e)) };  if <nil> { sub := sky_asSkyResult(__subject).OkValue; _ = sub; return Compiler_PatternCheck_CheckPatternList(counter, registry, env, argPats, sky_call(sky_listMap(func(x any) any { return applySub(sub, x) }), argTypes), sub, []any{}) };  return nil }() }() }() }()
}

func Compiler_PatternCheck_SplitFunType(t any) any {
	return func() any { return func() any { __subject := t; if <nil> { fromT := sky_asMap(__subject)["V0"]; _ = fromT; toT := sky_asMap(__subject)["V1"]; _ = toT; return func() any { inner := Compiler_PatternCheck_SplitFunType(toT); _ = inner; rest := sky_fst(inner); _ = rest; result := sky_snd(inner); _ = result; return SkyTuple2{V0: append([]any{fromT}, sky_asList(rest)...), V1: result} }() };  if true { return SkyTuple2{V0: []any{}, V1: t} };  return nil }() }()
}

func Compiler_PatternCheck_CheckPatternList(counter any, registry any, env any, pats any, types any, sub any, bindings any) any {
	return func() any { return func() any { __subject := pats; if <nil> { return func() any { return func() any { __subject := types; if <nil> { return SkyOk(map[string]any{"substitution": sub, "bindings": Compiler_PatternCheck_Bindings}) };  if true { return SkyErr("Pattern/type count mismatch") };  if <nil> { p := sky_asList(__subject)[0]; _ = p; ps := sky_asList(__subject)[1:]; _ = ps; return func() any { return func() any { __subject := types; if <nil> { return SkyErr("Pattern/type count mismatch") };  if <nil> { t := sky_asList(__subject)[0]; _ = t; ts := sky_asList(__subject)[1:]; _ = ts; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, p, applySub(sub, t)); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { result := sky_asSkyResult(__subject).OkValue; _ = result; return Compiler_PatternCheck_CheckPatternList(counter, registry, env, ps, ts, composeSubs(sky_asMap(result)["substitution"], sub), sky_call(sky_listAppend(Compiler_PatternCheck_Bindings), sky_asMap(result)["bindings"])) };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_PatternCheck_CheckPatternListSame(counter any, registry any, env any, pats any, elemType any, sub any, bindings any) any {
	return func() any { return func() any { __subject := pats; if <nil> { return SkyOk(map[string]any{"substitution": sub, "bindings": Compiler_PatternCheck_Bindings}) };  if <nil> { p := sky_asList(__subject)[0]; _ = p; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, p, applySub(sub, elemType)); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { result := sky_asSkyResult(__subject).OkValue; _ = result; return Compiler_PatternCheck_CheckPatternListSame(counter, registry, env, rest, elemType, composeSubs(sky_asMap(result)["substitution"], sub), sky_call(sky_listAppend(Compiler_PatternCheck_Bindings), sky_asMap(result)["bindings"])) };  return nil }() }() };  return nil }() }()
}

func Compiler_PatternCheck_LiteralType(lit any) any {
	return func() any { return func() any { __subject := lit; if <nil> { return TConst("Int") };  if <nil> { return TConst("Float") };  if <nil> { return TConst("String") };  if <nil> { return TConst("Bool") };  if <nil> { return TConst("Char") };  return nil }() }()
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
	return sky_dictEmpty()
}

func Compiler_Adt_LookupConstructor(ctorName any, registry any) any {
	return Compiler_Adt_LookupCtorInEntries(ctorName, sky_dictValues(registry))
}

func Compiler_Adt_LookupCtorInEntries(ctorName any, adts any) any {
	return func() any { return func() any { __subject := adts; if <nil> { return SkyNothing() };  if <nil> { adt := sky_asList(__subject)[0]; _ = adt; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := sky_call(sky_dictGet(ctorName), sky_asMap(adt)["constructors"]); if <nil> { scheme := sky_asSkyMaybe(__subject).JustValue; _ = scheme; return SkyJust(scheme) };  if <nil> { return Compiler_Adt_LookupCtorInEntries(ctorName, rest) };  return nil }() }() };  return nil }() }()
}

func Compiler_Adt_LookupConstructorAdt(ctorName any, registry any) any {
	return Compiler_Adt_LookupCtorAdtInEntries(ctorName, sky_dictValues(registry))
}

func Compiler_Adt_LookupCtorAdtInEntries(ctorName any, adts any) any {
	return func() any { return func() any { __subject := adts; if <nil> { return SkyNothing() };  if <nil> { adt := sky_asList(__subject)[0]; _ = adt; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := sky_call(sky_dictGet(ctorName), sky_asMap(adt)["constructors"]); if <nil> { return SkyJust(adt) };  if <nil> { return Compiler_Adt_LookupCtorAdtInEntries(ctorName, rest) };  return nil }() }() };  return nil }() }()
}

func Compiler_Adt_RegisterAdts(counter any, decls any) any {
	return Compiler_Adt_RegisterAdtsLoop(counter, decls, Compiler_Adt_EmptyRegistry(), Compiler_Env_Empty(), []any{})
}

func Compiler_Adt_RegisterAdtsLoop(counter any, decls any, registry any, env any, diagnostics any) any {
	return func() any { return func() any { __subject := decls; if <nil> { return SkyTuple3{V0: registry, V1: env, V2: diagnostics} };  if <nil> { decl := sky_asList(__subject)[0]; _ = decl; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := decl; if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; params := sky_asMap(__subject)["V1"]; _ = params; variants := sky_asMap(__subject)["V2"]; _ = variants; return func() any { __tup_newRegistry_newEnv_newDiags := Compiler_Adt_RegisterOneAdt(counter, Compiler_Adt_Name, params, variants, registry, env); newRegistry := sky_asTuple3(__tup_newRegistry_newEnv_newDiags).V0; _ = newRegistry; newEnv := sky_asTuple3(__tup_newRegistry_newEnv_newDiags).V1; _ = newEnv; newDiags := sky_asTuple3(__tup_newRegistry_newEnv_newDiags).V2; _ = newDiags; return Compiler_Adt_RegisterAdtsLoop(counter, rest, newRegistry, newEnv, sky_call(sky_listAppend(diagnostics), newDiags)) }() };  if true { return Compiler_Adt_RegisterAdtsLoop(counter, rest, registry, env, diagnostics) };  return nil }() }() };  return nil }() }()
}

func Compiler_Adt_RegisterOneAdt(counter any, typeName any, typeParams any, variants any, registry any, env any) any {
	return func() any { arity := sky_listLength(typeParams); _ = arity; ctorSchemes := sky_call2(sky_listFoldl(func(variant any) any { return func(acc any) any { return func() any { scheme := Compiler_Adt_BuildConstructorScheme(counter, typeName, typeParams, variant); _ = scheme; return sky_call2(sky_dictInsert(sky_asMap(variant)["name"]), scheme, acc) }() } }), sky_dictEmpty(), variants); _ = ctorSchemes; adt := map[string]any{"name": typeName, "arity": Compiler_Adt_Arity, "constructors": ctorSchemes}; _ = adt; newRegistry := sky_call2(sky_dictInsert(typeName), adt, registry); _ = newRegistry; newEnv := sky_call2(sky_dictFoldl(func(ctorName any) any { return func(scheme any) any { return func(acc any) any { return Compiler_Env_Extend(ctorName, scheme, acc) } } }), env, ctorSchemes); _ = newEnv; return SkyTuple3{V0: newRegistry, V1: newEnv, V2: []any{}} }()
}

func Compiler_Adt_BuildConstructorScheme(counter any, typeName any, typeParams any, variant any) any {
	return func() any { paramVars := sky_call(sky_listMap(func(p any) any { return freshVar(counter, SkyJust(p)) }), typeParams); _ = paramVars; paramMap := Compiler_Adt_BuildParamMap(typeParams, paramVars, sky_dictEmpty()); _ = paramMap; resultType := func() any { if sky_asBool(sky_listIsEmpty(paramVars)) { return TConst(typeName) }; return TApp(TConst(typeName), paramVars) }(); _ = resultType; fieldTypes := sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Adt_ResolveTypeExpr(paramMap, __pa0) }), sky_asMap(variant)["fields"]); _ = fieldTypes; ctorType := Compiler_Adt_BuildFunType(fieldTypes, resultType); _ = ctorType; quantified := sky_call(sky_listFilterMap(Compiler_Adt_GetVarId), paramVars); _ = quantified; return map[string]any{"quantified": quantified, "type_": ctorType} }()
}

func Compiler_Adt_BuildParamMap(names any, vars any, acc any) any {
	return func() any { return func() any { __subject := names; if <nil> { return acc };  if <nil> { nameH := sky_asList(__subject)[0]; _ = nameH; nameRest := sky_asList(__subject)[1:]; _ = nameRest; return func() any { return func() any { __subject := vars; if <nil> { return acc };  if <nil> { varH := sky_asList(__subject)[0]; _ = varH; varRest := sky_asList(__subject)[1:]; _ = varRest; return Compiler_Adt_BuildParamMap(nameRest, varRest, sky_call2(sky_dictInsert(nameH), varH, acc)) };  return nil }() }() };  return nil }() }()
}

func Compiler_Adt_BuildFunType(args any, result any) any {
	return func() any { return func() any { __subject := args; if <nil> { return result };  if <nil> { arg := sky_asList(__subject)[0]; _ = arg; rest := sky_asList(__subject)[1:]; _ = rest; return TFun(arg, Compiler_Adt_BuildFunType(rest, result)) };  return nil }() }()
}

func Compiler_Adt_GetVarId(t any) any {
	return func() any { return func() any { __subject := t; if <nil> { id := sky_asMap(__subject)["V0"]; _ = id; return SkyJust(id) };  if true { return SkyNothing() };  return nil }() }()
}

func Compiler_Adt_ResolveTypeExpr(paramMap any, texpr any) any {
	return func() any { return func() any { __subject := texpr; if <nil> { parts := sky_asMap(__subject)["V0"]; _ = parts; args := sky_asMap(__subject)["V1"]; _ = args; return func() any { name := sky_call(sky_stringJoin("."), parts); _ = name; resolvedArgs := sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Adt_ResolveTypeExpr(paramMap, __pa0) }), args); _ = resolvedArgs; return func() any { return func() any { __subject := sky_call(sky_dictGet(Compiler_Adt_Name), paramMap); if <nil> { tv := sky_asSkyMaybe(__subject).JustValue; _ = tv; return tv };  if <nil> { return func() any { if sky_asBool(sky_listIsEmpty(resolvedArgs)) { return TConst(Compiler_Adt_Name) }; return TApp(TConst(Compiler_Adt_Name), resolvedArgs) }() };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { return func() any { __subject := sky_call(sky_dictGet(Compiler_Adt_Name), paramMap); if <nil> { tv := sky_asSkyMaybe(__subject).JustValue; _ = tv; return tv };  if <nil> { return TVar(0, SkyJust(Compiler_Adt_Name)) };  if <nil> { fromT := sky_asMap(__subject)["V0"]; _ = fromT; toT := sky_asMap(__subject)["V1"]; _ = toT; return TFun(Compiler_Adt_ResolveTypeExpr(paramMap, fromT), Compiler_Adt_ResolveTypeExpr(paramMap, toT)) };  if <nil> { fields := sky_asMap(__subject)["V0"]; _ = fields; return func() any { fieldDict := sky_call2(sky_listFoldl(func(f any) any { return func(acc any) any { return sky_call2(sky_dictInsert(sky_asMap(f)["name"]), Compiler_Adt_ResolveTypeExpr(paramMap, sky_asMap(f)["type_"]), acc) } }), sky_dictEmpty(), fields); _ = fieldDict; return TRecord(fieldDict) }() };  if <nil> { items := sky_asMap(__subject)["V0"]; _ = items; return TTuple(sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Adt_ResolveTypeExpr(paramMap, __pa0) }), items)) };  if <nil> { return TConst("Unit") };  return nil }() }() };  return nil }() }() }() };  return nil }() }()
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
	return sky_dictEmpty()
}

func Compiler_Types_ApplySub(sub any, t any) any {
	return func() any { return func() any { __subject := t; if <nil> { id := sky_asMap(__subject)["V0"]; _ = id; return func() any { return func() any { __subject := sky_call(sky_dictGet(id), sub); if <nil> { replacement := sky_asSkyMaybe(__subject).JustValue; _ = replacement; return replacement };  if <nil> { return t };  if <nil> { return t };  if <nil> { fromT := sky_asMap(__subject)["V0"]; _ = fromT; toT := sky_asMap(__subject)["V1"]; _ = toT; return TFun(Compiler_Types_ApplySub(sub, fromT), Compiler_Types_ApplySub(sub, toT)) };  if <nil> { ctor := sky_asMap(__subject)["V0"]; _ = ctor; args := sky_asMap(__subject)["V1"]; _ = args; return TApp(Compiler_Types_ApplySub(sub, ctor), sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Types_ApplySub(sub, __pa0) }), args)) };  if <nil> { items := sky_asMap(__subject)["V0"]; _ = items; return TTuple(sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Types_ApplySub(sub, __pa0) }), items)) };  if <nil> { fields := sky_asMap(__subject)["V0"]; _ = fields; return TRecord(sky_call(sky_dictMap(func(kk any) any { return func(v any) any { return Compiler_Types_ApplySub(sub, v) } }), fields)) };  if true { return t };  return nil }() }() };  return nil }() }()
}

func Compiler_Types_ApplySubToScheme(sub any, scheme any) any {
	return func() any { filtered := sky_call2(sky_listFoldl(func(q any) any { return func(s any) any { return sky_call(sky_dictRemove(q), s) } }), sub, sky_asMap(scheme)["quantified"]); _ = filtered; return sky_recordUpdate(scheme, map[string]any{"type_": Compiler_Types_ApplySub(filtered, sky_asMap(scheme)["type_"])}) }()
}

func Compiler_Types_ComposeSubs(s1 any, s2 any) any {
	return func() any { applied := sky_call(sky_dictMap(func(kk any) any { return func(t any) any { return Compiler_Types_ApplySub(s1, t) } }), s2); _ = applied; return sky_call(sky_dictUnion(applied), s1) }()
}

func Compiler_Types_FreeVars(t any) any {
	return func() any { return func() any { __subject := t; if <nil> { id := sky_asMap(__subject)["V0"]; _ = id; return sky_setSingleton(id) };  if <nil> { return sky_setEmpty() };  if <nil> { fromT := sky_asMap(__subject)["V0"]; _ = fromT; toT := sky_asMap(__subject)["V1"]; _ = toT; return sky_call(sky_setUnion(Compiler_Types_FreeVars(fromT)), Compiler_Types_FreeVars(toT)) };  if <nil> { ctor := sky_asMap(__subject)["V0"]; _ = ctor; args := sky_asMap(__subject)["V1"]; _ = args; return sky_call2(sky_listFoldl(func(arg any) any { return func(acc any) any { return sky_call(sky_setUnion(Compiler_Types_FreeVars(arg)), acc) } }), Compiler_Types_FreeVars(ctor), args) };  if <nil> { items := sky_asMap(__subject)["V0"]; _ = items; return sky_call2(sky_listFoldl(func(item any) any { return func(acc any) any { return sky_call(sky_setUnion(Compiler_Types_FreeVars(item)), acc) } }), sky_setEmpty(), items) };  if <nil> { fields := sky_asMap(__subject)["V0"]; _ = fields; return sky_call2(sky_dictFoldl(func(kk any) any { return func(v any) any { return func(acc any) any { return sky_call(sky_setUnion(Compiler_Types_FreeVars(v)), acc) } } }), sky_setEmpty(), fields) };  if true { return sky_setEmpty() };  return nil }() }()
}

func Compiler_Types_FreeVarsInScheme(scheme any) any {
	return func() any { typeVars := Compiler_Types_FreeVars(sky_asMap(scheme)["type_"]); _ = typeVars; quantifiedSet := sky_setFromList(sky_asMap(scheme)["quantified"]); _ = quantifiedSet; return sky_call(sky_setDiff(typeVars), quantifiedSet) }()
}

func Compiler_Types_Instantiate(counter any, scheme any) any {
	return func() any { sub := sky_call2(sky_listFoldl(func(qv any) any { return func(s any) any { return func() any { fresh := Compiler_Types_FreshVar(counter, SkyNothing()); _ = fresh; return sky_call2(sky_dictInsert(qv), fresh, s) }() } }), Compiler_Types_EmptySub(), sky_asMap(scheme)["quantified"]); _ = sub; return Compiler_Types_ApplySub(sub, sky_asMap(scheme)["type_"]) }()
}

func Compiler_Types_Generalize(env any, t any) any {
	return func() any { typeVars := Compiler_Types_FreeVars(t); _ = typeVars; envVars := sky_call2(sky_dictFoldl(func(kk any) any { return func(scheme any) any { return func(acc any) any { return sky_call(sky_setUnion(Compiler_Types_FreeVarsInScheme(scheme)), acc) } } }), sky_setEmpty(), env); _ = envVars; quantified := sky_setToList(sky_call(sky_setDiff(typeVars), envVars)); _ = quantified; return map[string]any{"quantified": Compiler_Types_Quantified, "type_": t} }()
}

func Compiler_Types_Mono(t any) any {
	return map[string]any{"quantified": []any{}, "type_": t}
}

func Compiler_Types_FormatType(t any) any {
	return func() any { return func() any { __subject := t; if <nil> { id := sky_asMap(__subject)["V0"]; _ = id; name := sky_asMap(__subject)["V1"]; _ = name; return func() any { return func() any { __subject := name; if <nil> { n := sky_asSkyMaybe(__subject).JustValue; _ = n; return n };  if <nil> { return sky_concat("t", sky_stringFromInt(id)) };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return name };  if <nil> { fromT := sky_asMap(__subject)["V0"]; _ = fromT; toT := sky_asMap(__subject)["V1"]; _ = toT; return func() any { fromStr := func() any { return func() any { __subject := fromT; if <nil> { return sky_concat("(", sky_concat(Compiler_Types_FormatType(fromT), ")")) };  if true { return Compiler_Types_FormatType(fromT) };  return nil }() }(); _ = fromStr; return sky_concat(fromStr, sky_concat(" -> ", Compiler_Types_FormatType(toT))) }() };  if <nil> { ctor := sky_asMap(__subject)["V0"]; _ = ctor; args := sky_asMap(__subject)["V1"]; _ = args; return sky_concat(Compiler_Types_FormatType(ctor), sky_concat(" ", sky_call(sky_stringJoin(" "), sky_call(sky_listMap(Compiler_Types_FormatType), args)))) };  if <nil> { items := sky_asMap(__subject)["V0"]; _ = items; return sky_concat("( ", sky_concat(sky_call(sky_stringJoin(" , "), sky_call(sky_listMap(Compiler_Types_FormatType), items)), " )")) };  if <nil> { fields := sky_asMap(__subject)["V0"]; _ = fields; return func() any { fieldStrs := sky_call(sky_listMap(func(pair any) any { return sky_concat(sky_fst(pair), sky_concat(" : ", Compiler_Types_FormatType(sky_snd(pair)))) }), sky_dictToList(fields)); _ = fieldStrs; return sky_concat("{ ", sky_concat(sky_call(sky_stringJoin(" , "), fieldStrs), " }")) }() };  if true { return "?" };  return nil }() }() };  return nil }() }()
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

var UnescapeString = Compiler_ParserCore_UnescapeString

var filterLayout = Compiler_ParserCore_FilterLayout

func Compiler_ParserCore_InitState(tokens any) any {
	return map[string]any{"tokens": Compiler_ParserCore_Tokens, "pos": 0, "errors": []any{}}
}

func Compiler_ParserCore_Peek(state any) any {
	return func() any { return func() any { __subject := sky_listHead(sky_call(sky_listDrop(sky_asMap(state)["pos"]), sky_asMap(state)["tokens"])); if <nil> { t := sky_asSkyMaybe(__subject).JustValue; _ = t; return t };  if <nil> { return map[string]any{"kind": TkEOF, "lexeme": "", "span": emptySpan} };  return nil }() }()
}

func Compiler_ParserCore_PeekAt(offset any, state any) any {
	return func() any { return func() any { __subject := sky_listHead(sky_call(sky_listDrop(sky_asInt(sky_asMap(state)["pos"]) + sky_asInt(offset)), sky_asMap(state)["tokens"])); if <nil> { t := sky_asSkyMaybe(__subject).JustValue; _ = t; return t };  if <nil> { return map[string]any{"kind": TkEOF, "lexeme": "", "span": emptySpan} };  return nil }() }()
}

func Compiler_ParserCore_Previous(state any) any {
	return func() any { if sky_asBool(sky_asInt(sky_asMap(state)["pos"]) > sky_asInt(0)) { return func() any { return func() any { __subject := sky_listHead(sky_call(sky_listDrop(sky_asInt(sky_asMap(state)["pos"]) - sky_asInt(1)), sky_asMap(state)["tokens"])); if <nil> { t := sky_asSkyMaybe(__subject).JustValue; _ = t; return t };  if <nil> { return map[string]any{"kind": TkEOF, "lexeme": "", "span": emptySpan} };  return nil }() }() }; return map[string]any{"kind": TkEOF, "lexeme": "", "span": emptySpan} }()
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
	return func() any { return func() any { __subject := k; if <nil> { return "Identifier" };  if <nil> { return "UpperIdentifier" };  if <nil> { return "Integer" };  if <nil> { return "Float" };  if <nil> { return "String" };  if <nil> { return "Char" };  if <nil> { return "Keyword" };  if <nil> { return "Operator" };  if <nil> { return "=" };  if <nil> { return ":" };  if <nil> { return "," };  if <nil> { return "." };  if <nil> { return "|" };  if <nil> { return "->" };  if <nil> { return "\\" };  if <nil> { return "(" };  if <nil> { return ")" };  if <nil> { return "[" };  if <nil> { return "]" };  if <nil> { return "{" };  if <nil> { return "}" };  if <nil> { return "newline" };  if <nil> { return "indent" };  if <nil> { return "dedent" };  if <nil> { return "EOF" };  return nil }() }()
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
	return sky_call(sky_listFilter(func(t any) any { return func() any { return func() any { __subject := sky_asMap(t)["kind"]; if <nil> { return false };  if <nil> { return false };  if <nil> { return false };  if true { return true };  return nil }() }() }), Compiler_ParserCore_Tokens)
}

var dispatchDeclaration = Compiler_Parser_DispatchDeclaration

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

func Compiler_Parser_DispatchDeclaration(first any, second any, state any) any {
	return func() any { if sky_asBool(sky_asBool(sky_equal(first, "foreign")) && sky_asBool(sky_equal(second, "import"))) { return Compiler_Parser_ParseForeignImport(state) }; return func() any { if sky_asBool(sky_asBool(sky_equal(first, "type")) && sky_asBool(sky_equal(second, "alias"))) { return Compiler_Parser_ParseTypeAlias(state) }; return func() any { if sky_asBool(sky_equal(first, "type")) { return Compiler_Parser_ParseTypeDecl(state) }; return func() any { if sky_asBool(sky_asBool(matchKind(TkIdentifier, state)) && sky_asBool(tokenKindEq(peekAt1Kind(state), TkColon))) { return Compiler_Parser_ParseTypeAnnot(state) }; return func() any { if sky_asBool(matchKind(TkIdentifier, state)) { return Compiler_Parser_ParseFunDecl(state) }; return SkyErr(sky_concat("Unexpected token: ", first)) }() }() }() }() }()
}

func Compiler_Parser_ParseVariantFields(state any) any {
	return func() any { if sky_asBool(sky_asBool(matchKind(TkUpperIdentifier, state)) || sky_asBool(sky_asBool(matchKind(TkIdentifier, state)) || sky_asBool(sky_asBool(matchKind(TkLParen, state)) || sky_asBool(matchKind(TkLBrace, state))))) { return func() any { if sky_asBool(sky_asBool(sky_equal(peekColumn(state), 1)) || sky_asBool(matchKind(TkPipe, state))) { return SkyTuple2{V0: []any{}, V1: state} }; return func() any { return func() any { __subject := Compiler_Parser_ParseTypePrimary(state); if <nil> { te := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = te; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { __tup_rest_s2 := Compiler_Parser_ParseVariantFields(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return SkyTuple2{V0: append([]any{te}, sky_asList(rest)...), V1: s2} }() };  if <nil> { return SkyTuple2{V0: []any{}, V1: state} };  return nil }() }() }() }; return SkyTuple2{V0: []any{}, V1: state} }()
}

func Compiler_Parser_ParseTypeArgs(state any) any {
	return func() any { if sky_asBool(sky_asBool(matchKind(TkUpperIdentifier, state)) || sky_asBool(sky_asBool(matchKind(TkIdentifier, state)) || sky_asBool(sky_asBool(matchKind(TkLParen, state)) || sky_asBool(matchKind(TkLBrace, state))))) { return func() any { if sky_asBool(sky_asBool(sky_equal(peekColumn(state), 1)) || sky_asBool(sky_asBool(matchKind(TkEquals, state)) || sky_asBool(matchKind(TkPipe, state)))) { return SkyTuple2{V0: []any{}, V1: state} }; return func() any { return func() any { __subject := Compiler_Parser_ParseTypePrimary(state); if <nil> { te := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = te; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { __tup_rest_s2 := Compiler_Parser_ParseTypeArgs(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return SkyTuple2{V0: append([]any{te}, sky_asList(rest)...), V1: s2} }() };  if <nil> { return SkyTuple2{V0: []any{}, V1: state} };  return nil }() }() }() }; return SkyTuple2{V0: []any{}, V1: state} }()
}

func Compiler_Parser_Parse(tokens any) any {
	return func() any { state := initState(filterLayout(tokens)); _ = state; return func() any { return func() any { __subject := Compiler_Parser_ParseModule(state); if <nil> { mod := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = mod; return SkyOk(mod) };  if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  return nil }() }() }()
}

func Compiler_Parser_ParseModule(state any) any {
	return func() any { return func() any { __subject := consumeLex(TkKeyword, "module", state); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { return func() any { __subject := Compiler_Parser_ParseModuleName(s1); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { name := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = name; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { __tup_exposing__s3 := Compiler_Parser_ParseOptionalExposing(s2); exposing_ := sky_asTuple2(__tup_exposing__s3).V0; _ = exposing_; s3 := sky_asTuple2(__tup_exposing__s3).V1; _ = s3; __tup_imports_s4 := Compiler_Parser_ParseImports(s3); imports := sky_asTuple2(__tup_imports_s4).V0; _ = imports; s4 := sky_asTuple2(__tup_imports_s4).V1; _ = s4; __tup_decls_s5 := Compiler_Parser_ParseDeclarations(s4); decls := sky_asTuple2(__tup_decls_s5).V0; _ = decls; s5 := sky_asTuple2(__tup_decls_s5).V1; _ = s5; return SkyOk(SkyTuple2{V0: map[string]any{"name": name, "exposing_": exposing_, "imports": imports, "declarations": decls, "span": emptySpan}, V1: s5}) }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Parser_ParseModuleName(state any) any {
	return func() any { return func() any { __subject := consume(TkUpperIdentifier, state); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { first := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = first; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return Compiler_Parser_ParseModuleNameParts([]any{sky_asMap(first)["lexeme"]}, s1) };  return nil }() }()
}

func Compiler_Parser_ParseModuleNameParts(parts any, state any) any {
	return func() any { if sky_asBool(matchKind(TkDot, state)) { return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := consume(TkUpperIdentifier, s1); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { part := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = part; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return Compiler_Parser_ParseModuleNameParts(sky_concat(parts, []any{sky_asMap(part)["lexeme"]}), s2) };  return nil }() }() }() }; return SkyOk(SkyTuple2{V0: parts, V1: state}) }()
}

func Compiler_Parser_ParseOptionalExposing(state any) any {
	return func() any { if sky_asBool(matchKindLex(TkKeyword, "exposing", state)) { return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := Compiler_Parser_ParseExposingClause(s1); if <nil> { ec := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = ec; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return SkyTuple2{V0: ec, V1: s2} };  if <nil> { return SkyTuple2{V0: ExposeNone, V1: state} };  return nil }() }() }() }; return SkyTuple2{V0: ExposeNone, V1: state} }()
}

func Compiler_Parser_ParseExposingClause(state any) any {
	return func() any { return func() any { __subject := consume(TkLParen, state); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkDot, s1)) { return func() any { __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; __tup_w_s3 := advance(s2); s3 := sky_asTuple2(__tup_w_s3).V1; _ = s3; return func() any { return func() any { __subject := consume(TkRParen, s3); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s4 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s4; return SkyOk(SkyTuple2{V0: ExposeAll, V1: s4}) };  return nil }() }() }() }; return func() any { __tup_items_s2 := Compiler_Parser_ParseExposedItems([]any{}, s1); items := sky_asTuple2(__tup_items_s2).V0; _ = items; s2 := sky_asTuple2(__tup_items_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkRParen, s2); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk(SkyTuple2{V0: ExposeList(items), V1: s3}) };  return nil }() }() }() }() };  return nil }() }()
}

func Compiler_Parser_ParseExposedItems(items any, state any) any {
	return func() any { if sky_asBool(matchKind(TkRParen, state)) { return SkyTuple2{V0: items, V1: state} }; return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; newItems := sky_concat(items, []any{sky_asMap(tok)["lexeme"]}); _ = newItems; s2 := func() any { if sky_asBool(matchKind(TkComma, s1)) { return func() any { __tup_w_s := advance(s1); s := sky_asTuple2(__tup_w_s).V1; _ = s; return s }() }; return s1 }(); _ = s2; return Compiler_Parser_ParseExposedItems(newItems, s2) }() }()
}

func Compiler_Parser_ParseImports(state any) any {
	return func() any { if sky_asBool(matchKindLex(TkKeyword, "import", state)) { return func() any { return func() any { __subject := Compiler_Parser_ParseImport(state); if <nil> { imp := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = imp; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { __tup_rest_s2 := Compiler_Parser_ParseImports(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return SkyTuple2{V0: append([]any{imp}, sky_asList(rest)...), V1: s2} }() };  if <nil> { return SkyTuple2{V0: []any{}, V1: state} };  return nil }() }() }; return SkyTuple2{V0: []any{}, V1: state} }()
}

func Compiler_Parser_ParseImport(state any) any {
	return func() any { return func() any { __subject := consumeLex(TkKeyword, "import", state); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { return func() any { __subject := Compiler_Parser_ParseModuleName(s1); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { modName := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = modName; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { __tup_alias__s3 := func() any { if sky_asBool(matchKindLex(TkKeyword, "as", s2)) { return func() any { __tup_w_sa := advance(s2); sa := sky_asTuple2(__tup_w_sa).V1; _ = sa; __tup_tok_sb := advance(sa); tok := sky_asTuple2(__tup_tok_sb).V0; _ = tok; sb := sky_asTuple2(__tup_tok_sb).V1; _ = sb; return SkyTuple2{V0: sky_asMap(tok)["lexeme"], V1: sb} }() }; return SkyTuple2{V0: "", V1: s2} }(); alias_ := sky_asTuple2(__tup_alias__s3).V0; _ = alias_; s3 := sky_asTuple2(__tup_alias__s3).V1; _ = s3; __tup_exposing__s4 := Compiler_Parser_ParseOptionalExposing(s3); exposing_ := sky_asTuple2(__tup_exposing__s4).V0; _ = exposing_; s4 := sky_asTuple2(__tup_exposing__s4).V1; _ = s4; return SkyOk(SkyTuple2{V0: map[string]any{"moduleName": modName, "alias_": alias_, "exposing_": exposing_, "span": emptySpan}, V1: s4}) }() };  return nil }() }() };  return nil }() }()
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
	return func() any { return func() any { __subject := result; if <nil> { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return Compiler_Parser_AddDeclAndContinue(sky_fst(pair), sky_snd(pair)) };  if <nil> { return Compiler_Parser_ParseDeclarations(sky_snd(advance(origState))) };  return nil }() }()
}

func Compiler_Parser_AddDeclAndContinue(decl any, s1 any) any {
	return Compiler_Parser_PrependToResult(decl, Compiler_Parser_ParseDeclarations(s1))
}

func Compiler_Parser_PrependToResult(decl any, result any) any {
	return SkyTuple2{V0: append([]any{decl}, sky_asList(sky_fst(result))...), V1: sky_snd(result)}
}

func Compiler_Parser_ParseForeignImport(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkString, s2); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { pkgToken := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = pkgToken; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { pkgName := sky_call2(sky_stringSlice(1), sky_asInt(sky_stringLength(sky_asMap(pkgToken)["lexeme"])) - sky_asInt(1), sky_asMap(pkgToken)["lexeme"]); _ = pkgName; __tup_exposing__s4 := Compiler_Parser_ParseOptionalExposing(s3); exposing_ := sky_asTuple2(__tup_exposing__s4).V0; _ = exposing_; s4 := sky_asTuple2(__tup_exposing__s4).V1; _ = s4; names := func() any { return func() any { __subject := exposing_; if <nil> { items := sky_asMap(__subject)["V0"]; _ = items; return items };  if true { return []any{} };  return nil }() }(); _ = names; firstDecl := func() any { return func() any { __subject := sky_listHead(names); if <nil> { n := sky_asSkyMaybe(__subject).JustValue; _ = n; return SkyOk(SkyTuple2{V0: ForeignImportDecl(n, pkgName, n, emptySpan), V1: s4}) };  if <nil> { return SkyErr("Foreign import must expose at least one name") };  return nil }() }(); _ = firstDecl; return firstDecl }() };  return nil }() }() }()
}

func Compiler_Parser_ParseTypeAlias(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkUpperIdentifier, s2); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { name := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = name; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { __tup_params_s4 := Compiler_Parser_ParseTypeParams(s3); params := sky_asTuple2(__tup_params_s4).V0; _ = params; s4 := sky_asTuple2(__tup_params_s4).V1; _ = s4; return func() any { return func() any { __subject := consume(TkEquals, s4); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s5 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s5; return func() any { return func() any { __subject := Compiler_Parser_ParseTypeExpr(s5); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { typeExpr := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = typeExpr; s6 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s6; return SkyOk(SkyTuple2{V0: TypeAliasDecl(sky_asMap(name)["lexeme"], params, typeExpr, emptySpan), V1: s6}) };  return nil }() }() };  return nil }() }() }() };  return nil }() }() }()
}

func Compiler_Parser_ParseTypeDecl(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := consume(TkUpperIdentifier, s1); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { name := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = name; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { __tup_params_s3 := Compiler_Parser_ParseTypeParams(s2); params := sky_asTuple2(__tup_params_s3).V0; _ = params; s3 := sky_asTuple2(__tup_params_s3).V1; _ = s3; return func() any { return func() any { __subject := consume(TkEquals, s3); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s4 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s4; return func() any { __tup_variants_s5 := Compiler_Parser_ParseTypeVariants(s4); variants := sky_asTuple2(__tup_variants_s5).V0; _ = variants; s5 := sky_asTuple2(__tup_variants_s5).V1; _ = s5; return SkyOk(SkyTuple2{V0: TypeDecl(sky_asMap(name)["lexeme"], params, variants, emptySpan), V1: s5}) }() };  return nil }() }() }() };  return nil }() }() }()
}

func Compiler_Parser_ParseTypeParams(state any) any {
	return func() any { if sky_asBool(matchKind(TkIdentifier, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; __tup_rest_s2 := Compiler_Parser_ParseTypeParams(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return SkyTuple2{V0: append([]any{sky_asMap(tok)["lexeme"]}, sky_asList(rest)...), V1: s2} }() }; return SkyTuple2{V0: []any{}, V1: state} }()
}

func Compiler_Parser_ParseTypeVariants(state any) any {
	return func() any { return func() any { __subject := consume(TkUpperIdentifier, state); if <nil> { return SkyTuple2{V0: []any{}, V1: state} };  if <nil> { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return Compiler_Parser_BuildVariant(sky_fst(pair), sky_snd(pair)) };  return nil }() }()
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
	return func() any { return func() any { __subject := Compiler_Parser_ParseTypeApp(state); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { left := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = left; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkArrow, s1)) { return func() any { __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; return func() any { return func() any { __subject := Compiler_Parser_ParseTypeExpr(s2); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { right := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = right; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk(SkyTuple2{V0: FunType(left, right, emptySpan), V1: s3}) };  return nil }() }() }() }; return SkyOk(SkyTuple2{V0: left, V1: s1}) }() };  return nil }() }()
}

func Compiler_Parser_ParseTypeApp(state any) any {
	return func() any { return func() any { __subject := Compiler_Parser_ParseTypePrimary(state); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return Compiler_Parser_ApplyTypeArgs(sky_fst(pair), sky_snd(pair)) };  return nil }() }()
}

func Compiler_Parser_ApplyTypeArgs(target any, s1 any) any {
	return Compiler_Parser_ResolveTypeApp(target, Compiler_Parser_ParseTypeArgs(s1))
}

func Compiler_Parser_ResolveTypeApp(target any, argsResult any) any {
	return func() any { if sky_asBool(sky_listIsEmpty(sky_fst(argsResult))) { return SkyOk(SkyTuple2{V0: target, V1: sky_snd(argsResult)}) }; return func() any { return func() any { __subject := target; if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; span := sky_asMap(__subject)["V2"]; _ = span; return SkyOk(SkyTuple2{V0: TypeRef(name, sky_fst(argsResult), span), V1: sky_snd(argsResult)}) };  if true { return SkyOk(SkyTuple2{V0: target, V1: sky_snd(argsResult)}) };  return nil }() }() }()
}

func Compiler_Parser_ParseTypePrimary(state any) any {
	return func() any { if sky_asBool(matchKind(TkUpperIdentifier, state)) { return func() any { __tup_id_s1 := advance(state); id := sky_asTuple2(__tup_id_s1).V0; _ = id; s1 := sky_asTuple2(__tup_id_s1).V1; _ = s1; __tup_parts_s2 := parseQualifiedParts([]any{sky_asMap(id)["lexeme"]}, s1); parts := sky_asTuple2(__tup_parts_s2).V0; _ = parts; s2 := sky_asTuple2(__tup_parts_s2).V1; _ = s2; return SkyOk(SkyTuple2{V0: TypeRef(parts, []any{}, emptySpan), V1: s2}) }() }; return func() any { if sky_asBool(matchKind(TkIdentifier, state)) { return func() any { __tup_id_s1 := advance(state); id := sky_asTuple2(__tup_id_s1).V0; _ = id; s1 := sky_asTuple2(__tup_id_s1).V1; _ = s1; return SkyOk(SkyTuple2{V0: TypeVar(sky_asMap(id)["lexeme"], emptySpan), V1: s1}) }() }; return func() any { if sky_asBool(matchKind(TkLParen, state)) { return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkRParen, s1)) { return func() any { __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; return SkyOk(SkyTuple2{V0: UnitTypeExpr(emptySpan), V1: s2}) }() }; return func() any { return func() any { __subject := Compiler_Parser_ParseTypeExpr(s1); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { inner := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = inner; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { if sky_asBool(matchKind(TkComma, s2)) { return Compiler_Parser_ParseTupleTypeRest([]any{inner}, s2) }; return func() any { return func() any { __subject := consume(TkRParen, s2); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk(SkyTuple2{V0: inner, V1: s3}) };  return nil }() }() }() };  return nil }() }() }() }() }; return func() any { if sky_asBool(matchKind(TkLBrace, state)) { return Compiler_Parser_ParseRecordType(state) }; return SkyErr(sky_concat("Unexpected token in type: ", peekLexeme(state))) }() }() }() }()
}

func Compiler_Parser_ParseTupleTypeRest(items any, state any) any {
	return func() any { if sky_asBool(matchKind(TkComma, state)) { return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := Compiler_Parser_ParseTypeExpr(s1); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { item := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = item; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return Compiler_Parser_ParseTupleTypeRest(sky_concat(items, []any{item}), s2) };  return nil }() }() }() }; return func() any { return func() any { __subject := consume(TkRParen, state); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return SkyOk(SkyTuple2{V0: TupleTypeExpr(items, emptySpan), V1: s1}) };  return nil }() }() }()
}

func Compiler_Parser_ParseRecordType(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; __tup_fields_s2 := Compiler_Parser_ParseRecordTypeFields([]any{}, s1); fields := sky_asTuple2(__tup_fields_s2).V0; _ = fields; s2 := sky_asTuple2(__tup_fields_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkRBrace, s2); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk(SkyTuple2{V0: RecordTypeExpr(fields, emptySpan), V1: s3}) };  return nil }() }() }()
}

func Compiler_Parser_ParseRecordTypeFields(fields any, state any) any {
	return func() any { if sky_asBool(matchKind(TkRBrace, state)) { return SkyTuple2{V0: fields, V1: state} }; return func() any { return func() any { __subject := consume(TkIdentifier, state); if <nil> { return SkyTuple2{V0: fields, V1: state} };  if <nil> { name := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = name; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { return func() any { __subject := consume(TkColon, s1); if <nil> { return SkyTuple2{V0: fields, V1: state} };  if <nil> { s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := Compiler_Parser_ParseTypeExpr(s2); if <nil> { return SkyTuple2{V0: fields, V1: state} };  if <nil> { te := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = te; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { newFields := sky_concat(fields, []any{map[string]any{"name": sky_asMap(name)["lexeme"], "type_": te}}); _ = newFields; s4 := func() any { if sky_asBool(matchKind(TkComma, s3)) { return func() any { __tup_w_s := advance(s3); s := sky_asTuple2(__tup_w_s).V1; _ = s; return s }() }; return s3 }(); _ = s4; return Compiler_Parser_ParseRecordTypeFields(newFields, s4) }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }()
}

func Compiler_Parser_ParseTypeAnnot(state any) any {
	return func() any { __tup_name_s1 := advance(state); name := sky_asTuple2(__tup_name_s1).V0; _ = name; s1 := sky_asTuple2(__tup_name_s1).V1; _ = s1; return func() any { return func() any { __subject := consume(TkColon, s1); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := Compiler_Parser_ParseTypeExpr(s2); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { te := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = te; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk(SkyTuple2{V0: TypeAnnotDecl(sky_asMap(name)["lexeme"], te, emptySpan), V1: s3}) };  return nil }() }() };  return nil }() }() }()
}

func Compiler_Parser_ParseFunDecl(state any) any {
	return func() any { __tup_name_s1 := advance(state); name := sky_asTuple2(__tup_name_s1).V0; _ = name; s1 := sky_asTuple2(__tup_name_s1).V1; _ = s1; __tup_params_s2 := Compiler_Parser_ParseFunParams(s1); params := sky_asTuple2(__tup_params_s2).V0; _ = params; s2 := sky_asTuple2(__tup_params_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkEquals, s2); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s3); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { body := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = body; s4 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s4; return SkyOk(SkyTuple2{V0: FunDecl(sky_asMap(name)["lexeme"], params, body, emptySpan), V1: s4}) };  return nil }() }() };  return nil }() }() }()
}

func Compiler_Parser_ParseFunParams(state any) any {
	return func() any { if sky_asBool(matchKind(TkEquals, state)) { return SkyTuple2{V0: []any{}, V1: state} }; return func() any { if sky_asBool(matchKind(TkIdentifier, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; pat := func() any { if sky_asBool(sky_equal(sky_asMap(tok)["lexeme"], "_")) { return PWildcard(emptySpan) }; return PVariable(sky_asMap(tok)["lexeme"], emptySpan) }(); _ = pat; __tup_rest_s2 := Compiler_Parser_ParseFunParams(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return SkyTuple2{V0: append([]any{pat}, sky_asList(rest)...), V1: s2} }() }; return func() any { if sky_asBool(matchKind(TkLParen, state)) { return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(state); if <nil> { pat := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = pat; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { __tup_rest_s2 := Compiler_Parser_ParseFunParams(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return SkyTuple2{V0: append([]any{pat}, sky_asList(rest)...), V1: s2} }() };  if <nil> { return SkyTuple2{V0: []any{}, V1: state} };  return nil }() }() }; return SkyTuple2{V0: []any{}, V1: state} }() }() }()
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
	return func() any { initial := map[string]any{"source": Compiler_Lexer_Source, "offset": 0, "line": 1, "column": 1, "tokens": []any{}, "indentStack": []any{0}}; _ = initial; final := Compiler_Lexer_LexLoop(initial); _ = final; eofToken := map[string]any{"kind": TkEOF, "lexeme": "", "span": Compiler_Lexer_MakeSpan(final)}; _ = eofToken; return map[string]any{"tokens": sky_listReverse(append([]any{eofToken}, sky_asList(sky_asMap(final)["tokens"])...)), "diagnostics": []any{}} }()
}

func Compiler_Lexer_LexLoop(state any) any {
	return func() any { if sky_asBool(sky_asInt(sky_asMap(state)["offset"]) >= sky_asInt(sky_stringLength(sky_asMap(state)["source"]))) { return state }; return func() any { ch := Compiler_Lexer_CharAt(sky_asMap(state)["source"], sky_asMap(state)["offset"]); _ = ch; return func() any { if sky_asBool(sky_equal(ch, string('\n'))) { return Compiler_Lexer_LexLoop(Compiler_Lexer_HandleNewline(state)) }; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, string(' '))) || sky_asBool(sky_equal(ch, string('\t')))) { return Compiler_Lexer_LexLoop(Compiler_Lexer_SkipWhitespace(state)) }; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, string('-'))) && sky_asBool(sky_equal(Compiler_Lexer_PeekChar(sky_asMap(state)["source"], sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(1)), string('-')))) { return Compiler_Lexer_LexLoop(Compiler_Lexer_SkipLineComment(state)) }; return func() any { if sky_asBool(sky_equal(ch, string('"'))) { return Compiler_Lexer_LexLoop(Compiler_Lexer_LexString(state)) }; return func() any { if sky_asBool(sky_equal(ch, string('\''))) { return Compiler_Lexer_LexLoop(Compiler_Lexer_LexChar(state)) }; return func() any { if sky_asBool(sky_charIsDigit(ch)) { return Compiler_Lexer_LexLoop(Compiler_Lexer_LexNumber(state)) }; return func() any { if sky_asBool(sky_asBool(sky_charIsAlpha(ch)) || sky_asBool(sky_equal(ch, string('_')))) { return Compiler_Lexer_LexLoop(Compiler_Lexer_LexIdentifier(state)) }; return Compiler_Lexer_LexLoop(Compiler_Lexer_LexOperatorOrPunctuation(state)) }() }() }() }() }() }() }() }() }()
}

func Compiler_Lexer_HandleNewline(state any) any {
	return func() any { newState := sky_recordUpdate(state, map[string]any{"offset": sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(1), "line": sky_asInt(sky_asMap(state)["line"]) + sky_asInt(1), "column": 1}); _ = newState; indent := Compiler_Lexer_CountIndent(sky_asMap(newState)["source"], sky_asMap(newState)["offset"]); _ = indent; newOffset := sky_asInt(sky_asMap(newState)["offset"]) + sky_asInt(indent); _ = newOffset; nlToken := map[string]any{"kind": TkNewline, "lexeme": "\\n", "span": Compiler_Lexer_MakeSpan(state)}; _ = nlToken; return sky_recordUpdate(newState, map[string]any{"offset": newOffset, "column": sky_asInt(indent) + sky_asInt(1), "tokens": append([]any{nlToken}, sky_asList(sky_asMap(state)["tokens"])...)}) }()
}

func Compiler_Lexer_CountIndent(source any, offset any) any {
	return func() any { if sky_asBool(sky_asInt(Compiler_Lexer_Offset) >= sky_asInt(sky_stringLength(Compiler_Lexer_Source))) { return 0 }; return func() any { ch := Compiler_Lexer_CharAt(Compiler_Lexer_Source, Compiler_Lexer_Offset); _ = ch; return func() any { if sky_asBool(sky_equal(ch, string(' '))) { return sky_asInt(1) + sky_asInt(Compiler_Lexer_CountIndent(Compiler_Lexer_Source, sky_asInt(Compiler_Lexer_Offset) + sky_asInt(1))) }; return func() any { if sky_asBool(sky_equal(ch, string('\t'))) { return sky_asInt(4) + sky_asInt(Compiler_Lexer_CountIndent(Compiler_Lexer_Source, sky_asInt(Compiler_Lexer_Offset) + sky_asInt(1))) }; return 0 }() }() }() }()
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
	return func() any { ch := Compiler_Lexer_CharAt(sky_asMap(state)["source"], sky_asMap(state)["offset"]); _ = ch; next := Compiler_Lexer_PeekChar(sky_asMap(state)["source"], sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(1)); _ = next; startOffset := sky_asMap(state)["offset"]; _ = startOffset; startCol := sky_asMap(state)["column"]; _ = startCol; __tup_kind_len := func() any { if sky_asBool(sky_equal(ch, string('('))) { return SkyTuple2{V0: TkLParen, V1: 1} }; return func() any { if sky_asBool(sky_equal(ch, string(')'))) { return SkyTuple2{V0: TkRParen, V1: 1} }; return func() any { if sky_asBool(sky_equal(ch, string('['))) { return SkyTuple2{V0: TkLBracket, V1: 1} }; return func() any { if sky_asBool(sky_equal(ch, string(']'))) { return SkyTuple2{V0: TkRBracket, V1: 1} }; return func() any { if sky_asBool(sky_equal(ch, string('{'))) { return SkyTuple2{V0: TkLBrace, V1: 1} }; return func() any { if sky_asBool(sky_equal(ch, string('}'))) { return SkyTuple2{V0: TkRBrace, V1: 1} }; return func() any { if sky_asBool(sky_equal(ch, string(','))) { return SkyTuple2{V0: TkComma, V1: 1} }; return func() any { if sky_asBool(sky_equal(ch, string('\\'))) { return SkyTuple2{V0: TkBackslash, V1: 1} }; return func() any { if sky_asBool(sky_equal(ch, string('.'))) { return SkyTuple2{V0: TkDot, V1: 1} }; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, string(':'))) && sky_asBool(!sky_equal(next, string(':')))) { return SkyTuple2{V0: TkColon, V1: 1} }; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, string('='))) && sky_asBool(!sky_equal(next, string('=')))) { return SkyTuple2{V0: TkEquals, V1: 1} }; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, string('|'))) && sky_asBool(sky_asBool(!sky_equal(next, string('>'))) && sky_asBool(!sky_equal(next, string('|'))))) { return SkyTuple2{V0: TkPipe, V1: 1} }; return func() any { if sky_asBool(sky_asBool(sky_equal(ch, string('-'))) && sky_asBool(sky_equal(next, string('>')))) { return SkyTuple2{V0: TkArrow, V1: 2} }; return func() any { opLen := Compiler_Lexer_ConsumeOperator(sky_asMap(state)["source"], sky_asMap(state)["offset"]); _ = opLen; return SkyTuple2{V0: TkOperator, V1: opLen} }() }() }() }() }() }() }() }() }() }() }() }() }() }(); kind := sky_asTuple2(__tup_kind_len).V0; _ = kind; len := sky_asTuple2(__tup_kind_len).V1; _ = len; s1 := sky_recordUpdate(state, map[string]any{"offset": sky_asInt(sky_asMap(state)["offset"]) + sky_asInt(len), "column": sky_asInt(sky_asMap(state)["column"]) + sky_asInt(len)}); _ = s1; lexeme := sky_call2(sky_stringSlice(startOffset), sky_asMap(s1)["offset"], sky_asMap(state)["source"]); _ = lexeme; token := map[string]any{"kind": kind, "lexeme": lexeme, "span": map[string]any{"start": map[string]any{"offset": startOffset, "line": sky_asMap(state)["line"], "column": startCol}, "end": map[string]any{"offset": sky_asMap(s1)["offset"], "line": sky_asMap(s1)["line"], "column": sky_asMap(s1)["column"]}}}; _ = token; return sky_recordUpdate(s1, map[string]any{"tokens": append([]any{token}, sky_asList(sky_asMap(s1)["tokens"])...)}) }()
}

func Compiler_Lexer_ConsumeOperator(source any, offset any) any {
	return func() any { if sky_asBool(sky_asInt(Compiler_Lexer_Offset) >= sky_asInt(sky_stringLength(Compiler_Lexer_Source))) { return 0 }; return func() any { ch := Compiler_Lexer_CharAt(Compiler_Lexer_Source, Compiler_Lexer_Offset); _ = ch; return func() any { if sky_asBool(Compiler_Lexer_IsOperatorChar(ch)) { return sky_asInt(1) + sky_asInt(Compiler_Lexer_ConsumeOperator(Compiler_Lexer_Source, sky_asInt(Compiler_Lexer_Offset) + sky_asInt(1))) }; return 0 }() }() }()
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
	return func() any { if sky_asBool(sky_asInt(idx) >= sky_asInt(sky_stringLength(str))) { return -1 }; return func() any { if sky_asBool(sky_equal(sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1), str), sky_stringFromChar(ch))) { return idx }; return Compiler_Pipeline_FindCharIdx(ch, str, sky_asInt(idx) + sky_asInt(1)) }() }()
}

func Compiler_Pipeline_FindLastSlash(path any, idx any) any {
	return func() any { if sky_asBool(sky_asInt(idx) < sky_asInt(0)) { return -1 }; return func() any { if sky_asBool(sky_equal(sky_call2(sky_stringSlice(idx), sky_asInt(idx) + sky_asInt(1), path), "/")) { return idx }; return Compiler_Pipeline_FindLastSlash(path, sky_asInt(idx) - sky_asInt(1)) }() }()
}

func Compiler_Pipeline_Compile(filePath any, outDir any) any {
	return func() any { sky_println("╔══════════════════════════════════════════════════╗"); sky_println("║  Sky Self-Hosted Compiler v0.4.2                ║"); sky_println("╚══════════════════════════════════════════════════╝"); sky_println(""); return func() any { return func() any { __subject := sky_fileRead(filePath); if <nil> { readErr := sky_asSkyResult(__subject).ErrValue; _ = readErr; return SkyErr(sky_concat("Cannot read file: ", sky_concat(filePath, sky_concat(" (", sky_concat(readErr, ")"))))) };  if <nil> { source := sky_asSkyResult(__subject).OkValue; _ = source; return func() any { lexResult := Compiler_Lexer_Lex(source); _ = lexResult; return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if <nil> { parseErr := sky_asSkyResult(__subject).ErrValue; _ = parseErr; return SkyErr(sky_concat("Parse error: ", parseErr)) };  if <nil> { mod := sky_asSkyResult(__subject).OkValue; _ = mod; return func() any { srcRoot := Compiler_Pipeline_DirOfPath(filePath); _ = srcRoot; hasLocalImports := sky_call2(sky_listFoldl(func(imp any) any { return func(acc any) any { return sky_asBool(acc) || sky_asBool(sky_not(Compiler_Resolver_IsStdlib(sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"])))) } }), false, sky_asMap(mod)["imports"]); _ = hasLocalImports; return func() any { if sky_asBool(hasLocalImports) { return Compiler_Pipeline_CompileMultiModule(filePath, outDir, srcRoot, mod) }; return Compiler_Pipeline_CompileSource(filePath, outDir, source) }() }() };  return nil }() }() }() };  return nil }() }() }()
}

func Compiler_Pipeline_CompileMultiModule(entryPath any, outDir any, srcRoot any, entryMod any) any {
	return func() any { localImports := sky_call(sky_listFilter(func(imp any) any { return sky_not(Compiler_Resolver_IsStdlib(sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]))) }), sky_asMap(entryMod)["imports"]); _ = localImports; loadedModules := Compiler_Pipeline_LoadLocalModules(srcRoot, localImports, []any{}); _ = loadedModules; aliasMap := Compiler_Pipeline_BuildAliasMap(sky_asMap(entryMod)["imports"]); _ = aliasMap; stdlibEnv := Compiler_Resolver_BuildStdlibEnv(); _ = stdlibEnv; depDecls := sky_call(sky_listConcatMap(func(__pa0 any) any { return Compiler_Pipeline_CompileDependencyModule(stdlibEnv, loadedModules, __pa0) }), loadedModules); _ = depDecls; return Compiler_Pipeline_CompileMultiModuleEntry(outDir, entryMod, aliasMap, stdlibEnv, depDecls, loadedModules) }()
}

func Compiler_Pipeline_CompileMultiModuleEntry(outDir any, entryMod any, aliasMap any, stdlibEnv any, depDecls any, loadedModules any) any {
	return func() any { entryCheckResult := Compiler_Checker_CheckModule(entryMod, SkyJust(stdlibEnv)); _ = entryCheckResult; entryRegistry := func() any { return func() any { __subject := entryCheckResult; if <nil> { result := sky_asSkyResult(__subject).OkValue; _ = result; return sky_asMap(result)["registry"] };  if <nil> { return Compiler_Adt_EmptyRegistry() };  return nil }() }(); _ = entryRegistry; return Compiler_Pipeline_EmitMultiModuleGo(outDir, entryMod, entryRegistry, aliasMap, depDecls, loadedModules) }()
}

func Compiler_Pipeline_EmitMultiModuleGo(outDir any, entryMod any, entryRegistry any, aliasMap any, depDecls any, loadedModules any) any {
	return func() any { baseCtx := Compiler_Lower_EmptyCtx(); _ = baseCtx; entryCtx := sky_recordUpdate(baseCtx, map[string]any{"registry": entryRegistry, "localFunctions": Compiler_Lower_CollectLocalFunctions(sky_asMap(entryMod)["declarations"]), "importedConstructors": Compiler_Lower_BuildConstructorMap(entryRegistry), "importAliases": aliasMap}); _ = entryCtx; entryGoDecls := Compiler_Lower_LowerDeclarations(entryCtx, sky_asMap(entryMod)["declarations"]); _ = entryGoDecls; entryCtorDecls := Compiler_Lower_GenerateConstructorDecls(entryRegistry, sky_asMap(entryMod)["declarations"]); _ = entryCtorDecls; helperDecls := Compiler_Lower_GenerateHelperDecls(); _ = helperDecls; allDecls := Compiler_Pipeline_DeduplicateDecls(sky_listConcat([]any{helperDecls, depDecls, entryCtorDecls, entryGoDecls})); _ = allDecls; return Compiler_Pipeline_WriteMultiModuleOutput(outDir, allDecls) }()
}

func Compiler_Pipeline_WriteMultiModuleOutput(outDir any, allDecls any) any {
	return func() any { goPackage := Compiler_Pipeline_MakeGoPackage(allDecls); _ = goPackage; rawGoCode := Compiler_Emit_EmitPackage(goPackage); _ = rawGoCode; goCode := Compiler_Pipeline_FixUnusedVars(rawGoCode); _ = goCode; outPath := sky_concat(outDir, "/main.go"); _ = outPath; sky_fileMkdirAll(outDir); sky_call(sky_fileWrite(outPath), goCode); sky_println(sky_concat("   Wrote ", outPath)); sky_println(sky_concat("   ", sky_concat(sky_stringFromInt(sky_listLength(allDecls)), " total Go declarations"))); sky_println(""); sky_println("✓ Compilation successful"); return SkyOk(goCode) }()
}

func Compiler_Pipeline_FixUnusedVars(code any) any {
	return code
}

func Compiler_Pipeline_MakeGoPackage(decls any) any {
	return map[string]any{"name": "main", "imports": []any{map[string]any{"path": "fmt", "alias_": ""}, map[string]any{"path": "bufio", "alias_": ""}, map[string]any{"path": "os", "alias_": ""}, map[string]any{"path": "os/exec", "alias_": "exec"}, map[string]any{"path": "strconv", "alias_": ""}, map[string]any{"path": "strings", "alias_": ""}}, "declarations": decls}
}

func Compiler_Pipeline_LoadLocalModules(srcRoot any, imports any, acc any) any {
	return func() any { return func() any { __subject := imports; if <nil> { return sky_listReverse(acc) };  if <nil> { imp := sky_asList(__subject)[0]; _ = imp; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { modName := sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]); _ = modName; filePath := Compiler_Resolver_ResolveModulePath(srcRoot, sky_asMap(imp)["moduleName"]); _ = filePath; return func() any { if sky_asBool(sky_asBool(Compiler_Resolver_IsStdlib(modName)) || sky_asBool(Compiler_Pipeline_IsModuleLoaded(modName, acc))) { return Compiler_Pipeline_LoadLocalModules(srcRoot, rest, acc) }; return func() any { return func() any { __subject := sky_fileRead(filePath); if <nil> { return Compiler_Pipeline_LoadLocalModules(srcRoot, rest, acc) };  if <nil> { source := sky_asSkyResult(__subject).OkValue; _ = source; return func() any { lexResult := Compiler_Lexer_Lex(source); _ = lexResult; return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if <nil> { return Compiler_Pipeline_LoadLocalModules(srcRoot, rest, acc) };  if <nil> { mod := sky_asSkyResult(__subject).OkValue; _ = mod; return func() any { transImports := sky_call(sky_listFilter(func(i any) any { return sky_not(Compiler_Resolver_IsStdlib(sky_call(sky_stringJoin("."), sky_asMap(i)["moduleName"]))) }), sky_asMap(mod)["imports"]); _ = transImports; withTransitive := Compiler_Pipeline_LoadLocalModules(srcRoot, transImports, append([]any{SkyTuple2{V0: modName, V1: mod}}, sky_asList(acc)...)); _ = withTransitive; return Compiler_Pipeline_LoadLocalModules(srcRoot, rest, withTransitive) }() };  return nil }() }() }() };  return nil }() }() }() }() };  return nil }() }()
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
	return func() any { return func() any { __subject := decls; if <nil> { return sky_listReverse(acc) };  if <nil> { decl := sky_asList(__subject)[0]; _ = decl; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { name := Compiler_Pipeline_GetDeclName(decl); _ = name; return func() any { if sky_asBool(sky_stringIsEmpty(name)) { return Compiler_Pipeline_DeduplicateDeclsLoop(rest, seen, append([]any{decl}, sky_asList(acc)...)) }; return func() any { return func() any { __subject := sky_call(sky_dictGet(name), seen); if <nil> { return Compiler_Pipeline_DeduplicateDeclsLoop(rest, seen, acc) };  if <nil> { return Compiler_Pipeline_DeduplicateDeclsLoop(rest, sky_call2(sky_dictInsert(name), "1", seen), append([]any{decl}, sky_asList(acc)...)) };  return nil }() }() }() }() };  return nil }() }()
}

func Compiler_Pipeline_GetDeclName(decl any) any {
	return func() any { return func() any { __subject := decl; if <nil> { funcDecl := sky_asMap(__subject)["V0"]; _ = funcDecl; return sky_asMap(funcDecl)["name"] };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return name };  if <nil> { code := sky_asMap(__subject)["V0"]; _ = code; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("var "), code)) { return Compiler_Pipeline_ExtractVarName(code) }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("func "), code)) { return Compiler_Pipeline_ExtractFuncName(code) }; return func() any { if sky_asBool(sky_call(sky_stringStartsWith("type "), code)) { return Compiler_Pipeline_ExtractTypeName(code) }; return "" }() }() }() };  return nil }() }()
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
	return func() any { return func() any { __subject := Compiler_Pipeline_FindModule(modName, allModules); if <nil> { return []any{} };  if <nil> { mod := sky_asSkyMaybe(__subject).JustValue; _ = mod; return func() any { prefix := sky_call2(sky_stringReplace("."), "_", modName); _ = prefix; declNames := Compiler_Pipeline_ExtractExportedNames(mod); _ = declNames; return sky_call(sky_listFilterMap(func(name any) any { return func() any { if sky_asBool(sky_stringIsEmpty(name)) { return SkyNothing() }; return SkyJust(GoDeclRaw(sky_concat("var ", sky_concat(name, sky_concat(" = ", sky_concat(prefix, sky_concat("_", Compiler_Pipeline_CapitalizeFirst(name)))))))) }() }), declNames) }() };  return nil }() }()
}

func Compiler_Pipeline_IsExposingAll(clause any) any {
	return sky_asBool(sky_not(Compiler_Pipeline_IsExposingNone(clause))) && sky_asBool(sky_listIsEmpty(Compiler_Pipeline_GetExposeNames(clause)))
}

func Compiler_Pipeline_IsExposingNone(clause any) any {
	return func() any { return func() any { __subject := clause; if <nil> { return true };  if true { return false };  return nil }() }()
}

func Compiler_Pipeline_GetExposeNames(clause any) any {
	return func() any { return func() any { __subject := clause; if <nil> { names := sky_asMap(__subject)["V0"]; _ = names; return names };  if true { return []any{} };  return nil }() }()
}

func Compiler_Pipeline_FindModule(modName any, modules any) any {
	return func() any { return func() any { __subject := modules; if <nil> { return SkyNothing() };  if <nil> { pair := sky_asList(__subject)[0]; _ = pair; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { if sky_asBool(sky_equal(sky_fst(pair), modName)) { return SkyJust(sky_snd(pair)) }; return Compiler_Pipeline_FindModule(modName, rest) }() };  return nil }() }()
}

func Compiler_Pipeline_ExtractExportedNames(mod any) any {
	return sky_call(sky_listConcatMap(Compiler_Pipeline_ExtractDeclNames), sky_asMap(mod)["declarations"])
}

func Compiler_Pipeline_ExtractDeclNames(decl any) any {
	return func() any { return func() any { __subject := decl; if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return []any{name} };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; variants := sky_asMap(__subject)["V2"]; _ = variants; return append([]any{name}, sky_asList(sky_call(sky_listMap(func(v any) any { return sky_asMap(v)["name"] }), variants))...) };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return []any{name} };  if <nil> { return []any{} };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return []any{name} };  return nil }() }()
}

func Compiler_Pipeline_CollectAllFunctionNames(decls any) any {
	return Compiler_Pipeline_DeduplicateStringList(sky_call(sky_listFilterMap(Compiler_Pipeline_ExtractDeclNameForFn), decls))
}

func Compiler_Pipeline_ExtractDeclNameForFn(d any) any {
	return func() any { return func() any { __subject := d; if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return SkyJust(name) };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return SkyJust(name) };  if true { return SkyNothing() };  return nil }() }()
}

func Compiler_Pipeline_DeduplicateStringList(items any) any {
	return Compiler_Pipeline_DeduplicateStringsLoop(items, sky_dictEmpty(), []any{})
}

func Compiler_Pipeline_DeduplicateStringsLoop(items any, seen any, acc any) any {
	return func() any { return func() any { __subject := items; if <nil> { return sky_listReverse(acc) };  if <nil> { item := sky_asList(__subject)[0]; _ = item; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := sky_call(sky_dictGet(item), seen); if <nil> { return Compiler_Pipeline_DeduplicateStringsLoop(rest, seen, acc) };  if <nil> { return Compiler_Pipeline_DeduplicateStringsLoop(rest, sky_call2(sky_dictInsert(item), "1", seen), append([]any{item}, sky_asList(acc)...)) };  return nil }() }() };  return nil }() }()
}

func Compiler_Pipeline_IsModuleLoaded(modName any, loaded any) any {
	return sky_call2(sky_listFoldl(func(pair any) any { return func(acc any) any { return sky_asBool(acc) || sky_asBool(sky_equal(sky_fst(pair), modName)) } }), false, loaded)
}

func Compiler_Pipeline_AddImportAlias(imp any, acc any) any {
	return func() any { modName := sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]); _ = modName; return func() any { if sky_asBool(Compiler_Resolver_IsStdlib(modName)) { return acc }; return sky_call2(sky_dictInsert(Compiler_Pipeline_ImportAlias(imp)), modName, acc) }() }()
}

func Compiler_Pipeline_ImportAlias(imp any) any {
	return func() any { if sky_asBool(sky_stringIsEmpty(sky_asMap(imp)["alias_"])) { return func() any { return func() any { __subject := sky_listReverse(sky_asMap(imp)["moduleName"]); if <nil> { last := sky_asList(__subject)[0]; _ = last; return last };  if <nil> { return "" };  return nil }() }() }; return sky_asMap(imp)["alias_"] }()
}

func Compiler_Pipeline_CompileDependencyModule(stdlibEnv any, allModules any, pair any) any {
	return func() any { modName := sky_fst(pair); _ = modName; mod := sky_snd(pair); _ = mod; prefix := sky_call2(sky_stringReplace("."), "_", modName); _ = prefix; checkResult := Compiler_Checker_CheckModule(mod, SkyJust(stdlibEnv)); _ = checkResult; registry := func() any { return func() any { __subject := checkResult; if <nil> { result := sky_asSkyResult(__subject).OkValue; _ = result; return sky_asMap(result)["registry"] };  if <nil> { return Compiler_Adt_EmptyRegistry() };  return nil }() }(); _ = registry; depBaseCtx := Compiler_Lower_EmptyCtx(); _ = depBaseCtx; depAliasMap := Compiler_Pipeline_BuildAliasMap(sky_asMap(mod)["imports"]); _ = depAliasMap; localFns := Compiler_Pipeline_CollectAllFunctionNames(sky_asMap(mod)["declarations"]); _ = localFns; sky_println(sky_concat("   [DEBUG] ", sky_concat(modName, sky_concat(": ", sky_concat(sky_stringFromInt(sky_listLength(localFns)), sky_concat(" fns: ", sky_call(sky_stringJoin(","), localFns))))))); ctx := sky_recordUpdate(depBaseCtx, map[string]any{"registry": registry, "localFunctions": localFns, "importedConstructors": Compiler_Lower_BuildConstructorMap(registry), "modulePrefix": prefix, "importAliases": depAliasMap, "localFunctionArity": Compiler_Lower_CollectLocalFunctionArities(sky_asMap(mod)["declarations"])}); _ = ctx; goDecls := Compiler_Lower_LowerDeclarations(ctx, sky_asMap(mod)["declarations"]); _ = goDecls; ctorDecls := Compiler_Lower_GenerateConstructorDecls(registry, sky_asMap(mod)["declarations"]); _ = ctorDecls; filtered := sky_call(sky_listFilter(Compiler_Pipeline_IsExportableDecl), sky_call(sky_listAppend(ctorDecls), goDecls)); _ = filtered; prefixed := sky_call(sky_listMap(func(__pa0 any) any { return Compiler_Pipeline_PrefixDecl(prefix, __pa0) }), filtered); _ = prefixed; aliases := Compiler_Pipeline_GenerateConstructorAliases(prefix, prefixed); _ = aliases; return Compiler_Pipeline_DeduplicateDecls(sky_call(sky_listAppend(aliases), prefixed)) }()
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
	return func() any { return func() any { __subject := decl; if <nil> { funcDecl := sky_asMap(__subject)["V0"]; _ = funcDecl; return sky_asBool(!sky_equal(sky_asMap(funcDecl)["name"], "_")) && sky_asBool(!sky_equal(sky_asMap(funcDecl)["name"], "main")) };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return !sky_equal(name, "_") };  if <nil> { return true };  return nil }() }()
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

func Compiler_Pipeline_PrefixDecl(prefix any, decl any) any {
	return func() any { return func() any { __subject := decl; if <nil> { funcDecl := sky_asMap(__subject)["V0"]; _ = funcDecl; return func() any { if sky_asBool(sky_equal(sky_asMap(funcDecl)["name"], "main")) { return decl }; return GoDeclFunc(sky_recordUpdate(funcDecl, map[string]any{"name": sky_concat(prefix, sky_concat("_", Compiler_Pipeline_CapitalizeFirst(sky_asMap(funcDecl)["name"])))})) }() };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; expr := sky_asMap(__subject)["V1"]; _ = expr; return func() any { if sky_asBool(sky_asBool(sky_equal(name, "_")) || sky_asBool(sky_call(sky_stringStartsWith("var _ ="), name))) { return decl }; return GoDeclVar(sky_concat(prefix, sky_concat("_", Compiler_Pipeline_CapitalizeFirst(name))), expr) }() };  if <nil> { code := sky_asMap(__subject)["V0"]; _ = code; return decl };  return nil }() }()
}

func Compiler_Pipeline_CompileSource(filePath any, outDir any, source any) any {
	return func() any { sky_println(sky_concat("── Lexing ", filePath)); lexResult := Compiler_Lexer_Lex(source); _ = lexResult; sky_println(sky_concat("   ", sky_concat(sky_stringFromInt(sky_listLength(sky_asMap(lexResult)["tokens"])), " tokens"))); return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if <nil> { parseErr := sky_asSkyResult(__subject).ErrValue; _ = parseErr; return SkyErr(sky_concat("Parse error: ", parseErr)) };  if <nil> { mod := sky_asSkyResult(__subject).OkValue; _ = mod; return func() any { sky_println("── Parsing"); sky_println(sky_concat("   Module: ", sky_call(sky_stringJoin("."), sky_asMap(mod)["name"]))); sky_println(sky_concat("   ", sky_concat(sky_stringFromInt(sky_listLength(sky_asMap(mod)["declarations"])), sky_concat(" declarations, ", sky_concat(sky_stringFromInt(sky_listLength(sky_asMap(mod)["imports"])), " imports"))))); return Compiler_Pipeline_CompileModule(filePath, outDir, mod) }() };  return nil }() }() }()
}

func Compiler_Pipeline_CompileModule(filePath any, outDir any, mod any) any {
	return func() any { counter := sky_refNew(100); _ = counter; sky_println(sky_concat("── Type Checking (src: ", sky_concat(Compiler_Pipeline_InferSrcRoot(filePath, sky_asMap(mod)["name"]), ")"))); stdlibEnv := Compiler_Resolver_BuildStdlibEnv(); _ = stdlibEnv; checkResult := Compiler_Checker_CheckModule(mod, SkyJust(stdlibEnv)); _ = checkResult; return func() any { return func() any { __subject := checkResult; if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Type error: ", e)) };  if <nil> { result := sky_asSkyResult(__subject).OkValue; _ = result; return func() any { Compiler_Pipeline_PrintDiagnostics(sky_asMap(result)["diagnostics"]); Compiler_Pipeline_PrintTypedDecls(sky_asMap(result)["declarations"]); sky_println("── Lowering to Go IR"); goPackage := Compiler_Lower_LowerModule(sky_asMap(result)["registry"], mod); _ = goPackage; sky_println(sky_concat("   ", sky_concat(sky_stringFromInt(sky_listLength(sky_asMap(goPackage)["declarations"])), " Go declarations"))); sky_println("── Emitting Go"); goCode := Compiler_Emit_EmitPackage(goPackage); _ = goCode; outPath := sky_concat(outDir, "/main.go"); _ = outPath; sky_fileMkdirAll(outDir); sky_call(sky_fileWrite(outPath), goCode); sky_println(sky_concat("   Wrote ", outPath)); sky_println(""); sky_println("✓ Compilation successful"); return SkyOk(goCode) }() };  return nil }() }() }()
}

func Compiler_Pipeline_CompileProject(entryPath any, outDir any) any {
	return func() any { srcRoot := Compiler_Pipeline_InferSrcRootFromEntry(entryPath); _ = srcRoot; return func() any { return func() any { __subject := Compiler_Resolver_ResolveProject(entryPath, srcRoot); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { graph := sky_asSkyResult(__subject).OkValue; _ = graph; return func() any { return func() any { __subject := Compiler_Resolver_CheckAllModules(graph); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { checkedGraph := sky_asSkyResult(__subject).OkValue; _ = checkedGraph; return func() any { return func() any { __subject := sky_listReverse(sky_asMap(checkedGraph)["modules"]); if <nil> { return SkyErr("No modules to compile") };  if <nil> { entryMod := sky_asList(__subject)[0]; _ = entryMod; return func() any { registry := func() any { return func() any { __subject := sky_asMap(entryMod)["checkResult"]; if <nil> { r := sky_asSkyMaybe(__subject).JustValue; _ = r; return sky_asMap(r)["registry"] };  if <nil> { return Compiler_Adt_EmptyRegistry() };  return nil }() }(); _ = registry; goPackage := Compiler_Lower_LowerModule(registry, sky_asMap(entryMod)["ast"]); _ = goPackage; goCode := Compiler_Emit_EmitPackage(goPackage); _ = goCode; outPath := sky_concat(outDir, "/main.go"); _ = outPath; sky_fileMkdirAll(outDir); sky_call(sky_fileWrite(outPath), goCode); return SkyOk(goCode) }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }()
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
	return func() any { return func() any { __subject := diags; if <nil> { return struct{}{} };  if true { return func() any { sky_println(sky_concat("   ⚠ ", sky_concat(sky_stringFromInt(sky_listLength(diags)), " diagnostics:"))); sky_call(sky_listMap(func(d any) any { return sky_println(sky_concat("     ", d)) }), diags); return struct{}{} }() };  return nil }() }()
}

func Compiler_Pipeline_PrintTypedDecls(decls any) any {
	return func() any { sky_call(sky_listMap(func(d any) any { return sky_println(sky_concat("   ", sky_concat(sky_asMap(d)["name"], sky_concat(" : ", sky_asMap(d)["prettyType"])))) }), decls); return struct{}{} }()
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
	return func() any { return func() any { __subject := Compiler_ParserExpr_ParseApplication(state); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return Compiler_ParserExpr_ParseExprLoop(minPrec, sky_fst(pair), sky_snd(pair)) };  return nil }() }()
}

func Compiler_ParserExpr_ParseExprLoop(minPrec any, left any, state any) any {
	return func() any { if sky_asBool(matchKind(TkEquals, state)) { return SkyOk(SkyTuple2{V0: left, V1: state}) }; return func() any { if sky_asBool(sky_asInt(peekColumn(state)) <= sky_asInt(1)) { return SkyOk(SkyTuple2{V0: left, V1: state}) }; return func() any { if sky_asBool(matchKind(TkPipe, state)) { return SkyOk(SkyTuple2{V0: left, V1: state}) }; return func() any { if sky_asBool(false) { return SkyOk(SkyTuple2{V0: left, V1: state}) }; return func() any { if sky_asBool(matchKind(TkOperator, state)) { return func() any { opToken := peek(state); _ = opToken; info := Compiler_ParserExpr_GetOperatorInfo(sky_asMap(opToken)["lexeme"]); _ = info; return func() any { return func() any { __subject := info; if <nil> { return SkyOk(SkyTuple2{V0: left, V1: state}) };  if <nil> { pair := sky_asSkyMaybe(__subject).JustValue; _ = pair; return func() any { prec := sky_fst(pair); _ = prec; assoc := sky_snd(pair); _ = assoc; return func() any { if sky_asBool(sky_asInt(prec) < sky_asInt(minPrec)) { return SkyOk(SkyTuple2{V0: left, V1: state}) }; return func() any { __tup_advTok_s1 := advance(state); advTok := sky_asTuple2(__tup_advTok_s1).V0; _ = advTok; s1 := sky_asTuple2(__tup_advTok_s1).V1; _ = s1; nextMin := func() any { if sky_asBool(sky_equal(assoc, "left")) { return sky_asInt(prec) + sky_asInt(1) }; return prec }(); _ = nextMin; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(nextMin, s1); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { right := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = right; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return Compiler_ParserExpr_ParseExprLoop(minPrec, BinaryExpr(sky_asMap(opToken)["lexeme"], left, right, emptySpan), s2) };  return nil }() }() }() }() }() };  return nil }() }() }() }; return SkyOk(SkyTuple2{V0: left, V1: state}) }() }() }() }() }()
}

func Compiler_ParserExpr_GetOperatorInfo(op any) any {
	return func() any { if sky_asBool(sky_equal(op, "||")) { return SkyJust(SkyTuple2{V0: 2, V1: "right"}) }; return func() any { if sky_asBool(sky_equal(op, "&&")) { return SkyJust(SkyTuple2{V0: 3, V1: "right"}) }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "==")) || sky_asBool(sky_asBool(sky_equal(op, "!=")) || sky_asBool(sky_asBool(sky_equal(op, "/=")) || sky_asBool(sky_asBool(sky_equal(op, "<")) || sky_asBool(sky_asBool(sky_equal(op, "<=")) || sky_asBool(sky_asBool(sky_equal(op, ">")) || sky_asBool(sky_equal(op, ">=")))))))) { return SkyJust(SkyTuple2{V0: 4, V1: "left"}) }; return func() any { if sky_asBool(sky_equal(op, "++")) { return SkyJust(SkyTuple2{V0: 5, V1: "right"}) }; return func() any { if sky_asBool(sky_equal(op, "::")) { return SkyJust(SkyTuple2{V0: 5, V1: "right"}) }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "+")) || sky_asBool(sky_equal(op, "-"))) { return SkyJust(SkyTuple2{V0: 6, V1: "left"}) }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "*")) || sky_asBool(sky_asBool(sky_equal(op, "/")) || sky_asBool(sky_asBool(sky_equal(op, "//")) || sky_asBool(sky_equal(op, "%"))))) { return SkyJust(SkyTuple2{V0: 7, V1: "left"}) }; return func() any { if sky_asBool(sky_equal(op, "|>")) { return SkyJust(SkyTuple2{V0: 1, V1: "left"}) }; return func() any { if sky_asBool(sky_equal(op, "<|")) { return SkyJust(SkyTuple2{V0: 1, V1: "right"}) }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, ">>")) || sky_asBool(sky_equal(op, "<<"))) { return SkyJust(SkyTuple2{V0: 9, V1: "right"}) }; return SkyNothing() }() }() }() }() }() }() }() }() }() }()
}

func Compiler_ParserExpr_ParseApplication(state any) any {
	return func() any { fnCol := peekColumn(state); _ = fnCol; return func() any { return func() any { __subject := Compiler_ParserExpr_ParsePrimary(state); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { fn := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = fn; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return Compiler_ParserExpr_ParseApplicationArgs(fnCol, fn, s1) };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseApplicationArgs(fnCol any, fn any, state any) any {
	return func() any { if sky_asBool(Compiler_ParserExpr_IsStartOfPrimary(state)) { return func() any { if sky_asBool(sky_asInt(peekColumn(state)) <= sky_asInt(1)) { return SkyOk(SkyTuple2{V0: fn, V1: state}) }; return func() any { if sky_asBool(sky_asInt(peekColumn(state)) < sky_asInt(fnCol)) { return SkyOk(SkyTuple2{V0: fn, V1: state}) }; return func() any { if sky_asBool(sky_asBool(matchKind(TkIdentifier, state)) && sky_asBool(tokenKindEq(peekAt1Kind(state), TkEquals))) { return SkyOk(SkyTuple2{V0: fn, V1: state}) }; return func() any { if sky_asBool(tokenKindEq(peekAt1Kind(state), TkArrow)) { return SkyOk(SkyTuple2{V0: fn, V1: state}) }; return func() any { if sky_asBool(matchKind(TkPipe, state)) { return SkyOk(SkyTuple2{V0: fn, V1: state}) }; return func() any { return func() any { __subject := Compiler_ParserExpr_ParsePrimary(state); if <nil> { return SkyOk(SkyTuple2{V0: fn, V1: state}) };  if <nil> { arg := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = arg; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return Compiler_ParserExpr_ParseApplicationArgs(fnCol, CallExpr(fn, []any{arg}, emptySpan), s1) };  return nil }() }() }() }() }() }() }() }; return SkyOk(SkyTuple2{V0: fn, V1: state}) }()
}

func Compiler_ParserExpr_IsStartOfPrimary(state any) any {
	return sky_asBool(matchKind(TkIdentifier, state)) || sky_asBool(sky_asBool(matchKind(TkUpperIdentifier, state)) || sky_asBool(sky_asBool(matchKind(TkInteger, state)) || sky_asBool(sky_asBool(matchKind(TkFloat, state)) || sky_asBool(sky_asBool(matchKind(TkString, state)) || sky_asBool(sky_asBool(matchKind(TkChar, state)) || sky_asBool(sky_asBool(matchKind(TkLParen, state)) || sky_asBool(sky_asBool(matchKind(TkLBrace, state)) || sky_asBool(sky_asBool(matchKind(TkLBracket, state)) || sky_asBool(sky_asBool(matchKind(TkBackslash, state)) || sky_asBool(sky_asBool(matchKindLex(TkKeyword, "case", state)) || sky_asBool(sky_asBool(matchKindLex(TkKeyword, "if", state)) || sky_asBool(matchKindLex(TkKeyword, "let", state)))))))))))))
}

func Compiler_ParserExpr_ParsePrimary(state any) any {
	return func() any { if sky_asBool(matchKindLex(TkKeyword, "case", state)) { return Compiler_ParserExpr_ParseCaseExpr(state) }; return func() any { if sky_asBool(matchKindLex(TkKeyword, "if", state)) { return Compiler_ParserExpr_ParseIfExpr(state) }; return func() any { if sky_asBool(matchKindLex(TkKeyword, "let", state)) { return Compiler_ParserExpr_ParseLetExpr(state) }; return func() any { if sky_asBool(matchKind(TkBackslash, state)) { return Compiler_ParserExpr_ParseLambdaExpr(state) }; return func() any { if sky_asBool(sky_asBool(matchKind(TkOperator, state)) && sky_asBool(sky_equal(peekLexeme(state), "-"))) { return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := Compiler_ParserExpr_ParsePrimary(s1); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { inner := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = inner; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return SkyOk(SkyTuple2{V0: NegateExpr(inner, emptySpan), V1: s2}) };  return nil }() }() }() }; return func() any { if sky_asBool(matchKind(TkLBrace, state)) { return Compiler_ParserExpr_ParseRecordOrUpdate(state) }; return func() any { if sky_asBool(matchKind(TkLParen, state)) { return Compiler_ParserExpr_ParseParenOrTuple(state) }; return func() any { if sky_asBool(matchKind(TkLBracket, state)) { return Compiler_ParserExpr_ParseListExpr(state) }; return func() any { if sky_asBool(matchKind(TkUpperIdentifier, state)) { return Compiler_ParserExpr_ParseQualifiedOrConstructor(state) }; return func() any { if sky_asBool(matchKind(TkIdentifier, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; return func() any { if sky_asBool(sky_asBool(matchKind(TkDot, s1)) && sky_asBool(matchKind(TkIdentifier, func() any { __tup_w_s := advance(s1); s := sky_asTuple2(__tup_w_s).V1; _ = s; return s }()))) { return Compiler_ParserExpr_ParseFieldAccess(tok, s1) }; return SkyOk(SkyTuple2{V0: IdentifierExpr(sky_asMap(tok)["lexeme"], sky_asMap(tok)["span"]), V1: s1}) }() }() }; return func() any { if sky_asBool(matchKind(TkInteger, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; return SkyOk(SkyTuple2{V0: IntLitExpr(0, sky_asMap(tok)["lexeme"], sky_asMap(tok)["span"]), V1: s1}) }() }; return func() any { if sky_asBool(matchKind(TkFloat, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; return SkyOk(SkyTuple2{V0: FloatLitExpr(0.0, sky_asMap(tok)["lexeme"], sky_asMap(tok)["span"]), V1: s1}) }() }; return func() any { if sky_asBool(matchKind(TkString, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; value := sky_call2(sky_stringSlice(1), sky_asInt(sky_stringLength(sky_asMap(tok)["lexeme"])) - sky_asInt(1), sky_asMap(tok)["lexeme"]); _ = value; return SkyOk(SkyTuple2{V0: StringLitExpr(value, sky_asMap(tok)["span"]), V1: s1}) }() }; return func() any { if sky_asBool(matchKind(TkChar, state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; return SkyOk(SkyTuple2{V0: CharLitExpr(sky_asMap(tok)["lexeme"], sky_asMap(tok)["span"]), V1: s1}) }() }; return func() any { if sky_asBool(matchKindLex(TkKeyword, "True", state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; return SkyOk(SkyTuple2{V0: BoolLitExpr(true, sky_asMap(tok)["span"]), V1: s1}) }() }; return func() any { if sky_asBool(matchKindLex(TkKeyword, "False", state)) { return func() any { __tup_tok_s1 := advance(state); tok := sky_asTuple2(__tup_tok_s1).V0; _ = tok; s1 := sky_asTuple2(__tup_tok_s1).V1; _ = s1; return SkyOk(SkyTuple2{V0: BoolLitExpr(false, sky_asMap(tok)["span"]), V1: s1}) }() }; return SkyErr }() }() }() }() }() }() }() }() }() }() }() }() }() }() }() }()
}

func Compiler_ParserExpr_ParseCaseExpr(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s1); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { subject := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = subject; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := consumeLex(TkKeyword, "of", s2); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { __tup_branches_s4 := Compiler_ParserExpr_ParseCaseBranches(s3); branches := sky_asTuple2(__tup_branches_s4).V0; _ = branches; s4 := sky_asTuple2(__tup_branches_s4).V1; _ = s4; return SkyOk(SkyTuple2{V0: CaseExpr(subject, branches, emptySpan), V1: s4}) }() };  return nil }() }() };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseCaseBranches(state any) any {
	return func() any { if sky_asBool(matchKind(TkEOF, state)) { return SkyTuple2{V0: []any{}, V1: state} }; return func() any { if sky_asBool(sky_asInt(peekColumn(state)) <= sky_asInt(1)) { return SkyTuple2{V0: []any{}, V1: state} }; return func() any { s0 := func() any { if sky_asBool(matchKind(TkPipe, state)) { return func() any { __tup_w_s := advance(state); s := sky_asTuple2(__tup_w_s).V1; _ = s; return s }() }; return state }(); _ = s0; return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(s0); if <nil> { return SkyTuple2{V0: []any{}, V1: state} };  if <nil> { pat := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = pat; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { return func() any { __subject := consume(TkArrow, s1); if <nil> { return SkyTuple2{V0: []any{}, V1: state} };  if <nil> { s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s2); if <nil> { return SkyTuple2{V0: []any{}, V1: state} };  if <nil> { body := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = body; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { __tup_rest_s4 := Compiler_ParserExpr_ParseCaseBranches(s3); rest := sky_asTuple2(__tup_rest_s4).V0; _ = rest; s4 := sky_asTuple2(__tup_rest_s4).V1; _ = s4; return SkyTuple2{V0: append([]any{map[string]any{"pattern": pat, "body": body}}, sky_asList(rest)...), V1: s4} }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }() }() }()
}

func Compiler_ParserExpr_ParseIfExpr(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s1); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { cond := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = cond; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := consumeLex(TkKeyword, "then", s2); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s3); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { thenExpr := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = thenExpr; s4 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s4; return func() any { return func() any { __subject := consumeLex(TkKeyword, "else", s4); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s5 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s5; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s5); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { elseExpr := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = elseExpr; s6 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s6; return SkyOk(SkyTuple2{V0: IfExpr(cond, thenExpr, elseExpr, emptySpan), V1: s6}) };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseLetExpr(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; __tup_bindings_s2 := Compiler_ParserExpr_ParseLetBindings(s1); bindings := sky_asTuple2(__tup_bindings_s2).V0; _ = bindings; s2 := sky_asTuple2(__tup_bindings_s2).V1; _ = s2; return func() any { return func() any { __subject := consumeLex(TkKeyword, "in", s2); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s3); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { body := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = body; s4 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s4; return SkyOk(SkyTuple2{V0: LetExpr(bindings, body, emptySpan), V1: s4}) };  return nil }() }() };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseLetBindings(state any) any {
	return func() any { if sky_asBool(sky_asBool(matchKindLex(TkKeyword, "in", state)) || sky_asBool(matchKind(TkEOF, state))) { return SkyTuple2{V0: []any{}, V1: state} }; return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(state); if <nil> { return SkyTuple2{V0: []any{}, V1: state} };  if <nil> { pat := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = pat; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { return func() any { __subject := consume(TkEquals, s1); if <nil> { return SkyTuple2{V0: []any{}, V1: state} };  if <nil> { s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s2); if <nil> { return SkyTuple2{V0: []any{}, V1: state} };  if <nil> { value := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = value; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { __tup_rest_s4 := Compiler_ParserExpr_ParseLetBindings(s3); rest := sky_asTuple2(__tup_rest_s4).V0; _ = rest; s4 := sky_asTuple2(__tup_rest_s4).V1; _ = s4; return SkyTuple2{V0: append([]any{map[string]any{"pattern": pat, "value": value}}, sky_asList(rest)...), V1: s4} }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseLambdaExpr(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; __tup_params_s2 := Compiler_ParserExpr_ParseLambdaParams(s1); params := sky_asTuple2(__tup_params_s2).V0; _ = params; s2 := sky_asTuple2(__tup_params_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkArrow, s2); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s3); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { body := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = body; s4 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s4; return SkyOk(SkyTuple2{V0: LambdaExpr(params, body, emptySpan), V1: s4}) };  return nil }() }() };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseLambdaParams(state any) any {
	return func() any { if sky_asBool(matchKind(TkArrow, state)) { return SkyTuple2{V0: []any{}, V1: state} }; return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(state); if <nil> { pat := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = pat; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { __tup_rest_s2 := Compiler_ParserExpr_ParseLambdaParams(s1); rest := sky_asTuple2(__tup_rest_s2).V0; _ = rest; s2 := sky_asTuple2(__tup_rest_s2).V1; _ = s2; return SkyTuple2{V0: append([]any{pat}, sky_asList(rest)...), V1: s2} }() };  if <nil> { return SkyTuple2{V0: []any{}, V1: state} };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseRecordOrUpdate(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; t1 := peekAt(1, s1); _ = t1; return func() any { if sky_asBool(sky_asBool(matchKind(TkIdentifier, s1)) && sky_asBool(tokenKindEq(sky_asMap(t1)["kind"], TkPipe))) { return func() any { __tup_base_s2 := advance(s1); base := sky_asTuple2(__tup_base_s2).V0; _ = base; s2 := sky_asTuple2(__tup_base_s2).V1; _ = s2; __tup_w_s3 := advance(s2); s3 := sky_asTuple2(__tup_w_s3).V1; _ = s3; __tup_fields_s4 := Compiler_ParserExpr_ParseRecordFields(s3); fields := sky_asTuple2(__tup_fields_s4).V0; _ = fields; s4 := sky_asTuple2(__tup_fields_s4).V1; _ = s4; return func() any { return func() any { __subject := consume(TkRBrace, s4); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s5 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s5; return SkyOk(SkyTuple2{V0: RecordUpdateExpr(IdentifierExpr(sky_asMap(base)["lexeme"], sky_asMap(base)["span"]), fields, emptySpan), V1: s5}) };  return nil }() }() }() }; return func() any { __tup_fields_s2 := Compiler_ParserExpr_ParseRecordFields(s1); fields := sky_asTuple2(__tup_fields_s2).V0; _ = fields; s2 := sky_asTuple2(__tup_fields_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkRBrace, s2); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk(SkyTuple2{V0: RecordExpr(fields, emptySpan), V1: s3}) };  return nil }() }() }() }() }()
}

func Compiler_ParserExpr_ParseRecordFields(state any) any {
	return func() any { if sky_asBool(matchKind(TkRBrace, state)) { return SkyTuple2{V0: []any{}, V1: state} }; return func() any { return func() any { __subject := consume(TkIdentifier, state); if <nil> { return SkyTuple2{V0: []any{}, V1: state} };  if <nil> { name := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = name; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { return func() any { __subject := consume(TkEquals, s1); if <nil> { return SkyTuple2{V0: []any{}, V1: state} };  if <nil> { s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s2); if <nil> { return SkyTuple2{V0: []any{}, V1: state} };  if <nil> { value := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = value; s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return func() any { s4 := func() any { if sky_asBool(matchKind(TkComma, s3)) { return func() any { __tup_w_s := advance(s3); s := sky_asTuple2(__tup_w_s).V1; _ = s; return s }() }; return s3 }(); _ = s4; __tup_rest_s5 := Compiler_ParserExpr_ParseRecordFields(s4); rest := sky_asTuple2(__tup_rest_s5).V0; _ = rest; s5 := sky_asTuple2(__tup_rest_s5).V1; _ = s5; return SkyTuple2{V0: append([]any{map[string]any{"name": sky_asMap(name)["lexeme"], "value": value}}, sky_asList(rest)...), V1: s5} }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseParenOrTuple(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkRParen, s1)) { return func() any { __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; return SkyOk(SkyTuple2{V0: UnitExpr(emptySpan), V1: s2}) }() }; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s1); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { first := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = first; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return func() any { if sky_asBool(matchKind(TkComma, s2)) { return Compiler_ParserExpr_ParseTupleRest([]any{first}, s2) }; return func() any { return func() any { __subject := consume(TkRParen, s2); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk(SkyTuple2{V0: ParenExpr(first, emptySpan), V1: s3}) };  return nil }() }() }() };  return nil }() }() }() }()
}

func Compiler_ParserExpr_ParseTupleRest(items any, state any) any {
	return func() any { if sky_asBool(matchKind(TkComma, state)) { return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, s1); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { item := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = item; s2 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s2; return Compiler_ParserExpr_ParseTupleRest(sky_concat(items, []any{item}), s2) };  return nil }() }() }() }; return func() any { return func() any { __subject := consume(TkRParen, state); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return SkyOk(SkyTuple2{V0: TupleExpr(items, emptySpan), V1: s1}) };  return nil }() }() }()
}

func Compiler_ParserExpr_ParseListExpr(state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkRBracket, s1)) { return func() any { __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; return SkyOk(SkyTuple2{V0: ListExpr([]any{}, emptySpan), V1: s2}) }() }; return func() any { __tup_items_s2 := Compiler_ParserExpr_ParseListItems(s1); items := sky_asTuple2(__tup_items_s2).V0; _ = items; s2 := sky_asTuple2(__tup_items_s2).V1; _ = s2; return func() any { return func() any { __subject := consume(TkRBracket, s2); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s3 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s3; return SkyOk(SkyTuple2{V0: ListExpr(items, emptySpan), V1: s3}) };  return nil }() }() }() }() }()
}

func Compiler_ParserExpr_ParseListItems(state any) any {
	return func() any { return func() any { __subject := Compiler_ParserExpr_ParseExpr(0, state); if <nil> { return SkyTuple2{V0: []any{}, V1: state} };  if <nil> { item := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = item; s1 := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = s1; return func() any { if sky_asBool(matchKind(TkComma, s1)) { return func() any { __tup_w_s2 := advance(s1); s2 := sky_asTuple2(__tup_w_s2).V1; _ = s2; __tup_rest_s3 := Compiler_ParserExpr_ParseListItems(s2); rest := sky_asTuple2(__tup_rest_s3).V0; _ = rest; s3 := sky_asTuple2(__tup_rest_s3).V1; _ = s3; return SkyTuple2{V0: append([]any{item}, sky_asList(rest)...), V1: s3} }() }; return SkyTuple2{V0: []any{item}, V1: s1} }() };  return nil }() }()
}

func Compiler_ParserExpr_ParseQualifiedOrConstructor(state any) any {
	return func() any { __tup_id_s1 := advance(state); id := sky_asTuple2(__tup_id_s1).V0; _ = id; s1 := sky_asTuple2(__tup_id_s1).V1; _ = s1; __tup_parts_s2 := parseQualifiedParts([]any{sky_asMap(id)["lexeme"]}, s1); parts := sky_asTuple2(__tup_parts_s2).V0; _ = parts; s2 := sky_asTuple2(__tup_parts_s2).V1; _ = s2; return func() any { if sky_asBool(sky_asInt(sky_listLength(parts)) > sky_asInt(1)) { return SkyOk(SkyTuple2{V0: QualifiedExpr(parts, emptySpan), V1: s2}) }; return SkyOk(SkyTuple2{V0: IdentifierExpr(sky_asMap(id)["lexeme"], sky_asMap(id)["span"]), V1: s2}) }() }()
}

func Compiler_ParserExpr_ParseFieldAccess(base any, state any) any {
	return func() any { __tup_w_s1 := advance(state); s1 := sky_asTuple2(__tup_w_s1).V1; _ = s1; __tup_field_s2 := advance(s1); field := sky_asTuple2(__tup_field_s2).V0; _ = field; s2 := sky_asTuple2(__tup_field_s2).V1; _ = s2; return SkyOk(SkyTuple2{V0: FieldAccessExpr(IdentifierExpr(sky_asMap(base)["lexeme"], sky_asMap(base)["span"]), sky_asMap(field)["lexeme"], emptySpan), V1: s2}) }()
}

var parsePatternExpr = Compiler_ParserPattern_ParsePatternExpr

var parsePrimaryPattern = Compiler_ParserPattern_ParsePrimaryPattern

var ParsePrimaryPatternUpper = Compiler_ParserPattern_ParsePrimaryPatternUpper

var ParsePrimaryPatternIdent = Compiler_ParserPattern_ParsePrimaryPatternIdent

var ParsePrimaryPatternInt = Compiler_ParserPattern_ParsePrimaryPatternInt

var ParsePrimaryPatternString = Compiler_ParserPattern_ParsePrimaryPatternString

var ParsePrimaryPatternParen = Compiler_ParserPattern_ParsePrimaryPatternParen

var ParsePrimaryPatternParenCont = Compiler_ParserPattern_ParsePrimaryPatternParenCont

var ParsePrimaryPatternBracket = Compiler_ParserPattern_ParsePrimaryPatternBracket

var parsePatternArgs = Compiler_ParserPattern_ParsePatternArgs

var parseTuplePatternRest = Compiler_ParserPattern_ParseTuplePatternRest

var parsePatternList = Compiler_ParserPattern_ParsePatternList

func Compiler_ParserPattern_ParsePatternExpr(state any) any {
	return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePrimaryPattern(state); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return func() any { pat := sky_fst(pair); _ = pat; s1 := sky_snd(pair); _ = s1; return func() any { if sky_asBool(sky_asBool(matchKind(TkOperator, s1)) && sky_asBool(sky_equal(peekLexeme(s1), "::"))) { return func() any { advResult := advance(s1); _ = advResult; s2 := sky_snd(advResult); _ = s2; return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(s2); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { pair2 := sky_asSkyResult(__subject).OkValue; _ = pair2; return func() any { tail := sky_fst(pair2); _ = tail; s3 := sky_snd(pair2); _ = s3; return SkyOk(SkyTuple2{V0: PCons(pat, tail, emptySpan), V1: s3}) }() };  return nil }() }() }() }; return func() any { if sky_asBool(matchKindLex(TkKeyword, "as", s1)) { return func() any { advResult := advance(s1); _ = advResult; s2 := sky_snd(advResult); _ = s2; advResult2 := advance(s2); _ = advResult2; nameTok := sky_fst(advResult2); _ = nameTok; s3 := sky_snd(advResult2); _ = s3; return SkyOk(SkyTuple2{V0: PAs(pat, sky_asMap(nameTok)["lexeme"], emptySpan), V1: s3}) }() }; return SkyOk(SkyTuple2{V0: pat, V1: s1}) }() }() }() };  return nil }() }()
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
	return func() any { advResult := advance(state); _ = advResult; tok := sky_fst(advResult); _ = tok; s1 := sky_snd(advResult); _ = s1; return func() any { return func() any { __subject := sky_stringToInt(sky_asMap(tok)["lexeme"]); if <nil> { n := sky_asSkyMaybe(__subject).JustValue; _ = n; return SkyOk(SkyTuple2{V0: PLiteral(LitInt(n), emptySpan), V1: s1}) };  if <nil> { return SkyOk(SkyTuple2{V0: PLiteral(LitInt(0), emptySpan), V1: s1}) };  return nil }() }() }()
}

func Compiler_ParserPattern_ParsePrimaryPatternString(state any) any {
	return func() any { advResult := advance(state); _ = advResult; tok := sky_fst(advResult); _ = tok; s1 := sky_snd(advResult); _ = s1; return SkyOk(SkyTuple2{V0: PLiteral(LitString(sky_asMap(tok)["lexeme"]), emptySpan), V1: s1}) }()
}

func Compiler_ParserPattern_ParsePrimaryPatternParen(state any) any {
	return func() any { advResult := advance(state); _ = advResult; s1 := sky_snd(advResult); _ = s1; return func() any { if sky_asBool(matchKind(TkRParen, s1)) { return func() any { advResult2 := advance(s1); _ = advResult2; s2 := sky_snd(advResult2); _ = s2; return SkyOk(SkyTuple2{V0: PLiteral(LitString("()"), emptySpan), V1: s2}) }() }; return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(s1); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return Compiler_ParserPattern_ParsePrimaryPatternParenCont(sky_fst(pair), sky_snd(pair)) };  return nil }() }() }() }()
}

func Compiler_ParserPattern_ParsePrimaryPatternParenCont(first any, s2 any) any {
	return func() any { if sky_asBool(matchKind(TkComma, s2)) { return Compiler_ParserPattern_ParseTuplePatternRest([]any{first}, s2) }; return func() any { return func() any { __subject := consume(TkRParen, s2); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return SkyOk(SkyTuple2{V0: first, V1: sky_snd(pair)}) };  return nil }() }() }()
}

func Compiler_ParserPattern_ParsePrimaryPatternBracket(state any) any {
	return func() any { advResult := advance(state); _ = advResult; s1 := sky_snd(advResult); _ = s1; return func() any { if sky_asBool(matchKind(TkRBracket, s1)) { return func() any { advResult2 := advance(s1); _ = advResult2; s2 := sky_snd(advResult2); _ = s2; return SkyOk(SkyTuple2{V0: PList([]any{}, emptySpan), V1: s2}) }() }; return func() any { listResult := Compiler_ParserPattern_ParsePatternList(s1); _ = listResult; items := sky_fst(listResult); _ = items; s2 := sky_snd(listResult); _ = s2; return func() any { return func() any { __subject := consume(TkRBracket, s2); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return SkyOk(SkyTuple2{V0: PList(items, emptySpan), V1: sky_snd(pair)}) };  return nil }() }() }() }() }()
}

func Compiler_ParserPattern_ParsePatternArgs(state any) any {
	return func() any { if sky_asBool(sky_asBool(matchKind(TkIdentifier, state)) || sky_asBool(sky_asBool(matchKind(TkUpperIdentifier, state)) || sky_asBool(matchKind(TkLParen, state)))) { return func() any { if sky_asBool(sky_asBool(matchKind(TkArrow, state)) || sky_asBool(sky_asInt(peekColumn(state)) <= sky_asInt(1))) { return SkyTuple2{V0: []any{}, V1: state} }; return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePrimaryPattern(state); if <nil> { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return func() any { pat := sky_fst(pair); _ = pat; s1 := sky_snd(pair); _ = s1; restResult := Compiler_ParserPattern_ParsePatternArgs(s1); _ = restResult; rest := sky_fst(restResult); _ = rest; s2 := sky_snd(restResult); _ = s2; return SkyTuple2{V0: append([]any{pat}, sky_asList(rest)...), V1: s2} }() };  if <nil> { return SkyTuple2{V0: []any{}, V1: state} };  return nil }() }() }() }; return SkyTuple2{V0: []any{}, V1: state} }()
}

func Compiler_ParserPattern_ParseTuplePatternRest(items any, state any) any {
	return func() any { if sky_asBool(matchKind(TkComma, state)) { return func() any { advResult := advance(state); _ = advResult; s1 := sky_snd(advResult); _ = s1; return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(s1); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return Compiler_ParserPattern_ParseTuplePatternRest(sky_concat(items, []any{sky_fst(pair)}), sky_snd(pair)) };  return nil }() }() }() }; return func() any { return func() any { __subject := consume(TkRParen, state); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return SkyOk(SkyTuple2{V0: PTuple(items, emptySpan), V1: sky_snd(pair)}) };  return nil }() }() }()
}

func Compiler_ParserPattern_ParsePatternList(state any) any {
	return func() any { return func() any { __subject := Compiler_ParserPattern_ParsePatternExpr(state); if <nil> { return SkyTuple2{V0: []any{}, V1: state} };  if <nil> { pair := sky_asSkyResult(__subject).OkValue; _ = pair; return func() any { item := sky_fst(pair); _ = item; s1 := sky_snd(pair); _ = s1; return func() any { if sky_asBool(matchKind(TkComma, s1)) { return func() any { advResult := advance(s1); _ = advResult; s2 := sky_snd(advResult); _ = s2; restResult := Compiler_ParserPattern_ParsePatternList(s2); _ = restResult; rest := sky_fst(restResult); _ = rest; s3 := sky_snd(restResult); _ = s3; return SkyTuple2{V0: append([]any{item}, sky_asList(rest)...), V1: s3} }() }; return SkyTuple2{V0: []any{item}, V1: s1} }() }() };  return nil }() }()
}

var EmitPackage = Compiler_Emit_EmitPackage

var EmitImports = Compiler_Emit_EmitImports

var EmitDecl = Compiler_Emit_EmitDecl

var EmitFuncDecl = Compiler_Emit_EmitFuncDecl

var EmitParam = Compiler_Emit_EmitParam

var EmitStmt = Compiler_Emit_EmitStmt

var EmitExpr = Compiler_Emit_EmitExpr

func Compiler_Emit_EmitPackage(pkg any) any {
	return func() any { header := sky_concat("package ", sky_concat(sky_asMap(pkg)["name"], "\n\n")); _ = header; imports := func() any { if sky_asBool(sky_listIsEmpty(sky_asMap(pkg)["imports"])) { return "" }; return sky_concat(Compiler_Emit_EmitImports(sky_asMap(pkg)["imports"]), "\n") }(); _ = imports; decls := sky_call(sky_stringJoin("\n\n"), sky_call(sky_listMap(Compiler_Emit_EmitDecl), sky_asMap(pkg)["declarations"])); _ = decls; return sky_concat(header, sky_concat(imports, sky_concat(decls, "\n"))) }()
}

func Compiler_Emit_EmitImports(imports any) any {
	return func() any { lines := sky_call(sky_listMap(func(imp any) any { return func() any { if sky_asBool(sky_equal(sky_asMap(imp)["alias_"], "")) { return sky_concat("\t\"", sky_concat(sky_asMap(imp)["path"], "\"")) }; return sky_concat("\t", sky_concat(sky_asMap(imp)["alias_"], sky_concat(" \"", sky_concat(sky_asMap(imp)["path"], "\"")))) }() }), imports); _ = lines; return sky_concat("import (\n", sky_concat(sky_call(sky_stringJoin("\n"), lines), "\n)\n")) }()
}

func Compiler_Emit_EmitDecl(decl any) any {
	return func() any { return func() any { __subject := decl; if <nil> { fd := sky_asMap(__subject)["V0"]; _ = fd; return Compiler_Emit_EmitFuncDecl(fd) };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; expr := sky_asMap(__subject)["V1"]; _ = expr; return sky_concat("var ", sky_concat(name, sky_concat(" = ", Compiler_Emit_EmitExpr(expr)))) };  if <nil> { code := sky_asMap(__subject)["V0"]; _ = code; return code };  return nil }() }()
}

func Compiler_Emit_EmitFuncDecl(fd any) any {
	return func() any { params := sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Emit_EmitParam), sky_asMap(fd)["params"])); _ = params; ret := func() any { if sky_asBool(sky_equal(sky_asMap(fd)["returnType"], "")) { return "" }; return sky_concat(" ", sky_asMap(fd)["returnType"]) }(); _ = ret; body := sky_call(sky_stringJoin("\n"), sky_call(sky_listMap(func(s any) any { return sky_concat("\t", Compiler_Emit_EmitStmt(s)) }), sky_asMap(fd)["body"])); _ = body; return sky_concat("func ", sky_concat(sky_asMap(fd)["name"], sky_concat("(", sky_concat(params, sky_concat(")", sky_concat(ret, sky_concat(" {\n", sky_concat(body, "\n}")))))))) }()
}

func Compiler_Emit_EmitParam(p any) any {
	return sky_concat(sky_asMap(p)["name"], sky_concat(" ", sky_asMap(p)["type_"]))
}

func Compiler_Emit_EmitStmt(stmt any) any {
	return func() any { return func() any { __subject := stmt; if <nil> { expr := sky_asMap(__subject)["V0"]; _ = expr; return Compiler_Emit_EmitExpr(expr) };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; expr := sky_asMap(__subject)["V1"]; _ = expr; return sky_concat(name, sky_concat(" = ", Compiler_Emit_EmitExpr(expr))) };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; expr := sky_asMap(__subject)["V1"]; _ = expr; return sky_concat(name, sky_concat(" := ", Compiler_Emit_EmitExpr(expr))) };  if <nil> { expr := sky_asMap(__subject)["V0"]; _ = expr; return sky_concat("return ", Compiler_Emit_EmitExpr(expr)) };  if <nil> { return "return" };  if <nil> { cond := sky_asMap(__subject)["V0"]; _ = cond; thenStmts := sky_asMap(__subject)["V1"]; _ = thenStmts; elseStmts := sky_asMap(__subject)["V2"]; _ = elseStmts; return func() any { thenBody := sky_call(sky_stringJoin("\n\t"), sky_call(sky_listMap(Compiler_Emit_EmitStmt), thenStmts)); _ = thenBody; elseBody := sky_call(sky_stringJoin("\n\t"), sky_call(sky_listMap(Compiler_Emit_EmitStmt), elseStmts)); _ = elseBody; return func() any { if sky_asBool(sky_listIsEmpty(elseStmts)) { return sky_concat("if ", sky_concat(Compiler_Emit_EmitExpr(cond), sky_concat(" {\n\t", sky_concat(thenBody, "\n}")))) }; return sky_concat("if ", sky_concat(Compiler_Emit_EmitExpr(cond), sky_concat(" {\n\t", sky_concat(thenBody, sky_concat("\n} else {\n\t", sky_concat(elseBody, "\n}")))))) }() }() };  if <nil> { stmts := sky_asMap(__subject)["V0"]; _ = stmts; return sky_call(sky_stringJoin("\n\t"), sky_call(sky_listMap(Compiler_Emit_EmitStmt), stmts)) };  return nil }() }()
}

func Compiler_Emit_EmitExpr(expr any) any {
	return func() any { return func() any { __subject := expr; if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return name };  if <nil> { value := sky_asMap(__subject)["V0"]; _ = value; return value };  if <nil> { value := sky_asMap(__subject)["V0"]; _ = value; return sky_concat("\"", sky_concat(value, "\"")) };  if <nil> { fn := sky_asMap(__subject)["V0"]; _ = fn; args := sky_asMap(__subject)["V1"]; _ = args; return sky_concat(Compiler_Emit_EmitExpr(fn), sky_concat("(", sky_concat(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Emit_EmitExpr), args)), ")"))) };  if <nil> { target := sky_asMap(__subject)["V0"]; _ = target; sel := sky_asMap(__subject)["V1"]; _ = sel; return sky_concat(Compiler_Emit_EmitExpr(target), sky_concat(".", sel)) };  if <nil> { elems := sky_asMap(__subject)["V0"]; _ = elems; return sky_concat("[]any{", sky_concat(sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Emit_EmitExpr), elems)), "}")) };  if <nil> { entries := sky_asMap(__subject)["V0"]; _ = entries; return func() any { pairs := sky_call(sky_listMap(func(pair any) any { return sky_concat(Compiler_Emit_EmitExpr(sky_fst(pair)), sky_concat(": ", Compiler_Emit_EmitExpr(sky_snd(pair)))) }), entries); _ = pairs; return sky_concat("map[string]any{", sky_concat(sky_call(sky_stringJoin(", "), pairs), "}")) }() };  if <nil> { params := sky_asMap(__subject)["V0"]; _ = params; body := sky_asMap(__subject)["V1"]; _ = body; return func() any { ps := sky_call(sky_stringJoin(", "), sky_call(sky_listMap(Compiler_Emit_EmitParam), params)); _ = ps; return sky_concat("func(", sky_concat(ps, sky_concat(") any { return ", sky_concat(Compiler_Emit_EmitExpr(body), " }")))) }() };  if <nil> { code := sky_asMap(__subject)["V0"]; _ = code; return code };  if <nil> { typeName := sky_asMap(__subject)["V0"]; _ = typeName; fields := sky_asMap(__subject)["V1"]; _ = fields; return func() any { fs := sky_call(sky_listMap(func(pair any) any { return sky_concat(sky_fst(pair), sky_concat(": ", Compiler_Emit_EmitExpr(sky_snd(pair)))) }), fields); _ = fs; return sky_concat(typeName, sky_concat("{", sky_concat(sky_call(sky_stringJoin(", "), fs), "}"))) }() };  if <nil> { op := sky_asMap(__subject)["V0"]; _ = op; left := sky_asMap(__subject)["V1"]; _ = left; right := sky_asMap(__subject)["V2"]; _ = right; return sky_concat(Compiler_Emit_EmitExpr(left), sky_concat(" ", sky_concat(op, sky_concat(" ", Compiler_Emit_EmitExpr(right))))) };  if <nil> { op := sky_asMap(__subject)["V0"]; _ = op; operand := sky_asMap(__subject)["V1"]; _ = operand; return sky_concat(op, Compiler_Emit_EmitExpr(operand)) };  if <nil> { target := sky_asMap(__subject)["V0"]; _ = target; index := sky_asMap(__subject)["V1"]; _ = index; return sky_concat(Compiler_Emit_EmitExpr(target), sky_concat("[", sky_concat(Compiler_Emit_EmitExpr(index), "]"))) };  if <nil> { return "nil" };  return nil }() }()
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

var MakeTypedDecl = Compiler_Checker_MakeTypedDecl

var CheckModule = Compiler_Checker_CheckModule

var RegisterTypeAliases = Compiler_Checker_RegisterTypeAliases

var CollectAnnotations = Compiler_Checker_CollectAnnotations

var CollectAnnotationsLoop = Compiler_Checker_CollectAnnotationsLoop

var PreRegisterFunctions = Compiler_Checker_PreRegisterFunctions

var PreRegisterOneFunction = Compiler_Checker_PreRegisterOneFunction

var InferAllDeclarations = Compiler_Checker_InferAllDeclarations

var InferDeclsLoop = Compiler_Checker_InferDeclsLoop

var InferOneDecl = Compiler_Checker_InferOneDecl

var InferOneFunDecl = Compiler_Checker_InferOneFunDecl

var CheckAllExhaustiveness = Compiler_Checker_CheckAllExhaustiveness

var CheckDeclExhaustiveness = Compiler_Checker_CheckDeclExhaustiveness

var CheckExprExhaustiveness = Compiler_Checker_CheckExprExhaustiveness

func Compiler_Checker_MakeTypedDecl(n any, s any, t any) any {
	return map[string]any{"name": n, "scheme": s, "prettyType": formatType(t)}
}

func Compiler_Checker_CheckModule(mod any, imports any) any {
	return func() any { counter := sky_refNew(100); _ = counter; baseEnv := func() any { return func() any { __subject := imports; if <nil> { importedEnv := sky_asSkyMaybe(__subject).JustValue; _ = importedEnv; return Compiler_Env_Union(importedEnv, Compiler_Env_CreatePreludeEnv()) };  if <nil> { return Compiler_Env_CreatePreludeEnv() };  return nil }() }(); _ = baseEnv; aliasEnv := Compiler_Checker_RegisterTypeAliases(sky_asMap(mod)["declarations"], baseEnv); _ = aliasEnv; __tup_registry_adtEnv_adtDiags := Compiler_Adt_RegisterAdts(counter, sky_asMap(mod)["declarations"]); registry := sky_asTuple3(__tup_registry_adtEnv_adtDiags).V0; _ = registry; adtEnv := sky_asTuple3(__tup_registry_adtEnv_adtDiags).V1; _ = adtEnv; adtDiags := sky_asTuple3(__tup_registry_adtEnv_adtDiags).V2; _ = adtDiags; env0 := Compiler_Env_Union(adtEnv, aliasEnv); _ = env0; annotations := Compiler_Checker_CollectAnnotations(sky_asMap(mod)["declarations"]); _ = annotations; env1 := Compiler_Checker_PreRegisterFunctions(counter, sky_asMap(mod)["declarations"], env0); _ = env1; __tup_typedDecls_finalEnv_inferDiags := Compiler_Checker_InferAllDeclarations(counter, Compiler_Checker_Registry, env1, sky_asMap(mod)["declarations"], annotations); typedDecls := sky_asTuple3(__tup_typedDecls_finalEnv_inferDiags).V0; _ = typedDecls; finalEnv := sky_asTuple3(__tup_typedDecls_finalEnv_inferDiags).V1; _ = finalEnv; inferDiags := sky_asTuple3(__tup_typedDecls_finalEnv_inferDiags).V2; _ = inferDiags; exhaustDiags := Compiler_Checker_CheckAllExhaustiveness(Compiler_Checker_Registry, sky_asMap(mod)["declarations"]); _ = exhaustDiags; allDiags := sky_listConcat([]any{adtDiags, inferDiags, exhaustDiags}); _ = allDiags; return SkyOk(map[string]any{"env": finalEnv, "registry": Compiler_Checker_Registry, "declarations": typedDecls, "diagnostics": allDiags}) }()
}

func Compiler_Checker_RegisterTypeAliases(decls any, env any) any {
	return func() any { return func() any { __subject := decls; if <nil> { return Compiler_Checker_Env };  if <nil> { decl := sky_asList(__subject)[0]; _ = decl; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := decl; if <nil> { aliasName := sky_asMap(__subject)["V0"]; _ = aliasName; aliasParams := sky_asMap(__subject)["V1"]; _ = aliasParams; aliasType := sky_asMap(__subject)["V2"]; _ = aliasType; return Compiler_Checker_RegisterTypeAliases(rest, Compiler_Checker_Env) };  if true { return Compiler_Checker_RegisterTypeAliases(rest, Compiler_Checker_Env) };  return nil }() }() };  return nil }() }()
}

func Compiler_Checker_CollectAnnotations(decls any) any {
	return Compiler_Checker_CollectAnnotationsLoop(decls, sky_dictEmpty())
}

func Compiler_Checker_CollectAnnotationsLoop(decls any, acc any) any {
	return func() any { return func() any { __subject := decls; if <nil> { return acc };  if <nil> { decl := sky_asList(__subject)[0]; _ = decl; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := decl; if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; typeExpr := sky_asMap(__subject)["V1"]; _ = typeExpr; return Compiler_Checker_CollectAnnotationsLoop(rest, sky_call2(sky_dictInsert(Compiler_Checker_Name), typeExpr, acc)) };  if true { return Compiler_Checker_CollectAnnotationsLoop(rest, acc) };  return nil }() }() };  return nil }() }()
}

func Compiler_Checker_PreRegisterFunctions(counter any, decls any, env any) any {
	return func() any { return func() any { __subject := decls; if <nil> { return Compiler_Checker_Env };  if <nil> { decl := sky_asList(__subject)[0]; _ = decl; rest := sky_asList(__subject)[1:]; _ = rest; return Compiler_Checker_PreRegisterOneFunction(counter, decl, rest, Compiler_Checker_Env) };  return nil }() }()
}

func Compiler_Checker_PreRegisterOneFunction(counter any, decl any, rest any, env any) any {
	return func() any { return func() any { __subject := decl; if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { tv := freshVar(counter, SkyNothing()); _ = tv; newEnv := Compiler_Env_Extend(Compiler_Checker_Name, mono(tv), Compiler_Checker_Env); _ = newEnv; return Compiler_Checker_PreRegisterFunctions(counter, rest, newEnv) }() };  if true { return Compiler_Checker_PreRegisterFunctions(counter, rest, Compiler_Checker_Env) };  return nil }() }()
}

func Compiler_Checker_InferAllDeclarations(counter any, registry any, env any, decls any, annotations any) any {
	return Compiler_Checker_InferDeclsLoop(counter, Compiler_Checker_Registry, Compiler_Checker_Env, decls, annotations, []any{}, []any{})
}

func Compiler_Checker_InferDeclsLoop(counter any, registry any, env any, decls any, annotations any, typedDecls any, diagnostics any) any {
	return func() any { return func() any { __subject := decls; if <nil> { return SkyTuple3{V0: sky_listReverse(typedDecls), V1: Compiler_Checker_Env, V2: Compiler_Checker_Diagnostics} };  if <nil> { decl := sky_asList(__subject)[0]; _ = decl; rest := sky_asList(__subject)[1:]; _ = rest; return Compiler_Checker_InferOneDecl(counter, Compiler_Checker_Registry, Compiler_Checker_Env, decl, rest, annotations, typedDecls, Compiler_Checker_Diagnostics) };  return nil }() }()
}

func Compiler_Checker_InferOneDecl(counter any, registry any, env any, decl any, rest any, annotations any, typedDecls any, diagnostics any) any {
	return func() any { return func() any { __subject := decl; if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return Compiler_Checker_InferOneFunDecl(counter, Compiler_Checker_Registry, Compiler_Checker_Env, Compiler_Checker_Name, decl, rest, annotations, typedDecls, Compiler_Checker_Diagnostics) };  if true { return Compiler_Checker_InferDeclsLoop(counter, Compiler_Checker_Registry, Compiler_Checker_Env, rest, annotations, typedDecls, Compiler_Checker_Diagnostics) };  return nil }() }()
}

func Compiler_Checker_InferOneFunDecl(counter any, registry any, env any, fnName any, decl any, rest any, annotations any, typedDecls any, diagnostics any) any {
	return func() any { annotation := sky_call(sky_dictGet(fnName), annotations); _ = annotation; return func() any { return func() any { __subject := Compiler_Infer_InferDeclaration(counter, Compiler_Checker_Registry, Compiler_Checker_Env, decl, annotation); if <nil> { inferResult := sky_asSkyResult(__subject).OkValue; _ = inferResult; return func(__pa0 any) any { return func(__pa1 any) any { return Compiler_Checker_InferDeclsLoop(counter, Compiler_Checker_Registry, Compiler_Env_Extend(sky_asMap(inferResult)["name"], sky_asMap(inferResult)["scheme"], Compiler_Checker_Env), rest, annotations, __pa0, __pa1) } } };  return nil }() }() }()
}

func Compiler_Checker_CheckAllExhaustiveness(registry any, decls any) any {
	return sky_call(sky_listConcatMap(func(__pa0 any) any { return Compiler_Checker_CheckDeclExhaustiveness(Compiler_Checker_Registry, __pa0) }), decls)
}

func Compiler_Checker_CheckDeclExhaustiveness(registry any, decl any) any {
	return func() any { return func() any { __subject := decl; if <nil> { body := sky_asMap(__subject)["V2"]; _ = body; return Compiler_Checker_CheckExprExhaustiveness(Compiler_Checker_Registry, body) };  if true { return []any{} };  return nil }() }()
}

func Compiler_Checker_CheckExprExhaustiveness(registry any, expr any) any {
	return func() any { return func() any { __subject := expr; if <nil> { branches := sky_asMap(__subject)["V1"]; _ = branches; return sky_call(sky_listConcatMap(func(b any) any { return Compiler_Checker_CheckExprExhaustiveness(Compiler_Checker_Registry, sky_asMap(b)["body"]) }), branches) };  if <nil> { cond := sky_asMap(__subject)["V0"]; _ = cond; thenB := sky_asMap(__subject)["V1"]; _ = thenB; elseB := sky_asMap(__subject)["V2"]; _ = elseB; return sky_listConcat([]any{Compiler_Checker_CheckExprExhaustiveness(Compiler_Checker_Registry, cond), Compiler_Checker_CheckExprExhaustiveness(Compiler_Checker_Registry, thenB), Compiler_Checker_CheckExprExhaustiveness(Compiler_Checker_Registry, elseB)}) };  if <nil> { bindings := sky_asMap(__subject)["V0"]; _ = bindings; body := sky_asMap(__subject)["V1"]; _ = body; return sky_call(sky_listAppend(sky_call(sky_listConcatMap(func(b any) any { return Compiler_Checker_CheckExprExhaustiveness(Compiler_Checker_Registry, sky_asMap(b)["value"]) }), bindings)), Compiler_Checker_CheckExprExhaustiveness(Compiler_Checker_Registry, body)) };  if <nil> { body := sky_asMap(__subject)["V1"]; _ = body; return Compiler_Checker_CheckExprExhaustiveness(Compiler_Checker_Registry, body) };  if <nil> { callee := sky_asMap(__subject)["V0"]; _ = callee; args := sky_asMap(__subject)["V1"]; _ = args; return sky_call(sky_listAppend(Compiler_Checker_CheckExprExhaustiveness(Compiler_Checker_Registry, callee)), sky_call(sky_listConcatMap(func(__pa0 any) any { return Compiler_Checker_CheckExprExhaustiveness(Compiler_Checker_Registry, __pa0) }), args)) };  if <nil> { left := sky_asMap(__subject)["V1"]; _ = left; right := sky_asMap(__subject)["V2"]; _ = right; return sky_call(sky_listAppend(Compiler_Checker_CheckExprExhaustiveness(Compiler_Checker_Registry, left)), Compiler_Checker_CheckExprExhaustiveness(Compiler_Checker_Registry, right)) };  if <nil> { items := sky_asMap(__subject)["V0"]; _ = items; return sky_call(sky_listConcatMap(func(__pa0 any) any { return Compiler_Checker_CheckExprExhaustiveness(Compiler_Checker_Registry, __pa0) }), items) };  if <nil> { items := sky_asMap(__subject)["V0"]; _ = items; return sky_call(sky_listConcatMap(func(__pa0 any) any { return Compiler_Checker_CheckExprExhaustiveness(Compiler_Checker_Registry, __pa0) }), items) };  if <nil> { inner := sky_asMap(__subject)["V0"]; _ = inner; return Compiler_Checker_CheckExprExhaustiveness(Compiler_Checker_Registry, inner) };  if true { return []any{} };  return nil }() }()
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
	return func() any { return func() any { __subject := expr; if <nil> { return SkyOk(map[string]any{"substitution": emptySub, "type_": TConst("Int")}) };  if <nil> { return SkyOk(map[string]any{"substitution": emptySub, "type_": TConst("Float")}) };  if <nil> { return SkyOk(map[string]any{"substitution": emptySub, "type_": TConst("String")}) };  if <nil> { return SkyOk(map[string]any{"substitution": emptySub, "type_": TConst("Char")}) };  if <nil> { return SkyOk(map[string]any{"substitution": emptySub, "type_": TConst("Bool")}) };  if <nil> { return SkyOk(map[string]any{"substitution": emptySub, "type_": TConst("Unit")}) };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { return func() any { __subject := Compiler_Env_Lookup(Compiler_Infer_Name, env); if <nil> { return SkyErr(sky_concat("Unbound variable: ", Compiler_Infer_Name)) };  if <nil> { scheme := sky_asSkyMaybe(__subject).JustValue; _ = scheme; return func() any { t := instantiate(counter, Compiler_Infer_Scheme); _ = t; return SkyOk(map[string]any{"substitution": emptySub, "type_": t}) }() };  if <nil> { parts := sky_asMap(__subject)["V0"]; _ = parts; return func() any { qualName := sky_call(sky_stringJoin("."), parts); _ = qualName; return func() any { return func() any { __subject := Compiler_Env_Lookup(qualName, env); if <nil> { scheme := sky_asSkyMaybe(__subject).JustValue; _ = scheme; return SkyOk(map[string]any{"substitution": emptySub, "type_": instantiate(counter, Compiler_Infer_Scheme)}) };  if <nil> { return func() any { return func() any { __subject := sky_listReverse(parts); if <nil> { last := sky_asList(__subject)[0]; _ = last; return func() any { return func() any { __subject := Compiler_Env_Lookup(last, env); if <nil> { scheme := sky_asSkyMaybe(__subject).JustValue; _ = scheme; return SkyOk(map[string]any{"substitution": emptySub, "type_": instantiate(counter, Compiler_Infer_Scheme)}) };  if <nil> { return SkyErr(sky_concat("Unbound qualified name: ", qualName)) };  if <nil> { return SkyErr("Empty qualified name") };  if <nil> { items := sky_asMap(__subject)["V0"]; _ = items; return Compiler_Infer_InferTupleItems(counter, registry, env, items, emptySub, []any{}) };  if <nil> { items := sky_asMap(__subject)["V0"]; _ = items; return Compiler_Infer_InferListItems(counter, registry, env, items) };  if <nil> { fields := sky_asMap(__subject)["V0"]; _ = fields; return Compiler_Infer_InferRecordFields(counter, registry, env, fields, emptySub, sky_dictEmpty()) };  if <nil> { base := sky_asMap(__subject)["V0"]; _ = base; fields := sky_asMap(__subject)["V1"]; _ = fields; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, base); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { baseResult := sky_asSkyResult(__subject).OkValue; _ = baseResult; return func() any { return func() any { __subject := Compiler_Infer_InferRecordUpdateFields(counter, registry, env, fields, sky_asMap(baseResult)["substitution"]); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { fieldSub := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = fieldSub; fieldTypes := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = fieldTypes; return func() any { combinedSub := composeSubs(fieldSub, sky_asMap(baseResult)["substitution"]); _ = combinedSub; baseType := applySub(combinedSub, sky_asMap(baseResult)["type_"]); _ = baseType; return func() any { return func() any { __subject := baseType; if <nil> { existingFields := sky_asMap(__subject)["V0"]; _ = existingFields; return func() any { updatedFields := sky_call(sky_dictUnion(fieldTypes), existingFields); _ = updatedFields; return SkyOk(map[string]any{"substitution": combinedSub, "type_": TRecord(updatedFields)}) }() };  if true { return SkyOk(map[string]any{"substitution": combinedSub, "type_": TRecord(fieldTypes)}) };  if <nil> { target := sky_asMap(__subject)["V0"]; _ = target; fieldName := sky_asMap(__subject)["V1"]; _ = fieldName; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, target); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { targetResult := sky_asSkyResult(__subject).OkValue; _ = targetResult; return func() any { resultVar := freshVar(counter, SkyNothing()); _ = resultVar; targetType := applySub(sky_asMap(targetResult)["substitution"], sky_asMap(targetResult)["type_"]); _ = targetType; return func() any { return func() any { __subject := targetType; if <nil> { fields := sky_asMap(__subject)["V0"]; _ = fields; return func() any { return func() any { __subject := sky_call(sky_dictGet(fieldName), fields); if <nil> { fieldType := sky_asSkyMaybe(__subject).JustValue; _ = fieldType; return SkyOk(map[string]any{"substitution": sky_asMap(targetResult)["substitution"], "type_": fieldType}) };  if <nil> { return SkyErr(sky_concat("Record has no field '", sky_concat(fieldName, "'"))) };  if true { return SkyOk(map[string]any{"substitution": sky_asMap(targetResult)["substitution"], "type_": resultVar}) };  if <nil> { callee := sky_asMap(__subject)["V0"]; _ = callee; args := sky_asMap(__subject)["V1"]; _ = args; return Compiler_Infer_InferCall(counter, registry, env, callee, args) };  if <nil> { params := sky_asMap(__subject)["V0"]; _ = params; body := sky_asMap(__subject)["V1"]; _ = body; return Compiler_Infer_InferLambda(counter, registry, env, params, body) };  if <nil> { condition := sky_asMap(__subject)["V0"]; _ = condition; thenBranch := sky_asMap(__subject)["V1"]; _ = thenBranch; elseBranch := sky_asMap(__subject)["V2"]; _ = elseBranch; return Compiler_Infer_InferIf(counter, registry, env, condition, thenBranch, elseBranch) };  if <nil> { bindings := sky_asMap(__subject)["V0"]; _ = bindings; body := sky_asMap(__subject)["V1"]; _ = body; return Compiler_Infer_InferLet(counter, registry, env, bindings, body) };  if <nil> { subject := sky_asMap(__subject)["V0"]; _ = subject; branches := sky_asMap(__subject)["V1"]; _ = branches; return Compiler_Infer_InferCase(counter, registry, env, subject, branches) };  if <nil> { op := sky_asMap(__subject)["V0"]; _ = op; left := sky_asMap(__subject)["V1"]; _ = left; right := sky_asMap(__subject)["V2"]; _ = right; return Compiler_Infer_InferBinary(counter, registry, env, op, left, right) };  if <nil> { inner := sky_asMap(__subject)["V0"]; _ = inner; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, inner); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { result := sky_asSkyResult(__subject).OkValue; _ = result; return func() any { return func() any { __subject := unify(sky_asMap(result)["type_"], TConst("Int")); if <nil> { sub := sky_asSkyResult(__subject).OkValue; _ = sub; return SkyOk(map[string]any{"substitution": composeSubs(sub, sky_asMap(result)["substitution"]), "type_": applySub(sub, sky_asMap(result)["type_"])}) };  if <nil> { return func() any { return func() any { __subject := unify(sky_asMap(result)["type_"], TConst("Float")); if <nil> { sub := sky_asSkyResult(__subject).OkValue; _ = sub; return SkyOk(map[string]any{"substitution": composeSubs(sub, sky_asMap(result)["substitution"]), "type_": applySub(sub, sky_asMap(result)["type_"])}) };  if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Negation requires a number type: ", e)) };  if <nil> { inner := sky_asMap(__subject)["V0"]; _ = inner; return Compiler_Infer_InferExpr(counter, registry, env, inner) };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_InferCall(counter any, registry any, env any, callee any, args any) any {
	return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, callee); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { calleeResult := sky_asSkyResult(__subject).OkValue; _ = calleeResult; return Compiler_Infer_InferCallArgs(counter, registry, env, sky_asMap(calleeResult)["type_"], sky_asMap(calleeResult)["substitution"], args) };  return nil }() }()
}

func Compiler_Infer_InferCallArgs(counter any, registry any, env any, fnType any, sub any, args any) any {
	return func() any { return func() any { __subject := args; if <nil> { return SkyOk(map[string]any{"substitution": sub, "type_": fnType}) };  if <nil> { arg := sky_asList(__subject)[0]; _ = arg; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { resultVar := freshVar(counter, SkyNothing()); _ = resultVar; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, Compiler_Infer_ApplySubToEnv(sub, env), arg); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { argResult := sky_asSkyResult(__subject).OkValue; _ = argResult; return func() any { combinedSub := composeSubs(sky_asMap(argResult)["substitution"], sub); _ = combinedSub; expectedFnType := TFun(sky_asMap(argResult)["type_"], resultVar); _ = expectedFnType; actualFnType := applySub(combinedSub, fnType); _ = actualFnType; return func() any { return func() any { __subject := unify(actualFnType, expectedFnType); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Type error in function call: ", e)) };  if <nil> { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return func() any { finalSub := composeSubs(unifySub, combinedSub); _ = finalSub; resultType := applySub(finalSub, resultVar); _ = resultType; return Compiler_Infer_InferCallArgs(counter, registry, env, resultType, finalSub, rest) }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }()
}

func Compiler_Infer_InferLambda(counter any, registry any, env any, params any, body any) any {
	return Compiler_Infer_InferLambdaParams(counter, registry, env, params, body, []any{})
}

func Compiler_Infer_InferLambdaParams(counter any, registry any, env any, params any, body any, paramTypes any) any {
	return func() any { return func() any { __subject := params; if <nil> { return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, body); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { bodyResult := sky_asSkyResult(__subject).OkValue; _ = bodyResult; return func() any { resultType := sky_call2(sky_listFoldr(func(pt any) any { return func(acc any) any { return TFun(pt, acc) } }), sky_asMap(bodyResult)["type_"], paramTypes); _ = resultType; return SkyOk(map[string]any{"substitution": sky_asMap(bodyResult)["substitution"], "type_": applySub(sky_asMap(bodyResult)["substitution"], resultType)}) }() };  if <nil> { pat := sky_asList(__subject)[0]; _ = pat; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { paramVar := freshVar(counter, SkyNothing()); _ = paramVar; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, pat, paramVar); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { patResult := sky_asSkyResult(__subject).OkValue; _ = patResult; return func() any { newEnv := sky_call2(sky_listFoldl(func(pair any) any { return func(acc any) any { return Compiler_Env_Extend(sky_fst(pair), mono(sky_snd(pair)), acc) } }), env, sky_asMap(patResult)["bindings"]); _ = newEnv; boundParamType := applySub(sky_asMap(patResult)["substitution"], paramVar); _ = boundParamType; return Compiler_Infer_InferLambdaParams(counter, registry, newEnv, rest, body, sky_call(sky_listAppend(paramTypes), []any{boundParamType})) }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_InferIf(counter any, registry any, env any, condition any, thenBranch any, elseBranch any) any {
	return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, condition); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { condResult := sky_asSkyResult(__subject).OkValue; _ = condResult; return func() any { return func() any { __subject := unify(sky_asMap(condResult)["type_"], TConst("Bool")); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Condition must be Bool: ", e)) };  if <nil> { condSub := sky_asSkyResult(__subject).OkValue; _ = condSub; return func() any { sub1 := composeSubs(condSub, sky_asMap(condResult)["substitution"]); _ = sub1; env1 := Compiler_Infer_ApplySubToEnv(sub1, env); _ = env1; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env1, thenBranch); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { thenResult := sky_asSkyResult(__subject).OkValue; _ = thenResult; return func() any { sub2 := composeSubs(sky_asMap(thenResult)["substitution"], sub1); _ = sub2; env2 := Compiler_Infer_ApplySubToEnv(sub2, env); _ = env2; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env2, elseBranch); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { elseResult := sky_asSkyResult(__subject).OkValue; _ = elseResult; return func() any { sub3 := composeSubs(sky_asMap(elseResult)["substitution"], sub2); _ = sub3; return func() any { return func() any { __subject := unify(applySub(sub3, sky_asMap(thenResult)["type_"]), applySub(sub3, sky_asMap(elseResult)["type_"])); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("If branches have different types: ", e)) };  if <nil> { branchSub := sky_asSkyResult(__subject).OkValue; _ = branchSub; return func() any { finalSub := composeSubs(branchSub, sub3); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "type_": applySub(finalSub, sky_asMap(thenResult)["type_"])}) }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_InferLet(counter any, registry any, env any, bindings any, body any) any {
	return Compiler_Infer_InferLetBindings(counter, registry, env, bindings, body)
}

func Compiler_Infer_InferLetBindings(counter any, registry any, env any, bindings any, body any) any {
	return func() any { return func() any { __subject := bindings; if <nil> { return Compiler_Infer_InferExpr(counter, registry, env, body) };  if <nil> { binding := sky_asList(__subject)[0]; _ = binding; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, sky_asMap(binding)["value"]); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { valueResult := sky_asSkyResult(__subject).OkValue; _ = valueResult; return func() any { sub := sky_asMap(valueResult)["substitution"]; _ = sub; envWithSub := Compiler_Infer_ApplySubToEnv(sub, env); _ = envWithSub; generalizedScheme := Compiler_Env_GeneralizeInEnv(envWithSub, applySub(sub, sky_asMap(valueResult)["type_"])); _ = generalizedScheme; return func() any { return func() any { __subject := sky_asMap(binding)["pattern"]; if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { newEnv := Compiler_Env_Extend(Compiler_Infer_Name, generalizedScheme, envWithSub); _ = newEnv; return func() any { return func() any { __subject := Compiler_Infer_InferLetBindings(counter, registry, newEnv, rest, body); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { bodyResult := sky_asSkyResult(__subject).OkValue; _ = bodyResult; return SkyOk(map[string]any{"substitution": composeSubs(sky_asMap(bodyResult)["substitution"], sub), "type_": sky_asMap(bodyResult)["type_"]}) };  if true { return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, sky_asMap(binding)["pattern"], applySub(sub, sky_asMap(valueResult)["type_"])); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { patResult := sky_asSkyResult(__subject).OkValue; _ = patResult; return func() any { combinedSub := composeSubs(sky_asMap(patResult)["substitution"], sub); _ = combinedSub; newEnv := sky_call2(sky_listFoldl(func(pair any) any { return func(acc any) any { return Compiler_Env_Extend(sky_fst(pair), mono(sky_snd(pair)), acc) } }), Compiler_Infer_ApplySubToEnv(combinedSub, env), sky_asMap(patResult)["bindings"]); _ = newEnv; return func() any { return func() any { __subject := Compiler_Infer_InferLetBindings(counter, registry, newEnv, rest, body); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { bodyResult := sky_asSkyResult(__subject).OkValue; _ = bodyResult; return SkyOk(map[string]any{"substitution": composeSubs(sky_asMap(bodyResult)["substitution"], combinedSub), "type_": sky_asMap(bodyResult)["type_"]}) };  return nil }() }() }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_InferCase(counter any, registry any, env any, subject any, branches any) any {
	return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, subject); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { subjectResult := sky_asSkyResult(__subject).OkValue; _ = subjectResult; return func() any { resultVar := freshVar(counter, SkyNothing()); _ = resultVar; return Compiler_Infer_InferCaseBranches(counter, registry, env, sky_asMap(subjectResult)["type_"], sky_asMap(subjectResult)["substitution"], branches, resultVar) }() };  return nil }() }()
}

func Compiler_Infer_InferCaseBranches(counter any, registry any, env any, subjectType any, sub any, branches any, resultType any) any {
	return func() any { return func() any { __subject := branches; if <nil> { return SkyOk(map[string]any{"substitution": sub, "type_": applySub(sub, resultType)}) };  if <nil> { branch := sky_asList(__subject)[0]; _ = branch; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { currentSubjectType := applySub(sub, subjectType); _ = currentSubjectType; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, sky_asMap(branch)["pattern"], currentSubjectType); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { patResult := sky_asSkyResult(__subject).OkValue; _ = patResult; return func() any { patSub := composeSubs(sky_asMap(patResult)["substitution"], sub); _ = patSub; branchEnv := sky_call2(sky_listFoldl(func(pair any) any { return func(acc any) any { return Compiler_Env_Extend(sky_fst(pair), mono(applySub(patSub, sky_snd(pair))), acc) } }), Compiler_Infer_ApplySubToEnv(patSub, env), sky_asMap(patResult)["bindings"]); _ = branchEnv; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, branchEnv, sky_asMap(branch)["body"]); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { bodyResult := sky_asSkyResult(__subject).OkValue; _ = bodyResult; return func() any { bodySub := composeSubs(sky_asMap(bodyResult)["substitution"], patSub); _ = bodySub; return func() any { return func() any { __subject := unify(applySub(bodySub, resultType), applySub(bodySub, sky_asMap(bodyResult)["type_"])); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Case branch type mismatch: ", e)) };  if <nil> { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return func() any { finalSub := composeSubs(unifySub, bodySub); _ = finalSub; return Compiler_Infer_InferCaseBranches(counter, registry, env, subjectType, finalSub, rest, resultType) }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }()
}

func Compiler_Infer_InferBinary(counter any, registry any, env any, op any, left any, right any) any {
	return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env, left); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { leftResult := sky_asSkyResult(__subject).OkValue; _ = leftResult; return func() any { sub1 := sky_asMap(leftResult)["substitution"]; _ = sub1; env1 := Compiler_Infer_ApplySubToEnv(sub1, env); _ = env1; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, env1, right); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { rightResult := sky_asSkyResult(__subject).OkValue; _ = rightResult; return func() any { sub2 := composeSubs(sky_asMap(rightResult)["substitution"], sub1); _ = sub2; lt := applySub(sub2, sky_asMap(leftResult)["type_"]); _ = lt; rt := applySub(sub2, sky_asMap(rightResult)["type_"]); _ = rt; return Compiler_Infer_InferBinaryOp(counter, op, lt, rt, sub2) }() };  return nil }() }() }() };  return nil }() }()
}

func Compiler_Infer_InferBinaryOp(counter any, op any, lt any, rt any, sub any) any {
	return func() any { if sky_asBool(sky_asBool(sky_equal(op, "+")) || sky_asBool(sky_asBool(sky_equal(op, "-")) || sky_asBool(sky_asBool(sky_equal(op, "*")) || sky_asBool(sky_equal(op, "%"))))) { return func() any { return func() any { __subject := unify(lt, rt); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Arithmetic operator '", sky_concat(op, sky_concat("': ", e)))) };  if <nil> { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return func() any { finalSub := composeSubs(unifySub, sub); _ = finalSub; resultType := applySub(finalSub, lt); _ = resultType; return SkyOk(map[string]any{"substitution": finalSub, "type_": resultType}) }() };  return nil }() }() }; return func() any { if sky_asBool(sky_equal(op, "/")) { return func() any { return func() any { __subject := unify(lt, TConst("Float")); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Division requires Float: ", e)) };  if <nil> { s1 := sky_asSkyResult(__subject).OkValue; _ = s1; return func() any { return func() any { __subject := unify(rt, TConst("Float")); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Division requires Float: ", e)) };  if <nil> { s2 := sky_asSkyResult(__subject).OkValue; _ = s2; return func() any { finalSub := composeSubs(s2, composeSubs(s1, sub)); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "type_": TConst("Float")}) }() };  return nil }() }() };  return nil }() }() }; return func() any { if sky_asBool(sky_equal(op, "//")) { return func() any { return func() any { __subject := unify(lt, TConst("Int")); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Integer division requires Int: ", e)) };  if <nil> { s1 := sky_asSkyResult(__subject).OkValue; _ = s1; return func() any { return func() any { __subject := unify(rt, TConst("Int")); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Integer division requires Int: ", e)) };  if <nil> { s2 := sky_asSkyResult(__subject).OkValue; _ = s2; return SkyOk(map[string]any{"substitution": composeSubs(s2, composeSubs(s1, sub)), "type_": TConst("Int")}) };  return nil }() }() };  return nil }() }() }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "==")) || sky_asBool(sky_asBool(sky_equal(op, "!=")) || sky_asBool(sky_asBool(sky_equal(op, "/=")) || sky_asBool(sky_asBool(sky_equal(op, "<")) || sky_asBool(sky_asBool(sky_equal(op, "<=")) || sky_asBool(sky_asBool(sky_equal(op, ">")) || sky_asBool(sky_equal(op, ">=")))))))) { return func() any { return func() any { __subject := unify(lt, rt); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Comparison operator '", sky_concat(op, sky_concat("': ", e)))) };  if <nil> { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return SkyOk(map[string]any{"substitution": composeSubs(unifySub, sub), "type_": TConst("Bool")}) };  return nil }() }() }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, "&&")) || sky_asBool(sky_equal(op, "||"))) { return func() any { return func() any { __subject := unify(lt, TConst("Bool")); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Logical operator requires Bool: ", e)) };  if <nil> { s1 := sky_asSkyResult(__subject).OkValue; _ = s1; return func() any { return func() any { __subject := unify(applySub(s1, rt), TConst("Bool")); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Logical operator requires Bool: ", e)) };  if <nil> { s2 := sky_asSkyResult(__subject).OkValue; _ = s2; return SkyOk(map[string]any{"substitution": composeSubs(s2, composeSubs(s1, sub)), "type_": TConst("Bool")}) };  return nil }() }() };  return nil }() }() }; return func() any { if sky_asBool(sky_equal(op, "++")) { return func() any { return func() any { __subject := unify(lt, rt); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Append operator: ", e)) };  if <nil> { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return SkyOk(map[string]any{"substitution": composeSubs(unifySub, sub), "type_": applySub(composeSubs(unifySub, sub), lt)}) };  return nil }() }() }; return func() any { if sky_asBool(sky_equal(op, "::")) { return func() any { listType := TApp(TConst("List"), []any{lt}); _ = listType; return func() any { return func() any { __subject := unify(rt, listType); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Cons operator: ", e)) };  if <nil> { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return SkyOk(map[string]any{"substitution": composeSubs(unifySub, sub), "type_": applySub(composeSubs(unifySub, sub), rt)}) };  return nil }() }() }() }; return func() any { if sky_asBool(sky_equal(op, "|>")) { return func() any { resultVar := freshVar(counter, SkyNothing()); _ = resultVar; expectedFnType := TFun(lt, resultVar); _ = expectedFnType; return func() any { return func() any { __subject := unify(rt, expectedFnType); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Pipeline operator: ", e)) };  if <nil> { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return func() any { finalSub := composeSubs(unifySub, sub); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "type_": applySub(finalSub, resultVar)}) }() };  return nil }() }() }() }; return func() any { if sky_asBool(sky_equal(op, "<|")) { return func() any { resultVar := freshVar(counter, SkyNothing()); _ = resultVar; expectedFnType := TFun(rt, resultVar); _ = expectedFnType; return func() any { return func() any { __subject := unify(lt, expectedFnType); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Reverse pipeline operator: ", e)) };  if <nil> { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return func() any { finalSub := composeSubs(unifySub, sub); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "type_": applySub(finalSub, resultVar)}) }() };  return nil }() }() }() }; return func() any { if sky_asBool(sky_asBool(sky_equal(op, ">>")) || sky_asBool(sky_equal(op, "<<"))) { return func() any { aVar := freshVar(counter, SkyJust("a")); _ = aVar; bVar := freshVar(counter, SkyJust("b")); _ = bVar; cVar := freshVar(counter, SkyJust("c")); _ = cVar; return func() any { if sky_asBool(sky_equal(op, ">>")) { return func() any { return func() any { __subject := unify(lt, TFun(aVar, bVar)); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Composition: ", e)) };  if <nil> { s1 := sky_asSkyResult(__subject).OkValue; _ = s1; return func() any { return func() any { __subject := unify(applySub(s1, rt), TFun(applySub(s1, bVar), cVar)); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Composition: ", e)) };  if <nil> { s2 := sky_asSkyResult(__subject).OkValue; _ = s2; return func() any { finalSub := composeSubs(s2, composeSubs(s1, sub)); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "type_": TFun(applySub(finalSub, aVar), applySub(finalSub, cVar))}) }() };  return nil }() }() };  return nil }() }() }; return func() any { return func() any { __subject := unify(rt, TFun(aVar, bVar)); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Composition: ", e)) };  if <nil> { s1 := sky_asSkyResult(__subject).OkValue; _ = s1; return func() any { return func() any { __subject := unify(applySub(s1, lt), TFun(applySub(s1, bVar), cVar)); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Composition: ", e)) };  if <nil> { s2 := sky_asSkyResult(__subject).OkValue; _ = s2; return func() any { finalSub := composeSubs(s2, composeSubs(s1, sub)); _ = finalSub; return SkyOk(map[string]any{"substitution": finalSub, "type_": TFun(applySub(finalSub, aVar), applySub(finalSub, cVar))}) }() };  return nil }() }() };  return nil }() }() }() }() }; return SkyErr(sky_concat("Unknown operator: ", op)) }() }() }() }() }() }() }() }() }() }()
}

func Compiler_Infer_InferDeclaration(counter any, registry any, env any, decl any, annotation any) any {
	return func() any { return func() any { __subject := decl; if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; params := sky_asMap(__subject)["V1"]; _ = params; body := sky_asMap(__subject)["V2"]; _ = body; return Compiler_Infer_InferFunction(counter, registry, env, Compiler_Infer_Name, params, body, annotation) };  if true { return SkyErr("inferDeclaration: not a function declaration") };  return nil }() }()
}

func Compiler_Infer_InferFunction(counter any, registry any, env any, name any, params any, body any, annotation any) any {
	return func() any { paramVars := sky_call(sky_listMap(func(p any) any { return freshVar(counter, SkyNothing()) }), params); _ = paramVars; bindResult := Compiler_Infer_BindParams(counter, registry, env, params, paramVars); _ = bindResult; return func() any { return func() any { __subject := bindResult; if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { paramSub := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V0; _ = paramSub; paramEnv := sky_asTuple2(sky_asSkyResult(__subject).OkValue).V1; _ = paramEnv; return func() any { selfVar := freshVar(counter, SkyNothing()); _ = selfVar; envWithSelf := Compiler_Env_Extend(Compiler_Infer_Name, mono(selfVar), paramEnv); _ = envWithSelf; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, envWithSelf, body); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("In function '", sky_concat(Compiler_Infer_Name, sky_concat("': ", e)))) };  if <nil> { bodyResult := sky_asSkyResult(__subject).OkValue; _ = bodyResult; return func() any { bodySub := composeSubs(sky_asMap(bodyResult)["substitution"], paramSub); _ = bodySub; resolvedParamTypes := sky_call(sky_listMap(func(pv any) any { return applySub(bodySub, pv) }), paramVars); _ = resolvedParamTypes; bodyType := applySub(bodySub, sky_asMap(bodyResult)["type_"]); _ = bodyType; funType := sky_call2(sky_listFoldr(func(pt any) any { return func(acc any) any { return TFun(pt, acc) } }), bodyType, resolvedParamTypes); _ = funType; selfType := applySub(bodySub, selfVar); _ = selfType; return func() any { return func() any { __subject := unify(selfType, funType); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Recursive type error in '", sky_concat(Compiler_Infer_Name, sky_concat("': ", e)))) };  if <nil> { selfSub := sky_asSkyResult(__subject).OkValue; _ = selfSub; return func() any { finalSub := composeSubs(selfSub, bodySub); _ = finalSub; finalType := applySub(finalSub, funType); _ = finalType; diagnostics := Compiler_Infer_CheckAnnotation(counter, env, finalType, annotation); _ = diagnostics; scheme := Compiler_Env_GeneralizeInEnv(env, finalType); _ = scheme; return SkyOk(map[string]any{"name": Compiler_Infer_Name, "scheme": Compiler_Infer_Scheme, "diagnostics": Compiler_Infer_Diagnostics}) }() };  return nil }() }() }() };  return nil }() }() }() };  return nil }() }() }()
}

func Compiler_Infer_BindParams(counter any, registry any, env any, params any, types any) any {
	return Compiler_Infer_BindParamsLoop(counter, registry, env, params, types, emptySub)
}

func Compiler_Infer_BindParamsLoop(counter any, registry any, env any, params any, types any, sub any) any {
	return func() any { return func() any { __subject := params; if <nil> { return SkyOk(SkyTuple2{V0: sub, V1: env}) };  if <nil> { pat := sky_asList(__subject)[0]; _ = pat; restPats := sky_asList(__subject)[1:]; _ = restPats; return func() any { return func() any { __subject := types; if <nil> { return SkyErr("Parameter count mismatch") };  if <nil> { t := sky_asList(__subject)[0]; _ = t; restTypes := sky_asList(__subject)[1:]; _ = restTypes; return func() any { return func() any { __subject := Compiler_PatternCheck_CheckPattern(counter, registry, env, pat, applySub(sub, t)); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { patResult := sky_asSkyResult(__subject).OkValue; _ = patResult; return func() any { combinedSub := composeSubs(sky_asMap(patResult)["substitution"], sub); _ = combinedSub; newEnv := sky_call2(sky_listFoldl(func(pair any) any { return func(acc any) any { return Compiler_Env_Extend(sky_fst(pair), mono(applySub(combinedSub, sky_snd(pair))), acc) } }), env, sky_asMap(patResult)["bindings"]); _ = newEnv; return Compiler_Infer_BindParamsLoop(counter, registry, newEnv, restPats, restTypes, combinedSub) }() };  return nil }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_CheckAnnotation(counter any, env any, inferredType any, annotation any) any {
	return func() any { return func() any { __subject := annotation; if <nil> { return []any{} };  if <nil> { annotExpr := sky_asSkyMaybe(__subject).JustValue; _ = annotExpr; return func() any { annotType := Compiler_Adt_ResolveTypeExpr(sky_dictEmpty(), annotExpr); _ = annotType; return func() any { return func() any { __subject := unify(inferredType, annotType); if <nil> { return []any{} };  if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return []any{sky_concat("Type annotation mismatch: declared ", sky_concat(formatType(annotType), sky_concat(" but inferred ", sky_concat(formatType(inferredType), sky_concat(" (", sky_concat(e, ")"))))))} };  return nil }() }() }() };  return nil }() }()
}

func Compiler_Infer_ApplySubToEnv(sub any, env any) any {
	return sky_call(sky_dictMap(func(kk any) any { return func(scheme any) any { return applySubToScheme(sub, Compiler_Infer_Scheme) } }), env)
}

func Compiler_Infer_InferTupleItems(counter any, registry any, env any, items any, sub any, types any) any {
	return func() any { return func() any { __subject := items; if <nil> { return SkyOk(map[string]any{"substitution": sub, "type_": TTuple(sky_listReverse(types))}) };  if <nil> { item := sky_asList(__subject)[0]; _ = item; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, Compiler_Infer_ApplySubToEnv(sub, env), item); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { result := sky_asSkyResult(__subject).OkValue; _ = result; return Compiler_Infer_InferTupleItems(counter, registry, env, rest, composeSubs(sky_asMap(result)["substitution"], sub), append([]any{sky_asMap(result)["type_"]}, sky_asList(types)...)) };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_InferListItems(counter any, registry any, env any, items any) any {
	return func() any { elemVar := freshVar(counter, SkyJust("elem")); _ = elemVar; return Compiler_Infer_InferListItemsLoop(counter, registry, env, items, emptySub, elemVar) }()
}

func Compiler_Infer_InferListItemsLoop(counter any, registry any, env any, items any, sub any, elemType any) any {
	return func() any { return func() any { __subject := items; if <nil> { return SkyOk(map[string]any{"substitution": sub, "type_": TApp(TConst("List"), []any{applySub(sub, elemType)})}) };  if <nil> { item := sky_asList(__subject)[0]; _ = item; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, Compiler_Infer_ApplySubToEnv(sub, env), item); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { result := sky_asSkyResult(__subject).OkValue; _ = result; return func() any { itemSub := composeSubs(sky_asMap(result)["substitution"], sub); _ = itemSub; return func() any { return func() any { __subject := unify(applySub(itemSub, elemType), sky_asMap(result)["type_"]); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("List element type mismatch: ", e)) };  if <nil> { unifySub := sky_asSkyResult(__subject).OkValue; _ = unifySub; return Compiler_Infer_InferListItemsLoop(counter, registry, env, rest, composeSubs(unifySub, itemSub), elemType) };  return nil }() }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_InferRecordFields(counter any, registry any, env any, fields any, sub any, fieldTypes any) any {
	return func() any { return func() any { __subject := fields; if <nil> { return SkyOk(map[string]any{"substitution": sub, "type_": TRecord(fieldTypes)}) };  if <nil> { field := sky_asList(__subject)[0]; _ = field; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, Compiler_Infer_ApplySubToEnv(sub, env), sky_asMap(field)["value"]); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { result := sky_asSkyResult(__subject).OkValue; _ = result; return func() any { newSub := composeSubs(sky_asMap(result)["substitution"], sub); _ = newSub; return Compiler_Infer_InferRecordFields(counter, registry, env, rest, newSub, sky_call2(sky_dictInsert(sky_asMap(field)["name"]), sky_asMap(result)["type_"], fieldTypes)) }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Infer_InferRecordUpdateFields(counter any, registry any, env any, fields any, sub any) any {
	return Compiler_Infer_InferRecordUpdateFieldsLoop(counter, registry, env, fields, sub, sky_dictEmpty())
}

func Compiler_Infer_InferRecordUpdateFieldsLoop(counter any, registry any, env any, fields any, sub any, fieldTypes any) any {
	return func() any { return func() any { __subject := fields; if <nil> { return SkyOk(SkyTuple2{V0: sub, V1: fieldTypes}) };  if <nil> { field := sky_asList(__subject)[0]; _ = field; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := Compiler_Infer_InferExpr(counter, registry, Compiler_Infer_ApplySubToEnv(sub, env), sky_asMap(field)["value"]); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { result := sky_asSkyResult(__subject).OkValue; _ = result; return Compiler_Infer_InferRecordUpdateFieldsLoop(counter, registry, env, rest, composeSubs(sky_asMap(result)["substitution"], sub), sky_call2(sky_dictInsert(sky_asMap(field)["name"]), sky_asMap(result)["type_"], fieldTypes)) };  return nil }() }() };  return nil }() }()
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
	return func() any { return func() any { __subject := t1; if <nil> { id1 := sky_asMap(__subject)["V0"]; _ = id1; return Compiler_Unify_BindVar(id1, t2) };  if <nil> { nameA := sky_asMap(__subject)["V0"]; _ = nameA; return Compiler_Unify_UnifyConst(nameA, t1, t2) };  if <nil> { fromA := sky_asMap(__subject)["V0"]; _ = fromA; toA := sky_asMap(__subject)["V1"]; _ = toA; return Compiler_Unify_UnifyFun(fromA, toA, t2) };  if <nil> { ctorA := sky_asMap(__subject)["V0"]; _ = ctorA; argsA := sky_asMap(__subject)["V1"]; _ = argsA; return Compiler_Unify_UnifyApp(ctorA, argsA, t2) };  if <nil> { itemsA := sky_asMap(__subject)["V0"]; _ = itemsA; return Compiler_Unify_UnifyTuple(itemsA, t2) };  if <nil> { fieldsA := sky_asMap(__subject)["V0"]; _ = fieldsA; return Compiler_Unify_UnifyRecord(fieldsA, t2) };  return nil }() }()
}

func Compiler_Unify_UnifyConst(nameA any, t1 any, t2 any) any {
	return func() any { return func() any { __subject := t2; if <nil> { id2 := sky_asMap(__subject)["V0"]; _ = id2; return Compiler_Unify_BindVar(id2, t1) };  if <nil> { nameB := sky_asMap(__subject)["V0"]; _ = nameB; return func() any { if sky_asBool(sky_equal(nameA, nameB)) { return SkyOk(emptySub) }; return func() any { if sky_asBool(sky_asBool(Compiler_Unify_IsUniversalUnifier(nameA)) || sky_asBool(Compiler_Unify_IsUniversalUnifier(nameB))) { return SkyOk(emptySub) }; return func() any { if sky_asBool(Compiler_Unify_IsNumericCoercion(nameA, nameB)) { return SkyOk(emptySub) }; return SkyErr(sky_concat("Type mismatch: ", sky_concat(nameA, sky_concat(" vs ", nameB)))) }() }() }() };  if true { return func() any { if sky_asBool(Compiler_Unify_IsUniversalUnifier(nameA)) { return SkyOk(emptySub) }; return SkyErr(sky_concat("Cannot unify ", sky_concat(formatType(t1), sky_concat(" with ", formatType(t2))))) }() };  return nil }() }()
}

func Compiler_Unify_UnifyFun(fromA any, toA any, t2 any) any {
	return func() any { return func() any { __subject := t2; if <nil> { id2 := sky_asMap(__subject)["V0"]; _ = id2; return Compiler_Unify_BindVar(id2, TFun(fromA, toA)) };  if <nil> { fromB := sky_asMap(__subject)["V0"]; _ = fromB; toB := sky_asMap(__subject)["V1"]; _ = toB; return func() any { return func() any { __subject := Compiler_Unify_Unify(fromA, fromB); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s1 := sky_asSkyResult(__subject).OkValue; _ = s1; return func() any { return func() any { __subject := Compiler_Unify_Unify(applySub(s1, toA), applySub(s1, toB)); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s2 := sky_asSkyResult(__subject).OkValue; _ = s2; return SkyOk(composeSubs(s2, s1)) };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { if sky_asBool(Compiler_Unify_IsUniversalUnifier(name)) { return SkyOk(emptySub) }; return SkyErr(sky_concat("Cannot unify ", sky_concat(formatType(TFun(fromA, toA)), sky_concat(" with ", formatType(t2))))) }() };  if true { return SkyErr(sky_concat("Cannot unify ", sky_concat(formatType(TFun(fromA, toA)), sky_concat(" with ", formatType(t2))))) };  return nil }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Unify_UnifyApp(ctorA any, argsA any, t2 any) any {
	return func() any { return func() any { __subject := t2; if <nil> { id2 := sky_asMap(__subject)["V0"]; _ = id2; return Compiler_Unify_BindVar(id2, TApp(ctorA, argsA)) };  if <nil> { ctorB := sky_asMap(__subject)["V0"]; _ = ctorB; argsB := sky_asMap(__subject)["V1"]; _ = argsB; return func() any { return func() any { __subject := Compiler_Unify_Unify(ctorA, ctorB); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s0 := sky_asSkyResult(__subject).OkValue; _ = s0; return Compiler_Unify_UnifyList(sky_call(sky_listMap(func(x any) any { return applySub(s0, x) }), argsA), sky_call(sky_listMap(func(x any) any { return applySub(s0, x) }), argsB), s0) };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { if sky_asBool(Compiler_Unify_IsUniversalUnifier(name)) { return SkyOk(emptySub) }; return SkyErr(sky_concat("Cannot unify ", sky_concat(formatType(TApp(ctorA, argsA)), sky_concat(" with ", formatType(t2))))) }() };  if true { return SkyErr(sky_concat("Cannot unify ", sky_concat(formatType(TApp(ctorA, argsA)), sky_concat(" with ", formatType(t2))))) };  return nil }() }() };  return nil }() }()
}

func Compiler_Unify_UnifyTuple(itemsA any, t2 any) any {
	return func() any { return func() any { __subject := t2; if <nil> { id2 := sky_asMap(__subject)["V0"]; _ = id2; return Compiler_Unify_BindVar(id2, TTuple(itemsA)) };  if <nil> { itemsB := sky_asMap(__subject)["V0"]; _ = itemsB; return func() any { if sky_asBool(!sky_equal(sky_listLength(itemsA), sky_listLength(itemsB))) { return SkyErr(sky_concat("Tuple arity mismatch: ", sky_concat(sky_stringFromInt(sky_listLength(itemsA)), sky_concat(" vs ", sky_stringFromInt(sky_listLength(itemsB)))))) }; return Compiler_Unify_UnifyList(itemsA, itemsB, emptySub) }() };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { if sky_asBool(Compiler_Unify_IsUniversalUnifier(name)) { return SkyOk(emptySub) }; return SkyErr(sky_concat("Cannot unify ", sky_concat(formatType(TTuple(itemsA)), sky_concat(" with ", formatType(t2))))) }() };  if true { return SkyErr(sky_concat("Cannot unify ", sky_concat(formatType(TTuple(itemsA)), sky_concat(" with ", formatType(t2))))) };  return nil }() }()
}

func Compiler_Unify_UnifyRecord(fieldsA any, t2 any) any {
	return func() any { return func() any { __subject := t2; if <nil> { id2 := sky_asMap(__subject)["V0"]; _ = id2; return Compiler_Unify_BindVar(id2, TRecord(fieldsA)) };  if <nil> { fieldsB := sky_asMap(__subject)["V0"]; _ = fieldsB; return Compiler_Unify_UnifyRecords(fieldsA, fieldsB) };  if <nil> { name := sky_asMap(__subject)["V0"]; _ = name; return func() any { if sky_asBool(Compiler_Unify_IsUniversalUnifier(name)) { return SkyOk(emptySub) }; return SkyErr(sky_concat("Cannot unify ", sky_concat(formatType(TRecord(fieldsA)), sky_concat(" with ", formatType(t2))))) }() };  if true { return SkyErr(sky_concat("Cannot unify ", sky_concat(formatType(TRecord(fieldsA)), sky_concat(" with ", formatType(t2))))) };  return nil }() }()
}

func Compiler_Unify_BindVar(id any, t any) any {
	return func() any { return func() any { __subject := t; if <nil> { otherId := sky_asMap(__subject)["V0"]; _ = otherId; return func() any { if sky_asBool(sky_equal(id, otherId)) { return SkyOk(emptySub) }; return SkyOk(sky_call2(sky_dictInsert(id), t, sky_dictEmpty())) }() };  if true { return func() any { if sky_asBool(sky_call(sky_setMember(id), freeVars(t))) { return SkyErr(sky_concat("Infinite type: t", sky_concat(sky_stringFromInt(id), sky_concat(" occurs in ", formatType(t))))) }; return SkyOk(sky_call2(sky_dictInsert(id), t, sky_dictEmpty())) }() };  return nil }() }()
}

func Compiler_Unify_IsUniversalUnifier(name any) any {
	return sky_asBool(sky_equal(name, "JsValue")) || sky_asBool(sky_asBool(sky_equal(name, "Foreign")) || sky_asBool(sky_equal(name, "Any")))
}

func Compiler_Unify_IsNumericCoercion(nameA any, nameB any) any {
	return sky_asBool(sky_asBool(sky_equal(nameA, "Int")) && sky_asBool(sky_equal(nameB, "Float"))) || sky_asBool(sky_asBool(sky_equal(nameA, "Float")) && sky_asBool(sky_equal(nameB, "Int")))
}

func Compiler_Unify_UnifyList(ts1 any, ts2 any, sub any) any {
	return func() any { return func() any { __subject := ts1; if <nil> { return func() any { return func() any { __subject := ts2; if <nil> { return SkyOk(sub) };  if true { return SkyErr("Type argument count mismatch") };  if <nil> { t1head := sky_asList(__subject)[0]; _ = t1head; rest1 := sky_asList(__subject)[1:]; _ = rest1; return func() any { return func() any { __subject := ts2; if <nil> { return SkyErr("Type argument count mismatch") };  if <nil> { t2head := sky_asList(__subject)[0]; _ = t2head; rest2 := sky_asList(__subject)[1:]; _ = rest2; return func() any { return func() any { __subject := Compiler_Unify_Unify(applySub(sub, t1head), applySub(sub, t2head)); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if <nil> { s := sky_asSkyResult(__subject).OkValue; _ = s; return Compiler_Unify_UnifyList(rest1, rest2, composeSubs(s, sub)) };  return nil }() }() };  return nil }() }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Unify_UnifyRecords(fieldsA any, fieldsB any) any {
	return func() any { allKeys := sky_setToList(sky_call(sky_setUnion(sky_setFromList(sky_dictKeys(fieldsA))), sky_setFromList(sky_dictKeys(fieldsB)))); _ = allKeys; return Compiler_Unify_UnifyRecordFields(allKeys, fieldsA, fieldsB, emptySub) }()
}

func Compiler_Unify_UnifyRecordFields(keys any, fieldsA any, fieldsB any, sub any) any {
	return func() any { return func() any { __subject := keys; if <nil> { return SkyOk(sub) };  if <nil> { key := sky_asList(__subject)[0]; _ = key; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { valA := sky_call(sky_dictGet(key), fieldsA); _ = valA; valB := sky_call(sky_dictGet(key), fieldsB); _ = valB; return func() any { return func() any { __subject := valA; if <nil> { typeA := sky_asSkyMaybe(__subject).JustValue; _ = typeA; return func() any { return func() any { __subject := valB; if <nil> { typeB := sky_asSkyMaybe(__subject).JustValue; _ = typeB; return func() any { return func() any { __subject := Compiler_Unify_Unify(applySub(sub, typeA), applySub(sub, typeB)); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("In record field '", sky_concat(key, sky_concat("': ", e)))) };  if <nil> { s := sky_asSkyResult(__subject).OkValue; _ = s; return Compiler_Unify_UnifyRecordFields(rest, fieldsA, fieldsB, composeSubs(s, sub)) };  if <nil> { return Compiler_Unify_UnifyRecordFields(rest, fieldsA, fieldsB, sub) };  if <nil> { return Compiler_Unify_UnifyRecordFields(rest, fieldsA, fieldsB, sub) };  return nil }() }() };  return nil }() }() };  return nil }() }() }() };  return nil }() }()
}

var ResolveProject = Compiler_Resolver_ResolveProject

var ResolveImports = Compiler_Resolver_ResolveImports

var ResolveModulePath = Compiler_Resolver_ResolveModulePath

var IsStdlib = Compiler_Resolver_IsStdlib

var CheckAllModules = Compiler_Resolver_CheckAllModules

var CheckModulesLoop = Compiler_Resolver_CheckModulesLoop

var BuildStdlibEnv = Compiler_Resolver_BuildStdlibEnv

func Compiler_Resolver_ResolveProject(entryPath any, srcRoot any) any {
	return func() any { return func() any { __subject := sky_fileRead(entryPath); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Cannot read entry file: ", sky_concat(entryPath, sky_concat(" (", sky_concat(e, ")"))))) };  if <nil> { source := sky_asSkyResult(__subject).OkValue; _ = source; return func() any { lexResult := Compiler_Lexer_Lex(source); _ = lexResult; return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_concat("Parse error in ", sky_concat(entryPath, sky_concat(": ", e)))) };  if <nil> { entryMod := sky_asSkyResult(__subject).OkValue; _ = entryMod; return func() any { entryName := sky_call(sky_stringJoin("."), sky_asMap(entryMod)["name"]); _ = entryName; entryLoaded := map[string]any{"name": entryName, "qualifiedName": sky_asMap(entryMod)["name"], "filePath": entryPath, "ast": entryMod, "checkResult": SkyNothing()}; _ = entryLoaded; __tup_allModules_diags := Compiler_Resolver_ResolveImports(srcRoot, sky_asMap(entryMod)["imports"], []any{entryLoaded}, sky_setEmpty(), sky_listReverse([]any{})); allModules := sky_asTuple2(__tup_allModules_diags).V0; _ = allModules; diags := sky_asTuple2(__tup_allModules_diags).V1; _ = diags; order := sky_call(sky_listMap(func(m any) any { return sky_asMap(m)["name"] }), sky_listReverse(allModules)); _ = order; return SkyOk(map[string]any{"modules": sky_listReverse(allModules), "order": Compiler_Resolver_Order, "diagnostics": diags}) }() };  return nil }() }() }() };  return nil }() }()
}

func Compiler_Resolver_ResolveImports(srcRoot any, imports any, loaded any, visited any, diagnostics any) any {
	return func() any { return func() any { __subject := imports; if <nil> { return SkyTuple2{V0: loaded, V1: Compiler_Resolver_Diagnostics} };  if <nil> { imp := sky_asList(__subject)[0]; _ = imp; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { modName := sky_call(sky_stringJoin("."), sky_asMap(imp)["moduleName"]); _ = modName; return func() any { if sky_asBool(sky_asBool(sky_call(sky_setMember(modName), visited)) || sky_asBool(Compiler_Resolver_IsStdlib(modName))) { return Compiler_Resolver_ResolveImports(srcRoot, rest, loaded, visited, Compiler_Resolver_Diagnostics) }; return func() any { filePath := Compiler_Resolver_ResolveModulePath(srcRoot, sky_asMap(imp)["moduleName"]); _ = filePath; return func() any { return func() any { __subject := sky_fileRead(Compiler_Resolver_FilePath); if <nil> { return Compiler_Resolver_ResolveImports(srcRoot, rest, loaded, sky_call(sky_setInsert(modName), visited), sky_concat(Compiler_Resolver_Diagnostics, []any{sky_concat("Module not found: ", sky_concat(modName, sky_concat(" (looked at ", sky_concat(Compiler_Resolver_FilePath, ")"))))})) };  if <nil> { source := sky_asSkyResult(__subject).OkValue; _ = source; return func() any { lexResult := Compiler_Lexer_Lex(source); _ = lexResult; return func() any { return func() any { __subject := Compiler_Parser_Parse(sky_asMap(lexResult)["tokens"]); if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return Compiler_Resolver_ResolveImports(srcRoot, rest, loaded, sky_call(sky_setInsert(modName), visited), sky_concat(Compiler_Resolver_Diagnostics, []any{sky_concat("Parse error in ", sky_concat(modName, sky_concat(": ", e)))})) };  if <nil> { modAst := sky_asSkyResult(__subject).OkValue; _ = modAst; return func() any { newLoaded := map[string]any{"name": modName, "qualifiedName": sky_asMap(imp)["moduleName"], "filePath": Compiler_Resolver_FilePath, "ast": modAst, "checkResult": SkyNothing()}; _ = newLoaded; newVisited := sky_call(sky_setInsert(modName), visited); _ = newVisited; __tup_withDeps_depDiags := Compiler_Resolver_ResolveImports(srcRoot, sky_asMap(modAst)["imports"], append([]any{newLoaded}, sky_asList(loaded)...), newVisited, Compiler_Resolver_Diagnostics); withDeps := sky_asTuple2(__tup_withDeps_depDiags).V0; _ = withDeps; depDiags := sky_asTuple2(__tup_withDeps_depDiags).V1; _ = depDiags; return Compiler_Resolver_ResolveImports(srcRoot, rest, withDeps, newVisited, depDiags) }() };  return nil }() }() }() };  return nil }() }() }() }() }() };  return nil }() }()
}

func Compiler_Resolver_ResolveModulePath(srcRoot any, parts any) any {
	return sky_concat(srcRoot, sky_concat("/", sky_concat(sky_call(sky_stringJoin("/"), parts), ".sky")))
}

func Compiler_Resolver_IsStdlib(modName any) any {
	return sky_asBool(sky_call(sky_stringStartsWith("Sky.Core."), modName)) || sky_asBool(sky_asBool(sky_call(sky_stringStartsWith("Std."), modName)) || sky_asBool(sky_asBool(sky_equal(modName, "Sky.Core.Prelude")) || sky_asBool(sky_equal(modName, "Sky.Interop"))))
}

func Compiler_Resolver_CheckAllModules(graph any) any {
	return func() any { __tup_checkedModules_diagnostics := Compiler_Resolver_CheckModulesLoop(sky_asMap(graph)["modules"], Compiler_Env_Empty(), []any{}); checkedModules := sky_asTuple2(__tup_checkedModules_diagnostics).V0; _ = checkedModules; diagnostics := sky_asTuple2(__tup_checkedModules_diagnostics).V1; _ = diagnostics; return SkyOk(sky_recordUpdate(graph, map[string]any{"modules": checkedModules, "diagnostics": sky_call(sky_listAppend(sky_asMap(graph)["diagnostics"]), Compiler_Resolver_Diagnostics)})) }()
}

func Compiler_Resolver_CheckModulesLoop(modules any, importedEnv any, diagnostics any) any {
	return func() any { return func() any { __subject := Compiler_Resolver_Modules; if <nil> { return SkyTuple2{V0: []any{}, V1: Compiler_Resolver_Diagnostics} };  if <nil> { mod := sky_asList(__subject)[0]; _ = mod; rest := sky_asList(__subject)[1:]; _ = rest; return func() any { return func() any { __subject := Compiler_Checker_CheckModule(sky_asMap(mod)["ast"], SkyJust(importedEnv)); if <nil> { result := sky_asSkyResult(__subject).OkValue; _ = result; return func() any { checkedMod := sky_recordUpdate(mod, map[string]any{"checkResult": SkyJust(result)}); _ = checkedMod; newImportedEnv := Compiler_Env_Union(sky_asMap(result)["env"], importedEnv); _ = newImportedEnv; __tup_restModules_restDiags := Compiler_Resolver_CheckModulesLoop(rest, newImportedEnv, sky_call(sky_listAppend(Compiler_Resolver_Diagnostics), sky_asMap(result)["diagnostics"])); restModules := sky_asTuple2(__tup_restModules_restDiags).V0; _ = restModules; restDiags := sky_asTuple2(__tup_restModules_restDiags).V1; _ = restDiags; return SkyTuple2{V0: append([]any{checkedMod}, sky_asList(restModules)...), V1: restDiags} }() };  if <nil> { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { __tup_restModules_restDiags := Compiler_Resolver_CheckModulesLoop(rest, importedEnv, sky_call(sky_listAppend(Compiler_Resolver_Diagnostics), []any{e})); restModules := sky_asTuple2(__tup_restModules_restDiags).V0; _ = restModules; restDiags := sky_asTuple2(__tup_restModules_restDiags).V1; _ = restDiags; return SkyTuple2{V0: append([]any{mod}, sky_asList(restModules)...), V1: restDiags} }() };  return nil }() }() };  return nil }() }()
}

func Compiler_Resolver_BuildStdlibEnv() any {
	return func() any { prelude := Compiler_Env_CreatePreludeEnv(); _ = prelude; stdFunctions := sky_dictFromList([]any{SkyTuple2{V0: "String.toUpper", V1: mono(TFun(TConst("String"), TConst("String")))}, SkyTuple2{V0: "String.toLower", V1: mono(TFun(TConst("String"), TConst("String")))}, SkyTuple2{V0: "String.length", V1: mono(TFun(TConst("String"), TConst("Int")))}, SkyTuple2{V0: "String.fromInt", V1: mono(TFun(TConst("Int"), TConst("String")))}, SkyTuple2{V0: "String.fromFloat", V1: mono(TFun(TConst("Float"), TConst("String")))}, SkyTuple2{V0: "String.join", V1: mono(TFun(TConst("String"), TFun(TApp(TConst("List"), []any{TConst("String")}), TConst("String"))))}, SkyTuple2{V0: "String.split", V1: mono(TFun(TConst("String"), TFun(TConst("String"), TApp(TConst("List"), []any{TConst("String")}))))}, SkyTuple2{V0: "String.contains", V1: mono(TFun(TConst("String"), TFun(TConst("String"), TConst("Bool"))))}, SkyTuple2{V0: "String.startsWith", V1: mono(TFun(TConst("String"), TFun(TConst("String"), TConst("Bool"))))}, SkyTuple2{V0: "String.trim", V1: mono(TFun(TConst("String"), TConst("String")))}, SkyTuple2{V0: "String.isEmpty", V1: mono(TFun(TConst("String"), TConst("Bool")))}, SkyTuple2{V0: "String.slice", V1: mono(TFun(TConst("Int"), TFun(TConst("Int"), TFun(TConst("String"), TConst("String")))))}, SkyTuple2{V0: "String.append", V1: mono(TFun(TConst("String"), TFun(TConst("String"), TConst("String"))))}, SkyTuple2{V0: "List.map", V1: map[string]any{"quantified": []any{0, 1}, "type_": TFun(TFun(TVar(0, SkyJust("a")), TVar(1, SkyJust("b"))), TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TApp(TConst("List"), []any{TVar(1, SkyJust("b"))})))}}, SkyTuple2{V0: "List.filter", V1: map[string]any{"quantified": []any{0}, "type_": TFun(TFun(TVar(0, SkyJust("a")), TConst("Bool")), TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TApp(TConst("List"), []any{TVar(0, SkyJust("a"))})))}}, SkyTuple2{V0: "List.foldl", V1: map[string]any{"quantified": []any{0, 1}, "type_": TFun(TFun(TVar(0, SkyJust("a")), TFun(TVar(1, SkyJust("b")), TVar(1, SkyJust("b")))), TFun(TVar(1, SkyJust("b")), TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TVar(1, SkyJust("b")))))}}, SkyTuple2{V0: "List.foldr", V1: map[string]any{"quantified": []any{0, 1}, "type_": TFun(TFun(TVar(0, SkyJust("a")), TFun(TVar(1, SkyJust("b")), TVar(1, SkyJust("b")))), TFun(TVar(1, SkyJust("b")), TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TVar(1, SkyJust("b")))))}}, SkyTuple2{V0: "List.head", V1: map[string]any{"quantified": []any{0}, "type_": TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TApp(TConst("Maybe"), []any{TVar(0, SkyJust("a"))}))}}, SkyTuple2{V0: "List.length", V1: map[string]any{"quantified": []any{0}, "type_": TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TConst("Int"))}}, SkyTuple2{V0: "List.reverse", V1: map[string]any{"quantified": []any{0}, "type_": TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}))}}, SkyTuple2{V0: "List.isEmpty", V1: map[string]any{"quantified": []any{0}, "type_": TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TConst("Bool"))}}, SkyTuple2{V0: "List.append", V1: map[string]any{"quantified": []any{0}, "type_": TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TApp(TConst("List"), []any{TVar(0, SkyJust("a"))})))}}, SkyTuple2{V0: "List.concatMap", V1: map[string]any{"quantified": []any{0, 1}, "type_": TFun(TFun(TVar(0, SkyJust("a")), TApp(TConst("List"), []any{TVar(1, SkyJust("b"))})), TFun(TApp(TConst("List"), []any{TVar(0, SkyJust("a"))}), TApp(TConst("List"), []any{TVar(1, SkyJust("b"))})))}}, SkyTuple2{V0: "Dict.empty", V1: map[string]any{"quantified": []any{0, 1}, "type_": TApp(TConst("Dict"), []any{TVar(0, SkyJust("k")), TVar(1, SkyJust("v"))})}}, SkyTuple2{V0: "Dict.insert", V1: map[string]any{"quantified": []any{0, 1}, "type_": TFun(TVar(0, SkyJust("k")), TFun(TVar(1, SkyJust("v")), TFun(TApp(TConst("Dict"), []any{TVar(0, SkyJust("k")), TVar(1, SkyJust("v"))}), TApp(TConst("Dict"), []any{TVar(0, SkyJust("k")), TVar(1, SkyJust("v"))}))))}}, SkyTuple2{V0: "Dict.get", V1: map[string]any{"quantified": []any{0, 1}, "type_": TFun(TVar(0, SkyJust("k")), TFun(TApp(TConst("Dict"), []any{TVar(0, SkyJust("k")), TVar(1, SkyJust("v"))}), TApp(TConst("Maybe"), []any{TVar(1, SkyJust("v"))})))}}, SkyTuple2{V0: "Log.println", V1: mono(TFun(TConst("String"), TConst("Unit")))}, SkyTuple2{V0: "println", V1: mono(TFun(TConst("String"), TConst("Unit")))}, SkyTuple2{V0: "Process.exit", V1: mono(TFun(TConst("Int"), TConst("Unit")))}, SkyTuple2{V0: "File.readFile", V1: mono(TFun(TConst("String"), TApp(TConst("Result"), []any{TConst("String"), TConst("String")})))}, SkyTuple2{V0: "File.writeFile", V1: mono(TFun(TConst("String"), TFun(TConst("String"), TApp(TConst("Result"), []any{TConst("String"), TConst("Unit")}))))}, SkyTuple2{V0: "File.mkdirAll", V1: mono(TFun(TConst("String"), TApp(TConst("Result"), []any{TConst("String"), TConst("Unit")})))}, SkyTuple2{V0: "Args.getArgs", V1: mono(TFun(TConst("Unit"), TApp(TConst("List"), []any{TConst("String")})))}, SkyTuple2{V0: "Args.getArg", V1: mono(TFun(TConst("Int"), TApp(TConst("Maybe"), []any{TConst("String")})))}, SkyTuple2{V0: "Ref.new", V1: map[string]any{"quantified": []any{0}, "type_": TFun(TVar(0, SkyJust("a")), TApp(TConst("Ref"), []any{TVar(0, SkyJust("a"))}))}}, SkyTuple2{V0: "Ref.get", V1: map[string]any{"quantified": []any{0}, "type_": TFun(TApp(TConst("Ref"), []any{TVar(0, SkyJust("a"))}), TVar(0, SkyJust("a")))}}, SkyTuple2{V0: "Ref.set", V1: map[string]any{"quantified": []any{0}, "type_": TFun(TVar(0, SkyJust("a")), TFun(TApp(TConst("Ref"), []any{TVar(0, SkyJust("a"))}), TConst("Unit")))}}}); _ = stdFunctions; return Compiler_Env_Union(stdFunctions, prelude) }()
}

func main() {
	func() any { args := sky_processGetArgs(struct{}{}); _ = args; return func() any { return func() any { __subject := sky_processGetArg(1); if <nil> { return sky_println("Usage: sky-compiler <file.sky>") };  if <nil> { filePath := sky_asSkyMaybe(__subject).JustValue; _ = filePath; return func() any { return func() any { __subject := Compiler_Pipeline_Compile(filePath, "sky-out"); if <nil> { return sky_println("Compilation successful.") };  if <nil> { msg := sky_asSkyResult(__subject).ErrValue; _ = msg; return func() any { sky_println(sky_concat("Error: ", msg)); return sky_processExit(1) }() };  return nil }() }() };  return nil }() }() }()
}
