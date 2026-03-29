func Tailwind_Styles() any {
	return sky_call(sky_htmlStyleNode([]any{}), Tailwind_Internal_Css_AllRules())
}

func Tailwind_Tw(attrs any) any {
	return func() any { classes := sky_call(sky_stringJoin(" "), sky_call(sky_listMap(func(_p any) any { v := sky_asTuple2(_p).V1; _ = v;  return v }), attrs)); _ = classes; return SkyTuple2{V0: "class", V1: classes} }()
}

func Tailwind_TwWith(extra any, attrs any) any {
	return func() any { classes := sky_call(sky_stringJoin(" "), sky_call(sky_listMap(func(_p any) any { v := sky_asTuple2(_p).V1; _ = v;  return v }), attrs)); _ = classes; return SkyTuple2{V0: "class", V1: sky_concat(classes, sky_concat(" ", extra))} }()
}

func Tailwind_P0() any {
	return Tailwind_Spacing_P0()
}

func Tailwind_P1() any {
	return Tailwind_Spacing_P1()
}

func Tailwind_P2() any {
	return Tailwind_Spacing_P2()
}

func Tailwind_P3() any {
	return Tailwind_Spacing_P3()
}

func Tailwind_P4() any {
	return Tailwind_Spacing_P4()
}

func Tailwind_P5() any {
	return Tailwind_Spacing_P5()
}

func Tailwind_P6() any {
	return Tailwind_Spacing_P6()
}

func Tailwind_P7() any {
	return Tailwind_Spacing_P7()
}

func Tailwind_P8() any {
	return Tailwind_Spacing_P8()
}

func Tailwind_P9() any {
	return Tailwind_Spacing_P9()
}

func Tailwind_P10() any {
	return Tailwind_Spacing_P10()
}

func Tailwind_P11() any {
	return Tailwind_Spacing_P11()
}

func Tailwind_P12() any {
	return Tailwind_Spacing_P12()
}

func Tailwind_P14() any {
	return Tailwind_Spacing_P14()
}

func Tailwind_P16() any {
	return Tailwind_Spacing_P16()
}

func Tailwind_P20() any {
	return Tailwind_Spacing_P20()
}

func Tailwind_P24() any {
	return Tailwind_Spacing_P24()
}

func Tailwind_P28() any {
	return Tailwind_Spacing_P28()
}

func Tailwind_P32() any {
	return Tailwind_Spacing_P32()
}

func Tailwind_P36() any {
	return Tailwind_Spacing_P36()
}

func Tailwind_P40() any {
	return Tailwind_Spacing_P40()
}

func Tailwind_P44() any {
	return Tailwind_Spacing_P44()
}

func Tailwind_P48() any {
	return Tailwind_Spacing_P48()
}

func Tailwind_P52() any {
	return Tailwind_Spacing_P52()
}

func Tailwind_P56() any {
	return Tailwind_Spacing_P56()
}

func Tailwind_P60() any {
	return Tailwind_Spacing_P60()
}

func Tailwind_P64() any {
	return Tailwind_Spacing_P64()
}

func Tailwind_P72() any {
	return Tailwind_Spacing_P72()
}

func Tailwind_P80() any {
	return Tailwind_Spacing_P80()
}

func Tailwind_P96() any {
	return Tailwind_Spacing_P96()
}

func Tailwind_Px0() any {
	return Tailwind_Spacing_Px0()
}

func Tailwind_Px1() any {
	return Tailwind_Spacing_Px1()
}

func Tailwind_Px2() any {
	return Tailwind_Spacing_Px2()
}

func Tailwind_Px3() any {
	return Tailwind_Spacing_Px3()
}

func Tailwind_Px4() any {
	return Tailwind_Spacing_Px4()
}

func Tailwind_Px5() any {
	return Tailwind_Spacing_Px5()
}

func Tailwind_Px6() any {
	return Tailwind_Spacing_Px6()
}

func Tailwind_Px7() any {
	return Tailwind_Spacing_Px7()
}

func Tailwind_Px8() any {
	return Tailwind_Spacing_Px8()
}

func Tailwind_Px9() any {
	return Tailwind_Spacing_Px9()
}

func Tailwind_Px10() any {
	return Tailwind_Spacing_Px10()
}

func Tailwind_Px11() any {
	return Tailwind_Spacing_Px11()
}

func Tailwind_Px12() any {
	return Tailwind_Spacing_Px12()
}

func Tailwind_Px14() any {
	return Tailwind_Spacing_Px14()
}

func Tailwind_Px16() any {
	return Tailwind_Spacing_Px16()
}

func Tailwind_Px20() any {
	return Tailwind_Spacing_Px20()
}

func Tailwind_Px24() any {
	return Tailwind_Spacing_Px24()
}

func Tailwind_Px28() any {
	return Tailwind_Spacing_Px28()
}

func Tailwind_Px32() any {
	return Tailwind_Spacing_Px32()
}

func Tailwind_Px36() any {
	return Tailwind_Spacing_Px36()
}

func Tailwind_Px40() any {
	return Tailwind_Spacing_Px40()
}

func Tailwind_Px44() any {
	return Tailwind_Spacing_Px44()
}

func Tailwind_Px48() any {
	return Tailwind_Spacing_Px48()
}

func Tailwind_Px52() any {
	return Tailwind_Spacing_Px52()
}

func Tailwind_Px56() any {
	return Tailwind_Spacing_Px56()
}

func Tailwind_Px60() any {
	return Tailwind_Spacing_Px60()
}

func Tailwind_Px64() any {
	return Tailwind_Spacing_Px64()
}

func Tailwind_Px72() any {
	return Tailwind_Spacing_Px72()
}

func Tailwind_Px80() any {
	return Tailwind_Spacing_Px80()
}

func Tailwind_Px96() any {
	return Tailwind_Spacing_Px96()
}

func Tailwind_Py0() any {
	return Tailwind_Spacing_Py0()
}

func Tailwind_Py1() any {
	return Tailwind_Spacing_Py1()
}

func Tailwind_Py2() any {
	return Tailwind_Spacing_Py2()
}

func Tailwind_Py3() any {
	return Tailwind_Spacing_Py3()
}

func Tailwind_Py4() any {
	return Tailwind_Spacing_Py4()
}

func Tailwind_Py5() any {
	return Tailwind_Spacing_Py5()
}

func Tailwind_Py6() any {
	return Tailwind_Spacing_Py6()
}

func Tailwind_Py7() any {
	return Tailwind_Spacing_Py7()
}

func Tailwind_Py8() any {
	return Tailwind_Spacing_Py8()
}

func Tailwind_Py9() any {
	return Tailwind_Spacing_Py9()
}

func Tailwind_Py10() any {
	return Tailwind_Spacing_Py10()
}

func Tailwind_Py11() any {
	return Tailwind_Spacing_Py11()
}

func Tailwind_Py12() any {
	return Tailwind_Spacing_Py12()
}

func Tailwind_Py14() any {
	return Tailwind_Spacing_Py14()
}

func Tailwind_Py16() any {
	return Tailwind_Spacing_Py16()
}

func Tailwind_Py20() any {
	return Tailwind_Spacing_Py20()
}

func Tailwind_Py24() any {
	return Tailwind_Spacing_Py24()
}

func Tailwind_Py28() any {
	return Tailwind_Spacing_Py28()
}

func Tailwind_Py32() any {
	return Tailwind_Spacing_Py32()
}

func Tailwind_Py36() any {
	return Tailwind_Spacing_Py36()
}

func Tailwind_Py40() any {
	return Tailwind_Spacing_Py40()
}

func Tailwind_Py44() any {
	return Tailwind_Spacing_Py44()
}

func Tailwind_Py48() any {
	return Tailwind_Spacing_Py48()
}

func Tailwind_Py52() any {
	return Tailwind_Spacing_Py52()
}

func Tailwind_Py56() any {
	return Tailwind_Spacing_Py56()
}

func Tailwind_Py60() any {
	return Tailwind_Spacing_Py60()
}

func Tailwind_Py64() any {
	return Tailwind_Spacing_Py64()
}

func Tailwind_Py72() any {
	return Tailwind_Spacing_Py72()
}

func Tailwind_Py80() any {
	return Tailwind_Spacing_Py80()
}

func Tailwind_Py96() any {
	return Tailwind_Spacing_Py96()
}

func Tailwind_Pt0() any {
	return Tailwind_Spacing_Pt0()
}

func Tailwind_Pt1() any {
	return Tailwind_Spacing_Pt1()
}

func Tailwind_Pt2() any {
	return Tailwind_Spacing_Pt2()
}

func Tailwind_Pt3() any {
	return Tailwind_Spacing_Pt3()
}

func Tailwind_Pt4() any {
	return Tailwind_Spacing_Pt4()
}

func Tailwind_Pt5() any {
	return Tailwind_Spacing_Pt5()
}

func Tailwind_Pt6() any {
	return Tailwind_Spacing_Pt6()
}

func Tailwind_Pt8() any {
	return Tailwind_Spacing_Pt8()
}

func Tailwind_Pt10() any {
	return Tailwind_Spacing_Pt10()
}

func Tailwind_Pt12() any {
	return Tailwind_Spacing_Pt12()
}

func Tailwind_Pt16() any {
	return Tailwind_Spacing_Pt16()
}

func Tailwind_Pt20() any {
	return Tailwind_Spacing_Pt20()
}

func Tailwind_Pt24() any {
	return Tailwind_Spacing_Pt24()
}

func Tailwind_Pr0() any {
	return Tailwind_Spacing_Pr0()
}

func Tailwind_Pr1() any {
	return Tailwind_Spacing_Pr1()
}

func Tailwind_Pr2() any {
	return Tailwind_Spacing_Pr2()
}

func Tailwind_Pr3() any {
	return Tailwind_Spacing_Pr3()
}

func Tailwind_Pr4() any {
	return Tailwind_Spacing_Pr4()
}

func Tailwind_Pr5() any {
	return Tailwind_Spacing_Pr5()
}

func Tailwind_Pr6() any {
	return Tailwind_Spacing_Pr6()
}

func Tailwind_Pr8() any {
	return Tailwind_Spacing_Pr8()
}

func Tailwind_Pb0() any {
	return Tailwind_Spacing_Pb0()
}

func Tailwind_Pb1() any {
	return Tailwind_Spacing_Pb1()
}

func Tailwind_Pb2() any {
	return Tailwind_Spacing_Pb2()
}

func Tailwind_Pb3() any {
	return Tailwind_Spacing_Pb3()
}

func Tailwind_Pb4() any {
	return Tailwind_Spacing_Pb4()
}

func Tailwind_Pb5() any {
	return Tailwind_Spacing_Pb5()
}

func Tailwind_Pb6() any {
	return Tailwind_Spacing_Pb6()
}

func Tailwind_Pb8() any {
	return Tailwind_Spacing_Pb8()
}

func Tailwind_Pl0() any {
	return Tailwind_Spacing_Pl0()
}

func Tailwind_Pl1() any {
	return Tailwind_Spacing_Pl1()
}

func Tailwind_Pl2() any {
	return Tailwind_Spacing_Pl2()
}

func Tailwind_Pl3() any {
	return Tailwind_Spacing_Pl3()
}

func Tailwind_Pl4() any {
	return Tailwind_Spacing_Pl4()
}

func Tailwind_Pl5() any {
	return Tailwind_Spacing_Pl5()
}

func Tailwind_Pl6() any {
	return Tailwind_Spacing_Pl6()
}

func Tailwind_Pl8() any {
	return Tailwind_Spacing_Pl8()
}

func Tailwind_M0() any {
	return Tailwind_Spacing_M0()
}

func Tailwind_M1() any {
	return Tailwind_Spacing_M1()
}

func Tailwind_M2() any {
	return Tailwind_Spacing_M2()
}

func Tailwind_M3() any {
	return Tailwind_Spacing_M3()
}

func Tailwind_M4() any {
	return Tailwind_Spacing_M4()
}

func Tailwind_M5() any {
	return Tailwind_Spacing_M5()
}

func Tailwind_M6() any {
	return Tailwind_Spacing_M6()
}

func Tailwind_M7() any {
	return Tailwind_Spacing_M7()
}

func Tailwind_M8() any {
	return Tailwind_Spacing_M8()
}

func Tailwind_M9() any {
	return Tailwind_Spacing_M9()
}

func Tailwind_M10() any {
	return Tailwind_Spacing_M10()
}

func Tailwind_M11() any {
	return Tailwind_Spacing_M11()
}

func Tailwind_M12() any {
	return Tailwind_Spacing_M12()
}

func Tailwind_M14() any {
	return Tailwind_Spacing_M14()
}

func Tailwind_M16() any {
	return Tailwind_Spacing_M16()
}

