package rt

// Std.Db.Store migrate, SQLite arm and pure renderers (v0.29.0, batch C).
//
// User decision D1: `Store.migrate` (Db_autoMigrate) and `sky db migrate
// --gen` (renderMigOp of a v2 op) follow ONE rule. A Maybe field gives a
// nullable column, any other field NOT NULL DEFAULT <declared, else zero>.
// Both paths render an added column through renderAddColumn.
//
// B8: a non-integer PK on SQLite accepted NULL (`notNull && !pk`).
// B9: a declared default was ignored when a column was added.
// B10: renderMigOp addIndex spliced unchecked column names into DDL.

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// --- B8 ---------------------------------------------------------------------

func TestStoreMigrateB8_SqliteTextPkIsNotNull(t *testing.T) {
	d := openStoreQueryDb(t)
	cs := []any{T2[any, any]{V0: "id", V1: "text"}, T2[any, any]{V0: "n", V1: "int"}}
	for _, stmt := range schemaRenderTable("sqlite", codecColspecSchemaMap("b8_items", cs, "id")) {
		if _, err := d.conn.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if _, err := d.conn.Exec("INSERT INTO b8_items (id, n) VALUES (NULL, 1)"); err == nil {
		t.Fatalf("a NULL text primary key was accepted")
	}
}

// --- Std.Db.Schema defaultNow on an integer column ----------------------------
//
// Found bug: `timestamp "at" |> Schema.defaultNow` rendered `DEFAULT now()` on
// a BIGINT column, which PostgreSQL refuses in CREATE TABLE, and SQLite stored
// `datetime('now')` text in an INTEGER column that an Int field cannot read.
// An integer column now takes epoch milliseconds.
func schemaNowTable(name string) map[string]any {
	return map[string]any{"Name": name, "Indexes": []any{}, "Columns": []any{
		map[string]any{"Name": "id", "Kind": "text", "IsPk": true, "DefaultKind": "none", "ForeignKey": ""},
		map[string]any{"Name": "at", "Kind": "timestamp", "IsNotNull": true, "DefaultKind": "now", "ForeignKey": ""},
	}}
}

func assertSchemaNowIsMillis(t *testing.T, d *SkyDb, table string) {
	t.Helper()
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table)
	t.Cleanup(func() { _, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table) })
	for _, s := range schemaRenderTable(d.driver, schemaNowTable(table)) {
		if _, err := d.conn.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if _, err := d.conn.Exec("INSERT INTO " + table + " (id) VALUES ('a')"); err != nil {
		t.Fatal(err)
	}
	var at int64
	if err := d.conn.QueryRow("SELECT at FROM " + table).Scan(&at); err != nil {
		t.Fatalf("at is not an integer: %v", err)
	}
	if now := time.Now().UnixMilli(); at < now-60000 || at > now+60000 {
		t.Fatalf("at = %d, want epoch milliseconds near %d", at, now)
	}
}

func TestStoreMigrateSchemaNowOnTimestampIsMillisSqlite(t *testing.T) {
	assertSchemaNowIsMillis(t, openStoreQueryDb(t), "schema_now_ms")
}

// --- B10 --------------------------------------------------------------------

func TestStoreMigrateB10_AddIndexColumnsAreValidated(t *testing.T) {
	for _, cols := range [][]string{{"a); DROP TABLE x; --"}, {}} {
		op := migOp{Kind: "addIndex", Table: "orders", Name: "ix_orders_x", IndexColumns: cols}
		if s, err := renderMigOp("sqlite", op); err == nil {
			t.Fatalf("columns %q rendered %q, want Err", cols, s)
		}
	}
	if _, err := codecStoreIndexes("orders", []any{map[string]any{
		"Name": "", "Cols": []any{"a b"}, "Unique": false, "Predicate": "",
	}}); err == nil {
		t.Fatalf("a Store index with column \"a b\" was accepted")
	}
}

// --- v1 golden bytes: an applied v1 migration keeps its checksum -------------

