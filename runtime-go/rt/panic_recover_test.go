// panic_recover_test.go — Cycle 6 PC (v0.15.43) verifies the top-
// level recover catches each "reachable from valid Sky" panic site
// AND produces a structured-log line (not a Go stack dump) with the
// right classification + errId.
//
// Per the audit doc (docs/v0.15.x-hardening/CYCLE-06-PC-panic-site-
// audit.md), 5 panic sites are reachable from valid Sky code:
//
//	rt.IntDiv  / rt.Rem  / rt.Div   — division-class
//	rt.AsInt   / rt.AsFloat / rt.AsBool — narrow-class
//	rt.cmp                               — comparison-class
//	rt.Coerce                            — coerce-class
//	(runtime IndexOutOfRange / NilDereference — caught at Go-runtime level)
//
// Each test triggers the panic + recovers + checks classifyPanic
// produces the right bucket. Symmetric coverage with Tier 1's
// integration spec (PanicRecoverTest.sky).

package rt

import (
	"strings"
	"testing"
)

func TestClassifyPanic_DivisionByZero(t *testing.T) {
	cases := []string{
		"rt.IntDiv: integer division by zero",
		"rt.Rem: modulo by zero",
		"rt.Div: division by zero",
	}
	for _, msg := range cases {
		kind, hint := classifyPanic(msg)
		if kind != "DivisionByZero" {
			t.Errorf("classifyPanic(%q): kind=%q, want DivisionByZero", msg, kind)
		}
		if !strings.Contains(hint, "divisor") {
			t.Errorf("classifyPanic(%q): hint missing divisor guidance: %q", msg, hint)
		}
	}
}

func TestClassifyPanic_TypeMismatch(t *testing.T) {
	cases := []string{
		"rt.AsInt: expected numeric value, got string (foo)",
		"rt.AsFloat: expected numeric value, got []interface {} ([])",
		"rt.AsBool: expected bool, got int (1)",
	}
	for _, msg := range cases {
		kind, _ := classifyPanic(msg)
		if kind != "TypeMismatch" {
			t.Errorf("classifyPanic(%q): kind=%q, want TypeMismatch", msg, kind)
		}
	}
}

func TestClassifyPanic_CoerceFailure(t *testing.T) {
	cases := []string{
		"rt.Coerce: expected int, got string (hello)",
		"rt.coerceInner: type mismatch — source X cannot be cast to target Y",
	}
	for _, msg := range cases {
		kind, _ := classifyPanic(msg)
		if kind != "CoerceFailure" {
			t.Errorf("classifyPanic(%q): kind=%q, want CoerceFailure", msg, kind)
		}
	}
}

func TestClassifyPanic_ComparisonMismatch(t *testing.T) {
	kind, _ := classifyPanic("rt.cmp: type mismatch (left int, right string)")
	if kind != "ComparisonMismatch" {
		t.Errorf("classifyPanic for rt.cmp: kind=%q, want ComparisonMismatch", kind)
	}
}

func TestClassifyPanic_IndexOutOfRange(t *testing.T) {
	kind, hint := classifyPanic("runtime error: index out of range [5] with length 3")
	if kind != "IndexOutOfRange" {
		t.Errorf("classifyPanic for index-out-of-range: kind=%q", kind)
	}
	if !strings.Contains(hint, "List.head") {
		t.Errorf("hint should suggest List.head/List.get: %q", hint)
	}
}

func TestClassifyPanic_NilDereference(t *testing.T) {
	kind, _ := classifyPanic("runtime error: invalid memory address or nil pointer dereference")
	if kind != "NilDereference" {
		t.Errorf("classifyPanic for nil: kind=%q", kind)
	}
}

func TestClassifyPanic_CompilerBug(t *testing.T) {
	cases := []string{
		"sky.Unreachable(case-exhaustiveness): impossible case",
		`Ffi.kernel "Foo" reached the runtime — the build-time call-site rewrite did not fire.`,
	}
	for _, msg := range cases {
		kind, hint := classifyPanic(msg)
		if kind != "CompilerBug" {
			t.Errorf("classifyPanic(%q): kind=%q, want CompilerBug", msg, kind)
		}
		if !strings.Contains(hint, "compiler bug") {
			t.Errorf("hint should mention compiler bug: %q", hint)
		}
	}
}

