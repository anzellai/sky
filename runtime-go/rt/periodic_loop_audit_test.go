package rt

// Source-shape audit: a PERIODIC BACKGROUND LOOP must recover per cycle, and
// must not discard a database write's error.
//
// # The rule, and why it is about SCOPE rather than about recovering
//
// A background goroutine written the obvious way carries one or both of two
// faults, and both fail silently and permanently:
//
//	go func() {
//	    defer func() { _ = recover() }()   // (1) recover at the TOP LEVEL
//	    t := time.NewTicker(6 * time.Hour)
//	    for range t.C {
//	        _, _ = db.Exec(`DELETE ...`)    // (2) the error is DISCARDED
//	    }
//	}()
//
//  1. The recover is scoped to the GOROUTINE, not the cycle. A panic anywhere
//     in the work unwinds PAST the loop, the deferred recover swallows it, and
//     the goroutine returns. The loop is dead for the process lifetime, with
//     no log line and no symptom until whatever it maintained has grown
//     without bound for a day. It LOOKS defensive and is the exact opposite:
//     without the recover the process would at least have crashed loudly.
//
//  2. A permissions failure, a lock timeout and a dropped table become
//     indistinguishable from a successful zero-row delete. A loop that has
//     never once done its job looks identical to a healthy one.
//
// # Why this is a gate and not a code review
//
// EIGHT sites carried this in one runtime: the analytics retention pruner and
// telemetry/persist.go's (fixed at 48a6a4be), then the telemetry drainer, the
// Time.every ticker, both SQL session-cleanup loops, the hub pruner, the jobs
// worker, the spool sweep and the JTI janitor. Every one of them was found by
// reading code, and each was found separately. The two fixed first had
// EXACTLY COMPLEMENTARY defects — one recovered at the top level and discarded
// its error, the other checked its error and had no recover — which is what a
// class looks like when it is being closed one instance at a time.
//
// # What it must NOT count, and why each one is here
//
//  1. A `defer` inside the loop body is NOT per-cycle. Go runs it at FUNCTION
//     exit, not at iteration end, so `for { defer func(){recover()}() ... }`
//     is the top-level defect wearing the fix's clothes — and it also leaks a
//     defer per iteration. That shape is reported as its own finding rather
//     than silently accepted, because it is the mistake a reader of the fixed
//     code is most likely to make next.
//
//  2. Recovery through a CALL is what counts: `periodic.Guard(...)` /
//     `periodic.Every(...)` in the loop body, or an immediately-invoked
//     `func(){ defer func(){recover()}(); ... }()`. Those genuinely run their
//     deferred recover once per iteration.
//
//  3. The audit does NOT descend into nested function literals when looking
//     for a function's own loops and defers — each literal is analysed as its
//     own function. That is what makes `go func(){ defer recover; for ... }()`
//     land on the literal, where the defect actually is, rather than on
//     whatever enclosing function happened to spawn it.
//
//  4. The TUI, webview and CLI event loops recover and then call
//     ExitProcess(2). That is loud, deliberate death, not a swallowed panic,
//     and it is the correct behaviour for a foreground process a user is
//     watching — so `exitOnPanic` shapes are recognised and permitted. See
//     tuiExitOnPanicRecovery.
//
//  5. Generated Sky output (rt/console_app) is not hand-written and cannot be
//     hand-fixed; it is excluded by path, and named here so nobody concludes
//     the audit silently missed it.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// periodicLoopAllowed — periodic loops permitted to recover outside the cycle,
// each with the reason it is safe. Keyed "<file>:<func>".
//
// EMPTY, and that is the intended state: every member of this class was fixed
// rather than allowlisted. An entry here is a claim that losing the whole
// goroutine on the first panic is acceptable, and it has to be argued in the
// string.
//
// A STALE entry fails this gate exactly as an unaccounted new loop does — see
// TestPeriodicLoopAllowlistHasNoStaleEntries. An exclusion that outlives the
// thing it excused is a hole nobody is holding open on purpose.
var periodicLoopAllowed = map[string]string{}

// discardedExecAllowed — discarded database write results, each with the
// reason the failure genuinely does not matter. Keyed "<file>:<func>".
//
// EMPTY, and intended to stay so. `_, _ = db.Exec(...)` says two things at
// once — "this row count is uninteresting" and "this failure is
// uninteresting" — and only the first is ever meant. Use execIgnoringRows, or
// return the error.
var discardedExecAllowed = map[string]string{}

