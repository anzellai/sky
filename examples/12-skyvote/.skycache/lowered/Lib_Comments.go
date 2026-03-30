func Lib_Comments_CreateComment(ideaId any, authorId any, content any) any {
	return func() any { id := sky_call(sky_call(sky_stringSlice(0), 12), sky_cryptoSha256(sky_concat(ideaId, sky_concat(":", sky_concat(authorId, sky_concat(":", content)))))); _ = id; result := Lib_Db_Exec_("INSERT INTO comments (id, idea_id, author_id, content) VALUES (?, ?, ?, ?)", []any{id, ideaId, authorId, content}); _ = result; sky_println(sky_concat("[COMMENT] Added on ", sky_concat(ideaId, sky_concat(" by ", authorId)))); return result }()
}

func Lib_Comments_GetComments(ideaId any) any {
	return sky_call(sky_resultWithDefault([]any{}), Lib_Db_QueryRows("SELECT c.id, c.content, c.created_at, u.username as author_name FROM comments c LEFT JOIN users u ON c.author_id = u.id WHERE c.idea_id = ? ORDER BY c.created_at ASC", []any{ideaId}))
}