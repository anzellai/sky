func Ui_Layout_H2_() any {
	return SkyTuple2{V0: "class", V1: "h-2"}
}

func Ui_Layout_H5_() any {
	return SkyTuple2{V0: "class", V1: "h-5"}
}

func Ui_Layout_H6_() any {
	return SkyTuple2{V0: "class", V1: "h-6"}
}

func Ui_Layout_BtnPrimary() any {
	return tw([]any{textSm, fontSemibold, textWhite, bgBlue600, px5, py2, roundedMd, inlineBlock, hover(bgBlue700), transitionColors})
}

func Ui_Layout_BtnPrimaryLg() any {
	return tw([]any{wFull, textBase, fontSemibold, textWhite, bgBlue600, py3, roundedMd, block, textCenter, hover(bgBlue700), transitionColors})
}

func Ui_Layout_BtnSecondary() any {
	return tw([]any{textSm, fontMedium, textGray500, px5, py2, border, borderGray200, roundedMd, inlineBlock, hover(borderGray400), hover(textGray900), transitionColors})
}

func Ui_Layout_BtnCheckout() any {
	return tw([]any{wFull, mt5, bgGreen600, textWhite, py3, roundedMd, fontSemibold, textBase, hover(bgGreen700), transitionColors})
}

func Ui_Layout_BtnDangerOutline() any {
	return tw([]any{textSm, fontMedium, textRed500, px4, py2, border, borderRed200, roundedMd, bgWhite, hover(bgRed50), transitionColors})
}

func Ui_Layout_BtnState() any {
	return tw([]any{textSm, fontMedium, px4, py2, border, borderGray200, roundedMd, bgWhite, textGray700, hover(bgGray100), transitionColors})
}

func Ui_Layout_Card() any {
	return tw([]any{bgWhite, roundedLg, border, borderGray200, overflowHidden})
}

func Ui_Layout_CardHeader() any {
	return tw([]any{p6, borderB, borderGray100})
}

func Ui_Layout_CardBody() any {
	return tw([]any{p6})
}

func Ui_Layout_CardSection() any {
	return tw([]any{p6, borderT, borderGray100})
}

func Ui_Layout_CardFooter() any {
	return tw([]any{p6, borderT, borderGray100, flex, itemsCenter, justifyBetween})
}

func Ui_Layout_FormInput() any {
	return tw([]any{wFull, border, borderGray300, roundedLg, px3, py2, textSm, bgWhite, focus(borderBlue500), focus(ring2), focus(ringBlue500)})
}

func Ui_Layout_FormLabel_() any {
	return tw([]any{block, textSm, fontSemibold, textGray700, mb1})
}

func Ui_Layout_QtyBtn() any {
	return tw([]any{w8, h8, flex, itemsCenter, justifyCenter, border, borderGray300, roundedMd, textBase, textGray700, bgWhite, hover(bgGray100)})
}

func Ui_Layout_EmptyIcon() any {
	return tw([]any{w20, h20, roundedFull, bgGray100, flex, itemsCenter, justifyCenter, mxAuto, mb6, text3xl, textGray400})
}

func Ui_Layout_EmptyIconSm() any {
	return tw([]any{w16, h16, roundedFull, bgGray100, flex, itemsCenter, justifyCenter, mxAuto, mb6, textXl, textGray400})
}

func Ui_Layout_AdminThumb() any {
	return tw([]any{w10, h10, roundedMd, objectCover, shrink0})
}

func Ui_Layout_AdminThumbPlaceholder() any {
	return tw([]any{w10, h10, roundedMd, shrink0, bgGray100, flex, itemsCenter, justifyCenter, textGray400, textXs})
}

func Ui_Layout_NotificationSuccess() any {
	return tw([]any{flex, itemsCenter, gap3, px4, py3, roundedMd, textSm, fontMedium, bgGreen50, textGreen700, border, borderGreen500})
}

func Ui_Layout_NotificationError() any {
	return tw([]any{flex, itemsCenter, gap3, px4, py3, roundedMd, textSm, fontMedium, bgRed50, textRed700, border, borderRed500})
}

