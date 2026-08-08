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
//
// G2 fixed the THIRD, and structurally the worst, way it was vacuous:
//
//  3. IT ONLY UNDERSTOOD ONE TRANSPORT. Every predicate keyed on `sseCh <-` sends
//     and `.fanOutFrame(` counts. Three ack paths write the frame DIRECTLY to the
//     http.ResponseWriter — the SSE reconnect-resync, the SSE drop-resync and the
//     handleEvent desync reply — so they matched nothing, and all three shipped a
//     fresh localSeq unpersisted while this test stayed green. A tripwire that
//     enumerates ONE mechanism can only ever pin that mechanism; a NEW transport is
//     invisible to it by construction, which is exactly what happened.
//
//     TestPersistBeforeAck_EverySeqAdvancingAckPersistsFirst below replaces the
//     enumeration with a property over the whole package: any function that can
//     advance localSeq (transitively — computed over the call graph, so wrapping the
//     render in a helper does not launder it) must, at EVERY point where it acks the
//     client by ANY mechanism, have persisted first. TestPersistBeforeAck_SingleRawSSEWriter
//     then pins that there is exactly one raw `event:` writer, so a fourth transport
//     cannot be hand-rolled past the property either.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
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

// ═══════════════════════════════════════════════════════════════════════════
// G2 — the persist-before-ack PROPERTY, over every transport
// ═══════════════════════════════════════════════════════════════════════════
//
// The analysis below is AST-based, not textual. Two reasons, both learned from
// the bypasses this replaces:
//
//   - Comments. live.go documents every one of these tokens in prose; a grep
//     matches the DESCRIPTION of a rule as readily as its violation.
//   - Control flow. A textual "is there a persist earlier in the file" rule is
//     satisfied by a persist in a DIFFERENT, mutually exclusive branch. Re-adding
//     the G-3 bypass would pass such a rule outright, because handleEvent's
//     sendBeacon-batch arm persists textually above the desync arm and returns
//     before ever reaching it. The rule here is dominance: the persist must be
//     unconditionally executed on the way to the ack.

// pkgFunc is one top-level func/method of package rt.
type pkgFunc struct {
	file  string
	name  string
	line  int
	decl  *ast.FuncDecl
	calls map[string]bool // callee names appearing anywhere in the body
}

// packageFuncs parses every non-test .go file in the package.
func packageFuncs(t *testing.T) []pkgFunc {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	var out []pkgFunc
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			calls := map[string]bool{}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					if nm := calleeName(c); nm != "" {
						calls[nm] = true
					}
				}
				return true
			})
			out = append(out, pkgFunc{
				file: name, name: fd.Name.Name,
				line: fset.Position(fd.Pos()).Line, decl: fd, calls: calls,
			})
		}
	}
	if len(out) == 0 {
		t.Fatal("no package funcs found — this tripwire would be vacuous")
	}
	return out
}

