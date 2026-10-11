package rt

// error_domain.go — the runtime side of `Sky.Core.Error.Domain DomainFailure`.
//
// `ErrorDetails` has one open-ended variant, `Domain DomainFailure`
// (sky-stdlib/Sky/Core/Error.sky). A stdlib domain ("ai", later "db",
// "durable", …) records a stable code, a retryable flag, an optional retry
// delay and string attributes in it, and the core type never changes when a
// domain adds a failure. Go code builds the value with ErrDomain and reads it
// back with DomainOf. Both follow the constructor order in Error.sky:
//
//	ErrorDetails:  0=FfiPanic 1=TypeMismatch 2=HttpStatus 3=JsonDecode
//	               4=Custom   5=Domain
//	DomainFailure: DomainFailure domain code retryable retryAfterMs attrs
//
// A test (error_domain_test.go) parses Error.sky and fails when either order
// moves, so the tags below cannot drift from the Sky source.

import (
	"reflect"
	"strconv"
)

// ErrorKind tags, in errorKindAdt's order (Error.sky's constructor order).
const (
	errKindIo = iota
	errKindNetwork
	errKindFfi
	errKindDecode
	errKindTimeout
	errKindNotFound
	errKindPermissionDenied
	errKindInvalidInput
	errKindConflict
	errKindUnavailable
	errKindUnexpected
)

const (
	errorDetailsDomainTag = 5
	errorDetailsDomainCtr = "Domain"
	domainFailureCtr      = "DomainFailure"
)

// DomainFailure is the Go view of a `Sky.Core.Error.DomainFailure`.
// RetryAfterMs < 0 means "no delay named" (Sky `Nothing`).
type DomainFailure struct {
	Domain       string
	Code         string
	Retryable    bool
	RetryAfterMs int
	Attrs        [][2]string
}

// Attr returns the value of one attribute.
func (f DomainFailure) Attr(key string) (string, bool) {
	for _, kv := range f.Attrs {
		if kv[0] == key {
			return kv[1], true
		}
	}
	return "", false
}

// With adds or replaces an attribute, keeping the first position of a key.
func (f DomainFailure) With(key, value string) DomainFailure {
	out := make([][2]string, 0, len(f.Attrs)+1)
	seen := false
	for _, kv := range f.Attrs {
		if kv[0] == key {
			if !seen {
				out = append(out, [2]string{key, value})
				seen = true
			}
			continue
		}
		out = append(out, kv)
	}
	if !seen {
		out = append(out, [2]string{key, value})
	}
	f.Attrs = out
	return f
}

// skyValue builds the Sky `DomainFailure` value.
func (f DomainFailure) skyValue() any {
	var after any = Nothing[any]()
	if f.RetryAfterMs >= 0 {
		after = Just[any](f.RetryAfterMs)
	}
	attrs := make([]any, 0, len(f.Attrs))
	for _, kv := range f.Attrs {
		attrs = append(attrs, SkyTuple2{V0: kv[0], V1: kv[1]})
	}
	return SkyADT{
		Tag:     0,
		SkyName: domainFailureCtr,
		Fields:  []any{f.Domain, f.Code, f.Retryable, after, attrs},
	}
}

// ErrDomain builds `Error kind { message = msg, details = Just (Domain f) }`.
// kindTag follows errorKindAdt's order (0=Io … 10=Unexpected).
func ErrDomain(kindTag int, msg string, f DomainFailure) any {
	name := "Error"
	if kindTag >= 0 && kindTag < len(errorKindCtors) {
		name = errorKindCtors[kindTag]
	}
	details := Just[any](SkyADT{
		Tag:     errorDetailsDomainTag,
		SkyName: errorDetailsDomainCtr,
		Fields:  []any{f.skyValue()},
	})
	return skyErrorAdt{
		Tag:     0,
		SkyName: "Error",
		Fields:  []any{errorKindAdt(kindTag, name), skyErrorInfo{Message: msg, Details: details}},
	}
}

