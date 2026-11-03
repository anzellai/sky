package rt

import (
	"math"
	"testing"
)

// A Sky `number` reaches Go as an `int` when an integer literal fills a
// `number` slot whose caller chose Float (`zero : number -> number; zero _ =
// 0`). `==` must compare it with the float64 by value, at the top level and
// inside every container (v0.29.2: each of these answered False).
func TestEqIntAndFloatAreOneNumber(t *testing.T) {
	type rec struct{ X, Y any }
	cases := []struct {
		name string
		a, b any
		want bool
	}{
		{"int 0 vs 0.0", 0, 0.0, true},
		{"0.0 vs int 0", 0.0, 0, true},
		{"int 1 vs 1.0", 1, 1.0, true},
		{"int 1 vs 1.5", 1, 1.5, false},
		{"int -3 vs -3.0", -3, -3.0, true},
		{"int64 vs int", int64(7), 7, true},
		{"int vs NaN", 0, math.NaN(), false},
		{"int vs +Inf", math.MaxInt64, math.Inf(1), false},
		{"2^53+1 vs 2^53", (1 << 53) + 1, float64(1 << 53), false},
		{"Just int vs Just float", Just[any](1), Just[float64](1.0), true},
		{"Just int vs Just other", Just[any](1), Just[float64](2.0), false},
		{"tuple", T2[any, any]{V0: 1.5, V1: 0}, T2[float64, float64]{V0: 1.5, V1: 0.0}, true},
		{"list", []any{1, 2.5}, []float64{1.0, 2.5}, true},
		{"record", rec{X: 0, Y: 1.5}, rec{X: 0.0, Y: 1.5}, true},
		{"string vs int stays stringly", "1", 1, true},
		{"bool vs int", true, 1, false},
	}
	for _, c := range cases {
		if got := Eq(c.a, c.b).(bool); got != c.want {
			t.Errorf("%s: Eq(%v, %v) = %v, want %v", c.name, c.a, c.b, got, c.want)
		}
		if got := NotEq(c.a, c.b).(bool); got == c.want {
			t.Errorf("%s: NotEq(%v, %v) = %v, want %v", c.name, c.a, c.b, got, !c.want)
		}
	}
}

// List.member goes through the same equality.
func TestListMemberIntInFloatList(t *testing.T) {
	if !AsBool(List_member(1.0, []any{1, 2.5})) {
		t.Fatal("List.member 1.0 [1, 2.5] (the 1 an int literal in a number slot) = False")
	}
}
