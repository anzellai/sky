package rt

import (
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"
)

// mirrors what a Sky record lowers to, with the S3 `sky:` field tags.
type acAddress struct {
	City string `sky:"city,string"`
	Zip  string `sky:"zip,string"`
}

type acUser struct {
	Id      string           `sky:"id,string"`
	Age     int              `sky:"age,int"`
	Active  bool             `sky:"active,bool"`
	Nick    SkyMaybe[string] `sky:"nick,rt.SkyMaybe[string]"`
	Address acAddress        `sky:"address,acAddress"`
	Tags    []string         `sky:"tags,[]string"`
}

func TestCodecAutoRoundTrip(t *testing.T) {
	u := acUser{
		Id: "u1", Age: 30, Active: true,
		Nick:    Just("ace"),
		Address: acAddress{City: "London", Zip: "E1"},
		Tags:    []string{"a", "b"},
	}

	// encode → JSON string
	encoded := Codec_autoEnc(true, u)
	jv, ok := encoded.(JsonValue)
	if !ok {
		t.Fatalf("autoEnc did not return a JsonValue: %T", encoded)
	}
	b, err := json.Marshal(jv.raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// decode via the reflection decoder
	dec := Codec_autoDecoder(true, acUser{}).(JsonDecoder)
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	res := dec.run(raw).(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("decode failed: %+v", res)
	}
	back := res.OkValue.(acUser)

	if back.Id != "u1" || back.Age != 30 || !back.Active {
		t.Errorf("scalars wrong: %+v", back)
	}
	if back.Nick.Tag != 0 || back.Nick.JustValue != "ace" {
		t.Errorf("Maybe wrong: %+v", back.Nick)
	}
	if back.Address.City != "London" || back.Address.Zip != "E1" {
		t.Errorf("nested record wrong: %+v", back.Address)
	}
	if len(back.Tags) != 2 || back.Tags[0] != "a" || back.Tags[1] != "b" {
		t.Errorf("list wrong: %+v", back.Tags)
	}
}

func TestCodecAutoNothingAndEmpty(t *testing.T) {
	u := acUser{Id: "u2", Nick: Nothing[string](), Tags: []string{}}
	encoded := Codec_autoEnc(true, u).(JsonValue)
	b, _ := json.Marshal(encoded.raw)

	dec := Codec_autoDecoder(true, acUser{}).(JsonDecoder)
	var raw any
	json.Unmarshal(b, &raw)
	back := dec.run(raw).(SkyResult[any, any]).OkValue.(acUser)

	if back.Nick.Tag != 1 {
		t.Errorf("Nothing did not round-trip: %+v", back.Nick)
	}
	if len(back.Tags) != 0 {
		t.Errorf("empty list wrong: %+v", back.Tags)
	}
}

func TestCodecAutoCols(t *testing.T) {
	cols := AsList(Codec_autoCols(true, acUser{}))
	want := map[string]string{
		"id": "text", "age": "int", "active": "bool",
		"nick": "text?", "address": "blob", "tags": "blob", // nick is Maybe → nullable
	}
	if len(cols) != len(want) {
		t.Fatalf("cols count = %d, want %d", len(cols), len(want))
	}
	for _, c := range cols {
		tup := AsTuple2(c)
		name := AsString(tup.V0)
		kind := AsString(tup.V1)
		if want[name] != kind {
			t.Errorf("col %q kind = %q, want %q", name, kind, want[name])
		}
	}
}

func TestCodecAutoSnakeVsCamel(t *testing.T) {
	type multiWord struct {
		ItemName   string `sky:"itemName,string"`
		PriceMinor int    `sky:"priceMinor,int"`
	}
	snakeCols := AsList(Codec_autoCols(true, multiWord{}))
	camelCols := AsList(Codec_autoCols(false, multiWord{}))
	if AsString(AsTuple2(snakeCols[0]).V0) != "item_name" || AsString(AsTuple2(snakeCols[1]).V0) != "price_minor" {
		t.Errorf("auto(snake) cols = %v, want item_name/price_minor", snakeCols)
	}
	if AsString(AsTuple2(camelCols[0]).V0) != "itemName" || AsString(AsTuple2(camelCols[1]).V0) != "priceMinor" {
		t.Errorf("autoCamel cols = %v, want itemName/priceMinor", camelCols)
	}
}

// awItem exercises Codec.autoWith's per-field override (a Bool stored as 0/1).
type awItem struct {
	Id     string `sky:"id,string"`
	Active bool   `sky:"active,bool"`
}

func TestCodecAutoWithOverride(t *testing.T) {
	// enc override: Bool -> int (0/1), like `intBool`.
	enc := func(v any) any {
		if b, _ := v.(bool); b {
			return JsonValue{raw: 1}
		}
		return JsonValue{raw: 0}
	}
	encs := []any{T2[any, any]{V0: "active", V1: enc}}
	got, _ := Codec_autoEncOverrides(true, encs, awItem{Id: "i1", Active: true}).(JsonValue)
	obj, ok := got.raw.(jsonOrderedObject)
	if !ok {
		t.Fatalf("enc: not an object: %#v", got.raw)
	}
	activeVal := any(nil)
	for i, k := range obj.keys {
		if k == "active" {
			activeVal = obj.vals[i]
		}
	}
	if activeVal != 1 {
		t.Errorf("active override should encode as int 1, got %#v", activeVal)
	}

	// dec override: int -> Bool.
	dec := JsonDecoder{run: func(raw any) any { return Ok[any, any](AsIntOrZero(raw) != 0) }}
	decs := []any{T2[any, any]{V0: "active", V1: dec}}
	d, _ := Codec_autoDecoderOverrides(true, decs, awItem{}).(JsonDecoder)
	res, ok := d.run(map[string]any{"id": "i1", "active": int64(1)}).(SkyResult[any, any])
	if !ok || res.Tag != 0 {
		t.Fatalf("dec: not Ok: %#v", res)
	}
	if out, _ := res.OkValue.(awItem); !out.Active {
		t.Errorf("active override should decode int 1 -> true, got %+v", res.OkValue)
	}
}

// ── v1 audit 2026-08-29: Codec.auto silent-data-loss regressions ─────────────

type acDictHolder struct {
	Counts map[string]int `sky:"counts,map[string]int"`
}

// Dict was silently encoded as `null` (the whole record) before the reflect.Map
// arm — the pinned persistence default writing null to the DB.
func TestCodecAutoDictRoundTrip(t *testing.T) {
	v := acDictHolder{Counts: map[string]int{"a": 1, "b": 2}}
	enc := Codec_autoEnc(true, v).(JsonValue)
	b, err := json.Marshal(enc.raw)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(b); got != `{"counts":{"a":1,"b":2}}` {
		t.Fatalf("Dict encoded wrong (was `null` before the fix): %s", got)
	}
	dec := Codec_autoDecoder(true, acDictHolder{}).(JsonDecoder)
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	res := dec.run(raw).(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("Dict decode failed: %+v", res)
	}
	back := res.OkValue.(acDictHolder)
	if back.Counts["a"] != 1 || back.Counts["b"] != 2 {
		t.Errorf("Dict round-trip lost data: %+v", back.Counts)
	}
}

type acDecimalHolder struct {
	Qty Std_Decimal_Decimal_forTest `sky:"qty,Std_Decimal_Decimal"`
}

// A Decimal field is an rt.SkyADT under the hood; the codec must encode it as
// its canonical string (was `null` before the fix). We mirror the generated
// `type Std_Decimal_Decimal = rt.SkyADT` with a local alias for the test.
type Std_Decimal_Decimal_forTest = SkyADT

func TestCodecAutoDecimalRoundTrip(t *testing.T) {
	v := acDecimalHolder{Qty: decimalBox(mustDecimal("3.14"))}
	enc := Codec_autoEnc(true, v).(JsonValue)
	b, _ := json.Marshal(enc.raw)
	if got := string(b); got != `{"qty":"3.14"}` {
		t.Fatalf("Decimal encoded wrong (was `null`): %s", got)
	}
	dec := Codec_autoDecoder(true, acDecimalHolder{}).(JsonDecoder)
	var raw any
	_ = json.Unmarshal(b, &raw)
	res := dec.run(raw).(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("Decimal decode failed: %+v", res)
	}
	back := res.OkValue.(acDecimalHolder)
	if decimalUnbox(back.Qty).String() != "3.14" {
		t.Errorf("Decimal round-trip wrong: %s", decimalUnbox(back.Qty).String())
	}
}

type acUnencodable struct {
	Bad chan int `sky:"bad,chan"`
}

// FAIL LOUD: an un-encodable field must PANIC, never silently null the whole
// record (which discarded all the OTHER fields' data too). Regression for the
// `return JsonValue{raw:nil}` swallow.
func TestCodecAutoFailsLoudNotSilent(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("Codec.auto silently encoded an un-encodable field instead of panicking")
		}
	}()
	Codec_autoEnc(true, acUnencodable{Bad: make(chan int)})
	t.Fatal("unreachable — Codec_autoEnc should have panicked")
}