// excludedDirs are paths the audit does not walk, with the reason.
var excludedDirs = map[string]string{
	"console_app": "generated Sky output — not hand-written, cannot be hand-fixed",
}

// ── the audit ───────────────────────────────────────────────────────────────

type loopFinding struct {
	key    string // "<file>:<func>"
	detail string
}

func TestPeriodicLoopsRecoverPerCycle(t *testing.T) {
	findings, seen := auditPeriodicLoops(t)

	var unaccounted []string
	for _, f := range findings {
		if _, ok := periodicLoopAllowed[f.key]; !ok {
			unaccounted = append(unaccounted, f.key+" — "+f.detail)
		}
	}
	sort.Strings(unaccounted)

	if len(seen) == 0 {
		t.Fatal("the audit found no periodic loops at all in runtime-go — it would " +
			"pass vacuously. The walk or the detector is broken, not the runtime.")
	}

	if len(unaccounted) > 0 {
		t.Fatalf("periodic background loop(s) do not recover per cycle:\n\n  %s\n\n"+
			"A panic in the work then unwinds PAST the loop and the goroutine exits for "+
			"the lifetime of the process — silently, because the deferred recover swallows "+
			"it. Scope the recover to ONE CYCLE by running the work through "+
			"periodic.Guard (or periodic.Every, which is that loop). Note a bare `defer` "+
			"INSIDE the loop body is not a fix: Go runs it at function exit, not at "+
			"iteration end.\n\n"+
			"(%d periodic loop(s) audited.)", strings.Join(unaccounted, "\n  "), len(seen))
	}
}

// TestPeriodicLoopAllowlistHasNoStaleEntries — an allowlist entry that no
// longer matches anything FAILS.
//
// A stale exclusion is worse than a missing one. It reads as a live decision
// somebody made on purpose, so the next reader honours it; meanwhile it covers
// nothing, and if the shape it excused ever comes back it is pre-approved. The
// allowlist has to shrink when the code does.
func TestPeriodicLoopAllowlistHasNoStaleEntries(t *testing.T) {
	findings, _ := auditPeriodicLoops(t)
	live := map[string]bool{}
	for _, f := range findings {
		live[f.key] = true
	}
	var stale []string
	for key, reason := range periodicLoopAllowed {
		if !live[key] {
			stale = append(stale, key+" (excused as: "+reason+")")
		}
	}

	execFindings := auditDiscardedExecs(t)
	liveExec := map[string]bool{}
	for _, f := range execFindings {
		liveExec[f.key] = true
	}
	for key, reason := range discardedExecAllowed {
		if !liveExec[key] {
			stale = append(stale, key+" (excused as: "+reason+")")
		}
	}

	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("allowlist entr(ies) no longer match anything in the tree:\n\n  %s\n\n"+
			"Delete them. A stale exclusion reads as a live decision, so the next reader "+
			"honours it — and if the shape it excused ever returns, it is pre-approved.",
			strings.Join(stale, "\n  "))
	}
}

func TestPeriodicLoopsDoNotDiscardWriteErrors(t *testing.T) {
	findings := auditDiscardedExecs(t)

	var unaccounted []string
	for _, f := range findings {
		if _, ok := discardedExecAllowed[f.key]; !ok {
			unaccounted = append(unaccounted, f.key+" — "+f.detail)
		}
	}
	sort.Strings(unaccounted)

	if len(unaccounted) > 0 {
		t.Fatalf("database write result(s) discarded:\n\n  %s\n\n"+
			"`_, _ = db.Exec(...)` says two things at once — this row count is "+
			"uninteresting, and this failure is uninteresting — and only the first is "+
			"ever meant. A permissions failure, a lock timeout and a successful zero-row "+
			"write become the same observable. Return the error, log it, or use "+
			"execIgnoringRows when genuinely only the row count is unwanted.",
			strings.Join(unaccounted, "\n  "))
	}
}

// ── detection ───────────────────────────────────────────────────────────────

