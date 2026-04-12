package rt

import (
	"bufio"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	mrand "math/rand"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// ═══════════════════════════════════════════════════════════
// Result
// ═══════════════════════════════════════════════════════════

type SkyResult[E any, A any] struct {
	Tag      int
	OkValue  A
	ErrValue E
}

func Ok[E any, A any](v A) SkyResult[E, A] {
	return SkyResult[E, A]{Tag: 0, OkValue: v}
}

func Err[E any, A any](e E) SkyResult[E, A] {
	return SkyResult[E, A]{Tag: 1, ErrValue: e}
}

// ═══════════════════════════════════════════════════════════
// Maybe
// ═══════════════════════════════════════════════════════════

type SkyMaybe[A any] struct {
	Tag       int
	JustValue A
}

func Just[A any](v A) SkyMaybe[A] {
	return SkyMaybe[A]{Tag: 0, JustValue: v}
}

func Nothing[A any]() SkyMaybe[A] {
	return SkyMaybe[A]{Tag: 1}
}

// ═══════════════════════════════════════════════════════════
// Task
// ═══════════════════════════════════════════════════════════

type SkyTask[E any, A any] func() SkyResult[E, A]

func Task_succeed[E any, A any](v A) SkyTask[E, A] {
	return func() SkyResult[E, A] { return Ok[E, A](v) }
}

func Task_fail[E any, A any](e E) SkyTask[E, A] {
	return func() SkyResult[E, A] { return Err[E, A](e) }
}

func Task_andThen[E any, A any, B any](fn func(A) SkyTask[E, B], task SkyTask[E, A]) SkyTask[E, B] {
	return func() SkyResult[E, B] {
		r := task()
		if r.Tag == 0 {
			return fn(r.OkValue)()
		}
		return Err[E, B](r.ErrValue)
	}
}

func Task_run[E any, A any](task SkyTask[E, A]) SkyResult[E, A] {
	return task()
}

func RunMainTask[E any, A any](task SkyTask[E, A]) {
	r := task()
	if r.Tag == 1 {
		fmt.Println("Error:", r.ErrValue)
	}
}

// ═══════════════════════════════════════════════════════════
// Composition
// ═══════════════════════════════════════════════════════════

func ComposeL[A any, B any, C any](f func(A) B, g func(B) C) func(A) C {
	return func(a A) C { return g(f(a)) }
}

func ComposeR[A any, B any, C any](g func(B) C, f func(A) B) func(A) C {
	return func(a A) C { return g(f(a)) }
}

// ═══════════════════════════════════════════════════════════
// Log
// ═══════════════════════════════════════════════════════════

func Log_println(args ...any) any {
	fmt.Println(args...)
	return struct{}{}
}

// ═══════════════════════════════════════════════════════════
// String
// ═══════════════════════════════════════════════════════════

func String_fromInt(n any) any {
	return strconv.Itoa(AsInt(n))
}

func String_fromFloat(f any) any {
	return strconv.FormatFloat(AsFloat(f), 'f', -1, 64)
}

// String.length returns the number of Unicode *code points* (runes), not bytes.
// So "世界" has length 2, not 6.
func String_length(s any) any {
	str := fmt.Sprintf("%v", s)
	n := 0
	for range str {
		n++
	}
	return n
}

func String_isEmpty(s any) any {
	return fmt.Sprintf("%v", s) == ""
}

// ═══════════════════════════════════════════════════════════
// Basics
// ═══════════════════════════════════════════════════════════

func Basics_identity[A any](a A) A {
	return a
}

func Basics_always[A any, B any](a A, _ B) A {
	return a
}

func Basics_not(b bool) bool {
	return !b
}

func Basics_toString(v any) string {
	return fmt.Sprintf("%v", v)
}

// ═══════════════════════════════════════════════════════════
// Concat (temporary — will use + when types are known)
// ═══════════════════════════════════════════════════════════

func Concat(a, b any) any {
	return fmt.Sprintf("%v%v", a, b)
}

// ═══════════════════════════════════════════════════════════
// Arithmetic and comparison (any-typed, until type checker)
// ═══════════════════════════════════════════════════════════

func AsInt(v any) int { if n, ok := v.(int); ok { return n }; return 0 }
func AsFloat(v any) float64 { if f, ok := v.(float64); ok { return f }; if n, ok := v.(int); ok { return float64(n) }; return 0 }
func AsBool(v any) bool { if b, ok := v.(bool); ok { return b }; return false }

func Add(a, b any) any { return AsInt(a) + AsInt(b) }
func Sub(a, b any) any { return AsInt(a) - AsInt(b) }
func Mul(a, b any) any { return AsInt(a) * AsInt(b) }
func Div(a, b any) any { if AsInt(b) == 0 { return 0 }; return AsInt(a) / AsInt(b) }

func Eq(a, b any) any { return a == b }
func Gt(a, b any) any { return AsInt(a) > AsInt(b) }
func Lt(a, b any) any { return AsInt(a) < AsInt(b) }
func Gte(a, b any) any { return AsInt(a) >= AsInt(b) }
func Lte(a, b any) any { return AsInt(a) <= AsInt(b) }

func And(a, b any) any { return AsBool(a) && AsBool(b) }
func Or(a, b any) any { return AsBool(a) || AsBool(b) }

func Negate(a any) any { return -AsInt(a) }

// ═══════════════════════════════════════════════════════════
// List operations
// ═══════════════════════════════════════════════════════════

func List_map(fn any, list any) any {
	f := fn.(func(any) any)
	items := list.([]any)
	result := make([]any, len(items))
	for i, item := range items { result[i] = f(item) }
	return result
}

func List_filter(fn any, list any) any {
	f := fn.(func(any) any)
	items := list.([]any)
	var result []any
	for _, item := range items {
		if AsBool(f(item)) { result = append(result, item) }
	}
	return result
}

func List_foldl(fn any, acc any, list any) any {
	f := fn.(func(any) any)
	items := list.([]any)
	result := acc
	for _, item := range items {
		step := f(item)
		result = step.(func(any) any)(result)
	}
	return result
}

func List_length(list any) any {
	items := list.([]any)
	return len(items)
}

func List_head(list any) any {
	items := list.([]any)
	if len(items) == 0 { return Nothing[any]() }
	return Just[any](items[0])
}

func List_reverse(list any) any {
	items := list.([]any)
	result := make([]any, len(items))
	for i, item := range items { result[len(items)-1-i] = item }
	return result
}

func List_take(n any, list any) any {
	count := AsInt(n)
	items := list.([]any)
	if count > len(items) { count = len(items) }
	return items[:count]
}

func List_drop(n any, list any) any {
	count := AsInt(n)
	items := list.([]any)
	if count > len(items) { count = len(items) }
	return items[count:]
}

func List_append(a any, b any) any {
	return append(a.([]any), b.([]any)...)
}

func List_range(lo any, hi any) any {
	l, h := AsInt(lo), AsInt(hi)
	result := make([]any, 0, h-l+1)
	for i := l; i <= h; i++ { result = append(result, i) }
	return result
}

// ═══════════════════════════════════════════════════════════
// More String operations
// ═══════════════════════════════════════════════════════════

func String_join(sep any, list any) any {
	s := fmt.Sprintf("%v", sep)
	items := list.([]any)
	parts := make([]string, len(items))
	for i, item := range items { parts[i] = fmt.Sprintf("%v", item) }
	return strings.Join(parts, s)
}

func String_split(sep any, s any) any {
	parts := strings.Split(fmt.Sprintf("%v", s), fmt.Sprintf("%v", sep))
	result := make([]any, len(parts))
	for i, p := range parts { result[i] = p }
	return result
}

func String_toInt(s any) any {
	n, err := strconv.Atoi(fmt.Sprintf("%v", s))
	if err != nil { return Nothing[any]() }
	return Just[any](n)
}

func String_toUpper(s any) any { return strings.ToUpper(fmt.Sprintf("%v", s)) }
func String_toLower(s any) any { return strings.ToLower(fmt.Sprintf("%v", s)) }
func String_trim(s any) any { return strings.TrimSpace(fmt.Sprintf("%v", s)) }
func String_contains(sub any, s any) any { return strings.Contains(fmt.Sprintf("%v", s), fmt.Sprintf("%v", sub)) }
func String_startsWith(prefix any, s any) any { return strings.HasPrefix(fmt.Sprintf("%v", s), fmt.Sprintf("%v", prefix)) }
func String_reverse(s any) any { runes := []rune(fmt.Sprintf("%v", s)); for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 { runes[i], runes[j] = runes[j], runes[i] }; return string(runes) }

// ═══════════════════════════════════════════════════════════
// Record operations
// ═══════════════════════════════════════════════════════════

func RecordGet(record any, field string) any {
	if m, ok := record.(map[string]any); ok { return m[field] }
	return nil
}

// RecordUpdate copies a record (map or struct) and applies field overrides.
// Works on both map[string]any and typed Go structs via reflect.
func RecordUpdate(base any, updates map[string]any) any {
	// Fast path: map-based record
	if m, ok := base.(map[string]any); ok {
		result := make(map[string]any, len(m)+len(updates))
		for k, v := range m { result[k] = v }
		for k, v := range updates { result[k] = v }
		return result
	}
	// Reflect path: struct-based record
	v := reflect.ValueOf(base)
	if v.Kind() == reflect.Ptr { v = v.Elem() }
	if v.Kind() != reflect.Struct {
		return base
	}
	// Build a new struct value (copy) and set fields
	copyVal := reflect.New(v.Type()).Elem()
	copyVal.Set(v)
	for k, newVal := range updates {
		f := copyVal.FieldByName(k)
		if !f.IsValid() || !f.CanSet() {
			continue
		}
		nv := reflect.ValueOf(newVal)
		if !nv.IsValid() {
			f.Set(reflect.Zero(f.Type()))
			continue
		}
		if nv.Type().AssignableTo(f.Type()) {
			f.Set(nv)
		} else if nv.Type().ConvertibleTo(f.Type()) {
			f.Set(nv.Convert(f.Type()))
		}
	}
	return copyVal.Interface()
}

// ═══════════════════════════════════════════════════════════
// Tuple types
// ═══════════════════════════════════════════════════════════

type SkyTuple2 struct { V0, V1 any }
type SkyTuple3 struct { V0, V1, V2 any }

// SkyADT: runtime type for ADT case-match dispatch.
// Codegen emits `msg.(rt.SkyADT)` so any local ADT type (with matching Tag/Fields)
// can be pattern-matched via integer Tag comparison.
type SkyADT struct {
	Tag     int
	Fields  []any
	SkyName string
}

// ═══════════════════════════════════════════════════════════
// Result operations
// ═══════════════════════════════════════════════════════════

func Result_map(fn any, result any) any {
	r := result.(SkyResult[any, any])
	if r.Tag == 0 { return Ok[any, any](fn.(func(any) any)(r.OkValue)) }
	return result
}

func Result_andThen(fn any, result any) any {
	r := result.(SkyResult[any, any])
	if r.Tag == 0 { return fn.(func(any) any)(r.OkValue) }
	return result
}

func Result_withDefault(def any, result any) any {
	r := result.(SkyResult[any, any])
	if r.Tag == 0 { return r.OkValue }
	return def
}

func Result_mapError(fn any, result any) any {
	r := result.(SkyResult[any, any])
	if r.Tag == 1 { return Err[any, any](fn.(func(any) any)(r.ErrValue)) }
	return result
}

// ═══════════════════════════════════════════════════════════
// Maybe operations
// ═══════════════════════════════════════════════════════════

func Maybe_withDefault(def any, maybe any) any {
	m := maybe.(SkyMaybe[any])
	if m.Tag == 0 { return m.JustValue }
	return def
}

func Maybe_map(fn any, maybe any) any {
	m := maybe.(SkyMaybe[any])
	if m.Tag == 0 { return Just[any](fn.(func(any) any)(m.JustValue)) }
	return maybe
}

func Maybe_andThen(fn any, maybe any) any {
	m := maybe.(SkyMaybe[any])
	if m.Tag == 0 { return fn.(func(any) any)(m.JustValue) }
	return maybe
}

// ═══════════════════════════════════════════════════════════
// Record field access (reflect-based for any-typed params)
// ═══════════════════════════════════════════════════════════

// ═══════════════════════════════════════════════════════════
// Dict operations
// ═══════════════════════════════════════════════════════════

func Dict_empty() any { return map[string]any{} }

func Dict_insert(key any, val any, dict any) any {
	m := dict.(map[string]any)
	new := make(map[string]any, len(m)+1)
	for k, v := range m { new[k] = v }
	new[fmt.Sprintf("%v", key)] = val
	return new
}

func Dict_get(key any, dict any) any {
	m := dict.(map[string]any)
	v, ok := m[fmt.Sprintf("%v", key)]
	if ok { return Just[any](v) }
	return Nothing[any]()
}

func Dict_remove(key any, dict any) any {
	m := dict.(map[string]any)
	new := make(map[string]any, len(m))
	k := fmt.Sprintf("%v", key)
	for kk, v := range m { if kk != k { new[kk] = v } }
	return new
}

func Dict_member(key any, dict any) any {
	m := dict.(map[string]any)
	_, ok := m[fmt.Sprintf("%v", key)]
	return ok
}

func Dict_keys(dict any) any {
	m := dict.(map[string]any)
	result := make([]any, 0, len(m))
	for k := range m { result = append(result, k) }
	return result
}

func Dict_values(dict any) any {
	m := dict.(map[string]any)
	result := make([]any, 0, len(m))
	for _, v := range m { result = append(result, v) }
	return result
}

func Dict_toList(dict any) any {
	m := dict.(map[string]any)
	result := make([]any, 0, len(m))
	for k, v := range m { result = append(result, SkyTuple2{V0: k, V1: v}) }
	return result
}

func Dict_fromList(list any) any {
	items := list.([]any)
	result := make(map[string]any, len(items))
	for _, item := range items {
		t := item.(SkyTuple2)
		result[fmt.Sprintf("%v", t.V0)] = t.V1
	}
	return result
}

func Dict_map(fn any, dict any) any {
	f := fn.(func(any) any)
	m := dict.(map[string]any)
	result := make(map[string]any, len(m))
	for k, v := range m { result[k] = f(v) }
	return result
}

func Dict_foldl(fn any, acc any, dict any) any {
	f := fn.(func(any) any)
	m := dict.(map[string]any)
	result := acc
	for k, v := range m {
		step := f(k)
		step2 := step.(func(any) any)(v)
		result = step2.(func(any) any)(result)
	}
	return result
}

func Dict_union(a any, b any) any {
	ma := a.(map[string]any)
	mb := b.(map[string]any)
	result := make(map[string]any, len(ma)+len(mb))
	for k, v := range mb { result[k] = v }
	for k, v := range ma { result[k] = v }
	return result
}

// ═══════════════════════════════════════════════════════════
// Math operations
// ═══════════════════════════════════════════════════════════

func Math_abs(n any) any { x := AsInt(n); if x < 0 { return -x }; return x }
func Math_min(a any, b any) any { if AsInt(a) < AsInt(b) { return a }; return b }
func Math_max(a any, b any) any { if AsInt(a) > AsInt(b) { return a }; return b }

func Field(record any, field string) any {
	v := reflect.ValueOf(record)
	if v.Kind() == reflect.Ptr { v = v.Elem() }
	if v.Kind() == reflect.Struct {
		f := v.FieldByName(field)
		if f.IsValid() { return f.Interface() }
	}
	return nil
}

// ═══════════════════════════════════════════════════════════
// Any-typed Task wrappers (until type checker provides types)
// ═══════════════════════════════════════════════════════════

func AnyTaskSucceed(v any) any {
	return func() any { return Ok[any, any](v) }
}

func AnyTaskFail(e any) any {
	return func() any { return Err[any, any](e) }
}

func AnyTaskAndThen(fn any, task any) any {
	return func() any {
		t := task.(func() any)
		r := t().(SkyResult[any, any])
		if r.Tag == 0 {
			next := fn.(func(any) any)(r.OkValue).(func() any)
			return next()
		}
		return Err[any, any](r.ErrValue)
	}
}

func AnyTaskRun(task any) any {
	t := task.(func() any)
	return t()
}

// ═══════════════════════════════════════════════════════════
// Time
// ═══════════════════════════════════════════════════════════

func Time_now() any {
	return time.Now().UnixMilli()
}

func Time_sleep(ms any) any {
	return func() any {
		time.Sleep(time.Duration(AsInt(ms)) * time.Millisecond)
		return Ok[any, any](struct{}{})
	}
}

func Time_unixMillis() any {
	return time.Now().UnixMilli()
}

// ═══════════════════════════════════════════════════════════
// Random
// ═══════════════════════════════════════════════════════════

func Random_int(lo any, hi any) any {
	return func() any {
		l, h := AsInt(lo), AsInt(hi)
		if h <= l { return Ok[any, any](l) }
		return Ok[any, any](l + mrand.Intn(h-l+1))
	}
}

func Random_float(lo any, hi any) any {
	return func() any {
		l := AsFloat(lo)
		h := AsFloat(hi)
		return Ok[any, any](l + mrand.Float64()*(h-l))
	}
}

func Random_choice(list any) any {
	return func() any {
		items := list.([]any)
		if len(items) == 0 { return Err[any, any]("empty list") }
		return Ok[any, any](items[mrand.Intn(len(items))])
	}
}

func Random_shuffle(list any) any {
	return func() any {
		items := list.([]any)
		result := make([]any, len(items))
		copy(result, items)
		mrand.Shuffle(len(result), func(i, j int) { result[i], result[j] = result[j], result[i] })
		return Ok[any, any](result)
	}
}

// ═══════════════════════════════════════════════════════════
// Process
// ═══════════════════════════════════════════════════════════

func Process_run(cmd any, args any) any {
	return func() any {
		cmdStr := fmt.Sprintf("%v", cmd)
		argList := args.([]any)
		strArgs := make([]string, len(argList))
		for i, a := range argList { strArgs[i] = fmt.Sprintf("%v", a) }
		c := exec.Command(cmdStr, strArgs...)
		out, err := c.CombinedOutput()
		if err != nil { return Err[any, any](fmt.Sprintf("%s: %v", string(out), err)) }
		return Ok[any, any](string(out))
	}
}

func Process_exit(code any) any {
	return func() any {
		os.Exit(AsInt(code))
		return Ok[any, any](struct{}{})
	}
}

func Process_getEnv(key any) any {
	return func() any {
		val := os.Getenv(fmt.Sprintf("%v", key))
		if val == "" { return Err[any, any]("env var not set: " + fmt.Sprintf("%v", key)) }
		return Ok[any, any](val)
	}
}

func Process_getCwd() any {
	return func() any {
		dir, err := os.Getwd()
		if err != nil { return Err[any, any](err.Error()) }
		return Ok[any, any](dir)
	}
}

// ═══════════════════════════════════════════════════════════
// File
// ═══════════════════════════════════════════════════════════

func File_readFile(path any) any {
	return func() any {
		data, err := os.ReadFile(fmt.Sprintf("%v", path))
		if err != nil { return Err[any, any](err.Error()) }
		return Ok[any, any](string(data))
	}
}

func File_writeFile(path any, content any) any {
	return func() any {
		err := os.WriteFile(fmt.Sprintf("%v", path), []byte(fmt.Sprintf("%v", content)), 0644)
		if err != nil { return Err[any, any](err.Error()) }
		return Ok[any, any](struct{}{})
	}
}

func File_append(path any, content any) any {
	return func() any {
		f, err := os.OpenFile(fmt.Sprintf("%v", path), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil { return Err[any, any](err.Error()) }
		defer f.Close()
		_, err = f.WriteString(fmt.Sprintf("%v", content))
		if err != nil { return Err[any, any](err.Error()) }
		return Ok[any, any](struct{}{})
	}
}

func File_exists(path any) any {
	return func() any {
		_, err := os.Stat(fmt.Sprintf("%v", path))
		return Ok[any, any](!os.IsNotExist(err))
	}
}

func File_remove(path any) any {
	return func() any {
		err := os.Remove(fmt.Sprintf("%v", path))
		if err != nil { return Err[any, any](err.Error()) }
		return Ok[any, any](struct{}{})
	}
}

func File_mkdirAll(path any) any {
	return func() any {
		err := os.MkdirAll(fmt.Sprintf("%v", path), 0755)
		if err != nil { return Err[any, any](err.Error()) }
		return Ok[any, any](struct{}{})
	}
}

func File_readDir(path any) any {
	return func() any {
		entries, err := os.ReadDir(fmt.Sprintf("%v", path))
		if err != nil { return Err[any, any](err.Error()) }
		result := make([]any, len(entries))
		for i, e := range entries { result[i] = e.Name() }
		return Ok[any, any](result)
	}
}

func File_isDir(path any) any {
	return func() any {
		info, err := os.Stat(fmt.Sprintf("%v", path))
		if err != nil { return Ok[any, any](false) }
		return Ok[any, any](info.IsDir())
	}
}

// ═══════════════════════════════════════════════════════════
// Io
// ═══════════════════════════════════════════════════════════

var stdinReader *bufio.Reader

func Io_readLine() any {
	return func() any {
		if stdinReader == nil { stdinReader = bufio.NewReader(os.Stdin) }
		line, err := stdinReader.ReadString('\n')
		if err != nil && err != io.EOF { return Err[any, any](err.Error()) }
		return Ok[any, any](strings.TrimRight(line, "\n\r"))
	}
}

func Io_writeStdout(s any) any {
	return func() any {
		fmt.Print(s)
		return Ok[any, any](struct{}{})
	}
}

func Io_writeStderr(s any) any {
	return func() any {
		fmt.Fprint(os.Stderr, s)
		return Ok[any, any](struct{}{})
	}
}

// ═══════════════════════════════════════════════════════════
// Crypto
// ═══════════════════════════════════════════════════════════

func Crypto_sha256(s any) any {
	h := sha256.Sum256([]byte(fmt.Sprintf("%v", s)))
	return hex.EncodeToString(h[:])
}

func Crypto_md5(s any) any {
	h := md5.Sum([]byte(fmt.Sprintf("%v", s)))
	return hex.EncodeToString(h[:])
}

// ═══════════════════════════════════════════════════════════
// Encoding
// ═══════════════════════════════════════════════════════════

func Encoding_base64Encode(s any) any {
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%v", s)))
}

func Encoding_base64Decode(s any) any {
	data, err := base64.StdEncoding.DecodeString(fmt.Sprintf("%v", s))
	if err != nil { return Err[any, any](err.Error()) }
	return Ok[any, any](string(data))
}

func Encoding_urlEncode(s any) any {
	return url.QueryEscape(fmt.Sprintf("%v", s))
}

func Encoding_urlDecode(s any) any {
	decoded, err := url.QueryUnescape(fmt.Sprintf("%v", s))
	if err != nil { return Err[any, any](err.Error()) }
	return Ok[any, any](decoded)
}

func Encoding_hexEncode(s any) any {
	return hex.EncodeToString([]byte(fmt.Sprintf("%v", s)))
}

func Encoding_hexDecode(s any) any {
	data, err := hex.DecodeString(fmt.Sprintf("%v", s))
	if err != nil { return Err[any, any](err.Error()) }
	return Ok[any, any](string(data))
}

// ═══════════════════════════════════════════════════════════
// Regex
// ═══════════════════════════════════════════════════════════

func Regex_match(pattern any, s any) any {
	matched, _ := regexp.MatchString(fmt.Sprintf("%v", pattern), fmt.Sprintf("%v", s))
	return matched
}

func Regex_find(pattern any, s any) any {
	re, err := regexp.Compile(fmt.Sprintf("%v", pattern))
	if err != nil { return Nothing[any]() }
	match := re.FindString(fmt.Sprintf("%v", s))
	if match == "" { return Nothing[any]() }
	return Just[any](match)
}

func Regex_findAll(pattern any, s any) any {
	re, err := regexp.Compile(fmt.Sprintf("%v", pattern))
	if err != nil { return []any{} }
	matches := re.FindAllString(fmt.Sprintf("%v", s), -1)
	result := make([]any, len(matches))
	for i, m := range matches { result[i] = m }
	return result
}

func Regex_replace(pattern any, replacement any, s any) any {
	re, err := regexp.Compile(fmt.Sprintf("%v", pattern))
	if err != nil { return s }
	return re.ReplaceAllString(fmt.Sprintf("%v", s), fmt.Sprintf("%v", replacement))
}

func Regex_split(pattern any, s any) any {
	re, err := regexp.Compile(fmt.Sprintf("%v", pattern))
	if err != nil { return []any{s} }
	parts := re.Split(fmt.Sprintf("%v", s), -1)
	result := make([]any, len(parts))
	for i, p := range parts { result[i] = p }
	return result
}

// ═══════════════════════════════════════════════════════════
// Char
// ═══════════════════════════════════════════════════════════

// firstRune extracts the first Unicode code point from its input.
// Works for both Sky Char (runtime-typed as single-rune string) and Sky String.
func firstRune(c any) rune {
	if r, ok := c.(rune); ok {
		return r
	}
	s := fmt.Sprintf("%v", c)
	for _, r := range s {
		return r
	}
	return 0
}

func Char_isUpper(c any) any { return unicode.IsUpper(firstRune(c)) }
func Char_isLower(c any) any { return unicode.IsLower(firstRune(c)) }
func Char_isDigit(c any) any { return unicode.IsDigit(firstRune(c)) }
func Char_isAlpha(c any) any { return unicode.IsLetter(firstRune(c)) }
func Char_toUpper(c any) any { return string(unicode.ToUpper(firstRune(c))) }
func Char_toLower(c any) any { return string(unicode.ToLower(firstRune(c))) }

// ═══════════════════════════════════════════════════════════
// Math (extended)
// ═══════════════════════════════════════════════════════════

func Math_sqrt(n any) any  { return math.Sqrt(AsFloat(n)) }
func Math_pow(base any, exp any) any { return math.Pow(AsFloat(base), AsFloat(exp)) }
func Math_floor(n any) any { return int(math.Floor(AsFloat(n))) }
func Math_ceil(n any) any  { return int(math.Ceil(AsFloat(n))) }
func Math_round(n any) any { return int(math.Round(AsFloat(n))) }
func Math_sin(n any) any   { return math.Sin(AsFloat(n)) }
func Math_cos(n any) any   { return math.Cos(AsFloat(n)) }
func Math_tan(n any) any   { return math.Tan(AsFloat(n)) }
func Math_pi() any         { return math.Pi }
func Math_e() any          { return math.E }
func Math_log(n any) any   { return math.Log(AsFloat(n)) }

// ═══════════════════════════════════════════════════════════
// Additional String functions
// ═══════════════════════════════════════════════════════════

func String_lines(s any) any {
	parts := strings.Split(fmt.Sprintf("%v", s), "\n")
	result := make([]any, len(parts))
	for i, p := range parts { result[i] = p }
	return result
}

func String_words(s any) any {
	parts := strings.Fields(fmt.Sprintf("%v", s))
	result := make([]any, len(parts))
	for i, p := range parts { result[i] = p }
	return result
}

func String_repeat(n any, s any) any {
	return strings.Repeat(fmt.Sprintf("%v", s), AsInt(n))
}

// runeCount returns the number of Unicode code points in s.
func runeCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

func String_padLeft(n any, ch any, s any) any {
	str := fmt.Sprintf("%v", s)
	pad := fmt.Sprintf("%v", ch)
	target := AsInt(n)
	for runeCount(str) < target {
		str = pad + str
	}
	return str
}

func String_padRight(n any, ch any, s any) any {
	str := fmt.Sprintf("%v", s)
	pad := fmt.Sprintf("%v", ch)
	target := AsInt(n)
	for runeCount(str) < target {
		str = str + pad
	}
	return str
}

func String_left(n any, s any) any {
	runes := []rune(fmt.Sprintf("%v", s))
	nn := AsInt(n)
	if nn > len(runes) {
		nn = len(runes)
	}
	if nn < 0 {
		nn = 0
	}
	return string(runes[:nn])
}

func String_right(n any, s any) any {
	runes := []rune(fmt.Sprintf("%v", s))
	nn := AsInt(n)
	if nn > len(runes) {
		nn = len(runes)
	}
	if nn < 0 {
		nn = 0
	}
	return string(runes[len(runes)-nn:])
}

func String_replace(old any, new_ any, s any) any {
	return strings.ReplaceAll(fmt.Sprintf("%v", s), fmt.Sprintf("%v", old), fmt.Sprintf("%v", new_))
}

// String.slice is rune-based. Negative indices count from the end.
func String_slice(start any, end any, s any) any {
	runes := []rune(fmt.Sprintf("%v", s))
	total := len(runes)
	st := AsInt(start)
	en := AsInt(end)
	if st < 0 {
		st = total + st
	}
	if en < 0 {
		en = total + en
	}
	if st < 0 {
		st = 0
	}
	if en > total {
		en = total
	}
	if st > en {
		return ""
	}
	return string(runes[st:en])
}

// ═══════════════════════════════════════════════════════════
// Additional List functions
// ═══════════════════════════════════════════════════════════

func List_sort(list any) any {
	items := list.([]any)
	result := make([]any, len(items))
	copy(result, items)
	sort.Slice(result, func(i, j int) bool {
		return fmt.Sprintf("%v", result[i]) < fmt.Sprintf("%v", result[j])
	})
	return result
}

func List_member(item any, list any) any {
	items := list.([]any)
	for _, v := range items {
		if v == item { return true }
	}
	return false
}

func List_any(fn any, list any) any {
	f := fn.(func(any) any)
	items := list.([]any)
	for _, item := range items {
		if AsBool(f(item)) { return true }
	}
	return false
}

func List_all(fn any, list any) any {
	f := fn.(func(any) any)
	items := list.([]any)
	for _, item := range items {
		if !AsBool(f(item)) { return false }
	}
	return true
}

func List_zip(a any, b any) any {
	la := a.([]any)
	lb := b.([]any)
	n := len(la)
	if len(lb) < n { n = len(lb) }
	result := make([]any, n)
	for i := 0; i < n; i++ { result[i] = SkyTuple2{V0: la[i], V1: lb[i]} }
	return result
}

func List_concat(lists any) any {
	items := lists.([]any)
	var result []any
	for _, l := range items {
		result = append(result, l.([]any)...)
	}
	return result
}

func List_concatMap(fn any, list any) any {
	f := fn.(func(any) any)
	items := list.([]any)
	var result []any
	for _, item := range items {
		mapped := f(item).([]any)
		result = append(result, mapped...)
	}
	return result
}

func List_filterMap(fn any, list any) any {
	f := fn.(func(any) any)
	items := list.([]any)
	var result []any
	for _, item := range items {
		maybe := f(item).(SkyMaybe[any])
		if maybe.Tag == 0 { result = append(result, maybe.JustValue) }
	}
	return result
}

func List_foldr(fn any, acc any, list any) any {
	f := fn.(func(any) any)
	items := list.([]any)
	result := acc
	for i := len(items) - 1; i >= 0; i-- {
		step := f(items[i])
		result = step.(func(any) any)(result)
	}
	return result
}

func List_tail(list any) any {
	items := list.([]any)
	if len(items) == 0 { return Nothing[any]() }
	return Just[any](items[1:])
}

// Suppress unused import warnings
var _ = bufio.NewReader
var _ = io.EOF
var _ = exec.Command
var _ = os.Exit
var _ = time.Now
var _ = mrand.Intn
var _ = sha256.Sum256
var _ = md5.Sum
var _ = base64.StdEncoding
var _ = hex.EncodeToString
var _ = url.QueryEscape
var _ = regexp.Compile
var _ = unicode.IsUpper
var _ = math.Pi
var _ = sort.Slice

// ═══════════════════════════════════════════════════════════
// Sky.Http.Server — HTTP server framework
// ═══════════════════════════════════════════════════════════

// Route represents a single HTTP route
type SkyRoute struct {
	Method  string
	Path    string
	Handler any // func(SkyRequest) any (Task that returns SkyResponse)
}

// SkyRequest wraps an HTTP request
type SkyRequest struct {
	Method  string
	Path    string
	Body    string
	Headers map[string]any
	Params  map[string]any
	Query   map[string]any
}

// SkyResponse wraps an HTTP response
type SkyResponse struct {
	Status  int
	Body    string
	Headers map[string]string
	ContentType string
}

func Server_listen(port any, routes any) any {
	p := AsInt(port)
	routeList := routes.([]any)
	mux := http.NewServeMux()

	for _, r := range routeList {
		route := r.(SkyRoute)
		handler := route.Handler
		pattern := route.Path

		mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
			skyReq := SkyRequest{
				Method:  req.Method,
				Path:    req.URL.Path,
				Headers: make(map[string]any),
				Params:  make(map[string]any),
				Query:   make(map[string]any),
			}
			if req.Body != nil {
				bodyBytes, _ := io.ReadAll(req.Body)
				skyReq.Body = string(bodyBytes)
			}
			for k, v := range req.URL.Query() {
				if len(v) > 0 { skyReq.Query[k] = v[0] }
			}

			task := handler.(func(any) any)(skyReq)
			result := task.(func() any)()

			resp, ok := result.(SkyResult[any, any])
			if ok && resp.Tag == 0 {
				skyResp := resp.OkValue.(SkyResponse)
				for k, v := range skyResp.Headers {
					w.Header().Set(k, v)
				}
				if skyResp.ContentType != "" {
					w.Header().Set("Content-Type", skyResp.ContentType)
				}
				if skyResp.Status > 0 {
					w.WriteHeader(skyResp.Status)
				}
				fmt.Fprint(w, skyResp.Body)
			} else {
				w.WriteHeader(500)
				fmt.Fprint(w, "Internal Server Error")
			}
		})
	}

	fmt.Printf("Sky server listening on http://localhost:%d\n", p)
	// Block — this is the main entry point for server apps
	err := http.ListenAndServe(fmt.Sprintf(":%d", p), mux)
	if err != nil {
		return Err[any, any](err.Error())
	}
	return Ok[any, any](struct{}{})
}

