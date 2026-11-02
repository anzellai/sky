//go:build !js

package rt

import (
	"strings"
	"testing"
)

// A Codec.auto Decimal or Money field (v0.23-v0.29.1) writes JSON text:
// `"9.5"`, `{"amount":"1","currency":"GBP"}`. A field moved to Codec.decimal
// or Codec.moneyIn on that column compared as text on PostgreSQL ("10" before
// "9.5", so orderAsc and gt gave wrong rows) and new writes (`20`) mixed with
// the old spelling on both dialects. Store.migrate and Store.pushProject
// (Db_autoMigrate) now refuse it, naming table.column and the statement that
// converts the column; after that statement the store migrates and the values
// order by value. NULL stays NULL.
func assertLegacyDecimalColumnRefused(t *testing.T, d *SkyDb, table, fixPrice, fixCost, orderBy string) {
	t.Helper()
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table)
	t.Cleanup(func() { _, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table) })
	for _, stmt := range []string{
		"CREATE TABLE " + table + " (id TEXT PRIMARY KEY NOT NULL, price TEXT, cost TEXT)",
		"INSERT INTO " + table + " VALUES ('a', '\"9.5\"', '{\"amount\":\"1.50\",\"currency\":\"GBP\"}')",
		"INSERT INTO " + table + " VALUES ('b', '\"10\"', '{\"amount\":\"2\",\"currency\":\"GBP\"}')",
		"INSERT INTO " + table + " VALUES ('c', '\"100\"', '{\"amount\":\"3\",\"currency\":\"GBP\"}')",
		"INSERT INTO " + table + " VALUES ('n', NULL, NULL)",
	} {
		if _, err := d.conn.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	cs := []any{
		T2[any, any]{V0: "id", V1: "text"},
		T2[any, any]{V0: "price", V1: "decimal?"},
		T2[any, any]{V0: "cost", V1: "decimal?|money=GBP"},
	}
	migrate := func() SkyResult[any, any] {
		res, ok := AnyTaskRun(Db_autoMigrate(d, table, cs, "id", []any{})).(SkyResult[any, any])
		if !ok {
			t.Fatalf("Db_autoMigrate returned %T", res)
		}
		return res
	}
	res := migrate()
	if res.Tag == 0 {
		t.Fatalf("a decimal field on a Codec.auto JSON text column was accepted")
	}
	msg := errorMessage(res.ErrValue)
	for _, want := range []string{table + ".price", "Codec.decimal", fixPrice} {
		if !strings.Contains(msg, want) {
			t.Errorf("the Err does not name %q: %s", want, msg)
		}
	}
	if _, err := d.conn.Exec(fixPrice); err != nil {
		t.Fatalf("the named conversion fails: %s: %v", fixPrice, err)
	}
	res = migrate()
	if res.Tag == 0 || !strings.Contains(errorMessage(res.ErrValue), fixCost) {
		t.Fatalf("the Money column was not refused with its conversion: %v", res)
	}
	if _, err := d.conn.Exec(fixCost); err != nil {
		t.Fatalf("the named conversion fails: %s: %v", fixCost, err)
	}
	if res = migrate(); res.Tag != 0 {
		t.Fatalf("migrate refused the converted table: %v", errorMessage(res.ErrValue))
	}
	if _, err := d.conn.Exec("INSERT INTO " + table + " VALUES ('e', '20', '3')"); err != nil {
		t.Fatal(err)
	}
	rows, err := d.conn.Query("SELECT id, COALESCE(CAST(price AS TEXT), 'NULL'), COALESCE(CAST(cost AS TEXT), 'NULL') FROM " + table + " ORDER BY " + orderBy + ", id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id, p, c string
		if err := rows.Scan(&id, &p, &c); err != nil {
			t.Fatal(err)
		}
		got = append(got, id+"="+p+"/"+c)
	}
	want := "a=9.5/1.50 b=10/2 e=20/3 c=100/3 n=NULL/NULL"
	if strings.Join(got, " ") != want {
		t.Fatalf("rows after the conversion = %v, want %s", got, want)
	}
}

