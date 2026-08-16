package rt

import (
	"strconv"
	"testing"
)

// The typed twins exist so a PROVEN `Sky.Core.List.foldl` / `.any` call site can
// be re-targeted away from the erased pure-Sky def. A twin that is not the exact
// semantic equal of what it replaces is a miscompile, not an optimisation — so
// every test here is DIFFERENTIAL: the typed helper is checked against the
// erased kernel it stands in for, on the same input, rather than against a
// hand-written expectation that could drift with it.
//
// The accumulators are deliberately NON-COMMUTATIVE (string concatenation,
// subtraction). A commutative accumulator such as sum would pass with the fold
// running in either direction, which is the one property most at risk here:
// `List_foldlT` takes `func(B, A) B` (accumulator first) and Sky's `foldl fn acc
// list` applies `fn x acc` (element first), so argument order and iteration
// order are exactly what a mistake would get wrong.

func erasedFoldl(fn func(any, any) any, seed any, xs []int) any {
	anyXs := make([]any, len(xs))
	for i, v := range xs {
		anyXs[i] = v
	}
	return List_foldl(fn, seed, anyXs)
}

func TestFoldlElemFirstT_agreesWithErasedKernel(t *testing.T) {
	xs := []int{1, 2, 3, 4}

	typed := List_foldlElemFirstT(func(x int, acc string) string {
		return acc + strconv.Itoa(x)
	}, "", xs)

	erased := erasedFoldl(func(x any, acc any) any {
		return acc.(string) + strconv.Itoa(x.(int))
	}, "", xs)

	// Pinned literal as well as the differential: if BOTH paths were reversed
	// together the differential alone would still pass.
	if typed != "1234" {
		t.Fatalf("List_foldlElemFirstT: got %q, want %q (left fold, element first)", typed, "1234")
	}
	if typed != erased {
		t.Fatalf("typed twin disagrees with erased kernel: typed=%q erased=%v", typed, erased)
	}
}

// Direction, pinned on a numeric accumulator as well as the string one above,
// so the property does not rest on a single test.
//
// The first draft of this used `acc - x` over [1,2,3] expecting -6. That is a
// USELESS assertion: subtracting every element from 0 gives -(1+2+3) whichever
// end you start from, so it passes with the fold running backwards. It was
// caught by mutating the helper to iterate in reverse and finding this test
// still green while the concatenation test went red. `acc*10 + x` is
// order-sensitive: 123 left-to-right, 321 reversed.
//
// Argument ORDER needs no test — `fn` is `func(A, B) B`, so applying it as
// `fn(acc, x)` is a Go type error the compiler rejects. Direction is the only
// half that can go wrong silently.
func TestFoldlElemFirstT_foldsLeftToRight(t *testing.T) {
	got := List_foldlElemFirstT(func(x int, acc int) int { return acc*10 + x }, 0, []int{1, 2, 3})
	if got != 123 {
		t.Fatalf("fold direction wrong: got %d, want 123 (left to right; 321 means reversed)", got)
	}
}

func TestFoldlElemFirstT_emptyListReturnsSeed(t *testing.T) {
	// `foldl fn acc [] = acc` — the pure-Sky base case.
	got := List_foldlElemFirstT(func(x int, acc string) string { return acc + "x" }, "seed", nil)
	if got != "seed" {
		t.Fatalf("empty list: got %q, want %q", got, "seed")
	}
	erased := erasedFoldl(func(x any, acc any) any { return acc }, "seed", nil)
	if got != erased {
		t.Fatalf("empty list disagrees with erased kernel: typed=%q erased=%v", got, erased)
	}
}

func TestAnyT_agreesWithErasedKernel(t *testing.T) {
	cases := []struct {
		name string
		xs   []int
		want bool
	}{
		{"hit in middle", []int{1, 2, 3, 4}, true},
		{"no hit", []int{1, 3, 5}, false},
		{"empty", nil, false},
		{"hit at head", []int{2, 3, 5}, true},
		{"hit at tail", []int{1, 3, 4}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			typed := List_anyT(func(x int) bool { return x%2 == 0 }, c.xs)
			if typed != c.want {
				t.Fatalf("List_anyT: got %v, want %v", typed, c.want)
			}
			anyXs := make([]any, len(c.xs))
			for i, v := range c.xs {
				anyXs[i] = v
			}
			erased := List_any(func(x any) any { return x.(int)%2 == 0 }, anyXs)
			if erased != any(c.want) {
				t.Fatalf("erased List_any: got %v, want %v", erased, c.want)
			}
		})
	}
}

// `any pred list` must stop at the first True — the pure-Sky def returns
// immediately (`if pred x then True`). A twin that evaluates the whole list is
// observably different for a predicate with side effects or a partial one.
func TestAnyT_shortCircuitsOnFirstTrue(t *testing.T) {
	calls := 0
	got := List_anyT(func(x int) bool { calls++; return x == 2 }, []int{1, 2, 3, 4, 5})
	if !got {
		t.Fatal("List_anyT: got false, want true")
	}
	if calls != 2 {
		t.Fatalf("List_anyT evaluated %d elements, want 2 (must short-circuit)", calls)
	}
}
