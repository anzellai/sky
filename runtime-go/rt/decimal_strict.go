package rt

// Strict Decimal and currency reading for data that crosses a trust boundary:
// JSON a codec decodes (Codec.decimal, Codec.moneyIn, Codec.auto Decimal and
// Money fields), a decimal column a Store reads, the text of a Store
// condition, and `Db.Decode.money`.
//
// shopspring's NewFromString accepts an exponent up to the int32 limit
// ("1e2000000000"), and String() then writes out billions of digits: a
// request body could make the process allocate gigabytes. The grammar here
// is `-?[0-9]+(\.[0-9]+)?`, at most decimalStrictMaxLen bytes, with no
// exponent, no `+`, no spaces, no NaN and no Infinity. `Decimal.fromString`
// keeps its wider grammar for values the program builds itself.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// decimalStrictMaxLen bounds the text of one decimal read from outside.
const decimalStrictMaxLen = 1000

// decimalStrictPattern is the JSON Schema pattern of the grammar.
const decimalStrictPattern = `^-?[0-9]+(\.[0-9]+)?$`

// decimalParseStrict reads `s` under the strict grammar.
func decimalParseStrict(s string) (decimal.Decimal, error) {
	if len(s) > decimalStrictMaxLen {
		return decimal.Zero, fmt.Errorf("a decimal has at most %d characters, and this one has %d", decimalStrictMaxLen, len(s))
	}
	if !decimalStrictText(s) {
		return decimal.Zero, fmt.Errorf("%q is not a decimal (digits with an optional `-` and one `.`, no exponent)", strictShortText(s))
	}
	return decimal.NewFromString(s)
}

// decimalMaxExponent bounds the exponent of a decimal read from text by
// Decimal.fromString (and Money.fromString), which keep the exponent grammar
// (`"1.5e3"`). A decimal with exponent e prints with about |e| digits, and an
// addition rescales both operands to the smaller exponent, so the cost of
// every later operation grows with |e|: `"1e2000000000"` parsed at once and
// then took minutes to print. At 10000 the longest value prints and adds in
// well under a millisecond, and no real quantity is near it (10^80 counts the
// atoms of the observable universe).
const decimalMaxExponent = 10000

// decimalParseBounded reads `s` under the lenient grammar of Decimal.fromString
// (an optional exponent), at most decimalStrictMaxLen bytes and an exponent
// within decimalMaxExponent either way. Its canonical text (the one every
// writer stores) is at most decimalStrictMaxLen characters too (v0.29.2 fifth
// Judge): `"1e1500"` is six characters of input and 1501 of output, and a
// decimal column, `Codec.decimal` and `Codec.auto` refuse to read it back, so
// a value built from such text could be written and never read.
func decimalParseBounded(s string) (decimal.Decimal, error) {
	if len(s) > decimalStrictMaxLen {
		return decimal.Zero, fmt.Errorf("a decimal has at most %d characters, and this one has %d", decimalStrictMaxLen, len(s))
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, err
	}
	if e := int64(d.Exponent()); e > decimalMaxExponent || e < -decimalMaxExponent {
		return decimal.Zero, fmt.Errorf("%q has an exponent past %d either way, too large to compute with", strictShortText(s), decimalMaxExponent)
	}
	if err := decimalCanonicalFits(d); err != nil {
		return decimal.Zero, fmt.Errorf("%q: %v", strictShortText(s), err)
	}
	return d, nil
}

// decimalCanonicalFits is the one bound on a decimal's canonical text: at most
// decimalStrictMaxLen characters, the most a strict reader takes. The parse of
// Decimal.fromString applies it; a computed decimal (Decimal.mul of two long
// ones) can still pass it, and the write check of Codec.decimal, moneyIn,
// moneyObject and an auto Decimal or Money field refuses that value, because
// it runs the same strict reader (decimalParseStrict).
func decimalCanonicalFits(d decimal.Decimal) error {
	if n := len(decimalCanonical(d)); n > decimalStrictMaxLen {
		return fmt.Errorf("written out it has %d characters, and a decimal has at most %d", n, decimalStrictMaxLen)
	}
	return nil
}

