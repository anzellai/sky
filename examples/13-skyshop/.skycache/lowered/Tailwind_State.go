func Tailwind_State_Hover(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("hover:", className)}
}

func Tailwind_State_Focus(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("focus:", className)}
}

func Tailwind_State_Active(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("active:", className)}
}

func Tailwind_State_Disabled(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("disabled:", className)}
}

func Tailwind_State_FirstChild(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("first:", className)}
}

func Tailwind_State_LastChild(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("last:", className)}
}

func Tailwind_State_Odd(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("odd:", className)}
}

func Tailwind_State_Even(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("even:", className)}
}

func Tailwind_State_Group() any {
	return cls("group")
}

func Tailwind_State_GroupHover(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("group-hover:", className)}
}

func Tailwind_State_GroupFocus(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("group-focus:", className)}
}

func Tailwind_State_FocusVisible(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("focus-visible:", className)}
}

func Tailwind_State_FocusWithin(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("focus-within:", className)}
}

func Tailwind_State_Placeholder(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("placeholder:", className)}
}

func Tailwind_State_Checked(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("checked:", className)}
}

func Tailwind_State_Required(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("required:", className)}
}

func Tailwind_State_Invalid(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("invalid:", className)}
}