// auditPeriodicLoops returns the offending loops and the total number audited.
// The second value exists so the gate can refuse to pass vacuously.
func auditPeriodicLoops(t *testing.T) (findings []loopFinding, seen []string) {
	t.Helper()
	tree := loadRuntimeTree(t)
	forEachFuncIn(tree, func(fn auditedFunc) {
		// Only DETACHED loops are in this class. A loop running on a
		// caller's stack — a transaction retry, `pg_embed`'s readiness
		// wait, an HTTP handler's SSE pump — has somebody to propagate a
		// panic to, and net/http or the caller decides what that means.
		// A loop on a goroutine nobody joined has nobody: the panic is the
		// end of it, and that is the whole defect.
		if !fn.goLaunched {
			return
		}
		loops := periodicLoopsIn(fn.body)
		if len(loops) == 0 {
			return
		}
		key := fn.file + ":" + fn.name
		body := fn.body
		seen = append(seen, key)

		// A recover deferred at the FUNCTION's top level is outside every
		// cycle by construction.
		if topLevelDeferRecovers(body) && !tuiExitOnPanicRecovery(body) {
			findings = append(findings, loopFinding{key,
				"recover is deferred at the function's top level, outside the loop"})
			return
		}
		for _, loop := range loops {
			lb := loopBody(loop)
			if lb == nil {
				continue
			}
			if deferDirectlyInLoop(lb) {
				findings = append(findings, loopFinding{key,
					"a bare `defer` sits in the loop body — Go runs it at FUNCTION exit, " +
						"not at iteration end, so it is not per-cycle and it leaks one " +
						"defer per iteration"})
				break
			}
			if !loopIsGuarded(lb, tree.recovering) {
				findings = append(findings, loopFinding{key,
					"the loop body has no per-cycle recovery — it calls nothing that " +
						"recovers, so the first panic ends the goroutine"})
				break
			}
		}
	})
	return findings, seen
}

func auditDiscardedExecs(t *testing.T) (findings []loopFinding) {
	t.Helper()
	forEachFuncIn(loadRuntimeTree(t), func(fn auditedFunc) {
		file, name, body := fn.file, fn.name, fn.body
		ast.Inspect(body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || len(assign.Rhs) != 1 {
				return true
			}
			call, ok := assign.Rhs[0].(*ast.CallExpr)
			if !ok || !isExecCall(call) {
				return true
			}
			allBlank := true
			for _, lhs := range assign.Lhs {
				if id, ok := lhs.(*ast.Ident); !ok || id.Name != "_" {
					allBlank = false
					break
				}
			}
			if allBlank {
				findings = append(findings, loopFinding{file + ":" + name,
					"the result of a database Exec is discarded into blanks"})
			}
			return true
		})
	})
	return findings
}

// periodicLoopsIn returns the periodic loops written DIRECTLY in body — not
// those inside nested function literals, which are audited as their own
// functions.
func periodicLoopsIn(body *ast.BlockStmt) []ast.Node {
	var loops []ast.Node
	walkSkippingFuncLits(body, func(n ast.Node) {
		switch loop := n.(type) {
		case *ast.RangeStmt:
			if isTickerChan(loop.X) {
				loops = append(loops, loop)
			}
		case *ast.ForStmt:
			if loopIsPeriodic(loop) {
				loops = append(loops, loop)
			}
		}
	})
	return loops
}

// loopIsPeriodic reports whether a `for` is one of the periodic shapes: it
// waits on a ticker/timer channel, sleeps, or is an unconditional loop driven
// by a select (the drainer / worker shape).
func loopIsPeriodic(loop *ast.ForStmt) bool {
	if loop.Body == nil {
		return false
	}
	periodic := false
	ast.Inspect(loop.Body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.UnaryExpr:
			if x.Op == token.ARROW && isTickerChan(x.X) {
				periodic = true
			}
		case *ast.CallExpr:
			if isTimeCall(x, "Sleep") || isTimeCall(x, "After") || isTimeCall(x, "Tick") {
				periodic = true
			}
		}
		return !periodic
	})
	if periodic {
		return true
	}
	// `for { select { ... } }` with no condition — the event-loop shape. Its
	// failure mode is identical to a ticker's: one panic and the goroutine is
	// gone.
	//
	// Excluded: the bounded DRAIN, `for { select { case <-ch: n++; default:
	// return n } }`. Its `default` terminates, so it always runs to completion
	// on the caller's stack and is not a background wait at all —
	// analytics_writer.go's sweepUnwritten is the live example, and counting
	// it was a false red. A `default` that merely falls through (the jobs
	// worker's stop-probe) does NOT exclude the loop, because that one does
	// park on its work.
	if loop.Cond == nil && loop.Init == nil && loop.Post == nil {
		for _, stmt := range loop.Body.List {
			sel, ok := stmt.(*ast.SelectStmt)
			if !ok {
				continue
			}
			if selectDefaultTerminates(sel) {
				continue
			}
			return true
		}
	}
	return false
}

