package rt

import "testing"

// The breakpoint is the largest listed width the viewport is at least, 0
// below all of them; the widths are kept positive, sorted and unique.
func TestBreakpointOfPicksTheLargestWidthReached(t *testing.T) {
	widths := normaliseBreakpoints([]any{900, 600, 0, -5, 600})
	if breakpointsHeader(widths) != "600,900" {
		t.Fatalf("widths = %v, want [600 900]", widths)
	}
	for w, want := range map[int]int{0: 0, 599: 0, 600: 600, 899: 600, 900: 900, 5000: 900} {
		if got := breakpointOf(w, widths); got != want {
			t.Errorf("breakpointOf(%d) = %d, want %d", w, got, want)
		}
	}
	if breakpointOf(1200, nil) != 0 {
		t.Error("no widths: always 0")
	}
}

// The breakpoint and query leaves are found through Sub.batch, the last leaf
// wins, and a tree without one yields nothing.
func TestBreakpointAndQueryLeavesAreFound(t *testing.T) {
	if _, _, ok := breakpointLeaf(Sub_every(1000, "Tick")); ok {
		t.Fatal("Sub.every has no breakpoint leaf")
	}
	if queryToMsg(Sub_none()) != nil {
		t.Fatal("Sub.none has no query leaf")
	}
	first := func(any) any { return "first" }
	last := func(any) any { return "last" }
	tree := Sub_batch([]any{
		Sub_onBreakpoint([]any{600}, first), Sub_onQuery(first),
		Sub_batch([]any{Sub_onBreakpoint([]any{300, 1200}, last), Sub_onQuery(last)}),
	})
	toMsg, widths, ok := breakpointLeaf(tree)
	if !ok || toMsg.(func(any) any)(nil) != "last" || breakpointsHeader(widths) != "300,1200" {
		t.Fatalf("the last breakpoint leaf must win: %v %v", ok, widths)
	}
	if q := queryToMsg(tree); q == nil || q.(func(any) any)(nil) != "last" {
		t.Fatal("the last query leaf must win")
	}
}

// The query is parsed like Http.parseQuery: percent-decoded, the first value
// of a repeated key, with or without the leading `?`.
func TestQueryDictParsesTheQueryString(t *testing.T) {
	d := queryDict("?page=2&sort=a%20b&page=9&flag").(SkyDict[any])
	get := func(k string) any { v, _ := d.Lookup(k); return v }
	if d.Len() != 3 || get("page") != "2" || get("sort") != "a b" || get("flag") != "" {
		t.Fatalf("queryDict = %d entries, page=%v sort=%v flag=%v", d.Len(), get("page"), get("sort"), get("flag"))
	}
	if queryDict("").(SkyDict[any]).Len() != 0 {
		t.Fatal("an empty query is an empty Dict")
	}
}
