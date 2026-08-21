package rt

import (
	"reflect"
)

// isErrResult: True when v is a SkyResult with Tag == 1 (Err).
func isErrResult(v any) bool {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Struct {
		return false
	}
	tag := rv.FieldByName("Tag")
	if !tag.IsValid() || tag.Kind() != reflect.Int {
		return false
	}
	return tag.Int() == 1
}

// extractErrResultValue: read the Err side's payload (usually String).
func extractErrResultValue(v any) any {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Struct {
		return ""
	}
	// Sky's SkyResult carries OkValue/ErrValue fields.
	fv := rv.FieldByName("ErrValue")
	if !fv.IsValid() {
		return ""
	}
	return fv.Interface()
}
