package rt

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// UF-12: a limit under 1 MB used to read "Max 0MB".
func TestSpaFileTooLargeMessageNamesTheRealLimit(t *testing.T) {
	cases := map[int]string{
		500_000:   "500 KB",
		1_500_000: "1.5 MB",
		2_000_000: "2 MB",
		900:       "900 bytes",
	}
	for n, want := range cases {
		msg := spaFileTooLargeMessage(n)
		if !strings.Contains(msg, want) || strings.Contains(msg, " 0MB") {
			t.Errorf("limit %d: message %q, want it to name %q", n, msg, want)
		}
	}
}

// F10 / F11: the payload shapes the Spa driver hands to a handler.
func TestSpaPayloadShapes(t *testing.T) {
	if payloadString(true) != "true" || payloadString(false) != "false" || payloadString("k") != "k" {
		t.Fatalf("payloadString shapes wrong")
	}
	if !payloadBool(true) || payloadBool(false) || !payloadBool("true") || payloadBool("") {
		t.Fatalf("payloadBool shapes wrong")
	}
}

// SPA-5: a route param decodes identically on the Spa client (which matches
// location.pathname, percent-encoded) and on the server (Sky.Live matchRoute
// and the Spa SSR resolver both match Go's decoded r.URL.Path).
func TestSpaRouteParamDecodesLikeTheServer(t *testing.T) {
	for _, raw := range []string{"/u/J%C3%B6rg", "/u/a%20b", "/u/plain", "/u/%E2%9C%93"} {
		req := httptest.NewRequest("GET", raw, nil)
		serverParams, ok := matchRoute("/u/:name", req.URL.Path)
		if !ok {
			t.Fatalf("server did not match %s", raw)
		}
		// The browser reports the encoded form as location.pathname.
		clientParams, ok := spaMatchRoute("/u/:name", spaRoutePath(req.URL.EscapedPath()))
		if !ok {
			t.Fatalf("client did not match %s", raw)
		}
		if !reflect.DeepEqual(serverParams, clientParams) {
			t.Errorf("%s: server captured %q, Spa client captured %q", raw, serverParams, clientParams)
		}
	}
	if got := spaRoutePath("/bad/%zz"); got != "/bad/%zz" {
		t.Errorf("undecodable path changed: %q", got)
	}
}