func mustDecimal(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return d
}

type acTagFieldRecord struct {
	Tag string `sky:"tag,string"`
	N   int    `sky:"n,int"`
}

type acOuterWithTagField struct {
	Box acTagFieldRecord `sky:"box,acTagFieldRecord"`
}

// A plain record with a field literally NAMED `tag` must round-trip as a JSON
// object, NOT be mistaken for an ADT wire `{"tag":<variant>,...}` and fed to
// BuildAdtFromWire. Regression for the #5 decode arm over-triggering on any
// map carrying a string `tag` key — the CodecConformanceTest `Inner = {tag,n}`
// nested-record case is the Sky-level twin of this.
func TestCodecAutoTagNamedFieldIsNotAnAdt(t *testing.T) {
	v := acOuterWithTagField{Box: acTagFieldRecord{Tag: "hello", N: 3}}
	enc := Codec_autoEnc(true, v).(JsonValue)
	b, _ := json.Marshal(enc.raw)
	if got := string(b); got != `{"box":{"tag":"hello","n":3}}` {
		t.Fatalf("record with `tag` field encoded wrong: %s", got)
	}
	dec := Codec_autoDecoder(true, acOuterWithTagField{}).(JsonDecoder)
	var raw any
	_ = json.Unmarshal(b, &raw)
	res := dec.run(raw).(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("record with `tag` field failed to decode (mistaken for ADT?): %+v", res)
	}
	back := res.OkValue.(acOuterWithTagField)
	if back.Box.Tag != "hello" || back.Box.N != 3 {
		t.Errorf("record with `tag` field round-trip lost data: %+v", back.Box)
	}
}

