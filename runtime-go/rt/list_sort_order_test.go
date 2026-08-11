package rt

import (
	"fmt"
	"testing"
)

// `List.sort` and `List.sortBy` ordered by the RENDERED form of the element,
// not by the element.
//
// Found by asking Family S's own question of the container modules other than
// `Dict`: is there an element-type x operation crossing that is untested? For
// `List` the answer was the whole ordering surface — `List.sort` / `List.sortBy`
// / `List.sortWith` had ZERO cases in the corpus battery, in any edge class, at
// any element type, and `List.sort` is not in `Sky/Core/List.sky` at all (it is
// on `UNTYPED_KERNEL_MEMBERS`). So nothing anywhere asserted an order.
//
// The defect is the same SHAPE as #174 and for the same reason — a user value
// rendered to a string and then treated as if the string were the value — but
// it damages the ORDER rather than the type:
//
//	List.sort [ 10, 9, 2 ]          gave  10, 2, 9      ("10" < "2" < "9")
//	List.sort [ -1, -20, 3 ]        gave  -1, -20, 3    ("-1" < "-20" < "3")
//	List.sort [ 10.5, 9.5, 2.5 ]    gave  10.5, 2.5, 9.5
//	List.sort [ 'a', '~', 'B' ]     gave  ~, B, a       (rune renders as its
//	                                                     decimal code point:
//	                                                     "126" < "66" < "97")
//	List.sortBy identity [ 'a', '~', 'B' ]   same, via skyLessThan's fallback
//
// `List String` was correct, which is why this survived: the rendered form of a
// string IS the string, exactly as the rendered form of a `String` Dict key was
// the key.
//
// Each subtest below FAILS on the pre-fix runtime and passes after. The fix
// routes both entry points through the ordering dispatch `cmp` already had.

func TestListSortOrdersByValueNotByRendering(t *testing.T) {
	cases := []struct {
		name string
		in   []any
		want []any
	}{
		// Ints whose lexical order differs from their numeric order. This is the
		// reported shape.
		{"int_multi_digit", []any{10, 9, 2}, []any{2, 9, 10}},
		// Negatives: "-1" < "-20" lexically, but -20 < -1.
		{"int_negative", []any{-1, -20, 3}, []any{-20, -1, 3}},
		// int64 is what an FFI / JSON boundary hands back.
		{"int64", []any{int64(10), int64(9), int64(2)}, []any{int64(2), int64(9), int64(10)}},
		// Floats, same class.
		{"float", []any{10.5, 9.5, 2.5}, []any{2.5, 9.5, 10.5}},
		// A `Char` is a Go rune (int32), and `%v` renders a rune as its DECIMAL
		// CODE POINT — so the lexical order of the rendering has nothing to do
		// with the code-point order. 'B'=66, 'a'=97, '~'=126.
		{"char", []any{'a', '~', 'B'}, []any{'B', 'a', '~'}},
		// Strings were always right; they are here so the fix is proven not to
		// have broken the one case that worked.
		{"string", []any{"b", "a", "c"}, []any{"a", "b", "c"}},
		// Elm orders lists of comparables lexicographically, shorter-is-less on
		// a common prefix. `cmp` already implemented this; `List_sort` did not
		// reach it.
		{"list_of_int", []any{[]any{2}, []any{10}, []any{1, 0}}, []any{[]any{1, 0}, []any{2}, []any{10}}},
		// The empty and single-element edges: an ordering primitive that cannot
		// be called must still return the input.
		{"empty", []any{}, []any{}},
		{"single", []any{7}, []any{7}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := asList(List_sort(c.in))
			if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", c.want) {
				t.Fatalf("List_sort(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// `List.sortBy` shares the defect through `skyLessThan`, whose type switch had
// arms for `int` / `int64` / `float64` / `string` and fell through to a rendered
// compare for everything else — which is every `Char`, every tuple, and every
// other integer width.
func TestListSortByOrdersByValueNotByRendering(t *testing.T) {
	id := func(v any) any { return v }
	cases := []struct {
		name string
		in   []any
		want []any
	}{
		// The arms that already existed — pinned so the fix keeps them.
		{"int", []any{10, 9, 2}, []any{2, 9, 10}},
		{"float", []any{10.5, 9.5, 2.5}, []any{2.5, 9.5, 10.5}},
		{"string", []any{"b", "a", "c"}, []any{"a", "b", "c"}},
		// The arms that did not: rune, and the narrower integer widths a typed
		// slice or an FFI boundary produces.
		{"char", []any{'a', '~', 'B'}, []any{'B', 'a', '~'}},
		{"int32", []any{int32(10), int32(9), int32(2)}, []any{int32(2), int32(9), int32(10)}},
		{"int_negative", []any{-1, -20, 3}, []any{-20, -1, 3}},
		// A tuple key: Elm orders tuples of comparables field by field.
		{
			"tuple",
			[]any{T2[any, any]{V0: 10, V1: "a"}, T2[any, any]{V0: 2, V1: "b"}},
			[]any{T2[any, any]{V0: 2, V1: "b"}, T2[any, any]{V0: 10, V1: "a"}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := asList(List_sortBy(id, c.in))
			if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", c.want) {
				t.Fatalf("List_sortBy(id, %v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// The ordering must be a total, consistent order, or `sort.Slice` is entitled to
// produce anything at all. `cmp` panics on a pair it cannot order — which is
// right for `<` in well-typed Sky, and wrong for `List.sort`, which the checker
// does not constrain to `comparable` (it has no `.sky` signature). So the sort
// path must DEGRADE to the rendered compare rather than panic, and this test is
// what pins that: a list of values `cmp` cannot order must still come back
// sorted somehow, and must not take the process down.
func TestListSortDoesNotPanicOnUnorderablePairs(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("List_sort panicked on an unorderable pair: %v", r)
		}
	}()
	// `Bool` is not `comparable` in Elm and `AsInt(true)` panics, so this pair
	// reaches the fallback.
	if got := asList(List_sort([]any{true, false})); len(got) != 2 {
		t.Fatalf("List_sort([true false]) lost an element: %v", got)
	}
	// Mixed kinds — reachable from an `any`-typed FFI return.
	if got := asList(List_sort([]any{1, "a"})); len(got) != 2 {
		t.Fatalf("List_sort([1 a]) lost an element: %v", got)
	}
	if got := asList(List_sortBy(func(v any) any { return v }, []any{true, false})); len(got) != 2 {
		t.Fatalf("List_sortBy lost an element: %v", got)
	}
}

// `cmpSafe` is the one dispatch behind both policies, so its two callers must
// agree wherever it succeeds: a pair `cmp` orders must be ordered the same way
// by `skyLessThan`. Before the fix there were three implementations of "which
// of these two comes first" and two of them disagreed with the third.
func TestCmpAndSkyLessThanAgree(t *testing.T) {
	pairs := [][2]any{
		{2, 10}, {10, 2}, {-20, -1}, {2.5, 10.5}, {'B', 'a'}, {'a', '~'},
		{"a", "b"}, {int64(2), int64(10)}, {int32(2), int32(10)},
		{[]any{1, 0}, []any{2}},
		{T2[any, any]{V0: 2, V1: "b"}, T2[any, any]{V0: 10, V1: "a"}},
	}
	for _, p := range pairs {
		c := cmp(p[0], p[1])
		less := skyLessThan(p[0], p[1])
		if (c < 0) != less {
			t.Fatalf("cmp(%v, %v) = %d but skyLessThan = %v — the two orderings disagree",
				p[0], p[1], c, less)
		}
	}
}
