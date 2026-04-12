package rt

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/md5"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
	"sync"
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

// Debug_toString: universal stringify for any Sky value. Used by the
// multiline-string interpolation desugarer at canonicalise time.
func Debug_toString(v any) any {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

func Log_println(args ...any) any {
	fmt.Println(args...)
	return struct{}{}
}

// ═══════════════════════════════════════════════════════════
// Structured logging — severity levels + optional JSON output.
// Set SKY_LOG_FORMAT=json for one-line JSON records suitable for log
// aggregators (Loki, Datadog, CloudWatch). Otherwise human-readable.
// Set SKY_LOG_LEVEL=debug|info|warn|error to gate output.
// ═══════════════════════════════════════════════════════════

const (
	logLevelDebug = 0
	logLevelInfo  = 1
	logLevelWarn  = 2
	logLevelError = 3
)

var (
	logThreshold = logLevelFromEnv()
	logJSON      = os.Getenv("SKY_LOG_FORMAT") == "json"
)

func logLevelFromEnv() int {
	switch strings.ToLower(os.Getenv("SKY_LOG_LEVEL")) {
	case "debug":
		return logLevelDebug
	case "warn", "warning":
		return logLevelWarn
	case "error":
		return logLevelError
	default:
		return logLevelInfo
	}
}

func logEmit(level int, levelName string, msg string, ctx any) {
	if level < logThreshold {
		return
	}
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	if logJSON {
		entry := map[string]any{
			"time":  ts,
			"level": levelName,
			"msg":   msg,
		}
		if m, ok := ctx.(map[string]any); ok {
			for k, v := range m {
				if k != "time" && k != "level" && k != "msg" {
					entry[k] = v
				}
			}
		}
		b, _ := json.Marshal(entry)
		if level >= logLevelWarn {
			fmt.Fprintln(os.Stderr, string(b))
		} else {
			fmt.Fprintln(os.Stdout, string(b))
		}
		return
	}
	line := ts + " " + strings.ToUpper(levelName) + " " + msg
	if m, ok := ctx.(map[string]any); ok && len(m) > 0 {
		var b strings.Builder
		for k, v := range m {
			b.WriteString(" ")
			b.WriteString(k)
			b.WriteString("=")
			b.WriteString(fmt.Sprintf("%v", v))
		}
		line += b.String()
	}
	if level >= logLevelWarn {
		fmt.Fprintln(os.Stderr, line)
	} else {
		fmt.Fprintln(os.Stdout, line)
	}
}

// Log.debug : String -> ()
func Log_debug(msg any) any {
	logEmit(logLevelDebug, "debug", fmt.Sprintf("%v", msg), nil)
	return struct{}{}
}

// Log.info : String -> ()
func Log_info(msg any) any {
	logEmit(logLevelInfo, "info", fmt.Sprintf("%v", msg), nil)
	return struct{}{}
}

// Log.warn : String -> ()
func Log_warn(msg any) any {
	logEmit(logLevelWarn, "warn", fmt.Sprintf("%v", msg), nil)
	return struct{}{}
}

// Log.error : String -> ()
func Log_error(msg any) any {
	logEmit(logLevelError, "error", fmt.Sprintf("%v", msg), nil)
	return struct{}{}
}

// Log.with : String -> Dict String any -> ()
// Structured log with additional context fields. E.g.
//   Log.with "request completed" (Dict.fromList [("method","GET"), ("status",200)])
func Log_with(msg any, ctx any) any {
	logEmit(logLevelInfo, "info", fmt.Sprintf("%v", msg), ctx)
	return struct{}{}
}

// Log.errorWith : String -> Dict String any -> ()
func Log_errorWith(msg any, ctx any) any {
	logEmit(logLevelError, "error", fmt.Sprintf("%v", msg), ctx)
	return struct{}{}
}

// ═══════════════════════════════════════════════════════════
// String
// ═══════════════════════════════════════════════════════════

func String_append(a any, b any) any {
	return fmt.Sprintf("%v", a) + fmt.Sprintf("%v", b)
}

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

func Basics_not(b any) any {
	return !AsBool(b)
}

func Basics_toString(v any) string {
	return fmt.Sprintf("%v", v)
}

// Basics_errorToString — Elm-compat extractor for Result errors. Preserves
// String/error values verbatim, stringifies anything else. Registered as a
// Prelude builtin (`errorToString`) so Sky programs can write:
//   Result.mapError errorToString someResult
func Basics_errorToString(v any) any {
	switch x := v.(type) {
	case string:
		return x
	case error:
		return x.Error()
	}
	return fmt.Sprintf("%v", v)
}

// Basics_js — legacy FFI pass-through. Legacy Sky code used `js "nil"` to
// inject a raw Go nil into an FFI call; here we mirror that so ex13 and
// similar programs compile without a user-visible change.
// Everything else flows through identity-style.
func Basics_js(v any) any {
	if s, ok := v.(string); ok && s == "nil" {
		return nil
	}
	return v
}

// ═══════════════════════════════════════════════════════════
// Context — Go's context pkg, surfaced for FFI boundary
// ═══════════════════════════════════════════════════════════

// Context_background : () -> context.Context — opaque, flows through FFI.
func Context_background(_ any) any { return context.Background() }
func Context_todo(_ any) any       { return context.TODO() }

func Context_withValue(parent any, key any, val any) any {
	ctx, _ := parent.(context.Context)
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, key, val)
}

func Context_withCancel(parent any) any {
	ctx, _ := parent.(context.Context)
	if ctx == nil {
		ctx = context.Background()
	}
	c, cancel := context.WithCancel(ctx)
	_ = cancel  // Sky can't easily thread the cancel fn; discard for now.
	return c
}

// ═══════════════════════════════════════════════════════════
// Fmt — subset of Go's fmt pkg for string-building interop
// ═══════════════════════════════════════════════════════════

func Fmt_sprint(args ...any) any    { return fmt.Sprint(args...) }
func Fmt_sprintf(format any, args ...any) any {
	return fmt.Sprintf(fmt.Sprintf("%v", format), args...)
}
func Fmt_sprintln(args ...any) any  { return fmt.Sprintln(args...) }
func Fmt_errorf(format any, args ...any) any {
	return fmt.Errorf(fmt.Sprintf("%v", format), args...)
}

// Basics_modBy, Basics_fst, Basics_snd — any-typed to match the codegen's
// default calling convention. modBy is (divisor, dividend) — divisor first
// to match the Elm/Sky argument order for pipeline use.
func Basics_modBy(divisor, n any) any {
	d := AsInt(divisor)
	if d == 0 {
		return 0
	}
	return AsInt(n) % d
}

func Basics_fst(t any) any {
	switch v := t.(type) {
	case SkyTuple2:
		return v.V0
	case SkyTuple3:
		return v.V0
	}
	return nil
}

func Basics_snd(t any) any {
	switch v := t.(type) {
	case SkyTuple2:
		return v.V1
	case SkyTuple3:
		return v.V1
	}
	return nil
}

