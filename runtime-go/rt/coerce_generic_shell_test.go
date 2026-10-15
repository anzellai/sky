package rt

import (
	"fmt"
	"strings"
	"testing"
)

// The generic narrowing helpers (Coerce, coerceInner, ResultCoerce,
// MaybeCoerce) are thin generic shells over non-generic reflect steps
// (coerceHead / coerceTail / coerceInnerSlow / reflectResultFields /
// reflectMaybeFields), so the reflect code is compiled once in this package and
// not once per type shape in the app's package. These tests pin every branch
// of the shells: each result, and each panic message the panic classifier
// reads, is the one the single generic body produced. They pass on the old
// generic body too; they guard the split.

type shellPt struct{ X int }

func shellPanic(f func()) (msg string) {
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprint(r)
		}
	}()
	f()
	return ""
}

func TestCoerceShellZeroResults(t *testing.T) {
	// nil into an interface / pointer / slice / map / func target is that
	// target's zero value.
	if got := Coerce[fmt.Stringer](nil); got != nil {
		t.Fatalf("Coerce[Stringer](nil) = %v, want nil", got)
	}
	if got := Coerce[*int](nil); got != nil {
		t.Fatalf("Coerce[*int](nil) = %v, want nil", got)
	}
	if got := Coerce[[]int](nil); got != nil {
		t.Fatalf("Coerce[[]int](nil) = %v, want nil", got)
	}
	if got := Coerce[map[string]int](nil); got != nil {
		t.Fatalf("Coerce[map](nil) = %v, want nil", got)
	}
	// Anything narrows to Unit.
	if got := Coerce[struct{}](42); got != (struct{}{}) {
		t.Fatalf("Coerce[struct{}](42) = %v", got)
	}
}

func TestCoerceShellUnwrapThenAssert(t *testing.T) {
	// A Result/Maybe source into a non-container target is unwrapped; the
	// shell's assertion of the unwrapped value returns it.
	if got := Coerce[int](SkyResult[any, any]{Tag: 0, OkValue: 5}); got != 5 {
		t.Fatalf("Coerce[int](Ok 5) = %v, want 5", got)
	}
	if got := Coerce[string](SkyMaybe[any]{Tag: 0, JustValue: "j"}); got != "j" {
		t.Fatalf("Coerce[string](Just j) = %v, want j", got)
	}
	// Unwrapped but still not T: the tail continues with the unwrapped value
	// (numeric widening here).
	if got := Coerce[int64](SkyResult[any, any]{Tag: 0, OkValue: 7}); got != 7 {
		t.Fatalf("Coerce[int64](Ok 7) = %v, want 7", got)
	}
	// A Result target keeps the container (no unwrap).
	r := Coerce[SkyResult[any, any]](SkyResult[any, any]{Tag: 1, ErrValue: "e"})
	if r.Tag != 1 || r.ErrValue != "e" {
		t.Fatalf("Coerce[SkyResult] = %+v", r)
	}
}

func TestCoerceShellFuncAndTailSteps(t *testing.T) {
	// func value into a differently-typed func slot: the reflect adapter.
	double := Coerce[func(int) int](func(a any) any { return a.(int) * 2 })
	if got := double(21); got != 42 {
		t.Fatalf("adapted func(21) = %v, want 42", got)
	}
	// numeric widening
	if got := Coerce[int64](3); got != 3 {
		t.Fatalf("Coerce[int64](3) = %v", got)
	}
	// []any into []int
	xs := Coerce[[]int]([]any{1, 2})
	if len(xs) != 2 || xs[0] != 1 || xs[1] != 2 {
		t.Fatalf("Coerce[[]int] = %v", xs)
	}
	// map[string]any into a record struct
	p := Coerce[shellPt](map[string]any{"X": 9})
	if p.X != 9 {
		t.Fatalf("Coerce[shellPt](map) = %+v", p)
	}
	// value into a pointer target
	ip := Coerce[*int](4)
	if ip == nil || *ip != 4 {
		t.Fatalf("Coerce[*int](4) = %v", ip)
	}
}