func Ui_Layout_SidebarLink_() any {
	return tw([]any{block, textSm, fontMedium, textGray500, px3, py2, roundedMd, mb1, hover(textGray900), hover(bgGray100)})
}

func Ui_Layout_SidebarLinkActive() any {
	return tw([]any{block, textSm, fontSemibold, textBlue600, bgBlue50, px3, py2, roundedMd, mb1})
}

func Ui_Layout_TableHeader_() any {
	return tw([]any{px6, py3, textXs, fontSemibold, uppercase, textGray500, borderB, borderGray200, bgGray50})
}

func Ui_Layout_AdminRow() any {
	return tw([]any{flex, itemsCenter, px6, py4, borderB, borderGray100, hover(bgGray50)})
}

func Ui_Layout_Badge_() any {
	return tw([]any{textXs, fontSemibold, px2, py1, roundedFull, inlineBlock})
}

func Ui_Layout_BadgeLive() any {
	return tw([]any{textXs, fontSemibold, px2, py1, roundedFull, inlineBlock, textGreen600, bgGreen50})
}

func Ui_Layout_BadgeDraft() any {
	return tw([]any{textXs, fontSemibold, px2, py1, roundedFull, inlineBlock, textGray500, bgGray100})
}

func Ui_Layout_BadgeOrdered() any {
	return tw([]any{textXs, fontSemibold, px2, py1, roundedFull, inlineBlock, textBlue700, bgBlue50})
}

func Ui_Layout_BadgeShipped() any {
	return tw([]any{textXs, fontSemibold, px2, py1, roundedFull, inlineBlock, textYellow500, bgYellow100})
}

func Ui_Layout_BadgeDelivered() any {
	return tw([]any{textXs, fontSemibold, px2, py1, roundedFull, inlineBlock, textGreen600, bgGreen50})
}

func Ui_Layout_BadgeCancelled() any {
	return tw([]any{textXs, fontSemibold, px2, py1, roundedFull, inlineBlock, textRed600, bgRed50})
}

func Ui_Layout_BadgeRefunded() any {
	return tw([]any{textXs, fontSemibold, px2, py1, roundedFull, inlineBlock, textGray500, bgGray100})
}

func Ui_Layout_BadgePending() any {
	return tw([]any{textXs, fontSemibold, px2, py1, roundedFull, inlineBlock, textOrange500, bgOrange100})
}

func Ui_Layout_InfoCard_() any {
	return tw([]any{bgWhite, roundedLg, border, borderGray200, p5})
}

func Ui_Layout_InfoCardLabel() any {
	return tw([]any{textXs, fontSemibold, uppercase, textGray400, block, mb2})
}

func Ui_Layout_CartItem() any {
	return tw([]any{flex, itemsCenter, gap4, px6, py5, borderB, borderGray100})
}

func Ui_Layout_CartThumb() any {
	return tw([]any{w16, h16, roundedMd, objectCover, shrink0})
}

func Ui_Layout_CartThumbPlaceholder() any {
	return tw([]any{w16, h16, roundedMd, shrink0, bgGray100, flex, itemsCenter, justifyCenter, textGray400})
}

func Ui_Layout_LineTotal() any {
	return tw([]any{fontSemibold, textGray900, textRight})
}

func Ui_Layout_RemoveBtn() any {
	return tw([]any{textXs, textRed500, px2, py1, roundedSm, hover(bgRed50)})
}

func Ui_Layout_GalleryMain() any {
	return tw([]any{aspectSquare, roundedLg, overflowHidden, border, borderGray200})
}

func Ui_Layout_GalleryThumb() any {
	return tw([]any{aspectSquare, roundedMd, overflowHidden, border, borderGray200, cursorPointer})
}

func Ui_Layout_GalleryPlaceholder() any {
	return tw([]any{aspectSquare, roundedLg, wFull, bgGray100, flex, itemsCenter, justifyCenter, textGray400})
}

func Ui_Layout_ImgPlaceholder() any {
	return tw([]any{bgGray100, flex, itemsCenter, justifyCenter, textGray400, textSm, wFull, hFull})
}

