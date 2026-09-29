package rt

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// sky_show.go — render a runtime value in SKY syntax.
//
// `Debug.toString` (and so `Basics.toString` and multiline-string
// interpolation) used to render a value with Go's `%v`, so a test failure read
// `expected {0 a <nil>} but got {0 b <nil>}` and a record read `{Ada 40 [a
// b]}`. The Sky printer renders what the user wrote:
//
//	Ok "a"                        Err (Io "x")
//	{ age = 40, name = "Ada" }    [Circle 1.5, Empty]
//	Just (1, 'c')                 Dict.fromList [(1, "a")]
//
// It works from the runtime shapes alone: `SkyResult` / `SkyMaybe`, the `T2`…
// tuples, typed ADT variants (`SkyVariantName`), the erased `SkyADT`
// (`SkyName` + `Fields`), records (Go structs; field names from the `sky:`
// tag, else the Go name with its first letter lowered), `Dict` (a string-keyed
// map, keys decoded and ordered as `Dict.toList` orders them) and `Set`.
//
// A union whose constructors all take no arguments lowers to a named Go int
// (`type Main_Color int`) whose generated `SkyEnumName` returns the
// constructor name, so it prints `Red` wherever it sits. A plain `int` that
// reached a record field typed as such an enum (runtime-built values) is named
// from the field's `sky:` tag and the enum registry.
//
// A value that is not a Sky shape but implements `fmt.Stringer` (a `Secret`,
// a crypto key, a `Decimal`) prints through `String()`, so a secret stays
// redacted here as it does in every other print path.

// skyShowDepth bounds the walk: a Go value reached through `any` can be
// cyclic (an FFI handle), and a printer must not hang.
const skyShowDepth = 64

// SkyShow renders v in Sky syntax. A top-level string is quoted.
func SkyShow(v any) string {
	s, _ := skyShow(reflect.ValueOf(v), 0, "")
	return s
}

// errorKindCtors are Sky.Core.Error.ErrorKind's constructor names, indexed by
// tag (errorKindLabels holds the human labels `IO`, `FFI`, …).
var errorKindCtors = []string{
	"Io", "Network", "Ffi", "Decode", "Timeout", "NotFound",
	"PermissionDenied", "InvalidInput", "Conflict", "Unavailable",
	"Unexpected",
}

// skyShow returns the rendering and whether it is ATOMIC — safe as a
// constructor argument without parentheses. `enumType` is the Sky type name a
// record field tag gave an int, or "".
func skyShow(rv reflect.Value, depth int, enumType string) (string, bool) {
	if depth > skyShowDepth {
		return "…", true
	}
	for rv.IsValid() && (rv.Kind() == reflect.Interface || rv.Kind() == reflect.Pointer) {
		if rv.IsNil() {
			return "()", true
		}
		// A Go handle with its own rendering on the pointer (a Std.Sync Ref).
		if rv.Kind() == reflect.Pointer && rv.CanInterface() {
			if s, ok := rv.Interface().(fmt.Stringer); ok {
				return s.String(), true
			}
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return "()", true
	}
	t := rv.Type()

	// Typed ADT variants carry their constructor name.
	if rv.CanInterface() {
		if nv, ok := rv.Interface().(interface{ SkyVariantName() string }); ok && rv.Kind() == reflect.Struct {
			args := make([]reflect.Value, 0, rv.NumField())
			for i := 0; i < rv.NumField(); i++ {
				args = append(args, rv.Field(i))
			}
			return skyShowCtor(nv.SkyVariantName(), args, depth)
		}
	}

	switch rv.Kind() {
	case reflect.String:
		return skyQuote(rv.String()), true
	case reflect.Bool:
		if rv.Bool() {
			return "True", true
		}
		return "False", true
	case reflect.Int32:
		// Sky `Char` is a Go `rune`.
		return skyQuoteChar(rune(rv.Int())), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int64:
		// A union whose constructors all take no arguments is a named Go
		// int with a generated `SkyEnumName`.
		if rv.CanInterface() {
			if en, ok := rv.Interface().(interface{ SkyEnumName() string }); ok {
				if name := en.SkyEnumName(); name != "" {
					return name, true
				}
			}
		}
		n := rv.Int()
		if enumType != "" {
			if name, ok := enumNameForOrdinal(enumType, int(n)); ok {
				return name, true
			}
		}
		s := strconv.FormatInt(n, 10)
		return s, n >= 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(rv.Uint(), 10), true
	case reflect.Float32, reflect.Float64:
		f := rv.Float()
		s := strconv.FormatFloat(f, 'g', -1, 64)
		return s, f >= 0
	case reflect.Func:
		return "<function>", true
	case reflect.Chan:
		return "<channel>", true
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return "[]", true
		}
		parts := make([]string, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			parts[i], _ = skyShow(rv.Index(i), depth+1, strings.TrimPrefix(enumType, "[]"))
		}
		return "[" + strings.Join(parts, ", ") + "]", true
	case reflect.Map:
		if t.Key().Kind() == reflect.String && rv.CanInterface() {
			ents := dictEntries(rv.Interface(), dictKeyString)
			parts := make([]string, len(ents))
			for i, e := range ents {
				k, _ := skyShow(reflect.ValueOf(e.key), depth+1, "")
				v, _ := skyShow(reflect.ValueOf(e.val), depth+1, "")
				parts[i] = "(" + k + ", " + v + ")"
			}
			return "Dict.fromList [" + strings.Join(parts, ", ") + "]", false
		}
		return fmt.Sprintf("%v", rv.Interface()), true
	case reflect.Struct:
		return skyShowStruct(rv, depth, t, enumType)
	}
	if rv.CanInterface() {
		return fmt.Sprintf("%v", rv.Interface()), true
	}
	return "<internal>", true
}