func Tailwind_M20() any {
	return Tailwind_Spacing_M20()
}

func Tailwind_M24() any {
	return Tailwind_Spacing_M24()
}

func Tailwind_M28() any {
	return Tailwind_Spacing_M28()
}

func Tailwind_M32() any {
	return Tailwind_Spacing_M32()
}

func Tailwind_M36() any {
	return Tailwind_Spacing_M36()
}

func Tailwind_M40() any {
	return Tailwind_Spacing_M40()
}

func Tailwind_M44() any {
	return Tailwind_Spacing_M44()
}

func Tailwind_M48() any {
	return Tailwind_Spacing_M48()
}

func Tailwind_M52() any {
	return Tailwind_Spacing_M52()
}

func Tailwind_M56() any {
	return Tailwind_Spacing_M56()
}

func Tailwind_M60() any {
	return Tailwind_Spacing_M60()
}

func Tailwind_M64() any {
	return Tailwind_Spacing_M64()
}

func Tailwind_M72() any {
	return Tailwind_Spacing_M72()
}

func Tailwind_M80() any {
	return Tailwind_Spacing_M80()
}

func Tailwind_M96() any {
	return Tailwind_Spacing_M96()
}

func Tailwind_MAuto() any {
	return Tailwind_Spacing_MAuto()
}

func Tailwind_Mx0() any {
	return Tailwind_Spacing_Mx0()
}

func Tailwind_Mx1() any {
	return Tailwind_Spacing_Mx1()
}

func Tailwind_Mx2() any {
	return Tailwind_Spacing_Mx2()
}

func Tailwind_Mx3() any {
	return Tailwind_Spacing_Mx3()
}

func Tailwind_Mx4() any {
	return Tailwind_Spacing_Mx4()
}

func Tailwind_Mx5() any {
	return Tailwind_Spacing_Mx5()
}

func Tailwind_Mx6() any {
	return Tailwind_Spacing_Mx6()
}

func Tailwind_Mx7() any {
	return Tailwind_Spacing_Mx7()
}

func Tailwind_Mx8() any {
	return Tailwind_Spacing_Mx8()
}

func Tailwind_Mx9() any {
	return Tailwind_Spacing_Mx9()
}

func Tailwind_Mx10() any {
	return Tailwind_Spacing_Mx10()
}

func Tailwind_Mx11() any {
	return Tailwind_Spacing_Mx11()
}

func Tailwind_Mx12() any {
	return Tailwind_Spacing_Mx12()
}

func Tailwind_Mx14() any {
	return Tailwind_Spacing_Mx14()
}

func Tailwind_Mx16() any {
	return Tailwind_Spacing_Mx16()
}

func Tailwind_Mx20() any {
	return Tailwind_Spacing_Mx20()
}

func Tailwind_Mx24() any {
	return Tailwind_Spacing_Mx24()
}

func Tailwind_MxAuto() any {
	return Tailwind_Spacing_MxAuto()
}

func Tailwind_My0() any {
	return Tailwind_Spacing_My0()
}

func Tailwind_My1() any {
	return Tailwind_Spacing_My1()
}

func Tailwind_My2() any {
	return Tailwind_Spacing_My2()
}

func Tailwind_My3() any {
	return Tailwind_Spacing_My3()
}

func Tailwind_My4() any {
	return Tailwind_Spacing_My4()
}

func Tailwind_My5() any {
	return Tailwind_Spacing_My5()
}

func Tailwind_My6() any {
	return Tailwind_Spacing_My6()
}

func Tailwind_My7() any {
	return Tailwind_Spacing_My7()
}

func Tailwind_My8() any {
	return Tailwind_Spacing_My8()
}

func Tailwind_My9() any {
	return Tailwind_Spacing_My9()
}

func Tailwind_My10() any {
	return Tailwind_Spacing_My10()
}

func Tailwind_My11() any {
	return Tailwind_Spacing_My11()
}

func Tailwind_My12() any {
	return Tailwind_Spacing_My12()
}

func Tailwind_My14() any {
	return Tailwind_Spacing_My14()
}

func Tailwind_My16() any {
	return Tailwind_Spacing_My16()
}

func Tailwind_My20() any {
	return Tailwind_Spacing_My20()
}

func Tailwind_My24() any {
	return Tailwind_Spacing_My24()
}

func Tailwind_Mt0() any {
	return Tailwind_Spacing_Mt0()
}

func Tailwind_Mt1() any {
	return Tailwind_Spacing_Mt1()
}

func Tailwind_Mt2() any {
	return Tailwind_Spacing_Mt2()
}

func Tailwind_Mt3() any {
	return Tailwind_Spacing_Mt3()
}

func Tailwind_Mt4() any {
	return Tailwind_Spacing_Mt4()
}

func Tailwind_Mt5() any {
	return Tailwind_Spacing_Mt5()
}

func Tailwind_Mt6() any {
	return Tailwind_Spacing_Mt6()
}

func Tailwind_Mt8() any {
	return Tailwind_Spacing_Mt8()
}

func Tailwind_Mt10() any {
	return Tailwind_Spacing_Mt10()
}

func Tailwind_Mt12() any {
	return Tailwind_Spacing_Mt12()
}

func Tailwind_Mt16() any {
	return Tailwind_Spacing_Mt16()
}

func Tailwind_Mt20() any {
	return Tailwind_Spacing_Mt20()
}

func Tailwind_Mr0() any {
	return Tailwind_Spacing_Mr0()
}

func Tailwind_Mr1() any {
	return Tailwind_Spacing_Mr1()
}

func Tailwind_Mr2() any {
	return Tailwind_Spacing_Mr2()
}

func Tailwind_Mr3() any {
	return Tailwind_Spacing_Mr3()
}

func Tailwind_Mr4() any {
	return Tailwind_Spacing_Mr4()
}

func Tailwind_Mr6() any {
	return Tailwind_Spacing_Mr6()
}

func Tailwind_Mr8() any {
	return Tailwind_Spacing_Mr8()
}

func Tailwind_MrAuto() any {
	return Tailwind_Spacing_MrAuto()
}

func Tailwind_Mb0() any {
	return Tailwind_Spacing_Mb0()
}

func Tailwind_Mb1() any {
	return Tailwind_Spacing_Mb1()
}

func Tailwind_Mb2() any {
	return Tailwind_Spacing_Mb2()
}

func Tailwind_Mb3() any {
	return Tailwind_Spacing_Mb3()
}

func Tailwind_Mb4() any {
	return Tailwind_Spacing_Mb4()
}

func Tailwind_Mb5() any {
	return Tailwind_Spacing_Mb5()
}

func Tailwind_Mb6() any {
	return Tailwind_Spacing_Mb6()
}

func Tailwind_Mb8() any {
	return Tailwind_Spacing_Mb8()
}

func Tailwind_Mb10() any {
	return Tailwind_Spacing_Mb10()
}

func Tailwind_Mb12() any {
	return Tailwind_Spacing_Mb12()
}

func Tailwind_Mb16() any {
	return Tailwind_Spacing_Mb16()
}

func Tailwind_Mb20() any {
	return Tailwind_Spacing_Mb20()
}

func Tailwind_Ml0() any {
	return Tailwind_Spacing_Ml0()
}

func Tailwind_Ml1() any {
	return Tailwind_Spacing_Ml1()
}

func Tailwind_Ml2() any {
	return Tailwind_Spacing_Ml2()
}

func Tailwind_Ml3() any {
	return Tailwind_Spacing_Ml3()
}

func Tailwind_Ml4() any {
	return Tailwind_Spacing_Ml4()
}

func Tailwind_Ml6() any {
	return Tailwind_Spacing_Ml6()
}

func Tailwind_Ml8() any {
	return Tailwind_Spacing_Ml8()
}

func Tailwind_MlAuto() any {
	return Tailwind_Spacing_MlAuto()
}

func Tailwind_Gap0() any {
	return Tailwind_Spacing_Gap0()
}

func Tailwind_Gap1() any {
	return Tailwind_Spacing_Gap1()
}

func Tailwind_Gap2() any {
	return Tailwind_Spacing_Gap2()
}

func Tailwind_Gap3() any {
	return Tailwind_Spacing_Gap3()
}

func Tailwind_Gap4() any {
	return Tailwind_Spacing_Gap4()
}

func Tailwind_Gap5() any {
	return Tailwind_Spacing_Gap5()
}

func Tailwind_Gap6() any {
	return Tailwind_Spacing_Gap6()
}

func Tailwind_Gap7() any {
	return Tailwind_Spacing_Gap7()
}

func Tailwind_Gap8() any {
	return Tailwind_Spacing_Gap8()
}

func Tailwind_Gap9() any {
	return Tailwind_Spacing_Gap9()
}

func Tailwind_Gap10() any {
	return Tailwind_Spacing_Gap10()
}

func Tailwind_Gap11() any {
	return Tailwind_Spacing_Gap11()
}

func Tailwind_Gap12() any {
	return Tailwind_Spacing_Gap12()
}

func Tailwind_Gap14() any {
	return Tailwind_Spacing_Gap14()
}

func Tailwind_Gap16() any {
	return Tailwind_Spacing_Gap16()
}

func Tailwind_Gap20() any {
	return Tailwind_Spacing_Gap20()
}

func Tailwind_Gap24() any {
	return Tailwind_Spacing_Gap24()
}

func Tailwind_GapX0() any {
	return Tailwind_Spacing_GapX0()
}

func Tailwind_GapX1() any {
	return Tailwind_Spacing_GapX1()
}

func Tailwind_GapX2() any {
	return Tailwind_Spacing_GapX2()
}

func Tailwind_GapX3() any {
	return Tailwind_Spacing_GapX3()
}

func Tailwind_GapX4() any {
	return Tailwind_Spacing_GapX4()
}

func Tailwind_GapX5() any {
	return Tailwind_Spacing_GapX5()
}

func Tailwind_GapX6() any {
	return Tailwind_Spacing_GapX6()
}

func Tailwind_GapX8() any {
	return Tailwind_Spacing_GapX8()
}

func Tailwind_GapX10() any {
	return Tailwind_Spacing_GapX10()
}

func Tailwind_GapX12() any {
	return Tailwind_Spacing_GapX12()
}

func Tailwind_GapY0() any {
	return Tailwind_Spacing_GapY0()
}

func Tailwind_GapY1() any {
	return Tailwind_Spacing_GapY1()
}

func Tailwind_GapY2() any {
	return Tailwind_Spacing_GapY2()
}

func Tailwind_GapY3() any {
	return Tailwind_Spacing_GapY3()
}

func Tailwind_GapY4() any {
	return Tailwind_Spacing_GapY4()
}

func Tailwind_GapY5() any {
	return Tailwind_Spacing_GapY5()
}

func Tailwind_GapY6() any {
	return Tailwind_Spacing_GapY6()
}

func Tailwind_GapY8() any {
	return Tailwind_Spacing_GapY8()
}

func Tailwind_GapY10() any {
	return Tailwind_Spacing_GapY10()
}

func Tailwind_GapY12() any {
	return Tailwind_Spacing_GapY12()
}

func Tailwind_TextXs() any {
	return Tailwind_Typography_TextXs()
}

func Tailwind_TextSm() any {
	return Tailwind_Typography_TextSm()
}

func Tailwind_TextBase() any {
	return Tailwind_Typography_TextBase()
}

func Tailwind_TextLg() any {
	return Tailwind_Typography_TextLg()
}

func Tailwind_TextXl() any {
	return Tailwind_Typography_TextXl()
}

func Tailwind_Text2xl() any {
	return Tailwind_Typography_Text2xl()
}

func Tailwind_Text3xl() any {
	return Tailwind_Typography_Text3xl()
}

func Tailwind_Text4xl() any {
	return Tailwind_Typography_Text4xl()
}

func Tailwind_Text5xl() any {
	return Tailwind_Typography_Text5xl()
}

func Tailwind_Text6xl() any {
	return Tailwind_Typography_Text6xl()
}

func Tailwind_Text7xl() any {
	return Tailwind_Typography_Text7xl()
}

func Tailwind_Text8xl() any {
	return Tailwind_Typography_Text8xl()
}

func Tailwind_Text9xl() any {
	return Tailwind_Typography_Text9xl()
}

func Tailwind_FontThin() any {
	return Tailwind_Typography_FontThin()
}

func Tailwind_FontExtralight() any {
	return Tailwind_Typography_FontExtralight()
}

func Tailwind_FontLight() any {
	return Tailwind_Typography_FontLight()
}

func Tailwind_FontNormal() any {
	return Tailwind_Typography_FontNormal()
}

func Tailwind_FontMedium() any {
	return Tailwind_Typography_FontMedium()
}

func Tailwind_FontSemibold() any {
	return Tailwind_Typography_FontSemibold()
}

