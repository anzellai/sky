package rt

// Regression fence for Std.Money kernel primitives.
// Phase 2.4.

import (
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
