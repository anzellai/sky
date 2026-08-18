package rt

// Source-shape audit: NO package-level `var` in the runtime may read the
// environment in its initializer.
//
// # The rule, and why it is about ORDER rather than about environment
//
// Go evaluates every package-level `var` initializer before any `init()` in
// the same package. `dotenv.go`'s `init()` is what loads `.env` into the
// process environment. A var that reads the environment while being
// initialized therefore reads it at the one moment `.env` has provably not
// been applied — and, holding a value rather than a lookup, never reads again.
//
// The failure mode is the worst available: total, silent, and indistinguishable
// from a typo in the variable name. `SKY_STREAM_DEBUG=1` in a `.env` did
// nothing for the life of the feature. `SKY_HTTP_CLIENT_TIMEOUT` — the
// documented escape hatch for slow upstreams — did nothing, so every outbound
// request stayed pinned at 30s no matter what the operator wrote.
//
// # Why this is a gate and not a code review
//
// `streamDebug` was found by inspection and written up in the design (§1.9).
// `skyHttpClient` was NOT, and would not plausibly have been: its `os.Getenv`
// is two calls away from the declaration —
//
//	var skyHttpClient = newSkyHttpClient()
//	  -> newSkyHttpClient() -> httpEnvTimeout(...) -> os.Getenv(key)
//
// — so nothing at the declaration site looks like an environment read. A
// reviewer scanning for `os.Getenv` next to a `var` finds one of the two. The
// fixpoint below finds both, and will find the next one at any depth.
//
// # What it must NOT count, and why each one is here
//
// Every entry below is a real shape in this package that a naive version of
// this gate reds on. They are the reason the audit walks the syntax tree
// rather than grepping.
//
//  1. A function VALUE, not a call: `var lookupEnvFunc = osLookupEnv`
//     (validate.go). Assigning a function reads nothing at init. Only
//     `*ast.CallExpr` counts. The known cost of that choice: `var f =
//     os.Getenv` followed by a later `f(...)` would slip through. That is
//     accepted deliberately — a bare alias is not in the defect class,
//     because it does not read at init.
//
//  2. Lazily-resolved env by contract: `http.ProxyFromEnvironment`
//     (http_stream.go) is passed as a value and resolved by net/http on
//     first use. Excluded by (1), and named here so nobody adds it to the
//     leaf set on the strength of the name.
//
//  3. Deferred bodies: `sync.OnceValue(func() { ... os.Getenv ... })` reads
//     nothing at init, because the closure does not run at init. The walk
//     therefore does NOT descend into `*ast.FuncLit` bodies. This is the
//     single likeliest source of a false red, and the shape is live in this
//     package (lazy.go, serverless.go, and now skyHTTPClientOnce).
//
//  4. Text that merely looks like Go: live.go and console_html.go embed tens
//     of thousands of lines of JavaScript in backtick raw strings, containing
//     both `var ` and env-ish identifiers. A grep-based version of this audit
//     is unusable for that reason alone.
//
// # Known gap, stated rather than papered over
//
// A var initialised from ANOTHER package's env-reading function (`var c =
// telemetry.SomethingFromEnv()`) is invisible to a per-package call graph.
// There are zero such instances today. The audit walks every package under
// `runtime-go/rt/` so each is checked against its own graph, but a
// cross-package initializer edge is not followed. Note that a subpackage var
// is initialised even EARLIER than `rt`'s — imported packages initialise
// first — so a future env read there is unrescuable by any hook `rt` owns.
//
// # The remedy this gate accepts
//
// Read on demand (`func x() bool { return os.Getenv(...) == "1" }`), or defer
// the read behind `sync.Once`/`OnceValue` so it happens at first use, or keep
// the var and register an `onEnvPrefixChange` hook that reassigns it (the
// `logThreshold` / `csrfEnabled` pattern). The first two are preferred: the
// hook fires only from `SetEnvPrefix` / `SetSkyDefault`, so it works only
// because generated `init()` code happens to always call one of them.

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

// envReadLeaves — functions that read the process environment. Names are
// matched on the final selector segment as well as the qualified form, so
// `os.Getenv` and a dot-imported `Getenv` both land.
var envReadLeaves = map[string]bool{
	"os.Getenv":      true,
	"os.LookupEnv":   true,
	"os.Environ":     true,
	"skyGetenv":      true,
	"skyLookupEnv":   true,
	"skyEnvName":     true,
	"lookupEnvRaw":   true,
	"osLookupEnv":    true,
	"osEnv":          true,
	"httpEnvTimeout": true,
}

// envInitAllowed — package-level vars permitted to read the environment at
// init, each with the reason it is safe. EMPTY, and that is the intended
// state: both members of this class were fixed rather than allowlisted. An
// entry here is a claim that the value cannot be affected by `.env`, and it
// has to be argued in the string.
var envInitAllowed = map[string]string{}

