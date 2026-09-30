//go:build !js

package rt

// live_namespace.go — the session namespace of an app started with
// `Live.serve` / `App.serve` (A-2).
//
// Two apps in one process resolve the SAME session store from the
// environment (SKY_LIVE_STORE / sky.toml), and a browser sends one cookie to
// every port of a host (cookies ignore the port). Before v0.27.0 both apps
// used the cookie `sky_sid` and the same `sky_sessions` rows, so app B loaded
// app A's session for that id: a user signed in to the public app opened the
// admin port and the admin app rendered the signed-in model.
//
// The rule:
//
//   - the process-owning app (`Live.app`, `App.run`, including an
//     `App.withEmbedded` one) is NOT namespaced: its cookie stays `sky_sid`
//     and its session ids stay 32 hex characters, so every existing
//     deployment keeps its sessions;
//   - every served app (`Live.serve`, `App.serve`) is namespaced: by its
//     `App.withName` / `Live.withName` value, else by `port-<N>`. Port 0 with
//     no name and a durable store refuses to start: a port the kernel picks
//     changes at every boot, so the namespace (and every stored session)
//     would be lost at every restart.
//
// The namespace is carried by the session ID itself: a namespaced app mints
// `<32 hex>.<ns>` and adopts only ids with its own suffix. Every structure
// keyed by the session id (the store rows, the rotation aliases, the durable
// snapshots, the revocation binding rows, the SSE tickets, the ids derived
// from header-transport tokens) is therefore distinct per app with no second
// key. The cookie is `sky_sid_<ns>` (`__Host-sky_sid_<ns>` over HTTPS), so the
// browser keeps one credential per app.

import (
	"fmt"
	"strings"
	"sync"
)

// liveNamespaceMax bounds a namespace's length (it is part of a cookie name
// and of every session id).
const liveNamespaceMax = 40

// sanitiseLiveNamespace lower-cases name and keeps [a-z0-9_-]; any other
// character becomes '-'. ok is false when nothing usable is left.
func sanitiseLiveNamespace(name string) (string, bool) {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	ns := strings.Trim(b.String(), "-")
	if len(ns) > liveNamespaceMax {
		ns = ns[:liveNamespaceMax]
	}
	return ns, ns != ""
}

// validNamespaceChars reports whether s is a sanitised namespace.
func validNamespaceChars(s string) bool {
	if s == "" || len(s) > liveNamespaceMax {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// looksLikeLiveSID reports whether s has the shape of an id this runtime
// mints for ANY app: 32 hex, optionally followed by `.<ns>`.
func looksLikeLiveSID(s string) bool {
	if validSessionID(s) {
		return true
	}
	i := strings.IndexByte(s, '.')
	return i == 32 && validSessionID(s[:i]) && validNamespaceChars(s[i+1:])
}

// sidSuffix is the suffix a namespaced app puts on its session ids.
func (a *liveApp) sidSuffix() string {
	if a == nil || a.ns == "" {
		return ""
	}
	return "." + a.ns
}

// newSID mints a session id for this app.
func (a *liveApp) newSID() string {
	return newLiveSessionID() + a.sidSuffix()
}

// ownsSID reports whether s is a session id of this app: 32 hex for the
// process-owning app, `<32 hex>.<ns>` for a namespaced one. A presented id
// that fails this is never adopted, looked up or followed.
func (a *liveApp) ownsSID(s string) bool {
	suf := a.sidSuffix()
	if suf == "" {
		return validSessionID(s)
	}
	return strings.HasSuffix(s, suf) && validSessionID(s[:len(s)-len(suf)])
}

// presentedMayBeOwn filters an id a request PRESENTS before it is looked up
// in this app's store (which other apps may share). A namespaced app sees
// only its own ids. The process-owning app keeps its pre-v0.27 handling of
// any value (an unknown one is "session-lost"), except an id that carries
// another app's namespace, which is never looked up.
func (a *liveApp) presentedMayBeOwn(s string) bool {
	if a.sidSuffix() != "" {
		return a.ownsSID(s)
	}
	return !looksLikeLiveSID(s) || validSessionID(s)
}

// tokenSID is the session id a header-transport token stands for in this
// app (sessionTokenSID plus the app's suffix).
func (a *liveApp) tokenSID(token string) string {
	return sessionTokenSID(token) + a.sidSuffix()
}

// liveNamespaces holds the namespaces of the served apps running in this
// process: two apps with one namespace would share their sessions again.
var liveNamespaces sync.Map // ns -> struct{}

// errLiveNamespace is a served app's namespace refusal.
type errLiveNamespace struct{ msg string }

func (e errLiveNamespace) Error() string { return e.msg }

// claimLiveNamespace records ns for a served app. The returned func releases
// it (on stop, or on a failed start).
func claimLiveNamespace(ns string) (func(), error) {
	if _, dup := liveNamespaces.LoadOrStore(ns, struct{}{}); dup {
		return nil, errLiveNamespace{fmt.Sprintf(
			"another app in this process already uses the session namespace %q; "+
				"give each served app its own name: `|> App.withName \"<name>\"`; "+
				"see docs/migration/v0.27.md#served-app-namespace", ns)}
	}
	return func() { liveNamespaces.Delete(ns) }, nil
}

// resolveServedNamespace picks the namespace of a served app: the configured
// name, else `port-<configured port>`. For port 0 with no name the bound port
// names it, which is refused when the store is durable (the namespace, and
// every stored session, would change at every boot).
func resolveServedNamespace(cfg any, configuredPort, boundPort int, durableStore bool) (string, error) {
	if raw := stringField(cfg, "Name"); strings.TrimSpace(raw) != "" {
		ns, ok := sanitiseLiveNamespace(raw)
		if !ok {
			return "", errLiveNamespace{fmt.Sprintf(
				"App.withName %q has no usable character (use letters, digits, '-' or '_')", raw)}
		}
		return ns, nil
	}
	if configuredPort > 0 {
		return fmt.Sprintf("port-%d", configuredPort), nil
	}
	if durableStore {
		return "", errLiveNamespace{
			"a served app on port 0 with a durable session store needs a name: " +
				"the port changes at every boot, and with it the app's sessions. " +
				"Add `|> App.withName \"<name>\"`, or give the app a fixed port; " +
				"see docs/migration/v0.27.md#served-app-namespace"}
	}
	return fmt.Sprintf("port-%d", boundPort), nil
}

// Live_withName — `Live.withName : String -> AppConfig model msg -> AppConfig
// model msg`. Names a served app's session namespace (cookie `sky_sid_<name>`,
// session ids `<hex>.<name>`). Ignored by the process-owning app.
func Live_withName(name, cfg any) any {
	return liveCfgSet(cfg, "Name", name)
}
