package rt

import "testing"

// These pin the Sky.Spa client address-bar sync (spa_urlsync.go) against the
// Sky.Live semantics it ports from live.go's __skyRunPaths. The DECISION is
// host-testable (no browser); the DOM/History glue in live_wasm.go is exercised
// only in the wasm build, so these cover the logic that used to be MISSING
// entirely on the client (a model-driven Navigate left the URL stale).

func TestSpaPathSyncPushOnPathChange(t *testing.T) {
	// A programmatic Navigate: the bar still shows the previous page, so a path
	// change is a real, Back-able navigation → pushState.
	op := spaPathSyncOp("/blog/hello", "/", "", true)
	if op.kind != "push" || op.url != "/blog/hello" {
		t.Fatalf("want push→/blog/hello, got %+v", op)
	}
}

func TestSpaPathSyncReplaceOnPathChangeWhenNotPush(t *testing.T) {
	// A full-body reconcile / mount / popstate: the URL is already correct, so a
	// differing marker reconciles with replaceState — never a second entry.
	op := spaPathSyncOp("/blog/hello", "/", "", false)
	if op.kind != "replace" || op.url != "/blog/hello" {
		t.Fatalf("want replace→/blog/hello, got %+v", op)
	}
}

func TestSpaPathSyncNoopWhenPathMatchesNoQuery(t *testing.T) {
	for _, push := range []bool{true, false} {
		op := spaPathSyncOp("/blog/hello", "/blog/hello", "", push)
		if op.kind != "none" {
			t.Fatalf("push=%v: want none, got %+v", push, op)
		}
	}
}

func TestSpaPathSyncStripsStaleQueryOnMatch(t *testing.T) {
	// live.go: `else if (location.search) replaceState(p)` — a matching path with
	// a lingering query strips the query (bare path), always via replace.
	op := spaPathSyncOp("/blog/hello", "/blog/hello", "?ref=x", true)
	if op.kind != "replace" || op.url != "/blog/hello" {
		t.Fatalf("want replace→/blog/hello (query stripped), got %+v", op)
	}
}

func TestSpaPathSyncEmptyMarkerIsNoop(t *testing.T) {
	if op := spaPathSyncOp("", "/anything", "?q=1", true); op.kind != "none" {
		t.Fatalf("empty marker must be a no-op, got %+v", op)
	}
}

func TestSpaQuerySyncReplaceOnChange(t *testing.T) {
	// A filter change: always replaceState (never grow Back history), path kept.
	op := spaQuerySyncOp("tag=go&page=2", "/blog", "")
	if op.kind != "replace" || op.url != "/blog?tag=go&page=2" {
		t.Fatalf("want replace→/blog?tag=go&page=2, got %+v", op)
	}
}

func TestSpaQuerySyncEmptyStripsExisting(t *testing.T) {
	op := spaQuerySyncOp("", "/blog", "?tag=go")
	if op.kind != "replace" || op.url != "/blog" {
		t.Fatalf("empty query must strip to bare path, got %+v", op)
	}
}

func TestSpaQuerySyncNoopWhenEqual(t *testing.T) {
	if op := spaQuerySyncOp("tag=go", "/blog", "?tag=go"); op.kind != "none" {
		t.Fatalf("want none when query already matches, got %+v", op)
	}
}

// Round-trip: a route → the page it resolves to, and the address-bar op the
// view's [data-sky-path] marker for that page produces. Landing on "/" and
// dispatching Navigate(BlogPost "hello") pushes /blog/hello; once there, the
// marker is idempotent (no-op); and resolving /blog/hello maps back to the page.
func TestSpaNavRouteURLRoundTrip(t *testing.T) {
	ctor := func(slug any) any { return map[string]any{"BlogPost": slug} }
	routes := []spaRoute{{path: "/blog/:slug", page: ctor}}

	// URL → route (deep-link / popstate direction).
	page, ok := spaResolveRoutes(routes, "/blog/hello")
	if !ok {
		t.Fatal("route /blog/:slug must match /blog/hello")
	}
	if m, isMap := page.(map[string]any); !isMap || m["BlogPost"] != "hello" {
		t.Fatalf("expected page {BlogPost:hello}, got %#v", page)
	}

	// Model → URL (the Navigate direction): from "/", the marker pushes.
	if op := spaPathSyncOp("/blog/hello", "/", "", true); op.kind != "push" || op.url != "/blog/hello" {
		t.Fatalf("Navigate from / should push /blog/hello, got %+v", op)
	}
	// Idempotent once the bar already matches (no double push on re-render).
	if op := spaPathSyncOp("/blog/hello", "/blog/hello", "", true); op.kind != "none" {
		t.Fatalf("re-render at /blog/hello must not push again, got %+v", op)
	}
}
