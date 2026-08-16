package rt

// The `os.Exit` audit — the gate that stops the ninth site.
//
// `os.Exit` does not run deferred functions. Generated `main` is
//
//	defer rt.LogPanicAndExit()
//	rt.MaybeStartEmbeddedPostgres()
//	defer rt.StopEmbeddedPostgres()
//	…
//
// so ANY `os.Exit` reached after the second line — from `main` itself, or from
// any goroutine the runtime started — skips the stop and leaves the embedded
// postmaster running with nothing left to stop it. The next run of the same
// binary finds it, adopts it, and (correctly, per the ownership rule in
// stopPostgres) never stops it either. One `os.Exit` is therefore not one
// orphaned database, it is a database that outlives every subsequent run.
//
// This was not hypothetical and it was not one site. `System.exit` — the
// ordinary way a `Sky.Cli` job ends, and `--embed` plus a one-shot job is
// exactly what ExitProcess exists for — called `os.Exit` directly, and so did
// the port-in-use paths, the profiler watchdog, the console invariant and three
// terminal-runtime paths. Each was found by reading; the eighth would have been
// found by an outage.
//
// So the list is kept honest by a gate rather than by attention: every exit in
// this package routes through `rt.ExitProcess`, and the two files below are the
// only ones allowed to call `os.Exit` themselves.
//
// The audit reads the SYNTAX TREE, not the text. A grep-based version would be
// defeated by a line break and would fire on the word appearing in a comment or
// a string — the two ways a tripwire becomes noise and then becomes disabled.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// exitAuditAllowed are the files entitled to call os.Exit directly.
//
//   - pg_embed.go IS ExitProcess, plus the two exits that cannot route through
//     it: the watchers that fire when the database has ALREADY gone away, where
//     there is nothing left to stop.
//   - panic_recover.go's LogPanicAndExit is the FIRST defer generated `main`
//     registers, so `defer rt.StopEmbeddedPostgres()` — registered second — runs
//     BEFORE it. Deferred calls run last-in-first-out; the database is already
//     down by the time this one exits. It is on the list because the ordering
//     proves it safe, not because it was overlooked.
var exitAuditAllowed = map[string]string{
	"pg_embed.go":      "defines ExitProcess; its other exits fire when the database is already gone",
	"panic_recover.go": "runs as main's FIRST defer, so StopEmbeddedPostgres (registered second) has already run",
}

// exitAuditBanned are the process-ending members, per package. syscall.Exit is
// here because it is the same primitive one import away, and a gate that named
// only os.Exit would be closed by spelling.
//
// `log` is here for the same reason and it is not a theoretical one. Every
// `log.Fatal*` ends with `os.Exit(1)` and every `log.Panic*` ends with a panic
// that no recover in the runtime is positioned to catch; both skip generated
// main's `defer rt.StopEmbeddedPostgres()` exactly as a bare os.Exit does. Two
// production paths reached the process this way — `failDurableStore` and
// `jobsStoreDegrade`, the fail-loud branches that fire when an `--embed` app's
// session or jobs store is unreachable. That is precisely the boot in which the
// postmaster has just been started, so an app misconfigured in production
// orphaned a cluster on EVERY boot attempt.
var exitAuditBanned = map[string]map[string]bool{
	"os":      {"Exit": true},
	"syscall": {"Exit": true},
	"log": {
		"Fatal": true, "Fatalf": true, "Fatalln": true,
		"Panic": true, "Panicf": true, "Panicln": true,
	},
}

