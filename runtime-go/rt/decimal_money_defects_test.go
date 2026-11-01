package rt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Regression tests for the Decimal / Money defects found in v0.29.2 (D-a to
// D-d). They use only the kernels the app calls, so each one fails on the
// v0.29.1 runtime.

type dmMoneyHolder struct {
	Price Std_Decimal_Decimal_forTest `sky:"price,Std_Money_Money"`
}

type dmDecimalHolder struct {
	Qty Std_Decimal_Decimal_forTest `sky:"qty,Std_Decimal_Decimal"`
}

func dmDecode(t *testing.T, witness any, doc string) SkyResult[any, any] {
	t.Helper()
	dec := Codec_autoDecoder(true, witness).(JsonDecoder)
	raw, err := jsonParseDocument(doc)
	if err != nil {
		t.Fatalf("bad test JSON %s: %v", doc, err)
	}
	return dec.run(raw).(SkyResult[any, any])
}

// D-a: the JSON Schema of a Codec.auto Decimal field was the open `{}`
// (exact = False), and a Money field's too, so `toXml` wrote JSON text.
func TestCodecAutoSchemaOfDecimalAndMoneyIsClosed(t *testing.T) {
	got := Codec_autoSchema(true, dmDecimalHolder{}).(string)
	if !strings.Contains(got, `"type":"string"`) || !strings.Contains(got, `"pattern"`) || strings.Contains(got, `"exact":false`) {
		t.Errorf("an auto Decimal field must be a string with a pattern, exact: %s", got)
	}
	got = Codec_autoSchema(true, dmMoneyHolder{}).(string)
	if !strings.Contains(got, `"amount"`) || !strings.Contains(got, `"currency"`) || strings.Contains(got, `"exact":false`) {
		t.Errorf("an auto Money field must be the {amount, currency} object, exact: %s", got)
	}
}

// D-b: a Money without a currency decoded as `CurrencyRaw ""`, and a
// lower-case code as `CurrencyRaw "gbp"`.
func TestCodecAutoMoneyDecodeIsStrict(t *testing.T) {
	if r := dmDecode(t, dmMoneyHolder{}, `{"price":{"amount":"12.34"}}`); r.Tag == 0 {
		t.Errorf("a Money with no currency must be an Err, got %+v", r.OkValue)
	}
	if r := dmDecode(t, dmMoneyHolder{}, `{"price":{"currency":"GBP"}}`); r.Tag == 0 {
		t.Errorf("a Money with no amount must be an Err, got %+v", r.OkValue)
	}
	r := dmDecode(t, dmMoneyHolder{}, `{"price":{"amount":"12.34","currency":"gbp"}}`)
	if r.Tag != 0 {
		t.Fatalf("a lower-case code must read: %+v", r.ErrValue)
	}
	m := r.OkValue.(dmMoneyHolder).Price
	if cur, ok := m.Fields[1].(SkyADT); !ok || cur.SkyName != "GBP" {
		t.Errorf("\"gbp\" must read as GBP, got %+v", m.Fields[1])
	}
	r = dmDecode(t, dmMoneyHolder{}, `{"price":{"amount":12.5,"currency":"GBP"}}`)
	if r.Tag != 0 {
		t.Fatalf("an exact JSON number amount must read: %+v", r.ErrValue)
	}
	if got := decimalUnbox(r.OkValue.(dmMoneyHolder).Price.Fields[0]).String(); got != "12.5" {
		t.Errorf("amount 12.5 read as %s", got)
	}
}

// D-c: an exponent reached shopspring's NewFromString, and String() then
// wrote out billions of digits. The strict grammar refuses it at once.
func TestCodecAutoDecimalRefusesExponentAndLongText(t *testing.T) {
	bad := []string{
		`"1e2000000000"`, `"1e3"`, `"1E3"`, `"abc"`, `""`, `"1.2.3"`, `"NaN"`, `"Infinity"`,
		`"+1"`, `" 1"`, `"1."`, `".5"`, `"` + strings.Repeat("9", 1001) + `"`, `null`, `true`,
	}
	for _, b := range bad {
		start := time.Now()
		r := dmDecode(t, dmDecimalHolder{}, `{"qty":`+b+`}`)
		if r.Tag == 0 {
			t.Errorf("%s must be refused, got %v", codecShortText(b), decimalUnbox(r.OkValue.(dmDecimalHolder).Qty))
		}
		if time.Since(start) > time.Second {
			t.Errorf("%s took %v to refuse", codecShortText(b), time.Since(start))
		}
	}
	good := map[string]string{
		`"0.1"`:                               "0.1",
		`"-0.000000001"`:                      "-0.000000001",
		`"123456789012345678901.123456789"`:   "123456789012345678901.123456789",
		`"12.50"`:                             "12.5",
		`7`:                                   "7",
		`"` + strings.Repeat("1", 1000) + `"`: strings.Repeat("1", 1000),
	}
	for in, want := range good {
		r := dmDecode(t, dmDecimalHolder{}, `{"qty":`+in+`}`)
		if r.Tag != 0 {
			t.Errorf("%s must read: %+v", codecShortText(in), r.ErrValue)
			continue
		}
		if got := decimalUnbox(r.OkValue.(dmDecimalHolder).Qty).String(); got != want {
			t.Errorf("%s read as %s, want %s", codecShortText(in), codecShortText(got), codecShortText(want))
		}
	}
}

// D-d: `Db.Decode.money` read only "USD 12.34" and took any case as the
// code ("usd 1" was `CurrencyRaw "usd"`); "12.34 USD" (formatWithCode) did
// not read; an exponent reached NewFromString.
func TestDbDecodeMoneyReadsBothTextFormsStrictly(t *testing.T) {
	run := func(s string) SkyResult[any, any] {
		return DbDec_money("m").(DbDecoder).run(map[string]any{"m": s}).(SkyResult[any, any])
	}
	for _, in := range []string{"USD 12.34", "12.34 USD", "usd 12.34"} {
		r := run(in)
		if r.Tag != 0 {
			t.Errorf("%q must read: %+v", in, r.ErrValue)
			continue
		}
		m := r.OkValue.(SkyADT)
		if cur := m.Fields[1].(SkyADT); cur.SkyName != "USD" {
			t.Errorf("%q: currency %+v, want USD", in, cur)
		}
		if got := decimalUnbox(m.Fields[0]).String(); got != "12.34" {
			t.Errorf("%q: amount %s", in, got)
		}
	}
	for _, in := range []string{"USD 1e2000000000", "USD", "12.34", "U 1", "USD 1 2"} {
		if r := run(in); r.Tag == 0 {
			t.Errorf("%q must be refused", in)
		}
	}
}

// A decoded currency must carry its constructor's Tag: a Sky `case` matches
// the Tag. Every named code had Tag 0, so a decoded GBP matched the `USD` arm
// (Codec.auto, Db.Decode.money). The order is read from Money.sky so the
// table cannot drift from the type.
func TestDecodedCurrencyTagMatchesMoneySky(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "sky-stdlib", "Std", "Money.sky"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "type Currency\n")
	if start < 0 {
		t.Fatal("type Currency not found in Money.sky")
	}
	var ctors []string
	for _, line := range strings.Split(body[start:], "\n")[1:] {
		l := strings.TrimSpace(line)
		if !strings.HasPrefix(l, "=") && !strings.HasPrefix(l, "|") {
			break
		}
		ctors = append(ctors, strings.Fields(strings.TrimLeft(l, "=| "))[0])
	}
	for i, c := range ctors {
		got := sqlCodeToCurrency(c)
		if c == "CurrencyRaw" {
			got = sqlCodeToCurrency("ZZQ")
		}
		if got.Tag != i || (c != "CurrencyRaw" && got.SkyName != c) {
			t.Errorf("%s: Tag %d SkyName %s, want Tag %d", c, got.Tag, got.SkyName, i)
		}
	}
	r := DbDec_money("m").(DbDecoder).run(map[string]any{"m": "GBP 1"}).(SkyResult[any, any])
	if cur := r.OkValue.(SkyADT).Fields[1].(SkyADT); cur.Tag != 2 {
		t.Errorf("Db.Decode.money GBP has Tag %d, want 2", cur.Tag)
	}
}
