package rt

// The termination-path audit — the gate for the link the shutdown gates do not
// prove.
//
// `live_store_shutdown_test.go` proves that `drainAndRelease` drains, then
// stops accepting, then waits on the completion barrier, then releases — and it
// proves it against a store `chooseStore` actually produced. What it does NOT
// prove, and its own closing note says so, is that anything CALLS it. The
// commit that landed it put the residual plainly:
//
//	"that link is one statically-visible line inside liveAppRun's goroutine,
//	 and reverting it would leave all three green."
//
// That is this branch's dominant failure mode restated: a gate proves a
// FUNCTION while the property lives one call further out. Three termination
// sequences reach the release phase — Sky.Live's signal handler, Sky.Http.
// Server's, and the embedded-PostgreSQL supervisor's three-phase shutdown — and
// each of the three is one line that a refactor can quietly turn back into a
// bare `RunShutdownHooks`. The behaviour would be a session store never closed,
// a `dbshare` refcount never dropped, and a Redis client left to process exit;
// the test suite would be entirely green.
//
// So the link is held by a gate rather than by attention, in the same shape and
// for the same reason as `pg_embed_exit_audit_test.go`: the audit reads the
// SYNTAX TREE, not the text. A grep would be defeated by a line break, would
// fire on the mention of `drainAndRelease` that already sits in a comment at
// live.go:3967, and would be blind to every one of the escapes below.
//
// THE ESCAPES IT IS BUILT TO SURVIVE. Each is a shape under which the call is
// still really made and a naive matcher reports the path broken (a false red
// that gets the gate deleted), or under which the call is NOT made and a naive
// matcher reports it intact (a false green, which is worse):
//
//   - A call moved into a helper — in this file or another file of the package.
//     Reachability is transitive over the whole package's call graph, so a
//     `func terminate()` extracted out of the handler still counts.
//   - A call through an alias. `var release = drainAndRelease` at package scope
//     and `release(budget, nil)` in the handler names `drainAndRelease` nowhere
//     near the handler. Package-scope function aliases are resolved to a
//     fixpoint, so the handler's `release` is read as the thing it is bound to.
//   - A function VALUE taken rather than called: `RegisterAcceptStopper("x",
//     drainAndRelease)` is not an `*ast.CallExpr` at the site that matters. The
//     audit counts REFERENCES, not calls — the same decision, arrived at for the
//     same reason, as the exit audit's `var f = log.Fatalf`.
//   - A call through a METHOD, `s.releaseEverything()`, whose receiver's type
//     `go/parser` cannot resolve. Method references are matched on the member
//     name across every method declared in the package.
//
// …and the two shapes it must NOT count:
//
//   - The name in a comment or a string literal. Neither is an `*ast.Ident`.
//   - A path that runs through ANOTHER entry point. `Server_listen` reaching
//     `drainAndRelease` by way of `liveAppRun` would prove nothing about
//     `Server_listen`'s own termination sequence, so the other entries are
//     removed from the graph while one entry is being checked.
//
// WHAT THIS GATE DOES NOT CATCH, stated rather than implied. It is a STATIC
// reachability audit and it asserts exactly that: the name is reachable from the
// entry. It does not know whether the call is on a path that runs. A
// `drainAndRelease` moved inside `if false`, onto an error branch that the
// signal handler never takes, or into a goroutine that is never started, is
// reachable in this graph and dead at runtime — and this gate would be green.
// Nor does it know about a FOURTH termination sequence: a new app shape that
// installs its own signal handler and never registers here is a hole no static
// rule over the existing three can see. The `RunShutdownHooks` allowlist below
// is the partial answer to that second one — a new sequence that drains has to
// name itself — but a sequence that neither drains nor releases is invisible to
// both.

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

// shutdownReleaseFn is the shared tail every termination sequence must reach.
const shutdownReleaseFn = "drainAndRelease"

// shutdownDrainFn is the drain half on its own. Reaching THIS and not
// `drainAndRelease` is the exact regression the residual named: the sequence
// still flushes telemetry and closes the listener, and never releases a thing.
const shutdownDrainFn = "RunShutdownHooks"

