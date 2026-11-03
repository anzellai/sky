package rt

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// v0.29.2 sixth Judge: a Codec.auto field of a data-carrying custom type
// read back as another value (`BoxM (Just 3)` as `BoxM (Just 0)`, `Fixed
// 12.5` as a broken Decimal) or not at all. A variant's payloads are now read
// by their Go type, strictly; a payload whose Go type does not say what it
// holds (an erased stdlib type, a type variable) is a builder problem of the
// field, and the reader refuses it.

type adtTestBox interface {
	SkyVariant
	isAdtTestBox()
}

type adtTestBox_M_V struct{ V0 SkyMaybe[int] }
type adtTestBox_L_V struct{ V0 []int }
type adtTestBox_R_V struct {
	V0 adtTestInner
	V1 adtTestBox
}
type adtTestBox_N_V struct{}

type adtTestInner struct {
	Label string `sky:"label,string"`
	Count int    `sky:"count,int"`
}

func (adtTestBox_M_V) SkyVariantTag() int     { return 0 }
func (adtTestBox_M_V) SkyVariantName() string { return "BoxM" }
func (adtTestBox_M_V) isAdtTestBox()          {}
func (adtTestBox_L_V) SkyVariantTag() int     { return 1 }
func (adtTestBox_L_V) SkyVariantName() string { return "BoxL" }
func (adtTestBox_L_V) isAdtTestBox()          {}
func (adtTestBox_R_V) SkyVariantTag() int     { return 2 }
func (adtTestBox_R_V) SkyVariantName() string { return "BoxR" }
func (adtTestBox_R_V) isAdtTestBox()          {}
func (adtTestBox_N_V) SkyVariantTag() int     { return 3 }
func (adtTestBox_N_V) SkyVariantName() string { return "BoxN" }
func (adtTestBox_N_V) isAdtTestBox()          {}

type adtTestPrice interface {
	SkyVariant
	isAdtTestPrice()
}

// The generated code erases a Decimal payload to rt.SkyADT.
type adtTestPrice_Fixed_V struct{ V0 SkyADT }
type adtTestPrice_Free_V struct{}

func (adtTestPrice_Fixed_V) SkyVariantTag() int     { return 0 }
func (adtTestPrice_Fixed_V) SkyVariantName() string { return "Fixed" }
func (adtTestPrice_Fixed_V) isAdtTestPrice()        {}
func (adtTestPrice_Free_V) SkyVariantTag() int      { return 1 }
func (adtTestPrice_Free_V) SkyVariantName() string  { return "Free" }
func (adtTestPrice_Free_V) isAdtTestPrice()         {}

func init() {
	// The factories ignore their payloads, as the generated ones lose them:
	// the reader must not depend on them.
	RegisterAdtVariant("rt.adtTestBox", "BoxM", func([]JsonRawMessage) any { return adtTestBox_M_V{} })
	RegisterAdtVariant("rt.adtTestBox", "BoxL", func([]JsonRawMessage) any { return adtTestBox_L_V{} })
	RegisterAdtVariant("rt.adtTestBox", "BoxR", func([]JsonRawMessage) any { return adtTestBox_R_V{} })
	RegisterAdtVariant("rt.adtTestBox", "BoxN", func([]JsonRawMessage) any { return adtTestBox_N_V{} })
	RegisterAdtVariant("rt.adtTestPrice", "Fixed", func([]JsonRawMessage) any { return adtTestPrice_Fixed_V{} })
	RegisterAdtVariant("rt.adtTestPrice", "Free", func([]JsonRawMessage) any { return adtTestPrice_Free_V{} })
}

type adtTestRow struct {
	Id  string     `sky:"id,string"`
	Box adtTestBox `sky:"box,adtTestBox"`
}

type adtTestPriced struct {
	Id    string       `sky:"id,string"`
	Price adtTestPrice `sky:"price,adtTestPrice"`
}

