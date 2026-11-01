//go:build !js

package rt

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// The decimal column kind (Codec.decimal, Codec.moneyIn): DDL, write, read,
// the SQLite collation and sum, the migrate defaults, and the classified
// CurrencyMismatch.

func TestDecimalColumnDDLPerDialect(t *testing.T) {
	if got := schemaTypeName("pgx", codecColKindToSchema("decimal")); got != "NUMERIC" {
		t.Errorf("PostgreSQL decimal DDL = %s, want NUMERIC", got)
	}
	if got := schemaTypeName("sqlite", codecColKindToSchema("decimal?")); got != "TEXT" {
		t.Errorf("SQLite decimal DDL = %s, want TEXT", got)
	}
}

func TestDecimalColumnWriteAndRead(t *testing.T) {
	writes := []struct {
		raw  any
		want string
	}{
		{"12.50", "12.5"},
		{json.Number("0.1"), "0.1"},
		{7, "7"},
		{int64(-3), "-3"},
		{"-0.000000001", "-0.000000001"},
	}
	for _, w := range writes {
		got, err := rawToSqlArg(w.raw, "decimal", "price")
		if err != nil || got != w.want {
			t.Errorf("write %v = %v, %v; want %s", w.raw, got, err, w.want)
		}
	}
	for _, bad := range []any{1.5, "1e3", "abc", true, map[string]any{"amount": "1", "currency": "USD"}} {
		if got, err := rawToSqlArg(bad, "decimal", "price"); err == nil {
			t.Errorf("write %v must be refused, got %v", bad, got)
		}
	}
	reads := map[string]string{
		"12.30":     "12.3",  // PostgreSQL NUMERIC text
		"12.3":      "12.3",  // the canonical text on SQLite
		`"12.34"`:   "12.34", // a v0.23-v0.29.1 Codec.auto blob column
		"100":       "100",
		"-0.500000": "-0.5",
	}
	for in, want := range reads {
		got, err := rowValToJsonRaw(in, true, "decimal", "price")
		if err != nil || got != want {
			t.Errorf("read %q = %v, %v; want %s", in, got, err, want)
		}
	}
	for _, bad := range []string{"NaN", "1e5", "x"} {
		if _, err := rowValToJsonRaw(bad, true, "decimal", "price"); err == nil {
			t.Errorf("read %q must be an error", bad)
		}
	}
}

func TestSqliteDecimalCollationAndSum(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE p (v TEXT)`); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"10", "9.5", "-1", "0.25", `"3"`, "0.1", "0.2"} {
		if _, err := db.Exec(`INSERT INTO p (v) VALUES (?)`, v); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.Query(`SELECT v FROM p ORDER BY v COLLATE sky_decimal`)
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		order = append(order, s)
	}
	rows.Close()
	if got := strings.Join(order, " "); got != `-1 0.1 0.2 0.25 "3" 9.5 10` {
		t.Errorf("sky_decimal order = %s", got)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM p WHERE v COLLATE sky_decimal > ?`, "9.50").Scan(&n); err != nil || n != 1 {
		t.Errorf("v > 9.50 matched %d rows (%v), want 1", n, err)
	}
	var sum string
	if err := db.QueryRow(`SELECT COALESCE(sky_decimal_sum(v), '0') FROM p`).Scan(&sum); err != nil {
		t.Fatal(err)
	}
	if sum != "22.05" {
		t.Errorf("sky_decimal_sum = %s, want 22.05 (exact: 0.1 + 0.2 is 0.3)", sum)
	}
	if err := db.QueryRow(`SELECT COALESCE(sky_decimal_sum(v), '0') FROM p WHERE 1 = 0`).Scan(&sum); err != nil || sum != "0" {
		t.Errorf("sum of no rows = %q, %v; want 0", sum, err)
	}
	if _, err := db.Exec(`INSERT INTO p (v) VALUES ('junk')`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT sky_decimal_sum(v) FROM p`).Scan(&sum); err == nil {
		t.Errorf("sky_decimal_sum over junk must fail, got %s", sum)
	}
}

func TestDecimalMigrateDefaults(t *testing.T) {
	z := migZeroDefault("decimal")
	if z == nil || z.Text == nil || *z.Text != "0" {
		t.Fatalf("decimal zero default = %+v, want text 0", z)
	}
	good := "12.5"
	if err := migDefaultFits("t", migColumn{Name: "p", Type: "decimal", Default: &migDefault{Text: &good}}); err != nil {
		t.Errorf("a decimal text default must fit: %v", err)
	}
	bad := "abc"
	if err := migDefaultFits("t", migColumn{Name: "p", Type: "decimal", Default: &migDefault{Text: &bad}}); err == nil {
		t.Error("a non-decimal text default must not fit a decimal column")
	}
	i := int64(3)
	if err := migDefaultFits("t", migColumn{Name: "p", Type: "decimal", Default: &migDefault{Int: &i}}); err != nil {
		t.Errorf("an int default must fit a decimal column: %v", err)
	}
	stmts, err := renderAddColumn("pgx", "t", migColumn{Name: "p", Type: "decimal"}, "", 1)
	if err != nil || len(stmts) == 0 || !strings.Contains(stmts[0], "NUMERIC NOT NULL DEFAULT '0'") {
		t.Errorf("PostgreSQL add decimal = %v, %v", stmts, err)
	}
}

func TestMoneyCurrencyMismatchIsClassified(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("Money_currencyMismatch must fail loudly")
		}
		if kind, _ := classifyPanic(r.(string)); kind != "CurrencyMismatch" {
			t.Errorf("classified as %s, want CurrencyMismatch", kind)
		}
	}()
	Money_currencyMismatch("add", "GBP", "USD")
}
