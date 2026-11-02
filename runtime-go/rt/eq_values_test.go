package rt

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// Sky `==` on a Decimal and a Json.Value compares the value (v0.29.2 sixth
// Judge): `Decimal "1.10" == Decimal "1.1"` and a Json.Value round-tripped
// through JSON text were False.
func TestEqDecimalAndJsonValueCompareTheValue(t *testing.T) {
	dec := func(s string) any { return decimalBox(decimal.RequireFromString(s)) }
	parse := func(s string) any {
		var v any
		d := json.NewDecoder(strings.NewReader(s))
		d.UseNumber()
		if err := d.Decode(&v); err != nil {
			t.Fatalf("parse %s: %v", s, err)
		}
		return JsonValue{raw: v}
	}
	same := [][2]any{
		{dec("1.10"), dec("1.1")},
		{dec("3"), dec("3.000")},
		{dec("0"), dec("-0.00")},
		{dec("1e900"), dec("1" + zeros(900))},
		{JsonValue{raw: 5}, parse("5")},
		{JsonValue{raw: 5}, parse("5.0")},
		{JsonValue{raw: 0.1}, parse("0.1")},
		{parse(`{"a":1,"b":[true,null,"x"]}`), JsonValue{raw: jsonOrderedObject{keys: []string{"b", "a"}, vals: []any{[]any{true, nil, "x"}, int64(1)}}}},
		{parse("12345678901234567890123"), parse("1.2345678901234567890123e22")},
	}
	for _, c := range same {
		if !deepEq(c[0], c[1]) || AsBool(NotEq(c[0], c[1])) {
			t.Errorf("%v == %v is False, want True", c[0], c[1])
		}
	}
	differ := [][2]any{
		{dec("1.1"), dec("1.10000000000000000001")},
		{dec("1e900"), dec("1e-900")},
		{JsonValue{raw: 5}, parse("6")},
		{parse(`{"a":1}`), parse(`{"a":1,"b":2}`)},
		{parse(`{"a":1}`), parse(`{"a":"1"}`)},
		{parse(`[1,2]`), parse(`[2,1]`)},
		{parse("null"), parse("0")},
		{parse("12345678901234567890123"), parse("12345678901234567890124")},
	}
	for _, c := range differ {
		if deepEq(c[0], c[1]) {
			t.Errorf("%v == %v is True, want False", c[0], c[1])
		}
	}
	// A Money holds its amount as a Decimal: equal amounts in one currency are
	// equal however they are written.
	m := func(s string) any {
		return SkyADT{Tag: 0, SkyName: "Money", Fields: []any{dec(s), sqlCodeToCurrency("GBP")}}
	}
	if !deepEq(m("1.50"), m("1.5")) {
		t.Errorf("Money 1.50 GBP == Money 1.5 GBP is False")
	}
}

func zeros(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '0'
	}
	return string(b)
}
