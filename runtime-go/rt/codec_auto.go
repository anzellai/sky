// Package rt — Codec.auto: derive a codec from a record type by reflection.
//
// Reads the `sky:"name,type"` field tags (emitted by codegen S3) to recover
// field names and drive encode/decode. Handles scalars, Maybe (↔null), nested
// records, lists, and nullary enums (as their ordinal int). Data-carrying ADTs
// error clearly — they need an explicit `taggedUnion` codec.
package rt

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
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

// camelToSnake converts a camelCase identifier to snake_case
// (priceMinor → price_minor). Single-word names are unchanged.
func camelToSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			b.WriteRune(r - 'A' + 'a')
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Db_snakeName is the Std.Db.Store kernel behind `Store.snakeName` — it exposes
// the exact `camelToSnake` used for column derivation so the query builder can
// resolve a record FIELD name (`priceMinor`) to its snake column (`price_minor`).
func Db_snakeName(s any) any { return camelToSnake(AsString(s)) }

// skyColKey is the DB column / JSON key a record field maps to under `Codec.auto`
// — snake_case by default (the DB convention), so `priceMinor` → `price_minor`
// with no hand-written codec. `Codec.autoCamel` keeps the raw camelCase name.
func skyColKey(f reflect.StructField, snake bool) string {
	name := skyTagName(f)
	if snake {
		return camelToSnake(name)
	}
	return name
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

func codecAutoEncodeVal(rv reflect.Value, snake bool) (any, error) {
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
			e, err := codecAutoEncodeVal(rv.Index(i), snake)
			if err != nil {
				return nil, err
			}
			out[i] = e
		}
		return out, nil
	case reflect.Map:
		// Dict k v → JSON object. `Dict String v` keys are already `string`
		// (encodeDictKey verbatim); other key types are stringified. Sorted for
		// determinism (Dict's own sorted-key contract). Before this arm a Dict
		// field hit `default:` → the whole record silently encoded as `null`.
		type kvpair struct {
			k string
			v reflect.Value
		}
		pairs := make([]kvpair, 0, rv.Len())
		for _, mk := range rv.MapKeys() {
			pairs = append(pairs, kvpair{fmt.Sprintf("%v", mk.Interface()), rv.MapIndex(mk)})
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].k < pairs[j].k })
		obj := jsonOrderedObject{}
		for _, p := range pairs {
			ev, err := codecAutoEncodeVal(p.v, snake)
			if err != nil {
				return nil, err
			}
			obj.keys = append(obj.keys, p.k)
			obj.vals = append(obj.vals, ev)
		}
		return obj, nil
	case reflect.Struct:
		t := rv.Type()
		if isSkyMaybeType(t) {
			if rv.FieldByName("Tag").Int() != 0 { // Nothing
				return nil, nil
			}
			return codecAutoEncodeVal(rv.FieldByName("JustValue"), snake)
		}
		// Set a → JSON array (sorted, deterministic via Set_toList). Before this
		// its sole unexported `items` field was skipped by codecAutoEncodeStruct
		// → `{}` (elements silently vanished).
		if t == reflect.TypeOf(SkySet{}) {
			elems := AsList(Set_toList(rv.Interface()))
			out := make([]any, len(elems))
			for i, e := range elems {
				ev, err := codecAutoEncodeVal(reflect.ValueOf(e), snake)
				if err != nil {
					return nil, err
				}
				out[i] = ev
			}
			return out, nil
		}
		// A data-carrying ADT (legacy `SkyADT` OR sealed-iface variant —
		// `unwrapADTShape` normalises both; a plain record returns ok=false).
		// Before this these fell to an error (→ null record) or, for a sealed
		// variant, were mis-read as a record (`{"v0":7}`, untagged, undecodable).
		if name, _, fields, ok := unwrapADTShape(rv.Interface()); ok {
			switch name {
			// Std.Decimal — shopspring-backed; its canonical string round-trips.
			case "Decimal__Internal":
				return decimalUnbox(rv.Interface()).String(), nil
			// Std.Money — the pinned currency default: {amount, currency}.
			case "Money":
				if len(fields) >= 2 {
					return jsonOrderedObject{
						keys: []string{"amount", "currency"},
						vals: []any{decimalUnbox(fields[0]).String(), currencyCodeOf(fields[1])},
					}, nil
				}
			}
			// General payload ADT → TAGGED `{"tag":<name>,"v0":…,…}`: nullary arms
			// are distinguished by tag and the whole thing decodes via
			// BuildAdtFromWire. (Nullary REGISTERED enums never reach here — they
			// lower to `int` and take the enum path — so this changes only the
			// data-carrying encoding, which previously did not round-trip at all.)
			obj := jsonOrderedObject{keys: []string{"tag"}, vals: []any{name}}
			for i, f := range fields {
				ev, err := codecAutoEncodeVal(reflect.ValueOf(f), snake)
				if err != nil {
					return nil, err
				}
				obj.keys = append(obj.keys, fmt.Sprintf("v%d", i))
				obj.vals = append(obj.vals, ev)
			}
			return obj, nil
		}
		return codecAutoEncodeStruct(rv, snake)
	case reflect.Interface:
		if rv.IsNil() {
			return nil, nil
		}
		return codecAutoEncodeVal(rv.Elem(), snake)
	default:
		return nil, fmt.Errorf("Codec.auto: cannot encode kind %s", rv.Kind())
	}
}

