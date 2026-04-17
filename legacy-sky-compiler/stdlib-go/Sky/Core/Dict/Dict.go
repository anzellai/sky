package sky_sky_core_dict

import (
	sky_wrappers "sky-out/sky_wrappers"
)

func Empty() any {
	return sky_wrappers.Sky_dict_Empty()
}

func Singleton(key any, val any) any {
	return sky_wrappers.Sky_dict_Singleton(key, val)
}

func Insert(key any, val any, d any) any {
	return sky_wrappers.Sky_dict_Insert(key, val, d)
}

func Get(key any, d any) any {
	return sky_wrappers.Sky_dict_Get(key, d)
}

func Remove(key any, d any) any {
	return sky_wrappers.Sky_dict_Remove(key, d)
}

func Keys(d any) any {
	return sky_wrappers.Sky_dict_Keys(d)
}

func Values(d any) any {
	return sky_wrappers.Sky_dict_Values(d)
}

func Map(fn any, d any) any {
	return sky_wrappers.Sky_dict_Map(fn, d)
}

func Foldl(fn any, acc any, d any) any {
	return sky_wrappers.Sky_dict_Foldl(fn, acc, d)
}

func FromList(l any) any {
	return sky_wrappers.Sky_dict_FromList(l)
}

func ToList(d any) any {
	return sky_wrappers.Sky_dict_ToList(d)
}

func IsEmpty(d any) any {
	return sky_wrappers.Sky_dict_IsEmpty(d)
}

func Size(d any) any {
	return sky_wrappers.Sky_dict_Size(d)
}

func Member(key any, d any) any {
	return sky_wrappers.Sky_dict_Member(key, d)
}

func Update(key any, fn any, d any) any {
	return sky_wrappers.Sky_dict_Update(key, fn, d)
}

func Filter(pred any, dict any) any {
	addIfMatch := func(arg0 any) any {
	k := arg0
	return func(arg0 any) any {
	v := arg0
	return func(arg0 any) any {
	acc := arg0
	return func() any {
	if sky_wrappers.Sky_AsBool(sky_wrappers.Sky_AsFunc(sky_wrappers.Sky_AsFunc(pred)(k))(v)) {
		return Insert(k, v, acc)
	} else {
		return acc
	}
}()
}
}
}
	return Foldl(addIfMatch, Empty(), dict)
}

func Union(d1 any, d2 any) any {
	return Foldl(func(arg0 any) any {
	k := arg0
	return func(arg0 any) any {
	v := arg0
	return func(arg0 any) any {
	acc := arg0
	return Insert(k, v, acc)
}
}
}, d2, d1)
}

func Intersect(d1 any, d2 any) any {
	return Filter(func(arg0 any) any {
	k := arg0
	return func(arg0 any) any {
	_ = arg0
	return Member(k, d2)
}
}, d1)
}

func Diff(d1 any, d2 any) any {
	return Filter(func(arg0 any) any {
	k := arg0
	return func(arg0 any) any {
	_ = arg0
	return func(arg0 any) any { if sky_wrappers.Sky_AsBool(arg0) { return false }; return true }(Member(k, d2))
}
}, d1)
}

func Partition(pred any, dict any) any {
	return Foldl(func(arg0 any) any {
	k := arg0
	return func(arg0 any) any {
	v := arg0
	return func(arg0 any) any {
	acc := arg0
	return func() any {
	__tuple4123 := acc.(sky_wrappers.Tuple2)
	yes := __tuple4123.V0
	no := __tuple4123.V1
	return func() any {
	if sky_wrappers.Sky_AsBool(sky_wrappers.Sky_AsFunc(sky_wrappers.Sky_AsFunc(pred)(k))(v)) {
		return sky_wrappers.Tuple2{Insert(k, v, yes), no}
	} else {
		return sky_wrappers.Tuple2{yes, Insert(k, v, no)}
	}
}()
}()
}
}
}, sky_wrappers.Tuple2{Empty(), Empty()}, dict)
}

func Foldr(f any, acc any, dict any) any {
	return Foldl(f, acc, dict)
}

