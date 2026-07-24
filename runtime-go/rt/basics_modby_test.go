package rt

import "testing"

// modBy is Elm's FLOORED modulo: the result's sign follows the divisor, unlike
// Go's `%` truncated remainder (whose sign follows the dividend). The any-dispatch
// Basics_modBy must share this with Basics_modByT — a prior version returned the
// raw `AsInt(n) % d`, so `modBy 3 -1` gave Go's -1 instead of Elm's 2.
func TestBasicsModByFloored(t *testing.T) {
	cases := []struct {
		divisor, n, want int
	}{
		{3, -1, 2},
		{3, -7, 2},
		{3, 7, 1},
		{5, 12, 2},
		{4, -1, 3},
		{4, 0, 0},
		{0, 5, 0}, // divisor 0 → 0 (no panic)
	}
	for _, c := range cases {
		if got := Basics_modBy(any(c.divisor), any(c.n)); got != any(c.want) {
			t.Fatalf("Basics_modBy(%d, %d) = %v, want %d", c.divisor, c.n, got, c.want)
		}
		if got := Basics_modByT(c.divisor, c.n); got != c.want {
			t.Fatalf("Basics_modByT(%d, %d) = %d, want %d", c.divisor, c.n, got, c.want)
		}
	}
}
