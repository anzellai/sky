package rt

// Every `Log_warn(...)` the runtime writes for ITSELF must actually be emitted.
//
// # The defect
//
// `Log_warn` is a Sky KERNEL: it returns a `Task`, which in the emitted Go is a
// `func() any` the runtime forces later. Written as a bare Go statement —
//
//	Log_warn("db.connect: " + skyEnvName("DB_MAX_OPEN_CONNS") + "=0 means UNLIMITED …")
//
// — it constructs the thunk and drops it. The call compiles, reads exactly like
// a log line, and emits nothing. Every internal warning in the runtime was
// written that way, so the operator-facing diagnostics for an ignored SQLite
// pool knob, an unparseable duration, an unlimited pool, an unrecognised
// isolation level and a non-replayable retry budget were all dead.
//
// That is worse than a missing feature: `resolveDbPoolConfigFor` FALLS BACK
// when a value cannot be used, and the warning was the only thing that told
// anyone the fallback had happened. A knob that looks set and does nothing,
// silently, is precisely the failure mode `sky.toml`'s unknown-key warning
// exists to prevent.
//
// # Two gates, because either alone is weak
//
//  1. `TestTheRuntimesOwnWarningsReachTheLog` forces the real code path and
//     reads the telemetry ring `logEmit` writes to. It proves the message is
//     delivered, but only for the paths it exercises.
//  2. `TestNoRuntimeSourceDropsALogKernelThunk` parses rt's own sources for a
//     `Log_*` kernel call in statement position. It cannot prove delivery, but
//     it covers every site — including one added tomorrow.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sky-app/rt/telemetry"
)

// ringCount is how many entries in the telemetry ring `logEmit` writes to carry
// `want`.
//
// A COUNT, not a presence check, and not a slice from a "before" length.
//
// Both simpler forms were tried and both were wrong here. `RecentLogs` returns
// newest-first, so `all[mark:]` reads the wrong end of the ring and reports the
// previous case's message as this one's. And a presence check that requires the
// message to be ABSENT beforehand fails in a full-package run, where other tests
// have already exercised the same warning paths — it turns a shared ring into a
// test-ordering dependency. The delta is what this gate actually means: THIS
// call emitted it.
func ringCount(want string) int {
	n := 0
	for _, e := range telemetry.Default().RecentLogs(0) {
		if strings.Contains(e.Message, want) {
			n++
		}
	}
	return n
}

func TestTheRuntimesOwnWarningsReachTheLog(t *testing.T) {
	cases := []struct {
		name string
		want string
		run  func(t *testing.T)
	}{
		{
			name: "an unlimited pool",
			want: "UNLIMITED",
			run: func(t *testing.T) {
				t.Setenv("SKY_DB_MAX_OPEN_CONNS", "0")
				_ = resolveDbPoolConfig("pgx")
			},
		},
		{
			name: "a pool knob SQLite must ignore",
			want: "ignored on SQLite",
			run: func(t *testing.T) {
				t.Setenv("SKY_DB_MAX_OPEN_CONNS", "16")
				_ = resolveDbPoolConfig("sqlite")
			},
		},
		{
			name: "an unparseable integer",
			want: "is not an integer",
			run: func(t *testing.T) {
				t.Setenv("SKY_DB_MAX_OPEN_CONNS", "lots")
				_ = resolveDbPoolConfig("pgx")
			},
		},
		{
			// The eleventh site, and the one the class gate below was carrying as
			// its single exemption. Same defect, and the same consequence: the
			// pragma loop deliberately does NOT fail the store when a pragma is
			// rejected — a filesystem that refuses WAL still works in rollback-
			// journal mode — so the warning was the only thing that said the
			// session store is running without the concurrency configuration
			// v0.17.10 added to stop it stalling under navigation load.
			name: "a session-store pragma the backing file rejects",
			want: "live session store: PRAGMA",
			run: func(t *testing.T) {
				// A DIRECTORY is not a database file. `sql.Open` is lazy, so the
				// first thing to touch it is the pragma loop and every pragma
				// fails; `newSQLiteStore` then returns an error at CREATE TABLE.
				// The error is not what is under test — the warnings emitted
				// before it are, and this is the path that reaches them.
				if _, err := newSQLiteStore(t.TempDir(), time.Minute, 0); err == nil {
					t.Fatal("precondition: opening a session store on a directory should fail; " +
						"without a failing pragma the loop never warns and this case proves nothing")
				}
			},
		},
		{
			name: "an unrecognised isolation level",
			want: "not a recognised isolation level",
			run: func(t *testing.T) {
				t.Setenv("SKY_DB_ISOLATION", "very serializable")
				_ = resolveDbTxConfig("pgx")
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withServerlessEnv(t, nil)
			before := ringCount(c.want)
			c.run(t)
			if after := ringCount(c.want); after <= before {
				t.Fatalf("the runtime decided something on the operator's behalf and said "+
					"nothing.\nExpected the log to gain a line containing %q; the count went "+
					"%d → %d.\n`Log_warn` returns a Task — a `func() any`. Called as a bare "+
					"statement it builds the thunk and drops it, so the warning is constructed "+
					"and never emitted.",
					c.want, before, after)
			}
		})
	}
}

