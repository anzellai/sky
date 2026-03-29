func Lib_Db_Ctx() any {
	return Context_Background(struct{}{})
}

func Lib_Db_GetProjectId(_ any) any {
	return func() any { env := sky_call(sky_resultWithDefault(""), Os_Getenv("GOOGLE_CLOUD_PROJECT")); _ = env; return func() any { if sky_asBool(sky_equal(env, "")) { return "skyshop" }; return env }() }()
}

func Lib_Db_GetFirestoreClient(_ any) any {
	return Cloud_Google_Com_Go_Firestore_NewClient(Lib_Db_Ctx(), Lib_Db_GetProjectId(struct{}{}), []any{})
}

func Lib_Db_AnyToString(val any) any {
	return Fmt_Sprint([]any{val})
}

func Lib_Db_SnapshotToDict(snapshot any) any {
	return func() any { rawData := Cloud_Google_Com_Go_Firestore_DocumentSnapshotData(snapshot); _ = rawData; docId := Cloud_Google_Com_Go_Firestore_DocumentRefID(Cloud_Google_Com_Go_Firestore_DocumentSnapshotRef(snapshot)); _ = docId; return sky_call2(sky_dictInsert("id"), docId, Lib_Db_MapToStringDict(rawData)) }()
}

func Lib_Db_MapToStringDict(rawMap any) any {
	return sky_call(sky_dictMap(func(_ any) any { return func(v any) any { return Lib_Db_AnyToString(v) } }), rawMap)
}

func Lib_Db_DictToMap(dict any) any {
	return dict
}

func Lib_Db_IntVal(n any) any {
	return sky_identity(n)
}

func Lib_Db_BoolVal(b any) any {
	return sky_identity(b)
}

func Lib_Db_FloatVal(f any) any {
	return sky_identity(f)
}

func Lib_Db_GetCollection(client any, collection any) any {
	return func() any { return func() any { __subject := Cloud_Google_Com_Go_Firestore_ClientCollection(client, collection); if sky_asSkyResult(__subject).SkyName == "Ok" { colRef := sky_asSkyResult(__subject).OkValue; _ = colRef; return SkyOk(colRef) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_errorToString(e)) };  return nil }() }()
}

func Lib_Db_GetDoc(collection any, docId any) any {
	return func() any { return func() any { __subject := Lib_Db_GetFirestoreClient(struct{}{}); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_errorToString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { client := sky_asSkyResult(__subject).OkValue; _ = client; return Lib_Db_GetDocWithClient(client, collection, docId) };  return nil }() }()
}

func Lib_Db_GetDocWithClient(client any, collection any, docId any) any {
	return func() any { return func() any { __subject := Lib_Db_GetCollection(client, collection); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { colRef := sky_asSkyResult(__subject).OkValue; _ = colRef; return Lib_Db_GetDocInner(colRef, docId) };  return nil }() }()
}

func Lib_Db_GetDocInner(colRef any, docId any) any {
	return func() any { return func() any { __subject := Cloud_Google_Com_Go_Firestore_CollectionRefDoc(colRef, docId); if sky_asSkyResult(__subject).SkyName == "Err" { return SkyOk(SkyNothing()) };  if sky_asSkyResult(__subject).SkyName == "Ok" { docRef := sky_asSkyResult(__subject).OkValue; _ = docRef; return Lib_Db_GetDocRef(docRef) };  return nil }() }()
}

func Lib_Db_GetDocRef(docRef any) any {
	return func() any { return func() any { __subject := Cloud_Google_Com_Go_Firestore_DocumentRefGet(docRef, Lib_Db_Ctx()); if sky_asSkyResult(__subject).SkyName == "Ok" { snapshot := sky_asSkyResult(__subject).OkValue; _ = snapshot; return Lib_Db_GetDocSnapshot(snapshot) };  if sky_asSkyResult(__subject).SkyName == "Err" { return SkyOk(SkyNothing()) };  return nil }() }()
}

func Lib_Db_GetDocSnapshot(snapshot any) any {
	return func() any { return func() any { __subject := Cloud_Google_Com_Go_Firestore_DocumentSnapshotExists(snapshot); if sky_asSkyResult(__subject).SkyName == "Ok" && sky_asBool(sky_asSkyResult(__subject).OkValue) == true { return SkyOk(SkyJust(Lib_Db_SnapshotToDict(snapshot))) };  if true { return SkyOk(SkyNothing()) };  return nil }() }()
}

func Lib_Db_MergeOpts() any {
	return []any{Cloud_Google_Com_Go_Firestore_MergeAll(struct{}{})}
}