// shutdownEntryPoints are the three termination sequences, keyed as the audit
// keys a declaration: a plain name for a function, `Type.Method` for a method.
//
// The value is what fails in production when that one line goes: it is printed
// in the failure so whoever trips this reads the consequence, not just the rule.
var shutdownEntryPoints = map[string]string{
	"liveAppRun": "Sky.Live's SIGINT/SIGTERM/SIGHUP handler — a Sky.Live app's session " +
		"store would never be closed, so its WAL is never checkpointed and its cleanup " +
		"goroutine never exits",
	"Server_listen": "Sky.Http.Server's signal handler — a mounted sub-app's session store " +
		"would never be closed",
	"pgSupervisor.shutdown": "the embedded-PostgreSQL supervisor's phase 2 — the pooled " +
		"handles pointing AT the database would still be open when pg_ctl stops it",
}

// shutdownDrainCallers are the declarations entitled to name `RunShutdownHooks`
// directly. There is exactly one: `drainAndRelease` IS the drain-then-release
// sequence, and every other caller must go through it.
//
// A new termination sequence that drains without releasing is the shape this
// catches. It cannot be caught by the reachability rule above — that rule only
// knows about entry points somebody remembered to declare — so the drain
// function is guarded from the other side: name it and you are on this list,
// and being on this list is a review.
var shutdownDrainCallers = map[string]string{
	shutdownReleaseFn: "IS the drain-then-release sequence",
}

// ---------------------------------------------------------------------------
// The matcher
// ---------------------------------------------------------------------------

// shutdownGraph is the package's reference graph: which declarations name which
// other declarations, with package-scope function aliases already resolved.
type shutdownGraph struct {
	// refs maps a declaration key to every name its body mentions.
	refs map[string]map[string]bool
	// decls is the set of declaration keys (`f`, `T.m`).
	decls map[string]bool
	// byMember maps a bare member name to every declaration that could be it:
	// the plain function of that name, and every method of that name on any
	// type. `s.releaseEverything()` is an `*ast.SelectorExpr` whose receiver
	// type go/parser cannot resolve, so the member name is all there is.
	byMember map[string][]string
	// alias maps a package-scope name bound to a function to that function.
	alias map[string]string
	files int
}

// shutdownDeclKey names a declaration the way the entry-point table does.
func shutdownDeclKey(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	t := fn.Recv.List[0].Type
	if star, ok := t.(*ast.StarExpr); ok {
		t = star.X
	}
	// A generic receiver is `T[P]`; the type name is the index expression's X.
	if idx, ok := t.(*ast.IndexExpr); ok {
		t = idx.X
	}
	if id, ok := t.(*ast.Ident); ok {
		return id.Name + "." + fn.Name.Name
	}
	return fn.Name.Name
}

// shutdownIdents collects every identifier a node mentions — the bare ones and
// the member half of every selector.
//
// References, not calls. A function value stored, passed or returned is the
// shape that defeats a call-shaped matcher, and it is a shape somebody writes on
// purpose when a termination sequence is being made configurable.
func shutdownIdents(n ast.Node) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(n, func(node ast.Node) bool {
		switch v := node.(type) {
		case *ast.SelectorExpr:
			out[v.Sel.Name] = true
			// Keep walking: the receiver may itself name something.
		case *ast.Ident:
			out[v.Name] = true
		}
		return true
	})
	return out
}

