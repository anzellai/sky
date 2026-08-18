package rt

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// runTaskRes runs a Sky Task thunk (func() any) and returns its result value.
func runTaskRes(t *testing.T, task any) any {
	t.Helper()
	fn, ok := task.(func() any)
	if !ok {
		t.Fatalf("expected a Task thunk (func() any), got %T", task)
	}
	return fn()
}

func mustOk(t *testing.T, res any, what string) any {
	t.Helper()
	tag, ok, errv := anyResultView(res)
	if tag != 0 {
		t.Fatalf("%s: expected Ok, got Err/unknown (tag=%d, err=%v)", what, tag, errv)
	}
	return ok
}

func mustErr(t *testing.T, res any, what string) {
	t.Helper()
	tag, _, _ := anyResultView(res)
	if tag == 0 {
		t.Fatalf("%s: expected Err, got Ok", what)
	}
}

func openMemAuthDb(t *testing.T) *SkyDb {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return &SkyDb{conn: conn, driver: "sqlite"}
}

// openFileAuthDb opens a fresh handle onto a shared temp-file sqlite DB, so two
// handles onto the SAME path model two replicas sharing one table.
func openFileAuthDb(t *testing.T, path string) *SkyDb {
	t.Helper()
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite %s: %v", path, err)
	}
	t.Cleanup(func() { conn.Close() })
	return &SkyDb{conn: conn, driver: "sqlite"}
}

// ─── Gate 10: canonicalSub ─────────────────────────────────────────

func TestCanonicalSub(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"string verbatim", "user-42", "user-42"},
		{"string numeric verbatim", "42", "42"},
		{"int64", int64(42), "42"},
		{"int", 42, "42"},
		// A large id passed as a STRING is exact — no float64 round-trip.
		{"large id string exact", "9007199254740993", "9007199254740993"}, // 2^53 + 1
		{"float64 integral", float64(42), "42"},
		{"float64 non-integral rejected", float64(42.5), ""},
		{"nil", nil, ""},
	}
	for _, c := range cases {
		if got := canonicalSub(c.in); got != c.want {
			t.Errorf("canonicalSub(%v [%s]) = %q, want %q", c.in, c.name, got, c.want)
		}
	}
	// The float64-sub caveat: a numeric JWT sub above 2^53 has already lost
	// precision in the JSON decode, so canonicalSub of the float and of the
	// exact string DISAGREE — which is why the admin API takes a String.
	big := float64(9007199254740993) // decodes to 9007199254740992
	if canonicalSub(big) == "9007199254740993" {
		t.Errorf("float64 above 2^53 unexpectedly exact — the caveat should hold")
	}
}

// ─── Gate 1: revoked_at persists across restart ────────────────────

func TestRevokedAtPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rev.db")
	dbA := openFileAuthDb(t, path)
	mustOk(t, runTaskRes(t, Auth_revokeUser(dbA, "42")), "revokeUser")

	// Reopen a FRESH handle (models a process restart / a second replica).
	dbB := openFileAuthDb(t, path)
	res := runTaskRes(t, Auth_isRevoked(dbB, "42", 1))
	rv := mustOk(t, res, "isRevoked after reopen")
	if b, _ := rv.(bool); !b {
		t.Fatalf("gate 1: revoked_at must persist across reopen — isRevoked=false")
	}
}

// ─── Gate 2: revoke != ban (bind-before vs bind-after) ─────────────

func TestRevokeIsNotBan(t *testing.T) {
	db := openMemAuthDb(t)
	// Revoke stamps revoked_at = now (a real unix second). Use boundAt values
	// straddling it: a very old iat (bound before) is revoked; a far-future iat
	// (a fresh bind after) is active.
	mustOk(t, runTaskRes(t, Auth_revokeUser(db, "42")), "revokeUser")

	before := int64(1) // long before revoked_at
	if st, err := authAccessState(db, "42", before); err != nil || st != accessRevoked {
		t.Fatalf("gate 2: a session bound BEFORE revoke must be Revoked, got state=%d err=%v", st, err)
	}
	after := int64(1 << 40) // year ~36812 — after any real revoked_at
	if st, err := authAccessState(db, "42", after); err != nil || st != accessActive {
		t.Fatalf("gate 2: a FRESH bind AFTER revoke must be Active (revoke != ban), got state=%d err=%v", st, err)
	}
}

// ─── Gate 7: disabled user cannot login (before verifyPassword) ────

