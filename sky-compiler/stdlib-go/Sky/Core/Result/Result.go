package sky_sky_core_result

import (
	sky_wrappers "sky-out/sky_wrappers"
)

func WithDefault(fallback any, result any) any {
	return sky_wrappers.Sky_result_WithDefault(fallback, result)
}

func Map(fn any, result any) any {
	return sky_wrappers.Sky_result_Map(fn, result)
}

func AndThen(fn any, result any) any {
	return sky_wrappers.Sky_result_AndThen(fn, result)
}

func MapError(fn any, result any) any {
	return sky_wrappers.Sky_result_MapError(fn, result)
}

func ToMaybe(result any) any {
	return sky_wrappers.Sky_result_ToMaybe(result)
}

func Map2(f any, ra any, rb any) any {
	return func() any {
	var __match_78589 any
	__match_78589 = ra
	switch __match_78589.(sky_wrappers.SkyResult).Tag {
	case 1:
		e := __match_78589.(sky_wrappers.SkyResult).ErrValue
		return sky_wrappers.SkyErr(e)
	case 0:
		a := __match_78589.(sky_wrappers.SkyResult).OkValue
		return func() any {
	var __match_42342 any
	__match_42342 = rb
	switch __match_42342.(sky_wrappers.SkyResult).Tag {
	case 1:
		e := __match_42342.(sky_wrappers.SkyResult).ErrValue
		return sky_wrappers.SkyErr(e)
	case 0:
		b := __match_42342.(sky_wrappers.SkyResult).OkValue
		return sky_wrappers.SkyOk(sky_wrappers.Sky_AsFunc(sky_wrappers.Sky_AsFunc(f)(a))(b))
	}
	panic("non-exhaustive pattern match")
	return nil
}()
	}
	panic("non-exhaustive pattern match")
	return nil
}()
}

func Map3(f any, ra any, rb any, rc any) any {
	return func() any {
	var __match_17425 any
	__match_17425 = ra
	switch __match_17425.(sky_wrappers.SkyResult).Tag {
	case 1:
		e := __match_17425.(sky_wrappers.SkyResult).ErrValue
		return sky_wrappers.SkyErr(e)
	case 0:
		a := __match_17425.(sky_wrappers.SkyResult).OkValue
		return func() any {
	var __match_20141 any
	__match_20141 = rb
	switch __match_20141.(sky_wrappers.SkyResult).Tag {
	case 1:
		e := __match_20141.(sky_wrappers.SkyResult).ErrValue
		return sky_wrappers.SkyErr(e)
	case 0:
		b := __match_20141.(sky_wrappers.SkyResult).OkValue
		return func() any {
	var __match_73891 any
	__match_73891 = rc
	switch __match_73891.(sky_wrappers.SkyResult).Tag {
	case 1:
		e := __match_73891.(sky_wrappers.SkyResult).ErrValue
		return sky_wrappers.SkyErr(e)
	case 0:
		c := __match_73891.(sky_wrappers.SkyResult).OkValue
		return sky_wrappers.SkyOk(sky_wrappers.Sky_AsFunc(sky_wrappers.Sky_AsFunc(sky_wrappers.Sky_AsFunc(f)(a))(b))(c))
	}
	panic("non-exhaustive pattern match")
	return nil
}()
	}
	panic("non-exhaustive pattern match")
	return nil
}()
	}
	panic("non-exhaustive pattern match")
	return nil
}()
}

func FromMaybe(err any, maybe any) any {
	return func() any {
	var __match_15042 any
	__match_15042 = maybe
	switch __match_15042.(sky_wrappers.SkyMaybe).Tag {
	case 0:
		v := __match_15042.(sky_wrappers.SkyMaybe).JustValue
		return sky_wrappers.SkyOk(v)
	case 1:
		return sky_wrappers.SkyErr(err)
	}
	panic("non-exhaustive pattern match")
	return nil
}()
}