func TestClassifyPanic_Unexpected(t *testing.T) {
	// An unrecognised panic message falls into Unexpected with a
	// hint pointing to panicMsg/stackFrame fields.
	kind, hint := classifyPanic("something completely unrelated")
	if kind != "Unexpected" {
		t.Errorf("classifyPanic for unknown msg: kind=%q", kind)
	}
	if !strings.Contains(hint, "panicMsg") {
		t.Errorf("Unexpected hint should reference panicMsg field: %q", hint)
	}
}

func TestNewErrId_Shape(t *testing.T) {
	// 4-byte hex → 8 lowercase hex chars.
	id := newErrId()
	if len(id) != 8 {
		t.Errorf("errId length: got %d, want 8", len(id))
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("errId %q contains non-hex char %q", id, c)
		}
	}
}

func TestNewErrId_Unique(t *testing.T) {
	// Cheap collision sanity — two consecutive IDs should differ.
	// 1 / 2^32 chance of false positive, irrelevant for the gate.
	a, b := newErrId(), newErrId()
	if a == b {
		t.Errorf("errId not unique: %q == %q", a, b)
	}
}

func TestCompressStack_DropsNoiseFrames(t *testing.T) {
	// Stack with the typical runtime/debug + panic_recover frames
	// at the top + an application frame at the bottom. compressStack
	// should drop the noise.
	raw := []byte(`goroutine 1 [running]:
runtime/debug.Stack()
	/go/src/runtime/debug/stack.go:24 +0x65
sky-app/rt.LogPanicAndExit()
	/x/panic_recover.go:42 +0x20
panic({0x100, 0x200})
	/go/src/runtime/panic.go:860 +0x12c
sky-app/rt.IntDiv(...)
	/x/rt.go:2181 +0x80
main.main()
	/x/main.go:108 +0x54
`)
	out := compressStack(raw, 8)
	if strings.Contains(out, "runtime/debug") {
		t.Errorf("compressStack should drop runtime/debug frame: %q", out)
	}
	if strings.Contains(out, "LogPanicAndExit") {
		t.Errorf("compressStack should drop LogPanicAndExit frame: %q", out)
	}
	if !strings.Contains(out, "main.main") {
		t.Errorf("compressStack should keep main.main frame: %q", out)
	}
}

// TestLogPanicAndExit_NoOpWhenNoPanic confirms the deferred call
// is harmless on the normal exit path — recover() returns nil,
// the function returns immediately without logging or exiting.
func TestLogPanicAndExit_NoOpWhenNoPanic(t *testing.T) {
	// If LogPanicAndExit called os.Exit on a clean path, this test
	// would never report. It's a smoke test that it returns
	// normally.
	LogPanicAndExit()
}

// TestIntDivByZero_IsTotal confirms integer division / remainder by zero is
// TOTAL — it returns 0 rather than panicking, matching Elm's `//` (`5 // 0 == 0`)
// and Sky's own `modBy 0` (also 0). A well-typed `n // 0` must not crash: integer
// division has a defined, representable total answer, so "if it compiles it
// works" requires 0, not a DivisionByZero panic. (Float `/` — rt.Div — stays a
// loud error because its total answer is ±Infinity, which Sky has no shape for.)
func TestIntDivByZero_IsTotal(t *testing.T) {
	if got := IntDiv(1, 0); AsInt(got) != 0 {
		t.Errorf("IntDiv(1, 0) = %v, want 0 (total, like modBy)", got)
	}
	if got := IntDiv(-7, 0); AsInt(got) != 0 {
		t.Errorf("IntDiv(-7, 0) = %v, want 0", got)
	}
	if got := Rem(9, 0); AsInt(got) != 0 {
		t.Errorf("Rem(9, 0) = %v, want 0 (total, like modBy)", got)
	}
	// Non-zero divisors still compute normally.
	if got := IntDiv(7, 2); AsInt(got) != 3 {
		t.Errorf("IntDiv(7, 2) = %v, want 3", got)
	}
	if got := Rem(7, 3); AsInt(got) != 1 {
		t.Errorf("Rem(7, 3) = %v, want 1", got)
	}
	// Float `/` by zero remains a loud, cleanly-classified error (documented
	// floor: Sky has no ±Infinity shape).
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("rt.Div(1, 0) should still panic (no float Infinity shape)")
			}
			if s, ok := r.(string); ok {
				if kind, _ := classifyPanic(s); kind != "DivisionByZero" {
					t.Errorf("rt.Div panic classify: got %q, want DivisionByZero", kind)
				}
			}
		}()
		_ = Div(1, 0)
	}()
}
