//go:build !js

package rt

import (
	"encoding/json"
	"testing"
)

func TestMigrateOpsAdoptionDoesNotDuplicateUniqueConstraint(t *testing.T) {
	for _, adopted := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "adopted"}[adopted], func(t *testing.T) {
			d := inspectTestDb(t)
			d.conn.SetMaxOpenConns(1)
			if adopted {
				if _, err := d.conn.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, a TEXT NOT NULL, b TEXT NOT NULL, UNIQUE(a,b))`); err != nil {
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
			result := AnyTaskRun(Db_migrateOps(d, string(raw))).(SkyResult[any, any])
			if result.Tag != 0 {
				t.Fatalf("migration refused: %v", result)
			}
			var indexes int
			if err := d.conn.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND tbl_name='items'`).Scan(&indexes); err != nil {
				t.Fatal(err)
			}
			if indexes != 1 {
				t.Errorf("got %d indexes, want one logical unique index on both adopted and fresh databases", indexes)
			}
			if _, err := d.conn.Exec(`INSERT INTO items VALUES (1,'x','y')`); err != nil {
				t.Fatal(err)
			}
			if _, err := d.conn.Exec(`INSERT INTO items VALUES (2,'x','y')`); err == nil {
				t.Error("uniqueness was not preserved")
			}
		})
	}
}

// Explicit migration ops must retain the requested name even when a constraint
// already enforces uniqueness (later raw SQL may refer to that index).
func TestMigrateOpsExplicitNamedIndexIsCreated(t *testing.T) {
	d := inspectTestDb(t)
	if _, err := d.conn.Exec(`CREATE TABLE items (a TEXT NOT NULL, b TEXT NOT NULL, UNIQUE(a,b))`); err != nil {
		t.Fatal(err)
	}
	files := []migFile{{Id: "explicit", Ops: []migOp{{Kind: "addIndex", Table: "items", Name: "ux_items_ab", IndexColumns: []string{"a", "b"}, Unique: true}}}}
	raw, err := json.Marshal(files)
	if err != nil {
		t.Fatal(err)
	}
	result := AnyTaskRun(Db_migrateOps(d, string(raw))).(SkyResult[any, any])
	if result.Tag != 0 {
		t.Fatalf("migration refused: %v", result)
	}
	var count int
	if err := d.conn.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND tbl_name='items'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("got %d indexes, want constraint and explicitly named index", count)
	}
}

func TestMigrateOpsAdoptionKeepsInexactConstraintsDistinct(t *testing.T) {
	for _, key := range []string{"a COLLATE NOCASE, b", "a DESC, b"} {
		t.Run(key, func(t *testing.T) {
			d := inspectTestDb(t)
			if _, err := d.conn.Exec("CREATE TABLE items (a TEXT, b TEXT, UNIQUE(" + key + "))"); err != nil {
				t.Fatal(err)
			}
			files := []migFile{{Id: "special", Ops: []migOp{{Kind: "addIndex", Table: "items", Name: "plain_unique", IndexColumns: []string{"a", "b"}, Unique: true, SkipEquivalentConstraint: true}}}}
			raw, err := json.Marshal(files)
			if err != nil {
				t.Fatal(err)
			}
			result := AnyTaskRun(Db_migrateOps(d, string(raw))).(SkyResult[any, any])
			if result.Tag != 0 {
				t.Fatalf("migration refused: %v", result)
			}
			var count int
			if err := d.conn.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND tbl_name='items'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 2 {
				t.Errorf("got %d indexes: inexact constraint must not suppress ordinary named index", count)
			}
		})
	}
}

func TestMigrateOpsAdoptionFlagChangesChecksum(t *testing.T) {
	plain := json.RawMessage(`[{"kind":"addIndex","table":"items","name":"u","indexColumns":["a"],"unique":true}]`)
	flagged := json.RawMessage(`[{"kind":"addIndex","table":"items","name":"u","indexColumns":["a"],"unique":true,"skipEquivalentConstraint":true}]`)
	a, err := migOpsChecksum(plain)
	if err != nil {
		t.Fatal(err)
	}
	b, err := migOpsChecksum(flagged)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Error("adoption semantics omitted from migration ledger checksum")
	}
}
