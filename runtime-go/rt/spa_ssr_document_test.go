//go:build !js

package rt

import (
	"strings"
	"testing"
)

// The Sky.Spa SSR page writes the app's <html> attributes (Spa.withDocument).
// The runtime's data-sky-hydrating marker stays, after the app's attributes.
func TestSpaSSRWritesHtmlAttributes(t *testing.T) {
	page := SpaSSRPage("", "<p>hi</p>", "main.wasm", "{}")
	doc := docFn(docAttrADT("lang", "ar"), docAttrADT("dir", "rtl"), docAttrADT("class", "a\"<&\nb"))
	got := Spa_ssrWithDocument(doc, nil, page)
	want := "<html lang=\"ar\" dir=\"rtl\" class=\"a&#34;&lt;&amp;\nb\" data-sky-hydrating=\"1\">"
	if !strings.Contains(got, want) {
		t.Fatalf("the SSR page must carry the app's attributes:\n%.300s", got)
	}
	if strings.Contains(got, `lang="en"`) {
		t.Fatalf("the hard-coded lang=\"en\" must go when the app sets lang:\n%.300s", got)
	}
	if strings.Count(got, "<html") != 1 {
		t.Fatalf("exactly one <html> start tag:\n%.300s", got)
	}
}

// A page whose app sets no lang keeps lang="en", so no existing page loses
// its language; an app with no Spa.withDocument (or an empty list) gets the
// page exactly as before.
func TestSpaSSRDocumentDefaults(t *testing.T) {
	page := SpaSSRPage("", "<p>hi</p>", "main.wasm", "{}")
	if !strings.Contains(page, `<html lang="en" data-sky-hydrating="1">`) {
		t.Fatalf("the default page must keep lang=\"en\":\n%.300s", page)
	}
	if got := Spa_ssrWithDocument(nil, nil, page); got != page {
		t.Fatalf("no document function must leave the page unchanged")
	}
	if got := Spa_ssrWithDocument(docFn(), nil, page); got != page {
		t.Fatalf("an empty list must leave the page unchanged")
	}
	got := Spa_ssrWithDocument(docFn(docAttrADT("dir", "rtl")), nil, page)
	if !strings.Contains(got, `<html lang="en" dir="rtl" data-sky-hydrating="1">`) {
		t.Fatalf("a set with no lang must keep lang=\"en\":\n%.300s", got)
	}
	bad := Spa_ssrWithDocument(docFn(docAttrADT("lang", "en GB")), nil, page)
	if !strings.Contains(bad, `<html lang="en" data-sky-hydrating="1">`) {
		t.Fatalf("a malformed lang is dropped and the default stays:\n%.300s", bad)
	}
}
