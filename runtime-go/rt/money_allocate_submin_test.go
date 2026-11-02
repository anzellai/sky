package rt

import (
	"math/rand"
	"testing"

	"github.com/shopspring/decimal"
)

// v0.29.2 seventh Judge: `Money.allocate 3 (1.005 USD)` gave
// [0.34, 0.33, 0.33], which sums to 1.00: the digits below the minor unit
// were truncated away. Such amounts come from Money.mul, percentOf and
// convert. The rule now: every part is a whole number of minor units, as
// even as possible, except that the digits below the minor unit go to the
// FIRST part, so the parts sum exactly to the input.
func TestMoney_Allocate_SubMinorAmount(t *testing.T) {
	in := decimal.RequireFromString("1.005")
	parts := invokeMoney(t, "Money_allocate", 2, 3, decimalBox(in)).([]any)
	want := []string{"0.345", "0.33", "0.33"}
	sum := decimal.Zero
	for i, p := range parts {
		d := decimalUnbox(p)
		sum = sum.Add(d)
		if !d.Equal(decimal.RequireFromString(want[i])) {
			t.Errorf("part %d: want %s got %s", i, want[i], d)
		}
	}
	if !sum.Equal(in) {
		t.Fatalf("allocate 3 (1.005 USD) sums to %s, want 1.005", sum)
	}
}

// Generative: amounts of any sign and scale (finer, equal and coarser than
// the currency's minor unit), minor units 0 / 2 / 3 / 18, 1-9 parts.
func TestMoney_Allocate_SumPreservingGenerative(t *testing.T) {
	r := rand.New(rand.NewSource(20291102))
	for n := 0; n < 4000; n++ {
		places := []int{0, 2, 3, 18}[r.Intn(4)]
		scale := int32(r.Intn(22))
		amount := decimal.New(r.Int63n(1_000_000_000_000)-500_000_000_000, -scale)
		parts := 1 + r.Intn(9)
		out := invokeMoney(t, "Money_allocate", places, parts, decimalBox(amount)).([]any)
		if len(out) != parts {
			t.Fatalf("%s/%d @%d: %d parts", amount, parts, places, len(out))
		}
		sum := decimal.Zero
		var minP, maxP decimal.Decimal
		for i, p := range out {
			d := decimalUnbox(p)
			sum = sum.Add(d)
			if i == 0 {
				continue
			}
			// Every part after the first is a whole number of minor units.
			if !d.Shift(int32(places)).Equal(d.Shift(int32(places)).Truncate(0)) {
				t.Fatalf("%s/%d @%d: part %d = %s is not whole minor units", amount, parts, places, i, d)
			}
			if i == 1 || d.LessThan(minP) {
				minP = d
			}
			if i == 1 || d.GreaterThan(maxP) {
				maxP = d
			}
		}
		if !sum.Equal(amount) {
			t.Fatalf("%s split %d @%d places sums to %s", amount, parts, places, sum)
		}
		if parts > 1 && maxP.Sub(minP).Shift(int32(places)).GreaterThan(decimal.NewFromInt(1)) {
			t.Fatalf("%s/%d @%d: parts differ by more than one minor unit (%s..%s)", amount, parts, places, minP, maxP)
		}
	}
}