type acDictDecimalHolder struct {
	Prices map[string]Std_Decimal_Decimal_forTest `sky:"prices,map[string]Std_Decimal_Decimal"`
}

// A Dict whose VALUE is a data-carrying ADT (here Decimal, an rt.SkyADT) must
// round-trip. Before the typed-path `map[` arm, the Dict fell to the reflect
// decode path, which sees the erased `rt.SkyADT` alias and errored "cannot
// derive data-carrying ADT" — a decode failure on well-formed data. The List
// and Maybe arms already threaded their element type; Dict was the gap.
func TestCodecAutoDictOfDecimalRoundTrip(t *testing.T) {
	v := acDictDecimalHolder{Prices: map[string]Std_Decimal_Decimal_forTest{
		"a": decimalBox(mustDecimal("12.99")),
		"b": decimalBox(mustDecimal("0.01")),
	}}
	enc := Codec_autoEnc(true, v).(JsonValue)
	b, _ := json.Marshal(enc.raw)
	if got := string(b); got != `{"prices":{"a":"12.99","b":"0.01"}}` {
		t.Fatalf("Dict-of-Decimal encoded wrong: %s", got)
	}
	dec := Codec_autoDecoder(true, acDictDecimalHolder{}).(JsonDecoder)
	var raw any
	_ = json.Unmarshal(b, &raw)
	res := dec.run(raw).(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("Dict-of-Decimal decode FAILED (the data-carrying-ADT gap): %+v", res)
	}
	back := res.OkValue.(acDictDecimalHolder)
	if decimalUnbox(back.Prices["a"]).String() != "12.99" || decimalUnbox(back.Prices["b"]).String() != "0.01" {
		t.Errorf("Dict-of-Decimal round-trip lost data: %+v", back.Prices)
	}
}