func Tailwind_FontBold() any {
	return Tailwind_Typography_FontBold()
}

func Tailwind_FontExtrabold() any {
	return Tailwind_Typography_FontExtrabold()
}

func Tailwind_FontBlack() any {
	return Tailwind_Typography_FontBlack()
}

func Tailwind_FontSans() any {
	return Tailwind_Typography_FontSans()
}

func Tailwind_FontSerif() any {
	return Tailwind_Typography_FontSerif()
}

func Tailwind_FontMono() any {
	return Tailwind_Typography_FontMono()
}

func Tailwind_TextLeft() any {
	return Tailwind_Typography_TextLeft()
}

func Tailwind_TextCenter() any {
	return Tailwind_Typography_TextCenter()
}

func Tailwind_TextRight() any {
	return Tailwind_Typography_TextRight()
}

func Tailwind_TextJustify() any {
	return Tailwind_Typography_TextJustify()
}

func Tailwind_TextTransparent() any {
	return Tailwind_Typography_TextTransparent()
}

func Tailwind_TextBlack() any {
	return Tailwind_Typography_TextBlack()
}

func Tailwind_TextWhite() any {
	return Tailwind_Typography_TextWhite()
}

func Tailwind_TextGray50() any {
	return Tailwind_Typography_TextGray50()
}

func Tailwind_TextGray100() any {
	return Tailwind_Typography_TextGray100()
}

func Tailwind_TextGray200() any {
	return Tailwind_Typography_TextGray200()
}

func Tailwind_TextGray300() any {
	return Tailwind_Typography_TextGray300()
}

func Tailwind_TextGray400() any {
	return Tailwind_Typography_TextGray400()
}

func Tailwind_TextGray500() any {
	return Tailwind_Typography_TextGray500()
}

func Tailwind_TextGray600() any {
	return Tailwind_Typography_TextGray600()
}

func Tailwind_TextGray700() any {
	return Tailwind_Typography_TextGray700()
}

func Tailwind_TextGray800() any {
	return Tailwind_Typography_TextGray800()
}

func Tailwind_TextGray900() any {
	return Tailwind_Typography_TextGray900()
}

func Tailwind_TextRed100() any {
	return Tailwind_Typography_TextRed100()
}

func Tailwind_TextRed200() any {
	return Tailwind_Typography_TextRed200()
}

func Tailwind_TextRed300() any {
	return Tailwind_Typography_TextRed300()
}

func Tailwind_TextRed400() any {
	return Tailwind_Typography_TextRed400()
}

func Tailwind_TextRed500() any {
	return Tailwind_Typography_TextRed500()
}

func Tailwind_TextRed600() any {
	return Tailwind_Typography_TextRed600()
}

func Tailwind_TextRed700() any {
	return Tailwind_Typography_TextRed700()
}

func Tailwind_TextRed800() any {
	return Tailwind_Typography_TextRed800()
}

func Tailwind_TextRed900() any {
	return Tailwind_Typography_TextRed900()
}

func Tailwind_TextOrange400() any {
	return Tailwind_Typography_TextOrange400()
}

func Tailwind_TextOrange500() any {
	return Tailwind_Typography_TextOrange500()
}

func Tailwind_TextOrange600() any {
	return Tailwind_Typography_TextOrange600()
}

func Tailwind_TextOrange700() any {
	return Tailwind_Typography_TextOrange700()
}

func Tailwind_TextYellow400() any {
	return Tailwind_Typography_TextYellow400()
}

func Tailwind_TextYellow500() any {
	return Tailwind_Typography_TextYellow500()
}

func Tailwind_TextYellow600() any {
	return Tailwind_Typography_TextYellow600()
}

func Tailwind_TextYellow700() any {
	return Tailwind_Typography_TextYellow700()
}

func Tailwind_TextGreen400() any {
	return Tailwind_Typography_TextGreen400()
}

func Tailwind_TextGreen500() any {
	return Tailwind_Typography_TextGreen500()
}

func Tailwind_TextGreen600() any {
	return Tailwind_Typography_TextGreen600()
}

func Tailwind_TextGreen700() any {
	return Tailwind_Typography_TextGreen700()
}

func Tailwind_TextGreen800() any {
	return Tailwind_Typography_TextGreen800()
}

func Tailwind_TextBlue100() any {
	return Tailwind_Typography_TextBlue100()
}

func Tailwind_TextBlue200() any {
	return Tailwind_Typography_TextBlue200()
}

func Tailwind_TextBlue300() any {
	return Tailwind_Typography_TextBlue300()
}

func Tailwind_TextBlue400() any {
	return Tailwind_Typography_TextBlue400()
}

func Tailwind_TextBlue500() any {
	return Tailwind_Typography_TextBlue500()
}

func Tailwind_TextBlue600() any {
	return Tailwind_Typography_TextBlue600()
}

func Tailwind_TextBlue700() any {
	return Tailwind_Typography_TextBlue700()
}

func Tailwind_TextBlue800() any {
	return Tailwind_Typography_TextBlue800()
}

func Tailwind_TextBlue900() any {
	return Tailwind_Typography_TextBlue900()
}

func Tailwind_TextIndigo400() any {
	return Tailwind_Typography_TextIndigo400()
}

func Tailwind_TextIndigo500() any {
	return Tailwind_Typography_TextIndigo500()
}

func Tailwind_TextIndigo600() any {
	return Tailwind_Typography_TextIndigo600()
}

func Tailwind_TextIndigo700() any {
	return Tailwind_Typography_TextIndigo700()
}

func Tailwind_TextPurple400() any {
	return Tailwind_Typography_TextPurple400()
}

func Tailwind_TextPurple500() any {
	return Tailwind_Typography_TextPurple500()
}

func Tailwind_TextPurple600() any {
	return Tailwind_Typography_TextPurple600()
}

func Tailwind_TextPurple700() any {
	return Tailwind_Typography_TextPurple700()
}

func Tailwind_TextPink400() any {
	return Tailwind_Typography_TextPink400()
}

func Tailwind_TextPink500() any {
	return Tailwind_Typography_TextPink500()
}

func Tailwind_TextPink600() any {
	return Tailwind_Typography_TextPink600()
}

func Tailwind_TextTeal400() any {
	return Tailwind_Typography_TextTeal400()
}

func Tailwind_TextTeal500() any {
	return Tailwind_Typography_TextTeal500()
}

func Tailwind_TextTeal600() any {
	return Tailwind_Typography_TextTeal600()
}

func Tailwind_TextTeal700() any {
	return Tailwind_Typography_TextTeal700()
}

func Tailwind_TextCyan400() any {
	return Tailwind_Typography_TextCyan400()
}

func Tailwind_TextCyan500() any {
	return Tailwind_Typography_TextCyan500()
}

func Tailwind_TextCyan600() any {
	return Tailwind_Typography_TextCyan600()
}

func Tailwind_TextCyan700() any {
	return Tailwind_Typography_TextCyan700()
}

func Tailwind_TextEmerald400() any {
	return Tailwind_Typography_TextEmerald400()
}

func Tailwind_TextEmerald500() any {
	return Tailwind_Typography_TextEmerald500()
}

func Tailwind_TextEmerald600() any {
	return Tailwind_Typography_TextEmerald600()
}

func Tailwind_TextEmerald700() any {
	return Tailwind_Typography_TextEmerald700()
}

func Tailwind_TextViolet400() any {
	return Tailwind_Typography_TextViolet400()
}

func Tailwind_TextViolet500() any {
	return Tailwind_Typography_TextViolet500()
}

func Tailwind_TextViolet600() any {
	return Tailwind_Typography_TextViolet600()
}

func Tailwind_TextViolet700() any {
	return Tailwind_Typography_TextViolet700()
}

func Tailwind_TextFuchsia400() any {
	return Tailwind_Typography_TextFuchsia400()
}

func Tailwind_TextFuchsia500() any {
	return Tailwind_Typography_TextFuchsia500()
}

func Tailwind_TextFuchsia600() any {
	return Tailwind_Typography_TextFuchsia600()
}

func Tailwind_TextFuchsia700() any {
	return Tailwind_Typography_TextFuchsia700()
}

func Tailwind_TextRose400() any {
	return Tailwind_Typography_TextRose400()
}

func Tailwind_TextRose500() any {
	return Tailwind_Typography_TextRose500()
}

func Tailwind_TextRose600() any {
	return Tailwind_Typography_TextRose600()
}

func Tailwind_TextRose700() any {
	return Tailwind_Typography_TextRose700()
}

func Tailwind_AlignBaseline() any {
	return Tailwind_Typography_AlignBaseline()
}

func Tailwind_AlignTop() any {
	return Tailwind_Typography_AlignTop()
}

func Tailwind_AlignMiddle() any {
	return Tailwind_Typography_AlignMiddle()
}

func Tailwind_AlignBottom() any {
	return Tailwind_Typography_AlignBottom()
}

func Tailwind_AlignTextTop() any {
	return Tailwind_Typography_AlignTextTop()
}

func Tailwind_AlignTextBottom() any {
	return Tailwind_Typography_AlignTextBottom()
}

func Tailwind_Underline() any {
	return Tailwind_Typography_Underline()
}

func Tailwind_Overline() any {
	return Tailwind_Typography_Overline()
}

func Tailwind_LineThrough() any {
	return Tailwind_Typography_LineThrough()
}

func Tailwind_NoUnderline() any {
	return Tailwind_Typography_NoUnderline()
}

func Tailwind_Uppercase() any {
	return Tailwind_Typography_Uppercase()
}

func Tailwind_Lowercase() any {
	return Tailwind_Typography_Lowercase()
}

func Tailwind_Capitalize() any {
	return Tailwind_Typography_Capitalize()
}

func Tailwind_NormalCase() any {
	return Tailwind_Typography_NormalCase()
}

func Tailwind_LeadingNone() any {
	return Tailwind_Typography_LeadingNone()
}

func Tailwind_LeadingTight() any {
	return Tailwind_Typography_LeadingTight()
}

func Tailwind_LeadingSnug() any {
	return Tailwind_Typography_LeadingSnug()
}

func Tailwind_LeadingNormal() any {
	return Tailwind_Typography_LeadingNormal()
}

func Tailwind_LeadingRelaxed() any {
	return Tailwind_Typography_LeadingRelaxed()
}

func Tailwind_LeadingLoose() any {
	return Tailwind_Typography_LeadingLoose()
}

func Tailwind_TrackingTighter() any {
	return Tailwind_Typography_TrackingTighter()
}

func Tailwind_TrackingTight() any {
	return Tailwind_Typography_TrackingTight()
}

func Tailwind_TrackingNormal() any {
	return Tailwind_Typography_TrackingNormal()
}

func Tailwind_TrackingWide() any {
	return Tailwind_Typography_TrackingWide()
}

func Tailwind_TrackingWider() any {
	return Tailwind_Typography_TrackingWider()
}

func Tailwind_TrackingWidest() any {
	return Tailwind_Typography_TrackingWidest()
}

func Tailwind_WhitespaceNormal() any {
	return Tailwind_Typography_WhitespaceNormal()
}

func Tailwind_WhitespaceNowrap() any {
	return Tailwind_Typography_WhitespaceNowrap()
}

func Tailwind_WhitespacePre() any {
	return Tailwind_Typography_WhitespacePre()
}

func Tailwind_WhitespacePreLine() any {
	return Tailwind_Typography_WhitespacePreLine()
}

func Tailwind_WhitespacePreWrap() any {
	return Tailwind_Typography_WhitespacePreWrap()
}

func Tailwind_Truncate() any {
	return Tailwind_Typography_Truncate()
}

func Tailwind_Block() any {
	return Tailwind_Layout_Block()
}

func Tailwind_InlineBlock() any {
	return Tailwind_Layout_InlineBlock()
}

func Tailwind_Inline() any {
	return Tailwind_Layout_Inline()
}

func Tailwind_Flex() any {
	return Tailwind_Layout_Flex()
}

func Tailwind_InlineFlex() any {
	return Tailwind_Layout_InlineFlex()
}

func Tailwind_Grid() any {
	return Tailwind_Layout_Grid()
}

func Tailwind_InlineGrid() any {
	return Tailwind_Layout_InlineGrid()
}

func Tailwind_Hidden_() any {
	return Tailwind_Layout_Hidden_()
}

func Tailwind_Table_() any {
	return Tailwind_Layout_Table_()
}

func Tailwind_TableRow() any {
	return Tailwind_Layout_TableRow()
}

func Tailwind_TableCell() any {
	return Tailwind_Layout_TableCell()
}

func Tailwind_Static_() any {
	return Tailwind_Layout_Static_()
}

func Tailwind_Relative() any {
	return Tailwind_Layout_Relative()
}