// exitAuditBannedMethods is the same set of process-ending operations reached
// through a VALUE rather than through the package qualifier: the `*log.Logger`
// methods (`lg.Fatalf(…)`), and — the shape that actually got past the previous
// audit — a package function taken as a function value and stored in a
// variable (`var storeFatalf = log.Fatalf`).
//
// Matching on the member name alone, without resolving the receiver's type, is
// deliberate. `go/parser` has no type information, and importing the whole
// type-checker to decide whether one identifier is a `*log.Logger` would buy
// nothing here: there is no other `Fatal*` or `Panic*` in this package, and a
// runtime type that acquired a method by one of these names would be worth
// looking at anyway. A false positive costs one line on this list; a false
// negative cost an orphaned database per boot.
//
// `Exit` is deliberately NOT in this set. It is a package function in both
// `os` and `syscall` and never a method of either, so banning the bare name
// would fire on any unrelated type with an `Exit` method while catching
// nothing the package rule above misses.
var exitAuditBannedMethods = map[string]bool{
	"Fatal": true, "Fatalf": true, "Fatalln": true,
	"Panic": true, "Panicf": true, "Panicln": true,
}

// exitAuditSignalSenders are the operations by which a process ends ITSELF with
// a signal rather than by returning or calling an exit function.
//
// SIGKILL runs no deferred functions and cannot be caught, so
// `syscall.Kill(os.Getpid(), SIGKILL)` skips generated main's `defer
// rt.StopEmbeddedPostgres()` exactly as `os.Exit` does — and the runtime
// already imports `syscall` and already calls `syscall.Kill`, so a future
// force-exit written this way is not a shape somebody would have to invent.
//
// What is banned is a SELF-DIRECTED signal, not signalling. Sending a signal to
// a CHILD is what the postmaster supervisor exists to do (`syscall.Kill(pid,
// SIGINT)` in pg_embed.go, `syscall.Kill(pid, 0)` as a liveness probe in
// pg_embed_bundle.go), and a rule that fired on those would be turned off
// within the week. The discriminator is the target: an argument naming
// `Getpid` is this process, whoever it is qualified by.
//
// `FindProcess` is here because `os.FindProcess(os.Getpid())` is how the same
// thing is written without `syscall`: the handle is obtained in one statement
// and signalled in another, so the Getpid is not in the `Signal` call to be
// seen. Taking a handle on THIS process is the reviewable act.
var exitAuditSignalSenders = map[string]bool{
	"Kill": true, "Signal": true, "FindProcess": true,
}

// exitAuditImports resolves the file's import set: qualified locals (so an
// aliased `import xos "os"` is still caught) and DOT imports.
//
// The dot import is the hole this closes. `import . "os"` puts `Exit` in the
// file's own scope, and the audit read only `*ast.SelectorExpr` — so
// `Exit(1)`, which is a bare `*ast.Ident`, was invisible. Confirmed against
// the audit's own matcher: `import . "os"; Exit(1)` scored 0 offences, and so
// did `import . "log"; Fatalf("x")`. Neither is Go anyone would write on
// purpose. Both compile, both end the process, and an audit that reports clean
// on a file it cannot read is worse than one that is absent.
func exitAuditImports(file *ast.File) (locals map[string]string, dotted map[string]bool) {
	locals = map[string]string{}
	dotted = map[string]bool{}
	for _, imp := range file.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if _, banned := exitAuditBanned[path]; !banned {
			continue
		}
		if imp.Name != nil && imp.Name.Name == "." {
			dotted[path] = true
			continue
		}
		local := path
		if imp.Name != nil {
			local = imp.Name.Name
		}
		locals[local] = path
	}
	return locals, dotted
}

// exitAuditMatch reports the banned name a selector NAMES — whether it is being
// called, assigned, passed or merely referenced. The audit reads references
// rather than calls because a reference is how the ninth site arrived: a
// function value assigned at package scope is not an `*ast.CallExpr` anywhere
// the assignment can be seen, so a call-shaped matcher looks straight through
// it and reports the package clean.
func exitAuditMatch(sel *ast.SelectorExpr, locals map[string]string) (string, bool) {
	if id, ok := sel.X.(*ast.Ident); ok {
		if path, isImport := locals[id.Name]; isImport {
			if exitAuditBanned[path][sel.Sel.Name] {
				return path + "." + sel.Sel.Name, true
			}
			// A non-banned member of a banned import (`os.Getenv`, `log.Printf`)
			// is not reconsidered as a method: the qualifier is the package.
			return "", false
		}
	}
	if exitAuditBannedMethods[sel.Sel.Name] {
		return "(value)." + sel.Sel.Name, true
	}
	return "", false
}

