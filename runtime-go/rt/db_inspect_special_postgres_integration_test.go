//go:build integration

package rt

import (
	"database/sql"
	"encoding/json"
	"testing"
)

func TestPostgresInspectMigrationAdoptionConstraint(t *testing.T) {
	for _, adopted := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "adopted"}[adopted], func(t *testing.T) {
			conn, err := sql.Open("pgx", requirePostgresDSN(t))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetMaxOpenConns(1)
			for _, stmt := range []string{`DROP SCHEMA IF EXISTS sky_review_migration CASCADE`, `CREATE SCHEMA sky_review_migration`, `SET search_path TO sky_review_migration`} {
				if _, err := conn.Exec(stmt); err != nil {
					t.Fatal(err)
				}
			}
			defer conn.Exec(`DROP SCHEMA sky_review_migration CASCADE`)
			if adopted {
				if _, err := conn.Exec(`CREATE TABLE items (id BIGINT PRIMARY KEY, a TEXT NOT NULL, b TEXT NOT NULL, UNIQUE(a,b))`); err != nil {
					t.Fatal(err)
				}
			}
			files := []migFile{{Id: "adopt", Ops: []migOp{
				{Kind: "createTable", Table: "items", Columns: []migColumn{{Name: "id", Type: "int", Pk: true}, {Name: "a", Type: "text"}, {Name: "b", Type: "text"}}},
				{Kind: "addIndex", Table: "items", Name: "ux_items_ab", IndexColumns: []string{"a", "b"}, Unique: true, SkipEquivalentConstraint: true},
			}}}
			raw, err := json.Marshal(files)
			if err != nil {
				t.Fatal(err)
			}
			result := AnyTaskRun(Db_migrateOps(&SkyDb{conn: conn, driver: "pgx"}, string(raw))).(SkyResult[any, any])
			if result.Tag != 0 {
				t.Fatalf("migration: %v", result)
			}
			var count int
			if err := conn.QueryRow(`SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema() AND tablename='items'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 2 {
				t.Errorf("got %d indexes, want primary plus one unique index", count)
			}
			if _, err := conn.Exec(`INSERT INTO items VALUES (1,'x','y')`); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Exec(`INSERT INTO items VALUES (2,'x','y')`); err == nil {
				t.Error("uniqueness was lost")
			}
		})
	}
}

// NULLS NOT DISTINCT rejects rows a plain Store unique index permits. Both
// standalone indexes and constraint-backed indexes must retain that fact,
// including a one-column constraint that would otherwise become Column.Unique.
func TestPostgresInspectNullsNotDistinct(t *testing.T) {
	conn, err := sql.Open("pgx", requirePostgresDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	var version int
	if err := conn.QueryRow(`SHOW server_version_num`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version < 150000 {
		t.Skip("NULLS NOT DISTINCT requires PostgreSQL 15 or later")
	}
	for _, stmt := range []string{
		`DROP SCHEMA IF EXISTS sky_review_nulls CASCADE`,
		`CREATE SCHEMA sky_review_nulls`,
		`SET search_path TO sky_review_nulls`,
		`CREATE TABLE items (id BIGINT PRIMARY KEY, label TEXT, tenant BIGINT)`,
		`CREATE UNIQUE INDEX ux_items_label ON items (label) NULLS NOT DISTINCT`,
		`ALTER TABLE items ADD CONSTRAINT items_tenant_label UNIQUE NULLS NOT DISTINCT (tenant, label)`,
		`ALTER TABLE items ADD CONSTRAINT items_tenant UNIQUE NULLS NOT DISTINCT (tenant)`,
		`CREATE TABLE ordinary (id BIGINT PRIMARY KEY, label TEXT UNIQUE)`,
		`INSERT INTO ordinary VALUES (1, NULL), (2, NULL)`,
		`INSERT INTO items VALUES (1, NULL, NULL)`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	defer conn.Exec(`DROP SCHEMA sky_review_nulls CASCADE`)
	if _, err := conn.Exec(`INSERT INTO items VALUES (2, NULL, NULL)`); err == nil {
		t.Fatal("NULLS NOT DISTINCT must reject a second null key, unlike ordinary UNIQUE")
	}
	report := runInspect(t, &SkyDb{conn: conn, driver: "pgx"}, "[]")
	found := false
	for _, table := range report.Tables {
		if table.Name != "items" {
			continue
		}
		found = true
		for _, col := range table.Columns {
			if col.Unique {
				t.Errorf("column %s uses NULLS NOT DISTINCT but is reported as plain uniqueness", col.Name)
			}
		}
		if len(table.Indexes) != 3 {
			t.Errorf("got %d indexes, want all three NULLS NOT DISTINCT indexes", len(table.Indexes))
		}
		for _, ix := range table.Indexes {
			if ix.Inexact != "NULLS NOT DISTINCT" {
				t.Errorf("index %s uses NULLS NOT DISTINCT but is reported as plain uniqueness", ix.Name)
			}
		}
	}
	if !found {
		t.Fatal("items table was not inspected")
	}
}

func TestPostgresInspectDeferrableAndInvalidUniqueness(t *testing.T) {
	conn, err := sql.Open("pgx", requirePostgresDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	for _, stmt := range []string{
		`DROP SCHEMA IF EXISTS sky_review_index_state CASCADE`,
		`CREATE SCHEMA sky_review_index_state`,
		`SET search_path TO sky_review_index_state`,
		`CREATE TABLE items (id BIGINT PRIMARY KEY, label TEXT UNIQUE DEFERRABLE INITIALLY DEFERRED, tenant BIGINT, UNIQUE (tenant, label) DEFERRABLE INITIALLY IMMEDIATE)`,
		`CREATE TABLE bad (id BIGINT PRIMARY KEY, label TEXT)`,
		`CREATE TABLE deferred_key (id BIGINT PRIMARY KEY DEFERRABLE INITIALLY DEFERRED)`,
		`CREATE TABLE included_key (id BIGINT, label TEXT, PRIMARY KEY (id) INCLUDE (label))`,
		`INSERT INTO bad VALUES (1, 'duplicate'), (2, 'duplicate')`,
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	defer conn.Exec(`DROP SCHEMA sky_review_index_state CASCADE`)
	// A failed concurrent build leaves its catalogue entry, but that entry
	// does not enforce the uniqueness that a Store declares.
	if _, err := conn.Exec(`CREATE UNIQUE INDEX CONCURRENTLY ux_bad ON bad (label)`); err == nil {
		t.Fatal("building a unique index over duplicate values must fail")
	}
	report := runInspect(t, &SkyDb{conn: conn, driver: "pgx"}, "[]")
	seen := 0
	for _, table := range report.Tables {
		for _, col := range table.Columns {
			if table.Name == "included_key" && (col.Pk != (col.Name == "id") || col.PkPos > 1) {
				t.Errorf("included primary-key column %s is not a key column", col.Name)
			}
			if table.Name == "items" && col.Name == "label" && col.Unique {
				t.Error("a deferrable constraint became a plain unique column")
			}
		}
		for _, ix := range table.Indexes {
			seen++
			if ix.Inexact == "" {
				t.Errorf("index %s has special enforcement semantics but was reported as plain uniqueness", ix.Name)
			}
		}
	}
	if seen != 5 {
		t.Errorf("got %d indexes, want three deferrable constraints, one invalid index and one INCLUDE key", seen)
	}
}
