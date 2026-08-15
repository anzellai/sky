package rt

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSessionStoreCloseIsIdempotent — the behavioural half of the contract.
//
// `SessionStore.Close` is a teardown method with more than one plausible
// caller: a shutdown hook, an explicit teardown in the app that owns the
// store, and a test harness that swaps a store out (see newIdleTestApp in
// live_idle_session_cookie_test.go, which closes app.store before replacing
// it). A bare `close(s.stop)` makes the SECOND call panic with "close of
// closed channel" — inside the shutdown window, which is precisely when a
// process must not die by panic. `jobs.Worker.Stop` fixed the identical shape
// with a `sync.Once`; this asserts the session stores did too.
//
// The `recover` is deliberate: without it the first backend to panic takes the
// whole test binary down and the remaining backends never report.
func TestSessionStoreCloseIsIdempotent(t *testing.T) {
	closeTwice := func(t *testing.T, s SessionStore) {
		t.Helper()
		if err := s.Close(); err != nil {
			t.Fatalf("first Close: %v", err)
		}
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("second Close panicked: %v\n"+
					"  Close must be idempotent — a bare close(s.stop) is not", r)
			}
		}()
		if err := s.Close(); err != nil {
			t.Fatalf("second Close returned an error: %v", err)
		}
	}

	t.Run("memory", func(t *testing.T) {
		closeTwice(t, newMemoryStore(time.Minute))
	})

	t.Run("sqlite", func(t *testing.T) {
		s, err := newSQLiteStore(filepath.Join(t.TempDir(), "sessions.db"), time.Minute, 0)
		if err != nil {
			t.Fatalf("newSQLiteStore: %v", err)
		}
		closeTwice(t, s)
	})

	t.Run("postgres", func(t *testing.T) {
		dsn := os.Getenv("SKY_TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("SKY_TEST_POSTGRES_DSN unset — skipping real-Postgres backend")
		}
		s, err := newPostgresStore(dsn, time.Minute, 0)
		if err != nil {
			t.Fatalf("newPostgresStore: %v", err)
		}
		closeTwice(t, s)
	})

	t.Run("redis", func(t *testing.T) {
		addr := os.Getenv("SKY_TEST_REDIS_ADDR")
		if addr == "" {
			t.Skip("SKY_TEST_REDIS_ADDR unset — skipping real-Redis backend")
		}
		s, err := newRedisStore(addr, time.Minute, 0)
		if err != nil {
			t.Fatalf("newRedisStore: %v", err)
		}
		closeTwice(t, s)
	})
}

// TestLiveStoreClosesLifecycleChannelsUnderOnce — the structural half.
//
// The behavioural test above proves the four backends that exist today. This
// one proves the CLASS: every `close(...)` in live_store.go sits inside a
// `sync.Once.Do`, so a fifth backend added next year cannot reintroduce the
// bare close that this pair of tests was written to kill. Without it, the
// docstring on SessionStore.Close asserting idempotency would be a claim with
// nothing comparing the code to it — the shape of defect that produced this
// fix in the first place.
func TestLiveStoreClosesLifecycleChannelsUnderOnce(t *testing.T) {
	const src = "live_store.go"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, src, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", src, err)
	}

	// The names declared `sync.Once` in this file. The barrier used to be
	// matched as "any call whose selector is spelled `.Do`", which is the same
	// name-vs-symbol weakness the dbshare accounting gate had, with the sign
	// reversed: an unrelated `.Do` (an http client, a rate limiter, a queue)
	// would have marked every close inside its function literal as guarded and
	// the gate would report clean over an unguarded close. `Do` is a common
	// enough method name that this is not a shape somebody would have to
	// invent.
	syncQ, syncDot := importQualifiers(f, "sync", "sync")
	onceNames := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		var typ ast.Expr
		var names []*ast.Ident
		switch d := n.(type) {
		case *ast.Field:
			typ, names = d.Type, d.Names
		case *ast.ValueSpec:
			typ, names = d.Type, d.Names
		default:
			return true
		}
		isOnce := false
		switch t := typ.(type) {
		case *ast.SelectorExpr:
			id, ok := t.X.(*ast.Ident)
			isOnce = ok && syncQ[id.Name] && t.Sel.Name == "Once"
		case *ast.Ident:
			isOnce = syncDot && t.Name == "Once"
		}
		if isOnce {
			for _, nm := range names {
				onceNames[nm.Name] = true
			}
		}
		return true
	})
	if len(onceNames) == 0 {
		t.Fatalf("%s declares no sync.Once — the gate cannot recognise the barrier it "+
			"is written against and would report whatever the file did", src)
	}

	// isOnceDo reports whether a call is `<a sync.Once>.Do(…)`.
	isOnceDo := func(call *ast.CallExpr) bool {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Do" {
			return false
		}
		switch recv := sel.X.(type) {
		case *ast.Ident:
			return onceNames[recv.Name]
		case *ast.SelectorExpr: // s.closeOnce.Do(…)
			return onceNames[recv.Sel.Name]
		}
		return false
	}

	var bare []string
	// walk descends n, flipping inOnce when it crosses a sync.Once `.Do(...)`
	// call — the barrier. A close() reached with inOnce false is unguarded.
	var walk func(n ast.Node, inOnce bool)
	walk = func(n ast.Node, inOnce bool) {
		if n == nil {
			return
		}
		ast.Inspect(n, func(c ast.Node) bool {
			if c == nil || c == n {
				return true
			}
			call, ok := c.(*ast.CallExpr)
			if !ok {
				return true
			}
			if isOnceDo(call) {
				for _, arg := range call.Args {
					walk(arg, true)
				}
				return false
			}
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "close" && !inOnce {
				bare = append(bare, fset.Position(call.Pos()).String())
			}
			return true
		})
	}
	walk(f, false)

	if len(bare) > 0 {
		t.Errorf("%s: %d channel close(s) outside a sync.Once.Do:", src, len(bare))
		for _, p := range bare {
			t.Errorf("  %s — a second Close here panics 'close of closed channel'", p)
		}
	}
}