// TestNoRuntimeSourceDropsALogKernelThunk is the class gate.
//
// It parses every non-test .go file in rt and fails on a `Log_*` kernel call
// used as an expression STATEMENT — the shape that discards the returned Task.
// The behavioural gate above can only cover the paths it happens to run; this
// covers the ones nobody thought to.
func TestNoRuntimeSourceDropsALogKernelThunk(t *testing.T) {
	// The kernels that return a Task rather than logging directly.
	kernels := map[string]bool{
		"Log_debug": true, "Log_info": true, "Log_warn": true, "Log_error": true,
		"Log_debugWith": true, "Log_infoWith": true, "Log_warnWith": true, "Log_errorWith": true,
	}
	// NOTHING is exempt, and the empty map is deliberate rather than left over.
	//
	// It carried exactly one entry — `live_store.go`'s failed-pragma warning,
	// deferred because another agent held that file in this cycle. That swap has
	// now been made and the entry deleted in the SAME change, which is what this
	// list's reverse check below demands: it is asserted in both directions, so a
	// fixed file left on the list turns the gate red rather than quietly
	// narrowing it. The map stays, and stays documented, so the next deferral has
	// somewhere to go that comes with the same expiry.
	pending := map[string]string{}
	var offenders []string
	var sawPending []string
	roots := []string{".", "telemetry", "dbshare", "hub", "jobs"}
	fset := token.NewFileSet()
	for _, dir := range roots {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				stmt, ok := n.(*ast.ExprStmt)
				if !ok {
					return true
				}
				call, ok := stmt.X.(*ast.CallExpr)
				if !ok {
					return true
				}
				ident, ok := call.Fun.(*ast.Ident)
				if !ok || !kernels[ident.Name] {
					return true
				}
				if _, exempt := pending[name]; exempt {
					sawPending = append(sawPending, name)
					return true
				}
				offenders = append(offenders,
					fset.Position(stmt.Pos()).String()+": "+ident.Name)
				return true
			})
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("%d call(s) to a Log_* KERNEL in statement position. Each returns a Task "+
			"(a `func() any`) which is then discarded, so the message is built and never "+
			"emitted. Use the internal `rtWarn` / `rtInfo` helpers, which call logEmit "+
			"directly:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
	// The exemption list must describe reality in BOTH directions. An entry for
	// a file that no longer has the defect is an exemption nobody will ever
	// remove, and this gate would then be quietly narrower than it reads.
	for file, why := range pending {
		found := false
		for _, s := range sawPending {
			if s == file {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is on the pending list (%s) but no longer contains a dropped "+
				"Log_* thunk — delete the entry; an exemption for a fixed file makes this "+
				"gate narrower than it looks", file, why)
		}
	}
}