// DomainOf reads the domain failure a Sky `Error` carries. It accepts the
// runtime's own representation and the typed one codegen emits (a Maybe of
// any element type, a list of typed tuples), through reflection.
func DomainOf(v any) (DomainFailure, bool) {
	ev := reflect.ValueOf(derefPointer(unwrapAny(v)))
	if !ev.IsValid() || ev.Kind() != reflect.Struct {
		return DomainFailure{}, false
	}
	if n := ev.FieldByName("SkyName"); !n.IsValid() || n.Kind() != reflect.String || n.String() != "Error" {
		return DomainFailure{}, false
	}
	fields := ev.FieldByName("Fields")
	if !fields.IsValid() || fields.Kind() != reflect.Slice || fields.Len() != 2 {
		return DomainFailure{}, false
	}
	info := reflectElem(fields.Index(1))
	if !info.IsValid() || info.Kind() != reflect.Struct {
		return DomainFailure{}, false
	}
	details := info.FieldByName("Details")
	if !details.IsValid() {
		return DomainFailure{}, false
	}
	inner, ok := reflectMaybe(details)
	if !ok {
		return DomainFailure{}, false
	}
	adt := reflectElem(inner)
	if !adt.IsValid() || adt.Kind() != reflect.Struct {
		return DomainFailure{}, false
	}
	if n := adt.FieldByName("SkyName"); !n.IsValid() || n.String() != errorDetailsDomainCtr {
		return DomainFailure{}, false
	}
	af := adt.FieldByName("Fields")
	if !af.IsValid() || af.Kind() != reflect.Slice || af.Len() != 1 {
		return DomainFailure{}, false
	}
	return domainFailureFromSky(af.Index(0))
}

func domainFailureFromSky(v reflect.Value) (DomainFailure, bool) {
	df := reflectElem(v)
	if !df.IsValid() || df.Kind() != reflect.Struct {
		return DomainFailure{}, false
	}
	fs := df.FieldByName("Fields")
	if !fs.IsValid() || fs.Kind() != reflect.Slice || fs.Len() != 5 {
		return DomainFailure{}, false
	}
	out := DomainFailure{RetryAfterMs: -1}
	dom := reflectElem(fs.Index(0))
	code := reflectElem(fs.Index(1))
	retry := reflectElem(fs.Index(2))
	if dom.Kind() != reflect.String || code.Kind() != reflect.String || retry.Kind() != reflect.Bool {
		return DomainFailure{}, false
	}
	out.Domain, out.Code, out.Retryable = dom.String(), code.String(), retry.Bool()
	if after, ok := reflectMaybe(fs.Index(3)); ok {
		a := reflectElem(after)
		if a.IsValid() && a.CanInt() {
			out.RetryAfterMs = int(a.Int())
		}
	}
	attrs := reflectElem(fs.Index(4))
	if attrs.IsValid() && attrs.Kind() == reflect.Slice {
		for i := 0; i < attrs.Len(); i++ {
			t := reflectElem(attrs.Index(i))
			if !t.IsValid() || t.Kind() != reflect.Struct {
				continue
			}
			k, v := reflectElem(t.FieldByName("V0")), reflectElem(t.FieldByName("V1"))
			if k.Kind() == reflect.String && v.Kind() == reflect.String {
				out.Attrs = append(out.Attrs, [2]string{k.String(), v.String()})
			}
		}
	}
	return out, true
}

// reflectElem unwraps interfaces and pointers.
func reflectElem(v reflect.Value) reflect.Value {
	for v.IsValid() && (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}

// reflectMaybe reads a SkyMaybe of any element type: (value, true) for Just.
func reflectMaybe(v reflect.Value) (reflect.Value, bool) {
	m := reflectElem(v)
	if !m.IsValid() || m.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	tag := m.FieldByName("Tag")
	val := m.FieldByName("JustValue")
	if !tag.IsValid() || !tag.CanInt() || !val.IsValid() {
		return reflect.Value{}, false
	}
	if tag.Int() != 0 {
		return reflect.Value{}, false
	}
	return val, true
}

// domainLogFields is the structured-log view of a domain failure: the
// `error.domain`, `error.code`, `error.retryable`, `error.retryAfterMs` and
// `error.attr.<key>` fields. Attributes never hold a secret by contract.
func domainLogFields(prefix string, f DomainFailure) map[string]any {
	m := map[string]any{
		prefix + ".domain":    f.Domain,
		prefix + ".code":      f.Code,
		prefix + ".retryable": strconv.FormatBool(f.Retryable),
	}
	if f.RetryAfterMs >= 0 {
		m[prefix+".retryAfterMs"] = strconv.Itoa(f.RetryAfterMs)
	}
	for _, kv := range f.Attrs {
		m[prefix+".attr."+kv[0]] = kv[1]
	}
	return m
}