// calleeName is the bare name of a call's target: `foo(` → "foo",
// `x.y.foo(` → "foo". Receiver-insensitive on purpose — the helpers below are
// unique names in this package.
func calleeName(c *ast.CallExpr) string {
	switch fn := c.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

// seqAdvancingFuncs computes, to a fixpoint over the package call graph, every
// function that can advance a session's localSeq. Seeded with nextLocalSeq (the
// counter's only writer) and closed under "calls a member of the set".
//
// TRANSITIVITY IS THE POINT. All three G2 bypasses reached the counter through a
// helper (prepareFrameSnapshot / encodeSSEFrame / renderResyncFrame), and the fix
// moves the render behind more helpers still. A rule that only looked for a
// literal nextLocalSeq() in the acking function would be laundered by any
// extraction — including the ones this commit performs.
func seqAdvancingFuncs(funcs []pkgFunc) map[string]bool {
	set := map[string]bool{"nextLocalSeq": true}
	for changed := true; changed; {
		changed = false
		for _, fn := range funcs {
			if set[fn.name] {
				continue
			}
			for callee := range fn.calls {
				if callee != fn.name && set[callee] {
					set[fn.name] = true
					changed = true
					break
				}
			}
		}
	}
	return set
}

// ackKind names one way the server tells the client "here is state you should
// apply". `combined` marks the funnel helpers, which persist AND ack in one step,
// so they satisfy the property at their own position.
type ackKind struct {
	combined bool
	what     string
}

// EVERY mechanism by which package rt acks a client. A transport added without a
// row here is invisible to this property — which is the exact failure mode G2
// closes — so TestPersistBeforeAck_SingleRawSSEWriter pins that the SSE wire has
// one and only one raw writer, and a hand-rolled fourth transport cannot appear
// without tripping it.
var ackCalls = map[string]ackKind{
	"fanOutFrame":              {false, "multi-connection broadcast"},
	"writeSSEEvent":            {false, "direct write to an SSE ResponseWriter"},
	"writeEventHTML":           {false, "full-body /_sky/event reply"},
	"writeEventJSON":           {false, "patch-list /_sky/event reply"},
	"persistAndShipFrame":      {true, "funnel — channel arm"},
	"persistAndWriteSSEFrame":  {true, "funnel — direct-writer arm"},
	"persistAndWriteEventHTML": {true, "funnel — POST-response arm"},
}

// persistCalls make the session durable. The funnel helpers count because they
// persist as their first act.
var persistCalls = map[string]bool{
	"persistBeforeAck":         true,
	"persistAndShipFrame":      true,
	"persistAndWriteSSEFrame":  true,
	"persistAndWriteEventHTML": true,
}

// isStoreSet matches the raw `<something>.store.Set(` / `store.Set(` persist that
// predates the funnel. Deliberately narrow: a bare Sel.Name == "Set" would also
// match w.Header().Set(…), and counting a response header as a persist is exactly
// the kind of false green this file exists to prevent.
func isStoreSet(c *ast.CallExpr) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Set" {
		return false
	}
	switch recv := sel.X.(type) {
	case *ast.Ident:
		return recv.Name == "store"
	case *ast.SelectorExpr:
		return recv.Sel.Name == "store"
	}
	return false
}

// node + its enclosing statement chain, outermost (the func body block) first.
type sited struct {
	pos   token.Pos
	chain []ast.Stmt
	name  string
	kind  ackKind
}

// collectSites walks a function body, recording every ack and every persist
// together with the chain of statements that encloses it.
func collectSites(fd *ast.FuncDecl) (acks, persists []sited) {
	var stack []ast.Node
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		stack = append(stack, n)
		chain := func() []ast.Stmt {
			var out []ast.Stmt
			for _, s := range stack {
				if st, ok := s.(ast.Stmt); ok {
					out = append(out, st)
				}
			}
			return out
		}
		// A raw send on sess.sseCh is an ack with no call expression.
		if send, ok := n.(*ast.SendStmt); ok {
			if sel, ok := send.Chan.(*ast.SelectorExpr); ok && sel.Sel.Name == "sseCh" {
				acks = append(acks, sited{pos: n.Pos(), chain: chain(),
					name: "sseCh <-", kind: ackKind{false, "raw SSE channel send"}})
			}
		}
		if c, ok := n.(*ast.CallExpr); ok {
			nm := calleeName(c)
			if k, isAck := ackCalls[nm]; isAck {
				acks = append(acks, sited{pos: n.Pos(), chain: chain(), name: nm, kind: k})
			}
			if persistCalls[nm] || isStoreSet(c) {
				label := nm
				if !persistCalls[nm] {
					label = "store.Set"
				}
				persists = append(persists, sited{pos: n.Pos(), chain: chain(), name: label})
			}
		}
		return true
	})
	return acks, persists
}

// branching reports whether reaching inside this statement is conditional on
// something (or deferred / spawned), so a persist nested under it is NOT
// guaranteed to have run by the time a sibling further down executes.
func branching(s ast.Stmt) bool {
	switch s.(type) {
	case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt,
		*ast.TypeSwitchStmt, *ast.SelectStmt, *ast.CaseClause, *ast.CommClause,
		*ast.DeferStmt, *ast.GoStmt:
		return true
	}
	return false
}