// codecOverrideMap turns a Sky `List (String, x)` into a col→x map (used for the
// per-field enc-closure / dec-JsonDecoder override lists that `Codec.autoWith`
// passes down).
func codecOverrideMap(arg any) map[string]any {
	m := map[string]any{}
	for _, e := range AsList(arg) {
		t := AsTuple2(e)
		m[AsString(t.V0)] = t.V1
	}
	return m
}

func codecDerefStruct(v any) reflect.Value {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Interface || rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return reflect.Value{}
		}
		rv = rv.Elem()
	}
	return rv
}

// Codec_autoEncOverrides is `Codec.autoWith`'s encoder: like Codec_autoEnc, but a
// top-level field whose column is in `encs` is encoded by CALLING that field's
// override enc-closure (`b -> Value`) instead of the reflection default.
func Codec_autoEncOverrides(snakeArg, encsArg, record any) any {
	snake := AsBool(snakeArg)
	ov := codecOverrideMap(encsArg)
	rv := codecDerefStruct(record)
	if !rv.IsValid() || rv.Kind() != reflect.Struct {
		return Codec_autoEnc(snakeArg, record)
	}
	t := rv.Type()
	obj := jsonOrderedObject{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		col := skyColKey(f, snake)
		var raw any
		if enc, ok := ov[col]; ok {
			if jv, isJV := SkyCall(enc, rv.Field(i).Interface()).(JsonValue); isJV {
				raw = jv.raw
			}
		} else {
			r, err := codecAutoEncodeTyped(rv.Field(i), skyTagType(f), snake)
			if err != nil {
				// Fail loud, never silently null the whole record (see Codec_autoEnc).
				panic("Codec.auto: cannot encode field " + col + " — " + err.Error())
			}
			raw = r
		}
		obj.keys = append(obj.keys, col)
		obj.vals = append(obj.vals, raw)
	}
	return JsonValue{raw: obj}
}

