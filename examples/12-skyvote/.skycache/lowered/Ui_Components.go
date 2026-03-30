// hash:6-7
func Ui_Components_Badge(label any, badgeClass any) any {
	return sky_call(sky_call(sky_htmlEl("span"), []any{sky_call(sky_attrSimple("class"), sky_concat("badge ", badgeClass))}), []any{sky_htmlText(label)})
}

func Ui_Components_StatusBadge(status any) any {
	return func() any { if sky_asBool(sky_equal(status, "open")) { return Ui_Components_Badge("Open", "badge-open") }; if sky_asBool(sky_equal(status, "planned")) { return Ui_Components_Badge("Planned", "badge-planned") }; if sky_asBool(sky_equal(status, "in-progress")) { return Ui_Components_Badge("In Progress", "badge-in-progress") }; if sky_asBool(sky_equal(status, "done")) { return Ui_Components_Badge("Done", "badge-done") }; if sky_asBool(sky_equal(status, "declined")) { return Ui_Components_Badge("Declined", "badge-declined") }; return Ui_Components_Badge(status, "badge-open") }()
}

func Ui_Components_CategoryBadge(category any) any {
	return func() any { if sky_asBool(sky_equal(category, "bug")) { return Ui_Components_Badge("Bug", "badge-bug") }; if sky_asBool(sky_equal(category, "improvement")) { return Ui_Components_Badge("Improvement", "badge-improvement") }; return Ui_Components_Badge("Feature", "badge-feature") }()
}

func Ui_Components_StatCard(label any, value any) any {
	return sky_call(sky_call(sky_htmlEl("div"), []any{sky_call(sky_attrSimple("class"), "stat-card")}), []any{sky_call(sky_call(sky_htmlEl("div"), []any{sky_call(sky_attrSimple("class"), "stat-value")}), []any{sky_htmlText(value)}), sky_call(sky_call(sky_htmlEl("div"), []any{sky_call(sky_attrSimple("class"), "stat-label")}), []any{sky_htmlText(label)})})
}

func Ui_Components_EmptyState(message any) any {
	return sky_call(sky_call(sky_htmlEl("div"), []any{sky_call(sky_attrSimple("class"), "empty-state")}), []any{sky_call(sky_call(sky_htmlEl("p"), []any{}), []any{sky_htmlText(message)})})
}

func Ui_Components_IdeaCard(idea any, userId any) any {
	return func() any { ideaId := Lib_Db_GetField("id", idea); _ = ideaId; voteCount := Lib_Db_GetField("vote_count", idea); _ = voteCount; commentCount := Lib_Db_GetField("comment_count", idea); _ = commentCount; category := Lib_Db_GetField("category", idea); _ = category; status := Lib_Db_GetField("status", idea); _ = status; authorName := Lib_Db_GetField("author_name", idea); _ = authorName; isVoted := func() any { if sky_asBool(sky_equal(userId, "")) { return false }; return Lib_Ideas_HasVoted(ideaId, userId) }(); _ = isVoted; return sky_call(sky_call(sky_htmlEl("div"), []any{sky_call(sky_attrSimple("class"), "idea-card")}), []any{sky_call(sky_call(sky_htmlEl("div"), []any{}), []any{sky_call(sky_call(sky_htmlEl("button"), []any{sky_call(sky_evtHandler("click"), ToggleVote(ideaId)), sky_call(sky_attrSimple("class"), "vote-btn")}), []any{sky_call(sky_call(sky_htmlEl("span"), []any{sky_call(sky_attrSimple("class"), "vote-count")}), []any{sky_htmlText(voteCount)}), sky_call(sky_call(sky_htmlEl("span"), []any{sky_call(sky_attrSimple("class"), "vote-label")}), []any{sky_htmlText("votes")})})}), sky_call(sky_call(sky_htmlEl("div"), []any{sky_call(sky_attrSimple("class"), "idea-content")}), []any{sky_call(sky_call(sky_htmlEl("div"), []any{}), []any{sky_call(sky_call(sky_htmlEl("button"), []any{sky_call(sky_evtHandler("click"), ViewIdea(ideaId)), sky_call(sky_attrSimple("class"), "idea-title-btn")}), []any{sky_htmlText(Lib_Db_GetField("title", idea))})}), sky_call(sky_call(sky_htmlEl("p"), []any{sky_call(sky_attrSimple("class"), "idea-desc")}), []any{sky_htmlText(Lib_Db_GetField("description", idea))}), sky_call(sky_call(sky_htmlEl("div"), []any{sky_call(sky_attrSimple("class"), "idea-meta")}), []any{Ui_Components_CategoryBadge(category), Ui_Components_StatusBadge(status), sky_call(sky_call(sky_htmlEl("span"), []any{}), []any{sky_htmlText(sky_concat("by ", authorName))}), sky_call(sky_call(sky_htmlEl("span"), []any{}), []any{sky_htmlText(sky_concat(commentCount, " comments"))})})})}) }()
}