func Ui_Layout_ObjectCover_() any {
	return tw([]any{wFull, hFull, objectCover})
}

func Ui_Layout_UploadZone() any {
	return tw([]any{flex, gap3, itemsCenter, p4, border2, borderDashed, borderGray200, roundedMd, bgGray50})
}

func Ui_Layout_ImgCardSquare() any {
	return tw([]any{relative, aspectSquare, roundedMd, overflowHidden, border, borderGray200})
}

func Ui_Layout_ImgOverlayDelete() any {
	return tw([]any{absolute, top1, right1, w6, Ui_Layout_H6_(), bgGray900, textWhite, roundedFull, textXs, flex, itemsCenter, justifyCenter})
}

func Ui_Layout_Page(model any, content any) any {
	return sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{minHScreen, flex, flexCol})}), []any{styles, Ui_Layout_GlobalStyles(), sky_htmlRaw(Lib_OAuth_FirebaseAuthScript()), Ui_Layout_ViewHeader(model), Ui_Layout_ViewNotification(model), sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{flex1, maxW7xl, mxAuto, wFull, px4, py8})}), []any{content}), Ui_Layout_ViewFooter(model), Ui_Layout_ViewSignOutTrigger(model)})
}

func Ui_Layout_ViewSignOutTrigger(model any) any {
	return func() any { if sky_asBool(sky_equal(sky_asMap(model)["signOutPending"], true)) { return sky_call(sky_call(sky_htmlEl("div"), []any{sky_call(sky_attrCustom("data-sky-eval"), "skySignOut()")}), []any{}) }; return sky_htmlText("") }()
}

func Ui_Layout_AdminPage(model any, content any) any {
	return func() any { return func() any { __subject := sky_asMap(model)["user"]; if sky_asSkyMaybe(__subject).SkyName == "Just" { user := sky_asSkyMaybe(__subject).JustValue; _ = user; return func() any { if sky_asBool(Lib_Auth_IsAdmin(user)) { return sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{minHScreen, flex, flexCol})}), []any{styles, Ui_Layout_GlobalStyles(), Ui_Layout_ViewHeader(model), Ui_Layout_ViewNotification(model), sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{flex1, wFull, px4, py8})}), []any{sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{flex, flexCol, lg(flexRow), gap6, maxW7xl, mxAuto})}), []any{Ui_Layout_AdminSidebar(model), sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{flex1, minW0})}), []any{content})})}), Ui_Layout_ViewFooter(model)}) }; return Ui_Layout_Page(model, sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{textCenter, py20})}), []any{sky_call(sky_call(sky_htmlEl("h2"), []any{tw([]any{text2xl, fontBold, textGray400})}), []any{sky_htmlText("Access denied")})})) }() };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return Ui_Layout_Page(model, sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{textCenter, py20})}), []any{sky_call(sky_call(sky_htmlEl("h2"), []any{tw([]any{text2xl, fontBold, textGray400})}), []any{sky_htmlText("Please sign in")})})) };  return nil }() }()
}

func Ui_Layout_ViewHeader(model any) any {
	return func() any { lang := sky_asMap(model)["lang"]; _ = lang; return sky_call(sky_call(sky_htmlEl("header"), []any{tw([]any{bgWhite, borderB, borderGray200, sticky, top0, z50}), sky_call(sky_attrSimple("style"), "backdrop-filter:blur(8px);background:rgba(255,255,255,.95)")}), []any{sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{maxW7xl, mxAuto, px4, flex, itemsCenter, justifyBetween, h16})}), []any{sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{flex, itemsCenter, gap6})}), []any{sky_call(sky_call(sky_htmlEl("a"), []any{sky_call(sky_attrSimple("href"), "/"), sky_call(sky_attrCustom("sky-nav"), ""), tw([]any{text2xl, fontExtrabold, textGray900})}), []any{sky_htmlText(Lib_Translation_T(lang, "app.name"))}), sky_call(sky_call(sky_htmlEl("nav"), []any{tw([]any{hidden_, md(flex), gap1})}), []any{Ui_Layout_NavLink("/", Lib_Translation_T(lang, "nav.home"), model), Ui_Layout_NavLink("/products", Lib_Translation_T(lang, "nav.products"), model)})}), sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{flex, itemsCenter, gap4})}), []any{Ui_Layout_LangToggle(model), Ui_Layout_ViewUserNav(model)})})}) }()
}