func Tailwind_Absolute() any {
	return Tailwind_Layout_Absolute()
}

func Tailwind_Fixed() any {
	return Tailwind_Layout_Fixed()
}

func Tailwind_Sticky() any {
	return Tailwind_Layout_Sticky()
}

func Tailwind_Inset0() any {
	return Tailwind_Layout_Inset0()
}

func Tailwind_InsetAuto() any {
	return Tailwind_Layout_InsetAuto()
}

func Tailwind_InsetX0() any {
	return Tailwind_Layout_InsetX0()
}

func Tailwind_InsetY0() any {
	return Tailwind_Layout_InsetY0()
}

func Tailwind_Top0() any {
	return Tailwind_Layout_Top0()
}

func Tailwind_Top1() any {
	return Tailwind_Layout_Top1()
}

func Tailwind_Top2() any {
	return Tailwind_Layout_Top2()
}

func Tailwind_Top3() any {
	return Tailwind_Layout_Top3()
}

func Tailwind_Top4() any {
	return Tailwind_Layout_Top4()
}

func Tailwind_Top5() any {
	return Tailwind_Layout_Top5()
}

func Tailwind_Top6() any {
	return Tailwind_Layout_Top6()
}

func Tailwind_Top8() any {
	return Tailwind_Layout_Top8()
}

func Tailwind_Top10() any {
	return Tailwind_Layout_Top10()
}

func Tailwind_Top12() any {
	return Tailwind_Layout_Top12()
}

func Tailwind_Right0() any {
	return Tailwind_Layout_Right0()
}

func Tailwind_Right1() any {
	return Tailwind_Layout_Right1()
}

func Tailwind_Right2() any {
	return Tailwind_Layout_Right2()
}

func Tailwind_Right3() any {
	return Tailwind_Layout_Right3()
}

func Tailwind_Right4() any {
	return Tailwind_Layout_Right4()
}

func Tailwind_Bottom0() any {
	return Tailwind_Layout_Bottom0()
}

func Tailwind_Bottom1() any {
	return Tailwind_Layout_Bottom1()
}

func Tailwind_Bottom2() any {
	return Tailwind_Layout_Bottom2()
}

func Tailwind_Bottom3() any {
	return Tailwind_Layout_Bottom3()
}

func Tailwind_Bottom4() any {
	return Tailwind_Layout_Bottom4()
}

func Tailwind_Left0() any {
	return Tailwind_Layout_Left0()
}

func Tailwind_Left1() any {
	return Tailwind_Layout_Left1()
}

func Tailwind_Left2() any {
	return Tailwind_Layout_Left2()
}

func Tailwind_Left3() any {
	return Tailwind_Layout_Left3()
}

func Tailwind_Left4() any {
	return Tailwind_Layout_Left4()
}

func Tailwind_Z0() any {
	return Tailwind_Layout_Z0()
}

func Tailwind_Z10() any {
	return Tailwind_Layout_Z10()
}

func Tailwind_Z20() any {
	return Tailwind_Layout_Z20()
}

func Tailwind_Z30() any {
	return Tailwind_Layout_Z30()
}

func Tailwind_Z40() any {
	return Tailwind_Layout_Z40()
}

func Tailwind_Z50() any {
	return Tailwind_Layout_Z50()
}

func Tailwind_ZAuto() any {
	return Tailwind_Layout_ZAuto()
}

func Tailwind_OverflowAuto() any {
	return Tailwind_Layout_OverflowAuto()
}

func Tailwind_OverflowHidden() any {
	return Tailwind_Layout_OverflowHidden()
}

func Tailwind_OverflowScroll() any {
	return Tailwind_Layout_OverflowScroll()
}

func Tailwind_OverflowVisible() any {
	return Tailwind_Layout_OverflowVisible()
}

func Tailwind_OverflowXAuto() any {
	return Tailwind_Layout_OverflowXAuto()
}

func Tailwind_OverflowXHidden() any {
	return Tailwind_Layout_OverflowXHidden()
}

func Tailwind_OverflowYAuto() any {
	return Tailwind_Layout_OverflowYAuto()
}

func Tailwind_OverflowYHidden() any {
	return Tailwind_Layout_OverflowYHidden()
}

func Tailwind_W0() any {
	return Tailwind_Layout_W0()
}

func Tailwind_W1() any {
	return Tailwind_Layout_W1()
}

func Tailwind_W2() any {
	return Tailwind_Layout_W2()
}

func Tailwind_W3() any {
	return Tailwind_Layout_W3()
}

func Tailwind_W4() any {
	return Tailwind_Layout_W4()
}

func Tailwind_W5() any {
	return Tailwind_Layout_W5()
}

func Tailwind_W6() any {
	return Tailwind_Layout_W6()
}

func Tailwind_W7() any {
	return Tailwind_Layout_W7()
}

func Tailwind_W8() any {
	return Tailwind_Layout_W8()
}

func Tailwind_W9() any {
	return Tailwind_Layout_W9()
}

func Tailwind_W10() any {
	return Tailwind_Layout_W10()
}

func Tailwind_W11() any {
	return Tailwind_Layout_W11()
}

func Tailwind_W12() any {
	return Tailwind_Layout_W12()
}

func Tailwind_W14() any {
	return Tailwind_Layout_W14()
}

func Tailwind_W16() any {
	return Tailwind_Layout_W16()
}

func Tailwind_W20() any {
	return Tailwind_Layout_W20()
}

func Tailwind_W24() any {
	return Tailwind_Layout_W24()
}

func Tailwind_W28() any {
	return Tailwind_Layout_W28()
}

func Tailwind_W32() any {
	return Tailwind_Layout_W32()
}

func Tailwind_W36() any {
	return Tailwind_Layout_W36()
}

func Tailwind_W40() any {
	return Tailwind_Layout_W40()
}

func Tailwind_W44() any {
	return Tailwind_Layout_W44()
}

func Tailwind_W48() any {
	return Tailwind_Layout_W48()
}

func Tailwind_W52() any {
	return Tailwind_Layout_W52()
}

func Tailwind_W56() any {
	return Tailwind_Layout_W56()
}

func Tailwind_W60() any {
	return Tailwind_Layout_W60()
}

func Tailwind_W64() any {
	return Tailwind_Layout_W64()
}

func Tailwind_W72() any {
	return Tailwind_Layout_W72()
}

func Tailwind_W80() any {
	return Tailwind_Layout_W80()
}

func Tailwind_W96() any {
	return Tailwind_Layout_W96()
}

func Tailwind_WAuto() any {
	return Tailwind_Layout_WAuto()
}

func Tailwind_WFull() any {
	return Tailwind_Layout_WFull()
}

func Tailwind_WScreen() any {
	return Tailwind_Layout_WScreen()
}

func Tailwind_WFit() any {
	return Tailwind_Layout_WFit()
}

func Tailwind_WMin() any {
	return Tailwind_Layout_WMin()
}

func Tailwind_WMax() any {
	return Tailwind_Layout_WMax()
}

func Tailwind_WHalf() any {
	return Tailwind_Layout_WHalf()
}

func Tailwind_WThird() any {
	return Tailwind_Layout_WThird()
}

func Tailwind_W2Third() any {
	return Tailwind_Layout_W2Third()
}

func Tailwind_WQuarter() any {
	return Tailwind_Layout_WQuarter()
}

func Tailwind_W3Quarter() any {
	return Tailwind_Layout_W3Quarter()
}

func Tailwind_H0() any {
	return Tailwind_Layout_H0()
}

func Tailwind_H1() any {
	return Tailwind_Layout_H1()
}

func Tailwind_H2() any {
	return Tailwind_Layout_H2()
}

func Tailwind_H3() any {
	return Tailwind_Layout_H3()
}

func Tailwind_H4() any {
	return Tailwind_Layout_H4()
}

func Tailwind_H5() any {
	return Tailwind_Layout_H5()
}

func Tailwind_H6() any {
	return Tailwind_Layout_H6()
}

func Tailwind_H7() any {
	return Tailwind_Layout_H7()
}

func Tailwind_H8() any {
	return Tailwind_Layout_H8()
}

func Tailwind_H9() any {
	return Tailwind_Layout_H9()
}

func Tailwind_H10() any {
	return Tailwind_Layout_H10()
}

func Tailwind_H11() any {
	return Tailwind_Layout_H11()
}

func Tailwind_H12() any {
	return Tailwind_Layout_H12()
}

func Tailwind_H14() any {
	return Tailwind_Layout_H14()
}

func Tailwind_H16() any {
	return Tailwind_Layout_H16()
}

func Tailwind_H20() any {
	return Tailwind_Layout_H20()
}

func Tailwind_H24() any {
	return Tailwind_Layout_H24()
}

func Tailwind_H28() any {
	return Tailwind_Layout_H28()
}

func Tailwind_H32() any {
	return Tailwind_Layout_H32()
}

func Tailwind_H36() any {
	return Tailwind_Layout_H36()
}

func Tailwind_H40() any {
	return Tailwind_Layout_H40()
}

func Tailwind_H44() any {
	return Tailwind_Layout_H44()
}

func Tailwind_H48() any {
	return Tailwind_Layout_H48()
}

func Tailwind_H52() any {
	return Tailwind_Layout_H52()
}

func Tailwind_H56() any {
	return Tailwind_Layout_H56()
}

func Tailwind_H60() any {
	return Tailwind_Layout_H60()
}

func Tailwind_H64() any {
	return Tailwind_Layout_H64()
}

func Tailwind_H72() any {
	return Tailwind_Layout_H72()
}

func Tailwind_H80() any {
	return Tailwind_Layout_H80()
}

func Tailwind_H96() any {
	return Tailwind_Layout_H96()
}

func Tailwind_HAuto() any {
	return Tailwind_Layout_HAuto()
}

func Tailwind_HFull() any {
	return Tailwind_Layout_HFull()
}

func Tailwind_HScreen() any {
	return Tailwind_Layout_HScreen()
}

func Tailwind_HFit() any {
	return Tailwind_Layout_HFit()
}

func Tailwind_HMin() any {
	return Tailwind_Layout_HMin()
}

func Tailwind_HMax() any {
	return Tailwind_Layout_HMax()
}

func Tailwind_MinW0() any {
	return Tailwind_Layout_MinW0()
}

func Tailwind_MinWFull() any {
	return Tailwind_Layout_MinWFull()
}

func Tailwind_MinWMin() any {
	return Tailwind_Layout_MinWMin()
}

func Tailwind_MinWMax() any {
	return Tailwind_Layout_MinWMax()
}

func Tailwind_MaxWNone() any {
	return Tailwind_Layout_MaxWNone()
}

func Tailwind_MaxWXs() any {
	return Tailwind_Layout_MaxWXs()
}

func Tailwind_MaxWSm() any {
	return Tailwind_Layout_MaxWSm()
}

func Tailwind_MaxWMd() any {
	return Tailwind_Layout_MaxWMd()
}

func Tailwind_MaxWLg() any {
	return Tailwind_Layout_MaxWLg()
}

func Tailwind_MaxWXl() any {
	return Tailwind_Layout_MaxWXl()
}

func Tailwind_MaxW2xl() any {
	return Tailwind_Layout_MaxW2xl()
}

func Tailwind_MaxW3xl() any {
	return Tailwind_Layout_MaxW3xl()
}

func Tailwind_MaxW4xl() any {
	return Tailwind_Layout_MaxW4xl()
}

func Tailwind_MaxW5xl() any {
	return Tailwind_Layout_MaxW5xl()
}

func Tailwind_MaxW6xl() any {
	return Tailwind_Layout_MaxW6xl()
}

func Tailwind_MaxW7xl() any {
	return Tailwind_Layout_MaxW7xl()
}

func Tailwind_MaxWFull() any {
	return Tailwind_Layout_MaxWFull()
}

func Tailwind_MaxWScreenSm() any {
	return Tailwind_Layout_MaxWScreenSm()
}

func Tailwind_MaxWScreenMd() any {
	return Tailwind_Layout_MaxWScreenMd()
}

func Tailwind_MaxWScreenLg() any {
	return Tailwind_Layout_MaxWScreenLg()
}

func Tailwind_MaxWScreenXl() any {
	return Tailwind_Layout_MaxWScreenXl()
}

func Tailwind_MinH0() any {
	return Tailwind_Layout_MinH0()
}

func Tailwind_MinHFull() any {
	return Tailwind_Layout_MinHFull()
}

func Tailwind_MinHScreen() any {
	return Tailwind_Layout_MinHScreen()
}

func Tailwind_MaxHNone() any {
	return Tailwind_Layout_MaxHNone()
}

func Tailwind_MaxHFull() any {
	return Tailwind_Layout_MaxHFull()
}

