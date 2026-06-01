// task_retry.go — v0.15.44 Task.retryWith combinator.
//
// Runs a Task (any thunk shape: `func() any` or SkyTask[any,any]) up to
// `maxAttempts` times, sleeping the policy-specified backoff between
// attempts.  Returns the first Ok result, or the last Err if every
// attempt failed.
//
// `shouldRetry` is consulted on each Err before the next attempt — a
// `False` short-circuits the loop and returns the current Err
// immediately.  Defaults to "always retry" (any err triggers another
// attempt up to maxAttempts).
//
// Jitter uses math/rand (NOT crypto/rand — retry-spread doesn't need
// cryptographic randomness).
package rt

import (
	"fmt"
	"math"
	mrand "math/rand"
	"reflect"
	"time"
)

const (
	retryKindLinear      = 0
	retryKindExponential = 1
	retryDelayCapMs      = 30_000
)

// readRetryPolicy unpacks a Sky-side RetryPolicy record.  Accepts the
// typed Go struct (PascalCase fields) and the map-based fallback
// (camelCase keys) — same shape as recordField in stdlib_extra.go.
func readRetryPolicy(p any) (maxAttempts, baseMs, kind int, jitter bool, shouldRetry any) {
	maxAttempts = AsInt(recordField(p, "MaxAttempts", "maxAttempts"))
	baseMs = AsInt(recordField(p, "BaseMs", "baseMs"))
	kind = AsInt(recordField(p, "Kind", "kind"))
	jitter, _ = recordField(p, "Jitter", "jitter").(bool)
	shouldRetry = recordField(p, "ShouldRetry", "shouldRetry")
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	if baseMs < 0 {
		baseMs = 0
	}
	return
}

// computeDelay returns the wait between attempt n (1-indexed: attempt 1
// runs first, then we sleep computeDelay(1), then attempt 2, etc.)
// according to the policy.  Exponential growth is capped at 30 s.
// Jitter multiplies by a uniform random in [0.5, 1.5].
func computeDelay(kind, baseMs, attempt int, jitter bool) time.Duration {
	d := baseMs
	if kind == retryKindExponential {
		// baseMs * 2^(attempt-1).  Guard against overflow on huge
		// attempt counts.
		if attempt <= 30 {
			d = baseMs * (1 << (attempt - 1))
		} else {
			d = retryDelayCapMs
		}
	}
	if d > retryDelayCapMs {
		d = retryDelayCapMs
	}
	if jitter && d > 0 {
		// Uniform in [0.5*d, 1.5*d].
		factor := 0.5 + mrand.Float64()
		d = int(math.Round(float64(d) * factor))
		if d > retryDelayCapMs {
			d = retryDelayCapMs
		}
	}
	if d < 0 {
		d = 0
	}
	return time.Duration(d) * time.Millisecond
}

// callShouldRetry decides whether to retry given the policy's
// `shouldRetry` field.  v0.15.50: the field is now the `ShouldRetry e`
// ADT (RetryAlways | RetryWhen (e -> Bool)) instead of the previous
// `any` value.  We switch on the constructor tag — cheaper than the
// reflect-backed callable detection and exhaustiveness-checked at the
// Sky source level.
//
// Defensive defaults: unknown ctor name OR malformed RetryWhen payload
// → retry (safer than dropping the err).
func callShouldRetry(fn any, errValue any) bool {
	if fn == nil {
		return true
	}
	name, fields := readShouldRetry(fn)
	switch name {
	case "RetryAlways":
		return true
	case "RetryWhen":
		if len(fields) != 1 {
			return true
		}
		inner := fields[0]
		if predicate, ok := inner.(func(any) any); ok {
			r := predicate(errValue)
			if b, ok := r.(bool); ok {
				return b
			}
			return true
		}
		r := skyCallOne(inner, errValue)
		if b, ok := r.(bool); ok {
			return b
		}
		return true
	default:
		// Unknown ctor (forward-compat extension) — retry safely.
		return true
	}
}

// readShouldRetry pulls (ctorName, args) off a ShouldRetry value.
// Two shapes accepted:
//   1. SkyADT (runtime-constructed values; codegen sets SkyName).
//   2. Any reflect-readable struct exposing SkyName + Fields fields
//      (the typed Go struct shape codegen emits for user-declared ADTs).
// Numeric-Tag fallback uses the Sky-source declaration order:
// RetryAlways = 0, RetryWhen = 1.
func readShouldRetry(v any) (string, []any) {
	if v == nil {
		return "", nil
	}
	if a, ok := v.(SkyADT); ok {
		if a.SkyName != "" {
			return a.SkyName, a.Fields
		}
		return shouldRetryNameForTag(a.Tag), a.Fields
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return "", nil
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() || rv.Kind() != reflect.Struct {
		return "", nil
	}
	skyName := ""
	if f := rv.FieldByName("SkyName"); f.IsValid() && f.Kind() == reflect.String {
		skyName = f.String()
	}
	var fields []any
	if f := rv.FieldByName("Fields"); f.IsValid() && f.Kind() == reflect.Slice {
		fields = make([]any, f.Len())
		for i := 0; i < f.Len(); i++ {
			fields[i] = f.Index(i).Interface()
		}
	}
	if skyName == "" {
		if f := rv.FieldByName("Tag"); f.IsValid() && f.CanInt() {
			skyName = shouldRetryNameForTag(int(f.Int()))
		}
	}
	return skyName, fields
}

func shouldRetryNameForTag(tag int) string {
	switch tag {
	case 0:
		return "RetryAlways"
	case 1:
		return "RetryWhen"
	}
	return ""
}

// Task.retryWith : RetryPolicy -> Task e a -> Task e a
// Returns a NEW Task that drives the body up to maxAttempts times.
func Task_retryWith(policy any, task any) any {
	maxAttempts, baseMs, kind, jitter, shouldRetry := readRetryPolicy(policy)
	return func() any {
		var last any
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			res := anyTaskInvoke(task)
			// anyTaskInvoke yields SkyResult[any, any]. Tag 0 = Ok.
			if res.Tag == 0 {
				return Ok[any, any](res.OkValue)
			}
			last = Err[any, any](res.ErrValue)
			if attempt >= maxAttempts {
				break
			}
			if !callShouldRetry(shouldRetry, res.ErrValue) {
				break
			}
			delay := computeDelay(kind, baseMs, attempt, jitter)
			if delay > 0 {
				time.Sleep(delay)
			}
		}
		return last
	}
}

// Ensure we don't have unused-import warnings if math/fmt are
// referenced only conditionally above.
var _ = fmt.Sprint