// decimalStrictText reports whether `s` matches `-?[0-9]+(\.[0-9]+)?`.
func decimalStrictText(s string) bool {
	i := 0
	if i < len(s) && s[i] == '-' {
		i++
	}
	start := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == start {
		return false
	}
	if i == len(s) {
		return true
	}
	if s[i] != '.' {
		return false
	}
	i++
	frac := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i > frac && i == len(s)
}

// decimalCanonical is the one text Sky writes for a decimal: shopspring's
// String(), which drops trailing zeros (`12.50` → `12.5`, `-0` → `0`), so
// equal values have equal text in a TEXT column.
func decimalCanonical(d decimal.Decimal) string {
	return d.String()
}

// jsonRawOf unwraps a Json Value to the raw tree a decoder reads.
func jsonRawOf(v any) any {
	for {
		jv, ok := v.(JsonValue)
		if !ok {
			return v
		}
		v = jv.raw
	}
}

// decimalFromJSONRaw reads a decimal from one JSON value: a string under the
// strict grammar, or a JSON number whose exact text fits it (a parsed number
// is a json.Number, an `Encode.int` an int). A Go float64 (an `Encode.float`)
// is refused: it is not exact, so its digits are not the sender's.
func decimalFromJSONRaw(raw any) (decimal.Decimal, error) {
	switch x := jsonRawOf(raw).(type) {
	case string:
		return decimalParseStrict(x)
	case json.Number:
		return decimalParseStrict(string(x))
	case int:
		return decimal.NewFromInt(int64(x)), nil
	case int64:
		return decimal.NewFromInt(x), nil
	case float64:
		return decimal.Zero, fmt.Errorf("a decimal must be sent as a string such as \"12.34\"; a Float is not exact")
	case nil:
		return decimal.Zero, fmt.Errorf("expected a decimal string, got null")
	default:
		return decimal.Zero, fmt.Errorf("expected a decimal string, got %s", jsonValueKind(x))
	}
}

// currencyCodeStrict reads a currency code: 3 to 10 ASCII letters or digits,
// read in upper case (`gbp` is GBP, as Money.currencyFromCode reads it). An
// empty or missing code is an error, never `CurrencyRaw ""`.
func currencyCodeStrict(code string) (string, error) {
	if len(code) < 3 || len(code) > 10 {
		return "", fmt.Errorf("%q is not a currency code (3 to 10 letters, such as \"GBP\")", strictShortText(code))
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			return "", fmt.Errorf("%q is not a currency code (3 to 10 letters, such as \"GBP\")", strictShortText(code))
		}
	}
	return strings.ToUpper(code), nil
}

// moneyFromJSONRaw reads the Money JSON object `{"amount":"12.34",
// "currency":"GBP"}`: both keys are required, the amount is a strict decimal
// (a string or an exact JSON number) and the currency a code.
func moneyFromJSONRaw(raw any) (SkyADT, error) {
	m, ok := jsonObjectMap(jsonRawOf(raw))
	if !ok {
		return SkyADT{}, fmt.Errorf("expected a Money object {\"amount\": \"12.34\", \"currency\": \"GBP\"}, got %s", jsonValueKind(jsonRawOf(raw)))
	}
	amtRaw, has := m["amount"]
	if !has {
		return SkyADT{}, fmt.Errorf("a Money object needs an \"amount\"")
	}
	amt, err := decimalFromJSONRaw(amtRaw)
	if err != nil {
		return SkyADT{}, fmt.Errorf("Money amount: %v", err)
	}
	curRaw, has := m["currency"]
	if !has {
		return SkyADT{}, fmt.Errorf("a Money object needs a \"currency\"")
	}
	curS, ok := jsonRawOf(curRaw).(string)
	if !ok {
		return SkyADT{}, fmt.Errorf("Money currency: expected a code string, got %s", jsonValueKind(jsonRawOf(curRaw)))
	}
	code, err := currencyCodeStrict(curS)
	if err != nil {
		return SkyADT{}, fmt.Errorf("Money currency: %v", err)
	}
	return SkyADT{SkyOwner: skyMoneyOwner, Tag: 0, SkyName: "Money", Fields: []any{decimalBox(amt), sqlCodeToCurrency(code)}}, nil
}

