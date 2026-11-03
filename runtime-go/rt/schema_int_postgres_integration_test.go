//go:build integration
// +build integration

package rt

// Real-PostgreSQL arm of schema_two_paths_test.go (v0.29.2): Schema.int is
// BIGINT through Schema.createSchema and through Store.migrate /
// pushProject, a value above 2^31 round-trips, and an INTEGER column an
// older createSchema made is a match (no ALTER) with a notice.

import (
	"database/sql"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func pgColumnType(t *testing.T, d *SkyDb, table, col string) string {
	t.Helper()
	var typ string
	if err := d.conn.QueryRow(
		"SELECT data_type FROM information_schema.columns WHERE table_name = $1 AND column_name = $2 AND table_schema = current_schema()",
		table, col).Scan(&typ); err != nil {
		t.Fatalf("type of %s.%s: %v", table, col, err)
	}
	return typ
}

func TestPostgresSchemaIntIsBigintOnBothPaths(t *testing.T) {
	dsn := requirePostgresDSN(t)
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	d := &SkyDb{conn: conn, driver: "pgx"}
	for _, tbl := range []string{"sky_int_create", "sky_int_push", "sky_int_old"} {
		if _, err := conn.Exec("DROP TABLE IF EXISTS " + tbl); err != nil {
			t.Fatal(err)
		}
	}
	// Schema.createSchema renders the Schema kind directly.
	day := schemaColMap("day", "int", false, false, false, false, "int", "0")
	day["Kind"] = "int" // the Schema kind, as createSchema passes it
	create := schemaRenderTable("pgx", map[string]any{
		"Name":    "sky_int_create",
		"Columns": []any{schemaColMap("id", "text", true, false, false, false, "none", ""), day},
		"Indexes": []any{},
	})
	for _, stmt := range create {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if got := pgColumnType(t, d, "sky_int_create", "day"); got != "bigint" {
		t.Errorf("createSchema Schema.int = %s, want bigint", got)
	}
	// Store.pushProject (Schema.toProject) / Store.migrate.
	if r := autoMigrate(t, d, "sky_int_push", cspec("id", "text", "day", "int"), nil); r.Tag != 0 {
		t.Fatalf("migrate: %+v", r.ErrValue)
	}
	if got := pgColumnType(t, d, "sky_int_push", "day"); got != "bigint" {
		t.Errorf("pushProject Schema.int = %s, want bigint", got)
	}
	// A value above 2^31 round-trips on the fresh column.
	if _, err := conn.Exec("INSERT INTO sky_int_create (id, day) VALUES ('a', $1)", int64(3000000000)); err != nil {
		t.Fatalf("insert 3000000000: %v", err)
	}
	var back int64
	if err := conn.QueryRow("SELECT day FROM sky_int_create WHERE id = 'a'").Scan(&back); err != nil || back != 3000000000 {
		t.Errorf("read back %d, %v; want 3000000000", back, err)
	}
	// An INTEGER column an older createSchema made: no ALTER, one notice.
	if _, err := conn.Exec("CREATE TABLE sky_int_old (id TEXT PRIMARY KEY, day INTEGER NOT NULL DEFAULT 0)"); err != nil {
		t.Fatal(err)
	}
	r := autoMigrate(t, d, "sky_int_old", cspec("id", "text", "day", "int"), nil)
	if r.Tag != 0 {
		t.Fatalf("migrate of the old table: %+v", r.ErrValue)
	}
	if applied := AsList(r.OkValue); len(applied) != 0 {
		t.Errorf("migrate changed an existing INTEGER column: %v", applied)
	}
	if got := pgColumnType(t, d, "sky_int_old", "day"); got != "integer" {
		t.Errorf("the old column became %s; it must stay integer", got)
	}
	types, err := codecTableColumnTypes(d, "sky_int_old")
	if err != nil {
		t.Fatal(err)
	}
	notes := codecNarrowIntNotices("sky_int_old", cspec("id", "text", "day", "int"), types)
	if len(notes) != 1 || !strings.Contains(notes[0], "ALTER TABLE sky_int_old ALTER COLUMN day TYPE BIGINT") {
		t.Errorf("notice = %q", notes)
	}
}
