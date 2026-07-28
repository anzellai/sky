// Package rt — Codec.auto: derive a codec from a record type by reflection.
//
// Reads the `sky:"name,type"` field tags (emitted by codegen S3) to recover
// field names and drive encode/decode. Handles scalars, Maybe (↔null), nested
// records, lists, and nullary enums (as their ordinal int). Data-carrying ADTs
// error clearly — they need an explicit `taggedUnion` codec.
package rt

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// skyTagName returns the Sky field name from a struct field's `sky:"name,type"`
// tag, falling back to lower-casing the Go field name.
func skyTagName(f reflect.StructField) string {
	if tag := f.Tag.Get("sky"); tag != "" {
		if i := strings.IndexByte(tag, ','); i >= 0 {
			return tag[:i]
		}
		return tag
	}
	if f.Name == "" {
		return ""
	}
	return strings.ToLower(f.Name[:1]) + f.Name[1:]
}

// skyTagType returns the declared Go type from a field's `sky:"name,type"` tag,
// or "" if absent — this is the metadata enum fields need (their Go kind is a
// bare int that reflection can't map to the enum registry without it).
func skyTagType(f reflect.StructField) string {
	tag := f.Tag.Get("sky")
	if i := strings.IndexByte(tag, ','); i >= 0 {
		return tag[i+1:]
	}
	return ""
}

func isSkyMaybeType(t reflect.Type) bool {
	if t.Kind() != reflect.Struct {
		return false
	}
	_, hasTag := t.FieldByName("Tag")
	_, hasJust := t.FieldByName("JustValue")
	return hasTag && hasJust
}

func isSkyAdtType(t reflect.Type) bool {
	if t.Kind() != reflect.Struct {
		return false
	}
	_, hasName := t.FieldByName("SkyName")
	_, hasFields := t.FieldByName("Fields")
	return hasName && hasFields
}

func isRecordType(t reflect.Type) bool {
	return t.Kind() == reflect.Struct && !isSkyMaybeType(t) && !isSkyAdtType(t)
}

// ── Encode: value → JSON raw ─────────────────────────────────────────────────

func codecAutoEncodeVal(rv reflect.Value) (any, error) {
	switch rv.Kind() {
	case reflect.String:
		return rv.String(), nil
	case reflect.Bool:
		return rv.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int64(rv.Uint()), nil
	case reflect.Float32, reflect.Float64:
		return rv.Float(), nil
	case reflect.Slice, reflect.Array:
		out := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			e, err := codecAutoEncodeVal(rv.Index(i))
			if err != nil {
				return nil, err
			}
			out[i] = e
		}
		return out, nil
	case reflect.Struct:
		t := rv.Type()
		if isSkyMaybeType(t) {
			if rv.FieldByName("Tag").Int() != 0 { // Nothing
				return nil, nil
			}
			return codecAutoEncodeVal(rv.FieldByName("JustValue"))
		}
		if isSkyAdtType(t) {
			return nil, fmt.Errorf("Codec.auto: cannot derive data-carrying ADT %q — use an explicit taggedUnion codec", t.Name())
		}
		return codecAutoEncodeStruct(rv)
	case reflect.Interface:
		if rv.IsNil() {
			return nil, nil
		}
		return codecAutoEncodeVal(rv.Elem())
	default:
		return nil, fmt.Errorf("Codec.auto: cannot encode kind %s", rv.Kind())
	}
}

func codecAutoEncodeStruct(rv reflect.Value) (any, error) {
	t := rv.Type()
	obj := jsonOrderedObject{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		raw, err := codecAutoEncodeTyped(rv.Field(i), skyTagType(f))
		if err != nil {
			return nil, err
		}
		obj.keys = append(obj.keys, skyTagName(f))
		obj.vals = append(obj.vals, raw)
	}
	return obj, nil
}

// codecAutoEncodeTyped encodes a value using its declared Sky type (from the
// field tag): a registered enum → its readable name; Maybe[T]/[]T unwrap the
// inner type; everything else falls back to value-based encoding.
func codecAutoEncodeTyped(rv reflect.Value, declaredType string) (any, error) {
	if declaredType != "" {
		if isRegisteredEnum(declaredType) {
			switch rv.Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				if name, ok := enumNameForOrdinal(declaredType, int(rv.Int())); ok {
					return name, nil
				}
			}
		}
		if inner, ok := strings.CutPrefix(declaredType, "rt.SkyMaybe["); ok && rv.Kind() == reflect.Struct && isSkyMaybeType(rv.Type()) {
			if rv.FieldByName("Tag").Int() != 0 {
				return nil, nil
			}
			return codecAutoEncodeTyped(rv.FieldByName("JustValue"), strings.TrimSuffix(inner, "]"))
		}
		if elem, ok := strings.CutPrefix(declaredType, "[]"); ok && rv.Kind() == reflect.Slice {
			out := make([]any, rv.Len())
			for i := 0; i < rv.Len(); i++ {
				e, err := codecAutoEncodeTyped(rv.Index(i), elem)
				if err != nil {
					return nil, err
				}
				out[i] = e
			}
			return out, nil
		}
	}
	return codecAutoEncodeVal(rv)
}

