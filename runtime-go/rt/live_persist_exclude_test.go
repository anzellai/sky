package rt

import (
	"net/http/httptest"
	"testing"
)

// App.withPersistExclude on Sky.Live (v0.29.0): a full page load of an
// existing session refreshes the named fields from `init`, and every other
// field keeps the session's value.

type persistExcludeModel struct {
	SiteConfig string `sky:"siteConfig,string"`
	Note       string `sky:"note,string"`
	Count      int    `sky:"count,int"`
}

func TestCopySkyFieldsCopiesOnlyTheNamedFields(t *testing.T) {
	stale := persistExcludeModel{SiteConfig: "old", Note: "draft", Count: 3}
	fresh := persistExcludeModel{SiteConfig: "new", Note: "", Count: 0}
	got := copySkyFields(stale, fresh, []string{"siteConfig", "missing"}).(persistExcludeModel)
	if got != (persistExcludeModel{SiteConfig: "new", Note: "draft", Count: 3}) {
		t.Fatalf("struct copy: %#v", got)
	}
	if stale.SiteConfig != "old" {
		t.Fatalf("the session's model must not be mutated in place")
	}
	m := copySkyFields(
		map[string]any{"siteConfig": "old", "note": "draft"},
		map[string]any{"siteConfig": "new", "note": ""},
		[]string{"siteConfig"},
	).(map[string]any)
	if m["siteConfig"] != "new" || m["note"] != "draft" {
		t.Fatalf("map copy: %#v", m)
	}
	if got := copySkyFields(stale, fresh, nil); got != stale {
		t.Fatalf("no names must keep the model")
	}
}

func TestLivePageLoadRefreshesExcludedFieldsFromInit(t *testing.T) {
	app := &liveApp{
		init: func(req any) any {
			return SkyTuple2{V0: persistExcludeModel{SiteConfig: "fresh"}, V1: nil}
		},
		persistExclude: []string{"siteConfig"},
	}
	r := httptest.NewRequest("GET", "/", nil)
	got := app.refreshPersistExcluded(persistExcludeModel{SiteConfig: "stale", Note: "kept", Count: 7}, r, "key")
	want := persistExcludeModel{SiteConfig: "fresh", Note: "kept", Count: 7}
	if got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if fieldOrNil(Live_withRefreshOnLoad([]any{"a", "b"}, map[string]any{}), "PersistExclude") == nil {
		t.Fatalf("Live.withRefreshOnLoad must set the config field")
	}
}
