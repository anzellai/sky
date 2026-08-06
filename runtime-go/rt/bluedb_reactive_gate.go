package rt

// bluedb_reactive_gate.go — Phase-4c RG#2 capability boot-gate (design §6.1 / phase4-grill-findings
// "RG#2 (BLOCKING)"). This is the SAFETY GATE that makes the "reactivity embedded-first; SQL =
// storage + post-v1 NOTIFY bridge" scoping decision SAFE rather than a silent defer.
//
// THE HAZARD. Embedded BlueDB is a single-writer LOCAL pebble store; sqlite is a local file. A
// process has NO intrinsic "replica count" signal (N replicas each with their own local dir are
// indistinguishable from one), so a multi-replica embedded/sqlite deploy that USES reactivity would
// boot green and then SILENTLY STALE — a tenant-A session on replica 2 never sees replica 1's write,
// because the write is in replica 1's store only. That is the exact cross-replica staleness the
// capability matrix (§6.2) claims is impossible.
//
// THE GATE (fail-closed, operator-asserted — parallels the SKY_CONSOLE_AUTH-must-be-set prod gate).
// Since topology is not runtime-detectable, we require an EXPLICIT operator assertion. When an app
// USES reactivity on a local single-writer data backend (embedded/sqlite) AND the runtime is in a
// non-dev env, booting REQUIRES SKY_DATA_REACTIVE_SCOPE=single-instance (settable via the
// `[data] reactiveScope = "single-instance"` sky.toml key, which the CLI maps to the env var). Absent
// the assertion → boot HARD-FATAL. Dev / unset env → WARN + allow (dev ergonomics preserved).
//
// The gate is about the DATA backend's replica scope — INDEPENDENT of the session store. A
// single-instance app that uses postgres SESSIONS for durability but embedded DATA is NOT
// false-fataled (its data backend is still single-writer-local, correctly gated on the scope
// assertion; its postgres session store is irrelevant to the data-staleness hazard).

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

// reactiveScopeEnv is the operator's single-instance assertion (design RG#2). The CLI maps the
// sky.toml `[data] reactiveScope` key onto this env var; process env wins (the standard precedence).
const reactiveScopeEnv = "SKY_DATA_REACTIVE_SCOPE"

// reactiveScopeSingleInstance is the accepted assertion value.
const reactiveScopeSingleInstance = "single-instance"

// isLocalSingleWriterBackend reports whether a data backend is a single-writer LOCAL store — the
// class the cross-replica-staleness hazard applies to (embedded pebble, sqlite file). A SHARED
// backend (postgres) is NOT in this class: its reactive multi-replica story is the §5 Redis-nudge
// path, gated separately, not by this assertion.
func isLocalSingleWriterBackend(backend string) bool {
	switch strings.ToLower(strings.TrimSpace(backend)) {
	case "embedded", "sqlite":
		return true
	}
	return false
}

// reactiveScopeAsserted reports whether the operator asserted single-instance (case/space-tolerant).
func reactiveScopeAsserted(scopeAssertion string) bool {
	return strings.EqualFold(strings.TrimSpace(scopeAssertion), reactiveScopeSingleInstance)
}

// reactiveCapabilityError is the PURE decision for the RG#2 boot-gate (unit-tested exhaustively in
// bluedb_reactive_gate_test.go — no os.Exit here so the matrix is directly assertable). It returns a
// non-nil error (⇒ the process MUST refuse to boot) EXACTLY when all of:
//   - the app uses reactivity (withReactive/liveInto present), AND
//   - the data backend is a local single-writer store (embedded/sqlite), AND
//   - the runtime is in a non-dev env (prod), AND
//   - the operator did NOT assert single-instance.
// Every other combination returns nil (boot proceeds): dev/unset env → allow (warn at the call
// site); a shared backend → not this gate's concern; the assertion present → allow; no reactivity →
// nothing to gate.
func reactiveCapabilityError(prod bool, scopeAssertion string, usesReactive bool, backend string) error {
	if !usesReactive {
		return nil
	}
	if !isLocalSingleWriterBackend(backend) {
		return nil // shared backend (postgres) or unknown — not the single-writer-local hazard
	}
	if !prod {
		return nil // dev / unset env — warn + allow (dev ergonomics)
	}
	if reactiveScopeAsserted(scopeAssertion) {
		return nil // operator asserted ONE replica — safe
	}
	return fmt.Errorf(
		"reactive Persist is single-instance only on the %s backend: a multi-replica deploy would "+
			"silently serve stale reads (each replica has its OWN local store; a write on one replica "+
			"never reaches a reactive session on another). Set %s=%s to assert this deployment runs "+
			"ONE replica, or move reactive DATA to postgres + a redis broker (the post-v1 cross-instance "+
			"path). Refusing to boot to avoid silent cross-replica staleness",
		backend, reactiveScopeEnv, reactiveScopeSingleInstance)
}

// reactiveDataBackendKind classifies the app's reactive DATA backend from its bindings. Today the
// only backend wired for reactive LIVE delivery is embedded (a KvConn handle resolves to an
// *EmbeddedBackend; a SqlConn passes -1 and does the initial fill only — see reactiveLoop). So a
// binding that resolves to an embedded backend ⇒ "embedded". Anything else ⇒ "" (not gated here):
// SQL reactive live-delivery is the documented post-v1 LISTEN/NOTIFY arm, and returning "" avoids
// false-fataling a postgres deploy. When the SQL reactive arm lands, this classifier gains the
// sqlite/postgres discrimination and the pure gate above already handles the "sqlite" string.
func (app *liveApp) reactiveDataBackendKind(model any) string {
	for _, b := range app.reactiveBindingsFor(model) {
		if _, ok := embeddedBackend(b.store); ok {
			return "embedded"
		}
	}
	return ""
}

// reactiveGateOnce guards the boot-gate so a per-session ensureReactiveStarted evaluates it EXACTLY
// once per process (the topology + env + backend are process-global facts).
var reactiveGateOnce sync.Once

// assertReactiveCapabilityOrExit runs the RG#2 boot-gate once. On a FATAL verdict it prints an
// actionable message + os.Exit(1) (mirroring AssertConsoleInvariantOrExit). In dev / unset env with
// an un-asserted single-instance-local reactive app it emits ONE warn (visibility without blocking
// dev). Called from ensureReactiveStarted — the first moment we know the app uses reactivity AND can
// resolve its data backend from a live model.
func (app *liveApp) assertReactiveCapabilityOrExit(model any) {
	reactiveGateOnce.Do(func() {
		usesReactive := app.reactiveBindings != nil
		if !usesReactive {
			return
		}
		backend := app.reactiveDataBackendKind(model)
		prod := productionFromEnv()
		scope := os.Getenv(reactiveScopeEnv)

		if err := reactiveCapabilityError(prod, scope, usesReactive, backend); err != nil {
			fmt.Fprintf(os.Stderr, "[sky.persist] FATAL: %s\n", err.Error())
			os.Exit(1)
		}

		// Non-fatal but worth surfacing: a single-instance-local reactive app in DEV without the
		// assertion is fine today, but WILL fatal the moment ENV becomes non-dev. Warn once so the
		// operator sets the assertion before the first staging/prod boot.
		if !prod && isLocalSingleWriterBackend(backend) && !reactiveScopeAsserted(scope) {
			logStructured("warn", "reactive.single-instance-unasserted",
				"backend", backend,
				"hint", "set "+reactiveScopeEnv+"="+reactiveScopeSingleInstance+
					" (or [data] reactiveScope) before a non-dev deploy; reactive on a local store is single-instance only")
		}
	})
}
