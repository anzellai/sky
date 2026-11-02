//go:build integration
// +build integration

package rt

// Std.Db.Store query path, PostgreSQL arm (v0.29.0, batch A). Runs in the CI
// integration jobs (`-tags integration -run Postgres`), so every test name
// carries "Postgres".
//
// B1: pgx preloads one row. An error after row 1 (here a malformed JSON value
// cast to jsonb) was lost, and the caller got Ok with a short list.
//
// B3: PostgreSQL folds unquoted identifiers to lower case. The Store DDL is
// unquoted, so an autoCamel column `priceMinor` is stored as `priceminor`.
// codecTableColumns then missed it (the second migrate ran ADD COLUMN again),
// and Db_queryObjects looked up `priceMinor` in a row keyed `priceminor`.
//
// B7: codecTableColumns read information_schema.columns by table name only,
// so a same-named table in another schema on the search_path made migrate
// skip the CREATE and ALTER the wrong table.

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func openStoreQueryPostgres(t *testing.T, dsnSuffix string) *SkyDb {
	t.Helper()
	dsn := requirePostgresDSN(t)
	if dsnSuffix != "" {
		if strings.Contains(dsn, "?") {
			dsn += "&" + dsnSuffix
		} else {
			dsn += "?" + dsnSuffix
		}
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.Ping(); err != nil {
		t.Fatalf("postgres unreachable at SKY_TEST_POSTGRES_DSN: %v", err)
	}
	return &SkyDb{conn: conn, driver: "pgx"}
}

func TestStoreQueryPostgresB1_MidIterationErrorIsErr(t *testing.T) {
	d := openStoreQueryPostgres(t, "")
	assertMidIterationErrorSurfaces(t, d, "events_b1_pg",
		"SELECT CAST(props AS jsonb) ->> 'a' AS a FROM events_b1_pg")
}

func TestStoreQueryPostgresB3_CamelCaseColumnsRoundTrip(t *testing.T) {
	d := openStoreQueryPostgres(t, "")
	const table = "orders_b3_pg"
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table)
	t.Cleanup(func() { _, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table) })
	cs := colspec([2]string{"id", "text"}, [2]string{"priceMinor", "int"}, [2]string{"customerId", "text?"})

	for i := 0; i < 2; i++ {
		res, ok := AnyTaskRun(Db_autoMigrate(d, table, cs, "id", []any{})).(SkyResult[any, any])
		if !ok || res.Tag != 0 {
			t.Fatalf("migrate run %d: %v", i+1, res.ErrValue)
		}
		if i == 1 && len(AsList(res.OkValue)) != 0 {
			t.Fatalf("second migrate must apply nothing, applied %v", res.OkValue)
		}
	}
	obj := storeObj([]string{"id", "priceMinor", "customerId"}, []any{"o1", 1250, "c9"})
	if r, _ := AnyTaskRun(Db_execObject(d, table, cs, obj, "id", "text")).(SkyResult[any, any]); r.Tag != 0 {
		t.Fatalf("insert: %v", r.ErrValue)
	}
	res, _ := AnyTaskRun(Db_queryObjects(d, "SELECT * FROM "+table, []any{}, cs)).(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("query: %v", res.ErrValue)
	}
	rows := AsList(res.OkValue)
	want := `{"id":"o1","priceMinor":1250,"customerId":"c9"}`
	if len(rows) != 1 || AsString(rows[0]) != want {
		t.Fatalf("rows = %v, want [%s]", rows, want)
	}
}

func TestStoreQueryPostgresB7_MigrateIgnoresOtherSchemas(t *testing.T) {
	d := openStoreQueryPostgres(t, "search_path=public,sq_b7_other")
	const table = "orders_b7_pg"
	for _, stmt := range []string{
		"DROP TABLE IF EXISTS public." + table,
		"CREATE SCHEMA IF NOT EXISTS sq_b7_other",
		"DROP TABLE IF EXISTS sq_b7_other." + table,
		"CREATE TABLE sq_b7_other." + table + " (id TEXT PRIMARY KEY)",
	} {
		if _, err := d.conn.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		_, _ = d.conn.Exec("DROP TABLE IF EXISTS public." + table)
		_, _ = d.conn.Exec("DROP SCHEMA IF EXISTS sq_b7_other CASCADE")
	})
	cs := colspec([2]string{"id", "text"}, [2]string{"total", "int"})
	res, _ := AnyTaskRun(Db_autoMigrate(d, table, cs, "id", []any{})).(SkyResult[any, any])
	if res.Tag != 0 {
		t.Fatalf("migrate: %v", res.ErrValue)
	}
	var reg sql.NullString
	if err := d.conn.QueryRow("SELECT CAST(to_regclass('public." + table + "') AS TEXT)").Scan(&reg); err != nil {
		t.Fatalf("to_regclass: %v", err)
	}
	if !reg.Valid {
		t.Fatalf("migrate did not create public.%s; it applied %v", table, res.OkValue)
	}
	var n int
	if err := d.conn.QueryRow("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = 'sq_b7_other' AND table_name = '" + table + "'").Scan(&n); err != nil {
		t.Fatalf("count other columns: %v", err)
	}
	if n != 1 {
		t.Fatalf("migrate altered sq_b7_other.%s: it has %d columns, want 1", table, n)
	}
}

