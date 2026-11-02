//go:build !js

package rt

import "testing"

// The runtime's legacy-column conversion and `sky db check`'s
// (rust/crates/sky/src/db_check.rs legacy_decimal_fix_pg /
// legacy_money_check_pg) are one statement: both pin these strings, and
// TestStoreQueryPostgresLegacyDecimalColumnRefused runs them on PostgreSQL.
func TestLegacyDecimalConversionMatchesDbCheck(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{codecLegacyDecimalFix("pgx", "legacy_dec_pg", "price", ""),
			`ALTER TABLE legacy_dec_pg ALTER COLUMN price TYPE NUMERIC USING (CASE WHEN price::text LIKE '{%' THEN ('a Money object is not a decimal: ' || price::text) ELSE trim(both '"' from price::text) END)::numeric`},
		{codecLegacyDecimalFix("pgx", "legacy_dec_pg", "cost", "GBP"),
			`ALTER TABLE legacy_dec_pg ALTER COLUMN cost TYPE NUMERIC USING (CASE WHEN cost::text LIKE '{%' THEN (CASE WHEN upper(cost::json->>'currency') = 'GBP' THEN cost::json->>'amount' ELSE 'a Money object that is not in GBP: ' || cost::text END) ELSE trim(both '"' from cost::text) END)::numeric`},
		{codecLegacyMoneyCheck("pgx", "legacy_dec_pg", "cost", "GBP"),
			`SELECT cost FROM legacy_dec_pg WHERE cost::text LIKE '{%' AND upper(cost::json->>'currency') IS DISTINCT FROM 'GBP'`},
	} {
		if c.got != c.want {
			t.Errorf("got  %s\nwant %s", c.got, c.want)
		}
	}
	if codecColCurrency("decimal?|money=GBP|u") != "GBP" || codecColCurrency("decimal|money=G'P") != "" ||
		codecColCurrency("decimal|money=USDT") != "USDT" || codecColCurrency("decimal|money=USDC") != "USDC" ||
		codecColCurrency("decimal|money=BTC") != "BTC" || codecColCurrency("decimal|money=gbp") != "" ||
		codecColCurrency("decimal|money=ABCDEFGHIJK") != "" {
		t.Fatalf("codecColCurrency reads the wrong flag")
	}
}

// Codec.moneyIn's column carries its currency into the colspec kind, with a
// nullable `?` before the flag (Std.Codec / Std.Db.Store colTypeKind).
func TestCodecColTypeKindCarriesTheCurrency(t *testing.T) {
	money := SkyADT{SkyName: "CMoneyIn", Fields: []any{"GBP"}}
	for _, c := range []struct {
		ct   any
		want string
	}{
		{SkyADT{SkyName: "CText"}, "text"},
		{SkyADT{SkyName: "CDecimal"}, "decimal"},
		{money, "decimal|money=GBP"},
		{SkyADT{SkyName: "CNull", Fields: []any{money}}, "decimal?|money=GBP"},
		{SkyADT{SkyName: "CNull", Fields: []any{SkyADT{SkyName: "CInt"}}}, "int?"},
	} {
		if got := codecColTypeKind(c.ct); got != c.want {
			t.Errorf("codecColTypeKind(%v) = %q, want %q", c.ct, got, c.want)
		}
	}
	if base, nullable := codecSplitKind("decimal?|money=GBP"); base != "decimal" || !nullable {
		t.Fatalf("codecSplitKind = %q %v", base, nullable)
	}
}
