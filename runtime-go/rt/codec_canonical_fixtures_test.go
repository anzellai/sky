package rt

import "github.com/shopspring/decimal"

// Native runtime tests have no generated main package. Explicitly model the
// source-granted alias registrations its canonical stdlib init blocks emit.
// Repeated registrations with the main provenance regressions are idempotent.
func init() {
	RegisterAdtOwner("test.CanonicalDecimal", "Std_Decimal_Decimal", skyDecimalOwner)
	RegisterAdtOwner("test.CanonicalMoney", "Std_Money_Money", skyMoneyOwner)
	RegisterAdtOwner("test.CanonicalCurrency", "Std_Money_Currency", skyCurrencyOwner)
}

// Start with the actual strict runtime producer, then replace its amount with
// the actual kernel Decimal box. This also represents a computed Money amount
// longer than the strict JSON reader accepts: the existing derived-check tests
// deliberately need that writer/reader distinction. Never manufacture an
// owner token, and never send the long computed amount through its strict
// reader (which would change or reject the adversarial fixture).
func canonicalMoneyFixture(amount decimal.Decimal, currency string) SkyADT {
	value, err := moneyFromJSONRaw(map[string]any{"amount": "0", "currency": currency})
	if err != nil {
		panic("invalid canonical Money fixture: " + err.Error())
	}
	value.Fields = []any{decimalBox(amount), value.Fields[1]}
	return value
}