// Codec_autoEnc : a -> Value. Reflects the record into a JSON object Value.
func Codec_autoEnc(record any) any {
	raw, err := codecAutoEncodeVal(reflect.ValueOf(record))
	if err != nil {
		// Encoding can't return a Result; surface as a JSON string error marker
		// is worse than a clear panic-free empty — but auto validates via cols,
		// so a genuine underivable type is caught at derivation. Return null.
		return JsonValue{raw: nil}
	}
	return JsonValue{raw: raw}
}

// ── Decode: JSON raw → value ─────────────────────────────────────────────────

func codecAutoDecodeVal(rt reflect.Type, raw any) (reflect.Value, error) {
	switch rt.Kind() {
	case reflect.String:
		return reflect.ValueOf(codecRawStr(raw)).Convert(rt), nil
	case reflect.Bool:
		if b, ok := raw.(bool); ok {
			return reflect.ValueOf(b).Convert(rt), nil
		}
		return reflect.ValueOf(dbTruthy(codecRawStr(raw))).Convert(rt), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflect.ValueOf(codecRawInt(raw)).Convert(rt), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return reflect.ValueOf(uint64(codecRawInt(raw))).Convert(rt), nil
	case reflect.Float32, reflect.Float64:
		return reflect.ValueOf(codecRawFloat(raw)).Convert(rt), nil
	case reflect.Slice:
		items, ok := raw.([]any)
		if !ok {
			return reflect.MakeSlice(rt, 0, 0), nil
		}
		out := reflect.MakeSlice(rt, len(items), len(items))
		for i, it := range items {
			ev, err := codecAutoDecodeVal(rt.Elem(), it)
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(i).Set(ev)
		}
		return out, nil
	case reflect.Struct:
		if isSkyMaybeType(rt) {
			out := reflect.New(rt).Elem()
			if raw == nil {
				out.FieldByName("Tag").SetInt(1) // Nothing
				return out, nil
			}
			out.FieldByName("Tag").SetInt(0) // Just
			inner, err := codecAutoDecodeVal(out.FieldByName("JustValue").Type(), raw)
			if err != nil {
				return reflect.Value{}, err
			}
			out.FieldByName("JustValue").Set(inner)
			return out, nil
		}
		if isSkyAdtType(rt) {
			return reflect.Value{}, fmt.Errorf("Codec.auto: cannot derive data-carrying ADT %q", rt.Name())
		}
		return codecAutoDecodeStruct(rt, raw)
	default:
		return reflect.Value{}, fmt.Errorf("Codec.auto: cannot decode kind %s", rt.Kind())
	}
}

func codecAutoDecodeStruct(rt reflect.Type, raw any) (reflect.Value, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return reflect.Value{}, fmt.Errorf("Codec.auto: expected object for %s", rt.Name())
	}
	out := reflect.New(rt).Elem()
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.PkgPath != "" {
			continue
		}
		fv, err := codecAutoDecodeTyped(f.Type, skyTagType(f), m[skyTagName(f)])
		if err != nil {
			return reflect.Value{}, err
		}
		out.Field(i).Set(fv)
	}
	return out, nil
}

