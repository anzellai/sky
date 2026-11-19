package rt

// The routed page write (v0.29.14). Sky.Live (applyRouteWithParams), the
// Sky.Spa server render (Spa_ssrResolveModel) and the Sky.Spa client
// (spaApplyURL) write the routed page, or the NotFound page, into the model's
// `page` field. The checker refuses a page whose type is not the field's type
// ([E2001] at `App.withRoutes`, `Live.config`, `Spa.withRoutes` and
// `Spa.withNotFound`). This is defence in depth for a value that reaches the
// runtime anyway: before, RecordUpdate panicked on every request
// (`reflect.Set: value of type string is not assignable to type
// main.Main_Route`), and a page of another enum type was converted silently.
// Now the mismatch is reported once, in Sky words, and the page is not
// written. A correct program takes the same RecordUpdate path as before.

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
)

// routedPageFailed is set by the first reported mismatch, so the report is
// made once per process.
var routedPageFailed atomic.Bool

// routedPageFatal reports a routed page mismatch and stops the process. A
// test replaces it.
var routedPageFatal = func(msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(1)
}

// writeRoutedPage returns model with its `page` field set to page, or model
// unchanged (after the one report) when the field cannot hold page.
func writeRoutedPage(model, page any) any {
	if msg := routedPageMismatch(model, page); msg != "" {
		if routedPageFailed.CompareAndSwap(false, true) {
			routedPageFatal(msg)
		}
		return model
	}
	return RecordUpdate(model, map[string]any{"Page": page})
}

// routedPageMismatch is "" when the model's `page` field can hold page (or
// the model has no such field), else the Sky-worded error.
func routedPageMismatch(model, page any) string {
	v := reflect.ValueOf(unwrapAny(model))
	if v.Kind() == reflect.Ptr && !v.IsNil() {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return ""
	}
	f, ok := v.Type().FieldByName("Page")
	if !ok {
		return ""
	}
	nv := reflect.ValueOf(page)
	if !nv.IsValid() || routedPageFits(nv, f.Type) {
		return ""
	}
	return fmt.Sprintf(
		"[E2001] The routes give a page of type `%s`, but the model's `page` field has type `%s`.\n\n"+
			"The app writes the routed page (and the NotFound page) into the model's `page` field "+
			"on every request, so the two types must be the same. Make every route page and "+
			"the NotFound page a `%s`.",
		skyTypeName(nv.Type()), skyTypeName(f.Type), skyTypeName(f.Type))
}

// routedPageFits mirrors RecordUpdate's assignment rules, except that a value
// of one Sky enum type never converts into another (`Screen` into `Page`).
func routedPageFits(nv reflect.Value, target reflect.Type) bool {
	src := nv.Type()
	if src.AssignableTo(target) {
		return true
	}
	if src.Kind() == reflect.Int && target.Kind() == reflect.Int &&
		src.Name() != "int" && target.Name() != "int" && src.Name() != "" && target.Name() != "" {
		// Two distinct defined int types: two Sky enums (`type Page = Home |
		// About`). RecordUpdate would convert one into the other.
		return false
	}
	if src.ConvertibleTo(target) && nominalContainerConversion(src, target) {
		return true
	}
	narrowed := narrowReflectValue(nv, target)
	return narrowed.IsValid() && narrowed.Type().AssignableTo(target)
}

// skyTypeName names a Go type the way the Sky source names it: `Main_Route`
// is `Route`, `string` is `String`.
func skyTypeName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		if t.Name() == "string" {
			return "String"
		}
	case reflect.Int:
		if t.Name() == "int" {
			return "Int"
		}
	case reflect.Float64:
		if t.Name() == "float64" {
			return "Float"
		}
	case reflect.Bool:
		if t.Name() == "bool" {
			return "Bool"
		}
	}
	name := t.Name()
	if name == "" {
		return t.String()
	}
	name = strings.TrimSuffix(name, "_R")
	if i := strings.LastIndex(name, "_"); i >= 0 && i+1 < len(name) {
		name = name[i+1:]
	}
	return name
}
