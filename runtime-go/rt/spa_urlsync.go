package rt

import "strings"

// spa_urlsync.go — the Sky.Spa client's address-bar sync, ported from Sky.Live's
// injected `__skyRunPaths` (live.go). Sky.Live honours a `[data-sky-path]` /
// `[data-sky-query]` marker emitted by the view on a model-driven `Navigate`,
// pushing / replacing browser history so the address bar tracks the page; the
// wasm client had NO such handling, so a client `Navigate` updated content but
// left the URL stale (Back/forward + reload-at-URL then broke).
//
// The DECISION — given a marker value, the current location, and the caller's
// push-vs-replace intent, what History-API op to run — is portable and lives
// here so it is host-testable without a browser. The DOM read + History write
// glue is in live_wasm.go (//go:build js) and calls straight into these.
//
// Semantics are copied VERBATIM from live.go's __skyRunPaths (the comment block
// at live.go ~6692). Do NOT diverge: paths push (a real, Back-able navigation)
// or replace (full-body reconcile where the URL is already correct); a stale
// query on a matching path is stripped with replace; the query marker ALWAYS
// replaces (a filter change must not grow the Back history).

// spaURLOp is the History-API operation a marker resolves to: a no-op, a
// pushState, or a replaceState to `url`.
type spaURLOp struct {
	kind string // "none" | "push" | "replace"
	url  string
}

// spaPathSyncOp mirrors live.go __skyRunPaths' per-`[data-sky-path]` rule EXACTLY:
//
//   - the marker path differs from location.pathname → pushState (a programmatic
//     Navigate, where the address bar still shows the previous page) or
//     replaceState (a full-body reconcile / mount / popstate, where the correct
//     URL is already in the bar), to the bare path;
//   - the marker path matches but a query string is present → replaceState to the
//     bare path, stripping the stale query;
//   - otherwise → no-op.
//
// `push` is the caller's intent, identical to __skyRunPaths' second arg:
// true for an SSE-driven / msg-driven patch, false for a full-body reconcile.
func spaPathSyncOp(dataSkyPath, curPathname, curSearch string, push bool) spaURLOp {
	if dataSkyPath == "" {
		return spaURLOp{kind: "none"}
	}
	if curPathname != dataSkyPath {
		if push {
			return spaURLOp{kind: "push", url: dataSkyPath}
		}
		return spaURLOp{kind: "replace", url: dataSkyPath}
	}
	if curSearch != "" {
		return spaURLOp{kind: "replace", url: dataSkyPath}
	}
	return spaURLOp{kind: "none"}
}

// spaQuerySyncOp mirrors live.go __skyRunPaths' per-`[data-sky-query]` rule: the
// value is the raw query string (no leading '?'); an empty value means "strip any
// existing query". It ALWAYS replaceStates (never pushes) — a filter change must
// not grow the Back-button history — preserving the current path. A value already
// equal to the current query is a no-op.
func spaQuerySyncOp(dataSkyQuery, curPathname, curSearch string) spaURLOp {
	current := strings.TrimPrefix(curSearch, "?")
	if dataSkyQuery == current {
		return spaURLOp{kind: "none"}
	}
	target := curPathname
	if dataSkyQuery != "" {
		target = curPathname + "?" + dataSkyQuery
	}
	return spaURLOp{kind: "replace", url: target}
}