func List_cons(head, tail any) any {
	if tail == nil {
		return []any{head}
	}
	switch xs := tail.(type) {
	case []any:
		out := make([]any, 0, len(xs)+1)
		out = append(out, head)
		out = append(out, xs...)
		return out
	}
	return []any{head}
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

func AsInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case int32:
		return int(n)
	case int16:
		return int(n)
	case int8:
		return int(n)
	case uint:
		return int(n)
	case uint64:
		return int(n)
	case uint32:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	}
	return 0
}
func AsFloat(v any) float64 { if f, ok := v.(float64); ok { return f }; if n, ok := v.(int); ok { return float64(n) }; return 0 }
func AsBool(v any) bool { if b, ok := v.(bool); ok { return b }; return false }

func Add(a, b any) any { return AsInt(a) + AsInt(b) }
func Sub(a, b any) any { return AsInt(a) - AsInt(b) }
func Mul(a, b any) any { return AsInt(a) * AsInt(b) }
func Div(a, b any) any { if AsInt(b) == 0 { return 0 }; return AsInt(a) / AsInt(b) }
func IntDiv(a, b any) any { if AsInt(b) == 0 { return 0 }; return AsInt(a) / AsInt(b) }
func Rem(a, b any) any { if AsInt(b) == 0 { return 0 }; return AsInt(a) % AsInt(b) }

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
// SkyTupleN: arity ≥ 4 tuples use a uniform slice-backed struct. Element
// access in generated code is `t.Vs[i]`, symmetric with `.V0/.V1/.V2` on
// 2/3-tuples.
type SkyTupleN struct { Vs []any }

// ═══════════════════════════════════════════════════════════
// FFI — name-based dispatch for user-supplied Go bindings
// ═══════════════════════════════════════════════════════════
//
// Two registries, reflecting Sky's effect boundary:
//
//   ffiRegistry     — effect-unknown (DEFAULT). Any Go code we can't
//                     personally audit lives here. Callable only via
//                     Ffi.callTask so the effect is deferred through
//                     Sky's Task mechanism, preserving referential
//                     transparency. Ffi.callPure on these names
//                     returns Err directing the caller to callTask.
//
//   ffiPureRegistry — hand-verified pure. For opaque-type getters,
//                     setters-that-copy, zero-value constructors, and
//                     pure data transforms where the Go source has been
//                     audited to have no I/O, no shared mutable state
//                     access, and no panic path other than explicit
//                     type-assertion failures (which our panic-recover
//                     will turn into Err anyway). Callable via either
//                     Ffi.callPure or Ffi.callTask.
//
// The auto-generated binding generator (sky add <pkg>) ALWAYS uses
// Register, never RegisterPure. Hand-written ffi/*.go files can use
// RegisterPure when the user vouches for a specific Go function.
//
// Every invocation is wrapped in panic-recover; a panic in Go code
// becomes an Err, never a process crash.

var (
	ffiRegistryMu   sync.RWMutex
	ffiRegistry     = map[string]func([]any) any{} // effect-unknown
	ffiPureRegistry = map[string]func([]any) any{} // hand-verified pure
)

// Register exposes a Go function with no purity claim.
// Auto-generated bindings use this. Callable only via Ffi.callTask.
// reflectValueOfAny / reflectNewOf: thin aliases over reflect package
// primitives, exported so auto-generated binding files (in package rt) don't
// need to import "reflect" themselves. Used by the identity-pointer
// generic fallback (Stripe's String[T any](v T) *T and friends).
func reflectValueOfAny(v any) reflect.Value { return reflect.ValueOf(v) }
func reflectNewOf(t reflect.Type) reflect.Value { return reflect.New(t) }

func Register(name string, fn func([]any) any) {
	ffiRegistryMu.Lock()
	defer ffiRegistryMu.Unlock()
	ffiRegistry[name] = fn
}

// RegisterPure exposes a Go function that the caller has audited to be pure.
// Safe for Ffi.callPure. Suitable for:
//   - opaque-type getters (struct field read via copy)
//   - opaque-type setters (struct field write on a copy)
//   - zero-value constructors (no args, deterministic output)
//   - pure data transforms (crypto hash, text slugification, …)
// NOT suitable for anything that reads time, env, args, files, network,
// random, a database, global state, or spawns goroutines.
func RegisterPure(name string, fn func([]any) any) {
	ffiRegistryMu.Lock()
	defer ffiRegistryMu.Unlock()
	ffiPureRegistry[name] = fn
}

// invokeFfi resolves and runs a registered function with panic recovery.
// When pureOnly is true we refuse effect-unknown bindings and direct the
// caller to use Ffi.callTask instead — this keeps the effect boundary
// enforced in the runtime, not merely by convention.
func invokeFfi(name string, args []any, pureOnly bool) any {
	ffiRegistryMu.RLock()
	if fn, ok := ffiPureRegistry[name]; ok {
		ffiRegistryMu.RUnlock()
		return runWithRecover(name, args, fn)
	}
	if fn, ok := ffiRegistry[name]; ok {
		ffiRegistryMu.RUnlock()
		if pureOnly {
			return Err[any, any](
				"Ffi.callPure: " + name +
					" is registered as effect-unknown — use Ffi.callTask. " +
					"Auto-generated FFI bindings default to effect-unknown. " +
					"Use rt.RegisterPure from a hand-written ffi/*.go file " +
					"only if you have audited the underlying Go function.")
		}
		return runWithRecover(name, args, fn)
	}
	ffiRegistryMu.RUnlock()
	return Err[any, any]("Ffi: not registered: " + name)
}

func runWithRecover(name string, args []any, fn func([]any) any) (result any) {
	defer func() {
		if r := recover(); r != nil {
			result = Err[any, any](fmt.Sprintf("Ffi %q panicked: %v", name, r))
		}
	}()
	return Ok[any, any](fn(args))
}

// Ffi.callPure : String -> List any -> Result String a
// Works ONLY on RegisterPure'd bindings. For effect-unknown bindings
// (the default for auto-generated Go FFI) this returns Err directing
// the caller to use Ffi.callTask. This enforces Sky's pure-functional
// effect boundary in the runtime, not just by convention.
func Ffi_callPure(name any, args any) any {
	return invokeFfi(fmt.Sprintf("%v", name), asList(args), true)
}

// Ffi.callTask : String -> List any -> Task String a
// Works on any registered binding. Returns a deferred thunk (Sky Task)
// that runs only when sequenced via Task.perform / Task.andThen. This
// is the ONLY correct way to call auto-generated / untrusted Go bindings.
func Ffi_callTask(name any, args any) any {
	n := fmt.Sprintf("%v", name)
	argList := asList(args)
	return func() any {
		return invokeFfi(n, argList, false)
	}
}

// Ffi.call : deprecated alias for callPure.
func Ffi_call(name any, args any) any {
	return Ffi_callPure(name, args)
}

// Ffi.has : String -> Bool — True if registered in either registry.
func Ffi_has(name any) any {
	n := fmt.Sprintf("%v", name)
	ffiRegistryMu.RLock()
	_, okE := ffiRegistry[n]
	_, okP := ffiPureRegistry[n]
	ffiRegistryMu.RUnlock()
	return okE || okP
}

