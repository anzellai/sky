package rt

import "sync"

// LazyCaf is the memo cell behind a top-level Sky binding (a CAF — Constant
// Applicative Form). A zero-parameter top-level binding is a single VALUE, not
// a function: `db = Task.run (Db.connect ())` is one handle, `apiKey =
// System.getenv "K" |> Task.run |> Result.withDefault ""` is one string. The
// compiler emits each such binding as
//
//	var Foo_bar__caf rt.LazyCaf[T]
//	func Foo_bar() T { return Foo_bar__caf.Get(func() T { <body> }) }
//
// so every reference resolves to the SAME value, computed exactly once, on
// first use. This is the "memoised handle" contract (CLAUDE.md Cardinal Rule
// 4): without it a `db` binding re-ran `Db.connect ()` on every query, opening
// a fresh *sql.DB pool per reference — a connection/handle leak, and (under
// WAL) a read-your-own-write hazard.
//
// Lazy (first-use), not init()-time, so a binding that reads runtime env
// (`System.getenv`) observes the value present when the program actually uses
// it, and CAF ordering resolves itself without an init dependency graph.
//
// Thread-safe: sync.Once guards the single computation; concurrent first-uses
// block until it completes, then all observe the cached value.
//
// The compiler NEVER memoises a self-referential binding (`conn = case … of
// Err _ -> conn`) — that would re-enter Once.Do and deadlock — so `compute`
// here is guaranteed not to call back into the same cell.
type LazyCaf[T any] struct {
	once sync.Once
	val  T
}

// Get returns the cell's value, running compute exactly once across all callers
// and all goroutines.
func (c *LazyCaf[T]) Get(compute func() T) T {
	c.once.Do(func() { c.val = compute() })
	return c.val
}