// Codec_autoDecoderOverrides is `Codec.autoWith`'s decoder: like Codec_autoDecoder,
// but a top-level field whose column is in `decs` is decoded by RUNNING that
// field's override `JsonDecoder` on the column's JSON value.
func Codec_autoDecoderOverrides(snakeArg, decsArg, witness any) any {
	snake := AsBool(snakeArg)
	ov := codecOverrideMap(decsArg)
	wt := reflect.TypeOf(witness)
	for wt != nil && wt.Kind() == reflect.Ptr {
		wt = wt.Elem()
	}
	return JsonDecoder{run: func(raw any) any {
		m, ok := raw.(map[string]any)
		if !ok || wt == nil || wt.Kind() != reflect.Struct {
			return Err[any, any](ErrDecode("Codec.autoWith: expected object"))
		}
		out := reflect.New(wt).Elem()
		for i := 0; i < wt.NumField(); i++ {
			f := wt.Field(i)
			if f.PkgPath != "" {
				continue
			}
			col := skyColKey(f, snake)
			if dec, ok := ov[col]; ok {
				jd, isJD := dec.(JsonDecoder)
				if !isJD {
					return Err[any, any](ErrDecode("Codec.autoWith: override for " + col + " is not a decoder"))
				}
				res := jd.run(m[col])
				r, isRes := res.(SkyResult[any, any])
				if !isRes || r.Tag != 0 {
					return res // propagate the override's Err verbatim
				}
				fvv := reflect.ValueOf(r.OkValue)
				switch {
				case fvv.IsValid() && fvv.Type().AssignableTo(f.Type):
					out.Field(i).Set(fvv)
				case fvv.IsValid() && fvv.Type().ConvertibleTo(f.Type):
					out.Field(i).Set(fvv.Convert(f.Type))
				}
			} else {
				key := skyColKey(f, snake)
				val, present := m[key]
				if !present && !codecFieldOptional(f) {
					return Err[any, any](ErrDecode(fmt.Sprintf("Codec.autoWith: missing required field %q", key)))
				}
				fv, err := codecAutoDecodeTyped(f.Type, skyTagType(f), val, snake)
				if err != nil {
					return Err[any, any](ErrDecode(err.Error()))
				}
				out.Field(i).Set(fv)
			}
		}
		return Ok[any, any](out.Interface())
	}}
}

func codecAutoEncodeStruct(rv reflect.Value, snake bool) (any, error) {
	t := rv.Type()
	obj := jsonOrderedObject{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		raw, err := codecAutoEncodeTyped(rv.Field(i), skyTagType(f), snake)
		if err != nil {
			return nil, err
		}
		obj.keys = append(obj.keys, skyColKey(f, snake))
		obj.vals = append(obj.vals, raw)
	}
	return obj, nil
}

// codecAutoEncodeTyped encodes a value using its declared Sky type (from the
// field tag): a registered enum → its readable name; Maybe[T]/[]T unwrap the
// inner type; everything else falls back to value-based encoding.
func codecAutoEncodeTyped(rv reflect.Value, declaredType string, snake bool) (any, error) {
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
			return codecAutoEncodeTyped(rv.FieldByName("JustValue"), strings.TrimSuffix(inner, "]"), snake)
		}
		if elem, ok := strings.CutPrefix(declaredType, "[]"); ok && rv.Kind() == reflect.Slice {
			out := make([]any, rv.Len())
			for i := 0; i < rv.Len(); i++ {
				e, err := codecAutoEncodeTyped(rv.Index(i), elem, snake)
				if err != nil {
					return nil, err
				}
				out[i] = e
			}
			return out, nil
		}
	}
	return codecAutoEncodeVal(rv, snake)
}

// currencyCodeOf renders a `Std.Money.Currency` value as its code string: a
// nullary arm (`USD`) → its name; a `CurrencyRaw "XYZ"` → the raw string.
func currencyCodeOf(cur any) string {
	name, _, fields, _ := unwrapADTShape(cur)
	if len(fields) > 0 {
		if s, ok := fields[0].(string); ok {
			return s
		}
	}
	return name
}

