package rt

import "testing"

// Db_dialect is the signal Std.Persist's SQL arm keys its dialect-aware,
// forced-semantics rendering off (LIKE vs ILIKE — BlueDB Phase-3 §0.6). The
// live SQLite≡Postgres≡embedded parity gate (examples/57-persist-parity) proves
// the rendered SQL end-to-end; this unit test pins the dialect classification a
// Postgres isn't required to check.
func TestDbDialect(t *testing.T) {
	cases := []struct {
		driver string
		want   string
	}{
		{"pgx", "postgres"},
		{"sqlite", "sqlite"},
		{"", "sqlite"},        // unknown driver → sqlite default
		{"anything", "sqlite"}, // never mis-classifies a non-pgx driver as postgres
	}
	for _, c := range cases {
		got := Db_dialect(&SkyDb{driver: c.driver})
		if got != c.want {
			t.Errorf("Db_dialect(driver=%q) = %q, want %q", c.driver, got, c.want)
		}
	}
	// A non-SkyDb arg falls back to the SQLite default rather than panicking.
	if got := Db_dialect(nil); got != "sqlite" {
		t.Errorf("Db_dialect(nil) = %q, want \"sqlite\"", got)
	}
}
