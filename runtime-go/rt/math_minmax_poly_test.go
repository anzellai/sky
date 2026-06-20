package rt

import "testing"

// Math.min / Math.max are polymorphic (`a -> a -> a`). They must compare by the
// VALUE's type, not by truncating to int — a Float `Math.min` over [0.4..1.3]
// previously collapsed to 0..1 (AsInt), mis-scaling every Std.Ui.Chart.

func TestMathMinMaxFloat(t *testing.T) {
	if got := Math_min(0.4, 1.3); got != 0.4 {
		t.Fatalf("Math_min(0.4, 1.3) = %v, want 0.4", got)
	}
	if got := Math_max(0.4, 1.3); got != 1.3 {
		t.Fatalf("Math_max(0.4, 1.3) = %v, want 1.3", got)
	}
	// Both < 1 — AsInt would tie at 0 and pick wrong; value-compare is correct.
	if got := Math_min(0.6, 0.4); got != 0.4 {
		t.Fatalf("Math_min(0.6, 0.4) = %v, want 0.4 (AsInt-truncation regression)", got)
	}
	if got := Math_max(0.6, 0.4); got != 0.6 {
		t.Fatalf("Math_max(0.6, 0.4) = %v, want 0.6 (AsInt-truncation regression)", got)
	}
}

func TestMathMinMaxInt(t *testing.T) {
	if got := Math_min(3, 7); got != 3 {
		t.Fatalf("Math_min(3, 7) = %v, want 3", got)
	}
	if got := Math_max(3, 7); got != 7 {
		t.Fatalf("Math_max(3, 7) = %v, want 7", got)
	}
}

func TestMathMinMaxString(t *testing.T) {
	if got := Math_min("apple", "banana"); got != "apple" {
		t.Fatalf("Math_min(apple, banana) = %v, want apple", got)
	}
	if got := Math_max("apple", "banana"); got != "banana" {
		t.Fatalf("Math_max(apple, banana) = %v, want banana", got)
	}
}
