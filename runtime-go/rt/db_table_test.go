package rt

import (
	"strings"
	"testing"
)

func TestDbFieldToColumn(t *testing.T) {
	cases := map[string]string{
		"Id":            "id",
		"Slug":          "slug",
		"PriceMinor":    "price_minor",
		"ImageUrl":      "image_url",
		"CreatedAt":     "created_at",
		"IsGuest":       "is_guest",
		"ShippingClass": "shipping_class",
	}
	for in, want := range cases {
		if got := dbFieldToColumn(in); got != want {
			t.Errorf("dbFieldToColumn(%q) = %q, want %q", in, got, want)
		}
	}
}

// A struct mirroring what a Sky record lowers to (exported PascalCase fields).
type tblProduct struct {
	Id         string
	Slug       string
	PriceMinor int
	Active     bool
	Note       SkyMaybe[string]
}

func TestDbTableToSchemaMap(t *testing.T) {
	tbl := Table_primaryKey("id",
		Table_unique("slug",
			Table_table("products", tblProduct{})))

	sm, ok := dbTableToSchemaMap(tbl)
	if !ok {
		t.Fatal("dbTableToSchemaMap failed")
	}
	// Render for both dialects and check the derived columns.
	sqlite := schemaRenderTable("sqlite", sm)[0]
	pg := schemaRenderTable("pgx", sm)[0]

	for _, want := range []string{"id TEXT PRIMARY KEY", "slug TEXT NOT NULL UNIQUE", "price_minor"} {
		if !strings.Contains(sqlite, want) {
			t.Errorf("sqlite DDL missing %q:\n%s", want, sqlite)
		}
	}
	// Int → INTEGER on sqlite, BIGINT on pg.
	if !strings.Contains(sqlite, "price_minor INTEGER") {
		t.Errorf("sqlite int should be INTEGER:\n%s", sqlite)
	}
	if !strings.Contains(pg, "price_minor BIGINT") {
		t.Errorf("pg int should be BIGINT:\n%s", pg)
	}
	// Bool → INTEGER on sqlite, BOOLEAN on pg.
	if !strings.Contains(sqlite, "active INTEGER") || !strings.Contains(pg, "active BOOLEAN") {
		t.Errorf("bool mapping wrong:\nsqlite=%s\npg=%s", sqlite, pg)
	}
	// Maybe String → nullable TEXT (no NOT NULL).
	if !strings.Contains(sqlite, "note TEXT") || strings.Contains(sqlite, "note TEXT NOT NULL") {
		t.Errorf("Maybe field should be nullable TEXT:\n%s", sqlite)
	}
}

func TestDbEncodeRecordColumns(t *testing.T) {
	tbl := Table_table("products", tblProduct{})
	cols, vals, err := dbEncodeRecord(tbl, tblProduct{Id: "p1", Slug: "w", PriceMinor: 500, Active: true, Note: Just("hi")})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"id", "slug", "price_minor", "active", "note"}
	if len(cols) != len(want) {
		t.Fatalf("cols = %v, want %v", cols, want)
	}
	for i, w := range want {
		if cols[i] != w {
			t.Errorf("col[%d] = %q, want %q", i, cols[i], w)
		}
	}
	if len(vals) != len(want) {
		t.Errorf("vals length = %d, want %d", len(vals), len(want))
	}
}