// stmtList returns the statement list a block-like statement owns, so two chain
// elements that diverge inside it can be ordered.
func stmtList(s ast.Stmt) []ast.Stmt {
	switch b := s.(type) {
	case *ast.BlockStmt:
		return b.List
	case *ast.CaseClause:
		return b.Body
	case *ast.CommClause:
		return b.Body
	}
	return nil
}

// dominates reports whether persist p is guaranteed to have executed before ack
// a — i.e. p sits on an unconditional prefix of every path that reaches a.
//
// The two chains share a prefix (at minimum the function body block). At the
// first level where they diverge, p's branch must come EARLIER in that block's
// statement list, and everything from the divergence down to p must be
// unconditional — otherwise p is in a branch that a's path may never take. That
// second clause is what catches a re-added G-3: handleEvent's sendBeacon-batch
// arm persists in a block that returns before the desync arm is ever reached.
func dominates(p, a sited) bool {
	i := 0
	for i < len(p.chain) && i < len(a.chain) && p.chain[i] == a.chain[i] {
		i++
	}
	if i == 0 {
		return false // not even a common function body — different functions
	}
	// p's chain is a prefix of a's: p is inside a statement that also encloses a
	// (e.g. both operands of one expression). Position order decides.
	if i == len(p.chain) {
		return p.pos < a.pos
	}
	if i == len(a.chain) {
		return false // the persist is nested INSIDE the acking statement
	}
	// Diverged: order the two branches within their common parent block.
	list := stmtList(p.chain[i-1])
	pi, ai := -1, -1
	for idx, s := range list {
		if s == p.chain[i] {
			pi = idx
		}
		if s == a.chain[i] {
			ai = idx
		}
	}
	if pi < 0 || ai < 0 || pi >= ai {
		return false
	}
	for _, s := range p.chain[i:] {
		if branching(s) {
			return false
		}
	}
	return true
}

// TestPersistBeforeAck_EverySeqAdvancingAckPersistsFirst is the property that
// replaces the old per-mechanism enumeration:
//
//	For every function that can advance localSeq, every client ack it performs
//	must be dominated by a persist — or BE one of the funnel helpers, which
//	persist as their first act.
//
// This is what the three G2 bypasses violated. handleSSE's reconnect-resync
// persisted AFTER its write; the drop-resync never persisted; the handleEvent
// desync arm returned before handleEvent's own store.Set. None of them tripped a
// `sseCh <-` count or a `.fanOutFrame(` count, so all three shipped green.
//
// Known bound, documented rather than silently accepted: dominance is computed
// within a single function, so a persist that happens in a CALLER is not seen. A
// helper that acks is therefore required to persist itself. That is the strict
// direction, and it is why every ack path in this package terminates in a funnel
// helper rather than trusting its caller.
func TestPersistBeforeAck_EverySeqAdvancingAckPersistsFirst(t *testing.T) {
	funcs := packageFuncs(t)
	seq := seqAdvancingFuncs(funcs)

	var failures, inventory []string
	for _, fn := range funcs {
		if !seq[fn.name] {
			continue
		}
		acks, persists := collectSites(fn.decl)
		for _, a := range acks {
			ok := a.kind.combined
			for _, p := range persists {
				if ok {
					break
				}
				ok = dominates(p, a)
			}
			verdict := "PERSISTS-FIRST"
			if !ok {
				verdict = "ACKS UNPERSISTED"
				failures = append(failures,
					fn.file+" "+fn.name+" → "+a.kind.what+" ("+a.name+")")
			}
			inventory = append(inventory,
				fn.file+":"+itoa(fn.line)+" "+fn.name+" — "+a.kind.what+
					" ("+a.name+") → "+verdict)
		}
	}

	sort.Strings(inventory)
	for _, line := range inventory {
		t.Log(line)
	}
	if len(inventory) == 0 {
		t.Fatal("no seq-advancing ack sites found at all — the scan is broken and this test is vacuous")
	}
	if len(failures) > 0 {
		t.Fatalf(`%d ack site(s) tell the client to apply state the store has not seen:

  %s

INVARIANT (grill A1): each of these can advance the session's localSeq and acks
before persisting. A crash in that window leaves the persisted OutSeq BEHIND the
client's __skyLastAppliedSeq; on restart the client silently discards every
replayed frame and the page freezes with no error until a hard reload.

Route the ack through the durability funnel — app.persistAndShipFrame (channel),
app.persistAndWriteSSEFrame (open SSE stream) or app.persistAndWriteEventHTML
(/_sky/event reply) — so the persist can be neither forgotten nor out-ordered.`,
			len(failures), strings.Join(failures, "\n  "))
	}
}

