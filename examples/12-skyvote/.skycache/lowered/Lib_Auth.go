// hash:5-10
func Lib_Auth_HashPassword(password any) any {
	return func() any { salted := sky_concat("skyvote-salt-v1:", password); _ = salted; hash := sky_call(sky_resultWithDefault(""), Crypto_Sha256_Sum256(sky_stringToBytes(salted))); _ = hash; return sky_call(sky_resultWithDefault(""), Encoding_Hex_EncodeToString(hash)) }()
}

func Lib_Auth_VerifyPassword(password any, storedHash any) any {
	return sky_equal(Lib_Auth_HashPassword(password), storedHash)
}

func Lib_Auth_CreateUser(username any, email any, password any) any {
	return func() any { id := sky_call(sky_call(sky_stringSlice(0), 12), sky_cryptoSha256(sky_concat(username, sky_concat(":", email)))); _ = id; passHash := Lib_Auth_HashPassword(password); _ = passHash; result := Lib_Db_Exec_("INSERT INTO users (id, username, email, password_hash) VALUES (?, ?, ?, ?)", []any{id, username, email, passHash}); _ = result; return func() any { return func() any { __subject := result; if sky_asSkyResult(__subject).SkyName == "Ok" { return func() any { sky_println(sky_concat("[AUTH] User created: ", sky_concat(username, sky_concat(" (", sky_concat(email, ")"))))); return SkyOk(id) }() };  if sky_asSkyResult(__subject).SkyName == "Err" { e := sky_asSkyResult(__subject).ErrValue; _ = e; return SkyErr("Username or email already taken.") };  panic("non-exhaustive case expression") }() }() }()
}

func Lib_Auth_AuthenticateUser(email any, password any) any {
	return func() any { rows := Lib_Db_QueryRows("SELECT id, username, email, password_hash FROM users WHERE email = ? LIMIT 1", []any{email}); _ = rows; userRows := sky_call(sky_resultWithDefault([]any{}), rows); _ = userRows; maybeUser := sky_listHead(userRows); _ = maybeUser; return func() any { return func() any { __subject := maybeUser; if sky_asSkyMaybe(__subject).SkyName == "Just" { user := sky_asSkyMaybe(__subject).JustValue; _ = user; return func() any { storedHash := Lib_Db_GetField("password_hash", user); _ = storedHash; return func() any { if sky_asBool(Lib_Auth_VerifyPassword(password, storedHash)) { return func() any { sky_println(sky_concat("[AUTH] Sign-in success: ", email)); return SkyOk(user) }() }; return func() any { sky_println(sky_concat("[AUTH] Sign-in failed (bad password): ", email)); return SkyErr("Invalid email or password.") }() }() }() };  if sky_asSkyMaybe(__subject).SkyName == "Nothing" { return func() any { sky_println(sky_concat("[AUTH] Sign-in failed (not found): ", email)); return SkyErr("Invalid email or password.") }() };  panic("non-exhaustive case expression") }() }() }()
}

func Lib_Auth_GetUserById(userId any) any {
	return func() any { rows := Lib_Db_QueryRows("SELECT id, username, email FROM users WHERE id = ? LIMIT 1", []any{userId}); _ = rows; userRows := sky_call(sky_resultWithDefault([]any{}), rows); _ = userRows; return sky_listHead(userRows) }()
}