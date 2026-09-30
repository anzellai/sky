package rt

import (
	"errors"
	"math"
	"strings"
	"testing"
)

type ffiThing struct{ N int }

type ffiDur int64

// ffiGuarded runs f under the format-3 wrapper guard and returns its Result.
func ffiGuarded[A any](f func() A) (out SkyResult[any, A]) {
	defer SkyFfiGuardT(&out)()
	out = Ok[any, A](f())
	return
}

func ffiErrText(t *testing.T, r SkyResult[any, any]) string {
	t.Helper()
	if r.Tag != 1 {
		t.Fatalf("want Err, got Ok %#v", r.OkValue)
	}
	return Basics_errorToStringT(r.ErrValue)
}

// C-4: a pointer to a primitive is a Maybe in both directions.
func TestFfiPointerIsMaybe(t *testing.T) {
	s := "x"
	if got := FfiRet(&s); got != any(Just[any]("x")) {
		t.Fatalf("*string non-nil: %#v", got)
	}
	var nilp *string
	if got := FfiRet(nilp); got != any(Nothing[any]()) {
		t.Fatalf("*string nil: %#v", got)
	}
	if p := FfiArg[*string](Just[string]("y")); p == nil || *p != "y" {
		t.Fatalf("Just y → %#v", p)
	}
	if p := FfiArg[*string](Nothing[string]()); p != nil {
		t.Fatalf("Nothing → %#v", p)
	}
	one := 1
	got := FfiRet([]*int{&one, nil}).([]any)
	if got[0] != any(Just[any](1)) || got[1] != any(Nothing[any]()) {
		t.Fatalf("[]*int: %#v", got)
	}
}

// C-5: a non-string map key is kept, keyed the way Sky keys a Dict.
func TestFfiIntKeyedMap(t *testing.T) {
	m := FfiRet(map[int]string{1: "a", 2: "b"}).(map[string]any)
	if len(m) != 2 || m[encodeDictKey(1)] != "a" || m[encodeDictKey(2)] != "b" {
		t.Fatalf("map[int]string → %#v", m)
	}
	back := FfiArg[map[int]string](m)
	if back[1] != "a" || back[2] != "b" {
		t.Fatalf("round trip: %#v", back)
	}
	// An untagged key (a Dict built by Go) still reads.
	if got := FfiArg[map[int]string](map[string]any{"7": "z"}); got[7] != "z" {
		t.Fatalf("plain key: %#v", got)
	}
}

// C-7: integers are range-checked in both directions.
func TestFfiIntegerRange(t *testing.T) {
	r := ffiGuarded(func() any { return FfiRet(uint64(math.MaxUint64)) })
	if msg := ffiErrText(t, r); !strings.Contains(msg, "out of range for Int") || strings.Contains(msg, "panic:") || !strings.Contains(msg, "see docs/migration/v0.27.md#ffi-integer-range") {
		t.Fatalf("uint64 max: %q", msg)
	}
	if got := FfiRet(uint64(7)); got != any(7) {
		t.Fatalf("uint64 7: %#v", got)
	}
	for _, c := range []struct {
		f    func() any
		want string
	}{
		{func() any { return FfiArg[uint8](300) }, "300 is out of range for uint8"},
		{func() any { return FfiArg[uint64](-1) }, "-1 is out of range for uint64"},
		{func() any { return FfiArg[int8](-129) }, "-129 is out of range for int8"},
		{func() any { return FfiArg[int32](int(math.MaxInt32) + 1) }, "out of range for int32"},
		{func() any { return FfiArg[float32](1e300) }, "out of range for float32"},
	} {
		if msg := ffiErrText(t, ffiGuarded(c.f)); !strings.Contains(msg, c.want) {
			t.Errorf("want %q in %q", c.want, msg)
		}
	}
	if got := FfiArg[uint8](200); got != 200 {
		t.Fatalf("uint8 200: %v", got)
	}
	if got := FfiArg[ffiDur](5); got != 5 {
		t.Fatalf("named int: %v", got)
	}
	if got := FfiRet(ffiDur(9)); got != any(9) {
		t.Fatalf("named int ret: %#v", got)
	}
}

// C-6: an opaque value of the wrong Go type is an Err, never a crash; T and
// *T adapt to each other.
func TestFfiOpaque(t *testing.T) {
	th := &ffiThing{N: 3}
	if got := FfiRet(th); got != any(th) {
		t.Fatalf("opaque pointer must pass through: %#v", got)
	}
	if got := FfiArg[*ffiThing](th); got != th {
		t.Fatal("same type")
	}
	if got := FfiArg[ffiThing](th); got.N != 3 {
		t.Fatal("*T → T")
	}
	if got := FfiArg[*ffiThing](ffiThing{N: 4}); got == nil || got.N != 4 {
		t.Fatal("T → *T")
	}
	msg := ffiErrText(t, ffiGuarded(func() any { return FfiArg[*ffiThing]("str") }))
	if !strings.Contains(msg, "expected *rt.ffiThing, got string") {
		t.Fatalf("mismatch: %q", msg)
	}
	msg = ffiErrText(t, ffiGuarded(func() any { return FfiArg[error](th) }))
	if !strings.Contains(msg, "does not implement error") {
		t.Fatalf("interface mismatch: %q", msg)
	}
	// An empty interface accepts anything.
	if got := FfiArg[any]("s"); got != "s" {
		t.Fatal("empty interface")
	}
}

