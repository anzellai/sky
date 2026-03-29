func Tailwind_Responsive_Sm(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("sm:", className)}
}

func Tailwind_Responsive_Md(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("md:", className)}
}

func Tailwind_Responsive_Lg(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("lg:", className)}
}

func Tailwind_Responsive_Xl(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("xl:", className)}
}

func Tailwind_Responsive_Xxl(_p any) any {
	className := sky_asTuple2(_p).V1; _ = className; 
	return SkyTuple2{V0: "class", V1: sky_concat("2xl:", className)}
}