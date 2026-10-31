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
