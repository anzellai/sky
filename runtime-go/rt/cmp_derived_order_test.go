package rt

import (
	"fmt"
	"math"
	"testing"
)

// Sealed variants as typed codegen emits them (v0.17): one Go struct per
// constructor, the tag from the SkyVariant interface. Declaration order:
// ordRed (0), ordGreen (1), ordBlue (2).
type ordRed struct{}

func (ordRed) SkyVariantTag() int     { return 0 }
func (ordRed) SkyVariantName() string { return "Red" }

type ordGreen struct{ V0 int }

func (ordGreen) SkyVariantTag() int     { return 1 }
func (ordGreen) SkyVariantName() string { return "Green" }

type ordBlue struct {
	V0 string
	V1 int
}

func (ordBlue) SkyVariantTag() int     { return 2 }
func (ordBlue) SkyVariantName() string { return "Blue" }

type ordRec struct {
	Zeta  int
	Alpha string
}

func cmpOf(t *testing.T, a, b any) int {
	t.Helper()
	c, ok := cmpSafe(a, b)
	if !ok {
		t.Fatalf("cmpSafe(%#v, %#v) cannot order the pair", a, b)
	}
	return c
}

// TestCompare_DerivedOrderOnUnions is the C-11 runtime regression. Two sealed
// variants are different Go structs, and the struct compare walked their
// fields positionally: `compare Red (Green 1)` was 0 (Red has no fields)
// while `Red == Green 1` was False, `List.sort [Green 3, Red, Green 1]` came
// back unchanged and a Set of unions iterated in an arbitrary order. The
// order is now derived as in Haskell and Elm-with-comparable: constructor
// declaration order first, then the payload left to right.
func TestCompare_DerivedOrderOnUnions(t *testing.T) {
	cases := []struct {
		a, b any
		want int
	}{
		{ordRed{}, ordGreen{1}, -1},
		{ordGreen{1}, ordRed{}, 1},
		{ordGreen{1}, ordGreen{3}, -1},
		{ordGreen{3}, ordGreen{3}, 0},
		{ordGreen{9}, ordBlue{"a", 0}, -1},
		{ordBlue{"a", 5}, ordBlue{"b", 0}, -1},
		{ordBlue{"a", 5}, ordBlue{"a", 2}, 1},
		// The legacy SkyADT shape orders the same way.
		{SkyADT{Tag: 0, SkyName: "Red"}, SkyADT{Tag: 1, SkyName: "Green", Fields: []any{1}}, -1},
		{SkyADT{Tag: 1, SkyName: "Green", Fields: []any{4}}, SkyADT{Tag: 1, SkyName: "Green", Fields: []any{2}}, 1},
		// Maybe / Result: the payload that the tag selects, never a zero one.
		{Just[any](1), Just[any](2), -1},
		{Just[any](5), Nothing[any](), -1},
		{Nothing[any](), Nothing[any](), 0},
		{Err[any, any]("a"), Err[any, any]("b"), -1},
		{Ok[any, any](7), Err[any, any]("a"), -1},
	}
	for _, c := range cases {
		if got := cmpOf(t, c.a, c.b); got != c.want {
			t.Errorf("compare %#v %#v = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := Basics_compare(c.a, c.b); got != c.want {
			t.Errorf("Basics_compare %#v %#v = %v, want %d", c.a, c.b, got, c.want)
		}
		// compare agrees with ==.
		if (c.want == 0) != (Eq(c.a, c.b) == true) {
			t.Errorf("compare and == disagree on %#v and %#v", c.a, c.b)
		}
	}
	sorted := List_sort([]any{ordGreen{3}, ordRed{}, ordBlue{"x", 1}, ordGreen{1}}).([]any)
	want := []any{ordRed{}, ordGreen{1}, ordGreen{3}, ordBlue{"x", 1}}
	if fmt.Sprint(sorted) != fmt.Sprint(want) {
		t.Errorf("List.sort = %v, want %v", sorted, want)
	}
	if got := Math_max(ordRed{}, ordGreen{9}); got != (ordGreen{9}) {
		t.Errorf("Math.max Red (Green 9) = %v", got)
	}
	set := Set_toList(Set_fromList([]any{ordBlue{"a", 1}, ordRed{}, ordGreen{2}, ordGreen{1}})).([]any)
	wantSet := []any{ordRed{}, ordGreen{1}, ordGreen{2}, ordBlue{"a", 1}}
	if fmt.Sprint(set) != fmt.Sprint(wantSet) {
		t.Errorf("Set.toList = %v, want %v", set, wantSet)
	}
}

// TestCompare_RecordsByFieldName: a record compares field by field in the
// order of the field NAMES, whatever its Go field order, and an erased record
// (a map) the same way.
func TestCompare_RecordsByFieldName(t *testing.T) {
	// Alpha decides before Zeta, although Zeta is declared first.
	if got := cmpOf(t, ordRec{Zeta: 9, Alpha: "a"}, ordRec{Zeta: 1, Alpha: "b"}); got != -1 {
		t.Errorf("record compare = %d, want -1 (alpha first)", got)
	}
	a := map[string]any{"zeta": 9, "alpha": "a"}
	b := map[string]any{"zeta": 1, "alpha": "b"}
	if got := cmpOf(t, a, b); got != -1 {
		t.Errorf("erased record compare = %d, want -1", got)
	}
	if _, ok := cmpSafe(map[string]any{"a": 1}, map[string]any{"b": 1}); ok {
		t.Error("records with different fields were ordered")
	}
}

// TestCompare_NaNPolicy: `compare` is a total order, -Inf < … < +Inf < NaN
// and `compare nan nan == EQ`, so a sort, a Set, min and max are
// deterministic over NaN. `==`, `<`, `>`, `<=`, `>=` stay IEEE: every one of
// them is False when an operand is NaN.
func TestCompare_NaNPolicy(t *testing.T) {
	nan, inf := math.NaN(), math.Inf(1)
	total := []struct {
		a, b any
		want int
	}{
		{nan, nan, 0},
		{1.0, nan, -1},
		{nan, 1.0, 1},
		{inf, nan, -1},
		{math.Inf(-1), 1.0, -1},
		{nan, 3, 1},
	}
	for _, c := range total {
		if got := cmpOf(t, c.a, c.b); got != c.want {
			t.Errorf("compare %v %v = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	for _, op := range []struct {
		name string
		f    func(a, b any) any
	}{{"<", Lt}, {">", Gt}, {"<=", Lte}, {">=", Gte}} {
		for _, p := range [][2]any{{nan, 1.0}, {1.0, nan}, {nan, nan}} {
			if op.f(p[0], p[1]) != false {
				t.Errorf("%v %s %v is not False", p[0], op.name, p[1])
			}
		}
	}
	if Eq(nan, nan) != false {
		t.Error("nan == nan is not False")
	}
	sorted := List_sort([]any{nan, 2.0, math.Inf(-1), 1.0}).([]any)
	if fmt.Sprint(sorted) != fmt.Sprint([]any{math.Inf(-1), 1.0, 2.0, nan}) {
		t.Errorf("List.sort with NaN = %v", sorted)
	}
}