func Lib_Db_SetDoc(collection any, docId any, data any) any {
	return func() any { return func() any { __subject := Lib_Db_GetFirestoreClient(struct{}{}); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_errorToString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { client := sky_asSkyResult(__subject).OkValue; _ = client; return Lib_Db_SetDocWithClient(client, collection, docId, data) };  return nil }() }()
}

func Lib_Db_SetDocWithClient(client any, collection any, docId any, data any) any {
	return func() any { return func() any { __subject := Lib_Db_GetCollection(client, collection); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { colRef := sky_asSkyResult(__subject).OkValue; _ = colRef; return Lib_Db_SetDocInner(colRef, docId, data) };  return nil }() }()
}

func Lib_Db_SetDocInner(colRef any, docId any, data any) any {
	return func() any { return func() any { __subject := Cloud_Google_Com_Go_Firestore_CollectionRefDoc(colRef, docId); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_errorToString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { docRef := sky_asSkyResult(__subject).OkValue; _ = docRef; return Lib_Db_SetDocRef(docRef, data) };  return nil }() }()
}

func Lib_Db_SetDocRef(docRef any, data any) any {
	return func() any { goMap := Lib_Db_DictToMap(data); _ = goMap; return func() any { return func() any { __subject := Cloud_Google_Com_Go_Firestore_DocumentRefSet(docRef, Lib_Db_Ctx(), goMap, Lib_Db_MergeOpts()); if sky_asSkyResult(__subject).SkyName == "Ok" { return SkyOk(struct{}{}) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_errorToString(e)) };  return nil }() }() }()
}

func Lib_Db_QueryDocs(collection any) any {
	return func() any { return func() any { __subject := Lib_Db_GetFirestoreClient(struct{}{}); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_errorToString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { client := sky_asSkyResult(__subject).OkValue; _ = client; return Lib_Db_QueryDocsWithClient(client, collection) };  return nil }() }()
}

func Lib_Db_QueryDocsWithClient(client any, collection any) any {
	return func() any { return func() any { __subject := Lib_Db_GetCollection(client, collection); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { colRef := sky_asSkyResult(__subject).OkValue; _ = colRef; return Lib_Db_QueryDocsInner(colRef) };  return nil }() }()
}

func Lib_Db_QueryDocsInner(colRef any) any {
	return func() any { return func() any { __subject := Cloud_Google_Com_Go_Firestore_CollectionRefDocuments(colRef, Lib_Db_Ctx()); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_errorToString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { iter := sky_asSkyResult(__subject).OkValue; _ = iter; return Lib_Db_GetAllSnapshots(iter) };  return nil }() }()
}

func Lib_Db_QueryWhere(collection any, field any, op any, value any) any {
	return func() any { return func() any { __subject := Lib_Db_GetFirestoreClient(struct{}{}); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_errorToString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { client := sky_asSkyResult(__subject).OkValue; _ = client; return Lib_Db_QueryWhereWithClient(client, collection, field, op, value) };  return nil }() }()
}

func Lib_Db_QueryWhereWithClient(client any, collection any, field any, op any, value any) any {
	return func() any { return func() any { __subject := Lib_Db_GetCollection(client, collection); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { colRef := sky_asSkyResult(__subject).OkValue; _ = colRef; return Lib_Db_QueryWhereInner(colRef, field, op, value) };  return nil }() }()
}

func Lib_Db_QueryWhereInner(colRef any, field any, op any, value any) any {
	return func() any { return func() any { __subject := Cloud_Google_Com_Go_Firestore_CollectionRefWhere(colRef, field, op, value); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_errorToString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { q := sky_asSkyResult(__subject).OkValue; _ = q; return Lib_Db_RunQuery(q) };  return nil }() }()
}

func Lib_Db_QueryWhereOrder(collection any, field any, op any, value any, orderField any, dir any) any {
	return func() any { return func() any { __subject := Lib_Db_GetFirestoreClient(struct{}{}); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_errorToString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { client := sky_asSkyResult(__subject).OkValue; _ = client; return Lib_Db_QueryWhereOrderWithClient(client, collection, field, op, value, orderField, dir) };  return nil }() }()
}

func Lib_Db_QueryWhereOrderWithClient(client any, collection any, field any, op any, value any, orderField any, dir any) any {
	return func() any { return func() any { __subject := Lib_Db_GetCollection(client, collection); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { colRef := sky_asSkyResult(__subject).OkValue; _ = colRef; return Lib_Db_QueryWhereOrderInner(colRef, field, op, value, orderField, dir) };  return nil }() }()
}

