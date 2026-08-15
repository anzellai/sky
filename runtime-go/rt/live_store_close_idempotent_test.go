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

	var bare []string
	// walk descends n, flipping inOnce when it crosses a `<something>.Do(...)`
	// call — the sync.Once barrier. A close() reached with inOnce false is
	// unguarded.
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
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Do" {
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