func Ui_Layout_NavLink(url any, label any, model any) any {
	return func() any { isActive := func() any { return func() any { __subject := sky_asMap(model)["page"]; if sky_asMap(__subject)["SkyName"] == "HomePage" { return sky_equal(url, "/") };  if sky_asMap(__subject)["SkyName"] == "ProductsPage" { return sky_equal(url, "/products") };  if sky_asMap(__subject)["SkyName"] == "ProductPage" { return sky_equal(url, "/products") };  if true { return false };  return nil }() }(); _ = isActive; return sky_call(sky_call(sky_htmlEl("a"), []any{sky_call(sky_attrSimple("href"), url), sky_call(sky_attrCustom("sky-nav"), ""), func() any { if sky_asBool(isActive) { return tw([]any{textSm, fontMedium, textBlue600, bgBlue50, px3, py1, roundedMd}) }; return tw([]any{textSm, fontMedium, textGray500, px3, py1, roundedMd, hover(textGray900), hover(bgGray100)}) }()}), []any{sky_htmlText(label)}) }()
}

func Ui_Layout_LangToggle(model any) any {
	return func() any { nextLang := func() any { if sky_asBool(sky_equal(sky_asMap(model)["lang"], "en")) { return "zh" }; return "en" }(); _ = nextLang; label := func() any { if sky_asBool(sky_equal(sky_asMap(model)["lang"], "en")) { return "中文" }; return "EN" }(); _ = label; return sky_call(sky_call(sky_htmlEl("button"), []any{sky_call(sky_evtHandler("click"), SetLang(nextLang)), tw([]any{textXs, fontSemibold, textGray500, border, borderGray200, roundedMd, px2, py1, bgWhite, hover(borderGray400), hover(textGray900)})}), []any{sky_htmlText(label)}) }()
}

func Ui_Layout_ViewUserNav(model any) any {
	return func() any { lang := sky_asMap(model)["lang"]; _ = lang; return func() any { return func() any { __subject := sky_asMap(model)["user"]; if sky_asSkyMaybe(__subject).SkyName == "Just" { user := sky_asSkyMaybe(__subject).JustValue; _ = user; return sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{flex, itemsCenter, gap4})}), []any{sky_call(sky_call(sky_htmlEl("a"), []any{sky_call(sky_attrSimple("href"), "/cart"), sky_call(sky_attrCustom("sky-nav"), ""), tw([]any{textSm, fontMedium, textGray500, relative, px3, py1, roundedMd, hover(textGray900), hover(bgGray100)})}), []any{sky_call(sky_call(sky_htmlEl("span"), []any{}), []any{sky_htmlText(Lib_Translation_T(lang, "nav.cart"))}), Ui_Layout_ViewCartBadge(model)}), sky_call(sky_call(sky_htmlEl("a"), []any{sky_call(sky_attrSimple("href"), "/orders"), sky_call(sky_attrCustom("sky-nav"), ""), tw([]any{textSm, fontMedium, textGray500, px3, py1, roundedMd, hover(textGray900), hover(bgGray100)})}), []any{sky_htmlText(Lib_Translation_T(lang, "nav.orders"))}), Ui_Layout_ViewAdminLink(model, user), sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{flex, itemsCenter, gap2, bgGray50, border, borderGray200, roundedFull, px3, py1})}), []any{sky_call(sky_call(sky_htmlEl("span"), []any{tw([]any{textSm, fontMedium})}), []any{sky_htmlText(Lib_Db_GetField("name", user))}), sky_call(sky_call(sky_htmlEl("button"), []any{sky_call(sky_evtHandler("click"), DoSignOut), tw([]any{textXs, fontMedium, textRed500, px2, py1, roundedSm, hover(bgRed50)})}), []any{sky_htmlText(Lib_Translation_T(lang, "nav.signout"))})})}) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{flex, itemsCenter, gap3})}), []any{sky_call(sky_call(sky_htmlEl("a"), []any{sky_call(sky_attrSimple("href"), "/auth/signin"), sky_call(sky_attrCustom("sky-nav"), ""), Ui_Layout_BtnPrimary()}), []any{sky_htmlText(Lib_Translation_T(lang, "nav.signin"))})}) };  return nil }() }() }()
}

