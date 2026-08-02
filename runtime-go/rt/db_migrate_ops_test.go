package rt

import (
	"strings"
	"testing"
)

func TestRenderMigOp(t *testing.T) {
	zero := int64(0)
	cases := []struct {
		op       migOp
		sqlite   string
		pg       string
	}{
		{migOp{Kind: "addColumn", Table: "users", Column: "age", Type: "int", Nullable: true},
			"ALTER TABLE users ADD COLUMN age INTEGER", "ALTER TABLE users ADD COLUMN age BIGINT"},
		{migOp{Kind: "addColumn", Table: "users", Column: "age", Type: "int", Nullable: false, Default: &migDefault{Int: &zero}},
			"ALTER TABLE users ADD COLUMN age INTEGER NOT NULL DEFAULT 0", "ALTER TABLE users ADD COLUMN age BIGINT NOT NULL DEFAULT 0"},
		{migOp{Kind: "renameColumn", Table: "users", From: "slug", To: "handle"},
			"ALTER TABLE users RENAME COLUMN slug TO handle", "ALTER TABLE users RENAME COLUMN slug TO handle"},
		{migOp{Kind: "dropColumn", Table: "users", Column: "legacy"},
			"ALTER TABLE users DROP COLUMN legacy", "ALTER TABLE users DROP COLUMN legacy"},
		{migOp{Kind: "addIndex", Table: "users", Name: "idx_email", IndexColumns: []string{"email"}, Unique: true},
			"CREATE UNIQUE INDEX IF NOT EXISTS idx_email ON users (email)", "CREATE UNIQUE INDEX IF NOT EXISTS idx_email ON users (email)"},
	}
	for i, c := range cases {
		got, err := renderMigOp("sqlite", c.op)
		if err != nil || got != c.sqlite {
			t.Errorf("case %d sqlite: got %q err=%v, want %q", i, got, err, c.sqlite)
		}
		gotp, err := renderMigOp("pgx", c.op)
		if err != nil || gotp != c.pg {
			t.Errorf("case %d pg: got %q err=%v, want %q", i, gotp, err, c.pg)
		}
	}
	// createTable renders a CREATE TABLE
	ct, _ := renderMigOp("pgx", migOp{Kind: "createTable", Table: "t", Columns: []migColumn{
		{Name: "id", Type: "text", Pk: true}, {Name: "n", Type: "int", Nullable: false},
	}})
	if !strings.Contains(ct, "CREATE TABLE IF NOT EXISTS t") || !strings.Contains(ct, "id TEXT PRIMARY KEY") || !strings.Contains(ct, "n BIGINT NOT NULL") {
		t.Errorf("createTable render wrong: %s", ct)
	}
	// invalid identifier rejected
	if _, err := renderMigOp("sqlite", migOp{Kind: "addColumn", Table: "u; DROP", Column: "x", Type: "int"}); err == nil {
		t.Error("expected error on injection identifier")
	}
}

// TestRenderMigOpCreateTableConstraints is the bug #9 regression: the committed-
// migration createTable must render serial AUTOINCREMENT/BIGSERIAL, UNIQUE, and
// DEFAULT — previously all three were silently dropped, so SQLite accepted
// duplicate emails and Postgres serial PKs became plain BIGINT (null-PK violation
// on Store.insert, which omits the generated PK).
func TestRenderMigOpCreateTableConstraints(t *testing.T) {
	ct := migOp{Kind: "createTable", Table: "users", Columns: []migColumn{
		{Name: "id", Type: "int", Pk: true, Autoinc: true},
		{Name: "email", Type: "text", Nullable: false, Unique: true},
		{Name: "created_at", Type: "int", Nullable: false, Default: &migDefault{Now: true}},
	}}

	gotSqlite, err := renderMigOp("sqlite", ct)
	if err != nil {
		t.Fatalf("sqlite render error: %v", err)
	}
	for _, want := range []string{
		"id INTEGER PRIMARY KEY AUTOINCREMENT",
		"email TEXT NOT NULL UNIQUE",
		"created_at INTEGER NOT NULL DEFAULT (datetime('now'))",
	} {
		if !strings.Contains(gotSqlite, want) {
			t.Errorf("sqlite createTable missing %q in:\n%s", want, gotSqlite)
		}
	}

	gotPg, err := renderMigOp("pgx", ct)
	if err != nil {
		t.Fatalf("pg render error: %v", err)
	}
	for _, want := range []string{
		"id BIGSERIAL PRIMARY KEY",
		"email TEXT NOT NULL UNIQUE",
		"created_at BIGINT NOT NULL DEFAULT now()",
	} {
		if !strings.Contains(gotPg, want) {
			t.Errorf("pg createTable missing %q in:\n%s", want, gotPg)
		}
	}
}

// TestMigrateCreateTableByteMatchesPush is invariant A: the committed-migration
// createTable DDL BYTE-MATCHES the `sky db push` DDL (codecColspecSchemaMap →
// schemaRenderTable) for BOTH dialects. Both paths now route through schemaColMap,
// so they physically cannot diverge — this test locks that in.
func TestMigrateCreateTableByteMatchesPush(t *testing.T) {
	// The push-path colspec Store builds for `serial "id" |> unique "email" |>
	// defaultNow "created_at"` (markers per Std.Db.Store: `!` autoinc, `|u` unique,
	// `|dnow` default-now).
	pushColspec := []any{
		T2[any, any]{V0: "id", V1: "int!"},
		T2[any, any]{V0: "email", V1: "text|u"},
		T2[any, any]{V0: "created_at", V1: "int|dnow"},
	}
	// The migOp the dump → diff → createTable pipeline yields for the same table.
	migColumns := []migColumn{
		{Name: "id", Type: "int", Pk: true, Autoinc: true},
		{Name: "email", Type: "text", Nullable: false, Unique: true},
		{Name: "created_at", Type: "int", Nullable: false, Default: &migDefault{Now: true}},
	}

	for _, driver := range []string{"sqlite", "pgx"} {
		pushDDL := strings.Join(
			schemaRenderTable(driver, codecColspecSchemaMap("users", pushColspec, "id")), ";\n")
		migDDL, err := renderMigOp(driver, migOp{Kind: "createTable", Table: "users", Columns: migColumns})
		if err != nil {
			t.Fatalf("%s migrate render error: %v", driver, err)
		}
		if pushDDL != migDDL {
			t.Errorf("%s: push DDL != migrate DDL\npush:    %q\nmigrate: %q", driver, pushDDL, migDDL)
		}
	}
}
