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

// exitAuditBanned are the process-ending calls that skip deferred functions.
// syscall.Exit is here too: it is the same primitive one import away, and a
// gate that named only os.Exit would be closed by spelling.
var exitAuditBanned = map[string]string{
	"os":      "Exit",
	"syscall": "Exit",
}

func TestNoRuntimeCodeExitsWithoutStoppingTheEmbeddedDatabase(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("cannot read the package directory: %v", err)
	}

	fset := token.NewFileSet()
	var offences []string
	scanned, allowedSeen := 0, map[string]bool{}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		// Test files are excluded deliberately: the child-process helpers in
		// pg_embed_live_test.go and pg_embed_ownership_test.go signal failure to
		// their parent with an exit code, which IS the observation the parent
		// makes. They are not app code and no generated main defers anything
		// around them.
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++

		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("cannot parse %s: %v", name, err)
		}
		// Track the local name of each banned import, so an aliased
		// `import xos "os"` is still caught.
		locals := map[string]string{} // local ident → package path
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if _, banned := exitAuditBanned[path]; !banned {
				continue
			}
			local := path
			if imp.Name != nil {
				local = imp.Name.Name
			}
			locals[local] = path
		}
		if len(locals) == 0 {
			continue
		}

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			path, ok := locals[pkgIdent.Name]
			if !ok || sel.Sel.Name != exitAuditBanned[path] {
				return true
			}
			if _, allowed := exitAuditAllowed[name]; allowed {
				allowedSeen[name] = true
				return true
			}
			pos := fset.Position(call.Pos())
			offences = append(offences, name+":"+strconv.Itoa(pos.Line)+" calls "+path+".Exit")
			return true
		})
	}

	if scanned < 10 {
		t.Fatalf("the audit only scanned %d files — it is not looking at the package "+
			"and would pass on anything", scanned)
	}

	if len(offences) > 0 {
		sort.Strings(offences)
		t.Errorf("%d runtime exit(s) bypass rt.ExitProcess:\n  %s\n\n"+
			"os.Exit does not run deferred functions, so each of these skips generated\n"+
			"main's `defer rt.StopEmbeddedPostgres()` and leaves an `--embed` app's\n"+
			"PostgreSQL running with nothing left to stop it — which the NEXT run adopts\n"+
			"and, by the ownership rule, never stops either.\n"+
			"Call rt.ExitProcess(code) instead. If a site genuinely cannot (the database\n"+
			"is already gone, or a defer ordering proves the stop has run), add the FILE\n"+
			"to exitAuditAllowed with the reason.",
			len(offences), strings.Join(offences, "\n  "))
	}

	// The allowlist is checked back: an entry whose file no longer calls
	// os.Exit is a stale exemption, and a stale exemption is how the next
	// bypass gets waved through.
	for file, why := range exitAuditAllowed {
		if !allowedSeen[file] {
			t.Errorf("exitAuditAllowed names %s (%q) but it no longer calls os.Exit — "+
				"drop the entry rather than leaving a file permanently exempt", file, why)
		}
	}
}

// The audit is only worth having if it can see a violation. This proves the
// matcher on synthetic sources rather than trusting that the real package is
// representative: an aliased import and a call inside a nested closure are the
// two shapes a text search misses, and a mention in a comment or a string is
// the shape a text search wrongly reports.
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

// countBannedExits is the audit's matcher, over one file. Kept beside the audit
// so the two cannot drift.
func countBannedExits(t *testing.T, path string) int {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	locals := map[string]string{}
	for _, imp := range file.Imports {
		p := strings.Trim(imp.Path.Value, `"`)
		if _, banned := exitAuditBanned[p]; !banned {
			continue
		}
		local := p
		if imp.Name != nil {
			local = imp.Name.Name
		}
		locals[local] = p
	}
	n := 0
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		id, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if p, ok := locals[id.Name]; ok && sel.Sel.Name == exitAuditBanned[p] {
			n++
		}
		return true
	})
	return n
}
