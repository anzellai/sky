package rt

// A SQL NULL column in a `Dict String String` row (`Db.query`,
// `Db.getById`, `Db.findOneByField`, ...) must read as "" through
// `Dict.get`, `Db.getField` and `Db.getString`, never as the Go text of
// an internal `Maybe` ("{1 <nil>}"). Before the fix the row held a
// `Nothing` value, and the String readers rendered it with `%v`.

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func assertNullRowReadsEmpty(t *testing.T, db *SkyDb, q string) {
	t.Helper()
	res := runTask(t, Db_query(db, q, []any{}))
	if res.Tag != 0 {
		t.Fatalf("query failed: %v", res.ErrValue)
	}
	rows := AsList(res.OkValue)
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	row := rows[0]
	if got := Db_getField("t", row); got != "" {
		t.Errorf("Db.getField on NULL = %q, want \"\"", got)
	}
	if got := Db_getString("t", row); got != "" {
		t.Errorf("Db.getString on NULL = %q, want \"\"", got)
	}
	m, ok := row.(map[string]any)
	if !ok {
		t.Fatalf("row is %T, want map[string]any", row)
	}
	for col, want := range map[string]string{"t": "", "s": "x", "n": "7"} {
		v, exists := m[col]
		if !exists {
			t.Errorf("column %q missing from the row", col)
			continue
		}
		s, isStr := v.(string)
		if !isStr {
			t.Errorf("Dict String String row holds %T (%v) for column %q, want a string", v, v, col)
			continue
		}
		if s != want {
			t.Errorf("row[%q] = %q, want %q", col, s, want)
		}
	}
	if got := Db_getField("s", row); got != "x" {
		t.Errorf("Db.getField text = %q, want \"x\"", got)
	}
	if got := Db_getInt("n", row); got != 7 {
		t.Errorf("Db.getInt = %d, want 7", got)
	}
}

func TestDbQuery_NullColumnReadsEmpty_Sqlite(t *testing.T) {
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer conn.Close()
	assertNullRowReadsEmpty(t, &SkyDb{conn: conn, driver: "sqlite"},
		"SELECT NULL AS t, 'x' AS s, 7 AS n")
}

// The by-key readers return the same row shape as Db.query.
func TestDbGetById_NullColumnReadsEmpty_Sqlite(t *testing.T) {
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, note TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO items (id, note) VALUES (1, NULL)`); err != nil {
		t.Fatal(err)
	}
	db := &SkyDb{conn: conn, driver: "sqlite"}
	res := runTask(t, Db_getById(db, "items", "1"))
	if res.Tag != 0 {
		t.Fatalf("getById failed: %v", res.ErrValue)
	}
	mb, ok := res.OkValue.(SkyMaybe[any])
	if !ok || mb.Tag != 0 {
		t.Fatalf("getById = %#v, want Just row", res.OkValue)
	}
	if got := Db_getField("note", mb.JustValue); got != "" {
		t.Errorf("Db.getField on NULL via getById = %q, want \"\"", got)
	}
	if v := mb.JustValue.(map[string]any)["note"]; v != "" {
		t.Errorf("getById row[note] = %#v, want \"\"", v)
	}
}
