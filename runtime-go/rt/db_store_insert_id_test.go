package rt

// Regression gate: `Store.insert` must return the id the DATABASE assigned,
// not the affected-row count.
//
// The insert-shaped Store kernels all ended in `AnyTaskRun(Db_exec(...))`, and
// `Db_exec` returns `RowsAffected`. For a single-row insert that is always 1,
// so `Store.insert` reported `1` for every row it ever wrote. The Sky signature
// is `Task Error Int`, nothing errors, and the value is a plausible id — so a
// caller wiring child rows to it silently attributes ALL of them to id 1.
// Compiles clean, runs clean, corrupts data.
//
// The verdict below is deliberately "two inserts must not report the same id".
// Asserting `== 1` on the first insert would pass against the broken code,
// which is exactly how this survived.
//
// `Store.insertMany` is pinned to rows-affected in the same file, because its
// docstring documents that and it is NOT part of the defect — a fix that
// "corrects" it would break the documented contract.

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// storeObj builds the JSON-object Value the Store kernels receive from
// `Codec.toValue`.
func storeObj(keys []string, vals []any) JsonValue {
	return JsonValue{raw: jsonOrderedObject{keys: keys, vals: vals}}
}

// colspec builds the `List (String, String)` (column, kind) argument.
func colspec(pairs ...[2]string) []any {
	out := make([]any, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, SkyTuple2{V0: p[0], V1: p[1]})
	}
	return out
}

// openStoreIdDb gives each test a serial-PK table — the shape whose id only the
// database knows, and therefore the shape the return value has to carry.
func openStoreIdDb(t *testing.T) *SkyDb {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := conn.Exec(`CREATE TABLE notes (
		id   INTEGER PRIMARY KEY AUTOINCREMENT,
		body TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create notes: %v", err)
	}
	return &SkyDb{conn: conn, driver: "sqlite"}
}

func TestStoreInsert_ReturnsAssignedId_execObjectWith(t *testing.T) {
	db := openStoreIdDb(t)
	ins := func(body string) int {
		t.Helper()
		res := runTask(t, Db_execObjectWith(db, "notes",
			colspec([2]string{"body", "text"}),
			storeObj([]string{"body"}, []any{body}),
			[]any{}, "id", "int"))
		if res.Tag != 0 {
			t.Fatalf("insert %q: %+v", body, res.ErrValue)
		}
		return AsIntOrZero(res.OkValue)
	}
	first, second := ins("first"), ins("second")
	if first == second {
		t.Fatalf("Store.insert reported the same id %d for two different rows — "+
			"this is rows-affected, not the assigned id; child rows wired to it "+
			"would all be attributed to one parent", first)
	}
	// Cross-check against what the engine actually stored.
	rows := mustRows(t, db, `SELECT id, body FROM notes ORDER BY id`)
	if len(rows) != 2 {
		t.Fatalf("row count: got %d want 2", len(rows))
	}
	if got := AsIntOrZero(rows[0]["id"]); got != first {
		t.Errorf("first insert returned %d but the row has id %d", first, got)
	}
	if got := AsIntOrZero(rows[1]["id"]); got != second {
		t.Errorf("second insert returned %d but the row has id %d", second, got)
	}
}

func TestStoreInsert_ReturnsAssignedId_execObject(t *testing.T) {
	db := openStoreIdDb(t)
	ins := func(body string) int {
		t.Helper()
		res := runTask(t, Db_execObject(db, "notes",
			colspec([2]string{"body", "text"}),
			storeObj([]string{"body"}, []any{body}), "id", "int"))
		if res.Tag != 0 {
			t.Fatalf("insert %q: %+v", body, res.ErrValue)
		}
		return AsIntOrZero(res.OkValue)
	}
	if first, second := ins("a"), ins("b"); first == second {
		t.Fatalf("Db_execObject reported the same id %d for two rows", first)
	}
}

// A caller-supplied (non-integer) PK has no DB-assigned integer to report, so
// the verb keeps returning rows-affected. Pinned so the fix cannot start
// fabricating an integer for String/UUID-keyed stores.
func TestStoreInsert_TextPkKeepsRowsAffected(t *testing.T) {
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if _, err := conn.Exec(`CREATE TABLE docs (id TEXT PRIMARY KEY, body TEXT)`); err != nil {
		t.Fatalf("create docs: %v", err)
	}
	db := &SkyDb{conn: conn, driver: "sqlite"}
	res := runTask(t, Db_execObjectWith(db, "docs",
		colspec([2]string{"id", "text"}, [2]string{"body", "text"}),
		storeObj([]string{"id", "body"}, []any{"d1", "hello"}),
		[]any{}, "id", "text"))
	if res.Tag != 0 {
		t.Fatalf("insert: %+v", res.ErrValue)
	}
	if got := AsIntOrZero(res.OkValue); got != 1 {
		t.Errorf("text-PK insert: got %d want 1 (rows affected)", got)
	}
}

func TestStoreUpsert_ReturnsAssignedId(t *testing.T) {
	db := openStoreIdDb(t)
	res := runTask(t, Db_upsertObject(db, "notes",
		colspec([2]string{"id", "int"}, [2]string{"body", "text"}),
		"id", storeObj([]string{"id", "body"}, []any{7, "seven"}), "int"))
	if res.Tag != 0 {
		t.Fatalf("upsert: %+v", res.ErrValue)
	}
	if got := AsIntOrZero(res.OkValue); got != 7 {
		t.Errorf("upsert returned %d want the row's id 7", got)
	}
}

// `Store.insertMany` documents "Returns rows affected". It shares the kernel
// family but NOT the defect: a bulk insert has no single id to report. Pinned
// so the root-cause sweep does not overshoot into a documented contract.
func TestStoreInsertMany_StaysRowsAffected(t *testing.T) {
	db := openStoreIdDb(t)
	res := runTask(t, Db_execObjectMany(db, "notes",
		colspec([2]string{"body", "text"}),
		[]any{
			storeObj([]string{"body"}, []any{"x"}),
			storeObj([]string{"body"}, []any{"y"}),
			storeObj([]string{"body"}, []any{"z"}),
		}))
	if res.Tag != 0 {
		t.Fatalf("insertMany: %+v", res.ErrValue)
	}
	if got := AsIntOrZero(res.OkValue); got != 3 {
		t.Errorf("insertMany: got %d want 3 (rows affected)", got)
	}
}