// exitAuditTerminalName is the member a callee names, through however many
// qualifiers: `Kill` for both `syscall.Kill` and a bare dot-imported `Kill`.
func exitAuditTerminalName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}

// exitAuditNamesGetpid reports whether an expression asks the kernel for THIS
// process's id, however it is qualified or aliased.
func exitAuditNamesGetpid(e ast.Expr) bool {
	found := false
	ast.Inspect(e, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "Getpid" {
			found = true
		}
		return !found
	})
	return found
}

// exitAuditOffences is THE matcher, over one file: what the whole-tree audit
// runs and what the falsification cases below run, so the two cannot disagree
// about what the rule is. Each entry is `line:<n> names <what>`.
func exitAuditOffences(fset *token.FileSet, file *ast.File) []string {
	locals, dotted := exitAuditImports(file)

	// The `Sel` half of a selector is an *ast.Ident too. Collected first so the
	// dot-import rule below does not count `os.Exit` a second time as a bare
	// `Exit`.
	selNames := map[*ast.Ident]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			selNames[sel.Sel] = true
		}
		return true
	})

	var out []string
	at := func(p token.Pos, what string) {
		out = append(out, "line:"+strconv.Itoa(fset.Position(p).Line)+" names "+what)
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.SelectorExpr:
			if named, banned := exitAuditMatch(node, locals); banned {
				at(node.Pos(), named)
			}
		case *ast.Ident:
			if selNames[node] {
				return true
			}
			for path := range dotted {
				if exitAuditBanned[path][node.Name] {
					at(node.Pos(), ". "+path+"."+node.Name)
				}
			}
		case *ast.CallExpr:
			if !exitAuditSignalSenders[exitAuditTerminalName(node.Fun)] {
				return true
			}
			for _, arg := range node.Args {
				if exitAuditNamesGetpid(arg) {
					at(node.Pos(), "a self-directed "+exitAuditTerminalName(node.Fun))
					break
				}
			}
		}
		return true
	})
	return out
}

