package rt

// Audit P1-4: Auth.signToken / Auth.verifyToken previously accepted
// `secret any` and did `fmt.Sprintf("%v", secret)` to coerce. A
// caller who passed a nil, Maybe, or Dict produced a wrong but
// deterministic secret ("<nil>", "map[…:…]") that signed and
// verified against itself — the bug was silent unless someone
// audited the emitted tokens. P1-4 locks the secret as String
// with a ≥32-byte minimum.

import (
	"testing"
)

const goodSecret = "0123456789abcdef0123456789abcdef" // 32 bytes

func TestSignToken_RejectsNonStringSecret(t *testing.T) {
	res := Auth_signToken(42, map[string]any{}, 3600)
	sr := res.(SkyResult[any, any])
	if sr.Tag != 1 {
		t.Fatal("signToken must reject non-string secret")
	}
}

func TestSignToken_RejectsShortSecret(t *testing.T) {
	res := Auth_signToken("short-secret", map[string]any{}, 3600)
	sr := res.(SkyResult[any, any])
	if sr.Tag != 1 {
		t.Fatal("signToken must reject secret shorter than 32 bytes")
	}
}

func TestSignToken_AcceptsProperSecret(t *testing.T) {
	res := Auth_signToken(goodSecret, map[string]any{"sub": "u1"}, 3600)
	sr := res.(SkyResult[any, any])
	if sr.Tag != 0 {
		t.Fatalf("signToken should accept 32-byte secret, got Err: %v", sr.ErrValue)
	}
	if _, ok := sr.OkValue.(string); !ok {
		t.Fatalf("signToken should return a string, got %T", sr.OkValue)
	}
}

func TestVerifyToken_RejectsNonStringSecret(t *testing.T) {
	// Sign with a good secret, then try to verify with a non-string.
	signed := Auth_signToken(goodSecret, map[string]any{"sub": "u1"}, 3600).(SkyResult[any, any]).OkValue.(string)
	res := Auth_verifyToken(nil, signed)
	sr := res.(SkyResult[any, any])
	if sr.Tag != 1 {
		t.Fatal("verifyToken must reject non-string (nil) secret")
	}
}

func TestVerifyToken_RejectsShortSecret(t *testing.T) {
	// A too-short secret also used to sign fine (via %v). Verify
	// should reject at the secret layer before JWT parsing.
	res := Auth_verifyToken("short", "any.token.string")
	sr := res.(SkyResult[any, any])
	if sr.Tag != 1 {
		t.Fatal("verifyToken must reject secret < 32 bytes")
	}
}

func TestSignVerify_RoundTrip(t *testing.T) {
	signed := Auth_signToken(goodSecret, map[string]any{"sub": "alice"}, 3600).(SkyResult[any, any])
	if signed.Tag != 0 {
		t.Fatalf("sign failed: %v", signed.ErrValue)
	}
	tok, _ := signed.OkValue.(string)
	verified := Auth_verifyToken(goodSecret, tok).(SkyResult[any, any])
	if verified.Tag != 0 {
		t.Fatalf("verify failed: %v", verified.ErrValue)
	}
	claims, _ := verified.OkValue.(map[string]any)
	if claims["sub"] != "alice" {
		t.Fatalf("round-trip lost the 'sub' claim: %v", claims)
	}
}

func TestSign_DifferentSecretsProduceDifferentTokens(t *testing.T) {
	// Critical: the old %v stringification meant that the secret
	// []byte("nil") would match regardless of type. Confirm that
	// two distinct strong secrets produce distinguishable signing
	// output on the same claim.
	a := Auth_signToken(goodSecret, map[string]any{"x": 1}, 3600).(SkyResult[any, any])
	otherSecret := "fedcba9876543210fedcba9876543210"
	b := Auth_signToken(otherSecret, map[string]any{"x": 1}, 3600).(SkyResult[any, any])
	if a.Tag != 0 || b.Tag != 0 {
		t.Fatal("sign failed")
	}
	if a.OkValue.(string) == b.OkValue.(string) {
		t.Fatal("different secrets produced identical signed token — HMAC broken")
	}
	// Cross-verification must fail: signed with A, verified with B.
	vA := Auth_verifyToken(otherSecret, a.OkValue.(string)).(SkyResult[any, any])
	if vA.Tag == 0 {
		t.Fatal("verify succeeded with wrong secret")
	}
}