// selectDefaultTerminates reports whether the select has a `default` clause
// that leaves the loop — the signature of a bounded drain rather than a
// background wait.
func selectDefaultTerminates(sel *ast.SelectStmt) bool {
	if sel.Body == nil {
		return false
	}
	for _, cc := range sel.Body.List {
		comm, ok := cc.(*ast.CommClause)
		if !ok || comm.Comm != nil { // Comm == nil is the `default` clause
			continue
		}
		for _, s := range comm.Body {
			switch st := s.(type) {
			case *ast.ReturnStmt:
				return true
			case *ast.BranchStmt:
				if st.Tok == token.BREAK {
					return true
				}
			}
		}
	}
	return false
}

// isTickerChan matches `<something>.C` — time.Ticker and time.Timer both
// expose their channel under that name, and nothing else in this runtime does.
func isTickerChan(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	return ok && sel.Sel != nil && sel.Sel.Name == "C"
}

func isTimeCall(call *ast.CallExpr, fn string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != fn {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "time"
}

func isExecCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil {
		return false
	}
	return sel.Sel.Name == "Exec" || sel.Sel.Name == "ExecContext"
}

// loopBody returns the block a for/range statement executes.
func loopBody(n ast.Node) *ast.BlockStmt {
	switch loop := n.(type) {
	case *ast.ForStmt:
		return loop.Body
	case *ast.RangeStmt:
		return loop.Body
	}
	return nil
}

// topLevelDeferRecovers reports whether the function defers a recover in its
// OWN statement list — outside any loop, and therefore outside every cycle.
func topLevelDeferRecovers(body *ast.BlockStmt) bool {
	for _, stmt := range body.List {
		if d, ok := stmt.(*ast.DeferStmt); ok && containsRecover(d) {
			return true
		}
	}
	return false
}

// deferDirectlyInLoop reports whether a `defer` sits in the loop's own
// statement list. Go runs it at FUNCTION exit, so it is not per-cycle.
func deferDirectlyInLoop(body *ast.BlockStmt) bool {
	for _, stmt := range body.List {
		if d, ok := stmt.(*ast.DeferStmt); ok && containsRecover(d) {
			return true
		}
		// Also inside a select's case bodies, which is where these loops
		// actually put their work.
		if sel, ok := stmt.(*ast.SelectStmt); ok && sel.Body != nil {
			for _, cc := range sel.Body.List {
				comm, ok := cc.(*ast.CommClause)
				if !ok {
					continue
				}
				for _, s := range comm.Body {
					if d, ok := s.(*ast.DeferStmt); ok && containsRecover(d) {
						return true
					}
				}
			}
		}
	}
	return false
}

// loopIsGuarded reports whether the loop body recovers through a CALL — the
// only construction that actually runs a recover once per iteration.
//
// `recovering` is the transitive set of function names that recover, computed
// as a fixpoint over the tree (see loadRuntimeTree). Following calls rather
// than pattern-matching `periodic.Guard` is what lets the audit accept the fix
// shape 48a6a4be established — a `pruneCycle` method holding the recover,
// called from the loop — instead of insisting every loop in the runtime be
// rewritten to look like the newest one.
func loopIsGuarded(body *ast.BlockStmt, recovering map[string]bool) bool {
	guarded := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if name := calleeName(call); name != "" && recovering[name] {
			guarded = true
		}
		// An immediately-invoked recovering literal: func(){ defer
		// func(){recover()}(); ... }().
		if lit, ok := call.Fun.(*ast.FuncLit); ok && containsRecover(lit) {
			guarded = true
		}
		return !guarded
	})
	return guarded
}