// sqlCodeToCurrency builds the Sky-side Currency ADT for a 3-letter ISO code.
// Named variants (USD/EUR/.../USDC) get a dedicated ADT with that SkyName;
// unknown codes fall through to `CurrencyRaw String`. Matches
// Std.Money.parseCurrency's semantics but lives at the runtime layer so the
// decoder doesn't reflect-call into Sky kernel code. It lives HERE (not in the
// `//go:build !js` db_decoder.go) because Codec.auto's Money JSON decode runs
// on the Sky.Spa wasm client too, and the function is a pure code→ADT switch
// with no DB dependency. The DB decoder (also !js) calls it from here.
func sqlCodeToCurrency(code string) SkyADT {
	switch code {
	case "USD", "EUR", "GBP", "JPY", "CHF", "AUD", "CAD", "NZD", "SEK", "NOK",
		"DKK", "CNY", "HKD", "SGD", "KRW", "TWD", "INR", "THB", "MYR", "IDR",
		"PHP", "VND", "BRL", "MXN", "ARS", "CLP", "ZAR", "TRY", "RUB", "UAH",
		"PLN", "CZK", "HUF", "RON", "BGN", "AED", "SAR", "QAR", "KWD", "BHD",
		"OMR", "JOD", "ILS", "EGP", "NGN", "KES", "GHS", "MAD", "TND", "DZD",
		"PKR", "BDT", "LKR", "NPR",
		"BTC", "ETH", "USDT", "USDC":
		return SkyADT{Tag: 0, SkyName: code, Fields: []any{}}
	default:
		return SkyADT{Tag: 0, SkyName: "CurrencyRaw", Fields: []any{code}}
	}
}

// Codec_autoEnc : a -> Value. Reflects the record into a JSON object Value.
func Codec_autoEnc(snakeArg, record any) any {
	raw, err := codecAutoEncodeVal(reflect.ValueOf(record), AsBool(snakeArg))
	if err != nil {
		// FAIL LOUD, never silently null. Encoding can't return a `Result` (the
		// Sky signature is `a -> Value`), and returning `null` here silently threw
		// away the ENTIRE record — the pinned persistence default writing null to
		// the DB with zero signal (a Money/Decimal/Dict field did exactly this).
		// A genuinely underivable field is a data-loss bug in well-typed code, so
		// a classified panic (surfaced by the runtime's panic net) is strictly
		// better than silent corruption. After the Dict/Set/Decimal/Money/ADT arms
		// this fires only for a truly un-encodable type.
		panic("Codec.auto: cannot encode this value — " + err.Error())
	}
	return JsonValue{raw: raw}
}

// ── Decode: JSON raw → value ─────────────────────────────────────────────────

// codecStrictInt decodes a JSON number to int64, matching
// Sky.Core.Json.Decode.int: the value must be a float64 (JSON has no
// int/float distinction) with no fractional part. Rejects strings, bools,
// null, and fractional numbers — this is conformance finding C2 (the
// reflective record decoder must reject a wrong-typed field, not coerce it
// to a zero-value default). Match the explicit object/field decoder.
func codecStrictInt(raw any) (int64, error) {
	// Shares the exact-text, platform-deterministic int decode with the
	// hand-written Codec.int (jsonDecodeInt): json.Number preserves the full
	// int64 range losslessly, and an out-of-range/fractional value errors the
	// same way on every platform. Re-prefix with "Codec.auto:" for the
	// reflective-decoder context.
	i, err := jsonDecodeInt(raw)
	if err != nil {
		return 0, fmt.Errorf("Codec.auto: %s", err.Error())
	}
	return i, nil
}