func Server_get(path any, handler any) any {
	return SkyRoute{Method: "GET", Path: fmt.Sprintf("%v", path), Handler: handler}
}

func Server_post(path any, handler any) any {
	return SkyRoute{Method: "POST", Path: fmt.Sprintf("%v", path), Handler: handler}
}

func Server_put(path any, handler any) any {
	return SkyRoute{Method: "PUT", Path: fmt.Sprintf("%v", path), Handler: handler}
}

func Server_delete(path any, handler any) any {
	return SkyRoute{Method: "DELETE", Path: fmt.Sprintf("%v", path), Handler: handler}
}

func Server_text(body any) any {
	return SkyResponse{Status: 200, Body: fmt.Sprintf("%v", body), ContentType: "text/plain"}
}

func Server_json(body any) any {
	return SkyResponse{Status: 200, Body: fmt.Sprintf("%v", body), ContentType: "application/json"}
}

func Server_html(body any) any {
	return SkyResponse{Status: 200, Body: fmt.Sprintf("%v", body), ContentType: "text/html"}
}

func Server_withStatus(status any, resp any) any {
	r := resp.(SkyResponse)
	r.Status = AsInt(status)
	return r
}

func Server_redirect(url any) any {
	return SkyResponse{
		Status: 302,
		Headers: map[string]string{"Location": fmt.Sprintf("%v", url)},
	}
}

func Server_param(name any, req any) any {
	r := req.(SkyRequest)
	v, ok := r.Params[fmt.Sprintf("%v", name)]
	if ok { return Just[any](v) }
	return Nothing[any]()
}

func Server_queryParam(name any, req any) any {
	r := req.(SkyRequest)
	v, ok := r.Query[fmt.Sprintf("%v", name)]
	if ok { return Just[any](v) }
	return Nothing[any]()
}

func Server_header(name any, req any) any {
	r := req.(SkyRequest)
	v, ok := r.Headers[fmt.Sprintf("%v", name)]
	if ok { return Just[any](v) }
	return Nothing[any]()
}

func Server_static(path any, dir any) any {
	return SkyRoute{
		Method: "GET",
		Path: fmt.Sprintf("%v", path),
		Handler: func(req any) any {
			return func() any {
				return Ok[any, any](SkyResponse{Status: 200, Body: "static:" + fmt.Sprintf("%v", dir)})
			}
		},
	}
}
