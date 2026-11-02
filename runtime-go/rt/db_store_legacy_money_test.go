//go:build !js

package rt

import (
	"strings"
	"testing"
)

// A Codec.auto Money column holds `{"amount":"5.00","currency":"USD"}`. The
// conversion Store.migrate printed for a field moved to Codec.moneyIn GBP
// took `->>'amount'` and dropped the currency, so USD 5.00 became GBP 5.00
// (v0.29.2 second Judge, gap 1). The refusal now names the foreign
// currencies, gives a check query that lists those rows, and a conversion
// that never relabels one: PostgreSQL fails on it, SQLite leaves it (and the
// store refuses the column again). A Codec.decimal field on Money objects
// gets no conversion at all, because it would drop the currency.

// legacySQLAfter returns the statement that follows `marker` in msg, up to
// the next ". " that ends a sentence (or the end of msg).
func legacySQLAfter(t *testing.T, msg, marker string) string {
	t.Helper()
	i := strings.Index(msg, marker)
	if i < 0 {
		t.Fatalf("the Err has no %q: %s", marker, msg)
	}
	rest := msg[i+len(marker):]
	if j := strings.Index(rest, ". Then "); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

// legacyConversion is the conversion statement the refusal ends with.
func legacyConversion(t *testing.T, msg string) string {
	t.Helper()
	i := strings.LastIndex(msg, "ALTER TABLE ")
	if j := strings.LastIndex(msg, "UPDATE "); j > i {
		i = j
	}
	if i < 0 {
		t.Fatalf("the Err gives no conversion: %s", msg)
	}
	return msg[i:]
}

func legacyMigrate(t *testing.T, d *SkyDb, table string, cs []any) SkyResult[any, any] {
	t.Helper()
	res, ok := AnyTaskRun(Db_autoMigrate(d, table, cs, "id", []any{})).(SkyResult[any, any])
	if !ok {
		t.Fatalf("Db_autoMigrate returned %T", res)
	}
	return res
}

func moneyExec(t *testing.T, d *SkyDb, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := d.conn.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

func legacyColumn(t *testing.T, d *SkyDb, table, col string) map[string]string {
	t.Helper()
	rows, err := d.conn.Query("SELECT id, COALESCE(CAST(" + col + " AS TEXT), 'NULL') FROM " + table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, v string
		if err := rows.Scan(&id, &v); err != nil {
			t.Fatal(err)
		}
		out[id] = v
	}
	return out
}

func assertLegacyMoneyKeepsCurrency(t *testing.T, d *SkyDb, table string) {
	t.Helper()
	pg := d.driver == "pgx"
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table)
	t.Cleanup(func() { _, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table) })
	moneyExec(t, d,
		"CREATE TABLE "+table+" (id TEXT PRIMARY KEY NOT NULL, price TEXT, cost TEXT)",
		"INSERT INTO "+table+` VALUES ('a', '"9.5"', '{"amount":"1.50","currency":"GBP"}')`,
		"INSERT INTO "+table+` VALUES ('b', '"10"', '{"amount":"5.00","currency":"USD"}')`,
		"INSERT INTO "+table+` VALUES ('c', '"100"', '"3"')`,
		"INSERT INTO "+table+` VALUES ('d', NULL, '{"amount":"2","currency":"gbp"}')`,
		"INSERT INTO "+table+` VALUES ('e', '20', '4')`,
		"INSERT INTO "+table+` VALUES ('n', NULL, NULL)`,
	)
	cs := []any{
		T2[any, any]{V0: "id", V1: "text"},
		T2[any, any]{V0: "price", V1: "decimal?"},
		T2[any, any]{V0: "cost", V1: "decimal?|money=GBP"},
	}

	// 1. The Codec.decimal column of quoted and bare decimals converts.
	res := legacyMigrate(t, d, table, cs)
	if res.Tag == 0 {
		t.Fatalf("a decimal field on a Codec.auto JSON text column was accepted")
	}
	msg := errorMessage(res.ErrValue)
	if !strings.Contains(msg, table+".price") || !strings.Contains(msg, "Codec.decimal") {
		t.Fatalf("the Err does not name the price column: %s", msg)
	}
	moneyExec(t, d, legacyConversion(t, msg))

	// 2. The Codec.moneyIn GBP column holds one USD row.
	res = legacyMigrate(t, d, table, cs)
	if res.Tag == 0 {
		t.Fatalf("the Money column was accepted")
	}
	msg = errorMessage(res.ErrValue)
	for _, want := range []string{table + ".cost", "Codec.moneyIn GBP", "USD"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the Err does not name %q: %s", want, msg)
		}
	}
	check := legacySQLAfter(t, msg, "(a Money object in another currency than GBP): ")
	rows, err := d.conn.Query(check)
	if err != nil {
		t.Fatalf("the check query fails: %s: %v", check, err)
	}
	var found []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		found = append(found, v)
	}
	rows.Close()
	if len(found) != 1 || !strings.Contains(found[0], "USD") {
		t.Fatalf("the check query found %v, want the one USD row", found)
	}
	fix := legacyConversion(t, msg)
	_, err = d.conn.Exec(fix)
	if pg && (err == nil || !strings.Contains(err.Error(), "not in GBP")) {
		t.Fatalf("the PostgreSQL conversion took a USD row: %s: %v", fix, err)
	}
	if !pg && err != nil {
		t.Fatalf("the SQLite conversion fails: %s: %v", fix, err)
	}
	if got := legacyColumn(t, d, table, "cost")["b"]; !strings.Contains(got, "USD") {
		t.Fatalf("the USD row was relabelled: cost = %s", got)
	}
	if res = legacyMigrate(t, d, table, cs); res.Tag == 0 || !strings.Contains(errorMessage(res.ErrValue), "USD") {
		t.Fatalf("the USD row is not refused after the conversion: %v", res)
	}

	// 3. The user moves the USD row out; the conversion then runs.
	moneyExec(t, d, "DELETE FROM "+table+" WHERE id = 'b'", fix)
	if res = legacyMigrate(t, d, table, cs); res.Tag != 0 {
		t.Fatalf("migrate refused the converted table: %v", errorMessage(res.ErrValue))
	}
	price, cost := legacyColumn(t, d, table, "price"), legacyColumn(t, d, table, "cost")
	got := ""
	for _, id := range []string{"a", "c", "d", "e", "n"} {
		got += id + "=" + price[id] + "/" + cost[id] + " "
	}
	if want := "a=9.5/1.50 c=100/3 d=NULL/2 e=20/4 n=NULL/NULL "; got != want {
		t.Fatalf("rows after the conversion = %q, want %q", got, want)
	}

	// 4. A Codec.decimal field on Money objects: no conversion is given.
	other := table + "_dec"
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + other)
	t.Cleanup(func() { _, _ = d.conn.Exec("DROP TABLE IF EXISTS " + other) })
	moneyExec(t, d,
		"CREATE TABLE "+other+" (id TEXT PRIMARY KEY NOT NULL, total TEXT)",
		"INSERT INTO "+other+` VALUES ('a', '{"amount":"5.00","currency":"USD"}')`,
		"INSERT INTO "+other+` VALUES ('b', '"3"')`,
	)
	csDec := []any{T2[any, any]{V0: "id", V1: "text"}, T2[any, any]{V0: "total", V1: "decimal?"}}
	res = legacyMigrate(t, d, other, csDec)
	if res.Tag == 0 {
		t.Fatalf("a Codec.decimal field on Money objects was accepted")
	}
	msg = errorMessage(res.ErrValue)
	if !strings.Contains(msg, "drop each row's currency") || strings.Contains(msg, "ALTER TABLE") || strings.Contains(msg, "UPDATE ") {
		t.Fatalf("a Codec.decimal field on Money objects was given a conversion: %s", msg)
	}
}

func TestStoreMigrateLegacyMoneyColumnKeepsItsCurrencySqlite(t *testing.T) {
	assertLegacyMoneyKeepsCurrency(t, openStoreQueryDb(t), "legacy_money")
}
