// hash:8-8
func Lib_Ideas_CreateIdea(title any, description any, category any, authorId any) any {
	return func() any { id := sky_call(sky_call(sky_stringSlice(0), 12), sky_cryptoSha256(sky_concat(title, sky_concat(":", authorId)))); _ = id; result := Lib_Db_Exec_("INSERT INTO ideas (id, title, description, category, author_id) VALUES (?, ?, ?, ?, ?)", []any{id, title, description, category, authorId}); _ = result; sky_println(sky_concat("[IDEA] Created: ", sky_concat(title, sky_concat(" by ", authorId)))); return func() any { return func() any { __subject := result; if sky_asSkyResult(__subject).SkyName == "Ok" { return SkyOk(id) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  panic("non-exhaustive case expression") }() }() }()
}

func Lib_Ideas_GetIdeas(sortBy any, filterCategory any, searchQuery any) any {
	return func() any { baseQuery := "SELECT i.id, i.title, i.description, i.category, i.status, i.author_id, i.created_at, u.username as author_name, (SELECT COUNT(*) FROM votes v WHERE v.idea_id = i.id) as vote_count, (SELECT COUNT(*) FROM comments c WHERE c.idea_id = i.id) as comment_count FROM ideas i LEFT JOIN users u ON i.author_id = u.id"; _ = baseQuery; whereClause := func() any { if sky_asBool(sky_equal(filterCategory, "all")) { return func() any { if sky_asBool(sky_equal(searchQuery, "")) { return "" }; return sky_concat(" WHERE (i.title LIKE '%", sky_concat(searchQuery, sky_concat("%' OR i.description LIKE '%", sky_concat(searchQuery, "%')")))) }() }; if sky_asBool(sky_equal(searchQuery, "")) { return sky_concat(" WHERE i.category = '", sky_concat(filterCategory, "'")) }; return sky_concat(" WHERE i.category = '", sky_concat(filterCategory, sky_concat("' AND (i.title LIKE '%", sky_concat(searchQuery, sky_concat("%' OR i.description LIKE '%", sky_concat(searchQuery, "%')")))))) }(); _ = whereClause; orderClause := func() any { if sky_asBool(sky_equal(sortBy, "votes")) { return " ORDER BY vote_count DESC, i.created_at DESC" }; if sky_asBool(sky_equal(sortBy, "comments")) { return " ORDER BY comment_count DESC, i.created_at DESC" }; return " ORDER BY i.created_at DESC" }(); _ = orderClause; fullQuery := sky_concat(baseQuery, sky_concat(whereClause, orderClause)); _ = fullQuery; return sky_call(sky_resultWithDefault([]any{}), Lib_Db_QueryRows(fullQuery, []any{})) }()
}

func Lib_Ideas_GetIdea(ideaId any) any {
	return func() any { rows := Lib_Db_QueryRows("SELECT i.id, i.title, i.description, i.category, i.status, i.author_id, i.created_at, u.username as author_name, (SELECT COUNT(*) FROM votes v WHERE v.idea_id = i.id) as vote_count, (SELECT COUNT(*) FROM comments c WHERE c.idea_id = i.id) as comment_count FROM ideas i LEFT JOIN users u ON i.author_id = u.id WHERE i.id = ? LIMIT 1", []any{ideaId}); _ = rows; ideaRows := sky_call(sky_resultWithDefault([]any{}), rows); _ = ideaRows; return sky_listHead(ideaRows) }()
}

func Lib_Ideas_GetIdeasByStatus(status any) any {
	return sky_call(sky_resultWithDefault([]any{}), Lib_Db_QueryRows("SELECT i.id, i.title, i.description, i.category, i.status, i.author_id, i.created_at, u.username as author_name, (SELECT COUNT(*) FROM votes v WHERE v.idea_id = i.id) as vote_count FROM ideas i LEFT JOIN users u ON i.author_id = u.id WHERE i.status = ? ORDER BY vote_count DESC", []any{status}))
}

func Lib_Ideas_ToggleVote(ideaId any, userId any) any {
	return func() any { existingRows := sky_call(sky_resultWithDefault([]any{}), Lib_Db_QueryRows("SELECT id FROM votes WHERE idea_id = ? AND user_id = ?", []any{ideaId, userId})); _ = existingRows; return func() any { return func() any { __subject := sky_listHead(existingRows); if sky_asSkyMaybe(__subject).SkyName == "Just" { return func() any { Lib_Db_Exec_("DELETE FROM votes WHERE idea_id = ? AND user_id = ?", []any{ideaId, userId}); sky_println(sky_concat("[VOTE] Removed: ", sky_concat(userId, sky_concat(" on ", ideaId)))); return struct{}{} }() };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return func() any { voteId := sky_call(sky_call(sky_stringSlice(0), 12), sky_cryptoSha256(sky_concat(ideaId, sky_concat(":", userId)))); _ = voteId; Lib_Db_Exec_("INSERT INTO votes (id, idea_id, user_id) VALUES (?, ?, ?)", []any{voteId, ideaId, userId}); sky_println(sky_concat("[VOTE] Added: ", sky_concat(userId, sky_concat(" on ", ideaId)))); return struct{}{} }() };  panic("non-exhaustive case expression") }() }() }()
}

func Lib_Ideas_HasVoted(ideaId any, userId any) any {
	return func() any { rows := sky_call(sky_resultWithDefault([]any{}), Lib_Db_QueryRows("SELECT id FROM votes WHERE idea_id = ? AND user_id = ?", []any{ideaId, userId})); _ = rows; return func() any { return func() any { __subject := sky_listHead(rows); if sky_asSkyMaybe(__subject).SkyName == "Just" { return true };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return false };  panic("non-exhaustive case expression") }() }() }()
}

func Lib_Ideas_GetVoteCount(ideaId any) any {
	return func() any { rows := sky_call(sky_resultWithDefault([]any{}), Lib_Db_QueryRows("SELECT COUNT(*) as cnt FROM votes WHERE idea_id = ?", []any{ideaId})); _ = rows; return func() any { return func() any { __subject := sky_listHead(rows); if sky_asSkyMaybe(__subject).SkyName == "Just" { row := sky_asSkyMaybe(__subject).JustValue; _ = row; return Lib_Db_GetField("cnt", row) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "0" };  panic("non-exhaustive case expression") }() }() }()
}

func Lib_Ideas_UpdateStatus(ideaId any, newStatus any) any {
	return func() any { Lib_Db_Exec_("UPDATE ideas SET status = ?, updated_at = datetime('now') WHERE id = ?", []any{newStatus, ideaId}); sky_println(sky_concat("[IDEA] Status changed: ", sky_concat(ideaId, sky_concat(" -> ", newStatus)))); return struct{}{} }()(0, 0)
}