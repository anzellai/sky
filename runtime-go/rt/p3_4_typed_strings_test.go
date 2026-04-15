package rt

// Audit P3-4: high-risk `fmt.Sprintf("%v", x)` sites in db_auth.go
// used to silently stringify non-string inputs. A caller passing a
// nil, an int, or a map would get a "deterministic but wrong"
// result — bcrypt hashing the literal string "<nil>" or "42", or a
// SQL driver receiving the stringified form of a Maybe value. These
// are boundary bugs that should surface as typed `Err` results, not
// hash-of-nonsense / silently-wrong-query.

import (
	"testing"
)


func p34IsErr(r any) bool {
	sr, ok := r.(SkyResult[any, any])
	return ok && sr.Tag == 1
}

func okValue(r any) any {
	sr, _ := r.(SkyResult[any, any])
	return sr.OkValue
}


// --- passwords ---------------------------------------------------------

func TestHashPassword_rejectsNonString(t *testing.T) {
	if !p34IsErr(Auth_hashPassword(nil)) {
		t.Fatalf("Auth.hashPassword(nil) must be Err")
	}
}

func TestHashPassword_rejectsInt(t *testing.T) {
	if !p34IsErr(Auth_hashPassword(12345678)) {
		t.Fatalf("Auth.hashPassword(int) must be Err")
	}
}

func TestVerifyPassword_rejectsNonString(t *testing.T) {
	if b, _ := Auth_verifyPassword(nil, "whatever").(bool); b {
		t.Fatalf("verifyPassword(nil, _) must not succeed")
	}
}

// --- SQL queries -------------------------------------------------------

func TestDbExec_rejectsNonStringQuery(t *testing.T) {
	conn := Db_connect(":memory:")
	if p34IsErr(conn) {
		t.Fatalf("connect failed")
	}
	db := okValue(conn)
	if !p34IsErr(Db_exec(db, nil, []any{})) {
		t.Fatalf("Db_exec(nil query) must be Err")
	}
}

func TestDbQuery_rejectsNonStringQuery(t *testing.T) {
	conn := Db_connect(":memory:")
	db := okValue(conn)
	if !p34IsErr(Db_query(db, 42, []any{})) {
		t.Fatalf("Db_query(int query) must be Err")
	}
}