// buildShutdownGraph reads the package's declarations into a reference graph.
func buildShutdownGraph(files []*ast.File) *shutdownGraph {
	g := &shutdownGraph{
		refs:     map[string]map[string]bool{},
		decls:    map[string]bool{},
		byMember: map[string][]string{},
		alias:    map[string]string{},
		files:    len(files),
	}

	for _, f := range files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key := shutdownDeclKey(fn)
			g.decls[key] = true
			g.byMember[fn.Name.Name] = append(g.byMember[fn.Name.Name], key)
			if g.refs[key] == nil {
				g.refs[key] = map[string]bool{}
			}
			for name := range shutdownIdents(fn.Body) {
				g.refs[key][name] = true
			}
		}
	}

	// Package-scope aliases: `var release = drainAndRelease`. Resolved to a
	// fixpoint so a chain (`var a = drainAndRelease; var b = a`) collapses.
	for _, f := range files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || len(vs.Names) != len(vs.Values) {
					continue
				}
				for i, name := range vs.Names {
					if id, ok := vs.Values[i].(*ast.Ident); ok {
						g.alias[name.Name] = id.Name
					}
				}
			}
		}
	}
	for range g.alias { // fixpoint; the chain cannot be longer than the map
		changed := false
		for from, to := range g.alias {
			if next, ok := g.alias[to]; ok && next != to && from != next {
				g.alias[from] = next
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	return g
}

// resolveAlias follows a name through the package-scope alias map.
func (g *shutdownGraph) resolveAlias(name string) string {
	if to, ok := g.alias[name]; ok {
		return to
	}
	return name
}

// reaches reports whether `target` is reachable from `from`, and the path it
// took. `blocked` names declarations the path may not run through — the OTHER
// entry points, so one sequence cannot borrow another's call.
func (g *shutdownGraph) reaches(from, target string, blocked map[string]bool) (bool, []string) {
	type step struct {
		key  string
		path []string
	}
	seen := map[string]bool{from: true}
	queue := []step{{key: from, path: []string{from}}}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]

		names := make([]string, 0, len(g.refs[cur.key]))
		for n := range g.refs[cur.key] {
			names = append(names, n)
		}
		sort.Strings(names) // deterministic path in the failure message

		for _, raw := range names {
			name := g.resolveAlias(raw)
			if name == target {
				return true, append(append([]string{}, cur.path...), target)
			}
			for _, next := range g.byMember[name] {
				if seen[next] || blocked[next] {
					continue
				}
				seen[next] = true
				queue = append(queue, step{key: next, path: append(append([]string{}, cur.path...), next)})
			}
		}
	}
	return false, nil
}

// shutdownAuditFiles parses every non-test .go file of THIS package.
//
// This directory alone, deliberately: `drainAndRelease` is unexported, so no
// declaration outside package `rt` can name it and no helper in a sub-package
// can be a link in the chain. Widening the walk would add files that cannot
// contribute and would make the file floor below meaningless.
func shutdownAuditFiles(t *testing.T, dir string) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("cannot read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var out []*ast.File
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("cannot parse %s: %v", e.Name(), err)
		}
		out = append(out, f)
	}
	return out
}

// ---------------------------------------------------------------------------
// The audit
// ---------------------------------------------------------------------------

func TestEveryTerminationSequenceReachesTheReleasePhase(t *testing.T) {
	g := buildShutdownGraph(shutdownAuditFiles(t, "."))

	// A floor, so an audit that silently stopped reading the package fails here
	// rather than passing on a fraction of it.
	if g.files < 60 {
		t.Fatalf("the audit parsed %d files — package rt is larger than that, so the walk "+
			"is broken and the reachability below would be answered from a fraction of "+
			"the call graph", g.files)
	}
	if !g.decls[shutdownReleaseFn] {
		t.Fatalf("package rt no longer declares %s — the audit is asserting reachability of "+
			"a name that does not exist, which every entry would fail on for the wrong "+
			"reason", shutdownReleaseFn)
	}

	for entry := range shutdownEntryPoints {
		if !g.decls[entry] {
			t.Errorf("shutdownEntryPoints names %q and package rt declares no such function — "+
				"a renamed termination sequence is one this audit stops watching, so update "+
				"the table rather than letting it rot", entry)
		}
	}
	if t.Failed() {
		return
	}

	for entry, consequence := range shutdownEntryPoints {
		blocked := map[string]bool{}
		for other := range shutdownEntryPoints {
			if other != entry {
				blocked[other] = true
			}
		}

		ok, path := g.reaches(entry, shutdownReleaseFn, blocked)
		if !ok {
			drains, _ := g.reaches(entry, shutdownDrainFn, blocked)
			extra := ""
			if drains {
				extra = "\n  It DOES reach " + shutdownDrainFn + ", which is the drain half on its own: " +
					"telemetry still flushes and the listener still closes, and nothing is ever released. " +
					"That is precisely the reverted-to-a-bare-drain shape this gate exists for."
			}
			t.Errorf("%s does not reach %s.\n  Consequence: %s.%s\n"+
				"  Nothing in live_store_shutdown_test.go covers this: those gates call "+
				"%s directly, so they stay green while the production path no longer does.",
				entry, shutdownReleaseFn, consequence, extra, shutdownReleaseFn)
			continue
		}
		t.Logf("%s reaches %s via %s", entry, shutdownReleaseFn, strings.Join(path, " → "))
	}
}

