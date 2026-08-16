package rt

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// TestHubExporterSubmitAfterDrainerExitsIsCounted — the behavioural half.
//
// Once the drainer goroutine has returned, `e.queue` has no reader and never
// will: the exporter is a singleton whose Start is `startOnce`-guarded, so
// nothing restarts it. A Submit accepted into that channel is neither pushed
// nor counted — it is lost in silence, which is the one outcome the exporter's
// own drop counters exist to prevent. Telemetry emitted by the shutdown hooks
// that run AFTER the exporter's own hook (the chain is LIFO, and the exporter
// registers late, so it drains early) lands in exactly this window.
//
// The rule is the one the analytics writer already ships under: an item is
// pushed or it is counted.
func TestHubExporterSubmitAfterDrainerExitsIsCounted(t *testing.T) {
	var pushes atomic.Int64
	exp := NewHubExporterForTesting(func(ctx context.Context, body []byte) (int, error) {
		pushes.Add(1)
		return 200, nil
	})
	exp.Start(context.Background())
	// Stop waits for the drainer's final push, so by the time it returns the
	// queue is provably reader-less.
	exp.Stop()

	const key = "sky_telemetry_dropped_total{level=error}"
	before := exp.Metrics()[key]
	exp.Submit(KindLog, []byte(`{"emitted":"after the drain"}`), SevError)

	if got := exp.QueueLen(); got != 0 {
		t.Errorf("Submit after Stop parked %d item(s) in a queue with no reader — "+
			"the drainer has returned, so nothing will ever push them", got)
	}
	if got := exp.Metrics()[key]; got != before+1 {
		t.Errorf("dropped{level=error} %d → %d after a post-drain Submit; want %d.\n"+
			"  The item was accepted and silently discarded: not pushed, not counted.",
			before, got, before+1)
	}
}

// TestHubExporterDropsWhatTheDrainerLeftBehind — the accounting half.
//
// The flag Submit reads is set as the drainer exits, so there is a
// sub-microsecond window where a Submit already past the check enqueues an
// item the drainer will no longer see. That item is unpushable; the exit path
// counts whatever it finds left in the queue so the drop total stays total,
// rather than being off by however many callers were mid-Submit.
func TestHubExporterDropsWhatTheDrainerLeftBehind(t *testing.T) {
	exp := NewHubExporterForTesting(func(ctx context.Context, body []byte) (int, error) {
		return 200, nil
	})
	// Never started: no drainer exists, so nothing can push. Items placed in
	// the queue directly stand in for the ones that lose the exit race.
	exp.queue <- telemetryItem{kind: KindLog, severity: SevError, payload: []byte(`{"a":1}`)}
	exp.queue <- telemetryItem{kind: KindLog, severity: SevWarn, payload: []byte(`{"b":2}`)}

	exp.Stop()

	if got := exp.QueueLen(); got != 0 {
		t.Errorf("Stop left %d item(s) in the queue; they are unreachable", got)
	}
	m := exp.Metrics()
	if got := m["sky_telemetry_dropped_total{level=error}"]; got != 1 {
		t.Errorf("dropped{level=error} = %d, want 1 — the abandoned item went uncounted", got)
	}
	if got := m["sky_telemetry_dropped_total{level=warn}"]; got != 1 {
		t.Errorf("dropped{level=warn} = %d, want 1 — the abandoned item went uncounted", got)
	}
}