func TestNoRuntimeCodeExitsWithoutStoppingTheEmbeddedDatabase(t *testing.T) {
	// The whole runtime tree, not just this directory. rt/jobs, rt/hub,
	// rt/telemetry and rt/console_app are separate packages but they are linked
	// into the same app binary, so an exit in any of them skips generated main's
	// defers exactly as one here would. The previous audit read `os.ReadDir(".")`
	// and skipped every directory, which left 35 files of runtime code — including
	// the jobs and hub packages, which have their own store-connect failure paths —
	// entirely unexamined.
	var files []string
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		// Test files are excluded deliberately: the child-process helpers in
		// pg_embed_live_test.go and pg_embed_ownership_test.go signal failure to
		// their parent with an exit code, which IS the observation the parent
		// makes. They are not app code and no generated main defers anything
		// around them.
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		t.Fatalf("cannot walk the runtime tree: %v", err)
	}

	fset := token.NewFileSet()
	var offences []string
	scanned, allowedSeen := 0, map[string]bool{}

	for _, name := range files {
		scanned++

		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("cannot parse %s: %v", name, err)
		}
		// NOT skipped when the file imports neither os nor syscall nor log: the
		// method rule reaches a process-ending call through a value, whose
		// import may be in another file entirely.
		found := exitAuditOffences(fset, file)
		if _, allowed := exitAuditAllowed[name]; allowed {
			if len(found) > 0 {
				allowedSeen[name] = true
			}
			continue
		}
		for _, f := range found {
			offences = append(offences, name+":"+strings.TrimPrefix(f, "line:"))
		}
	}

	// The floor is deliberately ABOVE the number of files in this directory
	// alone (82 of the 117), so an audit that silently stopped walking into the
	// sub-packages fails here rather than passing on a fraction of the tree. A
	// floor low enough for `rt` on its own to clear would not be a floor.
	if scanned < 100 {
		t.Fatalf("the audit only scanned %d files — it is not looking at the whole runtime "+
			"tree (rt plus rt/jobs, rt/hub, rt/telemetry, rt/console_app) and would pass "+
			"on anything it missed", scanned)
	}

	if len(offences) > 0 {
		sort.Strings(offences)
		t.Errorf("%d runtime exit(s) bypass rt.ExitProcess:\n  %s\n\n"+
			"os.Exit does not run deferred functions, and log.Fatal*/log.Panic* end in\n"+
			"os.Exit and an unrecovered panic respectively, so each of these skips\n"+
			"generated main's `defer rt.StopEmbeddedPostgres()` and leaves an `--embed`\n"+
			"app's PostgreSQL running with nothing left to stop it — which the NEXT run\n"+
			"adopts and, by the ownership rule, never stops either.\n"+
			"A REFERENCE counts, not only a call: `var f = log.Fatalf` is the same exit\n"+
			"one indirection away, and is how two of these arrived.\n"+
			"Call rt.ExitProcess(code) instead — or, where a message has to be logged\n"+
			"first, rt.fatalfAndExit. If a site genuinely cannot (the database is already\n"+
			"gone, or a defer ordering proves the stop has run), add the FILE to\n"+
			"exitAuditAllowed with the reason.",
			len(offences), strings.Join(offences, "\n  "))
	}

	// The allowlist is checked back: an entry whose file no longer names a
	// banned exit is a stale exemption, and a stale exemption is how the next
	// bypass gets waved through.
	for file, why := range exitAuditAllowed {
		if !allowedSeen[file] {
			t.Errorf("exitAuditAllowed names %s (%q) but it no longer names a banned exit — "+
				"drop the entry rather than leaving a file permanently exempt", file, why)
		}
	}
}

