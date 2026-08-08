package rt

// Phase-5d (grill B3) / G1: STRUCTURAL enforcement of the persist-before-ack funnel.
//
// Every path that mutates the session Model and ACKs it (ships an SSE frame) must
// persist the session BEFORE the ack — else a crash loses a change the user saw
// succeed (grill A1). Rather than a per-site count, the async server-initiated
// frames ship through exactly ONE helper, app.persistAndShipFrame, which
// persists-before-ack. So there is exactly ONE raw `sess.sseCh <-` send in the
// whole package, and it is inside persistAndShipFrame.
//
// (The 3 fanOutFrame broadcast sites are the multi-connection paths: the sky-nav
// mirror and the batch flush persist separately just before fanning out, and
// ensureSSERelay re-sends ALREADY-shipped frames — no new Model mutation. They are
// pinned so a new broadcast site gets reviewed for persist-before-ack.)
//
// G1 fixed two ways this test used to be VACUOUS — it could not detect the loss it
// exists to protect:
//
//  1. NO PREDICATE MENTIONED THE PERSIST. It asserted a send count, a position, and
//     a fanOutFrame count. Deleting `app.store.Set(sess.sid, sess)` from the funnel
//     body left all three TRUE, so the test passed while the invariant it names was
//     gone. Now the funnel body itself is asserted to contain the store.Set.
//
//  2. IT READ ONLY live.go. The package had THREE raw sends — live.go,
//     bluedb_reactive.go (reactiveRefreshOnce) and websocket.go (dispatchOneWsSub) —
//     so the stated "exactly ONE raw send" invariant was already false at HEAD, and
//     that is precisely how those two unpersisted bypasses shipped unnoticed. The
//     scan now covers every non-test .go file in the package.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// packageGoSources returns every non-test .go file in the package, as name→content.
// Scanning the WHOLE package (not just live.go) is the point: a raw send added in
// any file must trip this test.
func packageGoSources(t *testing.T) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		out[name] = string(b)
	}
	if len(out) == 0 {
		t.Fatal("no package sources found — this tripwire would be vacuous")
	}
	return out
}

// rawSseSendSites lists `file:line` for every CODE (non-comment) line that sends on
// a session's sseCh. Comment lines are skipped so the funnel's own doc comment does
// not count itself; receives (`<-s.sseCh`) do not match the `sseCh <-` shape.
func rawSseSendSites(srcs map[string]string) []string {
	var sites []string
	for name, s := range srcs {
		for i, line := range strings.Split(s, "\n") {
			code := strings.TrimSpace(line)
			if strings.HasPrefix(code, "//") {
				continue
			}
			if strings.Contains(code, "sseCh <-") {
				sites = append(sites, name+":"+itoa(i+1))
			}
		}
	}
	return sites
}

func TestPersistBeforeAck_FunnelIsSoleSender(t *testing.T) {
	srcs := packageGoSources(t)

	// (1) Exactly one raw async send in the WHOLE package, and it is in live.go.
	sites := rawSseSendSites(srcs)
	if len(sites) != 1 || !strings.HasPrefix(sites[0], "live.go:") {
		t.Fatalf(`raw "sseCh <-" sends = %v, want exactly one in live.go (the persistAndShipFrame funnel).

A frame-shipping site was added or removed. INVARIANT (grill A1/B3): async
server-initiated frames must ship through app.persistAndShipFrame, which
persists-before-ack. If you added an emit path, route it through the funnel
(don't raw-send); it cannot then forget to persist.`, sites)
	}

	live := srcs["live.go"]
	funnelStart := strings.Index(live, "func (app *liveApp) persistAndShipFrame(")
	if funnelStart < 0 {
		t.Fatal("persistAndShipFrame not found in live.go — update this tripwire")
	}
	// The funnel body: from its signature to the next top-level func declaration.
	body := live[funnelStart:]
	if j := strings.Index(body[1:], "\nfunc "); j >= 0 {
		body = body[:j+1]
	}

	// (2) The sole send lives INSIDE the funnel.
	if !strings.Contains(body, "sseCh <-") {
		t.Fatal("the sole sseCh send is not inside persistAndShipFrame — it may skip persist-before-ack")
	}

	// (3) THE PREDICATE THAT MAKES THIS TEST NON-VACUOUS: the funnel actually
	// persists. Without this, deleting the store.Set leaves every other assertion
	// here true and the test greenlights the exact data loss it is named for.
	if !strings.Contains(body, "app.persistBeforeAck(") {
		t.Fatal(`persistAndShipFrame no longer calls app.persistBeforeAck(.

INVARIANT (grill A1): the funnel's WHOLE PURPOSE is to persist BEFORE it acks.
Every async ack site was converted to call it precisely so the persist could not
be forgotten. Without the store.Set the funnel is a plain send and every caller
silently regressed to acked-then-lost.`)
	}
	// And it persists BEFORE the ack, not after.
	if strings.Index(body, "app.persistBeforeAck(") > strings.Index(body, "sseCh <-") {
		t.Fatal("persistAndShipFrame sends BEFORE it persists — a crash between the two loses an acked change")
	}

	// (4) Pin the fanOutFrame broadcast sites (call sites use `.fanOutFrame(`; the
	// method def is `) fanOutFrame(`, no leading dot).
	if got := strings.Count(live, ".fanOutFrame("); got != 3 {
		t.Fatalf(`fanOutFrame call sites = %d, want 3.

A broadcast site was added/removed. It must persist-before-ack (like the sky-nav
mirror / batch flush) or be a relay of already-persisted frames (ensureSSERelay),
then update this count with a note.`, got)
	}
}
