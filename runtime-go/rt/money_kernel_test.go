package rt

// Regression fence for Std.Money kernel primitives.
// Phase 2.4.

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func invokeMoney(t *testing.T, name string, args ...any) any {
	t.Helper()
	ffiRegistryMu.RLock()
	fn, ok := ffiPureRegistry[name]
	ffiRegistryMu.RUnlock()
	if !ok {
		t.Fatalf("money kernel %q not registered", name)
	}
	return fn(args)
}

func TestMoney_MinorUnits(t *testing.T) {
	cases := []struct {
		code string
		want int
	}{
		{"USD", 2}, {"JPY", 0}, {"BHD", 3}, {"KRW", 0}, {"EUR", 2},
		{"BTC", 8}, {"ETH", 18},
		{"XYZ", 2}, // unknown defaults to 2dp
	}
	for _, c := range cases {
		got := invokeMoney(t, "Money_minorUnits", c.code)
		if got != c.want {
			t.Errorf("%s minor units: want %d got %v", c.code, c.want, got)
		}
	}
}

func TestMoney_Symbol(t *testing.T) {
	cases := map[string]string{
		"USD": "$", "GBP": "£", "EUR": "€", "JPY": "¥",
	}
	for code, want := range cases {
		if got := invokeMoney(t, "Money_symbol", code); got != want {
			t.Errorf("%s symbol: want %q got %q", code, want, got)
		}
	}
}

func TestMoney_IsKnownCurrency(t *testing.T) {
	if invokeMoney(t, "Money_isKnownCurrency", "USD") != true {
		t.Fatal("USD should be known")
	}
	if invokeMoney(t, "Money_isKnownCurrency", "XYZ") != false {
		t.Fatal("XYZ should not be known")
	}
}

func TestMoney_Format(t *testing.T) {
	usd := decimalBox(decimal.NewFromFloat(12.34))
	if got := invokeMoney(t, "Money_format", "USD", usd); got != "$12.34" {
		t.Errorf("USD format: want %q got %q", "$12.34", got)
	}
	jpy := decimalBox(decimal.NewFromInt(500))
	if got := invokeMoney(t, "Money_format", "JPY", jpy); got != "¥500" {
		t.Errorf("JPY format: want %q got %q", "¥500", got)
	}
	// Negative
	neg := decimalBox(decimal.NewFromFloat(-12.34))
	if got := invokeMoney(t, "Money_format", "USD", neg); got != "-$12.34" {
		t.Errorf("neg USD: want %q got %q", "-$12.34", got)
	}
}

func TestMoney_FormatWithCode(t *testing.T) {
	usd := decimalBox(decimal.NewFromFloat(12.34))
	if got := invokeMoney(t, "Money_formatWithCode", "USD", usd); got != "12.34 USD" {
		t.Errorf("formatWithCode: want %q got %q", "12.34 USD", got)
	}
	// Lowercase code normalises to uppercase
	if got := invokeMoney(t, "Money_formatWithCode", "usd", usd); got != "12.34 USD" {
		t.Errorf("formatWithCode lowercase: want %q got %q", "12.34 USD", got)
	}
}

func TestMoney_Allocate_SumExact(t *testing.T) {
	// $100 / 3 = [$33.34, $33.33, $33.33] — sum back to $100 exactly.
	hundred := decimalBox(decimal.NewFromInt(100))
	parts := invokeMoney(t, "Money_allocate", 2, 3, hundred)
	parr, ok := parts.([]any)
	if !ok {
		t.Fatalf("allocate returned %T", parts)
	}
	if len(parr) != 3 {
		t.Fatalf("allocate(3) → %d parts", len(parr))
	}
	sum := decimal.Zero
	for _, p := range parr {
		sum = sum.Add(decimalUnbox(p))
	}
	if !sum.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("allocate sum: want 100 got %v", sum)
	}
	// First part gets the cent: $33.34
	if !decimalUnbox(parr[0]).Equal(decimal.NewFromFloat(33.34)) {
		t.Fatalf("first part: want 33.34 got %v", decimalUnbox(parr[0]))
	}
}

