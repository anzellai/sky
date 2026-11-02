package rt

// The write check of a `Codec.auto` / `autoCamel` / `autoWith` codec
// (v0.29.2 fifth Judge). A codec is PARTIAL when its encoder can write a value
// its own decoder refuses; Std.Db.Store then refuses that value on a write
// (Codec.writeChecks), so no row is stored that every later read of the table
// fails on. For a hand-built codec Std.Codec derives partiality from the codec
// tree. For an auto codec the tree is the record's Go type, so it is derived
// here, from that type: a field is partial when its type holds a Decimal or a
// Money at any depth (a field, a List, a Maybe, a Dict, a nested record). The
// encoder writes a Decimal as its canonical text, and a computed Decimal
// (Decimal.mul of two long ones) can be longer than the decimalStrictMaxLen
// characters the strict reader takes back. Every other field type is total.
//
// Codec_autoCols marks such a column's kind with the flag `|check`. Std.Codec
// then checks it on a write with the codec's own reader (its blank record with
// the written value in that column), so the check and the reader cannot
// disagree, and no kernel call is added (the kinds are read already).

import (
	"reflect"
	"strings"
)

// codecAutoPartialNames are the Go names of the leaf types an auto field can
// hold that its reader can refuse after its writer wrote them.
var codecAutoPartialNames = []string{"Std_Decimal_Decimal", "Std_Money_Money"}

// codecAutoTypePartial reports whether a field of Go type `t`, declared as
// `declared` (the `sky:"name,type"` tag), holds a partial leaf at any depth.
func codecAutoTypePartial(t reflect.Type, declared string, depth int) bool {
	for _, n := range codecAutoPartialNames {
		if strings.Contains(declared, n) {
			return true
		}
	}
	if t == nil || depth > 32 {
		return false
	}
	for _, n := range codecAutoPartialNames {
		if t.Name() == n {
			return true
		}
	}
	switch t.Kind() {
	case reflect.Slice, reflect.Array, reflect.Pointer, reflect.Map:
		return codecAutoTypePartial(t.Elem(), "", depth+1)
	case reflect.Struct:
		if isSkyMaybeType(t) {
			if jv, ok := t.FieldByName("JustValue"); ok {
				return codecAutoTypePartial(jv.Type, "", depth+1)
			}
			return false
		}
		if !isRecordType(t) {
			return false
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue
			}
			if codecAutoTypePartial(f.Type, skyTagType(f), depth+1) {
				return true
			}
		}
	}
	return false
}

// Codec_enumNotListed : v -> Value. What Codec.enum writes for a value that is
// not in its pairs (v0.29.2 fifth Judge): `{"notInEnum": "<value>"}`, the value
// in Sky syntax (`Suspended`). Every reader refuses it, naming the value, and
// a Std.Db.Store write refuses the row. It used to be `""`, which read back as
// another value when `""` was one of the names, and as an unreadable row
// otherwise.
func Codec_enumNotListed(v any) any {
	return JsonValue{raw: jsonOrderedObject{keys: []string{"notInEnum"}, vals: []any{SkyShow(v)}}}
}

// Codec_enumString : List String -> Decoder String. The text reader of
// Codec.enum: a JSON string reads as itself (the enum then looks the name up),
// and the `{"notInEnum": …}` object Codec_enumNotListed writes is an error
// naming the value and the enum's names, at any depth (an element of a
// Codec.list names it too). Anything else fails as Json.Decode.string does.
// A decoder is a Value-like `any`, so the codec pays no runtime narrowing for
// it.
func Codec_enumString(namesArg any) any {
	names := []string{}
	for _, n := range asList(namesArg) {
		if s, ok := n.(string); ok {
			names = append(names, s)
		}
	}
	return JsonDecoder{run: func(v any) any {
		if s, ok := v.(string); ok {
			return Ok[any, any](s)
		}
		if m, ok := jsonObjectMap(v); ok {
			if ctor, ok := m["notInEnum"].(string); ok && len(m) == 1 {
				return Err[any, any](ErrDecode("Codec.enum: the value " + ctor +
					" is not one of the enum's values (" + strings.Join(names, ", ") +
					"), so it has no name to write"))
			}
		}
		return Err[any, any](ErrDecode("expected String, got " + jsonValueKind(v)))
	}}
}
