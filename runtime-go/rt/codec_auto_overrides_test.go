package rt

import (
	"encoding/json"
	"strings"
	"testing"
)

// Codec.autoWith runs each override's decoder, which returns an erased value
// (a `Codec.maybe Codec.int` decoder yields `SkyMaybe[any]`). The record field
// is typed (`SkyMaybe[int]`). Before v0.29.1 the decoder only assigned or
// converted the value, and a SkyMaybe[any] is neither assignable nor
// convertible to SkyMaybe[int], so the field kept its zero value with no
// error: 3000 read as `Just 0` and null read as `Just 0`.

type ovInner struct {
	N int `sky:"n,int"`
}

type ovRec struct {
	Id     int                     `sky:"id,int"`
	Pub    SkyMaybe[int]           `sky:"pub,rt.SkyMaybe[int]"`
	Name   string                  `sky:"name,string"`
	Ratio  SkyMaybe[float64]       `sky:"ratio,rt.SkyMaybe[float64]"`
	Flag   SkyMaybe[bool]          `sky:"flag,rt.SkyMaybe[bool]"`
	Note   SkyMaybe[string]        `sky:"note,rt.SkyMaybe[string]"`
	In     SkyMaybe[ovInner]       `sky:"in,rt.SkyMaybe[ovInner]"`
	Nested SkyMaybe[SkyMaybe[int]] `sky:"nested,rt.SkyMaybe[rt.SkyMaybe[int]]"`
}

func ovBlank() ovRec {
	return ovRec{
		Pub:    Nothing[int](),
		Ratio:  Nothing[float64](),
		Flag:   Nothing[bool](),
		Note:   Nothing[string](),
		In:     Nothing[ovInner](),
		Nested: Nothing[SkyMaybe[int]](),
	}
}

func ovInnerDecoder() any {
	return JsonDecoder{run: func(raw any) any {
		m, ok := raw.(map[string]any)
		if !ok {
			return Err[any, any](ErrDecode("expected object"))
		}
		r := runDec(JsonDec_int(), m["n"])
		if isErr(r) {
			return r
		}
		return Ok[any, any](ovInner{N: okVal(r).(int)})
	}}
}

func ovDecoders() map[string]any {
	return map[string]any{
		"pub":    JsonDec_nullable(JsonDec_int()),
		"ratio":  JsonDec_nullable(JsonDec_float()),
		"flag":   JsonDec_nullable(JsonDec_bool()),
		"note":   JsonDec_nullable(JsonDec_string()),
		"in":     JsonDec_nullable(ovInnerDecoder()),
		"nested": JsonDec_nullable(JsonDec_nullable(JsonDec_int())),
	}
}

func ovDecode(t *testing.T, cols []string, text string) SkyResult[any, any] {
	t.Helper()
	all := ovDecoders()
	decs := []any{}
	for _, c := range cols {
		decs = append(decs, T2[any, any]{V0: c, V1: all[c]})
	}
	var raw any
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		t.Fatal(err)
	}
	return Codec_autoDecoderOverrides(true, decs, ovBlank()).(JsonDecoder).run(raw).(SkyResult[any, any])
}

const ovValues = `{"id":1,"pub":3000,"name":"a","ratio":2.5,"flag":false,"note":"","in":{"n":7},"nested":0}`
const ovNulls = `{"id":2,"pub":null,"name":"b","ratio":null,"flag":null,"note":null,"in":null,"nested":null}`

func TestCodecAutoWithOverridesNarrowEveryMaybeField(t *testing.T) {
	sets := [][]string{
		{"pub"},
		{"pub", "note"},
		{"ratio", "flag", "in"},
		{"pub", "ratio", "flag", "note", "in", "nested"},
	}
	for _, cols := range sets {
		name := strings.Join(cols, ",")
		r := ovDecode(t, cols, ovValues)
		if r.Tag != 0 {
			t.Fatalf("%s: values did not decode: %s", name, errorMessage(r.ErrValue))
		}
		v := r.OkValue.(ovRec)
		want := ovRec{
			Id: 1, Pub: Just[int](3000), Name: "a", Ratio: Just[float64](2.5),
			Flag: Just[bool](false), Note: Just[string](""), In: Just[ovInner](ovInner{N: 7}),
			Nested: Just[SkyMaybe[int]](Just[int](0)),
		}
		if v != want {
			t.Fatalf("%s: values read as %+v, want %+v", name, v, want)
		}
		r = ovDecode(t, cols, ovNulls)
		if r.Tag != 0 {
			t.Fatalf("%s: nulls did not decode: %s", name, errorMessage(r.ErrValue))
		}
		v = r.OkValue.(ovRec)
		if v != (ovRec{Id: 2, Name: "b", Pub: Nothing[int](), Ratio: Nothing[float64](),
			Flag: Nothing[bool](), Note: Nothing[string](), In: Nothing[ovInner](),
			Nested: Nothing[SkyMaybe[int]]()}) {
			t.Fatalf("%s: nulls read as %+v", name, v)
		}
	}
}

// An override whose value cannot become the field's type is an Err naming the
// column, never a field left at its zero value.
func TestCodecAutoWithOverrideOfTheWrongTypeIsAnErr(t *testing.T) {
	decs := []any{T2[any, any]{V0: "pub", V1: JsonDec_nullable(JsonDec_string())}}
	var raw any
	_ = json.Unmarshal([]byte(`{"id":1,"pub":"x","name":"a"}`), &raw)
	r := Codec_autoDecoderOverrides(true, decs, ovBlank()).(JsonDecoder).run(raw).(SkyResult[any, any])
	if r.Tag == 0 {
		t.Fatalf("a String override for a Maybe Int field decoded: %+v", r.OkValue)
	}
	if !strings.Contains(errorMessage(r.ErrValue), "pub") {
		t.Fatalf("the Err does not name the column: %s", errorMessage(r.ErrValue))
	}
}

// An override encoder that returns something other than a JSON value is a
// classified encode failure, never a silent null.
func TestCodecAutoWithOverrideEncoderThatReturnsNoJSONPanics(t *testing.T) {
	encs := []any{T2[any, any]{V0: "pub", V1: func(any) any { return "not json" }}}
	msg := func() (msg string) {
		defer func() {
			if r := recover(); r != nil {
				msg, _ = r.(string)
			}
		}()
		_ = Codec_autoEncOverrides(true, encs, ovBlank())
		return ""
	}()
	if msg == "" {
		t.Fatal("an override encoder that returned no JSON value wrote null")
	}
	if kind, _ := classifyPanic(msg); kind != "JsonEncodeFailure" {
		t.Fatalf("panic %q is classified %s, want JsonEncodeFailure", msg, kind)
	}
}