func codecAutoDecodeVal(rt reflect.Type, raw any, snake bool) (reflect.Value, error) {
	switch rt.Kind() {
	case reflect.String:
		s, ok := raw.(string)
		if !ok {
			return reflect.Value{}, fmt.Errorf("Codec.auto: expected String, got %s", jsonValueKind(raw))
		}
		return reflect.ValueOf(s).Convert(rt), nil
	case reflect.Bool:
		b, ok := raw.(bool)
		if !ok {
			return reflect.Value{}, fmt.Errorf("Codec.auto: expected Bool, got %s", jsonValueKind(raw))
		}
		return reflect.ValueOf(b).Convert(rt), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := codecStrictInt(raw)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(n).Convert(rt), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := codecStrictInt(raw)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(uint64(n)).Convert(rt), nil
	case reflect.Float32, reflect.Float64:
		f, ok := jsonDecodeFloat(raw)
		if !ok {
			return reflect.Value{}, fmt.Errorf("Codec.auto: expected Float, got %s", jsonValueKind(raw))
		}
		return reflect.ValueOf(f).Convert(rt), nil
	case reflect.Slice:
		items, ok := raw.([]any)
		if !ok {
			return reflect.Value{}, fmt.Errorf("Codec.auto: expected Array, got %s", jsonValueKind(raw))
		}
		out := reflect.MakeSlice(rt, len(items), len(items))
		for i, it := range items {
			ev, err := codecAutoDecodeVal(rt.Elem(), it, snake)
			if err != nil {
				return reflect.Value{}, err
			}
			out.Index(i).Set(ev)
		}
		return out, nil
	case reflect.Map:
		// Dict k v ← JSON object. The witness carries the concrete element type
		// (`map[string]int`), so values decode without a declaredType. Keys are
		// JSON strings decoded to the key type.
		m, ok := raw.(map[string]any)
		if !ok {
			return reflect.Value{}, fmt.Errorf("Codec.auto: expected object for Dict, got %s", jsonValueKind(raw))
		}
		out := reflect.MakeMapWithSize(rt, len(m))
		for k, v := range m {
			vv, err := codecAutoDecodeVal(rt.Elem(), v, snake)
			if err != nil {
				return reflect.Value{}, err
			}
			kv, err := codecAutoDecodeVal(rt.Key(), k, snake)
			if err != nil {
				return reflect.Value{}, err
			}
			out.SetMapIndex(kv, vv)
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
			inner, err := codecAutoDecodeVal(out.FieldByName("JustValue").Type(), raw, snake)
			if err != nil {
				return reflect.Value{}, err
			}
			out.FieldByName("JustValue").Set(inner)
			return out, nil
		}
		if isSkyAdtType(rt) {
			return reflect.Value{}, fmt.Errorf("Codec.auto: cannot derive data-carrying ADT %q", rt.Name())
		}
		return codecAutoDecodeStruct(rt, raw, snake)
	default:
		return reflect.Value{}, fmt.Errorf("Codec.auto: cannot decode kind %s", rt.Kind())
	}
}

// codecFieldOptional reports whether a record field may be omitted from the
// JSON object (or be null) without a decode error — true only for Maybe-typed
// fields, which decode an absent/null value to Nothing. Every other field is
// required, matching the explicit object/field/buildObject decoder. C2.
func codecFieldOptional(f reflect.StructField) bool {
	return isSkyMaybeType(f.Type)
}

func codecAutoDecodeStruct(rt reflect.Type, raw any, snake bool) (reflect.Value, error) {
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
		key := skyColKey(f, snake)
		val, present := m[key]
		// A required (non-Maybe) field absent from the object is a decode
		// error — no more silent zero-value fill (C2). A Maybe field may be
		// omitted: val is nil, which codecAutoDecodeTyped decodes to Nothing.
		if !present && !codecFieldOptional(f) {
			return reflect.Value{}, fmt.Errorf("Codec.auto: missing required field %q", key)
		}
		fv, err := codecAutoDecodeTyped(f.Type, skyTagType(f), val, snake)
		if err != nil {
			return reflect.Value{}, err
		}
		out.Field(i).Set(fv)
	}
	return out, nil
}

