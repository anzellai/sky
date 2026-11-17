//go:build js

package rt

import "context"

// goCtx under wasm: a Sky.Spa client has no Live session stamp, no trace
// context and no SSR settle, so a goSky goroutine inherits nothing.
type goCtx struct{}

func captureGoCtx() goCtx { return goCtx{} }

func (goCtx) run(fn func()) { fn() }

// withTrace: there is no goroutine-local trace context under wasm, so a
// deadline context is not carried into the goroutine (task_deadline.go).
func (gc goCtx) withTrace(context.Context) goCtx { return gc }

// skyPanicStack: the wasm driver owns panic reporting; no stack is carried.
func skyPanicStack() []byte { return nil }
