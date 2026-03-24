package sky_sky_core_maybe

import (
	sky_wrappers "sky-out/sky_wrappers"
)

func WithDefault(fallback any, value any) any {
	return sky_wrappers.Sky_maybe_WithDefault(fallback, value)
}

func Map(fn any, maybe any) any {
	return sky_wrappers.Sky_maybe_Map(fn, maybe)
}

func AndThen(fn any, maybe any) any {
	return sky_wrappers.Sky_maybe_AndThen(fn, maybe)
}

func Map2(f any, ma any, mb any) any {
	return func() any {
	var __match_50782 any
	__match_50782 = ma
	switch __match_50782.(sky_wrappers.SkyMaybe).Tag {
	case 1:
		return sky_wrappers.SkyNothing()
	case 0:
		a := __match_50782.(sky_wrappers.SkyMaybe).JustValue
		return func() any {
	var __match_74139 any
	__match_74139 = mb
	switch __match_74139.(sky_wrappers.SkyMaybe).Tag {
	case 1:
		return sky_wrappers.SkyNothing()
	case 0:
		b := __match_74139.(sky_wrappers.SkyMaybe).JustValue
		return sky_wrappers.SkyJust(sky_wrappers.Sky_AsFunc(sky_wrappers.Sky_AsFunc(f)(a))(b))
	}
	panic("non-exhaustive pattern match")
	return nil
}()
	}
	panic("non-exhaustive pattern match")
	return nil
}()
}

func Map3(f any, ma any, mb any, mc any) any {
	return func() any {
	var __match_61728 any
	__match_61728 = ma
	switch __match_61728.(sky_wrappers.SkyMaybe).Tag {
	case 1:
		return sky_wrappers.SkyNothing()
	case 0:
		a := __match_61728.(sky_wrappers.SkyMaybe).JustValue
		return func() any {
	var __match_84682 any
	__match_84682 = mb
	switch __match_84682.(sky_wrappers.SkyMaybe).Tag {
	case 1:
		return sky_wrappers.SkyNothing()
	case 0:
		b := __match_84682.(sky_wrappers.SkyMaybe).JustValue
		return func() any {
	var __match_22388 any
	__match_22388 = mc
	switch __match_22388.(sky_wrappers.SkyMaybe).Tag {
	case 1:
		return sky_wrappers.SkyNothing()
	case 0:
		c := __match_22388.(sky_wrappers.SkyMaybe).JustValue
		return sky_wrappers.SkyJust(sky_wrappers.Sky_AsFunc(sky_wrappers.Sky_AsFunc(sky_wrappers.Sky_AsFunc(f)(a))(b))(c))
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

