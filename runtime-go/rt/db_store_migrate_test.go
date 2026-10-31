package rt

// Std.Db.Store migrate, SQLite arm and pure renderers (v0.29.0, batch C).
//
// User decision D1: `Store.migrate` (Db_autoMigrate) and `sky db migrate
// --gen` (renderMigOp of an op) follow ONE rule. A Maybe field gives a
// nullable column, any other field NOT NULL DEFAULT <declared, else zero>.
// Both paths render an added column through renderAddColumn.
//
// B8: a non-integer PK on SQLite accepted NULL (`notNull && !pk`).
// B9: a declared default was ignored when a column was added.
// B10: renderMigOp addIndex spliced unchecked column names into DDL.

import (
	"database/sql"
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

// --- One renderer for every op file: no header, the stamp at apply ----------

func TestStoreMigrateOpFileHasNoHeaderAndSwapsNowAtApply(t *testing.T) {
	op := migOp{Kind: "addColumn", Table: "orders", Column: "seen_at", Type: "text",
		Default: &migDefault{NowAtApply: true}}
	text, err := renderMigFile("sqlite", migFile{Id: "002", Ops: []migOp{op}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "ALTER TABLE orders ADD COLUMN seen_at TEXT NOT NULL DEFAULT " + migNowToken; text != want {
		t.Fatalf("sqlite text:\n got %q\nwant %q", text, want)
	}
	if pg, _ := renderMigFile("pgx", migFile{Id: "002", Ops: []migOp{op}}); pg != "ALTER TABLE orders ADD COLUMN seen_at TEXT NOT NULL DEFAULT now()" {
		t.Fatalf("pg text: %q", pg)
	}
	applied := migApplyText(text)
	if strings.Contains(applied, migNowToken) || !regexp.MustCompile(`DEFAULT '\d{4}-\d\d-\d\d \d\d:\d\d:\d\d'$`).MatchString(applied) {
		t.Fatalf("the token was not swapped for the apply time: %q", applied)
	}
}

// Hand-written Db.migrate SQL that holds the token text runs unchanged.
func TestStoreMigrateHandWrittenSqlKeepsTheTokenText(t *testing.T) {
	d := openStoreQueryDb(t)
	raw := "CREATE TABLE notes (body TEXT);\nINSERT INTO notes (body) VALUES (" + migNowToken + ")"
	if r := migrateSql(t, d, "001_notes", raw); r.Tag != 0 {
		t.Fatalf("apply: %+v", r)
	}
	var body string
	_ = d.conn.QueryRow("SELECT body FROM notes").Scan(&body)
	if "'"+body+"'" != migNowToken {
		t.Fatalf("hand-written SQL was rewritten: %q", body)
	}
}

// migrateOpsJson runs Db_migrateOps (`sky db migrate`) on a JSON array of
// migration files.
func migrateOpsJson(t *testing.T, d *SkyDb, files string) SkyResult[any, any] {
	t.Helper()
	r, ok := AnyTaskRun(Db_migrateOps(d, files)).(SkyResult[any, any])
	if !ok {
		t.Fatalf("Db_migrateOps returned a non-Result")
	}
	return r
}

// migrateSql runs Db_migrateApply (`Db.migrate`) on one hand-written migration.
func migrateSql(t *testing.T, d *SkyDb, name, sqlText string) SkyResult[any, any] {
	t.Helper()
	r, ok := AnyTaskRun(Db_migrateApply(d, []any{T2[any, any]{V0: name, V1: sqlText}})).(SkyResult[any, any])
	if !ok {
		t.Fatalf("Db_migrateApply returned a non-Result")
	}
	return r
}

func migLedgerRow(t *testing.T, d *SkyDb, name string) (string, string) {
	t.Helper()
	var sum string
	var kind sql.NullString
	if err := d.conn.QueryRow("SELECT checksum, kind FROM _sky_migrations WHERE name = "+d.placeholder(1), name).
		Scan(&sum, &kind); err != nil {
		t.Fatalf("ledger row %s: %v", name, err)
	}
	return sum, kind.String
}

func opsChecksumOf(t *testing.T, ops string) string {
	t.Helper()
	sum, err := migOpsChecksum(json.RawMessage(ops))
	if err != nil {
		t.Fatal(err)
	}
	return sum
}

func TestStoreMigrateSqliteNowAppliedOnceWithTheOpsChecksum(t *testing.T) {
	d := openStoreQueryDb(t)
	for _, s := range []string{
		"CREATE TABLE orders (id TEXT PRIMARY KEY, status TEXT)",
		"INSERT INTO orders (id, status) VALUES ('o1', 'open')",
	} {
		if _, err := d.conn.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	ops := `[{"kind":"addColumn","table":"orders","column":"seen_at","type":"text","nullable":false,"default":{"nowAtApply":true}}]`
	files := `[{"id":"002_seen","ops":` + ops + `}]`
	if r := migrateOpsJson(t, d, files); r.Tag != 0 || len(AsList(r.OkValue)) != 1 {
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
	if r := migrateOpsJson(t, d, files); r.Tag != 0 || len(AsList(r.OkValue)) != 0 {
		t.Fatalf("second apply drifted or re-ran: %+v", r)
	}
	if sum, kind := migLedgerRow(t, d, "002_seen"); sum != opsChecksumOf(t, ops) || kind != "ops" {
		t.Fatalf("ledger row = (%s, %q), want the ops checksum and kind 'ops'", sum, kind)
	}
	var again string
	_ = d.conn.QueryRow("SELECT seen_at FROM orders WHERE id = 'o1'").Scan(&again)
	if again != first {
		t.Fatalf("the row changed from %q to %q", first, again)
	}
}

// The ops checksum ignores layout, key order and a `"v"` key, and changes
// when an op changes.
func TestStoreMigrateOpsChecksumIsCanonical(t *testing.T) {
	a := opsChecksumOf(t, `[{"kind":"addColumn","table":"t","column":"n","type":"int","default":{"int":9007199254740993}}]`)
	b := opsChecksumOf(t, "[ {\"v\": 2, \"default\": {\"int\": 9007199254740993}, \"type\": \"int\",\n \"column\": \"n\", \"table\": \"t\", \"kind\": \"addColumn\"} ]")
	if a != b {
		t.Fatalf("the same ops gave two checksums")
	}
	if c := opsChecksumOf(t, `[{"kind":"addColumn","table":"t","column":"n","type":"int","default":{"int":9007199254740992}}]`); c == a {
		t.Fatalf("a changed default kept the checksum")
	}
}

// --- Upgrade from a v0.28 ledger --------------------------------------------
//
// A v0.28 ledger has no `kind` column, and its checksum of an op file is the
// sha256 of the SQL that the v0.28.1 renderer gave. This renderer gives
// different SQL for the same file (B8 adds NOT NULL to a text PK), so the old
// rule would fail every existing deployment with "checksum mismatch".

// legacyOps is an op file that v0.28 --gen wrote (no "v" key).
const legacyOps = `[{"kind":"createTable","table":"ledger_users","columns":[` +
	`{"name":"id","type":"text","nullable":false,"pk":true,"autoinc":false,"unique":false},` +
	`{"name":"age","type":"int","nullable":false,"pk":false,"autoinc":false,"unique":false,"default":{"int":0}}]},` +
	`{"kind":"addColumn","table":"ledger_users","column":"score","type":"int","nullable":false,"default":{"int":0}}]`

// legacyText is the SQL that v0.28.1 rendered for legacyOps, taken from the
// v1 renderer before it was removed.
var legacyText = map[string]string{
	"sqlite": "CREATE TABLE IF NOT EXISTS ledger_users (\n  id TEXT PRIMARY KEY,\n  age INTEGER NOT NULL DEFAULT 0\n);\n" +
		"ALTER TABLE ledger_users ADD COLUMN score INTEGER NOT NULL DEFAULT 0",
	"pgx": "CREATE TABLE IF NOT EXISTS ledger_users (\n  id TEXT PRIMARY KEY,\n  age BIGINT NOT NULL DEFAULT 0\n);\n" +
		"ALTER TABLE ledger_users ADD COLUMN score BIGINT NOT NULL DEFAULT 0",
}

// legacyLedger sets d up as v0.28 left it: the schema of legacyText applied,
// a ledger without `kind`, and rows with v0.28 checksums for the op file and
// for one hand-written SQL migration.
func legacyLedger(t *testing.T, d *SkyDb) (handSql string) {
	t.Helper()
	handSql = "CREATE TABLE ledger_notes (body TEXT)"
	at := "2026-10-01T00:00:00Z"
	stmts := strings.Split(legacyText[d.driver], ";\n")
	stmts = append(stmts, handSql,
		"CREATE TABLE _sky_migrations (name TEXT PRIMARY KEY, checksum TEXT NOT NULL, applied_at TEXT NOT NULL)",
		fmt.Sprintf("INSERT INTO _sky_migrations VALUES ('001_users', '%s', '%s')", migSQLChecksum(legacyText[d.driver]), at),
		fmt.Sprintf("INSERT INTO _sky_migrations VALUES ('000_notes', '%s', '%s')", migSQLChecksum(handSql), at),
	)
	for _, s := range stmts {
		if _, err := d.conn.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	// The renderer of this release gives other SQL: the old rule would fail.
	if text, _ := renderMigFile(d.driver, migFile{Ops: mustOps(t, legacyOps)}); text == legacyText[d.driver] {
		t.Fatalf("the fixture renders as it did in v0.28.1: it proves nothing")
	}
	return handSql
}

func mustOps(t *testing.T, ops string) []migOp {
	t.Helper()
	var out []migOp
	if err := json.Unmarshal([]byte(ops), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func migKindColumns(t *testing.T, d *SkyDb) int {
	t.Helper()
	q := "SELECT COUNT(*) FROM pragma_table_info('_sky_migrations') WHERE name = 'kind'"
	if d.driver == "pgx" {
		q = "SELECT COUNT(*) FROM information_schema.columns WHERE table_schema = current_schema() " +
			"AND table_name = '_sky_migrations' AND column_name = 'kind'"
	}
	var n int
	if err := d.conn.QueryRow(q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// An op file applied by v0.28 is re-stamped once with its ops checksum; the
// second run is a no-op and the `kind` column is added once.
func assertLegacyLedgerRestamped(t *testing.T, d *SkyDb) {
	t.Helper()
	legacyLedger(t, d)
	files := `[{"id":"001_users","ops":` + legacyOps + `}]`
	for run := 1; run <= 2; run++ {
		if r := migrateOpsJson(t, d, files); r.Tag != 0 || len(AsList(r.OkValue)) != 0 {
			t.Fatalf("run %d: %+v", run, r)
		}
		if sum, kind := migLedgerRow(t, d, "001_users"); sum != opsChecksumOf(t, legacyOps) || kind != "ops" {
			t.Fatalf("run %d: ledger row = (%s, %q), want the ops checksum and kind 'ops'", run, sum, kind)
		}
		if n := migKindColumns(t, d); n != 1 {
			t.Fatalf("run %d: %d kind columns", run, n)
		}
	}
	// The hand-written row is not touched by an op-file run.
	if _, kind := migLedgerRow(t, d, "000_notes"); kind != "" {
		t.Fatalf("the hand-written row got kind %q", kind)
	}
}

// A hand-written SQL migration applied under the v0.28 ledger still verifies
// by its SQL text, and an edited one still fails.
func assertLegacyHandWrittenSql(t *testing.T, d *SkyDb) {
	t.Helper()
	hand := legacyLedger(t, d)
	if r := migrateSql(t, d, "000_notes", hand); r.Tag != 0 || len(AsList(r.OkValue)) != 0 {
		t.Fatalf("the applied hand-written migration did not verify: %+v", r)
	}
	r := migrateSql(t, d, "000_notes", hand+" -- edited")
	if r.Tag == 0 || !strings.Contains(fmt.Sprint(r.ErrValue), "checksum mismatch") {
		t.Fatalf("an edited hand-written migration was accepted: %+v", r)
	}
}

// After the re-stamp an op file is checked by its ops: an edit fails.
func assertEditedOpFileFails(t *testing.T, d *SkyDb) {
	t.Helper()
	legacyLedger(t, d)
	if r := migrateOpsJson(t, d, `[{"id":"001_users","ops":`+legacyOps+`}]`); r.Tag != 0 {
		t.Fatalf("re-stamp: %+v", r)
	}
	edited := strings.Replace(legacyOps, `"column":"score"`, `"column":"points"`, 1)
	r := migrateOpsJson(t, d, `[{"id":"001_users","ops":`+edited+`}]`)
	if r.Tag == 0 || !strings.Contains(fmt.Sprint(r.ErrValue), "checksum mismatch") {
		t.Fatalf("an edited op file was accepted: %+v", r)
	}
}

// A pending op file that v0.28 --gen wrote (no "v") applies with this
// release's rule: a required field is NOT NULL with its zero default.
func assertPendingV028FileAppliesWithTheNewRule(t *testing.T, d *SkyDb) {
	t.Helper()
	legacyLedger(t, d)
	if _, err := d.conn.Exec("INSERT INTO ledger_users (id, age, score) VALUES ('u1', 3, 4)"); err != nil {
		t.Fatal(err)
	}
	ops := `[{"kind":"addColumn","table":"ledger_users","column":"visits","type":"int","nullable":false}]`
	files := `[{"id":"001_users","ops":` + legacyOps + `},{"id":"002_visits","ops":` + ops + `}]`
	if r := migrateOpsJson(t, d, files); r.Tag != 0 || len(AsList(r.OkValue)) != 1 {
		t.Fatalf("apply: %+v", r)
	}
	var visits int64
	if err := d.conn.QueryRow("SELECT visits FROM ledger_users WHERE id = 'u1'").Scan(&visits); err != nil || visits != 0 {
		t.Fatalf("existing row reads visits = %d (%v), want 0", visits, err)
	}
	if _, err := d.conn.Exec("INSERT INTO ledger_users (id, age, score, visits) VALUES ('u2', 1, 1, NULL)"); err == nil {
		t.Fatalf("visits accepted NULL: the column is not NOT NULL")
	}
	if sum, kind := migLedgerRow(t, d, "002_visits"); sum != opsChecksumOf(t, ops) || kind != "ops" {
		t.Fatalf("ledger row = (%s, %q)", sum, kind)
	}
}

func TestStoreMigrateSqliteLegacyLedgerRestamped(t *testing.T) {
	assertLegacyLedgerRestamped(t, openStoreQueryDb(t))
}

func TestStoreMigrateSqliteLegacyHandWrittenSql(t *testing.T) {
	assertLegacyHandWrittenSql(t, openStoreQueryDb(t))
}

func TestStoreMigrateSqliteEditedOpFileFails(t *testing.T) {
	assertEditedOpFileFails(t, openStoreQueryDb(t))
}

func TestStoreMigrateSqlitePendingV028FileAppliesWithTheNewRule(t *testing.T) {
	assertPendingV028FileAppliesWithTheNewRule(t, openStoreQueryDb(t))
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
		"rank", "int|dint=7", "score", "real|dfloat=1.5", "active", "bool", "payload", `blob|zero=["it's"]`)
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
		`ALTER TABLE orders ADD COLUMN payload TEXT NOT NULL DEFAULT '["it''s"]'`,
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
	// A required JSON (blob) column reads the codec's blank value (Gap A).
	var payload string
	if err := d.conn.QueryRow("SELECT payload FROM orders WHERE id = 'o1'").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != `["it's"]` {
		t.Fatalf("old row reads payload=%q, want the blank's JSON", payload)
	}
	if _, err := d.conn.Exec("INSERT INTO orders (id, status, qty) VALUES ('o2', 'x', NULL)"); err == nil {
		t.Fatalf("NULL was accepted in the NOT NULL column qty")
	}
	if r := autoMigrate(t, d, "orders", cs, nil); r.Tag != 0 || len(AsList(r.OkValue)) != 0 {
		t.Fatalf("second migrate is not empty: %+v", r)
	}
}

// assertRequiredBlobWithoutZero: a required JSON (blob) column whose codec
// knows no zero value (a hand-built codec with a taggedUnion field, a
// `Std.Db.Schema.blob` column) is added NOT NULL with no default, which only
// an empty table can take. On a table with a row `Store.migrate` is Err and
// the table is unchanged, and a migration file fails with the same advice and
// rolls back. Before Gap A the column was added NULLABLE on both paths, which
// broke rule D1. Shared by the SQLite and PostgreSQL arms.
func assertRequiredBlobWithoutZero(t *testing.T, d *SkyDb, table string) {
	t.Helper()
	empty := table + "_empty"
	id := "0002_" + table + "_doc"
	cleanup := func() {
		_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table)
		_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + empty)
		_, _ = d.conn.Exec("DELETE FROM _sky_migrations WHERE name = '" + id + "'")
	}
	cleanup()
	t.Cleanup(cleanup)
	// An empty table takes the column NOT NULL, with no default.
	if r := autoMigrate(t, d, empty, cspec("id", "text"), nil); r.Tag != 0 {
		t.Fatalf("create: %+v", r)
	}
	r := autoMigrate(t, d, empty, cspec("id", "text", "doc", "blob"), nil)
	if r.Tag != 0 {
		t.Fatalf("migrate an empty table: %+v", r)
	}
	ran := []string{}
	for _, s := range AsList(r.OkValue) {
		ran = append(ran, AsString(s))
	}
	if want := "ALTER TABLE " + empty + " ADD COLUMN doc TEXT NOT NULL"; strings.Join(ran, "\n") != want {
		t.Fatalf("statements:\n%s\nwant\n%s", strings.Join(ran, "\n"), want)
	}
	// A table with one row: Err, and the table is unchanged.
	if r := autoMigrate(t, d, table, cspec("id", "text"), nil); r.Tag != 0 {
		t.Fatalf("create: %+v", r)
	}
	if _, err := d.conn.Exec("INSERT INTO " + table + " (id) VALUES ('a')"); err != nil {
		t.Fatal(err)
	}
	advice := []string{"column " + table + ".doc", "no zero value", "Maybe", "Codec.auto"}
	r = autoMigrate(t, d, table, cspec("id", "text", "doc", "blob"), nil)
	if r.Tag == 0 {
		t.Fatalf("a required blob with no zero value was added to a table with a row: %+v", r.OkValue)
	}
	msg := fmt.Sprintf("%v", r.ErrValue)
	for _, want := range advice {
		if !strings.Contains(msg, want) {
			t.Fatalf("Store.migrate error does not say %q:\n%s", want, msg)
		}
	}
	if cols, _ := codecTableColumns(d, table); codecHasColumn(d.driver, cols, "doc") {
		t.Fatalf("Store.migrate changed the table")
	}
	// The same column from a migration file: the same advice, rolled back.
	f := migFile{Id: id, Ops: []migOp{
		{Kind: "addColumn", Table: table, Column: "doc", Type: "blob", Nullable: false},
	}}
	r = migrateOpsJson(t, d, migFilesJson(t, f))
	if r.Tag == 0 {
		t.Fatalf("the migration file was applied")
	}
	msg = fmt.Sprintf("%v", r.ErrValue)
	for _, want := range append(advice, "rolled back") {
		if !strings.Contains(msg, want) {
			t.Fatalf("sky db migrate error does not say %q:\n%s", want, msg)
		}
	}
	if cols, _ := codecTableColumns(d, table); codecHasColumn(d.driver, cols, "doc") {
		t.Fatalf("the migration file did not roll back")
	}
	var n int
	_ = d.conn.QueryRow("SELECT COUNT(*) FROM _sky_migrations WHERE name = '" + id + "'").Scan(&n)
	if n != 0 {
		t.Fatalf("the failed migration was recorded in the ledger")
	}
}

func TestStoreMigrateD1_SqliteRequiredBlobWithoutZero(t *testing.T) {
	assertRequiredBlobWithoutZero(t, openStoreQueryDb(t), "docs_nz")
}

// assertFieldZerosOnRows: rule D1 takes each column's zero from its OWN field
// (Codec.fieldZeros), so Std.Db.Store sends a `zero=` flag for a list field
// whose sibling taggedUnion field has none, and for an enum (TEXT) column.
// Store.migrate and a migration file give one DEFAULT each: `'[]'` and the
// enum's first name, quoted. The old row reads both, and the no-zero sibling
// is still refused on a table with a row. Shared by SQLite and PostgreSQL.
func assertFieldZerosOnRows(t *testing.T, d *SkyDb, table string) {
	t.Helper()
	id := "0003_" + table + "_zero"
	cleanup := func() {
		_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table)
		_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table + "_f")
		_, _ = d.conn.Exec("DELETE FROM _sky_migrations WHERE name = '" + id + "'")
	}
	cleanup()
	t.Cleanup(cleanup)
	if r := autoMigrate(t, d, table, cspec("id", "text", "shade", "blob"), nil); r.Tag != 0 {
		t.Fatalf("create: %+v", r)
	}
	if _, err := d.conn.Exec("INSERT INTO " + table + " (id, shade) VALUES ('o1', '[\"Plain\"]')"); err != nil {
		t.Fatal(err)
	}
	added := []string{"tags", "blob|zero=[]", "stage", "text|zero=draft", "mood", "text|zero=it's"}
	r := autoMigrate(t, d, table, cspec(append([]string{"id", "text", "shade", "blob"}, added...)...), nil)
	if r.Tag != 0 {
		t.Fatalf("migrate: %+v", r)
	}
	ran := []string{}
	for _, s := range AsList(r.OkValue) {
		ran = append(ran, AsString(s))
	}
	want := []string{
		"ALTER TABLE " + table + " ADD COLUMN tags TEXT NOT NULL DEFAULT '[]'",
		"ALTER TABLE " + table + " ADD COLUMN stage TEXT NOT NULL DEFAULT 'draft'",
		"ALTER TABLE " + table + " ADD COLUMN mood TEXT NOT NULL DEFAULT 'it''s'",
	}
	if strings.Join(ran, ";") != strings.Join(want, ";") {
		t.Fatalf("Store.migrate ran:\n%q\nwant:\n%q", ran, want)
	}
	var tags, stage, mood string
	if err := d.conn.QueryRow("SELECT tags, stage, mood FROM "+table+" WHERE id = 'o1'").Scan(&tags, &stage, &mood); err != nil {
		t.Fatal(err)
	}
	if tags != "[]" || stage != "draft" || mood != "it's" {
		t.Fatalf("the old row reads tags=%q stage=%q mood=%q", tags, stage, mood)
	}
	// The migration file `sky db migrate --gen` writes from the schema dump
	// gives the same values.
	if _, err := d.conn.Exec("CREATE TABLE " + table + "_f (id TEXT PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.conn.Exec("INSERT INTO " + table + "_f (id) VALUES ('o1')"); err != nil {
		t.Fatal(err)
	}
	ops := []migOp{}
	for i := 0; i+1 < len(added); i += 2 {
		c := codecDumpColumn(added[i], added[i+1])
		ops = append(ops, migOp{Kind: "addColumn", Table: table + "_f", Column: c.Name,
			Type: c.Kind, Nullable: false, Default: c.Zero})
	}
	if r := migrateOpsJson(t, d, migFilesJson(t, migFile{Id: id, Ops: ops})); r.Tag != 0 {
		t.Fatalf("sky db migrate: %+v", r)
	}
	if err := d.conn.QueryRow("SELECT tags, stage, mood FROM "+table+"_f WHERE id = 'o1'").Scan(&tags, &stage, &mood); err != nil {
		t.Fatal(err)
	}
	if tags != "[]" || stage != "draft" || mood != "it's" {
		t.Fatalf("the migration file gave tags=%q stage=%q mood=%q", tags, stage, mood)
	}
	// The sibling with no zero is still refused on a table with a row.
	_, _ = d.conn.Exec("DROP TABLE IF EXISTS " + table)
	if r := autoMigrate(t, d, table, cspec("id", "text", "tags", "blob|zero=[]"), nil); r.Tag != 0 {
		t.Fatalf("create: %+v", r)
	}
	if _, err := d.conn.Exec("INSERT INTO " + table + " (id, tags) VALUES ('o1', '[]')"); err != nil {
		t.Fatal(err)
	}
	r = autoMigrate(t, d, table, cspec("id", "text", "tags", "blob|zero=[]", "shade", "blob"), nil)
	if r.Tag == 0 {
		t.Fatalf("a required blob with no zero was added to a table with a row")
	}
	if msg := fmt.Sprintf("%v", r.ErrValue); !strings.Contains(msg, table+".shade") || !strings.Contains(msg, "no zero value") {
		t.Fatalf("the refusal does not name the column:\n%s", msg)
	}
}

func TestStoreMigrateD1_SqliteFieldZerosOnRows(t *testing.T) {
	assertFieldZerosOnRows(t, openStoreQueryDb(t), "zeros_d1")
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

// parityNormalise maps the runtime stamp and the apply-time token to one
// marker.
func parityNormalise(stmts []string) string {
	s := strings.Join(stmts, ";\n")
	return parityStamp.ReplaceAllString(s, "<now>")
}

// renderParityOps renders a column's ops the way `sky db migrate` does.
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
			stmts, err := renderAddColumn(driver, f.Table, codecColspecMigColumn(c.Colspec[0], c.Colspec[1], f.Pk), sqliteNowLiteral(), 0)
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

// --- A required unique column added by a migration file to a table with rows
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
		{Kind: "addColumn", Table: table, Column: "code", Type: "text",
			Nullable: false, Default: &migDefault{Text: &zero}},
		{Kind: "addIndex", Table: table, Name: codecIndexName(table, cols, true, ""),
			IndexColumns: cols, Unique: true},
	}}
	r := migrateOpsJson(t, d, migFilesJson(t, f))
	if r.Tag == 0 {
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
	f := migFile{Id: "0002_one", Ops: []migOp{
		{Kind: "addColumn", Table: "codes_one", Column: "code", Type: "text", Default: &migDefault{Text: &zero}},
		{Kind: "addIndex", Table: "codes_one", Name: codecIndexName("codes_one", cols, true, ""),
			IndexColumns: cols, Unique: true},
	}}
	if r := migrateOpsJson(t, d, migFilesJson(t, f)); r.Tag != 0 {
		t.Fatalf("apply: %+v", r)
	}
}

// migFilesJson writes migration files as the JSON array Db_migrateOps reads.
func migFilesJson(t *testing.T, files ...migFile) string {
	t.Helper()
	b, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
