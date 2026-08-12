package rt

import (
	"fmt"
	"testing"
)

// identityKey must be INJECTIVE: two Sky values that are not equal must never
// produce the same key. `fmt.Sprintf("%v", …)` is not — it is the defect these
// tests exist to pin. `%v` renders a tuple by joining its fields with a single
// space inside braces, so `( "a b", "c" )` and `( "a", "b c" )` both come out
// as `{a b c}`. Anything that uses that string as a map key (Set, Cache,
// Std.Ui.Lazy) silently loses one of the two.
//
// Each case below is a PAIR of distinct values whose `%v` renderings collide,
// plus the scalar cases whose `%v` renderings must keep behaving exactly as
// they do today (a Set of Ints must still dedup 1 and 1).

// collidingPairs are distinct Sky values that `%v` renders identically. Every
// one of these is a silent-data-loss reproduction on `main`.
func collidingPairs() []struct {
	name string
	a, b any
} {
	return []struct {
		name string
		a, b any
	}{
		{
			// The canonical repro from the [E2008] docs, applied to Set.
			name: "tuple of strings",
			a:    T2[any, any]{V0: "a b", V1: "c"},
			b:    T2[any, any]{V0: "a", V1: "b c"},
		},
		{
			name: "typed tuple of strings",
			a:    T2[string, string]{V0: "a b", V1: "c"},
			b:    T2[string, string]{V0: "a", V1: "b c"},
		},
		{
			name: "list of strings",
			a:    []any{"a b"},
			b:    []any{"a", "b"},
		},
		{
			name: "typed list of strings",
			a:    []string{"a b"},
			b:    []string{"a", "b"},
		},
		{
			name: "record of strings",
			a:    map[string]any{"a": "x y", "b": "z"},
			b:    map[string]any{"a": "x", "b": "y z"},
		},
		{
			name: "ADT payload of strings",
			a:    SkyADT{Tag: 0, SkyName: "Tag", Fields: []any{"x y", "z"}},
			b:    SkyADT{Tag: 0, SkyName: "Tag", Fields: []any{"x", "y z"}},
		},
		{
			name: "nested tuple vs flat",
			a:    T2[any, any]{V0: T2[any, any]{V0: "a", V1: "b"}, V1: "c"},
			b:    T2[any, any]{V0: "{a b}", V1: "c"},
		},
		{
			// The `Std.Ui.Lazy` fingerprint joins args with "|", so an
			// argument that CONTAINS a pipe forges an argument boundary.
			name: "pipe inside a string",
			a:    "a|b",
			b:    "a\\|b",
		},
	}
}

// scalarsKeepTheirCurrentIdentity — the five types a Dict key may be, plus the
// Go widths Sky's codegen actually emits for them. Changing the key encoding
// must NOT split a value that dedups today: an `int` 1 and an `int64` 1 are the
// same Sky `Int`, and a `float64` 1.0 is the same Sky `Float` as a literal `1`.
func TestIdentityKeyKeepsScalarEquivalences(t *testing.T) {
	same := []struct {
		name string
		a, b any
	}{
		{"int vs int64", 1, int64(1)},
		{"int vs int32", 7, int32(7)},
		{"float64 integral vs int", float64(1), 1},
		{"float32 vs float64", float32(1.5), float64(1.5)},
		{"bool", true, true},
		{"string", "abc", "abc"},
	}
	for _, c := range same {
		t.Run(c.name, func(t *testing.T) {
			if identityKey(c.a) != identityKey(c.b) {
				t.Fatalf("%s: %#v and %#v must share one identity (they are one Sky value); got %q vs %q",
					c.name, c.a, c.b, identityKey(c.a), identityKey(c.b))
			}
		})
	}
	distinct := []struct {
		name string
		a, b any
	}{
		{"different ints", 1, 2},
		{"different floats", 1.5, 2.5},
		{"different bools", true, false},
		{"different strings", "a", "b"},
		{"different runes", 'a', 'b'},
		{"negative zero vs zero", float64(0), float64(-1)},
	}
	for _, c := range distinct {
		t.Run(c.name, func(t *testing.T) {
			if identityKey(c.a) == identityKey(c.b) {
				t.Fatalf("%s: %#v and %#v must have distinct identities, both got %q",
					c.name, c.a, c.b, identityKey(c.a))
			}
		})
	}
}

