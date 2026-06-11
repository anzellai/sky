package rt

// #586 — Std.Db.insertFieldsReturning end-to-end tests.
//
// Composes Db_insertFields' DEFAULT-omittable INSERT builder with a
// `RETURNING <projection>` clause + the Std.Db.Decode decode path.
// SQLite ≥ 3.35 (we link modernc.org/sqlite which is ≥ 3.45) honours
// `RETURNING` on plain INSERT and on DEFAULT VALUES.

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func openReturningTestDb(t *testing.T) *SkyDb {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := conn.Exec(`CREATE TABLE items (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT NOT NULL DEFAULT 'unnamed',
		status TEXT NOT NULL DEFAULT 'pending',
		note TEXT,
		qty INTEGER
	)`); err != nil {
		t.Fatalf("create items: %v", err)
	}
	return &SkyDb{conn: conn, driver: "sqlite"}
}

// makeStringDecoder returns a DbDecoder that pulls a single TEXT
// column out of the row by name.  Mirrors what `DbDec_string` builds
// on the Sky side without dragging the kernel registration into the
// test file.
func makeStringDecoder(col string) DbDecoder {
	return DbDecoder{
		run: func(row map[string]any) any {
			v, present := row[col]
			if !present {
				return Err[any, any](ErrDecode("col missing: " + col))
			}
			if v == nil {
				return Err[any, any](ErrDecode("col is NULL: " + col))
			}
			s, ok := v.(string)
			if !ok {
				return Err[any, any](ErrDecode("col not a string: " + col))
			}
			return Ok[any, any](s)
		},
		cols: []string{col},
	}
}

func makeIntDecoder(col string) DbDecoder {
	return DbDecoder{
		run: func(row map[string]any) any {
			v, present := row[col]
			if !present {
				return Err[any, any](ErrDecode("col missing: " + col))
			}
			if v == nil {
				return Err[any, any](ErrDecode("col is NULL: " + col))
			}
			// Driver gives int64 for INTEGER columns; AsInt narrows.
			return Ok[any, any](AsInt(v))
		},
		cols: []string{col},
	}
}

// nullableStringDecoder mirrors DbDec_nullable wrapping a string
// decoder.  Recognises both NULL forms used by normaliseSqlValue:
// raw Go nil and SkyMaybe[any]{Tag:1} (Nothing).
func nullableStringDecoder(col string) DbDecoder {
	inner := makeStringDecoder(col)
	return DbDecoder{
		run: func(row map[string]any) any {
			v, present := row[col]
			if !present || v == nil {
				return Ok[any, any](Nothing[any]())
			}
			if m, ok := v.(SkyMaybe[any]); ok && m.Tag == 1 {
				return Ok[any, any](Nothing[any]())
			}
			innerRes := inner.run(row)
			sr, ok := innerRes.(SkyResult[any, any])
			if !ok || sr.Tag != 0 {
				return innerRes
			}
			return Ok[any, any](Just[any](sr.OkValue))
		},
		cols: []string{col},
	}
}

func TestDb_insertFieldsReturning_OmittedDefaultDecodesApplied(t *testing.T) {
	db := openReturningTestDb(t)
	// Omit `status` — the database applies `'pending'` and RETURNING
	// hands it back.  Brief case 1: omit a DEFAULT column + RETURN it.
	task := Db_insertFieldsReturning(db, "items", []any{
		pair("name", setField(sqlValue("SqlString", "Widget"))),
		pair("status", omitField()),
		pair("note", setField(sqlValue("SqlString", "first batch"))),
	}, "status", makeStringDecoder("status"))
	res := runTask(t, task)
	if res.Tag != 0 {
		t.Fatalf("insert returning failed: %+v", res.ErrValue)
	}
	rows := AsList(res.OkValue)
	if len(rows) != 1 {
		t.Fatalf("rows: got %d want 1", len(rows))
	}
	if got, _ := rows[0].(string); got != "pending" {
		t.Errorf("RETURNING decoded status: got %q want pending", got)
	}
}

