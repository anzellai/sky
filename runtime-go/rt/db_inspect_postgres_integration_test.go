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

// The PostgreSQL arm of TestInspectSqliteIndexAndKeyShapes, plus the shapes
// only PostgreSQL has: INCLUDE columns, a non-default operator class, a
// non-B-tree method, and a foreign key on another table that names a plain
// unique index in `conindid` (it must stay a plain index, not a constraint).
// Index names are not compared: PostgreSQL names constraint indexes itself.
func TestPostgresInspectIndexAndKeyShapes(t *testing.T) {
	dsn := requirePostgresDSN(t)
	cases := []inspectShapeCase{
		{name: "multi-column UNIQUE constraint",
			ddl:   []string{`CREATE TABLE ext2 (code TEXT PRIMARY KEY, name TEXT NOT NULL, tenant BIGINT NOT NULL, UNIQUE (tenant, name))`},
			table: "ext2", pk: []string{"code"},
			indexes: []inspectIndex{{Columns: []string{"tenant", "name"}, Unique: true, Constraint: true}}},
		{name: "single-column UNIQUE constraint is the column's",
			ddl:   []string{`CREATE TABLE u1 (id BIGINT PRIMARY KEY, email TEXT NOT NULL UNIQUE)`},
			table: "u1", pk: []string{"id"}, unique: []string{"email"}},
		{name: "expression index",
			ddl:   []string{`CREATE TABLE ext5 (id BIGINT PRIMARY KEY, name TEXT NOT NULL, tenant BIGINT NOT NULL)`, `CREATE UNIQUE INDEX u5 ON ext5 (lower(name), tenant)`},
			table: "ext5", pk: []string{"id"},
			indexes: []inspectIndex{{Columns: []string{"lower(name)", "tenant"}, Unique: true, Inexact: "x"}}},
		{name: "INCLUDE columns",
			ddl:   []string{`CREATE TABLE inc (id BIGINT PRIMARY KEY, a TEXT NOT NULL, b TEXT NOT NULL)`, `CREATE UNIQUE INDEX ux_inc ON inc (a) INCLUDE (b)`},
			table: "inc", pk: []string{"id"},
			indexes: []inspectIndex{{Columns: []string{"a"}, Unique: true, Inexact: "x"}}},
		{name: "COLLATE",
			ddl:   []string{`CREATE TABLE co (id BIGINT PRIMARY KEY, email TEXT NOT NULL)`, `CREATE UNIQUE INDEX ux_co ON co (email COLLATE "C")`},
			table: "co", pk: []string{"id"},
			indexes: []inspectIndex{{Columns: []string{"email"}, Unique: true, Inexact: "x"}}},
		{name: "operator class",
			ddl:   []string{`CREATE TABLE oc (id BIGINT PRIMARY KEY, name TEXT NOT NULL)`, `CREATE INDEX ix_oc ON oc (name text_pattern_ops)`},
			table: "oc", pk: []string{"id"},
			indexes: []inspectIndex{{Columns: []string{"name"}, Inexact: "x"}}},
		{name: "hash method",
			ddl:   []string{`CREATE TABLE hm (id BIGINT PRIMARY KEY, name TEXT NOT NULL)`, `CREATE INDEX ix_hm ON hm USING hash (name)`},
			table: "hm", pk: []string{"id"},
			indexes: []inspectIndex{{Columns: []string{"name"}, Inexact: "x"}}},
		{name: "DESC",
			ddl:   []string{`CREATE TABLE d1 (id BIGINT PRIMARY KEY, at BIGINT NOT NULL)`, `CREATE INDEX ix_d1 ON d1 (at DESC)`},
			table: "d1", pk: []string{"id"},
			indexes: []inspectIndex{{Columns: []string{"at"}, Inexact: "x"}}},
		{name: "composite primary key",
			ddl:   []string{`CREATE TABLE ext4 (a BIGINT NOT NULL, b BIGINT NOT NULL, v TEXT, PRIMARY KEY (b, a))`},
			table: "ext4", pk: []string{"b", "a"}},
		{name: "a unique index a foreign key names stays a plain index",
			ddl: []string{`CREATE TABLE par (id BIGINT PRIMARY KEY, code TEXT NOT NULL)`,
				`CREATE UNIQUE INDEX ux_par_code ON par (code)`,
				`CREATE TABLE chi (id BIGINT PRIMARY KEY, pcode TEXT REFERENCES par (code))`},
			table: "par", pk: []string{"id"},
			indexes: []inspectIndex{{Columns: []string{"code"}, Unique: true}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conn, err := sql.Open("pgx", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetMaxOpenConns(1)
			for _, s := range []string{`DROP SCHEMA IF EXISTS sky_inspect_s CASCADE`, `CREATE SCHEMA sky_inspect_s`, `SET search_path TO sky_inspect_s`} {
				if _, err := conn.Exec(s); err != nil {
					t.Fatal(err)
				}
			}
			defer conn.Exec(`DROP SCHEMA IF EXISTS sky_inspect_s CASCADE`)
			for _, s := range c.ddl {
				if _, err := conn.Exec(s); err != nil {
					t.Fatalf("%s: %v", s, err)
				}
			}
			rep := runInspect(t, &SkyDb{conn: conn, driver: "pgx"}, "[]")
			checkInspectShape(t, rep, c, true)
		})
	}
}
