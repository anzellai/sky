package sky_sky_core_list

import (
	sky_wrappers "sky-out/sky_wrappers"
)

func Map(fn any, list any) any {
	return sky_wrappers.Sky_list_Map(fn, list)
}

func Filter(fn any, list any) any {
	return sky_wrappers.Sky_list_Filter(fn, list)
}

func Foldl(fn any, acc any, list any) any {
	return sky_wrappers.Sky_list_Foldl(fn, acc, list)
}

func Foldr(fn any, acc any, list any) any {
	return sky_wrappers.Sky_list_Foldr(fn, acc, list)
}

func Head(list any) any {
	return sky_wrappers.Sky_list_Head(list)
}

func Tail(list any) any {
	return sky_wrappers.Sky_list_Tail(list)
}

func Length(list any) any {
	return sky_wrappers.Sky_list_Length(list)
}

func Append(a any, b any) any {
	return sky_wrappers.Sky_list_Append(a, b)
}

func Reverse(list any) any {
	return sky_wrappers.Sky_list_Reverse(list)
}

func Member(item any, list any) any {
	return sky_wrappers.Sky_list_Member(item, list)
}

func Range(start any, end any) any {
	return sky_wrappers.Sky_list_Range(start, end)
}

func IsEmpty(list any) any {
	return sky_wrappers.Sky_list_IsEmpty(list)
}

func Take(n any, list any) any {
	return sky_wrappers.Sky_list_Take(n, list)
}

func Drop(n any, list any) any {
	return sky_wrappers.Sky_list_Drop(n, list)
}

func Sort(list any) any {
	return sky_wrappers.Sky_list_Sort(list)
}

func Intersperse(sep any, list any) any {
	return sky_wrappers.Sky_list_Intersperse(sep, list)
}

func Concat(lists any) any {
	return sky_wrappers.Sky_list_Concat(lists)
}

func ConcatMap(fn any, list any) any {
	return sky_wrappers.Sky_list_ConcatMap(fn, list)
}

func IndexedMap(fn any, list any) any {
	return sky_wrappers.Sky_list_IndexedMap(fn, list)
}

func Singleton(x any) any {
	return []any{x}
}

func All(pred any, list any) any {
	return func() any {
	__list9434 := sky_wrappers.Sky_AsList(list)
	if len(__list9434) == 0 {
		return true
	} else {
		if len(__list9434) > 0 {
			x := __list9434[0]
			xs := __list9434[1:]
			return func() any {
	if sky_wrappers.Sky_AsBool(sky_wrappers.Sky_AsFunc(pred)(x)) {
		return All(pred, xs)
	} else {
		return false
	}
}()
		} else {
			panic("non-exhaustive pattern match in list expression")
		}
	}
}()
}

func Any(pred any, list any) any {
	return func() any {
	__list9298 := sky_wrappers.Sky_AsList(list)
	if len(__list9298) == 0 {
		return false
	} else {
		if len(__list9298) > 0 {
			x := __list9298[0]
			xs := __list9298[1:]
			return func() any {
	if sky_wrappers.Sky_AsBool(sky_wrappers.Sky_AsFunc(pred)(x)) {
		return true
	} else {
		return Any(pred, xs)
	}
}()
		} else {
			panic("non-exhaustive pattern match in list expression")
		}
	}
}()
}

func Sum(list any) any {
	return Foldl(func(arg0 any) any {
	x := arg0
	return func(arg0 any) any {
	acc := arg0
	return sky_wrappers.Sky_AsInt(x) + sky_wrappers.Sky_AsInt(acc)
}
}, 0, list)
}

func Product(list any) any {
	return Foldl(func(arg0 any) any {
	x := arg0
	return func(arg0 any) any {
	acc := arg0
	return sky_wrappers.Sky_AsInt(x) * sky_wrappers.Sky_AsInt(acc)
}
}, 1, list)
}

func Maximum(list any) any {
	return sky_wrappers.Sky_list_Maximum(list)
}

func Minimum(list any) any {
	return sky_wrappers.Sky_list_Minimum(list)
}

func Partition(pred any, list any) any {
	return sky_wrappers.Tuple2{Filter(pred, list), Filter(func(arg0 any) any {
	x := arg0
	return func(arg0 any) any { if sky_wrappers.Sky_AsBool(arg0) { return false }; return true }(sky_wrappers.Sky_AsFunc(pred)(x))
}, list)}
}

func Find(pred any, list any) any {
	return sky_wrappers.Sky_list_Find(pred, list)
}

func FilterMap(f any, list any) any {
	return sky_wrappers.Sky_list_FilterMap(f, list)
}

func SortBy(toKey any, list any) any {
	return func() any {
	__list57 := sky_wrappers.Sky_AsList(list)
	if len(__list57) == 0 {
		return []any{}
	} else {
		if len(__list57) > 0 {
			pivot := __list57[0]
			rest := __list57[1:]
			return func() any {
	pivotKey := sky_wrappers.Sky_AsFunc(toKey)(pivot)
	lesser := Filter(func(arg0 any) any {
	x := arg0
	return sky_wrappers.Sky_AsInt(sky_wrappers.Sky_AsFunc(toKey)(x)) < sky_wrappers.Sky_AsInt(pivotKey)
}, rest)
	greater := Filter(func(arg0 any) any {
	x := arg0
	return sky_wrappers.Sky_AsInt(sky_wrappers.Sky_AsFunc(toKey)(x)) >= sky_wrappers.Sky_AsInt(pivotKey)
}, rest)
	return append(sky_wrappers.Sky_AsList(SortBy(toKey, lesser)), sky_wrappers.Sky_AsList(append(sky_wrappers.Sky_AsList([]any{pivot}), sky_wrappers.Sky_AsList(SortBy(toKey, greater))...))...)
}()
		} else {
			panic("non-exhaustive pattern match in list expression")
		}
	}
}()
}

func Zip(listA any, listB any) any {
	return sky_wrappers.Sky_list_Zip(listA, listB)
}

func Unzip(list any) any {
	return sky_wrappers.Sky_list_Unzip(list)
}

func Map2(f any, listA any, listB any) any {
	return sky_wrappers.Sky_list_Map2(f, listA, listB)
}