// codecAutoDecodeTyped decodes using the declared Sky type (from the field tag):
// a registered enum decodes its name back to the ordinal; Maybe[T]/[]T unwrap.
func codecAutoDecodeTyped(gt reflect.Type, declaredType string, raw any, snake bool) (reflect.Value, error) {
	if declaredType != "" {
		if isRegisteredEnum(declaredType) {
			switch gt.Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				name := codecRawStr(raw)
				if ord, ok := enumOrdinalForName(declaredType, name); ok {
					return reflect.ValueOf(int64(ord)).Convert(gt), nil
				}
				// Unknown enum value: error rather than silently defaulting to
				// ordinal 0 (the first variant) — that was silent corruption (C2).
				return reflect.Value{}, fmt.Errorf("Codec.auto: unknown %s value %q", declaredType, name)
			}
		}
		if inner, ok := strings.CutPrefix(declaredType, "rt.SkyMaybe["); ok && isSkyMaybeType(gt) {
			out := reflect.New(gt).Elem()
			if raw == nil {
				out.FieldByName("Tag").SetInt(1)
				return out, nil
			}
			out.FieldByName("Tag").SetInt(0)
			iv, err := codecAutoDecodeTyped(out.FieldByName("JustValue").Type(), strings.TrimSuffix(inner, "]"), raw, snake)
			if err != nil {
				return reflect.Value{}, err
			}
			out.FieldByName("JustValue").Set(iv)
			return out, nil
		}
		if elem, ok := strings.CutPrefix(declaredType, "[]"); ok && gt.Kind() == reflect.Slice {
			items, isList := raw.([]any)
			if !isList {
				return reflect.Value{}, fmt.Errorf("Codec.auto: expected Array, got %s", jsonValueKind(raw))
			}
			out := reflect.MakeSlice(gt, len(items), len(items))
			for i, it := range items {
				ev, err := codecAutoDecodeTyped(gt.Elem(), elem, it, snake)
				if err != nil {
					return reflect.Value{}, err
				}
				out.Index(i).Set(ev)
			}
			return out, nil
		}
		// Dict k v ← JSON object. The declared type is `map[<key>]<value>`; the
		// FIRST `]` closes the key, the rest is the value type. Threading BOTH
		// declared types down is what lets a Dict VALUE that is itself a
		// data-carrying ADT — `Dict String Money`, `Dict String Decimal`, a Dict
		// of a user ADT — decode: without this arm the Dict fell to the reflect
		// path (codecAutoDecodeVal), which sees every such value as the erased
		// `rt.SkyADT` alias and cannot tell Money from Decimal from a user ADT,
		// so it errored "cannot derive data-carrying ADT". List/Maybe already
		// threaded their element type; Dict was the gap.
		if inner, ok := strings.CutPrefix(declaredType, "map["); ok && gt.Kind() == reflect.Map {
			close := strings.IndexByte(inner, ']')
			if close < 0 {
				return reflect.Value{}, fmt.Errorf("Codec.auto: malformed Dict type %q", declaredType)
			}
			keyType := inner[:close]
			valType := inner[close+1:]
			m, isObj := raw.(map[string]any)
			if !isObj {
				return reflect.Value{}, fmt.Errorf("Codec.auto: expected object for Dict, got %s", jsonValueKind(raw))
			}
			out := reflect.MakeMapWithSize(gt, len(m))
			for k, v := range m {
				kv, err := codecAutoDecodeTyped(gt.Key(), keyType, k, snake)
				if err != nil {
					return reflect.Value{}, err
				}
				vv, err := codecAutoDecodeTyped(gt.Elem(), valType, v, snake)
				if err != nil {
					return reflect.Value{}, err
				}
				out.SetMapIndex(kv, vv)
			}
			return out, nil
		}
		// Std.Decimal — a JSON string; rebuild via decimalBox (round-trips the
		// canonical `Decimal.String()` the encoder emitted).
		if declaredType == "Std_Decimal_Decimal" {
			s, ok := raw.(string)
			if !ok {
				return reflect.Value{}, fmt.Errorf("Codec.auto: expected Decimal string, got %s", jsonValueKind(raw))
			}
			d, err := decimal.NewFromString(s)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("Codec.auto: Decimal parse %q: %v", s, err)
			}
			return reflect.ValueOf(decimalBox(d)).Convert(gt), nil
		}
		// Std.Money — `{"amount":"12.99","currency":"USD"}`; reuse the DB decoder's
		// currency reconstruction (nullary code → its variant; unknown → CurrencyRaw).
		if declaredType == "Std_Money_Money" {
			m, ok := raw.(map[string]any)
			if !ok {
				return reflect.Value{}, fmt.Errorf("Codec.auto: expected Money object, got %s", jsonValueKind(raw))
			}
			amtS, _ := m["amount"].(string)
			curS, _ := m["currency"].(string)
			amt, err := decimal.NewFromString(amtS)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("Codec.auto: Money amount parse %q: %v", amtS, err)
			}
			money := SkyADT{Tag: 0, SkyName: "Money", Fields: []any{decimalBox(amt), sqlCodeToCurrency(curS)}}
			return reflect.ValueOf(money).Convert(gt), nil
		}
		// A user payload ADT — the tagged `{"tag":<name>,"v0":…}` the encoder
		// emits — reconstructed via the same wire factory the runtime trusts.
		// GUARD: only when the TARGET type is genuinely an ADT (a SkyADT-backed
		// struct or a sealed-variant interface). A plain RECORD may legitimately
		// carry a field NAMED `tag` (e.g. `Inner = { tag : String, n : Int }`),
		// and it must decode field-by-field, never be mistaken for an ADT wire
		// and fed to BuildAdtFromWire — the CodecConformanceTest `Inner = {tag,n}`
		// nested-record round-trip exists to pin exactly this (it was the silent
		// string→ADT confusion the test's docstring warns about).
		if isSkyAdtType(gt) || gt.Kind() == reflect.Interface {
			if obj, ok := raw.(map[string]any); ok {
				if tag, hasTag := obj["tag"].(string); hasTag {
					var rawArgs []json.RawMessage
					for i := 0; ; i++ {
						v, present := obj[fmt.Sprintf("v%d", i)]
						if !present {
							break
						}
						b, mErr := json.Marshal(v)
						if mErr != nil {
							return reflect.Value{}, fmt.Errorf("Codec.auto: ADT arg marshal: %v", mErr)
						}
						rawArgs = append(rawArgs, b)
					}
					// The variant registry is keyed by the PACKAGE-QUALIFIED name
					// (`main.Main_Role`), which `reflect.Type.String()` yields — the
					// bare `declaredType` (`Main_Role`) is deliberately NOT a key
					// (adt_variant_factory.go:103). Fall back to the bare name for a
					// legacy SkyADT-tag registration.
					adtName := gt.String()
					val, built := BuildAdtFromWire(adtName, tag, rawArgs, -1)
					if !built {
						val, built = BuildAdtFromWire(declaredType, tag, rawArgs, -1)
					}
					if built {
						// The field's Go type may be a SEALED INTERFACE (sealed-variant
						// ADT — `Convert` can't target an interface) or a concrete
						// `SkyADT`-backed type. Assign directly when the built variant
						// implements the field type; else convert.
						rvVal := reflect.ValueOf(val)
						out := reflect.New(gt).Elem()
						switch {
						case rvVal.Type().AssignableTo(gt):
							out.Set(rvVal)
						case rvVal.Type().ConvertibleTo(gt):
							out.Set(rvVal.Convert(gt))
						default:
							return reflect.Value{}, fmt.Errorf("Codec.auto: rebuilt %s value is not assignable to the field type %s", declaredType, gt)
						}
						return out, nil
					}
					return reflect.Value{}, fmt.Errorf("Codec.auto: cannot rebuild ADT %s variant %q", declaredType, tag)
				}
			}
		}
	}
	return codecAutoDecodeVal(gt, raw, snake)
}

