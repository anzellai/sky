//go:build !js

package rt

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// The SQLite conversion of a legacy Money column to a Codec.moneyIn column
// read a JSON-number amount through json_extract, which returns a REAL:
// 0.12345678901234567891 became 0.12345678901234568, and
// 12345678901234567890123 became 1.2345678901234568e+22 (v0.29.2 sixth
// Judge). It reads the number's JSON text now, so the amount is exact.
func TestLegacyMoneyConversionOnSQLiteIsExact(t *testing.T) {
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Exec(`CREATE TABLE legacy (id TEXT, cost TEXT)`); err != nil {
		t.Fatal(err)
	}
	rows := map[string]string{
		"a": `{"amount":0.12345678901234567891,"currency":"GBP"}`,
		"b": `{"amount":12345678901234567890123,"currency":"gbp"}`,
		"c": `{"amount":"12.50","currency":"GBP"}`,
		"d": `"9.5"`,
		"e": `{"amount":"3","currency":"USD"}`,
		"f": `{"amount":-7.25,"currency":"GBP"}`,
	}
	for id, v := range rows {
		if _, err := conn.Exec(`INSERT INTO legacy VALUES (?, ?)`, id, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := conn.Exec(codecLegacyDecimalFix("sqlite", "legacy", "cost", "GBP")); err != nil {
		t.Fatalf("conversion: %v", err)
	}
	want := map[string]string{
		"a": "0.12345678901234567891",
		"b": "12345678901234567890123",
		"c": "12.50",
		"d": "9.5",
		"e": `{"amount":"3","currency":"USD"}`,
		"f": "-7.25",
	}
	for id, w := range want {
		var got string
		var typ string
		if err := conn.QueryRow(`SELECT CAST(cost AS TEXT), typeof(cost) FROM legacy WHERE id = ?`, id).Scan(&got, &typ); err != nil {
			t.Fatal(err)
		}
		if got != w || typ != "text" {
			t.Errorf("row %s: %s (%s), want %s (text)", id, got, typ, w)
		}
	}
}