func TestDisabledUserCannotLogin(t *testing.T) {
	db := openMemAuthDb(t)
	uid := mustOk(t, runTaskRes(t, Auth_register(db, "a@b.c", "correct-horse")), "register")
	uidInt, _ := uid.(int)
	if uidInt == 0 {
		t.Fatalf("register returned no id")
	}

	// A correct-password login works BEFORE disabling.
	mustOk(t, runTaskRes(t, Auth_login(db, "a@b.c", "correct-horse")), "login pre-disable")

	// Disable, then the SAME correct password is rejected — and the rejection
	// is the disabled lock-out (checked before the bcrypt verify), not a
	// credential mismatch.
	mustOk(t, runTaskRes(t, Auth_disableUser(db, strconv.Itoa(uidInt))), "disableUser")
	res := runTaskRes(t, Auth_login(db, "a@b.c", "correct-horse"))
	mustErr(t, res, "login while disabled")
	_, _, errv := anyResultView(res)
	if !strings.Contains(strings.ToLower(errMessage(errv)), "disabled") {
		t.Fatalf("gate 7: disabled login must fail with a disabled lock-out (before verifyPassword), got %v", errv)
	}

	// enableUser restores login.
	mustOk(t, runTaskRes(t, Auth_enableUser(db, strconv.Itoa(uidInt))), "enableUser")
	mustOk(t, runTaskRes(t, Auth_login(db, "a@b.c", "correct-horse")), "login after enable")
}

// ─── Gate 8: cross-replica shared-table read ───────────────────────

func TestCrossReplicaRevocationViaSharedTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.db")
	replicaA := openFileAuthDb(t, path)
	replicaB := openFileAuthDb(t, path)

	// B sees the user active before A revokes.
	if st, err := authAccessState(replicaB, "77", 100); err != nil || st != accessActive {
		t.Fatalf("pre-revoke: B should see Active, got %d err=%v", st, err)
	}
	// A revokes; B — reading the SHARED table fresh, with no cached session —
	// must now see Revoked.
	mustOk(t, runTaskRes(t, Auth_revokeUser(replicaA, "77")), "revoke via A")
	if st, err := authAccessState(replicaB, "77", 100); err != nil || st != accessRevoked {
		t.Fatalf("gate 8: B must read the revoke from the shared table, got %d err=%v", st, err)
	}
}

// ─── Gate 9: the session blob carries NO revocation state ──────────

func TestBlobHasNoRevocationField(t *testing.T) {
	// The binding (UserID/BoundAt) round-trips; the revocation STATE
	// (revoked_at/disabled_at) must NEVER be on the blob — else a stale replica
	// serves an out-of-date verdict.
	sess := &liveSession{
		model:   map[string]any{"n": 1},
		userID:  "u-99",
		boundAt: 123456,
	}
	blob, err := encodeSession(sess)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := decodeSession(blob)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.userID != "u-99" || got.boundAt != 123456 {
		t.Fatalf("gate 9: UserID/BoundAt must round-trip, got %q/%d", got.userID, got.boundAt)
	}
	// Reflect over storableSession's field names — no revocation-state field.
	tp := reflect.TypeOf(storableSession{})
	for i := 0; i < tp.NumField(); i++ {
		lower := strings.ToLower(tp.Field(i).Name)
		if strings.Contains(lower, "revok") || strings.Contains(lower, "disabled") {
			t.Fatalf("gate 9: storableSession must carry NO revocation state, found field %q", tp.Field(i).Name)
		}
	}
}

// ─── Pre-feature users table migrates disabled_at in idempotently ──

func TestDisabledColumnMigratesIdempotently(t *testing.T) {
	db := openMemAuthDb(t)
	// A pre-feature users table WITHOUT disabled_at.
	if _, err := db.conn.Exec(`CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		role TEXT DEFAULT 'user',
		created_at BIGINT NOT NULL)`); err != nil {
		t.Fatalf("create legacy users: %v", err)
	}
	// ensureUsersDisabledColumn adds it; a second call is a no-op (idempotent).
	if err := ensureUsersDisabledColumn(db); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	if err := ensureUsersDisabledColumn(db); err != nil {
		t.Fatalf("second migrate (must be idempotent): %v", err)
	}
	if _, err := db.conn.Exec(`INSERT INTO users (email, password_hash, created_at) VALUES ('x@y.z','h',1)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	disabled, err := authIsDisabled(db, "1")
	if err != nil || disabled {
		t.Fatalf("fresh user should not be disabled, got disabled=%v err=%v", disabled, err)
	}
}

func errMessage(errv any) string {
	// Sky Error values render via Debug_toString; a plain fmt covers the rest.
	if errv == nil {
		return ""
	}
	if s, ok := errv.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", errv)
}
