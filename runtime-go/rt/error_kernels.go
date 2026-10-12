package rt

// error_kernels.go — typed kernels behind three `Sky.Core.Error` readers:
// `isRetryable`, `domain` and `retryAfter` (the `Ffi.kernel` bindings in
// sky-stdlib/Sky/Core/Error.sky, which stays their signature and doc source).
//
// `Sky.Core.Error` is a stdlib ADT, so it is the erased `rt.SkyADT` and every
// payload read in Sky source is a runtime narrowing (doc 14 §4.5, origin R6).
// Written in Sky, `isRetryable` read the `Error` payload, the `ErrorDetails`
// payload and the `DomainFailure` payload, one narrowing each. Here the whole
// read is one reflective walk (DomainOf), and the lowerer calls the `T` twins,
// whose Go results are already `bool` / `SkyMaybe[…]`, so the call site
// narrows nothing (lower.rs `kernel_typed_return_twin`, doc 14 §5.3).
//
// Every function is total: an `Error` of any shape (runtime-built, typed
// codegen, a malformed domain failure, nil) gives an answer, never a panic.
// The erased forms (`Error_isRetryable`, …) back a reference to the binding
// used as a value (`List.filter Error.isRetryable errs`).

import "reflect"

// Error_isRetryableT: a domain failure's own retryable flag decides;
// otherwise `Timeout`, `Network` and `Unavailable` are transient.
func Error_isRetryableT(err any) bool {
	if f, ok := DomainOf(err); ok {
		return f.Retryable
	}
	switch tag, _ := errorKindTagOf(err); tag {
	case errKindTimeout, errKindNetwork, errKindUnavailable:
		return true
	}
	return false
}

// Error_domainT: the domain of the failure an error carries.
func Error_domainT(err any) SkyMaybe[string] {
	if f, ok := DomainOf(err); ok {
		return Just[string](f.Domain)
	}
	return Nothing[string]()
}

// Error_retryAfterT: the retry delay the error's domain failure names.
func Error_retryAfterT(err any) SkyMaybe[int] {
	if f, ok := DomainOf(err); ok && f.RetryAfterMs >= 0 {
		return Just[int](f.RetryAfterMs)
	}
	return Nothing[int]()
}

func Error_isRetryable(err any) any { return Error_isRetryableT(err) }
func Error_domain(err any) any      { return Error_domainT(err) }
func Error_retryAfter(err any) any  { return Error_retryAfterT(err) }

// errorKindTagOf reads the `ErrorKind` ordinal of a Sky `Error`, in any of the
// representations EnumTagIs accepts: a named-int enum (typed codegen), the
// plain int the runtime builds, a SkyADT or a sealed variant. -1 when the value
// is not an `Error` or the kind is unreadable.
func errorKindTagOf(v any) (int, bool) {
	ev := reflect.ValueOf(derefPointer(unwrapAny(v)))
	if !ev.IsValid() || ev.Kind() != reflect.Struct {
		return -1, false
	}
	if n := ev.FieldByName("SkyName"); !n.IsValid() || n.Kind() != reflect.String || n.String() != "Error" {
		return -1, false
	}
	fields := ev.FieldByName("Fields")
	if !fields.IsValid() || fields.Kind() != reflect.Slice || fields.Len() < 1 {
		return -1, false
	}
	k := reflectElem(fields.Index(0))
	if !k.IsValid() {
		return -1, false
	}
	if k.CanInterface() {
		switch x := k.Interface().(type) {
		case SkyVariant:
			return x.SkyVariantTag(), true
		case SkyADT:
			return x.Tag, true
		}
	}
	switch k.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return int(k.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return int(k.Uint()), true
	}
	return -1, false
}
