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

// callShouldRetry invokes a Sky-side predicate `(e -> Bool)` with the
// surfaced Err value.  Returns true when the predicate value is nil
// (the default-retry case) or a non-callable value (defensive).
func callShouldRetry(fn any, errValue any) bool {
	if fn == nil {
		return true
	}
	// Sky lambdas land as `func(any) any`.
	if predicate, ok := fn.(func(any) any); ok {
		r := predicate(errValue)
		if b, ok := r.(bool); ok {
			return b
		}
		return true
	}
	// Curried wrapper / typed adapter — fall back to skyCallOne.
	r := skyCallOne(fn, errValue)
	if b, ok := r.(bool); ok {
		return b
	}
	return true
}

// Task.retryAlways : any
// Default `shouldRetry` predicate.  Always returns True.
func Task_retryAlways(_ any) any {
	return true
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
