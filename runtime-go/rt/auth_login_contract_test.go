package rt

import (
	"path/filepath"
	"testing"
)

func forceAuthTask(v any) any {
	if f, ok := v.(func() any); ok {
		return f()
	}
	return v
}

// #3: `Std.Auth.login : Db -> String -> String -> Task Error Int` must return the
// user id (an int) — matching `register` and the doc comment — NOT a
// `map{id,email,role}`. The runtime previously returned the map, so well-typed
// Sky code (which treats the result as Int per the signature) mis-coerced it.
func TestAuth_Login_ReturnsUserIdMatchingRegister(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-login-contract.db")

	dbRes := forceAuthTask(Db_connect(path)).(SkyResult[any, any])
	if dbRes.Tag != 0 {
		t.Fatalf("db connect failed: %v", dbRes.ErrValue)
	}
	db := dbRes.OkValue

	regRes := forceAuthTask(Auth_register(db, "a@example.com", "pw12345678")).(SkyResult[any, any])
	if regRes.Tag != 0 {
		t.Fatalf("register failed: %v", regRes.ErrValue)
	}
	regID, ok := regRes.OkValue.(int)
	if !ok {
		t.Fatalf("register must return an int id, got %T", regRes.OkValue)
	}

	loginRes := forceAuthTask(Auth_login(db, "a@example.com", "pw12345678")).(SkyResult[any, any])
	if loginRes.Tag != 0 {
		t.Fatalf("login failed: %v", loginRes.ErrValue)
	}
	loginID, ok := loginRes.OkValue.(int)
	if !ok {
		t.Fatalf("login must return an int id (Task Error Int), got %T: %v",
			loginRes.OkValue, loginRes.OkValue)
	}
	if loginID != regID {
		t.Fatalf("login id %d != register id %d", loginID, regID)
	}
}
