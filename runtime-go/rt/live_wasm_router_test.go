package rt

import "testing"

// H-3: the Sky.Spa router keeps only client routes in the app. A sign-out link
// to an App.api route, a console link and an unknown path are the server's.
func TestSpaLinkIsServerPath(t *testing.T) {
	routes := []spaRoute{
		Spa_route("/", "Home").(spaRoute),
		Spa_route("/blog", "Blog").(spaRoute),
		Spa_route("/blog/:slug", func(s string) any { return "Post " + s }).(spaRoute),
		Spa_route("/admin/login", "Login").(spaRoute),
	}
	server := []spaServerRoute{
		Spa_serverRoute("GET /admin/logout").(spaServerRoute),
		Spa_serverRoute("GET /admin/login").(spaServerRoute),
		Spa_serverRoute("POST /admin/save").(spaServerRoute),
		Spa_serverRoute("/files/").(spaServerRoute),
		Spa_serverRoute("/hook/:id").(spaServerRoute),
	}
	cases := []struct {
		path string
		want bool
	}{
		{"/", false},
		{"/blog", false},
		{"/blog/why", false},
		{"/blog/why%20not", false},
		{"/admin/logout", true},   // an App.api route
		{"/admin/login", true},    // a server route wins over a client route
		{"/admin/save", true},     // a POST-only server route; no client route either
		{"/_sky/console", true},   // the runtime's own paths
		{"/_sky", true},           //
		{"/_rpc/Anything", true},  //
		{"/files/a/b.png", true},  // a subtree mount
		{"/hook/42", true},        // a param server route
		{"/nowhere", true},        // no client route: the server answers
		{"/blog/why/extra", true}, //
	}
	for _, c := range cases {
		if got := spaLinkIsServerPath(routes, server, c.path); got != c.want {
			t.Errorf("spaLinkIsServerPath(%q) = %v, want %v", c.path, got, c.want)
		}
	}
	// A POST-only server route does not take a GET of a client page.
	if spaLinkIsServerPath([]spaRoute{Spa_route("/save", "Save").(spaRoute)}, []spaServerRoute{Spa_serverRoute("POST /save").(spaServerRoute)}, "/save") {
		t.Error("a POST-only server route must not take a link to a client page")
	}
}

// The server-only entries ride in the same route list; the client routing
// (asSpaRoutes) ignores them and asSpaServerRoutes picks them out.
func TestSpaServerRoutesRideInTheRouteList(t *testing.T) {
	list := []any{Spa_route("/", "Home"), Spa_serverRoute("GET /logout")}
	if got := asSpaRoutes(list); len(got) != 1 {
		t.Fatalf("client routes: want 1, got %d", len(got))
	}
	srv := asSpaServerRoutes(list)
	if len(srv) != 1 || srv[0].method != "GET" || srv[0].pattern != "/logout" {
		t.Fatalf("server routes: %+v", srv)
	}
}