func Tailwind_MaxHScreen() any {
	return Tailwind_Layout_MaxHScreen()
}

func Tailwind_FlexRow() any {
	return Tailwind_Flex_FlexRow()
}

func Tailwind_FlexRowReverse() any {
	return Tailwind_Flex_FlexRowReverse()
}

func Tailwind_FlexCol() any {
	return Tailwind_Flex_FlexCol()
}

func Tailwind_FlexColReverse() any {
	return Tailwind_Flex_FlexColReverse()
}

func Tailwind_FlexWrap() any {
	return Tailwind_Flex_FlexWrap()
}

func Tailwind_FlexWrapReverse() any {
	return Tailwind_Flex_FlexWrapReverse()
}

func Tailwind_FlexNowrap() any {
	return Tailwind_Flex_FlexNowrap()
}

func Tailwind_JustifyStart() any {
	return Tailwind_Flex_JustifyStart()
}

func Tailwind_JustifyEnd() any {
	return Tailwind_Flex_JustifyEnd()
}

func Tailwind_JustifyCenter() any {
	return Tailwind_Flex_JustifyCenter()
}

func Tailwind_JustifyBetween() any {
	return Tailwind_Flex_JustifyBetween()
}

func Tailwind_JustifyAround() any {
	return Tailwind_Flex_JustifyAround()
}

func Tailwind_JustifyEvenly() any {
	return Tailwind_Flex_JustifyEvenly()
}

func Tailwind_ItemsStart() any {
	return Tailwind_Flex_ItemsStart()
}

func Tailwind_ItemsEnd() any {
	return Tailwind_Flex_ItemsEnd()
}

func Tailwind_ItemsCenter() any {
	return Tailwind_Flex_ItemsCenter()
}

func Tailwind_ItemsBaseline() any {
	return Tailwind_Flex_ItemsBaseline()
}

func Tailwind_ItemsStretch() any {
	return Tailwind_Flex_ItemsStretch()
}

func Tailwind_SelfAuto() any {
	return Tailwind_Flex_SelfAuto()
}

func Tailwind_SelfStart() any {
	return Tailwind_Flex_SelfStart()
}

func Tailwind_SelfEnd() any {
	return Tailwind_Flex_SelfEnd()
}

func Tailwind_SelfCenter() any {
	return Tailwind_Flex_SelfCenter()
}

func Tailwind_SelfStretch() any {
	return Tailwind_Flex_SelfStretch()
}

func Tailwind_ContentStart() any {
	return Tailwind_Flex_ContentStart()
}

func Tailwind_ContentEnd() any {
	return Tailwind_Flex_ContentEnd()
}

func Tailwind_ContentCenter() any {
	return Tailwind_Flex_ContentCenter()
}

func Tailwind_ContentBetween() any {
	return Tailwind_Flex_ContentBetween()
}

func Tailwind_ContentAround() any {
	return Tailwind_Flex_ContentAround()
}

func Tailwind_Flex1() any {
	return Tailwind_Flex_Flex1()
}

func Tailwind_FlexAuto() any {
	return Tailwind_Flex_FlexAuto()
}

func Tailwind_FlexInitial() any {
	return Tailwind_Flex_FlexInitial()
}

func Tailwind_FlexNone() any {
	return Tailwind_Flex_FlexNone()
}

func Tailwind_Grow() any {
	return Tailwind_Flex_Grow()
}

func Tailwind_Grow0() any {
	return Tailwind_Flex_Grow0()
}

func Tailwind_Shrink() any {
	return Tailwind_Flex_Shrink()
}

func Tailwind_Shrink0() any {
	return Tailwind_Flex_Shrink0()
}

func Tailwind_Order1() any {
	return Tailwind_Flex_Order1()
}

func Tailwind_Order2() any {
	return Tailwind_Flex_Order2()
}

func Tailwind_Order3() any {
	return Tailwind_Flex_Order3()
}

func Tailwind_Order4() any {
	return Tailwind_Flex_Order4()
}

func Tailwind_Order5() any {
	return Tailwind_Flex_Order5()
}

func Tailwind_Order6() any {
	return Tailwind_Flex_Order6()
}

func Tailwind_Order7() any {
	return Tailwind_Flex_Order7()
}

func Tailwind_Order8() any {
	return Tailwind_Flex_Order8()
}

func Tailwind_Order9() any {
	return Tailwind_Flex_Order9()
}

func Tailwind_Order10() any {
	return Tailwind_Flex_Order10()
}

func Tailwind_Order11() any {
	return Tailwind_Flex_Order11()
}

func Tailwind_Order12() any {
	return Tailwind_Flex_Order12()
}

func Tailwind_OrderFirst() any {
	return Tailwind_Flex_OrderFirst()
}

func Tailwind_OrderLast() any {
	return Tailwind_Flex_OrderLast()
}

func Tailwind_OrderNone() any {
	return Tailwind_Flex_OrderNone()
}

func Tailwind_GridCols1() any {
	return Tailwind_Grid_GridCols1()
}

func Tailwind_GridCols2() any {
	return Tailwind_Grid_GridCols2()
}

func Tailwind_GridCols3() any {
	return Tailwind_Grid_GridCols3()
}

func Tailwind_GridCols4() any {
	return Tailwind_Grid_GridCols4()
}

func Tailwind_GridCols5() any {
	return Tailwind_Grid_GridCols5()
}

func Tailwind_GridCols6() any {
	return Tailwind_Grid_GridCols6()
}

func Tailwind_GridCols7() any {
	return Tailwind_Grid_GridCols7()
}

func Tailwind_GridCols8() any {
	return Tailwind_Grid_GridCols8()
}

func Tailwind_GridCols9() any {
	return Tailwind_Grid_GridCols9()
}

func Tailwind_GridCols10() any {
	return Tailwind_Grid_GridCols10()
}

func Tailwind_GridCols11() any {
	return Tailwind_Grid_GridCols11()
}

func Tailwind_GridCols12() any {
	return Tailwind_Grid_GridCols12()
}

func Tailwind_GridColsNone() any {
	return Tailwind_Grid_GridColsNone()
}

func Tailwind_GridRows1() any {
	return Tailwind_Grid_GridRows1()
}

func Tailwind_GridRows2() any {
	return Tailwind_Grid_GridRows2()
}

func Tailwind_GridRows3() any {
	return Tailwind_Grid_GridRows3()
}

func Tailwind_GridRows4() any {
	return Tailwind_Grid_GridRows4()
}

func Tailwind_GridRows5() any {
	return Tailwind_Grid_GridRows5()
}

func Tailwind_GridRows6() any {
	return Tailwind_Grid_GridRows6()
}

func Tailwind_GridRowsNone() any {
	return Tailwind_Grid_GridRowsNone()
}

func Tailwind_ColSpan1() any {
	return Tailwind_Grid_ColSpan1()
}

func Tailwind_ColSpan2() any {
	return Tailwind_Grid_ColSpan2()
}

func Tailwind_ColSpan3() any {
	return Tailwind_Grid_ColSpan3()
}

func Tailwind_ColSpan4() any {
	return Tailwind_Grid_ColSpan4()
}

func Tailwind_ColSpan5() any {
	return Tailwind_Grid_ColSpan5()
}

func Tailwind_ColSpan6() any {
	return Tailwind_Grid_ColSpan6()
}

func Tailwind_ColSpan7() any {
	return Tailwind_Grid_ColSpan7()
}

func Tailwind_ColSpan8() any {
	return Tailwind_Grid_ColSpan8()
}

func Tailwind_ColSpan9() any {
	return Tailwind_Grid_ColSpan9()
}

func Tailwind_ColSpan10() any {
	return Tailwind_Grid_ColSpan10()
}

func Tailwind_ColSpan11() any {
	return Tailwind_Grid_ColSpan11()
}

func Tailwind_ColSpan12() any {
	return Tailwind_Grid_ColSpan12()
}

func Tailwind_ColSpanFull() any {
	return Tailwind_Grid_ColSpanFull()
}

func Tailwind_RowSpan1() any {
	return Tailwind_Grid_RowSpan1()
}

func Tailwind_RowSpan2() any {
	return Tailwind_Grid_RowSpan2()
}

func Tailwind_RowSpan3() any {
	return Tailwind_Grid_RowSpan3()
}

func Tailwind_RowSpan4() any {
	return Tailwind_Grid_RowSpan4()
}

func Tailwind_RowSpan5() any {
	return Tailwind_Grid_RowSpan5()
}

func Tailwind_RowSpan6() any {
	return Tailwind_Grid_RowSpan6()
}

func Tailwind_RowSpanFull() any {
	return Tailwind_Grid_RowSpanFull()
}

func Tailwind_GridFlowRow() any {
	return Tailwind_Grid_GridFlowRow()
}

func Tailwind_GridFlowCol() any {
	return Tailwind_Grid_GridFlowCol()
}

func Tailwind_GridFlowDense() any {
	return Tailwind_Grid_GridFlowDense()
}

func Tailwind_GridFlowRowDense() any {
	return Tailwind_Grid_GridFlowRowDense()
}

func Tailwind_GridFlowColDense() any {
	return Tailwind_Grid_GridFlowColDense()
}

func Tailwind_PlaceContentCenter() any {
	return Tailwind_Grid_PlaceContentCenter()
}

func Tailwind_PlaceContentStart() any {
	return Tailwind_Grid_PlaceContentStart()
}

func Tailwind_PlaceContentEnd() any {
	return Tailwind_Grid_PlaceContentEnd()
}

func Tailwind_PlaceContentBetween() any {
	return Tailwind_Grid_PlaceContentBetween()
}

func Tailwind_PlaceItemsCenter() any {
	return Tailwind_Grid_PlaceItemsCenter()
}

func Tailwind_PlaceItemsStart() any {
	return Tailwind_Grid_PlaceItemsStart()
}

func Tailwind_PlaceItemsEnd() any {
	return Tailwind_Grid_PlaceItemsEnd()
}

func Tailwind_PlaceItemsStretch() any {
	return Tailwind_Grid_PlaceItemsStretch()
}

func Tailwind_PlaceSelfCenter() any {
	return Tailwind_Grid_PlaceSelfCenter()
}

func Tailwind_PlaceSelfStart() any {
	return Tailwind_Grid_PlaceSelfStart()
}

func Tailwind_PlaceSelfEnd() any {
	return Tailwind_Grid_PlaceSelfEnd()
}

func Tailwind_PlaceSelfAuto() any {
	return Tailwind_Grid_PlaceSelfAuto()
}

func Tailwind_BgTransparent() any {
	return Tailwind_Background_BgTransparent()
}

func Tailwind_BgBlack() any {
	return Tailwind_Background_BgBlack()
}

func Tailwind_BgWhite() any {
	return Tailwind_Background_BgWhite()
}

func Tailwind_BgGray50() any {
	return Tailwind_Background_BgGray50()
}

func Tailwind_BgGray100() any {
	return Tailwind_Background_BgGray100()
}

func Tailwind_BgGray200() any {
	return Tailwind_Background_BgGray200()
}

func Tailwind_BgGray300() any {
	return Tailwind_Background_BgGray300()
}

func Tailwind_BgGray400() any {
	return Tailwind_Background_BgGray400()
}

func Tailwind_BgGray500() any {
	return Tailwind_Background_BgGray500()
}

func Tailwind_BgGray600() any {
	return Tailwind_Background_BgGray600()
}

func Tailwind_BgGray700() any {
	return Tailwind_Background_BgGray700()
}

func Tailwind_BgGray800() any {
	return Tailwind_Background_BgGray800()
}

func Tailwind_BgGray900() any {
	return Tailwind_Background_BgGray900()
}

func Tailwind_BgGray950() any {
	return Tailwind_Background_BgGray950()
}

func Tailwind_BgSlate50() any {
	return Tailwind_Background_BgSlate50()
}

func Tailwind_BgSlate100() any {
	return Tailwind_Background_BgSlate100()
}

func Tailwind_BgSlate200() any {
	return Tailwind_Background_BgSlate200()
}

func Tailwind_BgSlate300() any {
	return Tailwind_Background_BgSlate300()
}

func Tailwind_BgSlate400() any {
	return Tailwind_Background_BgSlate400()
}

func Tailwind_BgSlate500() any {
	return Tailwind_Background_BgSlate500()
}

func Tailwind_BgSlate600() any {
	return Tailwind_Background_BgSlate600()
}

func Tailwind_BgSlate700() any {
	return Tailwind_Background_BgSlate700()
}

func Tailwind_BgSlate800() any {
	return Tailwind_Background_BgSlate800()
}

func Tailwind_BgSlate900() any {
	return Tailwind_Background_BgSlate900()
}

func Tailwind_BgRed50() any {
	return Tailwind_Background_BgRed50()
}