func TestStoreMigrateRefusesADecimalFieldOnALegacyJsonColumnSqlite(t *testing.T) {
	d := openStoreQueryDb(t)
	const table = "legacy_dec"
	assertLegacyDecimalColumnRefused(t, d, table,
		`UPDATE legacy_dec SET price = substr(price, 2, length(price) - 2) WHERE price LIKE '"%"'`,
		`UPDATE legacy_dec SET cost = CASE WHEN cost LIKE '{%' THEN (CASE json_type(cost, '$.amount') WHEN 'text' THEN json_extract(cost, '$.amount') ELSE cost -> '$.amount' END) ELSE substr(cost, 2, length(cost) - 2) END WHERE cost LIKE '"%"' OR (cost LIKE '{%' AND upper(json_extract(cost, '$.currency')) = 'GBP')`,
		"price IS NULL, price COLLATE sky_decimal")
}

// A decimal field on a TEXT column that holds only canonical decimals (a
// table Codec.decimal made on SQLite) is a match, not a refusal.
func TestStoreMigrateAcceptsADecimalFieldOnCanonicalTextSqlite(t *testing.T) {
	d := openStoreQueryDb(t)
	const table = "canon_dec"
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table)
	t.Cleanup(func() { _, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table) })
	for _, stmt := range []string{
		"CREATE TABLE " + table + " (id TEXT PRIMARY KEY NOT NULL, price TEXT NOT NULL)",
		"INSERT INTO " + table + " VALUES ('a', '9.5')",
	} {
		if _, err := d.conn.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	cs := []any{T2[any, any]{V0: "id", V1: "text"}, T2[any, any]{V0: "price", V1: "decimal"}}
	res, ok := AnyTaskRun(Db_autoMigrate(d, table, cs, "id", []any{})).(SkyResult[any, any])
	if !ok || res.Tag != 0 {
		t.Fatalf("a decimal field on canonical TEXT was refused: %v", res)
	}
}

// A column name the engine reserves (`index` on SQLite, `user` on
// PostgreSQL) gave a syntax error at the first statement: the Store writes
// names unquoted. migrate and create refuse it on that engine, naming the
// column, and accept it on the engine that allows it (v0.29.2 Judge gap).
func TestStoreMigrateRefusesAnEngineReservedColumnName(t *testing.T) {
	d := openStoreQueryDb(t)
	cs := []any{T2[any, any]{V0: "id", V1: "text"}, T2[any, any]{V0: "index", V1: "int"}}
	for _, verb := range []func() any{
		func() any { return Db_autoMigrate(d, "kw_rows", cs, "id", []any{}) },
		func() any { return Db_createCols(d, "kw_rows", cs, "id", []any{}) },
	} {
		res, ok := AnyTaskRun(verb()).(SkyResult[any, any])
		if !ok || res.Tag == 0 {
			t.Fatalf("a column named index was accepted on SQLite: %v", res)
		}
		msg := errorMessage(res.ErrValue)
		if !strings.Contains(msg, "kw_rows.index") || !strings.Contains(msg, "SQLite") {
			t.Errorf("the Err does not name the column and engine: %s", msg)
		}
	}
	// `user` is reserved on PostgreSQL only: SQLite takes it.
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS kw_users")
	csUser := []any{T2[any, any]{V0: "id", V1: "text"}, T2[any, any]{V0: "user", V1: "text"}}
	if res, ok := AnyTaskRun(Db_autoMigrate(d, "kw_users", csUser, "id", []any{})).(SkyResult[any, any]); !ok || res.Tag != 0 {
		t.Fatalf("a column named user was refused on SQLite: %v", res)
	}
	if err := codecReservedNameError("pgx", "kw_users", csUser); err == nil || !strings.Contains(err.Error(), "PostgreSQL") {
		t.Fatalf("a column named user is not refused for PostgreSQL: %v", err)
	}
}
