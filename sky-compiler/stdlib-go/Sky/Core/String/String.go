package sky_sky_core_string

import (
	sky_wrappers "sky-out/sky_wrappers"
	"fmt"
)

func FromInt(n any) any {
	return fmt.Sprintf("%d", n)
}

func ToBytes(s any) any {
	return []byte(sky_wrappers.Sky_AsString(s))
}

func FromBytes(b any) any {
	return sky_wrappers.Sky_string_FromBytes(b)
}

func Split(sep any, s any) any {
	return sky_wrappers.Sky_string_Split(sep, s)
}

func Join(sep any, list any) any {
	return sky_wrappers.Sky_string_Join(sep, list)
}

func Contains(sub any, s any) any {
	return sky_wrappers.Sky_string_Contains(sub, s)
}

func Replace(old any, new_ any, s any) any {
	return sky_wrappers.Sky_string_Replace(old, new_, s)
}

func Trim(s any) any {
	return sky_wrappers.Sky_string_Trim(s)
}

func Length(s any) any {
	return sky_wrappers.Sky_string_Length(s)
}

func ToLower(s any) any {
	return sky_wrappers.Sky_string_ToLower(s)
}

func ToUpper(s any) any {
	return sky_wrappers.Sky_string_ToUpper(s)
}

func StartsWith(prefix any, s any) any {
	return sky_wrappers.Sky_string_StartsWith(prefix, s)
}

func EndsWith(suffix any, s any) any {
	return sky_wrappers.Sky_string_EndsWith(suffix, s)
}

func Slice(start any, end any, s any) any {
	return sky_wrappers.Sky_string_Slice(start, end, s)
}

func IsEmpty(s any) any {
	return sky_wrappers.Sky_string_IsEmpty(s)
}

func FromFloat(f any) any {
	return sky_wrappers.Sky_string_FromFloat(f)
}

func ToInt(s any) any {
	return sky_wrappers.Sky_string_ToInt(s)
}

func ToFloat(s any) any {
	return sky_wrappers.Sky_string_ToFloat(s)
}

func Lines(s any) any {
	return sky_wrappers.Sky_string_Lines(s)
}

func Words(s any) any {
	return sky_wrappers.Sky_string_Words(s)
}

func Repeat(n any, s any) any {
	return sky_wrappers.Sky_string_Repeat(n, s)
}

func PadLeft(n any, ch any, s any) any {
	return sky_wrappers.Sky_string_PadLeft(n, ch, s)
}

func PadRight(n any, ch any, s any) any {
	return sky_wrappers.Sky_string_PadRight(n, ch, s)
}

func Left(n any, s any) any {
	return sky_wrappers.Sky_string_Left(n, s)
}

func Right(n any, s any) any {
	return sky_wrappers.Sky_string_Right(n, s)
}

func Reverse(s any) any {
	return sky_wrappers.Sky_string_Reverse(s)
}

func Indexes(sub any, s any) any {
	return sky_wrappers.Sky_string_Indexes(sub, s)
}

func Concat(strs any) any {
	return Join("", strs)
}

func FromChar(c any) any {
	return c
}

