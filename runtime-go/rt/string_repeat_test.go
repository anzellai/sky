package rt

import "testing"

// String_repeat with a non-positive count must return "" (Elm semantics), NOT
// panic — `strings.Repeat` panics on a negative count, and well-typed Sky must
// never panic (CLAUDE.md §8: no runtime panic from well-typed Sky).
func TestStringRepeat_NonPositiveCountIsEmpty(t *testing.T) {
	for _, n := range []int{-1, -100, 0} {
		if got := String_repeat(n, "ab"); got != "" {
			t.Errorf("String_repeat(%d, \"ab\") = %q, want \"\"", n, got)
		}
	}
	if got := String_repeat(3, "x"); got != "xxx" {
		t.Errorf("String_repeat(3, \"x\") = %q, want \"xxx\"", got)
	}
}
