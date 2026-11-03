//go:build integration
// +build integration

package rt

// Real-PostgreSQL arm of the Int cast (db_pgx_int_cast.go): an Int compared
// with a TEXT, NUMERIC, INTEGER, SMALLINT or BIGINT column, one value or a
// long list, on the simple protocol the app pool runs and on the extended
// one. Before v0.29.2 an out-of-range value on INTEGER / SMALLINT was an
// "out of range" error; the fifth Judge's unconditional `CAST(? AS BIGINT)`
// fixed that and broke a TEXT column ("operator does not exist: text =
// bigint"). Every column must answer, with the rows SQL's own comparison
// gives, and the cast must keep the column's index.
//
//	SKY_TEST_POSTGRES_DSN=postgres://… go test -tags integration ./rt/ -run PgIntCast

import (
	"database/sql"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const pgIntCastTable = "b292_intcast"

func openPgIntCastDb(t *testing.T, simple bool) (*sql.DB, *SkyDb) {
	t.Helper()
	dsn := requirePostgresDSN(t)
	if simple {
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			t.Fatalf("parse dsn: %v", err)
		}
		cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
		dsn = stdlib.RegisterConnConfig(cfg)
	}
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	if err := conn.Ping(); err != nil {
		t.Fatalf("postgres unreachable at SKY_TEST_POSTGRES_DSN: %v", err)
	}
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS ` + pgIntCastTable,
		`DROP DOMAIN IF EXISTS b292_small_dom`,
		`CREATE DOMAIN b292_small_dom AS SMALLINT`,
		`CREATE TABLE ` + pgIntCastTable + ` (
			id  INTEGER PRIMARY KEY,
			t   TEXT,
			nu  NUMERIC,
			i4  INTEGER,
			i2  SMALLINT,
			i8  BIGINT,
			dm  b292_small_dom
		)`,
		`INSERT INTO ` + pgIntCastTable + ` VALUES
			(1, '5', 5, 5, 5, 5, 5),
			(2, '6', 6, 6, 6, 6, 6),
			(3, '1500', 1500, 1500, 1500, 1500, 1500)`,
		`CREATE INDEX b292_intcast_i4 ON ` + pgIntCastTable + ` (i4)`,
		`CREATE INDEX b292_intcast_i2 ON ` + pgIntCastTable + ` (i2)`,
		`CREATE INDEX b292_intcast_i8 ON ` + pgIntCastTable + ` (i8)`,
		`CREATE INDEX b292_intcast_nu ON ` + pgIntCastTable + ` (nu)`,
		`CREATE INDEX b292_intcast_t ON ` + pgIntCastTable + ` (t)`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		conn.Exec(`DROP TABLE IF EXISTS ` + pgIntCastTable)
		conn.Exec(`DROP DOMAIN IF EXISTS b292_small_dom`)
		pgIntCacheReset(conn)
		conn.Close()
	})
	return conn, &SkyDb{conn: conn, driver: "pgx"}
}

func pgIntCastCount(t *testing.T, d *SkyDb, where string, args ...any) (int, error) {
	t.Helper()
	var n int
	err := d.executor().QueryRow(d.rebind(`SELECT COUNT(*) FROM `+pgIntCastTable+` WHERE `+where), args...).Scan(&n)
	return n, err
}

func intMark(col string) string {
	return "?/*sky-int:" + pgIntCastTable + "|" + col + "*/"
}

func TestPgIntCast_EveryColumnTypeAnswers(t *testing.T) {
	for _, simple := range []bool{true, false} {
		_, d := openPgIntCastDb(t, simple)
		long := make([]any, 0, 1003)
		for v := int64(1000); v <= 2000; v++ {
			long = append(long, v)
		}
		long = append(long, int64(5), int64(99999999999))
		listMark := func(col string) string {
			return "?/*sky-list:" + strconv.Itoa(len(long)) + ":int:" + pgIntCastTable + "|" + col + "*/"
		}
		for _, col := range []string{"t", "nu", "i4", "i2", "i8", "dm"} {
			cases := []struct {
				name  string
				where string
				args  []any
				want  int
			}{
				{"eq", col + " = " + intMark(col), []any{int64(5)}, 1},
				{"eq out of range", col + " = " + intMark(col), []any{int64(99999999999)}, 0},
				{"lt out of range", col + " < " + intMark(col), []any{int64(99999999999)}, 3},
				{"short in", col + " IN (" + intMark(col) + ", " + intMark(col) + ")", []any{int64(5), int64(99999999999)}, 1},
				{"long in", col + " = ANY(" + listMark(col) + ")", long, 2},
				{"not long in", "NOT (" + col + " = ANY(" + listMark(col) + "))", long, 1},
			}
			for _, c := range cases {
				got, err := pgIntCastCount(t, d, c.where, c.args...)
				if err != nil {
					t.Errorf("simple=%v %s %s: %v", simple, col, c.name, err)
					continue
				}
				if got != c.want {
					t.Errorf("simple=%v %s %s: %d rows, want %d", simple, col, c.name, got, c.want)
				}
			}
		}
	}
}

// The cast goes only where it is needed, and keeps the index.
func TestPgIntCast_CastsOnlyNarrowIntegerColumnsAndKeepsTheIndex(t *testing.T) {
	conn, d := openPgIntCastDb(t, true)
	for col, want := range map[string]bool{"t": false, "nu": false, "i4": true, "i2": true, "i8": false, "dm": true} {
		q, marked := pgIntCastQuery(conn, conn, d.rebind(col+" = "+intMark(col)))
		if !marked {
			t.Fatalf("%s: the mark was not found in %q", col, q)
		}
		if got := strings.HasPrefix(q, col+" = CAST($1/*sky-int:"); got != want {
			t.Errorf("%s: cast=%v, want %v: %q", col, got, want, q)
		}
	}
	tx, err := conn.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	txd := &SkyDb{conn: conn, tx: tx, driver: "pgx"}
	for _, col := range []string{"i4", "i2", "i8", "nu", "t"} {
		for _, where := range []string{col + " = " + intMark(col), col + " < " + intMark(col)} {
			rows, err := txd.executor().Query(txd.rebind(`EXPLAIN SELECT id FROM `+pgIntCastTable+` WHERE `+where), int64(5))
			if err != nil {
				t.Fatalf("explain %s: %v", where, err)
			}
			var plan []string
			for rows.Next() {
				var line string
				rows.Scan(&line)
				plan = append(plan, line)
			}
			rows.Close()
			if !strings.Contains(strings.Join(plan, "\n"), "b292_intcast_"+col) {
				t.Errorf("%s: the plan does not use the column's index:\n%s", where, strings.Join(plan, "\n"))
			}
		}
	}
}

// A column whose type another process changed: the stale entry costs one
// failed statement, which clears the cache, and the next one answers.
func TestPgIntCast_StaleEntryHealsAfterOneError(t *testing.T) {
	_, d := openPgIntCastDb(t, true)
	if n, err := pgIntCastCount(t, d, "t = "+intMark("t"), int64(5)); err != nil || n != 1 {
		t.Fatalf("text column: %d, %v", n, err)
	}
	other, err := sql.Open("pgx", requirePostgresDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Exec(`ALTER TABLE ` + pgIntCastTable + ` ALTER COLUMN t TYPE INTEGER USING t::integer`); err != nil {
		t.Fatal(err)
	}
	// The cache still says TEXT (no cast): the out-of-range value fails once.
	if _, err := pgIntCastCount(t, d, "t = "+intMark("t"), int64(99999999999)); err == nil {
		t.Fatalf("expected the stale entry to fail once")
	}
	if n, err := pgIntCastCount(t, d, "t = "+intMark("t"), int64(99999999999)); err != nil || n != 0 {
		t.Fatalf("after the failure the cache must be re-read: %d, %v", n, err)
	}
	// A schema statement through the handle clears the cache at once.
	if _, err := d.executor().Exec(`ALTER TABLE ` + pgIntCastTable + ` ALTER COLUMN t TYPE TEXT`); err != nil {
		t.Fatal(err)
	}
	if n, err := pgIntCastCount(t, d, "t = "+intMark("t"), int64(5)); err != nil || n != 1 {
		t.Fatalf("after ALTER through the handle: %d, %v", n, err)
	}
}

// A domain over a domain over INTEGER is an INTEGER column: the cast follows
// the domain chain to its base type at any depth (Judge round 7). Before,
// one level was resolved, and an Int condition with a value beyond 32 bits
// failed with SQLSTATE 22003 on eq, gt and inList, short and long.
func TestPgIntCast_NestedDomainIsFollowedToItsBaseType(t *testing.T) {
	dsn := requirePostgresDSN(t)
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	const table = "b292b_nested_dom"
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS ` + table,
		`DROP DOMAIN IF EXISTS b292b_d3`,
		`DROP DOMAIN IF EXISTS b292b_d2`,
		`DROP DOMAIN IF EXISTS b292b_d1`,
		`CREATE DOMAIN b292b_d1 AS INTEGER`,
		`CREATE DOMAIN b292b_d2 AS b292b_d1`,
		`CREATE DOMAIN b292b_d3 AS b292b_d2`,
		`CREATE TABLE ` + table + ` (id INTEGER PRIMARY KEY, dd b292b_d2, ddd b292b_d3)`,
		`INSERT INTO ` + table + ` VALUES (1, 5, 5), (2, 6, 6)`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		conn.Exec(`DROP TABLE IF EXISTS ` + table)
		conn.Exec(`DROP DOMAIN IF EXISTS b292b_d3`)
		conn.Exec(`DROP DOMAIN IF EXISTS b292b_d2`)
		conn.Exec(`DROP DOMAIN IF EXISTS b292b_d1`)
		pgIntCacheReset(conn)
		conn.Close()
	})
	mark := func(col string) string { return "?/*sky-int:" + table + "|" + col + "*/" }
	d := &SkyDb{conn: conn, driver: "pgx"}
	cols, err := pgReadNarrowIntColumns(conn, table)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"dd", "ddd"} {
		if !cols[c] {
			t.Errorf("%s: a domain chain over INTEGER is not read as a narrow integer: %v", c, cols)
		}
	}
	for _, c := range []string{"dd", "ddd"} {
		for _, tc := range []struct {
			where string
			args  []any
			want  int
		}{
			{c + " = " + mark(c), []any{int64(3000000000)}, 0},
			{c + " > " + mark(c), []any{int64(3000000000)}, 0},
			{c + " < " + mark(c), []any{int64(3000000000)}, 2},
			{c + " = " + mark(c), []any{int64(5)}, 1},
		} {
			var got int
			if err := d.executor().QueryRow(d.rebind(`SELECT count(*) FROM `+table+` WHERE `+tc.where), tc.args...).Scan(&got); err != nil {
				t.Errorf("%s: %v", tc.where, err)
				continue
			}
			if got != tc.want {
				t.Errorf("%s: %d rows, want %d", tc.where, got, tc.want)
			}
		}
	}
}
