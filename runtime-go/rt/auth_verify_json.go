//go:build !js

package rt

// Auth_verifyTokenT is the typed kernel entry point (doc 14 §5.3) behind
// `Std.Auth.verifyToken`:
//
//	verifyToken : Secret -> String -> Result Error Json.Value
//
// It verifies exactly as Auth_verifyToken (same HMAC check, same errors) and
// hands the claims back as a Json.Value, which the caller reads with a
// `Json.Decode` decoder (`Decode.decodeValue (Decode.field "sub" Decode.string)`).
//
// WHY a Value and not a type variable. The signature used to be
// `Result Error a`: a trusted kernel with a result variable no parameter fixes
// is an unchecked cast. The claims map came back typed as a `Dict String
// String`, a record or an `Int`, whichever the call site asked for, and a wrong
// guess failed later as a run-time narrowing (the CAST audit finding, v0.27.0).
// A Value is the honest type of decoded JSON: every read is a decoder, and a
// wrong shape is an `Err`, not a panic.
//
// Auth_verifyToken keeps returning the raw claims map for the runtime's own
// callers (sliding auth, Spa session verification), which read claims in Go.
func Auth_verifyTokenT(secret any, token any) any {
	res := Auth_verifyToken(secret, token)
	r, ok := res.(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		return res
	}
	return Ok[any, any](JsonValue{raw: r.OkValue})
}