func skyShowStruct(rv reflect.Value, depth int, t reflect.Type, enumType string) (string, bool) {
	name := t.Name()
	switch {
	case t.NumField() == 0:
		return "()", true
	case strings.HasPrefix(name, "SkyResult["):
		if rv.FieldByName("Tag").Int() == 0 {
			return skyShowCtor("Ok", []reflect.Value{rv.FieldByName("OkValue")}, depth)
		}
		return skyShowCtor("Err", []reflect.Value{rv.FieldByName("ErrValue")}, depth)
	case strings.HasPrefix(name, "SkyMaybe["):
		if rv.FieldByName("Tag").Int() == 0 {
			// A `Maybe Color` field's tag is `rt.SkyMaybe[Main_Color]`.
			inner := strings.TrimSuffix(strings.TrimPrefix(enumType, "rt.SkyMaybe["), "]")
			s, atomic := skyShow(rv.FieldByName("JustValue"), depth+1, inner)
			if !atomic {
				s = "(" + s + ")"
			}
			return "Just " + s, false
		}
		return "Nothing", true
	case isTupleTypeName(name):
		parts := make([]string, t.NumField())
		for i := range parts {
			parts[i], _ = skyShow(rv.Field(i), depth+1, "")
		}
		return "(" + strings.Join(parts, ", ") + ")", true
	case name == "SkyTupleN":
		vs := rv.FieldByName("Vs")
		parts := make([]string, vs.Len())
		for i := range parts {
			parts[i], _ = skyShow(vs.Index(i), depth+1, "")
		}
		return "(" + strings.Join(parts, ", ") + ")", true
	case name == "SkySet" && rv.CanInterface():
		items := Set_toList(rv.Interface()).([]any)
		parts := make([]string, len(items))
		for i, it := range items {
			parts[i], _ = skyShow(reflect.ValueOf(it), depth+1, "")
		}
		return "Set.fromList [" + strings.Join(parts, ", ") + "]", false
	case name == "SkyADT":
		ctor := rv.FieldByName("SkyName").String()
		fields := rv.FieldByName("Fields")
		args := make([]reflect.Value, 0, fields.Len())
		for i := 0; i < fields.Len(); i++ {
			args = append(args, fields.Index(i))
		}
		if ctor == "Error" && len(args) == 2 {
			if s, ok := skyShowError(args, depth); ok {
				return s, false
			}
		}
		// An opaque stdlib box (`Decimal__Internal d`) shows its payload.
		if strings.HasSuffix(ctor, "__Internal") && len(args) == 1 {
			return skyShow(args[0], depth+1, "")
		}
		return skyShowCtor(ctor, args, depth)
	}
	// A Go value with its own rendering (a Secret stays redacted).
	if rv.CanInterface() {
		if s, ok := rv.Interface().(fmt.Stringer); ok {
			return s.String(), true
		}
	}
	// A record: every field exported.
	type field struct{ name, text string }
	fields := make([]field, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		if !sf.IsExported() {
			if rv.CanInterface() {
				return fmt.Sprintf("%v", rv.Interface()), true
			}
			return "<internal>", true
		}
		fname, ftype := skyFieldName(sf)
		text, _ := skyShow(rv.Field(i), depth+1, ftype)
		fields = append(fields, field{fname, text})
	}
	sort.SliceStable(fields, func(i, j int) bool { return fields[i].name < fields[j].name })
	parts := make([]string, len(fields))
	for i, f := range fields {
		parts[i] = f.name + " = " + f.text
	}
	return "{ " + strings.Join(parts, ", ") + " }", true
}