// TestHubExporterHasNoWriteOnlyFlags — the structural half, and the one aimed
// at the CLASS rather than at this instance of it.
//
// `draining` was written in two places, described in a third as the gate
// `Submit` consults, and read nowhere. A flag with no reader cannot be wrong
// at runtime, which is precisely why nothing caught it: the code and the
// comment were never compared. This test does the comparison mechanically —
// every atomic field of HubExporter must have at least one site that READS it
// (Load / Swap / CompareAndSwap). A field that is only ever Store()d or Add()ed
// is either dead or a gate someone forgot to wire.
func TestHubExporterHasNoWriteOnlyFlags(t *testing.T) {
	fset := token.NewFileSet()
	pkg, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	rtPkg := pkg["rt"]
	if rtPkg == nil {
		t.Fatalf("package rt not found in parsed dir (got %d packages)", len(pkg))
	}

	// 1. Collect the atomic-typed fields of HubExporter.
	fields := map[string]token.Position{}
	for _, f := range rtPkg.Files {
		// Resolve `sync/atomic`'s qualifier from THIS file's imports rather
		// than assuming it is spelled `atomic`. Matching the spelling made an
		// `import a "sync/atomic"` invisible: no field would be tracked, and
		// the gate's own "found no atomic fields" self-check only fires when
		// EVERY file is aliased, so one aliased file dropped its fields
		// silently and the gate still reported clean.
		atomicQ, atomicDot := importQualifiers(f, "sync/atomic", "atomic")
		ast.Inspect(f, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "HubExporter" {
				return true
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return false
			}
			for _, fld := range st.Fields.List {
				if !isAtomicType(fld.Type, atomicQ, atomicDot) {
					continue
				}
				for _, name := range fld.Names {
					fields[name.Name] = fset.Position(name.Pos())
				}
			}
			return false
		})
	}
	if len(fields) == 0 {
		t.Fatal("found no atomic fields on HubExporter — the gate is looking at the wrong type")
	}

	// 2. Count read vs write sites across the whole non-test package. Scanning
	//    by field NAME (not by resolved type) can only make the gate more
	//    permissive — a same-named field on another struct that IS read would
	//    satisfy this one — so it cannot produce a false red.
	//
	//    What counts as a READ is decided by whether the RESULT IS USED, not by
	//    the operation's name. `Add`, `Swap` and `CompareAndSwap` all return the
	//    state they observed, so `fails := e.consecFailures.Add(1)` is a read of
	//    the counter; `e.consecFailures.Add(1)` standing alone as a statement is
	//    not. The first draft of this gate keyed on the method name and reported
	//    consecFailures — whose value gates the circuit breaker — as write-only.
	discarded := map[*ast.CallExpr]bool{}
	for _, f := range rtPkg.Files {
		ast.Inspect(f, func(n ast.Node) bool {
			if stmt, ok := n.(*ast.ExprStmt); ok {
				if call, ok := stmt.X.(*ast.CallExpr); ok {
					discarded[call] = true
				}
			}
			return true
		})
	}
	reads := map[string]int{}
	writes := map[string]int{}
	for _, f := range rtPkg.Files {
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			op, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			fieldSel, ok := op.X.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			name := fieldSel.Sel.Name
			if _, tracked := fields[name]; !tracked {
				return true
			}
			switch op.Sel.Name {
			case "Load":
				reads[name]++
			case "Swap", "CompareAndSwap", "Add":
				if discarded[call] {
					writes[name]++
				} else {
					reads[name]++
				}
			case "Store":
				writes[name]++
			}
			return true
		})
	}

	for name, pos := range fields {
		if reads[name] == 0 {
			t.Errorf("HubExporter.%s (%s): %d write site(s), 0 read sites.\n"+
				"  A flag nothing reads is dead weight or a missing gate — decide which, "+
				"but do not leave a comment claiming it is consulted.",
				name, pos, writes[name])
		}
	}
}

// atomicWrapperNames are sync/atomic's exported wrapper types. Needed only for
// the dot-import case, where `atomic.Bool` is written as a bare `Bool` and
// there is no qualifier left to resolve.
var atomicWrapperNames = map[string]bool{
	"Bool": true, "Int32": true, "Int64": true, "Uint32": true,
	"Uint64": true, "Uintptr": true, "Pointer": true, "Value": true,
}

// isAtomicType reports whether the field type is one of the sync/atomic
// wrapper types (atomic.Bool / Int32 / Int64 / Uint64 / Pointer[T] / Value),
// under whatever local name the declaring file imports `sync/atomic` by.
func isAtomicType(expr ast.Expr, atomicQ map[string]bool, dotted bool) bool {
	switch t := expr.(type) {
	case *ast.SelectorExpr:
		pkg, ok := t.X.(*ast.Ident)
		return ok && atomicQ[pkg.Name]
	case *ast.Ident: // `import . "sync/atomic"` — the qualifier is gone
		return dotted && atomicWrapperNames[t.Name]
	case *ast.IndexExpr: // atomic.Pointer[T]
		return isAtomicType(t.X, atomicQ, dotted)
	}
	return false
}
