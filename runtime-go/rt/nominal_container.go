package rt

import "reflect"

// Container identity is nominal. Ordinary Sky records may legally use every
// container field name. Markers alone are insufficient because Go embedding
// promotes methods, so require this runtime package and the direct layout too.
type skyMaybeValue interface{ skyMaybeValue() }
type skyResultValue interface{ skyResultValue() }

var (
	skyMaybeValueType       = reflect.TypeOf((*skyMaybeValue)(nil)).Elem()
	skyResultValueType      = reflect.TypeOf((*skyResultValue)(nil)).Elem()
	skyContainerPackagePath = reflect.TypeOf(SkyMaybe[any]{}).PkgPath()
)

func nominalContainerLayout(t reflect.Type, marker reflect.Type, fields ...string) bool {
	if t == nil || t.Kind() != reflect.Struct || t.PkgPath() != skyContainerPackagePath ||
		t.NumField() != len(fields) || !t.Implements(marker) {
		return false
	}
	for i, name := range fields {
		f := t.Field(i)
		if f.Name != name || f.Anonymous || f.PkgPath != "" {
			return false
		}
	}
	return t.Field(0).Type.Kind() == reflect.Int
}

func isSkyResultType(t reflect.Type) bool {
	return nominalContainerLayout(t, skyResultValueType, "Tag", "OkValue", "ErrValue")
}

func isSkyMaybeType(t reflect.Type) bool {
	return nominalContainerLayout(t, skyMaybeValueType, "Tag", "JustValue")
}

func isSkyAdtType(t reflect.Type) bool { return t == skyADTType }

func isSkyValueContainerType(t reflect.Type) bool {
	return isSkyMaybeType(t) || isSkyResultType(t)
}

// Go permits conversions between structs with the same fields even when they
// have different tags or methods. That cannot turn an ordinary record into a
// Sky container, or silently reinterpret one container family as another.
func nominalContainerConversion(from, to reflect.Type) bool {
	if isSkyMaybeType(from) || isSkyMaybeType(to) {
		return isSkyMaybeType(from) && isSkyMaybeType(to)
	}
	if isSkyResultType(from) || isSkyResultType(to) {
		return isSkyResultType(from) && isSkyResultType(to)
	}
	if isSkyAdtType(from) || isSkyAdtType(to) {
		return isSkyAdtType(from) && isSkyAdtType(to)
	}
	return true
}
