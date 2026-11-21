package rt

import (
	"encoding/json"
	"strconv"
)

// The Float key text of Codec.dict (Std/Codec.sky). Codec.auto writes a
// `Dict Float v` key as dictFloatKeyText of the key, so Codec.dict writes the
// same text, and reads back only that text.

// The `propertyNames` patterns of a Dict's JSON Schema. Std/Codec.sky
// (intKeyPattern, floatKeyPattern) writes the same text. Neither bounds the
// size of a key, and the Float one admits spellings the reader refuses, so a
// schema that holds one is not exact.
const (
	codecIntKeyPattern   = `^(0|-?[1-9][0-9]*)$`
	codecFloatKeyPattern = `^(NaN|[+-]Inf|-?[0-9]+(\\.[0-9]+)?(e[+-][0-9]+)?)$`
)

// Codec_floatKeyText : Value -> String — the object key Codec.dict writes for
// a Float key, given the key codec's JSON of it. A Value that is not a number
// is written as its JSON text, which the reader refuses.
func Codec_floatKeyText(v any) any {
	raw := v
	if jv, ok := v.(JsonValue); ok {
		raw = jv.raw
	}
	switch n := raw.(type) {
	case float64:
		return dictFloatKeyText(n)
	case int:
		return dictFloatKeyText(float64(n))
	case int64:
		return dictFloatKeyText(float64(n))
	case json.Number:
		if f, err := strconv.ParseFloat(n.String(), 64); err == nil {
			return dictFloatKeyText(f)
		}
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return "null"
	}
	return string(b)
}

// Codec_floatKeyValue : String -> Value — the Float a member name stands for,
// as a JSON Value, when the name is the one text Codec.dict writes for that
// Float ("1.5", "1e+21", "NaN", "+Inf"). Any other spelling ("1.50", "-0",
// "0x1p0", "inf") is JSON null, which the caller refuses with the name.
func Codec_floatKeyValue(s any) any {
	text := AsString(s)
	f, err := strconv.ParseFloat(text, 64)
	if err != nil || dictFloatKeyText(f) != text {
		return JsonValue{raw: nil}
	}
	return JsonValue{raw: f}
}