func TestCoerceShellPanicMessage(t *testing.T) {
	msg := shellPanic(func() { _ = Coerce[shellPt]("x") })
	if msg != "rt.Coerce: expected rt.shellPt, got string (x)" {
		t.Fatalf("panic = %q", msg)
	}
	// An interface target names `<nil>`, as `%T` of its zero value does.
	msg = shellPanic(func() { _ = Coerce[fmt.Stringer](3) })
	if msg != "rt.Coerce: expected <nil>, got int (3)" {
		t.Fatalf("interface-target panic = %q", msg)
	}
	if kind, _ := classifyPanic(msg); kind != "CoerceFailure" {
		t.Fatalf("classifyPanic = %q, want CoerceFailure", kind)
	}
}

func TestCoerceInnerShell(t *testing.T) {
	if got := coerceInner[fmt.Stringer](nil); got != nil {
		t.Fatalf("coerceInner[Stringer](nil) = %v", got)
	}
	if got := coerceInner[int](int64(5)); got != 5 {
		t.Fatalf("coerceInner[int](int64 5) = %v", got)
	}
	// A raw string into the Error ADT becomes an Unexpected error.
	e := coerceInner[SkyADT]("boom")
	if e.SkyName != "Error" || len(e.Fields) != 2 {
		t.Fatalf("coerceInner[SkyADT](string) = %+v", e)
	}
	// Maybe reconstruction through the reflect fallback.
	m := coerceInner[SkyMaybe[int]](SkyMaybe[any]{Tag: 0, JustValue: 3})
	if m.Tag != 0 || m.JustValue != 3 {
		t.Fatalf("coerceInner[SkyMaybe[int]] = %+v", m)
	}
	f := coerceInner[func(int) int](func(a any) any { return a.(int) + 1 })
	if f(1) != 2 {
		t.Fatalf("coerceInner func adapter")
	}
	msg := shellPanic(func() { _ = coerceInner[shellPt](3) })
	if !strings.HasPrefix(msg, "rt.coerceInner: type mismatch — source int cannot be cast to target rt.shellPt.") {
		t.Fatalf("panic = %q", msg)
	}
	if kind, _ := classifyPanic(msg); kind != "CoerceFailure" {
		t.Fatalf("classifyPanic = %q, want CoerceFailure", kind)
	}
	// An interface target has no reflect type to name, and the message
	// construction dereferences it: a runtime nil-dereference, as before.
	msg = shellPanic(func() { _ = coerceInner[fmt.Stringer](3) })
	if !strings.Contains(msg, "nil pointer dereference") {
		t.Fatalf("interface-target panic = %q", msg)
	}
}

func TestResultAndMaybeCoerceReflectFallback(t *testing.T) {
	// A SkyResult of another instantiation reaches the reflect fallback.
	r := ResultCoerce[SkyADT, int](SkyResult[string, any]{Tag: 0, OkValue: 7})
	if r.Tag != 0 || r.OkValue != 7 {
		t.Fatalf("ResultCoerce Ok = %+v", r)
	}
	r = ResultCoerce[SkyADT, int](SkyResult[string, any]{Tag: 1, ErrValue: "bad"})
	if r.Tag != 1 || r.ErrValue.SkyName != "Error" {
		t.Fatalf("ResultCoerce Err = %+v", r)
	}
	// A non-Result source is a bare Ok value.
	r = ResultCoerce[SkyADT, int](8)
	if r.Tag != 0 || r.OkValue != 8 {
		t.Fatalf("ResultCoerce bare = %+v", r)
	}
	m := MaybeCoerce[int](SkyMaybe[int64]{Tag: 0, JustValue: 5})
	if m.Tag != 0 || m.JustValue != 5 {
		t.Fatalf("MaybeCoerce Just = %+v", m)
	}
	m = MaybeCoerce[int](SkyMaybe[int64]{Tag: 1})
	if m.Tag != 1 {
		t.Fatalf("MaybeCoerce Nothing = %+v", m)
	}
	m = MaybeCoerce[int]("not a maybe")
	if m.Tag != 1 {
		t.Fatalf("MaybeCoerce non-Maybe = %+v", m)
	}
}
