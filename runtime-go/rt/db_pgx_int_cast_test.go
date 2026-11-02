//go:build !js

package rt

import "testing"

// pgIntSites finds the scalar and list Int marks of a rebound statement and
// nothing else (db_pgx_int_cast.go).
func TestPgIntSites_FindsOnlyIntMarks(t *testing.T) {
	q := `SELECT * FROM t WHERE (a = $1/*sky-int:t|a*/) AND (b = ANY($2/*sky-list:1001:int:t|b*/))` +
		` AND (c = ANY($3/*sky-list:1001*/)) AND (d = $4/*sky:t|d*/) AND e = '/*sky-int:t|e*/'` +
		` AND (f = $5/*sky-int:t|bad name*/) AND (g = ANY($6/*sky-list:2:reals:t|g*/))`
	sites := pgIntSites(q)
	if len(sites) != 2 {
		t.Fatalf("want 2 sites, got %+v", sites)
	}
	if s := sites[0]; q[s.start:s.end] != "$1/*sky-int:t|a*/" || s.list || s.col != "a" {
		t.Errorf("scalar site: %+v %q", s, q[s.start:s.end])
	}
	if s := sites[1]; q[s.start:s.end] != "$2/*sky-list:1001:int:t|b*/" || !s.list || s.col != "b" {
		t.Errorf("list site: %+v %q", s, q[s.start:s.end])
	}
}

// A statement with no Int mark is passed through without a catalogue read.
func TestPgIntCastQuery_NoMarkIsUntouched(t *testing.T) {
	q := `SELECT * FROM t WHERE a = $1 AND b = ANY($2/*sky-list:1001*/)`
	got, marked := pgIntCastQuery(nil, nil, q)
	if got != q || marked {
		t.Fatalf("got %q marked=%v", got, marked)
	}
}

// The list mark reads its `int` flag.
func TestParseListMark_IntFlag(t *testing.T) {
	m, ok := parseListMark("1001:int:t|b")
	if !ok || !m.int || m.reals || m.table != "t" || m.col != "b" || m.n != 1001 {
		t.Fatalf("got %+v ok=%v", m, ok)
	}
	if _, ok := parseListMark("1001:int:reals:t|b"); ok {
		t.Fatalf("int and reals together must be refused")
	}
}