func Tailwind_BgRed100() any {
	return Tailwind_Background_BgRed100()
}

func Tailwind_BgRed200() any {
	return Tailwind_Background_BgRed200()
}

func Tailwind_BgRed300() any {
	return Tailwind_Background_BgRed300()
}

func Tailwind_BgRed400() any {
	return Tailwind_Background_BgRed400()
}

func Tailwind_BgRed500() any {
	return Tailwind_Background_BgRed500()
}

func Tailwind_BgRed600() any {
	return Tailwind_Background_BgRed600()
}

func Tailwind_BgRed700() any {
	return Tailwind_Background_BgRed700()
}

func Tailwind_BgRed800() any {
	return Tailwind_Background_BgRed800()
}

func Tailwind_BgRed900() any {
	return Tailwind_Background_BgRed900()
}

func Tailwind_BgOrange50() any {
	return Tailwind_Background_BgOrange50()
}

func Tailwind_BgOrange100() any {
	return Tailwind_Background_BgOrange100()
}

func Tailwind_BgOrange200() any {
	return Tailwind_Background_BgOrange200()
}

func Tailwind_BgOrange300() any {
	return Tailwind_Background_BgOrange300()
}

func Tailwind_BgOrange400() any {
	return Tailwind_Background_BgOrange400()
}

func Tailwind_BgOrange500() any {
	return Tailwind_Background_BgOrange500()
}

func Tailwind_BgOrange600() any {
	return Tailwind_Background_BgOrange600()
}

func Tailwind_BgOrange700() any {
	return Tailwind_Background_BgOrange700()
}

func Tailwind_BgYellow50() any {
	return Tailwind_Background_BgYellow50()
}

func Tailwind_BgYellow100() any {
	return Tailwind_Background_BgYellow100()
}

func Tailwind_BgYellow200() any {
	return Tailwind_Background_BgYellow200()
}

func Tailwind_BgYellow300() any {
	return Tailwind_Background_BgYellow300()
}

func Tailwind_BgYellow400() any {
	return Tailwind_Background_BgYellow400()
}

func Tailwind_BgYellow500() any {
	return Tailwind_Background_BgYellow500()
}

func Tailwind_BgGreen50() any {
	return Tailwind_Background_BgGreen50()
}

func Tailwind_BgGreen100() any {
	return Tailwind_Background_BgGreen100()
}

func Tailwind_BgGreen200() any {
	return Tailwind_Background_BgGreen200()
}

func Tailwind_BgGreen300() any {
	return Tailwind_Background_BgGreen300()
}

func Tailwind_BgGreen400() any {
	return Tailwind_Background_BgGreen400()
}

func Tailwind_BgGreen500() any {
	return Tailwind_Background_BgGreen500()
}

func Tailwind_BgGreen600() any {
	return Tailwind_Background_BgGreen600()
}

func Tailwind_BgGreen700() any {
	return Tailwind_Background_BgGreen700()
}

func Tailwind_BgGreen800() any {
	return Tailwind_Background_BgGreen800()
}

func Tailwind_BgGreen900() any {
	return Tailwind_Background_BgGreen900()
}

func Tailwind_BgBlue50() any {
	return Tailwind_Background_BgBlue50()
}

func Tailwind_BgBlue100() any {
	return Tailwind_Background_BgBlue100()
}

func Tailwind_BgBlue200() any {
	return Tailwind_Background_BgBlue200()
}

func Tailwind_BgBlue300() any {
	return Tailwind_Background_BgBlue300()
}

func Tailwind_BgBlue400() any {
	return Tailwind_Background_BgBlue400()
}

func Tailwind_BgBlue500() any {
	return Tailwind_Background_BgBlue500()
}

func Tailwind_BgBlue600() any {
	return Tailwind_Background_BgBlue600()
}

func Tailwind_BgBlue700() any {
	return Tailwind_Background_BgBlue700()
}

func Tailwind_BgBlue800() any {
	return Tailwind_Background_BgBlue800()
}

func Tailwind_BgBlue900() any {
	return Tailwind_Background_BgBlue900()
}

func Tailwind_BgIndigo50() any {
	return Tailwind_Background_BgIndigo50()
}

func Tailwind_BgIndigo100() any {
	return Tailwind_Background_BgIndigo100()
}

func Tailwind_BgIndigo200() any {
	return Tailwind_Background_BgIndigo200()
}

func Tailwind_BgIndigo300() any {
	return Tailwind_Background_BgIndigo300()
}

func Tailwind_BgIndigo400() any {
	return Tailwind_Background_BgIndigo400()
}

func Tailwind_BgIndigo500() any {
	return Tailwind_Background_BgIndigo500()
}

func Tailwind_BgIndigo600() any {
	return Tailwind_Background_BgIndigo600()
}

func Tailwind_BgIndigo700() any {
	return Tailwind_Background_BgIndigo700()
}

func Tailwind_BgPurple50() any {
	return Tailwind_Background_BgPurple50()
}

func Tailwind_BgPurple100() any {
	return Tailwind_Background_BgPurple100()
}

func Tailwind_BgPurple200() any {
	return Tailwind_Background_BgPurple200()
}

func Tailwind_BgPurple300() any {
	return Tailwind_Background_BgPurple300()
}

func Tailwind_BgPurple400() any {
	return Tailwind_Background_BgPurple400()
}

func Tailwind_BgPurple500() any {
	return Tailwind_Background_BgPurple500()
}

func Tailwind_BgPurple600() any {
	return Tailwind_Background_BgPurple600()
}

func Tailwind_BgPurple700() any {
	return Tailwind_Background_BgPurple700()
}

func Tailwind_BgPink50() any {
	return Tailwind_Background_BgPink50()
}

func Tailwind_BgPink100() any {
	return Tailwind_Background_BgPink100()
}

func Tailwind_BgPink200() any {
	return Tailwind_Background_BgPink200()
}

func Tailwind_BgPink300() any {
	return Tailwind_Background_BgPink300()
}

func Tailwind_BgPink400() any {
	return Tailwind_Background_BgPink400()
}

func Tailwind_BgPink500() any {
	return Tailwind_Background_BgPink500()
}

func Tailwind_BgPink600() any {
	return Tailwind_Background_BgPink600()
}

func Tailwind_BgPink700() any {
	return Tailwind_Background_BgPink700()
}

func Tailwind_BgAuto() any {
	return Tailwind_Background_BgAuto()
}

func Tailwind_BgCover() any {
	return Tailwind_Background_BgCover()
}

func Tailwind_BgContain() any {
	return Tailwind_Background_BgContain()
}

func Tailwind_BgCenter() any {
	return Tailwind_Background_BgCenter()
}

func Tailwind_BgTop() any {
	return Tailwind_Background_BgTop()
}

func Tailwind_BgBottom() any {
	return Tailwind_Background_BgBottom()
}

func Tailwind_BgLeft() any {
	return Tailwind_Background_BgLeft()
}

func Tailwind_BgRight() any {
	return Tailwind_Background_BgRight()
}

func Tailwind_BgRepeat() any {
	return Tailwind_Background_BgRepeat()
}

func Tailwind_BgNoRepeat() any {
	return Tailwind_Background_BgNoRepeat()
}

func Tailwind_BgRepeatX() any {
	return Tailwind_Background_BgRepeatX()
}

func Tailwind_BgRepeatY() any {
	return Tailwind_Background_BgRepeatY()
}

func Tailwind_Border() any {
	return Tailwind_Border_Border()
}

func Tailwind_Border0() any {
	return Tailwind_Border_Border0()
}

func Tailwind_Border2() any {
	return Tailwind_Border_Border2()
}

func Tailwind_Border4() any {
	return Tailwind_Border_Border4()
}

func Tailwind_Border8() any {
	return Tailwind_Border_Border8()
}

func Tailwind_BorderT() any {
	return Tailwind_Border_BorderT()
}

func Tailwind_BorderR() any {
	return Tailwind_Border_BorderR()
}

func Tailwind_BorderB() any {
	return Tailwind_Border_BorderB()
}

func Tailwind_BorderL() any {
	return Tailwind_Border_BorderL()
}

func Tailwind_BorderT0() any {
	return Tailwind_Border_BorderT0()
}

func Tailwind_BorderT2() any {
	return Tailwind_Border_BorderT2()
}

func Tailwind_BorderB0() any {
	return Tailwind_Border_BorderB0()
}

func Tailwind_BorderB2() any {
	return Tailwind_Border_BorderB2()
}

func Tailwind_BorderTransparent() any {
	return Tailwind_Border_BorderTransparent()
}

func Tailwind_BorderBlack() any {
	return Tailwind_Border_BorderBlack()
}

func Tailwind_BorderWhite() any {
	return Tailwind_Border_BorderWhite()
}

func Tailwind_BorderGray50() any {
	return Tailwind_Border_BorderGray50()
}

func Tailwind_BorderGray100() any {
	return Tailwind_Border_BorderGray100()
}

func Tailwind_BorderGray200() any {
	return Tailwind_Border_BorderGray200()
}

func Tailwind_BorderGray300() any {
	return Tailwind_Border_BorderGray300()
}

func Tailwind_BorderGray400() any {
	return Tailwind_Border_BorderGray400()
}

func Tailwind_BorderGray500() any {
	return Tailwind_Border_BorderGray500()
}

func Tailwind_BorderGray600() any {
	return Tailwind_Border_BorderGray600()
}

func Tailwind_BorderGray700() any {
	return Tailwind_Border_BorderGray700()
}

func Tailwind_BorderGray800() any {
	return Tailwind_Border_BorderGray800()
}

func Tailwind_BorderGray900() any {
	return Tailwind_Border_BorderGray900()
}

func Tailwind_BorderRed100() any {
	return Tailwind_Border_BorderRed100()
}

func Tailwind_BorderRed200() any {
	return Tailwind_Border_BorderRed200()
}

func Tailwind_BorderRed300() any {
	return Tailwind_Border_BorderRed300()
}

func Tailwind_BorderRed400() any {
	return Tailwind_Border_BorderRed400()
}

func Tailwind_BorderRed500() any {
	return Tailwind_Border_BorderRed500()
}

func Tailwind_BorderRed600() any {
	return Tailwind_Border_BorderRed600()
}

func Tailwind_BorderRed700() any {
	return Tailwind_Border_BorderRed700()
}

func Tailwind_BorderRed800() any {
	return Tailwind_Border_BorderRed800()
}

func Tailwind_BorderBlue100() any {
	return Tailwind_Border_BorderBlue100()
}

func Tailwind_BorderBlue200() any {
	return Tailwind_Border_BorderBlue200()
}

func Tailwind_BorderBlue300() any {
	return Tailwind_Border_BorderBlue300()
}

func Tailwind_BorderBlue400() any {
	return Tailwind_Border_BorderBlue400()
}

func Tailwind_BorderBlue500() any {
	return Tailwind_Border_BorderBlue500()
}

func Tailwind_BorderBlue600() any {
	return Tailwind_Border_BorderBlue600()
}

func Tailwind_BorderBlue700() any {
	return Tailwind_Border_BorderBlue700()
}

func Tailwind_BorderBlue800() any {
	return Tailwind_Border_BorderBlue800()
}

func Tailwind_BorderGreen100() any {
	return Tailwind_Border_BorderGreen100()
}

func Tailwind_BorderGreen200() any {
	return Tailwind_Border_BorderGreen200()
}

func Tailwind_BorderGreen300() any {
	return Tailwind_Border_BorderGreen300()
}

func Tailwind_BorderGreen400() any {
	return Tailwind_Border_BorderGreen400()
}

func Tailwind_BorderGreen500() any {
	return Tailwind_Border_BorderGreen500()
}

func Tailwind_BorderGreen600() any {
	return Tailwind_Border_BorderGreen600()
}

func Tailwind_BorderGreen700() any {
	return Tailwind_Border_BorderGreen700()
}

func Tailwind_BorderGreen800() any {
	return Tailwind_Border_BorderGreen800()
}

func Tailwind_BorderYellow200() any {
	return Tailwind_Border_BorderYellow200()
}

func Tailwind_BorderYellow300() any {
	return Tailwind_Border_BorderYellow300()
}

func Tailwind_BorderYellow400() any {
	return Tailwind_Border_BorderYellow400()
}

func Tailwind_BorderYellow500() any {
	return Tailwind_Border_BorderYellow500()
}

func Tailwind_BorderYellow600() any {
	return Tailwind_Border_BorderYellow600()
}

func Tailwind_BorderIndigo500() any {
	return Tailwind_Border_BorderIndigo500()
}

func Tailwind_BorderIndigo600() any {
	return Tailwind_Border_BorderIndigo600()
}

func Tailwind_BorderPurple500() any {
	return Tailwind_Border_BorderPurple500()
}