// TestPersistBeforeAck_SingleRawSSEWriter pins the other half: the property above
// can only see acks it has a row for, so the SSE wire must have exactly ONE raw
// writer. Hand-rolling `fmt.Fprintf(w, "event: …")` at a new site is precisely how
// the reconnect-resync and drop-resync bypasses came to exist, and such a site is
// invisible to any table that does not yet know about it.
//
// With this pinned, a new direct-to-ResponseWriter frame path has two options:
// call writeSSEEvent (which the property above then polices) or fail here.
func TestPersistBeforeAck_SingleRawSSEWriter(t *testing.T) {
	funcs := packageFuncs(t)
	var writers []string
	for _, fn := range funcs {
		n := 0
		ast.Inspect(fn.decl.Body, func(node ast.Node) bool {
			if lit, ok := node.(*ast.BasicLit); ok &&
				lit.Kind == token.STRING && strings.HasPrefix(lit.Value, `"event: `) {
				n++
			}
			return true
		})
		if n > 0 {
			writers = append(writers, fn.file+":"+itoa(fn.line)+" "+fn.name)
		}
	}
	sort.Strings(writers)
	if len(writers) != 1 || !strings.HasSuffix(writers[0], " writeSSEEvent") {
		t.Fatalf(`raw SSE "event: " writers = %v, want exactly one (writeSSEEvent).

INVARIANT (grill A1 / G2): every SSE frame leaves through writeSSEEvent, so the
persist-before-ack property has a single, known chokepoint to police. A hand-rolled
fmt.Fprintf(w, "event: …") is a NEW ack transport no table knows about — that is
exactly how the reconnect-resync and drop-resync bypasses shipped green.

If you need to emit a frame: connection control (no session state, no seq) →
writeSSEControl; a frame already off sess.sseCh → writeSSERelayed; anything you
just rendered → app.persistAndWriteSSEFrame.`, writers)
	}
}

// TestPersistBeforeAck_OutsideFunnelWritersArePinned. writeSSEControl and
// writeSSERelayed are the two ways to reach writeSSEEvent WITHOUT persisting, and
// each is legitimate for a specific reason: control events carry no session state,
// and a relayed frame was already persisted by persistAndShipFrame before it
// entered sess.sseCh. Both reasons are properties of the CALL SITE, not of the
// helper — so the sites are counted here, and a new one must be justified in
// review rather than inheriting an existing exemption.
func TestPersistBeforeAck_OutsideFunnelWritersArePinned(t *testing.T) {
	funcs := packageFuncs(t)
	count := func(name string) int {
		n := 0
		for _, fn := range funcs {
			if fn.name == name {
				continue // the helper's own body, forwarding to writeSSEEvent
			}
			ast.Inspect(fn.decl.Body, func(node ast.Node) bool {
				if c, ok := node.(*ast.CallExpr); ok && calleeName(c) == name {
					n++
				}
				return true
			})
		}
		return n
	}
	if got := count("writeSSEControl"); got != 2 {
		t.Fatalf(`writeSSEControl call sites = %d, want 2 (handleSSE hello + heartbeat).

These bypass the funnel because they carry NO session state and advance no seq.
A new call site must be genuinely stateless — if it ships anything the client
applies, use app.persistAndWriteSSEFrame instead, then update this count.`, got)
	}
	if got := count("writeSSERelayed"); got != 1 {
		t.Fatalf(`writeSSERelayed call sites = %d, want 1 (handleSSE's sseOut relay).

This bypasses the funnel because the frame ALREADY came off sess.sseCh, so
persistAndShipFrame persisted it before it was enqueued. A site that relays a
frame it rendered itself is an unpersisted ack — use app.persistAndWriteSSEFrame,
then update this count.`, got)
	}
}