func TestMoney_Allocate_ExactSplit(t *testing.T) {
	// $9 / 3 = $3, $3, $3 (no remainder).
	nine := decimalBox(decimal.NewFromInt(9))
	parts := invokeMoney(t, "Money_allocate", 2, 3, nine).([]any)
	for i, p := range parts {
		if !decimalUnbox(p).Equal(decimal.NewFromInt(3)) {
			t.Errorf("part %d: want 3 got %v", i, decimalUnbox(p))
		}
	}
}

func TestMoney_FXRoundTrip(t *testing.T) {
	// Start clean
	invokeMoney(t, "Money_clearRates")

	// Setting a rate auto-registers the inverse.
	rate := decimalBox(decimal.NewFromFloat(0.92))
	r := invokeMoney(t, "Money_setRate", "USD", "EUR", rate)
	if sr, ok := r.(SkyResult[any, any]); !ok || sr.Tag != 0 {
		t.Fatalf("setRate failed: %v", r)
	}

	// Forward rate
	got := invokeMoney(t, "Money_getRate", "USD", "EUR")
	sr := got.(SkyResult[any, any])
	if sr.Tag != 0 {
		t.Fatalf("getRate USD→EUR failed: %v", sr.ErrValue)
	}
	if !decimalUnbox(sr.OkValue).Equal(decimal.NewFromFloat(0.92)) {
		t.Fatalf("USD→EUR rate: want 0.92 got %v", decimalUnbox(sr.OkValue))
	}

	// Inverse auto-registered
	inv := invokeMoney(t, "Money_getRate", "EUR", "USD")
	sr2 := inv.(SkyResult[any, any])
	if sr2.Tag != 0 {
		t.Fatalf("inverse not registered")
	}
	// 1/0.92 ≈ 1.0869565...
	gotInv := decimalUnbox(sr2.OkValue)
	expected, _ := decimal.NewFromString("1.08695652173913")
	diff := gotInv.Sub(expected).Abs()
	if diff.GreaterThan(decimal.NewFromFloat(0.0001)) {
		t.Fatalf("inverse rate: want ~1.0869 got %v (diff %v)", gotInv, diff)
	}

	// hasRate
	if invokeMoney(t, "Money_hasRate", "USD", "EUR") != true {
		t.Fatal("hasRate USD→EUR")
	}
	if invokeMoney(t, "Money_hasRate", "USD", "GBP") != false {
		t.Fatal("hasRate USD→GBP should be false (unregistered)")
	}

	// Same currency always has rate of 1
	same := invokeMoney(t, "Money_getRate", "USD", "USD")
	sr3 := same.(SkyResult[any, any])
	if sr3.Tag != 0 || !decimalUnbox(sr3.OkValue).Equal(decimal.NewFromInt(1)) {
		t.Fatalf("USD→USD rate not 1")
	}

	invokeMoney(t, "Money_clearRates")
}

func TestMoney_SetRate_ZeroRejected(t *testing.T) {
	zero := decimalBox(decimal.Zero)
	r := invokeMoney(t, "Money_setRate", "USD", "EUR", zero)
	sr, ok := r.(SkyResult[any, any])
	if !ok || sr.Tag != 1 {
		t.Fatalf("setRate(0) should Err: %v", r)
	}
	msg := extractErrString(sr.ErrValue)
	if !strings.Contains(msg, "positive") {
		t.Fatalf("Err message should mention 'positive', got: %v", msg)
	}
}

func extractErrString(v any) string {
	if adt, ok := v.(SkyADT); ok && len(adt.Fields) >= 2 {
		if info, ok := adt.Fields[1].(skyErrorInfo); ok {
			return info.Message
		}
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
