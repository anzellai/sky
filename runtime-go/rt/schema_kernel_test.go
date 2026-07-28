package rt

import (
	"strings"
	"testing"
)

// mkCol builds the map[string]any shape a Sky Column record lowers to, so the
// render path (Field-reads) is exercised exactly as at runtime.
func mkCol(name, kind string, mods map[string]any) map[string]any {
	c := map[string]any{
		"Name": name, "Kind": kind,
		"IsPk": false, "IsNotNull": false, "IsUnique": false, "IsAutoInc": false,
		"DefaultKind": "none", "DefaultVal": "", "ForeignKey": "",
	}
	for k, v := range mods {
		c[k] = v
	}
	return c
}

func mkTable(name string, cols []any, indexes []any) map[string]any {
	if indexes == nil {
		indexes = []any{}
	}
	return map[string]any{"Name": name, "Columns": cols, "Indexes": indexes}
}

func TestSchemaDialectMapping(t *testing.T) {
	tbl := mkTable("products", []any{
		mkCol("id", "text", map[string]any{"IsPk": true}),
		mkCol("slug", "text", map[string]any{"IsNotNull": true, "IsUnique": true}),
		mkCol("price_minor", "int", map[string]any{"IsNotNull": true, "DefaultKind": "int", "DefaultVal": "0"}),
		mkCol("active", "bool", map[string]any{"IsNotNull": true, "DefaultKind": "bool", "DefaultVal": "true"}),
		mkCol("created_at", "bigint", map[string]any{"IsNotNull": true, "DefaultKind": "int", "DefaultVal": "0"}),
		mkCol("seq", "bigint", map[string]any{"IsPk": true, "IsAutoInc": true}),
	}, []any{
		map[string]any{"Name": "idx_products_slug", "Columns": []any{"slug"}, "IsUniqueIndex": false},
	})

	sqlite := schemaRenderTable("sqlite", tbl)
	pg := schemaRenderTable("pgx", tbl)

	// The CREATE TABLE is element 0; the index is element 1.
	if len(sqlite) != 2 || len(pg) != 2 {
		t.Fatalf("expected 2 statements each; got sqlite=%d pg=%d", len(sqlite), len(pg))
	}
	sq, pq := sqlite[0], pg[0]

	// --- the dialect-critical divergences ---
	// bigint: INTEGER (sqlite) vs BIGINT (pg) — the millis-overflow fix.
	if !strings.Contains(sq, "created_at INTEGER") {
		t.Errorf("sqlite bigint should be INTEGER:\n%s", sq)
	}
	if !strings.Contains(pq, "created_at BIGINT") {
		t.Errorf("pg bigint should be BIGINT:\n%s", pq)
	}
	// auto-increment PK: INTEGER PRIMARY KEY AUTOINCREMENT vs BIGSERIAL PRIMARY KEY.
	if !strings.Contains(sq, "seq INTEGER PRIMARY KEY AUTOINCREMENT") {
		t.Errorf("sqlite serial wrong:\n%s", sq)
	}
	if !strings.Contains(pq, "seq BIGSERIAL PRIMARY KEY") {
		t.Errorf("pg serial wrong:\n%s", pq)
	}

	// --- read-consistency choices: bool is INTEGER on BOTH ---
	if !strings.Contains(sq, "active INTEGER") || !strings.Contains(pq, "active INTEGER") {
		t.Errorf("bool should be INTEGER on both:\nsqlite=%s\npg=%s", sq, pq)
	}
	if !strings.Contains(sq, "DEFAULT 1") || !strings.Contains(pq, "DEFAULT 1") {
		t.Errorf("bool default should be 1 on both:\nsqlite=%s\npg=%s", sq, pq)
	}

	// --- shared structure ---
	for _, q := range []string{sq, pq} {
		if !strings.Contains(q, "CREATE TABLE IF NOT EXISTS products") {
			t.Errorf("missing CREATE TABLE:\n%s", q)
		}
		if !strings.Contains(q, "id TEXT PRIMARY KEY") {
			t.Errorf("text PK wrong:\n%s", q)
		}
		if !strings.Contains(q, "slug TEXT NOT NULL UNIQUE") {
			t.Errorf("slug modifiers wrong:\n%s", q)
		}
		if !strings.Contains(q, "price_minor INTEGER NOT NULL DEFAULT 0") {
			t.Errorf("int default wrong:\n%s", q)
		}
	}

	// index (same on both)
	if !strings.Contains(sqlite[1], "CREATE INDEX IF NOT EXISTS idx_products_slug ON products (slug)") {
		t.Errorf("index DDL wrong:\n%s", sqlite[1])
	}
}

func TestSchemaDefaultsAndFk(t *testing.T) {
	if got := schemaDefault("pgx", "now", ""); got != "now()" {
		t.Errorf("pg now default = %q", got)
	}
	if got := schemaDefault("sqlite", "now", ""); got != "(datetime('now'))" {
		t.Errorf("sqlite now default = %q", got)
	}
	if got := schemaDefault("pgx", "text", "O'Brien"); got != "'O''Brien'" {
		t.Errorf("text default escaping = %q", got)
	}
	col := mkCol("order_id", "text", map[string]any{"ForeignKey": "orders(id)"})
	if got := schemaRenderColumn("pgx", col); !strings.Contains(got, "REFERENCES orders(id)") {
		t.Errorf("fk wrong: %q", got)
	}
}
