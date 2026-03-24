package sky_sky_core_prelude

import (
	sky_wrappers "sky-out/sky_wrappers"
)

func Identity(x any) any {
	return x
}

func Not(b any) any {
	return func() any {
	if sky_wrappers.Sky_AsBool(b) {
		return false
	} else {
		return true
	}
}()
}

func ErrorToString(e any) any {
	return sky_wrappers.Sky_errorToString(e)
}

func Always(a any, _ any) any {
	return a
}

func Fst(pair any) any {
	return func() any {
	__tuple7213 := pair.(sky_wrappers.Tuple2)
	a := __tuple7213.V0
	return a
}()
}

func Snd(pair any) any {
	return func() any {
	__tuple9981 := pair.(sky_wrappers.Tuple2)
	b := __tuple9981.V1
	return b
}()
}

func Clamp(low any, high any, val any) any {
	return func() any {
	if sky_wrappers.Sky_AsInt(val) < sky_wrappers.Sky_AsInt(low) {
		return low
	} else {
		return func() any {
	if sky_wrappers.Sky_AsInt(val) > sky_wrappers.Sky_AsInt(high) {
		return high
	} else {
		return val
	}
}()
	}
}()
}

func ModBy(divisor any, n any) any {
	return sky_wrappers.Sky_AsInt(n) % sky_wrappers.Sky_AsInt(divisor)
}

func Js(s any) any {
	return sky_wrappers.Sky_JS(s)
}