// codecAutoDecodeTyped decodes using the declared Sky type (from the field tag):
// a registered enum decodes its name back to the ordinal; Maybe[T]/[]T unwrap.
func codecAutoDecodeTyped(gt reflect.Type, declaredType string, raw any) (reflect.Value, error) {
	if declaredType != "" {
		if isRegisteredEnum(declaredType) {
			switch gt.Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				if ord, ok := enumOrdinalForName(declaredType, codecRawStr(raw)); ok {
					return reflect.ValueOf(int64(ord)).Convert(gt), nil
				}
			}
		}
		if inner, ok := strings.CutPrefix(declaredType, "rt.SkyMaybe["); ok && isSkyMaybeType(gt) {
			out := reflect.New(gt).Elem()
			if raw == nil {
				out.FieldByName("Tag").SetInt(1)
				return out, nil
			}
			out.FieldByName("Tag").SetInt(0)
			iv, err := codecAutoDecodeTyped(out.FieldByName("JustValue").Type(), strings.TrimSuffix(inner, "]"), raw)
			if err != nil {
				return reflect.Value{}, err
			}
			out.FieldByName("JustValue").Set(iv)
			return out, nil
		}
		if elem, ok := strings.CutPrefix(declaredType, "[]"); ok && gt.Kind() == reflect.Slice {
			items, isList := raw.([]any)
			if !isList {
				return reflect.MakeSlice(gt, 0, 0), nil
			}
			out := reflect.MakeSlice(gt, len(items), len(items))
			for i, it := range items {
				ev, err := codecAutoDecodeTyped(gt.Elem(), elem, it)
				if err != nil {
					return reflect.Value{}, err
				}
				out.Index(i).Set(ev)
			}
			return out, nil
		}
	}
	return codecAutoDecodeVal(gt, raw)
}

// Codec_autoDecoder : a -> Decoder a. A JSON decoder that reflection-builds the
// witness's type.
func Codec_autoDecoder(witness any) any {
	wt := reflect.TypeOf(witness)
	return JsonDecoder{run: func(raw any) any {
		v, err := codecAutoDecodeVal(wt, raw)
		if err != nil {
			return Err[any, any](ErrDecode(err.Error()))
		}
		return Ok[any, any](v.Interface())
	}}
}

// Codec_autoCols : a -> List (String, String). The (name, kind) columns for the
// DB shape, derived from the record's fields. Returns an empty list if the
// witness is not a record.
func Codec_autoCols(witness any) any {
	wt := reflect.TypeOf(witness)
	if wt == nil || !isRecordType(wt) {
		return []any{}
	}
	out := []any{}
	for i := 0; i < wt.NumField(); i++ {
		f := wt.Field(i)
		if f.PkgPath != "" {
			continue
		}
		out = append(out, T2[any, any]{V0: skyTagName(f), V1: codecColKindTyped(f.Type, skyTagType(f))})
	}
	return out
}

// codecColKindTyped: an enum column (registered type, incl. inside Maybe) is
// stored as its readable name → "text"; otherwise fall back to the Go type.
func codecColKindTyped(t reflect.Type, declaredType string) string {
	if declaredType != "" {
		if isRegisteredEnum(declaredType) {
			return "text"
		}
		if inner, ok := strings.CutPrefix(declaredType, "rt.SkyMaybe["); ok {
			if isRegisteredEnum(strings.TrimSuffix(inner, "]")) {
				return "text"
			}
		}
	}
	return codecColKind(t)
}

func codecColKind(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "text"
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "int"
	case reflect.Float32, reflect.Float64:
		return "real"
	case reflect.Struct:
		if isSkyMaybeType(t) {
			if jv, ok := t.FieldByName("JustValue"); ok {
				return codecColKind(jv.Type) // nullable scalar keeps its kind
			}
		}
		return "blob"
	default:
		return "blob"
	}
}

// ── raw coercion helpers ─────────────────────────────────────────────────────

func codecRawStr(raw any) string {
	if raw == nil {
		return ""
	}
	if s, ok := raw.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", raw)
}

func codecRawInt(raw any) int64 {
	switch v := raw.(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case string:
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

func codecRawFloat(raw any) float64 {
	switch v := raw.(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return 0
}

// ── Enum registry (populated by codegen init()s) ─────────────────────────────

// enumRegistry maps an enum type name → its ordered variant names. Written only
// from generated init() functions (single-threaded at startup); read-only after.
var enumRegistry = map[string][]string{}

// RegisterEnum records an enum type's variant names, in ordinal order.
func RegisterEnum(name string, variants []string) { enumRegistry[name] = variants }

func isRegisteredEnum(typeName string) bool { _, ok := enumRegistry[typeName]; return ok }

func enumNameForOrdinal(typeName string, ord int) (string, bool) {
	vs, ok := enumRegistry[typeName]
	if !ok || ord < 0 || ord >= len(vs) {
		return "", false
	}
	return vs[ord], true
}

func enumOrdinalForName(typeName, name string) (int, bool) {
	vs, ok := enumRegistry[typeName]
	if !ok {
		return 0, false
	}
	for i, v := range vs {
		if v == name {
			return i, true
		}
	}
	return 0, false
}
