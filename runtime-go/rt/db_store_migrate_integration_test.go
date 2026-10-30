//go:build integration
// +build integration

package rt

// Std.Db.Store migrate, PostgreSQL arm (v0.29.0, batch C). Every test name
// carries "Postgres" so the CI integration jobs (`-run Postgres`) run it.

import (
	"strings"
	"testing"
)

// D1 parity on a real PostgreSQL table with a row: Store.migrate runs the
// same SQL as renderMigOp of the v2 ops `sky db migrate --gen` writes.
func TestStoreMigratePostgresParity(t *testing.T) {
	assertMigrateParity(t, openStoreQueryPostgres(t, ""))
}

func TestStoreMigratePostgresD1_AddsNotNullColumnsToATableWithRows(t *testing.T) {
	d := openStoreQueryPostgres(t, "")
	const table = "orders_migc_pg"
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table)
	t.Cleanup(func() { _, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table) })
	if r := autoMigrate(t, d, table, cspec("id", "text"), nil); r.Tag != 0 {
		t.Fatalf("create: %+v", r)
	}
	if _, err := d.conn.Exec("INSERT INTO " + table + " (id) VALUES ('o1')"); err != nil {
		t.Fatal(err)
	}
	cs := cspec("id", "text", "qty", "int", "label", "text|dtext=new", "seen_at", "text|dnow",
		"note", "text?", "score", "real|dfloat=1.5", "active", "bool")
	r := autoMigrate(t, d, table, cs, nil)
	if r.Tag != 0 {
		t.Fatalf("migrate: %+v", r)
	}
	ran := []string{}
	for _, s := range AsList(r.OkValue) {
		ran = append(ran, AsString(s))
	}
	want := strings.Join([]string{
		"ALTER TABLE " + table + " ADD COLUMN qty BIGINT NOT NULL DEFAULT 0",
		"ALTER TABLE " + table + " ADD COLUMN label TEXT NOT NULL DEFAULT 'new'",
		"ALTER TABLE " + table + " ADD COLUMN seen_at TEXT NOT NULL DEFAULT now()",
		"ALTER TABLE " + table + " ADD COLUMN note TEXT",
		"ALTER TABLE " + table + " ADD COLUMN score DOUBLE PRECISION NOT NULL DEFAULT 1.5",
		"ALTER TABLE " + table + " ADD COLUMN active BOOLEAN NOT NULL DEFAULT FALSE",
	}, "\n")
	if got := strings.Join(ran, "\n"); got != want {
		t.Fatalf("statements:\n%s\nwant\n%s", got, want)
	}
	var qty int
	var label, seen string
	var active bool
	if err := d.conn.QueryRow("SELECT qty, label, seen_at, active FROM "+table+" WHERE id = 'o1'").
		Scan(&qty, &label, &seen, &active); err != nil {
		t.Fatal(err)
	}
	if qty != 0 || label != "new" || seen == "" || active {
		t.Fatalf("old row: qty=%d label=%q seen=%q active=%v", qty, label, seen, active)
	}
	if r := autoMigrate(t, d, table, cs, nil); r.Tag != 0 || len(AsList(r.OkValue)) != 0 {
		t.Fatalf("second migrate is not empty: %+v", r)
	}
	// A required unique column on a table with rows is Err, table unchanged.
	if r := autoMigrate(t, d, table, append(cs, cspec("code", "text|u")...), nil); r.Tag == 0 {
		t.Fatalf("a required unique column was added to a table with rows")
	}
	if cols, _ := codecTableColumns(d, table); cols["code"] {
		t.Fatalf("the table was changed")
	}
}

func TestStoreMigratePostgresIndexes_CreatedOnceAndLookedUpByTable(t *testing.T) {
	d := openStoreQueryPostgres(t, "")
	const table = "orders_ixc_pg"
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table)
	t.Cleanup(func() { _, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table) })
	cs := cspec("id", "text", "customerId", "text", "slug", "text")
	ixs := []any{storeIdx([]string{"customerId", "id"}, false, ""), storeIdx([]string{"slug"}, true, "slug <> ''")}
	if r := autoMigrate(t, d, table, cs, ixs); r.Tag != 0 || len(AsList(r.OkValue)) != 3 {
		t.Fatalf("first migrate: %+v", r)
	}
	if r := autoMigrate(t, d, table, cs, ixs); r.Tag != 0 || len(AsList(r.OkValue)) != 0 {
		t.Fatalf("second migrate is not empty (index lookup missed a folded name): %+v", r)
	}
	for _, s := range []string{"('1','c','')", "('2','c','')", "('3','c','a')"} {
		if _, err := d.conn.Exec("INSERT INTO " + table + " VALUES " + s); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.conn.Exec("INSERT INTO " + table + " VALUES ('4','c','a')"); err == nil {
		t.Fatalf("the partial unique index let a second 'a' in")
	}
}

// On PostgreSQL a dnow column is left out of the INSERT, so the database
// default now() stamps it.
func TestStoreMigratePostgresInsertLeavesDefaultNowToTheDatabase(t *testing.T) {
	d := openStoreQueryPostgres(t, "")
	const table = "events_nowc_pg"
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table)
	t.Cleanup(func() { _, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table) })
	cs := cspec("id", "text", "created_at", "text|dnow")
	_ = autoMigrate(t, d, table, cs, nil)
	res, ok := AnyTaskRun(Db_execObjectWith(d, table, cs, storeObj([]string{"id"}, []any{"e1"}),
		[]any{}, "id", "text")).(SkyResult[any, any])
	if !ok || res.Tag != 0 {
		t.Fatalf("insert: %+v", res)
	}
	var at string
	_ = d.conn.QueryRow("SELECT created_at FROM " + table).Scan(&at)
	if at == "" {
		t.Fatalf("created_at is empty")
	}
}

func TestStoreMigratePostgresSchemaNowOnTimestampIsMillis(t *testing.T) {
	assertSchemaNowIsMillis(t, openStoreQueryPostgres(t, ""), "schema_now_ms_pg")
}

// A required unique column that a v2 migration adds to a table with 2 rows:
// the apply error names the table, the column and the fix, and rolls back.
func TestStoreMigratePostgresRequiredUniqueOnRowsIsExplained(t *testing.T) {
	assertRequiredUniqueOnRowsIsExplained(t, openStoreQueryPostgres(t, ""), "codes_ru_pg")
}
