package rt

// Audit P1-3: Db.findWhere's string-WHERE form is SQL-injection-prone
// by design (the comment explicitly says "never build from untrusted
// input"). The safe alternatives — findOneByField, findManyByField,
// findByConditions — take column names that are identifier-validated
// and values that are always bound as SQL parameters. This spec
// confirms the safe APIs: (a) return correct results on honest input,
// (b) survive a SQLi payload as a value, and (c) reject malformed
// identifiers.

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// openTestDb sets up an in-memory SQLite with a users table and
// three rows so we can exercise the find* helpers end-to-end.
func openTestDb(t *testing.T) *SkyDb {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := conn.Exec(`CREATE TABLE users (
		id INTEGER PRIMARY KEY,
		email TEXT NOT NULL,
		role TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create users: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO users (id, email, role) VALUES
		(1, 'alice@example.com', 'admin'),
		(2, 'bob@example.com',   'member'),
		(3, 'carol@example.com', 'member')`); err != nil {
		t.Fatalf("seed users: %v", err)
	}
	return &SkyDb{conn: conn, driver: "sqlite"}
}

func asOkList(t *testing.T, v any) []any {
	t.Helper()
	sr, ok := v.(SkyResult[any, any])
	if !ok {
		t.Fatalf("expected SkyResult, got %T", v)
	}
	if sr.Tag != 0 {
		t.Fatalf("expected Ok, got Err: %v", sr.ErrValue)
	}
	xs, ok := sr.OkValue.([]any)
	if !ok {
		t.Fatalf("expected []any, got %T", sr.OkValue)
	}
	return xs
}

func TestFindOneByField_HonestQueryReturnsJust(t *testing.T) {
	db := openTestDb(t)
	defer db.conn.Close()
	res := Db_findOneByField(db, "users", "email", "alice@example.com")
	sr := res.(SkyResult[any, any])
	if sr.Tag != 0 {
		t.Fatalf("unexpected Err: %v", sr.ErrValue)
	}
	m, ok := sr.OkValue.(SkyMaybe[any])
	if !ok {
		t.Fatalf("expected SkyMaybe, got %T", sr.OkValue)
	}
	if m.Tag != 0 {
		t.Fatalf("expected Just, got Nothing")
	}
}

func TestFindOneByField_MissingReturnsNothing(t *testing.T) {
	db := openTestDb(t)
	defer db.conn.Close()
	res := Db_findOneByField(db, "users", "email", "ghost@example.com")
	sr := res.(SkyResult[any, any])
	if sr.Tag != 0 {
		t.Fatalf("unexpected Err: %v", sr.ErrValue)
	}
	m, _ := sr.OkValue.(SkyMaybe[any])
	if m.Tag != 1 {
		t.Fatal("expected Nothing for missing row")
	}
}

func TestFindOneByField_SqliValueIsParameterised(t *testing.T) {
	// Classic payload. If findOneByField naively interpolated the
	// value, the comment would neutralise the rest of the query
	// and the query would return all rows, leaking data. The
	// parameter binding means SQLite sees the literal string —
	// no matching email, returns Nothing.
	db := openTestDb(t)
	defer db.conn.Close()
	payload := "alice@example.com' OR 1=1 -- "
	res := Db_findOneByField(db, "users", "email", payload)
	sr := res.(SkyResult[any, any])
	if sr.Tag != 0 {
		t.Fatalf("SQLi payload in value: should succeed with Nothing, got Err %v", sr.ErrValue)
	}
	m, _ := sr.OkValue.(SkyMaybe[any])
	if m.Tag != 1 {
		t.Fatal("SQLi payload matched a row — parameter binding broken")
	}
}

func TestFindOneByField_RejectsMaliciousColumnName(t *testing.T) {
	// Column name goes through quoteIdent which only accepts
	// [a-zA-Z0-9_]. A malicious column name should produce Err,
	// not a successful injection.
	db := openTestDb(t)
	defer db.conn.Close()
	res := Db_findOneByField(db, "users", "email; DROP TABLE users--", "x")
	sr := res.(SkyResult[any, any])
	if sr.Tag != 1 {
		t.Fatal("malicious column name should produce Err, not silent success")
	}
}

func TestFindManyByField_ReturnsMatchingRows(t *testing.T) {
	db := openTestDb(t)
	defer db.conn.Close()
	res := Db_findManyByField(db, "users", "role", "member")
	xs := asOkList(t, res)
	if len(xs) != 2 {
		t.Fatalf("expected 2 members, got %d", len(xs))
	}
}

func TestFindByConditions_MultipleFieldsAnded(t *testing.T) {
	db := openTestDb(t)
	defer db.conn.Close()
	res := Db_findByConditions(db, "users",
		map[string]any{"email": "bob@example.com", "role": "member"})
	xs := asOkList(t, res)
	if len(xs) != 1 {
		t.Fatalf("expected 1 row for (bob + member), got %d", len(xs))
	}
}

func TestFindByConditions_EmptyMeansSelectAll(t *testing.T) {
	db := openTestDb(t)
	defer db.conn.Close()
	res := Db_findByConditions(db, "users", map[string]any{})
	xs := asOkList(t, res)
	if len(xs) != 3 {
		t.Fatalf("empty conditions should return all rows, got %d", len(xs))
	}
}

func TestFindByConditions_RejectsMaliciousColumn(t *testing.T) {
	db := openTestDb(t)
	defer db.conn.Close()
	res := Db_findByConditions(db, "users",
		map[string]any{"role; DROP TABLE users--": "any"})
	sr := res.(SkyResult[any, any])
	if sr.Tag != 1 {
		t.Fatal("malicious column name in conditions should produce Err")
	}
}