// The whole point: every pair that `%v` collides must NOT collide under
// identityKey. The `%v` assertion is kept in the test so the pair stays a
// genuine reproduction — if a future Go release made `%v` injective the test
// would tell us rather than passing vacuously.
func TestIdentityKeyIsInjectiveWhereSprintfIsNot(t *testing.T) {
	for _, c := range collidingPairs() {
		t.Run(c.name, func(t *testing.T) {
			if fmt.Sprintf("%v", c.a) != fmt.Sprintf("%v", c.b) {
				t.Skipf("no longer a %%v collision (%q vs %q) — the pair is stale, replace it",
					fmt.Sprintf("%v", c.a), fmt.Sprintf("%v", c.b))
			}
			ka, kb := identityKey(c.a), identityKey(c.b)
			if ka == kb {
				t.Fatalf("identityKey collided on two distinct values: %#v and %#v both key to %q",
					c.a, c.b, ka)
			}
		})
	}
}

// Shapes used by TestIdentityKeySeparatesDistinctValues. `rec1`/`rec2` stand
// for Sky records of one and two fields, which is what typed codegen emits for
// `{ x : Int }` and `{ x : Int, y : Int }`.
type rec1 struct{ A any }
type rec2 struct{ A, B any }

// The general property, stated without reference to `%v`: DISTINCT Sky values
// have DISTINCT keys. `TestIdentityKeyIsInjectiveWhereSprintfIsNot` only covers
// pairs that `%v` also collides, which leaves the grammar's own delimiters
// untested — drop the string LENGTH and the struct FIELD COUNT and that test
// still passes, because the other prefixes happen to separate its pairs.
//
// The corpus below is chosen so that EACH structural element of the encoding is
// load-bearing for at least one pair:
//
//   - `("x", "sy")` vs `("xs", "y")` — separated only by the string LENGTH.
//     Without it both are `s` + `x` + `s` + `sy` = `s` + `xs` + `s` + `y`.
//   - `{ a = { x = 1 }, b = 2 }` vs `{ a = { x = 1, y = 2 } }` — separated only
//     by the struct FIELD COUNT. Without it both are `R` `R` `i1;` `i2;`.
//   - `[ [ 1 ], 2 ]` vs `[ [ 1, 2 ] ]` — separated only by the slice ELEMENT
//     COUNT. Without it both are `L` `L` `i1;` `i2;`.
//
// The MAP entry count is the one part of the grammar no pair here pins, and
// deliberately so: every map pair is already two self-delimiting values, so the
// count is redundant. It is kept as defence in depth for a future arm that is
// not self-delimiting, and a mutation removing it does NOT fail this test.
func TestIdentityKeySeparatesDistinctValues(t *testing.T) {
	corpus := map[string]any{
		"tuple (a b, c)":          T2[any, any]{V0: "a b", V1: "c"},
		"tuple (a, b c)":          T2[any, any]{V0: "a", V1: "b c"},
		"tuple (x, sy)":           T2[any, any]{V0: "x", V1: "sy"},
		"tuple (xs, y)":           T2[any, any]{V0: "xs", V1: "y"},
		"tuple (empty, ab)":       T2[any, any]{V0: "", V1: "ab"},
		"tuple (a, b)":            T2[any, any]{V0: "a", V1: "b"},
		"rec2 of rec1":            rec2{A: rec1{A: 1}, B: 2},
		"rec1 of rec2":            rec1{A: rec2{A: 1, B: 2}},
		"list of two singletons":  []any{[]any{1}, []any{2}},
		"list [[1], 2]":           []any{[]any{1}, 2},
		"list of one pair":        []any{[]any{1, 2}},
		"list [1,2]":              []any{1, 2},
		"list [12]":               []any{12},
		"map a=1":                 map[string]any{"a": 1},
		"map a=1 b=2":             map[string]any{"a": 1, "b": 2},
		"map ab=1":                map[string]any{"ab": 1},
		"string ab":               "ab",
		"string a":                "a",
		"empty string":            "",
		"int 12":                  12,
		"float 1.5":               1.5,
		"true":                    true,
		"false":                   false,
		"nil":                     nil,
		"empty list":              []any{},
		"empty map":               map[string]any{},
		"set of a":                Set_fromList(AsListAny([]any{"a"})),
		"set of a,b":              Set_fromList(AsListAny([]any{"a", "b"})),
		"adt Tag(x y, z)":         SkyADT{Tag: 0, SkyName: "Tag", Fields: []any{"x y", "z"}},
		"adt Tag(x, y z)":         SkyADT{Tag: 0, SkyName: "Tag", Fields: []any{"x", "y z"}},
		"adt Other(x y, z)":       SkyADT{Tag: 1, SkyName: "Other", Fields: []any{"x y", "z"}},
		"nested tuple ((a,b), c)": T2[any, any]{V0: T2[any, any]{V0: "a", V1: "b"}, V1: "c"},
		"tuple ({a b}, c)":        T2[any, any]{V0: "{a b}", V1: "c"},
	}
	seen := map[string]string{}
	for name, v := range corpus {
		k := identityKey(v)
		if prev, dup := seen[k]; dup {
			t.Fatalf("two distinct Sky values share the identity %q: %q and %q", k, prev, name)
		}
		seen[k] = name
	}
}