func TestFfiBytesAndArrays(t *testing.T) {
	if got := FfiRet([]byte("hi")); got != any("hi") {
		t.Fatalf("[]byte: %#v", got)
	}
	var a [4]byte
	copy(a[:], "abcd")
	if got := FfiRet(a); got != any("abcd") {
		t.Fatalf("[4]byte: %#v", got)
	}
	if got := FfiArg[[4]byte]("wxyz"); string(got[:]) != "wxyz" {
		t.Fatalf("→ [4]byte: %v", got)
	}
	msg := ffiErrText(t, ffiGuarded(func() any { return FfiArg[[4]byte]("abc") }))
	if !strings.Contains(msg, "needs exactly 4 bytes") {
		t.Fatalf("length: %q", msg)
	}
	if got := FfiArg[[2]int]([]any{1, 2}); got != [2]int{1, 2} {
		t.Fatalf("[2]int: %v", got)
	}
	if got := FfiRet([]string{"a"}); len(got.([]string)) != 1 {
		t.Fatalf("[]string keeps its shape: %#v", got)
	}
}

// C-12 / R3: a Sky function adapts to a Go callback, its arguments and
// result converted.
func TestFfiCallbacks(t *testing.T) {
	cb := FfiArg[func(*string) string](func(m SkyMaybe[string]) string {
		if m.Tag == 0 {
			return m.JustValue + "!"
		}
		return "none"
	})
	s := "y"
	if got := cb(&s) + cb(nil); got != "y!none" {
		t.Fatalf("pointer callback: %q", got)
	}
	unit := 0
	f0 := FfiArg[func()](func(_ struct{}) struct{} { unit++; return struct{}{} })
	f0()
	f0()
	if unit != 2 {
		t.Fatalf("zero-parameter callback ran %d times", unit)
	}
	big := FfiArg[func(uint64) string](func(n int) string { return "unreached" })
	msg := ffiErrText(t, ffiGuarded(func() any { return big(math.MaxUint64) }))
	if !strings.Contains(msg, "out of range for Int") {
		t.Fatalf("callback argument range: %q", msg)
	}
	withErr := FfiArg[func(int) error](func(n int) SkyResult[any, struct{}] {
		if n > 0 {
			return Err[any, struct{}](ErrFfi("neg"))
		}
		return Ok[any, struct{}](struct{}{})
	})
	if withErr(0) != nil || withErr(1) == nil {
		t.Fatal("error-returning callback")
	}
	narrow := FfiArg[func(int) int8](func(n int) int { return n * 100 })
	msg = ffiErrText(t, ffiGuarded(func() any { return narrow(9) }))
	if !strings.Contains(msg, "out of range for int8") {
		t.Fatalf("callback result range: %q", msg)
	}
}

func TestFfiReflectCallShapes(t *testing.T) {
	lookup := func(k string) (int, bool) { return 1, k == "a" }
	r := SkyFfiReflectCall3(ReflectValueOfAny(lookup), false, []any{"a"}).(SkyResult[any, any])
	if r.Tag != 0 || r.OkValue != any(Just[any](1)) {
		t.Fatalf("comma-ok: %#v", r)
	}
	fails := func() (uint64, error) { return 0, errors.New("boom") }
	r = SkyFfiReflectCall3(ReflectValueOfAny(fails), true, []any{struct{}{}}).(SkyResult[any, any])
	if r.Tag != 1 {
		t.Fatalf("error result: %#v", r)
	}
	takes := func(x uint8) int { return int(x) }
	r = SkyFfiReflectCall3(ReflectValueOfAny(takes), false, []any{300}).(SkyResult[any, any])
	if r.Tag != 1 || !strings.Contains(Basics_errorToStringT(r.ErrValue), "out of range") {
		t.Fatalf("reflect arg range: %#v", r)
	}
}

func TestFfiFieldAccess(t *testing.T) {
	type rec struct {
		P *string
		U uint64
	}
	s := "v"
	got := SkyFfiFieldGet3(&rec{P: &s}, "P").(SkyResult[any, any])
	if got.OkValue != any(Just[any]("v")) {
		t.Fatalf("pointer field: %#v", got)
	}
	got = SkyFfiFieldGet3(&rec{U: math.MaxUint64}, "U").(SkyResult[any, any])
	if got.Tag != 1 {
		t.Fatalf("out-of-range field must be Err: %#v", got)
	}
	var nilRec *rec
	if r := SkyFfiFieldGet3(nilRec, "P").(SkyResult[any, any]); r.Tag != 1 {
		t.Fatal("nil receiver")
	}
	r := &rec{}
	set := SkyFfiFieldSet3(Just[any]("w"), r, "P").(SkyResult[any, any])
	if set.Tag != 0 || r.P == nil || *r.P != "w" {
		t.Fatalf("set pointer field: %#v", set)
	}
	if bad := SkyFfiFieldSet3(-1, r, "U").(SkyResult[any, any]); bad.Tag != 1 {
		t.Fatal("negative into uint64 must be Err")
	}
}
