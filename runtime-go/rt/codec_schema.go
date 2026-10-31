package rt

// codec_schema.go — the JSON Schema of the JSON a `Codec.auto` codec reads and
// writes, derived from the witness's Go type by the same rules the auto
// encoder follows (codec_auto.go). `Std.Codec.jsonSchema` uses it to tell a
// model the shape of a tool's arguments or of a structured answer.
//
// A part the encoder writes in a form a schema cannot pin down (a
// data-carrying union, a Json.Value, an opaque handle) is the open schema `{}`
// and makes the whole schema inexact: the decoder still checks it.

import (
	"encoding/json"
	"reflect"
	"strings"
)

// codecSchema is one schema as JSON text plus whether it is exact.
type codecSchema struct {
	json  string
	exact bool
}

var codecOpenSchema = codecSchema{json: "{}", exact: false}

func codecSchemaOf(t string) codecSchema {
	return codecSchema{json: `{"type":"` + t + `"}`, exact: true}
}

func codecJSONString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// codecObjectSchema is a closed object: every field required, no other key.
func codecObjectSchema(names []string, fields []codecSchema) codecSchema {
	var b strings.Builder
	exact := true
	b.WriteString(`{"type":"object","properties":{`)
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(codecJSONString(n))
		b.WriteByte(':')
		b.WriteString(fields[i].json)
		exact = exact && fields[i].exact
	}
	b.WriteString(`},"required":[`)
	for i, n := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(codecJSONString(n))
	}
	b.WriteString(`],"additionalProperties":false}`)
	return codecSchema{json: b.String(), exact: exact}
}

// codecSchemaTyped is the schema of a field with its declared Sky type (from
// the `sky:"name,type"` tag): a registered enum is its names.
func codecSchemaTyped(t reflect.Type, declared string, snake bool, depth int) codecSchema {
	if depth > 32 {
		return codecOpenSchema
	}
	if declared != "" {
		if names, ok := enumRegistry[declared]; ok {
			quoted := make([]string, len(names))
			for i, n := range names {
				quoted[i] = codecJSONString(n)
			}
			return codecSchema{json: `{"type":"string","enum":[` + strings.Join(quoted, ",") + `]}`, exact: true}
		}
		if inner, ok := strings.CutPrefix(declared, "rt.SkyMaybe["); ok && isSkyMaybeType(t) {
			if jv, ok := t.FieldByName("JustValue"); ok {
				s := codecSchemaTyped(jv.Type, strings.TrimSuffix(inner, "]"), snake, depth+1)
				return codecSchema{json: `{"anyOf":[` + s.json + `,{"type":"null"}]}`, exact: s.exact}
			}
		}
		if elem, ok := strings.CutPrefix(declared, "[]"); ok && t.Kind() == reflect.Slice {
			s := codecSchemaTyped(t.Elem(), elem, snake, depth+1)
			return codecSchema{json: `{"type":"array","items":` + s.json + `}`, exact: s.exact}
		}
	}
	return codecSchemaType(t, snake, depth)
}

func codecSchemaType(t reflect.Type, snake bool, depth int) codecSchema {
	if depth > 32 {
		return codecOpenSchema
	}
	switch t.Kind() {
	case reflect.String:
		return codecSchemaOf("string")
	case reflect.Bool:
		return codecSchemaOf("boolean")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return codecSchemaOf("integer")
	case reflect.Float32, reflect.Float64:
		return codecSchemaOf("number")
	case reflect.Slice, reflect.Array:
		s := codecSchemaType(t.Elem(), snake, depth+1)
		return codecSchema{json: `{"type":"array","items":` + s.json + `}`, exact: s.exact}
	case reflect.Map:
		s := codecSchemaType(t.Elem(), snake, depth+1)
		return codecSchema{json: `{"type":"object","additionalProperties":` + s.json + `}`, exact: s.exact}
	case reflect.Struct:
		if isSkyDictType(t) {
			vt := reflect.Zero(t).Interface().(skyDictAny).dictValueType()
			s := codecSchemaType(vt, snake, depth+1)
			return codecSchema{json: `{"type":"object","additionalProperties":` + s.json + `}`, exact: s.exact}
		}
		if isSkyMaybeType(t) {
			if jv, ok := t.FieldByName("JustValue"); ok {
				s := codecSchemaType(jv.Type, snake, depth+1)
				return codecSchema{json: `{"anyOf":[` + s.json + `,{"type":"null"}]}`, exact: s.exact}
			}
			return codecOpenSchema
		}
		if t == reflect.TypeOf(SkySet{}) || t == reflect.TypeOf(JsonValue{}) || codecOpaqueStruct(t) {
			return codecOpenSchema
		}
		if name, _, _, ok := unwrapADTShape(reflect.Zero(t).Interface()); ok {
			switch name {
			case "Decimal__Internal":
				return codecSchemaOf("string")
			case "Money":
				return codecObjectSchema([]string{"amount", "currency"}, []codecSchema{codecSchemaOf("string"), codecSchemaOf("string")})
			}
			return codecOpenSchema
		}
		if !isRecordType(t) {
			return codecOpenSchema
		}
		var names []string
		var fields []codecSchema
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue
			}
			names = append(names, skyColKey(f, snake))
			fields = append(fields, codecSchemaTyped(f.Type, skyTagType(f), snake, depth+1))
		}
		return codecObjectSchema(names, fields)
	}
	return codecOpenSchema
}

// Codec_autoSchema : Bool -> a -> String
//
// `{"fields":[{"name":..,"schema":{..},"exact":bool},…]}` for a record
// witness, in field order, or `{"fields":null}` when the witness is not a
// record. Std.Codec builds the object schema from the fields, so
// `Codec.autoWith` can put an override codec's schema in place of a field's.
func Codec_autoSchema(snakeArg, witness any) any {
	snake := AsBool(snakeArg)
	wt := reflect.TypeOf(witness)
	if wt == nil || !isRecordType(wt) {
		return `{"fields":null}`
	}
	if _, _, _, ok := unwrapADTShape(witness); ok {
		return `{"fields":null}`
	}
	var b strings.Builder
	b.WriteString(`{"fields":[`)
	n := 0
	for i := 0; i < wt.NumField(); i++ {
		f := wt.Field(i)
		if f.PkgPath != "" {
			continue
		}
		s := codecSchemaTyped(f.Type, skyTagType(f), snake, 1)
		if n > 0 {
			b.WriteByte(',')
		}
		n++
		b.WriteString(`{"name":` + codecJSONString(skyColKey(f, snake)) + `,"schema":` + s.json + `,"exact":`)
		if s.exact {
			b.WriteString("true}")
		} else {
			b.WriteString("false}")
		}
	}
	b.WriteString(`]}`)
	return b.String()
}
