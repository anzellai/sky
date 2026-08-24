package rt

import (
	"reflect"
)

// asInt64 narrows an `any` to int64 for stream ids. Accepts int /
// int32 / int64 / float64 (JSON-decoded number) AND a single-field
// SkyADT (StreamId Int → unwrap to the inner Int).
//
// The SkyADT unwrap covers the call from `Sub.subscribeStream sid`
// when `sid` arrives as the typed `StreamId Int` value — the
// kernel signature can't see the inner Int without it.
//
// Unknown shapes fall through to AsInt (which returns 0 on
// unrecognised types, so lookupStream then resolves to nil →
// idempotent no-op in close).
func asInt64(v any) int64 {
	if v == nil {
		return 0
	}
	switch x := v.(type) {
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	}
	// SkyADT wrap: type StreamId = StreamId Int — the runtime ADT
	// is `SkyADT{Tag:0, SkyName:"StreamId", Fields:[42]}`. Pull
	// Fields[0] and re-coerce.
	if adt, ok := v.(SkyADT); ok && len(adt.Fields) == 1 {
		return asInt64(adt.Fields[0])
	}
	// User-defined-ADT struct fallback. Sky's codegen emits
	// `type StreamId struct{Tag int; SkyName string; Fields []any}`
	// — same layout as SkyADT but a distinct Go type. Reflect to
	// extract Fields[0] when the type-assertion to SkyADT fails.
	rv := reflect.ValueOf(v)
	if rv.IsValid() && rv.Kind() == reflect.Struct {
		fieldsF := rv.FieldByName("Fields")
		if fieldsF.IsValid() && fieldsF.Kind() == reflect.Slice && fieldsF.Len() == 1 {
			return asInt64(fieldsF.Index(0).Interface())
		}
	}
	return int64(AsInt(v))
}