// The other half of the rule: the drain is not reachable EXCEPT through the
// release sequence.
//
// The reachability audit above can only watch entry points somebody declared. A
// fourth app shape that installs its own handler, calls `RunShutdownHooks` and
// stops there is a termination sequence with no release phase, and no rule over
// the existing three would see it. Guarding the drain from the other side turns
// that into a compile-time-visible act: name `RunShutdownHooks` and you are on
// the allowlist, and being on the allowlist is a review.
func TestOnlyTheReleaseSequenceDrainsTheShutdownHooks(t *testing.T) {
	g := buildShutdownGraph(shutdownAuditFiles(t, "."))

	var offenders []string
	for key, names := range g.refs {
		if !names[shutdownDrainFn] {
			continue
		}
		if _, allowed := shutdownDrainCallers[key]; allowed {
			continue
		}
		offenders = append(offenders, key)
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf("%d declaration(s) name %s outside the release sequence:\n  %s\n\n"+
			"A termination sequence that drains and does not release closes the listener, "+
			"flushes the telemetry, and leaves the session store's cleanup goroutine, the "+
			"dbshare refcount and the Redis client to process exit — the defect the release "+
			"phase was added to close, re-introduced one app shape at a time.\n"+
			"Call %s instead. If a site genuinely must drain without releasing, add it to "+
			"shutdownDrainCallers with the reason.",
			len(offenders), shutdownDrainFn, strings.Join(offenders, "\n  "), shutdownReleaseFn)
	}

	// The allowlist is checked back, as the exit audit's is: an entry that no
	// longer names the drain is a stale exemption, and a stale exemption is how
	// the next bypass gets waved through.
	for key, why := range shutdownDrainCallers {
		if !g.refs[key][shutdownDrainFn] {
			t.Errorf("shutdownDrainCallers names %s (%q) but it no longer names %s — drop the "+
				"entry rather than leaving a declaration permanently exempt",
				key, why, shutdownDrainFn)
		}
	}
}

// The audit is only worth having if it can see a violation, and only safe to
// keep if it does not see one that is not there. This proves the matcher on
// synthetic packages rather than trusting the real one to be representative:
// every escape named at the top of this file is a case here, and so are the two
// shapes that must NOT count.
func TestTheTerminationPathMatcherSeesTheShapesTextSearchMisses(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  bool
	}{
		{
			"a direct call",
			[]string{"package p\nfunc drainAndRelease() {}\nfunc entry() { drainAndRelease() }\n"},
			true,
		},
		{
			"inside a goroutine inside the handler — the real shape",
			[]string{"package p\nfunc drainAndRelease() {}\nfunc entry() { go func() { drainAndRelease() }() }\n"},
			true,
		},
		{
			"a call moved into a helper",
			[]string{"package p\nfunc drainAndRelease() {}\nfunc terminate() { drainAndRelease() }\nfunc entry() { terminate() }\n"},
			true,
		},
		{
			"a helper in ANOTHER file of the package",
			[]string{
				"package p\nfunc drainAndRelease() {}\nfunc entry() { terminate() }\n",
				"package p\nfunc terminate() { drainAndRelease() }\n",
			},
			true,
		},
		{
			"two helpers deep, across two files",
			[]string{
				"package p\nfunc drainAndRelease() {}\nfunc entry() { outer() }\n",
				"package p\nfunc outer() { inner() }\nfunc inner() { drainAndRelease() }\n",
			},
			true,
		},
		{
			"a package-scope alias",
			[]string{"package p\nfunc drainAndRelease() {}\nvar release = drainAndRelease\nfunc entry() { release() }\n"},
			true,
		},
		{
			"a chain of package-scope aliases",
			[]string{"package p\nfunc drainAndRelease() {}\nvar a = drainAndRelease\nvar release = a\nfunc entry() { release() }\n"},
			true,
		},
		{
			"a local alias",
			[]string{"package p\nfunc drainAndRelease() {}\nfunc entry() { f := drainAndRelease; f() }\n"},
			true,
		},
		{
			"a function value passed rather than called",
			[]string{"package p\nfunc drainAndRelease() {}\nfunc reg(func()) {}\nfunc entry() { reg(drainAndRelease) }\n"},
			true,
		},
		{
			"a function value returned rather than called",
			[]string{"package p\nfunc drainAndRelease() {}\nfunc entry() func() { return drainAndRelease }\n"},
			true,
		},
		{
			"a call through a method whose receiver type is unresolvable",
			[]string{"package p\nfunc drainAndRelease() {}\ntype S struct{}\nfunc (s *S) releaseEverything() { drainAndRelease() }\nfunc entry() { s := get(); s.releaseEverything() }\nfunc get() *S { return nil }\n"},
			true,
		},

		// The regression this gate exists for.
		{
			"reverted to a bare drain",
			[]string{"package p\nfunc drainAndRelease() {}\nfunc RunShutdownHooks() {}\nfunc entry() { RunShutdownHooks() }\n"},
			false,
		},
		{
			"the handler does nothing at all",
			[]string{"package p\nfunc drainAndRelease() {}\nfunc entry() {}\n"},
			false,
		},
		{
			"a helper that no longer calls it",
			[]string{"package p\nfunc drainAndRelease() {}\nfunc terminate() {}\nfunc entry() { terminate() }\n"},
			false,
		},

		// The shapes that must NOT count.
		{
			"the name in a comment",
			[]string{"package p\nfunc drainAndRelease() {}\nfunc entry() {\n\t// See drainAndRelease for why the order matters.\n}\n"},
			false,
		},
		{
			"the name in a doc comment on the entry itself",
			[]string{"package p\nfunc drainAndRelease() {}\n\n// entry terminates via drainAndRelease.\nfunc entry() {}\n"},
			false,
		},
		{
			"the name in a string literal",
			[]string{"package p\nfunc drainAndRelease() {}\nfunc log(string) {}\nfunc entry() { log(\"drainAndRelease\") }\n"},
			false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := buildShutdownGraph(parseShutdownSources(t, c.files))
			got, path := g.reaches("entry", "drainAndRelease", nil)
			if got != c.want {
				t.Errorf("reaches(entry, drainAndRelease) = %v (path %v), want %v, in:\n%s",
					got, path, c.want, strings.Join(c.files, "\n---\n"))
			}
		})
	}
}