func TestNoPackageLevelVarReadsEnvAtInit(t *testing.T) {
	root := runtimeRTRoot(t)
	var findings []string

	for _, dir := range goPackageDirs(t, root) {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, 0)
		if err != nil {
			t.Fatalf("parsing %s: %v", dir, err)
		}
		for _, pkg := range pkgs {
			findings = append(findings, auditPackage(fset, pkg)...)
		}
	}

	sort.Strings(findings)
	if len(findings) > 0 {
		t.Fatalf("package-level var(s) read the environment at init, BEFORE dotenv's "+
			"init() loads .env — so the variable is silently unreachable from a .env "+
			"file, permanently:\n\n  %s\n\nFix by reading on demand, deferring behind "+
			"sync.Once/OnceValue, or registering an onEnvPrefixChange hook that "+
			"reassigns it. See this file's header for the three remedies and their "+
			"trade-offs.", strings.Join(findings, "\n  "))
	}
}

// auditPackage returns one finding per offending package-level var.
func auditPackage(fset *token.FileSet, pkg *ast.Package) []string {
	// 1. Call graph over functions declared in this package.
	calls := map[string]map[string]bool{}
	for _, f := range pkg.Files {
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			name := fn.Name.Name
			if calls[name] == nil {
				calls[name] = map[string]bool{}
			}
			for c := range calledNames(fn.Body, true) {
				calls[name][c] = true
			}
		}
	}

	// 2. Fixpoint: which functions transitively reach an environment read.
	//    Two levels is not enough — skyHttpClient was exactly two — so this
	//    iterates to closure rather than to a depth limit.
	reaches := map[string]bool{}
	for name := range envReadLeaves {
		reaches[name] = true
	}
	for changed := true; changed; {
		changed = false
		for fn, callees := range calls {
			if reaches[fn] {
				continue
			}
			for c := range callees {
				if reaches[c] {
					reaches[fn] = true
					changed = true
					break
				}
			}
		}
	}

	// 3. Vars a registered onEnvPrefixChange callback reassigns.
	rescued := rescuedVars(pkg, calls)

	// 4. Every file-scope var initializer.
	var findings []string
	for _, f := range pkg.Files {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, val := range vs.Values {
					if i >= len(vs.Names) {
						break
					}
					varName := vs.Names[i].Name
					// FuncLit bodies excluded: a deferred closure reads
					// nothing at init (see header, item 3).
					var hit string
					for c := range calledNames(val, false) {
						if reaches[c] {
							hit = c
							break
						}
					}
					if hit == "" {
						continue
					}
					if _, ok := envInitAllowed[varName]; ok {
						continue
					}
					if rescued[varName] {
						continue
					}
					pos := fset.Position(vs.Names[i].Pos())
					findings = append(findings, filepath.Base(pos.Filename)+":"+
						strconv.Itoa(pos.Line)+" — var "+varName+" reaches an environment "+
						"read via "+hit+"()")
				}
			}
		}
	}
	return findings
}

// rescuedVars collects the identifiers assigned inside every callback handed
// to onEnvPrefixChange, covering all three local shapes: an inline closure
// (rt.go's logThreshold/logJSON), a named function (csrf_middleware.go's
// refreshCsrfEnabled), and an atomic Store on a package-level var.
func rescuedVars(pkg *ast.Package, calls map[string]map[string]bool) map[string]bool {
	rescued := map[string]bool{}
	// Bodies of named functions, for resolving `onEnvPrefixChange(fnName)`.
	bodies := map[string]*ast.BlockStmt{}
	for _, f := range pkg.Files {
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Body != nil {
				bodies[fn.Name.Name] = fn.Body
			}
		}
	}
	collect := func(n ast.Node) {
		ast.Inspect(n, func(x ast.Node) bool {
			switch s := x.(type) {
			case *ast.AssignStmt:
				for _, lhs := range s.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						rescued[id.Name] = true
					}
				}
			case *ast.CallExpr:
				// `someVar.Store(...)` — the atomic.Bool shape.
				if sel, ok := s.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Store" {
					if id, ok := sel.X.(*ast.Ident); ok {
						rescued[id.Name] = true
					}
				}
			}
			return true
		})
	}
	for _, f := range pkg.Files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			id, ok := call.Fun.(*ast.Ident)
			if !ok || id.Name != "onEnvPrefixChange" || len(call.Args) != 1 {
				return true
			}
			switch a := call.Args[0].(type) {
			case *ast.FuncLit:
				collect(a.Body)
			case *ast.Ident:
				if b, ok := bodies[a.Name]; ok {
					collect(b)
				}
			}
			return true
		})
	}
	return rescued
}

// calledNames returns the names CALLED within n. `descendFuncLits` is true for
// function bodies (where a closure does run) and false for var initializers
// (where it does not — see header item 3).
func calledNames(n ast.Node, descendFuncLits bool) map[string]bool {
	out := map[string]bool{}
	ast.Inspect(n, func(x ast.Node) bool {
		if _, ok := x.(*ast.FuncLit); ok && !descendFuncLits {
			return false
		}
		call, ok := x.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			out[fn.Name] = true
		case *ast.SelectorExpr:
			out[fn.Sel.Name] = true
			if pkgID, ok := fn.X.(*ast.Ident); ok {
				out[pkgID.Name+"."+fn.Sel.Name] = true
			}
		}
		return true
	})
	return out
}

func runtimeRTRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return wd
}

func goPackageDirs(t *testing.T, root string) []string {
	t.Helper()
	seen := map[string]bool{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			seen[filepath.Dir(path)] = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	dirs := make([]string, 0, len(seen))
	for d := range seen {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	if len(dirs) == 0 {
		t.Fatal("no Go packages found — the audit would pass vacuously")
	}
	return dirs
}