// jsonObjectMap reads a JSON object as a map, from a parsed document
// (map[string]any) or an `Encode.object` tree (jsonOrderedObject).
func jsonObjectMap(v any) (map[string]any, bool) {
	switch o := v.(type) {
	case map[string]any:
		return o, true
	case jsonOrderedObject:
		m := make(map[string]any, len(o.keys))
		for i, k := range o.keys {
			if i < len(o.vals) {
				m[k] = o.vals[i]
			}
		}
		return m, true
	}
	return nil, false
}

// moneyFromText reads the Money text forms Sky writes: `"GBP 12.34"`
// (SqlMoney, Db.Decode.money, Std.Analytics) and `"12.34 GBP"`
// (Money.formatWithCode). The code is read in upper case and the amount under
// the strict grammar; anything else is an error.
func moneyFromText(s string) (SkyADT, error) {
	parts := strings.Fields(s)
	if len(parts) != 2 {
		return SkyADT{}, fmt.Errorf("expected Money as \"GBP 12.34\", got %q", strictShortText(s))
	}
	codeS, amtS := parts[0], parts[1]
	if decimalStrictText(codeS) {
		codeS, amtS = amtS, codeS
	}
	amt, err := decimalParseStrict(amtS)
	if err != nil {
		return SkyADT{}, fmt.Errorf("Money amount: %v", err)
	}
	code, err := currencyCodeStrict(codeS)
	if err != nil {
		return SkyADT{}, fmt.Errorf("Money currency: %v", err)
	}
	return SkyADT{SkyOwner: skyMoneyOwner, Tag: 0, SkyName: "Money", Fields: []any{decimalBox(amt), sqlCodeToCurrency(code)}}, nil
}

func init() {
	// Codec_decimalFromJson : Value -> Result Error Decimal
	RegisterPure("Codec_decimalFromJson", func(args []any) any {
		if len(args) < 1 {
			return Err[any, any](ErrDecode("Codec.decimal: missing value"))
		}
		d, err := decimalFromJSONRaw(args[0])
		if err != nil {
			return Err[any, any](ErrDecode("Codec.decimal: " + err.Error()))
		}
		return Ok[any, any](decimalBox(d))
	})
	// Codec_moneyFromJson : Value -> Result Error Money
	RegisterPure("Codec_moneyFromJson", func(args []any) any {
		if len(args) < 1 {
			return Err[any, any](ErrDecode("Codec.moneyObject: missing value"))
		}
		m, err := moneyFromJSONRaw(args[0])
		if err != nil {
			return Err[any, any](ErrDecode("Codec: " + err.Error()))
		}
		return Ok[any, any](m)
	})
	// Codec_decimalFromText : String -> Result Error Decimal
	RegisterPure("Codec_decimalFromText", func(args []any) any {
		if len(args) < 1 {
			return Err[any, any](ErrDecode("decimal: missing text"))
		}
		d, err := decimalParseStrict(AsString(args[0]))
		if err != nil {
			return Err[any, any](ErrDecode(err.Error()))
		}
		return Ok[any, any](decimalBox(d))
	})
	// Codec_currencyCode : String -> Result Error String
	RegisterPure("Codec_currencyCode", func(args []any) any {
		if len(args) < 1 {
			return Err[any, any](ErrDecode("currency: missing code"))
		}
		code, err := currencyCodeStrict(AsString(args[0]))
		if err != nil {
			return Err[any, any](ErrDecode(err.Error()))
		}
		return Ok[any, any](code)
	})
}

// strictShortText is a value for an error message, cut at 40 bytes (this file
// also builds for the wasm client, where db_codec.go's helper does not).
func strictShortText(s string) string {
	if len(s) > 40 {
		return s[:40] + "…"
	}
	return s
}