// A path through another entry point proves nothing about the entry it started
// from, so the audit blocks the other entries while it checks one. Its own case,
// because it is the difference between "all three sequences release" and "at
// least one of them does".
func TestOneSequenceCannotBorrowAnothersReleaseCall(t *testing.T) {
	src := []string{
		"package p\n" +
			"func drainAndRelease() {}\n" +
			"func liveAppRun() { drainAndRelease() }\n" +
			// Server_listen names liveAppRun (in the real package it does not, but a
			// shared boot helper that did would be an ordinary refactor) and does not
			// terminate through it.
			"func Server_listen() { liveAppRun() }\n",
	}
	g := buildShutdownGraph(parseShutdownSources(t, src))

	if ok, path := g.reaches("Server_listen", "drainAndRelease", nil); !ok {
		t.Fatalf("precondition: unblocked, Server_listen should reach it via liveAppRun (path %v)", path)
	}
	blocked := map[string]bool{"liveAppRun": true}
	if ok, path := g.reaches("Server_listen", "drainAndRelease", blocked); ok {
		t.Errorf("Server_listen counted as reaching drainAndRelease via %v — a path through "+
			"ANOTHER termination sequence says nothing about this one's own shutdown", path)
	}
}

// The allowlist matcher, on the shape it is for: a new termination sequence that
// drains and never releases.
func TestTheDrainAllowlistSeesANewSequenceThatNeverReleases(t *testing.T) {
	src := []string{
		"package p\n" +
			"func RunShutdownHooks() {}\n" +
			"func drainAndRelease() { RunShutdownHooks() }\n" +
			"func Webview_run() { RunShutdownHooks() }\n",
	}
	g := buildShutdownGraph(parseShutdownSources(t, src))

	var offenders []string
	for key, names := range g.refs {
		if names["RunShutdownHooks"] {
			if _, allowed := shutdownDrainCallers[key]; !allowed {
				offenders = append(offenders, key)
			}
		}
	}
	sort.Strings(offenders)
	if len(offenders) != 1 || offenders[0] != "Webview_run" {
		t.Errorf("offenders = %v, want [Webview_run] — drainAndRelease is allowlisted and the "+
			"new sequence is not", offenders)
	}
}

func parseShutdownSources(t *testing.T, srcs []string) []*ast.File {
	t.Helper()
	fset := token.NewFileSet()
	var out []*ast.File
	for i, src := range srcs {
		f, err := parser.ParseFile(fset, "src"+string(rune('a'+i))+".go", src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse case source %d: %v\n%s", i, err, src)
		}
		out = append(out, f)
	}
	return out
}
