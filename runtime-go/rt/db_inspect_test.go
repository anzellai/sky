//go:build !js

package rt

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func inspectTestDb(t *testing.T) *SkyDb {
	t.Helper()
	conn, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "inspect.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return &SkyDb{conn: conn, driver: "sqlite"}
}

func runInspect(t *testing.T, d *SkyDb, migrations string) inspectReport {
	t.Helper()
	r := AnyTaskRun(Db_inspect(d, migrations))
	res, ok := r.(SkyResult[any, any])
	if !ok || res.Tag != 0 {
		t.Fatalf("inspect failed: %#v", r)
	}
	var rep inspectReport
	if err := json.Unmarshal([]byte(AsString(res.OkValue)), &rep); err != nil {
		t.Fatal(err)
	}
	return rep
}

// The catalogue facts of a table that a migration file created: types, NOT
// NULL, defaults, the serial key, a column UNIQUE and a partial index.
func TestInspectSqliteCatalogue(t *testing.T) {
	d := inspectTestDb(t)
	sqlText, err := renderMigFile("sqlite", migFile{Id: "1_init", Ops: []migOp{
		{Kind: "createTable", Table: "users", Columns: []migColumn{
			{Name: "id", Type: "int", Pk: true, Autoinc: true},
			{Name: "email", Type: "text", Unique: true},
			{Name: "nick", Type: "text", Nullable: true},
			{Name: "rank", Type: "int", Default: &migDefault{Int: ptrInt64(7)}},
		}},
		{Kind: "addIndex", Table: "users", Name: "ux_users_live", IndexColumns: []string{"email"},
			Unique: true, Where: "rank > 0"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.conn.Exec(sqlText); err != nil {
		t.Fatal(err)
	}
	rep := runInspect(t, d, "[]")
	if rep.Dialect != "sqlite" || len(rep.Tables) != 1 {
		t.Fatalf("report: %+v", rep)
	}
	u := rep.Tables[0]
	byName := map[string]inspectColumn{}
	for _, c := range u.Columns {
		byName[c.Name] = c
	}
	if c := byName["id"]; !c.Pk || !c.Autoinc || !c.NotNull {
		t.Errorf("id: %+v", c)
	}
	if c := byName["email"]; !c.Unique || !c.NotNull || c.Type != "TEXT" {
		t.Errorf("email: %+v", c)
	}
	if c := byName["nick"]; c.NotNull {
		t.Errorf("nick is nullable: %+v", c)
	}
	if c := byName["rank"]; c.Default == nil || *c.Default != "7" {
		t.Errorf("rank default: %+v", c)
	}
	if len(u.Indexes) != 1 || u.Indexes[0].Name != "ux_users_live" || !u.Indexes[0].Unique ||
		u.Indexes[0].Where != "rank > 0" || len(u.Indexes[0].Columns) != 1 {
		t.Errorf("indexes: %+v", u.Indexes)
	}
	if rep.Ledger.Present {
		t.Errorf("no ledger was created, but the report says one is present")
	}
}

// The ledger states, by the one ledger rule; and inspect never writes: on a
// database with no ledger it does not create `_sky_migrations`.
func TestInspectLedgerStatesAndReadOnly(t *testing.T) {
	d := inspectTestDb(t)
	files := `[{"id":"1_a","ops":[{"kind":"raw","sql":"CREATE TABLE a (x TEXT)"}]},` +
		`{"id":"2_b","ops":[{"kind":"raw","sql":"CREATE TABLE b (x TEXT)"}]}]`
	rep := runInspect(t, d, files)
	if rep.Ledger.Present || len(rep.Ledger.Migrations) != 2 ||
		rep.Ledger.Migrations[0].State != "pending" || rep.Ledger.Migrations[1].State != "pending" {
		t.Fatalf("fresh ledger: %+v", rep.Ledger)
	}
	var n int
	if err := d.conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = '_sky_migrations'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("inspect created the ledger (n=%d, err=%v)", n, err)
	}
	// Apply both files, then edit the first; record a Db.migrate row as well.
	if r := AnyTaskRun(Db_migrateOps(d, files)); r.(SkyResult[any, any]).Tag != 0 {
		t.Fatalf("migrate: %#v", r)
	}
	if _, err := d.conn.Exec(`INSERT INTO _sky_migrations (name, checksum, applied_at, kind) VALUES ('0001_hand', 'x', 'now', 'sql')`); err != nil {
		t.Fatal(err)
	}
	edited := `[{"id":"1_a","ops":[{"kind":"raw","sql":"CREATE TABLE a (y TEXT)"}]},` +
		`{"id":"2_b","ops":[{"kind":"raw","sql":"CREATE TABLE b (x TEXT)"}]},` +
		`{"id":"3_c","ops":[]}]`
	rep = runInspect(t, d, edited)
	got := map[string]string{}
	for _, m := range rep.Ledger.Migrations {
		got[m.Name] = m.State
	}
	if got["1_a"] != "edited" || got["2_b"] != "applied" || got["3_c"] != "pending" {
		t.Errorf("states: %v", got)
	}
	if len(rep.Ledger.Other) != 1 || rep.Ledger.Other[0] != "0001_hand" {
		t.Errorf("other ledger rows: %v", rep.Ledger.Other)
	}
}

func TestIndexPredicateOf(t *testing.T) {
	cases := map[string]string{
		"CREATE INDEX ix ON t (a, b)":                             "",
		"CREATE UNIQUE INDEX ux ON t (a) WHERE deleted IS NULL":   "deleted IS NULL",
		"CREATE INDEX ix ON t (lower(a)) where a <> ')'":          "a <> ')'",
		"CREATE UNIQUE INDEX IF NOT EXISTS u ON t(a)   WHERE b=1": "b=1",
	}
	for ddl, want := range cases {
		if got := indexPredicateOf(ddl); got != want {
			t.Errorf("%q: got %q, want %q", ddl, got, want)
		}
	}
}

func ptrInt64(n int64) *int64 { return &n }

// inspectShapeCase is one live table shape and what the report must say.
type inspectShapeCase struct {
	name    string
	ddl     []string
	table   string
	pk      []string // primary-key columns in key order
	unique  []string // columns the report marks UNIQUE
	indexes []inspectIndex
}

// The index and key shapes a Store cannot see in a column list, each read as
// what it is (v0.29.2 Judge: a multi-column UNIQUE constraint was skipped, a
// composite key kept its first column only, an expression column was dropped
// and `COLLATE NOCASE` lost, and `sky db check` then said "matches").
var inspectShapeCases = []inspectShapeCase{
	{name: "multi-column UNIQUE constraint",
		ddl:   []string{`CREATE TABLE ext2 (code TEXT PRIMARY KEY, name TEXT NOT NULL, tenant INTEGER NOT NULL, UNIQUE (tenant, name))`},
		table: "ext2", pk: []string{"code"},
		indexes: []inspectIndex{{Name: "sqlite_autoindex_ext2_2", Columns: []string{"tenant", "name"}, Unique: true, Constraint: true}}},
	{name: "single-column UNIQUE constraint is the column's",
		ddl:   []string{`CREATE TABLE u1 (id INTEGER PRIMARY KEY, email TEXT NOT NULL UNIQUE)`},
		table: "u1", pk: []string{"id"}, unique: []string{"email"}},
	{name: "UNIQUE constraint with COLLATE NOCASE",
		ddl:   []string{`CREATE TABLE u2 (id INTEGER PRIMARY KEY, email TEXT NOT NULL, UNIQUE (email COLLATE NOCASE))`},
		table: "u2", pk: []string{"id"},
		indexes: []inspectIndex{{Name: "sqlite_autoindex_u2_1", Columns: []string{"email"}, Unique: true, Constraint: true, Inexact: "column email has COLLATE NOCASE"}}},
	{name: "an inherited NOCASE collation still cannot be declared by a Store",
		ddl:   []string{`CREATE TABLE u3 (id INTEGER PRIMARY KEY, email TEXT COLLATE NOCASE NOT NULL)`, `CREATE UNIQUE INDEX ux_u3 ON u3 (email)`},
		table: "u3", pk: []string{"id"},
		indexes: []inspectIndex{{Name: "ux_u3", Columns: []string{"email"}, Unique: true, Inexact: "column email has COLLATE NOCASE"}}},
	{name: "an inherited NOCASE UNIQUE constraint cannot become a plain unique column",
		ddl:   []string{`CREATE TABLE u6 (id INTEGER PRIMARY KEY, email TEXT COLLATE NOCASE NOT NULL UNIQUE)`},
		table: "u6", pk: []string{"id"},
		indexes: []inspectIndex{{Name: "sqlite_autoindex_u6_1", Columns: []string{"email"}, Unique: true, Constraint: true, Inexact: "column email has COLLATE NOCASE"}}},
	{name: "unique index with COLLATE NOCASE",
		ddl:   []string{`CREATE TABLE u4 (id INTEGER PRIMARY KEY, email TEXT NOT NULL)`, `CREATE UNIQUE INDEX ux_u4 ON u4 (email COLLATE NOCASE)`},
		table: "u4", pk: []string{"id"},
		indexes: []inspectIndex{{Name: "ux_u4", Columns: []string{"email"}, Unique: true, Inexact: "column email has COLLATE NOCASE"}}},
	{name: "expression index",
		ddl:   []string{`CREATE TABLE ext5 (id INTEGER PRIMARY KEY, name TEXT NOT NULL, tenant INTEGER NOT NULL)`, `CREATE UNIQUE INDEX u5 ON ext5 (lower(name), tenant)`},
		table: "ext5", pk: []string{"id"},
		indexes: []inspectIndex{{Name: "u5", Columns: []string{"<expression>", "tenant"}, Unique: true, Inexact: "an expression column"}}},
	{name: "DESC index",
		ddl:   []string{`CREATE TABLE d1 (id INTEGER PRIMARY KEY, at INTEGER NOT NULL)`, `CREATE INDEX ix_d1 ON d1 (at DESC)`},
		table: "d1", pk: []string{"id"},
		indexes: []inspectIndex{{Name: "ix_d1", Columns: []string{"at"}, Inexact: "column at is DESC"}}},
	{name: "composite primary key",
		ddl:   []string{`CREATE TABLE ext4 (a INTEGER NOT NULL, b INTEGER NOT NULL, v TEXT, PRIMARY KEY (b, a))`},
		table: "ext4", pk: []string{"b", "a"}},
	{name: "plain multi-column unique index",
		ddl:   []string{`CREATE TABLE p1 (id INTEGER PRIMARY KEY, x INTEGER NOT NULL, y INTEGER NOT NULL)`, `CREATE UNIQUE INDEX ux_p1 ON p1 (x, y)`},
		table: "p1", pk: []string{"id"},
		indexes: []inspectIndex{{Name: "ux_p1", Columns: []string{"x", "y"}, Unique: true}}},
}

func TestInspectSqliteIndexAndKeyShapes(t *testing.T) {
	for _, c := range inspectShapeCases {
		t.Run(c.name, func(t *testing.T) {
			d := inspectTestDb(t)
			for _, s := range c.ddl {
				if _, err := d.conn.Exec(s); err != nil {
					t.Fatalf("%s: %v", s, err)
				}
			}
			rep := runInspect(t, d, "[]")
			checkInspectShape(t, rep, c, false)
		})
	}
}

// checkInspectShape compares a report with a case. `pg` compares the index
// columns and flags only: PostgreSQL names constraint indexes its own way.
func checkInspectShape(t *testing.T, rep inspectReport, c inspectShapeCase, pg bool) {
	t.Helper()
	var tbl *inspectTable
	for i := range rep.Tables {
		if rep.Tables[i].Name == c.table {
			tbl = &rep.Tables[i]
		}
	}
	if tbl == nil {
		t.Fatalf("table %s not reported: %+v", c.table, rep.Tables)
	}
	pk := make([]string, len(tbl.Columns))
	n := 0
	var unique []string
	for _, col := range tbl.Columns {
		if col.Pk != (col.PkPos > 0) {
			t.Errorf("column %s: pk %v but pkPos %d", col.Name, col.Pk, col.PkPos)
		}
		if col.PkPos > 0 && col.PkPos <= len(pk) {
			pk[col.PkPos-1] = col.Name
			n++
		}
		if col.Unique {
			unique = append(unique, col.Name)
		}
	}
	if got := strings.Join(pk[:n], ","); got != strings.Join(c.pk, ",") {
		t.Errorf("primary key = (%s), want (%s)", got, strings.Join(c.pk, ","))
	}
	if strings.Join(unique, ",") != strings.Join(c.unique, ",") {
		t.Errorf("unique columns = %v, want %v", unique, c.unique)
	}
	if len(tbl.Indexes) != len(c.indexes) {
		t.Fatalf("indexes = %+v, want %+v", tbl.Indexes, c.indexes)
	}
	for i, want := range c.indexes {
		got := tbl.Indexes[i]
		if (!pg && got.Name != want.Name) || got.Unique != want.Unique ||
			got.Constraint != want.Constraint || (got.Inexact == "") != (want.Inexact == "") ||
			(!pg && got.Inexact != want.Inexact) ||
			(!pg || want.Inexact == "") && strings.Join(got.Columns, ",") != strings.Join(want.Columns, ",") {
			t.Errorf("index %d = %+v, want %+v", i, got, want)
		}
	}
}

func TestSqliteColumnCollations(t *testing.T) {
	got := sqliteColumnCollations(`CREATE TABLE "t" ("Email" TEXT COLLATE nocase NOT NULL, name TEXT, ` +
		"`n2` TEXT DEFAULT 'a,(b' COLLATE RTRIM, CONSTRAINT c UNIQUE (name COLLATE NOCASE))")
	if got["email"] != "NOCASE" || got["n2"] != "RTRIM" || got["name"] != "" || len(got) != 2 {
		t.Errorf("collations = %v", got)
	}
}

func TestPostgresIndexEnforcementIsNotPlain(t *testing.T) {
	if got := pgIndexInexact(pgIndexFeatures{nullsNotDistinct: true}, "btree", "u"); got != "NULLS NOT DISTINCT" {
		t.Errorf("NULLS NOT DISTINCT index: got %q", got)
	}
	if got := pgIndexInexact(pgIndexFeatures{}, "btree", "u"); got != "" {
		t.Errorf("ordinary unique index: got %q, want exact", got)
	}
	for _, c := range []struct {
		features pgIndexFeatures
		want string
	}{
		{pgIndexFeatures{deferrable: true}, "a deferrable constraint"},
		{pgIndexFeatures{unusable: true}, "an index that is not valid, ready and live"},
	} {
		if got := pgIndexInexact(c.features, "btree", "u"); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
}
