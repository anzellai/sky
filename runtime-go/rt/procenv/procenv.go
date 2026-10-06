// Package procenv is a Sky program's own configuration table: the values the
// program sets for ITSELF, kept apart from the process environment.
//
// # The defect this closes (v0.27.7)
//
// A Sky program configures itself through environment-variable NAMES: the
// generated `init()` seeds `<PREFIX>_LIVE_PORT`, `<PREFIX>_LIVE_TTL` and every
// sky.toml-derived default (`rt.SetSkyDefault`), `rt.ApplyConfig` writes each
// `Sky.Config.withX` value, `--embed` hands its cluster's DSN to the app as
// `<PREFIX>_DB_PATH` / `DATABASE_URL`, and the embedded Sky Console is told its
// parent URL, logout route and per-boot internal token the same way.
//
// All of those used to be `os.Setenv`. The process environment is what EVERY
// child process inherits, so each of them leaked into every program the app
// started (`Process.spawn`, `Process.run`, any Go library that execs). A child
// Sky program then read the parent's seeded `SKY_LIVE_PORT=8000` as an
// OPERATOR override — the top layer of the precedence, above its own builder
// and its own sky.toml — and listened on the parent's port. A child also
// received the parent's console internal token, which authenticates
// `/_sky/console/api/*`.
//
// # The rule
//
// The process environment holds only what the OPERATOR set: the shell, a
// `.env` file, a container's env, or the program's own explicit
// `System.setenv`. That is what children inherit, and it is still inherited
// (operator intent reaches the child). Everything the program sets for itself
// lives in this table, which no child can see.
//
// Every in-process read goes through [Lookup] / [Getenv]: the operator's
// value when there is one, else the table's. So a seeded default is visible to
// the program that seeded it — to `System.getenv`, to the runtime's own readers
// — exactly as before, and to nothing else.
package procenv

import (
	"os"
	"sync"
)

// Source says who set a table entry. It is what lets the Sky.Live precedence
// (`configLayers`) rank a value: operator env > builder (Applied) > seeded.
type Source uint8

const (
	// Seeded: a default the generated prologue seeded from sky.toml or from
	// the compiler's fallback (`rt.SetSkyDefault`). The lowest layer.
	Seeded Source = iota + 1
	// Applied: a `Sky.Config.withX` value written by `rt.ApplyConfig`. The
	// builder layer.
	Applied
	// Runtime: a value the runtime derived while running (the `--embed` DSN,
	// the embedded console's parent URL and internal token).
	Runtime
)

type entry struct {
	value  string
	source Source
}

var (
	mu    sync.RWMutex
	table = map[string]entry{}
)

// Lookup is the in-process view of an environment variable: the operator's
// value when the process environment has a non-empty one, else the program's
// own value from the table, else whatever the environment holds (an empty
// value, or nothing).
//
// An EMPTY operator value does not hide the program's own value. Every Sky
// setting already reads empty as "not set" (configLayers), and the runtime used
// to overwrite an empty variable when it set its own value (`--embed` handing
// its DSN to an app started with `DATABASE_URL=`), so this keeps that meaning.
func Lookup(name string) (string, bool) {
	osv, osok := os.LookupEnv(name)
	if osok && osv != "" {
		return osv, true
	}
	mu.RLock()
	e, ok := table[name]
	mu.RUnlock()
	if ok {
		return e.value, true
	}
	return osv, osok
}

// Getenv is [Lookup] with os.Getenv's "missing is empty" shape.
func Getenv(name string) string {
	v, _ := Lookup(name)
	return v
}

// Set records the program's own value for name. It never touches the process
// environment, so no child process sees it.
func Set(name, value string, src Source) {
	mu.Lock()
	table[name] = entry{value: value, source: src}
	mu.Unlock()
}

// SetDefault sets name only when [Lookup] finds nothing — neither an operator
// value nor an earlier table entry — and reports whether it set it. The first
// default wins, which is the set-if-unset contract the generated prologue
// relies on (sky.toml values are seeded before the compiler's fallbacks).
func SetDefault(name, value string, src Source) bool {
	if _, ok := os.LookupEnv(name); ok {
		return false
	}
	mu.Lock()
	defer mu.Unlock()
	if _, ok := table[name]; ok {
		return false
	}
	table[name] = entry{value: value, source: src}
	return true
}

// SourceOf reports who set the value [Lookup] currently returns for name. ok
// is false when the value is the operator's (the process environment has a
// non-empty value for the name) or when nothing set it.
func SourceOf(name string) (Source, bool) {
	if v, ok := os.LookupEnv(name); ok && v != "" {
		return 0, false
	}
	mu.RLock()
	e, ok := table[name]
	mu.RUnlock()
	return e.source, ok
}

// Clear drops name's table entry when it was set by src.
func Clear(name string, src Source) {
	mu.Lock()
	if e, ok := table[name]; ok && e.source == src {
		delete(table, name)
	}
	mu.Unlock()
}

// Delete drops name's table entry, whoever set it.
func Delete(name string) {
	mu.Lock()
	delete(table, name)
	mu.Unlock()
}
