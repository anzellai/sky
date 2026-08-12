package rt

import (
	"reflect"
	"testing"
)

// Sky.Core.Set silently DROPPED elements.
//
//	Set.fromList [ ( "a b", "c" ), ( "a", "b c" ) ]   -- size 1, not 2
//
// `SkySet` keyed its store on `fmt.Sprintf("%v", element)`, which is not
// injective on composites: both tuples render `{a b c}`, so the second
// overwrote the first. No error, no panic — one element of the user's data was
// simply gone, and `member` then answered True for a value the set no longer
// held (it matched the survivor's key) while `toList` returned one element.
//
// This is the same non-injectivity `Dict` has, but `Dict` and `Set` need
// DIFFERENT things from their key encoding: a `Dict` must also DECODE the key
// back out (`toList`/`keys`/`foldl`), which is why `[E2008]` restricts a Dict
// key to five scalar types. A `Set` stores the ORIGINAL element beside the key
// and returns that from `toList`, so it needs injectivity ONLY. That is why the
// fix here is a runtime one — an injective key — rather than a check-time
// rejection: applying `[E2008]`'s five-type rule to `Set` would reject
// `Set ( Int, Int )`, `Set Color`, `Set (Maybe Int)`, `Set { x : Int }` and
// `Set (List Int)`, all of which work correctly today.

// setElems is the observable content of a Set: what `Set.toList` yields.
func setSize(t *testing.T, s any) int {
	t.Helper()
	return AsInt(Set_size(s))
}

// Each case is two DISTINCT Sky values whose `%v` renderings collide. Before
// the fix every one of these produced a set of size 1.
func TestSetKeepsDistinctCompositeElements(t *testing.T) {
	cases := []struct {
		name string
		a, b any
	}{
		{
			name: "tuple of strings (the canonical repro)",
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
			name: "record of strings",
			a:    map[string]any{"a": "x y", "b": "z"},
			b:    map[string]any{"a": "x", "b": "y z"},
		},
		{
			name: "ADT with string payload",
			a:    SkyADT{Tag: 0, SkyName: "Tag", Fields: []any{"x y", "z"}},
			b:    SkyADT{Tag: 0, SkyName: "Tag", Fields: []any{"x", "y z"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := Set_fromList(AsListAny([]any{c.a, c.b}))
			if got := setSize(t, s); got != 2 {
				t.Fatalf("Set.fromList [a, b] dropped an element: size %d, want 2 (a=%#v b=%#v)", got, c.a, c.b)
			}
			if len(AsListAny(Set_toList(s))) != 2 {
				t.Fatalf("Set.toList returned %d elements, want 2", len(AsListAny(Set_toList(s))))
			}
			if !AsBool(Set_member(c.a, s)) {
				t.Fatalf("Set.member said the first element is absent: %#v", c.a)
			}
			if !AsBool(Set_member(c.b, s)) {
				t.Fatalf("Set.member said the second element is absent: %#v", c.b)
			}
			// insert is the other write door into the same map.
			s2 := Set_insert(c.b, Set_fromList(AsListAny([]any{c.a})))
			if got := setSize(t, s2); got != 2 {
				t.Fatalf("Set.insert dropped an element: size %d, want 2", got)
			}
			// remove must take out exactly one of them.
			s3 := Set_remove(c.a, s)
			if got := setSize(t, s3); got != 1 {
				t.Fatalf("Set.remove removed %d elements: size %d, want 1", 2-got, got)
			}
			if !AsBool(Set_member(c.b, s3)) {
				t.Fatalf("Set.remove took out the wrong element")
			}
		})
	}
}

// Set algebra is keyed on the same map, so it collided too: a union of two
// singletons whose elements only "look" equal must have two members, and an
// intersection of two genuinely-different singletons must be empty.
func TestSetAlgebraOnCollidingElements(t *testing.T) {
	a := T2[any, any]{V0: "a b", V1: "c"}
	b := T2[any, any]{V0: "a", V1: "b c"}
	sa := Set_fromList(AsListAny([]any{a}))
	sb := Set_fromList(AsListAny([]any{b}))

	if got := setSize(t, Set_union(sa, sb)); got != 2 {
		t.Fatalf("Set.union of two distinct singletons: size %d, want 2", got)
	}
	if got := setSize(t, Set_intersect(sa, sb)); got != 0 {
		t.Fatalf("Set.intersect of two disjoint singletons: size %d, want 0", got)
	}
	if got := setSize(t, Set_diff(sa, sb)); got != 1 {
		t.Fatalf("Set.diff of two disjoint singletons: size %d, want 1", got)
	}
}

// De-duplication must still WORK — the fix must not turn "one element twice"
// into two. This is the half of the contract an over-eager key would break.
func TestSetStillDeduplicates(t *testing.T) {
	cases := []struct {
		name string
		xs   []any
		want int
	}{
		{"ints", []any{1, 2, 1, 3, 2}, 3},
		{"int widths are one Sky Int", []any{1, int64(1), int32(1)}, 1},
		{"strings", []any{"a", "b", "a"}, 2},
		{"bools", []any{true, false, true}, 2},
		{"floats", []any{1.5, 2.5, 1.5}, 2},
		{"runes", []any{'a', 'b', 'a'}, 2},
		{"equal tuples built separately", []any{
			T2[any, any]{V0: "a b", V1: "c"},
			T2[any, any]{V0: "a b", V1: "c"},
		}, 1},
		{"equal records built separately", []any{
			map[string]any{"a": "x", "b": "y"},
			map[string]any{"b": "y", "a": "x"},
		}, 1},
		{"equal lists built separately", []any{
			[]any{1, 2, 3},
			[]any{1, 2, 3},
		}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := setSize(t, Set_fromList(AsListAny(c.xs))); got != c.want {
				t.Fatalf("Set.fromList %v: size %d, want %d", c.xs, got, c.want)
			}
		})
	}
}

// #461 — a Set that has crossed the typed-codegen boundary arrives as a
// `map[any]bool` and is reified by `toSkySet`. That reification keys the same
// way, so it collided the same way. (The map form cannot itself hold two
// colliding elements — Go's own map key equality is structural — so the defect
// is purely in the reification, and a round trip must preserve the count.)
func TestToSkySetReificationKeepsDistinctElements(t *testing.T) {
	src := map[any]bool{
		T2[string, string]{V0: "a b", V1: "c"}: true,
		T2[string, string]{V0: "a", V1: "b c"}: true,
	}
	got := toSkySet(src)
	if len(got.items) != 2 {
		t.Fatalf("toSkySet(map[any]bool) reified %d of 2 elements — one was dropped: %#v", len(got.items), got.items)
	}
	back, ok := skySetToMap(SkySet{items: got.items}, reflect.TypeOf(map[any]bool{}))
	if !ok {
		t.Fatal("skySetToMap returned ok=false")
	}
	if back.Len() != 2 {
		t.Fatalf("SkySet → map[any]bool round trip lost an element: %d of 2", back.Len())
	}
}
