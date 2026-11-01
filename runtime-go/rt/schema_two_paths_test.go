//go:build !js

package rt

import (
	"strings"
	"testing"
)

// One Std.Db.Schema column gets one column type, whichever path creates the
// table: `Schema.createSchema` renders the Schema kind directly, and
// `Store.pushProject (Schema.toProject …)` (and `sky db push` / `migrate`)
// renders the Store kind that toProject maps it to. `Schema.int` was INTEGER
// on PostgreSQL through createSchema and BIGINT through pushProject, so a
// fresh database differed with the path that made it.
func TestSchemaKindHasOneColumnTypeOnBothPaths(t *testing.T) {
	// Schema kind → the Store kind Schema.toProject gives it (storeKindBase
	// in sky-stdlib/Std/Db/Schema.sky). `blob` is left out: Schema.blob is
	// binary through createSchema and a JSON text column under the Store.
	toStore := map[string]string{
		"text": "text", "int": "int", "bigint": "int", "bool": "bool",
		"real": "real", "timestamp": "int", "json": "blob",
	}
	for _, driver := range []string{"pgx", "sqlite"} {
		for schemaKind, storeKind := range toStore {
			direct := schemaTypeName(driver, schemaKind)
			viaStore := schemaTypeName(driver, codecColKindToSchema(storeKind))
			if direct != viaStore {
				t.Errorf("%s: Schema kind %q is %s through createSchema and %s through pushProject",
					driver, schemaKind, direct, viaStore)
			}
		}
	}
}

// An Int column an older Schema.createSchema made as a 4-byte INTEGER is not
// altered, and gets a notice that names it and gives the widening statement.
func TestNarrowIntColumnNotice(t *testing.T) {
	types := map[string]string{"id": "text", "day": "integer", "n": "bigint", "label": "integer"}
	got := codecNarrowIntNotices("orders", cspec("id", "text", "day", "int|dint=0", "n", "int", "label", "text"), types)
	if len(got) != 1 {
		t.Fatalf("notices = %q, want one for orders.day", got)
	}
	for _, want := range []string{"orders.day", "INTEGER", "32-bit", "ALTER TABLE orders ALTER COLUMN day TYPE BIGINT"} {
		if !strings.Contains(got[0], want) {
			t.Errorf("notice %q lacks %q", got[0], want)
		}
	}
}