func TestDb_insertFieldsReturning_AutoincrementId(t *testing.T) {
	db := openReturningTestDb(t)
	// Insert twice — RETURNING id should give 1 then 2.  Brief case 2.
	for want := 1; want <= 2; want++ {
		task := Db_insertFieldsReturning(db, "items", []any{
			pair("name", setField(sqlValue("SqlString", "Gadget"))),
		}, "id", makeIntDecoder("id"))
		res := runTask(t, task)
		if res.Tag != 0 {
			t.Fatalf("insert returning failed (n=%d): %+v", want, res.ErrValue)
		}
		rows := AsList(res.OkValue)
		if len(rows) != 1 {
			t.Fatalf("rows: got %d want 1", len(rows))
		}
		if got, _ := rows[0].(int); got != want {
			t.Errorf("autoincrement id: got %d want %d", got, want)
		}
	}
}

func TestDb_insertFieldsReturning_NullableOmittedNothing(t *testing.T) {
	db := openReturningTestDb(t)
	// Omit `note` (nullable, no DEFAULT) — RETURNING note should
	// decode to Nothing via the nullable decoder.  Brief case 4.
	task := Db_insertFieldsReturning(db, "items", []any{
		pair("name", setField(sqlValue("SqlString", "Sprocket"))),
		pair("note", omitField()),
	}, "note", nullableStringDecoder("note"))
	res := runTask(t, task)
	if res.Tag != 0 {
		t.Fatalf("insert returning failed: %+v", res.ErrValue)
	}
	rows := AsList(res.OkValue)
	if len(rows) != 1 {
		t.Fatalf("rows: got %d want 1", len(rows))
	}
	mOmit, ok := rows[0].(SkyMaybe[any])
	if !ok || mOmit.Tag != 1 {
		t.Errorf("nullable decoded: got %+v want SkyMaybe Nothing", rows[0])
	}
}

func TestDb_insertFieldsReturning_NullableExplicitNull(t *testing.T) {
	db := openReturningTestDb(t)
	// Explicitly bind NULL via SqlNull witness — RETURNING decodes
	// to Nothing same as the omit case.
	task := Db_insertFieldsReturning(db, "items", []any{
		pair("name", setField(sqlValue("SqlString", "Knob"))),
		pair("note", setField(sqlValue("SqlNull", sqlValue("SqlString", "")))),
	}, "note", nullableStringDecoder("note"))
	res := runTask(t, task)
	if res.Tag != 0 {
		t.Fatalf("insert returning failed: %+v", res.ErrValue)
	}
	rows := AsList(res.OkValue)
	mExplicit, ok := rows[0].(SkyMaybe[any])
	if !ok || mExplicit.Tag != 1 {
		t.Errorf("nullable from explicit NULL: got %+v want SkyMaybe Nothing", rows[0])
	}
}

func TestDb_insertFieldsReturning_AllOmitDefaultValuesReturnsId(t *testing.T) {
	db := openReturningTestDb(t)
	// All columns OmitField → INSERT INTO items DEFAULT VALUES
	// RETURNING id.  Confirms the DEFAULT VALUES + RETURNING shape
	// (SQLite ≥ 3.35 + PostgreSQL) works through the kernel.
	task := Db_insertFieldsReturning(db, "items", []any{
		pair("name", omitField()),
		pair("status", omitField()),
	}, "id", makeIntDecoder("id"))
	res := runTask(t, task)
	if res.Tag != 0 {
		t.Fatalf("insert returning failed: %+v", res.ErrValue)
	}
	rows := AsList(res.OkValue)
	if got, _ := rows[0].(int); got != 1 {
		t.Errorf("first autoincrement id from DEFAULT VALUES: got %d want 1", got)
	}
}

func TestDb_insertFieldsReturning_EmptyProjectionRejected(t *testing.T) {
	db := openReturningTestDb(t)
	task := Db_insertFieldsReturning(db, "items", []any{
		pair("name", setField(sqlValue("SqlString", "x"))),
	}, "", makeStringDecoder("name"))
	res := runTask(t, task)
	if res.Tag == 0 {
		t.Fatalf("expected error for empty projection; got Ok %+v", res.OkValue)
	}
}

func TestDb_insertFieldsReturning_BadColumnRejected(t *testing.T) {
	db := openReturningTestDb(t)
	task := Db_insertFieldsReturning(db, "items", []any{
		pair("name; DROP TABLE items;--", setField(sqlValue("SqlString", "x"))),
	}, "id", makeIntDecoder("id"))
	res := runTask(t, task)
	if res.Tag == 0 {
		t.Fatalf("expected error for bad column name; got Ok %+v", res.OkValue)
	}
}
