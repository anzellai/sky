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


// p34IsErr / okValue handle both legacy eager Result returns and the
// new Task-everywhere thunk returns by routing through AnyTaskRun
// (which forces thunks and passes Results through).
func p34IsErr(r any) bool {
	sr, ok := AnyTaskRun(r).(SkyResult[any, any])
	return ok && sr.Tag == 1
}

func okValue(r any) any {
	sr, _ := AnyTaskRun(r).(SkyResult[any, any])
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

func TestDbConnect_unitFallsBackToSkyDbPath(t *testing.T) {
	// Sky `()` evaluates to struct{} at the Go FFI boundary.
	// Db.connect () should consult SKY_DB_PATH (set from sky.toml
	// [database].path at program startup).
	t.Setenv("SKY_DB_PATH", ":memory:")
	r := Db_connect(struct{}{})
	if isErr := func() bool {
		sr, ok := r.(SkyResult[any, any]); return ok && sr.Tag == 1
	}(); isErr {
		t.Fatalf("Db.connect () must succeed when SKY_DB_PATH is set, got %+v", r)
	}
}

func TestDbConnect_unitWithoutSkyDbPathIsErr(t *testing.T) {
	// No SKY_DB_PATH → typed Err with a helpful message instead
	// of silently opening a file named `{}` (the pre-P3-4 bug).
	t.Setenv("SKY_DB_PATH", "")
	if !p34IsErr(Db_connect(struct{}{})) {
		t.Fatalf("Db.connect () must be Err when SKY_DB_PATH unset")
	}
}

func TestDbQuery_rejectsNonStringQuery(t *testing.T) {
	conn := Db_connect(":memory:")
	db := okValue(conn)
	if !p34IsErr(Db_query(db, 42, []any{})) {
		t.Fatalf("Db_query(int query) must be Err")
	}
}