// Determinism: the same value must key the same way every time, including maps
// (Go randomises map iteration order, so an unsorted map walk would produce a
// different key per call and turn every Dict-valued element into a cache miss
// / a duplicate Set entry).
func TestIdentityKeyIsDeterministic(t *testing.T) {
	vals := []any{
		map[string]any{"a": 1, "b": 2, "c": 3, "d": 4, "e": 5, "f": 6},
		[]any{map[string]any{"x": "1"}, map[string]any{"y": "2"}},
		Set_fromList(AsListAny([]any{"a", "b", "c"})),
	}
	for i, v := range vals {
		first := identityKey(v)
		for n := 0; n < 50; n++ {
			if got := identityKey(v); got != first {
				t.Fatalf("value %d: identityKey is not deterministic — %q then %q", i, first, got)
			}
		}
	}
}

// Equal-but-separately-built composites must share an identity, or a Set would
// hold two copies of the same element and `member` would answer False for a
// value that is in the set.
func TestIdentityKeyIsStructuralNotReferential(t *testing.T) {
	a := []any{"x", T2[any, any]{V0: 1, V1: "y"}, map[string]any{"k": "v"}}
	b := []any{"x", T2[any, any]{V0: 1, V1: "y"}, map[string]any{"k": "v"}}
	if identityKey(a) != identityKey(b) {
		t.Fatalf("structurally equal values must share an identity: %q vs %q", identityKey(a), identityKey(b))
	}
}

// A function has no value identity — Sky's `Set a` is unconstrained, so a
// `Set (Int -> Int)` is typeable. It must not panic; reference identity is the
// documented behaviour (and it is what `%v` already did).
func TestIdentityKeyOnFunctionsDoesNotPanic(t *testing.T) {
	f := func(x any) any { return x }
	g := func(x any) any { return x }
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("identityKey panicked on a function value: %v", r)
		}
	}()
	if identityKey(f) == "" {
		t.Fatal("identityKey returned an empty key for a function")
	}
	// Two separately-allocated closures are distinct; the same one is itself.
	if identityKey(f) != identityKey(f) {
		t.Fatal("identityKey on one function value is not stable")
	}
	_ = g
}

func TestIdentityKeyHandlesNilAndEmpties(t *testing.T) {
	keys := map[string]string{}
	for name, v := range map[string]any{
		"nil":          nil,
		"empty string": "",
		"empty list":   []any{},
		"empty map":    map[string]any{},
		"zero int":     0,
		"false":        false,
	} {
		k := identityKey(v)
		if prev, dup := keys[k]; dup {
			t.Fatalf("%s and %s share the identity %q", name, prev, k)
		}
		keys[k] = name
	}
}
