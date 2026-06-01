package rt

import (
	"sync/atomic"
	"testing"
	"time"
)

// retryAlwaysADT builds a `RetryAlways` ShouldRetry value as the
// runtime sees it post-v0.15.50. The SkyADT layout mirrors what
// codegen emits for the constructor.
func retryAlwaysADT() any {
	return SkyADT{Tag: 0, SkyName: "RetryAlways", Fields: nil}
}

// retryWhenADT wraps a Sky predicate `(e -> Bool)` in the RetryWhen
// constructor.
func retryWhenADT(predicate func(any) any) any {
	return SkyADT{Tag: 1, SkyName: "RetryWhen", Fields: []any{predicate}}
}

// Task.retryWith returns the first successful result and stops calling
// the body after the first Ok.
func TestTaskRetryStopOnOk(t *testing.T) {
	var calls atomic.Int32
	body := func() any {
		n := calls.Add(1)
		if n < 3 {
			return Err[any, any]("transient")
		}
		return Ok[any, any]("done")
	}
	// linearBackoff 5 attempts, 10 ms delay.
	policy := map[string]any{
		"maxAttempts": 5,
		"baseMs":      10,
		"jitter":      false,
		"kind":        0,
		"shouldRetry": retryAlwaysADT(),
	}
	task := Task_retryWith(policy, body)
	res := anyTaskInvoke(task)
	if res.Tag != 0 {
		t.Fatalf("retryWith returned Err on eventual success: %v", res.ErrValue)
	}
	if res.OkValue != "done" {
		t.Fatalf("retryWith returned wrong Ok value: %v", res.OkValue)
	}
	if calls.Load() != 3 {
		t.Errorf("expected 3 body calls, got %d", calls.Load())
	}
}

// Task.retryWith honours maxAttempts and returns the LAST Err when every
// attempt fails.
func TestTaskRetryExhaustsAttempts(t *testing.T) {
	var calls atomic.Int32
	body := func() any {
		calls.Add(1)
		return Err[any, any]("nope")
	}
	policy := map[string]any{
		"maxAttempts": 3,
		"baseMs":      1,
		"jitter":      false,
		"kind":        0,
		"shouldRetry": retryAlwaysADT(),
	}
	task := Task_retryWith(policy, body)
	res := anyTaskInvoke(task)
	if res.Tag != 1 {
		t.Errorf("retryWith returned Ok after exhausting attempts")
	}
	if calls.Load() != 3 {
		t.Errorf("expected 3 body calls, got %d", calls.Load())
	}
}

// shouldRetry returning False short-circuits the retry loop.
func TestTaskRetryShouldRetryFalse(t *testing.T) {
	var calls atomic.Int32
	body := func() any {
		calls.Add(1)
		return Err[any, any]("validation")
	}
	policy := map[string]any{
		"maxAttempts": 5,
		"baseMs":      1,
		"jitter":      false,
		"kind":        0,
		"shouldRetry": retryWhenADT(func(_ any) any { return false }),
	}
	task := Task_retryWith(policy, body)
	res := anyTaskInvoke(task)
	if res.Tag != 1 {
		t.Errorf("retryWith returned Ok on a fail-only body")
	}
	if calls.Load() != 1 {
		t.Errorf("shouldRetry=False should short-circuit at first attempt, got %d calls", calls.Load())
	}
}

// Exponential backoff grows wait between attempts.
func TestTaskRetryExponentialDelay(t *testing.T) {
	d1 := computeDelay(retryKindExponential, 100, 1, false)
	d2 := computeDelay(retryKindExponential, 100, 2, false)
	d3 := computeDelay(retryKindExponential, 100, 3, false)
	if d1 != 100*time.Millisecond {
		t.Errorf("d1: expected 100ms, got %v", d1)
	}
	if d2 != 200*time.Millisecond {
		t.Errorf("d2: expected 200ms, got %v", d2)
	}
	if d3 != 400*time.Millisecond {
		t.Errorf("d3: expected 400ms, got %v", d3)
	}
	// Cap at 30 s.
	dHuge := computeDelay(retryKindExponential, 10000, 20, false)
	if dHuge > 30*time.Second {
		t.Errorf("delay cap missed: got %v", dHuge)
	}
}

// Jitter perturbs the delay but stays within [0.5, 1.5] × base.
func TestTaskRetryJitterBounds(t *testing.T) {
	for i := 0; i < 50; i++ {
		d := computeDelay(retryKindLinear, 100, 1, true)
		if d < 50*time.Millisecond || d > 150*time.Millisecond {
			t.Errorf("jitter out of [50ms, 150ms]: got %v", d)
		}
	}
}