// Ffi.isPure : String -> Bool — True if the binding was registered as pure.
func Ffi_isPure(name any) any {
	n := fmt.Sprintf("%v", name)
	ffiRegistryMu.RLock()
	_, ok := ffiPureRegistry[n]
	ffiRegistryMu.RUnlock()
	return ok
}

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

// Result.map2..map5 — apply a function to N successful results, short-
// circuiting on first Err.
func Result_map2(fn, a, b any) any {
	ra, _ := a.(SkyResult[any, any]); if ra.Tag != 0 { return a }
	rb, _ := b.(SkyResult[any, any]); if rb.Tag != 0 { return b }
	return Ok[any, any](apply2(fn, ra.OkValue, rb.OkValue))
}

func Result_map3(fn, a, b, c any) any {
	ra, _ := a.(SkyResult[any, any]); if ra.Tag != 0 { return a }
	rb, _ := b.(SkyResult[any, any]); if rb.Tag != 0 { return b }
	rc, _ := c.(SkyResult[any, any]); if rc.Tag != 0 { return c }
	return Ok[any, any](apply3(fn, ra.OkValue, rb.OkValue, rc.OkValue))
}

func Result_map4(fn, a, b, c, d any) any {
	ra, _ := a.(SkyResult[any, any]); if ra.Tag != 0 { return a }
	rb, _ := b.(SkyResult[any, any]); if rb.Tag != 0 { return b }
	rc, _ := c.(SkyResult[any, any]); if rc.Tag != 0 { return c }
	rd, _ := d.(SkyResult[any, any]); if rd.Tag != 0 { return d }
	return Ok[any, any](apply4(fn, ra.OkValue, rb.OkValue, rc.OkValue, rd.OkValue))
}

func Result_map5(fn, a, b, c, d, e any) any {
	ra, _ := a.(SkyResult[any, any]); if ra.Tag != 0 { return a }
	rb, _ := b.(SkyResult[any, any]); if rb.Tag != 0 { return b }
	rc, _ := c.(SkyResult[any, any]); if rc.Tag != 0 { return c }
	rd, _ := d.(SkyResult[any, any]); if rd.Tag != 0 { return d }
	re, _ := e.(SkyResult[any, any]); if re.Tag != 0 { return e }
	return Ok[any, any](apply5(fn, ra.OkValue, rb.OkValue, rc.OkValue, rd.OkValue, re.OkValue))
}

// Result.andMap : Result e (a -> b) -> Result e a -> Result e b
func Result_andMap(fr, ra any) any {
	rr, _ := fr.(SkyResult[any, any])
	if rr.Tag != 0 { return fr }
	rv, _ := ra.(SkyResult[any, any])
	if rv.Tag != 0 { return ra }
	return Ok[any, any](pipelineApply(rr.OkValue, rv.OkValue))
}

// Result.combine : List (Result e a) -> Result e (List a)
// First Err short-circuits.
func Result_combine(results any) any {
	items := asList(results)
	out := make([]any, 0, len(items))
	for _, r := range items {
		rr, ok := r.(SkyResult[any, any])
		if !ok { return r }
		if rr.Tag != 0 { return r }
		out = append(out, rr.OkValue)
	}
	return Ok[any, any](out)
}

// Result.traverse : (a -> Result e b) -> List a -> Result e (List b)
func Result_traverse(fn, items any) any {
	xs := asList(items)
	out := make([]any, 0, len(xs))
	f, ok := fn.(func(any) any)
	if !ok {
		return Err[any, any]("Result.traverse: fn must be a 1-arg function")
	}
	for _, x := range xs {
		r := f(x)
		rr, rok := r.(SkyResult[any, any])
		if !rok { return Err[any, any]("Result.traverse: fn did not return a Result") }
		if rr.Tag != 0 { return r }
		out = append(out, rr.OkValue)
	}
	return Ok[any, any](out)
}

// Log.Slog.info / warn / error / debug — aliases for existing Log functions
// for code that was written against Go's slog-style names.
func Slog_info(args ...any)  any { return Log_info(stringifyLogArgs(args)) }
func Slog_warn(args ...any)  any { return Log_warn(stringifyLogArgs(args)) }
func Slog_error(args ...any) any { return Log_error(stringifyLogArgs(args)) }
func Slog_debug(args ...any) any { return Log_debug(stringifyLogArgs(args)) }