func Tailwind_BorderPurple600() any {
	return Tailwind_Border_BorderPurple600()
}

func Tailwind_BorderOrange500() any {
	return Tailwind_Border_BorderOrange500()
}

func Tailwind_BorderPink500() any {
	return Tailwind_Border_BorderPink500()
}

func Tailwind_BorderSlate200() any {
	return Tailwind_Border_BorderSlate200()
}

func Tailwind_BorderSlate300() any {
	return Tailwind_Border_BorderSlate300()
}

func Tailwind_BorderSlate400() any {
	return Tailwind_Border_BorderSlate400()
}

func Tailwind_BorderAmber500() any {
	return Tailwind_Border_BorderAmber500()
}

func Tailwind_BorderTeal500() any {
	return Tailwind_Border_BorderTeal500()
}

func Tailwind_BorderCyan500() any {
	return Tailwind_Border_BorderCyan500()
}

func Tailwind_BorderSky500() any {
	return Tailwind_Border_BorderSky500()
}

func Tailwind_BorderEmerald500() any {
	return Tailwind_Border_BorderEmerald500()
}

func Tailwind_BorderViolet500() any {
	return Tailwind_Border_BorderViolet500()
}

func Tailwind_BorderRose500() any {
	return Tailwind_Border_BorderRose500()
}

func Tailwind_BorderSolid() any {
	return Tailwind_Border_BorderSolid()
}

func Tailwind_BorderDashed() any {
	return Tailwind_Border_BorderDashed()
}

func Tailwind_BorderDotted() any {
	return Tailwind_Border_BorderDotted()
}

func Tailwind_BorderDouble() any {
	return Tailwind_Border_BorderDouble()
}

func Tailwind_BorderNone_() any {
	return Tailwind_Border_BorderNone_()
}

func Tailwind_RoundedNone() any {
	return Tailwind_Border_RoundedNone()
}

func Tailwind_RoundedSm() any {
	return Tailwind_Border_RoundedSm()
}

func Tailwind_Rounded() any {
	return Tailwind_Border_Rounded()
}

func Tailwind_RoundedMd() any {
	return Tailwind_Border_RoundedMd()
}

func Tailwind_RoundedLg() any {
	return Tailwind_Border_RoundedLg()
}

func Tailwind_RoundedXl() any {
	return Tailwind_Border_RoundedXl()
}

func Tailwind_Rounded2xl() any {
	return Tailwind_Border_Rounded2xl()
}

func Tailwind_Rounded3xl() any {
	return Tailwind_Border_Rounded3xl()
}

func Tailwind_RoundedFull() any {
	return Tailwind_Border_RoundedFull()
}

func Tailwind_RoundedT() any {
	return Tailwind_Border_RoundedT()
}

func Tailwind_RoundedR() any {
	return Tailwind_Border_RoundedR()
}

func Tailwind_RoundedB() any {
	return Tailwind_Border_RoundedB()
}

func Tailwind_RoundedL() any {
	return Tailwind_Border_RoundedL()
}

func Tailwind_RoundedTLg() any {
	return Tailwind_Border_RoundedTLg()
}

func Tailwind_RoundedBLg() any {
	return Tailwind_Border_RoundedBLg()
}

func Tailwind_RoundedTXl() any {
	return Tailwind_Border_RoundedTXl()
}

func Tailwind_RoundedBXl() any {
	return Tailwind_Border_RoundedBXl()
}

func Tailwind_Ring() any {
	return Tailwind_Border_Ring()
}

func Tailwind_Ring0() any {
	return Tailwind_Border_Ring0()
}

func Tailwind_Ring1() any {
	return Tailwind_Border_Ring1()
}

func Tailwind_Ring2() any {
	return Tailwind_Border_Ring2()
}

func Tailwind_Ring4() any {
	return Tailwind_Border_Ring4()
}

func Tailwind_ShadowSm() any {
	return Tailwind_Effects_ShadowSm()
}

func Tailwind_Shadow() any {
	return Tailwind_Effects_Shadow()
}

func Tailwind_ShadowMd() any {
	return Tailwind_Effects_ShadowMd()
}

func Tailwind_ShadowLg() any {
	return Tailwind_Effects_ShadowLg()
}

func Tailwind_ShadowXl() any {
	return Tailwind_Effects_ShadowXl()
}

func Tailwind_Shadow2xl() any {
	return Tailwind_Effects_Shadow2xl()
}

func Tailwind_ShadowInner() any {
	return Tailwind_Effects_ShadowInner()
}

func Tailwind_ShadowNone() any {
	return Tailwind_Effects_ShadowNone()
}

func Tailwind_Opacity0() any {
	return Tailwind_Effects_Opacity0()
}

func Tailwind_Opacity5() any {
	return Tailwind_Effects_Opacity5()
}

func Tailwind_Opacity10() any {
	return Tailwind_Effects_Opacity10()
}

func Tailwind_Opacity20() any {
	return Tailwind_Effects_Opacity20()
}

func Tailwind_Opacity25() any {
	return Tailwind_Effects_Opacity25()
}

func Tailwind_Opacity30() any {
	return Tailwind_Effects_Opacity30()
}

func Tailwind_Opacity40() any {
	return Tailwind_Effects_Opacity40()
}

func Tailwind_Opacity50() any {
	return Tailwind_Effects_Opacity50()
}

func Tailwind_Opacity60() any {
	return Tailwind_Effects_Opacity60()
}

func Tailwind_Opacity70() any {
	return Tailwind_Effects_Opacity70()
}

func Tailwind_Opacity75() any {
	return Tailwind_Effects_Opacity75()
}

func Tailwind_Opacity80() any {
	return Tailwind_Effects_Opacity80()
}

func Tailwind_Opacity90() any {
	return Tailwind_Effects_Opacity90()
}

func Tailwind_Opacity95() any {
	return Tailwind_Effects_Opacity95()
}

func Tailwind_Opacity100() any {
	return Tailwind_Effects_Opacity100()
}

func Tailwind_TransitionAll() any {
	return Tailwind_Effects_TransitionAll()
}

func Tailwind_Transition() any {
	return Tailwind_Effects_Transition()
}

func Tailwind_TransitionColors() any {
	return Tailwind_Effects_TransitionColors()
}

func Tailwind_TransitionOpacity() any {
	return Tailwind_Effects_TransitionOpacity()
}

func Tailwind_TransitionShadow() any {
	return Tailwind_Effects_TransitionShadow()
}

func Tailwind_TransitionTransform() any {
	return Tailwind_Effects_TransitionTransform()
}

func Tailwind_TransitionNone() any {
	return Tailwind_Effects_TransitionNone()
}

func Tailwind_Duration75() any {
	return Tailwind_Effects_Duration75()
}

func Tailwind_Duration100() any {
	return Tailwind_Effects_Duration100()
}

func Tailwind_Duration150() any {
	return Tailwind_Effects_Duration150()
}

func Tailwind_Duration200() any {
	return Tailwind_Effects_Duration200()
}

func Tailwind_Duration300() any {
	return Tailwind_Effects_Duration300()
}

func Tailwind_Duration500() any {
	return Tailwind_Effects_Duration500()
}

func Tailwind_Duration700() any {
	return Tailwind_Effects_Duration700()
}

func Tailwind_Duration1000() any {
	return Tailwind_Effects_Duration1000()
}

func Tailwind_EaseLinear() any {
	return Tailwind_Effects_EaseLinear()
}

func Tailwind_EaseIn() any {
	return Tailwind_Effects_EaseIn()
}

func Tailwind_EaseOut() any {
	return Tailwind_Effects_EaseOut()
}

func Tailwind_EaseInOut() any {
	return Tailwind_Effects_EaseInOut()
}

func Tailwind_Scale0() any {
	return Tailwind_Effects_Scale0()
}

func Tailwind_Scale50() any {
	return Tailwind_Effects_Scale50()
}

func Tailwind_Scale75() any {
	return Tailwind_Effects_Scale75()
}

func Tailwind_Scale90() any {
	return Tailwind_Effects_Scale90()
}

func Tailwind_Scale95() any {
	return Tailwind_Effects_Scale95()
}

func Tailwind_Scale100() any {
	return Tailwind_Effects_Scale100()
}

func Tailwind_Scale105() any {
	return Tailwind_Effects_Scale105()
}

func Tailwind_Scale110() any {
	return Tailwind_Effects_Scale110()
}

func Tailwind_Scale125() any {
	return Tailwind_Effects_Scale125()
}

func Tailwind_Scale150() any {
	return Tailwind_Effects_Scale150()
}

func Tailwind_Rotate0() any {
	return Tailwind_Effects_Rotate0()
}

func Tailwind_Rotate1() any {
	return Tailwind_Effects_Rotate1()
}

func Tailwind_Rotate2() any {
	return Tailwind_Effects_Rotate2()
}

func Tailwind_Rotate3() any {
	return Tailwind_Effects_Rotate3()
}

func Tailwind_Rotate6() any {
	return Tailwind_Effects_Rotate6()
}

func Tailwind_Rotate12() any {
	return Tailwind_Effects_Rotate12()
}

func Tailwind_Rotate45() any {
	return Tailwind_Effects_Rotate45()
}

func Tailwind_Rotate90() any {
	return Tailwind_Effects_Rotate90()
}

func Tailwind_Rotate180() any {
	return Tailwind_Effects_Rotate180()
}

func Tailwind_CursorAuto() any {
	return Tailwind_Effects_CursorAuto()
}

func Tailwind_CursorDefault() any {
	return Tailwind_Effects_CursorDefault()
}

func Tailwind_CursorPointer() any {
	return Tailwind_Effects_CursorPointer()
}

func Tailwind_CursorWait() any {
	return Tailwind_Effects_CursorWait()
}

func Tailwind_CursorText() any {
	return Tailwind_Effects_CursorText()
}

func Tailwind_CursorMove() any {
	return Tailwind_Effects_CursorMove()
}

func Tailwind_CursorNotAllowed() any {
	return Tailwind_Effects_CursorNotAllowed()
}

func Tailwind_CursorGrab() any {
	return Tailwind_Effects_CursorGrab()
}

func Tailwind_CursorGrabbing() any {
	return Tailwind_Effects_CursorGrabbing()
}

func Tailwind_PointerEventsNone() any {
	return Tailwind_Effects_PointerEventsNone()
}

func Tailwind_PointerEventsAuto() any {
	return Tailwind_Effects_PointerEventsAuto()
}

func Tailwind_SelectNone() any {
	return Tailwind_Effects_SelectNone()
}

func Tailwind_SelectText() any {
	return Tailwind_Effects_SelectText()
}

func Tailwind_SelectAll() any {
	return Tailwind_Effects_SelectAll()
}

func Tailwind_SelectAuto() any {
	return Tailwind_Effects_SelectAuto()
}

func Tailwind_Hover(attr any) any {
	return Tailwind_State_Hover(attr)
}

func Tailwind_Focus(attr any) any {
	return Tailwind_State_Focus(attr)
}

func Tailwind_Active(attr any) any {
	return Tailwind_State_Active(attr)
}

func Tailwind_Disabled(attr any) any {
	return Tailwind_State_Disabled(attr)
}

func Tailwind_FirstChild(attr any) any {
	return Tailwind_State_FirstChild(attr)
}

func Tailwind_LastChild(attr any) any {
	return Tailwind_State_LastChild(attr)
}

func Tailwind_Odd(attr any) any {
	return Tailwind_State_Odd(attr)
}

func Tailwind_Even(attr any) any {
	return Tailwind_State_Even(attr)
}

func Tailwind_Sm(attr any) any {
	return Tailwind_Responsive_Sm(attr)
}

func Tailwind_Md(attr any) any {
	return Tailwind_Responsive_Md(attr)
}

func Tailwind_Lg(attr any) any {
	return Tailwind_Responsive_Lg(attr)
}

func Tailwind_Xl(attr any) any {
	return Tailwind_Responsive_Xl(attr)
}

func Tailwind_Xxl(attr any) any {
	return Tailwind_Responsive_Xxl(attr)
}

func Tailwind_Group() any {
	return Tailwind_State_Group()
}

func Tailwind_GroupHover(attr any) any {
	return Tailwind_State_GroupHover(attr)
}

func Tailwind_GroupFocus(attr any) any {
	return Tailwind_State_GroupFocus(attr)
}

func Tailwind_FocusVisible(attr any) any {
	return Tailwind_State_FocusVisible(attr)
}

func Tailwind_FocusWithin(attr any) any {
	return Tailwind_State_FocusWithin(attr)
}

func Tailwind_Placeholder_(attr any) any {
	return Tailwind_State_Placeholder(attr)
}

func Tailwind_Checked(attr any) any {
	return Tailwind_State_Checked(attr)
}

