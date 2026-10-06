package rt

// v0.27.7: `==` between two NULLARY constructors of a type that also has a
// constructor with fields returned True. `type T = A | B | C Int` lowers to a
// sealed interface with one Go struct per constructor; deepEq's
// "fields-by-name fallback" compared `A{}` and `B{}` as two structs with no
// fields and never looked at the constructor. identityKey (Set / Cache /
// Std.Ui.Lazy) had the same fault: every nullary variant keyed `R0;`.
// union_value.go is the one recogniser all three paths now share.

import (
	"testing"
)

type eqA struct{}

func (eqA) SkyVariantTag() int     { return 0 }
func (eqA) SkyVariantName() string { return "A" }

type eqB struct{}

func (eqB) SkyVariantTag() int     { return 1 }
func (eqB) SkyVariantName() string { return "B" }

type eqC struct{ V0 int }

func (eqC) SkyVariantTag() int     { return 2 }
func (eqC) SkyVariantName() string { return "C" }

type eqD struct{ V0 int }

func (eqD) SkyVariantTag() int     { return 3 }
func (eqD) SkyVariantName() string { return "D" }

// A generic custom type: `type Box a = Empty | Full a`, instantiated twice.
type eqEmpty[A any] struct{}

func (eqEmpty[A]) SkyVariantTag() int     { return 0 }
func (eqEmpty[A]) SkyVariantName() string { return "Empty" }

type eqFull[A any] struct{ V0 A }

func (eqFull[A]) SkyVariantTag() int     { return 1 }
func (eqFull[A]) SkyVariantName() string { return "Full" }

// A record that happens to have fields named like the legacy union shape.
type eqTagRecord struct {
	Tag    int
	Fields []any
	Name   string
}

type eqHolder struct {
	Kind  any
	Count int
}

func TestUnionEqComparesTheConstructor(t *testing.T) {
	cases := []struct {
		name string
		a, b any
		want bool
	}{
		{"A == B", eqA{}, eqB{}, false},
		{"B == A", eqB{}, eqA{}, false},
		{"A == A", eqA{}, eqA{}, true},
		{"C 1 == C 1", eqC{1}, eqC{1}, true},
		{"C 1 == C 2", eqC{1}, eqC{2}, false},
		{"C 1 == D 1", eqC{1}, eqD{1}, false},
		{"A == C 0", eqA{}, eqC{0}, false},
		{"C 0 == A", eqC{0}, eqA{}, false},
		{"record holding A == record holding B", eqHolder{eqA{}, 1}, eqHolder{eqB{}, 1}, false},
		{"record holding A == record holding A", eqHolder{eqA{}, 1}, eqHolder{eqA{}, 1}, true},
		{"[A, C 1] == [B, C 1]", []any{eqA{}, eqC{1}}, []any{eqB{}, eqC{1}}, false},
		{"[A, C 1] == [A, C 1]", []any{eqA{}, eqC{1}}, []any{eqA{}, eqC{1}}, true},
		{"Just A == Just B", Just[any](eqA{}), Just[any](eqB{}), false},
		{"Just A == Just A", Just[any](eqA{}), Just[any](eqA{}), true},
		{"Ok A == Ok B", Ok[any, any](eqA{}), Ok[any, any](eqB{}), false},
		{"Err A == Ok A", Err[any, any](eqA{}), Ok[any, any](eqA{}), false},
		{"(A, 1) == (B, 1)", T2[any, int]{V0: eqA{}, V1: 1}, T2[any, int]{V0: eqB{}, V1: 1}, false},
		{"Empty[int] == Empty[any]", eqEmpty[int]{}, eqEmpty[any]{}, true},
		{"Empty == Full 0", eqEmpty[int]{}, eqFull[int]{0}, false},
		{"Full[int] 1 == Full[any] 1", eqFull[int]{1}, eqFull[any]{1}, true},
		{"Full 1 == Full 2", eqFull[int]{1}, eqFull[int]{2}, false},
		{"legacy A == legacy B", SkyADT{Tag: 0, SkyName: "A"}, SkyADT{Tag: 1, SkyName: "B"}, false},
		{"legacy A == legacy A", SkyADT{Tag: 0, SkyName: "A"}, SkyADT{Tag: 0, SkyName: "A"}, true},
		{"legacy A == legacy A (nil vs empty Fields)", SkyADT{Tag: 0, SkyName: "A"}, SkyADT{Tag: 0, SkyName: "A", Fields: []any{}}, true},
		{"sealed A == legacy A", eqA{}, SkyADT{Tag: 0, SkyName: "A"}, true},
		{"sealed A == legacy B", eqA{}, SkyADT{Tag: 1, SkyName: "B"}, false},
		{"Nothing[any] == Nothing[string]", Nothing[any](), Nothing[string](), true},
	}
	for _, c := range cases {
		if got := AsBool(Eq(c.a, c.b)); got != c.want {
			t.Errorf("%s: Eq = %v, want %v", c.name, got, c.want)
		}
		if got := AsBool(NotEq(c.a, c.b)); got == c.want {
			t.Errorf("%s: NotEq = %v, want %v", c.name, got, !c.want)
		}
	}
}

