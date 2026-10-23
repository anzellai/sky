package rt

import "testing"

// Elm's `toFloat : Int -> Float` had no Sky equivalent. Both entry points
// (the erased one and the typed twin the lowerer picks for a proven `int`)
// must agree.
func TestBasicsToFloat(t *testing.T) {
	for _, n := range []int{0, 3, -7, 1 << 53} {
		want := float64(n)
		if got := Basics_toFloatT(n); got != want {
			t.Fatalf("Basics_toFloatT(%d) = %v, want %v", n, got, want)
		}
		if got, ok := Basics_toFloat(n).(float64); !ok || got != want {
			t.Fatalf("Basics_toFloat(%d) = %#v, want %v", n, Basics_toFloat(n), want)
		}
	}
}
