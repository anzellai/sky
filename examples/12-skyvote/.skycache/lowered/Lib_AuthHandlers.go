// hash:6-10
func Lib_AuthHandlers_HandleSignInSuccess(user any, model any) any {
	return func() any { ideas := sky_call(sky_resultWithDefault([]any{}), Lib_Ideas_GetIdeas("newest", "all", "")); _ = ideas; sky_println(sky_concat("[AUTH] Sign-in: ", Lib_Db_GetField("email", user))); return SkyTuple2{V0: sky_recordUpdate(model, map[string]any{"currentUser": SkyJust(user), "ideas": ideas, "page": BoardPage, "authEmail": "", "authPassword": "", "authError": "", "notification": "Welcome back!"}), V1: sky_cmdNone()} }()
}

func Lib_AuthHandlers_HandleSignIn(model any) any {
	return func() any { return func() any { __subject := Lib_Auth_AuthenticateUser(sky_asMap(model)["authEmail"], sky_asMap(model)["authPassword"]); if sky_asSkyResult(__subject).SkyName == "Ok" { user := sky_asSkyResult(__subject).OkValue; _ = user; return Lib_AuthHandlers_HandleSignInSuccess(user, model) };  if sky_asSkyResult(__subject).SkyName == "Err" { errMsg := sky_asSkyResult(__subject).ErrValue; _ = errMsg; return SkyTuple2{V0: sky_recordUpdate(model, map[string]any{"authError": errMsg}), V1: sky_cmdNone()} };  panic("non-exhaustive case expression") }() }()
}

func Lib_AuthHandlers_HandleSignUpSuccess(userId any, model any) any {
	return func() any { return func() any { __subject := Lib_Auth_GetUserById(userId); if sky_asSkyMaybe(__subject).SkyName == "Just" { user := sky_asSkyMaybe(__subject).JustValue; _ = user; return func() any { ideas := sky_call(sky_resultWithDefault([]any{}), Lib_Ideas_GetIdeas("newest", "all", "")); _ = ideas; sky_println(sky_concat("[AUTH] Sign-up success: ", userId)); return SkyTuple2{V0: sky_recordUpdate(model, map[string]any{"currentUser": SkyJust(user), "ideas": ideas, "page": BoardPage, "authEmail": "", "authUsername": "", "authPassword": "", "authError": "", "notification": "Account created! Welcome to SkyVote!"}), V1: sky_cmdNone()} }() };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return SkyTuple2{V0: sky_recordUpdate(model, map[string]any{"authError": "Account created but login failed. Please sign in.", "page": SignInPage}), V1: sky_cmdNone()} };  panic("non-exhaustive case expression") }() }()
}

func Lib_AuthHandlers_HandleSignUpValidated(model any) any {
	return func() any { return func() any { __subject := Lib_Auth_CreateUser(sky_asMap(model)["authUsername"], sky_asMap(model)["authEmail"], sky_asMap(model)["authPassword"]); if sky_asSkyResult(__subject).SkyName == "Ok" { userId := sky_asSkyResult(__subject).OkValue; _ = userId; return Lib_AuthHandlers_HandleSignUpSuccess(userId, model) };  if sky_asSkyResult(__subject).SkyName == "Err" { errMsg := sky_asSkyResult(__subject).ErrValue; _ = errMsg; return SkyTuple2{V0: sky_recordUpdate(model, map[string]any{"authError": errMsg}), V1: sky_cmdNone()} };  panic("non-exhaustive case expression") }() }()
}

func Lib_AuthHandlers_HandleSignUp(model any) any {
	return func() any { if sky_asBool(sky_equal(sky_asMap(model)["authEmail"], "")) { return SkyTuple2{V0: sky_recordUpdate(model, map[string]any{"authError": "Email is required."}), V1: sky_cmdNone()} }; if sky_asBool(sky_equal(sky_asMap(model)["authUsername"], "")) { return SkyTuple2{V0: sky_recordUpdate(model, map[string]any{"authError": "Username is required."}), V1: sky_cmdNone()} }; if sky_asBool(sky_numCompare("<", sky_stringLength(sky_asMap(model)["authPassword"]), 8)) { return SkyTuple2{V0: sky_recordUpdate(model, map[string]any{"authError": "Password must be at least 8 characters."}), V1: sky_cmdNone()} }; return Lib_AuthHandlers_HandleSignUpValidated(model) }()
}

func Lib_AuthHandlers_HandleSignOut(model any) any {
	return func() any { sky_println("[AUTH] User signed out"); return SkyTuple2{V0: sky_recordUpdate(model, map[string]any{"currentUser": SkyNothing(), "page": BoardPage, "notification": "", "authError": ""}), V1: sky_cmdNone()} }()
}