package rt

// Regression gate: a FUNCTION-typed value that crossed an `any`-typed kernel
// boundary must still be callable where codegen declared a concrete function
// parameter.
//
// This is the defect behind "Std.Config applicative record decoding panics at
// runtime". `Config.succeed <curried ctor> |> Config.andThen (\f -> Config.map
// f <field>)` — the shape Std/Config.sky's OWN module docstring documents —
// died with:
//
//	rt.skyCallDirect: argument 0 type mismatch — function expects
//	func(int, string) main.Main_DbCfg_R, got func(interface {}) interface {}
//
// Both sides are right on their own terms. The Config kernels take and return
// `any`, so the partially-applied record constructor reaches the next stage as
// the runtime's generic curried closure. Meanwhile HM DID infer `f`'s type, so
// codegen compiled the continuation with a concrete multi-argument parameter.
// `skyCallDirect` matched the two structurally, found func != func, and
// panicked — a well-typed Sky program failing at runtime.
//
// The gate is written against SkyCall rather than through Std.Config so it
// pins the general contract: this bridge is what any kernel carrying a
// function-typed type variable depends on, not just Config.

import "testing"

type cfgProbeRec struct {
	Host string
	Port int
}

// The exact shape from the panic: a curried `func(any) any` chain handed to a
// parameter typed as a single multi-argument Go func.
func TestSkyCall_AdaptsGenericCurriedFuncToConcreteMultiArgParam(t *testing.T) {
	// What the any-typed kernels carry: partial application produced by the
	// runtime's own currying, so every layer is func(any) any.
	generic := func(a any) any {
		return func(b any) any {
			return cfgProbeRec{Host: AsString(a), Port: AsIntOrZero(b)}
		}
	}
	// What codegen emits for the continuation, having inferred f concretely.
	continuation := func(f func(string, int) cfgProbeRec) cfgProbeRec {
		return f("localhost", 5432)
	}
	out := SkyCall(continuation, generic)
	got, ok := out.(cfgProbeRec)
	if !ok {
		t.Fatalf("result type: got %T want cfgProbeRec", out)
	}
	if got.Host != "localhost" || got.Port != 5432 {
		t.Errorf("got %+v want {localhost 5432}", got)
	}
}

// The single-argument form, which is what a two-field record decoder produces.
func TestSkyCall_AdaptsGenericFuncToConcreteSingleArgParam(t *testing.T) {
	generic := func(a any) any { return AsIntOrZero(a) * 2 }
	continuation := func(f func(int) int) int { return f(21) }
	if got := AsIntOrZero(SkyCall(continuation, generic)); got != 42 {
		t.Errorf("got %d want 42", got)
	}
}

// A concrete func argument that already matches must not be re-wrapped — the
// adaptation is a fallback, not a new default path.
func TestSkyCall_ConcreteFuncArgStillPassesThrough(t *testing.T) {
	exact := func(s string) int { return len(s) }
	continuation := func(f func(string) int) int { return f("abcd") }
	if got := AsIntOrZero(SkyCall(continuation, exact)); got != 4 {
		t.Errorf("got %d want 4", got)
	}
}

// A genuinely wrong argument must still fail loudly. The fix must not turn
// skyCallDirect into a coerce-anything path.
func TestSkyCall_NonFuncArgToFuncParamStillPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected a panic when a non-function is passed to a " +
				"function parameter; the adaptation must not swallow real mismatches")
		}
	}()
	continuation := func(f func(int) int) int { return f(1) }
	SkyCall(continuation, "not a function")
}