func TestStoreMigrateV1OpsKeepTheV0281Bytes(t *testing.T) {
	zero := int64(0)
	create := migOp{Kind: "createTable", Table: "users", Columns: []migColumn{
		{Name: "id", Type: "text", Pk: true},
		{Name: "email", Type: "text", Unique: true},
		{Name: "age", Type: "int", Default: &migDefault{Int: &zero}},
		{Name: "nick", Type: "text", Nullable: true},
	}}
	add := migOp{Kind: "addColumn", Table: "users", Column: "score", Type: "int", Default: &migDefault{Int: &zero}}
	addNull := migOp{Kind: "addColumn", Table: "users", Column: "bio", Type: "text", Nullable: true}
	golden := map[string][]string{
		"sqlite": {
			"CREATE TABLE IF NOT EXISTS users (\n  id TEXT PRIMARY KEY,\n  email TEXT NOT NULL UNIQUE,\n  age INTEGER NOT NULL DEFAULT 0,\n  nick TEXT\n)",
			"ALTER TABLE users ADD COLUMN score INTEGER NOT NULL DEFAULT 0",
			"ALTER TABLE users ADD COLUMN bio TEXT",
		},
		"pgx": {
			"CREATE TABLE IF NOT EXISTS users (\n  id TEXT PRIMARY KEY,\n  email TEXT NOT NULL UNIQUE,\n  age BIGINT NOT NULL DEFAULT 0,\n  nick TEXT\n)",
			"ALTER TABLE users ADD COLUMN score BIGINT NOT NULL DEFAULT 0",
			"ALTER TABLE users ADD COLUMN bio TEXT",
		},
	}
	for driver, want := range golden {
		for i, op := range []migOp{create, add, addNull} {
			got, err := renderMigOp(driver, op)
			if err != nil || got != want[i] {
				t.Errorf("%s op %d: got %q err=%v\nwant %q", driver, i, got, err, want[i])
			}
		}
		text, err := renderMigFile(driver, migFile{Id: "001", Ops: []migOp{create, add}})
		if err != nil || strings.Contains(text, migV2Header) {
			t.Errorf("%s: a v1 file gained the v2 header: %q err=%v", driver, text, err)
		}
	}
}

// --- v2 header and the apply-time stamp -------------------------------------

func TestStoreMigrateV2HeaderAndNowToken(t *testing.T) {
	op := migOp{Kind: "addColumn", V: 2, Table: "orders", Column: "seen_at", Type: "text",
		Default: &migDefault{NowAtApply: true}}
	text, err := renderMigFile("sqlite", migFile{Id: "002", Ops: []migOp{op}})
	if err != nil {
		t.Fatal(err)
	}
	want := migV2Header + "\nALTER TABLE orders ADD COLUMN seen_at TEXT NOT NULL DEFAULT " + migNowToken
	if text != want {
		t.Fatalf("sqlite v2 text:\n got %q\nwant %q", text, want)
	}
	pg, _ := renderMigFile("pgx", migFile{Id: "002", Ops: []migOp{op}})
	if pg != migV2Header+"\nALTER TABLE orders ADD COLUMN seen_at TEXT NOT NULL DEFAULT now()" {
		t.Fatalf("pg v2 text: %q", pg)
	}
	applied := migApplyText(text)
	if strings.Contains(applied, migNowToken) || !regexp.MustCompile(`DEFAULT '\d{4}-\d\d-\d\d \d\d:\d\d:\d\d'$`).MatchString(applied) {
		t.Fatalf("the v2 token was not swapped for the apply time: %q", applied)
	}
	// Hand-written SQL (Db.migrate) that holds the token text but no header
	// runs unchanged.
	raw := "INSERT INTO notes (body) VALUES (" + migNowToken + ")"
	if migApplyText(raw) != raw {
		t.Fatalf("a text without the v2 header was rewritten")
	}
}

func migrateCoreSqlite(t *testing.T, d *SkyDb, pairs []any) SkyResult[any, any] {
	t.Helper()
	r, ok := dbMigrateCore(d, pairs, "", func(_ string, e any) any { return e }).(SkyResult[any, any])
	if !ok {
		t.Fatalf("dbMigrateCore returned a non-Result")
	}
	return r
}

