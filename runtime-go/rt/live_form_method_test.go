package rt

import (
	"strings"
	"testing"
)

// A <form> with a submit handler MUST render method="post". Without a method a
// native submit (before the client interceptor runs — the Sky.Spa wasm
// hydration window, or JS disabled) defaults to GET and leaks its fields
// (e.g. a password) into the URL. Regression for the darraghstudio Sky.Spa
// sign-in credential-leak-via-GET (2026-09-11).
func TestFormWithSubmitRendersMethodPost(t *testing.T) {
	n := VNode{
		Kind:  "element",
		Tag:   "form",
		SkyID: "r.1#form",
		Events: map[string]any{"submit": "DoSignIn"},
		Children: []VNode{
			{Kind: "element", Tag: "input", SkyID: "r.1#form.0", Attrs: map[string]string{"type": "password", "name": "password"}},
		},
	}
	html := renderVNode(n, nil)
	if !strings.Contains(html, `method="post"`) {
		t.Fatalf("submit-form must render method=\"post\" (GET leaks credentials); got:\n%s", html)
	}
}

// A user-set method (e.g. a GET search form) is left untouched — the default
// is only applied when no method was specified.
func TestFormRespectsUserSetMethod(t *testing.T) {
	n := VNode{
		Kind:   "element",
		Tag:    "form",
		SkyID:  "r.1#form",
		Attrs:  map[string]string{"method": "get"},
		Events: map[string]any{"submit": "DoSearch"},
	}
	html := renderVNode(n, nil)
	if !strings.Contains(html, `method="get"`) {
		t.Fatalf("user-set method=get must be preserved; got:\n%s", html)
	}
	if strings.Count(html, "method=") != 1 {
		t.Fatalf("must not emit a second method attr; got:\n%s", html)
	}
}

// A <form> with NO submit handler is not forced to POST.
func TestFormWithoutSubmitGetsNoMethod(t *testing.T) {
	n := VNode{Kind: "element", Tag: "form", SkyID: "r.1#form"}
	html := renderVNode(n, nil)
	if strings.Contains(html, "method=") {
		t.Fatalf("a form with no submit handler must not get a forced method; got:\n%s", html)
	}
}
