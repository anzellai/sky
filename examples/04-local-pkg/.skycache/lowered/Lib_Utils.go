// hash:4-2
func Lib_Utils_Add(a any, b any) any {
	return sky_numBinop("+", a, b)
}

func Lib_Utils_FormatMessage(msg any, count any) any {
	return sky_concat(msg, sky_concat(" count is: ", sky_stringFromInt(count)))
}