func Lib_Db_QueryWhereOrderInner(colRef any, field any, op any, value any, orderField any, dir any) any {
	return func() any { return func() any { __subject := Cloud_Google_Com_Go_Firestore_CollectionRefWhere(colRef, field, op, value); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_errorToString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { q := sky_asSkyResult(__subject).OkValue; _ = q; return Lib_Db_QueryWhereOrderQuery(q, orderField, dir) };  return nil }() }()
}

func Lib_Db_QueryWhereOrderQuery(q any, orderField any, dir any) any {
	return func() any { direction := func() any { if sky_asBool(sky_equal(dir, "desc")) { return Cloud_Google_Com_Go_Firestore_Desc() }; return Cloud_Google_Com_Go_Firestore_Asc() }(); _ = direction; return func() any { return func() any { __subject := Cloud_Google_Com_Go_Firestore_QueryOrderBy(q, orderField, direction); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_errorToString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { ordered := sky_asSkyResult(__subject).OkValue; _ = ordered; return Lib_Db_RunQuery(ordered) };  return nil }() }() }()
}

func Lib_Db_DeleteDoc(collection any, docId any) any {
	return func() any { return func() any { __subject := Lib_Db_GetFirestoreClient(struct{}{}); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_errorToString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { client := sky_asSkyResult(__subject).OkValue; _ = client; return Lib_Db_DeleteDocWithClient(client, collection, docId) };  return nil }() }()
}

func Lib_Db_DeleteDocWithClient(client any, collection any, docId any) any {
	return func() any { return func() any { __subject := Lib_Db_GetCollection(client, collection); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  if sky_asSkyResult(__subject).SkyName == "Ok" { colRef := sky_asSkyResult(__subject).OkValue; _ = colRef; return Lib_Db_DeleteDocInner(colRef, docId) };  return nil }() }()
}

func Lib_Db_DeleteDocInner(colRef any, docId any) any {
	return func() any { return func() any { __subject := Cloud_Google_Com_Go_Firestore_CollectionRefDoc(colRef, docId); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_errorToString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { docRef := sky_asSkyResult(__subject).OkValue; _ = docRef; return Lib_Db_DeleteDocRef(docRef) };  return nil }() }()
}

func Lib_Db_DeleteDocRef(docRef any) any {
	return func() any { return func() any { __subject := Cloud_Google_Com_Go_Firestore_DocumentRefDelete(docRef, Lib_Db_Ctx(), []any{}); if sky_asSkyResult(__subject).SkyName == "Ok" { return SkyOk(struct{}{}) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_errorToString(e)) };  return nil }() }()
}

func Lib_Db_RunQuery(q any) any {
	return func() any { return func() any { __subject := Cloud_Google_Com_Go_Firestore_QueryDocuments(q, Lib_Db_Ctx()); if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_errorToString(e)) };  if sky_asSkyResult(__subject).SkyName == "Ok" { iter := sky_asSkyResult(__subject).OkValue; _ = iter; return Lib_Db_GetAllSnapshots(iter) };  return nil }() }()
}

func Lib_Db_GetAllSnapshots(iter any) any {
	return func() any { return func() any { __subject := Cloud_Google_Com_Go_Firestore_DocumentIteratorGetAll(iter); if sky_asSkyResult(__subject).SkyName == "Ok" { snapshots := sky_asSkyResult(__subject).OkValue; _ = snapshots; return SkyOk(sky_call(sky_listMap(Lib_Db_SnapshotToDict), snapshots)) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(sky_errorToString(e)) };  return nil }() }()
}

func Lib_Db_GetField(field any, row any) any {
	return sky_call(sky_maybeWithDefault(""), sky_call(sky_dictGet(field), row))
}

func Lib_Db_GetInt(field any, row any) any {
	return func() any { val := Lib_Db_GetField(field, row); _ = val; return func() any { if sky_asBool(sky_equal(val, "")) { return 0 }; return sky_call(sky_resultWithDefault(0), sky_call(sky_stringToInt, val)) }() }()
}

func Lib_Db_GetBool(field any, row any) any {
	return func() any { val := Lib_Db_GetField(field, row); _ = val; return sky_asBool(sky_equal(val, "1")) || sky_asBool(sky_equal(val, "true")) }()
}

func Lib_Db_InitDb() any {
	return func() any { return func() any { __subject := Lib_Db_GetFirestoreClient(struct{}{}); if sky_asSkyResult(__subject).SkyName == "Ok" { return func() any { sky_println("[DB] Firestore client initialised"); return struct{}{} }() };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { sky_println(sky_concat("[DB] ERROR: Failed to initialise Firestore: ", sky_errorToString(e))); return struct{}{} }() };  return nil }() }()
}