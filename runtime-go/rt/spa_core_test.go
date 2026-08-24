package rt

import "testing"

// The String-param route (Spa_route) applies the captured segment verbatim to
// the page constructor — the pre-existing behaviour, pinned so the Int variant
// cannot regress it.
func TestSpaResolveStringParam(t *testing.T) {
	ctor := func(s any) any { return s }
	routes := []spaRoute{{path: "/thing/:name", page: ctor}}
	p, ok := spaResolveRoutes(routes, "/thing/widget")
	if !ok || p != "widget" {
		t.Fatalf("expected string param 'widget', got %#v ok=%v", p, ok)
	}
}

// The Int-param route (Spa_routeInt) parses the captured segment to a Go int
// (Sky Int == Go int) before applying it to the constructor.
func TestSpaResolveIntParam(t *testing.T) {
	ctor := func(id any) any { return id }
	routes := []spaRoute{{path: "/todo/:id", page: ctor, intParam: true}}

	page, ok := spaResolveRoutes(routes, "/todo/42")
	if !ok {
		t.Fatal("expected /todo/42 to match the Int route")
	}
	n, isInt := page.(int)
	if !isInt || n != 42 {
		t.Fatalf("expected a Go int 42, got %#v", page)
	}
}

// A non-integer segment makes an Int route NOT match — it must fall through to a
// later route or the 404, never construct a bogus page from an unparseable id.
func TestSpaIntParamNonIntegerDoesNotMatch(t *testing.T) {
	ctor := func(id any) any { return id }
	routes := []spaRoute{{path: "/todo/:id", page: ctor, intParam: true}}
	if p, ok := spaResolveRoutes(routes, "/todo/abc"); ok {
		t.Fatalf("a non-integer segment must not match the Int route, got %#v", p)
	}
}

// Ordering + fall-through: a literal route before the Int route wins for its
// exact path; the Int route claims numeric ids; a non-numeric id matches
// neither.
func TestSpaIntParamOrderingAndFallThrough(t *testing.T) {
	ctorInt := func(id any) any { return map[string]any{"todo": id} }
	routes := []spaRoute{
		{path: "/todo/new", page: "NEW"},                       // literal first
		{path: "/todo/:id", page: ctorInt, intParam: true},     // then the Int param
	}

	if p, ok := spaResolveRoutes(routes, "/todo/new"); !ok || p != "NEW" {
		t.Fatalf("the literal /todo/new must win, got %#v ok=%v", p, ok)
	}

	p, ok := spaResolveRoutes(routes, "/todo/7")
	if !ok {
		t.Fatal("the Int route must match /todo/7")
	}
	if m, isMap := p.(map[string]any); !isMap || m["todo"].(int) != 7 {
		t.Fatalf("expected the Int route to build {todo:7}, got %#v", p)
	}

	if p, ok := spaResolveRoutes(routes, "/todo/abc"); ok {
		t.Fatalf("neither route should match /todo/abc, got %#v", p)
	}
}