func Ui_Layout_ViewAdminLink(model any, user any) any {
	return func() any { if sky_asBool(Lib_Auth_IsAdmin(user)) { return sky_call(sky_call(sky_htmlEl("a"), []any{sky_call(sky_attrSimple("href"), "/admin/products"), sky_call(sky_attrCustom("sky-nav"), ""), tw([]any{textXs, fontSemibold, textPurple600, bgPurple50, px2, py1, roundedMd, hover(bgPurple100)})}), []any{sky_htmlText(Lib_Translation_T(sky_asMap(model)["lang"], "nav.admin"))}) }; return sky_htmlText("") }()
}

func Ui_Layout_ViewCartBadge(model any) any {
	return func() any { itemCount := sky_listLength(sky_asMap(model)["cartItems"]); _ = itemCount; return func() any { if sky_asBool(sky_asInt(itemCount) > sky_asInt(0)) { return sky_call(sky_call(sky_htmlEl("span"), []any{tw([]any{absolute, bgRed500, textWhite, textXs, fontBold, roundedFull, flex, itemsCenter, justifyCenter, w5, Ui_Layout_H5_()}), sky_call(sky_attrSimple("style"), "top:-4px;right:-4px;font-size:.625rem")}), []any{sky_htmlText(sky_stringFromInt(itemCount))}) }; return sky_htmlText("") }() }()
}

func Ui_Layout_ViewNotification(model any) any {
	return func() any { if sky_asBool(sky_equal(sky_asMap(model)["notification"], "")) { return sky_htmlText("") }; return func() any { notifStyle := func() any { if sky_asBool(sky_equal(sky_asMap(model)["notificationType"], "error")) { return Ui_Layout_NotificationError() }; return Ui_Layout_NotificationSuccess() }(); _ = notifStyle; return sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{maxW7xl, mxAuto, px4, mt4})}), []any{sky_call(sky_call(sky_htmlEl("div"), []any{notifStyle, sky_call(sky_attrSimple("style"), "animation:slideDown .2s ease")}), []any{sky_call(sky_call(sky_htmlEl("span"), []any{tw([]any{flex1})}), []any{sky_htmlText(sky_asMap(model)["notification"])}), sky_call(sky_call(sky_htmlEl("button"), []any{sky_call(sky_evtHandler("click"), DismissNotification), tw([]any{textBase, opacity50, px1, hover(opacity100)})}), []any{sky_htmlText("x")})})}) }() }()
}

func Ui_Layout_AdminSidebar(model any) any {
	return func() any { lang := sky_asMap(model)["lang"]; _ = lang; return sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{wFull, lg(w48), shrink0, pt2})}), []any{sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{px3, py2, mb2})}), []any{sky_call(sky_call(sky_htmlEl("span"), []any{tw([]any{textXs, uppercase, fontBold, textGray400})}), []any{sky_htmlText(Lib_Translation_T(lang, "nav.admin"))})}), sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{flex, flexRow, lg(flexCol), gap1, flexWrap})}), []any{Ui_Layout_AdminSidebarLink("/admin/products", "admin.products", model), Ui_Layout_AdminSidebarLink("/admin/orders", "admin.orders", model)}), sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{borderT, borderGray200, mt4, pt4})}), []any{sky_call(sky_call(sky_htmlEl("a"), []any{sky_call(sky_attrSimple("href"), "/"), sky_call(sky_attrCustom("sky-nav"), ""), Ui_Layout_SidebarLink_()}), []any{sky_htmlText("View Store")})})}) }()
}