func stringifyLogArgs(args []any) any {
	if len(args) == 0 {
		return ""
	}
	if len(args) == 1 {
		return args[0]
	}
	var sb strings.Builder
	for i, a := range args {
		if i > 0 { sb.WriteString(" ") }
		sb.WriteString(fmt.Sprintf("%v", a))
	}
	return sb.String()
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

// Task_sequence: run tasks in order, collect results as a list.
// First error short-circuits.
func Task_sequence(tasks any) any {
	return func() any {
		var xs []any
		if tl, ok := tasks.([]any); ok {
			xs = tl
		}
		out := make([]any, 0, len(xs))
		for _, t := range xs {
			r := SkyCall(t).(SkyResult[any, any])
			if r.Tag != 0 {
				return r
			}
			out = append(out, r.OkValue)
		}
		return Ok[any, any](out)
	}
}

// Task_parallel: goroutine-backed fan-out; preserves input order; first err wins.
func Task_parallel(tasks any) any {
	return func() any {
		var xs []any
		if tl, ok := tasks.([]any); ok {
			xs = tl
		}
		n := len(xs)
		results := make([]any, n)
		errs := make([]any, n)
		var wg sync.WaitGroup
		for i, t := range xs {
			wg.Add(1)
			go func(i int, t any) {
				defer wg.Done()
				r := SkyCall(t).(SkyResult[any, any])
				if r.Tag == 0 {
					results[i] = r.OkValue
				} else {
					errs[i] = r.ErrValue
				}
			}(i, t)
		}
		wg.Wait()
		for _, e := range errs {
			if e != nil {
				return Err[any, any](e)
			}
		}
		return Ok[any, any](results)
	}
}

func Task_map(fn any, task any) any {
	return func() any {
		r := SkyCall(task).(SkyResult[any, any])
		if r.Tag != 0 {
			return r
		}
		return Ok[any, any](SkyCall(fn, r.OkValue))
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
	return Ok[any, any](time.Now().UnixMilli())
}

// Time_timeString: format unixMillis as "HH:MM:SS"
func Time_timeString(ms any) any {
	return Ok[any, any](time.Unix(int64(AsInt(ms))/1000, 0).Format("15:04:05"))
}

// Sha256, Hex, String.toBytes wrappers matching the Sky.Core namespace split.
// sum256: (List Int of UTF-8 bytes) -> Result String (List Int of hash bytes)
func Sha256_sum256(bytes any) any {
	var b []byte
	if xs, ok := bytes.([]any); ok {
		b = make([]byte, len(xs))
		for i, v := range xs {
			b[i] = byte(AsInt(v))
		}
	} else {
		b = []byte(fmt.Sprintf("%v", bytes))
	}
	h := sha256.Sum256(b)
	out := make([]any, len(h))
	for i, v := range h {
		out[i] = int(v)
	}
	return Ok[any, any](out)
}

func Sha256_sum256String(s any) any {
	h := sha256.Sum256([]byte(fmt.Sprintf("%v", s)))
	return Ok[any, any](hex.EncodeToString(h[:]))
}

func Hex_encodeToString(bytes any) any {
	if xs, ok := bytes.([]any); ok {
		b := make([]byte, len(xs))
		for i, v := range xs {
			b[i] = byte(AsInt(v))
		}
		return Ok[any, any](hex.EncodeToString(b))
	}
	return Ok[any, any](hex.EncodeToString([]byte(fmt.Sprintf("%v", bytes))))
}

func Hex_encode(bytes any) any { return Hex_encodeToString(bytes) }

func Hex_decode(s any) any {
	b, err := hex.DecodeString(fmt.Sprintf("%v", s))
	if err != nil {
		return Err[any, any](err.Error())
	}
	out := make([]any, len(b))
	for i, v := range b {
		out[i] = int(v)
	}
	return Ok[any, any](out)
}

func String_toBytes(s any) any {
	b := []byte(fmt.Sprintf("%v", s))
	out := make([]any, len(b))
	for i, v := range b {
		out[i] = int(v)
	}
	return out
}

func String_fromBytes(bytes any) any {
	if xs, ok := bytes.([]any); ok {
		b := make([]byte, len(xs))
		for i, v := range xs {
			b[i] = byte(AsInt(v))
		}
		return string(b)
	}
	return ""
}

func String_fromChar(c any) any {
	if r, ok := c.(rune); ok {
		return string(r)
	}
	return fmt.Sprintf("%v", c)
}

func String_toChar(s any) any {
	str := fmt.Sprintf("%v", s)
	for _, r := range str {
		return r
	}
	return rune(0)
}

// Os — CLI args, environment, cwd, exit.
// Zero-arg Sky funcs take a unit param at runtime so the call-site form
// `Os.args ()` emits `rt.Os_args(struct{}{})` and works uniformly with C2.
func Os_args(_ any) any {
	out := make([]any, 0, len(os.Args))
	if len(os.Args) > 1 {
		for _, a := range os.Args[1:] {
			out = append(out, a)
		}
	}
	return out
}

func Os_getenv(name any) any {
	v := os.Getenv(fmt.Sprintf("%v", name))
	if v == "" {
		return Nothing[any]()
	}
	return Just[any](v)
}

func Os_cwd(_ any) any {
	wd, err := os.Getwd()
	if err != nil {
		return Err[any, any](err.Error())
	}
	return Ok[any, any](wd)
}

func Os_exit(code any) any {
	os.Exit(AsInt(code))
	return struct{}{}
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

// Time.formatISO8601 : Int -> String
// (unixMillis) → ISO-8601 / RFC 3339 UTC timestamp: "2026-04-12T12:34:56.789Z".
// The web-standard format — use for JSON APIs, logs, database timestamps.
func Time_formatISO8601(ms any) any {
	t := time.UnixMilli(int64(AsInt(ms))).UTC()
	return t.Format("2006-01-02T15:04:05.000Z")
}

// Time.formatRFC3339 : Int -> String
func Time_formatRFC3339(ms any) any {
	t := time.UnixMilli(int64(AsInt(ms))).UTC()
	return t.Format(time.RFC3339Nano)
}

// Time.formatHTTP : Int -> String
// (unixMillis) → HTTP date header format: "Mon, 02 Jan 2006 15:04:05 GMT".
// Use for Last-Modified, Date, Expires headers.
func Time_formatHTTP(ms any) any {
	t := time.UnixMilli(int64(AsInt(ms))).UTC()
	return t.Format(http.TimeFormat)
}

// Time.format : String -> Int -> String
// (goLayout, unixMillis) — emits a custom Go-style layout. Sky exposes the
// Go reference layout "2006-01-02 15:04:05" verbatim. Prefer formatISO8601
// / formatRFC3339 for machine-readable output and this only for UI text.
func Time_format(layout any, ms any) any {
	t := time.UnixMilli(int64(AsInt(ms))).UTC()
	return t.Format(fmt.Sprintf("%v", layout))
}

// Time.parseISO8601 : String -> Result String Int
// Parses an ISO-8601 / RFC 3339 timestamp and returns unix millis.
// Strict: requires the "T" separator and either a "Z" or +hh:mm offset.
func Time_parseISO8601(s any) any {
	str := fmt.Sprintf("%v", s)
	t, err := time.Parse(time.RFC3339Nano, str)
	if err != nil {
		// Try without nanos
		t, err = time.Parse(time.RFC3339, str)
		if err != nil {
			return Err[any, any]("parseISO8601: " + err.Error())
		}
	}
	return Ok[any, any](t.UnixMilli())
}

// Time.parse : String -> String -> Result String Int
// (goLayout, input) — parses using an explicit Go layout string.
func Time_parse(layout any, s any) any {
	t, err := time.Parse(fmt.Sprintf("%v", layout), fmt.Sprintf("%v", s))
	if err != nil {
		return Err[any, any]("time.parse: " + err.Error())
	}
	return Ok[any, any](t.UnixMilli())
}

// Time.addMillis : Int -> Int -> Int
func Time_addMillis(delta any, ms any) any {
	return AsInt(ms) + AsInt(delta)
}

// Time.diffMillis : Int -> Int -> Int
// (later, earlier) — returns later - earlier.
func Time_diffMillis(later any, earlier any) any {
	return AsInt(later) - AsInt(earlier)
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

// Default maximum size for File.readFile (100 MiB). Use File.readFileLimit
// for custom limits. Large files should be streamed with File.openReader.
const defaultFileReadLimit = 100 << 20

// File.readFile : String -> Task String String
// Reads up to 100 MiB (hard default). Returns Err if larger — protects against
// OOMing on an unbounded input. For different limits use readFileLimit.
func File_readFile(path any) any {
	return File_readFileLimit(path, defaultFileReadLimit)
}

// File.readFileLimit : String -> Int -> Task String String
// Reads up to `limit` bytes. Returns Err if the file exceeds that size, or
// if the contents are not valid UTF-8 (callers should use readFileBytes for
// binary data).
func File_readFileLimit(path any, limit any) any {
	return func() any {
		p := fmt.Sprintf("%v", path)
		n := int64(AsInt(limit))
		if n <= 0 {
			n = defaultFileReadLimit
		}
		f, err := os.Open(p)
		if err != nil {
			return Err[any, any](err.Error())
		}
		defer f.Close()
		// Stat first so we can early-reject oversize files without reading them.
		st, err := f.Stat()
		if err != nil {
			return Err[any, any](err.Error())
		}
		if st.Size() > n {
			return Err[any, any](fmt.Sprintf("file exceeds %d-byte limit (actual: %d)", n, st.Size()))
		}
		data, err := io.ReadAll(io.LimitReader(f, n))
		if err != nil {
			return Err[any, any](err.Error())
		}
		return Ok[any, any](string(data))
	}
}

// File.readFileBytes : String -> Task String (List Int)
// Reads up to the default limit as a list of byte values (0..255) — for
// binary data where UTF-8 validity doesn't apply.
func File_readFileBytes(path any) any {
	return func() any {
		f, err := os.Open(fmt.Sprintf("%v", path))
		if err != nil {
			return Err[any, any](err.Error())
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, defaultFileReadLimit))
		if err != nil {
			return Err[any, any](err.Error())
		}
		out := make([]any, len(data))
		for i, b := range data {
			out[i] = int(b)
		}
		return Ok[any, any](out)
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

func Crypto_sha512(s any) any {
	h := sha512.Sum512([]byte(fmt.Sprintf("%v", s)))
	return hex.EncodeToString(h[:])
}

// Crypto.md5 — retained for legacy interoperability only.
// Do not use for security-sensitive hashing: use sha256/sha512 instead.
func Crypto_md5(s any) any {
	h := md5.Sum([]byte(fmt.Sprintf("%v", s)))
	return hex.EncodeToString(h[:])
}

// Crypto.hmacSha256 : String -> String -> String
// (key, message) → hex HMAC. Uses crypto/hmac.
func Crypto_hmacSha256(key any, msg any) any {
	mac := hmac.New(sha256.New, []byte(fmt.Sprintf("%v", key)))
	mac.Write([]byte(fmt.Sprintf("%v", msg)))
	return hex.EncodeToString(mac.Sum(nil))
}

// Crypto.constantTimeEqual : String -> String -> Bool
// Compares two strings in constant time — use when comparing secrets (tokens,
// MACs, password hashes) so attackers can't use timing signals to leak bytes.
// `==` / String equality is NOT safe for this; it short-circuits on first mismatch.
func Crypto_constantTimeEqual(a any, b any) any {
	sa := fmt.Sprintf("%v", a)
	sb := fmt.Sprintf("%v", b)
	return subtle.ConstantTimeCompare([]byte(sa), []byte(sb)) == 1
}

// Crypto.randomBytes : Int -> Task String String
// Returns n cryptographically-secure random bytes, hex-encoded. Use for session
// IDs, tokens, CSRF nonces, password-reset keys, etc.
// Backed by crypto/rand which reads from the OS CSPRNG.
func Crypto_randomBytes(n any) any {
	return func() any {
		size := AsInt(n)
		if size <= 0 || size > 1024 {
			return Err[any, any]("Crypto.randomBytes: size must be 1..1024")
		}
		b := make([]byte, size)
		if _, err := cryptorand.Read(b); err != nil {
			return Err[any, any]("Crypto.randomBytes: " + err.Error())
		}
		return Ok[any, any](hex.EncodeToString(b))
	}
}

// Crypto.randomToken : Int -> Task String String
// Like randomBytes but returns URL-safe base64 (RFC 4648) for use in cookies,
// reset links, etc. Width is in bytes of entropy; the returned string is longer.
func Crypto_randomToken(n any) any {
	return func() any {
		size := AsInt(n)
		if size <= 0 || size > 1024 {
			return Err[any, any]("Crypto.randomToken: size must be 1..1024")
		}
		b := make([]byte, size)
		if _, err := cryptorand.Read(b); err != nil {
			return Err[any, any]("Crypto.randomToken: " + err.Error())
		}
		return Ok[any, any](base64.RawURLEncoding.EncodeToString(b))
	}
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

func List_isEmpty(list any) any {
	items, ok := list.([]any)
	return ok && len(items) == 0 || list == nil
}

// Io_writeString — accepts (text) to stdout OR (writer, text) to the
// supplied io.Writer. Matches both Sky.Core.Io.writeString signatures
// historically used.
func Io_writeString(args ...any) any {
	switch len(args) {
	case 1:
		return func() any {
			fmt.Print(fmt.Sprintf("%v", args[0]))
			return Ok[any, any](struct{}{})
		}
	case 2:
		if w, ok := args[0].(io.Writer); ok {
			_, _ = w.Write([]byte(fmt.Sprintf("%v", args[1])))
			return Ok[any, any](struct{}{})
		}
		fmt.Print(fmt.Sprintf("%v", args[1]))
		return Ok[any, any](struct{}{})
	}
	return Ok[any, any](struct{}{})
}

func List_sort(list any) any {
	items := list.([]any)
	result := make([]any, len(items))
	copy(result, items)
	sort.Slice(result, func(i, j int) bool {
		return fmt.Sprintf("%v", result[i]) < fmt.Sprintf("%v", result[j])
	})
	return result
}

// List_sortBy(keyFn, xs) — stable sort by the `keyFn elem` projection.
// Keys may be Int, Float, String, or anything fmt.Sprintf can format.
func List_sortBy(keyFn any, list any) any {
	items, _ := list.([]any)
	result := make([]any, len(items))
	copy(result, items)
	sort.SliceStable(result, func(i, j int) bool {
		a := SkyCall(keyFn, result[i])
		b := SkyCall(keyFn, result[j])
		return skyLessThan(a, b)
	})
	return result
}

// skyLessThan — generic ordering used by List_sortBy. Treats numeric types
// specially; falls back to lexicographic string compare for everything else.
func skyLessThan(a, b any) bool {
	switch x := a.(type) {
	case int:
		if y, ok := b.(int); ok { return x < y }
	case int64:
		if y, ok := b.(int64); ok { return x < y }
	case float64:
		if y, ok := b.(float64); ok { return x < y }
	case string:
		if y, ok := b.(string); ok { return x < y }
	}
	return fmt.Sprintf("%v", a) < fmt.Sprintf("%v", b)
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
	Cookies map[string]string
	Form    map[string]string
}

// SkyResponse wraps an HTTP response
type SkyResponse struct {
	Status  int
	Body    string
	Headers map[string]string
	ContentType string
}

// HTTP server safety limits.
// These apply to every Sky.Http.Server request. They exist to prevent
// trivial resource-exhaustion DoS. Users can tune per-handler via extractors.
const (
	serverReadHeaderTimeout = 10 * time.Second
	serverReadTimeout       = 30 * time.Second
	serverWriteTimeout      = 30 * time.Second
	serverIdleTimeout       = 120 * time.Second
	serverMaxHeaderBytes    = 1 << 20 // 1 MiB
	serverMaxBodyBytes      = 1 << 25 // 32 MiB; users can override per-handler
)

func Server_listen(port any, routes any) any {
	p := AsInt(port)
	routeList := routes.([]any)
	mux := http.NewServeMux()

	for _, r := range routeList {
		route := r.(SkyRoute)
		handler := route.Handler
		pattern := route.Path

		mux.HandleFunc(pattern, func(w http.ResponseWriter, req *http.Request) {
			// Panic recovery — one bad handler mustn't kill the process.
			defer func() {
				if rec := recover(); rec != nil {
					w.WriteHeader(500)
					fmt.Fprint(w, "Internal Server Error")
				}
			}()
			// Bound body read to prevent memory exhaustion.
			req.Body = http.MaxBytesReader(w, req.Body, serverMaxBodyBytes)

			skyReq := SkyRequest{
				Method:  req.Method,
				Path:    req.URL.Path,
				Headers: make(map[string]any),
				Params:  make(map[string]any),
				Query:   make(map[string]any),
				Cookies: make(map[string]string),
			}
			for _, ck := range req.Cookies() {
				skyReq.Cookies[ck.Name] = ck.Value
			}
			for k, v := range req.Header {
				if len(v) > 0 {
					skyReq.Headers[k] = v[0]
				}
			}
			if req.Body != nil {
				bodyBytes, err := io.ReadAll(req.Body)
				if err != nil {
					w.WriteHeader(413) // Payload Too Large
					fmt.Fprint(w, "request body too large")
					return
				}
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
				// Safe-by-default security headers (callers can override).
				if w.Header().Get("X-Content-Type-Options") == "" {
					w.Header().Set("X-Content-Type-Options", "nosniff")
				}
				if w.Header().Get("X-Frame-Options") == "" {
					w.Header().Set("X-Frame-Options", "SAMEORIGIN")
				}
				if w.Header().Get("Referrer-Policy") == "" {
					w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
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

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", p),
		Handler:           mux,
		ReadHeaderTimeout: serverReadHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       serverIdleTimeout,
		MaxHeaderBytes:    serverMaxHeaderBytes,
	}
	fmt.Printf("Sky server listening on http://localhost:%d\n", p)
	err := srv.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
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

// ═══════════════════════════════════════════════════════════
// Sky.Http.Middleware — handler → handler transformations
// ═══════════════════════════════════════════════════════════

// Middleware.withCors : List String -> Handler -> Handler
// Takes a list of allowed origins ("*" for all) and wraps a handler to
// add Access-Control-Allow-Origin etc. and short-circuit preflights.
func Middleware_withCors(origins any, handler any) any {
	allowed := map[string]bool{}
	allowAll := false
	for _, o := range asList(origins) {
		s := fmt.Sprintf("%v", o)
		if s == "*" {
			allowAll = true
		}
		allowed[s] = true
	}
	return func(req any) any {
		return func() any {
			r, _ := req.(SkyRequest)
			origin := ""
			if o, ok := r.Headers["Origin"]; ok {
				origin = fmt.Sprintf("%v", o)
			}
			allow := ""
			if allowAll {
				allow = "*"
			} else if allowed[origin] {
				allow = origin
			}
			// Preflight
			if r.Method == "OPTIONS" {
				resp := SkyResponse{
					Status:  204,
					Headers: map[string]string{},
				}
				if allow != "" {
					resp.Headers["Access-Control-Allow-Origin"] = allow
					resp.Headers["Access-Control-Allow-Methods"] = "GET, POST, PUT, DELETE, OPTIONS"
					resp.Headers["Access-Control-Allow-Headers"] = "Content-Type, Authorization"
					resp.Headers["Access-Control-Max-Age"] = "3600"
				}
				return Ok[any, any](resp)
			}
			// Delegate to inner handler, then add CORS headers to response.
			task := handler.(func(any) any)(req)
			res := task.(func() any)()
			if sr, ok := res.(SkyResult[any, any]); ok && sr.Tag == 0 {
				if resp, ok := sr.OkValue.(SkyResponse); ok {
					if resp.Headers == nil {
						resp.Headers = map[string]string{}
					}
					if allow != "" {
						resp.Headers["Access-Control-Allow-Origin"] = allow
					}
					return Ok[any, any](resp)
				}
			}
			return res
		}
	}
}

// Middleware.withLogging : Handler -> Handler
// Logs method, path, status, duration for each request.
func Middleware_withLogging(handler any) any {
	return func(req any) any {
		return func() any {
			r, _ := req.(SkyRequest)
			start := time.Now()
			task := handler.(func(any) any)(req)
			res := task.(func() any)()
			status := 0
			if sr, ok := res.(SkyResult[any, any]); ok && sr.Tag == 0 {
				if resp, ok := sr.OkValue.(SkyResponse); ok {
					status = resp.Status
					if status == 0 {
						status = 200
					}
				}
			}
			dur := time.Since(start).Milliseconds()
			ctx := map[string]any{
				"method":  r.Method,
				"path":    r.Path,
				"status":  status,
				"ms":      dur,
			}
			logEmit(logLevelInfo, "info", "http request", ctx)
			return res
		}
	}
}

// Middleware.withBasicAuth : String -> String -> Handler -> Handler
// Wraps a handler with HTTP Basic authentication. user + pass are the
// expected credentials; on mismatch returns 401 with WWW-Authenticate.
// WARNING: requires HTTPS in production — Basic sends credentials in the clear.
func Middleware_withBasicAuth(expectedUser any, expectedPass any, handler any) any {
	eu := fmt.Sprintf("%v", expectedUser)
	ep := fmt.Sprintf("%v", expectedPass)
	return func(req any) any {
		return func() any {
			r, _ := req.(SkyRequest)
			authHeader, _ := r.Headers["Authorization"].(string)
			const prefix = "Basic "
			if !strings.HasPrefix(authHeader, prefix) {
				return Ok[any, any](SkyResponse{
					Status:  401,
					Body:    "authentication required",
					Headers: map[string]string{"WWW-Authenticate": `Basic realm="Sky"`},
				})
			}
			decoded, err := base64.StdEncoding.DecodeString(authHeader[len(prefix):])
			if err != nil {
				return Ok[any, any](SkyResponse{Status: 401, Body: "invalid auth"})
			}
			parts := strings.SplitN(string(decoded), ":", 2)
			if len(parts) != 2 {
				return Ok[any, any](SkyResponse{Status: 401, Body: "invalid auth"})
			}
			// Constant-time compare to avoid timing side channels.
			userOk := subtle.ConstantTimeCompare([]byte(parts[0]), []byte(eu)) == 1
			passOk := subtle.ConstantTimeCompare([]byte(parts[1]), []byte(ep)) == 1
			if !(userOk && passOk) {
				return Ok[any, any](SkyResponse{Status: 401, Body: "bad credentials"})
			}
			task := handler.(func(any) any)(req)
			return task.(func() any)()
		}
	}
}

// Middleware.withRateLimit : String -> Int -> Int -> Handler -> Handler
// (name, capacity, refillPerSec, handler) — applies a per-IP token bucket
// limit using the named RateLimit bucket store. Clients over limit get 429.
func Middleware_withRateLimit(name any, capacity any, refillPerSec any, handler any) any {
	return func(req any) any {
		return func() any {
			r, _ := req.(SkyRequest)
			ip := ""
			// Try X-Forwarded-For first (behind reverse proxy), then Remote.
			if v, ok := r.Headers["X-Forwarded-For"].(string); ok && v != "" {
				if idx := strings.Index(v, ","); idx > 0 {
					ip = strings.TrimSpace(v[:idx])
				} else {
					ip = strings.TrimSpace(v)
				}
			}
			if ip == "" {
				if v, ok := r.Headers["X-Real-Ip"].(string); ok {
					ip = v
				}
			}
			if ip == "" {
				ip = "unknown"
			}
			allowed := RateLimit_allow(name, ip, capacity, refillPerSec).(bool)
			if !allowed {
				return Ok[any, any](SkyResponse{
					Status:  429,
					Body:    "rate limit exceeded",
					Headers: map[string]string{"Retry-After": "1"},
				})
			}
			task := handler.(func(any) any)(req)
			return task.(func() any)()
		}
	}
}

// Server.getCookie : String -> Request -> Maybe String
func Server_getCookie(name any, req any) any {
	r, ok := req.(SkyRequest)
	if !ok {
		return Nothing[any]()
	}
	if r.Cookies == nil {
		return Nothing[any]()
	}
	v, has := r.Cookies[fmt.Sprintf("%v", name)]
	if !has {
		return Nothing[any]()
	}
	return Just[any](v)
}

// SkyCookie — a named cookie value ready to be attached to a response.
type SkyCookie struct {
	Name  string
	Value string
}

// Server.cookie : String -> String -> Cookie
// Build an opaque cookie value (safe HttpOnly + SameSite=Lax defaults).
func Server_cookie(name any, value any) any {
	return SkyCookie{Name: fmt.Sprintf("%v", name), Value: fmt.Sprintf("%v", value)}
}

// Server.withCookie — flexible arity so Sky can pipe either a pre-built
// cookie object or a name/value/attrs triple straight into a response.
// Forms:
//   withCookie(Cookie, Response) -> Response
//   withCookie(name, value, Response) -> Response      (no extra attrs)
//   withCookie(name, value, attrs, Response) -> Response
func Server_withCookie(args ...any) any {
	switch len(args) {
	case 2:
		cookie, resp := args[0], args[1]
		r, ok := resp.(SkyResponse)
		if !ok {
			return resp
		}
		c, cok := cookie.(SkyCookie)
		if !cok {
			return resp
		}
		if r.Headers == nil {
			r.Headers = map[string]string{}
		}
		r.Headers["Set-Cookie"] = fmt.Sprintf("%s=%s; Path=/; HttpOnly; SameSite=Lax", c.Name, c.Value)
		return r
	case 3:
		name, value, resp := args[0], args[1], args[2]
		return setCookieHeader(resp, fmt.Sprintf("%v", name), fmt.Sprintf("%v", value), "Path=/; HttpOnly; SameSite=Lax")
	case 4:
		name, value, attrs, resp := args[0], args[1], args[2], args[3]
		return setCookieHeader(resp, fmt.Sprintf("%v", name), fmt.Sprintf("%v", value), fmt.Sprintf("%v", attrs))
	default:
		return nil
	}
}

func setCookieHeader(resp any, name, value, attrs string) any {
	r, ok := resp.(SkyResponse)
	if !ok {
		return resp
	}
	if r.Headers == nil {
		r.Headers = map[string]string{}
	}
	r.Headers["Set-Cookie"] = fmt.Sprintf("%s=%s; %s", name, value, attrs)
	return r
}

// Server.method : Request -> String   — HTTP method name in upper case.
func Server_method(req any) any {
	if r, ok := req.(SkyRequest); ok {
		return r.Method
	}
	return "GET"
}

// Server.formValue : String -> Request -> String
func Server_formValue(key any, req any) any {
	if r, ok := req.(SkyRequest); ok {
		if r.Form != nil {
			if v, ok2 := r.Form[fmt.Sprintf("%v", key)]; ok2 {
				return v
			}
		}
	}
	return ""
}

// Server.body : Request -> String
func Server_body(req any) any {
	if r, ok := req.(SkyRequest); ok {
		return r.Body
	}
	return ""
}

// Server.path : Request -> String
func Server_path(req any) any {
	if r, ok := req.(SkyRequest); ok {
		return r.Path
	}
	return ""
}

// Server.group : prefix -> routes -> Route
// Prepends prefix to every route's path.
func Server_group(prefix any, routes any) any {
	pStr := fmt.Sprintf("%v", prefix)
	var out []any
	if xs, ok := routes.([]any); ok {
		for _, rt := range xs {
			if sr, ok2 := rt.(SkyRoute); ok2 {
				sr.Path = pStr + sr.Path
				out = append(out, sr)
			} else {
				out = append(out, rt)
			}
		}
	}
	return out
}

// Server.use : middleware -> routes -> routes (identity for now; wiring TBD).
func Server_use(_ any, routes any) any { return routes }

// Server.withHeader : String -> String -> Response -> Response
func Server_withHeader(name any, value any, resp any) any {
	r, ok := resp.(SkyResponse)
	if !ok {
		return resp
	}
	if r.Headers == nil {
		r.Headers = map[string]string{}
	}
	r.Headers[fmt.Sprintf("%v", name)] = fmt.Sprintf("%v", value)
	return r
}

// Server.any : String -> Handler -> Route
// Matches any HTTP method on the given path.
func Server_any(path any, handler any) any {
	return SkyRoute{Method: "*", Path: fmt.Sprintf("%v", path), Handler: handler}
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

// ═══════════════════════════════════════════════════════════
// FFI support — panic recovery + argument coercion helpers
// ═══════════════════════════════════════════════════════════

// SkyFfiRecover installs a deferred recover that converts any Go panic raised
// inside an FFI call into an Err[any,any] written to *out. Generated FFI
// wrappers wire it in as:
//
//     func <K>_foo(args ...) (out any) {
//         defer SkyFfiRecover(&out)()
//         ... actual FFI call ...
//         return Ok[any, any](result)
//     }
//
// `out` is a named return so the deferred closure can reassign it.
func SkyFfiRecover(out *any) func() {
	return func() {
		if r := recover(); r != nil {
			*out = Err[any, any](fmt.Sprintf("panic: %v", r))
		}
	}
}

// SkyFfiArg_string coerces a Sky-side any to a Go string without allocating
// when the value is already a string. Used by generated FFI wrappers.
func SkyFfiArg_string(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// SkyFfiArg_int coerces a Sky-side any to a Go int. Handles the common
// numeric types produced by Sky literals (int, int64, float64).
func SkyFfiArg_int(v any) int {
	return AsInt(v)
}

// SkyFfiArg_bytes coerces a Sky-side any to a Go []byte. Accepts []byte,
// []any (as list of ints), or a string.
func SkyFfiArg_bytes(v any) []byte {
	switch x := v.(type) {
	case []byte:
		return x
	case string:
		return []byte(x)
	case []any:
		out := make([]byte, len(x))
		for i, e := range x {
			out[i] = byte(AsInt(e))
		}
		return out
	}
	return []byte(fmt.Sprintf("%v", v))
}

// SkyFfiRet_bytes wraps a Go []byte as a Sky []any of int codepoints so
// downstream Sky code can inspect it via List operations.
func SkyFfiRet_bytes(b []byte) any {
	out := make([]any, len(b))
	for i, c := range b {
		out[i] = int(c)
	}
	return out
}

// SkyFfiRet_maybeString wraps a *string as Maybe String.
func SkyFfiRet_maybeString(p *string) any {
	if p == nil {
		return Nothing[any]()
	}
	return Just[any](*p)
}

// SkyFfiFieldGet — reflect-based struct-field read, shared by every
// generated <TypeName><FieldName> getter wrapper so the per-field
// emission stays a one-liner (keeps stripe_bindings.go & friends
// manageable in size).
func SkyFfiFieldGet(recv any, field string) any {
	if recv == nil {
		return Err[any, any](field + ": nil receiver")
	}
	v := reflect.ValueOf(recv)
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return Err[any, any](field + ": nil receiver")
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return Err[any, any](field + ": receiver is not a struct")
	}
	f := v.FieldByName(field)
	if !f.IsValid() {
		return Err[any, any](field + ": no such field")
	}
	return f.Interface()
}

// SkyFfiFieldSet — reflect-based struct-field write, returning the
// (mutated or copied) receiver for pipeline-friendly |> composition.
// value is Sky-any; assignable or convertible types coerce automatically.
func SkyFfiFieldSet(value any, recv any, field string) any {
	if recv == nil {
		return Err[any, any](field + ": nil receiver")
	}
	rv := reflect.ValueOf(recv)
	var addrable reflect.Value
	switch rv.Kind() {
	case reflect.Ptr:
		if rv.IsNil() {
			return Err[any, any](field + ": nil receiver")
		}
		addrable = rv.Elem()
	case reflect.Struct:
		tmp := reflect.New(rv.Type())
		tmp.Elem().Set(rv)
		addrable = tmp.Elem()
		rv = tmp
	default:
		return Err[any, any](field + ": receiver is not a struct or pointer")
	}
	if addrable.Kind() != reflect.Struct {
		return Err[any, any](field + ": receiver is not a struct")
	}
	f := addrable.FieldByName(field)
	if !f.IsValid() {
		return Err[any, any](field + ": no such field")
	}
	if !f.CanSet() {
		return Err[any, any](field + ": field is not settable")
	}
	if value == nil {
		f.Set(reflect.Zero(f.Type()))
	} else {
		vv := reflect.ValueOf(value)
		if vv.Type().AssignableTo(f.Type()) {
			f.Set(vv)
		} else if vv.Type().ConvertibleTo(f.Type()) {
			f.Set(vv.Convert(f.Type()))
		} else {
			return Err[any, any](field + ": value type incompatible with field")
		}
	}
	return rv.Interface()
}

// SkyFfiReflectCall invokes a reflect.Value of a function with Sky-side args.
// Used by generated FFI wrappers when the Go signature contains types the
// wrapper cannot spell (internal/vendor pkgs, bare generic T, or methods on
// generic receivers). The reflect.Value is obtained from the caller either
// via `reflect.ValueOf(pkg.Func)` or `reflect.ValueOf(recv).MethodByName(...)`.
//
// hasError:
//   false → wrap pure result in Ok (or bare list for multi-return)
//   true  → last Go return must be error; Ok(prefix)/Err on non-nil
func SkyFfiReflectCall(fn reflect.Value, hasError bool, args []any) any {
	if !fn.IsValid() || fn.Kind() != reflect.Func {
		return Err[any, any]("SkyFfiReflectCall: not a function value")
	}
	ft := fn.Type()
	n := ft.NumIn()
	variadic := ft.IsVariadic()

	// Coerce each Sky-side any to the expected reflect.Type of the Go param.
	vals := make([]reflect.Value, 0, len(args))
	for i, a := range args {
		var pt reflect.Type
		if variadic && i >= n-1 {
			pt = ft.In(n - 1).Elem()
		} else if i < n {
			pt = ft.In(i)
		} else {
			return Err[any, any](fmt.Sprintf("SkyFfiReflectCall: too many args (%d) for %v", len(args), ft))
		}
		if a == nil {
			vals = append(vals, reflect.Zero(pt))
			continue
		}
		v := reflect.ValueOf(a)
		if v.Type() != pt {
			if v.Type().ConvertibleTo(pt) {
				v = v.Convert(pt)
			} else if pt.Kind() == reflect.Interface && v.Type().Implements(pt) {
				// fine — reflect will accept an interface-satisfying value
			}
		}
		vals = append(vals, v)
	}

	// Ensure variadic is invoked correctly when Sky handed us a single slice
	var results []reflect.Value
	if variadic && len(args) == n && vals[n-1].Kind() == reflect.Slice {
		results = fn.CallSlice(vals)
	} else {
		results = fn.Call(vals)
	}

	return unpackReflectResults(results, hasError)
}

func unpackReflectResults(results []reflect.Value, hasError bool) any {
	n := len(results)
	switch {
	case n == 0:
		return Ok[any, any](struct{}{})
	case n == 1 && hasError:
		err, _ := results[0].Interface().(error)
		if err != nil {
			return Err[any, any](err.Error())
		}
		return Ok[any, any](struct{}{})
	case n == 1:
		return results[0].Interface()
	case hasError:
		err, _ := results[n-1].Interface().(error)
		if err != nil {
			return Err[any, any](err.Error())
		}
		if n == 2 {
			return Ok[any, any](results[0].Interface())
		}
		out := make([]any, n-1)
		for i := 0; i < n-1; i++ {
			out[i] = results[i].Interface()
		}
		return Ok[any, any](out)
	default:
		out := make([]any, n)
		for i := 0; i < n; i++ {
			out[i] = results[i].Interface()
		}
		return out
	}
}

// ═══════════════════════════════════════════════════════════
// SkyCall — reflect-based dispatch for any-typed callees
// ═══════════════════════════════════════════════════════════

// SkyCall invokes f with args, where f is any-typed. Used when the codegen
// cannot statically prove the callee is a direct Go func (e.g. lambda params,
// record-field-of-func-type, let-bound closures).
func SkyCall(f any, args ...any) any {
	if f == nil {
		return nil
	}
	rv := reflect.ValueOf(f)
	if rv.Kind() != reflect.Func {
		if len(args) == 0 {
			return f
		}
		return nil
	}
	nin := rv.Type().NumIn()
	if nin == len(args) && !rv.Type().IsVariadic() {
		return skyCallDirect(rv, args)
	}
	if nin == 0 {
		out := rv.Call(nil)
		if len(out) == 0 {
			return nil
		}
		res := out[0].Interface()
		if len(args) == 0 {
			return res
		}
		return SkyCall(res, args...)
	}
	result := f
	for _, a := range args {
		result = skyCallOne(result, a)
	}
	return result
}

func skyCallDirect(rv reflect.Value, args []any) any {
	vals := make([]reflect.Value, len(args))
	for i, a := range args {
		pt := rv.Type().In(i)
		if a == nil {
			vals[i] = reflect.Zero(pt)
			continue
		}
		av := reflect.ValueOf(a)
		if av.Type() == pt {
			vals[i] = av
		} else if av.Type().ConvertibleTo(pt) {
			vals[i] = av.Convert(pt)
		} else {
			vals[i] = av
		}
	}
	out := rv.Call(vals)
	if len(out) == 0 {
		return nil
	}
	return out[0].Interface()
}

func skyCallOne(f any, arg any) any {
	if f == nil {
		return nil
	}
	rv := reflect.ValueOf(f)
	if rv.Kind() != reflect.Func {
		return f
	}
	if rv.Type().NumIn() == 0 {
		out := rv.Call(nil)
		if len(out) == 0 {
			return nil
		}
		return out[0].Interface()
	}
	pt := rv.Type().In(0)
	var av reflect.Value
	if arg == nil {
		av = reflect.Zero(pt)
	} else {
		av = reflect.ValueOf(arg)
		if av.Type() != pt && av.Type().ConvertibleTo(pt) {
			av = av.Convert(pt)
		}
	}
	out := rv.Call([]reflect.Value{av})
	if len(out) == 0 {
		return nil
	}
	return out[0].Interface()
}