// calleeName reduces a call to the name the call graph is keyed on: `f(...)`
// to "f", `x.m(...)` and `pkg.F(...)` to "m" / "F".
//
// Matching on the final segment is an over-approximation — two methods sharing
// a name are conflated, so a loop calling ANY `pruneCycle` counts as guarded if
// SOME `pruneCycle` recovers. That is the same trade the sibling audit in
// env_init_order_audit_test.go makes, and it is stated rather than hidden: it
// can only cause a MISS, never a false red, and method names in this runtime
// are distinctive enough that no live pair collides today.
func calleeName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		if fn.Sel != nil {
			return fn.Sel.Name
		}
	}
	return ""
}

func containsRecover(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "recover" {
			found = true
		}
		return !found
	})
	return found
}

// tuiExitOnPanicRecovery recognises the TUI / webview / CLI shape: recover,
// then terminate the process deliberately. That is loud death, not a swallowed
// panic — the opposite of this defect class — and it is correct for a
// foreground process a user is watching, so it is permitted rather than
// allowlisted.
func tuiExitOnPanicRecovery(body *ast.BlockStmt) bool {
	exits := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := ""
		switch fn := call.Fun.(type) {
		case *ast.Ident:
			name = fn.Name
		case *ast.SelectorExpr:
			if fn.Sel != nil {
				name = fn.Sel.Name
			}
		}
		if name == "ExitProcess" || name == "Exit" || name == "Goexit" {
			exits = true
		}
		return !exits
	})
	return exits
}

// ── walking ─────────────────────────────────────────────────────────────────

// auditedFunc is one function or function literal under audit.
type auditedFunc struct {
	file string // path relative to runtime-go/
	name string // "pruner", "Start.func1"
	body *ast.BlockStmt
	// goLaunched records that this function runs on a detached goroutine —
	// either it IS a `go func(){...}()` literal, or its name appears as the
	// callee of a `go` statement somewhere in the tree.
	goLaunched bool
}

// runtimeTree is the parsed runtime plus the two derived facts the audit needs:
// which functions run detached, and which recover.
type runtimeTree struct {
	funcs []auditedFunc
	// recovering is the transitive closure of "this function recovers" over
	// the call graph, keyed on the final name segment. Seeded from functions
	// whose own body defers a recover, plus periodic.Guard / periodic.Every,
	// which live in a package the per-package graph does not span.
	recovering map[string]bool
}

func forEachFuncIn(tree *runtimeTree, fn func(auditedFunc)) {
	for _, f := range tree.funcs {
		fn(f)
	}
}

