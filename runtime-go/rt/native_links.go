package rt

import (
	"encoding/json"
	"net/url"
	"strings"
)

// Universal links into the macOS desktop app (Bundle.AssociatedDomain
// "applinks:<host>"). The iOS shell (App.swift, `linkTarget`) and the Android
// shell (MainActivity.java, `linkTarget`) route an incoming link the same way;
// this is the desktop window's copy, pure so it is tested on every build
// (native_links_test.go). The Cocoa side that receives the link and drives the
// WKWebView is native_desktop_links_darwin.go.

// desktopLinkHostDeclared reports whether `host` is one of the app's
// `applinks:` hosts. A `*.` entry matches its subdomains and the domain
// itself, as on iOS and Android.
func desktopLinkHostDeclared(hosts []string, host string) bool {
	host = strings.ToLower(host)
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(h, "*."); ok {
			if host == rest || strings.HasSuffix(host, "."+rest) {
				return true
			}
		} else if host == h {
			return true
		}
	}
	return false
}

// desktopLinkTarget is the backend address an incoming link opens: the
// link's path, query and fragment on the backend's own origin (`base`, the
// address the window loads), where the app's router shows the page. It is ""
// for a link that is not http(s), or whose host the app did not declare: the
// app then ignores it and stays where it is.
func desktopLinkTarget(hosts []string, base, link string) string {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || u.Hostname() == "" {
		return ""
	}
	if s := strings.ToLower(u.Scheme); s != "https" && s != "http" {
		return ""
	}
	if !desktopLinkHostDeclared(hosts, u.Hostname()) {
		return ""
	}
	b, err := url.Parse(strings.TrimSpace(base))
	if err != nil || b.Scheme == "" || b.Host == "" {
		return ""
	}
	basePath := strings.TrimSuffix(b.EscapedPath(), "/")
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	out := b.Scheme + "://" + b.Host + basePath + path
	if u.RawQuery != "" || u.ForceQuery {
		out += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		out += "#" + u.EscapedFragment()
	}
	return out
}

// desktopLinkInPlaceJS is the script that opens `target` in a page that is
// already the app: the client router navigates in place (history.pushState +
// popstate, as Back / Forward does), so the app keeps its state. It returns
// "" when `target` does not parse.
func desktopLinkInPlaceJS(target string) string {
	u, err := url.Parse(target)
	if err != nil {
		return ""
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" || u.ForceQuery {
		path += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		path += "#" + u.EscapedFragment()
	}
	quoted, _ := json.Marshal(path)
	return "history.pushState(null, '', " + string(quoted) + "); " +
		"dispatchEvent(new PopStateEvent('popstate', { state: null }));"
}
