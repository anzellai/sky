// hash:2-3
func Lib_Todo_ShowStatus(done any) any {
	return func() any { if sky_asBool(sky_equal(done, "1")) { return "[x]" }; return "[ ]" }()
}

func Lib_Todo_FormatTodo(row any) any {
	return func() any { todoId := Lib_Db_GetField("id", row); _ = todoId; todoName := Lib_Db_GetField("title", row); _ = todoName; todoDone := Lib_Db_GetField("done", row); _ = todoDone; return sky_identity(sky_concat("  ", sky_concat(todoId, sky_concat(". ", sky_concat(Lib_Todo_ShowStatus(todoDone), sky_concat(" ", todoName)))))) }()
}