package rt

import (
	"encoding/json"
	"reflect"
	"testing"
)

// Std.I18n's Text and Locale round-trip through Codec.auto by their declared
// owner, with the documented JSON shapes, and a malformed value is refused.
func TestCodecAutoI18nRoundTrip(t *testing.T) {
	adt := reflect.TypeOf(SkyADT{})
	cases := []struct {
		declared string
		value    SkyADT
		json     string
	}{
		{"Std_I18n_Locale", SkyADT{SkyOwner: skyI18nLocaleOwner, SkyName: i18nLocaleCtor, Fields: []any{"zh-Hant-HK"}}, `"zh-Hant-HK"`},
		{"Std_I18n_Text", SkyADT{SkyOwner: skyI18nTextOwner, Tag: 1, SkyName: i18nTextVerbatim, Fields: []any{"Ada"}}, `{"verbatim":"Ada"}`},
		{"Std_I18n_Text", SkyADT{SkyOwner: skyI18nTextOwner, SkyName: i18nTextMessage, Fields: []any{"shop.added",
			[]any{SkyTuple3{V0: "item", V1: "s", V2: "tea"}, SkyTuple3{V0: "n", V1: "i", V2: "2"}}}},
			`{"key":"shop.added","args":[["item","s","tea"],["n","i","2"]]}`},
		{"Std_I18n_Text", SkyADT{SkyOwner: skyI18nTextOwner, SkyName: i18nTextMessage, Fields: []any{"k", []any{}}},
			`{"key":"k","args":[]}`},
	}
	n := 0
	for _, c := range cases {
		raw, err := codecAutoEncodeTyped(reflect.ValueOf(c.value), c.declared, true)
		if err != nil {
			t.Fatalf("%s encode: %v", c.declared, err)
		}
		b, err := json.Marshal(raw)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if string(b) != c.json {
			t.Errorf("%s: wrote %s, want %s", c.declared, b, c.json)
		}
		var back any
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
		got, err := codecAutoDecodeTyped(adt, c.declared, back, true)
		if err != nil {
			t.Fatalf("%s decode: %v", c.declared, err)
		}
		g := got.Interface().(SkyADT)
		again, err := codecAutoEncodeTyped(reflect.ValueOf(g), c.declared, true)
		if err != nil {
			t.Fatalf("%s re-encode: %v", c.declared, err)
		}
		if b2, _ := json.Marshal(again); string(b2) != c.json {
			t.Errorf("%s: the read value writes %s, want %s", c.declared, b2, c.json)
		}
		if g.SkyOwner != c.value.SkyOwner || g.SkyName != c.value.SkyName || g.Tag != c.value.Tag {
			t.Errorf("%s: read %+v", c.declared, g)
		}
		n++
	}
	for _, bad := range []struct{ declared, json string }{
		{"Std_I18n_Locale", `"en GB"`},
		{"Std_I18n_Locale", `42`},
		{"Std_I18n_Text", `{"args":[]}`},
		{"Std_I18n_Text", `{"key":"k","args":[["a","x","b"]]}`},
		{"Std_I18n_Text", `{"key":"k","args":[["a","s"]]}`},
		{"Std_I18n_Text", `{"verbatim":1}`},
	} {
		var v any
		_ = json.Unmarshal([]byte(bad.json), &v)
		if _, err := codecAutoDecodeTyped(adt, bad.declared, v, true); err == nil {
			t.Errorf("%s %s: want a refusal", bad.declared, bad.json)
		}
		n++
	}
	t.Logf("ASSERTIONS: %d", n)
}