func Ui_Layout_AdminSidebarLink(url any, key any, model any) any {
	return func() any { lang := sky_asMap(model)["lang"]; _ = lang; isActive := func() any { return func() any { __subject := sky_asMap(model)["page"]; if sky_asMap(__subject)["SkyName"] == "AdminProductsPage" { return sky_equal(url, "/admin/products") };  if sky_asMap(__subject)["SkyName"] == "AdminNewProductPage" { return sky_equal(url, "/admin/products") };  if sky_asMap(__subject)["SkyName"] == "AdminEditProductPage" { return sky_equal(url, "/admin/products") };  if sky_asMap(__subject)["SkyName"] == "AdminOrdersPage" { return sky_equal(url, "/admin/orders") };  if sky_asMap(__subject)["SkyName"] == "AdminOrderPage" { return sky_equal(url, "/admin/orders") };  if true { return false };  return nil }() }(); _ = isActive; return sky_call(sky_call(sky_htmlEl("a"), []any{sky_call(sky_attrSimple("href"), url), sky_call(sky_attrCustom("sky-nav"), ""), func() any { if sky_asBool(isActive) { return Ui_Layout_SidebarLinkActive() }; return Ui_Layout_SidebarLink_() }()}), []any{sky_htmlText(Lib_Translation_T(lang, key))}) }()
}

func Ui_Layout_ViewFooter(model any) any {
	return func() any { lang := sky_asMap(model)["lang"]; _ = lang; return sky_call(sky_call(sky_htmlEl("footer"), []any{tw([]any{bgWhite, borderT, borderGray200, SkyTuple2{V0: "margin-top", V1: "auto"}})}), []any{sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{maxW7xl, mxAuto, px4})}), []any{sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{grid, gridCols1, md(gridCols3), gap8, py12})}), []any{sky_call(sky_call(sky_htmlEl("div"), []any{}), []any{sky_call(sky_call(sky_htmlEl("h4"), []any{tw([]any{fontBold, textGray900, mb3})}), []any{sky_htmlText(Lib_Translation_T(lang, "app.name"))}), sky_call(sky_call(sky_htmlEl("p"), []any{tw([]any{textSm, textGray500, leadingRelaxed})}), []any{sky_htmlText(Lib_Translation_T(lang, "footer.tagline"))})}), sky_call(sky_call(sky_htmlEl("div"), []any{}), []any{sky_call(sky_call(sky_htmlEl("h4"), []any{tw([]any{fontBold, textGray900, mb3, textSm, uppercase})}), []any{sky_htmlText(Lib_Translation_T(lang, "footer.quickLinks"))}), sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{flex, flexCol, gap2})}), []any{sky_call(sky_call(sky_htmlEl("a"), []any{sky_call(sky_attrSimple("href"), "/products"), sky_call(sky_attrCustom("sky-nav"), ""), tw([]any{textSm, textGray500})}), []any{sky_htmlText(Lib_Translation_T(lang, "nav.products"))}), sky_call(sky_call(sky_htmlEl("a"), []any{sky_call(sky_attrSimple("href"), "/cart"), sky_call(sky_attrCustom("sky-nav"), ""), tw([]any{textSm, textGray500})}), []any{sky_htmlText(Lib_Translation_T(lang, "nav.cart"))}), sky_call(sky_call(sky_htmlEl("a"), []any{sky_call(sky_attrSimple("href"), "/orders"), sky_call(sky_attrCustom("sky-nav"), ""), tw([]any{textSm, textGray500})}), []any{sky_htmlText(Lib_Translation_T(lang, "nav.orders"))})})}), sky_call(sky_call(sky_htmlEl("div"), []any{}), []any{sky_call(sky_call(sky_htmlEl("h4"), []any{tw([]any{fontBold, textGray900, mb3, textSm, uppercase})}), []any{sky_htmlText(Lib_Translation_T(lang, "footer.legal"))}), sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{flex, flexCol, gap2})}), []any{sky_call(sky_call(sky_htmlEl("a"), []any{sky_call(sky_attrSimple("href"), "/privacy-policy"), sky_call(sky_attrCustom("sky-nav"), ""), tw([]any{textSm, textGray500})}), []any{sky_htmlText(Lib_Translation_T(lang, "footer.privacy"))}), sky_call(sky_call(sky_htmlEl("a"), []any{sky_call(sky_attrSimple("href"), "/terms"), sky_call(sky_attrCustom("sky-nav"), ""), tw([]any{textSm, textGray500})}), []any{sky_htmlText(Lib_Translation_T(lang, "footer.terms"))})})})}), sky_call(sky_call(sky_htmlEl("div"), []any{tw([]any{borderT, borderGray200, py6, textCenter})}), []any{sky_call(sky_call(sky_htmlEl("p"), []any{tw([]any{textSm, textGray400})}), []any{sky_htmlText(sky_concat("2024 ", Lib_Translation_T(lang, "footer.copyright")))})})})}) }()
}

