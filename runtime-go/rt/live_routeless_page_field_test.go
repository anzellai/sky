package rt

// v0.30.0 (F6): a Sky.Live app with no routes and a NotFound page
// (`App.withNotFound ()` and no `App.withRoutes`) had the NotFound page
// written into its model's `page` field on every request. With no routes the
// runtime owns no page type, so the field is the user's own field of any
// type: a `page : String` field was overwritten with `()` and the view
// rendered `{}`. The runtime now writes the page field only when the app has
// routes. An unknown path on a route-less app still answers 404.

import (
	"net/http"
	"strings"
	"testing"
)

func routelessPageFieldApp() *liveApp {
	app := namespaceTestApp()
	app.routes = nil
	// The NotFound page of a route-less app: a value of a type the model's
	// `page` field does not have (unit, as `App.withNotFound ()` gives).
	app.notFound = struct{}{}
	app.init = func(req any) any {
		return SkyTuple2{V0: map[string]any{"Page": "kept-page-value"}, V1: cmdT{kind: "none"}}
	}
	app.view = func(model any) any {
		v := "<missing>"
		if m, ok := model.(map[string]any); ok {
			if p, ok := m["Page"].(string); ok {
				v = p
			} else {
				v = "<not a string>"
			}
		}
		return velement("div", nil, []any{vtext("page=" + v)})
	}
	return app
}

func TestApplyRoute_RoutelessAppKeepsUserPageField(t *testing.T) {
	app := routelessPageFieldApp()
	for _, path := range []string{"/", "/no/such/page"} {
		model := map[string]any{"Page": "kept-page-value"}
		got, _ := applyRouteWithParams(app, model, path)
		m, ok := got.(map[string]any)
		if !ok {
			t.Fatalf("%s: model is %T, want the record", path, got)
		}
		if p, ok := m["Page"].(string); !ok || p != "kept-page-value" {
			t.Fatalf("%s: the route-less app's page field became %#v, want \"kept-page-value\"", path, m["Page"])
		}
	}
}

func TestLiveRoutelessAppRendersUserPageField(t *testing.T) {
	app := routelessPageFieldApp()
	for _, tc := range []struct {
		path string
		code int
	}{{"/", http.StatusOK}, {"/no/such/page", http.StatusNotFound}} {
		rr := liveGet(app, http.MethodGet, tc.path, nil, nil)
		if rr.Code != tc.code {
			t.Fatalf("GET %s: status %d, want %d", tc.path, rr.Code, tc.code)
		}
		if body := rr.Body.String(); !strings.Contains(body, "page=kept-page-value") {
			t.Fatalf("GET %s: the view did not see the user's page field:\n%s", tc.path, truncate(body, 600))
		}
	}
}

// An app with routes keeps its routing: an unknown path writes the NotFound
// page into the page field the router owns.
func TestApplyRoute_RoutedAppStillWritesNotFoundPage(t *testing.T) {
	app := namespaceTestApp()
	got, _ := applyRouteWithParams(app, map[string]any{"Page": "home"}, "/no/such/page")
	if p := got.(map[string]any)["Page"]; p != "notFound" {
		t.Fatalf("routed app, unknown path: Page = %#v, want \"notFound\"", p)
	}
}

// The Sky.Spa server render resolves a path the way the wasm client does
// (spaApplyURL): with no routes it never writes the page field.
func TestSpaSsrResolveModel_RoutelessAppKeepsUserPageField(t *testing.T) {
	model := map[string]any{"Page": "kept-page-value"}
	got := Spa_ssrResolveModel([]any{}, struct{}{}, model, "/no/such/page")
	if p := got.(map[string]any)["Page"]; p != "kept-page-value" {
		t.Fatalf("route-less Spa SSR: Page = %#v, want \"kept-page-value\"", p)
	}
}
