package rt

// v0.30.0: a routed page whose Go type the model's `page` field cannot hold.
// The checker refuses such a program ([E2001]), so this is defence in depth:
// before, every request panicked in RecordUpdate (`reflect.Set: value of type
// string is not assignable to type main.Main_Route`), and two enums of the
// same shape converted silently. The runtime now reports the mismatch once,
// in Sky words, and never writes the page.

import (
	"strings"
	"testing"
)

type Main_Route struct {
	Tag     int
	SkyName string
}

type Main_Screen int

type Main_Page int

type routedStructModel struct {
	Page  Main_Route
	Title string
}

type routedIotaModel struct {
	Page  Main_Page
	Title string
}

// captureRoutedPageFatal replaces the fatal hook for one test and returns the
// messages it received.
func captureRoutedPageFatal(t *testing.T) *[]string {
	t.Helper()
	var got []string
	prev := routedPageFatal
	routedPageFailed.Store(false)
	routedPageFatal = func(msg string) { got = append(got, msg) }
	t.Cleanup(func() {
		routedPageFatal = prev
		routedPageFailed.Store(false)
	})
	return &got
}

func routedTestApp(page, notFound any) *liveApp {
	app := namespaceTestApp()
	app.routes = []liveRoute{{path: "/", page: page}}
	app.notFound = notFound
	return app
}

func TestApplyRoute_StringPageIntoStructPageFieldFailsOnce(t *testing.T) {
	got := captureRoutedPageFatal(t)
	app := routedTestApp("home", "missing")
	model := routedStructModel{Page: Main_Route{Tag: 0, SkyName: "Dashboard"}, Title: "t"}
	for _, path := range []string{"/", "/no/such/page", "/"} {
		out, _ := applyRouteWithParams(app, model, path)
		m, ok := out.(routedStructModel)
		if !ok || m.Page != model.Page {
			t.Fatalf("%s: the model changed to %#v; a page of the wrong type must not be written", path, out)
		}
	}
	if len(*got) != 1 {
		t.Fatalf("the mismatch must be reported exactly once, got %d: %q", len(*got), *got)
	}
	msg := (*got)[0]
	for _, want := range []string{"String", "Route", "`page` field", "[E2001]"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the message must name %q:\n%s", want, msg)
		}
	}
}

func TestApplyRoute_OtherEnumPageIntoEnumPageFieldFailsOnce(t *testing.T) {
	got := captureRoutedPageFatal(t)
	app := routedTestApp(Main_Screen(1), Main_Screen(2))
	model := routedIotaModel{Page: Main_Page(0), Title: "t"}
	out, _ := applyRouteWithParams(app, model, "/")
	if m := out.(routedIotaModel); m.Page != Main_Page(0) {
		t.Fatalf("a Screen page was converted into the Page field: %#v", m)
	}
	if len(*got) != 1 || !strings.Contains((*got)[0], "Screen") || !strings.Contains((*got)[0], "Page") {
		t.Fatalf("the mismatch must be reported once, naming Screen and Page: %q", *got)
	}
}

func TestApplyRoute_PageOfTheFieldTypeIsWritten(t *testing.T) {
	got := captureRoutedPageFatal(t)
	detail := Main_Route{Tag: 1, SkyName: "Detail"}
	app := routedTestApp(detail, Main_Route{Tag: 2, SkyName: "Missing"})
	out, _ := applyRouteWithParams(app, routedStructModel{Title: "t"}, "/")
	if m := out.(routedStructModel); m.Page != detail {
		t.Fatalf("the routed page was not written: %#v", m)
	}
	out, _ = applyRouteWithParams(app, routedStructModel{Title: "t"}, "/zz")
	if m := out.(routedStructModel); m.Page.SkyName != "Missing" {
		t.Fatalf("the NotFound page was not written: %#v", m)
	}
	if len(*got) != 0 {
		t.Fatalf("a correct page must not be reported: %q", *got)
	}
}

func TestSpaSsrResolveModel_WrongPageTypeFailsOnce(t *testing.T) {
	got := captureRoutedPageFatal(t)
	model := routedStructModel{Page: Main_Route{SkyName: "Dashboard"}, Title: "t"}
	routes := []any{Spa_route("/", Main_Route{Tag: 1, SkyName: "Detail"})}
	out := Spa_ssrResolveModel(routes, "missing", model, "/zz")
	if m := out.(routedStructModel); m.Page != model.Page {
		t.Fatalf("a NotFound page of the wrong type was written: %#v", m)
	}
	if len(*got) != 1 {
		t.Fatalf("the mismatch must be reported once: %q", *got)
	}
}
