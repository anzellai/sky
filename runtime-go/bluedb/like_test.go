package bluedb

import "testing"

// likeMatch is the embedded arm's SQL LIKE — forced case-insensitive ASCII (§0.6)
// so a Persist LIKE is byte-identical across embedded / SQLite / Postgres (ILIKE).
func TestLikeMatchCaseInsensitiveASCII(t *testing.T) {
	cases := []struct {
		s, pat string
		want   bool
	}{
		{"Alice", "%a%", true},   // capital 'A' matches lowercase pattern (case-insensitive)
		{"alice", "%A%", true},   // and the reverse
		{"Bob", "%a%", false},    // no 'a'/'A' anywhere
		{"Cara", "%a%", true},    // lowercase 'a'
		{"Dan", "d%", true},      // leading, case-insensitive
		{"Dan", "D_N", true},     // '_' single-char wildcard, case-insensitive
		{"Dan", "dan", true},     // exact, case-folded
		{"Boston", "%OS%", true}, // interior, case-insensitive
		{"Berlin", "berl%", true},
		{"", "%", true},
		{"x", "", false},
	}
	for _, c := range cases {
		if got := likeMatch(c.s, c.pat); got != c.want {
			t.Errorf("likeMatch(%q, %q) = %v, want %v", c.s, c.pat, got, c.want)
		}
	}
}
