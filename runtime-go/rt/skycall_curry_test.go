package rt

// Regression tests for curry-on-undersaturation in skyCallOne. Sky
// semantics curry every function, but the typed codegen emits multi-
// arg Go funcs directly (`func myFn(int, string) any`). Higher-order
// combinators in the runtime (List.indexedMap, List.foldl, Cmd.perform,
// etc.) drive the call one arg at a time via skyCallOne — which used
// to panic with "reflect: Call with too few input arguments" when the
// callee was a top-level multi-arg binding rather than a single-arg
// curried lambda.
//
// Repro from sendcrafts: `List.indexedMap uploadedImageTile xs`
// panicked at runtime; user worked around with `\i u -> uploadedImageTile i u`.
//
// Fix: skyCallOne now detects NumIn() > 1 and returns a curried
// closure (curryRemainingArgs) that captures partial args until the
// arity is satisfied, then dispatches via skyCallDirect.

import (
	"testing"
)

// TestSkyCallOne_CurriesTwoArgGoFn — the exact shape that triggered
// the user's panic: 2-arg top-level Go function passed by reference,
// invoked one arg at a time.
func TestSkyCallOne_CurriesTwoArgGoFn(t *testing.T) {
	twoArg := func(i int, s string) string {
		return s + ":" + intToStr(i)
	}
	// First call peels off the int — should return a func(any) any
	// closure, NOT panic.
	stage1 := skyCallOne(any(twoArg), 7)
	if stage1 == nil {
		t.Fatal("skyCallOne(twoArg, 7) returned nil; expected curried closure")
	}
	// Second call peels off the string — should invoke the underlying
	// function and return the result.
	out := skyCallOne(stage1, "hello")
	got, ok := out.(string)
	if !ok {
		t.Fatalf("expected string result, got %T %v", out, out)
	}
	if got != "hello:7" {
		t.Errorf("want 'hello:7', got %q", got)
	}
}

// TestSkyCallOne_CurriesThreeArgGoFn — same shape but three params.
// Each skyCallOne should return a closure until the last arg lands.
func TestSkyCallOne_CurriesThreeArgGoFn(t *testing.T) {
	threeArg := func(a int, b int, c int) int {
		return a*100 + b*10 + c
	}
	s1 := skyCallOne(any(threeArg), 1)
	s2 := skyCallOne(s1, 2)
	out := skyCallOne(s2, 3)
	if out != 123 {
		t.Errorf("curried 3-arg call: want 123, got %v", out)
	}
}

// TestSkyCallOne_FullySaturatedSingleArgUnchanged — the existing
// happy path (single-arg Go func, single arg) must still work
// unchanged. Catches regressions where the new curry branch
// accidentally short-circuits the legacy path.
func TestSkyCallOne_FullySaturatedSingleArgUnchanged(t *testing.T) {
	oneArg := func(s string) string { return "<" + s + ">" }
	out := skyCallOne(any(oneArg), "x")
	if out != "<x>" {
		t.Errorf("single-arg call regressed: got %v, want <x>", out)
	}
}

// TestSkyCallOne_CurriedFnFlowsThroughSkyCall — once curryRemainingArgs
// returns a closure, downstream SkyCall(closure, moreArgs...) must
// keep working. List.indexedMap typically goes through SkyCall with
// the iteration value.
func TestSkyCallOne_CurriedFnFlowsThroughSkyCall(t *testing.T) {
	twoArg := func(i int, label string) string {
		return label + "#" + intToStr(i)
	}
	stage1 := skyCallOne(any(twoArg), 42)
	out := SkyCall(stage1, "row")
	if out != "row#42" {
		t.Errorf("SkyCall on curried closure: got %v, want row#42", out)
	}
}

// intToStr — local helper to avoid pulling in strconv just for the
// test fixtures (matches the style of other rt/*_test.go files).
func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}
