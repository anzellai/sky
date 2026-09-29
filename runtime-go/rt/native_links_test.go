package rt

import "testing"

// The macOS desktop window routes a universal link the way the iOS and
// Android shells do: only a declared host, onto the backend's own origin,
// keeping the path, query and fragment. Before v0.27.0 the desktop window did
// not route a link at all and the app opened on its first page.
func TestDesktopLinkTargetMapsADeclaredLinkOntoTheBackend(t *testing.T) {
	hosts := []string{"example.com", "*.shop.example"}
	cases := []struct {
		base, link, want string
	}{
		// The link's path, query and fragment on the backend's origin.
		{"http://127.0.0.1:8951/", "https://example.com/orders/7?tab=a%20b#top",
			"http://127.0.0.1:8951/orders/7?tab=a%20b#top"},
		// A backend under a path keeps it.
		{"https://app.example.net/base/", "https://example.com/probe/deep",
			"https://app.example.net/base/probe/deep"},
		// No path is the root.
		{"https://app.example.net", "https://example.com", "https://app.example.net/"},
		// Host case does not matter; a port on the link is not the backend's.
		{"https://app.example.net/", "https://EXAMPLE.com:8443/a", "https://app.example.net/a"},
		// A wildcard matches a subdomain and the domain itself.
		{"https://app.example.net/", "https://eu.shop.example/x", "https://app.example.net/x"},
		{"https://app.example.net/", "https://shop.example/x", "https://app.example.net/x"},
		// An escaped path stays escaped.
		{"https://app.example.net/", "https://example.com/a%2Fb/c%20d", "https://app.example.net/a%2Fb/c%20d"},
		// Not declared: ignored.
		{"https://app.example.net/", "https://other.example.org/x", ""},
		{"https://app.example.net/", "https://notexample.com/x", ""},
		{"https://app.example.net/", "https://evilshop.example/x", ""},
		// Not a web link: ignored.
		{"https://app.example.net/", "mailto:someone@example.com", ""},
		{"https://app.example.net/", "file:///etc/passwd", ""},
		{"https://app.example.net/", "sky-probe://example.com/x", ""},
		{"https://app.example.net/", "", ""},
	}
	for _, c := range cases {
		if got := desktopLinkTarget(hosts, c.base, c.link); got != c.want {
			t.Errorf("desktopLinkTarget(%q, %q) = %q, want %q", c.base, c.link, got, c.want)
		}
	}
	// An app that declares no domain takes no link.
	if got := desktopLinkTarget(nil, "https://app.example.net/", "https://example.com/x"); got != "" {
		t.Errorf("no declared host: got %q, want none", got)
	}
}

// A link that arrives while the app runs navigates the client router in
// place, with the path as a JSON string (a quote in it cannot end the
// script).
func TestDesktopLinkInPlaceJSPushesThePathAndFiresPopstate(t *testing.T) {
	got := desktopLinkInPlaceJS("http://127.0.0.1:8951/probe/again?x=1#f")
	want := `history.pushState(null, '', "/probe/again?x=1#f"); ` +
		`dispatchEvent(new PopStateEvent('popstate', { state: null }));`
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	quoted := desktopLinkInPlaceJS(`http://h/a'b"c`)
	if want := `history.pushState(null, '', "/a%27b%22c"); `; len(quoted) < len(want) || quoted[:len(want)] != want {
		t.Errorf("a quote must stay inside the JSON string: %q", quoted)
	}
}