// Batch B: the join read kernel on PostgreSQL. The `a__clientId` alias is
// quoted, so PostgreSQL keeps its case and the kernel finds it.
func TestStoreQueryPostgresJoinGroups_StripsPrefixesAndNullsTheMissingSide(t *testing.T) {
	assertJoinGroups(t, openStoreQueryPostgres(t, ""), "_pg")
}

// A Codec.auto Decimal / Money TEXT column under a Codec.decimal /
// Codec.moneyIn field compared as text on PostgreSQL (v0.29.2 Judge gap):
// migrate refuses it with the ALTER that converts it, and the ALTER works.
func TestStoreQueryPostgresLegacyDecimalColumnRefused(t *testing.T) {
	d := openStoreQueryPostgres(t, "")
	assertLegacyDecimalColumnRefused(t, d, "legacy_dec_pg",
		`ALTER TABLE legacy_dec_pg ALTER COLUMN price TYPE NUMERIC USING (CASE WHEN price::text LIKE '{%' THEN ('a Money object is not a decimal: ' || price::text) ELSE trim(both '"' from price::text) END)::numeric`,
		`ALTER TABLE legacy_dec_pg ALTER COLUMN cost TYPE NUMERIC USING (CASE WHEN cost::text LIKE '{%' THEN (CASE WHEN upper(cost::json->>'currency') = 'GBP' THEN cost::json->>'amount' ELSE 'a Money object that is not in GBP: ' || cost::text END) ELSE trim(both '"' from cost::text) END)::numeric`,
		"price IS NULL, price")
}

// A Codec.moneyIn GBP field on a Codec.auto Money column holding a USD row:
// the conversion fails on that row instead of relabelling it GBP (v0.29.2
// second Judge, gap 1).
func TestStoreQueryPostgresLegacyMoneyColumnKeepsItsCurrency(t *testing.T) {
	assertLegacyMoneyKeepsCurrency(t, openStoreQueryPostgres(t, ""), "legacy_money_pg")
}

// `user` is reserved on PostgreSQL: migrate refuses it with the reason
// instead of "syntax error at or near user".
func TestStoreQueryPostgresReservedColumnNameRefused(t *testing.T) {
	d := openStoreQueryPostgres(t, "")
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS kw_users_pg")
	cs := []any{T2[any, any]{V0: "id", V1: "text"}, T2[any, any]{V0: "user", V1: "text"}}
	res, ok := AnyTaskRun(Db_autoMigrate(d, "kw_users_pg", cs, "id", []any{})).(SkyResult[any, any])
	if !ok || res.Tag == 0 {
		t.Fatalf("a column named user was accepted on PostgreSQL: %v", res)
	}
	if msg := errorMessage(res.ErrValue); !strings.Contains(msg, "kw_users_pg.user") || strings.Contains(msg, "syntax error") {
		t.Fatalf("the Err does not name the column: %s", msg)
	}
}

// The PostgreSQL keyword list is measured against the server: every R or T
// keyword of pg_get_keywords() fails as a column and as a table name, and is
// listed (Store.sky sqlKeywords or codecReservedPostgres); a listed name that
// is R or T here fails (v0.29.2 second Judge, gap 5: `system_user`, reserved
// since PostgreSQL 16, was missing).
func TestStoreQueryPostgresKeywordListIsMeasured(t *testing.T) {
	d := openStoreQueryPostgres(t, "")
	both := storeSqlKeywords(t)
	rows, err := d.conn.Query(`SELECT word FROM pg_get_keywords() WHERE catcode IN ('R', 'T') ORDER BY word`)
	if err != nil {
		t.Fatal(err)
	}
	var words []string
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			t.Fatal(err)
		}
		words = append(words, w)
	}
	rows.Close()
	if len(words) < 50 {
		t.Fatalf("pg_get_keywords() gave %d R/T keywords", len(words))
	}
	schema := "kwp_" + strings.ReplaceAll(t.Name(), "/", "_")
	schema = strings.ToLower(schema)
	ctx := context.Background()
	conn, err := d.conn.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = conn.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
	if _, err := conn.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE") }()
	var problems []string
	for _, w := range words {
		_, _ = conn.ExecContext(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		_, _ = conn.ExecContext(ctx, "CREATE SCHEMA "+schema)
		_, _ = conn.ExecContext(ctx, "SET search_path = "+schema)
		col, tab := keywordFails(func(q string) error { _, e := conn.ExecContext(ctx, q); return e }, w)
		if !col || !tab {
			problems = append(problems, w+": R/T keyword that PostgreSQL takes in a position")
		}
		if !both[w] && !codecReservedName("pgx", w, false) {
			problems = append(problems, w+": fails on PostgreSQL and is not listed")
		}
	}
	if len(problems) > 0 {
		t.Fatalf("the PostgreSQL keyword list does not match the server:\n%s", strings.Join(problems, "\n"))
	}
}

func TestStoreQueryPostgresCheckProjectDataMatchesMigrate(t *testing.T) {
	assertCheckProjectDataMatchesMigrate(t, openStoreQueryPostgres(t, ""), "check_data_pg")
}