func adtRoundTrip(t *testing.T, blank, v any) SkyResult[any, any] {
	t.Helper()
	b, err := json.Marshal(Codec_autoEnc(true, v).(JsonValue).raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw any
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	return Codec_autoDecoder(true, blank).(JsonDecoder).run(raw).(SkyResult[any, any])
}

func autoColProblems(blank any) []string {
	var out []string
	for _, c := range asList(Codec_autoCols(true, blank)) {
		kind := c.(T2[any, any]).V1.(string)
		for _, flag := range strings.Split(kind, "|")[1:] {
			if p, ok := strings.CutPrefix(flag, "problem="); ok {
				out = append(out, p)
			}
		}
	}
	return out
}

func TestCodecAutoVariantPayloadsRoundTripExactly(t *testing.T) {
	blank := adtTestRow{Box: adtTestBox_N_V{}}
	values := []adtTestRow{
		{Id: "m", Box: adtTestBox_M_V{V0: Just[int](3)}},
		{Id: "n", Box: adtTestBox_M_V{V0: Nothing[int]()}},
		{Id: "l", Box: adtTestBox_L_V{V0: []int{9223372036854775807, -1}}},
		{Id: "r", Box: adtTestBox_R_V{V0: adtTestInner{Label: "x", Count: 7}, V1: adtTestBox_M_V{V0: Just[int](5)}}},
		{Id: "z", Box: adtTestBox_N_V{}},
	}
	for _, v := range values {
		res := adtRoundTrip(t, blank, v)
		if res.Tag != 0 {
			t.Errorf("%+v: Err %v", v, res.ErrValue)
			continue
		}
		if !deepEq(res.OkValue, v) {
			t.Errorf("%+v read back as %+v", v, res.OkValue)
		}
	}
	if p := autoColProblems(blank); len(p) != 0 {
		t.Errorf("a custom type of readable payloads has problems %v", p)
	}
}

func TestCodecAutoVariantOfAnErasedPayloadIsAProblemAndRefused(t *testing.T) {
	blank := adtTestPriced{Price: adtTestPrice_Free_V{}}
	p := autoColProblems(blank)
	if len(p) != 1 || !strings.Contains(p[0], `the field "price"`) || !strings.Contains(p[0], "Fixed") {
		t.Fatalf("problems = %q, want one naming the field price and the variant Fixed", p)
	}
	v := adtTestPriced{Id: "a", Price: adtTestPrice_Fixed_V{V0: decimalBox(decimal.RequireFromString("12.5"))}}
	if res := adtRoundTrip(t, blank, v); res.Tag == 0 {
		t.Errorf("Fixed 12.5 read back as %+v, want an Err", res.OkValue)
	}
	if res := adtRoundTrip(t, blank, adtTestPriced{Id: "b", Price: adtTestPrice_Free_V{}}); res.Tag != 0 {
		t.Errorf("Free: Err %v", res.ErrValue)
	}
}

// Two fields that become one snake_case key: a builder problem and a decode
// Err, never `{aB = 2, a_b = 2}`.
func TestCodecAutoTwoFieldsOneKeyIsAProblem(t *testing.T) {
	type rec struct {
		AB  int `sky:"aB,int"`
		A_b int `sky:"a_b,int"`
	}
	p := autoColProblems(rec{})
	if len(p) != 1 || !strings.Contains(p[0], `"aB" and "a_b"`) {
		t.Fatalf("problems = %q", p)
	}
	if res := Codec_autoDecoder(true, rec{}).(JsonDecoder).run(map[string]any{"a_b": json.Number("1")}).(SkyResult[any, any]); res.Tag == 0 {
		t.Errorf("decoded %+v, want an Err", res.OkValue)
	}
	if p := autoColProblems(rec{}); len(autoColProblemsCamel(rec{})) != 0 {
		t.Errorf("autoCamel keeps both keys, got problems %q (snake %q)", autoColProblemsCamel(rec{}), p)
	}
}

func autoColProblemsCamel(blank any) []string {
	var out []string
	for _, c := range asList(Codec_autoCols(false, blank)) {
		if strings.Contains(c.(T2[any, any]).V1.(string), "|problem=") {
			out = append(out, c.(T2[any, any]).V1.(string))
		}
	}
	return out
}

// A `Maybe (Maybe Int)` field's `Just Nothing` and a `Maybe Json.Value`
// field's `Just null` have no JSON of their own: written as null (toJson
// unchanged) but refused by every reader in memory, so the Store write check
// refuses the row. A stored null still reads as Nothing.
func TestCodecAutoJustOfNullIsRefusedInMemory(t *testing.T) {
	type rec struct {
		Id string                  `sky:"id,string"`
		Mm SkyMaybe[SkyMaybe[int]] `sky:"mm,rt.SkyMaybe[rt.SkyMaybe[int]]"`
	}
	v := rec{Id: "a", Mm: Just[SkyMaybe[int]](Nothing[int]())}
	enc := Codec_autoEnc(true, v).(JsonValue)
	if b, _ := json.Marshal(enc.raw); string(b) != `{"id":"a","mm":null}` {
		t.Errorf("toJson = %s", b)
	}
	if res := Codec_autoDecoder(true, rec{}).(JsonDecoder).run(enc.raw).(SkyResult[any, any]); res.Tag == 0 {
		t.Errorf("Just Nothing decoded in memory as %+v, want an Err", res.OkValue)
	}
	ok := Codec_autoDecoder(true, rec{}).(JsonDecoder).run(map[string]any{"id": "b", "mm": nil}).(SkyResult[any, any])
	if ok.Tag != 0 {
		t.Errorf("null: Err %v", ok.ErrValue)
	}
	if !codecAutoTypePartial(reflect.TypeOf(rec{}).Field(1).Type, "", 0) {
		t.Errorf("a Maybe (Maybe Int) field is not checked on a write")
	}
}

// A `List Json.Value` field with the blank `[ Json.Encode.null ]` reads its
// elements as JSON; every read used to fail "cannot decode kind interface".
func TestCodecAutoListOfJsonValueReads(t *testing.T) {
	type rec struct {
		Vs []any `sky:"vs,[]any"`
	}
	blank := rec{Vs: []any{JsonValue{raw: nil}}}
	res := Codec_autoDecoder(true, blank).(JsonDecoder).run(map[string]any{"vs": []any{json.Number("5"), "x", nil}}).(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("Err %v", res.ErrValue)
	}
	got := res.OkValue.(rec).Vs
	if len(got) != 3 || !deepEq(got[0], JsonValue{raw: 5}) || !deepEq(got[1], JsonValue{raw: "x"}) {
		t.Errorf("read %+v", got)
	}
}

// A Currency field is its code and reads back; the tagged object it used to
// be written as (which no read rebuilt) reads too.
func TestCodecAutoCurrencyFieldRoundTrips(t *testing.T) {
	type rec struct {
		Cur SkyADT `sky:"cur,Std_Money_Currency"`
	}
	for _, c := range []SkyADT{sqlCodeToCurrency("GBP"), sqlCodeToCurrency("XYZ"), sqlCodeToCurrency("USDT")} {
		res := adtRoundTrip(t, rec{Cur: sqlCodeToCurrency("GBP")}, rec{Cur: c})
		if res.Tag != 0 || !deepEq(res.OkValue, rec{Cur: c}) {
			t.Errorf("%v: %+v", c.SkyName, res)
		}
	}
	for raw, want := range map[string]string{`{"tag":"EUR"}`: "EUR", `{"tag":"CurrencyRaw","v0":"xyz"}`: "XYZ", `"gbp"`: "GBP"} {
		var r any
		_ = json.Unmarshal([]byte(`{"cur":`+raw+`}`), &r)
		res := Codec_autoDecoder(true, rec{Cur: sqlCodeToCurrency("GBP")}).(JsonDecoder).run(r).(SkyResult[any, any])
		if res.Tag != 0 || currencyCodeOf(res.OkValue.(rec).Cur) != want {
			t.Errorf("%s: %+v", raw, res)
		}
	}
	if p := autoColProblems(rec{Cur: sqlCodeToCurrency("GBP")}); len(p) != 0 {
		t.Errorf("a Currency field has problems %q", p)
	}
}
