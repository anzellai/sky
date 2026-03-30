// hash:5-6
func Lib_Db_DbRef() any {
	return sky_refNew(Database_Sql_Open("sqlite", "skyvote.db"))
}

func Lib_Db_GetConn(_ any) any {
	return sky_refGet(Lib_Db_DbRef())
}

func Lib_Db_Exec_(query any, args any) any {
	return func() any { return func() any { __subject := Lib_Db_GetConn(struct{}{}); if sky_asSkyResult(__subject).SkyName == "Ok" { conn := sky_asSkyResult(__subject).OkValue; _ = conn; return Database_Sql_DBExec(conn, query, args) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  panic("non-exhaustive case expression") }() }()
}

func Lib_Db_QueryRows(query any, args any) any {
	return func() any { return func() any { __subject := Lib_Db_GetConn(struct{}{}); if sky_asSkyResult(__subject).SkyName == "Ok" { conn := sky_asSkyResult(__subject).OkValue; _ = conn; return Database_Sql_QueryToMaps(conn, query, args) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  panic("non-exhaustive case expression") }() }()
}

func Lib_Db_GetField(field any, row any) any {
	return sky_call(sky_maybeWithDefault(""), sky_call(sky_dictGet(field), row))
}