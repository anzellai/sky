package rt

// P5 (Gap A6) — Auth typed-boundary regression.
//
// Every security-critical Auth kernel error message returned to the
// caller MUST NOT leak the actual Go type of the offending value.
// The pre-P5 code path stringified `%T` into the user-visible message
// — so a non-string secret would surface in API responses / logs
// the runtime type of the leak source (e.g. `int`, `<nil>`,
// `*rt.SkyMaybe[…]`). That gives an attacker free reconnaissance
// of how the upstream binding is shaped.
//
// Post-P5 the user-visible message is a fixed `expected String`
// blurb; the actual Go type is logged via `Log.warn` for the
// server-side audit trail.
//
// The six cases below cover every public Auth kernel that takes a
// String argument:
//   1. Auth_hashPassword (pw)
//   2. Auth_hashPasswordCost (pw)
//   3. Auth_passwordStrength (pw)
//   4. Auth_signToken (secret)
//   5. Auth_verifyToken (secret)
//   6. Auth_verifyToken (token, secret OK)

import (
	"strings"
	"testing"
)

// authErrorMessage extracts the user-visible error message from a
// Sky Err result for assertion. Returns "" when the result isn't an
// Err shape (test will fail).
func authErrorMessage(t *testing.T, res any) string {
	t.Helper()
	sr, ok := res.(SkyResult[any, any])
	if !ok {
		t.Fatalf("result was not SkyResult: %#v", res)
	}
	if sr.Tag != 1 {
		t.Fatalf("result was not Err (Tag=%d, OkValue=%#v)", sr.Tag, sr.OkValue)
	}
	adt, ok := sr.ErrValue.(SkyADT)
	if !ok {
		t.Fatalf("error value was not SkyADT: %#v", sr.ErrValue)
	}
	if len(adt.Fields) < 2 {
		t.Fatalf("error ADT had %d fields, expected ≥2", len(adt.Fields))
	}
	info, ok := adt.Fields[1].(SkyErrorInfo)
	if !ok {
		t.Fatalf("error info field was not SkyErrorInfo: %#v", adt.Fields[1])
	}
	return info.Message
}

// assertFixedAuthError asserts the message is a fixed "expected String"
// blurb keyed on the caller tag AND does NOT contain any Go type
// reconnaissance leak (`int`, `<nil>`, `map[`, `*`).
//
// The exact required prefix is `<callerTag>: expected String`. The
// pre-P5 code returned `… expected String, got int` — the `, got <Go-type>`
// suffix is the leak this test forbids.
func assertFixedAuthError(t *testing.T, msg, callerTag string) {
	t.Helper()
	want := callerTag + ": expected String"
	if !strings.HasPrefix(msg, want) {
		t.Fatalf("error message %q must start with %q", msg, want)
	}
	// The most damaging leak — `%T` output — is rejected outright.
	// Any of these substrings would indicate a regression.
	forbidden := []string{
		", got int",
		", got <nil>",
		", got map[",
		", got *",
		", got rt.",
		", got float64",
		", got bool",
		", got []",
		", got struct",
	}
	for _, f := range forbidden {
		if strings.Contains(msg, f) {
			t.Fatalf("auth error leaks runtime type via %q in message %q",
				f, msg)
		}
	}
}

func TestAuth_HashPassword_NonStringMessageHidesType(t *testing.T) {
	res := Auth_hashPassword(42)
	msg := authErrorMessage(t, res)
	assertFixedAuthError(t, msg, "hashPassword")
}

func TestAuth_HashPasswordCost_NonStringMessageHidesType(t *testing.T) {
	res := Auth_hashPasswordCost(map[string]any{"sneaky": "value"}, 12)
	msg := authErrorMessage(t, res)
	assertFixedAuthError(t, msg, "hashPassword")
}

func TestAuth_PasswordStrength_NonStringMessageHidesType(t *testing.T) {
	res := Auth_passwordStrength(nil)
	msg := authErrorMessage(t, res)
	assertFixedAuthError(t, msg, "passwordStrength")
}

func TestAuth_SignToken_NonStringSecretMessageHidesType(t *testing.T) {
	res := Auth_signToken(123, map[string]any{}, 3600)
	msg := authErrorMessage(t, res)
	assertFixedAuthError(t, msg, "signToken")
}

func TestAuth_VerifyToken_NonStringSecretMessageHidesType(t *testing.T) {
	res := Auth_verifyToken(nil, "some.token.string")
	msg := authErrorMessage(t, res)
	assertFixedAuthError(t, msg, "verifyToken")
}

func TestAuth_VerifyToken_NonStringTokenMessageHidesType(t *testing.T) {
	res := Auth_verifyToken(goodSecret, 99)
	msg := authErrorMessage(t, res)
	// The token-leg uses callerTag "verifyToken" too.
	assertFixedAuthError(t, msg, "verifyToken")
}
