func Lib_Auth_Ctx() any {
	return Context_Background(struct{}{})
}

func Lib_Auth_AdminEmails() any {
	return func() any { envAdmins := sky_call(sky_resultWithDefault(""), Os_Getenv("ADMIN_EMAILS")); _ = envAdmins; return func() any { if sky_asBool(sky_equal(envAdmins, "")) { return []any{"admin@example.com"} }; return sky_call(sky_stringSplit(","), envAdmins) }() }()
}

func Lib_Auth_IsAdmin(user any) any {
	return Lib_Db_GetBool("is_admin", user)
}

func Lib_Auth_GetAuthClient(_ any) any {
	return func() any { credFile := sky_call(sky_resultWithDefault(""), Os_Getenv("GOOGLE_APPLICATION_CREDENTIALS")); _ = credFile; opts := func() any { if sky_asBool(sky_equal(credFile, "")) { return []any{} }; return []any{Google_Golang_Org_Api_Option_WithCredentialsFile(credFile)} }(); _ = opts; appResult := Firebase_Google_Com_Go_V4_NewApp(Lib_Auth_Ctx(), sky_js("nil"), opts); _ = appResult; return func() any { return func() any { __subject := appResult; if sky_asSkyResult(__subject).SkyName == "Ok" { app_ := sky_asSkyResult(__subject).OkValue; _ = app_; return Firebase_Google_Com_Go_V4_AppAuth(app_, Lib_Auth_Ctx()) };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  return nil }() }() }()
}

func Lib_Auth_ClaimString(key any, claims any) any {
	return func() any { return func() any { __subject := sky_call(sky_dictGet(key), claims); if sky_asSkyMaybe(__subject).SkyName == "Just" { val := sky_asSkyMaybe(__subject).JustValue; _ = val; return Fmt_Sprint([]any{val}) };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return "" };  return nil }() }()
}

func Lib_Auth_VerifyToken(idToken any) any {
	return func() any { return func() any { __subject := Lib_Auth_GetAuthClient(struct{}{}); if sky_asSkyResult(__subject).SkyName == "Ok" { client := sky_asSkyResult(__subject).OkValue; _ = client; return func() any { return func() any { __subject := Firebase_Google_Com_Go_V4_Auth_ClientVerifyIDToken(client, Lib_Auth_Ctx(), idToken); if sky_asSkyResult(__subject).SkyName == "Ok" { token := sky_asSkyResult(__subject).OkValue; _ = token; return func() any { uid := Firebase_Google_Com_Go_V4_Auth_TokenUID(token); _ = uid; claims := Firebase_Google_Com_Go_V4_Auth_TokenClaims(token); _ = claims; email := Lib_Auth_ClaimString("email", claims); _ = email; name := Lib_Auth_ClaimString("name", claims); _ = name; sky_println(sky_concat("[AUTH] Firebase token verified for: ", sky_concat(email, sky_concat(" (uid: ", sky_concat(uid, ")"))))); return Lib_Auth_FindOrCreateUser(uid, email, name) }() };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { sky_println(sky_concat("[AUTH] Firebase token verification failed: ", sky_errorToString(e))); return SkyErr("Authentication failed. Please try again.") }() };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return func() any { sky_println(sky_concat("[AUTH] Firebase auth client error: ", sky_errorToString(e))); return SkyErr("Authentication service unavailable.") }() };  return nil }() }() };  return nil }() }()
}

func Lib_Auth_FindOrCreateUser(uid any, email any, name any) any {
	return func() any { isAdminUser := sky_call(sky_listMember(email), Lib_Auth_AdminEmails()); _ = isAdminUser; adminStr := func() any { if sky_asBool(isAdminUser) { return "true" }; return "false" }(); _ = adminStr; upsertData := sky_dictFromList([]any{SkyTuple2{V0: "id", V1: uid}, SkyTuple2{V0: "firebase_uid", V1: uid}, SkyTuple2{V0: "email", V1: email}, SkyTuple2{V0: "name", V1: name}, SkyTuple2{V0: "is_admin", V1: Lib_Db_BoolVal(isAdminUser)}}); _ = upsertData; Lib_Db_SetDoc("users", uid, upsertData); result := Lib_Db_GetDoc("users", uid); _ = result; return func() any { return func() any { __subject := result; if sky_asSkyResult(__subject).SkyName == "Ok" { maybeUser := sky_asSkyResult(__subject).OkValue; _ = maybeUser; return func() any { return func() any { __subject := maybeUser; if sky_asSkyMaybe(__subject).SkyName == "Just" { user := sky_asSkyMaybe(__subject).JustValue; _ = user; return func() any { if sky_asBool(Lib_Db_GetBool("suspended", user)) { return SkyErr("Account suspended. Please contact support.") }; return func() any { sky_println(sky_concat("[AUTH] Signed in: ", sky_concat(email, sky_concat(" (admin: ", sky_concat(adminStr, ")"))))); return SkyOk(user) }() }() };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyErr("Failed to load user profile.") };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr(e) };  return nil }() }() };  return nil }() }() }()
}

func Lib_Auth_SignOut() any {
	return func() any { sky_println("[AUTH] User signed out"); return struct{}{} }()
}

func Lib_Auth_GetUserById(userId any) any {
	return Lib_Db_GetDoc("users", userId)
}

func Lib_Auth_GetSessionUser(userId any) any {
	return func() any { return func() any { __subject := Lib_Auth_GetUserById(userId); if sky_asSkyResult(__subject).SkyName == "Ok" { maybeUser := sky_asSkyResult(__subject).OkValue; _ = maybeUser; return maybeUser };  if sky_asSkyResult(__subject).SkyName == "Err" { return SkyNothing() };  return nil }() }()
}