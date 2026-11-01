//go:build integration
// +build integration

package rt

// Real-PostgreSQL arm of db_inspect_test.go: the catalogue facts of a table a
// migration file created (BIGSERIAL key, UNIQUE column, NOT NULL, default,
// partial index), and inspect never creates `_sky_migrations`.

import (
	"database/sql"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresInspectCatalogue(t *testing.T) {
	dsn := requirePostgresDSN(t)
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	d := &SkyDb{conn: conn, driver: "pgx"}
	if _, err := conn.Exec(`DROP SCHEMA IF EXISTS sky_inspect_t CASCADE`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`CREATE SCHEMA sky_inspect_t`); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(`DROP SCHEMA IF EXISTS sky_inspect_t CASCADE`)
	conn.SetMaxOpenConns(1)
	if _, err := conn.Exec(`SET search_path TO sky_inspect_t`); err != nil {
		t.Fatal(err)
	}
	text, err := renderMigFile("pgx", migFile{Id: "1_init", Ops: []migOp{
		{Kind: "createTable", Table: "users", Columns: []migColumn{
			{Name: "id", Type: "int", Pk: true, Autoinc: true},
			{Name: "email", Type: "text", Unique: true},
			{Name: "rank", Type: "int", Default: &migDefault{Int: ptrInt64(7)}},
			{Name: "price", Type: "decimal", Nullable: true},
		}},
		{Kind: "addIndex", Table: "users", Name: "ix_users_rank", IndexColumns: []string{"rank"},
			Where: "rank > 0"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range strings.Split(text, ";\n") {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	rep := runInspect(t, d, "[]")
	if rep.Dialect != "postgres" || len(rep.Tables) != 1 {
		t.Fatalf("report: %+v", rep)
	}
	byName := map[string]inspectColumn{}
	for _, c := range rep.Tables[0].Columns {
		byName[c.Name] = c
	}
	if c := byName["id"]; !c.Pk || !c.Autoinc || c.Type != "bigint" {
		t.Errorf("id: %+v", c)
	}
	if c := byName["email"]; !c.Unique || !c.NotNull {
		t.Errorf("email: %+v", c)
	}
	if c := byName["rank"]; c.Default == nil || *c.Default != "7" {
		t.Errorf("rank: %+v", c)
	}
	if c := byName["price"]; c.Type != "numeric" || c.NotNull {
		t.Errorf("price: %+v", c)
	}
	ix := rep.Tables[0].Indexes
	if len(ix) != 1 || ix[0].Name != "ix_users_rank" || ix[0].Where == "" || ix[0].Columns[0] != "rank" {
		t.Errorf("indexes: %+v", ix)
	}
	if rep.Ledger.Present {
		t.Errorf("ledger reported present")
	}
	var reg sql.NullString
	if err := conn.QueryRow(`SELECT to_regclass('_sky_migrations')::text`).Scan(&reg); err != nil || reg.Valid {
		t.Errorf("inspect created the ledger (%v, %v)", reg, err)
	}
}
