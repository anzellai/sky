package rt

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// v0.29.2 eighth Judge: a Codec.auto `Currency` field got no write check.
// The partial leaves were a hand-kept list (Decimal, Money) beside the list of
// stdlib types Codec.auto reads by name (Decimal, Money, Currency), so
// `CurrencyRaw "zz"` was stored and every later read of the table failed with
// `"ZZ" is not a currency code`. The partial leaves are now derived from
// codecAutoReadable. These tests hold the derivation complete.

// codecAutoRefusedWrite is, for each stdlib type Codec.auto reads by name, a
// value its writer writes and its own reader refuses. A new readable type
// must add its case here (TestCodecAutoEveryReadableStdTypeIsPartial fails
// until it does), so it cannot join the readers without its write check.
func codecAutoRefusedWrite() map[string]any {
	long := decimal.RequireFromString(strings.Repeat("9", 600))
	return map[string]any{
		"Std_Decimal_Decimal": decimalBox(long.Mul(long)),
		"Std_Money_Money":     SkyADT{SkyName: "Money", Fields: []any{decimalBox(long.Mul(long)), sqlCodeToCurrency("GBP")}},
		"Std_Money_Currency":  sqlCodeToCurrency("zz"),
	}
}

type acCurRow struct {
	Id   string                            `sky:"id,string"`
	Cur  SkyADT                            `sky:"cur,Std_Money_Currency"`
	Curs []SkyADT                          `sky:"curs,[]Std_Money_Currency"`
	Mc   SkyMaybe[SkyADT]                  `sky:"mc,rt.SkyMaybe[Std_Money_Currency]"`
	Td   T2[SkyADT, int]                   `sky:"td,rt.T2[Std_Decimal_Decimal, int]"`
	Tm   T2[SkyADT, string]                `sky:"tm,rt.T2[Std_Money_Money, string]"`
	Tc   T3[int, SkyADT, SkyMaybe[SkyADT]] `sky:"tc,rt.T3[int, Std_Money_Currency, rt.SkyMaybe[Std_Decimal_Decimal]]"`
	Qty  int                               `sky:"qty,int"`
}

func TestCodecAutoEveryReadableStdTypeIsPartial(t *testing.T) {
	refused := codecAutoRefusedWrite()
	adt := reflect.TypeOf(SkyADT{})
	for name := range codecAutoReadable {
		v, ok := refused[name]
		if !ok {
			t.Errorf("%s is read by name by Codec.auto but has no refused-write case in codecAutoRefusedWrite: add one, so its write check is proven", name)
			continue
		}
		for _, declared := range []string{name, "[]" + name, "rt.SkyMaybe[" + name + "]", "rt.SkyDict[" + name + "]", "map[string]" + name, "rt.T2[int, " + name + "]"} {
			if !codecAutoTypePartial(adt, declared, 0) {
				t.Errorf("a field declared %s is not partial: it gets no write check", declared)
			}
		}
		raw, err := codecAutoEncodeTyped(reflect.ValueOf(v), name, true)
		if err != nil {
			t.Errorf("%s: the writer refused the case itself (%v); the case must be a value it writes", name, err)
			continue
		}
		if _, err := codecAutoDecodeTyped(adt, name, codecJSONRoundTrip(t, raw), true); err == nil {
			t.Errorf("%s: the reader took the refused-write case back; the case must be one it refuses", name)
		}
	}
	for name := range refused {
		if !codecAutoReadable[name] {
			t.Errorf("%s has a refused-write case but is not in codecAutoReadable", name)
		}
	}
}

// Every stdlib type name the auto reader dispatches on is in
// codecAutoReadable, so it is partial by construction.
func TestCodecAutoReaderNamesAreAllReadable(t *testing.T) {
	src, err := os.ReadFile("codec_auto.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`declaredType == "(Std_[A-Za-z0-9_]+)"`)
	found := 0
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		found++
		if !codecAutoReadable[m[1]] {
			t.Errorf("codec_auto.go dispatches on %s, which is not in codecAutoReadable: it would get no write check", m[1])
		}
	}
	if found == 0 {
		t.Fatal("no `declaredType == \"Std_…\"` dispatch found in codec_auto.go: the scan is stale")
	}
}

func TestCodecAutoColsChecksEveryCurrencyAndTupleColumn(t *testing.T) {
	got := map[string]string{}
	for _, c := range AsList(Codec_autoCols(true, acCurRow{})) {
		p := c.(T2[any, any])
		got[p.V0.(string)] = p.V1.(string)
	}
	for col, partial := range map[string]bool{"id": false, "cur": true, "curs": true, "mc": true, "td": true, "tm": true, "tc": true, "qty": false} {
		k := got[col]
		if strings.Contains(k, "|problem=") {
			t.Errorf("column %s is a builder problem: %q", col, k)
		}
		if strings.Contains(k, "|check") != partial {
			t.Errorf("column %s kind %q: checked %v, want %v", col, k, !partial, partial)
		}
	}
}