// Codec_autoDecoder : a -> Decoder a. A JSON decoder that reflection-builds the
// witness's type.
func Codec_autoDecoder(snakeArg, witness any) any {
	wt := reflect.TypeOf(witness)
	return JsonDecoder{run: func(raw any) any {
		v, err := codecAutoDecodeVal(wt, raw, AsBool(snakeArg))
		if err != nil {
			return Err[any, any](ErrDecode(err.Error()))
		}
		return Ok[any, any](v.Interface())
	}}
}

// Codec_autoCols : a -> List (String, String). The (name, kind) columns for the
// DB shape, derived from the record's fields. Returns an empty list if the
// witness is not a record.
func Codec_autoCols(snakeArg, witness any) any {
	snake := AsBool(snakeArg)
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
		kind := codecColKindTyped(f.Type, skyTagType(f))
		if isSkyMaybeType(f.Type) { // Maybe field → nullable column (marked with `?`)
			kind += "?"
		}
		out = append(out, T2[any, any]{V0: skyColKey(f, snake), V1: kind})
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
	case json.Number:
		if n, err := strconv.ParseInt(v.String(), 10, 64); err == nil {
			return n
		}
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
	case json.Number:
		if f, err := strconv.ParseFloat(v.String(), 64); err == nil {
			return f
		}
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