func TestStoreMigrateV2SqliteNowAppliedOnceWithOneChecksum(t *testing.T) {
	d := openStoreQueryDb(t)
	for _, s := range []string{
		"CREATE TABLE orders (id TEXT PRIMARY KEY, status TEXT)",
		"INSERT INTO orders (id, status) VALUES ('o1', 'open')",
	} {
		if _, err := d.conn.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	op := migOp{Kind: "addColumn", V: 2, Table: "orders", Column: "seen_at", Type: "text",
		Default: &migDefault{NowAtApply: true}}
	text, _ := renderMigFile("sqlite", migFile{Id: "002_seen", Ops: []migOp{op}})
	pairs := []any{T2[any, any]{V0: "002_seen", V1: text}}
	if r := migrateCoreSqlite(t, d, pairs); r.Tag != 0 || len(AsList(r.OkValue)) != 1 {
		t.Fatalf("first apply: %+v", r)
	}
	var first string
	if err := d.conn.QueryRow("SELECT seen_at FROM orders WHERE id = 'o1'").Scan(&first); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d$`).MatchString(first) {
		t.Fatalf("existing row holds %q, want the apply time", first)
	}
	time.Sleep(1100 * time.Millisecond)
	if r := migrateCoreSqlite(t, d, pairs); r.Tag != 0 || len(AsList(r.OkValue)) != 0 {
		t.Fatalf("second apply drifted or re-ran: %+v", r)
	}
	var sum string
	_ = d.conn.QueryRow("SELECT checksum FROM _sky_migrations WHERE name = '002_seen'").Scan(&sum)
	if sum != fmt.Sprintf("%x", sha256.Sum256([]byte(text))) {
		t.Fatalf("the ledger checksum is not the checksum of the v2 template")
	}
	var again string
	_ = d.conn.QueryRow("SELECT seen_at FROM orders WHERE id = 'o1'").Scan(&again)
	if again != first {
		t.Fatalf("the row changed from %q to %q", first, again)
	}
}

// A v1 migration applied by v0.28.1 (ledger holds the checksum of its
// golden text) is not seen as drifted by the new renderer.
func TestStoreMigrateV1AppliedMigrationDoesNotDrift(t *testing.T) {
	d := openStoreQueryDb(t)
	zero := int64(0)
	golden := "CREATE TABLE IF NOT EXISTS users (\n  id TEXT PRIMARY KEY,\n  age INTEGER NOT NULL DEFAULT 0\n)"
	for _, s := range []string{
		golden,
		"CREATE TABLE _sky_migrations (name TEXT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)",
		fmt.Sprintf("INSERT INTO _sky_migrations VALUES ('001_users', '%x', '2026-10-01T00:00:00Z')", sha256.Sum256([]byte(golden))),
	} {
		if _, err := d.conn.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	text, err := renderMigFile("sqlite", migFile{Id: "001_users", Ops: []migOp{{Kind: "createTable", Table: "users",
		Columns: []migColumn{{Name: "id", Type: "text", Pk: true}, {Name: "age", Type: "int", Default: &migDefault{Int: &zero}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if r := migrateCoreSqlite(t, d, []any{T2[any, any]{V0: "001_users", V1: text}}); r.Tag != 0 {
		t.Fatalf("an applied v1 migration drifted: %+v", r)
	}
}

// --- D1 on a table with rows (SQLite) ----------------------------------------

func autoMigrate(t *testing.T, d *SkyDb, table string, cs []any, indexes []any) SkyResult[any, any] {
	t.Helper()
	r, ok := AnyTaskRun(Db_autoMigrate(d, table, cs, "id", indexes)).(SkyResult[any, any])
	if !ok {
		t.Fatalf("Db_autoMigrate returned a non-Result")
	}
	return r
}

func cspec(pairs ...string) []any {
	out := []any{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, T2[any, any]{V0: pairs[i], V1: pairs[i+1]})
	}
	return out
}

func TestStoreMigrateD1_SqliteAddsNotNullColumnsToATableWithRows(t *testing.T) {
	d := openStoreQueryDb(t)
	if r := autoMigrate(t, d, "orders", cspec("id", "text", "status", "text"), nil); r.Tag != 0 {
		t.Fatalf("create: %+v", r)
	}
	if _, err := d.conn.Exec("INSERT INTO orders (id, status) VALUES ('o1', 'open')"); err != nil {
		t.Fatal(err)
	}
	cs := cspec("id", "text", "status", "text",
		"qty", "int", "label", "text|dtext=new", "seen_at", "text|dnow", "note", "text?",
		"rank", "int|dint=7", "score", "real|dfloat=1.5", "active", "bool", "payload", "blob")
	r := autoMigrate(t, d, "orders", cs, nil)
	if r.Tag != 0 {
		t.Fatalf("migrate: %+v", r)
	}
	got := []string{}
	for _, s := range AsList(r.OkValue) {
		got = append(got, AsString(s))
	}
	stamp := regexp.MustCompile(`'\d{4}-\d\d-\d\d \d\d:\d\d:\d\d'`)
	want := []string{
		"ALTER TABLE orders ADD COLUMN qty INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE orders ADD COLUMN label TEXT NOT NULL DEFAULT 'new'",
		"ALTER TABLE orders ADD COLUMN seen_at TEXT NOT NULL DEFAULT <now>",
		"ALTER TABLE orders ADD COLUMN note TEXT",
		"ALTER TABLE orders ADD COLUMN rank INTEGER NOT NULL DEFAULT 7",
		"ALTER TABLE orders ADD COLUMN score REAL NOT NULL DEFAULT 1.5",
		"ALTER TABLE orders ADD COLUMN active INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE orders ADD COLUMN payload TEXT",
	}
	if len(got) != len(want) {
		t.Fatalf("statements:\n%s", strings.Join(got, "\n"))
	}
	for i := range want {
		if stamp.ReplaceAllString(got[i], "<now>") != want[i] {
			t.Errorf("statement %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
	var qty, rank int
	var label, seen string
	var note any
	var score float64
	if err := d.conn.QueryRow("SELECT qty, label, seen_at, note, rank, score FROM orders WHERE id = 'o1'").
		Scan(&qty, &label, &seen, &note, &rank, &score); err != nil {
		t.Fatal(err)
	}
	if qty != 0 || label != "new" || note != nil || rank != 7 || score != 1.5 || !stamp.MatchString("'"+seen+"'") {
		t.Fatalf("old row reads qty=%d label=%q seen=%q note=%v rank=%d score=%v", qty, label, seen, note, rank, score)
	}
	if _, err := d.conn.Exec("INSERT INTO orders (id, status, qty) VALUES ('o2', 'x', NULL)"); err == nil {
		t.Fatalf("NULL was accepted in the NOT NULL column qty")
	}
	if r := autoMigrate(t, d, "orders", cs, nil); r.Tag != 0 || len(AsList(r.OkValue)) != 0 {
		t.Fatalf("second migrate is not empty: %+v", r)
	}
}

func TestStoreMigrateD1_SqliteRequiredUniqueOnATableWithRowsIsErr(t *testing.T) {
	d := openStoreQueryDb(t)
	_ = autoMigrate(t, d, "accounts", cspec("id", "text"), nil)
	if _, err := d.conn.Exec("INSERT INTO accounts (id) VALUES ('a1')"); err != nil {
		t.Fatal(err)
	}
	// One row: one default value cannot collide, so the column is added.
	one := openStoreQueryDb(t)
	_ = autoMigrate(t, one, "accounts", cspec("id", "text"), nil)
	if _, err := one.conn.Exec("INSERT INTO accounts (id) VALUES ('a1')"); err != nil {
		t.Fatal(err)
	}
	if r := autoMigrate(t, one, "accounts", cspec("id", "text", "code", "text|u"), nil); r.Tag != 0 {
		t.Fatalf("a required unique column was refused on a table with 1 row: %+v", r)
	}
	if _, err := d.conn.Exec("INSERT INTO accounts (id) VALUES ('a2')"); err != nil {
		t.Fatal(err)
	}
	r := autoMigrate(t, d, "accounts", cspec("id", "text", "code", "text|u"), nil)
	if r.Tag == 0 {
		t.Fatalf("a required unique column was added to a table with rows")
	}
	cols, _ := codecTableColumns(d, "accounts")
	if cols["code"] {
		t.Fatalf("the table was changed")
	}
	// On an empty table it is added, with its unique index.
	e := openStoreQueryDb(t)
	_ = autoMigrate(t, e, "accounts", cspec("id", "text"), nil)
	r = autoMigrate(t, e, "accounts", cspec("id", "text", "code", "text|u"), nil)
	if r.Tag != 0 || len(AsList(r.OkValue)) != 2 {
		t.Fatalf("empty table: %+v", r)
	}
}

// --- indexes ------------------------------------------------------------------

func storeIdx(cols []string, unique bool, pred string) any {
	c := []any{}
	for _, x := range cols {
		c = append(c, x)
	}
	return map[string]any{"Name": "", "Cols": c, "Unique": unique, "Predicate": pred}
}

func TestStoreMigrateIndexes_SqliteCreatedOnceAndPartialUniqueHolds(t *testing.T) {
	d := openStoreQueryDb(t)
	cs := cspec("id", "text", "customer_id", "text", "slug", "text")
	ixs := []any{storeIdx([]string{"customer_id", "id"}, false, ""), storeIdx([]string{"slug"}, true, "slug <> ''")}
	r := autoMigrate(t, d, "orders", cs, ixs)
	if r.Tag != 0 || len(AsList(r.OkValue)) != 3 {
		t.Fatalf("first migrate: %+v", r)
	}
	if r := autoMigrate(t, d, "orders", cs, ixs); r.Tag != 0 || len(AsList(r.OkValue)) != 0 {
		t.Fatalf("second migrate is not empty: %+v", r)
	}
	for _, s := range []string{
		"INSERT INTO orders VALUES ('1', 'c', '')", "INSERT INTO orders VALUES ('2', 'c', '')",
		"INSERT INTO orders VALUES ('3', 'c', 'a')",
	} {
		if _, err := d.conn.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if _, err := d.conn.Exec("INSERT INTO orders VALUES ('4', 'c', 'a')"); err == nil {
		t.Fatalf("the partial unique index let a second 'a' in")
	}
	// An index added to an existing table is created by the next migrate.
	ixs = append(ixs, storeIdx([]string{"slug"}, false, ""))
	if r := autoMigrate(t, d, "orders", cs, ixs); r.Tag != 0 || len(AsList(r.OkValue)) != 1 {
		t.Fatalf("new index: %+v", r)
	}
}

func TestStoreMigrateIndexNamesNeverCollide(t *testing.T) {
	a := codecIndexName("a_b", []string{"c"}, false, "")
	b := codecIndexName("a", []string{"b", "c"}, false, "")
	if a == b {
		t.Fatalf("a_b(c) and a(b, c) share the name %s", a)
	}
	if codecIndexName("t", []string{"c"}, true, "c <> ''") == codecIndexName("t", []string{"c"}, true, "") {
		t.Fatalf("a changed predicate kept the name")
	}
	if n := codecIndexName(strings.Repeat("t", 63), []string{"c"}, false, ""); len(n) > 63 {
		t.Fatalf("index name %d bytes, PostgreSQL keeps 63", len(n))
	}
}

// --- defaultNow on insert (SQLite) -------------------------------------------

func TestStoreMigrateInsertStampsDefaultNowFromTheAppClockOnSqlite(t *testing.T) {
	d := openStoreQueryDb(t)
	_ = autoMigrate(t, d, "events", cspec("id", "text"), nil)
	_, _ = d.conn.Exec("INSERT INTO events (id) VALUES ('old')")
	_ = autoMigrate(t, d, "events", cspec("id", "text", "created_at", "text|dnow"), nil)
	if _, err := d.conn.Exec("UPDATE events SET created_at = '2000-01-01 00:00:00'"); err != nil {
		t.Fatal(err)
	}
	obj := storeObj([]string{"id"}, []any{"new"})
	res, ok := AnyTaskRun(Db_execObjectWith(d, "events", cspec("id", "text", "created_at", "text|dnow"),
		obj, []any{}, "id", "text")).(SkyResult[any, any])
	if !ok || res.Tag != 0 {
		t.Fatalf("insert: %+v", res)
	}
	var at string
	_ = d.conn.QueryRow("SELECT created_at FROM events WHERE id = 'new'").Scan(&at)
	if at == "2000-01-01 00:00:00" || !regexp.MustCompile(`^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d$`).MatchString(at) ||
		!strings.HasPrefix(at, time.Now().UTC().Format("2006-01-02")) {
		t.Fatalf("new row created_at = %q, want the insert time", at)
	}
}

// --- parity harness (D1) --------------------------------------------------------

type parityFixture struct {
	Table    string     `json:"table"`
	Pk       string     `json:"pk"`
	Existing [][]string `json:"existing"`
	Columns  []struct {
		Colspec []string        `json:"colspec"`
		Schema  json.RawMessage `json:"schema"`
		Ops     []migOp         `json:"ops"`
	} `json:"columns"`
}

func loadParity(t *testing.T) parityFixture {
	t.Helper()
	b, err := os.ReadFile("testdata/migrate_parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var f parityFixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

var parityStamp = regexp.MustCompile(`'(\d{4}-\d\d-\d\d \d\d:\d\d:\d\d|__sky_now_at_apply__)'|'__sky_now_ms_at_apply__'|\b\d{13}\b`)

// parityNormalise maps the runtime stamp and the v2 token to one marker and
// drops the v2 header line.
func parityNormalise(stmts []string) string {
	s := strings.TrimPrefix(strings.Join(stmts, ";\n"), migV2Header+"\n")
	return parityStamp.ReplaceAllString(s, "<now>")
}

// renderParityOps renders a column's v2 ops the way `sky db migrate` does.
func renderParityOps(t *testing.T, driver string, ops []migOp) string {
	t.Helper()
	text, err := renderMigFile(driver, migFile{Id: "p", Ops: ops})
	if err != nil {
		t.Fatal(err)
	}
	return parityNormalise([]string{text})
}

// The colspec dumps to the fixture's schema, and for both drivers the pure
// add-column rendering equals renderMigOp of the fixture ops.
func TestStoreMigrateParity_DumpAndRendererAgree(t *testing.T) {
	f := loadParity(t)
	for _, c := range f.Columns {
		var want, got any
		_ = json.Unmarshal(c.Schema, &want)
		b, _ := json.Marshal(codecDumpColumn(c.Colspec[0], c.Colspec[1]))
		_ = json.Unmarshal(b, &got)
		if fmt.Sprint(want) != fmt.Sprint(got) {
			t.Errorf("%v: dump %s, fixture %s", c.Colspec, b, c.Schema)
		}
		for _, driver := range []string{"sqlite", "pgx"} {
			stmts, _, err := renderAddColumn(driver, f.Table, codecColspecMigColumn(c.Colspec[0], c.Colspec[1], f.Pk), sqliteNowLiteral(), false)
			if err != nil {
				t.Fatal(err)
			}
			if a, b := parityNormalise(stmts), renderParityOps(t, driver, c.Ops); a != b {
				t.Errorf("%s %v:\n Store.migrate %q\n migrate file  %q", driver, c.Colspec, a, b)
			}
		}
	}
}

// assertMigrateParity runs the real Db_autoMigrate on a table with a row,
// one fixture column at a time, and compares what it ran with renderMigOp of
// the fixture ops. Shared by the SQLite and PostgreSQL arms.
func assertMigrateParity(t *testing.T, d *SkyDb) {
	t.Helper()
	f := loadParity(t)
	base := []string{}
	for _, e := range f.Existing {
		base = append(base, e[0], e[1])
	}
	for _, c := range f.Columns {
		_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + f.Table)
		if r := autoMigrate(t, d, f.Table, cspec(base...), nil); r.Tag != 0 {
			t.Fatalf("create: %+v", r)
		}
		if _, err := d.conn.Exec("INSERT INTO " + f.Table + " (id, status) VALUES ('o1', 'open')"); err != nil {
			t.Fatal(err)
		}
		r := autoMigrate(t, d, f.Table, cspec(append(append([]string{}, base...), c.Colspec...)...), nil)
		if r.Tag != 0 {
			t.Fatalf("%v: %+v", c.Colspec, r)
		}
		ran := []string{}
		for _, s := range AsList(r.OkValue) {
			ran = append(ran, AsString(s))
		}
		if a, b := parityNormalise(ran), renderParityOps(t, d.driver, c.Ops); a != b {
			t.Errorf("%s %v:\n Store.migrate %q\n migrate file  %q", d.driver, c.Colspec, a, b)
		}
	}
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + f.Table)
}

func TestStoreMigrateParity_SqliteRealDb(t *testing.T) {
	assertMigrateParity(t, openStoreQueryDb(t))
}

// --- A required unique column added by a v2 migration to a table with rows --
//
// Found bug (batch D gap b): `sky db migrate --gen` writes a required unique
// column as an addColumn op (NOT NULL DEFAULT <zero>) and an addIndex op
// (UNIQUE). On a table with 2 or more rows every row took the same default,
// the unique index failed, and `sky db migrate` showed only the raw database
// error ("UNIQUE constraint failed" / "could not create unique index"). The
// apply now names the table, the column and the fix, and still rolls back.
func assertRequiredUniqueOnRowsIsExplained(t *testing.T, d *SkyDb, table string) {
	t.Helper()
	id := "0002_" + table + "_code"
	cleanup := func() {
		_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table)
		_, _ = d.conn.Exec("DELETE FROM _sky_migrations WHERE name = '" + id + "'")
	}
	cleanup()
	t.Cleanup(cleanup)
	for _, s := range []string{
		"CREATE TABLE " + table + " (id TEXT PRIMARY KEY)",
		"INSERT INTO " + table + " (id) VALUES ('a')",
		"INSERT INTO " + table + " (id) VALUES ('b')",
	} {
		if _, err := d.conn.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	zero := ""
	cols := []string{"code"}
	f := migFile{Id: id, Ops: []migOp{
		{Kind: "addColumn", V: 2, Table: table, Column: "code", Type: "text",
			Nullable: false, Default: &migDefault{Text: &zero}},
		{Kind: "addIndex", V: 2, Table: table, Name: codecIndexName(table, cols, true, ""),
			IndexColumns: cols, Unique: true},
	}}
	text, err := renderMigFile(d.driver, f)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := dbMigrateCore(d, []any{T2[any, any]{V0: id, V1: text}}, "",
		func(_ string, e any) any { return e }).(SkyResult[any, any])
	if !ok || r.Tag == 0 {
		t.Fatalf("the migration was applied: %+v", r)
	}
	msg := fmt.Sprintf("%v", r.ErrValue)
	for _, want := range []string{"column " + table + ".code", "required and unique",
		"table " + table + " already has rows", "data step", "Maybe", "rolled back"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the error does not say %q:\n%s", want, msg)
		}
	}
	if cols, _ := codecTableColumns(d, table); cols["code"] {
		t.Fatalf("the column was added: the migration did not roll back")
	}
	var n int
	_ = d.conn.QueryRow("SELECT COUNT(*) FROM _sky_migrations WHERE name = '" + id + "'").Scan(&n)
	if n != 0 {
		t.Fatalf("the failed migration was recorded in the ledger")
	}
}

func TestStoreMigrateSqliteRequiredUniqueOnRowsIsExplained(t *testing.T) {
	assertRequiredUniqueOnRowsIsExplained(t, openStoreQueryDb(t), "codes_ru")
}

// One row takes the default and the unique index holds: no hint, it applies.
func TestStoreMigrateSqliteRequiredUniqueOnOneRowApplies(t *testing.T) {
	d := openStoreQueryDb(t)
	for _, s := range []string{
		"CREATE TABLE codes_one (id TEXT PRIMARY KEY)",
		"INSERT INTO codes_one (id) VALUES ('a')",
	} {
		if _, err := d.conn.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	zero := ""
	cols := []string{"code"}
	text, _ := renderMigFile("sqlite", migFile{Id: "0002_one", Ops: []migOp{
		{Kind: "addColumn", V: 2, Table: "codes_one", Column: "code", Type: "text", Default: &migDefault{Text: &zero}},
		{Kind: "addIndex", V: 2, Table: "codes_one", Name: codecIndexName("codes_one", cols, true, ""),
			IndexColumns: cols, Unique: true},
	}})
	if r := migrateCoreSqlite(t, d, []any{T2[any, any]{V0: "0002_one", V1: text}}); r.Tag != 0 {
		t.Fatalf("apply: %+v", r)
	}
}