// A tuple holding a Decimal, a Money or a Currency round-trips (it was a
// builder problem whose message read "a standard-library custom type is a
// standard-library custom type Codec.auto does not read back").
func TestCodecAutoTupleOfStdTypesRoundTrips(t *testing.T) {
	d := decimal.RequireFromString("12.50")
	row := acCurRow{
		Id: "a", Cur: sqlCodeToCurrency("GBP"), Curs: []SkyADT{sqlCodeToCurrency("JPY")},
		Mc: Just[SkyADT](sqlCodeToCurrency("EUR")),
		Td: T2[SkyADT, int]{V0: decimalBox(d), V1: 3},
		Tm: T2[SkyADT, string]{V0: SkyADT{SkyName: "Money", Fields: []any{decimalBox(d), sqlCodeToCurrency("USD")}}, V1: "x"},
		Tc: T3[int, SkyADT, SkyMaybe[SkyADT]]{V0: 1, V1: sqlCodeToCurrency("CHF"), V2: Just[SkyADT](decimalBox(d))},
	}
	enc, err := codecAutoEncodeTyped(reflect.ValueOf(row), "", true)
	if err != nil {
		t.Fatal(err)
	}
	raw := codecJSONRoundTrip(t, enc)
	if m, _ := jsonObjectMap(raw); m != nil {
		if tc, _ := jsonObjectMap(m["tc"]); tc == nil || tc["v1"] != "CHF" {
			t.Errorf("a Currency in a tuple is written as %v, want its code", m["tc"])
		}
	}
	back, err := codecAutoDecodeValW(reflect.TypeOf(row), raw, true, reflect.ValueOf(acCurRow{}))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := back.Interface().(acCurRow)
	if currencyCodeOf(got.Tc.V1) != "CHF" || got.Td.V1 != 3 || !decimalUnbox(got.Td.V0).Equal(d) || got.Tm.V1 != "x" ||
		currencyCodeOf(got.Tm.V0.Fields[1]) != "USD" || !decimalUnbox(got.Tc.V2.JustValue).Equal(d) {
		t.Errorf("round trip changed the row: %+v", got)
	}
	// A refused value inside a tuple is refused by the reader.
	bad := row
	bad.Tc.V1 = sqlCodeToCurrency("zz")
	enc, _ = codecAutoEncodeTyped(reflect.ValueOf(bad), "", true)
	if _, err := codecAutoDecodeValW(reflect.TypeOf(row), codecJSONRoundTrip(t, enc), true, reflect.ValueOf(acCurRow{})); err == nil {
		t.Error("CurrencyRaw \"zz\" in a tuple was read back")
	}
}

func TestCodecDeclaredTupleArgs(t *testing.T) {
	for in, want := range map[string][]string{
		"rt.T2[Std_Decimal_Decimal, int]":                              {"Std_Decimal_Decimal", "int"},
		"rt.T3[map[string]rt.T2[int, string], []int, struct{ A int }]": {"map[string]rt.T2[int, string]", "[]int", "struct{ A int }"},
		"rt.T2[int]":        nil,
		"rt.SkyMaybe[int]":  nil,
		"rt.Tx[int, int]":   nil,
		"[]rt.T2[int, int]": nil,
	} {
		if got := codecDeclaredTupleArgs(in); !reflect.DeepEqual(got, want) {
			t.Errorf("codecDeclaredTupleArgs(%q) = %q, want %q", in, got, want)
		}
	}
}

// The builder-problem fix hint names only functions that exist: it named
// `Dict.singleton`, which Sky.Core.Dict does not have.
func TestCodecAutoBlankHintNamesRealFunctions(t *testing.T) {
	p := codecAutoBlankProblem(reflect.TypeOf((*any)(nil)).Elem(), reflect.Value{}, 0)
	if strings.Contains(p, "Dict.singleton") || !strings.Contains(p, "Dict.fromList") {
		t.Errorf("hint %q", p)
	}
	stdlib, err := os.ReadFile("../../sky-stdlib/Sky/Core/Dict.sky")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile("Dict\\.([a-zA-Z]+)").FindAllStringSubmatch(p, -1) {
		if !regexp.MustCompile(`(?m)^` + m[1] + ` :`).Match(stdlib) {
			t.Errorf("the hint names Dict.%s, which Sky.Core.Dict does not declare", m[1])
		}
	}
}

func codecJSONRoundTrip(t *testing.T, v any) any {
	t.Helper()
	s := AsString(JsonEnc_encode(0, JsonValue{raw: v}))
	r := JsonDec_decodeString(JsonDec_value(), s)
	sr, ok := r.(SkyResult[any, any])
	if !ok || sr.Tag != 0 {
		t.Fatalf("JSON round trip of %v: %v", v, r)
	}
	if jv, ok := sr.OkValue.(JsonValue); ok {
		return jv.raw
	}
	return sr.OkValue
}