func Tailwind_AspectAuto() any {
	return Tailwind_Sizing_AspectAuto()
}

func Tailwind_AspectSquare() any {
	return Tailwind_Sizing_AspectSquare()
}

func Tailwind_AspectVideo() any {
	return Tailwind_Sizing_AspectVideo()
}

func Tailwind_ObjectContain() any {
	return Tailwind_Sizing_ObjectContain()
}

func Tailwind_ObjectCover() any {
	return Tailwind_Sizing_ObjectCover()
}

func Tailwind_ObjectFill() any {
	return Tailwind_Sizing_ObjectFill()
}

func Tailwind_ObjectNone() any {
	return Tailwind_Sizing_ObjectNone()
}

func Tailwind_ObjectScaleDown() any {
	return Tailwind_Sizing_ObjectScaleDown()
}

func Tailwind_ObjectBottom() any {
	return Tailwind_Sizing_ObjectBottom()
}

func Tailwind_ObjectCenter() any {
	return Tailwind_Sizing_ObjectCenter()
}

func Tailwind_ObjectLeft() any {
	return Tailwind_Sizing_ObjectLeft()
}

func Tailwind_ObjectRight() any {
	return Tailwind_Sizing_ObjectRight()
}

func Tailwind_ObjectTop() any {
	return Tailwind_Sizing_ObjectTop()
}

func Tailwind_BoxBorder() any {
	return Tailwind_Sizing_BoxBorder()
}

func Tailwind_BoxContent() any {
	return Tailwind_Sizing_BoxContent()
}

func Tailwind_Container_() any {
	return Tailwind_Sizing_Container()
}

func Tailwind_AnimateNone() any {
	return Tailwind_Animation_AnimateNone()
}

func Tailwind_AnimateSpin() any {
	return Tailwind_Animation_AnimateSpin()
}

func Tailwind_AnimatePing() any {
	return Tailwind_Animation_AnimatePing()
}

func Tailwind_AnimatePulse() any {
	return Tailwind_Animation_AnimatePulse()
}

func Tailwind_AnimateBounce() any {
	return Tailwind_Animation_AnimateBounce()
}

func Tailwind_TranslateX0() any {
	return Tailwind_Transforms_TranslateX0()
}

func Tailwind_TranslateX1() any {
	return Tailwind_Transforms_TranslateX1()
}

func Tailwind_TranslateX2() any {
	return Tailwind_Transforms_TranslateX2()
}

func Tailwind_TranslateX4() any {
	return Tailwind_Transforms_TranslateX4()
}

func Tailwind_TranslateX8() any {
	return Tailwind_Transforms_TranslateX8()
}

func Tailwind_TranslateXHalf() any {
	return Tailwind_Transforms_TranslateXHalf()
}

func Tailwind_TranslateXFull() any {
	return Tailwind_Transforms_TranslateXFull()
}

func Tailwind_NegTranslateXHalf() any {
	return Tailwind_Transforms_NegTranslateXHalf()
}

func Tailwind_NegTranslateXFull() any {
	return Tailwind_Transforms_NegTranslateXFull()
}

func Tailwind_TranslateY0() any {
	return Tailwind_Transforms_TranslateY0()
}

func Tailwind_TranslateY1() any {
	return Tailwind_Transforms_TranslateY1()
}

func Tailwind_TranslateY2() any {
	return Tailwind_Transforms_TranslateY2()
}

func Tailwind_TranslateY4() any {
	return Tailwind_Transforms_TranslateY4()
}

func Tailwind_TranslateY8() any {
	return Tailwind_Transforms_TranslateY8()
}

func Tailwind_TranslateYHalf() any {
	return Tailwind_Transforms_TranslateYHalf()
}

func Tailwind_TranslateYFull() any {
	return Tailwind_Transforms_TranslateYFull()
}

func Tailwind_NegTranslateYHalf() any {
	return Tailwind_Transforms_NegTranslateYHalf()
}

func Tailwind_NegTranslateYFull() any {
	return Tailwind_Transforms_NegTranslateYFull()
}

func Tailwind_OriginCenter() any {
	return Tailwind_Transforms_OriginCenter()
}

func Tailwind_OriginTop() any {
	return Tailwind_Transforms_OriginTop()
}

func Tailwind_OriginBottom() any {
	return Tailwind_Transforms_OriginBottom()
}

func Tailwind_OriginLeft() any {
	return Tailwind_Transforms_OriginLeft()
}

func Tailwind_OriginRight() any {
	return Tailwind_Transforms_OriginRight()
}

func Tailwind_BlurNone() any {
	return Tailwind_Filters_BlurNone()
}

func Tailwind_BlurSm() any {
	return Tailwind_Filters_BlurSm()
}

func Tailwind_Blur_() any {
	return Tailwind_Filters_Blur_()
}

func Tailwind_BlurMd() any {
	return Tailwind_Filters_BlurMd()
}

func Tailwind_BlurLg() any {
	return Tailwind_Filters_BlurLg()
}

func Tailwind_BlurXl() any {
	return Tailwind_Filters_BlurXl()
}

func Tailwind_BackdropBlurNone() any {
	return Tailwind_Filters_BackdropBlurNone()
}

func Tailwind_BackdropBlurSm() any {
	return Tailwind_Filters_BackdropBlurSm()
}

func Tailwind_BackdropBlur() any {
	return Tailwind_Filters_BackdropBlur()
}

func Tailwind_BackdropBlurMd() any {
	return Tailwind_Filters_BackdropBlurMd()
}

func Tailwind_BackdropBlurLg() any {
	return Tailwind_Filters_BackdropBlurLg()
}

func Tailwind_Grayscale() any {
	return Tailwind_Filters_Grayscale()
}

func Tailwind_Grayscale0() any {
	return Tailwind_Filters_Grayscale0()
}

func Tailwind_Invert() any {
	return Tailwind_Filters_Invert()
}

func Tailwind_Sepia() any {
	return Tailwind_Filters_Sepia()
}

func Tailwind_DropShadow() any {
	return Tailwind_Filters_DropShadow()
}

func Tailwind_DropShadowMd() any {
	return Tailwind_Filters_DropShadowMd()
}

func Tailwind_DropShadowLg() any {
	return Tailwind_Filters_DropShadowLg()
}

func Tailwind_DropShadowNone() any {
	return Tailwind_Filters_DropShadowNone()
}

func Tailwind_BgGradientToT() any {
	return Tailwind_Gradient_BgGradientToT()
}

func Tailwind_BgGradientToTR() any {
	return Tailwind_Gradient_BgGradientToTR()
}

func Tailwind_BgGradientToR() any {
	return Tailwind_Gradient_BgGradientToR()
}

func Tailwind_BgGradientToBR() any {
	return Tailwind_Gradient_BgGradientToBR()
}

func Tailwind_BgGradientToB() any {
	return Tailwind_Gradient_BgGradientToB()
}

func Tailwind_BgGradientToBL() any {
	return Tailwind_Gradient_BgGradientToBL()
}

func Tailwind_BgGradientToL() any {
	return Tailwind_Gradient_BgGradientToL()
}

func Tailwind_BgGradientToTL() any {
	return Tailwind_Gradient_BgGradientToTL()
}

func Tailwind_FromTransparent() any {
	return Tailwind_Gradient_FromTransparent()
}

func Tailwind_FromBlack() any {
	return Tailwind_Gradient_FromBlack()
}

func Tailwind_FromWhite() any {
	return Tailwind_Gradient_FromWhite()
}

func Tailwind_FromSlate900() any {
	return Tailwind_Gradient_FromSlate900()
}

func Tailwind_FromSlate800() any {
	return Tailwind_Gradient_FromSlate800()
}

func Tailwind_FromSlate700() any {
	return Tailwind_Gradient_FromSlate700()
}

func Tailwind_FromBlue500() any {
	return Tailwind_Gradient_FromBlue500()
}

func Tailwind_FromBlue600() any {
	return Tailwind_Gradient_FromBlue600()
}

func Tailwind_FromGreen500() any {
	return Tailwind_Gradient_FromGreen500()
}

func Tailwind_FromRed500() any {
	return Tailwind_Gradient_FromRed500()
}

func Tailwind_FromPurple500() any {
	return Tailwind_Gradient_FromPurple500()
}

func Tailwind_ToTransparent() any {
	return Tailwind_Gradient_ToTransparent()
}

func Tailwind_ToBlack() any {
	return Tailwind_Gradient_ToBlack()
}

func Tailwind_ToWhite() any {
	return Tailwind_Gradient_ToWhite()
}

func Tailwind_ToSlate500() any {
	return Tailwind_Gradient_ToSlate500()
}

func Tailwind_ToSlate700() any {
	return Tailwind_Gradient_ToSlate700()
}

func Tailwind_ToBlue500() any {
	return Tailwind_Gradient_ToBlue500()
}

func Tailwind_ToGreen500() any {
	return Tailwind_Gradient_ToGreen500()
}

func Tailwind_OutlineNone() any {
	return Tailwind_Outline_OutlineNone()
}

func Tailwind_Outline_() any {
	return Tailwind_Outline_Outline_()
}

func Tailwind_OutlineDashed() any {
	return Tailwind_Outline_OutlineDashed()
}

func Tailwind_Outline0() any {
	return Tailwind_Outline_Outline0()
}

func Tailwind_Outline2() any {
	return Tailwind_Outline_Outline2()
}

func Tailwind_OutlineOffset0() any {
	return Tailwind_Outline_OutlineOffset0()
}

func Tailwind_OutlineOffset2() any {
	return Tailwind_Outline_OutlineOffset2()
}

func Tailwind_DivideX() any {
	return Tailwind_Divide_DivideX()
}

func Tailwind_DivideY() any {
	return Tailwind_Divide_DivideY()
}

func Tailwind_DivideX0() any {
	return Tailwind_Divide_DivideX0()
}

func Tailwind_DivideY0() any {
	return Tailwind_Divide_DivideY0()
}

func Tailwind_DivideGray200() any {
	return Tailwind_Divide_DivideGray200()
}

func Tailwind_DivideSlate200() any {
	return Tailwind_Divide_DivideSlate200()
}

func Tailwind_RingTransparent() any {
	return Tailwind_RingColor_RingTransparent()
}

func Tailwind_RingBlack() any {
	return Tailwind_RingColor_RingBlack()
}

func Tailwind_RingWhite() any {
	return Tailwind_RingColor_RingWhite()
}

func Tailwind_RingBlue500() any {
	return Tailwind_RingColor_RingBlue500()
}

func Tailwind_RingBlue600() any {
	return Tailwind_RingColor_RingBlue600()
}

func Tailwind_RingGreen500() any {
	return Tailwind_RingColor_RingGreen500()
}

func Tailwind_RingRed500() any {
	return Tailwind_RingColor_RingRed500()
}

func Tailwind_RingOffset0() any {
	return Tailwind_RingColor_RingOffset0()
}

func Tailwind_RingOffset2() any {
	return Tailwind_RingColor_RingOffset2()
}

func Tailwind_RingInset() any {
	return Tailwind_RingColor_RingInset()
}

func Tailwind_SrOnly() any {
	return Tailwind_Accessibility_SrOnly()
}

func Tailwind_NotSrOnly() any {
	return Tailwind_Accessibility_NotSrOnly()
}

func Tailwind_FillNone() any {
	return Tailwind_Svg_FillNone()
}

func Tailwind_FillCurrent() any {
	return Tailwind_Svg_FillCurrent()
}

func Tailwind_StrokeNone() any {
	return Tailwind_Svg_StrokeNone()
}

func Tailwind_StrokeCurrent() any {
	return Tailwind_Svg_StrokeCurrent()
}

func Tailwind_BorderCollapse() any {
	return Tailwind_Tables_BorderCollapse()
}

func Tailwind_BorderSeparate() any {
	return Tailwind_Tables_BorderSeparate()
}

func Tailwind_TableAuto() any {
	return Tailwind_Tables_TableAuto()
}

func Tailwind_TableFixed() any {
	return Tailwind_Tables_TableFixed()
}

func Tailwind_AppearanceNone() any {
	return Tailwind_Interactivity_AppearanceNone()
}

func Tailwind_ScrollSmooth() any {
	return Tailwind_Interactivity_ScrollSmooth()
}

func Tailwind_ScrollAuto() any {
	return Tailwind_Interactivity_ScrollAuto()
}

func Tailwind_TouchNone() any {
	return Tailwind_Interactivity_TouchNone()
}

func Tailwind_TouchManipulation() any {
	return Tailwind_Interactivity_TouchManipulation()
}

func Tailwind_WillChangeTransform() any {
	return Tailwind_Interactivity_WillChangeTransform()
}

func Tailwind_ResizeNone() any {
	return Tailwind_Interactivity_ResizeNone()
}