// A record whose fields are named `Tag` and `Fields` is still a record: all of
// its fields count, for == and for ordering.
func TestRecordWithTagFieldIsNotAUnion(t *testing.T) {
	a := eqTagRecord{Tag: 0, Fields: []any{1}, Name: "x"}
	b := eqTagRecord{Tag: 0, Fields: []any{1}, Name: "y"}
	if AsBool(Eq(a, b)) {
		t.Error("records differing in Name compared equal")
	}
	if c, ok := cmpSafe(a, b); !ok || c >= 0 {
		t.Errorf("cmpSafe(x, y) = (%d, %v), want (<0, true)", c, ok)
	}
}

func TestUnionIdentityKeySeparatesConstructors(t *testing.T) {
	distinct := [][2]any{
		{eqA{}, eqB{}},
		{eqC{1}, eqD{1}},
		{eqA{}, eqC{0}},
		{eqEmpty[int]{}, eqFull[int]{0}},
		{SkyADT{Tag: 0, SkyName: "A"}, SkyADT{Tag: 1, SkyName: "B"}},
		{eqHolder{eqA{}, 1}, eqHolder{eqB{}, 1}},
	}
	for _, p := range distinct {
		if identityKey(p[0]) == identityKey(p[1]) {
			t.Errorf("identityKey(%#v) == identityKey(%#v) = %q", p[0], p[1], identityKey(p[0]))
		}
	}
	same := [][2]any{
		{eqA{}, eqA{}},
		{eqEmpty[int]{}, eqEmpty[any]{}},
		{eqFull[int]{1}, eqFull[any]{1}},
		{Nothing[any](), Nothing[string]()},
	}
	for _, p := range same {
		if identityKey(p[0]) != identityKey(p[1]) {
			t.Errorf("identityKey(%#v) = %q, identityKey(%#v) = %q; want equal",
				p[0], identityKey(p[0]), p[1], identityKey(p[1]))
		}
	}

	s := Set_fromList([]any{eqA{}, eqB{}, eqC{1}, eqD{1}, eqA{}})
	if n := AsInt(Set_size(s)); n != 4 {
		t.Errorf("Set.fromList [A, B, C 1, D 1, A] has size %d, want 4", n)
	}
	if !AsBool(Set_member(eqB{}, Set_fromList([]any{eqB{}}))) {
		t.Error("Set.member B [B] = False")
	}
	if AsBool(Set_member(eqB{}, Set_fromList([]any{eqA{}}))) {
		t.Error("Set.member B [A] = True")
	}
}

func TestUnionMemberAndCompare(t *testing.T) {
	if AsBool(List_member(eqB{}, []any{eqA{}, eqC{1}})) {
		t.Error("List.member B [A, C 1] = True")
	}
	if !AsBool(List_member(eqC{1}, []any{eqA{}, eqC{1}})) {
		t.Error("List.member (C 1) [A, C 1] = False")
	}
	if c := cmp(eqA{}, eqB{}); c != -1 {
		t.Errorf("compare A B = %d, want -1", c)
	}
	if c := cmp(eqC{2}, eqC{1}); c != 1 {
		t.Errorf("compare (C 2) (C 1) = %d, want 1", c)
	}
	if c := cmp(eqA{}, eqA{}); c != 0 {
		t.Errorf("compare A A = %d, want 0", c)
	}
}
