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
