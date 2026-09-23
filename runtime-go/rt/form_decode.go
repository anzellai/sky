// Strict decode of a submitted form into the onSubmit handler's record.
//
// Every client delivers a form submit the same way: a map from control name
// to the control's text value, with an unchecked checkbox / radio left out
// (Sky.Live `__skyExtractArgs`, Sky.Spa `spaFormData`, the terminal form).
// The handler takes a record (`Ui.onSubmit SignIn`, `SignIn : Creds -> Msg`),
// and the typed codegen narrows the handler's argument to that record with
// rt.Coerce. The generic map→record narrowing zero-fills: an Int field from
// "42" became 0, a checked box became False, a missing field became "". A
// form submit therefore travels as FormFields, and every narrowing of a
// FormFields value into a record goes through decodeFormRecord, which never
// zero-fills.
//
// The rules (shared by every target, so a form behaves the same on the web,
// in the wasm client and in the terminal):
//
//	String       the text. Missing → error.
//	Int / Float  the text parsed (surrounding spaces ignored). Missing, empty
//	             or not a number → error.
//	Bool         "true" / "on" / "checked" / "1" / "yes" → True (a checked
//	             checkbox sends "on" unless it has its own value);
//	             missing / "" / "false" / "off" / "0" / "no" → False (an
//	             unchecked checkbox is left out of the submit). Anything else
//	             → error.
//	Maybe X      missing or "" → Nothing, else Just (X decoded as above).
//	other        error: a form control cannot produce it (the compiler
//	             rejects such a handler with [E2010]; this is the backstop).
//
// A failure is a *FormDecodeError. Where the record type is visible (a typed
// constructor parameter) the dispatcher gets it as an error value; where it is
// not (the `func(any) any` wrapper whose body calls rt.Coerce) it is raised as
// a panic that the event dispatcher recovers. Either way the event is DROPPED
// and logged, and the model is not touched.

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

// FormFields is a submitted form: control name → value (a string). Its own
// named type marks the value as a form payload, so the record narrowing can
// decode it strictly instead of zero-filling.
type FormFields map[string]any

var formFieldsType = reflect.TypeOf(FormFields(nil))

// FormDecodeError names the field that could not be decoded and why.
type FormDecodeError struct {
	Record string // the Go record type
	Field  string // the Sky field name
	Reason string
}

func (e *FormDecodeError) Error() string {
	return fmt.Sprintf("rt.FormDecode: form submit into %s: field %q %s", e.Record, e.Field, e.Reason)
}

// NewFormFields copies a decoded form map into a FormFields value.
func NewFormFields(m map[string]any) FormFields {
	out := make(FormFields, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// formFieldName is the Sky field name of a record field: the `sky` tag's name
// when present, else the Go name with its first letter lower-cased.
func formFieldName(sf reflect.StructField) string {
	if tag, ok := sf.Tag.Lookup("sky"); ok {
		if name := strings.SplitN(tag, ",", 2)[0]; name != "" {
			return name
		}
	}
	r, size := utf8.DecodeRuneInString(sf.Name)
	if r == utf8.RuneError {
		return sf.Name
	}
	return string(unicode.ToLower(r)) + sf.Name[size:]
}

// lookup finds a field by exact name, then case-insensitively (in sorted key
// order, so the choice is deterministic when two controls differ only in case).
func (f FormFields) lookup(name string) (string, bool, error) {
	v, ok := f[name]
	if !ok {
		keys := make([]string, 0, len(f))
		for k := range f {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if strings.EqualFold(k, name) {
				v, ok = f[k], true
				break
			}
		}
	}
	if !ok {
		return "", false, nil
	}
	switch x := v.(type) {
	case string:
		return x, true, nil
	case bool:
		return strconv.FormatBool(x), true, nil
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true, nil
	case nil:
		return "", false, nil
	default:
		return "", true, fmt.Errorf("has a %T value, not text", v)
	}
}

// decodeFormScalar decodes one present text value into a String / Int /
// Float / Bool slot.
func decodeFormScalar(raw string, t reflect.Type) (reflect.Value, string) {
	out := reflect.New(t).Elem()
	switch t.Kind() {
	case reflect.String:
		out.SetString(raw)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return out, fmt.Sprintf("is %q, which is not an Int", raw)
		}
		out.SetInt(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return out, fmt.Sprintf("is %q, which is not a Float", raw)
		}
		out.SetFloat(f)
	case reflect.Bool:
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "true", "on", "checked", "1", "yes":
			out.SetBool(true)
		case "", "false", "off", "0", "no":
			out.SetBool(false)
		default:
			return out, fmt.Sprintf("is %q, which is not a Bool", raw)
		}
	default:
		return out, fmt.Sprintf("has type %s, which a form control cannot fill (use String, Int, Float, Bool or a Maybe of those)", t)
	}
	return out, ""
}

// decodeFormRecord builds a `target` record from a submitted form, strictly.
func decodeFormRecord(fields FormFields, target reflect.Type) (reflect.Value, error) {
	rec := reflect.New(target).Elem()
	for i := 0; i < target.NumField(); i++ {
		sf := target.Field(i)
		if !sf.IsExported() {
			continue
		}
		name := formFieldName(sf)
		fail := func(reason string) (reflect.Value, error) {
			return reflect.Value{}, &FormDecodeError{Record: target.String(), Field: name, Reason: reason}
		}
		raw, present, err := fields.lookup(name)
		if err != nil {
			return fail(err.Error())
		}
		ft := sf.Type
		switch {
		case isSkyMaybeType(ft):
			slot := reflect.New(ft).Elem()
			if !present || raw == "" {
				slot.FieldByName("Tag").SetInt(1)
			} else {
				jt := slot.FieldByName("JustValue").Type()
				v, reason := decodeFormScalar(raw, jt)
				if reason != "" {
					return fail(reason)
				}
				slot.FieldByName("Tag").SetInt(0)
				slot.FieldByName("JustValue").Set(v)
			}
			rec.Field(i).Set(slot)
		case ft.Kind() == reflect.Bool:
			if !present {
				rec.Field(i).SetBool(false)
				continue
			}
			v, reason := decodeFormScalar(raw, ft)
			if reason != "" {
				return fail(reason)
			}
			rec.Field(i).Set(v)
		default:
			if !present {
				switch ft.Kind() {
				case reflect.String, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32,
					reflect.Int64, reflect.Float32, reflect.Float64:
					return fail(fmt.Sprintf("is missing: the form has no control named %q", name))
				}
			}
			v, reason := decodeFormScalar(raw, ft)
			if reason != "" {
				return fail(reason)
			}
			rec.Field(i).Set(v)
		}
	}
	return rec, nil
}

// mustDecodeFormRecord is decodeFormRecord for the narrowing paths that have no
// error return (rt.Coerce, narrowMapToStruct). The panic carries the
// *FormDecodeError; the event dispatchers recover it and drop the event.
func mustDecodeFormRecord(fields FormFields, target reflect.Type) reflect.Value {
	v, err := decodeFormRecord(fields, target)
	if err != nil {
		panic(err)
	}
	return v
}

// asFormDecodeError reports whether a recovered panic value is a form decode
// failure.
func asFormDecodeError(r any) (*FormDecodeError, bool) {
	e, ok := r.(*FormDecodeError)
	return e, ok
}
