//go:build !js

package rt

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
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
