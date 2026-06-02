// Package console_app holds the Std.Ui Sky.Live console UI, translated
// to Go ONCE at compiler-release time by scripts/regenerate-console.sh
// and committed alongside the rest of the runtime.
//
// Why a subpackage of `sky-app/rt` rather than a peer?
//   - `runtime-go/` is embedded recursively into the Sky compiler
//     binary via TH (`Sky.Build.EmbeddedRuntime`), then re-materialised
//     into every user app's `sky-out/rt/`. Putting console_app inside
//     `rt/` is the only way to get it materialised alongside the rest
//     of the runtime without changing the embedding mechanism.
//   - The directory layout mirrors what user apps see at build time:
//       sky-out/main.go              package main         imports sky-app/rt
//       sky-out/rt/*.go              package rt
//       sky-out/rt/console_app/*.go  package console_app  imports sky-app/rt
//
// PR 1 status (v0.16.0):
//   - main.go contains the generated Go translation of
//     sky-bundled/console/src/*.sky.
//   - This file (mount.go) exposes MountInlineConsole — the public
//     entry point a host app calls to register the console handlers
//     onto an existing *http.ServeMux. The implementation is
//     intentionally minimal for PR 1: a single server-rendered HTML
//     view from the bundled Sky source. SSE / event dispatch / full
//     Sky.Live wiring lands in PR 2/3.
//   - rt (the parent package) cannot import console_app — that would
//     be an import cycle (console_app imports rt). Instead, the rt
//     side exposes rt.MountInlineConsole as a registration shim
//     (see runtime-go/rt/console_inline.go); console_app's init()
//     in register.go pushes its implementation into that shim.
//     Until something in the user's binary imports console_app
//     (blank import is sufficient), the shim returns
//     ErrInlineConsoleUnavailable. PR 2 adds the wiring in user
//     codegen + flips SKY_CONSOLE_MODE's default to inline.

package console_app

import (
	"fmt"
	"net/http"
	"strings"

	rt "sky-app/rt"
)

// MountInlineConsole registers the inline Sky Console handlers on
// `mux`. basePath is the same kind of prefix Sky.Live's MountSubApp
// would use ("" for "no prefix"; "/admin" for "this app sits under
// /admin/_sky/console/…"). The function is safe to call exactly
// once per mux; subsequent calls on the same mux + path will panic
// inside net/http when registering a duplicate pattern.
//
// What it serves (PR 1):
//   - GET <basePath>/_sky/console      — server-rendered HTML shell
//     produced by calling the bundled Sky.Live console's init/view
//     functions on a fresh Model. No JS bundling, no SSE patch
//     channel — the UI is static-rendered at request time. PR 2/3
//     wires up the full Sky.Live event + SSE plumbing.
//
// The JSON API endpoints (/_sky/console/api/*) are NOT touched here.
// They are mounted by rt.MountConsoleEndpoints (the existing
// console.go path) — keeping the two surfaces separate makes the PR
// 1 deletion-window (PR 2) cleaner: PR 2 removes the legacy HTML
// shell, but the JSON API stays exactly where it is.
func MountInlineConsole(mux *http.ServeMux, basePath string) error {
	if mux == nil {
		return fmt.Errorf("console_app: MountInlineConsole called with nil *http.ServeMux")
	}
	prefix := normaliseBasePath(basePath)
	path := prefix + "/_sky/console"
	// Two-arg signature: handle both /_sky/console and /_sky/console/
	// (Go's ServeMux treats trailing-slash as different patterns).
	mux.HandleFunc(path, handleConsoleRoot)
	if !strings.HasSuffix(path, "/") {
		mux.HandleFunc(path+"/", handleConsoleRoot)
	}
	return nil
}

// handleConsoleRoot renders the initial HTML view of the bundled
// console. The view is derived by calling the generated `init_` to
// produce a starter Model, then `viewWrapped` to produce a Sky.Html
// value, then rt.HtmlRender to flatten it to HTML.
//
// Authentication is intentionally NOT enforced here for PR 1 — the
// host app's mux will route via rt.consoleAccessAllowed in PR 2/3
// when this becomes the canonical mount path. For now the inline
// path is opt-in (SKY_CONSOLE_MODE=inline) so it never auto-mounts
// on a user app's listener.
func handleConsoleRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	defer func() {
		// Defensive: if the generated Sky code panics for any reason
		// (e.g. a stdlib version mismatch between the regen-time
		// runtime-go and the host's), serve a 500 with a diagnostic
		// hint rather than letting the panic propagate.
		if rec := recover(); rec != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w,
				"<!DOCTYPE html><html><body style=\"font-family:system-ui;padding:24px;\">"+
					"<h1>Sky Console — inline mount panic</h1>"+
					"<p>The bundled console UI panicked while rendering. This is a Sky compiler / "+
					"stdlib mismatch — regenerate via <code>scripts/regenerate-console.sh</code> "+
					"against the current runtime.</p>"+
					"<pre style=\"background:#f0f0f0;padding:12px;overflow:auto;\">%v</pre>"+
					"</body></html>",
					rec)
		}
	}()

	// Build the initial Model via the generated init_ function. The
	// init takes a request shape — pass an empty Dict-shaped value
	// because the bundled console doesn't read req fields (it reads
	// SKY_PARENT_URL from env instead).
	req := map[string]any{"path": "/", "query": ""}
	tuple := init_[any](req)
	model, ok := tuple.V0.(State_Model_R)
	if !ok {
		// Should never happen — init_ is statically typed to return
		// SkyTuple2{V0: State_Model_R}. Treat as compiler bug.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, "console_app: init_ returned unexpected V0 type %T", tuple.V0)
		return
	}
	htmlNode := viewWrapped(model)
	body := rt.HtmlRender(htmlNode)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Sky-Console-Mode", "inline")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusOK)
	// Wrap the body fragment in a minimal HTML5 document. The
	// generated view returns a layout-rooted Std.Ui tree, which when
	// rendered produces a self-contained `<div>` tree with inline
	// styles. We add the doctype + html/head/body shell here for
	// browser compatibility.
	fmt.Fprintf(w,
		"<!DOCTYPE html>\n"+
			"<html lang=\"en\">\n"+
			"<head>\n"+
			"  <meta charset=\"utf-8\">\n"+
			"  <meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\n"+
			"  <meta name=\"sky-console-mode\" content=\"inline\">\n"+
			"  <title>Sky Console</title>\n"+
			"  <style>html,body{margin:0;padding:0;background:#0f1115;color:#e4e6eb;font-family:ui-sans-serif,system-ui,-apple-system,Segoe UI,Roboto,Helvetica,Arial,sans-serif;}</style>\n"+
			"</head>\n"+
			"<body>%s</body>\n"+
			"</html>\n",
		body)
}

// normaliseBasePath mirrors rt.normaliseBasePath so callers don't need
// to know the prefix-cleaning rules. (Trimmed copy to avoid an
// exported-helper churn in rt itself, which PR 2 will revisit when
// the rt-side mounting code consolidates.)
//
// Rules:
//   - "" or "/" → ""        (no prefix; routes mount at /_sky/console)
//   - "/admin"  → "/admin"
//   - "admin"   → "/admin"  (leading slash inserted)
//   - "/admin/" → "/admin"  (trailing slash stripped)
func normaliseBasePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || p == "/" {
		return ""
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if strings.HasSuffix(p, "/") {
		p = strings.TrimRight(p, "/")
	}
	return p
}