func Ui_Layout_GlobalStyles() any {
	return sky_call(sky_htmlStyleNode([]any{}), sky_cssStylesheet([]any{sky_call(sky_cssRule("*"), []any{"margin:0", "padding:0", "box-sizing:border-box"}), sky_call(sky_cssRule("body"), []any{"font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif", "line-height:1.6", "color:#1a202c", "background:#f8fafc"}), sky_call(sky_cssRule("a"), []any{"text-decoration:none", "color:inherit"}), sky_call(sky_cssRule("img"), []any{"max-width:100%", "height:auto", "display:block"}), sky_call(sky_cssRule("button"), []any{"cursor:pointer", "border:none", "background:none", "font-family:inherit"}), sky_call(sky_cssRule("input,textarea,select"), []any{"font-family:inherit", "font-size:inherit", "outline:none"}), sky_call(sky_cssRule(".line-through"), []any{"text-decoration:line-through"}), sky_call(sky_cssRule(".product-card"), []any{"transition:all .2s ease"}), sky_call(sky_cssRule(".product-card:hover"), []any{"border-color:#cbd5e1", "box-shadow:0 4px 12px rgba(0,0,0,.08)", "transform:translateY(-2px)"}), sky_call(sky_cssRule(".product-card:hover img"), []any{"transform:scale(1.03)"}), sky_call(sky_cssRule(".product-card img"), []any{"transition:transform .3s ease"}), sky_call(sky_cssRule(".toggle"), []any{"width:44px", "height:24px", "border-radius:12px", "position:relative", "transition:background .15s ease"}), sky_call(sky_cssRule(".toggle-on"), []any{"background:#2563eb"}), sky_call(sky_cssRule(".toggle-off"), []any{"background:#d1d5db"}), sky_call(sky_cssRule(".toggle-knob"), []any{"width:20px", "height:20px", "background:#fff", "border-radius:50%", "position:absolute", "top:2px", "transition:all .15s ease", "box-shadow:0 1px 3px rgba(0,0,0,.15)"}), sky_call(sky_cssRule(".toggle-knob-on"), []any{"right:2px"}), sky_call(sky_cssRule(".toggle-knob-off"), []any{"left:2px"}), sky_call(sky_cssRule(".spinner"), []any{"width:48px", "height:48px", "border:3px solid #e2e8f0", "border-top-color:#3b82f6", "border-radius:50%", "animation:spin 1s linear infinite", "margin:0 auto 1.5rem"}), sky_call(sky_cssRule(".qty-input"), []any{"width:3.5rem", "text-align:center", "font-weight:600", "border:1px solid #d1d5db", "border-left:none", "border-right:none", "height:32px", "font-size:.875rem"}), sky_call(sky_cssKeyframes("slideDown"), []any{sky_call(sky_cssFrame("from"), []any{"opacity:0", "transform:translateY(-8px)"}), sky_call(sky_cssFrame("to"), []any{"opacity:1", "transform:translateY(0)"})}), sky_call(sky_cssKeyframes("spin"), []any{sky_call(sky_cssFrame("to"), []any{"transform:rotate(360deg)"})})}))
}