// skyFieldName is a record field's Sky name and, from its `sky:"name,Type"`
// tag, the Sky type name (used to name an enum's constructor).
func skyFieldName(sf reflect.StructField) (string, string) {
	if tag, ok := sf.Tag.Lookup("sky"); ok {
		name, typ, _ := strings.Cut(tag, ",")
		if name != "" && name != "-" {
			return name, typ
		}
	}
	r, size := utf8.DecodeRuneInString(sf.Name)
	return string(unicode.ToLower(r)) + sf.Name[size:], ""
}

func isTupleTypeName(name string) bool {
	if len(name) < 3 || name[0] != 'T' || name[1] < '2' || name[1] > '9' || name[2] != '[' {
		return false
	}
	return true
}

func skyShowCtor(ctor string, args []reflect.Value, depth int) (string, bool) {
	if len(args) == 0 {
		return ctor, true
	}
	var b strings.Builder
	b.WriteString(ctor)
	for _, a := range args {
		s, atomic := skyShow(a, depth+1, "")
		b.WriteByte(' ')
		if atomic {
			b.WriteString(s)
		} else {
			b.WriteString("(" + s + ")")
		}
	}
	return b.String(), false
}

// skyShowError renders a Sky.Core.Error as its kind constructor and message
// (`Io "x"`), with its details when present (`Decode "bad" (JsonDecode "…")`).
func skyShowError(args []reflect.Value, depth int) (string, bool) {
	kv := args[0]
	for kv.Kind() == reflect.Interface && !kv.IsNil() {
		kv = kv.Elem()
	}
	if !kv.IsValid() || !kv.CanInt() {
		return "", false
	}
	kind := "Error"
	if k := int(kv.Int()); k >= 0 && k < len(errorKindCtors) {
		kind = errorKindCtors[k]
	}
	info := args[1]
	for info.Kind() == reflect.Interface && !info.IsNil() {
		info = info.Elem()
	}
	if !info.IsValid() || info.Kind() != reflect.Struct {
		return "", false
	}
	msg := info.FieldByName("Message")
	if !msg.IsValid() || msg.Kind() != reflect.String {
		return "", false
	}
	out := kind + " " + skyQuote(msg.String())
	if d := info.FieldByName("Details"); d.IsValid() {
		ds, _ := skyShow(d, depth+1, "")
		if strings.HasPrefix(ds, "Just ") {
			inner := strings.TrimPrefix(ds, "Just ")
			out += " " + inner
		}
	}
	return out, true
}

func skyQuote(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u{%04X}`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func skyQuoteChar(r rune) string {
	switch r {
	case '\'':
		return `'\''`
	case '\\':
		return `'\\'`
	case '\n':
		return `'\n'`
	case '\r':
		return `'\r'`
	case '\t':
		return `'\t'`
	}
	if r < 0x20 || r == 0x7f {
		return fmt.Sprintf(`'\u{%04X}'`, r)
	}
	return "'" + string(r) + "'"
}
