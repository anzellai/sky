package rt

import "testing"

// Elm's `comparable` includes tuples and lists of comparables, ordered
// lexicographically. Before this, cmp only handled scalars and fell through to
// AsInt on a struct/slice — a runtime panic on well-typed `(1,2) < (1,3)`.
func TestCmpCompositeLexicographic(t *testing.T) {
	tup := func(xs ...any) T2[any, any] {
		// A 2-tuple is enough to exercise the struct path; build via T2.
		return T2[any, any]{V0: xs[0], V1: xs[1]}
	}
	cases := []struct {
		name string
		a, b any
		want int
	}{
		{"tuple first-field less", tup(1, 2), tup(1, 3), -1},
		{"tuple first-field greater", tup(2, 0), tup(1, 9), 1},
		{"tuple equal", tup(1, 2), tup(1, 2), 0},
		{"tuple string then int", tup("a", 1), tup("a", 2), -1},
		{"list element less", []any{1, 2, 3}, []any{1, 3}, -1},
		{"list prefix shorter-is-less", []any{1, 2}, []any{1, 2, 0}, -1},
		{"list equal", []any{1, 2}, []any{1, 2}, 0},
		{"list longer-is-greater", []any{1, 2, 0}, []any{1, 2}, 1},
		{"nested list of tuples", []any{tup(1, 1)}, []any{tup(1, 2)}, -1},
	}
	for _, c := range cases {
		if got := cmp(c.a, c.b); got != c.want {
			t.Errorf("%s: cmp = %d, want %d", c.name, got, c.want)
		}
	}
}