// The audit is only worth having if it can see a violation. This proves the
// matcher on synthetic sources rather than trusting that the real package is
// representative: an aliased import and a call inside a nested closure are the
// two shapes a text search misses, a mention in a comment or a string is the
// shape a text search wrongly reports — and a package function taken as a
// VALUE is the shape a call-shaped AST matcher misses, which is the one that
// let two live sites through.
func TestTheExitAuditMatcherSeesTheShapesTextSearchMisses(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want int
	}{
		{"plain", "package p\nimport \"os\"\nfunc f() { os.Exit(1) }\n", 1},
		{"aliased", "package p\nimport xos \"os\"\nfunc f() { xos.Exit(1) }\n", 1},
		{"nested closure", "package p\nimport \"os\"\nfunc f() { go func() { defer func() { os.Exit(2) }() }() }\n", 1},
		{"syscall spelling", "package p\nimport \"syscall\"\nfunc f() { syscall.Exit(1) }\n", 1},
		{"line broken", "package p\nimport \"os\"\nfunc f() {\n\tos.Exit(\n\t\t1,\n\t)\n}\n", 1},
		{"in a comment", "package p\nimport \"os\"\n// os.Exit(1) would be wrong here\nfunc f() { _ = os.Getenv(\"X\") }\n", 0},
		{"in a string", "package p\nimport \"os\"\nfunc f() { _ = os.Getenv(\"os.Exit(1)\") }\n", 0},
		{"a method of the same name", "package p\nimport \"os\"\ntype T struct{}\nfunc (T) Exit(int) {}\nfunc f() { var os2 T; os2.Exit(1); _ = os.Getenv(\"\") }\n", 0},

		// The reference shapes — none of these is an *ast.CallExpr at the site
		// that matters, and every one of them ends the process.
		{"log.Fatalf called", "package p\nimport \"log\"\nfunc f() { log.Fatalf(\"x\") }\n", 1},
		{"log.Fatalf as a package-scope value", "package p\nimport \"log\"\nvar fatalf = log.Fatalf\n", 1},
		{"os.Exit as a value", "package p\nimport \"os\"\nvar bye = os.Exit\n", 1},
		{"log.Fatal passed as an argument", "package p\nimport \"log\"\nfunc g(func(...any)) {}\nfunc f() { g(log.Fatal) }\n", 1},
		{"log.Panicf", "package p\nimport \"log\"\nfunc f() { log.Panicf(\"x\") }\n", 1},
		{"a *log.Logger method", "package p\nimport \"log\"\nvar lg = log.New(nil, \"\", 0)\nfunc f() { lg.Fatalf(\"x\") }\n", 1},
		{"a *log.Logger method as a value", "package p\nimport \"log\"\nvar lg = log.New(nil, \"\", 0)\nvar fatalf = lg.Fatalln\n", 1},
		{"log's other members are not exits", "package p\nimport \"log\"\nfunc f() { log.Printf(\"x\"); log.SetFlags(0) }\n", 0},
		{"the word in a string", "package p\nimport \"log\"\nfunc f() { log.Printf(\"log.Fatalf is banned\") }\n", 0},

		// DOT IMPORTS. The exit is not a selector at all, so the whole matcher
		// looked straight through it. Both of these scored 0.
		{"dot-imported os.Exit", "package p\nimport . \"os\"\nfunc f() { Exit(1) }\n", 1},
		{"dot-imported log.Fatalf", "package p\nimport . \"log\"\nfunc f() { Fatalf(\"x\") }\n", 1},
		{"dot-imported os.Exit as a value", "package p\nimport . \"os\"\nvar bye = Exit\n", 1},
		{"a dot import's other members are not exits", "package p\nimport . \"os\"\nfunc f() { _ = Getenv(\"X\") }\n", 0},
		{"the same name without the dot import", "package p\nfunc Exit(int) {}\nfunc f() { Exit(1) }\n", 0},

		// SELF-DIRECTED SIGNALS. SIGKILL runs no defers and cannot be caught,
		// and the runtime already imports syscall and already calls
		// syscall.Kill — so this is the shape a future force-exit takes.
		{
			"syscall.Kill on this process",
			"package p\nimport (\"os\"\n\"syscall\")\nfunc f() { _ = syscall.Kill(os.Getpid(), syscall.SIGKILL) }\n",
			1,
		},
		{
			"a handle taken on this process",
			"package p\nimport \"os\"\nfunc f() { p, _ := os.FindProcess(os.Getpid()); _ = p.Signal(os.Interrupt) }\n",
			1,
		},
		// …and the reason the rule is about the TARGET and not about signalling:
		// the postmaster supervisor's whole job is signalling a child.
		{
			"signalling a child is not an exit",
			"package p\nimport \"syscall\"\nfunc f(pid int) { _ = syscall.Kill(pid, syscall.SIGINT) }\n",
			0,
		},
		{
			"a liveness probe on a foreign pid",
			"package p\nimport \"syscall\"\nfunc f(pid int) bool { return syscall.Kill(pid, 0) == nil }\n",
			0,
		},
		{
			"finding another process",
			"package p\nimport \"os\"\nfunc f(pid int) { p, _ := os.FindProcess(pid); _ = p.Kill() }\n",
			0,
		},
		// Getpid on its own is not an exit — it is in three live paths here,
		// building fallback token strings.
		{
			"Getpid in a formatted string",
			"package p\nimport (\"fmt\"\n\"os\")\nfunc f() string { return fmt.Sprintf(\"x-%d\", os.Getpid()) }\n",
			0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "x.go")
			if err := os.WriteFile(path, []byte(c.src), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := countBannedExits(t, path); got != c.want {
				t.Errorf("found %d banned exit(s), want %d, in:\n%s", got, c.want, c.src)
			}
		})
	}
}

// countBannedExits runs the audit's matcher — literally the same function the
// whole-tree audit runs — over one file. Not a reimplementation of it: the
// previous version was a second copy of the walk, and a second copy is a second
// rule that can be right about the package while the real one is wrong.
func countBannedExits(t *testing.T, path string) int {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return len(exitAuditOffences(fset, file))
}
