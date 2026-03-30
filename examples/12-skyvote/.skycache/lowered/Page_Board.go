func Page_Board_GetUserId(model any) any {
	return func() any { return func() any { __subject := sky_asMap(model)["currentUser"]; if sky_asSkyMaybe(__subject).SkyName == "Just" { user := sky_asSkyMaybe(__subject).JustValue; _ = user; return Lib_Db_GetField("id", user) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "" };  panic("non-exhaustive case expression") }() }()
}

func Page_Board_ViewSubmitButton(model any) any {
	return func() any { return func() any { __subject := sky_asMap(model)["currentUser"]; if sky_asSkyMaybe(__subject).SkyName == "Just" { return sky_call(sky_call(sky_htmlEl("a"), []any{sky_call(sky_attrSimple("href"), "/submit"), sky_call(sky_attrCustom("sky-nav"), ""), sky_call(sky_attrSimple("class"), "btn btn-primary")}), []any{sky_htmlText("+ Submit Idea")}) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return sky_call(sky_call(sky_htmlEl("a"), []any{sky_call(sky_attrSimple("href"), "/auth/signin"), sky_call(sky_attrCustom("sky-nav"), ""), sky_call(sky_attrSimple("class"), "btn btn-secondary")}), []any{sky_htmlText("Sign in to vote")}) };  panic("non-exhaustive case expression") }() }()
}

func Page_Board_ViewBoard(model any) any {
	return func() any { userId := Page_Board_GetUserId(model); _ = userId; return Ui_Layout_Page(model, sky_call(sky_call(sky_htmlEl("div"), []any{}), []any{sky_call(sky_call(sky_htmlEl("div"), []any{sky_call(sky_attrSimple("class"), "board-header")}), []any{sky_call(sky_call(sky_htmlEl("div"), []any{}), []any{sky_call(sky_call(sky_htmlEl("h1"), []any{sky_call(sky_attrSimple("class"), "page-title")}), []any{sky_htmlText("Feature Board")}), sky_call(sky_call(sky_htmlEl("p"), []any{sky_call(sky_attrSimple("class"), "page-subtitle")}), []any{sky_htmlText("Vote on ideas you care about. The best ones rise to the top.")})}), Page_Board_ViewSubmitButton(model)}), Page_Board_ViewToolbar(model), Page_Board_ViewIdeaList(model, userId)})) }()
}

func Page_Board_ViewIdeaList(model any, userId any) any {
	return sky_call(sky_call(sky_htmlEl("div"), []any{sky_call(sky_attrSimple("class"), "idea-list")}), func() any { if sky_asBool(sky_listIsEmpty(sky_asMap(model)["ideas"])) { return []any{emptyState("No ideas yet. Be the first to submit one!")} }; return sky_call(sky_listMap(func(idea any) any { return ideaCard(idea, userId) }), sky_asMap(model)["ideas"]) }())
}

func Page_Board_ViewToolbar(model any) any {
	return sky_call(sky_call(sky_htmlEl("div"), []any{sky_call(sky_attrSimple("class"), "toolbar")}), []any{sky_call(sky_htmlVoid("input"), []any{sky_call(sky_attrSimple("type"), "text"), sky_call(sky_attrSimple("class"), "search-input"), sky_call(sky_attrSimple("placeholder"), "Search ideas..."), sky_call(sky_attrSimple("value"), sky_asMap(model)["searchQuery"]), sky_call(sky_evtHandler("input"), SetSearch)}), sky_call(sky_call(sky_htmlEl("div"), []any{sky_call(sky_attrSimple("class"), "filter-group")}), []any{Page_Board_FilterBtn("all", "All", sky_asMap(model)["filterCategory"]), Page_Board_FilterBtn("feature", "Features", sky_asMap(model)["filterCategory"]), Page_Board_FilterBtn("bug", "Bugs", sky_asMap(model)["filterCategory"]), Page_Board_FilterBtn("improvement", "Improvements", sky_asMap(model)["filterCategory"])}), sky_call(sky_call(sky_htmlEl("div"), []any{sky_call(sky_attrSimple("class"), "filter-group")}), []any{Page_Board_SortBtn("newest", "Newest", sky_asMap(model)["sortBy"]), Page_Board_SortBtn("votes", "Top Voted", sky_asMap(model)["sortBy"]), Page_Board_SortBtn("comments", "Most Discussed", sky_asMap(model)["sortBy"])})})
}

func Page_Board_FilterBtn(filterValue any, label any, activeFilter any) any {
	return sky_call(sky_call(sky_htmlEl("button"), []any{sky_call(sky_evtHandler("click"), SetFilter(filterValue)), sky_call(sky_attrSimple("class"), func() any { if sky_asBool(sky_equal(activeFilter, filterValue)) { return "filter-btn active" }; return "filter-btn" }())}), []any{sky_htmlText(label)})
}

func Page_Board_SortBtn(sortValue any, label any, activeSort any) any {
	return sky_call(sky_call(sky_htmlEl("button"), []any{sky_call(sky_evtHandler("click"), SetSort(sortValue)), sky_call(sky_attrSimple("class"), func() any { if sky_asBool(sky_equal(activeSort, sortValue)) { return "filter-btn active" }; return "filter-btn" }())}), []any{sky_htmlText(label)})
}