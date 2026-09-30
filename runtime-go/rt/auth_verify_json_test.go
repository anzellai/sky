package rt

import "testing"

// The Sky surface of verifyToken returns the claims as a Json.Value that the
// Json.Decode decoders read, and keeps every Err of the underlying verify.
func TestAuthVerifyTokenT_ClaimsAreAJsonValue(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef-extra"
	signed := Auth_signToken(secret, map[string]any{"sub": "alice", "n": 7}, 3600).(SkyResult[any, any])
	if signed.Tag != 0 {
		t.Fatalf("sign failed: %v", signed.ErrValue)
	}
	res := Auth_verifyTokenT(secret, signed.OkValue).(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("verify failed: %v", res.ErrValue)
	}
	v, ok := res.OkValue.(JsonValue)
	if !ok {
		t.Fatalf("claims must be a Json.Value, got %T", res.OkValue)
	}
	sub := JsonDec_decodeValue(JsonDec_field("sub", JsonDec_string()), v).(SkyResult[any, any])
	if sub.Tag != 0 || sub.OkValue != "alice" {
		t.Fatalf(`field "sub" must decode to "alice": %#v`, sub)
	}
	n := JsonDec_decodeValue(JsonDec_field("n", JsonDec_int()), v).(SkyResult[any, any])
	if n.Tag != 0 || AsInt(n.OkValue) != 7 {
		t.Fatalf(`field "n" must decode to 7: %#v`, n)
	}
	// A wrong guess about the claims is an Err from the decoder, not a panic.
	bad := JsonDec_decodeValue(JsonDec_field("sub", JsonDec_int()), v).(SkyResult[any, any])
	if bad.Tag != 1 {
		t.Fatalf("decoding a string claim as Int must be Err: %#v", bad)
	}
}

func TestAuthVerifyTokenT_KeepsVerifyErrors(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef-extra"
	signed := Auth_signToken(secret, map[string]any{"sub": "alice"}, 3600).(SkyResult[any, any])
	other := "ffffffffffffffffffffffffffffffff-nope"
	res := Auth_verifyTokenT(other, signed.OkValue).(SkyResult[any, any])
	if res.Tag != 1 {
		t.Fatalf("a token signed with another secret must be Err: %#v", res)
	}
	garbage := Auth_verifyTokenT(secret, "not-a-token").(SkyResult[any, any])
	if garbage.Tag != 1 {
		t.Fatalf("garbage must be Err: %#v", garbage)
	}
}