// loadRuntimeTree parses every non-test Go file under runtime-go/ and derives
// the go-launched set and the recovering fixpoint.
//
// # Known gap, stated rather than papered over
//
// The go-launched set is keyed on the callee's final name segment across the
// WHOLE tree, not resolved per type. A method named `run` launched with `go`
// in one package therefore marks every `run` as detached. That over-includes,
// which can only cost a false RED that a reader resolves by looking — never a
// miss. A function launched exclusively through a stored `func()` value
// (`h := s.loop; go h()`) is invisible to it; there are no such sites today.
func loadRuntimeTree(t *testing.T) *runtimeTree {
	t.Helper()
	root := runtimeGoRoot(t)
	fset := token.NewFileSet()

	tree := &runtimeTree{recovering: map[string]bool{
		// Cross-package, so the per-name fixpoint below cannot reach them.
		"Guard": true,
		"Every": true,
	}}
	goLaunchedNames := map[string]bool{}
	// literalIsGoLaunched marks the *ast.FuncLit nodes that are the callee of
	// a `go` statement, so `go func(){...}()` is recognised directly.
	literalIsGoLaunched := map[*ast.FuncLit]bool{}
	// callsOf maps a function's name to the names it calls, for the fixpoint.
	callsOf := map[string][]string{}
	recoversDirectly := map[string]bool{}

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if _, skip := excludedDirs[info.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		rel, _ := filepath.Rel(root, path)

		// Pass 1 over this file: every `go` statement.
		ast.Inspect(f, func(n ast.Node) bool {
			gostmt, ok := n.(*ast.GoStmt)
			if !ok || gostmt.Call == nil {
				return true
			}
			if lit, ok := gostmt.Call.Fun.(*ast.FuncLit); ok {
				literalIsGoLaunched[lit] = true
				return true
			}
			if name := calleeName(gostmt.Call); name != "" {
				goLaunchedNames[name] = true
			}
			return true
		})

		// Pass 2: collect functions and literals.
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			record := func(name string, body *ast.BlockStmt, lit *ast.FuncLit) {
				if topLevelDeferRecovers(body) {
					recoversDirectly[name] = true
				}
				var calls []string
				walkSkippingFuncLits(body, func(n ast.Node) {
					if c, ok := n.(*ast.CallExpr); ok {
						if cn := calleeName(c); cn != "" {
							calls = append(calls, cn)
						}
					}
				})
				callsOf[name] = append(callsOf[name], calls...)
				tree.funcs = append(tree.funcs, auditedFunc{
					file: rel, name: name, body: body,
					goLaunched: lit != nil && literalIsGoLaunched[lit],
				})
			}
			record(fd.Name.Name, fd.Body, nil)
			// Each nested literal is its own function — that is what puts
			// `go func(){ defer recover; for ... }()` on the literal, where
			// the defect is.
			litN := 0
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				fl, ok := n.(*ast.FuncLit)
				if !ok || fl.Body == nil {
					return true
				}
				litN++
				record(fmt.Sprintf("%s.func%d", fd.Name.Name, litN), fl.Body, fl)
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}

	// Resolve go-launched-by-name now that every file's `go` statements are in,
	// then propagate along calls to a fixed point.
	//
	// The propagation is not a refinement, it is load-bearing. `go
	// s.cleanupLoop()` where `cleanupLoop` is a one-line delegation to
	// `runCleanupLoop` — the shape this audit's own fixes introduced, so the
	// loop could be driven by a gate with an injected execer — leaves the LOOP
	// in a function no `go` statement names. Without this, the audit skipped
	// every loop it had just been written to protect, and reported PASS. It
	// was caught by mutating a fixed site back to the defect and watching the
	// gate stay green.
	for i := range tree.funcs {
		if goLaunchedNames[tree.funcs[i].name] {
			tree.funcs[i].goLaunched = true
		}
	}
	for changed := true; changed; {
		changed = false
		for i := range tree.funcs {
			if !tree.funcs[i].goLaunched {
				continue
			}
			for _, callee := range callsOf[tree.funcs[i].name] {
				if !goLaunchedNames[callee] {
					goLaunchedNames[callee] = true
					changed = true
				}
			}
		}
		if changed {
			for i := range tree.funcs {
				if goLaunchedNames[tree.funcs[i].name] {
					tree.funcs[i].goLaunched = true
				}
			}
		}
	}

	// Fixpoint: a function recovers if it defers a recover itself, or calls
	// something that recovers. Iterated to a fixed point so a recover two or
	// three calls deep still counts — which is the shape `analyticsPruneOnce`
	// and `pruneCycle` both have.
	for name := range recoversDirectly {
		tree.recovering[name] = true
	}
	for changed := true; changed; {
		changed = false
		for name, calls := range callsOf {
			if tree.recovering[name] {
				continue
			}
			for _, c := range calls {
				if tree.recovering[c] {
					tree.recovering[name] = true
					changed = true
					break
				}
			}
		}
	}
	return tree
}

// walkSkippingFuncLits visits every node in body EXCEPT those inside nested
// function literals, which are audited as their own functions. Without the
// skip, `go func(){ defer recover; for range t.C {...} }()` would report
// against the enclosing function — which has no loop and no defect — instead
// of against the literal, where both are.
func walkSkippingFuncLits(body *ast.BlockStmt, visit func(ast.Node)) {
	var walk func(ast.Node)
	walk = func(n ast.Node) {
		ast.Inspect(n, func(node ast.Node) bool {
			if node == nil {
				return false
			}
			if _, ok := node.(*ast.FuncLit); ok && node != ast.Node(body) {
				return false // audited separately
			}
			if node != ast.Node(body) {
				visit(node)
			}
			return true
		})
	}
	walk(body)
}

// runtimeGoRoot returns runtime-go/, so the audit covers rt AND its
// subpackages — rt/hub, rt/jobs, rt/telemetry and rt/periodic all own loops in
// this class, and three of them cannot import rt.
func runtimeGoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Dir(wd) // rt/ -> runtime-go/
}
