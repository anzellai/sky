//go:build !js

// Form submit for the terminal Element view (Ui.form + Ui.onSubmit).

package rt

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// tuiDecodeFormSubmit builds the onSubmit Msg from the submitted fields.
//
// The handler is a plain Msg (returned as is) or a constructor taking the
// form record. Each record field is filled from the control with the same
// name (the Sky field name, case-insensitive) and converted to the field's
// type (String / Int / Float / Bool). A missing field (other than a Bool,
// which an unchecked checkbox leaves out) or a value that does not parse is
// an error — never a zero-filled record.
//
// The typed codegen usually wraps the constructor as `func(any) any` that
// narrows its argument to the record (`rt.Coerce[Rec](arg)`), so the
// parameter type does not name the record. Handing it a map of strings
// would let that narrowing zero-fill an Int field from "42". The record
// type is therefore read from the Msg the handler builds for an empty
// probe, and the fields are decoded strictly into that type first.
func tuiDecodeFormSubmit(handler any, fields map[string]string) (any, error) {
	if handler == nil {
		return nil, nil
	}
	if !isFunc(handler) {
		return handler, nil
	}
	rv := reflect.ValueOf(handler)
	if rv.Kind() != reflect.Func || rv.Type().NumIn() == 0 {
		return sky_call(handler, nil), nil
	}
	pt := rv.Type().In(0)
	switch pt.Kind() {
	case reflect.Struct:
		rec, err := tuiBuildFormRecord(pt, fields)
		if err != nil {
			return nil, err
		}
		return rv.Call([]reflect.Value{rec})[0].Interface(), nil
	case reflect.Map:
		if pt.Key().Kind() != reflect.String || pt.Elem().Kind() != reflect.String {
			return nil, fmt.Errorf("the onSubmit handler takes %s; a form submits String fields", pt)
		}
		m := reflect.MakeMapWithSize(pt, len(fields))
		for k, v := range fields {
			m.SetMapIndex(reflect.ValueOf(k).Convert(pt.Key()), reflect.ValueOf(v).Convert(pt.Elem()))
		}
		return rv.Call([]reflect.Value{m})[0].Interface(), nil
	case reflect.Interface:
		if recT, ok := tuiProbeFormRecordType(handler); ok {
			rec, err := tuiBuildFormRecord(recT, fields)
			if err != nil {
				return nil, err
			}
			return sky_call(handler, rec.Interface()), nil
		}
		// No record behind the handler (a Dict String String / untyped
		// handler): hand it the field map, the shape the web clients send.
		m := make(map[string]any, len(fields))
		for k, v := range fields {
			m[k] = v
		}
		return sky_call(handler, m), nil
	}
	return nil, fmt.Errorf("the onSubmit handler takes %s; a form submits a record of String / Int / Float / Bool fields", pt)
}

// tuiProbeFormRecordType calls the handler with an empty probe and reads the
// record type out of the Msg it builds: the first struct-typed payload field
// (V0, V1, …) whose fields carry Sky record tags.
func tuiProbeFormRecordType(handler any) (t reflect.Type, ok bool) {
	defer func() {
		if recover() != nil {
			t, ok = nil, false
		}
	}()
	msg := sky_call(handler, map[string]any{})
	mv := reflect.ValueOf(msg)
	if !mv.IsValid() || mv.Kind() != reflect.Struct {
		return nil, false
	}
	for i := 0; i < mv.NumField(); i++ {
		ft := mv.Type().Field(i).Type
		if ft.Kind() == reflect.Struct && tuiIsSkyRecord(ft) {
			return ft, true
		}
	}
	return nil, false
}

func tuiIsSkyRecord(t reflect.Type) bool {
	for i := 0; i < t.NumField(); i++ {
		if _, ok := t.Field(i).Tag.Lookup("sky"); ok {
			return true
		}
	}
	return false
}

// tuiFormFieldName is the Sky field name of a record field: the `sky` tag's
// name when present, else the Go name with a lower-case first letter.
func tuiFormFieldName(sf reflect.StructField) string {
	if tag, ok := sf.Tag.Lookup("sky"); ok {
		if name := strings.SplitN(tag, ",", 2)[0]; name != "" {
			return name
		}
	}
	return strings.ToLower(sf.Name[:1]) + sf.Name[1:]
}

func tuiBuildFormRecord(pt reflect.Type, fields map[string]string) (reflect.Value, error) {
	lookup := func(name string) (string, bool) {
		if v, ok := fields[name]; ok {
			return v, true
		}
		for k, v := range fields {
			if strings.EqualFold(k, name) {
				return v, true
			}
		}
		return "", false
	}
	rec := reflect.New(pt).Elem()
	for i := 0; i < pt.NumField(); i++ {
		sf := pt.Field(i)
		if !sf.IsExported() {
			continue
		}
		name := tuiFormFieldName(sf)
		raw, present := lookup(name)
		fv := rec.Field(i)
		switch sf.Type.Kind() {
		case reflect.String:
			if !present {
				return rec, fmt.Errorf("field %q is missing (give its input Ui.name %q)", name, name)
			}
			fv.SetString(raw)
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			if !present {
				return rec, fmt.Errorf("field %q is missing (give its input Ui.name %q)", name, name)
			}
			n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
			if err != nil {
				return rec, fmt.Errorf("field %q: %q is not an Int", name, raw)
			}
			fv.SetInt(n)
		case reflect.Float32, reflect.Float64:
			if !present {
				return rec, fmt.Errorf("field %q is missing (give its input Ui.name %q)", name, name)
			}
			f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
			if err != nil {
				return rec, fmt.Errorf("field %q: %q is not a Float", name, raw)
			}
			fv.SetFloat(f)
		case reflect.Bool:
			switch strings.ToLower(strings.TrimSpace(raw)) {
			case "true", "on", "checked", "1", "yes":
				fv.SetBool(true)
			case "", "false", "off", "0", "no":
				fv.SetBool(false)
			default:
				return rec, fmt.Errorf("field %q: %q is not a Bool", name, raw)
			}
		default:
			return rec, fmt.Errorf("field %q has type %s, which a form cannot fill (use String, Int, Float or Bool)", name, sf.Type)
		}
	}
	return rec, nil
}
